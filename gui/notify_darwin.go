//go:build darwin

package main

import (
	"log"
	"sync/atomic"
	"time"

	"github.com/capybara-translation/lanxfer/internal/chat"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// macOS refuses the system notification API to an ad-hoc signed app
// ("Notifications are not allowed for this application"), so a small window of
// our own is used instead. wails notifications.New() is not used on macOS.

const (
	// toastWindowName names the notification window. Events from the page
	// carry the name of the window that sent them, and only this one is trusted.
	toastWindowName = "toast"

	eventToastShow  = "toast-show"  // Go to page: {title, body, seq}
	eventToastShown = "toast-shown" // page to Go: the text of that seq is in place
	eventToastReady = "toast-ready" // page to Go: the page listens (after each load)
	eventToastClick = "toast-click" // page to Go: the user clicked

	// toastReadyWait is how long to wait for the page before warning in the log.
	toastReadyWait = 10 * time.Second
	// toastAckWait is the longest present waits for the page to confirm the
	// new text before it shows the window anyway.
	toastAckWait = 250 * time.Millisecond
)

// platformServices returns the services this OS adds. macOS adds none.
func platformServices() []application.Service { return nil }

// wailsToastWindow is the notification window on top of a Wails window.
type wailsToastWindow struct {
	app *application.App
	win *application.WebviewWindow
	log *log.Logger

	seq  atomic.Uint64 // numbers each present, so an ack can be matched
	acks chan uint64   // seq values the page has confirmed
}

// present fills the window, moves it to the top-right corner of the primary
// screen's work area and shows it. The new text is sent first and the window is
// shown only once the page confirms it (or after a short wait), so the first
// frame never shows an empty card or the previous toast's text. The window is a
// non-activating panel, and Wails shows such a panel with orderFrontRegardless:
// it comes to the front without becoming the key window and without activating
// this app, so whatever the user is typing into keeps the keyboard.
func (w *wailsToastWindow) present(c toastContent) {
	// The position is worked out again each time: the display or the Dock
	// may have changed since the last toast.
	if sc := w.app.Screen.GetPrimary(); sc != nil {
		x, y := toastOrigin(sc.WorkArea.X, sc.WorkArea.Y, sc.WorkArea.Width, toastWidth, toastMargin)
		w.win.SetPosition(x, y)
	} else {
		w.log.Printf("warning: no primary screen; the notification keeps its position")
	}
	seq := w.seq.Add(1)
	w.app.Event.Emit(eventToastShow, map[string]any{"title": c.Title, "body": c.Body, "seq": seq})
	w.waitAck(seq)
	w.win.Show()
}

// waitAck waits for the page to confirm seq, for at most toastAckWait. Older
// confirmations that arrive late are skipped.
func (w *wailsToastWindow) waitAck(seq uint64) {
	timer := time.NewTimer(toastAckWait)
	defer timer.Stop()
	for {
		select {
		case got := <-w.acks:
			if got == seq {
				return
			}
		case <-timer.C:
			return
		}
	}
}

// dismiss hides the window. The page keeps its text until the next toast
// replaces it, and the window is not shown again before that happens.
func (w *wailsToastWindow) dismiss() {
	w.win.Hide()
}

// newPlatformNotifier creates the notification window, hidden, and returns the
// notifier that drives it. openChat is called when the toast is clicked. The
// returned function stops the toast's timer; call it on quit.
func newPlatformNotifier(app *application.App, logger *log.Logger, openChat func(chat.Key)) (notifier, func()) {
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:          toastWindowName,
		Title:         "lanxfer notification",
		URL:           "/toast.html",
		Width:         toastWidth,
		Height:        toastHeight,
		Frameless:     true,
		DisableResize: true,
		AlwaysOnTop:   true,
		Hidden:        true,
		// No EnableFileDrop: the toast is not a drop target.
		Mac: application.MacWindow{
			// An NSPanel that does not activate the app: showing it, and
			// clicking it, leave the frontmost app and its key window alone.
			// It becomes key only if a clicked view needs the keyboard.
			WindowClass: application.MacWindowClassPanel,
			PanelPreferences: application.MacPanelPreferences{
				FloatingPanel:          true,
				NonActivating:          true,
				BecomesKeyOnlyIfNeeded: true,
			},
			WindowLevel: application.MacWindowLevelStatus,
			// On every Space, over full-screen apps, and out of Cmd+` cycling.
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces |
				application.MacWindowCollectionBehaviorFullScreenAuxiliary |
				application.MacWindowCollectionBehaviorStationary |
				application.MacWindowCollectionBehaviorIgnoresCycle,
			CornerRadius: 10,
		},
	})

	tw := &wailsToastWindow{app: app, win: win, log: logger, acks: make(chan uint64, 8)}
	ts := newToaster(tw, openChat, toasterConfig{
		Duration:  toastDuration,
		ReadyWait: toastReadyWait,
		Logf:      logger.Printf,
	})
	app.Event.On(eventToastShown, func(e *application.CustomEvent) {
		if n, ok := e.Data.(float64); ok && e.Sender == toastWindowName {
			select {
			case tw.acks <- uint64(n):
			default: // nobody is waiting; the oldest confirmations are not needed
			}
		}
	})
	app.Event.On(eventToastReady, func(e *application.CustomEvent) {
		if e.Sender == toastWindowName {
			ts.pageReady()
		}
	})
	app.Event.On(eventToastClick, func(e *application.CustomEvent) {
		if e.Sender == toastWindowName {
			ts.clicked()
		}
	})
	return ts, ts.Close
}
