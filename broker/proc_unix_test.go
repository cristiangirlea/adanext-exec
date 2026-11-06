//go:build !windows

package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const otherPlatformShell = ShellCmd

type shellCase struct {
	shell    Shell
	script   string
	optional bool
}

var shellCases = []shellCase{
	{ShellSh, `echo 'a "b" c'; exit 4`, false},
	{ShellBash, `echo 'a "b" c'; exit 4`, true},
	{ShellPwsh, `Write-Output 'a "b" c'; exit 4`, true},
}

type echildReport struct {
	Err      string
	ExitCode int
}

func helperPlatform(mode string) {
	switch mode {
	case "escapee":
		// A grandchild in a session of its own, outside the group, holding stdout.
		child := helper("sleep").cmdForTest()
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		startRecorded(child)
		os.Exit(0)
	case "middle-keeper":
		// The leader: starts a middle process that stays, and stays itself.
		startRecordedTo(helper("middle").cmdForTest(), os.Getenv("ADANEXT_TEST_MIDDLE_FILE"))
		time.Sleep(time.Minute)
		os.Exit(0)
	case "middle":
		// Starts a grandchild that leaves the group and drops the environment, so only its
		// parent links it to the tree, then stays.
		child := helper("sleep").cmdForTest()
		child.Env = []string{helperEnv + "=sleep"}
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		startRecorded(child)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "clean-orphaner":
		// Writes ADANEXT_TEST_OUT bytes, then leaves a grandchild that dropped the environment
		// and the group, holding stdout, and exits: nothing links that grandchild to the tree.
		if n, err := strconv.Atoi(os.Getenv("ADANEXT_TEST_OUT")); err == nil {
			_, _ = os.Stdout.Write([]byte(digits(n)))
		}
		child := helper("sleep").cmdForTest()
		child.Env = []string{helperEnv + "=sleep"}
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		startRecorded(child)
		os.Exit(0)
	case "selfterm":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "echild":
		// Children are reaped by the kernel here, so Run loses its leader, which leaves a
		// grandchild behind.
		signal.Ignore(syscall.SIGCHLD)
		res, err := Run(context.Background(), helper("orphaner", "ADANEXT_TEST_PID_FILE="+os.Getenv("ADANEXT_TEST_PID_FILE")))
		rep := echildReport{ExitCode: res.ExitCode}
		if err != nil {
			rep.Err = err.Error()
		}
		_ = json.NewEncoder(os.Stdout).Encode(rep)
		os.Exit(0)
	}
}

func TestRunKillsAnOrphanThatLeftTheGroup(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	watch(t, file)
	c := helper("escapee", "ADANEXT_TEST_PID_FILE="+file)
	c.Timeout = time.Minute
	res, err := Run(context.Background(), c)
	id := readIdentity(t, file)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("result %+v, want exit code 0", res)
	}
	assertGone(t, id)
}

func TestRunKillsADescendantThatLeftTheGroupAndDroppedTheMarker(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	middleFile := filepath.Join(t.TempDir(), "middle.json")
	watch(t, file, middleFile)
	c := helper("middle-keeper", "ADANEXT_TEST_PID_FILE="+file, "ADANEXT_TEST_MIDDLE_FILE="+middleFile)
	c.Timeout = 3 * time.Second
	res, err := Run(context.Background(), c)
	id := readIdentity(t, file)
	assertGone(t, readIdentity(t, middleFile))
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("result %+v, want a timeout", res)
	}
	assertGone(t, id)
}

func TestRunReportsOutputHeldByAnOrphanItCannotFind(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	watch(t, file)
	c := helper("clean-orphaner", "ADANEXT_TEST_PID_FILE="+file)
	c.Timeout = time.Minute
	start := time.Now()
	res, err := Run(context.Background(), c)
	took := time.Since(start)
	readIdentity(t, file) // out of reach by design: killed at cleanup
	if !errors.Is(err, errOutputHeld) {
		t.Fatalf("err %v, want the held output reported", err)
	}
	if res.ExitCode != 0 || took > 20*time.Second {
		t.Fatalf("result %+v after %s, want exit code 0 soon after the grace", res, took)
	}
}

func TestRunReportsASignalFromElsewhere(t *testing.T) {
	c := helper("selfterm")
	c.Timeout = time.Minute
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Signal != "terminated" || res.ExitCode != -1 || res.TimedOut || res.Canceled {
		t.Fatalf("result %+v, want ended by SIGTERM", res)
	}
}

func TestRunNeverSignalsAGroupWhoseLeaderWasReapedElsewhere(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	var out strings.Builder
	watch(t, file)
	cmd := helper("echild", "ADANEXT_TEST_PID_FILE="+file).cmdForTest()
	cmd.Stdout = &out
	err := cmd.Run()
	id := readIdentity(t, file)
	if err != nil {
		t.Fatalf("helper: %v (%s)", err, out.String())
	}
	var rep echildReport
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("helper said %q: %v", out.String(), err)
	}
	if !strings.Contains(rep.Err, "reaped elsewhere") || rep.ExitCode != -1 {
		t.Fatalf("report %+v, want the lost leader reported and no group signalled", rep)
	}
	// What the leader left still carries the marker, so it is killed all the same.
	assertGone(t, id)
}

func TestRunWritesTheCappedTailOnceWhenOutputIsHeld(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	watch(t, file)
	var out strings.Builder
	c := helper("clean-orphaner", "ADANEXT_TEST_PID_FILE="+file, "ADANEXT_TEST_OUT=5000")
	c.Stdout = &out
	c.OutputCap = 900
	res, err := Run(context.Background(), c)
	got := out.String()
	time.Sleep(200 * time.Millisecond)
	if !errors.Is(err, errOutputHeld) {
		t.Fatalf("err %v, want the held output reported", err)
	}
	if want := digits(5000)[:600] + "\n[... 4100 bytes cut ...]\n" + digits(5000)[4700:]; got != want || out.String() != got {
		t.Fatalf("output %q (then %d bytes), want the head, the line and the tail once", got, out.Len())
	}
	if res.OutputCut != 4100 {
		t.Fatalf("OutputCut %d, want 4100", res.OutputCut)
	}
}
