package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/cristiangirlea/adanext-exec/broker"
	"github.com/cristiangirlea/adanext-exec/internal/originals"
)

// selfExecutable is this binary, which runs an original's read for run --shape; tests replace it.
var selfExecutable = os.Executable

// original is "adanext-exec original": prints an original that run --shape kept, or bytes a to
// b of it, at most originals.MaxRead at a time.
func original(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("original", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rng := flags.String("range", "", fmt.Sprintf("bytes a-b of the original, zero-based and end exclusive, at most %d of them", originals.MaxRead))
	dir := flags.String("originals", "", "the directory that keeps the originals")
	fail := func(msg string) int {
		fmt.Fprintf(stderr, "adanext-exec original: %s\n", msg)
		return ExitUsage
	}
	handle := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		handle, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return ExitUsage
	}
	switch {
	case handle == "" && flags.NArg() == 1:
		handle = flags.Arg(0)
	case flags.NArg() != 0:
		return fail("one handle, and only flags after it")
	}
	switch {
	case handle == "":
		return fail("no handle")
	case *dir == "":
		return fail("no --originals")
	}
	data, _, err := originals.Read(*dir, handle, *rng)
	switch {
	case errors.Is(err, originals.ErrHandle), errors.Is(err, originals.ErrRange):
		return fail(err.Error())
	case err != nil:
		fmt.Fprintf(stderr, "adanext-exec original: %v\n", err)
		return ExitCannotRun
	}
	if _, err := stdout.Write(data); err != nil {
		fmt.Fprintf(stderr, "adanext-exec original: write the original: %v\n", err)
		return ExitBrokerFailed
	}
	return 0
}

// originalReadText is shell text that only reads an original: the bare program name, so no
// path can name another program, and nothing a shell would read as more than words.
var originalReadText = regexp.MustCompile(`^\s*adanext-exec(?:\.exe)?[ \t]+original[ \t]+([0-9a-f]{16})(?:[ \t]+--range(?:[ \t]+|=)([0-9]{1,18}-[0-9]{1,18}))?\s*$`)

var (
	handleWord = regexp.MustCompile(`^[0-9a-f]{16}$`)
	rangeWord  = regexp.MustCompile(`^[0-9]{1,18}-[0-9]{1,18}$`)
)

// originalRead returns the handle, and "--range" with its range when one is given, when c is
// exactly a read of an original, as the line run --shape writes names it; otherwise nil.
func originalRead(c broker.Command) []string {
	if c.Shell != "" {
		m := originalReadText.FindStringSubmatch(c.Script)
		switch {
		case m == nil:
			return nil
		case m[2] != "":
			return []string{m[1], "--range", m[2]}
		}
		return []string{m[1]}
	}
	a := c.Args
	if len(a) < 3 || (a[0] != "adanext-exec" && a[0] != "adanext-exec.exe") || a[1] != "original" || !handleWord.MatchString(a[2]) {
		return nil
	}
	switch rest := a[3:]; {
	case len(rest) == 0:
		return []string{a[2]}
	case len(rest) == 2 && rest[0] == "--range" && rangeWord.MatchString(rest[1]):
		return []string{a[2], "--range", rest[1]}
	case len(rest) == 1 && strings.HasPrefix(rest[0], "--range=") && rangeWord.MatchString(rest[0][len("--range="):]):
		return []string{a[2], "--range", rest[0][len("--range="):]}
	}
	return nil
}
