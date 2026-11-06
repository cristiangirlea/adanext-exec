//go:build windows

package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

const otherPlatformShell = ShellSh

type shellCase struct {
	shell    Shell
	script   string
	optional bool
}

var shellCases = []shellCase{
	{ShellCmd, `echo a "b" c& exit /b 4`, false},
	{ShellPowerShell, `Write-Output 'a "b" c'; exit 4`, false},
	{ShellBash, `echo 'a "b" c'; exit 4`, true},
	{ShellPwsh, `Write-Output 'a "b" c'; exit 4`, true},
}

var (
	testKernel32     = syscall.NewLazyDLL("kernel32.dll")
	procProcessTimes = testKernel32.NewProc("GetProcessTimes")
	procExitCode     = testKernel32.NewProc("GetExitCodeProcess")
)

// processState says whether pid runs and when it was created, so a reused pid is not taken for
// the same process.
func processState(pid int) (born string, alive bool, err error) {
	h, err := syscall.OpenProcess(0x1000, false, uint32(pid)) // PROCESS_QUERY_LIMITED_INFORMATION
	if err == syscall.Errno(87) {                             // ERROR_INVALID_PARAMETER: no such process
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer syscall.CloseHandle(h)
	var created, exited, kernel, user syscall.Filetime
	if ok, _, err := procProcessTimes.Call(uintptr(h), uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		return "", false, err
	}
	var code uint32
	if ok, _, err := procExitCode.Call(uintptr(h), uintptr(unsafe.Pointer(&code))); ok == 0 {
		return "", false, err
	}
	return fmt.Sprintf("%d:%d", created.HighDateTime, created.LowDateTime), code == 259, nil // STILL_ACTIVE
}

func TestRunRefusesABatchFileAsArgv(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	batch := filepath.Join(dir, "x.cmd")
	if err := os.WriteFile(batch, []byte("@echo ran> \""+marker+"\"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "y.BAT"), []byte("@echo ran> \""+marker+"\"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, args := range map[string][]string{
		"by path":            {batch, "a&b"},
		"upper case":         {filepath.Join(dir, "y.BAT")},
		"found through PATH": {"x", "a"},
		"trailing dot":       {batch + ".", "a"},
		"trailing space":     {batch + " ", "a"},
		"cmd.exe":            {"cmd.exe", "/c", "echo"},
		"cmd":                {"CMD", "/c", "echo"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Run(context.Background(), Command{Args: args})
			if err == nil {
				t.Fatal("ran, want a refusal")
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("the batch file ran")
			}
			if !errors.Is(err, ErrInvalid) && name != "trailing dot" && name != "trailing space" {
				t.Fatalf("err %v, want ErrInvalid", err)
			}
		})
	}
	// The same batch file runs as shell text.
	res, err := Run(context.Background(), Command{Shell: ShellCmd, Script: `"` + batch + `" a`})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("as shell text: %+v, %v", res, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the batch file did not run as shell text: %v", err)
	}
}

func helperPlatform(string) {}

func TestRunRefusesCmdExeUnderAnotherName(t *testing.T) {
	sys, err := systemDir()
	if err != nil {
		t.Fatal(err)
	}
	cmdExe := filepath.Join(sys, "cmd.exe")
	dir := t.TempDir()
	links := map[string]func(string) error{
		"symbolic link": func(p string) error { return os.Symlink(cmdExe, p) },
		"hard link":     func(p string) error { return os.Link(cmdExe, p) },
	}
	made := 0
	for name, link := range links {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".exe")
			if err := link(p); err != nil {
				t.Skipf("cannot make a %s here: %v", name, err)
			}
			made++
			marker := filepath.Join(dir, "ran-"+strings.ReplaceAll(name, " ", "-"))
			_, err := Run(context.Background(), Command{Args: []string{p, "/d", "/c", "echo x>" + marker}})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err %v, want ErrInvalid", err)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("cmd.exe ran")
			}
		})
	}
	if made == 0 {
		t.Log("no link could be made: the identity check is unverified on this machine")
	}
}

func TestFindBashPassesOverALauncherAndKeepsLooking(t *testing.T) {
	launcherDir, gitDir := t.TempDir(), t.TempDir()
	for _, d := range []string{launcherDir, gitDir} {
		if err := os.WriteFile(filepath.Join(d, "bash.exe"), []byte("MZ"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := findBash([]string{"", "relative", launcherDir, gitDir}, func(p string) bool {
		return strings.EqualFold(filepath.Dir(p), launcherDir)
	})
	if err != nil || !strings.EqualFold(filepath.Dir(got), gitDir) {
		t.Fatalf("found %q (%v), want the bash in %s", got, err, gitDir)
	}
	if _, err := findBash([]string{launcherDir}, func(string) bool { return true }); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err %v, want not found when only a launcher is there", err)
	}
}

func TestIsWSLLauncherSeesSystem32ThroughAJunction(t *testing.T) {
	sys, err := systemDir()
	if err != nil {
		t.Fatal(err)
	}
	launchers := []string{filepath.Join(sys, "bash.exe"), filepath.Join(sys, "wsl.exe")}
	var target string
	for _, l := range launchers {
		if _, err := os.Stat(l); err == nil {
			target = l
			break
		}
	}
	if target == "" {
		t.Skip("WSL's launchers are not installed")
	}
	junction := filepath.Join(t.TempDir(), "sys")
	if out, err := exec.Command(filepath.Join(sys, "cmd.exe"), "/d", "/c", "mklink", "/J", junction, sys).CombinedOutput(); err != nil {
		t.Skipf("cannot make a junction: %v %s", err, out)
	}
	if !isWSLLauncher(filepath.Join(junction, filepath.Base(target)), launchers) {
		t.Fatal("a launcher reached through a junction was not recognised")
	}
	exe, _ := os.Executable()
	if isWSLLauncher(exe, launchers) {
		t.Fatal("an ordinary program was taken for a launcher")
	}
	if !isWSLLauncher(filepath.Join(t.TempDir(), "missing.exe"), launchers) {
		t.Fatal("a file that cannot be read must count as a launcher")
	}
}

func TestIsWSLLauncherSeesAStoreAlias(t *testing.T) {
	apps := filepath.Join(t.TempDir(), "WindowsApps")
	if err := os.Mkdir(apps, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(apps, "bash.exe")
	if err := os.WriteFile(alias, []byte("MZ"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !isWSLLauncher(alias, nil) {
		t.Fatal("a bash in a WindowsApps folder was not taken for a launcher")
	}
	link := filepath.Join(t.TempDir(), "bash.exe")
	if err := os.Symlink(alias, link); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	if !isWSLLauncher(link, nil) {
		t.Fatal("a link to a WindowsApps alias was not taken for a launcher")
	}
}
