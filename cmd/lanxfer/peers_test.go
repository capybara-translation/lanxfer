package main

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

func TestPrintPeers(t *testing.T) {
	var buf bytes.Buffer
	printPeers(&buf, []peerRow{
		{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.54:8425"), OS: "darwin", Online: true},
		{Name: "ubuntu-box", Addr: netip.MustParseAddrPort("192.168.68.59:8425"), OS: "linux", Online: true},
	})
	want := "" +
		"NAME        ADDRESS             OS      STATUS  LAST SEEN\n" +
		"mac2        192.168.68.54:8425  darwin  online  -\n" +
		"ubuntu-box  192.168.68.59:8425  linux   online  -\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestPrintPeersWithStatus(t *testing.T) {
	// LastSeen is shown in local time; pin it so the expectation is fixed.
	origLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = origLocal })

	var buf bytes.Buffer
	printPeers(&buf, []peerRow{
		{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.54:8425"), OS: "darwin", Online: true,
			LastSeen: time.Date(2026, 10, 20, 9, 5, 30, 0, time.UTC)},
		{Name: "ubuntu-box", Addr: netip.MustParseAddrPort("192.168.68.59:8425"), OS: "linux",
			LastSeen: time.Date(2026, 10, 19, 23, 41, 0, 0, time.UTC)},
	})
	want := "" +
		"NAME        ADDRESS             OS      STATUS   LAST SEEN\n" +
		"mac2        192.168.68.54:8425  darwin  online   -\n" +
		"ubuntu-box  192.168.68.59:8425  linux   offline  2026-10-19 23:41\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestPeerRows(t *testing.T) {
	seen := time.Date(2026, 10, 20, 9, 5, 0, 0, time.UTC)
	local := []presence.Peer{
		{ID: idMac2, Name: "mac2", OS: "darwin", Addr: netip.MustParseAddrPort("192.168.68.54:8425"), LastSeen: seen, Online: false},
	}
	rows := rowsFromPresence(local)
	if len(rows) != 1 || rows[0].Name != "mac2" || rows[0].Online || !rows[0].LastSeen.Equal(seen) {
		t.Errorf("rowsFromPresence = %+v", rows)
	}
	found := []discovery.Peer{{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.54:8425"), OS: "darwin"}}
	rows = rowsFromDiscovery(found)
	if len(rows) != 1 || rows[0].Name != "mac2" || !rows[0].Online {
		t.Errorf("rowsFromDiscovery = %+v", rows)
	}
}

func TestRunPeersRejectsArgs(t *testing.T) {
	if err := runPeers([]string{"extra"}); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}

func TestCollectPeers(t *testing.T) {
	local := []presence.Peer{onlinePeer("mac2", idMac2, "192.168.68.54:8425")}
	tests := []struct {
		name          string
		localPeers    []presence.Peer
		localErr      error
		wantDiscovery bool
		wantRows      int
		wantErr       bool
	}{
		{name: "local list", localPeers: local, wantRows: 1},
		{name: "no local receiver", localErr: client.ErrNoLocalReceiver, wantDiscovery: true, wantRows: 1},
		{name: "empty local list", wantDiscovery: true, wantRows: 1},
		{name: "local API failed", localErr: errors.New("boom"), wantDiscovery: true, wantRows: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubLocalPeers(t, tt.localPeers, tt.localErr)
			calls := stubDiscover(t, []discovery.Peer{mac2}, nil)
			rows, err := collectPeers(t.Context(), 8425, idSelf, time.Second)
			if err != nil || len(rows) != tt.wantRows {
				t.Fatalf("rows = %+v, err = %v", rows, err)
			}
			if (*calls == 1) != tt.wantDiscovery {
				t.Errorf("discovery called %d times, want used = %v", *calls, tt.wantDiscovery)
			}
		})
	}
}

func TestCollectPeersPassesOwnID(t *testing.T) {
	stubLocalPeers(t, nil, client.ErrNoLocalReceiver)
	ids := watchSelfIDs(t)
	stubDiscover(t, nil, nil)
	if _, err := collectPeers(t.Context(), 8425, idSelf, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(ids.peers) != 1 || ids.peers[0] != idSelf {
		t.Errorf("local API was given IDs %v, want [%s]", ids.peers, idSelf)
	}
}

func TestRunPeersAsksLocalReceiverWithOwnID(t *testing.T) {
	dir := stubConfigDir(t)
	stubLocalPeers(t, []presence.Peer{onlinePeer("mac2", idMac2, "192.168.68.54:8425")}, nil)
	ids := watchSelfIDs(t)
	stubDiscover(t, nil, errors.New("must not be called"))
	if err := runPeers(nil); err != nil {
		t.Fatal(err)
	}
	if want := storedDeviceID(t, dir); len(ids.peers) != 1 || ids.peers[0] != want {
		t.Errorf("local API was given IDs %v, want [%s]", ids.peers, want)
	}
}

func TestCollectPeersNoneFound(t *testing.T) {
	stubDiscover(t, nil, nil)
	rows, err := collectPeers(t.Context(), 8425, idSelf, time.Second)
	if err != nil || len(rows) != 0 {
		t.Errorf("rows = %+v, err = %v", rows, err)
	}
}
