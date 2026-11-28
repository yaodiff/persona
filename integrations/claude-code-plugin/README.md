# Persona plugin for Claude Code

In Claude Desktop, background sessions and terminals outside Persona's engines, this plugin delivers the owner's
messages as typed input. The peer inbox identifies them as coming from another session. See
`docs/integrations/claude-integration.md`, "Owner input through the Persona plugin".

The plugin polls the local node for messages, Stop, session model and effort choices, and authorized permission,
plan and question cards. It reports the main loop's effort for Desktop sessions. It reads only its manifest and
stays quiet when the node is unavailable.

## Packaging

- Plugin name: `persona`. Version: `version` in `.claude-plugin/plugin.json` (semver). The installer compares it; the
  plugin reports it to the node at run time, read from that same file.
- The folder is its own marketplace: `.claude-plugin/marketplace.json` names the marketplace `persona` and lists the
  plugin with `"source": "./"`. Installing it with Claude Code's own commands:

  ```sh
  claude plugin marketplace add <folder>
  claude plugin install persona@persona --scope user
  ```

  A marketplace that is a folder is read from that folder itself, so the installer keeps the folder at a stable path
  (not a temporary one) and replaces its files to update; `claude plugin update persona@persona`, then
  `/reload-plugins` in a running session (or its next start), picks the new version up. A user-scope plugin installed
  from a terminal also loads in the local sessions Claude's desktop app starts.
- Files: `.claude-plugin/plugin.json`, `.claude-plugin/marketplace.json`, `hooks/hooks.json`, `hooks/register.ts`,
  `hooks/bridge.ts`; `tests/` is for `claude plugin test` only. A node in standby (Persona Link while its manager is
  away) or an older node gives no HTTP answer; the plugin backs off and tries again.
- Needs a Claude Code with function hooks and `$.prompt.submit`'s `asUser` (measured on 2.1.291, 2.1.299 and 2.1.308).
  An older Claude Code does not load the module; the node then keeps using the peer inbox.

Check it: `claude plugin validate integrations/claude-code-plugin` and `claude plugin test integrations/claude-code-plugin`.
Try it in one session without installing anything: `claude --plugin-dir integrations/claude-code-plugin`.

## Contract with the node (protocol 1)

The plugin talks HTTP/1.1 to the node's own socket, the one hooks and `personad ask` use: `$PERSONA_HOME/node.sock`
when `PERSONA_HOME` is set (resolved as the node resolves it), else `~/.persona/node.sock`
(`%USERPROFILE%\.persona\node.sock` on Windows). The node tells HTTP from its JSON-line requests by the first line.
Every request carries the session id from `$.session.id()`. The node answers only the claude process that has that
session open: it asks the system for the process id at the socket's other end (`SO_PEERCRED`, `LOCAL_PEERPID`,
`SIO_AF_UNIX_GETPEERPID`) and looks it up in Claude Code's `sessions/<pid>.json`. Any other process, or a request for
another session, gets `403`.

`POST /claude-plugin/v1/poll`

```json
{"session_id": "<uuid>", "plugin_version": "0.3.2", "engine_version": "2.1.331", "capabilities": ["busy_input", "interrupt", "settings", "permissions"], "classic_passed_over": true, "wait_ms": 25000}
```

- Held until work comes or `wait_ms` passes (at most 25 s; Claude Code's fetch gives up at 30 s), then asked
  again at once. A newer poll for the same session answers the older one at once, empty. The plugin polls whether the
  session works or rests; when `busy_input` is advertised, owner prompts are also handed over while a turn runs so
  Claude Code queues them natively without holding them at the computer in `bypassPermissions` or `auto` mode.
  `settings` overrides can be delivered to any active poll.
- `200`: `{"messages": [{"id": "<id>", "text": "...", "deadline_ms": <epoch ms>}], "interrupt": {"id": "<id>", "deadline_ms": <epoch ms>}, "settings": {"id": "<id>", "model": "...", "effort": "..."}, "permissions": true, "retry_ms": <ms>}`.
  `messages` is empty when nothing came; `interrupt`, `settings` and `permissions` are omitted when inactive.
  `permissions: true` is returned only when the session is served by the plugin and not managed by `persona-hooks` or an
  external tmux pane. `retry_ms`, when present, is how long to wait before the next poll: a session the node does not
  deliver to this way (one in a Persona terminal, the assistant's own) is told to come back much later.
- For a Desktop session, `observed_effort` reports the last main-loop step after session overrides, for example
  `{"at_ms": 1790000000000, "level": "high"}`. The timestamp belongs to the step. A missing or numeric effort has no
  named level. An applied `auto` choice remains automatic. A timestamped `reset: true` clears the observation on reload
  or session change. Subagent steps do not update it. The node uses the observation while the same session and process are
  live, and compares it with transcript evidence and newer session choices. A machine default is never a readback.
- `classic_passed_over: true` is sent after a main-loop turn used tools but no `classic.PreToolUse` arrived.
  Claude Code's managed-settings guard can do that to every `classic.*` hook in a user-tier plugin. The node then
  returns no `permissions: true` for that session, refuses direct `/permission` calls with `passthrough`, withdraws any
  active plugin card and leaves `clq_` question cards alone. Approve on the computer or start a session in a Persona terminal. The field is omitted while classic hooks reach the plugin.
- `403 {"error": "not_this_session"}`: the caller is not the claude that has this session open. `404`, or an answer
  that is not HTTP: a node without this protocol. The plugin backs off (up to a minute) and tries again.

`POST /claude-plugin/v1/permission`

```json
{"session_id": "<uuid>", "req_id": "<id>", "wait_ms": 25000, "tool_name": "Bash", "tool_input": {"command": "ls"}, "tool_use_id": "toolu_..."}
```

- Only called after `/poll` authorizes `permissions: true`, the plugin has not reported `classic_passed_over`, and after
  giving fast local command hooks (`next(e)`) a 500 ms grace window. Pairs `tool_use_id` from `classic.PreToolUse`.
- `200`: `{"action": "renew"}` (keep renewing with the same `req_id` under the 30 s fetch cap),
  `{"action": "decide", "decision": {"behavior": "allow" | "deny", ...}}`, or `{"action": "passthrough"}` (fall back
  to the computer UI).

`POST /claude-plugin/v1/permission-done`

```json
{"session_id": "<uuid>", "req_id": "<id>", "tool_use_id": "toolu_...", "reason": "post_tool_use"}
```

- Sent asynchronously from `classic.PostToolUse`, `classic.PermissionDenied`, `turn.complete`, or when a slower local
  hook wins the race, so the node immediately withdraws the phone card and ends any active renewal wait.

`POST /claude-plugin/v1/ack`

```json
{"session_id": "<uuid>", "id": "<id>", "submitted": true}
```

`submitted: false` with a `reason` (`late`, `invalid`, `command`, `dropped`, `failed`) when the plugin did not submit it.
The plugin submits a message only before its `deadline_ms`; after that the node may send it through the inbox instead.
The 250 ms settle window only releases the poll loop. A submission still pending then sends no final ack until its real
result arrives; the node can independently confirm its queue/prompt line. The timer therefore never turns a later
rejection or `drop` into an earlier success.

Registration is the poll itself: a session counts as having the plugin while its claude process lives and a poll from
that process waits, or ended less than 90 s ago. The node lists such a session with `capabilities.plugin_input: true`.

### Status for installers and diagnostics

On the same socket, the JSON-line request `{"kind": "claude-plugin.status"}` answers
`{"protocol": 1, "sessions": [{"session_id", "pid", "plugin_version", "engine_version", "polling", "seen_at"}]}`,
one entry per live session whose plugin has polled this node since the node started. An empty list means no running
session has loaded the plugin, or none has polled yet (a session polls as it starts, then without a break).

## What goes this way

Text the owner typed in a Persona app (`owner_input` on `session.send`, idle or busy when `busy_input` is advertised),
owner-attested turn interrupts (`interrupt`, `$.turn.abort`), and session-scoped model / effort overrides (`settings`).
Everything else keeps the peer inbox: messages from the Persona assistant, extensions and channels (Lark, WeChat),
relays between conversations, slash commands, the answer to a question the session asked, and non-owner interrupts.
Details and fallback rules:
`docs/integrations/claude-integration.md`.
