package peername

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, ok := range []string{"mac2", "Junya's MacBook", "日本語", strings.Repeat("a", MaxLen)} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", MaxLen+1), "tab\there", "nul\x00", "del\x7f"} {
		if err := Validate(bad); err == nil {
			t.Errorf("Validate(%q) = nil, want error", bad)
		}
	}
}

func TestDefaultName(t *testing.T) {
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
		got, err := Default(tt.flag, tt.hostname)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Default(%q, %q) = %q, %v; want %q, err=%v", tt.flag, tt.hostname, got, err, tt.want, tt.wantErr)
		}
	}
}
