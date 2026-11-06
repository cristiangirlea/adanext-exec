package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/cristiangirlea/adanext-exec/internal/guard"
)

// Decision is what a policy says of a command.
type Decision string

// The decisions, from the most permissive to the strictest.
const (
	// Allow runs the command.
	Allow Decision = "allow"
	// Ask runs it only once a person approves; where no person can be asked, it is refused.
	Ask Decision = "ask"
	// Refuse does not run it.
	Refuse Decision = "refuse"
)

// strictness orders the decisions; the strictest of the rules a command meets wins.
func (d Decision) strictness() int {
	switch d {
	case Allow:
		return 0
	case Ask:
		return 1
	case Refuse:
		return 2
	}
	return -1
}

// Verdict is a policy's answer for one command: the decision, and the rule that gave it ("" when
// no rule matched and the policy's default decided).
type Verdict struct {
	Decision Decision
	Rule     string
}

// ErrPolicy means the policy could not be read or is not valid, so nothing it would judge runs.
var ErrPolicy = errors.New("the policy cannot be used")

// ErrRefused means the policy did not allow the command, so it was not run. The error is a
// *RefusedError, which carries the verdict.
var ErrRefused = errors.New("refused by the policy")

// RefusedError is the error Run returns for a command its policy refused, or answered ask for
// with no person to ask.
type RefusedError struct {
	Verdict Verdict
}

func (e *RefusedError) Error() string {
	rule := e.Verdict.Rule
	if rule == "" {
		rule = "the default"
	}
	if e.Verdict.Decision == Ask {
		return fmt.Sprintf("%v: %s asks a person, and none can be asked here", ErrRefused, rule)
	}
	return fmt.Sprintf("%v: %s", ErrRefused, rule)
}

// Is makes errors.Is(err, ErrRefused) hold.
func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// Policy decides whether a command runs. The built-in rules are the guard's (docs/design.md
// lists them), each refusing unless the policy names it with another decision; a policy adds
// rules of its own as regular expressions. A command meeting several rules gets the strictest
// of their decisions, so a rule can lift only what it names; the verdict names the first rule
// with that decision, the built-in ones (in docs/design.md's order) before the policy's own (in
// its order). Matching is textual: it stops the plausible accident, not a determined adversary.
//
// A Policy comes from ParsePolicy, LoadPolicy or DefaultPolicy; the zero Policy refuses every
// command.
type Policy struct {
	builtin map[string]Decision
	rules   []policyRule
	def     Decision
}

type policyRule struct {
	name     string
	re       *regexp.Regexp
	decision Decision
}

// policyFile is the JSON form of a policy.
type policyFile struct {
	Version int `json:"version"`
	// Default is raw, so a default given as "" or null is told from one left out.
	Default json.RawMessage `json:"default"`
	Rules   []ruleFile      `json:"rules"`
}

type ruleFile struct {
	Name     string   `json:"name"`
	Match    string   `json:"match"`
	Decision Decision `json:"decision"`
}

// policyVersion is the version of the policy file this SDK reads.
const policyVersion = 1

// maxPolicySize bounds what LoadPolicy reads.
const maxPolicySize = 1 << 20

var ruleName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// DefaultPolicy is the built-in rules, each refusing, and allow for everything else.
func DefaultPolicy() *Policy {
	p, err := ParsePolicy([]byte(`{"version": 1}`))
	if err != nil {
		panic(err)
	}
	return p
}

// ParsePolicy reads a policy from its JSON form:
//
//	{
//	  "version": 1,
//	  "default": "allow",
//	  "rules": [
//	    {"name": "git-push", "decision": "ask"},
//	    {"name": "no-force", "match": "--force\\b", "decision": "refuse"}
//	  ]
//	}
//
// A rule without "match" names a built-in rule and gives it another decision; a rule with
// "match" is the policy's own, a regular expression (Go's syntax) tested against the command
// line. "default" decides a command no rule matches; it is allow when left out. Anything else,
// an unknown field, a name used twice, a decision that is not allow, ask or refuse, or a match
// that does not compile, is an error (ErrPolicy). With an error comes a zero Policy, which
// refuses every command, so a caller that misses the error still runs nothing.
func ParsePolicy(data []byte) (*Policy, error) {
	p, err := parsePolicy(data)
	if err != nil {
		return &Policy{}, err
	}
	return p, nil
}

func parsePolicy(data []byte) (*Policy, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f policyFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicy, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: data after the policy", ErrPolicy)
	}
	if f.Version != policyVersion {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrPolicy, f.Version, policyVersion)
	}
	p := &Policy{builtin: map[string]Decision{}, def: Allow}
	if f.Default != nil {
		var d Decision
		if err := json.Unmarshal(f.Default, &d); err != nil || d.strictness() < 0 {
			return nil, fmt.Errorf("%w: default %s is not allow, ask or refuse", ErrPolicy, f.Default)
		}
		p.def = d
	}
	builtins := guard.Names()
	for _, name := range builtins {
		p.builtin[name] = Refuse
	}
	seen := map[string]bool{}
	for i, r := range f.Rules {
		switch {
		case !ruleName.MatchString(r.Name):
			return nil, fmt.Errorf("%w: rule %d: name %q is not lower-case letters, digits and dashes", ErrPolicy, i+1, r.Name)
		case seen[r.Name]:
			return nil, fmt.Errorf("%w: rule %q appears twice", ErrPolicy, r.Name)
		case r.Decision.strictness() < 0:
			return nil, fmt.Errorf("%w: rule %q: decision %q is not allow, ask or refuse", ErrPolicy, r.Name, r.Decision)
		}
		seen[r.Name] = true
		builtin := slices.Contains(builtins, r.Name)
		switch {
		case builtin && r.Match != "":
			return nil, fmt.Errorf("%w: rule %q is built in and takes no match", ErrPolicy, r.Name)
		case builtin:
			p.builtin[r.Name] = r.Decision
		case r.Match == "":
			return nil, fmt.Errorf("%w: rule %q is not built in and has no match", ErrPolicy, r.Name)
		default:
			re, err := regexp.Compile(r.Match)
			if err != nil {
				return nil, fmt.Errorf("%w: rule %q: %v", ErrPolicy, r.Name, err)
			}
			p.rules = append(p.rules, policyRule{name: r.Name, re: re, decision: r.Decision})
		}
	}
	return p, nil
}

// LoadPolicy reads a policy file (see ParsePolicy). On Linux and macOS the file must be the
// user's own and writable by no one else, since whoever can change it decides what runs; on
// Windows its access list decides, as for the audit. A link is followed: what is checked is the
// file opened. With an error comes a zero Policy, as from ParsePolicy.
func LoadPolicy(path string) (*Policy, error) {
	p, err := loadPolicy(path)
	if err != nil {
		return &Policy{}, err
	}
	return p, nil
}

func loadPolicy(path string) (*Policy, error) {
	// Non-blocking, so a pipe with no writer is refused below instead of hanging the open.
	f, err := os.OpenFile(path, os.O_RDONLY|openNonBlock, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicy, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicy, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrPolicy, path)
	}
	if err := onlyOwnerWrites(path, fi); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicy, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPolicySize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicy, err)
	}
	if len(data) > maxPolicySize {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrPolicy, path, maxPolicySize)
	}
	return parsePolicy(data)
}

// Check judges a command without running it. An error means the policy cannot judge it (a
// zero Policy), and the command must not run.
func (p *Policy) Check(c Command) (Verdict, error) {
	if p == nil || p.builtin == nil {
		return Verdict{}, fmt.Errorf("%w: not loaded", ErrPolicy)
	}
	// The faithful reading is the command as given: shell text as written, an argv with each
	// argument quoted, so an argument holding a separator stays one word. The other readings
	// (an argv joined plainly, so a quoted program path is not cut off from its arguments, and
	// each reading with quoted separators read as text) look for what the faithful one could
	// miss; they may add refusals and asks, never an allow.
	var faithful, more []string
	if len(c.Args) > 0 {
		line, plain := shellLine(c.Args), strings.Join(c.Args, " ")
		faithful = append(faithful, line)
		more = append(more, guard.QuotedSeparators(line), plain, guard.QuotedSeparators(plain))
	}
	if c.Script != "" {
		faithful = append(faithful, c.Script)
		more = append(more, guard.QuotedSeparators(c.Script))
	}
	metFaithful, metMore := map[string]bool{}, map[string]bool{}
	meet := func(lines []string, met map[string]bool) {
		for _, line := range lines {
			for _, name := range guard.MatchesAsWritten(line) {
				met[name] = true
			}
			for _, r := range p.rules {
				if r.re.MatchString(line) {
					met[r.name] = true
				}
			}
		}
	}
	meet(faithful, metFaithful)
	meet(more, metMore)
	// Of the rules met, the strictest decides; among equals, the first in this order names it.
	var best *Verdict
	consider := func(rule string, d Decision) {
		met := metFaithful[rule] || (metMore[rule] && d != Allow)
		if met && (best == nil || d.strictness() > best.Decision.strictness()) {
			best = &Verdict{Decision: d, Rule: rule}
		}
	}
	for _, name := range guard.Names() {
		consider(name, p.builtin[name])
	}
	for _, r := range p.rules {
		consider(r.name, r.decision)
	}
	if best == nil {
		return Verdict{Decision: p.def}, nil
	}
	return *best, nil
}

// shellLine writes an argv as shell text, so a rule written for shell text meets the same
// command given as an argv: an argument with anything but plain characters is single-quoted.
// It is for matching only: a shell would read a backslash or a leading ~ in a plain argument
// differently.
func shellLine(args []string) string {
	words := make([]string, len(args))
	for i, a := range args {
		if a != "" && strings.Trim(a, plainChars) == "" {
			words[i] = a
			continue
		}
		words[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(words, " ")
}

const plainChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@%+,~\\"
