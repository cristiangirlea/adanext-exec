package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cristiangirlea/adanext-exec/broker"
	"github.com/cristiangirlea/adanext-exec/internal/claudecode"
)

// hookInput is the part of Claude Code's PreToolUse input the hook reads. Other fields are
// left alone: Claude Code adds fields between versions.
type hookInput struct {
	Event     string          `json:"hook_event_name"`
	Session   string          `json:"session_id"`
	Cwd       string          `json:"cwd"`
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

// sessionID is the form of a session id the rewrite carries. Windows PowerShell passes a
// native argument's double quotes through to the program's own parser, so anything else could
// add flags to the rewrite.
var sessionID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// hookExecutable is the binary the rewrite runs; tests replace it.
var hookExecutable = os.Executable

// claudeSources are where Claude Code's settings files are, for a project; tests replace it.
var claudeSources = claudecode.DefaultSources

// claudeRules are the user's Claude Code rules for the session's project: the directory the
// session started in (CLAUDE_PROJECT_DIR), else the hook's.
func claudeRules(cwd string) (*claudecode.Rules, error) {
	project := os.Getenv("CLAUDE_PROJECT_DIR")
	if project == "" {
		project = cwd
	}
	if project == "" {
		var err error
		if project, err = os.Getwd(); err != nil {
			return nil, fmt.Errorf("find the project: %v", err)
		}
	}
	s, err := claudeSources(project)
	if err != nil {
		return nil, err
	}
	return claudecode.Load(s)
}

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
	shape := flags.Bool("shape", false, "rewrite each command, but one in the background, to run --shape")
	originalsDir := flags.String("originals", "", "with --shape: the directory that keeps the originals")
	originalsRoot := flags.String("originals-root", "", "with --shape: run's --originals-root")
	originalsMax := flags.String("originals-max", "", "with --shape: run's --originals-max")
	originalsAllMax := flags.String("originals-all-max", "", "with --shape: run's --originals-all-max")
	outputCap := flags.Int("output-cap", 0, "with --shape: run's --output-cap, the size past which an output is shaped")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		if err == nil {
			err = errors.New("arguments after the flags")
		}
		return deny("the hook's arguments: " + err.Error())
	}
	var shaping []string
	switch {
	case *shape && *originalsDir == "":
		return deny("the hook's arguments: --shape needs --originals")
	case !*shape && (*originalsDir != "" || *originalsRoot != "" || *originalsMax != "" || *originalsAllMax != "" || *outputCap != 0):
		return deny("the hook's arguments: --originals, --originals-root, --originals-max, --originals-all-max and --output-cap need --shape")
	case *shape:
		// The command may run in another directory than the hook.
		dir, err := filepath.Abs(*originalsDir)
		if err != nil {
			return deny("the originals' path: " + err.Error())
		}
		shaping = []string{"--shape", "--originals", dir}
		if *originalsRoot != "" {
			root, err := filepath.Abs(*originalsRoot)
			if err != nil {
				return deny("the originals root's path: " + err.Error())
			}
			shaping = append(shaping, "--originals-root", root)
		}
		for name, size := range map[string]string{"--originals-max": *originalsMax, "--originals-all-max": *originalsAllMax} {
			if _, err := parseSize(size); err != nil {
				return deny("the hook's arguments: " + name + ": " + err.Error())
			}
		}
		if *originalsMax != "" {
			shaping = append(shaping, "--originals-max", *originalsMax)
		}
		if *originalsAllMax != "" {
			shaping = append(shaping, "--originals-all-max", *originalsAllMax)
		}
		switch {
		case *outputCap < 0:
			return deny("the hook's arguments: a negative --output-cap")
		case *outputCap > 0:
			shaping = append(shaping, "--output-cap", strconv.Itoa(*outputCap))
		}
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
	timeout, err := hookTimeout(input)
	if err != nil {
		return deny(err.Error())
	}
	if in.Session != "" && !sessionID.MatchString(in.Session) {
		return deny("the session id is not letters, digits and . _ : -")
	}
	// The rewrite hides the text from the user's own Claude Code rules, so their deny and ask
	// count here, on the original.
	rules, err := claudeRules(in.Cwd)
	if err != nil {
		return deny("the Claude Code settings: " + err.Error())
	}
	userDecision, userRule := rules.Check(in.ToolName, command)
	if userDecision == claudecode.Deny {
		return deny(fmt.Sprintf("your Claude Code rule %q denies it", userRule))
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
	switch {
	case v.Decision == broker.Refuse:
		return deny(fmt.Sprintf("refused under rule %q", v.Rule))
	case v.Decision == broker.Ask:
		// The rewrite hides the text, so the reason shows the person what they approve.
		answer.Decision = "ask"
		under := fmt.Sprintf("under rule %q", v.Rule)
		if v.Rule == "" {
			under = "by the policy's default"
		}
		answer.Reason = fmt.Sprintf("adanext-exec asks %s. The command:\n%s", under, command)
	case userDecision == claudecode.Ask:
		answer.Decision = "ask"
		answer.Reason = fmt.Sprintf("your Claude Code rule %q asks. The command:\n%s", userRule, command)
		if userRule == claudecode.Unchecked {
			answer.Reason = fmt.Sprintf("the command is too long or tangled to check against your Claude Code rules. The command:\n%s", command)
		}
	default:
		answer.Decision = "allow"
		answer.Reason = "adanext-exec allows it; it runs through adanext-exec run"
	}
	exe, err := hookExecutable()
	if err != nil {
		return deny("find the binary for the rewrite: " + err.Error())
	}
	id, err := build()
	if err != nil {
		return deny("identify this build: " + err.Error())
	}
	argv := []string{"run", "--hook-build", id, "--shell", string(shell)}
	if *policy != "" {
		argv = append(argv, "--policy", *policy)
	}
	if *profile != "" {
		argv = append(argv, "--profile", *profile)
	}
	if v.Decision == broker.Ask {
		// One word even for the default's empty rule: Windows PowerShell drops an empty argument.
		argv = append(argv, "--approved="+v.Rule)
	}
	if timeout > 0 {
		argv = append(argv, "--timeout", timeout.String())
	}
	var background bool
	// A call in the background is read as it comes, which shaping would hold back.
	if json.Unmarshal(input["run_in_background"], &background) != nil || !background {
		argv = append(argv, shaping...)
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

// hookTimeout is the call's own timeout, in the milliseconds the tool takes, for the rewrite. At that
// time Claude Code moves a command to the background and leaves it running, so the gate must end
// it. A call already in the background has none: it is meant to outlive its call.
func hookTimeout(input map[string]json.RawMessage) (time.Duration, error) {
	raw, set := input["timeout"]
	if !set || string(raw) == "null" {
		return 0, nil
	}
	var ms float64
	if err := json.Unmarshal(raw, &ms); err != nil || ms <= 0 || math.Ceil(ms) > float64(math.MaxInt64/int64(time.Millisecond)) {
		return 0, errors.New("the tool input's timeout is not a positive number of milliseconds")
	}
	var background bool
	if json.Unmarshal(input["run_in_background"], &background) == nil && background {
		return 0, nil
	}
	return time.Duration(math.Ceil(ms)) * time.Millisecond, nil
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
