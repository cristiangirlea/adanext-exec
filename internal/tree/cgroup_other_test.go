//go:build !linux && !windows

package tree

// helperCgroup has no programs here: cgroups are Linux's.
func helperCgroup(string) {}
