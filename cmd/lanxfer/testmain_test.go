package main

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

// Device IDs used across the tests.
const (
	idMac2   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idUbuntu = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	idOther  = "cccccccccccccccccccccccccccccccc"
	idSelf   = "dddddddddddddddddddddddddddddddd"
)

// TestMain isolates the whole test binary from the machine it runs on. A
// developer may have a real receiver on the default port and a real config
// folder, so by default no test may reach either: the config folder is a
// temporary one, the local receiver is absent, and broadcast and unicast
// queries find nobody. Tests that need other behavior override these with the stub helpers.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "lanxfer-cmd-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	defer os.RemoveAll(dir)

	configDirFunc = func() (string, error) { return dir, nil }
	localPeersFunc = func(int, string) ([]presence.Peer, error) { return nil, client.ErrNoLocalReceiver }
	localRecordFunc = func(int, string, history.Entry) error { return client.ErrNoLocalReceiver }
	discoverFunc = func(context.Context, int, time.Duration) ([]discovery.Peer, error) { return nil, nil }
	queryAddrFunc = func(context.Context, netip.AddrPort, time.Duration) ([]discovery.Peer, error) { return nil, nil }
	return m.Run()
}

// stubConfigDir points configDirFunc at a fresh temporary folder for one
// test and returns it.
func stubConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	setConfigDir(t, dir)
	return dir
}

// setConfigDir makes configDirFunc return dir for one test.
func setConfigDir(t *testing.T, dir string) {
	t.Helper()
	orig := configDirFunc
	configDirFunc = func() (string, error) { return dir, nil }
	t.Cleanup(func() { configDirFunc = orig })
}

// stubLocalPeers replaces localPeersFunc for one test and returns a pointer
// to its call count.
func stubLocalPeers(t *testing.T, peers []presence.Peer, err error) *int {
	t.Helper()
	calls := 0
	orig := localPeersFunc
	localPeersFunc = func(int, string) ([]presence.Peer, error) {
		calls++
		return peers, err
	}
	t.Cleanup(func() { localPeersFunc = orig })
	return &calls
}

// selfIDs lists the device IDs the commands passed to the local API.
type selfIDs struct{ peers, record []string }

// watchSelfIDs wraps the current localPeersFunc and localRecordFunc, so call
// it after stubbing them, and records the device ID each call was given.
func watchSelfIDs(t *testing.T) *selfIDs {
	t.Helper()
	ids := &selfIDs{}
	origPeers, origRecord := localPeersFunc, localRecordFunc
	localPeersFunc = func(port int, selfID string) ([]presence.Peer, error) {
		ids.peers = append(ids.peers, selfID)
		return origPeers(port, selfID)
	}
	localRecordFunc = func(port int, selfID string, e history.Entry) error {
		ids.record = append(ids.record, selfID)
		return origRecord(port, selfID, e)
	}
	t.Cleanup(func() { localPeersFunc, localRecordFunc = origPeers, origRecord })
	return ids
}

// storedDeviceID returns the device ID kept in dir, which deviceID creates
// when there is none.
func storedDeviceID(t *testing.T, dir string) string {
	t.Helper()
	id, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// stubQueryAddr replaces queryAddrFunc for one test and returns a pointer to
// the addresses it was asked about.
func stubQueryAddr(t *testing.T, replies []discovery.Peer, err error) *[]netip.AddrPort {
	t.Helper()
	var asked []netip.AddrPort
	orig := queryAddrFunc
	queryAddrFunc = func(_ context.Context, addr netip.AddrPort, _ time.Duration) ([]discovery.Peer, error) {
		asked = append(asked, addr)
		return replies, err
	}
	t.Cleanup(func() { queryAddrFunc = orig })
	return &asked
}

// stubLocalRecord replaces localRecordFunc for one test. It returns the
// entries it received and the port it was called with.
func stubLocalRecord(t *testing.T, err error) (*[]history.Entry, *int) {
	t.Helper()
	var got []history.Entry
	var gotPort int
	orig := localRecordFunc
	localRecordFunc = func(port int, _ string, e history.Entry) error {
		gotPort = port
		got = append(got, e)
		return err
	}
	t.Cleanup(func() { localRecordFunc = orig })
	return &got, &gotPort
}

// onlinePeer returns an online presence.Peer.
func onlinePeer(name, id, addr string) presence.Peer {
	return presence.Peer{
		ID: id, Name: name, OS: "darwin", Addr: netip.MustParseAddrPort(addr),
		LastSeen: time.Now(), Online: true,
	}
}
