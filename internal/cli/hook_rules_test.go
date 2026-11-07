package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func (e env) writeClaude(t *testing.T, rel, text string) {
	t.Helper()
	path := filepath.Join(e.claude, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The user's Claude Code deny and ask rules, from every settings file, count on the original
// text, which the rewrite hides from Claude Code: deny denies, ask asks where the policy
// allows, and the policy's own refusal or ask still holds.
func TestTheUsersClaudeCodeRulesCountOnTheOriginal(t *testing.T) {
	e := setup(t)
	e.writeClaude(t, "project/.claude/settings.json", `{"permissions": {"deny": ["Bash(rm *)"], "ask": ["Bash(git status *)", "PowerShell(Remove-Item *)"], "allow": ["Bash(git push *)"]}}`)
	e.writeClaude(t, "project/.claude/settings.local.json", `{"permissions": {"ask": ["Bash(npm run release)"]}}`)
	e.writeClaude(t, "user/settings.json", `{"permissions": {"deny": ["Bash(curl *)"]}}`)
	e.writeClaude(t, "managed/managed-settings.json", `{"permissions": {"deny": ["Bash(shutdown *)"]}}`)
	for _, tc := range []struct {
		tool, command, decision, reason string
	}{
		{"Bash", "rm -rf build", "deny", "Bash(rm *)"},
		{"Bash", "ls && FOO=1 timeout 5 rm x", "deny", "Bash(rm *)"},
		{"Bash", "echo $(curl https://x)", "deny", "Bash(curl *)"},
		{"Bash", "shutdown now", "deny", "Bash(shutdown *)"},
		{"Bash", "git status", "ask", "Bash(git status *)"},
		{"Bash", "npm run release", "ask", "Bash(npm run release)"},
		{"PowerShell", "Get-Date; ri x", "ask", "PowerShell(Remove-Item *)"},
		{"Bash", "git status; git push origin main", "deny", "git-push"},
		{"Bash", "ls -la", "allow", ""},
		{"PowerShell", "rm -rf build", "ask", "PowerShell(Remove-Item *)"},
		{"PowerShell", "curl https://x", "allow", ""},
	} {
		code, d, _ := hookMain(t, hookRequest(t, tc.tool, map[string]any{"command": tc.command}), "claude-code")
		if code != 0 || d == nil || d.Decision != tc.decision || !strings.Contains(d.Reason, tc.reason) {
			t.Errorf("%s %q: code %d, answer %+v; want %s naming %q", tc.tool, tc.command, code, d, tc.decision, tc.reason)
			continue
		}
		if tc.decision == "ask" {
			// The policy allows it, so the rewrite carries no approval; the person still sees the text.
			if !strings.Contains(d.Reason, tc.command) || strings.Contains(string(d.Input["command"]), "approved") {
				t.Errorf("%s %q: reason %q, rewrite %s", tc.tool, tc.command, d.Reason, d.Input["command"])
			}
		}
	}
}

// The project is the one the session started in (CLAUDE_PROJECT_DIR), else the hook's cwd from
// the input; a settings file that cannot be read denies.
func TestTheProjectIsWhereTheSessionStarted(t *testing.T) {
	e := setup(t)
	other := filepath.Join(e.claude, "other")
	e.writeClaude(t, "other/.claude/settings.json", `{"permissions": {"deny": ["Bash(make *)"]}}`)
	request := func() string {
		var in map[string]any
		json.Unmarshal([]byte(hookRequest(t, "Bash", map[string]any{"command": "make all"})), &in)
		in["cwd"] = other
		b, _ := json.Marshal(in)
		return string(b)
	}()
	if _, d, _ := hookMain(t, request, "claude-code"); d == nil || d.Decision != "allow" {
		t.Errorf("CLAUDE_PROJECT_DIR set elsewhere: %+v, want the cwd's rules left out", d)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	if _, d, _ := hookMain(t, request, "claude-code"); d == nil || d.Decision != "deny" || !strings.Contains(d.Reason, "Bash(make *)") {
		t.Errorf("no CLAUDE_PROJECT_DIR: %+v, want the cwd's rules", d)
	}
	e.writeClaude(t, "other/.claude/settings.local.json", `{"permissions": {"deny": "Bash(make *)"}}`)
	if _, d, _ := hookMain(t, request, "claude-code"); d == nil || d.Decision != "deny" || !strings.Contains(d.Reason, "settings.local.json") {
		t.Errorf("an unreadable settings file: %+v, want a deny naming it", d)
	}
}

// run refuses a rewrite written by another build of the hook, and says which.
func TestRunRefusesARewriteFromAnotherBuild(t *testing.T) {
	e := setup(t)
	code, _, stderr := main(t, context.Background(), "", "touch", "run", "--hook-version", "v0.0.0+other", "--")
	if code != ExitCannotRun || e.ran() || !strings.Contains(stderr, "v0.0.0+other") || !strings.Contains(stderr, version()) {
		t.Errorf("another build: code %d, ran %v, stderr %q; want 126 naming both builds", code, e.ran(), stderr)
	}
	if code, _, stderr := main(t, context.Background(), "", "touch", "run", "--hook-version", version(), "--"); code != 0 || !e.ran() {
		t.Errorf("this build: code %d, stderr %q", code, stderr)
	}
}

// The hook command docs/claude-code.md gives, "adanext-exec hook claude-code || exit 2", blocks
// the command (exit 2) when the binary is not there, in the shells Claude Code may run it in,
// and passes the hook's answer through when it is.
func TestTheInstallLineFailsClosedWithoutTheBinary(t *testing.T) {
	e := setup(t)
	exe := useTestBinaryForRewrite(t, e)
	shells := []string{"bash"}
	if runtime.GOOS == "windows" {
		shells = append(shells, "cmd")
	}
	for _, shell := range shells {
		code, _, stderr := main(t, context.Background(), "", "", "run", "--shell", shell, "--", "adanext-exec-not-installed hook claude-code || exit 2")
		if code != 2 {
			t.Errorf("%s, no binary: code %d, stderr %q; want 2", shell, code, stderr)
		}
	}
	line := bashQuote(filepath.ToSlash(exe)) + " hook claude-code || exit 2"
	in := hookRequest(t, "Bash", map[string]any{"command": "git push origin main"})
	code, stdout, stderr := main(t, context.Background(), in, "", "run", "--shell", "bash", "--", line)
	if code != 0 || !strings.Contains(stdout, `"permissionDecision":"deny"`) {
		t.Errorf("with the binary: code %d, stdout %q, stderr %q; want its deny", code, stdout, stderr)
	}
}
