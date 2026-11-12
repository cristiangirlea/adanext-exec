//go:build !windows

package originals

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A lock attempt a signal interrupts is tried again, not taken for a failure; any other error ends
// it at once.
func TestALockAttemptASignalInterruptsIsTriedAgain(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), lockName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := flock
	t.Cleanup(func() { flock = old })
	for _, then := range []struct {
		err  error
		busy bool
	}{{nil, false}, {syscall.EWOULDBLOCK, true}} {
		calls := 0
		flock = func(fd, how int) error {
			if calls++; calls <= 2 {
				return syscall.EINTR
			}
			return then.err
		}
		busy, err := tryLock(f)
		if err != nil || busy != then.busy || calls != 3 {
			t.Fatalf("interrupted twice, then %v: busy %v, %v after %d calls; want busy %v, no error, 3 calls", then.err, busy, err, calls, then.busy)
		}
	}
	calls := 0
	flock = func(fd, how int) error { calls++; return syscall.EBADF }
	if busy, err := tryLock(f); err == nil || busy || calls != 1 {
		t.Fatalf("a bad descriptor: busy %v, %v after %d calls; want the error after 1 call", busy, err, calls)
	}
}
