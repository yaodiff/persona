#!/bin/bash
# Records StreamingScrollTests (docs/apple/ios-state.md "Scrolling while streaming"): builds once, then per case a fresh mock hub, a screen
# recording of the simulator and the app's scroll trace, into $OUT/<case>.{mp4,trace.txt,log,marks,recstart}.
#   SIM=<simulator udid> OUT=/tmp/stream-shots scripts/diag/stream-scroll/run-ios.sh [case…]
# Env: SIM (required), OUT, PORT (mock hub, default 18831; never one another agent's mock is on),
# STYLE (glass|beautiful), SPM (a SourcePackages directory to reuse). Navigation follows the app's defaults.
# One RESULT line per case and one at the end.
set -u
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
SIM=${SIM:?SIM=<simulator udid>}; PORT=${PORT:-18831}; OUT=${OUT:-/tmp/stream-shots}; mkdir -p "$OUT"
CASES=${*:-testFollowingWhileAReplyStreams testFollowingWhenTheClearComesFirst testReadingWhileAReplyStreams testArrowWhileAReplyStreams testDragAwayWhileAReplyStreams testFollowingARunningTurn testFollowingAnAgentAtWork testReadingWhileAnAgentWorks}
BUNDLE=$(cd "$ROOT" && python3 scripts/persona_config.py env | sed -n 's/^export PERSONA_BUNDLE_ID=//p')
cd "$ROOT" && eval "$(python3 scripts/persona_config.py env)" >/dev/null
if [ -x "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" ]; then
  "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" "$ROOT/native/app-core/scripts/build-apple.sh" || exit 1
else
  "$ROOT/native/app-core/scripts/build-apple.sh" || exit 1
fi
SIM=$(scripts/app/sim-boot.sh "$SIM") || { echo "RESULT no simulator"; exit 1; }
cd apple && xcodegen generate -q
COMMON=(-project Persona.xcodeproj -scheme Persona -destination "id=$SIM" -derivedDataPath "$ROOT/build/DerivedData")
[ -n "${SPM:-}" ] && COMMON+=(-clonedSourcePackagesDirPath "$SPM" -disableAutomaticPackageResolution)
perl -e 'alarm 1800; exec @ARGV' xcodebuild build-for-testing "${COMMON[@]}" > "$OUT/build.log" 2>&1 || { echo "RESULT build failed ($OUT/build.log)"; exit 1; }
for c in $CASES; do
  if lsof -nP -tiTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1; then echo "RESULT port $PORT busy"; exit 3; fi
  case $c in *RunningTurn*) M="MOCK_ASSISTANT_RUN=1";; *Agent*) M="MOCK_STREAM=1 MOCK_LONG_HISTORY=1 MOCK_AGENTS=1";; *) M="MOCK_LONG_HISTORY=1";; esac
  (cd "$ROOT" && env $M PORT=$PORT python3 scripts/mock/mock_hub.py > "$OUT/$c.mock.log" 2>&1) &
  for i in $(seq 1 30); do curl -s -o /dev/null http://127.0.0.1:$PORT/api/v1/machines && break; sleep 0.5; done
  MOCKPID=$(lsof -nP -tiTCP:$PORT -sTCP:LISTEN)
  xcrun simctl uninstall "$SIM" "$BUNDLE" 2>/dev/null
  xcrun simctl io "$SIM" recordVideo --codec=h264 --force "$OUT/$c.mp4" > "$OUT/$c.rec.log" 2>&1 &
  REC=$!
  # The recording starts about a second before this is written (analyze.sh allows for it).
  sleep 1; python3 -c 'import time; print("%.3f" % time.time())' > "$OUT/$c.recstart"
  env TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:$PORT TEST_RUNNER_PERSONA_RUN_HUB=http://127.0.0.1:$PORT \
    TEST_RUNNER_PERSONA_UI_STYLE=${STYLE:-glass} \
    perl -e 'alarm 600; exec @ARGV' xcodebuild test-without-building "${COMMON[@]}" -only-testing:PersonaUITests/StreamingScrollTests/$c > "$OUT/$c.log" 2>&1
  rc=$?
  kill -INT $REC; wait $REC 2>/dev/null
  d=$(xcrun simctl get_app_container "$SIM" "$BUNDLE" data 2>/dev/null)
  cp "$d/Documents/scroll-trace.txt" "$OUT/$c.trace.txt" 2>/dev/null
  kill $MOCKPID 2>/dev/null
  grep -h "^STREAM " "$OUT/$c.log" | sort -u > "$OUT/$c.marks"
  echo "[$c] RESULT rc=$rc $(grep -Eo 'TEST (EXECUTE )?(SUCCEEDED|FAILED)' "$OUT/$c.log" | tail -1)"
done
echo "RESULT run-ios done $OUT"
