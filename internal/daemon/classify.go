package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// tool says which of a program's commands are heavy: any (all of them), or the subcommands
// listed; valueFlags are its global flags that take the next word as their value.
type tool struct {
	any        bool
	sub        []string
	valueFlags []string
}

// heavyTools are the built-in heavy commands: builds, test runners and package installs, the
// commands that load every core or the disk.
var heavyTools = map[string]tool{
	"go":         {sub: []string{"build", "test", "vet", "install", "generate"}, valueFlags: []string{"-C"}},
	"cargo":      {sub: []string{"build", "test", "check", "clippy", "bench", "run", "install"}, valueFlags: []string{"-C", "--manifest-path", "--config", "-Z"}},
	"npm":        {sub: []string{"test", "t", "ci", "install", "i", "build", "rebuild"}, valueFlags: []string{"--prefix", "-w", "--workspace", "-C"}},
	"pnpm":       {sub: []string{"test", "t", "install", "i", "build", "rebuild"}, valueFlags: []string{"--filter", "-F", "-C", "--dir"}},
	"yarn":       {sub: []string{"test", "install", "build"}, valueFlags: []string{"--cwd"}},
	"bun":        {sub: []string{"test", "install", "i", "build"}, valueFlags: []string{"--cwd"}},
	"gradle":     {any: true},
	"gradlew":    {any: true},
	"mvn":        {any: true},
	"mvnw":       {any: true},
	"make":       {any: true},
	"gmake":      {any: true},
	"ninja":      {any: true},
	"msbuild":    {any: true},
	"bazel":      {sub: []string{"build", "test", "run", "coverage"}},
	"dotnet":     {sub: []string{"build", "test", "publish", "pack", "restore"}},
	"pytest":     {any: true},
	"tox":        {any: true},
	"nox":        {any: true},
	"tsc":        {any: true},
	"jest":       {any: true},
	"vitest":     {any: true},
	"playwright": {sub: []string{"test"}},
}

// runners run a package script: the script is heavy when its name says build or test.
var runners = []string{"npm", "pnpm", "yarn", "bun"}

// Classifier tells heavy commands from light ones.
type Classifier struct {
	defaults bool
	patterns []*regexp.Regexp
}

// NewClassifier makes a classifier from the built-in heavy commands (when defaults is true)
// and patterns of its own, Go regular expressions matched against the command line.
func NewClassifier(defaults bool, patterns []string) (*Classifier, error) {
	c := &Classifier{defaults: defaults}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("heavy pattern %q: %v", p, err)
		}
		c.patterns = append(c.patterns, re)
	}
	return c, nil
}

// Heavy reports whether an acquire's command needs a slot.
//
// The built-in classification reads a command the way its program does: the program by its
// base name without .exe, .cmd or .bat, its global flags skipped (with their values), then its
// subcommand, so go -C dir test and npm --prefix web run build are builds, while git grep make
// and echo "go test" are not. Shell text is read command by command, split at &&, ||, ;, |
// and new lines, with leading VAR=value assignments skipped. A pattern of the configuration's
// own matches the whole command line, as written.
func (c *Classifier) Heavy(m Message) bool {
	line := m.Script
	if len(m.Args) > 0 {
		line = strings.Join(m.Args, " ")
	}
	for _, re := range c.patterns {
		if re.MatchString(line) {
			return true
		}
	}
	if !c.defaults {
		return false
	}
	if len(m.Args) > 0 {
		return heavyArgv(m.Args)
	}
	for _, words := range shellCommands(m.Shell, m.Script) {
		if heavyArgv(words) {
			return true
		}
	}
	return false
}

// wrappers run the command that follows them; their own flags (and, for some, the flag's
// value) come first.
var wrappers = map[string][]string{
	"time":    {"-o", "--output", "-f", "--format"},
	"env":     {"-u", "--unset", "-C", "--chdir", "-S", "--split-string"},
	"sudo":    {"-u", "--user", "-g", "--group", "-h", "--host", "-p", "--prompt", "-C", "--close-from", "-D", "--chdir", "-r", "--role", "-t", "--type", "-U", "--other-user"},
	"nice":    {"-n", "--adjustment"},
	"nohup":   nil,
	"exec":    {"-a"},
	"command": nil,
	"xargs":   {"-n", "-P", "-I", "-L", "-s", "-d", "-E", "-a"},
	"ionice":  {"-c", "-n", "-p"},
	"timeout": {"-s", "--signal", "-k", "--kill-after"},
}

// executors run a package's tool: the word after them (past their flags) is the program.
var executors = map[string][]string{
	"npx":  {"-p", "--package", "-c", "--call"},
	"bunx": {"-p", "--package"},
}

// heavyArgv applies the built-in table to one command's words.
func heavyArgv(words []string) bool {
	for {
		for len(words) > 0 && isAssignment(words[0]) {
			words = words[1:]
		}
		if len(words) == 0 {
			return false
		}
		valueFlags, ok := wrappers[programName(words[0])]
		if !ok {
			break
		}
		// timeout takes its duration as the first word that is not a flag.
		skipFirst := programName(words[0]) == "timeout"
		words = words[1:]
		for len(words) > 0 && (strings.HasPrefix(words[0], "-") || isAssignment(words[0])) {
			if slices.Contains(valueFlags, words[0]) && len(words) > 1 {
				words = words[1:]
			}
			words = words[1:]
		}
		if skipFirst && len(words) > 0 {
			words = words[1:]
		}
	}
	prog := programName(words[0])
	args := words[1:]
	switch prog {
	case "python", "python3", "py":
		// python -m pytest, python -m tox.
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-m" {
				return args[i+1] == "pytest" || args[i+1] == "tox"
			}
		}
		return false
	case "cmake":
		return slices.Contains(args, "--build")
	case "docker":
		sub, rest := subcommand(args, nil)
		switch sub {
		case "build":
			return true
		case "compose", "buildx":
			next, _ := subcommand(rest, nil)
			return next == "build"
		}
		return false
	}
	if flags, ok := executors[prog]; ok {
		return heavyArgv(afterFlags(args, flags))
	}
	t, ok := heavyTools[prog]
	if !ok {
		return false
	}
	if t.any {
		return true
	}
	sub, rest := subcommand(args, t.valueFlags)
	if slices.Contains(t.sub, sub) {
		return true
	}
	if slices.Contains(runners, prog) {
		switch sub {
		case "exec", "dlx":
			// pnpm exec tsc, yarn exec jest: the tool is the program.
			return heavyArgv(afterFlags(rest, t.valueFlags))
		case "run", "run-script":
			script, _ := subcommand(rest, t.valueFlags)
			return strings.Contains(script, "build") || strings.Contains(script, "test")
		}
	}
	return false
}

// afterFlags is args from the first word that is not a flag, skipping a flag's value when the
// flag is one of valueFlags.
func afterFlags(args, valueFlags []string) []string {
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			return args[i:]
		}
		if slices.Contains(valueFlags, args[i]) {
			i++
		}
	}
	return nil
}

// subcommand is the first word that is not a flag, skipping a flag's value when the flag is
// one of valueFlags (and has no =value of its own), and the words after it. A +word (cargo's
// +toolchain) is passed over as a flag.
func subcommand(args, valueFlags []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
			return a, args[i+1:]
		}
		if slices.Contains(valueFlags, a) {
			i++
		}
	}
	return "", nil
}

func isAssignment(w string) bool {
	k, _, ok := strings.Cut(w, "=")
	if !ok || k == "" {
		return false
	}
	for _, r := range k {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// shellCommands splits shell text into its commands' words: at &&, ||, ;, |, &, parentheses and
// new lines, words at white space, quotes taken off. A command substitution is read as a command
// of its own, also inside double quotes: $( ) in sh, bash and PowerShell, and a pair of
// backticks in sh and bash. The escape character is the shell's: a backslash in sh and bash, a
// backtick in PowerShell, a caret outside quotes in cmd. It reads plain command lines, not
// every form a shell has.
func shellCommands(shell, script string) [][]string {
	var escape rune
	subst, backticks := false, false
	switch shell {
	case "sh", "bash":
		escape, subst, backticks = '\\', true, true
	case "powershell", "pwsh":
		escape, subst = '`', true
	case "cmd":
		escape = '^'
	}
	// A frame is an open substitution, backtick pair or group, with the quote it opened in, to
	// go back to when it closes.
	type frame struct {
		kind  byte // '$' a substitution, '`' a backtick pair, '(' a group
		quote rune
	}
	var frames []frame
	var cmds [][]string
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	flushWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	flushCmd := func() {
		flushWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	rs := []rune(script)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case escape != 0 && r == escape && quote != '\'' && !(shell == "cmd" && quote != 0) && i+1 < len(rs):
			i++
			word.WriteRune(rs[i])
			inWord = true
		case subst && quote != '\'' && r == '$' && i+1 < len(rs) && rs[i+1] == '(':
			flushCmd()
			frames = append(frames, frame{'$', quote})
			quote = 0
			i++
		case backticks && quote != '\'' && r == '`':
			flushCmd()
			if n := len(frames); n > 0 && frames[n-1].kind == '`' {
				quote = frames[n-1].quote
				frames = frames[:n-1]
			} else {
				frames = append(frames, frame{'`', quote})
				quote = 0
			}
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == '(':
			flushCmd()
			frames = append(frames, frame{'(', 0})
		case r == ')':
			flushCmd()
			if n := len(frames); n > 0 && frames[n-1].kind != '`' {
				quote = frames[n-1].quote
				frames = frames[:n-1]
			}
		case r == '&' || r == '|' || r == ';' || r == '\n' || r == '\r':
			flushCmd()
		case r == ' ' || r == '\t':
			flushWord()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	flushCmd()
	return cmds
}

// ShellCommands is shellCommands for the hook's matching of Claude Code's rules.
func ShellCommands(shell, script string) [][]string { return shellCommands(shell, script) }

func programName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		if strings.HasSuffix(strings.ToLower(p), ext) {
			return p[:len(p)-len(ext)]
		}
	}
	return p
}

// ParallelHints are the hints sent with a slot: each heavy command gets its share of the CPUs,
// NumCPU divided by the slots, at least one, as the parallelism of Go, make and cargo.
func ParallelHints(slots int) func(Message) []string {
	k := strconv.Itoa(max(1, runtime.NumCPU()/max(1, slots)))
	hints := []string{"GOFLAGS=-p=" + k, "GOMAXPROCS=" + k, "MAKEFLAGS=-j" + k, "CARGO_BUILD_JOBS=" + k}
	return func(Message) []string { return hints }
}

// Config is the daemon's configuration file.
type Config struct {
	Version int `json:"version"`
	// Heavy adds patterns (Go regular expressions, matched against the command line) of heavy
	// commands to the built-in ones, or, with Only, replaces them.
	Heavy []string `json:"heavy"`
	Only  bool     `json:"only"`
}

// maxConfigSize bounds what LoadConfig reads.
const maxConfigSize = 1 << 20

// LoadConfig reads the daemon's configuration and makes its classifier. The file must be a
// regular file of at most 1 MiB, and on Linux and macOS the user's and writable by no one else,
// as the policy file is; anything in it the daemon does not know is an error, a key spelled in
// another case included.
func LoadConfig(path string) (*Classifier, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|openNonBlock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if err := onlyOwnerWrites(path, fi); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxConfigSize)
	}
	if err := strict(data); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	// Go's decoding matches field names in any case; the file's keys must be exactly these.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	for k := range raw {
		if k != "version" && k != "heavy" && k != "only" {
			return nil, fmt.Errorf("%s: unknown key %q", path, k)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if c.Version != 1 {
		return nil, fmt.Errorf("%s: version %d, want 1", path, c.Version)
	}
	return NewClassifier(!c.Only, c.Heavy)
}
