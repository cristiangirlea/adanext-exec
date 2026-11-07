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

// maxReadings bounds the text of all the readings of one command, and of the keys that tell
// them apart, so a long or tangled command cannot hold the hook: Claude Code runs a command
// whose hook times out.
const maxReadings = 1 << 21

// readings are the texts a command is matched as; a rule that matches any of them matches the
// command. The text is read several ways: whole; cut at every separator, substitution, bracket,
// backtick and new line regardless of quotes and comments; and each of those split into commands
// by a reader that keeps quotes (braces read as blocks, and again as letters). Each command is
// read as written and with quotes taken off, and so are the forms with what runs it taken off
// (leading assignments, shell keywords, wrappers) and, in PowerShell, with an alias read as its
// cmdlet. Every reading only adds a way to match, so a rule matches more text than in Claude
// Code, never on purpose less. Like Claude Code's own rules, it reads text: it is not a parser
// of every form a shell has. It is not complete when the readings would pass maxReadings.
func readings(ps bool, command string) ([]string, bool) {
	var texts []string
	seen := map[string]bool{}
	budget := maxReadings
	add := func(s string) bool {
		if seen[s] {
			return true
		}
		if budget -= len(s); budget < 0 {
			return false
		}
		seen[s] = true
		texts = append(texts, s)
		return true
	}
	for _, text := range append([]string{command}, cuts.Split(command, -1)...) {
		if !add(strings.TrimSpace(text)) {
			return texts, false
		}
		for _, braces := range []bool{true, false} {
			for _, c := range split(ps, text, braces) {
				if !add(c.span) {
					return texts, false
				}
				fs, complete := forms(ps, c.tokens, &budget)
				if !complete {
					return texts, false
				}
				for _, form := range fs {
					if !add(join(form, true)) || !add(join(form, false)) {
						return texts, false
					}
				}
			}
		}
	}
	return texts, true
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

// forms are the token lists a command is matched as: the command without its braces, each
// piece between braces (a block's commands), and of each the forms with what runs it taken off.
// Their keys are taken from the budget; it is not complete when the budget runs out.
func forms(ps bool, tokens []token, budget *int) ([][]token, bool) {
	var out [][]token
	seen := map[string]bool{}
	var walk func(w []token)
	walk = func(w []token) {
		if len(w) == 0 || *budget < 0 {
			return
		}
		key := join(w, true) + string(rune(0)) + join(w, false)
		if *budget -= len(key); *budget < 0 {
			return
		}
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
	return out, *budget >= 0
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
