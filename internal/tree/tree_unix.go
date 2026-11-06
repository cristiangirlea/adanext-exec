//go:build linux || darwin

package tree

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// markerPrefix starts the environment variable that marks every process of one tree. Its name
// carries the tree's random id, so the markers of nested brokers live side by side.
const markerPrefix = "ADANEXT_EXEC_TREE_"

// maxSweeps bounds the rounds of a kill: each round kills what the last scan found, and a tree
// that keeps forking faster than it is killed is reported, not chased forever.
const maxSweeps = 50

// Tree is a started command in its own process group, with a marker in its environment that
// its descendants inherit. Kill reaches the group, every process that carries the marker (also
// one that left the group with setsid), and their descendants.
type Tree struct {
	pgid    int
	start   uint64 // the leader's start time; no process of the tree started before it
	marker  string // NAME=1
	release func() // lets the thread that started the command go (Linux)

	mu      sync.Mutex
	lost    error // set when the leader was reaped by someone else
	unknown error // set when the leader's start time could not be read
}

// Start starts cmd as the leader of a new process group, with the tree's marker added to its
// environment (cmd.Env, or the caller's environment when nil).
func Start(cmd *exec.Cmd) (*Tree, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("%w: make the tree's marker: %v", ErrNotContained, err)
	}
	t := &Tree{marker: markerPrefix + hex.EncodeToString(id) + "=1", release: func() {}}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env[:len(cmd.Env):len(cmd.Env)], t.marker)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	if err := startCmd(t, cmd); err != nil {
		return nil, err
	}
	t.pgid = cmd.Process.Pid
	// The leader is not reaped before WaitExit, so it is in the table even if it has exited.
	if p, err := findProc(cmd.Process.Pid); err != nil {
		t.unknown = err
	} else {
		t.start = p.start
	}
	return t, nil
}

// findProc reads one process from the table.
func findProc(pid int) (proc, error) {
	procs, err := listProcs()
	if err != nil {
		return proc{}, fmt.Errorf("read the process table: %w", err)
	}
	for _, p := range procs {
		if p.pid == pid {
			return p, nil
		}
	}
	return proc{}, fmt.Errorf("process %d is not in the process table", pid)
}

const pPID = 1 // idtype_t P_PID, the same on Linux and macOS

// WaitExit returns once the group's leader has exited, leaving it a zombie: until the caller
// reaps it, its pid, which is the group id, cannot be given to another process. This holds
// only while the caller alone reaps its children: when something else reaped the leader (a
// program that ignores SIGCHLD or waits for any child), WaitExit fails and Kill then no longer
// signals the group, whose id may have passed to another process; it still kills by marker.
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

// Kill kills the whole tree: the group, every process carrying the marker, and the
// descendants of both, scanning again until a scan finds none. A tree with no process left is
// not an error. Call it before the leader is reaped.
//
// Without the leader's start time, nothing marks where the tree's processes begin: then only
// the group is killed, and the error says so.
func (t *Tree) Kill() error {
	t.mu.Lock()
	lost := t.lost
	t.mu.Unlock()
	if t.pgid <= 1 {
		// kill(0) and kill(-1) would reach the caller's group or every process.
		return fmt.Errorf("no process group to kill (%d)", t.pgid)
	}
	byGroup := lost == nil
	if t.unknown != nil {
		if !byGroup {
			return fmt.Errorf("the leader was reaped elsewhere and its start time is unknown, so nothing could be killed safely: %v; %w", t.unknown, lost)
		}
		if err := t.killGroup(); err != nil {
			return err
		}
		return fmt.Errorf("only the process group was killed: the leader's start time is unknown, so what left the group cannot be told apart: %w", t.unknown)
	}
	var errs []error
	if !byGroup {
		errs = append(errs, fmt.Errorf("the group's leader was reaped elsewhere, so its id may belong to another process; only marked processes were killed: %w", lost))
	}
	marked := func(pid int) bool { return hasEnv(pid, t.marker) }
	for round := 0; round < maxSweeps; round++ {
		procs, err := listProcs()
		if err != nil {
			// The group can still be killed; what left it cannot be found.
			if byGroup {
				_ = t.killGroup()
			}
			return errors.Join(append(errs, fmt.Errorf("read the process table: %w", err))...)
		}
		found := t.members(procs, byGroup, marked)
		if byGroup {
			if err := t.killGroup(); err != nil {
				return errors.Join(append(errs, err)...)
			}
		}
		if len(found) == 0 {
			return errors.Join(errs...)
		}
		var killErrs []error
		for _, p := range found {
			if err := killProc(p); err != nil {
				killErrs = append(killErrs, err)
			}
		}
		if len(killErrs) > 0 {
			return errors.Join(append(errs, killErrs...)...)
		}
	}
	return errors.Join(append(errs, fmt.Errorf("the tree still had processes after %d rounds of killing", maxSweeps))...)
}

func (t *Tree) killGroup() error {
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

// members picks the live processes of the tree from a scan: the group's (when byGroup), those
// marked, and their descendants. A process that started before the leader is never one, and a
// parent link is followed only to a child no older than its parent, so a link read from a pid
// that was reused during the scan does not lead to an older process. This process is never one.
func (t *Tree) members(procs []proc, byGroup bool, marked func(int) bool) []proc {
	self := os.Getpid()
	children := make(map[int][]proc)
	var queue []proc
	for _, p := range procs {
		if p.zombie || p.pid <= 1 || p.pid == self || p.start < t.start {
			continue
		}
		children[p.ppid] = append(children[p.ppid], p)
		if (byGroup && p.pgid == t.pgid) || marked(p.pid) {
			queue = append(queue, p)
		}
	}
	seen := make(map[int]bool)
	var found []proc
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p.pid] {
			continue
		}
		seen[p.pid] = true
		found = append(found, p)
		for _, c := range children[p.pid] {
			if c.start >= p.start {
				queue = append(queue, c)
			}
		}
	}
	return found
}

// Close lets the thread that started the command go.
func (t *Tree) Close() { t.release() }

// proc is one process from the table.
type proc struct {
	pid, ppid, pgid int
	start           uint64 // start time, in the platform's own unit
	zombie          bool
}

// hasEnv reports whether the process's environment holds the entry exactly. A process whose
// environment cannot be read (another user's, or one that made itself undumpable) does not.
func hasEnv(pid int, entry string) bool {
	env, err := readEnv(pid)
	if err != nil {
		return false
	}
	for _, e := range bytes.Split(env, []byte{0}) {
		if string(e) == entry {
			return true
		}
	}
	return false
}

// envFromProcArgs takes the environment out of a KERN_PROCARGS2 buffer: argc (int32), the
// executable path, NUL padding, argc arguments, then the environment's NUL-terminated strings.
// Only the environment is returned, so an argument that looks like an entry never counts.
func envFromProcArgs(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, errors.New("arguments too short")
	}
	argc := int(int32(binary.LittleEndian.Uint32(b)))
	if argc < 0 {
		return nil, errors.New("a negative argument count")
	}
	b = b[4:]
	i := bytes.IndexByte(b, 0) // the executable path
	if i < 0 {
		return nil, errors.New("no executable path")
	}
	b = b[i:]
	for len(b) > 0 && b[0] == 0 { // its padding
		b = b[1:]
	}
	for n := 0; n < argc; n++ {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			return nil, errors.New("fewer arguments than the count says")
		}
		b = b[i+1:]
	}
	return b, nil
}
