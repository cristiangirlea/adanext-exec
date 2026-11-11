package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func handleOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

func TestRunShapeKeepsTheOriginalInTheCallersDirectory(t *testing.T) {
	e := setup(t)
	dir := filepath.Join(t.TempDir(), "originals")
	code, stdout, stderr := main(t, context.Background(), "", "print:40000", "run", "--shape", "--originals", dir, "--")
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr)
	}
	text := strings.Repeat("x", 40000)
	h := handleOf(text)
	want := text[:20000] + "\n[... 10000 bytes cut ...]\n" + text[30000:] + "\n" +
		fmt.Sprintf("original: %s (40000 bytes); adanext-exec original %s --range 0-30000 reads it, at most 1048576 bytes a read\n", h, h)
	if stdout != want {
		t.Fatalf("stdout of %d bytes, want the cut and the original's line", len(stdout))
	}
	if kept, err := os.ReadFile(filepath.Join(dir, h+".txt")); err != nil || string(kept) != text {
		t.Fatalf("the original holds %d bytes (%v)", len(kept), err)
	}
	data, err := os.ReadFile(auditFile(t, e.audit))
	if err != nil || !strings.Contains(string(data), `"original":"`+h+`"`) {
		t.Fatalf("the audit does not give the handle: %s (%v)", data, err)
	}
}

func TestRunShapeTakesItsThresholdFromTheOutputCap(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	code, stdout, stderr := main(t, context.Background(), "", "print:150", "run", "--shape", "--originals", dir, "--output-cap", "100", "--")
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr)
	}
	text := strings.Repeat("x", 150)
	h := handleOf(text)
	if want := text + "\n" + fmt.Sprintf("original: %s (150 bytes); adanext-exec original %s --range 0-100 reads it, at most 1048576 bytes a read\n", h, h); stdout != want {
		t.Fatalf("stdout %q, want %q", stdout, want)
	}
}

func TestRunShapeSaysWhenTheOriginalIsNotKept(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	code, stdout, stderr := main(t, context.Background(), "", "print:40000", "run", "--shape", "--originals", dir, "--originals-max", "1K", "--")
	if code != 0 || !strings.Contains(stderr, "the original was not kept") || strings.Contains(stdout, "original:") {
		t.Fatalf("code %d, stdout %d bytes, stderr %q; want 0, the cut and the note", code, len(stdout), stderr)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*.txt")); len(entries) != 0 {
		t.Fatalf("kept %v past the cap", entries)
	}
}

func TestRunShapeTakesTheRootAndItsCap(t *testing.T) {
	setup(t)
	root := t.TempDir()
	other := filepath.Join(root, "run-1", "originals")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, handleOf("y")+".txt"), []byte(strings.Repeat("y", 30000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "run-2"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "run-2", "originals")
	args := []string{"run", "--shape", "--originals", dir, "--originals-root", root, "--originals-all-max", "60000", "--"}
	if code, _, stderr := main(t, context.Background(), "", "print:40000", args...); code != 0 || !strings.Contains(stderr, "the original was not kept") {
		t.Fatalf("code %d, stderr %q; want the cap over all to refuse", code, stderr)
	}
}

func TestRunShapeFlagsGoTogether(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	for _, args := range [][]string{
		{"run", "--shape", "--"},
		{"run", "--originals", dir, "--"},
		{"run", "--originals-root", dir, "--"},
		{"run", "--originals-max", "1M", "--"},
		{"run", "--originals-all-max", "1M", "--"},
		{"run", "--shape", "--originals", dir, "--originals-max", "lots", "--"},
		{"run", "--shape", "--originals", dir, "--originals-all-max", "-1", "--"},
	} {
		if code, _, stderr := main(t, context.Background(), "", "exit:0", args...); code != ExitUsage {
			t.Errorf("%v: code %d (stderr %q), want %d", args, code, stderr, ExitUsage)
		}
	}
}

func TestRunShapeDoesNotRunWithADirectoryItCannotUse(t *testing.T) {
	e := setup(t)
	file := filepath.Join(t.TempDir(), "originals")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := main(t, context.Background(), "", "touch", "run", "--shape", "--originals", file, "--")
	if code != ExitCannotRun || e.ran() {
		t.Fatalf("code %d (stderr %q), ran %v; want %d and not run", code, stderr, e.ran(), ExitCannotRun)
	}
}

// auditFile is the audit's only file.
func auditFile(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("audit files %v (%v), want one", files, err)
	}
	return files[0]
}
