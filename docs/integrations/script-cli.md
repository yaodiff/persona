# Script commands: `personad ask` / `personad notify` / `personad decision` and direction writes

Let scripts, CI, long-running jobs, and programs that don't run in herdr reach the phone directly, or write a direction from outside an Agent session:

- `personad ask "question"`: blocks until someone answers on the phone; the answer becomes the exit code and stdout.
- `personad notify "title" [body]`: sends a notification, best effort by default.
- `personad create-direction <name> --project <slug>`: creates a direction and prints its leader as JSON.
- `personad write-progress <direction-id>`: replaces a direction's progress and prints its leader as JSON.
- `personad decision raise --file decision.json`: raises a rich Needs-you decision and returns its durable id immediately; `get` polls the answer and `withdraw` cancels it.

Inspired by the commands of the same name in moshi-hook. Like `personad hook`, all five commands only talk to the local node (`~/.persona/node.sock`, 0600, same system user).
They never touch the hub token and need no network configuration: if node is connected to the hub they work; if not, they follow the exit codes below.

A resident node (Persona Link, the Mac's launchd node, a service node) still answers while its manager is away, in standby:
`notify` is kept in `~/.persona/node-spool.jsonl` and sent when the manager is back (the command exits 0 at once; see `--strict` below).
`ask` is told to retry during the hold (10 minutes after the manager was last connected) and gets its card if the manager is back within `nodeclient.ResumeWait` (2 minutes);
past either, the phone counts as unreachable (exit code 4). Details: `../protocol/protocol.md` ("When the manager is away (resident nodes)").

## Commands and exit codes

```
personad ask "question" [--options a,b,c] [--timeout 10m] [--require-remote]
personad notify "title" [body] [--silent] [--error] [--strict]
personad create-direction <name> --project <slug> [--machine <machine-id> --session <session-id>]
personad write-progress <direction-id> [--reached <text>] [--doing <text>] [--needs-you <text>] [--link <url> ...]
```

`ask`'s flags can go before or after the question; if the question starts with `-`, put it after `--`. `--timeout` takes seconds (`90`) or a duration (`10m`); the default is 30 minutes (same as hook), the maximum 24 hours.
`--options` is comma-separated, so an option can't contain a comma. Empty and duplicate items are dropped; at most 12 items, each at most 100 characters.

| Exit code | `ask` | `notify` |
| --- | --- | --- |
| 0 | Approved (no options), or an option was picked (printed to stdout, one line) | Always 0 (best effort); with `--strict`, it means at least one push device or WebSocket connection was selected |
| 1 | Denied; or "Hand back to computer" was tapped on the phone and there's no one to ask in the terminal | Only with `--strict`: the hub did not accept it (no node, hub not connected, hub too old), it was queued for later (the manager is away; it still goes out when the manager is back), or no recipient was selected: `muted` (session muted), `no_device` (no reachable push device or capable WebSocket connection), `filtered` (all reachable recipients declined by notification preferences or desktop suppression) |
| 2 | Usage error (no question, too many options, invalid `--timeout`…) | Usage error (no title, more than two arguments…). This is a mistake in the script, so it is not covered by "best effort" |
| 3 | Timed out, nobody answered | - |
| 4 | The phone can't be reached: no node, node not connected to the hub, hub didn't accept it | - |
| 130 | Ctrl-C / SIGTERM; the card on the phone is withdrawn too | - |

Human-readable diagnostics after the `personad ask: ` / `personad notify: ` prefixes come from the `scriptcli-ask` and `scriptcli-notify` catalogs in the terminal's language (`PERSONA_LANG`, then the POSIX locale variables; English, Simplified Chinese and Japanese, with English for an unknown language). The chosen option on stdout, exit codes, output streams and line order do not change with language. Error detail returned by the node or hub is passed through as it is, never translated by matching its text. Usage and flag help stay English.

- On exit code 4 or "Hand back to computer", if stdin is a real terminal (not `/dev/null` or a pipe) and `--require-remote` is not set, it falls back to asking in the terminal: a numbered list of options (type the number or the option text),
  or `[y/N]` without options. EOF counts as deny, and Ctrl-C / SIGTERM at that question exits 130. A timeout or a denial is an answer, so neither falls back to the terminal.
- Without `--strict`, a failure only writes one line to stderr, `Not sent: <reason>` (localized), and the exit code stays 0, so a `set -e` script is never broken by a notification.
  A queued notification (`{"queued":true}`, a resident node in standby) writes nothing and exits 0; with `--strict` it writes `Not sent: <reason>` and exits 1, because nothing was sent yet.
- The "confirmation" of `--strict` means it was handed to push or a capable WebSocket connection (`{"sent":true}`), not that the phone displayed it: notification delivery has no receipt anyway (see `../protocol/protocol.md` ("Notifications over the persistent connection (ws_notify)")).

## Direction writes from outside an Agent session

`create-direction` and `write-progress` are for programs that are not running inside a Persona Agent session. They serve the same direction and progress model as the leader tools, but take their identity explicitly instead of discovering an Agent:

- `--project` is required when creating a direction. `--machine` and `--session` are optional only together and must both be non-empty when supplied. The command never infers a session from its working directory, terminal pane, environment or transcripts.
- Flags can go before or after the positional argument; a bare `--` ends flag parsing, so a name or direction id beginning with `-` can follow it.
- Every progress write replaces all three text fields. An omitted `--reached`, `--doing` or `--needs-you` is sent as an empty string, clearing that field; this is not a patch of only the supplied flags. At least one of the three must be non-empty. No text, including a write with links alone, is a local usage error and does not contact the node.
- Repeat `--link` for evidence links. Values stay in command-line order and are never split on commas, so a URL containing a comma remains one link.
- On success, stdout is exactly one compact JSON object, `{"leader":{...}}`, followed by a newline. The leader object is passed through from the manager. Diagnostics go to stderr with the `personad create-direction: ` or `personad write-progress: ` prefix.

| Exit code | Both direction commands |
| --- | --- |
| 0 | The manager replied `ok`; the leader JSON is on stdout |
| 1 | The manager replied `failed`; its error is on stderr |
| 2 | Local usage error, or the manager replied `invalid`; no usage error dials the node |
| 4 | The manager replied `unavailable`, the socket transport failed, the reply was malformed, or an old node does not know the kind |
| 130 | Ctrl-C / SIGTERM interrupted the call |

Each command makes one bounded (30-second), one-shot node.sock request. It never retries a write and never sends an automatic second request, even after a timeout, a broken connection or a reply asking for a retry: the first write may already have reached the manager, so only the caller can decide to try again. A manager error string is passed through unchanged; usage and flag help stay English, and the missing-node diagnostic reuses the existing localized script CLI text.

## Non-blocking owner decisions

Use `decision` when a job needs the owner's rich decision but must not keep a terminal, Agent or model turn waiting:

```
personad decision raise --file decision.json
personad decision raise --file - < decision.json
personad decision get <decision-id>
personad decision withdraw <decision-id>
```

A raise document contains the required question and its context, 2–4 options with outcomes, a recommendation, why only the owner can decide, the cost of waiting and reversibility. `todo_id` optionally associates it with an existing open to-do; omit it for a standalone decision. `ref` is an optional stable idempotency key for safe retries. `allow_default:false` opts this decision out of the normal reversible 24-hour recommendation default; omitting it keeps that default, and irreversible or visible-to-others decisions never default.

```json
{
  "ref": "nightly-2026-11-20",
  "title": "Publish the nightly build?",
  "background": "Checks passed; the release notes are ready.",
  "why_owner": "priority",
  "options": [
    {"label": "Publish", "leads_to": "Users receive the build"},
    {"label": "Hold", "leads_to": "No build goes out"}
  ],
  "recommend": "Hold",
  "cost_of_waiting": "The next nightly is delayed.",
  "reversible": true,
  "allow_default": false
}
```

`raise` writes one JSON `OutsideDecisionReply` to stdout and exits as soon as the manager has durably raised the decision; `decision.id` is the polling id and `decision.ask_todo_id` is the existing Needs-you backing row. Repeating the same caller, `ref` and normalized request returns the original id with `inserted:false`, without a second card or push. Reusing the ref for different content is a conflict.

`get` prints the same durable polling view. Its `decision.state` is `pending`, `answered` or `withdrawn`; after an answer, `decision.answer` carries the selected `option`, fixed `response` or owner's `text`, and `answered_by` is `owner` or `default`. `explain_first` is a recorded fixed response but deliberately leaves the state pending and stops the default, allowing a later final answer. The existing Needs-you card offers all options, the recommendation and the three fixed answers; its background names the authenticated script source. No answer is relayed to an Agent or supervisor, and Persona never invokes a model to answer one of these decisions on the owner's behalf.

An option may also carry a stable `id`, independently of its localizable `label`. IDs are unique ASCII protocol values. `recommend` may name an ID when no option label matches exactly; the owner still sees the label. After an answer, `answer.option` preserves that label and `answer.option_id` carries the stable ID. Automation should authorize only an exact expected ID (for example `Approve`), never a translation, prefix or case-folded label. Options without an ID preserve the original label-only response.

`withdraw` removes a pending decision from Needs you and fences later answers. Repeating withdrawal is idempotent. Withdrawing an answered decision fails with `decision_closed`; an answer that arrives after withdrawal is refused. Only the same connected computer can poll or withdraw its script's decision.

Unlike `ask`, these commands never wait for an answer, reconnect to a blocking interaction, spool in standby or fall back to the terminal. A script polls with `get` at its own cadence. Raise documents and ids are bounded (64 KiB JSON input, 30-second request timeout); question text still goes to the owner's devices, so do not put secrets in it. The equivalent HTTP routes, complete response fields and `decisions.raise` extension scope are in `../protocol/protocol.md` ("Decisions raised outside a supervisor").

### User-mode CLIs, including `feishu-cli --as user`

A user-mode CLI that talks to Lark directly does not pass through Persona's node command gate. Prefer routing the operation through that gate and a standard Agent API: ordinary reads need no card, a write gets the owner card derived from its real method and path, and the node's one-use grant authorizes exactly the approved request. The upstream needs no Persona-specific approval header, callback or consumption endpoint.

For an operation that cannot be migrated yet, an administrator can install a fixed wrapper around the real binary:

1. Map each supported subcommand to the real operation and target. Reject unknown subcommands instead of guessing whether they read or write. Do not put message text, document contents, tokens or keys in the decision document.
2. Raise an ordinary `personad decision` with that derived operation and target, option IDs `Deny` and `Approve`, independently localized labels and outcome text, `recommend:"Deny"`, `reversible:false` and `allow_default:false`. Use a stable `ref` when retrying a lost raise response; a different operation needs a different ref.
3. Poll `personad decision get <id>`. Execute the original, unchanged argv only when the state is `answered`, `answer.answered_by` is `owner` and `answer.option_id` is exactly `Approve`. A missing ID, a translated label without that ID, denial, withdrawal, timeout and malformed replies execute nothing. Do not automatically execute again after an uncertain process exit.
4. Expose only the wrapper to the calling environment. For example, its final step after those verified answers may be `exec feishu-cli --as user "$@"`; reaching this line without all three checks is a wrapper bug.

This wrapper is **advisory**, not a hard security boundary, while the original binary, its credentials or another user-mode API remain directly callable. For a hard boundary, remove those direct entries and credentials from the Agent environment and expose only the wrapper, or route the operation through Persona's gate. The repository does not install or configure this wrapper; deployment is an administrator's explicit local choice.

| Exit code | `decision` |
| --- | --- |
| 0 | The request succeeded; a raise or poll may still be `pending` |
| 1 | `decision_not_found`, `decision_ref_conflict`, `decision_closed`, or another state conflict |
| 2 | Usage error, malformed JSON, or `invalid_decision` |
| 4 | The local node, hub or manager is unavailable, or the node is too old |
| 130 | Interrupted by Ctrl-C / SIGTERM; an already-raised durable decision is not implicitly withdrawn |

Diagnostics use the `scriptcli-decision` catalog and the same language selection as `ask` / `notify`. Stdout remains one machine-readable JSON result; errors go to stderr.

## Path

```
personad ask / notify / decision
  └─ node.sock  {"kind":"ask"|"notify"|"decision.raise"|"decision.get"|"decision.withdraw","input":{…},"pane":"…","attempt":"…"}   one request / one reply per connection, one JSON line each
       └─ node (node/scriptcli.go)
            ├─ ask      → manager request interaction.create (assistant/fleet_events.go, same path as hook) → blocks until answered / expired
            ├─ notify   → passed on by the manager to the hub's notify (deliverNotify in hub/notify_api.go, shared with POST /api/v1/notify)
            └─ decision → short decision.raise / decision.get / decision.withdraw request to the assistant manager; polling history is durable
```

Direction writes use the same one-JSON-line request / reply framing, with no `pane`, `attempt`, hub token or resume marker:

```
personad create-direction
  └─ node.sock  {"kind":"direction.create","input":{"name":"…","project":"…","machine_id":"…","session_id":"…"}}
personad write-progress
  └─ node.sock  {"kind":"direction.progress","input":{"id":"…","reached":"…","doing":"","needs_you":"","links":["…","…"]}}
```

`machine_id` and `session_id` are omitted together when no explicit session was supplied. The CLI always includes the three progress text fields, including empty strings, and omits `links` when none were supplied. The node forwards one request to its manager and returns one reply:

- `{"status":"ok","leader":{...}}` on success;
- `{"status":"invalid","error":"…"}`, `{"status":"failed","error":"…"}` or `{"status":"unavailable","error":"…"}` on the matching failure.

node's replies for `ask` / `notify`:

- `ask`: `{"status":"answered|denied|dismissed|timeout|unavailable|invalid","answer":"…","error":"…","retry":true}`.
  `retry` means node is exiting (hub or node restart) or is in standby within the hold; the command reconnects to the next node with the same `attempt`, for at most `nodeclient.ResumeWait` (2 minutes), exactly like hook.
- `notify`: `{"sent":true|false,"reason":"muted|no_device|filtered","queued":true,"error":"…"}` (the reason describes hub-side suppression; `queued` means a resident node in standby kept it and sends it when its manager is back).
- `decision.*`: `OutsideDecisionReply` (`decision`, `inserted`, and on refusal `code` / `error`). Decision requests omit the resumable `attempt`; retry a lost raise with the same stable `ref`.

An old node doesn't know `ask` / `notify` / `decision.*` (replies `{"error":"unknown kind"}`), and the command treats that as unreachable. With a new node and an old hub, the `notify` request fails with unknown method and is likewise treated as not sent; a decision request is unavailable. Direction commands map the same old-node reply to `unavailable` (exit 4).

### What `ask` looks like on clients

It reuses existing question and permission card shapes. Cards without a session open directly on supported clients, including the web (#1882); see "Attribution" below.

| Command | `interaction.kind` | Card |
| --- | --- | --- |
| With `--options` | `question`, one `Question` (`header` is `脚本` ("Script"), `multi_select: false`, one `label` per option) | The same question card as AskUserQuestion; the answer is `answers[<original question text>]`, the option or free text (when the card supports "Other") |
| Without `--options` | `permission`, `summary` is the question | Allow / Deny card; the notification buttons can answer it directly |

A direct node.sock caller can add `"format":"markdown"` to the ask `input`. The node preserves that opt-in as the interaction's top-level `format`; omitted, `plain` and unrecognized values stay plain and emit no format. On iOS and the web, an opted-in permission summary or question uses the same Markdown renderer as chat replies, including inline styles, lists and short tables. The original Markdown source remains the answer-map key. Notifications and clients without Markdown ask support keep their existing literal text. This does not change the `personad ask` flags, terminal fallback, or the 1000-character question limit.

Both carry `tool_name: "personad_ask"`, `agent: "script"`, `agent_label: "脚本"` ("Script"; this is how runtime cards label "who is asking", `runtimes.md`):
the notification title is "Script Has a Question" / "Script Needs Your Permission", and the card offers no "Always Allow", so it is easy to tell apart from Claude's own permission cards.
`resumable: true`, and the id is derived from `attempt`: when the hub or node restarts, the card on the phone isn't lost, and the reconnected command reattaches to the same card (`../protocol/protocol.md` ("Interactions survive restarts")).
When the command is killed (Ctrl-C), node sends `interaction.cancel` and the card is withdrawn.

### Attribution

The command reads the caller's pane with `nodeclient.HookPane` (`HERDR_PANE_ID`, then herdr-core's `HC_TERMINAL_ID`, Orca's `ORCA_PANE_KEY`) and sends it to node with the request.
node uses the "pane → session" table learned from hooks (`bindPane`) to find the Claude session in that pane:

- Found: the card / notification is attached to that session (`session_id`), the card's subtitle is the session title, tapping the notification opens that session, and `notify` sends nothing when that session is muted.
- Not found (Claude Code never ran in the pane, no hook has fired yet, or it isn't in herdr): `session_id` is empty, the card subtitle is the name of the calling directory, and the notification subtitle is the machine name.
  iOS / Mac / watch (#4059), Android and web (#1882) can still answer: explicitly sessionless cards do not check session-source approval permissions. On the web, Overview and Inbox open `/requests/<id>` with the existing question or permission card; ordinary session cards still require that session's approval capability. New requests notify individually, using the device's existing notification preferences, and opening one uses the same request route.
  Tapping there or in the Inbox opens the card directly (no chat exists to open).
- `ask` also carries the calling directory (`cwd`). `notify` carries no path.

### Preferences and muting

`notify` uses the hub's existing "send notification" path (the same function `deliverNotify` as extensions' `POST /api/v1/notify`): a device that turned off "Needs You" (`prefs.input`) doesn't get it, and a muted session / space sends nothing (`reason: "muted"`).
Delivery goes through APNs (direct or via the push relay) and the persistent connection event of `ws_notify` (`kind: "alert"`). If neither channel selects a recipient, the hub returns `sent: false`: `no_device` when no device has a usable push route and no capable connection is online; `filtered` when reachable recipients exist but all are excluded by "Needs You", manager notification switches, "Phone Muted", or the open-desktop rule. A selection on either channel, or a browser's Web Push (a subscribed browser with no Persona page open), is enough for `sent: true`; asynchronous APNs or relay failures do not change that reply.

- `--error`: `interruption-level: time-sensitive`, and the title gets a "⚠️ " prefix; the default is `active`.
- `--silent`: no `sound` in `aps`. `notifyRequest` gained an optional `silent` field for this, which the HTTP API also accepts. Through the push relay the phone takes the sound from the sealed `aps`, so it is quiet there too; only the "New message" fallback of a push that can't be opened rings (`../operations/push-relay.md`).
- The title is at most 120 characters, the body at most 1000; anything longer is truncated in node, without an error.
  The push itself is bounded in bytes (3500 encoded), so the hub then cuts the body further, by characters with a trailing "…", until it fits (`fitBody`): 1000 emoji or escaped `<` `>` `&` are still delivered. Only a title too large on its own is refused (`notification is too large`). Extensions' `POST /api/v1/notify` is not cut: it still gets a 400 over the limit.

## Clients

- Notifications: `kind: "alert"` and `aps` already exist; no changes needed.
- Question / permission cards reuse the existing shapes. iOS and the web honor the optional Markdown format; other clients keep their literal-text rendering.
- **Not checked on a device / simulator**: "Unverified" below lists what to look at.

## Security and privacy

- node.sock is a 0600 unix socket; only processes of the same system user can connect, which is the same trust boundary as hook and MCP. Any process of this user can pop things up on the phone with `ask` / `notify` / `decision`, so cards are clearly labeled "Script" or name the script source. The same user boundary authorizes `direction.create` and `direction.progress`; neither direction command reads or sends a hub token.
- An `ask` with options is an ordinary question card: the assistant's existing rules for `interaction_answer` apply to it too (the assistant may answer on the user's behalf when allowed, `docs/assistant/assistant.md`), and the command can't see `answered_by` in the answer. To make sure only a human can answer, use the form without options (the assistant can't answer permission cards), or use `personad decision`, whose answer path never invokes the assistant or a model and reports `answered_by`.
- The question, body, and calling directory name go into the phone's notifications (end-to-end encrypted when going through the push relay, `../operations/push-relay.md`) and interaction history; don't put secrets in them. `notify` carries no path.

## Verification

- Unit tests: `server/internal/scriptcli` (exit codes, flags, terminal fallback, Ctrl-C, `--strict`, decision JSON input/output and polling/withdrawal); `server/internal/node/scriptcli_test.go` (fake hub + fake socket: card shape, Markdown format propagation and plain defaults, attribution, timeout, withdrawal, restart reconnect, `notify` parameters and truncation, decision forwarding); `server/internal/node/decision_script_test.go` (real local socket and node-derived source);
  `server/internal/assistant/outside_decisions_test.go` (manager lifecycle, idempotency, caller isolation, fixed answers, defaults, withdrawal and restart); `server/internal/hub/outside_decisions_scope_test.go` (explicit `decisions.raise` scope and missing-scope 403);
  `server/internal/hub/notify_api_test.go` (`silent`, `notify` from node: muting, preferences, no extension fields, the body cut to fit the push).
- Direction unit tests: `server/internal/scriptcli/direction_test.go` and `server/internal/nodeclient/script_direction_test.go` (exact wire payloads, interspersed flags and `--`, machine/session pairs, links-only rejection, ordered links containing commas, no dial on usage errors, compact stdout, stderr and exit codes, malformed and old-node replies, deadlines, interruption and no automatic second request); `server/internal/nodecmd/direction_test.go` checks device-command dispatch.
- `apple/PersonaTests/PlanPermissionTests.swift` (`ScriptAskMarkdownTests`) covers the optional wire field, both card kinds and visual styles, and plain defaults; `web/src/views/chat/Interactions.markdown.test.tsx` covers the same behavior on the web, including the original answer key.
- The real `personad` binary was run against a fake node socket to check the exit codes and wire JSON of `ask` / `notify` (without touching a real node or phone).
- Not done: end-to-end against a real hub + phone (by convention, tests don't send real notifications).

Unverified: how "Script" cards actually display on Android; where cards with an empty `session_id` show up in the Agents page / Inbox (the hub handles them by `machine_id + id`; clients not checked);
whether `--silent` (no `sound`) is actually silent on a real device under APNs; notification delivery has no receipt. Physical-device display and end-to-end delivery remain unchecked on every client.
