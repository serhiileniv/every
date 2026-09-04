//go:build !windows

package store

import (
	"os"
	"syscall"
)

// flock(2) is released automatically when the process dies, so a crash cannot
// leave a stale lock behind. That property is why this is flock rather than a
// lock file whose existence means "held".
func lockExclusive(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		// A signal can interrupt the blocking call; that is not a failure to
		// lock, so retry rather than reporting one.
		if err == syscall.EINTR {
			continue
		}
		return err
	}
}
