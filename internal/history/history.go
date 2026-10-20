// Package history keeps an append-only log of sends and receipts as JSON
// Lines. Entries are never rewritten or removed.
package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Direction of a transfer, relative to this device.
const (
	In  = "in"
	Out = "out"
)

// What was transferred.
const (
	KindText   = "text"
	KindFile   = "file"
	KindFolder = "folder"
)

// How the transfer ended.
const (
	StatusOK        = "ok"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// MaxText is the largest text a single entry may carry. It matches
// server.MaxMessageSize.
const MaxText = 1 << 20

const version = 1

// Entry is one line of the history file.
type Entry struct {
	V        int       `json:"v"` // always 1
	Time     time.Time `json:"time"`
	Dir      string    `json:"dir"`
	PeerID   string    `json:"peer_id,omitempty"`
	PeerName string    `json:"peer_name,omitempty"`
	PeerAddr string    `json:"peer_addr,omitempty"` // IP only
	Kind     string    `json:"kind"`
	Text     string    `json:"text,omitempty"`
	Name     string    `json:"name,omitempty"`     // name as sent
	SavedAs  string    `json:"saved_as,omitempty"` // name the receiver used
	Path     string    `json:"path,omitempty"`     // saved path (in) or source path (out)
	Size     int64     `json:"size,omitempty"`
	Files    int       `json:"files,omitempty"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
}

// Validate reports whether the entry can be written to the history file.
func (e Entry) Validate() error {
	if e.V != version {
		return fmt.Errorf("history: unsupported version %d", e.V)
	}
	switch e.Dir {
	case In, Out:
	default:
		return fmt.Errorf("history: invalid dir %q", e.Dir)
	}
	switch e.Kind {
	case KindText, KindFile, KindFolder:
	default:
		return fmt.Errorf("history: invalid kind %q", e.Kind)
	}
	switch e.Status {
	case StatusOK, StatusFailed, StatusCancelled:
	default:
		return fmt.Errorf("history: invalid status %q", e.Status)
	}
	if len(e.Text) > MaxText {
		return fmt.Errorf("history: text is %d bytes, limit is %d", len(e.Text), MaxText)
	}
	return nil
}

// Store appends entries to one history file. Separate Stores (and
// processes) on the same path are safe: each entry is written with a
// single append-mode write, which local file systems do not interleave
// with other appends.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns a Store for path. It neither creates nor opens the file;
// the parent folder must already exist when Append is called.
func Open(path string) *Store {
	return &Store{path: path}
}

// Append validates e and adds it to the file. It sets V to 1, and Time to
// the current time if it is zero. The file is created with mode 0600 if
// missing.
func (s *Store) Append(e Entry) error {
	e.V = version
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if err := e.Validate(); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("history: encode entry: %w", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	// O_RDWR (not O_WRONLY) so the last byte can be inspected below.
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("history: open %s: %w", s.path, err)
	}
	// A crash or full disk can leave a fragment without a trailing newline.
	// Start on a fresh line so the fragment does not swallow this entry. If
	// another writer appends between this check and the write, the worst
	// case is an extra empty line, which ReadAll skips.
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	// One Write call keeps the line whole against concurrent appenders.
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("history: write %s: %w", s.path, werr)
	}
	if cerr != nil {
		return fmt.Errorf("history: close %s: %w", s.path, cerr)
	}
	return nil
}

// ReadAll returns every well-formed entry in the file, oldest first. Lines
// that are not valid JSON or have an unknown version are skipped. A missing
// file yields nil, nil.
func ReadAll(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("history: open %s: %w", path, err)
	}
	defer f.Close()

	// bufio.Reader, unlike bufio.Scanner, has no line length cap; a text
	// entry can be over 1 MiB once JSON-escaped.
	r := bufio.NewReader(f)
	var entries []Entry
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e Entry
			if json.Unmarshal(line, &e) == nil && e.V == version {
				entries = append(entries, e)
			}
		}
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return entries, fmt.Errorf("history: read %s: %w", path, err)
		}
	}
}
