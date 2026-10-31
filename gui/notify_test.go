package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/chat"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/node"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

func incoming(kind string) history.Entry {
	return history.Entry{
		V: history.Version, Time: time.Now(), Dir: history.In, PeerID: testPeer.ID,
		PeerName: "other", Kind: kind, Status: history.StatusOK,
	}
}

func TestShouldNotify(t *testing.T) {
	in := incoming(history.KindText)
	out := in
	out.Dir = history.Out

	tests := []struct {
		name    string
		e       history.Entry
		openKey chat.Key
		key     chat.Key
		focused bool
		want    bool
	}{
		{"incoming, nothing open", in, "", "id:A", false, true},
		{"incoming, nothing open, window focused", in, "", "id:A", true, true},
		{"incoming for another open chat, focused", in, "id:B", "id:A", true, true},
		{"incoming for the open chat, window not focused", in, "id:A", "id:A", false, true},
		{"incoming for the open chat, window focused", in, "id:A", "id:A", true, false},
		{"outgoing never notifies", out, "", "id:A", false, false},
		{"outgoing to another chat", out, "id:B", "id:A", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldNotify(tt.e, tt.openKey, tt.key, tt.focused); got != tt.want {
				t.Errorf("shouldNotify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNotificationBody(t *testing.T) {
	text := incoming(history.KindText)
	text.Text = "line one\nline two"

	longText := incoming(history.KindText)
	longText.Text = strings.Repeat("a", 300)

	file := incoming(history.KindFile)
	file.Name, file.Size = "report.pdf", 1258291 // 1.2 MiB

	savedAs := incoming(history.KindFile)
	savedAs.SavedAs, savedAs.Size = "report (1).pdf", 10

	folder := incoming(history.KindFolder)
	folder.Name, folder.Files = "trip", 152

	oneFile := incoming(history.KindFolder)
	oneFile.Name, oneFile.Files = "solo", 1

	// The name comes from another machine: control and bidirectional control
	// characters must not reach the notification.
	nasty := incoming(history.KindFile)
	nasty.Name, nasty.Size = "a"+string(rune(0x202E))+"b\nc.txt", 5

	hugeName := incoming(history.KindFile)
	hugeName.Name, hugeName.Size = strings.Repeat("n", 500), 5

	tests := []struct {
		name string
		e    history.Entry
		want string
	}{
		{"text keeps one line", text, "line one line two"},
		{"file", file, "Received report.pdf (1.2 MiB)"},
		{"file falls back to the saved name", savedAs, "Received report (1).pdf (10 B)"},
		{"folder", folder, "Received trip/ (152 files)"},
		{"folder with one file", oneFile, "Received solo/ (1 file)"},
		{"control characters are removed", nasty, "Received a" + "b c.txt (5 B)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := notificationBody(tt.e); got != tt.want {
				t.Errorf("notificationBody = %q, want %q", got, tt.want)
			}
		})
	}

	if got := []rune(notificationBody(longText)); len(got) != 200 {
		t.Errorf("long text body has %d runes, want 200", len(got))
	}
	got := notificationBody(hugeName)
	if n := len([]rune(got)); n > 200 {
		t.Errorf("long file name body has %d runes, want at most 200", n)
	}
	if !strings.HasPrefix(got, "Received ") || !strings.HasSuffix(got, " (5 B)") {
		t.Errorf("a long name must be cut, not the framing: %q", got)
	}
}

func TestFocusTracker(t *testing.T) {
	visible := true
	f := &focusTracker{visible: func() bool { return visible }}
	if f.active() {
		t.Error("a new tracker reports an active window")
	}
	f.setFocused(true)
	if !f.active() {
		t.Error("focused and visible should be active")
	}
	visible = false
	if f.active() {
		t.Error("a focused window that is hidden must not count as active")
	}
	visible = true
	f.setFocused(false)
	if f.active() {
		t.Error("blur should clear the active state")
	}

	// Without a visibility check the focus alone decides.
	g := &focusTracker{}
	g.setFocused(true)
	if !g.active() {
		t.Error("focus alone should be active when no visibility check is set")
	}
}

func TestToastOrigin(t *testing.T) {
	// A work area that starts at (0, 25) because of a menu bar, 1440 wide.
	if x, y := toastOrigin(0, 25, 1440, 340, 16); x != 1084 || y != 41 {
		t.Errorf("origin = (%d, %d), want (1084, 41)", x, y)
	}
	// A second display to the left has a negative X.
	if x, y := toastOrigin(-1920, 0, 1920, 340, 16); x != -356 || y != 16 {
		t.Errorf("origin on a left display = (%d, %d), want (-356, 16)", x, y)
	}
	// A work area narrower than the toast keeps the left edge on screen.
	if x, _ := toastOrigin(100, 0, 200, 340, 16); x != 100 {
		t.Errorf("x on a narrow work area = %d, want 100", x)
	}
}

// fakeToastWindow records what a toaster asks the window to do.
type fakeToastWindow struct {
	mu       sync.Mutex
	calls    []string
	presents []toastContent
	block    chan struct{} // when non-nil, present waits for it
	entered  chan struct{} // receives once per present call when non-nil
}

func (f *fakeToastWindow) present(c toastContent) {
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.calls = append(f.calls, "present")
	f.presents = append(f.presents, c)
	f.mu.Unlock()
}

func (f *fakeToastWindow) dismiss() {
	f.mu.Lock()
	f.calls = append(f.calls, "dismiss")
	f.mu.Unlock()
}

func (f *fakeToastWindow) snapshot() ([]string, []toastContent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]toastContent(nil), f.presents...)
}

func (f *fakeToastWindow) count(call string) int {
	calls, _ := f.snapshot()
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}

// newReadyToaster returns a toaster whose page has said it is ready.
func newReadyToaster(t *testing.T, w toastWindow, d time.Duration, open func(chat.Key)) *toaster {
	t.Helper()
	if open == nil {
		open = func(chat.Key) {}
	}
	ts := newToaster(w, open, d)
	t.Cleanup(ts.Close)
	ts.pageReady()
	return ts
}

func TestToasterShowsThenHides(t *testing.T) {
	w := &fakeToastWindow{}
	ts := newReadyToaster(t, w, 60*time.Millisecond, nil)

	ts.Show("other", "hello", "id:A")
	waitFor(t, "the toast to show", func() bool { return w.count("present") == 1 })
	_, presents := w.snapshot()
	if presents[0].Title != "other" || presents[0].Body != "hello" {
		t.Errorf("presented %+v", presents[0])
	}
	waitFor(t, "the toast to hide", func() bool { return w.count("dismiss") == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := w.count("dismiss"); n != 1 {
		t.Errorf("dismissed %d times, want once", n)
	}
}

func TestToasterReplacesContentAndExtendsTime(t *testing.T) {
	w := &fakeToastWindow{}
	const d = 150 * time.Millisecond
	ts := newReadyToaster(t, w, d, nil)

	ts.Show("a", "first", "id:A")
	waitFor(t, "the first toast", func() bool { return w.count("present") == 1 })
	time.Sleep(100 * time.Millisecond)
	ts.Show("b", "second", "id:B")
	waitFor(t, "the second toast", func() bool { return w.count("present") == 2 })

	// 100 ms + 100 ms is past the first deadline but within the extended one.
	time.Sleep(100 * time.Millisecond)
	if n := w.count("dismiss"); n != 0 {
		t.Fatalf("the toast was hidden %d times before the extended deadline", n)
	}
	waitFor(t, "the toast to hide", func() bool { return w.count("dismiss") == 1 })
	_, presents := w.snapshot()
	if presents[1].Body != "second" {
		t.Errorf("second presentation = %+v", presents[1])
	}
}

func TestToasterClickOpensTheNewest(t *testing.T) {
	w := &fakeToastWindow{}
	var mu sync.Mutex
	var opened []chat.Key
	ts := newReadyToaster(t, w, time.Minute, func(k chat.Key) {
		mu.Lock()
		opened = append(opened, k)
		mu.Unlock()
	})

	ts.Show("a", "first", "id:A")
	ts.Show("b", "second", "id:B")
	waitFor(t, "the newest toast", func() bool {
		_, p := w.snapshot()
		return len(p) > 0 && p[len(p)-1].Key == "id:B"
	})
	ts.clicked()

	mu.Lock()
	got := append([]chat.Key(nil), opened...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "id:B" {
		t.Fatalf("opened %v, want [id:B]", got)
	}
	waitFor(t, "the toast to hide after the click", func() bool { return w.count("dismiss") == 1 })

	// A second click on a toast that is gone does nothing.
	ts.clicked()
	mu.Lock()
	n := len(opened)
	mu.Unlock()
	if n != 1 {
		t.Errorf("a click with no toast opened a chat (%d opens)", n)
	}
}

func TestToasterClickWithoutToastIsIgnored(t *testing.T) {
	var opens atomic.Int32
	ts := newReadyToaster(t, &fakeToastWindow{}, time.Minute, func(chat.Key) { opens.Add(1) })
	ts.clicked()
	if opens.Load() != 0 {
		t.Error("clicking before any toast opened a chat")
	}
}

func TestToasterWaitsForThePage(t *testing.T) {
	w := &fakeToastWindow{}
	ts := newToaster(w, func(chat.Key) {}, time.Minute)
	t.Cleanup(ts.Close)

	ts.Show("a", "early", "id:A")
	time.Sleep(50 * time.Millisecond)
	if n := w.count("present"); n != 0 {
		t.Fatalf("presented %d times before the page was ready", n)
	}
	ts.pageReady()
	waitFor(t, "the held toast", func() bool { return w.count("present") == 1 })
	_, p := w.snapshot()
	if p[0].Body != "early" {
		t.Errorf("presented %+v", p[0])
	}
}

func TestToasterShowDoesNotBlock(t *testing.T) {
	w := &fakeToastWindow{block: make(chan struct{}), entered: make(chan struct{}, 8)}
	ts := newReadyToaster(t, w, time.Minute, nil)
	defer close(w.block)

	ts.Show("a", "one", "id:A")
	<-w.entered // the window call is now stuck

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			ts.Show("a", "more", "id:A")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Show blocked while the window call was stuck")
	}
}

func TestToasterCloseStopsTheTimer(t *testing.T) {
	w := &fakeToastWindow{}
	ts := newToaster(w, func(chat.Key) {}, 80*time.Millisecond)
	ts.pageReady()
	ts.Show("a", "x", "id:A")
	waitFor(t, "the toast", func() bool { return w.count("present") == 1 })
	ts.Close()
	ts.Close() // safe to repeat
	time.Sleep(200 * time.Millisecond)
	if n := w.count("dismiss"); n != 0 {
		t.Errorf("a closed toaster still hid the window %d times", n)
	}
	// Showing after Close must not start anything again.
	ts.Show("a", "late", "id:A")
	time.Sleep(50 * time.Millisecond)
	if n := w.count("present"); n != 1 {
		t.Errorf("presented %d times, want 1", n)
	}
}

// recordingNotifier records the notifications a Service asks for.
type recordingNotifier struct {
	mu   sync.Mutex
	list []toastContent
}

func (r *recordingNotifier) Show(title, body string, key chat.Key) {
	r.mu.Lock()
	r.list = append(r.list, toastContent{Title: title, Body: body, Key: key})
	r.mu.Unlock()
}

func (r *recordingNotifier) all() []toastContent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]toastContent(nil), r.list...)
}

func TestServiceNotifiesOnIncoming(t *testing.T) {
	s, em, _ := newTestService(t, svcOpts{})
	rec := &recordingNotifier{}
	active := false
	s.setNotifier(rec, func() bool { return active })

	s.app.cfg.OnEvent(node.Event{Peers: []presence.Peer{testPeer}})
	text := incoming(history.KindText)
	text.Text = "hi there"
	s.app.cfg.OnEvent(node.Event{Entry: &text})
	barrier(t, s, em)

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("notifications = %+v, want one", got)
	}
	if got[0].Title != "other" || got[0].Body != "hi there" || got[0].Key != testPeerKey {
		t.Errorf("notification = %+v", got[0])
	}

	file := incoming(history.KindFile)
	file.Name, file.Size = "report.pdf", 1258291
	s.app.cfg.OnEvent(node.Event{Entry: &file})
	barrier(t, s, em)
	if got := rec.all(); len(got) != 2 || got[1].Body != "Received report.pdf (1.2 MiB)" {
		t.Fatalf("notifications = %+v", got)
	}
}

func TestServiceDoesNotNotifyForOpenFocusedChat(t *testing.T) {
	s, em, _ := newTestService(t, svcOpts{})
	rec := &recordingNotifier{}
	active := true
	s.setNotifier(rec, func() bool { return active })

	s.app.cfg.OnEvent(node.Event{Peers: []presence.Peer{testPeer}})
	barrier(t, s, em)
	s.OpenChat(testPeerKey)

	text := incoming(history.KindText)
	text.Text = "while you look"
	s.app.cfg.OnEvent(node.Event{Entry: &text})
	barrier(t, s, em)
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("notified for the chat in front: %+v", got)
	}

	// Once the window loses focus the same chat does notify.
	active = false
	s.app.cfg.OnEvent(node.Event{Entry: &text})
	barrier(t, s, em)
	if got := rec.all(); len(got) != 1 {
		t.Fatalf("notifications after blur = %+v, want one", got)
	}
}

func TestServiceDoesNotNotifyForSentEntries(t *testing.T) {
	s, em, _ := newTestService(t, svcOpts{})
	rec := &recordingNotifier{}
	s.setNotifier(rec, func() bool { return false })

	out := incoming(history.KindText)
	out.Dir, out.Text = history.Out, "sent"
	s.queue.push(work{kind: workAdd, entry: out})
	barrier(t, s, em)
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("notified for a sent message: %+v", got)
	}
}

func TestServiceDoesNotNotifyForLoadedHistory(t *testing.T) {
	old := incoming(history.KindText)
	old.Text = "from the file"
	old.Time = time.Now().Add(-time.Hour)
	s, em, _ := newTestService(t, svcOpts{skipStart: true, readHist: func(string) ([]history.Entry, error) {
		return []history.Entry{old}, nil
	}})
	rec := &recordingNotifier{}
	s.setNotifier(rec, func() bool { return false })
	s.start()
	<-s.ready
	barrier(t, s, em)
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("notified for the loaded history: %+v", got)
	}
}

func TestServiceNotifyIsOptional(t *testing.T) {
	// No notifier set: an arrival must not panic.
	s, em, _ := newTestService(t, svcOpts{})
	text := incoming(history.KindText)
	text.Text = "x"
	s.app.cfg.OnEvent(node.Event{Entry: &text})
	barrier(t, s, em)
}
