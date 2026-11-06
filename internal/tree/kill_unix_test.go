//go:build linux || darwin

package tree

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// noGroup is a process group id that no process has, so the group signal finds nothing.
const noGroup = 0x7ffffff0

// fakeTable makes Kill read the given scans, one per round (the last repeats), and record the
// processes it signals, failing those in fail.
func fakeTable(t *testing.T, scans [][]proc, fail map[int]bool) *[]int {
	t.Helper()
	oldScan, oldSignal := scanProcs, signal
	t.Cleanup(func() { scanProcs, signal = oldScan, oldSignal })
	round := 0
	scanProcs = func() ([]proc, error) {
		s := scans[min(round, len(scans)-1)]
		round++
		return s, nil
	}
	var signalled []int
	signal = func(p proc) error {
		signalled = append(signalled, p.pid)
		if fail[p.pid] {
			return errors.New("cannot check it")
		}
		return nil
	}
	return &signalled
}

func TestKillSignalsADyingProcessOnceAndStillReachesItsNewChild(t *testing.T) {
	a := proc{pid: 900001, ppid: 1, pgid: noGroup, start: 10}
	b := proc{pid: 900002, ppid: a.pid, pgid: 900002, start: 11} // left the group after the first scan
	got := fakeTable(t, [][]proc{{a}, {a, b}, {a, b}}, nil)
	tr := &Tree{pgid: noGroup, marker: markerPrefix + "00=1"}
	if err := tr.Kill(); err != nil {
		t.Fatal(err)
	}
	if s := pidList(*got); s != "900001 900002" {
		t.Fatalf("signalled %s, want each once: 900001 900002", s)
	}
}

func TestKillReportsAProcessItCouldNotCheck(t *testing.T) {
	a := proc{pid: 900001, ppid: 1, pgid: noGroup, start: 10}
	fakeTable(t, [][]proc{{a}}, map[int]bool{a.pid: true})
	tr := &Tree{pgid: noGroup, marker: markerPrefix + "00=1"}
	if err := tr.Kill(); err == nil || !strings.Contains(err.Error(), "cannot check it") {
		t.Fatalf("err %v, want the failed check reported", err)
	}
}

func TestKillWithoutTheLeadersStartTimeKillsOnlyTheGroupAndSaysSo(t *testing.T) {
	cmd := helperCmd("sleep")
	tr := startTree(t, cmd)
	tr.unknown = errors.New("the table could not be read")
	err := tr.Kill()
	if err == nil || !strings.Contains(err.Error(), "only the process group was killed") {
		t.Fatalf("err %v, want it to say only the group was killed", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the group was not killed")
	}
}

func TestKillWithoutTheLeaderOrItsStartTimeKillsNothing(t *testing.T) {
	cmd := helperCmd("sleep")
	tr := startTree(t, cmd)
	tr.unknown = errors.New("the table could not be read")
	tr.lost = errors.New("reaped elsewhere")
	if err := tr.Kill(); err == nil || !strings.Contains(err.Error(), "nothing could be killed safely") {
		t.Fatalf("err %v, want a refusal to kill", err)
	}
	if p, err := findProc(cmd.Process.Pid); err != nil || p.zombie {
		t.Fatalf("the command was killed (%+v, %v)", p, err)
	}
}

func pidList(pids []int) string {
	var b strings.Builder
	for i, p := range pids {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Itoa(p))
	}
	return b.String()
}
