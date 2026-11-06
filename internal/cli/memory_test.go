package cli

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/cristiangirlea/adanext-exec/broker"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "1": 1, "4096": 4096, "64K": 64 << 10, "512m": 512 << 20, "2G": 2 << 30} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "-1", "1T", "abc", "1.5G", "K", "9999999999999G"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) took it", in)
		}
	}
}

// A command over its ceiling exits 122, on every platform the ceiling is enforced on.
func TestTheMemoryCeiling(t *testing.T) {
	setup(t)
	code, _, stderr := main(t, context.Background(), "", "alloc", "run", "--memory", "128M", "--timeout", "1m", "--")
	if code != ExitMemory || strings.Contains(stderr, "not enforced") {
		t.Errorf("code %d, stderr %q; want 122", code, stderr)
	}
}

func TestABadMemorySizeIsAUsageError(t *testing.T) {
	setup(t)
	if code, _, _ := main(t, context.Background(), "", "", "run", "--memory", "lots", "--", "x"); code != ExitUsage {
		t.Errorf("code %d, want 121", code)
	}
}

// A failure after the run outranks the ceiling: 120, not 122, so the broker's failure is not
// hidden.
func TestABrokerFailureOutranksTheMemoryCeiling(t *testing.T) {
	if runtime.GOOS != "windows" {
		// The watch elsewhere kills the helper outright, so it writes no crash report to fail on.
		t.Skip("needs the helper's crash report as its allocation is refused, a Windows event")
	}
	e := setup(t)
	auditDirOf = func(*testing.T) string { return e.audit }
	t.Setenv(helperEnv, "alloc")
	// The helper's crash report, as its allocation is refused, goes to a standard error that
	// cannot take it: the run ends over its ceiling and with its output undelivered.
	var out strings.Builder
	code := Main(context.Background(), []string{"run", "--memory", "128M", "--timeout", "1m", "--", os.Args[0], "-test.run=^$"}, strings.NewReader(""), &out, failingWriter{})
	if code != ExitBrokerFailed {
		t.Errorf("code %d; want 120", code)
	}
	// The same run was over its ceiling: 120 won over 122, it did not replace it.
	lines := auditLines(t, auditDirOf(t))
	if len(lines) != 1 || lines[0]["outcome"] != "over its memory ceiling" {
		t.Errorf("audit %v, want the run over its memory ceiling", lines)
	}
}

var auditDirOf func(*testing.T) string

// The order of the codes for a command that ran, the ceiling against everything else.
func TestTheExitCodeOrder(t *testing.T) {
	broken := errors.New("output lost")
	for _, tc := range []struct {
		res  broker.Result
		err  error
		want int
	}{
		{broker.Result{MemoryExceeded: true}, broker.ErrSlotLost, ExitNoDaemon},
		{broker.Result{MemoryExceeded: true}, broken, ExitBrokerFailed},
		{broker.Result{MemoryExceeded: true, TimedOut: true}, nil, ExitMemory},
		{broker.Result{MemoryExceeded: true, ExitCode: -1}, nil, ExitMemory},
		{broker.Result{TimedOut: true}, nil, ExitTimeout},
		{broker.Result{Canceled: true}, nil, ExitInterrupted},
		{broker.Result{ExitCode: 3}, nil, 3},
	} {
		if got := ranCode(tc.res, tc.err); got != tc.want {
			t.Errorf("ranCode(%+v, %v) = %d, want %d", tc.res, tc.err, got, tc.want)
		}
	}
}
