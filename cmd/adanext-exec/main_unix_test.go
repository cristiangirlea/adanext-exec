//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "ADANEXT_MAIN_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "parent":
		// Starts a grandchild, records its id, and waits.
		child := exec.Command(os.Args[0], "-test.run=^$")
		child.Env = append(os.Environ(), helperEnv+"=sleep")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		os.WriteFile(os.Getenv("ADANEXT_MAIN_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A terminate request to the binary itself cancels the command: its whole tree dies, and the
// binary exits 130.
func TestATerminateRequestKillsTheTreeAndExits130(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the binary refuses to run as root")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "adanext-exec")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	pidFile := filepath.Join(dir, "pid")
	cmd := exec.Command(bin, "run", "--audit-dir", filepath.Join(dir, "audit"), "--", os.Args[0], "-test.run=^$")
	// A home of its own, so no policy of the user's is read.
	cmd.Env = append(os.Environ(), helperEnv+"=parent", "ADANEXT_MAIN_PID_FILE="+pidFile,
		"HOME="+dir, "XDG_CONFIG_HOME="+dir, "XDG_STATE_HOME="+dir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var pid int
	for deadline := time.Now().Add(20 * time.Second); pid == 0; {
		if time.Now().After(deadline) {
			t.Fatalf("the grandchild never started; the binary said %q", stderr.String())
		}
		if b, err := os.ReadFile(pidFile); err == nil && len(b) > 0 {
			pid, _ = strconv.Atoi(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 130 {
		t.Errorf("the binary ended with %v, want exit 130", err)
	}
	for deadline := time.Now().Add(10 * time.Second); !gone(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the grandchild %d outlived the binary", pid)
		}
	}
}

// gone reports whether a process no longer runs: no such process, or a zombie no one reaped.
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return true
		}
		if i := strings.LastIndexByte(string(b), ')'); i > 0 && i+2 < len(b) {
			return b[i+2] == 'Z'
		}
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err != nil || strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
