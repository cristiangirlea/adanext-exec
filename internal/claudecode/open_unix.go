//go:build !windows

package claudecode

import "syscall"

// openNonBlock opens a pipe with no writer at once, so it is refused instead of hanging.
const openNonBlock = syscall.O_NONBLOCK
