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
		if err != nil || !res.MemoryEnforced || !res.MemoryExceeded || res.ExitCode != -1 {
			t.Errorf("%s: %+v, %v; want it stopped at its ceiling", mode, res, err)
		}
		if took > 30*time.Second {
			t.Errorf("%s: stopping took %v", mode, took)
		}
		// Windows counts commits, and stands a little over the ceiling when it stops the tree;
		// the watch elsewhere reads resident memory every 100 ms, and can see it a read's growth
		// past it.
		most := int64(132 * mib)
		if runtime.GOOS != "windows" {
			most = 512 * mib
		}
		if res.PeakMemory <= 0 || res.PeakMemory > most {
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
	if !res.MemoryEnforced || res.PeakMemory < 32*mib {
		t.Errorf("%+v; want the ceiling enforced and a peak of at least 32 MiB", res)
	}
}

func TestAnUnknownMemoryCeilingIsInvalid(t *testing.T) {
	c := helper("exit")
	c.MemoryCeiling = "hard"
	if _, err := Run(context.Background(), c); !errors.Is(err, ErrInvalid) {
		t.Errorf("Run = %v, want ErrInvalid", err)
	}
}

func TestThePolicyNamesAMemoryCeiling(t *testing.T) {
	for _, m := range []MemoryCeiling{CeilingAuto, CeilingCgroup, CeilingWatchdog} {
		p, err := ParsePolicy([]byte(`{"version": 1, "memory_ceiling": "` + string(m) + `"}`))
		if err != nil || p.ceiling != m {
			t.Errorf("%s: ceiling %q, %v", m, p.ceiling, err)
		}
	}
	if p, err := ParsePolicy([]byte(`{"version": 1}`)); err != nil || p.ceiling != "" {
		t.Errorf("left out: ceiling %q, %v; want none", p.ceiling, err)
	}
	for _, bad := range []string{`""`, `null`, `"hard"`, `1`, `["auto"]`} {
		if _, err := ParsePolicy([]byte(`{"version": 1, "memory_ceiling": ` + bad + `}`)); !errors.Is(err, ErrPolicy) {
			t.Errorf("memory_ceiling %s: %v, want ErrPolicy", bad, err)
		}
	}
}

// The policy's ceiling holds for a command that names none, and a command's own wins. Where the
// kernel cannot hold a ceiling, cgroup runs nothing: macOS never can, Windows always can, and
// Linux can where a delegated cgroup is free to divide (rarely under go test).
func TestTheMemoryCeilingIsChosenByThePolicyOrTheCommand(t *testing.T) {
	policy, err := ParsePolicy([]byte(`{"version": 1, "memory_ceiling": "cgroup"}`))
	if err != nil {
		t.Fatal(err)
	}
	c := helper("exit")
	c.MemoryLimit = 256 * mib
	c.Policy = policy
	c.Audit = &Audit{Dir: t.TempDir()}
	res, err := Run(context.Background(), c)
	lines := readAudit(t, c.Audit.Dir)
	switch {
	case runtime.GOOS == "darwin" || runtime.GOOS == "linux" && errors.Is(err, ErrNoHardCeiling):
		if !errors.Is(err, ErrNoHardCeiling) || lines[0]["outcome"] != "not run" {
			t.Errorf("cgroup: %v, audit %v; want ErrNoHardCeiling and nothing run", err, lines[0])
		}
	default:
		if err != nil || !res.MemoryEnforced || !res.MemoryHard || lines[0]["memory_hard"] != true {
			t.Errorf("cgroup: %+v, %v, audit %v; want the kernel to hold the ceiling", res, err, lines[0])
		}
	}

	c.MemoryCeiling = CeilingWatchdog
	res, err = Run(context.Background(), c)
	if err != nil || !res.MemoryEnforced || res.MemoryHard != (runtime.GOOS == "windows") {
		t.Errorf("watchdog: %+v, %v; want it watched (held by the job on Windows)", res, err)
	}
}

// No ceiling asked for: nothing about memory is claimed.
func TestNoCeilingClaimsNothing(t *testing.T) {
	res, err := Run(context.Background(), helper("exit"))
	if err != nil || res.MemoryEnforced || res.MemoryHard || res.MemoryExceeded {
		t.Errorf("%+v, %v", res, err)
	}
}
