//go:build linux

package tree

// groupHasLiveMembers is only asked after EPERM, which on Linux is a real refusal.
func groupHasLiveMembers(int) bool { return true }
