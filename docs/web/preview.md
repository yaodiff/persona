# Dev server previews (#150)

The page an Agent's dev server serves on one of your computers (vite, next dev, `python -m http.server`…), shown live in a panel beside the session's chat on the web: use it (click, scroll, type, back / forward / reload / address, desktop or phone size), take a screenshot into the message box, or pick an element and comment on it for the Agent. Servers come from the project's `.claude/launch.json` (the file Claude Desktop reads) and from the ports the node already finds for the session (`Session.ports`, [ports.md](../operations/ports.md)). The interaction follows Claude Desktop's Browser pane; none of its code is used.

## Why a browser on the computer

The App shows an Agent's port by listening on the phone's loopback and forwarding each connection ([ports.md](../operations/ports.md)). A web page cannot listen, and serving the dev page from the hub's own origin would let its scripts read the web's hub token from `localStorage` (an earlier attempt at opening those links on the web stopped there). A separate origin needs a new public port or wildcard DNS, which is the user's call.

So (decided with the user, 2026-11-07) **the node runs a headless browser on that computer and streams the page**. Nothing about the network changes, the dev page never runs in the web app's origin, and screenshots and picked elements are the real page's. The cost: it is remote-desktop-like (a picture per change, input sent back; about a round trip of latency), and that computer needs Chrome, Chromium, Edge or Brave.

```
web panel  ──WebSocket──▶  hub …/preview/connect  ──stream (streams.Mux)──▶  node  ──CDP over a pipe──▶  headless browser tab
  <canvas> ◀─ JPEG pictures ──────────────────────────────────────────────────────── Page.startScreencast    http://localhost:<port>
  input    ── JSON ──────────────────────────────────────────────────────────────────▶ Input.dispatch*
```

## The browser (`server/internal/node/preview_browser.go`)

- **Which**: `PERSONA_CHROME` (in the node's environment) when set; else Google Chrome, Chromium, Microsoft Edge, Brave, Chrome for Testing or Canary in `/Applications` / `~/Applications` (macOS) or on `PATH` (Linux), then Chromium builds Playwright downloaded (`~/Library/Caches/ms-playwright`, `~/.cache/ms-playwright`). `preview.servers` says which one (`browser`), or nothing when there is none.
- **How it runs**: `--headless=new --remote-debugging-pipe`: the DevTools protocol goes over the browser's fds 3 and 4 (NUL-separated JSON), so **there is no debugging port** another program or user on the computer could connect to. Its own profile under the user cache folder (`persona/preview-browser`): a sign-in to the dev app is still there next time; the user's own browser profile is never touched. The profile holds those sign-ins, so it is a `privateCacheDir` folder (`internal/node/cachedir.go`): made 0700, and a symlink there or a folder of another user or open to others stops the browser from starting, with the reason. Without a user cache folder (a Linux node without `HOME`) it is a folder of the node process's own made by `os.MkdirTemp`, never a fixed name in the shared `/tmp`; sign-ins then last until the node restarts. No first-run, sync, extensions, component updates or background networking; `--use-mock-keychain` and `--password-store=basic` so macOS asks for nothing; `--no-sandbox` only when the node runs as root on Linux (a container). Downloads are refused.
- **Lifetime**: one browser per node, started by the first preview (a second or two) and ended 10 minutes after the last preview closed; a tab per preview, at most 6 at once. It is in a process group of its own, so its renderers go with it, and it exits by itself when the node does (its pipe closes).
- **Not done yet**: Windows (CDP over a pipe needs inherited fds 3 and 4, which Windows has no equivalent of in Go's `ExtraFiles`); `preview.open` there fails with the reason.

## Servers (`preview_launch.go`)

`preview.servers {cwd}` reads the nearest `.claude/launch.json` from the session's folder up to the repository's root (a folder with `.git`) or the home folder. The file is JSON with comments and trailing commas allowed (VS Code writes it that way), at most 256 KB, in Claude Desktop's shape:

```json
{
  "version": "0.0.1",
  "configurations": [
    { "name": "web", "runtimeExecutable": "npm", "runtimeArgs": ["run", "dev"], "port": 5173 },
    { "name": "api", "runtimeExecutable": "go", "runtimeArgs": ["run", "./cmd/api"], "port": 8080, "cwd": "server", "env": { "PORT": "8080" } },
    { "name": "docs", "url": "http://localhost:4000/guide" }
  ]
}
```

- `port`, or the port of a local `url`; `url` (http / https on `localhost`, `*.localhost`, `127.x`, `[::1]`) opens that page instead of `/`. An entry with a `url` and no command attaches to a server you start yourself.
- `cwd` is relative to the project (the folder holding `.claude`; `${workspaceFolder}` works), `env` is added in front of the command.
- Each entry comes back with `listening` and the page's `title` (the same probe as port discovery). Entries that cannot be used come last with a `problem`: `no_name`, `no_port`, `not_local` (a `url` on another computer: a preview only shows this one), `no_run` (no command and no `url`), `bad_env`. A file that is not JSON or too big comes back as `error`, with no entries.

**Start** (`preview.start {cwd, name, workspace, kind?}`): offered for a session with a workspace and a terminal (a Paseo session has neither of its own: its workspace is Paseo's). The panel shows the exact command (and folder, variables) and asks first. The node reads the file again (what runs is what the file says now), opens a new terminal tab in the session's workspace named after the entry, in its folder, and types the command: each word shell-quoted, `env K=V …` in front for its `env`, never a shell string from the file. The output is in that tab (the terminal page, herdr on the computer), and Ctrl-C there stops it. The panel then waits for the port: it reads `…/preview/servers` every 1.5 s for 90 s, one read at a time and none while the browser tab is hidden, and after Cancel a read still out changes nothing.

## A preview (`preview.go`)

`GET /api/v1/machines/{mid}/preview/connect?port=&path=&w=&h=&dpr=&mobile=1` (a WebSocket, full devices only) → the hub checks the port the way port forwarding does (an Agent's, or opened with `POST …/ports/{port}/open`: the panel opens the server the user picked), opens a stream and asks the node `preview.open {id, port, path, w, h, dpr, mobile}`. Refusals the hub can tell by itself (a device that cannot, a port not allowed, an old node, offline) are HTTP errors before the upgrade; the panel checks the same things over REST first (`…/preview/servers`, `…/ports/{port}/open`), since a browser cannot read why a WebSocket was refused. Then the hub upgrades and asks the node, which checks that something listens there, takes the browser (starting it), opens a tab and sets it up. When it cannot, the web gets `{t: "error", code, error}` (`port_gone`, `no_browser`, `browser_failed`, `offline`, `failed`) and the close. On the stream each WebSocket message is one frame (`server/internal/preview`: 4-byte big-endian length, `t` text or `b` binary, the bytes); the hub re-frames without reading.

### Messages

From the node:

| Message | Meaning |
| --- | --- |
| binary `f` | A picture: `seq` (uint32), CSS width and height (uint16 each), then image bytes — JPEG (`FF D8 FF`) for screencast frames (up to 2x pixel size at `dpr` 2) or PNG (`89 50 4E 47`) for still frames once the web sends `features {png: true}` (else a higher-quality JPEG). Once the page is still for ~150 ms with no new screencast frame, the node sends one sharper still frame and drops it if a newer screencast arrives first. At most 2 frames are in flight: the browser makes the next screencast frame when the web acknowledges one, so a slow link gets fewer pictures, not a backlog; nothing is sent while the page does not change |
| binary `s` | A screenshot: `id` (uint32), then a PNG |
| `ready` | `{browser, w, h}` |
| `error` | `{code, error}` from the hub: the preview could not open (above); the close follows |
| `page` | `{url, title, loading, back, forward}`, whenever one of them changes |
| `external` | `{url}`: the page (or the address typed) went somewhere that is not this computer; it was not followed, the web offers to open it in a new tab |
| `failed` | `{url, error}`: the address could not be loaded (`net::ERR_CONNECTION_REFUSED`: the server stopped) |
| `dialog` / `dialog_closed` | `{kind: alert / confirm / prompt / beforeunload, message, default}`: the page waits for the answer |
| `select_open` | `{id, rect: {x, y, w, h}, selected, options: [{index, label, disabled?, group?}]}`: a single-choice `<select>` (not `multiple`, `size` <= 1, not disabled) opened by a primary click/tap or popup-opening key after `features {select: true}`. The node prevents the invisible native popup and focuses the select. `id` increases per tab (uint32 > 0; a new open replaces an open one); `rect` is viewport-relative CSS pixels (`getBoundingClientRect`); `selected` is `selectedIndex` (-1 when none); `options` lists up to 1000 visible options in document order with collapsed labels/groups capped at 200 characters |
| `select_close` | `{id}`: the node withdrew open `<select>` list `id` (the select lost focus, was removed or disabled, the page navigated, or the viewport size changed) |
| `pick` / `picked` / `pick_failed` | Picking ended; the element picked (below) |
| `copied` | `{text}`: what ⌘C / Ctrl+C copied in the page, for the web to put on the clipboard |
| `notice` | `{code: file_chooser}`: the page asked for a file, which a preview cannot give yet |
| `crashed` | The page's renderer died (reload brings it back) |
| `closed` | `{code: tab_closed / browser_exited}`: the preview is over |

From the web:

| Message | Meaning |
| --- | --- |
| `ack` | `{seq}`: a picture (screencast or still frame) was drawn |
| `features` | `{select: true, png: true}`: sent after every `ready`; enables the native `<select>` bridge (for new documents and the current one, idempotent) and PNG still frames |
| `select_choose` | `{id, index}`: choose option `index` (into `select.options`) of open list `id`; ignores a stale `id`, missing or disabled option, else sets `selectedIndex`, dispatches bubbling `input` then `change` only when the value changed, keeps focus on the select, and closes the list |
| `select_close` | `{id}`: dismiss open list `id` without changing the page (focus stays on the select) |
| `mouse` | `{type: down / up / move, x, y, button, buttons, clicks, mods}` in CSS pixels of the page; `mods` is CDP's (Alt 1, Ctrl 2, Meta 4, Shift 8) |
| `wheel` | `{x, y, dx, dy, mods}` |
| `key` | `{type: down / up, key, code, key_code, text?, mods, repeat, location}`; `text` makes it a typed character |
| `text` | `{text}`: typed as is (a paste, an input method's result) |
| `cmd` | `{name: selectAll / undo / redo / delete / copy / cut}`: the page's editing commands (a headless browser has no menu for ⌘A, ⌘Z…) |
| `nav` | `{url}`: a path on the page's server, `host:port/…` or a whole URL |
| `back` / `forward` / `reload` | |
| `size` | `{w, h, dpr, mobile}`: the panel's size (CSS pixels, clamped to 200–3840 × 200–2400, `dpr` 1–2). `mobile` emulates a phone: touch, a phone's user agent (the page reloads) |
| `shot` | `{id, full?}`: a screenshot of what is in view, or of the whole page up to 12,000 pixels high |
| `pick` | `{on}`: element picking on or off |
| `dialog` | `{accept, text?}`: the answer to the page's dialog |

### Staying on this computer

The tab's top frame may only go to this computer's loopback addresses over http / https (`localhost`, `*.localhost`, `127.0.0.0/8`, `[::1]`) or `about:blank`. Each top-frame document request is checked (`Fetch.requestPaused`); one elsewhere is answered with 204 (so the page stays where it was, no error page) and the web gets `external`. A window the page opens is closed, and its address shown in the tab when it is local, else sent as `external`. So the preview is not a browser into the computer's network: the address bar cannot reach the router, the intranet or the cloud's metadata address. The page's own requests (scripts, images, `fetch`) go as they would in a browser on that computer; that is the page the Agent wrote, run where it already runs.

Within the preview the address bar and links may go to any loopback port, as the user could open one with `POST …/ports/{port}/open`.

### Picking an element

`pick {on: true}` puts the tab in the inspector's "select an element" mode (`Overlay.setInspectMode`): the element under the pointer is outlined in the pictures (content, padding, margin), and a click picks it instead of clicking it. The node runs `preview_pick.js` on it and sends `picked`: a CSS `selector` (ids first, readable class names, `:nth-of-type` where needed, unique when it can be), `tag`, its `text` (300 characters), `html` (`outerHTML`, 6000 characters), `role`, `label` (aria-label), its box (`rect`), the page's `url` and `title`, and when a dev build says so the `component` that drew it and its `source` (React 18's `_debugSource`, Vue 3's `__file`, Svelte's `__svelte_meta`). The web turns it into a comment for the Agent: an `Element comment N` block in front of the next message (`docs/protocol/protocol.md` "Quote and comment blocks"), the HTML cut to 60 lines.

### Typing and a phone's keyboard

The web panel types into the page through a hidden text field (`web/src/views/session/PreviewPanel.tsx`): keys, an input method's text and a paste go from there as `key` / `text`. On a phone, focusing that field raises the keyboard, which shrinks the panel and so the page (a smaller `size`, and the page lays itself out again). So on a touch screen only a tap that lands on the page focuses it, once the finger lifts (until then the press may become a scroll); a finger's scroll, Pick element (cancelled there with the bar's Cancel rather than Esc) and Go in the address bar (which puts the keyboard away) leave it alone. A mouse or trackpad focuses it on the press, and after Pick and Go.

- A tap on a part of the page that takes no typing still raises the keyboard: telling the two apart needs the node to say when an editable element of the page has focus.
- A touch's `pointerup` is a user activation in the HTML standard, which is what lets iOS raise the keyboard for a focus made in it; assumed for iOS WebKit, not yet checked on a device.
- `scripts/test/web_preview_e2e.py`'s phone step checks it at 390 with CDP touches and a stand-in for the iOS keyboard (up while a text field has focus): no focus and no smaller `size` after a scroll, Pick or Go; a tap focuses the field only after the finger lifts. `web/src/views/session/PreviewPanel.touch.test.tsx` covers the same cases and the mouse.

### Native `<select>` lists and high-DPI pictures (web)

Headless Chromium draws native `<select>` popups in an OS widget outside the screencast surface. Every time the web panel (`web/src/views/session/PreviewPanel.tsx`) receives `ready`, it announces `features {select: true, png: true}` so compatible nodes bridge native single-choice `<select>` popups and send lossless PNG still frames.

- **Option overlay (`web/src/ui/SelectListbox.tsx`)**: when `select_open` arrives, the panel renders a `role="listbox"` overlay above `<canvas>` with `role="option"` rows, `aria-selected`, `aria-disabled`, `aria-activedescendant`, and `optgroup` headings. Labels and group headings are capped in `web/src/lib/preview.ts` (`normalizeSelectOpen`) and always rendered as plain text (never HTML).
- **Positioning and scrolling (`placeSelectPopup`)**: the page-space `rect` is mapped onto the canvas display box by the current `fit` scale. The popup opens below the `<select>` by default and flips above when space below is insufficient and more room exists above; its width is at least the select's rendered width (clamped to the container), its height is capped (`280 px`) with scroll overflow fades, and opening scrolls the currently selected option into view.
- **Pointer, touch and keyboard behavior**: mouse clicks and touch taps choose an enabled option and send `select_choose {id, index}` (always including `index`, even `0`); disabled options cannot be chosen. While the popup is open, keys are handled by the listbox instead of being forwarded to the page: `ArrowUp` / `ArrowDown` move across enabled options (skipping disabled ones), `Home` / `End` jump to the first / last enabled option, `Enter` and `Space` choose the active option, and `Esc` dismisses the list and sends `select_close {id}` without closing the side panel. Clicking or tapping outside the popup (swallowed inside the preview container so the click does not hit the page underneath), wheel scrolling outside the popup, resizing the panel, switching Desktop / Phone, or navigating via the address bar all close the popup and send `select_close {id}`. A matching node `select_close {id}`, a replacement `select_open`, or a socket disconnect closes the popup locally without echoing `select_close`. Closing returns focus to the hidden keyboard input when focus stayed inside the popup.
- **High-DPI frames and PNG still frames**: binary `f` frames are identified by magic bytes (`frameImageMime`: PNG `89 50 4E 47` vs JPEG `FF D8 FF`) rather than assuming JPEG. The `<canvas>` backing store sizes to the decoded image's own pixel dimensions while its CSS display size follows `canvasFit` (CSS size × `fit`, keeping `MAX_DPR = 2` and aligning desktop display dimensions to physical pixels without subpixel centering). Every `f` frame is acknowledged with `ack {seq}`, even if decoding fails.
- **Tests**: `web/src/views/session/PreviewPanel.select.test.tsx`, `web/src/lib/preview.test.ts`, `scripts/mock/mock_preview.py`, and `scripts/test/web_preview_e2e.py`.

### Implementation notes

- The tab's goroutine handles CDP events and the web's messages in order and never waits for a CDP answer that may depend on an event it has not handled yet (a navigation waits on `Fetch.requestPaused`): input and acknowledgements are sent without waiting, calls whose answer matters run on goroutines of their own and post the answer back.
- Browsers do not report a page's title as it changes (headless Edge 156 says only `localhost:<port>`), so a script added to each document reports `document.title` through a `Runtime.addBinding` function as it changes.
- `Page.getNavigationHistory` fails while the page is between documents; back / forward are asked again when loading stops.
- Headless Chromium's `Page.startScreencast` ignores `Emulation.setDeviceMetricsOverride`'s `deviceScaleFactor` on a 1x virtual display and streams 1x frames unless the browser starts with `--force-device-scale-factor=2`; with that flag, `dpr=2` streams at `2w × 2h` while `dpr=1` stays `w × h` via `maxWidth` / `maxHeight`.
- Native `<select>` popups are rendered outside `Page.startScreencast`. After `features {select: true}`, the node registers a `Runtime.addBinding` handler and injects `preview_select.go`'s bridge into an isolated world (so page scripts cannot see the binding) for both new documents and the current document.
- `TestPreviewDrivesARealBrowser`, `TestPreviewDPRAndStillFrames` and `TestPreviewSelectBridge` (`preview_live_test.go`) drive the browser this computer has: picture, 2x/1x DPR and PNG still frames, click, typing (with `Input.insertText` for an input method's text), native `<select>` bridge (mouse, keyboard and phone touch), a link elsewhere and an address elsewhere refused, a screenshot, a picked element, a local link and back, phone size. They are skipped where there is no browser, and in `-short`.
- The web panel is described in [web.md](web.md) "Side panels". The iPhone's preview is below ("iPhone").

## iPhone

The iPhone supports two preview modes inside the same link card (`LinkSheet` + `PreviewPage`):

1. **Direct loopback forwarding (default)**: opens the computer's port through `PortForwarder` ([ports.md](../operations/ports.md) "Forwarding") and runs the page natively in `WKWebView`. No browser on the computer is required.
2. **View through the computer (headless browser stream)**: reuses the hub/node WebSocket stream (`GET /api/v1/machines/{mid}/preview/connect?port=&path=&w=&h=&dpr=&mobile=1`) implemented in `PreviewStreamView.swift` and `PreviewStreamModel.swift` (`@MainActor @Observable` with `Deps` injection). It draws binary `f` frames decoded off the main actor via ImageIO (detecting PNG `89 50 4E 47` and JPEG `FF D8 FF` by magic bytes) and sends `ack {seq}` for every frame, announces `features {select: true, png: true}` after every `ready`, sends `size` with `mobile: true` and `dpr: min(displayScale, 2)`, translates taps into `mouse` move/down/up and drags into `wheel`, bridges software/IME typing through a hidden `UITextField` (`key`/`text`) toggled from the bottom bar, updates title/URL/back/forward from `page`, routes `external` through the card's normal in-app link handling, presents native SwiftUI alerts for `dialog` (`alert`/`confirm`/`prompt`/`beforeunload`) and replies with `dialog`, and opens `PreviewSelectSheet` for `select_open` (grouped options, disabled rows, selected checkmark), sending `select_choose {id, index}` on selection or `select_close {id}` on dismiss and closing locally without echo on a node `select_close`. Screenshots (`shot`) and element picking (`pick` / `picked` / `pick_failed`) reuse the same bottom bar controls and composer comment flow as direct mode.

The iPad keeps the same card for now (a column beside the chat is a follow-up); the Mac app is frozen and has none of it.

- **Switching modes and automatic fallback**:
  - The card header's `⋯` menu switches between **View through the computer** and **View directly** while keeping the current page path. When the computer has no supported browser (`no_browser` / empty `browser` from `…/preview/servers`) or runs an older node (`node_outdated`), stream mode is not offered and the menu explains why.
  - When direct mode hits a failure that the computer's browser can resolve (gateway/connection/TLS/timeout/listener errors), the direct error overlay shows **View through the computer** beside **Retry**. Failures that streaming cannot solve either (`port_gone`, computer offline, authentication/permission failures, hub unreachable, no internet) omit the stream button.
  - Declining a loopback self-signed certificate, an untrusted TLS error, a direct load timeout, a local listener failure, or two consecutive recoverable direct failures automatically switch the card to **View through the computer** (when the hub advertises `preview` and stream mode is available) and show a dismissible `NoticeBanner`: *"Couldn't show the page directly, so it's shown through the computer."* The chosen mode stays pinned for the open card so it does not bounce back and forth between modes (`PreviewCardModeState`).
- **Servers**: an Agent chat's ⋯ › Preview (when its hub lists the `preview` feature) opens `PreviewChooser`
  (`apple/Persona/Views/Link/PreviewChooser.swift`): `…/preview/servers` for the session's folder, the usable launch.json
  entries first, then the session's own ports, then the entries that cannot be used and why; with nothing, the launch.json
  example to copy. A running one opens in the link card (`POST …/ports/{port}/open`, then the forwarding); Another Port…
  opens a port typed in. The card's subtitle (`MacBook :5173 · web ⌄`) opens the same list to switch.
- **Start…**, offered as on the web only for a session with a workspace and a terminal (`PreviewLogic.canStart`: not a
  Paseo session's), shows the command as the node runs it (`env K=V …`, each word quoted) and the folder, and asks first; then
  `POST …/preview/start` with the session's workspace and its host kind, and a look at the servers every 1.5 s for 90 s
  until the port answers (an Agent's port counts too). The row says what it waits for and can be cancelled; the page opens
  once the port answers. `PreviewModel` holds this; `PreviewLogic` (`Preview.swift`) is the web's `lib/preview.ts`.
- **The bar** under any forwarded page opened from a chat (`PreviewPage` in `PreviewBar.swift`): back, forward, where the
  page is on the computer (`:5173/settings`), a keyboard toggle when viewing through the computer, a screenshot (Visible Area
  through `takeSnapshot` or `shot {full: false}`, Whole Page from the page's PDF or `shot {full: true}`, at most 12,000
  pixels high) into the message box, and Pick an Element.
- **Picking** in direct mode runs the node's own `preview_pick.js` in the page: the app bundles a copy
  (`apple/Persona/Resources/Render/preview_pick.js`, kept equal by `scripts/test/test_preview_pick_copy.py`), injected with a
  wrapper in the page's own world (an isolated world could not see what React, Vue or Svelte leave on the element). In stream
  mode it sends `pick {on: true}` and consumes the node's `picked` / `pick_failed` messages. Either way, a tap that does not
  scroll picks the element under it without pressing it; the element stays outlined with its selector and size, the bar gives
  way to a line to write about it, and Add to Message puts an `Element comment N` card over the composer, the same block the
  web writes. Its `Page:` is the computer's address (`http://localhost:5173/…`, `PortForwarder.key(for:)`), not the phone's
  forwarded one. In direct mode the pick comes back through the script handler `personaPreviewPick`, added while picking and
  removed after: the pool's web views share one user content controller, so a handler left there would answer for every page.
- **The accessibility text sizes** (#5114, #5172): a row's Running, Start… or Cancel goes on a line under its name and port; the bar
  puts the address on a line of its own above its buttons; picking's banner takes up to four lines; the comment panel
  stacks Add to Message over Cancel and has the room under the header to itself (the page folds away while it is open),
  scrolling when it is taller than the room above the keyboard; the card's header, a bar, grows with the text up to the
  largest standard size (xxxLarge), as the viewers' bars do, so its title and the server chooser stay readable beside its
  buttons. The standard sizes are unchanged.
- **Tests**: `PreviewLogicTests` / `PreviewModelTests` (the web's cases, and Start against `FakeHub`); `PreviewStreamModelTests`
  (frame parsing, PNG/JPEG detection, per-frame `ack`, `features` on `ready`, coordinate conversion, `select_open` /
  `select_choose` / `select_close`, and automatic fallback policy against a fake socket); `PreviewUITests` and
  `PreviewStreamUITests` against `MOCK_PREVIEW=1`, whose running ports forward to the mock's own page server with the made-up
  app (`mock_preview.forward`) and stream frames / `<select>` popups over `/preview/connect`.

## Isolated preview origin

When `preview.domain` is configured in `hub.json` (off by default; [config.md](../operations/config.md), [network.md](../operations/network.md) "Wildcard preview origins"), the hub can also proxy a machine's dev server directly over HTTP and WebSocket (Vite / Next HMR) on a dedicated wildcard subdomain so the web app can embed the live page in an `<iframe>` with native rendering, native `<select>` controls, and zero screencast latency, while keeping the hub's `localStorage` token and APIs isolated in a separate origin.

```
web panel (hub origin)
  ├─ 1. POST /api/v1/machines/{mid}/preview/grant {port, path?}  (Bearer full device token)
  │     ◀─ {url: "https://<label>.<preview.domain>/__persona_preview_auth?code=…", origin, expires_at}
  └─ 2. <iframe src={url}>
        │  GET /__persona_preview_auth?code=…  ──▶ hub consumes code, sets __Host-persona_preview, 302 to path
        ▼
      preview origin (https://<label>.<preview.domain>[:port])
        └─ HTTP + WS (HMR) ──▶ hub preview proxy ──▶ port.dial + streams.Mux ──▶ node ──▶ localhost:<port>
```

### Capability advertisement

- The hub adds `preview_origin` to WebSocket `hello.features` and trusted `GET /api/v1/health` only when `preview.domain` is validly configured (extension reports cannot add it).
- When `preview_origin` is absent, web clients stay on the headless browser stream (`GET /api/v1/machines/{mid}/preview/connect`).

### Grant, redemption, and session lifecycle (`server/internal/hub/preview_origin.go`)

1. **Grant (`POST /api/v1/machines/{mid}/preview/grant`)**:
   - Request JSON: `{"port": 5173, "path": "/optional/path?q=1#hash"}` (`path` defaults to `"/"`; must start with a single `/` and cannot target `/__persona_preview_auth` or `/api`).
   - Requires a full device credential (read-only, answer, scoped, extension, and pairing credentials get `403`). Enforces the same `portAllowed` rule as `GET /api/v1/machines/{mid}/ports/{port}/connect` (a port in some session's `ports`, or opened via `POST …/ports/{port}/open` within 24 hours; otherwise `403 port_not_allowed`). Returns `404 preview_origin_disabled` when `preview.domain` is unset, `400` on invalid port or path, and `503` when the machine is offline.
   - Response JSON (`200 OK`): `{"url", "origin", "expires_at"}`.
   - Each `(machine_id, port)` maps to a deterministic, opaque DNS label `p-<24 lowercase base32 chars>` computed with HMAC-SHA256 from a key derived via HKDF-SHA256 from the hub's persistent `secret.key`. Different ports and machines get distinct labels, isolating browser storage (`localStorage`, cookies, IndexedDB) between ports while keeping the label stable across restarts.
   - The redemption `code` is 256 bits of random entropy held in hub memory for 30 seconds and bound to `(device, machine, port, label, path, verified hub origin)`.
2. **Redemption (`GET https://<label>.<preview.domain>[:port]/__persona_preview_auth?code=…`)**:
   - Consumes the code atomically on first use (replay, expiry, or wrong label returns `401` with `auth_failed`).
   - Seals a self-contained AES-256-GCM session token (12-hour TTL, authenticated with AAD bound to `label`) using a separate HKDF key derived from `secret.key`, so active sessions survive hub restarts without keeping session state in memory.
   - Sets host-only cookie `__Host-persona_preview` with `HttpOnly; Secure; SameSite=None; Partitioned; Path=/` (no `Domain` attribute).
   - Responds `302 Found` to `Location: <path>` with `Referrer-Policy: no-referrer`, `Cache-Control: no-store`, and `Content-Security-Policy: frame-ancestors <hub web origins>`. The redemption code never remains in `location.href` or `document.referrer`.
3. **Revocation**:
   - Every proxied request verifies the owning credential is still active (fast in-memory revocation fence + short cache evicted on revocation + database lookup on cold cache / after restart).
   - Active proxied HTTP requests and HMR WebSockets are tracked under the owning device credential and aborted immediately when that device token is revoked.
   - Grants minted with the master token (loopback / LAN only) are rejected if redeemed or used from a public source.

### Proxying and security boundaries

- **Host isolation**: Any request whose `Host` matches `<preview.domain>` or `*.<preview.domain>` is dispatched exclusively to the preview handler and never reaches hub API, admin, sharing, or web routes. Bare `<preview.domain>` and multi-level subdomains return `404`. On preview hosts, `/api` and `/api/*` always return `404` regardless of authentication or upstream dev-server routes.
- **Hub credential boundary**: Hub APIs accept only Bearer tokens (never ambient cookies) and never emit `Access-Control-Allow-Origin` for preview origins. Admin routes keep `SameSite=Strict`, `Sec-Fetch-Site: same-origin`, `Origin` verification, and `X-Persona-CSRF`. Because `__Host-persona_preview` has no `Domain`, is `HttpOnly`, and is `Partitioned`, sibling subdomains cannot toss cookies and scripts in the preview page cannot read it.
- **Upstream sanitization & transparent relay**: Reuses `port.dial` + `streams.Mux` with the same `portAllowed` check and `64` concurrent streams per machine (`maxPortStreams`). Paths, query strings, `Location` redirects, dev-server cookies, and response bodies pass through unrewritten. Before forwarding to the dev server, the hub strips `Authorization`, `Proxy-Authorization`, `X-Persona-*` / `X-Jarvis-*`, `__Host-persona_preview`, and Persona admin cookies, rewrites `Host` to `localhost:<port>`, verifies incoming WebSocket `Origin` matches the preview origin (rejecting cross-origin WebSockets with `403`), and rewrites the upstream WebSocket `Origin` to `http://localhost:<port>`. Upstream `Set-Cookie` headers attempting to overwrite `__Host-persona_preview` are dropped.
- **Framing policy**: Upstream `X-Frame-Options` and upstream CSP `frame-ancestors` directives are stripped (other upstream CSP directives stay intact), and the hub adds `Content-Security-Policy: frame-ancestors <hub web origins>` combining `public_url`, `public.advertise`, `preview.allowed_ancestors`, and the verified same-host `Origin` of the grant request.

### Fallback signal (`postMessage`)

When an `<iframe>` or document navigation (`GET` with `Sec-Fetch-Dest: iframe|document` or `Accept: text/html`) cannot be served, the hub returns a small self-contained HTML error page (strict nonce CSP, `Referrer-Policy: no-referrer`, `Cache-Control: no-store`, `frame-ancestors` restricted to the hub web origins) that posts a secret-free fallback message to each allowed hub parent origin (never `"*"`):

```json
{ "persona_preview": "unreachable", "code": "<reason>" }
```

Non-HTML subresources and non-GET requests receive a JSON error body `{"error": "…", "code": "<reason>"}` instead.

| `code` | HTTP status | Meaning |
| --- | --- | --- |
| `port_gone` | 502 | Nothing is listening on the target port (`port.dial` returned `port_gone`) |
| `offline` | 503 | Target machine is offline or disconnected (or device store temporarily unavailable) |
| `timeout` | 504 | Dialing the target port through the node timed out |
| `too_many_connections` | 429 | Machine reached the 64-stream port-forwarding limit |
| `port_not_allowed` | 403 | Port is no longer in any session's `ports` and was not opened with `POST …/ports/{port}/open` |
| `bad_gateway` | 502 | Upstream connection error or reset while proxying |
| `auth_failed` | 401 / 403 | Redemption code expired / replayed / invalid, preview cookie expired, or device revoked |

Web clients should verify `event.origin === grant.origin` (and `event.source === iframe.contentWindow`) and `event.data?.persona_preview === "unreachable"` before acting: e.g., re-granting once on `auth_failed` if the device is still signed in, or falling back automatically to the headless browser stream (`GET /api/v1/machines/{mid}/preview/connect`) on `port_gone`, `timeout`, `bad_gateway`, or `too_many_connections`.

### Web panel modes and fallback (`web/src/views/session/PreviewPanel.tsx`, `web/src/lib/preview.ts`)

- **Default mode**: When `hello.features` includes `preview_origin`, opening a dev server starts directly in isolated-origin iframe mode without asking the user to pick a mode. When `preview_origin` is absent, the panel stays on the headless browser stream with unchanged behavior and controls.
- **Direct mode iframe**: Opens the port (`POST …/ports/{port}/open`), requests `POST …/preview/grant {port, path}`, and renders `<iframe src={grant.url}>` with `sandbox="allow-scripts allow-same-origin allow-forms allow-modals allow-popups"` (no `allow-top-navigation`, so the preview page cannot navigate the hub tab) and `referrerPolicy="no-referrer"`. Desktop fills the panel; Phone renders a `390 × 844` frame scaled to fit without unmounting the iframe on toggle.
- **Controls by mode**:
  - **Both modes**: Reload (in direct mode reloads `grant.origin + currentPath` using the session cookie rather than replaying the one-time redemption code), Address bar (in direct mode swaps `src` directly on the same port, re-grants when navigating to another localhost port, and offers external URLs in a new tab), Desktop / Phone viewport toggle, Pick an element, and Screenshot.
  - **Browser stream only**: Back and Forward history buttons, and live in-page address synchronization. These are hidden in direct mode because cross-origin iframe history cannot be inspected or driven without affecting the hub page's own history.
  - **Pick an element & Screenshot in direct mode**: Clicking either automatically switches the panel to viewing through the computer (with a brief notice banner) and runs the pick or screenshot once the browser stream is ready.
- **Automatic fallback**: Direct mode falls back to the headless browser stream automatically when `preview/grant` fails (non-2xx or malformed response), when a verified `unreachable` `postMessage` arrives (`event.origin === grant.origin` and `event.source === iframe.contentWindow`; `auth_failed` retries `preview/grant` once before falling back, while all other codes fall back immediately; messages from any other origin or window are ignored), or when the iframe does not finish loading within 15 seconds (`DIRECT_LOAD_TIMEOUT_MS`). After falling back, the panel shows a dismissible notice ("Couldn't show the page directly, so it's shown through the computer.") and remembers the fallback for that `(machine, port)` within the panel session so switching servers and returning does not bounce back and forth.
- **Manual switching**: Shown only when `preview_origin` is advertised, inside the preview toolbar's `⋯` secondary menu (never as a top-level button): **View through the computer** while in direct mode, and **View directly** while in browser stream mode (which clears any remembered fallback for that port).

### Verification

- Go unit and integration tests live in `server/internal/hub/preview_origin_test.go`.
- Real-browser isolation E2E test: `node scripts/test/preview_origin_isolation_e2e.mjs` (runs a throwaway Go hub + SNI wildcard TLS + dev server via `TestPreviewOriginE2EHarness` and launches Chromium with `--host-resolver-rules` to prove parent `localStorage` / `document`, `document.cookie`, cross-origin hub API fetches, same-origin `/api/v1/*`, `document.referrer`, `location.href`, and cross-origin framing are all blocked while ES modules, native `<select>`, dev cookies/redirects, HMR WebSocket, and `unreachable` `postMessage` work).
- Web panel unit and E2E tests: `web/src/lib/preview.test.ts`, `web/src/views/session/PreviewPanel.origin.test.tsx`, and `scripts/test/web_preview_e2e.py` (with `MOCK_PREVIEW=1` and `MOCK_PREVIEW_ORIGIN=1`).

