package broker

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// digits is "0123456789" repeated to n bytes; letters is "abcdefghij" likewise.
func digits(n int) string  { return strings.Repeat("0123456789", n/10+1)[:n] }
func letters(n int) string { return strings.Repeat("abcdefghij", n/10+1)[:n] }

func spew(out, errOut int, env ...string) Command {
	return helper("spew", append([]string{"ADANEXT_TEST_OUT=" + strconv.Itoa(out), "ADANEXT_TEST_ERR=" + strconv.Itoa(errOut)}, env...)...)
}

func TestRunCapsOutputKeepingTheHeadAndTheTail(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := spew(10000, 5000)
	c.Stdout, c.Stderr = &stdout, &stderr
	c.OutputCap = 900 // a head of 600 bytes and a tail of 300
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if want := digits(10000)[:600] + "\n[... 9100 bytes cut ...]\n" + digits(10000)[9700:]; stdout.String() != want {
		t.Fatalf("stdout %d bytes, want the 600-byte head, the line and the 300-byte tail:\n%q", stdout.Len(), stdout.String())
	}
	if want := letters(5000)[:600] + "\n[... 4100 bytes cut ...]\n" + letters(5000)[4700:]; stderr.String() != want {
		t.Fatalf("stderr %q", stderr.String())
	}
	if res.OutputCut != 9100+4100 {
		t.Fatalf("OutputCut %d, want 13200", res.OutputCut)
	}
}

func TestRunPassesOutputUnderTheCapWhole(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := spew(900, 10)
	c.Stdout, c.Stderr = &stdout, &stderr
	c.OutputCap = 900
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != digits(900) || stderr.String() != letters(10) || res.OutputCut != 0 {
		t.Fatalf("stdout %d bytes, stderr %q, cut %d; want all of it", stdout.Len(), stderr.String(), res.OutputCut)
	}
}

func TestRunCapsOneWriterForBothStreamsOnce(t *testing.T) {
	var out bytes.Buffer
	c := spew(10000, 5000)
	c.Stdout, c.Stderr = &out, &out
	c.OutputCap = 900
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	line := "\n[... 14100 bytes cut ...]\n"
	if strings.Count(out.String(), "bytes cut") != 1 || !strings.Contains(out.String(), line) || out.Len() != 900+len(line) {
		t.Fatalf("output %d bytes, want 900 kept and one line %q", out.Len(), line)
	}
	if res.OutputCut != 14100 {
		t.Fatalf("OutputCut %d, want 14100", res.OutputCut)
	}
}

func TestRunCapsALargeOutputQuickly(t *testing.T) {
	var out bytes.Buffer
	c := spew(64<<20, 0)
	c.Stdout = &out
	c.OutputCap = 1024
	start := time.Now()
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.OutputCut != 64<<20-1024 || out.Len() > 1024+64 {
		t.Fatalf("cut %d, %d bytes passed on", res.OutputCut, out.Len())
	}
	if took := time.Since(start); took > 60*time.Second {
		t.Fatalf("64 MiB under a 1 KiB cap took %s", took)
	}
}

// signalWriter tells when its first bytes arrive.
type signalWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	first chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() == 0 {
		close(w.first)
	}
	return w.buf.Write(p)
}

func TestRunPassesTheHeadOnWhileTheCommandRuns(t *testing.T) {
	w := &signalWriter{first: make(chan struct{})}
	c := spew(600, 0, "ADANEXT_TEST_WAIT=3s")
	c.Stdout = w
	c.OutputCap = 900
	done := make(chan error, 1)
	go func() { _, err := Run(context.Background(), c); done <- err }()
	select {
	case <-w.first:
	case err := <-done:
		t.Fatalf("Run returned (%v) before any output arrived", err)
	case <-time.After(20 * time.Second):
		t.Fatal("no output while the command ran")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned (%v) as the head arrived; the command was still waiting", err)
	default:
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.String() != digits(600) {
		t.Fatalf("output %q", w.buf.String())
	}
}
