package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

// stubDiscover replaces discoverFunc for one test and returns a pointer to
// its call count.
func stubDiscover(t *testing.T, peers []discovery.Peer, err error) *int {
	t.Helper()
	calls := 0
	orig := discoverFunc
	discoverFunc = func(context.Context, int, time.Duration) ([]discovery.Peer, error) {
		calls++
		return peers, err
	}
	t.Cleanup(func() { discoverFunc = orig })
	return &calls
}

var (
	mac2   = discovery.Peer{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.54:8425"), OS: "darwin", ID: idMac2}
	ubuntu = discovery.Peer{Name: "ubuntu", Addr: netip.MustParseAddrPort("192.168.68.59:9000"), OS: "linux", ID: idUbuntu}
	mac2b  = discovery.Peer{Name: "MAC2", Addr: netip.MustParseAddrPort("192.168.68.77:8425"), OS: "darwin"}
)

func TestResolveTargetIPLiteralSkipsDiscovery(t *testing.T) {
	calls := stubDiscover(t, nil, errors.New("must not be called"))
	localCalls := stubLocalPeers(t, nil, errors.New("must not be called"))
	got, err := resolveTarget(context.Background(), "192.168.68.54", 8425, idSelf)
	if err != nil || got.ByName || got.Addr.String() != "192.168.68.54:8425" || got.ID != "" || got.Name != "" {
		t.Errorf("got %+v, %v", got, err)
	}
	if *localCalls != 0 {
		t.Errorf("local peers queried %d times for an IP literal", *localCalls)
	}
	if *calls != 0 {
		t.Errorf("discovery called %d times for an IP literal", *calls)
	}
}

func TestResolveTargetByNameIsCaseInsensitive(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2, ubuntu}, nil)
	got, err := resolveTarget(context.Background(), "UBUNTU", 8425, idSelf)
	if err != nil || !got.ByName {
		t.Fatalf("got %+v, %v", got, err)
	}
	// The HTTP port comes from the peer's reply, not from --port.
	if got.Addr.String() != "192.168.68.59:9000" {
		t.Errorf("addr = %s, want 192.168.68.59:9000", got.Addr)
	}
	if got.ID != ubuntu.ID || got.Name != "ubuntu" {
		t.Errorf("ID, Name = %q, %q; want %q, %q", got.ID, got.Name, ubuntu.ID, "ubuntu")
	}
}

func TestResolveTargetNoMatch(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2}, nil)
	_, err := resolveTarget(context.Background(), "winbox", 8425, idSelf)
	if err == nil || !strings.Contains(err.Error(), `no peer named "winbox"`) {
		t.Errorf("err = %v", err)
	}
}

func TestResolveTargetAmbiguousViaDiscovery(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2, mac2b}, nil)
	_, err := resolveTarget(context.Background(), "mac2", 8425, idSelf)
	if err == nil || !strings.Contains(err.Error(), "192.168.68.54") || !strings.Contains(err.Error(), "192.168.68.77") {
		t.Errorf("err = %v, want both candidates listed", err)
	}
}

func TestResolveTargetAmbiguous(t *testing.T) {
	calls := stubDiscover(t, nil, errors.New("must not be called"))
	stubLocalPeers(t, []presence.Peer{
		onlinePeer("mac2", idMac2, "192.168.68.54:8425"),
		onlinePeer("MAC2", idOther, "192.168.68.77:8425"),
	}, nil)
	_, err := resolveTarget(context.Background(), "mac2", 8425, idSelf)
	if !errors.Is(err, presence.ErrAmbiguous) {
		t.Fatalf("err = %v, want ErrAmbiguous", err)
	}
	want := `multiple peers named "mac2" (192.168.68.54:8425, 192.168.68.77:8425); specify an IP address`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if *calls != 0 {
		t.Errorf("discovery called %d times for an ambiguous local list", *calls)
	}
}

func TestResolveTargetUsesLocalPeers(t *testing.T) {
	calls := stubDiscover(t, nil, errors.New("must not be called"))
	queries := stubQueryAddr(t, nil, errors.New("must not be called"))
	var gotPort int
	var gotID string
	orig := localPeersFunc
	localPeersFunc = func(port int, selfID string) ([]presence.Peer, error) {
		gotPort, gotID = port, selfID
		return []presence.Peer{
			onlinePeer("ubuntu", idUbuntu, "192.168.68.59:9000"),
			onlinePeer("mac2", idMac2, "192.168.68.54:8425"),
		}, nil
	}
	t.Cleanup(func() { localPeersFunc = orig })

	got, err := resolveTarget(context.Background(), "UBUNTU", 8500, idSelf)
	if err != nil {
		t.Fatal(err)
	}
	want := target{Addr: netip.MustParseAddrPort("192.168.68.59:9000"), ByName: true, ID: idUbuntu, Name: "ubuntu"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if gotPort != 8500 {
		t.Errorf("local API queried on port %d, want 8500", gotPort)
	}
	if gotID != idSelf {
		t.Errorf("local API given ID %q, want %q", gotID, idSelf)
	}
	if *calls != 0 || len(*queries) != 0 {
		t.Errorf("discovery %d times, unicast %d times; want none", *calls, len(*queries))
	}
}

func TestResolveTargetVerifiesOfflinePeer(t *testing.T) {
	lastSeen := time.Date(2026, 10, 20, 9, 5, 0, 0, time.UTC)
	offline := func(id string) presence.Peer {
		p := onlinePeer("mac2", id, "192.168.68.54:8425")
		p.Online, p.LastSeen = false, lastSeen
		return p
	}
	newAddr := netip.MustParseAddrPort("192.168.68.99:8425")
	tests := []struct {
		name          string
		peer          presence.Peer
		replies       []discovery.Peer // the unicast reply
		queryErr      error
		discovered    []discovery.Peer // the broadcast fallback
		discoverErr   error
		wantDiscovery bool   // the broadcast fallback is expected to run
		wantAddr      string // "" means an offline error is expected
		wantID        string
		wantName      string // the target's name when found
	}{
		{
			name:     "same ID, uses the reply's address",
			peer:     offline(idMac2),
			replies:  []discovery.Peer{{Name: "mac2-renamed", ID: idMac2, Addr: netip.MustParseAddrPort("192.168.68.54:9001")}},
			wantAddr: "192.168.68.54:9001", wantID: idMac2, wantName: "mac2-renamed",
		},
		{
			name:     "legacy peer matches by name, ignoring case",
			peer:     offline(""),
			replies:  []discovery.Peer{{Name: "MAC2", Addr: netip.MustParseAddrPort("192.168.68.54:8425")}},
			wantAddr: "192.168.68.54:8425", wantName: "MAC2",
		},
		{
			name:          "different ID at the same address, nobody else answers",
			peer:          offline(idMac2),
			replies:       []discovery.Peer{{Name: "mac2", ID: idOther, Addr: netip.MustParseAddrPort("192.168.68.54:8425")}},
			wantDiscovery: true,
		},
		{
			name:          "reply without ID does not match a peer with one",
			peer:          offline(idMac2),
			replies:       []discovery.Peer{{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.54:8425")}},
			wantDiscovery: true,
		},
		{name: "no reply", peer: offline(idMac2), wantDiscovery: true},
		{name: "query fails", peer: offline(idMac2), queryErr: errors.New("network down"), wantDiscovery: true},
		{
			name:          "legacy peer with another name",
			peer:          offline(""),
			replies:       []discovery.Peer{{Name: "other", Addr: netip.MustParseAddrPort("192.168.68.54:8425")}},
			wantDiscovery: true,
		},
		{
			name:          "no reply, same ID found at a new address by broadcast",
			peer:          offline(idMac2),
			discovered:    []discovery.Peer{{Name: "mac2", ID: idMac2, Addr: newAddr}},
			wantDiscovery: true,
			wantAddr:      newAddr.String(), wantID: idMac2, wantName: "mac2",
		},
		{
			name:          "no reply, same ID found under a new name by broadcast",
			peer:          offline(idMac2),
			discovered:    []discovery.Peer{mac2b, {Name: "mac2-renamed", ID: idMac2, Addr: newAddr}},
			wantDiscovery: true,
			wantAddr:      newAddr.String(), wantID: idMac2, wantName: "mac2-renamed",
		},
		{
			name:          "mismatching reply, same ID found elsewhere by broadcast",
			peer:          offline(idMac2),
			replies:       []discovery.Peer{{Name: "mac2", ID: idOther, Addr: netip.MustParseAddrPort("192.168.68.54:8425")}},
			discovered:    []discovery.Peer{{Name: "mac2", ID: idMac2, Addr: newAddr}},
			wantDiscovery: true,
			wantAddr:      newAddr.String(), wantID: idMac2, wantName: "mac2",
		},
		{
			name:          "query fails, same ID found by broadcast",
			peer:          offline(idMac2),
			queryErr:      errors.New("network down"),
			discovered:    []discovery.Peer{{Name: "mac2", ID: idMac2, Addr: newAddr}},
			wantDiscovery: true,
			wantAddr:      newAddr.String(), wantID: idMac2, wantName: "mac2",
		},
		{
			name:          "broadcast finds only another ID with the same name",
			peer:          offline(idMac2),
			discovered:    []discovery.Peer{{Name: "mac2", ID: idOther, Addr: newAddr}, mac2b},
			wantDiscovery: true,
		},
		{
			name:          "legacy peer found by name by broadcast",
			peer:          offline(""),
			discovered:    []discovery.Peer{ubuntu, {Name: "MAC2", Addr: newAddr}},
			wantDiscovery: true,
			wantAddr:      newAddr.String(), wantName: "MAC2",
		},
		{
			name:          "legacy peer found by broadcast with an ID takes it",
			peer:          offline(""),
			discovered:    []discovery.Peer{{Name: "mac2", ID: idMac2, Addr: newAddr}},
			wantDiscovery: true,
			wantAddr:      newAddr.String(), wantID: idMac2, wantName: "mac2",
		},
		{
			name:          "broadcast fails",
			peer:          offline(idMac2),
			discoverErr:   discovery.ErrNoNetwork,
			wantDiscovery: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var discoverPort int
			discoverCalls := 0
			orig := discoverFunc
			discoverFunc = func(_ context.Context, port int, _ time.Duration) ([]discovery.Peer, error) {
				discoverCalls++
				discoverPort = port
				return tt.discovered, tt.discoverErr
			}
			t.Cleanup(func() { discoverFunc = orig })
			stubLocalPeers(t, []presence.Peer{tt.peer}, nil)
			queries := stubQueryAddr(t, tt.replies, tt.queryErr)

			got, err := resolveTarget(context.Background(), "mac2", 8500, idSelf)
			if len(*queries) != 1 || (*queries)[0] != tt.peer.Addr {
				t.Errorf("queried %v, want exactly the stored address %s", *queries, tt.peer.Addr)
			}
			if (discoverCalls == 1) != tt.wantDiscovery || discoverCalls > 1 {
				t.Errorf("broadcast discovery ran %d times, want it used = %v", discoverCalls, tt.wantDiscovery)
			}
			if discoverCalls == 1 && discoverPort != 8500 {
				t.Errorf("discovery on port %d, want 8500", discoverPort)
			}
			if tt.wantAddr == "" {
				want := "mac2 is offline (last seen " + lastSeen.Local().Format("2006-01-02 15:04") + ")"
				if err == nil || err.Error() != want {
					t.Errorf("err = %v, want %q", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Addr.String() != tt.wantAddr || !got.ByName || got.ID != tt.wantID || got.Name != tt.wantName {
				t.Errorf("got %+v, want addr %s, ID %q, name %q", got, tt.wantAddr, tt.wantID, tt.wantName)
			}
		})
	}
}

func TestResolveTargetOfflineSameIDOnSeveralInterfacesUsesFirst(t *testing.T) {
	p := onlinePeer("mac2", idMac2, "192.168.68.54:8425")
	p.Online = false
	stubLocalPeers(t, []presence.Peer{p}, nil)
	first := netip.MustParseAddrPort("192.168.68.60:8425")
	stubDiscover(t, []discovery.Peer{
		{Name: "mac2", ID: idMac2, Addr: first},
		{Name: "mac2", ID: idMac2, Addr: netip.MustParseAddrPort("10.0.0.5:8425")},
	}, nil)
	got, err := resolveTarget(context.Background(), "mac2", 8425, idSelf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != first || got.ID != idMac2 {
		t.Errorf("got %+v, want the first reply %s", got, first)
	}
}

func TestResolveTargetOfflineLegacyPeerAmbiguousViaDiscovery(t *testing.T) {
	p := onlinePeer("mac2", "", "192.168.68.54:8425")
	p.Online = false
	stubLocalPeers(t, []presence.Peer{p}, nil)
	stubDiscover(t, []discovery.Peer{
		{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.60:8425")},
		{Name: "MAC2", Addr: netip.MustParseAddrPort("192.168.68.61:8425")},
	}, nil)
	_, err := resolveTarget(context.Background(), "mac2", 8425, idSelf)
	want := `multiple peers named "mac2" (192.168.68.60, 192.168.68.61); specify an IP address`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestResolveTargetOfflineCancelledStaysCancelled(t *testing.T) {
	p := onlinePeer("mac2", idMac2, "192.168.68.54:8425")
	p.Online = false
	stubLocalPeers(t, []presence.Peer{p}, nil)

	t.Run("during the unicast check", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		orig := queryAddrFunc
		queryAddrFunc = func(context.Context, netip.AddrPort, time.Duration) ([]discovery.Peer, error) {
			cancel()
			return nil, ctx.Err()
		}
		t.Cleanup(func() { queryAddrFunc = orig })
		calls := stubDiscover(t, nil, errors.New("must not be called"))
		_, err := resolveTarget(ctx, "mac2", 8425, idSelf)
		if !errors.Is(interrupted(ctx, err), errCancelled) {
			t.Errorf("err = %v, want it reported as a cancellation", err)
		}
		if *calls != 0 {
			t.Errorf("broadcast discovery ran %d times after cancellation", *calls)
		}
	})

	t.Run("during the broadcast", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		stubQueryAddr(t, nil, nil)
		orig := discoverFunc
		discoverFunc = func(context.Context, int, time.Duration) ([]discovery.Peer, error) {
			cancel()
			return nil, ctx.Err()
		}
		t.Cleanup(func() { discoverFunc = orig })
		_, err := resolveTarget(ctx, "mac2", 8425, idSelf)
		if !errors.Is(interrupted(ctx, err), errCancelled) {
			t.Errorf("err = %v, want it reported as a cancellation", err)
		}
	})
}

func TestResolveTargetFallsBackWhenNoLocalReceiver(t *testing.T) {
	tests := []struct {
		name  string
		peers []presence.Peer
		err   error
	}{
		{name: "no local receiver", err: client.ErrNoLocalReceiver},
		{name: "local API failed", err: errors.New("local receiver returned 500")},
		{name: "empty list"},
		{name: "name not in the list", peers: []presence.Peer{onlinePeer("other", idOther, "192.168.68.99:8425")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubLocalPeers(t, tt.peers, tt.err)
			calls := stubDiscover(t, []discovery.Peer{mac2}, nil)
			got, err := resolveTarget(context.Background(), "mac2", 8425, idSelf)
			if err != nil {
				t.Fatal(err)
			}
			if *calls != 1 {
				t.Errorf("discovery called %d times, want 1", *calls)
			}
			if got.Addr != mac2.Addr || !got.ByName || got.ID != mac2.ID || got.Name != "mac2" {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestResolveTargetDiscoveryError(t *testing.T) {
	stubDiscover(t, nil, discovery.ErrNoNetwork)
	if _, err := resolveTarget(context.Background(), "mac2", 8425, idSelf); !errors.Is(err, discovery.ErrNoNetwork) {
		t.Errorf("err = %v, want ErrNoNetwork", err)
	}
}

func TestResolveTargetBadPort(t *testing.T) {
	if _, err := resolveTarget(context.Background(), "192.168.68.54", 70000, idSelf); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}

func TestInterrupted(t *testing.T) {
	other := errors.New("boom")
	if got := interrupted(context.Background(), other); got != other {
		t.Errorf("not cancelled: got %v, want %v", got, other)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := interrupted(ctx, other); !errors.Is(got, errCancelled) {
		t.Errorf("cancelled: got %v, want errCancelled", got)
	}
}

func TestPrintDone(t *testing.T) {
	tests := []struct {
		name     string
		res      client.Result
		sentName string
		size     int64
		want     string
	}{
		{
			name:     "folder shows content size, not tar size",
			res:      client.Result{RemoteName: "trip", Size: 10240, Duration: 2 * time.Second},
			sentName: "trip",
			size:     2048,
			want:     "done: 2.0 KiB in 2s (1.0 KiB/s)\n",
		},
		{
			name:     "renamed by the receiver",
			res:      client.Result{RemoteName: "trip (1)", Size: 10240, Duration: 2 * time.Second},
			sentName: "trip",
			size:     2048,
			want:     "done: 2.0 KiB in 2s (1.0 KiB/s) [saved as \"trip (1)\"]\n",
		},
		{
			name:     "zero duration has no rate",
			res:      client.Result{RemoteName: "a.txt", Size: 2048},
			sentName: "a.txt",
			size:     2048,
			want:     "done: 2.0 KiB in 0s\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printDone(&buf, &tt.res, tt.sentName, tt.size)
			if got := buf.String(); got != tt.want {
				t.Errorf("printDone = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrintSkipped(t *testing.T) {
	many := make([]client.SkippedItem, 12)
	var wantMany strings.Builder
	wantMany.WriteString("skipped 12 items (system files, links, and other special files):\n")
	for i := range many {
		many[i] = client.SkippedItem{Path: fmt.Sprintf("d/._%02d", i), Reason: "system file"}
		if i < 10 {
			fmt.Fprintf(&wantMany, "  d/._%02d (system file)\n", i)
		}
	}
	wantMany.WriteString("  ... and 2 more\n")

	tests := []struct {
		name  string
		items []client.SkippedItem
		want  string
	}{
		{
			name: "two items",
			items: []client.SkippedItem{
				{Path: ".DS_Store", Reason: "system file"},
				{Path: "test/.DS_Store", Reason: "system file"},
			},
			want: "skipped 2 items (system files, links, and other special files):\n" +
				"  .DS_Store (system file)\n" +
				"  test/.DS_Store (system file)\n",
		},
		{name: "more than shown", items: many, want: wantMany.String()},
		{
			name:  "control character is quoted",
			items: []client.SkippedItem{{Path: "Icon\r", Reason: "system file"}, {Path: "写真/link", Reason: "symbolic link"}},
			want: "skipped 2 items (system files, links, and other special files):\n" +
				"  \"Icon\\r\" (system file)\n" +
				"  写真/link (symbolic link)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printSkipped(&buf, tt.items)
			if got := buf.String(); got != tt.want {
				t.Errorf("printSkipped =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}
