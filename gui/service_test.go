package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/node"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/target"
	"github.com/capybara-translation/lanxfer/internal/transfer"
)

// emitted is one call of fakeEmitter.
type emitted struct {
	name string
	data any
}

// fakeEmitter records the events a Service emits.
type fakeEmitter struct {
	mu   sync.Mutex
	list []emitted
	// peerEvents counts the peer list events the node has sent. A starting
	// node reports its saved list, which would replace peers a test set.
	peerEvents atomic.Int32
}

func (f *fakeEmitter) Emit(name string, data any) {
	f.mu.Lock()
	f.list = append(f.list, emitted{name, data})
	f.mu.Unlock()
}

func (f *fakeEmitter) all(name string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []any
	for _, e := range f.list {
		if e.name == name {
			out = append(out, e.data)
		}
	}
	return out
}

func (f *fakeEmitter) messages() []MessageEvent {
	var out []MessageEvent
	for _, d := range f.all(eventMessage) {
		out = append(out, d.(MessageEvent))
	}
	return out
}

// barrier returns once everything queued before it was handled and emitted.
var barriers atomic.Int64

func barrier(t *testing.T, s *Service, f *fakeEmitter) {
	t.Helper()
	mark := fmt.Sprintf("barrier %d", barriers.Add(1))
	s.queue.push(work{kind: workState, state: NodeState{Error: mark}})
	waitFor(t, "the event queue to drain", func() bool {
		for _, d := range f.all(eventState) {
			if d.(NodeState).Error == mark {
				return true
			}
		}
		return false
	})
}

// waitNodeSettled waits until the node has sent its n-th peer list and the
// service has handled it.
func waitNodeSettled(t *testing.T, s *Service, em *fakeEmitter, n int32) {
	t.Helper()
	waitFor(t, "the node's peer list", func() bool { return em.peerEvents.Load() >= n })
	barrier(t, s, em)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// testPeer is a peer that is in the list but cannot be reached.
var testPeer = presence.Peer{
	ID:       "0123456789abcdef0123456789abcdef",
	Name:     "other",
	Addr:     netip.MustParseAddrPort("192.0.2.7:8425"),
	LastSeen: time.Now(),
	Online:   true,
}

const testPeerKey = "id:0123456789abcdef0123456789abcdef"

type svcOpts struct {
	port      int
	noConfig  bool // run without a config folder
	breakHist bool // make the history file unwritable
	readHist  func(string) ([]history.Entry, error)
	skipStart bool
}

// newTestService returns a Service whose sends fail at the lookup, so no
// network is needed, and which keeps everything in a temp folder.
func newTestService(t *testing.T, o svcOpts) (*Service, *fakeEmitter, string) {
	t.Helper()
	fakeHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config")
	if o.port == 0 {
		o.port = freePort(t)
	}
	st := Settings{Name: "test", SaveDir: filepath.Join(dir, "save")}
	if err := st.Save(filepath.Join(cfgDir, settingsFileName)); err != nil {
		t.Fatal(err)
	}
	if o.breakHist {
		// A directory where the file should be: reading and appending fail.
		if err := os.Mkdir(filepath.Join(cfgDir, historyFileName), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	em := &fakeEmitter{}
	cfg := serviceConfig{
		ConfigDir: cfgDir,
		Port:      o.port,
		Emitter:   em,
		Locate: func(context.Context, presence.Peer, int) (target.Target, error) {
			return target.Target{}, errors.New("unreachable in tests")
		},
		ReadHistory: o.readHist,
	}
	if o.noConfig {
		cfg.ConfigDir = ""
	}
	s := newService(cfg)
	t.Cleanup(s.quit)
	orig := s.app.cfg.OnEvent
	s.app.cfg.OnEvent = func(e node.Event) {
		orig(e)
		if e.Entry == nil {
			em.peerEvents.Add(1)
		}
	}
	if !o.skipStart {
		s.start()
		<-s.ready
		if s.State().Running {
			waitNodeSettled(t, s, em, 1)
		}
	}
	return s, em, cfgDir
}

func historyLines(t *testing.T, cfgDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfgDir, historyFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		t.Fatal(err)
	}
	return bytes.Count(data, []byte("\n"))
}

// busyPort returns a port that is taken, so the receiver cannot start.
func busyPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestRecordWithoutNode(t *testing.T) {
	s, em, cfgDir := newTestService(t, svcOpts{port: busyPort(t)})
	if s.State().Running {
		t.Fatal("the receiver runs on a busy port")
	}
	s.store.SetPeers([]presence.Peer{testPeer})

	if err := s.SendText(testPeerKey, "hello"); err == nil {
		t.Fatal("SendText succeeded without a reachable peer")
	}
	barrier(t, s, em)

	// The entry was written to the history file directly...
	entries, err := history.ReadAll(filepath.Join(cfgDir, historyFileName))
	if err != nil || len(entries) != 1 {
		t.Fatalf("history = %d entries, %v; want 1", len(entries), err)
	}
	if e := entries[0]; e.Dir != history.Out || e.Text != "hello" || e.Status != history.StatusFailed || e.PeerID != testPeer.ID {
		t.Fatalf("history entry = %+v", e)
	}
	// ...and shown in the chat.
	if msgs := em.messages(); len(msgs) != 1 || msgs[0].Key != testPeerKey || msgs[0].Entry.Text != "hello" {
		t.Fatalf("message events = %+v", msgs)
	}
	page := s.Messages(testPeerKey, 0)
	if len(page.Messages) != 1 || page.Next != -1 {
		t.Fatalf("Messages = %+v", page)
	}
}

func TestOneChatEntryPerSend(t *testing.T) {
	cases := []struct {
		name string
		opts func(t *testing.T) svcOpts
		// wantFile is the number of lines the history file should have, or
		// -1 when it cannot hold any.
		wantFile int
	}{
		{"node running", func(*testing.T) svcOpts { return svcOpts{} }, 1},
		{"no node", func(t *testing.T) svcOpts { return svcOpts{port: busyPort(t)} }, 1},
		{"record fails, node running", func(*testing.T) svcOpts { return svcOpts{breakHist: true} }, -1},
		{"record fails, no node", func(t *testing.T) svcOpts { return svcOpts{port: busyPort(t), breakHist: true} }, -1},
		{"no config folder", func(t *testing.T) svcOpts { return svcOpts{noConfig: true} }, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, em, cfgDir := newTestService(t, tc.opts(t))
			s.store.SetPeers([]presence.Peer{testPeer})

			for i := 0; i < 3; i++ {
				_ = s.SendText(testPeerKey, "msg")
			}
			barrier(t, s, em)

			if n := len(em.messages()); n != 3 {
				t.Errorf("message events = %d, want 3 (one per send)", n)
			}
			if n := len(s.Messages(testPeerKey, 0).Messages); n != 3 {
				t.Errorf("chat has %d entries, want 3", n)
			}
			if tc.wantFile >= 0 {
				if n := historyLines(t, cfgDir); n != 3 {
					t.Errorf("history file has %d lines, want 3", n)
				}
			}
		})
	}
}

func TestSendToUnknownKeyFails(t *testing.T) {
	s, em, cfgDir := newTestService(t, svcOpts{})

	for _, key := range []string{"id:nobody", "name:ghost", "addr:192.0.2.1", ""} {
		if err := s.SendText(key, "hi"); err == nil {
			t.Errorf("SendText(%q) succeeded without a peer", key)
		}
	}
	if err := s.SendText(testPeerKey, ""); err == nil {
		t.Error("SendText of an empty message succeeded")
	}
	barrier(t, s, em)
	if n := len(em.messages()); n != 0 {
		t.Errorf("%d message events for sends that never started", n)
	}
	if n := historyLines(t, cfgDir); n != 0 {
		t.Errorf("history has %d lines for sends that never started", n)
	}

	// A drop needs an open chat, and the chat needs a peer.
	f := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.drop([]string{f})
	s.OpenChat("id:nobody")
	s.drop([]string{f})
	ignored := em.all(eventDropIgnored)
	if len(ignored) != 2 {
		t.Fatalf("drop-ignored events = %d, want 2", len(ignored))
	}
	for _, d := range ignored {
		if d.(DropIgnored).Reason == "" {
			t.Error("drop-ignored without a reason")
		}
	}
	barrier(t, s, em)
	if n := historyLines(t, cfgDir); n != 0 {
		t.Errorf("an ignored drop wrote %d history lines", n)
	}
}

func TestDropSendsToOpenChat(t *testing.T) {
	s, em, cfgDir := newTestService(t, svcOpts{})
	s.store.SetPeers([]presence.Peer{testPeer})
	s.OpenChat(testPeerKey)

	f := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.drop([]string{f})

	// The lookup fails, so the item ends as failed: one transfer event with
	// Done, and one chat entry.
	waitFor(t, "the dropped item to finish", func() bool {
		for _, d := range em.all(eventTransfer) {
			if te := d.(TransferEvent); te.Update.Done && te.Key == testPeerKey {
				return true
			}
		}
		return false
	})
	barrier(t, s, em)
	if n := len(em.messages()); n != 1 {
		t.Errorf("message events = %d, want 1", n)
	}
	if n := historyLines(t, cfgDir); n != 1 {
		t.Errorf("history lines = %d, want 1", n)
	}
	if n := len(em.all(eventDropIgnored)); n != 0 {
		t.Errorf("drop-ignored events = %d, want 0", n)
	}
}

func TestLoadBeforeNodeStart(t *testing.T) {
	port := freePort(t)
	release := make(chan struct{})
	entered := make(chan struct{})
	old := history.Entry{
		V: history.Version, Time: time.Now().Add(-time.Hour), Dir: history.In,
		PeerID: testPeer.ID, PeerName: "other", Kind: history.KindText, Text: "old", Status: history.StatusOK,
	}
	s, em, _ := newTestService(t, svcOpts{port: port, skipStart: true, readHist: func(string) ([]history.Entry, error) {
		close(entered)
		<-release
		return []history.Entry{old}, nil
	}})
	s.start()
	<-entered

	// The history is being read: nothing may have started the receiver.
	time.Sleep(100 * time.Millisecond)
	if answers(port) || s.State().Running {
		t.Fatal("the receiver started before the history was loaded")
	}
	if got := s.Chats(); len(got) != 0 {
		t.Fatalf("Chats before loading = %v, want none", got)
	}
	if n := len(em.all(eventChats)); n != 0 {
		t.Fatalf("chats events before loading = %d", n)
	}

	close(release)
	<-s.ready
	if !s.State().Running {
		t.Fatalf("the receiver did not start after loading: %+v", s.State())
	}
	barrier(t, s, em)
	chats := s.Chats()
	if len(chats) != 1 || chats[0].Preview != "old" {
		t.Fatalf("Chats after loading = %+v", chats)
	}
	waitFor(t, "a chats event after loading", func() bool { return len(em.all(eventChats)) > 0 })
	// The entry from the file is in the chat once, and was not reported as a
	// new message.
	if n := len(s.Messages(string(chats[0].Key), 0).Messages); n != 1 {
		t.Fatalf("chat has %d entries, want 1", n)
	}
	if n := len(em.messages()); n != 0 {
		t.Fatalf("%d message events for a loaded entry", n)
	}
}

func TestBrokenHistoryStillStarts(t *testing.T) {
	s, _, _ := newTestService(t, svcOpts{readHist: func(string) ([]history.Entry, error) {
		return nil, errors.New("disk on fire")
	}})
	if !s.State().Running {
		t.Fatalf("State = %+v: a broken history must not stop the receiver", s.State())
	}
	if s.Chats() == nil {
		t.Fatal("Chats returned nil")
	}
}

func TestNodeEventsReachChats(t *testing.T) {
	s, em, _ := newTestService(t, svcOpts{})
	in := history.Entry{
		V: history.Version, Time: time.Now(), Dir: history.In, PeerID: testPeer.ID,
		PeerName: "other", Kind: history.KindText, Text: "hi", Status: history.StatusOK,
	}
	// OnEvent must return at once, even when the consumer is busy; order is
	// kept: the peer list first, then the entry.
	s.app.cfg.OnEvent(node.Event{Peers: []presence.Peer{testPeer}})
	s.app.cfg.OnEvent(node.Event{Entry: &in})
	barrier(t, s, em)

	msgs := em.messages()
	if len(msgs) != 1 || msgs[0].Key != testPeerKey {
		t.Fatalf("message events = %+v", msgs)
	}
	sums := s.Chats()
	if len(sums) != 1 || sums[0].Name != "other" || sums[0].Unread != 1 {
		t.Fatalf("Chats = %+v", sums)
	}
	s.OpenChat(testPeerKey)
	if got := s.Chats(); got[0].Unread != 0 {
		t.Fatalf("unread after OpenChat = %d", got[0].Unread)
	}
}

func TestSaveSettingsRestarts(t *testing.T) {
	s, em, _ := newTestService(t, svcOpts{})
	s.store.SetPeers([]presence.Peer{testPeer})
	dir := t.TempDir()

	for _, bad := range []Settings{
		{Name: "", SaveDir: dir},
		{Name: "x", SaveDir: "relative/dir"},
	} {
		if err := s.SaveSettings(bad); err == nil {
			t.Errorf("SaveSettings(%+v) succeeded", bad)
		}
	}
	if err := s.SaveSettings(Settings{Name: "renamed", SaveDir: dir}); err != nil {
		t.Fatal(err)
	}
	if got := s.Settings(); got.Name != "renamed" || got.SaveDir != dir {
		t.Fatalf("Settings = %+v", got)
	}
	if !s.State().Running || s.app.SaveDir() != dir {
		t.Fatalf("after SaveSettings: %+v, save dir %q", s.State(), s.app.SaveDir())
	}
	// Sending still works through the new manager, with one entry per send.
	// The restarted receiver reports its saved list, which is empty here.
	waitNodeSettled(t, s, em, 2)
	s.store.SetPeers([]presence.Peer{testPeer})
	if err := s.SendText(testPeerKey, "again"); err == nil || errors.Is(err, transfer.ErrClosed) {
		t.Fatalf("SendText = %v, want the lookup failure", err)
	}
	barrier(t, s, em)
	if n := len(em.messages()); n != 1 {
		t.Fatalf("message events = %d, want 1", n)
	}
}

func TestQuitFreesThePort(t *testing.T) {
	port := freePort(t)
	s, _, _ := newTestService(t, svcOpts{port: port})
	if !answers(port) {
		t.Fatal("receiver does not answer")
	}
	done := make(chan struct{})
	go func() {
		s.quit()
		s.quit() // a second call is fine
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("quit hung")
	}
	if answers(port) {
		t.Fatal("receiver still answers after quit")
	}
	if err := s.SendText(testPeerKey, "x"); err == nil {
		t.Fatal("SendText succeeded after quit")
	}
	s.Retry() // must not restart the receiver
	if answers(port) {
		t.Fatal("Retry restarted the receiver after quit")
	}
}

func TestQuitBeforeStart(t *testing.T) {
	s, _, _ := newTestService(t, svcOpts{skipStart: true})
	done := make(chan struct{})
	go func() { s.quit(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("quit waited for a drain goroutine that never started")
	}
}

func TestSendWithoutNodeUsesSavedPeers(t *testing.T) {
	port := busyPort(t) // the receiver cannot start, so it never reports peers
	fakeHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]presence.Peer{testPeer})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "peers.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	em := &fakeEmitter{}
	s := newService(serviceConfig{
		ConfigDir: cfgDir,
		Port:      port,
		Emitter:   em,
		Locate: func(context.Context, presence.Peer, int) (target.Target, error) {
			return target.Target{}, errors.New("unreachable in tests")
		},
	})
	t.Cleanup(s.quit)
	s.start()
	<-s.ready
	if s.State().Running {
		t.Fatal("the receiver runs on a busy port")
	}
	barrier(t, s, em)

	// No store.SetPeers here: the peer came from the saved list.
	err = s.SendText(testPeerKey, "hello")
	if err == nil || !strings.Contains(err.Error(), "unreachable in tests") {
		t.Fatalf("SendText = %v, want the lookup failure, not a missing peer", err)
	}
	barrier(t, s, em)
	if n := len(em.messages()); n != 1 {
		t.Fatalf("message events = %d, want 1", n)
	}
	if n := historyLines(t, cfgDir); n != 1 {
		t.Fatalf("history lines = %d, want 1", n)
	}
}

func TestSaveSettingsRejectsUnusableFolder(t *testing.T) {
	s, _, _ := newTestService(t, svcOpts{})
	before := s.Settings()

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := []string{file, filepath.Join(file, "sub")}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		ro := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		bad = append(bad, ro, filepath.Join(ro, "sub"))
	}
	for _, dir := range bad {
		if err := s.SaveSettings(Settings{Name: "x", SaveDir: dir}); err == nil {
			t.Errorf("SaveSettings accepted the unusable folder %s", dir)
		}
	}
	if got := s.Settings(); got != before {
		t.Errorf("settings changed to %+v after refused saves, want %+v", got, before)
	}
	if !s.State().Running {
		t.Error("a refused save stopped the receiver")
	}

	// A folder that does not exist yet is created, and no probe is left.
	dir := filepath.Join(t.TempDir(), "new", "save")
	if err := s.SaveSettings(Settings{Name: "x", SaveDir: dir}); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".lanxfer-probe-*")); len(left) != 0 {
		t.Errorf("probe files left: %v", left)
	}
}

func TestStartingState(t *testing.T) {
	s, _, _ := newTestService(t, svcOpts{skipStart: true})
	if st := s.State(); !st.Starting || st.Running {
		t.Fatalf("before start: %+v", st)
	}
	s.start()
	<-s.ready
	if st := s.State(); st.Starting || !st.Running {
		t.Fatalf("after start: %+v", st)
	}

	busy, _, _ := newTestService(t, svcOpts{port: busyPort(t)})
	if st := busy.State(); st.Starting || st.Running || st.Error == "" {
		t.Fatalf("after a failed start: %+v", st)
	}
}

func TestSaveDirBeforeStart(t *testing.T) {
	s, _, _ := newTestService(t, svcOpts{skipStart: true})
	if got, want := s.saveDir(), s.Settings().SaveDir; got == "" || got != want {
		t.Fatalf("saveDir = %q, want the saved setting %q", got, want)
	}
}
