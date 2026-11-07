//go:build linux

package tree

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The places and probes the hard ceiling uses; tests replace them.
var (
	cgroupRoot      = "/sys/fs/cgroup"
	selfCgroupFile  = "/proc/self/cgroup"
	isCgroup2       = statfsIsCgroup2
	probeIntoCgroup = forkIntoCgroup
)

// cgroup2Magic is CGROUP2_SUPER_MAGIC, the filesystem type of a cgroup v2 hierarchy.
const cgroup2Magic = 0x63677270

// The names the broker gives the cgroups it makes.
const (
	brokerLeaf = "adanext-exec-broker"
	cmdPrefix  = "adanext-exec-cmd-"
)

// cgSetup is the one-time setup of this process's cgroup: where the commands' cgroups are made,
// or why none can be.
var cgSetup struct {
	mu   sync.Mutex
	done bool
	base string
	err  error
}

// cgroupBase returns the cgroup the commands' cgroups are made in, setting it up the first time
// it is asked. A setup that failed is not tried again: nothing it found changes while this
// process runs, except by its own hand.
func cgroupBase() (string, error) {
	cgSetup.mu.Lock()
	defer cgSetup.mu.Unlock()
	if !cgSetup.done {
		cgSetup.base, cgSetup.err = setUpCgroup()
		cgSetup.done = true
	}
	return cgSetup.base, cgSetup.err
}

// setUpCgroup makes this process's cgroup ready to hold the commands' cgroups. It must be a
// cgroup v2 cgroup, not the root, with the memory controller available, holding this process
// alone, and one this process may start a child into. Then this process moves into a leaf of its
// own (adanext-exec-broker), since a cgroup with processes in it cannot hand controllers to its
// children, and turns on the memory controller for the children. Whatever fails before that is
// left as it was found.
func setUpCgroup() (string, error) {
	ok, err := isCgroup2(cgroupRoot)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", cgroupRoot, err)
	}
	if !ok {
		return "", fmt.Errorf("%s is not a cgroup v2 hierarchy", cgroupRoot)
	}
	path, err := ownCgroup()
	if err != nil {
		return "", err
	}
	if path == "/" {
		return "", errors.New("this process is in the root cgroup")
	}
	dir := filepath.Join(cgroupRoot, path)
	ctl, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		return "", fmt.Errorf("read the cgroup's controllers: %w", err)
	}
	if !hasWord(ctl, "memory") {
		return "", fmt.Errorf("the memory controller is not available to %s", dir)
	}
	self := os.Getpid()
	pids, err := readPids(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return "", fmt.Errorf("read the cgroup's processes: %w", err)
	}
	for _, pid := range pids {
		if pid != self {
			return "", fmt.Errorf("%s holds other processes than this one", dir)
		}
	}
	if err := probeIntoCgroup(dir); err != nil {
		return "", fmt.Errorf("start a process into a cgroup: %w", err)
	}
	leaf := filepath.Join(dir, brokerLeaf)
	if err := os.Mkdir(leaf, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("make %s: %w", leaf, err)
	}
	me := strconv.Itoa(self)
	if err := writeCgroupFile(filepath.Join(leaf, "cgroup.procs"), me); err != nil {
		_ = os.Remove(leaf)
		return "", fmt.Errorf("move this process into %s: %w", leaf, err)
	}
	if err := writeCgroupFile(filepath.Join(dir, "cgroup.subtree_control"), "+memory"); err != nil {
		// Back to where it was, and the leaf gone.
		_ = writeCgroupFile(filepath.Join(dir, "cgroup.procs"), me)
		_ = os.Remove(leaf)
		return "", fmt.Errorf("turn on the memory controller in %s: %w", dir, err)
	}
	removeStale(dir)
	return dir, nil
}

// ownCgroup reads this process's cgroup v2 path from /proc/self/cgroup: the one line "0::PATH".
// A line of a cgroup v1 hierarchy means a hybrid setup, where memory may be v1's.
func ownCgroup() (string, error) {
	data, err := os.ReadFile(selfCgroupFile)
	if err != nil {
		return "", fmt.Errorf("read this process's cgroup: %w", err)
	}
	path := ""
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.SplitN(line, ":", 3)
		if len(f) != 3 {
			return "", fmt.Errorf("an unreadable line in %s: %q", selfCgroupFile, line)
		}
		if f[0] != "0" || f[1] != "" {
			return "", errors.New("a cgroup v1 hierarchy is mounted (a hybrid setup)")
		}
		path = f[2]
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") {
		return "", fmt.Errorf("no cgroup v2 path in %s", selfCgroupFile)
	}
	return path, nil
}

// statfsIsCgroup2 reports whether path is the root of a cgroup v2 hierarchy.
func statfsIsCgroup2(path string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, err
	}
	return st.Type == cgroup2Magic, nil
}

// forkIntoCgroup proves that a child can be started straight into the cgroup at dir (clone3 with
// CLONE_INTO_CGROUP: Linux 5.7, and a seccomp filter that allows it). The child is started for a
// program that cannot exist, so it fails its exec and is reaped at once: only that exec error
// shows the start itself worked.
func forkIntoCgroup(dir string) error {
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	_, err = syscall.ForkExec(filepath.Join(dir, "adanext-exec-none"), []string{"adanext-exec-none"}, &syscall.ProcAttr{
		Sys: &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd},
	})
	switch {
	case errors.Is(err, syscall.ENOENT):
		return nil
	case err == nil:
		return errors.New("a program that cannot exist was started")
	}
	return err
}

// removeStale removes the empty cgroups an earlier broker left in dir. A cgroup that still holds
// a process cannot be removed, so nothing in use goes.
func removeStale(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), cmdPrefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// cgroup is one command's cgroup.
type cgroup struct {
	dir string
	fd  int // open until the command has started in it
}

// newCgroup makes a cgroup for one command, with its memory ceiling set: memory.max, no swap
// (memory.swap.max, where swap is accounted) and the whole cgroup stopped together when the
// kernel's out-of-memory killer acts (memory.oom.group).
func newCgroup(limit uint64) (*cgroup, error) {
	base, err := cgroupBase()
	if err != nil {
		return nil, err
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	g := &cgroup{dir: filepath.Join(base, cmdPrefix+hex.EncodeToString(id)), fd: -1}
	if err := os.Mkdir(g.dir, 0o755); err != nil {
		return nil, fmt.Errorf("make the command's cgroup: %w", err)
	}
	fail := func(err error) (*cgroup, error) {
		g.remove()
		return nil, err
	}
	if err := writeCgroupFile(filepath.Join(g.dir, "memory.max"), strconv.FormatUint(limit, 10)); err != nil {
		return fail(fmt.Errorf("set memory.max: %w", err))
	}
	for _, f := range []string{"memory.swap.max:0", "memory.oom.group:1"} {
		name, value, _ := strings.Cut(f, ":")
		if err := writeCgroupFile(filepath.Join(g.dir, name), value); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fail(fmt.Errorf("set %s: %w", name, err))
		}
	}
	if g.fd, err = syscall.Open(g.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0); err != nil {
		g.fd = -1
		return fail(fmt.Errorf("open the command's cgroup: %w", err))
	}
	return g, nil
}

// closeFD closes the cgroup's directory once the command has started in it.
func (g *cgroup) closeFD() {
	if g.fd >= 0 {
		_ = syscall.Close(g.fd)
		g.fd = -1
	}
}

// kill kills every process in the cgroup (cgroup.kill, Linux 5.14). An older kernel has no such
// file, and the tree's own kill does the work.
func (g *cgroup) kill() error {
	err := writeCgroupFile(filepath.Join(g.dir, "cgroup.kill"), "1")
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("kill the command's cgroup: %w", err)
}

// final reads whether the kernel stopped the cgroup at its ceiling (memory.events: oom_kill), and
// its peak where the kernel keeps one (memory.peak, Linux 5.19; 0 otherwise).
func (g *cgroup) final() (exceeded bool, peak uint64, err error) {
	events, err := os.ReadFile(filepath.Join(g.dir, "memory.events"))
	if err != nil {
		return false, 0, fmt.Errorf("read the cgroup's memory events: %w", err)
	}
	exceeded = eventCount(events, "oom_kill") > 0 || eventCount(events, "oom_group_kill") > 0
	if data, err := os.ReadFile(filepath.Join(g.dir, "memory.peak")); err == nil {
		peak, _ = strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	}
	return exceeded, peak, nil
}

// remove removes the cgroup and any its command made under it, once their processes are gone;
// it waits up to two seconds for them to finish dying.
func (g *cgroup) remove() {
	g.closeFD()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if removeTree(g.dir) == nil || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// removeTree removes a cgroup's children, deepest first, then the cgroup.
func removeTree(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = removeTree(filepath.Join(dir, e.Name()))
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// eventCount reads one counter from a cgroup's memory.events.
func eventCount(events []byte, name string) uint64 {
	for _, line := range bytes.Split(events, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) == 2 && f[0] == name {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			return n
		}
	}
	return 0
}

// writeCgroupFile writes one value to an existing cgroup file, as the kernel wants it: one write.
func writeCgroupFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// readPids reads the pids in a cgroup.procs file.
func readPids(path string) ([]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, f := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("a pid that is not a number: %q", f)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// hasWord reports whether the space-separated list holds word.
func hasWord(list []byte, word string) bool {
	for _, f := range strings.Fields(string(list)) {
		if f == word {
			return true
		}
	}
	return false
}

// StartCeiling is StartLimited with a choice of how the ceiling is held. With a ceiling and
// CeilingAuto or CeilingCgroup, the command is started straight into a cgroup of its own with
// memory.max set, so the kernel holds the ceiling; the watch runs as well, for a process that
// moves itself out of the cgroup. Where no such cgroup can be made, CeilingAuto watches the
// ceiling only, and CeilingCgroup does not start the command (ErrNoHardCeiling).
func StartCeiling(cmd *exec.Cmd, memoryLimit uint64, mode Ceiling) (*Tree, error) {
	if memoryLimit == 0 || mode == CeilingWatch {
		return StartLimited(cmd, memoryLimit)
	}
	g, err := newCgroup(memoryLimit)
	if err != nil {
		if mode == CeilingCgroup {
			return nil, fmt.Errorf("%w: %v", ErrNoHardCeiling, err)
		}
		return StartLimited(cmd, memoryLimit)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = g.fd
	t, err := StartLimited(cmd, memoryLimit)
	g.closeFD()
	if err != nil {
		g.remove()
		return nil, err
	}
	t.cg = g
	return t, nil
}
