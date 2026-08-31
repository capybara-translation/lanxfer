package server

import "testing"

func TestIsAllowedRemote(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		// allowed: private (RFC 1918)
		{"192.168.1.10:54321", true},
		{"10.0.0.5:1234", true},
		{"172.16.0.1:80", true},
		{"172.31.255.255:80", true},
		// allowed: loopback and link-local
		{"127.0.0.1:9999", true},
		{"[::1]:9999", true},
		{"169.254.10.20:1", true},
		{"[fe80::abcd]:1", true},
		// rejected: global addresses
		{"8.8.8.8:53", false},
		{"203.0.113.9:443", false},
		{"172.32.0.1:80", false}, // just outside 172.16/12
		{"[2001:4860:4860::8888]:443", false},
		// rejected: malformed input
		{"", false},
		{"not-an-address", false},
		{"192.168.1.10", false}, // missing port (RemoteAddr is always host:port)
	}
	for _, tt := range tests {
		if got := IsAllowedRemote(tt.addr); got != tt.want {
			t.Errorf("IsAllowedRemote(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}
