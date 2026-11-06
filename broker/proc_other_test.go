//go:build !windows && !linux && !darwin

package broker

import (
	"errors"
	"syscall"
)

// processState has no identity here; Run refuses on this platform anyway.
func processState(pid int) (born string, alive bool, err error) {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return "", false, nil
	}
	return "", true, nil
}
