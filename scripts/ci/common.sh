#!/bin/bash
set -euo pipefail
CI_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$CI_ROOT"
ci_run() {
  local label="$1" seconds="$2"
  shift 2
  python3 "$CI_ROOT/scripts/ci/run.py" --label "$label" --timeout "$seconds" -- "$@"
}
# Installs the shared Rust core's pinned toolchain (native/app-core/rust-toolchain.toml) when it is missing.
ci_rust_toolchain() {
  local channel
  channel=$(sed -n 's/^channel = "\(.*\)"$/\1/p' "$CI_ROOT/native/app-core/rust-toolchain.toml")
  [ -n "$channel" ] || { printf 'ci: FAIL cannot read native/app-core/rust-toolchain.toml\n' >&2; return 1; }
  command -v rustup >/dev/null || { printf 'ci: FAIL rustup is required (https://rustup.rs)\n' >&2; return 1; }
  rustup run "$channel" cargo --version >/dev/null 2>&1 ||
    rustup toolchain install "$channel" --profile minimal --component rustfmt,clippy
}
