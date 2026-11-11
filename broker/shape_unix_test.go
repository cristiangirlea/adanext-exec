//go:build !windows

package broker

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Output a process outside the tree holds open is abandoned as without Shape: what came is
// still shaped and written, and the result carries the error.
func TestShapeWritesWhatCameWhenTheOutputIsHeldOpen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	watch(t, file)
	c := helper("clean-orphaner", "ADANEXT_TEST_PID_FILE="+file)
	c.Timeout = time.Minute
	var stdout bytes.Buffer
	c.Stdout = &stdout
	c.Shape = &Shape{Dir: t.TempDir()}
	res, err := Run(context.Background(), c)
	readIdentity(t, file) // out of reach by design: killed at cleanup
	if !errors.Is(err, errOutputHeld) || res.ExitCode != 0 {
		t.Fatalf("%v (%+v), want the held output reported after exit 0", err, res)
	}
}
