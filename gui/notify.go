package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/capybara-translation/lanxfer/internal/chat"
	"github.com/capybara-translation/lanxfer/internal/history"
)

// This file holds the parts of notifications that do not depend on the OS:
// when to notify, what to say, and how the small notification window on macOS
// behaves over time. It has no cgo and no Wails imports, so "go test" covers
// it everywhere.

const (
	// notifyBodyRunes is the longest notification body.
	notifyBodyRunes = 200
	// receivedPrefix starts the body of a received file or folder.
	receivedPrefix = "Received "
	// notifyTitleRunes is the longest notification title.
	notifyTitleRunes = 64

	toastWidth  = 340
	toastHeight = 84
	// toastMargin is the gap between the toast and the screen's work area.
	toastMargin = 16
	// toastDuration is how long a toast stays after the last arrival.
	toastDuration = 5 * time.Second
)

// notifier shows that something arrived. Show must return at once: it is
// called from the goroutine that updates the chats.
type notifier interface {
	Show(title, body string, key chat.Key)
}

// shouldNotify reports whether an entry that was added to chat key should
// raise a notification. Only received entries do, and not when the user is
// already looking at that chat: openKey is the chat the window shows, and
// focused says the window is in front and has the keyboard.
func shouldNotify(e history.Entry, openKey, key chat.Key, focused bool) bool {
	if e.Dir != history.In {
		return false
	}
	return !(focused && openKey == key)
}

// notificationBody returns the text of a notification for a received entry:
// the message itself, or "Received report.pdf (1.2 MiB)" and "Received trip/
// (152 files)". The text comes from another machine, so Preview cleans it.
func notificationBody(e history.Entry) string {
	if e.Kind == history.KindText {
		return chat.Preview(e, notifyBodyRunes)
	}
	return receivedPrefix + chat.Preview(e, notifyBodyRunes-len(receivedPrefix))
}

// focusTracker says whether the main window is in front and usable. Focus
// comes from the window's focus and blur events; visibility from the code that
// shows and hides the window. Both are atomics, so active can be called from
// any goroutine without waiting for the main thread.
type focusTracker struct {
	focused atomic.Bool
	visible atomic.Bool
}

// newFocusTracker returns a tracker for a window that is shown (or not) at the
// start and has no focus yet.
func newFocusTracker(visible bool) *focusTracker {
	f := &focusTracker{}
	f.visible.Store(visible)
	return f
}

func (f *focusTracker) setFocused(v bool) { f.focused.Store(v) }
func (f *focusTracker) setVisible(v bool) { f.visible.Store(v) }

// active reports whether the window has focus and is visible.
func (f *focusTracker) active() bool {
	return f.focused.Load() && f.visible.Load()
}

// notificationTitle returns the title for a chat's display name. The name was
// chosen by the peer, so control and bidirectional-control characters are
// removed and it is cut to notifyTitleRunes.
func notificationTitle(name string) string {
	if t := chat.Label(name, notifyTitleRunes); t != "" {
		return t
	}
	return "Unknown"
}

// systemText prepares title or body text for the system's notifications. On
// Linux the text is interpreted as markup by some notification daemons, so a
// peer could show a link: &, < and > are escaped there. Other systems show the
// text as it is.
func systemText(goos, s string) string {
	if goos != "linux" {
		return s
	}
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// toastOrigin returns the top-left corner for a toast of the given width in
// the top-right of a work area: margin from the top and right edges. The left
// edge stays inside the work area when the area is narrower than the toast.
func toastOrigin(workX, workY, workWidth, width, margin int) (x, y int) {
	x = workX + workWidth - width - margin
	if x < workX {
		x = workX
	}
	return x, workY + margin
}

// toastContent is what one toast shows. Key is kept on the Go side: a click
// opens the chat that Go remembers, so the page cannot choose it.
type toastContent struct {
	Title string
	Body  string
	Key   chat.Key
}

// toastWindow is the small notification window. present fills it, moves it to
// the corner and shows it without taking keyboard focus; dismiss hides it.
type toastWindow interface {
	present(c toastContent)
	dismiss()
}

// toaster decides when the notification window shows and hides. A new arrival
// replaces the content and restarts the timer. All window calls happen on one
// goroutine, in order, so a late timer cannot hide a newer toast and Show never
// waits for the window.
type toaster struct {
	win      toastWindow
	openChat func(chat.Key)
	duration time.Duration
	logf     func(format string, args ...any)

	wake chan struct{} // capacity 1: "look at the state again"
	done chan struct{} // closed by Close
	once sync.Once

	mu     sync.Mutex
	closed bool
	ready  bool // the page has subscribed to the content event
	want   bool // a toast should be on screen
	cur    toastContent
	at     time.Time // when cur was shown
	gen    int       // counts Show calls
	pages  int       // counts pageReady calls: one per page load
	timer  *time.Timer
	// readyTimer fires once if the page has not reported ready in time.
	readyTimer *time.Timer
}

// toasterConfig holds the settings of a toaster.
type toasterConfig struct {
	// Duration is how long a toast stays after the last arrival.
	Duration time.Duration
	// ReadyWait is how long to wait for the page before logging a warning;
	// zero means never.
	ReadyWait time.Duration
	// Logf logs a warning; nil discards.
	Logf func(format string, args ...any)
}

func newToaster(win toastWindow, openChat func(chat.Key), cfg toasterConfig) *toaster {
	t := &toaster{
		win:      win,
		openChat: openChat,
		duration: cfg.Duration,
		logf:     cfg.Logf,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	if t.logf == nil {
		t.logf = func(string, ...any) {}
	}
	if cfg.ReadyWait > 0 {
		t.readyTimer = time.AfterFunc(cfg.ReadyWait, func() { t.warnNotReady(cfg.ReadyWait) })
	}
	go t.run()
	return t
}

// warnNotReady logs, once, that the page never reported ready: without it a
// toast cannot be shown.
func (t *toaster) warnNotReady(wait time.Duration) {
	t.mu.Lock()
	skip := t.ready || t.closed
	t.mu.Unlock()
	if !skip {
		t.logf("warning: the notification page did not report ready within %s; notifications are held until it does", wait)
	}
}

// Show displays a notification, or replaces the one on screen and restarts the
// timer. It never blocks.
func (t *toaster) Show(title, body string, key chat.Key) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.cur = toastContent{Title: title, Body: body, Key: key}
	t.at = time.Now()
	t.want = true
	t.gen++
	t.mu.Unlock()
	t.poke()
}

// pageReady is called each time the notification page reports that it listens:
// after it loaded, and again after a reload. Content that arrived earlier is
// shown now, unless it is already older than a toast lasts, and a toast that is
// on screen is sent to the page again, because a reloaded page has lost it.
func (t *toaster) pageReady() {
	t.mu.Lock()
	t.ready = true
	t.pages++
	t.mu.Unlock()
	t.poke()
}

// clicked is called when the user clicks the toast: it opens the newest chat
// and hides the toast. A click when no toast is on screen does nothing.
func (t *toaster) clicked() {
	t.mu.Lock()
	if t.closed || !t.want {
		t.mu.Unlock()
		return
	}
	key := t.cur.Key
	t.want = false
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Unlock()
	t.poke()
	t.openChat(key)
}

// Close stops the timer and the worker. It does not wait for a window call in
// progress, which may be waiting for the main thread. It is safe to call more
// than once.
func (t *toaster) Close() {
	t.once.Do(func() {
		t.mu.Lock()
		t.closed = true
		if t.timer != nil {
			t.timer.Stop()
		}
		if t.readyTimer != nil {
			t.readyTimer.Stop()
		}
		t.mu.Unlock()
		close(t.done)
	})
}

func (t *toaster) poke() {
	select {
	case t.wake <- struct{}{}:
	default: // a pending wake-up will read the latest state
	}
}

// expire hides the toast of generation gen unless a newer one came.
func (t *toaster) expire(gen int) {
	t.mu.Lock()
	if t.closed || t.gen != gen || !t.want {
		t.mu.Unlock()
		return
	}
	t.want = false
	t.mu.Unlock()
	t.poke()
}

// arm starts the hide timer for generation gen, if it is still the latest.
func (t *toaster) arm(gen int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.gen != gen || !t.want {
		return
	}
	if t.timer != nil {
		t.timer.Stop()
	}
	t.timer = time.AfterFunc(t.duration, func() { t.expire(gen) })
}

func (t *toaster) run() {
	shown := false
	// What was last sent to the page: a wake-up with nothing new changes
	// nothing.
	lastGen, lastPages := 0, 0
	for {
		select {
		case <-t.done:
			return
		case <-t.wake:
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		ready, want, c, gen, at, pages := t.ready, t.want, t.cur, t.gen, t.at, t.pages
		t.mu.Unlock()

		switch {
		case want && ready && time.Since(at) >= t.duration:
			// It waited for the page longer than a toast lasts: drop it.
			t.expire(gen)
		case want && ready && (gen != lastGen || pages != lastPages):
			t.win.present(c)
			shown = true
			lastGen, lastPages = gen, pages
			t.arm(gen)
		case !want && shown:
			t.win.dismiss()
			shown = false
		}
	}
}
