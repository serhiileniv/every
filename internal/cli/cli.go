package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/runner"
	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
	"github.com/serhiileniv/every/internal/ui"
)

// CLI holds everything one invocation needs.
type CLI struct {
	Dirs    paths.Dirs
	Stdout  io.Writer
	Stderr  io.Writer
	Color   ui.Color
	Backend backend.Backend
	Now     func() time.Time
}

// Run dispatches one invocation and returns the process exit code.
//
// The exit codes are a contract, documented in the man page and asserted by the
// e2e suite: 0 ok, 64 usage or bad arguments, 66 no such task or log, 1
// anything else. Runs additionally surface 124 and 128+signum from the runner.
func (c *CLI) Run(argv []string) int {
	err := c.dispatch(argv)
	if err == nil {
		return 0
	}

	var exit *exitError
	if errors.As(err, &exit) {
		if exit.msg != "" {
			fmt.Fprintln(c.Stderr, exit.msg)
		}
		return exit.code
	}

	var invocation *invocationError
	if errors.As(err, &invocation) {
		fmt.Fprintf(c.Stderr, "usage: every %s\n", invocation.msg)
		return paths.ExitUsage
	}

	var usage *usageError
	if errors.As(err, &usage) {
		fmt.Fprintf(c.Stderr, "every: %s\n", usage.msg)
		fmt.Fprintln(c.Stderr, "see: every help")
		return paths.ExitUsage
	}

	// A corrupt registry prints bare, with no error-class suffix: Ruby used
	// abort() there, which bypassed its rescue, and the message is asserted.
	var corrupt *store.ErrCorrupt
	if errors.As(err, &corrupt) {
		fmt.Fprintf(c.Stderr, "every: %s\n", corrupt.Error())
		return 1
	}

	fmt.Fprintf(c.Stderr, "every: %s\n", err)
	return 1
}

// exitError carries an explicit exit code, for the paths that are not usage
// errors but still have a code of their own.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func noInput(format string, a ...any) error {
	return &exitError{code: paths.ExitNoInput, msg: "every: " + fmt.Sprintf(format, a...)}
}

func (c *CLI) dispatch(argv []string) error {
	if len(argv) == 0 {
		fmt.Fprint(c.Stdout, helpText(c.Dirs.Data))
		return nil
	}

	switch argv[0] {
	case "help", "-h", "--help":
		fmt.Fprint(c.Stdout, helpText(c.Dirs.Data))
		return nil
	case "version", "--version":
		fmt.Fprint(c.Stdout, versionText())
		return nil
	case "list", "ls":
		return c.list(argv[1:])
	case "log":
		return c.log(argv[1:])
	case "rm", "remove":
		return c.remove(argv[1:])
	case "pause":
		return c.setPaused(argv[1:], true)
	case "resume":
		return c.resume(argv[1:])
	case "doctor":
		return c.doctor()
	// Hidden test hooks; see testhooks.go for why they exist.
	case "__parse":
		return c.parseProbe(argv[1:])
	case "__seed":
		return c.seed(argv[1:])
	case "__last-exit":
		return c.lastExit(argv[1:])
	case "__count":
		return c.taskCount()
	case "run":
		if len(argv) < 2 {
			return invocationf("run <name>")
		}
		return c.runTask(argv[1])
	default:
		return c.add(argv)
	}
}

// requireName is the shared shape of the single-argument subcommands.
func requireName(args []string, usage string) (string, error) {
	if len(args) == 0 || args[0] == "" {
		return "", invocationf("%s", usage)
	}
	return args[0], nil
}

func (c *CLI) add(argv []string) error {
	// Everything before `--` is schedule tokens and flags; everything after is
	// the command, joined with single spaces the way cron does.
	sep := -1
	for i, tok := range argv {
		if tok == "--" {
			sep = i
			break
		}
	}
	if sep == -1 {
		return usagef("%s isn't a command, and there's no `--` before a task.\n"+
			"  to schedule:  every <when> -- <command>   (e.g. every day 9am -- brew update)\n"+
			"  commands:     list, log, run, pause, resume, rm, doctor, version",
			rubyInspect(argv[0]))
	}

	pre := append([]string{}, argv[:sep]...)
	cmdTokens := argv[sep+1:]
	if len(cmdTokens) == 0 {
		return usagef("missing command after --")
	}
	cmd := strings.Join(cmdTokens, " ")

	pre, quiet := removeFlag(pre, "--quiet")

	pre, explicitName, hasName, err := extractValueFlag(pre, "--name")
	if err != nil {
		return err
	}

	pre, timeoutRaw, hasTimeout, err := extractValueFlag(pre, "--timeout")
	if err != nil {
		return err
	}
	timeout := 0
	if hasTimeout {
		if timeout, err = parseDuration(timeoutRaw); err != nil {
			return err
		}
	}

	sched, err := schedule.Parse(pre)
	if err != nil {
		return &usageError{msg: err.Error()}
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

	var name string
	if hasName {
		name = sanitize(explicitName)
		if name == "" {
			return usagef("--name %s is empty after sanitizing (names allow a-z 0-9 . _ -)",
				rubyInspect(explicitName))
		}
		if len([]rune(name)) > maxName {
			return usagef("--name is too long (max %d chars)", maxName)
		}
		if _, exists := s.Tasks.Get(name); exists {
			return usagef("task %s already exists (every rm %s, or pick another --name)",
				rubyInspect(name), name)
		}
	} else {
		name = deriveName(cmd, func(n string) bool { _, ok := s.Tasks.Get(n); return ok })
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// A re-used name must not inherit the previous task's history.
	if err := c.resetHistory(name); err != nil {
		return err
	}

	task := &store.Task{
		Cmd: cmd, Schedule: sched.ToRecord(), Cwd: cwd,
		CreatedAt: c.Now().Format(time.RFC3339),
		Paused:    false, Quiet: quiet, Timeout: timeout,
	}
	if err := s.Add(name, task); err != nil {
		return err
	}

	// Roll the store back if the scheduler refuses, so a failed add leaves no
	// task that `list` shows but nothing will ever run.
	if err := c.schedule(name, sched); err != nil {
		_ = s.Remove(name)
		_ = c.Backend.DeleteUnits(name)
		var ue *usageError
		if errors.As(err, &ue) {
			return err
		}
		return fmt.Errorf("could not schedule %s: %w", name, err)
	}

	fmt.Fprintf(c.Stdout, "%s scheduled %s: %s — %s\n", c.Color.Green("✓"), name, sched.Raw, cmd)
	if sched.Kind == schedule.Calendar {
		if next := sched.NextRun(c.Now()); !next.IsZero() {
			fmt.Fprintf(c.Stdout, "  next run: %s\n", next.Format("Mon 02 Jan 15:04"))
		}
	} else {
		fmt.Fprintf(c.Stdout, "  runs every %s while the machine is awake\n", sched.HumanInterval())
	}
	fmt.Fprintf(c.Stdout, "  output:   runs in the background → see it with `every log %s`\n", name)
	return nil
}

func (c *CLI) schedule(name string, sched *schedule.Schedule) error {
	if err := c.Backend.Write(name, sched); err != nil {
		return err
	}
	return c.Backend.Enable(name)
}

func (c *CLI) resetHistory(name string) error {
	paths := []string{
		c.Dirs.Runs + "/" + name + ".jsonl",
		c.Dirs.Logs + "/" + name + ".log",
		c.Dirs.Logs + "/" + name + ".log.old",
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (c *CLI) log(args []string) error {
	n := 40
	var rest []string
	for i := 0; i < len(args); i++ {
		// -n is accepted before or after the name.
		if args[i] == "-n" && i+1 < len(args) {
			if v, err := parseInt(args[i+1]); err == nil && v > 0 {
				n = v
			}
			i++
			continue
		}
		rest = append(rest, args[i])
	}

	name, err := requireName(rest, "log <name> [-n N]")
	if err != nil {
		return err
	}

	path := c.Dirs.Logs + "/" + name + ".log"
	if _, err := os.Stat(path); err != nil {
		return noInput("no logs yet for %s (has it run? check: every list)", rubyInspect(name))
	}
	lines, err := tailLines(path, n)
	if err != nil {
		return err
	}
	fmt.Fprint(c.Stdout, strings.Join(lines, ""))
	return nil
}

func (c *CLI) remove(args []string) error {
	name, err := requireName(args, "rm <name>")
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
	if _, ok := s.Tasks.Get(name); !ok {
		return noInput("no task %s", rubyInspect(name))
	}

	if err := c.Backend.Disable(name); err != nil {
		return err
	}
	if err := c.Backend.DeleteUnits(name); err != nil {
		return err
	}
	if err := s.Remove(name); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout, "%s removed %s (logs kept in %s)\n", c.Color.Green("✓"), name, c.Dirs.Logs)
	return nil
}

func (c *CLI) setPaused(args []string, paused bool) error {
	name, err := requireName(args, "pause <name>")
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
	if _, ok := s.Tasks.Get(name); !ok {
		return noInput("no task %s", rubyInspect(name))
	}

	if err := c.Backend.Disable(name); err != nil {
		return err
	}
	if err := s.SetPaused(name, paused); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout, "%s paused %s\n", c.Color.Green("✓"), name)
	return nil
}

func (c *CLI) resume(args []string) error {
	name, err := requireName(args, "resume <name>")
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
	task, ok := s.Tasks.Get(name)
	if !ok {
		return noInput("no task %s", rubyInspect(name))
	}

	sched, err := schedule.FromRecord(task.Schedule)
	if err != nil {
		return err
	}
	if err := c.schedule(name, sched); err != nil {
		return err
	}
	if err := s.SetPaused(name, false); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout, "%s resumed %s\n", c.Color.Green("✓"), name)
	return nil
}

func (c *CLI) runTask(name string) error {
	r := runner.New(c.Dirs, c.Stdout, c.Stderr, c.Color)
	code, err := r.Run(name)
	if err != nil {
		return err
	}
	if code != 0 {
		return &exitError{code: code}
	}
	return nil
}
