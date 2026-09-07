package main

import (
	"runtime/debug"
	"strings"
)

// version is overridden at build time via `-ldflags "-X main.version=..."`
// (set by GoReleaser). When that override is absent, init() consults
// debug.ReadBuildInfo to pick up the module version embedded by
// `go install ...@vX.Y.Z`. Pseudo versions (commit-hash based, "+dirty", etc.)
// and "(devel)" are deliberately rejected so a local build never surfaces a
// version string that looks like a real release.
var version = "dev"

func init() {
	info, _ := debug.ReadBuildInfo()
	version = resolveVersion(version, info)
}

// resolveVersion picks the effective version string from either the ldflags
// override (ldVersion) or the build info embedded by Go modules. It is split
// out from init() so it can be tested without rebuilding with custom ldflags.
//
// Pseudo versions starting with "v0.0.0-" (Go's auto-generated commit-hash
// based versions, e.g. when installing from a non-tagged commit or a dirty
// tree) are intentionally treated as "dev": surfacing them as if they were
// releases makes bug reports ambiguous about which exact build is running.
func resolveVersion(ldVersion string, info *debug.BuildInfo) string {
	if ldVersion != "dev" {
		return ldVersion
	}
	if info == nil {
		return "dev"
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" || strings.HasPrefix(v, "v0.0.0-") {
		return "dev"
	}
	return v
}
