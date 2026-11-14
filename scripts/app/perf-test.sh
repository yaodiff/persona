#!/bin/bash
# The iPhone app's performance UI tests (apple/PersonaUITests/Perf*Tests.swift, #5730; docs/operations/testing.md
# "Performance UI tests"): an optimized build, one mock hub per suite with that suite's heavy data, XCTHitchMetric and
# scroll / navigation signposts per page and transition, XCTApplicationLaunchMetric for launch, then a JSON + Markdown report.
#
#   scripts/app/perf-test.sh [suite…]        # suites: launch agents assistant composer inbox settings machines (default: all)
#
# Env:
#   PERF_SIM         the assigned, existing simulator udid or name (required if "Persona Perf" is absent)
#   PERF_ITERATIONS  measure() iterations per test (default 5)
#   PERF_ONLY        space-separated test names to run only those (e.g. "PerfAgentsTests/testAgentsListScroll")
#   PERF_OUT         where the result bundles and the report go (default /tmp/persona-perf-<time>)
#   PERF_DD          DerivedData of the optimized build (default /tmp/persona-perf-dd)
#   PERF_SKIP_BUILD  1: reuse the last build in PERF_DD
#   PERF_SIM_LOCK    the lock every simulator use runs under (default /tmp/playbox-sim.lock, shared with other agents)
#   PERF_SAMPLE      a test name (e.g. testAgentChatScrollBig): also `sample` the app's main thread during that test
#
# Builds always go through ~/.local/bin/xcode-slot (at most two builds on the machine). Building does not hold
# the simulator lock; booting and running tests do, only for as long as each suite runs.
# Always ends with one line: "perf-test: ok|failed|timeout …".
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 2
ROOT=$PWD
eval "$(python3 scripts/persona_config.py env)"
python3 scripts/persona_config.py xcconfig >/dev/null
if [ -x "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" ]; then
  "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" native/app-core/scripts/build-apple.sh
else
  native/app-core/scripts/build-apple.sh
fi

SUITES=("$@")
[ ${#SUITES[@]} -gt 0 ] || SUITES=(launch agents assistant composer inbox settings machines)
SIM_NAME="${PERF_SIM:-Persona Perf}"
ITER="${PERF_ITERATIONS:-5}"
OUT="${PERF_OUT:-/tmp/persona-perf-$(date +%Y%m%d-%H%M%S)}"
DD="${PERF_DD:-/tmp/persona-perf-dd}"
LOCK="${PERF_SIM_LOCK:-/tmp/playbox-sim.lock}"
SLOT=("$HOME/.local/bin/xcode-slot")
[ -x "${SLOT[0]}" ] || { echo "perf-test: failed xcode-slot is required"; exit 2; }
[ ! -e "$OUT" ] || { echo "perf-test: failed output directory already exists: $OUT"; exit 2; }
mkdir -p "$OUT"
log() { echo "perf-test: $*" >&2; }
verdict() { echo "perf-test: $*"; }

# Each suite's mock: the heavy data its pages need (the flags are documented in scripts/mock/mock_hub.py).
mock_env() {
  case "$1" in
    launch) echo "MOCK_AGENTS=1 MOCK_LONG_ASSISTANT=1 MOCK_LONG_HISTORY=1" ;;
    agents) echo "MOCK_AGENTS=1 MOCK_BIG=1 MOCK_CHAT_LINES=1 MOCK_SUBAGENT_CARDS=1 MOCK_FILES=1 MOCK_MACHINES=1 MOCK_TURN_IMAGES=many" ;;
    assistant|composer) echo "MOCK_LONG_ASSISTANT=1 MOCK_AGENTS=1 MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_DOCK=many MOCK_ASSISTANT_SIDEBAR=1 MOCK_KB_MANY_LINKS=1" ;;
    inbox) echo "MOCK_LARK=1 MOCK_LARK_RICH=1 MOCK_LARK_TOPIC=1 MOCK_INBOX_EXTRA=150 MOCK_MAIL=1 MOCK_AGENTS=1" ;;
    settings) echo "MOCK_EXTENSIONS=1 MOCK_SCHEDULE=1 MOCK_AGENTS=1" ;;
    machines) echo "MOCK_PERF_MACHINES=24 MOCK_PERF_METRIC_STEP=50 MOCK_AGENTS=1" ;;
    *) return 1 ;;
  esac
}
suite_class() {
  case "$1" in
    launch) echo PerfLaunchTests ;; agents) echo PerfAgentsTests ;; assistant) echo PerfAssistantTests ;; composer) echo PerfComposerTests ;;
    inbox) echo PerfInboxTests ;; settings) echo PerfSettingsTests ;; machines) echo PerfMachinesTests ;;
  esac
}
for s in "${SUITES[@]}"; do mock_env "$s" >/dev/null || { verdict "failed unknown suite $s (launch agents assistant composer inbox settings machines)"; exit 2; }; done

# Never create or boot an extra simulator when an explicitly assigned device is missing.
UDID=$(xcrun simctl list devices -j | python3 -c '
import json,sys
want=sys.argv[1]
for rt,ds in json.load(sys.stdin)["devices"].items():
    for d in ds:
        if d["udid"]==want or d["name"]==want: print(d["udid"]); sys.exit()' "$SIM_NAME")
if [ -z "$UDID" ]; then
  verdict "failed simulator $SIM_NAME does not exist; supply the assigned PERF_SIM"; exit 2
fi
log "simulator $UDID, default navigation, $ITER iterations, results in $OUT"

# 1. The optimized build (no simulator lock: it only compiles).
if [ "${PERF_SKIP_BUILD:-0}" != 1 ]; then
  (cd apple && xcodegen generate -q) || { verdict "failed xcodegen"; exit 1; }
  log "building (optimized, $DD)…"
  "${SLOT[@]}" timeout 3600 xcodebuild build-for-testing -project apple/Persona.xcodeproj -scheme Persona \
    -destination "id=$UDID" -derivedDataPath "$DD" \
    SWIFT_OPTIMIZATION_LEVEL=-O SWIFT_COMPILATION_MODE=wholemodule >"$OUT/build.log" 2>&1
  build_rc=$?
  if [ $build_rc -ne 0 ]; then
    grep -E "error:" "$OUT/build.log" | head -20 >&2
    if [ $build_rc -eq 124 ] || [ $build_rc -eq 125 ]; then verdict "timeout build (log $OUT/build.log)"
    else verdict "failed build (log $OUT/build.log)"; fi
    exit 1
  fi
fi

free_port() {
  local port
  for _ in $(seq 1 10); do
    port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
    if ! lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then echo "$port"; return; fi
  done
  return 1
}
MOCK_PID=""
stop_mock() {
  if [ -n "$MOCK_PID" ] && kill -0 "$MOCK_PID" 2>/dev/null; then kill "$MOCK_PID" 2>/dev/null; wait "$MOCK_PID" 2>/dev/null; fi
  MOCK_PID=""
}
SAMPLER=""
stop_sampler() { [ -n "$SAMPLER" ] && kill "$SAMPLER" 2>/dev/null; SAMPLER=""; }
trap 'stop_mock; stop_sampler' EXIT

# While a suite runs: `sample` the app's main thread once PERF_SAMPLE's test starts (its log line in the test output).
sample_when() {
  local test="$1" logf="$2" dest="$3"
  for _ in $(seq 1 3600); do
    if grep -q "Test Case .*$test\]' started" "$logf" 2>/dev/null; then
      sleep 25 # launch, pairing and settling before the measured part
      local pid
      pid=$(pgrep -n -f "CoreSimulator/Devices/$UDID/.*/Persona.app/Persona" 2>/dev/null)
      [ -n "$pid" ] && sample "$pid" 20 1 -file "$dest" >/dev/null 2>&1
      return
    fi
    sleep 1
  done
}

FAILED=()
TIMED_OUT=0
for s in "${SUITES[@]}"; do
  cls=$(suite_class "$s")
  port=$(free_port)
  log "suite $s: mock on 127.0.0.1:$port ($(mock_env "$s"))"
  env $(mock_env "$s") PORT="$port" python3 scripts/mock/mock_hub.py >"$OUT/mock-$s.log" 2>&1 &
  MOCK_PID=$!
  up=0
  for _ in $(seq 1 40); do
    curl -s -m 2 -o /dev/null "http://127.0.0.1:$port/api/v1/sessions" -H "Authorization: Bearer mock" && { up=1; break; }
    sleep 0.5
  done
  if [ $up != 1 ]; then log "the $s mock did not start (log $OUT/mock-$s.log)"; FAILED+=("$s:mock"); stop_mock; continue; fi

  only=()
  if [ -n "${PERF_ONLY:-}" ]; then
    for t in $PERF_ONLY; do case "$t" in "$cls"/*) only+=("-only-testing:PersonaUITests/$t") ;; esac; done
    [ ${#only[@]} -gt 0 ] || { stop_mock; continue; }
  else
    only=("-only-testing:PersonaUITests/$cls")
  fi
  if [ -e "$OUT/$s.xcresult" ]; then
    log "refusing to overwrite $OUT/$s.xcresult"; FAILED+=("$s:existing-result"); stop_mock; continue
  fi
  [ -n "${PERF_SAMPLE:-}" ] && { sample_when "$PERF_SAMPLE" "$OUT/test-$s.log" "$OUT/sample-$PERF_SAMPLE.txt" & SAMPLER=$!; }
  log "suite $s: waiting for the simulator lock, then testing…"
  # The lock covers the boot, the run and the shutdown of this suite only.
  lockf -k -t "${PERF_LOCK_TIMEOUT:-1800}" "$LOCK" /bin/bash -c '
    UDID="$1"; APP="$2"; SUITE="$3"; shift 3
    trap '\''xcrun simctl shutdown "$UDID" 2>/dev/null'\'' EXIT
    "'"$ROOT"'/scripts/app/sim-boot.sh" "$UDID" >/dev/null || exit 1
    if [ "$SUITE" = launch ]; then
      xcrun simctl install "$UDID" "$APP" || exit 1
    fi
    "$@"' _ "$UDID" "$DD/Build/Products/Debug-iphonesimulator/Persona.app" "$s" \
    env TEST_RUNNER_PERSONA_PERF_HUB="http://127.0.0.1:$port" \
      TEST_RUNNER_PERSONA_PERF_ITERATIONS="$ITER" \
      TEST_RUNNER_PERSONA_PERF_SIM="$UDID" \
      TEST_RUNNER_PERSONA_PERF_BUNDLE="$PERSONA_BUNDLE_ID" \
      TEST_RUNNER_PERSONA_PERF_SIM_HOME="$HOME" \
    "${SLOT[@]}" timeout 5400 xcodebuild test-without-building -project apple/Persona.xcodeproj -scheme Persona \
      -destination "id=$UDID" -derivedDataPath "$DD" -parallel-testing-enabled NO \
      -resultBundlePath "$OUT/$s.xcresult" "${only[@]}" >"$OUT/test-$s.log" 2>&1
  rc=$?
  stop_sampler
  stop_mock
  grep -E "Test Case .*(passed|failed|skipped)" "$OUT/test-$s.log" | sed 's/^/  /' >&2
  [ $rc -eq 0 ] || {
    if [ $rc -eq 124 ] || [ $rc -eq 125 ] || [ $rc -eq 75 ]; then TIMED_OUT=1; FAILED+=("$s:timeout")
    else FAILED+=("$s"); fi
  }
done

python3 -I scripts/app/perf_report.py "$OUT" --iterations "$ITER" > "$OUT/report.md" || FAILED+=("report")
cat "$OUT/report.md"
if [ ${#FAILED[@]} -eq 0 ]; then verdict "ok report $OUT/report.md ($OUT/report.json)"
elif [ $TIMED_OUT -eq 1 ]; then verdict "timeout ${FAILED[*]} (logs and report in $OUT)"; exit 1
else verdict "failed ${FAILED[*]} (logs and report in $OUT)"; exit 1; fi
