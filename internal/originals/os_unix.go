//go:build !windows

package originals

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes an exclusive lock on path without waiting; busy says another holds it. The
// lock goes with the file, or the process, however it ends.
func tryLock(path string) (unlock func(), busy bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open the originals' lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, false, nil
}

// ownerOnly refuses a directory another user owns, and takes away any access for others.
func ownerOnly(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}
