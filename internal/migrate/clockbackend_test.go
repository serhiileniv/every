package migrate

import (
	"encoding/binary"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/serhiileniv/every/internal/schedule"
)

// clockBackend behaves the way the Windows backend really does, which the plain
// fakeBackend cannot express: it stores its unit UTF-16LE-encoded, and it puts
// a clock-derived value inside it. Both are true of Task Scheduler, and between
// them they meant a rendered unit never equalled the one on disk.
type clockBackend struct {
	fakeBackend
	now time.Time
}

func (c *clockBackend) Render(name string, s *schedule.Schedule) (string, error) {
	return fmt.Sprintf("ARGV=%s run %s KIND=%s\n<StartBoundary>%s</StartBoundary>\n",
		c.launcher, name, string(s.Kind), c.now.Format(time.RFC3339)), nil
}

func (c *clockBackend) Write(name string, s *schedule.Schedule) error {
	c.written = append(c.written, name)
	body, _ := c.Render(name, s)
	return os.WriteFile(c.UnitPath(name), encodeUTF16(body), 0o644)
}

// CanonicalUnit is the seam the real TaskScheduler implements.
func (c *clockBackend) CanonicalUnit(unit string) string {
	if decoded, ok := decodeUTF16LE(unit); ok {
		unit = decoded
	}
	return testStartBoundaryRe.ReplaceAllString(unit, "<StartBoundary></StartBoundary>")
}

var testStartBoundaryRe = regexp.MustCompile(`(?s)<StartBoundary>.*?</StartBoundary>`)

func encodeUTF16(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

func decodeUTF16LE(s string) (string, bool) {
	b := []byte(s)
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE || len(b)%2 != 0 {
		return s, false
	}
	b = b[2:]
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i < len(b); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(b[i:]))
	}
	return string(utf16.Decode(units)), true
}

// A backend that encodes its unit, and stamps the clock into it, must not be
// repaired when nothing has actually changed.
//
// The stamp includes the store's mtime, so a pass runs after every write --
// which is by design. What was not by design is that each pass found every
// Windows task "stale" and re-registered it. Task Scheduler derives an interval
// trigger's phase from the moment of registration, so each pass pushed the next
// run out by however long had elapsed. Writing the store more often than a
// task's interval starved it completely, and `every list` went on reporting ok
// and a next-run time that had already passed.
func TestNoRepairWhenOnlyEncodingAndClockDiffer(t *testing.T) {
	dirs, fake := setup(t)
	b := &clockBackend{fakeBackend: *fake, now: time.Now()}
	addTask(t, dirs, "backup")

	// The unit this version would write, on disk exactly as Write leaves it.
	sched, err := schedule.Parse([]string{"day", "9am"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Write("backup", sched); err != nil {
		t.Fatal(err)
	}
	b.written = nil

	// Time passes, and something writes the store, so the stamp is stale and a
	// pass runs -- the ordinary case, not an upgrade.
	b.now = b.now.Add(37 * time.Second)

	res := Run(dirs, b, b.launcher, "0.5.1")
	if len(res.Repaired) != 0 {
		t.Errorf("repaired %v, want nothing: the unit is current, only its "+
			"encoding and clock stamp differ", res.Repaired)
	}
	if len(b.written) != 0 || len(b.enabled) != 0 || len(b.disabled) != 0 {
		t.Errorf("re-registered an unchanged task (written=%v enabled=%v disabled=%v); "+
			"on Task Scheduler that resets an interval trigger's phase",
			b.written, b.enabled, b.disabled)
	}
}

// The same backend must still notice a unit that is genuinely stale, or the
// 0.4 Ruby-argv repair silently stops happening.
func TestRepairStillHappensThroughCanonicalUnit(t *testing.T) {
	dirs, fake := setup(t)
	b := &clockBackend{fakeBackend: *fake, now: time.Now()}
	addTask(t, dirs, "backup")

	// A unit from an older every: the launcher argv is wrong.
	stale := "ARGV=/usr/bin/ruby /usr/local/bin/every run backup KIND=calendar\n" +
		"<StartBoundary>whenever</StartBoundary>\n"
	if err := os.WriteFile(b.UnitPath("backup"), encodeUTF16(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Run(dirs, b, b.launcher, "0.5.1")
	if len(res.Repaired) != 1 || res.Repaired[0] != "backup" {
		t.Fatalf("repaired %v, want [backup]", res.Repaired)
	}
	if len(b.enabled) != 1 || len(b.disabled) != 1 {
		t.Errorf("a repaired task must be re-registered: enabled=%v disabled=%v",
			b.enabled, b.disabled)
	}
}
