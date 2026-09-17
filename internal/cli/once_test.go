package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/store"
)

func pinClock(c *CLI, at time.Time) { c.Now = func() time.Time { return at } }

func taskNames(t *testing.T, c *CLI) []string {
	t.Helper()
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	return s.Tasks.Names()
}

// A one-shot is a normal task until its moment: `every run` beforehand (to
// check the command) leaves it in place. Once the moment has come, the run
// that fires it removes it from the store and the scheduler, keeping the log
// and ledger like rm does.
func TestOnceRetiresAfterItsMoment(t *testing.T) {
	c, out := stubCLI(t)
	stub := c.Backend.(*stubBackend)
	added := time.Date(2026, 9, 2, 10, 30, 0, 0, time.Local)
	pinClock(c, added)

	if code := c.Run([]string{"once", "tomorrow", "9am", "--name", "o", "--quiet", "--", "echo fired"}); code != 0 {
		t.Fatalf("add: exit %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "runs once at Thu 03 Sep 09:00, then removes itself") {
		t.Errorf("add output:\n%s", out.String())
	}

	// A pre-flight run: the moment has not come, so nothing is retired.
	out.Reset()
	if code := c.Run([]string{"run", "o"}); code != 0 {
		t.Fatalf("early run: exit %d\n%s", code, out.String())
	}
	if got := taskNames(t, c); len(got) != 1 {
		t.Fatalf("early run retired the task: %v", got)
	}
	if _, ok := stub.units["o"]; !ok {
		t.Fatal("early run removed the unit")
	}

	// The scheduled fire, a second after the instant.
	pinClock(c, time.Date(2026, 9, 3, 9, 0, 1, 0, time.Local))
	out.Reset()
	if code := c.Run([]string{"run", "o", "--json"}); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out.String())
	}
	var payload runPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("run --json emitted no object before retiring: %v\n%s", err, out.String())
	}
	if payload.Exit != 0 || !strings.Contains(payload.Output, "fired") {
		t.Errorf("payload: %+v", payload)
	}
	if got := taskNames(t, c); len(got) != 0 {
		t.Errorf("task still in the store after firing: %v", got)
	}
	if _, ok := stub.units["o"]; ok {
		t.Error("unit still registered after firing")
	}
	for _, f := range []string{filepath.Join(c.Dirs.Logs, "o.log"), filepath.Join(c.Dirs.Runs, "o.jsonl")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s gone after retiring: %v", f, err)
		}
	}

	out.Reset()
	if code := c.Run([]string{"log", "o"}); code != 0 || !strings.Contains(out.String(), "fired") {
		t.Errorf("log after retiring: exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := c.Run([]string{"exists", "o"}); code != 66 {
		t.Errorf("exists after retiring: exit %d", code)
	}
}

// The retire step re-reads the store under the lock: if the name now belongs
// to a different task (a `set` during the run), it is left alone.
func TestRetireLeavesAReplacedTaskAlone(t *testing.T) {
	c, out := stubCLI(t)
	stub := c.Backend.(*stubBackend)
	pinClock(c, time.Date(2026, 9, 2, 10, 30, 0, 0, time.Local))
	if code := c.Run([]string{"once", "tomorrow", "9am", "--name", "o", "--quiet", "--", "true"}); code != 0 {
		t.Fatalf("add: exit %d\n%s", code, out.String())
	}

	// A different instant than the one the run captured.
	c.retire("o", time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local), true)
	if got := taskNames(t, c); len(got) != 1 {
		t.Errorf("retire with a mismatched instant removed the task: %v", got)
	}
	if _, ok := stub.units["o"]; !ok {
		t.Error("retire with a mismatched instant removed the unit")
	}

	// Not a once task at all.
	if code := c.Run([]string{"15m", "--name", "iv", "--quiet", "--", "true"}); code != 0 {
		t.Fatalf("add interval: exit %d", code)
	}
	c.retire("iv", time.Time{}, true)
	if got := taskNames(t, c); len(got) != 2 {
		t.Errorf("retire touched a non-once task: %v", got)
	}
}

// A one-shot whose moment has passed cannot be resumed: launchd would arm it
// for the same date next year. rm is the way out, and the message says so.
func TestResumeRefusesAnExpiredOnce(t *testing.T) {
	c, out := stubCLI(t)
	pinClock(c, time.Date(2026, 9, 2, 10, 30, 0, 0, time.Local))
	if code := c.Run([]string{"once", "tomorrow", "9am", "--name", "o", "--quiet", "--", "true"}); code != 0 {
		t.Fatalf("add: exit %d\n%s", code, out.String())
	}
	if code := c.Run([]string{"pause", "o"}); code != 0 {
		t.Fatalf("pause: exit %d", code)
	}

	pinClock(c, time.Date(2026, 9, 4, 0, 0, 0, 0, time.Local))
	if code := c.Run([]string{"resume", "o"}); code != 64 {
		t.Errorf("resume of an expired once: exit %d, want 64", code)
	}
	if stderr := c.Stderr.(interface{ String() string }).String(); !strings.Contains(stderr, "every rm \"o\"") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if got := taskNames(t, c); len(got) != 1 {
		t.Errorf("failed resume changed the store: %v", got)
	}

	// Before the moment it resumes fine.
	pinClock(c, time.Date(2026, 9, 2, 12, 0, 0, 0, time.Local))
	if code := c.Run([]string{"resume", "o"}); code != 0 {
		t.Errorf("resume before the moment: exit %d", code)
	}
}

// list shows a stranded one-shot as missed rather than a question mark, and
// inspect exposes the instant.
func TestOnceIsVisibleInListAndInspect(t *testing.T) {
	c, out := stubCLI(t)
	pinClock(c, time.Date(2026, 9, 2, 10, 30, 0, 0, time.Local))
	if code := c.Run([]string{"once", "tomorrow", "9am", "--name", "o", "--quiet", "--", "true"}); code != 0 {
		t.Fatalf("add: exit %d\n%s", code, out.String())
	}

	out.Reset()
	if code := c.Run([]string{"inspect", "o", "--json"}); code != 0 {
		t.Fatalf("inspect: exit %d", code)
	}
	var view TaskView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Kind != "once" || view.At == nil || !strings.HasPrefix(*view.At, "2026-09-03T09:00:00") {
		t.Errorf("inspect view: kind=%s at=%v", view.Kind, view.At)
	}
	if view.Next == nil || *view.Next != *view.At {
		t.Errorf("inspect next=%v, want the instant", view.Next)
	}

	out.Reset()
	if code := c.Run([]string{"list"}); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	// Pinned at 2 Sep 10:30, so the instant is 22h30m out and NEXT renders
	// relative. The absolute instant is asserted above, against inspect's
	// --json, which is where an exact time belongs.
	if !strings.Contains(out.String(), "in 22h") {
		t.Errorf("list before the moment:\n%s", out.String())
	}

	pinClock(c, time.Date(2026, 9, 4, 0, 0, 0, 0, time.Local))
	out.Reset()
	if code := c.Run([]string{"list"}); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(out.String(), "missed") {
		t.Errorf("list after the moment should say missed:\n%s", out.String())
	}
}

func TestMonthlyAddPrintsNextRun(t *testing.T) {
	c, out := stubCLI(t)
	pinClock(c, time.Date(2026, 9, 2, 10, 30, 0, 0, time.Local))
	if code := c.Run([]string{"monthly", "1st", "9am", "--name", "m", "--quiet", "--", "true"}); code != 0 {
		t.Fatalf("add: exit %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "next run: Thu 01 Oct 09:00") {
		t.Errorf("add output:\n%s", out.String())
	}
	out.Reset()
	if code := c.Run([]string{"inspect", "m", "--json"}); code != 0 {
		t.Fatalf("inspect: exit %d", code)
	}
	var view TaskView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Kind != "monthly" || len(view.Entries) != 1 || view.Entries[0].Day == nil || *view.Entries[0].Day != 1 {
		t.Errorf("inspect view: %+v", view)
	}
}
