#!/usr/bin/env bash
# The shared Rust core (native/app-core, docs/plans/plan-rust-core.md): formatting, lints, unit tests, the shared
# fixtures it implements, the generated client codecs up to date, the HarmonyOS binding's lints and notices, and the
# release library. Requires rustup; the pinned toolchain and the HarmonyOS Rust target are installed when missing.
# No private config, SDKs or devices.
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_rust_toolchain
cd "$CI_ROOT/native/app-core"
ci_run 'Rust formatting' 300 cargo fmt --all --check
ci_run 'Rust lints' 1200 cargo clippy --locked --all-targets -- -D warnings
ci_run 'Rust tests and shared fixtures' 1200 cargo test --locked
ci_run 'Generated codecs up to date' 900 cargo run --locked --quiet -p persona-core-typegen -- --check
# The NAPI binding exists only behind the `ohos` feature, which the host build above never compiles. Checking it for
# the device target needs the Rust target but no SDK: nothing is linked, so OHOS_NDK_HOME (read by napi-build-ohos
# for its link directories) only has to be set.
ci_run 'HarmonyOS Rust target' 600 rustup target add aarch64-unknown-linux-ohos
ci_run 'HarmonyOS binding lints' 1200 env OHOS_NDK_HOME="$CI_ROOT/native/app-core/target/no-ohos-sdk" cargo clippy --locked --target aarch64-unknown-linux-ohos -p persona-core-ohos --features ohos -- -D warnings
# Host cargo check expands napi-derive but never links NAPI. The generator sorts its JSON-lines function records into
# index.d.ts's stable order, maps Buffer to Uint8Array and rejects error-first / non-Buffer callback signatures.
ci_run 'HarmonyOS NAPI declarations' 1200 python3 -I scripts/ohos-napi-defs.py --check
ci_run 'HarmonyOS NAPI declaration tests' 300 python3 -I scripts/test_ohos_napi_defs.py
# Writes the notices the HAP packages (an ignored build output) from Cargo.lock and the registry sources the step
# above unpacked. It fails when a locked package has no notice or a vendored upstream notice changed.
ci_run 'HarmonyOS core licenses' 300 python3 -I scripts/android-licenses.py --workspace . --registry-src "${CARGO_HOME:-$HOME/.cargo}/registry/src" --output "$CI_ROOT/harmony/build/core-licenses" --crate persona-core-ohos --target aarch64-unknown-linux-ohos --features ohos --label HarmonyOS
# The generated ArkTS codec is otherwise only checked against itself. This replay runs the continuity corpus through
# the real runtime (persona-core-ohos's registry, the code under the NAPI entry points), with ArkTS encoding the
# actions and decoding the effects and patches, plus byte-equality samples built independently in Rust. Host evidence
# only: no NAPI library, HAP or device. It needs Node.js 22.15+ (module.registerHooks / stripTypeScriptTypes), which
# .github/workflows/core-test.yml installs; the portable harmony/tests/*.test.mjs glob deliberately excludes it.
ci_run 'ArkTS host replay helper' 600 cargo build --locked -p persona-core-ohos --example host_replay
ci_run 'ArkTS core host tests' 900 env PERSONA_CORE_HOST_REPLAY="$CI_ROOT/native/app-core/target/debug/examples/host_replay" node --test "$CI_ROOT/harmony/tests/rustcore-wire.test.mjs" "$CI_ROOT/harmony/tests/rustcore-adapter.test.mjs" "$CI_ROOT/harmony/tests/native-core-async.test.mjs" "$CI_ROOT/harmony/tests/core-host/replay.test.mjs" "$CI_ROOT/harmony/tests/core-host/transport.test.mjs" "$CI_ROOT/harmony/tests/core-host/sort.test.mjs"
ci_run 'Release library' 1200 cargo build --locked --release -p persona-core-ffi
