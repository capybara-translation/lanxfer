package client_test

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/server"
)

const (
	senderID   = "00112233445566778899aabbccddeeff"
	receiverID = "ffeeddccbbaa99887766554433221100"
)

// headerLog records the headers of every request a server sees.
type headerLog struct {
	mu   sync.Mutex
	seen []http.Header
}

func (l *headerLog) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.seen = append(l.seen, r.Header.Clone())
		l.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

func (l *headerLog) requests() []http.Header {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]http.Header(nil), l.seen...)
}

// startIDReceiver runs a receiver that has a device ID and accepts files,
// folders and messages.
func startIDReceiver(t *testing.T) (string, int, *headerLog) {
	t.Helper()
	st, err := server.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inbox := server.NewInbox(nil, io.Discard)
	srv := server.New(st, 1<<20, inbox, log.New(io.Discard, "", 0), server.WithDeviceID(receiverID))
	l := &headerLog{}
	ts := httptest.NewServer(l.wrap(srv.Handler()))
	t.Cleanup(ts.Close)
	host, port := splitHostPort(t, ts.URL)
	return host, port, l
}

// operations lists the three ways to send, each as a function that performs
// one send against host:port.
var operations = map[string]func(t *testing.T, host string, port int, opts client.SendOptions) (*client.Result, error){
	"Send": func(t *testing.T, host string, port int, opts client.SendOptions) (*client.Result, error) {
		src := filepath.Join(t.TempDir(), "f.txt")
		if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		return client.Send(context.Background(), host, port, src, opts)
	},
	"SendDir": func(t *testing.T, host string, port int, opts client.SendOptions) (*client.Result, error) {
		dir := filepath.Join(t.TempDir(), "folder")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
			t.Fatal(err)
		}
		plan, err := client.ScanDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return client.SendDir(context.Background(), host, port, plan, opts)
	},
	"Say": func(t *testing.T, host string, port int, opts client.SendOptions) (*client.Result, error) {
		return client.Say(context.Background(), host, port, "hello", opts)
	},
}

func TestSendSendsFromIDAndReadsPeerID(t *testing.T) {
	for name, op := range operations {
		t.Run(name+"/with id", func(t *testing.T) {
			host, port, seen := startIDReceiver(t)
			res, err := op(t, host, port, client.SendOptions{From: "mac1", FromID: senderID})
			if err != nil {
				t.Fatal(err)
			}
			if res.PeerID != receiverID {
				t.Errorf("PeerID = %q, want %q", res.PeerID, receiverID)
			}
			reqs := seen.requests()
			if len(reqs) != 1 {
				t.Fatalf("server saw %d requests, want 1", len(reqs))
			}
			if got := reqs[0].Get(server.FromIDHeader); got != senderID {
				t.Errorf("%s = %q, want %q", server.FromIDHeader, got, senderID)
			}
			if got := reqs[0].Get(server.FromHeader); got != "mac1" {
				t.Errorf("%s = %q, want mac1", server.FromHeader, got)
			}
		})
		t.Run(name+"/without id", func(t *testing.T) {
			host, port, seen := startIDReceiver(t)
			res, err := op(t, host, port, client.SendOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if res.PeerID != receiverID {
				t.Errorf("PeerID = %q, want %q", res.PeerID, receiverID)
			}
			for _, h := range seen.requests() {
				if _, ok := h[http.CanonicalHeaderKey(server.FromIDHeader)]; ok {
					t.Errorf("%s was sent although FromID is empty", server.FromIDHeader)
				}
			}
		})
	}
}

func TestPeerIDIgnoresMalformedHeader(t *testing.T) {
	bad := map[string]string{
		"missing":   "",
		"not hex":   "not-an-id",
		"uppercase": "FFEEDDCCBBAA99887766554433221100",
		"too short": "ffeedd",
	}
	for name, op := range operations {
		for label, value := range bad {
			t.Run(name+"/"+label, func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if value != "" {
						w.Header().Set(server.IDHeader, value)
					}
					if r.Method == http.MethodPost {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					w.WriteHeader(http.StatusCreated)
					io.WriteString(w, "saved")
				}))
				t.Cleanup(ts.Close)
				host, port := splitHostPort(t, ts.URL)
				res, err := op(t, host, port, client.SendOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if res.PeerID != "" {
					t.Errorf("PeerID = %q, want empty", res.PeerID)
				}
			})
		}
	}
}
