// Package store is the task registry (one JSON file) and the run history (one
// JSONL file per task).
//
// Ported from lib/every/store.rb.
package store

import (
	"github.com/serhiileniv/every/internal/schedule"
)

// Task is one entry in tasks.json.
//
// Field order is the emitted JSON key order, and it matches the order Ruby
// inserted these keys in CLI#add. tasks.json is compared byte for byte against
// fixtures generated from the Ruby, so this is a compatibility surface rather
// than a style choice -- reordering the struct silently rewrites every user's
// file on the next save.
type Task struct {
	Cmd       string          `json:"cmd"`
	Schedule  schedule.Record `json:"schedule"`
	Cwd       string          `json:"cwd"`
	CreatedAt string          `json:"created_at"`
	Paused    bool            `json:"paused"`
	Quiet     bool            `json:"quiet"`
	Timeout   int             `json:"timeout,omitempty"`
}

// Run is one line of a task's JSONL ledger.
//
// Written by the runner, read by `list` and `doctor`. Key order is ts, exit,
// dur, as Ruby emitted it.
//
// Dur is a Duration rather than a float64 because Ruby renders an integral
// float as "12.0" and Go renders it as "12". The ledger is user-visible via
// `every list --json`, so the rendering is pinned; see duration.go.
type Run struct {
	At   string   `json:"ts"`
	Exit int      `json:"exit"`
	Dur  Duration `json:"dur"`
}
