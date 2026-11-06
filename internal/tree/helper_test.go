//go:build !windows

package tree

import (
	"os"
	"syscall"
	"time"
)

// take takes and touches mb MiB, and keeps it.
func take(mb int) [][]byte {
	var held [][]byte
	for range mb {
		b := make([]byte, 1<<20)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		held = append(held, b)
	}
	return held
}

func helperPlatform(mode string) {
	switch mode {
	case "escape":
		// Starts a middle process that leaves a grower outside the group, orphaned, and waits.
		if err := helperCmd("middle").Run(); err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	case "middle":
		child := helperCmd("grow")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "pair":
		// It and a child each take 64 MiB: under a 96 MiB ceiling apart, over it together.
		if err := helperCmd("half").Start(); err != nil {
			os.Exit(2)
		}
		held := take(64)
		time.Sleep(time.Minute)
		_ = held
		os.Exit(0)
	case "half":
		held := take(64)
		time.Sleep(time.Minute)
		_ = held
		os.Exit(0)
	}
}
