#!/bin/bash
# Build, install and launch Persona in the iPhone simulator, paired with the hub of the config (docs/operations/config.md).
#   ./scripts/app/sim.sh [extra launch args...]
set -euo pipefail
cd "$(dirname "$0")/../.."
eval "$(python3 scripts/persona_config.py env)"
python3 scripts/persona_config.py xcconfig
if [ -x "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" ]; then
  "${PERSONA_XCODE_RUNNER:-$HOME/.local/bin/xcode-slot}" native/app-core/scripts/build-apple.sh
else
  native/app-core/scripts/build-apple.sh
fi
RESOLVER=$(python3 scripts/persona_identity.py --service hub --prefix -)
cd apple
SIM="${SIM:-$PERSONA_SIM}"
[ -n "$SIM" ] || { echo "set SIM=<udid or name> or \"simulator\" in the config (docs/operations/config.md)" >&2; exit 2; }
# boots it slim (SIM_PROFILE picks the simslim profile; a booted one is used as it is), docs/operations/testing.md "Simulators"
SIM=$(../scripts/app/sim-boot.sh "$SIM") || exit 1
xcodegen generate -q
xcodebuild -project Persona.xcodeproj -scheme Persona -destination "id=$SIM" -derivedDataPath ../build/DerivedData build 2>&1 | grep -E "error:|BUILD (SUCCEEDED|FAILED)" | sort -u
xcrun simctl uninstall "$SIM" "$PERSONA_BUNDLE_ID" 2>/dev/null || true
xcrun simctl install "$SIM" ../build/DerivedData/Build/Products/Debug-iphonesimulator/Persona.app
TOKEN=$(ssh "$PERSONA_HUB_SSH" "$RESOLVER
python3 -c 'import json,os;print(json.load(open(os.path.join(os.environ[\"PERSONA_HOME\"],\"hub.json\")))[\"token\"])'")
URL=$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1],safe=""))' "$PERSONA_HUB_URL")
xcrun simctl terminate "$SIM" "$PERSONA_BUNDLE_ID" 2>/dev/null || true
xcrun simctl launch "$SIM" "$PERSONA_BUNDLE_ID" -pairURL "persona://pair?url=$URL&token=$TOKEN" "$@" >/dev/null
