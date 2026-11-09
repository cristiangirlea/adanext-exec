package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func hookRewrite(t *testing.T, tool string, input map[string]any) string {
	t.Helper()
	code, d, stderr := hookMain(t, hookRequest(t, tool, input), "claude-code")
	if code != 0 || d == nil || d.Decision != "allow" {
		t.Fatalf("code %d, answer %+v, stderr %q", code, d, stderr)
	}
	var rewrite string
	if err := json.Unmarshal(d.Input["command"], &rewrite); err != nil {
		t.Fatal(err)
	}
	return rewrite
}

func TestTheRewriteCarriesTheCallsTimeoutOnBothTools(t *testing.T) {
	setup(t)
	for _, tool := range []string{"Bash", "PowerShell"} {
		for _, tc := range []struct {
			timeout any
			want    string
		}{{5000, "'--timeout' '5s'"}, {1500, "'--timeout' '1.5s'"}, {0.5, "'--timeout' '1ms'"}} {
			got := hookRewrite(t, tool, map[string]any{"command": "echo hi", "timeout": tc.timeout})
			if !strings.Contains(got, tc.want) {
				t.Errorf("%s timeout %v: the rewrite %q lacks %s", tool, tc.timeout, got, tc.want)
			}
		}
	}
}

func TestTheRewriteHasNoTimeoutWhenTheCallSetsNoneOrRunsInTheBackground(t *testing.T) {
	setup(t)
	for name, in := range map[string]map[string]any{
		"none":       {"command": "echo hi"},
		"null":       {"command": "echo hi", "timeout": nil},
		"background": {"command": "echo hi", "timeout": 5000, "run_in_background": true},
	} {
		if got := hookRewrite(t, "Bash", in); strings.Contains(got, "--timeout") {
			t.Errorf("%s: the rewrite %q has a timeout", name, got)
		}
	}
	if got := hookRewrite(t, "Bash", map[string]any{"command": "echo hi", "timeout": 5000, "run_in_background": false}); !strings.Contains(got, "'--timeout'") {
		t.Errorf("a foreground call lost its timeout: %q", got)
	}
}

func TestTheHookDeniesATimeoutItCannotUse(t *testing.T) {
	setup(t)
	for _, timeout := range []any{0, -1, "5000", true, 1e300, map[string]any{}} {
		code, d, _ := hookMain(t, hookRequest(t, "Bash", map[string]any{"command": "echo hi", "timeout": timeout}), "claude-code")
		if code != 0 || d == nil || d.Decision != "deny" || !strings.Contains(d.Reason, "timeout") || d.Input != nil {
			t.Errorf("timeout %v: code %d, answer %+v; want a deny naming the timeout", timeout, code, d)
		}
	}
}

func TestTheRewriteKillsTheCommandAtTheCallsTimeout(t *testing.T) {
	e := setup(t)
	useTestBinaryForRewrite(t, e)
	rewrite := hookRewrite(t, "Bash", map[string]any{"command": "sleep 60", "timeout": 300})
	start := time.Now()
	code, _, stderr := main(t, context.Background(), "", "", "run", "--shell", "bash", "--", rewrite)
	if code != ExitTimeout || time.Since(start) > 30*time.Second {
		t.Fatalf("code %d after %s, stderr %q; want %d soon after the timeout", code, time.Since(start), stderr, ExitTimeout)
	}
}
