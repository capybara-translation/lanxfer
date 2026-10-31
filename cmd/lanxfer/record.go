package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/identity"
)

// Variables so tests can run without a config folder or a local receiver.
var (
	configDirFunc   = identity.ConfigDir
	localRecordFunc = client.LocalRecord
)

// deviceID returns this machine's device ID for the sender header. The ID is
// optional, so a failure only prints a warning to warn and returns "".
func deviceID(warn io.Writer) string {
	dir, err := configDirFunc()
	if err == nil {
		var id string
		if id, err = identity.LoadOrCreate(dir); err == nil {
			return id
		}
	}
	fmt.Fprintf(warn, "warning: no device ID: %v\n", err)
	return ""
}

// recordSent writes a send to the history. When a receiver runs on this
// machine it takes the entry, which keeps it the only writer of the file.
// Otherwise the entry is appended directly, which includes the case of a
// receiver that belongs to another device ID (another user's, on a shared
// machine). A failure prints one warning to warn and never changes the result
// of the command. selfID is this machine's device ID.
func recordSent(port int, selfID string, e history.Entry, warn io.Writer) {
	err := localRecordFunc(port, selfID, e)
	if errors.Is(err, client.ErrNoLocalReceiver) {
		err = appendHistory(e)
	}
	if err != nil {
		fmt.Fprintf(warn, "warning: could not record history: %v\n", err)
	}
}

// appendHistory appends e to the history file in the config folder, creating
// the folder when needed.
func appendHistory(e history.Entry) error {
	dir, err := configDirFunc()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return history.Open(filepath.Join(dir, "history.jsonl")).Append(e)
}
