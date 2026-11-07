//go:build windows

package tree

import (
	"errors"
	"fmt"
	"time"
	"unsafe"
)

// memoryPoll is how often the job's memory is read while the command runs.
const memoryPoll = 50 * time.Millisecond

// limitFits reports whether a memory limit fits the job's SIZE_T field, whose largest value
// is max: on 32-bit Windows a larger one would wrap to a smaller ceiling than the one asked for.
func limitFits(limit, max uint64) bool { return limit <= max }

// over reports whether a peak shows the ceiling was hit. The job refuses a commit that would
// take it past the limit and counts the refused amount in its peak, so the peak passing the
// limit is the sign; reaching it exactly is a commit the job allowed.
func over(peak, limit uint64) bool { return peak > limit }

// watchMemory reads the job's peak memory while the command runs and stops the tree once it
// passes the ceiling, whether the command then crashed or went on with failing allocations. The
// job's completion-port notice of the same event is not relied on: Windows does not promise to
// deliver it. A peak that cannot be read stops the tree too, since the ceiling can no longer be
// watched; the error is kept for the caller.
func (t *Tree) watchMemory() {
	t.stopWatch = make(chan struct{})
	t.watchDone = make(chan struct{})
	go func() {
		defer close(t.watchDone)
		tick := time.NewTicker(memoryPoll)
		defer tick.Stop()
		for {
			select {
			case <-t.stopWatch:
				return
			case <-tick.C:
			}
			peak, err := t.readPeak()
			if err != nil {
				t.setMemoryErr(err)
				_ = t.Kill()
				return
			}
			if over(peak, t.limit) {
				t.exceeded.Store(true)
				_ = t.Kill()
				return
			}
		}
	}()
}

// stopWatching ends the watch and waits for it, so it never reads a job handle that was closed.
func (t *Tree) stopWatching() {
	t.stopOnce.Do(func() {
		if t.stopWatch != nil {
			close(t.stopWatch)
			<-t.watchDone
		}
	})
}

func (t *Tree) setMemoryErr(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.memoryErr == nil {
		t.memoryErr = err
	}
}

// errJobClosed means the job is gone, so its memory cannot be read.
var errJobClosed = errors.New("the job is closed")

// queryPeak reads the job's peak committed memory, refused commits counted.
func (t *Tree) queryPeak() (uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job == 0 {
		return 0, errJobClosed
	}
	var info jobObjectExtendedLimitInformation
	if ok, _, err := procQueryInformationJobObject.Call(uintptr(t.job), jobObjectExtendedLimitInformationClass, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0); ok == 0 {
		return 0, fmt.Errorf("read the job's memory: %w", err)
	}
	return uint64(info.PeakJobMemoryUsed), nil
}

// MemoryEnforced reports whether a memory ceiling was set and is enforced here: always, on
// Windows, through the job.
func (t *Tree) MemoryEnforced() bool { return t.limit > 0 }

// MemoryHard reports whether the kernel holds the ceiling: on Windows the job does, whenever
// there is one.
func (t *Tree) MemoryHard() bool { return t.limit > 0 }

// RemoveCgroup has nothing to remove on Windows: the job goes with Close.
func (t *Tree) RemoveCgroup() error { return nil }

// FinalMemory is the tree's memory once the command has ended: whether it passed its ceiling,
// its peak in bytes (committed memory, a refused commit counted), and why the ceiling could
// not be watched, when it could not. It first stops the watch and waits for it, so nothing it
// finds afterwards is missed; then it reads the peak once. Call it before Close.
func (t *Tree) FinalMemory() (exceeded bool, peak uint64, err error) {
	t.stopWatching()
	peak, readErr := t.readPeak()
	if readErr != nil && t.limit > 0 {
		t.setMemoryErr(readErr)
	}
	exceeded = t.limit > 0 && (t.exceeded.Load() || readErr == nil && over(peak, t.limit))
	if readErr != nil {
		peak = 0
	}
	return exceeded, peak, t.MemoryErr()
}

// MemoryErr is why the ceiling could not be watched, when it could not; the tree was stopped
// then.
func (t *Tree) MemoryErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.memoryErr
}

// errLimitTooLarge is the error for a ceiling the platform's job cannot hold.
func errLimitTooLarge(limit uint64) error {
	return fmt.Errorf("%w: a memory limit of %d bytes is larger than this platform can set", ErrNotContained, limit)
}
