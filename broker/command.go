package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/cristiangirlea/adanext-exec/internal/tree"
)

// Command is one command for Run. It is either an argv (Args), run directly with no shell, or
// shell text (Script) read by a named Shell. Exactly one of the two is set.
type Command struct {
	// Args is the program and its arguments. The program is looked up in PATH when it has no
	// path separator. On Windows a batch file (.bat, .cmd) or cmd.exe is refused here, since
	// cmd.exe parses its arguments again and they cannot be quoted safely: run it as shell text
	// with ShellCmd instead.
	Args []string

	// Shell names the shell that reads Script.
	Shell Shell
	// Script is shell text, passed to Shell unchanged. Quoting inside it is the caller's.
	Script string

	// Dir is the working directory; empty means the caller's. It is not confined.
	Dir string
	// Env is the environment, as KEY=value; nil means the caller's environment, passed through
	// unchanged.
	Env []string
	// Stdin is the command's input; nil means none (the null device).
	Stdin io.Reader
	// Stdout and Stderr receive the command's output; nil discards it. When both are the same
	// comparable value, one stream carries both and Write is never called concurrently.
	Stdout, Stderr io.Writer

	// Timeout stops the command and kills its whole tree once it has run this long. Zero means
	// no timeout of its own; the context passed to Run still applies.
	Timeout time.Duration

	// OutputCap bounds, in bytes, what reaches Stdout and what reaches Stderr (one stream when
	// both are the same writer); zero means no cap. Its first two thirds pass through as the
	// command writes them; the rest of the output is held, and once the command has ended its
	// last third is written after a line that counts the bytes left out. Memory stays bounded by
	// the cap whatever the command writes.
	OutputCap int

	// Caller says who asked, for the audit; it is self-reported.
	Caller Caller
	// Audit, when set, gets one line for this command, written whether it ran or not. A
	// command whose line cannot be opened is not run (ErrAudit). The environment is never
	// written to the audit.
	Audit *Audit

	// Policy, when set, judges the command before it starts: one it refuses is not run
	// (ErrRefused), nor one it answers ask for unless Ask approves it, nor one a zero Policy
	// cannot judge (ErrPolicy). Nil judges nothing; the binary always sets one.
	Policy *Policy
	// Ask, when set, is called when the policy answers ask, before the command starts, to put
	// the command to a person: true runs it, false refuses it (a *RefusedError), and an error
	// leaves it not run. Nil means no person can be asked, and an ask refuses. Ask is never
	// called for a command the policy allows or refuses. The time it takes counts against ctx,
	// not Timeout. Its Command has its own copies of Args and Env, so changing them changes
	// nothing that runs; Stdin, Stdout and Stderr are the caller's own and are not to be used.
	Ask func(ctx context.Context, c Command, v Verdict) (bool, error)
	// MemoryLimit is a ceiling, in bytes, on the memory of the command and everything it
	// starts, together; zero means none. A tree that goes over it is stopped
	// (Result.MemoryExceeded). Where the platform cannot enforce it, the command runs without
	// it and Result.MemoryEnforced says so; docs/design.md lists the platforms.
	MemoryLimit int64

	// Slots, when set, makes Run wait for one of the daemon's heavy-command slots before the
	// command starts, and hold it until the command has ended. Nil shares no limits.
	Slots *Slots
}

// Shell names a shell that reads a Command's Script.
type Shell string

// The shells Run knows. Each is found in a fixed place or in PATH, as its comment says.
const (
	// ShellSh is /bin/sh -c (Linux and macOS).
	ShellSh Shell = "sh"
	// ShellBash is bash -c from PATH; on Windows Git Bash, never System32's bash.exe, which
	// starts WSL.
	ShellBash Shell = "bash"
	// ShellPowerShell is Windows PowerShell (System32's powershell.exe -NoLogo -NoProfile
	// -NonInteractive -Command), Windows only.
	ShellPowerShell Shell = "powershell"
	// ShellPwsh is PowerShell 7 (pwsh from PATH, with the same flags).
	ShellPwsh Shell = "pwsh"
	// ShellCmd is System32's cmd.exe /d /s /c, with Script as the rest of its command line,
	// Windows only.
	ShellCmd Shell = "cmd"
)

// ErrInvalid means the Command is not one Run can run as given; the error says why.
var ErrInvalid = errors.New("invalid command")

// ErrNotContained means the command could not be placed where its whole tree can be killed, so
// it was not run.
var ErrNotContained = tree.ErrNotContained

// ErrUnsupported means this platform cannot hold a process tree, so nothing is run here.
var ErrUnsupported = tree.ErrUnsupported

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// cmd checks the Command's shape and builds the process it describes.
func (c Command) cmd() (*exec.Cmd, error) {
	if c.Timeout < 0 {
		return nil, invalid("a negative timeout")
	}
	if c.OutputCap < 0 {
		return nil, invalid("a negative output cap")
	}
	if c.MemoryLimit < 0 {
		return nil, invalid("a negative memory limit")
	}
	var cmd *exec.Cmd
	switch {
	case len(c.Args) > 0 && (c.Shell != "" || c.Script != ""):
		return nil, invalid("both Args and a Shell or Script; give one")
	case len(c.Args) > 0:
		if c.Args[0] == "" {
			return nil, invalid("an empty program name")
		}
		cmd = exec.Command(c.Args[0], c.Args[1:]...)
		if cmd.Err != nil {
			return nil, cmd.Err
		}
		if err := checkProgram(cmd.Path); err != nil {
			return nil, err
		}
	case c.Shell == "" && c.Script == "":
		return nil, invalid("no Args and no Script")
	case c.Shell == "":
		return nil, invalid("a Script without a Shell")
	case c.Script == "":
		return nil, invalid("a Shell without a Script")
	default:
		var err error
		if cmd, err = shellCmd(c.Shell, c.Script); err != nil {
			return nil, err
		}
	}
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdin = c.Stdin
	return cmd, nil
}
