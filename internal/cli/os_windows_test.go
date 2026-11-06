//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func helperSignal() {}

// elevated agrees with whoami: an elevated token carries the high mandatory level, S-1-16-12288.
func TestElevatedAgreesWithWhoami(t *testing.T) {
	out, err := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe"), "/groups").Output()
	if err != nil {
		t.Skipf("whoami: %v", err)
	}
	if want := strings.Contains(string(out), "S-1-16-12288"); elevated() != want {
		t.Errorf("elevated() = %v, whoami says high level %v", elevated(), want)
	}
}
