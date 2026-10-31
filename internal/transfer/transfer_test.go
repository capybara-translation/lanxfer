package transfer

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/server"
	"github.com/capybara-translation/lanxfer/internal/target"
)

const (
	idMac2   = "11111111-1111-4111-8111-111111111111"
	idUbuntu = "33333333-3333-4333-8333-333333333333"
	peerKey  = "peer-key"
)

// receiver starts a real receive server and returns the peer that reaches it
// and the folder it saves into.
func receiver(t *testing.T) (presence.Peer, string) {
	t.Helper()
	p, dir, _ := trackedReceiver(t)
	return p, dir
}

// trackedReceiver is receiver that also counts the connections it holds open.
func trackedReceiver(t *testing.T) (presence.Peer, string, *atomic.Int64) {
	t.Helper()
	st, err := server.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Int64
	ts := httptest.NewUnstartedServer(server.New(st, 64<<20, server.NewInbox(nil, io.Discard), log.New(io.Discard, "", 0)).Handler())
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	ts.Start()
	t.Cleanup(ts.Close)
	addr, err := netip.ParseAddrPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return presence.Peer{ID: idMac2, Name: "mac2", Addr: addr, Online: true}, st.Dir, &open
}

// harness collects what a Manager reports.
type harness struct {
	mu       sync.Mutex
	updates  []Update
	entries  []history.Entry
	unrec    []history.Entry
	events   []string // "record" and "done", in order
	recordFn func(history.Entry) error
	locates  atomic.Int32
	locateFn func(ctx context.Context, p presence.Peer, port int) (target.Target, error)

	byID   map[string]chan struct{} // closed when the item's Done update arrives
	doneMu sync.Mutex
}

func newHarness() *harness { return &harness{byID: map[string]chan struct{}{}} }

func (h *harness) doneChan(id string) chan struct{} {
	h.doneMu.Lock()
	defer h.doneMu.Unlock()
	if h.byID[id] == nil {
		h.byID[id] = make(chan struct{})
	}
	return h.byID[id]
}

func (h *harness) config() Config {
	return Config{
		From: "me",
		Port: 8425,
		Locate: func(ctx context.Context, p presence.Peer, port int) (target.Target, error) {
			h.locates.Add(1)
			if h.locateFn != nil {
				return h.locateFn(ctx, p, port)
			}
			return target.Finder{}.Locate(ctx, p, port)
		},
		Record: func(e history.Entry) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.events = append(h.events, "record")
			if h.recordFn != nil {
				if err := h.recordFn(e); err != nil {
					return err
				}
			}
			h.entries = append(h.entries, e)
			return nil
		},
		OnUnrecorded: func(key string, e history.Entry) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if key != peerKey {
				panic("unexpected peer key " + key)
			}
			h.unrec = append(h.unrec, e)
		},
		OnUpdate: func(key string, u Update) {
			if key != peerKey {
				panic("unexpected peer key " + key)
			}
			h.mu.Lock()
			h.updates = append(h.updates, u)
			if u.Done {
				h.events = append(h.events, "done")
			}
			h.mu.Unlock()
			if u.Done {
				close(h.doneChan(u.ID))
			}
		},
	}
}

func (h *harness) wait(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		select {
		case <-h.doneChan(id):
		case <-time.After(10 * time.Second):
			t.Fatalf("item %s did not finish", id)
		}
	}
}

func (h *harness) updatesFor(id string) []Update {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Update
	for _, u := range h.updates {
		if u.ID == id {
			out = append(out, u)
		}
	}
	return out
}

func (h *harness) last(id string) Update {
	us := h.updatesFor(id)
	return us[len(us)-1]
}

func (h *harness) recorded() []history.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]history.Entry(nil), h.entries...)
}

func newManager(t *testing.T, h *harness) *Manager {
	t.Helper()
	m := New(h.config())
	t.Cleanup(m.Close)
	return m
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// steppedClock returns a clock that moves forward by step on every call.
func steppedClock(step time.Duration) (func() time.Time, *atomic.Int64) {
	var calls atomic.Int64
	base := time.Unix(1_700_000_000, 0)
	return func() time.Time {
		n := calls.Add(1)
		return base.Add(time.Duration(n) * step)
	}, &calls
}

func TestSendPathsFileAndFolder(t *testing.T) {
	p, dir := receiver(t)
	src := t.TempDir()
	file := filepath.Join(src, "a.txt")
	writeFile(t, file, 1000)
	folder := filepath.Join(src, "trip")
	writeFile(t, filepath.Join(folder, "one.txt"), 300)
	writeFile(t, filepath.Join(folder, "sub", "two.txt"), 200)
	writeFile(t, filepath.Join(folder, ".DS_Store"), 10)

	h := newHarness()
	m := newManager(t, h)
	ids := m.SendPaths(peerKey, p, []string{file, folder})
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("ids = %v", ids)
	}
	h.wait(t, ids...)

	// A queued card appears for each path before anything is sent.
	for i, id := range ids {
		first := h.updatesFor(id)[0]
		want := Update{ID: id, Name: filepath.Base([]string{file, folder}[i])}
		if first != want {
			t.Errorf("first update of %s = %+v, want %+v", id, first, want)
		}
	}

	fileDone := h.last(ids[0])
	if !fileDone.Done || fileDone.Status != history.StatusOK || fileDone.Sent != 1000 || fileDone.Total != 1000 || fileDone.Folder {
		t.Errorf("file done = %+v", fileDone)
	}
	folderDone := h.last(ids[1])
	if !folderDone.Done || folderDone.Status != history.StatusOK || !folderDone.Folder ||
		folderDone.Total != 500 || folderDone.Sent != 500 || folderDone.Files != 2 || folderDone.Skipped != 1 || folderDone.Name != "trip" {
		t.Errorf("folder done = %+v", folderDone)
	}
	for _, u := range h.updatesFor(ids[1]) {
		if u.Sent > u.Total {
			t.Errorf("update %+v has Sent above Total", u)
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || len(got) != 1000 {
		t.Errorf("received file: %d bytes, %v", len(got), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trip", "sub", "two.txt")); err != nil {
		t.Errorf("folder not received: %v", err)
	}

	entries := h.recorded()
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if e := entries[0]; e.Kind != history.KindFile || e.Path != file || e.Name != "a.txt" || e.Size != 1000 ||
		e.Status != history.StatusOK || e.PeerID != idMac2 || e.PeerName != "mac2" || e.PeerAddr != p.Addr.Addr().String() || e.Dir != history.Out {
		t.Errorf("file entry = %+v", e)
	}
	if e := entries[1]; e.Kind != history.KindFolder || e.Path != folder || e.Name != "trip" || e.Size != 500 || e.Files != 2 || e.Status != history.StatusOK {
		t.Errorf("folder entry = %+v", e)
	}
}

func TestSendPathsRecordsAbsolutePath(t *testing.T) {
	p, _ := receiver(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rel.txt"), 5)
	t.Chdir(dir)

	h := newHarness()
	m := newManager(t, h)
	ids := m.SendPaths(peerKey, p, []string{"rel.txt"})
	h.wait(t, ids...)

	if e := h.recorded()[0]; !filepath.IsAbs(e.Path) || filepath.Base(e.Path) != "rel.txt" {
		t.Errorf("Path = %q, want an absolute path", e.Path)
	}
}

func TestSendPathsContinuesAfterFailure(t *testing.T) {
	p, dir := receiver(t)
	src := t.TempDir()
	a, c := filepath.Join(src, "a.txt"), filepath.Join(src, "c.txt")
	missing := filepath.Join(src, "missing.txt")
	writeFile(t, a, 10)
	writeFile(t, c, 20)

	h := newHarness()
	m := newManager(t, h)
	ids := m.SendPaths(peerKey, p, []string{a, missing, c})
	h.wait(t, ids...)

	want := []string{history.StatusOK, history.StatusFailed, history.StatusOK}
	for i, id := range ids {
		if got := h.last(id).Status; got != want[i] {
			t.Errorf("item %d status = %q, want %q", i, got, want[i])
		}
	}
	if u := h.last(ids[1]); u.Error == "" || u.Name != "missing.txt" {
		t.Errorf("failed update = %+v", u)
	}
	entries := h.recorded()
	if len(entries) != 3 || entries[1].Status != history.StatusFailed || entries[1].Path != missing || entries[1].Error == "" {
		t.Errorf("entries = %+v", entries)
	}
	for _, name := range []string{"a.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not received: %v", name, err)
		}
	}
}

func TestProgressThrottled(t *testing.T) {
	p, _ := receiver(t)
	file := filepath.Join(t.TempDir(), "big.bin")
	writeFile(t, file, 2<<20)

	h := newHarness()
	m := newManager(t, h)
	// Each clock reading is 30 ms after the previous one, so about one in
	// four progress calls may produce an update.
	now, calls := steppedClock(30 * time.Millisecond)
	m.now = now
	ids := m.SendPaths(peerKey, p, []string{file})
	h.wait(t, ids...)

	progress := 0
	for _, u := range h.updatesFor(ids[0]) {
		if !u.Done && u.Sent > 0 {
			progress++
		}
	}
	n := int(calls.Load())
	if n < 16 {
		t.Fatalf("only %d progress calls; the file is too small to test throttling", n)
	}
	if progress == 0 || progress > n/3 {
		t.Errorf("%d progress updates from %d clock readings; want some, at most %d", progress, n, n/3)
	}
}

func TestProgressNeverAfterDone(t *testing.T) {
	m := New(Config{})
	t.Cleanup(m.Close)
	var got []Update
	m.cfg.OnUpdate = func(_ string, u Update) { got = append(got, u) }
	it := &item{base: Update{ID: "t1", Total: 100}}

	m.progress(peerKey, it, 10) // first reading is emitted
	m.finish(peerKey, it, history.Entry{Status: history.StatusOK})
	m.progress(peerKey, it, 50)
	m.finish(peerKey, it, history.Entry{Status: history.StatusFailed})

	if len(got) != 2 || got[0].Done || !got[1].Done || got[1].Status != history.StatusOK {
		t.Errorf("updates = %+v", got)
	}
}

func TestScaleProgress(t *testing.T) {
	tests := []struct{ sent, tar, content, want int64 }{
		{0, 1000, 400, 0},
		{500, 1000, 400, 200},
		{1000, 1000, 400, 400},
		{1500, 1000, 400, 400}, // never above the content size
		{-5, 1000, 400, 0},
		{10, 0, 0, 0},
		{5, 1024, 0, 0},
		// Products near 2^63 must not overflow.
		{6_000_000_000_000, 8_000_000_000_000, 4_000_000_000_000, 3_000_000_000_000},
		{9_000_000_000_000_000_000, 9_200_000_000_000_000_000, 9_100_000_000_000_000_000, 8_902_173_913_043_478_260},
	}
	for _, tt := range tests {
		if got := scaleProgress(tt.sent, tt.tar, tt.content); got != tt.want {
			t.Errorf("scaleProgress(%d, %d, %d) = %d, want %d", tt.sent, tt.tar, tt.content, got, tt.want)
		}
	}
}

// bigFile writes a file big enough that a send cannot finish before the
// first progress update is handled.
func bigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "big.bin")
	writeFile(t, path, 8<<20)
	return path
}

func TestCancelRunning(t *testing.T) {
	p, _ := receiver(t)
	file := bigFile(t)

	h := newHarness()
	cfg := h.config()
	var m *Manager
	var once sync.Once
	inner := cfg.OnUpdate
	cfg.OnUpdate = func(key string, u Update) {
		inner(key, u)
		if !u.Done && u.Sent > 0 {
			once.Do(func() {
				if !m.Cancel(u.ID) {
					t.Error("Cancel of a running item returned false")
				}
			})
		}
	}
	m = New(cfg)
	t.Cleanup(m.Close)
	now, _ := steppedClock(time.Second) // every progress call emits
	m.now = now

	ids := m.SendPaths(peerKey, p, []string{file})
	h.wait(t, ids...)

	if u := h.last(ids[0]); u.Status != history.StatusCancelled {
		t.Errorf("done = %+v, want cancelled", u)
	}
	if e := h.recorded()[0]; e.Status != history.StatusCancelled {
		t.Errorf("entry = %+v, want cancelled", e)
	}
	if m.Cancel(ids[0]) {
		t.Error("Cancel of a finished item returned true")
	}
	if m.Cancel("nope") {
		t.Error("Cancel of an unknown ID returned true")
	}
}

func TestCancelQueued(t *testing.T) {
	p, dir := receiver(t)
	src := t.TempDir()
	a, b := filepath.Join(src, "a.txt"), filepath.Join(src, "b.txt")
	writeFile(t, a, 10)
	writeFile(t, b, 10)

	h := newHarness()
	release := make(chan struct{})
	h.locateFn = func(ctx context.Context, p presence.Peer, port int) (target.Target, error) {
		<-release // holds the first item in the lookup
		return target.Target{Addr: p.Addr, ID: p.ID, Name: p.Name}, nil
	}
	m := newManager(t, h)
	// Registered after the Manager's Close, so it runs first and a failed
	// assertion cannot leave Close waiting on the lookup.
	var releaseOnce sync.Once
	releaseLookup := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLookup)
	ids := m.SendPaths(peerKey, p, []string{a, b})

	if !m.Cancel(ids[1]) {
		t.Fatal("Cancel of a queued item returned false")
	}
	releaseLookup()
	h.wait(t, ids...)

	if got := h.last(ids[0]).Status; got != history.StatusOK {
		t.Errorf("first status = %q", got)
	}
	if u := h.last(ids[1]); u.Status != history.StatusCancelled || !u.Done {
		t.Errorf("queued item done = %+v", u)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Errorf("cancelled item was sent (stat err = %v)", err)
	}
	entries := h.recorded()
	if len(entries) != 2 || entries[1].Status != history.StatusCancelled || entries[1].Path != b || entries[1].Name != "b.txt" ||
		entries[1].PeerID != idMac2 || entries[1].PeerName != "mac2" || entries[1].Kind != history.KindFile {
		t.Errorf("entries = %+v", entries)
	}
	m.mu.Lock()
	left := len(m.items)
	m.mu.Unlock()
	if left != 0 {
		t.Errorf("%d finished items still tracked", left)
	}
}

func TestCloseCancelsRunningTransfers(t *testing.T) {
	p, _ := receiver(t)
	src := t.TempDir()
	big := bigFile(t)
	small := filepath.Join(src, "small.txt")
	writeFile(t, small, 10)

	h := newHarness()
	cfg := h.config()
	var m *Manager
	var once sync.Once
	closed := make(chan struct{})
	inner := cfg.OnUpdate
	cfg.OnUpdate = func(key string, u Update) {
		inner(key, u)
		if !u.Done && u.Sent > 0 {
			once.Do(func() {
				go func() { m.Close(); close(closed) }()
			})
		}
	}
	m = New(cfg)
	t.Cleanup(m.Close)
	now, _ := steppedClock(time.Second)
	m.now = now

	ids := m.SendPaths(peerKey, p, []string{big, small})
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2 seconds")
	}

	entries := h.recorded()
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	for i, e := range entries {
		if e.Status != history.StatusCancelled {
			t.Errorf("entry %d = %+v, want cancelled", i, e)
		}
	}
	for _, id := range ids {
		if u := h.last(id); !u.Done || u.Status != history.StatusCancelled {
			t.Errorf("last update of %s = %+v", id, u)
		}
	}

	// After Close nothing is queued and nothing is recorded.
	m.Close()
	if got := m.SendPaths(peerKey, p, []string{small}); got != nil {
		t.Errorf("SendPaths after Close = %v, want nil", got)
	}
	if err := m.SendText(peerKey, p, "hi"); err == nil {
		t.Error("SendText after Close succeeded")
	}
	if n := len(h.recorded()); n != 2 {
		t.Errorf("%d entries after Close, want 2", n)
	}
}

func TestCloseCancelsTextInProgress(t *testing.T) {
	p, _ := receiver(t)
	h := newHarness()
	entered := make(chan struct{})
	h.locateFn = func(ctx context.Context, _ presence.Peer, _ int) (target.Target, error) {
		close(entered)
		<-ctx.Done()
		return target.Target{}, &target.OfflineError{Name: "mac2"}
	}
	m := newManager(t, h)
	errc := make(chan error, 1)
	go func() { errc <- m.SendText(peerKey, p, "hello") }()
	<-entered

	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2 seconds")
	}
	if err := <-errc; err == nil {
		t.Error("SendText returned nil after Close cancelled it")
	}
	if e := h.recorded(); len(e) != 1 || e[0].Status != history.StatusCancelled || e[0].Kind != history.KindText {
		t.Errorf("entries = %+v", e)
	}
}

func TestSendTextRecords(t *testing.T) {
	p, _ := receiver(t)
	h := newHarness()
	m := newManager(t, h)

	if err := m.SendText(peerKey, p, "hello there"); err != nil {
		t.Fatal(err)
	}
	e := h.recorded()
	want := history.Entry{
		Dir: history.Out, PeerID: idMac2, PeerName: "mac2", PeerAddr: p.Addr.Addr().String(),
		Kind: history.KindText, Text: "hello there", Status: history.StatusOK,
	}
	if len(e) != 1 || e[0] != want {
		t.Errorf("entries = %+v, want %+v", e, want)
	}
	if len(h.updates) != 0 {
		t.Errorf("text sends must not emit updates, got %+v", h.updates)
	}
}

func TestSendTextFailureReturnsAndRecords(t *testing.T) {
	p, _ := receiver(t)
	p.Addr = netip.MustParseAddrPort("127.0.0.1:1") // nothing listens here
	h := newHarness()
	m := newManager(t, h)

	err := m.SendText(peerKey, p, "hello")
	if err == nil {
		t.Fatal("SendText succeeded without a receiver")
	}
	e := h.recorded()
	if len(e) != 1 || e[0].Status != history.StatusFailed || e[0].Error != err.Error() {
		t.Errorf("entries = %+v, err = %v", e, err)
	}
}

func TestSendTextTimesOut(t *testing.T) {
	// A receiver that accepts the request and never answers.
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); ts.Close() })
	addr, err := netip.ParseAddrPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p := presence.Peer{ID: idMac2, Name: "mac2", Addr: addr, Online: true}

	h := newHarness()
	cfg := h.config()
	cfg.TextTimeout = 200 * time.Millisecond
	m := New(cfg)
	t.Cleanup(m.Close)

	start := time.Now()
	err = m.SendText(peerKey, p, "hello")
	if err == nil {
		t.Fatal("SendText succeeded against a receiver that never answers")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("SendText took %s", d)
	}
	if !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("err = %v", err)
	}
	// A timeout is a failure, not a cancellation.
	e := h.recorded()
	if len(e) != 1 || e[0].Status != history.StatusFailed || e[0].Error != err.Error() {
		t.Errorf("entries = %+v", e)
	}
}

func TestLocateFailureRecordsFailed(t *testing.T) {
	src := t.TempDir()
	a, b := filepath.Join(src, "a.txt"), filepath.Join(src, "b")
	writeFile(t, a, 10)
	writeFile(t, filepath.Join(b, "f.txt"), 10)
	p := presence.Peer{ID: idUbuntu, Name: "ubuntu", Addr: netip.MustParseAddrPort("192.168.68.59:9000")}

	h := newHarness()
	h.locateFn = func(context.Context, presence.Peer, int) (target.Target, error) {
		return target.Target{}, &target.OfflineError{Name: "ubuntu", LastSeen: time.Unix(0, 0)}
	}
	m := newManager(t, h)
	ids := m.SendPaths(peerKey, p, []string{a, b})
	h.wait(t, ids...)

	for _, id := range ids {
		u := h.last(id)
		if u.Status != history.StatusFailed || !strings.Contains(u.Error, "ubuntu is offline") {
			t.Errorf("update = %+v", u)
		}
	}
	entries := h.recorded()
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	for _, e := range entries {
		if e.Status != history.StatusFailed || e.PeerID != idUbuntu || e.PeerName != "ubuntu" || e.PeerAddr != "192.168.68.59" || e.Error == "" {
			t.Errorf("entry = %+v", e)
		}
	}
	if entries[1].Kind != history.KindFolder {
		t.Errorf("folder entry kind = %q", entries[1].Kind)
	}
	if n := h.locates.Load(); n != 1 {
		t.Errorf("Locate called %d times, want once for the whole batch", n)
	}
}

func TestLocateCancelledByItemIsRetried(t *testing.T) {
	p, dir := receiver(t)
	src := t.TempDir()
	a, b := filepath.Join(src, "a.txt"), filepath.Join(src, "b.txt")
	writeFile(t, a, 10)
	writeFile(t, b, 10)

	h := newHarness()
	var m *Manager
	entered := make(chan string, 1)
	h.locateFn = func(ctx context.Context, p presence.Peer, port int) (target.Target, error) {
		if h.locates.Load() == 1 {
			entered <- "first"
			<-ctx.Done()
			return target.Target{}, ctx.Err()
		}
		return target.Target{Addr: p.Addr, ID: p.ID, Name: p.Name}, nil
	}
	m = newManager(t, h)
	ids := m.SendPaths(peerKey, p, []string{a, b})
	<-entered
	m.Cancel(ids[0])
	h.wait(t, ids...)

	if got := h.last(ids[0]).Status; got != history.StatusCancelled {
		t.Errorf("first status = %q", got)
	}
	if got := h.last(ids[1]).Status; got != history.StatusOK {
		t.Errorf("second status = %q, want ok (the lookup is retried)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Errorf("b.txt not received: %v", err)
	}
}

func TestRecordFailureDoesNotFailSend(t *testing.T) {
	p, dir := receiver(t)
	file := filepath.Join(t.TempDir(), "a.txt")
	writeFile(t, file, 10)

	h := newHarness()
	h.recordFn = func(history.Entry) error { return errors.New("disk full") }
	m := newManager(t, h)
	ids := m.SendPaths(peerKey, p, []string{file})
	h.wait(t, ids...)

	if u := h.last(ids[0]); u.Status != history.StatusOK || u.Error != "" {
		t.Errorf("done = %+v, want ok", u)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Errorf("file not received: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.unrec) != 1 || h.unrec[0].Path != file || h.unrec[0].Status != history.StatusOK {
		t.Errorf("unrecorded = %+v", h.unrec)
	}
}

func TestSendTextRecordFailureStillReturnsSendResult(t *testing.T) {
	p, _ := receiver(t)
	h := newHarness()
	h.recordFn = func(history.Entry) error { return errors.New("disk full") }
	m := newManager(t, h)

	if err := m.SendText(peerKey, p, "hi"); err != nil {
		t.Errorf("SendText = %v, want nil", err)
	}
	if len(h.unrec) != 1 || h.unrec[0].Text != "hi" {
		t.Errorf("unrecorded = %+v", h.unrec)
	}
}

func TestRecordComesBeforeDone(t *testing.T) {
	p, _ := receiver(t)
	file := filepath.Join(t.TempDir(), "a.txt")
	writeFile(t, file, 10)

	h := newHarness()
	m := newManager(t, h)
	h.wait(t, m.SendPaths(peerKey, p, []string{file})...)

	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.Join(h.events, ",") != "record,done" {
		t.Errorf("events = %v", h.events)
	}
}

func TestOnUpdateMayCallManager(t *testing.T) {
	p, _ := receiver(t)
	file := filepath.Join(t.TempDir(), "a.txt")
	writeFile(t, file, 10)

	h := newHarness()
	cfg := h.config()
	var m *Manager
	inner := cfg.OnUpdate
	cfg.OnUpdate = func(key string, u Update) {
		m.Cancel(u.ID)
		m.SendPaths(key, p, nil)
		inner(key, u)
	}
	m = New(cfg)
	t.Cleanup(m.Close)

	ids := m.SendPaths(peerKey, p, []string{file})
	h.wait(t, ids...) // a deadlock would time out here
}

func TestEntry(t *testing.T) {
	tg := target.Target{Addr: netip.MustParseAddrPort("192.168.68.54:8425"), ID: idMac2, Name: "mac2"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("file ok", func(t *testing.T) {
		res := &client.Result{RemoteName: "a (1).txt", PeerID: idUbuntu}
		e := Entry(context.Background(), tg, "/src/a.txt", "a.txt", 12, nil, res, nil)
		want := history.Entry{
			Dir: history.Out, PeerID: idUbuntu, PeerName: "mac2", PeerAddr: "192.168.68.54",
			Kind: history.KindFile, Name: "a.txt", SavedAs: "a (1).txt", Path: "/src/a.txt",
			Size: 12, Status: history.StatusOK,
		}
		if e != want {
			t.Errorf("got  %+v\nwant %+v", e, want)
		}
	})
	t.Run("response without ID uses the resolved one", func(t *testing.T) {
		res := &client.Result{RemoteName: "a.txt"}
		e := Entry(context.Background(), tg, "/src/a.txt", "a.txt", 12, nil, res, nil)
		if e.PeerID != idMac2 {
			t.Errorf("PeerID = %q, want %q", e.PeerID, idMac2)
		}
	})
	t.Run("folder cancelled", func(t *testing.T) {
		plan := &client.DirPlan{Name: "trip", Files: 3, ContentSize: 300}
		e := Entry(cancelled, tg, "/src/trip", "trip", 0, plan, nil, errors.New("context canceled"))
		want := history.Entry{
			Dir: history.Out, PeerID: idMac2, PeerName: "mac2", PeerAddr: "192.168.68.54",
			Kind: history.KindFolder, Name: "trip", Path: "/src/trip", Size: 300, Files: 3,
			Status: history.StatusCancelled, Error: "context canceled",
		}
		if e != want {
			t.Errorf("got  %+v\nwant %+v", e, want)
		}
	})
	t.Run("failed", func(t *testing.T) {
		e := Entry(context.Background(), tg, "/src/a.txt", "a.txt", 12, nil, nil, errors.New("connection refused"))
		if e.Status != history.StatusFailed || e.Error != "connection refused" || e.SavedAs != "" {
			t.Errorf("got %+v", e)
		}
	})
	t.Run("text", func(t *testing.T) {
		e := TextEntry(context.Background(), tg, "hi", &client.Result{PeerID: idUbuntu}, nil)
		want := history.Entry{
			Dir: history.Out, PeerID: idUbuntu, PeerName: "mac2", PeerAddr: "192.168.68.54",
			Kind: history.KindText, Text: "hi", Status: history.StatusOK,
		}
		if e != want {
			t.Errorf("got  %+v\nwant %+v", e, want)
		}
	})
}

func TestSendsLeaveNoConnectionsOrGoroutines(t *testing.T) {
	p, _, open := trackedReceiver(t)
	file := filepath.Join(t.TempDir(), "a.txt")
	writeFile(t, file, 100)

	runtime.GC()
	before := runtime.NumGoroutine()

	h := newHarness()
	m := New(h.config())
	const n = 30
	for i := 0; i < n; i++ {
		h.wait(t, m.SendPaths(peerKey, p, []string{file})...)
		if err := m.SendText(peerKey, p, "hi"); err != nil {
			t.Fatal(err)
		}
	}
	m.Close()

	// Connections and their goroutines wind down shortly after the last
	// response; wait for that instead of sleeping a fixed time.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (open.Load() > 0 || runtime.NumGoroutine() > before+5) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := open.Load(); got != 0 {
		t.Errorf("%d connections still open after %d sends", got, 2*n)
	}
	if got := runtime.NumGoroutine(); got > before+5 {
		t.Errorf("%d goroutines, started with %d", got, before)
	}
}

func TestFolderCardShowsDuringScanAndAfterScanFailure(t *testing.T) {
	p, _ := receiver(t)
	// A path deeper than the receiver accepts makes the scan fail.
	deep := filepath.Join(t.TempDir(), "deep")
	cur := deep
	for i := 0; i < 70; i++ {
		cur = filepath.Join(cur, "d")
	}
	writeFile(t, filepath.Join(cur, "f.txt"), 1)

	h := newHarness()
	m := newManager(t, h)
	ids := m.SendPaths(peerKey, p, []string{deep})
	h.wait(t, ids...)

	us := h.updatesFor(ids[0])
	if len(us) < 3 || !us[1].Folder || us[1].Done {
		t.Errorf("no folder card before the scan result: %+v", us)
	}
	if u := us[len(us)-1]; !u.Done || u.Status != history.StatusFailed || !u.Folder {
		t.Errorf("done = %+v, want a failed folder", u)
	}
	if e := h.recorded()[0]; e.Kind != history.KindFolder || e.Status != history.StatusFailed {
		t.Errorf("entry = %+v", e)
	}
}

func TestPeerWithoutAddressLeavesPeerAddrEmpty(t *testing.T) {
	h := newHarness()
	h.locateFn = func(context.Context, presence.Peer, int) (target.Target, error) {
		return target.Target{}, errors.New("no address")
	}
	m := newManager(t, h)
	p := presence.Peer{ID: idUbuntu, Name: "ubuntu"} // zero Addr

	if err := m.SendText(peerKey, p, "hi"); err == nil {
		t.Fatal("SendText succeeded")
	}
	file := filepath.Join(t.TempDir(), "a.txt")
	writeFile(t, file, 1)
	h.wait(t, m.SendPaths(peerKey, p, []string{file})...)

	entries := h.recorded()
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	for _, e := range entries {
		if e.PeerAddr != "" || e.PeerName != "ubuntu" {
			t.Errorf("entry = %+v, want an empty PeerAddr", e)
		}
	}
}

func TestIDsAreUniqueAcrossManagers(t *testing.T) {
	p, _ := receiver(t)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		h := newHarness()
		m := New(h.config())
		ids := m.SendPaths(peerKey, p, []string{"/nonexistent/x"})
		h.wait(t, ids...)
		m.Close()
		if seen[ids[0]] {
			t.Fatalf("ID %s repeated", ids[0])
		}
		seen[ids[0]] = true
		time.Sleep(time.Millisecond) // the prefix is the start time
	}
}
