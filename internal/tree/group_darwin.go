//go:build darwin

package tree

import (
	"encoding/binary"
	"errors"
	"syscall"
)

// The layout of struct kinfo_proc on 64-bit macOS (amd64 and arm64), from <sys/sysctl.h> and
// <sys/proc.h>.
const (
	kinfoProcSize = 648
	kinfoStat     = 36  // kp_proc.p_stat
	kinfoPGID     = 564 // kp_eproc.e_pgid
	stateZombie   = 5   // SZOMB
)

// groupHasLiveMembers reports whether a process that is not a zombie is in the group. When the
// process table cannot be read, it says yes, so the refusal is reported.
func groupHasLiveMembers(pgid int) bool {
	var raw string
	var err error
	for i := 0; i < 5; i++ {
		// The table can grow between sysctl's size query and its read.
		if raw, err = syscall.Sysctl("kern.proc.all"); !errors.Is(err, syscall.ENOMEM) {
			break
		}
	}
	if err != nil {
		return true
	}
	b := []byte(raw)
	if r := len(b) % kinfoProcSize; r != 0 {
		b = append(b, make([]byte, kinfoProcSize-r)...) // Sysctl drops a final zero byte
	}
	for off := 0; off+kinfoProcSize <= len(b); off += kinfoProcSize {
		k := b[off : off+kinfoProcSize]
		if int(int32(binary.LittleEndian.Uint32(k[kinfoPGID:]))) == pgid && k[kinfoStat] != stateZombie {
			return true
		}
	}
	return false
}
