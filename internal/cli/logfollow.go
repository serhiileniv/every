package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"
)

// Following a task's log while it runs.
//
// Polling rather than fsevents/inotify/ReadDirectoryChangesW: the writer is a
// different process, the platforms are three, and at human reading speed a
// 200 ms poll is indistinguishable from instant. `tail -f` itself polls on most
// platforms. The dependency-free build is worth more than the latency.
const (
	followPoll = 200 * time.Millisecond

	// followWaitPoll is the slower beat used before the log exists at all.
	// Nothing can arrive until the task runs, and that may be hours.
	followWaitPoll = time.Second

	// readChunkBytes is the copy buffer. The runner has its own; this is a
	// different process reading a different end of the same file.
	readChunkBytes = 16 * 1024
)

// followLog prints the tail, then streams what is appended until interrupted.
func (c *CLI) followLog(name string, n int) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	stop := make(chan struct{})
	go func() {
		<-sig
		close(stop)
	}()
	return c.follow(name, n, stop)
}

// follow is followLog with the stop condition injected, so a test can end it
// without raising a real signal.
//
// Returns nil on stop. Ctrl-C out of a follow is how following ENDS, not a
// failure: `timeout 5 every log x -f` in a script must not look like an error.
func (c *CLI) follow(name string, n int, stop <-chan struct{}) error {
	// The tail first, the way tail -f does: the last thing that happened is
	// context for the next thing, and a follow that opens with a blank screen
	// looks broken on a task that is between runs.
	//
	// hadTail also decides where streaming starts. When the log was already
	// there its tail has just been printed, so streaming resumes at the end.
	// When it was not, everything the file arrives holding is new and unseen,
	// and starting at the end would silently swallow the very first run -- the
	// one the user started following in order to watch.
	hadTail := c.logExists(name)
	if hadTail {
		lines, err := c.tailLog(name, n)
		if err != nil {
			return err
		}
		fmt.Fprint(c.Stdout, joinLines(lines))
	} else if err := c.waitForLog(name, stop); err != nil {
		return err
	}

	f, err := os.Open(c.logPath(name))
	if err != nil {
		// The log was rotated or removed between the two steps above. Nothing
		// to stream from; the tail already printed is the honest answer.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	if hadTail {
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return err
		}
	}

	for {
		if err := c.drain(f); err != nil {
			return err
		}
		reopened, err := c.reopenIfRotated(f, name)
		if err != nil {
			return err
		}
		if reopened != nil {
			f.Close()
			f = reopened
			continue // read the new generation before sleeping
		}

		select {
		case <-stop:
			return nil
		case <-time.After(followPoll):
		}
	}
}

// waitForLog blocks until the task's log appears. A task that has never run has
// no log yet, and under -f that is a thing to wait for rather than the
// `no_logs` error a plain `every log` correctly reports.
func (c *CLI) waitForLog(name string, stop <-chan struct{}) error {
	for !c.logExists(name) {
		select {
		case <-stop:
			return errFollowStopped
		case <-time.After(followWaitPoll):
		}
	}
	return nil
}

// errFollowStopped unwinds a wait that was interrupted. Never reaches the user:
// follow's callers turn it back into a clean exit.
var errFollowStopped = fmt.Errorf("follow stopped")

// drain copies everything available right now.
func (c *CLI) drain(f *os.File) error {
	buf := make([]byte, readChunkBytes)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if _, wErr := c.Stdout.Write(buf[:n]); wErr != nil {
				return wErr
			}
		}
		if err == io.EOF || n == 0 {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// reopenIfRotated returns a fresh handle when the live log is no longer the
// file we hold, and nil when nothing changed.
//
// Two cases, both real: rotation renames the log aside at 5 MB and starts a new
// one (same path, different file), and a truncation-in-place leaves the path
// pointing at our own file with the offset now past its end. Missing the first
// means a follow that goes silent forever exactly when the task is busiest,
// which is the moment someone is most likely to be watching.
func (c *CLI) reopenIfRotated(f *os.File, name string) (*os.File, error) {
	path := c.logPath(name)
	onDisk, err := os.Stat(path)
	if err != nil {
		return nil, nil // mid-rotation; the next poll will find it
	}
	ours, err := f.Stat()
	if err != nil {
		return nil, err
	}

	if !os.SameFile(onDisk, ours) {
		fresh, err := os.Open(path)
		if os.IsNotExist(err) {
			return nil, nil
		}
		return fresh, err
	}

	// Same file, but shorter than where we are reading: truncated in place.
	if pos, err := f.Seek(0, io.SeekCurrent); err == nil && onDisk.Size() < pos {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l
	}
	return out
}
