//go:build linux

package tree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// startCmd starts the leader with SIGKILL as its parent-death signal, so it dies with the
// caller. Linux sends that signal when the thread that started it ends, so the start happens on
// a locked thread that is kept until Close.
func startCmd(t *Tree, cmd *exec.Cmd) error {
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	started := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		started <- cmd.Start()
		<-done
		runtime.UnlockOSThread()
	}()
	var once sync.Once
	t.release = func() { once.Do(func() { close(done) }) }
	if err := <-started; err != nil {
		t.release()
		return err
	}
	return nil
}

// listProcs reads the process table from /proc.
func listProcs() ([]proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		p, err := readStat(pid)
		if err != nil {
			continue // gone, or not ours to read
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// readStat parses /proc/<pid>/stat. The command name may hold spaces and parentheses, so the
// fields start after the last ')'.
func readStat(pid int) (proc, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return proc{}, err
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return proc{}, fmt.Errorf("process %d: no command name in stat", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return proc{}, fmt.Errorf("process %d: short stat", pid)
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgid, err2 := strconv.Atoi(f[2])
	start, err3 := strconv.ParseUint(f[19], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil {
		return proc{}, fmt.Errorf("process %d: %w", pid, err)
	}
	return proc{pid: pid, ppid: ppid, pgid: pgid, start: start, zombie: f[0] == "Z"}, nil
}

func readEnv(pid int) ([]byte, error) {
	return os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
}

// killProc pins the process with a pidfd, checks it is still the one scanned (its start time),
// and signals through the pidfd, so a reused pid is never signalled.
func killProc(p proc) error {
	fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(p.pid), 0, 0)
	switch errno {
	case 0:
	case syscall.ESRCH:
		return nil
	case syscall.ENOSYS:
		// Before Linux 5.3: the start time is checked just before the signal instead, which
		// leaves the pid open to reuse between the two.
		if q, err := readStat(p.pid); err != nil || q.start != p.start {
			return nil
		}
		if err := syscall.Kill(p.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill process %d: %w", p.pid, err)
		}
		return nil
	default:
		return fmt.Errorf("open process %d: %w", p.pid, errno)
	}
	defer syscall.Close(int(fd))
	if q, err := readStat(p.pid); err != nil || q.start != p.start {
		return nil // gone, or the pid now names another process
	}
	if _, _, errno := syscall.Syscall6(sysPidfdSendSignal, fd, uintptr(syscall.SIGKILL), 0, 0, 0, 0); errno != 0 && errno != syscall.ESRCH {
		return fmt.Errorf("kill process %d: %w", p.pid, errno)
	}
	return nil
}
