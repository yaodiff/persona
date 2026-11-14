#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_run 'Desktop TypeScript and policy tests' 600 npm --prefix desktop ci --ignore-scripts
ci_run 'Desktop typecheck' 180 npm --prefix desktop run typecheck
ci_run 'Desktop tests' 180 npm --prefix desktop test
ci_run 'Desktop dependency audit' 180 npm --prefix desktop audit --audit-level=high
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    # The scan compares exact file contents and a Windows checkout rewrites line endings; the Linux lane of the same
    # workflow scans the same commit.
    printf 'SKIP Desktop privacy scan (runs in the Linux lane; a Windows checkout changes line endings)\n' ;;
  *) ci_run 'Desktop privacy scan' 120 python3 scripts/privacy_check.py ;;
esac
