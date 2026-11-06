//go:build !windows

package broker

import (
	"os/exec"
)

// checkProgram has nothing to refuse outside Windows: argv reaches the program as given.
func checkProgram(string) error { return nil }

func shellCmd(shell Shell, script string) (*exec.Cmd, error) {
	switch shell {
	case ShellSh:
		return exec.Command("/bin/sh", "-c", script), nil
	case ShellBash:
		path, err := exec.LookPath("bash")
		if err != nil {
			return nil, err
		}
		return exec.Command(path, "-c", script), nil
	case ShellPwsh:
		path, err := exec.LookPath("pwsh")
		if err != nil {
			return nil, err
		}
		return exec.Command(path, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script), nil
	case ShellPowerShell, ShellCmd:
		return nil, invalid("the shell %s is only available on Windows", shell)
	}
	return nil, invalid("unknown shell %q", shell)
}
