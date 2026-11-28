# TestFlight releases

TestFlight internal testing (testers in the configured `app_store.testers`), plus external testing joined through a public link (see "External testing" at the end).

## Accounts and identifiers

The product is Persona (previously under another product name), but the existing Bundle IDs, App Group, keychain groups, signing profile names and App Store Connect app record stay the same; don't recreate the App because of the rename.
Since 2026-11-03 the store name in App Store Connect is **Persona Side** (formerly "Persona Herd"; the home screen still shows `CFBundleDisplayName`, Persona), and the privacy policy URL is `app_store.external.privacy_url` (the website's privacy policy page). The store name lives in the App Info localizations (`name` in `appInfoLocalizations`) and can be changed directly via the API while the version hasn't been submitted for review; see "Changing the store name" below. "Persona App Store" and "Persona Watch App Store" below are existing signing profile names and stay as they are; the generated project / targets and archive / `.app` products use the Persona name, and old project / scheme aliases and module identity are in [compatibility migration](rename.md). Historical build records keep the names of their time. New default config and `personad` / `personad` compatibility are in [naming and configuration](config.md#naming-and-configuration).

All in the build config (`docs/operations/config.md`): `app.bundle_id` / `app.team_id`, `app_store.key_id` / `issuer_id` / `key_path` (the API key file lives only
outside the repo, e.g. `~/.appstoreconnect/`), `app_store.app_id` (the app record's numeric ID), `app_store.testers`, `app_store.feedback_email`.

| Item | Notes |
| --- | --- |
| Bundle ID | iOS `<bundle_id>`, watch `<bundle_id>.watchkitapp`, Live Activity `<bundle_id>.liveactivity`, notification service extension `<bundle_id>.notify` (push relay, `docs/operations/push-relay.md`), share extension `<bundle_id>.share` (#4586); new watch widget `app.watch_widget_bundle_id` (default `<bundle_id>.watchkitapp.widgets`, #1147); with `app.siri_calls` only, the SiriKit extension `<bundle_id>.callintents` ("Siri calls" below). `scripts/release/asc_signing.py` covers these targets; #1147 did not register the new resources |
| App ID capabilities | iOS: `PUSH_NOTIFICATIONS`, `USERNOTIFICATIONS_TIMESENSITIVE` (time-sensitive notifications, #1572), `HEALTHKIT`, `APP_GROUPS`, and `SIRIKIT` with `app.siri_calls` only; Live Activity: `APP_GROUPS`; watch app and watch widget: `APP_GROUPS` using the separate configured watch group. The App Group itself can only be created on the website and attached to App IDs ("App Group" below) |
| Provisioning profiles | "Persona App Store", "Persona Watch App Store", "Persona Share App Store", etc. (`IOS_APP_STORE`; the share extension's was added 2026-11-06, so run `asc_signing.py` once before its first release to register its Bundle ID and profile); `asc_signing.py` deletes and recreates them and installs them into `~/Library/MobileDevice/Provisioning Profiles/` |
| Signing identity | `Apple Distribution: … (team_id)` in the login keychain |
| App Store Connect App ID | After creating the app record on the website, put it in the config's `app_store.app_id` |

The API key's role is App Manager, which can't use cloud-managed signing (`-allowProvisioningUpdates` cloud signing fails), so
archiving uses automatic signing, and at export `release.sh` generates ExportOptions from config (`scripts/persona_config.py export-options`) and re-signs with the two App Store profiles specified manually.

## 现有描述文件的名称（#6150）

产品名称不决定已经注册的描述文件名称。若签名预检因新默认名称报 `missing or invalid`，先在构建配置的 `app_store.provisioning_profiles` 中填写各 Bundle ID 对应的现有名称，再运行 `asc_signing.py --check`。签名预检与 ExportOptions 使用同一个映射；未配置的 target 仍使用 Persona 默认名称。

此配置只选择已有资源，不修改 Bundle ID、证书、capability 或描述文件。不要为名称不匹配直接运行不带 `--check` 的签名脚本：该脚本会删除并重建所选描述文件。若现有描述文件确实失效或缺少权限，预检仍会阻止发布，需要单独处理。

`--dev` 仍单独处理开发描述文件：名称中的 `App Store` 替换为 `Development`；自定义名不含 `App Store` 时追加 ` Development`，不复用或删除同名的发布描述文件。

## First-time setup (in order)

1. Fill in `~/.config/persona/config.json` (`app`, and the key part of `app_store`).
2. `python3 scripts/release/asc_signing.py`: registers the two Bundle IDs, turns on push, time-sensitive notifications and HealthKit for iOS, creates the two App Store profiles and installs them locally. Idempotent; rerun whenever capabilities change.
3. **Create the app record on the website** (the API can't): App Store Connect › Apps › New App: iOS, name, primary language, Bundle ID, SKU, full access.
   Put the App ID in the config's `app_store.app_id`.
4. `python3 scripts/release/asc_testflight.py setup`: creates the internal group `Internal` (`hasAccessToAllBuilds`) and adds the configured testers.
5. `NO_UPLOAD=1 ./scripts/release/release.sh` to verify archive + export (works without an app record).
6. `./scripts/release/release.sh` uploads the first build → `python3 scripts/release/asc_testflight.py wait` waits for processing and adds it to Internal automatically.

Without `app_store.app_id`, `scripts/release/asc_testflight.py` fails right away with a hint.

## Every release

```sh
./scripts/release/release.sh                      # build number = yyyymmddHHMM; archive, check, upload, tag testflight/<build number>
python3 scripts/release/asc_testflight.py status  # recent builds and their processing state
python3 scripts/release/asc_testflight.py info "What to Test for this build"
git push --tags
```

The build number can also be given by hand: `./scripts/release/release.sh 42`.

What `release.sh` does:

1. `xcodegen generate` (the source is `apple/project.yml`, generating `Persona.xcodeproj`; generated content isn't committed).
2. Release archive to `~/Library/Developer/Xcode/Archives/<date>/Persona <build number>.xcarchive` (dSYMs kept for Organizer symbolication),
   with the watch App embedded.
3. `check_bundle_strings.py` checks `Persona.app` and `Persona.app/Watch/Persona.app`: `*UsageDescription` in Info.plist
   must not be empty or equal to the key, and the same for `*.lproj/InfoPlist.strings` (placeholder text gets auto-rejected by App Review).
4. `debug_switches.py check` reads the Release binary's `strings` and fails when a line equals one of the simulator switches that exist only under `#if DEBUG`, such as `pairURL`, `openSession`, `showNewChat`,
   or when a line contains a name of the DEBUG runtime CLI (`runtimeCLI`, `RuntimeCLI`, the accessibility hooks it turns on; `docs/operations/runtime-cli.md`).
   It reads all of the output: under `set -o pipefail`, `if strings … | grep -q …` passed when the match came early, since `strings` dies of SIGPIPE once grep stops reading (#10654).
   The switches are listed one per line, sorted and without duplicates, in `scripts/release/debug_switches.txt`: a new switch is one line there, never an edit to `release.sh`.
   `.gitattributes` merges the file with `merge=union`, so two branches that each add a switch merge cleanly; when both added theirs in the same place the result can be out of order,
   which `scripts/release/test_debug_switches.py` reports and `python3 scripts/release/debug_switches.py sort` fixes. A union merge can also bring back a name another branch removed, so check union merges that delete names.
   Comments go in the file's header; the checker refuses a comment or a blank line below the first name. The list is kept by hand rather than read from the Swift sources:
   switches are read in many shapes (UserDefaults `forKey:` on the standard store or a local alias, arrays of keys, `ProcessInfo` arguments), DEBUG code also reads settings keys that do ship, and the list keeps retired switches.
   `check_bundle_privacy.py` checks that the whole bundle has no personal values (external testing is a public link, see the end).
5. `xcodebuild -exportArchive`: manual signing + `method=app-store-connect` + the two profile mappings. **This step is the App Store signing**:
   the archive uses automatically signed development profiles (so `aps-environment` in the archive is `development`), and export re-signs with the App Store profiles to
   `production`. With `NO_UPLOAD=1`, `destination` changes from `upload` to `export`, and it additionally checks that `aps-environment` in the exported ipa
   really is `production`.
6. `scripts/release/entitlements.py check`: in the archive (plus the exported ipa with `NO_UPLOAD=1`), every bundle carries its target's keychain groups, and the group name in Info.plist is among them; see "entitlements" below. It also fails when any binary in the app links CallKit; see "No CallKit" below.
7. `scripts/release/check_mac_imports.py` checks the archived iPhone executable's strong imports against MacOSX.sdk TBD exports, preferring each framework's `System/iOSSupport` copy. An iPhone app on an Apple silicon Mac must not bind a symbol the Mac lacks: dyld otherwise ends its launch before app code. Weak imports pass; libraries with neither a Mac nor an iOS-support stub are outside this comparison. Run it by hand with `python3 scripts/release/check_mac_imports.py <path to Persona.app>/Persona`; findings are printed, a clean check prints nothing.
8. `git tag -f testflight/<build number>`.

A build shows up in TestFlight about 10–30 minutes after archiving. `asc_testflight.py wait [minutes] [build number]` checks every 30 seconds until
`processingState=VALID`, then writes the default What to Test and adds it to the `Internal` group; `INVALID` / `FAILED` exits with an error
(the reason is on the build page in App Store Connect). **Pass the build number release.sh printed**: a just-uploaded build can't be found yet, and without the build number
"the latest build" is the previous one (long since VALID), so What to Test gets written to the old build (hit on 2026-10-25).

## Unattended default-branch releases

`scripts/operations/autorelease.py` is a standard-library worker for a user-level systemd timer, not an Agent-session task.
Install the worker in `~/.local/lib/persona-autorelease/`, its private JSON configuration in
`~/.local/state/persona-autorelease/config.json`, and the supplied `persona-autorelease.service` and
`persona-autorelease.timer` from `scripts/operations/` in `~/.config/systemd/user/`.
Enable linger for the user if the service must survive logout. The timer checks every ten minutes and coalesces intervening commits.

CI admission belongs to the PR's local verification: run every affected platform check and `scripts/privacy_check.py` before
merging. PRs start no hosted checks. A scheduler explicitly dispatches full CI on `main`, including iOS and Mac; Mac remains
independent of the `CI required` aggregate. Failures are fixed separately rather than being treated as local PR verification.
The unattended worker consumes the newest already-merged `origin/main`, then applies path selection and local build/release verification.
It does not wait for the scheduled main CI result before releasing; the two checks are separate operational gates.

Each target has its own last-success revision, path patterns, command argument vector, and timeout. Worker events are appended to
`worker.log` in the private state directory; `log_output: true` additionally retains command output in mode-0600 per-step files
under its `logs/` subdirectory. Do not enable shell tracing or print credentials from action commands. Recommended triggers:

| Target | Paths |
| --- | --- |
| Hub | `server/**`, `integrations/**`, `sdk/go/**`, `schema/**`, `spec/**`, `scripts/deploy/**`, `scripts/operations/**`, `scripts/persona_config.py`, `scripts/persona_identity.py` |
| Web | `web/**`, `schema/extension/**`, `spec/fixtures/injected/**`, `scripts/deploy/**`, `scripts/operations/**`, `scripts/persona_config.py`, `scripts/persona_identity.py` |
| TestFlight | `apple/**`, `schema/**`, `spec/**`, `scripts/release/**`, `scripts/i18n/**`, `scripts/operations/**`, `scripts/persona_config.py`, `scripts/persona_identity.py`, `scripts/privacy_check.py`, `scripts/app/sim-boot.sh` |

A missing baseline runs that target once. Later checks compare the last successful commit directly with the newest branch HEAD,
including deleted paths. Failure does not advance that baseline; two failures at the same HEAD suspend that target until another
HEAD appears. Lock contention exits 75 and is a deferral, not a failure. The deploy script's explicit checkout-behind-origin
message with exit 2 also defers the target without notification or a failure-budget charge; the next round fetches the newest HEAD.
Other exit-2 errors still count as failures, and the freshness guard is never bypassed.

Failures invoke the configured notification command once per target and consecutive failure cause, even across new commits.
The worker hashes the step/result and first diagnostic line (or first nonempty output line), normalizing revision identifiers and
timestamps; raw summaries never enter notification payloads or state. Each target's successful action clears its remembered failure, so the
same cause notifies again if it recurs after recovery. Successful actions only log, including recovery; there is no extra recovery
push. Preflight failures have a separate worker identity, cleared when preflight completes. Lock contention and stale checkouts
neither clear incidents nor create them. Failed notification deliveries remain in the durable at-least-once outbox, retried without
queuing another copy of a continuing failure. There is no periodic reminder for an unchanged cause.

For a worker upgrade, pause future timer runs and let any active service finish, copy the verified main worker to its installed
path with mode 0600, and resume the same timer. Keep configuration, targets, retry state, checkpoints, adapters and unit files intact;
record the source revision and installed file hash in the private STATE document.

Use a dedicated detached clean worktree on the hub and another on the build computer. The worker refuses to discard dirty files.
`scripts/operations/hub_action.py deploy` retains the deployment lock through a live API and serving-binary verification;
`testflight_action.py` retains the release lock through upload, `asc_testflight.py wait 45 <build>`, and Internal-group verification.
Its checkpoint resumes processing a previously uploaded build instead of uploading it again. Set `NO_PUBLISH=1` for TestFlight-only
work. Do not disable the deploy script's freshness check.

Manual `deploy.sh` now takes the hub's existing `releases/hub/.publish.lock`, shared with release promotion; callers on another
computer retain it through an SSH connection. Manual `release.sh` takes the build computer's
`~/.local/state/persona/locks/release.lock`. Older checkouts that predate these locks must be updated before manual use.
The release script runs under `PERSONA_XCODE_RUNNER` (default `~/.local/bin/xcode-slot`) when installed, covering every xcodebuild
subprocess, not just archive. The unattended release uses no simulator.

Failure notifications use hub-local `POST /api/v1/notify`, reading the runtime token inside the process. No credentials enter
arguments or logs. `sent:true` is hub acceptance, not a phone-display receipt; verify a controlled failure on the phone during installation.

```sh
systemctl --user daemon-reload
systemctl --user enable --now persona-autorelease.timer
systemctl --user list-timers persona-autorelease.timer
systemctl --user start persona-autorelease.service             # one immediate run
systemctl --user disable --now persona-autorelease.timer       # pause future runs
systemctl --user stop persona-autorelease.service              # also stop an active run
systemctl --user enable --now persona-autorelease.timer        # resume
journalctl --user -u persona-autorelease.service -n 100 --no-pager
python3 ~/.local/lib/persona-autorelease/autorelease.py --config ~/.local/state/persona-autorelease/config.json --inject-failure
```

Keep a private STATE document alongside the configuration, recording the actual paths, notification receipt, last successful revisions,
and TestFlight build evidence. Runtime/build configuration and signing keys remain outside the repository.

## Releasing with asc

`scripts/release/asc_cli.py` runs the [App Store Connect CLI](https://github.com/rorkai/App-Store-Connect-CLI) (`asc`) with your own App Store Connect API key; it can do uploads, TestFlight, review submission, metadata and signing.
The key is the same config as the release scripts above, so configure it once and everything works. The script pins asc's version and sha256, downloads it to `~/.cache/persona/asc/<version>/` on first run, and refuses to install if the checksum fails. Telemetry is always off.

1. **Set up the key**: in App Store Connect › Users and Access › Integrations › App Store Connect API, create a team key (the App Manager role is enough to upload builds and submit for review), download `AuthKey_<Key ID>.p8`,
   and put it outside the repo (e.g. `~/.appstoreconnect/`, `chmod 600`). Put the Key ID, Issuer ID, file path and the app record's numeric ID into `~/.config/persona/config.json` as
   `app_store.key_id` / `issuer_id` / `key_path` / `app_id`. Environment variables `ASC_KEY_ID`, `ASC_ISSUER_ID`, `ASC_PRIVATE_KEY_PATH` (or `ASC_KEY_PATH`), `ASC_APP_ID` also work and take precedence.
   The repo's `.gitignore` ignores `*.p8` and `.asc/`.
2. **Check**: `python3 scripts/release/asc_cli.py check`. It only reads config locally and sends no requests: lists asc's version and location, the last four characters of the Key ID, the key file and its permissions, and the App ID; the last line is `ok`.
   Anything missing is reported with the environment variable name and config key. To confirm the key is valid on Apple's side, run `python3 scripts/release/asc_cli.py auth status --validate` (sends a request).
3. **Use**:
   ```sh
   UPLOADER=asc ./scripts/release/release.sh                         # archive, export, check the ipa, upload to TestFlight with asc
   python3 scripts/release/asc_testflight.py wait 45 <build number>           # then as before: into the Internal group, write What to Test, attach the external group
   python3 scripts/release/asc_cli.py review submit --version <version> --build-number <build number> --dry-run   # before submitting, see which steps it would take; changes nothing
   python3 scripts/release/asc_cli.py review submit --version <version> --build-number <build number> --confirm   # attach the uploaded build to the App Store version and submit for review
   python3 scripts/release/asc_cli.py <any asc command>                 # e.g. builds list --output table, review status, validate --version <version>
   ```
   `UPLOADER=asc` goes through the `NO_UPLOAD=1` export and ipa checks (`aps-environment` is `production`, entitlements), then uploads with `asc builds upload`; when unset, `xcodebuild` still uploads directly at export.
   asc's own help: `python3 scripts/release/asc_cli.py --help`, `… <command> --help` (no key needed).

**Customizing**: to change asc itself, fork upstream, run `go build -o ~/bin/asc .` in the fork (needs the Go version in `go.mod`), then `ASC_BIN=~/bin/asc python3 scripts/release/asc_cli.py …`; key and telemetry handling stay the same.
Persona's own release logic (test groups, What to Test, external testing) stays in scripts like `asc_testflight.py`, or is composed from asc commands in `asc_cli.py`.
**Upgrading asc**: change `VERSION` and `SHA256` in `asc_cli.py` (from `asc_<version>_checksums.txt` in the release), and run `python3 -B -m unittest discover -s scripts/release -p 'test_asc_cli.py'`.

asc only uses the key in Persona's config: `asc_cli.py` strips `ASC_PROFILE`, `ASC_CONFIG_PATH`, `ASC_BYPASS_KEYCHAIN`, `ASC_PRIVATE_KEY`, `ASC_PRIVATE_KEY_B64` at runtime,
so a profile saved to the keychain by `asc auth login` never gets mixed in. The App Store version (version number, screenshots, description) must be prepared first on the website or with `asc versions` / `asc metadata` before `review submit` can submit.

## Why sign this way

| Stage | Signing | Profile |
| --- | --- | --- |
| Archive `xcodebuild archive` | Automatic (`CODE_SIGN_STYLE=Automatic`) | The development `iOS Team Provisioning Profile` |
| Export `xcodebuild -exportArchive` | Manual (`signingStyle=manual`, `signingCertificate=Apple Distribution`) | "Persona App Store" / "Persona Watch App Store" |

An App Store Connect API key can only do what App Manager can, and cloud-managed signing needs Admin; manual signing gets around that.
Changing Bundle ID capabilities (say, adding another capability) invalidates the old profiles; just rerun `scripts/release/asc_signing.py`.

## entitlements (2026-11-02)

The App and its extensions hand things over through shared groups: the push relay's private key goes through a keychain group (`<team>.<bundle id>.push`, App → PersonaNotify), and the Dynamic Island avatar and avatar pack images go through the App Group (`group.<bundle id>`, App → PersonaWidgets, next section).
Every bundle's signature must have its target's `keychain-access-groups` and `com.apple.security.application-groups`, and the group the Info.plist tells the code to use (`PersonaPushKeychainGroup`, `PersonaAppGroup`) must be one of them; if anything is missing, it silently falls back to defaults.
The App Group ID itself has no team prefix, so the Info.plist writes `$(PERSONA_APP_GROUP)` directly, and it has a value even in an unsigned archive.

- **Info.plist group names use `$(DEVELOPMENT_TEAM).`, not `$(AppIdentifierPrefix)`**: the latter only has a value during Xcode signing and is empty in an unsigned archive. Entitlements files still use `$(AppIdentifierPrefix)`.
- **Unsigned archive (`CODE_SIGNING_ALLOWED=NO`, when this Mac has no distribution certificate and export uses cloud signing)**: bundles in the archive have no entitlements, and export signs according to what's in the bundle.
  Before export, `python3 scripts/release/entitlements.py sign <archive>` ad hoc signs inside-out, giving each bundle its target's entitlements (found via `xcodebuild -showBuildSettings`,
  with build variables expanded and `$(AppIdentifierPrefix)` set to the team's; if something can't be expanded it errors instead of leaving it empty). If you only run `codesign --entitlements <original file>` on the App and leave out the extensions,
  export expands the App's `$(PERSONA_BUNDLE_ID)` to empty and the extensions get no groups at all (that's what happened in build 92: the Dynamic Island avatar didn't follow the setting, `docs/apple/live-activity.md`).
- **Check after export**: `python3 scripts/release/entitlements.py check <ipa>` lists every problem and exits non-zero; it reports 9 issues on build 92's ipa.
  App Groups are checked the same way: every bundle's signature has its target's group, and the Info.plist's `PersonaAppGroup` is one of them.
  In the exported ipa / pkg, the push entitlement (`aps-environment`; on Mac `com.apple.developer.aps-environment`) must be `production`. A Mac .pkg can be checked too.
- **HealthKit background delivery** (`com.apple.developer.healthkit.background-delivery`, #2526): signed in only with config.json
  `app.health_background_delivery: true`, so that workout reports come right after a workout (`docs/assistant/health.md`,
  "Automatic workout reports (iOS)"). The App ID's HealthKit capability covers it. If the profiles predate it, rerun
  `asc_signing.py` before turning the switch on. `entitlements.py check` errors if an exported App built with the switch lacks
  it, because the Settings page would promise reports that wait for Persona to open. Check the first such build on a device:
  finish a workout on the Watch with Persona closed, and the report should arrive.
- **Time-sensitive notifications** (`com.apple.developer.usernotifications.time-sensitive`, #1572): the hub's "Needs you" pushes and plugins' urgent alerts carry `interruption-level: time-sensitive`.
  If the App's signature lacks this entitlement, the system shows them all as `active`: they don't break through Focus, so with Focus on the phone doesn't ring and the watch doesn't get them either, and nothing reports an error. The App ID must first enable `USERNOTIFICATIONS_TIMESENSITIVE`
  (`asc_signing.py` enables it; afterwards the old profiles are invalid and need one rerun to rebuild); `entitlements.py check` errors if the exported App lacks it.
  **After merging #1572 and before the next release, run `python3 scripts/release/asc_signing.py` once** (if you sign development builds, add `--dev` to rebuild the Development profiles).
  `release.sh` runs `asc_signing.py --check` at the start, so a missing capability or invalid profile stops it before archiving rather than failing at export.
  The Mac App also requests this entitlement (#1655, PersonaMac in `project.yml`): Mac and iPhone share one App ID (UNIVERSAL), so the capability enabled above covers the Mac too;
  `entitlements.py check` requires it for the Mac .pkg / archive as well (checked whenever the target requests it, `scripts/release/test_entitlements.py`).
  No .pkg has been exported and checked yet in an environment that can ship the Mac version, and no banner has been seen on a Mac with Focus on; close #1655 after that's done.
  The Apple Vision Pro version (PersonaVision) also requests it (#2435); its bundle id is the same as the iPhone's, so it's covered by the capability above too. No banner has been seen on Vision Pro with Focus on yet.

## App Group (2026-11-02)

The App and the Dynamic Island extension (PersonaWidgets) hand over the avatar and avatar pack images through files in the App Group container (`docs/apple/live-activity.md` ("Avatar"); the Live Activity extension can't read the keychain on a phone).
The group name defaults to `group.<bundle id>` and can be changed with the config's `app.app_group` (`docs/operations/config.md`); it's written into both targets' entitlements (`com.apple.security.application-groups`) and the Info.plist's `PersonaAppGroup`.
It was registered on the developer website on 2026-11-02 and attached to the App IDs of the App and the Dynamic Island extension; since then, the TestFlight flow (unsigned archive → `entitlements.py sign` → export with API key cloud signing) produces an ipa where both bundles carry the group.

**API keys can't register App Groups** (checked 2026-11-02):
- The App Store Connect API has no App Group endpoints (`GET /v1/appGroups` is 404). `bundleIdCapabilities` can only turn on the `APP_GROUPS` capability, not attach a specific group (settings only accepts three keys, see "Pitfalls" below).
  fastlane's `produce group` / `associate_group` use Apple ID sign-in (website cookies), not an API key.
- At export, API key + `-allowProvisioningUpdates` cloud signing regenerates profiles from the App ID's existing capabilities, but can't create or attach a group; if it isn't attached, export fails.
- So when switching to another account or bundle id, **finish on the website first, then build with App Group entitlements**.

Steps on the developer website (developer.apple.com › Account › Certificates, IDs & Profiles, as Account Holder or Admin):
1. Identifiers › "+" › choose **App Groups** › Continue › Description `Persona`, Identifier `group.<bundle id>` (matching the config's `app.app_group`) › Continue › Register.
2. Identifiers › switch the top-right filter back to App IDs › click `<bundle id>` › check **App Groups** under Capabilities › click Configure next to it (Edit if already checked) ›
   check `group.<bundle id>` › Continue › Save › Confirm in the dialog (changing capabilities invalidates this App ID's existing profiles).
3. Do step 2 for the Dynamic Island extension's App ID `<bundle id>.liveactivity` too. The notification extension does not need it.
   For #1147, register the **separate watch group** (`app.watch_app_group`, default `<phone group>.watch`) and attach it to the existing watch app and the new configured watch widget App ID. These account changes and regenerated watch profiles require explicit approval; they have not been performed as part of the code change.
4. Profiles: cloud-signed export (automatic signing) regenerates them itself; `release.sh`'s manual signing needs a rerun of `python3 scripts/release/asc_signing.py`,
   which checks every profile for the group, and if missing prints the steps above and exits non-zero.
5. After a release export, `scripts/release/entitlements.py check` verifies the App Groups of the app and its extensions, including both watch bundles.

The #1147 system-surface store is independent of Fleet's avatar files, because it contains per-device credentials. Validate first unlock, offline expiry, pairing/rotation/removal, WidgetCenter full reports (including removal of the last configuration), paired-watch direct requests, and real background `extension_widget` pushes on devices before claiming acceptance. Simulator-only unsigned builds cannot validate App Group signing or background APNs delivery. No build, screenshots or device acceptance were run for #1147 while builds were paused.

## No CallKit (#9290)

App Review rejects a build offered in China when **any** binary in it links `CallKit.framework`, even weakly and even when the only use is a `CXCallObserver` inside an SDK. Calls go through LiveCommunicationKit (LCK). The system LCK framework links CallKit itself; that is Apple's binary, not the app's, and does not count. Only the Mach-O files inside `Persona.app` count. LCK reports its errors in CallKit's domains (`com.apple.CallKit.error.…`). Compare those as strings, and never `import CallKit` for a constant.

Two checks:

- **Sources** (`scripts/test/test_apple_no_callkit.py`, no build needed): every Swift and Objective-C file under `apple/` (all targets and their tests, `Shared`, the local packages) must not import CallKit or name a `CX…` type, outside comments and strings. An unused import counts, in any spelling: ``import `CallKit` ``, `import` and the name on different lines, `@import CallKit;`. The XcodeGen specs, `Package.swift` manifests, module maps, xcconfig files and the xcconfig `scripts/persona_config.py` writes must not mention CallKit outside a comment (`sdk: CallKit.framework`, `-framework` / `-weak_framework CallKit`, `-lswiftCallKit`, `.linkedFramework("CallKit")`, a Call Directory extension). It runs in `scripts/ci/guards.sh`, at the start of `scripts/ci/apple.sh`, and in `scripts/check-fast.sh` for `apple/` changes.
- **Binaries** (`scripts/release/check_no_callkit.py <.app | .ipa | .xcarchive>`): every file in the bundle that starts with a Mach-O magic number is checked, whatever its name (the main binary, `*.debug.dylib`, `Frameworks/*`, `PlugIns/*.appex`, the Watch app and its extensions), in every architecture. `otool -arch all -L` lists the dylib load commands, and any install name with CallKit in it fails the check (`CallKit.framework`, `libswiftCallKit.dylib`, weak or re-exported). The check prints `binary [arch] → dependency` for each one. An otool error, or a bundle with no Mach-O in it, also fails. On success it prints `no CallKit: N Mach-O files checked`. Only the binaries show what a remote package or a prebuilt framework links. `scripts/ci/apple.sh` runs it on the device build and on the simulator build-for-testing app. `entitlements.py check` runs it on the release archive, and with `NO_UPLOAD=1` or `UPLOADER=asc` on the exported ipa too. Export re-signs the binaries but does not change their load commands, so the archive check also covers the default upload.

Run them by hand:

```sh
python3 scripts/test/test_apple_no_callkit.py
python3 scripts/release/check_no_callkit.py <Persona.app | .ipa | .xcarchive>
```

## Siri calls (#9290, off by default)

"Call Persona using Persona" goes through SiriKit (`docs/apple/siri.md`, "Calling Persona"). The app is single-scene, so the intent needs an Intents extension (`PersonaCallIntents`, bundle ID `<bundle_id>.callintents`) with its own App ID and App Store profile, and the app needs the Siri entitlement (`com.apple.developer.siri`) with `SIRIKIT` on its App ID. That is account work, so it is behind config.json `app.siri_calls` (default `false`, `docs/operations/config.md`). The App Shortcut **Call Persona** ("Hey Siri, call Persona") needs none of it and ships in every build.

`NSSiriUsageDescription` (translated in `InfoPlist.xcstrings`) stays in `apple/project.yml` and the tracked `apple/Persona/Resources/Info.plist` in every build so App Store Connect does not reject uploads with ITMS-90683 (the iOS app unconditionally compiles `SiriCalls.swift` and `OwnerCall.swift`, which link `Intents`). With the switch off, the extension, Siri entitlement, `PersonaSiriCalls` flag, runtime Siri authorization prompt, signing preflight (`asc_signing.py --check`), export options and `entitlements.py check` stay off. With it on:

- `persona_config.py env` exports `PERSONA_SIRI_CALLS=YES`, so xcodegen merges `apple/siri-calls.yml`: the extension target embedded in the app; the app signed with `apple/PersonaSiriCalls.entitlements` (its entitlements plus `com.apple.developer.siri`, written by xcodegen, git-ignored); `PersonaSiriCalls` put into the built Info.plist before signing, as the commit stamp is; and Siri's example phrases (`AppIntentVocabulary.plist`, en / zh-Hans / ja; without them App Store Connect refuses the upload with ITMS-90626). No tracked file is rewritten, so the release worktree stays clean, which the unattended worker checks after every step. That is also why the app's entitlements path is in `apple/app-entitlements.yml`: in an XcodeGen merge `project.yml`'s own values win over every include's, while a later include's win over an earlier one's, so only a path set in an include can be moved by the overlay.
- The export options name the extension's profile, "Persona Call Intents App Store" (`persona_config.py export-options`), and `persona_config.py xcconfig` lists the entitlement in `apple/PersonaOptIn.entitlements` too.
- `asc_signing.py` asks for `SIRIKIT` on the iPhone App ID and for the extension's Bundle ID ("Persona Call Intents") and profile. `--check` (the start of every `release.sh`, the unattended worker's included) fails until both are there: that is why the switch must not be turned on first.
- `entitlements.py check` fails when the exported app lacks the Siri entitlement.

Enabling it, once, by the account's maintainer (Account Holder or Admin, or the App Manager API key the scripts use), in this order:

1. Make a copy of the config with `"siri_calls": true` under `app` (outside the repository, like the config itself) and prepare the app's App ID with it: `PERSONA_CONFIG=<copy> python3 scripts/release/asc_signing.py --only <bundle_id>`. This turns `SIRIKIT` on and rebuilds "Persona App Store" (changing a capability invalidates the old profile anyway). The other targets' profiles are left alone. Releases from the real config, still off, go on working: a profile that allows Siri signs an app that doesn't ask for it.
2. Register the extension and make its profile: `PERSONA_CONFIG=<copy> python3 scripts/release/asc_signing.py --only <bundle_id>.callintents`. It needs no capability (Siri's entitlement is the app's).
3. Signing development builds with these profiles: run both again with `--dev`.
4. `PERSONA_CONFIG=<copy> python3 scripts/release/asc_signing.py --check` must pass, then `PERSONA_CONFIG=<copy> NO_UPLOAD=1 ./scripts/release/release.sh`: archive, export with the extension's profile, and `entitlements.py check` on the ipa (the Siri entitlement, no CallKit in the extension either).
5. Turn `app.siri_calls` on in the real config, and in the unattended release worker's config if it has its own. The next release carries the extension.
6. On a phone, from TestFlight: Persona asks once whether it may use Siri (allow it; Settings › Apps › Persona › Siri changes it later). Say "Call Persona using Persona" and, in those languages, "用 Persona 打给 Persona" and "PersonaでPersonaに電話して": the call page opens and the call connects. Check it locked (Siri asks to unlock first, also with AirPods) and with AirPods.

Not known yet: how App Review in China treats a SiriKit call extension (it is Intents, not CallKit, so `check_no_callkit.py` passes; no report either way was found), and whether Beta App Review asks why a call intent is there (the #8734 review notes on calls apply). To turn it off again, set the switch to `false`; SIRIKIT and the extension's App ID can stay on the account.

## Changing the store name

The store name (shown in the App Store / TestFlight, unrelated to the home screen's `CFBundleDisplayName`) must be globally unique and lives in the App Info localizations. While the version is still `PREPARE_FOR_SUBMISSION`, an App Manager API key can change it:

```sh
python3 scripts/release/asc.py GET /v1/apps/<app id>/appInfos                    # get the appInfo id
python3 scripts/release/asc.py GET /v1/appInfos/<appInfo id>/appInfoLocalizations # one per language; get the id
# PATCH /v1/appInfoLocalizations/<id>, with name (and privacyPolicyUrl) in attributes; an occupied name returns 409
```

Renaming doesn't replace the app record: Bundle IDs, test groups, public links, released builds and existing installs are unaffected.

## Pitfalls

- **`bundleIdCapabilities` cannot take invented settings**: passing
  `settings: [{"key": "PUSH_NOTIFICATIONS", ...}]` for `PUSH_NOTIFICATIONS` is rejected with 409 (`settings/0/key` only accepts `ICLOUD_VERSION`,
  `DATA_PROTECTION_PERMISSION_LEVEL`, `APPLE_ID_AUTH_APP_CONSENT`). For push and HealthKit, pass an empty `settings: []`.
- **A profile with the same name blocks rebuilding**: after a capability change, the old profile becomes `INVALID`, and creating a new one with the same name returns 409. `asc_signing.py` deletes the same-named profile first, then creates it.
- **The test-group list is sometimes incomplete** (#945): `GET /v1/apps/{id}/betaGroups` sometimes returns an empty list, omits groups, or times out. The script used to treat that as a missing group and create one;
  on 2026-11-03 it created 4 extra Friends groups in one day, attached builds to an empty group, while all testers were in the earliest group. Now groups are matched by name and internal/external type, and the earliest-created group wins among duplicates.
  Only after three queries find nothing is a group considered absent. Only `setup` and `external` create groups; `wait` / `info` / `catch-up` error if none is found (during a release, just one line with a retry command).
  The script doesn't delete the extra empty groups; a person decides whether to delete them.
- **Create internal testers with `POST /v1/betaTesters`, including the group relationship**: if the same email already has a tester record under another App, directly linking it returns 409;
  on 409, the script prompts you to add that person to this App's `Internal` group on the website.
- **`aps-environment` in the archive is always `development`**: archiving uses a development profile; final signing happens at export. Don't check the archived product for this.
- **Push and production**: `UIBackgroundModes` uses `remote-notification` (the watch App has no background modes); `ITSAppUsesNonExemptEncryption=false`
  is already in `apple/project.yml`, avoiding the export-compliance questions on every upload.
- **No iCloud directories**: this Mac isn't unlocked; skip anything involving `~/Library/Mobile Documents`.

## Pending for the leader

(None. 2026-10-24: the clipJSON Chinese-truncation problem is fixed, and transcript.go has been gofmt'd.)

## Push (APNs)

- Create an APNs key at developer.apple.com (Sandbox & Production, Team Scoped); neither the Key ID nor the .p8 file goes into the repo.
- File: `~/.persona/apns_key.p8` on the hub machine (config `~/.persona/apns.json`), with another copy in a password manager. A key can only be downloaded once; if lost, create a new one.
- The page says the Apple Developer Program License Agreement has been updated; the Account Holder must accept it at developer.apple.com (doesn't affect app creation or TestFlight uploads).

## External testing (public link)

Internal testers must be team members; friends use external testing in the "Friends" group (`app_store.external.group`). On 2026-10-30 the user chose a **public link**
(`public_link: true`): share `https://testflight.apple.com/join/…` in a group, and anyone who clicks can install. Since 2026-11-03 there is **no tester cap** (`public_link_limit: 0`, previously 50, after which the link stopped accepting people): 0 or an omitted `public_link_limit` disables the cap, subject only to Apple's limit of 10,000 external testers per App; a positive number sets the cap.
Current public link: <https://testflight.apple.com/join/vsjfy3hE> (`asc_testflight.py external` prints it every time; update this if it changes; checked 2026-11-03, it belongs to the earliest Friends group).
iPhone and Mac use the same link (the same Friends group); opening it in TestFlight on a Mac gives the Mac version.
Since 2026-11-03 this link may be written in plain text in the repo (requested; README's installation instructions include it). Ask separately before putting it on the website (`site/`); it isn't there now. If the link changes (for example, the group is recreated), update both README and this doc.
No email collection needed; `app_store.external.testers` still works, and listed emails also get invitations. With `public_link: false`, only those emails can join.

```sh
python3 scripts/release/asc_testflight.py external   # create the group, enable/disable the public link per config, write test info, attach the latest build, submit for review; print the public link
```

- **Public link = anyone with the link can install**: builds must contain nothing personal (hub address, hostname, email, paths…). Before uploading, `release.sh` runs
  `scripts/release/check_bundle_privacy.py <Persona.app>` (using the same sensitive-word list as `privacy_check.py`, checking binary strings, plists and lzfse metadata;
  bundle ids, team ids and signing data are inherent to every build and don't count). Custom release scripts must run it before uploading too.
- **No separate command needed for each release**: with `app_store.external` configured, after attaching Internal, `wait` and `info` also attach this build to Friends
  and submit for review (inviting listed testers if any). A failure here only prints one line (with the retry command `external`) and doesn't fail the release.
- **A new build can't be submitted while another build of the same version is in review** (422 `ANOTHER_BUILD_IN_REVIEW`, "submit after review finishes"), and nothing used to submit it later:
  while Mac 110 was in review, 111–127 were all attached to Friends but stuck in `READY_FOR_BETA_SUBMISSION`; external testers never got them (2026-11-03, #792).
  `asc_testflight.py [--platform mac] catch-up`: if the latest VALID build hasn't been submitted, attach it to Friends and submit (without changing its What to Test);
  if another is in review, say to wait for the next run; if already submitted / approved / rejected, write nothing. Each local auto-tf round (every 10 minutes while waiting for a new commit) runs this once for iPhone and once for Mac.
- **The review submission limit**: when App Store Connect's limit on Beta App Review submissions is used up (422 `SUBMISSION_LIMIT_REACHED`), `wait`, `info`, `external`
  and `catch-up` print one line saying so, leave the build in Friends unsubmitted and don't invite testers; a later `catch-up` round submits it. It is never retried at once.
- **Dropped connections**: every App Store Connect call (`scripts/release/asc.py`) tries again with doubling waits (2 s up to 30 s, six tries, about a minute) when the
  connection fails or breaks, such as a TLS handshake ended early (`[SSL: UNEXPECTED_EOF_WHILE_READING]`), a reset or a timeout, and on HTTP 429 and 5xx. Other 4xx answers fail at
  once. A `POST` is tried again only when it never got out (urllib's `URLError`), so a repeat can't create a second resource (#945). Two auto-tf rounds lost their Mac step to
  one such handshake before this (#4034).
- External builds must pass **Beta App Review** (the first takes about a day; later builds of the same version usually pass automatically). A contact is required before submission:
  `first_name` / `last_name` / `phone` / `email` in `app_store.external.review`. If missing, the script only attaches the build, without submitting or adding testers.
- The public link opens before any build passes review (the API gives the address directly), but the page says "This beta isn't accepting any new testers right now";
  joining is possible only after review passes.
- Review state is `buildBetaDetail.externalBuildState`: `READY_FOR_BETA_SUBMISSION` (attached, not submitted) → `WAITING_FOR_BETA_REVIEW` →
  `IN_BETA_REVIEW` → `BETA_APPROVED` / `BETA_REJECTED`.
- Friends deploy their own hubs: without an APNs key from a developer account, newer Apps receive notifications and Live Activities through the publisher's push relay
  (requires `app.push_relay` in the release config and successful device registration; see `docs/operations/push-relay.md`); older versions without a relay only receive live updates while the App is open.
  For Gmail login they must create their own Google OAuth client and put it in their manager's `manager.json` on the hub machine (`docs/extensions/mail.md`).
- Test info needs a privacy policy URL (`app_store.external.privacy_url`); Chinese and English descriptions live in `BETA_INFO` in `asc_testflight.py`.
- Without a hub, reviewers can choose **"Try it first (made-up data)"** on the welcome page. This offline demo ships in Release,
  needs no account or server, and uses bundled fictional data with canned replies; external operations are not performed
  (`docs/apple/ios.md`, "Demo mode"). Review notes (`review.notes`) must give that entry and its limits, and explain that full use
  requires the user's own self-hosted hub. Never give reviewers access to a developer's personal hub or private data.

## Retired native Mac version (2026-11-02)

**This native `PersonaMac` distribution path is retired (#6770). Do not run it for new desktop releases.** The Electron desktop's universal macOS DMG/ZIP, unsigned development configuration and separate signing/notarization gates are in [desktop.md](../desktop/desktop.md). The former TestFlight Mac build process is retained below only as a historical record; it is not evidence that an Electron Developer ID or Windows signing identity exists.

The Mac App (target and scheme `PersonaMac`) shares the iPhone's bundle id, so it is **the same app record** in App Store Connect (macOS platform added, version 1.0),
and uses the same TestFlight: the same Internal / Friends groups and public link. Testers install TestFlight on a Mac to see Mac builds.

- **Build numbers**: a number can only be used once per platform; different platforms can reuse it. Rule: **the Mac build and iPhone build in one release use the same number**
  (same number = same commit, easy to match when investigating); iPhone builds and standalone Mac builds use **the largest number across both platforms plus one**
  (`python3 scripts/release/asc_testflight.py next-build`), so one number never identifies two different commits on the two platforms.
- **`asc_testflight.py --platform mac`**: `status` / `wait` / `info` / `external` / `catch-up` only consider Mac builds; without it, only iPhone builds.
  Previously platforms weren't distinguished; after Mac builds appeared, "the latest VALID build" could be on the other platform, and What to Test would be written to the wrong build.
- **Icon**: the system doesn't round Mac icon corners; the icon itself has a rounded square and shadow (the macOS grid: a centered 824 square within 1024). Besides the single-size iOS image in `AppIcon`,
  there are `idiom: mac` sizes 16–512 (1x / 2x), generated from the iPhone image by `python3 scripts/assets/make_icon.py mac` (also written when regenerating everything).
  Without these the Mac package has no icon and the Mac App Store rejects it. Xcode 26's `AppIcon.icns` only has sizes 16 and 128; the full set is in `Assets.car`
  (the system reads it using `CFBundleIconName`), so the check looks for 512 and 512@2x in `Assets.car` (`xcrun assetutil --info`).
- **Info.plist**: `LSApplicationCategoryType` (productivity) and all permission descriptions are defined in the `PersonaMac` target in `apple/project.yml`.
- **Sandbox**: `com.apple.security.app-sandbox`, plus networking (client and local server for port forwarding), user-selected files, contacts / calendar / location / photos,
  microphone / camera (entitlements in `project.yml`).
- **Push** (historical, since 2026-11-02): `com.apple.developer.aps-environment` (the macOS name; iOS uses `aps-environment`).
  The App ID is UNIVERSAL and already has Push Notifications enabled, so no developer-account change is needed; the archive has `development`, and cloud signing at App Store export changes it to `production`.
  The package also includes notification service extension `PersonaNotify.appex` (target `PersonaMacNotify`, module / notification main class still `PersonaNotify`, in `Contents/PlugIns/`, sandbox + `.push` keychain group, #432).
  Its bundle id is the same as the iPhone extension's (UNIVERSAL App ID); cloud signing creates its Mac profile at export. `entitlements.py sign / check` signs and checks it too.
- **Pre-release checks** match the iPhone's: permission descriptions (`check_bundle_strings.py`), privacy scan (`check_bundle_privacy.py`, recognizes Mac's
  `Contents/` layout, skips `embedded.provisionprofile`, scans the entire universal binary), no DEBUG switches in Release; also checks the category and 512 / 512@2x icons.
- Export produces a `.pkg` (ExportOptions `method` is also `app-store-connect`), cloud-signed like the iPhone: the App uses Apple Distribution,
  the `.pkg` uses 3rd Party Mac Developer Installer (`pkgutil --check-signature` says "issued by Apple (Development)"; all certificates of this type do).
  Upload with `altool -t macos`. The local release script runs `entitlements.py sign` before export, as for the iPhone, then `entitlements.py check <.pkg>` afterwards:
  every bundle has its target's entitlements, and push is `production` (`check` recognizes Mac's `Contents/` layout and `.pkg`; it checks the push environment in iPhone ipas too).
- **Package files must be readable by other users** (Mac 110, 2026-11-02): files in the `.pkg` were owner-readable only, causing ITMS-90255 at upload,
  "files that are only readable by the root user" (macOS verifies signatures as the user running the App). auto-tf's umask is 077;
  the script's `cp -R` made the copied archive entirely 0700 / 0600, which never showed up when testing in an interactive shell (umask 022). The release script now starts with `umask 022` and copies archives with `ditto` (preserves permissions).
  It checks the `.app` before export and the App unpacked from the `.pkg` afterwards: every file a+r, every directory a+rx, otherwise no upload. The iPhone script was changed too (ipas aren't checked for this; 110 passed there).
- **Automatic release (historical)**: after releasing the iPhone build, if a commit included changes used by the Mac App (its native-Mac source directory, `apple/Shared`, `apple/Persona` except a few iOS-only
  files, `apple/Packages`, `project.yml`, `Package.resolved`), the former process released Mac next with the same number. This step is retired with `PersonaMac`; use the Electron packaging workflow.

## Publishing personad (`scripts/release/publish.sh`, 2026-10-31)

Let others install the server without getting the source: prebuilt `personad` and the installer are hosted on the `site/` Worker (configured `publish.site_url`).

```sh
curl -fsSL <publish.site_url>/install.sh | sh   # install to ~/.persona/bin/personad; verify against SHA256SUMS, refuse if wrong
```

- On the computer that runs the hub (`--hub`, a `hub.json`, or the two there already) it installs `persona-hub` and `persona-manager` with `personad`, the same release, each checked before any is put in place. A release without one of them leaves the one there as it was, unless a systemd unit or launchd agent runs it: then it is refused before anything changes ([../server/server.md](../server/server.md) ("Programs"), [hub-update.md](hub-update.md) ("A release without `persona-hub` or `persona-manager`")).
- After installing `personad`, if herdr can't be found on this computer (PATH and directories node searches, such as `~/.local/bin`), install it with herdr's official installer (`https://herdr.dev/install.sh`)
  too, so sessions appear in Agents after the first install. Failure only prints one warning and doesn't affect `personad`; computers using Orca / Herdr App can skip it with `PERSONA_SKIP_HERDR=1`.
  `deploy.sh` does the same when installing `personad` on each computer (`scripts/deploy/ensure-herdr.sh`; keep the two sets of steps aligned).

- **Two editions.** The same run builds the core edition (Persona Core alone, `persona_core_only`) for the same
  platforms into `build-publish/core` (`scripts/release/core_edition.sh`), checks it and both editions against each
  other, and lays it out as served: `core-<name>.gz`, `core-latest.json`, `core-SHA256SUMS`, `core-VERSION`.
  `PERSONA_CORE_PACKAGES=0` leaves it out. A deploying run uploads it only with `PERSONA_PUBLISH_CORE=1`; the GitHub
  release workflows do not attach it yet ([core-install.md](core-install.md)). `install.sh --core` installs it.
- `release.sh` runs this after uploading TestFlight. The version is `<commit>-<build number>`, matching that TestFlight release; `NO_PUBLISH=1` only releases TestFlight,
  and no publish happens without `publish.site_url` in config. Standalone: `./scripts/release/publish.sh`; `NO_DEPLOY=1` only builds and checks (outputs in `build-publish/`). When the tag coordinator supplies `PERSONA_RELEASE_VERSION`, `publish.sh` instead uses that exact tag version, and the coordinator rejects a different manifest version before uploading assets.
- Website-only changes (`site/`): `SITE_ONLY=1 ./scripts/release/publish.sh` skips a new build, fetches the small files of the currently published version (`VERSION`, `SHA256SUMS`, `latest.json`, `SLOT`, `install.sh`) and deploys them with the committed `site/`, leaving archives in R2 unchanged. Afterwards it checks the live small files are unchanged and every archive still exists (`HEAD` size). It refuses if there's no live `dl/SLOT` yet (a release from before R2); do one full publish first. **Don't run `wrangler deploy` directly in `site/`**: it makes `/dl/` and `install.sh` return 404 (`docs/lessons/server.md`).
- Web dependencies always install from the official npm registry (`npm ci --registry=https://registry.npmjs.org/`, overridable with `PERSONA_NPM_REGISTRY`), ignoring local npm config. `package-lock.json` already points to the official registry; a locally configured mirror may lack packages (on 2026-11-06 it lacked `elkjs`, stopping release before web bundling).
- Build four platforms (linux / darwin × amd64 / arm64), with `-tags "$(cat server/build-tags.txt)" -trimpath -s -w`, embedding `channel=release`, build number (UTC `YYYYMMDDHHMM`) and `updateURL`
  (a non-empty environment `PERSONA_UPDATE_URL`, then the config's `publish.update_url` as written, e.g. a GitHub repository's `releases/latest/download`, else `<publish.site_url>/dl`; the script notes when the binaries look somewhere other than the site it publishes to; a hub's `hub.json` `update.url` replaces it at run time, `docs/operations/hub-update.md` ("Update source")). `install.sh` is rendered from that same update base, so its downloads and the binaries' update checks cannot silently select different feeds. For GitHub's moving Latest base, the installer reads `latest.json` once and pins `SHA256SUMS` and every archive to that version's immutable tag:
  - **Web bundled into the binary** (`server/internal/hub/webdist`, `go:embed`): the hub uses it when the hub machine has no `~/.persona/web`; otherwise it still uses the directory.
    The plain copy is privacy-scanned first, then its hashed assets and `third-party-licenses.txt` are kept gzipped (`go run ./internal/webasset/precompress`); the script stops if none are, or if a compressible asset of 1 KB or more is left plain. The first screen's brotli copies (`name.br`) come from `npm run build` and stay as they are (`docs/web/web.md` "Precompressed assets").
  - **No skills included**: the skill connecting to the Lark gateway was copied from a private upstream repository (GPL-3.0) and cannot be redistributed. Skills live in `~/.persona/skills/` on each machine,
    so there is only one binary, used by our own hub too (`docs/operations/hub-update.md`). The script ensures the binary only contains skills registered as redistributable (`scripts/release/check_bundle_skills.py`).
    The tags are the one line of `server/build-tags.txt`. No persona file builds differently with any of its tags (`TestOneBuild`). Its `public` marker only tells the privacy scan the binary is a release build, and the scan accepts exactly that line.
    Its `ts_omit_` tags leave out the tailscale.com features persona never uses (SSH server, web client, debug pages and metrics, and others), which makes `personad`, `persona-manager` and `persona-node` 4.6–4.8 MB smaller on linux/amd64 and leaves less to initialize at each start.
    `server/internal/buildtags` computes the line from tailscale.com's feature registry; after a tailscale.com bump, `TestTagsFileInSync` asks for `cd server && go run ./internal/buildtags/printtags > build-tags.txt`.
    The image, `deploy.sh`, `mac-link.sh` and the test scripts build with the same line (`scripts/test/test_build_tags_builds.py` checks every `go build` of the image, `deploy.sh`, `mac-link.sh` and `publish.sh`), and CI builds the programs and runs the tunnel tests with it (`scripts/ci/server.sh`).
  - **Version manifest** `dl/latest.json` (each platform's archive and unpacked sha256, build number, commit), unsigned (the user's decision; trade-offs in `docs/operations/hub-update.md` ("Security"));
    before publishing, check that the build number is greater than the live one, the live version's commit is an ancestor of this one, and server/ and web/ have no uncommitted changes.
    `release manifest` refuses binaries of another build than the one it writes, and `personad release verify` reads the written `latest.json` back against every download before the first upload. It runs this machine's binaries (`version -v` must say the version and build) and finds the build number in the others. A manifest written over an earlier run's binaries (v0.2.1, #4522) stops there (`docs/operations/hub-update.md` ("Version manifest")). The manifest includes all commits in this release (`ancestors`):
    hubs read it for auto-update, but only install automatically if it includes the commit they're running (`docs/operations/hub-update.md` ("Preventing rollback")).
  - **Programs published together**: `persona-hub` and `persona-manager` (`docs/server/server.md`), built for all four platforms with the same
    `-X` flags, privacy scan and size check as personad; its `version -v` must match personad's. The manifest's `programs` lists them, and hub auto-update replaces them together.
  - Before upload, run `check_bundle_privacy.py --whole-short`: binaries must contain none of the configured values (except Go module paths; values of four characters or fewer match whole words,
    since Unicode tables and compressed JS contain short letter combinations everywhere). A few protocol names, official contact links and standard network constants in public dependencies are classified by exact context, without allowing whole domains or private network ranges;
    it requires the binary's `public` marker, unreplaced dependencies and checksums matching `server/go.sum`, and `go mod verify` before building. Registered personal values always take precedence and are blocked; see `docs/lessons/testing.md`.
- Build the iPhone helper `persona-iphone-bridge-<os>-<arch>` for the four platforms when this computer can (`native/iphone-bridge/release.sh`; macOS needs `rustup target add aarch64-apple-darwin x86_64-apple-darwin`, Linux needs `zig` and `cargo install cargo-zigbuild` plus `rustup target add x86_64-unknown-linux-gnu aarch64-unknown-linux-gnu`). Cargo keeps its build directory at `PERSONA_BRIDGE_TARGET` (default `~/.cache/persona/iphone-bridge-target`). A platform it can't build, or whose build fails, is left out with a `note:` line; the release goes on without it. `latest.json` lists only the builds that are there under `helpers`, and they are kept with the node builds under `dl/<version>/` ([iphone-bridge.md](../extensions/iphone-bridge.md), "Releases"). A version-tag upload should carry `persona-iphone-bridge-<os>-<arch>.gz` with the other downloads; when it doesn't, `tag_release.py` warns and uploads `latest.json` without those helpers instead of failing.
- Build the connector `persona-link-<os>-<arch>` for the same four platforms too (2026-11-02, node service, `install.sh --node`, see `docs/operations/link.md` ("Installing the node service")), plus `persona-link-windows-amd64.exe` and `persona-link-windows-arm64.exe`, with the same privacy scan and gzip compression, included in `SHA256SUMS`. `latest.json` lists all six connectors under `links`; hub updates never swap them. `persona-link version -v` reports the same version, channel and build as personad so the manifest verifier can identify every connector. Windows download and scheduled-task installation commands are in [windows.md](windows.md) ("Install and start at logon"). The POSIX `install.sh` remains Linux/macOS-only.
- Build the device's own program `persona-node-<os>-<arch>` (`server/cmd/persona-node`) for the same four platforms, scanned, compressed and included in `SHA256SUMS` the same way (it must report the same `version -v` as personad). `install.sh --node` installs it at `~/.persona/bin/persona-node` and the node service runs it ([link.md](link.md) "The device's program"). `latest.json` lists it under `nodes` (old hubs ignore that): hub self-update still brings `personad-*` alone, and the manager fetches persona-node from `dl/<version>/` for a device that runs it (`docs/operations/node-update.md` ("Node packages")).
- Downloads are `personad-<os>-<arch>.gz`: scan the raw binary first, then compress with `gzip -n -9`; `SHA256SUMS` verifies the archive. The installer verifies it before unpacking and installing; failed verification / decompression never overwrites the old version. Public artifacts and their index use only Persona names, including `persona-link-<os>-<arch>.gz`; old-name aliases remain inside installed runtime compatibility, not new downloads. The installer still reads historical manifests with older names.
  **Archives go in R2 bucket `persona-dl`, not the site** (#2541): Workers static assets have a 25 MiB per-file limit ([official limit](https://developers.cloudflare.com/workers/platform/limits/#static-assets));
  after #1002 embedded tiered avatar images, compressed personad exceeded it. The Worker binds the bucket as `DL` (`site/wrangler.jsonc`); `worker.mjs` serves `/dl/*.gz` from it (`GET` / `HEAD` / `Range` / `If-None-Match`),
  while other `/dl/` files remain static. The bucket has slots `a` and `b`: publishing uploads archives to the inactive slot (`<slot>/<filename>`), then deploys the site, switching `dl/SLOT` and `latest.json` together.
  The other slot keeps the previous version until the next publish, so downloads in progress at deployment can finish.
  Every release is also uploaded to `release/<version>/` (its node builds, `personad-<os>-<arch>.gz` and `persona-node-<os>-<arch>.gz`, and `latest.json` as `manifest.json`), and to `release/X.Y.Z/` when the commit carries the tag `vX.Y.Z` at publish time (tag before publishing; looked up with `git tag --points-at`, as the commit has its `testflight/<build>` tag too; never for a `-dirty` build); these are kept (about 160 MB a release, twice for a tagged one: remove old ones from the bucket by hand once no hub of theirs is left), and `worker.mjs` serves them as `/dl/<version>/<file>`. A hub's manager fetches the node build of its hub's version there (`docs/operations/node-update.md` ("Node packages")); publishing checks the served copy too. The site only has small files now; `check_asset_sizes.py` still checks the whole site directory. Local `build-publish/bin` keeps uncompressed binaries for debugging but doesn't upload them.
  Regression: `python3 -B -m unittest discover -s scripts/deploy -p 'test_compressed_publish.py'`.
- **Before releasing a hub ahead of the App, check compatibility with the released App** (#1693): if this hub makes clients draw something new (a new connector-login `kind`, interaction type,
  card / event fields) but the latest TestFlight doesn't yet include the client changes, installed Apps show it blank or error (the WeChat plugin's `kind: qr` on an App without #1401
  is an empty form with a permanently disabled "Next"). Before publishing the hub, compare against the latest TestFlight commit (`git merge-base --is-ancestor <client change> <TestFlight commit>`):
  if missing, release TestFlight first, make the new feature fall back to something old Apps understand, or raise `proto.MinAppBuild` (`server/internal/proto/proto.go`, hello and
  `min_app_build` in `latest.json`) to the build containing it so old Apps prompt for an update. Raising `min_app_build` makes every older App unable to use this hub; ask the user first.
- Only the latest version is hosted (R2 also retains the previous one, as above). Bucket uploads and deployment use configured `publish.wrangler` (otherwise `npx wrangler`). Cloudflare OAuth expires after one hour;
  on an auth error, run `cf auth login` and retry. After deployment it reads back `dl/VERSION`, `dl/SHA256SUMS`, `dl/latest.json`, `dl/SLOT` to confirm the just-built version is live, and checks every archive's size with `HEAD`.
  The push relay is in the same Worker (`/relay/*`) and is redeployed on each publish too: when `/relay/v1/health` answered `ok` and `apns` before the deploy,
  the publish fails unless it still does within about two minutes after it (#1263; a site without a working relay only gets a note).
- Push relay deployed (2026-11-02): resident `personad relay` + Cloudflare Tunnel + website Worker forwarding; public health, origin protection, registration and authentication verified.
  Release config needs `app.push_relay`; only a new TestFlight with the notification extension registers. Notification decryption on real devices still needs separate acceptance; see `docs/operations/push-relay.md`.
- Not yet available (others can install, but it's still awkward; see `docs/operations/config.md`):
  Developer ID notarization of the macOS binary (browser downloads get blocked by Gatekeeper; `curl` doesn't).

## Release PRs and GitHub Actions publication

**Every new formal or beta release is an explicit release PR.** The PR title is `release: vX.Y.Z` for a formal release or `release: vX.Y.Z-beta.N` for a beta. It targets `main`, originates in this repository, and is authored by administrator `chyroc`; an ordinary feature PR, fork PR or branch is not a release request. Do not create a local version tag to start a new release.

1. `release-schedule.yml` runs stable selection at 12:00 in the operator's configured UTC+08:00 zone (04:00 UTC) and beta selection every two hours (never every two minutes). It skips an unchanged source and opens a release PR with an unused immutable version. After any protected-environment approval, it fetches and detaches the then-current `origin/main`, then runs `scripts/operations/select_green_commit.py` to walk first-parent `main` history backward from `origin/main` to (excluding) the previously released commit and select the newest commit whose full `ci.yml` run passed on `main` (all eight platform lanes plus repository guards and `CI required`). If no newer full green CI commit exists, it exits cleanly without opening a release PR; otherwise it detaches that green commit and records its SHA and `ci_run_id` in `.github/release-prs/<version>.json`. A feature since the previous stable tag increments minor; a fix-only history increments patch. The subject classifier recognizes `feat:` / `feature:`, an imperative `Add …`, and explicit additions, support or implementation under `Android:`, `iOS:` / `iPhone:`, `Watch:` / `watchOS:`, `Web:` and `Desktop:`. Fixes, tests, documentation and refactoring stay on the patch path; subjects outside these conventions require review rather than an assumption that every change is a feature. There is at most one open release PR per channel, including a retry-suffixed `-rN` branch: while one exists, a scheduler reports its fixed-source verification instead of silently displacing it because main advanced. A real repair explicitly supersedes and closes that unmerged PR before selecting a replacement. A replacement for a failed, unmerged candidate keeps that candidate's branch and source unchanged and uses a fresh `-rN` branch; it never rewrites a failed candidate or creates its tag before the replacement passes. It uses `RELEASE_PR_TOKEN` in the protected `persona-release` environment so the PR has the administrator's identity, while `select_green_commit.py` reads workflow runs using the workflow's own `GITHUB_TOKEN` (`actions: read`). A new scheduled tick replaces an older scheduled run waiting in that environment, rather than leaving it in the automatic concurrency group until every later tick is displaced and cancelled before its first job. Only schedule events have this cancellation behavior; manual stable and beta dispatches retain separate non-cancelling groups. This supersedes only an unstarted planner: the latest run still selects from `origin/main` after approval, and it never automatically merges, tags, publishes or deploys. Do not replace it with the workflow's ordinary `GITHUB_TOKEN` or run a second local release scheduler.
2. `release-pr.yml` first checks out the PR's proposed merge result, not an arbitrary branch or an earlier PR head, and resolves the pinned green-CI target commit from `.github/release-prs/<version>.json` via `release_pr.py resolve-target` (verifying that the pinned commit is an ancestor of the merge commit, or falling back to the merge commit when no descriptor is present). CLI/Web, Android, iPhone/Watch and Windows build, test and sign that exact pinned target commit into one verification plan that binds both `commit` (pinned target) and `merge_commit` (PR merge result), while the required merge check attaches to `merge_commit`. If a required build or check fails, repair the release PR and run it again; do not merge a failing release PR or substitute an older artifact. The aggregate runs with `always()` for every non-empty release plan and checks out `merge_commit` before its platform gate, so its verification and failure-conclusion scripts are always available even when a dependency failed or the pinned target predates newer workflow tooling. A failed or skipped platform makes both the aggregate and the exact merge-result check fail, rather than leaving the check pending forever.
3. Only after every required platform check passes does the protected merge job squash-merge that release PR with the administrator's `RELEASE_PR_TOKEN`. The ordinary PR watcher never merges a `release:` PR, and the main ruleset requires `All available release-PR checks passed`; an unrelated or direct merge cannot bypass the platform gate. The merge gate also requires the exact successful check and unexpired verification artifact; the merge job is restricted to an administrator-authored, same-repository release PR and verifies its unchanged head again. A different merger is a hard publication failure, not an identity to whitelist. Squash creates a new main SHA: the `push` run of `release-pr.yml` then resolves the merged PR and its pinned target commit and **rebuilds and reverifies that exact pinned target on every platform**. A successful PR head is never treated as proof for the squash result.
4. Only after the post-merge run succeeds does `release-publish.yml` start through `workflow_run`. It rereads the merged PR, resolves its pinned target commit from `.github/release-prs/<version>.json`, requires both the merged release PR commit and its pinned target commit to remain ancestors of the current `main`, and verifies the new plan (`commit` and `merge_commit`) and every artifact digest. Ordinary PRs may merge during that build: they do not replace the release source, and the tag remains bound to the verified pinned target commit, never to a later `main` tip. It then creates the immutable annotated tag once at that exact pinned target commit. An existing tag for another source is a hard failure, never moved. The existing tag-triggered `release.yml` builds the container and creates the stable/prerelease GitHub Release. The publication job uploads the verified artifacts, compares remote SHA-256, independently downloads and compares every artifact, and only then advances the beta pointer. Stable `releases/latest` and stable metadata never come from a beta PR.
5. TestFlight and release attachments are separate results. A completed release PR does not deploy the production Hub/site, submit an App Store production version or install on a phone. Repeating a failed publication is an administrator-only `release-publish` dispatch for the same merged release PR and its verified merge commit, which must remain an ancestor of current main; it never selects another source.

### Protected environment

`persona-release` holds the release-PR token and the signing/build values used by these jobs. `RELEASE_PR_TOKEN` creates release PRs as `chyroc`; after all checks pass, the same protected identity performs the release merge, creates the immutable tag and marks its Release as owned by that PR. The last two operations are required so the tag-triggered `release.yml` can verify its administrator actor and the signing jobs can recognize the verified release-PR artifacts. Prefer a GitHub App installation token for that account, restricted to this repository with only **Contents: read and write** and **Pull requests: read and write**; a short-lived fine-grained token with exactly the same repository and permissions is the alternative. It needs no administration, packages, signing, Actions or organization access. The scheduler exchanges it only through the protected environment and never writes it to the repository or logs. Do not reuse the current interactive `gh` credential as a permanent release token. Android uses `PERSONA_ANDROID_APPLICATION_ID`, `PERSONA_ANDROID_CERTIFICATE_SHA256`, `PERSONA_ANDROID_KEYSTORE_BASE64`, `PERSONA_ANDROID_KEY_ALIAS`, `PERSONA_ANDROID_KEYSTORE_PASSWORD` and `PERSONA_ANDROID_KEY_PASSWORD`; the private `ci_release_config.py` writes a temporary mode-0600 signer configuration and keystore only inside the runner. Apple uses `PERSONA_APPLE_API_KEY`, `PERSONA_APPLE_API_KEY_ID`, `PERSONA_APPLE_API_ISSUER`, `PERSONA_APPLE_APP_ID`, `PERSONA_APPLE_TEAM_ID`, `PERSONA_APP_BUNDLE_ID`, `PERSONA_TESTFLIGHT_FEEDBACK_EMAIL`, `PERSONA_TESTFLIGHT_EXTERNAL_JSON`, and optional `PERSONA_APP_GROUP`, `PERSONA_WATCH_APP_GROUP`, `PERSONA_WATCH_WIDGET_BUNDLE_ID`. Windows desktop uses the existing `PERSONA_DESKTOP_APP_ID`, `WIN_CSC_LINK` and `WIN_CSC_KEY_PASSWORD` prerequisites. Never commit these values, put them in PR text, or include the temporary configuration in an artifact.

An unconfigured required signing platform blocks the release PR; it is not a successful skip. Windows amd64 native verification runs on `windows-latest`; its three verification reports are included once, and a platform seeing the same download again with identical bytes is idempotent (different bytes still fail). The combined receipt checks only the runner's actual architecture: amd64 evidence never marks ARM64 verified, and ARM64 remains blocked until a matching native ARM64 runner supplies its reports. HarmonyOS production HAP still requires DevEco and production signing (#6734). No debug IPA/APK, unsigned HAP, development Electron installer or ad-hoc signature is an acceptable replacement. Release-PR jobs use only their required protected credentials; the unprivileged CLI/Web job has no signing secrets.

### Historical local tag consumers

The local tag-consumer section below documents the pre-release-PR pipeline and existing private recovery tools. It is not a second scheduler to run alongside release PR publication. Existing tags and Releases remain immutable historical records.

## Publishing a release (`.github/workflows/release.yml`)

A `v*` tag publishes the hub container image and creates a GitHub Release. **Only `chyroc` may create, update or delete a `v*` tag, dispatch an image release or rerun its jobs.** The repository's `Release tags: administrators only` ruleset permits only the administrator role to bypass its create/update/delete restrictions; other collaborators cannot publish a version tag. The workflow independently requires both the run's actor and its triggering actor to be `chyroc` before any checkout, build or upload, including manual dispatches and reruns. The Release's `github-actions[bot]` author identifies the workflow that created it, not the person who pushed the tag; the Actions run records that triggering actor.

This boundary covers automated publication; collaborators retain their existing write permissions. GitHub does not offer a separate release-only permission on this organization, so those permissions still allow a manual GitHub Release from an existing ref. Restricting manual releases too would require a separate collaborator-permission change, outside this policy.

Docker hubs (`docs/operations/docker.md`) use the ghcr image, linux/amd64 + linux/arm64:

```sh
git tag v1.2.0 && git push origin v1.2.0
# -> ghcr.io/<owner>/<repo>:1.2.0 and :latest
# -> GitHub Release v1.2.0 with generated notes
```

- Smoke-test first (amd64 + compose), then build and push both architectures only after it passes. Once image publication succeeds, the same job creates a GitHub Release named for the full tag with automatically generated notes. Stable Releases are created with `--latest=false`: the complete verified hub consumer marks one Latest only after its assets and immutable tag manifest pass publication. Prerelease tags containing `-` (`v1.3.0-rc1`) only push the version image, without touching `latest`, and mark the GitHub Release as a prerelease. Creating the Release does not push another tag.
- To get an image of the current branch without a tag or GitHub Release, `chyroc` can manually run release in Actions (`edge`, `sha-<commit>`).
- Image smoke tests run through an explicit workflow dispatch; only the documented `v*` release-tag event starts image and GitHub Release publication automatically (`packages: write` and `contents: write` are granted only to the publish job). This exception supplies the version and `latest` images used by Docker installations; no branch push or PR starts it. A manual dispatch never creates a GitHub Release.
- If Release creation fails after the images were pushed, rerun the failed job for the same tag. Rebuilding and pushing the same immutable image tags does not advance `latest` to another source revision; fix any reported tag or Release conflict rather than creating a replacement tag.
- The image's personad uses the tag minus `v` as its version (`personad version`); when node differs, the App prompts to redeploy, using the image's own builds or the release's (`docs/operations/docker.md` ("Node builds")).
- The image's personad is on the `docker` channel: no skills (like all binaries), no self-update; a new image prompts you to pull it (`docs/operations/hub-update.md`).
- When the repository is private, the package is private too; NAS installs need `docker login ghcr.io` (`docs/operations/docker.md` ("Image")).

### Darwin command-line programs on version tags

The `darwin-cli-sign` job signs `personad`, `persona-node`, `persona-link`, `persona-hub` and `persona-manager` for both darwin architectures on stable and prerelease version tags. Its `darwin_cli` availability check needs only `MAC_CSC_LINK` and `MAC_CSC_KEY_PASSWORD` in the protected `persona-desktop-release` environment; it is independent of the desktop package version gate and does not require the desktop notarization credentials.

The job builds the tag's programs, signs them under fixed identifiers, verifies each Developer ID signature, recompresses the signed binaries, rebuilds `SHA256SUMS` and `latest.json` from those downloads, verifies that manifest, and attaches the darwin archives and metadata to the same Release. If either certificate secret is missing, the gate reports `Darwin CLI signing blocked` with the missing secret names and the job does not build or upload; there is no ad-hoc fallback. A missing Developer ID identity also stops the job before upload.

Prerelease CLI artifacts do not change the desktop package channel policy or GitHub's `releases/latest` selection. The `latest.json` attached to a prerelease is that Release's version manifest, not a move of the stable Release pointer.

### Desktop packages on version tags

The same workflow attaches the Electron desktop packages to the Release (`docs/desktop/desktop.md`, "Updates"); installed desktop apps update from them.

- Nothing extra to run: push the stable tag as above. After the `authorize` job, `desktop-gate` checks which signing secrets are configured, `desktop-mac` and `desktop-win` build and verify signed packages on native runners with the tag's version, and `desktop-publish` attaches them after the Release exists: packages and block maps first, `latest-mac.yml` and `latest.yml` last.
- **Without the signing secrets the release still succeeds and has no desktop packages.** The run's summary says which platform was skipped and which secrets are missing. This is the state today: no Developer ID or Windows code-signing certificate has been supplied (`docs/desktop/desktop.md`, "What the owner must provide", lists the eight secrets and what each must contain).
- Each platform stands alone: with only the macOS secrets configured, the Release gets the macOS packages and the summary says Windows was skipped.
- Prerelease `vX.Y.Z-beta.N` tags publish signed desktop packages when the same signing prerequisites exist. electron-updater reads the prerelease channel (`beta.yml` / `beta-mac.yml`); stable packages continue to read `latest.yml` / `latest-mac.yml` and never opt into prereleases. The prerelease's own immutable Release owns its metadata. Other prerelease spellings and manual dispatches publish no desktop packages.
- If a desktop job fails after the Release was created, rerun the failed jobs for the same tag; uploads replace assets of the same name. The hub image and the Release itself are not affected by a desktop failure.
- After the first signed release, check on a real Mac and a real Windows PC that an installed older version offers "Restart to update" and reopens as the new one. That path is not covered by any automated test (the update test uses an unsigned build and a local feed).

### Private version-tag consumers

A version tag starts the public container/GitHub Release workflow only. It does
**not** copy Apple or Android signing keys, private publication endpoints, or
broad personal API tokens to GitHub Actions. Each release computer instead runs
its existing Claude loop with the prompt printed by:

```sh
python3 scripts/operations/tag_release.py --config <unused> --repository <owner>/<name>
```

The loop calls `scripts/operations/tag_release.py --once` for every private
platform configuration installed at `~/.config/persona/tag-release/<platform>.json`
(0600). It does not run an Agent or build on every poll: the coordinator skips
a completed platform/tag pair and invokes an optional per-tag Agent only for a
new one. The same prompt file also describes the platform-specific publication
and receipt contract (`scripts/operations/tag_release_prompt.md`). Configure the
loop's own normal scheduler on each machine; do not start a second scheduler
for the same platform.

A private configuration has only these fields, with no secrets:

```json
{
  "repo": "/absolute/path/to/shared/repository",
  "worktree": "/absolute/path/to/dedicated/detached/worktree",
  "state_dir": "/absolute/path/to/private/platform-state",
  "repository": "<owner>/<name>",
  "platform": "android",
  "command": ["/absolute/path/to/existing/platform-publisher", "--source", "{commit}", "--version", "{version}"],
  "receipt": "/absolute/path/to/private/platform-state/artifacts.json",
  "agent": ["claude", "--print", "--permission-prompts", "host", "{tag}"]
}
```

- `command` is required and is an argv array, never a shell string. Allowed
  placeholders are `{repo}`, `{worktree}`, `{state_dir}`, `{tag}`, `{commit}`,
  and `{version}`. The coordinator runs it only after checking the immutable
  remote tag and preparing its exact detached worktree. The command reuses the
  platform's existing signing, release lock, and publication receipts.
- The optional `agent` argv array additionally accepts `{prompt_file}` and
  `{log_file}`. Its prompt and tag/commit environment let the local Claude loop
  select already-supported platform steps, not discover or distribute secrets.
  The coordinator retains its exclusive lock across the Agent and the command.
- The private command writes the receipt only after its platform verification
  succeeds. The receipt has the exact `commit`, `store_verified: true`, and an
  `assets` list of absolute regular files with safe names and SHA-256 digests.
  An empty list is valid only for verified store-only publication such as
  TestFlight; it never substitutes an unsigned APK or development archive.
- `ios` uses the existing resumable TestFlight action and verifies its exact
  commit's build is VALID and in Internal. The embedded Watch app shares that
  archive. The frozen native Mac app is not restarted by a generic release;
  desktop uses the web artifact and Persona Link.
- `android` runs `scripts/release/android_tag_release.py` with the existing
  signed-APK pipeline's private configuration (`android_release_worker.py`;
  "Android on version tags" below). If that configuration is absent, the
  platform is blocked and reported as such, not silently omitted. A signed APK
  must pass package, version, source and certificate checks before upload.
- `hub` uses `publish.sh`, its publication lock, and live download verification;
  `web` is carried by that verified artifact. GitHub asset upload does not
  independently prove a production deployment or TestFlight processing is done.
- The coordinator never creates or moves a tag. It requires the corresponding
  public (not draft) GitHub Release to exist, then uploads verified assets and
  compares their remote SHA-256 digests. A failed platform does not complete
  that platform's checkpoint, and does not claim that another platform passed.
- A receipt that carries `latest.json` (the hub's) is checked before anything
  is uploaded. It must list only files in the receipt, at their sizes and
  sha256s. Every binary must be its build, and this machine's `personad
  version -v` must say its version and build. Otherwise the platform fails
  (#4522: v0.2.1's manifest named another build than its binaries). Every
  `personad` must contain the exact GitHub update base used by the Release's
  `install.sh`, and the installer's own `DL` default is checked too;
  `publish.sh` renders that installer from the same base it compiles into the
  binaries. Tagged builds use the Release's UTC `createdAt` minute through
  `PERSONA_RELEASE_BUILD`, so CI and local verification share one build identity
  even for backfills and retries. When Darwin signing is enabled in CI, the coordinator waits for that job, downloads its staging
  `ci-latest.json` and `ci-SHA256SUMS` with digest checks, and non-Windows artifacts, and merges them with the locally
  verified Windows archives and reports into the final manifest and checksums;
  a skipped job keeps the private build, and a failed job stops publication. The downloads go up first; `VERSION`, `SHA256SUMS`, `android.json`
  and then `latest.json` go last, each on its own, so a hub reading
  `releases/latest/download` never sees a manifest whose files are not there
  yet. After remote digest verification, stable hub publication fetches the
  immutable tag's `latest.json`, requires the tag's version and build, and only
  then marks the Release Latest. A backfill of an older stable tag does not
  move Latest backward. Prereleases and non-hub receipts do not move the
  Latest pointer. The uploaded checkpoint retains the exact receipt and asset
  digests, so a feed or promotion retry reuses it and reruns only remote
  verification and promotion instead of rebuilding conflicting assets. Every
  resume repeats the same deterministic receipt validation before upload. The
  primary release-PR pipeline calls `scripts/operations/stable_release.py` after
  independently downloading every uploaded artifact: it verifies the immutable
  tag manifest, refuses to move Latest to an older stable tag, and requires the
  final `isLatest` readback before declaring publication complete.
- A receipt that carries `android.json` (the Android platform's) must ship the
  APK it names, at its size and sha256, for the tag's version.
- Windows CLI bundles (`Persona-<version>-windows-<arch>.zip`) and each current
  `persona-link-windows-<arch>.exe.gz`, `personad-windows-<arch>.exe.gz` and
  `persona-node-windows-<arch>.exe.gz` require native verification before upload (#8338).
  On Windows of that architecture, run `python scripts/operations/windows_release.py --archive
  <archive.zip-or.exe.gz> --version <version> --build <build>` for each artifact. It runs the actual
  `version`, `version -v`, and for `personad` / `persona-node` `node-update-check` with time limits,
  without installing or starting services. Link's native report cannot stand in for a node's.
  Success writes `<artifact-stem>.verification.json` beside the artifact; failure removes a previous
  report. Include the artifact, report and `BUILD.json` or `latest.json` in the private artifact
  receipt. Node programs also require the receipt's `latest.json` to list their exact platform, name
  and compressed and unpacked hashes. The coordinator checks the requested version, build, command
  results, PE architecture, archive digest and executable digests before any upload.
  This report is output from the trusted release pipeline, preventing stale or mismatched verification;
  it is not a signed attestation against a malicious publisher, nor an Authenticode signature.
  Cross-compilation or a matching build number alone does not prove the Windows programs report the
  intended version. A missing Windows runner is an explicit publication prerequisite, not a passed
  or skipped check.

For the first release, merge this consumer's PR before pushing `v0.1.0` from
the merged `main` commit. The public workflow creates the Release and image;
the separately configured private loops then publish their platforms and
attach verified artifacts. Inspect the Release assets and every platform's
actual store/publication receipt before declaring the whole release complete.

### Android on version tags

The `android` tag-release configuration runs the tag's own publisher, with the main-branch worker's private
configuration, so an APK from a Release and one a hub got from the main branch share their package, certificate and
version codes and update each other:

```json
"command": ["python3", "{worktree}/scripts/release/android_tag_release.py",
            "--config", "/absolute/path/to/android-release-worker.json",
            "--worktree", "{worktree}", "--commit", "{commit}", "--version", "{version}",
            "--receipt", "/absolute/path/to/private/platform-state/artifacts.json"]
```

- It builds the tag's commit with the worker's tests, R8 check and version code (`version_base` + first-parent commit
  count; the same commit gets the same code in both lanes), named with the release version (`0.3.0`) instead of the
  main branch's `0.1.<code>-<commit>`. Runs live in the worker's `state_directory` under `tags/<version>`, apart from
  the main branch's `runs/`.
- It signs with the worker's `signing_queue` (the signing host, `android_signing_worker.py`) when the configuration has
  one, else with `local_signing` on this machine: `{"keystore", "alias", "password_command", "key_password_command"?,
  "sdk_dir"?}`, the signing host's own fields. With neither it stops with a prerequisite error. It holds the worker's
  `worker.lock` while building and signing (the queue takes one request at a time), waiting up to
  `lock_timeout_seconds` (default 7200) for a main-branch cycle to finish.
- `aapt2` and `apksigner` then read the signed APK itself (package, version code and name, certificate against
  `certificate_sha256`); only then does it write the receipt, with two assets: `Persona-<version>-android.apk` and
  `android.json`. The SDK tools come from `build_environment` (`ANDROID_HOME`), as the build's do.
- `android.json` is the hub's `apps/android/latest.json` (what `publish_android_apk.py` writes) plus the release
  version and the APK's file name beside it:

  ```json
  {"version": "0.3.0", "name": "Persona-0.3.0-android.apk", "package_name": "<application id>",
   "version_code": 105432, "version_name": "0.3.0", "min_sdk": 29, "certificate_sha256": "<64 hex>",
   "sha256": "<64 hex>", "size": 19476047, "source_sha": "<commit>", "notes": ""}
  ```

  A hub reads it beside `latest.json` to offer the APK to its phones (#5563). A prerelease tag's APK is attached to
  its Release, but `releases/latest/download` never points at a prerelease.

### Android build commit

Settings › About shows the commit an Android APK was built from ([android.md](../android/android.md) "Build commit"). Release
builds pass it explicitly: `android_release_worker.py` adds `-PpersonaBuildCommit=<sha>` with the full sha it checked out (the
same value as the receipt's `source_sha`, for main-branch and version-tag builds), and `scripts/app/android.sh release` passes
this repository's `HEAD`. Version names, version codes, signing and R8 are unchanged.

- **Receipt reuse**: `build.json` records `build_commit` (the full sha). A run directory whose `unsigned.apk` receipt has no
  `build_commit`, or one that is not the source sha, is built again; an `unsigned.apk` made before this metadata existed is never
  published, and nobody has to delete the directory by hand.
- **Check before keeping the APK**: after `:app:assembleRelease` and the R8 check, and before `unsigned.apk` is copied,
  `scripts/release/check_android_build_commit.py` reads `persona_build_commit` with `aapt2 dump resources` and fails unless it equals
  the source sha. A build without the injection, with a wrong value, or one that left an old APK behind stops there and writes no receipt.

## Android publisher repository migration and status retries

`scripts/release/android_release_worker.py` uses a private configuration file. Its `repository`, `git_url`, `branch` and
dedicated `checkout` must all identify the intended source repository. Checkpoints and build receipts carry the repository identity;
an old checkpoint without that identity is rejected rather than silently attributed to a different repository.

`report_commit_status` uses the same opt-in policy as the Hub worker: missing or `false` disables every GitHub
status write; only the JSON boolean `true` enables `release/android` pending/success/failure reporting. Local
logs, `runs`, checkpoints and Hub APK metadata are still written. No historical GitHub status is changed.

When moving to rewritten history, pause the existing publisher, retain its evidence and use a separate `state_directory` for the new
repository. Keep the existing package, signing certificate and signing entry. Never post an old commit's status to the new repository
or treat an old verified APK as a build of the new source. Compare migrated file content when commit IDs have changed.

The release APK is shrunk and optimized by R8 without obfuscation (docs/android/android.md "Release shrinking"). The worker
removes `app/build/outputs/apk/release` and `mapping/release` before the build, so nothing an earlier build left is checked or
signed. After `:app:assembleRelease` and before the APK is copied for signing, it runs `scripts/release/check_android_shrink.py`
on it and on R8's output in `app/build/outputs/mapping/release`; any class, constructor or method that Android or the app
reaches only by name and that R8 removed, and any exception, the app's or a library's, that R8 merged into another class,
fails the build, so nothing is signed or published. It also fails when any method grows past 10,000 dex code units,
which ART would then leave interpreted, the libraries' included because R8 merges classes across packages; a method in
`scripts/release/known_oversized_methods.txt` (one full signature per line) only warns while it stays at or below the
size recorded there, and one between 9,000 and 10,000 warns as the next split's deadline
(docs/android/android.md "Release shrinking"). It then keeps R8's `mapping.txt` gzipped as
`runs/<source>/mapping-<unsigned sha256>.txt.gz` (about 12 MB), which `retrace` needs for frames from optimized code. Before
the first shrunk or optimized release, install it on a phone and
open pairing, a chat with a formula, the Inbox, Settings, a notification action, the home-screen widget and the Quick Settings
tile; a crash there is fixed with a keep rule in `android/app/proguard-rules.pro` (or rolled back with `isMinifyEnabled = false`
and the test line that pins it, docs/android/android.md "Release shrinking").

Versions are `version_base + first-parent commit count`; `version_base` defaults to 100000. Before enabling the new lane, choose a
fixed nonnegative base so its first version exceeds the currently published APK. Keep that base stable thereafter. The worker rejects
history rewrites and version regressions within the lane; the atomic Hub publisher also rejects stale versions across lanes.

After publishing and completing the configured full online verifier, the worker durably records `verified.json`, including the source,
repository, package, version, signed APK hash and certificate. Only then does it post the optional GitHub success status and advance `published.json`.
If that status write fails, the next cycle for the same source revalidates the local artifact and retries only the status/checkpoint;
it does not build, sign, publish or download again. The receipt proves the completed verification, not current availability after an
unrelated deployment. A newer source follows the normal full pipeline. Preserve failure logs and report publication, verification,
status reporting and handset installation separately. Legacy releases without a verified receipt are not automatically imported.

