# Mac acceptance manual for the desktop app

This is the checklist for verifying the Persona desktop app **on a real Mac, by a person**, for
[#6770](https://github.com/nwverse/persona/issues/6770). The automated smoke and hosted-runner evidence
(`desktop.md`, "Acceptance evidence") stop at the main-process boundary; this manual is the missing physical-Mac
half. Work top to bottom. Every step says what to do, what should happen, what to capture, and which #6770
acceptance criterion it serves.

**Scope and honesty rules**

- Signing and notarization are **skipped**: there is no Developer ID certificate yet. You will test the **unsigned
  development package** from CI. Anything an unsigned package cannot prove (signed installation, Gatekeeper's
  notarized verdict, a published auto-update) stays unverified — record it, do not work around it.
- Windows is deferred. Do not infer Windows results from this manual.
- Use placeholders everywhere in your notes: `<hub address>`, `me@example.com`. Never paste real tokens, pairing
  links, or personal messages into an issue or PR. Use a test hub with **synthetic content only**: no real
  conversations, real machine names, internal addresses, real Inbox items or real approval details. **Review and
  redact every screenshot before uploading** — the privacy scan cannot read text inside images.
- If a step fails, capture the failure exactly as it appears (screenshot + what you did), and keep going to the
  next section if the app still runs. A failed step is evidence, not something to hide.
- Two known blockers to watch for, both to **record**, never to route around:
  1. **Packaged first-run setup** (draft PR #7561): the clean-profile `setup.html` may fail to load from the asar
     (`ERR_FILE_NOT_FOUND`). If the very first window is an error page instead of "Welcome to Persona", that is
     this blocker — screenshot it and stop the install part there.
  2. **Unsigned full-hub load**: on an unsigned/ad-hoc-signed build the hub window may never finish loading,
     because Electron's cookie encryption (like `safeStorage`) needs a consistent valid signature on macOS
     (draft PR #7561, "macOS root cause narrowed"). If sign-in reaches a window that never loads, that is this
     **signing blocker**. Do not disable fuses, strip signatures, or switch to a different app ID to make it
     "pass".

Suggested evidence layout, one folder per run:

```text
mac-acceptance-<date>/
  00-environment.txt      # macOS version, Mac model, the CI run URL you downloaded from
  01-install/ … 09-notch-island/
```

Record in `00-environment.txt`: macOS version, chip (Apple silicon / Intel), the exact artifact file names, and
whether this Mac has ever had Persona or Persona Link installed before (a clean Mac and an upgrade are different
runs; prefer a Mac that has never had it for the first run).

## 1. Get the package and get past Gatekeeper

The development workflow builds unsigned packages on a macOS runner and uploads them as the
`persona-desktop-macos-unsigned` artifact (the universal DMG and ZIP). Signed packages do not exist yet.

| Step | Do | Expected | Capture | #6770 item |
| --- | --- | --- | --- | --- |
| 1.1 | Download `persona-desktop-macos-unsigned` from a recent Desktop CI run (Actions › the run › Artifacts), or with `gh run download <run-id> -n persona-desktop-macos-unsigned -D <new empty folder>`. Unzip it. | A `.dmg` and a `.zip`, universal (x86_64 + arm64). | `00-environment.txt`: run URL, file names, byte sizes. | Install on macOS |
| 1.2 | Open the DMG, drag **Persona** to `/Applications`. First launch: double-click it. | macOS refuses: a dialog that the app is from an unidentified developer / "cannot be opened because Apple cannot check it for malicious software". This is expected for an unsigned package. | Screenshot of the exact dialog wording. | Install on macOS |
| 1.3 | Right-click (Control-click) the app in Finder › **Open**, then **Open** in the dialog. | The same warning now offers an **Open** button; the app launches. If it still will not open, in Terminal run `xattr -dr com.apple.quarantine "/Applications/Persona.app"` on the downloaded copy and try 1.3 again. Record which route you needed. | Screenshot of the first window you see (the local setup page, "Welcome to Persona"). | Install on macOS |

What this proves: the app installs and starts on a real Mac. What it does not: anything about signed distribution
or notarization. If 1.3 shows an immediate crash or a blank window, stop and file that with the screenshot.

## 2. First run and sign-in (root path and sub-path)

Use a hub you can create test data on, not a production hub full of private conversations. You need its address
(`<hub address>`, for example `https://hub.example.com`) and a way to sign in (the existing web sign-in: pairing
link or account, whichever your hub offers). The second part needs a hub served under a path prefix, for example
`https://hub.example.com/persona` — if you have no sub-path hub, mark that part "not tested" rather than skipping
it silently.

| Step | Do | Expected | Capture | #6770 item |
| --- | --- | --- | --- | --- |
| 2.1 | On the setup page, enter `<hub address>` (root path) and **Continue**. | The hub's own sign-in page loads in the window — the same page a browser shows, no Electron-specific login. | Screenshot of the sign-in page. | Sign in to a root-path hub |
| 2.2 | Sign in as you would in a browser. | You land on the main chat. No blank window, no endless spinner. If the window never loads on this unsigned build, that is the known signing blocker above: screenshot it, note "unsigned full-hub blocked (#7561)", and continue with what still works. | Screenshot of the main chat after sign-in. | Sign in; main flows |
| 2.3 | Use the local options window (Settings › General › **This computer**) to enter a different hub: `<hub address>/persona` (the sub-path one), and **Continue**. This is the required hub-change flow — do not just sign out, which does not exercise it. | The old hub's window closes, a new window opens on the sub-path hub with its own persisted session, and its sign-in page loads. Links and API calls stay under `/persona`. | Screenshots before and after the switch; the sub-path hub's main chat after signing in. | Sign in to a sub-path hub |

**Before sections 4–5, on each hub you sign in to** (and again after the hub switch in 2.3): in the app go to
**Settings › Notifications** and turn on **Show system notifications** and **When something needs you**. Both are
off by default; without them no system notification is sent and no `register_desktop` happens, so the next two
sections would test nothing. The Electron permission prompt is owned by the shell; you may still need to allow
Persona in System Settings › Notifications.

## 3. Main flows

Still signed in. These are the existing web screens; you are checking they work inside the shell, not re-testing
the web app itself.

| Step | Do | Expected | Capture | #6770 item |
| --- | --- | --- | --- | --- |
| 3.1 | Send a message in the main chat and wait for the reply. | The reply streams in; scrolling, markdown and code blocks render as on the web. | One screenshot of the conversation. | Chat works |
| 3.2 | Open **Agents**, start or open an Agent session on one of your computers. | The session opens, its output streams, and its status is visible. | Screenshot of the Agents list and one open session. | Agents work |
| 3.3 | Open **Inbox** and the to-dos. | Items load; opening one works. | One screenshot each. | Inbox and to-dos work |
| 3.4 | Make an Agent ask for approval (run something that needs a tool approval or an answer). | A waiting card appears with working buttons; answering it lets the Agent continue. | Screenshot of the card before answering; note what happened after. | Approval cards work |

## 4. Window closed: notification, click-through, Dock badge

| Step | Do | Expected | Capture | #6770 item |
| --- | --- | --- | --- | --- |
| 4.1 | Close the window (Cmd-W or the red light). The app keeps running in the menu bar / Dock. | The app does not quit; the tray icon stays. | — | Close-to-background |
| 4.2 | From another device, make a **new** session need you (a fresh approval card or question — not one you already used). | A **macOS system notification** appears with the session's title. The Dock icon shows a badge with the same Needs-you count the web shows. | Screenshot of the notification as delivered by macOS, and of the Dock badge. | Closed-window notification + badge |
| 4.3 | Click the system notification. | The Persona window comes forward **on that session**, not on a home page. | Screenshot of the window it opened. | Click opens the session |
| 4.4 | If macOS never shows notifications: open System Settings › Notifications › Persona and allow them, then repeat 4.2 **with a fresh, distinct item**. | The permission prompt or setting is honored. Exactly **one** notification per item, and its click opens the session in Persona. If a native display failed, the web falls back to an in-page notification: a duplicate notice, or an item that produced **no** native OS notification at all, is a **failure to record**, not a pass. | Note whether you had to grant permission manually, and whether any duplicate or fallback (non-native) notice appeared. | Notification permission |

What this proves that CI cannot: the OS notification center actually displays and delivers a click, and the Dock
badge is really drawn. The smoke only reads the count the shell received.

## 5. Lock / unlock, `register_desktop`, and Phone Muted

You need your iPhone with the Persona app, signed in to the same hub. Turn on **Phone Muted**: with the desktop
app active, the phone shows a **"Persona is signed in on your Mac"** banner over the chat list — tap it and enable
**Phone Muted** in that sheet. (It is not in the phone's general Settings.) It suppresses "Needs you" pushes while
a desktop is active. Confirm section 2's notification switches are on, or no desktop registration happens.

| Step | Do | Expected | Capture | #6770 item |
| --- | --- | --- | --- | --- |
| 5.1 | With the desktop app open and unlocked, make a session need you. | The Mac notifies; the phone stays silent (Phone Muted). | Note the time; screenshot of the Mac notification. | `register_desktop`; Phone Muted while active |
| 5.2 | Lock the Mac (Ctrl-Cmd-Q), wait ~30 s, make another session need you. | The phone **does** get its push now: a locked desktop withdraws its registration instead of swallowing alerts. | Screenshot of the phone's notification; note the delay. | Withdrawal on lock |
| 5.3 | Unlock the Mac, wait ~30 s, make another session need you. | The Mac notifies again and the phone is muted again. | Note what happened on both devices. | Re-registration on unlock |

## 6. Launch at login, tray, updates

| Step | Do | Expected | Capture | #6770 item |
| --- | --- | --- | --- | --- |
| 6.1 | In the local options window (first-run page, or Settings › General › **This computer** after sign-in), turn on **Open Persona when you sign in to this computer**. Log out of macOS and back in. | Persona starts at login. If macOS asks for approval in System Settings › General › Login Items, the setting must say so rather than claim success; approve it there and log out/in once more. | Screenshot of the setting, and of the app running after login. | Launch at login |
| 6.2 | Click the menu-bar tray icon. | A menu with **Open Persona**, **Check for updates**, **Quit Persona**. Open brings the window back; Quit really exits (no icon left). | Screenshot of the menu. | Tray |
| 6.3 | Choose **Check for updates**. | It turns into "Checking for updates…", then a result line, then back to **Check for updates**. Record exactly what it says. This unsigned CI artifact has **no update feed** (`electron-builder.unsigned.yml` sets `publish: null`), so the check is expected to **fail** while loading the missing update configuration — that failure is the correct result here, not a bug. Whether a published feed, a download, or macOS's install refusal works is **not** verifiable from this package. | Screenshot of the menu after the check. | Update UI (not a published update) |

A real published auto-update (old signed version → new version after restart) is **not testable** until signed
releases exist. Leave that row open in #6770.

## 7. Persona Link: pairing, reconnect, service management

Link is a separate install for a Mac whose Agents the hub should reach. If this Mac already runs Link, use that;
otherwise install it first with the [independent Link install guide](../operations/persona-link-install.md) (source build; ad-hoc signed). Do not
expect the desktop app to install Link for you.

**Prerequisites for 7.2** (the desktop artifact alone is not enough): macOS 15 or later, the **full Xcode app**
(not just command-line tools), Go, and a repository checkout on this Mac. `mac-link.sh` also needs a bundle-id
prefix: set `mac.link_bundle_id` (or `app.bundle_id`) in `~/.config/persona/config.json`, or export
`PERSONA_LINK_BUNDLE_ID`. Without these the install stops and says what to set.

| Step | Do | Expected | Capture | #6770 item |
| --- | --- | --- | --- | --- |
| 7.1 | With Link **not installed** (or before installing): open the local options window. | The Persona Link section says Link is not installed and points to the separate guide; pairing/reconnect buttons are disabled. Chat works without it. | Screenshot of that section. | Independent installs |
| 7.2 | Install Link (`./scripts/app/mac-link.sh install`). Reopen the local options window or **Refresh**. | Its redacted status appears: connection state and names only — no `link.json`, no token, no internal addresses. | Screenshot. | Link status in the app |
| 7.3 | In the signed-in web app, **Machines › Add computer**, copy the pairing link, then in the desktop's Link section choose **Pair this computer** and paste it. Confirm on the web screen. | Pairing succeeds; the computer shows up on the hub's Machines page. | Screenshots of the pairing flow and the Machines entry. | Pairing from the app |
| 7.4 | Disconnect the connector from the hub side (stop the hub's connection, or briefly turn off this Mac's network), then use **Reconnect** in the app once the network is back. | The connector reconnects immediately on your request, without you editing `link.json` or running `persona-link reconnect` in a shell. | Before/after status screenshots. | Reconnect from the app |
| 7.5 | Check the background service: quit the desktop app entirely, then on the hub look at this Mac's node. | Link and the resident node keep running with the desktop app closed. Verify **both** services: `~/.persona/bin/persona-link status` reports the connector's `com.persona.link` agent, and `~/.persona/bin/persona-link node status` reports the resident node's `com.persona.node` agent (one command does not show both). | Screenshots of both status outputs, with any real names redacted. | Service independent of the app |

## 8. Screen Recording permission

Persona Screen (part of the Link package, not of the Electron app) owns this permission. Two prerequisites before
**View screen** exists on a clean Mac: enable **Computer control** (or Browser) for this Mac on its Machines page,
and turn on **System Settings › General › Sharing › Screen Sharing** on the Mac itself. Without both, the entry is
absent and you cannot reach the permission prompt.

| Step | Do | Expected | Capture | #6770 item |
| --- | --- | --- | --- | --- |
| 8.1 | In the web app open **Machines › this Mac**, and click **View screen** at the top of the page. The live screen uses macOS **Screen Sharing** (VNC): if asked, enter this Mac's own screen-sharing account/password (the Mac's login). Once the video-capable client signs in, macOS asks for **Screen Recording** for Persona Screen. Allow it in System Settings › Privacy & Security › Screen Recording, then choose **Back** and open **View screen** again. | The permission prompt names Persona Screen. | Screenshot of the permission prompt. | Screen Recording permission |
| 8.2 | After granting, confirm the **video** path actually works: in the live view switch **Picture › Video** (or open Connection stats). | Actual **video** plays, not just the VNC picture. A working VNC picture alone does **not** prove Persona Screen — VNC is the fallback when video permission is missing. If only VNC appears, the video proof has **not** passed: check Connection stats and the fallback reason — if it says video is not allowed, the grant did not take effect; on a multi-display Mac, select one display first (video can also stay off over plain HTTP, without a WebCodecs decoder, or on a busy/locked stream). | Screenshot of the live **video** (synthetic content only) or the Connection stats video state. | Screen Recording works |
| 8.3 | Rebuild/reinstall Link without changing Persona Screen's sources, and start the live screen again. | The grant survives (the helper is reused byte-identically). If you did change its sources or signing, macOS asks once more — that is expected, note it. | Note whether you were asked again. | Permission persistence |

## 9. The notch island (#7669)

Off by default and only on a Mac (`desktop.md`, "Island at the MacBook notch"). On a MacBook with a notch the pill sits
around it; on a Mac without one (or with its lid closed) the same pill without the middle sits at the top center of
the display. Keep a few synthetic sessions on the test hub that need you or are working.

| Step | Do | Expected | Capture | #7669 item |
| --- | --- | --- | --- | --- |
| 9.1 | Settings › Notifications: turn on **Show at the MacBook notch**. | A black pill appears around the notch at once, its top corners square against the top of the screen (no menu bar showing at them): the most urgent category with its symbol and count on the left (Needs you red, then Working amber, then Unread green), the next one on the right; the same counts as the Dock badge and the Overview. With nothing to count it stays hidden. | Screenshot of the menu bar around the notch (crop the rest of the menu bar). | Opt-in; counts |
| 9.2 | Move the pointer over the pill, then away. | It grows downward at the pill's width, covering no more of the menu bar than the pill, into at most five agents: the ones that need you, then the working ones, newest first, each with its title, state and the line its Agents-list row shows. It closes a moment after the pointer leaves, and never takes focus from the app in front. | Screenshot of the open list. | The list |
| 9.3 | Click a row. | The Persona window comes forward on that session, as a notification's click brings it. | Screenshot of the window it opened. | Click opens the session |
| 9.4 | With the island on, look at the Dock, press Cmd-Tab, bring Persona to the front. | Persona keeps its Dock icon and Needs-you badge, is in the app switcher and has its menu bar. | Screenshot of the Dock and the switcher. | The app stays in the Dock |
| 9.5 | Put another app in full screen (its green button) and stay in its Space; then switch Spaces. | The pill stays at the notch over the full-screen app, and on every Space. | Screenshot over the full-screen app. | Over full-screen apps |
| 9.6 | Switch System Settings › Appearance between Light and Dark, and Persona's own theme too. | The island stays dark in both, as the notch is; the Settings row follows Persona's theme. | The Settings row in both. | Light and dark |
| 9.7 | Sign out: the sidebar's assistant menu › **Disconnect this browser…**. | The pill goes with the account: nothing of it stays at the notch over the sign-in page. | Screenshot of the notch after signing out. | Signing out clears it |
| 9.8 | Turn the switch off. Turn it on again, quit Persona and open it. | Off hides the pill at once. On survives the restart: the pill is back once signed in. | Note both results. | Kept for this computer |

What this proves that CI cannot: the pill is really drawn at the notch, over the menu bar and full-screen apps, and a
real pointer opens it and clicks through. The acceptance smoke checks only that turning it on keeps the app in the
Dock, and what the shell keeps across a hub change.

## 10. What stays open after this manual

- **Signed + notarized installation** and everything Gatekeeper does differently for it.
- **A published auto-update** from a real GitHub Release, including macOS's Squirrel.Mac accepting the new
  signature.
- **Windows**: every equivalent step, on a real Windows PC.
- The packaged **first-run** gate is covered by draft PR #7561 and its owner. The two blockers named at the top
  (clean-profile setup not loading; the unsigned full-hub cookie/signing block) are that same boundary, not new
  bugs to route around.

Report results on #6770 with the evidence folder's screenshots, naming the exact package artifact and run you
tested. Mark each criterion you verified; leave the rest explicitly unverified.
