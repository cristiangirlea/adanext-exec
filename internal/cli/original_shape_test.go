package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cristiangirlea/adanext-exec/internal/originals"
)

// Under run --shape a read is at most the threshold, the size the caller takes an agent to see whole.
func TestRunShapeBoundsAReadByTheThreshold(t *testing.T) {
	e := setup(t)
	useTestBinaryAsSelf(t, e)
	text := digitsText(60000)
	dir, h := keep(t, text)
	code, stdout, stderr := main(t, context.Background(), "", "", "run", "--shape", "--originals", dir, "--shell", "bash", "--", "adanext-exec original "+h+" --range 0-30001")
	if code != ExitUsage || stdout != "" || !strings.Contains(stderr, "30000") {
		t.Fatalf("a read past the threshold: code %d, %d bytes, stderr %q; want %d naming the bound", code, len(stdout), stderr, ExitUsage)
	}
	code, stdout, _ = main(t, context.Background(), "", "", "run", "--shape", "--originals", dir, "--shell", "bash", "--", "adanext-exec original "+h+" --range 30000-60000")
	if code != 0 || stdout != text[30000:] {
		t.Fatalf("a read of the threshold: code %d, %d bytes", code, len(stdout))
	}
}

// The hint never names more than one read gives, whatever the threshold, and the command it names runs.
func TestRunShapeHintsARangeOneReadGives(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	code, stdout, stderr := main(t, context.Background(), "", "print:2100000", "run", "--shape", "--originals", dir, "--output-cap", "2000000", "--")
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr)
	}
	body := strings.TrimSuffix(stdout, "\n")
	last := body[strings.LastIndex(body, "\n")+1:]
	h := handleOf(strings.Repeat("x", 2100000))
	tail := " reads it, at most 1048576 bytes a read"
	if want := fmt.Sprintf("original: %s (2100000 bytes); adanext-exec original %s --range 0-1048576", h, h) + tail; last != want {
		t.Fatalf("last line %q, want %q", last, want)
	}
	useTestBinaryAsSelf(t, e)
	read := strings.TrimSuffix(strings.SplitN(last, "; ", 2)[1], tail)
	code, stdout, stderr = main(t, context.Background(), "", "", "run", "--shape", "--originals", dir, "--output-cap", "2000000", "--shell", "bash", "--", read)
	if code != 0 || len(stdout) != originals.MaxRead {
		t.Fatalf("the hinted %q: code %d, %d bytes, stderr %q", read, code, len(stdout), stderr)
	}
}

// The policy judges the agent's own text of a read: a refusal refuses it, and an ask runs it only
// with the approval.
func TestRunShapeJudgesAReadAsTheAgentWroteIt(t *testing.T) {
	e := setup(t)
	useTestBinaryAsSelf(t, e)
	dir, h := keep(t, "0123456789")
	read := "adanext-exec original " + h
	refusing := e.writePolicy(t, "no-reads.json", `{"version": 1, "rules": [{"name": "no-reads", "match": "^adanext-exec\\s+original", "decision": "refuse"}]}`)
	code, stdout, stderr := main(t, context.Background(), "", "", "run", "--shape", "--originals", dir, "--policy", refusing, "--shell", "bash", "--", read)
	if code != ExitRefused || stdout != "" || !strings.Contains(stderr, "no-reads") {
		t.Fatalf("a refused read: code %d, stdout %q, stderr %q; want %d naming the rule", code, stdout, stderr, ExitRefused)
	}
	asking := e.writePolicy(t, "ask-reads.json", `{"version": 1, "rules": [{"name": "ask-reads", "match": "^adanext-exec\\s+original", "decision": "ask"}]}`)
	if code, _, _ := main(t, context.Background(), "", "", "run", "--shape", "--originals", dir, "--policy", asking, "--shell", "bash", "--", read); code != ExitRefused {
		t.Fatalf("an ask without approval: code %d, want %d", code, ExitRefused)
	}
	code, stdout, stderr = main(t, context.Background(), "", "", "run", "--shape", "--originals", dir, "--policy", asking, "--approved=ask-reads", "--shell", "bash", "--", read)
	if code != 0 || stdout != "0123456789" {
		t.Fatalf("an approved read: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// Text that only looks like a read runs as written and is shaped: here a shell function of the
// name prints more than the threshold.
func TestRunShapeShapesWhatOnlyLooksLikeARead(t *testing.T) {
	e := setup(t)
	useTestBinaryAsSelf(t, e)
	dir, h := keep(t, "0123456789")
	script := `adanext-exec() { head -c 40000 /dev/zero | tr '\0' y; }; adanext-exec original ` + h
	code, stdout, stderr := main(t, context.Background(), "", "", "run", "--shape", "--originals", dir, "--shell", "bash", "--", script)
	if code != 0 || !strings.Contains(stdout, "[... 10000 bytes cut ...]") || !strings.Contains(stdout, "\noriginal: "+handleOf(strings.Repeat("y", 40000))) {
		t.Fatalf("code %d, stdout of %d bytes ending %q, stderr %q; want the function's output shaped", code, len(stdout), stdout[max(0, len(stdout)-200):], stderr)
	}
}

// End to end: through the tool's shell, a shaping rewrite shapes a large output, the read its
// last line names comes back through the hook as the original's bytes, and a background call's
// rewrite runs unshaped. The originals directory's name needs quoting in the shell.
func TestTheHooksShapingRewriteRunsThroughTheShell(t *testing.T) {
	e := setup(t)
	exe := useTestBinaryForRewrite(t, e)
	old := selfExecutable
	selfExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExecutable = old })
	dir := filepath.Join(t.TempDir(), "it's a ’dir’", "originals")
	if err := os.Mkdir(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	through := func(command string, background bool) (int, string, string) {
		t.Helper()
		_, d, stderr := hookMain(t, hookRequest(t, "Bash", map[string]any{"command": command, "run_in_background": background}), "claude-code", "--shape", "--originals", dir)
		if d == nil || d.Decision != "allow" {
			t.Fatalf("%q: answer %+v, stderr %q", command, d, stderr)
		}
		return main(t, context.Background(), "", "", "run", "--shell", "bash", "--", rewriteOf(t, d))
	}
	big := `head -c 40000 /dev/zero | tr '\0' z`
	text := strings.Repeat("z", 40000)
	h := handleOf(text)
	code, stdout, stderr := through(big, false)
	if code != 0 || !strings.Contains(stdout, "[... 10000 bytes cut ...]") || !strings.HasSuffix(stdout, "original "+h+" --range 0-30000 reads it, at most 30000 bytes a read\n") {
		t.Fatalf("code %d, stdout of %d bytes ending %q, stderr %q; want it shaped", code, len(stdout), stdout[max(0, len(stdout)-200):], stderr)
	}
	code, stdout, stderr = through("adanext-exec original "+h+" --range 0-30000", false)
	if code != 0 || stdout != text[:30000] {
		t.Fatalf("the hinted read: code %d, %d bytes, stderr %q; want the original's first 30000 bytes", code, len(stdout), stderr)
	}
	code, stdout, _ = through(big, true)
	if code != 0 || stdout != text {
		t.Fatalf("a background call: code %d, %d bytes; want the output as it is", code, len(stdout))
	}
}
