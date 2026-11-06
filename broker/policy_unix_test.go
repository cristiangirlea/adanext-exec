//go:build !windows

package broker

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Whoever can change the policy decides what runs: a file others can write is refused.
func TestLoadPolicyRefusesAFileOthersCanWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"version": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o620, 0o602, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPolicy(path); !errors.Is(err, ErrPolicy) {
			t.Errorf("mode %v: LoadPolicy = %v, want ErrPolicy", mode, err)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(path); err != nil {
		t.Errorf("mode 0644: LoadPolicy = %v, want it read", err)
	}
}

// A pipe with no writer is refused at once, not waited on, also through a link; a link to a
// policy file reads that file.
func TestLoadPolicyRefusesAPipeWithoutWaiting(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, link} {
		done := make(chan error, 1)
		go func() {
			_, err := LoadPolicy(path)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrPolicy) {
				t.Errorf("LoadPolicy(%s) = %v, want ErrPolicy", path, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("LoadPolicy(%s) still waits on the pipe", path)
		}
	}
	file := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(file, []byte(`{"version": 1, "default": "refuse"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	toFile := filepath.Join(dir, "to-file")
	if err := os.Symlink(file, toFile); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(toFile)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := p.Check(Command{Args: []string{"ls"}}); v != (Verdict{Refuse, ""}) {
		t.Errorf("Check = %+v, want the linked file's default", v)
	}
}
