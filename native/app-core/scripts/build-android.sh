#!/usr/bin/env bash
# Build persona_core for Android with the pinned NDK's LLVM tools, without cargo-ndk.
# The raw libraries retain symbols; the packaged copies are stripped and linked for 16 KB pages.
set -euo pipefail

WORKSPACE=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUTPUT=${1:-"$WORKSPACE/build/android"}
SYMBOLS=${2:-"$WORKSPACE/build/android-symbols"}
REPORTS=${3:-"$WORKSPACE/build/android-reports"}
[[ $OUTPUT != "$SYMBOLS" && $OUTPUT != "$REPORTS" && $SYMBOLS != "$REPORTS" ]] || {
  printf 'Android core build: FAILED (jni, symbols and reports need distinct output roots)\n' >&2; exit 2;
}
case $OUTPUT in /*) ;; *) printf 'Android core build: FAILED (output must be an absolute path)\n' >&2; exit 2 ;; esac
case $SYMBOLS in /*) ;; *) printf 'Android core build: FAILED (symbols must be an absolute path)\n' >&2; exit 2 ;; esac
case $REPORTS in /*) ;; *) printf 'Android core build: FAILED (reports must be an absolute path)\n' >&2; exit 2 ;; esac
printf 'Android core build: outputs jni=%s symbols=%s reports=%s\n' "$OUTPUT" "$SYMBOLS" "$REPORTS"
NDK_VERSION=29.0.14206865
SDK=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
[[ -n $SDK && -d $SDK ]] || { printf 'Android core build: FAILED (set ANDROID_HOME or ANDROID_SDK_ROOT)\n' >&2; exit 2; }
SDK=$(cd "$SDK" && pwd)
NDK=${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-$SDK/ndk/$NDK_VERSION}}
[[ -d $NDK ]] || { printf 'Android core build: FAILED (install NDK %s)\n' "$NDK_VERSION" >&2; exit 2; }
NDK=$(cd "$NDK" && pwd)
EXPECTED=$(sed -n 's/^Pkg.Revision = //p' "$NDK/source.properties")
[[ $EXPECTED == "$NDK_VERSION" ]] || { printf 'Android core build: FAILED (NDK must be %s, found %s)\n' "$NDK_VERSION" "$EXPECTED" >&2; exit 2; }
[[ $(sed -n 's/^channel = "\(.*\)"$/\1/p' "$WORKSPACE/rust-toolchain.toml") == 1.98.0 ]] || {
  printf 'Android core build: FAILED (the reviewed toolchain is Rust 1.98.0)\n' >&2; exit 2;
}
command -v rustup >/dev/null || { printf 'Android core build: FAILED (rustup is required)\n' >&2; exit 2; }
for target in aarch64-linux-android x86_64-linux-android; do
  rustup target list --installed --toolchain 1.98.0 | grep -Fx "$target" >/dev/null ||
    rustup target add --toolchain 1.98.0 "$target"
done
HOST_OS=$(uname -s)
case "$HOST_OS" in
  Linux) NDK_HOST=linux-x86_64 ;;
  Darwin) NDK_HOST=darwin-x86_64 ;;
  *) printf 'Android core build: FAILED (unsupported NDK host OS %s)\n' "$HOST_OS" >&2; exit 2 ;;
esac
LLVM=$NDK/toolchains/llvm/prebuilt/$NDK_HOST
[[ -d $LLVM ]] || { printf 'Android core build: FAILED (NDK %s lacks the %s host tools)\n' "$NDK_VERSION" "$NDK_HOST" >&2; exit 2; }
READELF=$LLVM/bin/llvm-readelf
STRIP=$LLVM/bin/llvm-strip
[[ -x $READELF && -x $STRIP ]] || { printf 'Android core build: FAILED (NDK LLVM tools are missing)\n' >&2; exit 2; }

mkdir -p "$OUTPUT" "$SYMBOLS" "$REPORTS"
OUTPUT=$(cd "$OUTPUT" && pwd)
SYMBOLS=$(cd "$SYMBOLS" && pwd)
REPORTS=$(cd "$REPORTS" && pwd)
# This task owns its output: do not leave an old unstripped copy or report in the packaging directory.
for abi in arm64-v8a x86_64; do
  # Keep only the packaged ABI directory and its stripped library; no symbol or report file is a JNI input.
  for stale in "$OUTPUT/$abi"/*; do
    [[ -e $stale ]] || continue
    [[ $stale == "$OUTPUT/$abi/libpersona_core.so" ]] || rm -R -- "$stale"
  done
done
# Do not inherit a host's linker/ar overrides: Android cross builds use only the reviewed NDK tools.
unset CARGO_TARGET_AARCH64_LINUX_ANDROID_LINKER CARGO_TARGET_AARCH64_LINUX_ANDROID_AR
unset CARGO_TARGET_X86_64_LINUX_ANDROID_LINKER CARGO_TARGET_X86_64_LINUX_ANDROID_AR
unset CARGO_TARGET_AARCH64_LINUX_ANDROID_RUSTFLAGS CARGO_TARGET_X86_64_LINUX_ANDROID_RUSTFLAGS
unset CARGO_TARGET_AARCH64_LINUX_ANDROID_RUNNER CARGO_TARGET_X86_64_LINUX_ANDROID_RUNNER
unset RUSTFLAGS CARGO_ENCODED_RUSTFLAGS
export CARGO_TARGET_AARCH64_LINUX_ANDROID_LINKER=$LLVM/bin/aarch64-linux-android30-clang
export CARGO_TARGET_AARCH64_LINUX_ANDROID_AR=$LLVM/bin/llvm-ar
export CARGO_TARGET_X86_64_LINUX_ANDROID_LINKER=$LLVM/bin/x86_64-linux-android30-clang
export CARGO_TARGET_X86_64_LINUX_ANDROID_AR=$LLVM/bin/llvm-ar
# Explicitly request 16 KB maximum page size for every linked dependency, not just the final .so.
export CARGO_TARGET_AARCH64_LINUX_ANDROID_RUSTFLAGS='-Clink-arg=-Wl,-z,max-page-size=16384'
export CARGO_TARGET_X86_64_LINUX_ANDROID_RUSTFLAGS='-Clink-arg=-Wl,-z,max-page-size=16384'

for pair in 'arm64-v8a:aarch64-linux-android' 'x86_64:x86_64-linux-android'; do
  abi=${pair%%:*}
  target=${pair#*:}
  printf 'Android core build: building %s (%s)\n' "$abi" "$target"
  (cd "$WORKSPACE" && cargo +1.98.0 build --locked --release --target "$target" -p persona-core-ffi)
  raw=$WORKSPACE/target/$target/release/libpersona_core.so
  destination=$OUTPUT/$abi
  mkdir -p "$destination" "$SYMBOLS/$abi" "$REPORTS"
  cp "$raw" "$SYMBOLS/$abi/libpersona_core.so"
  cp "$raw" "$destination/libpersona_core.so"
  "$STRIP" --strip-unneeded "$destination/libpersona_core.so"
  "$READELF" --file-header --program-headers "$destination/libpersona_core.so" > "$REPORTS/$abi-readelf.txt"
  "$READELF" --file-header --program-headers "$SYMBOLS/$abi/libpersona_core.so" > "$REPORTS/$abi-readelf-unstripped.txt"
  python3 -I - "$REPORTS/$abi-readelf.txt" "$REPORTS/$abi-readelf-unstripped.txt" "$abi" <<'PY'
import re
import sys
from pathlib import Path

report = Path(sys.argv[1]).read_text()
unstripped = Path(sys.argv[2]).read_text()
abi = sys.argv[3]
if re.search(r'^\s*Machine:\s*(.+)$', report, re.M).group(1) != re.search(r'^\s*Machine:\s*(.+)$', unstripped, re.M).group(1):
    raise SystemExit('Android core build: FAILED (stripping changed the ELF machine for ' + abi + ')')
loads = re.findall(r'^\s*LOAD\s.*$', report, re.M)
if not loads or any(int(line.split()[-1], 16) != 0x4000 for line in loads):
    raise SystemExit('Android core build: FAILED (' + abi + ' has a non-16-KB LOAD alignment)')
print('Android core build: ELF PASS (%s, %d LOAD segments, align 0x4000)' % (abi, len(loads)))
PY
  # ELF reports and stripped libraries are evidence; unstripped symbols are retained separately for release archival.
done
printf 'Android core build: PASSED (arm64-v8a and x86_64; NDK %s; Rust 1.98.0)\n' "$NDK_VERSION"
printf 'Android core size: use android-package-report.py with the assembled APK for raw, stripped, gzip and package bytes.\n'
