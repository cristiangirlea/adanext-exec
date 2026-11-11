package originals

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func saved(t *testing.T, text string) (dir, handle string) {
	t.Helper()
	dir = t.TempDir()
	h, err := open(t, dir, "", 0, 0).Save([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return dir, h
}

func TestReadGivesARangeOrTheWhole(t *testing.T) {
	text := "0123456789abcdef"
	dir, h := saved(t, text)
	for _, c := range []struct {
		rng  string
		want string
	}{
		{"", text},
		{"0-4", "0123"},
		{"10-16", "abcdef"},
		{"4-4", ""},
		{"12-99", "cdef"}, // an end past the original reads to its end
		{"16-20", ""},     // a start at its end reads nothing
	} {
		got, size, err := Read(dir, h, c.rng, 0)
		if err != nil || string(got) != c.want || size != int64(len(text)) {
			t.Errorf("range %q: %q, %d (%v), want %q", c.rng, got, size, err, c.want)
		}
	}
}

func TestReadRefusesWhatIsNotARangeOfTheOriginal(t *testing.T) {
	dir, h := saved(t, "0123456789")
	for _, rng := range []string{"17-20", "5-4", "4", "-4", "a-b", "1-2-3", " 0-4", "0-4 ", "+1-4", "99999999999999999999-1"} {
		if _, _, err := Read(dir, h, rng, 0); !errors.Is(err, ErrRange) {
			t.Errorf("range %q: %v, want ErrRange", rng, err)
		}
	}
}

func TestReadIsBounded(t *testing.T) {
	text := strings.Repeat("x", MaxRead+1)
	dir, h := saved(t, text)
	if _, _, err := Read(dir, h, "", 0); !errors.Is(err, ErrRange) || !strings.Contains(err.Error(), "--range") {
		t.Fatalf("a whole original past the bound: %v, want ErrRange naming --range", err)
	}
	if _, _, err := Read(dir, h, "0-1048577", 0); !errors.Is(err, ErrRange) {
		t.Fatalf("a range past the bound: %v, want ErrRange", err)
	}
	got, _, err := Read(dir, h, "1-1048577", 0)
	if err != nil || !bytes.Equal(got, []byte(text[1:])) {
		t.Fatalf("a range of exactly the bound: %d bytes (%v)", len(got), err)
	}
}

func TestReadTakesOnlyAHandle(t *testing.T) {
	dir, h := saved(t, "x")
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", strings.ToUpper(h), h[:15], h + "0", "../secret", "..\\secret", "/etc/passwd", h + ".txt", "0123456789abcdeg"} {
		if got, _, err := Read(dir, bad, "", 0); !errors.Is(err, ErrHandle) {
			t.Errorf("handle %q: %q (%v), want ErrHandle", bad, got, err)
		}
	}
	if _, _, err := Read(dir, Handle([]byte("not kept")), "", 0); err == nil || errors.Is(err, ErrHandle) {
		t.Fatalf("a handle with no original: %v, want an error that is not ErrHandle", err)
	}
}

func TestReadRefusesALinkAtTheHandlesName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := Handle([]byte("outside"))
	if err := os.Symlink(target, filepath.Join(dir, h+".txt")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if got, _, err := Read(dir, h, "", 0); err == nil {
		t.Fatalf("read %q through a link", got)
	}
}

func TestReadRefusesADirectoryThatIsNotPlain(t *testing.T) {
	file := filepath.Join(t.TempDir(), "originals")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(file, Handle(nil), "", 0); err == nil {
		t.Fatal("read from a file as the originals directory")
	}
}

func TestReadTakesASmallerBound(t *testing.T) {
	dir, h := saved(t, "0123456789")
	if got, _, err := Read(dir, h, "2-7", 5); err != nil || string(got) != "23456" {
		t.Fatalf("a range of the bound: %q (%v)", got, err)
	}
	for _, rng := range []string{"0-6", ""} {
		if _, _, err := Read(dir, h, rng, 5); !errors.Is(err, ErrRange) || !strings.Contains(err.Error(), "5") {
			t.Errorf("range %q past a bound of 5: %v, want ErrRange naming the bound", rng, err)
		}
	}
}
