package backend

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/serhiileniv/every/internal/naming"
	"github.com/serhiileniv/every/internal/schedule"
)

// Launchd schedules through macOS launchd user agents.
type Launchd struct{ cfg Config }

func NewLaunchd(cfg Config) *Launchd { return &Launchd{cfg: cfg} }

func (l *Launchd) Name() string { return "launchd" }

func (l *Launchd) Label(name string) string { return "com.every." + name }

func (l *Launchd) UnitPath(name string) string {
	return filepath.Join(l.cfg.Dirs.Agents, l.Label(name)+".plist")
}

func (l *Launchd) ResourceExists(name string) bool {
	_, err := os.Stat(l.UnitPath(name))
	return err == nil
}

func (l *Launchd) Write(name string, s *schedule.Schedule) error {
	// The last gate before a name becomes a path. See internal/naming: the
	// store is a plain file, so `add`'s sanitizer is not the only way in.
	if err := naming.Validate(name); err != nil {
		return err
	}
	if err := os.MkdirAll(l.cfg.Dirs.Agents, 0o755); err != nil {
		return err
	}
	// logs/ is created here, at add time, so the first fire's _agent.log
	// redirect has somewhere to open. launchd will not create it.
	if err := os.MkdirAll(l.cfg.Dirs.Logs, 0o755); err != nil {
		return err
	}
	return os.WriteFile(l.UnitPath(name), []byte(l.PlistXML(name, s)), 0o644)
}

// PlistXML builds the agent definition.
//
// Built as a string, not through encoding/xml: that package reorders
// attributes, reformats whitespace, and escapes more characters than the three
// entities used here. The output is compared byte for byte against fixtures
// generated from the Ruby, and a plist that differs only cosmetically would
// still make an upgrade rewrite every user's agents for no reason.
func (l *Launchd) PlistXML(name string, s *schedule.Schedule) string {
	// path.Join, not filepath.Join: this string goes INTO a plist, and launchd
	// is macOS-only, so the separator is always "/" regardless of the host
	// building it. filepath.Join uses the HOST's separator, which turned the
	// log path into backslashes when the generator ran on Windows -- harmless
	// in production, since the backend only runs on macOS, but it broke the
	// cross-platform golden tests that exist precisely so these generators are
	// checked everywhere.
	agentLog := path.Join(l.cfg.Dirs.Logs, "_agent.log")

	var trigger string
	if s.Kind == schedule.Interval {
		trigger = "  <key>StartInterval</key>\n  <integer>" + s.Interval.String() + "</integer>"
	} else {
		dicts := make([]string, 0, len(s.Entries))
		for _, e := range s.Entries {
			var lines []string
			// Weekday only when the entry has one: its absence is what makes
			// `day 9am` fire daily rather than on Sunday.
			if e.Weekday != nil {
				lines = append(lines, fmt.Sprintf("      <key>Weekday</key><integer>%d</integer>", *e.Weekday))
			}
			lines = append(lines,
				fmt.Sprintf("      <key>Hour</key><integer>%d</integer>", e.Hour),
				fmt.Sprintf("      <key>Minute</key><integer>%d</integer>", e.Minute))
			dicts = append(dicts, "    <dict>\n"+strings.Join(lines, "\n")+"\n    </dict>")
		}
		trigger = "  <key>StartCalendarInterval</key>\n  <array>\n" +
			strings.Join(dicts, "\n") + "\n  </array>"
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>run</string>
    <string>%s</string>
  </array>
%s
%s  <key>RunAtLoad</key>
  <false/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`,
		xesc(l.Label(name)),
		xesc(l.cfg.Launcher),
		xesc(name),
		trigger,
		l.envBlock(),
		xesc(agentLog),
		xesc(agentLog))
}

// envBlock pins the resolved data dir.
//
// launchd does not inherit the shell's EVERY_HOME or XDG_DATA_HOME, so without
// this an XDG (or EVERY_HOME) install's scheduled runs would recompute the
// default directory, fail to find the task, and never fire.
func (l *Launchd) envBlock() string {
	return "  <key>EnvironmentVariables</key>\n  <dict>\n" +
		fmt.Sprintf("    <key>EVERY_HOME</key><string>%s</string>\n", xesc(l.cfg.Dirs.Data)) +
		"  </dict>\n"
}

// xesc escapes exactly the three entities the Ruby escaped, in that order.
// Not xml.EscapeText, which also escapes quotes, tabs and newlines as numeric
// references and would change every fixture.
func xesc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

func (l *Launchd) uid() int { return os.Getuid() }

func (l *Launchd) domain() string { return fmt.Sprintf("gui/%d", l.uid()) }

// Enable loads the agent, with a fallback ladder.
//
// bootstrap is the modern API. Its most common failure is an agent with this
// label already loaded, so on failure we bootout and retry. `load -w` is the
// pre-10.10 path and covers hosts where bootstrap is unavailable or refuses
// over a bare SSH session.
func (l *Launchd) Enable(name string) error {
	path := l.UnitPath(name)

	if out, err := runCmd("launchctl", "bootstrap", l.domain(), path); err == nil {
		_ = out
		return nil
	}

	_ = l.Disable(name)

	out, err := runCmd("launchctl", "bootstrap", l.domain(), path)
	if err == nil {
		return nil
	}

	if _, legacyErr := runCmd("launchctl", "load", "-w", path); legacyErr == nil {
		return nil
	}

	// The message carries the second bootstrap's output, which is the
	// informative one; `load -w`'s failure is almost always just "unsupported".
	return fmt.Errorf("launchctl bootstrap failed: %s", strings.TrimSpace(out))
}

// Disable unloads the agent. A task the scheduler no longer has must still
// disable cleanly: CLI rm calls this before deleting the units, so raising on
// an already-absent agent would strand the store entry with no way to remove it.
func (l *Launchd) Disable(name string) error {
	_, _ = runCmd("launchctl", "bootout", l.domain()+"/"+l.Label(name))
	return nil
}

func (l *Launchd) DeleteUnits(name string) error {
	err := os.Remove(l.UnitPath(name))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *Launchd) Loaded(name string) bool {
	_, err := runCmd("launchctl", "print", l.domain()+"/"+l.Label(name))
	return err == nil
}

// LoadedNames lists every loaded agent in ONE call, rather than one `print` per
// task -- `list` and `doctor` then test membership instead of forking a
// subprocess for each.
func (l *Launchd) LoadedNames() ([]string, error) {
	out, err := runCmd("launchctl", "list")
	if err != nil {
		// A soft failure: an unreachable launchd means "nothing is loaded",
		// which is what doctor should report rather than a crash.
		return nil, nil
	}
	return parseLabels(out), nil
}

// parseLabels pulls every-owned labels out of `launchctl list` output, whose
// rows are "PID<TAB>Status<TAB>Label".
func parseLabels(out string) []string {
	const prefix = "com.every."
	var names []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		label := strings.TrimSpace(fields[len(fields)-1])
		if strings.HasPrefix(label, prefix) {
			// Replace only the first occurrence, as Ruby's sub did.
			names = append(names, strings.Replace(label, prefix, "", 1))
		}
	}
	return names
}

// runCmd runs a command and returns its merged output.
func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Render returns what Write would produce, without writing it. Used by the
// migration to decide whether a unit on disk is stale.
func (l *Launchd) Render(name string, s *schedule.Schedule) (string, error) {
	return l.PlistXML(name, s), nil
}
