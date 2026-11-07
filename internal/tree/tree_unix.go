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
	"sync/atomic"
	"syscall"
	"unsafe"
)

// markerPrefix starts the environment variable that marks every process of one tree. Its name
// carries the tree's random id, so the markers of nested brokers live side by side.
const markerPrefix = "ADANEXT_EXEC_TREE_"

// maxSweeps bounds the rounds of a kill: each round kills what the last scan found that was not
// signalled yet, and a tree that keeps forking faster than it is killed is reported, not chased
// forever.
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

	// The memory ceiling's watch (StartLimited).
	limit     uint64
	enforced  bool
	exceeded  atomic.Bool
	peakSeen  atomic.Uint64
	memoryErr error
	stopWatch chan struct{}
	watchDone chan struct{}
	stopOnce  sync.Once
	// readRSS reads a process's resident memory, and readMark whether it carries the tree's
	// marker; tests replace them.
	readRSS  func(pid int) (uint64, error)
	readMark func(pid int) (bool, error)

	// cg is the command's own cgroup, where the kernel holds its ceiling (Linux, StartCeiling).
	cg *cgroup
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
	// Taken before the start, so a leader reaped at once (by a caller that ignores SIGCHLD)
	// still leaves the tree a bound.
	bound, boundErr := clockBound()
	if err := startCmd(t, cmd); err != nil {
		return nil, err
	}
	t.pgid = cmd.Process.Pid
	// The leader's own start time is the tighter bound; it is in the table until reaped.
	switch p, err := findProc(cmd.Process.Pid); {
	case err == nil:
		t.start = p.start
	case boundErr == nil:
		t.start = bound
	default:
		t.unknown = errors.Join(err, boundErr)
	}
	return t, nil
}

// The process table and the signal, as Kill uses them; tests replace them.
var (
	scanProcs = listProcs
	signal    = killProc
)

// errNoProc means the pid is not in the process table: the process is gone.
var errNoProc = errors.New("not in the process table")

// findProc reads one process from the table.
func findProc(pid int) (proc, error) {
	procs, err := scanProcs()
	if err != nil {
		return proc{}, fmt.Errorf("read the process table: %w", err)
	}
	for _, p := range procs {
		if p.pid == pid {
			return p, nil
		}
	}
	return proc{}, fmt.Errorf("process %d: %w", pid, errNoProc)
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
//
// A tree in a cgroup of its own (StartCeiling) has everything in that cgroup killed first.
func (t *Tree) Kill() error {
	if t.cg == nil {
		return t.killTree()
	}
	cgErr := t.cg.kill()
	return errors.Join(cgErr, t.killTree())
}

// killTree kills the group, the marked processes and their descendants, as Kill says.
func (t *Tree) killTree() error {
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
	// A process sent SIGKILL can still show in the next scan while it dies: it is not signalled
	// again, and a scan that finds only such processes ends the kill. Its descendants are
	// still followed.
	signalled := make(map[int]uint64)
	for round := 0; round < maxSweeps; round++ {
		procs, err := scanProcs()
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
		var fresh []proc
		for _, p := range found {
			if start, ok := signalled[p.pid]; !ok || start != p.start {
				fresh = append(fresh, p)
			}
		}
		if len(fresh) == 0 {
			return errors.Join(errs...)
		}
		var killErrs []error
		for _, p := range fresh {
			// Only a process signalled, or seen to be gone, counts as done.
			if err := signal(p); err != nil {
				killErrs = append(killErrs, err)
				continue
			}
			signalled[p.pid] = p.start
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
// marked, and their descendants; an exiting process is walked through but not returned, since a
// signal cannot reach it. A process that started before the leader is never one, and a
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
		if !p.exiting {
			found = append(found, p)
		}
		for _, c := range children[p.pid] {
			if c.start >= p.start {
				queue = append(queue, c)
			}
		}
	}
	return found
}

// RemoveCgroup removes the command's cgroup (StartCeiling, Linux) once the command has been
// reaped, and says why when it could not; a tree with none, or one already removed, is not an
// error.
func (t *Tree) RemoveCgroup() error {
	if t.cg == nil {
		return nil
	}
	return t.cg.remove()
}

// Close lets the thread that started the command go, and removes the command's cgroup if
// RemoveCgroup did not.
func (t *Tree) Close() {
	t.stopWatching()
	_ = t.RemoveCgroup()
	t.release()
}

// proc is one process from the table.
type proc struct {
	pid, ppid, pgid int
	start           uint64 // start time, in the platform's own unit
	zombie          bool
	exiting         bool // on its way out (macOS P_WEXIT): needs no signal, may still have children
}

// hasEnv reports whether the process's environment holds the entry exactly. A process whose
// environment cannot be read (another user's, or one that made itself undumpable) does not.
func hasEnv(pid int, entry string) bool {
	v, err := envHas(pid, entry)
	return err == nil && v
}

// envHas reports whether the process's environment holds the entry exactly, or why it could
// not be read.
func envHas(pid int, entry string) (bool, error) {
	env, err := readEnv(pid)
	if err != nil {
		return false, err
	}
	for _, e := range bytes.Split(env, []byte{0}) {
		if string(e) == entry {
			return true, nil
		}
	}
	return false, nil
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
