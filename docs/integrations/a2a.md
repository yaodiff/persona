# Agent-to-Agent communication

Persona can send work from one session to an existing session or launch a new
Agent on another enrolled computer. A session keeps its own harness and local
execution environment. The manager owns the task and records both participants
and the messages they exchange. Use this in the Agents workspace or from an
integration that can act on task updates.

## Default scope

A2A is available by default on the confirmed personal computers of a manager.
Upgrade the hub, manager, and participating nodes together. To narrow the scope,
set `agents.a2a.machines` in the owning manager's `manager.json` and restart
the manager. Use the exact IDs from the Computers page. An empty list disables
A2A for that manager.

```json
{
  "agents": {
    "a2a": { "machines": ["mac", "box"] }
  }
}
```

Sessions in scope can address other eligible sessions on those computers.
Guest, work-trust, pending, hidden, archived, and health sessions are excluded.
Keep enrollment to computers whose sessions may exchange tasks. A container
running a node enrolls by its machine ID. A runtime daemon enrolls through its
node. To let it call A2A tools, set `"a2a": true` on that daemon's entry
in `daemons.json`. Its original session ID is used when it calls the runtime
SDK, and Persona addresses it by the namespaced ID listed in discovery.
An enrolled daemon can discover and send tasks to eligible sessions across
the Manager's enrolled computers. Enable this for runtimes you trust with that access.

## Send and receive

Use the tools through Persona MCP, a runtime's existing `callTool` API, or
`personad tool` from a shell associated with a supported session. Because
`personad` is often not on a terminal pane's `PATH` (or may be installed under
another name), when the manager renders an A2A message for a session that calls
Persona's tools from a node-verifiable terminal (a pane or a Codex thread)
without Persona MCP tools—or for the first prompt of a session launched by A2A
or `dispatch_start` unless its launcher runs Claude Code or a runtime—it uses
the absolute executable the recipient node reported when it connected, falling
back to a bounded `node.info` read when no absolute path is cached. It includes
the path as a JSON string to decode and quote for the shell, matching task
handoffs. Sessions with Persona MCP tools, runtimes that wire A2A tools in
themselves, and nodes with no verified absolute executable keep the plain
`personad tool` wording unchanged.

| Tool | Purpose |
| --- | --- |
| `a2a_sessions` | Discover eligible session addresses, harnesses and states; with `presets`, also the computers a subagent can start on and their Agent presets |
| `a2a_read` | Read a session's recent conversation: the user's messages, its replies and one line per tool call |
| `a2a_send` | Send a request to a session, start an Agent, or answer its request for input |
| `a2a_get` | Get a task and its delivery receipts, or list the caller's incoming and outgoing tasks |
| `a2a_report` | Report progress, request input, complete the task, or report a failure |
| `a2a_cancel` | Cancel a request that has not entered a turn, or one waiting for input |

For an existing session, send its exact machine and session IDs.
An address names the session's conversation. When its computer lists that
conversation under another ID by the time an Agent uses the address, a tool
that takes the address acts on the row listed now, which names the IDs the
conversation went by in its `aliases` (`docs/protocol/protocol.md`). The
common case is an Antigravity CLI conversation: it is listed under its terminal
pane's row while a pane shows it, and under its own `agy-<conversation id>`
otherwise (`docs/integrations/engines.md`). An ID that no listed row names, or
that two rows name, is not resolved. A task keeps the target it was created with.

Each receiving session runs one active A2A task at a time; additional sends to a
busy session are queued and delivered in FIFO order when the active task ends.
An active request waiting for delivery is retried when the recipient becomes idle.
Idle session lists and turn-end events make that request due immediately, rather
than leaving it behind the delivery worker's previous polling lease.
If it still cannot be delivered after ten minutes of active waiting, it fails with
an explanation and the next queued task is released. Time spent behind another
task in the FIFO does not consume this delivery budget. The deadline survives a
manager restart and is not reset by a node refusing a busy session.
The recipient can use `a2a_report` with `failed` or `completed` to clear an
undelivered request, including one still in the FIFO. Its input is discarded,
not marked as delivered. Other reports return an error explaining how to clear it.
Task views expose `waiting_since`, `waiting_seconds`, and `waiting_reason`;
JSON-RPC metadata uses `personaWaitingSince`, `personaWaitingSeconds`, and
`personaWaitingReason`. These describe the total wait, including FIFO time.

```sh
personad tool a2a_send '{"machine_id":"box","session_id":"runtime.session-1","message_id":"request-001","text":"Run the checks and return the failing cases."}'
```

`personad tool` waits up to 30 minutes for a call, because `a2a_send` may
wait for the owner's answer on the phone. A read (`a2a_sessions`, `a2a_get`,
`a2a_read`, and `dispatch_read`, which adds its own `wait_seconds`) gets 30
seconds. Without an answer by then it fails with exit code 3 and kind
`unavailable`, and its error names the tool and what did not answer. A read
changes nothing, so it can be tried again; a write that got no answer may still
have run. On macOS, a call started in a folder that does not answer, such as a
protected folder while the system service that checks access to it is stuck,
moves to `/` after 2 seconds instead of waiting for the folder: Go programs
otherwise wait for their folder before they start (`server/internal/startdir`).

To start a new cross-machine or cross-harness subagent, provide the target
`machine_id` (which may be the caller's own computer for cross-harness work such
as Claude launching Codex) and `cwd`, with an optional `launcher` and optional
`permission_mode`. New launches default to `plan`. Requesting any mode above
`plan` (`default`, `acceptEdits`, `auto`, `bypassPermissions`) requires live
source-node permission attestation bound to the calling request (cached session
snapshots are never trusted, and unmapped modes fail closed): `prompting`
attestation allows at most `default`, while `bypass` attestation allows any
supported mode. Launching subagents is enabled by default on the manager's
confirmed personal computers, using the manager's Agent model (or the Agent
preset named below) and existing depth (1) and concurrency
(`agents.dispatch.max_active`, default 3) limits. Set
`agents.a2a.launch` to `false` in `manager.json` to disable launching new
sessions while keeping peer messaging (queued launches that have not yet called
`session.start` also recheck this switch and fail cleanly), or set
`agents.a2a.machines` to `[]` to disable A2A altogether.
Sessions launched by `a2a_send` or `dispatch_start` are durably tagged as
subagents (`is_subagent`, `parent_machine_id`, `parent_session_id`,
`a2a_task_id`, `subagent_origin`) and folded under their parent session across
machines; ordinary A2A messages sent to existing independent sessions never mark
them as subagents. Subagents are excluded from `a2a_sessions` peer discovery,
while remaining allowed to receive messages, call `a2a_report`, and be observed
on their own launch task.
Reuse the same `message_id` when a tool reply is lost. The manager records the
request and a reserved session ID before attempting the launch.
A new Agent starts where `agent_start` would start one, since nobody sits at
its pane either: when its launcher runs the Antigravity CLI (agy) and the
target computer's Antigravity companion runs that launcher, it starts on the
companion, which holds `plan` in agy's own plan mode.
A launch in `plan` whose launcher has no read-only mode on that computer (pi,
the Cursor CLI, or agy where only a terminal runs it) is refused when
`a2a_send` is called, before the task is recorded, with `readonly_unsupported`
naming that computer's Claude Code and Codex launchers. The launch is checked
again when it goes out, since it can wait for its computer. If the companion
went away meanwhile, a writable launch starts in the computer's default host
and a read-only one fails. A launch that did not start fails with the reason,
such as the node's refusal; only a launch whose outcome is unknown fails with
`Persona could not confirm the Agent started.`

```sh
personad tool a2a_send '{"machine_id":"box","cwd":"~/code/project","message_id":"request-002","text":"Inspect the failing integration test and explain the cause."}'
```

Add `tier` to start a subagent on one of the owner's Agent presets: the tiers
of the model profile in use ([model profiles](../assistant/model-profiles.md)
"Agent presets (tiers)"), each with a description saying when to use it. The
tier resolves as it does for the assistant's `agent_start`, before anything is
recorded or started: an unknown tier is refused (`err_tier_not_found`, naming
the tiers there are), and so is one whose launcher the target computer lacks
(`launcher_not_on_machine`, naming the launchers it has). A `launcher` named
with it that is not the tier's own wins, and the tier then gives nothing. The
tier's launcher, model, effort and faster speed go to `session.start`, with the
manager's Agent model filling in what the tier leaves out. Its
`permission_mode` is not used: the launch keeps the mode described above,
`plan` unless a higher one is requested and attested. This is deliberate. An
owner's presets never raise what an Agent-started Agent may do. A retry with
the same `message_id` must name the same tier; another tier is a conflict.

To see the presets first, call `a2a_sessions` with `"presets": true`. It
answers `{"sessions": […], "computers": […]}`: each computer a launch can go to,
the caller's own included, whether it is online, and the presets it can run,
with what each runs (launcher, model, effort, speed) and its description. The
list is filtered as New Agent's is: only presets whose launcher that computer
has, a preset whose launcher failed its latest check marked `unavailable`, and
`presets_unread` on an online computer whose launcher list could not be read.

When Agent dispatch is enabled, `dispatch_start` uses A2A for new tasks and
keeps the existing launcher, worktree and supervision options. It returns the
new session and task IDs. Supply a stable `message_id` when retrying a launch.
It takes `tier` too, resolved, refused and matched on retries the same way; the
started Agent keeps the permission rule dispatch gives it.
It chooses the companion for agy the same way and keeps its own permission
mode. When Model configuration starts it in `plan`, a launcher that cannot
start read-only there is refused before the task is recorded.
`dispatch_read` without arguments lists each computer a task can go to with
the presets it can run. It accepts either ID for new dispatches and can still
read the dispatch history created earlier.

When the receiving Agent needs something from the sender, it reports an
`input_required` event with the question. The sender responds with
`a2a_send` using the same `task_id` and a new `message_id`. Persona also
supports this exchange through chat. The recipient can start its final reply
with `A2A <task ID> input_required` or `A2A <task ID> failed` and put the
question or reason on the following line. The sender can reply to a question
with `A2A <task ID> answer` on the first line and its answer below. Persona
accepts these replies from the turn that received the corresponding message.
An ordinary final reply to a task request becomes its result.
Native approval prompts stay with the user. The task records a waiting state
while the receiving session is blocked.

Questions, approval waits, resumed work, and final results go back to the
sending session once it is live and idle.
Ordinary progress is available in the task history. Set `wake: false` to
read updates with `a2a_get` instead. Messages and receipts survive a manager
restart. If a device is offline or the user stopped the session, pending
messages wait for a live session. Returning results can also resume a parent
whose node recorded an idle stop or restart. Runtimes such as Paseo can also
wake a parent they released while idle. If the runtime may have taken a message
and its reply was lost, the manager checks for delivery evidence. An unresolved
delivery is shown as `uncertain` for up to ten minutes. It then records
`unconfirmed` and releases the task's hold on the recipient. A closed receiving
session ends the task and sends the parent a status update.

## Read a session

`a2a_read` takes a session's exact `machine_id` and `session_id` and an
optional `turns` (2 by default, at most 10). It returns the session's recent
conversation as text: a line with its title, harness, computer, state and
folder, then the user's messages, its replies and one line per tool call, cut
to the last 40,000 bytes. It reads the sessions `a2a_send` can address in the
same scope, and also read-only sessions in that scope, which take no messages.
It never reads guest, work-trust, pending, hidden, archived or health sessions,
or the assistant's own conversations. Reading changes no task and does not
notify the session.

```sh
personad tool a2a_read '{"machine_id":"box","session_id":"runtime.session-1","turns":4}'
```

## Sessions quoted with @

In a chat on the web, iOS or Android, picking another session in the @ menu
puts a quote of it in front of the message: a `<persona-context kind="context">`
block whose `url` is the session's address,
`persona://session/<machine>/<session>`, with its title, folder and last words.
Before an Agent gets the message, its node ends that quote's body with a note in
the owner's language: the session's `machine_id` and `session_id`, `a2a_read`
to read more of it, and `a2a_send` to message it when the user's request calls
for that. For a harness without the Persona MCP tools, the note names the
node's executable to run with `tool a2a_read` or `tool a2a_send` and one JSON
object. The chip shown for the quote is unchanged; the note shows when the
quote is opened. A quote that already carries its note, such as an edited
message sent again, is left alone. The assistant's conversations and guests
get the quote as written: the assistant reads Agents with `agent_read`.
A new Agent whose first prompt goes with its start, rather than as its first
message, gets the quote without the note.

## Client and harness coverage

Web, Electron desktop, iOS, iPadOS, macOS, visionOS, Android and HarmonyOS
can display A2A messages through Persona's session APIs. Task discovery,
delivery receipts and task-ID actions are available through Agent tools and
the JSON-RPC interface. Apple Watch and CarPlay use their existing session
notification and interaction APIs.

| Integration | A2A participation |
| --- | --- |
| Claude Code with Persona MCP | Every Claude Code Agent Persona starts carries `--mcp-config` with the node's `mcp.json`, which lists the A2A tools, `a2a_read` included, while the session is in scope. Calls work when the node can bind the MCP process to the session |
| Codex with a Node-verifiable thread or terminal session | Has no Persona MCP tools: discovers, reads, sends and reports by running the node's executable with `tool <name>` (`personad tool`). `personad` may not be on its `PATH`; the node's notes name the executable |
| Antigravity CLI in a terminal engine | Persona attaches no MCP server to it: as Codex, it runs the node's executable with `tool <name>` from its shell, and the node binds the call to the pane's session |
| DSH and Grok runtime tool bridges | Discover the generic tool catalog and forward calls with a runtime-owned session ID |
| Pi and Oh My Pi with the Persona extension | Register A2A tools in the harness using its current session ID |
| OpenCode v2 with the Persona plugin | Registers A2A tools through the native tool editor and uses the executing OpenCode session ID |
| OpenCode ACP companion | Uses its existing per-session MCP route to discover and call A2A tools |
| Antigravity CLI through Persona ACP | Attaches the A2A tool catalog to Persona Agent chats through a session-scoped MCP ticket |
| Paseo | Attaches a session-scoped A2A MCP server when Persona starts an Agent on a provider that supports external MCP servers. Other writable sessions can participate through chat |
| Cindy host, Orca structured Claude chats, Antigravity Desktop, Claude Desktop and Codex App | Writable sessions can receive tasks and participate through chat where the bridge exposes the matching turn. Agent-initiated structured calls require a tool bridge or a verified CLI caller |
| Runtime SDK or daemon with A2A tools wired into its harness | Can call A2A through the session-scoped runtime tool API. Remote daemons require the local daemon setting |
| Cindy database fallback, other read-only integrations and historical sessions | Can be viewed through the ordinary session client |

Terminal engines such as herdr, Herdr App, Orca and Tern use the A2A support
of the Agent running in their terminal. When a terminal Agent (such as Codex or
Antigravity CLI in a node-verifiable pane, or a Codex thread) has no Persona MCP
tools, A2A messages name the recipient node's reported absolute executable as a
JSON string so the Agent can run `tool` commands even when `personad` is not on
`PATH`. The supported launch, send and resume operations still depend on the
engine and runtime.

The server integration tests exercise two Node connections, a Manager,
Postgres and the Hub. They check an input-required round trip and read the
parent's question through the app transcript endpoint. Runtime tests exercise
tool calls and session binding with simulated providers.

## Use a standard A2A client

Set `agents.a2a.public_url` to the hub's HTTPS base URL, including any proxy
prefix. The Agent Card for a session is at
`/api/v1/a2a/{machine}/{session}/.well-known/agent-card.json`. Use the
interface URL in that card and send `A2A-Version: 1.0` with JSON-RPC calls.

From the hub's master credential, issue a scoped A2A credential and verify
the response grants exactly the intended scope before giving it to a client.

```json
{"name":"A2A client","kind":"other","scopes":["agents.a2a"]}
```

Use the issued token in the Authorization header. A client can only read or
continue the A2A tasks created with its credential. The recipient can read
and report on its own tasks through the node.
This credential can send tasks to any eligible session in the Manager's enrollment.

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "SendMessage",
  "params": {
    "configuration": { "returnImmediately": true },
    "message": {
      "messageId": "request-003",
      "role": "ROLE_USER",
      "parts": [{ "text": "Summarize the test results." }]
    }
  }
}
```

The interface supports `SendMessage`, `SendStreamingMessage`, `GetTask`,
`ListTasks`, `CancelTask` and `SubscribeToTask`. Input and result artifacts
are text. Streaming starts with a task snapshot, followed by status, progress
and artifact events while the connection lasts. After a disconnect, use the
latest snapshot and task history to catch up. The Agent Card advertises the
JSON-RPC binding and streaming capability. Webhook push, external-agent
discovery and outbound calls to third-party A2A servers are outside this
interface. Session-to-session discovery and delivery stay within the enrolled
machines of one manager.

[A2A specification](https://a2a-protocol.org/latest/specification/)
