package tree

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

const helperEnv = "ADANEXT_TREE_HELPER"

// TestHelperTree is the program the tests start, chosen by ADANEXT_TREE_HELPER.
func TestHelperTree(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		return
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "exit":
		os.Exit(0)
	case "fork":
		// Leaves a child in the group and exits.
		if err := helperCmd("sleep").Start(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	default:
		helperPlatform(os.Getenv(helperEnv))
	}
}

func helperCmd(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperTree$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	return cmd
}

// startTree starts cmd in a tree and has it killed and reaped at cleanup, whatever the test did.
func startTree(t *testing.T, cmd *exec.Cmd) *Tree {
	t.Helper()
	tr, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		tr.Close()
	})
	return tr
}

func TestKillStopsTheCommandAndKillingTwiceIsFine(t *testing.T) {
	cmd := helperCmd("sleep")
	tr := startTree(t, cmd)
	if err := tr.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := tr.WaitExit(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Kill(); err != nil {
		t.Fatalf("killing a tree with nothing left: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the command exited cleanly, want killed")
	}
}

func TestStartReturnsAStartError(t *testing.T) {
	tr, err := Start(exec.Command(t.TempDir() + "/missing-program"))
	if err == nil || tr != nil {
		t.Fatalf("tree %v, err %v; want no tree and an error", tr, err)
	}
}
