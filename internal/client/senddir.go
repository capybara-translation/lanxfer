package client

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/capybara-translation/lanxfer/internal/server"
)

// DirPlan is a folder scanned for sending: what goes into the tar stream
// and how long that stream will be.
type DirPlan struct {
	Root        string        // folder on disk (symbolic links resolved)
	Name        string        // folder name sent to the receiver
	Files       int           // regular files to send
	ContentSize int64         // total size of those files
	TarSize     int64         // exact length of the tar stream
	Skipped     []SkippedItem // excluded system files and folders, links, special files, in walk order
	entries     []dirEntry
}

// SkippedItem is something ScanDir left out of the transfer.
type SkippedItem struct {
	Path   string // slash-separated, relative to the folder
	Reason string // "system file", "system folder", "symbolic link", or "special file"
}

type dirEntry struct {
	rel     string // slash-separated path relative to Root
	isDir   bool
	size    int64
	modTime time.Time
	exec    bool
}

// Files and folders that operating systems create on their own and that are
// of no use on another machine. Names match exactly, except that every file
// starting with "._" (macOS AppleDouble) is excluded.
var (
	junkFiles = map[string]bool{
		".DS_Store":   true, // macOS Finder view settings
		"Icon\r":      true, // macOS custom folder icon
		"Thumbs.db":   true, // Windows thumbnail cache
		"desktop.ini": true, // Windows folder settings
	}
	junkDirs = map[string]bool{
		"__MACOSX":                true, // macOS metadata in zips unpacked elsewhere
		".Spotlight-V100":         true,
		".fseventsd":              true,
		".Trashes":                true,
		".TemporaryItems":         true,
		".DocumentRevisions-V100": true,
	}
)

func isJunk(name string, isDir bool) bool {
	if isDir {
		return junkDirs[name]
	}
	return junkFiles[name] || strings.HasPrefix(name, "._")
}

// ScanDir walks the folder at path and plans its transfer. It skips system
// junk, symbolic links, and special files, and fails before anything is
// sent if a name would be rejected by the receiver.
func ScanDir(path string) (*DirPlan, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// The name comes from the path as given, so a link called "alias" is
	// sent as "alias"; the walk starts from what it points to.
	name := filepath.Base(abs)
	if err := server.ValidateFilename(name); err != nil {
		return nil, fmt.Errorf("cannot send folder: %w", err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", path)
	}

	plan := &DirPlan{Root: root, Name: name}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isJunk(d.Name(), d.IsDir()) {
			if d.IsDir() {
				plan.Skipped = append(plan.Skipped, SkippedItem{rel, "system folder"})
				return filepath.SkipDir
			}
			plan.Skipped = append(plan.Skipped, SkippedItem{rel, "system file"})
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			reason := "special file" // devices, pipes, sockets
			if d.Type()&fs.ModeSymlink != 0 {
				reason = "symbolic link"
			}
			plan.Skipped = append(plan.Skipped, SkippedItem{rel, reason})
			return nil
		}
		if err := server.ValidateFilename(d.Name()); err != nil {
			return fmt.Errorf("cannot send %q: %w", rel, err)
		}
		if err := checkEntryPath(rel); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := dirEntry{rel: rel, isDir: d.IsDir(), modTime: info.ModTime()}
		if !e.isDir {
			e.size = info.Size()
			e.exec = info.Mode().Perm()&0o111 != 0
			plan.Files++
			plan.ContentSize += e.size
		}
		plan.entries = append(plan.entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if plan.TarSize, err = tarSize(plan.entries); err != nil {
		return nil, err
	}
	return plan, nil
}

// checkEntryPath applies the receiver's limits on entry paths (see
// server.MaxEntryDepth) to a slash-separated path relative to the folder.
func checkEntryPath(rel string) error {
	if n := strings.Count(rel, "/") + 1; n > server.MaxEntryDepth {
		return fmt.Errorf("cannot send %q: nested deeper than %d folders", rel, server.MaxEntryDepth)
	}
	if len(rel) > server.MaxEntryPathLen {
		return fmt.Errorf("cannot send %.100q...: path longer than %d bytes", rel, server.MaxEntryPathLen)
	}
	return nil
}

// header is the tar header written for e. Only the executable bit of the
// permissions travels; the receiver grants owner-only access.
func (e dirEntry) header() *tar.Header {
	if e.isDir {
		return &tar.Header{Name: e.rel + "/", Typeflag: tar.TypeDir, Mode: 0o700, ModTime: e.modTime}
	}
	mode := int64(0o600)
	if e.exec {
		mode = 0o700
	}
	return &tar.Header{Name: e.rel, Typeflag: tar.TypeReg, Mode: mode, Size: e.size, ModTime: e.modTime}
}

// tarSize computes the exact length writeTar will produce, without reading
// any file: each header as tar.Writer encodes it, each file's content padded
// to a 512-byte block, and the 1024-byte end-of-archive marker.
func tarSize(entries []dirEntry) (int64, error) {
	var total int64
	for _, e := range entries {
		var c countWriter
		if err := tar.NewWriter(&c).WriteHeader(e.header()); err != nil {
			return 0, err
		}
		total += c.n + (e.size+511)/512*512
	}
	return total + 1024, nil
}

// writeTar writes plan as a tar stream. A file that changed size, vanished,
// or stopped being a regular file since ScanDir is an error, so the stream
// never disagrees with TarSize.
func writeTar(w io.Writer, plan *DirPlan) error {
	tw := tar.NewWriter(w)
	for _, e := range plan.entries {
		if err := tw.WriteHeader(e.header()); err != nil {
			return err
		}
		if e.isDir {
			continue
		}
		if err := copyFile(tw, filepath.Join(plan.Root, filepath.FromSlash(e.rel)), e); err != nil {
			return err
		}
	}
	return tw.Close()
}

func copyFile(w io.Writer, path string, e dirEntry) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: %w", e.rel, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%s: %w", e.rel, err)
	}
	if !info.Mode().IsRegular() || info.Size() != e.size {
		return fmt.Errorf("%s changed while sending", e.rel)
	}
	if _, err := io.CopyN(w, f, e.size); err != nil {
		return fmt.Errorf("%s: %w", e.rel, err)
	}
	return nil
}

// countWriter discards what is written and counts the bytes.
type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// ErrDirsUnsupported is returned by SendDir when the receiver runs a lanxfer
// version without folder support.
var ErrDirsUnsupported = errors.New("the receiver does not support folders; update lanxfer on it")

// SendDir uploads the folder described by plan to the lanxfer receiver at
// host:port as one tar stream, built while it is being sent. Cancelling ctx
// aborts the transfer; the receiver then discards what it got. Result.Size
// is the length of the tar stream.
func SendDir(ctx context.Context, host string, port int, plan *DirPlan, opts SendOptions) (*Result, error) {
	pr, pw := io.Pipe()
	writeErr := make(chan error, 1)
	go func() {
		err := writeTar(pw, plan)
		pw.CloseWithError(err)
		writeErr <- err
	}()

	var body io.Reader = pr
	if opts.Progress != nil {
		body = &countingReader{r: pr, total: plan.TarSize, report: opts.Progress}
	}
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/dirs/" + plan.Name, // percent-encoded when the request is written
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), body)
	if err != nil {
		pr.Close()
		<-writeErr
		return nil, err
	}
	req.ContentLength = plan.TarSize
	req.Header.Set("Content-Type", "application/x-tar")
	if opts.From != "" {
		req.Header.Set(server.FromHeader, opts.From)
	}

	start := time.Now()
	resp, err := newHTTPClient().Do(req)
	// The transport may stop reading the body early (an error, or a response
	// sent before the upload ended). Closing the pipe unblocks the writer.
	pr.Close()
	werr := <-writeErr
	if err != nil {
		if werr != nil && !errors.Is(werr, io.ErrClosedPipe) {
			return nil, werr // a local problem, such as a file that changed, is the real cause
		}
		return nil, fmt.Errorf("send to %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusNotFound:
		return nil, ErrDirsUnsupported
	default:
		return nil, responseError(u.Host, resp)
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &Result{
		RemoteName: strings.TrimSpace(string(respBody)),
		Size:       plan.TarSize,
		Duration:   time.Since(start),
	}, nil
}
