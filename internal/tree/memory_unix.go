//go:build linux || darwin

package tree

import "os/exec"

// StartLimited is Start with a ceiling on the memory of the whole tree, in bytes. Linux and macOS
// do not enforce it yet (MemoryEnforced reports so): the command starts as with Start.
func StartLimited(cmd *exec.Cmd, memoryLimit uint64) (*Tree, error) { return Start(cmd) }

// MemoryEnforced reports whether a memory ceiling is enforced here: not yet on Linux and macOS.
func (t *Tree) MemoryEnforced() bool { return false }

// FinalMemory is the tree's memory once the command has ended; nothing is watched here yet.
func (t *Tree) FinalMemory() (exceeded bool, peak uint64, err error) { return false, 0, nil }
