package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"

	"github.com/cristiangirlea/adanext-exec/broker"
	"github.com/cristiangirlea/adanext-exec/internal/daemon"
)

// defaultSocket is where serve listens and run --slots asks unless told otherwise; tests
// replace it.
var defaultSocket = broker.DefaultSocket

// serve is "adanext-exec serve": the daemon that shares heavy-command slots, until ctx ends.
func serve(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	slots := flags.Int("slots", max(1, runtime.NumCPU()/4), "how many heavy commands run at once")
	socket := flags.String("socket", "", "the socket to listen on (default: the user's state directory)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return ExitUsage
	}
	switch {
	case flags.NArg() != 0:
		fmt.Fprintln(stderr, "adanext-exec serve: no arguments")
		return ExitUsage
	case *slots < 1:
		fmt.Fprintln(stderr, "adanext-exec serve: --slots must be 1 or more")
		return ExitUsage
	}
	path := *socket
	if path == "" {
		var err error
		if path, err = defaultSocket(); err != nil {
			fmt.Fprintf(stderr, "adanext-exec serve: %v\n", err)
			return ExitCannotRun
		}
	}
	l, closeFn, err := daemon.Listen(ctx, path, func() {
		fmt.Fprintln(stderr, "adanext-exec serve: waiting for the last daemon's commands to end")
	})
	if err != nil {
		fmt.Fprintf(stderr, "adanext-exec serve: %v\n", err)
		return ExitCannotRun
	}
	defer closeFn()
	fmt.Fprintf(stderr, "adanext-exec serve: %d slots on %s\n", *slots, path)
	if err := (&daemon.Server{Slots: *slots}).Serve(ctx, l); err != nil {
		fmt.Fprintf(stderr, "adanext-exec serve: %v\n", err)
		return ExitCannotRun
	}
	return 0
}
