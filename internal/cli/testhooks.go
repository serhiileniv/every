package cli

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
)

// Hidden subcommands, for the end-to-end scripts only.
//
// They are not features: they are absent from help, from the man page and from
// every completion script, and the double underscore is there so an
// accidental invocation reads as internal. They exist because two things the
// e2e suite must test have no side-effect-free path through the real CLI:
//
//   - Parsing a schedule without registering anything. The Ruby suite called
//     Schedule.parse in-process; a compiled binary has no equivalent, and
//     driving `every <sched> -- true` would register eleven real launchd
//     agents just to ask whether the grammar accepts a string.
//   - Seeding the store without touching the scheduler, which is how the
//     execution, ledger and durability sections stay fast and hermetic -- and
//     the only way to exercise the lock from five concurrent processes.
//
// Keeping them here rather than in a separate test binary means the e2e script
// drives exactly the code path users get, which is the entire point of it.

// parseProbe exits 0 if the tokens parse as a schedule, 64 if they do not.
// Nothing is written.
func (c *CLI) parseProbe(args []string) error {
	if _, err := schedule.Parse(args); err != nil {
		return &usageError{msg: err.Error()}
	}
	return nil
}

// seed writes one task straight into the store, under the same lock `add`
// takes, without generating or registering a unit.
//
//	every __seed <name> <command> [timeout-seconds]
func (c *CLI) seed(args []string) error {
	if len(args) < 2 {
		return invocationf("__seed <name> <command> [timeout-seconds]")
	}
	name, cmd := args[0], args[1]

	timeout := 0
	if len(args) > 2 && args[2] != "" {
		v, err := strconv.Atoi(args[2])
		if err != nil {
			return usagef("__seed: bad timeout %q", args[2])
		}
		timeout = v
	}

	sched, err := schedule.Parse([]string{"15m"})
	if err != nil {
		return err
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

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	return s.Add(name, &store.Task{
		Cmd: cmd, Schedule: sched.ToRecord(), Cwd: cwd,
		CreatedAt: c.Now().Format(time.RFC3339),
		Quiet:     true, Timeout: timeout,
	})
}

// lastExit prints the exit code of a task's most recent complete run, so the
// e2e script can assert on the ledger without a JSON parser.
func (c *CLI) lastExit(args []string) error {
	name, err := requireName(args, "__last-exit <name>")
	if err != nil {
		return err
	}
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}
	run, err := s.LastRun(name)
	if err != nil {
		return err
	}
	if run == nil {
		return noInput("no runs for %s", rubyInspect(name))
	}
	fmt.Fprintln(c.Stdout, run.Exit)
	return nil
}

// taskCount prints how many tasks the store holds.
func (c *CLI) taskCount() error {
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.Stdout, s.Tasks.Len())
	return nil
}
