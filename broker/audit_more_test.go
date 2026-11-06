package broker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheAuditFileIsTheStartDayInUTC(t *testing.T) {
	start := time.Date(2031, 1, 2, 23, 59, 59, 900_000_000, time.UTC)
	old := now
	t.Cleanup(func() { now = old })
	now = func() time.Time { return start.In(time.FixedZone("east", 3*3600)) } // local 2031-01-03
	dir := t.TempDir()
	c := helper("exit")
	c.Audit = &Audit{Dir: dir}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2031-01-02.jsonl")); err != nil {
		t.Fatalf("no 2031-01-02.jsonl: %v", err)
	}
	if l := readAudit(t, dir)[0]; l["time"] != "2031-01-02T23:59:59.9Z" {
		t.Fatalf("time %v, want the start in UTC", l["time"])
	}
}

func TestTheAuditOutcomeOfEachEnding(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-program")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, c := range map[string]struct {
		ctx     context.Context
		cmd     Command
		outcome string
		code    any // nil: no exit code
		errText string
	}{
		"cannot start":        {context.Background(), Command{Args: []string{missing}}, "not run", nil, "missing-program"},
		"cancelled before":    {cancelled, helper("exit"), "not run", nil, "context canceled"},
		"a failed write":      {context.Background(), func() Command { c := helper("exit"); c.Stdout = failingWriter{}; return c }(), "exited", float64(0), "disk full"},
		"exit code":           {context.Background(), helper("exit", "ADANEXT_TEST_CODE=5"), "exited", float64(5), ""},
		"cancelled during it": {nil, helper("sleep"), "cancelled", float64(-1), ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ctx := c.ctx
			if ctx == nil {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				time.AfterFunc(time.Second, cancel)
			}
			cmd := c.cmd
			cmd.Audit = &Audit{Dir: dir}
			_, _ = Run(ctx, cmd)
			l := readAudit(t, dir)[0]
			if l["outcome"] != c.outcome || l["exit_code"] != c.code {
				t.Fatalf("line %v, want outcome %q and exit code %v", l, c.outcome, c.code)
			}
			if got, _ := l["error"].(string); (c.errText == "") != (got == "") || !strings.Contains(got, c.errText) {
				t.Fatalf("error %q, want one holding %q", got, c.errText)
			}
		})
	}
}

func TestAFailedAuditWriteKeepsTheResult(t *testing.T) {
	old := auditWrite
	t.Cleanup(func() { auditWrite = old })
	auditWrite = func(*os.File, []byte) error { return errors.New("the disk is gone") }
	c := helper("exit", "ADANEXT_TEST_CODE=3")
	c.Audit = &Audit{Dir: t.TempDir()}
	res, err := Run(context.Background(), c)
	if !errors.Is(err, ErrAudit) || !strings.Contains(err.Error(), "the disk is gone") {
		t.Fatalf("err %v, want ErrAudit with the cause", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("result %+v, want the command's own result kept", res)
	}
}

func TestSeveralProcessesShareOneAuditFile(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := helper("audit-writer", "ADANEXT_TEST_AUDIT="+dir).cmdForTest()
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- errors.New(err.Error() + ": " + string(out))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := len(readAudit(t, dir)); n != 20 {
		t.Fatalf("%d whole lines, want 20", n)
	}
}

func TestTheAuditRecordsTheDirectoryTheCommandRanIn(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	dir := t.TempDir()
	var out strings.Builder
	c := helper("pwd")
	c.Stdout = &out
	c.Audit = &Audit{Dir: dir}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ran, _ := filepath.EvalSymlinks(out.String())
	recorded, _ := filepath.EvalSymlinks(readAudit(t, dir)[0]["dir"].(string))
	if ran == "" || !strings.EqualFold(ran, recorded) {
		t.Fatalf("ran in %q, the audit says %q", out.String(), readAudit(t, dir)[0]["dir"])
	}
}

func TestTheAuditMasksAnInheritedSecretThatChangesWhileTheCommandRuns(t *testing.T) {
	// No Env: the command inherits this process's environment, where the secret lives.
	const secret = "the-inherited-value-before-77"
	t.Setenv("ADANEXT_TEST_TOKEN", secret)
	t.Setenv(helperEnv, "sleep")
	dir := t.TempDir()
	c := Command{Args: []string{os.Args[0], "-test.run=^TestHelperProcess$", "--", secret}, Timeout: 2 * time.Second, Audit: &Audit{Dir: dir}}
	time.AfterFunc(500*time.Millisecond, func() { os.Setenv("ADANEXT_TEST_TOKEN", "another-value-entirely-00") })
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if data := string(auditBytes(t, dir)); strings.Contains(data, secret) {
		t.Fatalf("the audit holds the inherited secret: %s", data)
	}
}

func TestTheAuditReadsTheEnvironmentAndDirectoryAsTheCommandStarted(t *testing.T) {
	const secret = "the-value-before-the-start-42"
	t.Setenv("ADANEXT_TEST_TOKEN", secret)
	start, later := t.TempDir(), t.TempDir()
	t.Chdir(start)
	dir := t.TempDir()
	c := helper("sleep")
	c.Args = append(c.Args, "--", secret)
	c.Timeout = 2 * time.Second
	c.Audit = &Audit{Dir: dir}
	time.AfterFunc(500*time.Millisecond, func() {
		os.Setenv("ADANEXT_TEST_TOKEN", "another-value-entirely-00")
		_ = os.Chdir(later)
	})
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	data := string(auditBytes(t, dir))
	if strings.Contains(data, secret) {
		t.Fatalf("the audit holds the secret the command started with: %s", data)
	}
	got := readAudit(t, dir)[0]["dir"].(string)
	want, _ := filepath.EvalSymlinks(start)
	if g, _ := filepath.EvalSymlinks(got); !strings.EqualFold(g, want) {
		t.Fatalf("dir %q, want the directory the command started in, %q", got, start)
	}
}

func TestTheCommandRunsInTheRecordedDirectoryEvenIfTheCallersChangesAfter(t *testing.T) {
	recorded, elsewhere := t.TempDir(), t.TempDir()
	t.Chdir(recorded)
	old := getwd
	t.Cleanup(func() { getwd = old })
	getwd = func() (string, error) {
		wd, err := old()
		_ = os.Chdir(elsewhere) // the caller's directory changes just after Run read it
		return wd, err
	}
	dir := t.TempDir()
	var out strings.Builder
	c := helper("pwd")
	c.Stdout = &out
	c.Audit = &Audit{Dir: dir}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ran, _ := filepath.EvalSymlinks(out.String())
	want, _ := filepath.EvalSymlinks(recorded)
	if !strings.EqualFold(ran, want) {
		t.Fatalf("ran in %q, want the recorded %q", out.String(), recorded)
	}
}

func TestACommandWhoseDirectoryCannotBeReadForTheAuditIsNotRun(t *testing.T) {
	old := getwd
	t.Cleanup(func() { getwd = old })
	getwd = func() (string, error) { return "", errors.New("the directory was removed") }
	marker := filepath.Join(t.TempDir(), "ran")
	c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
	c.Audit = &Audit{Dir: t.TempDir()}
	res, err := Run(context.Background(), c)
	if !errors.Is(err, ErrAudit) || res != (Result{}) {
		t.Fatalf("%+v, %v; want ErrAudit and nothing run", res, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran")
	}
}
