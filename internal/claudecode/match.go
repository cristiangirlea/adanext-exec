package claudecode

import (
	"regexp"
	"strings"

	"github.com/cristiangirlea/adanext-exec/internal/daemon"
)

// rule is one Bash or PowerShell permission rule: Tool or Tool(pattern).
type rule struct {
	text, tool string
	// re is the pattern; nil matches every command of the tool.
	re *regexp.Regexp
}

// parseRule reads a rule for Bash or PowerShell; a rule for another tool is left out.
func parseRule(text string) (rule, bool) {
	tool, spec, hasSpec := strings.Cut(text, "(")
	tool = strings.TrimSpace(tool)
	if tool != "Bash" && tool != "PowerShell" {
		return rule{}, false
	}
	r := rule{text: text, tool: tool}
	if !hasSpec {
		return r, true
	}
	spec, ok := strings.CutSuffix(spec, ")")
	if !ok {
		return rule{}, false
	}
	if spec == "*" || spec == "" {
		return r, true
	}
	// A trailing " *" or ":*" also matches the command with nothing after it.
	prefix := false
	for _, suffix := range []string{" *", ":*"} {
		if s, ok := strings.CutSuffix(spec, suffix); ok {
			spec, prefix = s, true
			break
		}
	}
	parts := strings.Split(spec, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	pattern := "(?s)^" + strings.Join(parts, ".*")
	if prefix {
		pattern += "(?: .*)?"
	}
	pattern += "$"
	if tool == "PowerShell" {
		pattern = "(?i)" + pattern
	}
	r.re = regexp.MustCompile(pattern)
	return r, true
}

// matches reports whether the rule matches the command or any command within it: the text as
// written, whole and between separators; and each command of a compound one, of a group, a
// block, a substitution or a loop's body, with quotes taken off, as written and with what runs
// it taken off (leading assignments, shell keywords, and wrappers). PowerShell's aliases count
// as the cmdlets they name. It reads more text as a match than Claude Code does, never on
// purpose less.
func (r rule) matches(command string) bool {
	if r.re == nil {
		return true
	}
	for _, seg := range separators.Split(command, -1) {
		if seg = strings.TrimSpace(seg); seg != "" && r.re.MatchString(seg) {
			return true
		}
	}
	if r.re.MatchString(strings.TrimSpace(command)) {
		return true
	}
	shell := "bash"
	if r.tool == "PowerShell" {
		shell = "pwsh"
	}
	for _, words := range daemon.ShellCommands(shell, command) {
		for _, form := range forms(r.tool, words) {
			if r.re.MatchString(strings.Join(form, " ")) {
				return true
			}
		}
	}
	return false
}

// separators split the text as written into the pieces a rule with quotes in it is matched
// against. Splitting inside quotes too only adds pieces.
var separators = regexp.MustCompile("&&|\\|\\||\\$\\(|[;|&{}()`\\r\\n]")

// wrappers run the command after their own arguments. Their options are not parsed: the command
// is looked for at every word after the wrapper.
var wrappers = map[string]bool{"timeout": true, "time": true, "nice": true, "nohup": true, "stdbuf": true, "command": true, "builtin": true, "noglob": true, "xargs": true}

// keywords start a command without being it, in Bash and in PowerShell (lower case).
var keywords = map[string]bool{
	"!": true, "if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true,
	"elseif": true, "foreach": true, "for": true, "switch": true, "try": true, "catch": true, "finally": true, "trap": true,
}

// forms are the ways a command's words are matched: as written, from each block it opens, and
// with what runs it taken off.
func forms(tool string, words []string) [][]string {
	var out [][]string
	seen := map[string]bool{}
	var walk func(w []string)
	walk = func(w []string) {
		w = trimClosing(w)
		key := strings.Join(w, string(rune(0)))
		if len(w) == 0 || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, w)
		// A block's commands start after the brace that opens it.
		for i, x := range w {
			if rest, ok := strings.CutPrefix(x, "{"); ok {
				next := w[i+1:]
				if rest != "" {
					next = append([]string{rest}, next...)
				}
				walk(next)
			}
		}
		first := strings.ToLower(w[0])
		if tool == "PowerShell" {
			if cmdlet, ok := aliases[first]; ok {
				walk(append([]string{cmdlet}, w[1:]...))
			}
		}
		switch {
		case keywords[first] || isAssignment(w[0]):
			walk(w[1:])
		case wrappers[w[0]] && !(w[0] == "command" && len(w) > 1 && (w[1] == "-v" || w[1] == "-V")):
			for i := 1; i < len(w); i++ {
				walk(w[i:])
			}
		}
	}
	walk(words)
	return out
}

// trimClosing takes off the braces that close blocks at the end of a command.
func trimClosing(w []string) []string {
	for len(w) > 0 {
		last := strings.TrimRight(w[len(w)-1], "}")
		if last == w[len(w)-1] {
			return w
		}
		w = w[:len(w)-1]
		if last != "" {
			return append(w[:len(w):len(w)], last)
		}
	}
	return w
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
