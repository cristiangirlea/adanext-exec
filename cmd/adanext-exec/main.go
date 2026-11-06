// Command adanext-exec is the gate agent shell commands go through: see docs/design.md.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/cristiangirlea/adanext-exec/internal/cli"
)

func main() {
	// Ctrl+C or a terminate request cancels the command, whose tree is then killed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
