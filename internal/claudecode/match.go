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

// matches reports whether the rule matches the command or any command within it: each command
// of a compound one, of a group, a substitution or a loop's body, as written and with what runs
// it taken off (leading assignments, shell keywords, and the wrappers Claude Code looks
// through). PowerShell's aliases count as the cmdlets they name. It reads more text as a match
// than Claude Code does (quotes are taken off), never on purpose less.
func (r rule) matches(command string) bool {
	if r.re == nil {
		return true
	}
	shell := "bash"
	if r.tool == "PowerShell" {
		shell = "pwsh"
	}
	for _, words := range daemon.ShellCommands(shell, command) {
		for _, form := range forms(r.tool, words) {
			if len(form) > 0 && r.re.MatchString(strings.Join(form, " ")) {
				return true
			}
		}
	}
	return false
}

// forms are the ways a command's words are matched: as written, and with what runs it taken off.
func forms(tool string, words []string) [][]string {
	out := [][]string{words}
	if tool == "PowerShell" {
		if cmdlet, ok := aliases[strings.ToLower(words[0])]; ok {
			out = append(out, append([]string{cmdlet}, words[1:]...))
		}
		return out
	}
	for {
		next := unwrap(words)
		if len(next) == len(words) || len(next) == 0 {
			return out
		}
		words = next
		out = append(out, words)
	}
}

// keywords start a command without being it.
var keywords = map[string]bool{"!": true, "{": true, "}": true, "if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true, "fi": true, "done": true}

// unwrap takes one layer off the front of a command: an assignment, a keyword, or a wrapper with
// its own flags.
func unwrap(w []string) []string {
	if isAssignment(w[0]) || keywords[w[0]] {
		return w[1:]
	}
	rest := w[1:]
	// skipFlags drops flags, and the value of those named, which come before the command.
	skipFlags := func(withValue ...string) []string {
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			f := rest[0]
			rest = rest[1:]
			for _, v := range withValue {
				if f == v && len(rest) > 0 {
					rest = rest[1:]
				}
			}
		}
		return rest
	}
	switch w[0] {
	case "time", "nohup", "noglob", "builtin":
		return skipFlags()
	case "command":
		if len(rest) > 0 && (rest[0] == "-v" || rest[0] == "-V") {
			return w
		}
		return skipFlags()
	case "nice":
		return skipFlags("-n")
	case "stdbuf":
		return skipFlags("-i", "-o", "-e")
	case "timeout":
		rest = skipFlags("-s", "-k")
		if len(rest) > 0 {
			return rest[1:]
		}
		return rest
	case "xargs":
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			return rest
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
