package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// printing is a command that writes parts in order: "out:" ones to stdout, "err:" ones to stderr.
func printing(t *testing.T, parts ...string) Command {
	t.Helper()
	dir := t.TempDir()
	var list []string
	for i, p := range parts {
		stream, text, _ := strings.Cut(p, ":")
		path := filepath.Join(dir, fmt.Sprint(i))
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		list = append(list, stream+":"+path)
	}
	return helper("print", "ADANEXT_TEST_PRINT="+strings.Join(list, string(os.PathListSeparator)))
}

func handleOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

// goTestOutput is verbose go test output: n passing tests, then one failing with a message.
func goTestOutput(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "=== RUN   TestPass%04d\n--- PASS: TestPass%04d (0.00s)\n", i, i)
	}
	b.WriteString("=== RUN   TestBroken\n    broken_test.go:42: got 3 rows, want 4\n--- FAIL: TestBroken (0.01s)\n")
	return b.String()
}

func originalsIn(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestShapePassesAShortOutputAsItIsWithBothStreamsInOrder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "originals")
	c := printing(t, "out:first\n", "err:second\n", "out:third\n")
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	c.Shape = &Shape{Dir: dir}
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "first\nsecond\nthird\n" || stderr.Len() != 0 {
		t.Fatalf("stdout %q, stderr %q, want the three lines in order on stdout", stdout.String(), stderr.String())
	}
	if res.Shaped || res.Original != "" || res.OutputCut != 0 || len(originalsIn(t, dir)) != 0 {
		t.Fatalf("a short output was shaped or kept: %+v", res)
	}
}

func TestShapePutsACheckedSummaryInPlaceOfALargeTestOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "originals")
	out := goTestOutput(1000)
	tail := "FAIL\nFAIL\texample/pkg\t0.512s\n"
	whole := out + tail
	c := printing(t, "out:"+out, "err:"+tail)
	var stdout bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stdout
	c.Shape = &Shape{Dir: dir}
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	h := handleOf(whole)
	got := stdout.String()
	trailer := fmt.Sprintf("\noriginal: %s (%d bytes)\n", h, len(whole))
	if !strings.HasPrefix(got, "tests: passed=1000 failed=1\n") || !strings.HasSuffix(got, trailer) ||
		!strings.Contains(got, "    broken_test.go:42: got 3 rows, want 4\n") || !strings.Contains(got, "FAIL\texample/pkg\t0.512s") {
		t.Fatalf("output %q, want the summary with the failure and the trailer", got)
	}
	if len(got) >= len(whole)/4 {
		t.Fatalf("the summary is %d bytes of %d", len(got), len(whole))
	}
	if !res.Shaped || res.Original != h || res.OriginalBytes != int64(len(whole)) || res.OutputCut != 0 {
		t.Fatalf("result %+v, want shaped with handle %s", res, h)
	}
	if kept, err := os.ReadFile(filepath.Join(dir, h+".txt")); err != nil || string(kept) != whole {
		t.Fatalf("the original holds %d bytes (%v), want the whole output", len(kept), err)
	}
}

func TestShapeCutsAnOutputItCannotSummarizeAndKeepsTheOriginal(t *testing.T) {
	dir := t.TempDir()
	line := digits(40000) // one line past the head-and-tail's bounds has no summary
	c := printing(t, "out:"+line)
	var stdout bytes.Buffer
	c.Stdout = &stdout
	c.Shape = &Shape{Dir: dir}
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	h := handleOf(line)
	want := line[:20000] + "\n[... 10000 bytes cut ...]\n" + line[30000:] + "\n" + fmt.Sprintf("original: %s (40000 bytes)\n", h)
	if stdout.String() != want {
		t.Fatalf("output of %d bytes, want the head, the cut, the tail and the trailer", stdout.Len())
	}
	if res.Shaped || res.Original != h || res.OutputCut != 10000 {
		t.Fatalf("result %+v, want cut by 10000 with the original kept", res)
	}
}

func TestShapeHonoursItsOwnThreshold(t *testing.T) {
	dir := t.TempDir()
	text := strings.Repeat("same line\n", 200)
	c := printing(t, "out:"+text)
	var stdout bytes.Buffer
	c.Stdout = &stdout
	c.Shape = &Shape{Dir: dir, Over: 1000}
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("same line\n[repeated 200 times]\noriginal: %s (2000 bytes)\n", handleOf(text))
	if stdout.String() != want || !res.Shaped {
		t.Fatalf("output %q (%+v), want %q", stdout.String(), res, want)
	}
}

func TestShapeCutsAndSaysWhyWhenTheOriginalWouldPassACap(t *testing.T) {
	for name, s := range map[string]struct {
		dirMax  int64
		prefill int
	}{
		"larger than the cap": {35000, 0},
		"no room left for it": {50000, 20000},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if s.prefill > 0 {
				if err := os.WriteFile(filepath.Join(dir, handleOf("x")+".txt"), bytes.Repeat([]byte("x"), s.prefill), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			line := digits(40000)
			c := printing(t, "out:"+line)
			var stdout bytes.Buffer
			c.Stdout = &stdout
			c.Shape = &Shape{Dir: dir, DirMax: s.dirMax}
			kept := 0
			if s.prefill > 0 {
				kept = 1
			}
			res, err := Run(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if want := line[:20000] + "\n[... 10000 bytes cut ...]\n" + line[30000:]; stdout.String() != want {
				t.Fatalf("output of %d bytes, want the cut without a trailer", stdout.Len())
			}
			if res.Original != "" || res.OriginalRefused == "" || res.OutputCut != 10000 || len(originalsIn(t, dir)) != kept {
				t.Fatalf("result %+v, want the original refused with a reason and nothing kept", res)
			}
		})
	}
}

func TestShapeIsInvalidWithAnOutputCapOrWithoutADirectory(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	for name, s := range map[string]*Shape{
		"no directory":    {},
		"a negative over": {Dir: t.TempDir(), Over: -1},
		"a negative cap":  {Dir: t.TempDir(), DirMax: -1},
		"with output cap": {Dir: t.TempDir()},
	} {
		c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
		c.Shape = s
		if name == "with output cap" {
			c.OutputCap = 100
		}
		res, err := Run(context.Background(), c)
		if !errors.Is(err, ErrInvalid) || res != (Result{}) {
			t.Errorf("%s: %v (%+v), want ErrInvalid", name, err, res)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an invalid shape ran the command")
	}
}

func TestShapeDoesNotRunACommandWhoseDirectoryCannotBeUsed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	file := filepath.Join(t.TempDir(), "originals")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	audit := t.TempDir()
	c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
	c.Shape = &Shape{Dir: file}
	c.Audit = &Audit{Dir: audit}
	res, err := Run(context.Background(), c)
	if err == nil || res != (Result{}) {
		t.Fatalf("%v (%+v), want an error and no run", err, res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran with a directory it could not use")
	}
	if l := readAudit(t, audit)[0]; l["outcome"] != "not run" || l["error"] == nil {
		t.Fatalf("audit line %v, want not run with the error", l)
	}
}

func TestTheAuditSaysAShapingHappenedWithoutTheContent(t *testing.T) {
	dir, audit := t.TempDir(), t.TempDir()
	out := goTestOutput(1000)
	c := printing(t, "out:"+out)
	c.Shape = &Shape{Dir: dir}
	c.Audit = &Audit{Dir: audit}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	l := readAudit(t, audit)[0]
	if l["shaped"] != true || l["original"] != handleOf(out) || l["original_bytes"] != float64(len(out)) {
		t.Fatalf("audit line %v, want shaped with the handle and size", l)
	}
	if bytes.Contains(auditBytes(t, audit), []byte("broken_test.go")) {
		t.Fatal("the audit holds the output")
	}

	// A refused original is said too.
	audit = t.TempDir()
	c = printing(t, "out:"+digits(40000))
	c.Shape = &Shape{Dir: t.TempDir(), DirMax: 1000}
	c.Audit = &Audit{Dir: audit}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if l := readAudit(t, audit)[0]; l["original_refused"] == nil || l["output_cut"] != float64(10000) || l["shaped"] != nil {
		t.Fatalf("audit line %v, want the refusal and the cut", l)
	}
}

func TestShapeFailsTheResultWhenTheOriginalCannotBeKeptForAnotherReason(t *testing.T) {
	dir := t.TempDir()
	line := digits(40000)
	if err := os.WriteFile(filepath.Join(dir, handleOf(line)+".txt"), []byte("other bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := printing(t, "out:"+line)
	var stdout bytes.Buffer
	c.Stdout = &stdout
	c.Shape = &Shape{Dir: dir}
	res, err := Run(context.Background(), c)
	if err == nil || res == (Result{}) || res.Original != "" || res.OriginalRefused != "" || res.OutputCut != 10000 {
		t.Fatalf("%v (%+v), want an error after the run and the output cut", err, res)
	}
	if want := line[:20000] + "\n[... 10000 bytes cut ...]\n" + line[30000:]; stdout.String() != want {
		t.Fatalf("output of %d bytes, want the cut", stdout.Len())
	}
}
