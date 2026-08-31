// Package server implements the receiving side of lanxfer: the HTTP server,
// file storage, and connection guard.
package server

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidFilename is a sentinel error indicating that a received filename
// is unsafe. The HTTP handler detects it with errors.Is and responds with 400.
var ErrInvalidFilename = errors.New("invalid filename")

// windowsReserved lists device names that cannot be used as filenames on
// Windows. Official documentation says names with extensions (e.g. CON.txt)
// should also be avoided, but on Windows 11 they are no longer treated as
// devices and can be created normally (verified on real hardware; an
// undocumented behavior change: https://github.com/python/cpython/issues/95486).
// Therefore only names that exactly match a reserved name are rejected.
var windowsReserved = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// ValidateFilename checks whether a filename provided by the sender is safe
// to use as a single file name directly under the receive directory.
// It rejects path traversal, absolute paths, control characters, and names
// incompatible with Windows.
func ValidateFilename(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidFilename, name)
	}
	if len(name) > 255 {
		return fmt.Errorf("%w: name too long (%d bytes)", ErrInvalidFilename, len(name))
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%w: %q contains path separator", ErrInvalidFilename, name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			return fmt.Errorf("%w: %q contains control character", ErrInvalidFilename, name)
		}
	}
	// Windows silently strips trailing dots and spaces, which would make the
	// name collide with a different file, so reject them.
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("%w: %q has trailing dot or space", ErrInvalidFilename, name)
	}
	if _, ok := windowsReserved[strings.ToUpper(name)]; ok {
		return fmt.Errorf("%w: %q is a reserved name on Windows", ErrInvalidFilename, name)
	}
	return nil
}

// Storage manages the directory that received files are saved into.
// Invariant: every (non-.part) file directly under Dir is a completely
// received file. Existing files are never overwritten.
type Storage struct {
	Dir string
}

// NewStorage creates the storage directory if needed and returns a Storage.
// Received files come from other people, so the directory is owner-only (0700).
func NewStorage(dir string) (*Storage, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	return &Storage{Dir: dir}, nil
}

// Save writes the contents of r to a temporary file (.part) and, only if
// exactly size bytes were received, promotes it to a non-colliding final name.
// On failure the temporary file is removed and nothing is left behind.
func (s *Storage) Save(name string, r io.Reader, size int64) (string, error) {
	if err := ValidateFilename(name); err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(s.Dir, ".lanxfer-*.part")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		tmp.Close() // double Close on the success path is harmless
		if !committed {
			os.Remove(tmpName)
		}
	}()

	n, err := io.Copy(tmp, r)
	if err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	if n != size {
		return "", fmt.Errorf("incomplete transfer: got %d of %d bytes", n, size)
	}
	// On Windows an open file cannot be renamed, so Close it first.
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}

	finalName, err := s.commit(tmpName, name)
	if err != nil {
		return "", err
	}
	committed = true
	return finalName, nil
}

// commit moves a .part file to a non-colliding final name.
// It probes for a free name with O_EXCL before renaming, so existing files
// are never overwritten. (There is a tiny race window between Remove and
// Rename, but the MVP accepts it since this process is assumed to be the
// only writer of these names.)
func (s *Storage) commit(partPath, name string) (string, error) {
	for i := 0; i <= 9999; i++ {
		candidate := numberedName(name, i)
		final := filepath.Join(s.Dir, candidate)

		// O_CREATE|O_EXCL is an atomic "create only if it does not exist,
		// fail otherwise". A two-step "check existence, then create" would
		// leave a gap where someone else could create the file in between
		// (a TOCTOU race); O_EXCL does it in one shot at the OS level.
		f, err := os.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue // name in use, try the next candidate
		}
		if err != nil {
			return "", fmt.Errorf("reserve final name: %w", err)
		}
		f.Close()

		// On Windows renaming onto an existing file fails, so remove the
		// placeholder used to reserve the name before renaming.
		os.Remove(final)
		if err := os.Rename(partPath, final); err != nil {
			return "", fmt.Errorf("finalize file: %w", err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("too many filename collisions for %q", name)
}

// numberedName returns the name as-is for i=0, and "base (i).ext" for i>0.
func numberedName(name string, i int) string {
	if i == 0 {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s (%d)%s", stem, i, ext)
}
