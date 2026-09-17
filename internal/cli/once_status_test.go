package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/store"

	"github.com/serhiileniv/every/internal/schedule"
)

// catchUpBackend is a scheduler that runs a missed trigger late, as systemd
// (Persistent=true) and Task Scheduler (StartWhenAvailable) do.
type catchUpBackend struct{ *stubBackend }

func (catchUpBackend) CatchesUpMissed() bool { return true }

func onceAt(t *testing.T, at time.Time) *schedule.Schedule {
	t.Helper()
	s, err := schedule.ParseAt(
		[]string{"once", at.Format("2006-01-02"), at.Format("15:04")}, at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The two schedulers genuinely differ, and calling a task lost on one that
// intends to run it is the bug this distinction exists for.
func TestOnceOverdueStatusFollowsTheScheduler(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local)
	past := onceAt(t, now.Add(-24*time.Hour))
	future := onceAt(t, now.Add(24*time.Hour))

	cases := []struct {
		name      string
		sched     *schedule.Schedule
		catchesUp bool
		running   bool
		want      string
	}{
		{"passed, scheduler drops it", past, false, false, "missed"},
		{"passed, scheduler catches up", past, true, false, "late"},
		{"passed, running", past, false, true, "running"},
		{"passed, running, catching up", past, true, true, "running"},
		{"still ahead", future, false, false, ""},
		{"still ahead, catching up", future, true, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := onceOverdue(tc.sched, now, tc.catchesUp, tc.running); got != tc.want {
				t.Errorf("onceOverdue = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every other schedule kind has no overdue answer at all.
func TestOnceOverdueIgnoresRecurringSchedules(t *testing.T) {
	now := time.Now()
	for _, spec := range [][]string{{"15m"}, {"day", "9am"}, {"monthly", "1st", "9am"}} {
		s, err := schedule.Parse(spec)
		if err != nil {
			t.Fatal(err)
		}
		if got := onceOverdue(s, now, false, false); got != "" {
			t.Errorf("onceOverdue(%v) = %q, want empty", spec, got)
		}
	}
}

// The STATUS column is the one people read. It used to show a stale `ok` from
// whatever the last manual `every run` did, while NEXT alone said `missed`.
func TestListStatusForAMissedOneShot(t *testing.T) {
	c, out := stubCLI(t)
	c.Now = func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local) }
	addOnceTask(t, c, "remind", c.Now().Add(-24*time.Hour))

	if err := c.list(nil); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); !containsField(got, "missed") {
		t.Errorf("list output has no missed status:\n%s", got)
	}
}

func TestListStatusForALateOneShot(t *testing.T) {
	c, out := stubCLI(t)
	c.Now = func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local) }
	addOnceTask(t, c, "remind", c.Now().Add(-24*time.Hour))
	c.Backend = catchUpBackend{c.Backend.(*stubBackend)}

	if err := c.list(nil); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !containsField(got, "late") {
		t.Errorf("list output has no late status:\n%s", got)
	}
	if containsField(got, "missed") {
		t.Errorf("list called a catching-up task missed:\n%s", got)
	}
}

// Paused wins: a paused task is doing exactly what was asked of it.
func TestPausedBeatsMissed(t *testing.T) {
	c, out := stubCLI(t)
	c.Now = func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local) }
	addOnceTask(t, c, "remind", c.Now().Add(-24*time.Hour))

	s, err := loadStore(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaused("remind", true); err != nil {
		t.Fatal(err)
	}

	if err := c.list(nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !containsField(got, "paused") {
		t.Errorf("list output:\n%s\nwant paused", got)
	}
}

// addOnceTask registers a one-shot in the stub CLI's store and scheduler.
func addOnceTask(t *testing.T, c *CLI, name string, at time.Time) {
	t.Helper()
	sched := onceAt(t, at)
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(name, &store.Task{
		Cmd: "echo hi", Schedule: sched.ToRecord(), Cwd: c.Dirs.Data, Quiet: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Backend.Write(name, sched); err != nil {
		t.Fatal(err)
	}
}

func loadStore(c *CLI) (*store.Store, error) { return store.Load(c.Dirs.Data) }

// containsField matches a whole column value, so "late" does not match inside
// another word.
func containsField(table, want string) bool {
	for _, line := range strings.Split(table, "\n") {
		for _, f := range strings.Fields(line) {
			if f == want {
				return true
			}
		}
	}
	return false
}

// A missed one-shot has no unit on purpose. Reporting that as "scheduler
// resource missing — re-create the task" turns a deliberate state into two
// problems with the wrong fix.
func TestDoctorDoesNotFaultADisarmedOneShot(t *testing.T) {
	c, out := stubCLI(t)
	c.Now = func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local) }
	addOnceTask(t, c, "remind", c.Now().Add(-24*time.Hour))
	// Disarmed, as migrate would leave it.
	if err := c.Backend.DeleteUnits("remind"); err != nil {
		t.Fatal(err)
	}

	err := c.doctor(nil)

	got := out.String()
	if !strings.Contains(got, "one-shot missed") {
		t.Errorf("doctor output:\n%s\nwant it to explain the missed one-shot", got)
	}
	if strings.Contains(got, "scheduler resource exists") {
		t.Errorf("doctor output:\n%s\nwant no resource check for a disarmed one-shot", got)
	}
	if err != nil {
		t.Errorf("doctor failed (%v) on a task in its intended state", err)
	}
}
