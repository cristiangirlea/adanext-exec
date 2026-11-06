package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// ErrNoDaemon means no daemon answers on the socket.
var ErrNoDaemon = errors.New("the daemon is not running")

// ErrRefusedByDaemon means the daemon refused the request or gave up waiting for a slot; the
// error says why.
var ErrRefusedByDaemon = errors.New("the daemon refused")

// Lease is a client's answer from the daemon: a slot, or none needed. A slot is held until
// Release.
type Lease struct {
	// Light is true when the command needs no slot.
	Light bool
	// Slot is the slot's number, when there is one.
	Slot int
	// Waited is how long the daemon queued the command.
	Waited time.Duration
	// Env holds the daemon's KEY=VALUE hints for the command's own parallelism.
	Env []string

	conn   net.Conn
	once   sync.Once
	lost   chan struct{}
	fenced func()
}

// Release gives the slot back, by closing the connection that holds it.
func (l *Lease) Release() {
	l.once.Do(func() {
		if l.conn != nil {
			l.conn.Close()
		}
		if l.fenced != nil {
			l.fenced()
		}
	})
}

// Lost is closed when the slot is no longer held: the daemon closed the connection or ended,
// or Release was called. It never closes for a light lease, which holds no slot.
func (l *Lease) Lost() <-chan struct{} { return l.lost }

// ErrSlotLost means the daemon let go of a slot while the command held it.
var ErrSlotLost = errors.New("the daemon let go of the slot")

// Acquire asks the daemon on socket for a slot for the command m (an acquire) and waits until
// the daemon grants one, says none is needed, or refuses; or until ctx ends. waiting, when
// set, is told each place in the queue.
func Acquire(ctx context.Context, socket string, m Message, waiting func(position int)) (*Lease, error) {
	m.Type = TypeAcquire
	// The command goes only to a daemon of the user's: a socket another account made could
	// be anyone's, and the request names the command.
	if err := ownedByUser(socket); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDaemon, err)
	}
	// Held from before the request until Release: a daemon that starts after this one ends
	// waits for it, so it cannot grant a slot while a command holding one of ours still runs.
	fenced, busy, err := tryLock(leasesFile(socket), false)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrNoDaemon, err)
	case busy:
		return nil, fmt.Errorf("%w: a daemon is starting", ErrNoDaemon)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		fenced()
		if err := ctxErr(ctx); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrNoDaemon, err)
	}
	// Ending ctx while waiting closes the connection, which ends the wait and leaves the queue.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	lease, err := acquire(conn, m, waiting)
	if !stop() || err != nil {
		conn.Close()
		fenced()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if lease.Light {
		fenced()
	} else {
		lease.fenced = fenced
	}
	return lease, nil
}

func acquire(conn net.Conn, m Message, waiting func(int)) (*Lease, error) {
	if err := Write(conn, m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDaemon, err)
	}
	r := bufio.NewReader(conn)
	for {
		a, err := Read(r)
		if err != nil {
			if errors.Is(err, ErrProtocol) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: the connection ended: %v", ErrNoDaemon, err)
		}
		switch a.Type {
		case TypeLight:
			conn.Close()
			return &Lease{Light: true, lost: make(chan struct{})}, nil
		case TypeQueued:
			if waiting != nil {
				waiting(a.Position)
			}
		case TypeGranted:
			l := &Lease{Slot: a.Slot, Waited: time.Duration(a.WaitedMS) * time.Millisecond, Env: a.Env, conn: conn, lost: make(chan struct{})}
			// The daemon sends nothing after a grant: any read that ends means the slot is gone.
			go func() {
				r.ReadByte()
				close(l.lost)
			}()
			return l, nil
		case TypeError:
			return nil, fmt.Errorf("%w: %s", ErrRefusedByDaemon, a.Error)
		default:
			return nil, fmt.Errorf("%w: a %s message from the daemon", ErrProtocol, a.Type)
		}
	}
}

// ctxErr is ctx's error, also for a deadline that has passed before ctx's own timer marked it
// done: the dialer's timer can fire first, and its timeout is the deadline's, not a daemon's.
func ctxErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok && !time.Now().Before(dl) {
		return context.DeadlineExceeded
	}
	return nil
}
