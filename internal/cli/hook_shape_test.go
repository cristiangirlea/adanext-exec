package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// rewriteOf is the command line the hook's answer runs.
func rewriteOf(t *testing.T, d *hookDecision) string {
	t.Helper()
	if d == nil || d.Input == nil {
		t.Fatalf("answer %+v has no rewrite", d)
	}
	var command string
	if err := json.Unmarshal(d.Input["command"], &command); err != nil {
		t.Fatal(err)
	}
	return command
}

func TestTheHookCarriesShapingIntoTheRewrite(t *testing.T) {
	setup(t)
	root := t.TempDir()
	dir := filepath.Join(root, "run-1", "originals")
	args := []string{"claude-code", "--shape", "--originals", dir, "--originals-root", root,
		"--originals-max", "8M", "--originals-all-max", "512M", "--output-cap", "20000"}
	code, d, stderr := hookMain(t, hookRequest(t, "Bash", map[string]any{"command": "go test ./..."}), args...)
	if code != 0 || d.Decision != "allow" {
		t.Fatalf("code %d, answer %+v, stderr %q", code, d, stderr)
	}
	rewrite := rewriteOf(t, d)
	for _, want := range []string{"'--shape'", "'--originals' '" + dir + "'", "'--originals-root' '" + root + "'",
		"'--originals-max' '8M'", "'--originals-all-max' '512M'", "'--output-cap' '20000'"} {
		if !strings.Contains(rewrite, want) {
			t.Errorf("rewrite %q lacks %s", rewrite, want)
		}
	}
	// A call that runs in the background is read as it comes, so it is not shaped.
	_, d, _ = hookMain(t, hookRequest(t, "Bash", map[string]any{"command": "go test ./...", "run_in_background": true}), args...)
	if rewrite := rewriteOf(t, d); strings.Contains(rewrite, "--shape") || strings.Contains(rewrite, "--originals") {
		t.Fatalf("a background call's rewrite shapes: %q", rewrite)
	}
}

func TestTheHookMakesTheOriginalsPathsAbsolute(t *testing.T) {
	setup(t)
	t.Chdir(t.TempDir())
	_, d, _ := hookMain(t, hookRequest(t, "Bash", map[string]any{"command": "ls"}), "claude-code", "--shape", "--originals", "rel")
	abs, err := filepath.Abs("rel")
	if err != nil {
		t.Fatal(err)
	}
	if rewrite := rewriteOf(t, d); !strings.Contains(rewrite, "'--originals' '"+abs+"'") {
		t.Fatalf("rewrite %q, want the absolute %s", rewrite, abs)
	}
}

func TestTheHookDeniesShapingFlagsThatDoNotGoTogether(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	req := hookRequest(t, "Bash", map[string]any{"command": "ls"})
	for _, args := range [][]string{
		{"claude-code", "--shape"},
		{"claude-code", "--originals", dir},
		{"claude-code", "--output-cap", "100"},
		{"claude-code", "--shape", "--originals", dir, "--originals-max", "lots"},
		{"claude-code", "--shape", "--originals", dir, "--output-cap", "-1"},
	} {
		if code, d, _ := hookMain(t, req, args...); code != 0 || d == nil || d.Decision != "deny" {
			t.Errorf("%v: code %d, answer %+v; want a deny", args, code, d)
		}
	}
}
