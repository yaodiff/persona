# Orca: how the node drives it

Audience: maintainers and contributors working on the Orca engine. What a user sees with Orca is in
[engines.md](engines.md) ("Capabilities by engine", "When Orca is not running", "Refusals in the reader's language");
this page is how the node gets there: the connection, how Orca's terminals map onto Persona's sessions, what was
found checking Orca, the cautions, Orca's own chats and the registered bot endpoints. The open decisions and the
streaming transport, a no-go for now, are in [plan-orca-support.md](../plans/plan-orca-support.md). Code: client
`server/internal/orcart`, host `server/internal/node/hostorca*.go`, chat bridge `server/internal/node/orcabridge*.go`, fake
runtime `server/internal/orcart/orcarttest`.

## Orca source

First built against Orca 1.4.245's source; checked since against 1.4.277 (its runtime bundle and an isolated headless
runtime, "Cautions" below).

### Connection

- Read `~/Library/Application Support/orca/orca-runtime.json` (0600): Unix socket path and `authToken` (Windows Orca writes `named-pipe` transport,
  `\\.\pipe\orca-<pid>-…`, not `unix`; see [windows.md](../operations/windows.md), "Orca"). Orca changes tokens on every launch;
  reread on connection failure or `unauthorized`. When Orca is not running, the file is absent and the host reports unavailable.
- Protocol: one JSON per line, request `{id, authToken, method, params}`, response `{id, ok, result | error}`. Slow calls may first send `{"_keepalive":true}`; skip it.
- Any process running as the same user can connect, without `ORCA_*` environment variables, including nodes started over ssh or kept running by launchd.
- This socket protocol has no public documentation, only an internal version (`RUNTIME_PROTOCOL_VERSION`, currently 3). Check capabilities with `status.get` at startup; disable if incompatible.
  Connect directly to the socket, not a new `orca` CLI process each time (starting a Node process every second while polling is too heavy).

### Terminals

Orca is an engine of its own (engine name `orca`, display name "Orca"), alone or in a host set beside herdr and the others
([engines.md](engines.md), "Host sets"). Its terminals were the first phase (2026-11-01); everything after built on them.

- **Pane ID**: Orca pane key `<tabId>:<leafId>` (`worktree.ps` reports Agents by it; terminal process `ORCA_PANE_KEY` also contains it), stored as
  `orca:<tabId>:<leafId>`. The host resolves handles required by `terminal.*` (`term_<uuid>`) by pane key from the latest `terminal.list`.
  On expired handles (`terminal_handle_stale`), relist: reads follow normally; writes (send, keys, close) retry only if it is still the same terminal process (`incarnationId` unchanged).
  If the process changed, reject without input. Reads, writes, and closes all carry `expectedIncarnationId`; Orca rejects a handle whose process changed, preventing writes to its replacement.
- **Connection**: open a connection per call (the socket supports multiple requests per connection, but we do not need that). Before the first call and after Orca changes runtime (`runtimeId` changes), call
  `status.get`: `runtimeProtocolVersion` below 3 or `minCompatibleRuntimeClientVersion` above 3 is incompatible, all calls error,
  and `node.info` marks Orca unavailable with a `note`. Missing metadata, no local transport for the platform (macOS / Linux `unix`; Windows `named-pipe`, then `unix`), or missing/refused socket / pipe means "not running" (`host.ErrNotRunning`):
  immediately answer "No terminal", with no logs if never connected. On connection failure or `unauthorized`, reread metadata and retry once (restart changes socket and token).
  If reply `_meta.runtimeId` differs from metadata, reread next time. Orca responds to unreadable requests (bad JSON, too large) with ID `unknown`; treat as rejection of this request.
  Metadata lookup: if `$ORCA_USER_DATA_PATH` is set, use only that. Otherwise check two locations and read the most recently written when both exist (an old runtime may leave a stale file):
  desktop app directory (macOS `~/Library/Application Support/orca/`, Windows `%APPDATA%\orca\`, Linux `$XDG_CONFIG_HOME/orca/`),
  and orcad directory (remote runtime started here over SSH by Orca on another computer, platform-independent: `$ORCA_USER_DATA`, otherwise `$XDG_DATA_HOME/Orca`, otherwise `~/.orca`;
  Orca 1.4.246's orcad, #2070). If unreadable, rediscover next call so Orca can start after node.
- **Timeouts**: `terminal.list`, `worktree.ps` 3 seconds (each refresh); `status.get`, `terminal.read`, `terminal.agentStatus` 5 seconds; `terminal.send` 30 seconds
  (Agent prompts use Orca-side long polling with keepalives); `terminal.close` 10 seconds.
- **No push**: does not implement `Watcher`. Orca machines have no event stream; node polls a snapshot every second, not the slow 10-second snapshot used with herdr event streams.
  Keep it lightweight: normally omit layouts in `terminal.list` (`includeVisualLayouts:false`; Orca says this is expensive), requesting them only when the terminal set or a worktree's `topologyRevisions` changes,
  or after 30 seconds. Pass the previous `afterSnapshotId` to `worktree.ps`, which returns only `unchanged` if unchanged. Scan processes at most every 1.5 seconds,
  in two steps (`ps -o pid=,stat=,etime=,args=` to find Claude Code, Codex, TRAE CLI, omp and the Antigravity CLI (`agy`), then `ps -E` only for them; Linux reads `/proc`). `Agents` after a failed snapshot reuses that attempt's result.
  Local measurement: about 0.2 seconds per refresh (10 terminals).

### Mapping

| Persona requirement | Orca implementation | Status |
| --- | --- | --- |
| List panes | `terminal.list {includeVisualLayouts, includePreview:false}` (layouts requested only under the "No push" rules above): local only (`executionHostId` is `local`; terminals on SSH targets have no node there to manage them), excluding `orphaned` ones except a terminal this host created (#5547, "New sessions" below) | ✅ |
| Identify Agents | Requires a **foreground** Claude Code, Codex, native TRAE CLI, omp or Antigravity CLI (`agy`) process (TRAE CLI and agy on Linux and macOS; `agentOfCmd` / `orcaAgentOfCmd` in `node/hostorca_procs.go`) in the pane (`ORCA_PANE_KEY` matches the pane key; fall back to `ORCA_AGENT_PANE`): not suspended (`ps` stat does not start with `T`), in the terminal foreground process group (stat contains `+`; Linux compares pgrp and tpgid in `/proc/<pid>/stat`), without `HERDR_PANE_ID` / `HC_TERMINAL_ID` (claude in herdr / herdr-core nested inside Orca inherits the outer `ORCA_PANE_KEY` but belongs to its own host). Orca's `agentIdentity` and Agent rows can remain after exit, so neither alone suffices. Only if process scanning fails does listing fall back to both `agentIdentity` and a matching Agent row with `hasLivePty` (all writes then reject) | ✅ |
| State | Agent row for this pane key in `worktree.ps` (matching type, `updatedAt` no earlier than 2 seconds before process start, or it may belong to the previous Agent): prefer `mainAgent.state` (main Agent's own state; row `state` includes children, remaining working if the main turn ended but background children run). Keep working / blocked / done, map waiting to blocked. Without a usable row, ask `terminal.agentStatus` (3-second cache): working unchanged, permission → blocked, idle → done; null `status` → unknown | ✅ Polling |
| Map pane to session | Foreground claude pid for the pane → `sessions/<pid>.json` (shared "process identifies session" path). An `agy` pane's conversation comes from Persona's agy hooks (`ORCA_PANE_KEY`) or, on Linux, from the `presence/<conversation>.lock` its foreground process holds open, as for a classic herdr pane (#16531, [engines.md](../integrations/engines.md), "Capabilities by engine"). macOS uses two `ps` calls (above), Linux reads `/proc`, Windows reads process PEB ([windows.md](../operations/windows.md), "Orca"). Extract only `ORCA_PANE_KEY` / `ORCA_AGENT_PANE` and the two nested-host exclusion variables; never retain, log, or send environments to hub (they also contain Orca's hook token) | ✅ |
| Spaces / slots | Space is worktree (`worktreeId`), named from branch (strip `refs/heads/`), falling back to path basename, ordered by `worktree.ps` (Orca sidebar order); a worktree on an SSH target (`hostId` not `local`) is no space of this computer's, in the layout or the workspace catalog (#17050). Slots are tabs in `visualLayouts`, in layout-tree reading order (`first`, then `second` for splits), named by tab title or index if empty | ✅ |
| Read screen | `terminal.read {terminal, screen:true}`: plain text, current screen only; rejoin soft wraps using `tailRows[].wrapped`; error on `source: screen-unavailable`. Host declares `host.PlainScreen`. The host's `recent` read also returns only the current screen, which the node's own checks read; what a client shows above the screen (`session.screen.before`, `session.screen` `recent`) comes from Orca's output tail and, where it agrees, its log by cursor (`host.Scrollbacker`, #17048, [terminal.md](../integrations/terminal.md) "31. Orca terminals: scrolling up, and a quicker plain view (#17048)"); the screen read is cheap, so a page polling it may read it four times a second while it changes (`host.QuickScreen`). Orca 1.4.277 lifts the text of an Agent's input box out of the rows into `draft`, leaving the prompt row as its bare glyph (`❯`, `›`): the read puts it back in that row (`orcart.Screen.Text`), or the node would take every box for empty and type `/effort` into a draft. The draft has no colors either: checked live on an isolated runtime with Claude Code 2.1.331, a typed draft and Claude's grey suggestion come back alike (Orca's own emulator knows which cells are dim, but no method on the local socket says so), so effort and speed changes are refused while the suggestion shows (#17042) | ✅ Plain text, scrollback |
| Send message | Do not use Orca's `agentPrompt`: it bracket-pastes only to Agents it has "seen working". Newly launched claude receives raw text + `\r`, submitting a multiline message at its first newline (tested 2026-11-01). Rejections with `accepted:false` (`no-agent` / `permission`) require `requireAgentStatus:"sendable"`, which Orca always rejects alongside "text + Enter", so it is unusable here too. Follow herdr's `agent.prompt`: first verify a foreground Agent (process table has a recognized foreground Agent in the pane, not inside a host nested in it; `terminal.agentStatus` is asked too: permission status returns `dialog_open`, an error means not foreground; its `isRunningAgent` is not required since #5463, because Orca reports false while Claude Code has background tasks and still takes input; failed process scan means not foreground; refusals return `not_delivered` with no input and log the check that refused), then send bracketed paste ourselves: `terminal.send {text: ESC[200~ + text + ESC[201~}` (no `enter`; replace ESC in text with `<ESC>`, like Orca, to prevent premature paste termination). Wait 150 ms plus 10 ms per line, capped at 1 second; verify foreground Agent again, then send `\r` separately. Any failure after paste returns `delivered:unknown` (text may remain in input). Always pass `client: {id:"persona", type:"desktop"}` (`client.id` required; absent yields `invalid_argument`). Pre-send checks only detect dialogs (no color to distinguish drafts from gray text). With no supported Agent in front but another program `terminal.inspectProcess` names (a CLI the scan does not know), `Prompt` answers `ErrNoAgent` and the node pastes the text and presses Enter itself while that program is still in front (send.go `typeable`, #17040); a shell in front stays refused | ✅ |
| Session not running | When a session is no longer an Agent pane (exited, suspended, backgrounded), herdr resumes it in a new pane before sending (herdr-core too, since #2532). A message to it is refused with no input on Orca: if its last pane still exists, `agent_not_in_front` (the Agent is not in front; it may be suspended); if gone and Orca cannot continue it, `not_running` ("…reopen it on the computer"). Previously this looked up a launcher and returned the confusing `unsupported: orca cannot do this`. A stopped Agent's Resume opens a new Orca terminal for Claude Code, and since #17044 for Codex, TRAE CLI and pi (`docs/integrations/engines.md`, "Capabilities by engine") | ✅ |
| After a reboot | Orca 1.4.277 keeps the tabs but lists none until a client opens one, then resumes the Agent with its own `claude --plugin-dir <agent view> --resume <id>` from what its Claude hooks reported; `terminal.create`'s `launchAgent` / `launchToken` / `launchConfig` are not used for that over the local socket (checked on an isolated runtime, #17038). So no resume command is given: the session is `interrupted` with Continue until the tab is opened, then `off_launcher` in it, and a switch back to its launcher restarts it there ([engines.md](../integrations/engines.md), "Orca after a reboot (#17038)") | ⚠️ Shown, not restored |
| Type without Enter | `terminal.send {text}` without `enter`: raw bytes, no paste, after the typing gate (`typeGate`, #17040): a foreground Agent from the scan (not permission: free-form dialog input needs it), or a program other than a shell `terminal.inspectProcess` names in front, or the owner's answer to a failed start's kept-tab question in a terminal this host opened under the same incarnation (`withStartTyping`). A shell in front of anything else, an unverifiable answer and an Orca without `inspectProcess` refuse with nothing typed. Orca does not declare `TextTyper` | ✅ |
| Special keys | Key names → xterm bytes: `enter` `\r`, `esc` `\x1b`, `shift+tab` `\x1b[Z`, `backspace` `\x7f`, `ctrl+<letter>` control characters, `ctrl+space` `\x00`, `alt+<character>` prefixed ESC. Modified arrows / Home / End / Insert / Delete / PageUp / PageDown / F1–F12 use xterm parameters (1 + shift 1 + alt 2 + ctrl 4, e.g. `alt+left` `\x1b[1;3D`, `ctrl+left` `\x1b[1;5D`). Send each key separately via `terminal.send`, 40 ms apart (cancel via caller ctx; concatenating `esc` with a following sequence can make an alt key). Reject unknown keys before sending any. Printable keys (letters, digits, space) pass the same typing gate as text; Enter, esc, arrows, ctrl+c do not (stuck panes must remain interruptible/closable) | ✅ |
| No input delivered | Foreground check fails; `terminal.send` returns `accepted:false` (permission → `dialog_open`, others → `not_delivered`); pre-write errors such as `terminal_handle_stale` (including incarnation mismatch), `terminal_not_writable`, `terminal_gone`, `terminal_exited`, `invalid_argument`, `runtime_busy`, `request_too_large` also return `not_delivered` (all `delivered:none`, safe to resend). Uncertain-write errors such as `runtime_error` remain errors | ✅ |
| Close | Like herdr, send ctrl+c (`\x03`) twice, then `terminal.close` (Orca closes the pane, or tab if it was the last pane). An already absent terminal counts as closed | ✅ |
| What runs in a terminal | `terminal.inspectProcess {terminal, expectedIncarnationId}` (`host.ForegroundNamer`, #17039): `foregroundProcess` names the program in front, `null` with `hasChildProcesses:false` is the shell itself (named from the evidence fence's `shellPid`); `idleShellProven` (1.4.277: shell in front, alone, no job behind it or stopped, no output since) is what the grace close needs. An answer whose `foregroundProcessEvidence.verdict` is not `live` (an incarnation that changed: `incarnation_mismatch`), or `childProcessEvidence:"unverifiable"`, says nothing. An Orca without the method (`method_not_found`) leaves the agent process scan, which names only a supported agent: anything else is "can't tell", which closes and types nothing. Orca 1.4.277's terminal daemon marks every answer on Windows unverifiable (`windows_ssh_foreground_unavailable`), so there nothing is closed or typed on its word | ✅ |
| Rename | `RenameSpace` sets a local worktree's display name (`worktree.set {displayName}`); `RenameSlot` renames the tab (`terminal.rename` on one of its terminals). On a runtime with no window the layout keeps the tab's first title (Orca 1.4.277, checked live): slot labels take the tab's custom name from `session.tabs.list` (the phone's tab model, which follows renames) whenever the layouts are read, once per worktree with tabs (a rename of the node's own has the next listing read them), and the guarded rename of an assistant-named tab (#15674) checks the name there; an Orca without it keeps the layouts' titles (#17043) | ✅ |
| Split, close tab | `host.Splitter`: `terminal.split {terminal, expectedIncarnationId, direction}` (`right` → `vertical`, `down` → `horizontal`); the local socket's split takes no folder (only its layout-addressed form does, which it refuses with `host_not_owned`), so a pane working outside the worktree root gets `cd <folder>` as the new shell's `command`. `host.SlotCloser`: `terminal.closeTab` on one of the tab's terminals. Neither holds `expectedIncarnationId` on 1.4.277, so the terminal is looked up afresh first. `method_not_found` → `unsupported`, nothing done, and the panes listing stops offering it for that runtime (#17043) | ✅ |
| Recognize hook pane | With no `HERDR_PANE_ID` or `HC_TERMINAL_ID`, `personad hook` reads `ORCA_PANE_KEY` (fallback `ORCA_AGENT_PANE`) and reports `orca:<tabId>:<leafId>`. node records these only on Orca-engine machines (herdr machines continue ignoring Orca panes). claude launched in Orca usually lacks Persona hooks (only Persona's own launches add them through `--settings`) | ✅ |
| New sessions, mode / thinking-effort changes | Implements `host.Launcher`; `terminal.create` supports creation in registered worktrees or their subdirectories and attempts to register unregistered Git repositories automatically. A directory in no worktree (`~/.persona`, say) gets its terminal in the worktree Orca is showing (`isActive`), else the first local one, and the start `cd`s there: Orca's UI does not adopt a terminal made in a worktree it is not showing, which about 2 s later becomes `orphaned` with its `tabId` / `leafId` rewritten to `pty:<worktreeId>@@…`, the same handle and incarnation (#5547). The node keeps the pane key creation gave, and the host finds a terminal it made (a start's, or the one Orca opens in a new worktree) by its handle and incarnation: one Orca orphaned and re-keyed stays listed under that key, its processes and agent row looked up under both keys, so the start's typing, readiness wait, tab rename and closing still reach it; one the user moved to another tab is listed where it is now. `Run` accepts an orphaned terminal only when it is that re-keyed one; a terminal whose handle changed is not ours (#972). A start whose pane is gone for 6 looks in a row (3 s) fails saying so. A start's terminal is created with the start environment as `terminal.create`'s `env` (`PERSONA_AGENT=1` and the startup switches, `host.EnvLauncher`; an Orca that refuses the field gets the create again without it, #17036). `terminal.create`'s `cwd` is not honoured for this create (Orca 1.4.277), so the start `cd`s through its launch grant, and New Terminal / Open Terminal Here for a folder asks for the terminal with `command: "cd <folder>"` and `startupCommandDelivery: "shell-ready"` (Orca types it into that new shell once it is ready; no launch grant for a user's shell). Mode and effort: the mode by shift+tab, effort and speed only while the input box shows no text (#17042; [engines.md](engines.md), "Capabilities by engine") | ✅ |
| Live terminal, colored screen reads, takeover dimensions | Only over paired encrypted WebSocket; hub falls back to screen reads ([plan-orca-support.md](../plans/plan-orca-support.md), "Streaming transport (#17048)": no-go until Orca adds a method) | ❌ |
| Push changes | Not on the socket (all `*.subscribe` methods are streaming) | — |

### Findings

- **When state rows disappear**: after an Agent exits, Orca removes its row from `worktree.ps` (retaining only recovery rows, which `worktree.ps` does not list). But `agentIdentity`
  and row removal need not synchronize with the process, so Agent identification is process-based.
- **A start does not take Orca's word alone** (#10588): a start once failed as a Claude stall in a tab Orca named claude in, where only empty prompts
  showed and the start line never arrived. A start in an Orca tab now counts claude as come only with Claude Code's screen or its start file run, and a
  line that never showed and never ran ends as `terminal_not_ready` (`node/startevidence.go`, `docs/integrations/claude-integration.md`).
- **`terminal.send` rejection is not an error**: it returns `{send:{accepted:false, bytesWritten:0, refusedReason}}`; inspect `accepted`. However, `no-agent` / `permission`
  appear only with `requireAgentStatus:"sendable"`; without it, Orca writes anyway. Orca directly rejects `requireAgentStatus` together with "text + Enter"
  (requiring two steps: text, then Enter), but that loses bracketed paste, so we must verify foreground Agent via `terminal.agentStatus` before sending.
- **Bracketed-paste conditions**: requires `agentPrompt`, text, `enter`, `client.type: desktop`, and a foreground Agent recognized by Orca **and previously seen working**.
  The reply includes a `send.prompt` receipt. Newly launched claude that has not worked yet does not qualify; Orca writes raw text followed by `\r`. Persona therefore performs bracketed paste itself (see "Send message").
  Text without `enter` is written raw. `client` requires `id` (Persona uses `"persona"`, as Orca CLI does); `type` alone returns `invalid_argument`.
- **Default screen reads switch sources**: `terminal.read` without `screen` reads the output-stream tail (`source: stream`); always pass `screen:true` for the screen.
- **Two kinds of kept output** (Orca 1.4.277, #17048): the output tail (`terminal.read {limit}`) keeps rows a program redrew in place as they ended, as a scrollback does (2000 lines and 256 KiB kept, at most 32 KiB a read, the screen instead on the alternate screen); the log by cursor (`terminal.read {cursor, limit}`, `oldestCursor` / `latestCursor`) is every completed line, each redraw of an agent's live rows included, so it is shown only where it agrees with the tail. `cursor` together with `screen:true` is refused.

### Cautions

- The token in `orca-runtime.json` grants full Orca control; claude process environments also contain Orca's hook token (`ORCA_AGENT_HOOK_TOKEN`). node reads only required variables, never logs or sends them to hub.
  The client never prints tokens; tests check logs contain no token.
- `ORCA_TERMINAL_HANDLE` may be absent (set only when a handle was preallocated). Recognize panes only by `ORCA_PANE_KEY`, falling back to `ORCA_AGENT_PANE`, whose name Claude Code does not strip.
- Orca installs its hooks globally in `~/.claude/settings.json`. claude outside Orca also runs them, but scripts exit when Orca variables are absent.
  Persona leaves these hooks alone.
- `~/Library/Application Support/orca/agent-hooks/last-status.json` also has per-pane session IDs (`entries[paneKey].providerSession.id`),
  but is an internal Orca file; do not use it.
- TRAE CLI on Linux and macOS uses the same foreground send check as Claude Code and Codex. The `traecli` command and its older alias identify the same agent. TRAE CLI history identifies the session, and the send check requires the matching foreground process in the pane.
- Live checks: the `orcalive` test against the user's own Orca is read-only, `go test -tags orcalive -run OrcaLive ./internal/node/ -v` uses a read-only client (allowing only `status.get`, `terminal.list`,
  `worktree.ps`, `terminal.read`, `terminal.agentStatus`; all other methods reject before sending) to list the active Orca once, print panes, status, pid, and session ID, and compare against
  `sessions/<pid>.json`, without printing screen content. Orca-mutating calls are tested against fake runtimes: `orcarttest` validates every request against Orca's
  parameter schema (`src/shared/rpc-contract`), returning and recording `invalid_argument` for invalid requests; tests verify none occur. And against a real Orca
  that is not the user's: the `orcaiso` tests start a headless Orca server with a temporary HOME, profile and port and drive start, send, keys, close, split,
  close tab, rename and worktree create / remove through the host, refusing any profile but the temporary one (`node/hostorca_iso_test.go`,
  [testing.md](../operations/testing.md), "Isolated Orca runtime (#17058)").
  node's test package points `ORCA_USER_DATA_PATH` at a nonexistent directory, preventing every test from touching real Orca.

## Structured sessions (#2072)

Since Orca 1.4.24x, "structured sessions" wrap Claude Code / Codex in Orca's own chat UI, without terminals. `terminal.list` omits them;
their rows in `worktree.ps` have pane keys `structured-agent-session-<session-id>:<uuid>`. The first phase recognized only terminals, leaving these invisible in Persona.
They are now integrated by the Orca bridge in node (`server/internal/node/orcabridge.go`, client `server/internal/orcart/session.go`) through the
runtime protocol (`docs/integrations/runtimes.md`), like the Paseo bridge but inside node (`net.Pipe` connects to `RuntimeHost`):

- **Activation**: enabled for the Orca engine, closed when switching away. Lists every structured session in local worktrees (`hostId: local`), whatever its provider (`agentType`).
- **Which chats**: the agent rows of `worktree.ps`, and the chat tabs of `session.tabs.listAll` (`type: agent-session`) that `worktree.ps` does not list: Orca reports a
  chat's row only once it has had a turn, so a chat opened and not written to yet is found only among the tabs. The tabs are read every 5 seconds; an Orca without the method
  (`method_not_found`) adds none. Checked on Orca 1.4.277.
- **Content**: a Claude chat's is its Claude transcript (`transcript`, `providerSession.id` from `agentSession.history`), read by the node as usual. A Codex chat is its Codex thread
  (`codex_thread`, the same field), so the node does not list the thread a second time from Codex's history; its content, like every other provider's chat's and a Claude chat's
  before its transcript is written, is Orca's own journal (`agentSession.history`, `orcabridge_items.go`): an item's `seq` is its journal sequence, a message's blocks its items
  `<seq>#<index>` (the user's and the agent's words, reasoning as thinking, tool calls and their results, Orca's warnings and errors as notices; turns, prompts and Orca's
  bookkeeping left out), `history` the journal's epoch (`orca:<epoch>`, which a rewind changes), `before_seq` / `after_seq` page with Orca's own `before` / `after` cursors.
  A provider the apps do not know is named in `agent_label` (Antigravity, Gemini, OpenCode…). Rows carry `host: "orca"`, so the apps ask the Orca engine about their worktree.
- **Session IDs**:
  - A Claude chat whose Claude session the bridge knows when it first lists it: that Claude session id, the id the same conversation has in a terminal or "Not running"
    (as before this version, so every link, pin and read state stays).
  - A chat Persona started (#17047) is `persona-<id>` in Orca and `<id>` in Persona: the id the hub named for it.
  - Any other chat (Codex, another provider, a Claude chat seen before its first message): Orca's own session id. A Claude chat listed so keeps that id once its Claude
    session appears; its row names the transcript from then on, and the node lists the transcript under no second id but names it in the row's `aliases`
    (`docs/integrations/runtimes.md`, "Runtimes that run Claude Code").
  - The bridge keeps the id it gave each chat in its **book** (`orcabridge_book.go`): `<user cache>/persona/orca/chats.json` (0600, in the node's private cache folder, the 300
    chats seen last), so a node that restarts gives each chat the same id. A book that is lost loses only the third rule's memory: such a Claude chat comes back under its
    Claude session id. Space is worktree (`worktreeId`), named from branch as for terminal sessions; child worktrees carry `parent_space_id` and collapse under their parent on
    the web, like terminal sessions (#2073).
- **Closed chats**: a chat closed in Orca (its tab gone; its record and journal stay) is listed from the book, not running (`offline`), the latest 80 of the last 7 days, as Paseo
  lists its archived agents. Its content is read as when it was open. A message to it opens it in Orca again (`agentSession.reveal`) and goes there, never to a terminal;
  modes are refused until then (`orca_closed`), and closing it in Persona takes it off the list. One Orca no longer has (deleted) answers `session_gone` and leaves the book.
  While Orca is not running every chat of the book is listed this way. So no chat's Claude transcript is left to the node as an ordinary session not running, which a Resume
  would open in an Orca terminal: the bridge sends `unlisted_transcripts: []` (it lists its closed chats itself and names none).
- **State**: poll `worktree.ps` every 1.5 seconds, using row `mainAgent.state` (working / blocked / done); a tab without a turn is idle. Push to hub only when the list changes.
- **Actions**: send text/images (messages sent while working queue after this turn, `delivery: queue-if-active`), interrupt (`agentSession.cancel`),
  close (`agentSession.close`, like Orca's close button; records remain). No terminal, screen reads, keys, or live terminal.
- **Controls** (#17046, `orcabridge_controls.go`; the bridge declares `caps.modes`, `model`, `rename`, `rewind` and `seen`):
  - Permission mode, effort and model are `agentSession.setOption`, in that order: model, then the effort chosen with it, then the mode. Persona's `default` /
    `acceptEdits` / `auto` / `bypassPermissions` are Orca's `ask` / `accept-edits` / `auto` / `full-access`. **Plan** is not a permission mode in Orca but its
    interaction mode (`setOption interactionMode plan`, Orca 1.4.277): a row in it reads `permission_mode: plan`, and any other mode picked on it sets
    `interactionMode default` first. A Claude chat takes Claude Code's chips, full access included at any time (`bypass_available`). Another provider's chat lists
    the modes Orca takes for it (`capabilities.permission_modes`, its approval as `default`, from Orca's own table: Codex ask / auto / full-access, Antigravity
    and Hermes ask / accept-edits / full-access, most providers ask / full-access, pi, aug and a provider outside the table none; plus `plan` where its options
    report an interaction mode, as Codex's do) and the efforts its model has that Persona can send (`efforts`, from `agentSession.options`' models); a mode Orca
    does not take for it is refused (`orca_mode_unsupported`) with nothing changed. A model change is offered on every open chat (`capabilities.model`), from
    the models of its provider's launcher (below) where it has one, else the computer's launcher models.
  - Rename is `agentSession.rename {sessionId, name}` (`agent-session.structured.rename.v1`; an Orca without it is refused as `orca_rename_unsupported`).
  - Rewind is `agentSession.rewind {envelope, itemId, expectedEpoch}` (`agent-session.rewind.v1`; a row says `capabilities.rewind` only while Orca has it and
    `agentSession.options` says the chat supports it). The bridge finds the user's message in Orca's journal (the newest 2,000 rows) by its text (white space
    aside), its occurrence counting from the end and how many of the user's messages came after it, and refuses (`rewind_mismatch`) a message not where the app
    saw it and a journal whose epoch moved on (Orca's `stale-epoch`); Orca's `busy` is `rewind_busy`. It rewinds the conversation only (no `rewind_code`), and
    the app puts the message back in its own input box (`input_cleared: true`). A Claude chat that Orca rewinds goes on in a fork of its Claude session: the
    bridge reads the chat's provider session again at once (and at most every 10 seconds while its row changes), so the row names the new transcript and keeps
    its id.
  - Seen is `agentSession.acknowledgeAttention {sessionId, observedCursor}` up to the journal's end (`agent-session.attention-ack.v1`; nothing on an Orca
    without it), so a chat read in Persona is not unread in Orca.
  - A closed chat (above) takes none of these until a message opens it again.
- **Starting a chat** (#17047, `orcabridge_start.go`; `caps.start`):
  - Launchers: one per provider Orca runs as a chat (its structured agents; on Orca 1.4.277 the ones `agentSession.agents` names) and found installed
    (`preflight.installedAgents`; both read at most once a minute), on an Orca whose `agent.launch` takes the chat surface (`agent.launch.surface.v1`):
    `orca-<provider>` (`orca-claude`, `orca-codex`…), display name "Orca chat · <provider>", pushed with `runtime.launchers` as they change. A provider
    other than Claude Code lists the permission modes a start takes (Orca's table, plan aside: the bridge promises no read-only start, `caps.readonly`,
    so the node refuses a plan start), and each lists the models its provider's open chats name in `agentSession.options` (with the efforts among
    Persona's levels). None while Orca is not running or cannot start chats, and none on a computer whose Orca host is off (the bridge does not run
    there). Every open chat's row names its provider's launcher (`launcher`), so the apps' model picker offers Orca's models; a chat Persona started says
    `started_by_persona`.
  - Only where Orca can open the chat (#17684): a provider whose chat runs on a runtime Orca installs itself (Claude Code's, Codex's and most others on
    1.4.277, `agentSession.agents`' `managedRuntime`) opens as a chat only once Orca has that runtime, or finds the provider's own CLI where it launches
    chats; installed agents alone don't say so. Orca tells whether it can open one only to streaming clients (`agentSession.subscribeAvailability`'s
    `chatProgram: available | installable | missing`, `managedRuntime.subscribe`), which the local socket refuses (`method_not_supported`, measured;
    `managedRuntime` is the same before and after the install). So the bridge learns it from Orca's refusal: `agent.launch` answering
    `agent_launch_surface_unavailable` because the chat runtime isn't installed ("…its chat runtime isn't installed on this host (install it from a new
    chat tab in Orca)…") refuses the start as `orca_chat_runtime_missing`, naming the provider, nothing started, and withdraws that provider's launcher at
    once, so New Agent no longer offers it and "Start new Agents as Orca chats" sends its default starts to a terminal. It is offered again when Orca
    shows a chat of that provider it did not have open then, or one of those changes (the person installed the runtime from a new chat tab in Orca and
    used it), and every launcher once Orca stops running (it may come back updated or with the runtime) or Persona Link restarts. Orca refusing the chat
    surface for another reason (a project under WSL, a host that cannot run one there) is `orca_chat_unavailable`, Orca's words as the detail, and keeps
    the launcher: that may hold for one folder only. `agentSession.create` is no way around it: Orca takes it on such a computer, and the chat then
    fails at once ("Claude wasn't found on the computer running this chat").
  - `start({cwd, prompt, client_msg_id, launcher, model, permission_mode, effort, session_id})`: the chat goes into the deepest local Orca worktree holding
    `cwd`, else, for a folder in a Git repository Orca does not have (not the home folder, the root or a temporary folder), the repository added with
    `repo.add` as a terminal start adds it; a folder no worktree holds is refused (`orca_start_folder`), never started in an unrelated workspace. The chat
    starts at its worktree's root (Orca's chat surface takes no start directory). It is `agent.launch {agent, operationId, target: {kind: "existing",
    worktree}, prompt: {text, delivery: "submit"}, chatOptions: {permissionMode, model, effort}, sessionId, surface: "chat", presentation: "background"}`,
    as Orca's own New chat, so Orca's window keeps what it shows; an Orca whose launch has no chat surface gets `agentSession.create` (envelope with no
    fence, `firstMessage`, `options`); one with neither is refused (`orca_start_unsupported`), and one that won't open that provider as a chat
    as above (`orca_chat_runtime_missing`, `orca_chat_unavailable`). Pictures reach it as for any runtime start: the node saves
    them and names their files in the prompt.
  - Never doubled: the Orca session id is `persona-<the id the hub named>` (the book maps it back, above). A start asked for again finds the chat it made
    (the row it answered with stands until Orca lists the chat, at most a minute, else Orca's `agentSession.history` knows the id) and launches nothing.
  - The per-computer default "Start new Agents as Orca chats" (#17650, off by default) routes a person's New Agent start that keeps the default plain
    Claude Code or Codex to `orca-claude` / `orca-codex`. It is decided in the manager (`assistant/orca_chat_default.go`, `agents.start_params`), which
    rewrites the start's launcher as an explicit pick would name it, so the node and the bridge need nothing new; the rules and what is never routed are in
    `docs/integrations/engines.md` ("Orca chats as New Agent's default"). The assistant's task Agents stay in terminals either way: an Orca chat carries
    none of Persona's launcher, hooks or MCP config.
- **Cards**: when blocked, read `agentSession.currentRequests`, mapping pending approvals to permission cards (if `detail` is tool-input JSON,
  show commands/files like hook cards), plan approvals to plan cards, and questions to question cards. Return answers with Orca option IDs and `expectedRevision`.
  Cards disappear from the phone if answered on the computer first. Every answer of a question goes: Orca 1.4.277 takes at most four in one reply and refuses more, which
  then counts as an answer not taken. An answer Orca did not take (refused, or its outcome unknown) is never dropped: while Orca still waits on the prompt it goes on the phone
  again as a new card (its own id), a permission card with Orca's words on why in its `reason`.
- **Errors**: the bridge's refusals keep their codes (`runtime_away`, `refused` and `session_gone` with `delivered: none`, `unknown` with `delivered: unknown`) and carry a
  `reason` (`orca_away`, `orca_refused`, `orca_unknown`, `orca_session_gone`, `orca_closed`) the hub words in the request's language (`rpc-errors.<reason>`), Orca's own
  words as the detail.
- **Write envelope**: every write carries `envelope {sessionId, clientOperationId, expectedRuntimeFence, payloadFingerprint}`.
  `clientOperationId` is `<13-digit-milliseconds>-<32-hex-digits>`; the same message (same `client_msg_id`) reuses it, so Orca returns the original result on resend, never sends twice.
  `payloadFingerprint` is SHA-256 of canonical JSON `{method, sessionId, fields}` (sorted keys, no spaces, escaping as JavaScript `JSON.stringify`).
  Approvals and questions use `agentSession.respondTo:approval` / `respondTo:question`. `expectedRuntimeFence` comes from `page.fence` in `history`
  (no longer compared since 1.4.246, but must be a positive integer). `refusal` with `ok:false` means nothing was done; `agent_session_operation_unknown` means completion is unknown.
- **Socket has request/response methods only**: `agentSession.subscribe*` are streaming methods available only on paired WebSocket, so this uses polling.
- **Live checks** (2026-11-05, Orca 1.4.246, newly created test sessions closed afterward): `setOption`, `send` (same operation ID resend does not send twice),
  Bash approval card content and deny (file not created), answering an already answered approval, `cancel` (working → stopped), and `close` all matched the behavior above.

## Registered bot endpoints (#12610)

The bot list shows only explicit manager-owned registrations in **Agents → Bots**. It does not infer bots from structured sessions, create a second transcript store, or add a `/api/v1/bots` chat API. Registration is a `bots.endpoints` entry in the manager's 0600 `manager.json` (`docs/operations/config.md`, "manager.json"); there is deliberately no browser link/add form in this phase. A registration has a stable public endpoint ID and title plus an exact machine and Orca session binding. Its public session ID is `bot_<endpoint id>:endpoint`, never Orca's structured-session identity.

Each registration is an independent `agents.Source`. The manager resolves the bound computer and uses `Machine.call`; the node alone calls Orca through `orcart.Client`. The internal `agentbot.describe`, `agentbot.history` and `agentbot.send` RPCs require an exact, unambiguous local structured-session binding. Missing, stale or duplicate bindings fail closed rather than falling back to a default bot or another session. Existing `/api/v1/agents...` routes, `agents.read` / `agents.control`, full / answer device rules, outbox, invalidation and polling are unchanged.

History comes from `agentSession.history`: stable `itemId`, `sequence`, `observedAt` and message `role` / `blocks`, with `page.window.nextCursor {epoch, sequence}` encoded as the existing API's opaque older-page cursor. `rankTimeMs` is the bound row's update time or the newest history message, whichever is later. Assistant replies are read from this history, never synthesized from a send receipt; acceptance does not mean completion. Text and known tool blocks are projected without forwarding unknown raw block payloads.

For writes, the manager persists only idempotency metadata (endpoint binding, SHA-256 of the exact text, operation ID and creation time), not message text or a transcript. It does so before calling the node, and passes that operation ID as Orca's `clientOperationId`; `agentSession.send` computes the exact payload fingerprint. The same client ID and payload therefore replay after a manager/node restart, while a changed payload or binding conflicts before another write. `retryUnknown` is never set and unknown outcomes never auto-repost. A retry is always the user's explicit retry with the same client ID. The book keeps the newest 2,000 operations (four times the outbox's 500), so a client ID outlives its outbox row; only a retry after that many newer sends would get a fresh operation ID.

Registering an endpoint makes it reachable by everything that already holds personal-Agent access: full devices, and extensions granted `agents.read` (history, including tool inputs and results) or `agents.control` (sending). An Orca session is usually a coding agent with tools on that computer, so register only a session you would let those extensions instruct.

## Bots found on a computer (#16891)

Phase 2 finds Orca's bots by itself, so no `bots.endpoints` entry or copied session ID is needed, and follows a bot to each new Home Chat. Everything goes through each computer's local Orca runtime socket (`orcart`); nothing goes through Orca Cloud. Learned from Orca 1.4.274.

- **Discovery.** A node that announces `orca_bots_v1` (`nodeproto.OrcaBots`) answers `agentbot.roster`. It reads `bots.roster` (`withoutChats`, this computer's bots only), then each bot's `bots.chats` and its open items (`bots.items.list`), and finds the bot's home folder from the worktree its Home Chat runs in (`worktree.ps`). The answer is `proto.OrcaBotRoster`. An Orca that is not running answers `running: false` with no bots, and one without bots (older, or Bots switched off) answers no bots, never an error card. Every 30 seconds the manager asks each connected computer that announces the capability (`extmgr/orcabots.go`), and keeps one Agents → Bots source per bot. Its registration (`agents.DiscoveredBot`) is bound to the bot, not to a session, with a public id derived from the computer and Orca's bot id (`bot_orca-<hash>:endpoint`). A computer that does not answer keeps its bots; an Orca that is not running, or a bot that is gone, takes them away. `bots.discovery: "off"` in `manager.json` finds none.
- **Hand-written endpoints keep working.** An entry may now bind `bot_id` instead of `session_id` (exactly one of the two), and is then followed to new Home Chats too. A bot that an entry already registers, by its bot or by its current Home Chat's session, gets no second source.
- **Following Home Chat resets.** `agentbot.describe`, `agentbot.history` and `agentbot.send` take `bot_id`. The node resolves the bot's open Home Chat (`bots.chats`; a closed earlier Home Chat is never it) on every call, and a page names the session it was read from. A send stays pinned. The manager resolves the chat, fixes the operation for that exact session in the operation book (`BotSource.binding`), and sends `bot_id` together with that `session_id`. If the bot has started a new Home Chat in between, the node answers `conflict` and writes nothing. The same clientId then conflicts in the book too, so a retry never follows the bot into a chat the user has not seen. A new message goes to the new chat.
- **Items as cards.** While the machine's engine is Orca, the bridge reads the bots every 20 seconds, off its session refresh (`node/orcabots.go`). An item with status `needs_you` and an open question becomes one question card (`orcaitem_<hash>`, from the item and the question's event). The card lives on the Persona session of the chat the item was raised in, or on the bot's open Home Chat when that chat is not listed or is listed closed (an earlier Home Chat the bridge lists from its book, "Structured sessions" above). Its questions are the ask's decisions, with their quick replies, the recommended one and multi-select. The item's title and the ask's context (Markdown, the bot's words) sit above the first question. The card is taken down when the item no longer waits on that question. A card answered on the phone goes back as the owner's comment, `bots.items.owner.comment` with `questionId` and the card's id as `requestId`, in Orca's own format: a label, labels joined by ", ", own words, or for several decisions one line each, `<question line> → <answer>`. Orca then wakes the bot. A card handed back, or left until the hook timeout, is not offered again for that question; Orca's own panel still has it. An answer Orca did not take (it refused it, or it is not known whether it took it) is never dropped, as with Orca's own prompts: while the item still waits on that question, the next read puts it on the phone again as a card of its own (the hash then includes the attempt). The card is a session question card like any other: it counts once in that session's Needs you, and the same delegation settings apply to it.
- **Automatic runs fold.** Sessions a bot runs on its own in its home folder or below it fold under its open Home Chat as side sessions (`parent_agent_id`, `linkOrcaBotRuns` in the node's session list). These are Look around, Tidy-up and its earlier Home Chats. The bot's other open chats, subagents, sessions that already name a parent, and sessions anywhere else are left alone, as is everything while the Home Chat is not listed. A Home Chat a reset has just opened is listed from Orca's tabs before its first turn, and the fold moves to it at once. Earlier Home Chats, which the bridge lists closed, fold under it like the runs. Workers the bot dispatches run in other folders and stay ordinary sessions.
- **Assistant tools.** `orca_bot_list`, `orca_bot_read` and `orca_bot_send` (`extmgr/orcabots.go`, descriptions in `server/internal/prompts/mcp/orca/`) are listed to the assistant's conversations only after a bot was found. They read the roster, items and recent Home Chat words live through the same node calls. A send goes through the bot's source and its operation book; accepted is not answered, and the reply is read back later. Both reads change nothing.

Discovered bots are reachable through the same `/api/v1/agents…` routes, scopes and device rules as registered endpoints ("Registered bot endpoints" above): full devices, and extensions granted `agents.read` or `agents.control`. A bot usually runs with full access on its computer, so switch discovery off where those extensions should not reach one.
