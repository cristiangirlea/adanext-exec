package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cristiangirlea/adanext-exec/broker"
)

// hookInput is the part of Claude Code's PreToolUse input the hook reads. Other fields are
// left alone: Claude Code adds fields between versions.
type hookInput struct {
	Event     string          `json:"hook_event_name"`
	Session   string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// hookOutput is the hook's answer: a permission decision, its reason, and for allow and ask
// the tool input with the command rewritten.
type hookOutput struct {
	Specific hookDecision `json:"hookSpecificOutput"`
}

type hookDecision struct {
	Event    string                     `json:"hookEventName"`
	Decision string                     `json:"permissionDecision"`
	Reason   string                     `json:"permissionDecisionReason"`
	Input    map[string]json.RawMessage `json:"updatedInput,omitempty"`
}

// The flags only a rewrite may carry: an approval, and a script the policy cannot read. A
// command that passes them itself could approve its own ask, or hide a script from the hook.
var rewriteOnlyFlags = []string{"-approved", "-script-b64"}

// hookExecutable is the binary the rewrite runs; tests replace it.
var hookExecutable = os.Executable

// lookPath finds a program in PATH; tests replace it.
var lookPath = exec.LookPath

// hook is "adanext-exec hook claude-code": Claude Code's PreToolUse hook. It judges a Bash or
// PowerShell command by the policy and answers deny, ask or allow; ask and allow rewrite the
// command so it runs through "adanext-exec run". Every other tool is left to Claude Code. A
// hook that exits 0 with no answer, or with a code other than 2, lets the command run as it
// is, so every failure here is a deny on stdout, or exit 2 when that cannot be written.
func hook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	deny := func(reason string) int {
		return hookAnswer(stdout, stderr, hookDecision{Decision: "deny", Reason: "adanext-exec: " + reason})
	}
	if len(args) == 0 || args[0] != "claude-code" {
		return deny(`unknown hook; the one there is "hook claude-code"`)
	}
	flags := flag.NewFlagSet("hook claude-code", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policy := flags.String("policy", "", "the policy file (default: the user's adanext-exec/policy.json, else the built-in rules)")
	profile := flags.String("profile", "", "the policy in the user's adanext-exec/profiles/<name>.json")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		if err == nil {
			err = errors.New("arguments after the flags")
		}
		return deny("the hook's arguments: " + err.Error())
	}
	if isElevated() {
		return deny(errElevated)
	}
	in, err := readHookInput(stdin)
	if err != nil {
		return deny(err.Error())
	}
	var shell broker.Shell
	switch in.ToolName {
	case "Bash":
		shell = broker.ShellBash
	case "PowerShell":
		shell = powerShell()
	default:
		// Not a shell command: Claude Code's own permissions decide.
		return 0
	}
	var input map[string]json.RawMessage
	var command string
	if err := json.Unmarshal(in.ToolInput, &input); err != nil || input == nil {
		return deny("the tool input is not a JSON object")
	}
	if err := json.Unmarshal(input["command"], &command); err != nil || command == "" {
		return deny("the tool input has no command string")
	}
	for _, f := range rewriteOnlyFlags {
		if strings.Contains(command, f) {
			return deny(fmt.Sprintf("the command passes %s itself; only the hook's rewrite carries it", f))
		}
	}
	if *policy != "" {
		// The command may run in another directory than the hook.
		if *policy, err = filepath.Abs(*policy); err != nil {
			return deny("the policy's path: " + err.Error())
		}
	}
	p, err := selectPolicy(*policy, *profile)
	if err != nil {
		return deny(err.Error())
	}
	v, err := p.Check(broker.Command{Shell: shell, Script: command})
	if err != nil {
		return deny(err.Error())
	}
	answer := hookDecision{Input: input}
	switch v.Decision {
	case broker.Refuse:
		return deny(fmt.Sprintf("refused under rule %q", v.Rule))
	case broker.Ask:
		// The rewrite hides the text, so the reason shows the person what they approve.
		answer.Decision = "ask"
		answer.Reason = fmt.Sprintf("adanext-exec asks under rule %q. The command:\n%s", v.Rule, command)
	default:
		answer.Decision = "allow"
		answer.Reason = "adanext-exec allows it; it runs through adanext-exec run"
	}
	exe, err := hookExecutable()
	if err != nil {
		return deny("find the binary for the rewrite: " + err.Error())
	}
	argv := []string{"run", "--shell", string(shell)}
	if *policy != "" {
		argv = append(argv, "--policy", *policy)
	}
	if *profile != "" {
		argv = append(argv, "--profile", *profile)
	}
	if v.Decision == broker.Ask {
		argv = append(argv, "--approved", v.Rule)
	}
	argv = append(argv, "--agent", "claude-code")
	if in.Session != "" {
		argv = append(argv, "--session", in.Session)
	}
	argv = append(argv, "--script-b64", base64.RawURLEncoding.EncodeToString([]byte(command)))
	rewrite, err := json.Marshal(shellCommandLine(shell, exe, argv))
	if err != nil {
		return deny("write the rewrite: " + err.Error())
	}
	input["command"] = rewrite
	return hookAnswer(stdout, stderr, answer)
}

// readHookInput reads and checks Claude Code's input: one JSON object for a PreToolUse event.
func readHookInput(stdin io.Reader) (hookInput, error) {
	data, err := io.ReadAll(io.LimitReader(stdin, maxRequestSize+1))
	switch {
	case err != nil:
		return hookInput{}, fmt.Errorf("read the hook input: %v", err)
	case len(data) > maxRequestSize:
		return hookInput{}, fmt.Errorf("the hook input is larger than %d bytes", maxRequestSize)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var in hookInput
	if err := dec.Decode(&in); err != nil {
		return hookInput{}, fmt.Errorf("the hook input is not valid JSON: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return hookInput{}, errors.New("the hook input is not valid JSON: data after it")
	}
	switch {
	case in.Event != "PreToolUse":
		return hookInput{}, fmt.Errorf("the hook runs on PreToolUse, not on %q", in.Event)
	case in.ToolName == "":
		return hookInput{}, errors.New("the hook input names no tool")
	}
	return in, nil
}

// powerShell is the shell Claude Code's PowerShell tool uses: PowerShell 7 when it is in PATH,
// else Windows PowerShell.
func powerShell() broker.Shell {
	if _, err := lookPath("pwsh"); err == nil || runtime.GOOS != "windows" {
		return broker.ShellPwsh
	}
	return broker.ShellPowerShell
}

// shellCommandLine is the command line that runs exe with argv in the tool's shell: each word
// in single quotes, which neither shell reads inside.
func shellCommandLine(shell broker.Shell, exe string, argv []string) string {
	var b strings.Builder
	if shell == broker.ShellBash {
		// Git Bash reads a Windows path with forward slashes the same.
		b.WriteString(bashQuote(filepath.ToSlash(exe)))
		for _, a := range argv {
			b.WriteString(" " + bashQuote(a))
		}
		return b.String()
	}
	b.WriteString("& " + powerShellQuote(exe))
	for _, a := range argv {
		b.WriteString(" " + powerShellQuote(a))
	}
	return b.String()
}

func bashQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// powerShellQuote doubles the quote and the typographic single quotes PowerShell also ends a
// string on.
func powerShellQuote(s string) string {
	for _, q := range []string{"'", "\u2018", "\u2019", "\u201a", "\u201b"} {
		s = strings.ReplaceAll(s, q, q+q)
	}
	return "'" + s + "'"
}

// hookAnswer writes the answer as Claude Code reads it, exit 0; when it cannot be written, a
// deny is the reason on stderr and exit 2, which blocks the command.
func hookAnswer(stdout, stderr io.Writer, d hookDecision) int {
	d.Event = "PreToolUse"
	line, err := json.Marshal(hookOutput{d})
	if err == nil {
		_, err = stdout.Write(append(line, '\n'))
	}
	if err != nil {
		fmt.Fprintf(stderr, "adanext-exec hook: write the answer: %v; %s\n", err, d.Reason)
		return 2
	}
	return 0
}
