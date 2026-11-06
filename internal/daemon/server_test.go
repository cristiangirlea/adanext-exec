package daemon

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const helperEnv = "ADANEXT_DAEMON_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "hold" {
		// Holds a slot on the socket named in the environment, says so, and waits.
		lease, err := Acquire(context.Background(), os.Getenv("ADANEXT_DAEMON_SOCKET"), Message{Version: Version, Args: []string{"go", "test"}}, nil)
		if err != nil {
			os.Exit(2)
		}
		os.WriteFile(os.Getenv("ADANEXT_DAEMON_MARKER"), nil, 0o600)
		time.Sleep(time.Minute)
		lease.Release()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// shortDir is a temporary directory with a short path: a Unix socket's path has a small limit.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ae")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// serve starts a daemon with the given slots and returns its socket.
func serve(t *testing.T, s *Server) string {
	t.Helper()
	socket := filepath.Join(shortDir(t), "d.sock")
	l, closeFn, err := Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
		closeFn()
	})
	return socket
}

var goTest = Message{Version: Version, Args: []string{"go", "test", "./..."}}

func acquireOK(t *testing.T, socket string, waiting func(int)) *Lease {
	t.Helper()
	l, err := Acquire(context.Background(), socket, goTest, waiting)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestALightCommandNeedsNoSlot(t *testing.T) {
	socket := serve(t, &Server{Slots: 1, Heavy: func(m Message) bool { return m.Args[0] == "go" }})
	held := acquireOK(t, socket, nil)
	defer held.Release()
	l, err := Acquire(context.Background(), socket, Message{Version: Version, Args: []string{"ls"}}, nil)
	if err != nil || !l.Light {
		t.Fatalf("ls: %+v, %v; want light even with every slot taken", l, err)
	}
}

// With one slot, the second client waits, is told its place, and gets the slot when the first
// lets it go, with the hints.
func TestTheSecondClientWaitsForTheFirstsSlot(t *testing.T) {
	socket := serve(t, &Server{Slots: 1, Hints: func(Message) []string { return []string{"GOFLAGS=-p=2"} }})
	first := acquireOK(t, socket, nil)
	if first.Slot != 1 || first.Light {
		t.Fatalf("first: %+v", first)
	}
	places := make(chan int, 4)
	got := make(chan *Lease, 1)
	go func() { got <- acquireOK(t, socket, func(p int) { places <- p }) }()
	if p := <-places; p != 1 {
		t.Fatalf("the second was told place %d, want 1", p)
	}
	select {
	case l := <-got:
		t.Fatalf("the second got %+v while the first held the slot", l)
	case <-time.After(200 * time.Millisecond):
	}
	first.Release()
	second := <-got
	defer second.Release()
	if second.Slot != 1 || second.Waited < 150*time.Millisecond || strings.Join(second.Env, " ") != "GOFLAGS=-p=2" {
		t.Errorf("second: %+v; want slot 1 after a wait, with the hint", second)
	}
}

// A client that leaves the queue moves the ones behind it up.
func TestPlacesMoveUpWhenAClientLeaves(t *testing.T) {
	socket := serve(t, &Server{Slots: 1})
	first := acquireOK(t, socket, nil)
	defer first.Release()
	ctxB, leaveB := context.WithCancel(context.Background())
	bQueued := make(chan struct{})
	go Acquire(ctxB, socket, goTest, func(int) { close(bQueued) })
	<-bQueued
	places := make(chan int, 4)
	go Acquire(context.Background(), socket, goTest, func(p int) { places <- p })
	if p := <-places; p != 2 {
		t.Fatalf("the third was told place %d, want 2", p)
	}
	leaveB()
	select {
	case p := <-places:
		if p != 1 {
			t.Errorf("after the second left, the third was told %d, want 1", p)
		}
	case <-time.After(5 * time.Second):
		t.Error("the third was never told it moved up")
	}
}

func TestAClientThatWaitsTooLongIsRefused(t *testing.T) {
	socket := serve(t, &Server{Slots: 1})
	first := acquireOK(t, socket, nil)
	defer first.Release()
	m := goTest
	m.MaxWaitMS = 200
	_, err := Acquire(context.Background(), socket, m, nil)
	if !errors.Is(err, ErrRefusedByDaemon) {
		t.Fatalf("Acquire = %v, want refused after max_wait_ms", err)
	}
	// Its place is given up: the slot goes to the next client once the first lets go.
	first.Release()
	l := acquireOK(t, socket, nil)
	l.Release()
}

// A client process that is killed gives its slot back: the lease is the connection.
func TestAKilledClientGivesItsSlotBack(t *testing.T) {
	socket := serve(t, &Server{Slots: 1})
	marker := filepath.Join(shortDir(t), "held")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"=hold", "ADANEXT_DAEMON_SOCKET="+socket, "ADANEXT_DAEMON_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never got the slot")
		}
	}
	got := make(chan *Lease, 1)
	go func() { got <- acquireOK(t, socket, nil) }()
	select {
	case l := <-got:
		t.Fatalf("got %+v while the helper held the slot", l)
	case <-time.After(200 * time.Millisecond):
	}
	cmd.Process.Kill()
	cmd.Wait()
	select {
	case l := <-got:
		l.Release()
	case <-time.After(10 * time.Second):
		t.Fatal("the killed helper's slot never came back")
	}
}

// A request the protocol does not allow, or another version, is answered with an error.
func TestTheDaemonAnswersABadRequestWithAnError(t *testing.T) {
	socket := serve(t, &Server{Slots: 1})
	for _, line := range []string{
		`{"version": "adanext-exec/2", "type": "acquire", "args": ["go"]}`,
		`{"version": "adanext-exec/1", "type": "queued", "position": 1}`,
		`{"version": "adanext-exec/1", "type": "acquire"}`,
		`not json`,
	} {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		conn.Write([]byte(line + "\n"))
		m, err := Read(bufio.NewReader(conn))
		conn.Close()
		if err != nil || m.Type != TypeError || m.Error == "" {
			t.Errorf("%s: answer %+v, %v; want an error", line, m, err)
		}
	}
}

func TestAcquireWithNoDaemon(t *testing.T) {
	_, err := Acquire(context.Background(), filepath.Join(shortDir(t), "none.sock"), goTest, nil)
	if !errors.Is(err, ErrNoDaemon) {
		t.Errorf("Acquire = %v, want ErrNoDaemon", err)
	}
}

// A caller that gives up while queued leaves the queue and gets its context's error.
func TestACallerThatGivesUpLeavesTheQueue(t *testing.T) {
	socket := serve(t, &Server{Slots: 1})
	first := acquireOK(t, socket, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, socket, goTest, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Acquire = %v, want the context's deadline", err)
	}
	first.Release()
	l := acquireOK(t, socket, nil)
	if l.Slot != 1 {
		t.Errorf("the next client got %+v", l)
	}
	l.Release()
}

func TestOnlyOneDaemonPerSocket(t *testing.T) {
	socket := filepath.Join(shortDir(t), "d.sock")
	l, closeFn, err := Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen(context.Background(), socket, nil); !errors.Is(err, ErrRunning) {
		t.Errorf("a second Listen = %v, want ErrRunning", err)
	}
	_ = l
	closeFn()
	// The first gone, its socket gone too, the next one starts.
	_, closeFn, err = Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatalf("after the first closed: %v", err)
	}
	closeFn()
}

// A socket a daemon left behind is removed under the lock, and a new daemon serves; anything
// else at the name is left alone.
func TestAStaleSocketIsReplacedAndAFileIsNot(t *testing.T) {
	socket := filepath.Join(shortDir(t), "d.sock")
	old, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	old.Close()
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("the stale socket is not there: %v", err)
	}
	_, closeFn, err := Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	closeFn()

	file := filepath.Join(shortDir(t), "f.sock")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen(context.Background(), file, nil); err == nil {
		t.Fatal("Listen took a regular file's name")
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "keep" {
		t.Errorf("the file was changed or removed: %q, %v", b, err)
	}
}

// A listener that fails ends Serve even while a client holds a slot, and the client's lease
// reads as lost.
func TestServeEndsWhenItsListenerFailsWithASlotHeld(t *testing.T) {
	socket := filepath.Join(shortDir(t), "d.sock")
	l, closeFn, err := Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	done := make(chan error, 1)
	go func() { done <- (&Server{Slots: 1}).Serve(context.Background(), l) }()
	lease := acquireOK(t, socket, nil)
	defer lease.Release()
	l.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not end with a slot held")
	}
	select {
	case <-lease.Lost():
	case <-time.After(10 * time.Second):
		t.Fatal("the lease was not lost when the daemon ended")
	}
}

// A client that never reads its answers still leaves the queue at its deadline, and the slot
// goes to the next one.
func TestAClientThatDoesNotReadLeavesAtItsDeadline(t *testing.T) {
	socket := serve(t, &Server{Slots: 1})
	holder := acquireOK(t, socket, nil)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"version": "adanext-exec/1", "type": "acquire", "args": ["go"], "max_wait_ms": 200}` + "\n"))
	time.Sleep(400 * time.Millisecond)
	holder.Release()
	got := make(chan *Lease, 1)
	go func() { got <- acquireOK(t, socket, nil) }()
	select {
	case l := <-got:
		l.Release()
	case <-time.After(5 * time.Second):
		t.Fatal("the slot went to a client past its deadline")
	}
}

// Clients get the slot in the order they asked.
func TestTheQueueIsFirstComeFirstServed(t *testing.T) {
	socket := serve(t, &Server{Slots: 1})
	holder := acquireOK(t, socket, nil)
	order := make(chan string, 3)
	for _, name := range []string{"a", "b", "c"} {
		queued := make(chan struct{})
		var once sync.Once
		go func() {
			l, err := Acquire(context.Background(), socket, goTest, func(int) { once.Do(func() { close(queued) }) })
			if err != nil {
				order <- "error"
				return
			}
			order <- name
			l.Release()
		}()
		<-queued
	}
	holder.Release()
	var got []string
	for range 3 {
		got = append(got, <-order)
	}
	if strings.Join(got, "") != "abc" {
		t.Errorf("granted in the order %v, want a, b, c", got)
	}
}

// Many clients taking and returning slots at once never hold more than there are, never lose
// one, and every one is served.
func TestSlotsHoldUnderLoad(t *testing.T) {
	const slots, clients = 3, 30
	socket := serve(t, &Server{Slots: slots})
	var held, most atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			if i%5 == 4 {
				// Some give up while they wait.
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(i)*time.Millisecond)
				defer cancel()
			}
			l, err := Acquire(ctx, socket, goTest, nil)
			if err != nil {
				if i%5 != 4 || !errors.Is(err, context.DeadlineExceeded) {
					errs <- err
				}
				return
			}
			n := held.Add(1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			time.Sleep(time.Duration(i%7) * time.Millisecond)
			held.Add(-1)
			l.Release()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if m := most.Load(); m > slots {
		t.Errorf("%d slots held at once, want at most %d", m, slots)
	}
	// Every slot came back: as many clients as slots are served at once.
	var leases []*Lease
	for range slots {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		l, err := Acquire(ctx, socket, goTest, nil)
		cancel()
		if err != nil {
			t.Fatalf("a slot was lost: %v", err)
		}
		leases = append(leases, l)
	}
	for _, l := range leases {
		l.Release()
	}
}

func TestServeWantsASlot(t *testing.T) {
	l, closeFn, err := Listen(context.Background(), filepath.Join(shortDir(t), "d.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if err := (&Server{}).Serve(context.Background(), l); err == nil {
		t.Error("Serve with no slots started")
	}
}

// The fence: a daemon does not listen while a client of an earlier one still holds a slot.
func TestListenWaitsForTheLastDaemonsClients(t *testing.T) {
	socket := filepath.Join(shortDir(t), "d.sock")
	release, busy, err := tryLock(leasesFile(socket), false)
	if err != nil || busy {
		t.Fatalf("take a client's lock: %v, %v", busy, err)
	}
	told := make(chan struct{}, 1)
	listened := make(chan func(), 1)
	go func() {
		_, closeFn, err := Listen(context.Background(), socket, func() { told <- struct{}{} })
		if err != nil {
			t.Error(err)
			listened <- func() {}
			return
		}
		listened <- closeFn
	}()
	select {
	case <-told:
	case closeFn := <-listened:
		closeFn()
		t.Fatal("Listen returned while a client still held its slot")
	case <-time.After(10 * time.Second):
		t.Fatal("Listen neither waited nor returned")
	}
	select {
	case <-listened:
		t.Fatal("Listen returned while a client still held its slot")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case closeFn := <-listened:
		closeFn()
	case <-time.After(10 * time.Second):
		t.Fatal("Listen still waited after the client let go")
	}
	// A context that ends stops the wait.
	release, _, _ = tryLock(leasesFile(socket), false)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := Listen(ctx, socket, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Listen = %v, want the context's end", err)
	}
}

// A directory that holds anything but the daemon's files is refused, and left as it was.
func TestTheSocketNeedsADirectoryOfItsOwn(t *testing.T) {
	dir := shortDir(t)
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(dir)
	if _, _, err := Listen(context.Background(), filepath.Join(dir, "d.sock"), nil); err == nil {
		t.Fatal("Listen took a directory with other files in it")
	}
	after, _ := os.Stat(dir)
	if before.Mode() != after.Mode() {
		t.Errorf("the directory's mode went from %v to %v", before.Mode(), after.Mode())
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("the other file: %v", err)
	}
}

// A client whose connection takes no writes at all still leaves the queue at its deadline,
// and the slot goes to no one it should not: a pipe blocks every write until it is read.
func TestADeadlineHoldsAgainstAClientThatTakesNoWrites(t *testing.T) {
	s := &Server{Slots: 1}
	s.init()
	holder := s.enqueue()
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		s.handle(context.Background(), server)
		close(done)
	}()
	client.Write([]byte(`{"version": "adanext-exec/1", "type": "acquire", "args": ["go"], "max_wait_ms": 200}` + string(rune(10))))
	// The handler's queued answer blocks: nothing reads it.
	time.Sleep(500 * time.Millisecond)
	s.mu.Lock()
	queued := len(s.queue)
	s.mu.Unlock()
	if queued != 0 {
		t.Fatalf("%d clients still queued past the deadline", queued)
	}
	s.leave(holder)
	s.mu.Lock()
	free := len(s.free)
	s.mu.Unlock()
	if free != 1 {
		t.Errorf("%d slots free after the holder left, want 1: it went to a client past its deadline", free)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("the handler never ended")
	}
}

// lateCtx is a context whose deadline has passed while its own timer has not yet marked it
// done: the moment the dialer's timer can fire first.
type lateCtx struct{ context.Context }

func (lateCtx) Deadline() (time.Time, bool) { return time.Now().Add(-time.Second), true }

// A deadline that passes while dialing is the caller's deadline, not a missing daemon.
func TestAnExpiredDeadlineIsNotAMissingDaemon(t *testing.T) {
	socket := serve(t, &Server{Slots: 1})
	ctx := lateCtx{context.Background()}
	if ctx.Err() != nil {
		t.Fatal("the context is already done")
	}
	if err := ctxErr(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ctxErr = %v, want the deadline", err)
	}
	if _, err := Acquire(ctx, socket, goTest, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Acquire = %v, want the deadline", err)
	}
	if err := ctxErr(context.Background()); err != nil {
		t.Errorf("ctxErr(no deadline) = %v", err)
	}
}
