//go:build darwin

package tree

import "testing"

func TestAGroupOfZombiesHasNoLiveMembers(t *testing.T) {
	cmd := helperCmd("sleep")
	tr := startTree(t, cmd)
	if !groupHasLiveMembers(cmd.Process.Pid) {
		t.Fatal("a running leader's group has no live member")
	}
	if err := tr.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := tr.WaitExit(); err != nil {
		t.Fatal(err)
	}
	if groupHasLiveMembers(cmd.Process.Pid) {
		t.Fatal("a group whose only member is a zombie has a live member")
	}
	if err := tr.Kill(); err != nil {
		t.Fatalf("killing a group of zombies: %v", err)
	}
}
