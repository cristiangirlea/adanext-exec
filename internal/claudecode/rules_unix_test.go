//go:build !windows

package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A pipe in a settings file's place is refused at once, with or without a writer, instead of
// holding the hook.
func TestLoadRefusesAPipeAtOnce(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, writer := range []bool{false, true} {
		if writer {
			// A writer that never writes: a read would wait for it.
			w, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
		}
		done := make(chan error, 1)
		go func() {
			_, err := Load(Sources{Project: project})
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("writer %v: %v, want not a regular file", writer, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("writer %v: Load still waits on the pipe", writer)
		}
	}
}

// A settings file the user cannot read is an error that names it.
func TestLoadFailsOnAFileItMayNotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	project := t.TempDir()
	path := filepath.Join(project, ".claude", "settings.json")
	write(t, path, `{}`)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Sources{Project: project}); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("%v, want an error naming %s", err, path)
	}
}
