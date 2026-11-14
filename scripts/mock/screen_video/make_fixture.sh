#!/bin/bash
# Makes scripts/mock/screen_video/fixture.bin (HEVC, else H.264) and fixture-h264.bin again (the mock hub's live-screen
# video, #3954 / #6558): builds make_fixture.swift with PersonaScreenKit's sources on this Mac and runs it. Needs Xcode
# (VideoToolbox's encoders).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
xcrun swiftc -O -parse-as-library -o "$tmp/make_fixture" "$here/make_fixture.swift" \
  "$root"/apple/Packages/PersonaScreenKit/Sources/PersonaScreenKit/*.swift
"$tmp/make_fixture" "$here/fixture.bin"
"$tmp/make_fixture" "$here/fixture-h264.bin" h264
