package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// holdRun runs "run" with args in the background; the func it returns waits for its exit
// code. When the test ends it is interrupted and waited for, so no command outlives the test.
func holdRun(t *testing.T, args ...string) func() int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var out, errb bytes.Buffer
	go func() { done <- Main(ctx, args, strings.NewReader(""), &out, &errb) }()
	var once sync.Once
	var code int
	wait := func() int {
		once.Do(func() { code = <-done })
		return code
	}
	t.Cleanup(func() {
		cancel()
		wait()
	})
	return wait
}

// helperHeavy is a daemon configuration that counts the tests' helper, and only it, heavy.
func helperHeavy(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "heavy": ["test[.]run"], "only": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// startServe runs "serve" in-process on a short socket path until the test ends.
func startServe(t *testing.T, args ...string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ae")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var errb bytes.Buffer
	go func() {
		done <- Main(ctx, append([]string{"serve", "--socket", socket}, args...), strings.NewReader(""), &bytes.Buffer{}, &errb)
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if c, err := net.Dial("unix", socket); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serve never listened")
		}
	}
	t.Cleanup(func() {
		cancel()
		if code := <-done; code != 0 {
			t.Errorf("serve ended %d", code)
		}
	})
	return socket
}

func TestRunTakesTurnsThroughServe(t *testing.T) {
	setup(t)
	socket := startServe(t, "--slots", "1", "--config", helperHeavy(t))
	t.Setenv(helperEnv, "sleep")
	holder := holdRun(t, "run", "--socket", socket, "--timeout", "700ms", "--", os.Args[0], "-test.run=^$")
	time.Sleep(250 * time.Millisecond)
	// The environment is read when each command starts; the holder has started by now.
	code, _, stderr := main(t, context.Background(), "", "exit:0", "run", "--socket", socket, "--")
	if code != 0 || !strings.Contains(stderr, "waiting for a heavy-command slot (place 1)") {
		t.Errorf("code %d, stderr %q; want it run after waiting at place 1", code, stderr)
	}
	if code := holder(); code != ExitTimeout {
		t.Errorf("the holder ended %d, want its timeout", code)
	}
}

func TestRunWithNoDaemonExits123(t *testing.T) {
	e := setup(t)
	missing := filepath.Join(t.TempDir(), "none.sock")
	code, _, stderr := main(t, context.Background(), "", "touch", "run", "--socket", missing, "--")
	if code != ExitNoDaemon || e.ran() {
		t.Errorf("code %d, ran %v, stderr %q; want 123 and nothing run", code, e.ran(), stderr)
	}
	// --slots asks at the default socket.
	defaultSocket = func() (string, error) { return missing, nil }
	if code, _, _ := main(t, context.Background(), "", "touch", "run", "--slots", "--"); code != ExitNoDaemon || e.ran() {
		t.Errorf("--slots: code %d; want 123", code)
	}
}

func TestNoSlotWithinMaxWaitExits124(t *testing.T) {
	e := setup(t)
	socket := startServe(t, "--slots", "1", "--config", helperHeavy(t))
	t.Setenv(helperEnv, "sleep")
	holdRun(t, "run", "--socket", socket, "--timeout", "2s", "--", os.Args[0], "-test.run=^$")
	time.Sleep(250 * time.Millisecond)
	code, _, stderr := main(t, context.Background(), "", "touch", "run", "--socket", socket, "--max-wait", "200ms", "--")
	if code != ExitTimeout || e.ran() {
		t.Errorf("code %d, ran %v, stderr %q; want 124 and nothing run", code, e.ran(), stderr)
	}
}

func TestServeUsageAndRefusals(t *testing.T) {
	setup(t)
	socket := startServe(t, "--slots", "1")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"serve", "--slots", "0", "--socket", socket}, ExitUsage},
		{[]string{"serve", "extra"}, ExitUsage},
		{[]string{"serve", "--no-such-flag"}, ExitUsage},
		// A second daemon on the same socket does not start.
		{[]string{"serve", "--socket", socket}, ExitCannotRun},
		{[]string{"run", "--max-wait", "1s", "--", "x"}, ExitUsage},
		{[]string{"run", "--socket", socket, "--max-wait", "-1s", "--", "x"}, ExitUsage},
	} {
		var out, errb bytes.Buffer
		if code := Main(context.Background(), tc.args, strings.NewReader(""), &out, &errb); code != tc.code {
			t.Errorf("%q: code %d, stderr %q; want %d", tc.args, code, errb.String(), tc.code)
		}
	}
}

// Elevated, serve does not start. No daemon runs in this test: isElevated is swapped while
// nothing else reads it.
func TestServeRefusesToRunElevated(t *testing.T) {
	setup(t)
	isElevated = func() bool { return true }
	defer func() { isElevated = func() bool { return false } }()
	var out, errb bytes.Buffer
	if code := Main(context.Background(), []string{"serve", "--socket", filepath.Join(t.TempDir(), "x.sock")}, strings.NewReader(""), &out, &errb); code != ExitCannotRun {
		t.Errorf("serve elevated: code %d, want 126", code)
	}
}

// A light command never waits, even with every slot taken.
func TestALightCommandRunsWhileTheSlotsAreFull(t *testing.T) {
	setup(t)
	socket := startServe(t, "--slots", "1", "--config", helperHeavy(t))
	t.Setenv(helperEnv, "sleep")
	holdRun(t, "run", "--socket", socket, "--timeout", "3s", "--", os.Args[0], "-test.run=^$")
	time.Sleep(250 * time.Millisecond)
	code, _, stderr := main(t, context.Background(), "", "", "run", "--socket", socket, "--max-wait", "500ms", "--", "go", "version")
	if code != 0 || strings.Contains(stderr, "waiting") {
		t.Errorf("go version: code %d, stderr %q; want it run at once", code, stderr)
	}
}

func TestServeRefusesAConfigItCannotUse(t *testing.T) {
	setup(t)
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"version": 1, "heavy": ["("]}`), 0o600)
	var out, errb bytes.Buffer
	if code := Main(context.Background(), []string{"serve", "--config", bad, "--socket", filepath.Join(t.TempDir(), "x.sock")}, strings.NewReader(""), &out, &errb); code != ExitUsage {
		t.Errorf("code %d, stderr %q; want 121", code, errb.String())
	}
}

// serve gives a heavy command its share of the CPUs, through the variables builds read.
func TestServeHandsAHeavyCommandItsShare(t *testing.T) {
	setup(t)
	for _, k := range []string{"GOFLAGS", "GOMAXPROCS", "MAKEFLAGS", "CARGO_BUILD_JOBS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	socket := startServe(t, "--slots", "2", "--config", helperHeavy(t))
	code, stdout, stderr := main(t, context.Background(), "", "hints", "run", "--socket", socket, "--")
	k := strconv.Itoa(max(1, runtime.NumCPU()/2))
	want := "GOFLAGS=-p=" + k + ";GOMAXPROCS=" + k + ";MAKEFLAGS=-j" + k + ";CARGO_BUILD_JOBS=" + k + ";"
	if code != 0 || stdout != want {
		t.Errorf("code %d, stdout %q, stderr %q; want %q", code, stdout, stderr, want)
	}
}
