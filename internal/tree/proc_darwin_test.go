//go:build darwin

package tree

import (
	"fmt"
	"syscall"
	"testing"
)

func TestTheCheckBeforeASignalTellsGoneFromUnreadable(t *testing.T) {
	old := lookUp
	t.Cleanup(func() { lookUp = old })
	p := proc{pid: 900001, start: 10}
	for name, c := range map[string]struct {
		err      error
		reported bool
	}{
		"gone":       {fmt.Errorf("process 900001: %w", errNoProc), false},
		"unreadable": {fmt.Errorf("read the process table: %w", syscall.ENOMEM), true},
	} {
		t.Run(name, func(t *testing.T) {
			lookUp = func(int) (proc, error) { return proc{}, c.err }
			if err := killProc(p); (err != nil) != c.reported {
				t.Fatalf("killProc: %v; want the error reported: %v", err, c.reported)
			}
		})
	}
}
