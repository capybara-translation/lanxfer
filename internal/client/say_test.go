package client_test

import (
	"bytes"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// syncBuffer is a bytes.Buffer safe to read while the server goroutine
// writes to it.
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

// startMessageReceiver runs a receiver that accepts messages and returns
// where it listens and the buffer its inbox prints to.
func startMessageReceiver(t *testing.T) (string, int, *syncBuffer) {
	t.Helper()
	st, err := server.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := &syncBuffer{}
	inbox := server.NewInbox(nil, out)
	ts := httptest.NewServer(server.New(st, 1<<20, inbox, log.New(io.Discard, "", 0)).Handler())
	t.Cleanup(ts.Close)
	host, port := splitHostPort(t, ts.URL)
	return host, port, out
}

func TestSayRoundtrip(t *testing.T) {
	host, port, out := startMessageReceiver(t)
	if _, err := client.Say(host, port, "hello\nworld", client.SendOptions{From: "mac1"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, " from mac1 (127.0.0.1) ---\nhello\nworld\n") {
		t.Errorf("terminal = %q", got)
	}
}

func TestSayReturnsResult(t *testing.T) {
	host, port, _ := startMessageReceiver(t)
	res, err := client.Say(host, port, "hi", client.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// This receiver has no device ID, so there is no peer ID to report.
	if res == nil || *res != (client.Result{}) {
		t.Errorf("result = %+v, want a zero Result", res)
	}
}

func TestSayWithoutName(t *testing.T) {
	host, port, out := startMessageReceiver(t)
	if _, err := client.Say(host, port, "hi", client.SendOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, " from 127.0.0.1 ---\n") {
		t.Errorf("terminal = %q", got)
	}
}

func TestSayTooLargeReportsServerHint(t *testing.T) {
	host, port, _ := startMessageReceiver(t)
	_, err := client.Say(host, port, strings.Repeat("a", server.MaxMessageSize+1), client.SendOptions{From: "mac1"})
	if err == nil || !strings.Contains(err.Error(), "lanxfer send") {
		t.Errorf("err = %v, want the server's hint to use lanxfer send", err)
	}
}

func TestSayConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if _, err := client.Say("127.0.0.1", port, "hi", client.SendOptions{From: "mac1"}); err == nil {
		t.Fatal("want error when nothing is listening")
	}
}
