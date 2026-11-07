//go:build darwin

package tree

import (
	"fmt"
	"os/exec"
)

// cgroup is Linux's; macOS has none, so a Tree never holds one.
type cgroup struct{}

func (*cgroup) kill() error                  { return nil }
func (*cgroup) final() (bool, uint64, error) { return false, 0, nil }
func (*cgroup) remove()                      {}

// StartCeiling is StartLimited: macOS has no hard ceiling, so CeilingCgroup does not start the
// command (ErrNoHardCeiling), and the other modes watch the ceiling.
func StartCeiling(cmd *exec.Cmd, memoryLimit uint64, mode Ceiling) (*Tree, error) {
	if memoryLimit > 0 && mode == CeilingCgroup {
		return nil, fmt.Errorf("%w: macOS has no cgroups", ErrNoHardCeiling)
	}
	return StartLimited(cmd, memoryLimit)
}
