package client_test

import (
	"io"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// startMessageReceiver runs a receiver that accepts messages and returns
// where it listens and the path of its message log. Assertions read the log
// file so no memory is shared with the server goroutine.
func startMessageReceiver(t *testing.T) (string, int, string) {
	t.Helper()
	st, err := server.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(st.Dir, "messages.log")
	inbox := server.NewInbox(logPath, io.Discard)
	ts := httptest.NewServer(server.New(st, 1<<20, inbox, log.New(io.Discard, "", 0)).Handler())
	t.Cleanup(ts.Close)
	host, port := splitHostPort(t, ts.URL)
	return host, port, logPath
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSayRoundtrip(t *testing.T) {
	host, port, logPath := startMessageReceiver(t)
	if err := client.Say(host, port, "mac1", "hello\nworld"); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, logPath); !strings.Contains(got, " from mac1 (127.0.0.1) ---\nhello\nworld\n") {
		t.Errorf("log = %q", got)
	}
}

func TestSayWithoutName(t *testing.T) {
	host, port, logPath := startMessageReceiver(t)
	if err := client.Say(host, port, "", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, logPath); !strings.Contains(got, " from 127.0.0.1 ---\n") {
		t.Errorf("log = %q", got)
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
