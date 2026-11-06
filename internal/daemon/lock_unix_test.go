//go:build !windows

package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
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
