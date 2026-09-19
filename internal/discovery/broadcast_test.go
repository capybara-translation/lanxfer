package discovery

import (
	"net"
	"testing"
)

// hostNet parses "ip/prefix" keeping the host IP (net.ParseCIDR alone would
// return the masked network address in the IPNet).
func hostNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return &net.IPNet{IP: ip, Mask: ipnet.Mask}
}

func TestBroadcastAddr(t *testing.T) {
	tests := []struct {
		cidr string
		want string // "" means not applicable
	}{
		{"192.168.68.53/22", "192.168.71.255"}, // Deco default: /22 crosses the third octet
		{"192.168.68.59/22", "192.168.71.255"}, // another host on the same network
		{"172.17.0.1/16", "172.17.255.255"},    // docker0
		{"10.0.0.5/24", "10.0.0.255"},
		{"10.1.2.3/8", "10.255.255.255"},
		{"192.168.1.130/25", "192.168.1.255"}, // upper half of a split /24
		{"192.168.1.5/25", "192.168.1.127"},   // lower half
		{"192.168.1.1/32", ""},                // host route: no broadcast
		{"192.168.1.0/31", ""},                // point-to-point: no broadcast
		{"8.8.8.8/24", ""},                    // not private
		{"127.0.0.1/8", ""},                   // loopback
		{"169.254.1.1/16", ""},                // link-local is not IsPrivate
		{"fe80::1/64", ""},                    // IPv6 has no broadcast
	}
	for _, tt := range tests {
		got, ok := broadcastAddr(hostNet(t, tt.cidr))
		if tt.want == "" {
			if ok {
				t.Errorf("broadcastAddr(%s) = %s, want not applicable", tt.cidr, got)
			}
			continue
		}
		if !ok || got.String() != tt.want {
			t.Errorf("broadcastAddr(%s) = %s (ok=%v), want %s", tt.cidr, got, ok, tt.want)
		}
	}
}

// A 16-byte mask can appear when an IPv4 address is carried in IPv6 form.
func TestBroadcastAddrAcceptsSixteenByteMask(t *testing.T) {
	ipnet := &net.IPNet{
		IP:   net.ParseIP("192.168.68.53"), // 16-byte form
		Mask: net.CIDRMask(96+22, 128),
	}
	got, ok := broadcastAddr(ipnet)
	if !ok || got.String() != "192.168.71.255" {
		t.Errorf("got %s (ok=%v), want 192.168.71.255", got, ok)
	}
}

// localBroadcastAddrs depends on the machine, so only its invariants are
// checked: every result is an IPv4 private address and there are no
// duplicates.
func TestLocalBroadcastAddrsInvariants(t *testing.T) {
	addrs, err := localBroadcastAddrs()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, a := range addrs {
		if !a.Is4() || !a.IsPrivate() {
			t.Errorf("%s is not a private IPv4 address", a)
		}
		if seen[a.String()] {
			t.Errorf("%s listed twice", a)
		}
		seen[a.String()] = true
	}
	t.Logf("broadcast addresses on this machine: %v", addrs)
}
