//go:build darwin

package tree

import (
	"errors"
	"testing"
)

// macOS has no hard ceiling: CeilingCgroup does not start the command, and auto watches it.
func TestStartCeilingOnMacOS(t *testing.T) {
	cmd := helperCmd("exit")
	if _, err := StartCeiling(cmd, 64<<20, CeilingCgroup); !errors.Is(err, ErrNoHardCeiling) || cmd.Process != nil {
		t.Fatalf("StartCeiling(cgroup) = %v, process %v; want ErrNoHardCeiling and nothing started", err, cmd.Process)
	}
	tr, _ := startCeiling(t, "exit", 64<<20, CeilingAuto)
	if !tr.MemoryEnforced() || tr.MemoryHard() {
		t.Errorf("auto: enforced %v, hard %v; want watched, not hard", tr.MemoryEnforced(), tr.MemoryHard())
	}
}
