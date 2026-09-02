package backend

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestSystemdUnitsMatchGolden(t *testing.T) {
	s := NewSystemd(goldenCfg())
	for slug, sched := range loadSchedules(t) {
		t.Run(slug, func(t *testing.T) {
			wantService, err := os.ReadFile(goldenRoot(t, "systemd", slug+".service"))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := s.ServiceUnit(slug), dropRubyArgv(string(wantService)); got != want {
				t.Errorf("service mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}

			wantTimer, err := os.ReadFile(goldenRoot(t, "systemd", slug+".timer"))
			if err != nil {
				t.Fatal(err)
			}
			if got := s.TimerUnit(slug, sched); got != string(wantTimer) {
				t.Errorf("timer mismatch\n--- got ---\n%s\n--- want ---\n%s", got, wantTimer)
			}
		})
	}
}

// The clock scripts/golden.rb pinned. StartBoundary is derived from it, so the
// Task Scheduler fixtures only reproduce at this instant in this zone.
func goldenNow(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("zoneinfo unavailable: %v", err)
	}
	now, err := time.ParseInLocation(time.RFC3339, "2026-09-02T10:30:00-04:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func goldenTaskScheduler(t *testing.T) *TaskScheduler {
	t.Helper()
	cfg := goldenCfg()
	// The fixtures pin the shim-branch launcher, whose .cmd suffix is what
	// selected that branch in the Ruby. The token is opaque; what matters is
	// that both sides use the same one. See scripts/golden.rb.
	cfg.Launcher = goldenLauncher + ".cmd"
	w := NewTaskScheduler(cfg)
	w.Now = func() time.Time { return goldenNow(t) }
	// The account scripts/golden.rb pinned via USERNAME and USERDOMAIN.
	w.User = func() (string, error) { return `GOLDEN\goldenuser`, nil }
	return w
}

func TestTaskXMLMatchesGolden(t *testing.T) {
	w := goldenTaskScheduler(t)
	// COMSPEC reaches the generated Command, so it has to be pinned too.
	t.Setenv("COMSPEC", "cmd.exe")

	for slug, sched := range loadSchedules(t) {
		t.Run(slug, func(t *testing.T) {
			raw, err := os.ReadFile(goldenRoot(t, "taskschd", slug+".xml"))
			if os.IsNotExist(err) {
				t.Skip("no fixture: sub-minute intervals are rejected on Windows")
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.TaskXML(slug, sched)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(raw) {
				t.Errorf("task XML mismatch\n--- got ---\n%s\n--- want ---\n%s", got, raw)
			}
		})
	}
}

// The encoded bytes, not just the text. 0.3.0 shipped a Windows backend that
// could not register a single task because this file went out as plain UTF-8:
// MSXML decoded it as ANSI, hit the encoding declaration and refused.
func TestTaskXMLIsUTF16WithBOM(t *testing.T) {
	w := goldenTaskScheduler(t)
	t.Setenv("COMSPEC", "cmd.exe")

	scheds := loadSchedules(t)
	sched, ok := scheds["day-9am"]
	if !ok {
		t.Fatal("missing the day-9am fixture")
	}
	xml, err := w.TaskXML("day-9am", sched)
	if err != nil {
		t.Fatal(err)
	}

	path := t.TempDir() + "/task.xml"
	if err := writeUTF16(path, xml); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want, err := os.ReadFile(goldenRoot(t, "taskschd", "day-9am.utf16"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("encoded bytes differ: got %d bytes, want %d", len(got), len(want))
	}
	if len(got) < 2 || got[0] != 0xFF || got[1] != 0xFE {
		t.Errorf("missing the UTF-16LE BOM: first bytes %x", got[:min(4, len(got))])
	}

	// And it must decode back to the same text.
	units := make([]uint16, 0, (len(got)-2)/2)
	for i := 2; i+1 < len(got); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(got[i:i+2]))
	}
	if decoded := string(utf16.Decode(units)); decoded != xml {
		t.Error("the encoded file does not decode back to the source XML")
	}
}

// The declaration inside the document must agree with how it is written, or
// MSXML refuses the file.
func TestXMLDeclarationMatchesEncoding(t *testing.T) {
	w := goldenTaskScheduler(t)
	t.Setenv("COMSPEC", "cmd.exe")
	sched := loadSchedules(t)["15m"]
	xml, err := w.TaskXML("demo", sched)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(xml, `<?xml version="1.0" encoding="UTF-16"?>`) {
		t.Errorf("declaration = %.50q, want UTF-16", xml)
	}
}

// Written as an interpolating literal, '\every\' becomes an ESC byte and the
// query can never match anything.
func TestTaskStateScriptHasNoControlBytes(t *testing.T) {
	for i, r := range taskStateScript {
		if r < 0x20 && r != '\n' && r != '\t' {
			t.Errorf("control byte %#x at offset %d", r, i)
		}
	}
}

// Sub-minute intervals are refused rather than silently rounded up.
func TestSubMinuteIntervalRejected(t *testing.T) {
	w := goldenTaskScheduler(t)
	sched := loadSchedules(t)["15m"]
	if err := w.ValidateSchedule(sched); err != nil {
		t.Errorf("15m must be accepted: %v", err)
	}

	short := loadSchedules(t)["90s"]
	if err := w.ValidateSchedule(short); err != nil {
		t.Errorf("90s is a minute and a half and must be accepted: %v", err)
	}

	// Build one below the floor directly; the fixtures have no such case.
	tiny := *sched
	tiny.Interval = 15
	err := w.ValidateSchedule(&tiny)
	if err == nil {
		t.Fatal("15s must be rejected on Windows")
	}
	if !strings.Contains(err.Error(), "1m") {
		t.Errorf("message %q should name the 1m floor", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
