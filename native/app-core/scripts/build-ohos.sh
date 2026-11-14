#!/bin/sh
set -eu

: "${OHOS_SDK_NATIVE:?Set OHOS_SDK_NATIVE to the API 26 SDK native directory}"
CORE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
for required in llvm/bin/clang llvm/bin/clang++ llvm/bin/llvm-ar build/cmake/ohos.toolchain.cmake; do
  [ -f "$OHOS_SDK_NATIVE/$required" ] || { printf 'Missing SDK component: %s\n' "$required" >&2; exit 1; }
done
export OHOS_SDK_NATIVE
# napi-build-ohos takes its link directories from OHOS_NDK_HOME, the directory that holds `native`.
if [ -z "${OHOS_NDK_HOME:-}" ]; then OHOS_NDK_HOME=$(CDPATH= cd -- "$OHOS_SDK_NATIVE/.." && pwd); fi
[ -d "$OHOS_NDK_HOME/native/sysroot" ] || { printf 'OHOS_NDK_HOME (%s) has no native/sysroot; set it to the directory that holds the SDK'"'"'s native directory\n' "$OHOS_NDK_HOME" >&2; exit 1; }
export OHOS_NDK_HOME
if [ -z "${RUSTC:-}" ]; then RUSTC=$(rustup which --toolchain 1.98.0 rustc); export RUSTC; fi
if [ -z "${RUSTDOC:-}" ]; then RUSTDOC=$(rustup which --toolchain 1.98.0 rustdoc); export RUSTDOC; fi
export CARGO_NET_OFFLINE=true
export CARGO_TARGET_AARCH64_UNKNOWN_LINUX_OHOS_LINKER="$CORE_DIR/toolchain/clang-ohos.sh"
export CC_aarch64_unknown_linux_ohos="$CORE_DIR/toolchain/clang-ohos.sh"
export CXX_aarch64_unknown_linux_ohos="$CORE_DIR/toolchain/clangxx-ohos.sh"
export AR_aarch64_unknown_linux_ohos="$OHOS_SDK_NATIVE/llvm/bin/llvm-ar"
export CMAKE_TOOLCHAIN_FILE_aarch64_unknown_linux_ohos="$OHOS_SDK_NATIVE/build/cmake/ohos.toolchain.cmake"
if [ -z "${LIBCLANG_PATH:-}" ]; then LIBCLANG_PATH="$OHOS_SDK_NATIVE/llvm/lib"; export LIBCLANG_PATH; fi
if [ -z "${CLANG_PATH:-}" ]; then CLANG_PATH="$OHOS_SDK_NATIVE/llvm/bin/clang"; export CLANG_PATH; fi
export PATH="$OHOS_SDK_NATIVE/build-tools/cmake/bin:$OHOS_SDK_NATIVE/build-tools/ninja:$PATH"
cd "$CORE_DIR"
rustup run 1.98.0 cargo build --locked --offline --release --target aarch64-unknown-linux-ohos -p persona-core-ohos --features ohos
python3 -I "$CORE_DIR/scripts/package-ohos-fixtures.py" --fixtures "$CORE_DIR/../../spec/fixtures/continuity" --output "$CORE_DIR/../../harmony/build/core-fixtures"
python3 -I "$CORE_DIR/scripts/android-licenses.py" --workspace "$CORE_DIR" --registry-src "${CARGO_HOME:-$HOME/.cargo}/registry/src" --output "$CORE_DIR/../../harmony/build/core-licenses" --crate persona-core-ohos --target aarch64-unknown-linux-ohos --features ohos --label HarmonyOS
printf 'OHOS core build and test resources: passed (library in target/aarch64-unknown-linux-ohos/release)\n'
