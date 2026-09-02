package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/serhiileniv/every/internal/schedule"
)

// Systemd schedules through systemd user timers: a service and a timer per task.
type Systemd struct{ cfg Config }

func NewSystemd(cfg Config) *Systemd { return &Systemd{cfg: cfg} }

func (s *Systemd) Name() string { return "systemd" }

var systemdDays = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

func (s *Systemd) unitBase(name string) string { return "every-" + name }

func (s *Systemd) UnitPath(name string) string {
	return filepath.Join(s.cfg.Dirs.Config, s.unitBase(name)+".timer")
}

func (s *Systemd) servicePath(name string) string {
	return filepath.Join(s.cfg.Dirs.Config, s.unitBase(name)+".service")
}

// ResourceExists checks the timer only: it is the unit that schedules, and a
// service without one is inert rather than half-registered.
func (s *Systemd) ResourceExists(name string) bool {
	_, err := os.Stat(s.UnitPath(name))
	return err == nil
}

func (s *Systemd) Write(name string, sched *schedule.Schedule) error {
	if err := os.MkdirAll(s.cfg.Dirs.Config, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(s.cfg.Dirs.Logs, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.servicePath(name), []byte(s.ServiceUnit(name)), 0o644); err != nil {
		return err
	}
	return os.WriteFile(s.UnitPath(name), []byte(s.TimerUnit(name, sched)), 0o644)
}

// ServiceUnit is what the timer triggers.
//
// systemd user services start from a clean environment, so the resolved data
// dir is pinned: without it a timer-spawned run recomputes the default
// directory instead of reading the store the task was added under.
//
// The quoting is asymmetric -- the launcher and name are quoted, the
// Environment= value is not -- because that is what the Ruby emitted and the
// units are compared byte for byte.
func (s *Systemd) ServiceUnit(name string) string {
	return fmt.Sprintf(`[Unit]
Description=every task %s

[Service]
Type=oneshot
Environment=EVERY_HOME=%s
ExecStart=%s run "%s"
`, name, s.cfg.Dirs.Data, quoteUnlessPlaceholder(s.cfg.Launcher), name)
}

// quoteUnlessPlaceholder wraps the launcher in double quotes, which is how the
// path reaches systemd intact when it contains spaces.
func quoteUnlessPlaceholder(launcher string) string {
	return `"` + launcher + `"`
}

// TimerUnit schedules the service.
func (s *Systemd) TimerUnit(name string, sched *schedule.Schedule) string {
	lines := []string{"[Unit]", "Description=every timer " + name, "", "[Timer]"}

	// Tighten the default one-minute slack, so sub-minute intervals and
	// calendar times fire close to when launchd would on macOS.
	lines = append(lines, "AccuracySec=1s")

	if sched.Kind == schedule.Interval {
		lines = append(lines,
			fmt.Sprintf("OnActiveSec=%d", sched.Interval),
			fmt.Sprintf("OnUnitActiveSec=%d", sched.Interval))
	} else {
		for _, c := range CalendarLines(sched) {
			lines = append(lines, "OnCalendar="+c)
		}
		// Persistent mirrors launchd's behavior of firing missed calendar runs
		// on wake or boot.
		lines = append(lines, "Persistent=true")
	}

	lines = append(lines, "", "[Install]", "WantedBy=timers.target")
	return strings.Join(lines, "\n") + "\n"
}

// CalendarLines renders each entry as a systemd OnCalendar expression.
func CalendarLines(sched *schedule.Schedule) []string {
	out := make([]string, 0, len(sched.Entries))
	for _, e := range sched.Entries {
		t := fmt.Sprintf("%02d:%02d:00", e.Hour, e.Minute)
		if e.Weekday != nil {
			// The modulo clamps a legacy weekday 7 to Sunday rather than
			// indexing past the end of the table.
			out = append(out, fmt.Sprintf("%s *-*-* %s", systemdDays[((*e.Weekday%7)+7)%7], t))
			continue
		}
		out = append(out, "*-*-* "+t)
	}
	return out
}

func (s *Systemd) systemctl(args ...string) (string, error) {
	return runCmd("systemctl", append([]string{"--user"}, args...)...)
}

func (s *Systemd) Enable(name string) error {
	// The reload's result is deliberately ignored: on a host with no user bus
	// it fails, and the enable below reports that far more usefully.
	_, _ = s.systemctl("daemon-reload")

	out, err := s.systemctl("enable", "--now", s.unitBase(name)+".timer")
	if err != nil {
		return fmt.Errorf("systemctl enable failed: %s", strings.TrimSpace(out))
	}
	return nil
}

// Disable tolerates a timer the scheduler no longer has: rm disables before
// deleting, so failing here would strand the store entry.
func (s *Systemd) Disable(name string) error {
	_, _ = s.systemctl("disable", "--now", s.unitBase(name)+".timer")
	return nil
}

func (s *Systemd) DeleteUnits(name string) error {
	for _, p := range []string{s.servicePath(name), s.UnitPath(name)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_, _ = s.systemctl("daemon-reload")
	return nil
}

func (s *Systemd) Loaded(name string) bool {
	_, err := s.systemctl("is-active", "--quiet", s.unitBase(name)+".timer")
	return err == nil
}

func (s *Systemd) LoadedNames() ([]string, error) {
	out, err := s.systemctl("list-units", "--type=timer", "--state=active",
		"--no-legend", "--plain", "every-*.timer")
	if err != nil {
		// Soft failure, as on launchd: an unreachable systemd means nothing is
		// loaded, which doctor reports better than a crash would.
		return nil, nil
	}
	return parseUnits(out), nil
}

// parseUnits pulls task names out of `list-units --plain --no-legend` rows,
// which are "UNIT LOAD ACTIVE SUB DESCRIPTION".
var unitRe = regexp.MustCompile(`^every-(.+)\.timer$`)

func parseUnits(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		// Whitespace-splitting discards leading blanks, so an indented row
		// still parses.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if m := unitRe.FindStringSubmatch(fields[0]); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}
