package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/cristiangirlea/adanext-exec/broker"
	"github.com/cristiangirlea/adanext-exec/internal/originals"
)

// auditDir is where the audit goes unless --audit-dir says otherwise; tests replace it.
var auditDir = broker.DefaultAuditDir

// run is "adanext-exec run": one command through broker.Run, with the policy and the audit
// always set.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	shell := flags.String("shell", "", "run the one argument after -- as shell text: sh, bash, powershell, pwsh or cmd")
	scriptB64 := flags.String("script-b64", "", "with --shell: the script as URL-safe base64 without padding, instead of an argument after --")
	policy := flags.String("policy", "", "the policy file (default: the user's adanext-exec/policy.json, else the built-in rules)")
	profile := flags.String("profile", "", "the policy in the user's adanext-exec/profiles/<name>.json")
	approved := flags.String("approved", "", "a person approved an ask under this rule (empty: the policy's default); it counts only when the policy answers ask under it")
	audit := flags.String("audit-dir", "", "the audit directory (default: the user's state directory)")
	timeout := flags.Duration("timeout", 0, "kill the command's tree after this long (0: none)")
	outputCap := flags.Int("output-cap", 0, "bound each output stream to this many bytes (0: none)")
	agent := flags.String("agent", "", "who asked, for the audit")
	session := flags.String("session", "", "the caller's session, for the audit")
	runID := flags.String("run-id", "", "the caller's run, for the audit")
	slots := flags.Bool("slots", false, "wait for one of the daemon's heavy-command slots first")
	socket := flags.String("socket", "", "the daemon's socket (default: the user's state directory); implies --slots")
	maxWait := flags.Duration("max-wait", 0, "give up after waiting this long for a slot (0: no limit)")
	hookBuild := flags.String("hook-build", "", "the build of the hook that wrote this command line; run refuses another build's")
	memory := flags.String("memory", "", "a ceiling on the memory of the command and all it starts: bytes, or with K, M or G")
	memoryCeiling := flags.String("memory-ceiling", "", "how --memory is held on Linux: auto, cgroup (by the kernel, or the command is not run) or watchdog (default: the policy's, else auto)")
	shape := flags.Bool("shape", false, "hold both streams as one output until the command ends, and put a checked summary in place of one past --output-cap bytes (default 30000), keeping the original in --originals")
	originalsDir := flags.String("originals", "", "with --shape: the directory that keeps the originals, made when its parent exists")
	originalsRoot := flags.String("originals-root", "", "with --shape: a directory of runs whose originals directories count together with --originals")
	originalsMax := flags.String("originals-max", "", "with --shape: the cap on --originals, in bytes or with K, M or G (default 64M)")
	originalsAllMax := flags.String("originals-all-max", "", "with --shape: the cap on --originals and the root's originals together (default 1G)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return ExitUsage
	}
	rest := flags.Args()
	switch {
	case *timeout < 0:
		return usageFail(stderr, "a negative --timeout")
	case *outputCap < 0:
		return usageFail(stderr, "a negative --output-cap")
	case *maxWait < 0:
		return usageFail(stderr, "a negative --max-wait")
	case *maxWait > 0 && !*slots && *socket == "":
		return usageFail(stderr, "--max-wait needs --slots")
	}
	ceiling := broker.MemoryCeiling(*memoryCeiling)
	switch ceiling {
	case "", broker.CeilingAuto, broker.CeilingCgroup, broker.CeilingWatchdog:
	default:
		return usageFail(stderr, "--memory-ceiling is auto, cgroup or watchdog")
	}
	if *hookBuild != "" {
		id, err := build()
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "adanext-exec: identify this build for the hook's command: %v\n", err)
			return ExitCannotRun
		case id != *hookBuild:
			fmt.Fprintf(stderr, "adanext-exec: the hook that wrote this command is build %s, and this binary is build %s; install one build for both\n", *hookBuild, id)
			return ExitCannotRun
		}
	}
	memoryLimit, err := parseSize(*memory)
	if err != nil {
		return usageFail(stderr, "--memory: "+err.Error())
	}
	var shaping *broker.Shape
	var readBound int64
	switch {
	case *shape && *originalsDir == "":
		return usageFail(stderr, "--shape needs --originals")
	case !*shape && (*originalsDir != "" || *originalsRoot != "" || *originalsMax != "" || *originalsAllMax != ""):
		return usageFail(stderr, "--originals, --originals-root, --originals-max and --originals-all-max need --shape")
	case *shape:
		shaping = &broker.Shape{Dir: *originalsDir, Root: *originalsRoot, Over: *outputCap}
		step := int64(*outputCap)
		if step == 0 {
			step = broker.DefaultShapeOver
		}
		// A read through run --shape is at most a threshold long, the size the caller takes an
		// agent to see whole, and never past the read's own bound.
		readBound = min(step, originals.MaxRead)
		shaping.ReadHint = func(h string, size int64) string {
			return fmt.Sprintf("adanext-exec original %s --range 0-%d reads it, at most %d bytes a read", h, min(size, readBound), readBound)
		}
		if shaping.DirMax, err = parseSize(*originalsMax); err != nil {
			return usageFail(stderr, "--originals-max: "+err.Error())
		}
		if shaping.AllMax, err = parseSize(*originalsAllMax); err != nil {
			return usageFail(stderr, "--originals-all-max: "+err.Error())
		}
		// Under --shape the output cap is the threshold, and the size of a cut.
		*outputCap = 0
	}
	c := broker.Command{
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Timeout: *timeout, OutputCap: *outputCap, MemoryLimit: memoryLimit, MemoryCeiling: ceiling,
		Caller: broker.Caller{Agent: *agent, Session: *session, RunID: *runID},
		Shape:  shaping,
	}
	switch {
	case *scriptB64 != "":
		// The script as base64: what a hook writes, so no shell between it and run reparses it.
		script, err := base64.RawURLEncoding.Strict().DecodeString(*scriptB64)
		switch {
		case *shell == "":
			return usageFail(stderr, "--script-b64 needs --shell")
		case len(rest) != 0:
			return usageFail(stderr, "--script-b64 takes no arguments after it")
		case err != nil || len(script) == 0:
			return usageFail(stderr, "--script-b64 is not URL-safe base64, without padding, of a script")
		}
		c.Shell, c.Script = broker.Shell(*shell), string(script)
	case len(rest) == 0:
		return usageFail(stderr, "no command after --")
	case *shell != "" && len(rest) != 1:
		return usageFail(stderr, "--shell takes the script as the one argument after --")
	case *shell != "":
		c.Shell, c.Script = broker.Shell(*shell), rest[0]
	default:
		c.Args = rest
	}
	// A policy that cannot be used still goes to Run, which refuses the command with its
	// reason and writes the audit line.
	p, err := selectPolicy(*policy, *profile)
	var ue usageError
	switch {
	case errors.As(err, &ue):
		return usageFail(stderr, ue.Error())
	case err != nil && p == nil:
		fmt.Fprintf(stderr, "adanext-exec: %v\n", err)
		p = &broker.Policy{}
	}
	c.Policy = p
	approvedSet := false
	flags.Visit(func(f *flag.Flag) { approvedSet = approvedSet || f.Name == "approved" })
	if approvedSet {
		// An empty RULE approves an ask by the policy's default, which names no rule.
		rule := *approved
		c.Ask = func(_ context.Context, _ broker.Command, v broker.Verdict) (bool, error) {
			return v.Rule == rule, nil
		}
	}
	if read := originalRead(c); shaping != nil && read != nil && readAllowed(p, c, approvedSet, *approved) {
		// Exactly a read of an original, which the line above told the agent to run, and one
		// the policy lets run as written: this binary's own read runs instead of whatever the
		// name finds, bounded by the threshold, neither shaped nor cut.
		exe, err := selfExecutable()
		if err != nil {
			fmt.Fprintf(stderr, "adanext-exec: find this binary to read the original: %v\n", err)
			return ExitCannotRun
		}
		dir, err := filepath.Abs(*originalsDir)
		if err != nil {
			fmt.Fprintf(stderr, "adanext-exec: %v\n", err)
			return ExitCannotRun
		}
		c.Args = append(append([]string{exe, "original"}, read...), "--originals", dir, "--max", strconv.FormatInt(readBound, 10))
		c.Shell, c.Script, c.Shape = "", "", nil
	}
	dir := *audit
	if dir == "" {
		if dir, err = auditDir(); err != nil {
			fmt.Fprintf(stderr, "adanext-exec: %v: %v\n", broker.ErrAudit, err)
			return ExitCannotRun
		}
	}
	c.Audit = &broker.Audit{Dir: dir}
	if *slots || *socket != "" {
		path := *socket
		if path == "" {
			if path, err = defaultSocket(); err != nil {
				fmt.Fprintf(stderr, "adanext-exec: %v: %v\n", broker.ErrNoDaemon, err)
				return ExitNoDaemon
			}
		}
		c.Slots = &broker.Slots{Socket: path, MaxWait: *maxWait, Waiting: func(p int) {
			fmt.Fprintf(stderr, "adanext-exec: waiting for a heavy-command slot (place %d)\n", p)
		}}
	}

	res, err := broker.Run(ctx, c)
	ran := res != (broker.Result{})
	if err != nil {
		fmt.Fprintf(stderr, "adanext-exec: %v\n", err)
	}
	if !ran {
		var refused *broker.RefusedError
		switch {
		case errors.As(err, &refused), errors.Is(err, broker.ErrPolicy):
			return ExitRefused
		case programNotFound(err):
			return ExitNotFound
		case errors.Is(err, broker.ErrNoDaemon), errors.Is(err, broker.ErrSlotLost):
			return ExitNoDaemon
		case errors.Is(err, broker.ErrNoSlot):
			return ExitTimeout
		case errors.Is(err, context.Canceled):
			return ExitInterrupted
		case errors.Is(err, context.DeadlineExceeded):
			return ExitTimeout
		}
		return ExitCannotRun
	}
	if res.OriginalRefused != "" {
		fmt.Fprintf(stderr, "adanext-exec: the original was not kept (%s); the output was cut\n", res.OriginalRefused)
	}
	if memoryLimit > 0 && !res.MemoryEnforced {
		fmt.Fprintln(stderr, "adanext-exec: the memory ceiling is not enforced on this platform; the command ran without it")
	}
	return ranCode(res, err)
}

// readAllowed reports whether the policy runs the agent's own text of a read: allowed, or asked
// about under the rule --approved names. Anything else is left as written, for Run to refuse.
func readAllowed(p *broker.Policy, c broker.Command, approvedSet bool, approved string) bool {
	v, err := p.Check(c)
	return err == nil && (v.Decision == broker.Allow || v.Decision == broker.Ask && approvedSet && v.Rule == approved)
}

// ranCode is the exit code of a command that ran, given its result and Run's error: a lost slot,
// then a failure after the run, then the ceiling, the timeout, an interruption and a signal,
// and otherwise the command's own code.
func ranCode(res broker.Result, err error) int {
	switch {
	case errors.Is(err, broker.ErrSlotLost):
		// The daemon went away while the command held its slot, and the command was killed.
		return ExitNoDaemon
	case err != nil:
		// It ran, but something after it failed: its output, its audit line, or killing what
		// it left running. Its own code would hide that, and so would the memory ceiling's.
		return ExitBrokerFailed
	case res.MemoryExceeded:
		return ExitMemory
	case res.TimedOut:
		return ExitTimeout
	case res.Canceled:
		return ExitInterrupted
	case res.Signal != "":
		if n := signalNumber(res.Signal); n > 0 {
			return ExitSignal + n
		}
		return ExitSignal
	}
	return res.ExitCode
}

// programNotFound tells a command that did not run because its program or shell is missing:
// not found in PATH, or started by a path where nothing is. The start error names the path it
// tried; another missing file behind the same error (a working directory, say) leaves that
// path in place, so it is not the program's absence.
func programNotFound(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var pe *fs.PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pe) || pe.Path == "" {
		return false
	}
	_, statErr := os.Stat(pe.Path)
	return errors.Is(statErr, fs.ErrNotExist)
}

func usageFail(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "adanext-exec run: %s\n", msg)
	return ExitUsage
}
