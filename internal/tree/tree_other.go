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

// StartLimited refuses, as Start does.
func StartLimited(cmd *exec.Cmd, memoryLimit uint64) (*Tree, error) { return nil, ErrUnsupported }

// MemoryEnforced is never reached on this platform.
func (t *Tree) MemoryEnforced() bool { return false }

// FinalMemory is never reached on this platform.
func (t *Tree) FinalMemory() (exceeded bool, peak uint64, err error) { return false, 0, nil }
