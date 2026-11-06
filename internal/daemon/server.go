package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"
)

// acquireTimeout is how long a new connection has to send its acquire, and writeTimeout how
// long one answer may take to go out to a client that does not read.
const (
	acquireTimeout = 10 * time.Second
	writeTimeout   = 5 * time.Second
)

// Server shares Slots heavy-command slots among its clients. Each client holds its slot for as
// long as its connection stays open, and the rest wait in a queue.
type Server struct {
	// Slots is how many heavy commands run at once; it must be 1 or more.
	Slots int
	// Heavy reports whether a command needs a slot; nil means every command does.
	Heavy func(Message) bool
	// Hints gives the KEY=VALUE hints sent with a granted slot; nil means none.
	Hints func(Message) []string

	mu    sync.Mutex
	free  []int
	queue []*waiter
	held  map[string]int // slots held, per client
	once  sync.Once
}

// waiter is one queued client: it learns its slot on grant, and a nudge whenever its place
// in the queue may have changed.
type waiter struct {
	grant  chan int
	moved  chan struct{}
	slot   int
	client string
}

func (s *Server) init() {
	s.once.Do(func() {
		s.held = map[string]int{}
		for i := s.Slots; i >= 1; i-- {
			s.free = append(s.free, i)
		}
	})
}

// Serve accepts clients on l until ctx ends or l fails, then closes l.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	if s.Slots < 1 {
		return fmt.Errorf("daemon: %d slots, want 1 or more", s.Slots)
	}
	s.init()
	// Every way out closes the listener and every connection before waiting for the handlers,
	// so a client that holds a slot cannot keep Serve from returning.
	hctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(hctx, func() { l.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(hctx, conn)
		}()
	}
}

// handle serves one connection: one acquire, then the answers, then the lease until it closes.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	// Every answer is bounded in time, so a client that stops reading costs its handler a
	// few seconds at most, never the queue.
	send := func(m Message) error {
		conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		return Write(conn, m)
	}
	conn.SetReadDeadline(time.Now().Add(acquireTimeout))
	r := bufio.NewReader(conn)
	m, err := Read(r)
	if err != nil {
		if errors.Is(err, ErrProtocol) {
			send(Message{Type: TypeError, Error: err.Error()})
		}
		return
	}
	if m.Type != TypeAcquire {
		send(Message{Type: TypeError, Error: fmt.Sprintf("a %s message from a client; want acquire", m.Type)})
		return
	}
	conn.SetReadDeadline(time.Time{})
	if s.Heavy != nil && !s.Heavy(m) {
		send(Message{Type: TypeLight})
		return
	}
	// The client sends nothing more: the read ends when the connection closes, which is the
	// end of the lease, however the client ended.
	closed := make(chan struct{})
	go func() {
		io.Copy(io.Discard, r)
		close(closed)
	}()
	// Answers go out from a writer of their own, so a client that stops reading delays only
	// its answers, never its deadline or its place: a queued place not yet sent is replaced by
	// the newer one, and the handler goes on.
	out := make(chan Message, 1)
	writeFailed := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for msg := range out {
			if send(msg) != nil {
				close(writeFailed)
				conn.Close()
				for range out {
				}
				return
			}
		}
	}()
	defer func() {
		close(out)
		<-writerDone
	}()
	post := func(msg Message) {
		select {
		case <-out:
		default:
		}
		out <- msg
	}
	start := time.Now()
	w := s.enqueue(clientOf(m, conn))
	defer func() {
		if w != nil {
			s.leave(w)
		}
	}()
	var deadline <-chan time.Time
	if m.MaxWaitMS > 0 && m.MaxWaitMS <= maxWaitMS {
		t := time.NewTimer(time.Duration(m.MaxWaitMS) * time.Millisecond)
		defer t.Stop()
		deadline = t.C
	}
	last := 0
	for {
		if pos := s.position(w); pos > 0 && pos != last {
			last = pos
			post(Message{Type: TypeQueued, Position: pos})
		}
		select {
		case slot := <-w.grant:
			var hints []string
			if s.Hints != nil {
				hints = s.Hints(m)
			}
			post(Message{Type: TypeGranted, Slot: slot, WaitedMS: time.Since(start).Milliseconds(), Env: hints})
			select {
			case <-closed:
			case <-writeFailed:
			}
			return
		case <-w.moved:
		case <-closed:
			return
		case <-writeFailed:
			return
		case <-deadline:
			// Out of the queue first: the error may be slow to go out, the place is not held.
			s.leave(w)
			w = nil
			post(Message{Type: TypeError, Error: fmt.Sprintf("no slot within max_wait_ms (%d)", m.MaxWaitMS)})
			return
		}
	}
}

// maxWaitMS is the longest wait a timer can hold; a longer one is no limit at all.
const maxWaitMS = int64(math.MaxInt64 / int64(time.Millisecond))

// clientOf names the client a request comes from, for its fair share: the agent and session it
// gives, or, when it gives neither, its own connection.
func clientOf(m Message, conn net.Conn) string {
	if m.Caller != nil && (m.Caller.Agent != "" || m.Caller.Session != "") {
		return "caller " + strconv.Quote(m.Caller.Agent) + " " + strconv.Quote(m.Caller.Session)
	}
	return fmt.Sprintf("connection %p", conn)
}

// enqueue puts a client in the queue and hands out whatever slots are free.
func (s *Server) enqueue(client string) *waiter {
	w := &waiter{grant: make(chan int, 1), moved: make(chan struct{}, 1), client: client}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, w)
	s.dispatch()
	return w
}

// leave takes a client out: its slot goes back if it had one, or its place if it was waiting.
func (s *Server) leave(w *waiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.slot > 0 {
		s.free = append(s.free, w.slot)
		w.slot = 0
		if s.held[w.client]--; s.held[w.client] <= 0 {
			delete(s.held, w.client)
		}
	} else {
		for i, q := range s.queue {
			if q == w {
				s.queue = append(s.queue[:i], s.queue[i+1:]...)
				break
			}
		}
	}
	s.dispatch()
}

// dispatch grants free slots in fair order and nudges everyone still waiting. s.mu is held.
func (s *Server) dispatch() {
	for len(s.free) > 0 && len(s.queue) > 0 {
		slot := s.free[len(s.free)-1]
		s.free = s.free[:len(s.free)-1]
		i := s.next()
		w := s.queue[i]
		s.queue = append(s.queue[:i], s.queue[i+1:]...)
		w.slot = slot
		s.held[w.client]++
		w.grant <- slot
	}
	for _, q := range s.queue {
		select {
		case q.moved <- struct{}{}:
		default:
		}
	}
}

// before reports whether queue[i] gets a slot before queue[j]: a client holding fewer slots
// goes first, and among equals the one that asked first. s.mu is held.
func (s *Server) before(i, j int) bool {
	hi, hj := s.held[s.queue[i].client], s.held[s.queue[j].client]
	return hi < hj || hi == hj && i < j
}

// next is the queue index the next slot goes to; the queue is not empty. s.mu is held.
func (s *Server) next() int {
	best := 0
	for i := 1; i < len(s.queue); i++ {
		if s.before(i, best) {
			best = i
		}
	}
	return best
}

// position is a waiting client's place, 1 being next, or 0 once it has a slot.
func (s *Server) position(w *waiter) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := slices.Index(s.queue, w)
	if at < 0 {
		return 0
	}
	place := 1
	for i := range s.queue {
		if i != at && s.before(i, at) {
			place++
		}
	}
	return place
}
