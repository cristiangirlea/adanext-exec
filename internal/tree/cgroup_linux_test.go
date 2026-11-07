//go:build linux

package tree

import (
	"bufio"
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
	"syscall"
	"testing"
	"time"
)

// cgroupTestEnv names a delegated cgroup v2 cgroup the tests may make cgroups in: the user's to
// write, with the memory controller turned on for its children, and the test process not in it
// directly. CI makes one; without it the tests that need a real cgroup are skipped.
const cgroupTestEnv = "ADANEXT_EXEC_CGROUP_TEST"

// resetCgroupSetup forgets this process's cgroup setup, so the next StartCeiling sets up again.
func resetCgroupSetup() {
	cgSetup.mu.Lock()
	defer cgSetup.mu.Unlock()
	cgSetup.done, cgSetup.base, cgSetup.err = false, "", nil
}

// fakeCgroups points the setup at a directory standing in for /sys/fs/cgroup, with this process
// in its cgroup "app": the files a delegated cgroup holds, as regular files. The probe and the
// filesystem check answer as given. Everything is put back at cleanup.
func fakeCgroups(t *testing.T, v2 bool, probe error) (root, app string) {
	t.Helper()
	root = t.TempDir()
	app = filepath.Join(root, "app")
	self := filepath.Join(t.TempDir(), "cgroup")
	files := map[string]string{
		self:                                     "0::/app\n",
		filepath.Join(app, "cgroup.controllers"): "cpu io memory pids\n",
		filepath.Join(app, "cgroup.procs"):       strconv.Itoa(os.Getpid()) + "\n",
		filepath.Join(app, "cgroup.subtree_control"): "",
	}
	for path, data := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldRoot, oldSelf, oldIs, oldProbe := cgroupRoot, selfCgroupFile, isCgroup2, probeIntoCgroup
	cgroupRoot, selfCgroupFile = root, self
	isCgroup2 = func(string) (bool, error) { return v2, nil }
	probeIntoCgroup = func(string) error { return probe }
	resetCgroupSetup()
	t.Cleanup(func() {
		cgroupRoot, selfCgroupFile, isCgroup2, probeIntoCgroup = oldRoot, oldSelf, oldIs, oldProbe
		resetCgroupSetup()
	})
	return root, app
}

func TestOwnCgroupReadsTheCgroupV2Path(t *testing.T) {
	for _, tc := range []struct {
		data, want, err string
	}{
		{data: "0::/user.slice/app.scope\n", want: "/user.slice/app.scope"},
		{data: "0::/\n", want: "/"},
		{data: "12:memory:/x\n0::/y\n", err: "cgroup v1"},
		{data: "1:name=systemd:/x\n0::/y\n", err: "cgroup v1"},
		{data: "0::/../..\n", err: "no cgroup v2 path"},
		{data: "", err: "unreadable"},
		{data: "garbage\n", err: "unreadable"},
	} {
		path := filepath.Join(t.TempDir(), "cgroup")
		if err := os.WriteFile(path, []byte(tc.data), 0o644); err != nil {
			t.Fatal(err)
		}
		old := selfCgroupFile
		selfCgroupFile = path
		got, err := ownCgroup()
		selfCgroupFile = old
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("ownCgroup(%q) = %q, %v; want an error about %q", tc.data, got, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ownCgroup(%q) = %q, %v; want %q", tc.data, got, err, tc.want)
		}
	}
}

// The setup refuses every cgroup it cannot use, and leaves it as it found it.
func TestTheSetupRefusesACgroupItCannotUse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		v2    bool
		probe error
		edit  func(root, app string)
		want  string
	}{
		{name: "cgroup v1", v2: false, want: "not a cgroup v2"},
		{name: "the root cgroup", v2: true, edit: func(root, app string) { writeTest(selfCgroupFile, "0::/\n") }, want: "root cgroup"},
		{name: "no memory controller", v2: true, edit: func(root, app string) { writeTest(filepath.Join(app, "cgroup.controllers"), "cpu pids\n") }, want: "memory controller is not available"},
		{name: "another process", v2: true, edit: func(root, app string) {
			writeTest(filepath.Join(app, "cgroup.procs"), strconv.Itoa(os.Getpid())+"\n1\n")
		}, want: "other processes"},
		{name: "no clone3 into a cgroup", v2: true, probe: syscall.ENOSYS, want: "start a process into a cgroup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, app := fakeCgroups(t, tc.v2, tc.probe)
			if tc.edit != nil {
				tc.edit(root, app)
			}
			_, err := cgroupBase()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("cgroupBase() = %v; want an error about %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(app, brokerLeaf)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the leaf was made: %v", err)
			}
			if got := readTest(t, filepath.Join(app, "cgroup.subtree_control")); got != "" {
				t.Errorf("subtree_control = %q; want it untouched", got)
			}
		})
	}
}

// A move into the leaf that fails leaves no leaf; a controller that cannot be turned on moves
// the process back where it was.
func TestTheSetupPutsBackWhatItChanged(t *testing.T) {
	_, app := fakeCgroups(t, true, nil)
	// A directory the kernel would fill with files: here the leaf has no cgroup.procs.
	if _, err := cgroupBase(); err == nil || !strings.Contains(err.Error(), "move this process") {
		t.Fatalf("cgroupBase() = %v; want the move to fail", err)
	}
	if _, err := os.Stat(filepath.Join(app, brokerLeaf)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the leaf is still there: %v", err)
	}

	_, app = fakeCgroups(t, true, nil)
	writeTest(filepath.Join(app, brokerLeaf, "cgroup.procs"), "")
	if err := os.Remove(filepath.Join(app, "cgroup.subtree_control")); err != nil {
		t.Fatal(err)
	}
	writeTest(filepath.Join(app, "cgroup.procs"), "")
	if _, err := cgroupBase(); err == nil || !strings.Contains(err.Error(), "turn on the memory controller") {
		t.Fatalf("cgroupBase() = %v; want the controller to fail", err)
	}
	if got := readTest(t, filepath.Join(app, "cgroup.procs")); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("cgroup.procs = %q; want this process moved back", got)
	}

	// A move back that fails is said, with where the process stays.
	_, app = fakeCgroups(t, true, nil)
	writeTest(filepath.Join(app, brokerLeaf, "cgroup.procs"), "")
	if err := os.Remove(filepath.Join(app, "cgroup.subtree_control")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(app, "cgroup.procs"), 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file: the failed move back is not tried")
	}
	_, err := cgroupBase()
	if err == nil || !strings.Contains(err.Error(), "turn on the memory controller") || !strings.Contains(err.Error(), "stays in "+filepath.Join(app, brokerLeaf)) {
		t.Errorf("cgroupBase() = %v; want the controller's failure and where the process stays", err)
	}
}

// A cgroup that cannot be removed is said, not dropped.
func TestARemovalThatFailsIsSaid(t *testing.T) {
	old := cgroupRemoveWait
	cgroupRemoveWait = 50 * time.Millisecond
	t.Cleanup(func() { cgroupRemoveWait = old })
	// A directory holding a file stands in for a cgroup that still holds a process.
	dir := t.TempDir()
	writeTest(filepath.Join(dir, "cg", "busy"), "")
	g := &cgroup{dir: filepath.Join(dir, "cg"), fd: -1}
	if err := g.remove(); err == nil {
		t.Error("remove() = nil; want why the cgroup stays")
	}
	gone := &cgroup{dir: filepath.Join(dir, "gone"), fd: -1}
	if err := gone.remove(); err != nil {
		t.Errorf("remove() of a cgroup already gone = %v; want nil", err)
	}
}

// A process is the command's while it is in the command's cgroup or one under it.
func TestOutsideTellsWhereAProcessIs(t *testing.T) {
	files := t.TempDir()
	old := procCgroupFile
	procCgroupFile = func(pid int) string { return filepath.Join(files, strconv.Itoa(pid)) }
	t.Cleanup(func() { procCgroupFile = old })
	g := &cgroup{dir: filepath.Join(cgroupRoot, "app", cmdPrefix+"1"), fd: -1}
	for pid, tc := range map[int]struct{ data, want string }{
		1: {"0::/app/" + cmdPrefix + "1\n", ""},
		2: {"0::/app/" + cmdPrefix + "1/sub\n", ""},
		3: {"0::/app/" + brokerLeaf + "\n", "/app/" + brokerLeaf},
		4: {"0::/app/" + cmdPrefix + "10\n", "/app/" + cmdPrefix + "10"},
	} {
		writeTest(filepath.Join(files, strconv.Itoa(pid)), tc.data)
		if got, err := g.outside(pid); err != nil || got != tc.want {
			t.Errorf("outside(%q) = %q, %v; want %q", tc.data, got, err, tc.want)
		}
	}
	if _, err := g.outside(99); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("outside of a process gone = %v; want not exist", err)
	}
}

// A usable cgroup: the process moves into the leaf, the memory controller is turned on for the
// children, empty cgroups an earlier broker left are removed, and the setup is done once.
func TestTheSetupMovesIntoALeafAndTurnsOnMemory(t *testing.T) {
	_, app := fakeCgroups(t, true, nil)
	writeTest(filepath.Join(app, brokerLeaf, "cgroup.procs"), "")
	other := filepath.Join(app, cmdPrefix+"old")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	probes := 0
	probeIntoCgroup = func(string) error { probes++; return nil }
	for range 2 {
		base, err := cgroupBase()
		if err != nil || base != app {
			t.Fatalf("cgroupBase() = %q, %v; want %q", base, err, app)
		}
	}
	if probes != 1 {
		t.Errorf("set up %d times; want once", probes)
	}
	if got := readTest(t, filepath.Join(app, brokerLeaf, "cgroup.procs")); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("the leaf's cgroup.procs = %q; want this process", got)
	}
	if got := readTest(t, filepath.Join(app, "cgroup.subtree_control")); got != "+memory" {
		t.Errorf("subtree_control = %q; want +memory", got)
	}
	// A cgroup the setup did not make is never removed, whatever its name.
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a cgroup the setup did not make was removed: %v", err)
	}
}

// Where no cgroup can be made, CeilingCgroup starts nothing and auto watches the ceiling;
// CeilingWatch and no ceiling never look at cgroups.
func TestStartCeilingWithoutACgroup(t *testing.T) {
	fakeCgroups(t, false, nil)
	cmd := helperCmd("exit")
	if _, err := StartCeiling(cmd, 64<<20, CeilingCgroup); !errors.Is(err, ErrNoHardCeiling) || cmd.Process != nil {
		t.Fatalf("StartCeiling(cgroup) = %v, process %v; want ErrNoHardCeiling and nothing started", err, cmd.Process)
	}
	tr, _ := startCeiling(t, "exit", 64<<20, CeilingAuto)
	if !tr.MemoryEnforced() || tr.MemoryHard() {
		t.Errorf("auto: enforced %v, hard %v; want watched, not hard", tr.MemoryEnforced(), tr.MemoryHard())
	}

	looked := false
	isCgroup2 = func(string) (bool, error) { looked = true; return false, nil }
	resetCgroupSetup()
	tr, _ = startCeiling(t, "exit", 64<<20, CeilingWatch)
	if !tr.MemoryEnforced() || tr.MemoryHard() {
		t.Errorf("watch: enforced %v, hard %v; want watched, not hard", tr.MemoryEnforced(), tr.MemoryHard())
	}
	tr, _ = startCeiling(t, "exit", 0, CeilingCgroup)
	if tr.MemoryEnforced() || tr.MemoryHard() {
		t.Errorf("no ceiling: enforced %v, hard %v; want neither", tr.MemoryEnforced(), tr.MemoryHard())
	}
	if looked {
		t.Error("cgroups were looked at for a watched ceiling or none")
	}
}

func TestEventCount(t *testing.T) {
	events := []byte("low 0\nhigh 3\nmax 12\noom 2\noom_kill 1\noom_group_kill 0\n")
	for name, want := range map[string]uint64{"oom_kill": 1, "oom_group_kill": 0, "max": 12, "oom": 2, "missing": 0} {
		if got := eventCount(events, name); got != want {
			t.Errorf("eventCount(%q) = %d; want %d", name, got, want)
		}
	}
}

func writeTest(path, data string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		panic(err)
	}
}

func readTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The tests below need a real delegated cgroup (cgroupTestEnv). Each starts a helper straight
// into a fresh cgroup of its own, where the helper is the only process, as a service in a
// delegated cgroup would be, and the helper reports what StartCeiling did, one key=value a line.

// testCgroup makes a fresh cgroup under the delegated one, removed at cleanup.
func testCgroup(t *testing.T) string {
	t.Helper()
	base := os.Getenv(cgroupTestEnv)
	if base == "" {
		t.Skipf("%s names no delegated cgroup v2 cgroup: the hard ceiling is not tried here", cgroupTestEnv)
	}
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "t-"+hex.EncodeToString(id))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for removeTree(dir) != nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	})
	return dir
}

// startIn starts the helper mode straight into the cgroup at dir.
func startIn(t *testing.T, dir, mode string, stdout *bytes.Buffer) *exec.Cmd {
	t.Helper()
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	cmd := helperCmd(mode)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// runIn runs the helper mode in the cgroup at dir and returns what it reported.
func runIn(t *testing.T, dir, mode string) map[string]string {
	t.Helper()
	var out bytes.Buffer
	cmd := startIn(t, dir, mode, &out)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the helper failed: %v\n%s", err, out.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("the helper ran on\n%s", out.String())
	}
	got := map[string]string{}
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			got[k] = v
		}
	}
	return got
}

// The kernel holds the ceiling: a growing tree is stopped by the out-of-memory killer in its own
// cgroup, the result says so, and the cgroup is removed once the command is done.
func TestCgroupStopsATreeOverItsCeiling(t *testing.T) {
	dir := testCgroup(t)
	got := runIn(t, dir, "cgroup-over")
	for k, want := range map[string]string{"hard": "true", "incgroup": "true", "oomkill": "true", "exceeded": "true", "err": "<nil>", "killed": "true", "gone": "true", "leaf": "true"} {
		if got[k] != want {
			t.Errorf("%s = %q; want %q (all: %v)", k, got[k], want, got)
		}
	}
	if !hasWord([]byte(readTest(t, filepath.Join(dir, "cgroup.subtree_control"))), "memory") {
		t.Error("the memory controller is not on in the helper's cgroup")
	}
}

// A tree under its ceiling runs in its cgroup untouched, and its peak is the cgroup's.
func TestCgroupLetsATreeUnderItsCeilingRun(t *testing.T) {
	dir := testCgroup(t)
	got := runIn(t, dir, "cgroup-under")
	for k, want := range map[string]string{"hard": "true", "incgroup": "true", "oomkill": "false", "exceeded": "false", "err": "<nil>", "gone": "true"} {
		if got[k] != want {
			t.Errorf("%s = %q; want %q (all: %v)", k, got[k], want, got)
		}
	}
	if got["haspeak"] == "true" {
		if peak, _ := strconv.ParseUint(got["peak"], 10, 64); peak < 64<<20 {
			t.Errorf("peak = %d; want at least the 64 MiB the tree took", peak)
		}
	}
}

// A process that moves itself out of the command's cgroup (the same user may) is no longer held
// by the kernel: the watch finds it and stops the tree, with the error.
func TestATreeThatLeavesItsCgroupIsStopped(t *testing.T) {
	dir := testCgroup(t)
	got := runIn(t, dir, "cgroup-escape")
	for k, want := range map[string]string{"stopped": "true", "left": "true", "killed": "true", "gone": "true"} {
		if got[k] != want {
			t.Errorf("%s = %q; want %q (all: %v)", k, got[k], want, got)
		}
	}
}

// A cgroup shared with another process cannot be divided: CeilingCgroup starts nothing, auto
// watches, and the cgroup is left as it was.
func TestASharedCgroupIsLeftAlone(t *testing.T) {
	dir := testCgroup(t)
	startIn(t, dir, "sleep", &bytes.Buffer{})
	got := runIn(t, dir, "cgroup-shared")
	for k, want := range map[string]string{"cgroup": "refused", "hard": "false", "enforced": "true"} {
		if got[k] != want {
			t.Errorf("%s = %q; want %q (all: %v)", k, got[k], want, got)
		}
	}
	if s := readTest(t, filepath.Join(dir, "cgroup.subtree_control")); strings.TrimSpace(s) != "" {
		t.Errorf("subtree_control = %q; want it untouched", s)
	}
	if _, err := os.Stat(filepath.Join(dir, brokerLeaf)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a leaf was made: %v", err)
	}
}

// helperCgroup holds the programs the delegated-cgroup tests start.
func helperCgroup(mode string) {
	report := func(k string, v any) { fmt.Printf("%s=%v\n", k, v) }
	switch mode {
	case "cgroup-over", "cgroup-under":
		child, limit := "grow", uint64(96<<20)
		if mode == "cgroup-under" {
			child, limit = "half", 256<<20
		}
		cmd := helperCmd(child)
		tr, err := StartCeiling(cmd, limit, CeilingCgroup)
		if err != nil {
			report("start", err)
			os.Exit(3)
		}
		report("hard", tr.MemoryHard())
		dir := tr.cg.dir
		// The command started in its own cgroup, not moved there later.
		where, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", cmd.Process.Pid))
		report("incgroup", strings.TrimSpace(string(where)) == "0::"+strings.TrimPrefix(dir, cgroupRoot))
		exited := make(chan error, 1)
		go func() { exited <- tr.WaitExit() }()
		wait := 30 * time.Second
		if mode == "cgroup-under" {
			wait = 2 * time.Second
		}
		select {
		case <-exited:
		case <-time.After(wait):
		}
		_, peakErr := os.Stat(filepath.Join(dir, "memory.peak"))
		events, _ := os.ReadFile(filepath.Join(dir, "memory.events"))
		report("oomkill", eventCount(events, "oom_kill") > 0)
		_ = tr.Kill()
		exceeded, peak, err := tr.FinalMemory()
		_ = cmd.Wait()
		tr.Close()
		_, statErr := os.Stat(dir)
		own, _ := os.ReadFile("/proc/self/cgroup")
		report("exceeded", exceeded)
		report("peak", peak)
		report("haspeak", peakErr == nil)
		report("err", err)
		report("killed", strings.Contains(cmd.ProcessState.String(), "killed"))
		report("gone", errors.Is(statErr, fs.ErrNotExist))
		report("leaf", strings.HasSuffix(strings.TrimSpace(string(own)), "/"+brokerLeaf))
		os.Exit(0)
	case "cgroup-escape":
		cmd := helperCmd("leave")
		tr, err := StartCeiling(cmd, 256<<20, CeilingCgroup)
		if err != nil {
			report("start", err)
			os.Exit(3)
		}
		dir := tr.cg.dir
		exited := make(chan error, 1)
		go func() { exited <- tr.WaitExit() }()
		select {
		case <-exited:
			report("stopped", true)
		case <-time.After(15 * time.Second):
			report("stopped", false)
		}
		_ = tr.Kill()
		_, _, err = tr.FinalMemory()
		_ = cmd.Wait()
		rmErr := tr.RemoveCgroup()
		tr.Close()
		_, statErr := os.Stat(dir)
		report("left", err != nil && strings.Contains(err.Error(), "left the command's cgroup"))
		report("err", err)
		report("killed", strings.Contains(cmd.ProcessState.String(), "killed"))
		report("gone", rmErr == nil && errors.Is(statErr, fs.ErrNotExist))
		os.Exit(0)
	case "leave":
		// Moves itself out of its cgroup into the broker's leaf beside it, then stays.
		own, err := ownCgroup()
		if err != nil {
			os.Exit(2)
		}
		leaf := filepath.Join(cgroupRoot, filepath.Dir(own), brokerLeaf, "cgroup.procs")
		if writeCgroupFile(leaf, strconv.Itoa(os.Getpid())) != nil {
			os.Exit(2)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	case "cgroup-shared":
		cmd := helperCmd("exit")
		if _, err := StartCeiling(cmd, 64<<20, CeilingCgroup); errors.Is(err, ErrNoHardCeiling) && cmd.Process == nil {
			report("cgroup", "refused")
		} else {
			report("cgroup", err)
		}
		cmd = helperCmd("exit")
		tr, err := StartCeiling(cmd, 64<<20, CeilingAuto)
		if err != nil {
			report("start", err)
			os.Exit(3)
		}
		report("hard", tr.MemoryHard())
		report("enforced", tr.MemoryEnforced())
		_ = tr.WaitExit()
		_ = tr.Kill()
		_, _, _ = tr.FinalMemory()
		_ = cmd.Wait()
		tr.Close()
		os.Exit(0)
	}
}
