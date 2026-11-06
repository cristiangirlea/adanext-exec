package broker

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

const mib = 1 << 20

func TestANegativeMemoryLimitIsInvalid(t *testing.T) {
	c := helper("exit")
	c.MemoryLimit = -1
	if _, err := Run(context.Background(), c); !errors.Is(err, ErrInvalid) {
		t.Errorf("Run = %v, want ErrInvalid", err)
	}
}

// A tree that goes over its ceiling is stopped and says so, where the platform enforces it; a
// grandchild's memory counts too.
func TestATreeOverItsMemoryCeilingIsStopped(t *testing.T) {
	for _, mode := range []string{"alloc", "alloc-child"} {
		c := helper(mode, "ADANEXT_TEST_ALLOC_MB=512")
		c.MemoryLimit = 128 * mib
		c.Timeout = time.Minute
		c.Audit = &Audit{Dir: t.TempDir()}
		start := time.Now()
		res, err := Run(context.Background(), c)
		took := time.Since(start)
		lines := readAudit(t, c.Audit.Dir)
		if runtime.GOOS != "windows" {
			// Not enforced here yet: it says so, and the command runs as it would.
			if err != nil || res.MemoryEnforced || res.MemoryExceeded || res.ExitCode != 0 {
				t.Errorf("%s: %+v, %v; want it run, the ceiling not enforced", mode, res, err)
			}
			if lines[0]["memory_enforced"] != false || lines[0]["memory_limit"] != float64(128*mib) {
				t.Errorf("%s: audit %v, want the ceiling recorded as not enforced", mode, lines[0])
			}
			continue
		}
		if err != nil || !res.MemoryEnforced || !res.MemoryExceeded || res.ExitCode != -1 {
			t.Errorf("%s: %+v, %v; want it stopped at its ceiling", mode, res, err)
		}
		if took > 30*time.Second {
			t.Errorf("%s: stopping took %v", mode, took)
		}
		// The job's accounting is by commit, not byte: it may stand a little over the ceiling
		// at the moment it stops the tree.
		if res.PeakMemory <= 0 || res.PeakMemory > 132*mib {
			t.Errorf("%s: peak memory %d, want some, and not much over the ceiling", mode, res.PeakMemory)
		}
		if peak, _ := lines[0]["peak_memory"].(float64); int64(peak) != res.PeakMemory {
			t.Errorf("%s: audit peak %v, result peak %d", mode, lines[0]["peak_memory"], res.PeakMemory)
		}
		if lines[0]["outcome"] != "over its memory ceiling" || lines[0]["memory_enforced"] != true {
			t.Errorf("%s: audit %v, want the ceiling", mode, lines[0])
		}
	}
}

// A tree that stays under its ceiling runs to its end, with its peak reported where known.
func TestATreeUnderItsCeilingRuns(t *testing.T) {
	c := helper("alloc", "ADANEXT_TEST_ALLOC_MB=32")
	c.MemoryLimit = 1024 * mib
	res, err := Run(context.Background(), c)
	if err != nil || res.ExitCode != 0 || res.MemoryExceeded {
		t.Fatalf("%+v, %v; want it run to its end", res, err)
	}
	if runtime.GOOS == "windows" && (!res.MemoryEnforced || res.PeakMemory < 32*mib) {
		t.Errorf("%+v; want the ceiling enforced and a peak of at least 32 MiB", res)
	}
}

// No ceiling asked for: nothing about memory is claimed.
func TestNoCeilingClaimsNothing(t *testing.T) {
	res, err := Run(context.Background(), helper("exit"))
	if err != nil || res.MemoryEnforced || res.MemoryExceeded {
		t.Errorf("%+v, %v", res, err)
	}
}
