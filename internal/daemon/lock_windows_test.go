//go:build windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var procSDToString = advapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")

// daclOf reads a path's access list back in SDDL.
func daclOf(t *testing.T, path string) string {
	t.Helper()
	p, _ := syscall.UTF16PtrFromString(path)
	var sd uintptr
	if r, _, _ := procGetNamedSecurityInfoW.Call(uintptr(unsafe.Pointer(p)), seFileObject, daclSecurityInformation, 0, 0, 0, 0, uintptr(unsafe.Pointer(&sd))); r != 0 {
		t.Fatalf("read the access list of %s: %v", path, syscall.Errno(r))
	}
	defer procLocalFree.Call(sd)
	var str *uint16
	if r, _, e := procSDToString.Call(sd, sddlRevision1, daclSecurityInformation, uintptr(unsafe.Pointer(&str)), 0); r == 0 {
		t.Fatalf("write the access list as text: %v", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(str)))
	return syscall.UTF16ToString(unsafe.Slice(str, 4096))
}

// The socket's directory is made owner-only: a protected list for the user, SYSTEM and
// Administrators, nothing inherited, nothing for anyone else.
func TestTheSocketDirectoryIsMadeOwnerOnly(t *testing.T) {
	dir := filepath.Join(shortDir(t), "sockets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	before := daclOf(t, dir)
	_, closeFn, err := Listen(context.Background(), filepath.Join(dir, "d.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	me, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	// The expected list goes through the same conversion, so a SID Windows writes by its alias
	// (LA for the built-in administrator, say) compares equal; AI, the auto-inherit mark
	// SetNamedSecurityInfo adds, is not part of what the list allows.
	want := canonicalDACL(t, "D:P(A;OICI;FA;;;"+me+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	got := strings.Replace(daclOf(t, dir), "D:PAI", "D:P", 1)
	if got != want {
		t.Errorf("access list %s (it was %s), want %s: protected, for %s, SYSTEM and Administrators only", got, before, want, me)
	}
}

// canonicalDACL writes an SDDL access list as Windows writes it back.
func canonicalDACL(t *testing.T, sddl string) string {
	t.Helper()
	p, _ := syscall.UTF16PtrFromString(sddl)
	var sd uintptr
	if r, _, e := procConvertSDDL.Call(uintptr(unsafe.Pointer(p)), sddlRevision1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		t.Fatalf("read %s: %v", sddl, e)
	}
	defer procLocalFree.Call(sd)
	var str *uint16
	if r, _, e := procSDToString.Call(sd, sddlRevision1, daclSecurityInformation, uintptr(unsafe.Pointer(&str)), 0); r == 0 {
		t.Fatalf("write %s back: %v", sddl, e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(str)))
	return syscall.UTF16ToString(unsafe.Slice(str, 4096))
}

func TestTheSocketsOwnerIsTheUser(t *testing.T) {
	socket := filepath.Join(shortDir(t), "d.sock")
	_, closeFn, err := Listen(context.Background(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if err := ownedByUser(socket); err != nil {
		t.Errorf("ownedByUser(the daemon's socket) = %v", err)
	}
}
