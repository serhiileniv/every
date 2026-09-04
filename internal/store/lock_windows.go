//go:build windows

package store

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32  = syscall.NewLazyDLL("kernel32.dll")
	procLockFile = modkernel32.NewProc("LockFileEx")
)

const lockfileExclusiveLock = 0x0002

// LockFileEx is the Windows counterpart to flock(2), and like it the lock is
// released when the handle closes -- including when the process dies. Written
// out by hand rather than taking golang.org/x/sys, which would be the module's
// only dependency and would cost the "zero dependencies" claim the whole
// rewrite exists to make literal.
func lockExclusive(f *os.File) error {
	var overlapped [4]uintptr // an OVERLAPPED, zeroed: lock from offset 0
	r, _, err := procLockFile.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock),
		0,
		1, 0, // one byte is enough; the range only has to be consistent
		uintptr(unsafe.Pointer(&overlapped[0])),
	)
	if r == 0 {
		return err
	}
	return nil
}
