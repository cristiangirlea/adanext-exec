package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperEnv = "ADANEXT_CLI_HELPER"

// TestMain lets the tests run as root (the Linux container) or elevated (a CI runner): the
// refusal to run elevated has its own test.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helperMain(mode)
		return
	}
	isElevated = func() bool { return false }
	os.Exit(m.Run())
}

// helperMain is the command the tests run: the test binary again, in one of these modes.
func helperMain(mode string) {
	name, arg, _ := strings.Cut(mode, ":")
	switch name {
	case "exit":
		n, _ := strconv.Atoi(arg)
		os.Exit(n)
	case "echo":
		b, _ := readAll(os.Stdin)
		os.Stdout.Write(b)
	case "touch":
		os.WriteFile(os.Getenv("ADANEXT_CLI_MARKER"), nil, 0o600)
	case "sleep":
		time.Sleep(time.Minute)
	case "print":
		n, _ := strconv.Atoi(arg)
		os.Stdout.Write(bytes.Repeat([]byte("x"), n))
	case "signal":
		helperSignal()
	case "args":
		// Its own arguments, after the test flag, one per line, and a line on standard error.
		for _, a := range os.Args[2:] {
			fmt.Fprintf(os.Stdout, "[%s]\n", a)
		}
		fmt.Fprint(os.Stderr, "to stderr")
	}
	os.Exit(0)
}

func readAll(f *os.File) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(f)
	return b.Bytes(), err
}

// env is where a test keeps the binary's configuration and audit, away from the user's.
type env struct {
	config, audit, marker string
}

func setup(t *testing.T) env {
	t.Helper()
	e := env{config: t.TempDir(), audit: t.TempDir(), marker: filepath.Join(t.TempDir(), "ran")}
	oldConfig, oldAudit := configDir, auditDir
	configDir = func() (string, error) { return e.config, nil }
	auditDir = func() (string, error) { return e.audit, nil }
	t.Cleanup(func() { configDir, auditDir = oldConfig, oldAudit })
	t.Setenv("ADANEXT_CLI_MARKER", e.marker)
	return e
}

func (e env) ran() bool {
	_, err := os.Stat(e.marker)
	return err == nil
}

// writePolicy puts a policy at a path under the test's configuration directory.
func (e env) writePolicy(t *testing.T, rel, text string) string {
	t.Helper()
	path := filepath.Join(e.config, "adanext-exec", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// main runs the binary in-process, with the helper in the given mode as the command when the
// arguments end with "--".
func main(t *testing.T, ctx context.Context, stdin string, mode string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	if mode != "" {
		t.Setenv(helperEnv, mode)
		args = append(args, os.Args[0], "-test.run=^$")
	}
	var out, errb bytes.Buffer
	code = Main(ctx, args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestTheCommandsExitCodeAndStreamsPassThrough(t *testing.T) {
	setup(t)
	for _, n := range []int{0, 3, 120} {
		if code, _, stderr := main(t, context.Background(), "", fmt.Sprintf("exit:%d", n), "run", "--"); code != n {
			t.Errorf("exit %d: code %d, stderr %s", n, code, stderr)
		}
	}
	if code, stdout, _ := main(t, context.Background(), "hello", "echo", "run", "--"); code != 0 || stdout != "hello" {
		t.Errorf("echo: code %d, stdout %q", code, stdout)
	}
}

const refuseHelper = `{"version": 1, "rules": [{"name": "helper", "match": "test\\.run", "decision": "refuse"}]}`
const askHelper = `{"version": 1, "rules": [{"name": "helper", "match": "test\\.run", "decision": "ask"}]}`

func TestARefusalExits125AndRunsNothing(t *testing.T) {
	e := setup(t)
	path := e.writePolicy(t, "policy.json", refuseHelper)
	for _, args := range [][]string{{"run", "--"}, {"run", "--policy", path, "--"}} {
		code, _, stderr := main(t, context.Background(), "", "touch", args...)
		if code != ExitRefused || e.ran() || !strings.Contains(stderr, "helper") {
			t.Errorf("%q: code %d, ran %v, stderr %q; want 125, nothing run, the rule named", args, code, e.ran(), stderr)
		}
	}
}

// --approved confirms an ask under its own rule only, and never turns a refusal into a run.
func TestApprovedCountsOnlyForAnAskUnderItsRule(t *testing.T) {
	e := setup(t)
	ask := e.writePolicy(t, "ask.json", askHelper)
	refuse := e.writePolicy(t, "refuse.json", refuseHelper)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"run", "--policy", ask, "--"}, ExitRefused},
		{[]string{"run", "--policy", ask, "--approved", "other", "--"}, ExitRefused},
		{[]string{"run", "--policy", refuse, "--approved", "helper", "--"}, ExitRefused},
		{[]string{"run", "--policy", ask, "--approved", "helper", "--"}, 0},
	} {
		os.Remove(e.marker)
		code, _, stderr := main(t, context.Background(), "", "touch", tc.args...)
		if code != tc.code || e.ran() != (tc.code == 0) {
			t.Errorf("%q: code %d, ran %v, stderr %q; want %d", tc.args, code, e.ran(), stderr, tc.code)
		}
	}
}

func TestThePolicyIsChosenByFlagProfileOrTheUsersFile(t *testing.T) {
	e := setup(t)
	// No policy file: the built-in rules, which let the helper run.
	if code, _, stderr := main(t, context.Background(), "", "touch", "run", "--"); code != 0 || !e.ran() {
		t.Fatalf("no policy: code %d, stderr %s", code, stderr)
	}
	e.writePolicy(t, "profiles/strict.json", `{"version": 1, "default": "refuse"}`)
	e.writePolicy(t, "profiles/broken.json", `{"version": 1, "default": "maybe"}`)
	bad := e.writePolicy(t, "bad.json", `{`)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"run", "--profile", "strict", "--"}, ExitRefused},
		{[]string{"run", "--profile", "broken", "--"}, ExitRefused},
		{[]string{"run", "--profile", "missing", "--"}, ExitRefused},
		{[]string{"run", "--profile", "../strict", "--"}, ExitUsage},
		{[]string{"run", "--policy", bad, "--"}, ExitRefused},
		{[]string{"run", "--policy", filepath.Join(e.config, "none.json"), "--"}, ExitRefused},
		{[]string{"run", "--policy", bad, "--profile", "strict", "--"}, ExitUsage},
	} {
		os.Remove(e.marker)
		code, _, stderr := main(t, context.Background(), "", "touch", tc.args...)
		if code != tc.code || e.ran() {
			t.Errorf("%q: code %d, ran %v, stderr %q; want %d and nothing run", tc.args, code, e.ran(), stderr, tc.code)
		}
	}
	// The user's own policy.json, when there is one, is the default.
	e.writePolicy(t, "policy.json", `{"version": 1, "default": "refuse"}`)
	os.Remove(e.marker)
	if code, _, _ := main(t, context.Background(), "", "touch", "run", "--"); code != ExitRefused || e.ran() {
		t.Errorf("the user's policy.json: code %d, ran %v; want refused", code, e.ran())
	}
}

func TestATimeoutExits124(t *testing.T) {
	setup(t)
	start := time.Now()
	code, _, stderr := main(t, context.Background(), "", "sleep", "run", "--timeout", "500ms", "--")
	if code != ExitTimeout || time.Since(start) > 30*time.Second {
		t.Errorf("code %d after %v, stderr %s; want 124", code, time.Since(start), stderr)
	}
}

// Ctrl+C reaches Main as a cancelled context: the tree is killed and the code says so.
func TestAnInterruptExits130(t *testing.T) {
	setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	if code, _, stderr := main(t, ctx, "", "sleep", "run", "--"); code != ExitInterrupted {
		t.Errorf("code %d, stderr %s; want 130", code, stderr)
	}
	// Interrupted before the command starts, it never runs.
	e := setup(t)
	done, stop := context.WithCancel(context.Background())
	stop()
	if code, _, _ := main(t, done, "", "touch", "run", "--"); code != ExitInterrupted || e.ran() {
		t.Errorf("cancelled before the start: code %d, ran %v; want 130, nothing run", code, e.ran())
	}
}

func TestAProgramNotFoundExits127(t *testing.T) {
	setup(t)
	code, _, stderr := main(t, context.Background(), "", "", "run", "--", "adanext-no-such-program-x1")
	if code != ExitNotFound {
		t.Errorf("code %d, stderr %s; want 127", code, stderr)
	}
}

func TestACommandThatCannotRunExits126(t *testing.T) {
	e := setup(t)
	// An audit directory that is a file cannot hold the audit, so the command does not run.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := main(t, context.Background(), "", "touch", "run", "--audit-dir", file, "--")
	if code != ExitCannotRun || e.ran() {
		t.Errorf("audit dir a file: code %d, ran %v, stderr %s; want 126, nothing run", code, e.ran(), stderr)
	}
	// A shell the broker does not know is a command it cannot run.
	if code, _, stderr := main(t, context.Background(), "", "", "run", "--shell", "fish", "--", "echo"); code != ExitCannotRun {
		t.Errorf("unknown shell: code %d, stderr %s; want 126", code, stderr)
	}
}

func TestUsageErrorsExit121(t *testing.T) {
	setup(t)
	for _, args := range [][]string{
		nil,
		{"walk"},
		{"run"},
		{"run", "--"},
		{"run", "--no-such-flag", "--", "x"},
		{"run", "--shell", "sh", "--", "echo", "two"},
		{"run", "--timeout", "soon", "--", "x"},
		{"run", "--timeout", "-1s", "--", "x"},
		{"run", "--output-cap", "-1", "--", "x"},
	} {
		if code, _, _ := main(t, context.Background(), "", "", args...); code != ExitUsage {
			t.Errorf("%q: code %d, want 121", args, code)
		}
	}
	for _, args := range [][]string{{"-h"}, {"help"}, {"run", "-h"}} {
		if code, _, _ := main(t, context.Background(), "", "", args...); code != 0 {
			t.Errorf("%q: code %d, want 0", args, code)
		}
	}
}

func TestTheBinaryRefusesToRunElevated(t *testing.T) {
	e := setup(t)
	isElevated = func() bool { return true }
	defer func() { isElevated = func() bool { return false } }()
	code, _, stderr := main(t, context.Background(), "", "touch", "run", "--")
	if code != ExitCannotRun || e.ran() || !strings.Contains(stderr, "elevated") {
		t.Errorf("code %d, ran %v, stderr %q; want 126 and nothing run", code, e.ran(), stderr)
	}
}

func TestTheAuditLineCarriesTheCaller(t *testing.T) {
	e := setup(t)
	other := t.TempDir()
	if code, _, _ := main(t, context.Background(), "", "exit:0", "run", "--agent", "claude", "--session", "s1", "--run-id", "r1", "--"); code != 0 {
		t.Fatal(code)
	}
	if code, _, _ := main(t, context.Background(), "", "exit:0", "run", "--audit-dir", other, "--"); code != 0 {
		t.Fatal(code)
	}
	for dir, want := range map[string]string{e.audit: "claude s1 r1", other: "  "} {
		lines := auditLines(t, dir)
		if len(lines) != 1 {
			t.Fatalf("%s: %d lines, want 1", dir, len(lines))
		}
		caller, _ := lines[0]["caller"].(map[string]any)
		agent, _ := caller["agent"].(string)
		session, _ := caller["session"].(string)
		runID, _ := caller["run_id"].(string)
		if got := agent + " " + session + " " + runID; got != want || lines[0]["decision"] != "allow" {
			t.Errorf("%s: line %v, want caller %q and the policy's decision", dir, lines[0], want)
		}
	}
}

// failingWriter is a caller's output that has gone away.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the reader went away") }

// A command that ran but whose output could not be delivered does not report its own code.
func TestAFailureAfterTheCommandRanExits120(t *testing.T) {
	setup(t)
	t.Setenv(helperEnv, "print:100")
	var errb bytes.Buffer
	code := Main(context.Background(), []string{"run", "--", os.Args[0], "-test.run=^$"}, strings.NewReader(""), failingWriter{}, &errb)
	if code != ExitBrokerFailed {
		t.Errorf("code %d, stderr %s; want 120", code, errb.String())
	}
}

// A policy that cannot be used refuses the command through Run, so the audit has its line and
// the reason.
func TestAnUnusablePolicyIsRefusedAndAudited(t *testing.T) {
	e := setup(t)
	bad := e.writePolicy(t, "bad.json", `{"version": 1, "default": "maybe"}`)
	code, _, stderr := main(t, context.Background(), "", "touch", "run", "--policy", bad, "--")
	if code != ExitRefused || e.ran() || !strings.Contains(stderr, "maybe") {
		t.Fatalf("code %d, ran %v, stderr %q; want 125 with the reason", code, e.ran(), stderr)
	}
	lines := auditLines(t, e.audit)
	if len(lines) != 1 {
		t.Fatalf("%d audit lines, want 1", len(lines))
	}
	if msg, _ := lines[0]["error"].(string); lines[0]["outcome"] != "not run" || !strings.Contains(msg, "maybe") {
		t.Errorf("audit line %v, want not run with the reason", lines[0])
	}
}

func TestAProgramGivenByAPathThatIsNotThereExits127(t *testing.T) {
	setup(t)
	for _, prog := range []string{
		"." + string(filepath.Separator) + "adanext-no-such-program-x1",
		filepath.Join(t.TempDir(), "adanext-no-such-program-x1"),
	} {
		if code, _, stderr := main(t, context.Background(), "", "", "run", "--", prog); code != ExitNotFound {
			t.Errorf("%s: code %d, stderr %s; want 127", prog, code, stderr)
		}
	}
}

func TestShellTextRunsWithItsQuotingAndExitCode(t *testing.T) {
	setup(t)
	shell, script := "sh", `printf '%s\n' "a  b"; exit 3`
	if runtime.GOOS == "windows" {
		shell, script = "cmd", `echo a  b& exit /b 3`
	}
	code, stdout, stderr := main(t, context.Background(), "", "", "run", "--shell", shell, "--", script)
	if code != 3 || !strings.Contains(stdout, "a  b") {
		t.Errorf("code %d, stdout %q, stderr %q; want 3 and the text as quoted", code, stdout, stderr)
	}
}

// Arguments reach the program as given, those with spaces, empty ones and ones that look like
// flags included, and its standard error passes through.
func TestArgumentsAndStderrPassThrough(t *testing.T) {
	setup(t)
	t.Setenv(helperEnv, "args")
	var out, errb bytes.Buffer
	args := []string{"run", "--", os.Args[0], "-test.run=^$", "a b", "", "--policy", "-x"}
	if code := Main(context.Background(), args, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("code %d, stderr %s", code, errb.String())
	}
	if want := "[a b]\n[]\n[--policy]\n[-x]\n"; out.String() != want {
		t.Errorf("the program got %q, want %q", out.String(), want)
	}
	if !strings.Contains(errb.String(), "to stderr") {
		t.Errorf("stderr %q, want the program's own", errb.String())
	}
}

func TestTheOutputCapBoundsTheOutput(t *testing.T) {
	setup(t)
	code, stdout, _ := main(t, context.Background(), "", "print:10000", "run", "--output-cap", "300", "--")
	if code != 0 || len(stdout) > 400 || !strings.Contains(stdout, "cut") {
		t.Errorf("code %d, %d bytes of output; want under 400 with a cut line", code, len(stdout))
	}
}

func auditLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var lines []map[string]any
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var m map[string]any
			if err := json.Unmarshal(l, &m); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			lines = append(lines, m)
		}
	}
	return lines
}

// Only a missing program or shell is "not found": a not-found error about something else, such
// as a working directory gone before the start, is a command that could not run.
func TestOnlyAMissingProgramIsNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "adanext-no-such-program-x1")
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{exec.ErrNotFound, true},
		{fmt.Errorf("bash: %w", exec.ErrNotFound), true},
		// A program or a shell started by a path where nothing is.
		{&fs.PathError{Op: "fork/exec", Path: missing, Err: fs.ErrNotExist}, true},
		// The program is there: what was missing is something else.
		{&fs.PathError{Op: "fork/exec", Path: os.Args[0], Err: fs.ErrNotExist}, false},
		{&fs.PathError{Op: "chdir", Path: os.Args[0], Err: fs.ErrNotExist}, false},
		{fs.ErrNotExist, false},
		{errors.New("other"), false},
	} {
		if got := programNotFound(tc.err); got != tc.want {
			t.Errorf("programNotFound(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
