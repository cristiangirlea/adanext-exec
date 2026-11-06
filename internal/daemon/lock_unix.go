//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes a lock on path without waiting, exclusive or shared; busy says another holds
// one that excludes it. The lock goes with the file, or the process, however it ends.
func tryLock(path string, exclusive bool) (unlock func(), busy bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, false, nil
}

// ownerOnlyDir refuses a directory another user owns, and takes away any access for others.
func ownerOnlyDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

func ownerOnlySocket(socket string) error { return os.Chmod(socket, 0o600) }

// ownedByUser refuses a path another user owns.
func ownedByUser(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", path)
	}
	return nil
}

// refused reports whether a dial failed because nothing serves the socket.
func refused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
