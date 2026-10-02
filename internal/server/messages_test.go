package server

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newMessageHandler returns a server handler with an Inbox writing to the
// returned buffer and log path. Tests call ServeHTTP directly, so the
// handler runs on the test goroutine.
func newMessageHandler(t *testing.T) (http.Handler, *bytes.Buffer, string) {
	t.Helper()
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(st.Dir, "messages.log")
	var out bytes.Buffer
	inbox := NewInbox(logPath, &out)
	inbox.now = func() time.Time { return fixedTime }
	return New(st, 1<<20, inbox, log.New(io.Discard, "", 0)).Handler(), &out, logPath
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
	h, out, logPath := newMessageHandler(t)
	rec := postMessage(h, "mac1", "hello", "192.168.68.53:5555")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	want := "--- 2026-09-30 10:12:03 from mac1 (192.168.68.53) ---\nhello\n\n"
	if out.String() != want {
		t.Errorf("terminal = %q, want %q", out.String(), want)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("log = %q, want %q", data, want)
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
