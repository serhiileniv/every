package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
)

// doctor explains, in plain language, why something isn't running.
//
// Every check is phrased as a claim that is either true or false, and every
// failure carries the command that fixes it. A diagnostic that says what is
// wrong without saying what to do is only half a diagnostic.
func (c *CLI) doctor() error {
	failures := 0

	// check reports one condition and returns whether it failed.
	check := func(label string, ok bool, fix string) {
		if ok {
			fmt.Fprintf(c.Stdout, "  %s %s\n", c.Color.Green("✓"), label)
			return
		}
		fmt.Fprintf(c.Stdout, "  %s %s\n", c.Color.Red("✗"), label)
		fmt.Fprintf(c.Stdout, "    → %s\n", fix)
		failures++
	}
	note := func(format string, a ...any) {
		fmt.Fprintf(c.Stdout, "  · %s\n", fmt.Sprintf(format, a...))
	}

	c.doctorPlatform(check)

	// The data dir has to be writable or nothing else can be true.
	writable := os.MkdirAll(c.Dirs.Data, 0o755) == nil && isWritable(c.Dirs.Data)
	check(fmt.Sprintf("data dir writable (%s)", c.Dirs.Data), writable,
		fmt.Sprintf("fix permissions on %s", c.Dirs.Data))

	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}
	if s.Tasks.Len() == 0 {
		note("(no tasks registered yet)")
		return c.doctorVerdict(failures)
	}

	loadedNames, err := c.Backend.LoadedNames()
	if err != nil {
		return err
	}
	loaded := map[string]bool{}
	for _, n := range loadedNames {
		loaded[n] = true
	}

	for _, name := range s.Tasks.Names() {
		task, _ := s.Tasks.Get(name)
		fmt.Fprintf(c.Stdout, "\ntask: %s\n", name)

		check(fmt.Sprintf("scheduler resource exists (%s)", c.Backend.UnitPath(name)),
			c.Backend.ResourceExists(name),
			fmt.Sprintf("re-create the task: every rm %s && every <schedule> -- <cmd>", name))

		if task.Paused {
			note("paused — resume with: every resume %s", name)
		} else {
			check(fmt.Sprintf("scheduled in %s", c.Backend.Name()), loaded[name],
				fmt.Sprintf("load it: every resume %s", name))
		}

		c.doctorCommand(task.Cmd, check, note)
		c.doctorCwd(task.Cwd, note)
		c.doctorLastRun(s, name, check, note)
	}

	return c.doctorVerdict(failures)
}

func (c *CLI) doctorVerdict(failures int) error {
	fmt.Fprintln(c.Stdout)
	if failures == 0 {
		fmt.Fprintln(c.Stdout, c.Color.Green("all good ✓"))
		return nil
	}
	fmt.Fprintln(c.Stdout, c.Color.Red(fmt.Sprintf("%d problem(s) found", failures)))
	return &exitError{code: 1}
}

func isWritable(dir string) bool {
	probe := filepath.Join(dir, ".write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

var bareWordRe = regexp.MustCompile(`^[\w][\w.-]*$`)

// doctorCommand probes whether the command's first word actually resolves.
//
// This is the check that catches the single most common real failure: a
// command that works in the user's terminal and not under the scheduler,
// because a login shell reads .zprofile rather than .zshrc.
func (c *CLI) doctorCommand(cmd string, check func(string, bool, string), note func(string, ...any)) {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return
	}
	word := fields[0]

	switch {
	case bareWordRe.MatchString(word):
		ok := commandResolves(word)
		fix := "not on the login shell's PATH. A login shell reads ~/.zprofile " +
			"(not ~/.zshrc), so a PATH set only for interactive shells won't be " +
			"there — use an absolute path, or set PATH in ~/.zprofile."
		if runtime.GOOS == "windows" {
			fix = "tasks use the Windows shell; check the user/system PATH, or use an absolute path"
		}
		check(fmt.Sprintf("command resolvable in login shell (%s)", word), ok, fix)

	case looksLikePath(word):
		expanded := expandUser(word)
		_, err := os.Stat(expanded)
		check(fmt.Sprintf("command file exists (%s)", word), err == nil,
			fmt.Sprintf("not found: %s — check the path", expanded))

	default:
		// A shell expression -- a variable, a subshell, a pipeline. Probing the
		// first token would be meaningless, so say so rather than guess.
		note("command begins with %q — not probed (shell expression)", word)
	}
}

func looksLikePath(w string) bool {
	if strings.Contains(w, "/") {
		return true
	}
	if runtime.GOOS == "windows" {
		return strings.Contains(w, `\`) || strings.Contains(w, ":")
	}
	return false
}

func expandUser(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + strings.TrimPrefix(p, "~")
		}
	}
	if runtime.GOOS == "windows" {
		if profile := os.Getenv("USERPROFILE"); profile != "" {
			p = strings.ReplaceAll(p, "%USERPROFILE%", profile)
		}
	}
	return p
}

func commandResolves(word string) bool {
	if runtime.GOOS == "windows" {
		return exec.Command("where.exe", word).Run() == nil
	}
	_, err := exec.LookPath(word)
	return err == nil
}

// doctorCwd warns about the macOS privacy folders, where a scheduler-spawned
// process can see a directory and still be refused when it reads it.
func (c *CLI) doctorCwd(cwd string, note func(string, ...any)) {
	if runtime.GOOS != "darwin" || cwd == "" {
		return
	}
	if !tccProtected(cwd) {
		return
	}
	note("cwd is in a privacy-protected folder (%s)", cwd)
	note("  if runs fail with \"Operation not permitted\", grant Full Disk Access to every, or move the task's directory")
}

var tccRe = regexp.MustCompile(`/(Documents|Desktop|Downloads)(/|$)`)

func tccProtected(p string) bool { return tccRe.MatchString(p) }

func (c *CLI) doctorLastRun(s *store.Store, name string, check func(string, bool, string), note func(string, ...any)) {
	last, err := s.LastRun(name)
	if err != nil || last == nil {
		note("no runs recorded yet (next: check `every list`)")
		return
	}
	if last.Exit != 0 {
		check("last run succeeded", false,
			fmt.Sprintf("exit=%d — see: every log %s", last.Exit, name))
		if runtime.GOOS == "darwin" && logMentionsPermission(c.Dirs.Logs, name) {
			note("  the log says \"Operation not permitted\" — this is macOS privacy protection")
			note("  grant Full Disk Access to the every binary, or move the task's directory")
		}
		return
	}
	fmt.Fprintf(c.Stdout, "  %s last run ok (%s)\n", c.Color.Green("✓"), last.At)
}

func logMentionsPermission(logDir, name string) bool {
	raw, err := os.ReadFile(filepath.Join(logDir, name+".log"))
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "Operation not permitted")
}

// used to keep the schedule import meaningful if the checks above change shape
var _ = schedule.Interval
