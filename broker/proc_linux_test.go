//go:build linux

package broker

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// processState says whether pid runs and when it started (start ticks), so a reused pid is not
// taken for the same process. A zombie does not run.
func processState(pid int) (born string, alive bool, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return "", false, nil // gone, also while the read was under way
	}
	if err != nil {
		return "", false, err
	}
	fields := strings.Fields(string(data)[strings.LastIndexByte(string(data), ')')+1:])
	if len(fields) < 20 {
		return "", false, fmt.Errorf("short /proc stat for %d", pid)
	}
	return fields[19], fields[0] != "Z", nil
}

func callerDeath() (leaderDies, childDies bool, why string) {
	return true, false, "Linux: the command's own process dies with its caller (parent-death signal); its descendants stay"
}
