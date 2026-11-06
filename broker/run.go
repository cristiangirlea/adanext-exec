package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/cristiangirlea/adanext-exec/internal/tree"
)

// Result is how a command ended.
type Result struct {
	// ExitCode is the command's exit code, or -1 when it was killed: by its timeout, by a
	// cancelled context, or (on Unix) by a signal from elsewhere.
	ExitCode int
	// Signal names the signal that ended the command on Unix ("killed", "terminated"), when one
	// did. It is empty on Windows and when the broker killed the command.
	Signal string
	// TimedOut is true when the Timeout or the context's deadline stopped the command.
	TimedOut bool
	// Canceled is true when the context was cancelled before the command ended.
	Canceled bool
	// Duration is how long the command ran, from its start to its end.
	Duration time.Duration
	// OutputCut counts the bytes of output left out under the Command's OutputCap, over both
	// streams; zero means all of it was passed on.
	OutputCut int64
}

// outputGrace is how long Run waits, once the tree is dead, for an output pipe that stays
// empty but open: only a process outside the tree can hold it so.
const outputGrace = time.Second

// errOutputHeld is joined to Run's error when a process outside the tree held the output open.
var errOutputHeld = errors.New("a process outside the command's tree held its output open; output after the command ended is not read")

// Run runs one command with its whole process tree contained: a Job Object on Windows, a new
// process group on Linux and macOS. When the command times out or ctx ends, Run kills the whole
// tree. When the command's own process exits, Run also kills whatever it left running, so
// nothing the command started outlives Run. On Unix a process that leaves the group (setsid,
// setpgid) is still found through a marker in its environment or its parent link; one that
// clears its environment and is orphaned, or that the user cannot read, is not (see
// docs/design.md). When the caller is killed first, Windows kills the whole tree, Linux the
// command's own process only, and macOS nothing. On Unix, Run must be the only one reaping its
// command: a caller
// that ignores SIGCHLD or waits for any child makes Run report an error and leave the group
// unsignalled, since its id may then belong to another process.
//
// A non-zero exit is a Result, not an error. When Run returns an error with a zero Result, the
// command did not run: the Command is invalid (ErrInvalid), it could not start, it could not be
// contained (ErrNotContained), or the platform cannot contain it (ErrUnsupported). An error
// with a Result comes after the command ran: writing its output failed, its output was held
// open from outside its tree, or killing or waiting for it failed.
//
// Run waits for every Write to Stdout and Stderr to return, and makes none after it returns. It
// does not wait for a Read from Stdin: once the command has ended, a Read that is still blocked
// is left to return on its own, and what it returns is dropped.
//
// With Command.Audit set, Run writes one line for the command, also for one it did not run,
// and refuses to run a command whose line it cannot open (ErrAudit).
//
// The console's Ctrl+C reaches the command as it reaches the caller. A caller that wants to
// report after one handles os.Interrupt and cancels ctx: Run then kills the tree and returns.
func Run(ctx context.Context, c Command) (Result, error) {
	begin := now()
	var log *auditFile
	var snap snapshot
	if c.Audit != nil {
		f, err := c.Audit.open(begin)
		if err != nil {
			return Result{}, err
		}
		log = f
		// Taken before the command starts: the environment it runs with, which the audit's
		// redaction reads, and the working directory it inherits.
		snap = snapshot{env: c.Env, dir: c.Dir}
		if snap.env == nil {
			snap.env = os.Environ()
			c.Env = snap.env
		}
		if snap.dir == "" {
			wd, err := os.Getwd()
			if err != nil {
				log.f.Close()
				return Result{}, fmt.Errorf("%w: read the working directory to record: %v", ErrAudit, err)
			}
			// The command runs where the audit says, whatever the caller's directory becomes.
			snap.dir, c.Dir = wd, wd
		}
	}
	res, ran, err := run(ctx, c)
	if log != nil {
		if aerr := log.write(c.record(snap, begin, res, ran, err)); aerr != nil {
			err = errors.Join(err, aerr)
		}
	}
	return res, err
}

// now is the clock Run reads its start from; tests replace it.
var now = time.Now

// run is Run without the audit; ran says whether the command started.
func run(ctx context.Context, c Command) (res Result, ran bool, _ error) {
	cmd, err := c.cmd()
	if err != nil {
		return Result{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, false, err
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	in, err := newInput(cmd, c.Stdin)
	if err != nil {
		return Result{}, false, err
	}
	defer in.close()
	out, err := newOutputs(cmd, c.Stdout, c.Stderr, c.OutputCap)
	if err != nil {
		return Result{}, false, err
	}
	start := time.Now()
	t, err := tree.Start(cmd)
	in.started()
	out.started()
	if err != nil {
		out.abandon()
		return Result{}, false, err
	}
	defer t.Close()
	ran = true

	exited := make(chan error, 1)
	go func() { exited <- t.WaitExit() }()
	var errs []error
	var waitErr error
	select {
	case waitErr = <-exited:
	case <-ctx.Done():
		select {
		case waitErr = <-exited:
			// It ended on its own as the context did.
		default:
			res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
			res.Canceled = !res.TimedOut
			if err := t.Kill(); err != nil {
				errs = append(errs, fmt.Errorf("kill the tree: %w", err))
				_ = cmd.Process.Kill()
			}
			waitErr = <-exited
		}
	}
	res.Duration = time.Since(start)
	if waitErr != nil {
		errs = append(errs, waitErr)
	}
	// Whatever the command left running goes too, before its process is reaped.
	if err := t.Kill(); err != nil {
		errs = append(errs, fmt.Errorf("kill what the command left running: %w", err))
	}
	err = cmd.Wait()
	in.close()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		errs = append(errs, err)
	}
	if err := in.readErr(); err != nil {
		errs = append(errs, err)
	}
	if err := out.finish(); err != nil {
		errs = append(errs, err)
	}
	res.OutputCut = out.cut()
	res.ExitCode = -1
	if cmd.ProcessState != nil {
		res.ExitCode, res.Signal = cmd.ProcessState.ExitCode(), signalOf(cmd.ProcessState)
	}
	if res.TimedOut || res.Canceled {
		res.ExitCode, res.Signal = -1, ""
	}
	return res, true, errors.Join(errs...)
}

// input feeds a reader that is not a file to the command through a pipe Run owns, so Run never
// waits for the reader.
type input struct {
	r, w           *os.File
	closeR, closeW sync.Once
	mu             sync.Mutex
	err            error // the reader's error, once the copy has ended on one
}

func newInput(cmd *exec.Cmd, stdin io.Reader) (*input, error) {
	in := &input{}
	if f, ok := stdin.(*os.File); ok || stdin == nil {
		if ok {
			cmd.Stdin = f
		}
		return in, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	in.r, in.w = r, w
	cmd.Stdin = r
	go in.copy(stdin)
	return in, nil
}

// copy feeds the pipe until the reader ends or the command stops taking input. A failed write
// means the command has gone or closed its input; a failed read is the caller's to hear.
func (in *input) copy(stdin io.Reader) {
	defer in.closeWrite() // the child's end is Run's to close, once the child has it
	buf := make([]byte, 32<<10)
	for {
		n, err := stdin.Read(buf)
		if err != nil && err != io.EOF {
			// Recorded first: the write below may fail or block until the command has gone.
			in.mu.Lock()
			in.err = fmt.Errorf("read the command's input: %w", err)
			in.mu.Unlock()
		}
		if n > 0 {
			if _, werr := in.w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// started closes the child's end.
func (in *input) started() {
	in.closeR.Do(func() {
		if in.r != nil {
			_ = in.r.Close()
		}
	})
}

// close ends the command's input and releases both ends; a copy still blocked in the reader
// stays blocked there.
func (in *input) close() {
	in.started()
	in.closeWrite()
}

// closeWrite ends the command's input.
func (in *input) closeWrite() {
	in.closeW.Do(func() {
		if in.w != nil {
			_ = in.w.Close()
		}
	})
}

// readErr is the reader's error, if the copy has ended on one by now.
func (in *input) readErr() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.err
}

// outputs copies the command's stdout and stderr from pipes Run owns, so Run, not exec, decides
// how long to wait for them after the tree is killed.
type outputs struct {
	readers []*os.File
	writers []*os.File // the child's ends, closed once it has started
	wg      sync.WaitGroup

	// mu guards the fields below and every Write to the caller's writers.
	mu        sync.Mutex
	streams   []*stream
	readSince []time.Time // per copy, when its pending Read began; zero while not reading
	finished  []bool
	abandoned bool
	flushed   bool
	err       error
}

// stream is one output's way to its writer under the cap: a head passed through, a tail held.
type stream struct {
	w        io.Writer // nil discards
	limit    int       // 0: no cap
	headLeft int       // bytes the head may still pass through
	keepTail int
	tail     []byte // at most 2*keepTail; its last keepTail bytes are written at the end
	total    int64  // bytes the command wrote
	passed   int64  // bytes written, or to be written, to w
}

func newStream(w io.Writer, limit int) *stream {
	head := limit/3*2 + limit%3*2/3 // two thirds, rounded down, without overflow
	return &stream{w: w, limit: limit, headLeft: head, keepTail: limit - head}
}

func newOutputs(cmd *exec.Cmd, stdout, stderr io.Writer, limit int) (*outputs, error) {
	o := &outputs{}
	var dests []io.Writer
	pipe := func(w io.Writer) (*os.File, error) {
		r, pw, err := os.Pipe()
		if err != nil {
			o.abandon()
			return nil, err
		}
		o.readers, o.writers = append(o.readers, r), append(o.writers, pw)
		dests = append(dests, w)
		return pw, nil
	}
	var err error
	if cmd.Stdout, err = pipe(stdout); err != nil {
		return nil, err
	}
	if stdout != nil && sameWriter(stdout, stderr) {
		cmd.Stderr = cmd.Stdout
	} else if cmd.Stderr, err = pipe(stderr); err != nil {
		return nil, err
	}
	// The copies start once the slices they share are complete.
	o.readSince, o.finished = make([]time.Time, len(o.readers)), make([]bool, len(o.readers))
	for _, w := range dests {
		o.streams = append(o.streams, newStream(w, limit))
	}
	for i, r := range o.readers {
		o.wg.Add(1)
		go o.copy(i, r)
	}
	return o, nil
}

// sameWriter reports whether two writers are one comparable value.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = false // an uncomparable dynamic type
		}
	}()
	return a == b
}

// copy keeps reading after a failed write, so the command never blocks on a full pipe, and
// writes nothing once the output is abandoned.
func (o *outputs) copy(i int, r *os.File) {
	defer o.wg.Done()
	buf := make([]byte, 32<<10)
	s := o.streams[i]
	for {
		o.mu.Lock()
		o.readSince[i] = time.Now()
		o.mu.Unlock()
		n, err := r.Read(buf)
		o.mu.Lock()
		o.readSince[i] = time.Time{}
		if n > 0 && !o.abandoned {
			o.take(s, buf[:n])
		}
		if err != nil {
			o.finished[i] = true
		}
		o.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// take passes p on under the stream's cap. Called with mu held.
func (o *outputs) take(s *stream, p []byte) {
	s.total += int64(len(p))
	if s.limit == 0 {
		o.write(s, p)
		return
	}
	head := min(s.headLeft, len(p))
	o.write(s, p[:head])
	s.headLeft -= head
	if rest := p[head:]; len(rest) > 0 {
		s.tail = append(s.tail, rest...)
		if len(s.tail) > 2*s.keepTail {
			s.tail = append(s.tail[:0:0], s.tail[len(s.tail)-s.keepTail:]...)
		}
	}
}

// write sends p to the stream's writer; after a failed write the rest is dropped and the first
// error kept. Called with mu held.
func (o *outputs) write(s *stream, p []byte) {
	s.passed += int64(len(p))
	if len(p) == 0 || s.w == nil {
		return
	}
	if _, err := s.w.Write(p); err != nil {
		if o.err == nil {
			o.err = fmt.Errorf("write the command's output: %w", err)
		}
		s.w = nil
	}
}

// flush writes each stream's held tail, after a line counting what was left out. Called with
// mu held, once.
func (o *outputs) flush() {
	if o.flushed {
		return
	}
	o.flushed = true
	for _, s := range o.streams {
		tail := s.tail
		if len(tail) > s.keepTail {
			tail = tail[len(tail)-s.keepTail:]
		}
		if cut := s.total - s.passed - int64(len(tail)); cut > 0 {
			line := fmt.Sprintf("\n[... %d bytes cut ...]\n", cut)
			if s.w != nil {
				if _, err := io.WriteString(s.w, line); err != nil {
					if o.err == nil {
						o.err = fmt.Errorf("write the command's output: %w", err)
					}
					s.w = nil
				}
			}
		}
		o.write(s, tail)
		s.tail = nil
	}
}

// cut counts the bytes left out over all streams.
func (o *outputs) cut() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	var n int64
	for _, s := range o.streams {
		n += s.total - s.passed
	}
	return n
}

// started closes the child's ends of the pipes, so a read ends once the tree has gone.
func (o *outputs) started() {
	for _, w := range o.writers {
		_ = w.Close()
	}
	o.writers = nil
}

// abandon closes every pipe and waits for the copies.
func (o *outputs) abandon() {
	o.started()
	for _, r := range o.readers {
		_ = r.Close()
	}
	o.wg.Wait()
}

// finish waits for the output to end, after the tree is dead. A copy that is writing is waited
// for however long it takes. A copy whose Read has waited on an empty pipe for outputGrace
// since then is reading from a pipe that only a process outside the tree holds open: when every
// unfinished copy is in that state, the output is abandoned and reported.
func (o *outputs) finish() error {
	done := make(chan struct{})
	go func() { o.wg.Wait(); close(done) }()
	since := time.Now()
	tick := time.NewTicker(outputGrace / 20)
	defer tick.Stop()
	for {
		select {
		case <-done:
			for _, r := range o.readers {
				_ = r.Close()
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			o.flush()
			return o.err
		case <-tick.C:
		}
		o.mu.Lock()
		held, open := true, 0
		for i, at := range o.readSince {
			if o.finished[i] {
				continue
			}
			open++
			if at.IsZero() || time.Since(laterOf(at, since)) < outputGrace {
				held = false
			}
		}
		held = held && open > 0
		if held {
			o.flush()
			o.abandoned = true
		}
		err := o.err
		o.mu.Unlock()
		if held {
			// A close can wait for the pending Read on some platforms; never in Run.
			go func(readers []*os.File) {
				for _, r := range readers {
					_ = r.Close()
				}
			}(o.readers)
			return errors.Join(err, errOutputHeld)
		}
	}
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
