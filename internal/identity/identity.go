// Package identity manages the per-device ID and the config folder that
// holds it.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

// LoadOrCreate returns the device ID stored in dir/device-id. If the file
// is missing or its content is not a valid ID, a new random ID is written
// (folder mode 0700, file mode 0600) and returned.
func LoadOrCreate(dir string) (string, error) {
	path := filepath.Join(dir, idFileName)
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); Valid(id) {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read device id: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	var raw [idBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate device id: %w", err)
	}
	id := hex.EncodeToString(raw[:])
	if err := writeFileAtomic(path, []byte(id+"\n")); err != nil {
		return "", fmt.Errorf("write device id: %w", err)
	}
	return id, nil
}

// writeFileAtomic writes data to a temp file in the same folder and renames
// it over path, so a crash never leaves a half-written file. The temp file
// is created with mode 0600.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		os.Remove(tmpName)
	}
	return err
}
