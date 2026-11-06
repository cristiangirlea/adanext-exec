package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ErrRunning means another daemon already serves the socket.
var ErrRunning = errors.New("a daemon is already running")

// Listen makes the daemon's socket, as the only daemon for it. It takes an exclusive lock on
// a file beside the socket for as long as the daemon runs, so two daemons never start at once,
// and only under that lock removes a socket a daemon that ended left behind: a socket, the
// user's, that refuses a connection. The directory must be the user's, hold nothing but the
// daemon's own files, and is made owner-only
// (0700 on Unix, a protected access list for the user, SYSTEM and Administrators on Windows),
// and so is the socket where the platform has file modes.
// Before it listens it waits, within ctx, until no client of an earlier daemon still holds a
// slot (waiting is told once if it must wait), so a restarted daemon never grants a slot that
// a command from before is still using. closeFn stops listening, removes the socket and lets
// the lock go.
func Listen(ctx context.Context, socket string, waiting func()) (l net.Listener, closeFn func(), err error) {
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	// Making the directory owner-only changes who can reach everything in it, so it must be
	// the daemon's own: the socket and its two lock files, nothing else.
	if err := onlyDaemonFiles(dir, filepath.Base(socket)); err != nil {
		return nil, nil, err
	}
	if err := ownerOnlyDir(dir); err != nil {
		return nil, nil, err
	}
	unlock, err := lockFile(socket + ".lock")
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (net.Listener, func(), error) {
		unlock()
		return nil, nil, err
	}
	if fi, err := os.Lstat(socket); err == nil {
		// Only a socket of the user's that nothing serves is stale: anything else at the name
		// (a file, a link, another user's socket) is left alone and refused.
		if fi.Mode()&fs.ModeSocket == 0 {
			return fail(fmt.Errorf("%s is not a socket; not removing it", socket))
		}
		if err := ownedByUser(socket); err != nil {
			return fail(err)
		}
		d := net.Dialer{Timeout: time.Second}
		c, err := d.DialContext(context.Background(), "unix", socket)
		switch {
		case err == nil:
			// One that predates the lock still answers: it is not stale.
			c.Close()
			return fail(ErrRunning)
		case !refused(err):
			return fail(fmt.Errorf("%s may not be stale: %w", socket, err))
		}
		if err := os.Remove(socket); err != nil {
			return fail(fmt.Errorf("remove the stale socket: %w", err))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fail(err)
	}
	if err := awaitLeases(ctx, socket, waiting); err != nil {
		return fail(err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fail(err)
	}
	if err := ownerOnlySocket(socket); err != nil {
		ln.Close()
		return fail(err)
	}
	return ln, func() {
		ln.Close()
		os.Remove(socket)
		unlock()
	}, nil
}

// lockFile takes the daemon's own lock: exclusive, or ErrRunning when another daemon has it.
func lockFile(path string) (unlock func(), err error) {
	unlock, busy, err := tryLock(path, true)
	if busy {
		return nil, ErrRunning
	}
	return unlock, err
}

// leasesFile is the file whose shared locks fence a daemon's start: every client that holds a
// slot holds a shared lock on it until its command's tree is dead, or its process ends.
func leasesFile(socket string) string { return socket + ".leases" }

// awaitLeases waits until no client of an earlier daemon still holds a slot: until it can take
// the leases file exclusively, which it then lets go, since its own clients take it shared.
func awaitLeases(ctx context.Context, socket string, waiting func()) error {
	told := false
	for {
		unlock, busy, err := tryLock(leasesFile(socket), true)
		if err != nil {
			return err
		}
		if !busy {
			unlock()
			return nil
		}
		if !told && waiting != nil {
			waiting()
			told = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// onlyDaemonFiles refuses a directory that holds anything but the socket and its lock files.
func onlyDaemonFiles(dir, socket string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		switch e.Name() {
		case socket, socket + ".lock", leasesFile(socket):
		default:
			return fmt.Errorf("%s holds %s: the socket needs a directory of its own", dir, e.Name())
		}
	}
	return nil
}
