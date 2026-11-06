package cli

import (
	"errors"
	"strconv"
	"strings"
)

// parseSize reads a size in bytes, with an optional K, M or G suffix (powers of 1024); empty
// is zero, none.
func parseSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	switch strings.ToUpper(s[len(s)-1:]) {
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	switch {
	case err != nil:
		return 0, errors.New("not a size: bytes, or a whole number with K, M or G")
	case n <= 0:
		return 0, errors.New("must be more than zero")
	case n > (1<<63-1)/mult:
		return 0, errors.New("too large")
	}
	return n * mult, nil
}
