//go:build linux || darwin

package tree

import (
	"errors"
	"syscall"
	"testing"
)

func TestWaitExitLeavesTheLeaderUnreapedSoItsGroupIdIsHeld(t *testing.T) {
	cmd := helperCmd("exit")
	tr := startTree(t, cmd)
	if err := tr.WaitExit(); err != nil {
		t.Fatal(err)
	}
	// The leader has exited but is not reaped: its group still exists, so a Kill now cannot
	// reach a process that took the id over. macOS answers EPERM for a group of zombies.
	if err := syscall.Kill(-cmd.Process.Pid, 0); errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the group of the exited leader: %v, want it held until Wait", err)
	}
	if err := tr.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait after WaitExit: %v, want the clean exit", err)
	}
}
