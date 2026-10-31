package main

import (
	"os"
	"path/filepath"
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
	f := newFocusTracker(true)
	if f.active() {
		t.Error("a new tracker reports an active window before any focus event")
	}
	f.setFocused(true)
	if !f.active() {
		t.Error("focused and visible should be active")
	}
	f.setVisible(false)
	if f.active() {
		t.Error("a focused window that is hidden must not count as active")
	}
	f.setVisible(true)
	if !f.active() {
		t.Error("showing the window again should restore the active state")
	}
	f.setFocused(false)
	if f.active() {
		t.Error("blur should clear the active state")
	}

	// A window that starts hidden is not active even after a focus event.
	g := newFocusTracker(false)
	g.setFocused(true)
	if g.active() {
		t.Error("a hidden window must not be active")
	}
}

func TestSystemBody(t *testing.T) {
	in := `<a href="http://x">&click</a>`
	if got, want := systemBody("linux", in), `&lt;a href="http://x"&gt;&amp;click&lt;/a&gt;`; got != want {
		t.Errorf("linux = %q, want %q", got, want)
	}
	for _, goos := range []string{"windows", "darwin"} {
		if got := systemBody(goos, in); got != in {
			t.Errorf("%s changed the text: %q", goos, got)
		}
	}
	// The title is not passed through this function: "A&B" must reach the
	// summary as it is. Already-escaped body text is escaped again.
	if got := systemBody("linux", "&lt;"); got != "&amp;lt;" {
		t.Errorf("linux double escape = %q", got)
	}
}

func TestSystemNotificationTextEscapesOnlyTheBody(t *testing.T) {
	title, body := systemNotificationText("linux", "A&B <x>", "A&B <x>")
	if title != "A&B <x>" {
		t.Errorf("title = %q, want it unchanged", title)
	}
	if body != "A&amp;B &lt;x&gt;" {
		t.Errorf("body = %q", body)
	}
	if title, body := systemNotificationText("windows", "A&B", "A&B"); title != "A&B" || body != "A&B" {
		t.Errorf("windows = %q, %q", title, body)
	}
}

func TestNotificationTitle(t *testing.T) {
	rlo := string(rune(0x202E))
	nel := string(rune(0x0085))
	ls := string(rune(0x2028))
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "other", "other"},
		{"controls and bidi are removed", "a" + rlo + "b" + nel + "c\nd" + ls + "e", "abc d e"},
		{"long names are cut at 64 runes", strings.Repeat("n", 100), strings.Repeat("n", 64)},
		{"nothing left falls back", rlo + nel, "Unknown"},
		{"empty falls back", "", "Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := notificationTitle(tt.in); got != tt.want {
				t.Errorf("notificationTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
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
	ts := newHeldToaster(t, w, toasterConfig{Duration: d}, open)
	ts.pageReady()
	return ts
}

// newHeldToaster returns a toaster whose page has not said it is ready.
func newHeldToaster(t *testing.T, w toastWindow, cfg toasterConfig, open func(chat.Key)) *toaster {
	t.Helper()
	if open == nil {
		open = func(chat.Key) {}
	}
	ts := newToaster(w, open, cfg)
	t.Cleanup(ts.Close)
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
	// Wide margins: 250 ms either side of every deadline, so a loaded machine
	// does not make the test flaky.
	const d = 600 * time.Millisecond
	ts := newReadyToaster(t, w, d, nil)

	ts.Show("a", "first", "id:A")
	waitFor(t, "the first toast", func() bool { return w.count("present") == 1 })
	time.Sleep(350 * time.Millisecond)
	ts.Show("b", "second", "id:B")
	waitFor(t, "the second toast", func() bool { return w.count("present") == 2 })

	// 350 ms + 350 ms is past the first deadline (600 ms) but well within the
	// extended one (350 ms + 600 ms).
	time.Sleep(350 * time.Millisecond)
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
	ts := newHeldToaster(t, w, toasterConfig{Duration: time.Minute}, nil)

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
	ts := newToaster(w, func(chat.Key) {}, toasterConfig{Duration: 80 * time.Millisecond})
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

func TestToasterDropsContentOlderThanTheToast(t *testing.T) {
	w := &fakeToastWindow{}
	ts := newHeldToaster(t, w, toasterConfig{Duration: 100 * time.Millisecond}, nil)
	ts.Show("a", "stale", "id:A")
	time.Sleep(250 * time.Millisecond)
	ts.pageReady()
	time.Sleep(100 * time.Millisecond)
	if n := w.count("present"); n != 0 {
		t.Errorf("a toast older than its own duration was shown (%d presentations)", n)
	}
	// Newer content still goes through.
	ts.Show("b", "fresh", "id:B")
	waitFor(t, "the fresh toast", func() bool { return w.count("present") == 1 })
}

func TestToasterResendsAfterPageReload(t *testing.T) {
	w := &fakeToastWindow{}
	ts := newReadyToaster(t, w, time.Minute, nil)
	ts.Show("a", "current", "id:A")
	waitFor(t, "the toast", func() bool { return w.count("present") == 1 })

	// The page reloaded and says it is ready again: the toast on screen is
	// sent again.
	ts.pageReady()
	waitFor(t, "the content to be sent again", func() bool { return w.count("present") == 2 })
	_, p := w.snapshot()
	if p[1].Body != "current" {
		t.Errorf("resent %+v", p[1])
	}
}

func TestToasterLogsOnceWhenThePageNeverComes(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, format)
		mu.Unlock()
	}
	ts := newHeldToaster(t, &fakeToastWindow{}, toasterConfig{
		Duration: time.Minute, ReadyWait: 50 * time.Millisecond, Logf: logf,
	}, nil)
	ts.Show("a", "x", "id:A")
	waitFor(t, "the warning", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) == 1
	})
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	n := len(lines)
	mu.Unlock()
	if n != 1 {
		t.Errorf("logged %d times, want once", n)
	}
}

func TestToasterDoesNotLogWhenThePageComes(t *testing.T) {
	var n atomic.Int32
	ts := newHeldToaster(t, &fakeToastWindow{}, toasterConfig{
		Duration: time.Minute, ReadyWait: 50 * time.Millisecond, Logf: func(string, ...any) { n.Add(1) },
	}, nil)
	ts.pageReady()
	time.Sleep(150 * time.Millisecond)
	if n.Load() != 0 {
		t.Error("logged although the page was ready")
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

func TestServiceSanitizesTheTitle(t *testing.T) {
	s, em, _ := newTestService(t, svcOpts{})
	rec := &recordingNotifier{}
	s.setNotifier(rec, func() bool { return false })

	rlo := string(rune(0x202E))
	p := testPeer
	p.Name = "ev" + rlo + "il\nname" + strings.Repeat("z", 100)
	s.app.cfg.OnEvent(node.Event{Peers: []presence.Peer{p}})
	text := incoming(history.KindText)
	text.Text = "hi"
	s.app.cfg.OnEvent(node.Event{Entry: &text})
	barrier(t, s, em)

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("notifications = %+v", got)
	}
	if strings.ContainsAny(got[0].Title, rlo+"\n") || len([]rune(got[0].Title)) > 64 {
		t.Errorf("title not cleaned: %q", got[0].Title)
	}
	if !strings.HasPrefix(got[0].Title, "evil name") {
		t.Errorf("title = %q", got[0].Title)
	}
}

func TestServiceSentEntryDoesNotAskTheWindow(t *testing.T) {
	s, _, _ := newTestService(t, svcOpts{})
	rec := &recordingNotifier{}
	s.setNotifier(rec, func() bool {
		t.Error("the window state was read for an entry that cannot notify")
		return false
	})
	out := incoming(history.KindText)
	out.Dir = history.Out
	s.notifyArrival(out, testPeerKey)
}

func TestServiceDoesNotNotifyWhileQuitting(t *testing.T) {
	s, _, _ := newTestService(t, svcOpts{})
	rec := &recordingNotifier{}
	s.setNotifier(rec, func() bool {
		t.Error("the window state was read while quitting")
		return false
	})
	s.quit()
	s.notifyArrival(incoming(history.KindText), testPeerKey)
	if got := rec.all(); len(got) != 0 {
		t.Errorf("notified while quitting: %+v", got)
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

// A page that reloads starts with nothing selected and says so with
// OpenChat(""). Both sides must then agree: a drop has no target, an arrival
// counts as unread, and a focused window is still notified.
func TestOpenChatEmptyClearsTheOpenChat(t *testing.T) {
	s, em, _ := newTestService(t, svcOpts{})
	rec := &recordingNotifier{}
	s.setNotifier(rec, func() bool { return true })

	s.app.cfg.OnEvent(node.Event{Peers: []presence.Peer{testPeer}})
	barrier(t, s, em)
	s.OpenChat(testPeerKey)
	s.OpenChat("")

	s.mu.Lock()
	open := s.openKey
	s.mu.Unlock()
	if open != "" {
		t.Errorf("openKey = %q, want empty", open)
	}

	f := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.drop([]string{f})
	if n := len(em.all(eventDropIgnored)); n != 1 {
		t.Errorf("drop-ignored events = %d, want 1: a drop must not go to the chat that was open", n)
	}

	text := incoming(history.KindText)
	text.Text = "after the reload"
	s.app.cfg.OnEvent(node.Event{Entry: &text})
	barrier(t, s, em)
	if got := s.Chats(); len(got) != 1 || got[0].Unread != 1 {
		t.Errorf("Chats = %+v, want one unread", got)
	}
	if got := rec.all(); len(got) != 1 {
		t.Errorf("notifications for a focused window with nothing open = %+v, want one", got)
	}
}
