package daemon

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheDefaultClassification(t *testing.T) {
	c, err := NewClassifier(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		m     Message
		heavy bool
	}{
		{Message{Args: []string{"go", "test", "./..."}}, true},
		{Message{Args: []string{`C:\Program Files\Go\bin\go.exe`, "build", "."}}, true},
		{Message{Args: []string{"/usr/bin/cargo", "build", "--release"}}, true},
		{Message{Args: []string{"npm.cmd", "ci"}}, true},
		{Message{Args: []string{"./gradlew", "assemble"}}, true},
		{Message{Shell: "bash", Script: "cd web && pnpm run build"}, true},
		{Message{Shell: "pwsh", Script: "dotnet test src/App.Tests"}, true},
		{Message{Shell: "sh", Script: "python3 -m pytest -q"}, true},
		{Message{Shell: "sh", Script: "make -j8 all"}, true},
		{Message{Args: []string{"go", "version"}}, false},
		{Message{Args: []string{"git", "status"}}, false},
		{Message{Shell: "bash", Script: "ls -la && cat go.mod"}, false},
		{Message{Shell: "sh", Script: "echo makefile"}, false},
		{Message{Args: []string{"npm", "view", "react"}}, false},
		// Global flags before the subcommand, and their values.
		{Message{Args: []string{"go", "-C", "project", "test", "./..."}}, true},
		{Message{Args: []string{"npm", "--prefix", "web", "run", "build"}}, true},
		{Message{Args: []string{"pnpm", "--filter", "web", "test"}}, true},
		{Message{Args: []string{"cargo", "+nightly", "build"}}, true},
		{Message{Args: []string{"npm", "run", "lint"}}, false},
		// Shell text, its program by base name, with assignments and several commands.
		{Message{Shell: "pwsh", Script: `& "C:\Go\bin\go.exe" test ./...`}, true},
		{Message{Shell: "bash", Script: "go.exe test ./..."}, true},
		{Message{Shell: "bash", Script: "CGO_ENABLED=0 go build ./cmd/x"}, true},
		{Message{Shell: "bash", Script: "git pull; npm ci"}, true},
		{Message{Shell: "bash", Script: "cat log | grep -c test"}, false},
		{Message{Shell: "sh", Script: "docker compose build web"}, true},
		{Message{Shell: "sh", Script: "docker compose up -d"}, false},
		{Message{Shell: "sh", Script: "npx vitest run"}, true},
		{Message{Shell: "sh", Script: "cmake --build build -j8"}, true},
		// Arguments that name a tool are data, not the program.
		{Message{Args: []string{"git", "grep", "make"}}, false},
		{Message{Args: []string{"echo", "go test"}}, false},
		{Message{Shell: "bash", Script: `echo "go test ./..."`}, false},
		{Message{Args: []string{"rg", "pytest", "docs"}}, false},
	} {
		if got := c.Heavy(tc.m); got != tc.heavy {
			t.Errorf("Heavy(%q %q) = %v, want %v", tc.m.Args, tc.m.Script, got, tc.heavy)
		}
	}
}

func TestTheHintsShareTheCPUsAmongTheSlots(t *testing.T) {
	for _, slots := range []int{1, 2, 64 * runtime.NumCPU()} {
		k := strconv.Itoa(max(1, runtime.NumCPU()/slots))
		want := "GOFLAGS=-p=" + k + " GOMAXPROCS=" + k + " MAKEFLAGS=-j" + k + " CARGO_BUILD_JOBS=" + k
		if got := strings.Join(ParallelHints(slots)(Message{}), " "); got != want {
			t.Errorf("%d slots: %s, want %s", slots, got, want)
		}
	}
}

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAConfigAddsToOrReplacesTheDefaults(t *testing.T) {
	add, err := LoadConfig(writeConfig(t, `{"version": 1, "heavy": ["\\bterraform\\s+plan\\b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !add.Heavy(Message{Args: []string{"terraform", "plan"}}) || !add.Heavy(Message{Args: []string{"go", "test"}}) {
		t.Error("an added pattern, or a default, is not heavy")
	}
	only, err := LoadConfig(writeConfig(t, `{"version": 1, "heavy": ["\\bterraform\\b"], "only": true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !only.Heavy(Message{Args: []string{"terraform", "plan"}}) || only.Heavy(Message{Args: []string{"go", "test"}}) {
		t.Error("only: the defaults are still heavy, or the pattern is not")
	}
}

func TestAConfigThatIsNotValidIsRefused(t *testing.T) {
	for name, text := range map[string]string{
		"not JSON":         `{`,
		"no version":       `{"heavy": []}`,
		"another version":  `{"version": 2}`,
		"an unknown field": `{"version": 1, "light": []}`,
		"a bad pattern":    `{"version": 1, "heavy": ["("]}`,
		"a null":           `{"version": 1, "heavy": [null]}`,
		"a key twice":      `{"version": 1, "only": false, "only": true}`,
		"another case":     `{"version": 1, "only": false, "Only": true, "heavy": []}`,
		"data after it":    `{"version": 1} {}`,
		"too large":        `{"version": 1, "heavy": ["` + strings.Repeat("x", maxConfigSize) + `"]}`,
	} {
		if _, err := LoadConfig(writeConfig(t, text)); err == nil {
			t.Errorf("%s: LoadConfig took %s", name, text)
		}
	}
	for name, path := range map[string]string{"missing": filepath.Join(t.TempDir(), "none.json"), "a directory": t.TempDir()} {
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s: LoadConfig took it", name)
		}
	}
}

// A slot goes to the client holding fewer, before an older request of a client holding more.
func TestSlotsAreSharedFairlyAmongClients(t *testing.T) {
	socket := serve(t, &Server{Slots: 2})
	as := func(agent string) Message {
		m := goTest
		m.Caller = &Caller{Agent: agent}
		return m
	}
	a1, err := Acquire(context.Background(), socket, as("a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := Acquire(context.Background(), socket, as("a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Release()
	order := make(chan string, 2)
	var wg sync.WaitGroup
	for _, agent := range []string{"a", "b"} {
		queued := make(chan struct{})
		var once sync.Once
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := Acquire(context.Background(), socket, as(agent), func(int) { once.Do(func() { close(queued) }) })
			if err != nil {
				order <- "error"
				return
			}
			order <- agent
			if agent == "b" {
				l.Release()
			}
			// a's third keeps its slot until the test ends, so b's comes from a1.
			t.Cleanup(l.Release)
		}()
		<-queued
	}
	a1.Release()
	if first := <-order; first != "b" {
		t.Errorf("the freed slot went to %s, want b, which held none", first)
	}
	a2.Release()
	if second := <-order; second != "a" {
		t.Errorf("then %s, want a", second)
	}
	wg.Wait()
}

// A pattern of the configuration's own matches the whole command line, as documented.
func TestAConfiguredPatternMatchesTheWholeLine(t *testing.T) {
	c, err := NewClassifier(false, []string{`--heavy-flag`})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Heavy(Message{Args: []string{"tool", "--heavy-flag"}}) || c.Heavy(Message{Args: []string{"go", "test"}}) {
		t.Error("a pattern of its own, with no defaults, classified wrongly")
	}
}

// The configuration's size bound is exact: 1 MiB is read, one byte more is not.
func TestTheConfigSizeBoundIsExact(t *testing.T) {
	head, tail := `{"version": 1, "heavy": ["x`, `"]}`
	at := head + strings.Repeat("x", maxConfigSize-len(head)-len(tail)) + tail
	if _, err := LoadConfig(writeConfig(t, at)); err != nil {
		t.Errorf("at the bound: %v", err)
	}
	if _, err := LoadConfig(writeConfig(t, at[:len(at)-len(tail)]+"x"+tail)); err == nil {
		t.Error("one byte over the bound was read")
	}
}

// Places follow the fair order: a client holding a slot waits behind those holding none, and
// equals keep the order they asked in.
func TestPlacesFollowTheFairOrder(t *testing.T) {
	s := &Server{Slots: 1}
	s.init()
	holder := s.enqueue("x") // granted at once: x holds the one slot
	x2 := s.enqueue("x")
	y := s.enqueue("y")
	anon1 := s.enqueue("connection 1")
	anon2 := s.enqueue("connection 2")
	got := []int{s.position(y), s.position(anon1), s.position(anon2), s.position(x2)}
	if got[0] != 1 || got[1] != 2 || got[2] != 3 || got[3] != 4 {
		t.Errorf("places %v, want y 1, the anonymous ones 2 and 3, then x 4", got)
	}
	// x holds nothing once its slot is back: all are equal, and the oldest, x2, goes first.
	s.leave(holder)
	if s.position(x2) != 0 || s.position(y) != 1 {
		t.Errorf("after the holder left: x2 at %d, y at %d; want x2 granted, y next", s.position(x2), s.position(y))
	}
}

// A client is its agent and session: two sessions of one agent are two clients, and a request
// that names neither is a client of its own connection.
func TestWhoIsAClient(t *testing.T) {
	c1, c2 := &net.UnixConn{}, &net.UnixConn{}
	s1 := Message{Caller: &Caller{Agent: "a", Session: "s1"}}
	s2 := Message{Caller: &Caller{Agent: "a", Session: "s2"}}
	if clientOf(s1, c1) == clientOf(s2, c1) {
		t.Error("two sessions of one agent are one client")
	}
	if clientOf(s1, c1) != clientOf(s1, c2) {
		t.Error("one session over two connections is two clients")
	}
	if clientOf(Message{}, c1) == clientOf(Message{}, c2) {
		t.Error("two anonymous connections are one client")
	}
	if clientOf(Message{Caller: &Caller{RunID: "r"}}, c1) == clientOf(Message{Caller: &Caller{RunID: "r"}}, c2) {
		t.Error("a run id alone made two connections one client")
	}
}

// Wrappers, executors and substitutions run the command inside them; quoting follows the
// shell's own escape character.
func TestWrappersExecutorsAndSubstitutions(t *testing.T) {
	c, err := NewClassifier(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		shell, script string
		heavy         bool
	}{
		{"bash", "time go test ./...", true},
		{"bash", "env FOO=1 go test ./...", true},
		{"bash", "sudo make install", true},
		{"bash", "nice -n 10 make", true},
		{"bash", "timeout 60 go test ./...", true},
		{"bash", "find . -name x | xargs -n1 go vet", true},
		{"bash", "npx playwright test", true},
		{"bash", "bunx playwright test", true},
		{"bash", "pnpm exec tsc -p .", true},
		{"bash", "yarn exec jest", true},
		{"bash", "pnpm -w test", true},
		{"bash", "echo `go test ./...`", true},
		{"bash", "echo \"$(go test ./...)\"", true},
		{"bash", "echo \"a\\\"b\"; go test ./...", true},
		{"pwsh", "echo \"a`\"b\"; go test ./...", true},
		{"cmd", "echo hi & go test ./...", true},
		{"bash", "echo \"go test\"", false},
		{"bash", "env | grep go", false},
		{"bash", "time ls", false},
		{"bash", "npx eslint .", false},
		{"bash", "pnpm exec eslint .", false},
		{"bash", "echo \"$(date)\"", false},
		{"bash", "echo \"`go test ./...`\"", true},
		{"bash", "echo \"$( (echo x); go test ./... )\"", true},
		{"pwsh", "echo \"$( (1+1); go test ./... )\"", true},
		{"bash", "/usr/bin/time -o timing.log go test ./...", true},
		{"bash", "sudo --user root make", true},
		{"cmd", "go te^st ./...", true},
		{"cmd", "echo \"$(go test)\"", false},
		{"bash", "echo '`go test`'", false},
		{"bash", "echo \"(not a group) go test\"", false},
	} {
		if got := c.Heavy(Message{Shell: tc.shell, Script: tc.script}); got != tc.heavy {
			t.Errorf("%s %q: heavy %v, want %v", tc.shell, tc.script, got, tc.heavy)
		}
	}
}

// Waiters that give up while queued, and one whose connection closes, leave the count of held
// slots exact: once every client has gone, nothing is held and every slot is free.
func TestHeldSlotsSurviveCancelsAndDisconnects(t *testing.T) {
	s := &Server{Slots: 2}
	socket := serve(t, s)
	as := func(agent string) Message {
		m := goTest
		m.Caller = &Caller{Agent: agent}
		return m
	}
	h1, err := Acquire(context.Background(), socket, as("a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Acquire(context.Background(), socket, as("b"), nil)
	if err != nil {
		t.Fatal(err)
	}
	type queued struct {
		cancel context.CancelFunc
		done   chan *Lease
	}
	var waiters []queued
	for i := range 4 {
		ctx, cancel := context.WithCancel(context.Background())
		in := make(chan struct{})
		var once sync.Once
		done := make(chan *Lease, 1)
		go func() {
			l, _ := Acquire(ctx, socket, as(strconv.Itoa(i%2)), func(int) { once.Do(func() { close(in) }) })
			done <- l
		}()
		<-in
		waiters = append(waiters, queued{cancel, done})
	}
	// A raw client that queues, then closes its connection.
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte(`{"version": "adanext-exec/1", "type": "acquire", "args": ["go", "test"]}` + string(rune(10))))
	if m, err := Read(bufio.NewReader(conn)); err != nil || m.Type != TypeQueued {
		t.Fatalf("the raw client was not queued: %+v, %v", m, err)
	}
	conn.Close()
	// Two give up while they wait.
	waiters[1].cancel()
	waiters[3].cancel()
	if l := <-waiters[1].done; l != nil {
		t.Error("a cancelled waiter got a slot")
	}
	if l := <-waiters[3].done; l != nil {
		t.Error("a cancelled waiter got a slot")
	}
	h1.Release()
	h2.Release()
	for _, i := range []int{0, 2} {
		select {
		case l := <-waiters[i].done:
			if l == nil {
				t.Fatalf("waiter %d got no slot", i)
			}
			l.Release()
		case <-time.After(10 * time.Second):
			t.Fatalf("waiter %d never got a slot", i)
		}
		waiters[i].cancel()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		held, free, inQueue := len(s.held), len(s.free), len(s.queue)
		s.mu.Unlock()
		if held == 0 && free == 2 && inQueue == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("held %d clients, %d free, %d queued; want 0, 2, 0", held, free, inQueue)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
