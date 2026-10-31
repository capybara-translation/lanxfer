#!/usr/bin/env bash
# Usage: stamp-version.sh <tag> <output-dir>
#
# Writes copies of build/darwin/Info.plist and build/windows/info.json with
# the version from <tag> (a leading "v" is dropped) into <output-dir>, and
# prints the version on stdout. The tracked files are never modified: the
# build tasks take the copies through the INFO_PLIST and INFO_JSON variables.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <tag> <output-dir>" >&2
  exit 2
fi

tag=$1
outdir=$2
version=${tag#v}

# X.Y.Z with an optional prerelease suffix, as in v0.8.0 or v0.8.0-rc.1.
if ! [[ $version =~ ^([0-9]+\.[0-9]+\.[0-9]+)(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "stamp-version: unexpected version '$tag'" >&2
  exit 1
fi
# The Windows file version must be four numbers, so a suffix is dropped.
file_version="${BASH_REMATCH[1]}.0"

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mkdir -p "$outdir"

# In the plist, a value sits on the line after its key.
awk -v v="$version" '
  prev ~ /<key>CFBundleVersion<\/key>|<key>CFBundleShortVersionString<\/key>/ {
    sub(/<string>[^<]*<\/string>/, "<string>" v "</string>")
  }
  { print; prev = $0 }
' "$root/build/darwin/Info.plist" >"$outdir/Info.plist"

sed -E \
  -e "s/(\"file_version\": *)\"[^\"]*\"/\1\"$file_version\"/" \
  -e "s/(\"ProductVersion\": *)\"[^\"]*\"/\1\"$version\"/" \
  "$root/build/windows/info.json" >"$outdir/info.json"

# Fail loudly if the stamp did not land, for example after a template change.
[ "$(grep -c "<string>$version</string>" "$outdir/Info.plist")" -eq 2 ]
grep -q "\"file_version\": \"$file_version\"" "$outdir/info.json"
grep -q "\"ProductVersion\": \"$version\"" "$outdir/info.json"

echo "$version"
