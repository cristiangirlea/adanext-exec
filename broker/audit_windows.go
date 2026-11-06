//go:build windows

package broker

import "os"

// openNoFollow has no Windows form; the name is checked with Lstat before the open.
const openNoFollow = 0

// ownerOnly has nothing to do on Windows, where the directory's access list decides.
func ownerOnly(string, os.FileMode) error { return nil }
