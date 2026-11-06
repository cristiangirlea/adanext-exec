//go:build !windows

package broker

import (
	"fmt"
	"os"
	"syscall"
)

// openNoFollow: a link at the audit's name is refused, and a pipe does not block the open.
const openNoFollow = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// ownerOnly makes sure only the user can reach path: it must be the user's own, and any access
// for the group or others is taken away.
func ownerOnly(path string, mode os.FileMode) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, mode)
	}
	return nil
}

// onlyOwnerWrites refuses a file another user owns or anyone but its owner can write.
func onlyOwnerWrites(path string, fi os.FileInfo) error {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s can be written by others (mode %v)", path, fi.Mode().Perm())
	}
	return nil
}

// openNonBlock: a pipe at the policy's name does not block the open.
const openNonBlock = syscall.O_NONBLOCK
