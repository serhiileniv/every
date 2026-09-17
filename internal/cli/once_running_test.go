package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/store"
)

// killingBackend is the stub with launchd's one behaviour that matters here:
// unloading a job kills it. Disable records any unload that lands before the
// task's command has run -- a one-shot that launchd would have killed.
type killingBackend struct {
	*stubBackend
	marker string
	killed []string
}

func (k *killingBackend) Disable(name string) error {
	if _, err := os.Stat(k.marker); err != nil {
		k.killed = append(k.killed, name)
	}
	return nil
}

func addOnceCmd(t *testing.T, c *CLI, name, cmd string, at time.Time) {
	t.Helper()
	sched := onceAt(t, at)
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(name, &store.Task{
		Cmd: cmd, Schedule: sched.ToRecord(), Cwd: c.Dirs.Data, Quiet: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Backend.Write(name, sched); err != nil {
		t.Fatal(err)
	}
}

// The scheduled `every run` of a one-shot starts with the start-up repair pass.
// That pass used to see the moment had come and unload the job, which on
// launchd kills the very process about to run the command: one-shots never ran.
// Late fires count too -- launchd runs a trigger the machine slept through on
// wake, well past the grace.
func TestScheduledOneShotRunsBeforeAnythingUnloadsIt(t *testing.T) {
	for _, late := range []time.Duration{time.Second, 30 * time.Second, 10 * time.Minute, 72 * time.Hour} {
		t.Run(late.String(), func(t *testing.T) {
			c, _ := stubCLI(t)
			marker := filepath.Join(c.Dirs.Data, "fired")
			kb := &killingBackend{stubBackend: c.Backend.(*stubBackend), marker: marker}
			c.Backend = kb
			at := time.Date(2026, 9, 3, 9, 0, 0, 0, time.Local)
			addOnceCmd(t, c, "o", "echo fired > '"+marker+"'", at)

			pinClock(c, at.Add(late))
			if code := c.Run([]string{"run", "o"}); code != 0 {
				t.Fatalf("run: exit %d\n%s", code, c.Stderr.(*bytes.Buffer).String())
			}

			if len(kb.killed) != 0 {
				t.Errorf("unloaded before the command ran (launchd would kill it): %v", kb.killed)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Error("the one-shot's command never ran")
			}
			if got := taskNames(t, c); len(got) != 0 {
				t.Errorf("not retired after firing: %v", got)
			}
			if _, ok := kb.units["o"]; ok {
				t.Error("unit still registered after firing")
			}
			if store.Running(c.Dirs.Data, "o") {
				t.Error("the run lock outlived the run")
			}
		})
	}
}

// A long one-shot is running and the user types `every list` or `every doctor`.
// Both start with the repair pass; neither may unload -- kill -- it, and
// neither may call it missed, late or failing.
func TestCommandsDuringARunningOneShot(t *testing.T) {
	for _, catchesUp := range []bool{false, true} {
		name := "drops missed"
		if catchesUp {
			name = "catches up"
		}
		t.Run(name, func(t *testing.T) {
			c, out := stubCLI(t)
			marker := filepath.Join(c.Dirs.Data, "never")
			kb := &killingBackend{stubBackend: c.Backend.(*stubBackend), marker: marker}
			c.Backend = kb
			if catchesUp {
				c.Backend = catchUpKiller{kb}
			}
			at := time.Date(2026, 9, 3, 9, 0, 0, 0, time.Local)
			addOnceCmd(t, c, "long", "sleep 600", at)
			pinClock(c, at.Add(20*time.Minute))

			lock, err := store.HoldRun(c.Dirs.Data, "long")
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()

			for _, argv := range [][]string{{"list"}, {"doctor"}, {"inspect", "long"}, {"list", "--failing"}} {
				out.Reset()
				c.Run(argv)
				if len(kb.killed) != 0 {
					t.Fatalf("every %s unloaded the running one-shot: %v", strings.Join(argv, " "), kb.killed)
				}
				if _, ok := kb.units["long"]; !ok {
					t.Fatalf("every %s removed the running one-shot's unit", strings.Join(argv, " "))
				}
				got := out.String()
				for _, bad := range []string{"missed", "late"} {
					if containsField(got, bad) || strings.Contains(got, "one-shot missed") {
						t.Errorf("every %s calls a running one-shot %s:\n%s", strings.Join(argv, " "), bad, got)
					}
				}
				switch argv[0] {
				case "list":
					if len(argv) == 1 && !containsField(got, "running") {
						t.Errorf("list does not show it running:\n%s", got)
					}
					if len(argv) == 2 && !strings.Contains(got, "nothing failing") {
						t.Errorf("--failing includes a running one-shot:\n%s", got)
					}
				}
			}
		})
	}
}

type catchUpKiller struct{ *killingBackend }

func (catchUpKiller) CatchesUpMissed() bool { return true }

// launchd starts a job some way into its minute. Straight after the moment, a
// one-shot that has not been spawned yet is due, not missed.
func TestOneShotInsideItsGraceIsDue(t *testing.T) {
	c, out := stubCLI(t)
	at := time.Date(2026, 9, 3, 9, 0, 0, 0, time.Local)
	addOnceTask(t, c, "o", at)

	pinClock(c, at.Add(30*time.Second))
	if code := c.Run([]string{"list"}); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if got := out.String(); containsField(got, "missed") || !containsField(got, "now") {
		t.Errorf("30s after its moment, want due (NEXT now), got:\n%s", got)
	}
	if _, ok := c.Backend.(*stubBackend).units["o"]; !ok {
		t.Error("list unloaded a one-shot inside its grace")
	}

	out.Reset()
	pinClock(c, at.Add(3*time.Minute))
	if code := c.Run([]string{"list"}); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if got := out.String(); !containsField(got, "missed") {
		t.Errorf("3m after its moment, want missed, got:\n%s", got)
	}
}

// systemd and Task Scheduler still intend to run a late one-shot: it is not a
// task needing attention.
func TestFailingExcludesALateOneShot(t *testing.T) {
	c, out := stubCLI(t)
	c.Backend = catchUpBackend{c.Backend.(*stubBackend)}
	at := time.Date(2026, 9, 3, 9, 0, 0, 0, time.Local)
	addOnceTask(t, c, "o", at)
	pinClock(c, at.Add(24*time.Hour))

	if code := c.Run([]string{"list"}); code != 0 || !containsField(out.String(), "late") {
		t.Fatalf("setup: want a late one-shot, exit %d:\n%s", code, out.String())
	}
	out.Reset()
	if code := c.Run([]string{"list", "--failing"}); code != 0 {
		t.Fatalf("list --failing: exit %d", code)
	}
	if !strings.Contains(out.String(), "nothing failing") {
		t.Errorf("--failing includes a late one-shot:\n%s", out.String())
	}
}

// launchd's plist has no year, so a one-shot whose unit survived a year fires
// again on the same date. That fire must not run a year-old command. Until the
// fix above, the run's own start-up pass killed it -- by accident, and silently.
// A merely late run, including a manual one, still runs.
func TestYearLateOneShot(t *testing.T) {
	at := time.Date(2025, 9, 3, 9, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name      string
		now       time.Time
		catchesUp bool
		runs      bool
	}{
		{"a year late on launchd", at.AddDate(1, 0, 0), false, false},
		{"a year and a month late on launchd", at.AddDate(1, 1, 0), false, false},
		{"300 days late on launchd", at.AddDate(0, 0, 300), false, true},
		{"a year late where the scheduler catches up", at.AddDate(1, 0, 0), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := stubCLI(t)
			stub := c.Backend.(*stubBackend)
			if tc.catchesUp {
				c.Backend = catchUpBackend{stub}
			}
			marker := filepath.Join(c.Dirs.Data, "fired")
			addOnceCmd(t, c, "o", "echo fired > '"+marker+"'", at)
			pinClock(c, tc.now)

			code := c.Run([]string{"run", "o"})
			_, err := os.Stat(marker)
			if ran := err == nil; ran != tc.runs {
				t.Fatalf("command ran = %v, want %v (exit %d)", ran, tc.runs, code)
			}
			if tc.runs {
				if code != 0 || len(taskNames(t, c)) != 0 {
					t.Errorf("exit %d, tasks %v; want a normal fire and retire", code, taskNames(t, c))
				}
				return
			}
			if code == 0 {
				t.Error("exit 0 for a run that was refused")
			}
			if _, ok := stub.units["o"]; ok {
				t.Error("unit left in place; it fires again next year")
			}
			if got := taskNames(t, c); len(got) != 1 {
				t.Errorf("store = %v, want the task kept so list reports it missed", got)
			}
			msg := c.Stderr.(*bytes.Buffer).String()
			if !strings.Contains(msg, "missed") || !strings.Contains(msg, "every rm o") {
				t.Errorf("stderr does not explain the refusal:\n%s", msg)
			}
		})
	}
}

// inspect and list must agree about a one-shot. inspect used to say
// "unscheduled" for a disarmed one-shot that list called missed.
func TestInspectStatusMatchesListForOneShots(t *testing.T) {
	at := time.Date(2026, 9, 3, 9, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name    string
		running bool
		want    string
	}{
		{"missed", false, "missed"},
		{"running", true, "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, out := stubCLI(t)
			addOnceTask(t, c, "o", at)
			pinClock(c, at.Add(time.Hour))
			if tc.running {
				lock, err := store.HoldRun(c.Dirs.Data, "o")
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}

			if code := c.Run([]string{"inspect", "o", "--json"}); code != 0 {
				t.Fatalf("inspect: exit %d", code)
			}
			var view TaskView
			if err := json.Unmarshal(out.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if view.Status != tc.want {
				t.Errorf("inspect status = %q, want %q", view.Status, tc.want)
			}
			out.Reset()
			c.Run([]string{"list"})
			if !containsField(out.String(), tc.want) {
				t.Errorf("list disagrees:\n%s", out.String())
			}
		})
	}
}
