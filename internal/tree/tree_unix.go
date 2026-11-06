//go:build linux || darwin

package tree

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// Tree is a started command in its own process group. A descendant that calls setsid or
// setpgid leaves the group.
type Tree struct {
	pgid int
	mu   sync.Mutex
	lost error // set when the leader was reaped by someone else
}

// Start starts cmd as the leader of a new process group.
func Start(cmd *exec.Cmd) (*Tree, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Tree{pgid: cmd.Process.Pid}, nil
}

const pPID = 1 // idtype_t P_PID, the same on Linux and macOS

// WaitExit returns once the group's leader has exited, leaving it a zombie: until the caller
// reaps it, its pid, which is the group id, cannot be given to another process. This holds
// only while the caller alone reaps its children: when something else reaped the leader (a
// program that ignores SIGCHLD or waits for any child), WaitExit fails and Kill then refuses
// to signal a group id that may no longer be the command's.
func (t *Tree) WaitExit() error {
	var info [256]byte // larger than siginfo_t on Linux (128) and macOS (104)
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(t.pgid), uintptr(unsafe.Pointer(&info)), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		switch errno {
		case 0:
			return nil
		case syscall.EINTR:
			continue
		default:
			err := fmt.Errorf("wait for process %d: %w", t.pgid, errno)
			t.mu.Lock()
			t.lost = err
			t.mu.Unlock()
			return err
		}
	}
}

// Kill sends SIGKILL to the whole group. A group with no process left is not an error. Call it
// before the leader is reaped.
func (t *Tree) Kill() error {
	t.mu.Lock()
	lost := t.lost
	t.mu.Unlock()
	if lost != nil {
		return fmt.Errorf("the group's leader was reaped elsewhere, so its id may belong to another process: %w", lost)
	}
	if t.pgid <= 1 {
		// kill(0) and kill(-1) would reach the caller's group or every process.
		return fmt.Errorf("no process group to kill (%d)", t.pgid)
	}
	err := syscall.Kill(-t.pgid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	// macOS answers EPERM for a group whose only members are zombies.
	if errors.Is(err, syscall.EPERM) && !groupHasLiveMembers(t.pgid) {
		return nil
	}
	return fmt.Errorf("kill process group %d: %w", t.pgid, err)
}

// Close has nothing to release on Unix.
func (t *Tree) Close() {}
