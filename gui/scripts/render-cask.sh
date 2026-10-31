#!/usr/bin/env bash
# Usage: render-cask.sh <version> <sha256> <output-path>
#
# Fills build/darwin/lanxfer-app.rb.tmpl with the release version (a leading
# "v" is dropped) and the SHA-256 of lanxfer-app_darwin_universal.zip, and
# writes the Homebrew cask to <output-path>.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <version> <sha256> <output-path>" >&2
  exit 2
fi

version=${1#v}
sha256=$2
output=$3

if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "render-cask: unexpected version '$1' (a release version has no suffix)" >&2
  exit 1
fi
if ! [[ $sha256 =~ ^[0-9a-f]{64}$ ]]; then
  echo "render-cask: sha256 must be 64 lowercase hex characters" >&2
  exit 1
fi

template="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/build/darwin/lanxfer-app.rb.tmpl"

sed -e "s/@VERSION@/$version/" -e "s/@SHA256@/$sha256/" "$template" >"$output"

if grep -q '@[A-Z0-9_]*@' "$output"; then
  echo "render-cask: unfilled placeholder left in $output" >&2
  exit 1
fi
