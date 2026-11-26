package originals

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

// standInOpen makes the first `missing` opens of the lock report the file missing, as macOS can
// while another process creates it, and counts every open.
func standInOpen(t *testing.T, missing int) *int {
	t.Helper()
	calls := 0
	old := openLock
	openLock = func(r *os.Root) (*os.File, error) {
		calls++
		if calls <= missing {
			return nil, &fs.PathError{Op: "openat", Path: lockName, Err: fs.ErrNotExist}
		}
		return old(r)
	}
	t.Cleanup(func() { openLock = old })
	return &calls
}

func TestALockOpenReportedMissingDuringACreateIsRetried(t *testing.T) {
	r, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	calls := standInOpen(t, 2)
	f, err := lockIn(r, "the test directory")
	if err != nil {
		t.Fatalf("lockIn: %v", err)
	}
	f.Close()
	if *calls != 3 {
		t.Fatalf("opened %d times, want 3", *calls)
	}
}

func TestALockThatStaysMissingFailsAfterAFewTries(t *testing.T) {
	r, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	calls := standInOpen(t, 1000)
	if _, err := lockIn(r, "the test directory"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v, want a missing-file error", err)
	}
	if *calls != 5 {
		t.Fatalf("opened %d times, want 5", *calls)
	}
}
