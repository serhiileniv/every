package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/schedule"
)

// rejectingBackend refuses to schedule anything, the way Task Scheduler
// refuses a sub-minute interval.
type rejectingBackend struct{ err error }

func (b *rejectingBackend) Write(string, *schedule.Schedule) error { return b.err }
func (b *rejectingBackend) Enable(string) error                    { return nil }
func (b *rejectingBackend) Disable(string) error                   { return nil }
func (b *rejectingBackend) DeleteUnits(string) error               { return nil }
func (b *rejectingBackend) Loaded(string) bool                     { return false }
func (b *rejectingBackend) LoadedNames() ([]string, error)         { return nil, nil }
func (b *rejectingBackend) ResourceExists(string) bool             { return false }
func (b *rejectingBackend) UnitPath(n string) string               { return "/dev/null/" + n }
func (b *rejectingBackend) Name() string                           { return "rejecting" }

func runAdd(t *testing.T, b backend.Backend) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	dir := t.TempDir()
	c := &CLI{
		Dirs:    paths.Dirs{Data: dir, Logs: dir + "/logs", Runs: dir + "/runs"},
		Stdout:  &out,
		Stderr:  &errBuf,
		Backend: b,
		Now:     time.Now,
	}
	code = c.Run([]string{"15s", "--name", "toofast", "--", "echo nope"})
	return code, out.String(), errBuf.String()
}

// A schedule the platform's scheduler cannot express must exit 64, not 1.
//
// The user typed something this machine cannot do; that is a bad argument, not
// a failure. The Ruby got this free by raising ArgumentError, which its CLI
// already rescued onto the exit-64 path. A plain Go error loses the
// distinction -- and did: `every 15s -- cmd` on Windows exited 1, with the
// explanation buried behind "could not schedule toofast:".
//
// Only the Windows end-to-end could catch it, since Task Scheduler is the one
// backend with such a limit. This pins the mapping so the next one is caught
// here, on any machine.
func TestUnsupportedScheduleExitsWithUsageCode(t *testing.T) {
	const msg = "Windows Task Scheduler supports interval schedules from 1m; 15s needs a future resident scheduler"
	code, _, stderr := runAdd(t, &rejectingBackend{
		err: &backend.UnsupportedScheduleError{Msg: msg},
	})

	if code != paths.ExitUsage {
		t.Errorf("exit = %d, want %d", code, paths.ExitUsage)
	}
	if !strings.Contains(stderr, msg) {
		t.Errorf("stderr = %q, want it to carry the explanation", stderr)
	}
	// Printed bare, not wrapped: "could not schedule toofast: ..." buries the
	// part the user has to read.
	if strings.Contains(stderr, "could not schedule") {
		t.Errorf("the explanation was wrapped: %q", stderr)
	}
	if !strings.Contains(stderr, "see: every help") {
		t.Errorf("stderr = %q, want the usage-error second line", stderr)
	}
}

// A genuine failure still exits 1, wrapped with which task it was.
func TestSchedulingFailureExitsOne(t *testing.T) {
	code, _, stderr := runAdd(t, &rejectingBackend{err: errors.New("launchctl exploded")})

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "could not schedule toofast") {
		t.Errorf("stderr = %q, want the task named", stderr)
	}
}

// A failed add must leave nothing behind -- no half-registered task that
// `list` shows and nothing will ever run.
func TestFailedAddRollsBackTheStore(t *testing.T) {
	var out, errBuf bytes.Buffer
	dir := t.TempDir()
	c := &CLI{
		Dirs:    paths.Dirs{Data: dir, Logs: dir + "/logs", Runs: dir + "/runs"},
		Stdout:  &out,
		Stderr:  &errBuf,
		Backend: &rejectingBackend{err: &backend.UnsupportedScheduleError{Msg: "nope"}},
		Now:     time.Now,
	}
	if code := c.Run([]string{"15s", "--name", "toofast", "--", "echo nope"}); code == 0 {
		t.Fatal("the add succeeded")
	}

	out.Reset()
	if code := c.Run([]string{"list"}); code != 0 {
		t.Fatalf("list exited %d", code)
	}
	if strings.Contains(out.String(), "toofast") {
		t.Errorf("the rejected task survived in the store:\n%s", out.String())
	}
}

// The type must survive wrapping, since the backend returns it through Write
// and the CLI recovers it with errors.As.
func TestUnsupportedScheduleSurvivesWrapping(t *testing.T) {
	inner := &backend.UnsupportedScheduleError{Msg: "no sub-minute here"}
	var target *backend.UnsupportedScheduleError
	if !errors.As(errors.Join(errors.New("context"), inner), &target) {
		t.Fatal("errors.As cannot recover it through a wrap")
	}
	if target.Msg != "no sub-minute here" {
		t.Errorf("Msg = %q", target.Msg)
	}
}
