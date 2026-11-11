//go:build windows

package originals

import (
	"errors"
	"fmt"
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

// tryLock takes an exclusive lock on path without waiting; busy says another holds it. The
// lock goes with the file, or the process, however it ends.
func tryLock(path string) (unlock func(), busy bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open the originals' lock: %w", err)
	}
	var ol syscall.Overlapped
	r, _, e := procLockFileEx.Call(f.Fd(), lockfileFailImmediately|lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		f.Close()
		if errors.Is(e, errLockViolation) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("lock %s: %w", path, e)
	}
	return func() { f.Close() }, false, nil
}

// ownerOnly has nothing to do on Windows, where the directory takes its parent's access list,
// as the audit's does.
func ownerOnly(string) error { return nil }
