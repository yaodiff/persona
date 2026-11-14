#!/bin/bash
# Builds the web client's bundle (web/dist) for the desktop acceptance smoke.
#
# The desktop lane needs the bundle the hub would serve, not the web lane's verdict. The web build's last step checks
# the first screen's size against its budget after every file is written, and that check belongs to the web lane
# (scripts/ci/web.sh), which reports it. When it is the only thing that failed here and the bundle is complete, this
# script says so in a warning and lets the smoke run on that bundle; any other build failure fails as usual.
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_run 'Web dependencies for the desktop smoke' 900 npm --prefix web ci
log="$(mktemp)"
trap 'rm -f "${log:?}"' EXIT
set +e
ci_run 'Web client bundle for the desktop smoke' 1200 npm --prefix web run build 2>&1 | tee "$log"
code="${PIPESTATUS[0]}"
set -e
if [[ "$code" -eq 0 ]]; then
  printf 'desktop-web: PASS the web build passed\n'
  exit 0
fi
complete=1
for file in web/dist/index.html web/dist/sw.js web/dist/pake/client.wasm; do
  [[ -s "$file" ]] || complete=0
done
compgen -G 'web/dist/assets/main-*.js' >/dev/null || complete=0
if [[ "$complete" -eq 1 ]] && grep -q 'first screen is over its budget' "$log" && [[ "$(grep -c 'error during build' "$log")" -eq 1 ]]; then
  message="$(grep -o 'the [^:]* first screen is over its budget of [0-9]* B ([^;]*' "$log" | head -n 1 || true)"
  printf '::warning title=Web first-screen budget::%s). The desktop smoke runs on this complete bundle; the web lane owns the budget.\n' "${message:-the first screen is over its budget (see the log}"
  printf 'desktop-web: PASS with a warning: the bundle is complete, only the web first-screen budget check failed\n'
  exit 0
fi
printf 'desktop-web: FAIL the web build failed (exit %s) and left no usable bundle\n' "$code" >&2
exit 1
