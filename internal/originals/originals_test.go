package originals

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperEnv = "ADANEXT_ORIGINALS_HELPER"

// TestMain runs the test binary as a writer of its own when helperEnv names a directory: it
// saves ADANEXT_ORIGINALS_TEXT there, under ADANEXT_ORIGINALS_ROOT when set, with the caps
// ADANEXT_ORIGINALS_MAX and ADANEXT_ORIGINALS_ALL (empty: the default), and exits 0 when the
// original was kept, 3 when the store was full, 1 on any other error.
func TestMain(m *testing.M) {
	if dir := os.Getenv(helperEnv); dir != "" {
		dirMax, _ := strconv.ParseInt(os.Getenv("ADANEXT_ORIGINALS_MAX"), 10, 64)
		allMax, _ := strconv.ParseInt(os.Getenv("ADANEXT_ORIGINALS_ALL"), 10, 64)
		s, err := Open(dir, os.Getenv("ADANEXT_ORIGINALS_ROOT"), dirMax, allMax)
		if err == nil {
			_, err = s.Save([]byte(os.Getenv("ADANEXT_ORIGINALS_TEXT")))
		}
		switch {
		case errors.Is(err, ErrFull):
			os.Exit(3)
		case err != nil:
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func hashOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

func open(t *testing.T, dir, root string, dirMax, allMax int64) *Store {
	t.Helper()
	s, err := Open(dir, root, dirMax, allMax)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSaveNamesAnOriginalByTheHashOfItsBytes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "originals")
	s := open(t, dir, "", 0, 0)
	text := "line one\nline two\n"
	h, err := s.Save([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if h != hashOf(text) || Handle([]byte(text)) != h {
		t.Fatalf("handle %q, want %q", h, hashOf(text))
	}
	got, err := os.ReadFile(filepath.Join(dir, h+".txt"))
	if err != nil || string(got) != text {
		t.Fatalf("the original holds %q (%v), want %q", got, err, text)
	}
}

func TestOpenMakesOnlyTheLastDirectory(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "originals"), "", 0, 0); err == nil {
		t.Fatal("made a directory whose parent is missing")
	}
	if _, err := Open("", "", 0, 0); err == nil {
		t.Fatal("opened no directory")
	}
	if _, err := Open(t.TempDir(), "", -1, 0); err == nil {
		t.Fatal("took a negative cap")
	}
}

func TestOpenRefusesAFileAtTheDirectorysName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "originals")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "", 0, 0); err == nil {
		t.Fatal("took a file for the originals directory")
	}
}

func TestOpenRefusesALinkAtTheDirectorysName(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "originals")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if _, err := Open(link, "", 0, 0); err == nil {
		t.Fatal("took a link for the originals directory")
	}
}

func TestSavingTheSameBytesAgainKeepsOneFileAndCountsNothing(t *testing.T) {
	dir := t.TempDir()
	text := strings.Repeat("a", 60)
	s := open(t, dir, "", 60, 60) // exactly one original's room
	for range 2 {
		if _, err := s.Save([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if n := files(t, dir); n != 1 {
		t.Fatalf("%d files, want 1", n)
	}
}

func TestTheDirectorysCapRefusesAndKeepsWhatIsThere(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, "", 100, 0)
	first, err := s.Save([]byte(strings.Repeat("a", 60)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save([]byte(strings.Repeat("b", 41))); !errors.Is(err, ErrFull) {
		t.Fatalf("41 bytes over 60 of 100 saved (%v), want ErrFull", err)
	}
	if _, err := os.Stat(filepath.Join(dir, first+".txt")); err != nil {
		t.Fatalf("the first original went: %v", err)
	}
	if _, err := s.Save([]byte(strings.Repeat("c", 40))); err != nil {
		t.Fatalf("40 bytes into the 40 left: %v", err)
	}
	if _, err := s.Save([]byte(strings.Repeat("d", 101))); !errors.Is(err, ErrFull) {
		t.Fatalf("an original larger than the cap saved (%v)", err)
	}
}

func TestTheCapOverAllCountsEveryRunUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(root, "run-1", "originals")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, hashOf("x")+".txt"), []byte(strings.Repeat("x", 80)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "run-2"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file beside the runs and a run with no originals count nothing.
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(strings.Repeat("n", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "run-3"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := open(t, filepath.Join(root, "run-2", "originals"), root, 0, 100)
	if _, err := s.Save([]byte(strings.Repeat("a", 21))); !errors.Is(err, ErrFull) {
		t.Fatalf("21 bytes over 80 of 100 in all saved (%v), want ErrFull", err)
	}
	if _, err := s.Save([]byte(strings.Repeat("a", 20))); err != nil {
		t.Fatalf("20 bytes into the 20 left: %v", err)
	}
}

func TestTheCapOverAllCountsAFileLinkedTwiceOnce(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a", "originals"), filepath.Join(root, "b", "originals")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	name := hashOf("x") + ".txt"
	if err := os.WriteFile(filepath.Join(a, name), []byte(strings.Repeat("x", 60)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(a, name), filepath.Join(b, name)); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	dir := filepath.Join(root, "c", "originals")
	if err := os.Mkdir(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir, root, 0, 100)
	if _, err := s.Save([]byte(strings.Repeat("a", 40))); err != nil {
		t.Fatalf("a file linked into two runs counted twice: %v", err)
	}
}

func TestADirectoryOutsideTheRootStillCounts(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	s := open(t, dir, root, 0, 100)
	if _, err := s.Save([]byte(strings.Repeat("a", 60))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save([]byte(strings.Repeat("b", 60))); !errors.Is(err, ErrFull) {
		t.Fatalf("the caller's own directory was left out of the cap over all (%v)", err)
	}
}

func TestOtherBytesAtTheHandlesNameAreAnError(t *testing.T) {
	dir := t.TempDir()
	text := "the output"
	if err := os.WriteFile(filepath.Join(dir, hashOf(text)+".txt"), []byte("something else"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir, "", 0, 0)
	if _, err := s.Save([]byte(text)); err == nil || errors.Is(err, ErrFull) {
		t.Fatalf("other bytes at the handle's name: %v, want an error that is not ErrFull", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, hashOf(text)+".txt")); string(got) != "something else" {
		t.Fatalf("the file at the handle's name was replaced: %q", got)
	}
}

func TestALinkAtTheHandlesNameIsAnError(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	text := "the output"
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, hashOf(text)+".txt")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	s := open(t, dir, "", 0, 0)
	if _, err := s.Save([]byte(text)); err == nil {
		t.Fatal("a link at the handle's name was taken for the original")
	}
}

func TestWritersInSeveralProcessesKeepWithinTheCap(t *testing.T) {
	dir := t.TempDir()
	const writers, size, limit = 8, 30, 100
	cmds := make([]*exec.Cmd, writers)
	for i := range cmds {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), helperEnv+"="+dir, "ADANEXT_ORIGINALS_MAX="+strconv.Itoa(limit),
			"ADANEXT_ORIGINALS_TEXT="+fmt.Sprintf("%0*d", size, i))
		cmds[i] = cmd
	}
	kept := runWriters(t, cmds)
	if kept != limit/size {
		t.Fatalf("%d kept, want %d", kept, limit/size)
	}
	if total := sizeOf(t, dir); total > limit {
		t.Fatalf("%d bytes kept under a cap of %d", total, limit)
	}
}

// Every run's directory under one root, each written by its own process, keeps within the cap
// over all: the root's lock holds them together.
func TestWritersUnderOneRootKeepWithinTheCapOverAll(t *testing.T) {
	root := t.TempDir()
	const writers, size, limit = 8, 30, 100
	cmds := make([]*exec.Cmd, writers)
	for i := range cmds {
		run := filepath.Join(root, fmt.Sprintf("run-%d", i))
		if err := os.Mkdir(run, 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), helperEnv+"="+filepath.Join(run, "originals"), "ADANEXT_ORIGINALS_ROOT="+root,
			"ADANEXT_ORIGINALS_ALL="+strconv.Itoa(limit), "ADANEXT_ORIGINALS_TEXT="+fmt.Sprintf("%0*d", size, i))
		cmds[i] = cmd
	}
	if kept := runWriters(t, cmds); kept != limit/size {
		t.Fatalf("%d kept, want %d", kept, limit/size)
	}
	var total int64
	for i := range writers {
		total += sizeOf(t, filepath.Join(root, fmt.Sprintf("run-%d", i), "originals"))
	}
	if total > limit {
		t.Fatalf("%d bytes kept under a cap over all of %d", total, limit)
	}
}

// runWriters starts the writers together and counts those that kept their original; every other
// one must have been refused as full.
func runWriters(t *testing.T, cmds []*exec.Cmd) int {
	t.Helper()
	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	kept, full := 0, 0
	for _, cmd := range cmds {
		err := cmd.Wait()
		var exit *exec.ExitError
		switch {
		case err == nil:
			kept++
		case errors.As(err, &exit) && exit.ExitCode() == 3:
			full++
		default:
			t.Errorf("a writer failed: %v", err)
		}
	}
	if kept+full != len(cmds) {
		t.Fatalf("%d kept and %d refused of %d", kept, full, len(cmds))
	}
	return kept
}

// sizeOf sums the originals in dir; a missing dir holds none.
func sizeOf(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".txt") {
			total += info.Size()
		}
	}
	return total
}

func TestSaveDoesNotFollowADirectorySwappedForALink(t *testing.T) {
	parent, other := t.TempDir(), t.TempDir()
	dir := filepath.Join(parent, "originals")
	s := open(t, dir, "", 0, 0)
	if err := os.Rename(dir, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if _, err := s.Save([]byte("x")); err == nil {
		t.Fatal("saved through a link put in the directory's place")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("wrote %d files where the link points", len(entries))
	}
}

func TestALinkAtTheLocksNameIsNotFollowedOutOfTheDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, lockName)); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	s := open(t, dir, "", 0, 0)
	if _, err := s.Save([]byte("x")); err == nil {
		t.Fatal("took a lock through a link out of the directory")
	}
}

func TestAHeldLockTimesOutWithAnErrorThatIsNotFull(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, "", 0, 0)
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if busy, err := tryLock(f); err != nil || busy {
		t.Fatalf("take the lock: busy %v, %v", busy, err)
	}
	old := lockWait
	lockWait = 100 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	if _, err := s.Save([]byte("x")); err == nil || errors.Is(err, ErrFull) || !strings.Contains(err.Error(), "held") {
		t.Fatalf("saved past a held lock: %v", err)
	}
}

func TestAStoreThatCannotBeMeasuredRefusesWithAnErrorThatIsNotFull(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "run-x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run-x", "originals"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "run-y"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := open(t, filepath.Join(root, "run-y", "originals"), root, 0, 0)
	if _, err := s.Save([]byte("x")); err == nil || errors.Is(err, ErrFull) {
		t.Fatalf("saved with a store it could not measure: %v", err)
	}
}

func TestOriginalsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps the directory's access list")
	}
	dir := filepath.Join(t.TempDir(), "originals")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir, "", 0, 0)
	h, err := s.Save([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, h+".txt"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s has mode %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}

// files counts the originals in dir, leaving out the lock.
func files(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".txt") {
			n++
		}
	}
	return n
}
