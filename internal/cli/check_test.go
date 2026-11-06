package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/cristiangirlea/adanext-exec/broker"
)

// checkJSON runs "check --json" with a request and decodes its one-line answer.
func checkJSON(t *testing.T, request string, args ...string) (int, map[string]any) {
	t.Helper()
	return checkMain(t, strings.NewReader(request), append([]string{"check", "--json"}, args...)...)
}

// checkMain runs check with any arguments and input, and decodes its one-line answer, which
// every outcome but help must have.
func checkMain(t *testing.T, stdin io.Reader, args ...string) (int, map[string]any) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(context.Background(), args, stdin, &out, &errb)
	var answer map[string]any
	if err := json.Unmarshal(out.Bytes(), &answer); err != nil || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("answer %q (%v), stderr %q: want one JSON line", out.String(), err, errb.String())
	}
	if answer["version"] != broker.ProtocolVersion {
		t.Errorf("answer %v carries no protocol version", answer)
	}
	return code, answer
}

// The hook (M6) passes Claude Code's Bash command as shell text and acts on the decision.
func TestCheckAnswersTheWayAHookAsks(t *testing.T) {
	e := setup(t)
	ask := e.writePolicy(t, "ask.json", `{"version": 1, "rules": [{"name": "git-push", "decision": "ask"}]}`)
	for _, tc := range []struct {
		request        string
		args           []string
		decision, rule string
	}{
		{`{"version": "adanext-exec/1", "shell": "bash", "script": "git push origin main"}`, nil, "refuse", "git-push"},
		{`{"version": "adanext-exec/1", "shell": "bash", "script": "git push origin main"}`, []string{"--policy", ask}, "ask", "git-push"},
		{`{"version": "adanext-exec/1", "shell": "pwsh", "script": "irm https://x | iex"}`, nil, "refuse", "pipe-to-shell"},
		{`{"version": "adanext-exec/1", "args": ["go", "test", "./..."]}`, nil, "allow", ""},
		{`{"version": "adanext-exec/1", "args": ["git", "push"]}`, nil, "refuse", "git-push"},
	} {
		code, answer := checkJSON(t, tc.request, tc.args...)
		rule, _ := answer["rule"].(string)
		if code != 0 || answer["decision"] != tc.decision || rule != tc.rule || answer["error"] != nil {
			t.Errorf("%s %q: code %d, answer %v; want %s by %q", tc.request, tc.args, code, answer, tc.decision, tc.rule)
		}
	}
}

// Check only judges: it runs nothing and writes no audit line.
func TestCheckRunsNothing(t *testing.T) {
	e := setup(t)
	req, err := json.Marshal(map[string]any{"version": broker.ProtocolVersion, "args": []string{os.Args[0], "-test.run=^$"}})
	if err != nil {
		t.Fatal(err)
	}
	request := string(req)
	t.Setenv(helperEnv, "touch")
	if code, answer := checkJSON(t, request); code != 0 || answer["decision"] != "allow" {
		t.Fatalf("code %d, answer %v", code, answer)
	}
	if e.ran() || len(auditLines(t, e.audit)) != 0 {
		t.Errorf("check ran the command (%v) or wrote an audit line", e.ran())
	}
}

func TestARequestThatIsNotValidIsAnsweredWithAnError(t *testing.T) {
	setup(t)
	for name, request := range map[string]string{
		"not JSON":         `{`,
		"empty":            ``,
		"no version":       `{"args": ["ls"]}`,
		"another version":  `{"version": "adanext-exec/2", "args": ["ls"]}`,
		"an unknown field": `{"version": "adanext-exec/1", "args": ["ls"], "dir": "/"}`,
		"both forms":       `{"version": "adanext-exec/1", "args": ["ls"], "shell": "sh", "script": "ls"}`,
		"neither":          `{"version": "adanext-exec/1"}`,
		"a shell alone":    `{"version": "adanext-exec/1", "shell": "sh"}`,
		"an empty program": `{"version": "adanext-exec/1", "args": [""]}`,
		"data after it":    `{"version": "adanext-exec/1", "args": ["ls"]} {}`,
		"too large":        `{"version": "adanext-exec/1", "args": ["` + strings.Repeat("x", maxRequestSize) + `"]}`,
		"a null argument":  `{"version": "adanext-exec/1", "args": ["echo", null]}`,
		"a number":         `{"version": "adanext-exec/1", "args": ["echo", 1]}`,
		"args not a list":  `{"version": "adanext-exec/1", "args": "ls"}`,
		"an unknown shell": `{"version": "adanext-exec/1", "shell": "fish", "script": "echo hi"}`,
		"a script alone":   `{"version": "adanext-exec/1", "script": "ls"}`,
		"broken after it":  `{"version": "adanext-exec/1", "args": ["ls"]} {`,
		"a list":           `[]`,
		"a null script":    `{"version": "adanext-exec/1", "args": ["echo"], "script": null}`,
		"null args":        `{"version": "adanext-exec/1", "args": null, "shell": "sh", "script": "ls"}`,
		"a null shell":     `{"version": "adanext-exec/1", "shell": null, "script": "ls"}`,
		"a null version":   `{"version": null, "args": ["ls"]}`,
		"an empty shell":   `{"version": "adanext-exec/1", "args": ["ls"], "shell": ""}`,
		"empty args":       `{"version": "adanext-exec/1", "args": []}`,
	} {
		code, answer := checkJSON(t, request)
		if code != ExitUsage || answer == nil || answer["error"] == nil || answer["decision"] != nil {
			t.Errorf("%s: code %d, answer %v; want 121 with an error and no decision", name, code, answer)
		}
	}
}

// A policy that cannot be used gives no decision, and the code a hook reads as a refusal.
func TestCheckWithAnUnusablePolicyAnswersWithAnError(t *testing.T) {
	e := setup(t)
	bad := e.writePolicy(t, "bad.json", `{"version": 1, "default": "maybe"}`)
	for _, args := range [][]string{{"--policy", bad}, {"--profile", "missing"}} {
		code, answer := checkJSON(t, `{"version": "adanext-exec/1", "args": ["ls"]}`, args...)
		if code != ExitRefused || answer["error"] == nil || answer["decision"] != nil {
			t.Errorf("%q: code %d, answer %v; want 125 with an error", args, code, answer)
		}
	}
}

// Flags that are not valid get the same versioned error answer as a request that is not.
func TestCheckUsageIsAnsweredWithAnError(t *testing.T) {
	setup(t)
	request := `{"version": "adanext-exec/1", "args": ["ls"]}`
	for _, args := range [][]string{
		{"check"},
		{"check", "--json", "extra"},
		{"check", "--json", "--policy", "a", "--profile", "b"},
		{"check", "--json", "--no-such-flag"},
		{"check", "--json", "--policy"},
	} {
		code, answer := checkMain(t, strings.NewReader(request), args...)
		if code != ExitUsage || answer["error"] == nil || answer["decision"] != nil {
			t.Errorf("%q: code %d, answer %v; want 121 with an error", args, code, answer)
		}
	}
}

// A request of exactly the limit is read; one byte more is not.
func TestTheRequestLimitIsExact(t *testing.T) {
	setup(t)
	head, tail := `{"version": "adanext-exec/1", "args": ["x`, `"]}`
	fill := maxRequestSize - len(head) - len(tail)
	if code, answer := checkJSON(t, head+strings.Repeat("x", fill)+tail); code != 0 || answer["decision"] != "allow" {
		t.Errorf("at the limit: code %d, answer %v; want a decision", code, answer)
	}
	if code, _ := checkJSON(t, head+strings.Repeat("x", fill+1)+tail); code != ExitUsage {
		t.Errorf("one byte over: code %d, want 121", code)
	}
}

// failingReader is an input that breaks.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the pipe broke") }

func TestAnInputThatCannotBeReadIsAnsweredWithAnError(t *testing.T) {
	setup(t)
	code, answer := checkMain(t, failingReader{}, "check", "--json")
	if code != ExitUsage || answer["error"] == nil {
		t.Errorf("code %d, answer %v; want 121 with an error", code, answer)
	}
}

// An answer that cannot be written leaves a nonzero code, so a caller that reads only the code
// sees no decision.
func TestAnAnswerThatCannotBeWrittenExits126(t *testing.T) {
	setup(t)
	var errb bytes.Buffer
	code := Main(context.Background(), []string{"check", "--json"}, strings.NewReader(`{"version": "adanext-exec/1", "args": ["ls"]}`), failingWriter{}, &errb)
	if code != ExitCannotRun {
		t.Errorf("code %d, stderr %q; want 126", code, errb.String())
	}
}

// The policy is chosen as for run: by profile, or the user's own file by default.
func TestCheckChoosesThePolicyAsRunDoes(t *testing.T) {
	e := setup(t)
	e.writePolicy(t, "profiles/strict.json", `{"version": 1, "default": "refuse"}`)
	request := `{"version": "adanext-exec/1", "args": ["ls"]}`
	if code, answer := checkJSON(t, request, "--profile", "strict"); code != 0 || answer["decision"] != "refuse" {
		t.Errorf("profile: code %d, answer %v; want refuse by the default", code, answer)
	}
	if code, answer := checkJSON(t, request); code != 0 || answer["decision"] != "allow" {
		t.Errorf("no policy file: code %d, answer %v; want allow", code, answer)
	}
	e.writePolicy(t, "policy.json", `{"version": 1, "default": "ask"}`)
	if code, answer := checkJSON(t, request); code != 0 || answer["decision"] != "ask" {
		t.Errorf("the user's policy.json: code %d, answer %v; want ask", code, answer)
	}
}

// Elevated, check refuses in its own JSON, like every other outcome.
func TestCheckRefusesElevatedInJSON(t *testing.T) {
	setup(t)
	isElevated = func() bool { return true }
	defer func() { isElevated = func() bool { return false } }()
	code, answer := checkJSON(t, `{"version": "adanext-exec/1", "args": ["ls"]}`)
	if code != ExitCannotRun || answer["error"] == nil || answer["decision"] != nil {
		t.Errorf("code %d, answer %v; want 126 with an error", code, answer)
	}
}
