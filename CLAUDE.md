# CLAUDE.md
## Product positioning: an Agent product for everyone

Persona is an Agent product for everyone, not a tool built only for software developers.

- **Assistant** is for everyday people: plain language, simple defaults, no engineering jargon.
- **Agents** is for people who collaborate directly with tools such as Claude Code; specialist workflows, engineering terminology, and advanced configuration belong here or in advanced settings.

Every design names its intended audience and answers "who does this serve?". Public-facing experiences stay simple; complexity lives in Agents or advanced settings.

**Fast, local-first and stateful:** show local feedback immediately and Processing as soon as the hub receives a message,
not after scheduling or model output. Keep receipt, delivery and execution distinct, make failures recoverable, and measure
feedback latency separately from execution latency (`docs/design/PRINCIPLES.md`, principle 7; super rule 5).


## Highest-priority rules

Read [the super rules](docs/SUPER_RULES.md) before acting. They take precedence over this file, `AGENTS.md`, and all
other repository documentation. Changes to the super-rules file, including additions and deletions, require a
GitHub issue and explicit approval from administrator `chyroc` in that issue before editing or opening a PR.
The PR must link the approved issue. No Agent or delegated task may bypass this requirement, even to fix a failing check.

**Product positioning: Persona is an Agent product for everyone, not only software developers.**

- **Assistant** is for everyday people, with no programming or Agent-tool knowledge required.
- **Agents** is for people who want to collaborate directly with Claude Code and other Agents.
- Before every design decision, ask **who it serves**. Keep Assistant and other everyday-facing interfaces simple and
  understandable; do not let developer terminology, engineering details or accumulating options make them harder to use.
  Put specialist workflows in Agents or advanced settings, and disclose details only when needed. For example, asking
  Assistant to organize a trip should not require choosing an Agent runtime or a repository.

Persona is a self-hosted personal SuperAgent. A Go server, `personad`, runs as the **hub** on an always-on machine and holds all state;
the clients (iPhone, Apple Watch, Mac, Vision Pro, Android and the web) talk only to the hub. Each of your computers runs a **node**
(connected to the hub through Persona Link), where coding Agents (Claude Code, Codex, pi, opencode and other runtimes) run in an engine
(herdr, Orca, Tern or Paseo); any client can watch them live, answer their questions and approve plans and tools.
The assistant's main chat (the **Persona** tab) hands work to those Agents and draws on the knowledge base (`~/.persona/data`), the Inbox,
mail, phone data tools and extensions (connectors, channels, voice). Overview: `README.md`, `docs/server/architecture.md`.

Values specific to your machines and accounts go in `CLAUDE.local.md` (not committed); without it, set up from `docs/operations/config.md`.

**Names**: the product is Persona; the binary is `personad`, the connector `persona-link`, the runtime directory `~/.persona`, the build
config `~/.config/persona/config.json`, environment variables `PERSONA_*`. Older names still work so old and new versions interoperate,
but their literals live only in each platform's compatibility files (`docs/protocol/rolling-upgrade-compatibility.md`); everything else
uses Persona. Registered app identities (bundle IDs, App Groups, keychain groups) come from private config and never change.

## Conventions (mandatory)

### Workflow

- **Every change goes issue → claim → branch → PR → merge after local verification passes** ("Development workflow" below). Code, docs and config alike;
  never commit or push to `main` directly.
- **Persona Core is controlled**: a PR that changes a path in `schema/core-paths.txt` (tests-only changes excepted) carries the
  `core-change` label and a `## Why this must change the core` section, and requests the owners' review; `scripts/hooks/merge-pr.sh`
  checks it (`CONTRIBUTING.md`, "Changing Persona Core"). Module directories are unaffected.
- **Old issue numbers**: `#N` in lines written up to 2026-11-07 (`git blame`) refers to the previous tracker; new text cites current numbers (`docs/operations/collaboration.md`, "Rules").
- **TestFlight feedback**: issues from crash reports or testers' feedback get the `testflight` label (or a `[TestFlight]` title prefix),
  with a scrubbed body and a dedupe marker (`docs/operations/collaboration.md`, "TestFlight feedback").
- **Write lessons down as you go**: pitfalls into `docs/lessons/<topic>.md` (index `docs/lessons/README.md`), design trade-offs into the
  module doc. Only the cross-module pitfalls people hit most get a line in "Most common pitfalls" here; keep this file short.
  One-off plans, acceptance reports and audits go to `docs/archive/` when done. New maintained docs go into the Docs index below
  (`scripts/test/test_doc_paths.py` checks the index and every quoted path).
- **Quality bar**: a top App Store app. Check every changed screen (watch included) in a simulator screenshot, dark and light, through
  DEBUG launch arguments + `xcrun simctl io <udid> screenshot`, and actually look at it; compiling is not enough (`docs/operations/testing.md`).
- **Screenshots go in the PR, never in the repository**: review evidence (light / dark, before / after, UI test captures) is shown in the
  PR body or a comment, not committed to `main`, docs included, and never pushed on a branch of their own (`artifacts/*`, `pr-assets/*`:
  every clone fetches them; `docs/operations/collaboration.md`, "Screenshots in PRs"). Only generated README / store images
  (`docs/assets/`) and app resources are committed. `scripts/test/test_no_committed_screenshots.py` blocks new ones.

### Public repository

This is a public open-source repository; everyone and every Agent maintains it to that standard.

- **Write as if anyone can read it**: code, docs, comments, test fixtures, file names, commit messages, issue and PR titles, bodies and
  comments. Edit history is public: changing the text later does not remove it, so don't write it in the first place.
- **No internal names**: internal company services, internal repositories, internal tools and intranet access setups never appear.
  Capabilities that need them are extensions kept in a private extension repository; this repository has only generic extension points and names.
- **Issue scope**: do not open issues here for changes to a private extension repository. If this repository also needs changes,
  describe only the work needed here in its issue, not the work in the private repository.
- **No private information**: no intranet domains / IPs / URLs, host names, real people's names or messages, personal accounts, mail
  addresses, tokens, keys or project IDs. Examples use placeholders (`<hub address>`, `me@example.com`). `scripts/privacy_check.py` checks
  (`git config core.hooksPath scripts/hooks` runs it on commit and push; `docs/operations/testing.md`, "Privacy scan").
- **Make it configurable**: machine names, domains, accounts, paths, launchers and preferences come from config (build / deploy:
  `~/.config/persona/config.json`; hub runtime: `~/.persona/hub.json`). The UI says "the hub machine" and "your computers".
- **Secrets never enter the repository**: the hub token and database password live only in `~/.persona/hub.json` (0600); mail
  credentials are AES-GCM encrypted in Postgres with the key file kept out of the database.

### Language

- **English for everything people read in the repository**: code comments, docs, commits, PR titles and bodies (a squash merge makes them
  the commit on `main`), issues and comments. Natural, concise English, not word-for-word translation. `docs/` is English only.
- **The README is bilingual**: `README.md` and `README.zh-CN.md` carry the same content and change in the same PR; their images come from
  `scripts/assets/make_readme_assets.py` in both languages.
- **User-facing text always goes through localization resources** (`docs/protocol/i18n.md`): Apple `apple/Shared/Localization/Localizable.xcstrings`,
  web `web/src/i18n/locales/`, Android `res/values*/strings.xml`, hub `server/internal/i18n/locales/`. Every new string ships in `en`,
  `zh-Hans` and `ja` together; the checks catch gaps. Apple: write `String(localized:)` / `Text`, run `scripts/i18n/apple_l10n.py sync <DerivedData>`
  after a build, then fill in en and ja. Apple tests run in zh-Hans.
- Chinese stays only as data that must match something real (hub or third-party output, prompts sent to a model, fixtures), marked
  `// i18n-ignore` where the checks ask.

### Architecture

- **Read `docs/architecture/evolution-guide.md` before changing code on any platform**: it states the design goals and the
  constraints every change keeps (the core never references a module, a switched-off module keeps the user's data, typed core
  values clients never derive themselves, constraints in the strongest layer, baselines only go down, rolling upgrades).
- A rule goes into a type or a check where one can hold it, and a new breach is never added to a baseline.

### Domain code

- **Server domain code is strict** (`docs/server/architecture.md` "Domain model"; the short form for Agents is `server/internal/CLAUDE.md`):
  a core holds value objects, never primitives; it has no exported fields, getters or setters; it is built only through parse / new /
  restore (a staged builder past four inputs), its data leaves only through `Accept(visitor)` and `Wire()` in its adapter, and it takes the
  clock and ids as arguments and returns failures as `outcome.Of`. Every new server package declares `//persona:layer`. Start one with
  `python3 scripts/gen/new_domain_slice.py go <dir> --doc "…"`; `go test ./<pkg>/...` names each broken rule and its fix.

### Server and data

- **All server state lives in the Postgres container** `persona-postgres` on the hub machine (`docs/server/server.md`); no JSON state files
  in `~/.persona/`. The exception is the knowledge base `~/.persona/data`: files first, the database as the index (`docs/assistant/knowledge.md`);
  its full-text search index (`.search/`) is a cache built from the files that can be deleted at any time (`docs/assistant/knowledge.md`, "Search index (#1273)").
- **The hub is only a communication center**: new business logic (endpoints, tables, model calls) goes into the managers
  (`server/internal/assistant`, `server/internal/devices`, `server/internal/extmgr`). `server/internal/hub/architecture_guard_test.go` pins the hub's routes,
  dependencies and Store; when it fails, move the logic as its hint says (`docs/server/architecture.md`).
- **Server-first logic, thin clients**: any logic that defines shared product truth, domain rules, or must agree across
  clients (state transitions, validation/idempotency, querying/windowing, projections/counts, permissions/affordances,
  orchestration, and cross-device state) belongs in the Go server; clients own presentation, local transient UI state,
  device-native capabilities, and offline/low-latency UX (`docs/architecture/server-first-logic.md`).
- **Prompts live in `server/internal/prompts/`** (MCP tool descriptions under `mcp/`): one prompt per file, composed with templates, never
  built with `fmt` / `+` in Go. Update the golden tests and read the diff (`docs/assistant/prompts.md`).
- **Prompt language**: user- and agent-facing templates carry `Language` / `Lang` from the caller and add en / zh-Hans / ja goldens. Preserve source data and existing empty-language bytes; follow `docs/assistant/prompts.md` ("Language migration"). The prompt-language CI inventory may only shrink.
- **Phones reach the hub directly over the internet** (TLS, preferring IPv6; VPNs are the user's own business), with the LAN kept
  compatible (`docs/operations/network.md`) An organization can instead keep everything on its own network
  (`security.private_network_only`, "Office network mode" there).

### Product and UI

- **Links open inside the app**: any link in a conversation opens in a card rising from the bottom that fills the screen; dragged down to
  0.6 it stops blocking the chat, further down it closes. Speed first (preloading, caching, reused WebViews). Selected text can go to the
  Agent as **AI edit** or **Use as context**, carried in the message as XML tags.
- **Never change how an existing screen looks directly**: a different visual style is a new **style** (`AppStyle`), a different color set a
  new **theme color** (`ThemeColor`); the default and every other option stay pixel-identical. Bug fixes and changes the user asked for
  don't count. Android works the same way with its three styles (`docs/android/android-design.md`).
- **The UI uses only the design system**: font sizes, spacing, radii, sizes, animation, colors, icons and brand marks come from tokens;
  pages never hard-code numbers, system font sizes, SF Symbol names or hex colors. Missing something? Add a token first.
- **Button text never wraps**: one line (`.lineLimit(1)`, then `.minimumScaleFactor`, shorter text or an icon); what doesn't fit in a row
  of buttons goes into a menu.
- **Persona Link stands alone**: Persona Link (`persona-link`, the connector's CLI/service) is installed only on computers whose Agents the
  hub should reach; it owns node connectivity and the Persona Screen and Input helpers. Its Mac menu bar App (`apple/PersonaLink/App`) and
  Windows tray (`server/internal/linktray`) match item for item and only pair the computer and keep its connection, with the Persona helper
  as their icon: no notifications, no Needs you, no session actions. No other desktop program installs, starts, stops or manages Link,
  and Link never depends on one (`docs/operations/link.md`, "Tray and menu bar App").

### Design spec for UI changes

- **Design principles**: `docs/design/PRINCIPLES.md` holds the seven product principles (defaults first, progressive disclosure,
  user control, consistency first, model and product separate, checkable rules, fast/local-first/stateful), each with how to check it. They apply to UI,
  settings and defaults, and how numbers are counted; acceptance criteria and the PR template's self-check are written against them.
- **Web Labs admission**: temporary trials or stable/new implementation choices only. Keep the stable default and lossless
  rollback, and link an issue with a plan to graduate or remove the entry. Put shipped module switches in General and lasting
  preferences in their owning category. Keep irreversible changes, internal debug controls and placeholder demos out of Labs
  (`docs/web/web.md`, "Labs").
- **Read the spec first**: before adding or changing any UI or interaction (any client, watch and web included), read
  `docs/design/PRINCIPLES.md` and the sections of `docs/design/DESIGN_SYSTEM.md` your change touches. Follow them; they win over
  habits copied from other screens. Known gaps against Apple's guidelines are in `docs/design/HIG_AUDIT.md`; don't add new ones.
- **Structure rules** (`docs/design/DESIGN_SYSTEM.md` sections 3.4, 4.8 and 5.6): in every grouping a session that needs the user is
  visible without opening anything; each pending request has one home, and every place that collects requests shows the same
  number; primary screens use the session's title and plain state words, never raw model IDs, session IDs, paths or engine names;
  a large UI change needs a person to look at its screenshots (section 1.4).
- **Say what you followed**: a PR that changes UI or interactions lists the principles and rules it followed, and for every
  deviation the reason. Run the "UI change self-check" in `docs/design/DESIGN_SYSTEM.md` and put the result in the PR.
- **Spec and code change together**: a new component, token or interaction pattern updates `docs/design/DESIGN_SYSTEM.md` in the same PR.

### Performance (Apple clients)

- **Read `docs/apple/performance.md` before changing `apple/` code.** Its rules P1–P12 cover main-thread IO, `@State`
  initial values, work in `body`, narrow reads, list identity, coalescing, images, background work, launch and memory,
  with budgets (no main-thread block over 100 ms, nothing over 16 ms while scrolling or typing).
- **A PR that touches `apple/` names the rules it followed** and the reason for any deviation (its review checklist).
  A performance-sensitive change (chat list, composer, Agents list, push handling, launch, images) attaches before and
  after measurements from an optimized build ("Measuring" there).
- `scripts/test/isolation-lint.sh` reads where each blocking call runs (Photos fetches, keychain and file reads, directory
  listings, image decodes) and counts it only on the main actor or where it cannot tell; no comment clears one.
  `scripts/test/test_apple_perf_patterns.py` keeps per-call localization lookups and unpaused animation timelines from
  growing per file. Neither takes a raised baseline.

## Development workflow

People and Agents follow the same steps, each with their own GitHub account. Templates and copyable `gh` commands: `docs/operations/collaboration.md`.

1. **Look for duplicates**: `gh issue list --state all --search "<keywords>"` and `gh pr list --state all --search "<keywords>"`, closed
   and merged ones included.
2. **Claim the existing issue, or create one** (`enhancement` for features, `bug` for fixes) saying what and why.
3. **Claim before you start**: comment `Claim:` / `Plan:` / `ETA:`; an Agent adds `Agent: yes (<machine id> / <branch or session>)`,
   using the config's machine id, never the host name. The author of the latest claim owns the work; nobody is assigned.
   Work that touches UI, settings or defaults, or how something is counted adds `Acceptance:`, criteria written against
   `docs/design/PRINCIPLES.md` (which principles, and what evidence shows each is met).
4. **Claimed by someone else**: if the owner updated it in the last 24 hours, leave it alone (adding information is fine). After 24 hours
   without an update, comment `Take over:` with the reason. Renew your own claim with `Progress:`.
5. **Work on your own branch / worktree** named with the issue number (`fix/123-xxx`). Bring it up to date by merging `origin/main`
   (preferred over a rebase; never force-push). Giving up: comment `Abandon:` with how far you got.
6. **Open one PR per issue**, in English, with `Fixes #N` / `Closes #N` (or `Refs #N` to link without closing) in the body. Fill in
   `.github/pull_request_template.md`, including the principles self-check when the change touches UI, settings, defaults or counts.
   `.github/workflows/pr-needs-issue.yml` closes PRs that link no issue and reopens them once the link is added.
7. **Merge after local verification passes**: run the checks in "Build and verify" for every affected area, including workflow lint for
   CI changes, and look at screenshots for UI changes. Opening or updating a PR does not run hosted CI. A scheduler dispatches full CI
   on `main`; its failures are fixed separately and do not replace the PR's local verification (`docs/operations/testing.md`, "Hosted CI").
   Then `scripts/hooks/merge-pr.sh <number>` (scans the PR with this machine's private values, which CI does not have, then squash-merges
   and deletes the branch). Make sure the issue is closed.

## Build and verify

- Portable CI: `scripts/ci/server.sh`, `scripts/ci/web.sh`, `scripts/ci/android.sh`, `scripts/ci/apple.sh`, `scripts/ci/scripts.sh`;
  runtime/SDK checks: `scripts/ci/runtimes.sh`. Same scripts locally and on hosted runners, no private config or signing secrets.
  Dependencies, optional path filters, coverage and cost: `docs/operations/testing.md` ("Hosted CI"). Manual dispatch defaults to all
  platform checks; no hosted repository guards run automatically on PRs.
- Server: `scripts/test/test-server.sh` (real Postgres on the hub machine); portable CI creates its own disposable Postgres 18 container.
  Format changed Go files before pushing (`gofmt -w` / `python3 scripts/ci/gofmt.py server sdk/go`, also run by `scripts/test/check-fast.sh`).
  Deploy: `scripts/deploy/deploy.sh [machine id…]`.
- iOS: `scripts/app/sim.sh [--with-watch] [-openSession <machine>/<session>]` builds, installs on your simulator and pairs it; without
  `--with-watch` it leaves out the watch app and share extension, and it builds only the Rust core's simulator slice
  (`docs/operations/testing.md`, "Development builds"). Before xcodegen / Xcode, run `python3 scripts/persona_config.py xcconfig`, and
  before a phone or Designed-for-iPad build in Xcode, `native/app-core/scripts/build-apple.sh` with no flag; before a manual `xcodebuild`,
  `eval "$(python3 scripts/persona_config.py env)"` or an old Xcode reports errors that look like someone else's breakage.
- Android: `scripts/app/android.sh [install|apk|test|lint|shots]` (`docs/android/android.md`).
- Shared Rust core: `scripts/ci/core.sh` (needs rustup); a change to a wire type regenerates the clients' codecs (`native/app-core/README.md`).
- HarmonyOS: from `harmony/`, run `node --test tests/*.test.mjs` and `node tests/run-domain.mjs` (Node.js 24+), then `devecocli build --build-mode release` with the API 26 SDK and locked native SSH core. Native device checks and fixture setup: `docs/harmony/harmony.md` ("Verification").
- Web: `cd web && npm test && npm run build`, the build again after merging the latest `origin/main` (it prints the first screen's room under
  `FIRST_SCREEN_BUDGET`; changes in flight share it); deploy with `scripts/deploy/deploy.sh web` (`docs/web/web.md`).
- Docs: `python3 scripts/test/test_doc_paths.py`, `python3 scripts/test/test_doc_go_paths.py`, `python3 scripts/test/test_doc_sections.py`
  (a section name quoted after a doc path names a current heading) and `python3 scripts/test/test_doc_wire.py`
  (every WebSocket type, feature flag, extension point and manifest field has its row). Every change: `python3 scripts/privacy_check.py`.
- Portable count ratchets (fixed sleeps, web font sizes, prompt language, hard-coded text, prompt sizes, the Android baselines) fail
  only when a count rises, so a PR need not lower a baseline; `python3 scripts/ci/lower_baselines.py` is tidying anyone can run
  (`docs/operations/testing.md`, "Count ratchets"). The Apple ratchets (`TokenRatchetTests`, the perf and isolation baselines) still
  fail on a fall.
- Releases: `docs/operations/release.md`. After `scripts/release/release.sh` uploads, run `python3 scripts/release/asc_testflight.py wait 45 <build number>`:
  it adds the build to the internal group and to the external group with Beta App Review. Report the build number and review status only after both.

## Most common pitfalls

The full list is in `docs/lessons/`; read the file for the area you touch first.

- **Several Agents share this repository**: work in your own worktree from `origin/main`, stage only your files, and check that
  `git diff origin/main...HEAD --stat` lists only them before pushing. Use your own simulator and mock hub port (check with `lsof`; stop a
  mock by its port, never `pkill -f mock_hub.py`). Name a new database migration by its UTC second, `$(date -u +%Y%m%d%H%M%S)_<name>.sql`,
  never a hand-picked number (`docs/lessons/workflow.md`).
- Scripts that wait for background jobs or logs always have a timeout and end with a one-line verdict on success, failure and timeout.
- **Simulators only through `scripts/app/sim-boot.sh`** (`--profile default|watch|live-activity|stock`, slimmed by default); never
  `simctl create/boot` or `-destination name=…`, and never reboot someone else's. At most 3 simulators are booted per Mac (a fourth
  boot waits, then exits 75): reuse one that exists (a new name adopts a shut-down `Persona …` simulator), `xcrun simctl shutdown <udid>`
  the moment you are done, and `xcrun simctl delete <udid>` when the task ends. A failure only on a slim simulator gets rerun on
  `--profile stock` first; a "no system prompt appeared" test proves something only on stock. UI tests launch with `app.launchPinned()` (`docs/operations/testing.md`).
- A DEBUG launch argument's name must not equal any Swift identifier, or `release.sh`'s strings check blocks TestFlight: name switches
  `open…` / `show…` and add each on its own line, in order, to `scripts/release/debug_switches.txt`.
- Release builds need `SWIFT_REFLECTION_METADATA_LEVEL = all`, or every App Intents / Shortcuts parameter is empty and the app crashes;
  Debug and the simulator never show it (`docs/lessons/ios.md`).
- A missing keychain group shared with extensions silently falls back on devices but works in the simulator: release ipas must pass
  `scripts/release/entitlements.py check` (`docs/operations/release.md`). Everything a Live Activity draws goes in its ContentState or
  App Group files; its extension can't use the keychain on a phone (`docs/apple/live-activity.md`).
- Don't write frequently changing state to UserDefaults (every write recomputes every `@AppStorage` view); use `StateCache`, and
  `@StoredValue` for heavy Mac views (`docs/lessons/macos.md`).
- App targets default to MainActor: closures the system calls on another queue are `{ @Sendable … in`, or they crash; plain data types in
  `apple/Shared` are `nonisolated`. Polling and read receipts use `.tabActiveTask` / `.onTabVisibility`, since hidden pages never get `onDisappear`.
- On the Mac, a constantly running `TimelineView(.animation)` relayouts the window every frame; hand persistent animations to Core Animation (`MacBreathingRing`).
- **Minimum compiler is Xcode 26.0 (Swift 6.2)**: wrap newer-SDK APIs in `#if compiler(>=6.4)` with the old behavior in `#else`
  (`#available` covers only runtime). Build with 26.0 when you have it; otherwise note the Xcode you used and "26.0 not verified" in the
  PR. Raising the minimum is the user's decision (`docs/lessons/ios.md`, "Minimum compatible compiler").
- The watch app targets watchOS 10 and compiles all of `Shared/`: only watchOS 10 APIs, with `#available` fallbacks (`Color.blended`, not `Color.mix`);
  `sim.sh` leaves the watch out, so check a `Shared/` change with `sim.sh --with-watch` (it says so; `docs/operations/testing.md`, "Development builds").
- The iPhone and iPad app targets iOS 18.6 (#6730): an iOS 26 API gets `if #available(iOS 26.0, *)` with the older behavior, or a
  `SystemCompat.swift` helper; toolbar items choose on the view, since `ToolbarContentBuilder` can't (`docs/lessons/ios.md`, "iOS 18.6 floor").
- Someone else's redeploy can wipe the hub's memory at any time: flows that span requests must be self-contained or persisted.
- The hub's port belongs to `personad.socket` (socket activation) and versions switch blue-green with `personad hub switch`. Before going
  back to a binary that doesn't understand `LISTEN_FDS`, run `PERSONA_HUB_SOCKET=0 scripts/deploy/hub-systemd.sh`; to stop the hub, stop the
  socket too (`docs/operations/hub-restart.md`).
- Behind a container port mapping every connection can look like the bridge gateway, and a same-machine reverse proxy makes internet
  traffic look like loopback: never trust the source address alone (`containerGateway`, `security.proxy_protocol`; `docs/operations/network.md`).
- Claude Code's config directory isn't always `~/.claude` (`CLAUDE_CONFIG_DIR`), and one session id can exist in two; take the most
  recently modified. Verify transcript or sync changes against real transcripts and bump the app's `ChatCache` version when items change
  (`docs/lessons/agents.md`, `docs/lessons/transcript.md`).
- Claude Code's transcript and hook `cwd` follow the model's Bash `cd`: place assistant and health conversations by the directory they
  started in, never their latest cwd (`docs/lessons/transcript.md`, #2368).

## Design system (iOS)

Tokens live in `apple/Shared/Design` (also compiled for the watch and widgets); values and rules in `docs/apple/ios.md` ("Design language").

| Need | Use |
| --- | --- |
| Font, emphasis, line height | `Theme.Font` levels and emphasis tokens, `.lineHeight(_:)` |
| Spacing | `Theme.Space` (4 pt grid; `page` is the side margin) |
| Corner radius | `Theme.Radius` |
| Size | `Theme.Size` + `.scaledFrame(…)` (scales with the font size); plain `frame` only for lines of 4 pt or less |
| Sidebar rows | `Theme.Sidebar` (web `--sidebar-row`): HIG row height + padding, same in every language (`docs/apple/sidebar.md`) |
| Animation | `Theme.Motion`; chat content unfolds with `.unfolding` |
| Color | `Theme` role colors, `Theme.Fill`, `Theme.Diff`, `Theme.Hue`, `Palette`, `AgentState.tone` |
| Icons, logos | `Theme.Icon` (one SF Symbol per meaning) drawn with `ThemedIcon` / `IconTile` in the style's icon language (`docs/apple/icons.md`); `Theme.Brand` drawn through `BrandMark` / `BrandTile` / `BrandBadge` |
| Style × theme color | `StyleSpec.current` (`ComponentSpec` × `ColorSpec`), one file per style in `apple/Shared/Design/Styles/` |

- **Components** live in `apple/Persona/DesignSystem`: `.surface(…)`, `.themedButton(…)`, `.themedPage()` / `.themedGroupedPage()`,
  `.themedRow()`, `Chip`, `CountBadge`, `EmptyStateView`, `Monogram`, `Loadable`, `SecretField` (never `SecureField`). Never write
  `glassEffect` / `.glass` directly or assemble a lookalike component in a page.
- **A new style** is a `ComponentSpec` + an `AppStyle` case, **a new theme color** a `ColorSpec` + a `ThemeColor` case; everything else reads
  `StyleSpec.current`, never `switch AppStyle`. BeautifulUI components appear only under that style, chosen inside design-system components.
- **Checks** in PersonaTests: `TypeScaleTests`, `IconVocabularyTests`, `TokenRatchetTests` (hard-coded values per file can only go down;
  after reducing them, delete `apple/PersonaTests/TokenBaseline.json` and rerun) and `StyleSpecTests` (contrast). Never add exemptions or raise a baseline.
- **Logic layer**: load → show → edit → save pages use an `@Observable` `XxxModel` with dependencies from `Deps` and tests with `FakeHub`;
  pull pure computation out of busy pages first (`docs/apple/ios.md`, "Logic-layer choice").
- **Android** has its own design system (`android/core/designsystem`, `docs/android/android-design.md`); `scripts/app/android_token_baseline.py`
  keeps hard-coded values going down. Check UI changes with `scripts/app/android.sh shots` in all three styles, dark and light.

## Layout

| Path | Contents |
| --- | --- |
| `server/` | Go module: `personad` (hub / node / hook / mcp and helper commands), `persona-link`; `docs/server/server.md` |
| `desktop/` | Electron desktop shell around the existing web app (`docs/desktop/desktop.md`); it does not manage Persona Link |
| `apple/` | XcodeGen project (`apple/project.yml`): iOS, watchOS and visionOS (`Vision/`) apps, extensions, shared code in `Shared/`, tests; `apple/PersonaLink/` is Persona Link's Mac side: Persona Screen and Input, and the menu bar App in `App/` (`docs/operations/link.md`) |
| `android/` | Kotlin + Compose: `app`, `core/*`, `feature/*`, `platform/*` (`docs/android/android.md`) |
| `web/` | Web app (Vite + React + Tailwind) served by the hub (`docs/web/web.md`) |
| `integrations/` | Agent runtime integrations and the runtime SDK (`docs/integrations/runtimes.md`) |
| `native/iphone-bridge/` | Optional Rust bridge a node runs to operate a paired iPhone, built on its own (`docs/extensions/iphone-bridge.md`) |
| `native/app-core/` | The shared Rust core under the iOS, Android and HarmonyOS clients (`docs/plans/plan-rust-core.md`); `scripts/ci/core.sh` |
| `sdk/`, `schema/`, `spec/` | Extension SDK, extension JSON Schemas, shared behavior specs and fixtures |
| `scripts/` | Build, test, deploy, release, mock hub and config tools (`scripts/README.md`) |
| `deploy/docker/`, `Dockerfile` | Docker deployment of the hub (`docs/operations/docker.md`) |
| `site/` | Website, privacy policy and terms (`docs/operations/site.md`) |
| `docs/` | Design docs (`docs/README.md`, index below) |

## Docs index

Every maintained doc under `docs/` (lessons excluded) must be listed here.

| Area | Docs |
| --- | --- |
| Agent-to-Agent communication | `docs/integrations/a2a.md` session addresses, durable delivery, and the A2A JSON-RPC interface |
| Super rules | `docs/SUPER_RULES.md` highest-priority rules and prior administrator approval for changes |
| Start | `docs/README.md` navigation · `docs/operations/getting-started.md` setup without a terminal · `docs/assistant/onboarding.md` new-user onboarding · `docs/operations/config.md` config.json and hub.json · `docs/operations/collaboration.md` claims, PRs, templates · `docs/protocol/i18n.md` localization |
| Feature map | `docs/feature-map.md` source-verified capabilities, public entries, implementation, test and deployment evidence, gaps and maintenance protocol |
| Architecture and protocol | `docs/server/architecture.md` · `docs/architecture/evolution-guide.md` the architecture's design goals and the constraints every change on every platform keeps · `docs/architecture/core-and-modules.md` Persona Core and feature modules: the module contract (`modapi`, `schema/module.schema.json`), registry, switch, guards, how to add a module · `docs/architecture/server-first-logic.md` server-first domain logic, thin-client boundaries, additive wire compatibility and PR self-check across all Persona domains · `docs/protocol/protocol.md` HTTP / WebSocket API and node RPC · `docs/protocol/file-transfer.md` receive folder and computer to computer transfers · `docs/protocol/agent-states.md` · `docs/protocol/counts.md` the counts people see: one view per manager, what each count id means, how an id may change and when the clients' fallbacks go · `docs/protocol/deep-links.md` one route table for links to Persona's pages: web paths, `persona://open<path>`, older forms, security · `docs/protocol/multi-hub.md` one device, several hubs · `docs/protocol/connector-protocol.md` · `docs/protocol/rolling-upgrade-compatibility.md` old / new name interop · `docs/operations/rename.md` naming contract · `docs/operations/persona-cutover.md` approval-gated naming cutover tool |
| Server | `docs/server/server.md` modes and operations · `docs/server/mesh.md` linked hubs · `docs/server/machines.md` monitoring · `docs/server/machine-usage-map.md` disk usage map: node scan and trash, the `disk_offload` extension point, the treemap on web and Apple, low-disk guidance (#14576) · `docs/server/usage-windows.md` subscription usage · `docs/server/launcher-health.md` · `docs/server/agent-sharing.md` trusted ownership and public sharing contract · `docs/server/agent-sandbox.md` macOS agent sandbox |
| Operations | `docs/operations/network.md` direct connection, TLS, pinning · `docs/operations/docker.md` · `docs/operations/hub-restart.md` seamless restarts · `docs/operations/hub-update.md` · `docs/operations/upgrade.md` moving an install to the split hub and manager · `docs/operations/core-install.md` the core edition: Persona Core alone as install packages, installing it by hand, how its updates stay core-only, the install check · `docs/operations/push-relay.md` · `docs/operations/admin.md` admin UI · `docs/operations/ports.md` port forwarding · `docs/operations/clipboard.md` clipboard sharing with the phone · `docs/operations/link.md` Persona Link and the node service · `docs/operations/persona-link-install.md` · `docs/operations/node-update.md` · `docs/operations/mac-node-signing.md` signing a Mac's node with its own certificate so privacy permissions survive updates · `docs/operations/windows.md` · `docs/operations/auth-relay.md` · `docs/operations/site.md` website · `docs/operations/testing.md` tests, simulators, privacy scan · `docs/operations/logical-input-reconciliation.md` blocked logical inputs and audited per-input resolution · `docs/operations/runtime-cli.md` DEBUG command channel · `docs/operations/developing-persona.md` the Developing Persona switch: a device's logs, errors and energy data, the package format and scrubbing · `docs/operations/pr-review-automation.md` maintainer-side semantic PR review and exact-head merging · `docs/operations/main-verification.md` independent main build/vet/rule verification and bounded repair PRs · `docs/operations/release.md` signing and TestFlight |
| Agents and runtimes | `docs/integrations/engines.md` herdr / Orca / Paseo, the host layer · `docs/integrations/orca.md` how the node drives Orca: connection, terminals, Orca's chats, bot endpoints · `docs/integrations/herdr-core.md` how the node drives Herdr App (herdr-core) · `docs/integrations/claude-integration.md` · `docs/integrations/codex.md` · `docs/integrations/codex-app-server.md` · `docs/integrations/runtimes.md` runtime spec · `docs/integrations/daemons.md` · `docs/integrations/dsh.md` · `docs/integrations/managed-agents.md` Managed Agents sessions (Ark cloud or self-hosted, your own key) · `docs/integrations/pi.md` · `docs/integrations/omp.md` Oh My Pi (pi family) · `docs/integrations/opencode.md` · `docs/integrations/paseo.md` · `docs/integrations/agent-goal.md` · `docs/integrations/terminal.md` · `docs/integrations/launchers.md` model providers · `docs/integrations/script-cli.md` `personad ask` / `notify` / `decision` |
| Assistant | `docs/assistant/assistant.md` main chat · `docs/assistant/approval-clarification.md` owner questions on prepared approval cards · `docs/assistant/managers.md` · `docs/assistant/assistant-views.md` rich UI · `docs/assistant/dashboards.md` Dashboards: pages a conversation drafts and the owner keeps in the sidebar, writers, Keep, limits, events · `docs/assistant/unified-entry.md` · `docs/assistant/agent-work.md` work state and triage · `docs/assistant/north-star.md` interruption and availability metrics · `docs/assistant/todos.md` · `docs/assistant/todo-status-sop.md` to-do status sections · `docs/assistant/voice.md` · `docs/assistant/urgent-call.md` urgent calls: when the assistant rings the owner's iPhone, the gates, the call and its fallbacks · `docs/assistant/health.md` · `docs/assistant/knowledge.md` knowledge base · `docs/assistant/meeting-minutes.md` minutes from recorded meetings · `docs/assistant/day-recap.md` all-day recording and the daily recap · `docs/assistant/inbox.md` · `docs/assistant/model-config.md` · `docs/assistant/model-profiles.md` · `docs/assistant/execution-spec.md` runtime, channel, model and effort: channels, launcher aliases, the combination check · `docs/assistant/prompts.md` · `docs/assistant/office.md` Word / PowerPoint / Excel |
| Extensions and sources | `docs/extensions/extensions.md` extension system · `docs/extensions/core-only-features.md` adding a feature to a core-only install without changing Core: the extension route step by step, the sample `sdk/examples/quicknotes`, what stays when it is off, the gaps · `docs/extensions/source-api-handlers.md` proposed authenticated source API, assistant handlers, bounded media and durable confirmation, with independent credential/session subcontracts (#1185) · `docs/extensions/volcengine-speech.md` the built-in Volcengine speech extension: meeting transcription on the hub and answers read aloud with the owner's key, one switch, connection test · `docs/extensions/extension-settings.md` host-owned settings layout · `docs/extensions/telegram-mtproto-session-transport.md` proposed controlled MTProto sessions and synthetic-only design evidence · `docs/extensions/secret-providers.md` secret values from provider extensions (references, resolution, records) · `docs/extensions/repository-providers.md` code-hosting sources from provider extensions (hosts, `repo.*` calls, when the provider is gone) · `docs/extensions/connectors.md` · `docs/extensions/subscriptions.md` · `docs/extensions/channels.md` · `docs/extensions/extension-messaging.md` · `docs/extensions/computer-use.md` · `docs/extensions/iphone.md` phone control · `docs/extensions/iphone-bridge.md` operating a paired iPhone · `docs/extensions/iphone-control.md` element numbers, results, limits and approval for the iPhone · `docs/extensions/iphone-pairing.md` iPhone control: pairing and sessions · `docs/extensions/mail.md` · `docs/extensions/wechat.md` · `docs/extensions/bots.md` Lark bot · `docs/extensions/feishu.md` · `docs/extensions/lark-agent.md` · `docs/extensions/lark-migration.md` · `docs/extensions/lark-attachments.md` · `docs/extensions/lark-encryption.md` |
| Apple | `docs/apple/ios.md` iOS / watchOS, design language · `docs/apple/icons.md` icons · `docs/apple/sidebar.md` sidebar rows · `docs/apple/ios-state.md` state and redraws · `docs/apple/performance.md` performance rules, budgets and measuring (read before changing `apple/`) · `docs/apple/shell.md` · `docs/apple/vision.md` · `docs/apple/live-activity.md` · `docs/apple/home-widgets.md` Home Screen and Lock Screen widgets · `docs/apple/intents.md` · `docs/apple/siri.md` · `docs/apple/calls.md` system calls (calling Persona, urgent calls) · `docs/apple/carplay.md` · `docs/apple/persona.md` avatars · `docs/apple/phone-tools.md` · `docs/apple/recording.md` · `docs/apple/recording-rolling.md` · `docs/apple/meta-glasses.md` |
| Desktop | `docs/desktop/desktop.md` Electron desktop app: packaging, security and presence; Persona Link is separate (`docs/operations/link.md`) · `docs/desktop/mac-acceptance.md` maintainer's physical-Mac acceptance checklist for #6770: Gatekeeper on unsigned packages, sign-in, flows, notifications, presence, Link and Screen Recording |
| Design | `docs/design/PRINCIPLES.md` the seven product design principles, how to check each, cases · `docs/design/DESIGN_SYSTEM.md` tokens, components, interaction and content rules (read before any UI change) · `docs/design/HIG_AUDIT.md` Apple HIG audit of the Apple clients, findings by severity · `docs/design/DECISIONS.md` the history of the design rules: what was decided when, and what it replaced · `docs/design/sidebar-navigation.md` the iPhone drawer and iPad sidebar: places keep their stacks, tasks open over the drawer, options compared · `docs/design/proposals/owner-at-a-glance.md` proposal: secretary → leaders (directions) → tasks in one view, New chat as a direction, leaders' reports to a work-log extension, one Needs you · `docs/design/proposals/continuous-improvement-loop.md` proposal: Opus plans and verifies, cheaper execution models by task type, the relay's hybrid mode, cadence and rollback · `docs/design/proposals/meeting-recording.md` proposal: the iPhone meeting recording page, live view, wrap-up, motion, haptics, Live Activity layout · `docs/design/proposals/main-chat-availability.md` proposal: a background supervisor for the main chat's tasks, the owner's message first, `ask_user` without holding the turn, reply-wait metrics, fewer asks · `docs/design/proposals/hub-token-boundary.md` proposal: a token on the hub machine is not the owner's approval; what the hub's account can reach, agent isolation vs a terminal-bound token vs person-confirmed changes · `docs/design/proposals/assistant-instructions.md` proposal: one home for each assistant instruction (shared core, role blocks, tool descriptions, skills), behaviour checks before cuts · `docs/design/proposals/inbox-sources-migration.md` proposal: built-in Inbox sources become an extension's read tools plus a default subscription; what subscription items lack, the per-source switch, rollback and order · `docs/design/proposals/assistant-search.md` proposal: find an assistant chat by what was said in it (a manager-side index of the user's messages and the replies, `GET /api/v1/assistant/search`, opening at the hit) and one rule for search in chat (no hidden tags, quoted contexts, injected lines as shown, folded work) · `docs/design/proposals/todos-redesign.md` proposal: the To-dos redesign (paper and pane), its domain model and the architecture convergence plan · `docs/design/prototypes/todos/README.md` clickable To-dos prototypes (V1–V3), each variant's notes in `docs/design/prototypes/todos/v1/notes.md` · `docs/design/prototypes/todos/v2/notes.md` · `docs/design/prototypes/todos/v3/notes.md` · `docs/design/proposals/team-project-spaces.md` proposal for versioned documents, membership and execution grants in opt-in team projects |
| Android and web | `docs/android/android.md` · `docs/android/android-design.md` · `docs/web/web.md` · `docs/web/workspace-board.md` · `docs/web/review.md` reviewing a session's changes · `docs/web/preview.md` dev server previews · `docs/web/screenshots/task-workspace/README.md` workspace policy browser fixture and captures · `docs/web/screenshots/task-execution/README.md` scheduled execution form fixture and captures · `docs/design/task-cards.md` task cards on iOS and Web compared, the merged design, a shared token table and the product calls open (#5569) |
| HarmonyOS | `docs/harmony/harmony.md` native API 26 phone and foldable client: product, design, interaction, architecture and verification · `docs/harmony/xiaoyi.md` HarmonyOS 7 XiaoYi integration (On-Device A2A, Intents Kit, Skills, ShareKit, and OpenClaw bridge) · `docs/harmony/prototype.html` historical design preview notice · `docs/harmony/device-validation.md` environment, measured windows and physical-device procedures; build entry `harmony/README.md` |
| Plans (open decisions) | `docs/plans/plan-typed-core.md` a strongly typed Core boundary (#15397): inventory of weak types in the contract layer, closed enums with an explicit unknown, ID newtypes, the five Agent phases (D-A/D-B/D-C), generator upgrades, ratchets, step-by-step PRs alongside Core slimming · `docs/plans/plan-kb-recording-port.md` moving the meeting-recording storage out of the knowledge base behind a port: inventory, coupling, port sketch, locks, PR plan (#13407) · `docs/plans/plan-lark-remaining-ports.md` the Feishu files still in the core and the ports that let them move into the lark module: inventory, seams, port sketches with module-off behaviour, PR plan, open questions (#13404) · `docs/plans/plan-refract-settings-pilot.md` bounded Refract audit for Android's About version group: native model boundaries, source/generator ownership, internal-only rollback and the iOS/Android/HarmonyOS evidence gates (#9620) · `docs/plans/plan-inbox-vertical-slice.md` the Inbox as the first vertical domain slice: what it owns, target boundaries on the server and every client, small PRs with conflict cost, rollback and evidence, metrics and the decided list (#11309) · `docs/plans/plan-derived-counts.md` the counts people see (Needs you, Inbox, To-dos, app icon, widgets, Live Activity) computed once in the managers and relayed by the hub: inventory, disagreements, target, small PRs with conflict cost, gates, rollback and evidence, metrics and the decided list (#11679) · `docs/plans/plan-extensions.md` · `docs/plans/plan-connectors.md` · `docs/plans/plan-orca-support.md` the Orca live terminal (a streaming no-go) and the open decisions · `docs/plans/plan-tmux-support.md` · `docs/plans/plan-lark-cli.md` · `docs/plans/plan-discord.md` · `docs/plans/plan-x.md` · `docs/plans/plan-ego-browser.md` · `docs/plans/plan-antigravity.md` · `docs/plans/plan-agent-dispatch.md` · `docs/plans/plan-session-search.md` searching Agent sessions by content across computers (#757): each node searches its own transcripts (a bounded scan, mnemo where installed), the manager fans out, redacted snippets, open decisions · `docs/plans/plan-rust-core.md` one Rust core for the iOS, Android and HarmonyOS clients (#6793): crate layout, bindings, phases, go / no-go · `docs/plans/plan-rust-core-phase4-storage.md` phase 4 design: per-hub caches and the launch snapshot in the core, file layout, versioning, PlatformHost capabilities, switch order, open maintainer decisions (#9085) · `docs/plans/plan-rust-core-sync.md` phase 2 `sync` domain design: the iOS survey with file/line citations, the `sync-driver` fixture inventory, clock/network injection and the first pure-driver slice (#9230) · `docs/plans/android-ios-alignment/README.md` aligning the Android app with iOS (#4194): decisions and phases, each phase's requirements and technical design in `docs/plans/android-ios-alignment/phase-0-decisions-and-baseline.md` · `docs/plans/android-ios-alignment/phase-1-tokens-and-guards.md` · `docs/plans/android-ios-alignment/phase-2-interactions.md` · `docs/plans/android-ios-alignment/phase-3-feature-gaps.md` · `docs/plans/android-ios-alignment/phase-4-staying-aligned.md`, and the areas compared as whole screens in `docs/plans/android-ios-alignment/agents-page.md` · `docs/plans/android-ios-alignment/chats.md` |
| Inbox UI evidence | `docs/web/screenshots/lark-inbox/README.md` synthetic Lark Inbox review captures |
| Lessons and archive | `docs/lessons/` pitfalls by topic (index `docs/lessons/README.md`) · `docs/archive/` dated records, evidence only (index `docs/archive/README.md`) |
