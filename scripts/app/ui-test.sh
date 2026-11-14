#!/bin/bash
# Run the UI tests (apple/PersonaUITests) in the simulator against the configured hub (docs/operations/config.md).
#   ./scripts/app/ui-test.sh [machine/session to open]   # default: the newest session of the hub's machine
set -euo pipefail
cd "$(dirname "$0")/../.."
eval "$(python3 scripts/persona_config.py env)"
python3 scripts/persona_config.py xcconfig
if [ -x "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" ]; then
  "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" native/app-core/scripts/build-apple.sh
else
  native/app-core/scripts/build-apple.sh
fi
SIM="${SIM:-$PERSONA_SIM}"
# boots it slim (SIM_PROFILE=default|watch|live-activity|stock), docs/operations/testing.md "Simulators"
SIM=$(scripts/app/sim-boot.sh "$SIM") || exit 1
RESOLVER=$(python3 scripts/persona_identity.py --service hub --prefix -)
TOKEN=$(ssh "$PERSONA_HUB_SSH" "$RESOLVER
python3 -c 'import json,os;print(json.load(open(os.path.join(os.environ[\"PERSONA_HOME\"],\"hub.json\")))[\"token\"])'")
URL=$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1],safe=""))' "$PERSONA_HUB_URL")
SESSION="${1:-}"
if [ -z "$SESSION" ]; then
  SESSION=$(curl -s -H "Authorization: Bearer $TOKEN" "$PERSONA_HUB_URL/api/v1/sessions" | python3 -c '
import json,sys
d=json.load(sys.stdin); ss=d if isinstance(d,list) else d.get("sessions",[])
live=[s for s in ss if s.get("pane_id") and not s.get("assistant")]
print(live[0]["machine_id"]+"/"+live[0]["session_id"] if live else "")')
fi
cd apple && xcodegen generate -q
TEST_RUNNER_PERSONA_PAIR_URL="persona://pair?url=$URL&token=$TOKEN" TEST_RUNNER_PERSONA_OPEN_SESSION="$SESSION" \
  xcodebuild test -project Persona.xcodeproj -scheme Persona -destination "id=$SIM" -derivedDataPath ../build/DerivedData \
  -only-testing:PersonaUITests 2>&1 | grep -E "error:|Test Case|✘|✔|TEST (SUCCEEDED|FAILED)|XCTAssert" | sed 's/token=[^ &]*/token=…/g'
