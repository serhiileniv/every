package store

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/serhiileniv/every/internal/jsonx"
)

// Tasks is an insertion-ordered map of task name to task.
//
// The ordering is not a nicety. Ruby Hashes preserve insertion order, and
// `every list` iterates the registry directly, so rows print in the order
// tasks were created -- and tasks.json is written in that order too. A plain
// Go map would randomize both on every single invocation: the table would
// reshuffle between runs, `list --json` would emit a different array order each
// time, and the file would churn on every save. It is the most likely silent
// regression in the whole port, and it is invisible in a unit test that only
// checks membership.
type Tasks struct {
	names  []string
	byName map[string]*Task
}

func NewTasks() *Tasks {
	return &Tasks{byName: map[string]*Task{}}
}

// Names returns the task names in insertion order. The slice is a copy, so a
// caller cannot reorder the registry by sorting what it was handed.
func (t *Tasks) Names() []string {
	out := make([]string, len(t.names))
	copy(out, t.names)
	return out
}

func (t *Tasks) Len() int { return len(t.names) }

func (t *Tasks) Get(name string) (*Task, bool) {
	v, ok := t.byName[name]
	return v, ok
}

// Set replaces a task, keeping its existing position if it is already present.
// A re-add under the same name must not jump to the end of the list.
func (t *Tasks) Set(name string, task *Task) {
	if _, exists := t.byName[name]; !exists {
		t.names = append(t.names, name)
	}
	t.byName[name] = task
}

func (t *Tasks) Delete(name string) {
	if _, exists := t.byName[name]; !exists {
		return
	}
	delete(t.byName, name)
	for i, n := range t.names {
		if n == name {
			t.names = append(t.names[:i], t.names[i+1:]...)
			break
		}
	}
}

// MarshalJSON writes the entries in insertion order.
func (t *Tasks) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, name := range t.names {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := jsonx.Marshal(name)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		val, err := jsonx.Marshal(t.byName[name])
		if err != nil {
			return nil, err
		}
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON reads the entries in file order, so a store round-trips
// without reordering. encoding/json would hand back a map and lose it, so the
// object is walked token by token.
func (t *Tasks) UnmarshalJSON(b []byte) error {
	t.names = nil
	t.byName = map[string]*Task{}

	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("tasks: expected an object, got %v", tok)
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("tasks: expected a string key, got %v", keyTok)
		}
		var task Task
		if err := dec.Decode(&task); err != nil {
			return fmt.Errorf("tasks: %q: %w", name, err)
		}
		t.Set(name, &task)
	}

	// Consume the closing brace so a trailing-garbage document is an error.
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}
