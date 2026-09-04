package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/serhiileniv/every/internal/jsonx"
	"github.com/serhiileniv/every/internal/store"
	"github.com/serhiileniv/every/internal/tail"
)

// appendLog writes the human-readable record: a header line, then the output.
func (r *Runner) appendLog(name string, started time.Time, exitCode int, duration float64, out []byte) error {
	path := filepath.Join(r.Dirs.Logs, name+".log")
	if err := rotate(path); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	header := fmt.Sprintf("=== %s exit=%d dur=%ss ===\n",
		started.Format("2006-01-02 15:04:05"), exitCode, store.Duration(duration))
	if _, err := f.WriteString(header); err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		return err
	}
	// One trailing newline, and only when the output did not already end with
	// one -- so the next header always starts at column zero without ever
	// inserting a blank line.
	if len(out) > 0 && !bytes.HasSuffix(out, []byte("\n")) {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	return nil
}

// rotate moves the log aside once it crosses the size cap. One generation only:
// the detailed log is a convenience, the ledger is the durable history.
func rotate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() <= maxLogBytes {
		return nil
	}
	return os.Rename(path, path+".old")
}

// appendRun adds one line to the task's JSONL ledger, then bounds it.
func (r *Runner) appendRun(name string, started time.Time, exitCode int, duration float64) error {
	path := filepath.Join(r.Dirs.Runs, name+".jsonl")

	line, err := jsonx.Marshal(store.Run{
		At: started.Format(time.RFC3339), Exit: exitCode, Dur: store.Duration(duration),
	})
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return trimRuns(path)
}

// trimRuns bounds the ledger.
//
// The run ledger is a rolling window: enough history for status and a staleness
// watchdog, but bounded so a task firing every minute for years cannot grow it
// without limit and slow `list` down linearly over months.
//
// Amortized-cheap: only touched once the file crosses the byte cap, then
// rewritten to the last maxRunRecords lines. The guarantee is bounded file
// SIZE, not an exact line count.
func trimRuns(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() <= runTrimBytes {
		return nil
	}

	lines, err := tail.Lines(path, maxRunRecords)
	if err != nil {
		return err
	}
	if len(lines) <= 0 {
		return nil
	}

	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	// Clean up on any failure path; after a successful rename this is a no-op.
	defer os.Remove(tmp)

	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "")), 0o644); err != nil {
		return err
	}
	// Rename rather than a copy: a crash mid-trim must not truncate history.
	// The error is returned rather than swallowed -- a failed replacement would
	// otherwise silently drop the freshly written trimmed ledger.
	return os.Rename(tmp, path)
}

// appendLogRaw appends text to a task's log without a run header.
//
// Used by the on-fail callback, whose output belongs beside the failure that
// triggered it rather than in a file of its own. Rotation still applies, so a
// noisy callback cannot grow the log past its cap either.
func (r *Runner) appendLogRaw(name, text string) error {
	path := filepath.Join(r.Dirs.Logs, name+".log")
	if err := rotate(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}
