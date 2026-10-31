package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/server"
)

const (
	waitTimeout = 5 * time.Second
	testID      = "0123456789abcdef0123456789abcdef"
)

// syncBuffer is a bytes.Buffer safe for use as a log or output sink shared
// between the node's goroutines and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// freePort returns a port number that was free for both TCP and UDP when
// checked. Another process could still take it before the node binds it.
func freePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 20; i++ {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		l.Close()
		if err != nil {
			continue
		}
		u.Close()
		return port
	}
	t.Fatal("no port free for both TCP and UDP")
	return 0
}

// harness is a running node plus everything a test needs to observe it.
type harness struct {
	t         *testing.T
	cfg       Config
	events    chan Event
	logs      *syncBuffer
	out       *syncBuffer
	cancel    context.CancelFunc
	done      chan error
	stopped   bool
	saveDir   string
	configDir string
}

// startNode runs a node on a free loopback-reachable port with short timers.
// tweak, if not nil, may change the config before the node starts.
func startNode(t *testing.T, tweak func(*Config)) *harness {
	t.Helper()
	h := &harness{
		t:         t,
		events:    make(chan Event, 1024),
		logs:      &syncBuffer{},
		out:       &syncBuffer{},
		done:      make(chan error, 1),
		saveDir:   filepath.Join(t.TempDir(), "save"),
		configDir: filepath.Join(t.TempDir(), "config"),
	}
	h.cfg = Config{
		SaveDir:       h.saveDir,
		ConfigDir:     h.configDir,
		Port:          freePort(t),
		MaxSize:       1 << 20,
		Name:          "node-under-test",
		Logger:        log.New(h.logs, "", 0),
		Out:           h.out,
		AnnounceEvery: 50 * time.Millisecond,
		ProbeEvery:    50 * time.Millisecond,
		OnEvent: func(e Event) {
			select {
			case h.events <- e:
			default: // never block the node; the buffer is far larger than any test needs
			}
		},
	}
	if tweak != nil {
		tweak(&h.cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- Run(ctx, h.cfg) }()
	t.Cleanup(func() { h.stop() })
	h.waitListening()
	return h
}

// stop cancels the node and returns Run's result. It is safe to call twice.
func (h *harness) stop() error {
	h.t.Helper()
	if h.stopped {
		return nil
	}
	h.stopped = true
	h.cancel()
	select {
	case err := <-h.done:
		return err
	case <-time.After(waitTimeout):
		h.t.Error("Run did not return after cancel")
		return errors.New("timeout")
	}
}

func (h *harness) addr() string { return fmt.Sprintf("127.0.0.1:%d", h.cfg.Port) }

// deviceID returns the node's device ID as the node itself reports it in the
// X-Lanxfer-Id header. It must not be read with identity.LoadOrCreate: the
// listener is open before the node creates its ID, so a test that creates the
// ID too could end up with a different one.
func (h *harness) deviceID() string {
	h.t.Helper()
	resp, err := http.Get("http://" + h.addr() + "/local/peers")
	if err != nil {
		h.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	id := resp.Header.Get(server.IDHeader)
	if id == "" {
		h.t.Fatalf("no %s header in the response (status %d)", server.IDHeader, resp.StatusCode)
	}
	return id
}

func (h *harness) waitListening() {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-h.done:
			h.t.Fatalf("Run returned early: %v\nlogs:\n%s", err, h.logs.String())
		default:
		}
		c, err := net.DialTimeout("tcp", h.addr(), 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("node never started listening\nlogs:\n%s", h.logs.String())
}

// waitEvent returns the first event, from now on, for which match is true.
// Other events are discarded.
func (h *harness) waitEvent(match func(Event) bool) Event {
	h.t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case e := <-h.events:
			if match(e) {
				return e
			}
		case <-timeout:
			h.t.Fatalf("timed out waiting for an event\nlogs:\n%s", h.logs.String())
		}
	}
}

func hasPeer(name string) func(Event) bool {
	return func(e Event) bool {
		for _, p := range e.Peers {
			if p.Name == name && p.Online {
				return true
			}
		}
		return false
	}
}

func announceJSON(name string, port int, id string) []byte {
	return []byte(fmt.Sprintf(`{"lanxfer":1,"type":"announce","name":%q,"port":%d,"os":"linux","id":%q}`, name, port, id))
}

// fakePeer is a UDP socket that plays a peer on the LAN.
type fakePeer struct {
	conn *net.UDPConn
	name string
	id   string
}

func newFakePeer(t *testing.T, name, id string) *fakePeer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &fakePeer{conn: conn, name: name, id: id}
}

func (p *fakePeer) port() int { return p.conn.LocalAddr().(*net.UDPAddr).Port }

// announceUntilLearned sends announces to the node until it reports the peer
// as online. The node binds its discovery socket shortly after its HTTP
// listener, so the first datagrams may be lost.
func (p *fakePeer) announceUntilLearned(h *harness) Event {
	h.t.Helper()
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: h.cfg.Port}
	pkt := announceJSON(p.name, p.port(), p.id)
	match := hasPeer(p.name)
	timeout := time.After(waitTimeout)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := p.conn.WriteToUDP(pkt, dst); err != nil {
			h.t.Fatal(err)
		}
		select {
		case e := <-h.events:
			if match(e) {
				return e
			}
		case <-tick.C:
		case <-timeout:
			h.t.Fatalf("node never learned peer %q\nlogs:\n%s", p.name, h.logs.String())
		}
	}
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunReceivesAndRecords(t *testing.T) {
	h := startNode(t, nil)
	src := writeTemp(t, "hello.txt", "hello node")

	if _, err := client.Send(context.Background(), "127.0.0.1", h.cfg.Port, src, client.SendOptions{From: "sender"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	e := h.waitEvent(func(e Event) bool { return e.Entry != nil })
	if e.Entry.Dir != history.In || e.Entry.Kind != history.KindFile || e.Entry.SavedAs != "hello.txt" {
		t.Errorf("unexpected entry: %+v", *e.Entry)
	}
	if e.Entry.Time.IsZero() {
		t.Error("event entry has no time")
	}

	got, err := os.ReadFile(filepath.Join(h.saveDir, "hello.txt"))
	if err != nil || string(got) != "hello node" {
		t.Errorf("saved file = %q, %v", got, err)
	}
	entries, err := history.ReadAll(filepath.Join(h.configDir, "history.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("history = %v, %v; want one entry", entries, err)
	}
	if !entries[0].Time.Equal(e.Entry.Time) || entries[0].SavedAs != "hello.txt" {
		t.Errorf("event %+v and file entry %+v disagree", *e.Entry, entries[0])
	}
	if _, err := os.Stat(filepath.Join(h.configDir, "device-id")); err != nil {
		t.Errorf("device-id not created: %v", err)
	}
}

func TestRunDeliversText(t *testing.T) {
	h := startNode(t, nil)
	if _, err := client.Say("127.0.0.1", h.cfg.Port, "ping 123", client.SendOptions{From: "sender"}); err != nil {
		t.Fatalf("Say: %v", err)
	}
	e := h.waitEvent(func(e Event) bool { return e.Entry != nil })
	if e.Entry.Kind != history.KindText || e.Entry.Text != "ping 123" {
		t.Errorf("unexpected entry: %+v", *e.Entry)
	}
	if !strings.Contains(h.out.String(), "ping 123") {
		t.Errorf("text not printed to Out: %q", h.out.String())
	}
}

func TestRunLearnsPeerFromAnnounce(t *testing.T) {
	h := startNode(t, nil)
	p := newFakePeer(t, "peer1", testID)
	e := p.announceUntilLearned(h)

	if len(e.Peers) != 1 || e.Peers[0].ID != testID || e.Peers[0].Addr.Port() != uint16(p.port()) {
		t.Errorf("unexpected peers: %+v", e.Peers)
	}
	// The saver writes the file before it reports the change.
	data, err := os.ReadFile(filepath.Join(h.configDir, "peers.json"))
	if err != nil {
		t.Fatalf("peers.json: %v", err)
	}
	var saved []presence.Peer
	if err := json.Unmarshal(data, &saved); err != nil || len(saved) != 1 || saved[0].Name != "peer1" {
		t.Errorf("peers.json = %s, %v", data, err)
	}

	resp, err := http.Get("http://" + h.addr() + "/local/peers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var served []presence.Peer
	if err := json.NewDecoder(resp.Body).Decode(&served); err != nil || len(served) != 1 || served[0].Name != "peer1" {
		t.Errorf("/local/peers = %+v, %v", served, err)
	}
}

func TestRunProbesKnownPeers(t *testing.T) {
	h := startNode(t, nil)
	p := newFakePeer(t, "peer1", testID)
	p.announceUntilLearned(h)

	// Probes go to the address the peer announced, which is its own socket.
	queries := 0
	buf := make([]byte, 2048)
	deadline := time.Now().Add(waitTimeout)
	for queries < 2 {
		p.conn.SetReadDeadline(deadline)
		n, _, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("after %d queries: %v", queries, err)
		}
		if strings.Contains(string(buf[:n]), `"type":"query"`) {
			queries++
		}
	}
}

// postLocalHistory posts body to the node's POST /local/history as a CLI with
// the given device ID would, and returns the status code.
func postLocalHistory(t *testing.T, h *harness, fromID, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+h.addr()+"/local/history", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if fromID != "" {
		req.Header.Set(server.FromIDHeader, fromID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestRunReportsLocalHistory(t *testing.T) {
	h := startNode(t, nil)
	body := `{"dir":"out","kind":"text","text":"sent from cli","peer_name":"other","status":"ok"}`
	selfID := h.deviceID()
	if status := postLocalHistory(t, h, selfID, body); status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}

	e := h.waitEvent(func(e Event) bool { return e.Entry != nil })
	if e.Entry.Dir != history.Out || e.Entry.Text != "sent from cli" {
		t.Errorf("unexpected entry: %+v", *e.Entry)
	}
	entries, err := history.ReadAll(filepath.Join(h.configDir, "history.jsonl"))
	if err != nil || len(entries) != 1 || entries[0].Text != "sent from cli" {
		t.Errorf("history = %v, %v", entries, err)
	}
	if !entries[0].Time.Equal(e.Entry.Time) {
		t.Errorf("event time %v differs from file time %v", e.Entry.Time, entries[0].Time)
	}
}

func TestRunRefusesLocalHistoryOfAnotherDevice(t *testing.T) {
	h := startNode(t, nil)
	body := `{"dir":"out","kind":"text","text":"for another user","status":"ok"}`
	for name, fromID := range map[string]string{"other ID": testID, "no ID": ""} {
		if status := postLocalHistory(t, h, fromID, body); status != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409", name, status)
		}
	}
	entries, err := history.ReadAll(filepath.Join(h.configDir, "history.jsonl"))
	if err != nil || len(entries) != 0 {
		t.Errorf("history = %v, %v; want nothing recorded", entries, err)
	}
}

func TestRunSendsSavedPeersFirst(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	saved := `[{"id":"` + testID + `","name":"saved1","addr":"192.0.2.7:8425","last_seen":"2026-10-20T09:00:00Z","online":true}]`
	if err := os.WriteFile(filepath.Join(configDir, "peers.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startNode(t, func(c *Config) {
		c.ConfigDir = configDir
		c.AnnounceEvery, c.ProbeEvery = time.Hour, time.Hour // nothing else may disturb the list
	})
	e := h.waitEvent(func(e Event) bool { return e.Peers != nil })
	if len(e.Peers) != 1 || e.Peers[0].Name != "saved1" || e.Peers[0].ID != testID || e.Peers[0].Online {
		t.Errorf("first peers event = %+v, want the saved peer, offline", e.Peers)
	}
}

func TestRunSendsEmptyPeersEventAtStartup(t *testing.T) {
	h := startNode(t, func(c *Config) { c.AnnounceEvery, c.ProbeEvery = time.Hour, time.Hour })
	select {
	case e := <-h.events:
		if e.Entry != nil || e.Peers == nil || len(e.Peers) != 0 {
			t.Errorf("first event = %+v, want an empty, non-nil peers list", e)
		}
	case <-time.After(waitTimeout):
		t.Fatal("no initial event")
	}
}

func TestRunRecordsAbsoluteReceiptPath(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	h := startNode(t, func(c *Config) { c.SaveDir = "inbox" })
	src := writeTemp(t, "hello.txt", "hi")
	if _, err := client.Send(context.Background(), "127.0.0.1", h.cfg.Port, src, client.SendOptions{From: "sender"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	e := h.waitEvent(func(e Event) bool { return e.Entry != nil })
	if !filepath.IsAbs(e.Entry.Path) {
		t.Fatalf("Path = %q, want an absolute path", e.Entry.Path)
	}
	if _, err := os.Stat(e.Entry.Path); err != nil {
		t.Errorf("recorded path does not exist: %v", err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	h := startNode(t, nil)
	p := newFakePeer(t, "peer1", testID)
	p.announceUntilLearned(h)

	start := time.Now()
	if err := h.stop(); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Run took %v to stop", d)
	}
	if !strings.Contains(h.logs.String(), "shutting down") {
		t.Errorf("no shutdown log: %q", h.logs.String())
	}
	data, err := os.ReadFile(filepath.Join(h.configDir, "peers.json"))
	if err != nil || !strings.Contains(string(data), "peer1") {
		t.Errorf("peers.json after shutdown = %q, %v", data, err)
	}
	// The port is released.
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", h.cfg.Port))
	if err != nil {
		t.Errorf("port still held: %v", err)
	} else {
		l.Close()
	}
}

func TestRunReturnsListenError(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	cfg := Config{
		SaveDir:   filepath.Join(t.TempDir(), "save"),
		ConfigDir: t.TempDir(),
		Port:      port,
		MaxSize:   1 << 20,
		Name:      "n",
		Logger:    log.New(io.Discard, "", 0),
	}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run succeeded on a port in use")
		}
	case <-time.After(waitTimeout):
		t.Fatal("Run did not return")
	}
}

func TestRunWithoutConfigDir(t *testing.T) {
	blocker := writeTemp(t, "blocker", "a regular file")
	h := startNode(t, func(c *Config) { c.ConfigDir = filepath.Join(blocker, "lanxfer") })

	src := writeTemp(t, "plain.txt", "still works")
	if _, err := client.Send(context.Background(), "127.0.0.1", h.cfg.Port, src, client.SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(h.saveDir, "plain.txt"))
	if err != nil || string(got) != "still works" {
		t.Errorf("saved file = %q, %v", got, err)
	}
	if !strings.Contains(h.logs.String(), "warning: config folder unavailable, running without device ID, history and saved peers:") {
		t.Errorf("no config warning in logs: %q", h.logs.String())
	}

	// Peers are still tracked in memory and served.
	p := newFakePeer(t, "peer1", testID)
	p.announceUntilLearned(h)
	resp, err := http.Get("http://" + h.addr() + "/local/peers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var served []presence.Peer
	if err := json.NewDecoder(resp.Body).Decode(&served); err != nil || len(served) != 1 {
		t.Errorf("/local/peers = %+v, %v", served, err)
	}
	if err := h.stop(); err != nil {
		t.Errorf("Run = %v", err)
	}
}

func TestRunWithEmptyConfigDirWarns(t *testing.T) {
	h := startNode(t, func(c *Config) { c.ConfigDir = "" })
	// The warning is logged after the HTTP port opens, so poll for it.
	deadline := time.Now().Add(waitTimeout)
	for !strings.Contains(h.logs.String(), "warning: config folder unavailable") {
		if time.Now().After(deadline) {
			t.Fatalf("no config warning in logs: %q", h.logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEventRecorder(t *testing.T) {
	var got []Event
	rec := &eventRecorder{
		store:  history.Open(filepath.Join(t.TempDir(), "missing-folder", "history.jsonl")),
		events: &emitter{fn: func(e Event) { got = append(got, e) }},
	}
	e := history.Entry{Dir: history.In, Kind: history.KindText, Text: "x", Status: history.StatusOK}
	if err := rec.Append(e); err == nil {
		t.Fatal("Append succeeded although the folder does not exist")
	}
	if len(got) != 0 {
		t.Errorf("event fired for a failed append: %+v", got)
	}

	histPath := filepath.Join(t.TempDir(), "history.jsonl")
	rec.store = history.Open(histPath)
	if err := rec.Append(e); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Entry == nil || got[0].Entry.Time.IsZero() {
		t.Fatalf("events = %+v, want one with a time", got)
	}
	if got[0].Entry.V != history.Version {
		t.Errorf("event entry V = %d, want %d", got[0].Entry.V, history.Version)
	}
	if !e.Time.IsZero() || e.V != 0 {
		t.Error("caller's entry was modified")
	}

	// Once closed, the entry is still recorded but no event is sent.
	rec.events.close()
	if err := rec.Append(e); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("event sent after close: %+v", got[1:])
	}
	entries, err := history.ReadAll(histPath)
	if err != nil || len(entries) != 2 {
		t.Errorf("history has %v, %v; want both appends", entries, err)
	}

	rec.events = &emitter{} // no callback
	if err := rec.Append(e); err != nil {
		t.Errorf("Append with no OnEvent: %v", err)
	}
}
