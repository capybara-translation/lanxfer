package server

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// newMessageHandler returns a server handler with an Inbox writing to the
// returned buffer and recorder. Tests call ServeHTTP directly, so the
// handler runs on the test goroutine.
func newMessageHandler(t *testing.T) (http.Handler, *bytes.Buffer, *memRecorder) {
	t.Helper()
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := &memRecorder{}
	var out bytes.Buffer
	inbox := NewInbox(rec, &out)
	inbox.now = func() time.Time { return fixedTime }
	return New(st, 1<<20, inbox, log.New(io.Discard, "", 0)).Handler(), &out, rec
}

func postMessage(h http.Handler, from, body, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(body))
	req.RemoteAddr = remote
	if from != "" {
		req.Header.Set(FromHeader, from)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMessageDelivered(t *testing.T) {
	h, out, mem := newMessageHandler(t)
	rec := postMessage(h, "mac1", "hello", "192.168.68.53:5555")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	want := "--- 2026-09-30 10:12:03 from mac1 (192.168.68.53) ---\nhello\n\n"
	if out.String() != want {
		t.Errorf("terminal = %q, want %q", out.String(), want)
	}
	if got := mem.list(); len(got) != 1 || got[0].Text != "hello" {
		t.Errorf("recorded = %+v, want one entry with text hello", got)
	}
}

func TestMessageInvalidNameShowsIPOnly(t *testing.T) {
	h, out, _ := newMessageHandler(t)
	postMessage(h, strings.Repeat("a", 65), "hi", "192.168.68.53:5555")
	if !strings.HasPrefix(out.String(), "--- 2026-09-30 10:12:03 from 192.168.68.53 ---\n") {
		t.Errorf("terminal = %q, want the IP-only header", out.String())
	}
}

func TestMessageRejected(t *testing.T) {
	tests := []struct {
		name, body string
		want       int
	}{
		{"empty", "", http.StatusBadRequest},
		{"invalid utf-8", "\xff\xfe", http.StatusBadRequest},
		{"too large", strings.Repeat("a", MaxMessageSize+1), http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		h, out, _ := newMessageHandler(t)
		rec := postMessage(h, "mac1", tt.body, "192.168.68.53:5555")
		if rec.Code != tt.want {
			t.Errorf("%s: status = %d, want %d", tt.name, rec.Code, tt.want)
		}
		if out.Len() != 0 {
			t.Errorf("%s: message was shown: %q", tt.name, out.String())
		}
	}
}

func TestMessageTooLargeSuggestsSend(t *testing.T) {
	h, _, _ := newMessageHandler(t)
	rec := postMessage(h, "", strings.Repeat("a", MaxMessageSize+1), "192.168.68.53:5555")
	if !strings.Contains(rec.Body.String(), "lanxfer send") {
		t.Errorf("body = %q, want a hint to use lanxfer send", rec.Body.String())
	}
}

func TestMessageFromGlobalIPIsForbidden(t *testing.T) {
	h, out, _ := newMessageHandler(t)
	rec := postMessage(h, "mac1", "hi", "203.0.113.5:5555")
	if rec.Code != http.StatusForbidden || out.Len() != 0 {
		t.Errorf("status = %d, shown = %q; want 403 and nothing shown", rec.Code, out.String())
	}
}

func TestMessagesDisabledWithoutInbox(t *testing.T) {
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, 1<<20, nil, log.New(io.Discard, "", 0)).Handler()
	if rec := postMessage(h, "", "hi", "192.168.68.53:5555"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// A web page open in the user's browser can make the browser send a request
// to any private address. Such requests carry an Origin header; lanxfer's own
// clients never send one.
func TestRejectsBrowserOrigin(t *testing.T) {
	const remote = "192.168.1.2:5555"
	tests := []struct {
		name         string
		method, path string
		origin       string
	}{
		{"message from a web page", http.MethodPost, "/messages", "https://evil.example"},
		{"message from a sandboxed page", http.MethodPost, "/messages", "null"},
		{"file upload from a web page", http.MethodPut, "/files/x", "https://evil.example"},
	}
	for _, tt := range tests {
		r := newRig(t, WithDeviceID(deviceID))
		w := r.do(tt.method, tt.path, "hello", reqOpts{remote: remote, origin: tt.origin})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", tt.name, w.Code)
		}
		if got := w.Header().Get(IDHeader); got != deviceID {
			t.Errorf("%s: %s = %q, want %q", tt.name, IDHeader, got, deviceID)
		}
		if r.out.Len() != 0 {
			t.Errorf("%s: message was shown: %q", tt.name, r.out.String())
		}
		if got := r.rec.list(); len(got) != 0 {
			t.Errorf("%s: recorded %+v, want nothing", tt.name, got)
		}
		if entries, err := os.ReadDir(r.storage.Dir); err != nil || len(entries) != 0 {
			t.Errorf("%s: storage = %v (err %v), want empty", tt.name, entries, err)
		}
		if !strings.Contains(r.logs.String(), "rejected browser request") {
			t.Errorf("%s: log = %q, want a rejection line", tt.name, r.logs.String())
		}
	}

	// An Origin header with an empty value still marks a browser request.
	r := newRig(t)
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader("hello"))
	req.RemoteAddr = remote
	req.Header["Origin"] = []string{""}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("empty Origin: status = %d, want 403", w.Code)
	}

	// The same message without Origin is delivered.
	r = newRig(t)
	if w := r.do(http.MethodPost, "/messages", "hello", reqOpts{remote: remote}); w.Code != http.StatusNoContent {
		t.Errorf("without Origin: status = %d, want 204", w.Code)
	}
	if r.out.Len() == 0 {
		t.Error("without Origin: message was not shown")
	}
}
