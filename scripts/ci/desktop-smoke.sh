#!/bin/bash
# The desktop acceptance smoke (docs/desktop/desktop.md "Packaging and verification"): a real Electron process against
# the synthetic hubs and the built web client, at the origin root, below a reverse-proxy prefix and across an explicit
# hub change. Needs web/dist (it builds one when missing, which needs Go) and the desktop's dependencies
# (scripts/ci/desktop.sh installs them).
# Output: desktop/release/smoke, or the directory given.
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_run 'Desktop acceptance smoke' 900 node desktop/scripts/smoke-with-hub.mjs "$@"
