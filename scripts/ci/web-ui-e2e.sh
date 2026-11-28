#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"

if [ ! -d web/node_modules ] || ! node web/scripts/installed-deps.mjs >/dev/null 2>&1; then
  ci_run 'Web UI E2E install' 600 bash -c 'cd web && npm ci --no-audit --no-fund'
fi

if [ "${PERSONA_UI_E2E_SKIP_BUILD:-0}" != "1" ]; then
  ci_run 'Web UI E2E build' 600 bash -c 'cd web && npm run build'
fi

ci_run 'Web UI E2E' 600 bash -c 'cd web && npx --no -- playwright test ${1+"$@"}' _ ${1+"$@"}
