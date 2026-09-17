package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/migrate"
	"github.com/serhiileniv/every/internal/naming"
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

	// Launcher is the path the scheduler invokes, needed to tell whether the
	// units on disk were written for this binary or an older runtime.
	Launcher string

	// Recolor rebuilds the colour decision once --color has been parsed out of
	// argv, which cannot happen before the CLI is constructed. Left nil, the
	// flag is still accepted and Color is simply whatever the caller built --
	// which is what the tests want, since they write to a buffer.
	Recolor func(ui.Mode) ui.Color
}

// applyColorFlag strips --color from the tokens ahead of `--` and applies it.
//
// A global flag rather than a per-command one: "do not paint this" is a
// property of the invocation, not of `list` in particular, and a user who
// pipes one command pipes them all.
func (c *CLI) applyColorFlag(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return argv, nil
	}
	// argv[0] is the command or the schedule's first token, never a flag --
	// the same rule --json follows.
	head, tail := argv[:1], argv[1:]

	// Only the flag half: after `--` the tokens are the user's command, and a
	// --color it passes to its own program is none of our business.
	cut := len(tail)
	for i, tok := range tail {
		if tok == "--" {
			cut = i
			break
		}
	}
	pre, rest := tail[:cut], tail[cut:]

	pre, value, found, err := extractValueFlag(pre, "--color")
	if err != nil {
		return nil, err
	}
	if !found {
		return argv, nil
	}
	mode, ok := ui.ParseMode(value)
	if !ok {
		return nil, coded(CodeUsage, "", "--color wants auto, always or never, got %s", rubyInspect(value))
	}
	if c.Recolor != nil {
		c.Color = c.Recolor(mode)
	}
	return append(append(append([]string{}, head...), pre...), rest...), nil
}

// Run dispatches one invocation and returns the process exit code.
//
// The exit codes are a contract, documented in the man page and asserted by the
// e2e suite: 0 ok, 64 usage or bad arguments, 66 no such task or log, 1
// anything else. Runs additionally surface 124 and 128+signum from the runner.
func (c *CLI) Run(argv []string) int {
	// Whether the failure is rendered as prose or as an object is decided by
	// the same flag that decides it for success, so a caller never has to
	// handle one of each.
	asJSON := wantsJSON(argv)

	argv, err := c.applyColorFlag(argv)
	if err != nil {
		return c.renderError(err, asJSON)
	}

	err = c.dispatch(argv)
	if err == nil {
		return 0
	}
	return c.renderError(err, asJSON)
}

// renderErrorText is the human rendering, unchanged from 0.4.0 down to the
// second line and the missing "every: " prefix on a corrupt store.
func (c *CLI) renderErrorText(err error) int {
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
	// Additive, for the --json renderer; see usageError.
	errorCode string
	name      string
}

func (e *exitError) Error() string { return e.msg }

func (e *exitError) errCode(fallback string) string {
	if e.errorCode == "" {
		return fallback
	}
	return e.errorCode
}

func noInput(format string, a ...any) error {
	return &exitError{code: paths.ExitNoInput, msg: "every: " + fmt.Sprintf(format, a...)}
}

// noInputCoded is noInput with the code and task name a program needs.
func noInputCoded(code, name, format string, a ...any) error {
	return &exitError{
		code: paths.ExitNoInput, msg: "every: " + fmt.Sprintf(format, a...),
		errorCode: code, name: name,
	}
}

func (c *CLI) dispatch(argv []string) error {
	if len(argv) == 0 {
		fmt.Fprint(c.Stdout, helpText(c.Dirs.Data))
		return nil
	}

	// Repair units left by an older runtime before anything reads them. Placed
	// here rather than in each command so a path added later cannot forget it;
	// the stamp file makes the no-op case a single small file read.
	//
	// Only for commands that are already touching the store or the scheduler:
	// `help` and `version` must stay usable on a broken install, and must not
	// take a detour through the data dir to print three lines.
	//
	// `run` repairs too, from runCommand: it has to mark the task running
	// first. See holdRun.
	switch argv[0] {
	case "list", "ls", "doctor", "inspect", "show", "set":
		c.migrate()
	}

	// -h/--help anywhere before `--` prints help, whatever the subcommand.
	//
	// Ahead of dispatch because each command would otherwise have to remember:
	// `every log --help` used to look up a task literally named "--help", and
	// under the flag strictness added in 0.6 it would have become "unknown
	// flag", which is a worse answer than the help the user asked for.
	//
	// The subcommand's own page when there is one, so `every log --help` is
	// about log. A schedule in argv[0] -- `every 15m --help` -- has no page
	// and falls through to the full help.
	if len(argv) > 1 && wantsHelp(argv[1:]) {
		if page := topicHelpText(argv[0]); page != "" {
			fmt.Fprint(c.Stdout, page)
			return nil
		}
		fmt.Fprint(c.Stdout, helpText(c.Dirs.Data))
		return nil
	}

	switch argv[0] {
	case "help", "-h", "--help":
		return c.help(argv[1:])
	case "version", "--version", "-V", "-v":
		if rest, _ := stripJSONFlag(argv[1:]); len(rest) > 0 {
			if err := rejectUnknownFlags(rest); err != nil {
				return err
			}
			if err := rejectExtraArgs(rest); err != nil {
				return err
			}
		}
		if wantsJSON(argv) {
			return emitJSON(c.Stdout, versionPayload{
				Version: Version, Tagline: Tagline, Homepage: Homepage,
			})
		}
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
		return c.doctor(argv[1:])
	case "set":
		return c.set(argv[1:])
	case "inspect", "show":
		return c.inspect(argv[1:])
	case "exists":
		return c.exists(argv[1:])
	case "schema":
		return c.schema(argv[1:])
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
		return c.runCommand(argv[1:])
	default:
		return c.add(argv)
	}
}

// requireName is the shared shape of the single-argument subcommands.
// The usage strings passed here are FROZEN. `every log` with no name is an
// existing invocation with a defined output, asserted byte for byte by the
// surface table, so flags added later are documented in help and the man page
// rather than appended to these lines.
func requireName(args []string, usage string) (string, error) {
	// Unknown flags are rejected before the name is read, so `every rm --oops x`
	// says what is actually wrong instead of removing x.
	if err := rejectUnknownFlags(args); err != nil {
		return "", err
	}
	if len(args) == 0 || args[0] == "" {
		return "", invocationf("%s", usage)
	}
	// These commands take exactly one name. A second positional is a mistake
	// -- most often a shell that split an unquoted name -- and acting on the
	// first while ignoring the rest is the wrong half to guess at.
	if err := rejectExtraArgs(args[1:]); err != nil {
		return "", err
	}
	return args[0], nil
}

// addSpec is a parsed `<schedule> [flags] -- <command>` invocation.
//
// Shared by add and set so there is one grammar rather than two that drift.
type addSpec struct {
	sched   *schedule.Schedule
	cmd     string
	name    string
	hasName bool
	quiet   bool
	timeout int
	onFail  string
	cwd     string
}

// parseAddSpec parses the form both add and set take. verb only shapes the
// error text.
func (c *CLI) parseAddSpec(argv []string, verb string) (*addSpec, error) {
	if len(argv) == 0 {
		return nil, coded(CodeUsage, "", "%s <when> -- <command>", verb)
	}

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
		if verb == "set" {
			return nil, coded(CodeUsage, "",
				"set <when> --name <name> -- <command>   (the `--` separates them)")
		}
		// A suggestion only when the token cannot be a schedule. `every 15m`
		// is not a command either, and "did you mean list" would be a wrong
		// answer to a correct invocation that merely lacks its `--`.
		hint := ""
		if _, schedErr := schedule.ParseAt(argv[:1], c.Now()); schedErr != nil {
			if guess := suggestCommand(argv[0]); guess != "" {
				hint = fmt.Sprintf("  did you mean:  every %s\n", guess)
			}
		}
		return nil, coded(CodeUsage, "", "%s isn't a command, and there's no `--` before a task.\n"+
			"%s"+
			"  to schedule:  every <when> -- <command>   (e.g. every day 9am -- brew update)\n"+
			"  commands:     list, log, run, pause, resume, rm, doctor, version",
			rubyInspect(argv[0]), hint)
	}

	// --json is stripped from the flag half only, and only here -- stripping it
	// from the whole invocation earlier renamed the offending token in the
	// "isn't a command" error, which reports argv[0] as the user typed it.
	pre, _ := stripJSONFlag(argv[:sep])
	cmdTokens := argv[sep+1:]
	if len(cmdTokens) == 0 {
		return nil, coded(CodeUsage, "", "missing command after --")
	}
	spec := &addSpec{cmd: strings.Join(cmdTokens, " ")}

	pre, spec.quiet = removeFlag(pre, "--quiet")

	pre, explicitName, hasName, err := extractValueFlag(pre, "--name")
	if err != nil {
		return nil, err
	}
	spec.hasName = hasName

	pre, timeoutRaw, hasTimeout, err := extractValueFlag(pre, "--timeout")
	if err != nil {
		return nil, err
	}
	if hasTimeout {
		if spec.timeout, err = parseDuration(timeoutRaw); err != nil {
			return nil, err
		}
	}

	pre, onFail, hasOnFail, err := extractValueFlag(pre, "--on-fail")
	if err != nil {
		return nil, err
	}
	if hasOnFail {
		spec.onFail = onFail
	}

	// Before the schedule parser sees them, so an unknown flag is reported as
	// one rather than as an unparseable schedule token.
	if err := rejectUnknownFlags(pre); err != nil {
		return nil, err
	}

	// The CLI clock, not the wall clock: a once schedule resolves "9am" to
	// today or tomorrow against it, and tests pin it.
	sched, err := schedule.ParseAt(pre, c.Now())
	if err != nil {
		return nil, &usageError{msg: err.Error(), code: CodeBadSchedule}
	}
	spec.sched = sched

	if hasName {
		spec.name = sanitize(explicitName)
		if spec.name == "" {
			return nil, coded(CodeBadName, "",
				"--name %s is empty after sanitizing (names allow a-z 0-9 . _ -)",
				rubyInspect(explicitName))
		}
		if len([]rune(spec.name)) > maxName {
			return nil, coded(CodeBadName, "", "--name is too long (max %d chars)", maxName)
		}
		if err := naming.Validate(spec.name); err != nil {
			return nil, coded(CodeBadName, spec.name, "%v", err)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	spec.cwd = cwd
	return spec, nil
}

func (c *CLI) add(argv []string) error {
	asJSON := wantsJSON(argv)

	spec, err := c.parseAddSpec(argv, "add")
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

	name := spec.name
	if spec.hasName {
		if _, exists := s.Tasks.Get(name); exists {
			return coded(CodeAlreadyExists, name,
				"task %s already exists (every rm %s, or pick another --name)",
				rubyInspect(name), name)
		}
	} else {
		name = deriveName(spec.cmd, func(n string) bool { _, ok := s.Tasks.Get(n); return ok })
	}

	// A re-used name must not inherit the previous task's history.
	if err := c.resetHistory(name); err != nil {
		return err
	}

	task := &store.Task{
		Cmd: spec.cmd, Schedule: spec.sched.ToRecord(), Cwd: spec.cwd,
		CreatedAt: c.Now().Format(time.RFC3339),
		Paused:    false, Quiet: spec.quiet, Timeout: spec.timeout, OnFail: spec.onFail,
	}
	if err := s.Add(name, task); err != nil {
		return err
	}

	// Roll the store back if the scheduler refuses, so a failed add leaves no
	// task that `list` shows but nothing will ever run.
	if err := c.schedule(name, spec.sched); err != nil {
		_ = s.Remove(name)
		_ = c.Backend.DeleteUnits(name)
		var ue *usageError
		if errors.As(err, &ue) {
			return err
		}
		var unsupported *backend.UnsupportedScheduleError
		if errors.As(err, &unsupported) {
			return &usageError{msg: unsupported.Error(), code: CodeUnsupportedSchedule, name: name}
		}
		return &exitError{
			code: 1, msg: fmt.Sprintf("could not schedule %s: %v", name, err),
			errorCode: CodeSchedulerFailed, name: name,
		}
	}

	if asJSON {
		view, vErr := c.taskViewFrom(s, name, task)
		if vErr != nil {
			return vErr
		}
		return emitJSON(c.Stdout, view)
	}

	fmt.Fprintf(c.Stdout, "%s scheduled %s: %s — %s\n",
		c.Color.Green("✓"), name, spec.sched.Raw, spec.cmd)
	c.printWhenItRuns(spec.sched)
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

// noLogsError separates a task that has not run yet from a name that is not a
// task at all.
//
// Both are exit 66 and both used to say "no logs yet for X (has it run?)",
// which is a confusing answer to give someone who simply mistyped the name --
// and unlike every other command, it left a program unable to tell the two
// apart, since the code was no_logs either way.
//
// The check is only reached once there are no logs, which is what keeps the
// original reason for not consulting the store intact: logs outlive the task
// that made them, `every rm` keeps them on purpose, and reading the log of a
// removed task still works.
func (c *CLI) noLogsError(name string) error {
	if s, err := store.Load(c.Dirs.Data); err == nil {
		if _, ok := s.Tasks.Get(name); !ok {
			return noInputCoded(CodeNoSuchTask, name, "no task %s", rubyInspect(name))
		}
	}
	return noInputCoded(CodeNoLogs, name,
		"no logs yet for %s (has it run? check: every list)", rubyInspect(name))
}

func (c *CLI) log(args []string) error {
	args, asJSON := stripJSONFlag(args)
	args, withOutput := removeFlag(args, "--with-output")
	args, follow := removeFlag(args, "--follow")
	if rest, short := removeFlag(args, "-f"); short {
		args, follow = rest, true
	}
	n := 40
	var rest []string
	for i := 0; i < len(args); i++ {
		// -n is accepted before or after the name.
		if args[i] == "-n" {
			if i+1 >= len(args) {
				return coded(CodeUsage, "", "-n needs a value")
			}
			v, err := parseCount(args[i+1])
			if err != nil {
				return err
			}
			n = v
			i++
			continue
		}
		rest = append(rest, args[i])
	}

	if err := rejectUnknownFlags(rest); err != nil {
		return err
	}
	name, err := requireName(rest, "log <name> [-n N]")
	if err != nil {
		return err
	}

	if asJSON {
		if follow {
			// A stream of prose is not the shape `every schema log` promises,
			// and emitting it under --json would break the one contract a
			// program relies on. Refused rather than silently ignored.
			return coded(CodeUsage, "", "--follow cannot be combined with --json")
		}
		return c.logJSON(name, n, withOutput)
	}

	if follow {
		// A log that does not exist yet is something to wait for under -f,
		// so the no_logs check below is deliberately skipped.
		if err := c.followLog(name, n); err != nil && err != errFollowStopped {
			return err
		}
		return nil
	}

	if !c.logExists(name) {
		return c.noLogsError(name)
	}
	lines, err := c.tailLog(name, n)
	if err != nil {
		return err
	}
	fmt.Fprint(c.Stdout, strings.Join(lines, ""))
	return nil
}

func (c *CLI) remove(args []string) error {
	args, asJSON := stripJSONFlag(args)
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
		return noInputCoded(CodeNoSuchTask, name, "no task %s", rubyInspect(name))
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
	if asJSON {
		return emitJSON(c.Stdout, okPayload{Name: name, OK: true})
	}
	fmt.Fprintf(c.Stdout, "%s removed %s (logs kept in %s)\n", c.Color.Green("✓"), name, c.Dirs.Logs)
	return nil
}

func (c *CLI) setPaused(args []string, paused bool) error {
	args, asJSON := stripJSONFlag(args)
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
		return noInputCoded(CodeNoSuchTask, name, "no task %s", rubyInspect(name))
	}

	if err := c.Backend.Disable(name); err != nil {
		return err
	}
	if err := s.SetPaused(name, paused); err != nil {
		return err
	}
	if asJSON {
		return emitJSON(c.Stdout, okPayload{Name: name, OK: true})
	}
	fmt.Fprintf(c.Stdout, "%s paused %s\n", c.Color.Green("✓"), name)
	return nil
}

func (c *CLI) resume(args []string) error {
	args, asJSON := stripJSONFlag(args)
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
		return noInputCoded(CodeNoSuchTask, name, "no task %s", rubyInspect(name))
	}

	// The name came from the store, which is a plain file anyone can edit --
	// so it has not necessarily been through `add`'s sanitizer. Refuse before
	// it reaches a scheduler rather than after, and say why.
	if err := naming.Validate(name); err != nil {
		return usagef("%v — remove it and re-add: every rm %s", err, rubyInspect(name))
	}

	sched, err := schedule.FromRecord(task.Schedule)
	if err != nil {
		return err
	}
	// A one-shot whose moment has passed has nothing to resume into: launchd
	// would register it for the same date next year.
	if sched.Kind == schedule.Once && !sched.At.After(c.Now()) {
		return usagef("%s was due %s and has passed — remove it and re-add: every rm %s",
			name, sched.At.Format("Mon 02 Jan 15:04"), rubyInspect(name))
	}
	if err := c.schedule(name, sched); err != nil {
		return err
	}
	if err := s.SetPaused(name, false); err != nil {
		return err
	}
	if asJSON {
		return emitJSON(c.Stdout, okPayload{Name: name, OK: true})
	}
	fmt.Fprintf(c.Stdout, "%s resumed %s\n", c.Color.Green("✓"), name)
	return nil
}

// runPayload is `every run <name> --json`.
type runPayload struct {
	Name    string         `json:"name"`
	At      string         `json:"at"`
	Exit    int            `json:"exit"`
	Seconds store.Duration `json:"seconds"`
	Output  string         `json:"output,omitempty"`
	DryRun  bool           `json:"dry_run,omitempty"`
	// Plan is filled only by --dry-run: what WOULD have been executed.
	Plan *runPlan `json:"plan,omitempty"`
}

// runPlan is everything resolved just before execution.
type runPlan struct {
	Command   string   `json:"command"`
	Shell     []string `json:"shell"`
	Directory string   `json:"directory"`
	TimeoutS  int      `json:"timeout_seconds"`
	OnFail    string   `json:"on_fail,omitempty"`
	// Note explains a directory that had to be substituted, which is the one
	// thing about a scheduled run that surprises people.
	Note string `json:"note,omitempty"`
}

func (c *CLI) runCommand(args []string) error {
	args, asJSON := stripJSONFlag(args)
	args, dryRun := removeFlag(args, "--dry-run")

	name, err := requireName(args, "run <name>")
	if err != nil {
		return err
	}
	if dryRun {
		c.migrate()
		return c.runDryRun(name, asJSON)
	}
	if lock := c.holdRun(name); lock != nil {
		defer lock.Close()
	}
	c.migrate()
	return c.runTask(name, asJSON)
}

// holdRun marks a task as running for the rest of this process, and must come
// before the repair pass.
//
// On launchd a scheduled run IS the job, and the pass unloads one-shots whose
// moment has passed and re-registers stale units -- both of which launchd
// answers by killing the job. Without the mark, a one-shot's own firing killed
// it before its command ran, and so did an `every list` typed during a long
// one. The pass skips anything marked running.
//
// Nil for a name the store does not have, so a typo leaves no lock file
// behind; runTask reports it. Best-effort otherwise: failing to mark a run is
// no reason not to run it.
func (c *CLI) holdRun(name string) *store.Lock {
	if naming.Validate(name) != nil {
		return nil
	}
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return nil
	}
	if _, ok := s.Tasks.Get(name); !ok {
		return nil
	}
	lock, err := store.HoldRun(c.Dirs.Data, name)
	if err != nil {
		return nil
	}
	return lock
}

func (c *CLI) runTask(name string, asJSON bool) error {
	// Checked here rather than left to the runner: an unknown task is a failure
	// to report, not a run to describe, and emitting a result object for one
	// would put a wall-clock timestamp in a response that has nothing to say.
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}
	task, ok := s.Tasks.Get(name)
	if !ok {
		return noInputCoded(CodeNoSuchTask, name,
			"unknown task %s — orphaned agent? try: every doctor", rubyInspect(name))
	}
	// Read before the run: the store may change underneath a long command,
	// and retire re-checks under the lock anyway.
	onceAt, isOnce := onceInstant(task)
	if isOnce && c.firedAYearLate(onceAt) {
		return c.refuseYearLate(name, onceAt)
	}

	r := runner.New(c.Dirs, c.Stdout, c.Stderr, c.Color)
	if asJSON {
		// Under --json the runner must not print the output itself: stdout
		// carries the object, and the output belongs inside it.
		r.Quiet = true
	}

	started := c.Now()
	code, err := r.Run(name)
	if err != nil {
		return err
	}

	if asJSON {
		s, lErr := store.Load(c.Dirs.Data)
		if lErr != nil {
			return lErr
		}
		payload := runPayload{Name: name, At: started.Format(time.RFC3339), Exit: code}
		if last, rErr := s.LastRun(name); rErr == nil && last != nil {
			payload.At, payload.Seconds = last.At, last.Dur
		}
		payload.Output = string(r.LastOutput)
		if eErr := emitJSON(c.Stdout, payload); eErr != nil {
			return eErr
		}
	}

	// A one-shot that has had its moment is done, whatever the exit code:
	// the failure is in the ledger and the notification has gone out. Gated
	// on the instant rather than on the run, so `every run` typed beforehand
	// to check the command works does not consume the task.
	if isOnce && !c.Now().Before(onceAt) {
		c.retire(name, onceAt, asJSON || !c.Color.Enabled)
	}

	if code != 0 {
		return &exitError{code: code, errorCode: CodeInternal, name: name}
	}
	return nil
}

// firedAYearLate reports a one-shot invoked at least a year after its moment on
// a scheduler that drops missed triggers.
//
// launchd's plist has Month, Day, Hour and Minute but no Year, so a unit that
// survived a year fires again on the same date. The start-up pass normally
// removes it long before -- see migrate.disarmMissedOnce -- but only if some
// every command ran in that year. A day short of a year, because launchd runs
// a trigger it slept through on wake: this is about the re-fire, not about
// lateness, and anything less late is still a run the user asked for.
func (c *CLI) firedAYearLate(at time.Time) bool {
	return !c.catchesUpMissed() && !c.Now().Before(at.AddDate(1, 0, -1))
}

// refuseYearLate removes the unit and keeps the store entry, which is exactly
// what disarming a missed one-shot does: `list` goes on saying missed and
// `every rm` clears it. Unregistering ends this process when launchd started
// it, so nothing a scheduled run would print survives -- the state it leaves
// is the report.
func (c *CLI) refuseYearLate(name string, at time.Time) error {
	_ = backend.Retire(c.Backend, name)
	return &exitError{
		code:      1,
		errorCode: CodeMissed,
		name:      name,
		msg: fmt.Sprintf("every: %s was due %s and missed; not running a one-shot a year late — "+
			"every rm %s, then every once … to schedule it again",
			name, at.Format("2006-01-02 15:04"), name),
	}
}

// onceInstant is the instant of a once task, and whether it is one.
func onceInstant(task *store.Task) (time.Time, bool) {
	if task.Schedule.Kind != string(schedule.Once) {
		return time.Time{}, false
	}
	sched, err := schedule.FromRecord(task.Schedule)
	if err != nil {
		return time.Time{}, false
	}
	return sched.At, true
}

// retire removes a fired once task: from the store first, then from the
// scheduler, in that order and with nothing after.
//
// The store is re-read under the lock rather than reusing the pre-run copy,
// which is stale by the width of the run: a `set` in the meantime may have
// turned the name into a different task, which must survive. The scheduler
// step is last and best-effort because on launchd it ends this process (see
// backend.Retirer), so everything the user can see is already written.
func (c *CLI) retire(name string, at time.Time, quiet bool) {
	lock, err := store.AcquireLock(c.Dirs.Data)
	if err != nil {
		return
	}
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		lock.Close()
		return
	}
	task, ok := s.Tasks.Get(name)
	if !ok {
		lock.Close()
		return
	}
	if cur, isOnce := onceInstant(task); !isOnce || !cur.Equal(at) {
		lock.Close()
		return
	}
	if err := s.Remove(name); err != nil {
		lock.Close()
		return
	}
	lock.Close()

	if !quiet {
		fmt.Fprintf(c.Stdout, "%s ran once, removed %s (logs kept in %s)\n", c.Color.Green("✓"), name, c.Dirs.Logs)
	}
	_ = backend.Retire(c.Backend, name)
}

// runDryRun resolves everything a run needs and executes nothing.
//
// The value is highest on Windows, where "what will Task Scheduler actually
// receive" is otherwise unanswerable without registering something and looking.
func (c *CLI) runDryRun(name string, asJSON bool) error {
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}
	task, ok := s.Tasks.Get(name)
	if !ok {
		return noInputCoded(CodeNoSuchTask, name, "no task %s", rubyInspect(name))
	}

	r := runner.New(c.Dirs, c.Stdout, c.Stderr, c.Color)
	dir, note := r.Workdir(task.Cwd)
	plan := &runPlan{
		Command: task.Cmd, Shell: r.ShellFor(), Directory: dir,
		TimeoutS: task.Timeout, OnFail: task.OnFail,
		Note: strings.TrimSuffix(note, "\n"),
	}

	if asJSON {
		return emitJSON(c.Stdout, runPayload{Name: name, DryRun: true, Plan: plan})
	}

	fmt.Fprintf(c.Stdout, "would run %s\n", name)
	fmt.Fprintf(c.Stdout, "  command:   %s\n", plan.Command)
	fmt.Fprintf(c.Stdout, "  shell:     %s\n", strings.Join(plan.Shell, " "))
	fmt.Fprintf(c.Stdout, "  directory: %s\n", plan.Directory)
	if plan.TimeoutS > 0 {
		fmt.Fprintf(c.Stdout, "  timeout:   %ds\n", plan.TimeoutS)
	}
	if plan.OnFail != "" {
		fmt.Fprintf(c.Stdout, "  on fail:   %s\n", plan.OnFail)
	}
	if plan.Note != "" {
		fmt.Fprintf(c.Stdout, "  note:      %s\n", plan.Note)
	}
	fmt.Fprintln(c.Stdout, "\nnothing was executed (--dry-run)")
	return nil
}

// migrate repairs stale scheduler units and reports what it did.
//
// Failures are surfaced but never fatal: a task that cannot be repaired must
// not stop the command the user actually asked for, and the message tells them
// how to fix it by hand.
//
// The report goes to stderr, so stdout stays the data channel. It used to go
// to stdout and be suppressed under --json, which protected programs and left
// `every list > tasks.txt` capturing the notice -- the gate was the workaround,
// stderr is the fix.
func (c *CLI) migrate() {
	if c.Backend == nil {
		return
	}
	res := migrate.Run(c.Dirs, c.Backend, c.Launcher, Version, c.Now())
	if res.Any() {
		migrate.Report(c.Stderr, res)
	}
}

// help prints the full page, or one command's.
//
// `every help` is the index; `every help <command>` is the entry. The index
// stays what a bare invocation prints, because "what can this do" and "how do
// I use this one" are different questions and a page that answers one badly
// answers neither.
func (c *CLI) help(args []string) error {
	if err := rejectUnknownFlags(args); err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Fprint(c.Stdout, helpText(c.Dirs.Data))
		return nil
	}
	if err := rejectExtraArgs(args[1:]); err != nil {
		return err
	}
	page := topicHelpText(args[0])
	if page == "" {
		return unknownTopic(args[0])
	}
	fmt.Fprint(c.Stdout, page)
	return nil
}
