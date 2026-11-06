//go:build windows

package tree

import (
	"errors"
	"os/exec"
	"testing"
	"time"
)

// A ceiling the job's SIZE_T cannot hold is refused, never cut down to one it can.
func TestALimitTheJobCannotHoldIsRefused(t *testing.T) {
	const max32 = 1<<32 - 1
	if limitFits(5<<30, max32) || !limitFits(1<<30, max32) || !limitFits(max32, max32) {
		t.Error("limitFits judged a 32-bit ceiling wrongly")
	}
}

// Close stops the watch and waits for it, so it never reads a handle Close let go.
func TestCloseEndsTheMemoryWatch(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
	tr, err := StartLimited(cmd, 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	tr.Kill()
	cmd.Wait()
	tr.Close()
	select {
	case <-tr.watchDone:
	case <-time.After(time.Second):
		t.Fatal("the watch still ran after Close")
	}
	if exceeded, peak, err := tr.FinalMemory(); exceeded || peak != 0 || err == nil {
		t.Errorf("after Close: exceeded %v, peak %d, err %v; want nothing read, and the reason", exceeded, peak, err)
	}
}

// Reaching the limit exactly is a commit the job allowed; only passing it shows a refusal.
func TestOverMeansPastTheLimit(t *testing.T) {
	if over(100, 100) || !over(101, 100) || over(99, 100) {
		t.Error("over judged the limit wrongly")
	}
}

// A peak that cannot be read stops the tree and keeps the error: it is never read as "not
// over".
func TestAPeakThatCannotBeReadStopsTheTree(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
	tr, err := StartLimited(cmd, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	defer cmd.Wait()
	broken := errors.New("no reading")
	tr.limit = 512 << 20
	tr.readPeak = func() (uint64, error) { return 0, broken }
	tr.watchMemory()
	select {
	case <-tr.watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch went on without a reading")
	}
	if !errors.Is(tr.MemoryErr(), broken) {
		t.Errorf("MemoryErr = %v, want the reading's error", tr.MemoryErr())
	}
	if n, _ := tr.Active(); n != 0 {
		t.Errorf("%d processes still run; want the tree stopped", n)
	}
	if exceeded, _, err := tr.FinalMemory(); exceeded || !errors.Is(err, broken) {
		t.Errorf("FinalMemory = %v, %v; want not over, with the reading's error", exceeded, err)
	}
}

// The final reading's own error is kept too: a peak that cannot be read at the end is reported,
// never a silent zero, and the watch has ended before it is taken.
func TestTheFinalReadingKeepsItsError(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	tr, err := StartLimited(cmd, 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cmd.Wait()
	broken := errors.New("no reading")
	tr.stopWatching()
	tr.readPeak = func() (uint64, error) { return 0, broken }
	if exceeded, peak, err := tr.FinalMemory(); exceeded || peak != 0 || !errors.Is(err, broken) {
		t.Errorf("FinalMemory = %v, %d, %v; want the reading's error", exceeded, peak, err)
	}
	select {
	case <-tr.watchDone:
	default:
		t.Error("FinalMemory returned with the watch still running")
	}
}

// FinalMemory itself ends the watch before it reads, so nothing the watch finds is lost.
func TestFinalMemoryEndsTheWatchItself(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	tr, err := StartLimited(cmd, 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cmd.Wait()
	if _, _, err := tr.FinalMemory(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.watchDone:
	default:
		t.Error("FinalMemory returned with the watch still running")
	}
}
