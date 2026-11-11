package originals

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
)

// MaxRead bounds one read of an original, so what an agent reads back is never larger than
// this; it reads a larger original in ranges.
const MaxRead = 1 << 20

var (
	// ErrHandle means a handle is not 16 lowercase hex characters.
	ErrHandle = errors.New("not an original's handle: want 16 lowercase hex characters")
	// ErrRange means a range is not a-b within the original and the bound.
	ErrRange = errors.New("not a range of the original")
)

var (
	handlePattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
	rangePattern  = regexp.MustCompile(`^([0-9]{1,18})-([0-9]{1,18})$`)
)

// Read returns bytes a to b of the original handle in dir, zero-based and end exclusive, or the
// whole original when rng is empty, and the original's size. An end past the original reads to
// its end; a start past it, a backward range, or more than MaxRead bytes is ErrRange. Only a
// regular file directly in dir is read.
func Read(dir, handle, rng string) ([]byte, int64, error) {
	if !handlePattern.MatchString(handle) {
		return nil, 0, fmt.Errorf("%w: %q", ErrHandle, handle)
	}
	var a, b int64 = 0, -1
	if rng != "" {
		m := rangePattern.FindStringSubmatch(rng)
		if m == nil {
			return nil, 0, fmt.Errorf("%w: %q is not a-b", ErrRange, rng)
		}
		a, _ = strconv.ParseInt(m[1], 10, 64)
		b, _ = strconv.ParseInt(m[2], 10, 64)
		if a > b {
			return nil, 0, fmt.Errorf("%w: %q ends before it starts", ErrRange, rng)
		}
		if b-a > MaxRead {
			return nil, 0, fmt.Errorf("%w: %q is more than %d bytes; read it in smaller ranges", ErrRange, rng, MaxRead)
		}
	}
	if err := plainDir(dir); err != nil {
		return nil, 0, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()
	name := handle + ".txt"
	info, err := r.Lstat(name)
	if err != nil {
		return nil, 0, fmt.Errorf("original %s: %w", handle, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("original %s is not a regular file", handle)
	}
	f, err := r.Open(name)
	if err != nil {
		return nil, 0, fmt.Errorf("original %s: %w", handle, err)
	}
	defer f.Close()
	// The file opened must be the one looked at: a link swapped in between is not read.
	opened, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !os.SameFile(info, opened) {
		return nil, 0, fmt.Errorf("original %s changed while it was opened", handle)
	}
	size := opened.Size()
	if rng == "" {
		if size > MaxRead {
			return nil, size, fmt.Errorf("%w: original %s is %d bytes, more than %d a read; read it with --range a-b", ErrRange, handle, size, MaxRead)
		}
		b = size
	}
	if a > size {
		return nil, size, fmt.Errorf("%w: %q starts past the original's %d bytes", ErrRange, rng, size)
	}
	b = min(b, size)
	data, err := io.ReadAll(io.NewSectionReader(f, a, b-a))
	if err != nil {
		return nil, size, err
	}
	return data, size, nil
}
