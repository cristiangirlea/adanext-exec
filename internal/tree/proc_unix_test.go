//go:build linux || darwin

package tree

import (
	"encoding/binary"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTheProcessTableShowsThisProcessAndItsLinks(t *testing.T) {
	p, err := findProc(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if p.ppid != os.Getppid() || p.pgid != syscall.Getpgrp() || p.start == 0 || p.zombie {
		t.Fatalf("this process read as %+v, want ppid %d and pgid %d", p, os.Getppid(), syscall.Getpgrp())
	}
}

func TestAStartedCommandCarriesTheMarkerAndNothingElseDoes(t *testing.T) {
	cmd := helperCmd("sleep")
	tr := startTree(t, cmd)
	if tr.unknown != nil {
		t.Fatal(tr.unknown)
	}
	if !strings.HasPrefix(tr.marker, markerPrefix) || !strings.HasSuffix(tr.marker, "=1") {
		t.Fatalf("marker %q", tr.marker)
	}
	if !hasEnv(cmd.Process.Pid, tr.marker) {
		t.Fatal("the command's environment does not hold the marker")
	}
	if hasEnv(os.Getpid(), tr.marker) {
		t.Fatal("the caller holds its own command's marker")
	}
	if hasEnv(cmd.Process.Pid, tr.marker+"0") || hasEnv(cmd.Process.Pid, strings.TrimSuffix(tr.marker, "=1")) {
		t.Fatal("a near match counted as the marker")
	}
	p, err := findProc(cmd.Process.Pid)
	if err != nil || p.ppid != os.Getpid() || p.pgid != cmd.Process.Pid || p.start != tr.start {
		t.Fatalf("the command read as %+v (%v), want start %d", p, err, tr.start)
	}
}

func TestAMarkerPassedAsAnArgumentDoesNotCount(t *testing.T) {
	marked := startTree(t, helperCmd("sleep"))
	cmd := helperCmd("sleep")
	cmd.Args = append(cmd.Args, marked.marker)
	startTree(t, cmd)
	if hasEnv(cmd.Process.Pid, marked.marker) {
		t.Fatal("a marker in the arguments counted as one in the environment")
	}
}

func TestTwoTreesHaveDifferentMarkers(t *testing.T) {
	a, b := startTree(t, helperCmd("sleep")), startTree(t, helperCmd("sleep"))
	if a.marker == b.marker {
		t.Fatal("two trees share a marker")
	}
}

func TestKillLeavesAProcessWhoseStartTimeChanged(t *testing.T) {
	cmd := helperCmd("sleep")
	startTree(t, cmd)
	p, err := findProc(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	stale := p
	stale.start++
	if err := killProc(stale); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if q, err := findProc(cmd.Process.Pid); err != nil || q.zombie {
		t.Fatalf("a process with another start time was killed (%+v, %v)", q, err)
	}
	if err := killProc(p); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the same process was not killed")
	}
}

func TestMembersNeverReachAProcessOlderThanTheTree(t *testing.T) {
	tr := &Tree{pgid: 100, start: 50}
	self := os.Getpid()
	procs := []proc{
		{pid: 100, ppid: self, pgid: 100, start: 50}, // the leader
		{pid: 101, ppid: 100, pgid: 100, start: 60},  // in the group
		{pid: 102, ppid: 101, pgid: 102, start: 70},  // left the group: a child of a member
		{pid: 103, ppid: 1, pgid: 103, start: 80},    // left and orphaned, carries the marker
		{pid: 104, ppid: 103, pgid: 103, start: 90},  // its child
		{pid: 105, ppid: 103, pgid: 105, start: 40},  // reads as its child, but is older than the tree
		{pid: 106, ppid: 104, pgid: 106, start: 85},  // reads as 104's child, but is older than 104
		{pid: 107, ppid: 1, pgid: 107, start: 95},    // unrelated
		{pid: 108, ppid: 101, pgid: 100, start: 65, zombie: true},
		{pid: self, ppid: 1, pgid: 100, start: 99}, // the caller, whatever it reads as
	}
	marked := func(pid int) bool { return pid == 103 || pid == 105 }
	got := pids(tr.members(procs, true, marked))
	if want := "100 101 102 103 104"; got != want {
		t.Fatalf("members %s, want %s", got, want)
	}
	if got := pids(tr.members(procs, false, marked)); got != "103 104" {
		t.Fatalf("without the group: %s, want 103 104", got)
	}
}

func pids(ps []proc) string {
	var s []int
	for _, p := range ps {
		s = append(s, p.pid)
	}
	sort.Ints(s)
	var b strings.Builder
	for i, n := range s {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

func procArgs(argc int32, parts ...string) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(argc))
	for _, p := range parts {
		b = append(append(b, p...), 0)
	}
	return b
}

func TestEnvFromProcArgsReadsOnlyTheEnvironment(t *testing.T) {
	const m = "ADANEXT_EXEC_TREE_ab=1"
	cases := []struct {
		name string
		buf  []byte
		env  string // the entries, joined by spaces; "!" for an error
	}{
		{"marker in the environment", procArgs(2, "/bin/x", "", "", "x", "arg", m, "HOME=/h"), m + " HOME=/h"},
		{"marker only as an argument", procArgs(2, "/bin/x", "", "x", m, "HOME=/h"), "HOME=/h"},
		{"empty arguments", procArgs(3, "/bin/x", "x", "", "", m), m},
		{"no environment", procArgs(1, "/bin/x", "x"), ""},
		{"fewer arguments than counted", procArgs(5, "/bin/x", "x", "y"), "!"},
		{"negative count", procArgs(-1, "/bin/x"), "!"},
		{"no path", []byte{1, 0, 0, 0, 'x'}, "!"},
		{"too short", []byte{1, 0}, "!"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, err := envFromProcArgs(c.buf)
			if c.env == "!" {
				if err == nil {
					t.Fatalf("env %q, want an error", env)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var entries []string
			for _, e := range strings.Split(string(env), "\x00") {
				if e != "" {
					entries = append(entries, e)
				}
			}
			if got := strings.Join(entries, " "); got != c.env {
				t.Fatalf("env %q, want %q", got, c.env)
			}
		})
	}
}
