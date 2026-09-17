package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// runDir holds one lock file per task, held for the length of each run.
const runDir = "running"

// HoldRun marks a task as running until the returned lock is closed or the
// process dies.
//
// The lock is shared, so two runs of one task can overlap exactly as they
// could before this existed; what it answers is only "is any run in progress",
// via Running. The kernel drops it when the process exits, however it exits --
// which matters on launchd, where retiring a one-shot ends the process -- so a
// killed run can never leave a task looking like it is still running.
//
// Like the registry lock, the file is never removed: a run that locked the
// unlinked inode would be invisible to Running on the replacement.
func HoldRun(dataDir, name string) (*Lock, error) {
	path, err := runLockPath(dataDir, name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockShared(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Running reports whether some process holds HoldRun for this task.
//
// It never creates anything and never blocks: a task that has never run has no
// file, and a probe that fails for any other reason answers false, because
// callers use this to decide whether unloading a task would kill it, and the
// state before this existed was "unload it".
func Running(dataDir, name string) bool {
	path, err := runLockPath(dataDir, name)
	if err != nil {
		return false
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	held, err := tryLockExclusive(f)
	return err == nil && held
}

// runLockPath refuses anything that is not a single path element. Names are
// validated long before they get here; this is not where a traversal gets in.
func runLockPath(dataDir, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", errors.New("not a task name: " + name)
	}
	return filepath.Join(dataDir, runDir, name), nil
}
