//go:build darwin

package tree

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// startCmd starts the leader. macOS has no parent-death signal: the tree outlives a caller that
// is killed before it can kill the tree.
func startCmd(t *Tree, cmd *exec.Cmd) error { return cmd.Start() }

// The layout of struct kinfo_proc on 64-bit macOS (amd64 and arm64), from <sys/sysctl.h> and
// <sys/proc.h>.
// kinfoProcSize, kinfoFlag, kinfoStat, kinfoPGID, stateZombie and gone are in group_darwin.go.
const (
	kinfoStartSec  = 0   // kp_proc.p_starttime.tv_sec, int64
	kinfoStartUsec = 8   // kp_proc.p_starttime.tv_usec, int32
	kinfoPID       = 40  // kp_proc.p_pid
	kinfoPPID      = 560 // kp_eproc.e_ppid
)

// listProcs reads the process table from kern.proc.all.
func listProcs() ([]proc, error) {
	var raw string
	var err error
	for i := 0; i < 5; i++ {
		// The table can grow between sysctl's size query and its read.
		if raw, err = syscall.Sysctl("kern.proc.all"); !errors.Is(err, syscall.ENOMEM) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("kern.proc.all: %w", err)
	}
	b := []byte(raw)
	if r := len(b) % kinfoProcSize; r != 0 {
		b = append(b, make([]byte, kinfoProcSize-r)...) // Sysctl drops a final zero byte
	}
	procs := make([]proc, 0, len(b)/kinfoProcSize)
	le := binary.LittleEndian
	for off := 0; off+kinfoProcSize <= len(b); off += kinfoProcSize {
		k := b[off : off+kinfoProcSize]
		sec := le.Uint64(k[kinfoStartSec:])
		usec := uint64(le.Uint32(k[kinfoStartUsec:]))
		procs = append(procs, proc{
			pid:    int(int32(le.Uint32(k[kinfoPID:]))),
			ppid:   int(int32(le.Uint32(k[kinfoPPID:]))),
			pgid:   int(int32(le.Uint32(k[kinfoPGID:]))),
			start:  sec*1_000_000 + usec,
			zombie: gone(k),
		})
	}
	return procs, nil
}

var argMax = sync.OnceValues(func() (uint32, error) { return syscall.SysctlUint32("kern.argmax") })

// readEnv reads the process's arguments and environment (KERN_PROCARGS2): a count, the
// executable path, then NUL-separated strings. Only the caller's own processes can be read.
func readEnv(pid int) ([]byte, error) {
	size, err := argMax()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	n := uintptr(size)
	mib := [3]int32{1, 49, int32(pid)} // CTL_KERN, KERN_PROCARGS2
	if _, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 3, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0); errno != 0 {
		return nil, errno
	}
	return envFromProcArgs(buf[:n])
}

// lookUp reads one process for the check before a signal; tests replace it.
var lookUp = findProc

// killProc reads the table again and signals the pid only if it still names the process
// scanned (its start time). macOS has no pidfd: the pid could still be reused between that
// read and the signal.
func killProc(p proc) error {
	q, err := lookUp(p.pid)
	if errors.Is(err, errNoProc) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check process %d before the signal: %w", p.pid, err)
	}
	if q.start != p.start || q.zombie {
		return nil // gone, or the pid now names another process
	}
	if err := syscall.Kill(p.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process %d: %w", p.pid, err)
	}
	return nil
}
