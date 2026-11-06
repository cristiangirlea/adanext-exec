//go:build !windows

package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
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
	case "selfterm":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "echild":
		// Children are reaped by the kernel here, so Run loses its leader.
		signal.Ignore(syscall.SIGCHLD)
		res, err := Run(context.Background(), helper("exit"))
		rep := echildReport{ExitCode: res.ExitCode}
		if err != nil {
			rep.Err = err.Error()
		}
		_ = json.NewEncoder(os.Stdout).Encode(rep)
		os.Exit(0)
	}
}

func TestRunReportsOutputHeldOpenFromOutsideTheGroup(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	c := helper("escapee", "ADANEXT_TEST_PID_FILE="+file)
	c.Timeout = time.Minute
	start := time.Now()
	res, err := Run(context.Background(), c)
	took := time.Since(start)
	readIdentity(t, file) // killed at cleanup: leaving the group is M1b's
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
	var out strings.Builder
	cmd := helper("echild").cmdForTest()
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper: %v (%s)", err, out.String())
	}
	var rep echildReport
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("helper said %q: %v", out.String(), err)
	}
	if !strings.Contains(rep.Err, "reaped elsewhere") || rep.ExitCode != -1 {
		t.Fatalf("report %+v, want the lost leader reported and no group signalled", rep)
	}
}
