package broker

import "testing"

// The kernel can force a few pages past memory.max (reclaim, allocations that must not fail);
// 64 pages is ample room for that and far below a tree that is not held.
const ceilingSlack = 64 * 4096

func heldAtCeiling(peak, ceiling int64) bool {
	return peak <= ceiling+ceilingSlack
}

func TestHeldAtCeilingAllowsAForcedChargeButNotARunaway(t *testing.T) {
	for _, c := range []struct {
		peak int64
		held bool
	}{
		{128 * mib, true},
		{128*mib + 4096, true},
		{128*mib + 256*1024, true},
		{128*mib + 256*1024 + 1, false},
		{512 * mib, false},
	} {
		if got := heldAtCeiling(c.peak, 128*mib); got != c.held {
			t.Errorf("heldAtCeiling(%d, 128 MiB) = %v, want %v", c.peak, got, c.held)
		}
	}
}
