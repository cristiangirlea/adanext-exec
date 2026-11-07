//go:build windows

package tree

import (
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// A Job Object holds every descendant, also those Git Bash re-parents; taskkill /T misses them.
var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW          = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
	procAssignProcessToJobObject  = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject        = kernel32.NewProc("TerminateJobObject")
	procCreateToolhelp32Snapshot  = kernel32.NewProc("CreateToolhelp32Snapshot")
	procThread32First             = kernel32.NewProc("Thread32First")
	procThread32Next              = kernel32.NewProc("Thread32Next")
	procOpenThread                = kernel32.NewProc("OpenThread")
	procResumeThread              = kernel32.NewProc("ResumeThread")
)

const (
	jobObjectBasicAccountingInformationClass = 1
	jobObjectExtendedLimitInformationClass   = 9
	// Only this flag is set: never JOB_OBJECT_LIMIT_BREAKAWAY_OK (0x800) or
	// JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK (0x1000), so no descendant can leave the job.
	jobObjectLimitKillOnJobClose = 0x2000
	// jobObjectLimitJobMemory caps the committed memory of every process in the job together.
	jobObjectLimitJobMemory = 0x0200
	processTerminate        = 0x0001
	processSetQuota         = 0x0100
	synchronize             = 0x00100000
	createSuspended         = 0x0004
	snapThread              = 0x0004
	threadSuspendResume     = 0x0002
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount, WriteOperationCount, OtherOperationCount uint64
	ReadTransferCount, WriteTransferCount, OtherTransferCount    uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type jobObjectBasicAccountingInformation struct {
	TotalUserTime, TotalKernelTime                                                 int64
	ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime                             int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses uint32
}

// Tree is a started command and the kill-on-close job that holds it and its descendants.
type Tree struct {
	mu        sync.Mutex
	job       syscall.Handle
	process   syscall.Handle
	limit     uint64
	exceeded  atomic.Bool
	stopWatch chan struct{}
	watchDone chan struct{}
	stopOnce  sync.Once
	memoryErr error
	// readPeak reads the job's peak memory; tests replace it.
	readPeak func() (uint64, error)
}

// Start starts cmd suspended, assigns it to a new kill-on-close job and only then lets it run,
// so nothing it starts can be outside the job. Jobs nest (Windows 8 and later), so this works
// when the caller is itself in a job. When the job cannot be made or assigned, the suspended
// process is killed and Start returns ErrNotContained.
func Start(cmd *exec.Cmd) (*Tree, error) { return StartLimited(cmd, 0) }

// StartCeiling is StartLimited: the job holds the ceiling on Windows whatever the mode.
func StartCeiling(cmd *exec.Cmd, memoryLimit uint64, mode Ceiling) (*Tree, error) {
	return StartLimited(cmd, memoryLimit)
}

// StartLimited is Start with a ceiling on the committed memory of the whole tree, in bytes; 0
// is none. The job enforces it: an allocation past it fails, and the watch (watchMemory) then
// kills the tree, so the command is stopped, not left to run on with failing allocations.
func StartLimited(cmd *exec.Cmd, memoryLimit uint64) (*Tree, error) {
	h, _, err := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return nil, fmt.Errorf("%w: create a job object: %v", ErrNotContained, err)
	}
	if !limitFits(memoryLimit, uint64(^uintptr(0))) {
		syscall.CloseHandle(syscall.Handle(h))
		return nil, errLimitTooLarge(memoryLimit)
	}
	t := &Tree{job: syscall.Handle(h), limit: memoryLimit}
	t.readPeak = t.queryPeak
	var info jobObjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if memoryLimit > 0 {
		info.BasicLimitInformation.LimitFlags |= jobObjectLimitJobMemory
		info.JobMemoryLimit = uintptr(memoryLimit)
	}
	if ok, _, err := procSetInformationJobObject.Call(h, jobObjectExtendedLimitInformationClass, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ok == 0 {
		t.Close()
		return nil, fmt.Errorf("%w: set the job's limits: %v", ErrNotContained, err)
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createSuspended
	if err := cmd.Start(); err != nil {
		t.Close()
		return nil, err
	}
	fail := func(err error) (*Tree, error) {
		// A suspended process must not be left behind.
		t.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	p, err := syscall.OpenProcess(processSetQuota|processTerminate|synchronize, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(fmt.Errorf("%w: open the process: %v", ErrNotContained, err))
	}
	t.process = p
	if ok, _, err := procAssignProcessToJobObject.Call(uintptr(t.job), uintptr(p)); ok == 0 {
		return fail(fmt.Errorf("%w: assign the process to its job: %v", ErrNotContained, err))
	}
	if err := resumeThreads(uint32(cmd.Process.Pid), resumeThread); err != nil {
		return fail(err)
	}
	if memoryLimit > 0 {
		t.watchMemory()
	}
	return t, nil
}

type threadEntry32 struct {
	Size, Usage, ThreadID, OwnerProcessID uint32
	BasePriority, DeltaPriority           int32
	Flags                                 uint32
}

// resumeThreads resumes every thread of the process: a snapshot cannot name the initial one,
// another program may have added threads, and resuming a running thread changes nothing.
func resumeThreads(pid uint32, resume func(uint32) error) error {
	snapshot, _, err := procCreateToolhelp32Snapshot.Call(snapThread, 0)
	if snapshot == ^uintptr(0) {
		return fmt.Errorf("snapshot the threads of process %d: %w", pid, err)
	}
	defer syscall.CloseHandle(syscall.Handle(snapshot))
	resumed := 0
	var resumeErr error
	entry := threadEntry32{Size: uint32(unsafe.Sizeof(threadEntry32{}))}
	ok, _, err := procThread32First.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.OwnerProcessID == pid {
			if err := resume(entry.ThreadID); err != nil {
				// An injected thread may vanish or deny access; try the others.
				resumeErr = err
			} else {
				resumed++
			}
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, err = procThread32Next.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}
	if resumed == 0 {
		if resumeErr != nil {
			return fmt.Errorf("resume process %d: %w", pid, resumeErr)
		}
		return fmt.Errorf("find the initial thread of process %d: %w", pid, err)
	}
	return nil
}

func resumeThread(id uint32) error {
	h, _, err := procOpenThread.Call(threadSuspendResume, 0, uintptr(id))
	if h == 0 {
		return fmt.Errorf("open thread %d: %w", id, err)
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	if count, _, err := procResumeThread.Call(h); uint32(count) == ^uint32(0) {
		return fmt.Errorf("resume thread %d: %w", id, err)
	}
	return nil
}

// WaitExit returns once the command's own process has exited; its descendants may still run.
func (t *Tree) WaitExit() error {
	t.mu.Lock()
	p := t.process
	t.mu.Unlock()
	if p == 0 {
		return fmt.Errorf("the tree is closed")
	}
	if ev, err := syscall.WaitForSingleObject(p, syscall.INFINITE); ev != syscall.WAIT_OBJECT_0 {
		return fmt.Errorf("wait for process: %v", err)
	}
	return nil
}

// Kill terminates every process in the job. A job with no process left is not an error.
func (t *Tree) Kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job == 0 {
		return fmt.Errorf("the tree is closed")
	}
	if ok, _, err := procTerminateJobObject.Call(uintptr(t.job), 1); ok == 0 {
		return fmt.Errorf("terminate the job: %w", err)
	}
	return nil
}

// Active counts the processes still in the job.
func (t *Tree) Active() (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job == 0 {
		return 0, fmt.Errorf("the tree is closed")
	}
	var info jobObjectBasicAccountingInformation
	if ok, _, err := procQueryInformationJobObject.Call(uintptr(t.job), jobObjectBasicAccountingInformationClass, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0); ok == 0 {
		return 0, fmt.Errorf("query the job: %w", err)
	}
	return int(info.ActiveProcesses), nil
}

// limitFlags reads the job's limit flags back from Windows.
func (t *Tree) limitFlags() (uint32, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var info jobObjectExtendedLimitInformation
	if ok, _, err := procQueryInformationJobObject.Call(uintptr(t.job), jobObjectExtendedLimitInformationClass, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0); ok == 0 {
		return 0, fmt.Errorf("query the job: %w", err)
	}
	return info.BasicLimitInformation.LimitFlags, nil
}

// Close releases the job; Windows then kills whatever is still in it.
func (t *Tree) Close() {
	t.stopWatching()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = syscall.CloseHandle(t.job)
		t.job = 0
	}
	if t.process != 0 {
		_ = syscall.CloseHandle(t.process)
		t.process = 0
	}
}
