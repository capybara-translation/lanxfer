package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

const (
	testID      = "0123456789abcdef0123456789abcdef"
	deviceID    = "fedcba9876543210fedcba9876543210"
	lanRemote   = "192.168.68.53:5555"
	localRemote = "127.0.0.1:5555"
)

// memRecorder is an in-memory Recorder. A non-nil err makes every Append fail.
type memRecorder struct {
	mu      sync.Mutex
	entries []history.Entry
	err     error
}

func (m *memRecorder) Append(e history.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, e)
	return nil
}

func (m *memRecorder) list() []history.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]history.Entry(nil), m.entries...)
}

// testRig is a server wired to in-memory collaborators. Tests call
// ServeHTTP directly so RemoteAddr can be chosen freely.
type testRig struct {
	h       http.Handler
	storage *Storage
	rec     *memRecorder
	out     *bytes.Buffer // what the inbox printed
	logs    *bytes.Buffer // what the server logged
}

func newRig(t *testing.T, opts ...Option) *testRig {
	t.Helper()
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &testRig{storage: st, rec: &memRecorder{}, out: &bytes.Buffer{}, logs: &bytes.Buffer{}}
	inbox := NewInbox(r.rec, r.out)
	inbox.now = func() time.Time { return fixedTime }
	opts = append([]Option{WithHistory(r.rec)}, opts...)
	r.h = New(st, 1<<20, inbox, log.New(r.logs, "", 0), opts...).Handler()
	return r
}

type reqOpts struct {
	remote      string
	from        string
	fromID      string
	host        string // default "127.0.0.1:8425"
	origin      string
	contentType string // default application/json for POST /local/history
}

func (r *testRig) do(method, target, body string, o reqOpts) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = o.remote
	if req.RemoteAddr == "" {
		req.RemoteAddr = lanRemote
	}
	req.Host = "127.0.0.1:8425"
	if o.host != "" {
		req.Host = o.host
	}
	if o.origin != "" {
		req.Header.Set("Origin", o.origin)
	}
	switch {
	case o.contentType != "":
		req.Header.Set("Content-Type", o.contentType)
	case method == http.MethodPost && target == "/local/history":
		req.Header.Set("Content-Type", "application/json")
	}
	if o.from != "" {
		req.Header.Set(FromHeader, o.from)
	}
	if o.fromID != "" {
		req.Header.Set(FromIDHeader, o.fromID)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func TestResponsesCarryDeviceID(t *testing.T) {
	r := newRig(t, WithDeviceID(deviceID))
	tests := []struct {
		name         string
		method, path string
		body         string
		remote       string
		wantStatus   int
	}{
		{"file upload", http.MethodPut, "/files/a.txt", "hi", lanRemote, http.StatusCreated},
		{"message", http.MethodPost, "/messages", "hi", lanRemote, http.StatusNoContent},
		{"unknown path", http.MethodGet, "/nope", "", lanRemote, http.StatusNotFound},
		{"bad name", http.MethodPut, "/files/..%2Fx", "hi", lanRemote, http.StatusBadRequest},
	}
	for _, tt := range tests {
		w := r.do(tt.method, tt.path, tt.body, reqOpts{remote: tt.remote})
		if w.Code != tt.wantStatus {
			t.Errorf("%s: status = %d, want %d", tt.name, w.Code, tt.wantStatus)
		}
		if got := w.Header().Get(IDHeader); got != deviceID {
			t.Errorf("%s: %s = %q, want %q", tt.name, IDHeader, got, deviceID)
		}
	}
}

func TestRejectedAddressLearnsNoDeviceID(t *testing.T) {
	r := newRig(t, WithDeviceID(deviceID))
	w := r.do(http.MethodPut, "/files/b.txt", "hi", reqOpts{remote: "203.0.113.5:5555"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if got := w.Header().Get(IDHeader); got != "" {
		t.Errorf("%s = %q, want none for a public source address", IDHeader, got)
	}
	// An allowed address that sends an Origin header is refused too, but it
	// is a LAN or local caller and still learns who answered.
	w = r.do(http.MethodPut, "/files/b.txt", "hi", reqOpts{origin: "https://evil.example"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("Origin: status = %d, want 403", w.Code)
	}
	if got := w.Header().Get(IDHeader); got != deviceID {
		t.Errorf("Origin rejection: %s = %q, want %q", IDHeader, got, deviceID)
	}
}

func TestNoDeviceIDHeaderWithoutOption(t *testing.T) {
	r := newRig(t)
	w := r.do(http.MethodPut, "/files/a.txt", "hi", reqOpts{})
	if got := w.Header().Get(IDHeader); got != "" {
		t.Errorf("%s = %q, want none", IDHeader, got)
	}
}

func TestPutRecordsHistory(t *testing.T) {
	tests := []struct {
		name               string
		from, fromID       string
		wantID, wantPeerNm string
	}{
		{"valid id and name", "mac1", testID, testID, "mac1"},
		{"no id", "mac1", "", "", "mac1"},
		{"malformed id", "mac1", "NOT-AN-ID", "", "mac1"},
		{"invalid name", strings.Repeat("a", 65), testID, testID, ""},
		{"no name", "", testID, testID, ""},
	}
	for _, tt := range tests {
		t.Run("file/"+tt.name, func(t *testing.T) {
			r := newRig(t)
			r.do(http.MethodPut, "/files/a.txt", "old", reqOpts{})
			r.rec.entries = nil
			w := r.do(http.MethodPut, "/files/a.txt", "hello", reqOpts{from: tt.from, fromID: tt.fromID})
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201", w.Code)
			}
			got := r.rec.list()
			if len(got) != 1 {
				t.Fatalf("recorded %d entries, want 1", len(got))
			}
			want := history.Entry{
				Dir: history.In, Kind: history.KindFile, PeerID: tt.wantID, PeerName: tt.wantPeerNm,
				PeerAddr: "192.168.68.53", Name: "a.txt", SavedAs: "a (1).txt",
				Path: filepath.Join(r.storage.Dir, "a (1).txt"), Size: 5, Status: history.StatusOK,
			}
			// The server fixes the time so the history and the notification agree.
			if got[0].Time.IsZero() {
				t.Error("entry has no time")
			}
			got[0].Time = time.Time{}
			if got[0] != want {
				t.Errorf("entry = %+v\nwant    %+v", got[0], want)
			}
		})
		t.Run("folder/"+tt.name, func(t *testing.T) {
			r := newRig(t)
			tarData := buildTar(t, tarDir("trip"), tarFile("trip/a.txt", "12345"), tarFile("trip/b.txt", "123"))
			w := r.do(http.MethodPut, "/dirs/trip", string(tarData), reqOpts{from: tt.from, fromID: tt.fromID})
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body)
			}
			got := r.rec.list()
			if len(got) != 1 {
				t.Fatalf("recorded %d entries, want 1", len(got))
			}
			want := history.Entry{
				Dir: history.In, Kind: history.KindFolder, PeerID: tt.wantID, PeerName: tt.wantPeerNm,
				PeerAddr: "192.168.68.53", Name: "trip", SavedAs: "trip",
				Path: filepath.Join(r.storage.Dir, "trip"), Size: 8, Files: 2, Status: history.StatusOK,
			}
			// The server fixes the time so the history and the notification agree.
			if got[0].Time.IsZero() {
				t.Error("entry has no time")
			}
			got[0].Time = time.Time{}
			if got[0] != want {
				t.Errorf("entry = %+v\nwant    %+v", got[0], want)
			}
		})
	}
}

func TestPutRecordsNothingOnFailure(t *testing.T) {
	r := newRig(t)
	w := r.do(http.MethodPut, "/dirs/trip", "not a tar stream", reqOpts{fromID: testID})
	if w.Code == http.StatusCreated {
		t.Fatalf("status = %d, want an error", w.Code)
	}
	r.do(http.MethodPut, "/files/..%2Fx", "x", reqOpts{fromID: testID})
	if got := r.rec.list(); len(got) != 0 {
		t.Errorf("recorded %+v, want nothing", got)
	}
}

func TestMessageRecordsBeforePrinting(t *testing.T) {
	r := newRig(t)
	r.rec.err = errors.New("disk full")
	w := r.do(http.MethodPost, "/messages", "hello", reqOpts{from: "mac1", fromID: testID})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if r.out.Len() != 0 {
		t.Errorf("terminal got %q, want nothing", r.out.String())
	}
}

func TestMessageRecordsSender(t *testing.T) {
	r := newRig(t)
	w := r.do(http.MethodPost, "/messages", "hello", reqOpts{from: "mac1", fromID: testID})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	want := history.Entry{
		Time: fixedTime, Dir: history.In, Kind: history.KindText, Text: "hello", PeerID: testID,
		PeerName: "mac1", PeerAddr: "192.168.68.53", Status: history.StatusOK,
	}
	if got := r.rec.list(); len(got) != 1 || got[0] != want {
		t.Errorf("recorded = %+v, want [%+v]", got, want)
	}
}

func TestUploadSucceedsWhenHistoryFails(t *testing.T) {
	r := newRig(t)
	r.rec.err = errors.New("disk full")
	for _, tt := range []struct{ name, path, body string }{
		{"file", "/files/a.txt", "hello"},
		{"folder", "/dirs/trip", string(buildTar(t, tarFile("trip/a.txt", "x")))},
	} {
		r.logs.Reset()
		w := r.do(http.MethodPut, tt.path, tt.body, reqOpts{fromID: testID})
		if w.Code != http.StatusCreated {
			t.Errorf("%s: status = %d, want 201", tt.name, w.Code)
		}
		if !strings.Contains(r.logs.String(), "warning: could not record") {
			t.Errorf("%s: log = %q, want a warning", tt.name, r.logs.String())
		}
	}
}

func TestObserveCalledWithSender(t *testing.T) {
	var got []presence.Sighting
	observe := func(s presence.Sighting) { got = append(got, s) }
	r := newRig(t, WithPresence(observe, nil))
	opts := reqOpts{from: "mac1", fromID: testID}

	r.do(http.MethodPut, "/files/a.txt", "x", opts)
	r.do(http.MethodPut, "/dirs/trip", string(buildTar(t, tarFile("trip/a.txt", "x"))), opts)
	r.do(http.MethodPost, "/messages", "hi", opts)
	want := presence.Sighting{ID: testID, Name: "mac1", Addr: netip.MustParseAddrPort("192.168.68.53:0")}
	if len(got) != 3 {
		t.Fatalf("observed %d sightings, want 3", len(got))
	}
	for i, s := range got {
		if s != want {
			t.Errorf("sighting %d = %+v, want %+v", i, s, want)
		}
	}

	got = nil
	r.do(http.MethodPut, "/files/b.txt", "x", reqOpts{from: "mac1"})                  // no ID
	r.do(http.MethodPut, "/files/c.txt", "x", reqOpts{from: "mac1", fromID: "bogus"}) // malformed ID
	r.do(http.MethodPut, "/files/..%2Fd", "x", opts)                                  // rejected
	r.do(http.MethodPost, "/messages", "", opts)                                      // rejected
	r.do(http.MethodPut, "/files/e.txt", "x", reqOpts{fromID: testID, from: strings.Repeat("a", 65)})
	if len(got) != 1 || got[0].ID != testID || got[0].Name != "" {
		t.Errorf("sightings = %+v, want only the one with an unusable name and an empty Name", got)
	}
}

func TestLocalPeersLoopbackOnly(t *testing.T) {
	peers := []presence.Peer{{
		ID: testID, Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.60:8425"),
		LastSeen: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC), Online: true,
	}}
	r := newRig(t, WithPresence(func(presence.Sighting) {}, func() []presence.Peer { return peers }))

	if w := r.do(http.MethodGet, "/local/peers", "", reqOpts{remote: "192.168.1.2:4000"}); w.Code != http.StatusForbidden {
		t.Errorf("LAN status = %d, want 403", w.Code)
	}
	for _, remote := range []string{"127.0.0.1:4000", "[::1]:4000"} {
		w := r.do(http.MethodGet, "/local/peers", "", reqOpts{remote: remote})
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", remote, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type = %q, want application/json", remote, ct)
		}
		var got []presence.Peer
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: body %q: %v", remote, w.Body, err)
		}
		if len(got) != 1 || got[0] != peers[0] {
			t.Errorf("%s: peers = %+v, want %+v", remote, got, peers)
		}
	}
}

func TestLocalPeersEmptyIsArray(t *testing.T) {
	for name, f := range map[string]func() []presence.Peer{
		"nil":   func() []presence.Peer { return nil },
		"empty": func() []presence.Peer { return []presence.Peer{} },
	} {
		r := newRig(t, WithPresence(func(presence.Sighting) {}, f))
		w := r.do(http.MethodGet, "/local/peers", "", reqOpts{remote: localRemote})
		if got := strings.TrimSpace(w.Body.String()); got != "[]" {
			t.Errorf("%s: body = %q, want []", name, got)
		}
	}
}

func TestLocalPeersAbsentWithoutPresence(t *testing.T) {
	r := newRig(t)
	if w := r.do(http.MethodGet, "/local/peers", "", reqOpts{remote: localRemote}); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestLocalHistory(t *testing.T) {
	valid := `{"v":1,"dir":"out","kind":"text","text":"hi","peer_id":"` + testID + `","status":"ok","future_field":1}`
	tests := []struct {
		name, body, remote string
		wantStatus         int
		wantRecorded       int
	}{
		{"valid", valid, localRemote, http.StatusNoContent, 1},
		{"valid from IPv6 loopback", valid, "[::1]:4000", http.StatusNoContent, 1},
		{"version omitted", `{"dir":"out","kind":"file","name":"a","status":"failed","error":"boom"}`, localRemote, http.StatusNoContent, 1},
		{"broken JSON", `{"v":1,`, localRemote, http.StatusBadRequest, 0},
		{"trailing data", valid + `{}`, localRemote, http.StatusBadRequest, 0},
		{"fails validation", `{"v":1,"dir":"sideways","kind":"text","status":"ok"}`, localRemote, http.StatusBadRequest, 0},
		{"received entry is refused", `{"v":1,"dir":"in","kind":"text","text":"forged","status":"ok"}`, localRemote, http.StatusBadRequest, 0},
		{"wrong version", `{"v":2,"dir":"out","kind":"text","status":"ok"}`, localRemote, http.StatusBadRequest, 0},
		{"not loopback", valid, "192.168.1.2:4000", http.StatusForbidden, 0},
		{"too large", `{"v":1,"dir":"out","kind":"text","status":"ok","error":"` + strings.Repeat("a", maxLocalHistoryBody) + `"}`, localRemote, http.StatusRequestEntityTooLarge, 0},
	}
	for _, tt := range tests {
		r := newRig(t)
		w := r.do(http.MethodPost, "/local/history", tt.body, reqOpts{remote: tt.remote})
		if w.Code != tt.wantStatus {
			t.Errorf("%s: status = %d, want %d (%s)", tt.name, w.Code, tt.wantStatus, w.Body)
		}
		if got := len(r.rec.list()); got != tt.wantRecorded {
			t.Errorf("%s: recorded %d entries, want %d", tt.name, got, tt.wantRecorded)
		}
	}
}

func TestLocalHistoryValidationErrorIsReported(t *testing.T) {
	r := newRig(t)
	w := r.do(http.MethodPost, "/local/history", `{"v":1,"dir":"sideways","kind":"text","status":"ok"}`, reqOpts{remote: localRemote})
	if !strings.Contains(w.Body.String(), `invalid dir "sideways"`) {
		t.Errorf("body = %q, want the validation error", w.Body)
	}
}

func TestLocalHistoryRecordsEntry(t *testing.T) {
	r := newRig(t)
	body := `{"dir":"out","kind":"folder","peer_name":"mac2","name":"trip","files":3,"size":42,"status":"cancelled"}`
	if w := r.do(http.MethodPost, "/local/history", body, reqOpts{remote: localRemote}); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	want := history.Entry{
		V: 1, Dir: history.Out, Kind: history.KindFolder, PeerName: "mac2", Name: "trip",
		Files: 3, Size: 42, Status: history.StatusCancelled,
	}
	if got := r.rec.list(); len(got) != 1 || got[0] != want {
		t.Errorf("recorded = %+v, want [%+v]", got, want)
	}
}

func TestLocalHistoryAppendFailureIs500(t *testing.T) {
	r := newRig(t)
	r.rec.err = errors.New("disk full")
	body := `{"v":1,"dir":"out","kind":"text","status":"ok"}`
	if w := r.do(http.MethodPost, "/local/history", body, reqOpts{remote: localRemote}); w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestLocalHistoryAbsentWithoutHistory(t *testing.T) {
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, 1<<20, nil, log.New(io.Discard, "", 0)).Handler()
	req := httptest.NewRequest(http.MethodPost, "/local/history", strings.NewReader(`{}`))
	req.RemoteAddr = localRemote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestLocalHistoryFromGlobalIPIsForbidden(t *testing.T) {
	r := newRig(t)
	body := `{"v":1,"dir":"out","kind":"text","status":"ok"}`
	if w := r.do(http.MethodPost, "/local/history", body, reqOpts{remote: "203.0.113.5:1"}); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestLocalAPIRejectsBrowserRequests(t *testing.T) {
	r := newRig(t, WithPresence(func(presence.Sighting) {}, func() []presence.Peer { return nil }))
	hist := `{"v":1,"dir":"out","kind":"text","status":"ok"}`
	routes := []struct{ name, method, path, body string }{
		{"history", http.MethodPost, "/local/history", hist},
		{"peers", http.MethodGet, "/local/peers", ""},
	}
	for _, rt := range routes {
		for name, o := range map[string]reqOpts{
			"origin present":       {remote: localRemote, origin: "https://evil.example"},
			"null origin":          {remote: localRemote, origin: "null"},
			"foreign host":         {remote: localRemote, host: "evil.example:8425"},
			"foreign host no port": {remote: localRemote, host: "evil.example"},
			"LAN IP host":          {remote: localRemote, host: "192.168.1.2:8425"},
		} {
			if w := r.do(rt.method, rt.path, rt.body, o); w.Code != http.StatusForbidden {
				t.Errorf("%s/%s: status = %d, want 403", rt.name, name, w.Code)
			}
		}
		for _, host := range []string{"localhost:8425", "LOCALHOST", "127.0.0.1:8425", "127.0.0.2", "[::1]:8425", "[::1]", "[::ffff:127.0.0.1]:8425"} {
			if w := r.do(rt.method, rt.path, rt.body, reqOpts{remote: localRemote, host: host}); w.Code >= 300 {
				t.Errorf("%s: Host %q: status = %d, want success", rt.name, host, w.Code)
			}
		}
	}
	if got := len(r.rec.list()); got != 7 {
		t.Errorf("recorded %d entries, want only the 7 allowed requests", got)
	}
}

func TestLocalHistoryRequiresJSONContentType(t *testing.T) {
	body := `{"v":1,"dir":"out","kind":"text","status":"ok"}`
	tests := []struct {
		contentType string
		want        int
	}{
		{"text/plain", http.StatusUnsupportedMediaType},
		{"text/plain;charset=UTF-8", http.StatusUnsupportedMediaType},
		{"application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"application/jsonx", http.StatusUnsupportedMediaType},
		{"application/json", http.StatusNoContent},
		{"application/json; charset=utf-8", http.StatusNoContent},
		{"Application/JSON", http.StatusNoContent},
	}
	for _, tt := range tests {
		r := newRig(t)
		w := r.do(http.MethodPost, "/local/history", body, reqOpts{remote: localRemote, contentType: tt.contentType})
		if w.Code != tt.want {
			t.Errorf("Content-Type %q: status = %d, want %d", tt.contentType, w.Code, tt.want)
		}
	}
	// No Content-Type header at all.
	r := newRig(t)
	req := httptest.NewRequest(http.MethodPost, "/local/history", strings.NewReader(body))
	req.RemoteAddr, req.Host = localRemote, "127.0.0.1:8425"
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("no Content-Type: status = %d, want 415", w.Code)
	}
}

func TestLocalHistoryRequiresMatchingDeviceID(t *testing.T) {
	body := `{"v":1,"dir":"out","kind":"text","status":"ok"}`
	tests := []struct {
		name         string
		serverID     string // "" starts the server without a device ID
		fromID       string
		wantStatus   int
		wantRecorded int
	}{
		{"same ID", deviceID, deviceID, http.StatusNoContent, 1},
		{"other receiver's ID", deviceID, testID, http.StatusConflict, 0},
		{"no ID from client", deviceID, "", http.StatusConflict, 0},
		{"malformed ID from client", deviceID, "bogus", http.StatusConflict, 0},
		{"ID-less server and ID-less client", "", "", http.StatusNoContent, 1},
		{"ID-less server and client with ID", "", testID, http.StatusConflict, 0},
	}
	for _, tt := range tests {
		var opts []Option
		if tt.serverID != "" {
			opts = append(opts, WithDeviceID(tt.serverID))
		}
		r := newRig(t, opts...)
		w := r.do(http.MethodPost, "/local/history", body, reqOpts{remote: localRemote, fromID: tt.fromID})
		if w.Code != tt.wantStatus {
			t.Errorf("%s: status = %d, want %d (%s)", tt.name, w.Code, tt.wantStatus, w.Body)
		}
		if got := len(r.rec.list()); got != tt.wantRecorded {
			t.Errorf("%s: recorded %d entries, want %d", tt.name, got, tt.wantRecorded)
		}
	}
}

func TestLocalHistoryAcceptsMaxTextWithHeavyEscaping(t *testing.T) {
	// "<" is written as a 6-byte escape by encoding/json, so this body is
	// about 6 MiB although the text is exactly MaxText bytes.
	e := history.Entry{V: 1, Dir: history.Out, Kind: history.KindText, Status: history.StatusOK,
		Text: strings.Repeat("<", history.MaxText)}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 2<<20 {
		t.Fatalf("body is only %d bytes; the test no longer exercises escaping", len(body))
	}
	r := newRig(t)
	if w := r.do(http.MethodPost, "/local/history", string(body), reqOpts{remote: localRemote}); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body)
	}
	if got := r.rec.list(); len(got) != 1 || len(got[0].Text) != history.MaxText {
		t.Errorf("recorded %d entries, want one with the full text", len(got))
	}
}
