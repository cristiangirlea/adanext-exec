package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/cristiangirlea/adanext-exec/broker"
)

// checkRequest is what "check --json" reads: one command, an argv or shell text. Its fields stay
// raw until each is checked, so a null is told from a field left out.
type checkRequest struct {
	Version json.RawMessage `json:"version"`
	Args    json.RawMessage `json:"args"`
	Shell   json.RawMessage `json:"shell"`
	Script  json.RawMessage `json:"script"`
}

// field decodes one request field into v; a field left out stays v's zero value, and a null
// or a value of another type is an error.
func field(name string, raw json.RawMessage, v any) error {
	if raw == nil {
		return nil
	}
	if string(raw) == "null" {
		return fmt.Errorf("%s is null", name)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	return nil
}

// checkResponse is what it writes: the decision and its rule, or why there is none.
type checkResponse struct {
	Version  string          `json:"version"`
	Decision broker.Decision `json:"decision,omitempty"`
	Rule     string          `json:"rule,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// maxRequestSize bounds what check reads from its input.
const maxRequestSize = 1 << 20

// check is "adanext-exec check --json": the policy's decision for one command, without running
// it. Every answer is one JSON line on stdout: a decision (0), or an error, for a request or
// flags that are not valid (121) or a policy that cannot be used (125). An answer that cannot
// be written exits 126, so a caller that reads only the code still sees no decision.
func check(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	answer := func(code int, r checkResponse) int {
		r.Version = broker.ProtocolVersion
		line, err := json.Marshal(r)
		if err == nil {
			_, err = stdout.Write(append(line, '\n'))
		}
		if err != nil {
			fmt.Fprintf(stderr, "adanext-exec check: write the answer: %v\n", err)
			return ExitCannotRun
		}
		return code
	}
	if isElevated() {
		return answer(ExitCannotRun, checkResponse{Error: errElevated})
	}
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "read the request as JSON on stdin and answer in JSON on stdout")
	policy := flags.String("policy", "", "the policy file (default: the user's adanext-exec/policy.json, else the built-in rules)")
	profile := flags.String("profile", "", "the policy in the user's adanext-exec/profiles/<name>.json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return answer(ExitUsage, checkResponse{Error: err.Error()})
	}
	if !*asJSON || flags.NArg() != 0 {
		return answer(ExitUsage, checkResponse{Error: "give --json, no arguments, and the request on stdin"})
	}
	c, err := readRequest(stdin)
	if err != nil {
		return answer(ExitUsage, checkResponse{Error: err.Error()})
	}
	p, err := selectPolicy(*policy, *profile)
	var ue usageError
	switch {
	case errors.As(err, &ue):
		return answer(ExitUsage, checkResponse{Error: ue.Error()})
	case err != nil:
		return answer(ExitRefused, checkResponse{Error: err.Error()})
	}
	v, err := p.Check(c)
	if err != nil {
		return answer(ExitRefused, checkResponse{Error: err.Error()})
	}
	return answer(0, checkResponse{Decision: v.Decision, Rule: v.Rule})
}

// The shells a request may name: those broker.Run knows.
var knownShells = []broker.Shell{broker.ShellSh, broker.ShellBash, broker.ShellPowerShell, broker.ShellPwsh, broker.ShellCmd}

// readRequest reads one request, whole and valid, into the command it names.
func readRequest(stdin io.Reader) (broker.Command, error) {
	data, err := io.ReadAll(io.LimitReader(stdin, maxRequestSize+1))
	switch {
	case err != nil:
		return broker.Command{}, fmt.Errorf("read the request: %v", err)
	case len(data) > maxRequestSize:
		return broker.Command{}, fmt.Errorf("the request is larger than %d bytes", maxRequestSize)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r checkRequest
	if err := dec.Decode(&r); err != nil {
		return broker.Command{}, fmt.Errorf("the request is not valid: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return broker.Command{}, errors.New("the request is not valid: data after it")
	}
	var version, shell, script string
	// Pointers, so a null element is told from an empty string.
	var args []*string
	for _, f := range []struct {
		name string
		raw  json.RawMessage
		v    any
	}{{"version", r.Version, &version}, {"args", r.Args, &args}, {"shell", r.Shell, &shell}, {"script", r.Script, &script}} {
		if err := field(f.name, f.raw, f.v); err != nil {
			return broker.Command{}, fmt.Errorf("the request is not valid: %v", err)
		}
	}
	switch {
	case version != broker.ProtocolVersion:
		return broker.Command{}, fmt.Errorf("version %q, want %q", version, broker.ProtocolVersion)
	case r.Args != nil && (r.Shell != nil || r.Script != nil):
		return broker.Command{}, errors.New("both args and a shell or script; give one")
	case r.Args != nil:
		if len(args) == 0 {
			return broker.Command{}, errors.New("args is empty")
		}
		argv := make([]string, len(args))
		for i, a := range args {
			if a == nil {
				return broker.Command{}, fmt.Errorf("argument %d is null, not a string", i)
			}
			argv[i] = *a
		}
		if argv[0] == "" {
			return broker.Command{}, errors.New("an empty program name")
		}
		return broker.Command{Args: argv}, nil
	case shell == "" || script == "":
		return broker.Command{}, errors.New("no args, and no shell with a script")
	case !slices.Contains(knownShells, broker.Shell(shell)):
		return broker.Command{}, fmt.Errorf("shell %q is not sh, bash, powershell, pwsh or cmd", shell)
	}
	return broker.Command{Shell: broker.Shell(shell), Script: script}, nil
}
