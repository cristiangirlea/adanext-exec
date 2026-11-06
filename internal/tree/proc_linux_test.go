//go:build linux

package tree

import (
	"testing"
	"time"
)

func TestKillByStatLeavesAProcessWhoseStartTimeChanged(t *testing.T) {
	// The path without pidfds (before Linux 5.3), which CI's kernels do not take on their own.
	cmd := helperCmd("sleep")
	startTree(t, cmd)
	p, err := findProc(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	stale := p
	stale.start++
	if err := killByStat(stale); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if q, err := findProc(cmd.Process.Pid); err != nil || q.zombie {
		t.Fatalf("a process with another start time was killed (%+v, %v)", q, err)
	}
	if err := killByStat(p); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the same process was not killed")
	}
}
