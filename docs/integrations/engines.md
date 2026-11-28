# Engines: herdr, Herdr App, Orca, Tern, Rex, Paseo, Cindy, T3 Code

**One engine per computer**: Persona sees and drives Claude Code / Codex through an engine. Each computer selects one; Agents shows live sessions only from it.
Sessions elsewhere (other engines, ordinary terminals) appear only as "Not running" history. Default herdr preserves existing machines without an explicit choice.

Exception: **"herdr + Herdr App (side by side)"**. Choose this when both classic herdr and Herdr App operate on one computer (typically local terminal herdr,
plus Herdr App on another computer opening terminals through remote). Sessions merge; boundaries are in "Side by side" below.

On computers with **botmux**, its sessions appear alongside the selected engine ("botmux" below). It is not an engine option and needs no selection.

**Paseo**, **Cindy** and **T3 Code** are terminal-free: they run Agents through provider SDKs and runtimes, with node connected through a bridge; see `docs/integrations/paseo.md`, `server/internal/node/cindybridge.go` and "T3 Code" below.

## Host sets (#2576)

A computer can **enable several engines at once**, any combination of herdr, Herdr App, Orca, Tern, Rex, Paseo, Cindy and T3 Code, or none at all, plus a **default engine**:
where new sessions started from the phone go. A retired session resumes in its previous terminal host while that host remains enabled;
an explicit host selection overrides it. Other offline sessions use the default host. Each single choice above is a special case of a set: herdr is `{herdr}`, and
"herdr + Herdr App" is `{herdr, herdr-core}` with herdr the default. API and storage: `docs/protocol/protocol.md` "Engines" ("Host sets").
The web is wired: the machine page's Engine section is a checklist with "Make default", New Agent has "Open in", and a session live in
two hosts shows "Also open in …" above the composer. iOS and Android still pick one engine, and the single engine picked keeps working;
iOS and Android also offer "No Terminal Engine", the empty set, last in Choose Engine when the hub reports `engine_hosts` (#3302).
A phone's pick never shrinks a set it can't show (#17052): on a computer with several engines set up on the web, picking one already in the
set only makes it the default (the hub keeps the others even for an older app's `{"engine"}`, `docs/protocol/protocol.md` "Engines").
Android names the whole set on the machine page ("herdr + Orca + Paseo"), says in Choose Engine which engines it holds, which is the default
and that the web edits the set, sends a pick inside it as the set with a new default, and enables Save only once the choice changes
(the pairing step still confirms the engine shown). iOS follows in #17056.

- **Only enabled hosts are registered.** An engine that is not checked gets no client, no polling and no event subscription; it is only probed once in
  `node.info` (for "Detected"). An engine that is not installed or not running does not slow the others down.
- **Hosts do not hold each other up.** Each host refreshes in parallel with its own timeout, and its events refresh only itself. A host that is down
  (unreachable, `not_running`) clears only its own sessions; the others are listed and driven as usual. This is the same mechanism as herdr-both
  (`docs/integrations/herdr-core.md` "Combined engine").
- **Default engine**: new sessions, offline sessions without a remembered terminal host and the mode menus go to the default engine; renaming a space goes to the
  enabled host that lists it, the default one first (`spaceOrganizer` in `node/rename.go`: space ids carry no host kind). Per-pane
  operations (reading the screen, sending, keys, closing) still go to the host the pane belongs to. Changing only the default resets nothing; changing
  the set itself works like switching engines did (live terminal views end, the session list is rebuilt). A session without a remembered terminal host
  continues in the default engine: Orca continues Claude Code, Codex, TRAE CLI and pi sessions as herdr does and refuses an omp one
  (`orca_resume`, "Resuming in Orca" below); Paseo takes over sessions it can resume.
  Retirement checks the session's own host under any default: it stops the agent and keeps its terminal,
  unless the user requests closing it. Runtime-owned sessions remain protected.
- **The empty set**: no terminal host. Agents shows transcripts, runtimes (DSH, pi, opencode) and botmux sessions only; starting or continuing a
  session from the phone is refused (`engine_missing`, reason `none`, "Refusals in the reader's language" below) and nothing is sent. This is the choice for a computer
  whose sessions are only the desktop apps' (Claude's and Codex's, `docs/integrations/claude-integration.md`, `docs/integrations/codex.md`):
  they stay listed, and the node probes no engine. New Agent with the launcher `claude-desktop` still starts a session there, in Claude's
  desktop app (`docs/integrations/claude-integration.md`, "New sessions in the desktop app"), and with `codex-app` a chat in the Codex App
  (`docs/integrations/codex.md`, "New chats in the Codex App").
- **One session live in two hosts** (say herdr `--resume`d a session that is still open in Orca): it is listed once and owned by one host
  (`node/ownership.go`). The owner is, in order: (1) the pane whose claude process runs it (`~/.claude/sessions/<pid>.json`); (2) the pane whose last
  SessionStart hook named it; (3) the default engine's pane; (4) otherwise herdr, Herdr App, Orca, Tern in that order. Messages, keys and interrupts go to
  the owner only, and its state is the only one used; the row's `also_open_in` lists the other hosts that have it open, so clients can warn of the
  conflict (the web does; the apps do not yet). Two panes of the same host, or a live pane and an offline row, behave as before.
- **Performance** (2026-11-06, one Mac with herdr 0.9.5 and Orca 1.4.250, `go test -tags enginehostslive -run EngineHostsLive ./internal/node/ -v`,
  40 rounds each, Orca refreshed once a second meanwhile as its watcher does; three runs, a typical one below, the other two of the same order):

  | | herdr only | herdr + Orca |
  | --- | --- | --- |
  | herdr send | p50 0.5 ms · p95 1.7 ms · max 2.5 ms | p50 0.5 ms · p95 1.1 ms · max 3.3 ms |
  | refresh after a herdr event (herdr only) | p50 82 ms · p95 205 ms · max 267 ms | p50 92 ms · p95 320 ms · max 430 ms |
  | refresh of every host | p50 62 ms · p95 125 ms · max 179 ms | p50 106 ms · p95 519 ms · max 1104 ms |

  In two of the three runs the herdr + Orca send had one max of about 170 ms (herdr alone had one of 149 ms in one run); p50 did not move.
  The refresh of every host is slower because it now includes Orca, the heaviest host. herdr's own refresh gains about 10 ms at p50 and about
  0.1 s at p95: the session merge after it (`setAgentsAt`: process lookups, session files) covers Orca's agents too.

  A herdr send waits for nothing of Orca's. Measured again for #5816 on fakes (2026-11-07,
  `PERSONA_BENCH_ENGINEHOSTS=1 go test -run EngineHostsLatencyOnFakes ./internal/node/ -v`: a fake herdr, a fake Orca whose process scan
  takes 170 ms as three `ps` runs do on a Mac, 400 rounds each), a herdr send is p95 0.1 ms with and without Orca. herdr itself answers
  about 0.1 s late now and then with nothing else asking it (`docs/lessons/agents.md`).

  The merge asks only the hosts that answered the refresh what their panes run; another host's pane keeps the pid its own refresh found.
  Only the owner of a session live in two hosts still asks both, so it does not change with the host that refreshed. When the merge asked
  every host, herdr's refresh ran Orca's process scan whenever an Orca pane was due for a look (p95 174 ms on the fakes, 1.6 ms now), and
  Orca's poll asked herdr about every herdr pane due for one.

"Engine" below means this choice. APIs, storage, and node switching: `docs/protocol/protocol.md` ("Engines"); design: "The host layer" below, `docs/integrations/herdr-core.md` and `docs/integrations/orca.md`.

## Engines

| Engine | App name | Where Claude runs | Detection |
| --- | --- | --- | --- |
| `herdr` | herdr | Run `herdr` in a terminal, then `claude` / `codex` in its panes (node starts it on demand if absent; see "When herdr is not running") | Connectable herdr socket or installed `herdr` command |
| `herdr-core` | Herdr App | Terminals opened in Herdr desktop app (herdr-core behind it) | Responsive herdr-core socket |
| `herdr-both` | herdr + Herdr App | Both above | Both runtimes detected |
| `orca` | Orca | Terminals and native chats (structured sessions: Claude Code, Codex and its other providers) in Orca desktop app, or its remote runtime launched here over SSH by Orca on another computer | Orca desktop / remote runtime running, with a Persona-compatible version |
| `tern` | Tern | Terminal blocks in Tern's sessions, in any of its windows ("Tern" below) | `tern` found (PATH, `~/.local/bin`, the app bundle) and its session daemon answering `tern inspect` |
| `rex` | Rex | Terminal blocks in Rex's sessions and windows ("Rex" below) | `rex` found (PATH, `~/.local/bin`, macOS Application Support / app bundle) and `rex --autostart=false api call server.status` answering |
| `paseo` | Paseo | Paseo Agents (desktop app, `paseo` command; Claude Code, Codex, ACP all count) | Paseo daemon running; address in its home's `paseo.pid` responds |
| `cindy` | Cindy | Cindy Agents (desktop app and `cindy-host`; Claude Code, Codex, Pi) | Cindy installed, running, or its loopback host endpoint / profile database present |
| `t3code` | T3 Code | T3 Code's threads (desktop app, `t3` server; Codex, Claude Code and its other providers) | `t3` command line found, or a T3 server running on this computer ("T3 Code" below) |

herdr and Herdr App are from the same developer but separate: neither sees the other's sessions. If using only one, select it;
if both are used and should appear on the phone, choose "herdr + Herdr App (side by side)."

## Switching

1. **During pairing**: after QR / manual pairing, asks "Which engine does this computer use?" (the hub machine). Each option says "Available" or "Not detected."
   Select one; herdr is default (or previous choice), confirmation switches; no Skip.
2. **Anytime later**: **Settings › Machines › computer › Engine**, select another. Choices are per computer; one can use herdr, another Orca.
3. **Offline computers** can change too, taking effect when connected.

iOS / Android have both entry points (Android Machines is under Agents: **List settings › Machine monitoring › computer**, or tree computer-header dashboard button, then **Engine and connection › Engine**). Web selects directly in Machines (**sidebar Machines › computer**, Engine section, #2071). Choice is stored on hub, not phone, and matches across phones, web, and Android.

## After switching

- Agents immediately shows new-engine sessions; old-engine sessions become Not running, with history retained.
- Live terminal / phone takeover disconnects; reopen in a new-engine session.
- No running Claude in any engine is closed or modified.

## The host layer

Audience: contributors adding or changing an engine. Every terminal engine is a **host** behind one interface
(`server/internal/host/host.go`); herdr was moved behind it first, without changing its behaviour (2026-11-01), then Orca,
Herdr App, botmux, Tern, Rex and tmux's approvals came in as more hosts. How each one is driven: herdr-core in
[herdr-core.md](herdr-core.md), Orca in [orca.md](orca.md), Tern, Rex and T3 Code below.

### Two concepts

- **Host**: where the terminal runs, such as herdr or Orca. The host lists panes, reads screens, sends input, and opens and closes panes.
- **Agent**: what runs in the pane, such as Claude Code, Codex, or TRAE CLI. The Agent determines transcript location, session-ID recognition,
  state detection, and dialog appearance. "Codex in Orca" and "TRAE CLI in herdr" are combinations from the two tables, hosts and Agents.

### Host interface

`server/internal/host` holds the interfaces, `PaneRef` and the kind-keyed host `Registry`; each host's implementation is in
`server/internal/node/host<kind>*.go` (herdr: `hostherdr.go`, the only node file that talks to the herdr client). Every host implements the core,
`Host`: its kind and version, a snapshot of spaces, slots and panes, its agent panes and one pane's agent, a screen read (visible, recent,
recent-unwrapped; plain or ANSI), text, Persona-wide key names (each host translates them), a prompt submitted the host's way (`ErrNoAgent` when
no supported Agent is in front: the node then types the text and Enter itself), a pane's foreground processes, and the environment variable that
names a pane (`HERDR_PANE_ID`, `ORCA_PANE_KEY`…). Everything else is an optional capability, asked for with a Go interface assertion, so a host
that lacks one is refused as `unsupported` and the apps hide what it cannot do (`host.go` lists them: `Watcher` for pushed changes,
`Launcher` / `EnvLauncher` to start sessions, `Organizer` to rename and close, `Splitter` and `SlotCloser`, `Worktreer`, `ForegroundNamer`,
`PlainScreen`, `Scrollbacker`, `QuickScreen`, `TextTyper`, `Streamer` / `Controller` for the live terminal, and more).

- **Pane reference `PaneRef`**: host kind + host-local ID. Stored and transmitted as `<kind>:<id>`; herdr panes have no prefix (herdr IDs already
  contain colons, such as `w1:p2`). A prefix not in `host.Kinds` (the fixed known kinds, independent of local registrations) is treated as herdr. A
  known kind with no local host reports "no <kind> host on this machine" on lookup and never sends to herdr, so the hub's database,
  `proto.Session` and the hook's pane values needed no migration. The node always stores this string (`n.panes`, `Agent.PaneID`), and
  `Registry.Pane` resolves host and ID at call time.
- **Session model**: the node keeps `map[string]host.Agent` (`PaneID` is a `PaneRef` string); panes in a host's `Agent` and `Created` already use
  that host's `PaneRef`.
- **Key names**: the apps' herdr notation (`shift+tab`, `ctrl+c`, `enter`…) is Persona's; herdr forwards it unchanged, Orca converts it to bytes
  (`shift+tab` → `\x1b[Z`).
- **Errors**: hosts mark their errors with `ErrNoAgent`, `ErrNotDelivered`, `ErrDialogOpen`, `ErrAttachBusy`, `ErrTakenOver` and the like using
  `host.Mark` (tested with `errors.Is`), keeping the text. A missing capability is `ErrUnsupported` from the node, with text beginning
  `unsupported` (the hub falls back to screen reads on that prefix) and a code the hub words in the reader's language ("Refusals in the reader's
  language" below).
- **Listing**: a snapshot from each enabled host, in parallel with each host's own timeout; a host that does not answer keeps its last Agents, and
  only a total failure counts as one ("Host sets" above).

### Shared layer above hosts

| Shared layer | Responsibility |
| --- | --- |
| Session recognition | Prefer a session ID supplied by the host; otherwise scan claude / codex process environments, find the pane via each host's `PaneEnv()`, then read `sessions/<pid>.json` (Codex uses thread names, TRAE CLI uses an owner file). `personad hook` reads pane variables for every host |
| State detection | Use host-reported state when available; otherwise infer from the screen (as for TRAE CLI), keep corrections such as `codexTurnOver`, then let the hub map to unified states (`docs/protocol/agent-states.md`) |
| Pre-send checks | If the host reads colored screens: full draft / gray-text detection and extra Enter (`checkPane`, `ensureSubmitted`); plain text only (`host.PlainScreen`): dialogs, and drafts where the host hands back the input box (Orca) |
| Capability reporting | Each session carries what its host and runtime can do, and the terminal browser gets each host's `abilities` (#17053); the apps hide what is not offered |

Space ids carry no host kind, so a session's row names its host (`space_kind`), and a space rename goes to the enabled host that lists it
(`spaceOrganizer` in `node/rename.go`).

## Capabilities by engine

| Capability | herdr | Herdr App | Orca | Tern | Rex | Paseo |
| --- | --- | --- | --- | --- | --- | --- |
| List sessions, show state (Needs you / Working / Unread) | Yes | Yes | Yes (polls each second) | Yes (`tern events` pushes changes) | Yes (`rex events --all --json` pushes changes) | Yes (Paseo pushes changes) |
| Send messages (multiline as one message) | Yes | Yes | Yes | Yes | Yes (`block.event` paste + `enter`) | Yes (while working, injects into current turn) |
| Keys (Enter, Esc, arrows…), interrupt | Yes | Yes | Yes | Yes | Yes (`rex send-key`) | Interrupt only |
| Answer dialogs (permissions, options) | Yes | Yes (screen-based) | Yes (screen-based) | Yes (screen-based) | Yes (screen-based) | Yes (cards, also answerable in Paseo app) |
| Close sessions from phone | Yes | Yes | Yes | Yes | Yes (`block.close` / `session.close_window`) | Backend supports archiving in Paseo; app button currently only for terminal sessions |
| Create sessions from phone; Resume a stopped one from its terminal page (a message does not, #4754) | Yes | Yes (a new tab in the Herdr App space for the directory, #2532) | Yes (create; resume a stopped Claude Code, Codex, TRAE CLI or pi session in a new terminal, #17044; not omp) | Yes | Yes (`session.create` / `session.new_window` with `focus: false`) | Yes (each Paseo provider is a launcher; a message continues a Paseo session) |
| Create a worktree from a workspace ([terminal.md](terminal.md), "23. New worktree…") | Yes (herdr 0.9 creates it; older versions fall back to git) | Yes (Persona falls back to git and opens a Herdr App space) | Yes (Orca creates it under the original workspace and owns the checkout directory; on the web and Android, which also rename and delete one after a preview, #17054) | No | No (git fallback) | Yes on the web (Paseo creates it in its own worktrees folder as a Paseo workspace, #6295, and deletes one it made, #6297; `docs/integrations/paseo.md` ("Spaces and workspaces")) |
| Rename spaces / tabs | Yes | Yes (written into Herdr App's layout) | Yes (`worktree.set` display name; `terminal.rename` for tabs, the assistant's guarded tab rename and the bulk rename included, #17043; on the web also a native chat's space, #17053) | Yes | Yes (`session.set_label` / `session.set_window_label`) | Spaces (Paseo's projects) and workspace titles on the web, #6297; no tabs (backend supports session names; app button currently only for terminal sessions) |
| Split a pane, close a whole tab ([terminal.md](terminal.md), "21. Node menus") | Yes | No | Yes (`terminal.split`, `terminal.closeTab`; an Orca without them is refused and its `abilities` say so, #17043) | No | Yes | No (no terminal) |
| Live terminal, phone takeover | Yes | No | No | No | No | No (no terminal) |
| Colored screen reads, draft detection | Yes | No (plain text, dialogs only) | No colors; drafts seen (Orca 1.4.277 hands back the input box's text, which a plain read puts back in its row), and Claude's grey suggestion reads as text too (#17042) | Yes | Yes (`rex capture --format=vt`) | No (no terminal) |
| Change permission mode / thinking effort | Yes | Supported, untested | Yes (fake-runtime tests; checked on an isolated Orca 1.4.277 with Claude Code 2.1.331): the mode by shift+tab; effort and speed only while the input box shows no text, so also not while Claude's grey suggestion shows (#17042) | Supported, untested | Supported, untested | Yes (end-to-end tested) |

Windows: herdr has native Windows support since 0.9.5; except live terminal, capabilities match above. Orca works too. See [windows.md](../operations/windows.md), "herdr."

**Antigravity CLI panes** show chat history once the pane's conversation ID is known. Two sources report it, and either is
enough: Persona's own hooks (below) and the hook installed by `herdr integration install antigravity-cli`. On Linux the
node also reads it from the `presence/<conversation>.lock` the pane's foreground agy holds open, in herdr, Herdr App and
Orca panes (#16531), so a pane is known before its first hook fires and after a node restart; a pane whose agy processes
hold two conversations' locks stays unknown. Persona reads
that conversation's local `transcript.jsonl` from the node's Antigravity data directory, or `transcript_full.jsonl` when
the compact export is absent. Switching export sources resets the history cursor. A launcher that gives each agy account
its own `HOME` or `ANTIGRAVITY_CLI_CONFIG_DIR` keeps the conversation outside the default data directory (#17097): the
node checks the default first and, when the conversation is not there, reads the agy data directories named by the
processes under its agy panes (their environment, at most once every 30 seconds and only for a pane whose conversation
no known directory holds). The conversation is then read from the directory that actually holds it, with the same
per-directory safety checks as the default. The runtime companion keeps the directory Persona itself started it with. The node uses the conversation ID to
restore Persona-started panes with `agy --conversation`. The pane's row is then titled after that conversation, as the Antigravity
companion titles it: the companion's title, else agy's own (its `cache/conversation_metadata.json`), else the first
message typed into it (#13639). agy sets no terminal title, so until one of these is known the row has the shell's title
or herdr's tab name. Until an ID is known the pane remains accessible through the terminal and can still receive prompts.

Persona's own Antigravity hooks (#13729) work the way Moshi's do, so agy under herdr-core, Orca or Tern, or under herdr
without its integration, is readable too. The node adds a group `persona` to agy's global hooks file (`hooks.json` in the
agy config directory next to the data directory, `~/.gemini/config` by default; `ANTIGRAVITY_CLI_CONFIG_DIR` moves both)
with two entries, `PreInvocation` and `Stop`, running `<personad> hook --agy <Event>`. Groups of other tools (herdr, Moshi,
Orca) are never touched; the write is additive, atomic and idempotent, and happens when the node starts and agy is
installed. The hook reads agy's JSON input, finds its pane from the environment the engine gave it (`HERDR_PANE_ID`,
`HC_TERMINAL_ID`, `ORCA_PANE_KEY`, `TERN_PANE`), reports `conversationId` to the node over its socket and always prints
`{}` within two seconds; with no node it does nothing. The TUI stays the only writer of the conversation.
A top-level group was chosen over an agy plugin directory because agy loads the global file for every interactive
session and one owned key is as easy to remove as a directory.

The node treats the two reports alike: one conversation belongs to one pane, a conversation reported in two panes is
unreadable in both, and when herdr's report and Persona's differ for one pane nothing is known (never guessed). A
conversation that opens as a built-in subagent's is ignored as before (#13336). A report is kept in node memory and
dropped when its pane no longer runs agy. A pane is only readable under an engine that lists agy in its panes at all.
`personad node doctor` has an `agy-hooks` line (installed / missing / stale when the binary moved; `--fix` rewrites it).
`personad node agy-hooks [status|install|uninstall]` manages the entries; `uninstall` also tells the node not to put
them back.

Assistant's `agent_read` and `agents_list` show an AGY pane's working or finished turn only when a native hook matches
the current input in its verified transcript. A successful idle `Stop` must also have a completed reply. The list
gets its preview from the bound transcript. Terminal controls still use the engine's status, including the window
between typing a prompt and AGY taking it.

A conversation's row changes ID with the pane that shows it (#14395). While a live pane shows it, the row is the pane's,
`agy-<workspace>-<pane>`, and the companion's own row for the conversation, `agy-<conversation id>`, is left out. With no
such pane (it closed, agy quit in it, the engine gave the conversation another pane) the companion's row is listed
instead. The row listed names the conversation's other IDs in its `aliases`: a pane's row names `agy-<conversation id>`,
and whichever row lists the conversation names the pane rows that showed it before, most recent first (up to eight).
The node remembers which conversation the last 256 pane rows it listed showed, in memory only, so a pane that closed
while the node was not running is not named. A pane that runs another conversation now names that one: its ID is its
own row's again. Agents' A2A addresses are resolved through these IDs (`docs/integrations/a2a.md`).

**Close…** on an agy Agent that is not busy archives and closes it, as it does a Claude Code one (#14520): from its
pane's row the pane closes (`session.retire`), from its own row the companion closes it. The archive is kept under
`agy-<conversation id>`, also when made on a pane's row, so it holds for whichever row lists the conversation next, and
after Restore a message continues the conversation in the companion; the tool's archive, which Antigravity does not
have, answers `unsupported` (`docs/protocol/protocol.md`, "Agent session archives"). When the user only closes a pane
(**Close anyway** on a busy Agent) or stops agy in it, the companion's row for its conversation is listed as a closed
Claude Code pane is: offline, stopped by the user ("Not running"), until a pane shows the conversation again, or it works
or changes a minute or more after the close. It still takes the user's messages, which the companion runs, as there is
no terminal to resume it in; A2A messages do not wake it. The node keeps this mark in memory for the last 256
conversations closed: after a restart the companion's rows are listed as it says.

Orca's native chats (structured, no terminal) connect through node's bridge: Claude Code's, Codex's and every other provider's.
List, state, send text / images, interrupt, close, answer approvals / questions, change the permission mode (plan and full access
included, only those Orca takes for the chat's provider), thinking and model, rename, rewind the conversation, and mark read; no
screen reads, keys, or live terminal. A Claude chat reads as its Claude transcript, a Codex chat is listed once as its Codex thread,
and the rest read from Orca's own journal. A chat shows before its first message, and a chat closed in Orca stays listed, not
running, until a message opens it in Orca again (never in a terminal). An answer Orca does not take comes back as a new card.
New Agent also offers an "Orca chat · <provider>" launcher for each provider Orca has installed (while Orca runs and can start
chats), except one whose chat Orca refused to open because its chat runtime isn't installed there (until Orca shows a new chat
of that provider, or restarts; #17684): picking one starts an Orca chat, in the background, in the Orca worktree holding the
folder, with the first message; New Agent's default launcher opens a terminal unless the computer starts new Agents as Orca chats
("Orca chats as New Agent's default" below). See `docs/integrations/orca.md` ("Structured sessions") for the ids, the closed chats, the cards, the controls and starts.

Unsupported actions explain failure instead of partially acting. Orca rejects sending without entering anything when its pane is still
there with the Agent not in front, saying it may be suspended and nothing was sent (`agent_not_in_front`), or asking to reopen it on the computer (`not_running`).

Typing into an Orca terminal (#17040) is decided right before each write, and every write stays fenced to the terminal's
process (its incarnation), so nothing reaches a process that replaced it. Text and the keys that type it (letters,
digits, space) go in when a supported Agent is in front (the process scan); when Orca's `terminal.inspectProcess` names
another program in front (pi, opencode, an editor, a CLI Persona does not recognize), which is what a message to such a
CLI then types, as on herdr for an agent herdr does not detect (paste, then Enter); and when the owner answers the
question a failed start's kept tab stopped on (`start.continue`, `start.answer`, `start.keys`: "Answering a start's
question" in `docs/protocol/protocol.md`), only in a terminal the node opened that still runs the same process. Any
other shell in front gets nothing (an Agent's tab whose Agent exited would run the text as a command), and neither does
a terminal Orca can't tell about or an Orca without `terminal.inspectProcess`. Keys that type nothing (Enter, Esc,
arrows, Ctrl-C) go to whatever is there, so a stuck terminal can still be interrupted. **Run Agent Here** and the
**launcher switch** (#5035, and Claude Code ↔ Codex, #5054) restart in the same Orca terminal through #17039's guarded
single-use grant: the start line goes in only if Orca still reads that terminal with just its shell in front. A plain
shell is typed into from the phone only through the live terminal, which Orca doesn't offer over its local socket
(#17048); Orca doesn't list pi or opencode in its tabs as Agents (its process scan knows Claude Code, Codex, TRAE
CLI, omp and agy).

Orca New Agent creates terminals inside already-open Orca workspaces or subdirectories, starting in the selected directory. Unopened Git-repository directories attempt automatic registration; any other directory gets a terminal in the first local Orca workspace that `cd`s there (refused only when Orca has no workspace open). The `cd` is typed into the shell's line editor, so a directory whose name holds a control character (a tab, a line break) is refused before any terminal opens (`directory_untypable`). New Agent never takes over an existing terminal; Run Agent Here and a Resume in the shell an Agent left type into one only through a single-use grant, after Orca reads that terminal again, right before the command goes in, as the same process with only its shell in front (`terminal.inspectProcess` with its incarnation; an Orca without the call refuses with nothing typed). A start's terminal gets the start environment (`terminal.create`'s `env`, "What a start tab's shell starts with"); **New Terminal** / Open Terminal Here for a folder opens a terminal whose shell Orca puts in that folder (`terminal.create`'s `command`, [terminal.md](terminal.md), "New terminal: `panes.new` (optional, explicit action)"), with no start environment and nothing typed by Persona afterwards.
Resume of a running session returns it. A Claude session whose agent exited is continued like on herdr (#5548): in the shell its Agent left when Persona opened that terminal for it, it is still there and its shell is in front (its session's directory in that terminal's worktree, #17039), else a new terminal in the Orca workspace holding its directory (the same rules as New Agent); `cd` there, the launcher that started it with `--resume`, then the message. Never a second writer: a session a live claude still has open (Claude's desktop app, another terminal, suspended) is refused (`open_elsewhere`). A session an Orca native chat owns is refused too. Orca's own `terminal create --agent claude --resume` is not used: it exists only in some Orca builds' CLI, and it starts Orca's claude command, not the session's launcher.

**Resuming in Orca** (#17044): the other agents come back the same way, each through its own resume command, so Orca resumes whatever herdr does.

| Agent | Resume on Orca | Never a second writer |
| --- | --- | --- |
| Claude Code | `<launcher> --resume <id>` | a live claude on the session, suspended or in another program (`open_elsewhere`) |
| Codex | `codex-start` with `<launcher words> resume <id>`, the terminal's own hooks (`--no-daemon` where the CLI has it, as a new Codex start in Orca); the stock `codex` launcher with Orca's own Codex arguments (`settings.getAgentLaunch`, as a new start of it there; preferences Orca cannot follow refuse with `codex_launch_preferences` before a terminal opens) | a Codex process holding the thread's rollout, an app-server owning it, the Codex App (`codex_resume`, `codex_read_only`; `docs/integrations/codex.md`) |
| TRAE CLI | `<launcher> resume <id>` (the session's launcher, else `traex`; its model and effort stay the CLI's own) | a TRAE CLI still running it in a pane is the running agent |
| pi | a message runs the runtime's `resume_argv` through `tab-start` (`docs/integrations/pi.md`) | the runtime that lists it |
| omp | refused (`orca_resume`): omp resumes only in herdr (`docs/integrations/omp.md`) | |
| agy, Cursor | as on every engine: an agy conversation is continued through the Antigravity companion, a Cursor one on the computer; a launcher resume is refused (`work_agent_resume`) | |

A stopped Agent's last terminal does not hold a resume up: the resume runs in the shell the Agent left when Persona opened that terminal and only its shell is in front (above), else in a new terminal. Before #17044 the Codex and TRAE CLI rows of an Orca computer offered Resume and were then refused, and a Codex takeover never went to Orca (an Orca-only computer had "no terminal engine" for it); it now goes to the start's host, Orca included. The refusals that remain (`orca_resume`, `work_agent_resume`, `agent_not_in_front`, and `not_running` when it names the engine) are worded by the hub in the request's language (`rpc-errors.*`).

Herdr App New Agent (#2532) opens a terminal the way the app does (the machine's shell, kept, with the app's terminal identity) as a new tab of
the Herdr App space whose folder matches the directory, else of a new space for the directory, and adds it to the app's layout document, so the app
shows it like a tab opened there. The core's returned shell determines command syntax, file sourcing and startup probes; an unknown foreground
never receives a start command. The document is edited, never replaced: only that tab (and space) is added, at the version read, redone after a
conflict, including folder selection and terminal labels. When there is no layout document (Herdr App never ran on this core) or it is in a form
Persona does not know, nothing is written and an automatic start opens outside the app's spaces, grouped by its directory. A start explicitly
targeting an app space is refused if its layout or space cannot be resolved. A Not running session is
continued as on herdr: a new tab, its launcher with `--resume`. Space and tab renames write the same document.

Selecting `herdr-both` attaches both herdr and herdr-core hosts:

- Agents merges sessions; spaces / tabs, state, screen reads, messages, keys, closing route to the pane's own host, as when selected alone.
- **Phone create / continue Not running / space rename use classic herdr** (complete capabilities); New Agent can also be pointed at Herdr App
  in its host picker. Continuing an exited Herdr App session from phone opens a new herdr pane with `--resume` (same Claude session ID, continuous
  history, different window). herdr-core live terminal / phone takeover remain unavailable.
- Neither slows the other: snapshots fetched concurrently with separate timeouts; herdr-core events refresh only herdr-core,
  herdr events only herdr; a failed side clears only its own online state.
- Available only when both herdr and herdr-core are detected (can preselect offline, effective on reconnect; if only one exists,
  it works normally while the other list is empty).
- Orca next to herdr goes through "Host sets" above; there are no more combination options.

### Orca chats as New Agent's default

Who it serves: Agents users who prefer Orca's chat view to a terminal (#17650). A computer whose engine set includes Orca has a
switch under its Engine, **Start new Agents as Orca chats**, off by default (web: the machine page's Engine section; Android: the
computer's Engine page; iOS follows in #17056). The manager keeps it per computer in settings (`machine_orca_chat_starts`), answers
`GET` / `PUT /api/v1/machines/{mid}/orca-chat-starts`, and sends it on the machine as `orca_chat_starts` (`"on"` / `"off"`) only while Orca is in the set
(`docs/protocol/protocol.md`, "Engines"); the clients show the switch exactly where it is sent. Taking Orca out of the set keeps the
switch's value but stops it applying.

With it on, the manager (`server/internal/assistant/orca_chat_default.go`, in `agents.start_params`, the step every New Agent start an
app makes goes through) sends a start to the computer's Orca chat launcher of the same provider (`orca-claude` / `orca-codex`,
"Starting a chat" in `docs/integrations/orca.md`) when all of these hold:

- the person kept New Agent's default: no launcher, preset or model picked, and no host but Orca;
- that default runs the plain Claude Code or Codex CLI (`claude` / `codex`). A wrapper or a provider kept in Persona stays in a
  terminal: Orca's chat runs the plain CLI, so it would answer on another model or account;
- the computer's Orca offers that chat launcher now (Orca running, able to start chats, the provider installed there and its chat
  not refused for a missing chat runtime, the launcher neither hidden nor out of the computer's scope), and the computer's default
  host is not an engine runtime (Paseo, Cindy);
- the chat takes the start: not plan mode (an Orca chat promises no read-only start), a permission mode Orca takes for the provider
  (Codex's chats take no Accept edits), and none of the options only a terminal takes (a faster speed, ultracode, a goal, model aliases).

The chat runs the default's model when Orca's chats of that provider list it, with its effort when that model lists it; otherwise
Orca's own, and the start's note says so. A start Orca refuses is refused as an explicit Orca chat start is, with nothing started: a
folder no Orca worktree holds and no Git repository Orca can add (`orca_start_folder`), or a provider whose chat runtime Orca has not
installed on that computer yet (`orca_chat_runtime_missing`: start it in a terminal, or install it from a new chat tab in Orca).
Orca says the latter only when asked to open the chat, so the first such start is refused; the bridge then withdraws that chat
launcher (`docs/integrations/orca.md`, "Structured sessions"), and the next default starts open a terminal until Orca can open
the chat. Picking a terminal launcher starts it there.

Never routed: the assistant's starts (`agent_start`, `dispatch_start`, `task_plan`, reviewers, follow-ups, which never go through
`agents.start_params`; an Orca chat carries none of Persona's launcher, hooks or MCP config that a supervised task needs), resumes,
and any start that names a launcher. New Agent says where a start will go: `GET /api/v1/machines/{mid}/launchers` carries
`default_opens` (the chat, the permission modes with which a default start opens there, and whether the default's model goes with
it), worked out by the same rule; web and Android show one line under the start while the form keeps the default
(`web/src/lib/orca-chat.ts`, Android `NewAgentState.opensAsOrcaChat`), and the line goes as soon as the person picks a launcher,
a preset, a model, plan mode or another host.

## Task workspaces (#4786)

On classic herdr a top-level task leader and its workers share one workspace named after the leader's task. This is
placement and cleanup for that task only. On Orca the leader and same-machine workers share tabs of the leader's
worktree. Other engines and ordinary tasks keep their existing workspace behavior.

- **Opening.** A leader's `session.start` carries `task_id` and `workspace_label`. With no recorded `workspace_id`, its
  first tab opens a new workspace under the task title instead of the directory's basename. The manager records the
  intended label before dispatch and the actual workspace ID from the start response or a later live session row,
  verified against the node's persisted task record before that row may change the ID;
  the node also persists `task_id`, `workspace_id` and `workspace_label` with each session it starts.
- **Workers.** A worker's start carries the same `task_id` and the recorded `workspace_id`, so its tab opens beside the
  leader. A writable worker still gets its own Git worktree and runs there: `worktree.create` with
  `open_workspace:false` makes the checkout through the node's existing Git fallback, without opening another herdr
  workspace and without needing a new herdr RPC. The leader uses the same
  checkout-only rule when it needs a new worktree. The checkout and branch are not deleted when the task ends.
- **Rework and continue.** A stopped session's explicit resume reads its recorded task identity from the node, and the
  manager supplies it through the same start path. A new tab selects the recorded workspace by ID, never a workspace
  whose label happens to equal the directory's basename. If the task workspace is already gone, it is recreated under
  the retained task label and the new ID is recorded. A resume that can reuse the kept shell opens no tab, as before.
- **Closing.** Once the top-level task is accepted or cancelled and every Agent under it has been retired, the existing
  task-close pass calls `workspace.close` with the recorded `task_id` and `workspace_id`. A per-task lock keeps a start
  or resume from racing the final activity check. The node proves that exact pair against its own session records and
  refuses a live workspace, another task's workspace, a user-opened workspace or an unrecorded ID. A rename does not
  transfer ownership; matching only a label is never enough. Already-gone and repeated closes are idempotent.
  Closing the workspace closes any shells left in it after the Agents stopped; this task-owned exception does not
  change ordinary terminal retention. Git checkouts and transcripts stay.

**Orca (#17051).** The leader opens a tab in its selected Orca worktree. Each same-machine worker opens a separate tab there. If the start reply
omitted the placement, the manager confirms the linked leader's task and worktree IDs with the node before starting
the first worker. Read-only workers use their assigned directory. Writable workers
enter their own checkout in their new shell. A node-prepared checkout keeps its planned path, and a worker that
needs a Git worktree through `worktree.create` uses `open_workspace:false`. This groups the terminals even when
the checkouts belong to different repositories. The existing Orca worktree keeps its name and other tabs.

After the task is accepted or cancelled and its Agents have stopped, the node checks each terminal's original
creation receipt, task ID, worktree ID and idle shell before closing that terminal by handle and incarnation.
User-opened or adopted terminals have no creation receipt for the task. An older Orca that cannot provide the
required evidence still opens the tab and leaves it open. Git worktrees, branches and conversation transcripts stay.

## Agents run in a terminal

#4754. An Agent runs on top of a terminal: every Agent Persona starts runs in the computer's terminal engine, and its terminal outlives it for a grace period (#10111, "Finished terminals close after a grace period" below). The Agents list shows only Agents, but each has a terminal under it.

- **Starting**: every start or resume of an Agent goes through the node's `session.start` / `session.send`: New Agent in the apps and the web, the assistant's `agent_start`, `dispatch_start` and `task_plan`, auto-review reviewers, launcher repair, the Lark bot's `/start` card, extension tokens, Run Agent Here. On herdr, Herdr App, Orca and Tern the Agent runs in a new tab (or the pane picked), never headless (`claude -p`, `codex exec`, an SDK process). A start into a workspace it names starts herdr's server first when it has stopped, as every other start does ("When herdr is not running").
- **Not Agents, headless by design**: assistant conversations (the main chat, leader chats, scheduled tasks, channels, meetings, the Inbox AI twin, Lark `/agent`), listed in the Persona tab and not under Agents, and model calls (titles, triage, the launcher Test).
- **No terminal, by the user's choice**: an engine that runs Agents through provider SDKs (Paseo, Cindy), Orca's native chats and runtimes (DSH, pi, Antigravity, daemons) own their sessions; Persona leaves them as they are. So does Claude's desktop app, for a session New Agent starts there with the launcher `claude-desktop` (`docs/integrations/claude-integration.md`, "New sessions in the desktop app"), and the Codex App, for a chat started with `codex-app` (`docs/integrations/codex.md`, "New chats in the Codex App").
- **Stopping keeps the terminal**: Persona's own passes stop an Agent with node `session.stop`: a finished task's, an idle one, a cancelled task's, and `/exit` sent by `agent_send` (`docs/assistant/todos.md` "Stop Agents of completed tasks"). The Agent process quits and its tab stays with the shell it ran in, until the grace period below has passed.
- **Closing it when the session ends, by the owner's choice** (#10355): the setting `close_ended_terminals` (`persona_settings` key
  `assistant.close_ended_terminals`; Settings › Assistant › Agent follow-up on the web, Assistant Profile › Agent Follow-Up ›
  Terminals on iOS, Assistant settings › Terminals on Android) is **off by default**, and off nothing above changes. On, the
  manager closes an Agent's tab when its session ends, through the user's close paths only (`session.retire` with
  `close_terminal`, plus `after_exit` for the shell an Agent that quit left): a done or cancelled task's idle Agent instead of
  stopping it, and an Agent seen running that then quit, told to (`/exit`, stop cause `exit`) or by itself (no stop cause).
  Not an end, so the terminal stays (until the grace period below, if it is on): the user's Stop, an idle stop, a restart. Never closed: a busy Agent (working, waiting
  for an answer, background work, running subagents or workflows, a `/loop`, queued messages, a server it runs), assistant
  conversations, other tools' sessions, Agents Persona did not start, panes without Persona's mark for the session. An
  Agent that quit can have its shell closed on Herdr or Orca. The transcript stays and Resume opens a new tab
  (`server/internal/assistant/agent_terminal_close.go`).
- **Closing one task's tab after acceptance** (#10201): the owner can opt in when starting a new planned task. After
  `task_review` saves its acceptance as done, or the owner passes a task it accepted with `user_check`, Persona can
  close the original Herdr or Orca tab of that task's Claude Code or Codex Agent. It checks the tab and running process from dispatch, the current layout and focus, open terminal views,
  input, pending interactions and live work. A split tab or a workspace's last tab stays open. The transcript and
  worktree stay. See `docs/assistant/todos.md` ("Close a task's tab after acceptance").
- **Close already dispatched when the Agent exits** (#9648): a normal `session.close` can still close its original
  marked shell on Herdr or Orca. The node reads the selected server directly, requires a matching session record, no active or
  rebound Agent and a known foreground shell, then rechecks ownership and layout before closing only that pane.
  An identified target counts as already gone only after a complete direct inventory confirms absence; uncertain or
  changed ownership returns a refusal. Historical state without any terminal identity keeps its existing idempotent
  response. These separate snapshot and close calls add no atomic engine claim. Sibling terminals, files and
  worktrees are left alone; ordinary Stop and automatic task completion still retain the terminal.
- **Retrying a stalled Orca start** (#9633): before closing its used kept tab for a replacement, the node rechecks
  that no session or Agent has taken the pane after terminal lookup, including a stale-handle refresh. Recovery
  during lookup leaves the old tab and its expiry intact; the retry uses a fresh tab, as when recovery is seen
  earlier. An actual close failure still fails the retry without a replacement. The check and engine request
  remain separate operations; this is not an atomic guard against recovery after the final check.
- **A stopped Agent is read-only** (#4754): an Agent of a terminal engine that is not running, stopped by Persona or by the user, exited by itself or cut off by a restart, keeps its conversation and its terminal (the session's terminal opens the shell it left), and takes no messages: node `session.send` refuses it as `agent_stopped` with nothing typed, before any resume, and its row's `capabilities` have `read` and `terminal` without `send` (`server/internal/node/stopped.go`). The assistant's `agent_send` and `agent_interrupt` refuse a plain message too. Two things start it again, both through the same node call, `session.start` with `resume_session_id`: the user's **Resume** on its terminal page, and the assistant's explicit resume, `agent_send` with `resume: true` and `resume_why` (#8099, `docs/assistant/todos.md` "How to reuse"). The call types the session's launcher with `--resume` into the shell the Agent left when that shell is at rest in the session's directory (on Orca, in the worktree holding it: Orca's start changes to the directory itself), else it opens a new tab. Not covered: assistant conversations, Claude's desktop-app sessions, runtimes and engines that run Agents themselves, and a branch nothing has started yet (its first message starts it).
- **Resume in Terminal** (#5521): the chat's ⋯ on iPhone and the web offers it for a stopped Agent, the same rule on both (`Session.terminalResume(on:)`, web `terminalResume` in `web/src/lib/agent-resume.ts`). It opens the session's terminal page and runs the user's Resume there. This covers a Claude Code typed into a terminal of the user's own (a plain terminal, an SSH login) that has closed since: the node lists it stopped like any other, and resumes it in a new tab in its folder. A Codex thread nothing has open is a stopped Agent too (`node/codex_registry.go` gives it `terminal` and the stopped reason while its source stays `codex_history`), with the same line and the same Resume.
  - **The node decides**: it marks a row stopped only where a terminal engine can host the resume, so the clients add nothing of their own beyond the row and the computer's engine set.
  - **No terminal engine** (the empty set, #3302): an Agent that isn't running gets the item disabled, with "No terminal engine on <computer>". Its terminal page says the same and offers no Resume.
  - **Never offered**: while the session runs, in Persona's terminal or in one of the user's own (`claude_cli`: a second `claude --resume` would write the same transcript, and the node refuses it as `open_elsewhere`); while it starts; for assistant conversations; for the desktop apps' sessions (a message reopens those in the app, #4300); for rows of older nodes, which carry no capabilities.
  - **Tests**: `server/internal/node/outside_resume_test.go` (herdr: a new tab with `--resume`; no engine: no terminal and `engine_missing`; open elsewhere: no terminal and `open_elsewhere`, nothing opened), `TerminalResumeTests`, `ResumeInTerminalTests` and `web/e2e/resume-in-terminal.mjs` (mock `MOCK_RESUME_TERMINAL=1`).
- **A task goes on with a new Agent**: when every Agent of a task has stopped, the user's message on the task's card or its Retry starts a new Agent for the task (same computer, directory and launcher as its latest Agent, linked to the to-do; `server/internal/assistant/tasks_successor.go`), its first message the user's after who worked on it before and that Agent's last reply. Persona's own follow-ups to a stopped task (relayed answers and other supervision) start nothing and are refused. The one task-scoped exception is rework of a retired classic herdr or Orca leader with recorded placement: it resumes the same session in its task workspace.
- **Finished terminals close after a grace period** (#10111, `server/internal/node/finished_close.go`): the node closes a terminal a stopped Agent left once it has sat unchanged for `PERSONA_AGENT_TERMINAL_GRACE` (a Go duration in the node's environment, default `30m`; `0` or `off` keeps them until the user closes them, as before). It looks every minute, on a fresh snapshot, and closes a pane only when all of these hold, checked again under the session's recovery lock right before closing:
  - Persona opened it for an Agent: herdr's `persona_tab` mark, or on Orca, the live record or the original terminal identity saved with the session. A matching saved record survives a node restart;
  - no Agent is in it or bound to it (working, idle or waiting for an answer), no start is in flight, no failed start keeps it for a retry and no Codex waits for its thread there;
  - only a shell is in front (herdr `pane.process_info`), and on Orca nothing else is in the terminal at all: Orca's `terminal.inspectProcess`, fenced to the terminal's incarnation, proves the shell idle (`idleShellProven`: no program in front, no job behind the shell or stopped, no output since it looked). A quiet dev server, an editor, a CLI Persona doesn't recognize (pi, opencode), a background job, an answer Orca marks unverifiable and an Orca without the call all keep the terminal (#17039; before, "no agent process carries the pane's key" let those count as only a shell). No client has its terminal open;
  - its screen has not changed for the whole grace period; new output restarts the clock.

  Terminals the user opened, a pane picked for Run Agent Here and the panes of other engines (Tern, Herdr App) are never touched. A workspace is never closed directly: closing the pane closes its tab when it was the only pane, and the engine drops a workspace with its last tab, so a workspace goes only when everything in it was such a terminal (the first shell of a worktree the assistant creates for an Agent is marked too, #10624: `worktree.create` with `for_agent`, herdr's `persona_worktree` token or the node's in-memory record; it closes with the same checks once every other pane of its workspace is a finished Agent terminal, so the whole workspace goes; a worktree the user creates is never marked). When the engine does not answer nothing is closed that round; a failed close is retried on a later round; the clocks live in memory, so a node restart closes later, never sooner. Closing never changes the session's stop cause: an Agent the hub stopped as `task_completed` or `idle` can still be resumed by the assistant afterwards (an Agent stopped by hand waits for the user's Resume; one that exited by itself has no cause, which the assistant may resume on purpose since #11528). Resume needs nothing new: within the grace period it runs in the kept shell (the same recovery lock keeps the two apart), after it in a new tab. Running the Agent with `exec` so the pane exits with it was rejected: the last output and the screen a failed start is diagnosed from would go at once, Resume could not reuse the shell, and the typed start's `&&` chains and per-shell quoting do not survive `exec` in fish or PowerShell.
- **Orca terminal ownership** (#17041): Persona records a new Agent tab's original handle, incarnation and PTY root
  process identity with its session. The node can list the terminal as finished after restarting. Before a close,
  it checks the current placement, the process tree under the original PTY root and the runtime's current process
  evidence. A stopped terminal must pass the same `idleShellProven` check as the grace-period close. The shell's `PERSONA_AGENT=1`
  mark also has to match where the OS exposes its environment. Protected macOS shells rely on the original creation
  receipt and live process evidence. Older runtimes can still open Agent tabs, and guarded close leaves an
  unverifiable terminal open. Task acceptance also requires a single-pane tab that the current layout shows in the
  background with another tab in the workspace. If Orca assigns a new pane key to a terminal its UI did not adopt,
  the node can still list and clean up its stopped shell while the live creation record matches the handle,
  incarnation and workspace. Without that live mapping it leaves the orphan open; an orphan has no tab layout
  to authorize task acceptance. Guarded closes support local macOS and Linux terminals.
- **The Agent mark** (#10111): every terminal Persona opens for an Agent starts with `PERSONA_AGENT=1` in its environment (herdr `workspace.create` / `tab.create` `env`, the Herdr App's `terminal.run` `env`, Orca's `terminal.create` `env`; `host.EnvLauncher`, with the startup switches in "What a start tab's shell starts with" below), so a shell's startup files can tell it from one the user opened, for example to skip a prompt's git status daemon: `[[ -n $PERSONA_AGENT ]] && …`. The typed start also puts `PERSONA_AGENT=1` in front of the Agent command (`agentMarkEnv`, `server/internal/node/agentenv.go`), so the Agent's processes carry it on every engine, including Tern, which takes no environment for a new terminal (there the shell itself does not have it). A herdr or an Orca without `env` support is asked again without it. Terminals the user opens never get it.
- **Cleanup by hand**: `panes.list` labels each terminal a stopped Agent left with `agent_session`. **Close finished terminals**
  (`POST /api/v1/machines/{mid}/panes/close_finished`) previews the eligible terminals and closes only the ones the user confirms,
  after checking again that only a shell is in front. On Orca, the node uses its live record or identifies the original terminal
  after a restart by matching its saved session record to the current handle, incarnation and placement. Before closing a
  terminal with a saved record, Persona also verifies the original shell's identity and that Orca reports it idle. If ownership
  or process state cannot be verified, the terminal stays open. Older Orca runtimes without `terminal.inspectProcess` (#17039)
  cannot distinguish a shell from another program for this cleanup, so even an identified terminal is kept.
- **Who closes a terminal**: the user (**Close** on a session, **Archive and close**, force archive of an Agent blocked on a confirmation, closing a pane or tab from the terminal list), the grace period above for a finished Agent's terminal, and a finished review's reviewer, an internal helper, which closes with its worktree copy.
- **Checks**: `server/internal/assistant/terminal_guard_test.go` pins the manager functions that ask a node to close something, `server/internal/node/terminal_guard_test.go` the node functions that close a pane or tab; `TestHerdrStopKeepsTheTerminal`, `TestOrcaRetireKeepsTheTerminal` and `TestStartStartsHerdrFirst` cover the behavior; `TestOrcaGraceCloseOnlyAnIdleShell`, `TestOrcaFinishedTerminalsByHand`, `TestOrcaResumeInTheKeptShell` and `TestOrcaAdoptedRunIsGuarded` (`server/internal/node/hostorca_foreground_test.go`) cover Orca's.

## Tern

[Tern](https://stencil.so) is Stencil's multiplexing terminal. The node drives it through its `tern` command line (`server/internal/terncli`,
`node/hosttern.go`, #2154): nothing else to install, and `tern` is found on PATH, in `~/.local/bin` or in the app bundle
(`/Applications/Tern.app/Contents/MacOS/tern`), so a node with only the system's PATH (launchd, systemd) finds it too.

- **Spaces, slots, panes**: Tern's sessions are spaces and their tabs slots; every terminal block is a pane, `tern:<block id>` (the number Tern
  puts in the block's processes as `TERN_PANE`, which hooks read: `nodeclient.HookPane`). Browser and file blocks are not panes. Every open window is
  listed (each `tern` command names its window with `--window`); a session two windows show counts once.
- **Agents**: Tern does not say what runs in a block (a block's `program` is the shell it started), so a look asks `tern process` for each terminal
  block's foreground process, four at a time: `claude`, `codex` (also behind `node` / `bun`). A shell or classic herdr's client in front is no agent
  of Tern's (herdr run in a Tern tab keeps its own panes). Tern reports no agent state, and its own `working` flag misses Claude thinking quietly:
  a Claude's state is the status Claude Code keeps in its `sessions/<pid>.json` (busy, idle, shell when idle with a background command running, or waiting on a dialog of its own), else its transcript
  and hooks as on the other hosts (`docs/protocol/agent-states.md`, "Claude in a Tern or Rex block").
- **Changes**: `tern events` for the first window (blocks spawned, exited or closed, layouts, titles, working directories, bells; a bell is urgent).
  The node still snapshots every 10 seconds, which covers other windows. Every `tern` call is a client of its own, so the `client_*` events are
  left out of the stream.
- **Screens and input**: `tern capture --ansi` (colors, so drafts are told from placeholders; rows end in `\r\n` as the node expects),
  `--scrollback` for "recent". `tern send … text` types, `tern send … keys` presses keys under Persona's own names (`enter`, `esc`,
  `shift+tab`, `ctrl+u`…). A prompt is a paste and then enter, each after checking an agent is in front: otherwise the shell would run the text,
  so it is refused with nothing typed.
- **Starting, renaming, closing**: a new space is `tern new session` (no window switches to it), a new slot `tern new tab` in the session's
  directory, then the launcher is typed with `tern run`. `tern rename` names sessions and tabs. Closing a pane closes its block; a session left with
  no blocks that no window shows (one Persona opened) is ended, since Tern keeps an empty session otherwise.
- **Not yet**: live terminal and phone takeover (no stream), new worktrees, events from windows other than the first.
- **Live test**: `go test -tags ternlive -run TernLive ./internal/node/ -v` opens a hidden session of its own, drives it and closes it again;
  the user's own sessions are only counted. Pitfalls: `docs/lessons/tern.md`.

## Rex

Rex is a multiplexing terminal driven through its `rex` CLI and server control API (`server/internal/rexcli`,
`server/internal/node/hostrex.go`, #12334). `rex` is located on PATH, in `~/.local/bin/rex`, or on macOS inside
`~/Library/Application Support/com.superlogical.rex/RexServer/Versions/Current/Rex Server.app/Contents/MacOS/rex` and
the Rex app bundles. Every command passes `--autostart=false` so probing and snapshotting never launch a Rex server
unexpectedly.

- **Spaces, slots, panes**: Rex sessions are spaces, windows are slots, and `com.superlogical.terminal` blocks are
  panes stored as `rex:<block id>` (where `<block id>` is Rex's full `block:<uuid>`, matching the `REX_BLOCK`
  environment variable Rex sets in block processes and Persona hooks read via `nodeclient.HookPane`). Non-terminal
  blocks (such as onboarding) are skipped.
- **Agents**: Rex does not report which Agent runs in a block, so each refresh calls `block call process` (up to four
  in parallel) and identifies `claude`, `codex` (including behind `node`/`bun`), and `traex` from the foreground
  process while ignoring shells and nested classic `herdr`. Claude Code state is read from `sessions/<pid>.json`,
  transcripts, hooks, and screen dialogs, using the same path as Tern.
- **Changes**: `rex --autostart=false events --all --json` streams server, session and block events (block events
  come only with `--all`). Every `rex` command is a client of its own, so the `client_*` server events and the
  `client_connected` / `client_disconnected` session events the node's own calls raise are ignored, or each refresh
  would set off the next. Sessions, windows, blocks and labels changing, and the terminal events `process_changed`,
  `child_exited`, `title_changed` and `pwd_changed`, trigger a refresh; `bell` and `desktop_notification` an urgent one.
- **Screens and input**: `rex capture --format=vt --trim` reads colored screens (and `--unwrap` for unwrapped recent
  reads). `Prompt` verifies an Agent is in front, sends a `block.event` paste (neutralizing escapes), pauses, rechecks
  foreground, and presses `enter` via `rex send-key`. `SendText` writes to `rex send` over stdin.
- **Starting, splitting, renaming, closing**: `session.create` opens a space, `session.new_window` (`focus: false`)
  opens a slot, and `session.new_split` (`focus: false`) splits a pane. `session.set_label` and
  `session.set_window_label` rename spaces and slots. `block.close` / `session.close_window` close panes and slots; an
  empty session with no attached clients is destroyed via `session.destroy`.
- **Not yet**: live terminal streaming and phone takeover.

## T3 Code

[T3 Code](https://github.com/pingdotgg/t3code) runs coding agents in threads that its own server owns; its desktop, web and phone clients
all talk to that server. With T3 Code enabled, the node runs an in-process runtime bridge to it (`server/internal/node/t3codebridge.go`, #11784):

- **Finding the server**: the state file the server writes while it runs, `<T3 home>/userdata/server-runtime.json` (the T3 home is
  `$T3CODE_HOME`, else `~/.t3`), names its origin, process and version. Only a loopback `http` origin whose process is alive counts.
- **Signing in**: the bridge issues itself an MCP client session with T3 Code's command line on this computer
  (`t3 auth session issue --subject mcp-client`, labelled Persona, so it shows in T3 Code's settings), keeps the token in memory only and revokes
  the session when the bridge stops. A session T3 Code no longer takes ends the bridge, which issues a new one.
- **Reading**: projects, threads and timelines through the server's `/mcp` endpoint (`t3_project_list`, `t3_thread_list`, `t3_thread_read`),
  every 2 seconds. A thread is session `t3code-<thread id>`; its project's folder is its space. Messages, reasoning and tool calls are shown;
  checkpoints and other bookkeeping items are not.
- **Writing**: a message is `t3 thread send` and a new thread `t3 thread start` (the message on standard input), which run with the machine
  owner's authority as T3 Code's own clients do. A session the bridge issues has T3 Code's narrowest mode ceiling (approval-required), which
  would refuse sending to threads in T3 Code's default full-access mode. A new thread goes to the T3 project whose folder is the deepest one holding
  the chosen directory, with that project's default model and mode; a directory outside every T3 project is refused.
- **Interrupting and closing**: Stop is `t3_thread_interrupt`, which the session's ceiling allows only for threads at approval-required; a
  thread above it is refused with a line saying to stop it in T3 Code. Closing archives the thread.
- **Not supported**: answering approvals and questions (T3 Code's MCP tools cannot; such a thread shows as Needs you and is answered in T3
  Code), pictures (refused, never dropped), changing modes, choosing the model, worktrees, rewinding. Not yet tested against a live T3 Code
  install; the tests run a synthetic MCP server and command line.

## tmux (approvals only)

A Claude Code the user starts by hand in a tmux pane (`tmux new -s work`, then `claude`) is not an engine. It is listed as a session in an ordinary
terminal and stays read-only, with one exception: its approval can be answered from the app.

Claude Code writes the pane it runs in into its own `sessions/<pid>.json` (`"tmux": "work:@11.%11"`), so no process environment is scanned. When a
dialog holds that pane (a permission prompt, a question, a picker), the session lists as Needs you and gets a screen card on web, Android and iOS.
An answer presses the keys in the pane through the tmux command (`server/internal/node/hosttmux.go`, `server/internal/tmux`); a dialog that changed
since the card was made is refused without a keypress (`prompt_changed`).

Nothing else comes from this host: no spaces, slots, terminal tab, live stream or starting sessions. A message still goes through the session's peer
inbox when it has one. tmux is added next to whichever engine the machine selected and needs no choice.

## Commands waiting for input

An Agent sometimes runs a command that stops at a prompt: a login asking for a password, a CLI that wants a pasted token, a sign-in that
prints a URL and waits for the code (#14527). The line goes straight into the command from a card the owner answers on any client, never
through the model, so it needs no turn of the Agent's and never enters its context.

- **Antigravity (agy) in a terminal pane** (herdr, Herdr App, Orca, Tern). agy runs a long command as a background task of its conversation,
  outside the pane, and holds every message while a non-daemon task runs: neither a chat message (nor Send now) nor typing in the pane reaches
  it, and a message that carries the answer does not start a turn until the task ends. The node reads the pane's conversation instead
  (`server/internal/node/agyinput.go`, once a second with the Codex cards): a task log in `<agy data>/brain/<conversation>/.system_generated/tasks/`
  that ends at a prompt and has not changed for 2 seconds, with no end message from the task (`Task id … finished`) and no restart notice
  after it in the conversation's `messages/`, written since the pane's agy started, is a `command_input` card in the node's card list
  (`codex.interactions`). A prompt is the output's last line with no newline after it, at most 300 characters, ending in `:`, `?`, `>`, `]`
  or `)` (`proto.CommandPrompt`). The card carries the task's description from the transcript's start step, the prompt and up to 20 lines
  before it, escapes and blank lines removed.
- **The answer** goes back as a Codex card's does (`codex.answer`). The node checks that the task still waits on the same prompt (otherwise
  `prompt_changed`, nothing typed), finds the language server of the pane's agy in the environment agy gives the commands it runs
  (`ANTIGRAVITY_LS_ADDRESS`, `ANTIGRAVITY_CSRF_TOKEN`, read from a process under the pane's agy; a loopback address only), and posts
  `SendAgentMessage {recipient: "<conversation>/<task>", senderId: "<conversation>", content: "<line>\n"}`, the message agy's own
  `manage_task send_input` sends. The task's pseudo-terminal types it, and the task wakes agy when it ends. Not now and a hand-back type
  nothing; after either, and after the line went in, the same prompt is not asked again. Refusals reach the device that answered, in its
  language, and the card stays: `command_input_line` (empty, a line break or another control character, too long), `command_input_unreachable`
  (no language server found or reached: nothing typed), `command_input_refused` (agy answered with an error: nothing typed) and
  `command_input_unconfirmed` (the request went out and no answer came: the line may be in, so look before sending it again).
- **A prompt in the pane itself** (a shell, `ssh`, `sudo` in a herdr or tmux pane): a blocked Agent gets a screen card as before, with a
  password field when its last line is a password prompt, and the web terminal's key bar has a line key that types one hidden line and Return
  into the pane (`docs/web/web.md`, "Terminal").
- **Not yet**: Claude Code's and Codex's shell tools give a command no terminal anyone can type into, so such a command fails or waits without
  a prompt Persona can answer; agy on the Antigravity companion (ACP); Windows, where agy's environment cannot be read. #14527 lists them.

Limit: agy itself keeps what reaches a task in the task's mailbox file and, when the prompt echoes, in its log, as it does for its own
`send_input`. Persona keeps no copy: the card's field is secret, so the hub records `<redacted>` (`docs/protocol/protocol.md`, "Commands
waiting for input").

## botmux

[botmux](https://github.com/deepcoldy/botmux) maps each Lark topic to a Claude Code / Codex / compatible CLI session in its own tmux. It is not an engine:
when `botmux` is on the computer's PATH, node adds a botmux host alongside the selected engine host (`server/internal/node/hostbotmux.go`).
Typical use: work Agents in botmux, personal Agents in herdr, viewed together.

- **Listing and state**: each refresh runs `botmux observe --include-raw` (all sessions from all daemons, reused within 5 seconds, immediately reread after sending; 0.3–0.9 seconds for about 500 local sessions; occasional observe failures retain the previous list). botmux exits 1 when one daemon's probe failed (`unauthorized`, `unreachable`, `daemon_offline`) and still prints every daemon's answer: the sessions of the daemons that answered are listed, a failed daemon's stay as it last listed them, and a message to one of those is refused with its probe error, nothing posted. Only when no daemon answered is the observe a failure (#8949).
  Lists Claude Code, Codex, and TRAE CLI (`cli.id: claude-code / codex / traex`), with identical state rules: running sessions use botmux state (dialog `tuiPromptActive` or botmux Needs you → Needs you; `working` / `starting` / `analyzing` → Working;
  others → Done). Suspended (`not_running`, not closed, active within 7 days) appear Not running. For Claude Code, botmux session ID is the Claude ID used to start it, so transcript handling is unchanged.
  For Codex / the compatible fork, observe omits CLI session ID (stored in botmux database), but every prompt starts with `<session_id><botmux-session-id></session_id>`.
  Node searches rollout beginnings in the same working directory (first 512 KB, stopping each file after finding the marker). Matching rollout is the thread (newest-written among forks),
  listed by thread ID and read as rollout history (`docs/integrations/codex.md`); title prefers thread name, otherwise botmux topic title (`botmuxThreads`).
  Without matching rollout, omit: newly opened sessions before initial prompt enters rollout (next refresh finds them), or custom bot-specific `CODEX_HOME` / `TRAE_HOME` directories unknown to node
  (Codex reads known homes, compatible fork only `~/.trae/cli`). `codex-app` (botmux drives Codex through app-server, without session-ID prompts) and other CLIs omitted.
  Space is cwd (like Herdr App terminals outside spaces; trim trailing `/` to avoid duplicates), named by final directory component. If it matches parent case-insensitively, include parent (`Botmux/botmux`, #1836). Derive only from its own path, not other listed sessions, so name-based pin / mute / group-to-do bindings stay stable.
- **Sending**: same for all three, delivered through botmux dashboard `POST /api/trigger` targeting the session (`target.kind: "turn"`, `botId`, `sessionId`), not terminal input. Botmux queues while busy,
  resumes suspended sessions itself (no separate herdr `claude --resume`, also for suspended beyond 7 days / unlisted), and sends replies back to Lark topics.
  Node gets the dashboard token through `botmux dashboard current`, memory-only, connecting only to local 127.0.0.1.
  Before sending, the node checks capabilities via the read-only `GET /api/sessions/:id/turns` probe (cached per botmux instance and re-probed after dashboard restart, token rotation, or CLI upgrade; never probed with a real message and never retried after delivery).
  Sending as the bot's owner needs the **BotMux Owner Union ID** (Settings › Agents, under "Show botmux sessions"; one hub-wide value handed to every node with `node.configure botmux_owner_union_id`, kept in the node's memory and handed again on each connection).
  Empty, the node sends the existing external-event trigger even to a botmux that advertises `ownerActor`, and sets `botmux_external_trigger: true` with `botmux_external_trigger_reason: "owner_unset"` so the composer says no BotMux Owner Union ID is set and messages go the usual way.
  Set, when `capabilities` includes `ownerActor`, `POST /api/trigger` sends top-level `actor: "owner"` (with no `envelope.payload` or `envelope.rawText`), adds `actorUnionId` only when `capabilities` also includes `ownerUnionId`, and confirms the echoed `actor: "owner"` in the response, so botmux delivers the turn as the bot owner with `<sender>` and `<message_location>`.
  botmux's `403 owner_unresolved` (several `allowedUsers`, no `ownerUnionId`) or `owner_mismatch` (another person's id) is not retried as a stale token: the send is refused with a reason the apps word plainly (`docs/protocol/protocol.md` "BotMux messages").
  On older botmux versions (below 3.59.23, where the endpoint returns `404` or `ownerActor` is absent), the node sends the existing external-event trigger and sets `botmux_external_trigger: true` (`botmux_external_trigger_reason: "upgrade"`) on the session so the composer shows a one-line upgrade notice (`3.59.23+`).
  Settings suggests an id from the computers' bots.json (`botmux.owner_candidates`: only `ownerUnionId` and the `on_…` entries of `allowedUsers`, read-only); a suggestion only fills the field, and nothing takes effect until it is saved.
- **Unsupported**: screen reads, keys, dialogs (answer in Lark), close, rename, create, live terminal; app hides or explains by capabilities.
- **Showing it as in Lark**: **structured turns first, wrapper parsing as per-turn fallback.**
  When the local botmux dashboard serves `GET /api/sessions/:id/turns` (`botmux.session-turns.v1`, `server/internal/node/hostbotmux_turns.go`, `server/internal/transcript/botmux_turns.go`), the node reads turns incrementally by `seq` cursor and matches each turn to its transcript entry by `anchor.cliEntryId` or `anchor.inputSha256`, filling `Item.botmux` with the inbound text, sender display name, attachment metadata, thread placement, mentions, and full reply text (including `--content-file` and `--card-file` replies).
  When the endpoint returns `404` (older botmux) or a turn has no structured record (e.g., turns before a botmux upgrade), the node falls back per turn to wrapper and `botmux send` parsing (`server/internal/transcript/botmux.go`).
  The transcript text is always unchanged so the original stays one tap away, and only display names leave the node (never `open_id` or `messageId`). Formats and fallbacks: `docs/protocol/protocol.md` ("BotMux messages").
- **Why it needs you / open in Lark**: reason from `botmux send --attention` goes into `attention_reason` while Needs you;
  app shows it in list rows / chat top. Raw observe topic link (`feishuThreadLink`, HTTPS only) becomes `external_url` for "Open in Lark."
  Both are raw botmux fields, not stable APIs; omit if absent.
- **Summary (`last_text`)**: when not Working / Needs you and botmux replied to the latest message (`previewBotState: replied`), use its last reply (`previewBotFullText`, otherwise `previewBotText`; cards stored as `[卡片: 标题]`, "card: title", strip wrapper, keep title).
  Agent answers go to Lark through `botmux send`, so transcript often ends only with `BOTMUX_NOTHING_TO_SEND`. While working, unreplied, Needs you (topic reply often explains the wait), or transcript has text over 10 minutes newer than this reply (work continued outside botmux), retain transcript's last text.
  Standalone `BOTMUX_NOTHING_TO_SEND` never counts as text in any session: retain earlier text if alone in a turn. A transcript containing only that still counts as spoken; after over 7 suspended days it is listed Not running.
- **Execution failures**: botmux failure notices (warning-symbol prefix, saying it stopped automatic continuation / recovery / restart / handling) are not questions and do not mean Needs you.
  State follows botmux turn (usually Done); reason in `failure_reason`; app shows a red error with Retry (send "Continue" through botmux) and Open in Lark.
  Agent-issued `botmux send --attention` still means Needs you, even with a warning prefix.
- **Stale attention**: botmux clears attention / failure notices only for messages passing through it (Lark, delivery), not terminal or other channels.
  A newer transcript user message after the attention / failure makes it stale: hide Needs you / reason, use turn state.
  Slash commands, `! cmd`, and other Agent reports do not count. Read only the final 384 KB; if a turn wrote more after that message, it may be invisible, conservatively retaining attention rather than falsely clearing.
- **No duplicates**: botmux sessions using herdr backend already are herdr panes, so botmux host omits them. If botmux Codex also appears in Codex history / desktop lists (same rollout),
  keep only botmux's actionable row, not read-only history. Botmux Codex / compatible-fork sessions do not attach app-server (`thread/resume`), read screens for question / approval cards, or inspect composer before sending;
  like botmux Claude Code, those actions stay in Lark / on computer. Codex suspended beyond 7 days and absent from botmux appears as read-only Codex history; reply in Lark.
- **Same session on both sides**: if also running in an engine pane (previously resumed from phone), the live pane wins and botmux's suspended record yields.
- **Detection**: find `botmux` when node connects and learns whether to list it (next item), and on engine switches: PATH first, then installer path `~/.botmux/bin/botmux` (launchd node has only system PATH). Installing later requires restarting that computer's personad.
- **Disable**: "Also show botmux sessions" (#2575; the web has it in the machine page header as **botmux · On / Off** beside the engine pill, one click flips it, #2842; iOS below Engine in machine details; Android next to Engine in Engine and connection, saved on tap, #2899), default on. Changes immediately send `node.configure {"botmux": …}` (the hub adds `claude_dirs` so the node keeps its Claude config directories), stored in hub settings.
  `"botmux": false` in the machine's `machines[]` hub.json entry is only its unset-setting default (`docs/operations/config.md`; old nodes ignore). Stops `botmux observe` / `botmux dashboard current` (in-flight results discarded); Agents has no `botmux:` panes.
  Hub specifies the setting in `node.configure` on connection; node does not attach botmux or push lists before that (preventing sessions briefly disappearing / returning). Old hubs omit it; attach as before when asked for sessions.
  After disabling, tmux botmux sessions still appear as Not running history. Replying on phone launches engine `claude --resume`, potentially colliding with botmux's instance; reply to these in Lark.
- **To-dos**: attach by session ID as usual; project-wide grouping uses botmux session cwd.
- Live test: `go test -tags botmuxlive -run BotmuxLive ./internal/node/ -v` (read-only listing plus one dry-run delivery: validation / rendering, no dispatch).

## Named herdr sessions (#2196)

herdr can run more than one server per account: besides the default one, named sessions (`herdr --session <name>`, listed by
`herdr session list`), each with its own socket under `<herdr config dir>/sessions/<name>/` and its own ids, so `w4:p1` can be a pane of two
of them at once. A node on the default herdr server drives every **running** named session as part of the herdr engine
(`node/hostherdr_sessions.go`):

- **Ids**: a named session's pane, tab and workspace ids are `@<session>:<id>` (`@build:w4:p1`, `herdr.QualifyID`); the default server's stay
  bare, so stored rows, hooks and clients read as before. No host kind starts with `@`, so the hub and every client take them for herdr panes, and they
  hold no `/` a path could split on. Sessions and their live rows are listed, read, sent to, interrupted, closed, renamed, split, streamed (observe,
  control, the full herdr view) and given resume commands on the session the id names; ids herdr hands back (a split, a new tab, a worktree) are
  qualified again.
- **Showing the session**: a named session's spaces are labelled `<session> · <workspace label>`. Renaming one sets herdr's label without that
  prefix (typed or not), and `space.rename` answers the name the space shows now, which the hub keys pins, mutes and to-do links by.
- **Left alone**: stopped sessions, the `persona-<hash>` sessions of other nodes' data directories (`herdr/session.go`), names that cannot stand in an
  id, sessions explicitly blocked per computer (`PUT /api/v1/machines/{mid}/herdr/sessions/blocked`, #14898), and every other session on a node that runs on a session of its own or on a socket `HERDR_SOCKET_PATH` moved. Windows drives the default server only.
- **Blocking named sessions (#14898)**: each computer keeps a blocked list of named herdr sessions (`herdr_sessions_blocked/<machine id>`, empty by default, configured in Web **Settings › Agents › Named herdr sessions** or `persona_settings` `agents.blocked_herdr_sessions`). The manager sends the list in `node.configure` (`herdr_sessions_blocked`), and the node persists it in `~/.persona/herdr-sessions-blocked.json` along with session IDs seen inside blocked sessions. When a named session is blocked, the node skips it in `herdrHost` discovery, snapshots, lookups and event streams, ends active terminal streams for it, and ignores hooks, standby replay, plugin permission polls, outside Claude processes, Codex process/rollout scans, runtime rows, and remembered offline transcripts from it (keeping assistant and kept sessions exempt). Unblocking immediately restores both live driving and remembered session IDs.
- **Pane identity from processes**: hooks, MCP calls, the pi extension (`herdrPaneOf`) and the process scans read `HERDR_SESSION` (or the session
  directory in `HERDR_SOCKET_PATH`) with `HERDR_PANE_ID` (`herdr.EnvSession`, `herdr.PaneOf`), so a hook in a named session's `w4:p1` never binds
  the default server's `w4:p1`. Hooks, MCP calls and the pi extension write panes as the default server sees them (`nodeclient.HookPane`), and the
  node translates them where they come in (`node ownPane`: the node socket, the standby spool, runtime hints); the node's own process scans write
  them as its herdr host does (`node envPane`). The two differ only on a node whose own server is a named session: its own session's panes are bare
  there, and the default server's are `@default:<id>`, which it does not drive.
- **Never in the default server's way**: `herdr session list` runs in the background, again once the list is 5 s old; an id of a session the list
  does not have makes the node ask again (at most once a second) and wait up to 6 s for the answer. Each listing asks the named sessions alongside
  the default server and waits at most 2 s from its start for them (and, the first time, for the list); what a session has not answered by then
  comes from its last answer, with one call to it out at a time. The default server failing is the herdr host failing, as before: while it is down
  the herdr host lists nothing, the named sessions included, and keeps its last list. Each server has its own event stream; the named ones are
  looked at again every 5 s (a session started since gets one, a stopped one's ends), and one that ends is opened again on its own.
- **Pane-less work stays on the default server**: new workspaces, and new sessions started without naming a workspace (a named session's labels
  never match a start's directory name). A start, a new tab or a worktree in a named session's workspace opens there.
- **Focus** following (`herdr.client.focus`) is the default server's: it is the computer's own herdr window. Focusing a named session's pane works
  but brings no echo back. A full herdr view is attached to one server (the session of the pane it opened on): asked to show a pane of another
  session it ends, and opening it again attaches to that session.
- **Known gap**: the iPhone and Android apps rename a space optimistically under the name typed; for a named session's space the name shown
  (`<session> · <name>`) differs, so they show the typed name until the app reloads. The web and the hub use the name `space.rename` answers.

## When herdr is not running

After reboot, launchd / systemd starts node, but no terminal has started classic `herdr` server (#1879). Node starts it on demand:

- **When**: node startup (after hub specifies settings in `node.configure`; old hubs omit them, so assume enabled after 10 seconds), and before phone create / continue Not running.
  Only with herdr or herdr + Herdr App, only classic herdr; herdr-core has its own lifecycle and is untouched. Not from polling,
  only those two occasions.
- **How**: `herdr server` (v0.9.5 headless foreground server), in a separate session (`setsid`) so node restarts do not kill it.
  Linux systemd-user nodes instead use `systemd-run --user --unit=persona-herdr-server` (stopping the node unit kills its whole cgroup).
  Wait for connectable socket (at most 20 seconds) before create / continue.
- **Clean environment**: herdr server passes its environment to every pane and claude. Node uses `env -i` with only `HOME`, `USER`, `LOGNAME`, `TMPDIR`, language,
  `XDG_*`, `HERDR_SOCKET_PATH`, `HERDR_SESSION`, `HERDR_CONFIG_PATH` (the server reads the config file the node reads for the worktrees directory), `PERSONA_HOME` (panes find the node socket in that directory), then user's login shell (`$SHELL -l -c 'exec herdr server'`; if launchd has no `SHELL`, read account login shell).
  PATH comes from that shell, which starts from the system directories with the tool directories after them: hub.json `machines[].extra_path` and the mise / asdf shims
  directories that exist (#10127, `docs/operations/config.md` ("hub runtime config (hub.json)"); a profile that sets PATH outright drops them). No node / launcher `ANTHROPIC_*` or proxy variables. Find herdr on node PATH first, then ask login shell.
- **What a start types** (#744, `node/agentenv.go`): a pane has its engine's environment, not the node's. A herdr server started some other way, or Orca's app, may lack the node's `PERSONA_HOME`, and an engine started from the assistant's Bash tool hands the assistant's own variables to every pane.
  So every command the node types into a pane to start or resume an Agent (Claude, Codex, compatible CLIs, pi; herdr's `resume_argv` too) does two things:
  - It puts `PERSONA_HOME=<the node's data directory>` in front, where the pane's shell takes `VAR=val cmd`.
  - In the sh family, it first runs `unset` on two sets of variables. One set is what the node gives only the assistant's processes: `PERSONA_ASSISTANT`, `PERSONA_SESSION_ID`, `PERSONA_SPACE`, … in both spellings, plus `CLAUDE_CODE_AUTO_COMPACT_WINDOW` and `ENABLE_TOOL_SEARCH`. The other is what a running Claude Code sets for the programs it runs: `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_EFFORT`, ….

  fish gets the directory only. A shell that takes no assignments (csh, nu, pwsh), and Windows, get the command as before. An Agent that should have `CLAUDE_CODE_AUTO_COMPACT_WINDOW` or `ENABLE_TOOL_SEARCH` gets it from its launcher or from Claude Code's settings, not from the pane.
  The generated `mcp.json` (the server's `env`) and the hook commands (`PERSONA_HOME=… personad hook`) name the directory as well, so they work even when the start command could not carry it.
- **What a start tab's shell starts with** (#10365, `node/startenv.go`): what a start types comes after the shell has read its startup files, too late for the questions those files ask (oh-my-zsh's "Would you like to update? [Y/n]" reads a key while `~/.zshrc` loads). So the tab a start opens (`newTab`, pi's resume tab) is created with `DISABLE_AUTO_UPDATE=true`, `DISABLE_UPDATE_PROMPT=true` (always together: the second alone means a silent update, a `git pull` in the tab), `ZSH_DISABLE_COMPFIX=true` (oh-my-zsh's insecure-directories check) and `POWERLEVEL9K_DISABLE_CONFIGURATION_WIZARD=true` in its shell's environment, next to the Agent mark `PERSONA_AGENT=1` (`startShellEnv`, one create through `host.EnvLauncher`), where the engine takes one:
  - herdr: `tab.create` / `workspace.create` `env` (an object of strings; the CLI's `--env KEY=VALUE`; herdr 0.9.5). A herdr that refuses the field (one from before environment support) gets the create again without it (`createWithEnv` in `server/internal/herdr/herdr.go`), and the tab opens without the variables. Never in `herdrServerEnv`: the user's own tabs keep updating.
  - the Herdr App: merged into `terminal.run`'s `env` with the app's terminal identity (which wins on a clash).
  - Orca: `terminal.create`'s `env` (an object of strings; checked on Orca 1.4.277, #17036). An Orca whose schema refuses the field (`invalid_argument`, answered before anything is made) gets the same create again without it (`orcart.WithEnv`, `server/internal/orcart/api.go`), and the terminal opens without the variables; an older one that does not know the field drops it the same way.
  - Tern has no environment for a new terminal yet: its tabs rely on the question being answered (`docs/protocol/protocol.md` "Answering a start's question").

  A terminal the user opens, a split and a preview's server tab never get them. The start line unsets them again in the sh family (`startupOnlyEnv`, next to the assistant's variables) and keeps the Agent mark, so the Agent and the shell the tab keeps have the user's environment plus `PERSONA_AGENT`; fish keeps them (no `unset`), which no Agent reads. A `zstyle ':omz:update' mode …` in `~/.zshrc` wins over the variables: that question, and the ones no variable turns off (a bare `compinit` outside oh-my-zsh on insecure directories, ssh's unknown host key, zsh-newuser-install, a passphrase), still stop the start with `shell_waiting`, answered from the App for a New Agent start and a planned task alike (`docs/protocol/protocol.md` "Answering a start's question").
- **When a stdio node ends** (#10114): a node serving one hub connection over stdio (`personad node --stdio`, a hub's `kind: local` machine) stops, as it exits, a herdr server it started itself in that process when the server has no panes, so a throwaway hub leaves no server behind. A server with panes, one started by anyone else, and the resident node's (`--attach`) are left running.
- **Only one**: in-node mutex plus `flock` on herdr configuration directory (another node for the same account); recheck socket after locking.
- **One server per data directory** (#6482): a herdr server's panes reach the node whose `PERSONA_HOME` the server was started with, so two nodes with different data directories on one account must not share a server.
  A node whose data directory is not `~/.persona` uses a herdr session named after it (`persona-<8 hex of the directory's hash>`): socket `<herdr config dir>/sessions/<name>/herdr.sock`, its own saved workspaces and log, the same `config.toml`.
  The node reaches it through that socket and passes `HERDR_SESSION` to the herdr CLI it runs and to the server it starts, so the panes carry it too. The node with the default data directory keeps the default server.
  An explicit `HERDR_SOCKET_PATH` or `HERDR_SESSION` in the node's environment wins (a node started inside a herdr pane has them, so it uses that pane's server; it logs when that is not the data directory's own session); under systemd the server of a session is a unit of its own (`persona-herdr-server-<session>`); Windows keeps the default server. A default server already running with another data directory's `PERSONA_HOME` (started by a node before this) is not detected: restart herdr.
- **Switch**: per machine, "Automatically start herdr when needed" below **Settings › Machines › Engine**. hub.json
  `machines[].herdr_autostart` is only the machine's unset-choice default (omitted means enabled). User choices live in hub settings
  (`herdr_autostart/<machine-id>`), sent through `node.configure` on connect / changes (a change also carries `claude_dirs`, so the node keeps its directories). Once disabled, manual `herdr server stop` stays stopped,
  and node does not restart it. `GET /api/v1/machines/{mid}/herdr` (`{autostart, running, known}`),
  `PUT` (`{"autostart": bool}`); row exists only for herdr / herdr-both. Client UI pending (#1915).
- **Manual start**: `node.info` includes `herdr_running` / `herdr_running_known` (old nodes omit both, distinguishing stopped from
  unknown). `POST /api/v1/machines/{mid}/herdr/start` forwards to node `herdr.server.start`, **independent of autostart**:
  that switch controls node-initiated starts; a user's Start tap explicitly requests it. Failure reasons below (`engine_missing`).
- **Current status**: `node.info` is requested only once on node connection. After reboot, node waits for `node.configure` (after `node.info`) to start herdr,
  so relying on `node.info` leaves stale notices until node reconnects; same after manual Start / computer-side `herdr server stop` (#1916).
  `GET …/herdr` therefore asks `herdr.server.status` live (one socket connection, `{running, known}`), reused for 3 seconds;
  successful `herdr/start` also records result. Old nodes without the method retain `node.info` values and avoid reasking within 3 seconds.
  Android Agents has the notice / Start button (#1916), shown when online and known stopped; tap explicitly starts.
  iOS / Mac too (`HerdrNotice.swift`): poll every 60 seconds while Agents is foregrounded and not covered by chat, and at once when a watched machine comes online or changes engine (#4685). One notice per machine atop list, respecting machine filters / hub groups.
  One read failure does not remove existing notices; endpoint 404 stops polling that machine. Start shows "Starting…"; failures show localized known reasons inline (immediate exit / killed, missing, no response, old node, offline, timeout, `HerdrFailure.says`; unknown reasons remain raw). Keep button; raw hub text, status, request ID under long-press / context-menu Copy details, not body.
  After success, use POST result for that machine during this run until hub also reports running (GET may still use connection-time value). Web same (`web/src/lib/herdr-notice.ts`, `views/agents/HerdrNotices.tsx`), below filters in sidebar / mobile Agents section, polling every 60 seconds while visible and at once when a watched machine comes online or changes engine or the tab is shown (#5060),
  same rules / localized failures, with raw hub text, status, request ID (`X-Request-Id`) in Copy details button / context menu. The notice's code, its polling and its words (`herdr-notice`) are not on the web first screen: `Sidebar.tsx` loads them with its first render (`lazyPage`, drawn once the chunk is in, without Suspense so the sidebar's other updates never wait for it; a chunk that won't load is reported and the next mount asks again, #14339).
- **While it is not running**: the node keeps asking herdr for its sessions about twice a second. With no socket it falls back to the herdr CLI, which can reach a server the socket cannot (another session, an older herdr). Once the CLI says no server runs (`server_not_running`), calls without a socket return that same answer without running herdr for 1 s, then 2 s, 4 s, … up to 30 s; the socket answering, or the CLI finding a server, ends the wait at once (`herdr.Client.call`). A computer with herdr installed and its server stopped used to run herdr twice a second (about 140 runs a minute, 240 ms of CPU a minute between the node and those runs; about 20 ms now). A started server is found on the next poll through its socket, so starting it from the phone or a terminal shows its sessions as fast as before. `herdr session list` (the named sessions, every 5 s) needs no server and succeeds, so it has no such answer; instead a list that found nothing running is kept while herdr itself, the entries of `<herdr config dir>/sessions/` and the absence of every listed socket stay the same, and given again without running herdr (`herdr/sessionlist.go`). A server starting creates its socket or its session's directory, so the next list runs herdr again; a list with any session running is never kept.
- **Failure**: still `engine_missing`. Missing herdr copy unchanged; installed-but-failed says "Automatic start also failed." Details include exit code / final output lines,
  output in `persona/herdr/persona-herdr-server.log` under the user's cache folder (`~/.cache` on Linux, `~/Library/Caches` on macOS; a folder of the node's own in the temp folder when there is none), also node logs. No autostart on Windows; behavior unchanged.

### When herdr is not installed

New computers often lack herdr (#2847); previously Start only said "herdr not installed," requiring someone to log in and install it. Node now installs it (`node/herdrinstall.go`):

- **What**: official herdr release binary for OS / CPU (linux / macos × x86_64 / aarch64), **version pinned** to match other computers
  (currently 0.8.0, not `latest.json`). Verify hardcoded sha256 before installing to `~/.local/bin/herdr` (no sudo, on node PATH).
  Write temporary file and rename only after verification, avoiding partial binaries on interruption. Windows / other architectures have no package and require manual install. Version upgrades update the version and all four sha256s.
- **When**: always install on user Start. Node-initiated starts (startup, before create / continue) install only if `machines[].herdr_autoinstall: true` in hub.json;
  omitted preserves "Not installed." One install at a time; concurrent requests share it.
- **Slow downloads**: background download up to 15 minutes; Start waits at most 15 seconds. If ready, start and return `running`; otherwise return "Installing herdr 0.8.0… It will start automatically when ready"
  (client shows original text after "Not started:"). Node starts server when done; next Agents poll sees running and removes notice.
  `herdr.server.status` / `GET /api/v1/machines/{mid}/herdr` add `installing`, `install_error` (last failure); client can show Installing, not wired yet.
- **Install failure**: GitHub unreachable, timeout, HTTP error, checksum mismatch all give a localized reason. Network failures say "A proxy may be needed: set node HTTPS_PROXY and restart it"
  (downloads use Go `ProxyFromEnvironment`). Also log reason on node.

### Resuming Persona-started sessions after restart

herdr restores workspaces, tabs, panes after server restart, then **after a client attaches** resumes Agents using built-in commands (`session.resume_agents_on_restore`, default on),
for Claude Code plain `claude --resume <id>`. Persona-created Claude uses launchers (`personad launch <name>`, ~/.zshrc functions),
`--settings`, `--mcp-config`, Persona hooks; plain resume loses all of these, switching gateway to user's subscription and dropping MCP / hooks.

Persona therefore reports its own recovery command for each Claude pane (`resume_argv`, only this pane):
`eval "<Persona's original launch command, --resume <id>>"`. herdr types it into the pane shell during restore, retaining ~/.zshrc launchers.

- User-started terminal claude is not reported; herdr restores normally; user `config.toml` untouched.
- Older Persona sessions / sessions predating node restart (launcher in node session records): report once on refresh if running in herdr.
- Claude changes sessions in a pane (`/clear`): report new ID when Persona SessionStart hook arrives; herdr does not track it automatically (v0.9.5 tested).
- Resume command omits permission / thinking settings; paths with single quotes are not reported (herdr rejects `'` arguments), falling back to built-in restore.
- Node-started headless server has no attached client, so herdr does not resume Agents until someone opens herdr on the computer. Resume on a Not running session's terminal page still works.
- herdr before 0.9.2 answers the report `ok` and drops `resume_argv`. The node asks the server's version first (`ping`, never the CLI's: a binary upgraded in place says the new one while the old server runs) and on such a server reports nothing, logs once per server that its Agents will resume bare until herdr is upgraded, and gives the command to the next server (#11792).
- An Agent resumed without Persona's command anyway (an old herdr, a resume typed by hand) is listed with `off_launcher`: the scan of running claudes sees its claude without Persona's `--mcp-config`. A launcher switch to the launcher it was started on brings it back there (`docs/protocol/protocol.md`, "Switching a running Agent to another provider").
- **Interrupted by restart** (#1917): sessions running before restart appear `interrupted` afterward (`docs/protocol/agent-states.md`), read-only like any stopped Agent, with Resume on the terminal page. Node records the pane and
  launch time (session records in hub settings); if launch predates current node startup and no Agent is alive, it is interrupted. Sessions launched after this node startup do not count,
  nor user-started terminal sessions (no recorded pane). No automatic continuation: restart may interrupt a tool call, so the user decides. Resume uses ordinary
  `session.start {resume}`, launcher `--resume` in the shell the Agent left when it is still there, else in a new pane.
- No "Continue all" yet (machine-wide batch start, client half).

#### Orca after a reboot (#17038)

Orca's terminals survive an orcad restart, and a node restart leaves them alone. A reboot of the computer stops orcad and
its terminal daemon together, and Orca 1.4.277 cannot be given Persona's command to bring an Agent back with. Checked on an
isolated 1.4.277 runtime with a stub `claude` (2026-11-28):

- After the reboot orcad keeps each tab in its workspace session (`session.tabs.listAll`: `status: "pending-handle"`, no
  terminal, the provider session Orca's own Claude hooks reported), but `terminal.list` lists none of them, and nothing
  starts on its own. A tab comes back when a client opens it (`session.tabs.activate`; the desktop app does it for the tab
  it shows); a headless orcad with no client attached brings nothing back.
- Orca then resumes the Agent with its own command, built from the provider session its hooks reported:
  `claude --plugin-dir <Orca's agent view plugin> --resume <id>`, in the session's directory, without the start's
  environment. Persona's launcher (a gateway's credentials), `--settings`, `--mcp-config` and hooks are gone, as with a
  bare herdr resume. Without Orca's Claude hooks in Claude Code's settings (Orca installs them when it finds `claude`),
  Orca knows no session to resume, and a tab Persona opened (no `launchAgent`) should open as a plain shell (read from
  Orca's bundle, not checked live).
- `terminal.create` takes `launchAgent`, `launchToken` and `launchConfig {agentCommand, agentArgs, agentEnv}`, and Orca's
  resume builder would run `<agentCommand> --resume <id>` from such a config. It uses only a launch config Orca holds as
  its own launch evidence, which a create over the local socket never gets: a start typed after a create with those fields,
  a start Orca typed itself (`command`), and either with `ORCA_AGENT_LAUNCH_TOKEN` in `env` were all resumed with Orca's
  bare command, and no resume record with the config was kept. `resumeProviderSession`, `resumeOrigin: "saved"` and
  `spawnIntent` ask Orca to resume a session now; none sets the command a later restore uses. `launchAgent` would also
  hand the terminal Orca's Claude account environment, so Persona sends none of these fields.

So the node keeps no resume command or launch record for Orca panes (`holdResume` is herdr's), and shows what happened instead:

- Before the tab is opened again, a session Persona started in it is `interrupted` (above: a recorded pane, started before
  this node, no live Agent) with Continue, which resumes it on its launcher in a new Orca terminal (`docs/protocol/agent-states.md`).
- Once Orca has brought it back with its own command, it is listed in that tab with `off_launcher` (the scan of running
  claudes sees no Persona `--mcp-config`); a switch to the launcher it was started on quits it and starts it again there, in
  the same terminal, with the next message (#17040, `docs/protocol/protocol.md`, "Switching a running Agent to another provider").
- Continue while Orca still holds the old tab: from Orca's bundle (not checked live), Orca does not resume a provider
  session that runs in another pane it knows of, so opening the old tab afterwards should not start a second Claude on it.

## When Orca is not running

Audience: everyone with Orca enabled on a computer (#17037). Orca is a desktop app the user opens and quits; Persona cannot start it.

- **What the node finds**: Orca's row of `node.info`'s engines has a `state` (`proto.EngineState`): `running` (its runtime answers
  `status.get` with a protocol Persona speaks; available), `stopped` (it does not answer, but it is installed: its app is where Orca
  itself looks for it, `/Applications/Orca.app` or `~/Applications/Orca.app`, `Orca.exe` under `%LOCALAPPDATA%\Programs\Orca` or
  `%ProgramFiles%\Orca`, `orca-ide` in `~/.local/bin`, `/usr/local/bin`, `/usr/bin` or `/opt/Orca`, or a data directory a runtime
  used has its `orca-runtime.json` or CLI shim: `orcart.Installed`; PATH's `orca` is not read, it is often GNOME's screen reader),
  `incompatible` (it answers with another protocol: its `version`, and the note `node-engine.orca_incompatible` the hub words in each
  reader's language) or `missing`. A stopped Orca keeps the version the macOS app's `Info.plist` says.
- **Kept current**: while the node is connected it asks Orca again every 10 seconds (`orcaEngineEvery`: one `status.get` on the local
  socket, or only a look for the metadata file when there is none) and sends `engines.changed` when Orca's row changed
  (`node/orcaengine.go`); the manager puts the engines on the machine and the apps get it as on any change
  (`devices/engines_changed.go`). So opening or quitting Orca reaches the engine pickers, the web's Orca worktree actions and the
  notice below within about 10 seconds, without the node reconnecting. Older nodes send no `state` and no `engines.changed`.
- **The notice**: the Agents list shows "Orca isn't running on <computer>" with what to do ("Open Orca on that computer…") for an
  online computer whose engine set holds Orca, default or not, and whose node says `stopped`; never for an incompatible or missing
  Orca, or a node too old to say (web `HerdrNotices.tsx` with `lib/engines.ts` `orcaStopped`; Android `OrcaNotice` with
  `Engines.orcaStopped`; iOS in #17056). It has no button: Orca is opened on the computer. It needs no polling of its own. The herdr
  notice keys on the engine set too: herdr enabled beside an Orca default still offers Start.
- **The pickers**: an installed Orca that is not running reads "Installed, not running" (with its version), an incompatible one
  "Not compatible · <version>" with the note under it, instead of "Not detected".
- **A start Orca cannot take** is refused `engine_missing` naming Orca, with `engine: "orca"` and `reason` `not_running` ("Open Orca on
  it and try again"), `not_installed` (install it, or choose another engine) or `incompatible` (update Orca and Persona); the hub words
  it in the reader's language (`rpc-errors.engine_<reason>`), and Orca's own error is the detail, never herdr's
  (`node/orcaengine.go` `orcaMissing`). Nothing is created or typed.
- **Doctor and health**: `personad node doctor` has an `orca` line where Orca is installed (running is OK with its version; stopped or
  incompatible is a warning saying what to do); a machine without Orca gets no line. The machine page's "Terminal and engine" stands on
  herdr's line where herdr is enabled and on Orca's where Orca is, so an Orca-only computer is not reported as having its engine off
  (web `lib/node-health.ts`, Android `NodeHealth.kt`).

## Refusals in the reader's language

Audience: everyone using Persona in English or Japanese with an Orca computer (#17057). Every refusal and error an Orca path returns
carries a stable code, and the data the hub words it by (`server/internal/apierr`: `rpcFailureKeys`, `reasonKeys`,
`engineMissingKeys`, `notDeliveredKeys`, `bridgeReasonKeys`) in each request's language (`rpc-errors.*` in
`server/internal/i18n/locales`); the engine's or Orca's own words are the `detail`. The node's message stays what it was, for older
hubs and logs (`docs/protocol/rolling-upgrade-compatibility.md`, "Orca refusals in the reader's language").

| Code | Data `reason` | When |
| --- | --- | --- |
| `orca_not_registered`, `orca_workspace_unavailable` | | a start Orca could not place: no project open, or the chosen workspace gone (`node/hostorca_launch.go`) |
| `runtime_session` | | a resume of a session another app's runtime owns, an Orca chat's (`node/control.go`) |
| `codex_launch_preferences` | `unreadable`, `custom`, `shell`, `quoting` | Orca's Codex launch settings a stock Codex start cannot follow (`node/codex_orca_launch_defaults.go`) |
| `engine_missing` | `not_running`, `not_installed`, `incompatible`, `unavailable`, `start_failed`, `resume_unavailable`, `not_enabled`, `none` | the engine could not take the start or resume; `engine` names it, except for `none` (no engine turned on) |
| `not_running` | `engine`, `directory_gone`, `resume_timeout`, `agent_exited` | a message to a session that is not running (`node/sendsafe.go`, `node/pi.go`) |
| `not_delivered` | `terminal_refused`, `terminal_finished`, `screen_unreadable` | nothing was typed |
| `agent_busy` | `terminal_changed` | a Close of a stopped Agent whose terminal changed (`node/orca_terminal_close.go`) |
| `terminal_open_failed`, `tab_close_failed`, `pane_close_failed`, `pane_split_failed` | | the engine would not open, close or split; its words in the summary (`detail`) |
| `tab_name_invalid`, `tab_gone`, `pane_gone` | | the terminal browser's checks (`node/panes_actions.go`) |
| `pane_has_agent`, `pane_not_at_prompt`, `pane_foreground_unknown`, `pane_directory_unknown` | `unreadable` (the last but one) | Run Agent Here's checks |
| `close_not_in_terminal` | | a Close of an Agent that runs in no terminal of the computer |
| `unsupported` | `engine_unsupported`, the bridge's `orca_*` | a capability the engine lacks (the words stay `unsupported: <engine> cannot do this`), or an Orca chat's |

The New worktree dialog's refusals (`worktree-composer.*`) are worded by the node itself in the request's language, which the hub sends
as `locale`.

## When an engine cannot be selected

- **"Not detected"**: engine is not running on that computer. Start `herdr` once in a terminal; open Herdr App / Orca. After a short wait,
  or Reconnect for that machine, detection refreshes. An installed Orca that is closed reads "Installed, not running" instead, and
  follows Orca opening and quitting within about 10 seconds ("When Orca is not running").
- **"herdr + Herdr App" not detected**: both runtimes are not running (herdr started in terminal, Herdr App open with remote connected to this machine).
  If only one is used, select it directly.
- **Orca says "Not compatible" with its version**: Orca / personad versions mismatch; update both (the note under it says so in the
  reader's language).
- **Paseo not detected**: open Paseo desktop app or run `paseo daemon start`. For a custom home, set personad `PASEO_HOME`.
  If available but a connection failure appears below, see `docs/integrations/paseo.md` ("Troubleshooting").
- **Engine selected, no sessions**: verify Claude actually runs in that engine; ordinary-terminal / other-app sessions appear only Not running.
- **Old node on hub machine** (version mismatch in machine info): old node does not understand switching and uses only herdr; update personad on that computer.

### Botmux display preference

There are two independent switches. A computer's **Also show botmux sessions**
(`GET/PUT /api/v1/machines/{mid}/botmux`) controls which sessions its node lists to
Persona. **Show botmux sessions in Agents**, in Web Settings › Agents,
is the paired hub's shared display preference (`agents.show_botmux`, kept by the
default manager). It defaults to on. Turning it off excludes botmux rows and
counts from both complete and paged Web Agents lists; the Lark group quick filter
is hidden when empty. It does not change node inventory, to-do attribution or
dispatch. Turn it back on from the same settings row.

A browser's old `persona.agentHideBotmux=1` is migrated only while the shared
preference is unset. The conditional write cannot replace an explicit choice,
even from another device at the same time. The browser removes the legacy key
after a confirmed stored value; failures retain it for retry. Reconnect and the
next Agents list refresh read the shared value again. Android's use of this
contract is tracked separately in #10242.
