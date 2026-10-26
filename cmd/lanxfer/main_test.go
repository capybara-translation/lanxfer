package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/server"
)

func TestRunSendMissingArgsIsUsageError(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"192.168.1.10"},
		{"192.168.1.10", "a.txt", "extra"},
	} {
		err := runSend(args)
		if !errors.Is(err, errUsage) {
			t.Errorf("runSend(%q) = %v, want errUsage", args, err)
		}
	}
}

func TestRunSendBadNameIsUsageError(t *testing.T) {
	err := runSend([]string{"--name", "bad\tname", "192.168.1.10", "a.txt"})
	if !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}

func TestRunSendChecksPathBeforeLookup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot create a file named CON")
	}
	calls := stubDiscover(t, nil, errors.New("must not be called"))
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "CON"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), bad} {
		if err := runSend([]string{"mac2", path}); err == nil {
			t.Errorf("runSend(%q): want an error", path)
		}
	}
	if *calls != 0 {
		t.Errorf("discovery called %d times; path problems must be reported first", *calls)
	}
}

// startReceiver runs a real receiver handler saving into a temp folder and
// returns its port.
func startReceiver(t *testing.T) int {
	t.Helper()
	st, err := server.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(st, 1<<20, nil, log.New(io.Discard, "", 0)).Handler())
	t.Cleanup(ts.Close)
	return serverPort(t, ts)
}

func TestRunSendRecordsFile(t *testing.T) {
	stubConfigDir(t)
	port := startReceiver(t)
	src := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, recordPort := stubLocalRecord(t, nil)

	if err := runSend([]string{"--port", strconv.Itoa(port), "127.0.0.1", src}); err != nil {
		t.Fatal(err)
	}

	if *recordPort != port || len(*got) != 1 {
		t.Fatalf("recorded %d entries on port %d", len(*got), *recordPort)
	}
	e := (*got)[0]
	want := history.Entry{
		Dir: history.Out, PeerAddr: "127.0.0.1", Kind: history.KindFile,
		Name: "a.txt", SavedAs: "a.txt", Path: src, Size: 5, Status: history.StatusOK,
	}
	if e != want {
		t.Errorf("got  %+v\nwant %+v", e, want)
	}
}

func TestRunSendRecordsFolder(t *testing.T) {
	stubConfigDir(t)
	port := startReceiver(t)
	src := filepath.Join(t.TempDir(), "trip")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"a.txt": "12345", "sub/b.txt": "123"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := stubLocalRecord(t, nil)

	if err := runSend([]string{"--port", strconv.Itoa(port), "127.0.0.1", src}); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 1 {
		t.Fatalf("recorded %d entries", len(*got))
	}
	e := (*got)[0]
	if e.Kind != history.KindFolder || e.Name != "trip" || e.SavedAs != "trip" || e.Path != src ||
		e.Files != 2 || e.Size != 8 || e.Status != history.StatusOK {
		t.Errorf("got %+v", e)
	}
}

func TestRunSendRecordsFailure(t *testing.T) {
	stubConfigDir(t)
	src := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := stubLocalRecord(t, nil)

	// A port that was just released, so the connection is refused.
	closed := httptest.NewServer(nil)
	closedPort := serverPort(t, closed)
	closed.Close()

	if err := runSend([]string{"--port", strconv.Itoa(closedPort), "127.0.0.1", src}); err == nil {
		t.Fatal("want an error")
	}
	if len(*got) != 1 || (*got)[0].Status != history.StatusFailed || (*got)[0].Error == "" || (*got)[0].SavedAs != "" {
		t.Errorf("recorded %+v", *got)
	}
}

func TestRunSendResolveFailureIsNotRecorded(t *testing.T) {
	stubConfigDir(t)
	stubDiscover(t, nil, nil)
	src := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := stubLocalRecord(t, nil)
	if err := runSend([]string{"nobody", src}); err == nil {
		t.Fatal("want an error")
	}
	if len(*got) != 0 {
		t.Errorf("recorded %+v for an unresolved target", *got)
	}
}

func TestRunSendCancelledDuringOfflineVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cannot send an interrupt signal to the test process on Windows")
	}
	stubConfigDir(t)
	offline := onlinePeer("mac2", idMac2, "192.168.68.54:8425")
	offline.Online = false
	stubLocalPeers(t, []presence.Peer{offline}, nil)
	got, _ := stubLocalRecord(t, nil)

	// The stub plays Ctrl-C during the unicast check: runSend has installed
	// its signal handler by then, so the interrupt cancels its context.
	orig := queryAddrFunc
	queryAddrFunc = func(ctx context.Context, _ netip.AddrPort, _ time.Duration) ([]discovery.Peer, error) {
		self, err := os.FindProcess(os.Getpid())
		if err != nil {
			return nil, err
		}
		if err := self.Signal(os.Interrupt); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("context was not cancelled")
		}
	}
	t.Cleanup(func() { queryAddrFunc = orig })

	src := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runSend([]string{"mac2", src})
	if !errors.Is(err, errCancelled) {
		t.Errorf("err = %v, want errCancelled", err)
	}
	if len(*got) != 0 {
		t.Errorf("recorded %+v although nothing was sent", *got)
	}
}
