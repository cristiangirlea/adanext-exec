//go:build linux || darwin

package tree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// memoryPoll is how often the watch reads the tree's memory.
const memoryPoll = 100 * time.Millisecond

// probeRSS is how StartLimited learns whether memory can be read here; tests replace it.
var probeRSS = residentBytes

// StartLimited is Start with a ceiling on the resident memory of the whole tree, in bytes; 0 is
// none. A watch reads the memory of every process in the tree's group and of their descendants
// every 100 ms, and kills the tree once the total passes the ceiling. It is best effort, and
// says so: a spike shorter than one read can pass. Where the platform's memory cannot be read
// here, the ceiling is not enforced, and MemoryEnforced says so.
func StartLimited(cmd *exec.Cmd, memoryLimit uint64) (*Tree, error) {
	return startWatched(cmd, memoryLimit, nil)
}

// startWatched is StartLimited for a command started in the cgroup cg (nil for none), which the
// tree holds before its watch starts.
func startWatched(cmd *exec.Cmd, memoryLimit uint64, cg *cgroup) (*Tree, error) {
	t, err := Start(cmd)
	if err != nil {
		return nil, err
	}
	t.cg = cg
	t.limit = memoryLimit
	t.readRSS = residentBytes
	t.readMark = func(pid int) (bool, error) { return envHas(pid, t.marker) }
	if memoryLimit > 0 {
		// A platform that cannot read the broker's own memory cannot read the tree's either:
		// then nothing is watched, and MemoryEnforced says so.
		if _, err := probeRSS(os.Getpid()); err == nil {
			t.enforced = true
			t.watchMemory()
		}
	}
	return t, nil
}

// watchMemory reads the tree's resident memory until stopped, and kills the tree once it passes
// the ceiling. A process table that cannot be read stops the tree too, since the ceiling can no
// longer be watched; the error is kept.
func (t *Tree) watchMemory() {
	t.stopWatch = make(chan struct{})
	t.watchDone = make(chan struct{})
	go func() {
		defer close(t.watchDone)
		tick := time.NewTicker(memoryPoll)
		defer tick.Stop()
		marks := map[pidStart]bool{}
		for {
			select {
			case <-t.stopWatch:
				return
			case <-tick.C:
			}
			total, err := t.treeMemory(marks)
			if err != nil {
				t.setMemoryErr(err)
				_ = t.Kill()
				return
			}
			for {
				seen := t.peakSeen.Load()
				if total <= seen || t.peakSeen.CompareAndSwap(seen, total) {
					break
				}
			}
			if total > t.limit {
				t.exceeded.Store(true)
				_ = t.Kill()
				return
			}
		}
	}()
}

// pidStart names one process across reads: a pid with its start time.
type pidStart struct {
	pid   int
	start uint64
}

// treeMemory sums the resident memory of the tree: its group, the processes that carry its
// marker (also those that left the group), and their descendants. A process's environment
// does not change once it runs, so whether it carries the marker is read once per process and
// kept; an environment that could not be read is read again next time. The table is read again after the samples: a sample counts only if its pid still names
// the process scanned, so a pid reused by another program is never counted, nor its error
// taken for the tree's.
func (t *Tree) treeMemory(marks map[pidStart]bool) (uint64, error) {
	procs, err := scanProcs()
	if err != nil {
		return 0, fmt.Errorf("read the process table: %w", err)
	}
	t.mu.Lock()
	byGroup := t.lost == nil
	read, readMark := t.readRSS, t.readMark
	t.mu.Unlock()
	starts := make(map[int]uint64, len(procs))
	for _, p := range procs {
		starts[p.pid] = p.start
	}
	seen := make(map[pidStart]bool, len(marks))
	marked := func(pid int) bool {
		key := pidStart{pid, starts[pid]}
		v, ok := marks[key]
		if !ok {
			var err error
			v, err = readMark(pid)
			if err != nil {
				// Not known this time: asked again next time, never taken for "no marker".
				return false
			}
			marks[key] = v
		}
		seen[key] = true
		return v
	}
	type sample struct {
		p   proc
		rss uint64
		err error
		// Where the process is, when the kernel holds the ceiling and it is outside the
		// command's cgroup, or why that could not be read.
		outside string
		cgErr   error
	}
	var samples []sample
	for _, p := range t.members(procs, byGroup, marked) {
		s := sample{p: p}
		s.rss, s.err = read(p.pid)
		if t.cg != nil {
			s.outside, s.cgErr = t.cg.outside(p.pid)
		}
		samples = append(samples, s)
	}
	for key := range marks {
		if !seen[key] {
			delete(marks, key)
		}
	}
	after, err := scanProcs()
	if err != nil {
		return 0, fmt.Errorf("read the process table: %w", err)
	}
	now := make(map[int]proc, len(after))
	for _, q := range after {
		now[q.pid] = q
	}
	var total uint64
	for _, s := range samples {
		q, ok := now[s.p.pid]
		if !ok || q.start != s.p.start || q.zombie {
			// Ended, or the pid names another process now: not the tree's.
			continue
		}
		if s.err != nil {
			if procEnded(s.err) {
				continue
			}
			return 0, fmt.Errorf("read the memory of process %d: %w", s.p.pid, s.err)
		}
		if s.cgErr != nil && !procEnded(s.cgErr) {
			return 0, fmt.Errorf("read the cgroup of process %d: %w", s.p.pid, s.cgErr)
		}
		if s.outside != "" {
			// The kernel no longer holds the ceiling for it: stop the tree, never call it hard.
			return 0, fmt.Errorf("process %d left the command's cgroup for %s", s.p.pid, s.outside)
		}
		total += s.rss
	}
	return total, nil
}

// procEnded reports whether a read failed because the process has ended.
func procEnded(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// stopWatching ends the watch and waits for it.
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

// MemoryEnforced reports whether a ceiling was set and is watched or held here.
func (t *Tree) MemoryEnforced() bool { return t.enforced || t.cg != nil }

// MemoryHard reports whether the kernel holds the ceiling: the command runs in a cgroup of its
// own with memory.max set (StartCeiling, Linux).
func (t *Tree) MemoryHard() bool { return t.cg != nil }

// FinalMemory is the tree's memory once the command has ended: whether it passed its ceiling,
// the most memory the watch saw it hold or its cgroup's peak, whichever is more (0 when neither
// is known), and why the ceiling could not be watched, when it could not. It stops the watch
// and waits for it first. A tree in a cgroup passed its ceiling also when the kernel's
// out-of-memory killer acted in that cgroup.
func (t *Tree) FinalMemory() (exceeded bool, peak uint64, err error) {
	t.stopWatching()
	t.mu.Lock()
	err = t.memoryErr
	t.mu.Unlock()
	exceeded, peak = t.exceeded.Load(), t.peakSeen.Load()
	if t.cg != nil {
		over, cgPeak, cgErr := t.cg.final()
		exceeded = exceeded || over
		peak = max(peak, cgPeak)
		err = errors.Join(err, cgErr)
	}
	return exceeded, peak, err
}
