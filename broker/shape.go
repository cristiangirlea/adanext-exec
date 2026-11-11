package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cristiangirlea/adanext-exec/internal/originals"
	"github.com/cristiangirlea/adanext-exec/internal/shape"
)

// DefaultShapeOver is the size past which Shape shapes an output when Over is zero.
const DefaultShapeOver = 30000

// MaxShapeHold bounds the output Shape holds, whatever the caps: an output past it is cut, and
// its original not kept. The binary holds it, and a copy for the summarizer, outside the
// command's memory ceiling.
const MaxShapeHold = 256 << 20

// Shape asks Run to take a command's stdout and stderr as one output, in the order written,
// and to write it to Stdout once the command has ended: as it is when it is at most Over bytes,
// and otherwise with the whole of it kept under Dir behind a handle and a checked summary in its
// place, or its head and tail when no summary keeps every fact (docs/design.md, "Shaping
// output"). A last line, "original: <handle> (<n> bytes)", names the original. An original that
// would pass a cap is not kept, and the output is cut instead (Result.OriginalRefused).
// Nothing is written while the command runs, so a command whose output must be seen live is run
// without Shape. Shape replaces OutputCap, which must be zero.
type Shape struct {
	// Dir holds the originals, each named <handle>.txt; Run makes it, owner-only, when it is
	// missing and its parent is not. It must be a plain directory, not a link.
	Dir string
	// Root, when set, is a directory with one directory per run, each with its originals in a
	// directory named originals: those and Dir count together against AllMax.
	Root string
	// Over is the size, in bytes, past which an output is shaped, and the size of the head and
	// tail of one that is cut; zero is DefaultShapeOver.
	Over int
	// DirMax caps the bytes in Dir, AllMax the bytes in Dir and under Root together; zero is
	// 64 MiB and 1 GiB.
	DirMax, AllMax int64
}

// open checks the Shape and opens its originals, before the command runs.
func (s *Shape) open(c Command) (*originals.Store, int, error) {
	switch {
	case s.Dir == "":
		return nil, 0, invalid("a Shape without a directory")
	case s.Over < 0 || s.Over > MaxShapeHold:
		return nil, 0, invalid("Shape.Over is not from 0 to %d", MaxShapeHold)
	case s.DirMax < 0 || s.AllMax < 0:
		return nil, 0, invalid("a negative originals cap")
	case c.OutputCap != 0:
		return nil, 0, invalid("both a Shape and an OutputCap; Shape.Over cuts what Shape cannot keep")
	}
	store, err := originals.Open(s.Dir, s.Root, s.DirMax, s.AllMax)
	if err != nil {
		return nil, 0, err
	}
	over := s.Over
	if over == 0 {
		over = DefaultShapeOver
	}
	return store, over, nil
}

// runShaped is run with the output held, then shaped and written to the caller's Stdout.
func runShaped(ctx context.Context, c Command, store *originals.Store, over int) (Result, bool, error) {
	h := &held{limit: max(min(store.Limit(), MaxShapeHold), int64(over)), keepTail: over - cutHead(over)}
	inner := c
	inner.Stdout, inner.Stderr, inner.OutputCap = h, h, 0
	res, ran, err := run(ctx, inner)
	if !ran {
		return res, ran, err
	}
	out, serr := h.shaped(store, over, &res)
	if c.Stdout != nil && out != "" {
		if _, werr := c.Stdout.Write([]byte(out)); werr != nil {
			serr = errors.Join(serr, fmt.Errorf("write the command's output: %w", werr))
		}
	}
	return res, true, errors.Join(err, serr)
}

// held keeps an output up to limit bytes, and its last keepTail bytes whatever its length.
type held struct {
	mu       sync.Mutex
	limit    int64
	keepTail int
	buf      []byte // the first limit bytes
	tail     []byte // at most 2*keepTail; its last keepTail bytes are the output's
	total    int64
}

func (h *held) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.total += int64(len(p))
	if room := h.limit - int64(len(h.buf)); room > 0 {
		h.buf = append(h.buf, p[:min(int64(len(p)), room)]...)
	}
	h.tail = append(h.tail, p...)
	if len(h.tail) > 2*h.keepTail {
		h.tail = append(h.tail[:0:0], h.tail[len(h.tail)-h.keepTail:]...)
	}
	return len(p), nil
}

// shaped is what the caller gets: the output as it is, a summary or a cut, with the original's
// line when one was kept. It records what it did in res.
func (h *held) shaped(store *originals.Store, over int, res *Result) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total <= int64(over) {
		return string(h.buf), nil
	}
	tail := h.tail
	if len(tail) > h.keepTail {
		tail = tail[len(tail)-h.keepTail:]
	}
	cut, n := cutOutput(h.buf, tail, h.total, over)
	if h.total > h.limit {
		res.OutputCut, res.OriginalRefused = n, fmt.Sprintf("%v: the output's %d bytes are past the cap of %d", originals.ErrFull, h.total, h.limit)
		return cut, nil
	}
	handle, err := store.Save(h.buf)
	if err != nil {
		res.OutputCut = n
		if errors.Is(err, originals.ErrFull) {
			res.OriginalRefused = err.Error()
			return cut, nil
		}
		return cut, err
	}
	res.Original, res.OriginalBytes = handle, h.total
	text := string(h.buf)
	body, ok := shape.Summarize(shape.Classify(text), text)
	if ok {
		res.Shaped = true
	} else {
		body, res.OutputCut = cut, n
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + fmt.Sprintf("original: %s (%d bytes)\n", handle, h.total), nil
}

// cutHead is the head's share of a cut: two thirds, as OutputCap keeps.
func cutHead(over int) int { return over/3*2 + over%3*2/3 }

// cutOutput is head, a line counting what was left out, and tail, moved off the middle of a
// UTF-8 character; it returns the count too.
func cutOutput(head, tail []byte, total int64, over int) (string, int64) {
	a := min(cutHead(over), len(head))
	for moved := 0; moved < utf8.UTFMax-1 && a > 0 && a < len(head) && !utf8.RuneStart(head[a]); moved++ {
		a--
	}
	b := 0
	for moved := 0; moved < utf8.UTFMax-1 && b < len(tail) && !utf8.RuneStart(tail[b]); moved++ {
		b++
	}
	n := total - int64(a) - int64(len(tail)-b)
	return string(head[:a]) + fmt.Sprintf("\n[... %d bytes cut ...]\n", n) + string(tail[b:]), n
}
