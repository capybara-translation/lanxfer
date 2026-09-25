package main

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
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
	mac2   = discovery.Peer{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.54:8425"), OS: "darwin"}
	ubuntu = discovery.Peer{Name: "ubuntu", Addr: netip.MustParseAddrPort("192.168.68.59:9000"), OS: "linux"}
	mac2b  = discovery.Peer{Name: "MAC2", Addr: netip.MustParseAddrPort("192.168.68.77:8425"), OS: "darwin"}
)

func TestResolveTargetIPLiteralSkipsDiscovery(t *testing.T) {
	calls := stubDiscover(t, nil, errors.New("must not be called"))
	addr, byName, err := resolveTarget("192.168.68.54", 8425)
	if err != nil || byName || addr.String() != "192.168.68.54:8425" {
		t.Errorf("got %v, %v, %v", addr, byName, err)
	}
	if *calls != 0 {
		t.Errorf("discovery called %d times for an IP literal", *calls)
	}
}

func TestResolveTargetByNameIsCaseInsensitive(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2, ubuntu}, nil)
	addr, byName, err := resolveTarget("UBUNTU", 8425)
	if err != nil || !byName {
		t.Fatalf("got %v, %v, %v", addr, byName, err)
	}
	// The HTTP port comes from the peer's reply, not from --port.
	if addr.String() != "192.168.68.59:9000" {
		t.Errorf("addr = %s, want 192.168.68.59:9000", addr)
	}
}

func TestResolveTargetNoMatch(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2}, nil)
	_, _, err := resolveTarget("winbox", 8425)
	if err == nil || !strings.Contains(err.Error(), `no peer named "winbox"`) {
		t.Errorf("err = %v", err)
	}
}

func TestResolveTargetAmbiguous(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2, mac2b}, nil)
	_, _, err := resolveTarget("mac2", 8425)
	if err == nil || !strings.Contains(err.Error(), "192.168.68.54") || !strings.Contains(err.Error(), "192.168.68.77") {
		t.Errorf("err = %v, want both candidates listed", err)
	}
}

func TestResolveTargetDiscoveryError(t *testing.T) {
	stubDiscover(t, nil, discovery.ErrNoNetwork)
	if _, _, err := resolveTarget("mac2", 8425); !errors.Is(err, discovery.ErrNoNetwork) {
		t.Errorf("err = %v, want ErrNoNetwork", err)
	}
}

func TestResolveTargetBadPort(t *testing.T) {
	if _, _, err := resolveTarget("192.168.68.54", 70000); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}
