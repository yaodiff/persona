#!/usr/bin/env bash
# Build the PersonaCore XCFramework from the checked-in Rust core. No binary is committed: the package's XCFramework
# and native/app-core/target are ignored. The simulator slice follows this Mac's architecture; device, simulator and
# macOS are present so both the iOS XCTest host and macOS `swift test` exercise the same C ABI.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
CORE="$ROOT/native/app-core"
PACKAGE="$ROOT/apple/Packages/PersonaCore"
DEST="$PACKAGE/build/PersonaCoreC.xcframework"
TOOLCHAIN=$(sed -n 's/^channel = "\(.*\)"$/\1/p' "$CORE/rust-toolchain.toml")
[ -n "$TOOLCHAIN" ] || { printf 'persona-core-apple: FAIL missing pinned Rust toolchain\n' >&2; exit 1; }
command -v rustup >/dev/null || { printf 'persona-core-apple: FAIL rustup is required\n' >&2; exit 1; }
command -v xcodebuild >/dev/null || { printf 'persona-core-apple: FAIL Xcode is required\n' >&2; exit 1; }
rustup run "$TOOLCHAIN" cargo --version >/dev/null 2>&1 ||
  rustup toolchain install "$TOOLCHAIN" --profile minimal --component rustfmt,clippy
case "$(uname -m)" in
  arm64) SIM_TARGET=aarch64-apple-ios-sim ;;
  x86_64) SIM_TARGET=x86_64-apple-ios ;;
  *) printf 'persona-core-apple: FAIL unsupported Mac architecture %s\n' "$(uname -m)" >&2; exit 1 ;;
esac
TARGETS="aarch64-apple-ios $SIM_TARGET $(rustc +"$TOOLCHAIN" -vV | sed -n 's/^host: //p')"
# shellcheck disable=SC2086
rustup target add --toolchain "$TOOLCHAIN" $TARGETS
STAMP="$PACKAGE/build/Inputs.stamp"
mkdir -p "$PACKAGE/build"
INPUT="$PACKAGE/build/Inputs.stamp.next"
trap 'rm -f "$INPUT"' EXIT
{
  find "$CORE/crates/persona-core" "$CORE/crates/persona-core-runtime" "$CORE/crates/persona-core-ffi" \
    -type f -print
  printf '%s\n' "$CORE/Cargo.lock" "$CORE/Cargo.toml" "$CORE/rust-toolchain.toml" "$0"
} | sort | while IFS= read -r source; do
  printf '%s\0' "${source#"$ROOT/"}"
  shasum -a 256 "$source"
done | shasum -a 256 > "$INPUT"
if [ -f "$STAMP" ] && cmp -s "$INPUT" "$STAMP" && [ -d "$DEST" ]; then
  printf 'persona-core-apple: cached XCFramework is current\n'
  exit 0
fi
export CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-$CORE/target/apple}"
# Panic metadata from dependencies names their registry source paths. Keep the build machine's home and checkout
# paths out of the published XCFramework, including when a caller supplies its own CARGO_HOME.
export RUSTFLAGS="${RUSTFLAGS:+$RUSTFLAGS }--remap-path-prefix=$HOME=/build/home --remap-path-prefix=$ROOT=/build/persona"
for TARGET in $TARGETS; do
  printf 'persona-core-apple: building %s\n' "$TARGET"
  SDKROOT=$(xcrun --sdk macosx --show-sdk-path)
  case "$TARGET" in
    aarch64-apple-ios) SDKROOT=$(xcrun --sdk iphoneos --show-sdk-path) ;;
    *-apple-ios*) SDKROOT=$(xcrun --sdk iphonesimulator --show-sdk-path) ;;
  esac
  export SDKROOT
  cargo +"$TOOLCHAIN" build --manifest-path "$CORE/Cargo.toml" --locked --release -p persona-core-ffi --target "$TARGET"
done
HEADERS=$(mktemp -d "${TMPDIR:-/tmp}/persona-core-apple-headers.XXXXXX")
trap 'rm -f "$INPUT"; rm -rf "$HEADERS"' EXIT
cp "$CORE/crates/persona-core-ffi/include/persona_core.h" "$HEADERS/persona_core.h"
cat > "$HEADERS/module.modulemap" <<'EOF'
module PersonaCoreC {
    header "persona_core.h"
    export *
}
EOF
ARGS=()
for TARGET in $TARGETS; do
  ARGS+=(-library "$CARGO_TARGET_DIR/$TARGET/release/libpersona_core.a" -headers "$HEADERS")
done
rm -rf "$DEST"
xcodebuild -create-xcframework "${ARGS[@]}" -output "$DEST"
cp "$INPUT" "$STAMP"
printf 'persona-core-apple: XCFramework ready at %s\n' "$DEST"
