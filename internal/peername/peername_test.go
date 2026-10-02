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
