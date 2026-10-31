package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/capybara-translation/lanxfer/internal/peername"
)

// settingsFileName is the settings file inside the lanxfer config folder.
const settingsFileName = "gui.json"

// Settings are the user's choices, kept in <config folder>/gui.json.
type Settings struct {
	// Name is announced to other machines.
	Name string `json:"name"`
	// SaveDir is the absolute path of the folder that receives files.
	SaveDir string `json:"saveDir"`
}

// DefaultSettings returns the settings used before the user changes anything:
// the host name, and ~/lanxfer, like "lanxfer recv". SaveDir is empty, with an
// error, when the home folder is unknown.
func DefaultSettings() (Settings, error) {
	hostname, _ := os.Hostname()
	// With an empty flag value Default cannot fail.
	name, _ := peername.Default("", hostname)
	s := Settings{Name: name}
	home, err := os.UserHomeDir()
	if err != nil {
		return s, fmt.Errorf("cannot determine the home folder: %w", err)
	}
	s.SaveDir = filepath.Join(home, "lanxfer")
	return s, nil
}

// LoadSettings reads the settings at path. A missing file gives the defaults
// without an error, and so does a field missing from the file. A file that
// cannot be read or parsed gives the defaults and an error. A field with an
// unusable value falls back to its default, the others are kept, and an error
// is returned. The result is always usable, except that SaveDir is empty if
// the home folder is unknown.
func LoadSettings(path string) (Settings, error) {
	def, defErr := DefaultSettings()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return def, nil
	}
	if err != nil {
		return def, fmt.Errorf("read settings: %w", err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return def, fmt.Errorf("parse %s: %w", path, err)
	}

	var problems []error
	if s.Name == "" {
		s.Name = def.Name
	} else if err := peername.Validate(s.Name); err != nil {
		problems = append(problems, fmt.Errorf("settings: %w", err))
		s.Name = def.Name
	}
	if s.SaveDir == "" {
		s.SaveDir = def.SaveDir
		if def.SaveDir == "" {
			problems = append(problems, defErr)
		}
	} else if !filepath.IsAbs(s.SaveDir) {
		problems = append(problems, fmt.Errorf("settings: save folder %q is not an absolute path", s.SaveDir))
		s.SaveDir = def.SaveDir
	}
	return s, errors.Join(problems...)
}

// Save writes the settings to path with mode 0600, creating the folder if
// needed. The file is replaced atomically: a crash leaves the old settings or
// the new ones, never half of either.
func (s Settings) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// CreateTemp makes the file 0600, and the temporary file is in the same
	// folder so that the rename cannot cross file systems.
	tmp, err := os.CreateTemp(dir, settingsFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmpName)
		return werr
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
