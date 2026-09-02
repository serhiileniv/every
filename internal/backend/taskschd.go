package backend

import (
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/serhiileniv/every/internal/schedule"
)

// TaskScheduler schedules through the Windows Task Scheduler service.
type TaskScheduler struct {
	cfg Config
	// Now is injectable because StartBoundary is clock-derived, and the
	// fixtures are generated at a pinned instant.
	Now func() time.Time
	// User overrides the account the task runs as, for tests.
	User func() (string, error)
}

func NewTaskScheduler(cfg Config) *TaskScheduler {
	ts := &TaskScheduler{cfg: cfg, Now: time.Now}
	ts.User = ts.currentUser
	return ts
}

func (w *TaskScheduler) Name() string { return "Windows Task Scheduler" }

// taskPathPrefix groups every's tasks under one folder in the service.
const taskPathPrefix = `\every\`

var weekdayTags = [...]string{
	"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday",
}

func (w *TaskScheduler) taskDir() string {
	return filepath.Join(w.cfg.Dirs.Data, "windows-tasks")
}

func (w *TaskScheduler) TaskName(name string) string { return taskPathPrefix + name }

// UnitPath is kept as a file path for diagnostics and cleanup. The source of
// truth for registration is the Task Scheduler service, not this XML copy.
func (w *TaskScheduler) UnitPath(name string) string {
	return filepath.Join(w.taskDir(), name+".xml")
}

// ResourceExists asks the service, not the filesystem.
//
// Task Scheduler stores tasks in its own registry, so this deliberately differs
// from the file check launchd and systemd use: an XML file left behind by a
// failed delete does not mean the task is registered, and vice versa.
func (w *TaskScheduler) ResourceExists(name string) bool { return w.Loaded(name) }

func (w *TaskScheduler) currentUser() (string, error) {
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		return "", fmt.Errorf("USERNAME is not set; cannot register a per-user Windows task")
	}
	if domain := os.Getenv("USERDOMAIN"); domain != "" {
		return domain + `\` + user, nil
	}
	return user, nil
}

// ValidateSchedule rejects what this scheduler cannot express.
//
// Task Scheduler has no reliable sub-minute repetition primitive, so intervals
// below a minute are refused rather than silently rounded up into a task that
// fires at the wrong rate.
func (w *TaskScheduler) ValidateSchedule(s *schedule.Schedule) error {
	if s.Kind == schedule.Interval && s.Interval < 60 {
		return fmt.Errorf(
			"Windows Task Scheduler supports interval schedules from 1m; %s needs a future resident scheduler",
			s.HumanInterval())
	}
	return nil
}

func (w *TaskScheduler) Write(name string, s *schedule.Schedule) error {
	if err := w.ValidateSchedule(s); err != nil {
		return err
	}
	if err := os.MkdirAll(w.taskDir(), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(w.cfg.Dirs.Logs, 0o755); err != nil {
		return err
	}

	xml, err := w.TaskXML(name, s)
	if err != nil {
		return err
	}
	if err := writeUTF16(w.UnitPath(name), xml); err != nil {
		return err
	}

	out, err := runCmd("schtasks.exe", "/Create", "/TN", w.TaskName(name),
		"/XML", w.UnitPath(name), "/F")
	if err != nil {
		return fmt.Errorf("Task Scheduler registration failed: %s", strings.TrimSpace(out))
	}
	return nil
}

// writeUTF16 writes the task XML as UTF-16LE with a byte-order mark.
//
// schtasks /Create /XML hands the file to MSXML, which relies on a BOM to know
// how it is encoded. Written as plain UTF-8 with no BOM it is decoded as ANSI,
// reaches the encoding declaration and fails with "The task XML is malformed.
// (1,40)::ERROR: unable to switch the encoding" -- which made `every add`
// impossible on Windows in 0.3.0. UTF-16LE + BOM is what schtasks documents,
// and the declaration inside says UTF-16 to match.
func writeUTF16(path, content string) error {
	buf := make([]byte, 0, 2+len(content)*2)
	buf = append(buf, 0xFF, 0xFE)
	for _, u := range utf16.Encode([]rune(content)) {
		buf = binary.LittleEndian.AppendUint16(buf, u)
	}

	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// TaskXML builds the task definition.
func (w *TaskScheduler) TaskXML(name string, s *schedule.Schedule) (string, error) {
	user, err := w.User()
	if err != nil {
		return "", err
	}
	command, arguments := w.taskAction(name)
	trigger := w.triggerXML(s)

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Author>%s</Author>
    <URI>%s</URI>
  </RegistrationInfo>
  <Triggers>
%s
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>
`, xesc(user), xesc(w.TaskName(name)), trigger, xesc(user),
		xesc(command), xesc(arguments)), nil
}

// taskAction is the command the service runs.
//
// Task Scheduler Exec actions expose no per-task environment block, so
// EVERY_HOME is set inline by the command processor before the binary is
// called. That is what makes a custom data directory behave the same here as
// the environment blocks do under launchd and systemd.
//
// The Ruby needed a generated wrapper script for this and a two-branch split
// between it and the installer's .cmd shim. With a single binary there is
// nothing to interpret, so both are gone.
func (w *TaskScheduler) taskAction(name string) (command, arguments string) {
	comspec := os.Getenv("COMSPEC")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	args := []string{
		"/d", "/s", "/c",
		"set", windowsQuote("EVERY_HOME=" + w.cfg.Dirs.Data),
		"&&", "call", windowsQuote(w.cfg.Launcher), "run", windowsQuote(name),
	}
	return comspec, strings.Join(args, " ")
}

// windowsQuote wraps a value for the command processor, escaping inner quotes.
func windowsQuote(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
}

// xmlTime is local wall-clock with no offset and no fractional part, which is
// the form Task Scheduler expects in a StartBoundary.
func xmlTime(t time.Time) string { return t.Format("2006-01-02T15:04:05") }

func (w *TaskScheduler) triggerXML(s *schedule.Schedule) string {
	now := w.Now()

	if s.Kind == schedule.Interval {
		return fmt.Sprintf(`<TimeTrigger>
  <StartBoundary>%s</StartBoundary>
  <Enabled>true</Enabled>
  <Repetition>
    <Interval>PT%dS</Interval>
    <StopAtDurationEnd>false</StopAtDurationEnd>
  </Repetition>
</TimeTrigger>
`, xmlTime(now.Add(time.Duration(s.Interval)*time.Second)), s.Interval)
	}

	var b strings.Builder
	for _, e := range s.Entries {
		b.WriteString(w.calendarTriggerXML(s, e, now))
	}
	return b.String()
}

func (w *TaskScheduler) calendarTriggerXML(s *schedule.Schedule, e schedule.Entry, now time.Time) string {
	var recurrence string
	if e.Weekday != nil {
		tag := weekdayTags[((*e.Weekday%7)+7)%7]
		recurrence = fmt.Sprintf(`<ScheduleByWeek>
  <DaysOfWeek><%s/></DaysOfWeek>
  <WeeksInterval>1</WeeksInterval>
</ScheduleByWeek>
`, tag)
	} else {
		recurrence = "<ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay>\n"
	}

	return fmt.Sprintf(`<CalendarTrigger>
  <StartBoundary>%s</StartBoundary>
  <Enabled>true</Enabled>
%s
</CalendarTrigger>
`, xmlTime(s.NextForEntry(e, now)), recurrence)
}

func (w *TaskScheduler) Enable(name string) error {
	out, err := runCmd("schtasks.exe", "/Change", "/TN", w.TaskName(name), "/ENABLE")
	if err != nil {
		return fmt.Errorf("Task Scheduler enable failed: %s", strings.TrimSpace(out))
	}
	return nil
}

// Disable is guarded on Loaded so a task the service no longer has still
// disables cleanly. rm calls this before deleting, so failing on an
// already-absent task would strand the store entry with no way to remove it.
func (w *TaskScheduler) Disable(name string) error {
	out, err := runCmd("schtasks.exe", "/Change", "/TN", w.TaskName(name), "/DISABLE")
	if err != nil && w.Loaded(name) {
		return fmt.Errorf("Task Scheduler disable failed: %s", strings.TrimSpace(out))
	}
	return nil
}

// DeleteUnits removes the task from the service and then its files.
//
// A failed delete that left the task registered would keep it firing at a
// launcher we are about to remove, with no `every` record to find it by. A
// failure because it was already gone is not an error.
func (w *TaskScheduler) DeleteUnits(name string) error {
	out, err := runCmd("schtasks.exe", "/Delete", "/TN", w.TaskName(name), "/F")
	if err != nil && w.Loaded(name) {
		return fmt.Errorf("Task Scheduler delete failed: %s", strings.TrimSpace(out))
	}
	if err := os.Remove(w.UnitPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (w *TaskScheduler) Loaded(name string) bool {
	_, err := runCmd("schtasks.exe", "/Query", "/TN", w.TaskName(name), "/FO", "LIST")
	return err == nil
}

// taskStateScript queries the service through PowerShell.
//
// No -TaskPath filter: it throws when nothing matches, and "no every tasks yet"
// is the normal state on a fresh install, not an error. Asking for everything
// fails only when the service really is broken, which is the one case the
// caller wants to hear about. parseTaskStates keeps just the \every\ rows.
//
// A raw string literal, and tested for stray control bytes: written as an
// interpolating one, a path literal like '\every\' silently becomes an ESC byte
// and the query can never match.
const taskStateScript = `$ErrorActionPreference = 'Stop'
Get-ScheduledTask | Select-Object TaskPath, TaskName, State |
  ConvertTo-Csv -NoTypeInformation`

func (w *TaskScheduler) LoadedNames() ([]string, error) {
	ps := os.Getenv("EVERY_POWERSHELL")
	if ps == "" {
		ps = "powershell.exe"
	}
	out, err := runCmd(ps, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", taskStateScript)
	if err != nil {
		// Unlike launchd and systemd this is hard: the query is designed to
		// succeed even with no tasks, so a failure means the service is broken
		// and reporting "nothing scheduled" would be a lie.
		return nil, fmt.Errorf("Task Scheduler state query failed: %s", strings.TrimSpace(out))
	}
	return enabledNames(parseTaskStates(out)), nil
}

// TaskState is one row of the PowerShell query.
type TaskState struct{ Name, State string }

// parseTaskStates reads ConvertTo-Csv output with TaskPath, TaskName and State
// columns.
//
// State is a stable enum property. Unlike schtasks' text status column it does
// not change with the user's display language, which is how a query that could
// never match shipped in 0.3.0-rc.
func parseTaskStates(out string) []TaskState {
	r := csv.NewReader(strings.NewReader(out))
	// LazyQuotes matches Ruby's liberal_parsing. FieldsPerRecord=-1 is
	// additionally required because Go rejects ragged rows by default and Ruby
	// does not.
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	rows, err := r.ReadAll()
	if err != nil && len(rows) == 0 {
		// Matching Ruby, a malformed document yields nothing rather than an
		// error: LoadedNames already failed loudly if the query itself failed.
		return nil
	}

	var states []TaskState
	for _, row := range rows {
		if len(row) < 2 || row[0] == "TaskPath" {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(row[0], "\uFEFF"))
		name := strings.TrimSpace(row[1])
		full := path + name
		// Case-insensitive: the service normalizes the folder's case.
		if !strings.HasPrefix(strings.ToLower(full), strings.ToLower(taskPathPrefix)) {
			continue
		}
		short := full[len(taskPathPrefix):]
		if short == "" {
			continue
		}
		state := ""
		if len(row) > 2 {
			state = strings.TrimSpace(row[2])
		}
		states = append(states, TaskState{Name: short, State: state})
	}
	return states
}

// enabledNames keeps everything that is not explicitly Disabled -- Ready,
// Running, Queued and Unknown all count as scheduled.
func enabledNames(states []TaskState) []string {
	var names []string
	for _, s := range states {
		if strings.EqualFold(s.State, "Disabled") {
			continue
		}
		names = append(names, s.Name)
	}
	return names
}

// SchedulerStatus is what doctor inspects to decide whether the service runs.
func (w *TaskScheduler) SchedulerStatus() (string, error) {
	return runCmd("sc.exe", "query", "Schedule")
}

// Render returns the task XML.
//
// Note this is compared against the UTF-16 file on disk, so the migration's
// caller reads that back as text; see internal/migrate. The StartBoundary is
// clock-derived and therefore always differs, which means a Windows task is
// rewritten on every migration pass rather than only when stale. That is
// acceptable -- the pass runs once per version change, guarded by the stamp --
// and the alternative is comparing everything except one element, which would
// silently stop noticing real drift.
func (w *TaskScheduler) Render(name string, s *schedule.Schedule) (string, error) {
	return w.TaskXML(name, s)
}
