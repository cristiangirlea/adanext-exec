package broker

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// gateWriter blocks every Write until released and counts the writes made after Run returned.
type gateWriter struct {
	release  chan struct{}
	mu       sync.Mutex
	buf      bytes.Buffer
	returned bool
	late     int
}

func (w *gateWriter) Write(p []byte) (int, error) {
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.returned {
		w.late++
	}
	return w.buf.Write(p)
}

func TestRunWaitsForABlockedWriterAndWritesNothingAfterReturning(t *testing.T) {
	w := &gateWriter{release: make(chan struct{})}
	c := helper("chunks")
	c.Stdout = w
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), c)
		w.mu.Lock()
		w.returned = true
		w.mu.Unlock()
		done <- err
	}()
	select {
	case err := <-done:
		close(w.release)
		t.Fatalf("Run returned (%v) while a Write was blocked", err)
	case <-time.After(3 * outputGrace):
	}
	close(w.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return once the writer was released")
	}
	time.Sleep(100 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.String() != "firstsecond" || w.late != 0 {
		t.Fatalf("output %q with %d writes after Run returned; want firstsecond and none", w.buf.String(), w.late)
	}
}

// brokenReader returns some input, then an error, or both at once.
type brokenReader struct{ sent, together bool }

func (r *brokenReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("the source broke")
	}
	r.sent = true
	n := copy(p, "partial")
	if r.together {
		return n, errors.New("the source broke")
	}
	return n, nil
}

func TestRunReportsAFailedStdinRead(t *testing.T) {
	for name, together := range map[string]bool{"after the data": false, "with the data": true} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			c := helper("cat")
			c.Stdin = &brokenReader{together: together}
			c.Stdout = &out
			res, err := Run(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), "the source broke") {
				t.Fatalf("err %v, want the read error", err)
			}
			if res.ExitCode != 0 || out.String() != "partial" {
				t.Fatalf("result %+v, output %q", res, out.String())
			}
		})
	}
}

func TestRunReportsAFailedStdinReadWhenTheCommandTakesNoInput(t *testing.T) {
	// The command exits without reading, so the write of the data fails or never happens.
	c := helper("exit")
	c.Stdin = &brokenReader{together: true}
	_, err := Run(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "the source broke") {
		t.Fatalf("err %v, want the read error", err)
	}
}
