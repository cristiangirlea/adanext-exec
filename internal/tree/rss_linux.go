//go:build linux

package tree

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
)

// residentBytes is a process's resident memory, from /proc/<pid>/statm (its second field, in
// pages).
func residentBytes(pid int) (uint64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0, err
	}
	f := bytes.Fields(b)
	if len(f) < 2 {
		return 0, fmt.Errorf("statm of %d: %q", pid, b)
	}
	pages, err := strconv.ParseUint(string(f[1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("statm of %d: %v", pid, err)
	}
	return pages * uint64(os.Getpagesize()), nil
}
