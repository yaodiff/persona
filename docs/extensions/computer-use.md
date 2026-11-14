# Computer control (Cua Driver / LCU)

The Persona assistant controls the user's Mac: view windows and screens, click, type, take screenshots. Implemented as the hub's **built-in `cua` extension** ([extensions.md](extensions.md)), **off by default**, then enabled **per machine** after the extension is on.
There are two backends, selected by Control Backend in extension settings:

| Backend | What it is | When to use it |
| --- | --- | --- |
| **Cua Driver** (default) | `cua-driver` from [trycua/cua](https://github.com/trycua/cua) (MIT, installed externally, not vendored) | Cua Driver is installed and has Accessibility and Screen Recording permission |
| **LCU** | [amontlabs/lcu](https://github.com/amontlabs/lcu) ([lcu.amontlabs.com](https://lcu.amontlabs.com)). It doesn't implement input and screenshots itself; it exposes the Codex computer-use runtime in the official ChatGPT desktop app as stdio MCP (command `lcu`) | The official ChatGPT desktop app and LCU are already installed locally |
| **Auto** | Uses LCU if LCU is installed and ChatGPT (or the Node recorded by the installer) is available, otherwise Cua Driver | Let each Mac choose for itself |

The assistant's tools remain `computer_state` / `computer_run`. LCU translates them into the runtime's `js` (`cua.getState()`, `cua.getApp(...)`). Confirmation doesn't use LCU's Claude / Codex approval UI; see "LCU confirmation" below.

Click sounds and the Agent cursor only apply to Cua Driver. LCU has no separate daemon: the node starts its process on the first call and keeps it alive during the chat, preserving the runtime's "allow for this session" decisions.

| Area | Issue | Status |
| --- | --- | --- |
| Integration: capability discovery, assistant `computer_state` / `computer_run`, screenshots | #1182 | "Integration" in this doc |
| One-tap installation on the phone, permission guidance | #1183 | "One-tap installation" in this doc |
| Live view: watch the Agent operate the computer on phone and web, take over if needed | #1184 | "Live view" in this doc |
| Lock screen, prevent sleep, per-App approval, click sounds (reference: Codex desktop app) | #1507 | Design decided, being implemented in order: "Locking, preventing sleep and per-App approval" in this doc |

## Switches

- **`cua` extension**: a declaration-only extension shipped with personad (no process), contributing the `computer` extension point. Toggle it in **Settings › Extensions** (#1191),
  or run `personad extension enable cua` on the hub machine (old `personad plugin …` commands still work). Off by default.
- **Per machine**: once the extension is on, each Mac's machine page gains Control Computer, with **Let the Assistant Control This Computer**
  (`PUT /api/v1/machines/{mid}/computer`), stored in `machines` in extension config. Controllable Computers on the extension page is the same value: Macs only, one switch each (name, online status, Cua status), effective immediately (`format: machine`, [extensions.md](extensions.md)).
- **Confirm before acting**: extension config `confirm` (Confirm on Phone Before Acting), off by default; see "Write operations".
- **From the extension page**: **Set Up** beside a Mac opens that machine's existing Control Computer section. Install Cua Driver there, grant **Accessibility** and **Screen & System Audio Recording** (Screen Recording on older macOS), then **Check Again**. These are macOS permissions for Cua Driver, not a Persona hub switch.

**With the extension off**:
- The hub never asks any node about cua-driver (the node never probes on its own).
- Machines have no `computer` field; nothing appears on their machine pages.
- The assistant has no `computer_*` tools.

**With the extension on but a machine off**: the hub still asks its node (at connection and extension changes). That Mac's `computer` contains its status, but `enabled` is false: the machine page only has a switch; its row on the extension page shows Cua status ("Online · Cua not installed"). Assistant tools appear only once at least one machine is enabled, and can only operate enabled machines.

Turning off the extension or a machine removes the tools and entry points; Cua Driver or LCU installed on that Mac is left untouched.

## LCU confirmation

When LCU's runtime actually touches an App (and a website origin when Chrome is open), it asks the client through MCP `elicitation/create`: allow once, allow for this session, always allow, deny; high-risk actions carry its own warning, and websites offer allow or deny. Official adapters hand these to Claude, Codex and Pi's own interfaces.

Persona doesn't register those adapters. The node handles this elicitation itself as the MCP client, and the hub's `computer.elicit` turns it into the **existing** phone and web approval cards (the same `permission` cards used by Ask Before First Operating an App and Confirm on Phone Before Acting; `question` when there are more options). For an MCP call that opted into wait updates, the existing `computer.call` and `computer.elicit` requests carry only the node's opaque UUID `call_id`, and only when the target node advertises `mcp_wait_v1`. The manager keeps the live call's originating machine and wait state, so the nested card joins that same reporter even when the tool operates another computer; no machine-routing field crosses either request. An unknown or malformed id gets no reporter. An older target receives no id: the manager reports `unknown` (`Still waiting`) for that delegated RPC, then restores the previous state rather than implying that the target can correlate its approval:

| Runtime request | Card |
| --- | --- |
| Allow once only, or also one of "this session" / "always" | Permission card: Allow / Always Allow (text specifies this session or permanently) / Deny |
| Both "this session" and "always" | Question card: Allow once, allow for this session, always allow, deny. A permission card only has one Always Allow and can't represent two durations; the wait kind is still `approval` |
| Website origin, form without fields | Permission card: Allow / Deny. Doesn't invent "always allow" when the runtime didn't offer it |
| Open a URL | Permission card includes the URL, allow or deny |
| One enum field | Question card, with the enum values plus deny; the wait kind is `answer` |
| Simple fields to fill in | Permission card input fields (phone can fill them; the web card can't, so missing required values never count as approval) |
| Unknown structure | Still a card; even tapping Allow returns cancellation, never pretends consent |

No answer, dismissing the card, timeout, or the assistant answering through `interaction_answer` all return `cancel`. The runtime considers it unapproved. The model never sees "allowed" and cannot approve itself.

Persona's own Ask Before First Operating an App still asks first (when the App can be identified). Any later runtime prompt also needs a human answer on the card; approval of the previous card doesn't auto-approve it.

Installation uses `scripts/install.sh --runtime-only --yes` from the release package: installs only the runtime and records ChatGPT's bundled Node; does **not** run `lcu setup --agent`, which registers approvals in Claude or Codex. The user must install the ChatGPT desktop app first (macOS default `/Applications/ChatGPT.app`); Persona neither downloads it nor signs in. On first use, Accessibility and Screen Recording permissions belong to ChatGPT itself, and must be allowed on that Mac. The installed command is usually `~/.local/share/lcu/current/bin/lcu`.

## Two paths

| | Assistant (Persona main chat, etc.) | Agent on a computer (Claude Code, Codex…) |
| --- | --- | --- |
| How it's called | hub MCP tools `computer_state` / `computer_run` → hub → node `computer.call` → Cua Driver `cua-driver call`, or LCU stdio MCP (`js`) | Register Cua Driver's MCP directly in the Agent on that computer: `cua-driver mcp-config --client claude --no-skills` prints config, `cua-driver mcp` maintains a session. LCU is not registered in the Agent this way, to avoid approvals appearing in the Agent's own UI |
| Which computers | Any Mac enabled in the extension | Only the computer the Agent runs on |
| Switches, confirmation, logs | This extension's | Cua Driver's and the Agent's own tool permissions |

The Agent path doesn't pass through the hub and is outside this extension's control.

## Integration (#1182)

### Capability discovery

The hub asks the node for `computer.status`. It only asks when the extension is enabled for that machine, at these times:
- on connection;
- when the switch is enabled;
- when the machine page opens (`GET …/computer`);
- when the extension switch or config changes.

The node's probes:

| Check | How |
| --- | --- |
| Installed | `cua-driver` in PATH, or `~/.local/bin/cua-driver` (installer default), or `/Applications/CuaDriver.app/Contents/MacOS/`. A resident node (launchd) has a short PATH, so the latter two are searched explicitly |
| Version | `cua-driver --version` |
| Daemon | `cua-driver status` ("daemon is running") |
| Permissions | `accessibility` and `screen_recording` from `cua-driver permissions status --json`. These belong to the daemon's own TCC identity (`com.trycua.driver`); unavailable if the daemon isn't running, recorded as `unknown` |
| Desktop session | `IOConsoleUsers` from `ioreg -n Root -d1`: whether this user's session is foreground (`kCGSSessionOnConsoleKey`) and locked (`CGSSessionScreenIsLocked` / `IOConsoleLocked`) → `active` / `locked` / `away` |

The result is `proto.Computer`. `ready` means everything is ready; `note` is user-facing text explaining what's missing and what to do. The machine page shows:
- ready: "Ready to control · Cua Driver 0.38.0";
- not ready: `note`, e.g. "Cua Driver doesn't have Screen Recording permission yet: run cua-driver permissions grant on this Mac and allow it in the window that appears".

**Locked, no desktop session**: Cua Driver relies on Accessibility and Screen Recording, both requiring a logged-in, unlocked desktop.
- When locked, `note` says "This Mac is locked; unlock it to control it".
- Logged out or switched to another user: "This Mac doesn't currently have a desktop logged in as you…".
- A Mac still at the FileVault unlock screen after reboot has no network either, so the machine is simply offline.

In these cases tools still attempt the call, returning Cua Driver's original error to the assistant on failure.

### Call path

The assistant's two tools (descriptions in `server/internal/prompts/mcp/computer/`):

- **`computer_state {machine, app?, pid?, window_id?, query?, screenshot?}`**
  - With `app`: `list_windows` for the app's windows. With one process, `cua.getApp(app)` in `run_script` finds its frontmost window (Cua Driver's own lookup); with several, the manager chooses (below). Then `get_window_state`: accessibility tree (markdown) plus screenshot.
  - With `pid` or `window_id`: that process's best window, or that window.
  - Without any: `get_accessibility_tree` (on-screen Apps and windows) plus `get_desktop_state` (full-screen screenshot).
- **`computer_run {machine, steps | script, app?, pid?, window_id?, observe?, timeout_ms?}`**
  - `steps` → `run_steps`: a sequence of actions, finding targets by role and name or `element_token`; stops on the first failure. `pid` / `window_id` (or the process chosen of an app with several) go to `run_steps` as the default window of steps that name none.
  - `script` → `run_script`: JS with loops and conditions, up to 120 seconds. Not with `pid` / `window_id`.
- **An app with several processes (#7591)** (`extmgr/computer_window.go`): two processes can share an app's name and
  bundle id (the user's browser and another copy of it with its own profile). Cua Driver's own lookup takes whichever it
  finds first, so it once read another copy's small untitled window and the user's own could not be chosen.
  - Each process's best window: a titled ordinary (layer 0) window, then on the current Space, on screen, in front of
    the process's others.
  - Between processes: the one with a titled window, then on the current Space, then the app active now (`list_apps`).
    Window stacking does not decide between processes: the other copy's windows were stacked over the user's.
  - Still more than one: nothing runs, and the result lists the candidates (pid, window_id, title, size, on the
    current Space) for the model to call again with `pid` or `window_id`, asking the user by the titles when unsure.
  - `computer_run`: the call's `app` with several processes is pinned to the chosen pid (not its window, so a dialog
    over it in that process is still found). A step that names such an app itself, or a script that does
    (`cua.getApp` takes no process), is refused with the candidates: a step's own app has Cua Driver choose again.
  - `pid` / `window_id` are checked against the Mac (`list_windows`, `list_apps`) and stand for their app in per-app
    approval and the protected-app checks. A pid of another app than the one named is an error, never a way around
    them. LCU takes neither.
  - The cost: one `list_windows` (about 75 ms on the Mac) per `computer_state` with an app, and per app a run's steps
    or script name.

**Session**: each chat's calls carry the same Cua Driver session name, `persona-<first 8 characters of chat id>`.
- Tested locally (Cua Driver 0.38.0):
  - Without a session name, each `cua-driver call` is a new session, and an `element_token` from the previous call is invalid next time (`stale_element_token`).
  - With the **same** session name, tokens work across calls: read the Clear token once, click it in the next `call`, and Calculator resets.
  - Named sessions expire after 5 minutes idle.
  - So line numbers from `computer_state` can be used in later `computer_run` calls.
- **A session that ended (#7590)**: once Cua Driver ends a session, it refuses every later call under that label
  ("session has ended; … Call start_session …") until `start_session` starts it again. It ends one after 5 minutes idle,
  and when the user presses Stop on the bar it shows on the Mac's screen ("You're in control / Return to agent / Stop").
  The error is the same for both, `get_session` on it is refused too, and Cua Driver keeps no reason (0.38.0, measured).
  The manager tells them apart by its own record of the label (`extmgr/computer_session.go`, in memory):
  | Found ended | Taken for | What happens |
  | --- | --- | --- |
  | Used under 5 minutes ago | The user's Stop (it cannot have idled out) | Nothing runs. The model is told the user stopped it on that Mac, not to call again in this turn, and to ask the user with a sentence in their language (`hub-computer.session_stopped`). Retries keep getting that answer |
  | After that, the owner spoke in the conversation (a message, or an answer to its question; the assistant's `ownerHeard`) | Their go-ahead | `start_session` under the same label, then the call once more |
  | Used 5 minutes ago or more, or never used by a manager that has run that long | Idled out | `start_session`, then the call once more, silently |
  | Never used, by a manager that started under 5 minutes ago and has not heard the owner since | Cannot tell | Treated as the user's Stop |
  - The manager's own label (`persona-apps`, its look at the apps and windows for the protected-app checks) and the
    node's (`persona-gate`, `cua_gate.go`) only read: no cursor, no bar, nothing for the user to stop. They always start
    again (the node does its own), or every check would fail as unreadable and every run would be refused.
  - What it can miss: a Stop after the label's last use, when the manager restarted in between and its next call
    comes 5 minutes or more after that restart with no word from the owner since. That call is taken for an
    idled-out session.
  - The node takes `start_session` only for the call's own label and with no other arguments.
- **Why not keep a `cua-driver mcp` child resident on the node**: a session name already preserves tokens. One `call` per invocation is stateless: node restarts, hub redeployments and two concurrent chats don't affect it. It costs one extra process per call (tens of milliseconds), negligible next to Cua Driver operations (about 0.3 seconds per click, 1.3 seconds to find a window).

**Arguments and tools**:
- Arguments go to `cua-driver call` through stdin: no length limit, and absent from process listings.
- The node accepts only 8 tools: `get_desktop_state`, `get_accessibility_tree`, `get_window_state`, `list_apps`, `list_windows`, `run_script`, `run_steps`, and `start_session` for the call's own label (a session that ended, above). Cua Driver settings, updates, extensions and clipboard don't go through this path.

**If the daemon isn't running**: the node first runs `open -g -b com.trycua.driver`, opening CuaDriver.app in the background through LaunchServices with no window. Measured: starts within 1 second, retaining permissions, then the call proceeds.

### Screenshots

- `cua-driver call --screenshot-out-file` writes the screenshot to a temporary file, so JSON no longer includes base64: the Calculator window result was only about 600 bytes.
- The node reads the image, converts it to JPEG if smaller than PNG (transparent corners padded white), puts it in `image` in the `computer.call` result, then deletes the temporary file.
- Calculator window: 230×408, about 12 KB as JPEG; full-screen images are larger, with Cua Driver limiting the long edge via `max_image_dimension`.
- The hub gives the image to the model as an MCP image block alongside text: the model needs pixels to operate, and MCP images have no "give a link" form.
- **No separate media cache**: once the image is in the assistant transcript, hub pushes to the App already omit image data (`lazy_images`), fetched per item when displayed. The App draws this image under the tool call: "View computer · MacBook · Calculator"; expand it for the screenshot.
- Screenshots aren't logged. Tool-result JSON over 60,000 bytes is truncated at a character boundary, with a hint to narrow it using `query`.

### Write operations

`computer_state` is read-only. `computer_run` may click, type, press keys, open or close Apps.

- **Allowed by default, logged**. One hub log line:
  ```
  [macbook] computer_run for 1a2b3c4d: run_steps [click click press_key], 1.2s, ok
  ```
  Only action names and step counts (byte count only for scripts), not typed text or click targets.
- After enabling **Confirm on Phone Before Acting** (`confirm`) in extension config, each `computer_run` first shows a permission card on the phone: "On MacBook, perform in order: click, click, press_key" or "Run script: <first line>".
  - If not approved (or unanswered for 15 minutes), tell the assistant the user denied it and not to retry.
  - Same cards as `connector_gate` (`hub.approve`).

### Security and privacy

- No real screenshots, window content or names in the repository or tests. Tests use fabricated PNGs and a fake cua-driver script.
- Screenshots from real-device end-to-end tests (below) are only written to a local file chosen by the tester.
- Screenshots live only in the assistant transcript (like photos sent to the assistant), not hub logs or extension audit (`plugin_audit` records only toggles and config changes, not values).
- Cua Driver's own telemetry: disabled during one-tap installation (`cua-driver telemetry disable`, #1183); users decide for manual installations.

## One-tap installation (#1183)

After enabling the extension for a Mac, Control Computer on its machine page offers buttons by status, rather than asking the user to type commands on that Mac:

| Status | Machine page |
| --- | --- |
| Not installed | "Cua Driver isn't installed on this Mac" + Install Cua Driver |
| Installing | Spinner + current step + Cancel |
| Failed | "Installation failed: <last installer lines>" + Reinstall |
| Missing permissions or requesting permission | Step-by-step guidance (#1280): approved permissions checked, missing ones numbered in order; uses the original macOS 26 System Settings names (System Settings › Privacy & Security › Accessibility; Screen & System Audio Recording, called Screen Recording in older versions; the app is listed as Cua Driver). Missing recording adds "When the system asks to quit and reopen or allow direct screen access, choose Allow"; ends with "Return here and tap Check Again". Buttons Grant Access and Check Again, plus VNC when Screen Sharing is on (the web has none here: its one entry is the page's top-bar View screen, and while the dialogs are up the section points to it, #3247) |
| Ready | "Ready to control · Cua Driver 0.38.0" |

The page polls every 2 seconds while a job runs or permission was requested and it is still waiting for Allow.

### Installation (`computer.install` on the node)

Only one background job runs at a time; status comes back in `job` in `computer.status` (`{kind: install|grant, state: running|done|failed|cancelled, step, error}`):

1. **Download installer**: Cua's official `install.sh` delegates to `_install-rust.sh`, which sources `_install-common.sh`.
   - Download all three scripts first from `https://cua.ai/driver/` into a new temporary directory, and **run from disk**: `install.sh` prefers a sibling helper, which prefers a sibling common script. Thus no other code is fetched from the network while running.
   - Before execution, check (`checkScript`): the two executable scripts are bash, the sourced file is shell, and `_install-rust.sh` contains verification against SHA256SUMS. Otherwise don't run.
   - Cua publishes no hashes for the install scripts themselves, so this only proves "HTTPS from cua.ai, looks like its installer". Recorded here; don't mistake it for signature verification.
2. **Download and verify Cua Driver, install in Applications**: `/bin/bash install.sh --no-modify-path`, without changing shell rc files.
   - The installer verifies the release package against its SHA256SUMS. If `cosign` is installed, add `--require-signature` (Sigstore verification).
   - No sudo needed: uses `ditto` into `/Applications`, so the account must be an administrator; `~/.local/bin/cua-driver` links into the app.
   - Replaces the same app if already installed: the installer stops the daemon first and retains permissions if the signing requirement is unchanged. That's why already-installed machines don't get Install.
3. **Disable Cua usage statistics**:
   - Pass `CUA_DRIVER_RS_TELEMETRY_ENABLED=0` during installation, so even an installation event isn't reported.
   - Afterward run `cua-driver telemetry disable` to persist the setting.
4. Once installed, the node starts the permission job immediately.

### Granting permissions (`computer.grant` on the node)

- The node runs `cua-driver permissions grant`: it opens CuaDriver.app through LaunchServices, showing the system Accessibility and Screen Recording prompts.
- **Allow must be clicked on that Mac's screen**; the App can't click it for you. If Screen Sharing is on, the machine page offers VNC (next section), so you can click remotely in the phone's view.
- **Not possible**: if Screen Sharing is also off on that Mac, first-time permission must be granted in person at the machine. Everything afterward can be remote.
- No uninstall button; it isn't essential. To uninstall, run Cua's own `https://cua.ai/driver/uninstall.sh`.

### API

`POST /api/v1/machines/{mid}/computer/{install|grant|cancel}` → 200 `Computer` (with `job`).
- Extension not enabled for this machine: 404.
- Offline, job already running, or node too old: 409 `{"error"}`.
- Cancel only stops installation; granting permissions waits for a system prompt on the Mac, which is up to the user.

## Live view (#1184)

Purpose: watch that Mac's real desktop on the phone or web while an Agent or assistant operates it. View-only first, then taking over input.
Cua Driver operates in the background without stealing focus; the view shows the real desktop. Its agent cursor is a topmost floating window visible on screen.

### Two paths

| | a) Built-in macOS Screen Sharing (VNC, `127.0.0.1:5900`) | b) node captures and streams the screen |
| --- | --- | --- |
| What the Mac needs | Enable System Settings › General › Sharing › Screen Sharing once | Screen Recording permission for the capture program: reuse Cua's permission, have Cua Driver capture and node poll |
| Enable remotely | Requires sudo; Apple says Remote Management enabled by command line (`kickstart`) since macOS 12.1 allows viewing but not control without MDM. Verify during implementation | Same permissions as Cua |
| Latency, frame rate | System encoding, tens of frames per second, low latency | One capture and encode per frame, estimated a few frames per second |
| Taking over input | Built into the protocol (keyboard/mouse) | Translate taps into Cua Driver clicks; clicking works, smooth dragging doesn't |
| Unlock, login window | Visible and controllable (can remotely unlock a locked screen) | Can't capture the desktop after locking |
| First-permission chicken and egg | With Screen Sharing enabled, Cua permission prompts can be clicked remotely | Capturing itself needs permission, so the first grant must be in person |

**Choose a**:
- Fewest one-time steps: enable Screen Sharing and grant Cua permissions in person once; afterward installing Cua, granting permissions and unlocking can all be remote.
- Lowest latency, takeover already implemented.
- b only matters for users unwilling to enable Screen Sharing; not in the first version.

### Connection

Only through the hub tunnel, no new externally exposed port:
- Client ⇄ hub: WebSocket `GET /api/v1/machines/{mid}/ports/5900/connect?token=…`, binary frames with raw TCP bytes (port forwarding in [ports.md](../operations/ports.md)).
  - When `cua` or `ego` is enabled for that Mac, the hub permanently allows 5900 (`portAllowed`), and only 5900; no Agent listener or manual `open` needed. ego's first setup must use it from the phone (#1507 item 6), so `cua` needn't also be on; the page asks `computer` first, then `browser` if not enabled.
  - Accepts the `binary` subprotocol noVNC may send.
  - Read-only devices (like the watch) still get 403.
- hub ⇄ node: `port.dial`; the node only connects to its own `127.0.0.1:5900`.
- `computer.status` adds `screen_sharing`: the node connects to `127.0.0.1:5900` and checks for an RFB version greeting (macOS: `RFB 003.889`). This determines whether the machine page offers VNC or explains how to enable Screen Sharing.
  While it answers, `computer.status` and `browser.status` also carry `displays`, the Mac's display arrangement ("Displays" below).
- Each stream's unacknowledged limit remains 512 KB: about 10 MB/s at 50 ms round-trip, sufficient for VNC; unchanged.

**Screen Sharing limitations**:
- It listens on every interface, so 5900 is visible on the LAN. That's system behavior; Persona opens no extra port. Restricting it to loopback needs a pf rule with sudo, for the user to decide.
- It requires that Mac's macOS account and password (Apple ARD authentication), or a separately configured VNC password.
  - The page asks each time, sends them only to that Mac, not the hub or repository, and doesn't store them.
  - Account name prefilled from the machine's home directory (`/Users/<name>`).

### Page: `/screen/<machine id>` (web, `web/src/screen/`)

- **Loading**: on `/screen/…`, the web entry bypasses login and sidebar and dynamically loads only this page. noVNC ([noVNC](https://github.com/novnc/noVNC) 1.7, MPL-2.0, `@novnc/novnc`) lives in a separate chunk, not the main bundle.
- **Token**:
  - The App opens it with its device token in `#token=…&embed=1`: URL fragments aren't sent to the server; the page removes it from the address bar immediately on load, keeping it only in memory.
    - Includes the machine's display name in `name=` (Mac Studio), used before the hub responds instead of displaying its id.
    - Each value is percent-encoded only once (`ScreenViewer.url`, `percentEncodedFragment`). Before #1498, double encoding turned `jd_` device tokens into `jd%5F…` on the page, and the hub returned invalid token.
    - Hub side unchanged: device tokens use `Authorization` or WebSocket `?token=`; devices that may answer but not type (the watch) get 403.
    - Tests: `ScreenViewerTests`, `screen.test.ts`, `TestTheLiveScreenTakesTheAppsDeviceToken`. The last uses a stand-in Screen Sharing server to verify its version greeting is received through forwarding with a device token, while incorrect and double-encoded tokens are rejected before dialing.
  - Doesn't exchange for a new browser token like web login links (which would add a device token on every open).
  - Users already logged into the web use their own token directly.
- **Ask the hub first**: does the machine exist, is it online, is the extension enabled, is Screen Sharing on? Each has a user-facing explanation. Only then connect.
- **Login**: when Screen Sharing requires login, show "Sign in to Screen Sharing on this Mac" (account, password, Connect). Failure says "Incorrect account or password", allowing another try.
- **Once connected**:
  - View Only (`viewOnly`) by default. Switching View Only / Take Over in the top bar hands keyboard and mouse (touch) input to that Mac.
  - Scale to the window (`scaleViewport`), without changing the Mac's resolution.
  - On a Mac with several displays, the top bar's Displays menu shows one of them ("Displays" below).
  - The top bar's Connection stats shows this connection's numbers ("Connection stats on the web" below).
  - On disconnection, offer Reconnect.
- **Without `crypto.subtle`**: ARD login AES-encrypts the account and password; noVNC's AES uses browser `crypto.subtle`. Pages opened over HTTP (hub LAN address, App WebView) aren't secure contexts and lack `crypto.subtle`.
  - `aes.ts` supplies an AES-CBC-encryption-only substitute when missing.
  - Tests: three FIPS-197 standard vectors and byte-for-byte comparison with WebCrypto.
  - HTTPS or localhost uses the browser's implementation, without the substitute.
- **View**: Cua Driver operates in the background without taking focus, so the view is the real desktop. Its agent cursor is a topmost window and visible.

#### Connection stats on the web (#6558)

- **Toggle**: the top bar's **Connection stats**, on any live Mac as the App's ⋯ › Connection Stats, off by default and
  kept per Mac in this browser (`video.ts` `statsShown`). It shows a small panel at the top right of the picture, which
  drags anywhere inside it and has a close button. A page an app embeds has no bar and no panel. The toggle and the panel
  are a chunk of their own (`StatsOverlay.tsx`, words in `screen-video`), loaded once the screen is live.
- **What it shows**, once a second (`meter.ts`): `VNC · Via hub`, then bitrate and frame rate, then on a line of its own
  **Round trip to the hub N ms**. Only what was measured: a number with nothing behind it is left out, never guessed. A
  still screen sends nothing, so it says 0 fps.
- **How**, only while the panel is shown (`probes.ts`):
  - Bitrate: the page opens the RFB WebSocket itself and hands it to noVNC (its constructor takes a channel: the same URL,
    no subprotocols, and noVNC closes it). A listener beside noVNC's counts each message's bytes, RFB's own without the
    WebSocket and TLS framing.
  - Frame rate: noVNC has no frame event. The hook wraps its display's `flip()`, which puts each finished update on the
    screen, at once or from its render queue, and counts the flips that draw. A noVNC whose display has no `flip()` and
    `_renderQ` is not hooked, and the frame rate is left out. `probes.test.ts` runs the hook on the real noVNC 1.7, so an
    upgrade that changes it fails there.
  - Round trip to the hub: the screen page holds no hub connection (`main.tsx` loads only `ScreenView` here), and another
    `/api/v1/ws` would count as one more connection of the device (a `device_sessions` row, every broadcast, the snapshots
    sent on connect). So every 2 s while the page is visible it fetches `GET /api/v1/health?probe=N` (no sign-in, the
    hub's memory only; `cache: "no-store"`, `redirect: "manual"`) and takes `responseStart − requestStart` from that
    request's own Resource Timing entry, through a `PerformanceObserver` (the global buffer is never cleared). A probe
    counts only when the hub answered `{"ok":true}` and both times are there; the panel shows the median of the last 5
    from the last 20 s, and a background tab probes nothing. That is the browser's HTTP round trip to the hub, a little
    of it the hub answering, and not the hub's way on to the Mac, so it never sits on the first line, where the App puts
    the video's end-to-end total.
- **Not yet**: the video channel's segments (capture · encode · network · decode), `path` (#6398) and `tier` (#6396)
  come with the web video path.
- **Tests**: `meter.test.ts`, `probes.test.ts`, `ScreenView.stats.test.tsx`, and in a real browser against the mock's
  moving box (`MOCK_COMPUTER=ready MOCK_SCREEN=fake MOCK_SCREEN_ANIMATE=1`) `node web/e2e/screen-stats.mjs <web URL>`:
  off by default, measured numbers in light and dark, kept per Mac, no round-trip line while the hub's health is refused.

### Native client in the App (#1343, `apple/Persona/Screen/`)

Picture in Picture (`AVSampleBufferDisplayLayer`) needs access to each frame, which noVNC in a WebView can't provide, so the App speaks RFB itself. The web still uses noVNC.

- **Connection**: hub's 5900 forwarding (`RFBWebSocket`), a binary WebSocket over the same route as the App's hub connection (`APIClient.url`: via SSH tunnel when used; `APIClient.session`: pinning for public TLS), with device token (`Authorization`).
- **Login** (`RFBAuth`):
  - macOS Screen Sharing offers 30 (Apple Remote Desktop), 33, 35, 36. This uses 30:
    1. Server sends the generator, prime and its public key: macOS 26 a 4096-bit prime with generator 5, older macOS 1024 bits.
    2. Client calculates the DH shared secret, using its MD5 as the AES-128 key (ECB). This needs no account or password,
       so `RFBAuth.exchange` runs as soon as the server's numbers arrive, while the sign-in form waits (#7809).
    3. Account and password each occupy 64 bytes, NUL-terminated and padded with random bytes. Send the encrypted 128 bytes first, then the client's public key.
  - Large-number arithmetic is a custom Montgomery modular exponentiation (`RFBBigUInt`: 64-bit limbs on raw buffers,
    a 4-bit window, the two exponentiations side by side), checked against Python `pow`. With a 4096-bit prime the old
    32-bit-limb array version took 48 s in a Debug build after Connect (0.6 s optimized); this takes 0.65 s and 0.03 s.
    A shared Rust implementation in PersonaCore was weighed and not taken: a new big-number crate and entry points on
    three clients for one function the Swift fix makes fast enough.
  - Mock Screen Sharing also offers VeNCrypt Plain (19 → 256) for tests; no authentication (1) is accepted too.
  - Account/password come from saved credentials (`ScreenCredentials`, #1322).
- **Frames** (`RFBFramebuffer`):
  - Requests 32-bit, depth 24, little-endian pixels with red at bit 16; BGRA in memory, directly usable as `CVPixelBuffer` 32BGRA.
  - Encoding preference: ZRLE (16), CopyRect (1), Raw (0), plus DesktopSize, ExtendedDesktopSize, Cursor and LastRect pseudo-encodings.
  - What macOS's Screen Sharing really does (macOS 26.6, `vncbench probe`, #3067): of the standard encodings only ZRLE,
    Zlib and Raw (ZRLE whatever the order asked; no Tight, JPEG, Hextile or H.264), and none of the pseudo-encodings:
    no Cursor (it draws the pointer into the framebuffer), no ContinuousUpdates or Fence, no ExtendedDesktopSize. It
    takes a 16-bit pixel format (ZRLE then sends about 43% of the bytes, no longer lossless). Its own encodings are
    1000 (1-bit), 1001 (4-bit grey), 1002 (16-bit colour through zlib, larger than ZRLE at 16 bits) and 1011, a
    DCT codec of its own (a tenth of ZRLE's bytes) whose entropy coding is undocumented; 1101, 1107, 1109 and 1110 carry
    the display layout, formats, the keyboard layout and the model. Asked for 100 or more encodings at once it answers
    nothing. So lossless ZRLE is the most a standard client gets.
  - One zlib stream for ZRLE over the whole connection, using Compression raw DEFLATE after skipping the two-byte zlib header (`RFBInflater`).
  - All five ZRLE tile types: raw, solid, packed palette, plain RLE, palette RLE, written into the framebuffer a row at a time (#3067).
  - The next incremental FramebufferUpdateRequest goes out once an update is in (stop-and-wait). Asking earlier was measured and left out (#3067, `vncbench run -mode early|gated|pipeline`, over a 25 ms each way, 30 Mbit/s link): asked for as each update starts, macOS's server goes quiet for up to two seconds whenever the request reaches it while it is still writing a big update (video at 1.8 frames a second instead of 5.3); asked for early only when the update is one rectangle of at most 64 KB, typing came back later (p50 374 ms against 215, slower in all 5 interleaved pairs at a load of 25–80); two requests in flight were slower still on loopback.
  - Requests always cover the whole framebuffer, also when zoomed in or showing one display of several (#3067): macOS's Screen Sharing leaves most incremental requests for less unanswered while that part changes (`vncbench trace -trace-area`: the bench page's part, a full-width band not starting at the top, or anything 1850 of 1920 pixels wide got no updates for 5 s while video played inside it, nor the page's own redraws; only near-whole requests from the top worked), so asking for the part shown would freeze a zoomed-in picture.
- **Showing frames** (`RFBFrameSink`, #3067): at most 60 a second, into IOSurface-backed pixel buffers from a pool. Each buffer the pool has handed out keeps the region that changed since it was last filled (by its IOSurface id; the client's damage comes with the framebuffer, `RFBClient.withDamage`), and a frame copies only that region: a new buffer, or one the pool let go and handed out again, is filled whole. The page's fps / latency numbers are reported at most four times a second, so the page doesn't redraw for every frame.
- **Sending back**: pointer (button bits: left, middle, right, wheel up/down), keys (keysym), clipboard (through the node; ClientCutText only when the node is too old for it, see "Clipboard" below).
  - All use one outgoing queue (`RFBOutbox`), sent in order by one task after the handshake. Previously a `Task` per message could reorder press/release or move/click on the Mac. Messages before the handshake wait and can't interrupt it.
  - A pointer event that only moves (the same buttons as the event before it) replaces one still waiting that only moved: when the way to the Mac stalls, the pointer goes to where the finger is instead of replaying the path. Presses, releases (where they were made) and wheel notches are never folded (#3067).
  - The queue's wait ends when its task is cancelled: the client's task group waits for both of its tasks when the reader ends.
- **Tests**:
  - `RFBClientTests`:
    - large numbers checked against Python;
    - ARD bytes checked against Python plus openssl encryption;
    - ZRLE checked against a Python encoder, all five tiles sharing one stream across two rectangles;
    - complete scripted session, outgoing bytes checked byte for byte;
    - ARD handshake and rejected login;
    - pointer, key and clipboard bytes and order (pre-handshake messages after handshake; 120 messages in original order).
  - `RFBFrameTests` (#3067): region merging, the outbox folding motion but not presses, releases or notches and ending on cancel, the frame sink copying only what changed with every pixel matching the framebuffer, and the next request going out only after an update's data.
  - Mock `MOCK_SCREEN=fake` now offers ARD first like macOS (DH in Python, AES with openssl), then ZRLE or Raw as the client requests. It records pointer, clicks, wheel and clipboard (read at `/mock/screen-password`; `?clip=…` sends clipboard to the client). `MOCK_SCREEN_ANIMATE=1` moves a square at about 30 frames per second to measure frame rate.

### Measuring (#3067)

`server/cmd/vncbench` measures a VNC server the way the app uses it, from the computer the server runs on (straight to
`127.0.0.1:5900`) or through a hub's relay (`-hub <hub address> -machine <id>`, a device token in `VNCBENCH_TOKEN`).
It signs in with the login keychain's generic password `persona-vnc-bench` (added once with
`security add-generic-password -s persona-vnc-bench -a "$(id -un)" -w`) or `VNCBENCH_ACCOUNT` / `VNCBENCH_PASSWORD`, never printing it.

- `probe`: which image encodings, pseudo-encodings and pixel formats the server answers with, Apple's private range
  included, and Tight's bytes and SSIM / PSNR at each quality level against lossless ZRLE.
- `run -scenes idle,type,scroll,drag,video -config <encodings> -mode stopwait|gated|early|pipeline|continuous`: a bench page of
  its own shown in a browser on that Mac; per scene the Mbit/s, updates a second, input-to-pixels latency (the page's
  marker changing colour, split into getting the input and drawing it back), and the picture's SSIM / PSNR against a
  second, lossless connection of the same moment. `-delay` and `-rate` add a phone link's latency and bandwidth.
- `wheel -apps page,finder,terminal -browser <executable> [-replay <schedules.json>]` (#7775): how far wheel notches
  scroll on that Mac. One notch at a time, then 20 at gaps from 100 ms down to none (does macOS speed quick notches up,
  or merge them?), then each named schedule (`{"name": "ms:n,…"}`, a gesture's notches at the times the app sends
  them). A browser page reports its own scroll position and each wheel event's `deltaY`; Finder and Terminal are
  measured from the picture, by how far their rows moved. It uses only windows it opens itself (a browser of its own
  with a profile of its own, a Finder window on a folder of its own, a Terminal window running its own script),
  closes them and brings back the app that was in front. It starts only after a minute with no input on the Mac and
  stops at the first key, click or pointer move it did not send.
- `record`: what the server sends, for replaying through the app's own client (decode CPU, bytes copied).
- `trace -scene <scene> -rec-mode <mode> [-trace-area x,y,w,h|page|stage]`: each request and each update's start, end,
  size and painted box in order, to see how the server answers a way of asking (or a part of the screen).
- `relay`: an echo server of its own behind the hub's port relay: small-message round trips and throughput, hop by hop
  when run on the hub and on the node in turn.

### Displays (#80)

macOS Screen Sharing sends one framebuffer with every display, laid out as System Settings › Displays arranges them. On iOS, iPadOS
and visionOS the VNC page's "…" › Displays offers All Displays (the default) or one display; one display is cropped out of the
framebuffer, zooming and panning stay inside it, and touches still send whole-framebuffer pixels. The choice is kept per Mac on the
device (`VNCDisplayChoice`, as Mouse / Trackpad Mode). The web page offers the same choice ("On the web" below); the Mac app and
Picture in Picture show every display.

Where the layout comes from (`VNCDisplays`), most trusted first:

1. **The server's screens**: the client asks for ExtendedDesktopSize (-308); a server that supports it sends every screen's rectangle in
   framebuffer pixels with the first full update and on changes. Used when it lists two or more screens that lie on the framebuffer.
2. **The node's arrangement**: macOS's server is not known to send ExtendedDesktopSize, so while Screen Sharing answers the node reads
   `NSScreen` through `osascript -l JavaScript` (about 0.2 s; `server/internal/node/displays.go`) and reports `displays` in
   `computer.status` / `browser.status`: id (macOS's display number), x, y, width, height in points in the global display space (origin
   at the main display's top-left corner, y down), `main`. No display names. The app scales the arrangement's bounding box onto the
   framebuffer; when the box's aspect ratio is more than 2% off the framebuffer's (a display changed since the last status, or a server
   that draws the displays at different scales), it offers nothing rather than crop the wrong place.
3. **Neither**: no choice; the page shows the whole framebuffer.

Without a GUI session (logged out, a node started as a daemon) `osascript` sees no screens and the node sends none. Older nodes send no
`displays` and older apps ignore it.

Known gaps: the aspect check cannot catch an arrangement whose bounding box happens to have the framebuffer's shape while the server
sends something else (four equal displays in a 2 × 2 grid, of which the server sends one); such a crop would be wrong. The menu numbers
displays left to right, then top to bottom, by their top-left corners.

**On the web** (#5685, `web/src/screen/displays.ts`): the top bar's Displays menu (All Displays / Display N, shown only with two or more
displays to offer; on a narrow screen the button keeps only its icon). The rules are the app's, with these differences:

- **Layout from the node only**: noVNC reads ExtendedDesktopSize but keeps only the first screen's id, so the page uses the `displays`
  of the computer / browser status it fetches before connecting, with the same scaling, 2% aspect check and ids (`display-<n>`). macOS's
  server sends no ExtendedDesktopSize (see "Frames" above), so on a Mac this is the app's result too.
- **Crop**: noVNC keeps scaling its whole framebuffer to its container. The page sizes that container to the framebuffer at the scale
  that makes the chosen display fit the page, and puts it inside a box the display's size that clips the rest. noVNC maps the pointer
  through its canvas, so input lands in whole-framebuffer pixels without touching noVNC. A mouse drag that leaves the box is sent again
  at the nearest whole pixel inside it, and a touch gesture's position (noVNC's gesture events carry it in their detail) is moved there,
  as the app keeps touches on the display.
- **Kept**: per Mac in this browser (`localStorage` `persona.screen.display.<machine id>`). The page asks for the status again when the
  framebuffer changes size (a display plugged in or out, another resolution), so a chosen display that comes back is shown again
  without a reload, and as the menu opens, so its list is the Mac's as the choice is made. Two displays of one size swapped while the
  menu stays closed keep the old crop until it opens.
- **Loading**: the menu (`screen/DisplayMenu.tsx`) is a chunk of its own, so its words (`screen-displays`) stay out of the first
  screen's catalog. It is fetched while a Mac whose status lists several displays connects, and the page crops to a kept display only
  once it is in, so a cropped picture always has the menu to go back. One that would not load (offline, the hub redeployed) leaves
  every display shown and is asked for again as the connection moves on.
- Pages an app embeds (`embed=1`) have no top bar: they show every display and ignore a choice this browser kept.
- **Tests**: `web/src/screen/displays.test.ts`, `ScreenView.displays.test.tsx`, and in a real browser against the mock hub
  (`MOCK_COMPUTER=ready MOCK_SCREEN=fake MOCK_SCREEN_DISPLAYS=2:node`) `node web/e2e/screen-displays.mjs <web URL> <mock hub URL>`: the
  chosen display fills its box, a click lands on it, drags out over a fractional edge and the top edge stay on it, a new page reopens it.

### App

- **Control Computer on the machine page**:
  - Ready: offers VNC.
  - Granting permissions, or still missing them after a request: prominent VNC button to click Allow remotely from the phone.
  - Screen Sharing off: explains how to enable it in System Settings › General › Sharing on that Mac.
- **Chat page**: VNC (`ScreenEntry`) also appears below tool details for two types of tool, provided the extension and Screen Sharing are enabled for that Mac:
  - assistant `computer_state` / `computer_run`: the named machine;
  - Cua Driver MCP tools registered directly by a computer's Agent (`mcp__cua-driver__…`): that Agent's machine.
- **The pill above the composer** (`ComputerActivityPill`): while the chat's newest computer tool call runs, and six seconds
  after it ends, "Controlling MacBook · Click AC → 6" sits over an Agent chat's composer and after the entries in a Persona
  conversation; a tap opens that Mac's VNC (disabled where its screen can't be shown). The app reads it from the transcript, as above:
  no hub field says a session is operating a computer. Android (Glass, #4339) draws the same pill from the same items
  (`ComputerActivity` in core/domain) and has no VNC, so its tap opens the work line that holds the call; the web shows none.
- **Full page** (`VNCPage`, #1343): native client fills the screen in its own window (`ScreenWindow`, #1604: iOS `UIWindow` above the shell, below toasts; separate Mac window; follows `AppModel.screenOpen`). Previously a `fullScreenCover` on `RootView`, whose presentation was swallowed when the machine page was in a sheet. The old web drawer (WKWebView + noVNC + bridge) is removed; the web still uses noVNC.
  - Top bar: Close, Picture in Picture (iOS, right of Close, #3075), connection stage (connecting / login required / connected / disconnected) and machine name, Help, "…" (Mouse Mode / Trackpad Mode, Zoom In or Zoom to Fit (iOS, #6196), Center Pointer, Picture in Picture Diagnostics; Picture in Picture itself on the Mac). Where the row is too narrow (the largest text sizes) the name and stage go on a line under the buttons, never cut to less than the name's first four letters, and where even the buttons don't fit, Picture in Picture goes into "…". When the assistant or an Agent operates this Mac, another line: "Now: tap AC → 6 → × → 7 → =".
  - Bottom bar: Clipboard, Rotate (iPhone only, #6196), I'm Done (only when the Agent that opened it is waiting for you), Keyboard.
  - **Rotate** (#6196): right of Clipboard on an iPhone, a tap turns the page on its side (`requestGeometryUpdate`), and on its side, where it sits under Clipboard down the trailing rail, back upright; its symbol and VoiceOver label name where the tap turns it (`Theme.Icon.rotateToLandscape` / `rotateToPortrait`). Once tapped, the page keeps that orientation however the phone is held (`AppOrientation.screenTurned`: `.landscape`, either side, or `.portrait`) until it is tapped again or the page closes, so it works with Portrait Orientation Lock on; untapped, the page turns with the phone as below. An iPad is never made to turn, so it has no button.
  - **Landscape** (#3075): on an iPhone the page turns to either landscape while it is up; the rest of the app stays upright (`AppOrientation`: the app delegate's mask, upright unless the page is up; Info.plist still lists only portrait, so the app launches upright however the phone is held). The bars become rails down either side (`verticalSizeClass == .compact`; one `AnyLayout`, so the picture and the keyboard's catcher stay the same views as the phone turns): Close, Picture in Picture, the status dot (name and stage for VoiceOver), Help and "…" down the leading rail; Clipboard, Rotate, I'm Done (icon only) and Keyboard down the trailing one; short of room (the keyboard up), a rail's buttons close up; at the largest text sizes Picture in Picture then goes into "…" so Close, Help and "…" stay above the keyboard, and only then does a rail scroll. Each rail stays one set of views whichever way it fits (#5289): a menu opened from it hides the keyboard and gives it room, and the menu morphs out of its button, which must still be there. The picture takes the whole height between them; the sign-in form scrolls. UIKit decides from the shell window's controllers and what they present (a machine's page in a sheet), not the screen window's, so opening and closing the page asks every controller in the scene to read the mask again; closing on its side turns the app upright at once (`requestGeometryUpdate`).
  - **Reconnects on return** (#4243): a suspended app's connection is often gone. Coming back from the background with the full page up (not Control Center or a dismissed alert), a connection already closed reconnects at once, and a drop reported within 5 seconds of returning reconnects once by itself. Login required, connecting and connected are left alone, so Reconnect is no longer needed. If it still says connected on return, the page asks the Mac for the whole screen once; when no frame arrives within 10 seconds, the connection is taken as silently gone in the background and reconnects once (#4380), instead of staying on a frozen picture.
  - **Two modes** (remembered per Mac, `vnc.mode.<group>/<machine>`):
    - Mouse Mode (default): pointer follows the finger. Tap clicks there; one-finger drag drags; two-finger drag scrolls; two-finger tap or long-press right-clicks; pinch zooms, then two-finger drag pans the zoomed view. A two-finger touch is a scroll or a pinch, never both (`VNCTwoFingers`, #6196): pinch and pan are recognized together, and any change in the fingers' spread used to zoom, so a scroll whose fingers drifted apart zoomed (and, zoomed in, turned into a pan). Whichever the fingers show first wins until they lift: a pinch needs the spread to change by 15% (10 pt at least) and by more than the fingers travel together; a scroll, 6 pt of travel, at least as much as the spread changed. Until then neither acts, and the travel is kept for the scroll.
    - Trackpad Mode: finger moves the pointer relatively; tap clicks at the pointer, tap then immediately drag drags, two-finger drag scrolls at the pointer. Use Center Pointer if it strays. No pinch (#6196): two fingers only scroll.
    - **Zoom** from "…" (#6196): Zoom In is twice the fitted size, around the pointer (the middle when the pointer is not on the part shown); once zoomed in, by it or a pinch, "…" offers Zoom to Fit instead. It is the only zoom in Trackpad Mode, where the zoomed picture moves to keep the pointer 32 pt inside the view (`VNCZoom.following`), so the pointer can reach every edge. When the page turns, a zoomed picture keeps its zoom against the new fit and the part that was in the middle of the view stays in the middle (`VNCZoom.turned`).
    - **Two-finger scrolling** (same in both modes, `VNCWheel`, #4878, #7775):
      - **What a notch does on the Mac** (measured with `vncbench`'s wheel probe on macOS 26.6, #7775): macOS's Screen Sharing scrolls a wheel notch one point, however fast notches come and with no speed-up: a browser page gets a 1-pixel wheel event (`deltaY` 1 in pixels; notches close together merge into one larger event, none lost) and a Finder list moves 1 pixel. A terminal (Terminal.app) scrolls a whole line (15 px) for each event it gets, and notches that arrive together merge into one or a few lines. Screen Sharing keeps up with 2000 notches a second (the page stopped 0.1 s after the last); at 6000 a second every notch arrived but the page went on scrolling for 6.5 s. Before #7775 the app sent a notch every 8 pt of finger travel, so a slow 300 pt drag scrolled a page 37 points.
      - **Speed curve**: a notch is a Mac point. While the fingers go no faster than 250 pt/s the Mac's content moves as far as they travel over its picture at the current zoom (view points × framebuffer pixels a view point ÷ framebuffer pixels a Mac point, `VNCDisplays.pixelsPerMacPoint`: 2 where Screen Sharing sends a Retina display at its full size, else 1), as if dragging it. Faster, each point counts for more: the gain rises in a straight line to 4× at 2000 pt/s (1.6× at 600, 3.1× at 1500), from UIKit's pan velocity.
      - **Pacing**: the fingers and the coast only move where the wheel should be. A display link sends what that has come to once a frame, about 12 ms behind, at most 2000 notches a second (33 a frame at 60 Hz). No more than 0.1 s worth waits; beyond that the fingers are held to that top speed, so the Mac stops scrolling soon after they do. `RFBSession.scroll` sends every notch it is given (it once dropped any past 20 in a call). The first notch is sent at half a notch; remainders are retained and reverse motion subtracts them.
      - **Coast**: letting go at 250 pt/s or faster continues from the speed the notches were going, with UIScrollView's normal deceleration (0.998 remaining per millisecond), sent no faster than 2000 a second: a harder flick keeps that pace longer before it slows, so it goes further. Touching again stops it and anything not yet sent.
      - In numbers (model of 300 pt drags reaching their speed in 50 ms and lifted moving; a 1920-point Mac on an iPhone's fitted picture, about 4.9 Mac points a view point): slow (200 pt/s) 1470 points, medium (600) about 2990, fast (1500) about 3930; before, 37, 61 and 66.
      - **Scroll Speed** (Settings › Live Screen, `VNCScrollSpeed`): Slower, Default or Faster scale the whole curve by 0.6, 1 or 1.6, read as two fingers go down; the 2000-a-second top speed stays.
      - Natural scrolling: pulling down reveals content above, like iOS and Mac defaults. Measuring: `VNCTests/testScrollMeasure` (`PERSONA_VNC_SCROLL_MEASURE=1`) plays slow, medium and fast 300 pt drags against the mock, whose relay stamps each notch as it arrives (`wheel_in`). A terminal still moves line by line, and a page stops dead at its ends: smooth, trackpad-like scrolling there needs a process on the Mac that posts continuous scroll events (#7775).
    - Pointer draws the cursor sent by the Mac (Cursor pseudo-encoding), or the app's own arrow until it does, through `VNCPointerArt` (#3075). Drawn at the picture's scale it was a speck: a 1920-pixel Mac on a 402-point phone is 4.8 pixels a point, so macOS's arrow came out about 4 points tall and its one-pixel white outline averaged into the black body; on a dark window or terminal it vanished. Now it is at least `Theme.Size.pointer` (22 pt) tall (its visible part; the Mac's own size when zoomed in further), scaled about the hot spot by whole device pixels so the outline stays a crisp line, and given the rings it needs to read on any background: a pointer that reads on both black and white (macOS's own arrow and I-beam, black with a white outline) is only enlarged; one with no light edge gets a 1 pt white ring, one with no dark edge a 1 pt black ring ("reads": every outline pixel has one within 2 pt that differs from the background by 0.5 in luminance). Any background is far from black or from white, so a pointer that reads on both reads on all. `VNCPointerArtTests` check macOS's arrow (with and without its shadow as a rim), the I-beam and black / white / red crosses over black, white, gray, a dark terminal and a busy picture.
  - **Keyboard**: the bottom-bar Keyboard opens the system keyboard; typed text goes directly to the Mac (`VNCKeyInputView`). A row of Mac keys above it (`VNCShortcutBar`, in the page, moving up with the keyboard): ⌘ ⌥ ⌃ ⇧ tap to hold, tap again to release; Esc, Tab, arrow keys, ⌘C ⌘V ⌘Z ⌘Tab ⌘Space. Latin-1 characters are sent as keys; Chinese and other characters are pasted via clipboard (put in the Mac clipboard, then ⌘V). ⌘ sends Super_L (0xffeb), interpreted as Command by macOS Screen Sharing.
  - **Clipboard**: Paste from iPhone puts the phone clipboard text in the Mac clipboard and presses ⌘V; Copy to Mac's Clipboard Only skips the keypress; Copy to iPhone retrieves the Mac clipboard text. Writing and reading go through the node (`computer.clipboard`: `pbcopy` / `pbpaste`, up to 1 MB; hub `GET/POST /api/v1/machines/{mid}/computer/clipboard`, requiring `cua` or `ego` enabled for the machine, like 5900 forwarding), which answers once the text is on the clipboard, and only then ⌘V goes out (`RFBInputQueue`). RFB cut text (ClientCutText) is used only when the node can't (one too old): it carries Latin-1 alone and has no answer, and macOS's Screen Sharing stores it only as the legacy `com.apple.traditional-mac-plain-text` type, with no `public.utf8-plain-text`, which `pbpaste` and Terminal don't read: on macOS 26 a ⌘V after it pasted nothing into Terminal (0 of 12, with or without a 0.5 s wait). It used to be the path for all Latin-1 text, so English pasted into a terminal never arrived (#3075). The node runs `pbcopy` / `pbpaste` in `en_US.UTF-8` (`LANG`, `LC_ALL`, `LC_CTYPE`): the resident node runs under launchd with no locale, where they take text as MacRoman and a pasted 中文 arrived as ‰∏≠Êñá (`TestMacClipboardRealRoundTrip`, `PERSONA_REAL_CLIPBOARD=1` on a Mac). Reading text copied by another App on the phone causes iOS to ask Allow Paste once. The page reads it off the main thread, where the read would hold the picture and the session still until it is answered; refused, the page says why rather than that there is no text (#3075).
  - **A new session's first seconds** (#3075): macOS's Screen Sharing drops every key event it gets in roughly the first 2 to 3 seconds after a client signs in (measured on macOS 26 against Terminal: a ⌘V or Return then never arrived, the latest loss 3.05 s in on a heavily loaded Mac, 2.2 s on a quiet one), while pointer events land at once, and nothing on the wire says when keys start to count. The page signs in again whenever the app comes back from the background, so text copied in another app and pasted right away was lost. `RFBInputQueue` holds keys, and a paste's ⌘V (not its clipboard write), for 3.5 s after each sign-in and then sends them in order; One-Tap Unlock's pause after its Shift is queued too (`pause`), so it counts from when the Shift goes out.
  - **I'm Done**: when the Agent waits for you to act on the computer (its chat opened this page), tapping sends 我在电脑上操作完了，你继续吧。 ("I'm done on the computer; please continue.") to its chat. Closing the page also hands control back.
  - **Help**: Using VNC page covers touch controls (zoom in each mode, and Rotate on an iPhone), input and clipboard, when to use Trackpad Mode, Picture in Picture, and handing back the computer.
- **Picture in Picture** (`VNCPiP`, #1343):
  - On iOS, system Picture in Picture: `AVPictureInPictureController` + `AVSampleBufferDisplayLayer`, floats over other Apps and the home screen, draggable and resizable. View-only; all input is on the full page. Restore (or tap the small window) returns to the full page; the full page's Picture in Picture button (right of Close) returns to the small window.
  - Background: audio session `.playback` (`mixWithOthers`, silent and doesn't interrupt other audio), `audio` in `UIBackgroundModes`; the small window keeps updating in the background. Closing it disconnects (unless the full page is still open).
  - Mac: a panel above other windows (`NSPanel`, `.floating`, on all desktops); clicking opens the full-page window, closing disconnects.
  - One connection per Mac (`VNC.hold` / `release`), shared by full page and PiP. Disconnects 2 seconds after the last release, so switching doesn't reconnect.
  - Frames: between updates, `RFBFrameSink` copies the framebuffer (locked) into `CVPixelBufferPool` (32BGRA, IOSurface), up to 30 fps, with increasing host-clock timestamps. The full-page layer sets DisplayImmediately; PiP's source layer has its own `controlTimebase` (host clock, rate 1) and receives samples without DisplayImmediately (AVFoundation advises against mixing them).
  - **PiP source** (#1839): a 192pt-wide display layer matching the Mac screen ratio (`Theme.Size.pictureInPictureSource`), in the App's main window behind content: in the window, not hidden, but not visible. Previously 2×2pt, and attached to the full-page window when opened from there; closing the full page removed it and PiP couldn't start on real devices (TestFlight 161).
  - **Where the source lies** (#6333): the system's zoom into the small window starts at the source layer's frame, and Restore's ends there. It used to sit in the main window's bottom-left corner, so a small copy of the picture popped up there before it jumped into the small window. Now, while the full page is up, the source lies exactly over the page's whole picture (`VNCScreenUIView` reports it in screen points through `onPicture`; zoomed in it runs off the screen as the picture does), so the picture itself shrinks into the small window; with no page, it is in the top trailing corner, where the small window comes up (`VNCPiPPlacement`).
  - **Handing the picture over** (#6333): at `willStart` the page closes with the transition (`AppModel.vncPiP` set then, not at `didStart`) and its picture turns transparent, so the picture never shows twice and no blank frame shows between them; the source stays where it was while the page goes. On Restore the full page opens first, and the system is told to animate (`restoreUserInterfaceForPictureInPictureStopWithCompletionHandler`'s completion) once the page has drawn its picture and the source lies over it, or after 0.6 seconds at the latest (`VNCPiPRestore`, answered once; the wait runs on a timer of its own, so a page slow to open on a busy main thread never holds the system's restore), so the small window grows back into the page's picture. The page's picture fades as the small window starts (`Theme.Motion.conceal`), since the system's copy comes over it a few frames later, and is back at once on Restore. Left: on the iPad simulator the system sometimes draws the small window's first frames (about 0.1 s) at the size it last had, in the top-left of a window that starts at the source's frame, before the picture fills it. The old corner source, about that size, hid it; a new content source, or a new controller and source for each start, did not change it, so it stays the system's.
  - **When it opens**: once the full page connects, prepare PiP (source, controller, audio session). The Picture in Picture button calls `startPictureInPicture` directly from the tap; as it starts (`willStart`), the full page closes as if × were tapped. Going home with the full page open starts PiP automatically (`canStartPictureInPictureAutomaticallyFromInline`). Tapping VNC from a machine page or chat (with login remembered) also starts from the tap: first put a card in the source (next item), don't wait for connection. If `isPictureInPicturePossible` is false or the system doesn't respond, try again when it becomes true, after 0.4 seconds, then every second, at most 6 times, only in the foreground (background relies on automatic start). If it can't start within 8 seconds, or the system refuses (`failedToStart`), show "Picture in Picture couldn't open: reason; switched to full page" and open the full page.
  - **Card** (`VNCPiPCard`, #1839 follow-up: user found "PiP only after connecting" sluggish): before a Mac frame arrives, the source shows an `ImageRenderer` frame (black background, machine name, spinning Connecting…, every 0.1 seconds), converted to a sample with the host clock and fed to the source layer. The first frame of this connection (`RFBSession.pictured`, reset each connection) replaces it with the Mac view. Failure or disconnection shows the reason and "Tap ▶ to reconnect, or Restore to return to the App"; mark playback paused, and PiP's ▶ (`setPlaying(true)`) reconnects. Saved password wrong: in the foreground, open the full page to sign in again (PiP hands over); in the background, the card asks you to return to the App to sign in.
  - Why a card (TestFlight 161 PiP diagnostics): `startPictureInPicture` with an empty source (still connecting), even with `isPictureInPicturePossible` true, made the system do nothing—neither opening nor reporting failure. Picture in Picture on the full page (already displaying frames) opened immediately. #1928 waited for the first frame, making it work, but requiring connection first.
  - **PiP diagnostics** (full-page "…" menu): device PiP support, audio session, background audio, and each recent step (`VNCPiPLog`: tapped VNC, prepared, possible, started during tap or after waiting for a frame, opened, closed, why fallback). Copy All sends it to us. DEBUG runtime CLI `state` includes `vncPiP`; system logs use subsystem persona, category vnc.
  - **No PiP in iPhone simulators** (`isPictureInPictureSupported()` is false in iOS 27.0 and 27.1 iPhone simulators, slim or stock); tapping VNC opens the full page. DEBUG `-showPiPSource YES` shows the source over the app (without system PiP), so card → frame → disconnection (`VNCTests.testTheSmallWindowConnectsInIt`) and where the source lies, over the page's picture upright and turned, else the top trailing corner (`VNCTests.testTheSourceLiesOverThePicture`), can be checked there. A stock iPad simulator (`scripts/app/sim-boot.sh --profile stock`) has system PiP: `VNCTests.testPictureInPictureRoundTrips` (`TEST_RUNNER_PERSONA_PIP_ROUNDS=1`) goes into the small window and back twice, for recordings. How it feels on a phone still needs a real device.
- **Where VNC opens**: with that Mac's login saved, no full page open and PiP supported, go straight to PiP (`AppModel.openVNC`); otherwise full page (always full page when login is needed).
- **Remember login** (#1322):
  - Login form adds Remember on This iPhone (Remember on This Mac on Mac), checked by default. Browser pages lack this; the form uses `username` / `current-password`, leaving it to browser password management.
  - What and where: account/password per machine in the device keychain (`ScreenCredentials`), with its own service. iOS `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`, no iCloud Keychain sync; Mac login keychain. Never uploaded to the hub or logged.
  - When: only after that Mac accepts login. Unchecked Remember deletes previous saved credentials.
  - Use: when the Mac requests login, the native client (`RFBSession`) tries saved keychain credentials first, otherwise shows the form. Password is handed to the client only in App memory, never through a web page.
  - Password changed on the Mac: form returns with Saved password no longer works and account prefilled; after a successful new login, replace the saved password.
  - In Control Computer on the machine page, saved login adds Forget Screen Sharing Password beside VNC.
  - One-tap unlock (plan a-2 above) uses the same credentials.
- **Entry points**:
  - **Activity bar**: while the latest computer-control tool in the chat (assistant `computer_*`, Agent `mcp__cua-driver__*`) is running or finished within 6 seconds, the front of the task bar above input adds "Operating MacBook · tap …" (`ComputerActivityPill`). Tap to open that machine's screen.
  - **··· menu** (`ScreenMenu`, #1604): an Agent chat has "VNC · <its Mac>"; absent if not a Mac, extension off or Screen Sharing off. An assistant chat uses its most recently controlled Mac, or lists viewable Macs if none (submenu for several).
  - VNC (`ScreenEntry`) on the machine page and in tool details opens the same thing (PiP or full page).
- **Readable tools** (#1323, `ComputerSteps`): tool details show a step summary first (consecutive clicks become "tap AC → 6 → × → 7 → =", Calculator key names become symbols), then screenshot and VNC. Original JSON is tucked into Raw Input, visible only when expanded. Titles use machine display names (MacBook), resolving by id or name (same as hub), falling back to the tool's text if unknown. Truncate at the end if it doesn't fit (file paths still truncate in the middle).

### Live screen video (#3954)

A hardware video channel next to RFB, so the phone shows a Mac's screen at a fraction of today's bitrate for the same
picture (from #3067's frames: 0.27–0.31 Mbit/s as HEVC at SSIM ≥ 0.98, against 2.1–2.6 as ZRLE). RFB stays open for
input, the clipboard and as the fallback; only the picture moves to video. The iPhone offers it for a Mac whose node
advertises `screen_video`, which a node does once it has the helper (the picture switcher in ⋯ stays hidden
otherwise). The web, the Mac app and Android don't decode video yet: they keep RFB and show no switcher (the web's is
behind `webVideo.plays` in `web/src/screen/video.ts`).

- **Persona Screen** (`apple/PersonaLink/Sources/PersonaScreen`, `apple/Packages/PersonaScreenKit`): a Swift helper app inside
  Persona Link.app (`Contents/Helpers/Persona Screen.app`, bundle id `<link id>.screen`) that captures one display
  with ScreenCaptureKit and encodes it with VideoToolbox (HEVC Main, else H.264 High). It is an app bundle so its
  Screen Recording grant is its own, not the node's: the node starts it through LaunchServices
  (`open -g -n <helper> --args --socket <path> --token <t>`), and it connects back over a 0600 Unix socket, one
  per stream, whose first line is the token. `mac-link.sh` builds it the same way every time (no timestamp), so an
  unchanged helper keeps its ad-hoc signature, and its grant, across Persona Link updates. `--request-permission` calls `CGRequestScreenCaptureAccess()` and exits;
  `--preflight` prints whether Screen Recording is granted without asking (`CGPreflightScreenCaptureAccess()`), which
  the node reports as `screen_video.permission`.
  - **Capture** (`ScreenCaptureSource`): the encoder settings gate ① measured (`RealTime`, low-latency rate control,
    no frame reordering, `MaxFrameDelayCount` 0, capture queue depth 3, NV12 straight into the encoder; ≈5 ms p50 and
    ≈10 ms p95 from a screen change to an encoded frame at 1080p60). The picture's long edge is at most `start.max_size`
    (never upscaled, even sides), the frame rate 1–60, and a keyframe comes at least every 10 s and whenever asked.
  - **Messages**: `start` gets `hello` (codec, display, size, and `features` from a helper that has more, `tiers` for #6396's
    quality tiers), then `format` and a keyframe; `format` again whenever the
    parameter sets change. A still screen gives no frames, so a `heartbeat` goes out each second nothing did. Each
    second frames did, `timing` carries the helper's medians for the phone's Connection Stats (`TimingWindow`):
    capture (display time to ScreenCaptureKit's callback, about 0), encode (VideoToolbox in to out) and network (half
    the round trip from a frame going out to the phone's report on it, so no clocks need to agree), and what went out.
    `update` changes the bitrate cap, the frame rate and the size in place (a new size gets a new encoder, then `format`
    and a keyframe; no new `hello`); a new display or codec restarts capture with a fresh `hello`, `format` and keyframe. `paused` says why capture stopped (`permission`, `locked`, `login_window`,
    `display_gone`, `busy`); when the Mac unlocks it sends `resumed` and starts again. The stream closing ends the
    helper.
- **Node** (`screen.dial {id, pair, keep_awake}`, `node/screen.go`, `node/screenhelper.go`): starts the helper and
  joins its socket to the stream, or refuses with `video_unavailable` (no helper or no permission), `not_signed_in` (no
  RFB stream with this `pair` past Screen Sharing's SecurityResult) or `busy` (two streams already open). It binds the
  video to the signed-in 5900 stream carrying the same `pair` and follows that handshake's security types (30, 2, 1)
  to confirm the login; video ends when that RFB stream closes. `keep_awake` works as it does for 5900
  (`node/awake.go`).
  - **Which helper**: the one in Persona Link.app under `/Applications` or `~/Applications`. On a Mac with a node
    signing certificate (`docs/operations/mac-node-signing.md`) the node runs its own copy instead,
    `~/.persona/bin/Persona Screen.app`, signed with that certificate under the helper's bundle id, so its grant lasts
    through helper updates too. It copies and signs again only when Persona Link installs another helper (a hash of its
    executable and `Info.plist`, kept beside the copy), never touches the one inside Persona Link (that would break the
    app's seal), and runs the installed one when signing fails.
  - **Start**: the socket is in the user's own temporary directory (0700), 0600 itself, and goes as soon as the helper
    is connected; the token is 16 random bytes, compared in constant time, and the line is read unbuffered so no
    stream bytes go with it. The helper has 10 s to connect and say it.
  - **Permission**: the helper's `--preflight`, run through LaunchServices (so it is about the helper's grant), at most
    once a minute. `screen_video {version, codecs, permission, asked}` carries it (`advertiseScreenVideo`, on since the
    official TestFlight build decodes the video; apps before it show no picture switcher). `screen.dial` works either
    way.
  - **Asking once**: macOS lists Persona Screen under Screen & System Audio Recording only after the helper has asked.
    The first time an app that plays the video (its 5900 stream carries a `pair`) signs in while the helper has no
    grant, the node starts the helper with `--request-permission`, once per helper version (its stamp, kept in
    `~/.persona/bin/Persona Screen.asked`). macOS shows its alert on the Mac's own screen, which that app is showing over
    VNC at that moment; `asked` then tells the app that Persona Screen is in the list.
- **Hub** (`GET …/screen/video/connect?pair=…`): a binary WebSocket with the same checks as `…/ports/5900/connect`
  (full device, `cua` or `ego` on), counted against the same 64 streams per machine. It calls `screen.dial` and relays
  the bytes without parsing them. The iOS app's screen-sharing stream carries the pair it will bind to
  (`…/ports/5900/connect?pair=…`, `RFBWebSocket`): 16 random bytes in hex, a new one each connection, so no other
  stream can bind video to it by guessing. Hubs and nodes from before the video channel ignore the parameter.
- **Wire** (length-prefixed messages: u32 big-endian length, u8 type, body). Helper → phone: `hello`, `format`,
  `frame`, `heartbeat`, `paused`, `resumed`, `timing`, `encoding`. Phone → helper: `start`, `report`, `keyframe`, `update`. Unknown types
  are skipped by length, so either side may add more. `PersonaScreenKit` (`apple/Packages/PersonaScreenKit`, its own
  package for macOS and iOS) is the codec, shared by the helper and the app's client. `update.path`
  `{kind, via, min_rtt_ms}` (#6398) says the stream moved to another network route, so the helper can start its delay
  baseline again (#6396); helpers that don't know the field skip it. `kind` (`lan`, `tailscale`, `ipv6`, `relay`) and
  `via` (the phone's `wifi`, `cellular`, `wired` or `other` interface) name the cases of `LinkStats`, the route
  measurement the route choice and the quality tiers share (`LinkStats.swift`, with `PathChange`).
- **Adaptation** (`RateController`): the phone reports when each frame arrived; the helper turns that into a queueing
  delay and cuts the bitrate 30% over 150 ms, skips frames over 500 ms, and raises it 10% under 50 ms for 2 s. The
  delay is measured above the lowest one-way delay of the last 10 s, so the phone's and the Mac's clocks needn't agree.
  After a cut the next waits 500 ms, so one congested moment cuts once; while skipping, one frame still goes every
  250 ms, so reports keep saying when the queue has drained. A stream starts at the phone's cap but at most 4 Mbit/s,
  and never goes below 0.2. The phone caps it: 12 Mbit/s
  on Wi-Fi, 4 on cellular, 1.5 in Low Data Mode; 15 fps in Low Power Mode or a serious thermal state.
- **Quality tiers (#6396, the helper's side)**: a helper that can is a `hello` with `features: ["tiers"]` and the
  display's size in `points` and `pixels`. A phone that picks tiers then adds the tier to `start` and `update`:
  `scale` (pixels per point to encode at, never past the display's pixels, still capped by `max_size`), `min_kbps` (the
  bitrate band's floor; `max_kbps` is its ceiling) and `target_kbps` (the bitrate to go to now). The helper applies a
  new size or frame rate to the running capture and answers with `encoding` (type 8: width, height, fps, scale, band)
  once it took effect, and after `hello`. With a band set, `RateController` keeps the bitrate inside it, a cut goes to
  85% of the rate the last second's reports say arrived (30% down when they say too little), and the first 10 s raise
  25% after each calm second until the first cut; the phone moves to another tier when the band no longer fits.
  `update.path` (with or without a band) starts the 10 s delay floor, the delivered rate and the calm stretch over
  (`RateController.pathChanged`): the new route's one-way delay is its own, not a queue.
  `timing` adds `target_kbps` (the bitrate asked of the encoder; `kbps` is what went out), `skipped` (frames left out
  over the 500 ms queue), `blocked_ms` (time frames waited to be written to the stream) and `pending` (the most frames
  the encoder held at once). An app without tiers sends none of the new fields and gets the adaptation above unchanged;
  apps and helpers before it skip `encoding` by length and ignore the new JSON keys.
- **Idle refinement** (`Refinement`, `frame` flag 2; for a phone that sets a band): 300 ms after the screen stops
  changing, the latest captured picture (even one skipped while a queue drained) is encoded again at four times the
  target bitrate, then twice more 200 ms apart, so text sharpens at any tier. A pass goes only while the link is calm
  (not skipping, the last frame's queueing under 150 ms) and the encoder holds nothing; one that comes back queued past
  150 ms ends the passes until the next change. Its report changes nothing in `RateController` but a keyframe request,
  and `timing` counts only its bytes. A still screen after a size change gets its keyframe through the first pass.
  ScreenCaptureKit's queue depth is 4 for such a stream, since refinement holds one picture. Over budget, VideoToolbox's
  low-latency rate control drops whole frames, for seconds after a keyframe at the lowest tier (`screenbench`), so a
  dropped pass doesn't count and is tried again 200 ms later, up to `Refinement.attempts` (12) tries per still moment:
  the screen's final picture still goes out once the budget allows.
- **Quality tiers (#6396, the phone's side)** (`PersonaScreenKit`: `TierLadder`, `ReceiverSignals`, `TierPolicy`):
  - **Ladder**: a tier is a scale (pixels per point) and a frame rate, best first. Auto steps 2×60, 1.5×60, 1.5×30, 1×30,
    1×15; Sharper gives up frame rate first (2×60, 2×30, 2×15, 1.5×15, 1×15), Smoother pixels (2×60, 1.5×60, 1×60, 1×30,
    1×15). Every tier keeps at least a pixel per point, so text stays readable. Each band is 0.02–0.08 bits per pixel per
    frame, the ceiling at the phone's cap; a tier whose floor is above the cap is left out, and ones that come out the
    same on a display merge. Each tier also carries the scale its picture really has (`pixelsPerPoint`, #7842): on a
    display at one pixel per point every tier is 1×, and 2× under the phone's long-edge cap is less; the tier line shows
    it and Data Saver's one pixel per point holds to it. On `screenbench` the floor keeps scrolling text at SSIM ≥ 0.98 against what was encoded.
    The band is per codec (`TierLadder.bitsFactor`; #6558: a browser without HEVC gets H.264). H.264's factor is 1, the
    same as HEVC's, as `screenbench` measured it (2026-11-14, every Auto tier, scene and band end at the same targets):
    text (typing, scrolling) within 0.002 SSIM of HEVC, the video scene 0.003–0.007 lower, and 0–11% more data an hour
    of the typical mix, so the cards' figures hold for both.
  - **Shared with the browser** (#6558): `spec/fixtures/screen-tiers` holds the ladder's and the signals' scenarios
    (input and expected output, one per file), which `TierFixtureTests` writes from these Swift types
    (`PERSONA_WRITE_TIER_FIXTURES=1`) and otherwise checks unchanged. The browser's port (`web/src/screen/tiers/`)
    replays them, so a change on either side fails both until they agree; so do the policy's timelines (route starts, every way down and up, a route change, a new bias, Data Saver and a warm device).
  - **Signals** (the phone's, from the frames): queueing above the 10 s floor of the one-way delay (p50 / p95 over
    0.5 s, rising or falling over 1 s), the rate the link carried while busy (frames queued behind one another: their
    bytes over their arrival gaps, so quiet stretches don't read as a slow link; frames read together all count, #7842),
    bursts of large frames, late frames, decode time and pictures replaced before they showed. A keyframe (one every
    10 s) and the frames queued behind it are left out of the p95 that says whether the link is calm: that queue is the
    keyframe's size, not a slower link, and its burst is the proof of headroom (#7842). The route's round trip, jitter and loss are `LinkStats`'.
  - **Policy**, every 250 ms. A tier fits the link when its floor is at most 80% of what the link carries. Down, fast:
    a long queue (over 400 ms for a quarter second, or skipped frames) or one the bitrate can't fix (the helper's
    bitrate within 15% of the band's floor and over 150 ms), with a tier that no longer fits, goes straight to the best
    tier that does; another step down waits 1 s and a queue that isn't draining, unless the queue runs away. A phone that
    can't decode or show as fast for 2 s gets a lower frame rate. Up, slow: one tier after a calm window (8 s; 3 s in the
    first 10 s or after a route change) with proof the link carried 1.25× the next floor, or three large frames at 1.5×
    it; a step up undone within 15 s doubles that tier's window, to 60 s. The start comes from the route: the same
    network the best tier, Tailscale (direct IPv6 too) under 30 ms the best else the second, the relay the second over
    Wi-Fi, the third over cellular. A slower route steps down at once; a new bias or cap takes the tier the rate fits,
    the rate at least the current tier's floor, which the link carries. The helper's `timing` comes once a second only
    while frames flow, so its skipped frames count until the next is due (1.25 s) and its bitrate proves headroom once:
    a still screen never steps down or up on an old second's numbers (#7842).
  - **Simulated links** (`TierPolicySimulationTests`, the real types over a shaped link): a steady link never moves in
    10 minutes; the relay settles on 2×60 in 5 s; 25 → 3 Mbit/s steps down in 1 s to 1.5×30 and holds (queueing p50 32
    / p95 179 ms, against 543 / 1,033 with 2×60 fixed); a link jittering 5–9 Mbit/s for 10 minutes never moves. On a
    display at one pixel per point with light content (scrolling text) and a keyframe every 10 s, as on a real Mac
    (#7842), a 3 Mbit/s cap steps down once, to 1.5×30, frames read one at a time or several together, and back to 2×60
    20.5 s after the cap lifts.
  - **The app** (`ScreenVideoTiers`, `ScreenVideoStream`, `ScreenVideoLink`): when the helper's `hello` lists `tiers`
    with the display's points and pixels and this app has them (`ScreenTiers.appSupported`), the stream starts a policy
    and sends its first tier as an `update` (`scale`, `fps`, `min_kbps`, `max_kbps`, `target_kbps`) once the first frame
    arrives: on `hello` the helper's capture hasn't really started, and ScreenCaptureKit drops a configuration change
    made then, so capture stayed at 60 fps (#7842). Every tier change waits the same way after `hello` and after
    `paused`, the latest one kept. The stream feeds it
    every frame from its own task (arrival, the time a frame spread over several reads began, decode time, pictures
    replaced before they showed) and ticks it every 250 ms; a new tier goes out as an `update` and to the link (the
    overlay's tier and reason). ⋯ › Quality changes the bias on the running stream; a route change (#6398) restarts the
    phone's delay floor and lets the policy look again; the tier each computer settles on, by kind of route, is where its
    next stream starts in this app run. ⋯ › Quality shows only with tiers on both sides.
  - **Mobile data** (`ScreenMobileData`, the user's choice, for every computer, Settings › Live Screen › Quality on Mobile
    Data): on an expensive network (cellular or a personal hotspot, `NWPath.isExpensive`) with tiers, Clearer keeps the
    4 Mbit/s cap (about 0.6 GB an hour of typical use on `screenbench`), Data Saver 1.5 Mbit/s at one pixel per point
    (about 0.2–0.3 GB an hour, `TierPolicy`'s `maxScale`); Low Data Mode is always Data Saver. Until the user chooses, the
    picture starts on Data Saver and the page asks once (`ScreenMobileDataCard`, titled for cellular or a hotspot by
    whether the path uses the cellular interface); unanswered, it asks once more next time, then no more. Without tiers
    the caps are as before.
- **Switching**: RFB signs in first and shows until the first video frame decodes; the phone then stops asking RFB for
  updates (Screen Sharing encodes nothing meanwhile). It returns to RFB within about a second on any of: the stream
  closes, no frame or heartbeat for 3 s, a decoding error, or the Mac locks (`paused`), then retries video after 30 s
  doubling to 5 min, and at once when the helper reports the Mac unlocked (`resumed`). While capture is paused the
  helper sends no heartbeat, so the 3 s rule waits for `resumed`.
- **iPhone** (`apple/Persona/Screen/ScreenVideoStream.swift`, `ScreenVideoLink.swift`): the iOS app links
  `PersonaScreenKit` (Mac and Vision compile the same sources and leave video out). `ScreenVideoStream` sends `start`,
  decodes each frame with VideoToolbox as it comes (the helper never reorders), sends a `report` for every frame, and
  shows only the newest picture of a burst. `ScreenVideoLink`, one per RFB session, opens the stream once RFB is
  signed in, shows its pictures in a layer over RFB's on the same display (`hello.rect`, else the display shown; RFB's layer is
  hidden meanwhile, so a picture of another shape leaves black, not a stale frame), and holds RFB's update requests while
  they show; the request held goes out when video stops, so RFB's picture catches up.
  Video is chosen by default on a Mac that offers it (⋯ › Picture, kept per Mac; ⋯ › Quality appears only once the
  helper's `hello.features` has `tiers` and the app has its side of them too, `ScreenTiers.appSupported`, since one
  release carries both and before #6396's iPhone policy the choice would change nothing) and runs only while one display is
  shown: the Mac's only one, or one picked from several the node numbered; all of several displays at once stay RFB.
  It pauses while Picture in Picture shows the screen or the app is in the background (RFB carries the picture then).
  When VNC shows instead, a line under the bars says why in plain words: the video stopped, the Mac is locked, or the
  Mac hasn't allowed video yet (`permission` missing, so no stream is opened).
  Connection Stats (⋯, off by default) reads the video once a second: frames shown and megabits that arrived, the
  helper's capture, encode and network from `timing`, the phone's decode, and their total first when encode, network
  and decode are all known (a helper before `timing` gives the phone's numbers only). With quality tiers (#6396) a Tier
  line shows the Quality choice, the picture's own scale and frame rate ("Auto · 1.5× · 60 fps", `ScreenTierText`;
"1×" on a display at one pixel per point, #7842) and why it last
  changed in plain words with how long ago (picked for this connection, the connection slowed down or is steady, the
  network changed, the iPhone is warm or saving power or couldn't keep up, you changed Quality); the resolution is the
  encoder's after an in-place change (`encoding`), not `hello`'s. Over VNC it shows RFB's bitrate
  and frame rate and, on a line of its own, **Round trip to the hub** (#6558): while the overlay is shown and the app is
  in front, the page pings the app's own connection to that Mac's hub every 2 s (`LiveConnection.roundTrip`, the
  heartbeat's kind of ping) and shows the median of the last 5 from the last 20 s (`HubRoundTrips`). It is the same leg
  the web overlay times ("Connection stats on the web"), and like it never sits on the first line. Bitrates have two
  decimals under 1 Mbps, one above, as on the web.
- **Browser** (#6558; off for users until it has been checked on a real Mac in each browser: `video.ts` `webVideo.plays`,
  on only in a development server started with `VITE_SCREEN_VIDEO=1`):
  - **Wire and decoding**: `web/src/screen/video-wire.ts` frames the wire as `PersonaScreenKit` does (both replay
    `spec/fixtures/screen-video`, written from Swift's own messages by `ScreenVideoFixtureTests`); `video-bitstream.ts`
    turns `format` into WebCodecs' codec string (`hvc1.1.6.L93.B0`, `avc1.64001F`) and decoder configuration record.
  - **What the browser offers** (`video-probe.ts`, once a page): WebCodecs exists only on a secure address (https or
    localhost); HEVC counts only with a hardware decoder, H.264 either way (`isConfigSupported`, never the user agent).
    `start.codecs` is what passed, HEVC first; nothing passed means no Picture switcher and VNC.
  - **The channel** (`video-stream.ts`, `video-link.ts`, `VideoPicture.tsx`, a chunk loaded only for a Mac that offers
    video): the RFB connection carries a `pair`, and once it signs in the page opens `…/screen/video/connect` with it
    over the hub's relay (a transport function, where #6398's browser route will plug in). Each picture is decoded as
    it comes, the newest of a burst drawn on a canvas over noVNC's at `hello.rect` (noVNC's canvas stays underneath,
    clear, and keeps the pointer and keyboard), and a `report` goes back for every frame. While video shows, noVNC's
    update requests are held (`rfb-hold.ts`, a guarded hook on `RFB.messages.fbUpdateRequest`; the last one goes out
    when video stops). The iPhone's rules: video by default where it plays, one display at a time, back to VNC with the
    same three lines (the Mac locked, video stopped, not allowed with "How to turn it on"), 3 s of silence, 30 s doubling
    to 5 min, reset after a minute of video, and nothing while the tab is in the background. A decoder that fails on the
    stream before any picture means this browser can't play it: no switcher, no retry on that page.
  - **Connection stats**: video mode shows the iPhone's lines (Video · Via hub · total; with tiers, Tier · Quality ·
    scale · fps and, under it, why it last changed and how long ago; bitrate · fps · codec · size, the size the
    encoder runs at after an `encoding`; capture · encode · network · decode); over VNC, when the Mac offers video but
    this page can't play it, one line says why: not a secure address, a browser that can't play it, or a decoder that
    couldn't start.
  - **Quality tiers** (`video-tiers.ts`, the iPhone's `ScreenVideoTiers`, on the ported `tiers/` types): once the
    helper's `hello` lists `tiers` with its display's points and pixels, the page's tiers start (`webTiers.supported`).
    The stream feeds `ReceiverSignals` from each frame as it arrives (when its first bytes came, for a frame over
    several reads; refinement passes apart), from each decode time and from each picture drawn or replaced before the
    next animation frame; the helper's `timing` goes to the policy, which decides every 250 ms (not while capture is
    paused). Each change goes out as an `update` (`scale`, `fps`, `min_kbps`, `target_kbps`, `max_kbps`). ⋯ › Quality
    (Auto, Sharper, Smoother, kept per Mac) moves the bias at once; the data cap and the mobile-data choice go to the
    policy (`setBudget`). The route is the hub's relay, read as an unmeasured one (the relay over Wi-Fi); the tier a
    computer settles on is where its next stream on the page starts. A helper without tiers keeps the cap as before.
  - **Data use** (`data-use.ts`, the iPhone's `ScreenVideoBudget` and `ScreenMobileData`): the cap goes in
    `start.max_kbps` and, when `navigator.connection` changes, in an `update`. Without tiers the caps are the iPhone's:
    1.5 Mbit/s with the browser's Data Saver on (`saveData`, its Low Data Mode), 4 on mobile data, 12 otherwise. Chrome
    and Edge say `type: "cellular"`; Safari and Firefox say nothing, so a phone's browser (a coarse pointer and a screen
    no wider than 600 CSS pixels on its short side) gets 4 and nothing is asked, any other 12. With tiers on both sides
    mobile data follows the user's choice as on the iPhone: Data Saver (1.5 Mbit/s and at most one pixel per point,
    the policy's `maxScale`) until they choose, Clearer 4. The stream starts on the cap without tiers, which aren't
    known before `hello`; the tiers then pick under the cap with the choice. The card (`MobileDataCard`: On mobile data, The screen is
    saving data. Make it clearer?, Clearer about 0.6 GB an hour, Data Saver about 0.2–0.3 GB an hour) sits under the bar
    while video shows, is asked at most twice while nothing is chosen (once a page), and the choice is kept in this
    browser for every computer (`persona.screen.mobileData`). The web has no Settings row for it: it sits in ⋯ as
    "Quality on mobile data" while it applies. The browser's Data Saver is always Data Saver and asks nothing. The card
    and the choice show only with a helper that has tiers.
  - **Tests**: `video-wire.test.ts`, `video-bitstream.test.ts`, `video-probe.test.ts`, `video-stream.test.ts`,
    `video-link.test.ts`, `video-tiers.test.ts`, `rfb-hold.test.ts` (on the real noVNC), `data-use.test.ts`,
    `VideoPicture.test.tsx`, `ScreenView.video.test.tsx`; `node web/e2e/screen-video-decode.mjs <web URL>` decodes both fixtures in a real
    browser, `node web/e2e/screen-video.mjs <web URL> <mock hub URL>` runs the page against the mock's helper side
    (`MOCK_SCREEN_VIDEO=1`, which plays `fixture.bin` (HEVC) or `fixture-h264.bin` as the helper picks from
    `start.codecs`), and `node web/e2e/screen-data-use.mjs <web URL> <mock hub URL>` the tiers against a helper with
    them (`MOCK_SCREEN_VIDEO_TIERS=1`): the tier under each data choice, the card and the choice, and on Wi-Fi the
    tier in the stats and Quality moving it.
- **Permission and login**: only input on that Mac answers macOS's Screen Recording question — someone at the Mac, or
  the user's own taps through Screen Sharing. Nothing in Persona answers it, and Agents' computer-use calls can't target
  System Settings' Privacy & Security or the system alert. Video needs the same login as Screen Sharing (the `pair`
  binding); no password travels anywhere new. `computer.status` / `browser.status` gain `screen_video {version, codecs,
  permission, asked}` (absent from an old node or one without the helper, so an old app keeps RFB). The phone's "How to
  turn it on" follows `asked`: before it, the Mac is about to show a window (choose Open System Settings, then turn on
  Persona Screen); after it, where in System Settings to turn it on.

### Live screen routes (#6398)

The live screen, picture and input together, takes the fastest route the phone can reach: the same local network,
Tailscale, or the hub relay ([#6398](https://github.com/nwverse/persona/issues/6398) has the design). The node's end is
`internal/screenlink`. What is in place: the screen session over the hub's relays and the node's direct routes; the
iPhone's side comes next.

- **Screen session**: a phone that asks for one gets its RFB stream and its video stream as channels of one session at
  the node, keyed by `pair`: channel 1 is the connection to Screen Sharing (still followed for the login, so video binds
  to the pair as before), and each video stream is a channel of its own from 2 up (Persona Screen's socket). Each channel
  is a resumable byte stream: bytes carry their offset, the receiver acknowledges them, and the sender keeps what isn't
  acknowledged yet, so any of the session's transports can carry a channel on from where the other side is.
  **A transport only carries a session the hub opened**: Screen Sharing is dialed and Persona Screen started through
  `port.dial` / `screen.dial` exactly as before; a transport joining later sends the session's token first.
- **Asking for it**: the phone offers the WebSocket subprotocol `persona-screen-link.1` (beside `binary`) on
  `…/ports/5900/connect?pair=…` and `…/screen/video/connect?pair=…`. The hub then passes `link: true` in `port.dial` /
  `screen.dial`, and selects the subprotocol only when the node answers `{link: 1}`. The web's noVNC (`binary`), apps
  before it (no subprotocol), a hub before it (never passes `link`) and a node before it (answers `{}`) all keep the raw
  stream, byte for byte. hub.json's `screen_link_off` turns sessions off for everyone: the hub then never passes `link` on.
- **Wire**: the video wire's framing (u32 length, u8 type, body) with its own types: `data` (channel, offset, bytes),
  `ack` (channel, received through), `paths` (the token; candidates and the certificate with the direct routes), `attach`
  / `attached` (a transport joining with the token), `ping` / `pong` (the pong adds the node's TCP retransmitted and sent
  packets on a direct route; 0 on a relay), `use` / `resumed` (carry these channels on this transport from these
  offsets), `close` (a channel's end) and `reset` (the video channel goes on at a message boundary). A new video
  channel's relay starts with `resumed` naming it.
- **Limits** (`screenlink.Limits`): the node keeps at most 4 MB of RFB and 512 KB of video unacknowledged, and reads
  Screen Sharing or Persona Screen only with room (Screen Sharing waits; the helper drops frames as it already does when
  the stream is full), so memory never grows with how long a route is down. Bytes left unacknowledged for 10 s mean the
  transport carrying them is stuck: it is closed and the channel goes on elsewhere. A channel with no transport for 10 s
  closes (RFB's ends the session), and so does a session with no hub relay for 10 s, so revoking the device or turning
  the screen off on the hub also ends it.
- **Moving a channel**: RFB is always sent on exactly from the phone's offset; its protocol state can't lose a byte.
  Video with a backlog older than 200 ms is not replayed: the node drops it, sends `reset` with where the stream goes
  on, asks Persona Screen for a keyframe (between two of the phone's messages, never inside one) and drops frames until
  it comes, so a late picture replaces a burst of old ones and the rate control isn't misled by their delay. When the
  transport carrying a channel fails, the node moves it to the channel's own relay (or another transport) by itself.
- **Direct routes** (`screenlink.Direct`): while at least one session is live, the node listens on one TCP port bound
  to each of its candidate addresses (never the wildcard), and closes the listener with the last session. Candidates
  come from its own interfaces, with no Tailscale API or command: private IPv4 and unique-local IPv6 on Ethernet or
  Wi-Fi (`lan`, with the prefix length so the phone can tell whether it is on the same network), and Tailscale's ranges
  (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) on a point-to-point interface (`tailscale`). Loopback, link-local, global
  addresses (not in v1), proxies' tunnels and their fake-IP range (RFC 2544's benchmark block, 198.18/15), other VPNs, and VM or container
  bridges are left out, and so are MagicDNS names. At most 8; the node reads them again every 5 s and sends new `paths`
  when they change. A connection must finish TLS 1.3 with ALPN `persona-screen/1` against a self-signed P-256
  certificate made per node process (named `screen.persona.invalid`; `paths` carries its SHA-256 for the phone to
  pin), then send `attach` with a live session's token, all within 3 s; at most 16 handshakes at once and 8 direct
  transports per session. Its pongs carry the TCP packets retransmitted and sent on it since the last pong (macOS
  `TCP_CONNECTION_INFO`, Linux `TCP_INFO`), the phone's measure of loss. When macOS's application firewall is on and the
  node isn't explicitly allowed through it (read with `socketfilterfw`, at most once a minute, never changed), the node
  doesn't listen and `paths` says `unavailable: "firewall"`, so no prompt appears on the Mac.
- **The phone's side** (`PersonaScreenKit`, pure and tested with `swift test`): `ScreenLinkMessage` frames the wire
  exactly as the node does (both check `spec/fixtures/screen-link`); `LinkReceiver` / `LinkSender` keep each channel's
  offsets (bytes once and in order, what the node hasn't acknowledged kept within a window); `VideoMessageAssembler`
  hands the decoder whole video messages only and drops a partial one on `reset`; `LinkMeter` turns pongs into
  `LinkStats`; `RouteEligibility` names the phone interface a candidate may be dialed on (a LAN address only from
  Wi-Fi or Ethernet on its subnet, a Tailscale address only through the phone's own Tailscale tunnel, never cellular,
  another VPN or a proxy); `PathPolicy` picks the route: by minimum round trip, a direct route winning a tie with the
  relay; at the stream's start at once, later only after 10 s healthy, a clear gain and 20 s since the last move; a lost
  route left at once and a degraded one (loss ≥ 5 %, or 50 ms over its own floor for 5 s) when another is healthy.
- **The iPhone** (`apple/Persona/Screen/ScreenLinkRoutes.swift`): on since the acceptance before real devices passed
  (a simulator on a real hub and a Mac on the same LAN: the direct route within a second, back on the relay 2.6 s after
  it went silent and 1.1 s after it broke, direct again 39 s after it recovered, every typed byte once). Still to check
  on a real iPhone: the Tailscale route, the local network question, and Wi-Fi to cellular. `ScreenRoutes.releaseEnabled`
  false turns it off in the next build; the hub's `screen_link_off` keeps every live screen on its relays at once.
  **Settings › This Device › Live Screen** (`LiveScreenSettingsView`, shown when a computer offers its live screen)
  has one switch, "Choose the Fastest Route Automatically", on by default; turned off, the
  phone asks for no session (everything through the hub, as before) and live sessions move back to their relays at
  once. The quality tiers (#6396) add their own section to the same page, and Scroll Speed (#7775) a third: Slower,
  Default or Faster (`VNCScrollSpeed`, this device's, for every computer: 0.6×, 1× and 1.6× the two-finger scroll's
  distance, slow drags too; the top speed stays Screen Sharing's). Since Scroll Speed applies to every computer that
  shows its live screen, the page is there whenever one does. The stats overlay's Route row shows the route's
  round trip and packet loss and why it last changed ("Faster route", "Previous route dropped", "Network changed", …, or
  that the computer's firewall blocks direct connections). The first try of a same-network route shows iOS's local
  network question with `NSLocalNetworkUsageDescription`; refused, those routes fail quietly and the relay stays.
  How it works: the RFB and video relays offer the subprotocol beside `binary`; `ScreenLinkRFBStream` /
  `ScreenLinkVideoStream` (`PersonaScreenKit`) read the first message, and when the hub selected the subprotocol the
  stream is the session's channel (`ScreenLinkSession`, an actor holding the channels, the transports, the pings and
  `PathPolicy`), else the same raw stream as before, so the RFB client and the video stream don't change. A session
  starts a `ScreenLinkProber` (its own actor): it reads the phone's interfaces (`NWPathMonitor`, `getifaddrs`), dials each
  candidate `RouteEligibility` allows on that interface (`NWParameters.requiredInterface`, TLS 1.3 with the ALPN, the
  certificate accepted only by its SHA-256 from `paths`) as `ProbePlan` says (at once, then 30 s doubling to 5 min after a
  failure, again at once when the phone's networks change), and hands what connects to the session, which pings and
  measures every route (the one in use every second, the others every two) and runs the policy once a second.
- **Moving**: when the policy picks another route, every live channel moves
  there (to its own relay for the relay): `use` with what the phone has of each, and nothing more goes out on it until
  the node's `resumed` says what the node has; each side then sends on from there, so RFB loses and repeats nothing,
  and a stale video backlog is dropped for a keyframe on the node's side. A direct route that fails, or stays silent for
  1.5 s with a ping out, puts the channels back on their relays at once (the node does the same on its side). A relay
  that drops while a direct route carries the session doesn't end it: the session asks the app to open the relay again
  (`attach=1`), which attaches with the token and is the channel's relay again, so the hub keeps the last word. A new
  video stream joins the route in use. Each move is a `PathChange` (the stream's first route is one too, `start`):
  `ScreenVideoLink` sends `update.path` to Persona Screen, keeps it as `route` and calls `onPathChange`, the quality
  tiers' hook (#6396). Logs (category `screen-link`) carry route kinds and reasons, never addresses.
- **Rejoining**: a relay that dropped while the session went on (a hub restart, the phone changing networks) is opened
  again with the same `pair` and `attach=1`; the node attaches it to the live session instead of dialing Screen Sharing
  or starting Persona Screen again (`session_gone`, 410 at the hub, when there is none). The phone then sends `use`.

### Pixel scrolling (#7775)

Two-finger scrolling over RFB sends wheel notches, and macOS's Screen Sharing scrolls a notch one point: a page follows
the fingers, but Screen Sharing falls behind past about 2000 notches a second, a terminal moves a whole line per event
(notches that come together merge), and a page stops dead at its ends. Continuous scroll events posted on the Mac
itself, as a trackpad's are, scroll by the point with no such ceiling, with the app's own momentum and rubber-banding.
Approved on #7775: a helper of its own posts them, with the Accessibility permission asked for once; without it,
scrolling stays on RFB's notches.

- **Persona Input** (`apple/PersonaLink/Input`, a package of its own; bundle `Persona Link.app/Contents/Helpers/Persona
  Input.app`, bundle id `<link id>.input`): started by the node through LaunchServices as Persona Screen is, on a 0600
  Unix socket whose first line is a token. It then says `granted` or `missing` (`CGPreflightPostEventAccess()`; missing
  ends it) and posts every scroll record that comes as `CGEvent(scrollWheelEvent2Source:units: .pixel, …)` with
  `scrollWheelEventIsContinuous`, the scroll phase and the momentum phase, at the pointer (RFB has just put it there),
  carrying fractions of a point to the next event. Scroll events only: no clicks, no keys. `--preflight` prints
  `post_event: granted|missing`, asking nothing; `--request-permission` asks macOS once (`CGRequestPostEventAccess()`),
  whose window only the user answers. Persona Screen is not touched: its package, binary and Screen Recording grant stay
  as they were, and the two share no code.
- **Same bits from build to build**: macOS ties the permission to the helper's code. The package builds in Swift 6 mode
  (`#file` is the module and file name) and links no debug map, so a build does not depend on its path:
  `scripts/app/mac-link.sh verify-input` builds it twice from clean copies at two paths and compares the executable and
  the signed bundle's cdhash (the same on 2026-11-15: executable `659dc1e66ff90a6f`, cdhash `da868531…`). On top of that,
  `mac-link.sh` keeps the built executable in `~/.persona/cache/persona-input/<key>` (`PERSONA_INPUT_CACHE`), the key a
  hash of the helper's and `PersonaInputKit`'s sources and the architectures, and reuses it byte for byte: a Persona Link
  update that leaves those sources alone never changes the helper, whatever checkout or Xcode builds it.
- **The record** (`PersonaInputKit`'s `ScrollRecord`, the helper and the iOS app; the node's codec in Go, both checked
  against `spec/fixtures/screen-input`): 16 bytes, big-endian. Byte 0 is **200**, an RFB client message type that IANA's
  registry leaves unassigned (151–244 are, checked 2026-11-15); the phone sends it inside the RFB stream and the node
  takes it out, so Screen Sharing never sees it. Then the version (1), the kind (0 hello: the phone can scroll by pixels;
  1 scroll), the scroll phase (CGScrollPhase: 0 none, 1 began, 2 changed, 4 ended, 8 cancelled), the momentum phase
  (CGMomentumScrollPhase: 0 none, 1 begin, 2 continue, 3 end), three reserved bytes, then dy and dx as `f32` Mac points
  (> 0 shows what is above or to the left, as the RFB wheel's up and CGEvent's axes). Another version is refused, never
  guessed at. The node hands the helper the same 16 bytes.
- Still to come on #7775: the node reading the phone's messages after the login and taking the record out, its status
  `screen_input {version, permission, asked}`, asking once on first use, and falling back to wheel notches; then the
  phone sending the record.

## Locking, preventing sleep and per-App approval (plan, #1507, decided)

Reference: Computer Use settings in the Codex desktop app. The main session read only the UI strings in its package, took no code, and found:
- Locked use;
- click sounds (none / foreground only / foreground and background);
- per-App approval list;
- live control for the three Office apps;
- read / send permissions for Messages.

### Current behavior

- **Doesn't work while locked**:
  - The node reads `locked` from `IOConsoleUsers`; the machine page says "This Mac is locked; unlock it to control it", as do assistant tools.
  - Since Cua Driver 0.37, all channels reject calls while locked with `screen_locked` ([trycua/cua#5483](https://github.com/trycua/cua/pull/5483)).
  - Empirical report ([trycua/cua#1745](https://github.com/trycua/cua/issues/1745)): Accessibility can only reach the menu bar while locked, and window screenshots are 0×0.
  - Real keyboard/mouse input lands on the lock screen, so rejecting it is correct.
- **When macOS locks itself**:
  - When the screen saver starts or the display dims from inactivity (`kLWLockFromDisplayDim`), particularly with Require Password set to Immediately.
  - So the Mac may lock from inactivity while an Agent operates it, or a user watches on the phone without tapping.
- **Cua Driver already handles half**:
  - After each tool call it holds a `PreventUserIdleDisplaySleep` assertion, lasting 10 minutes after the last call. `CUA_DRIVER_KEEP_DISPLAY_AWAKE=<seconds>` changes the duration; `0` disables it.
  - It doesn't wake an already sleeping display or bypass a lock.
  - Only covers calls through cua-driver, not VNC or an Agent thinking for more than 10 minutes without calls.

### a) Use while locked

Three approaches; recommend 1 + 2, not 3.

1. **Prevent locking**: b below. Simplest, reversible anytime. Cost: the Mac is open to anyone sitting in front of it during the unlocked period.
2. **Unlock entry: user unlocks it**:
   - When that Mac is locked, offer Unlock in the machine page, drawer and assistant hints, opening live view. Screen Sharing supports viewing and typing while locked; the user enters the password on the lock screen. Works already, only needs an entry point.
   - Combined with #1322 (remember Mac login password in the phone's device keychain, require Face ID), make it one tap: retrieve the password on the phone and type it into the lock screen password field via VNC keyboard events.
     - Password only crosses the tunnel; not persisted on the hub, logged or checked in.
     - Hub log records only who, when and which machine was unlocked.
   - **Security boundary**:
     - Phone + Face ID can unlock that Mac. Therefore off by default, enabled per Mac, with one confirmation on the phone when enabled.
     - Doesn't automatically relock afterward. Drawer reminds "Remember to lock when done" and offers Lock (`pmset displaysleepnow`, locks when the display sleeps).
   - **One-time action on the Mac**: enable Screen Sharing, already required for VNC. Nothing else.
   - **Out of scope**:
     - FileVault unlock after reboot: Screen Sharing can't connect at that stage.
     - Sleeping MacBook with lid closed: the node is disconnected too.
3. **An authorization plug-in like Codex**:
   - Codex installs an Apple authorization plug-in participating in macOS unlocking. It temporarily unlocks only during its own active operations, covers all displays and relocks on local keyboard/mouse input ([OpenAI documentation](https://developers.openai.com/codex/app/computer-use)).
   - Costs:
     - Administrator installation; changes system authentication rules (`authorizationdb`).
     - Bugs could prevent locking or unlocking. The plug-in itself adds attack surface: compromising the hub or phone means unlocking the Mac.
     - Still can't work with the lid closed.
   - Cua has the same plan (#1745 phase 2: `cua-driver unlock-screen --purpose … --duration 30s`), not implemented yet.
   - **Not doing this**. Reassess integration when Cua offers it; don't write our own system plug-in.

- **One-tap unlock (a-2, implemented)**:
  - After remembering Screen Sharing login (#1322), Control Computer on the machine page adds One-Tap Unlock on This iPhone (`ScreenUnlock`, stored on this device per Mac), off by default, confirmed once when enabled. Forgetting the password disables it too.
  - A locked Mac offers One-Tap Unlock:
    1. Face ID / Touch ID / device passcode first (`LAContext`).
    2. Open full page (`unlock`); native client signs in to Screen Sharing with the saved account.
    3. After connecting, press Shift to wake the lock screen, then type the saved password and Return as keysyms (`RFBSession.unlockWithKeptPassword`). Password only lives in App memory.
  - Hub logs only who, when and which machine (`POST …/computer/unlocking`); password never enters its storage or logs.
  - With one-tap unlock enabled, the machine page offers Lock after unlocking (`POST …/computer/lock` → node `pmset displaysleepnow`: display sleeps immediately; locks if Require Password is Immediately).
  - Limits: no FileVault unlock after reboot or sleeping MacBook with closed lid (Screen Sharing can't connect).

### b) Prevent sleep and automatic locking (`cua` extension settings, two switches per machine)

Both use `caffeinate`, without changing system settings (no changes to Lock Screen timers or Require Password). Disabling them or exiting the node restores normal behavior.

Flags used:
- `-d`: prevent display sleep;
- `-i`: prevent idle system sleep;
- `-m`: prevent disk sleep;
- `-s`: prevent system sleep, on AC power only;
- `-u`: declare user activity, waking a sleeping display;
- `-t`: duration in seconds;
- `-w <pid>`: remove when that process exits.

- **Keep Awake During Control and VNC** (implemented, `cua` extension `keep_awake`, on by default):
  - One global extension switch, not per Mac. The extension's per-machine switches can't default to on; this only applies during control and VNC and withdraws itself afterward, so per-machine settings add little. Only No Automatic Lock below is per machine.
  - The node holds one `caffeinate -dimsu -t <seconds> -w <node pid>` at a time (`node/awake.go`):
    - assistant `computer_*` calls: hub includes `keep_awake` in `computer.call`; expires 10 minutes after the last call;
    - someone on VNC: hub includes `keep_awake` in 5900 `port.dial`; continually renews while the stream is open (replace before expiration without a gap), withdraws on stream close. Watching without tapping isn't local activity, so previously the Mac locked while being watched.
  - An Agent's own Cua MCP (`mcp__cua-driver__*`) bypasses the node. Cua 0.37+ keeps awake for 10 minutes after each call itself, so the node doesn't scan sessions.
  - Mac only. Verified (`TestAwakeOnThisMac`, `PERSONA_AWAKE_E2E=1`): `pmset -g assertions` shows this caffeinate's `PreventUserIdleDisplaySleep`, disappearing on removal.
- **Unlock entry while locked** (implemented): Control Computer says "This Mac is locked; unlock it to control it". With Screen Sharing enabled, Unlock opens live view: switch to Take Over and enter the login password on the lock screen. Node guidance now also says "With Screen Sharing enabled, enter your login password in its view to unlock".
- **Prevent Automatic Lock While Extension Is Enabled** (implemented, #1507 b2, `cua` extension `no_auto_lock`: per-Mac list, empty by default):
  - Confirm on enable: the machine page switch shows a confirmation; the extension page's per-machine switch reads schema `x-confirm` (generic extension form convention, [extensions.md](extensions.md)), with an App confirmation dialog and web `window.confirm`. No confirmation when disabling.
  - When both the extension and this switch are enabled for the machine, the node holds `caffeinate -dims -t 1800 -w <node pid>` (`computer.nolock` in `node/awake.go`, separate from Keep Awake).
  - Hub sends it on switch/extension change and node connection, then every 10 minutes (`extmgr/computer_nolock.go`). Each receipt replaces it with a new 30-minute assertion; if the hub stops sending (disconnected), it expires after 30 minutes and timed locking resumes.
  - No `-u`, so it doesn't wake an already sleeping display.
  - Withdraws when switch/extension is disabled (hub immediately sends off), node exits, or node and hub have been disconnected 30 minutes.
  - Changing the machine page switch logs one hub line (machine, on/off).
  - **Can't prevent**:
    - manual locking (Control-Command-Q, hot corners);
    - lid closing;
    - fast user switching;
    - enforced screen-saver lock policies on a company-managed Mac; verify empirically.
  - **Security boundary**: this Mac remains unlocked, so nearby people can use it directly. Machine and extension pages say so below the switch; hub logs toggle times.
  - **Unlike Codex**: Codex operates while locked (requires an authorization plug-in); this prevents locking. Simple and fully reversible, but the Mac is exposed during that unlocked period.

### c) Per-App approval (implemented)

- **Asking**:
  - `cua` extension Ask Before First Operating an App (`app_approval`), on by default.
  - The first time the assistant operates an App on a Mac (or views the whole screen), the phone shows a card: "Let the assistant operate Calculator on MacBook", options Allow / "Don't ask again for Calculator" / Deny.
  - Always Allow is stored by bundle id in hub settings (`computer.apps`, per machine), preventing future prompts.
  - Allowed Apps for Assistant in Control Computer lists each one; Remove deletes the saved approval. Future prompts follow `app_approval`; removing a record does not enable that setting (`GET` / `DELETE /api/v1/machines/{mid}/computer/apps[/{bundle}]`).
  - The web sends `?expected_manager=<manager id>` and requires the hub's successful `X-Persona-Computer-Apps-Owner` response acknowledgement before enabling revocation. The hub checks the expected owner against the same routing target it relays to, returning `409 machine_owner_changed` before a stale confirmation reaches any receiver, including an older manager that ignores the query. Updated managers also check their immutable identity before settings access.
  - The web's guarded mutation is `DELETE /api/v1/machines/{mid}/computer/apps/revoke/{encoded identifier}` with the required expected owner. A capable hub checks then translates it to the established DELETE for older managers; updated managers also accept the guarded path directly. Legacy receivers cannot match that path as an ordinary DELETE, so a hub downgrade cannot turn an earlier acknowledgement into an unsafe mutation. Reads remain visible through an older hub with a clear update notice and disabled revoke controls. Legacy clients using the original GET/DELETE routes without a precondition keep their behavior.
- **Identifying Apps** (`callApps`, `computer_apps.go`):
  - `app` in `computer_state`; absent means whole screen, also asks once.
  - Each step's `app` in `computer_run` (or the call-level `app`).
  - The app a `launch_app` step opens: its `name` (or `bundle_id`), in both step forms (`{"launch_app": {…}}`, `{"tool": "launch_app", "args": {…}}`), never the call-level `app`.
  - Literal `cua.getApp("…")` and `cua.launch("…")` in scripts.
  - Names resolved to bundle ids through Cua `list_apps` before comparison, so identically named different Apps aren't conflated; unknown names stored by name.
  - Any step without an app, a `launch_app` step naming no app, computed app names in a script (`getApp` or `launch`), or a script naming no app makes identification incomplete: ask every time for the whole call, without Always Allow.
    A `getApp` / `launch` occurrence the literal reader cannot parse counts the same way: space or a comment before the call's parentheses (`getApp ("Terminal")`), a computed callee (`cua["getApp"]`, a saved function), or an argument that is not a plain string. The completeness check compares every occurrence of the callee's name with the parsed literal calls, so an unreadable one cannot be missed.
- **Always ask, never offer Always Allow**: terminals (Terminal, iTerm2, Warp, Ghostty, etc.), System Settings, Keychain Access, password managers (Passwords, 1Password, Bitwarden, etc.), Persona, identified by bundle id or name (Chinese/English).
- **Never operated** (#4336, #7560, `computer_protected.go`): System Settings and the system's permission and authorization prompts (`com.apple.UserNotificationCenter`, `com.apple.SecurityAgent`, `com.apple.LocalAuthentication.UIAgent`, and older systems' Accessibility alert). macOS's privacy permissions are meant to be answered by a person; one Allow on a card that said "operate System Settings" would otherwise let an Agent turn any of them on. Reading them (`computer_state`) is still asked about every time. Acting is decided by one check, `protectedapps.Look.Check` (`server/internal/protectedapps/check.go`), before any card and again after one, whatever App approval or Confirm on Phone say. It refuses only what could reach one of them; having System Settings open does not stop other apps:
  - **Named or aimed at, always**: a `computer_run` that names one (a step's or the call's `app`, a `launch_app`, a literal `cua.getApp` / `cua.launch`), by bundle id or by name in English, Chinese or Japanese (names resolved through Cua `list_apps` first), or aims at one's pid, window id or window title.
  - **A prompt on the screen stops everything**: while a permission or password prompt has a window on the screen, every acting call is refused. Prompts are modal and float on top, and #4349 found the Screen Recording prompt takes synthetic clicks.
  - **While System Settings has a window** (each window of `list_windows` matched to its bundle id through `list_apps`, so it counts in any language), a call is refused only when it could reach it:
    - **element tokens**: a token carries its own pid, so it is traced to its window by reading each of System Settings' windows with `since: <the token's snapshot id>`. `other_window` means another window's token, which goes. Its own token, or one that can't be traced, is refused, minimized windows included. Only System Settings' windows are read, never the one the call is for, because a read replaces a window's current snapshot and would make the caller's tokens stale;
    - **points** (a click, drag, scroll or pointer move at `x, y`): refused where System Settings' window can be the topmost one, by `z_index`, or by its bounds when the order isn't known. The pixels map back by a screenshot scale the call doesn't carry (up to 2 on a Retina display at full size, below 1 for a capped image). So every place they can land is checked: from the window's origin plus half the point, out along the same line to the window's edge;
    - **keys**: keys aimed at an app (a pid, a window, an element token, a named app, or a step's inherited window) are judged by that app. Keys with no target are refused only while System Settings is the frontmost app (`list_apps` `active`);
    - **scripts** don't run while System Settings is on the screen, because what a script will click can't be checked beforehand. Steps can be used instead;
    - **anything that can't be placed** (a pixel point in no known window, a tool whose target isn't known) is refused. Browser page tools, the clipboard and the agent cursor go.
  - **Not checked**: windows that can't be read stop the call ("could not be read", nothing done). A script whose apps can't all be read is refused (they were asked about every time before): it could open one under a computed name. The model is told to name them literally.
  - Steps naming no app (they act on whatever has the focus) run one `run_steps` call at a time, each checked just before it runs, so a prompt that an earlier step brought up stops the rest. The answer lists each step's own answer (`one_by_one`).
  - LCU: a native-app question about one of them is declined without a card (`lcuconfirm.NativeApp`). App-less steps already fail there.
  Each refusal tells the model which case applies and what can still be done: only the protected app is off limits, for example "System Settings is on top where this would click; act by `element_token`, or ask the user to move it out of the way or close it". When an app is named or aimed at, the model is told the user does that themselves, on the Mac or through the live screen, and not to retry. The hub log says which rule refused it and the app's name, and nothing else of the call.
  - **The Agents' own Cua Driver** (#4349, `server/internal/node/cua_gate.go`): in the sessions Persona starts on a Mac, a PreToolUse hook on Cua Driver's MCP tools (`mcp__<server>__<tool>`, any server name with "cua" in it) and on Bash commands that run `cua-driver call` applies the same check on the computer, at once, with no card. Reads pass without a look. A script whose apps can't be read is denied, and so is a `cua-driver call` whose arguments aren't in the command, or one the gate can't follow (a heredoc, a subshell). `cua-driver` counts only where it is the program run (after variable assignments, `env` / `sudo` and the like, or inside `sh -c`), so a `grep` or a commit message that mentions it is no call.
  - **Not covered**:
    - what a script does with a window it finds once it is running;
    - a window that comes up partway through steps that each name their app: those run as one batch, checked before it starts (steps naming no app are checked one by one);
    - the sessions the user starts themselves (Persona's global hooks only report, `docs/integrations/claude-integration.md` "Global report-only hooks");
    - Codex Agents' own MCP tools;
    - any other program a shell can use to drive the screen.
    Cua Driver 0.38 can't close these itself: its hard invariants protect only its own consent UI, its policy file sees tool names and arguments only, and its capability manifest is an allowlist of apps. macOS's own protections are the backstop there.
- **With Confirm on Phone Before Acting**: if this call already asked because of an App, don't ask twice; otherwise with confirmation enabled, ask each time as before.
- **Agent's own Cua MCP** (`mcp__cua-driver__*`): bypasses the hub and still uses the Agent's tool approvals, except the never-operated Apps above, which its PreToolUse hook denies (#4349). Integrate allowed Apps later.

### d) Small settings

- **Click sounds** (implemented, `cua` extension `click_sound`): none / foreground clicks only / foreground and background clicks, default none.
  - Cua has two click delivery modes: foreground (`delivery_mode: foreground`) brings the window forward and moves the real mouse; background (default) delivers to the process without moving the cursor.
  - The hub includes the setting in `computer.call`; the node counts click steps in `run_steps` (`click`, `double_click`, `right_click`, accepting both `{"click": {…}}` and `{tool, args}`). If sound is required, plays a system sound once (`afplay /System/Library/Sounds/Tink.aiff`, without waiting), once per call: Cua runs the steps as a batch, so the node doesn't know each click's timing.
  - Not covered: clicks in `run_script` (inside its process), and the computer Agent's own Cua MCP (bypasses node).
- **Show Agent Cursor** (implemented, `cua` extension `show_cursor`): on by default, so VNC viewers can see where it clicks.
  - Cursor overlay belongs to Cua's background process and is determined at startup; `cua-driver config` can't change it (`agent_cursor.enabled` read-only). Disabling stops it and restarts with `open -g -b com.trycua.driver --args serve --no-overlay` (`computer.cursor` in `node/cua_extras.go`), also used when the node starts it later. In-progress operations are briefly interrupted; the setting says so.
  - Hub sends on settings change and node connection (`extmgr/computer_cursor.go`): configured value only on machines enabled for the extension, "on" for other Macs.
  - "On" only undoes what this node disabled; Cua started by the user with `--no-overlay` isn't touched. No restart if the process already matches or isn't running.
  - Computer Agents' own Cua MCP uses the same background process, so their cursor is disabled too.
- **Separate Office / Messages permissions**: not implemented. We have no dedicated integration for these; reading Messages belongs to the future iMessage connector ([plan-connectors.md](../plans/plan-connectors.md)).

### Order

1. **b**: keep awake during Agent control, including VNC. Unlock entry when locked, opening live view. Small, first.
2. **c**: per-App approval, assistant first, then Agent Cua MCP.
3. **a-2**: one-tap unlock, together with #1322.
4. **b2**: prevent automatic locking while the extension is enabled (implemented).
5. **d**: click sounds and cursor switch (implemented).

Test each step on this Mac: switches, locking and restoration. Lock testing requires the user present: someone must enter the password afterward.

### Decisions (2026-11-04, user accepted all recommendations)

1. **Locking**: only prevent locking and provide an unlock entry; no system authorization plug-in.
2. **Prevent automatic locking while enabled**: off by default, confirm on the phone when enabling; withdraw automatically after 30 minutes without node–hub connection.
3. **Per-App approval**: on by default. Ask on first operation per App, remember afterward, removable in Settings.
4. **Default deny list**: terminals, System Settings, Keychain Access, password managers, Persona, nothing else.
5. **One-tap unlock**: implement. Mac password in phone keychain, typed into the lock screen via VNC. Off by default, per Mac, together with #1322 Remember on This iPhone.
6. **ego**: when `ego` is enabled for that Mac, also allow its Screen Sharing (VNC); the page recognizes ego too.

Follow the order above, one PR per area. Item 6 can go with b, or a separate small PR merged first.

## Verification

- **Unit tests**:
  - `server/internal/node/computer_test.go`: parsing, readiness, `computer.call` through a fake cua-driver script (arguments, session name, screenshots, rejected tools).
  - `server/internal/node/computer_install_test.go`: fake download source and installer, verifies all three scripts run from disk, arguments/environment, telemetry off, permission grant afterward, rejection of non-installer scripts, cancellation.
    `PERSONA_CUA_INSTALLER_CHECK=1` downloads and checks only against real cua.ai (passed 2026-11-04).
  - `server/internal/extmgr/computer_test.go`: extension off by default, per-machine enabling, tools appear/disappear, calls through a fake node, denied confirmation.
  - `apple/PersonaTests/ComputerTests.swift`: machine `computer` field, Chinese tool names, readable steps, latest operation considered running before result arrives.
- **Real device (node side)**: run node package `TestCuaEndToEnd` on a Mac with Cua Driver installed.
  - Enable with `PERSONA_CUA_E2E=1`, optionally `PERSONA_CUA_SHOT=<image path>`; compile the test binary with `GOOS=darwin go test -c` and copy it over.
  - Uses `run_steps` to calculate 6×7 in Calculator, then reads the window, asserts 42, and writes a screenshot.
  - Verified 2026-11-04: ready, 42, 230×408 JPEG. It operates Calculator on that Mac.
- **Web**: `web/src/screen/aes.test.ts` (AES substitute vs FIPS-197 and WebCrypto), `screen.test.ts` (path, token in fragment, WebSocket URL, prefilled account).
- **VNC end-to-end** (App native client → hub 5900 forwarding → mock Screen Sharing, `MOCK_SCREEN=fake`, [testing.md](../operations/testing.md)):
  - `ComputerSectionTests.testTheLiveScreenAsksForTheMacsSignIn` taps VNC through to login required (ARD).
  - `ScreenDrawerTests` (mock with `MOCK_COMPUTER_OPERATING=1 MOCK_SCREEN=fake`; without `MOCK_SCREEN=fake`, it uses this Mac's real Screen Sharing, showing "hub doesn't allow this machine's screen" and failing if off; assistant and Agent chats each have a running operation): open the full page from the operating bar and ··· menu in assistant and Agent chats, close with ×.
  - `ScreenPasswordTests`: remember, automatic login next time (PiP on real device, full page in simulator), reenter changed password, forget; one-tap unlock.
  - `VNCTests.testNotAskedYetSaysAWindowWillShow` (#3954, a second mock with `MOCK_SCREEN_VIDEO=missing` at
    `TEST_RUNNER_PERSONA_SCREEN_VIDEO_MISSING`): before macOS asked, "How to turn it on" says a window will show.
  - `VNCTests.testVideoTakesThePictureAndFallsBack` (#3954, mock `MOCK_SCREEN_VIDEO=1`, runner
    `TEST_RUNNER_PERSONA_SCREEN_VIDEO=1`): video takes the picture after the sign-in, with Wi-Fi's cap in `start` and
    reports coming back; the Mac locking and the stream dropping each put VNC back with their line, unlocking brings
    video back. `testTheOverlayBreaksTheLatencyDown`: Connection Stats over the video shows every segment.
    `ScreenVideoTests` (unit) decode the mock's fixture and drive the stream, the link and the overlay's reading from
    a script; `TimingWindowTests` (`swift test`) the helper's medians.
  - `VNCTests`: PiP (real device: menu → PiP → remains on home screen → restore full page → PiP again; simulator has no PiP, verifies full-page fallback only). PiP source (`-showPiPSource`, also runs in simulator): appears within 1.5 seconds of tapping, shows Connecting (mock delays 3 seconds), then live view, and Failed to Connect after mock disconnects. On full page, tap/drag in both modes, Center Pointer (mock records clicks and pointer positions), clipboard both ways, typing and Mac shortcut row, Help. `TEST_RUNNER_PERSONA_VNC_PAUSE=2` slows it for recording; `testMeasure` (`MOCK_SCREEN_ANIMATE=1`, `TEST_RUNNER_PERSONA_VNC_MEASURE=1`) prints frame rate and latency.
  - Logging into a real Mac needs that Mac's password, so tests don't do it.
- **Screenshots**: mock `MOCK_COMPUTER=ready|permissions|locked|missing|off` ([testing.md](../operations/testing.md)), with `-tab settings -openMachines YES -openMachineDetail macbook`.
- **Post-deployment acceptance**:
  1. Enable Computer Control (Cua) in **Settings › Extensions**.
  2. Enable the switch on that Mac's machine page.
  3. On the phone, tell the assistant "Use Calculator on <machine> to calculate 6×7 and send me the screenshot".

  The chat should have Control Computer and View Computer tool rows, expanding to a screenshot showing 42.
