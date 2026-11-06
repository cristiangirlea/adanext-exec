//go:build windows

package cli

import (
	"syscall"
	"unsafe"
)

// tokenElevation is TOKEN_INFORMATION_CLASS's TokenElevation.
const tokenElevation = 20

// elevated reports whether the process token is elevated; one that cannot be read counts as
// elevated, so the binary refuses rather than guess.
func elevated() bool {
	p, err := syscall.GetCurrentProcess()
	if err != nil {
		return true
	}
	var t syscall.Token
	if err := syscall.OpenProcessToken(p, syscall.TOKEN_QUERY, &t); err != nil {
		return true
	}
	defer t.Close()
	var e uint32
	var n uint32
	if err := syscall.GetTokenInformation(t, tokenElevation, (*byte)(unsafe.Pointer(&e)), uint32(unsafe.Sizeof(e)), &n); err != nil {
		return true
	}
	return e != 0
}

// signalNumber has nothing to find on Windows, where the broker reports no signals.
func signalNumber(string) int { return 0 }
