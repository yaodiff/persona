# Desktop app and Persona Link

The desktop experience for macOS and Windows is an Electron shell around the hub-hosted web app. It is not a second chat interface, a second design system or a replacement for the computer's node service.

## Two independent installations

**Persona desktop** is for everyone who wants to chat and use Agents on any computer. It needs no local node and no Persona Link. It supplies the existing web sign-in and screens, native notifications, the existing Needs-you badge, desktop presence, single-instance behavior, window-state restoration, deep links, a tray with close-to-background behavior, a visible launch-at-login setting and public-release update wiring.

**Persona Link** is only for a computer whose Agents the hub should reach. Its CLI and service are independently installable and useful without the desktop app, including on Linux and headless computers. The connector owns the hub connection, node-service management and the Persona Screen recording helper. Do not delete these capabilities when removing an old desktop UI.

When an installed Link is present, the desktop's local setup page can show its redacted status and offer fixed operations for pairing, reconnect and node-service installation. Link remains a separate install; the desktop app never silently installs a background service or bundles one as a prerequisite. When it is absent, those controls are disabled and the page offers the separate installation guide.

After sign-in, **Settings › General › This computer** opens that same local options window through the additive `openLocalSettings` bridge capability. A normal browser and an older desktop without the method do not show the entry. The hub renderer can request only that the window open: it receives no Link command, local status, hub address or credential. The separate local window retains its own origin-checked preload for the fixed operations. Merely opening or closing it does not navigate or destroy the signed-in hub window. If the person explicitly chooses **Continue** for a different hub, the shell waits for the old hub window to close and creates a new window with that hub's own persistent session; merely saving the global address cannot leave the old page denied by IPC. The acceptance smoke checks both boundaries, the absent-Link explanation and the second hub's sign-in; it does not perform real pairing.

## Existing web behavior

- The desktop loads only the configured hub origin (including its reverse-proxy prefix). Other HTTP/HTTPS links open in the system browser. Authentication, service-worker permission actions, notification text/tags/deduplication/withdrawal, quiet hours and the Needs-you count stay in the existing web code.
- Native notification clicks return to the existing SPA route. Native display failure keeps the browser fallback. Signing out clears the previous account's native notices.
- `register_desktop` uses the existing authenticated web socket, never a token in the Electron main process. An active, unlocked, recently used desktop with notifications may register. Locking, idling or disabling notifications withdraws it. New hubs use `unregister_desktop`; older hubs use socket-close withdrawal and reconnect only when active and notifying again. Resume alone does not mean unlocked; only the actual unlock event restores presence.
- Standard platform TLS validation is used. There is no broad certificate bypass and no unverified saved-pin shortcut.

## Process and privilege boundaries

`desktop/` uses TypeScript and Electron. The hub renderer has `contextIsolation: true`, `nodeIntegration: false`, sandboxing, web security, no webviews, no Node workers/subframes and no production DevTools. The privileged local setup window has a different preload from the hub window.

Preloads expose fixed operations, not arbitrary IPC, WebSocket messages, paths, tokens or process execution. Desktop IPC checks the owning window, current main frame and expected origin. Link commands use only `~/.persona/bin/persona-link` (`.exe` on Windows), fixed argv, no shell and pairing input on stdin. Status returns only the state and names needed by the UI, not `link.json`, internal addresses, PIDs, logs or node errors. Per-window handlers are removed when that window closes. The local window's login-item operations use their own setup-only channels; its cleanup never removes the hub window's public `DesktopBridge` login-item handlers.

### One bridge type, additive compatibility

`spec/desktop-bridge.d.ts` is the single pure-type `DesktopBridge` contract shared by the web renderer and desktop preload. `web/` and `desktop/` remain separate build roots: both use `import type` and neither bundles a runtime module from that file. The hub-window preload annotates its complete implementation with `satisfies DesktopBridge`; web mocks use the same type. Runtime route validation and the local setup-window's separate privileged contract are not part of this type-only file.

**Compatibility rule:** the desktop application may update more slowly than the hub-hosted web it loads. Version 1 therefore only adds capabilities. The web checks for an additive method or field before using it and keeps its browser fallback when an older desktop does not provide it. `openLocalSettings` is such an optional addition: without it, the Settings entry stays absent. A removed or incompatible method, parameter, return value or meaning requires incrementing `version` and an explicit migration, never silently changing the version-1 contract.

Closing the main window hides it to the tray while the signed-in renderer stays alive. The tray opens the current main window, not a destroyed setup window; explicit Quit exits. Only the desktop app itself is controlled by launch at login, not the Link service. macOS may still require approval in System Settings, which the setting must report rather than claim success.

## Packaging and verification

`electron-builder.yml` defines universal macOS DMG/ZIP and Windows NSIS artifacts. `electron-builder.unsigned.yml` uses a temporary development app ID and explicitly disables certificate discovery/signing and notarization. It is for development only. Unsigned artifacts do not prove signed distribution, notarization or installation.

CI has a Linux job (policy tests, the web client's build and the headless acceptance smoke), macOS universal packaging and Windows NSIS jobs, each with the acceptance smoke on its own runner and its screenshots uploaded (`persona-desktop-<os>-smoke`). Windows is built on Windows, not via a failed Wine cross-build. The package steps run whether or not the smoke passed, so each reports its own verdict. The Linux job builds the web client once (`scripts/ci/desktop-web.sh`) and hands the bundle to the other two jobs. The desktop lane needs that bundle, not the web lane's verdict: when the only thing that failed in the web build is its first-screen size budget, checked after every file is written, the script warns and the smoke runs on the complete bundle; any other build failure fails the job. The development workflow builds unsigned packages only and never receives signing credentials; signing and notarization run only in the release workflow. Signed artifacts require explicitly supplied credentials and an authorized run; no certificate is purchased, inferred from App Store signing or sought from a private key store. The secrets and what each must contain are listed under "What the owner must provide" below.

### The acceptance smoke

`scripts/ci/desktop-smoke.sh` (`node desktop/scripts/smoke-with-hub.mjs [output directory]`) runs a real Electron process against the repository's synthetic hubs. A small local gateway (`desktop/scripts/smoke-gateway.mjs`) serves the built web client the way the hub does: its files, `index.html` for page routes, `/api/` and the WebSocket forwarded to the hub. Every service is on a port the script owns and is stopped by it. The hubs refuse every request without a credential; the app signs in with a temporary pairing code made for the run, through the same `persona://pair` path a person's pairing link takes. It needs `web/dist` (built when missing, which needs Go) and ends with one verdict line; `result.json` holds each scenario's steps, and each scenario's directory its screenshots, per-step details and Electron log.

Four scenarios run, each in its own profile:

- **Origin root**, against a hub that lists `desktop_unregister`: the hub's sign-in page, sign-in, then the main chat (light and dark), the Agents list, an Agent session with a waiting card, Inbox and To-dos, each captured once the hub's data is in the page's DOM. With the window closed to the tray, a new Needs-you item from the hub must produce a notification request with the session's title, tag and route, the badge the web's own count, and a click must show the window on that session. Locking withdraws the desktop registration with `unregister_desktop`; unlocking registers again.
- **Reverse-proxy prefix**, against an older hub: the gateway answers only below the prefix and rewrites `<base>` as the hub does, and any request of the page outside the prefix fails the run. Sign-in, the chat and the Agents list load; an in-hub link meant for a new window stays in the window; an off-hub link, an off-hub navigation and the same origin outside the prefix are refused and handed to the system browser. Locking withdraws the registration by closing the socket.
- **Hub change**, against two independent hub origins, plus **same-origin prefix to parent**, against a prefixed hub and its origin-root parent: the signed-in local window submits Continue for the second hub; the old window is destroyed before the new one loads, its desktop registration is withdrawn, and the new origin-isolated session signs in. Notification permission, presence, a native notification request and both public login-item methods cross the production preload and IPC on the new hub. Closing the original local window in the root scenario also leaves those login-item methods working.

The driver, `desktop/scripts/e2e-main.mjs`, is Electron's entry point for the run and then loads the unmodified production main module. It is not part of `dist/`, so it is never packaged; it refuses to run packaged or without its switch, gives the hub renderer nothing and adds no IPC channel. It only watches or replaces the outer edge of the main process:

| Observed or replaced | What the smoke therefore proves | What it does not |
| --- | --- | --- |
| `Notification.prototype.show` / `close` record the call; a click is the notification's own `click` event | The web's request crosses the production preload and IPC, passes route validation and reaches the system call with the right text; the click handler shows the window and routes | That the OS notification center displays, groups or delivers a click, and its permission prompt |
| `Notification.isSupported` and the idle time answer "supported" and "just used" | Presence logic from those inputs to `register_desktop` / withdrawal on the authenticated socket | Real idle detection and a runner's own notification service |
| `lock-screen` / `unlock-screen` are emitted on the power monitor | The shell's reaction to the events | That the OS emits them when a person locks the computer |
| `shell.openExternal` records the address | The decision for each link | A browser opening |
| `desktop:badge` is read beside the real handler; on macOS the Dock's count is read back | The count sent equals the web's | The Windows taskbar overlay and the Dock's drawing |

On a Linux machine without a display server the smoke uses Chromium's headless platform and leaves out the tray's menu, which GTK cannot build there; Linux is not a shipping platform. The smoke runs the development build, not an installed package: it does not prove installation, signing, notarization, the tray's own interactions, launch at login or an update from a published release. Generated output is cleaned before compilation so deleted tests cannot linger, and a scenario's directory is emptied before it runs. Linux/macOS development evidence does not substitute for Windows acceptance or a published-release update test.

`npm --prefix desktop run smoke` remains the quick check of the first window against a hub already running at `PERSONA_HUB_URL`.

## Updates

**The default, in one sentence:** Persona checks for a new version when it opens and every four hours, downloads it in the background, and installs it only when you quit or choose **Restart to update**. Nothing restarts the app while you are working, and there is no setting to configure.

### What people see

The update state lives in the tray menu, between **Open Persona** and **Quit Persona**. There is no web UI for it and no bridge method.

| State | Tray entry | Enabled |
| --- | --- | --- |
| Nothing to report | Check for updates | yes |
| A check is running | Checking for updates… | no (status line) |
| A new version is downloading | Downloading version 1.2.0… | no (status line) |
| Downloaded, waiting for a restart | Restart to update to 1.2.0 | yes |
| After a check you asked for found nothing | "Persona is up to date", then Check for updates | status line, then yes |
| After a check you asked for failed | "Couldn't check for updates. Persona will try again later.", then Check for updates | status line, then yes |

- **Check for updates** changes to "Checking for updates…" at once, before any network work. Asking again while a check runs does not start a second one.
- Only a check the person asked for leaves a result line. A scheduled check that finds nothing, or fails, changes nothing visible; the next scheduled check retries. A failure never blocks or covers the web UI.
- An update the platform has accepted stays offered until the app restarts. On macOS that is Squirrel.Mac's own acceptance, not electron-updater's earlier file-downloaded event; a refusal returns to the retry schedule instead of offering a restart that cannot work. Quitting installs it silently; **Restart to update** installs it and reopens Persona. No further checks run in that session.
- The schedule is fixed (launch, then every four hours, no random delay), so it is the same on every run and unit-tested with a fake clock (`desktop/test/update.test.ts`).
- Development runs and Linux have no update feed, so the tray shows no update entries there.
- The feed is the one written into the package at build time (`app-update.yml`: this repository's GitHub Releases). No environment variable, setting or hub page can point a packaged app at another feed; `PERSONA_DESKTOP_DISABLE_UPDATE=1` only turns checks off. A test pins that the electron-updater calls which would override the feed are never used.
- Tray labels and update strings come from `desktop/src/locales` in English, Simplified Chinese and Japanese.

### How a release publishes desktop packages

A stable `v<major>.<minor>.<patch>` tag runs `.github/workflows/release.yml` (`docs/operations/release.md`, "Desktop packages on version tags"). Behind the same administrator-only gate as the hub image, it:

1. decides per platform whether every signing secret below is configured (`scripts/ci/desktop-release-gate.sh`; only whether a secret exists reaches the script, never its value);
2. builds on native runners with `scripts/ci/desktop-package.sh`: the universal macOS DMG and ZIP, signed and notarized, and the Windows NSIS installer, signed. The package version is the tag without its `v`; the app ID comes from the `PERSONA_DESKTOP_APP_ID` secret and is never written in the repository;
3. verifies the result (`codesign` and Gatekeeper's `spctl` on macOS, `Get-AuthenticodeSignature` on Windows);
4. attaches the packages and their block maps to the GitHub Release the workflow already created, and only then the update metadata (`latest-mac.yml`, `latest.yml`), so an installed app never reads metadata naming a package that is not there yet.

**A release never ships an unsigned package as if it were final.** When a platform's secrets are missing, that platform is skipped, the run's summary names the missing secrets, and the hub image and GitHub Release are published as before. In release mode the package script refuses to build without a certificate or with the placeholder app ID instead of falling back to the unsigned development build. Prerelease tags (`v1.3.0-rc1`) publish no desktop packages: installed apps follow the latest stable Release only.

Between the moment the Release is created and the moment its desktop metadata is attached, the newest Release has no update metadata. A check in that window fails quietly and the next scheduled one succeeds.

### What the owner must provide

No certificate exists yet, so no release has published desktop packages. Before adding credentials, the owner must create the `persona-desktop-release` GitHub Environment with required reviewer approval restricted to the administrator, and configure these as **Environment secrets**, not repository secrets. The environment restricts their use to the release jobs on stable version tags. The development Desktop workflow never receives them. None is ever committed, and nobody should look for them in a key store.

| Secret | What it is |
| --- | --- |
| `PERSONA_DESKTOP_APP_ID` | The app's registered identifier (reverse-DNS, the macOS bundle ID). It must stay the same for every release: macOS ties the signature and Windows ties the installed copy to it. |
| `MAC_CSC_LINK` | Base64 of a `.p12` export holding a **Developer ID Application** certificate with its private key. Apple Development, Apple Distribution (App Store) and Developer ID Installer certificates do not work. |
| `MAC_CSC_KEY_PASSWORD` | The password chosen when exporting that `.p12`. |
| `APPLE_API_KEY` | The text of an App Store Connect API key file (`AuthKey_<key id>.p8`), used for notarization. The build writes it to a private temporary file and removes it afterwards. |
| `APPLE_API_KEY_ID` | That key's ID. |
| `APPLE_API_ISSUER` | The issuer ID shown on the App Store Connect API keys page. |
| `WIN_CSC_LINK` | Base64 of a `.pfx` holding an Authenticode code-signing certificate (OV or EV) with its private key. |
| `WIN_CSC_KEY_PASSWORD` | The password of that `.pfx`. |

Two things to know before buying anything:

- **macOS updates need the Developer ID signature, not just the download.** The macOS updater (Squirrel.Mac) installs a new version only into a Developer ID signed app, and only when the new version is signed by the same team. An unsigned or ad-hoc build finds and downloads the update and then cannot install it; the update test below records exactly that. electron-updater's own "downloaded" event fires before Squirrel.Mac has judged the file, so it is not evidence of an installable update on macOS.
- **Windows certificates issued since mid-2023 usually cannot be exported.** Certificate authorities now keep the private key on a hardware token or in a cloud HSM, so there may be no `.pfx` to put in `WIN_CSC_LINK`. Signing with such a certificate needs a cloud-signing step in place of the `.pfx` (for example the authority's signing service). Which service to use is the owner's decision and is not implemented. An unsigned Windows package does update (the updater checks the download's SHA-512 against the metadata), but Windows SmartScreen warns on install and the updater cannot check the publisher.

### The update test

`node desktop/scripts/update-test.mjs` performs a real update with no certificate and no published release. It builds a test-only package (`desktop/electron-builder.update-test.yml`) twice, as version 0.9.0 and 0.9.1, with a local feed address written into the package the same way a release's feed is; serves the new build's directory on a free loopback port; runs the **packaged** old version with a saved hub address and an isolated profile; and checks what it does. The test-only entry (`desktop/update-test/main.mjs`) records the updater's events and quits once the download is ready, then loads the real main process unchanged. It is listed only in the test configuration, and the test build has its own name so its install directory and profile never touch a real install. Each run uses one random test-only identity for its package name, app ID and explicit NSIS GUID, stable between that run's two versions; electron-builder writes the derived updater cache name into `app-update.yml`, and the harness reads it back. The two exact per-run NSIS registry keys must not exist before installation and are checked after cleanup; the disposable package creates no desktop or Start-menu shortcuts. Its entry records `dirname(app.getPath("exe"))` as the running executable directory, compares it with the harness-owned installation directory passed only through the run-scoped `PERSONA_UPDATE_TEST_EXECUTABLE_DIR` environment value, and keeps electron-updater's optional `installDirectory` as `updaterInstallDirectory` for diagnostics. Locked electron-updater does not assign that optional property; it is not evidence of NSIS's destination. The harness never assigns the expected directory to the updater. NSIS chooses its destination through the run GUID's `HKCU\\Software\\<run GUID>\\InstallLocation` and optional `/D`; after the reopened new version and current-feed checks, the harness requires the exact per-run `InstallLocation` to match its actual destination. The instrumentation forwards the updater's complete argument list unchanged. The per-run macOS bundle ID likewise keeps Squirrel's cache and launchd job separate; the job must be absent before launch and is individually removed after the run, with a failure if it remains registered. All platform cache bases, that run's Squirrel cache and the updater-launched AppImage's default profile are under its temporary HOME, with `CFFIXED_USER_HOME` set for macOS processes. Cleanup is restricted to those owned paths and native keys/jobs and its failures make the run fail separately from the update result. A failed final event snapshot is itself a receipt failure, but cannot skip cleanup or report writing. A run deadline rejects into the same cleanup/report path rather than exiting directly; cleanup has its own bounded deadline. A result-report write failure returns a controlled nonzero verdict. `desktop/scripts/update-test-cache.test.mjs` checks the identity derivation and preservation of the unsigned development cache, a concurrent run's cache/ShipIt/profile and unrelated HOME content using only temporary sentinels; it does not exercise a real HOME, registry, launchd job or install.

The exported helpers in `desktop/scripts/update-test-receipts.mjs` read `app-update.yml` from `win-unpacked/resources`, `mac/Persona Update Test.app/Contents/Resources` on x64, `mac-arm64/Persona Update Test.app/Contents/Resources` on arm64, or `linux-unpacked/resources`. On macOS there is no additional `resources` component below `Contents/Resources`. Focused tests use new temporary files to check those exact paths for both macOS architectures and reject missing configs and misplaced decoys. Event tests use the production watcher against real asynchronous `update-test.jsonl` writes: a delayed matching event, a missing event until the deadline, an event from the wrong executable directory, and a matching append after early nonmatching events. The watcher awaits the file read and event before checking the receipt, propagates read/malformed-record failures, and retries an unfinished final line. `desktop/scripts/update-test-native.mjs` keeps native command access behind the existing injected runner; bounded doubles cover exact GUID queries, `InstallLocation` parsing, explicit not-found output, access-denied failures, per-key cleanup and the per-run ShipIt job. None of these unit tests invokes `reg`, PowerShell, `launchctl`, an installer or an installed app, and none assigns the expected directory to the updater.

| System | What is checked |
| --- | --- |
| Windows (hosted runner) | Silent per-user NSIS install of the old version into a temporary directory; the app finds the new version, downloads it, and the file's SHA-512 equals the metadata's; the awaited NSIS event's actual executable directory must be the owned destination; quitting installs it; the reopened app reports the new version and finds nothing newer; the exact run-GUID `InstallLocation` must match; then uninstall. |
| macOS (hosted runner) | The old version runs from its ZIP with an ad-hoc signature (no certificate). It finds the new version and downloads the ZIP, whose SHA-512 equals the metadata's. The test then follows Squirrel.Mac's own verdict rather than the download event: on the hosted runner Squirrel.Mac refuses the file ("did not pass validation: code failed to satisfy specified code requirement(s)"), and the test reports that refusal instead of an install. Had it accepted, the test would require the install and the reopened version as on Windows. |
| Linux (AppImage) | The same as Windows through an AppImage, under Xvfb. The product ships no Linux package; this exercises the shared feed, differential download and checksum code where no macOS or Windows computer is available. |

The Desktop workflow runs it on all three systems (`update` job in `.github/workflows/desktop-test.yml`) and uploads `result.json` and the app's log.

Not proven by this test: a signed build updating from a published GitHub Release, the macOS install, notarization, and the tray entries as drawn on screen (their text and order are unit-tested; no screenshot of a native menu is taken).

# Acceptance evidence

The complete acceptance checklist for the desktop migration is in
[#6770](https://github.com/nwverse/persona/issues/6770). This section is its maintained evidence record: it names what was
checked, where the result can be reproduced, and what remains unverified. "Implemented" is not the same as "accepted". The
issue remains open until its installation, signed-release, physical-device and first-run requirements have actual evidence.

Snapshot: 2026-11-14, against `origin/main` at `b5561c9115`. The merged PRs below are the source of the implementation and
its original verification claims. Their hosted runs were read back by head SHA, and their smoke/update artifacts were read
directly during this audit. Current local commands reproduce the static checks; they do not manufacture native-device,
screenshot or release evidence.

## What each kind of evidence proves

- **A hosted macOS or Windows runner is not a physical Mac or Windows PC.** In particular, no person has installed the
  desktop on a physical Windows machine or checked its taskbar overlay, notification center, lock behavior or tray.
- **The smoke screenshots prove rendered development builds only.** The driver starts the production main module, not an
  installed package. The 2026-11-14 audit inspected the macOS/Windows light and dark chat, sign-in, Agents, waiting card,
  Inbox, To-dos and notification-click captures. These screenshots do not prove first-run setup from an asar, OS-drawn
  notifications, a taskbar overlay, the native tray menu or a signed installation.
- **Notification, click and lock coverage stops at the main-process boundary.** The test replaces notification display,
  idle/support answers and power-monitor events; it asserts the production web/preload/IPC request, route and socket
  behavior. It does not observe an OS notification click or an actual screen lock. The macOS Dock count is read back;
  Windows verifies the count passed to the shell, not the taskbar's drawing.
- **The update test uses a local feed and test-only packages.** Windows installed 0.9.0, verified the 0.9.1 download's
  SHA-512, installed on quit and reopened as 0.9.1. macOS found and downloaded a checksummed 0.9.1 ZIP, but Squirrel.Mac
  **refused the ad-hoc-signed build; it was not installed**. Neither is a signed update from a published GitHub Release.
- **Release tests are static and synthetic.** They check workflow structure, authorization, Environment/credential
  boundaries, gate decisions and refusal to silently produce unsigned release packages. They do not access credentials,
  prove that an Environment is configured, run `codesign`, `spctl` or Authenticode against a release, notarize an app or
  publish an update. No desktop release job has run on a real version tag.

## Acceptance criteria

| Criterion | Merged implementation and evidence | Reproduce / inspect | Unresolved limits and verdict |
| --- | --- | --- | --- |
| Native `PersonaMac`, `apple/Mac/` and `apple/PersonaMacTests/` are gone; iOS, watchOS and visionOS still pass | [#7228](https://github.com/nwverse/persona/pull/7228), squash `dce8ed2f`: target/source removal; Xcode 27.1 generic iOS and watchOS builds, iOS test build and shared package tests | Inspect targets with `grep -n PersonaMac apple/project.yml` (no hits); reproduce supported Apple checks with `scripts/ci/apple.sh ios` on the repository's supported Mac/Xcode setup | **Not accepted.** visionOS reproduced existing main failures (`VNCScreenView.swift` / `WindowTextSize`); required iOS 26.0.x simulator tests were not executed and Xcode 26.0.x was not verified. Removal is merged; the platform pass criterion is not met |
| Installed macOS/Windows apps sign in to root/sub-path hubs and run chat, Agents, Inbox, to-dos and approval cards, with screenshots on both | [#7210](https://github.com/nwverse/persona/pull/7210), squash `ee4fabff`; [#7283](https://github.com/nwverse/persona/pull/7283), squash `30d5fb65`; [#7406](https://github.com/nwverse/persona/pull/7406), squash `0bd77c6b`; [#7464](https://github.com/nwverse/persona/pull/7464), squash `14f8fd8d`. Exact-head run [43567591329](https://github.com/nwverse/persona/actions/runs/43567591329) (`c2a4d4b57b`) passed both platforms, root 14/14 and prefix 12/12 | `scripts/ci/desktop-smoke.sh`; `gh run download 43567591329 -R nwverse/persona -n persona-desktop-macos-smoke -D /tmp/persona-desktop-macos-smoke`; repeat with `persona-desktop-windows-smoke` and a fresh output directory, then open `result.json` and the PNGs | **Not accepted.** Hosted screenshots are development builds against a synthetic hub. A waiting question is rendered, not answered/approved. The packaged clean-profile setup failure below blocks installation acceptance. No physical Windows or signed installation |
| Closed-window Needs-you notification and Dock/taskbar badge; click opens the session | [#7162](https://github.com/nwverse/persona/pull/7162), squash `d916689d`; [#7210](https://github.com/nwverse/persona/pull/7210); [#7464](https://github.com/nwverse/persona/pull/7464). Run 43567591329 observed notification title/tag/route, badge 2→3 matching web, macOS Dock count 3 and the simulated click route | Same smoke/artifact command. `desktop/test/notifications.test.ts` checks prefixed routes; `web/src/lib/desktop-attention.test.tsx` checks the web/native boundary | **Not accepted.** Main-process calls and synthetic events only; no OS notification display/click, real Windows overlay or real tray interaction. macOS reads Dock's count, not its pixels |
| `register_desktop` is sent and Phone Muted works while active | [#7162](https://github.com/nwverse/persona/pull/7162) plus [#7464](https://github.com/nwverse/persona/pull/7464). Both runners registered on the authenticated socket; synthetic lock withdrew via `unregister_desktop` (root) or socket close (older hub) and unlock registered again | `scripts/ci/desktop-smoke.sh`; `(cd server && go test ./internal/hub -run 'Desktop|MuteWhileDesktop')`; `desktop/test/presence.test.ts` | **Not accepted.** `server/internal/hub/desktops_test.go` tests the suppression policy and withdrawal; no physical phone received/suppressed a real push, and no real idle/lock occurred |
| Local Link pairing/status/management is in the app; old Mac menu-bar and Windows tray UIs are removed | [#7209](https://github.com/nwverse/persona/pull/7209), squash `0d3d25a8`; [#7210](https://github.com/nwverse/persona/pull/7210); [#7276](https://github.com/nwverse/persona/pull/7276), squash `3f4e8bb9`; [#7282](https://github.com/nwverse/persona/pull/7282), squash `c01e7992`. Fixed CLI/status boundary and independent macOS universal Link package; Windows service retained without the old tray UI | `npm --prefix desktop test`; `(cd server && go test ./cmd/persona-link)`. The only `server/internal/linktray/host.go` left is the non-UI `PairError` type | **Not accepted.** Unit tests and unsigned/ad-hoc packages, not a live installed Link pairing, reconnect, service operation or Screen Recording permission change. No physical Windows scheduled-task acceptance |
| CI builds signed macOS and Windows packages | [#7442](https://github.com/nwverse/persona/pull/7442), squash `6af756ba`: protected release jobs, native packaging and static signature-verification steps. [#7406](https://github.com/nwverse/persona/pull/7406) and [#7442](https://github.com/nwverse/persona/pull/7442) built **unsigned development** macOS/Windows packages | `python3 scripts/ci/test_desktop_release.py`, `python3 scripts/ci/test_ci.py`, `python3 scripts/ci/test_release_authorization.py`. Inspect exact-head update evidence below, not as signing evidence | **Not accepted.** No certificate, signed package, notarization or version-tag desktop release run. The Environment-binding correction in open [#7521](https://github.com/nwverse/persona/pull/7521) is still pending supervisor review and is not on this branch's base |
| Rules/docs describe Electron around the existing web app and independent Link | [#7210](https://github.com/nwverse/persona/pull/7210), [#7276](https://github.com/nwverse/persona/pull/7276), [#7282](https://github.com/nwverse/persona/pull/7282) and [#7442](https://github.com/nwverse/persona/pull/7442) | `python3 scripts/test/test_doc_paths.py`, `test_doc_go_paths.py`, `test_doc_sections.py`, `test_doc_wire.py`; review `CLAUDE.md`, `docs/desktop/desktop.md`, `docs/web/web.md`, `docs/operations/link.md`, `docs/operations/windows.md` and both READMEs | **Static criterion verified; not a substitute for the other acceptance gates.** The old prohibition is replaced with the one-web-UI boundary. Documentation does not prove product behavior |

## Exact-head artifact readback

All three final hosted runs matched the PR head recorded in the merged PR, not an earlier branch revision. They prove their
own steps only; the squash commits are [#7406](https://github.com/nwverse/persona/pull/7406) `0bd77c6b`,
[#7464](https://github.com/nwverse/persona/pull/7464) `14f8fd8d` and [#7442](https://github.com/nwverse/persona/pull/7442)
`6af756ba`.

| Run | PR / exact head | Readback |
| --- | --- | --- |
| [43561725548](https://github.com/nwverse/persona/actions/runs/43561725548) | #7406 / `bd45966510` | Linux, macOS and Windows jobs succeeded; unsigned native packages and initial sign-in smoke. Not a signed or installed release |
| [43567591329](https://github.com/nwverse/persona/actions/runs/43567591329) | #7464 / `c2a4d4b57b` | macOS and Windows: root 14/14, prefix 12/12, 12 screenshots each. Inspected directly for this audit as described above |
| [43569276735](https://github.com/nwverse/persona/actions/runs/43569276735) | #7442 / `4420bc534c` | All six jobs succeeded. Windows update result: 16/16 steps, SHA-512 match, the installed `ProductVersion` in its `result.json` is the 0.9.1 package's four-part form, and the reopened app is 0.9.1. macOS: checksum match followed by explicit Squirrel.Mac signature refusal and `not installed` |

For the update evidence, use a fresh empty directory per artifact and read the files without executing them:

```sh
mkdir -p /tmp/persona-desktop-update-evidence
gh run download 43569276735 -R nwverse/persona -n persona-desktop-update-test-Windows -D /tmp/persona-desktop-update-evidence
# Read /tmp/persona-desktop-update-evidence/result.json and app.log.
# Use a different new directory for persona-desktop-update-test-macOS.
```

To produce new local development-build evidence, run `scripts/ci/desktop-smoke.sh` after `scripts/ci/desktop.sh`; on a
headless Linux machine use an owned Xvfb display, for example `xvfb-run -a scripts/ci/desktop-smoke.sh`. On a native
macOS/Windows machine `node desktop/scripts/update-test.mjs <new result directory>` produces the same local-feed test as
CI. A Linux AppImage update is useful code-path evidence but Linux is not a shipping desktop platform.

## Open first-run blocker and ownership

The original child-agent report in [#7442](https://github.com/nwverse/persona/pull/7442) reproduced a packaged clean-profile
`setup.html` / asar failure with `grantFileProtocolExtraPrivileges: false` (`ERR_FILE_NOT_FOUND`). The [other leader's
claim](https://github.com/nwverse/persona/issues/6770#issuecomment-6873777179) owns reproducing and repairing that path.
This audit preserves that old report; it neither re-runs it nor claims that #7442 fixed it. A preconfigured-hub update test
passing is not first-install acceptance. Do not relax fuses or the existing origin/IPC boundaries to make this evidence look
green.

## Owner actions and decisions still required

1. **Protected release configuration:** approve and configure `persona-desktop-release` with administrator-only required
   reviewers, then supply the Environment secrets listed above. Choose a Developer ID Application certificate and Apple
   notarization API key for macOS; choose Authenticode signing for Windows. Modern non-exportable Windows certificates need
   a cloud-signing design decision, not an insecure export workaround. No certificate is available to this repository's
   static tests.
2. **Pending static gate correction:** [#7521](https://github.com/nwverse/persona/pull/7521) binds the gate to that
   Environment. Until it merges, the documented environment-only configuration would make the gate report every secret
   absent and skip both platforms. This was found statically, not observed as a production signing failure. The follow-up
   tests here enforce the same documented Environment for all secret-reading release jobs; they do not duplicate its binding.
3. **Physical platform acceptance:** on an actual Windows PC and a Mac, install the intended packages, exercise first run,
   Link pairing/reconnect, real notifications/clicks, badge/overlay, lock/unlock and Phone Muted, launch at login and the
   tray. Capture platform-labelled screenshots and distinguish an installed package from a development run.
4. **Authorized release:** only the administrator can authorize a real version-tag release. After that actual signed and
   notarized release, install an older signed version on both platforms and verify a real published update through restart,
   not merely a local-feed download. `scripts/ci/test_desktop_release.py` passing never substitutes for this step.

## Small CI selection follow-up

`scripts/ci/paths.py` selects the desktop lane for `desktop/**`, `web/**`, the synthetic hub and its stream helper
and desktop test, `spec/desktop-bridge.d.ts`, the desktop CI scripts and its workflow. The web build also compiles the
sign-in WebAssembly, so `server/cmd/password-wasm/**`, `server/internal/passwordpake/**` and `server/go.mod`/`go.sum`
are inputs too. These are the inputs the lane builds, loads or compiles. A regression in `scripts/ci/test_ci.py` checks each one. This does not change the default full-CI run or
explicit `desktop-test.yml` dispatch.

## Independent-review provenance

Review evidence is separate from test evidence. Only #7442 had the two completed pre-merge independent reviews described
in its PR: workflow `wf_b0758023-e5f`, reviewers `review:update:0` (`ab2093d409085743b`, security/privilege boundaries) and
`review:update:1` (`a1ddbee1a621ded96`, correctness/claims), covering the updater/release diff and leading to its macOS
readiness, signing-boundary, cancellation and test-isolation fixes. Their reviews did not cover the later Environment gate
finding. **#7406 and #7464 have no completed independent-review evidence.** Their implementation/local/hosted results remain
useful but must not be described as independent approval, and #7442's reviews do not cover code outside its diff. The
[issue correction](https://github.com/nwverse/persona/issues/6770#issuecomment-6873821152) is the authoritative provenance
record; this audit does not backdate a new review or imply a prior one.

## Migration boundaries

The retired `PersonaMac` target and native-Mac source directory are removed separately, with iOS, watchOS, visionOS and shared package checks. The old Mac menu bar UI and Windows `linktray` are removed too: independent Link installation, pairing/reconnect/status/service controls and Persona Screen packaging now use the CLI and fixed desktop setup bridge. The CLI/service works without the desktop app; Mac uses launchd `com.persona.link`, Windows retains its scheduled task.

Remaining platform acceptance, signed/notarized artifacts, Windows installation and a published auto-update belong to the migration issue (#6770), not to a claim that the shell or independent Link packaging completes the migration.
