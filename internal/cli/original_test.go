package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cristiangirlea/adanext-exec/broker"
	"github.com/cristiangirlea/adanext-exec/internal/originals"
)

// keep saves text as an original in a new directory and returns the directory and the handle.
func keep(t *testing.T, text string) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "originals")
	s, err := originals.Open(dir, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Save([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return dir, h
}

func TestOriginalPrintsARangeOrTheWhole(t *testing.T) {
	setup(t)
	text := "0123456789"
	dir, h := keep(t, text)
	for args, want := range map[string]string{
		"--range 2-5":   "234",
		"--range=7-100": "789",
		"":              text,
	} {
		argv := append([]string{"original", h}, strings.Fields(args)...)
		argv = append(argv, "--originals", dir)
		if code, stdout, stderr := main(t, context.Background(), "", "", argv...); code != 0 || stdout != want {
			t.Errorf("%v: code %d, stdout %q, stderr %q; want %q", argv, code, stdout, stderr, want)
		}
	}
	// The handle may come after the flags too.
	if code, stdout, _ := main(t, context.Background(), "", "", "original", "--originals", dir, "--range", "0-1", h); code != 0 || stdout != "0" {
		t.Fatalf("handle last: code %d, stdout %q", code, stdout)
	}
}

func TestOriginalRefusesWhatItCannotRead(t *testing.T) {
	setup(t)
	dir, h := keep(t, "0123456789")
	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"original", "--originals", dir}, ExitUsage},
		{[]string{"original", h}, ExitUsage},
		{[]string{"original", h, "extra", "--originals", dir}, ExitUsage},
		{[]string{"original", "../" + h, "--originals", dir}, ExitUsage},
		{[]string{"original", h, "--range", "5-1", "--originals", dir}, ExitUsage},
		{[]string{"original", h, "--range", "20-30", "--originals", dir}, ExitUsage},
		{[]string{"original", h, "--range", "0-1048577", "--originals", dir}, ExitUsage},
		{[]string{"original", h, "--range", "0-6", "--max", "5", "--originals", dir}, ExitUsage},
		{[]string{"original", h, "--max", "0", "--originals", dir}, ExitUsage},
		{[]string{"original", h, "--max", "1048577", "--originals", dir}, ExitUsage},
		{[]string{"original", originals.Handle([]byte("never kept")), "--originals", dir}, ExitCannotRun},
		{[]string{"original", h, "--originals", filepath.Join(dir, "missing")}, ExitCannotRun},
	} {
		if code, stdout, stderr := main(t, context.Background(), "", "", c.args...); code != c.code || stdout != "" || stderr == "" {
			t.Errorf("%v: code %d, stdout %q, stderr %q; want %d with a message", c.args, code, stdout, stderr, c.code)
		}
	}
}

// useTestBinaryAsSelf makes run's read of an original start the test binary as the binary itself.
func useTestBinaryAsSelf(t *testing.T, e env) {
	t.Helper()
	old := selfExecutable
	selfExecutable = func() (string, error) { return os.Args[0], nil }
	t.Cleanup(func() { selfExecutable = old })
	t.Setenv(helperEnv, "main")
	t.Setenv("ADANEXT_CLI_CONFIG", e.config)
	t.Setenv("ADANEXT_CLI_AUDIT", e.audit)
	t.Setenv("ADANEXT_CLI_CLAUDE", e.claude)
}

func TestRunShapePassesAReadOfAnOriginalThroughWhole(t *testing.T) {
	e := setup(t)
	useTestBinaryAsSelf(t, e)
	text := digitsText(60000)
	dir, h := keep(t, text)
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"shell text with a range":  {[]string{"--shell", "bash", "--", "adanext-exec original " + h + " --range 1000-51000\n"}, text[1000:51000]},
		"shell text, range joined": {[]string{"--shell", "pwsh", "--", "adanext-exec.exe original " + h + " --range=0-40000"}, text[:40000]},
		"argv, the whole":          {[]string{"--", "adanext-exec", "original", h}, text},
	} {
		// A threshold of 60000, so one read may take the whole original.
		argv := append([]string{"run", "--shape", "--originals", dir, "--output-cap", "60000"}, c.args...)
		code, stdout, stderr := main(t, context.Background(), "", "", argv...)
		if code != 0 || stdout != c.want {
			t.Errorf("%s: code %d, %d bytes (stderr %q), want the %d bytes of the original unshaped", name, code, len(stdout), stderr, len(c.want))
		}
	}
	data, err := os.ReadFile(auditFile(t, e.audit))
	if err != nil {
		t.Fatal(err)
	}
	var line struct {
		Args   []string `json:"args"`
		Shaped bool     `json:"shaped"`
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if err := json.Unmarshal([]byte(first), &line); err != nil {
		t.Fatal(err)
	}
	if len(line.Args) < 3 || line.Args[0] != os.Args[0] || line.Args[1] != "original" || line.Args[2] != h || line.Shaped {
		t.Fatalf("audit args %q (shaped %v), want this binary's own read", line.Args, line.Shaped)
	}
}

func TestOnlyAnExactReadOfAnOriginalIsPassedThrough(t *testing.T) {
	h := originals.Handle([]byte("x"))
	for _, script := range []string{
		"adanext-exec original " + h,
		"  adanext-exec   original\t" + h + "  \n",
		"adanext-exec original " + h + " --range 0-10",
		"adanext-exec.exe original " + h + " --range=5-10",
	} {
		if originalRead(broker.Command{Shell: broker.ShellBash, Script: script}) == nil {
			t.Errorf("%q is a read", script)
		}
	}
	for _, script := range []string{
		"adanext-exec original " + h + "; cat secrets.txt",
		"adanext-exec original " + h + " && echo done",
		"adanext-exec original " + h + "\ncat x",
		"/usr/local/bin/adanext-exec original " + h,
		"./adanext-exec original " + h,
		"adanext-exec original " + h + " --originals /elsewhere",
		"adanext-exec original " + strings.ToUpper(h),
		"adanext-exec original " + h + " --range 0-10 --range 0-20",
		"adanext-exec original " + h + " extra",
		"adanext-exec original $(cat h)",
		"adanext-exec original " + h + " > out.txt",
		"ADANEXT=1 adanext-exec original " + h,
		"adanext-exec run -- adanext-exec original " + h,
	} {
		if r := originalRead(broker.Command{Shell: broker.ShellBash, Script: script}); r != nil {
			t.Errorf("%q taken for a read: %q", script, r)
		}
	}
	if r := originalRead(broker.Command{Args: []string{"adanext-exec", "original", h, "--range", "0-10"}}); !slices.Equal(r, []string{h, "--range", "0-10"}) {
		t.Errorf("argv read: %q", r)
	}
	for _, args := range [][]string{
		{"/bin/adanext-exec", "original", h},
		{"adanext-exec", "original", h, "--originals", "/x"},
		{"adanext-exec", "original", h, "--range"},
		{"adanext-exec", "original", h + " "},
	} {
		if r := originalRead(broker.Command{Args: args}); r != nil {
			t.Errorf("%q taken for a read: %q", args, r)
		}
	}
}

func TestRunWithoutShapeRunsAReadAsWritten(t *testing.T) {
	setup(t)
	// No adanext-exec in PATH: a read taken over by run would find this binary instead.
	t.Setenv("PATH", t.TempDir())
	code, _, stderr := main(t, context.Background(), "", "", "run", "--", "adanext-exec", "original", "0123456789abcdef")
	if code != ExitNotFound {
		t.Fatalf("code %d (stderr %q), want the program looked up as written", code, stderr)
	}
}

func TestRunShapeNamesTheReadInTheLastLine(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	code, stdout, stderr := main(t, context.Background(), "", "print:40000", "run", "--shape", "--originals", dir, "--")
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr)
	}
	h := handleOf(strings.Repeat("x", 40000))
	want := fmt.Sprintf("\noriginal: %s (40000 bytes); adanext-exec original %s --range 0-30000 reads it, at most 30000 bytes a read\n", h, h)
	if !strings.HasSuffix(stdout, want) {
		t.Fatalf("stdout ends %q, want %q", stdout[max(0, len(stdout)-200):], want)
	}
}

func digitsText(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%05d", i)
	}
	return b.String()[:n]
}
