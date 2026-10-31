#!/usr/bin/env bash
# Usage: build-release.sh <macos|windows|linux> <tag> <output-dir>
#
# Builds the desktop app for one platform exactly as a release does and
# writes its distribution file into <output-dir>:
#
#   macos    lanxfer-app_darwin_universal.zip   (run on macOS)
#   windows  lanxfer-app_windows_amd64.zip      (cross-compiles on any host)
#   linux    lanxfer-app_linux_amd64.tar.gz     (run on Linux)
#
# <tag> is stamped into the build (see stamp-version.sh); CI checks pass the
# dummy v9.9.9. The tracked files are not modified. The release
# workflow and the check workflow both call this script, so they cannot
# drift apart. Needs Go, Node.js and wails3 on the PATH.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <macos|windows|linux> <tag> <output-dir>" >&2
  exit 2
fi

target=$1
tag=$2
mkdir -p "$3"
out=$(cd "$3" && pwd)

gui=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$gui"

case "$target" in
  macos) [ "$(uname -s)" = Darwin ] || { echo "build-release: macos must be built on macOS" >&2; exit 1; } ;;
  linux) [ "$(uname -s)" = Linux ] || { echo "build-release: linux must be built on Linux" >&2; exit 1; } ;;
  windows) ;;
  *) echo "build-release: unknown target '$target'" >&2; exit 2 ;;
esac

stamped=$(mktemp -d)
trap 'rm -rf "$stamped"' EXIT
scripts/stamp-version.sh "$tag" "$stamped" >/dev/null

(cd frontend && npm ci)

case "$target" in
  macos)
    wails3 task darwin:package:universal INFO_PLIST="$stamped/Info.plist"

    app=bin/lanxfer.app
    archs=$(lipo -archs "$app/Contents/MacOS/lanxfer")
    echo "architectures: $archs"
    case " $archs " in
      *" arm64 "*) ;;
      *) echo "build-release: no arm64 slice in the universal binary" >&2; exit 1 ;;
    esac
    case " $archs " in
      *" x86_64 "*) ;;
      *) echo "build-release: no x86_64 slice in the universal binary" >&2; exit 1 ;;
    esac
    codesign --verify --deep --strict "$app"

    # ditto keeps the bundle intact; --norsrc and --noextattr leave out
    # resource forks and extended attributes, which a plain unzip would turn
    # into stray "._" files inside the signed bundle.
    zip="$out/lanxfer-app_darwin_universal.zip"
    rm -f "$zip"
    ditto -c -k --norsrc --noextattr --keepParent "$app" "$zip"

    # What a user gets with a plain unzip must still have a valid signature.
    check=$(mktemp -d)
    unzip -q "$zip" -d "$check"
    codesign --verify --deep --strict "$check/lanxfer.app"
    rm -rf "$check"
    ;;
  windows)
    wails3 task windows:build ARCH=amd64 INFO_JSON="$stamped/info.json"
    zip="$out/lanxfer-app_windows_amd64.zip"
    rm -f "$zip"
    (cd bin && zip -q "$zip" lanxfer.exe)
    ;;
  linux)
    wails3 task linux:build
    tar -czf "$out/lanxfer-app_linux_amd64.tar.gz" -C bin lanxfer
    ;;
esac
