# Codex support

Agent renames use `thread/name/set` on a connected app-server for that Codex home, or `/rename` in a live terminal.
They never write Claude's `custom-title` records into a Codex rollout. Worktree display names are separate host metadata.

Codex (`codex` CLI) running in herdr appears in the "Agents" tab like Claude: lists, chats, live state, messaging, terminals, and closing all work,
and it can also be started from the phone ("Starting from the phone"). Supported since 2026-10-26; `server/internal/node/codex.go` + `server/internal/transcript/codex.go`.

## Standalone TUI takeover

A takeover succeeds only after the same thread is loaded and joined on the shared app-server. A transient input box during TUI startup is not sufficient: folder-trust and update dialogs can appear later. The takeover keeps applying the existing start-readiness checks until shared ownership is confirmed, or returns an explicit not-ready error.

Eligible sessions advertise `capabilities.takeover: {running, can_resume, reason?, app_owned?}`, where `reason` is `encrypted_context`, `app_running`, or `unavailable`. A signed-in user on a full device can use the manager's `GET` then `POST /api/v1/machines/{mid}/codex/{sid}/takeover` flow; generic device-call APIs and assistant tools cannot invoke it. GET returns `{token, cwd, transcript_path, continuation_prompt}`; the single-use token expires after two minutes. POST requires `confirm_stop: true` while a CLI writer is running or when taking over an idle, unowned Codex App thread (`app_owned: true`, confirming that the Codex App will no longer own the chat); when a CLI writer has exited, `false` is accepted.

The manager sends one Ctrl+D through the selected CLI session's terminal writer only after confirming the composer is empty, with no modal or queued input, and rechecking both process identity and terminal instance. Remote terminal-writer bridges are supported. It waits for the process to exit, the transcript to end on a complete stable line, and a fresh scan to find no writer. For an idle, unowned Codex App thread (no live App bus, no connected app-server owner, no live rollout holder, no running turn), it rechecks that the Codex App is not running and could not load the thread before launching `codex resume <thread>` under the same `CODEX_HOME`. Any failed check or timeout refuses takeover; no signal or forced termination is used. Takeover resumes the same thread and session ID in a new terminal tab of the start's terminal engine, else the first other one the computer has (herdr, Herdr App, Orca, Tern; Orca since #17044, so an Orca-only computer can take over too). The separate continue option starts an independent session with context and the attached to-do preserved. `encrypted_context` and `app_running` are continuation-only.

CLI takeover also supports non-default Codex homes: the new terminal runs `CODEX_HOME=<home> codex ... resume <thread>` with the original thread ID, and the session keeps its opaque list ID.
Sidebar-listed threads with the shared app-server stamp (#10438, #14492) follow Codex App takeover rules (`app_owned: true`, confirmation required, continuation only while the App runs) and have no ordinary terminal resume.

## Recognition

- herdr reports Codex panes as `agent: "codex"`. With herdr's Codex integration installed (`~/.codex/herdr-agent-state.sh`, registered on
  SessionStart in `~/.codex/hooks.json`), `agent_session.value` is the Codex session id.
- Panes without a session id (started before the integration was installed, or herdr recorded the id only at startup and `/resume` later switched threads in Codex):
  Codex writes the thread name in the terminal title ("thread name | directory", either part possibly truncated to `...`). Find a same-named thread in `session_index.jsonl` (`codexByTitle`);
  a unique match or the matching id recorded by the host or by Persona identifies the thread. A manually opened Codex in herdr or Herdr App with no established identity gets a waiting row of its own.
  A shared directory or an ambiguous title gives no evidence of which conversation that pane is running. Its earlier conversations keep their history and last-active times.
- **Sub-Agent rollouts are not sessions**: Codex thread_spawn sub-Agents (and Guardian approval reviews, Codex's and the fork's, below) each write their own rollout,
  in the same directory and cwd as the main chat, with newer writes. Identify them from `session_meta` only: `thread_source` is `subagent`, or `source` is `{"subagent": …}`.
  Their parent chat is `session_id` (fall back to `parent_thread_id`). Skip them when guessing sessions; if herdr reports a sub-Agent id, replace it with the main chat's
  (`rolloutMeta`, `mainThread`). A scan of 435 Codex rollouts on a Mac on 2026-11-01 found 203 sub-Agents, all with `thread_source` and `session_id`.
- Rollouts live in the directory for the **day the thread started** (`sessions/YYYY/MM/DD`) and keep being appended there: a thread started six weeks ago may still be used today. When listing rollouts, scan directories from the last two weeks
  every 5 seconds and older directories every minute, retaining only files written within two weeks (`rollouts`).
- The node indexes each Codex home separately. A folder permission prompt or an unresponsive mount can delay that
  home's history. Other homes, terminal Agents and runtimes continue to appear. Previously observed threads in a
  delayed home retain their titles and identities and show an unknown status until the scan finishes. If all 16
  filesystem workers are busy, a new home's history first appears after a worker becomes available (#13812).
  A terminal start also checks available homes for its first message and retries homes whose scans are still pending.
  Herdr reports a raw thread ID. The node uses the pane process's observed home, or verifies a matching rollout
  before binding the pane. A slow history or title lookup is retried on subsequent refreshes while other panes and runtimes remain visible.
- herdr's terminal title "dots | codex | Ready" is not a usable chat title. Use Codex's own thread name (`thread_name` in `~/.codex/session_index.jsonl`,
  taking the last entry for a given id), or the first user message if absent.
- `proto.Session.agent` = `codex`. App list rows and space-page cards show an Agent tag (the same small outlined `AgentTag` for Claude and Codex); the chat subtitle includes the Agent name and the input says "Message Codex".
  Share cards are attributed to Codex (dark icon).

## Session history (rollout)

`~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<session id>.jsonl`, one `{timestamp, type, payload}` per line. The first line is `session_meta`
(including cwd and about 20 KB of system instructions). When `transcript.ParseLine` sees a line with `payload`, it parses it as Codex (`Meta.Codex`), without a separate file type.

Continuation filenames may append `_<segment UUID>` after the thread UUID. The authoritative identity is `session_meta.payload.id`; segment IDs do not create new sessions. Discovery and open-file matching use that identity and follow the newest rollout for the thread, as for resumed transcript copies. Genuine subagents retain their own metadata IDs and parent links. Incomplete continuation headers are retried rather than advertised under a guessed ID. Original files remain unchanged; older and archived rollouts remain addressable by the thread ID. This selects the current rollout rather than concatenating potentially superseded histories.

| Rollout | In the chat |
| --- | --- |
| `event_msg/item_completed` UserMessage | User message |
| AgentMessage `phase=commentary` | Assistant text (narration between tools) |
| AgentMessage `phase=final_answer` | Reply |
| Reasoning (with `summary_text`) | Thinking |
| CommandExecution | `Bash` (strip the `/bin/zsh -lc` wrapper), result is `aggregated_output`, nonzero exit code marks failure |
| FileChange | One `Edit` per file, input includes `diff` (unified diff, rendered by the App's `UnifiedDiffView`) |
| McpToolCall | `mcp__<server>__<tool>` |
| ImageView | `Read` |
| Extension `web.search` | `WebSearch` / `WebFetch` (below) |
| Extension `clock.sleep` | `sleep`, input `duration` (`25s`, `1m30s`) and `duration_ms`; a run of sleeps folds with the work around it |
| SubAgentActivity (`started` / `interacted` / `completed`) | `subagent`, input `name` (the last segment of `agent_path`), `event`, `agent_path`, `agent_thread_id`, and on `started` the `model` and `reasoning_effort` Codex gave the subagent; the activity line follows `event` (dispatching, messaging, finished) |
| CollabAgentToolCall | `wait_agent` for `wait`, otherwise the record's `tool`; input `receiver_thread_ids` / `receiver_agents`, result `agents_states`, `status: failed` marks failure; the `wait` records seen so far carry none of the three, so a wait shows without a subject or a count |
| ContextCompaction | "Context compacted" notice |
| `event_msg/turn_aborted` | "Interrupted" notice |
| `turn_context` / `token_count` | Model, approval policy, context tokens |

Commentary is ordinary assistant text on the wire, so it can answer a mid-turn question in the latest-text slot. Latest-page folding keeps the newest commentary visible until a final answer follows it; earlier commentary and commentary on historical pages fold into completed work through an internal-only narration marker. It is not counted as thinking or exposed as another item kind. The iOS `ChatCache` version changes with this kind mapping so cached thinking items cannot hide the answers.

Codex's own XML in UserMessage (all rollouts on the local and hub machines scanned 2026-10-26):
`<send_user_message_question_reply>` (answers to Codex `request_user_input`, containing JSON) keeps the answer in `text`, with one "question: answer" line per question for multiple answers. A standalone envelope also carries `question_replies` so HarmonyOS can show each original question as a collapsible quotation. For a single reply the answer comes from the existing `text` field.
strip `<in-app-browser-context>` (browser state automatically attached by the Codex App); `local_image` content becomes `<attached_images>` paths, displayed as thumbnails.
`<environment_context>`, `<INSTRUCTIONS>`, `<skill>`, `<turn_aborted>`, `<subagent_notification>`, `<realtime_delegation>`,
and `<codex_internal_context>` appear only in response_item (model input), not in the chat.

`response_item` is the model wire format for the same content and is skipped. Extension `web.search` displays as `WebSearch` (search, query input) / `WebFetch` (`action.type=openPage`, URL input), with one "title  URL" per result. A coordinating chat's `clock.sleep`, SubAgentActivity and CollabAgentToolCall are finished tool calls, not notices (#10436): the apps draw them as ordinary calls ("Call subagent · review_api") and they fold into the turn's work, so a chat that delegates shows its prompt, one folded work row and its reply rather than a line per sleep or subagent ping. The subagents themselves are their own threads, listed under the chat. Other Extensions (image generation) and item types Persona does not know remain recognizable event notices; the original session history is still authoritative for full content.

The Persona assistant ("Persona" tab) can also use Codex launchers through `codex app-server`; see `docs/assistant/assistant.md` ("Codex").

Multiple existing app-server instances on one computer (desktop App, separate `CODEX_HOME`s, multiple clients reading / writing / approving) are integrated into the existing Agents list. See [`codex-app-server.md`](codex-app-server.md) for how that works and its limits; the source research behind it is in [the archive](../archive/integrations/codex-app-server-research.md). Discovering history requires no herdr; only instances exposing a protected Unix / loopback WebSocket endpoint support live connection and control. External stdio is explicitly read-only.

## Visualize

Codex's visualize plugin asks the model to write a self-contained HTML fragment (no network access of its own: no `fetch` / XHR, everything inlined, under 1 MB) and marks it inside an AgentMessage's text with private-use runes:

`<U+E200>visualize<U+E202>{"path":"<abs>.html","mode":"wide"?,"title"?}<U+E201>`

(`U+E200` opens, `U+E202` separates the tag from the JSON payload, `U+E201` closes.) The files live under `$CODEX_HOME/visualizations/YYYY/MM/DD/<codex session id>/`; `mode` is absent (the Codex client's 736 px frame) or `"wide"` (1024 px), and `title` is optional. The Codex client renders the fragment inline where the marker stood; without handling it, a reader sees only the file path as text.

Persona's transcript parser (`server/internal/transcript/codex_visualize.go`) splits each marker out of the AgentMessage text: the words before, between and after the markers stay one or more ordinary `text` items, and each well-formed marker becomes its own `visualization` item (`proto.Visualization{path, mode, title}`), in order. A marker is kept verbatim as text when its JSON is invalid, its path is relative, its mode is unknown, its title is overlong, or it is not closed; blank lines around a marker are collapsed. The item never carries the fragment's bytes, only the file reference.

Only the session's own completed AgentMessages are scanned. Markers also appear verbatim where one thread reads another's history — inside `codex_app` `read_thread` DynamicToolCall output, `custom_tool_call_output` snapshots and a compacted rollout's `guardian_history` — but those stay part of the tool result: they reference another session's files and repeat on every read, so re-emitting them would duplicate items the owning thread already produced.

The node serves the bytes through `session.visualization`, and the hub through `GET /api/v1/sessions/{mid}/{sid}/items/{iid}/visualization` (`docs/protocol/protocol.md`, "Sessions"). The node resolves symlinks and only returns a file that:

- is inside `$CODEX_HOME/visualizations`, with the fixed `YYYY/MM/DD/<session id>/<file>` shape;
- belongs to the requested session: the directory's id is the session id or its main thread's;
- is a regular file of at most 1 MiB (`visualizationMaxBytes`).

Anything else answers `not_found`, so a marker cannot name an arbitrary file on the computer. The hub marks the response `text/html; charset=utf-8` and privately cacheable.

The web client renders the item with `views/chat/VisualizationCard.tsx`: it fetches the fragment once when scrolled on screen and runs it in the same offline QuickJS sandbox as assistant HTML cards (`web/src/lib/html-runtime`, an `allow-scripts`-only iframe, no same-origin access), with every `persona.*` bridge call refused. The frame follows the fragment's height up to 640 px and is torn down off screen; a wide card may take 1024 px, another 736 px. Fetch and runtime failures show the file name and Retry. A Markdown export writes a `_[Visualization: <title or file name>]_` placeholder because the interactive frame has no Markdown form.

## Sessions on the daemon (app-server route)

Rewind metadata preserves native terminal sessions' read, send, interrupt, approval and terminal capabilities.
An existing explicit capability object retains its operation restrictions when the node adds rewind flags.

Since 2026-11-02, for Codex panes running on a shared app-server daemon, Persona joins their threads as a second client. Questions, approvals, and messaging use the protocol,
not screen reading or typing into the terminal. Panes it cannot join retain the terminal route under "Questions and approvals" below (#214).

- **Why joining works**: since 0.163, bare `codex` automatically starts or joins the shared daemon (`<CODEX_HOME>/app-server-control/app-server-control.sock`).
  The TUI is just one client. Server requests for a thread (approvals, `request_user_input`) go to every subscriber; the first answer wins, then everyone gets
  `serverRequest/resolved` and removes their prompts (`docs/integrations/codex-app-server.md`).
- **Finding the daemon**: the node already discovers each home's official control socket and app-server processes with `--listen`, opening one WebSocket per instance
  (`codex_remote.go`). The 0.163 control socket is an alias: a symlink to `<canonical /tmp>/codex-daemon-<uid>/<SHA-256 of the alias's canonical path>`
  (Codex's `uds/src/daemon_directory.rs`, `unix_socket.rs`). Previously, "not a socket / is a symlink" was always rejected, so it never connected.
  Now follow only the exact target Codex itself computes (the alias directory must belong to the current user and be unwritable by others; target directory 0700, socket 0600). Other symlinks remain rejected (`codexSocketPath`).
- **Starting with an explicit effort**: when the requested effort differs from the effective configuration, Persona creates a thread on the launcher's shared daemon before opening the pane. A new paginated thread may defer writing its rollout until the first turn. Persona asks Codex to materialize the rollout through `thread/read` with `includeTurns` before it applies the effort with `thread/settings/update` and checks the effective setting. A server that supports only the start configuration can confirm the effort in its `thread/start` reply. An ambiguous creation error stops the start because a thread may already exist. The pane resumes that thread, and Persona sends the first message through `turn/start` once the composer is ready. An empty start opens the configured thread without sending a turn. Both retain structured approvals, questions, and model and effort controls. If the TUI refuses to resume, Persona reports its error and discards the unused preparation.
- **Which panes use it**: if the thread id identified by the node for a live Codex pane in herdr (`agent_session` / thread name / rollout, see "Recognition") appears in a connected
  app-server's `thread/loaded/list`, that app-server runs the thread (only one writer per thread); the TUI in the pane is its client.
  The node also joins loaded threads that are working or waiting for approval or input, including external terminals and subagents. It checks every second and
  joins in the background with `thread/resume` (only `threadId` and `excludeTurns`, no model, directory, sandbox, or instructions, changing no session settings;
  app-server also ignores overrides when subscribers already exist). Failed joins retry after 30 seconds, with the reason recorded on the session (`codex_daemon.go`).
- **Once connected**:
  - Approvals (commands, file changes, permissions, MCP tools) and Plan-mode `request_user_input` are app-server requests, converted to the same cards as the assistant's
    (with `agent: "codex"`, Codex's reason, and "Always Approve" = this session: `acceptForSession` / permission `scope: session`). Answers return through the protocol.
    If a reason accompanies a denial, return `decline`, then inject the reason as a message into this turn (`turn/steer`, like the assistant's `tellModel`).
  - Async questions (`request_user_input_async`) are messages, not requests. Recognize them from rollout as before ("Questions and approvals"); send the answer envelope as an app-server message (`turn/start` / `turn/steer`).
  - Phone messages use `turn/start` when idle or `turn/steer` with the current turn while working (for a turn already running before connection, read it once with `thread/read`).
    No terminal typing: computer input drafts stay untouched, neither swallowed nor mixed into the message.
  - `/compact` alone (spaces around it allowed, no photos) compacts the thread with `thread/compact/start`, the call the TUI's own `/compact` makes (#10188, `node/codex_remote_compact.go`).
    The app-server runs no slash commands: sent as a turn, `/compact` reached the model as words. Codex writes the rollout's `compacted` line and its `ContextCompaction` item, so the session shows
    the same "Context compacted" notice as a terminal compaction and no user message. While a turn runs it is refused with `codex_compact_busy`, nothing sent and nothing steered: Codex's compaction
    replaces a running turn instead of waiting for it, and the TUI refuses `/compact` mid-turn too. A thread the node last saw resting is read again (`thread/read`) just before, so a turn
    begun at the computer since is not cut short; an app-server without the method refuses it (`codex_refused`). Anything more (`/compact now`, a photo) is a message as before. Other slash
    commands still go to the model as text; panes on the terminal route type `/compact` into the TUI as before.
  - The thread's turn on the app-server counts over herdr's state: a pane herdr calls done / idle / unknown is listed Working while the app-server runs a turn in it,
    from the reply to `turn/start` on (#4865, `docs/protocol/agent-states.md`, "Codex").
  - Thinking effort can be changed on the phone (permission mode cannot); see "Thinking effort" below.
  - Computer answers first: `serverRequest/resolved` removes the card (`interaction.dismiss`); a later phone answer does nothing. Phone answers first: the TUI prompt closes.
    Simultaneous answers: app-server accepts only the first, and runs the command only once. Observers never auto-answer (`codex_remote_asks.go`).
  - Disable the terminal route for these panes: do not inspect their screens for approvals or recognize blocking questions from their rollout. One question must not produce two cards or two answers.
- **Panes that cannot join**: launches with `-c` / `--profile` and similar arguments (0.163 runs in its own process rather than the daemon in this case), already-running non-daemon sessions, 0.153 (auto-connects only
  if the socket already exists), and TRAE CLI retain the terminal route.
- **Daemon restart, disconnect, or version change**: disconnect clears join state, and these panes immediately fall back to the terminal route (screen cards, typing), with existing cards removed. The TUI displays
  "Reconnecting to server…", reconnects when the new daemon starts, and reloads the thread. The node reconnects to the new daemon every 2–5 seconds and rejoins; pending requests replay as new cards.
  Persona does not block the TUI: requests also go to it, so users can answer on the computer at any time.
- **Visible routing**: sessions carry `codex_route` (`app_server` / `terminal`) and `codex_route_note` (why terminal). The App's "Session Info" shows "Connection: app-server (structured)"
  or "Terminal (screen reading)" plus the reason. Node logs record every successful / failed join.
- **Tests**: `node/codex_daemon_test.go` (auto-join on a synthetic app-server, messages, async answers, approval cards, denial reasons, join retry, socket alias), `node/codex_remote_compact_test.go` (`/compact`);
  real Codex: `scripts/test/test-codex.sh` with `PERSONA_CODEX_DAEMON_BINS` (`docs/operations/testing.md`, "Codex integration"), starts a test-only `codex app-server --listen unix://…` in isolated herdr
  (does not touch the active daemon; node discovery is disabled and recognizes only this one), with the TUI connected using `--remote`.

## Thinking effort

Since 2026-11-04 (#1709), Codex sessions controlled by the phone through app-server can have their thinking effort changed on the phone: send-capable "Codex live" sessions in Agents
(`source_kind: codex_app_server`), and herdr panes already joined to the shared daemon ("Sessions on the daemon" above, "Connection: app-server").
Messages to them already use `turn/start` / `turn/steer`; thinking effort is the thread's own setting, requiring no terminal typing (`node/codex_effort.go`).

Assistant conversations on a custom Codex launcher also accept Codex's native levels, including `minimal` and `ultra`.
Both the first-message selection and a configured effort are checked against the agent that the launcher actually runs,
not its command name; Claude launchers still reject Codex-only levels (`node/assistant_codex_alias_effort_test.go`).

- **Unchanged**: permission mode still cannot be changed on the phone (Codex approval policy and sandbox remain determined by launch arguments and `config.toml`; requests with permission mode return
  "Only Codex thinking effort can be changed on the phone; change permission mode on the computer"). Sub-Agents
  and TRAE CLI cannot even change thinking effort, with existing prompts unchanged; panes on the terminal route change it, and their model, through Codex's own `/model` ("On the terminal route" below). Codex desktop App chats change it through the App (#5575, "Sessions in the Codex desktop App" below). For a new task, a purpose or preset can supply the first turn’s effort (see "Starting from the phone").
- **Available levels**: `supportedReasoningEfforts` reported by this thread's model in app-server `model/list` (read once per connection with `includeHidden`,
  since the thread may use a hidden model). If the model is absent or the list cannot be read, use the same set as the "Codex Settings" page: minimal / low / medium / high / xhigh / max / ultra.
  An explicitly empty list means unsupported: neither show nor accept it. The session includes `efforts` (levels from weakest to strongest) and `effort` (current value); clients show the selector only when `efforts` is nonempty
  (`docs/protocol/protocol.md`, "Codex sources and capabilities"). The node validates levels itself: observed 0.163 `thread/settings/update` accepts any string.
- **Current value**: the value app-server says is configured for this thread (`reasoningEffort` from `thread/read` every 5 seconds, the join-time `thread/resume` reply,
  and `thread/settings/updated` notifications). Otherwise use the latest rollout turn's `turn_context.effort`, including
  terminal-only and history-only sessions. A model's default (`defaultReasoningEffort` in `model/list`) is only a fallback
  for a controlled thread with no recorded effort. The computer-wide `config.toml` is not session evidence.
  The incremental metadata scan retains the latest effort even when long tool output pushes its turn context outside the quick tail.
  Changes in the computer TUI update the phone. Readback does not grant control: unsupported sessions still offer no effort choices.
- **Changing it**: after validation, the node sends `thread/settings/update {threadId, effort}`. It applies from the next turn, whether sent from phone or computer; the TUI also receives
  `thread/settings/updated`. It can be changed while the current turn runs, without affecting that turn. In 0.163 this method is experimental (the node already declares `experimentalApi` when connecting).
  Observed: successful call, immediate `thread/settings/updated`, new value read back with `thread/read`.
- **App-server lacks the method**: recognize only JSON-RPC `-32601` (method not found). Store the selection temporarily in node memory (`sessionModes.Codex`),
  include `effort` on the phone's next `turn/start` (its effort applies to this and later turns), then clear it after one send. The session displays this selection meanwhile.
  `turn/steer` (interjecting during a running turn) cannot carry effort, so wait for the next `turn/start`. The selection is valid only for that thread on that connection:
  a disconnect/reconnect (possibly a newer version now supporting the method, or a computer-side effort change), or the thread being unloaded in app-server (session ended there),
  invalidates it. Do not send it; show the thread's own value and require another phone selection. Also discard it if Codex rejects the `turn/start` carrying it, avoiding failure of every later message.
  A node restart loses unsent selections (a restart is a new connection); successful `thread/settings/update` values reside in app-server and are not lost.
- **All other failures return errors without recording a selection**: also clear any previous unsent selection, so a later `turn/start` does not carry a value the phone has already been told failed to change. Pass through other Codex errors
  (-32602 invalid arguments, missing thread, etc.); report "Disconnected" on disconnect; after 15 seconds without a reply, report "May not have changed" (the request may already have reached app-server;
  session display follows its next read).
- **Clients**: web, iOS, and Android show only thinking effort for send-capable sessions with nonempty `efforts`: the chip below the web input, the label above the old iOS input and new shell's "Model · Thinking effort"
  menu, Android's "Mode" button. Options come directly from `efforts` (minimal "Minimal", ultra "Ultra"; unknown values shown as-is). Claude Code and the assistant still have both settings.
  On the web, the current Codex model row remains actionable for its effort flyout even when changing the model itself is unsupported.
  The panel preserves recorded effort even if the launcher's catalog omits that level; it never replaces it with the catalog default.
  Offline, read-only, parent-controlled, terminal-only, and non-adjustable-model states explain why effort cannot change here.
  Supported changes explain that they apply on the next turn without interrupting the current one.
- **Tests**: `node/codex_effort_test.go` (synthetic app-server: rejected before joining; after joining accepts only effort; invalid levels; `thread/settings/update` parameters;
  -32601 carries effort on next `turn/start` but not `turn/steer`; other errors / timeout / disconnect report failure, record nothing, and clear previous selections; rejected `turn/start` discards the selection;
  changed connection or unloaded thread invalidates it; model levels and defaults; Codex rejection; model without levels; `thread/settings/updated`; a terminal-route session an older Codex started, and TRAE CLI, still rejected);
  web `session-actions.test.ts`; iOS `SyncEventTests.codexThreadOnItsAppServerSetsItsEffortOnly`; Android `SessionSourceTest`, `SessionModesTest`.
  Real Codex was manually checked only for `model/list`, `thread/settings/update`, and `thread/read` on a stdio app-server with temporary `CODEX_HOME` (no model run).
  `scripts/test/test-codex.sh` does not yet cover this.

### On the terminal route (#12572)

A Codex in a herdr or Orca pane that runs in its own process (a launcher with `-c` or `--no-daemon`, "Starting from the phone") or is not joined
on an app-server yet gets its messages typed into its box, and since Codex 0.169 its model and thinking effort too. Codex's own `/model` takes the
choice as arguments, `/model <model> --reasoning=<level>` (either part may be left out), and applies it to this session only, from the next turn,
mid-turn too; `config.toml` stays as it is (`node/codex_terminal_modes.go`).

- **Row**: `capabilities.model: true`, plus the current model, effort and speed. Before the rollout records its settings, the node uses the launch
  selections, launcher arguments and Codex configuration from the home that owns the rollout. The model catalog supplies defaults when available.
  That home's `models_cache.json` gives the model's effort levels (`supported_reasoning_levels`) and speed choices. The row uses Codex's whole
  effort list for an unlisted model and an empty list for a model with no effort choices. Speed choices require tiers in the catalog.
- **Version check** uses the running Codex (#13290). The node requires a rollout and reads the pane process's executable version
  (`<exe> --version`, cached by executable path and modification time). If that version is unavailable, it uses the rollout's first
  `session_meta.cli_version`. The effective version must be 0.169 or later for the row to offer controls and for settings requests to type
  anything. An older session resumed by a supported binary gains the controls. A supported session resumed by an older binary loses them.
  Sub-Agents, botmux panes, TRAE CLI and sessions with uncertain ownership are excluded.
- The phone reads the named launcher's catalog when the session reports one. A manually opened Codex pane with no known launcher
  gets the machine's Codex catalog. A pick changes the running session's model within its current provider. The gateway determines which
  model IDs it will serve.
- **Changing it**: one `/model` line per request, after the checks a message gets (a draft in the box or a dialog on the screen refuses, nothing
  typed); the session's typing lock is held only while typing. A model alone keeps the current effort when the new model takes it (Codex would put
  the new model on its default level), as the app-server route does, and a pick equal to what the rollout shows types nothing. The change counts
  as made only when the rollout records it: Codex writes `thread_settings_applied` with the thread's `model` and `reasoning_effort` as soon as it
  applies them, between turns and mid-turn, and again at each turn's start. The transcript reader takes that event as it takes a `turn_context`,
  so the row shows the new values at once.
- **Speed** uses its own request. The node types `/model --speed=<tier>` with `standard` for Standard and `fast` for Fast. It reports success after
  a new rollout settings event confirms the requested tier. The request carries no model or effort change.
- **Refusals** (`session.modes` codes, worded in en / zh-Hans / ja): a permission mode, as on the app-server route; a level the model does not take;
  `modes_codex_version` (the effective version is older than 0.169 or unknown); `modes_codex_no_rollout` (no rollout yet);
  `modes_codex_refused` with Codex's own words (a model or level it does not have, Reserve, an unknown argument, `/model` disabled
  while a task runs; "still starting" is typed once more after 2 seconds, and a second "still starting" is passed on);
  `modes_codex_unconfirmed` (`delivered: "unknown"`) when the rollout does not show the change within 15 seconds. Nothing is typed again after
  it. Codex's words count only at the start of a screen line that is new since the typing (one more copy than before counts, as Codex prints a
  repeated refusal again), and a record in the rollout wins over them.
- **Observed** in a private pty, temporary `CODEX_HOME`, no model reachable: with Codex 0.169.1 mid-turn and 0.171.0-alpha.13 between turns,
  `Model changed to <model> <level> for this session only` on the screen and `thread_settings_applied` in the rollout at once; the model's
  default level for a model alone; `model` / `model_reasoning_effort` in `config.toml` unchanged. 0.171.0-alpha.13 was not tried mid-turn.
- **Tests**: `node/codex_terminal_modes_test.go` (the row by running binary and rollout version and cache; binary `--version` caching, mtime invalidation, and error fallback; the line typed for an effort, a model, both, mid-turn and an
  unchanged pick; every refusal, including a dialog on the screen, a botmux pane, a subagent and "still starting" twice; Codex's answers on the
  screen; the rollout's record), `transcript/codex_effort_observe_test.go`
  (`thread_settings_applied` in the parser and the incremental scan). `TestCodexTerminalLive` (`node/codex_terminal_modes_live_test.go`, opt-in
  with `PERSONA_LIVE_CODEX_TUI=<codex binary>`) runs a real Codex TUI in a private pty with a temporary home. The base configuration and
  a named profile point to distinct local test endpoints. Ordinary turns reach the profile's endpoint with the chosen model and effort
  before and after switching. The test also checks that the settings commands stay out of model input, an unavailable model is refused,
  and the base and profile keep their configured model and effort. The gateway's supported models depend on its own catalog.

## Gateway model catalog

A managed Codex provider (Settings › Models, agent `codex`) runs its codex with Persona's own model provider
(`docs/integrations/launchers.md`, "Provider model and gateway parameters (#2455)"). Codex knows nothing of a gateway's models from that: its picker
lists the OpenAI models it bundles, and a model only the gateway has runs on fallback metadata ("Model metadata for `…`
not found. Defaulting to fallback metadata"), with the wrong context window, compaction threshold and thinking levels.
Since #8797, when the gateway serves Codex's own catalog, the provider table carries `model_catalog_url` and codex reads
the gateway's models from it (`server/internal/node/codex_catalog.go`).

- **Codex's side** (checked against the 0.167.0 source and run): `model_catalog_url` is a key of the provider table.
  Codex asks `<url>?client_version=<major.minor.patch>` with the provider's key (`Authorization: Bearer`), within 5
  seconds and following no redirect, and reads `{"models":[…]}` of its own model entries. That catalog is then the only
  list it uses: one that fails, never answers or cannot be decoded leaves no list, and the start still runs, the model
  on fallback metadata. The key exists since 0.156; until 0.160 Codex reads it only with
  `[features] api_key_model_discovery = true`, stable and on by default since 0.161. Older versions ignore the key.
  A `model_catalog_json` file would have to hold every field Codex requires in its version, and one Codex cannot read
  stops it from starting; `model_context_window` changes one model's window, not the list or its levels. Neither is used.
- **Codex's cache** (`$CODEX_HOME/models_cache.json`, checked with codex-cli 0.167.0, two providers in one
  `CODEX_HOME`, #8814): one file for every provider in that home, holding the last list fetched and whose it was. Each
  fetch overwrites it, so a gateway's catalog and the user's own codex take turns in it; neither ever lists the other's
  models (an entry is used only by the provider that wrote it), each just fetches again. A catalog that fails is
  answered from it only when the file is that provider's, from the same Codex version and under 5 minutes old; else
  the list is empty.
- **Finding the catalog**: the node asks, when `personad launch` asks for the provider (`launcher.env`), what codex
  would: both `<base>/models` (Codex's own path for its backend's catalog; some gateways answer it in Codex's shape when
  `client_version` is given, with OpenAI's list otherwise) and `<base>/codex/models` (magpie's, beside an OpenAI
  `/v1/models`), at once, with the provider's key and proxy and this computer's `codex --version`, in 5 seconds in all.
  The first, in that order, whose answer is a catalog Codex can read is taken: a `models` list whose entries each have a
  unique slug and every field Codex 0.161–0.169 requires, of its JSON kind (`display_name`, `supported_reasoning_levels`
  with an `effort` and a `description` in each, `shell_type`, `visibility`, `supported_in_api`, `priority` (a whole
  number), `support_verbosity`, `truncation_policy` with a `mode` and a whole-number `limit`,
  `experimental_supported_tools`), one of them
  listed. The words inside (a level, a shell type) are left to the gateway, which was told the version. An entry with
  only some of them is none: codex-cli 0.167.0 fails such a catalog whole ("failed to decode models response"), lists
  nothing and runs the model on fallback metadata, worse than no catalog. OpenAI's `{"data":[…]}`, an error or a
  redirect is none too. Nothing depends on the gateway's name; the node's log names only the path below the base URL.
- **Never holding a start**: `launcher.env` waits for that whole lookup (finding codex, its version, the probe) at most
  2 seconds (`codexCatalogWait`, well under the 10 seconds `personad launch` waits for the node). Past it the provider's
  codex starts without a catalog, as before, and the lookup goes on: what it finds is kept for the next start. The
  answer, a catalog or none, is fresh for 10 minutes per gateway, key, proxy and Codex version; for a day after that it
  is still given at once while the gateway is asked again in the background. A probe that cannot tell (no answer in
  time, a body that does not arrive whole, a 5xx, 408 or 429) keeps the catalog found before and is tried again after a
  minute; only a gateway that answers without a catalog drops it. A codex that is not found, or whose `--version` fails
  or hangs (its run gets a second to give up the output after it is stopped), is looked for again after a minute, not on
  every start; a run its caller gave up on keeps nothing. A version read is fresh for 10 minutes, then still given at
  once while `codex --version` runs again in the background: a shim (volta, mise, asdf) stays the same file when the
  codex behind it is upgraded, and a slow one must not hold a start. A panic in the lookup's own goroutines is logged
  and finds no catalog; it does not end the node. One lookup runs at a time per gateway, key and proxy as codex will
  see them, the provider's own `env` included (a gateway set only there is looked up too).
- **Which codex**: the node checks the codex it finds, and `launcher.env` names it (`codex_catalog_codex`). The codex
  `personad launch` runs comes from the shell's `PATH`, which may name another install: then `personad launch` asks
  that one its version (within 5 seconds, after the node has answered, so not part of `LaunchNodeWait`) and passes no
  catalog to one before 0.161 or one that does not answer.
- **Exactly as before** without one: no gateway, no key (codex would not fetch it and would list nothing), a codex
  before 0.161 or a prerelease of it, arguments that choose their own provider or profile (the provider and everything
  in it are left out then), a base URL with a query, or a probe that found nothing. The arguments are then the same,
  byte for byte, and an older node or `personad` simply does not pass the fields (`codex_catalog_url`,
  `codex_catalog_codex`, node.sock only; nothing new on the hub–node wire). The probe runs on the node with the node's
  environment and the provider's: a proxy only the shell sets is not used for it, so such a computer may find no
  catalog its codex could read (it then starts as before); codex reads the catalog from the same gateway it talks to. The user's `~/.codex/config.toml` is never written.
- **The picker**: the node lists a managed Codex launcher's models by running `personad launch <name> app-server` and
  asking `model/list` (`node/launcher_models.go`). That is the same codex a start runs, with the same catalog, so the
  picker's models and levels are the ones codex is given. A model whose catalog entry has no levels offers none, and no
  default level when Codex reports that default as `none`. Before #8797 the node ran `personad launch <name>` alone, whose
  codex started its TUI and exited with "stdin is not a terminal", so the picker had no list for these launchers.
- **Tests**: `node/codex_catalog_test.go` (found at either path by shape, the first in order winning; OpenAI's list, an
  error, a redirect, a body that is no catalog and no answer in time are none; no probe for an old or prerelease codex,
  no codex, no key, the launcher's own provider or profile; kept per key; given at once when stale and asked again;
  kept through a probe that cannot tell, dropped by a gateway without one; a failing `codex --version` not rerun on
  every start; #8814: every required field of its kind, a cut-off body, `launcher.env`'s bounded wait for a slow gateway
  or `codex --version`, a `--version` whose child keeps the output open, a shim's upgrade read again in the background,
  a caller giving up, a missing codex, a gateway set in the provider's `env`, a panic in the lookup, the log line),
  `nodecmd` `TestLaunchWaitsLongerThanTheCatalogLookup`, `node/managed_test.go`
  `TestLaunchArgsCodexGateway` (the catalog goes in the provider table only; arguments unchanged without it),
  `node/launcher_models_test.go` `TestManagedCodexModelsAskTheAppServer`, and `cmd/personad`
  `TestLaunchCodexGatewayCatalog` (a fake node and a fake codex: catalog, none, the caller's own provider, another
  codex than the node checked that reads one or is too old).

## Sessions in the Codex desktop App (App-local IPC)

**App updates can make chats view-only** (#13190). Read-only inspection of macOS App 26.1117.41037 (build 29803) found that it no longer starts the desktop IPC router. Its local app-server still uses private stdio, with no `--listen` argument or TCP / named Unix listener. The `hostId=durable transport=websocket` log describes a cloud host, not a local endpoint for these chats. The bundle contains an optional local-daemon connection gated by `CODEX_APP_SERVER_USE_LOCAL_DAEMON=1`; it was unset in the inspected processes and `<CODEX_HOME>/app-server-control/app-server-control.sock` was absent. No usable local control route was found in that configuration. The investigation extracted the bundle into an empty temporary directory and only read it; no existing conversation or App configuration was changed.

The node checks whether the legacy bus accepts connections, rather than relying on the socket file left by an older App. On Unix the probe connects and closes without sending a bus request, with a 250 ms budget, once per Codex home in a session-list pass. This detects missing and stale sockets without hard-coding App versions. A connected app-server that already owns the thread still takes priority; otherwise a live legacy bus retains the behavior below.

On macOS, when the legacy follower socket is absent or unserved for the default Codex home (`~/.codex`), Persona also supports the native thread queue in the bundled App CLI (`server/internal/node/codex_desktop_native_queue.go`). It probes the bundled CLI once per installed App build (`queue --help`), checks `thread/queue/list` for an already-persisted `clientUserMessageId`, opens `codex://threads/<id>` in the same App bundle only when that bundle does not already hold the thread's writer lock, and enqueues the original thread ID, client message ID, text, and images with `thread/queue/add`. The owning desktop process drains the persisted submission (including after a running turn finishes). Persona reports a queued receipt only after Codex acknowledges the message ID; a server rejection returns `codex_read_only` (`delivered: none`), while a broken transport or missing receipt leaves delivery unknown (`delivered: unknown`). Interrupting a running turn and changing model/effort still require the legacy follower bus and are not advertised in queue-only mode; user-created desktop chats may also be identified by `source: vscode` and `thread_source: user` on builds that migrated sidebar membership out of the legacy lists.

Without a bus listener or native queue route, App chats retain their history, App identity, project grouping and observed turn status, but advertise no send, interrupt or model / effort controls. The App launcher is unavailable too. Requests from an older session list receive `codex_read_only` with `delivered: none`, without a raw socket path or dial error. Sends are refused before saving attachments or accepting a held message, including while the App is working. While the Codex App is running (or a turn/rollout is active), terminal resume / takeover stays refused (`app_running` when idle with no live writer, offering **Continue in a new session** when a terminal launcher is available, or pure view-only when no launcher is available or a writer is active). Once the Codex App is not running and no live writer or running turn holds the thread (#13772), the row offers **Continue in a new session** and guarded **Resume / Take over** (under the same `CODEX_HOME`, behind confirmation that the App will no longer own the chat, falling back to continuation on `encrypted_context`), updates its view-only reason to name the phone actions, and makes the idle unowned history eligible for `agent_send` with `resume: true`. A listener or native queue route appearing again restores the corresponding App controls on the next list. A failure after a turn request was sent retains the existing uncertain-delivery behavior; it is never retried as a new send.

In builds that expose the legacy bus, the desktop App's own app-server uses stdio, so external clients cannot join it. Starting another app-server to resume the same session is rejected (active writer already exists).
But the App's main process exposes a local message bus at `<CODEX_HOME>/ipc/ipc.sock` (on Windows the named pipe `\\.\pipe\codex-ipc`, see "Windows" below); App windows use it to request "run a turn in the session you own."
The node joins as another bus client (`server/internal/node/codex_desktop.go`):

- Frames are a 4-byte little-endian length + JSON. First `initialize`, then `thread-owner-discovery` to find the owning window, then `thread-follower-start-turn` to that window.
  The App runs the turn normally and shows it in the UI. Replies go into the same rollout, which the phone reads as before.
- **Every request must include the App's expected `version`** (owner-discovery 1, start-turn 2, interrupt-turn 4, update-thread-settings 2, read-model-settings 1; table in App main-process code). Missing or wrong versions get no client handler, and the bus returns `no-client-found`.
- The bus also asks each client "Can you handle this request?" (`client-discovery-request`). Without an answer it waits for the full timeout; the node immediately returns `canHandle: false`.
- Only sessions loaded since this App launch have an owner. For unloaded sessions (newly created, or not opened since launch), the node first asks the App to open them (`open codex://threads/<id>`: brings the App to the foreground and switches chats; a background open does not load them), waits up to 30 seconds for an owner, then sends. If no owner appears, reject (`codex_desktop_not_loaded`, not delivered).
- Archiving / restoring chats in the App **moves** rollout files between `sessions/` and `archived_sessions/` (same inode). When the cached `transcript.File` sees the original path disappear, the node calls `Relocate` to the new path and continues incremental reads. Previously it kept failing to read the old path and sent stale content to the phone.
- The App's usual rollout origins in the first `session_meta` line are `Codex Desktop` and `codex_work_desktop`.
  The App uses `source: vscode` for sidebar chats, `exec` for background runs and `subagent.thread_spawn` for
  sub-Agents. Other tools can also write a desktop originator, so Persona checks `thread-project-assignments`,
  `projectless-thread-ids` or active native `threads.project_id` assignments. If a
  [shared app-server](codex-app-server.md#what-the-code-relies-on) stamped a root chat with `originator: "persona"`
  and `source: "vscode"`, the same home's sidebar must list that thread. The chat then uses the existing App grouping
  and resume protections. The App bus is a fallback when there is no connected shared owner. Owner conflicts leave
  the chat view-only. Chats Persona creates through its App launcher have `originator: persona_codex_app` and are
  recognized independently of native sidebar registration. App sub-Agents are view-only with `parent_thread_id`,
  belong to their parent chat's project and count as Seen after finishing. CLI rollouts use `codex-tui` / `cli` and
  keep `agent_name` "Codex CLI".
- **Sub-Agents without a parent session are not listed** (#4745): rollouts marked as sub-Agents in `session_meta` (as above, `thread_source: "subagent"` or `source.subagent`) that are neither on the shared app-server nor under a parent chat in the Codex App sidebar do not enter Agents as standalone sessions, generate completion pushes, or show as Unread. Previously each such subthread (for example, a reviewer delegated by a skill to review an MR) had its own row, title falling back to the cwd directory name, and completion push. Their work remains in the parent session's history (SubAgentActivity). App-server sub-Agents remain view-only with `parent_thread_id` (their permission cards must be answerable).
- **Program-created one-shot sessions are not listed** (#2131): rollouts with `session_meta.source: exec` (`codex exec`, SDK, App background runs), or `originator: codex_exec` / `codex_sdk_*` (inherited by their sub-Agents), do not enter Agents, snapshot statistics, or `agents_list`. Scripts launch these in batches and they end when finished (one evaluation created dozens in separate temporary `CODEX_HOME`s; over two thousand existed on the development machine within two weeks). Do not classify them by directory. Those already in clients are removed as "no longer on the machine" on the next Codex-history fetch. Sessions in herdr panes use the pane route and are unaffected.
- **Guardian approval reviews are not listed** (#5486): when Codex has a review model assess a tool call before it runs (Guardian, also the fork's below), each review writes its own rollout next to the chat, with `session_meta.source: {"subagent": {"other": "guardian"}}`, `thread_source: subagent`, and the chat in `session_id` / `forked_from_id`. It holds only the review's request and assessment JSON, nothing to answer, and in a long chat such rollouts far outnumber real sub-Agents, so they enter neither Agents nor their chat's sub-Agents (`readCodexOrigin`, `codexSessionsIn`; a review whose rollout is gone by its row in the App's database, "Chats without a rollout" below); clients that listed them drop them on the next Codex-history fetch. `thread_spawn` sub-Agents are listed as before. Test: `node/codex_hierarchy_test.go`.
- Persona combines the App's sidebar metadata with native project records, with states isomorphic to herdr (`docs/protocol/agent-states.md`). Read state comes from `electron-thread-read-state-v1` in `<CODEX_HOME>/.codex-global-state.json`; project names, roots, order and assignments merge that file with native SQLite metadata ("Saved projects" below). Native `threads.project_id` and legacy assignments are resolved to the same project identity. Group by project, newest first within groups; unassigned chats and their sub-Agents go under "Codex chats", including chats with an App-created folder under `~/Documents/Codex/<date>/<name>`. This changes space grouping, while the session's `repo_root` remains its folder. The merged catalog supplies its name and `space_index`; a native-only row is not proof of an entry in the visible desktop sidebar. A sub-Agent takes its parent chat's group and index, even when the parent chat is not listed. For chats from the last 3 days: last turn running → Working; finished and still unread in the App → `done` (Unread); seen in the App → `idle` (Seen). Viewing on the phone also counts as Seen, as usual.
- **Chats without a rollout** (#3305): since the App moved history into `thread_history_1.sqlite` (`history_mode: paginated`) it deletes rollouts, so most sidebar chats have none, and chats untouched for two weeks fall outside the rollout scan. For the chats in the sidebar's lists the node reads `<CODEX_HOME>/state_<n>.sqlite` (the highest `n`; table `threads`: `id`, `rollout_path`, `name` or `title`, `cwd`, `updated_at_ms`, `archived`) (and `model`, `reasoning_effort` where the schema has them: what the App runs the chat with, which a chat whose history is the App's database says nowhere else; its row's `model`, when nothing else gave one, and `effort` come from them, #6501) with the system `sqlite3 -readonly -json`, as the Messages and Cindy readers do, at most every 5 seconds and only when the database or the lists changed (`node/codex_appdb.go`). The App removes a database's `-wal` and `-shm` when it closes it (it quit, or the database was idle), and a read-only connection cannot open a WAL database without its `-shm`: such a database is opened as `immutable` (no locks, no `-shm`) and read as last checkpointed, which with the App's connection closed is every change it committed (`codexDBTarget`; the history database below too). An unarchived row is listed: through `rollout_path` while that file exists, whatever its age, else from the row alone (title, directory, last update), grouped and sent to (through the App's bus) like any App chat. An id in the lists with no row is a chat deleted in the App (the lists keep those) and is not listed. Without `sqlite3`, the database or these columns nothing more is listed than before. A row's `source` is what its rollout's `session_meta.source` was, so a row whose rollout is gone says where the thread came from as the rollout did (`codexThreadOrigin`, #8191): the migrated App's own rows include its sub-Agents (`{"subagent": {"thread_spawn": {"parent_thread_id": …}}}`), which fold under their chat, read-only, and its Guardian reviews (`{"subagent": {"other": "guardian"}}`), which are not listed; neither is a chat to send to. Read only from the rollout, each was listed as an agent of its own titled with its folder (one machine: 229 sub-Agents and 32 reviews beside 21 chats). Tests: `node/codex_appdb_test.go`, `node/codex_appdb_migrated_test.go`.
- **Their history** (#3305): such a chat's transcript is its history in `<CODEX_HOME>/thread_history_<n>.sqlite` (the highest `n`): `thread_turns` (turn, status, duration, error) and `thread_items`, the app-server's ThreadItems (v2, camelCase), read in one query (`sqlite3 -readonly`, one snapshot, turns and their items by ordinal). The node writes it as rollout lines into `<state dir>/codex-app-history/<home hash>-<thread>.jsonl` (`node/codex_apphistory.go`): `session_meta` (originator Codex Desktop), then per finished turn `task_started`, an `item_completed` per item and `task_complete` (with the App's error when it failed) or `turn_aborted` (interrupted); the last turn while it still runs waits for the next write, and a turn that never finished (the App quit during it) but has turns after it is written as `turn_aborted`. Each ThreadItem becomes the rollout item the transcript reader knows: `userMessage` → `UserMessage` (text, `localImage` → `local_image`), `agentMessage` → `AgentMessage` with its phase, `reasoning` → `Reasoning`, `commandExecution` → `CommandExecution` (the login shell around the command dropped, as a rollout's argv is, the command unquoted as a POSIX shell would, for `sh`, `bash`, `zsh`, `dash`, `ksh` and `fish`; one that is not a single shell word is kept whole; output, exit code, duration), `fileChange` → `FileChange`, `mcpToolCall` → `McpToolCall`, `imageView` → `ImageView`, `imageGeneration` → `ImageView` of the saved picture, `webSearch` → `Extension` `web.search`, `contextCompaction` → `ContextCompaction`; other kinds (`sleep`, `subAgentActivity`, …) and items still running are left out. The file grows as a rollout does: the turns finished since are appended, so readers go on where they were; `<file>.mark` records the last turn written (ordinal, `turn_id` and its status then), what it is up to date with (the state database's `updated_at_ms` and the history database's files: the App can write a turn's rows after updating the chat, and finish a turn without updating it) and the file's size. A file unlike its mark, or whose last written turn is no longer in the database as it was (the App rolled the chat back, or finished a turn written as interrupted), is written anew (through a temporary file renamed over it, so readers start over), and removed when no finished turn is left. One writer at a time, across processes too (`<file>.lock`): the others wait for its write, up to 30 seconds, so a chat opened by several requests at once has its transcript for each. It is brought up to date when the transcript is read (`node.file`) and about once a second while a Codex thread is watched (`pumpWatch`, in its reading goroutine; a watched chat whose rollout the App deletes goes on from the written file), never by the list; with no change the database is not asked. While the App writes, a watched chat is checked against the database about once a second (one short query from its last written turn), so a watching client gets a turn about a second after the App finishes it; a message sent from the phone shows once its turn ends. A failed write is tried again at once when the chat's `updated_at_ms` changes, after 10 seconds when only the history database changed, and after 5 minutes otherwise. Turns before the last one written are not read again: one the App changes afterwards (an item added to a finished turn, an interrupted turn finished after a later one began) shows as written until the file is written anew. Files of chats the App no longer lists are removed when another of the home's chats is written; the directory can be deleted at any time. A chat with no finished turn, or without `sqlite3` or the history database, has no transcript, as before. Tests: `node/codex_apphistory_test.go`, `node/codex_shellargv_test.go`.
- Replace new-chat directives in Codex replies, `::created-thread{threadId="…"}`, with `persona://session/<machine>/<session>` links (the in-App chat-link format: Android opens that chat; iOS currently recognizes only Live Activity `persona://open?…` links, so these chat links do not yet navigate and require a release), letting users tap on the phone to open the chat.
- **A message while the chat runs a turn is delivered once, after the turn** (#9416): the bus has only `thread-follower-start-turn`, and the owning window passes it on as a `turn/start`, which Codex steers into a running turn and the App then runs again as a turn of its own when that turn ends: the Agent got the message twice. So while the chat's turn runs (the rollout's tail says `task_started` without its end, or, for a chat whose rollout the App deleted, its last turn in the history database is `inProgress`) the node holds the message, `session.send` answers `{queued: true}` (`agent_send` says "Queued", as for pi, #8097), and when the turn ends everything held for the chat goes in as one turn, texts in the order sent and pictures after them. A turn the node started counts as running for up to 20 seconds until the App writes something of it, so a second message right after the first waits for that turn too. Held messages live in the node process: a node that stops first drops them (logged), as does a turn still running after 12 hours (`node/codex_desktop_queue.go`; tests `node/codex_desktop_queue_test.go`).
- **Stopping a turn from the phone**: a chat whose turn runs (`working`, read off its rollout as above; not a subagent) has `capabilities.interrupt`. `session.interrupt` sends `thread-follower-interrupt-turn` (version 4) to the owning window with `{conversationId, mode: "user-stop"}`, the App's own stop button: the turn stops, and so do the subagents it started, and the App shows it stopped. A thread no App window has loaded runs no turn there: refused (`codex_read_only`), not opened to be stopped (`node/codex_desktop.go` `interruptCodexDesktop`).
- **Model and thinking effort from the phone** (#5575): a chat (not a subagent) has `capabilities.model`, the `efforts` its model takes, and its next turn's model and effort (`node/codex_desktop_effort.go`). It has no `capabilities.modes`, which would offer permission mode too.
  - **Levels**: the App's Codex (the one it bundles, `codexAppCLI` as for new chats below) is asked `model/list` with `includeHidden` over stdio as the App's client, in the background and again after 10 minutes, so listing never waits on it. It runs with the node's Codex home as `CODEX_HOME`, the App's, so it reads the App's `config.toml` (custom catalog entries included). The node can fall back to that home's `models_cache.json`. Until the list comes, and for a model it does not list, Codex's whole effort list is offered, as on an app-server whose list could not be read. A model whose list is empty takes no effort: the chat then has no `efforts`.
  - **Current values**: the phone's recorded model/effort choice while no newer turn has superseded it, else the rollout's last `turn_context`, else the chat's `model` and `reasoning_effort` in the App's database (a chat whose history is the App's database has no `turn_context`; #6501, "Chats without a rollout" below). A later turn records what it ran with, including a choice made in the App's picker in the meantime. The phone's choice is in node memory: after a node restart the last turn's values show until the next turn, while the App keeps its settings.
  - **Changing them**: `session.modes` with `model` and/or `effort` sends `thread-follower-update-thread-settings` (version 2) to the owning window, `{conversationId, threadSettings: {model?, effort?}}`, with no `activeTurnId`. It changes the next turn: a running turn goes on with its own settings. A chat no window has loaded is opened first, as for a message (`loadedOwner`). A model change reconciles an incompatible previous effort with the new model's known levels. The change counts only when the window answers `applied: true` and `thread-follower-read-model-settings` reads each requested value back.
  - **Refused**: an invalid model identifier; an effort the selected model does not take; `permission_mode` (the App's to change); the App not running (`codex_read_only`) or not loading the chat (`codex_desktop_not_loaded`); the window refusing or not applying the settings (`codex_refused`). A change not read back is an error ("may not have changed"), and no choice is recorded.
  - Measured for effort on November 13, 2026 (Codex App 26.1106, its Codex 0.167; a scratch chat): set to `max` before its first turn, with the App opening it (11.2 s), then to `ultra` 6 s into a turn running `sleep 15` (5 ms). The first turn ran at `max` and completed, not aborted; the second ran at `ultra`. This record does not measure model changes or the Windows extension.
  - Tests: `node/codex_desktop_effort_test.go` (settings updates and readback through a fake bus, rows before and after later turns, refusals that record nothing, opening a chat not loaded, model-specific levels and their background read, `model/list` over stdio).
- Not supported yet through the desktop bus: steering a running turn, approvals, and changing a chat's permission mode. New chats use metadata creation followed by delivery to the owner, as described below; the App's `codex_app` MCP channel still validates its peer process and is not used by Persona.

### Saved projects

The workspace catalog lists saved Codex projects independently of running engines or chats, including empty projects. During the migration from `.codex-global-state.json` to native storage, the node merges `local-projects` / `project-order` with `projects` and `project_roots` in `state_<n>.sqlite`. The saved legacy-to-native mapping keeps thread assignments attached to the same project; native order leads, followed by projects not yet migrated. Multiple roots retain one project identity. A failed refresh reports a source error and keeps the last successful catalog (`node/codex_projectsdb.go`, `node/desktop_workspaces.go`).

Workflow board can bind an existing Codex project. Native sidebar project creation is deferred and remains disabled. `workspace.create` with `desktop_app: "codex_app"` returns `desktop_project_sync_unavailable` before creating a directory or calling a native project API. Create the project in Codex, then bind the discovered project in Persona. Ordinary Persona project creation remains available; its title does not rename a Codex project. SQLite and global-state files are read only by Persona; Codex owns their writes.

In the inspected Windows app, `project/create` writes the native database, while the visible sidebar uses a separate legacy project catalog cached by the main process. No public project IPC was found that synchronizes that catalog. A successful metadata API response therefore cannot satisfy native sidebar creation or project-name synchronization, and the public project-creation path does not call it. Refreshing or reopening is not a verified remedy. Direct JSON edits are not used because the app's cached state can overwrite them. Existing-project discovery and exact native project selection remain available independently of native project creation.

Starting from a board card passes its `source_id` as `desktop_project_id`, preserving the exact project when several share a folder. The node checks its Codex-home tag, resolves migrated legacy IDs through the saved native mapping, and verifies the selected project's current roots with `project/list` before `thread/start {projectId}`. A missing, ambiguous, different-profile or unmigrated JSON-only project fails before chat creation; it never substitutes another project in the same folder. A JSON-only project must first be opened or updated in Codex so a native mapping becomes available; that migration path remains unverified on Windows. Without an explicit selection, the node uses the most specific registered root for the folder.

### New chats in the Codex App

The `codex-app` launcher is offered while the desktop bus is available and a usable Codex CLI can be found. It needs no terminal engine. Windows support extends the upstream launcher from PR #5755, merged on November 13, 2026, with Windows process handling.

`launchers.list` identifies it with `source: "codex_app"` and `agent: "codex"`; a login-shell launcher with the same name takes precedence. The node prefers the desktop app's bundled CLI, then PATH or known Codex install locations. The launcher is an explicit choice: fallback selection prefers plain `codex` and other available launchers and never chooses `codex-app`. Its model catalog uses the app's Codex model list. Run Agent Here and new terminal starts refuse this native launcher; an existing desktop chat continues through its normal message route. The implementation is in `node/codexapp_new.go` (#5726).

A short-lived app-server creates metadata with `thread/start` and `thread/name/set`, then exits without a model turn. Persona opens the new thread through the desktop protocol handler and sends the first message through `thread-follower-start-turn` to its owning window. Threads created by this flow carry originator `persona_codex_app` and are recognized as desktop chats. Before the first message, explicit model and effort choices are applied through `setCodexDesktopSettings` and read back from the owning window. Unselected values retain the App's settings; permissions remain the App's, and Plan mode is refused. The first message is required because a chat without one is not visible in the App sidebar.

The app's `codex://new` link only fills its composer. The metadata client uses the node's `CODEX_HOME`; `thread/name/set` persists the new thread before the first turn, and the title is the first message's first line or `name_hint`, spaces folded and limited to 40 characters. It never resumes another thread. Persona's assistant threads retain originator `persona` and are not classified as these desktop chats. A missing prompt (`codex_needs_prompt`), stopped app (`codex_read_only`), Plan mode (`readonly_unsupported`) or `secret_ticket` (`secret_env_agent`) is refused before chat creation.

If metadata creation returns a thread ID but naming or first-message delivery fails, `desktop_start_incomplete` retains that session and reports `created: true` plus `first_message_delivered`. Retry sends to the existing session instead of creating another chat. An unknown first-message outcome remains uncertain to the user; a transport failure before the thread ID is known also remains uncertain. See the [new-session protocol](../protocol/protocol.md#new-session).

An explicit folder may be an existing absolute directory or one new child under an existing parent. A blank folder creates a unique directory under `~/Documents/Codex/<date>/`. The chat follows the App's project assignment for its folder. Windows tests exercise metadata creation, safe process arguments and mocked owner delivery; real Windows creation and message delivery remain unverified. See [Windows support](../operations/windows.md#desktop-projects-and-new-sessions).

The temporary folder's slug comes from ASCII words in the first message's first line, falling back to `chat` and adding `-2`, `-3` and so on when occupied. Upstream acceptance on November 12, 2026 used Codex App 26.1106 and its Codex 0.167 on macOS without a terminal engine: the start answered after 16.7 seconds, the App replied after 24 seconds, and the chat appeared among chats with no project. This does not validate Windows creation or native project synchronization. Tests: `node/codexapp_new_test.go`.

## Windows

Same features as macOS, with Windows' own ways in (`codex_desktop_windows.go`, `codex_procs_windows.go`; status table and verification record in [windows.md](../operations/windows.md), "Codex"):

- **Codex App**: the bus is one named pipe per user session, `\\.\pipe\codex-ipc`, not a socket in the Codex home (the App's `app.asar` picks it when `process.platform` is `win32`); frames, methods and versions are the macOS ones.
  The node looks for the pipe in the pipe namespace (`FindFirstFile`) and connects only to send: `os.Stat` on a pipe opens it. As the pipe is the same whatever the home, an App chat is one in a home holding the App's
  `.codex-global-state.json` (`~/.codex` for the App on this machine). An unloaded thread is opened with `codex://threads/<id>` via `ShellExecute`, once `codex://` is a registered URL protocol (the MSIX package registers it as a packaged protocol);
  when it cannot open, the send is refused at once (`codex_desktop_not_loaded`) instead of waiting 30 seconds. List grouping and state are the macOS rules; the App's project roots and rollout directories are compared without case.
- **Codex CLI**: `codex.exe` processes are read from the process table and the PEB, and the rollouts they hold open from their handle tables, so a CLI running in any terminal shows its thread as working / done
  from the rollout and never as resumable (a second `codex resume` would be a second writer). With the herdr engine, an exited thread resumes in a new tab through `personad codex-start`, which runs the `codex.exe` behind npm's `codex.cmd`
  rather than the batch file (cmd.exe would read the first message as commands). The Orca engine resumes it the same way, in a new Orca terminal (#17044; not yet checked on Windows).
- **app-server endpoints**: a `--listen ws://127.0.0.1:<port>` app-server is joined as elsewhere. A `--listen unix://` one serves an AF_UNIX socket file; it is accepted when the socket and its directory are this user's with an access list that lets nobody else in.
  Codex 0.163's shared daemon does not start from npm's launcher on Windows, so a plain `codex` runs in its own process there and keeps the terminal route.
- **SQLite dependency**: paginated history and native projects require `sqlite3.exe`. If the node's inherited PATH misses a recent installation, lookup also checks beside `personad.exe`, the current user/system PATH in the registry and `%LOCALAPPDATA%\Microsoft\WinGet\Links`. It does not mutate PATH. SQLite is launched read-only in a hidden child process (`node/codex_sqlite_windows.go`).
- **How to check it on your computer**: with the Codex App running and the node connected, open a chat of the App on the phone or the web: it shows "Codex App" with an input box, not "view only".
  Send a short message to a chat you can spare (or a new one you made in the App for this); it shows in the App and the reply on the phone. For an unloaded chat, quit and restart the App, then send to a chat you have not opened since: the App comes to the front on it and runs the message.
  For the CLI, run `codex` in a terminal outside any engine and open its thread on the phone: it is view only, its reason says Codex is running on the computer, and it shows Working while it answers.

## Starting from the phone

Since 2026-11-02, choosing a Codex launcher in the phone's "New Chat" starts a new herdr tab on that computer running Codex, with the entered message as its first message.
Since 2026-11-12 the message is optional: with none, Codex opens in its tab and waits for one ("Starting without a first message" below).
The entry point is the same as Claude's (machine, directory, launcher); subsequent behavior uses "Sessions on the daemon" above (bare `codex` 0.163 joins automatically) or the terminal route below.

- **Which launchers are Codex launchers**: `codex` on PATH adds a launcher named `codex`. On a Mac where the login shell finds
  no `codex` and has no alias or function of that name, the CLI the ChatGPT app bundles is that launcher, run by its absolute path
  (`bundledCodexCLI` in `node/codexapp_new.go`, #10437); its base name is still `codex`, so herdr recognizes the pane, `codex-start`
  runs it and the effort check treats it as plain `codex`. `agentExecutable("codex")` finds it last too. Without this, such a Mac
  offered only `codex-app`, so its Codex Agents ran as Codex App chats outside herdr on the App's model. Aliases in `~/.zshrc` that run a single `codex` command are listed after Claude launchers (can have preceding
  `export A=1;`, `VAR=… codex`, or wrappers such as `env` / `command`; arguments are retained, model taken from `-m` / `--model` / `-c model=…`).
  They carry `agent: "codex"` in the launcher list and are marked "Codex" on the App's launchers page (`parseCodexAliases` in `launchers.go`). Unsupported: shell functions running Codex
  under other names (rejected with an explanation, `codex_launcher`), aliases doing additional work (`cd x && codex`, not listed), and TRAE CLI.
  On Unix, the native `codex` command honors a same-name shell function, including its environment and flags (#4876).
  With an available Bash/Zsh, the function or executable can be supplied by its rc file; it need not exist on the outer process's PATH.
- **First message arrives unchanged**: Codex creates a thread only after receiving its first message. A start that includes it answers with that thread (below); one without it answers with a waiting row
  ("Starting without a first message"). Current nodes handle `async:true` synchronously for terminal Codex: they wait for the first thread and return its real ID. An older node may refuse the async request with `codex_needs_prompt`; an older App retries with the message. Older nodes refuse a start without a message the same way,
  so Apps make the message optional only for launchers marked `prompt_optional`. Launcher words and message are written together to a private temporary file under the node directory (0600). The pane receives only
  `personad codex-start <file>`: it claims the file (renames it away, so a node that gave up on the start and withdrew the file the same way leaves it nothing, and the other way round), reads and deletes it, then runs the launcher words (preceding `VAR=…` enters the environment), adding `--` and the message only when one is supplied.
  On Unix, native `codex` runs through the user's interactive shell to preserve a same-name function. The shell executes only `"$@"`: launcher words and the message are positional arguments, never interpolated as shell code.
  On every OS, the private launch file carries the complete prompt and the helper passes it as one native argv value after `--` (#11145). Windows does not type this message through ConPTY: its key stream and paste-burst grouping do not preserve an atomic long paste reliably. The npm shim is resolved to the native executable; unknown batch shims with a prompt are refused. Windows checks the fully quoted command line against 32,767 UTF-16 units including the NUL terminator, without truncation or terminal fallback. A rejected helper closes its owned new pane through the usual failed-start path. Line endings become LF and terminal controls remain inert on Windows; complete rollout matching still confirms startup. Codex App uses structured IPC input instead.
  Quotes, newlines, `$`, backticks, Chinese, and thousands of characters all reach Codex unchanged (`codex_start.go`). Launcher words also stay off-screen and out of shell history
  (aliases may contain keys). With `--remote`, add `-C <directory>` (the TUI does not supply the directory itself when connecting to another app-server).
- **Text only**: the first message carries no photos or files. New Agent offers no attachments for a Codex launcher on iPhone and iPad, and the web refuses such a start with "This launcher can't take attachments in the first message" (#2321).
- **Tell herdr this is Codex** (#3454): in herdr panes (except Windows), the file also contains `agent: "codex"`. `codex-start` sets `HERDR_AGENT=codex` in the exec environment,
  replacing an inherited shell value (appending would leave two entries with the same name; getenv and herdr take the first, inherited one). herdr can then recognize codex wrapped under another name
  (why and the first herdr version supporting this: `docs/integrations/launchers.md`, "How herdr recognizes Agents started by launchers"). The same applies when resuming exited Codex (`resume <id>`).
  Only `'<personad>' codex-start '<file>'` is typed into the pane, without a prefix, so it works with any shell. Preceding launcher `VAR=…` words still append after the inherited environment
  (an existing same-named shell variable wins; these words are not shell-expanded, so `$HOME` and `~` remain literal and cannot substitute values). pi's herdr-resume command `personad tab-start` shares this code without setting `HERDR_AGENT`.
- **Match this request**: startup is synchronous. The node waits for the Codex thread to appear before replying, and returns that thread's session, so the App opens the actual Agents entry,
  not a separate "Starting" placeholder later replaced by another. Match a rollout in this directory started after launch (`session_meta` timestamp) whose first user message exactly matches what was sent.
  During this wait `codexStarting` reserves the pane for this explicit start. Discovery leaves the pane for the start to identify.
  The new tab first forgets every binding of its pane id, the node's and the pane an older session's record keeps (`forgetPane`, for every start's new
  tab): herdr numbers its workspaces again after its server restarts, so the id can be a closed tab's, and that session's binding, which the hub hands
  back, was taken for the pane's session until the thread was known. It was listed as this Codex, then left behind as an extra offline Codex row (#14682).
  Each lookup includes newly discovered Codex homes and checks the pane's observed home first. The start claims the public ID
  from the matched rollout's home and native thread ID, then uses that same ID in its reply and pane binding (#13737).
  Answer Codex's dialogs according to the user's intent: trust the directory (the user selected it), skip update prompts.
  The screen is read as `visible` (the blank rows under it left out): 0.167 draws its trust dialog from the top of the pane, and the last 40 `recent` rows of a 51-row pane were all blank (#2351).
  The trust dialog is known by its question asking about trust, the option by what it says ("Trust and continue", "Yes, continue"), not by its number or the other option
  (0.167 has "Back to Agent Command Center" where 0.163 had "Quit"); the cursor is walked to it, then Enter.
  A rollout's `session_meta` is kept only once its first line is all written: 0.167 creates the file empty and writes the line milliseconds later, and a meta read in between, kept, hid the thread from the start for good.
- **A daemon without its socket**: a shared daemon (0.163 and later) that still runs while its control socket under `/tmp/codex-daemon-<uid>` is gone takes no new client, and every plain `codex` exits with "failed to connect to …/app-server-control.sock" after printing the tail of the daemon's own old log. Before opening a tab, a start of a launcher that runs on the shared daemon checks for that (`codexDaemonSocketGone`: the control alias points at a missing socket and `app-server-daemon/daemon.pid` names a live process of the user) and fails at once with `codex_daemon_socket_gone`, naming `codex app-server daemon restart`. `launchers.check` does the same for such a launcher: its `codex exec` runs in its own process and passed while every start failed (2026-11-14). A stopped daemon, a managed launcher and a launcher with `-c` or `--remote` are not affected.
- **No match**: the node waits at most 60 seconds (hub 100 seconds, App 110 seconds), then reports "Codex has opened in a new tab on the computer, but its session was not identified within 60 seconds…",
  followed by the last screen lines of that tab (falling back to the visible screen when the bottom rows are blank). If the launcher cannot run (command not found), fail as soon as a `personad codex-start:` line appears, without waiting the full timeout. codex-start's notes that it goes on after (the agent sandbox not wrapping the start) say `personad codex-start (note):` instead, so the Codex after them is not taken for a failure.
  If the terminal still reports Codex running, the error carries `agent_start_stalled`: the hub neither counts it as a broken launcher nor retries or reroutes the task automatically.
  **Not signed in**: Codex shows its sign-in screen in place of its input box when it has no login on that computer, and no wait gets past it. A start that sees that screen (with or without a first message) fails at the next look, presses no key on it, and answers `codex_not_logged_in` with the screen's last lines (`codexSignInScreen`, `codexNotLoggedIn`). The screen is known by a "Sign in with …" choice under the cursor ("Sign in with ChatGPT", "Sign in with Device Code"), not by its numbers or the other choices ("Provide your own API key" in older versions, "Sign in another way" in newer ones), and never while the input box is drawn. The hub words it in the user's language, naming the computer and the fix (run `codex login` in a terminal there), and launcher health records it as `auth` from its code, never as a stalled start (`docs/server/launcher-health.md`). The new tab is closed as below; if closing fails, the error keeps `codex_not_logged_in` and adds the cleanup's words.
  Only after this wait returns a failure (including cancellation), the node closes the new pane to stop a Codex still waiting on a screen from later executing its queued prompt without a linked task.
  If that pane explicitly identifies a fresh thread, or its unique Codex writer holds the fresh rollout, cleanup also asks Codex to archive that thread natively after the pane closes (#11145). This removes an already-published orphan from the Agent list while retaining its transcript. A directory, title or matching first line never authorizes archive; adopted panes remain untouched. Native archive failure is retained with the failed-pane cleanup for retry.
  This uses the terminal's existing close operation instead of keeping a background start waiting indefinitely for a thread. The last screen lines remain in the error.
  Independent starts in the same directory with the same launcher remain concurrent; waiting for one thread does not delay another start.
  If closing fails, the error says termination could not be confirmed. The node remembers every failed-cleanup pane for the directory and launcher; a later start must close the pending panes before opening another tab. Only that cleanup is serialized, and failures from concurrent starts are retained separately.
  This retry guard lasts for the node process. Closing a TUI does not prove that an already running turn on a shared or remote app-server has stopped; the cleanup addresses starts still waiting on their initial screen.
- **Route**: bare `codex` 0.163 (or an alias without the following arguments) runs on the shared daemon and joins within seconds; "Connection" in "Session Info" shows app-server.
  Launchers with `-c` / `--profile` / `--enable` / `--disable` / `--oss` / `--strict-config` / `--no-daemon` run in their own process using the terminal route (#214).
  "Connection" explains "Started with launcher 'X': it includes -c, so Codex runs in its own process, not the shared app-server". Version 0.153 has no daemon and also uses the terminal route.
- **Read-only tasks**: a terminal start with `permission_mode: plan` adds `--sandbox read-only --ask-for-approval never`. Conflicting launcher permission flags are refused before creating a tab; other modes preserve launcher arguments. See [To-dos](../assistant/todos.md#read-only-planned-tasks).
- **Creation defaults**: Codex uses launcher arguments and `~/.codex/config.toml`. An empty effort uses those defaults. An explicit effort already set by the effective config needs no override. Otherwise Persona prepares it on the shared daemon as described above.
  On Orca, a new start of the stock `codex` launcher with no explicit Persona permission mode first reads `settings.getAgentLaunch` and inserts `agentDefaultArgs.codex` as literal argv before the normal model, effort and permission resolution. Alias and configured launchers, and every explicit Persona permission mode, keep their own launch configuration without reading those Orca defaults. Unavailable preferences, a custom Orca command or environment, incomplete quoting, newlines and shell-dependent arguments refuse the start with `codex_launch_preferences` before a terminal opens; Persona never writes Orca's launch settings on this path.
  If the daemon cannot apply the effort, or the launcher's profile, provider, or isolated hooks require a terminal process, the start keeps the launcher settings and passes `-c model_reasoning_effort=...` after them. The task card explains that approvals and questions will use the terminal before the node starts that fallback.
- **Shell wrappers**: Persona exports `PERSONA_CODEX_START=1` before interactive shell startup for the effort probe, `personad codex-start`, and herdr restore of a Persona-started pane. An rc file that normally puts a wrapper ahead of the native CLI can check this variable while choosing its PATH. Persona preserves launcher functions and treats unrecognized scripts as opaque because they can select a different home, provider or profile.
- **In the conversation's folder**: the file also names the directory (`dir`), and `codex-start` changes to it (and sets `PWD` to it when the shell had one) before it runs the words, for a resume too.
  Codex records that directory as the thread's, and finds the thread's files from there; nothing but `codex-start` and its file is typed, so no `cd` goes to the shell either.
- **In an Agent's own terminal** (a switch from Claude Code, #5054; [claude-integration.md](claude-integration.md) "Switching a running Agent to another engine"): the start runs in the pane the claude left, once its shell is at rest, instead of a new tab.
  That pane is the user's terminal: a start that fails there never closes it (nor counts it as a start to clean up later); the switch quits what it left in front and brings the claude back.
  The first message is the handoff block, then the user's words.
- **Tests**: `node/codex_start_test.go` (fake herdr: pane command only `codex-start <file>`, file contains launcher words and message, exactly one session, route note, rejection, timeout and startup failure errors, trust dialogs,
  `codex-start` subprocess receives message byte-for-byte, Codex aliases in `~/.zshrc`), `node/codex_orca_launch_preferences_test.go` (fake Orca: stock launch defaults, model/effort precedence, literal quoting, bypasses and fail-closed validation with no terminal or settings writes). `node/codex_start_thread_test.go` covers first-turn effort and fallback, and `node/codex_restore_resolver_test.go` compares the executable used by the probe, launch and restore. Real Codex: `TestLiveCodexStart` in `scripts/test/test-codex.sh`
  (0.163 connected with `--remote` to the test's own app-server, 0.153, 0.163 with `-c`; each round in a fresh git repository, so Codex asks to trust it and the start must answer;
  multiple turns each, messages with quotes / newlines / `$` / backticks / Chinese / long text,
  unchanged in rollout, one session per pane, correct route, a subsequent phone message received). App: `MachinePagesTests.testNewCodexSessionStartsWithItsFirstMessage`.

### Starting without a first message

Since 2026-11-12 (#4961) a Codex start may leave out the first message: Agents run in a terminal (#4754), and `codex` in a terminal needs none. The node opens Codex in its new tab as above, without the message,
and answers with a **waiting row** for the pane (`node/codex_waiting.go`).

- **Before its thread exists, Codex has no session id**: herdr lists the pane as `agent: codex` with no `session_id` (Codex 0.153.4, herdr 0.9.3), and no rollout or `threads` row exists yet. About two seconds after the first message
  the rollout appears, and the pane's `codex` process holds it open. herdr still reports no session id.
- **The start**: `codex-start` runs the launcher words alone. The node waits for Codex's input box, at most 60 seconds. On the way it trusts the folder, waits out a model still loading (`model: loading` in the header),
  and skips the update offer. Codex's own three-choice offer (`Update now`, `Skip`, `Skip until next version`) is skipped with Escape on every operating system, never Enter: Enter takes the highlighted "Update now", which runs the installer. Version upgrades belong to operations, not a message-delivery attempt.
  If the launcher fails (a `personad codex-start:` line), the start fails and the tab closes, as above. If Codex runs but its box has not come up in time, the row is still returned and the tab stays.
  Nothing is queued in that tab that could run later.
- **The waiting row**: its id is the node's own: a UUID whose first group is `c0de0000`, which no Codex thread id (a UUIDv7) or Claude session id takes. It carries `awaiting_first_message: true`,
  `agent: codex`, the pane and tab, `status: idle` whatever herdr says (Codex starting its MCP servers reads as working, then done), or `blocked` while a dialog holds the pane.
  Its capabilities are read, send and terminal. Its conversation is empty. Modes, the queue and forking are refused until the thread exists, and it has no history, archive or resume.
  The message index returns a valid empty list under the waiting row's history token.
  Web follows the replacement Codex row on the same computer and pane from either the compact session list or the full session list, then reads its complete settings.
  The node binds the pane to the row's id. The hub stores and returns that binding, so a restarted node knows the row again from the id's shape and its record.
  A Codex started by hand in a herdr or Herdr App tab uses the same row. Its title is the tab's label, with the directory name as a fallback. The row's time is when Persona first saw that pane.
- **Attaching the thread, never by directory**: the node looks every second while a row waits. It scans the Codex processes only when a new thread appeared in the row's directory or herdr or the title named one, and every 15 seconds otherwise.
  A new thread that no rule attached in three looks (one from a Codex outside herdr in the same repository) no longer triggers scans; only the 15-second scan looks at it again.
  1. The rollout the pane's own `codex` process holds open: `HERDR_PANE_ID` in its environment, `lsof` or `/proc` for its files. App-server processes are left out: a daemon a pane started inherits that pane's `HERDR_PANE_ID` and holds every thread it serves.
     This covers 0.153 and launchers whose thread runs in its own process.
  2. The thread herdr's Codex integration names for the pane, unless another pane's process writes it. Then the thread the pane's title names ("<name> | <directory>"), when exactly one thread has that whole name
     (not one cut short with "…") and no other pane's process writes it. This is what a `/resume` in that Codex shows, which rules 1, 3 and 4 can't see on the shared app-server.
  3. A first message sent from the Apps: the one thread begun in the directory since it was typed whose first user message is that text, which no other pane runs.
  4. The shared app-server daemon (0.163 and later), whose TUI holds no rollout. A row takes a thread only when all of these hold:
     - it is the only new thread in the row's directory since the row began;
     - it is at least two seconds old;
     - no TUI process holds it;
     - its first message shows, as a Codex user message (a `› ` line and the lines indented under it), on that pane's screen and on no other waiting pane's.

  Anything else attaches nothing, and the node logs why, once per reason. The row then stays, its terminal works, and the thread is listed as ordinary Codex history.
  On attach, an explicit Persona start transfers its launcher, directory, start time, read-only mode and creation receipt. A manually opened tab keeps its own name and ownership.
  The pane is bound to the thread, and requests that still name the row reach it.
  The row leaves the list and the thread's row comes, in the same pane.
- **The first message from the Apps** (`session.send` to the row): text only (photos refuse with `codex_first_text`, nothing typed). The node brings Codex to its empty input box first, at most 20 seconds.
  On the way it answers the trust dialog and the update offer, and waits out the model loading and "Starting MCP servers".
  It types nothing, and refuses with the usual codes, when the box holds a draft (`input_draft`), when an approval, question or dialog that isn't ours is open (`dialog_open`), or when the box doesn't come (`codex_not_ready`).
  The message goes in as one bracketed paste, unchanged, and Enter is pressed only once the box shows it (or Codex's `[Pasted Content N chars]` chips with matching visible text before, between or after them). Chip lengths and visible fragments must cover the whole expected message in order, allowing terminal wrapping whitespace; a chip for only a prefix waits for its tail before Enter. Hidden content is still unverified: only a complete first-message rollout match confirms startup. A compact status row whose directory is just `~` is excluded from the draft too.
  A recognized footer delimits the whole draft across blank paragraph breaks, including Codex 0.171's separate warning row.
  If the recognized update offer appears after paste, startup skips it once. A retained matching draft is submitted without pasting again; only an empty, unqueued composer may receive one replay. A different draft or unknown dialog is never cleared or submitted. This recovery has a bounded confirmation window within the original startup request.
  The send then waits up to 45 seconds for the thread (rules 1–3) and answers `{ok, session}` with the thread's session. Rule 3 finding two threads answers `{ok}` alone, and the row stays until rule 1 or 2 knows it.
  No thread in time is `codex_first_unconfirmed` with `delivered: unknown`: the message went in, so it is not sent again.
  First messages to one row go one at a time: a second one (another phone, a quick second tap) waits for the first, then goes to the thread it began as any message does.
  `/exit` or `/quit` quits Codex as for any agent, with nothing typed.
  Modes, the queue and forking on a waiting row refuse with `codex_waiting`. A start whose Codex never shows its box and isn't running fails with `codex_no_box`
  (`codex_no_box_here` in a pane the user picked, which stays as it is).
  The message's echo (`client_msg_id`) is the thread's. The hub links a to-do to the thread the answer names (`hub/send.go`).
- **The user may type the first message in the terminal instead**: rule 1, 2 or 4 attaches the thread.
- **Codex quits before a first message** (or the tab closes): after 10 looks with no `codex` in the pane, the row and the pane's binding go. The terminal stays.
- **Clients**: launchers of nodes that start Codex this way carry `prompt_optional: true`. The manager marks its own managed Codex launchers so when the node's Codex launchers are marked.
  - **Run Agent Here** (#5504): Codex launchers of nodes that start them in a shell pane the user picked carry `run_here: true` (the manager marks its managed ones the same way). The start there is this one, with no first message, and answers the waiting row; one that fails leaves the user's pane open and queues nothing to close (`docs/integrations/terminal.md`, section 21).
  - New Agent offers the first message as optional for those launchers only. The web's work picker reads `prompt_required: false` from the start options.
  - The row says "Waiting for its first message" (it is not Needs you, not unread, and sends no push). Its chat says Codex is open on the computer and takes the first message.
  - Web, iOS and Android follow the first message answer's `session` from a waiting row to the thread, and follow the same computer's pane in the already-loaded session list when the message was typed in the terminal (`web/src/lib/codex-waiting.ts`, iOS `ChatView.codexThread`, Android `codexThreadOf` in `CodexWaiting.kt`). Android replaces the open route; it does not fetch extra pages when a filter excludes the thread.
- **Verified**: Codex 0.153.4 and herdr 0.9.3 on a Mac (2026-11-12), two Codex in one directory:
  - the App's first message (quotes, `$HOME`, backticks, newline, Chinese, 2,098 characters) reached the rollout unchanged, attached in 1.9 seconds;
  - the other row's message, typed in its terminal, attached to its own thread;
  - both attachments used rule 1.

  Rule 4 (the daemon route) is covered by unit tests only.
- **Tests**: `node/codex_waiting_test.go` and `node/codex_start_input_test.go`. They cover: the start skipping the update offer with Escape and never Enter; late update offers, retained/lost pastes, mixed chips and visible text, complete transcript confirmation and bounded failure cleanup; the row listed idle while herdr says working; the App's first message as one paste and the row becoming the thread;
  drafts, photos and dialogs refused with nothing typed; a model loading and MCP servers starting waited out; two rows in one directory;
  an app-server holding a pane's thread ignored; rule 4's ambiguities; the row known again after a restart and gone with Codex.
  The clients: the web's unit tests (`web/src/lib/codex-waiting.test.ts`, `web/src/lib/new-agent.test.ts`,
  `web/src/views/NewAgent.recents.test.tsx`) and `web/e2e/codex-first-message.mjs` against the fake hub with `MOCK_CODEX_WAITING=1`
  (a start with no message, the waiting chat, its first message moving the page to the thread). iOS screenshots use the same
  fake hub: `-tab dev -showNewChat YES -showNewAgentPrompt YES` for New Agent's hint (New Agent's default set to Codex),
  `-tab dev -dev.harnessFilter codex -dev.grouping recent` for the row, `-openSession macbook/c0de0000-0000-4000-8000-000000000001` for its chat.

## Resuming from the phone

A Codex thread nothing runs is a stopped Agent, read-only like any other ([engines.md](engines.md) "Agents run in a terminal", #4754): a plain message to it is refused as `agent_stopped` with nothing opened, on every engine.
It comes back in a new tab of the engine with `codex resume <id>` (through `personad codex-start`, like a new one) on `session.start` with `resume_session_id`: the user's Resume, or the assistant's explicit resume (no message unless the start brings one, which goes in as the resume's prompt, nothing typed).
Code: `node/codex_resume.go`, `node/retire.go` (`resumeOther`, `resumeCodex`); #1700, #5220.

- **Started by Persona**: the launcher, directory, model and effort it ran with (node state; #10434). The effort recorded is the one the start asked for, even when the start left out `-c model_reasoning_effort` because Codex's config already runs that effort; the resume leaves it out again in that case, so it stays on the shared app-server. A terminal read-only start also retains `app_mode: plan`, so a later resume uses the same read-only CLI flags. A fresh `plan` resume applies and saves that constraint too; conflicting launcher flags are refused before opening a tab.
- **Natively archived threads**: after the resume's stop-cause, writer and permission checks pass, restore through the existing native archive route (`thread/unarchive` or `codex unarchive`) before opening the terminal. Otherwise Codex's archive confirmation can block the first prompt. A failed, unsupported or unconfirmed restore refuses the start without opening a tab; Persona never moves the rollout or edits Codex's database itself. Supervisor resumes still refuse user and exit stops, unrecognized causes and rows without capabilities; an unknown cause is resumable (#11528).
- **Started elsewhere** (`codex` in a terminal, since closed; listed as a "Codex history" row): the directory from its rollout's `session_meta`,
  the launcher the start gave when it runs Codex, otherwise plain `codex`. From then on it is Persona's:
  what it was resumed with is kept, and it is retired and resumed like one Persona started.
- **Only when nothing has it open**, else the row stays view only (`codex_read_only`): no app-server owns it (sends go there), no process holds its rollout
  (a Codex CLI still running, the desktop App's own app-server: a second writer would fork the thread), not a subagent or a program's run (`codex exec`, SDKs),
  and the engine can open tabs (not a runtime engine; Orca can since #17044: a new terminal in the Orca worktree holding the thread's directory, `cd` there, then `codex-start`). Only the default `~/.codex` home: a thread of another home is listed under an opaque key, not its thread id.
  An explicit Codex App origin is additionally eligible only once the App and its bus / native queue are no longer available; an ambiguous shared app-server origin corroborated by the App sidebar remains App-owned.
- The history row of such a thread has `terminal` and the stopped reason without `send` (`node/codex_registry.go`), so the apps show it read-only with Resume. One a connected shared app-server could load offers its takeover instead (`capabilities.takeover`); a message does not take it over (#17809).
- **Switching an Agent between the engines** (#5054, [claude-integration.md](claude-integration.md) "Switching a running Agent to another engine"): a Codex Agent Persona started (or resumed) in a terminal can move to Claude Code like a Claude Code Agent moves to Codex.
  It is quit with Ctrl+C in its pane; once only a shell is in front and no Codex CLI holds its rollout (an app-server that keeps the thread loaded does not count: nothing of the other engine writes it), Claude Code starts in that shell.
  When the conversation comes back to Codex, the thread it ran in is resumed in the same pane with `codex resume <id> -- <handoff and message>` through the launcher picked, when that launcher runs Codex in the thread's home and nothing has the thread open (the rules above: an app-server that still has it loaded keeps it from being resumed); otherwise a fresh thread takes the conversation with the whole handoff.
  The resume runs in the thread's directory with the switch's mode (plan as read-only flags, bypassPermissions as the bypass flags), the model picked (`--model`) and the effort picked (`-c model_reasoning_effort`, left out when Codex's config already runs it); the record then names that launcher, model and effort. A switch that fails brings the Codex back on its own launcher with `codex resume <id>` in the same pane, its record's model, effort and mode kept.
- **Tests**: `node/codex_resume_test.go` (history row read-only with Resume / view only while held, a message refused, resume on a start, guarded Codex App recovery after the App exits, shared-origin refusal), `node/orca_resume_test.go` (the same on Orca), `node/codex_takeover_activate_test.go` (a message to a cold shared app-server thread loads nothing), `node/codex_resume_model_test.go` (model and effort recorded at the start and run again on a resume), `node/retire_test.go`.

## Archive

Archiving a Codex session in Persona (`POST /api/v1/sessions/{mid}/{sid}/archive`, `docs/protocol/protocol.md` "Agent session archives") also archives it in Codex,
with Codex's own reversible archive; restoring it in Persona restores it in Codex. Codex's archive moves the rollout to `<CODEX_HOME>/archived_sessions/` and back
(and marks it in its state database), so the Codex App and `codex resume`'s picker list it as archived; nothing is deleted, and Persona moves no file itself.
Code: `node/archive.go` (`session.archive` / `session.unarchive`), `assistant/agent_archive_native.go` (when the manager asks, retries). #88.

- **Protocol** (confirmed in `codex app-server generate-json-schema` of codex-cli 0.163.0 and `codex-rs/app-server/src/request_processors/thread_processor.rs` on main):
  `thread/archive {threadId}` → `{}` and `thread/unarchive {threadId}` → `{thread}`, notifying `thread/archived` / `thread/unarchived` to the app-server's clients.
  Archiving a thread loaded in that app-server shuts it down there first (like the Codex App's own Archive), together with the sub-Agent threads it spawned; an archived thread can't be resumed
  (`thread/resume` and `codex resume` answer "session … is archived. Run `codex unarchive …`").
- **Which way**: through an app-server the node is connected to for the thread's `CODEX_HOME` (the one running it, else any of that home: the Codex App, the shared daemon),
  so its clients see the change at once. With none, `codex archive <thread>` / `codex unarchive <thread>` (the CLI's subcommands, which run the same method on an app-server of their own),
  in the user's interactive shell with `CODEX_HOME` set to the thread's home; with the session's own launcher when it is an alias of codex a resume would use, else plain `codex` (never a managed launcher).
- **Not while it is open in a terminal**: never while a herdr pane still runs it (either way: archiving through the shared daemon would take the thread from under its TUI), and from the command line
  never while any Codex process has the rollout open (`codexHeldNow`, or the processes could not all be read): refused as `native_archive_live`, and the manager tries again later,
  so a herdr Codex is archived in Codex once its TUI exits (closing it, as a finished task's agent is closed, lets it through). A Codex App thread is archived through the App's app-server as the App's own Archive would. Codex refuses too: its cross-process writer lock
  (`thread-writer-locks/`) makes `codex archive` fail while another process writes the thread (checked with a throwaway `CODEX_HOME`). Never while it works (`native_archive_busy`).
- **Idempotent on Persona's side only**: Codex answers `-32600 "no rollout found for thread id …"` to archiving an archived thread (and "no archived rollout" to restoring a restored one),
  and the command exits 1. So the node first reads where the rollout is, and answers done without a call when it already is as asked; after a call it reads it again, and the files decide:
  moved is done even when the answer was an error (an answer lost), not moved is refused (`native_archive_failed`).
- **After archiving**: the thread leaves the node's list (the rule below for threads archived in the Codex App). The manager keeps the archived row from its snapshot, so it stays archived and restorable in Persona.
- **Older Codex** without the `archive` subcommand (clap's "unrecognized subcommand"): `unsupported`, Persona's archive alone.
- **Tests**: `node/archive_test.go` (app-server route with a fake app-server moving files, answers checked on disk, a running turn, the command route with live / unknown holders,
  an older Codex, Claude unsupported, runtimes with and without `caps.archive`, the shell wrapper with a real bash), `assistant/agent_archive_native_test.go`.

## Questions and approvals (structured cards)

Since 2026-11-02, Codex / internal Codex-fork questions and approvals in Agents use the same question and permission cards as Claude, rather than generic "The terminal is asking you" interactions.
Codex has no terminal hook that can answer on its behalf ("Research" below), so the node reads rollout and screen itself:

| Source | Discovery | Card | Answering | Confirmation |
| --- | --- | --- | --- | --- |
| `request_user_input_async` (Default mode; available only when the model catalog's `experimental_supported_tools` includes `send_user_message_async` / `request_user_input_async`, as current gpt-6 models do) | An AgentMessage in rollout `item_completed` carries `delivery: "async"` and `questions` (`title` + string options; item id is call id). The call immediately returns `{"accepted":true}` and the turn continues or ends normally. | Question card (options + other answer) | Send a user message with the reply envelope used by Codex's own clients (desktop, 0.163 TUI): `<send_user_message_question_reply>[{"questionItemId":"[\"request_user_input_async\",\"<call id>\",<index>]","question":…,"answer":…}]</send_user_message_question_reply>`, one line, JSON without HTML escaping. | A user message with this `questionItemId` appears in rollout (0.163 TUI uses it to remove the question from its panel), or the screen shows it in "Queued follow-up inputs". |
| `request_user_input` (Plan mode, or `features.default_mode_request_user_input`) | Rollout `function_call` named `request_user_input`, arguments `{questions:[{id, header, question, options:[{label, description}]}]}`; the turn stops on a TUI overlay ("Question 1/2"). | Question card (multiple questions, headings, option descriptions) | Keys: option number selects and advances (submits on the final question). For a custom answer, move to "None of the above", Tab opens notes, paste, Enter; Codex records `["None of the above", "user_note: …"]`. | This call's `function_call_output`. |
| Approvals (commands, patches, permissions, MCP) | Not in rollout: when herdr reports blocked, read the screen for "Would you like to …?", numbered options, and "Press enter to confirm or esc to cancel" (option text in `tui/src/bottom_pane/approval_overlay.rs`, identical in 0.153 / 0.163). | Permission card: full command or changes, Codex's reason, approve / Always Approve (explains what it permits; omitted if Codex does not offer it) / deny (optional reason). | Number keys: approve = Yes, proceed; Always Approve prefers the session option (don't ask again … in this session / for these files), falling back to the prefix option (commands that start with …). Deny = No, continue without running it; if absent or a reason is supplied, use No, and tell Codex what to do differently (stops the turn), then send the reason as the next message. | This approval (by `rev`) disappears from the screen. |

- Recognize async questions only from items, not `request_user_input_async` `function_call`s. Occasionally model arguments cannot be parsed by Codex (for example, `question` instead of `title`),
  the call is rejected (error output), and the model immediately retries. The rejected call has no item. Creating cards from `function_call` used to leave an unanswerable card forever (observed 2026-11-02).
- Cards disappear when answered on the computer (reply envelope, or any other user message: 0.153 has no question panel, so users type answers directly), the turn is interrupted (`turn_aborted`), or the Agent exits.
  Do not restore async questions older than 12 hours (node restart rereads rollout).
- TRAE CLI shares rollout parsing (also recognizes AgentMessage / UserMessage in `history_mutation`) and screen parsing. The node determines the state from the terminal.
- Implementation: the node scans live Codex and TRAE CLI sessions every second (`node/codexask.go`; rollout reads only appended data, initially the last 8 MB). Whenever open cards change, send the complete list to the hub
  (`codex.interactions`). The manager creates a local interaction for each card (`assistant/fleet_ix_codex.go`): push new ones, end those absent from the list, and replace generic screen cards for the same session.
  Answers return to the node through `codex.answer`. Reread screen and rollout before keys / input; errors return directly to the answering user (synchronously, like screen cards).
  If input fails (draft, wrong UI state), retain the card. If the node says Codex no longer asks (`prompt_changed`), remove it. Relisted cards after node reconnect do not ring again.
  The hub waits 3 extra seconds before creating a generic Codex screen card, giving node cards time to arrive. Push titles are "Codex has a question" and "Codex needs permission".
  Async cards do not replace terminal dialogs: herdr also reports blocked while they are open, so the hub creates another generic screen card only if it actually reads a different dialog (not for a bare input field).
- Threads joined to app-server ("Sessions on the daemon" above, `joined` in `codex_remote.go`) receive approval and blocking-question cards directly from app-server, not TUI inspection.
  Async questions still come from rollout, with answers sent through app-server.

### Different UI states on the computer

Before answering, the node restores the TUI to an answerable state. If it cannot, it presses nothing and reports the reason to the answering user (same error codes as messaging: `input_draft` / `dialog_open` / `prompt_changed`).

Codex 0.166's model/path/warning footer is not draft text, even without a blank separator or shortcuts row.
The blinking `[ . ]` / `[ ! ] Action Required | ` title prefix is also status, not part of the thread name. A named
pane that cannot be identified uses neither a stale pane binding nor directory-based rollout guessing.
The session id herdr reports for the pane stands under such a title while `session_index.jsonl` has no name for that
thread: nothing says the title names another one, and dropping it left the pane unlisted and its thread only as its
app-server's row, without a terminal (#7538). A reported thread the index knows under another name is still dropped,
unless the index gave that thread the title's name before its latest one: a rename (Persona renames a joined thread on
its app-server with `thread/name/set`) leaves two lines for the thread, and a title still showing the first name is
the same thread's.
A reported sub-Agent id is not: the name the index has is its main chat's, not the reported thread's, so the pane stays
the main chat's row.

| On the computer | Behavior |
| --- | --- |
| Partially typed input | Refuse to answer and include the draft in the error: Codex has no reliable "save draft and restore after answering" (images and pasted blocks cannot be restored). Ordinary messaging also refuses; herdr's `agent prompt` previously appended directly to the draft and sent it. |
| 0.153 transcript pager (Ctrl+T) | The pager can cover a later approval while herdr already reports blocked. If the node sees an approval obscured by the pager, press `q` to close it (read-only view, no data lost; at most once every 10 seconds). Never press `esc` in the pager: it means "edit previous message." In 0.163 Ctrl+T only expands details, leaving the input visible. |
| Expanded 0.163 question panel (⇧←) | Typed text would become this question's "Other" answer: collapse it with ⇧→ before sending a reply. |
| `/` command menu, `@` file picker | Appear only with text in the input; treat as a draft. Always use bracketed paste for answers, so `@` and newlines do not open selectors or submit early. |
| Scrolled upward (herdr scrollback) | `pane read --source visible` returns scrolled-up content. Always read Codex's output tail (`--source recent`; the pager uses the alternate screen, so reads return the pager itself). Keys are unaffected by scrollback and do not reset it. |
| Answering an async question with an approval, overlay, or another selector open | Refuse (`dialog_open`): handle the approval card first. |
| Someone typing in the overlay's notes field | Refuse, press nothing. |
| Already answered on the computer | Card disappears with rollout / screen changes. A later phone answer finds no card, returns `prompt_changed`, and types nothing. |
| Pane in the background, herdr on another tab, very small window | Screen reads and keys target the pane itself, unaffected. Rejoin wrapped headings, commands, and options in narrow-window parsing. |
| Turn still running when reply is sent | 0.163 steers it into the turn immediately or queues it; either counts as accepted. |

herdr reports blocked when Codex has an unanswered async question, so `agent prompt` rejects it (`agent_blocked`). Codex messaging handles this by pasting + Enter itself (after confirming the input is empty).

Terminal messaging confirms that the composer is recognized and empty after either the host prompt or the blocked-session
fallback, waiting up to 12 seconds for consumption. An unchanged or edited draft, a dialog, an unknown screen, a failed
read, or cancellation cannot confirm delivery: the result is `delivered: "unknown"`, not success. The check only reads;
it never clears input, pastes again, or retries Enter. The existing client message ID keeps retries from typing twice.
Even an exact match with earlier assistant text does not authorize submitting a pre-existing draft: text equality alone
cannot prove ownership or that an earlier message was not already delivered. App-server and Botmux sends retain their
own receipt paths; Botmux has no readable terminal composer.

### Tests

- Unit tests: `screen/codex_test.go` (recorded screens, 0.153.4 / 0.163.0, `screen/testdata/codex-*`), `transcript/codexask_test.go` (rollout recognition, both schemas,
  partial answers, direct typing, old desktop replies, TRAE CLI), `node/codexask_test.go` (keys and input for every UI state on fake herdr, no keys on refusal),
  `node/codex_submit_test.go` (terminal submission confirmation, uncertain outcomes, retry deduplication and matching-draft preservation),
  `hub/codexcards_test.go` (list additions / removals, restoring cards after failed answers, silent reconnect, push titles).
- Real Codex: `scripts/test/test-codex.sh` (`docs/operations/testing.md`, "Codex integration testing") starts Codex in isolated herdr, asks questions and requests approvals, with one case per UI state and multiple turns each.
- App: `CodexCardsTests` (mock `MOCK_CODEX=1`), light / dark screenshots.

## Closeout

A Codex Agent learns the closeout's form (`docs/assistant/agent-work.md`, "Closeout") the way it learns its task: in the prompt. With the `closeout` triage switch on, the prompts of `task_plan`, `agent_start` and `dispatch_start` end with the form, whatever launcher runs them, and the first message is what a Codex session is started with ("Starting from the phone"). Triage reads the closeout from the last reply of the turn as the node reads it from the rollout, so a Codex closeout is parsed like any other (`TestCodexCloseoutIsRead`).

Two things Claude Code sessions have are missing here. Persona installs no Codex hooks of its own (the engine's integration only reports the session id, "Recognition"), so no Codex stop is held when a turn that did work ends without a closeout, and a Codex session started without a task prompt (from an app, in a terminal) is told nothing up front. Codex's own Stop and SessionStart hooks would need the user to trust them in `config.toml` first ("Research: app-server and approval hooks for Codex in Agents (2026-11-02, conclusions only, not implemented)" below). `assistant/codex_instructions.md` is the developer instructions of the Codex-run assistant, not of Agents, and assistant conversations write no closeout.

## Not supported yet

- Resuming from the phone covers a thread nothing has open in the default home ("Resuming from the phone"). Offline / archived history on external app-server instances is read-only; never silently start another app-server to continue it. Codex App threads continue through the App while it owns them; once the App has exited, the guarded same-home resume / takeover described above is available.
- Threads archived only in Codex (the Codex App's Archive) are not listed in Persona's archive; only Persona's own archives are mirrored to Codex ("Archive").
- Custom launcher functions can supply their own provider, home, or profile. Persona preserves their launch behavior and shows a task-card notice when an effort override requires the terminal route.
- Permission mode cannot be changed on the phone. Thinking effort and model change on app-server sessions ("Thinking effort") and, from Codex 0.169, on the terminal route ("On the terminal route"); TRAE CLI changes neither.
- Sessions started by launchers with arguments such as `-c` are not on the shared daemon and use only the terminal route (screen reading, typing).
- Unrecognized Codex dialogs (not matching "Questions and approvals" above) retain generic "Terminal needs attention" screen interactions.
- Queued messages, sub-Agents, background tasks, and other information unique to Claude transcripts are empty in Codex sessions.

<a id="internal-codex-fork"></a>
## TRAE CLI

[TRAE CLI](https://docs.trae.cn/cli_get-started-with-trae-cli-2?lang=zh) starts with the `traecli` command. Persona uses the product name for sessions, launchers, and approval cards.
In Agents you can see live state, send messages, open the terminal, and close sessions. Existing sessions keep their stored agent identifier.
The node integration (`server/internal/node/traex.go`) shares rollout parsing with Codex in `transcript/codex.go`.

- **Model selection**: New Agent lists the models with selectable configuration names from the computer's `traex models --json`.
  An explicit choice is passed as `--model` on a fresh start; Default leaves the CLI's own model preference in effect.
- **herdr does not recognize it**: TRAE CLI panes are ordinary herdr panes (`agent_status: unknown`, absent from `agent list`); herdr has no custom-Agent configuration either.
  The node finds them itself: `~/.trae/cli/thread-writer-locks/<session id>.owner.json` records the pid writing the session. That pid's environment contains herdr's
  `HERDR_PANE_ID` (Linux reads `/proc/<pid>/environ`, macOS uses `ps -E`), giving an exact pane → pid → session mapping without directory guessing. Pane information comes from
  `panes` in `herdr api snapshot`.
- **One process writes multiple threads**: before executing a tool, TRAE CLI sends that step to a review model for automatic approval assessment (guardian; `session_meta` has
  `source: {"subagent": {"other": "guardian"}}`, `thread_source: subagent`, and `session_id` / `forked_from_id` both pointing to the main chat).
  The same process writes this review chat, which has its own owner file and newer writes than the main chat. Selecting "the newest owner file per pid" displays the review chat in the App
  (review-request and assessment-result JSON), unlike the terminal (user feedback 2026-11-01). Now select the newest non-sub-Agent thread for each pid; if only sub-Agents exist, take their main chat.
  Older versions lacking these `session_meta` fields still use the newest owner file (`traexOwners`).
- **Infer state independently (fallback only)**: when Herdr or `herdr-core` natively reports lifecycle state (`Working`, `Done`, `Blocked`, or native `Idle`), the node retains that state instead of scraping the terminal. Only when no native state is available (including classic herdr's synthetic `custom:persona` resume fallback) does a braille spinner before the terminal title (`terminal_title`, not the stripped version) or "esc to interrupt" on screen map to Working, numbered choices such as "› 1." to Needs you, and anything else to `done` (read at most every 2 seconds).
- **Shared app-server writers and foreground resume**: when TRAE CLI runs conversations through a shared `app-server` process, that daemon can hold `.owner.json` writer locks for multiple terminals while retaining only its launching terminal's pane environment (`HERDR_PANE_ID` / `HC_TERMINAL_ID`). The node keeps Herdr terminals as the sole live session source (`traexAgents` in `server/internal/node/traex.go`), prefers an explicitly resumed conversation ID from the terminal's foreground client (`traecli resume <id>` / `traex resume <id>` / `--resume <id>`) over shared writer metadata, preserves valid host-reported sessions, and never assigns an unrelated shared-server conversation to a terminal.
- **Same file layout as Codex**, rooted at `~/.trae/cli`: `sessions/YYYY/MM/DD/rollout-*.jsonl` and `session_index.jsonl` (thread names for titles).
- **Paginated history (TRAE CLI 0.215)**: `state_<n>.sqlite` and `thread_history_<n>.sqlite` can be authoritative even when a rollout remains.
  The node uses the existing paginated reader, with the system `sqlite3` 3.33 or newer, to list and read these conversations, including stored items
  of a running turn and stopped conversations. Databases are opened read-only; generated transcripts stay in the node's private state directory.
  Per-thread projection revisions keep unrelated database writes from rebuilding every transcript. Older schemas retain the database-stamp fallback.
  Cold offline histories are not projected eagerly during listing; full history is projected when a conversation is read or watched, and live or just-stopped terminal conversations refresh their projection (`traexRefreshHistory` in `server/internal/node/traex_history.go`) so final reply text and nonzero completion timestamps stay attached to the matched conversation.
  Live writer locks and process ancestry select one main conversation per pane; nested exec and guardian threads cannot replace it.
  A fresh start excludes all historical IDs, and an asynchronous request waits for the CLI's actual thread instead of preallocating a Claude ID.
- **Conversation directory and terminal container**: the session's cwd comes from its TRAE metadata, not the host workspace.
  Orca may put a terminal for an unregistered directory in its active workspace and then change into the requested directory. Its workspace ID
  still describes that physical container; it does not change where the CLI reads or writes. Register the project in Orca when its terminal
  should belong to that project. Task permission handling remains as documented in [Read-only planned tasks](../assistant/todos.md#read-only-planned-tasks).
- **Newer rollout format**: user messages and replies are in `history_mutation.display_completions[].item` (same item structure as `item_completed`),
  while tool calls remain in `event_msg/item_completed`. The same item may appear in both places within a turn (deduplicate by id within the turn). A `history_mutation` line may span hundreds of KB to 1 MB
  (full context after compaction); decode only `display_completions`. `task_started` / `task_complete` are recorded in `Meta.TurnOpen`.
  `TerminalInteraction` (polling / sending input to a running command) is not displayed.
- **Messaging**: herdr's `agent prompt` recognizes only known Agents and returns not found for fork panes, so use `pane send-text` + Enter (`Node.prompt`).
- A phone message to an exited TRAE CLI session is refused: a stopped Agent is read-only (#4754). Resume on its terminal page resumes one Persona started (`<launcher> resume <id>`, `node/retire.go` `resumeOtherWith`); one started outside Persona returns an error (its launcher is not known).

## Research: app-server and approval hooks for Codex in Agents (2026-11-02, conclusions only, not implemented)

Read codex-rs source tags `rust-v0.153.4` / `rust-v0.163.0`, plus binaries for both versions (`--help`, `app-server generate-json-schema`).
"Source" means verified in code; "inference" means not run to validate.

### B. Switching to app-server

- **The TUI can connect to app-server and already is an app-server client** (source): it communicates only with app-server, either in-process, through a local daemon, or remotely
  (`tui/src/lib.rs`). `codex`, `resume`, and `fork` all have `--remote ws://|wss://|unix://`, without a feature flag. The daemon is `codex app-server --listen unix://`,
  with socket at `$CODEX_HOME/app-server-control/app-server-control.sock`. It carries WebSocket, not newline-delimited JSON: Persona's current assistant stdio client
  cannot be reused directly (`codex app-server proxy` only forwards bytes); the node needs a WebSocket client over a Unix socket.
- **0.163 uses the daemon by default** (source): `daemon_auto_start` is Stable and enabled by default; bare `codex` starts or joins the shared daemon (`--no-daemon` disables it).
  Arguments such as `-c`, `--profile`, `--enable/--disable`, and `--oss` fall back to in-process. Sessions started by launchers with `-c model_provider=…`, as in `docs/assistant/assistant.md`, are not on the daemon.
  Version 0.153 auto-connects only if the socket already exists; it does not start the daemon for you.
- **Multiple clients share one thread** (source, both versions): events go to all connections subscribed to the thread. Server requests (command / patch approvals,
  `item/permissions/requestApproval`, `item/tool/requestUserInput`, MCP elicitation) send the same request id to everyone; the first answer wins, and later answers only generate logs.
  Every subscriber then receives `serverRequest/resolved {threadId, requestId}`, causing the TUI to close its own prompt. A second client subscribes to a running thread with `thread/resume`,
  receiving replayed pending requests. This is exactly the required "phone knows when the computer answered first" behavior, without screen reading. It is all in stable schema without `--experimental`;
  both versions export identical approval, question, elicitation, and `serverRequest/resolved` parameters (0.163 removes `thread/rollback`, adds `thread/attachment/*`).
- **Costs**:
  - Covers only daemon sessions: 0.153 needs someone to start a daemon first; launchers with `-c`, `--no-daemon`, existing pure-TUI sessions, and TRAE CLI use the terminal route,
    so the rollout + screen route must remain. Version 0.163 rejects a second writer process for the same thread (`rollout/src/writer_lock.rs`); an already-running session cannot be taken over by starting another process.
  - The daemon inherits its starting process's environment (source). Inference: launcher environment and herdr's `HERDR_PANE_ID` come from the first pane to start the daemon, and herdr's
    SessionStart hook runs in the daemon too. Check how `herdr-agent-state.sh` identifies panes before using it.
  - Both app-server and daemon commands are marked `[experimental]`; 0.163 `daemon update` may interrupt active work.
- **Also makes "start Codex from the phone" feasible**: `thread/start` on the daemon, then `codex resume <id>` in a herdr pane (0.163 auto-connects; otherwise
  `--remote unix:// -C <cwd>`), so both computer and phone can view and answer. Inference: rollout is written only after the first user message; an empty thread may not be resumable,
  requiring `turn/start` before opening the pane. Simpler: run `codex "<first message>"` directly in the pane and identify the thread from `thread/started`.
- **Conclusion**: worth doing, but only for 0.163 daemon panes, retaining screen cards as fallback for everything else. **First step implemented**: see "Sessions on the daemon".
  Step two (phone creation) does not use `thread/start`; it runs `codex -- <first message>` directly in the pane ("Starting from the phone").
- **Relationship to existing implementation**: `codex-app-server.md` (dd83e7b0) already integrates external instances with Unix / loopback WebSocket endpoints, which includes the 0.163 shared daemon.
  However, live Codex panes in herdr are treated as terminal sessions first (`externalCodex` false), without joining their daemon threads. Next step could be: for 0.163 Codex in herdr,
  find a daemon owner (`codexOwner`), join if found, and use app-server for questions / approvals (either side may answer first, `serverRequest/resolved` removes cards);
  otherwise fall back to rollout / screen reads here. Already-joined threads are already skipped here ("Questions and approvals" above).

### C. Is there an approval-intercepting hook like Claude PreToolUse?

- **Yes: `PermissionRequest`, with the same format as Claude** (source). Both versions have identical hook-engine code; `hooks` is Stable and enabled by default in both. Configure it in
  `$CODEX_HOME/hooks.json` or `[hooks]` in `config.toml`. Events: PreToolUse, PermissionRequest, PostToolUse, PreCompact, PostCompact,
  SessionStart, SessionEnd, UserPromptSubmit, SubagentStart, SubagentStop, Stop, Interrupt.
- `PermissionRequest` runs first in the approval flow, before Guardian and terminal prompts. It covers shell commands (`Bash`), apply_patch (`apply_patch`, also matches
  `Write` / `Edit`), `write_stdin`, MCP tools (`mcp__server__tool`), network access (`Bash`, description `network-access <target>`), and `request_permissions`.
  Respond with `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"|"deny","message":…}}}`, or exit 2 + a reason on stderr
  to deny. No `updatedInput` / `updatedPermissions` support; with multiple hooks, any denial wins. Timeout defaults to 600 seconds and is configurable. Failure, timeout, or no decision
  falls through to the terminal prompt. Input includes `session_id` (thread id), `turn_id`, `cwd`, and `transcript_path`, so no session guessing is needed.
- **Limitations**:
  - The hook runs **before** the terminal prompt, not concurrently. Before the phone answers, the user at the computer cannot see or answer the prompt, conflicting with the requirement that "the phone notices if the computer answers first."
    A very short timeout (seconds to tens of seconds) can fall back to the terminal, but late phone answers then become useless.
  - User hooks run only when trusted: a `trusted_hash` in `config.toml`'s `hooks.state`, managed / system configuration, or
    `--dangerously-bypass-hook-trust`. Measured on 0.163 (re-check on the installed version), the details differ from what
    the source suggests:
    - the hash covers the handler configuration only, so editing a hook's `command` / `args` / `timeout` drops the trust and
      asks again, while an unrelated config change does not;
    - a separate `hooks.state` **file** has no effect; the state lives under `[hooks]` in `config.toml`;
    - the hook engine itself is on by default in the versions read above; where a machine reports hooks off, the switch is
      `[features] hooks = true`;
    - `--dangerously-bypass-hook-trust` given **before** `app-server` does nothing — it is not parsed as a daemon flag — so
      passing it on the daemon command line does not make hooks trusted. (An earlier note said the flag also disables the
      daemon in 0.163; this measurement neither confirms nor contradicts that, so re-test it before relying on either.)
    On the `app-server` daemon command line there is thus no per-machine bypass to rely on; Codex configuration must change
    on every machine.
  - Async hooks cannot make decisions.
  - `request_user_input` triggers only `PreToolUse` (tool name `request_user_input`), not `PermissionRequest`. PreToolUse supports only "deny + reason" or
    "allow with rewritten input", not a plain allow. An inferred workaround is to use the phone answer as a denial reason, which the model reads as tool output, but the question never appears in the terminal. `notify` has only `agent-turn-complete`.
- **Conclusion**: `PermissionRequest` can handle approvals in both versions, in-process and on the daemon. But it is mutually exclusive with "the computer user can answer first." This implementation therefore uses screen-reading cards,
  allowing either computer or phone to answer first. After B is implemented, both can answer without this hook. Before then, if fast phone answers matter more, reevaluate it as an optional short-timeout path.

### Orca hook ownership when starting Codex

A new local Codex launched inside Orca must keep that terminal’s hook environment. A shared Codex app-server daemon can retain a previously launched terminal’s Orca identity, attributing a new conversation and first-work naming to the wrong worktree. The phone-start helper probes the executable in the new terminal and adds `--no-daemon` when supported. It preserves explicit remote connections, already isolated launchers and older CLIs without that flag. Other terminal engines keep their existing launch behavior. A resume in Orca (#17044, "Resuming from the phone") does the same; it also runs the stock `codex` launcher with Orca's own Codex arguments, as a new start of it in Orca does.

## Speed

The web model panel lists the running model's advertised speeds. It offers Standard, Fast, Ultrafast and any other
tiers the runtime advertises, such as Flex. The control is independent of reasoning effort and takes effect on
subsequent turns. Settings > Agents > Codex also exposes the native speed default for new sessions.

An app-server thread uses `thread/settings/update` with the advertised native ID. The default ID is `default` and
the common Fast ID is `priority`. `thread/resume` and `thread/settings/updated` report the acknowledged speed.
Native null means Standard. Missing speed stays unknown because `thread/read` does not report it.

A live terminal-route Codex 0.169 or later uses the current model's tiers in its Codex home catalog. Startup settings
and the running-version check follow "On the terminal route". The node checks the terminal for a draft or dialog,
then types `/model --speed=<tier>`. The terminal takes `standard` for Standard and `fast` for Fast, with other tier IDs
passed through. The command changes speed independently of model and effort. A new `thread_settings_applied` event
confirms the selection before the next turn. A timeout reports an uncertain change. Panes without a verified rollout
or advertised tiers, screenless hosts and subagents do not offer this control.

The iOS main Assistant conversation reads the same session fields in its existing composer menu. A Speed section appears
only when the session advertises `default` or `priority`; it uses localized Standard / Fast labels, explains Fast's extra
quota use and says the choice applies to the next turn. Working does not disable it, while an offline or read-only action
carries a one-line reason. Each choice is its own `POST …/modes {service_tier}` and nothing is drawn optimistically: the
acknowledged session value owns the checkmark, summary updates preserve both fields, and a refusal or unconfirmed request
shows an error without moving the last acknowledged selection. Model and effort requests remain separate.

A new Persona conversation can carry the same native tier in `POST /api/v1/assistant/conversations` while the default
manager names `assistant_start_service_tier` in `manager.hello` and the hub lists that feature. Launcher model discovery
preserves `serviceTiers` as `service_tiers`; the web offers Fast only for a row that advertises `priority`, never from a
model name, and only while using the default manager. The node uses the same acknowledged `thread/settings/update`
after `thread/start` (or warm-thread adoption) and before the first `turn/start`, so effort and speed stay independent.
Omission sends no override and keeps the configured native default. A native refusal prevents the first turn, stops the
cold or adopted process, and saves no conversation, mode or recovery state. Validation and native failures use coded,
localized refusals (`assistant_service_tier_start_only`, `assistant_service_tier_unsupported` and
`assistant_service_tier_refused`) without exposing the native diagnostic as person-facing detail. A manager without the
capability gets 501 `assistant_service_tier_manager_outdated` before relaying; a node without `assistant_service_tier`
returns 409 `node_outdated`. An older hub ignores the unknown field, so the web neither offers nor sends it there. The
start choice is one-shot, not execution-fingerprint or recovery state; later changes still use `session.speed`.
New terminal Agent starts have a separate `speed` contract for Codex Fast / Ultrafast and Claude Code fast mode, including catalog validation, start-only semantics and the `session_start_speed` compatibility gate; see [Launchers](launchers.md) "Speed". The New Agent form itself still has no speed control.
