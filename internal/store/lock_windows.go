//go:build windows

package store

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32    = syscall.NewLazyDLL("kernel32.dll")
	procLockFile   = modkernel32.NewProc("LockFileEx")
	procUnlockFile = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x0001
	lockfileExclusiveLock   = 0x0002
	errorLockViolation      = syscall.Errno(33)
)

// LockFileEx is the Windows counterpart to flock(2), and like it the lock is
// released when the handle closes -- including when the process dies. Written
// out by hand rather than taking golang.org/x/sys, which would be the module's
// only dependency and would cost the "zero dependencies" claim the whole
// rewrite exists to make literal.
func lockExclusive(f *os.File) error {
	return lockFileEx(f, lockfileExclusiveLock)
}

func lockShared(f *os.File) error {
	return lockFileEx(f, 0)
}

// tryLockExclusive reports false, not an error, when another handle holds the
// lock. On success the probe's lock is released before returning.
func tryLockExclusive(f *os.File) (held bool, err error) {
	err = lockFileEx(f, lockfileExclusiveLock|lockfileFailImmediately)
	switch err {
	case nil:
		var overlapped [4]uintptr
		_, _, _ = procUnlockFile.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped[0])))
		return false, nil
	case errorLockViolation, syscall.ERROR_IO_PENDING:
		return true, nil
	}
	return false, err
}

func lockFileEx(f *os.File, flags uintptr) error {
	var overlapped [4]uintptr // an OVERLAPPED, zeroed: lock from offset 0
	r, _, err := procLockFile.Call(
		f.Fd(),
		flags,
		0,
		1, 0, // one byte is enough; the range only has to be consistent
		uintptr(unsafe.Pointer(&overlapped[0])),
	)
	if r == 0 {
		return err
	}
	return nil
}
