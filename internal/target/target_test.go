package target

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

const (
	idMac2   = "11111111-1111-4111-8111-111111111111"
	idOther  = "22222222-2222-4222-8222-222222222222"
	idUbuntu = "33333333-3333-4333-8333-333333333333"
)

var (
	mac2b  = discovery.Peer{Name: "MAC2", Addr: netip.MustParseAddrPort("192.168.68.77:8425"), OS: "darwin"}
	ubuntu = discovery.Peer{Name: "ubuntu", Addr: netip.MustParseAddrPort("192.168.68.59:9000"), OS: "linux", ID: idUbuntu}
)

func listed(name, id, addr string, online bool) presence.Peer {
	return presence.Peer{Name: name, ID: id, Addr: netip.MustParseAddrPort(addr), Online: online}
}

// stub is a Finder whose network calls are recorded and answered from fields.
type stub struct {
	replies     []discovery.Peer
	queryErr    error
	discovered  []discovery.Peer
	discoverErr error

	queried       []netip.AddrPort
	discoverCalls int
	discoverPort  int
}

func (s *stub) finder() Finder {
	return Finder{
		Query: func(_ context.Context, addr netip.AddrPort, _ time.Duration) ([]discovery.Peer, error) {
			s.queried = append(s.queried, addr)
			return s.replies, s.queryErr
		},
		Discover: func(_ context.Context, port int, _ time.Duration) ([]discovery.Peer, error) {
			s.discoverCalls++
			s.discoverPort = port
			return s.discovered, s.discoverErr
		},
	}
}

func TestLocateOnlinePeerIsUsedAsListed(t *testing.T) {
	s := &stub{queryErr: errors.New("must not be called"), discoverErr: errors.New("must not be called")}
	p := listed("mac2", idMac2, "192.168.68.54:9001", true)
	got, err := s.finder().Locate(context.Background(), p, 8425)
	if err != nil {
		t.Fatal(err)
	}
	want := Target{Addr: p.Addr, ID: idMac2, Name: "mac2"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if len(s.queried) != 0 || s.discoverCalls != 0 {
		t.Errorf("queried %d times, broadcast %d times; want none", len(s.queried), s.discoverCalls)
	}
}

func TestLocateVerifiesOfflinePeer(t *testing.T) {
	lastSeen := time.Date(2026, 10, 20, 9, 5, 0, 0, time.UTC)
	offline := func(id string) presence.Peer {
		p := listed("mac2", id, "192.168.68.54:8425", false)
		p.LastSeen = lastSeen
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
			s := &stub{replies: tt.replies, queryErr: tt.queryErr, discovered: tt.discovered, discoverErr: tt.discoverErr}
			got, err := s.finder().Locate(context.Background(), tt.peer, 8500)
			if len(s.queried) != 1 || s.queried[0] != tt.peer.Addr {
				t.Errorf("queried %v, want exactly the stored address %s", s.queried, tt.peer.Addr)
			}
			if (s.discoverCalls == 1) != tt.wantDiscovery || s.discoverCalls > 1 {
				t.Errorf("broadcast discovery ran %d times, want it used = %v", s.discoverCalls, tt.wantDiscovery)
			}
			if s.discoverCalls == 1 && s.discoverPort != 8500 {
				t.Errorf("discovery on port %d, want 8500", s.discoverPort)
			}
			if tt.wantAddr == "" {
				want := "mac2 is offline (last seen " + lastSeen.Local().Format("2006-01-02 15:04") + ")"
				var off *OfflineError
				if !errors.As(err, &off) {
					t.Fatalf("err = %v, want an *OfflineError", err)
				}
				if off.Name != "mac2" || !off.LastSeen.Equal(lastSeen) {
					t.Errorf("OfflineError = %+v", *off)
				}
				if err.Error() != want {
					t.Errorf("err = %q, want %q", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Addr.String() != tt.wantAddr || got.ID != tt.wantID || got.Name != tt.wantName {
				t.Errorf("got %+v, want addr %s, ID %q, name %q", got, tt.wantAddr, tt.wantID, tt.wantName)
			}
		})
	}
}

func TestLocateOfflineSameIDOnSeveralInterfacesUsesFirst(t *testing.T) {
	first := netip.MustParseAddrPort("192.168.68.60:8425")
	s := &stub{discovered: []discovery.Peer{
		{Name: "mac2", ID: idMac2, Addr: first},
		{Name: "mac2", ID: idMac2, Addr: netip.MustParseAddrPort("10.0.0.5:8425")},
	}}
	got, err := s.finder().Locate(context.Background(), listed("mac2", idMac2, "192.168.68.54:8425", false), 8425)
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != first || got.ID != idMac2 {
		t.Errorf("got %+v, want the first reply %s", got, first)
	}
}

func TestLocateOfflineLegacyPeerAmbiguousViaBroadcast(t *testing.T) {
	s := &stub{discovered: []discovery.Peer{
		{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.60:8425")},
		{Name: "MAC2", Addr: netip.MustParseAddrPort("192.168.68.61:8425")},
	}}
	_, err := s.finder().Locate(context.Background(), listed("mac2", "", "192.168.68.54:8425", false), 8425)
	want := `multiple peers named "mac2" (192.168.68.60, 192.168.68.61); specify an IP address`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	var off *OfflineError
	if errors.As(err, &off) {
		t.Errorf("an ambiguous result must not be an OfflineError: %v", err)
	}
}

func TestLocateOfflineCancelled(t *testing.T) {
	p := listed("mac2", idMac2, "192.168.68.54:8425", false)

	t.Run("during the unicast check", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s := &stub{discoverErr: errors.New("must not be called")}
		f := s.finder()
		f.Query = func(context.Context, netip.AddrPort, time.Duration) ([]discovery.Peer, error) {
			cancel()
			return nil, ctx.Err()
		}
		_, err := f.Locate(ctx, p, 8425)
		var off *OfflineError
		if !errors.As(err, &off) {
			t.Errorf("err = %v, want an *OfflineError so the caller reports the cancellation", err)
		}
		if s.discoverCalls != 0 {
			t.Errorf("broadcast discovery ran %d times after cancellation", s.discoverCalls)
		}
	})

	t.Run("during the broadcast", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s := &stub{}
		f := s.finder()
		f.Discover = func(context.Context, int, time.Duration) ([]discovery.Peer, error) {
			cancel()
			return nil, ctx.Err()
		}
		_, err := f.Locate(ctx, p, 8425)
		var off *OfflineError
		if !errors.As(err, &off) {
			t.Errorf("err = %v, want an *OfflineError", err)
		}
	})
}

func TestPick(t *testing.T) {
	a := discovery.Peer{Name: "mac2", ID: idMac2, Addr: netip.MustParseAddrPort("192.168.68.54:8425")}
	got, err := Pick("mac2", []discovery.Peer{a})
	if err != nil || got != (Target{Addr: a.Addr, ID: idMac2, Name: "mac2"}) {
		t.Errorf("one match: got %+v, %v", got, err)
	}
	_, err = Pick("mac2", []discovery.Peer{a, mac2b})
	want := `multiple peers named "mac2" (192.168.68.54, 192.168.68.77); specify an IP address`
	if err == nil || err.Error() != want {
		t.Errorf("several matches: err = %v, want %q", err, want)
	}
}

func TestFormatLastSeen(t *testing.T) {
	ts := time.Date(2026, 10, 20, 9, 5, 0, 0, time.UTC)
	want := ts.Local().Format("2006-01-02 15:04")
	if got := FormatLastSeen(ts); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
