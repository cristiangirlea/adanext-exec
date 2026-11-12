//go:build !windows

package originals

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// flock is syscall.Flock; tests stand in for it.
var flock = syscall.Flock

// tryLock takes an exclusive lock on f without waiting; busy says another holds it. The lock
// goes with the file, or the process, however it ends.
func tryLock(f *os.File) (busy bool, err error) {
	// A signal may interrupt the call on some systems; that is not an answer, so ask again.
	for err = syscall.EINTR; errors.Is(err, syscall.EINTR); {
		err = flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// ownerOnly refuses a directory another user owns, and takes away any access for others, through
// the directory's own root so no link is followed.
func ownerOnly(r *os.Root, dir string) error {
	info, err := r.Stat(".")
	if err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return r.Chmod(".", 0o700)
	}
	return nil
}
