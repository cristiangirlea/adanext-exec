package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const helperEnv = "ADANEXT_TEST_HELPER"

type identity struct {
	PID  int
	Born string
}

// TestHelperProcess is the program the tests run, chosen by ADANEXT_TEST_HELPER.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	switch mode {
	case "":
		return
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "parent", "orphaner":
		// Starts a grandchild that holds this process's stdout, records it, then waits a
		// minute (parent) or exits at once (orphaner).
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		child.Env = append(os.Environ(), helperEnv+"=sleep")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		startRecorded(child)
		if mode == "orphaner" {
			os.Exit(0)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	case "exit":
		fmt.Fprint(os.Stdout, "to stdout")
		fmt.Fprint(os.Stderr, "to stderr")
		code, _ := strconv.Atoi(os.Getenv("ADANEXT_TEST_CODE"))
		os.Exit(code)
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "args":
		for i, a := range os.Args {
			if a == "--" {
				_ = json.NewEncoder(os.Stdout).Encode(os.Args[i+1:])
				os.Exit(0)
			}
		}
		os.Exit(2)
	case "pwd":
		dir, _ := os.Getwd()
		fmt.Print(dir)
		os.Exit(0)
	case "chunks":
		fmt.Print("first")
		time.Sleep(300 * time.Millisecond)
		fmt.Print("second")
		os.Exit(0)
	default:
		helperPlatform(mode)
		os.Exit(2)
	}
}

// startRecorded starts child and writes its identity to ADANEXT_TEST_PID_FILE. A child that
// cannot be recorded is killed, since no test could find it.
func startRecorded(child *exec.Cmd) {
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fail := func(err error) {
		_ = child.Process.Kill()
		_ = child.Wait()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	born, _, err := processState(child.Process.Pid)
	if err != nil {
		fail(err)
	}
	data, _ := json.Marshal(identity{child.Process.Pid, born})
	if err := os.WriteFile(os.Getenv("ADANEXT_TEST_PID_FILE"), data, 0o600); err != nil {
		fail(err)
	}
}

// cmdForTest builds the helper's process directly, outside Run.
func (c Command) cmdForTest() *exec.Cmd {
	cmd, err := c.cmd()
	if err != nil {
		panic(fmt.Sprint(err))
	}
	return cmd
}

func helper(mode string, env ...string) Command {
	return Command{
		Args: []string{os.Args[0], "-test.run=^TestHelperProcess$"},
		Env:  append(append(os.Environ(), helperEnv+"="+mode), env...),
	}
}

// readIdentity waits for a helper's grandchild record and has that grandchild killed at
// cleanup, should a test fail before the broker killed it.
func readIdentity(t *testing.T, file string) identity {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(file)
		var id identity
		if err == nil && json.Unmarshal(data, &id) == nil && id.PID > 0 {
			track(t, id)
			return id
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the helper did not record its grandchild")
	return identity{}
}

// track kills the process at cleanup if it is still the same process.
func track(t *testing.T, id identity) {
	t.Cleanup(func() { killIfSame(id) })
}

// killIfSame kills id's process only while its pid still names the process born then.
func killIfSame(id identity) {
	if born, alive, err := processState(id.PID); err == nil && alive && born == id.Born {
		if p, err := os.FindProcess(id.PID); err == nil {
			_ = p.Kill()
		}
	}
}

func TestKillIfSameLeavesAProcessBornAtAnotherTimeAlone(t *testing.T) {
	cmd := helper("sleep").cmdForTest()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = cmd.Process.Kill()
			<-done
		}
	})
	born, alive, err := processState(cmd.Process.Pid)
	if err != nil || !alive {
		t.Fatalf("a running child: alive %v, err %v", alive, err)
	}
	killIfSame(identity{cmd.Process.Pid, born + "0"})
	select {
	case <-done:
		reaped = true
		t.Fatal("a process with another birth was killed")
	case <-time.After(300 * time.Millisecond):
	}
	killIfSame(identity{cmd.Process.Pid, born})
	select {
	case <-done:
		reaped = true
	case <-time.After(10 * time.Second):
		t.Fatal("the same process was not killed")
	}
}

// assertGone waits a little for the grandchild to be reaped by whoever adopted it.
func assertGone(t *testing.T, id identity) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		born, alive, err := processState(id.PID)
		if err != nil {
			t.Fatal(err)
		}
		if !alive || born != id.Born {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("grandchild %d (born %s) is still running", id.PID, id.Born)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunTimeoutKillsTheWholeTree(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	c := helper("parent", "ADANEXT_TEST_PID_FILE="+file)
	c.Timeout = 3 * time.Second
	res, err := Run(context.Background(), c)
	id := readIdentity(t, file)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Canceled || res.ExitCode != -1 || res.Signal != "" {
		t.Fatalf("result %+v, want a timeout with exit code -1", res)
	}
	if res.Duration < 3*time.Second || res.Duration > 30*time.Second {
		t.Fatalf("duration %s, want about the 3s timeout", res.Duration)
	}
	assertGone(t, id)
}

func TestRunReportsTheContextDeadlineAsATimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := Run(ctx, helper("sleep"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Canceled || res.ExitCode != -1 {
		t.Fatalf("result %+v, want a timeout", res)
	}
}

func TestRunCancelKillsTheWholeTree(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := make(chan identity, 1)
	go func() {
		defer close(ids)
		for ctx.Err() == nil {
			data, err := os.ReadFile(file)
			var id identity
			if err == nil && json.Unmarshal(data, &id) == nil && id.PID > 0 {
				ids <- id
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	c := helper("parent", "ADANEXT_TEST_PID_FILE="+file)
	c.Timeout = time.Minute
	res, err := Run(ctx, c)
	id, ok := <-ids
	if !ok {
		t.Fatalf("the helper did not record its grandchild (%+v, %v)", res, err)
	}
	track(t, id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Canceled || res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("result %+v, want cancelled with exit code -1", res)
	}
	assertGone(t, id)
}

func TestRunKillsWhatTheCommandLeftRunning(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	var out bytes.Buffer
	c := helper("orphaner", "ADANEXT_TEST_PID_FILE="+file)
	c.Stdout = &out
	c.Timeout = time.Minute
	start := time.Now()
	res, err := Run(context.Background(), c)
	took := time.Since(start)
	id := readIdentity(t, file)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.TimedOut || res.Canceled {
		t.Fatalf("result %+v, want exit code 0", res)
	}
	// The grandchild sleeps a minute holding stdout; Run must not wait for it.
	if took > 20*time.Second {
		t.Fatalf("Run took %s, so it waited for the grandchild", took)
	}
	assertGone(t, id)
}

func TestRunReportsTheExitCodeAndBothStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := helper("exit", "ADANEXT_TEST_CODE=3")
	c.Stdout, c.Stderr = &stdout, &stderr
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.TimedOut || res.Canceled || res.Signal != "" {
		t.Fatalf("result %+v, want exit code 3", res)
	}
	if stdout.String() != "to stdout" || stderr.String() != "to stderr" {
		t.Fatalf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestRunGivesOneWriterBothStreams(t *testing.T) {
	// bytes.Buffer is not safe for concurrent writes: the race detector checks Run's promise.
	var out bytes.Buffer
	c := helper("exit")
	c.Stdout, c.Stderr = &out, &out
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "to stdout") || !strings.Contains(out.String(), "to stderr") {
		t.Fatalf("output %q, want both streams", out.String())
	}
}

func TestRunPassesArgvUnchanged(t *testing.T) {
	want := []string{"", "two words", `quote " inside`, `back\slash\`, `trailing\\`, "a&b|c<d>e^f", "%PATH%", "$HOME", "'single'", "*"}
	var out bytes.Buffer
	c := helper("args")
	c.Args = append(append(c.Args, "--"), want...)
	c.Stdout = &out
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") || len(got) != len(want) {
		t.Fatalf("argv %q, want %q", got, want)
	}
}

func TestRunUsesTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	c := helper("pwd")
	c.Dir = dir
	c.Stdout = &out
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	got, err1 := filepath.EvalSymlinks(out.String())
	want, err2 := filepath.EvalSymlinks(dir)
	if err1 != nil || err2 != nil || !strings.EqualFold(got, want) {
		t.Fatalf("working directory %q, want %q (%v, %v)", out.String(), dir, err1, err2)
	}
}

func TestRunPassesStdin(t *testing.T) {
	var out bytes.Buffer
	c := helper("cat")
	c.Stdin = strings.NewReader("from stdin")
	c.Stdout = &out
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if out.String() != "from stdin" {
		t.Fatalf("output %q", out.String())
	}
}

func TestRunDoesNotWaitForAStdinThatNeverEnds(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	for name, c := range map[string]Command{
		"exit":    helper("exit"),
		"timeout": func() Command { c := helper("sleep"); c.Timeout = time.Second; return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			c.Stdin = r
			done := make(chan error, 1)
			go func() { _, err := Run(context.Background(), c); done <- err }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("Run waited for a stdin reader that never returns")
			}
		})
	}
}

func TestRunDoesNotWaitForAStdinThatNeverEndsWhenCancelled(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(time.Second, cancel)
	c := helper("sleep")
	c.Stdin = r
	done := make(chan Result, 1)
	go func() { res, _ := Run(ctx, c); done <- res }()
	select {
	case res := <-done:
		if !res.Canceled {
			t.Fatalf("result %+v, want cancelled", res)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run waited for a stdin reader that never returns")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRunReportsAFailedWriteAndStillFinishes(t *testing.T) {
	c := helper("exit", "ADANEXT_TEST_CODE=0")
	c.Stdout = failingWriter{}
	res, err := Run(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err %v, want the write error", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("result %+v, want exit code 0 still reported", res)
	}
}

// slowWriter holds its first Write past the output grace.
type slowWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	calls int
}

func (w *slowWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.calls++
	first := w.calls == 1
	w.mu.Unlock()
	if first {
		time.Sleep(2 * outputGrace)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func TestRunWaitsForASlowWriterAndKeepsAllTheOutput(t *testing.T) {
	w := &slowWriter{}
	c := helper("chunks")
	c.Stdout = w
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if got := w.buf.String(); got != "firstsecond" {
		t.Fatalf("output %q, want firstsecond", got)
	}
}

func TestRunRefusesAnInvalidCommand(t *testing.T) {
	for name, c := range map[string]Command{
		"empty":            {},
		"empty program":    {Args: []string{""}},
		"args and script":  {Args: []string{"x"}, Shell: ShellBash, Script: "x"},
		"args and shell":   {Args: []string{"x"}, Shell: ShellBash},
		"script, no shell": {Script: "echo"},
		"shell, no script": {Shell: ShellBash},
		"unknown shell":    {Shell: "zsh", Script: "echo"},
		"negative timeout": {Args: []string{os.Args[0]}, Timeout: -time.Second},
		"other os's shell": {Shell: otherPlatformShell, Script: "echo"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Run(context.Background(), c); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err %v, want ErrInvalid", err)
			}
		})
	}
}

func TestRunDoesNotStartUnderADoneContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, helper("exit")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
}

func TestRunReportsAProgramThatCannotStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-program")
	if runtime.GOOS == "windows" {
		missing += ".exe"
	}
	_, err := Run(context.Background(), Command{Args: []string{missing}})
	if err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v, want a start error", err)
	}
}

func TestRunRunsShellText(t *testing.T) {
	for _, tc := range shellCases {
		t.Run(string(tc.shell), func(t *testing.T) {
			var out bytes.Buffer
			res, err := Run(context.Background(), Command{Shell: tc.shell, Script: tc.script, Stdout: &out, Stderr: &out})
			if errors.Is(err, exec.ErrNotFound) && tc.optional {
				t.Skipf("%s is not installed", tc.shell)
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.ExitCode != 4 || strings.TrimSpace(out.String()) != `a "b" c` {
				t.Fatalf("exit %d, output %q; want 4 and a \"b\" c", res.ExitCode, out.String())
			}
		})
	}
}
