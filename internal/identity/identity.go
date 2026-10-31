// Package identity manages the per-device ID and the config folder that
// holds it.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	appDirName = "lanxfer"
	idFileName = "device-id"
	idBytes    = 16 // 128 bits
)

// ConfigDir returns the lanxfer config folder (os.UserConfigDir()/lanxfer).
// It does not create the folder.
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, appDirName), nil
}

// Valid reports whether id is 32 lowercase hexadecimal characters.
func Valid(id string) bool {
	if len(id) != idBytes*2 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// linkFunc is a variable so tests can simulate a filesystem without hard
// links.
var linkFunc = os.Link

// LoadOrCreate returns the device ID stored in dir/device-id. If the file
// is missing or its content is not a valid ID, a new random ID is written
// (folder mode 0700, file mode 0600) and returned.
//
// Concurrent callers, even in different processes, agree on one ID: the new
// file is published with a hard link, which fails when the file already
// exists, so the first writer wins and the others return its ID. On a
// filesystem without hard links the file is renamed into place instead,
// which cannot tell a winner apart.
func LoadOrCreate(dir string) (string, error) {
	path := filepath.Join(dir, idFileName)
	id, ok, err := readID(path)
	if err != nil {
		return "", err
	}
	if ok {
		return id, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	var raw [idBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate device id: %w", err)
	}
	id = hex.EncodeToString(raw[:])
	// The temp file is complete before it gets its final name, so nobody
	// can read half an ID. After a rename the deferred Remove finds nothing
	// to do; after a link it removes the second name.
	tmp, err := writeTemp(dir, id+"\n")
	if err != nil {
		return "", fmt.Errorf("write device id: %w", err)
	}
	defer os.Remove(tmp)

	err = linkFunc(tmp, path)
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, fs.ErrExist):
		// Another caller created the file first. Use its ID unless the
		// content is corrupt, which only a replacement can fix.
		other, ok, rerr := readID(path)
		if rerr != nil {
			return "", rerr
		}
		if ok {
			return other, nil
		}
	}
	// Either the file is corrupt or hard links are not available here.
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("write device id: %w", err)
	}
	return id, nil
}

// readID reads the ID stored at path. ok is false when the file is missing
// or does not hold a valid ID.
func readID(path string) (id string, ok bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read device id: %w", err)
	}
	id = strings.TrimSpace(string(data))
	return id, Valid(id), nil
}

// writeTemp writes content to a new file (mode 0600) in dir, closes it, and
// returns its name.
func writeTemp(dir, content string) (string, error) {
	f, err := os.CreateTemp(dir, idFileName+".tmp-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}
