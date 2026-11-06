package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cristiangirlea/adanext-exec/internal/redact"
)

// Caller says who asked for a command, for the audit. Every field is what the caller says of
// itself; nothing checks it.
type Caller struct {
	Agent   string `json:"agent,omitempty"`
	Session string `json:"session,omitempty"`
	RunID   string `json:"run_id,omitempty"`
}

// Audit writes one JSON line for each command Run is given, to <Dir>/<date>.jsonl, the date
// being the command's start in UTC. The directory is made owner-only (0700) and each file
// owner-only (0600) where the platform has such modes. A line is appended with one write, so
// lines from several processes do not interleave. Secrets in the command, its error and the
// caller are redacted first, by pattern and by the values of secret-named variables in the
// command's environment: best effort, not a guarantee. The audit lives in the user's own
// directory; it is not tamper-evident.
type Audit struct {
	Dir string

	mu sync.Mutex
}

// ErrAudit means the audit could not be opened, so the command was not run.
var ErrAudit = errors.New("the audit cannot be written")

// DefaultAuditDir is where the audit goes unless told otherwise: the user's state directory
// ($XDG_STATE_HOME or ~/.local/state on Linux, ~/Library/Application Support on macOS,
// %LocalAppData% on Windows), then adanext-exec/audit.
func DefaultAuditDir() (string, error) {
	var base string
	var err error
	switch runtime.GOOS {
	case "windows":
		base, err = os.UserCacheDir() // %LocalAppData%
	case "darwin":
		base, err = os.UserConfigDir() // ~/Library/Application Support
	default:
		if base = os.Getenv("XDG_STATE_HOME"); base == "" || !filepath.IsAbs(base) {
			var home string
			home, err = os.UserHomeDir()
			base = filepath.Join(home, ".local", "state")
		}
	}
	if err != nil {
		return "", fmt.Errorf("find the user's state directory: %w", err)
	}
	return filepath.Join(base, "adanext-exec", "audit"), nil
}

// auditFile is one day's file, open from before the command starts until its line is written.
type auditFile struct {
	a *Audit
	f *os.File
}

// open makes sure the line can be written before the command runs.
func (a *Audit) open(at time.Time) (*auditFile, error) {
	if a.Dir == "" {
		return nil, fmt.Errorf("%w: no directory", ErrAudit)
	}
	if err := os.MkdirAll(a.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAudit, err)
	}
	if err := ownerOnly(a.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAudit, err)
	}
	name := filepath.Join(a.Dir, at.UTC().Format("2006-01-02")+".jsonl")
	// A link or a special file (a device, a pipe) at the name would take the lines elsewhere
	// or block; only a regular file is an audit.
	if fi, err := os.Lstat(name); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrAudit, name)
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE|openNoFollow, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAudit, err)
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", name)
	}
	if err == nil {
		err = ownerOnly(name, 0o600)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %v", ErrAudit, err)
	}
	return &auditFile{a: a, f: f}, nil
}

// auditWrite appends a line; tests replace it.
var auditWrite = func(f *os.File, line []byte) error {
	_, err := f.Write(line)
	return err
}

// write appends the line and closes the file.
func (af *auditFile) write(rec auditRecord) error {
	defer af.f.Close()
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAudit, err)
	}
	af.a.mu.Lock()
	defer af.a.mu.Unlock()
	if err := auditWrite(af.f, append(line, '\n')); err != nil {
		return fmt.Errorf("%w: %v", ErrAudit, err)
	}
	return nil
}

// auditRecord is one line of the audit.
type auditRecord struct {
	Time       string   `json:"time"` // the start, RFC 3339 in UTC
	Caller     *Caller  `json:"caller,omitempty"`
	Dir        string   `json:"dir"` // the working directory, not confined to anything
	Args       []string `json:"args,omitempty"`
	Shell      string   `json:"shell,omitempty"`
	Script     string   `json:"script,omitempty"`
	Outcome    string   `json:"outcome"` // exited, signalled, timed out, cancelled, not run
	ExitCode   *int     `json:"exit_code,omitempty"`
	Signal     string   `json:"signal,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	OutputCut  int64    `json:"output_cut,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// snapshot is what the audit reads of the caller's state, taken before the command starts.
type snapshot struct {
	env []string
	dir string
}

// record describes the command, as given and as it ended, with secrets taken out.
func (c Command) record(snap snapshot, begin time.Time, res Result, ran bool, err error) auditRecord {
	clean := cleaner(snap.env)
	rec := auditRecord{
		Time:       begin.UTC().Format(time.RFC3339Nano),
		Dir:        clean(snap.dir),
		Shell:      clean(string(c.Shell)),
		Script:     clean(c.Script),
		DurationMS: res.Duration.Milliseconds(),
		OutputCut:  res.OutputCut,
	}
	for i, a := range c.Args {
		if i > 0 && takesSecret(c.Args[i-1]) {
			a = redactedValue
		}
		rec.Args = append(rec.Args, clean(a))
	}
	if c.Caller != (Caller{}) {
		rec.Caller = &Caller{Agent: clean(c.Caller.Agent), Session: clean(c.Caller.Session), RunID: clean(c.Caller.RunID)}
	}
	if err != nil {
		rec.Error = clean(err.Error())
	}
	switch {
	case !ran:
		rec.Outcome = "not run"
	case res.TimedOut:
		rec.Outcome = "timed out"
	case res.Canceled:
		rec.Outcome = "cancelled"
	case res.Signal != "":
		rec.Outcome = "signalled"
	default:
		rec.Outcome = "exited"
	}
	if ran {
		code := res.ExitCode
		rec.ExitCode = &code
		rec.Signal = res.Signal
	}
	return rec
}

const redactedValue = "[redacted]"

// takesSecret reports whether an argument is a flag whose next argument is a secret, as in
// argv ["--password", "x"], which the patterns, reading one argument at a time, cannot see.
func takesSecret(arg string) bool {
	switch strings.ToLower(arg) {
	case "--password", "--passwd", "--token", "--api-key", "--api_key", "--secret", "--auth",
		"--credentials", "--cookie", "--session", "-u", "--user", "--proxy-user", "bearer":
		return true
	}
	return false
}

// cleaner redacts secrets from a string: the values of secret-named variables in env, then
// the patterns.
func cleaner(env []string) func(string) string {
	values := redact.Env(env)
	return func(s string) string {
		if s == "" {
			return s
		}
		if values != nil {
			s = values.Replace(s)
		}
		return redact.Text(strings.ToValidUTF8(s, "�"))
	}
}
