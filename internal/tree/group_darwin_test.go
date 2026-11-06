//go:build darwin

package tree

import (
	"encoding/binary"
	"syscall"
	"testing"
	"time"
)

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

func TestALiveChildKeepsTheGroupOfAZombieLeaderAlive(t *testing.T) {
	// The leader starts a child, which stays in the group, and exits: the child's pid is not
	// the group's id, so this reads e_pgid, not p_pid.
	cmd := helperCmd("fork")
	tr := startTree(t, cmd)
	t.Cleanup(func() { _ = tr.Kill() }) // runs before the leader is reaped
	if err := tr.WaitExit(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !groupHasLiveMembers(cmd.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("the leader's live child is not seen in its group")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := tr.Kill(); err != nil {
		t.Fatal(err)
	}
	for groupHasLiveMembers(cmd.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("the group still has a live member after Kill")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// table builds a process table of the given members.
func table(members ...[2]int) string {
	b := make([]byte, 0, len(members)*kinfoProcSize)
	for _, m := range members {
		k := make([]byte, kinfoProcSize)
		binary.LittleEndian.PutUint32(k[kinfoPGID:], uint32(m[0]))
		k[kinfoStat] = byte(m[1])
		b = append(b, k...)
	}
	return string(b)
}

func TestTheGroupCountsAsLiveUnlessTwoReadsFindOnlyZombies(t *testing.T) {
	const pgid, running = 4242, 2 // SRUN
	fixed := func(s string) func() (string, error) { return func() (string, error) { return s, nil } }
	if groupHasLiveMembersIn(pgid, fixed(table([2]int{pgid, stateZombie}, [2]int{1, running})), 0) {
		t.Fatal("zombies only, read twice: want no live member")
	}
	if !groupHasLiveMembersIn(pgid, fixed(table([2]int{pgid, stateZombie}, [2]int{pgid, running})), 0) {
		t.Fatal("a running member: want live")
	}
	reads := 0
	second := func() (string, error) {
		reads++
		if reads == 1 {
			return table([2]int{pgid, stateZombie}), nil
		}
		return table([2]int{pgid, stateZombie}, [2]int{pgid, running}), nil
	}
	if !groupHasLiveMembersIn(pgid, second, 0) {
		t.Fatal("a member seen only by the second read: want live")
	}
	if !groupHasLiveMembersIn(pgid, func() (string, error) { return "", syscall.EIO }, 0) {
		t.Fatal("a table that cannot be read: want live, so the refusal is reported")
	}
	if !groupHasLiveMembersIn(pgid, func() (string, error) { return "", syscall.ENOMEM }, 0) {
		t.Fatal("a table that keeps growing: want live")
	}
}
