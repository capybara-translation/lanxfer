package main

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// version is overridden at build time via `-ldflags "-X main.version=..."`
// (set by GoReleaser). When that override is absent, init() consults
// debug.ReadBuildInfo to pick up the module version embedded by
// `go install ...@vX.Y.Z` (or `go build` on a clean tagged checkout). Pseudo
// versions, "+dirty" builds and "(devel)" are deliberately rejected so a local
// build never surfaces a version string that looks like a real release.
var version = "dev"

// pseudoVersionSuffix matches the timestamp-and-commit tail shared by every Go
// pseudo version form:
//
//	vX.0.0-yyyymmddhhmmss-abcdefabcdef          (no tag yet)
//	vX.Y.(Z+1)-0.yyyymmddhhmmss-abcdefabcdef    (commits after vX.Y.Z)
//	vX.Y.Z-pre.0.yyyymmddhhmmss-abcdefabcdef    (commits after a prerelease)
var pseudoVersionSuffix = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}$`)

func init() {
	info, _ := debug.ReadBuildInfo()
	version = resolveVersion(version, info)
}

// resolveVersion picks the effective version string from either the ldflags
// override (ldVersion) or the build info embedded by Go modules. It is split
// out from init() so it can be tested without rebuilding with custom ldflags.
//
// Since Go 1.24, `go build` in a git checkout embeds a pseudo version for an
// untagged commit and appends "+dirty" for uncommitted changes. Those are
// treated as "dev": surfacing them as if they were releases makes bug reports
// ambiguous about which exact build is running.
func resolveVersion(ldVersion string, info *debug.BuildInfo) string {
	if ldVersion != "dev" {
		return ldVersion
	}
	if info == nil {
		return "dev"
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" || strings.Contains(v, "+dirty") || pseudoVersionSuffix.MatchString(v) {
		return "dev"
	}
	return v
}
