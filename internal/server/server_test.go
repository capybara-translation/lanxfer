package server

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer starts a test HTTP server saving into a temp directory.
func newTestServer(t *testing.T, maxSize int64) (*httptest.Server, *Storage) {
	t.Helper()
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, maxSize, log.New(io.Discard, "", 0))
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
	srv := New(st, 1<<20, log.New(io.Discard, "", 0))
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
