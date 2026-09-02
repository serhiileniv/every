package store

import (
	"os"
	"path/filepath"
)

// Lock is a held exclusive lock on the registry. Close releases it.
type Lock struct{ f *os.File }

// AcquireLock serializes the read-modify-write of the registry across
// concurrent every processes, so a second writer waits instead of clobbering
// the first's change.
//
// It blocks, with no timeout, matching Ruby's LOCK_EX. The lock file is created
// once and never written to or removed: deleting it would let a waiter acquire
// a lock on an unlinked inode while a newcomer locks the replacement, which is
// exactly the mutual exclusion this is for.
//
// Only add, rm, pause and resume take it. list, log, doctor and run deliberately
// do not -- they read, and blocking a scheduled run behind an interactive
// command would turn a UI convenience into a missed task.
func AcquireLock(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, ".lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Close releases the lock. Closing the descriptor is what releases it on both
// platforms, so an unlock call would be redundant.
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	return f.Close()
}
