package broker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// askPolicy answers ask for the helper and allows the rest.
const askPolicy = `{"version": 1, "rules": [{"name": "helper", "match": "TestHelperProcess", "decision": "ask"}]}`

// runAsked runs the marker helper under askPolicy with an asker and an audit, and reports
// whether it ran, the error, the audit line and how many times the asker was called.
func runAsked(t *testing.T, policy string, ctx context.Context, ask func(context.Context, Command, Verdict) (bool, error)) (error, bool, map[string]any, int) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
	c.Policy = mustPolicy(t, policy)
	c.Audit = &Audit{Dir: dir}
	calls := 0
	if ask != nil {
		c.Ask = func(ctx context.Context, cc Command, v Verdict) (bool, error) {
			calls++
			return ask(ctx, cc, v)
		}
	}
	_, err := Run(ctx, c)
	_, statErr := os.Stat(marker)
	lines := readAudit(t, dir)
	if len(lines) != 1 {
		t.Fatalf("%d audit lines, want 1", len(lines))
	}
	return err, statErr == nil, lines[0], calls
}

func TestAnApprovedAskRuns(t *testing.T) {
	var got Verdict
	var args []string
	err, ran, line, calls := runAsked(t, askPolicy, context.Background(), func(_ context.Context, c Command, v Verdict) (bool, error) {
		got, args = v, c.Args
		return true, nil
	})
	if err != nil || !ran || calls != 1 {
		t.Fatalf("Run = %v, ran = %v, asked %d times; want it run after one ask", err, ran, calls)
	}
	// The asker is shown the verdict and the command it is about.
	if got != (Verdict{Ask, "helper"}) || len(args) != 2 || args[0] != os.Args[0] {
		t.Errorf("the asker got %+v for %q", got, args)
	}
	if line["outcome"] != "exited" || line["decision"] != "ask" || line["rule"] != "helper" || line["approved"] != true {
		t.Errorf("audit line %v, want exited, ask by helper, approved", line)
	}
}

func TestADeclinedAskDoesNotRun(t *testing.T) {
	err, ran, line, calls := runAsked(t, askPolicy, context.Background(), func(context.Context, Command, Verdict) (bool, error) {
		return false, nil
	})
	var refused *RefusedError
	if ran || calls != 1 || !errors.As(err, &refused) || !refused.Declined || refused.Verdict != (Verdict{Ask, "helper"}) {
		t.Fatalf("Run = %v, ran = %v, asked %d times; want a declined refusal", err, ran, calls)
	}
	if line["outcome"] != "refused" || line["decision"] != "ask" || line["approved"] != false {
		t.Errorf("audit line %v, want refused, ask, not approved", line)
	}
}

func TestAnAskThatFailsDoesNotRun(t *testing.T) {
	broken := errors.New("no terminal")
	err, ran, line, _ := runAsked(t, askPolicy, context.Background(), func(context.Context, Command, Verdict) (bool, error) {
		return true, broken
	})
	if ran || !errors.Is(err, broken) || errors.Is(err, ErrRefused) {
		t.Fatalf("Run = %v, ran = %v; want the asker's error and nothing run", err, ran)
	}
	if line["outcome"] != "not run" || line["decision"] != "ask" || line["approved"] != nil {
		t.Errorf("audit line %v, want not run, ask, no answer", line)
	}
}

// An approval does not outlive the caller: a context that ended while the person was asked
// runs nothing.
func TestAnAskApprovedAfterTheContextEndedDoesNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err, ran, _, _ := runAsked(t, askPolicy, ctx, func(context.Context, Command, Verdict) (bool, error) {
		cancel()
		return true, nil
	})
	if ran || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, ran = %v; want context.Canceled and nothing run", err, ran)
	}
}

// Only an ask is put to a person: a refusal is not, and neither is a command the policy allows.
func TestOnlyAnAskIsPutToAPerson(t *testing.T) {
	yes := func(context.Context, Command, Verdict) (bool, error) { return true, nil }
	refuse := `{"version": 1, "rules": [{"name": "helper", "match": "TestHelperProcess", "decision": "refuse"}]}`
	err, ran, _, calls := runAsked(t, refuse, context.Background(), yes)
	if ran || calls != 0 || !errors.Is(err, ErrRefused) {
		t.Errorf("refuse: Run = %v, ran = %v, asked %d times; want refused unasked", err, ran, calls)
	}
	err, ran, line, calls := runAsked(t, `{"version": 1}`, context.Background(), yes)
	if !ran || calls != 0 || err != nil {
		t.Errorf("allow: Run = %v, ran = %v, asked %d times; want run unasked", err, ran, calls)
	}
	if _, ok := line["approved"]; ok {
		t.Errorf("allow: audit line %v has an answer, want none", line)
	}
}

func TestWithNoAskerAnAskIsRefused(t *testing.T) {
	err, ran, line, _ := runAsked(t, askPolicy, context.Background(), nil)
	var refused *RefusedError
	if ran || !errors.As(err, &refused) || refused.Declined {
		t.Fatalf("Run = %v, ran = %v; want refused, not declined", err, ran)
	}
	if line["outcome"] != "refused" || line["approved"] != nil {
		t.Errorf("audit line %v, want refused with no answer", line)
	}
}

// What the asker changes in its Command reaches neither what runs, nor the caller's slices,
// nor the audit.
func TestTheAskerCannotChangeTheCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
	c.Policy = mustPolicy(t, askPolicy)
	c.Audit = &Audit{Dir: dir}
	args, env := slices.Clone(c.Args), slices.Clone(c.Env)
	c.Ask = func(_ context.Context, shown Command, _ Verdict) (bool, error) {
		shown.Args[1] = "-test.run=^TestNothing$"
		shown.Env[len(shown.Env)-1] = "ADANEXT_TEST_MARKER=" + elsewhere
		return true, nil
	}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the command that ran was not the one judged: %v", err)
	}
	if _, err := os.Stat(elsewhere); err == nil {
		t.Error("the asker's change to Env reached the command")
	}
	if !slices.Equal(c.Args, args) || !slices.Equal(c.Env, env) {
		t.Error("the asker's changes reached the caller's slices")
	}
	lines := readAudit(t, dir)
	if len(lines) != 1 {
		t.Fatalf("%d audit lines, want 1", len(lines))
	}
	recorded, _ := lines[0]["args"].([]any)
	if len(recorded) != 2 || recorded[1] != args[1] {
		t.Errorf("the audit recorded args %v, want the command as judged, %q", recorded, args)
	}
}

// The time a person takes to answer is not the command's: a short command with a timeout
// shorter than the wait still runs to its end.
func TestTheWaitForAnAnswerIsNotTheCommandsTime(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
	c.Policy = mustPolicy(t, askPolicy)
	c.Timeout = 5 * time.Second
	c.Ask = func(context.Context, Command, Verdict) (bool, error) {
		time.Sleep(6 * time.Second)
		return true, nil
	}
	res, err := Run(context.Background(), c)
	if err != nil || res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v; want it run to its end", res, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the command did not run: %v", err)
	}
}

// The asker is given the caller's context, so a caller that gives up stops the asking.
func TestTheAskerGetsTheCallersContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sawDone := false
	err, ran, _, _ := runAsked(t, askPolicy, ctx, func(ctx context.Context, _ Command, _ Verdict) (bool, error) {
		select {
		case <-ctx.Done():
			sawDone = true
			return false, ctx.Err()
		case <-time.After(5 * time.Second):
			return true, nil
		}
	})
	if !sawDone || ran || !errors.Is(err, context.Canceled) {
		t.Fatalf("saw done = %v, ran = %v, err = %v; want the asker to see the caller's cancellation", sawDone, ran, err)
	}
}
