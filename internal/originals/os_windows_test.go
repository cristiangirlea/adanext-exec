//go:build windows

package originals

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A junction needs no privilege to make, so Windows tests a link at the directory's name with one.
func TestOpenRefusesAJunctionAtTheDirectorysName(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "originals")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("no junction: %v %s", err, out)
	}
	if _, err := Open(link, "", 0, 0); err == nil {
		t.Fatal("took a junction for the originals directory")
	}
	s, err := Open(target, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A junction put in the directory's place after Open is refused too.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", target, other).CombinedOutput(); err != nil {
		t.Skipf("no junction: %v %s", err, out)
	}
	if _, err := s.Save([]byte("x")); err == nil {
		t.Fatal("saved through a junction put in the directory's place")
	}
}
