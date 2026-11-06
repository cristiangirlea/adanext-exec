package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// readAudit returns the audit's lines, from its only file, which must be named for today in UTC.
func readAudit(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("audit files %v (%v), want one", files, err)
	}
	if want := time.Now().UTC().Format("2006-01-02") + ".jsonl"; filepath.Base(files[0]) != want {
		t.Fatalf("audit file %s, want %s", filepath.Base(files[0]), want)
	}
	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("a line that is not JSON: %q", sc.Text())
		}
		lines = append(lines, m)
	}
	return lines
}

func TestRunWritesOneAuditLinePerCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	work := t.TempDir()
	c := helper("exit", "ADANEXT_TEST_CODE=3")
	c.Dir = work
	c.Audit = &Audit{Dir: dir}
	c.Caller = Caller{Agent: "claude", Session: "s-1", RunID: "r-2"}
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	lines := readAudit(t, dir)
	if len(lines) != 1 {
		t.Fatalf("%d lines, want 1", len(lines))
	}
	l := lines[0]
	if l["outcome"] != "exited" || l["exit_code"] != float64(3) || l["dir"] != work {
		t.Fatalf("line %v, want exited with 3 in %s", l, work)
	}
	if caller, _ := l["caller"].(map[string]any); caller["agent"] != "claude" || caller["session"] != "s-1" || caller["run_id"] != "r-2" {
		t.Fatalf("caller %v", l["caller"])
	}
	if args, _ := l["args"].([]any); len(args) != 2 || args[0] != os.Args[0] {
		t.Fatalf("args %v, want the command's argv", l["args"])
	}
	if at, err := time.Parse(time.RFC3339Nano, l["time"].(string)); err != nil || time.Since(at) > time.Minute {
		t.Fatalf("time %v (%v)", l["time"], err)
	}
	if d := l["duration_ms"].(float64); d < 0 || d > float64(res.Duration.Milliseconds()+1) {
		t.Fatalf("duration %v, Result says %s", d, res.Duration)
	}
	if _, ok := l["env"]; ok {
		t.Fatal("the environment was written")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl"))
		dinfo, derr := os.Stat(dir)
		if err != nil || derr != nil || info.Mode().Perm() != 0o600 || dinfo.Mode().Perm() != 0o700 {
			t.Fatalf("file mode %v, directory mode %v (%v, %v); want 0600 and 0700", info.Mode(), dinfo.Mode(), err, derr)
		}
	}
}

func TestTheAuditTakesSecretsOut(t *testing.T) {
	dir := t.TempDir()
	const fromEnv = "value-of-a-secret-variable-0123"
	c := helper("exit")
	c.Env = append(c.Env, "MY_SERVICE_TOKEN="+fromEnv)
	c.Args = append(c.Args, "--", "API_KEY=plain-secret-1", "--password", "plain-secret-2", "-u", "user:plain-secret-5", "Bearer", "plain-secret-6", "https://user:plain-secret-3@example.test/x", fromEnv)
	c.Audit = &Audit{Dir: dir}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	script := Command{Shell: otherPlatformShell, Script: "curl -H 'Authorization: Bearer plain-secret-4' x", Audit: &Audit{Dir: dir}}
	if _, err := Run(context.Background(), script); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v, want the other platform's shell refused", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"plain-secret-1", "plain-secret-2", "plain-secret-3", "plain-secret-4", "plain-secret-5", "plain-secret-6", fromEnv} {
		if strings.Contains(string(data), secret) {
			t.Errorf("the audit holds %q", secret)
		}
	}
	if strings.Count(string(data), "[redacted]") < 7 {
		t.Fatalf("audit %s, want seven redactions", data)
	}
}

func TestTheAuditRecordsACommandItDidNotRun(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(context.Background(), Command{Audit: &Audit{Dir: dir}, Caller: Caller{Agent: "hrn"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v, want ErrInvalid", err)
	}
	l := readAudit(t, dir)[0]
	if l["outcome"] != "not run" || !strings.Contains(l["error"].(string), "invalid command") {
		t.Fatalf("line %v, want not run with the reason", l)
	}
	if _, ok := l["exit_code"]; ok {
		t.Fatal("a command that did not run has an exit code")
	}
}

func TestTheAuditRecordsATimeoutAndCutOutput(t *testing.T) {
	dir := t.TempDir()
	c := spew(5000, 0, "ADANEXT_TEST_WAIT=1m")
	c.Timeout = 2 * time.Second
	c.OutputCap = 900
	c.Audit = &Audit{Dir: dir}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	l := readAudit(t, dir)[0]
	if l["outcome"] != "timed out" || l["exit_code"] != float64(-1) || l["output_cut"] != float64(4100) {
		t.Fatalf("line %v, want timed out, -1, 4100 bytes cut", l)
	}
	if d := l["duration_ms"].(float64); d < 2000 {
		t.Fatalf("duration %v ms, want the 2 s timeout", d)
	}
}

func TestACommandWhoseAuditCannotBeOpenedIsNotRun(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	c := helper("args")
	c.Dir = marker // a missing directory would fail the start; the audit must fail first
	for _, a := range []*Audit{{Dir: notADir}, {Dir: ""}} {
		c.Audit = a
		res, err := Run(context.Background(), c)
		if !errors.Is(err, ErrAudit) || res != (Result{}) {
			t.Fatalf("audit %q: %+v, %v; want ErrAudit and nothing run", a.Dir, res, err)
		}
	}
}

func TestConcurrentCommandsWriteWholeLines(t *testing.T) {
	dir := t.TempDir()
	a := &Audit{Dir: dir}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := helper("exit")
			c.Args = append(c.Args, "--", strings.Repeat("x", 16<<10)) // under Windows' 32 KiB command line
			c.Audit = a
			if _, err := Run(context.Background(), c); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(readAudit(t, dir)); n != 8 {
		t.Fatalf("%d lines, want 8", n)
	}
}

func TestTheDefaultAuditDirectory(t *testing.T) {
	if runtime.GOOS == "linux" {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		if dir, err := DefaultAuditDir(); err != nil || dir != filepath.Join(state, "adanext-exec", "audit") {
			t.Fatalf("with XDG_STATE_HOME: %q (%v)", dir, err)
		}
		t.Setenv("XDG_STATE_HOME", "relative")
		home, _ := os.UserHomeDir()
		if dir, err := DefaultAuditDir(); err != nil || dir != filepath.Join(home, ".local", "state", "adanext-exec", "audit") {
			t.Fatalf("with a relative XDG_STATE_HOME: %q (%v)", dir, err)
		}
		return
	}
	dir, err := DefaultAuditDir()
	if err != nil || !filepath.IsAbs(dir) || !strings.HasSuffix(dir, filepath.Join("adanext-exec", "audit")) {
		t.Fatalf("%q (%v)", dir, err)
	}
}
