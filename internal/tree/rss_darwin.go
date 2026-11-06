//go:build darwin

package tree

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

// proc_info's call and flavor for a process's task information (sys/proc_info.h).
const (
	procInfoCallPIDInfo = 2
	procPIDTaskInfo     = 4
	// procTaskInfoSize is the size of struct proc_taskinfo; pti_resident_size is its second
	// field, at offset 8.
	procTaskInfoSize = 96
)

// residentBytes is a process's resident memory, from proc_info's task information.
func residentBytes(pid int) (uint64, error) {
	buf := make([]byte, procTaskInfoSize)
	n, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(pid), procPIDTaskInfo, 0, uintptr(unsafe.Pointer(&buf[0])), procTaskInfoSize)
	if errno != 0 {
		return 0, errno
	}
	if n != procTaskInfoSize {
		return 0, fmt.Errorf("proc_info of %d: %d bytes, want %d", pid, n, procTaskInfoSize)
	}
	return binary.LittleEndian.Uint64(buf[8:16]), nil
}
