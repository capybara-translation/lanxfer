package transfer

import (
	"context"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/target"
)

// Entry describes one finished (or failed) send of a file or folder for the
// history. path is the source path to store and name the name that was sent;
// size is the file size, which a folder plan overrides with its content size.
// res may be nil when the transfer failed. ctx tells a cancellation from a
// failure. The command line and the GUI share it.
func Entry(ctx context.Context, tg target.Target, path, name string, size int64, plan *client.DirPlan, res *client.Result, err error) history.Entry {
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

// TextEntry describes one sent message for the history. ctx tells a
// cancellation from a failure.
func TextEntry(ctx context.Context, tg target.Target, text string, res *client.Result, err error) history.Entry {
	e := history.Entry{
		Dir:      history.Out,
		PeerID:   tg.ID,
		PeerName: tg.Name,
		PeerAddr: tg.Addr.Addr().String(),
		Kind:     history.KindText,
		Text:     text,
	}
	fillOutcome(ctx, &e, res, err)
	return e
}

// fillOutcome sets the result fields of e. The receiver's own ID, when its
// response carried one, wins over the one found while resolving the target,
// because it names the machine that actually answered. A failure while ctx is
// done counts as a cancellation, since the failure is then the effect of the
// cancellation.
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
	case ctx.Err() != nil:
		e.Status, e.Error = history.StatusCancelled, err.Error()
	default:
		e.Status, e.Error = history.StatusFailed, err.Error()
	}
}
