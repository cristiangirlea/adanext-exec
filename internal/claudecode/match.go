package claudecode

import (
	"regexp"
	"strings"
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

// matches reports whether the rule matches the command or any command within it. The text is
// read several ways, and a match in any counts: whole; cut at every separator, substitution,
// bracket, backtick and new line regardless of quotes and comments; and each of those split
// into commands by a reader that keeps quotes (braces read as blocks, and again as letters).
// Each command is tried as written and with quotes taken off, and so are the forms with what
// runs it taken off (leading assignments, shell keywords, wrappers) and, in PowerShell, with an
// alias read as its cmdlet. Every reading only adds a way to match, so it matches more text than
// Claude Code does, never on purpose less. Like Claude Code's own rules, it reads text: it is
// not a parser of every form a shell has.
func (r rule) matches(command string) bool {
	if r.re == nil {
		return true
	}
	ps := r.tool == "PowerShell"
	tried := map[string]bool{}
	try := func(s string) bool {
		if tried[s] {
			return false
		}
		tried[s] = true
		return r.re.MatchString(s)
	}
	texts := append([]string{command}, cuts.Split(command, -1)...)
	for _, text := range texts {
		if try(strings.TrimSpace(text)) {
			return true
		}
		for _, braces := range []bool{true, false} {
			for _, c := range split(ps, text, braces) {
				if try(c.span) {
					return true
				}
				fs, complete := forms(ps, c.tokens)
				if !complete {
					// Too many readings to try: counted as a match, so the rule applies.
					return true
				}
				for _, form := range fs {
					if try(join(form, true)) || try(join(form, false)) {
						return true
					}
				}
			}
		}
	}
	return false
}

// cuts are where the text is cut for the reading that ignores quotes and comments; a comment
// ends at a new line, which is a cut.
var cuts = regexp.MustCompile("&&|\\|\\||\\$\\(|[;|&{}()`\\r\\n]")

// join is the tokens with one space between, as written or with quotes taken off.
func join(ts []token, raw bool) string {
	words := make([]string, len(ts))
	for i, t := range ts {
		words[i] = t.text
		if raw {
			words[i] = t.raw
		}
	}
	return strings.Join(words, " ")
}

// wrappers run the command after their own arguments. Their options are not parsed: the command
// is looked for at every word after the wrapper.
var wrappers = map[string]bool{"timeout": true, "time": true, "nice": true, "nohup": true, "stdbuf": true, "command": true, "builtin": true, "noglob": true, "xargs": true}

// keywords start a command without being it, in Bash and in PowerShell (lower case).
var keywords = map[string]bool{
	"!": true, "if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true,
	"elseif": true, "foreach": true, "for": true, "switch": true, "try": true, "catch": true, "finally": true, "trap": true,
}

// maxForms bounds the readings of one command, so a long or tangled command cannot hold the
// hook: Claude Code runs a command whose hook times out.
const maxForms = 4096

// forms are the token lists a command is matched as: the command without its braces, each
// piece between braces (a block's commands), and of each the forms with what runs it taken off.
// It is not complete when there were more than maxForms to try.
func forms(ps bool, tokens []token) ([][]token, bool) {
	var out [][]token
	seen := map[string]bool{}
	visits := 0
	var walk func(w []token)
	walk = func(w []token) {
		if len(w) == 0 || visits > maxForms {
			return
		}
		visits++
		key := join(w, true) + string(rune(0)) + join(w, false)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, w)
		// A closing brace stuck to the last word, quoted or not, also read as not there.
		if last := w[len(w)-1]; strings.HasSuffix(last.text, "}") {
			rest := append([]token(nil), w[:len(w)-1]...)
			if text := strings.TrimRight(last.text, "}"); text != "" {
				rest = append(rest, token{raw: strings.TrimRight(last.raw, "}"), text: text})
			}
			walk(rest)
		}
		first := strings.ToLower(w[0].text)
		if cmdlet, ok := aliases[first]; ok && ps {
			walk(append([]token{{raw: cmdlet, text: cmdlet}}, w[1:]...))
		}
		switch {
		case keywords[first] || isAssignment(w[0].text):
			walk(w[1:])
		case wrappers[w[0].text] && !(w[0].text == "command" && len(w) > 1 && (w[1].text == "-v" || w[1].text == "-V")):
			for i := 1; i < len(w); i++ {
				walk(w[i:])
			}
		}
	}
	var whole, piece []token
	for _, t := range tokens {
		if t.brace {
			walk(piece)
			piece = nil
			continue
		}
		whole = append(whole, t)
		piece = append(piece, t)
	}
	walk(piece)
	walk(whole)
	return out, visits <= maxForms
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
