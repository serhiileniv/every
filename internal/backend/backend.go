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
