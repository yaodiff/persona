#!/bin/bash
# Blocking calls on the main actor in the Apple sources (docs/apple/performance.md, "Automated checks").
#
#   scripts/test/isolation-lint.sh                check apple/ against scripts/test/apple_isolation_baseline.json
#   scripts/test/isolation-lint.sh --update       rewrite the baseline after moving calls off the main actor (never up)
#   scripts/test/isolation-lint.sh --list --why   every call, where it runs and why
#   scripts/test/isolation-lint.sh test           the lint's own tests
#
# Builds the SwiftSyntax package in scripts/test/isolation-lint into build/isolation-lint (ISOLATION_LINT_BUILD
# changes that): about two minutes the first time, seconds afterwards. Needs Xcode 26.0 or newer and XcodeGen.
# The last line printed starts with `isolation-lint: PASS` or `isolation-lint: FAIL`.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PACKAGE="$ROOT/scripts/test/isolation-lint"
SCRATCH="${ISOLATION_LINT_BUILD:-$ROOT/build/isolation-lint}"
for tool in xcrun xcodegen; do
  command -v "$tool" >/dev/null || { printf 'isolation-lint: FAIL missing %s\n' "$tool"; exit 2; }
done

if [ "${1:-}" = test ]; then
  shift
  # Without testable imports (the tests use the public API), the tests and the check below share one build of
  # swift-syntax; with them, each would rebuild it from scratch after the other.
  if xcrun swift test -c release --disable-testable-imports --package-path "$PACKAGE" --scratch-path "$SCRATCH" "$@"; then
    printf 'isolation-lint: PASS its own tests\n'
    exit 0
  fi
  printf 'isolation-lint: FAIL its own tests\n'
  exit 1
fi

LOG=$(mktemp "${TMPDIR:-/tmp}/isolation-lint-build.XXXXXX")
if ! xcrun swift build -c release --product isolation-lint --package-path "$PACKAGE" --scratch-path "$SCRATCH" >"$LOG" 2>&1; then
  cat "$LOG"
  rm -f "$LOG"
  printf 'isolation-lint: FAIL the lint did not build\n'
  exit 2
fi
rm -f "$LOG"
exec "$SCRATCH/release/isolation-lint" --root "$ROOT" --baseline "$ROOT/scripts/test/apple_isolation_baseline.json" "$@"
