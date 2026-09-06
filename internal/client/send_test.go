package client_test

import (
	"io"
	"log"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// startReceiver starts a test receive server and returns its host, port,
// and storage directory.
func startReceiver(t *testing.T) (host string, port int, dir string) {
	t.Helper()
	st, err := server.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(st, 1<<20, log.New(io.Discard, "", 0)).Handler())
	t.Cleanup(ts.Close)
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	h, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	portNum, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, portNum, st.Dir
}

func TestSendRoundtrip(t *testing.T) {
	host, port, dir := startReceiver(t)

	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := client.Send(host, port, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteName != "src.bin" {
		t.Errorf("RemoteName = %q, want %q", res.RemoteName, "src.bin")
	}
	if res.Size != int64(len("payload")) {
		t.Errorf("Size = %d, want %d", res.Size, len("payload"))
	}
	data, err := os.ReadFile(filepath.Join(dir, "src.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Errorf("received content = %q, want %q", data, "payload")
	}
}

func TestSendJapaneseFilename(t *testing.T) {
	host, port, dir := startReceiver(t)

	src := filepath.Join(t.TempDir(), "日本語 名前.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := client.Send(host, port, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteName != "日本語 名前.txt" {
		t.Errorf("RemoteName = %q, want %q", res.RemoteName, "日本語 名前.txt")
	}
	if _, err := os.Stat(filepath.Join(dir, "日本語 名前.txt")); err != nil {
		t.Errorf("file not stored under original name: %v", err)
	}
}

func TestSendMissingFile(t *testing.T) {
	host, port, _ := startReceiver(t)
	if _, err := client.Send(host, port, "/no/such/file.bin"); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestSendDirectoryIsRejected(t *testing.T) {
	host, port, _ := startReceiver(t)
	if _, err := client.Send(host, port, t.TempDir()); err == nil {
		t.Fatal("want error for directory")
	}
}

func TestSendServerErrorIsReported(t *testing.T) {
	host, port, dir := startReceiver(t)
	// 2 MiB exceeds the receiver's 1 MiB limit, so the server answers 413.
	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Send(host, port, src); err == nil {
		t.Fatal("want error when server rejects upload")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("receiver dir not empty after rejected upload")
	}
}

func TestSendConnectionRefused(t *testing.T) {
	// Grab a free port, then close the listener so nothing is listening.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	src := filepath.Join(t.TempDir(), "x.bin")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Send("127.0.0.1", port, src); err == nil {
		t.Fatal("want error when nothing is listening")
	}
}
