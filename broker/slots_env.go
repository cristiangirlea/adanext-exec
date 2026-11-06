package broker

import (
	"os"
	"runtime"
	"strings"
)

// environ is the caller's environment; tests replace it.
var environ = os.Environ

// hasVar reports whether env sets the variable name, in any case on Windows, where names are
// not case-sensitive.
func hasVar(env []string, name string) bool {
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == name || runtime.GOOS == "windows" && strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}
