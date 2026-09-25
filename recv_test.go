package main

import "testing"

func TestPeerName(t *testing.T) {
	tests := []struct {
		flag, hostname, want string
		wantErr              bool
	}{
		{"mac2", "ignored.local", "mac2", false},                      // flag wins
		{"", "Junyas-MacBook-Pro.local", "Junyas-MacBook-Pro", false}, // .local stripped
		{"", "ubuntu", "ubuntu", false},
		{"", "", "lanxfer", false},       // hostname unavailable
		{"", ".local", "lanxfer", false}, // nothing left after stripping
		{"bad\nname", "host", "", true},  // explicit bad name is an error
	}
	for _, tt := range tests {
		got, err := peerName(tt.flag, tt.hostname)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("peerName(%q, %q) = %q, %v; want %q, err=%v", tt.flag, tt.hostname, got, err, tt.want, tt.wantErr)
		}
	}
}
