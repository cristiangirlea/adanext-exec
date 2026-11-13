//go:build linux

package broker

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Run holds a ceiling in a cgroup end to end: a caller alone in a delegated cgroup (CI makes
// one, named by ADANEXT_EXEC_CGROUP_TEST) runs a tree that grows past its ceiling with
// CeilingCgroup; the kernel stops it, and the result and the audit say the kernel held it.
func TestRunHoldsTheCeilingInACgroup(t *testing.T) {
	base := os.Getenv("ADANEXT_EXEC_CGROUP_TEST")
	if base == "" {
		t.Skip("ADANEXT_EXEC_CGROUP_TEST names no delegated cgroup v2 cgroup: the hard ceiling is not tried here")
	}
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "b-"+hex.EncodeToString(id))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeCgroups(dir) })
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)

	audit := t.TempDir()
	cmd := helper("cgroup-run", "ADANEXT_TEST_AUDIT_DIR="+audit).cmdForTest()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the caller failed: %v\n%s", err, out.String())
		}
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("the caller ran on\n%s", out.String())
	}
	var rep cgroupReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	res := rep.Result
	if rep.Err != "" || !res.MemoryEnforced || !res.MemoryHard || !res.MemoryExceeded || res.ExitCode != -1 {
		t.Errorf("%+v, %q; want the kernel to stop the tree at its ceiling", res, rep.Err)
	}
	if !heldAtCeiling(res.PeakMemory, 128*mib) {
		t.Errorf("peak %d; the kernel holds the tree at 128 MiB", res.PeakMemory)
	}
	lines := readAudit(t, audit)
	if lines[0]["outcome"] != "over its memory ceiling" || lines[0]["memory_hard"] != true {
		t.Errorf("audit %v; want the ceiling, held by the kernel", lines[0])
	}
}

// removeCgroups removes a test's cgroup and those made in it, deepest first, once their
// processes are gone.
func removeCgroups(dir string) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				removeCgroups(filepath.Join(dir, e.Name()))
			}
		}
		if err := os.Remove(dir); err == nil || os.IsNotExist(err) || time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
