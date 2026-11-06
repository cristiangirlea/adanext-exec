//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func elevated() bool { return os.Geteuid() == 0 }

// signalNumber finds the number of a signal by the name the broker reports, or 0.
func signalNumber(name string) int {
	for n := 1; n < 65; n++ {
		if syscall.Signal(n).String() == name {
			return n
		}
	}
	return 0
}
