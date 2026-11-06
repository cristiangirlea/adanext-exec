//go:build linux

package tree

import (
	"os"
	"syscall"
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

func TestTheCheckBeforeASignalTellsGoneFromUnreadable(t *testing.T) {
	old := statOf
	t.Cleanup(func() { statOf = old })
	p := proc{pid: 900001, start: 10}
	for name, c := range map[string]struct {
		err      error
		reported bool
	}{
		"gone (no entry)":      {os.ErrNotExist, false},
		"gone during the read": {syscall.ESRCH, false},
		"unreadable":           {syscall.EIO, true},
		"denied":               {os.ErrPermission, true},
	} {
		t.Run(name, func(t *testing.T) {
			statOf = func(int) (proc, error) { return proc{}, c.err }
			same, err := sameStart(p)
			if same || (err != nil) != c.reported {
				t.Fatalf("same %v, err %v; want the error reported: %v", same, err, c.reported)
			}
			if err := killByStat(p); (err != nil) != c.reported {
				t.Fatalf("killByStat: %v; want the error reported: %v", err, c.reported)
			}
		})
	}
}
