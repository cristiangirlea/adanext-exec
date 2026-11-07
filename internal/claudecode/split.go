package claudecode

import "strings"

// token is one word of a command, as written and with quotes and escapes taken off, or an
// unquoted brace, which opens or closes a block.
type token struct {
	raw, text string
	brace     bool
}

// command is one command of shell text: its text as written, and its tokens.
type command struct {
	span   string
	tokens []token
}

// split cuts shell text into its commands: at &&, ||, ;, |, &, and new lines outside quotes. A
// command substitution, $( ) and, in Bash, a pair of backticks (also inside double quotes), and
// a parenthesised group are commands of their own, and the command around them goes on after
// them. Unquoted braces are tokens of their own. The escape character is a backslash in Bash
// and a backtick in PowerShell. It reads plain command lines, not every form a shell has; where
// it is unsure it cuts, so a rule is tried on more pieces, not fewer.
func split(powershell bool, script string) []command {
	escape, backticks := '\\', true
	if powershell {
		escape, backticks = '`', false
	}
	// A frame is an open substitution, backtick pair or group, with the command it interrupts.
	type frame struct {
		kind      byte // '$' a substitution, '`' a backtick pair, '(' a group
		quote     rune
		tokens    []token
		span      string
		raw, text string
		inWord    bool
	}
	var (
		frames    []frame
		cmds      []command
		tokens    []token
		span      strings.Builder
		raw, text strings.Builder
		inWord    bool
		quote     rune
	)
	flushWord := func() {
		if inWord {
			tokens = append(tokens, token{raw: raw.String(), text: text.String()})
			raw.Reset()
			text.Reset()
			inWord = false
		}
	}
	flushCmd := func() {
		flushWord()
		if len(tokens) > 0 {
			cmds = append(cmds, command{span: strings.TrimSpace(span.String()), tokens: tokens})
		}
		tokens = nil
		span.Reset()
	}
	open := func(kind byte) {
		frames = append(frames, frame{kind, quote, tokens, span.String(), raw.String(), text.String(), inWord})
		tokens, quote, inWord = nil, 0, false
		span.Reset()
		raw.Reset()
		text.Reset()
	}
	closeFrame := func() {
		flushCmd()
		f := frames[len(frames)-1]
		frames = frames[:len(frames)-1]
		tokens, quote, inWord = f.tokens, f.quote, f.inWord
		span.WriteString(f.span)
		raw.WriteString(f.raw)
		text.WriteString(f.text)
	}
	const single = rune(39)
	rs := []rune(script)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == escape && quote != single && i+1 < len(rs):
			span.WriteRune(r)
			raw.WriteRune(r)
			i++
			span.WriteRune(rs[i])
			raw.WriteRune(rs[i])
			text.WriteRune(rs[i])
			inWord = true
		case quote != single && r == '$' && i+1 < len(rs) && rs[i+1] == '(':
			open('$')
			i++
		case backticks && quote != single && r == '`':
			if n := len(frames); n > 0 && frames[n-1].kind == '`' {
				closeFrame()
			} else {
				open('`')
			}
		case quote != 0:
			span.WriteRune(r)
			raw.WriteRune(r)
			inWord = true
			if r == quote {
				quote = 0
			} else {
				text.WriteRune(r)
			}
		case r == single || r == '"':
			span.WriteRune(r)
			raw.WriteRune(r)
			quote = r
			inWord = true
		case r == '(':
			open('(')
		case r == ')':
			if n := len(frames); n > 0 && frames[n-1].kind != '`' {
				closeFrame()
			} else {
				flushCmd()
			}
		case r == '&' || r == '|' || r == ';' || r == '\n' || r == '\r':
			flushCmd()
		case r == '{' || r == '}':
			span.WriteRune(r)
			flushWord()
			tokens = append(tokens, token{raw: string(r), text: string(r), brace: true})
		case r == ' ' || r == '\t':
			span.WriteRune(r)
			flushWord()
		default:
			span.WriteRune(r)
			raw.WriteRune(r)
			text.WriteRune(r)
			inWord = true
		}
	}
	for len(frames) > 0 {
		closeFrame()
	}
	flushCmd()
	return cmds
}
