//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	advapi32       = syscall.NewLazyDLL("advapi32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
	procLocalFree  = kernel32.NewProc("LocalFree")

	procGetNamedSecurityInfoW = advapi32.NewProc("GetNamedSecurityInfoW")
	procSetNamedSecurityInfoW = advapi32.NewProc("SetNamedSecurityInfoW")
	procConvertSDDL           = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procGetSDDacl             = advapi32.NewProc("GetSecurityDescriptorDacl")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errLockViolation        = syscall.Errno(33)
	// wsaeconnrefused is the error a dial to a socket nothing serves gets.
	wsaeconnrefused = syscall.Errno(10061)

	seFileObject             = 1
	ownerSecurityInformation = 0x1
	daclSecurityInformation  = 0x4
	protectedDacl            = 0x80000000
	sddlRevision1            = 1
)

// tryLock takes a lock on path without waiting, exclusive or shared; busy says another holds
// one that excludes it. The lock goes with the file, or the process, however it ends.
func tryLock(path string, exclusive bool) (unlock func(), busy bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	flags := uintptr(lockfileFailImmediately)
	if exclusive {
		flags |= lockfileExclusiveLock
	}
	var ol syscall.Overlapped
	r, _, e := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		f.Close()
		if errors.Is(e, errLockViolation) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("lock %s: %w", path, e)
	}
	return func() { f.Close() }, false, nil
}

// userSID is the string form of the SID the process runs as.
func userSID() (string, error) {
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer t.Close()
	u, err := t.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String()
}

// ownerSID is the string form of the SID that owns path.
func ownerSID(path string) (string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var owner *syscall.SID
	var sd uintptr
	r, _, _ := procGetNamedSecurityInfoW.Call(uintptr(unsafe.Pointer(p)), seFileObject, ownerSecurityInformation,
		uintptr(unsafe.Pointer(&owner)), 0, 0, 0, uintptr(unsafe.Pointer(&sd)))
	if r != 0 {
		return "", fmt.Errorf("read the owner of %s: %w", path, syscall.Errno(r))
	}
	defer procLocalFree.Call(sd)
	return owner.String()
}

// ownedByUser refuses a path another account owns.
func ownedByUser(path string) error {
	me, err := userSID()
	if err != nil {
		return err
	}
	owner, err := ownerSID(path)
	if err != nil {
		return err
	}
	if owner != me {
		return fmt.Errorf("%s belongs to another account (%s)", path, owner)
	}
	return nil
}

// ownerOnlyDir refuses a directory another account owns, and gives the user's own a protected
// access list that lets in only the user, SYSTEM and Administrators, as 0700 does on Unix: the
// socket inside takes it, since Windows sets none on the socket itself and tells the daemon
// nothing about who connects.
func ownerOnlyDir(dir string) error {
	if err := ownedByUser(dir); err != nil {
		return err
	}
	me, err := userSID()
	if err != nil {
		return err
	}
	sddl, err := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;" + me + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	var sd uintptr
	if r, _, e := procConvertSDDL.Call(uintptr(unsafe.Pointer(sddl)), sddlRevision1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return fmt.Errorf("build an owner-only access list: %w", e)
	}
	defer procLocalFree.Call(sd)
	var present, defaulted int32
	var dacl uintptr
	if r, _, e := procGetSDDacl.Call(sd, uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&defaulted))); r == 0 {
		return fmt.Errorf("read the owner-only access list: %w", e)
	}
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	if r, _, _ := procSetNamedSecurityInfoW.Call(uintptr(unsafe.Pointer(p)), seFileObject, daclSecurityInformation|protectedDacl, 0, 0, dacl, 0); r != 0 {
		return fmt.Errorf("make %s owner-only: %w", dir, syscall.Errno(r))
	}
	return nil
}

// ownerOnlySocket has nothing to do on Windows: the socket takes its directory's access list.
func ownerOnlySocket(string) error { return nil }

// refused reports whether a dial failed because nothing serves the socket.
func refused(err error) bool { return errors.Is(err, wsaeconnrefused) }
