package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// failingReader fails the test if messageText reads from it.
type failingReader struct{ t *testing.T }

func (r failingReader) Read([]byte) (int, error) {
	r.t.Error("stdin was read")
	return 0, errors.New("unexpected read")
}

func TestMessageTextFromArgs(t *testing.T) {
	got, err := messageText([]string{"hello", "world"}, failingReader{t}, false)
	if err != nil || got != "hello world" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestMessageTextFromStdin(t *testing.T) {
	got, err := messageText(nil, strings.NewReader("line1\nline2\n"), false)
	if err != nil || got != "line1\nline2\n" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestMessageTextRefusesTerminal(t *testing.T) {
	_, err := messageText(nil, failingReader{t}, true)
	if !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}

func TestMessageTextRejects(t *testing.T) {
	tests := map[string]string{
		"empty":        "",
		"invalid utf8": "\xff",
		"too large":    strings.Repeat("a", server.MaxMessageSize+1),
	}
	for name, in := range tests {
		if _, err := messageText(nil, strings.NewReader(in), false); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestMessageTextAtLimit(t *testing.T) {
	in := strings.Repeat("a", server.MaxMessageSize)
	got, err := messageText(nil, strings.NewReader(in), false)
	if err != nil || len(got) != len(in) {
		t.Errorf("len = %d, err = %v", len(got), err)
	}
}

func TestRunSayMissingTarget(t *testing.T) {
	if err := runSay(nil); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}

// serverPort returns the TCP port of a httptest server.
func serverPort(t *testing.T, ts *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestRunSayRecordsHistory(t *testing.T) {
	stubConfigDir(t)
	var gotFromID string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFromID = r.Header.Get(server.FromIDHeader)
		w.Header().Set(server.IDHeader, idMac2)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ts.Close)
	port := serverPort(t, ts)
	got, recordPort := stubLocalRecord(t, nil)

	if err := runSay([]string{"--port", strconv.Itoa(port), "127.0.0.1", "hello", "there"}); err != nil {
		t.Fatal(err)
	}

	if !identity.Valid(gotFromID) {
		t.Errorf("sender ID header = %q, want a device ID", gotFromID)
	}
	if *recordPort != port || len(*got) != 1 {
		t.Fatalf("recorded %d entries on port %d", len(*got), *recordPort)
	}
	e := (*got)[0]
	want := history.Entry{
		Dir: history.Out, PeerID: idMac2, PeerAddr: "127.0.0.1",
		Kind: history.KindText, Text: "hello there", Status: history.StatusOK,
	}
	if e != want {
		t.Errorf("got  %+v\nwant %+v", e, want)
	}
}

func TestRunSayRecordsFailure(t *testing.T) {
	stubConfigDir(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	got, _ := stubLocalRecord(t, nil)

	err := runSay([]string{"--port", strconv.Itoa(serverPort(t, ts)), "127.0.0.1", "hi"})
	if err == nil {
		t.Fatal("want an error")
	}
	if len(*got) != 1 || (*got)[0].Status != history.StatusFailed || !strings.Contains((*got)[0].Error, "nope") {
		t.Errorf("recorded %+v", *got)
	}
}

func TestRunSayByNameRecordsPeer(t *testing.T) {
	stubConfigDir(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent) // no X-Lanxfer-Id: an older receiver
	}))
	t.Cleanup(ts.Close)
	port := serverPort(t, ts)
	p := onlinePeer("mac2", idMac2, "127.0.0.1:"+strconv.Itoa(port))
	stubLocalPeers(t, []presence.Peer{p}, nil)
	got, _ := stubLocalRecord(t, nil)

	if err := runSay([]string{"--port", strconv.Itoa(port), "mac2", "hi"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].PeerID != idMac2 || (*got)[0].PeerName != "mac2" {
		t.Errorf("recorded %+v", *got)
	}
}

func TestRunSayResolveFailureIsNotRecorded(t *testing.T) {
	stubConfigDir(t)
	stubDiscover(t, nil, nil)
	got, _ := stubLocalRecord(t, nil)
	if err := runSay([]string{"nobody", "hi"}); err == nil {
		t.Fatal("want an error")
	}
	if len(*got) != 0 {
		t.Errorf("recorded %+v for an unresolved target", *got)
	}
}
