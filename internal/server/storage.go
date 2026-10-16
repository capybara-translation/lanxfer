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
	"runtime"
	"strings"
	"time"
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
// incompatible with Windows. Characters that only Windows forbids are
// checked only when running on Windows: a Windows receiver rejects them, and
// a Windows sender cannot have such names anyway. Other systems do not
// restrict them because the receiver's system is unknown when sending by IP.
func ValidateFilename(name string) error {
	return validateFilename(name, runtime.GOOS == "windows")
}

func validateFilename(name string, windows bool) error {
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
	if windows && strings.ContainsAny(name, `<>:"|?*`) {
		return fmt.Errorf("%w: %q contains a character Windows does not allow in names", ErrInvalidFilename, name)
	}
	// Windows silently strips trailing dots and spaces, which would make the
	// name collide with a different file, so reject them.
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("%w: %q has trailing dot or space", ErrInvalidFilename, name)
	}
	if _, ok := windowsReserved[strings.ToUpper(name)]; ok {
		return fmt.Errorf("%w: %q is a reserved name on Windows", ErrInvalidFilename, name)
	}
	// Startup cleanup deletes stale files with this name, so a received file
	// must never carry it.
	if ok, _ := filepath.Match(partPattern, name); ok {
		return fmt.Errorf("%w: %q is reserved for temporary files", ErrInvalidFilename, name)
	}
	return nil
}

// partPattern names the temporary files Save writes into.
const partPattern = ".lanxfer-*.part"

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

	tmp, err := os.CreateTemp(s.Dir, partPattern)
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

	finalName, err := s.commit(tmpName, name, false)
	if err != nil {
		return "", err
	}
	committed = true
	return finalName, nil
}

// commit moves a finished .part file or folder to a non-colliding final
// name. It reserves a free name atomically before renaming, so existing
// entries are never overwritten. (There is a tiny race window between
// Remove and Rename, but this process is assumed to be the only writer of
// these names.)
func (s *Storage) commit(partPath, name string, isDir bool) (string, error) {
	for i := 0; i <= 9999; i++ {
		candidate := numberedName(name, i, isDir)
		final := filepath.Join(s.Dir, candidate)

		// Both reservations are an atomic "create only if nothing exists
		// there, fail otherwise": O_CREATE|O_EXCL for a file, Mkdir for a
		// folder. A two-step "check existence, then create" would leave a
		// gap where someone else could take the name (a TOCTOU race).
		err := reserve(final, isDir)
		if errors.Is(err, fs.ErrExist) {
			continue // name in use, try the next candidate
		}
		if err != nil {
			return "", fmt.Errorf("reserve final name: %w", err)
		}

		// On Windows renaming onto an existing file or folder fails, so
		// remove the placeholder used to reserve the name before renaming.
		os.Remove(final)
		if err := os.Rename(partPath, final); err != nil {
			return "", fmt.Errorf("finalize: %w", err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("too many name collisions for %q", name)
}

// reserve creates an empty placeholder at path, failing with fs.ErrExist if
// anything is already there.
func reserve(path string, isDir bool) error {
	if isDir {
		return os.Mkdir(path, 0o700)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// numberedName returns the name as-is for i=0. For i>0 a file becomes
// "base (i).ext" and a folder "name (i)": a dot in a folder name is not an
// extension, so "photos.2024" becomes "photos.2024 (1)".
func numberedName(name string, i int, isDir bool) string {
	if i == 0 {
		return name
	}
	if isDir {
		return fmt.Sprintf("%s (%d)", name, i)
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s (%d)%s", stem, i, ext)
}

// RemoveStaleParts deletes temporary files and folders left behind by
// transfers that ended without cleaning up, such as when the receiver was
// killed or the machine lost power. Only entries untouched for at least
// olderThan are removed; for a folder that means nothing inside it changed
// either, because writing a file deep inside does not update the folder
// itself. A transfer still being written by another receiver sharing the
// directory keeps updating its entry and is left alone. Symbolic links and
// other special files are never touched. It returns how many entries were
// removed; one that cannot be removed is reported in the error without
// stopping the sweep.
func (s *Storage) RemoveStaleParts(olderThan time.Duration) (int, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		if ok, _ := filepath.Match(partPattern, e.Name()); !ok {
			continue
		}
		path := filepath.Join(s.Dir, e.Name())
		var newest time.Time
		var remove func(string) error
		switch {
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				continue // removed in the meantime
			}
			newest, remove = info.ModTime(), os.Remove
		case e.IsDir():
			t, err := newestModTime(path)
			if err != nil {
				continue // removed in the meantime, or unreadable: leave it
			}
			newest, remove = t, os.RemoveAll
		default:
			continue
		}
		if time.Since(newest) < olderThan {
			continue
		}
		if err := remove(path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// newestModTime returns the latest modification time of dir and everything
// under it. Only folders and regular files count; symbolic links and other
// special entries are neither followed nor counted, because the receiver never
// creates them and their own timestamps say nothing about a live transfer.
func newestModTime(dir string) (time.Time, error) {
	var newest time.Time
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, err
}
