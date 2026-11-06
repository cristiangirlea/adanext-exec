// Package daemon is the local daemon that shares heavy-command slots among all clients, and the
// messages it and its clients exchange: newline-delimited JSON over one connection per
// command, as docs/protocol.md describes.
package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
)

// Version is the protocol version every message carries; it is broker.ProtocolVersion, kept
// here too so the daemon does not import the SDK.
const Version = "adanext-exec/1"

// The message types.
const (
	// TypeAcquire: the client asks for a slot for one command.
	TypeAcquire = "acquire"
	// TypeLight: the command needs no slot; the connection may close.
	TypeLight = "light"
	// TypeQueued: the command waits; Position is its place, 1 being next.
	TypeQueued = "queued"
	// TypeGranted: the slot is the client's for as long as the connection stays open.
	TypeGranted = "granted"
	// TypeError: the daemon refuses the request, or gave up waiting; the connection closes.
	TypeError = "error"
)

// Caller says who asked, as the audit does; it is self-reported.
type Caller struct {
	Agent   string `json:"agent,omitempty"`
	Session string `json:"session,omitempty"`
	RunID   string `json:"run_id,omitempty"`
}

// Message is every message of the protocol; Type says which fields it carries.
type Message struct {
	Version string `json:"version"`
	Type    string `json:"type"`

	// acquire: the command, as an argv or shell text, who asked, and how long the client will
	// wait for a slot (0: as long as it takes).
	Args      []string `json:"args,omitempty"`
	Shell     string   `json:"shell,omitempty"`
	Script    string   `json:"script,omitempty"`
	Caller    *Caller  `json:"caller,omitempty"`
	MaxWaitMS int64    `json:"max_wait_ms,omitempty"`

	// queued: the place in the queue.
	Position int `json:"position,omitempty"`

	// granted: the slot's number, how long the client waited, and KEY=VALUE hints that bound
	// the command's own parallelism.
	Slot     int      `json:"slot,omitempty"`
	WaitedMS int64    `json:"waited_ms,omitempty"`
	Env      []string `json:"env,omitempty"`

	// error: why.
	Error string `json:"error,omitempty"`
}

// fields lists, per type, the fields a message of that type may carry besides version and type.
var fields = map[string][]string{
	TypeAcquire: {"args", "shell", "script", "caller", "max_wait_ms"},
	TypeLight:   {},
	TypeQueued:  {"position"},
	TypeGranted: {"slot", "waited_ms", "env"},
	TypeError:   {"error"},
}

// MaxLine bounds one message on the wire.
const MaxLine = 1 << 20

// ErrProtocol means a message is not one the protocol allows; the error says why.
var ErrProtocol = errors.New("not a valid message")

// Read reads one message, a line of at most MaxLine bytes, and checks it.
func Read(r *bufio.Reader) (Message, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > MaxLine+1 {
			return Message{}, fmt.Errorf("%w: longer than %d bytes", ErrProtocol, MaxLine)
		}
		switch {
		case err == nil:
			return Decode(bytes.TrimSuffix(line, []byte("\n")))
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err == io.EOF && len(line) > 0:
			return Message{}, fmt.Errorf("%w: no newline at the end", ErrProtocol)
		}
		return Message{}, err
	}
}

// maxDepth bounds how deep a message may nest; the protocol's own messages are two deep.
const maxDepth = 8

// strict reads the whole line token by token, before anything collapses it: one object, no
// null anywhere, no key twice in an object, no deeper than maxDepth, nothing after it. Go reads
// a null as an empty value and keeps the last of two equal keys, so either could hide what a
// check looks for.
func strict(line []byte) error {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('{') {
		return errors.New("not an object")
	}
	if err := walkObject(dec, 1); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the message")
	}
	return nil
}

func walkObject(dec *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := t.(string)
		if seen[key] {
			return fmt.Errorf("%q twice", key)
		}
		seen[key] = true
		if err := walkValue(dec, depth); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	return err
}

func walkValue(dec *json.Decoder, depth int) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	switch t {
	case nil:
		return errors.New("a null")
	case json.Delim('{'), json.Delim('['):
		if depth >= maxDepth {
			return fmt.Errorf("nested deeper than %d", maxDepth)
		}
		if t == json.Delim('{') {
			return walkObject(dec, depth+1)
		}
		for dec.More() {
			if err := walkValue(dec, depth+1); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	return nil
}

// Shells lists the shells an acquire may name: those the broker runs.
var Shells = []string{"sh", "bash", "powershell", "pwsh", "cmd"}

// Decode checks and decodes one message: one strict JSON object, of a known type, with only
// that type's fields, the right version, and the type's own rules.
func Decode(line []byte) (Message, error) {
	if err := strict(line); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	var typ string
	if err := json.Unmarshal(raw["type"], &typ); err != nil {
		return Message{}, fmt.Errorf("%w: type: %v", ErrProtocol, err)
	}
	allowed, known := fields[typ]
	if !known {
		return Message{}, fmt.Errorf("%w: type %q", ErrProtocol, typ)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != "version" && k != "type" && !slices.Contains(allowed, k) {
			return Message{}, fmt.Errorf("%w: field %q in a %s message", ErrProtocol, k, typ)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var m Message
	if err := dec.Decode(&m); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	if m.Type == TypeAcquire {
		// Which form is judged by the fields given, not their values: an empty one is still there.
		_, args := raw["args"]
		_, shell := raw["shell"]
		_, script := raw["script"]
		switch {
		case args && (shell || script):
			return Message{}, fmt.Errorf("%w: both args and a shell or script", ErrProtocol)
		case args && len(m.Args) == 0:
			return Message{}, fmt.Errorf("%w: args is empty", ErrProtocol)
		}
	}
	if err := m.check(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// check applies the rules of the message's type to its values.
func (m Message) check() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrProtocol, fmt.Sprintf(format, a...))
	}
	if m.Version != Version {
		return bad("version %q, want %q", m.Version, Version)
	}
	if _, known := fields[m.Type]; !known {
		return bad("type %q", m.Type)
	}
	switch m.Type {
	case TypeAcquire:
		switch {
		case len(m.Args) > 0 && (m.Shell != "" || m.Script != ""):
			return bad("both args and a shell or script")
		case len(m.Args) > 0 && m.Args[0] == "":
			return bad("an empty program name")
		case len(m.Args) == 0 && (m.Shell == "" || m.Script == ""):
			return bad("no args, and no shell with a script")
		case len(m.Args) == 0 && !slices.Contains(Shells, m.Shell):
			return bad("shell %q is not sh, bash, powershell, pwsh or cmd", m.Shell)
		case m.MaxWaitMS < 0:
			return bad("a negative max_wait_ms")
		}
	case TypeQueued:
		if m.Position < 1 {
			return bad("position %d, want 1 or more", m.Position)
		}
	case TypeGranted:
		if m.Slot < 1 || m.WaitedMS < 0 {
			return bad("slot %d, waited_ms %d", m.Slot, m.WaitedMS)
		}
		for _, kv := range m.Env {
			if k, _, ok := bytes.Cut([]byte(kv), []byte("=")); !ok || len(k) == 0 {
				return bad("env entry %q is not KEY=VALUE", kv)
			}
		}
	case TypeError:
		if m.Error == "" {
			return bad("an error with no text")
		}
	}
	return nil
}

// Write writes one message as a line, after reading its own encoding back as Read would, so
// nothing is sent that a reader refuses.
func Write(w io.Writer, m Message) error {
	m.Version = Version
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(line) > MaxLine {
		return fmt.Errorf("%w: longer than %d bytes", ErrProtocol, MaxLine)
	}
	if _, err := Decode(line); err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}
