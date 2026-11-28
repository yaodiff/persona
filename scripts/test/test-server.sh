#!/bin/bash
# Run every server test, including the Postgres ones, on the hub machine (it has the Postgres container).
#   ./scripts/test/test-server.sh [go test args...]
set -euo pipefail
cd "$(dirname "$0")/../.."
eval "$(python3 scripts/persona_config.py env)"
H="$PERSONA_HUB_SSH"
# One copy per checkout: worktrees testing at the same time must not rsync over each other.
D=".cache/persona-test-$(basename "$PWD")"
# Resolve the hub's state directory, never prepare it: a test run must not create it or its old-name alias (#1424). The
# resolver exports _PERSONA_STATE_DIR, so db.sh uses that one too.
python3 scripts/persona_identity.py --service hub --prefix scripts/deploy/db.sh | ssh "$H" 'bash -s' >/dev/null
ssh "$H" "mkdir -p ~/$D/sdk ~/$D/schema ~/$D/scripts/mock ~/$D/scripts/deploy ~/$D/scripts/ci ~/$D/scripts/release ~/$D/web/src/i18n ~/$D/integrations/antigravity/test"
rsync -a --delete --exclude bin server/ "$H":"$D/server/"
# Go's local SDK replacement must keep the same sibling path as in this checkout, including embedded schemas.
rsync -a --delete sdk/go/ "$H":"$D/sdk/go/"
# Manifest example tests parse the extension templates' persona-extension.json (../../../sdk/templates).
rsync -a --delete sdk/templates/ "$H":"$D/sdk/templates/"
# The extension page tests bundle guest code with Bun straight from the TypeScript SDK's sources (denohost's adapter.ts
# and testdata, extmgr's testdata import ../../../../sdk/ts/src). Its sources need no npm packages, so no node_modules (#1947).
rsync -a --delete --exclude node_modules sdk/ts/ "$H":"$D/sdk/ts/"
# Extension conformance tests read the source schemas' valid and invalid examples outside the Go module.
rsync -a --delete schema/extension/ "$H":"$D/schema/extension/"
# The wire type tests (assistantwire, todo projection) read the generated wire schemas from ../../../schema/wire (#10307).
rsync -a --delete schema/wire/ "$H":"$D/schema/wire/"
# The configuration form tests read the portable definitions from ../../../schema/configuration (#10028).
rsync -a --delete schema/configuration/ "$H":"$D/schema/configuration/"
# Wire, continuity and resolution tests read the shared fixtures next to server/ (../../../spec from a package),
# and the resolution ones check the apps' Swift sources against them.
rsync -a --delete spec/ "$H":"$D/spec/"
# The Antigravity pane tests (internal/node agypane_test.go) replay the integration's recorded wake fixtures from
# ../../../integrations/antigravity/test/fixtures (#11841).
rsync -a --delete integrations/antigravity/test/fixtures/ "$H":"$D/integrations/antigravity/test/fixtures/"
# Hub inbox list tests read this shared mock fixture from ../../../scripts/mock.
rsync -a scripts/mock/inbox_events.json "$H":"$D/scripts/mock/inbox_events.json"
# The domain rule tests (archguard TestDomainLayers, each package's TestDomainRules) read the wire-types baseline's
# bare-id names from ../scripts/ci (internal/archguard/domainrules/bareid.go).
rsync -a scripts/ci/wire_types_baseline.json "$H":"$D/scripts/ci/wire_types_baseline.json"
# The hub's TestNodeUpdateRelayWaitsForCommit checks the relay's commit wait against the COMMIT_WAIT this release script
# reads with (../../../scripts/release, #12673).
rsync -a scripts/release/promote_node_release.py "$H":"$D/scripts/release/promote_node_release.py"
# pgdocker's server-args test compares the container with this deployment script next to the Go module (#3575).
rsync -a scripts/deploy/db.sh "$H":"$D/scripts/deploy/db.sh"
rsync -a --delete --prune-empty-dirs --include '*/' --include '*.swift' --exclude '*' apple/ "$H":"$D/apple/"
# Some tests read docs next to the code they describe (docs/**/*.md).
rsync -a --delete --prune-empty-dirs --include '*/' --include '*.md' --exclude '*' docs/ "$H":"$D/docs/"
# kb's minutes tests compare their section names with the web's locale files (web/src/i18n/locales/<lang>/*.json).
rsync -a --delete --prune-empty-dirs --include '*/' --include '*.json' --exclude '*' web/src/i18n/locales/ "$H":"$D/web/src/i18n/locales/"
rsync -a scripts/test/server_test_env.py "$H":"$D/server_test_env.py"
# internal/node runs in the partitions CI and the release build use: serially it outgrew Go's ten-minute -timeout (#5594).
rsync -a scripts/test/go_partitions.py scripts/test/go_test_partitioned.py "$H":"$D/"
RESOLVER=$(python3 scripts/persona_identity.py --service hub --prefix -)
# The recovered service root is only for locating the database configuration, never the Go test environment.
# ~/.local/bin: the pinned Bun (docs/operations/testing.md), which a non-interactive shell may not have on PATH (#1947).
ssh "$H" "$RESOLVER
cd ~/$D/server && export PATH=\$PATH:\$HOME/.local/bin:/opt/homebrew/bin:/usr/local/go/bin && \
  python3 -I ../server_test_env.py --database-file \"\$PERSONA_HOME/hub.json\" -- \
  python3 -I ../go_test_partitioned.py ${GOCMD:-go test -count=1 -p 1} ${*:-./...}"
