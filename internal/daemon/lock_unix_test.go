//go:build !windows

package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// nobody is an account that is not the test's, for the root-only tests.
const nobody = 65534

// A client does not send its command to a socket another user made, and a daemon does not
// take a directory another user owns. Only root can set those up.
func TestAnotherUsersSocketOrDirectoryIsRefused(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to make files another user owns")
	}
	dir := shortDir(t)
	socket := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.Lchown(socket, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), socket, goTest, nil); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("Acquire on another user's socket = %v, want refused", err)
	}
	other := filepath.Join(shortDir(t), "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(other, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen(context.Background(), filepath.Join(other, "d.sock"), nil); err == nil {
		t.Error("Listen took a directory another user owns")
	}
}

func TestTheSocketAndItsDirectoryAreOwnerOnly(t *testing.T) {
	dir := filepath.Join(shortDir(t), "open")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o777)
	socket := filepath.Join(dir, "d.sock")
	_, closeFn, err := Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	for path, want := range map[string]os.FileMode{dir: 0o700, socket: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
}

// The configuration is read as the policy is: one others can write is refused, and a pipe is
// refused at once, not waited on.
func TestAConfigOthersCanWriteOrAPipeIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(path, []byte(`{"version": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Error("a config others can write was read")
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := LoadConfig(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a pipe was read as a config")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadConfig waited on a pipe")
	}
}
