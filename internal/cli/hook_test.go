package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cristiangirlea/adanext-exec/broker"
)

const nl = string(rune(10))

// hookRequest is a PreToolUse input as Claude Code writes it, for a tool and its input.
func hookRequest(t *testing.T, tool string, input map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id": "5f1c-session", "transcript_path": "/x/t.jsonl", "cwd": "/x",
		"permission_mode": "default", "hook_event_name": "PreToolUse",
		"tool_name": tool, "tool_input": input, "tool_use_id": "toolu_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hookMain runs the hook and decodes its answer, which must be one JSON line, or none.
func hookMain(t *testing.T, stdin string, args ...string) (int, *hookDecision, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(context.Background(), append([]string{"hook"}, args...), strings.NewReader(stdin), &out, &errb)
	if out.Len() == 0 {
		return code, nil, errb.String()
	}
	raw := out.String()
	var answer struct {
		Specific *hookDecision `json:"hookSpecificOutput"`
	}
	dec := json.NewDecoder(&out)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&answer); err != nil || answer.Specific == nil || strings.Count(raw, nl) != 1 || !strings.HasSuffix(raw, nl) {
		t.Fatalf("answer %q (%v): want one hookSpecificOutput line", raw, err)
	}
	if answer.Specific.Event != "PreToolUse" {
		t.Errorf("hookEventName %q, want PreToolUse", answer.Specific.Event)
	}
	return code, answer.Specific, errb.String()
}

// The rewrite runs a copy of the test binary from a directory whose name needs quoting in both
// shells, as the binary itself (helper mode "main").
func useTestBinaryForRewrite(t *testing.T, e env) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "it's a ’dir’")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, filepath.Base(os.Args[0]))
	src, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, src, 0o700); err != nil {
		t.Fatal(err)
	}
	old := hookExecutable
	hookExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { hookExecutable = old })
	t.Setenv(helperEnv, "main")
	t.Setenv("ADANEXT_CLI_CONFIG", e.config)
	t.Setenv("ADANEXT_CLI_AUDIT", e.audit)
	t.Setenv("ADANEXT_CLI_CLAUDE", e.claude)
	return exe
}

// powerShellsHere are the shells the PowerShell tool can use here: pwsh when it is in PATH, and
// Windows PowerShell on Windows.
func powerShellsHere() []broker.Shell {
	var shells []broker.Shell
	if _, err := exec.LookPath("pwsh"); err == nil {
		shells = append(shells, broker.ShellPwsh)
	}
	if runtime.GOOS == "windows" {
		ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(ps); err == nil {
			shells = append(shells, broker.ShellPowerShell)
		}
	}
	return shells
}

// For each decision and each tool the hook answers as Claude Code reads it, and the rewrite,
// run through the tool's own shell, runs the exact original text: an allow and an ask, under a
// rule or the policy's default (whose rewrite carries the approval), run it; a refusal denies
// naming its rule.
func TestTheHookRewriteRunsTheExactText(t *testing.T) {
	e := setup(t)
	useTestBinaryForRewrite(t, e)
	named := e.writePolicy(t, "hook.json", `{"version": 1, "rules": [{"name": "say-hi", "match": "say-hi", "decision": "ask"}]}`)
	byDefault := e.writePolicy(t, "default-ask.json", `{"version": 1, "default": "ask"}`)
	tab, backslash := string(rune(9)), string(rune(92))
	type tool struct {
		name                 string
		shell                broker.Shell
		script, mark, output string
	}
	tools := []tool{{
		"Bash", broker.ShellBash,
		`x='a  b'; printf '[%s]' "$x" $'c\td' "$(echo $((2+3)))"; echo " \"q\" \\ done" | tr d D`,
		"echo say-hi >/dev/null; ",
		"[a  b][c" + tab + "d][5] \"q\" " + backslash + " Done" + nl,
	}}
	for _, sh := range powerShellsHere() {
		tools = append(tools, tool{
			"PowerShell", sh,
			`$x = 'a;b|c'; Write-Output "$x $(1+2) ""q"" 'r' ` + "`$y" + `"; 1..3 | ForEach-Object { $_ * 2 }`,
			"$null = 'say-hi'; ",
			"a;b|c 3 \"q\" 'r' $y" + nl + "2" + nl + "4" + nl + "6" + nl,
		})
	}
	if len(tools) == 1 {
		t.Log("no PowerShell here; the PowerShell tool's rewrite is not run")
	}
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	for _, tl := range tools {
		// The PowerShell tool takes pwsh when it is in PATH: this one is chosen.
		lookPath = func(string) (string, error) {
			if tl.shell == broker.ShellPwsh {
				return "pwsh", nil
			}
			return "", exec.ErrNotFound
		}
		for _, tc := range []struct {
			name, policy, mark, rule string
		}{{"allow", named, "", ""}, {"ask", named, tl.mark, "say-hi"}, {"default ask", byDefault, "", ""}} {
			script := tc.mark + tl.script
			decision := strings.Fields(tc.name)[len(strings.Fields(tc.name))-1]
			in := map[string]any{"command": script, "description": "d", "timeout": 5000, "run_in_background": false}
			code, d, stderr := hookMain(t, hookRequest(t, tl.name, in), "claude-code", "--policy", tc.policy)
			if code != 0 || d == nil || d.Decision != decision {
				t.Fatalf("%s %s %s: code %d, answer %+v, stderr %q", tl.name, tl.shell, tc.name, code, d, stderr)
			}
			if tc.rule != "" && !strings.Contains(d.Reason, tc.rule) || decision == "ask" && !strings.Contains(d.Reason, script) {
				t.Errorf("%s %s: reason %q, want the rule and the command", tl.name, tc.name, d.Reason)
			}
			if string(d.Input["description"]) != `"d"` || string(d.Input["timeout"]) != "5000" || string(d.Input["run_in_background"]) != "false" || len(d.Input) != 4 {
				t.Errorf("%s: updatedInput %v, want the other fields kept", tl.name, d.Input)
			}
			var rewrite string
			if err := json.Unmarshal(d.Input["command"], &rewrite); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rewrite, "'--shell' '"+string(tl.shell)+"'") || tc.mark != "" && strings.Contains(rewrite, tc.mark) || strings.Contains(rewrite, "Write-Output") || strings.Contains(rewrite, "printf") {
				t.Errorf("%s: the rewrite %q runs another shell, or carries the text where a shell reads it", tl.name, rewrite)
			}
			// Claude Code's tool runs the rewrite through its shell; here, run does.
			code, stdout, stderr := main(t, context.Background(), "", "", "run", "--shell", string(tl.shell), "--", rewrite)
			stdout = strings.ReplaceAll(stdout, string(rune(13))+nl, nl)
			if code != 0 || stdout != tl.output {
				t.Errorf("%s %s %s: the rewrite exits %d with %q, stderr %q; want 0 and %q", tl.name, tl.shell, tc.name, code, stdout, stderr, tl.output)
			}
		}
		code, d, _ := hookMain(t, hookRequest(t, tl.name, map[string]any{"command": "git push origin main"}), "claude-code")
		if code != 0 || d == nil || d.Decision != "deny" || !strings.Contains(d.Reason, "git-push") || d.Input != nil {
			t.Errorf("%s: a refused command: code %d, answer %+v; want a deny naming git-push", tl.name, code, d)
		}
	}
}

// The rewrite carries the policy as an absolute path, the profile, the agent and the session,
// and an ask's approval; nothing else.
func TestTheRewriteCarriesThePolicyAndTheCaller(t *testing.T) {
	e := setup(t)
	e.writePolicy(t, "profiles/ci.json", `{"version": 1, "rules": [{"name": "say-hi", "match": "say-hi", "decision": "ask"}]}`)
	old := hookExecutable
	hookExecutable = func() (string, error) { return "/bin/adanext-exec", nil }
	t.Cleanup(func() { hookExecutable = old })
	_, d, _ := hookMain(t, hookRequest(t, "Bash", map[string]any{"command": "say-hi"}), "claude-code", "--profile", "ci")
	var rewrite string
	if d == nil || json.Unmarshal(d.Input["command"], &rewrite) != nil {
		t.Fatalf("answer %+v", d)
	}
	want := `'/bin/adanext-exec' 'run' '--hook-version' '` + version() + `' '--shell' 'bash' '--profile' 'ci' '--approved=say-hi' '--agent' 'claude-code' '--session' '5f1c-session' '--script-b64' 'c2F5LWhp'`
	if rewrite != want {
		t.Errorf("rewrite %s\nwant    %s", rewrite, want)
	}

	t.Chdir(t.TempDir())
	if err := os.WriteFile("p.json", []byte(`{"version": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, d, _ = hookMain(t, hookRequest(t, "Bash", map[string]any{"command": "ls"}), "claude-code", "--policy", "p.json")
	abs, _ := filepath.Abs("p.json")
	if d == nil || !strings.Contains(string(d.Input["command"]), strings.ReplaceAll(abs, `\`, `\\`)) {
		t.Errorf("answer %+v: want the policy's absolute path %s", d, abs)
	}
}

// Every other tool is left to Claude Code: no answer, exit 0.
func TestTheHookLeavesOtherToolsToClaudeCode(t *testing.T) {
	setup(t)
	for _, tool := range []string{"Read", "Edit", "WebFetch", "mcp__x__y", "bash"} {
		if code, d, stderr := hookMain(t, hookRequest(t, tool, map[string]any{"command": "git push"}), "claude-code"); code != 0 || d != nil || stderr != "" {
			t.Errorf("%s: code %d, answer %+v, stderr %q; want nothing", tool, code, d, stderr)
		}
	}
}

// An input the hook cannot read, a policy it cannot use, a misconfigured hook, an elevated run
// and a command that passes the rewrite's own flags each deny, with the reason.
func TestTheHookDeniesWhatItCannotJudge(t *testing.T) {
	e := setup(t)
	bad := e.writePolicy(t, "bad.json", `{"version": 2}`)
	bash := func(cmd any) string { return hookRequest(t, "Bash", map[string]any{"command": cmd}) }
	for _, tc := range []struct {
		name, stdin string
		args        []string
		reason      string
	}{
		{"empty input", "", nil, "not valid JSON"},
		{"not JSON", "{", nil, "not valid JSON"},
		{"data after it", bash("ls") + " {}", nil, "data after it"},
		{"another event", strings.Replace(bash("ls"), "PreToolUse", "PostToolUse", 1), nil, "PostToolUse"},
		{"no tool", `{"hook_event_name": "PreToolUse", "tool_input": {}}`, nil, "names no tool"},
		{"input not an object", `{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": "ls"}`, nil, "not a JSON object"},
		{"input null", `{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": null}`, nil, "not a JSON object"},
		{"no command", hookRequest(t, "Bash", map[string]any{"description": "x"}), nil, "no command string"},
		{"command not a string", bash([]string{"ls"}), nil, "no command string"},
		{"empty command", bash(""), nil, "no command string"},
		{"too large", bash(strings.Repeat("x", maxRequestSize)), nil, "larger than"},
		{"an unusable policy", bash("ls"), []string{"--policy", bad}, "version"},
		{"a missing profile", bash("ls"), []string{"--profile", "nope"}, "nope"},
		{"both policy and profile", bash("ls"), []string{"--policy", bad, "--profile", "x"}, "not both"},
		{"an unknown flag", bash("ls"), []string{"--nope"}, "the hook's arguments"},
		{"an argument", bash("ls"), []string{"extra"}, "the hook's arguments"},
		{"self-approval", bash("adanext-exec run --approved git-push -- git push"), nil, "-approved"},
		{"a hidden script", bash("x run --shell bash -script-b64 Z2l0IHB1c2g"), nil, "-script-b64"},
		{"a session id with quotes", strings.Replace(bash("ls"), "5f1c-session", `a \"b --timeout 1ns --session \"d`, 1), nil, "session id"},
	} {
		args := append([]string{"claude-code"}, tc.args...)
		code, d, _ := hookMain(t, tc.stdin, args...)
		if code != 0 || d == nil || d.Decision != "deny" || !strings.Contains(d.Reason, tc.reason) || d.Input != nil {
			t.Errorf("%s: code %d, answer %+v; want a deny saying %q", tc.name, code, d, tc.reason)
		}
	}
	for _, args := range [][]string{nil, {"codex"}} {
		if code, d, _ := hookMain(t, bash("ls"), args...); code != 0 || d == nil || d.Decision != "deny" {
			t.Errorf("hook %q: code %d, answer %+v; want a deny", args, code, d)
		}
	}
	isElevated = func() bool { return true }
	defer func() { isElevated = func() bool { return false } }()
	if code, d, _ := hookMain(t, bash("ls"), "claude-code"); code != 0 || d == nil || d.Decision != "deny" || !strings.Contains(d.Reason, errElevated) {
		t.Errorf("elevated: code %d, answer %+v; want a deny", code, d)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("gone") }

// Input that cannot be read, and a binary that cannot be found for the rewrite, deny.
func TestTheHookDeniesWhenItCannotReadOrRewrite(t *testing.T) {
	setup(t)
	var out, errb bytes.Buffer
	if code := Main(context.Background(), []string{"hook", "claude-code"}, brokenReader{}, &out, &errb); code != 0 || !strings.Contains(out.String(), `"deny"`) || !strings.Contains(out.String(), "gone") {
		t.Errorf("unreadable input: code %d, answer %q", code, out.String())
	}
	old := hookExecutable
	t.Cleanup(func() { hookExecutable = old })
	hookExecutable = func() (string, error) { return "", errors.New("no path") }
	code, d, _ := hookMain(t, hookRequest(t, "Bash", map[string]any{"command": "ls"}), "claude-code")
	if code != 0 || d == nil || d.Decision != "deny" || !strings.Contains(d.Reason, "no path") || d.Input != nil {
		t.Errorf("no binary: code %d, answer %+v; want a deny", code, d)
	}
}

// An empty --approved approves an ask by the policy's default, which names no rule, and only that.
func TestAnEmptyApprovalCountsOnlyForTheDefaultsAsk(t *testing.T) {
	e := setup(t)
	byDefault := e.writePolicy(t, "default-ask.json", `{"version": 1, "default": "ask"}`)
	named := e.writePolicy(t, "ask.json", askHelper)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"run", "--policy", byDefault, "--approved=", "--"}, 0},
		{[]string{"run", "--policy", byDefault, "--approved", "", "--"}, 0},
		{[]string{"run", "--policy", byDefault, "--approved=helper", "--"}, ExitRefused},
		{[]string{"run", "--policy", byDefault, "--"}, ExitRefused},
		{[]string{"run", "--policy", named, "--approved=", "--"}, ExitRefused},
	} {
		os.Remove(e.marker)
		code, _, stderr := main(t, context.Background(), "", "touch", tc.args...)
		if code != tc.code || e.ran() != (tc.code == 0) {
			t.Errorf("%q: code %d, ran %v, stderr %q; want %d", tc.args, code, e.ran(), stderr, tc.code)
		}
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

// An answer that cannot be written blocks the command: exit 2, the reason on stderr.
func TestAnAnswerThatCannotBeWrittenExits2(t *testing.T) {
	setup(t)
	var errb bytes.Buffer
	in := hookRequest(t, "Bash", map[string]any{"command": "git push"})
	if code := Main(context.Background(), []string{"hook", "claude-code"}, strings.NewReader(in), brokenWriter{}, &errb); code != 2 || !strings.Contains(errb.String(), "git-push") {
		t.Errorf("code %d, stderr %q; want 2 and the reason", code, errb.String())
	}
}

// The PowerShell tool's shell is PowerShell 7 when it is in PATH; else Windows PowerShell, on
// Windows only.
func TestThePowerShellToolsShell(t *testing.T) {
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	want := broker.ShellPwsh
	if runtime.GOOS == "windows" {
		want = broker.ShellPowerShell
	}
	if got := powerShell(); got != want {
		t.Errorf("without pwsh: %s, want %s", got, want)
	}
	lookPath = func(string) (string, error) { return "pwsh", nil }
	if got := powerShell(); got != broker.ShellPwsh {
		t.Errorf("with pwsh: %s, want pwsh", got)
	}
}

// run takes the script as URL-safe base64 without padding, and only with --shell and nothing
// after it.
func TestRunScriptB64(t *testing.T) {
	setup(t)
	// "printf 'a  b'" and "echo a  b".
	shell, b64 := "sh", "cHJpbnRmICdhICBiJw"
	if runtime.GOOS == "windows" {
		shell, b64 = "cmd", "ZWNobyBhICBi"
	}
	code, stdout, stderr := main(t, context.Background(), "", "", "run", "--shell", shell, "--script-b64", b64)
	if code != 0 || !strings.Contains(stdout, "a  b") {
		t.Errorf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, args := range [][]string{
		{"run", "--script-b64", b64, "--", "x"},
		{"run", "--shell", shell, "--script-b64", b64, "--", "x"},
		{"run", "--shell", shell, "--script-b64", b64 + "=="},
		{"run", "--shell", shell, "--script-b64", "a+b/"},
		{"run", "--shell", shell, "--script-b64", "!"},
	} {
		if code, _, _ := main(t, context.Background(), "", "", args...); code != ExitUsage {
			t.Errorf("%q: code %d, want %d", args, code, ExitUsage)
		}
	}
}
