//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package tree

// pidfd_open and pidfd_send_signal in the table shared by Linux's other architectures.
const (
	sysPidfdOpen       = 434
	sysPidfdSendSignal = 424
)
