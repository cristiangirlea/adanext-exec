//go:build linux || darwin

package tree

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// startLimited starts cmd under a ceiling and has it killed, reaped and closed at cleanup.
func startLimited(t *testing.T, mode string, limit uint64) (*Tree, func() error) {
	t.Helper()
	cmd := helperCmd(mode)
	tr, err := StartLimited(cmd, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The whole tree, then the watch, then the reap: as the broker does it.
		_ = tr.Kill()
		tr.stopWatching()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		tr.Close()
	})
	return tr, func() error { return tr.WaitExit() }
}

// A tree that grows past its ceiling is killed by the watch, and says so.
func TestTheWatchKillsATreeOverItsCeiling(t *testing.T) {
	tr, wait := startLimited(t, "grow", 96<<20)
	if !tr.MemoryEnforced() {
		t.Fatal("the ceiling is not enforced here")
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the tree grew on past its ceiling")
	}
	exceeded, peak, err := tr.FinalMemory()
	if !exceeded || err != nil || peak <= 96<<20 {
		t.Errorf("FinalMemory = %v, %d, %v; want over, with a peak past 96 MiB", exceeded, peak, err)
	}
}

// A process's memory that cannot be read stops the tree and keeps the error.
func TestAMemoryThatCannotBeReadStopsTheTree(t *testing.T) {
	tr, wait := startLimited(t, "sleep", 1<<30)
	broken := errors.New("no reading")
	// The watch has not read yet: its first read is a poll away.
	tr.mu.Lock()
	tr.readRSS = func(int) (uint64, error) { return 0, broken }
	tr.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the tree ran on without its memory read")
	}
	if exceeded, _, err := tr.FinalMemory(); exceeded || !errors.Is(err, broken) {
		t.Errorf("FinalMemory = %v, %v; want not over, with the reading's error", exceeded, err)
	}
}

// FinalMemory ends the watch itself before it answers.
func TestFinalMemoryEndsTheWatch(t *testing.T) {
	tr, _ := startLimited(t, "sleep", 1<<30)
	if _, _, err := tr.FinalMemory(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.watchDone:
	default:
		t.Error("FinalMemory returned with the watch still running")
	}
}

func TestResidentBytesReadsThisProcess(t *testing.T) {
	n, err := residentBytes(os.Getpid())
	if err != nil || n < 1<<20 {
		t.Errorf("residentBytes(self) = %d, %v; want a megabyte or more", n, err)
	}
	if _, err := residentBytes(1 << 30); !procEnded(err) {
		t.Errorf("residentBytes(no such process) = %v; want it read as ended", err)
	}
}

// waitStopped waits for the tree's command to end, or fails.
func waitStopped(t *testing.T, wait func() error, what string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: the tree ran on past its ceiling", what)
	}
}

// Two processes, each under the ceiling, are over it together: the ceiling is the tree's.
func TestTheCeilingIsTheWholeTrees(t *testing.T) {
	tr, wait := startLimited(t, "pair", 96<<20)
	waitStopped(t, wait, "pair")
	if exceeded, _, err := tr.FinalMemory(); !exceeded || err != nil {
		t.Errorf("FinalMemory = %v, %v; want over", exceeded, err)
	}
}

// A process that left the group and was orphaned is still counted, through its marker: the
// leader stays, and the escaped one grows past the ceiling.
func TestAnEscapedProcessCounts(t *testing.T) {
	tr, _ := startLimited(t, "escape", 96<<20)
	deadline := time.Now().Add(30 * time.Second)
	for !tr.exceeded.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the escaped grower was not counted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if exceeded, _, err := tr.FinalMemory(); !exceeded || err != nil {
		t.Errorf("FinalMemory = %v, %v; want over", exceeded, err)
	}
}

// A sample whose pid no longer names the process scanned is not the tree's: it counts nothing,
// however large it reads.
func TestASampleOfAReusedPidIsNotCounted(t *testing.T) {
	real := scanProcs
	calls := 0
	var mu sync.Mutex
	scanProcs = func() ([]proc, error) {
		procs, err := real()
		mu.Lock()
		calls++
		after := calls%2 == 0
		mu.Unlock()
		if after {
			// The second read of each poll: every pid now started later.
			for i := range procs {
				procs[i].start++
			}
		}
		return procs, err
	}
	defer func() { scanProcs = real }()
	tr, _ := startLimited(t, "sleep", 64<<20)
	tr.mu.Lock()
	tr.readRSS = func(int) (uint64, error) { return 1 << 40, nil }
	tr.mu.Unlock()
	time.Sleep(time.Second)
	if exceeded, _, err := tr.FinalMemory(); exceeded || err != nil {
		t.Errorf("FinalMemory = %v, %v; want not over: every sample was of a changed pid", exceeded, err)
	}
}

// Where memory cannot be read at all, the ceiling is not enforced, and nothing is watched.
func TestNoMemoryReadingMeansNotEnforced(t *testing.T) {
	real := probeRSS
	probeRSS = func(int) (uint64, error) { return 0, errors.New("no reading here") }
	defer func() { probeRSS = real }()
	tr, _ := startLimited(t, "sleep", 64<<20)
	if tr.MemoryEnforced() || tr.watchDone != nil {
		t.Errorf("enforced %v, watching %v; want neither", tr.MemoryEnforced(), tr.watchDone != nil)
	}
}

// FinalMemory waits for a read already under way, so nothing it finds is lost.
func TestFinalMemoryWaitsForARead(t *testing.T) {
	tr, _ := startLimited(t, "sleep", 1<<30)
	release := make(chan struct{})
	reading := make(chan struct{}, 1)
	tr.mu.Lock()
	tr.readRSS = func(int) (uint64, error) {
		select {
		case reading <- struct{}{}:
		default:
		}
		<-release
		return 0, nil
	}
	tr.mu.Unlock()
	<-reading
	done := make(chan struct{})
	go func() {
		tr.FinalMemory()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("FinalMemory returned during a read")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FinalMemory did not return after the read")
	}
}

// Close ends the watch too, for a run that never asks for its memory.
func TestCloseEndsTheWatch(t *testing.T) {
	cmd := helperCmd("sleep")
	tr, err := StartLimited(cmd, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	tr.Kill()
	tr.WaitExit()
	tr.Close()
	cmd.Wait()
	select {
	case <-tr.watchDone:
	default:
		t.Error("the watch still ran after Close")
	}
}

// An environment that cannot be read at first is read again later: the escaped grower is
// still found once its marker can be read.
func TestAMarkerReadThatFailsIsTriedAgain(t *testing.T) {
	cmd := helperCmd("escape")
	tr, err := StartLimited(cmd, 96<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tr.Kill()
		tr.stopWatching()
		_ = cmd.Wait()
		tr.Close()
	})
	var mu sync.Mutex
	failed := map[int]bool{}
	tr.mu.Lock()
	real := tr.readMark
	tr.readMark = func(pid int) (bool, error) {
		mu.Lock()
		first := !failed[pid]
		failed[pid] = true
		mu.Unlock()
		if first {
			return false, errors.New("not readable yet")
		}
		return real(pid)
	}
	tr.mu.Unlock()
	deadline := time.Now().Add(30 * time.Second)
	for !tr.exceeded.Load() {
		if time.Now().After(deadline) {
			t.Fatal("a marker that failed its first read was never read again")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A process that ended between the scan and its read counts nothing and is not an error.
func TestAnEndedProcessCountsNothing(t *testing.T) {
	for _, gone := range []error{syscall.ESRCH, os.ErrNotExist} {
		tr, _ := startLimited(t, "sleep", 64<<20)
		tr.mu.Lock()
		tr.readRSS = func(int) (uint64, error) { return 1 << 40, gone }
		tr.mu.Unlock()
		time.Sleep(400 * time.Millisecond)
		if exceeded, _, err := tr.FinalMemory(); exceeded || err != nil {
			t.Errorf("%v: FinalMemory = %v, %v; want nothing counted, no error", gone, exceeded, err)
		}
	}
}

// A process table that cannot be read, before the samples or after them, stops the tree and
// keeps the error.
func TestAScanThatFailsStopsTheTree(t *testing.T) {
	// Start reads the table once; then each poll reads it twice. which 0 fails the first read
	// of a poll (before the samples), which 1 the second (after them).
	for _, which := range []int{0, 1} {
		real := scanProcs
		var mu sync.Mutex
		calls := 0
		scanProcs = func() ([]proc, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n > 2 && n%2 == which {
				return nil, errors.New("no table")
			}
			return real()
		}
		cmd := helperCmd("sleep")
		tr, err := StartLimited(cmd, 1<<30)
		if err != nil {
			scanProcs = real
			t.Fatal(err)
		}
		// The watch itself must stop the command: nothing else here kills it.
		stopped := make(chan error, 1)
		go func() { stopped <- tr.WaitExit() }()
		var stoppedByWatch bool
		select {
		case <-stopped:
			stoppedByWatch = true
		case <-time.After(10 * time.Second):
		}
		_, _, ferr := tr.FinalMemory()
		scanProcs = real
		_ = tr.Kill()
		_ = cmd.Wait()
		tr.Close()
		what := map[int]string{0: "before the samples", 1: "after the samples"}[which]
		if ferr == nil || !stoppedByWatch {
			t.Errorf("a scan failing %s: error %v, stopped by the watch %v; want both", what, ferr, stoppedByWatch)
		}
	}
}
