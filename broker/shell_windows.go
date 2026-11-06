//go:build windows

package broker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var procGetSystemDirectoryW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")

// systemDir is System32, from Windows itself rather than an environment variable.
func systemDir() (string, error) {
	buf := make([]uint16, syscall.MAX_PATH)
	n, _, err := procGetSystemDirectoryW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) >= len(buf) {
		return "", fmt.Errorf("find the system directory: %v", err)
	}
	return syscall.UTF16ToString(buf[:n]), nil
}

// checkProgram refuses what cmd.exe would run with its own parsing of the arguments: a batch
// file, which CreateProcess hands to cmd.exe, and cmd.exe itself, also under another name
// through a link. A copy of cmd.exe under another name is not recognised.
func checkProgram(path string) error {
	names := []string{path}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		names = append(names, real)
	}
	for _, name := range names {
		base := filepath.Base(name)
		// Windows drops trailing dots and spaces, and a stream suffix names the same file.
		if i := strings.IndexByte(base, ':'); i >= 0 {
			base = base[:i]
		}
		base = strings.ToLower(strings.TrimRight(base, ". "))
		switch {
		case strings.HasSuffix(base, ".bat"), strings.HasSuffix(base, ".cmd"):
			return invalid("%s is a batch file, whose arguments cmd.exe parses again and cannot be quoted safely; run it as shell text with ShellCmd", path)
		case base == "cmd.exe" || base == "cmd":
			return invalid("cmd.exe parses its arguments again and they cannot be quoted safely; run shell text with ShellCmd")
		}
	}
	sys, err := systemDir()
	if err != nil {
		return err
	}
	if sameFileAsAny(path, filepath.Join(sys, "cmd.exe"), filepath.Join(filepath.Dir(sys), "SysWOW64", "cmd.exe")) {
		return invalid("%s is cmd.exe under another name; run shell text with ShellCmd", path)
	}
	return nil
}

// sameFileAsAny reports whether path is the same file as one of others (a hard link, a
// symbolic link or a junction to it).
func sameFileAsAny(path string, others ...string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	for _, other := range others {
		if oi, err := os.Stat(other); err == nil && os.SameFile(fi, oi) {
			return true
		}
	}
	return false
}

func shellCmd(shell Shell, script string) (*exec.Cmd, error) {
	switch shell {
	case ShellCmd:
		sys, err := systemDir()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(sys, "cmd.exe")
		cmd := exec.Command(path)
		// /s: cmd.exe strips the outer quotes and runs the rest exactly as written.
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + path + `" /d /s /c "` + script + `"`}
		return cmd, nil
	case ShellPowerShell:
		sys, err := systemDir()
		if err != nil {
			return nil, err
		}
		return exec.Command(filepath.Join(sys, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script), nil
	case ShellPwsh:
		path, err := exec.LookPath("pwsh")
		if err != nil {
			return nil, err
		}
		return exec.Command(path, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script), nil
	case ShellBash:
		path, err := gitBash()
		if err != nil {
			return nil, err
		}
		return exec.Command(path, "-c", script), nil
	case ShellSh:
		return nil, invalid("the shell sh is not available on Windows; use bash (Git Bash)")
	}
	return nil, invalid("unknown shell %q", shell)
}

// gitBash finds bash in PATH, passing over WSL's launchers, then in Git for Windows.
func gitBash() (string, error) {
	sys, err := systemDir()
	if err != nil {
		return "", err
	}
	launchers := []string{filepath.Join(sys, "bash.exe"), filepath.Join(sys, "wsl.exe")}
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
		if dir := os.Getenv(env); dir != "" {
			dirs = append(dirs, filepath.Join(dir, "Git", "bin"))
		}
	}
	return findBash(dirs, func(path string) bool { return isWSLLauncher(path, launchers) })
}

// findBash returns the first bash in dirs that is not a launcher. Relative directories are
// skipped, as exec.LookPath skips them.
func findBash(dirs []string, launcher func(string) bool) (string, error) {
	for _, dir := range dirs {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		path, err := exec.LookPath(filepath.Join(dir, "bash"))
		if err != nil || launcher(path) {
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("bash: %w (Git Bash is not installed or not in PATH)", exec.ErrNotFound)
}

// isWSLLauncher reports whether path starts WSL, whose Linux processes no job holds: System32's
// bash.exe or wsl.exe under any name or link, or a Store app alias in a WindowsApps folder. A
// file that cannot be read counts as one.
func isWSLLauncher(path string, launchers []string) bool {
	names := []string{path}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		names = append(names, real)
	}
	for _, name := range names {
		if strings.EqualFold(filepath.Base(filepath.Dir(name)), "WindowsApps") {
			return true
		}
	}
	if _, err := os.Stat(path); err != nil {
		return true
	}
	return sameFileAsAny(path, launchers...)
}
