# Port forwarding: viewing pages an Agent serves, on your phone

An Agent starts a dev server on one of your computers (vite, next dev, `python -m http.server`…). One tap on the phone shows the page inside the App,
and hot reload keeps working. The reference is Browser preview in Moshi (an SSH / mosh terminal for iOS); we borrow only the interaction.

## At a glance

```
Phone App                             hub                                node (that computer)
WKWebView ─ http://localhost:5173 ─▶ local 127.0.0.1:5173 (App listens)
                                      │ one WebSocket per TCP connection
                                      ▼
                         GET /api/v1/machines/{mid}/ports/5173/connect (device token)
                                      │ opens a stream (stream.*) on the existing hub↔node connection
                                      ▼
                                                       connects to local 127.0.0.1:5173 (or the address it was bound to when scanned)
```

## Discovering ports (node, `server/internal/node/ports.go`)

- **Who owns it**: for each live Agent's pane, take the process the host reports (herdr's foreground process; the first one is claude / codex itself),
  walk all its descendants, and add the processes on the same terminal (tty) (including ones adopted by init after `npm run dev &` and the outer shell exiting).
  Listeners belonging to claude / codex themselves (IDE bridge, MCP) do not count. herdr lists the foreground group's leader first, which is a wrapper
  when the launcher runs claude under one (`<wrapper> run -- <claude arguments>`), so the herdr client puts Claude Code's own process first: the one named
  after a version (Claude Code names its process so), else one whose name or program file says claude. herdr 0.9.5 reports a process's `argv`, not
  `argv0`. Taking the wrapper for the agent listed Claude Code's own loopback listener as a server it runs, which kept an idle Agent from Close (#14681).
  The host is asked for a pane's foreground processes only when the pane is new or its first process has changed (#5515): while that process is still in the
  process table with the same parent, terminal and name, the last answer is used again, less any other process in it that exited or whose pid now names a different process (a reused pid could belong to another pane). A shell first (the Agent not started yet) is asked for on every scan,
  so claude starting under it is not taken for one of the pane's servers. Before this, every scan made one herdr call per pane (about 10 calls/s with 40 Agents at work).
- **How it scans**: on Linux it reads `/proc` directly (`/proc/*/stat` for the process tree; LISTEN sockets in `/proc/net/tcp{,6}` are matched to process fds by inode),
  with no child processes. On macOS it uses `ps -Ao pid=,ppid=,tty=,comm=` plus `lsof -a -p <these processes> -nP -iTCP -sTCP:LISTEN -Fpcn`.
  `lsof` exits 1 when one of the pids has exited / is a zombie but still prints results, so results are used as-is; when a scan really fails, the same error is logged only once.
  On macOS one scan costs about 0.17 s CPU (on a machine with about 3,700 processes, `ps` is most of it).
  Windows (#2098) also spawns no child processes: the process tree comes from a Toolhelp snapshot (`CreateToolhelp32Snapshot`), listening sockets and their owning processes come from iphlpapi's
  `GetExtendedTcpTable` (`TCP_TABLE_OWNER_PID_LISTENER`, one table each for IPv4 and IPv6), in `server/internal/node/ports_windows.go`.
  Windows has no controlling terminal, so ownership is by process tree only. After a parent exits, Windows still reports its pid, and pids get reused, so when the parent started later than the child
  (or its start time can't be read) the parent-child link is ignored, so a new process doesn't "adopt" someone else's children.
  Git Bash breaks that tree (#9815): MSYS2 emulates `fork`, and for `cd x && cmd` the forked copy that runs `cmd` as a new Windows process exits, so `cmd`'s parent pid names a process that is gone. The scan reads every listener anyway, so a listener no pane's tree owns has its process's engine pane variables read (`ORCA_PANE_KEY`, `HERDR_PANE_ID`…, which MSYS passes on; cached per process) and counts for the pane they name when that pane is being scanned. The pane's agent itself stays out, as from the tree (`paneListeners`). On a machine with about 650 processes and 65 listening sockets a scan took 15 ms without that and 18 ms with it.
- **What counts**: for each `pid:port`, send one `GET /` (`Host: localhost:port`, which dev servers' host checks accept). Only ones that answer with HTTP are listed,
  and the home page's `<title>` (up to 60 characters) is captured along the way. One that accepts the connection but says nothing for 3 seconds (next dev compiling for the first time) is listed without a title and asked again on the next scan, up to 5 times.
  Ones that don't answer with HTTP (ssh, databases) are not listed. Results are cached per `pid:port` and dropped when the process or port goes away.
- **How often**: every 15 seconds while there is a live Agent. When an Agent's transcript changes (usually a tool ran; starting / stopping a server is one tool call)
  or a forward finds the port gone, an extra scan runs within 4 seconds. No Agents, no scanning.
- **How it is pushed**: results go in `Session.ports` and travel with the session list. If the port set (port, pid, title) hasn't changed, nothing is pushed (`listKey` includes it),
  so scanning adds no extra pushes (the lesson of #178: don't push what hasn't changed, don't make the UI recompute).

## Recognizing links in replies (App, `apple/Persona/Code/PortLinks.swift`)

- `http(s)://localhost|127.0.0.1|0.0.0.0|[::1]|*.localhost|this machine's hostname (with or without .local):port/path` all count as "a port + path on this machine",
  as does the scheme-less `localhost:3000/x` that servers often print. Links without a port, LAN addresses and other hosts don't count (LAN addresses open as ordinary web pages).
- "This machine" is the machine the session runs on (for assistant chats, the hub machine).
- These show up in: Agent replies (a row of badges under the message), Bash tool output (under the output once expanded), the top of the session (`Session.ports`),
  and when tapping such a link itself in a message or the terminal (`ChatActions.openLink` checks for port links first). These links are no longer preloaded (preloading would open the phone's own localhost).
- A link to one of the hub's own pages (`/s/…`, `/todos`, `/persona/…`; a hub on the agent's machine) is no dev server: it gets no badge and opens as that page (#2696).
- The web (#504) recognizes the same links (`web/src/lib/port-links.ts`) and draws the badges under an agent's finished answer (`views/chat/PortChips.tsx`): `:5173` with the page's
  title, "Stopped" and dimmed once nothing listens there. A port some agent on that machine lists (`Session.ports`) is live; any other is asked about with `GET …/ports/{port}`
  (`lib/port-presence.ts`: a live answer is kept a minute, a gone one 30 s, no answer 20 s). A badge, or the link itself, opens the page in the **Preview** panel ([preview.md](../web/preview.md)),
  only on a hub with `preview` and for an agent's session with a working folder; assistant conversations get none.
## Forwarding

### Why "listen locally on the phone"

| Option | Problem | Verdict |
| --- | --- | --- |
| One subdomain per port (`p5173-mac.<hub domain>`) | Needs wildcard DNS and a wildcard certificate, so DNS has to change | Optional opt-in for web previews (`preview.domain`, [preview.md](../web/preview.md) "Isolated preview origin"); not required for the App |
| hub opens a separate port per forward | New public ports, each one has to be allowed through the firewall | Rejected |
| Path prefix + rewriting (`/api/v1/…/ports/5173/…`) | SPA absolute paths (`/@vite/client`, `/_next/…`), the HMR ws address, Cookie Path and redirect Location all need rewriting; rewriting HTML / JS is unreliable | Rejected |
| **The App listens on the phone's 127.0.0.1 / ::1, one WebSocket to the hub per connection** | Only the App (iOS, Mac) can do it, not the web version (browsers can't listen) | **Chosen (zero-config default for mobile)** |

Same as Moshi: on the phone the page is `http://localhost:5173/`, exactly as on the computer. Bytes are forwarded untouched, so HTTP / WebSocket (HMR) /
redirects / cookies need no rewriting. No DNS changes, no new ports: it uses the hub address and device token the App already has.

### Phone (`apple/Persona/Code/PortForwarder.swift`)

- One forward per (group, machine, port), since machine ids are only unique within a group: binds both `127.0.0.1` and `::1` at the same port number when both are free; if either is taken (another forward on the phone, or an existing program on the Mac running the simulator), it picks an ephemeral port free on both so `localhost` (which resolves `::1` first) never connects to an unrelated process. Only loopback is bound, so nobody on the LAN can connect.
- Each incoming connection opens a `wss://<hub>/api/v1/machines/{mid}/ports/{port}/connect` (`Authorization: Bearer <device token>`) on a `URLSession` configured with `httpMaximumConnectionsPerHost = 64` (matching the hub's `maxPortStreams = 64`, avoiding iOS's default limit of 4 starving multi-module dev servers).
  The hub address, route and token are read when each connection arrives, not when the forward starts (#1886): the group's current hub
  (the one its socket is on), `HubTransport`'s tunnel route at that moment, and the current device token. After a switch between a tunnel
  and a direct address, to another hub of the group, or a re-pairing, a reload or HMR reconnect goes the new way. Disconnecting a group,
  removing its last hub or unpairing closes its forwards (`AppModel.forgetGroups` / `unpair`).
  Each side "finishes sending the previous chunk before reading the next", so a slow side holds back a fast one instead of piling up in memory; client TCP half-close stops local reads while continuing to deliver remote response bytes until the remote side closes.
- The listener stays up. If it breaks after the App is suspended, `ensureReady` rebuilds it when returning to the foreground, refreshing, or next opening (reusing the old number when possible; the page's HMR reconnects by itself).
- WebSocket handshake failures (`401`/`403`/`404`/`429`/`502`/`503`/`504` or unreachable hub) are recorded per forward and surfaced in `LinkSheet` as a plain-language `EmptyStateView` error overlay with **Retry** instead of a blank WebView. Self-signed TLS certificates on loopback ports owned by `PortForwarder` prompt for explicit session-scoped confirmation (defaulting to untrusted), while all other hosts keep system default TLS verification.
- **Two preview modes and fallback on iPhone** (`apple/Persona/Views/Link/PreviewStreamView.swift`, `PreviewStreamModel.swift`, [preview.md](../web/preview.md) "iPhone"): direct loopback forwarding stays the default, and when the hub supports `preview` and the computer has a supported browser, users can also switch manually in the preview card's `⋯` menu between **View through the computer** (the node's headless browser stream over `GET /api/v1/machines/{mid}/preview/connect`) and **View directly**. Direct errors that a remote browser can resolve (gateway/TLS/timeout/listener failures) offer **View through the computer** beside **Retry**, while errors that streaming cannot fix either (`port_gone`, computer offline, auth/permission, hub unreachable, no internet) omit it. Declining a self-signed loopback certificate, an untrusted TLS failure, a direct load timeout, a listener failure, or two consecutive recoverable failures automatically fall back to **View through the computer** with a dismissible notice banner and stay in that mode for the open card without bouncing back.
- Before opening, ask the hub: if the port is in some Agent's `ports`, open it directly (re-probing via `POST …/open` once if the first load fails, in case the 15-second scan cache is stale); otherwise `POST …/open` (the user explicitly tapped it). If nothing is listening, show "Nothing is listening on port :N anymore",
  and the badge turns gray.
- The web card is the same one used for ordinary links (`LinkSheet`), with "machine name :port" under the title bar. Its menu has no **Open in Safari** or **Share**
  (an address on the phone's loopback means nothing to other apps); for forwarded previews on a preview-capable hub, the `⋯` menu holds **View through the computer** / **View directly** and **Copy Link**. Dev server pages skip the disk cache (`reloadIgnoringLocalCacheData`). A port page's URL is the same every time, so `WebViewPool` hands back the previous WKWebView:
  it is reloaded when taken out (unless it is still loading), otherwise you'd see the page as it was when last closed (2026-11-05, #1934). The refresh button in the top bar (`link-reload`) uses `reloadFromOrigin`
  to fetch again (or sends `reload` in stream mode); native Lark pages don't have it.
- The Mac App uses the same code; the sandbox needs `com.apple.security.network.server` (listening on loopback only).
- Android (#2144): `core/hub/PortForwarder.kt` likewise listens on 127.0.0.1 and ::1 with one WebSocket per connection; see `docs/android/android.md` ("Terminal and links").

### hub (`server/internal/hub/ports.go`) and streams (`server/internal/streams`)

- `…/connect` checks first, then upgrades: the port must be in the `ports` of some session on that machine, or the user must have `POST …/open`ed it within 24 hours; otherwise 403.
  Then it registers a stream and asks the node to `port.dial`. If that fails it returns 404 (not yet upgraded, so the App gets the status code); only on success does it upgrade to WebSocket.
- A stream uses `stream.data`, `stream.ack` and `stream.close` events on the hub↔node JSON-RPC connection (`docs/protocol/protocol.md`). Each direction allows at most 512 KB
  unacknowledged; at that point the sender waits. A large file from a page can't fill the connection and starve other RPCs, and a slow reader can't make the other end buffer without limit.
  Event handling (`Mux.Event`) never blocks, because it runs in the RPC read loop.
- TCP endpoints that negotiate stream version 1 can send `stream.eof` to half-close one direction. The App's binary WebSocket path uses full close.
- At most 64 streams per machine at once (a page typically uses about 6); when a machine disconnects all its streams are closed.
- The slow-request log skips `…/connect` (it is long by nature).

## Security boundary

- Auth is the same as other APIs: device token (or pairing token). Read-only devices (the watch) always get 403, even for an Agent's port.
- Only ports on **the configured machine itself** can be reached: the node only dials the address that listener was bound to when scanned (a local one) or 127.0.0.1 / ::1, and accepts no hostnames. It is not a proxy that can open arbitrary intranet addresses.
- The port must belong to an Agent (found by the node's scan) or have been tapped open by the user in the App; guessing a port number gets 403. Tapped-open ports are kept in hub memory for 24 hours
  and cleared on hub restart (just tap again).
- Probing (`GET …/ports/{port}`) only reports "is something listening, is it HTTP, title"; it grants no access.
- Exception: when the `cua` plugin is on for a Mac, that Mac's 5900 (macOS Screen Sharing) is permanently allowed, for "VNC" (see [computer-use.md](../extensions/computer-use.md), "Live view"); the WebSocket accepts the `binary` subprotocol (noVNC may send it).
## Not done yet

- Without `preview.domain` configured on the hub, the web version still shows a dev server through a headless browser on the computer ([preview.md](../web/preview.md)). When `preview.domain` is configured, the hub's isolated preview-origin proxy (`server/internal/hub/preview_origin.go`) reuses the same `port.dial` + `streams.Mux` tunnel, `portAllowed` rules, and 64-stream cap to serve HTTP and WebSocket (HMR) directly to an isolated web `<iframe>` ([preview.md](../web/preview.md) "Isolated preview origin").
- Android https dev servers: bytes are forwarded, but Android WebView does not yet have the iOS loopback self-signed certificate confirmation prompt.
