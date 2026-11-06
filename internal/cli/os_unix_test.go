//go:build !windows

package cli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func helperSignal() {
	syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {}
}

// A command ended by a signal exits 128 plus the signal's number, as shells report it.
func TestACommandEndedByASignalExits128PlusItsNumber(t *testing.T) {
	setup(t)
	code, _, stderr := main(t, context.Background(), "", "signal", "run", "--")
	if code != ExitSignal+int(syscall.SIGTERM) {
		t.Errorf("code %d, stderr %s; want %d", code, stderr, ExitSignal+int(syscall.SIGTERM))
	}
}

// elevated agrees with id -u, which is the user's own account of being root.
func TestElevatedAgreesWithID(t *testing.T) {
	out, err := exec.Command("id", "-u").Output()
	if err != nil {
		t.Skipf("id: %v", err)
	}
	if want := strings.TrimSpace(string(out)) == "0"; elevated() != want {
		t.Errorf("elevated() = %v, id -u says %s", elevated(), out)
	}
}
