// Package backend is the scheduler abstraction: launchd on macOS, systemd user
// timers on Linux, Task Scheduler on Windows.
//
// Ported from lib/every/backend.rb.
package backend

import (
	"fmt"
	"runtime"

	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/schedule"
)

// Backend is what every platform implements.
//
// The units invoke `every run <name>`, never the command directly. That is
// deliberate and load-bearing: the runner is what captures output, exit code
// and duration into the data dir, and visibility is the product. A plist that
// ran the command itself would schedule fine and tell you nothing.
type Backend interface {
	// Write generates the scheduler's files for a task.
	Write(name string, s *schedule.Schedule) error
	// Enable registers and starts the task with the scheduler.
	Enable(name string) error
	// Disable unloads it, tolerating one that is already gone.
	Disable(name string) error
	// DeleteUnits removes the generated files.
	DeleteUnits(name string) error
	// Loaded reports whether the scheduler currently holds this task.
	Loaded(name string) bool
	// LoadedNames lists every task the scheduler holds, in one query rather
	// than one subprocess per task.
	LoadedNames() ([]string, error)
	// ResourceExists reports whether the task's definition is present. For
	// launchd and systemd that is a file; for Task Scheduler it is the service
	// itself, since the XML on disk is only a diagnostic copy.
	ResourceExists(name string) bool
	// UnitPath is the generated file, for diagnostics and cleanup.
	UnitPath(name string) string
	// Name is what doctor calls this scheduler in prose.
	Name() string
}

// Retirer is implemented by a backend that can remove a task from the
// scheduler FROM INSIDE that task's own run. A once task does this after it
// fires. The ordering hazard is platform-specific, which is why it is not
// simply Disable followed by DeleteUnits:
//
//   - launchd's bootout SIGTERMs the job, and the job is the `every run`
//     process doing the retiring, so it has to be the very last thing.
//   - systemd's disable fails once the unit file is gone, leaving an elapsed
//     timer loaded until logout, so there it has to come first.
//
// Everything durable (the store, the emitted output) must already be written
// when this is called; the result is best-effort.
type Retirer interface {
	Retire(name string) error
}

// Retire removes a task from the scheduler from inside its own run, using the
// backend's own ordering when it has one and the rm ordering otherwise.
func Retire(b Backend, name string) error {
	if r, ok := b.(Retirer); ok {
		return r.Retire(name)
	}
	if err := b.Disable(name); err != nil {
		return err
	}
	return b.DeleteUnits(name)
}

// CatchUpper is implemented by a backend whose scheduler runs a calendar
// trigger the machine slept or powered off through, instead of dropping it.
//
// The three schedulers genuinely differ, and a one-shot is where it shows:
// systemd timers carry Persistent=true and Task Scheduler tasks
// StartWhenAvailable, so both fire a missed one late; launchd drops anything
// it was powered off across. Reporting such a task as "missed" everywhere
// calls it lost on two platforms that intend to run it.
//
// An optional interface rather than a tenth method on Backend, following
// Retirer above: a capability only some schedulers have belongs where only
// they have to answer for it.
type CatchUpper interface{ CatchesUpMissed() bool }

// CatchesUpMissed reports whether this scheduler runs a missed calendar
// trigger late. False for a backend that does not claim otherwise, which is
// the safe default: it is the answer that keeps `every` from promising a run
// it cannot deliver.
func CatchesUpMissed(b Backend) bool {
	c, ok := b.(CatchUpper)
	return ok && c.CatchesUpMissed()
}

// UnsupportedScheduleError means the schedule is valid but this platform's
// scheduler cannot express it -- Task Scheduler has no reliable sub-minute
// repetition, for instance.
//
// It is a distinct type because it is a BAD-ARGUMENT error, not a failure: the
// user typed something this machine cannot do, so it belongs on the exit-64
// path with the other usage errors rather than the generic exit-1 one. The
// Ruby got this by raising ArgumentError, which its CLI already rescued; a
// plain Go error loses that distinction, and did.
type UnsupportedScheduleError struct{ Msg string }

func (e *UnsupportedScheduleError) Error() string { return e.Msg }

// Config is what every backend needs to generate a unit.
type Config struct {
	Dirs paths.Dirs
	// Launcher is the program the scheduler invokes. It is the path as
	// invoked, NOT symlink-resolved: recording the unresolved symlink is what
	// lets an upgrade reach already-scheduled tasks. See paths.ExpandPath.
	Launcher string
}

// Current returns the backend for this platform.
//
// darwin is checked before windows, matching the Ruby dispatch order.
func Current(cfg Config) (Backend, error) {
	switch runtime.GOOS {
	case "darwin":
		return NewLaunchd(cfg), nil
	case "windows":
		return NewTaskScheduler(cfg), nil
	case "linux":
		return NewSystemd(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
