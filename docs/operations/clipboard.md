# Clipboard sharing: the phone and your computers

Copy a link or a screenshot on the phone and paste it on a computer, or the other way round, without passing it
through a chat app (#3064). Persona Link is unchanged: the feature lives in the resident node (`personad node`), which
already runs in the user's login session, and goes over the existing manager ↔ node RPC.

```
Phone App ── GET/PUT /api/v1/machines/{mid}/clipboard ──▶ hub ──(relayed, devicewire.Routes)──▶ manager
                                                                                                   │ switch on? full credentials?
                                                                                                   ▼
                                                                     node: clipboard.get / clipboard.set (its own session's clipboard)
```

## What it does (first version)

- **Read** a computer's clipboard: text, or an image (always returned as PNG).
- **Write** text, or a PNG / JPEG image, to a computer's clipboard.
- **Concealed items** that a password manager marks as secret or transient come back as `kind: "concealed"`
  without their content: `org.nspasteboard.ConcealedType` / `org.nspasteboard.TransientType` on macOS,
  `ExcludeClipboardContentFromMonitorProcessing` on Windows, `x-kde-passwordManagerHint` on Linux.
- **Text first**: when the clipboard holds both (a spreadsheet's cells, files copied in Finder or Explorer carry a
  picture of themselves), the text is returned; an image alone (a screenshot, "Copy Image") is returned as PNG.

Not in this version: automatic sync between computers, a computer pushing to the phone (that needs a node → hub
notification), and entries on Android, the web and the Mac app. iOS cannot read the clipboard in the background, so
phone → computer is always a tap.

## Per-computer switch (off by default)

Each computer has a **Share clipboard** switch, off until the user turns it on. The manager keeps it in the settings
table (key `machine_clipboard`, a map of machine id → true; Postgres like every other server state, no migration) and
caches it for 10 seconds (another manager process on the same settings, as in a blue-green switch, may change it);
when settings cannot be read it fails closed (off). The switch can be changed while the computer is
offline. The machine list carries the state:

```json
"clipboard": {"supported": true, "enabled": false}
"clipboard": {"supported": false, "enabled": true, "reason": "This computer has no desktop session, so it has no clipboard.", "reason_code": "no_display"}
```

`reason_code` is stable (`no_display`, `no_tool`, `no_session`, `os`, `node_outdated`); `reason` is filled by the
hub in the reader's language (`i18n.ClipboardStatus`, catalog `devices-clipboard.reason_<code>`). A machine whose node
has never connected has `supported: false` and no reason yet.

## API

| Method | Path | |
| --- | --- | --- |
| GET | `/api/v1/machines/{mid}/clipboard` | → 200 `{kind: "text" \| "image" \| "empty" \| "concealed", text?, image?: {mime: "image/png", data: <base64>}}` |
| PUT | `/api/v1/machines/{mid}/clipboard` | `{"text": "…"}` or `{"image": {"mime": "image/png" \| "image/jpeg", "data": <base64>}}` → 204 |
| GET | `/api/v1/machines/{mid}/clipboard/settings` | → 200 the machine's `clipboard` object |
| PUT | `/api/v1/machines/{mid}/clipboard/settings` | `{"enabled": bool}` → 200 the machine's `clipboard` object; `machines` is pushed as usual |

Errors use the usual body (`{error, code}`):

| Status | `code` | When |
| --- | --- | --- |
| 403 | `read_only` | Not full credentials: the watch's answer-only token, an extension's token |
| 403 | `clipboard_disabled` | The switch is off for this computer |
| 409 | `clipboard_unsupported` | The computer cannot share its clipboard; `error` says why |
| 503 | `offline` | The computer is offline |
| 413 | `too_large` | Body over 10 MB, text over 4 MiB, an image over 7 MiB (either way) |
| 400 | `clipboard_bad_image` | The image's bytes are not the PNG / JPEG its `mime` names |
| 400 | `bad_request` | Neither or both of `text` / `image`; missing `enabled` |
| 502 | `clipboard_failed` | The node could not read or write the clipboard |

The hub's own checks come first: answer-only devices are limited to an allow-list of paths
(`hub/devicetokens.go`), and no extension scope opens these routes (`extwire.RouteScopes`). The manager checks the
relayed role again.

## Node (`server/internal/node/clipshare/`)

`clipboard.get` / `clipboard.set` are announced with the capability `clipboard`; `node.info` carries
`clipboard: {supported, reason?}`. The node does not know about the switch; the manager only calls it while it is on.

| OS | How | Unsupported when |
| --- | --- | --- |
| macOS | `osascript -l JavaScript` (JXA) talks to `NSPasteboard` directly. `pbcopy` / `pbpaste` were not enough: they see only plain text, cannot see pasteboard types (so not the concealed marker), and use the locale's encoding (a LaunchAgent without `LANG` turns Chinese into MacRoman). JXA ships with every macOS and needs no cgo or extra binary to build and sign. | never (the resident node runs in `gui/<uid>`) |
| Windows | user32 (`OpenClipboard` …): `CF_UNICODETEXT`; images from the registered `PNG` format, else `CF_DIB` converted to PNG (`clipdib.go` in that package); writing an image puts both `PNG` and a 32-bit `CF_DIB` | the node runs in session 0 (`no_session`) |
| Linux | `wl-paste` / `wl-copy` under Wayland, `xclip` under X11 (or XWayland without wl-clipboard) | no `DISPLAY` / `WAYLAND_DISPLAY` (`no_display`), or neither tool installed (`no_tool`) |
| other | | always (`os`) |

The content never touches the disk, a command line or a log: it moves through the helper's stdin / stdout (macOS,
Linux) or memory (Windows). Errors say what failed, never what was copied. Each read or write gives up after 15 s.

## Privacy and safety

- Off by default, per computer; only devices with full credentials.
- The manager logs only the kind and the size (`clipboard set from the app: text, 16 bytes`), never the content.
- Concealed items are never sent.

## Verification

- Unit tests: `go test ./internal/node -run 'Clip|DIB'` (macOS, Linux and Windows' DIB conversion with fakes),
  `go test ./internal/devices -run Clipboard` (switch, routes, roles, errors, no content in logs),
  `go test ./internal/hub -run Clipboard` (answer-only devices, extension scopes, localized reasons).
- Real macOS pasteboard: `PERSONA_CLIPBOARD_LIVE=1 go test ./internal/node -run MacClipShareLive` (text with CJK
  characters and a PNG round-trip on the Mac running it; puts back what was there).
- Windows: the user32 path is cross-compiled (`GOOS=windows go vet ./internal/node`); run it on a real Windows
  computer before relying on it. One thing to watch there: writes open the clipboard without an owner window
  (`OpenClipboard(NULL)`, as common Go clipboard libraries do); Microsoft documents that `SetClipboardData` can then
  fail, in which case the node needs a hidden message-only window as the owner.
