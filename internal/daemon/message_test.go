package daemon

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestEveryMessageRoundTrips(t *testing.T) {
	for _, m := range []Message{
		{Type: TypeAcquire, Args: []string{"go", "test", "./..."}, Caller: &Caller{Agent: "claude", RunID: "r1"}, MaxWaitMS: 60000},
		{Type: TypeAcquire, Shell: "bash", Script: "make -j8"},
		{Type: TypeLight},
		{Type: TypeQueued, Position: 3},
		{Type: TypeGranted, Slot: 1, WaitedMS: 1500, Env: []string{"GOFLAGS=-p=4", "MAKEFLAGS=-j4"}},
		{Type: TypeError, Error: "would wait longer than max_wait_ms"},
	} {
		var buf bytes.Buffer
		if err := Write(&buf, m); err != nil {
			t.Fatalf("Write(%+v): %v", m, err)
		}
		if !strings.HasSuffix(buf.String(), "\n") || strings.Count(buf.String(), "\n") != 1 {
			t.Errorf("Write(%+v) = %q, want one line", m, buf.String())
		}
		got, err := Read(bufio.NewReader(&buf))
		m.Version = Version
		if err != nil || !reflect.DeepEqual(got, m) {
			t.Errorf("Read = %+v, %v; want %+v", got, err, m)
		}
	}
}

func TestAMessageTheProtocolDoesNotAllowIsRefused(t *testing.T) {
	for name, line := range map[string]string{
		"not JSON":              `{`,
		"a list":                `[]`,
		"no type":               `{"version": "adanext-exec/1"}`,
		"an unknown type":       `{"version": "adanext-exec/1", "type": "release"}`,
		"another version":       `{"version": "adanext-exec/2", "type": "light"}`,
		"no version":            `{"type": "light"}`,
		"an unknown field":      `{"version": "adanext-exec/1", "type": "light", "slot": 1}`,
		"another type's field":  `{"version": "adanext-exec/1", "type": "queued", "position": 1, "slot": 2}`,
		"a null":                `{"version": "adanext-exec/1", "type": "acquire", "args": ["ls"], "caller": null}`,
		"a null argument":       `{"version": "adanext-exec/1", "type": "acquire", "args": ["ls", null]}`,
		"a wrong type":          `{"version": "adanext-exec/1", "type": "queued", "position": "1"}`,
		"both forms":            `{"version": "adanext-exec/1", "type": "acquire", "args": ["ls"], "shell": "sh", "script": "ls"}`,
		"no command":            `{"version": "adanext-exec/1", "type": "acquire"}`,
		"an empty program":      `{"version": "adanext-exec/1", "type": "acquire", "args": [""]}`,
		"a negative max wait":   `{"version": "adanext-exec/1", "type": "acquire", "args": ["ls"], "max_wait_ms": -1}`,
		"position zero":         `{"version": "adanext-exec/1", "type": "queued"}`,
		"slot zero":             `{"version": "adanext-exec/1", "type": "granted"}`,
		"an env entry no key":   `{"version": "adanext-exec/1", "type": "granted", "slot": 1, "env": ["=x"]}`,
		"an env entry no equal": `{"version": "adanext-exec/1", "type": "granted", "slot": 1, "env": ["GOFLAGS"]}`,
		"an empty error":        `{"version": "adanext-exec/1", "type": "error"}`,
		"a caller field":        `{"version": "adanext-exec/1", "type": "acquire", "args": ["ls"], "caller": {"user": "x"}}`,
		"an unknown shell":      `{"version": "adanext-exec/1", "type": "acquire", "shell": "fish", "script": "make"}`,
		"empty args and shell":  `{"version": "adanext-exec/1", "type": "acquire", "args": [], "shell": "bash", "script": "make"}`,
		"args and empty shell":  `{"version": "adanext-exec/1", "type": "acquire", "args": ["go"], "shell": ""}`,
		"empty args":            `{"version": "adanext-exec/1", "type": "acquire", "args": []}`,
		"a duplicate key":       `{"version": "adanext-exec/1", "type": "acquire", "args": [null], "args": ["go"]}`,
		"a duplicate type":      `{"version": "adanext-exec/1", "type": "error", "type": "light"}`,
		"a duplicate in caller": `{"version": "adanext-exec/1", "type": "acquire", "args": ["go"], "caller": {"agent": "a", "agent": "b"}}`,
		"a null in caller":      `{"version": "adanext-exec/1", "type": "acquire", "args": ["go"], "caller": {"agent": null}}`,
		"a null in env":         `{"version": "adanext-exec/1", "type": "granted", "slot": 1, "env": [null]}`,
		"a negative position":   `{"version": "adanext-exec/1", "type": "queued", "position": -1}`,
		"a negative slot":       `{"version": "adanext-exec/1", "type": "granted", "slot": -1}`,
		"a negative wait":       `{"version": "adanext-exec/1", "type": "granted", "slot": 1, "waited_ms": -5}`,
		"too deep":              `{"version": "adanext-exec/1", "type": "acquire", "args": [[[[[[[[["go"]]]]]]]]]}`,
		"a type not a string":   `{"version": "adanext-exec/1", "type": 1}`,
	} {
		if _, err := Read(bufio.NewReader(strings.NewReader(line + "\n"))); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: Read(%s) = %v, want ErrProtocol", name, line, err)
		}
	}
}

// A null element hides in a list: json reads it as an empty string unless refused.
func TestANullArgumentIsNotAnEmptyOne(t *testing.T) {
	_, err := Decode([]byte(`{"version": "adanext-exec/1", "type": "acquire", "args": ["ls", null]}`))
	if !errors.Is(err, ErrProtocol) {
		t.Errorf("Decode = %v, want ErrProtocol", err)
	}
}

func TestReadKeepsToOneBoundedLine(t *testing.T) {
	long := `{"version": "adanext-exec/1", "type": "error", "error": "` + strings.Repeat("x", MaxLine) + `"}` + "\n"
	if _, err := Read(bufio.NewReaderSize(strings.NewReader(long), 4096)); !errors.Is(err, ErrProtocol) {
		t.Errorf("a line over the bound: %v, want ErrProtocol", err)
	}
	if _, err := Read(bufio.NewReader(strings.NewReader(`{"version": "adanext-exec/1", "type": "light"}`))); !errors.Is(err, ErrProtocol) {
		t.Errorf("no newline: %v, want ErrProtocol", err)
	}
	if _, err := Read(bufio.NewReader(strings.NewReader(""))); err != io.EOF {
		t.Errorf("a closed connection: %v, want io.EOF", err)
	}
	// Two messages on one connection are read one at a time.
	r := bufio.NewReader(strings.NewReader("{\"version\": \"adanext-exec/1\", \"type\": \"queued\", \"position\": 2}\n{\"version\": \"adanext-exec/1\", \"type\": \"queued\", \"position\": 1}\n"))
	for want := 2; want >= 1; want-- {
		if m, err := Read(r); err != nil || m.Position != want {
			t.Errorf("message %d: %+v, %v", 3-want, m, err)
		}
	}
}

func TestWriteRefusesWhatReadWouldRefuse(t *testing.T) {
	for _, m := range []Message{
		{Type: TypeLight, Slot: 1},
		{Type: TypeQueued, Position: 1, Error: "x"},
		{Type: TypeAcquire, Shell: "fish", Script: "make"},
		{Type: "release"},
		{Type: TypeQueued},
		{Type: TypeAcquire},
		{Type: TypeError},
		{Type: TypeError, Error: strings.Repeat("x", MaxLine)},
	} {
		if err := Write(io.Discard, m); !errors.Is(err, ErrProtocol) {
			t.Errorf("Write(%.60v) = %v, want ErrProtocol", m, err)
		}
	}
}

// A type name is JSON, escapes included.
func TestAnEscapedTypeNameIsItsType(t *testing.T) {
	// The type is written as a JSON escape; the backslash is built so no tool rewrites it.
	escaped := `{"version": "adanext-exec/1", "type": "li` + string(rune(92)) + `u0067ht"}`
	m, err := Decode([]byte(escaped))
	if err != nil || m.Type != TypeLight {
		t.Errorf("Decode = %+v, %v; want a light message", m, err)
	}
}

// A message longer than the reader's buffer is read whole, and the bound is exact.
func TestReadTakesALineAcrossChunksUpToTheBound(t *testing.T) {
	head, tail := `{"version": "adanext-exec/1", "type": "error", "error": "`, `"}`
	at := head + strings.Repeat("x", MaxLine-len(head)-len(tail)) + tail
	if len(at) != MaxLine {
		t.Fatalf("built %d bytes", len(at))
	}
	m, err := Read(bufio.NewReaderSize(strings.NewReader(at+"\n"), 16))
	if err != nil || len(m.Error) != MaxLine-len(head)-len(tail) {
		t.Errorf("at the bound: %v", err)
	}
	over := head + strings.Repeat("x", MaxLine-len(head)-len(tail)+1) + tail
	if _, err := Read(bufio.NewReaderSize(strings.NewReader(over+"\n"), 16)); !errors.Is(err, ErrProtocol) {
		t.Errorf("one byte over: %v, want ErrProtocol", err)
	}
}

// The depth bound holds for objects, lists and both mixed: the message is level one, eight
// levels pass, and nine do not.
func TestStrictBoundsTheDepth(t *testing.T) {
	nest := func(levels int, open, close string) string {
		return `{"a": ` + strings.Repeat(open, levels-1) + `1` + strings.Repeat(close, levels-1) + `}`
	}
	for _, tc := range []struct{ open, close string }{{`[`, `]`}, {`{"k": `, `}`}} {
		if err := strict([]byte(nest(8, tc.open, tc.close))); err != nil {
			t.Errorf("%s at 8 levels: %v", tc.open, err)
		}
		if err := strict([]byte(nest(9, tc.open, tc.close))); err == nil {
			t.Errorf("%s at 9 levels was taken", tc.open)
		}
	}
	if err := strict([]byte(`{"a": [{"b": [{"c": [{"d": [1]}]}]}]}`)); err != nil {
		t.Errorf("eight mixed levels: %v", err)
	}
	if err := strict([]byte(`{"a": [{"b": [{"c": [{"d": [[1]]}]}]}]}`)); err == nil {
		t.Error("nine mixed levels were taken")
	}
}
