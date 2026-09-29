package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name      string
		ldVersion string
		info      *debug.BuildInfo
		want      string
	}{
		{"ldflags override wins", "v1.2.3", &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, "v1.2.3"},
		{"nil build info", "dev", nil, "dev"},
		{"tagged module version", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "v0.1.0"},
		{"(devel) is hidden", "dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"empty is hidden", "dev", &debug.BuildInfo{Main: debug.Module{Version: ""}}, "dev"},
		{"pseudo version is hidden", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260908000000-abcdef123456"}}, "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.ldVersion, tt.info); got != tt.want {
				t.Errorf("resolveVersion(%q, %v) = %q, want %q", tt.ldVersion, tt.info, got, tt.want)
			}
		})
	}
}
