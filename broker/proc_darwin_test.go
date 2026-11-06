//go:build darwin

package broker

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// The layout of struct kinfo_proc on 64-bit macOS (amd64 and arm64).
const (
	kinfoProcSize   = 648
	kinfoStartTime  = 0  // kp_proc.p_starttime: tv_sec int64, tv_usec int32
	kinfoStat       = 36 // kp_proc.p_stat
	kinfoPID        = 40 // kp_proc.p_pid
	processIsZombie = 5  // SZOMB
)

// processState says whether pid runs and when it started, from kern.proc.all, so a reused pid
// is not taken for the same process. A zombie does not run.
func processState(pid int) (born string, alive bool, err error) {
	var raw string
	for i := 0; i < 5; i++ {
		if raw, err = syscall.Sysctl("kern.proc.all"); err != syscall.ENOMEM {
			break
		}
	}
	if err != nil {
		return "", false, fmt.Errorf("kern.proc.all: %w", err)
	}
	b := []byte(raw)
	if r := len(b) % kinfoProcSize; r != 0 {
		b = append(b, make([]byte, kinfoProcSize-r)...) // Sysctl drops a final zero byte
	}
	for off := 0; off+kinfoProcSize <= len(b); off += kinfoProcSize {
		p := b[off : off+kinfoProcSize]
		if int32(binary.LittleEndian.Uint32(p[kinfoPID:])) != int32(pid) {
			continue
		}
		sec := int64(binary.LittleEndian.Uint64(p[kinfoStartTime:]))
		usec := int32(binary.LittleEndian.Uint32(p[kinfoStartTime+8:]))
		return fmt.Sprintf("%d.%06d", sec, usec), p[kinfoStat] != processIsZombie, nil
	}
	return "", false, nil
}

func TestProcessStateReadsTheProcessTable(t *testing.T) {
	born, alive, err := processState(os.Getpid())
	if err != nil || !alive || born == "" || born == "0.000000" {
		t.Fatalf("this process: born %q, alive %v, err %v", born, alive, err)
	}
	cmd := helper("sleep").cmdForTest()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if _, alive, err := processState(cmd.Process.Pid); err != nil || !alive {
		t.Fatalf("a running child: alive %v, err %v", alive, err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, alive, err := processState(cmd.Process.Pid); err != nil || alive {
		t.Fatalf("a reaped child: alive %v, err %v", alive, err)
	}
}

func callerDeath() (leaderDies, childDies bool, why string) {
	return false, false, "macOS has no parent-death signal: the tree outlives a caller killed before it can kill it"
}
