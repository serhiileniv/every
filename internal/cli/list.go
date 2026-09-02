package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/serhiileniv/every/internal/jsonx"
	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
	"github.com/serhiileniv/every/internal/tail"
	"github.com/serhiileniv/every/internal/ui"
)

func parseInt(s string) (int, error) { return strconv.Atoi(s) }

func tailLines(path string, n int) ([]string, error) { return tail.Lines(path, n) }

// record is one computed row, shared by the table and the JSON renderer so the
// two can never disagree about a task's status.
type record struct {
	name      string
	schedule  string
	command   string
	paused    bool
	scheduled bool
	status    string
	last      *store.Run
	nextHuman string
	nextISO   string
}

// jsonRecord is the `list --json` wire format.
//
// This is a public API: the man page ships a jq example against it and all
// three completion scripts scrape "name":"..." with a regex. Field order and
// the compact encoding are therefore frozen, not stylistic.
type jsonRecord struct {
	Name      string    `json:"name"`
	Schedule  string    `json:"schedule"`
	Command   string    `json:"command"`
	Paused    bool      `json:"paused"`
	Scheduled bool      `json:"scheduled"`
	Status    string    `json:"status"`
	Last      *jsonLast `json:"last"`
	Next      *string   `json:"next"`
}

type jsonLast struct {
	At      string         `json:"at"`
	Exit    int            `json:"exit"`
	Seconds store.Duration `json:"seconds"`
}

func (c *CLI) list(args []string) error {
	_, asJSON := removeFlag(args, "--json")

	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}

	if s.Tasks.Len() == 0 {
		if asJSON {
			fmt.Fprintln(c.Stdout, "[]")
		} else {
			fmt.Fprintln(c.Stdout, "no tasks yet — try: every day 9am -- brew update")
		}
		return nil
	}

	// One scheduler query for all tasks, rather than one subprocess per task.
	loadedNames, err := c.Backend.LoadedNames()
	if err != nil {
		return err
	}
	loaded := map[string]bool{}
	for _, n := range loadedNames {
		loaded[n] = true
	}

	records := make([]record, 0, s.Tasks.Len())
	for _, name := range s.Tasks.Names() {
		task, _ := s.Tasks.Get(name)
		records = append(records, c.buildRecord(s, name, task, loaded))
	}

	if asJSON {
		return c.renderJSON(records)
	}
	return c.renderTable(records)
}

// buildRecord computes one row.
//
// Anything that goes wrong for a single task -- an unreadable schedule, a
// forward-incompatible record, a corrupt timestamp -- produces one "invalid"
// row rather than aborting the whole listing. One bad record must never hide
// every other task.
func (c *CLI) buildRecord(s *store.Store, name string, task *store.Task, loaded map[string]bool) record {
	invalid := record{
		name: name, schedule: task.Schedule.Raw, command: task.Cmd,
		status: "invalid", nextHuman: "—",
	}
	if invalid.schedule == "" {
		invalid.schedule = "?"
	}

	sched, err := schedule.FromRecord(task.Schedule)
	if err != nil {
		return invalid
	}

	last, err := s.LastRun(name)
	if err != nil {
		return invalid
	}

	scheduled := !task.Paused && loaded[name]
	var lastExit *int
	if last != nil {
		e := last.Exit
		lastExit = &e
	}

	r := record{
		name: name, schedule: sched.Raw, command: task.Cmd,
		paused: task.Paused, scheduled: scheduled,
		status: taskStatus(task.Paused, scheduled, lastExit),
		last:   last, nextHuman: "—",
	}
	if scheduled {
		r.nextHuman = c.nextDisplay(sched, last)
		r.nextISO = c.nextISO(sched, last)
	}
	return r
}

// nextDisplay is the NEXT column.
//
// A calendar schedule has a real answer. An interval one does not -- the
// scheduler decides -- so it is estimated from the last run, and reads "soon"
// when there has not been one yet.
func (c *CLI) nextDisplay(sched *schedule.Schedule, last *store.Run) string {
	if sched.Kind == schedule.Interval {
		lt := safeTime(last)
		if lt.IsZero() {
			return "soon"
		}
		return lt.Add(time.Duration(sched.Interval) * time.Second).Format("02 Jan 15:04")
	}
	next := sched.NextRun(c.Now())
	if next.IsZero() {
		return "?"
	}
	return next.Format("02 Jan 15:04")
}

func (c *CLI) nextISO(sched *schedule.Schedule, last *store.Run) string {
	if sched.Kind == schedule.Interval {
		lt := safeTime(last)
		if lt.IsZero() {
			return ""
		}
		return lt.Add(time.Duration(sched.Interval) * time.Second).Format(time.RFC3339)
	}
	next := sched.NextRun(c.Now())
	if next.IsZero() {
		return ""
	}
	return next.Format(time.RFC3339)
}

// safeTime parses a ledger timestamp, returning the zero time for anything
// unparseable. A corrupt timestamp must not kill `list`.
func safeTime(last *store.Run) time.Time {
	if last == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, last.At)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (c *CLI) renderJSON(records []record) error {
	out := make([]jsonRecord, 0, len(records))
	for _, r := range records {
		j := jsonRecord{
			Name: r.name, Schedule: r.schedule, Command: r.command,
			Paused: r.paused, Scheduled: r.scheduled, Status: r.status,
		}
		if r.last != nil {
			j.Last = &jsonLast{At: r.last.At, Exit: r.last.Exit, Seconds: r.last.Dur}
		}
		if r.nextISO != "" {
			next := r.nextISO
			j.Next = &next
		}
		out = append(out, j)
	}

	b, err := jsonx.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.Stdout, string(b))
	return nil
}

func (c *CLI) renderTable(records []record) error {
	tbl := ui.Table{Headers: []string{"NAME", "SCHEDULE", "LAST", "STATUS", "NEXT"}}
	anyUnscheduled := false

	for _, r := range records {
		lastStr := "—"
		if lt := safeTime(r.last); !lt.IsZero() {
			lastStr = lt.Format("02 Jan 15:04")
		}
		tbl.Rows = append(tbl.Rows, []string{r.name, r.schedule, lastStr, r.status, r.nextHuman})
		if r.status == "unscheduled" {
			anyUnscheduled = true
		}
	}

	if err := tbl.Render(c.Stdout, c.Color); err != nil {
		return err
	}

	// The hint exists because "unscheduled" is the one status a user cannot act
	// on without being told how.
	if anyUnscheduled {
		fmt.Fprintln(c.Stdout, "\n· some tasks aren't loaded in the scheduler — `every resume <name>` to fix, or `every doctor`")
	}
	return nil
}
