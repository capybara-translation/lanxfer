package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// newTestServer starts a test HTTP server saving into a temp directory.
func newTestServer(t *testing.T, maxSize int64) (*httptest.Server, *Storage) {
	t.Helper()
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, maxSize, nil, log.New(io.Discard, "", 0))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func putFile(t *testing.T, url, content string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestPutStoresFileAndReturnsName(t *testing.T) {
	ts, st := newTestServer(t, 1<<20)
	resp := putFile(t, ts.URL+"/files/hello.txt", "hello")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := strings.TrimSpace(string(body)); got != "hello.txt" {
		t.Errorf("response body = %q, want %q", got, "hello.txt")
	}
	data, err := os.ReadFile(filepath.Join(st.Dir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("stored content = %q, want %q", data, "hello")
	}
}

func TestPutCollisionReturnsRenamedName(t *testing.T) {
	ts, _ := newTestServer(t, 1<<20)
	putFile(t, ts.URL+"/files/a.txt", "one")
	resp := putFile(t, ts.URL+"/files/a.txt", "two")
	body, _ := io.ReadAll(resp.Body)
	if got := strings.TrimSpace(string(body)); got != "a (1).txt" {
		t.Errorf("second PUT saved as %q, want %q", got, "a (1).txt")
	}
}

func TestPutTooLargeIsRejected(t *testing.T) {
	ts, st := newTestServer(t, 4) // 4-byte limit
	resp := putFile(t, ts.URL+"/files/big.bin", "12345")
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if entries, _ := os.ReadDir(st.Dir); len(entries) != 0 {
		t.Errorf("dir not empty after rejected upload")
	}
}

func TestPutFromGlobalIPIsForbidden(t *testing.T) {
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, 1<<20, nil, log.New(io.Discard, "", 0))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/files/a.txt", strings.NewReader("x"))
	req.RemoteAddr = "203.0.113.5:12345" // spoof a global source address
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestPutTraversalNeverStoresFile(t *testing.T) {
	ts, st := newTestServer(t, 1<<20)
	// %2F decodes to "/", so the name becomes "../evil.txt". Whether routing
	// or validation rejects it is an implementation detail; what matters is
	// that it never returns 201 and never stores a file.
	resp := putFile(t, ts.URL+"/files/..%2Fevil.txt", "x")
	if resp.StatusCode == http.StatusCreated {
		t.Errorf("traversal request returned 201")
	}
	if entries, _ := os.ReadDir(st.Dir); len(entries) != 0 {
		t.Errorf("dir not empty after traversal attempt")
	}
}

// newLoggingHandler returns a handler whose log lines go to the returned
// buffer. Tests call ServeHTTP directly, so the handler runs on the test
// goroutine.
func newLoggingHandler(t *testing.T) (http.Handler, *bytes.Buffer) {
	t.Helper()
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	return New(st, 1<<20, nil, log.New(&logs, "", 0)).Handler(), &logs
}

// serveUpload sends a PUT for name from 192.168.68.53 straight to h.
func serveUpload(h http.Handler, name, from string, body io.Reader, size int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/files/"+url.PathEscape(name), body)
	req.ContentLength = size
	req.RemoteAddr = "192.168.68.53:5555"
	if from != "" {
		req.Header.Set(FromHeader, from)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPutLogsStartAndFinish(t *testing.T) {
	tests := []struct{ name, from, who string }{
		{"named sender", "mac1", "mac1 (192.168.68.53)"},
		{"no name", "", "192.168.68.53"},
		{"invalid name is dropped", "bad\tname", "192.168.68.53"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, logs := newLoggingHandler(t)
			rec := serveUpload(h, "a.txt", tt.from, strings.NewReader("hello"), 5)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201", rec.Code)
			}
			want := `receiving "a.txt" (5 B) from ` + tt.who + "\n" +
				`received "a.txt" (5 B) from ` + tt.who + "\n"
			if logs.String() != want {
				t.Errorf("logs = %q, want %q", logs.String(), want)
			}
		})
	}
}

func TestPutLogsRenamedName(t *testing.T) {
	h, logs := newLoggingHandler(t)
	serveUpload(h, "a.txt", "", strings.NewReader("one"), 3)
	serveUpload(h, "a.txt", "", strings.NewReader("two"), 3)
	if want := `received "a (1).txt" (3 B) from 192.168.68.53`; !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want a line %q", logs.String(), want)
	}
}

func TestPutLogsFailure(t *testing.T) {
	h, logs := newLoggingHandler(t)
	// 10 bytes declared, 3 arrive, then the connection breaks.
	body := io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(errors.New("connection reset")))
	rec := serveUpload(h, "a.txt", "mac1", body, 10)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	want := `failed to receive "a.txt" from mac1 (192.168.68.53) after 3 B of 10 B: `
	if len(lines) != 2 || !strings.HasPrefix(lines[1], want) {
		t.Errorf("logs = %q, want a second line starting with %q", logs.String(), want)
	}
}

func TestPutRejectedNameLogsNothing(t *testing.T) {
	h, logs := newLoggingHandler(t)
	rec := serveUpload(h, "CON", "", strings.NewReader("x"), 1)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if logs.Len() != 0 {
		t.Errorf("logs = %q, want nothing for a rejected name", logs.String())
	}
}

func TestPutLogsEscapeReorderingCharacters(t *testing.T) {
	// RIGHT-TO-LEFT OVERRIDE makes "a<RLO>txt.exe" display as "aexe.txt".
	rlo := string(rune(0x202E))
	h, logs := newLoggingHandler(t)
	rec := serveUpload(h, "a"+rlo+"txt.exe", "mac"+rlo+"1", strings.NewReader("x"), 1)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if strings.ContainsRune(logs.String(), 0x202E) {
		t.Errorf("logs contain a raw U+202E: %q", logs.String())
	}
	// The escaped filename and sender must still be present, not dropped.
	if want := strconv.Quote("a" + rlo + "txt.exe"); !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want the quoted filename %s", logs.String(), want)
	}
	if want := sanitize("mac"+rlo+"1") + " (192.168.68.53)"; !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want the escaped sender %q", logs.String(), want)
	}
}

// partFiles lists the temporary upload files in dir.
func partFiles(t *testing.T, dir string) []string {
	t.Helper()
	var parts []string
	for _, name := range listFiles(t, dir) {
		if strings.HasPrefix(name, ".lanxfer-") && strings.HasSuffix(name, ".part") {
			parts = append(parts, name)
		}
	}
	return parts
}

// waitFor polls cond until it holds, failing the test after 2 seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWaitUploadsCoversCleanupAfterClose(t *testing.T) {
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, 1<<20, nil, log.New(io.Discard, "", 0))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Start an upload that declares 1000 bytes but sends only a few.
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "PUT /files/big.bin HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\npartial")
	waitFor(t, func() bool { return len(partFiles(t, st.Dir)) == 1 })

	ts.CloseClientConnections() // what http.Server.Close does to live transfers
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.WaitUploads(ctx); err != nil {
		t.Fatalf("WaitUploads = %v", err)
	}
	if got := partFiles(t, st.Dir); len(got) != 0 {
		t.Errorf("left behind %v", got)
	}
}

func TestPutFailureLogEscapesErrorText(t *testing.T) {
	rlo := string(rune(0x202E))
	h, logs := newLoggingHandler(t)
	// The error text carries sender-controlled characters, as a wrapped
	// path error would.
	boom := "boom" + rlo + "txt"
	body := io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(errors.New(boom)))
	rec := serveUpload(h, "a.txt", "", body, 10)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.ContainsRune(logs.String(), 0x202E) {
		t.Errorf("logs contain a raw U+202E: %q", logs.String())
	}
	if want := sanitize(boom); !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want them to contain %q", logs.String(), want)
	}
}
