package cli

import (
	"fmt"
	"time"

	"github.com/serhiileniv/every/internal/schedule"
	"github.com/serhiileniv/every/internal/store"
)

// TaskView is the full description of one task: everything stored, plus what
// can only be computed -- whether the scheduler actually has it, and when it
// runs next.
//
// Field order is the emitted key order. New fields go at the END, always: a
// consumer indexing by position is doing something unsupported, but one
// diffing two versions of this output should see additions rather than
// rearrangement.
type TaskView struct {
	Name      string           `json:"name"`
	Schedule  string           `json:"schedule"`
	Command   string           `json:"command"`
	Cwd       string           `json:"cwd"`
	CreatedAt string           `json:"created_at"`
	Paused    bool             `json:"paused"`
	Quiet     bool             `json:"quiet"`
	Timeout   int              `json:"timeout"`
	OnFail    string           `json:"on_fail,omitempty"`
	Scheduled bool             `json:"scheduled"`
	Status    string           `json:"status"`
	Last      *runView         `json:"last"`
	Next      *string          `json:"next"`
	Kind      string           `json:"kind"`
	Entries   []schedule.Entry `json:"entries,omitempty"`
	Interval  *int64           `json:"interval_seconds,omitempty"`
	UnitPath  string           `json:"unit_path"`
	// At is the instant of a once task, RFC 3339. Appended, per the rule above.
	At *string `json:"at,omitempty"`
}

type runView struct {
	At      string         `json:"at"`
	Exit    int            `json:"exit"`
	Seconds store.Duration `json:"seconds"`
}

func (c *CLI) inspect(args []string) error {
	args, asJSON := stripJSONFlag(args)
	name, err := requireName(args, "inspect <name>")
	if err != nil {
		return err
	}

	view, err := c.taskView(name)
	if err != nil {
		return err
	}

	if asJSON {
		return emitJSON(c.Stdout, view)
	}

	fmt.Fprintf(c.Stdout, "%s\n", view.Name)
	fmt.Fprintf(c.Stdout, "  schedule:  %s\n", view.Schedule)
	fmt.Fprintf(c.Stdout, "  command:   %s\n", view.Command)
	fmt.Fprintf(c.Stdout, "  directory: %s\n", view.Cwd)
	fmt.Fprintf(c.Stdout, "  status:    %s\n", view.Status)
	if view.Timeout > 0 {
		fmt.Fprintf(c.Stdout, "  timeout:   %ds\n", view.Timeout)
	}
	if view.OnFail != "" {
		fmt.Fprintf(c.Stdout, "  on fail:   %s\n", view.OnFail)
	}
	if view.Quiet {
		fmt.Fprintf(c.Stdout, "  quiet:     no failure notification\n")
	}
	if view.Last != nil {
		fmt.Fprintf(c.Stdout, "  last run:  %s (exit %d in %ss)\n",
			c.humanStamp(view.Last.At, humanPast), view.Last.Exit, view.Last.Seconds)
	} else {
		fmt.Fprintf(c.Stdout, "  last run:  never\n")
	}
	if view.Next != nil {
		fmt.Fprintf(c.Stdout, "  next run:  %s\n", c.humanStamp(*view.Next, humanFuture))
	}
	fmt.Fprintf(c.Stdout, "  unit:      %s\n", view.UnitPath)
	fmt.Fprintf(c.Stdout, "  created:   %s\n", c.humanStamp(view.CreatedAt, humanPast))
	return nil
}

// exists answers with nothing but an exit code.
//
// A verb rather than a flag on inspect: it is the call a program makes most
// often, and `every exists foo && ...` reads correctly in a shell where
// `every inspect foo --quiet >/dev/null` does not.
func (c *CLI) exists(args []string) error {
	args, _ = stripJSONFlag(args)
	name, err := requireName(args, "exists <name>")
	if err != nil {
		return err
	}
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}
	if _, ok := s.Tasks.Get(name); !ok {
		// Silent by design: the exit code IS the answer, and a program testing
		// for absence should not have to discard a message about it.
		return &exitError{code: 66, errorCode: CodeNoSuchTask, name: name,
			msg: fmt.Sprintf("every: no task %s", rubyInspect(name))}
	}
	return nil
}

// taskView assembles everything known about one task.
func (c *CLI) taskView(name string) (*TaskView, error) {
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return nil, err
	}
	task, ok := s.Tasks.Get(name)
	if !ok {
		return nil, noInputCoded(CodeNoSuchTask, name, "no task %s", rubyInspect(name))
	}

	loadedNames, err := c.Backend.LoadedNames()
	if err != nil {
		return nil, err
	}
	loaded := false
	for _, n := range loadedNames {
		if n == name {
			loaded = true
			break
		}
	}

	view := &TaskView{
		Name: name, Schedule: task.Schedule.Raw, Command: task.Cmd,
		Cwd: task.Cwd, CreatedAt: task.CreatedAt,
		Paused: task.Paused, Quiet: task.Quiet, Timeout: task.Timeout,
		OnFail: task.OnFail, Kind: task.Schedule.Kind,
		UnitPath: c.Backend.UnitPath(name),
	}

	sched, sErr := schedule.FromRecord(task.Schedule)
	last, lErr := s.LastRun(name)
	scheduled := !task.Paused && loaded
	view.Scheduled = scheduled

	if sErr != nil || lErr != nil {
		view.Status = "invalid"
		return view, nil
	}

	view.Entries = sched.Entries
	if sched.Kind == schedule.Interval {
		iv := sched.Interval.Int64()
		view.Interval = &iv
	}
	if sched.Kind == schedule.Once {
		at := sched.At.Format(time.RFC3339)
		view.At = &at
	}

	var lastExit *int
	if last != nil {
		e := last.Exit
		lastExit = &e
		view.Last = &runView{At: last.At, Exit: last.Exit, Seconds: last.Dur}
	}
	view.Status = taskStatus(task.Paused, scheduled, lastExit)
	// The same override list makes, so the two never disagree about a one-shot.
	if overdue := c.onceStatus(name, sched); overdue != "" && !task.Paused {
		view.Status = overdue
	}

	if scheduled {
		if iso := c.nextISO(sched, last); iso != "" {
			view.Next = &iso
		}
	}
	return view, nil
}

var _ = time.RFC3339

// humanStamp renders an RFC 3339 field for a person: the absolute time, then
// the relative one in parentheses.
//
// inspect is where the precise instant belongs -- `list` went relative because
// its columns are scanned, this one is read. Both are shown because "14 Sep
// 10:00" and "in 18h" answer different questions and inspect has room for both.
//
// An unparseable stamp is printed as stored. It came from the ledger, and
// showing the raw value beats inventing a prettier lie about it.
func (c *CLI) humanStamp(raw string, rel func(t, now time.Time) string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return fmt.Sprintf("%s (%s)", t.Format(absoluteFormat), rel(t, c.Now()))
}
