#!/usr/bin/env bash
# Build the PersonaCore XCFramework from the checked-in Rust core. No binary is committed: the package's XCFramework
# and native/app-core/target are ignored. On a Mac the simulator slice follows the Mac's architecture.
#
#   native/app-core/scripts/build-apple.sh               # device, simulator and macOS: release, CI, `swift test`
#   native/app-core/scripts/build-apple.sh --simulator   # the simulator slice only (scripts/app/sim.sh)
#
# The default builds all three slices, so both the iOS XCTest host and macOS `swift test` exercise the same C ABI.
# A simulator-only framework links for no phone and no "My Mac (Designed for iPad)": Xcode stops with "no library for
# this platform was found". The script says so whenever it leaves one; running it with no flag adds the other slices.
# Built frameworks are kept in a cache shared by every worktree on the machine (#10713), keyed by a hash of the
# inputs (sources, Cargo files, toolchain, this script, the Mac's architecture, the Apple SDKs and the caller's
# RUSTFLAGS and deployment targets), never by the checkout's path: a worktree at the same Rust inputs copies the
# framework instead of building it. A full entry also serves a --simulator request. PERSONA_APP_CORE_CACHE moves
# the cache (default ~/Library/Caches/persona/app-core; on Linux persona/app-core under $XDG_CACHE_HOME or ~/.cache),
# PERSONA_APP_CORE_CACHE=0 turns it off, and only the newest PERSONA_APP_CORE_CACHE_KEEP keys (default 3) are kept. The
# cache is best effort: when an entry cannot be read, or the cache cannot be written, the script says so in one line on
# stderr and builds, or keeps, the worktree's copy as it would without a cache.
#
# On Linux, for a Linux Apple toolchain that builds the iOS app without Xcode (#16944), the
# same commands build the device and arm64 simulator slices whatever the machine's architecture (every Apple Rust
# target is a cross target there, and such a toolchain builds for the arm64 simulator), and no macOS slice: macOS
# `swift test` needs a Mac, and the script says so in one line. With no Apple linker and no xcodebuild, cargo builds
# the crate without its cdylib (`cargo rustc --crate-type staticlib,rlib`), and the script writes the XCFramework
# itself, laid out as `xcodebuild -create-xcframework` lays it out. The key takes the Apple SDKs' build versions from
# the toolchain's copy of the Xcode SDKs, found as apple-build finds it (APPLE_BUILD_SDK_ROOT, else sdk_root in its
# config.json), each `none` when it cannot be read, as when xcrun fails on a Mac. The cache, the stamp and --simulator
# work as on a Mac. Any other system is refused.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
CORE="$ROOT/native/app-core"
SCRIPT="$CORE/scripts/build-apple.sh"
PACKAGE="$ROOT/apple/Packages/PersonaCore"
DEST="$PACKAGE/build/PersonaCoreC.xcframework"
STAMP="$PACKAGE/build/Inputs.stamp"
fail() { printf 'persona-core-apple: FAIL %s\n' "$*" >&2; exit 1; }
SLICES=all
for arg in "$@"; do
  case "$arg" in
    --simulator) SLICES=simulator ;;
    *) printf 'usage: %s [--simulator]\n' "$0" >&2; exit 2 ;;
  esac
done
TOOLCHAIN=$(sed -n 's/^channel = "\(.*\)"$/\1/p' "$CORE/rust-toolchain.toml")
[ -n "$TOOLCHAIN" ] || fail "missing pinned Rust toolchain"
HOST_OS=$(uname -s)
case "$HOST_OS" in
  Darwin)
    command -v xcodebuild >/dev/null || fail "Xcode is required"
    case "$(uname -m)" in
      arm64) SIM_TARGET=aarch64-apple-ios-sim ;;
      x86_64) SIM_TARGET=x86_64-apple-ios ;;
      *) fail "unsupported Mac architecture $(uname -m)" ;;
    esac
    ;;
  Linux)
    # python3 reads the SDK metadata and writes the XCFramework's Info.plist.
    command -v python3 >/dev/null || fail "python3 is required"
    SIM_TARGET=aarch64-apple-ios-sim
    ;;
  *) fail "unsupported system $HOST_OS: build on macOS with Xcode, or on Linux with a Linux Apple toolchain" ;;
esac
# shasum comes with perl, which a slim Linux may lack; coreutils' sha256sum prints the same line, so the same key.
if command -v shasum >/dev/null; then
  sha256() { shasum -a 256; }
elif command -v sha256sum >/dev/null; then
  sha256() { sha256sum; }
else
  fail "shasum or sha256sum is required"
fi
# The last line for a worktree left with the simulator slice only, or on Linux without the macOS slice ($1: the slices
# it has).
note_slices() {
  if [ "$1" = simulator ]; then
    printf 'persona-core-apple: simulator slice only; for a phone or "My Mac (Designed for iPad)" in Xcode, first run %s\n' \
      "native/app-core/scripts/build-apple.sh (no flag)"
  elif [ "$HOST_OS" = Linux ]; then
    printf 'persona-core-apple: no macOS slice on Linux; macOS swift test needs a Mac\n'
  fi
}
CLEANUP=()
cleanup() {
  local path
  for path in ${CLEANUP[@]+"${CLEANUP[@]}"}; do rm -rf "$path" 2>/dev/null || true; done
}
trap cleanup EXIT

# The SDK lines of the key on Linux: each SDK's ProductBuildVersion in the toolchain's copy of the Xcode SDKs, the
# value xcrun --show-sdk-build-version reports on a Mac. The copy is found as apple-build finds it, so the key follows
# the SDK the app is built against; <Platform>.sdk is the SDK's own folder in Xcode (the versioned name links to it)
# and a name apple-build's sdk-import always links. Anything unreadable is `none`, as when xcrun fails on a Mac.
linux_sdk_builds() {
  python3 -I - <<'EOF'
import json, os, plistlib
from pathlib import Path

try:
    root = os.environ.get('APPLE_BUILD_SDK_ROOT')
    if not root:
        config = Path(os.environ.get('XDG_CONFIG_HOME') or '~/.config').expanduser() / 'apple-build/config.json'
        root = json.loads(config.read_text(encoding='utf-8')).get('sdk_root')
    platforms = Path(root).expanduser() / 'Developer/Platforms'
except Exception:
    platforms = None
for sdk, name in (('iphoneos', 'iPhoneOS'), ('iphonesimulator', 'iPhoneSimulator'), ('macosx', 'MacOSX')):
    try:
        version = '%s.platform/Developer/SDKs/%s.sdk/System/Library/CoreServices/SystemVersion.plist' % (name, name)
        build = plistlib.loads((platforms / version).read_bytes())['ProductBuildVersion'].strip()
    except Exception:
        build = ''
    print('sdk %s %s' % (sdk, build or 'none'))
EOF
}

# The key: every input's path relative to the checkout, a NUL (which no path holds) and its content's hash, then what the
# environment changes about the output. Never an absolute path, or no two worktrees would share an entry. The stream is
# hashed in the pipeline, never captured first: a command substitution cannot hold a NUL, and bash 4.4+ drops it with
# an "ignored null byte" warning on every run.
KEY=$(
  {
    {
      find "$CORE/crates/persona-core" "$CORE/crates/persona-core-runtime" "$CORE/crates/persona-core-ffi" -type f -print
      printf '%s\n' "$CORE/Cargo.lock" "$CORE/Cargo.toml" "$CORE/rust-toolchain.toml" "$SCRIPT"
    } | LC_ALL=C sort | while IFS= read -r source; do
      printf '%s\0' "${source#"$ROOT/"}"
      sha256 < "$source"
    done
    if [ "$HOST_OS" = Darwin ]; then
      printf 'arch %s\n' "$(uname -m)"
      for sdk in iphoneos iphonesimulator macosx; do
        printf 'sdk %s %s\n' "$sdk" "$(xcrun --sdk "$sdk" --show-sdk-build-version 2>/dev/null || echo none)"
      done
    else
      # A Linux framework has no macOS slice, so its key never matches a Mac's in a cache both can reach.
      printf 'arch %s %s\n' "$HOST_OS" "$(uname -m)"
      linux_sdk_builds
    fi
    printf 'RUSTFLAGS %s\nIPHONEOS_DEPLOYMENT_TARGET %s\nMACOSX_DEPLOYMENT_TARGET %s\n' \
      "${RUSTFLAGS:-}" "${IPHONEOS_DEPLOYMENT_TARGET:-}" "${MACOSX_DEPLOYMENT_TARGET:-}"
  } | sha256 | cut -c1-64
)
mkdir -p "$PACKAGE/build"

# The worktree's own copy: current when it was made from this key and has the slices asked for.
if [ -d "$DEST" ] && [ -f "$STAMP" ]; then
  read -r have_key have_slices < "$STAMP" || true
  if [ "${have_key:-}" = "$KEY" ] && { [ "${have_slices:-}" = all ] || [ "${have_slices:-}" = "$SLICES" ]; }; then
    printf 'persona-core-apple: XCFramework is current (%s)\n' "${have_slices:-}"
    note_slices "${have_slices:-}"
    exit 0
  fi
fi

# Copies the framework at $1 to $2: a clone where the file system can (APFS, one volume), else a plain copy (GNU cp
# has no -c). Quiet: each caller says in one line what a failure means.
copy_framework() {
  cp -cR "$1" "$2" 2>/dev/null || { rm -rf "$2" 2>/dev/null; cp -R "$1" "$2" 2>/dev/null; }
}
# Puts the framework at $1 (moved: copy it first if it must stay) in the worktree and records what it is ($2: its slices).
install_framework() {
  rm -f "$STAMP"
  rm -rf "$DEST"
  mv "$1" "$DEST"
  printf '%s %s\n' "$KEY" "$2" > "$STAMP"
}

CACHE="${PERSONA_APP_CORE_CACHE:-}"
if [ -z "$CACHE" ]; then
  if [ "$(uname -s)" = Darwin ]; then CACHE="$HOME/Library/Caches/persona/app-core"; else CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/persona/app-core"; fi
elif [ "$CACHE" = 0 ]; then
  CACHE=
fi
KEEP="${PERSONA_APP_CORE_CACHE_KEEP:-3}"
case "$KEEP" in ''|*[!0-9]*|0) fail "PERSONA_APP_CORE_CACHE_KEEP must be a positive number" ;; esac
ENTRY=${CACHE:+$CACHE/$KEY}
if [ -n "$ENTRY" ]; then
  # A --simulator request takes its own entry first (a third of the copy), else a full one.
  candidates=all
  [ "$SLICES" = simulator ] && candidates="simulator all"
  for slices in $candidates; do
    [ -f "$ENTRY/$slices/PersonaCoreC.xcframework/Info.plist" ] || continue
    next="$PACKAGE/build/.PersonaCoreC.xcframework.$$"
    CLEANUP+=("$next")
    rm -rf "$next"
    if copy_framework "$ENTRY/$slices/PersonaCoreC.xcframework" "$next"; then
      # The newest stay when the cache is pruned; a cache this user cannot write is still read.
      touch "$ENTRY" 2>/dev/null || true
      install_framework "$next" "$slices"
      printf 'persona-core-apple: XCFramework (%s) from the shared cache %s\n' "$slices" "$ENTRY"
      note_slices "$slices"
      exit 0
    fi
    rm -rf "$next"
    printf 'persona-core-apple: could not copy %s from the shared cache; building it\n' "$ENTRY/$slices" >&2
    break
  done
fi

command -v rustup >/dev/null || fail "rustup is required"
rustup run "$TOOLCHAIN" cargo --version >/dev/null 2>&1 ||
  rustup toolchain install "$TOOLCHAIN" --profile minimal --component rustfmt,clippy
if [ "$SLICES" = simulator ]; then
  TARGETS="$SIM_TARGET"
elif [ "$HOST_OS" = Darwin ]; then
  TARGETS="aarch64-apple-ios $SIM_TARGET $(rustc +"$TOOLCHAIN" -vV | sed -n 's/^host: //p')"
else
  TARGETS="aarch64-apple-ios $SIM_TARGET"
fi
# shellcheck disable=SC2086
rustup target add --toolchain "$TOOLCHAIN" $TARGETS
export CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-$CORE/target/apple}"
# Panic metadata from dependencies names their registry source paths. Keep the build machine's home and checkout
# paths out of the published XCFramework, including when a caller supplies its own CARGO_HOME. A home or checkout
# reached through a symbolic link is remapped as it really is too: rustup and cargo hand rustc the real path. The
# checkout's flags come last, so they win for its files, which usually lie in the home.
remap() { # $1: a path, $2: what it becomes
  local real
  printf -- '--remap-path-prefix=%s=%s' "$1" "$2"
  real=$(cd "$1" 2>/dev/null && pwd -P) || return 0
  [ "$real" = "$1" ] || printf -- ' --remap-path-prefix=%s=%s' "$real" "$2"
}
RUSTFLAGS="${RUSTFLAGS:+$RUSTFLAGS }$(remap "$HOME" /build/home) $(remap "$ROOT" /build/persona)"
export RUSTFLAGS
for TARGET in $TARGETS; do
  printf 'persona-core-apple: building %s\n' "$TARGET"
  if [ "$HOST_OS" = Linux ]; then
    # The crate's crate types less the cdylib, which needs an Apple linker. The rlib stays for what it does to the
    # static library: with an rlib among them, cargo runs none of the profile's fat LTO, as on a Mac, so the library
    # holds the same objects as the Mac's (a staticlib alone would be one LTO-merged object). No dependency compiles
    # C, so nothing reads an SDK and SDKROOT is left as it is; like the Mac's build, this sets no deployment target:
    # both take rustc's default, or the caller's IPHONEOS_DEPLOYMENT_TARGET (part of the key).
    cargo +"$TOOLCHAIN" rustc --manifest-path "$CORE/Cargo.toml" --locked --release -p persona-core-ffi --lib \
      --target "$TARGET" --crate-type staticlib,rlib
    continue
  fi
  SDKROOT=$(xcrun --sdk macosx --show-sdk-path)
  case "$TARGET" in
    aarch64-apple-ios) SDKROOT=$(xcrun --sdk iphoneos --show-sdk-path) ;;
    *-apple-ios*) SDKROOT=$(xcrun --sdk iphonesimulator --show-sdk-path) ;;
  esac
  export SDKROOT
  cargo +"$TOOLCHAIN" build --manifest-path "$CORE/Cargo.toml" --locked --release -p persona-core-ffi --target "$TARGET"
done
HEADERS=$(mktemp -d "${TMPDIR:-/tmp}/persona-core-apple-headers.XXXXXX")
CLEANUP+=("$HEADERS")
cp "$CORE/crates/persona-core-ffi/include/persona_core.h" "$HEADERS/persona_core.h"
cat > "$HEADERS/module.modulemap" <<'EOF'
module PersonaCoreC {
    header "persona_core.h"
    export *
}
EOF
# What `xcodebuild -create-xcframework` makes of these libraries, written without Xcode ($1: the framework's path): a
# folder per slice with the library and the headers, and an Info.plist listing the slices with the keys Xcode writes
# for a static library, BinaryPath among them.
write_xcframework() {
  local target slice slices=()
  mkdir "$1"
  for target in $TARGETS; do
    case "$target" in
      aarch64-apple-ios) slice=ios-arm64 ;;
      aarch64-apple-ios-sim) slice=ios-arm64-simulator ;;
      *) fail "no XCFramework slice for $target" ;;
    esac
    mkdir -p "$1/$slice/Headers"
    cp "$CARGO_TARGET_DIR/$target/release/libpersona_core.a" "$1/$slice/libpersona_core.a"
    cp "$HEADERS/persona_core.h" "$HEADERS/module.modulemap" "$1/$slice/Headers/"
    slices+=("$slice")
  done
  python3 -I - "$1/Info.plist" "${slices[@]}" <<'EOF'
import plistlib, sys

libraries = []
for identifier in sys.argv[2:]:
    library = {'BinaryPath': 'libpersona_core.a', 'HeadersPath': 'Headers', 'LibraryIdentifier': identifier,
               'LibraryPath': 'libpersona_core.a', 'SupportedArchitectures': ['arm64'], 'SupportedPlatform': 'ios'}
    if identifier.endswith('-simulator'):
        library['SupportedPlatformVariant'] = 'simulator'
    libraries.append(library)
with open(sys.argv[1], 'wb') as plist:
    plistlib.dump({'AvailableLibraries': libraries, 'CFBundlePackageType': 'XFWK', 'XCFrameworkFormatVersion': '1.0'},
                  plist)
EOF
}
BUILT="$PACKAGE/build/.PersonaCoreC.built.$$"
CLEANUP+=("$BUILT")
rm -rf "$BUILT"
mkdir -p "$BUILT"
if [ "$HOST_OS" = Linux ]; then
  write_xcframework "$BUILT/PersonaCoreC.xcframework"
else
  ARGS=()
  for TARGET in $TARGETS; do
    ARGS+=(-library "$CARGO_TARGET_DIR/$TARGET/release/libpersona_core.a" -headers "$HEADERS")
  done
  xcodebuild -create-xcframework "${ARGS[@]}" -output "$BUILT/PersonaCoreC.xcframework"
fi

# Into the cache under a private name, then renamed into place: a reader never sees half a framework, and when another
# worktree published the same key meanwhile, its entry stays and this copy goes. Every step that can fail returns
# (errexit does not apply in a function called as a condition): a cache that cannot be written costs the next worktree
# a build, never this one its framework.
store_in_cache() {
  local staged="$ENTRY/.$SLICES.$$"
  mkdir -p "$ENTRY" 2>/dev/null || return 1
  CLEANUP+=("$staged")
  rm -rf "$staged" 2>/dev/null || return 1
  mkdir "$staged" 2>/dev/null || return 1
  copy_framework "$BUILT/PersonaCoreC.xcframework" "$staged/PersonaCoreC.xcframework" || return 1
  if [ ! -e "$ENTRY/$SLICES" ]; then
    mv "$staged" "$ENTRY/$SLICES" 2>/dev/null || return 1
    # Lost a race after the check: mv put this copy inside the winner's entry.
    rm -rf "$ENTRY/$SLICES/.$SLICES.$$" 2>/dev/null || true
  fi
  touch "$ENTRY" 2>/dev/null || true
  # Only the newest keys stay; this one was just touched. Anything else in the folder is left alone.
  # shellcheck disable=SC2012
  ls -1t "$CACHE" 2>/dev/null | { grep -E '^[0-9a-f]{64}$' || true; } | tail -n +"$((KEEP + 1))" | while IFS= read -r old; do
    rm -rf "${CACHE:?}/$old" 2>/dev/null || true
  done
  return 0
}
if [ -n "$ENTRY" ] && ! store_in_cache; then
  printf 'persona-core-apple: could not write the shared cache %s; the XCFramework stays in this worktree only\n' \
    "$CACHE" >&2
fi
install_framework "$BUILT/PersonaCoreC.xcframework" "$SLICES"
printf 'persona-core-apple: XCFramework (%s) ready at %s\n' "$SLICES" "$DEST"
note_slices "$SLICES"
