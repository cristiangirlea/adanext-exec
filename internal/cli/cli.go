// Package cli is the adanext-exec binary: its subcommands, flags and exit codes. The binary's
// main only hands it the process's arguments, streams and a context that ends on Ctrl+C.
package cli

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"
)

// The exit codes the binary itself gives, documented in docs/protocol.md. Any other code is the
// command's own. A command that exits with one of these codes by itself cannot be told from the
// binary's; a caller that must tell reads the audit line.
const (
	// ExitBrokerFailed: the command ran, but something after it failed: writing its output,
	// writing its audit line, or killing what it left running.
	ExitBrokerFailed = 120
	// ExitUsage: the arguments are not valid.
	ExitUsage = 121
	// ExitMemory: the command went over its memory ceiling (--memory), and its tree was stopped.
	ExitMemory = 122
	// ExitNoDaemon: the command asked for a heavy-command slot and no daemon answered, or the
	// daemon went away while the command held its slot, and the command was killed.
	ExitNoDaemon = 123
	// ExitTimeout: the command ran past its timeout, and its tree was killed; or it waited past
	// --max-wait for a slot, or the daemon refused it one, and did not run.
	ExitTimeout = 124
	// ExitRefused: the policy refused the command, answered ask with no approval, or could not
	// be used; the command did not run.
	ExitRefused = 125
	// ExitCannotRun: the command did not run: it could not start, be contained or be audited,
	// or the binary runs elevated.
	ExitCannotRun = 126
	// ExitNotFound: the program or the shell was not found.
	ExitNotFound = 127
	// ExitSignal plus the signal's number: the command was ended by a signal (Unix).
	ExitSignal = 128
	// ExitInterrupted: the binary was interrupted (Ctrl+C), and the command's tree was killed.
	ExitInterrupted = 130
)

const usage = `usage:
  adanext-exec run [flags] -- <program> [args...]
  adanext-exec run [flags] --shell <name> -- <script>
  adanext-exec run [flags] --shell <name> --script-b64 <script as URL-safe base64>
  adanext-exec check --json [--policy FILE | --profile NAME] < request.json
  adanext-exec serve [--slots N] [--socket PATH]
  adanext-exec hook claude-code [--policy FILE | --profile NAME] < PreToolUse.json

Run "adanext-exec run -h" for the flags.
`

// Main runs the binary with its arguments (the program name left out) and returns the exit code.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "run":
		if isElevated() {
			fmt.Fprintln(stderr, "adanext-exec: "+errElevated)
			return ExitCannotRun
		}
		return run(ctx, args[1:], stdin, stdout, stderr)
	case "serve":
		if isElevated() {
			fmt.Fprintln(stderr, "adanext-exec: "+errElevated)
			return ExitCannotRun
		}
		return serve(ctx, args[1:], stderr)
	case "check":
		// check answers the refusal in its own JSON, as it answers everything else.
		return check(args[1:], stdin, stdout, stderr)
	case "hook":
		// The hook answers every failure, elevation included, as a deny Claude Code reads.
		return hook(args[1:], stdin, stdout, stderr)
	}
	fmt.Fprintf(stderr, "adanext-exec: unknown command %q\n%s", args[0], usage)
	return ExitUsage
}

// version is this binary's build, as the Go toolchain recorded it: the module's version, and
// the commit it was built from. A hook's rewrite carries it, so run refuses a rewrite written by
// another build.
func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	v := bi.Main.Version
	for _, s := range bi.Settings {
		switch {
		case s.Key == "vcs.revision":
			v += "+" + s.Value
		case s.Key == "vcs.modified" && s.Value == "true":
			v += "-modified"
		}
	}
	if v == "" {
		return "unknown"
	}
	return v
}

// isElevated reports whether the binary runs as root or an elevated administrator; tests replace it.
var isElevated = elevated

const errElevated = "refusing to run as root or an elevated administrator"
