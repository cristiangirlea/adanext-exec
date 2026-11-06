//go:build linux && (mips || mipsle)

package tree

// pidfd_open and pidfd_send_signal on MIPS o32, offset by 4000.
const (
	sysPidfdOpen       = 4434
	sysPidfdSendSignal = 4424
)
