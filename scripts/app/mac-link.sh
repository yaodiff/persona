#!/bin/bash
# Independently installable Persona Link: persona-link, persona-node, Persona Screen and Persona Input, no desktop UI
# required.
# The bundle id is PERSONA_LINK_BUNDLE_ID, else mac.link_bundle_id in ~/.config/persona/config.json, else the app's
# registered identity (PERSONA_BUNDLE_ID, else app.bundle_id) + ".link". Never a placeholder: macOS keeps Persona
# Screen's Screen Recording grant per bundle id, so a later build with another id has to be allowed again (#7569).
# UNIVERSAL=1 builds for Apple silicon and Intel together. `verify-input` checks that Persona Input builds to the same
# bits from two clean copies at different paths (no bundle id needed).
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
link_id() {
  ID=${PERSONA_LINK_BUNDLE_ID:-$(python3 scripts/persona_config.py get mac.link_bundle_id 2>/dev/null || true)}
  if [ -z "$ID" ]; then
    APP_ID=${PERSONA_BUNDLE_ID:-$(python3 scripts/persona_config.py get app.bundle_id 2>/dev/null || true)}
    [ -n "$APP_ID" ] || { echo "Persona Link needs a bundle id: set mac.link_bundle_id or app.bundle_id in ~/.config/persona/config.json, or PERSONA_LINK_BUNDLE_ID (docs/operations/config.md)" >&2; exit 2; }
    ID=$APP_ID.link
  fi
}
PKG="$ROOT/apple/PersonaLink"
OUT="$PKG/build"
APP="$OUT/Persona Link.app"

# Persona Input (#7775, docs/extensions/computer-use.md "Pixel scrolling"): the pixel scrolling helper, a package of its
# own. macOS ties its Accessibility permission to its code, so a Persona Link update must not change its bits unless its
# sources changed: the built executable is kept by a hash of its sources, PersonaInputKit's and the architectures, and
# reused byte for byte whatever builds Persona Link later (another checkout path, another Xcode).
INPUT_PKG="$PKG/Input"
INPUT_CACHE=${PERSONA_INPUT_CACHE:-$HOME/.persona/cache/persona-input}
input_key() { # the architectures as arguments
  { (cd "$ROOT" && find apple/PersonaLink/Input apple/Packages/PersonaInputKit -type f \( -name '*.swift' -o -name '*.plist' \) \
      -not -path '*/.build/*' -not -path '*/Tests/*' | LC_ALL=C sort | while IFS= read -r f; do shasum -a 256 "$f"; done)
    echo "$*"; } | shasum -a 256 | cut -c1-24
}
# swift_build_input <package dir> <scratch dir> <archs…>: builds it and prints its executable's path.
swift_build_input() {
  local pkg=$1 scratch=$2 flags=()
  shift 2
  for a in "$@"; do flags+=(--arch "$a"); done
  xcrun swift build -c release --package-path "$pkg" --scratch-path "$scratch" "${flags[@]}" >&2
  echo "$(xcrun swift build -c release --package-path "$pkg" --scratch-path "$scratch" "${flags[@]}" --show-bin-path)/PersonaInput"
}
input_binary() { # the architectures as arguments: prints the cached executable, building it first if its key is new
  local key dir scratch bin
  key=$(input_key "$@")
  dir="$INPUT_CACHE/$key"
  if [ ! -x "$dir/PersonaInput" ]; then
    scratch=$(mktemp -d "${TMPDIR:-/tmp}/persona-input.XXXXXX")
    bin=$(swift_build_input "$INPUT_PKG" "$scratch" "$@")
    mkdir -p "$INPUT_CACHE"
    rm -rf "$dir.part"
    mkdir "$dir.part"
    cp "$bin" "$dir.part/PersonaInput"
    mv "$dir.part" "$dir"
    rm -rf "$scratch"
  fi
  echo "$dir/PersonaInput"
}
# The helper bundle at $1 from executable $2, under bundle id $3, ad-hoc signed.
input_bundle() {
  mkdir -p "$1/Contents/MacOS"
  cp "$2" "$1/Contents/MacOS/PersonaInput"
  sed -e "s/__INPUT_BUNDLE_ID__/$3/" "$INPUT_PKG/PersonaInput-Info.plist" > "$1/Contents/Info.plist"
  codesign --force --sign - "$1"
}
verify_input() { # two clean builds from copies at two different paths: the same executable and the same signature
  local work arch out=() hashes=()
  work=$(mktemp -d "${TMPDIR:-/tmp}/persona-input-verify.XXXXXX")
  arch=$(uname -m)
  for copy in first "second copy"; do
    local root="$work/$copy/repo"
    mkdir -p "$root/apple/PersonaLink" "$root/apple/Packages"
    rsync -a --exclude .build "$INPUT_PKG/" "$root/apple/PersonaLink/Input/"
    rsync -a --exclude .build "$ROOT/apple/Packages/PersonaInputKit/" "$root/apple/Packages/PersonaInputKit/"
    local bin
    bin=$(swift_build_input "$root/apple/PersonaLink/Input" "$work/$copy/scratch" "$arch")
    input_bundle "$work/$copy/Persona Input.app" "$bin" example.persona.link.input
    out+=("$(shasum -a 256 < "$bin" | cut -c1-16)")
    hashes+=("$(codesign -dvvv "$work/$copy/Persona Input.app" 2>&1 | sed -n 's/^CDHash=//p')")
  done
  rm -rf "$work"
  if [ "${out[0]}" = "${out[1]}" ] && [ "${hashes[0]}" = "${hashes[1]}" ] && [ -n "${hashes[0]}" ]; then
    echo "verify-input: same executable ${out[0]} and cdhash ${hashes[0]} from two paths"
  else
    echo "verify-input: DIFFERENT executable ${out[0]} / ${out[1]}, cdhash ${hashes[0]} / ${hashes[1]}"
    return 1
  fi
}

build() {
  local version archs=() helpers=() nodes=()
  link_id
  version="$(git describe --tags --match 'v[0-9]*' --always --dirty)-$(date +%Y%m%d%H%M)"
  if [ "${UNIVERSAL:-}" = 1 ]; then archs=(arm64 x86_64); else archs=("$(uname -m)"); fi
  # Persona Screen, the live screen's video helper (#3954): a scheme of its own in the package.
  (cd "$PKG" && xcodebuild -quiet -scheme PersonaScreen -configuration Release -derivedDataPath build/dd \
    -destination 'generic/platform=macOS' ARCHS="${archs[*]}" ONLY_ACTIVE_ARCH=NO build)
  mkdir -p "$OUT"
  for a in "${archs[@]}"; do
    local goarch=$a
    [ "$a" = x86_64 ] && goarch=amd64
    (cd server && GOOS=darwin GOARCH=$goarch CGO_ENABLED=0 go build -buildvcs=false -tags "$(cat build-tags.txt)" -trimpath \
      -ldflags "-s -w -X main.version=$version" -o "$OUT/persona-link-$a" ./cmd/persona-link)
    helpers+=("$OUT/persona-link-$a")
    # The resident node the independent installer can set up (persona-link node install --node-bin).
    (cd server && GOOS=darwin GOARCH=$goarch CGO_ENABLED=0 go build -buildvcs=false -tags "$(cat build-tags.txt)" -trimpath \
      -ldflags "-s -w -X main.version=$version" -o "$OUT/persona-node-$a" ./cmd/persona-node)
    nodes+=("$OUT/persona-node-$a")
  done
  rm -rf "$APP"
  mkdir -p "$APP/Contents/Helpers"
  lipo -create "${helpers[@]}" -output "$APP/Contents/Helpers/persona-link"
  lipo -create "${nodes[@]}" -output "$APP/Contents/Helpers/persona-node"
  # Persona Screen.app (docs/extensions/computer-use.md "Live screen video (#3954)"): its own bundle owns the
  # Screen Recording grant, not the PC App or the node. Build it the same way so the grant survives Link updates.
  local screen="$APP/Contents/Helpers/Persona Screen.app"
  mkdir -p "$screen/Contents/MacOS"
  cp "$OUT/dd/Build/Products/Release/PersonaScreen" "$screen/Contents/MacOS/PersonaScreen"
  sed -e "s/__SCREEN_BUNDLE_ID__/$ID.screen/" "$PKG/PersonaScreen-Info.plist" > "$screen/Contents/Info.plist"
  # Ad-hoc signatures keep local development self-contained; production signing is a separate authorized step.
  codesign --force --sign - --identifier "$ID.helper" "$APP/Contents/Helpers/persona-link"
  codesign --force --sign - --identifier "$ID.persona-node" "$APP/Contents/Helpers/persona-node"
  codesign --force --sign - "$screen"
  local input="$APP/Contents/Helpers/Persona Input.app"
  input_bundle "$input" "$(input_binary "${archs[@]}")" "$ID.input"
  codesign --verify --strict "$APP/Contents/Helpers/persona-link" "$APP/Contents/Helpers/persona-node" "$screen" "$input"
  echo "built $APP ($ID, persona-link $version, no menu bar UI)"
}

case "${1:-build}" in
  build) build ;;
  install)
    build
    rm -rf "/Applications/Persona Link.app"
    ditto "$APP" "/Applications/Persona Link.app"
    # The helpers have stable paths for the node's Persona Screen lookup and the desktop's separately installed Link.
    # The bundle is an install location, not a launchable UI: launchd owns the connector and its login startup.
    "$APP/Contents/Helpers/persona-link" install --node-bin "$APP/Contents/Helpers/persona-node"
    echo "installed independent Persona Link at ~/.persona/bin/persona-link (launchd com.persona.link; no menu bar UI)" ;;
  verify-input) verify_input ;;
  *) echo "usage: $0 [build|install|verify-input]" >&2; exit 2 ;;
esac
