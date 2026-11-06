//go:build darwin

package tree

import (
	"encoding/binary"
	"errors"
	"syscall"
	"time"
)

// The layout of struct kinfo_proc on 64-bit macOS (amd64 and arm64), from <sys/sysctl.h> and
// <sys/proc.h>.
const (
	kinfoProcSize = 648
	kinfoStat     = 36  // kp_proc.p_stat
	kinfoPGID     = 564 // kp_eproc.e_pgid
	stateZombie   = 5   // SZOMB
)

// groupHasLiveMembers reports whether a process that is not a zombie is in the group.
func groupHasLiveMembers(pgid int) bool {
	return groupHasLiveMembersIn(pgid, func() (string, error) { return syscall.Sysctl("kern.proc.all") }, 10*time.Millisecond)
}

// groupHasLiveMembersIn says no only when two reads of the table, apart, both find no live
// member: the kernel lists the pids before it reads each one, so a member that forks and exits
// within one read can hide its child from it. A table that cannot be read means yes, so the
// refusal is reported.
func groupHasLiveMembersIn(pgid int, read func() (string, error), apart time.Duration) bool {
	for i := 0; i < 2; i++ {
		if i > 0 {
			time.Sleep(apart)
		}
		live, err := liveInTable(pgid, read)
		if err != nil || live {
			return true
		}
	}
	return false
}

func liveInTable(pgid int, read func() (string, error)) (bool, error) {
	var raw string
	var err error
	for i := 0; i < 5; i++ {
		// The table can grow between sysctl's size query and its read.
		if raw, err = read(); !errors.Is(err, syscall.ENOMEM) {
			break
		}
	}
	if err != nil {
		return false, err
	}
	b := []byte(raw)
	if r := len(b) % kinfoProcSize; r != 0 {
		b = append(b, make([]byte, kinfoProcSize-r)...) // Sysctl drops a final zero byte
	}
	for off := 0; off+kinfoProcSize <= len(b); off += kinfoProcSize {
		k := b[off : off+kinfoProcSize]
		if int(int32(binary.LittleEndian.Uint32(k[kinfoPGID:]))) == pgid && k[kinfoStat] != stateZombie {
			return true, nil
		}
	}
	return false, nil
}
