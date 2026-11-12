//go:build !windows

package broker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Output a process outside the tree holds open is abandoned as without Shape: what came is
// still shaped and written, its original kept, and the result carries the error.
func TestShapeWritesWhatCameWhenTheOutputIsHeldOpen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "id.json")
	watch(t, file)
	dir := t.TempDir()
	c := helper("clean-orphaner", "ADANEXT_TEST_PID_FILE="+file, "ADANEXT_TEST_OUT=40000")
	c.Timeout = time.Minute
	var stdout bytes.Buffer
	c.Stdout = &stdout
	c.Shape = &Shape{Dir: dir}
	res, err := Run(context.Background(), c)
	readIdentity(t, file) // out of reach by design: killed at cleanup
	if !errors.Is(err, errOutputHeld) || res.ExitCode != 0 {
		t.Fatalf("%v (%+v), want the held output reported after exit 0", err, res)
	}
	text := digits(40000)
	h := handleOf(text)
	want := text[:20000] + "\n[... 10000 bytes cut ...]\n" + text[30000:] + "\n" + fmt.Sprintf("original: %s (40000 bytes)\n", h)
	if stdout.String() != want || res.Original != h {
		t.Fatalf("output of %d bytes (%+v), want the cut and the original's line", stdout.Len(), res)
	}
	if kept, err := os.ReadFile(filepath.Join(dir, h+".txt")); err != nil || string(kept) != text {
		t.Fatalf("the original holds %d bytes (%v)", len(kept), err)
	}
}
