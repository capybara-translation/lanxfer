package server

import (
	"archive/tar"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ErrInvalidEntry reports a folder upload whose tar stream contains an entry
// lanxfer refuses to create: an unsafe path, a duplicate, or anything other
// than a regular file or a folder.
var ErrInvalidEntry = errors.New("invalid folder entry")

// Limits on entry paths. Creating a deep tree is slow (thousands of levels
// take tens of seconds) and tar allows names of a megabyte, so a hostile
// sender could tie up the receiver; real folders stay far below these. The
// sender checks the same limits so it fails before sending anything.
const (
	MaxEntryDepth   = 64
	MaxEntryPathLen = 4096
)

// SaveDir extracts the tar stream in r, which must be exactly size bytes,
// into a temporary folder and, only if every entry is valid and the stream
// is complete, moves it to a non-colliding final name. On failure the
// temporary folder is removed and nothing is left behind. It returns the
// final name and the number of files written.
func (s *Storage) SaveDir(name string, r io.Reader, size int64) (string, int, error) {
	if err := ValidateFilename(name); err != nil {
		return "", 0, err
	}
	tmpPath, err := os.MkdirTemp(s.Dir, partPattern)
	if err != nil {
		return "", 0, fmt.Errorf("create temp folder: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmpPath)
		}
	}()

	files, err := extractInto(tmpPath, r, size)
	if err != nil {
		return "", 0, err
	}
	finalName, err := s.commit(tmpPath, name, true)
	if err != nil {
		return "", 0, err
	}
	committed = true
	return finalName, files, nil
}

// extractor unpacks one tar stream into root and remembers every path it
// created, so a later entry can never silently reuse one of them.
type extractor struct {
	root    *os.Root
	created map[string]bool // slash path -> is a folder
	written int64           // file bytes accepted so far
	limit   int64           // declared length of the whole stream
	folders int             // folders created so far
	maxDirs int             // most folders the stream may create
}

// folderBudget is the most folders a stream of size bytes may create. Every
// entry takes at least one 512-byte block and lanxfer's sender sends one
// entry per folder, so size/512 covers an honest stream. The extra
// MaxEntryDepth allows for the parents of the last entry. Without a bound,
// one block naming a deep chain of folders would create dozens of them.
func folderBudget(size int64) int {
	return int(min(size/512, 1<<30)) + MaxEntryDepth
}

// extractInto unpacks the tar stream into dir. Every file operation goes
// through an os.Root, so even an entry that slipped past the checks cannot
// reach outside dir.
func extractInto(dir string, r io.Reader, size int64) (int, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	// Closed before returning: Windows cannot rename a folder with an open handle.
	defer root.Close()

	ex := &extractor{root: root, created: map[string]bool{}, limit: size, maxDirs: folderBudget(size)}
	body := &countingReader{r: io.LimitReader(r, size)}
	tr := tar.NewReader(body)
	type dirTime struct {
		path  string
		mtime time.Time
	}
	var dirs []dirTime
	files := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if errors.Is(err, tar.ErrHeader) || errors.Is(err, tar.ErrFieldTooLong) {
			return 0, fmt.Errorf("%w: read folder stream: %w", ErrInvalidEntry, err)
		}
		if err != nil {
			return 0, fmt.Errorf("read folder stream: %w", err)
		}
		// A sparse entry reports a huge size while carrying almost no data,
		// which would fill the disk far beyond the declared stream length.
		for key := range hdr.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") {
				return 0, fmt.Errorf("%w: %q is a sparse file", ErrInvalidEntry, hdr.Name)
			}
		}
		rel, err := entryPath(hdr.Name)
		if err != nil {
			return 0, err
		}
		if rel == "" {
			continue // the folder itself
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := ex.mkdirAll(rel, hdr.Name); err != nil {
				return 0, err
			}
			dirs = append(dirs, dirTime{filepath.FromSlash(rel), hdr.ModTime})
		case tar.TypeReg:
			if err := ex.writeEntry(rel, tr, hdr); err != nil {
				return 0, err
			}
			files++
		default:
			return 0, fmt.Errorf("%w: %q is not a regular file or folder", ErrInvalidEntry, hdr.Name)
		}
	}
	// tar commands pad archives to a record size; read the rest so a body
	// that ends early is noticed.
	if _, err := io.Copy(io.Discard, body); err != nil {
		return 0, fmt.Errorf("read folder stream: %w", err)
	}
	if body.n != size {
		return 0, fmt.Errorf("incomplete transfer: got %d of %d bytes", body.n, size)
	}

	// Creating entries inside a folder updates its modification time, so
	// folders get theirs last, deepest first.
	slices.SortFunc(dirs, func(a, b dirTime) int {
		return cmp.Compare(strings.Count(b.path, string(filepath.Separator)), strings.Count(a.path, string(filepath.Separator)))
	})
	for _, d := range dirs {
		if err := root.Chtimes(d.path, d.mtime, d.mtime); err != nil {
			return 0, err
		}
	}
	return files, nil
}

// entryPath turns a tar entry name into a safe slash-separated path relative
// to the folder being received, or "" for the folder itself. Names from tar
// commands ("./a", "dir/") are accepted.
func entryPath(name string) (string, error) {
	p := strings.TrimPrefix(strings.TrimSuffix(name, "/"), "./")
	if p == "" || p == "." {
		return "", nil
	}
	if len(p) > MaxEntryPathLen {
		return "", fmt.Errorf("%w: a path is longer than %d bytes", ErrInvalidEntry, MaxEntryPathLen)
	}
	parts := strings.Split(p, "/")
	if len(parts) > MaxEntryDepth {
		return "", fmt.Errorf("%w: %q is nested deeper than %d folders", ErrInvalidEntry, name, MaxEntryDepth)
	}
	for _, part := range parts {
		if err := ValidateFilename(part); err != nil {
			return "", fmt.Errorf("%w: %q: %w", ErrInvalidEntry, name, err)
		}
	}
	return p, nil
}

// mkdirAll creates every folder along the slash path rel, one component at a
// time. Unlike os.Root.MkdirAll it never accepts a folder it did not create:
// on a case-insensitive or normalizing file system, "Docs" and "docs" would
// otherwise merge into one folder.
func (ex *extractor) mkdirAll(rel, name string) error {
	for i := 0; i <= len(rel); i++ {
		if i < len(rel) && rel[i] != '/' {
			continue
		}
		prefix := rel[:i]
		if isDir, ok := ex.created[prefix]; ok {
			if !isDir {
				return fmt.Errorf("%w: %q is both a file and a folder", ErrInvalidEntry, name)
			}
			continue
		}
		path := filepath.FromSlash(prefix)
		if _, err := ex.root.Lstat(path); err == nil {
			return fmt.Errorf("%w: %q differs from another entry only in letter case or Unicode normalization", ErrInvalidEntry, name)
		}
		if ex.folders >= ex.maxDirs {
			return fmt.Errorf("%w: %q would create more folders than the upload can describe", ErrInvalidEntry, name)
		}
		if err := ex.root.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("create folder %q: %w", prefix, err)
		}
		ex.folders++
		ex.created[prefix] = true
	}
	return nil
}

// writeEntry creates one file from the current tar entry. It never replaces
// anything: a second entry with the same name, or one that differs only in
// letter case on a case-insensitive file system, is an error.
func (ex *extractor) writeEntry(rel string, r io.Reader, hdr *tar.Header) error {
	if hdr.Size > ex.limit-ex.written {
		return fmt.Errorf("%w: %q is larger than the stream that carries it", ErrInvalidEntry, hdr.Name)
	}
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		if err := ex.mkdirAll(rel[:i], hdr.Name); err != nil {
			return err
		}
	}
	if isDir, ok := ex.created[rel]; ok {
		if isDir {
			return fmt.Errorf("%w: %q is both a file and a folder", ErrInvalidEntry, hdr.Name)
		}
		return fmt.Errorf("%w: %q appears twice", ErrInvalidEntry, hdr.Name)
	}
	path := filepath.FromSlash(rel)
	perm := os.FileMode(0o600)
	if hdr.Mode&0o111 != 0 {
		perm = 0o700
	}
	f, err := ex.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %q appears twice (or differs from another name only in letter case)", ErrInvalidEntry, hdr.Name)
	}
	if err != nil {
		return fmt.Errorf("create %q: %w", hdr.Name, err)
	}
	ex.created[rel] = false
	n, err := io.Copy(f, r)
	ex.written += n
	if err != nil {
		f.Close()
		return fmt.Errorf("write %q: %w", hdr.Name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %q: %w", hdr.Name, err)
	}
	return ex.root.Chtimes(path, hdr.ModTime, hdr.ModTime)
}
