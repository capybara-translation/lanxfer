package server

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/history"
)

// notices collects the entries passed to WithNotify.
type notices struct {
	mu  sync.Mutex
	got []history.Entry
}

func (n *notices) add(e history.Entry) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, e)
}

func (n *notices) list() []history.Entry {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]history.Entry(nil), n.got...)
}

func TestNotifyReportsFileAndFolder(t *testing.T) {
	var n notices
	r := newRig(t, WithNotify(n.add))
	before := time.Now()
	if w := r.do(http.MethodPut, "/files/a.txt", "hello", reqOpts{from: "mac1", fromID: testID}); w.Code != http.StatusCreated {
		t.Fatalf("file status = %d", w.Code)
	}
	tarData := buildTar(t, tarDir("trip"), tarFile("trip/a.txt", "12345"))
	if w := r.do(http.MethodPut, "/dirs/trip", string(tarData), reqOpts{from: "mac1", fromID: testID}); w.Code != http.StatusCreated {
		t.Fatalf("folder status = %d", w.Code)
	}

	got, recorded := n.list(), r.rec.list()
	if len(got) != 2 || len(recorded) != 2 {
		t.Fatalf("notified %d, recorded %d; want 2 and 2", len(got), len(recorded))
	}
	for i, e := range got {
		if e.Time.Before(before) || e.V != history.Version {
			t.Errorf("entry %d: Time = %v, V = %d; want a fresh time and V = %d", i, e.Time, e.V, history.Version)
		}
		// The notified entry is the recorded one, apart from V which the
		// recorder itself sets.
		want := recorded[i]
		want.V = history.Version
		if e != want {
			t.Errorf("entry %d = %+v\nwant     %+v", i, e, want)
		}
	}
	if got[0].Kind != history.KindFile || got[0].SavedAs != "a.txt" || got[0].PeerName != "mac1" || got[0].PeerID != testID {
		t.Errorf("file entry = %+v", got[0])
	}
	if got[1].Kind != history.KindFolder || got[1].Files != 1 || got[1].Path != filepath.Join(r.storage.Dir, "trip") {
		t.Errorf("folder entry = %+v", got[1])
	}
}

func TestNotifyReportsReceiptWithoutHistory(t *testing.T) {
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var n notices
	var out bytes.Buffer
	inbox := NewInbox(nil, &out)
	h := New(st, 1<<20, inbox, log.New(io.Discard, "", 0), WithNotify(n.add)).Handler()

	if rec := serveUpload(h, "a.txt", "mac1", bytes.NewReader([]byte("hi")), 2); rec.Code != http.StatusCreated {
		t.Fatalf("file status = %d", rec.Code)
	}
	if rec := postMessage(h, "mac1", "hello", "192.168.68.53:5555"); rec.Code != http.StatusNoContent {
		t.Fatalf("message status = %d", rec.Code)
	}
	got := n.list()
	if len(got) != 2 {
		t.Fatalf("notified %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Kind != history.KindFile || got[0].Time.IsZero() || got[0].Status != history.StatusOK || got[0].PeerAddr != "192.168.68.53" {
		t.Errorf("file entry = %+v", got[0])
	}
	if got[1].Kind != history.KindText || got[1].Text != "hello" || got[1].Time.IsZero() || got[1].V != history.Version {
		t.Errorf("text entry = %+v", got[1])
	}
	if out.Len() == 0 {
		t.Error("text was not printed")
	}
}

func TestNotifyStillReportsFilesWhenHistoryFails(t *testing.T) {
	var n notices
	r := newRig(t, WithNotify(n.add))
	r.rec.err = errors.New("disk full")
	for _, tt := range []struct{ name, path, body string }{
		{"file", "/files/a.txt", "hello"},
		{"folder", "/dirs/trip", string(buildTar(t, tarFile("trip/a.txt", "x")))},
	} {
		if w := r.do(http.MethodPut, tt.path, tt.body, reqOpts{fromID: testID}); w.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d", tt.name, w.Code)
		}
	}
	if got := n.list(); len(got) != 2 {
		t.Errorf("notified %d entries, want 2: %+v", len(got), got)
	}
}

func TestNotifyIgnoresTextThatFailsToRecord(t *testing.T) {
	var n notices
	r := newRig(t, WithNotify(n.add))
	r.rec.err = errors.New("disk full")
	if w := r.do(http.MethodPost, "/messages", "hello", reqOpts{fromID: testID}); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := n.list(); len(got) != 0 {
		t.Errorf("notified %+v for a text that was not received", got)
	}
}

func TestNotifyIgnoresFailedUploads(t *testing.T) {
	var n notices
	r := newRig(t, WithNotify(n.add))
	r.do(http.MethodPut, "/dirs/trip", "not a tar stream", reqOpts{fromID: testID})
	r.do(http.MethodPut, "/files/..%2Fx", "x", reqOpts{fromID: testID})
	r.do(http.MethodPost, "/messages", "", reqOpts{fromID: testID})
	if got := n.list(); len(got) != 0 {
		t.Errorf("notified %+v, want nothing", got)
	}
}

func TestNotifyTextEntryMatchesRecord(t *testing.T) {
	var n notices
	r := newRig(t, WithNotify(n.add))
	if w := r.do(http.MethodPost, "/messages", "hello", reqOpts{from: "mac1", fromID: testID}); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	want := history.Entry{
		V: history.Version, Time: fixedTime, Dir: history.In, Kind: history.KindText, Text: "hello",
		PeerID: testID, PeerName: "mac1", PeerAddr: "192.168.68.53", Status: history.StatusOK,
	}
	if got := n.list(); len(got) != 1 || got[0] != want {
		t.Errorf("notified = %+v, want [%+v]", got, want)
	}
}

func TestNotifyTextRunsOutsideInboxLock(t *testing.T) {
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inbox := NewInbox(&memRecorder{}, io.Discard)
	locked := make(chan bool, 1)
	h := New(st, 1<<20, inbox, log.New(io.Discard, "", 0), WithNotify(func(history.Entry) {
		if inbox.mu.TryLock() {
			inbox.mu.Unlock()
			locked <- false
		} else {
			locked <- true
		}
	})).Handler()
	if rec := postMessage(h, "mac1", "hello", "192.168.68.53:5555"); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if <-locked {
		t.Error("notify was called while the inbox lock was held")
	}
}
