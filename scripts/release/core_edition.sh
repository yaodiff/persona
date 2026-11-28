#!/bin/bash
# The core edition's packages (docs/operations/core-install.md): Persona Core alone, every program built with
# persona_core_only besides server/build-tags.txt, so no built-in module is in it. scripts/release/publish.sh runs this
# for every platform it publishes; scripts/ci/core-install.py for this machine's, to install from what it made.
#
#   LDFLAGS=… scripts/release/core_edition.sh build <out> <os>/<arch>…
#       <out>/bin/<program>-<os>-<arch>[.exe]: personad, persona-link and persona-node for every platform, persona-hub
#       and persona-manager where a hub runs (not Windows). The names the full release's files have in its own
#       directory. LDFLAGS is the full release's (-X main.version, channel, build, commit, updateURL).
#   scripts/release/core_edition.sh pack <out> <version> <build> <commit> [<channel> [<full bin dir>]]
#       checks them and lays them out as they are served, in <out>/dl: core-<name>.gz, core-latest.json (the manifest:
#       edition core, each file's served name), core-SHA256SUMS and core-VERSION. One flat namespace with the full
#       release, which keeps every name it has: a core-only install reads only these (update.FeedName), a full
#       install never does. With <full bin dir>, the full release's programs there are checked the other way: none
#       built with the tag, and neither edition's personad takes the other's manifest.
#
# No iPhone helper: it belongs to a module. Run from the repository's root; nothing is uploaded.
set -euo pipefail
cd "$(dirname "$0")/../.."
CORE_TAG=persona_core_only
usage() { sed -n '2,18p' "$0" >&2; exit 2; }
[ $# -ge 2 ] || usage
mode="$1" out="$2"
shift 2
case "$out" in /*) ;; *) out="$PWD/$out" ;; esac

# built_with_core <file>: its build info records the tag (go version -m reads any platform's build).
built_with_core() {
  go version -m "$1" | grep -E '^[[:space:]]*build[[:space:]]+-tags=' | tr ',= ' '\n\n\n' | grep -qx "$CORE_TAG"
}

case "$mode" in
build)
  [ $# -ge 1 ] || usage
  tags="$(cat server/build-tags.txt),$CORE_TAG"
  mkdir -p "$out/bin"
  for t in "$@"; do
    programs="personad persona-link persona-node persona-hub persona-manager" ext=""
    if [ "${t%/*}" = windows ]; then programs="personad persona-link persona-node" ext=.exe; fi
    for p in $programs; do
      (cd server && GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -tags "$tags" -trimpath -ldflags "${LDFLAGS:?}" \
        -o "$out/bin/$p-${t%/*}-${t#*/}$ext" "./cmd/$p")
    done
  done
  ;;
pack)
  [ $# -ge 3 ] || usage
  version="$1" build="$2" commit="$3" channel="${4:-release}" full="${5:-}"
  plat="$(uname -s | tr A-Z a-z)-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  host="$out/bin/personad-$plat"
  [ -x "$host" ] || { echo "no core-only personad for this machine ($plat) in $out/bin: it writes the manifest" >&2; exit 1; }
  # Every program says its edition in its build info: the core's the tag, the full ones never.
  for f in "$out/bin"/*; do
    case "$f" in *.gz | *.json) continue ;; esac
    built_with_core "$f" || { echo "$f was not built with $CORE_TAG" >&2; exit 1; }
  done
  [ "$("$host" version)" = "$version" ] || { echo "the core-only personad does not report $version" >&2; exit 1; }
  for name in persona-link persona-node persona-hub persona-manager; do
    [ "$("$out/bin/$name-$plat" version -v)" = "$("$host" version -v)" ] \
      || { echo "the core edition's $name is not the same build as its personad" >&2; exit 1; }
  done
  rm -f "$out/bin"/*.gz "$out/bin/latest.json"
  for bin in "$out/bin"/*; do gzip -n -9 -c "$bin" > "$bin.gz"; done
  # Written by the core edition's own personad, which stamps the edition and each file's served name, and read back.
  "$host" release manifest --dir "$out/bin" --version "$version" --build "$build" --commit "$commit" --channel "$channel" --out "$out/bin/latest.json"
  "$host" release verify --dir "$out/bin" --manifest "$out/bin/latest.json"
  if [ -n "$full" ]; then
    [ "$("$full/personad-$plat" version -v)" = "$("$host" version -v)" ] \
      || { echo "the core edition is not the same build as the full release in $full" >&2; exit 1; }
    for f in "$full"/personad-* "$full"/persona-link-* "$full"/persona-node-* "$full"/persona-hub-* "$full"/persona-manager-*; do
      case "$f" in *.gz) continue ;; esac
      [ -f "$f" ] || continue
      if built_with_core "$f"; then echo "$f of the full release was built with $CORE_TAG" >&2; exit 1; fi
    done
    # Neither edition's personad takes the other's manifest.
    if "$full/personad-$plat" release verify --dir "$out/bin" --manifest "$out/bin/latest.json" >/dev/null 2>&1; then
      echo "the full personad took the core edition's manifest" >&2; exit 1
    fi
    if [ -f "$full/latest.json" ] && "$host" release verify --dir "$full" --manifest "$full/latest.json" >/dev/null 2>&1; then
      echo "the core edition's personad took the full manifest" >&2; exit 1
    fi
  fi
  rm -rf "$out/dl"
  mkdir -p "$out/dl"
  for f in "$out/bin"/*.gz; do ln "$f" "$out/dl/core-${f##*/}" 2>/dev/null || cp "$f" "$out/dl/core-${f##*/}"; done
  cp "$out/bin/latest.json" "$out/dl/core-latest.json"
  (cd "$out/dl" && shasum -a 256 core-*.gz > core-SHA256SUMS && printf '%s\n' "$version" > core-VERSION)
  echo "core edition $version: $(ls "$out/dl"/core-*.gz | wc -l | tr -d ' ') downloads in $out/dl"
  ;;
*) usage ;;
esac
