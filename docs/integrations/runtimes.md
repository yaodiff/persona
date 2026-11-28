# Integrating Agent runtimes

Agents in Persona come from two kinds of places, integrated differently:

| | Host | Runtime |
| --- | --- | --- |
| What it is | Where the terminal lives: herdr, Herdr App, Orca, Tern (`docs/integrations/engines.md`) | An Agent runtime that manages its own sessions and has no terminal: DSH (`docs/integrations/dsh.md`), later the pi extension, platforms built on DSH… |
| Where sessions come from | node reads panes, recognizes sessions by process, reads Claude / Codex transcripts | The runtime reports its sessions and provides their content |
| Interactions (questions / plans / permissions) | Claude Code hooks | The runtime asks itself (`interaction.create`) |
| Where the code lives | One host implementation in Persona (`internal/host`, `node/host*.go`) | **No code added to Persona**: the runtime implements the same protocol with the SDK |
| Per machine | Exactly one engine | Any number, alongside the engine |
| How the App knows what it can do | The host's capabilities (terminal, keys…) | Sessions carry `source_kind: "runtime"` and `capabilities` (the same fields as Codex app-server sessions), computed from the `caps` the runtime declares |

Paseo is a bit of both: it has no terminal and connects over the runtime protocol, yet it's an engine a machine can choose (`docs/integrations/paseo.md`).

This doc specifies the second kind: how a new runtime plugs in, what it must implement, and how it's accepted. The wire protocol is in `docs/protocol/protocol.md` ("Runtime ↔ node");
node's implementation is `server/internal/node/runtimes.go`.

## How multiple machines are aggregated

A runtime only connects to the **local** personad node (`~/.persona/runtime.sock`). Aggregating machines isn't the runtime's job; it's Persona's existing structure:

```
 phone / watch / web / Mac ── hub (one, always on) ── node (one per machine, over ssh or tailcat)
                                                 ├─ engine: Claude, Codex in herdr / Herdr App / Orca
                                                 └─ runtime.sock ◄── DSH, other runtimes (on this machine)
```

So "DSH on ten machines" = ten machines each with a node, each DSH plugin connected to its own node, and the hub merges them into one list (grouped by machine → space).
The runtime doesn't need to know where the hub is, open ports or authenticate (a 0600 socket for the same system user); networking (NAT, hole punching) is all handled on the node ↔ hub leg.

A runtime that can't reach the local socket (it's a long-running service: another system user, in a container, on another machine) connects the other way round and is called a **daemon** (`docs/integrations/daemons.md`):
it opens a WebSocket endpoint and node connects to it per `~/.persona/daemons.json`, with a token. Once connected it speaks the same protocol, and the SDK driver doesn't change a line
(`createRuntime(driver, {listen: {port, token}})`). The name comes from config and session ids are namespaced.
The daemon's config can enroll it in [A2A](a2a.md) for its own sessions.

For [A2A](a2a.md) tools, use `rt.tools({sessionId})` and
`rt.callTool({name, args, session_id: sessionId})` with the executing Agent's
session ID. The node checks runtime ownership before a call. A local runtime
that configures a provider's stdio MCP server can request a ticket with
`rt.a2aMcpToken(sessionId)`. Pass it as `PERSONA_A2A_TOKEN`, alongside
`PERSONA_SESSION_ID`, to a private `personad mcp` process. The ticket grants
that provider A2A tools for its own session and should stay in its MCP environment.

A runtime that releases idle sessions and can safely wake them with an
ordinary send may set `capabilities.a2a_wake_on_send` on those rows. The
node passes `a2a_wake_on_send: true` to `send` when delivering a task update
through that path. Decline if the session was manually stopped or archived.

## What to implement

With the SDK (`integrations/runtime-sdk`, Node 22+, no dependencies) you only write a driver. Two methods are required:

| Driver method | Returns |
| --- | --- |
| `listSessions()` | Session rows: `session_id`, `title`, `cwd`, `status` (`working` / `blocked` / `done` / `idle` / `offline`), `updated_at`, `rev` (changes whenever content changes; the App fetches new content based on it), `last_text`, `space_id` / `space_name` (sessions in the same space are grouped together in the App; a runtime without workspaces of its own gives the session's directory, and the node puts the session in that folder's space on the computer, the terminal workspace rooted there when there is one: `docs/protocol/agent-states.md` ("One folder, one space (#13533)")), `model`; optionally `permission_mode` / `effort` (the current mode), `transcript` (this session's chat is a local Claude transcript: the content belongs to node, see "Runtimes that run Claude Code" below), `codex_thread` (this session's chat is a local Codex thread, see "Runtimes that run Codex" below), `is_subagent` / `parent_session_id` (another of the runtime's sessions started this one, see "Sub-Agents and background tasks" below), `started_at` (when the session began: a child's card counts its running time from it) and `started_by_persona` (the hub started this session through the runtime or had it take the session over: the manager finds a start whose answer it lost among these, so say it after a restart too; a daemon's is dropped, #8130). A session `start` answered before it really began (see below) is listed with `starting: true` until it has, and once it failed as `offline` with `start_error` (why) for a while, as the node lists its own starts (`docs/protocol/protocol.md` ("New session")) |
| `items(params)` | One page of items (same params and result as `session.items`: from the end backwards by `turns` / `limit`, `before_seq` to page back, `after_seq` to catch up, `from_seq` + `through_seq` for a range), with `history` and `to_seq` |

A row's `last_text` is the agent's reply preview. When botmux delivers a reply to its topic, the agent can finish with a line containing only `BOTMUX_NOTHING_TO_SEND`. The node removes standalone marker lines from reported previews. A marker-only update retains the previous words for that session or subagent when available. Runtime drivers that shorten previews should remove marker lines before joining lines or clipping text. The full message remains available in `session.items`.

Optional (for each one present, the SDK declares the capability in `runtime.hello`; for those missing, node answers "not supported" for you and the App displays as usual):

| Driver method | Capability |
| --- | --- |
| `cursor(sessionId)` | The current `to_seq`, or `{to_seq, history}` (with history, a rewrite with the same item count can still be detected; if omitted, the `to_seq` and `history` of a `turns: 1` page are used) |
| `image(params)` / `item(params)` | Raw image data / the full text of a truncated item |
| `send({session_id, text, images, client_msg_id})` | Sending a message from the phone. **Dedup by `client_msg_id`**: the App resends on reconnect. Resolve to `{queued: true}` when the message only waits behind the running turn, so that nobody takes it for read (#8097) |
| `input({session_id, task, wait_id, line})` | `caps.input` on the local runtime socket. Deliver one owner-supplied line to the waiting command. See "Commands waiting for input" below. |
| `editQueue({session_id, keep})` | `caps.queue_edit`: keep the given queue indexes in that order. Return the original `{queue, images?}`; each `images` entry is the message's image array or `null`. Refuse an empty queue with `nothing_queued`, and invalid indexes with `queue_changed`. |
| `sendNow({session_id})` | `caps.queue_send_now`: start delivering the waiting messages without waiting for the active turn to finish naturally. Refuse an empty queue with `nothing_queued`. The SDK answers `{sent: true}` on success. |
| `interrupt({session_id})` | Stop the running turn. Resolve to `{settled: true}` once the turn has actually ended, and throw when it could not be stopped; resolving to nothing means "asked to stop, not confirmed" (#8097). A turn waiting on the user (a dialog, a card) must end too |
| `rename` / `close` / `start({cwd, prompt, client_msg_id, defer_mcp?})` | Interrupt, rename, close, create from the phone (with `start` and a configured `launcher`, the new-session form gains a launcher field). If present, `client_msg_id` identifies the first message, just as in `send`. Antigravity can also receive `images` on start, with or without text. The apps offer Rename and Close on its rows from `capabilities.rename` / `capabilities.close`, which carry `caps.rename` (not on a sub-Agent's row) and `caps.close` (#6294) |
| `hasLocalClient(sessionId)` | Whether this runtime's own UI is open for that session (decides who gets permission requests first, see below) |
| `unlistedTranscripts()` | The Claude session ids of sessions of its own that `listSessions()` no longer lists (archived in the runtime), or nothing while it cannot say yet; no capability is declared. See "Runtimes that run Claude Code" below |
| `modes({session_id, permission_mode, effort, model})` | Switching permission mode / reasoning effort from the phone (`caps.modes`; optional model changes require `createRuntime` option `modelSwitch: true`, advertised as `caps.model`; values are Persona's: `default` / `acceptEdits` / `plan` / `auto` / `bypassPermissions`, `low` … `max`). The apps show the mode chips on its rows whose agent is `claude` or the runtime's own, never Paseo's catch-all `paseo` (`capabilities.modes`, `docs/protocol/protocol.md` "Codex sources and capabilities"). A row may take modes and model away from its own session in its `capabilities` (`modes: false`, `model: false`: Paseo's archived agents, #8133), and a row whose agent is not `claude` may list the levels `effort` takes for it (`efforts`, weakest first), which the apps offer alone on it, and the permission modes it takes (`capabilities.permission_modes`, #8261: `default`, its own approval, and `bypassPermissions` where it has an Allow all), between which the apps offer a two-state switch, "Launcher default" and Allow all; node drops both where the runtime declared no `caps.modes` or the row took modes away, and `permission_modes` where the Claude Code chips stand |
| `rewind({session_id, prompt, occurrence, prompts_after, restore})` | Rewinding a conversation to before one of the user's messages (`caps.rewind`): find the message by its text, its occurrence counting from the end and how many of the user's came after it, and refuse (`rewind_mismatch`) rather than cut elsewhere. Its rows may narrow it per session in their own `capabilities` (`rewind: false`), and only a row's `rewind_code: true` lets `restore: "code_and_conversation"` through (`docs/protocol/protocol.md` "Codex sources and capabilities") |
| `fork({session_id, at_item_id})` | Branch an Agent or Assistant conversation (`caps.fork`) through the selected reply's turn, or copy all history when `at_item_id` is empty. Return `{session_id}` and publish the branch; an optional `session` row lists it before the next push. Refuse active turns (`fork_turn_running`) or stale replies (`fork_mismatch`). |
| `models({launcher, refresh})` / `check({launcher, cwd, prompt})` | The launcher's models as the runtime has them now (`caps.models`, the pickers' refresh) / Settings › Models "Test" run through the runtime with its own login (`caps.check`): one real prompt in the node's empty check directory, answered as `{ok, model, answer, error}` |
| `seen({session_id})` | The user saw the session in Persona (`caps.seen`, #6299): a runtime that keeps an unread mark of its own clears it, so the session is not unread in its own app too. The manager tells the node only while the runtime lists the session as `done` (unread), best effort |
| `archive({session_id})` / `unarchive({session_id})` | The runtime's own reversible archive (`caps.archive`, declared only when both exist): when the user archives a session in Persona, it is archived in the runtime too, and restored with it. Archive an idle session without stopping or closing anything (Persona refuses busy sessions first) and keep it restorable; both must be idempotent (already archived / restored is success). A runtime whose archive also stops the session, or that has no way back, leaves them out: the archive is then Persona's alone (`docs/protocol/protocol.md`, "Agent session archives") |
| `createWorkspace({cwd, workspace_id, branch, create_branch, base, name, key})` / `prepareWorkspace({cwd, repo_root})` | Workspaces of its own (`caps.workspaces`, declared only when both exist: the node asks where the worktrees go before each one it has made): a worktree it makes as a workspace of its own / the folder it puts a repository's worktrees in. With them it lists its workspaces through `rt.workspacesChanged(list)`. Only an engine's are used; see "Workspaces" below |
| `renameWorkspace({workspace_id, title})` / `renameWorkspace({space_id, title})`, `archiveWorkspace({workspace_id})` | With `caps.workspaces`: a workspace's title (resolves to `{workspace}`, its row now) or a space's name (resolves to `{space_name}`, the name it shows now) / the workspace archived, a worktree it made removed from the disk (its branch kept). Without them those requests are `unsupported`; see "Workspaces" below |
| `listHistory({since, until, cursor, limit})` | Sessions active in a time window, archived, finished and offline ones too (`caps.history`); returns `{rows, next_cursor, complete}`. See "History" below |
| `watchChanged(id, watching, {transcript})` | The phone starts / stops watching a session (when a new node connects or the runtime stops, all stop): for runtimes that only follow a session while someone is watching (Paseo subscribes to the timeline per session). A watch is the one sign that someone has the chat open: every App watches a chat it opens, the manager watches only the turn of a message a channel delivered there, and background reads never watch. A runtime whose engine would start a stopped agent just to read it (Paseo loads an agent it let go, relaunching its provider, #8132) reads such a session only while it is watched, and answers other reads from what it already has or refuses them with `RpcError(…, 'not_loaded')` (`items`, `item`) |

In `createRuntime`'s options, a runtime that runs several Agents can also pass `launchers: [{name, model, agent, models, auto_accept}]`: besides `launcher`, the new-session form then has these entries, one per provider,
each with the Agent and models it starts; when they change later, call `rt.launchersChanged(list)`. `auto_accept: true` marks an entry whose Agents ask for approval
unless a switch is on, which its `start` turns on for `permission_mode: "bypassPermissions"` (Paseo's Auto Accept): the manager starts its own Agents there in
`bypassPermissions`, as it starts Claude Code's (#8141). An entry may say what the apps call it (`display_name`, Paseo's label for the provider) and, when its
agent is not Claude Code, the permission modes a start on it takes (`permission_modes`, #8261): New Agent then offers those ("Launcher default", sent as no
mode, and Allow all with two of them) and only the efforts its model lists, rather than Claude Code's five modes or none. `resume: true`: `start` accepts `{resume: <Claude session id>}` to take over a Claude session it doesn't have
and keep that id (`caps.resume`, for runtimes acting as the engine to continue "Not running" sessions). `start` also receives the form's choices (`launcher`, `model`, `permission_mode`, `effort`,
`workspace_id`) and the id the hub named for the new session (`session_id`, #8130): give the session that id when it is valid and no
session of the runtime's has it, so that a start whose answer the hub never got is found by it; otherwise pick one's own. See
`docs/protocol/protocol.md` ("Runtime ↔ node"). A `start` that takes longer than the node waits (30 s) may answer with the row the session
will have, `starting: true`, and finish in the background: keep listing that row until the session runs, and should it fail, list it
as `offline` with `start_error` for a while rather than letting it vanish; a message sent to it meanwhile waits for it (resolve to
`{queued: true}` when it goes later than the call's wait), and is refused once the start failed (Paseo, `docs/integrations/paseo.md`).
An Antigravity supervised-task start may carry `defer_mcp: true`. A Companion that supports it creates the real ACP session and applies its model and mode, but returns its row with `starting: true` before attaching MCP or sending the first prompt. After the hub has linked the session's task role and supervision relationship, it activates that row with the ordinary first `session.send` (the same `client_msg_id`): the send resumes the ACP session with its Persona MCP servers, then prompts it. Closing the row before that send discards the unused session. An older Companion ignores the unknown field and completes the start normally, so the hub sends the first message separately only when the answer actually has `starting: true`; an immediate answer is never given a duplicate prompt.
The node does not take a session it saw listed only as starting or as failed for one of the runtime's once it leaves the list: a
Claude session whose takeover failed is the machine's again, sent to as a session not running. `readonly: true` (`caps.readonly`): a `start` with `permission_mode: "plan"` (an ordinary
plan-mode start, or a read-only task on a non-Claude runtime, #3238) runs in a read-only mode before its first prompt, or throws `readonly_unsupported` — never falling back to a writable
provider, and a takeover that cannot set the mode refuses too. Without it the node refuses plan starts for the runtime itself. Claude task dispatches use `bypassPermissions` and do not require this capability.
A launcher entry may carry `prompt_optional: true`: a Codex one the runtime starts with no first message too, so the apps do not ask for one.
`sameKind: true` (`caps.same_kind`, #8140, Paseo): its starts run only its own launchers. A `start` naming a launcher it does not have carries
`agent_type`, the kind of agent that launcher starts (`claude`, `codex`…): the runtime starts one of its own launchers of that kind, or throws
`launcher_kind_missing`, never another kind of agent nor a default of its own. Whatever the start asked for that it does not run with (that
launcher, a model its stand-in does not offer, an effort the model does not offer, lowered to the nearest it does) its answer says as
`instead: {launcher?, model?, effort?}`, the row saying what it runs with; the node passes it on, and the hub and the manager tell the user.
Where such a runtime is the machine's engine, its launchers are then all the new-session form lists for a start there (`launchers.list` with
that host), and a name it shares with a launcher of the login shell or of Persona is its own there (`docs/integrations/paseo.md` "A launcher
Paseo does not have"). Without it, a launcher it does not have reaches it as `shell_launcher` (it picks its own of that name, or its default).

Call the SDK when things happen:

| Call | When |
| --- | --- |
| (no call needed) | The list only needs recent sessions: node still recognizes older unlisted sessions as yours (ones listed before are remembered; ones never listed get one `items` query), and calls are still routed to you, so `send` / `items` must handle sessions not in the list |
| `rt.sessionsChanged()` | Sessions added or removed, a turn started / ended, a rename |
| `rt.append(id, {items, fromSeq, toSeq, history, update})` | New content was persisted (when no one is watching, the SDK just has the list refresh `rev`). `items` can be empty: content not shown in the chat must still advance seq. Set `update: true` to replace existing items by id without advancing seq; a batch keeps the flag if any append sets it. This flag stays inside the runtime-to-node protocol: App `items` events omit it because clients already merge by id and reserve `update` for the hub-update object |
| `rt.stream(id, text)` / `rt.streamEnd(id)` | The full text being generated / that segment ended (the SDK throttles to at most once every 120 ms) |
| `rt.workspacesChanged(list)` | Its workspaces changed (one made, renamed or archived, a branch moved): the whole list, which the SDK also tells every node it connects to later. Only with `createWorkspace` and `prepareWorkspace` ("Workspaces" below) |
| `rt.ask(interaction, {signal})` | A person must decide: the card goes to the phone and waits for an answer. Returns a `Resolution`; `{dismiss: true}` = handed back to the computer (tapped on the phone, expired, no hub), for the runtime's own UI. Meanwhile the session shows "Needs you" automatically |
| `rt.phoneFirst(id)` | Whether to ask the phone or the local UI first |
| `rt.tools()` / `rt.callTool({name, args, session_id})` | The hub's tools (the phone's `phone_*`, `mail_*`) for your own Agent: list them and register as your own tools; calls go through node to the hub (`createRuntime`'s `onHub` is called after hello and whenever the hub comes and goes, which is the right time to fetch tools) |
| Throw `RpcError(…, 'handover', {delivered: 'none'})` on send | A disk-only companion process: the session doesn't run here, and the program that can run it has been started; node hands the message to the runtime taking over (`requires: ['handover']`, DSH's `dsh-files`) |

`rt.streamEnd(id)` retires a published live text even after the final viewer leaves. It cancels pending text and
sends the empty boundary to the node while a stream entry exists, so the Hub clears its cached reply before a later
viewer subscribes. Call it whenever the generated segment ends, including while the session is unwatched.

What the SDK handles (no runtime needs to write it again): connect / reconnect / `runtime.hello` (with this process's `instance`: if another process uses the same runtime id, node refuses the connection; the SDK logs one line and retries every 30 seconds, without taking over), list pushes, watch backfill and continuity (marking honestly when `from_seq` / `to_seq` don't line up,
so the phone fills the gap), byte-by-byte streaming, the session turning "Needs you" while asking the phone, asking again with the same id for the same interaction when node restarts or its hub link drops (`interactionId(...)`; it waits up to 2 minutes from when the node or hub went away, however long the card was up before, #8128; a card under an id the runtime gives is resumable on the hub, so the phone keeps the card meanwhile and an answer given then is not lost),
withdrawing retracted interactions from the phone, handing the same `client_msg_id` to the driver only once for ten minutes (retries get the first result; a refusal with `delivered: 'none'` or an ordinary error can be resent at once, while `delivered: 'unknown'` stays cached so a reconnect cannot send it twice), the "who answers first" policy (when node says there's no hub, always the local UI first, `rt.hubConnected`), and reconnecting quickly after a drop (one attempt per second while the socket file exists). After the owner approves Retry and the node proves the original attempt `known_unseen`, `session.send_unseen` supersedes only that completed ambiguous attempt: a pending or acknowledged attempt keeps its outcome. An older SDK refuses the method as `unsupported` rather than risking a second send.

Things shared across runtimes but not part of the protocol are in the SDK too, for runtimes to use directly:

| What | Does what |
| --- | --- |
| `rt.recent(rows, {days, max, pinned})`, `rt.touched(id)` | Picks the sessions to list: running / waiting for an answer (`pinned`) and ones the phone touched (last 50) are always listed; the rest within `days` days fill up to `max`, newest first. A session open on the phone must stay listed |
| `rt.history(rows, {since, until, cursor, limit}, {complete})` (also `historyPage`) | Answers `listHistory` from all the runtime's session rows (or what it read from storage): window, order, paging and cursor. See "History" below |
| `page(items, params, {history, toSeq})` | Paging for `session.items` (from the end by turns / count, backwards, catch-up, by range) |
| `appToolName(name, own)` | Maps tool names to the set the App draws (`bash`→`Bash`, `read`→`Read`…); the runtime adds its own (DSH's `ask_user_question`, pi's `find`); parameter names are converted by each runtime |
| `scriptedModel()` in `testing/model.mjs` | A scriptable fake model for end-to-end tests (DSH's llm-mock-server, switching scripts by phase) |
| `conformance()` in `testing/conformance.mjs` | Generic end-to-end acceptance: text turns (thinking, reply, turn end, paging), approval, denial with a reason, questions, streaming, interrupt, push continuity; a runtime only passes its own tool parameters and then checks what's specific to it |

Not in the SDK (used by only one runtime, or the two read different things): each one's way of reading its session storage, and keeping companion processes alive.

## History

The list carries only recent sessions (`rt.recent`). A caller reading an older period (an extension looking back 90 days) asks for sessions by a time window instead (`docs/protocol/protocol.md`, "Session history"). A runtime that keeps older sessions implements `listHistory({since, until, cursor, limit})` → `{rows, next_cursor, complete}`:

- `since` / `until`: RFC 3339 (the SDK passes `until` as now when the caller left it out). A session is in the window when its time is in `[since, until)`; its time is `active_at`, else `updated_at`, else `started_at`.
- Archived, finished and offline sessions are listed too; archived ones with `archived: true`. Deleted ones never are (the SDK drops rows with `status: "deleted"` or `deleted: true`).
- Rows are `listSessions` rows, plus these optional fields when the runtime knows them: `active_at` (when it was last active, e.g. its last message; give it when archiving moves `updated_at`), `started_at`, `origin` (`user`, or `automation` for a session a scheduled job started) and `user_messages` (how many messages the user wrote in it).
- Newest first, ties by `session_id`; `limit` defaults to 200, at most 500; `next_cursor` is an opaque string, empty on the last page.
- `complete`: whether it can vouch that the window is fully listed (false when its storage was pruned).

`rt.history(rows, params)` does all of that from the rows it is given, so most runtimes write `listHistory: (p) => rt.history(allRows(), p)`. A session listed only here is still read with `items` (node asks the runtimes about a session it does not know); `send` and `watch` may refuse it with `not_found`.

## Content

- **seq**: each session has one monotonically increasing sequence number, starting at 1 (0 means "nothing yet"). An item's seq is the number of the record that produced it; one record can yield several items,
  with ids `<seq>#<index>`. DSH's event seq starts at 0, so it maps to seq + 1. **Ids are unique only within a session**: across sessions, and especially across two branches split from the same session,
  the same id is different content. Anything a client caches by id (render results, parse results) must key on the session too (#372).
- **history**: the identity of the session's content (`docs/protocol/protocol.md` ("Continuity of items")). It changes when content is rewritten (branch, reset), and the App refetches the whole page; it stays the same on pure appends. A batch with a new history doesn't need a larger seq (switching back to a branch with the same count, shorter after a rollback); the SDK pushes it anyway and continues from its `toSeq`.
  An explicit `fromSeq: 0` also resets the cursor, even with the same history; it promises that the batch contains the complete range through `toSeq`. Node and Hub retain the new cursor for reconnects. Catch-up pages report the actual cursor even below `after_seq`. An empty rewind notification retains its positive `from_seq` so clients reload the missing history. Node and Hub pass the known history in internal `session.watch` requests, so a same-length rewrite is reported after reconnect too. An unchanged idle watch sends no empty batch.
- **Item kinds** follow what the App already has: `text` (user / assistant), `thinking`, `tool_use` (`tool_name`, `tool_input`, `tool_use_id`), `tool_result`
  (`tool_use_id`, `is_error`), `image` (`lazy: true` placeholder, data via `image`), `notice` (system: interrupt, error, compaction).
- **Tool names match Claude's**: the App draws commands, diffs and files for `Bash`, `Read`, `Edit`, `Write`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, `TodoWrite`,
  and parameter names are Claude's too (`command`, `file_path`, `old_string` / `new_string`, `pattern`). Unrecognized names are shown as is.
- **Length**: in a page, at most 24 KB of text per item and 4 KB of tool output, cut with the SDK's `light(item)` (with the "truncated" note the App recognizes and `full_size`); the full text goes via `item`.
  Tool input objects use `clipInput`, which follows the hub's `clipJSON` in three passes. First keep at most 12 KB per string at any
  depth, including arrays such as MultiEdit's `edits[]`; if the input still exceeds 24 KB, share the remaining budget among strings
  longer than 256 bytes, accounting for JSON escapes and the truncation note. Cuts respect UTF-8 boundaries and the second pass only
  cuts when the string becomes strictly shorter as JSON. A plain ASCII tail of at most the note's encoded size stays whole (37 bytes
  for a three-digit original length); a longer or more heavily escaped tail may be cut.
  If the result is still over budget, a third pass drops tail items from the currently longest arrays (the most retained items,
  preorder breaking ties), then trailing fields from the currently widest objects the same way. Object children use Unicode
  code-point order for removal and preorder ties, matching the hub's UTF-8 byte-wise sort rather than JavaScript's UTF-16 or
  integer-first enumeration order. Surviving array items keep their order; surviving object fields keep each implementation's
  own output order and value types. For every top-level object accepted by the JSON decoder, the final encoded input is at
  most **24,576 bytes**, including the marker: one collision-safe top-level field named `zz_persona_clipped` (suffixed `_2`,
  `_3`, … if needed), whose value is an object of the form `{"note":"Omitted X array items and Y object fields.\n…（已截断，共 N 字节）"}`.
  The note counts direct removals and ends with the usual cut note and the original whole-input byte size. The object wrapper
  is deliberate: old and new clients' "first string argument" subject and summary fallbacks cannot mistake the marker for the
  call's subject. Typed tool projections ignore the unknown root field; generic argument and Raw Input views show it.
  Inputs at or under budget are untouched. Each implementation measures its own wire encoding: both leave `<>&` raw, but
  `JSON.stringify` also leaves U+2028/U+2029 raw while the hub's Go encoder escapes them, so their shares and output bytes can differ.
  Malformed input, trailing JSON and nesting the decoder rejects (deeper than 10,000 levels) pass through unchanged as before.
  A valid number outside float64 range, such as `1e1000`, is retried with `UseNumber` so its object is still clipped. Top-level
  arrays and other non-object inputs stay unchanged.
- **Only what people need to see**: context injected for the model (time, skill list, system instructions) and internal scheduling events don't become items.

## Commands waiting for input

For a new session started by Persona, a local runtime that implements `start` and `input` can include a `waiting_inputs` array in each live session returned by
`listSessions()`. For example, a command asking which workspace to use could report:

```json
{"session_id":"setup","waiting_inputs":[{"task":"setup-1","wait_id":"step-2","command":"example setup","prompt":"Workspace name?","output":"Choose a workspace.","masked":false}]}
```

The driver must honor the session ID requested by `start`. The SDK retains a node-issued proof in memory for that session and attaches it only to its negotiated live reports. This lets the node verify the destination again after a socket reconnect. A session discovered only through inventory, or returned under a different ID, has no binding and cannot request a protected input card. A new node or driver process needs a fresh bound start.

Use a fresh `wait_id` for each occurrence, even if the task repeats the same question. Keep it stable in subsequent
snapshots. Call `rt.sessionsChanged()` when a wait appears, changes or ends. Omit finished waits from the next snapshot.
The node creates the standard command-input card for the current owner of the session. Use `rt.nodeHas("input")` to check support. The SDK omits waits when a node lacks the feature.

In `input`, atomically verify and claim that exact session, task and wait before typing the line and pressing Enter.
Preserve the line's whitespace. Resolve to `{typed:true}` only once delivery is confirmed. If the wait has moved on,
throw an error with `code: "prompt_changed"`. A definitive refusal may also use `command_input_line`,
`command_input_unreachable` or `command_input_refused`. If delivery is uncertain the SDK returns
`command_input_unconfirmed`. Never route the line through a model, log it, store it or replay it after reconnecting.
Keep any terminal echo of secret input out of reported output and chat history. The node masks recognized secret prompts, or any prompt marked `masked: true`. **Not now** dismisses this occurrence without calling the driver.

The SDK exports the `WaitingInput`, `SessionInput` and `InputHandler` JSDoc types. In a driver test, use
`startFakeNode(t)` and call `node.peer.call("session.start", {session_id, input_binding: randomUUID()})` to start a bound test session. Inspect its next `runtime.sessions` snapshot and call
`node.input({session_id, task, wait_id, line})` to exercise the same wire request.

## Interactions

- Three kinds: `question` (`questions[{question, header, options[{label, description}], multi_select}]`; the answer is "question text → option text", multi-select joined with `, `,
  anything not among the options is what the user typed), `plan` (`plan` body; answer `approve` + `feedback`), `permission` (`tool_name`, `tool_input`, `summary`; answer `allow`).
- **Who answers first** (`answerFirst`): `auto` (default) = the phone first if the phone sent this session a message or created it in the last 6 hours, or the local UI isn't open; otherwise the local UI first.
  `phone` / `local` pin one side. "Hand back to computer" on the phone and expiry both return `{dismiss: true}`, and the runtime then hands it to the local UI, so it never ends up unattended on both sides.
  A session the hub started through another runtime counts as phone-driven here too: the node says so with `runtime.driven` (#8097).
- **Waiting in its own UI**: while a session waits on the user in the runtime's own dialog, call `rt.waitingOnUser(sessionId)` and the returned
  release when it ends: the session lists as `blocked` meanwhile, as for a card on the phone. A dialog nobody may be looking at (a web page
  with no browser open) must also be withdrawable: pass the turn's abort signal to it, so that an interrupt ends it (#8097).
- **Cards belong to the runtime**: node tags cards it asks with `agent` (the Agent the runtime runs, or the one the session's row names) and `agent_label` (its name: the row's own `agent_label` whenever it has one, else the runtime's `name`; a runtime running several Agents under its own id names each in its rows, as Paseo's providers, #8142), and the App draws them as "another Agent's card"
  (the same kind as Codex's: "DSH Needs Your Permission", Approve / Deny, `reason` above the command). **Always Approve** only appears when the card has `always_label` (saying exactly what is always approved);
  don't send it if the runtime can't do it. `always_label` itself is just text; "always" is enforced by the runtime (opencode's save rules and Paseo's suggestions are both narrower than the whole tool).
  If a runtime can't remember session rules itself (or withholds native session rules on Assistant sessions so Persona's command gate and revocation stay authoritative, as Antigravity does), it declares `caps.keep_always: true` in hello (the SDK's `keepAlways`):
  - For local `assistant_environment` runtimes (Antigravity), node computes scoped `tool:*` keys matching Claude Code hooks (`tool:Read(//dir/**)` / `tool:Edit(//dir/**)` for absolute non-root, non-home, non-space, non-escaping file paths, `tool:Bash` for inspected non-empty non-truncated Bash outside `origin:inbox`, and `tool:<tool_name>` for safe non-file/non-command tools — failing closed on unknown tools carrying path or command inputs); Assistant cards always clear `always_label` and keep ACP on `allow_once`, while non-assistant cards carrying a native `always_label` keep their native rule without hub keys so narrow rules are never widened.
  - For other `keep_always` runtimes (DSH), when a `permission` card carries `always_label`, node writes `always_keys: ["runtime:<runtime id>:tool:<tool_name>"]` (still withholding whole-tool Bash on `origin:inbox`).
  The hub remembers `always_keys` per session id, and later cards whose keys are all kept are answered directly with "allow + always + remembered" (#3974).
  `always_keys` sent by a runtime are always cleared; no key is written for runtimes without `keep_always`, `remote_agent` runtimes or sessions, spaces, or cards with `fields`. When the runtime runs Claude Code (`agent: "claude"`), cards are drawn as Claude's.
- **The text written on deny**: `Resolution.message`. If the runtime's permission can carry a reason, pass it; otherwise deny first, then steer that sentence into the turn (DSH does this).
- **"Needs you" doesn't mean there's a card**: a session waiting for a person in its local UI reports `blocked`, the phone shows "Needs you", and the hub doesn't read the screen to build a terminal card (never for sessions without terminal capability).
- Use the runtime's own "timed question" if it has one: if the local side times out first, the card on the phone can stay, and a late answer is handed back to the runtime (DSH's `userQuestions.answer`).

## Naming

- `runtime` id: lowercase, `[A-Za-z0-9][A-Za-z0-9_.-]*`; it's also the `space_kind` and the sessions' default `agent`. `name` is what the App calls it (sessions carry `agent_label`).
- If the runtime runs an Agent the App knows (say, a platform running Claude Code), set `agent` to that (`claude`) and the App displays it accordingly; otherwise leave it unset.
- A runtime id can be used by only one process per machine: to run the same program twice on one machine (two DSH profiles), make the id, `name` and `launcher` configurable;
  node rejects a second process with the same id (`runtime_conflict`). If a runtime changes its id but still runs the same kind of Agent, set `agent` to that Agent (a renamed DSH sets `dsh`), and session rows carry `agent_label`.
- Session ids are unique on the machine, at most 128 characters, and can't be paths. `launcher` names shouldn't clash with launchers in `~/.zshrc` (on a clash the shell's wins and the runtime's isn't listed, except where every start goes to the runtime as an engine with `caps.same_kind`: there its own wins);
  if two runtimes' launchers clash, only the one that connected first provides it.
  A local `agy` launcher supplies the model catalog and starts in a pane when the selected host is herdr (#10237),
  including when herdr is the default and the node detects the local CLI. The connected companion serves Assistant
  and terminal-less Agent requests (the node RPC can target `host: "agy"`); the assistant's `task_plan` and `agent_start`
  target it for an agy launcher it runs when they name no host, and a start targeting it while it is not connected is
  refused with `agy_companion_offline` (#13447). A shell alias, function or helper definition
  named `agy` still has precedence over automatic companion selection (#9181).

## Minimal example

`integrations/runtime-sdk/examples/echo-runtime.mjs`: sessions in memory; it "thinks" briefly and then echoes back whatever it receives, asking for a permission first if the message contains `ask`.
About a hundred lines, and it's the skeleton for a new runtime: replace `sessions` with your session storage, `reply` with your Agent, and the item mapping with your own
(see `integrations/dsh/lib/map.js`). Keep its entry check, which compares real paths: Node loads the entry module by its real path, so
comparing `process.argv[1]` with `import.meta.url` as given skips the program when the path goes through a symlink (#12848).

```js
import { createRuntime, light } from '<Persona>/integrations/runtime-sdk/index.js'

const rt = createRuntime({
  listSessions: async () => [...],          // session rows
  items: async (params) => ({ items, history, to_seq }),
  send: async ({ session_id, text, client_msg_id }) => { /* hand to the Agent, dedup by client_msg_id */ },
}, { runtime: 'acme', name: 'Acme', launcher: 'acme' })
rt.start()
// When content is persisted: rt.append(id, { items: [light(item)], fromSeq, toSeq, history })
// When a person must decide: const res = await rt.ask({ id, kind: 'permission', session_id, tool_name, tool_input })
```

## Runtimes that run Claude Code (optional)

When a runtime runs Claude Code (via the SDK, e.g. Paseo, `docs/integrations/paseo.md`), its sessions already have a local Claude transcript. If the session row carries
`transcript: <Claude session id>`, node serves the content from that transcript: items, images, watch, `session.info`, sub-Agents, the row's last line and "what it's doing",
with the same parsing and the same look as Claude sessions in herdr; the transcript's own "Not running" row is no longer listed. The runtime only handles state, sending, cards and modes;
on watch it receives `transcript: true` and only pushes streaming text (the SDK automatically drops `append` for such sessions). Sessions whose transcript hasn't been written yet get content from the runtime as usual,
and node switches over automatically once the file appears. A daemon can't do this (it would read other sessions on the machine).

Three things stay the runtime's word on such a row. Its own `background` list is kept beside the background tasks the transcript shows (#4494): work the runtime started for the
session outside Claude Code. If both sources name the same task ID, it counts once and the transcript keeps its richer local details
(including its output file, #9320). Its `model` decides the context window when it names a 1M variant (`…[1m]`, #4496): Claude Code writes the id without the suffix into the
transcript, and a gateway writes an id of its own, so the transcript alone reads as 200K, or as no window. A window the transcript records itself keeps the transcript's word,
and so does a session answering as another model: after a refusal's fallback, or when the reply came from a Claude model that is not the one the runtime names. And its `title`
(#8133): where the transcript records no title (a custom or an AI one), node takes a terminal session's title for its first prompt (`title_source: "prompt"`, which Persona's
automatic naming may replace), but a runtime's title is the first prompt only when it is what Paseo makes of that prompt (`server/internal/node/runtimetitle.go`). Any other
title (a rename in the runtime, one another agent gave) has no known provenance, and nothing replaces it.

**Sessions the runtime no longer lists** (#4521). A runtime that starts Claude Code's command line itself (Paseo does; a host on the Agent SDKs does not) writes transcripts
that say `sdk-cli`, like a script's one-shot `claude -p` run, and node leaves those out of the sessions not running. While the runtime lists a session, its row is the session.
Once it stops listing it (archived in the runtime), only the transcript is left and nothing in it says whose it was. So the runtime names them: `unlistedTranscripts()` returns
their Claude session ids, the SDK sends them with every list (`unlisted_transcripts` beside `sessions`, the feature `unlisted_transcripts`), and node lists those transcripts as
sessions not running, under the usual limits (7 days, the 80 newest). Name only the user's own sessions: not the ones another session started, not ones the user deleted. At
most 2000 count, the last ones named; a transcript that a row also names is that row. A named transcript is node's from then on, even when the runtime listed it before under
that id: node reads its content, and a message to it starts it in the engine again (`start({resume})`), never a call to the runtime about a session it no longer has (#8131).

- An empty list says there are none. While the runtime cannot say (it was just started and has not read its archive), `unlistedTranscripts()` returns nothing, the SDK leaves
  the field out, and node keeps what the runtime named before. If the method throws, the list still goes out, with the ones named last.
- After the runtime disconnects, node keeps them for as long as its list of sessions not running goes back (7 days), and with them the runtime's rows that name a transcript
  here (#8131): those stay listed under their ids, not running, read from their transcripts, with no controls but reading and sending, until the runtime lists again. A
  message, a rename or closing one is refused as `runtime_away`, nothing sent. So a runtime that restarts does not take its sessions off the list and bring them back, and
  one that stays away (its program stopped, the computer moved to another engine) leaves its Claude conversations readable meanwhile. Rows that name no transcript here
  leave with it. Node remembers none of this across its own restart.
- Name only a session whose id is its transcript's. Node lists a named transcript under the transcript's id, so a session the runtime lists under an id of its own (a session
  Persona started before Claude Code had a session id, as Paseo's `paseo-<agent id>`) would come back under another id once archived, and everything Persona keeps under
  its id (a to-do's link, a pin, an archive mark, its read state) would be left behind (#4771). Keep listing such a session yourself instead: an `offline` row under the same
  id, with `transcript`, bounded like node's own list (Paseo: the latest 80 of the last 7 days, beside its other rows). Node reads its content from the transcript and lists
  no second row for it, as for any row naming a transcript, so this needs nothing new from node. A message to it reaches the runtime, which continues it under that id.
- A row under an id of its own also names, for the manager, the other ids its conversation went by: node puts the transcript and the sessions it was forked from (each
  transcript's first record names its source in `forkedFrom`) in the row's `aliases`, lists none of them beside it, and the manager shows no archive record under one
  (`docs/protocol/protocol.md` "Agent session archives", #8131).

## Runtimes that run Codex (optional)

When a runtime runs Codex itself (e.g. Paseo), each of its sessions is also a Codex thread with a rollout on this computer, which node finds on its own and would list a second
time as Codex history. If the session row carries `codex_thread: <thread id>` (the feature `codex_thread`, #4495), node lists the chat once, as the runtime's session: the
thread's history row is not listed, and the thread's own sub-Agents (Codex thread spawns) carry the runtime's session as `parent_session_id` and sit in its space. Content,
sending and state stay the runtime's. When two rows name one thread, the one that is not `offline` counts, then the one used last. A thread is matched by its id, in whichever
Codex home its rollout is. A daemon can't do this: its threads are on another computer.

While the runtime's session is `offline`, another program may take the thread up (the Codex App runs it, a shared app-server has it loaded). That program's row is then listed
as well, with the thread's sub-Agents under it, so a message from the phone goes where the thread is open. Once the runtime's session runs again, the chat is that session alone.

## Claiming a Codex app-server instance (optional)

node attaches to every Codex app-server it finds on this computer and reads each one's loaded threads every 5 seconds (`docs/integrations/codex-app-server.md`). A runtime
that serves an app-server-compatible daemon's chats itself (a companion that ships with the daemon and talks to its app-server like any client) would have node follow the same
endpoint a second time: node keeps reading the threads the runtime already lists, and lists the threads the runtime's rows do not name (`codex_thread` hides only those they
name). So the runtime claims the instance in hello (#12571):

- hello carries `codex_claims: [{home?, endpoint?}]`; with the SDK, the `createRuntime` option `codexClaims` is a list, or a function that returns the list synchronously,
  asked at every hello. An entry names the instances with that Codex home, the instance at that endpoint (`unix://<absolute path>` or `ws://<loopback IP>:<port>`), or, with
  both, only the instance that has both. If other app-servers (the desktop App, for one) use the same home, claim by endpoint, or by home and endpoint together. node
  compares them as it resolves its own instances: a home absolute with its links followed (`~/` is node's home), a socket alias as its target. At most 16 entries count.
- node answers with the feature `codex_claims` (`rt.nodeHas('codex_claims')`); an older node ignores the field. Only a runtime on the local socket can claim: a daemon's
  app-servers are another computer's, and the link passes no claims on.
- While a claim holds, node closes its connection to a matching instance before it answers the hello, makes no `thread/*` call to it, and does not dial a matching instance it
  discovers later. The instance's live chats are then listed only as the runtime's rows. The home's rollouts are still read as history, and `codex_thread` keeps hiding the
  history row of each thread the runtime names.
- A claim holds for 60 seconds after the runtime's last connection leaves, so a runtime that restarts does not have node attach to the instance, list its threads under node's
  own rows and drop them again once the runtime is back. The runtime's own rows still leave the list while it is gone: as for any runtime, only rows whose conversation is a
  Claude transcript on this computer stay. After the 60 seconds node attaches again within 5 seconds, with no restart. A hello without `codex_claims` ends that runtime's
  claims at once. node logs when an instance becomes claimed, by which runtime, and when the claim ends.

A claim removes a duplicate reader; it is not a write lock. The daemon still orders the writes of all its clients, and the phone, the web app, the desktop App and the terminal
can all still write. Unlike `codex_discovery: false` (every discovered instance, `docs/operations/config.md`), a claim leaves out only the instances the runtime
serves.

## Sub-Agents and background tasks (optional)

When an Agent hands work off and ends the turn (background sub-Agents, workflows, commands running in the background), the session is "done" after the turn while the work isn't. The runtime reports them on the session row,
with the same fields as Claude sessions (`Session.subagents` / `background` in `docs/protocol/protocol.md`), which the App and hub already understand:

- `subagents`: `[{id, description, type, running, tool_use_id, started_at, updated_at, last_text}]`. `id` is the sub-Agent's id in the runtime (same character set);
  **its content is read by session id `<session>@<id>`**: node routes `session.items` / `item` / `image` / `watch` for that id to the runtime owning the parent session,
  and the runtime also uses that id when pushing `session.items` / `session.delta`; node always rewrites `session_id` to `<session>@<id>`, whatever the runtime writes.
  `tool_use_id` is the tool call that started it, and the App opens it from that call (the call name must map to `Agent`, parameters `{description, prompt}`). Sub-Agents aren't listed as separate sessions.
- `background`: tasks still running that will come back after the turn ends, `[{id, kind, description, command, started_at}]`; `kind` is `shell` (`command` is the command) or `agent`.
  `output_file` is a local file; runtimes don't provide it (node clears it). Foreground calls that are waiting for their result don't count.
- While any are running, the session counts as Working (#2228), and the hub's "done" push waits until they finish and the turn in which the session is woken up ends. Re-push `runtime.sessions` when they start and end.
- At most 50 sub-Agents and 50 background tasks per row, text cut to 500 characters; invalid ids are dropped. A daemon's sub-Agent ids are prefixed the same way: `<daemon>.<session>@<id>`.
- Controls such as messaging or renaming a sub-Agent: if the runtime doesn't support them, it returns `unsupported` (DSH doesn't: sub-Agents are managed by the parent session).
- **A session of its own that another session started** (Paseo's Agents created by another Agent, #4494) is not a `subagents` entry: it is opened, sent to and can need the user
  like any session. List it as its own row with `is_subagent: true` and `parent_session_id: <the starting session's id>`; node fills in `root_session_id`, and a daemon's
  parent ids get the daemon's prefix like its session ids. The hub sends no "done" push for it and holds its parent's while it runs or has background work. It is its parent's
  subtask: an App that folds subtasks lists it under its parent, and the fleet counts (the Live Activity, a phone's notification and widget) count the parent, not it.
  - While its `status` is `blocked` (it waits for the user) node lists it without its parent, so it is a row of its own in every App that folds subtasks by those fields: it
    shows it and counts it under Needs you, whatever it folds. Node names the parent in `asking_parent_session_id` meanwhile, so the Apps that show children as cards in
    their parent's chat keep it there (`docs/protocol/protocol.md` ("Session hierarchy")). It is listed under its parent again once it was answered.
  - List every session between a listed one and the session the line starts at: a row whose parent is not listed hangs nowhere.
  - Give the parent's row a `background` entry of kind `agent` for each one that has not finished (running, or waiting for an answer), so the parent reads Working until they
    report back. A parent that is `offline` meanwhile is left out of lists of what runs, and its subtasks with it: do not list it as `offline` while they run.

DSH's approach is in `docs/integrations/dsh.md` ("Subagents and background tasks").

## Workspaces (optional)

A runtime with workspaces of its own (Paseo's checkouts, worktrees and folders, #6295) can make them the computer's: the
workspace catalog lists them with or without sessions in them, its sessions sit in them, and "New worktree…" makes a
worktree as one of them. It implements `createWorkspace` and `prepareWorkspace` (the SDK then declares `caps.workspaces`)
and calls `rt.workspacesChanged(list)` with the whole list whenever it changes (`docs/protocol/protocol.md` ("Runtime ↔ node")).

- **A row** is `{workspace_id, title?, cwd, kind, branch?, space_id?, space_name?, repo_root?, owned?, pinned_at?}`. `workspace_id` is
  what its sessions name in their own `workspace_id` (the session id rule); `cwd` is absolute; `kind` is `checkout` (a
  repository's main checkout), `worktree` (a linked worktree) or `folder` (no repository; anything else counts as one);
  `space_id` / `space_name` are the space its sessions are grouped in; `repo_root` is the repository's main checkout;
  `owned` says the runtime made the checkout and removes it with the workspace. Optional `pinned_at` is the host's native
  workspace pin time (RFC3339), separate from Persona's session/project pins; absent, cleared or invalid means unpinned.
  A changed timestamp or an unpin is part of the next full push. At most 2000 rows; invalid ones are dropped.
- **Only an engine's count**: the node uses the list while the runtime is one of the computer's enabled engines (Paseo) on
  its own socket; a runtime that is connected but not enabled makes, lists and deletes no workspaces. Its rows are then `workspaces.list` rows whose `source` is the runtime id (`project_id` its `space_id`, `is_main`
  true for a checkout, `workspace_kind` `git` or `folder`), and its session rows keep their `workspace_id`. Any other
  runtime's `workspace_id` is cleared, as before, and a daemon never gets the feature.
- **Starting in one**: `start` gets the catalog row's id as `workspace_id`, sessions in it or not. One the runtime no longer
  has is refused (`workspace_missing`) rather than the session going elsewhere; a space id or a folder names no workspace.
- **Making a worktree** (`workspace.create`: "New worktree…", a new Agent's own worktree and the assistant's `agent_start`
  with a worktree): the node first checks what it checks for a terminal host (the source in a repository with a commit,
  a valid branch not checked out in another worktree, a folder that holds nothing), then asks for `{cwd, workspace_id?,
  branch?, create_branch, base?, name?, key}`: an existing branch checked out as it is (`create_branch: false`), else a new
  one cut from `base` (the source checkout's branch, or its commit when detached), `name` the folder's name; with neither
  branch nor name, the runtime names both. `key` is the same for a retried call. It answers `{workspace}`, the new
  workspace's row with the folder and branch it actually has, once the checkout exists; the node lists it at once.
  `name` comes as Paseo names a folder (`[a-z0-9]` runs joined by single hyphens, at most 50 characters), and the node
  has checked that nothing is at `<checkouts>/<name>`, an empty folder included: make the worktree there, never beside it,
  so the path the user was shown is the one made. The node gives the whole create one deadline (145 s, inside the hub's
  150 s), the time `workspace.prepare` took included.
- **Where** (`workspace.prepare {cwd, repo_root}` → `{checkouts}`): the folder the runtime puts that repository's worktrees
  in. The dialog's path must be in it; only the worktree's own folder name can be chosen.
- **Deleting one** (`workspace.archive`, #6297): only a worktree the runtime made (`owned`, which the catalog row carries
  too), and only when nothing could be lost that the user did not confirm: the node refuses while one of the workspace's
  sessions is at work (starting, working, waiting for the user, a state it does not say) or an Agent of another engine
  runs in the folder, and checks the changes the preview showed. Archive the workspace and its sessions, remove the
  folder, keep the branch; answer once it is done. A closed Agent's own worktree (`worktree/remove`) goes the same way,
  refused while a session in the workspace still runs.
- **Renaming** (`workspace.rename`, #6297): "Rename" on a catalog row sends `{workspace_id, title}` (its title only), and
  "Rename space" `{space_id, title}`: the space its sessions are grouped in, also when the apps named it by one of its
  workspaces. Answer `{workspace}` / `{space_name}`, and list the new names (rows and workspaces) at once.
- **Deleting** (`workspace.archive {workspace_id}`, #6297): sent only for a worktree the runtime made (`owned`), once the
  user confirmed the node's preview (the changes it showed, the sessions in it, the checkout's identity). Archive the
  workspace and its sessions, remove the worktree from the disk and keep its branch; answer once that is done. The node
  counts it deleted when the workspace has left the list and the folder is gone. Closing a workspace (archiving it and
  keeping its folder) is not asked of a runtime.

## Auto-update (optional)

When a runtime is **a plugin installed inside another program** (the DSH plugin), that program loads it once and keeps it; replacing the files has no effect until someone reloads it inside that program.
To avoid going to the computer and clicking every time, the plugin is split into two layers, and node brings the code over (#619):

- **Loader**: the file the host loads (DSH's `integrations/dsh/index.js`) only does "pick a version, load it, swap versions", using the SDK's `createLoader`
  (`runtime-sdk/lib/loader.js`), and is no longer changed. The **implementation** (`dsh/lib/plugin.js` and everything it imports) is stored per version under
  `<store>/<hash>/`, and the loader imports from there. The bundled copy is also copied there first and then loaded, because the host's module cache is keyed by path: when the plugin is reloaded in the same process,
  an unchanged path means the old code. The store sits next to node's socket: `~/.persona/runtime-packages/<package name>/`, with `current.json` recording which one is running.
- **Package**: a set of files and their hash (sorted by path, one `path\0sha256 of content\n` per file, then a sha256 over the whole). Which files belong to a package is listed twice, in Go
  (`Packages` in `server/internal/runtimepkg`) and JS (`PACKAGES` in `runtime-sdk/lib/package.js`), and the two must match
  (guarded by `TestPackagesMatchTheLoader`). Before building personad, run `go generate ./internal/runtimepkg`, which packs these files from `integrations/` and embeds them in the binary
  (gzipped, a directory several packages share stored once; a package is unpacked the first time node needs it)
  (the Dockerfile, `deploy.sh` and `publish.sh` all run it), so node always carries the plugin from the same build as itself. A build that skipped generation carries no package and offers nothing.
- **Flow**: hello carries `package: {name, hash}`; when node carries a package with the same name and a different hash, the reply includes `update: {name, hash, version}`.
  The SDK passes it to `onUpdate`; the loader fetches the files with `runtime.package`, checks paths, sizes and hashes, and writes them to `<store>/<hash>/` (written alongside and then renamed, so there's never a half-written version).
  It then **waits until the phone has no cards from this runtime** (`rt.busy()`, at most 2 hours), stops the old one, imports the new one and starts it. If the new one fails to start, it goes back to the old one
  and doesn't retry this hash in this process. When the new one connects to node, hello reports the new hash and node stops offering it. If the `current.json` receipt cannot be saved, the loader stops the new version
  before restarting the previous one and doesn't retry the failed hash in this process. Removing older stored versions is best-effort: a cleanup failure is logged and the new version stays running.
- **Which wins**: whatever node carries is what runs; versions aren't compared as newer/older (if node rolls back, the plugin rolls back too). One exception: if the bundled files were replaced by someone (a new copy installed by hand),
  their hash differs from what `current.json` records, so the hand-installed copy runs first, and node offers its own if different.
- **Security**: only runtimes connected over the local `runtime.sock` (same user as node) can get packages. Daemons can't (the link doesn't forward `package`,
  and `runtime.package` returns `unsupported`), and the DSH plugin in daemon mode doesn't accept updates by default either (`autoUpdate`); to change its version, restart it.
- **The first time**: an old plugin without a loader has to be reloaded once by hand in the host to switch to the loader; after that it's always "release → hub → node → plugin". Changing the loader itself also needs one manual reload, so it should be as thin as possible.
- **Another kind of host: one that reloads by itself (pi)**: pi reads extensions from disk on reload and doesn't cache modules, so it doesn't need the loader layer. The extension is installed from a fixed path in the store,
  `<store>/current/…` (`current` is a symlink to `<hash>`, replaced by rename via `installCurrent`). On an offer it fetches the package, installs it as the new `current`,
  and when idle asks the host to reload (pi uses a command the extension registers itself); the new version's host connects to node with the same process identity (`createRuntime`'s `instance`) and replaces the old one.
  A long-running standalone process (pi's and Antigravity's `companion.mjs`) exits once idle after installing and is brought back up by launchd / systemd (`--no-update` opts out). The installer passes its own running hash to `installCurrent`, which retains that version, `current`, the previous version and the three most recently installed other generations; older idle versions and day-old temporary files are still removed, so a busy companion survives consecutive upgrades without unbounded idle-version growth. Antigravity companions also register their hash under `<store>/.active/<hash>/` for their process lifetime, protecting every active companion when another process installs. Installation and registration share a nonblocking, crash-recoverable `.install.lock`; lock contention retries asynchronously with the already verified package and without blacklisting the offered hash. A transient package-fetch failure also remains retryable, while a hash/content mismatch is not fetched again. Every completed hello cancels an older pending retry before that hello's offer is handled, so reconnecting to a node that rolled back to the running hash cannot later publish a stale offer. Updates wait for that completed hello (`rt.ready`, rather than merely an open socket) and check that its offer is still current after every asynchronous fetch; a retry therefore cannot install or recreate itself while disconnected, negotiating a replacement hello, or after a no-offer rollback. A valid response whose package hash differs from the hello's offered hash is not installed or cached for a later retry. Process start identities detect PID reuse (Unix `ps` reads are pinned to UTC and the C locale), cleanup reclaims registrations whose process is gone, and it preserves all generations if registrations cannot be read. Reinstalling a cached generation, including a rollback, refreshes its modification time so it counts as recently used. `selfPackage(import.meta.url, name)` tells a module which package it's running from
  and whether it's inside the store (if not, it doesn't update). The lock coordinates installers and Go built-in writers that use this SDK contract; an already-running legacy installer does not know the lock and cannot be made mutually exclusive with new cleanup in the same store. The supervisor must restart old companions once onto the fixed code, and rollback to an unlocked installer brings back its old pruning behavior. Details in `docs/integrations/pi.md` ("Automatic updates") and "Antigravity CLI" below.
- An implementation module that can be swapped exports `api` (`LOADER_API`, currently 1) and `start(host)`, returning `{stop, busy, fetchPackage}` (the methods of the same name on `createRuntime`).
  `host` holds the loader's `context` (for DSH, `{ctx, config}`) plus the `package` and `onUpdate` to pass to `createRuntime`. To add such a runtime: register its files in both lists,
  and change the entry point the host loads to `createLoader`.

## Acceptance

A connected runtime must pass these (`integrations/runtime-sdk/e2e/echo.mjs` is the template: a real `personad node`, with the script acting as the hub;
`integrations/runtime-sdk/testing/node-harness.mjs` starts node, and `testing/fake-node.js` is for unit tests):

- [ ] After connecting, its launcher (if any) is in `launchers.list`, and its sessions appear in `sessions.list` with `agent`, `agent_label`, `space_kind`, `source_kind: "runtime"` and `capabilities`, and no `pane_id`
- [ ] Create from the phone, send messages (resending the same `client_msg_id` gets in only once), interrupt
- [ ] Content: the person's messages, thinking, replies, tool calls and results are all there; paging back from the end and catching up both work
- [ ] Every batch watch pushes continues the previous one (`from_seq` = the previous batch's `to_seq`)
- [ ] Streaming output
- [ ] Permissions / questions / plans reach the phone and answers get back to the Agent; when the phone hands it back, the local UI pops up as usual
- [ ] Terminal-type calls return `unsupported`; after the runtime exits, its sessions disappear from the list
- [ ] (With sub-Agents / background tasks) when the turn ends while they're still running, the session row's `subagents` / `background` list them; `<session>@<sub-Agent>` can page and watch; when they're done, the row says so
- [ ] (Installed inside another program, using the loader) when node carries another version, it swaps it in and keeps working, and after the host restarts it runs the swapped-in one (`integrations/dsh/e2e/update.mjs` is the template)

These checks are `testing/conformance.mjs`: the end-to-end tests for DSH (`integrations/dsh/e2e/run.mjs`, a real `dsh web`) and pi (`integrations/pi/e2e/run.mjs`,
`herdr.mjs`) are all "start the host + open a session + `conformance()` + a few checks of its own".

## Assistant (optional)

A runtime can be the backend of the "Persona" tab's assistant, the AI twin, Lark `/agent`, and scheduled tasks (all of which go through node's `assistant.send`), alongside Claude Code and Codex:
`createRuntime(…, {assistant: true})` (the driver must have `start` and `send`) declares `caps.assistant` in hello; its launcher in `launchers.list` then carries
`assistant: true`, the App's assistant launchers include it (`Launcher.runsAssistant`), and the hub accepts it as `assistant.launcher`. The assistant layer stays in node
(`server/internal/node/assistant_runtime.go`, which names no runtime in code): chat state, the live snapshot at the start of each message, permission mode, shared memory,
Lark tokens and gates. node drives it with calls the protocol already has:

| node sends | With (all optional fields; runtimes that don't declare `caps.assistant` never receive them) |
| --- | --- |
| `session.start {cwd, assistant: true, supervision_instructions?}` | `cwd` is the assistant directory (`~/.persona/assistant`, containing bounded `AGENTS.md` / `CLAUDE.md` entry files, generated native rules and `.agents/skills/`); no `prompt`. The runtime reports this `cwd` for the chat's whole life (`runtime.sessions`, its cards), not where a tool's `cd` left the shell: the hub places assistant and health chats by that directory exactly (#2368). `supervision_instructions` is sent only when nonempty and the runtime also declared `caps.supervision_policy` (#11173) |
| `session.send {session_id, text, images, client_msg_id, assistant: true, context, mode, memory_dir, supervision_instructions?}` | `context`: a `<persona-context>` block given to the model (the live snapshot; the first message of a new chat also has memory and Lark notes), not shown in the chat, and left out of a message that is only a slash command (`/compact`, one line, no picture) so the runtime can run it: the snapshot goes with the next message, and after `/compact` in full. For an Antigravity native workflow command (`/boost`, `/teamwork`, `/swarm`, `/goal`, `/plan`, `/grill-me`, `/learn`, `/schedule`, `/browser`), `text` is already trimmed to start at byte 0, and the runtime puts its text block before images and the context resource last, so agy sees the command before Persona's context body. Other messages keep the context resource first; `mode`: Persona's permission mode (`default` / `acceptEdits` / `auto` / `plan` / `bypassPermissions`); `memory_dir`: the memory directory; `client_msg_id`: the id the message came with (the App's, a channel's), absent when it came without one: put it on the message's item, or leave the item without one, never another (`docs/protocol/protocol.md` "Matching pending messages"); `supervision_instructions`: sent only when the runtime declared `caps.supervision_policy` (always present, including explicit `""` to clear a prior role on demotion) |
| `session.interrupt` | Stop on the phone; closing the assistant chat = stopping the running turn, the chat stays in the runtime |

A **local** runtime can opt into `caps.assistant_environment`. Node then supplies `env` as an object mapping environment variable names to values on `session.start`, `session.send`, `session.modes`, and an assistant `session.fork`. This is the existing generic Assistant environment: the generated `assistant-bin` / `assistant-bins` command directory first on `PATH`, `PERSONA_ASSISTANT_BIN`, `PERSONA_SPACE`, non-proxied gate variables, and—for Auth-backed or relayed (`socket_env`) command gates—a session-scoped local gate proxy address in `base_env` reused idempotently per `(session, command)` across turns, mode changes, forks, interrupt-only assistant closes, and same-instance reconnects until an explicit runtime close, instance replacement, or disconnect past the reconnect grace window. It never contains Auth-held credentials, raw `socket_env` paths, a direct upstream replacing their proxy address, or raw secret references; the node's gate proxy injects credentials and enforces gate and space rules. Missing credentials stay absent rather than substituting the raw configuration. For eligible static proxy-authenticated Lark send/reply commands routed through `assistant_command_gate` or the node's MCP command-gate tool (`assistantGateTool`), node also declares `editable_text`, applies edited Allow answers by rewriting the JSON body inside `gateProxy` (bound once to the unmodified original request), preserves detached `pending: true` dismissals so the runtime cancels or finishes the turn cleanly without executing or retrying, and returns explicit owner deny notes (without `reason: "gate_rule"` on `assistant_command_gate`, and in the tool error result on `assistantGateTool`).

Apply a snapshot only when spawning that conversation's **dedicated Assistant worker**. Keep it with the conversation for an idle/crash replacement and use the newest supplied snapshot for the next worker; changing an already-running process's environment is neither required nor possible. Never put it on the companion, model probes, or the shared ordinary-Agent worker, and never log its values. Runtimes without the capability and daemon / remote runtimes receive no `env` field. Their wire contract and gate-only credential boundary stay unchanged.

Antigravity sends `context` as an ACP embedded text resource before the separate user text block, except for its
native workflow commands: there the text block comes first and the resource comes after any images and the
refused-image notice. Plain text blocks
are subject to native slash-command expansion, so auxiliary context must not use that channel, even in its own block.
An explicit standalone command still uses a plain text block without context.
Antigravity natively loads the generated flat `.agents/rules/` files with `trigger: always_on` on every turn. Other
runtimes have no verified native rule-directory contract: when the complete instructions exceed the bounded entry,
node includes the exact remainder in `context` on every non-command message, so later turns and post-compaction turns
do not lose it. This is not a replacement or summary of the runtime's `AGENTS.md`; it is the continuation of that file.

For sessions it starts, the node restores persisted ownership and launcher metadata before publishing runtime rows.
The original assistant or health directory identifies the conversation after rediscovery and reconnects. Runtime
status, model, permissions and capabilities continue to describe the running session.

For shell calls to `personad tool --cli`, a runtime can use `rt.assistantPid(sessionId, pid)` from the runtime SDK.
Report a dedicated local worker for each assistant conversation and pass `0` when that worker is dropped. Never report
the main runtime process or a worker shared with ordinary Agent sessions. The node checks the session's ownership and
assistant status, and verifies the worker is a strict process descendant of the connected runtime. It derives the
caller's session from the worker's process tree, not from the CLI request. Antigravity runs each active assistant chat
in its own ACP process, separate from the Agents' shared process. Both an idle assistant worker and the shared Agent
ACP process exit after five minutes without work, preserving each session's reported context window; the next send or
mode change resumes the conversation in a fresh process (#15253). A forked assistant chat gets its own
worker when first used. An active turn or unanswered permission keeps the worker alive, and an Agent turn
running in the same companion keeps assistant workers warm too: the assistant that supervises it hears from it when it
ends, so the five minutes count from the end of the last such turn (#13471).

A runtime that declares `caps.assistant` must:

1. For **assistant chats** (started with `assistant: true`, or whose directory is the assistant directory), always send permissions and questions to the phone first (when there's a hub), regardless of whether the local UI is open.
2. Attach the tools from `mcp.tools {assistant: true}` to the model (the phone's and mail's, plus assistant-only ones like `ask_user`, `schedule_followup`… and the tools declared by extension command gates);
   assistant-only tools go only to assistant chats, called as `mcp.call {…, assistant: true}`. node treats a call as the assistant's only when "this runtime declared the assistant, the session is its, and it's an assistant chat";
   otherwise as a regular tool (with no assistant-only tools).
   A runtime that gives an assistant chat Persona's MCP server instead (`personad mcp` with `PERSONA_ASSISTANT=1` and the chat's `PERSONA_SESSION_ID`, as the Antigravity runtime does)
   must start that server in its own process tree: node takes the server's word only from a descendant of the process connected to its runtime socket with `caps.assistant` (`node/clicall.go` `mcpAssistantOf`),
   and only for a session id of the runtime's own shape (not empty, not a Claude or Codex uuid) that, once the runtime lists it, is this runtime's assistant chat.
3. Lark always goes through tools declared by extension command gates (`proto.CommandGate.Tool`): node first passes the Lark gate (reads allowed, writes others will see confirmed on the phone, configured rules deny), then runs
   the skill's script on node; Auth-held credentials live only in node's proxy, never in the runtime. The generic opt-in snapshot above excludes those credentials and their proxy address. The tools carry `shell` (the command name and the skill script's path); when the runtime sees them in the assistant chat's shell
   it blocks them and says which tool to use (DSH's `onPreExecute`).
4. Give `context` to the model before the user's text and don't show it in the chat (a `<persona-context>` block without attributes; context quoted from the phone has attributes and is shown as usual).
5. Set its own permissions per `mode` (plan = read-only); changes inside `memory_dir` aren't asked about outside plan mode.
OpenCode's companion follows the same dedicated-worker boundary as agy: Assistant conversations use separate ACP process trees, ordinary Agents share a process, and only dedicated worker PIDs are reported. Idle retirement revokes the MCP URL and PID; resume uses `session/resume` and replays acknowledged options. Its plugin path cannot isolate a server process and rejects ambiguous same-directory tool ownership. Its separate instruction channel is advertised only after a pinned version and effective-config startup self-check; prefer advertised ACP systemPrompt append, otherwise use process-local instructions. The plugin always lacks that capability. See `docs/integrations/opencode.md`.

6. **Optional supervision policy channel (`supervisionPolicy: true` → `caps.supervision_policy`)** (#11173): only declare this if the runtime can apply `supervision_instructions` on an authoritative channel separate from user message text, persisting across compaction and isolated per session (never rewriting shared `~/.persona/assistant/AGENTS.md` and never leaking policy into concurrent ordinary chats). The runtime must refuse a policy change while the session is busy with `code: "busy"`, clear its per-session policy when `supervision_instructions` is `""` or the session closes, and fail closed with `code: "assistant_supervision_runtime"` (never sending the prompt) if the policy cannot be applied. Runtimes that do not declare `caps.supervision_policy` never receive `supervision_instructions` and keep the existing refusal boundary for supervision deliveries (`docs/assistant/assistant.md` "Role-scoped supervision instructions (#8251)").
7. **Optional remote assistant runtime (`caps.remote_agent`, `nwverse/persona#13305`)**: runtimes that back the assistant remotely (cloud APIs or sandboxed extensions without local filesystem access to `memory_dir`) declare `caps.remote_agent: true` alongside `caps.assistant: true`. The node advertises two additional MCP tools (`memory_read` and `memory_write`, implemented in `server/internal/node/assistant_runtime_memory.go`) in `mcp.tools {assistant: true}`, executes them in `mcp.call` scoped to the assistant shared memory directory (`memory_read` returns empty content when the memory directory or note does not exist yet; `memory_write` is refused in `plan` mode; both are refused for guest or non-assistant sessions), instructs the model in `runtime_instructions.md` to read/write shared memory via those tools, and enforces confirm-writes posture on command gates (converting configured write allow rules to confirm, ignoring `larkAlways`, and omitting `AlwaysKeys` even under `bypassPermissions`).

The assistant launcher's entry in `launchers` may also carry `assistant_for`: the name of an Agent launcher on the same computer that runs the same agent with the same
account but can't run the assistant itself (a Paseo provider; the Grok companion's `--assistant-for grok`, `integrations/grok/README.md`). The person setting the runtime up
declares it; nothing guesses it from names. `launchers.list` passes it on only for the launcher that runs the assistant, and the main chat's picker then points that
launcher at this one (`unavailable[].instead`, [model-config.md](../assistant/model-config.md) "A conversation's own model"). Nothing is ever run on one in place of the other.

Model and reasoning effort are up to the runtime (DSH picks them in its Web GUI); Persona's model config doesn't pick a specific model for such launchers. DSH's approach is in `docs/integrations/dsh.md` ("Assistant"),
end to end in `integrations/dsh/e2e/assistant.mjs`.

## Existing runtimes

| Runtime | Where | Doc |
| --- | --- | --- |
| DSH (`dsh`, configurable) | `integrations/dsh`: a Cordis plugin for `dsh web`; companion process `companion.mjs` (`dsh-files`) for when DSH isn't running | `docs/integrations/dsh.md` |
| pi (`pi-<pid>`, `pi-web`, `pi-files`) | `integrations/pi`: pi extension (one connection per pi process) + pi-web companion process | `docs/integrations/pi.md` |
| omp (`omp-<pid>`) | `integrations/omp`: the pi host under omp's identity (agent and name `omp`); no companion and no auto-update | `docs/integrations/omp.md` |
| opencode (`opencode` / `opencode-<pid>`) | `integrations/opencode`: embedded `persona-opencode-runtime` ACP companion (`companion.mjs`, auto-supervised by `personad` via `server/internal/node/opencodebridge.go` when `opencode` is installed and no external companion or `opencode serve` plugin is connected; `start`, `models`, `modes`, `assistant: true` with loopback HTTP MCP proxy, lazy/idle ACP cleanup, reads 1.x & 2.x `opencode.db`) and opencode server plugin (`index.js`, one connection per `opencode serve` process, merges with herdr TUI panes) | `docs/integrations/opencode.md` |
| Grok CLI (`grok-chat`) | `integrations/grok/companion.mjs`: optional standalone ACP companion with assistant tools, permission hooks, native renaming and restart recovery; also lists conversations started in a terminal (read-only while the terminal holds them) | `integrations/grok/README.md` |
| Hermes Agent (`hermes`) | `integrations/hermes/companion.mjs` provides an opt-in workspace launcher via the official `hermes acp` transport | `integrations/hermes/README.md` |
| Antigravity CLI (`antigravity`) | `integrations/antigravity/companion.mjs`: companion process; reads CLI transcripts and runs terminal-less Agent and assistant sessions through `agy acp` | "Antigravity CLI" below |
| Managed Agents (`managed-agents`, configurable) | `integrations/managed-agents/companion.mjs`: standalone companion for Volcengine Ark Managed Agents and compatible endpoints (cloud or self-hosted, the user's own key); reads sessions over the REST API, sends, interrupts, starts, and answers tool confirmations and question / plan tools on the phone; `--listen` makes it a daemon | `docs/integrations/managed-agents.md` |
| echo (example) | `integrations/runtime-sdk/examples/echo-runtime.mjs` (with `--listen <port>` it's a daemon) | "Minimal example" in this doc, `docs/integrations/daemons.md` |

### Separate task follow-up instructions

Assistant support and supervision support are independent. A launcher may run an ordinary
Assistant chat without an authoritative instruction channel; its tasks then use the existing
compatible-supervisor fallback. Launcher inventory exposes `supervision_policy` only on the
runtime's primary Assistant launcher after its verification succeeds. An absent capability
never grants supervision. Inventory is descriptive; the execution path checks again before sending.

| Engine / path | Separate supervision instruction channel |
| --- | --- |
| Claude Code / Messages | Node-owned appended system instructions |
| Codex app-server | Node-owned developer instructions |
| Antigravity CLI | Isolated worker policy directory in each Assistant conversation's own process; checked against agy 1.5.14 (#12883) |
| OpenCode ACP companion | Isolated worker; pinned-version startup verification of process instructions, preferring ACP append when advertised |
| OpenCode plugin | No isolated instruction channel; supervision refused |
| DSH / Grok | No verified separate channel; supervision refused |
| Cursor ACP / TRAE ACP | No verified channel or per-session MCP identity contract; supervision refused |
| Cursor SDK | Optional separate runtime under evaluation; no capability is advertised until account and permission contracts pass |
| pi / omp / other runtimes | Only an explicitly advertised, verified runtime channel permits supervision; unknown capability is refused |

Refusals list the compatible launchers in the destination computer's inventory, without
starting a provider or assuming that Claude/Codex are installed. An unreadable/empty inventory
says that none was detected. `assistant_supervision_account` distinguishes an engine whose
system instructions are supported but not enabled for the current account; runtimes must use
that code only after an authoritative account rejection, never infer it from a generic failure.
Its translated explanation asks the owner to enable the account feature with the engine provider.

### Antigravity Desktop (macOS, experimental)

With Antigravity Desktop open and the Persona node running, start the desktop companion from the checkout:

```sh
node integrations/antigravity/desktop.mjs
```

It discovers the local desktop hub through the current user's private descriptor under
`~/.gemini/antigravity/daemon/`, advertises an opt-in `antigravity-desktop` launcher, lists active
conversations, watches exported transcript items, starts new conversations through
`ConversationService/CreateConversation` (with model and effort selection via `launchers.models`
and `session.start`), forwards text replies and settled interrupts, and supports native `rename`,
`archive`, and `unarchive` controls through `ConversationService/UpdateConversation`.
Desktop-UI-created conversations without an initialized configuration still require an initial turn
in the desktop app so replies can reuse their model and tool configuration, and approvals stay in
Antigravity Desktop. The desktop and CLI companions can run together. Override the desktop data
directory with `--desktop-dir`, the Persona runtime socket with `--socket`, or the runtime identity
and default model with `--runtime`, `--launcher`, `--display-name`, and `--model`. The desktop RPC
is private and version dependent.

### Antigravity CLI

Panes (agy in a terminal) are not read by the companion: the node learns a pane's conversation from Persona's own hooks
in agy's global `hooks.json` (group `persona`, `PreInvocation` and `Stop`, #13729) or from herdr's integration, under any
engine, and reads its transcript itself; see `docs/integrations/engines.md`. The ACP companion is for sessions without a
terminal.

The web Agent session overview requests `session.info` while open for a terminal pane or a local Antigravity runtime session.
The node reads the conversation's `.system_generated/swarm/members.json` and adds the optional `swarm_workers`
roster to that response. A running worker in `working` or `mentions` wake mode shows as Working or Waiting,
respectively. Stopped and killed workers show as Stopped. The overview shows the worker count and an expandable
roster. The coordinator and board messages are outside that count.

Assistant context remains an ACP embedded text resource (#12435). Antigravity exports that resource as a
`Content of persona-context:///snapshot.txt:` line and context XML inside `USER_INPUT.content`: before the user's text
for ordinary prompts, after it for a native workflow command. Both the companion and pane history readers remove the
complete leading or trailing Persona resource envelope before projecting user text (#12813). A complete trailing
envelope may contain literal `<persona-context>` tags and attributed recovery / skill-instruction blocks; malformed or
whitespace URIs, bare labels, other URI schemes and ordinary quoted text retain their existing behavior. If ordinary
prose follows a resource, only the envelope is removed and the prose stays. History format
versions invalidate cached items from before this cleanup; native export contents are unchanged, while the ACP block
order differs only for the workflow commands above.

The session's `last_text` preview contains assistant response text after the latest explicit user input. A new user
input clears the previous reply preview, including input with no visible text. Thinking and tool records leave it empty
until the assistant writes response text. This keeps user-authored questions out of awaiting-reply detection while
preserving genuine assistant questions. User messages and tool records remain available in the conversation items.

A row may report a long `title` or `last_text` (a whole opening prompt, a whole reply): the node lists the row with the
title cut to 256 characters and `last_text` to 200, as it cuts its own rows, after reading the question a long reply
ends with into `asks` (#16343). The conversation items keep the whole text.

agy leaves print-mode (`agy -p`) conversations untitled. Before falling back to the folder name, the session title uses
the first non-empty line of the cached metadata `summary.Preview`, with agy's request wrapper and Persona's context
removed, cut to 60 characters (#14629). Several one-shot runs in one folder then show their own prompts. Such a title
carries `title_source: "prompt"`, so the node keeps that provenance for the agy pane and Persona's naming may replace it.
A preview whose first line is Persona's context (`Content of persona-context:…`, `<persona-context …>`) gives no title:
agy keeps only that line, so the block cannot be removed whole.

Antigravity CLI sessions show background work in Agents for both herdr panes and the ACP companion (#13594). Each
running command, scheduled task, workflow or subagent counts once. Scheduled tasks use the Monitor row, including
the cron description when available. Persona's `/loop` control applies to Claude Code's scheduler.

The readers match a task's ID across the native start, finish, cancellation and kill records. A tool result that
changes from running to done or error also ends its task. Up to 20 outcomes from the last day stay in Completed.
Running background tasks keep their loaded ACP worker alive through the idle timeout.
When a pane stops or the ACP session is no longer loaded, its running tasks disappear. Losing the session does
not record an outcome. A joined live pane uses the companion's inventory after the native rewind cut. When ACP
does not have that session loaded, its companion row carries projected running tasks in `agy_pane_background`
for the live pane only. Without a companion row, the pane reads its local export. The node links a task log when
the local export names the corresponding file in that conversation's task directory.

A session is listed `unknown` when the evidence cannot say working, idle or done (`transcriptState` in
`integrations/antigravity/lib/map.js`). It is never guessed as idle: an idle native summary closes a turn only when its
step count covers the last exported step (or when both the export and an idle summary have zero steps). A freshly
`session.start`-ed conversation with no completed turns also stays `idle` if `agy` writes a 0-byte `transcript.jsonl`
before populating a summary row (provided the summary store is readable and shows zero steps and no active/unrecognized
status). Active native
states (`CASCADE_RUN_STATUS_RUNNING`, `CASCADE_RUN_STATUS_CANCELING`, `CASCADE_RUN_STATUS_BUSY`, and `not_fully_idle`)
all report `working`. The companion logs why, as reason counts, each time the tally across listed
sessions changes (`agy unknown status: no_summary 1, summary_behind 2`, then `none` once it clears, #12003):
`read_failed` (the native summary database could not be read), `no_summary`, `summary_not_idle`, `summary_behind` (the
summary's step count does not cover the last exported step), `no_step_index`, and `no_relevant_step` (no user input,
response, tool result or error has been exported). Any non-empty unrecognized summary status value is also logged once
as a sanitized enum token (`agy unrecognized summary status: …`). A failed summary read is logged when it starts or its cause changes,
with SQLite's result-code text only, and once more when reads recover, with the number of failures. The lines carry no
session IDs, message text or paths; under `personad node` they appear in the node log after `agy runtime:`.

A settled Persona-owned ACP reply writes `.persona-turn-completion` beside the conversation's history-epoch file (mode
0600). It stores SHA-256 fingerprints of the branch and actual settled reply index: one for the full reply and one for
its byte length and first/last eight characters so truncated exports can match, never message text (a clean tool-only
turn ending without trailing text writes an `idle:` fingerprint of the completed `GENERIC` step instead). After a restart,
a `summary_behind`, `no_summary` (with a readable summary store), or unspecified `summary_not_idle` (`""` or
`CASCADE_RUN_STATUS_UNSPECIFIED`, with no unexported native steps past the export) row becomes `done` (or `idle` for a
settled tool-only turn) only while the current export matches that settled turn; a missing or mismatched marker, an
unrecognized future summary status, or later native work leaves `unknown`. When `GetCascadeTrajectory` has already
verified an idle native trajectory matching every exported step past a lagging `summary_behind` count, that cached
verification resolves the row until the export or summary changes. Starting another prompt, an autonomous wake, or a
native rewind clears the marker and verification cache, and a known or pending native-cut check suppresses recovery.
Transcript-only status rules and the step-count coverage check above are unchanged.

What subagents and background tasks send back reaches the CLI companion's chat (#14625). agy 1.5.15 writes each
as a `SYSTEM_MESSAGE` step whose body is `[Message] … sender=<id> … content=<text>`; the companion maps it to a
`notice` item with a `task` note (`integrations/antigravity/lib/map.js`): a subagent's report or its "has gone idle"
response is `kind: "agent"` with the report as `detail`, a background task's result (`sender=<conversation>/task-N`)
is `kind: "task"` titled `task-N` with its status and exit code, and agy's own notices (stopped by a server restart)
are `kind: "task"` too. A page cuts a `detail` longer than 24 KiB and sets `full_size`; `session.item` returns it
whole. `SYSTEM_MESSAGE` steps without that header, such as agy's injected "Reminder" prompts, stay hidden. These
notices never carry `wake`: when one wakes the agent after its reply, the generic `wake` notice described below still
opens the new exchange with the resumed step, because a client may already hold the earlier notice. `invoke_subagent`
cards take their description from `toolSummary`, else `Role`, else `TypeName`, and their prompt from `Subagents`,
else `Prompt`. Panes read by the node (`server/internal/node/agytranscript.go`) do not map these yet.

CLI companion sessions and herdr panes show images from exported tool results. Conversation pages carry only image
references; Persona loads the bytes when an image is shown. It accepts PNG, JPEG, GIF and WebP up to 1,152 KiB from
the result's inline data or local file. The Desktop companion still omits these images.

A picture the model service cannot decode never enters a conversation (`integrations/antigravity/lib/media-guard.js`, #11789).
agy's own `read_file` sends a picture's bytes to the model and keeps them in the history, so one truncated JPEG (a chat
attachment served cut short) makes every later request fail with `400 INVALID_ARGUMENT` / `INVALID_MEDIA`, a Continue
included. The CLI companion writes an agy plugin, `<agy config>/../config/plugins/persona-media-guard`, whose
`PreToolUse` hook checks the file `read_file` / `view_file` is about to open. A complete PNG, JPEG, GIF or WebP, and any
file that is no picture, gets no opinion (`ask`: agy's own permissions decide). A cut or damaged one is refused (`deny`,
which wins over another hook's `allow`) with a reason the model passes on, and the file is left as it is. A truncated
JPEG whose image data began also gets a terminated copy under `<agy config>/persona-media-guard/`, named in the reason,
so the model can still look at the part that arrived. The companion atomically copies its `media-guard.mjs` and decision helper into the plugin
directory and the hook executes those installed copies, so later upgrades, another companion's exit or a changed `current`
cannot strand it in a deleted generation. The installed `package.json` gives the copied `.js` decision helper an explicit ES-module scope even under a CommonJS parent. The hook answers `ask` when an installed copy is gone, so a stale plugin never blocks agy; `--no-media-guard` keeps the companion from writing it. agy runs a hook through
`/bin/sh` on macOS and Linux and through `cmd.exe` on Windows, so the Windows hook is a `cmd` command with every path
quoted that checks Node.js and the installed scripts with `if exist` and answers `ask` when any is gone (a cmd that cannot find
the program prints no JSON at all). Pictures attached to a
message from Persona get the same check: a broken one is left out of the prompt with a line saying so.

Native model errors can be omitted from the JSONL export even when ACP reports a completed prompt. When an idle
summary has unexported steps among its final eight, the companion reads them with `GetCascadeTrajectorySteps`, starting
at the first unexported index, and puts every `ERROR_MESSAGE` back at its own index (#14627). A conversation another agy
process created is not in the companion's service, so when the read returns no step for an unexported index the companion
loads the conversation (`session/resume`) and reads once more; a step still absent is not in the native trajectory (a
summary ahead of a rewind) and shows nothing. A step that came back is not read again on the same exported branch;
inserting an error before the export's last step adds `:native-errors:<indices>` to the history token so clients
reload. If the read or the load fails, the turn shows `Persona could not read this turn's result from Antigravity.` as
its error and the history token gains `:native-read-failed`, so a later read that replaces or drops that error
reloads clients and refreshes the row. The read is retried at most twice, 30 seconds apart, while the summary and export stay the same. Discovery opens the
native service through read-only session listing. An omitted error uses the shared assistant-text `API Error:`
representation for the existing error card, and a terminal one populates the session's `turn_error`
fields, including a subscription reset timestamp supplied in native quota metadata. When Antigravity gives only its
generic `Agent execution terminated due to error.`, the text is the native short error with the `ErrorInfo` reason
(`INVALID_ARGUMENT (code 400): … (INVALID_MEDIA)`). A 4xx other than 401, 408 and 429 is a request the service refuses
as it is (a picture in the history it cannot decode, an invalid argument): a Continue sends it again, so it is published
as `turn_error_kind: "user"` (#11742). New input clears the session's `turn_error` while the error stays in the
history; a rewind drops recovered errors past its cut. Rejected ACP prompts also publish their failure through the session's error state.
agy can also stream a failed model request as the turn's only message text, `⚠ FAILED_PRECONDITION (code 400): …`, end
the prompt normally, leave the error out of the export and leave its summary behind (#14628). When a turn with no thinking
or tool activity streams exactly that one-line shape (`streamedErrorInfo` in `integrations/antigravity/lib/errors.js`), it
is the turn's error, classified by its code like a native error, not a reply. It is kept as an `API Error:` notice on the
turn's input, so it stays in history after later steps export, and that input settles the session `idle`.
The history token includes the error format so connected clients refresh items cached before this representation.

**Gemini API key provider** (#13762). agy can run on a Gemini API key (`"modelProvider": "gemini"` in agy's `settings.json`, with
`GEMINI_API_KEY` and, for a self-hosted gateway, `GOOGLE_GEMINI_BASE_URL`) instead of a Google sign-in. The companion is in this mode when
`PERSONA_AGY_MODEL_PROVIDER=gemini` is in its environment, or, when that is unset, when `<agy config>/settings.json` says `modelProvider: gemini`
(`PERSONA_AGY_MODEL_PROVIDER=oauth` forces the sign-in). The key and address come from the companion's environment, which
the node fills from a Persona-managed Antigravity launcher that carries a key (`docs/integrations/launchers.md`; the first such launcher by name
when there are several, because one companion serves every agy session on the computer): the node starts the companion with that launcher's
`GOOGLE_GEMINI_BASE_URL` / `GEMINI_API_KEY` plus `PERSONA_AGY_MODEL_PROVIDER=gemini` and restarts it when the launcher's provider changes.
In this mode:

- Every `agy acp` process is authenticated with the ACP method `gemini-api-key` right after `initialize` (when agy offers it), which reads the key from
  its environment. `--settings` is not needed for `agy acp`; without that `authenticate`, `session/new` answers `Authentication required`.
- The model list is agy's ids that the provider serves. `agy --settings '{"modelProvider":"gemini"}' models` lists agy's whole built-in catalog
  without a sign-in, and `session/set_model` refuses ids that catalog does not have, so the companion keeps an id when `GET {base}/v1beta/models`
  (header `x-goog-api-key`; the Google endpoint when no base is set) has a model of that name or of that name without agy's effort suffix
  (`-low`, `-medium`, `-high`, `-xhigh`, `-max`). The models agy's own sessions report are not published in this mode. A provider that cannot be
  reached or has none of agy's ids is an error in the model list, never the built-in catalog.
- There are no account quota windows: the native quota read is skipped, and one line (`agy usage windows: not available with a Gemini API key provider`)
  is logged.
- The key is passed in the environment of the agy child only; log lines (the companion's own and agy's stderr) have it replaced by `***`, and
  provider errors carry neither the key nor the address. It is in no item, in no `runtime.hello` and in no file the companion writes.

The CLI companion runs the **Antigravity CLI (`agy`)**. When `personad node`
runs (`--attach` or `--stdio`), `agy` is installed, Node.js 22.5+ (`node:sqlite`) is available, and no external `agy`
runtime is connected on `runtime.sock`, the node automatically writes out the embedded `persona-antigravity-runtime`
package and supervises `antigravity/companion.mjs` (passing `--no-update --parent-stdin`, and its own program as
`--personad-bin`), yielding whenever an external
`agy` runtime connects (`server/internal/node/agybridge.go`). The built-in package is unpacked under the separate
`runtime-packages/persona-antigravity-runtime-builtin/<hash>/` store, so its cleanup never scans external companions'
generations or changes their `current`. The sibling layout also keeps built-in files out of legacy external-store pruning. Go writes and pruning use the same `.install.lock` and process-start identity format as JavaScript registration, so a reused PID cannot strand a lock or inherit a lease. The node reserves the generation with its own process lease before launching the companion; a cached launch refreshes that reservation and retries cleanup. Once the companion registers its own lease, it releases the node's reservation for that generation. Reserving a newly launched generation also removes this node's reservations from other generations, which is necessary after a Unix `exec` upgrade retains both PID and start identity. This closes the window before the companion can register its own lease, while idle generations and dead-process leases remain reclaimable. The node resolves `agy` when it starts the companion and
looks again every 30 seconds while it runs (#14533): when the path or the file there changed (replaced by an update,
installed elsewhere, removed) and two looks in a row agree, it restarts the companion on the `agy` found now, waiting
while one of its sessions is working or a call to it is pending (the old file keeps running until then). A session whose
turn ended still counts as working while finite background work it started runs in the companion's `agy acp` (a
command, workflow or subagent the companion matched to its call, #17200); an agy `schedule` (kind `monitor`) does not,
as a cron job never ends, and neither does a task the companion could not match to its call (kind `shell` with no
command line), which the row cannot tell from a schedule, so a one-shot timer or an unmatched task is still stopped by
the restart, as before. That wait has
no deadline, so a background command that never exits holds the restart until it is stopped; the handoff to an external
`agy` runtime waits for the same work at most 5 seconds, as for a working turn, then stops the companion. The node sees
only the sessions the companion lists (its recent ones) and at most 50 running tasks each, so work outside those limits
does not hold the restart yet; a drain signal from the companion is the follow-up. You can also start
`node integrations/antigravity/companion.mjs` manually from the repository root, or install into the runtime package store
with `node integrations/antigravity/install.mjs` and supervise
`node ~/.persona/runtime-packages/persona-antigravity-runtime/current/antigravity/companion.mjs` under launchd / systemd.
When run from that store without `--no-update`, the companion reports `persona-antigravity-runtime` in `runtime.hello`,
installs the version `personad` carries as the new `current` ("Auto-update (optional)" above), waits for active ACP turns,
availability probes, rewinds and phone permission cards to finish, then stops `agy acp` and exits so its supervisor restarts
it on the new code; direct launches outside the store leave self-update off. The companion connects to the node's runtime
socket, lists CLI transcripts, and uses `agy acp` for terminal-less session creation, sending and interruption. It can also
serve as an assistant backend through the optional assistant contract above. It requires an installed, authenticated `agy`
with ACP support; `--agy-bin`, `--agy-dir`, `--personad-bin`, `--socket`, `--no-update` and `--parent-stdin` override the
executable, CLI data directory, node program, node socket, automatic updates and parent-pipe shutdown. When `--agy-dir` is passed explicitly, the companion also sets `ANTIGRAVITY_CLI_CONFIG_DIR` for its spawned `agy` child processes (`acp` and `models`) so discovery, ACP sessions, and transcript reads share the same directory.
To run an additional companion instance alongside the default `agy` companion (for example, a wrapped `agy` executable or separate configuration directory), pass a distinct `--runtime <id>` (default `agy`), and optionally `--launcher <name>` (default `<runtime>`), `--display-name <label>` (default `Antigravity`), `--prefix <prefix>` (default `<runtime>-`), and `--assistant-for <launcher>` to point a non-assistant Agent launcher at this companion in the main-chat picker. Custom runtime IDs keep `agent: "agy"` and do not cause the node's built-in `agy` companion to yield.
Models come from authenticated `agy models` discovery and ACP session metadata. The companion discovers the catalog at
startup and again every five minutes in the background, with backoff on sign-in timeouts (#15481). It reuses a fresh
catalog on `models({launcher})` and refreshes on demand once the five-minute TTL has elapsed or the picker requests
`models({launcher, refresh: true})` (`caps.models`, `launchers.models`; #15253).
Discovery never sends a prompt. The native settings file determines the advertised
default. New sessions and availability checks explicitly apply that model through ACP, since ACP may ignore the
settings file when choosing its initial model. An idle conversation resumed on a fresh ACP connection also applies
the configured model before its first prompt. An explicit caller model takes precedence, and an acknowledged model
choice stays in effect on that connection. Reading history does not change the model or resume the session. An
unavailable catalog returns the native error instead of advertising a built-in model list.
A CLI whose stored sign-in expired long ago does not fail `agy models`: it hangs. The companion kills it after 25 seconds
and reports that the sign-in may have expired and that running `agy` once on the computer signs in again; each
discovery in a row that times out doubles the backoff for unforced lookups (from five minutes up to an hour), while an
explicit `refresh: true` retries immediately and a successful discovery clears the backoff (#11106, #15253). Whether a
call that sends no prompt can refresh such a sign-in is not established: a `agy models` run refreshed a token close to
its expiry, but one expired for months stayed hung until a prompt in print mode signed in silently.
One `agy acp` process can keep the model list it had when the account's plan lapsed or the CLI signed out (#10368), so
a session's model list replaces the catalog only when its ACP process started after the latest successful discovery
began. When ACP rejects a model as unknown and a fresh discovery lists it, the companion replaces the ACP process and
runs the start, model change or availability check once more. Every runtime call, turn and native read that uses ACP
holds a lease for its whole run, and the process is replaced only when no lease other than the caller's, ACP request
or permission card is open. That last check and the replacement run without an await between them, and calls that
arrive during a replacement wait for the old process to exit and start on the new one. A replacement that rejects the
same model is not replaced again while the catalog stays the same. A successful discovery after one that found the CLI
signed out replaces the ACP process the same way: at once when nothing uses it, otherwise before the next start, send,
model change or check that finds it idle.

A sign-in with another account while the CLI stays signed in triggers neither of those, and an `agy acp` process keeps
the account it read at startup and writes it back at every hourly token refresh (#17860). Each native OAuth ACP process
(shared, per-channel or assistant) therefore remembers the stamp of the CLI's sign-in it started under
(`lib/sign-in.js`): the stat of `antigravity-oauth-token`, plus, on macOS, the dates of the login-keychain item
`security find-generic-password -s gemini -a antigravity` prints without `-g` or `-w`. Nothing reads a token. The
companion compares the stamp every 15 seconds while a process runs and before each call; a change makes the processes
that started under the old stamp stale. A turn that fails with a quota or sign-in error (`RESOURCE_EXHAUSTED`,
`usage_limit`, "please sign in") makes every running process stale too, at most once every five minutes, so a fresh
process on an account that really is out of quota is not replaced again and again. A stale process is replaced by the
reaper as soon as it is idle (no lease, request, permission card, running turn or background task), or before the
next call that finds it idle; it is never replaced under a running turn. Interactive `agy` sessions in a terminal that
are still on the old account keep writing it back; that is the CLI's behavior, and Persona follows whatever the CLI is
signed in with at the time.

Idle sessions can switch model, thinking level and permission mode through ACP. Native configuration updates determine
the displayed model and effort. Each accepted field is published even if another field fails, and failures reach the caller.
`auto` keeps native safe-operation approval when agy accepts it. If agy applies `default` instead, the companion changes
to `bypassPermissions` through agy's `accept-edits`, answers later permission requests automatically, publishes that final
mode and adds one reloadable notice through the existing localized runtime-notice path. A session's explicit mode remains
authoritative over later settings-file changes. A mode taken from agy's settings is read again on each session projection
and applied again before the next prompt, so changing `agentMode` needs no companion restart. External session models stay
unspecified until native metadata is available. Session discovery does not resume them.
An Antigravity CLI from before `session/set_mode` answers it with -32601 (#14533): a request for `default` is then a no-op
(the session is already in the CLI's own default), and any other mode fails with "This Antigravity CLI (agy) is too old to
switch to the … mode … Please update agy.", which launcher health classes as `cli_outdated`.

Availability checks use optional `check({launcher, cwd, prompt})` (`caps.check`) through the companion's authenticated
ACP client. They switch the probe session to `default` only when `session/new` lists that mode (or lists none), and a CLI
without `session/set_mode` skips the switch. They run the existing one-word probe, cancel tool permission requests, and close their temporary session
on success, failure or timeout. Completed probe workspaces stay out of the user inventory after companion restarts.
The runtime bounds native calls to a 75-second budget, followed by bounded session cleanup. Older runtimes keep the
node's shell check. Shell and managed launchers keep their own execution context.

A chat gets Persona's MCP server, named `persona`, through ACP `mcpServers`. Regular Agent chats can also get
`persona_a2a` with a session-scoped ticket. Both run the node's own program as `<program> mcp`: the node names it with
`--personad-bin`, and a companion started some other way takes `personad` from a state directory's `bin`, or
`persona-node` beside it, the name a Persona Link install has (#14582). With neither, its chats get no Persona tools,
which the companion logs once as it starts (`no Persona node program`). `agy` defers the tools over its
declaration budget, largest first, and has no `call_mcp_tool` for servers passed through ACP, so `persona` runs behind
`integrations/antigravity/mcp-proxy.mjs`, which adds a small `persona_call` tool that calls any Persona tool by name
(#7333). The proxy declares only the tools larger than `persona_call`, so it is always the smallest declared and never
the one deferred, however many small tools connectors add (sizes are what agy declares of a tool: its name, description
and input schema). The others stay callable through it: `persona_call` with no name lists every tool, and those left out
with their description and arguments (#10756). A companion loaded from `<store>/<hash>/` resolves the proxy afresh for
each new or resumed session, preferring `<store>/current/antigravity/mcp-proxy.mjs` whenever it exists. Consecutive
upgrades therefore do not leave later sessions pointing into a deleted generation; source checkouts without `current`
keep using their own `mcp-proxy.mjs`. The proxy entry is self-contained (its schema projection is inlined), so after
Node loads it, pruning its generation cannot break a later relative import. For assistant sessions, when the upstream catalog marks core tools with `_meta["anthropic/alwaysLoad"] === true`,
the proxy declares only those always-load tools (larger than `persona_call`) and keeps deferred tools reachable through
`persona_call`; when all tools carry `alwaysLoad` (e.g. `assistant.load_all_tools = true`) or none do (older servers),
it falls back to declaring every tool larger than `persona_call`. For non-assistant Agent sessions (`--agent`),
the proxy prioritizes core task, leader and supervision tools and caps secondary declarations within the eager budget so
`agy` does not evict leader or supervision tools while keeping secondary tools reachable through `persona_call` (#15822);
`persona_a2a` stays on its separate scoped server.
A role-bearing task start targeting the Companion (including read-only and explicit `agy` host starts) uses `defer_mcp`
as described under "What to implement": the real ACP session id exists and the task's role link is committed before the
first MCP attach and prompt, and deferred activation preserves separate `task_instructions` and `task_attachments`, so
that first catalog already has the correct leader or worker scope (#15327, #15822).

Implementation: `integrations/antigravity/lib/runtime.js`, `integrations/antigravity/lib/acp.js` and `integrations/antigravity/lib/policy.js`.

For assistant supervision (`caps.supervision_policy`, #11173), the companion stores each supervised session's policy in an isolated directory `<agyDir>/persona-supervision/<rawId>` outside `~/.persona/assistant` and attaches it to that session through ACP `additionalDirectories` (verified against `initialize`'s `agentCapabilities.sessionCapabilities.additionalDirectories`). `agy` loads both `AGENTS.md` and `GEMINI.md` from `additionalDirectories` directly into `<user_rules>` on every turn (24,000-byte per-file cap), so policies above 24,000 bytes split cleanly across `AGENTS.md` and `GEMINI.md` up to 48,000 bytes, while shared `~/.persona/assistant/AGENTS.md` and concurrent ordinary chats stay untouched. A directory once attached stays visible to the later sessions of the same `agy` process too (agy 1.5.14, #12883). So the isolation rests on each Assistant conversation running in its own ACP process (`acpFor`) while ordinary Agents share another, and a policy directory is never attached on the shared process. Policy updates and demotions (`""`) synchronize atomically and reload the session via `session/resume`. `agy` reads the rules again on every turn: a rewritten file applies from the next turn, and a directory removed and left out of the resume no longer applies (agy 1.5.14, #12883); busy role changes are refused with `busy`; missing ACP `additionalDirectories` support, unwritable directories or oversized policies fail closed with `assistant_supervision_runtime` before any prompt runs; and `session.close` removes the session's policy directory.

Live text belongs to the ACP prompt currently executing. Queued prompts acquire the stream when they start, with an
empty boundary. A tool call ends the current text message, so the next message replaces its words in the live slot.
Each prompt clears its live text on completion, including when another prompt is queued.

An unsolicited ACP update after a delivered reply opens its own exchange before streaming text. In an exported
Antigravity transcript, a `SYSTEM_MESSAGE` after a completed answer marks the same boundary once the model
resumes before the next owner input. Multiple notifications for that resumption share one generic `wake` notice.
The notice shares the first resumed activity's sequence, ahead of its content. Its ID derives from the preceding
delivered native answer. Private scheduling text stays in the export. If ACP observed the wake and its matching
reply lands without a scheduling record, the companion records the two native step fingerprints in the conversation's
`.persona-observed-wakes` file. The companion and terminal pane can then reconstruct the same exchange on
reconnect or restart. A read-only conversation keeps this evidence for the life of the companion. An older export
with neither a scheduling record nor an observed boundary retains its original grouping.

While a conversation is watched, the companion polls its native export every 400 ms. Persona-owned ACP turns reuse
their existing poll. Unwatch, disconnection and shutdown release the watched poll. Native records that update an
existing step carry a stable content revision in the history token, so a response completed at the same sequence
replaces retained content. Ordinary appends preserve that revision and continue through incremental item batches.

Until the native export catches up, a Persona-owned prompt's steps come from ACP: the sent message, thinking, tool
calls and results, and the settled reply; a background wake's steps the same way. While they only grow they keep the
conversation's history token, so a send and every live update arrive as appends and clients keep their page (#12014).
Their item IDs are not the export's, so the token changes once when the export replaces them, or when an interrupt
drops some (a send that takes over a running wake included). The token that follows is written to the conversation's
`.persona-history-epoch` file before the first such step shows, so a restarted companion never reports the token
clients hold those steps under, and an idle conversation keeps its token across restarts. Where that file cannot be
written, the token changes as soon as such steps appear.

Session inventory, watch cursors, item pages and live item pushes read `transcript_full.jsonl` when available.
Antigravity Agent and Assistant sessions publish waiting messages in `Session.queued`, using an empty string for a
photo-only message. Send Now cancels only the active prompt and starts the next queued message when it settles.
Interrupt cancels the active prompt and clears the queue.

Antigravity declares `caps.rewind` for idle Agent conversations. The selected message, duplicate occurrence from the
end, and following user-input count are checked against `GetCascadeTrajectory`. The owned ACP process's private log
identifies its loopback native service. `RevertToCascadeStep` receives `conversationOnly: true` and the preceding step
index, including `-1` for an empty conversation. Native refusal propagates to the caller. The bridge uses the current
native summary and replacement step indices to discard the abandoned export tail, then publishes a changed history
token. This survives companion restarts without modifying native transcript files. When the export instead numbers
later steps after the abandoned tail, they show behind the kept steps once `GetCascadeTrajectory` confirms them, and a
later rewind cuts at the matching visible step (#14297).
A reused lower index is not proof of a rewind: when the export goes past the replaced steps before the replacement run
reaches their last index, another process wrote the same conversation. The bridge then keeps both runs, ordered by
step index (old steps first), and changes the content revision (#14626). If the replacement run later exports an index
inside the restored steps (a rewind whose parallel results arrived out of order, #11086), it did re-cover them: the bridge
drops them again for good. A second writer that goes on into those indices is treated the same way.

Antigravity declares `caps.fork` for Agent and Assistant chats. `ForkConversation` keeps the existing workspace and
copies through an inclusive native step index. The bridge resolves a reply from the exported transcript and stops
before the next user turn so tool calls keep their results. A whole-chat fork sends `-1`; omitting the native index
would select step zero. A fork of the main Assistant chat opens as a separate chat.

The compact `transcript.jsonl` export can contain native `<truncated N bytes>` placeholders and can be ahead of
the complete export. Use one source for sequences and content so a watch cannot advance past unread complete items.
Older histories fall back to the compact export when the complete file is unavailable. Persona applies the SDK's
normal page limits to complete native records and serves larger items through the full-item endpoint. The history
scope includes `:full` to invalidate previously cached compact pages. Parsed files are cached by file identity, size
and nanosecond modification/change times, bounded to 16 files and 64 MiB of source data. Files modified within the
last second bypass the cache because rapid same-size writes can share a filesystem timestamp tick.

### Runtime token usage

For an agent without local token records, call `rt.usage({session_id, ts, model, request_id?, input, output, cache_read, cache_write, reasoning?})` once per completed model request. `ts` is an RFC3339 timestamp; counts are nonnegative safe integers. Input excludes cache reads and writes; output already includes reasoning. Reasoning is an optional part of output, never added again. All-zero reports are ignored.

The node advertises `usage` in hello features. The SDK returns false while disconnected, without a hub or with an older node; it does not queue or replay reports. A true result means the event was written, not acknowledged or durably saved. Reports can be lost during a reconnect, node restart or storage failure; there is no backfill. Upgrade the Hub storage support before using the new node feature.

Only currently listed, owned sessions count. The row's agent must match the runtime's registered default agent or an advertised launcher agent. The node supplies the recorded launcher name. Claude Code, Codex, TRAE CLI, pi, omp and opencode remain metered from their local records, including when hosted in Paseo. For other agents, a nonempty request ID deduplicates within one runtime connection and session for 30 minutes. Without an ID each report counts. The cache retains at most 65,536 request IDs and evicts its oldest entry when full. Reports outside the last 26 hours of minute buckets, more than a minute in the future, or with any token count over 1,000,000,000 are rejected. The file meter's 10-second tick sends the buffered per-minute totals. The manager restores persisted runtime minute buckets at startup.

Daemon links allow the same event and prefix its session ID as usual. Runtime usage goes to the same token totals and agent buckets as file usage. It is added, rather than replacing a minute's total, and full file scans preserve these unreconstructable buckets.

### Runtime subscription usage windows

A runtime with read-only access to a subscription's rate-limit windows calls `rt.usageWindows({provider, name, plan?, sampled_at, windows})`. Each window is `{label, window_minutes, used_percent, resets_at?, model?}`; timestamps are RFC3339, lengths are positive safe-integer minutes, percentages are finite numbers from 0 through 100, and one report has at most 32 windows. This is the same account shape that `docs/server/usage-windows.md` defines; it is separate from the per-request token counts above.

The node advertises `usage_windows` only to runtimes on the local socket, not to daemons (their accounts must not be attributed to the node's machine). The SDK returns false while disconnected from the node or with an older node and does not queue reports. The runtime host forwards reports to the node serving the hub connection, where they are stored in memory. Reports replace the source named by the runtime id, so a companion restart does not duplicate its account. An older sample cannot replace a newer one; a valid empty `windows` array explicitly clears the source. The machine's existing `usage_windows` off switch rejects these reports too.

Antigravity samples quota through `RetrieveUserQuotaSummary` with `forceRefresh: false` when an ACP session opens or a turn completes. On hub connect it republishes a sample less than five minutes old or checks a live worker, then refreshes the session list. Session listing can launch a shared worker if it sees a running native conversation or one updated in the past 20 minutes and needs a fresh sample (#16118). Concurrent reads share one attempt, and failures wait five minutes before retrying. A quota read leaves the idle timer alone, and a worker launched just to read quota retires after the normal five minutes of idle time. Idle computers with no recent conversation activity keep the default of no background quota worker (#15253).

### Shared runtime behavior

Antigravity item identities include native creation time and the replacement branch's generation (#9410). Native
step indices remain pagination sequences, not globally reusable item or tool-call IDs: after rewind a repeated input
must not inherit the abandoned branch's pending-send binding or reply cache. Retained prefix records and same-step
completion updates keep their identities. The history scope includes `:step-ids-v2` to discard older numeric-only
item caches; full-item lookup resolves only the current branch's exact ID. Identity metadata is computed with parsed
records, kept outside native JSON and reused by list, push, page and full-item reads.

When two runtimes list the same session (pi's companion lists it from disk while pi-web or a pi terminal is running it), node makes the one running it responsible (ones listing it as `offline` go after),
and on a tie the newer connection wins. Listing a session as `offline` tells node "I can open it, but it isn't running here".

Paging uses the SDK's `page(items, params, {history, toSeq})`: the runtime just needs its items sorted by seq.

A runtime that depends on a new node capability uses `createRuntime(…, {requires: ['prefer_running']})`: with an old node that doesn't support it, it doesn't connect (logs one line),
and retries when the socket file is replaced (the resident node restarted, e.g. after an upgrade) or after 10 minutes; `rt.nodeHas(feature)` checks the current node. Sessions started via node that run elsewhere (pi's companion creating one in pi-web) are reported as `offline`,
and `session.start` returns the row of the runtime actually running it.

**An Agent in a host paired with a runtime plugin** (pi and its family, omp, opencode): panes, state and terminal belong to the host (herdr), chat and control to the runtime. When the runtime's session row has the same `session_id` as the session id the host reports,
node merges the two rows into one (`mergePiRow`, `mergeOpencodeRow`: title, last line, model, context usage and revision come from the runtime, state from the host, and the runtime saying `blocked` counts as "Needs you").
Without the runtime, the row is terminal-only: the chat is a single note and sending is refused (`terminalOnly`, `server/internal/node/opencode.go`). Agents herdr reports that Persona has no way to read are listed as terminal-only rows too.
Don't put host pane information in the runtime id: panes are matched on node's side, and `pane_id` in a runtime row is only an optional hint.

Platforms built on DSH that don't run `dsh web` (multi-tenant, running DSH in containers) implement the same protocol on the platform side and connect as a daemon (`docs/integrations/daemons.md`),
reusing the SDK and `integrations/dsh/lib/map.js` directly, with no code added on the Persona side.

### Assistant admission evidence (#11936)

`TestCodexAndPiAssistantConversationRoles` exercises manager admission and actual
fake-node delivery for `codex` and `pi`, each with `main`,
`background_supervisor`, `direction_supervisor`, and `scheduled_task` subtests.
Supervisor inputs carry the exact manager policy; scheduled runs resume the same
conversation and retain their execution binding. The managed Codex launcher and
the policy-capable pi-web companion use the contracts merged in #12569
(`7085c82a1f`). Pi's runtime hook contract is also covered by
`assistant policy/context and tool authority are isolated to the actual pi session`
in `integrations/pi/test/assistant.test.js`. Tests use fake runtimes/models.

Unproven supervision channels (plugin paths, DSH, Grok, Cursor ACP and TRAE)
remain refused as required by the adopted isolation review. Their refusal does
not imply that Codex or a verified pi companion is ineligible.
