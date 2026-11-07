package tree

import "errors"

// Ceiling is how a memory ceiling is held where there is a choice (Linux).
type Ceiling int

const (
	// CeilingAuto holds the ceiling in a cgroup when one can be made, and watches it otherwise.
	CeilingAuto Ceiling = iota
	// CeilingCgroup holds it in a cgroup, or does not start the command (ErrNoHardCeiling).
	CeilingCgroup
	// CeilingWatch only watches it, as M5b does, and never touches cgroups.
	CeilingWatch
)

// ErrNoHardCeiling means a hard memory ceiling was asked for and none can be held here, so the
// command was not started.
var ErrNoHardCeiling = errors.New("no hard memory ceiling can be held here")
