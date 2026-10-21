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
	if err := client.Say(host, port, "mac1", "hello\nworld"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, " from mac1 (127.0.0.1) ---\nhello\nworld\n") {
		t.Errorf("terminal = %q", got)
	}
}

func TestSayWithoutName(t *testing.T) {
	host, port, out := startMessageReceiver(t)
	if err := client.Say(host, port, "", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, " from 127.0.0.1 ---\n") {
		t.Errorf("terminal = %q", got)
	}
}

func TestSayTooLargeReportsServerHint(t *testing.T) {
	host, port, _ := startMessageReceiver(t)
	err := client.Say(host, port, "mac1", strings.Repeat("a", server.MaxMessageSize+1))
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
	if err := client.Say("127.0.0.1", port, "mac1", "hi"); err == nil {
		t.Fatal("want error when nothing is listening")
	}
}
