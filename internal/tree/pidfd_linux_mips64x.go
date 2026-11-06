//go:build linux && (mips64 || mips64le)

package tree

// pidfd_open and pidfd_send_signal on MIPS n64, offset by 5000.
const (
	sysPidfdOpen       = 5434
	sysPidfdSendSignal = 5424
)
