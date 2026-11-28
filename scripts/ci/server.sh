#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
lane="${1:-all}"
[ "$#" -le 1 ] || { printf 'Usage: scripts/ci/server.sh [all|node|managers|other|build|core-node|core-managers|core-other]\n' >&2; exit 2; }
case "$lane" in all|node|managers|other|build|core-node|core-managers|core-other) ;; *) printf 'Unknown server lane: %s\n' "$lane" >&2; exit 2 ;; esac
# Go's build cache key holds each package's directory unless -trimpath is set: add it, so checkouts and worktrees
# share compiled packages (#10614). The helper keeps the caller's GOFLAGS; vet clears -ldflags on its command line.
GOFLAGS=$(sh scripts/test/go_trimpath.sh)
export GOFLAGS

if [ "$lane" = all ] || [ "$lane" = build ]; then
  ci_run 'Go formatting' 120 python3 scripts/ci/gofmt.py server sdk/go
  ci_run 'Server vet' 600 bash -c 'cd server && go vet -ldflags= ./...'
  ci_run 'Server build' 600 bash -c 'cd server && go build ./...'
  ci_run 'Windows node build and vet' 600 bash -c 'cd server && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go vet -ldflags= ./...'
  # The release's tags (server/build-tags.txt, server/internal/buildtags): the programs build with them on Linux and
  # macOS, and the tunnel works with the tailscale.com subset they leave.
  ci_run 'Server programs with build-tags.txt' 600 bash -c 'cd server && t="$(cat build-tags.txt)" && go build -tags "$t" ./cmd/... && GOOS=darwin GOARCH=arm64 go build -tags "$t" ./cmd/...'
  ci_run 'Tunnel tests with build-tags.txt' 300 bash -c 'cd server && go test -tags "$(cat build-tags.txt)" -count=1 -timeout=4m ./internal/buildtags/... ./internal/tunnel/...'
  # The core alone runs (#13978): a persona_core_only hub and manager start against a disposable Postgres, the manager
  # loads no module, every module's program, extension and route is absent, and both stop cleanly.
  ci_run 'Core-only hub smoke' 900 python3 scripts/ci/core-only-smoke.py
  # Each module left out on its own (server/internal/modules/<id>/module.json, so a module that moves is covered with no
  # edit here; a "-" in the id is "_" in its tag): the server builds, and the module list and the guards pass.
  ci_run 'Builds without each module' 900 bash -c 'cd server && for m in internal/modules/*/module.json; do id="$(basename "$(dirname "$m")")" && t="persona_without_${id//-/_}" && go build -tags "$t" ./... && go test -tags "$t" -count=1 -timeout=4m ./internal/modules/all/... ./internal/archguard/... || exit 1; done'
  ci_run 'Extension schema and SDK bundle' 300 bash -c 'python3 scripts/test/test_extension_schema.py && python3 scripts/test/test_module_schema.py && python3 scripts/test/test_sdk_bundle.py'
  ci_run 'Go SDK tests' 300 bash -c 'cd sdk/go && go test -count=1 -timeout=4m ./...'
  # A feature added to Persona Core without changing Core (docs/extensions/core-only-features.md, #16244): the sample
  # extension's own tests, then the document's steps on a persona_core_only hub with a disposable Postgres: absent,
  # installed (flag, navigation entry, route, stored value), switched off (entry points gone, stored file unchanged),
  # on again and uninstalled (entry points gone, data directory explicitly deleted).
  ci_run 'Sample extension tests' 300 bash -c 'test -z "$(gofmt -l sdk/examples)" && cd sdk/examples/quicknotes && go vet ./... && go test -count=1 -timeout=4m ./...'
  ci_run 'Core-only sample extension' 900 python3 scripts/ci/core-sample.py
fi
if [ "$lane" = build ]; then exit 0; fi

# Persona Core without its feature modules, one group of steps per lane so that no hosted job outgrows its 20 minutes
# (the build lane alone ran them all and was cancelled at the limit; two lanes were still too few: core-node reached
# 20 minutes with three of them): four shards carry one or two each. `all` runs every group.
# Each step runs only what nothing else runs (#14412): `go build -tags persona_without_<id> ./...` and the module list and
# guard tests per tag are 'Builds without each module' (build lane), and every package's tests with persona_core_only are
# the core-node, core-managers and core-other lanes'. A step's own tag round recompiled internal/assistant's and
# internal/node's test binaries, about two minutes each on a hosted runner, for tests those lanes already ran in full.
if [ "$lane" = all ] || [ "$lane" = node ]; then
  if [ -d server/internal/modules/office ]; then
    ci_run 'Builds without the node modules' 900 bash -c 'cd server && go build -tags persona_core_only ./... && GOOS=windows go build -tags persona_core_only ./cmd/persona-node ./cmd/personad && for t in persona_without_office persona_without_screen_share persona_without_phone_extra; do GOOS=windows go build -tags "$t" ./cmd/persona-node ./cmd/personad && go test -tags "$t" -count=1 -timeout=6m ./internal/nodecmd/commandport/... ./internal/node/screenport/... ./internal/node/iphoneport/... ./cmd/persona-node/... && go test -tags "$t" -count=1 -timeout=8m -run "Builtin|IPhone|PortDial|Link" ./internal/node/ || exit 1; done'
  fi
  # Persona Core without its feature modules (docs/architecture/core-and-modules.md "The switch"): the core builds, and
  # the module list, the kernel and the assistant's tests of the briefing's paths (they skip what needs the module and
  # check the core without it) pass with the pilot left out; with every module left out they are the core lanes'. The
  # assistant runs a -run subset: its whole package takes about 9 minutes per tag, too long for this lane.
  ci_run 'Core-only and module-less builds' 900 bash -c 'cd server && for t in persona_without_briefing; do go test -tags "$t" -count=1 -timeout=6m ./internal/modapi/... ./internal/modules/briefing/... ./internal/assistant/kernel/... && go test -tags "$t" -count=1 -timeout=6m -run "Brief|Module|Setting|Routine|Facts|ScheduledConfiguration|NorthStar|Accept|Later|Golden" ./internal/assistant/ || exit 1; done'
fi
if [ "$lane" = all ] || [ "$lane" = node ]; then
  # Mail (docs/architecture/core-and-modules.md "Provider seams"): the extension manager reaches it through its port, so
  # the core without it builds and the manager's mail, inbox, connector and settings tests pass (those of mail itself
  # skip; TestManagerWithoutMail checks it is absent).
  ci_run 'Builds without the mail module' 900 bash -c 'cd server && for t in persona_without_mail; do go test -tags "$t" -count=1 -timeout=6m ./internal/mailwire/... && go test -tags "$t" -count=1 -timeout=6m -run "Mail|Inbox|Connector|SelfSettings|Sent|Recall|Golden|SourceMove" ./internal/extmgr/ || exit 1; done'
  # Meeting recording (docs/architecture/core-and-modules.md "Recording"): left out, the server builds and the core's
  # tests of where it meets the module pass (the routes, capabilities and room tools are absent, the module's paths
  # skip); its own tests do not depend on the tags and run in the shards.
  if [ -d server/internal/modules/recording ]; then
    ci_run 'Builds without the recording module' 900 bash -c 'cd server && for t in persona_without_recording; do go test -tags "$t" -count=1 -timeout=6m ./internal/recordinghost/... && go test -tags "$t" -count=1 -timeout=8m -run "Recording|Meeting|DayRecap|Evening|Minutes|Transcription|JustMe|Attendee|Bus|StartPath" ./internal/assistant/ || exit 1; done'
  fi
fi
if [ "$lane" = all ] || [ "$lane" = core-node ]; then
  # The built-in modules that run as extension programs (docs/architecture/core-and-modules.md "Provider seams"): left
  # out, the binaries build without their subcommands and the extension host registers no such extension. The modules'
  # own tests do not depend on the tags: they run in the shards.
  if [ -d server/internal/modules/volc ] || [ -d server/internal/modules/voice ]; then
    ci_run 'Builds without the speech modules' 900 bash -c 'cd server && for t in persona_without_volc persona_without_voice; do go test -tags "$t" -count=1 -timeout=6m ./internal/builtinext/... ./cmd/personad/... ./cmd/persona-manager/... && go test -tags "$t" -count=1 -timeout=6m -run "Volc|Voice|Transcription|Orphan|ModulesOff" ./internal/extmgr/ && go test -tags "$t" -count=1 -timeout=6m -run "TranscriptionEndToEnd" ./internal/assistant/ || exit 1; done'
  fi
  if [ -d server/internal/modules/lark ]; then
    # The Feishu / Lark module (#13404, docs/architecture/core-and-modules.md "Provider seams"): left out, the binaries
    # build, the extension host has no bot client (internal/botport) and no lark-cli adapter or connector
    # (internal/cliport), and their tests skip; the knowledge base's Lark import (internal/assistant/kblark) skips its
    # runs; the rest of the extension host and the assistant's Feishu paths still pass.
    ci_run 'Builds without the Lark module' 900 bash -c 'cd server && for t in persona_without_lark; do go test -tags "$t" -count=1 -timeout=6m ./internal/botport/... ./internal/botcard/... ./internal/larkhost/... ./internal/cliport/... ./internal/cliwire/... ./internal/assistant/kblark/... ./cmd/personad/... ./cmd/persona-manager/... && go test -tags "$t" -count=1 -timeout=8m -run "Lark|Feishu|Bot|Card|Connector|Inbox|Builtin|SelfSettings|Golden" ./internal/extmgr/ && go test -tags "$t" -count=1 -timeout=6m -run "Feishu|Bot|Card|Golden|KBLark" ./internal/assistant/ || exit 1; done'
  fi
fi
if [ "$lane" = all ] || [ "$lane" = core-node ]; then
  if [ -d server/internal/modules/github ] || [ -d server/internal/modules/imessage ]; then
    # The connector modules (docs/architecture/core-and-modules.md "Provider seams"): GitHub and iMessage reach the
    # extension manager through their ports (iMessage's node side through the node's), so the core without either builds,
    # the manager registers no such connector (the connector, inbox, sent and golden tests pass; those that need the
    # module skip), the assistant's CI checks still run on githubwire and the node answers as one without Messages.
    ci_run 'Builds without the connector modules' 900 bash -c 'cd server && for t in persona_without_github persona_without_imessage; do go test -tags "$t" -count=1 -timeout=6m ./internal/githubwire/... ./internal/imessagewire/... ./internal/connector/... ./cmd/persona-node/... && go test -tags "$t" -count=1 -timeout=6m -run "GitHub|IMessage|Connector|Inbox|Sent|Recall|Golden|Subscription|Market|Audit" ./internal/extmgr/ && go test -tags "$t" -count=1 -timeout=6m -run "TestCI|TestIMessage|Builtin|LocalConnector" ./internal/assistant/ ./internal/node/ || exit 1; done'
  fi
fi

if [ "$lane" = all ] || [ "$lane" = other ]; then
  ci_run 'Non-root extension container isolation' 420 python3 scripts/ci/extension-container.py
fi
if [ "$lane" = all ] || [ "$lane" = core-other ]; then
  # The core edition installs from its packages (#16239, docs/operations/core-install.md): this machine's packages of
  # both editions built as a release builds them, the document's commands run in a clean container against a disposable
  # Postgres, the install stays core-only through an update check and a second installer run, and the full edition
  # installs from the same directory unchanged.
  ci_run 'Core-only install from packages' 900 python3 scripts/ci/core-install.py --timeout 840
fi

fixture="$(mktemp -d "${TMPDIR:-/tmp}/persona-ci-db.XXXXXX")"
container="persona-ci-db-$(basename "$fixture" | tr '[:upper:]' '[:lower:]')"
cleanup() {
  docker rm -fv "$container" >/dev/null 2>&1 || true
  rm -f "${fixture:?}/database.json"
  rmdir "$fixture" 2>/dev/null || true
}
trap cleanup EXIT
# A disposable test database needs no durability: with fsync on, every commit waited for a busy CI disk and the
# database became the slowest part of the shards (docs/operations/testing.md, "Affected checks").
ci_run 'Postgres 18 fixture' 180 docker run --detach --name "$container" \
  -e POSTGRES_USER=persona -e POSTGRES_PASSWORD=ci-only -e POSTGRES_DB=persona_test \
  -p 127.0.0.1::5432 postgres:18-alpine \
  -c fsync=off -c synchronous_commit=off -c full_page_writes=off
ci_run 'Postgres readiness' 90 python3 scripts/ci/postgres.py "$container" "$fixture/database.json"
if [ "$lane" = all ] || [ "$lane" = node ] || [ "$lane" = core-node ]; then
  # The real-browser package has its own hosted runner, without build or other-package contention.
  ci_run 'Browser runner resources' 60 bash -e -o pipefail -c 'google-chrome --version; getconf _NPROCESSORS_ONLN; free -m; df -h /dev/shm'
fi
for shard in node managers other; do
  shard_budget=600
  if [ "$shard" = managers ]; then shard_budget=900; fi
  if [ "$lane" = all ] || [ "$lane" = "$shard" ]; then
    ci_run "Server $shard tests with Postgres" "$shard_budget" python3 scripts/ci/server-shards.py "$shard" --database-file "$fixture/database.json"
  fi
  # Persona Core alone (#13978): the same shard's every package with every module left out (persona_core_only); tests
  # of a module's own behaviour skip without it (needsBriefingModule, needsVoiceModule, needsVolcModule).
  if [ "$lane" = all ] || [ "$lane" = "core-$shard" ]; then
    GOFLAGS="$GOFLAGS -tags=persona_core_only" ci_run "Server $shard tests with Postgres, core only" "$shard_budget" \
      python3 scripts/ci/server-shards.py "$shard" --database-file "$fixture/database.json"
  fi
done
