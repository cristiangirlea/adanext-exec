//go:build windows

package originals

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errLockViolation        = syscall.Errno(33)
)

// tryLock takes an exclusive lock on f without waiting; busy says another holds it. The lock
// goes with the file, or the process, however it ends.
func tryLock(f *os.File) (busy bool, err error) {
	var ol syscall.Overlapped
	r, _, e := procLockFileEx.Call(f.Fd(), lockfileFailImmediately|lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		if errors.Is(e, errLockViolation) {
			return true, nil
		}
		return false, e
	}
	return false, nil
}

// ownerOnly has nothing to do on Windows, where the directory takes its parent's access list,
// as the audit's does.
func ownerOnly(*os.Root, string) error { return nil }
