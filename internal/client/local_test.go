package client_test

import (
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

// startLocalServer runs a real receiver with the given options and returns
// its port.
func startLocalServer(t *testing.T, opts ...server.Option) int {
	t.Helper()
	st, err := server.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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

	got, err := client.LocalPeers(port)
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

func TestLocalPeersEmptyList(t *testing.T) {
	port := startLocalServer(t, server.WithPresence(nil, func() []presence.Peer { return nil }))
	got, err := client.LocalPeers(port)
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
		"not JSON": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "<html>hello</html>")
		}),
		"JSON object": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"peers":[]}`)
		}),
		"JSON null": startHandler(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "null")
		}),
	}
	for name, port := range tests {
		t.Run(name, func(t *testing.T) {
			peers, err := client.LocalPeers(port)
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
	_, err := client.LocalPeers(port)
	if !errors.Is(err, client.ErrNoLocalReceiver) {
		t.Errorf("err = %v, want ErrNoLocalReceiver", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v, want about 1s", d)
	}
}

func TestLocalPeersKeepsCauseInMessage(t *testing.T) {
	_, err := client.LocalPeers(closedPort(t))
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
	if err := client.LocalRecord(port, e); err != nil {
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

func TestLocalRecordWaitsLongerThanPeersCall(t *testing.T) {
	// A timeout would make the CLI append on its own and risk a duplicate,
	// so a receiver that is merely slow must still be waited for.
	port := startHandler(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	e := history.Entry{Time: time.Unix(1, 0).UTC(), Dir: history.Out, Kind: history.KindText, Text: "hi", Status: history.StatusOK}
	if err := client.LocalRecord(port, e); err != nil {
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
	if err := client.LocalRecord(port, e); err != nil {
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
	}
	for name, port := range tests {
		t.Run(name, func(t *testing.T) {
			if err := client.LocalRecord(port, e); !errors.Is(err, client.ErrNoLocalReceiver) {
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
		err := client.LocalRecord(port, e)
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
	err := client.LocalRecord(port, history.Entry{Dir: "sideways"})
	if err == nil || errors.Is(err, client.ErrNoLocalReceiver) {
		t.Errorf("err = %v, want a plain error", err)
	}
}
