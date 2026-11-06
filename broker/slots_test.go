package broker

import (
	"bufio"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cristiangirlea/adanext-exec/internal/daemon"
)

// hold runs c in the background until the test ends, then stops it and waits for it, so no
// process outlives the test.
func hold(t *testing.T, c Command) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, c)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// daemonFor serves slots on a short socket path for one test.
func daemonFor(t *testing.T, s *daemon.Server) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ae")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	l, closeFn, err := daemon.Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		<-done
		closeFn()
	})
	return socket
}

// Two commands sharing one slot run one after the other: the second waits for the first's
// whole run, and says how long.
func TestCommandsSharingASlotTakeTurns(t *testing.T) {
	socket := daemonFor(t, &daemon.Server{Slots: 1})
	first := helper("sleep")
	first.Timeout = 600 * time.Millisecond
	first.Slots = &Slots{Socket: socket}
	firstDone := make(chan time.Time, 1)
	go func() {
		Run(context.Background(), first)
		firstDone <- time.Now()
	}()
	// The first holds the slot before the second asks.
	time.Sleep(200 * time.Millisecond)
	var places []int
	var mu sync.Mutex
	second := helper("exit")
	second.Slots = &Slots{Socket: socket, Waiting: func(p int) { mu.Lock(); places = append(places, p); mu.Unlock() }}
	res, err := Run(context.Background(), second)
	secondDone := time.Now()
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("second: %+v, %v", res, err)
	}
	if end := <-firstDone; secondDone.Before(end) {
		t.Errorf("the second ended before the first")
	}
	if res.Waited < 200*time.Millisecond {
		t.Errorf("the second waited %v, want the first's run", res.Waited)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(places) == 0 || places[0] != 1 {
		t.Errorf("the second was told places %v, want 1", places)
	}
}

func TestACommandAskingForASlotWithNoDaemonDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
	c.Slots = &Slots{Socket: filepath.Join(dir, "none.sock")}
	c.Audit = &Audit{Dir: t.TempDir()}
	res, err := Run(context.Background(), c)
	if !errors.Is(err, ErrNoDaemon) || res != (Result{}) {
		t.Fatalf("Run = %+v, %v; want ErrNoDaemon and nothing run", res, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran")
	}
	lines := readAudit(t, c.Audit.Dir)
	if len(lines) != 1 || lines[0]["outcome"] != "not run" {
		t.Errorf("audit %v, want one line, not run", lines)
	}
}

func TestNoSlotInTimeDoesNotRun(t *testing.T) {
	socket := daemonFor(t, &daemon.Server{Slots: 1})
	holder := helper("sleep")
	holder.Timeout = 2 * time.Second
	holder.Slots = &Slots{Socket: socket}
	hold(t, holder)
	time.Sleep(200 * time.Millisecond)
	marker := filepath.Join(t.TempDir(), "ran")
	c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
	c.Slots = &Slots{Socket: socket, MaxWait: 200 * time.Millisecond}
	if _, err := Run(context.Background(), c); !errors.Is(err, ErrNoSlot) {
		t.Errorf("Run = %v, want ErrNoSlot", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran")
	}
}

// The daemon's hints reach the command, but never over a variable the caller set.
func TestTheDaemonsHintsReachTheCommand(t *testing.T) {
	socket := daemonFor(t, &daemon.Server{Slots: 1, Hints: func(daemon.Message) []string {
		return []string{"ADANEXT_TEST_HINT=from-daemon", "ADANEXT_TEST_KEEP=from-daemon"}
	}})
	var out strings.Builder
	c := helper("env", "ADANEXT_TEST_KEEP=mine")
	c.Stdout = &out
	c.Slots = &Slots{Socket: socket}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "ADANEXT_TEST_HINT=from-daemon") || !strings.Contains(got, "ADANEXT_TEST_KEEP=mine") || strings.Contains(got, "ADANEXT_TEST_KEEP=from-daemon") {
		t.Errorf("the command's environment:\n%s", got)
	}
}

func TestTheAuditRecordsTheWait(t *testing.T) {
	socket := daemonFor(t, &daemon.Server{Slots: 1})
	holder := helper("sleep")
	holder.Timeout = 500 * time.Millisecond
	holder.Slots = &Slots{Socket: socket}
	hold(t, holder)
	time.Sleep(150 * time.Millisecond)
	c := helper("exit")
	c.Slots = &Slots{Socket: socket}
	c.Audit = &Audit{Dir: t.TempDir()}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	lines := readAudit(t, c.Audit.Dir)
	if wait, _ := lines[0]["queue_wait_ms"].(float64); len(lines) != 1 || wait < 100 {
		t.Errorf("audit %v, want the wait", lines)
	}
}

// The socket has a directory of its own, beside the audit's, since the daemon makes it
// owner-only and would otherwise change who can reach the audit.
func TestDefaultSocketHasADirectoryOfItsOwn(t *testing.T) {
	socket, err := DefaultSocket()
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := DefaultAuditDir()
	if filepath.Dir(filepath.Dir(socket)) != filepath.Dir(audit) || filepath.Base(filepath.Dir(socket)) != "daemon" || filepath.Base(socket) != "daemon.sock" {
		t.Errorf("DefaultSocket = %s, audit %s", socket, audit)
	}
}

// fakeDaemon grants every acquire, then closes the connection after hold: a daemon that goes
// away while a command holds its slot.
func fakeDaemon(t *testing.T, hold time.Duration) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ae")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	l, closeFn, err := daemon.Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := daemon.Read(bufio.NewReader(conn)); err != nil {
					return
				}
				daemon.Write(conn, daemon.Message{Type: daemon.TypeGranted, Slot: 1})
				time.Sleep(hold)
			}()
		}
	}()
	return socket
}

// A daemon that lets go of the slot while the command runs takes the command with it.
func TestALostSlotKillsTheCommand(t *testing.T) {
	socket := fakeDaemon(t, 300*time.Millisecond)
	c := helper("sleep")
	c.Timeout = time.Minute
	c.Slots = &Slots{Socket: socket}
	start := time.Now()
	res, err := Run(context.Background(), c)
	if !errors.Is(err, ErrSlotLost) || !res.Canceled {
		t.Fatalf("Run = %+v, %v; want the command cancelled with ErrSlotLost", res, err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("Run took %v after the slot was lost", took)
	}
	// A daemon started again on the same socket serves the next command.
	again := daemonFor(t, &daemon.Server{Slots: 1})
	c2 := helper("exit")
	c2.Slots = &Slots{Socket: again}
	if _, err := Run(context.Background(), c2); err != nil {
		t.Errorf("after a new daemon: %v", err)
	}
}

// A slot lost as soon as it is granted never leaves a command running without it.
func TestASlotLostAtOnceRunsNothingWithoutIt(t *testing.T) {
	socket := fakeDaemon(t, 0)
	for range 5 {
		c := helper("sleep")
		c.Timeout = time.Minute
		c.Slots = &Slots{Socket: socket}
		start := time.Now()
		res, err := Run(context.Background(), c)
		if !errors.Is(err, ErrSlotLost) {
			t.Fatalf("Run = %+v, %v; want ErrSlotLost", res, err)
		}
		if time.Since(start) > 20*time.Second {
			t.Fatal("the command ran on without its slot")
		}
	}
}

func TestAWaitBoundUnderAMillisecondStaysABound(t *testing.T) {
	for d, want := range map[time.Duration]int64{0: 0, -time.Second: 0, time.Microsecond: 1, 500 * time.Microsecond: 1, time.Millisecond: 1, 1500 * time.Microsecond: 2, time.Second: 1000, time.Duration(math.MaxInt64): math.MaxInt64/int64(time.Millisecond) + 1} {
		if got := waitMS(d); got != want {
			t.Errorf("waitMS(%v) = %d, want %d", d, got, want)
		}
	}
}

// The slot goes back only once the command's whole tree is dead: the next holder never runs
// beside what the last one left behind.
func TestTheNextHolderStartsAfterTheTreeIsDead(t *testing.T) {
	socket := daemonFor(t, &daemon.Server{Slots: 1})
	file := filepath.Join(t.TempDir(), "id.json")
	first := helper("orphaner", "ADANEXT_TEST_PID_FILE="+file)
	first.Timeout = time.Minute
	first.Slots = &Slots{Socket: socket}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() {
		_, err := Run(ctx, first)
		ran <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ran:
		case <-time.After(30 * time.Second):
		}
	})
	id := readIdentity(t, file)
	lease, err := daemon.Acquire(context.Background(), socket, daemon.Message{Version: daemon.Version, Args: []string{"go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	// Granted: the grandchild the first command left must already be gone.
	born, alive, err := processState(id.PID)
	if err != nil {
		t.Fatal(err)
	}
	if alive && born == id.Born {
		t.Errorf("the slot came back while the grandchild %d still ran", id.PID)
	}
	if err := <-ran; err != nil {
		t.Errorf("the first command: %v", err)
	}
	ran <- nil
}

// A slot lost between the grant and the start: the command never starts.
func TestASlotLostBeforeTheStartRunsNothing(t *testing.T) {
	socket := fakeDaemon(t, 0)
	// The slot is surely gone before Run goes on to the start.
	slotGranted = func(lost <-chan struct{}) {
		select {
		case <-lost:
		case <-time.After(10 * time.Second):
			t.Error("the fake daemon never let go of the slot")
		}
	}
	defer func() { slotGranted = func(<-chan struct{}) {} }()
	marker := filepath.Join(t.TempDir(), "ran")
	c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
	c.Slots = &Slots{Socket: socket}
	res, err := Run(context.Background(), c)
	if !errors.Is(err, ErrSlotLost) || res != (Result{}) {
		t.Fatalf("Run = %+v, %v; want ErrSlotLost and nothing run", res, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command started without its slot")
	}
}

// A caller that gives up as the daemon goes gets its own error, never ErrSlotLost.
func TestTheCallersOwnEndIsNotASlotLost(t *testing.T) {
	socket := fakeDaemon(t, 200*time.Millisecond)
	for range 8 {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		c := helper("sleep")
		c.Slots = &Slots{Socket: socket}
		_, err := Run(ctx, c)
		if ctx.Err() != nil && errors.Is(err, ErrSlotLost) {
			t.Errorf("the caller's deadline reported as a lost slot: %v", err)
		}
		cancel()
	}
}

// A daemon restarted on the same socket waits for the commands of the one before: it serves
// only once their trees are dead.
func TestARestartedDaemonWaitsForTheLastOnesCommands(t *testing.T) {
	dir, err := os.MkdirTemp("", "ae")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	l, closeA, err := daemon.Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctxA, stopA := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		(&daemon.Server{Slots: 1}).Serve(ctxA, l)
		close(served)
	}()
	var stopOnce sync.Once
	stopDaemonA := func() {
		stopOnce.Do(func() {
			stopA()
			<-served
			closeA()
		})
	}
	t.Cleanup(stopDaemonA)
	file := filepath.Join(t.TempDir(), "id.json")
	c := helper("parent", "ADANEXT_TEST_PID_FILE="+file)
	c.Timeout = time.Minute
	c.Slots = &Slots{Socket: socket}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		Run(ctx, c)
		close(ran)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ran:
		case <-time.After(30 * time.Second):
		}
	})
	id := readIdentity(t, file)
	stopDaemonA()
	_, closeB, err := daemon.Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeB()
	// Listening, the new daemon may grant: the old command's tree must already be dead.
	born, alive, err := processState(id.PID)
	if err != nil {
		t.Fatal(err)
	}
	if alive && born == id.Born {
		t.Errorf("the new daemon listened while the old command's grandchild %d still ran", id.PID)
	}
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Error("the old command's Run never returned")
	}
}
