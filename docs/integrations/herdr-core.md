# Herdr App (herdr-core): how the node drives it

Audience: maintainers and contributors working on the Herdr App engine. What a user sees is in [engines.md](engines.md);
this page is how the node drives herdr-core, the runtime behind the Herdr desktop app: why it is a host of its own, the
connection, the mapping, the findings, the layout document and the cautions. Code: client `server/internal/herdrcore`, host
`server/internal/node/hostherdrcore.go` (starting and renaming: `hostherdrcore_launch.go`).

## herdr-core host

herdr-core (command `hc`) is the terminal runtime behind the Herdr desktop app. It has the same author as herdr, but is a rewrite and incompatible: different socket, `terminal.*` methods,
and environment variables. It is therefore a separate host, kind `herdr-core`, storing panes as `herdr-core:<address>` (such as `herdr-core:t7`).
Checked against herdr-core 0.1.16 source and `hc schema`; starting and renaming (#2532) against 0.1.19's `hc schema` and the app's own terminals.

### One engine per machine

Each machine used to select one "engine" (a set of hosts): herdr (default, one host), herdr-core, orca ([orca.md](orca.md)), and
`herdr-both`, added 2026-11-03 (herdr + herdr-core side by side, "Combined engine" below). Since #2576 a machine enables any set of hosts with a
default one ([engines.md](engines.md), "Host sets"), on the per-host isolation the combined engine brought. Users choose per machine in the app
(during pairing, **Settings › Machines**). hub persists the choice and sends `engine.use` when node connects (API/storage in
`docs/protocol/protocol.md`, "Engines"; node code `server/internal/node/engine.go`). What follows is how the single engines came about.

- **Why not combine by default**: initially both hosts registered together. Review found this slowed herdr: slow herdr-core delayed herdr refresh/send too (up to about 4 seconds);
  if herdr died while herdr-core remained, stale herdr Agents stayed online; herdr-core events triggered unnecessary herdr snapshots; TRAE CLI in herdr-core
  forced herdr to poll every second. Selecting only one registers only that host and avoids all four. The combined engine is explicit and addresses them individually with parallelism and per-host isolation.
- **herdr selected**: node's host table, refresh, polling, and event stream remain exactly as before; no herdr-core client or subscription. The sole exception is `node.info`,
  which checks herdr-core availability once to fill `engines`.
- **On connection**: for a machine with a saved choice, hub's first call is `engine.use`. node waits for the engine to be settled (`engine.use`, hub listing sessions directly, or at most 3 seconds)
  before polling/subscribing, so herdr-core machines never briefly flash herdr sessions. Machines using herdr (no saved choice) receive no `engine.use`.
  With herdr-core selected, `node.info` does not query herdr either; the herdr version comes from `herdr --version`.
- **Runtime switching**: `engine.use` replaces the host table, discards old Agents, space names, and process caches (invalidating refreshes begun before the switch), ends every terminal stream
  (pushes `terminal.end`), stops old event streams and starts new ones, then pushes the session list. Each event stream has its own live marker; stopped old streams cannot clear new-stream state.
- **Operations without a host**: operations not deriving a host from a pane, such as creating sessions or renaming spaces, use `defaultHost` / `defaultHostKind`: herdr-core when selected,
  returning `unsupported` for unavailable capabilities (worktrees), not "No herdr on this machine". Combined mode always defaults to classic herdr. herdr-selected machines are unchanged.
- **Nested herdr**: with herdr-core selected, a terminal whose foreground is classic herdr is not an Agent, even if inner claude OSC 7501 escapes through.
- **Nested ptys** (#5463): processes in a pty a program in the pane made (tmux, zellij, screen, `script`, a node-pty) inherit its `ORCA_PANE_KEY` and are each "in front" of that pty while the pane itself may show a shell. They are not the pane's: the scan walks up from each candidate to the first ancestor on another terminal (the pty's owner) and drops the candidate when that owner carries the same pane key; in an Orca pane the owner is Orca's terminal host, which has none (seen on a Mac 2026-11-06: `claude → bash → bash → login → orca-terminal-host`, versus `claude → bash → bash → tmux server` with the key). No owner found: not taken. Not by `TMUX=`: Orca's own Claude agent teams set it in a real pane. Without this, an agent's `tmux new-session -d` workers running `claude -p` took the pane from the claude that started them, which was then listed as interrupted.
  Combined mode registers such terminals under herdr-core with no Agent; the inner claude belongs to a herdr pane and is not counted twice.

### Combined engine (herdr-both, 2026-11-03)

Reason: Herdr App can connect remotely to another machine's terminals (`herdr remote-client-bridge`), so classic herdr (local terminals) and
herdr-core (Herdr App remote access) can both run on one machine. Selecting either hides the other's sessions. `herdr-both` registers both,
isolating the four issues in "Why not combine by default":

- **Refreshes do not wait for each other**: `refreshHosts` changes from sequential to per-host parallel, with each call bounded by its client timeout (herdr-core list: 2 seconds).
  One stuck host does not block the other; partial refresh replaces only that host's Agent cache, retaining the other's last result.
- **Events do not trigger each other**: each watcher host has its own `watcherSignals` (live marker + kick channel, `herdrevents.go`). The polling loop selects all kicks,
  adding only the emitting host to that round's due list; hosts without events retain their own slow-snapshot cadence (10 seconds). herdr-core events no longer trigger herdr snapshots.
- **Online state is per-host**: Agent cache is `map[hostKind][]Agent`; a host returning `not_running` clears only its own group.
- **TRAE CLI does not slow the other host**: `hostHasTraex(kind)` counts its sessions only in the given host's panes and decides whether that host needs a snapshot every second.
- **Default host is herdr**: create / continue / rename use herdr; pane actions still route by PaneRef kind. herdr-core pane restrictions (no color, no text-only typing,
  no live terminal) are exactly the same as when herdr-core alone is selected.
- Orca was not part of herdr-both (no event stream, one-second polling, the heaviest model). Since #2576 it sits in a host set beside any other
  host, kept apart by the same per-host refresh, events and online state ([engines.md](engines.md), "Host sets").

### Connection

- Socket location follows `hc` rules: `$HC_SOCKET`, otherwise top-level `socket` in `$XDG_CONFIG_HOME/herdr-core/config.toml`, otherwise `<session>.sock` in the runtime directory
  (`$XDG_RUNTIME_DIR/herdr-core`, or `/run/user/<uid>/herdr-core` if it exists), otherwise state directory
  `$XDG_STATE_HOME/herdr-core/<session>/<session>.sock` (usually `~/.local/state/herdr-core/default/default.sock` on macOS). Session name is `$HC_SESSION`, default `default`.
  On Windows herdr-core has no socket file: it serves the named pipe `\\.\pipe\herdr-core-<user>-<session>.sock` (`%USERNAME%`, then the session; the holder beside it is `…-<session>.holder.<id>.sock`), which the node checks and dials instead (#11189); `$HC_SOCKET` and `config.toml` are not read there unless `$HC_SOCKET` is already a pipe name.
- Protocol: JSON-RPC 2.0, one message per line. Ordinary calls open a fresh connection; `terminal.list` and `doc.get` have 2-second limits (every refresh), sending 20 seconds
  (`terminal.send` waits for 3 seconds without typing on the computer), keys 8 seconds, others 5 seconds. Errors carry stable `data.kind` (`not_found`, `blocked`, etc.).
- `events.subscribe` does not use framed mode: after its response, the ordinary connection sends only notifications (`event`, `heartbeat` every 15 seconds, `missed` plus a new snapshot when behind).
  No message for 45 seconds means disconnected; reconnect with backoff. Only `terminal.attach` needs `hello {framed:true}`.
- Only a missing socket file or refused connection (no listener) means not running (`host.ErrNotRunning`): immediately answer "No terminal", clear sessions, no logs.
  Connection timeouts and similar failures mean it is still there but busy; retain the last sessions. If previously connected, log one line at the start of each outage and one on recovery.
  Opening Herdr later requires no node restart.

### Mapping

| Persona requirement | herdr-core implementation | Status |
| --- | --- | --- |
| List panes | `terminal.list`: local terminals only (address has no `/`) with `state: live` | ✅ |
| Agent type | `status.app` (program's own OSC 7501 value, unchanged): `claude-code` → claude, `codex` → codex, others are not Agents (left to internal-variant detection). If no status yet, check foreground `argv[0]` for `claude` / `codex`. Foreground shell, classic herdr, or no foreground process is not an Agent: herdr-core retains an exited Agent's last status until the terminal's own program ends | ✅ |
| State | `status.state`: working / blocked / done / idle unchanged; `error` and claude without status → unknown (error says an error occurred, not what it is doing now) | ✅ |
| Map pane to session | Foreground `foreground.pid` is claude's own pid; read `sessions/<pid>.json`; herdr-core reports no session ID | ✅ |
| Spaces / slots | Herdr-created terminals have labels `herdr.space`, `herdr.tab` (space/tab IDs in layout document `doc.get {name:"herdr-app"}`), and `where` (space / tab display name). Use document names, then `where`, then directory basename / index; indices follow document order. Non-Herdr terminals (`hc run`) group by directory into space `dir:<directory>`, with empty slot | ✅ |
| Read screen | `terminal.read {terminal, lines:0}`: plain text only, soft wraps already joined, current screen only. History reads make herdr-core send wheel events to full-screen programs and withhold user keys, so `recent` also returns only the current screen, without scrolling back | ✅ Plain text, no history |
| Send message | First `terminal.get` verifies an Agent remains (reject after exit so the shell cannot execute the message as a command), then `terminal.send {terminal, text}`: paste then Enter (bracketed paste if enabled), waiting for 3 seconds without computer typing. Rejection delivers no input: `blocked` → `dialog_open`; `status_mismatch` / `unavailable` / `finished` / `not_found` → `not_delivered` (all `delivered:none`, safe to resend) | ✅ |
| Special keys | `terminal.keys`: names match Persona (`enter`, `esc`, `shift+tab`, `ctrl+u`, `up`, etc.), forwarded unchanged | ✅ |
| Type without Enter | Not possible; see below. "Text + Enter" and `/effort` use `terminal.send`; other text input (including dialog options requiring keys to open an input field first) rejects before any keys (`cannot_type`, Chinese explanation) | Partial |
| Recognize hook pane | All terminal processes have `HC_TERMINAL_ID` (the address); without `HERDR_PANE_ID`, `personad hook` reports `herdr-core:<address>` | ✅ |
| Push changes | One `events.subscribe` connection covers all terminals; any change (except size, keep, and other-machine changes) immediately requests a snapshot | ✅ |
| Live terminal | Not implemented; see below. hub falls back to screen reads | ❌ |
| Close | Like herdr, send ctrl+c twice, then `terminal.kill` (hangup → terminate → force kill, returning terminal state after at most about 3 seconds). Herdr App also closes panes with only `terminal.kill`; when the terminal ends as `killed`, the app removes the pane (or tab if last), without `terminal.rm`. We likewise only kill, retaining the ended record. Ended or absent (`not_found`) counts as closed; still `live` after kill is an error | ✅ |
| Rename | Space and tab names live in Herdr App's layout document: `RenameSpace` / `RenameSlot` set `name` there (see "Layout document"). A directory group (`dir:`) has no name to change (`unsupported`). Session titles (`/rename`) work normally | ✅ (#2532) |
| Create | `Launcher` (#2532): `terminal.run` with no argv (the machine's shell, as the app opens one), `keep`, `no_parent`, the app's terminal identity (`TERM=xterm-256color`, `COLORTERM=truecolor`, `TERM_PROGRAM=ghostty`) and labels (`herdr.space`, `herdr.tab`, `where`, `herdr.seen: 0`), then a tab added to the layout document; the launcher goes in through the start file, as on herdr (`terminal.send` pastes into the same canonical tty). `Run` refuses unless a shell is in front. `NameAgent` does nothing (a terminal's name is set only as it starts) | ✅ |
| Takeover | No `Controller` (see "Live terminal" below) | ❌ |

### Findings

- **Are addresses reused?** No. Addresses use an ever-increasing counter (`t1`…`t9`, `t10`, `t1b`, etc.), never recycled when terminals are deleted, persisted in the state directory across restarts.
  Only clearing the state directory (or detecting a cloned disk) restarts from `t1`. Pane IDs therefore use the address directly, without `created_ms`: clearing state is a rare manual operation,
  and session recognition checks `sessions/<pid>.json` first, using old bindings only when process identification fails.
- **Can it read colors?** No. `terminal.read` is plain text; only the first `terminal.attach` frame has color. The host declares `host.PlainScreen`:
  colored reads return `unsupported`; when the app requests color, node sends plain text instead (line endings converted to `\r\n`). Claude pre-send checks only dialogs
  (`screen.Choosing`), not drafts, since draft text cannot be distinguished from gray hints without color. herdr's path remains unchanged (it does not declare `PlainScreen`).
  Extra Enter (`ensureSubmitted`) and suggested replies (`suggestion.go`) are skipped without color, and unnecessary: `terminal.send` adds Enter itself.
- **Can it paste without Enter?** No. `terminal.send` always adds Enter; `terminal.keys` accepts key names only (single letters/digits and some punctuation, not arbitrary text).
  Raw input exists only in the framed `terminal.attach` stream, and attach without `observe` resizes the terminal to our dimensions. `SendText` therefore returns `unsupported`,
  and the host declares `host.TextTyper` with `CanTypeText: false`. node sends "text + Enter" and `/effort` as one `terminal.send`; other key requests requiring text
  reject before any keys (including free-form dialog options, which previously opened the input first and failed when typing, leaving a half-open field).
- **What format is the stream?** `terminal.attach {observe:true}` first sends a complete redraw (`0x18` followed by VT sequences), then raw program PTY output, not delta frames;
  it redraws only when backlog exceeds 4 MB. node's current frames are a subset of herdr's grid redraws, and the app's custom VT subset may not handle arbitrary raw program output,
  so phase 1 has no `Streamer`; hub falls back to screen reads.
- **Can keep terminals close?** Yes, but ctrl+c alone is insufficient. Herdr App Agent terminals run `sh -ic '"$@"; exec "$0" -i' sh <agent>` with `keep`:
  the shell continues after Agent exit and the terminal remains `live`. `keep` starts a replacement shell only if a program is "lost" (killed by signal, holder gone, etc.); normal exit with an exit code and
  `terminal.kill` are final (herdr-core `terminals.rs` tests `keep_preserves_losses…`, `terminal.keep` documentation). Always follow two ctrl+c sends
  with `terminal.kill`. Tested on an owned instance: after ctrl+c terminal stayed `live` with a foreground shell; after kill it was `finished`,
  with no revival after the keep grace period (`TestHerdrCoreLiveCloseKeep`).

### Layout document

Herdr App keeps its spaces and tabs in the core's `herdr-app` document (`doc.get` / `doc.set`, checked 2026-11-09 on 0.1.19):
`{spaces: [{id, name, folder, tabs: [{id, name, root}]}]}`, where `root` is a tree of `row` / `column` nodes (`children`, a `size.weight`
per child) whose `block` leaves hold `content: {terminal: "t7"}`. Ids are a kind prefix and 32 hex digits (`sp…`, `tb…`, `p…`, `s…`); a tab's
root shares the tab's id. A space for a folder has `folder` set and an empty `name` (the app shows the folder's name); a named space may
have no folder.

- **Edited, never replaced**: decoded as generic JSON (numbers kept as written), only a space, a tab or a `name` is added or changed, so
  whatever the app keeps that Persona does not know is written back as it was. `doc.set` writes at the version read; `conflict` (the app wrote
  meanwhile) reads it again and redoes the change, at most 4 times.
- **Only a known form is written**: no document (the app never ran on this core), `spaces` not a list, a space or tab without an id or `tabs`
  / `root` of another type: nothing is written, and an automatic or directory-group start opens its terminal unplaced (its directory's `dir:`
  group, like `hc run`). An explicit app-space target is refused before opening a terminal if that layout cannot resolve it.
- **Folder identity**: automatic starts match the space's `folder` to the cleaned directory path, not its mutable display name. Each conflict
  retry repeats that match so concurrent first starts reuse the same space; `terminal.label` keeps the terminal's space and `where` labels
  consistent with the selected space. Explicit targets keep their space id even if another space matches the directory.
- **Order**: the terminal starts first (its address goes in the block), then the document; a document the core refuses ends that terminal
  rather than leave it running where the app does not show it. Cleanup errors, including a terminal still live after kill, accompany the
  placement error. A space that disappeared meanwhile refuses the start (`not delivered`).
- **The app removes closed tabs itself**: `Close` stays `terminal.kill`; Persona does not delete blocks.

### Cautions

- herdr-core has no hooks; status comes only from the program's own OSC 7501 (Claude Code sends it). Programs that do not emit it have no status.
- herdr-core does not discard `terminal.send` / `terminal.keys` abandoned by the caller: they wait for 3 seconds without computer typing and may arrive after Persona times out.
  Key timeout was shortened to 8 seconds to reduce this window; dialog answers do not recheck revision just before delivery.
- Known minor differences (not addressed yet): our parser and `hc` may differ on malformed `config.toml`; a snapshot can take about 4 seconds (`terminal.list` + `doc.get`,
  affecting only herdr-core-selected machines); pane IDs omit core identity, so clearing state restarts addresses at `t1` (process-first recognition limits impact).
- `terminal.list` also lists other machines connected to this core (addresses contain `/`); they have their own node, so skip them here.
- Live checks use only owned instances: point `HC_SOCKET`, `HC_SESSION`, `XDG_STATE_HOME`, and `XDG_CONFIG_HOME` under `/tmp`, then `hc server stop` afterward.
  On the default instance, use read-only calls only (`terminal.list`, `terminal.get`, `doc.get`); terminal-ending actions such as close are tested only on owned instances. See `server/internal/node/hostherdrcore_live_test.go` (`-tags hclive`).
