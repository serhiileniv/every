package cli

import (
	"fmt"
	"time"

	"github.com/serhiileniv/every/internal/backend"
	"github.com/serhiileniv/every/internal/jsonx"
	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
	"github.com/serhiileniv/every/internal/tail"
	"github.com/serhiileniv/every/internal/ui"
)

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
	rest, asJSON := removeFlag(args, "--json")
	rest, onlyFailing := removeFlag(rest, "--failing")
	if err := rejectUnknownFlags(rest); err != nil {
		return err
	}
	if err := rejectExtraArgs(rest); err != nil {
		return err
	}

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
		r := c.buildRecord(s, name, task, loaded)
		if onlyFailing && !isFailing(r.status) {
			continue
		}
		records = append(records, r)
	}

	// An empty result is an answer, not an error: --failing asks a question,
	// and "nothing is failing" is the good outcome. Exit stays 0.
	if len(records) == 0 && onlyFailing {
		if asJSON {
			fmt.Fprintln(c.Stdout, "[]")
		} else {
			fmt.Fprintln(c.Stdout, "nothing failing")
		}
		return nil
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

	status := taskStatus(task.Paused, scheduled, lastExit)
	// A one-shot past its moment is neither "ok" nor "unscheduled", whatever
	// its last manual run did and whether or not the agent is still loaded.
	// Paused still wins: that one is doing what was asked of it.
	if overdue := c.onceStatus(name, sched); overdue != "" && !task.Paused {
		status = overdue
	}

	r := record{
		name: name, schedule: sched.Raw, command: task.Cmd,
		paused: task.Paused, scheduled: scheduled,
		status: status,
		last:   last, nextHuman: "—",
	}
	if scheduled {
		r.nextHuman = c.nextDisplay(name, sched, last)
		r.nextISO = c.nextISO(sched, last)
	}
	return r
}

// catchesUpMissed is the backend's answer, tolerating a CLI built without one
// (the docs test constructs such a thing).
func (c *CLI) catchesUpMissed() bool {
	if c.Backend == nil {
		return false
	}
	return backend.CatchesUpMissed(c.Backend)
}

// onceOverdue is the status of a one-shot whose moment has gone by while the
// task is still in the store. Firing retires it, so still being here means it
// is firing right now, or never fired. Empty for every other schedule, and
// inside schedule.OnceGrace, where the scheduler may not have spawned it yet.
//
// Two answers for one that never fired, because the schedulers differ: launchd
// drops a trigger it was powered off across, while systemd and Task Scheduler
// run it late. "missed" on those two would report a task as lost that the
// scheduler intends to run.
func onceOverdue(sched *schedule.Schedule, now time.Time, catchesUp, running bool) string {
	if sched.Kind != schedule.Once || sched.At.After(now) {
		return ""
	}
	if running {
		return "running"
	}
	if !sched.OnceOverdue(now) {
		return ""
	}
	if catchesUp {
		return "late"
	}
	return "missed"
}

// onceStatus is onceOverdue for a stored task. The run lock is only probed for
// a one-shot whose moment has come, so recurring tasks cost nothing.
func (c *CLI) onceStatus(name string, sched *schedule.Schedule) string {
	running := sched.Kind == schedule.Once && !sched.At.After(c.Now()) &&
		store.Running(c.Dirs.Data, name)
	return onceOverdue(sched, c.Now(), c.catchesUpMissed(), running)
}

// nextDisplay is the NEXT column.
//
// A calendar schedule has a real answer. An interval one does not -- the
// scheduler decides -- so it is estimated from the last run, and reads "soon"
// when there has not been one yet.
func (c *CLI) nextDisplay(name string, sched *schedule.Schedule, last *store.Run) string {
	if sched.Kind == schedule.Interval {
		lt := safeTime(last)
		if lt.IsZero() {
			return "soon"
		}
		due := lt.Add(time.Duration(sched.Interval.Int64()) * time.Second)
		return humanFuture(due, c.Now())
	}
	next := sched.NextRun(c.Now())
	if next.IsZero() {
		// A once task still in the store after its moment was not fired -- the
		// machine was off across it, most likely. Whether it still will is the
		// scheduler's answer, not ours; there is no instant to print either
		// way, since a catch-up happens at the next boot rather than at a time
		// anyone can name.
		switch s := c.onceStatus(name, sched); s {
		case "missed", "late":
			return s
		case "running", "":
			if sched.Kind == schedule.Once {
				return "now"
			}
		}
		return "?"
	}
	return humanFuture(next, c.Now())
}

func (c *CLI) nextISO(sched *schedule.Schedule, last *store.Run) string {
	if sched.Kind == schedule.Interval {
		lt := safeTime(last)
		if lt.IsZero() {
			return ""
		}
		return lt.Add(time.Duration(sched.Interval.Int64()) * time.Second).Format(time.RFC3339)
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
			lastStr = humanPast(lt, c.Now())
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
	//
	// On stderr, because stdout is the data channel: `every list > tasks.txt`
	// must capture the table and nothing else. That is also why it needs no
	// --json gate -- the object on stdout is untouched either way.
	if anyUnscheduled {
		fmt.Fprintln(c.Stderr, "\n· some tasks aren't loaded in the scheduler — `every resume <name>` to fix, or `every doctor`")
	}
	return nil
}

// isFailing is what --failing selects: a task that needs attention.
//
// "paused" is excluded because a paused task is doing exactly what was asked
// of it, and "·" because a task that has not run yet has not failed. Both
// would otherwise turn --failing into "everything that is not ok", which is a
// different and much noisier question. "running" and "late" are one-shots the
// scheduler is running or still means to run.
func isFailing(status string) bool {
	switch status {
	case "ok", "paused", "·", "running", "late":
		return false
	}
	return true
}
