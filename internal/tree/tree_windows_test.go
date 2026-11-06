//go:build windows

package tree

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

const createBreakawayFromJob = 0x01000000

func helperPlatform(mode string) {
	switch mode {
	case "breakaway":
		// Asks Windows to start a child outside the job; the job must not allow it.
		child := helperCmd("sleep")
		child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createBreakawayFromJob}
		if err := child.Start(); err != nil {
			fmt.Print("refused")
			os.Exit(0)
		}
		_ = child.Process.Kill()
		fmt.Print("escaped")
		os.Exit(3)
	case "nest":
		// Starts a tree of its own while inside one.
		inner := helperCmd("sleep")
		tr, err := Start(inner)
		if err != nil {
			fmt.Print(err)
			os.Exit(4)
		}
		if err := tr.Kill(); err != nil {
			fmt.Print(err)
			os.Exit(5)
		}
		_ = inner.Wait()
		tr.Close()
		fmt.Print("nested")
		os.Exit(0)
	}
}

func TestTheJobKillsOnCloseAndNeverAllowsBreakaway(t *testing.T) {
	cmd := helperCmd("sleep")
	tr := startTree(t, cmd)
	flags, err := tr.limitFlags()
	if err != nil {
		t.Fatal(err)
	}
	if flags&jobObjectLimitKillOnJobClose == 0 {
		t.Fatalf("flags %#x lack KILL_ON_JOB_CLOSE", flags)
	}
	if flags&(0x800|0x1000) != 0 {
		t.Fatalf("flags %#x allow breakaway", flags)
	}
}

func TestADescendantCannotBreakAwayFromTheJob(t *testing.T) {
	cmd := helperCmd("breakaway")
	var out strings.Builder
	cmd.Stdout = &out
	startTree(t, cmd)
	if err := cmd.Wait(); err != nil || out.String() != "refused" {
		t.Fatalf("helper said %q (%v), want refused", out.String(), err)
	}
}

func TestATreeStartsInsideAnotherTree(t *testing.T) {
	cmd := helperCmd("nest")
	var out strings.Builder
	cmd.Stdout = &out
	startTree(t, cmd)
	if err := cmd.Wait(); err != nil || out.String() != "nested" {
		t.Fatalf("helper said %q (%v), want nested", out.String(), err)
	}
}

func TestCloseKillsWhatIsStillInTheJob(t *testing.T) {
	cmd := helperCmd("sleep")
	tr := startTree(t, cmd)
	if n, err := tr.Active(); err != nil || n != 1 {
		t.Fatalf("active %d (%v), want 1", n, err)
	}
	tr.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done // cleanup must not wait at the same time
		t.Fatal("closing the job did not kill the command")
	}
}

func TestStartOfAProgramThatIsNotOneKeepsNothing(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Path = t.TempDir() // a directory: CreateProcess fails
	if tr, err := Start(cmd); err == nil {
		tr.Close()
		t.Fatal("started a directory")
	}
}
