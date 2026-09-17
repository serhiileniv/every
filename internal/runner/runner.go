package runner

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/serhiileniv/every/internal/paths"
	"github.com/serhiileniv/every/internal/store"
	"github.com/serhiileniv/every/internal/ui"
)

const (
	maxLogBytes   = 5 * 1024 * 1024
	maxRunRecords = 500
	runTrimBytes  = 256 * 1024
	readChunk     = 16 * 1024

	// detachGrace is how long capture keeps reading after the command itself
	// has been reaped, for output a process it left behind is still flushing.
	// Long enough for a line in flight, short enough that a task which starts a
	// daemon finishes now instead of when the daemon does.
	detachGrace = 1 * time.Second
)

// Runner executes tasks and records the result.
type Runner struct {
	Dirs   paths.Dirs
	Stdout io.Writer
	Stderr io.Writer
	Color  ui.Color

	// Now is injectable so tests can pin the wall clock. Durations use a
	// monotonic measurement regardless, and never this.
	Now func() time.Time

	env  func(string) string
	goos string

	// Quiet suppresses the interactive echo of a run's output. Set when the
	// caller is rendering JSON: stdout carries the object, and the output
	// belongs inside it rather than printed alongside.
	Quiet bool

	// LastOutput is what the most recent Run captured, so a caller can put it
	// in a structured response without reading the log back.
	LastOutput []byte
}

// Workdir resolves where a task would run, and any note about why it moved.
// Exported for `run --dry-run`, which reports the decision without making it.
func (r *Runner) Workdir(cwd string) (string, string) { return r.workdir(cwd) }

// ShellFor is the shell a command would be run through on this platform.
func (r *Runner) ShellFor() []string {
	if r.goos == "windows" {
		return r.WindowsShell()
	}
	return r.LoginShell()
}

// New builds a Runner for the real process environment.
func New(dirs paths.Dirs, stdout, stderr io.Writer, color ui.Color) *Runner {
	return &Runner{
		Dirs: dirs, Stdout: stdout, Stderr: stderr, Color: color,
		Now: time.Now, env: os.Getenv, goos: runtime.GOOS,
	}
}

// Result is what one execution produced.
type Result struct {
	Output    []byte
	ExitCode  int
	Duration  float64
	StartedAt time.Time
}

// Run executes one task by name and returns the exit code the process should
// exit with.
//
// The ordering is load-bearing: the run is durably recorded before any
// notification can fail, so a task's history never depends on whether a
// notifier happened to be installed.
func (r *Runner) Run(name string) (int, error) {
	s, err := store.Load(r.Dirs.Data)
	if err != nil {
		return 1, err
	}
	task, ok := s.Tasks.Get(name)
	if !ok {
		fmt.Fprintf(r.Stderr, "every: unknown task %s — orphaned agent? try: every doctor\n", rubyInspect(name))
		return paths.ExitNoInput, nil
	}

	if err := os.MkdirAll(r.Dirs.Logs, 0o755); err != nil {
		return 1, err
	}
	if err := os.MkdirAll(r.Dirs.Runs, 0o755); err != nil {
		return 1, err
	}

	started := r.Now()
	// A monotonic measurement: an NTP or DST wall-clock jump mid-run cannot
	// make this negative. The ledger timestamp still uses wall-clock `started`.
	mono := time.Now()

	dir, note := r.workdir(task.Cwd)
	res, err := r.capture(task.Cmd, dir, time.Duration(task.Timeout)*time.Second)
	if err != nil {
		return 1, err
	}
	out := res.Output
	if note != "" {
		out = append([]byte(note), out...)
	}
	duration := math.Round(time.Since(mono).Seconds()*100) / 100

	if err := r.appendLog(name, started, res.ExitCode, duration, out); err != nil {
		return 1, err
	}
	if err := r.appendRun(name, started, res.ExitCode, duration); err != nil {
		return 1, err
	}
	if res.ExitCode != 0 {
		if !task.Quiet {
			r.notifyFailure(name, res.ExitCode)
		}
		r.runOnFail(name, task, res.ExitCode)
	}

	r.LastOutput = out

	// A scheduled run has no terminal, so it prints nothing; an interactive
	// `every run` echoes what happened.
	if r.Color.Enabled && !r.Quiet {
		r.Stdout.Write(out)
		summary := fmt.Sprintf("— exit %d in %ss (logged: every log %s)",
			res.ExitCode, store.Duration(duration), name)
		if res.ExitCode == 0 {
			fmt.Fprintln(r.Stdout, r.Color.Green(summary))
		} else {
			fmt.Fprintln(r.Stdout, r.Color.Red(summary))
		}
	}

	return res.ExitCode, nil
}

// capture runs the command, merging stdout and stderr, bounding the output and
// enforcing the timeout.
func (r *Runner) capture(cmd, dir string, timeout time.Duration) (Result, error) {
	return r.captureWithEnv(cmd, dir, timeout, nil)
}

// captureWithEnv is capture with extra environment entries, for the on-fail
// callback -- which needs to be told which task failed without that being
// interpolated into a shell command.
func (r *Runner) captureWithEnv(cmd, dir string, timeout time.Duration, extraEnv []string) (Result, error) {
	argv, cleanup, err := r.commandArgv(cmd)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = dir
	if len(extraEnv) > 0 {
		c.Env = append(os.Environ(), extraEnv...)
	}

	// One pipe for both streams, so interleaving is preserved exactly as the
	// command produced it. Two pipes plus a merging goroutine would not.
	pr, pw, err := os.Pipe()
	if err != nil {
		return Result{}, err
	}
	c.Stdout = pw
	c.Stderr = pw
	// The child sees EOF on stdin immediately rather than inheriting a
	// terminal it could block reading from.
	c.Stdin = nil

	setProcessGroup(c)

	if err := c.Start(); err != nil {
		pr.Close()
		pw.Close()
		return Result{}, err
	}
	// The parent's copy must close or the read below never sees EOF.
	pw.Close()

	// The deadline kills the whole process GROUP, by negative pid. That is the
	// only way to reach a shell's children, and it is also why the reaped flag
	// matters: once Wait has collected the child, its pid -- and therefore the
	// group id derived from it -- can be recycled by the kernel, and signalling
	// it would hit an unrelated process group.
	//
	// So the check and the kill happen together under the mutex, and Wait sets
	// reaped under the same one. A residual window remains where the timer wins
	// the lock at the instant the command exits on its own; that is inherent to
	// POSIX process handling without pidfd, and the implementation being
	// replaced has the same window with no guard at all.
	var (
		mu       sync.Mutex
		timedOut bool
		reaped   bool
	)
	if timeout > 0 {
		timer := time.AfterFunc(timeout, func() {
			mu.Lock()
			defer mu.Unlock()
			if reaped {
				return
			}
			timedOut = true
			terminate(c.Process)
		})
		defer timer.Stop()
	}

	// Wait runs alongside the read rather than after it.
	//
	// Reading to EOF means reading until the LAST holder of the write end lets
	// go, and that is not necessarily our child: `server &`, an ssh that forks,
	// anything that daemonizes, hands the pipe to a grandchild that outlives
	// the command. The command exits in milliseconds and the read blocks for as
	// long as the grandchild lives -- during which the scheduler will not start
	// a second copy of the task, so the task is dead with `list` still saying
	// ok. Waiting concurrently lets the reaping of our own child, rather than
	// EOF, be what ends the capture.
	//
	// The timer above is still armed across both phases, so a command that
	// closes stdout early but keeps running still has to die at the deadline.
	waitDone := make(chan struct{})
	var waitErr error
	go func() {
		err := c.Wait()
		mu.Lock()
		reaped = true
		waitErr = err
		mu.Unlock()
		close(waitDone)
		// Our child is gone; whatever still holds the pipe is not ours to wait
		// on. The grace is for output already in flight.
		_ = pr.SetReadDeadline(time.Now().Add(detachGrace))
	}()

	out := &bounded{}
	buf := make([]byte, readChunk)
	detached := false
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			out.write(buf[:n])
		}
		if err != nil {
			// The deadline only ever gets armed once the child is reaped, so
			// this is the detached-grandchild case and not a slow command.
			detached = errors.Is(err, os.ErrDeadlineExceeded)
			break
		}
	}
	pr.Close()

	<-waitDone

	mu.Lock()
	killed := timedOut
	mu.Unlock()

	switch {
	case killed:
		out.appendRaw(fmt.Sprintf("\n[every: killed after %ds timeout]\n", int(timeout.Seconds())))
	case detached:
		// Deliberately not killed: a task whose whole job is to start something
		// that outlives it is a legitimate task. Said out loud because the
		// output that follows is the grandchild's and every stopped reading it.
		out.appendRaw(fmt.Sprintf(
			"\n[every: command exited; something it started still holds the output pipe — stopped capturing after %s]\n",
			detachGrace))
	}

	return Result{
		Output:   out.bytes(),
		ExitCode: exitCodeFor(c.ProcessState, waitErr, killed),
	}, nil
}

// exitCodeFor maps a finished process to the code every reports.
//
// A contract, documented in the man page: 124 for a timeout, the child's own
// code for a clean exit, 128+signum for a signalled death, 1 when nothing
// better is known. The timeout wins even if a status was harvested.
func exitCodeFor(state *os.ProcessState, waitErr error, timedOut bool) int {
	if timedOut {
		return 124
	}
	if state == nil {
		return 1
	}
	if sig, ok := terminatingSignal(state); ok {
		return 128 + sig
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	if waitErr != nil {
		return 1
	}
	return 0
}
