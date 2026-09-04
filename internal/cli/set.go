package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
)

// set adds a task, or updates one that already exists.
//
//	every set <schedule> --name <n> [flags] -- <command>
//
// It exists because the alternative races with itself. Without it a program
// has to write:
//
//	every rm foo 2>/dev/null; every day 9am --name foo -- cmd
//
// and between those two commands the task does not exist. A concurrent `list`
// sees it gone; if the second command fails -- a scheduler refusing, a
// schedule this platform cannot express, exactly the failures that cannot be
// pre-checked -- it is gone permanently.
//
// set holds the store lock across the whole operation and never leaves the
// store without the task: on failure the previous unit is put back and
// re-registered, so the task is either entirely the old one or entirely the
// new one.
//
// Two behaviours that differ from add, deliberately:
//
//   - It PRESERVES run history. add clears it, because a re-used name is a new
//     task; an update to an existing one is not, and a program adjusting a
//     schedule should not silently destroy the record of whether the thing has
//     been working.
//   - add still refuses a duplicate name. Changing that would alter an
//     invocation that already had a defined answer, and the refusal is a
//     useful guard for a person typing by hand.
func (c *CLI) set(argv []string) error {
	asJSON := wantsJSON(argv)

	spec, err := c.parseAddSpec(argv, "set")
	if err != nil {
		return err
	}
	if !spec.hasName {
		return coded(CodeUsage, "", "set needs --name (it identifies which task to update)")
	}

	lock, err := store.AcquireLock(c.Dirs.Data)
	if err != nil {
		return err
	}
	defer lock.Close()

	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}

	previous, existed := s.Tasks.Get(spec.name)
	var previousUnit []byte
	if existed {
		// Read the old unit before overwriting it, so the rollback has
		// something to restore rather than something to regenerate -- what is
		// on disk is the truth, and regenerating could differ if the previous
		// version wrote it.
		if raw, rErr := os.ReadFile(c.Backend.UnitPath(spec.name)); rErr == nil {
			previousUnit = raw
		}
	}

	task := &store.Task{
		Cmd: spec.cmd, Schedule: spec.sched.ToRecord(), Cwd: spec.cwd,
		CreatedAt: c.Now().Format(time.RFC3339),
		Paused:    false, Quiet: spec.quiet, Timeout: spec.timeout, OnFail: spec.onFail,
	}
	if existed {
		// Keep the creation time: the task is being updated, not replaced, and
		// resetting it would make "how long has this been running" unanswerable.
		task.CreatedAt = previous.CreatedAt
	} else {
		if err := c.resetHistory(spec.name); err != nil {
			return err
		}
	}

	if err := c.schedule(spec.name, spec.sched); err != nil {
		c.restore(spec.name, existed, previousUnit)
		var ue *usageError
		if errors.As(err, &ue) {
			return err
		}
		var unsupported *backend.UnsupportedScheduleError
		if errors.As(err, &unsupported) {
			return &usageError{msg: unsupported.Error(), code: CodeUnsupportedSchedule, name: spec.name}
		}
		return &exitError{
			code: 1, msg: fmt.Sprintf("could not schedule %s: %v", spec.name, err),
			errorCode: CodeSchedulerFailed, name: spec.name,
		}
	}

	// The store is written LAST. If anything above failed the registry still
	// describes what the scheduler actually has.
	if err := s.Add(spec.name, task); err != nil {
		c.restore(spec.name, existed, previousUnit)
		return err
	}

	if asJSON {
		view, vErr := c.taskViewFrom(s, spec.name, task)
		if vErr != nil {
			return vErr
		}
		return emitJSON(c.Stdout, view)
	}

	verb := "created"
	if existed {
		verb = "updated"
	}
	fmt.Fprintf(c.Stdout, "%s %s %s: %s — %s\n",
		c.Color.Green("✓"), verb, spec.name, spec.sched.Raw, spec.cmd)
	c.printWhenItRuns(spec.sched)
	fmt.Fprintf(c.Stdout, "  output:   runs in the background → see it with `every log %s`\n", spec.name)
	return nil
}

// restore puts back whatever was there before a failed set.
//
// Best effort by necessity: the scheduler has already refused once, so a
// second failure here is reported by the error the caller is about to return
// rather than replacing it with a less useful one.
func (c *CLI) restore(name string, existed bool, previousUnit []byte) {
	if !existed {
		_ = c.Backend.Disable(name)
		_ = c.Backend.DeleteUnits(name)
		return
	}
	if previousUnit == nil {
		return
	}
	if err := os.WriteFile(c.Backend.UnitPath(name), previousUnit, 0o644); err != nil {
		return
	}
	_ = c.Backend.Enable(name)
}

// taskViewFrom builds a view from a store already loaded and locked, so set
// does not have to drop its lock to describe what it just wrote.
func (c *CLI) taskViewFrom(s *store.Store, name string, task *store.Task) (*TaskView, error) {
	sched, err := schedule.FromRecord(task.Schedule)
	if err != nil {
		return nil, err
	}
	last, _ := s.LastRun(name)

	view := &TaskView{
		Name: name, Schedule: task.Schedule.Raw, Command: task.Cmd,
		Cwd: task.Cwd, CreatedAt: task.CreatedAt,
		Paused: task.Paused, Quiet: task.Quiet, Timeout: task.Timeout,
		OnFail: task.OnFail, Kind: task.Schedule.Kind,
		UnitPath: c.Backend.UnitPath(name), Scheduled: true,
		Entries: sched.Entries,
	}
	if sched.Kind == schedule.Interval {
		iv := sched.Interval.Int64()
		view.Interval = &iv
	}
	var lastExit *int
	if last != nil {
		e := last.Exit
		lastExit = &e
		view.Last = &runView{At: last.At, Exit: last.Exit, Seconds: last.Dur}
	}
	view.Status = taskStatus(task.Paused, true, lastExit)
	if iso := c.nextISO(sched, last); iso != "" {
		view.Next = &iso
	}
	return view, nil
}

// printWhenItRuns is the shared "next run" / "runs every" line.
func (c *CLI) printWhenItRuns(sched *schedule.Schedule) {
	if sched.Kind == schedule.Calendar {
		if next := sched.NextRun(c.Now()); !next.IsZero() {
			fmt.Fprintf(c.Stdout, "  next run: %s\n", next.Format("Mon 02 Jan 15:04"))
		}
		return
	}
	fmt.Fprintf(c.Stdout, "  runs every %s while the machine is awake\n", sched.HumanInterval())
}
