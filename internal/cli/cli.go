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

	err := c.dispatch(argv)
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
	switch argv[0] {
	case "list", "ls", "run", "doctor", "inspect", "show", "set":
		c.migrate(wantsJSON(argv))
	}

	switch argv[0] {
	case "help", "-h", "--help":
		fmt.Fprint(c.Stdout, helpText(c.Dirs.Data))
		return nil
	case "version", "--version":
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
	if len(args) == 0 || args[0] == "" {
		return "", invocationf("%s", usage)
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
		return nil, coded(CodeUsage, "", "%s isn't a command, and there's no `--` before a task.\n"+
			"  to schedule:  every <when> -- <command>   (e.g. every day 9am -- brew update)\n"+
			"  commands:     list, log, run, pause, resume, rm, doctor, version",
			rubyInspect(argv[0]))
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

	sched, err := schedule.Parse(pre)
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

func (c *CLI) log(args []string) error {
	args, asJSON := stripJSONFlag(args)
	args, withOutput := removeFlag(args, "--with-output")
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

	if asJSON {
		return c.logJSON(name, n, withOutput)
	}

	path := c.Dirs.Logs + "/" + name + ".log"
	if _, err := os.Stat(path); err != nil {
		return noInputCoded(CodeNoLogs, name, "no logs yet for %s (has it run? check: every list)", rubyInspect(name))
	}
	lines, err := tailLines(path, n)
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
		return c.runDryRun(name, asJSON)
	}
	return c.runTask(name, asJSON)
}

func (c *CLI) runTask(name string, asJSON bool) error {
	// Checked here rather than left to the runner: an unknown task is a failure
	// to report, not a run to describe, and emitting a result object for one
	// would put a wall-clock timestamp in a response that has nothing to say.
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}
	if _, ok := s.Tasks.Get(name); !ok {
		return noInputCoded(CodeNoSuchTask, name,
			"unknown task %s — orphaned agent? try: every doctor", rubyInspect(name))
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

	if code != 0 {
		return &exitError{code: code, errorCode: CodeInternal, name: name}
	}
	return nil
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
// migrate repairs stale scheduler units and reports what it did.
//
// The report is suppressed under --json: it is prose on stdout, and stdout is
// the data channel. A repaired task still shows up in the data itself, and
// doctor reports it explicitly.
func (c *CLI) migrate(quiet bool) {
	if c.Backend == nil {
		return
	}
	res := migrate.Run(c.Dirs, c.Backend, c.Launcher, Version)
	if res.Any() && !quiet {
		migrate.Report(c.Stdout, res)
	}
}
