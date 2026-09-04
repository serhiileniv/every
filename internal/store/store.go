package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/serhiileniv/every/internal/jsonx"
	"github.com/serhiileniv/every/internal/tail"
)

// Store is the task registry backed by one JSON file.
type Store struct {
	dir  string // the data dir
	path string // <dir>/tasks.json
	runs string // <dir>/runs

	Tasks *Tasks

	// extra preserves any top-level key that is not "tasks".
	//
	// Ruby parsed the whole document and only ever touched data["tasks"], so a
	// key written by a future version survived a save by an older one. Dropping
	// unknown keys would make a downgrade silently destructive, so they are
	// carried through verbatim.
	extra map[string]json.RawMessage
}

// ErrCorrupt is returned when tasks.json is not valid JSON.
//
// The CLI turns this into a bare message and exit 1, deliberately without the
// error-class suffix the generic handler appends -- Ruby used abort() here,
// which bypassed its rescue. Matching that keeps the message identical.
type ErrCorrupt struct {
	Path string
	Err  error
}

func (e *ErrCorrupt) Error() string {
	return fmt.Sprintf("%s is corrupted (%v) — fix or delete it", e.Path, e.Err)
}

func (e *ErrCorrupt) Unwrap() error { return e.Err }

// Load reads the registry. A missing file is an empty registry, not an error.
func Load(dataDir string) (*Store, error) {
	s := &Store{
		dir:   dataDir,
		path:  filepath.Join(dataDir, "tasks.json"),
		runs:  filepath.Join(dataDir, "runs"),
		Tasks: NewTasks(),
		extra: map[string]json.RawMessage{},
	}

	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &ErrCorrupt{Path: s.path, Err: err}
	}
	for k, v := range doc {
		if k == "tasks" {
			if err := json.Unmarshal(v, s.Tasks); err != nil {
				return nil, &ErrCorrupt{Path: s.path, Err: err}
			}
			continue
		}
		s.extra[k] = v
	}
	return s, nil
}

// Path is the registry file's location, for error messages.
func (s *Store) Path() string { return s.path }

// Add replaces a task wholesale and saves.
func (s *Store) Add(name string, t *Task) error {
	s.Tasks.Set(name, t)
	return s.Save()
}

// Remove deletes a task and saves. A missing name is not an error, matching
// Ruby's Hash#delete.
func (s *Store) Remove(name string) error {
	s.Tasks.Delete(name)
	return s.Save()
}

// SetPaused updates just the paused flag, which is all `pause` and `resume`
// change. Ruby's update() merged into the existing record, keeping every other
// field and its position; mutating in place does the same thing.
func (s *Store) SetPaused(name string, paused bool) error {
	t, ok := s.Tasks.Get(name)
	if !ok {
		// Ruby's update() created a record for an unknown name. No caller does
		// that -- both call sites check first -- so refusing is safer than
		// materializing a task with no command.
		return fmt.Errorf("no task %s", name)
	}
	t.Paused = paused
	return s.Save()
}

// Save writes the registry atomically.
//
// A crash mid-write must never truncate the task registry: write a temp file,
// fsync it, then rename over the target, which is atomic on the same
// filesystem. The temp file lives in the data dir for exactly that reason.
func (s *Store) Save() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}

	// Key order: "tasks" first, then any preserved unknown keys, sorted so the
	// output is stable. Ruby emitted parse order, which for a file it wrote is
	// this same order.
	doc := &bytes.Buffer{}
	doc.WriteString("{\n  \"tasks\": ")
	tasksJSON, err := jsonx.MarshalIndent(s.Tasks)
	if err != nil {
		return err
	}
	doc.Write(indentContinuation(tasksJSON))
	for _, k := range sortedKeys(s.extra) {
		doc.WriteString(",\n  ")
		key, err := jsonx.Marshal(k)
		if err != nil {
			return err
		}
		doc.Write(key)
		doc.WriteString(": ")
		doc.Write(indentContinuation(s.extra[k]))
	}
	doc.WriteString("\n}\n")

	tmp := fmt.Sprintf("%s.tmp.%d", s.path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	// Clean up the temp file on any failure path. After a successful rename it
	// no longer exists, so the Remove is a no-op.
	defer os.Remove(tmp)

	if _, err := f.Write(doc.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// os.Rename replaces an existing file on both POSIX and Windows, and
	// returns the error rather than swallowing it -- a failed replacement must
	// not silently leave the old registry in place.
	return os.Rename(tmp, s.path)
}

// indentContinuation re-indents an already-pretty JSON value that is being
// embedded one level deep. Only continuation lines shift; the first line is
// already positioned by the caller.
func indentContinuation(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\n  "))
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Small n; a simple insertion sort avoids pulling in sort for one call.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// RunsPath is the ledger file for one task.
func (s *Store) RunsPath(name string) string {
	return filepath.Join(s.runs, name+".jsonl")
}

// LastRun returns the most recent complete ledger record, or nil if there is
// none.
//
// It scans from the end for the last *complete* record: a crash mid-append can
// leave a torn final line, and reporting "no runs" because of it would be a lie
// about the thing this tool exists to tell you.
//
// The window starts small and quadruples. Normally the last line answers
// immediately; only an all-blank or all-corrupt window makes it grow, and it
// still ends up scanning the whole file in that case. A short read is the proof
// the whole file was seen, which is why tail.Lines returning fewer lines than
// asked is contract rather than incidental.
func (s *Store) LastRun(name string) (*Run, error) {
	path := s.RunsPath(name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	for n := 4; ; n *= 4 {
		lines, err := tail.Lines(path, n)
		if err != nil {
			return nil, err
		}
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var r Run
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				continue // torn or corrupt; keep looking backwards
			}
			return &r, nil
		}
		if len(lines) < n {
			return nil, nil // saw the whole file, found nothing usable
		}
	}
}
