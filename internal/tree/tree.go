// Package tree starts a command so that everything it starts can be killed together: a Job
// Object on Windows, a new process group on Linux and macOS.
//
// The order of calls is Start, WaitExit, Kill, then the caller's exec.Cmd.Wait, then Close.
// WaitExit returns once the command's own process has exited without reaping it on Unix, so
// its process group id cannot be reused by another program before Kill sends the signal.
package tree

import "errors"

// ErrNotContained means the command could not be placed where its whole tree can be killed. It
// was stopped before it ran, never left running uncontained.
var ErrNotContained = errors.New("the command could not be contained")

// ErrUnsupported means this platform has no way here to hold a process tree.
var ErrUnsupported = errors.New("process trees are not supported on this platform")
