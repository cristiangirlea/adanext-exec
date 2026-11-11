// Package originals keeps a command's whole output in a directory the caller names, each
// original named by a hash of its bytes, within caps that refuse a new original rather than
// evict an old one, so a handle already handed out never dangles.
package originals

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The caps when the caller gives none: a directory's originals, and all of them together.
const (
	DefaultDirMax = 64 << 20
	DefaultAllMax = 1 << 30
)

// ErrFull means an original was not kept because a cap would be passed.
var ErrFull = errors.New("the originals are full")

// lockName is the file whose lock serializes measuring and writing, in the directory and in the
// root.
const lockName = ".adanext-exec-originals.lock"

// lockWait bounds the wait for another writer, which holds the lock only to measure and write.
var lockWait = 10 * time.Second

// Handle names text: the first 16 hex characters of its SHA-256.
func Handle(text []byte) string {
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:8])
}

// Store is an originals directory, the root whose runs' originals count together, and the caps.
type Store struct {
	dir, root      string
	dirMax, allMax int64
}

// Open makes dir (its parent must exist) owner-only, or checks the one there. A cap of zero is
// the default. root, when set, is a directory holding one directory per run, each with its
// originals in a directory named "originals": they count together, with dir, against allMax.
func Open(dir, root string, dirMax, allMax int64) (*Store, error) {
	if dir == "" {
		return nil, errors.New("no originals directory")
	}
	if dirMax < 0 || allMax < 0 {
		return nil, errors.New("a negative originals cap")
	}
	if dirMax == 0 {
		dirMax = DefaultDirMax
	}
	if allMax == 0 {
		allMax = DefaultAllMax
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("make the originals directory: %w", err)
	}
	r, err := openPlain(dir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err := ownerOnly(r, dir); err != nil {
		return nil, fmt.Errorf("the originals directory: %w", err)
	}
	if root != "" {
		if root, err = filepath.Abs(root); err != nil {
			return nil, err
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("the originals root: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("the originals root %s is not a directory", root)
		}
	}
	return &Store{dir: dir, root: root, dirMax: dirMax, allMax: allMax}, nil
}

// Limit is the most one original can hold: the smaller cap.
func (s *Store) Limit() int64 { return min(s.dirMax, s.allMax) }

// openPlain opens dir as a root only when its path names a plain directory and the directory
// opened is the one the path names, so a link put in its place is never followed; what is done
// through the root then stays in that directory.
func openPlain(dir string) (*os.Root, error) {
	if err := plainDir(dir); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := r.Stat(".")
	if err == nil {
		var named os.FileInfo
		if named, err = os.Lstat(dir); err == nil && (named.Mode().Type() != fs.ModeDir || !os.SameFile(opened, named)) {
			err = fmt.Errorf("the originals directory %s changed while it was opened", dir)
		}
	}
	if err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// plainDir refuses anything at path but a directory: a link, a junction or a file.
func plainDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Type() != fs.ModeDir {
		return fmt.Errorf("the originals directory %s is not a plain directory", path)
	}
	return nil
}

// Save keeps text and returns its handle. The same bytes kept before return the same handle
// and count nothing; an original that would pass a cap is refused with ErrFull.
func (s *Store) Save(text []byte) (string, error) {
	r, err := openPlain(s.dir)
	if err != nil {
		return "", err
	}
	defer r.Close()
	unlock, err := s.lock(r)
	if err != nil {
		return "", err
	}
	defer unlock()
	handle := Handle(text)
	name := handle + ".txt"
	if held, err := holds(r, name, text); err != nil || held {
		return handle, err
	}
	inDir, inAll, err := s.measure()
	if err != nil {
		// An unmeasured store would fail open, so it refuses.
		return "", fmt.Errorf("measure the originals: %w", err)
	}
	size := int64(len(text))
	if size > s.dirMax || inDir > s.dirMax-size {
		return "", fmt.Errorf("%w: %d bytes would pass the directory's cap of %d, which holds %d", ErrFull, size, s.dirMax, inDir)
	}
	if size > s.allMax || inAll > s.allMax-size {
		return "", fmt.Errorf("%w: %d bytes would pass the cap over all of %d, which holds %d", ErrFull, size, s.allMax, inAll)
	}
	if err := write(r, name, text); err != nil {
		return "", fmt.Errorf("keep the original: %w", err)
	}
	return handle, nil
}

// holds reports whether name already holds text; anything else there is an error.
func holds(r *os.Root, name string, text []byte) (bool, error) {
	info, err := r.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("original %s is not a regular file", name)
	}
	if info.Size() == int64(len(text)) {
		f, err := r.Open(name)
		if err != nil {
			return false, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, int64(len(text))+1))
		if err != nil {
			return false, err
		}
		if string(data) == string(text) {
			return true, nil
		}
	}
	return false, fmt.Errorf("original %s already holds other bytes", name)
}

// write puts text at name through a temporary file, so a reader never sees half an original.
func write(r *os.Root, name string, text []byte) error {
	temp := ".original-" + rand.Text() + ".tmp"
	f, err := r.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer r.Remove(temp)
	_, err = f.Write(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return r.Rename(temp, name)
}

// lock takes the root's lock, when there is a root, then the directory's (dir, opened), so every
// writer of the directory waits for the others whatever root each names, and every writer under
// the root for those under it. Each lock file is made through a root, which no link leaves.
func (s *Store) lock(dir *os.Root) (func(), error) {
	var held []*os.File
	unlock := func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Close()
		}
	}
	if s.root != "" {
		r, err := os.OpenRoot(s.root)
		if err != nil {
			return nil, err
		}
		f, err := lockIn(r, s.root)
		r.Close()
		if err != nil {
			return nil, err
		}
		held = append(held, f)
	}
	f, err := lockIn(dir, s.dir)
	if err != nil {
		unlock()
		return nil, err
	}
	return func() { f.Close(); unlock() }, nil
}

// lockIn takes the lock file in r, waiting up to lockWait for another writer; closing the file
// lets it go.
func lockIn(r *os.Root, at string) (*os.File, error) {
	f, err := r.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the originals' lock in %s: %w", at, err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		busy, err := tryLock(f)
		if err == nil && !busy {
			return f, nil
		}
		if err == nil && time.Now().After(deadline) {
			err = fmt.Errorf("another writer held it for %v", lockWait)
		}
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("lock the originals in %s: %w", at, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// measure sums the regular files in the directory, and in it and every run's originals under
// the root together, a file linked into several once.
func (s *Store) measure() (inDir, inAll int64, err error) {
	if inDir, err = size(s.dir, nil); err != nil {
		return 0, 0, err
	}
	seen := map[int64][]os.FileInfo{}
	if inAll, err = size(s.dir, seen); err != nil {
		return 0, 0, err
	}
	if s.root == "" {
		return inDir, inAll, nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := size(filepath.Join(s.root, e.Name(), "originals"), seen)
		if err != nil {
			return 0, 0, err
		}
		inAll += n
	}
	return inDir, inAll, nil
}

// size sums the regular files in dir, skipping those already seen; a missing dir or a link to
// one holds nothing, and anything else at its name is an error.
func size(dir string, seen map[int64][]os.FileInfo) (int64, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0, nil
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if seen != nil {
			dup := false
			for _, old := range seen[info.Size()] {
				if os.SameFile(old, info) {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			seen[info.Size()] = append(seen[info.Size()], info)
		}
		total += info.Size()
	}
	return total, nil
}
