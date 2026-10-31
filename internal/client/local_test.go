package client_test

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// memRecorder is an in-memory server.Recorder.
type memRecorder struct {
	mu      sync.Mutex
	entries []history.Entry
}

func (m *memRecorder) Append(e history.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

func (m *memRecorder) all() []history.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]history.Entry(nil), m.entries...)
}

// startLocalServer runs a real receiver whose device ID is receiverID, plus
// the given options, and returns its port.
func startLocalServer(t *testing.T, opts ...server.Option) int {
	t.Helper()
	st, err := server.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts = append([]server.Option{server.WithDeviceID(receiverID)}, opts...)
	ts := httptest.NewServer(server.New(st, 1<<20, nil, log.New(io.Discard, "", 0), opts...).Handler())
	t.Cleanup(ts.Close)
	_, port := splitHostPort(t, ts.URL)
	return port
}

// startHandler runs h and returns its port.
func startHandler(t *testing.T, h http.HandlerFunc) int {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	_, port := splitHostPort(t, ts.URL)
	return port
}

// closedPort returns a port on 127.0.0.1 that nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestLocalPeers(t *testing.T) {
	want := []presence.Peer{
		{ID: receiverID, Name: "mac2", OS: "darwin", Addr: netip.MustParseAddrPort("192.168.1.20:8425"),
			LastSeen: time.Unix(1_700_000_000, 0).UTC(), Online: true},
		{Name: "old", Addr: netip.MustParseAddrPort("192.168.1.30:8425"),
			LastSeen: time.Unix(1_600_000_000, 0).UTC()},
	}
	port := startLocalServer(t, server.WithPresence(func(presence.Sighting) {}, func() []presence.Peer { return want }))

	got, err := client.LocalPeers(port, receiverID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d peers, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("peer %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// peersHandler serves peers as JSON, the way a receiver with the given device
// ID would. An empty id sends no ID header.
func peersHandler(id string, peers []presence.Peer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id != "" {
			w.Header().Set(server.IDHeader, id)
		}
		data, _ := json.Marshal(peers)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

func TestLocalPeersRejectsOtherReceiver(t *testing.T) {
	// Another user's receiver may answer on this port; its list must not be
	// used for send targets.
	peers := []presence.Peer{{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.1.20:8425"), Online: true}}
	otherID := "00112233445566778899aabbccddeeff"
	tests := map[string]struct {
		port   int
		selfID string
	}{
		"other receiver's ID": {startLocalServer(t, server.WithDeviceID(otherID), server.WithPresence(nil, func() []presence.Peer { return peers })), receiverID},
		"receiver without ID": {startHandler(t, peersHandler("", peers)), receiverID},
		"malformed ID":        {startHandler(t, peersHandler("not-an-id", peers)), receiverID},
		"client without ID":   {startLocalServer(t, server.WithPresence(nil, func() []presence.Peer { return peers })), ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := client.LocalPeers(tt.port, tt.selfID)
			if !errors.Is(err, client.ErrNoLocalReceiver) {
				t.Errorf("err = %v, want ErrNoLocalReceiver", err)
			}
			if got != nil {
				t.Errorf("peers = %+v, want nil", got)
			}
		})
	}
}

func TestLocalPeersAcceptsReceiverWithoutIDForClientWithoutID(t *testing.T) {
	peers := []presence.Peer{{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.1.20:8425"), Online: true}}
	port := startHandler(t, peersHandler("", peers))
	got, err := client.LocalPeers(port, "")
	if err != nil || len(got) != 1 {
		t.Errorf("got %+v, %v; want the one peer", got, err)
	}
}

func TestLocalPeersDropsUnusableEntries(t *testing.T) {
	good := presence.Peer{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.1.20:8425"), Online: true}
	peers := []presence.Peer{
		{Name: "public", Addr: netip.MustParseAddrPort("8.8.8.8:8425")},
		good,
		{Name: "", Addr: netip.MustParseAddrPort("192.168.1.21:8425")},
		{Name: "bad\nname", Addr: netip.MustParseAddrPort("192.168.1.22:8425")},
		{Name: strings.Repeat("a", 65), Addr: netip.MustParseAddrPort("192.168.1.23:8425")},
		{Name: "unspecified", Addr: netip.MustParseAddrPort("0.0.0.0:8425")},
		{Name: "invalid addr"},
		{Name: "loopback", Addr: netip.MustParseAddrPort("127.0.0.1:8425")},
	}
	got, err := client.LocalPeers(startHandler(t, peersHandler(receiverID, peers)), receiverID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != good || got[1].Name != "loopback" {
		t.Errorf("got %+v, want only mac2 and loopback", got)
	}
}

func TestLocalPeersKeepsEmptyListNonNilWhenAllDropped(t *testing.T) {
	peers := []presence.Peer{{Name: "public", Addr: netip.MustParseAddrPort("8.8.8.8:8425")}}
	got, err := client.LocalPeers(startHandler(t, peersHandler(receiverID, peers)), receiverID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want a non-nil empty slice", got)
	}
}

func TestLocalPeersEmptyList(t *testing.T) {
	port := startLocalServer(t, server.WithPresence(nil, func() []presence.Peer { return nil }))
	got, err := client.LocalPeers(port, receiverID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want no peers", got)
	}
}

func TestLocalPeersReportsNoLocalReceiver(t *testing.T) {
	tests := map[string]int{
		"connection refused":   closedPort(t),
		"404 (older receiver)": startLocalServer(t),
		"405": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusMethodNotAllowed)
		}),
		"403": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		}),
		"500": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}),
		// These answer with the right ID, so the body is what gets rejected.
		"not JSON": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(server.IDHeader, receiverID)
			io.WriteString(w, "<html>hello</html>")
		}),
		"JSON object": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(server.IDHeader, receiverID)
			io.WriteString(w, `{"peers":[]}`)
		}),
		"JSON null": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(server.IDHeader, receiverID)
			io.WriteString(w, "null")
		}),
	}
	for name, port := range tests {
		t.Run(name, func(t *testing.T) {
			peers, err := client.LocalPeers(port, receiverID)
			if !errors.Is(err, client.ErrNoLocalReceiver) {
				t.Errorf("err = %v, want ErrNoLocalReceiver", err)
			}
			if peers != nil {
				t.Errorf("peers = %+v, want nil", peers)
			}
		})
	}
}

func TestLocalPeersTimesOut(t *testing.T) {
	port := startHandler(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	start := time.Now()
	_, err := client.LocalPeers(port, receiverID)
	if !errors.Is(err, client.ErrNoLocalReceiver) {
		t.Errorf("err = %v, want ErrNoLocalReceiver", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v, want about 1s", d)
	}
}

func TestLocalPeersKeepsCauseInMessage(t *testing.T) {
	_, err := client.LocalPeers(closedPort(t), receiverID)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("err = %v, want the dial error in the message", err)
	}
}

func TestLocalRecord(t *testing.T) {
	rec := &memRecorder{}
	port := startLocalServer(t, server.WithHistory(rec))
	e := history.Entry{
		Time: time.Unix(1_700_000_000, 0).UTC(), Dir: history.Out, PeerID: receiverID, PeerName: "mac2",
		PeerAddr: "192.168.1.20", Kind: history.KindFile, Name: "a.txt", Size: 12, Status: history.StatusOK,
	}
	if err := client.LocalRecord(port, receiverID, e); err != nil {
		t.Fatal(err)
	}
	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(got))
	}
	want := e
	want.V = history.Version // the server fills in a missing version
	if got[0] != want {
		t.Errorf("recorded %+v, want %+v", got[0], want)
	}
}

func TestLocalRecordSendsOwnID(t *testing.T) {
	var got string
	port := startHandler(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(server.FromIDHeader)
		w.WriteHeader(http.StatusNoContent)
	})
	e := history.Entry{Time: time.Unix(1, 0).UTC(), Dir: history.Out, Kind: history.KindText, Text: "hi", Status: history.StatusOK}
	if err := client.LocalRecord(port, receiverID, e); err != nil {
		t.Fatal(err)
	}
	if got != receiverID {
		t.Errorf("%s = %q, want %q", server.FromIDHeader, got, receiverID)
	}
	got = "unset"
	if err := client.LocalRecord(port, "", e); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("%s = %q, want none without an ID", server.FromIDHeader, got)
	}
}

func TestLocalRecordOtherReceiverIsNoLocalReceiver(t *testing.T) {
	// Another user's receiver answers 409; nothing may be recorded there and
	// the caller must fall back to its own history file.
	rec := &memRecorder{}
	port := startLocalServer(t, server.WithDeviceID("00112233445566778899aabbccddeeff"), server.WithHistory(rec))
	e := history.Entry{Time: time.Unix(1, 0).UTC(), Dir: history.Out, Kind: history.KindText, Text: "secret", Status: history.StatusOK}
	if err := client.LocalRecord(port, receiverID, e); !errors.Is(err, client.ErrNoLocalReceiver) {
		t.Errorf("err = %v, want ErrNoLocalReceiver", err)
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("other receiver recorded %+v, want nothing", got)
	}
}

func TestLocalRecordWaitsLongerThanPeersCall(t *testing.T) {
	// A timeout would make the CLI append on its own and risk a duplicate,
	// so a receiver that is merely slow must still be waited for.
	port := startHandler(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	e := history.Entry{Time: time.Unix(1, 0).UTC(), Dir: history.Out, Kind: history.KindText, Text: "hi", Status: history.StatusOK}
	if err := client.LocalRecord(port, receiverID, e); err != nil {
		t.Errorf("err = %v, want the slow reply to be accepted", err)
	}
}

func TestLocalRecordSendsJSON(t *testing.T) {
	var ct, origin, body string
	port := startHandler(t, func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		origin = r.Header.Get("Origin")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	})
	e := history.Entry{Time: time.Unix(1, 0).UTC(), Dir: history.Out, Kind: history.KindText, Text: "hi", Status: history.StatusOK}
	if err := client.LocalRecord(port, receiverID, e); err != nil {
		t.Fatal(err)
	}
	if ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if origin != "" {
		t.Errorf("Origin = %q, want none", origin)
	}
	if !strings.Contains(body, `"text":"hi"`) {
		t.Errorf("body = %q", body)
	}
}

func TestLocalRecordReportsNoLocalReceiver(t *testing.T) {
	e := history.Entry{V: history.Version, Time: time.Unix(1, 0).UTC(), Dir: history.Out, Kind: history.KindText, Text: "hi", Status: history.StatusOK}
	tests := map[string]int{
		"connection refused": closedPort(t),
		"404 (no history)":   startLocalServer(t),
		"405": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusMethodNotAllowed)
		}),
		"409 (other device)": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not this device's receiver", http.StatusConflict)
		}),
	}
	for name, port := range tests {
		t.Run(name, func(t *testing.T) {
			if err := client.LocalRecord(port, receiverID, e); !errors.Is(err, client.ErrNoLocalReceiver) {
				t.Errorf("err = %v, want ErrNoLocalReceiver", err)
			}
		})
	}
}

func TestLocalRecordOtherFailuresAreNotNoLocalReceiver(t *testing.T) {
	e := history.Entry{V: history.Version, Time: time.Unix(1, 0).UTC(), Dir: history.Out, Kind: history.KindText, Text: "hi", Status: history.StatusOK}
	for _, status := range []int{400, 403, 413, 415, 500} {
		port := startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "  detail here \n", status)
		})
		err := client.LocalRecord(port, receiverID, e)
		if err == nil {
			t.Errorf("status %d: want error", status)
			continue
		}
		if errors.Is(err, client.ErrNoLocalReceiver) {
			t.Errorf("status %d: err = %v, must not be ErrNoLocalReceiver", status, err)
		}
		if !strings.Contains(err.Error(), "detail here") || !strings.Contains(err.Error(), http.StatusText(status)) {
			t.Errorf("status %d: err = %q, want status and server message", status, err)
		}
	}
}

func TestLocalRecordInvalidEntryIsRejectedByReceiver(t *testing.T) {
	port := startLocalServer(t, server.WithHistory(&memRecorder{}))
	err := client.LocalRecord(port, receiverID, history.Entry{Dir: "sideways"})
	if err == nil || errors.Is(err, client.ErrNoLocalReceiver) {
		t.Errorf("err = %v, want a plain error", err)
	}
}
