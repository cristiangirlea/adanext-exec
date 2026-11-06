//go:build windows

package broker

import "os"

func signalOf(*os.ProcessState) string { return "" }
