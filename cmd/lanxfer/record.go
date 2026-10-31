package main

import (
	"context"
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

// sendEntry describes one finished (or failed) send for the history. path is
// the source path to store and name the name that was sent; size is the file
// size, which a folder plan overrides with its content size. res may be nil
// when the transfer failed. ctx tells a cancellation from a failure.
func sendEntry(ctx context.Context, tg target, path, name string, size int64, plan *client.DirPlan, res *client.Result, err error) history.Entry {
	e := history.Entry{
		Dir:      history.Out,
		PeerID:   tg.ID,
		PeerName: tg.Name,
		PeerAddr: tg.Addr.Addr().String(),
		Kind:     history.KindFile,
		Name:     name,
		Path:     path,
		Size:     size,
	}
	if plan != nil {
		e.Kind, e.Name, e.Size, e.Files = history.KindFolder, plan.Name, plan.ContentSize, plan.Files
	}
	fillOutcome(ctx, &e, res, err)
	return e
}

// textEntry describes one sent message for the history.
func textEntry(tg target, text string, res *client.Result, err error) history.Entry {
	e := history.Entry{
		Dir:      history.Out,
		PeerID:   tg.ID,
		PeerName: tg.Name,
		PeerAddr: tg.Addr.Addr().String(),
		Kind:     history.KindText,
		Text:     text,
	}
	fillOutcome(context.Background(), &e, res, err)
	return e
}

// fillOutcome sets the result fields of e. The receiver's own ID, when its
// response carried one, wins over the one found while resolving the target,
// because it names the machine that actually answered.
func fillOutcome(ctx context.Context, e *history.Entry, res *client.Result, err error) {
	if res != nil && res.PeerID != "" {
		e.PeerID = res.PeerID
	}
	switch {
	case err == nil:
		e.Status = history.StatusOK
		if res != nil {
			e.SavedAs = res.RemoteName
		}
	case errors.Is(interrupted(ctx, err), errCancelled):
		e.Status, e.Error = history.StatusCancelled, err.Error()
	default:
		e.Status, e.Error = history.StatusFailed, err.Error()
	}
}

// absPath returns path as an absolute path, or path itself when it cannot be
// made absolute.
func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
