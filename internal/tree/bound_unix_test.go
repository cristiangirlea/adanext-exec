//go:build linux || darwin

package tree

import "testing"

func TestTheClockBoundIsNoLaterThanAProcessStartedAfterIt(t *testing.T) {
	bound, err := clockBound()
	if err != nil {
		t.Fatal(err)
	}
	cmd := helperCmd("sleep")
	startTree(t, cmd)
	p, err := findProc(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if bound == 0 || p.start < bound {
		t.Fatalf("bound %d, a process started after it shows %d", bound, p.start)
	}
}

func TestALeaderMissingFromTheTableStillLeavesTheTreeABound(t *testing.T) {
	// As when a caller that ignores SIGCHLD has the leader reaped before Start reads it.
	old := scanProcs
	t.Cleanup(func() { scanProcs = old })
	scanProcs = func() ([]proc, error) { return nil, nil }
	cmd := helperCmd("sleep")
	tr := startTree(t, cmd)
	scanProcs = old
	if tr.unknown != nil || tr.start == 0 {
		t.Fatalf("start %d, unknown %v; want the clock's bound", tr.start, tr.unknown)
	}
	p, err := findProc(cmd.Process.Pid)
	if err != nil || p.start < tr.start {
		t.Fatalf("the leader %+v (%v) started before the bound %d", p, err, tr.start)
	}
	if err := tr.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the leader was not killed")
	}
}
