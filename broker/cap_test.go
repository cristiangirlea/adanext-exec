package broker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// digits is n bytes of five-digit counters ("000000000100002…"), so no stretch of it repeats
// and a misplaced head or tail cannot match by chance; letters is the same with a for 0, b for
// 1, and so on.
func digits(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%05d", i)
	}
	return b.String()[:n]
}

func letters(n int) string {
	return strings.Map(func(r rune) rune { return 'a' + r - '0' }, digits(n))
}

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

func TestRunCapsOfOneAndTwoBytes(t *testing.T) {
	for _, c := range []struct {
		cap  int
		want string
		cut  int64
	}{
		{1, "\n[... 99 bytes cut ...]\n" + digits(100)[99:], 99},                   // no head, a tail of 1
		{2, digits(100)[:1] + "\n[... 98 bytes cut ...]\n" + digits(100)[99:], 98}, // a head of 1, a tail of 1
		{3, digits(100)[:2] + "\n[... 97 bytes cut ...]\n" + digits(100)[99:], 97},
	} {
		t.Run(strconv.Itoa(c.cap), func(t *testing.T) {
			var out bytes.Buffer
			cmd := spew(100, 0)
			cmd.Stdout = &out
			cmd.OutputCap = c.cap
			res, err := Run(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			if out.String() != c.want || res.OutputCut != c.cut {
				t.Fatalf("output %q, cut %d; want %q, %d", out.String(), res.OutputCut, c.want, c.cut)
			}
		})
	}
}

func TestRunTakesTheLargestCap(t *testing.T) {
	var out bytes.Buffer
	c := spew(1000, 0)
	c.Stdout = &out
	c.OutputCap = math.MaxInt
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != digits(1000) || res.OutputCut != 0 {
		t.Fatalf("output %d bytes, cut %d; want all 1000", out.Len(), res.OutputCut)
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
	if i := strings.Index(out.String(), line); i != 600 || strings.Count(out.String(), "bytes cut") != 1 || out.Len() != 900+len(line) {
		t.Fatalf("output %d bytes with the line at %d, want a 600-byte head, the line, a 300-byte tail", out.Len(), i)
	}
	// Both streams end at once, so the tail is the last 300 bytes of one of them.
	if tail := out.String()[600+len(line):]; tail != digits(10000)[9700:] && tail != letters(5000)[4700:] {
		t.Fatalf("tail %q is not the end of either stream", tail)
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

func TestTheHeldTailStaysWithinTwiceItsSize(t *testing.T) {
	s := newStream(nil, 900)
	o := &outputs{streams: []*stream{s}}
	chunk := []byte(digits(32 << 10))
	for i := 0; i < 1000; i++ {
		o.take(s, chunk)
		if len(s.tail) > 2*300 {
			t.Fatalf("after %d chunks the held tail is %d bytes, over twice the 300 kept", i+1, len(s.tail))
		}
	}
	o.flush()
	if s.total != 1000*int64(len(chunk)) || s.passed != 900 {
		t.Fatalf("total %d, passed %d; want everything counted and 900 passed", s.total, s.passed)
	}
}

// failingAt fails its nth Write and every one after, and counts them all.
type failingAt struct {
	mu    sync.Mutex
	n     int
	calls int
	got   bytes.Buffer
}

func (w *failingAt) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.calls >= w.n {
		return 0, errors.New("the writer broke")
	}
	return w.got.Write(p)
}

func TestRunStopsWritingAStreamOnceItsWriterFails(t *testing.T) {
	// stdout fails on its head, stderr on the line before its tail: neither gets another call,
	// and OutputCut still counts only what the cap left out.
	stdout, stderr := &failingAt{n: 1}, &failingAt{n: 2}
	c := spew(10000, 5000)
	c.Stdout, c.Stderr = stdout, stderr
	c.OutputCap = 900
	res, err := Run(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "the writer broke") {
		t.Fatalf("err %v, want the write error", err)
	}
	if stdout.calls != 1 || stderr.calls != 2 || stderr.got.String() != letters(5000)[:600] {
		t.Fatalf("stdout called %d times, stderr %d with %q; want 1 and 2, the head only", stdout.calls, stderr.calls, stderr.got.String())
	}
	if res.OutputCut != 9100+4100 {
		t.Fatalf("OutputCut %d, want 13200", res.OutputCut)
	}
}
