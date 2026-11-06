package broker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cristiangirlea/adanext-exec/internal/daemon"
)

// Slots is where a command gets its heavy-command slot: the daemon (adanext-exec serve) that
// shares them among every client on the machine. A slot is held for as long as the command
// runs, and given back when it ends, however the caller ends.
type Slots struct {
	// Socket is the daemon's socket; empty means DefaultSocket.
	Socket string
	// MaxWait bounds the wait for a slot; zero means as long as the context allows.
	MaxWait time.Duration
	// Waiting, when set, is told the command's place in the queue whenever it changes.
	Waiting func(position int)
}

// ErrNoDaemon means the command asked for a slot and no daemon answered, so it did not run.
var ErrNoDaemon = daemon.ErrNoDaemon

// ErrSlotLost means the daemon let go of the command's slot, by ending or closing the
// connection; the command was killed with its tree, or never started.
var ErrSlotLost = daemon.ErrSlotLost

// ErrNoSlot means the daemon refused the command a slot, or gave none within MaxWait, so it
// did not run; the error says why.
var ErrNoSlot = errors.New("no heavy-command slot")

// DefaultSocket is where the daemon listens unless told otherwise: in the user's state
// directory (see DefaultAuditDir), adanext-exec/daemon/daemon.sock, a directory of its own,
// which the daemon makes owner-only.
func DefaultSocket() (string, error) {
	dir, err := DefaultAuditDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dir), "daemon", "daemon.sock"), nil
}

func (s *Slots) acquire(ctx context.Context, c Command) (*daemon.Lease, error) {
	socket := s.Socket
	if socket == "" {
		var err error
		if socket, err = DefaultSocket(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoDaemon, err)
		}
	}
	m := daemon.Message{
		Version:   daemon.Version,
		Args:      c.Args,
		Shell:     string(c.Shell),
		Script:    c.Script,
		MaxWaitMS: waitMS(s.MaxWait),
	}
	if c.Caller != (Caller{}) {
		m.Caller = &daemon.Caller{Agent: c.Caller.Agent, Session: c.Caller.Session, RunID: c.Caller.RunID}
	}
	lease, err := daemon.Acquire(ctx, socket, m, s.Waiting)
	switch {
	case errors.Is(err, daemon.ErrRefusedByDaemon), errors.Is(err, daemon.ErrProtocol):
		return nil, fmt.Errorf("%w: %v", ErrNoSlot, err)
	}
	return lease, err
}

// withHints adds the daemon's KEY=VALUE hints to a command's environment, the caller's own
// environment when it has none; a variable already set keeps its value.
func withHints(env, hints []string) []string {
	if len(hints) == 0 {
		return env
	}
	if env == nil {
		env = environ()
	}
	out := append([]string(nil), env...)
	for _, h := range hints {
		k, _, _ := strings.Cut(h, "=")
		if !hasVar(out, k) {
			out = append(out, h)
		}
	}
	return out
}

// waitMS is a wait bound in whole milliseconds, rounded up, so a bound under a millisecond
// stays a bound and never becomes "no limit".
func waitMS(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	ms := int64(d / time.Millisecond)
	if d%time.Millisecond != 0 {
		ms++
	}
	return ms
}
