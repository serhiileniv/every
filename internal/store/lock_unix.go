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

func lockShared(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
		if err == syscall.EINTR {
			continue
		}
		return err
	}
}

// tryLockExclusive reports false, not an error, when another descriptor holds
// the lock. On success the probe's lock is released before returning.
func tryLockExclusive(f *os.File) (held bool, err error) {
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch err {
		case syscall.EINTR:
			continue
		case nil:
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			return false, nil
		case syscall.EWOULDBLOCK:
			return true, nil
		}
		return false, err
	}
}
