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
			view.Last.At, view.Last.Exit, view.Last.Seconds)
	} else {
		fmt.Fprintf(c.Stdout, "  last run:  never\n")
	}
	if view.Next != nil {
		fmt.Fprintf(c.Stdout, "  next run:  %s\n", *view.Next)
	}
	fmt.Fprintf(c.Stdout, "  unit:      %s\n", view.UnitPath)
	fmt.Fprintf(c.Stdout, "  created:   %s\n", view.CreatedAt)
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

	var lastExit *int
	if last != nil {
		e := last.Exit
		lastExit = &e
		view.Last = &runView{At: last.At, Exit: last.Exit, Seconds: last.Dur}
	}
	view.Status = taskStatus(task.Paused, scheduled, lastExit)

	if scheduled {
		if iso := c.nextISO(sched, last); iso != "" {
			view.Next = &iso
		}
	}
	return view, nil
}

var _ = time.RFC3339
