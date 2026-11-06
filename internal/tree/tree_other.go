//go:build !windows && !linux && !darwin

package tree

import "os/exec"

// Tree is not available on this platform.
type Tree struct{}

// Start refuses: this platform has no tree implementation, and a command that cannot be
// contained is not run.
func Start(cmd *exec.Cmd) (*Tree, error) { return nil, ErrUnsupported }

// WaitExit is never reached on this platform.
func (t *Tree) WaitExit() error { return ErrUnsupported }

// Kill is never reached on this platform.
func (t *Tree) Kill() error { return ErrUnsupported }

// Close is never reached on this platform.
func (t *Tree) Close() {}
