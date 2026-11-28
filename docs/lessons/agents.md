# Lessons: Claude Code, Codex, and herdr

## A stopped Agent's row is a contract beyond the node (#17809)

- `send` missing from a stopped row is what the assistant's `stoppedAgent` reads. Making stopped Codex rows sendable,
  so that a message resumes them, also let a plain `agent_send` and automatic relays wake them past the explicit
  resume (#8099, #8214). A stopped Agent stays read-only on every engine (#4754); changing that rule takes the owner's
  decision and every gate changing with it, not one runtime's send path.
- Widening a glob (`rollout-*.jsonl*` for compressed rollouts, #17768) breaks tests that match the old pattern by
  string, and turns misses into hits: check that the hit cache survives the minute listing as the miss cache does.

## Codex dialog hints do not identify the active dialog (#17406)

Codex 0.172 shares submit/cancel hints between approvals and questions; match the active title and options, and
ignore approval titles above a composer or a newer question. PlainScreen drops dim styling: compare complete known
placeholders after joining wrapped rows, and preserve text that only starts with a placeholder.

## A missing queue receipt leaves delivery unknown (#17444)

Fall back to steering after an explicit queue rejection. A lost or incomplete receipt may follow a persisted submission, so steering then can deliver a duplicate.

## A held Codex rollout needs its engine's pane identity (#17406)

- A no-daemon Codex in herdr-core, Tern or Orca holds its own rollout. Resolve the process's engine-specific pane
  through `host.PaneRef` before recording ownership; an empty owner rejects even the waiting pane's own thread as
  another writer's. Exercise both direct held-rollout selection and new-thread filtering, including a different pane
  that must still be refused. Open-file fixture paths may be canonicalized by the OS; resolve symlinks in assertions.

## Shared Codex app-server snapshots can lag events (#17428)

Keep pending requests and same-turn blocked state across metadata refreshes. When waiting for an interrupt, read the current thread on each poll.
## A delivery label does not prove a disabled feature (#16936)

- A structured closeout can mistakenly say `delivery: off` while its missing work is unrelated data backfill. Require
  an explicit default-off statement in its missing or waiting gate before presenting that claim to the owner.
- Apply the same guard to historical acceptance cards and model judgments. Preserve a deployed report when it gives
  deployment evidence; without that evidence, keep an unverified delivery hold instead of inventing a disabled switch.
- Exercise the closeout-to-title path and the judge's criterion checks together; a second assignment of the raw model
  delivery can otherwise restore the unsupported state after normalization.

## Inbox handoffs are not task ownership (#17119)

- A conversation's historical handoff does not identify the source of every later task. Omitted sources must not
  claim unrelated handoffs, and continuations skip source inference. A strongly matching handoff is still inferred:
  retain its matching snapshot and recheck competing task ownership before admission. Explicit source selection is
  a separate contract.

## Persona's own agy hooks: report only, own one key, prove the pane (#13729)

- agy's global `hooks.json` is shared: herdr, Moshi and Orca each own a top-level group. Write only the `persona` key through the ordered-object editor (`editSettings`), never rewrite the others, and treat a changed command (moved binary) as stale, not as foreign.
- agy runs the hook with the terminal's environment, which has no `PERSONA_HOME`: a node with a non-default directory must put `--persona-home <dir>` in the command, or the hook dials the default node's socket.
- The hook must print `{}` and exit fast on every path (no node, unknown pane, bad input), or agy waits on it. Subagent conversations inherit the pane's environment and report too, so the node still has to prove the conversation is not a subagent's (#13336) before it reads or resumes.
- herdr's own report and Persona's can disagree for a moment after a new conversation; the node treats a disagreement as unknown instead of picking one.

## What the node starts is told the node's program, never left to find it by name (#14582)

- The node's program has more than one name: `personad`, `persona-node` in a Persona Link install, either with `.exe` on
  Windows. The Antigravity companion looked only for `<state>/bin/personad`, found nothing on a Link install and attached
  no MCP server, so its chats had no Persona tools and nothing said why. Hand a process the node starts the node's own
  spelling of its program (`selfProgram`'s `bin`, as the generated hooks and MCP configs have it), keep a lookup by name
  only for a process started some other way, and log when it finds nothing.

## Visibility-only `TurnErrorCode` values must not trip `readinessBlocks` or `CutOff` on node `TurnError` (#13372)

- `readinessBlocks` (`server/internal/assistant/readiness.go`) defaults every `StepProblem` code to `mode: "setup"` unless explicitly excluded. Any non-user-fix `TurnErrorCode` (such as `upstream_unavailable`, alongside `rate_limited`, `model_fallback_unavailable`, and `tool_search_unsupported`) must be excluded in `readinessBlocks` and paired with `test_assistant` and plain-language readiness strings, or an upstream outage locks every client's composer with "Connect a model first".
- Apply `assistantwire.CutOff` (`"API Error:"` prefix check) only when falling back to `Session.LastText`, never to a node-populated `Session.TurnError` (`latestTurnError`): Codex rollouts and runtime bridges report `5xx` errors as plain messages (`unexpected status 503 Service Unavailable`) that do not start with `"API Error:"`.
- When tracking multi-turn streaks in `transcript.Meta`, copy the streak fields in `Tail.Settle` (`server/internal/transcript/branch.go`) and preserve/merge them in `mergeModelFallback` (`server/internal/node/model_refusal.go`), which otherwise resets `m.turnErr.code` on every `bgScan` merge.

## Codex's `/model` with arguments: confirm from the rollout, and send the effort with a model (2026-11-23, #12572)

- Codex 0.169 and later take `/model <model> --reasoning=<level>` for this session only. `/model <model>` alone does not keep the effort: Codex
  puts the new model on that model's default level. To keep the user's effort, type it with the model when the new model takes it
  (`setCodexTerminalSettings` in `server/internal/node/codex_terminal_modes.go`).
- Confirm the change from the rollout, not the screen: Codex writes `thread_settings_applied` with the thread's `model` and `reasoning_effort` as
  soon as it applies them, between turns and mid-turn. The screen's `Model changed to …` line can scroll away or be a line from earlier; read the
  screen only for Codex's refusal, and only lines that are new since the typing (a repeated refusal is printed again below the first).

## An App update can leave a socket without its server (#13190)

- A Unix socket file proves only that a process bound that path at some point. After the Codex App removed its IPC router, the old `<CODEX_HOME>/ipc/ipc.sock` remained and a file-type check still advertised writable chats. Check that a listener accepts connections, with a short timeout and one probe per home per list, and handle a later connection failure as a normal control refusal.
- Match transport logs to the host that owns the threads. In App build 29803, `hostId=durable transport=websocket` describes a cloud connection; the local host still spawns a private stdio app-server. A socket path in a permission allowlist, or a daemon path behind an inactive process flag, does not establish an available endpoint. Inspect process arguments and actual listeners read-only before choosing a route.
- Keep App chat identity separate from transport availability. History and project grouping remain useful without control; a missing bus must not advertise send / stop / settings, accept a queued message, or invite another app-server to become the thread's writer. Preserve uncertain delivery after a request has actually gone out. See [Codex support](../integrations/codex.md#sessions-in-the-codex-desktop-app-app-local-ipc).

## Detached approvals and hub-owned device methods (#12918)

- When a prepared or detachable `PreToolUse` approval releases its hook with `approval.WaitingText` or `approval.ClarifyingText`, Claude Code writes that message into the transcript as an ordinary `tool_result` carrying the original `tool_use_id`. Transcript reconciliation for terminal-answered permissions (`syncPlanInteractions`) must skip `p.prepared != nil` and non-final approval tool results, or the retained 24-hour card is dismissed as `answered_by=terminal` a second later.
- Methods in `assistantwire.HubDeviceMethods` (`interactions.list`, `sessions.list`, `session.get`) never exist on nodes. Answer them in both `localFleetDeviceCall` and `hub.deviceCallFrom` for concrete machine IDs as well as `DeviceAll`, and never mask a missing manager/hub branch by injecting `f.nodes[DeviceInteractionsList]` in tests.

## A one-start speed must not become a resume default (#11141)

- A terminal Codex speed switch uses `/model --speed=standard` to reset the speed on Codex 0.172. The native setting
  records `default`. Confirm the switch from a new `thread_settings_applied` record because an already-running turn
  can still write its previous speed.
- Codex's per-start `service_tier` belongs only in the initial argv; validate it against the selected model's catalog rather than trusting Codex to refuse an unadvertised tier.
- Claude Code fast mode needs `"fastMode": true` in the effective single `--settings` file. Give the initial command a merged copy and keep the base `claudeLaunch` spec for herdr's `resume_argv`, or an automatic resume silently inherits the billable choice. A shell function that supplies its own `--settings` cannot be merged: refuse fast mode rather than appending a fast-only file that would discard the function's credentials and model.
- A new `session.start` field needs its own node capability: older nodes otherwise accept it and start at the launcher's speed. Faster speed on a resume or another runtime is a coded refusal before approval or launch.

## An account-sealed compaction needs a new conversation, not Continue (#10399)

- Classify the permanent cause before trusting a runtime's generic transient kind or a gateway's status. Cancel
  continuations already queued before the cause was recognized; hiding the button alone does not stop them.
- Archiving is visibility, not invalidation: a newer transcript restores it. Derive obsolete interruption rows from
  the linked task's current participants, parking and close state, and keep the same cases on every client.
- Error detection also runs on every ordinary session's last answer. Compare a case-insensitive prefix without
  lowercasing whole answers; an extra allocation per row broke the 8,200-session list's allocation budget.
- A wire kind/code must drive the invalid label and full subtitle too, not only hide Continue: raw-text-only row checks
  otherwise show an opaque error as an ordinary interruption. Apply the working/waiting replacement guard to each
  client's separate To-dos triage as well as the Assistant tray, and recheck permanent refusal in the timed-out resume
  path before it sends `session.start`.
- For mock screenshots, combine `MOCK_TASKS=1 MOCK_TASKS_CUT=1 MOCK_TASKS_INVALID=1` with the mock hub. This is UI
  evidence only, not deployment or proof that a real account-sealed conversation has disappeared from Needs you.
- Keep permanent compaction errors separate from retryable tool-search refusals when merging error handling.
  Preserve both wire codes and both failed-turn tests; loading all tools can fix tool search, not account-sealed context.


## A deferred action on an agent belongs to its process, not its session id (#10296)

- A session id outlives its Claude Code process: the owner quits and resumes, Persona's Resume starts a new one, a crash is resumed. A queued restart into Full access, keyed by session id alone for a day, quit a claude that resumed the conversation later in Default and restarted it in Full access.
- Bind anything that waits to act on "the agent" to the process it was decided for: pid plus start mark (`hookTurns.process`, else `sessions/<pid>.json` via `liveClaudes`), checked with `processAlive` at every look and again right before acting. Drop it once that process is gone or the hooks report another one; refuse it where the process cannot be told.
- Keep the wait short. "Once this turn ends" is not "whenever this conversation next runs".

## Approval occurrences must survive every asynchronous handoff (#11310)

- A runtime interaction ID is not an occurrence. Carry its creation time (and the exact-call fingerprint for detached commands) through live announcements, delayed terminal events (including the public `interaction_resolved.created_at`), reconnect catch-up, text asks, Feishu posting placeholders and persisted ledgers, callbacks, and finalization. A stale A event must neither close nor suppress same-ID B; zero-time legacy records keep their ID-only behavior. Clients compare the original RFC3339 text or an exact instant, not a millisecond `Date`.
- Restart-generated delivery text is not execution truth. If a live owner claim wins, its honest outcome replaces the unfinalized recovery result. Recovery must not write a competing terminal status while that claim runs; reload the journal after finalization before revoking a grant or delivering a result, and reconcile an already-durable terminal record rather than retrying a conflicting cancellation forever. An interrupted apply is different: its execution-uncertain `failed` outcome must not become a confident `cancelled` / "not started" when the restored owner claim reaches the journal's terminal-result guard.
- A fresh retry interaction is not the original approval. Keep `ApprovalID` when retiring an edited command's original grant and when reporting the edited execution. Generate fallback delivery IDs from the durable operation identity: random per-attempt clarification IDs defeat receipt retry, while a pre-upgrade continuation's legacy queue identity must still deduplicate its journal redelivery after text rewriting.
- A journal's terminal `Result` is complete delivery text, but `finishPrepared` accepts a detail and adds the standard prefix. Strip exactly that entry's own prefix when closing a restored live card, or recovery delivers the prefix twice. When a test backdates an occurrence, update the SQL `created_at`, JSON payload, owner book and Feishu ledger to the same instant and precision; otherwise the occurrence guards correctly reject the fixture as different operations.

## Create an agy ACP session before its role-scoped MCP (#15327)

- The Companion's first `session/resume` both attaches `personad mcp` and triggers native agy's initial catalog read. A task role linked only after `session.start` returns is therefore too late for that catalog; `notifications/tools/list_changed` is not evidence that agy lists again. Create with no MCP, return the real `agy-<uuid>` as `starting: true`, link the role and supervision relationship, then let the ordinary first `session.send` resume with MCP and prompt.
- Rolling upgrades need no new activation RPC: an older Companion ignores `defer_mcp` and finishes normally, so only an answer actually marked `starting: true` gets the post-link send. Keep the same `client_msg_id` across the start and activation. Close a definite unused start, but never close or retry an uncertain activation whose prompt may already be running.

## Project MCP enums only in agy's tool declarations (#9944)

agy can turn a valid boolean enum such as `task_review`'s `evidence.ready: enum [true]` into an empty Gemini string
enum and reject the whole request. In its proxy's matched `tools/list` responses, omit enum constraints that are not
nonempty arrays of nonempty strings. Walk schema keywords, not arbitrary objects: a property named `enum` is still
a property, and defaults, examples and constants are data. Keep call arguments and the server's schema and validation
unchanged. Regress with the real tool schema and proxy wire flow; a `tools/call` result containing `tools` is not a list.

- Keep filesystem reads under a lock specific to the Codex home. Opening an existing directory can wait for
  permission even after its metadata is readable. Bound the list's total wait and preserve the last observation
  while a home's rollout and App database scan is pending (#13812).

- Keep Codex app-server ancestry as raw thread IDs until session decoration maps it into home-scoped public IDs
  (#9393). Mapping a secondary home's parent twice leaves the child orphaned. A terminal start already claims its public
  ID from the matched rollout. Bind that ID directly to preserve the source home and native thread for later messages
  (#13737). Model-facing Agent lists must reuse
  `folded_under` and the parent's folded state too; fixing the client list alone still leaves helper rows in
  `agents_list` and the main chat's counts. Do not hide an unknown-parent session by its title or subagent flag alone.

## Compact unattended delivery from actual context and user activity (#8658)

Supervision and scheduled messages are inputs but are not user activity. Keep their structured source when deciding
whether the owner has been away; reset the idle clock when the owner's input arrives, before a send lock can wait.
Claude's `result.usage` and `modelUsage` aggregate requests and cannot stand in for context size. Use the last
main-model request's input plus cache-read and cache-creation tokens, or `compact_boundary`'s `post_tokens`.
Wait for the compaction command's result, not just its boundary, before writing the next message: the result still
ends that command. Persist the attempt latch with the session, and rearm only after user activity or a context drop
below the threshold followed by another idle interval. Failed compactions must not turn every background wake into
another summary attempt.

Node-only JSON round trips do not prove this state survives a restart. The manager decodes `state.put` through
`db.NodeSession`, then writes the `node_model` settings value and projects it back for `state.load`. Carry the opaque
idle state through every boundary, and test the real manager entry points against Postgres after reopening the store.

## An acknowledged Enter does not confirm Codex consumed a message (#8354)

Terminal key transport can succeed while the paste stays in the composer. Check consumption after both the host prompt
and the blocked-session fallback; preserve drafts and report unknown delivery if it cannot be observed. Do not recover
a pre-existing draft merely because it equals earlier assistant text: equality proves neither ownership nor non-delivery.
Use an isolated fixture that acknowledges Enter without consuming the paste, and prove it fails without the production
check. This demonstrates the confirmation gap, not the cause of any particular live incident.

## herdr's "idle" is not "ready for the first message", and `agent.prompt` only types (#11345)

herdr reports a work agent as soon as it sees its process, and calls agy idle as soon as its input box shows, but agy
keeps signing in after that (model list, slash commands, skills and hooks reloaded) and can drop a message typed then.
herdr's `agent.prompt` without `wait` answers `ok` once it has typed the text and Enter, so a start that sends once and
returns reports a dropped task as started. On one Windows node two of three agy starts lost their task this way, with
the Enter landing at the same time after agy's "ready" line in the lost and the delivered starts alike, so a fixed
delay is no fix. Confirm instead: the agent turns working, blocked or done, or its integration reports a session.
Retype only what is provably gone (the agent still idle, nothing of the message or a `[Pasted text …]` chip on screen),
and otherwise list the session as needing the user.

# A continuation must stay on its original to-do (#6820)

When replacing an Agent to fix or retry work, keep the original to-do and immutable brief. Stop/release prior Agents at Start and reopen a closed to-do in place; failed starts must not create successor tasks. For Codex edits, a plan/read-only sandbox needs a fresh process with explicit writable permission. Keep takeover and legacy-successor duplicate refusals intact.

## A start-turn into a running Codex App turn is delivered twice (2026-11-17, #9416)

The Codex App's bus offers a follower only `thread-follower-start-turn`. Into a running turn Codex's app-server steers it
(`start_or_steer_turn`), and the App then runs the same request again as a new turn once the running one ends. The
Agent answers it mid-turn, then starts over on it, while Persona's own read of the chat shows it once. Before asking the
App for a turn, look whether one runs (rollout tail, or the history database for a chat without a rollout) and hold the
message on the node until it ends; count a turn the node just started as running until the App writes it down, or two
sends in a row steer the second into the first's turn. On Codex's own app-server, steer with `turn/steer` and the
observed turn id instead; never send both a steer and a start for one message.

## Task workspace sources and ownership (#16161)

For a local linked checkout, keep the source Git directory's identity separate from the
owned task path. Otherwise a generic delete guard can claim the user's source repository.
Save the chosen commit and operation before dispatching a side effect. A missing capability
can select the older checkout path. An uncertain prepare or start must retain the same operation.
Use the same path check at preview and prepare so existing symlinks cannot pass one check
and fail the other.

On hosts with a symlinked home, Git records physical worktree paths (#16305).
Resolve the configured home prefix before deriving the task and repository paths, and
resolve the configured state prefix before appending control directories. Checking the
remaining components as written preserves the distinction between host aliases and
links inside the workspace.

## Task branch reuse is not forced worktree creation (#8357)

An explicitly named task branch may already have a checkout outside the engine's usual directory. Discover it from Git,
not from the generated path or an error message. Reuse needs successful clean-tree and complete live-Agent checks on the node,
plus the hub's unfinished-task claim check under workspace admission. Missing inspection is not proof of safety.
Persist reuse separately from creation and keep its machine/path on retry; do not relax ordinary `worktree.create` or Start approval.


Pitfalls, odd behavior, and the trade-offs made at the time (formerly in `CLAUDE.md`, index in `docs/lessons/README.md`). Add new lessons to the matching section; create a section if none fits.

## Local adapter metadata is not execution authority (#7940)

A runtime's model-list hooks belong to its session driver; they are not a configuration-only plugin. Reuse the
native CLI lifecycle for a local connection adapter instead of creating a second session owner. Only trusted
node-local configuration may name the executable: an inventory, a hub-supplied launcher name or a client scope
field must never become a shell command. Keep the latest directory revision separate from the actual process
binding so a refresh cannot interrupt a turn or silently change a model. Materialize an external default selection
into its exact model ID when it is saved; otherwise a remote default change becomes an unintended conversation edit.

A freshly loaded registry is not evidence that a saved target was deleted. Ordinary directory reads may remain
nonblocking, but validation of a specific persisted pick must join one bounded inventory load on a miss and then
repeat identity, scope and capability checks. Cover a completely cold registry and resident connection handover
with an already-running process; fixtures that always prewarm inventory hide both failures.

Pin connection environment in one final settings file while preserving Persona's hooks, MCP and permission fields;
process environment alone does not establish the intended precedence, and appending a second `--settings` can drop
the first. Let the node own the private output directory so group termination still removes credential files when
the adapter's cleanup cannot run. A required or unknown agent sandbox is unsupported until the bootstrap has a
verified sandbox boundary, not a reason to execute without it. Test initialization and control acknowledgments before
user input, not just that a wrapper process exists.

- A native terminal Codex start must preserve a user's same-name `codex()` function (#4876), not just find the executable. `codex-start` used to bypass it, losing launcher environment while a normal terminal command worked; the assistant app-server fix (#3707) did not cover this path. Pass the launcher and prompt as positional arguments to the interactive shell, never interpolate them as shell code, and keep the shell inside the agent sandbox.
  Fake-launcher tests must isolate rc paths such as `ZDOTDIR`, not just `HOME`: an inherited rc directory can otherwise load the user's real startup file and change the fake executable or flags.
  A native launcher must prefer the user's supported login shell even when another shell's rc file exists; test with both rc files present, not just one.

## A saved task name can disappear without another model failure (#7337)

Automatic naming had already succeeded for several instruction-titled sessions, but a terminal spinner's
`Claude Code` label later became the first user message. Matching both presentations against a narrow
instruction-prefix list hid the saved name, while the successful-change limit prevented another call.
Check the shared session response against the persisted raw title and counter before blaming the model.
Use proven `prompt` provenance for first-message copies and preserve the saved count across presentation
changes. An unknown source from an old node stays unknown: a hub deployment cannot establish manual intent
that the node did not report. Failure logs use only static reasons and session identifiers, never a provider
error body that could echo private text or credentials.

## Supervised handoffs and runtime children (#5751)

Codex sidecar parent/root metadata establishes display ancestry, not a to-do link. A standalone `create_thread` can
have no parent metadata at all. Use `task_spawn` for a separate session on the current supervised task; never work
around missing task tools by extracting the hub token. Codex's shared daemon may carry an old pane environment, so
its per-turn `CODEX_THREAD_ID` must resolve unambiguously in the node's registry before a CLI handoff. Unknown
threads must not fall back to whichever task an inherited pane happens to run.

The task handoff footer must describe the Agent that will actually run (#9846). Fit and health detours can change
both machine and family. Source matters too: a Codex terminal has no injected Persona MCP server, while Paseo's
Codex bridge does. A login shell need not have `personad` on PATH. Render after selection; give known terminal
callers the node's reported absolute executable as data for the existing scoped CLI. Bound the metadata read;
unavailable metadata or unknown capabilities omit unverified hints, not the assignment.
Keep the existing project context, approval snapshot and attachment order intact.

Read that executable from the `node.info` the node's connection began with, as the devices keep it
(`devices.Summary.Executable`), not from a fresh call (#14446). A node works `node.info` out whole on every call, Claude
Code's version from an interactive login shell included (`claudeProbe`). On a computer whose shell took about 2.4 s to
start, while `claude --version` itself took 12 ms, every fresh call outlasted the one-second bound, and every handoff
there lost its terminal entry. A value that cannot change while the connection lasts needs no round trip: ask the node,
within the bound, only when the devices name none.

A handoff note that reads as an instruction gets followed (#12995). A Codex worker at high reasoning effort read
"use the terminal entry below for a separate session on this assignment" as a step of its task: it handed its whole
assignment to a child in the same checkout, telling it the parent would coordinate and verify, then polled the
child's process, `git status` and transcript until it finished. Nothing said the caller still owns its assignment,
that the child shares its directory and permissions, or that the child reports to the supervisor, not to the caller.
The note now says each wherever an entry is offered. The server still allows writable children in the caller's
directory: continuations rely on them (`tasks_continuation_test.go`).

- A guest conversation's session id and tools are registered before the node starts it: Claude Code lists the
  Persona MCP server's tools as it starts, a later `guestSet` makes that first list empty, and a connected MCP server
  with no tools does not fail by itself. Verify `system/init.tools` and a real `tools/call`, not a server saying
  `connected` or the model claiming a tool exists.

- A queued AGY turn cannot own the executing prompt's text (#4564)

The pending-turn record can change before the previous ACP prompt finishes. Keep the executing prompt's stream
owner separately, clear its text at completion, and publish an empty boundary before the next prompt starts. Text
chunks separated by a tool call are distinct messages. Accumulating them across the whole turn makes progress and
final replies appear concatenated in the next exchange. Runtime wire tests cover queued and consecutive prompts.

The web hook must also forget raw live text once its transcript item replaces it. Hiding the derived visible text
alone retains a copy that can return when a new user message changes the matching transcript tail. A fresh delta
may legitimately repeat an earlier answer, so retirement follows the landed stream's lifetime.

- A source-search query containing a colon is not necessarily a URL: Go's `url.Parse` treats `bug: login` and `is:open login` as schemes.
  In `server/internal/node/worktree_composer.go`, only an explicit HTTP(S) prefix selects URL validation; other text remains a repository-scoped
  search, even when it contains a URL later in the query. Regress through `worktree.source` and check the exact query sent to `github.listWorkItems`,
  alongside numbered lookups, URL validation and rejection of links to another repository.

- Task placement must preserve locality (#1175): a failed launcher check is not permission to move an explicitly targeted task to another computer with the original absolute directory. Persist fixed placement by default through preview, card edits and retries; require an explicit flexible policy for portable work. Returning from background dispatch only proves the work was queued, not that a native Agent received its first prompt.

- A successful session deletion must survive a node reclassifying an unchanged offline transcript as `interrupted`. Apply persisted deletion notes to that status too, using machine/session ID and last-use time; neither a shared project name nor a missing worktree means another conversation was deleted (#5933).

- Recent directories for a new development session must be filtered with `identity.IsAssistantDir` after node combines transcript directories and running Agent directories, then sorted and limited to 15 (#1662). Assistant transcripts update continuously and otherwise dominate default suggestions; don't filter solely by the directory name `assistant`, which could hide a project with that name. Filtering affects only suggestions; existing sessions and explicit directory browsing remain.
- Multiple Claude Code PermissionRequest hooks run concurrently. The first `allow` grants permission; the others aren't ended and run until their own timeouts (2.1.308, #2648), leaving an expired phone card from the waiting hook. The answering hook must send `hook.decided` (`docs/integrations/claude-integration.md`). In bypass mode, remaining prompts mainly come from rm targets that can't be resolved statically (relative globs after `cd`, runtime-only targets); length, heredocs, and `eval` don't prompt.
- When starting claude in a new herdr tab, an untrusted cwd first prompts "trust this folder"; text sent by `agent prompt` becomes a selection → selects No and exits.
  node must check for a blocked screen after starting a session (`docs/integrations/claude-integration.md`).
- A fresh Claude Code 2.1.318 (never run on the machine) shows three screens before its input box (2026-11-07): the style picker with **unnumbered** choices
  (`Auto (match terminal)` first, cursor on `✔ Dark mode`), then "Security notes … Press Enter to continue…", then the folder's trust dialog (cursor on `No, exit`).
  The node's picker match expected numbered choices (a hand-written fixture), so starts on a new machine timed out after 1 minute on the picker.
  `firstRunKeys` answers the picker and the notes; fixtures now come from a real capture (`claude-2.1.318-theme-picker-120.txt`, `-security-notes-120.txt`).
- A dialog drawn at the top of a fresh, tall pane reads as the dialog then dozens of blank rows (2026-11-11: Claude Code 2.1.322's Bypass Permissions confirmation, 36
  blank rows in a 50-row pane). `screen.ParseDialog` and `dialogTop` looked back a fixed number of rows from the read's last row, so the dialog fell outside: no card
  choices, and its head read as `❯ No, exit`. Count rows back from the last non-blank one (`withoutBlankTail`), and capture fixtures from a pane taller than the dialog.
- Claude Code 2.1.322 shows the Bypass Permissions confirmation for `--allow-dangerously-skip-permissions` too (a plan that goes on in bypass), not only for
  `--permission-mode bypassPermissions`. Several `--settings` flags don't merge: the last one wins, so a per-start settings file can't be added next to Persona's hooks file
  or a launcher's own. Accepting the confirmation makes Claude Code write `"skipDangerousModePermissionPrompt": true` into the profile's `settings.json`
  (`CLAUDE_CONFIG_DIR`): reproduce it only with a throwaway config directory, never your own.
- Seeing an input box doesn't mean Claude can receive text (2026-11-05, #2423): an automatic update on Claude Code startup ("Updated to latest…") also showed an Auto mode explanation.
  After node waited for the input box, `agent prompt` swallowed the first message. The transcript was only 1 KB, with no user message, and the Agent sat idle for 1.5 hours. Check the transcript after sending;
  if it didn't arrive, wait for readiness and resend, finally mark "Needs you" (`node/firstprompt.go`). Sending alone isn't success.
- A new reason a first message did not go in (`lostFirst`, `node/firstprompt.go`) must reach every reader of the reasons, not only the list (#7897): #5464's
  `lostDialogHeld` (the node types the held message itself once the dialog is answered) was missed by `firstPromptHeldBack`, and a switch between Claude Code and Codex
  stopped keeping its handoff for the next message. `grep -n 'lostDialog\b'` finds them. Whether a message went in since is a decision: read the transcript as written now
  (`metaNow` in `promptedSince`), not the list's read of up to 3 s ago, or a handoff the held message carried goes in front of the next one again.
- Gray text in Claude's input box (predicted next message, new-chat `Try "…"`) looks identical to user input in plain-text screen reads: detect drafts from ANSI dim (SGR 2).
  The 2.1.312 trust-directory dialog has unnumbered options (`❯ No, exit`); recognize it by the bottom `Enter to confirm · Esc to cancel` (`server/internal/screen`).
- herdr's `agent_status` can lag behind Claude (#2069, herdr 0.9.3): after the previous turn ended (done, unread), an App message started a new turn, but herdr kept reporting done.
  The terminal status line spun, the title was "◐ …", and the transcript grew, yet the App showed completion. Five local attempts with the same steps (background workspace, send-text + Enter, long tool calls,
  background-task wakeups) didn't reproduce it. Rather than guess when herdr misses updates, node checks two fallback signals (`claudeStatus`): the title's spinner glyphs (in 2.1.3xx, working uses
  ◐◓◑◒, awaiting input uses ✳), and an unfinished transcript turn (`turn_duration` / interruption ends it; 913 of 955 local turns ended with `turn_duration`).
  Only correct done / idle / unknown, not blocked. If the transcript says the turn has ended, trust herdr (the title may have been read just before completion).
- Idle Claude sessions showed as working for hours after Claude Code compacted on its own while idle (#5967, 2.1.31x: "Compacted while idle, before the prompt cache expired"). Two things together:
  the node's quick tail read skips user lines over 8 KB that are not tool results and takes each for a long prompt, and the compaction summary is a 17 KB user line (`isCompactSummary`), so a turn "started" at the compaction and never ended;
  and "written within 45 seconds" was the file's mtime, which Claude Code's in-place rewrites of its trailing metadata (`last-prompt`, `ai-title`, `mode`, `permission-mode`, `atis-latch`; no timestamps, same size) kept moving with no turn at all.
  On one machine the session's `state_since` matched the transcript's mtime to the second while herdr said `done` and the title showed ✳. A shortcut that guesses what a skipped line is must leave out the lines it cannot be (summaries, meta lines), and a "recently written" check should read the content's time, not the file's.
  When reproducing, note that a new herdr pane's shell lacks the current session's `CLAUDE_CONFIG_DIR`; claude reports `Not logged in` (can't read login from the default directory).
- Claude Code `--settings` honors only the last occurrence, without merging (measured 2026-11-02: with `--settings A --settings B`, all of A's `env` disappeared). If a launcher alias supplies
  `--settings` with credentials in that file's `env`, appending another `--settings` in Persona overrides it. Phone-started sessions and every assistant message then return
  `Not logged in · Please run /login` (`isApiErrorMessage` + `authentication_failed` in the transcript), though typing the same alias in herdr works.
  Merging files only rescues aliases: a function's `--settings` isn't readable (#1411). Persona additions therefore use composable flags (`--plugin-dir`, `--mcp-config`),
  not `--settings`: hooks are a plugin (`docs/integrations/claude-integration.md` ("Hooks")); only older Claude Code versions still merge. Having users recognize Persona's `--settings` by its directory name
  inside their launcher function and yield to it is also wrong: changing the data directory from `~/.persona` to `~/.persona` breaks that detection.
- A launcher function can write its `--settings` to a temporary file for each start and remove it when claude exits (an `EXIT` trap; #9119). The probe's stub claude
  exits at once, so the path the probe reports is gone by the time a start reads it, and each probe names a new one. Every assistant message then failed with HTTP 502
  (`open …/settings-XXXXXXXX.json: no such file or directory`, a new name each time) because `assistantCompactionSettings` returned that error. Treat a launcher
  settings file that is gone as nothing to merge, as the other readers do; only one that is there but does not read stops a start. The launcher writes it again when it runs claude.
- Windows inline `--mcp-config` JSON can be corrupted before Claude starts (#3402). A native argv recorder launched directly by Go preserved doubled backslashes;
  Git Bash's builtin `printf '%s\n' "$@"` already saw single backslashes instead. The loss is in `bash.exe`/MSYS parsing the incoming native command line, not Bash
  forwarding arguments to a Windows child. The installed Claude was a native executable, not an npm shim. With the same `-p` stream-json flags and empty stdin,
  inline JSON exited 1 with an invalid MCP config; an absolute forward-slash file path exited 0 with the JSON bytes unchanged.
  Keep Windows CLI JSON in a unique 0600 file per process in `privateCacheDir` (Windows privacy relies on the user cache directory's ACL), retain it until exit,
  and remove it on preparation/start failure or exit. This includes guests; macOS/Linux keep inline JSON. Do not fix this by rewriting JSON backslashes or logging its contents, which may contain extension credentials.
- Fixing MCP argv does not prove a Windows assistant can receive its first prompt (#3577). The generated shell-form hook can still fail during
  `UserPromptSubmit` with an unmatched quote around Windows paths. Use Claude Code's native hook `command` plus `args` on Windows, with the existing
  `--persona-home` flag instead of a shell environment assignment. Keep every event, matcher and timeout unchanged: never disable hooks to pass a smoke test.
  A real Windows `SessionStart` native-argv probe returned its marker successfully; full acceptance must also deliver a user prompt and observe a reply.
- Plugin (`--plugin-dir`) hooks behave like those in `--settings` (measured 2026-11-04 on 2.1.318: SessionStart, UserPromptSubmit, Stop fire; PreToolUse `deny` and PermissionRequest `allow`
  work, in both interactive and `-p` modes), and load alongside the launcher's other `--plugin-dir` entries. Disabling hooks (`disableAllHooks`) still needs
  `--settings`: `--bare` also skips hooks, but accepts only `ANTHROPIC_API_KEY` / `apiKeyHelper`; launchers using login or `ANTHROPIC_AUTH_TOKEN` fail.
- Persona-managed launchers pass keys through `ANTHROPIC_API_KEY`. Interactive claude first seeing that key prompts "Detected a custom API key … Do you want to use this API key?",
  with No selected by default (measured 2026-11-03 on 2.1.309). Selecting No puts the key in `customApiKeyResponses.rejected`, and every session from that launcher reports not logged in. `-p` doesn't ask,
  so the launcher "Check" passes but creating a session from the phone blocks. `ANTHROPIC_AUTH_TOKEN` (sends `Authorization: Bearer`) and `apiKeyHelper` don't ask, but the official API doesn't accept the former,
  and the latter needs another `--settings` (only the last is used, above). `personad launch` now records the key as allowed before startup (`docs/integrations/launchers.md` ("Running Persona-managed launchers")).
  Acquire Claude's own config lock before writing (the `<config path>.lock` directory, stale after 10 seconds), or concurrent writes by a running claude lose one side's changes.
  Claude Code 2.1.317 includes a `storageV5` config backend (off by default); once it becomes default, global config may stop reading `.claude.json`. Recheck this behavior when upgrading Claude Code.
- If a launcher connected to a relay gateway returns 401, don't suspect the header first: the relay used here accepted both `Authorization: Bearer` and `x-api-key` from its first version (check its authentication middleware).
  Use the `POST /v1/messages` error to tell what Claude sent (tested with a fake key 2026-11-04): `invalid x-api-key` means only `x-api-key` was present, with a wrong key;
  `invalid or missing API key` means an `Authorization` header was present (or neither header); `API key has expired` means expiration. When both credentials are present, that relay requires exactly one to match.
- A managed launcher sets only `ANTHROPIC_API_KEY`, but the hub machine's `~/.claude/settings.json` has `ANTHROPIC_AUTH_TOKEN` in `env` (fallback for another gateway):
  Claude Code fills variables **absent** from the environment from settings `env`, so it sends both `Authorization: Bearer <that token>` and `x-api-key`, and the relay returns 401
  `invalid or missing API key` (2026-11-04, once misdiagnosed as "the gateway only accepts Bearer"). Existing environment variables, even empty ones, aren't overwritten by settings. `personad launch` now sets the other variable empty.
  Reproduce with a fake gateway logging headers, a `settings.json` with `env` under `CLAUDE_CONFIG_DIR=<temporary directory>`, and `claude -p`.
- User-started claude (launchers, profile directories) lacks Persona hooks; AskUserQuestion can only be answered through screen-reading cards. These must recognize 2.1.316 multiple selection: `[ ]` / `[✔]` are state, not text. Digits toggle but don't submit; move to Submit, press enter, then press 1 on the confirmation page. A digit on the free-answer row types text; a digit on Submit does nothing. Sending several keys together loses some; send one at a time (`docs/integrations/claude-integration.md` ("Screen cards for multiselect questions")).
- With preview-bearing AskUserQuestion options, digits only move the cursor; enter is needed to select. A digit-only screen card appears answered but has no effect. After keys, check the screen to confirm the question actually advanced (`docs/integrations/claude-integration.md` ("Questions with previews")).
- When a screen card is withdrawn because the Agent is no longer blocked, also mark it ended (cancelled) in the database, or history stays pending until hub restarts.
- Screen dialog recognition must first anchor the dialog's starting row (`screen.dialogTop`: the final separator below the conversation, or the row after a selection panel's `▔` edge), then find numbers below it. Searching the whole screen mistakes the Agent's "1. … 2. …" answer for options—
  a notice dismissible with Enter becomes a question, `dismiss_notice` refuses to dismiss it, and the phone's screen card lists answer text as buttons (#2872). This anchor, `DialogHead`, was already used; both readers now share it.
- AskUserQuestion: a PermissionRequest hook returning `decision.behavior: allow` + `updatedInput.answers` answers on the user's behalf (real pty on 2.1.319). Persona's PreToolUse lets the terminal dialog open and PermissionRequest waits for the phone; PostToolUse removes `answers` to find the call, then completes every waiter of its exact `tool_use_id` (including one that arrived before PermissionRequest). Don't complete by question text or an empty-id wildcard: a late receipt would cancel the next identical question. Keep the PreToolUse pairing by hook attempt: a resume must reuse its assignment, not consume a newer pending id, and overlapping first / resumed requests need the same binding. After a Node replacement, recover only an unambiguous recent transcript tool_use / tool_result; several possible calls hand that ambiguous generation to the terminal and withdraw its restored cards until each PostToolUse arrives, rather than guess or wait for an unmatchable Post. Standby replay must restore Pre / Permission / Post even when stale event reports are filtered, retained for the hook + reconnect lifetime, not the two-minute `hook.decided` dedup window. Don't route receipts through `decisions.decide`: after a phone answer they would be saved for and wrongly suppress that next question (#2382).
- ExitPlanMode: uses PermissionRequest, not PreToolUse. `allow` must carry `updatedInput` + `updatedPermissions:[{type:setMode,...}]`
  to work; plain `{behavior:allow}` still shows the TUI dialog. `deny` + `message` delivers feedback to the model as tool_result.
- A recorded post-plan mode must reach the interaction, not only the node's session state: the clients' legacy primary actions hard-code different modes. Put `plan_then` on the card, show that one mode, and normalize the answer in the manager as well as the hook so old clients cannot make history disagree with Claude's actual `setMode` (#3784). Terminal decisions use `answered_by: "terminal"`; a dismissal alone also covers other hand-backs and does not prove the source.
- A read-only `task_plan` task on Claude starts in `bypassPermissions` and carries its read-only constraint in the first prompt (#4864). Codex read-only tasks keep the plan-mode sandbox; TRAE CLI and omp start with no mode at all, so the hub must not send `plan`, a default mode or a tier's effort to them, or the node refuses the start (#7559); the work agents pi, cursor and agy (`proto.WorkAgentTypes`) get none either, but the node ignores rather than refuses one, so the hub drops a caller's instead of refusing it (#8151); a read-only task for one of them is refused when it is planned instead, since sending no mode would bypass the node's `plan` refusal and leave only the prompt line (#8316). Name the agents that take no modes (`proto.NoModeAgentTypes`, `proto.WorkAgentTypes`) rather than the ones that do: Paseo providers report the agent `paseo` and take `bypassPermissions` through their bridge (#8141), and an allow-list of Claude and Codex would drop it. If a read-only task is already in plan mode, resolve its `ExitPlanMode` request with a denial and instructions to return findings before registering a card; allowing the exit would lift that constraint. Look up the task's saved `readonly` value, including a reused topic to-do whose plan carries its binding instead of `Todo.PlanID` (#3985).
- After approving a plan in `herdr`, the `PermissionRequest` hook may not exit promptly, and `interaction.cancel` may not reach hub (2026-11-01: the transcript already had a successful `ExitPlanMode` tool_result, session working, phone still needs_you). On sessions / revs changes, hub reads bounded transcript pages for pending plans and ends them using the matching tool_use_id's tool_result. For old hooks without an id, accept only a unique ExitPlanMode call within 30 seconds before creation, with a result no earlier than card creation. Ambiguous matches, offline nodes, "working" alone, or model text never clear it. A 30-second timer covers lost events; per-card rate limiting and single-flight avoid duplicate disk reads.
  Cleanup only removes stale cards, broadcasts `interaction_resolved`, and records dismissed history; it doesn't fabricate phone approval, permission mode, or answers. A still-waiting hook receives only Dismiss, not allow. New plans, questions / tool permissions aren't affected by old plan results. Regressions: `assistant/fleet_cards_test.go` (missing tool_use_id, cross-page matching, local deny / interrupt, fake approval text, old/new card races, App push and persistence).
- Tool permissions have the same issue (#3563): answering the terminal permission dialog doesn't end PermissionRequest, leaving the phone card until timeout. Permission cards likewise end from the tool_use_id's tool_result, but require an exact id (don't guess for id-less cards); don't collect phone-answered (claimed) cards, to avoid rewriting resolved as dismissed.
- TRAE startup, identity and history are separate checks (#7559). A new task can work while SQLite-only history remains unreadable.
  Test both retained rollouts and paginated-only history; exclude existing thread IDs before a fresh start, and do not preallocate a Claude ID.
  An Orca workspace can merely contain a terminal that changed directory: derive the TRAE session cwd from its metadata and preserve the real container separately.
- Hooks in `--settings <file>` are read only at startup; matcher changes require restarting claude. The hook script itself is reread on every execution.
- `herdr agent wait` may return agent_not_found before herdr recognizes the Agent: poll `agent get` after startup.
- On computers without herdr's claude integration, `agent list` lacks session ids (fallback in `docs/integrations/claude-integration.md`).
- To map panes to sessions without herdr's claude integration, get claude's pid from `herdr pane process-info`, then read
  sessionId from `~/.claude/sessions/<pid>.json`. Don't guess from "newest transcript in the same directory" (six Agents in one repo mixed every tab name).
- Claude Code 2.1.30x `/fork` and backgrounded sessions run in Claude's daemon (`kind: "bg"` in `sessions/<pid>.json`), without panes or herdr hooks,
  and were always listed offline. The original pane's claude shows the agents view with `parkedJobId`, while herdr still reports "Working" using the original session id and the background session's terminal title (2026-11-04).
  Before mapping panes, read `sessions/<pid>.json`: a parked process isn't running its own session (`docs/integrations/claude-integration.md` ("Background sessions")); but when the pane has opened the job it moved to, the pane belongs to that job—don't list the job separately as read-only and lose its tab. When on the agents list, keep the pane in the original session's tab too (can open the terminal and select from the list, but can't receive messages), rather than listing it nowhere.
- Forking from a specific reply (#2137) can't rely on the CLI: `claude --resume <id> --fork-session --session-id <new id>` copies the whole chat; hidden `--resume-session-at <uuid>` truncates in `-p`, but not interactive mode (measured 2026-11-05, 2.1.309). node copies the transcript through that turn's end, replaces only `"sessionId":"<old id>"`, then runs `claude --resume <new id>` (`node/fork.go`).
- Agents herdr doesn't recognize (such as TRAE CLI): herdr reports only built-in recognized Agents, and `agent prompt` returns not found for other panes. node uses owner files (pid → session)
  plus `HERDR_PANE_ID` from the pid's environment (pid → pane) to identify them itself, and reads spinner title characters and screens for state (`docs/integrations/codex.md`, the TRAE CLI section).
- Claude Code's config directory isn't necessarily `~/.claude`: with a launcher's `CLAUDE_CONFIG_DIR` (multiple profiles), transcripts, `sessions/<pid>.json`, and `settings.json`
  all live there. Every 30 seconds, node scans running claude process environments (Linux `/proc`, one `ps -E` on macOS), searches all directories together,
  and records discovered directories in the database. Put rarely running profiles in `machines[].claude_config_dirs` in `hub.json` (`server/internal/node/claudedirs.go`).
  The old workaround symlinked `~/.claude/projects` and `sessions` to a profile; existing symlinks are fine (deduped by real path), but no more are needed.
  Symptoms of a missed directory: assistant resume says "Session ID … is already in use", transcript not found in the App, herdr Agents can't be mapped to sessions.
- Auto memory follows the config directory too: a conversation given its own profile gets its own `projects/<encoded cwd>/memory`, with nothing
  saying so. Whatever must be one memory across profiles needs `autoMemoryDirectory` in the start's `--settings` (ignored in checked-in project
  settings, known from Claude Code 2.1.74) and a merge of the copies made before it (#9440; `docs/assistant/assistant.md`, "One memory").
  Conversely, automation running claude with its own `CLAUDE_CONFIG_DIR` is discovered and remembered by hub, flooding lists with hundreds of offline sessions. Deleting `claude_dirs/<machine>` records doesn't help
  (the next scan reports it again); use "Mute configuration directory" (`docs/integrations/claude-integration.md`, #676).
- After node discovered all config directories (PR #12), a machine with over ten Agents and hundreds of MB of transcripts read disk continuously at about 20 MB/s: every herdr event rebuilt session lists,
  rereading 384 KB from each growing transcript's tail. List rebuilds are now at most once per second; growing-file meta is reread at most once per 3 seconds (`metaMinInterval`),
  background-task scanning streams (previously loaded the whole file initially), and large files' first reads run one by one in the background: list construction once waited for several GB of reads, leaving no sessions visible for minutes after node restart. Cold-opening a hundreds-of-MB transcript parses it all (317 MB takes about 20 seconds; hub 15/30-second timeouts returned 502),
  so node warms running sessions one by one after startup. Measure latency with real large sessions using App cold-open parameters (`items?turns=2`), reporting P50 / P99.
- Claude Code cleans transcripts older than 30 days by default (`cleanupPeriodDays`, at startup; on 2026-10-29 it deleted over 200 sessions on this machine):
  promptly redact and save sessions needed as parsing samples as fixtures (`server/internal/transcript/testdata/README.md`). Redaction must be allowlist-based;
  "keep short numbers unchanged" lets every IP segment through (encountered).
- Names for new herdr tabs (requested 2026-10-31): tabs added to existing spaces used to be hardcoded `persona`. The new-chat form now accepts "Tab name";
  if empty, the manager generates a short English name from the first message (`assistant/agents_start.go`, hub's `tabname.go` before 2026-11-04). Reasoning models spend hidden thinking tokens first: with `max_tokens` 20 the content is empty
  (`stop_reason: max_tokens`), while 200 produces a name. Don't set model token caps too tightly for small tasks.
- herdr can report long-finished Codex sessions as working (2026-10-30: a turn ended two days earlier showed "Working for 45 hours" for eight minutes without rollout changes; the App times from that turn's user message).
  node trusts rollout: if it ends in task_complete / turn_aborted and hasn't been written for 30 seconds, treat it as done (`codexTurnOver`).
- Conversely, don't leave unattachable Codex state unknown: the App treats unknown as idle (2026-11-02: a running Codex desktop App task appeared in "Idle spaces" on the phone; its app-server was stdio, and node knew only the open rollouts, reporting all unknown). Derive state from rollout (`codexSessions`); only 384 KB of tail is read, while a turn often writes several MB. Looking only for task_started makes long tasks return to unknown: no turn event in the tail and not having read the file start means still in a turn (`quickMeta.turnOpen`).
- One Claude session id can have files in two config directories (resumed under another profile: the old one stopped, the new one kept writing). Taking the first by directory order reads the old copy:
  running phone sessions stop two days back, sub-Agents report not found, and the same session appears twice (2026-10-31, 45 on one machine). Always take the most recently modified file when looking up an id (`newestFirst`).
- Don't map Codex panes from "newest rollout in the same directory" (same pitfall as Claude): terminal titles are "thread name | directory"; map by thread name in `session_index.jsonl`.
  Rollouts keep writing in the directory of the thread's start date; scanning only the last two weeks' date directories misses old threads (2026-10-31: a running Codex appeared as another session with a gray dot).
  Comparing content by id won't catch this; compare against actual pane truth (`scripts/diag/live_panes_check.py`).
- A Codex pane title that names no thread in `session_index.jsonl` is not evidence that the pane runs another thread: the index may simply lack the name.
  Discarding herdr's reported session id for it left the pane mapped to nothing, so a Persona-started Codex joined on its app-server was listed only as the `codex_app_server` row, with no pane or terminal (#7538).
  Drop the reported id only when the index knows that thread under a different name, and never under a name it gave the thread before (a rename leaves the earlier line in the index, and the node keeps only the latest); a remembered pane binding is still dropped.
  Check the name of the thread herdr reported, not of the main chat a sub-Agent id was replaced with: that name says nothing about the reported thread.
  Claude panes likewise: pane bindings for Persona-started sessions live in hub's database and reload after node restart, but can be stale; `sessions/<pid>.json` takes priority.
  Pane ids are reused too: herdr numbers its workspaces again after its server restarts, so a new tab can get the id (`wD:p1`) of a tab that closed days
  ago, and the binding of the session that ran there comes back on every hub connection. A tab Persona opens forgets every binding of its id first (#14682).
  After changing pane identification logic, rerun verification after deployment (restart); pre- and post-restart results may differ.
- herdr `agent.prompt` pastes, then waits about 300 ms before Enter: serialize sends to the same session, or the second screen read mistakes the first message for an input draft. Sending deduplication lives in hub Postgres (`sent_messages`):
  node's in-memory deduplication disappears on reconnect / hub restart, exactly when the App auto-retries. Full session-reading and sending verification, with per-hop timings: `docs/archive/integrations/session-verify.md`.
- Like `/effort`, `/model <alias>` writes to `~/.claude/settings.json` (`model` / `effortLevel`) as the default for future sessions, not just this one;
  cached chats first prompt "Switch model?" (changing model rereads all history). Don't test in your usual configuration; restore both keys afterward if you do.
- Claude Code 2.1.206 changed `/effort` to a slider ("←/→ to adjust · Enter to confirm", low at the left), removing the old list's "for this session only";
  node's `tuiSetEffort` recognizes both (`effortPicker`). Confirming the slider writes `effortLevel` in `~/.claude/settings.json` ("saved as your default for new sessions"),
  not just this session. `CLAUDE_CODE_EFFORT_LEVEL` in the environment (settings.json `env` or launcher) wins; Claude only replies "… overrides this session" and changes nothing, and the App reports that accurately.
- Claude Code 2.1.330 advertises the session-only confirmation key in the `/effort` footer (#13534). Read it after moving the slider.
  Savable levels offer `s`, while a session-only selection such as `max` uses Enter. The effort indicator above the input box
  can confirm the level when the command's result is outside the visible area.
- Effort `auto` (Claude Code 2.1.316) works only through `/effort auto`: `--effort auto` warns and is ignored, and settings.json `effortLevel: auto`
  is silently dropped (`max` too; settings accepts only low…xhigh). The picker has no auto position. `/effort auto` clears this model's default in settings;
  phone auto handling must differ by path (`docs/apple/ios.md` ("Effort and permission modes")). Testing `/effort …` on a dev machine also changes real settings.json; don't use your own configuration.
- **Settings defaults and session effort need different option lists** (#5946): Claude Code 2.1.318 accepts only `low`, `medium`, `high`, and `xhigh` in settings.json. Keep `max` in session controls, reject it in settings writes, and report an existing stored `max` as ignored with the model default in effect. Never reuse `proto.EffortLevels` for the settings whitelist. Web settings override node descriptions with their own translations, so a server warning also needs a localized web warning.
- **Catalog defaults need execution precedence too** (#8941). A launcher's `--settings` model can differ from the CLI catalog's recommendation, but its `effortLevel` does not beat `CLAUDE_CODE_EFFORT_LEVEL`, `--effort`, or a saved level in the same file's `modelSettings`. Native model matching covers aliases and provider IDs, so keep the native effort where that matching may apply. A probe's differential `Env` also omits inherited shell values: combine `Shell`, `Unset`, launcher changes and profile/explicit settings env before projecting a default. Preserve native aliases and leave unknown settings or unsupported levels unchanged; test both inherited variables and settings values that only session controls accept.
- **A custom launcher name does not identify its Agent** (#8940): assistant effort validation must resolve the executable behind the launcher before deciding whether Codex-only levels are valid. Validate cold and warm starts, explicit and configured effort, and a follow-up that inherits the saved launcher; checking only the literal `codex` command leaves aliases broken. Once dispatch has selected Claude or Codex, validate against that backend rather than the launcher's spelling.
- **An explicit `--permission-mode default` is not "unset"** (#3026): it overrides `permissions.defaultMode` in Claude Code's settings.json, so
  filling an unset mode with `default` (#2194) silently put every Agent the assistant started into Ask every step for someone whose Claude Code
  starts in Auto. Resolve "unset" to the mode that would really run (the computer's `permissions.defaultMode`), or pass no flag and show it as unset.
- ultracode is a second axis (#3427): in `2.1.203–2.1.283` it was a level to the right of max and locked effort; since `2.1.284`, it is independent `/effort ultracode on|off`,
  writes no default, and is off after resume. Persona supports only the latter, does not add ultracode to `EffortLevels`, send `--effort ultracode`, or add `--settings` for it.
  For unknown versions, confirm from result wording; command success does not mean enabled. If `enableWorkflows` is unset, the account plan determines the default; do not fabricate a fixed on / off default.
- ultracode `ultra_effort_enter` attachments use `reminderType: full|sparse`, generated only on the next real prompt; sparse is not guaranteed every turn either.
  Do not infer off from assistant rows or tail windows. In-process, use the newest evidence timestamp; discard old evidence across `startup` / `resume`. Session `StartedAt` survives resume, so is not a process boundary.
  `SessionStart` `clear` / `compact`, hook retries, and sidechains must not turn off the main session's switch again. The typing lock must distinguish sending paths already holding it from asynchronous startup, avoiding startup waiting for a message while the message waits for startup.
  The result wording, settings schema, and attachment keys above were checked only in static code of the installed `2.1.318` CLI; unit tests use synthetic fixtures, not real-session end-to-end acceptance.
- Claude Code eats the image paths it is sent (#32, Claude Code 2.1.316–2.1.318): its paste handler cuts each line at a space before a path; a line of only paths loses each image path
  (`.png` / `.jpg` / `.jpeg` / `.gif` / `.webp`, to a readable image file) to an `[Image #n]`, and blank lines go. With bare paths in `<attached_images>` the transcript recorded `[Image #n]text\n<attached_images>\n</attached_images>`:
  the model saw the picture but had no file to copy or resize. A line starting with a word (`path: <path>`) stays text, so the node adds one per path for Claude (`keepImagePaths`).
  The apps take only block lines starting with `/` or `~` as photos; reading `path:` lines as photos would show each picture twice next to the transcript's image item.
  The `path:` lines and the block's tags make a message with words more than the two line breaks Claude shows inline (2.1.316 folds a paste with more into `[Pasted text #n +k lines]`, or one over 800 characters),
  so its box shows `[Image #1][Pasted text #2 +3 lines]`: the stuck-prompt check (`ensureSubmitted`) takes that placeholder after image chips as the message (`pastePlaceholder`, `longPaste`).
- Startup submission needs the same placeholder recognition as ordinary sends (#4794, Claude Code 2.1.322):
  `stillPrompted` removes `[Image #n]` and `[Pasted text #n +k lines]`; a chip-only draft has no visible words left,
  so using it alone both skips recovery Enter and reports the owned draft as someone else's. Use `pendingPrompt`
  under the startup typing lock, pin the placeholder IDs before recovery (the lock cannot stop a person editing
  the terminal), and retry only Enter with a grace between attempts. Fresh sessions expect the text chip after
  their image chips; changed IDs or appended words are not the owned paste. Once recovery Enter was attempted,
  an empty box without a transcript receipt is ambiguous,
  not permission to paste again. A later follow-up's timestamp can show it was queued after work began; do not
  attribute the original submission to that follow-up without a terminal-key receipt.
- A prompt of pictures only has no words to compare (#10179, from the web): its box held `[Image #n]<attached_images>`
  and the photo's `path:` line, `pendingPrompt` saw nothing of its own, no recovery Enter went in, the window ended and
  the send answered 200, and the leftover refused every later send to the pane as a draft for 11 minutes. Its own
  `path:` line names the upload the send just saved under a fresh random name, so it identifies the prompt
  (`ownPictures`); a chip alone is still nothing to tell by. Claude Code before 2.1.316 drops the `path:` lines, so
  such a box there is not recovered.
- Never confirm a first image prompt from quick metadata alone (#4794): its head stops at 256 KB and tail at
  384 KB, so a multi-megabyte user row can be absent from both. A real disposable Agent's normal start recorded
  the same image prompt three times while confirmation believed it had been dropped. Capture a full incremental
  `promptWatch` before typing; attach lazily with a timestamp filter when the file appears late, and recheck it
  after readiness waits before re-pasting. The normal and swallowed-Enter real tests must both record exactly
  one image prompt.
- An image message once remained unsent in Claude's input box (2026-10-31, Claude Code 2.1.206: `[Image #1]` and text in the box, Agent `done`, Enter on the computer sent it).
  Forty-two later attempts in a separate herdr session (small PNG, 1.8 MB JPEG, saturated CPU) didn't reproduce it; cause unknown. On 11-01 a phone photo did it again. node fallback (`ensureSubmitted`): 2 seconds after sending,
  read the input box once; if it's still the just-sent text (strip `[Image #n]` / `[Pasted text #n]`, compare the first few words), press Enter once more. If empty or different, press nothing.
  Use only that post-wait read: the first version read immediately and gave up on a mismatch, which missed the 11-01 case (right after pasting the box may not yet contain this text).
- Codex app-server integration (2026-11-02): Unix sockets carry WebSocket, not JSON-lines; external stdio has no second attach port. Directory discovery doesn't resume; control goes only to the original loaded owner. Unknown / cancelled requests must not send error / decline, which could consume the desktop approval callback. `serverRequest/resolved` withdraws cards as dismissed without fabricating approval; deduplicate using generation and typed request ID together. Generic server errors after send admission aren't "definitely not sent" and must not auto-retry; old outbox operation ids don't expire after one day (`docs/integrations/codex-app-server.md`).
- An npm-installed Codex can be found by absolute path while its `/usr/bin/env node` launcher still fails under a node
  service (#11686). For stdio app-server calls, resolve the npm launcher symlink and add its installation's bin directory
  to the child PATH when Node.js is missing. Test subscription polling with a service PATH that omits the NVM directory;
  a successful interactive launch does not exercise that path.
- Session effort readback is not an edit capability (#6887): keep Codex rollout effort visible for terminal/history rows,
  and do not replace a running effort missing from a stale model list with its default. Long tool output can move the
  latest `turn_context` beyond the quick tail; include those small setting rows in the existing incremental scan.
  A current row unable to switch models can still open its effort controls, while genuinely unsupported changes need
  a visible reason. Connection tests must apply the draft effort on the wire, not just echo an ignored input.
- Codex app-server `thread/loaded/list` isn't all chats (2026-11-05, #2118, 0.166): TUI creates `ephemeral: true` threads for small background tasks in the shared managed daemon
  (`threadSource: "thread_title"`, title generation), without `path` / rollout; they stay loaded and `active` after finishing. Ignoring `ephemeral` produces a row of untitled "Working" Codex entries from the same directory and second on the Agents page.
  Skip `ephemeral` before listing sessions; they also don't support `includeTurns` (returns -32600).
  Remember the answer too (#12517): an app-server-compatible daemon kept 600 to 740 finished helper threads loaded, and reading every one on each 5 s refresh made about 100 reads a second
  and used up the refresh's 20 s before the real threads were read. Nor read a long list in its own order, or count a read the refresh's time cut short as read: either way the same threads are cut on every refresh.
- Use the exported `codex app-server` protocol (`codex app-server generate-ts --out <dir>`). Experimental APIs (`thread/backgroundTerminals/*`, `process/*`)
  require `--experimental` to export and `capabilities.experimentalApi: true` in `initialize` to use. Other Codex assistant pitfalls are in `assistant.md`.
  Codex assistant rollout begins with tens of KB of instructions (AGENTS.md, snapshot), with the first user message after 70 KB: scanning only the first 32 KB for a title fails (`readQuickMeta`).
- Codex image generation (`scripts/assets/make_persona.py generate`): `codex exec`'s built-in `image_gen` can create transparent PNGs using Codex's own ChatGPT login,
  but its HTTP client doesn't accept `socks5h://` proxies (chat requests do); use an HTTP CONNECT proxy. `-i` takes multiple values, prompts come from stdin; ChatGPT login supports only some models.
  If generation hangs (over ten minutes per image, all parallel work stopped), measure proxy tunnel throughput before blaming rate limits: on 2026-10-31 the hub machine's outbound bandwidth was only 11 KB/s; opening `ssh -D` on a faster internet machine produced four avatars in 6 minutes (`docs/apple/persona.md`).
- Orca host (`docs/integrations/orca.md` ("Orca source")): `terminal.send`'s `client` requires `id`; providing only `type` returns `invalid_argument` on every send
  (the first version did this; fake runtime didn't validate parameters, so all tests passed; `orcarttest` now validates Orca's schema). If Orca can't recognize the foreground Agent, it types the "prompt" straight into
  the shell and presses Enter; `accepted:false` exists only with `requireAgentStatus`. Before sending, verify the Agent is foreground using the process table + `terminal.agentStatus`;
  Orca's `agentPrompt` pastes only for Agents it has seen working; newly started claude receives raw text, and multiline input submits at the first newline. Persona sends bracketed paste itself, then Enter separately;
  `worktree.ps` row `state` includes sub-Agents (working when the main turn has ended but background sub-Agents run); pane-local state is `mainAgent.state`;
  `agentIdentity` may remain after the Agent exits; decide "is an Agent" from claude / codex processes matching `ORCA_PANE_KEY`. node tests point `ORCA_USER_DATA_PATH`
  to an empty directory; never touch the live Orca (its socket fully controls Orca).
  New Orca terminals (#924): omit `presentation: "background"` in `terminal.create` to avoid an orphan PTY; after creation, check identity and generation via `terminal.list` using the exact handle; ordinary creation explicitly `cd`s to the target directory. Permission to create sessions isn't permission to resume ones still open elsewhere: since #5548 Orca resumes an exited Claude session like herdr, after `openElsewhereRefusal` (a live claude on the session, including a suspended one), and since #17044 Codex, TRAE CLI and pi sessions too, each kept from a second writer by its own check (a Codex holding the rollout, a TRAE CLI a pane still runs, the pi runtime); `orca_resume` refuses only omp. A stopped Agent leaves its shell in its last pane (`docs/integrations/engines.md`, "Agents run in a terminal"), so a resume must not take an open last pane for a suspended agent: only a message still checks it (`notRunningRefusal`), a resume checks the engine (`resumeEngineRefusal`). A bare Resume (the web sends only the id) of a TRAE CLI session needs its launcher from the node's record, or it starts Claude Code on that id (`traexResumeWith`). Orca's `terminal create --agent --resume` is a CLI of some builds only and drops the session's launcher.
  An Orca pane key is not a terminal's identity (#5547): a terminal made in a worktree Orca's window is not showing is not adopted by the UI, and about 2 s after `terminal.create` it turns `orphaned` with `tabId` / `leafId` rewritten to `pty:<worktreeId>@@…`, the same handle and incarnation. Starts from a directory in no worktree went to the first worktree listed, so the node lost the pane it had just typed into: `Run` said "replaced", the readiness wait said "is the machine busy?" for 3 minutes while claude had started in 2 s, and the empty tab was never closed. Fall back to the active worktree, and find a terminal the host made by handle + incarnation under the key creation gave (`orcaHost.made`); the opposite case, a new handle under the same key (#972), stays refused. `orcarttest.Fake.Orphan` reproduces it.
  Claude with background tasks (#5463): Orca's `terminal.agentStatus` says `isRunningAgent: false` while Claude Code has background Bash tasks, though its input box takes text; requiring it refused every send to such a session with nothing logged. The process scan is the foreground gate. A `tmux new-session -d` an agent runs inherits the pane's `ORCA_PANE_KEY`, and each process in it is "in front" of tmux's own pty: a `claude -p` worker with a lower pid took the pane and its manager dropped to interrupted. Without Orca's check, "in front" must mean in front of the pane's own pty: a candidate whose pty owner (first ancestor on another terminal) carries the same pane key is nested (tmux, zellij, script…) and not the pane's. Not by `TMUX=`: Orca's agent teams set it in a real pane. A refusal and a pane changing sessions are logged since.
  A pane variable is not ownership (2026-11-08): a claude whose environment carried `ORCA_PANE_KEY` was skipped as "its engine lists it" even on a node that did not enable Orca, or when the macOS scan missed it, so every running claude in Orca's terminals showed as offline. The outside-claude list now defers to an engine only when an engine in use lists that pane with a claude in it.
  An agent Orca finds installed is not a chat it can open (#17684): on a fresh Orca 1.4.277 `preflight.installedAgents` named Claude Code and Codex, yet `agent.launch` refused both chats (`agent_launch_surface_unavailable`, "its chat runtime isn't installed on this host"), while `agentSession.create` took the chat and it failed at its first turn ("Claude wasn't found on the computer running this chat"). Orca resolves a chat's program apart from the installed-agent scan (its own runtime under its data folder, or the CLI where it launches chats), and reports the result only on streaming methods the local socket refuses (`agentSession.subscribeAvailability`); `agentSession.agents`' `managedRuntime` reads the same before and after the install. Learn it from the launch refusal and stop offering that chat; never retry through `agentSession.create`. An isolated runtime (empty HOME) reproduces it, and `managedRuntime.install {agent: "claude"}` there (a 400 MB download) makes the launch succeed.
- Launchers (`docs/integrations/launchers.md`, 2026-11-01): discovery once only read `_claude_launchers` helper output; a machine with all aliases in `~/.zshrc` (41 for one user)
  listed only `claude`. Let zsh tokenize aliases (`${(@Q)${(z)value}}`), not Go quote parsing. Aliases defined by a script in `zsh -c` don't expand within that
  script (it is parsed all at once); tests must use `zsh -ic` like node and write definitions to temporary `$ZDOTDIR/.zshrc`. With `alias claude=…` in `~/.zshrc`,
  a probe definition like `claude() { … }` expands the alias first, breaking the whole script. Define stubs as `function claude { … }`.
- Stock-Orca Codex defaults must distinguish a configured no-op alias from plain PATH Codex even though both have `["codex"]` argv. Keep alias provenance beside the launcher argv cache; removing the plain entry breaks bundled and alias discovery (#1984).
- Tests running launchers or probes use a minimal environment (`HOME`, `ZDOTDIR`, `PATH`); never print all output in failure messages: probe output contains the complete process environment, and dev machines
  contain real keys.
- Anything run through `zsh -ic` / `bash -ic` shares stdout with the rc files (#6310): Ubuntu's `/etc/bash.bashrc` prints a two-line sudo hint until the user has run sudo once,
  other rc files print greetings, and an `EXIT` trap prints after the script. The launcher scan read that as helper output ("To", "See" listed as launchers) and lost the last PATH entry.
  The script now frames its output with begin/end markers and node parses only between them (`launcherOutput`); a test's fake rc file should print noise before and after the definitions.

- Codex has two kinds of questions (source read + measurements on 0.153.4 / 0.163.0, 2026-11-02): in Default mode, current models receive `request_user_input_async`; the call immediately returns
  `{"accepted":true}`, the turn ends normally, and the answer later arrives as a user message carrying `<send_user_message_question_reply>`. "Call without output" can't recognize this;
  inspect the AgentMessage with `delivery: "async"` in `item_completed`. The 0.163 TUI clears the question panel as soon as the turn ends, leaving only text. Only Plan mode uses blocking
  `request_user_input` (overlay, output after answering). Pressing the number for "None of the above" submits that phrase as the answer; to write your own, move the cursor there, then Tab.
- herdr with Codex: an unanswered async question reports blocked and `agent prompt` returns `agent_blocked`; `agent prompt` appends text to the input draft and sends both
  (Codex sends previously didn't check drafts). In scrolled-up panes, `pane read --source visible` reads the scrolled content; `--source recent` reads the output tail.
  Scrolling doesn't affect keypresses. The 0.153 Ctrl+T pager covers subsequent approvals (`esc` in the pager edits the last message; close with `q`); 0.163 Ctrl+T only expands details.
  Headless herdr splits don't narrow panes; test small windows with `stty cols <n>` before starting Codex. `pane.send_text` forwards unchanged; bracketed paste (`\x1b[200~…\x1b[201~`) works in Codex.
- Trusting a directory while testing Codex locally writes `~/.codex/config.toml` (`codex exec` does too). `-c projects.…trust_level` doesn't suppress the trust dialog; remove your added entry after testing.
- Codex 0.167 asks to trust a folder only when it is a git repository (a plain `mkdir` never shows the dialog), and draws the dialog from the top of the pane. herdr's
  `pane read --source recent --lines N` counts the blank rows under it, so the last 40 rows of a 51-row pane were all blank and the start never saw the dialog (#2351);
  `--source visible` leaves those rows out. Headless herdr panes are 40 rows, which hides this: a live test that needs a tall pane keeps a herdr client attached in a pty
  of that size (unset the `HERDR_*` variables, or it refuses to nest), and the size is lost once the client leaves.
- Codex 0.167 creates a rollout empty and writes its `session_meta` line 8–19 ms later. Anything that caches what a rollout's first line says must not cache a read made before that line ends in a newline.
- The 0.163 app-server control socket (daemon `<CODEX_HOME>/app-server-control/app-server-control.sock`, likewise `--listen unix://<path>`)
  is a symlink; the actual socket is at `<canonical /tmp>/codex-daemon-<uid>/<SHA-256 of the alias's canonical path>`. Identifying it by "Lstat says socket, not symlink"
  makes the daemon impossible to discover or connect to (as before 2026-11-02); follow only the target Codex itself computes (`codexSocketPath`). 0.153 uses a normal socket file.
  If the daemon dies, attached TUIs show "Reconnecting to server…" and reconnect and reload the thread when a new daemon starts. npm-installed `codex` is a node wrapper script
  that starts a native child: stopping app-server in tests must kill the whole process group.
- A `request_user_input_async` `function_call` doesn't mean a question was asked: if parameter parsing fails (model writes `question` instead of `title`), output is an error and the model retries immediately.
  Only accepted calls write an AgentMessage with `delivery: "async"`. #214 created cards from `function_call`, leaving a permanently unanswerable card; it appeared only in the first of three measured turns (2026-11-02).
- When finding Codex rollouts by directory, `session_meta`'s `cwd` can be symlink-resolved (Codex uses the shell working directory) or the original `-C` value;
  accept both. macOS temporary `/var/…` is really `/private/var/…`: accepting only resolved paths passed every Linux test but timed out after 60 seconds on Mac (phone-created Codex, 2026-11-02).
- Canonicalize a path that may not exist yet with `realPath`, not `filepath.EvalSymlinks` alone (2026-11-10, #2695). `EvalSymlinks` fails on a missing path, and keeping the
  written spelling then gives one place two spellings over time: the node fixed Codex's `LegacyHome` before `~/.codex` existed, resolved `Homes` once it did, and under a linked
  ancestor every session of the default home got a `c0de-…` ID. `realPath` resolves the longest existing ancestor. A test builds the home through an explicit link so Linux CI catches it.
- When typing a command into a herdr pane to start Codex, don't type expanded alias words: aliases may contain keys, exposing them on screen and in shell history. Write launcher words and the first message together to a 0600 file;
  type only `personad codex-start <file>` in the pane (`codex_start.go`).
- **Claude sessions open in Claude desktop (or another terminal)** (2026-11-01, Mac without herdr): Claude desktop runs a real `claude` process and writes
  `~/.claude/projects` transcripts and `sessions/<pid>.json` (`entrypoint: "claude-desktop"`). Without a terminal host, Persona couldn't see it running, listed it from transcripts as
  "Not running", and phone sends attempted `claude --resume` in herdr. With herdr installed, this starts a second process writing the same session; without it, the error was
  `herdr is not running on this machine: herdr workspace list: exec: "herdr"…`. Before resuming, now check whether a live claude's `sessions/<pid>.json` has this session open;
  if so, reject (`open_elsewhere`, explaining where). Engine startup failures also get human-readable refusals (`engine_missing`). Since 2026-11-07 such sessions list as running (`claude_desktop` / `claude_cli`) and take messages through Claude Code's peer inbox (`docs/integrations/claude-integration.md` ("Claude outside Persona's terminals"), pitfalls in `docs/lessons/windows.md` ("Claude Code's peer inbox")).
- Sub-Agents in Codex and TRAE CLI write their own rollouts alongside the main chat, with the same directory and cwd, and newer updates. TRAE CLI's pre-tool automatic approval evaluation (guardian)
  is written by the same process, with its own owner file. Identifying sessions by "newest rollout / newest owner file" makes the App show the review chat, unlike the terminal (2026-11-01).
  Always inspect `thread_source` / `source.subagent` in `session_meta`; replace sub-Agent `session_id` with its main chat (`docs/integrations/codex.md`).
- **A core chooses its own shell and layout destination** (#2532): use `terminal.run`'s returned argv for command syntax, sourcing and startup
  probes; the node's login shell can be different. A missing foreground is unknown, not permission to type. Resolve automatic placement by
  folder inside every layout CAS retry, updating terminal labels if the destination changes. Keep explicit space selection distinct so a
  missing or unrecognized document cannot silently redirect it to a directory group.
- **Classic herdr and Herdr App are separate, non-interoperable runtimes that can coexist on one computer** (2026-11-03): when Herdr App connects remotely from another computer,
  the local process is `herdr-core serve` (socket `$XDG_RUNTIME_DIR/herdr-core/<session>.sock`, state `~/.local/state/herdr-core/`),
  alongside `herdr remote-client-bridge` and `peers.json`; terminal tabs live in `doc.get("herdr-app")`. It and terminal `herdr server`
  (`~/.config/herdr/herdr.sock`) can't see each other. If "sessions opened locally through App remote aren't found", check herdr-core `terminal.list` first, not only the classic socket.
  When using both on a computer, select engine `herdr-both`: node refreshes both hosts in parallel, isolates events per host (`watcherSignals`), and always creates / resumes sessions through classic herdr
  (`docs/integrations/engines.md` ("Host sets"), `docs/integrations/herdr-core.md` ("Combined engine")). Don't return to "refresh asks every host sequentially": a 2-second herdr-core delay slows herdr too.

## Starting a headless conversation clean without changing its id (2026-11-14, #7315, Claude Code 2.1.322)

Measured in `-p --input-format stream-json` against a local mock of the model API (`TestAssistantRoundLive`, build tag `roundlive`):

- **`/clear` changes the session id.** Sent as a message, it answers `conversation_reset`, then a new `system/init` with a new `session_id`, and every later turn goes to `<new id>.jsonl`. The node keeps only the version and the tools of `init`, so it would go on resuming the old id with the full history while the new turns never reached the conversation's row. Following a clear would take an id indirection through the node's state, the transcript lookup, `--resume`, hooks and usage.
- **`--session-id <an existing id>` is refused** ("Session ID … is already in use"), and `--resume --fork-session` makes a new id by definition (its help text).
- **`--resume <id> --resume-session-at <uuid>` keeps the id and the file**: the request holds the messages up to that line and the new message, the turn is appended to the same file as a branch from that line, and a later plain `--resume` goes on from the latest branch. It is a print-mode flag (ignored interactively) and undocumented, so it is gated on the version it was measured on, the anchor is checked first, and a refused start is retried without it.
- **It reaches only ancestors of the latest leaf**: any other uuid ends the process at once ("No message found with message.uuid of …"). A compaction starts a new chain (`compact_boundary` with a null parent), so the first turn is no ancestor any more: a conversation whose rounds start at the first turn must never be compacted, and one compacted before has to be replaced.
- **Anchor at the end of the first turn, not its first message**: anchored at the user line, the request has the start message and the round's next to each other with no answer between them, one prompt to the model, which then also reads the start message (a role notice that needs only an acknowledgement and no tools) as part of the round. The round also becomes the latest chain, so the first turn's end is no ancestor any more.
- **`--tools` without `ToolSearch` adds a `DeferredToolPlaceholder`** tool to the request, whatever the conversation started with (measured after a start on the default tools and after one on `--tools Bash,Read,Write,Skill`); with `ToolSearch` listed and nothing deferred the request carries only the built-ins named. Changing the tools on a resume also adds a user message listing the tools no longer available.
- **A refused anchor looks like output**: the process prints one `result` line on stdout (`error_during_execution`, `is_error`, `num_turns: 0`, the reason in `errors`) with no `system/init` before it, says it on stderr too and exits 1. A reader that takes any stdout line for a started process misses the refusal and reports the message delivered while no process read it, so the node counts a round's process started only at its `init` and takes a result before that for no start and no turn (`readAssistant`, `TestAssistantSendSuperviseRoundStartFailureRetriesPlain` with that line).
- **A process started with fewer tools keeps them until something restarts it**: the tools are fixed when the process starts (`--tools`, `--mcp-config`, `PERSONA_TOOL_SET`), so a switch that stops asking for the narrow set changes nothing in a process already running, and every later turn there goes on without the rest. The node therefore compares, on every send, the tool set the message needs with the one the process was started with, and replaces an idle process that differs (a plain `--resume`, no round state). A busy one must neither be cut nor take the message: a model switch lets the busy process take the message and switches at the next idle one (#1554), but here that message, and every turn after it until a later message, would still run without the rest of the tools. Holding the message at the node until the process is idle looked like the fix, but a second queue in front of Claude Code's own lost messages (a send answered as delivered whose later delivery failed, or whose connection ended), reordered them, and needed a trigger of its own for every way work can end. The node instead reports a round's process as working until it settled, so the manager's persisted relay queue keeps supervision messages back. A message that needs every tool and comes anyway (the owner's, a relay racing the row) while the process runs a turn waits for that turn inside its send, up to 30 s (the node's RPC carries no deadline; the hub and the manager give a message 90 s), then goes into a plain resume with every tool, and a Stop meanwhile ends the round at once. After that Stop the message still waits until the process exited: one that holds on past the wait's bound, or prints its turn's result on its way out, otherwise looked like a working process to join, and the message written into its closed input was lost. That bounded wait is not the queue above: the send holds the conversation's locks and answers only once the message went in, so nothing is reported delivered early and later messages wait behind it in order. Past 30 s, or with only background tasks running (they may run for hours), the message joins the process once on the round's tools and has it let go once it settled (`TestRoundProcessWorkingKeepsSupervisionQueued`, `TestAMessageWaitsForTheRoundsTurnAndGetsEveryTool`, `TestStopWhileAMessageWaitsForTheRound`, `TestAMessageWaitsForAStoppedRoundToExit`, `TestAMessageThatJoinsARoundProcessRetiresIt`). The first version only acted when a round was asked for, so turning `assistant.background_supervisor_keeps_context` on left the narrowed, never-compacted process in place.
- **Not busy is not idle**: before replacing a headless claude for a reason of the node's own, check everything a new process would lose, not just the turn flag and background tasks. The user's `/compact` runs with no busy flag (`assistantCompact`), and a turn's `result` clears busy while a message Claude Code queued behind that turn (`command_lifecycle` `queued`) has not started yet: a restart in that gap ends the compaction unfinished or drops a message Claude Code already accepted, which only the old process's input list knew about. Silence is not idle either: Claude Code says `queued` within a millisecond of a write, so remember a message before writing it (a lifecycle line for an id the node does not know yet is dropped), and count it until a lifecycle line says what became of it; and while the line reader handles a slow line (a `result`'s transcript refresh), the lines behind it, a turn's `init` among them, wait unread, so measure the quiet from when the reader is back at the pipe. Whatever cancels and rewrites a process's input (the queue edit) holds the same lock as whatever lets the process go (`assistantLife`), or the process is let go between the cancel and the rewrite. The round's checks share one definition (`workingLocked`).
- **A turn starts before anything streams**: in `-p` stream-json, Claude Code says `command_lifecycle` `started` 3 ms after the previous turn's `result`, prints a `system/init` as every turn begins, and its first `message_start` comes only once the model answers, seconds later. A turn it starts by itself for a finished background task's notification has no lifecycle line at all: its `init` comes 7 ms after a `result`, or 26 ms after `background_tasks_changed` emptied the list. A node that counts a turn only from `message_start` sees an idle process for seconds while a turn runs, and a restart then cuts it. So `started` and every `init` after the first mark the process busy. Since nothing announces Claude Code's own turn before its `init`, the three stops a supervision round brings about wait until the process is not working and printed nothing for a second (`assistantSettle`), then take the last look and stop it in one step under the lock the line reader marks turns with (`stopSettled`): a fresh round stopping the conversation's process, a message that needs every tool replacing a round's process, and a retired round's process being let go (`roundSettled`); a round's process also reports itself working until then. No other node action waits for the settle: the idle reaper and idle compaction keep their own checks after minutes without activity, and a restart for a model, mode or instructions change still goes by the busy flag and background tasks (measured on 2.1.322 against a local mock of the model API; `TestAStartedTurnIsNotCut`, `TestLaterInitIsATurn`).
- **One field of another type fails the whole stream line** (2026-11-25, #14351): `json.Unmarshal` returns an `UnmarshalTypeError` for a field it cannot convert (a gateway's token count sent as a string) but still decodes the rest; `readAssistant` used to drop such a line, and a dropped `result` left the turn working, so a `result` with a mismatched field is now applied (`TestAssistantResultWithAnUnreadableFieldStillEndsTheTurn`).
- **Resume cost grows with the file**: the first request came 0.24 s after the process started with a 0.4 MB transcript, 0.47 s at 32 MiB and about 0.8 s at 128 MiB (synthetic lines of about 4 KiB; real transcripts that large were not measured and may parse slower; the node's anchor scan 0.09 s and 0.35 s). The file only grows (a compaction appends to it too) and every round now restarts the process, which reads all of it, so the background supervisor is replaced past 32 MiB (`backgroundTranscriptLimit`).

## `ANTHROPIC_BASE_URL` is the gateway root: Claude Code adds `/v1/messages` (2026-11-12, #4886)

- **A tolerant probe hides a wrong address**: measured with Claude Code 2.1.322 against a fake gateway, `ANTHROPIC_BASE_URL=http://host/v1` sends `POST /v1/v1/messages?beta=true` (`/prefix/v1` → `/prefix/v1/v1/messages`). The manager's connection test and its own direct calls accepted a base with or without `/v1`, so a provider pasted as `https://gw/v1` passed Test connection and then failed every Agent with 404. Whatever turns an address into a request must be the same function on the test path and the launch path; here it is `proto.MessagesBaseURL`.
- **A normalizer applied twice must be idempotent**: the hub stores its result and the node applies it again at launch. Dropping only one `/v1` made `https://gw/v1/v1` test as `/v1/v1/messages` and launch as `/v1/messages`; the function drops a whole run.
- **Not every base is an `ANTHROPIC_BASE_URL`**: Responses / Chat Completions bases and Codex's `OPENAI_BASE_URL` include `/v1` by convention (`https://api.openai.com/v1`), and the web sends `api: "messages"` for every launcher, Codex ones included. What is stored, delivered to nodes and launched is decided by the agent and protocol together (`ManagedLauncher.LaunchBaseURL`), never by `api` alone; only the connection test, which sends a Messages request whenever `api` says so, goes by the protocol.

## Behind a gateway, read what Claude Code sends, not what its docs or other clients say (2026-11-16, #8798)

- **Measure the request, on the version you ship against**: a local fake gateway that logs each body (`model`,
  `max_tokens`, `thinking`, `output_config`, `safeguards`, `anthropic-beta`), with `claude -p`, `HOME` and
  `CLAUDE_CONFIG_DIR` in a temporary directory, a fake key and `env -i`, so neither the user's settings nor the shell
  of the session running the test leaks in. On 2.1.325 this showed what no page says: an unknown model is sent
  `output_config.effort: high` with no `--effort`, and auto mode behind a gateway with nonessential traffic off refuses
  every reviewed action when no review comes back, where the permission-modes page describes a fall-back to the local
  classifier. Another open-source client's notes were right about the variables but describe a different launch
  (`CLAUDE_CODE_USE_GATEWAY`), where the 2.1.325 code gives an unknown model no thinking level by default.
- **A working variable is not a supported one**: `CLAUDE_CODE_MODEL_CAPABILITIES` does what it should behind
  `ANTHROPIC_BASE_URL`, but it is missing from the official reference, while the documented
  `ANTHROPIC_DEFAULT_*_MODEL_SUPPORTED_CAPABILITIES` are ignored there. Writing an undocumented variable into every
  launch ties every provider to its current format; record what it does and leave it to a provider's own `env`.
- **A per-process value must hold for every model of the process**: `CLAUDE_CODE_MAX_OUTPUT_TOKENS` serves the
  session, its tiers, the small model and sub-Agents at once. Taking the session model's limit alone would ask a
  smaller tier for more than it writes, so the launch takes the least reported limit and sets nothing when one of them
  reports none (`outputTokens`). And the session's model is the start's `--model`, not the provider's default the
  node's env and the settings file are written for: a value pinned in the settings file would beat the one worked out
  for the start, so `personad launch` works it out again and the settings file leaves it out (`sessionOutput`,
  `docs/integrations/claude-integration.md`, "What a gateway reports about a model").
- **What Persona puts in the environment reaches the commands Claude runs** (#8816): Claude Code passes its environment
  to Bash, so a `personad launch` inside a session inherits every value the outer launch set. A rule "clear it only
  when this launch sets it" then hands the outer session's reply limit to a provider that reports none. Clearing it
  always would also drop a value the user exported; mark the value Persona writes (`PERSONA_MAX_OUTPUT_TOKENS`) and
  clear an inherited one only while it still equals the mark.
- **A larger reply is also a smaller room for input**: a gateway that counts input plus `max_tokens` against the window
  refuses long before Claude Code compacts once the reply asked for passes the 20,000 it reserves. Claude Code recovers
  only from Anthropic's wording of the refusal; an OpenAI-compatible wording ends the turn and, behind a gateway,
  triggers a compaction. Bound the reply by the window (an eighth, `outputLimit`), not only by what the model writes.
- **Check a doc's side effect in the code before acting on it**: the reference says raising the reply limit "reduces
  the effective context window available before auto-compaction"; 2.1.325 reserves `min(max output, 20000)`, so going
  from 32,000 to 128,000 reserves exactly as much as before.

## Claude Code plugins from a folder marketplace (2026-11-16, 2.1.291 and 2.1.308)

Measured with `claude plugin … --json` in throwaway `CLAUDE_CONFIG_DIR`s while building the node's plugin keeper
(`server/internal/claudeplugin`, `docs/operations/link.md` "Claude Code plugin"):

- Every CLI run, even `claude plugin list`, writes the config directory's `.claude.json` (and a `backups/` copy). With
  `CLAUDE_CONFIG_DIR` set to `~/.claude` that file lands inside `~/.claude` instead of the user's `~/.claude.json`: a second global
  config. Run commands for the user's own directory with `CLAUDE_CONFIG_DIR` unset; name any other directory. Tests decide "the user's
  own" from the process's real home, never a home a test passed in, so a test cannot reach the real `~/.claude`.
- `claude plugin install` on a plugin that is installed but turned off turns it back on. Never call it to "make sure": read
  `plugin list --json` first and leave a disabled plugin alone (`update` keeps it disabled, but updating a plugin the user turned off is
  not ours to do either).
- A marketplace that is a folder, with the plugin at a relative `source`, is read from that folder: `list --json` shows `readFromFolder`
  and `folderVersion` beside the recorded `version`. Writing new files there reaches new sessions at once; `claude plugin update` moves
  the recorded version (`updateOutcome: updated | up_to_date`). The folder must stay where it was added: `list` keeps showing the plugin
  after the folder is gone, but nothing loads.
- `marketplace add` of another folder under a name already added re-points that marketplace silently ("now points at …"), and adding the
  same folder again is a no-op. Failures print `{"outcome":"failed","failureCode":…}` on stdout and exit 1:
  `already_in_goal_state` (enable / disable), `not_installed` (uninstall), `not_configured` (marketplace remove), `not_found`.
- Uninstalling leaves `"enabledPlugins": {}` and `"extraKnownMarketplaces": {}` in `settings.json`; every other key stays as it was.
- Each command takes about 1.5 s on Windows. The desktop app (the Store build, Claude-3p) ships its own, newer CLI under
  `%LOCALAPPDATA%\Claude-3p\claude-code\<version>\<build>\claude.exe` (2.1.308 there while `PATH` had 2.1.291): a computer with only the
  desktop app still has a CLI to install with.
- Its Code sessions use `~/.claude` (the node already starts desktop sessions without `CLAUDE_CONFIG_DIR`, `node/claudedesktop_new.go`), and a
  user-scope plugin installed from the CLI loads there; inside the app `/plugin` is not available, only **+ › Plugins**.

## A plugin update reaches only sessions that start again; the inbox can lose a message silently (2026-11-22)

- `claude plugin update` answers "Restart to apply changes": a running session keeps the plugin code it loaded, and an
  SDK session (the desktop app's) refuses `/reload-plugins` even from the plugin's own `$.command.run` ("isn't
  available over a remote connection"). A plugin feature the node relies on (busy delivery, `busy_input`) therefore
  arrives per session, as each is opened again: the node reads the capabilities each plugin advertises on its poll,
  never the installed version, and the app tells the owner to reopen a session whose plugin is older
  (`relay_reason: "plugin_outdated"`).
- Claude Code's peer inbox gives its sender no answer unless the sender has an inbox of its own: a held message leaves
  a "Held peer message" notice in the transcript, but a dropped or refused one leaves nothing (seen on 2.1.330: twelve
  messages in a row taken during one long turn, the thirteenth never written anywhere). Never treat an inbox write as a
  delivery, and prefer the plugin's `$.prompt.submit({ asUser: true })` for the owner's words whenever the session's
  plugin can take them, busy or not (`docs/integrations/claude-integration.md` "Owner input through the Persona plugin").
- Run `claude plugin validate` and `claude plugin test` on the exact conflict-resolved head, not only the pre-merge
  branch: one lost `}` kept every hook from loading. A timer that releases a busy poll is only a scheduling boundary,
  never `submitted: true`; let the real submission continuation ack and cover delayed rejection and `drop`, idle and
  busy (#11980).

## Claude Code writes into its transcript as it exits: delete it after the process (2026-11-11, #4665)

Deleting a meeting room stopped its `claude -p` and removed the transcript at once; a moment later the file was back
with three lines Claude Code appends on exit (`last-prompt` with the last prompt's text, `atis-latch`, `cost-state`).
Wait for the process to exit (`stopAndWait`) before removing a conversation's files, and remove its
`<config dir>/session-env/<session id>` folder with them.

## A new private profile and `--setting-sources project` both drop the login (2026-11-11, #3862)

A meeting room (an assistant guest started with `--setting-sources project` so none of the owner's setup reaches it)
answered every question "Not logged in · Please run /login" on a machine whose launcher is a shell function and whose
gateway and token sit in `~/.claude/settings.json`'s `env`. Two things dropped that login: since #4310 a new assistant
conversation that names no `config_dir` runs in `<PERSONA_HOME>/profiles/<launcher>`, a fresh config directory with no
credentials and no `settings.json`; and `--setting-sources project` leaves the user's settings file out even in the right
profile. A conversation that must sign in as the assistant does asks for `config_dir: "legacy"` and, when it limits the
setting sources, carries the login keys (`env`, `apiKeyHelper`, the AWS and `forceLogin…` keys, `model`) of the profile's
`settings.json` in its own `--settings`. The synthetic reply is an ordinary assistant text item (`isApiErrorMessage`,
model `<synthetic>`, error `authentication_failed` only in the transcript); read the session row's `turn_error` before
taking the turn's last words as an answer.

## A setting in `~/.claude/settings.json` does not reach a private profile: put it in the launcher's environment (2026-11-14, #7319)

`CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=1` in the user's `~/.claude/settings.json` `env` kept Claude Code from switching
refused replies to a fallback model the gateway does not serve, but only in the default profile: on one install over 7
days, 71 such stops there ended without a switch, while all 4 switches in Persona conversations ended in 404. Those run
in a private config directory (`<PERSONA_HOME>/profiles/<launcher>`, #4310), and Claude Code reads the user settings of
the config directory it runs in, never `~/.claude`'s. Something that must hold wherever Persona runs claude goes into the
managed launcher's environment (`providerEnv`, `node/launcherenv.go`): that reaches `personad launch` and the
generated `--settings` env, whatever profile the process uses. A rule Persona applies to every provider
of a kind is code there (as #7319 is for gateways whose model list lacks a fallback model); a user's own choice is the
provider's `env` field, where a value, an empty one too, overrides a Persona default. Check such a setting in the
profile the process really uses, not in `~/.claude`.

## A profile hook installed on the next scan is too late for the first start (#7636)

Claude Code reads settings hooks only at process startup. An extension that discovers a new assistant profile on its next
heartbeat can therefore leave the conversation's first process without its `PermissionRequest` policy even when periodic
reconciliation is otherwise correct. Seed a brand-new directory only with the extension's stable tagged entry, never copy
arbitrary hooks or rewrite an existing profile; the extension still owns the policy and later profile-local reconciliation.
A synthetic real-Claude check on 2.1.322 also rejected a plausible but wrong event attribution: storing an `ssh` command in
a variable and expanding it as the command did not emit `PermissionRequest` in bypass mode and the command ran. Do not infer
the prompt's command from elapsed time; capture the hook event or use a known bypass-immune call.

## `--setting-sources project` still reads `~/.claude` as the home's project files (2026-11-12, #5786)

A guest conversation's `--tools ""` and `--strict-mcp-config` take away tools, not context: an extension's guest on the
CLI read the owner's `~/.claude/CLAUDE.md`, every `~/.claude/rules` file and the auto-memory index (about 39k tokens in
its first request on one machine), so the person it answered could ask for them. A meeting's room was started with
`--setting-sources project`, CLAUDE.md excludes for every directory above its own and auto memory off, and even that let
the rules through (about 27k of those tokens): for a directory below the home, Claude Code reads `~/.claude` as the home
directory's project `.claude`, rules included. `claudeMdExcludes` takes paths and globs and covers rules files too, so
every guest now also excludes `<dir>/.claude/rules/**` for each directory above its own, the home's and the profile's;
the same first request was under 3k tokens. The same goes for `AGENTS.md` and `.claude/AGENTS.md` above it, which Claude
Code reads in place of CLAUDE.md when the guest's directory has no CLAUDE.md of its own. Check such isolation on the real CLI: pick a marker line from each source and
count it in the run's transcript, which records the instructions the process loaded.

## A private profile drops `~/.claude`'s settings, not its CLAUDE.md (2026-11-14, #7318)

With `CLAUDE_CONFIG_DIR` set to a private profile, files and settings under `~/.claude` do not reach the conversation
as the user's: the skills switched off in `~/.claude/settings.json` came back (about 5.1k tokens per request on one
machine). The one way in is the ancestor walk: for a directory below the home, Claude Code still reads
`~/.claude/CLAUDE.md` and `~/.claude/rules` as the home's project instructions (about 5.2k tokens there), as it does for
a guest (above). Exclude those with `claudeMdExcludes` and carry over only what should follow the user (here the
`skillOverrides` set to `"off"`). "Not `~/.claude`" is not the same as "private": `claude_start_dir`, a wrapper's
`CLAUDE_CONFIG_DIR` and an explicit `config_dir` elsewhere are profiles the user set up and may run plain `claude` in,
and the user's switches live in the one `claude_start_dir` names when it is set. Only the directories Persona creates
count as private. To check on the real CLI without a model, point `ANTHROPIC_BASE_URL` at a local mock that records
request bodies and count a marker line from each file in what Claude Code sends.

## Read colors to determine whether Claude's input box is empty (#1434)

After each turn, Claude Code puts a suggested next message in dim text (SGR 2), such as `❯ 现在部署一下 hub` ("deploy the hub now"). Plain-text screen reads can't distinguish it from typed text.
The pre-effort-change check read only plain text, mistook suggestions for drafts, and kept reporting "The terminal input box has unsent text". To detect actual user text, always use
`readPaneANSI` + `screen.ClaudeInput` (`Draft` already excludes suggestions, new-chat `Try "…"` hints, and the cursor); fall back to plain text only when the host can't read colors (`plainScreen`).

## A typed start must fit macOS's canonical line (2026-11-09, #2022)

A new herdr tab's tty is in canonical mode until zsh's line editor takes it, and macOS limits a canonical line to 1024 bytes (`getconf MAX_CANON /`). herdr 0.9.5 refuses a longer `pane run` up front with `pane_send_failed`. The node types its start about 300 ms after creating the tab, so the shell is rarely at its prompt yet. After #1055 added the `unset` of the assistant's environment (about 460 bytes), every managed-launcher start or resume was over the limit and failed every time. Before that, it only failed when the shell happened to be slow. A retry that seemed to work had a shorter line.

In a herdr pane whose shell sources a file (sh family, fish), a start now writes its whole command to a 0600 `agent-start-*.sh` in the node's directory and types only `. '<file>'`. The shell sources the file, so aliases and functions used as launchers still work, and the file deletes itself first. When the pane refuses the line, the start closes the tab it made and returns `terminal_not_ready` (503, `retryable: true`, `delivered: none`). `TestHerdrStartTypesAShortLine` checks the worst case. Not covered: herdr's restore (`claudeResumeArgv`) still types the full command. Orca panes were left out until #5450 (below).

## A startup question eats a typed start's head (2026-11-10, #2570)

oh-my-zsh asks "[oh-my-zsh] Would you like to update? [Y/n]" while `~/.zshrc` loads and reads a single key. A start typed into a new tab a fixed 300 ms after creating it lost its first character to that question: the shell printed `command not found: nset` (for `unset …`) or `permission denied` for the start file (for `. <file>`, with the `.` gone), and the start then waited the full 3 minutes for a claude that was never run. Typed input is not "buffered either way": anything that reads the terminal before the line editor (`read -k 1`, `read -q`, a `[Y/n]` prompt) consumes it.

The node now waits for the new tab's shell to settle before typing (`node/shellsettle.go`): only shells in front of the pane (no `git` or `nvm` an rc file runs) and the screen unchanged for 600 ms, at most 15 s. A question on the last line is never answered for the user: after 20 s the start ends with `shell_waiting` and the tab is kept open for 10 minutes. A typed line the shell still answers for (`rejects` / `rejectedLine` in `node/shellmiss.go`) is typed once more right away instead of waiting out `claudeStartWait`. Avoid fixed sleeps before typing into a fresh shell; read its screen and foreground processes instead.

Waiting made the question certain to show, and a kept tab nobody could reach left the start failed for good (#10365): the phone could not type into it, a resend got the stored error for 10 minutes, and a Retry opened another tab that stopped on the same question. Three things together fix it:

- **Turn the question off where it is asked**: what the node types arrives after the rc files ran, so it cannot help. The start tab is created with `DISABLE_AUTO_UPDATE=true` + `DISABLE_UPDATE_PROMPT=true` (never the second alone: that updates silently) and `POWERLEVEL9K_DISABLE_CONFIGURATION_WIZARD=true` in its shell's environment, in the same create that gives it the Agent mark `PERSONA_AGENT=1` (#10111; herdr's `tab.create` `env`, asked again without it by a herdr that refuses the field; the Herdr App's `terminal.run` `env`), and the start line unsets them again, keeping the mark, so the Agent's environment stays the user's. A second way to hand a new shell variables (ours first carried them on the context and probed `--help`) was folded into the one #10111 added: one `host.EnvLauncher` create, one old-herdr fallback. Never put them in the herdr server's environment: the user's own tabs would stop updating.
- **Let the owner answer**: a question no variable turns off is still never answered *for* the user, but its answers go with `shell_waiting` (`questionChoices`) and a full device's pick is typed into the kept tab by `start.continue`, only while the same question is still the tab's last line and only one of the offered keys; never anything into a password prompt. The record goes back to starting under the same id (`startLog.restart`), so a resend waits for the new start instead of the old error.
- **A retry in the kept tab fails fast**: one whose question is still open ends with `shell_waiting` at once and a renewed `kept_until`, instead of a new tab with the same question. After an answer, though, the question stays the last non-blank line until the shell prints its prompt (oh-my-zsh only moves the cursor), so that start waits it out as a new tab does. That holds for anything typed into the kept tab since its start failed, the assistant's `start.keys` too (`keptStart.typed`): its remedy is keys and then a Retry, often within a second, and a fast fail there spent a retry and invited the same key again, typed at the prompt.
- **A key that shows after the question is not always a line waiting for Enter**: compinit's `read -q` echoes the key with no newline and has already read it (measured in a pty, 2026-11-18). An Enter "for the echo" goes to whatever reads next: a later `[Y/n]` takes its default, or the prompt gets an empty command. Questions known to read one key (`shellAsk.SingleKey`: compinit, zsh-newuser-install, powerlevel10k's wizard, oh-my-zsh's update question) get the key alone; only a line reader, or one not known (a bare `[Y/n]`), is watched for the echo.
- **A question answered at the computer is no refusal**: the card's answer then types nothing (an answer at the prompt could run as a command) and the start runs again in the tab all the same, where the shell settles or the question there now ends it with its own answers. A `prompt_changed` refusal ("refresh before answering") left every button on the card failing until the tab closed, though the words had offered the computer as an equal way.

## Recheck a kept tab after terminal lookup (2026-11-17, #9633)

A stalled Orca Agent can bind its pane while the closer looks up the terminal. A check made before that lookup
does not protect the recovered Agent. Put the local ownership check inside each close-attempt callback, including
the callback after a stale handle is refreshed. Hold the node mutex only for the predicate, never across the RPC.
Recovery uses the existing released-claim/fresh-tab path; a real close failure still stops the retry. This narrows
the lookup window and does not make the later engine request atomic with binding.

## "The shell is at rest" needs a screen to read (2026-11-11, #5450)

On a Mac running Orca, starts kept failing after 3 minutes with "the tab's shell had not run it yet", the start line sitting unrun after the prompt. Its echo was printed above the shell's banner too, so it had been typed before the shell started. For the first 40–100 ms after `terminal.create` returns, Orca's `terminal.read` answers `source: "screen-unavailable"` (`orcart.ErrNoScreen`). `settleShell` took any read error to mean "this host cannot read screens" and typed at once. The tty was still canonical, and Orca had been left out of the start file, so the 1400-byte command was cut at 1024 bytes (the echo above the banner is exactly that long) and its Enter was dropped. It looked like it depended on load or on whether the start made a worktree, but it was a race on every start: whether the first settle read came before Orca had a screen.

`settleShell` now waits while the host has no screen yet, and Orca starts type the start file as herdr's do (`TestOrcaStartWaitsForTheScreenAndTypesAShortLine`). If a host can say "not drawn yet", treat that as "wait", not as "cannot read". To check by hand: `orca terminal create` followed at once by `orca terminal send` of a line over 1024 bytes plus `\r` leaves the line unrun.

## An engine naming claude in a start's tab is not claude having come (2026-11-19, #10588)

On a Linux node with Orca, one of three starts sent in the same second failed twice with `agent_start_stalled`, "claude did not become ready within 1m0s of starting". Its tab held only about 30 empty zsh prompts: the start line was never echoed and Claude Code never ran there. No dialog had appeared, so Orca must have named claude for that pane, and `waitReady` took that alone as claude running (`started()`): it waited 1 minute for an input box instead of 3 for the shell, blamed Claude, and skipped the shell's answers (`shellmiss.go`, #1642 and #2570), which it reads only while claude has not come. Orca names an agent from a process scan by pane key and, when that fails, from its own row and terminal identity, which outlive the agent (`hostorca.go`); its `terminal.send` does not say whether the text reached the shell either. Why the line never arrived is still unknown.

A start in an Orca tab now needs evidence of its own before Orca's word counts (`node/startevidence.go`): Claude Code's screen (its banner, input box or a dialog), or the start line run, meaning its start file is gone (the file removes itself first) and nothing on screen says the shell could not run it. The echo alone is not enough: a line sitting unrun after the prompt (#5450) and a line the shell answered for (a cut head, a command not found) both leave it on screen. A start whose line never showed in the tab and whose start file was never read ends after the shell's wait as `terminal_not_ready`: nothing ran, the tab is closed, and the hub's one retry opens a new one. herdr keeps its word: it names the program in front of the pane, stops once it exits (`claudeGoneWait` relies on that), and reports a line it could not send (`pane_send_failed`). `TestOrcaStartNeedsEvidenceBeyondOrcasWord` covers each case. Not covered: an Orca terminal takes one launch (`orcaHost.Run`), so the #2570 retype of a rejected line cannot run in the same Orca tab and fails as a second launch.

## A pane's shell may be fish: quote for it too (2026-11-10, #2867)

What the node quotes for a pane is parsed by the user's own shell: the start file runs through `source` in fish, and Orca types `cd -- <cwd>` and the command into the terminal. fish still takes `\'` and `\\` as escapes inside single quotes, so the POSIX quoting of the first message `a\';touch PWNED;#` kept the quotes open in fish and ran `touch`. `shellQuote` (`server/internal/node/node.go`) now puts each backslash outside the quotes (`'\\'`), where the sh family and fish read it alike, as the SSH deploy already did after the same finding (`docs/lessons/server.md`). `TestShellQuoteReadsBackInRealShells` reads such words back in every installed shell, fish included. No quoting makes a control character safe on a typed line (a tab completes and closes the quoted word): a first message gets its controls made inert (`typedArg`, "Text an agent is sent can press keys in its terminal" below), but a directory can't be rewritten, so Orca refuses one whose name holds a control character before it opens a terminal.

## Allowing one command for an Agent (2026-11-04, #1300)

- **A skill's `allowed-tools` makes launching the skill itself an approval** (immediately denied under `-p`), so it doesn't save a card, and a fixed SKILL.md can't carry per-computer paths.
  Grant permission with `--allowedTools` when Persona starts claude: it applies only to that session and composes with launcher settings (Claude Code honors only the last `--settings`; rules there either override or are overridden by the launcher's settings).
- **Allow rules use absolute paths, no wildcards**: `Bash(sh */x/scripts/run.sh *)` also allows an identically named script the Agent wrote in its own directory (acceptEdits doesn't ask before writing); confirmed it actually ran.
  Claude Code splits commands joined with `;` or `&&` and checks each individually; acceptEdits still asks for files under `.claude/`.
- Once allowed, the command's capabilities no longer require human review: if arguments can specify output paths (`-o`), restrict them inside the command itself.
- Don't approve test cards meant for the user's phone on their behalf: allow through rules first, or let the user approve.

## Wait must not close unread logs (#3276)

- `os/exec` owns the read end of `Cmd.StderrPipe()`, and `Wait()` closes it after process exit. If the reader goroutine hasn't finished, the final startup diagnostics are lost; waiting for `stderrDone` afterward can't recover those bytes. `StdoutPipe()` has the same read / wait constraints; concurrent calls aren't safe.
- node's assistant stderr now uses its own `os.Pipe()`: give the write end to `cmd.Stderr`, close the parent's write end immediately after `Start()` returns (also on startup failure), and let the reader own and close the read end after EOF. `Wait()` then can't close it prematurely. Paths that never start a reader must close it too.
- If a reader stops scanning because of an overlong line, log the error, keep draining the pipe, then close the read end, avoiding a child blocked on a full pipe or an unexpected broken pipe.
- Deterministic regression: a test child writes a short diagnostic and exits, then `Wait()` runs before starting the reader. The old implementation loses logs every time, with no sleep dependency. The child gets only test-required environment and an isolated HOME, not the user's shell / profile; repeat existing launcher regressions with `GOMAXPROCS=1`.

## "claude did not become ready within 1m0s" usually means a slow shell, not slow claude (#1396)

With around ten Agents on a Mac (15-minute load average 69, 18 cores), a task dispatched from a chat reported this error. Investigation showed zsh in the new herdr tab
started after 2 seconds, but loading `~/.zshrc` took 54 seconds before executing `claude` (`ps -o lstart` compares zsh and claude start times). node started its 60-second timer when typing the command,
so claude timed out just 7 seconds after starting. The tab remained; claude became ready later and waited without any session binding. Readiness detection (input box seen twice, herdr recognizing claude)
was fine. Timing is now split: up to 3 minutes for the shell to launch claude (herdr recognition or trust dialog), then up to 1 minute for the input box. Timeout errors identify the stage
(`claude did not start within 3m0s: the tab's shell had not run it yet` / `… did not become ready within 1m0s of starting`), and failure closes the newly created tab,
so retrying doesn't leave another waiting claude. hub startup waits expand accordingly (5 minutes for task startup; 5 minutes for sends to starting / resuming sessions).

The first stage, "herdr recognizes claude", can also never arrive (#3454): a wrapper launcher execs a native binary named something like `claude-2.1.318`. Claude's input box is already visible,
but herdr's `agent.get` never reports claude; node waits 3 minutes, then says "the tab's shell had not run it yet (is the machine busy?)", directing users to machine load. herdr's Agent detection rules:
- By foreground process name: basename accepts only `claude` / `claude-code` (`codex` for Codex); wrappers whose argv[0] remains `claude` already work. For unrecognized panes,
  `agent.get` returns error `agent_not_found` (herdr 0.9.5 source), not an empty-agent result; node maps this to `host.ErrNoAgent`, meaning "herdr doesn't know an Agent is here".
- Before the name, it reads `HERDR_AGENT` from the foreground process's exec-time environment (`/proc/<pid>/environ`, macOS `KERN_PROCARGS2`), since herdr 0.7.1 / macOS 0.7.5; older versions and Windows ignore it.
- `HERDR_AGENT` in the pane shell's own environment is authoritative and can't be removed with `unset`; everything subsequently run in the pane counts as that Agent. Therefore put it only before the Agent command, never export it into the pane shell or tab / workspace environment.

node now starts Claude in herdr panes with `HERDR_AGENT=claude <launcher> …` (only in shells supporting that syntax; `codex-start` sets `HERDR_AGENT=codex` for Codex; `docs/integrations/launchers.md` ("How herdr recognizes Agents started by launchers")).
If still unrecognized (old herdr, Windows, a pane shell like tcsh that can't use the prefix, or an alias first executing `export` / `cd`), keep waiting rather than failing early. If the final check in a herdr pane sees
an input box but herdr doesn't know claude is present (`agent_not_found` or empty agent), change the error to
`claude's input box is on screen but herdr did not recognise the agent within 3m0s (herdr sees <foreground process name> in front): is claude a wrapper? …`,
then explain process-name / `HERDR_AGENT=claude` recognition and cases where the prefix doesn't reach claude. Foreground process name comes from herdr `pane.process_info`. With no input box, retain the shell-not-started error;
other engines' panes (Orca) keep their original error.

`pane.process_info` in herdr 0.9.5 names each foreground process by `name`, `argv` and `cmdline`, with no `argv0`, and Claude Code's `name` is its version
(`2.1.330`). Its list starts with the foreground group's leader: a launcher's wrapper when claude runs under one. Code that takes the first process for
claude itself must pick Claude Code out first (`herdr.Client.PaneProcesses`): matching `argv0` found nothing, and the port scan reported Claude Code's own
loopback listener as a server the Agent runs, which kept every idle Agent behind a wrapper from Close (#14681).

- A Claude Code turn ending with a model API error ("API Error: Connection lost mid-response…", overload, rate limit, expired login) is done in herdr just like normal completion, so hub counts it unread:
  task_plan tasks once showed green "Done" cards with the raw error as summary and "finished" in snapshots (#1516). Only the last text distinguishes this: when a session is stopped and `last_text` begins with these errors, hub sets `turn_error` (prefix only;
  mentioning "API Error" in an answer doesn't count), marks the task interrupted, doesn't auto-close it, and writes an `interrupted` receipt. Ordinary Agent pushes also stop using a success marker.
- **The model service returns 200 with empty content, and the turn ends silently** (measured 2026-11-05, one gateway upstream account): the transcript contains no assistant item and no `isApiErrorMessage`, error, or `stop_reason`;
  Claude Code inserts an `isMeta` "[Your previous response had no visible output. Please continue…]" and retries once; another empty response ends the turn without terminal error, so there is no `turn_error`.
  Tasks once wrote "completed" receipts, sent done notifications, and auto-closed (#2349). Now inspect the turn's items: no reply, tool call, or image counts as interrupted.
  A verdict that only reaches the receipt is not enough: the Agent's row still said done, so the task read "done, not seen" on every client, in the snapshot and in `agents_list` (#10187). Everything that counts a task interrupted keys on the row's `turn_error`, so the hub puts its own verdict there (`turn_error_kind` `no_output` / `stopped`, `silent_turns.go`) and keeps Session Triage from treating it as a model error.
  Persona's own Stop (`stopReporting`'s Esc to a task Agent mid first request) can end a turn with no output and no "[Request interrupted by user]" line, so the transcript alone reads it as a model failure; the hub notes the agents it interrupts and calls such a turn stopped.
- **Codex writes no reply when interrupted by the model service** (measured 2026-11-05, Codex 0.166, fake service returning 503): rollout has only this turn's `task_complete`, with `last_agent_message`
  `null` and the error in `error` (`{"message":"unexpected status 503 …","codex_error_info":{"http_connection_failed":{"http_status_code":503}}}`);
  app-server reports `turn/completed` with `turn.status: "failed"` + `turn.error` (camelCase `codexErrorInfo`). Last-text-only `turn_error` is always empty for Codex:
  session triage doesn't auto-continue, tasks don't write interrupted receipts, and the last text remains the pre-error answer and may be judged done (#2185). node now reports `turn_error` and its type;
  see `docs/assistant/agent-work.md` ("Codex error types"). To verify the protocol, `codex app-server generate-json-schema --out <dir>` exports every type for the installed version.
- herdr associates SessionStart with a pane by `HERDR_PANE_ID`, which processes outside panes can inherit: a tmux server first started inside herdr (botmux's
  `tmux new-session -d`) retains it, and every claude subsequently started by botmux reports into the same pane, making the main tab show a botmux session (2026-11-04).
  Compare herdr-reported sessions with the pane's own claude process `sessions/<pid>.json` (`docs/integrations/claude-integration.md` ("Session ids and transcripts")).
- **Claude Code enables Artifacts by default; a gateway rejects every new herdr Agent's first turn** (2026-11-04): since 2.1.26x, interactive mode enables Artifact tools by default,
  including their skill description in system messages; some gateways reject the capability (`400 request is not representable on this edge: Claude Code Artifacts are enabled`).
  `claude -p` main chat works, looking like only Agents are broken. It surfaced when one computer jumped from a long-outdated Claude Code (2.1.110) to a new version,
  while others already disabled it in user settings with no reports: first check how large the version jump was. `--disallowedTools Artifact` doesn't work (skill instructions still sent),
  nor does another `--settings` in launcher arguments (Persona's later `--settings` replaces it entirely).
  Manually adding `enableArtifact: false` to `launcher-settings/<name>-env-settings.json` seems ineffective: node rewrites that file at every launch, losing the edit before the next Agent starts.
  Both `enableArtifact: false` in `--settings` and environment `CLAUDE_CODE_DISABLE_ARTIFACT=1` worked in tests (interactive claude in tmux, three configs compared);
  Persona-managed launchers now carry both (`docs/integrations/launchers.md` ("Running Persona-managed launchers")).

## A launchd node has no `$SHELL`, and macOS's `/etc/passwd` has no users (2026-11-26, #16217)

The model list failed on a Mac with `claude did not provide a model list: unexpected EOF (zsh:1: command not found: claude)` while sessions started fine.
- A launchd agent's environment has no `SHELL` and `PATH=/usr/bin:/bin:/usr/sbin:/sbin`. The `/etc/passwd` fallback that works under systemd finds nothing on macOS, which keeps accounts in Directory Services: the node guessed zsh for a bash user.
- Even bash would not have helped: `bash -ic` reads `~/.bashrc`, but Terminal opens login shells, so Mac setups (nvm's installer, `brew shellenv`) live in `~/.bash_profile` / `~/.zprofile`. The session worked only because herdr's tab is a login shell.
- On macOS read the shell with `dscl . -read /Users/<user> UserShell` (the node builds with `CGO_ENABLED=0`, so no `getpwuid`; `os/user` has no shell field) and run it as `-lic`; a bash with no `~/.bash_profile`, `~/.bash_login` or `~/.profile` stays `-ic`, since a login bash would read none of the user's files.
- `zsh -l` runs `/etc/zprofile`'s `path_helper`, which moves the system directories ahead of the inherited PATH: a test that checks an exact PATH through the login shell expects `path_helper -s`'s order. Check with the node's own environment, not your terminal's: `env -i HOME=$HOME PATH=/usr/bin:/bin:/usr/sbin:/sbin bash -lic 'command -v claude'`.
- A test that the node "runs the user's shell" must unset `SHELL`; a developer's terminal always has it, which hid this. The other way round, the node package's `TestMain` pins `SHELL` to zsh on macOS (`pinTestLoginShell`), so a developer whose login shell is bash runs the `~/.zshrc` fixtures in zsh.

## "the tab's shell had not run it yet (is the machine busy?)" when the launcher isn't there at all (#1642)

The same 3-minute error came from a start whose launcher the tab's shell simply did not have: herdr's panes ran inside a container whose `~/.bashrc` detected the container and returned early,
so the launcher function defined further down existed in the node's `bash -ic` (where the list, "Test availability" and the health checks run) but not in the pane. The tab printed
`bash: <launcher>: command not found` a second after the command; herdr never names an agent in such a pane, so nothing ended the wait, and the cause was buried under the echoed
700-character command in the error's screen tail. Launcher health made it worse: the failed start was classed `other`, and the check that runs right after a failed start
passed in the login shell and reset the launcher to `ok`, so the assistant kept picking it. App starts were not recorded at all.

- Persona lists and checks launchers in one environment and runs them in another; nothing guarantees they match (a container, a different login shell than `loginShellProgram`, herdr's `default_shell`, a bash login tab reading `~/.bash_profile`).
- While claude has not come, `waitReady` now reads the shell's own not-found line for the command it typed (`node/shellmiss.go`: bash, zsh, dash / sh, ksh, fish, Ubuntu's and Fedora's handlers, paths that are missing or can't be run)
  and fails at once with a coded refusal. Anchor on the session id in the typed command: only lines after it are this start's (an adopted pane's scrollback is not), and a line about claude counts only after the command's last echo
  with the shell back in front (a slow rc prints its own noise between the terminal's echo and the shell's). Accept only whole lines: Claude's output quoting a shell error (`⎿ zsh: command not found: x`) is not one.
- A health check in the login shell cannot clear a failure only the terminal has: keep it as its own class (`not_in_terminal`), skip the follow-up check and the repairs, and clear it only on a start that works.
- Keep a refusal's code through every hop (`control.go` used to wrap it into text with the screen tail; `startLog.finish` kept only `err.Error()`): the apps and the hub can only word what still has its code.
- Match what the programs really print, not what they mean to: `personad launch` ends through `log.Fatal`, whose default flags put the date and time in front (a pattern anchored at the start of the line never matched it);
  dash, ksh and busybox say `not found` of a missing path; fish follows an error inside a function with a backtrace that repeats the function's arguments, the session id with them.
- Don't read a shell's lines by their English words alone (#6122): bash translates them through gettext (about 40 catalogs) and glibc translates `strerror`, and Homebrew's bash on a Mac follows the system language even without `LANG` (`bash: nosuch: 未找到命令`), so the start waited out its timeout on any non-English machine. Read the line's shape instead (a shell's name first, then colon or full-width colon parts, one of them the start's own command), and only for the command the start itself typed, so rc noise in any language still never ends a start.
- Don't compare a node's timestamps with the manager's clock: a node whose clock ran behind made a first failed start look counted already. Keep what was counted instead (with the health records, across restarts).

## A subscription reset time is not proof that quota is still exhausted (#9386)

Quota can recover before the reported reset. Skipping every on-demand probe until `resets_at` made a stale usage-limit record refuse starts even while the launcher worked. Reuse the machine × launcher single-flight probe and throttle; only a passing model request clears the record. A successful terminal start alone does not prove quota recovered. A default Sonnet probe cannot clear an Opus limit: request and verify the limited family. Capture the health record before sending the probe and compare it atomically when recording the answer; newer exhaustion must survive an older success. Save a failed probe's new reset evidence instead of releasing a weekly limit at an older short-window reset. Merge, never overwrite: a later failure with fewer details ("usage limit reached") must not erase the known family or reset, an unrelated failure (a timeout) says nothing of the quota, and a pass clears only its own family; patching each overwrite one at a time kept reopening the same hole. Keep each family's own reset: one shared reset let a known Opus reset release a Sonnet limit whose reset was unknown. Any change to the evidence, merging an older failure included, is a new version of the record that an in-flight pass cannot clear; comparing only the check time missed merges that keep it. An unknown reset is rechecked on the same throttle, not left to the periodic round. Keep periodic probes waiting for the reset so an idle, genuinely exhausted subscription spends no extra requests. Evidence that is never recorded is a hole too: an Agent's turn whose reset was unknown was skipped, so another family's pass admitted the family it named (#9646). Record it with an unknown reset, dated by the turn's own time so a transcript the hub reads late goes stale sooner. The record has one evidence time for all its families, so when that goes stale, a sibling family's known reset must still hold.

## Don't default to bare claude when resuming externally created sessions (2026-11-05, #411)

- On many computers, `claude` itself isn't logged in; launchers (functions in `~/.zshrc`, or Persona-added launchers) supply the gateway and key.
  For exited sessions created outside Persona, if the launcher can't be inferred (different gateway URL spelling, `inferLauncher` doesn't match), a bare `claude --resume`
  yields only "Not logged in · Please run /login". If the user selected a working launcher in "New Agent", use it (hub passes it to node, which uses it only when neither of the first two choices exists).
- Keep the session's original model on resume (`sessionState.Model`); switch to the "New Agent" model only when using the hub-selected launcher.

## `--resume` without `--model` brings back the session's last model (2026-11-09, #2487, 2.1.296)

- A model's `model_not_found` is not automatically a configuration error (#5234). The live main conversation's model answered normally, then the gateway returned 404 on later turns; treating every user-selected model as final left each turn on an error card. Record when a model last answered in this conversation: a `model_not_found` within an hour gets the same bounded same-model retry as a 5xx, then the normal model chain. Never replay a model that has not answered here, and remember a missing recovery-chosen fallback for its launcher across conversations and resident handovers so it is not offered again at once.

- Claude Code restores the model a resumed session last ran on. Only `--model`, `ANTHROPIC_MODEL` or an `ANTHROPIC_DEFAULT_*_MODEL` stops it; `"model"` in `settings.json` or `--settings` does not.
  The main chat had no model configured, so the app said "Claude Code default", but every `--resume` kept the model an earlier start had used. When the gateway dropped that model, every turn failed (`model_not_found`, later a 503 retried ten times), and changing Claude Code's settings changed nothing.
- To check which model a turn used, read the last `assistant` line's `message.model` in the transcript. `modelUsage` in `-p --output-format json` and in `cost-state` adds up the whole session.
- An assistant conversation resumed with no configured model now gets `--model` with what a new session would use (`node/assistant_resume_model.go`). `--model default` alone would be wrong: it ignores the settings' `model`, and on a gateway set up through `ANTHROPIC_MODEL` it would replace that gateway's model.

## herdr restart and restore (v0.9.5, 2026-11-04, #1879)

- `herdr server` is a foreground headless server; it doesn't daemonize itself. Socket: `$XDG_CONFIG_HOME/herdr/herdr.sock` (or `HERDR_SOCKET_PATH`).
  Processes inside herdr panes (including Agents on this computer) inherit `HERDR_SOCKET_PATH` pointing to the user's live server. Integration checks and tests must always use `env -i` with isolated HOME / XDG_CONFIG_HOME,
  or herdr commands hit the user's server.
- Native restore (`resume_agents_on_restore`) runs only **after a client attaches**; the headless server doesn't resume Agents itself, and the socket API has no trigger.
  In tests, attach with a PTY via `script -q /dev/null herdr`.
- Claude restore uses built-in bare `claude --resume <id>`, losing launchers, `--settings`, and `--mcp-config` (2026-11-04: after the user opened herdr, 8 Persona sessions switched to their own subscription,
  lost MCP and hooks, and the transcript-less one showed Login). No per-pane disable-restore switch exists, but a per-pane restore command does: `resume_argv` in `pane.report_agent_session`.
  - When herdr already detects the Agent process in the pane (Claude, Codex, pi…), `pane.report_agent_session` accepts and persists `resume_argv` directly without `pane.report_agent`. Do **not** claim detected panes with a one-shot `pane.report_agent {state: "idle"}`: in herdr 0.9.x a custom reporter's `hook_authority` overrides screen and OSC state detection in `recompute_effective_state`, locking `agent_status` to `idle` for the lifetime of the Agent (#4657). Clear any stale `custom:persona` authority with `pane.clear_agent_authority {source}` (never without `source`, which would also drop `herdr:pi`'s full-lifecycle authority), and fall back to `pane.report_agent` only when `pane.report_agent_session` returns `resume_not_accepted` (an Agent process herdr doesn't detect natively, such as a compatible CLI, where `traexAgents` reads state itself and must still merge session-less entries in `snap.Agents`). Releasing authority via CLI (`herdr pane release-agent`) also requires `--seq` greater than the last reported `seq`, or `accept_hook_report` drops the release.
  - herdr types argv into the pane shell: quote only necessary elements (an alias as the first element still expands); elements must not contain single quotes.
  - If official integration later reports the same or another session id, herdr retains the original `resume_argv`: after a session id change, explicitly report again or it restores the old session.
  - herdr before 0.9.2 answers `ok` to `resume_argv` and drops it (2026-11-13: a dev machine ran 0.9.0 for days; the upgrade to 0.9.5 by live handoff carried nothing over, the node believed it had reported, and the next restart resumed Persona's Agents bare). A report holds only for the server that took it: the node records the server instance (`herdr.Client.ServerID`, the socket's mtime, new on every restart, handoff and upgrade) and `backfillResume` reports again when a Persona pane's server changed.
  - The node did not check the version, so a computer that stayed below 0.9.2 resumed bare on every restart without a word (2026-11-20: a power cut restarted a 0.9.1 server, four Persona Agents on a gateway launcher came back on the account's default model, and the assistant's switch back was refused as "runs on <launcher> already", #11792). `reportResume` now asks the server (`ServerVersion`, ping only) and skips a server older than 0.9.2, saying so once; rows whose claude lacks Persona's `--mcp-config` carry `off_launcher` (`offlauncher.go`). Upgrading herdr's binary is not enough: the running server keeps dropping until a live handoff or restart starts the new one.
  - The argv is `eval "<the start command with --resume <id>>"`: herdr only checks that the first word is a plain name (`eval` passes) and types it into the pane's shell, so `CLAUDE_CONFIG_DIR=…` prefixes and launchers that are shell functions work without a wrapper script on PATH. herdr also refuses control characters and more than 8 KiB (`resumeSayable`). Real-Claude check in a throwaway named session: `herdrresume_live_test.go`.


## Claude status line (2026-11-05, #1963)

- Working Claude draws `✻ Frosting… (16m 15s · ↓ 22.8k tokens · thinking with xhigh effort)` above the input box: spinner characters change each frame, parenthesized segments may be absent or reordered (`thought for 1s`, `esc to interrupt`), and ↑ means input tokens. An identical line quoted in the conversation isn't a status line; take the last line above the input box (`screen.AboveInputBox`, `node/claudestatus.go`). When a pane is scrolled up, visible content has no status line.
- A model retry does not use the ordinary `verb… (…)` grammar: 2.1.326 draws `✻ 429 <error> · retrying in 4s · attempt 5/10` (a 500 has the same shape, and an early line can be only `API Error`). Parse the observed error and attempts before the normal spinner pattern, leave the countdown out, and clear on normal work, line removal, turn end and unwatch; elapsed time alone is not error evidence (#887).

## Changing Codex app-server effort: validate levels yourself; `null` means model default (2026-11-04, #1709)

- The experimental 0.163 `thread/settings/update {threadId, effort}` accepts any string, even `"bogus-level"`, and reports success. node must validate levels against
  `supportedReasoningEfforts` from `model/list`, not rely on app-server errors.
- For new threads, `reasoningEffort` in `thread/read` is `null` (unset, running the model's `defaultReasoningEffort`), not "unknown"; display the model default.
- `turn/start` `effort` applies to this and all later turns, not only this turn; `turn/steer` has no `effort`.
- The old-version fallback (remember the selection, include it in the next `turn/start`) must recognize only `-32601` (method not found). The first version treated every non--32602 error as "unsupported",
  so disconnections, timeouts, and missing threads appeared "changed" on the phone, and later `turn/start` carried the stale selection, potentially changing thread settings unexpectedly after reconnect (PR review).
  Bind remembered selections to that connection and thread; invalidate on change.
- Test method: start stdio `codex app-server` in a temporary `CODEX_HOME`; `thread/start` needs no login and calls no model, so it can test settings APIs. Don't touch live `~/.codex`.
  See `docs/integrations/codex.md` ("Thinking effort").

## A gateway's Codex catalog replaces Codex's own list, and `personad launch` is no shell `codex` (2026-11-16, #8797)

- `model_catalog_url` in a Codex provider table makes that catalog the only list Codex has: no bundled fallback. A URL
  that answers an error at start leaves Codex with no models (or the last list in `$CODEX_HOME/models_cache.json`), and
  a keyless provider never fetches it, so the list is empty too. Pass one only where the gateway answered with a
  catalog, and only with `env_key`; do not let a timeout or a 5xx overwrite that answer, and do not make a start wait on
  asking again (give the last answer and ask in the background).
- 0.156–0.160 know the key but read it only with `api_key_model_discovery` on; 0.161 made that the default, so the node
  needs 0.161 (`codex --version`) rather than enabling an under-development feature, whose warning a 0.160 TUI would
  show. The node's codex (the service's `PATH`) need not be the one `personad launch` execs (the shell's): the launch
  checks its own when they are different files.
- `GET /v1/models` has two shapes on some gateways: Codex's catalog when the request carries `client_version`, OpenAI's
  list without. A probe must send what Codex sends (its whole version, no prerelease) and judge the answer's shape; a
  name or path alone says nothing. Codex itself appends `client_version`: a URL that already has one is refused there.
- A managed launcher's codex is started by `personad launch`, which execs codex: wrappers that codexShell puts around
  the shell's `codex` (adding `app-server`) never apply to it. The model list ran `personad launch <name>` alone, and
  its codex started a TUI on a pipe ("stdin is not a terminal"): put the subcommand in the launch's own arguments.
  Reproduce with a fake `personad` that is an app-server only when it gets `app-server`
  (`TestManagedCodexModelsAskTheAppServer`).
- Whatever the node does while `launcher.env` waits counts against the 10 seconds `personad launch` waits for the
  node (`LaunchNodeWait`), and those steps add up: finding codex can run a login shell for 15 seconds, a `codex
  --version` whose wrapper's child keeps stdout open outlives its timeout without `WaitDelay`, and the probe takes 5
  more. Bound the wait itself (`codexCatalogWait`), let the lookup go on behind it, and keep failures for a while (#8814).
- Judge a catalog by every field Codex requires, not a few: one entry short of them fails Codex's whole list, and the
  start then runs on fallback metadata with no list at all. A 200 whose body does not arrive whole says nothing about
  the catalog. `$CODEX_HOME/models_cache.json` is one file for every provider in that home, used only for its writer,
  the same version and 5 minutes (#8814).
- Observe Codex against a local fake gateway with a temporary `CODEX_HOME`: the requests it makes (path, query,
  `Authorization`), what `model/list` returns and whether `codex exec` warns about fallback metadata. Never point the
  experiment at a real gateway with the user's key. See `docs/integrations/codex.md` ("Gateway model catalog").

## Interrupt (Esc) doesn't trigger Stop; pid files include procStart (2026-11-04)

- Claude's `Stop` hook fires only on normal turn completion, not Esc interruption (local slash commands not measured): counting hooks alone leaves "submissions > completions" forever.
  node's `judgeHookTurn` falls back to transcript interruption lines and the title's ✳ waiting marker; see `docs/protocol/agent-states.md` ("Hook turn signals").
- `~/.claude/sessions/<pid>.json` includes `procStart` (equals field 22 of `/proc/<pid>/stat` on Linux, matched in all five instances), `pidDomain`, `status` (busy / idle), and `statusUpdatedAt`.
  To determine whether a pid file still refers to the live claude, don't just use `kill -0`: another program reusing the pid makes it permanently appear "open elsewhere". Compare `procStart` with the current process start time.
- A hook process's parent is usually a shell, not claude: walk ancestors to find the Agent process whose `sessions/<pid>.json` `sessionId` matches; don't guess by process name.
- The local Go toolchain must match `go.mod` (1.27.1); dependencies include modules with `go 1.27.1`, so lowering the `go` line won't build. If automatic toolchain downloading fails in the sandbox,
  download from go.dev, verify SHA-256, extract into a temporary directory, and set `GOROOT`, `GOTOOLCHAIN=local` (an existing `GOROOT` pointing to another version otherwise selects the wrong standard library).

## Hooks in user-global settings.json: asynchronous, reporting only (2026-11-04)

- Before installing hooks in the user's **global** `~/.claude/settings.json`, inspect existing hooks (locally: moshi-hook, Orca synchronous `PermissionRequest`). Hooks for an event compose;
  several blocking hooks run one after another, and terminal dialogs wait until all return. Persona's permission / question hook waits for the phone (`timeout` 86400); installed globally, it prevents the person at the computer from answering in the terminal too.
  Global hooks must therefore be asynchronous, short-timeout, reporting-only (`hub.json machines[].claude_global_hooks`, `docs/integrations/claude-integration.md` ("Global report-only hooks")); blocking hooks remain in the `--plugin-dir` plugin and affect only Persona-started sessions.
- `CLAUDE_CODE_PLUGIN_DIRS` exists (absolute-path list, PATH separator), but is one variable. Another tool (moshi) already occupies its `settings.json` `env` value; using it for Persona would mean changing someone else's value.
- Global entries must be recognizable: commands end with `hook --persona-global`, and install / uninstall touches only such entries; don't use generic words like `hook --global` that other tools may also use.
- These global hooks must not bind panes by environment `HERDR_PANE_ID` either: claude started by a tmux server (botmux) inherits it without being in that pane.
- Global reporting-only events still count toward turns (they are the only hook signals for user-created sessions), but don't mean "this session runs Persona hooks": that makes hub suppress screen cards while waiting for a hook card that will never arrive.
  Persona-started sessions already report through the plugin, so discard the global duplicates before counting, or each submission counts twice. See `docs/protocol/agent-states.md` ("Hook turn signals").

## Package-level work is paid by every short personad command (2026-11-08, #1308)

- Claude Code runs `personad hook` (or `persona-node hook`) on each of Persona's hook events and waits for it to exit. Most of that run was package initialization of the server it links: the i18n catalogs (parsed at start, and still parsed after they were made lazy because `extension.UISlots` and `assistant.silentTurnError` called `i18n.T` in package variables), every prompt template (parsed at start, and still parsed after they were made lazy because `prompts.peerMessageHead` rendered one in a package variable), about 460 package-level regexps (one `[\p{L}\p{N}_-]{1,64}` alone 0.8 ms and 2.5 MB), and the six garbage collections those 28 MB of allocations set off. On a busy Apple silicon Mac, where a Go program that only reads stdin took 6.8 ms, the hook took 40.4 ms (median of 50 runs; persona-node 45.3 ms); after making them lazy (`internal/lazyre` for patterns, `sync.OnceValue` for the rest and in the contract packages, which use the standard library only) it takes 15.2 ms (persona-node 16.4 ms), with init down from 29.5 ms / 28 MB to 3.6 ms / 1.4 MB and no collection. Keep package-level work out of anything personad links: `GODEBUG=inittrace=1 personad hook </dev/null` lists what each package's init costs, `GODEBUG=gctrace=1` the collections during it; a package-level `i18n.T` or prompt render defeats the lazy catalogs and templates, and `lazyre`'s guard test refuses a package-level `regexp.MustCompile`.

## A session with no space is in "No project", whatever its repository (2026-11-15, #8521)

The clients' Agents list by **Project** groups by `space_id` (the hub's catalog of spaces), not by `repo_root`. A source
that never sets a space puts all its sessions under "No project" even when `repo_root` is right, so the **Repository**
grouping looks fine and the bug shows only in the other one. Hosts set it (herdr, Orca, Paseo); a desktop app's sessions
need the node to set one: the Codex App's always had one, the Claude desktop app's had none until #8521. A new source of
sessions outside the engines should set a space too. On the list path, take the repository from the list's Git places
(`fillGitPlace`, read in the background) rather than running git per row: it runs every second.

## One folder was a project for each way its Agents ran (2026-11-23, #13533)

Every way an Agent runs names its space its own way: herdr by its workspace id (`w1`), the desktop apps by
`codex-project:<root>`, a runtime without workspaces by its directory. A folder worked in more than one way was a project
for each in every client's **Project** list, all with the folder's name, and one CLI split too: Claude Code in herdr
against the Claude desktop app, an agy pane in herdr against an agy runtime conversation. Pins, mutes and to-do space
links go by `machine/space name`, so nothing there showed it. The node's last pass now gives every session whose space
only stands for a folder that folder's space (`server/internal/node/folderspace.go`); a new source that names its space by
a folder needs nothing more, and should not invent another path-keyed id. Compare folders by their physical paths: the
same folder reaches the list as written by a runtime and resolved by herdr or a desktop app, and a home directory on
Linux is often a link to another volume. A joined space holds several `space_kind`s, and a client may name its host by
any of its sessions' kinds, so a space action takes a kind that is no host's to mean the workspace's host.

## The session list runs every second: per-row lookups add up (2026-11-09, #2538)

- `pollAgents` rebuilds the whole list every second, so a lookup made once per row is made once per row per second. With 199 Codex rollouts on disk, `codexSessions` resolved each rollout's directory through `realPath` (an `lstat` per path part, a `readlink` per link), stat'ed the rollout up to three times, resolved the Codex home per rollout and rebuilt the assistant's project name with a regexp: about 19 stats per rollout per pass, 5,600 `newfstatat` and 380 `readlinkat` a second from an idle node, half its CPU. A pass-local memo (`codexListPass`) that keeps each directory's resolved path, each rollout's stat, the App socket check per home and the project name for that one call brought it to about 1,000 `newfstatat` and 13 `readlinkat` a second, with the rows unchanged. Keep such a memo local to the call: nothing is shared between callers, and nothing can go stale for longer than one pass. Count with `strace -c -f -p <node pid>` over a steady window against a copy of a real corpus (symlinks to `~/.claude/projects` and `~/.codex/sessions` in a scratch HOME), not on a fixture with a handful of files.
- A walk of a Go map before a stable sort leaks the map's random order into the result wherever the sort key ties. Live panes with no transcript yet all have a zero `UpdatedAt`, the list key is order-sensitive, and every reshuffle sent the whole list to the hub (67 times a minute with eight such agents). Walk the map in sorted key order when the result is compared or published.
- The pass belongs to the whole rebuild, not one part of it (#2869). After #2630 every Claude row's decoration (`decorate` -> `ultracodeRow` -> `isAssistantSession`) still looked up without it, so each row resolved the data directory (`identity.DataDir`: an `lstat` of the former state directory, then stats) and rebuilt the assistant's project name every second, and each Codex row's transcript lookup stat'ed the rollout the list had just stat'ed. `sessionList` now makes one `codexListPass` and hands it to `decorateIn`, `codexSessionsIn` and `transcriptPathIn`; the nil-pass wrappers (`decorate`, `transcriptPath`) keep every other caller as it was. A per-call check with a regexp adds up the same way: `nodeclient.ValidSessionID` ran `^[0-9a-fA-F-]{8,64}$` (about 520 ns) for every row's id, up to 8% of a resting node's profile; a byte loop gives the same answers in about 25 ns. The usage meter's sweep every 10 seconds globbed the projects directories twice (`*/*.jsonl`, `*/*/subagents/*.jsonl`): `filepath.Glob` stats every depth-2 entry it tries as a directory, which is every transcript. One walk (`project_walk.go`) takes the type from the directory entry and stats only links, with Glob's names and order (a test and a fuzz target compare them). On the real corpus the three took a resting node from about 500 to about 380 ms of CPU a minute and cut its file stats by a third (20 s of `strace -f`: 21,200 `newfstatat` to 13,500, the former state directory's 1,622 to 22).
- On a node's first list, `scanBackground` reads each listed transcript whole and tested each line against 28 markers one by one; that was over half of a reconnect's CPU on a real corpus. One Aho-Corasick pass (`bgmatch.go`) is about four times faster on real lines. It is built on the first scan, never at package init ("Package-level work" above). A new marker still goes into `bgMarkers` or transcript's marker slices; the matcher is built from them.

## A resident node's reconnect read every transcript again (2026-11-10, #2864)

- `personad node --attach` makes a new `Node` for each hub connection. The assistant keeper, the spool and the runtime socket already belonged to the resident process, but the background-task scans (`bgScans`) did not, so every reconnect read each listed transcript whole again: on a real corpus of about 270 MB that was half of the reconnect's CPU and over half a gigabyte read, and the first list after it had no background or plan state for the big transcripts until those reads were done. `Attach` now carries the scans to the next connection's `Node` (`carry.go`): adopted before `Serve`, handed over after it returns, never shared by two Nodes. Something kept across connections must be checked on its first use, not trusted: a transcript can be deleted, replaced (another inode at the same path) or rewritten in place while no connection runs. A carried scan goes on only if the path is still the same file (`os.SameFile`), not shorter than what it read, and its first bytes hash the same; anything else starts it over. A new connection has no quick metas yet, so pruning has to look at scans without one too.

## MessageDisplay holds the terminal; measuring it in an isolated claude (2026-11-07, #1309, 2.1.319)

- Registering any `MessageDisplay` hook changes how the TUI shows replies: batch by batch instead of token by token, each batch only once its hooks are done, `"async": true` or not (the debug log says "Detected async hook but forceSyncExecution is true"). A slow hook delays every line on the user's own terminal by its run time, and a hook that prints `hookSpecificOutput.displayContent` replaces the lines on screen. Persona's hook therefore passes the lines to the node and exits at once, printing nothing (`docs/integrations/claude-integration.md` "Live replies (MessageDisplay)"). A Python logging hook cost 90–260 ms per batch just to start. What `personad hook` itself costs, and why it was slow to start, is the section above. Those are the program's own times (exec to exit): a shell-form hook (`command` a command line) runs as `/bin/sh -c`, which on a Mac adds another 6–8 ms to every batch, so a hook on the display path is in exec form (`{"command": "<program>", "args": [...]}`, started directly; in Claude Code 2.1.150 and every build with MessageDisplay, not in 2.1.110). Measure a hook the way Claude Code runs it, shell included, before calling it fast. Without a shell there is nowhere to put `PERSONA_HOME=<dir>` in front, and a command hook has no `env` field (2.1.319's schema: `command`, `args`, `shell`, `timeout`, `async`, …), so the live replies hook gets the node's directory as an argument (`personad hook --persona-home <dir>`).
- Its ids match nothing in the transcript but `prompt_id`; the transcript can be written before the batches arrive and a Stop can come before the last batch. Never let the live text decide anything: replace it by prefix with the transcript and drop late batches.
- Live state tied to "a Stop clears it" stays stuck: Esc (and Persona's stop) fires no Stop, a model error fires `StopFailure` instead. Tie it to the turn as the transcript tells it (an interrupt line, `turn_duration`, `API Error: …`, the next prompt or wake), comparing line timestamps with when the state began, so the end of the turn before, read late, does not count.
- Matching live text to the transcript by prefix across the whole exchange goes wrong two ways: the message before a tool and the one after often begin alike ("I'll …"), and an item can be a prefix of the live text in the middle of a word (`好的` / `好的，…`). Match only the live message's own API message (the words after the last tool result, or the message before it when the node knows that one's text), and only where a word ends (`livereply.go` `unlanded`).
- Measuring in a pty-driven `claude` with a per-run `--settings` (never the user's `~/.claude/settings.json`): the 2.1.319 trust dialog preselects "No, exit" (press Down, then Enter); after the first turn a "Teach auto mode about your environment?" guide can appear (after several starts and denials) and swallow what is typed next, and `"skillOverrides": {"auto-mode-setup": "off"}` in that run's settings keeps it away; start the child with a clean environment (drop `CLAUDECODE`, `CLAUDE_CODE_*`, `HERDR_*`) so it does not attach to the herdr pane it runs from. Claude Code records the temporary folder's trust in `~/.claude.json`, and the run's transcripts land under `~/.claude/projects`: copy them out and delete them afterwards.

## Sessions dispatched by other platforms: use file markers and filter before truncation (2026-11-05, #2428)

- Cloud Agent platforms start claude / codex on the computer in the same `~/.claude` / `~/.codex` as the user's sessions, so muting config directories can't separate them. Reliable markers are at the start of session files:
  Codex rollout's first-line `session_meta.originator` (the platform's own name; Persona uses `persona`); Claude Code (SDK-started, `entrypoint: sdk-ts`)
  has `additionalWorkingDirectories` (the platform's skills directory) in the `attachment.type: environment` immediately after the first message. First-message prefixes are only a fallback: user discussions of the platform also mention its name.
- Claude offline lists contain only the latest 80 sessions: filter before truncation, or hundreds of daily platform sessions crowd out the user's (`scanOffline`).
- Measurement (hub machine, 7 days): about 1300 Codex sessions had the platform originator, about 30 were the user's; about 210 Claude sessions had platform directories, plus 1 with only the prefix. Cold-reading about 2300 file beginnings took 2.8 seconds; subsequent reads use per-file caches.

## herdr pane ids and layout (#2320)

- Pane ids aren't "`p` + decimal sequence": examples `w4R:pW`, `w4R:p2E`, `w4R:p3A`; the count after `p` is base 36 (increasing as panes open), so parsing with `/:p(\d+)$/` mostly fails.
- `layouts` in `api snapshot` has one entry per tab: `panes[]` includes `rect {x, y, width, height}` (width / height, not w / h); `splits[]` contains split-tree nodes like `{"id":"split_1_1","direction":"down","ratio":0.5,"rect":{…}}`:
  no child nodes or pane references; match nodes to panes only by rect (a node's rect is the area it splits). Measured `panes` order is tree preorder, but undocumented, so node orders by `splits` + rect itself.
- Rects alone can't determine split order: both horizontal and vertical lines span a 2×2 grid; only `splits` knows whether top/bottom or left/right was split first.
- When zooming a pane, `zoomed: true`, but every pane's `rect` remains its original split position, not fullscreen (measured herdr 0.9.5).

## Rebuilding a truncated herdr snapshot (#6083)

- A bigger reader buffer cannot recover bytes herdr never wrote. Reuse `ErrTruncated` from the pane-read fix (#6080), then request smaller objects.
  `pane.layout {pane_id}` returns the entire tab's layout, so read it once per tab, keeping both rectangles and split nodes.
- `pane.list` and snapshot `panes` omit agent-only fields such as `name`; derive snapshot `agents` through `agent.get {target: pane_id}`.
  Depending on a whole `agent.list` would reintroduce the size limit. Preserve shell panes separately.
- A tab can be locally focused in an inactive workspace. Select the global focus through the focused workspace, its focused tab, and that tab's `focused_pane_id`.
- Keep the snapshot truncation streak separate from transport failure logging: every successful small call marks herdr reachable, but only a whole snapshot ends the truncation streak.
  Test by cutting the entire serialized socket response at exactly 8192 bytes; a fabricated decode error does not exercise the transport path.
- Multiple reads are not an atomic snapshot. A failed small read must not publish a partial tree that clears the cached names and layouts; let the existing agent-list fallback handle that scan.

## Driving Claude Code `/rewind` (2026-11-05, #2436, 2.1.318)

Recorded screens: `server/internal/screen/testdata/claude-2.1.318-rewind-*`; parsing: `screen/rewind.go`; driving: `node/rewind.go`.

- Type `/rewind`, Enter: a panel below a `▔` line (not `─`, so `isRule` doesn't recognize it). **The list window has only two rows** (`↑ N more` / `↓ N more`), cursor starts on the last `(current)` row; ↑ goes to earlier messages and doesn't wrap at the top.
  A row is a message truncated to 109 columns ending in `…`, with `No code changes` or `notes.txt +1 -1` below. If nothing can be rewound: `Nothing to rewind to yet.` (after `/clear`, the list still contains one `/clear`).
- Enter opens confirmation: `│ ` quotes the full message (wrapped at 109 columns, final row `(47s ago)`), then options. **Options depend on whether code changed after this message**: yes → `Restore code and conversation` / `Restore conversation` / `Restore code` /
  `Summarize from here` / `Summarize up to here` (fifth offscreen, preceded by `↓`); no → `Restore conversation` / both Summarize options / `Never mind`. Numeric selection gets it wrong; select by name.
  The explanation below (`The code will be restored +1 -1 in notes.txt.` / `The code will be unchanged.`) follows the cursor. 2.1.304 wording differs (`The conversation will be forked`); recognize only by title and option names.
- Esc: confirmation → list (cursor retained) → closed; Enter on `(current)` also closes silently. After confirmation, the panel can close **before Claude puts the restored prompt into the input box** (#4609): panel closure and the restored draft are not one atomic screen. An app-driven rewind therefore cannot answer on the first empty box, or an immediate send is typed into the prompt when it arrives later. It waits until the matching draft has been cleared; if nothing is restored, the box must stay empty for one ordinary key-effect grace. Files are restored as selected.
- **Chat history isn't deleted**: the file is a tree; the next message after rewind has `parentUuid` equal to the uuid before the target, and abandoned turns remain. node transcript reading removes them using "two user-authored messages under the same parent abandon the earlier message's whole subtree"
  (`transcript/branch.go`, `docs/protocol/protocol.md` ("Rewinding to a message")). **Don't walk parentUuid backward from the final line**: parallel tool results and attachments are legitimate branches (7681 in a real corpus of 122 files); a compaction `compact_boundary`
  has null `parentUuid`, so walking a chain loses the entire pre-compaction chat. Real corpus (122 local jsonl files, comparing only counts / ids): 3 had such user-message branches; old → new reading removed only abandoned items, every other file matched item-for-item,
  and windowed and full reading agree (`TestCorpusWindowed`). Rewind writes no new lines, so node marks its own rewind directly (`File.Rewound`); this isn't persisted, and after node restart it becomes detectable again only with the next message.
  In windowed reads (only the last 8 MB of large files), if the earlier branch begins before the window, its in-window lines are removed only when paging back to that section; removal changes the token and clients refetch.
- **After a terminal rewind the next line is not always a message** (2026-11-06, #4966): after a while away Claude writes a `system` / `away_summary` line whose `parentUuid` is the rewind point, and there may be no new message for hours, so the app kept the rewound turn
  (with a red "Model request failed" card when that turn ended in an `API Error`). So a system line also counts as a rewind, more narrowly: the user message beside it must have been answered **and that turn must have ended** (a `turn_duration` in its subtree),
  so a recap written with a stale parent cannot hide a turn still running. An unanswered message stays (#3036). In the corpus (about 19.7k Claude transcripts) a system line beside an answered message appeared once, and it was this rewind.
  The session list's quick read (`node/session_meta.go`, last 384 KB only) uses the same tree (`transcript.Tail`) to pick the last assistant text; otherwise the rewound turn's `API Error` stays LastText and the hub derives TurnError ("model service error") from it.
  The quick read does not parse user lines over 8 KB (image prompts, long pastes, big tool results), but those lines must still enter the tree (`Tail.Skipped` parses a copy without the image data and tool-result content):
  the reported prompt carried an image (one 360 KB line); left out of the tree, the rewind was not recognised, and a big tool result in the turn broke the chain so the `API Error` after it stayed.
  In the real transcript the next message after the rewind hangs from that recap (not from the rewind point), so the old "two messages under one parent" rule would not have caught this rewind even when the next message came.
  An app-driven rewind (`session.rewind`) also writes no new line, so the quick read has to be told: node records `File.RewoundLines()` (the whole rewound part, not just its start, which may lie before the last 384 KB) in that file's quick-read cache and makes it re-read at once;
  otherwise the session list's LastText stays the rewound turn's last reply (over 90 seconds on a real node).
- **The /rewind list is not every message the app shows** (2026-11-06, #5397, read from the 2.1.309 bundle's message selector): Claude lists the user's lines that are not `isMeta`, a tool result, a compact summary,
  a command's output (`<local-command-stdout>` / `-stderr>`, `<bash-stdout>` / `-stderr>`), a `<task-notification>` or another agent's message. A local slash command such as `/rename` (`local-jsx`, `display: "system"`) is written as two
  `system` / `local_command` lines (the command, then its output) plus an `isMeta` user note, so it is not a user line and not listed; the app shows the command as the user's message and counted it in `prompts_after`, so rewinding past it was refused (`rewind_mismatch`).
  Older versions wrote commands as user lines, which Claude lists. The node translates the app's numbers before walking the list (`transcript.File.AsListed`, `node/rewind.go` `asListed`), so the four clients count as before;
  when the transcript does not have the message where the app saw it, the app's numbers stand and the screen check refuses them as it always did.
- **A terminal rewind with the prompt left in the box writes nothing at all** (2026-11-06, #5396, 2.1.318): after "Restore conversation" Claude puts the rewound prompt back into the input box, and while a draft is there (or the pane has focus) it skips the `away_summary` recap,
  so the #4966 rule had nothing to go on (18 minutes with no line in a live test). Nothing else records it either: no transcript line (only the SDK `rewind_conversation` path writes `last-prompt {rewound:true}`), no hook event, no change in `~/.claude.json`;
  `sessions/<pid>.json` only goes `waiting` (`waitingFor: "dialog open"`) → `idle`, exactly as for a cancelled panel. The screen is the only signal: the box holds the prompt as typed, and the conversation is redrawn without it, so the last `❯ ` row above the box is the prompt before it.
  The node checks this in the dialog read of resting herdr Claude panes (`node/dialogstatus.go`, every 5 s; the list is rebuilt every second): the plain read has text in the box, a screen not looked at before (plain screen hash + transcript size, so an idle pane with Claude's suggestion costs one coloured read per screen), a coloured read shows it is typed (not Claude's grey suggestion), the transcript's last prompt has those words, was answered, its turn ended, and nothing was written since outside its subtree (`File.LastPrompt`);
  then `screen.TerminalRewound` reads 200 rows of history: the last non-command `❯ ` row has to be the prompt before (for the first prompt: no `❯` row and no `⏺`/`●` row at all). ↑ recall (`─── History n/m ───` over the box, the prompt's row still shown), the same words typed again (its row still shown) and Esc before the reply (#3036, unanswered) are not taken for it.
  It marks the prompt's line by uuid (`File.RewoundAt`, same path as `session.rewind`). A wrong guess undoes itself: a message or reply Claude writes **under** a node-marked root after the mark drops that root, the `File` re-reads the file (new token) and the quick read's `Tail` forgets that turn's lines (other marked turns stay out). The turn's own lines read again when the tree is rebuilt (paging back into a windowed file, the re-read itself) do not count: the lines under a root when it was marked are remembered with it (`branchTree.marked`).
  A resend of the same prompt hangs from the prompt's parent, not under it, so it keeps the root. Live (disposable herdr tab, `go test -tags herdrlive -run TestLiveTerminalRewind`, `PERSONA_LIVE_PANE` / `PERSONA_LIVE_SESSION` / `PERSONA_LIVE_TRANSCRIPT`): seen 0.7 s after the panel closed, by `/rewind` and by Esc Esc, with the transcript unchanged.
  Not covered: the marks are not persisted (after a node restart the detector fires again only while the prompt is still in the box), placeholder (paste/image) drafts, a rewind to an earlier prompt than the last, and hosts with plain-only reads.
- Clear input: single-line uses `ctrl+k ctrl+u` (no notice); multiline needs one `ctrl+c`, which shows `Press Ctrl-C again to exit` below. Press once only, and only after confirming the box holds that message.
- Tests: `claudeSim` in `node/rewind_sim_test.go` renders the recorded screens, and `TestClaudeSimMatchesRecordedScreens` compares each row, so flow tests use actual screen appearances. Its restore gate can hold the prompt back after the panel closes, making the late-restore race deterministic instead of relying on a sleep.
  Live testing runs only in a disposable session (`go test -tags herdrlive -run TestLiveRewind`, `PERSONA_LIVE_PANE` / `PERSONA_LIVE_PROMPT`), with an isolated herdr server and a fresh directory, never the agent's working herdr or a real conversation.

## Paseo's rewind of a Claude Code agent forks its session (2026-11-12, #6293, Paseo 0.14)

- `agent.rewind.request` with `mode: "conversation"` forks the Claude session at the last answered turn before the target (the Agent SDK's `forkSession`): a new `<id>.jsonl` beside the old one, which stays untouched, and the agent moves to the new id (`persistence.sessionId`). With no answered turn before it, the agent gets a fresh id and no transcript until its next prompt.
- A Persona session listed under its Claude session id would therefore come back under another id. Pin it first: `update_agent_request {labels: {"persona.session": <id>}}` (labels merge; one cannot be removed over the wire, so the pin is for good, as for a session Persona started).
- `messageId` is the timeline `user_message.messageId`: the transcript entry's uuid for history Paseo loaded, else the client message id, which Paseo maps to the provider's id (and refuses before the provider acknowledged the prompt). The handler does not load the agent: read its timeline first (that loads it).
- Which providers can: the agent snapshot's `capabilities.supportsRewindConversation` / `supportsRewindFiles` / `supportsRewindBoth` (Claude Code all three; Codex, pi, omp the conversation; OpenCode both at once only).

## herdr reports terminal-blocking Claude notices as done (2026-11-05, #2553)

Claude Code 2.1.309 shows an auto mode billing notice after a turn ("We're changing auto mode…", "Enter to continue · Esc to cancel"), while herdr `agent_status` stays `done`.
Persona only checked herdr and hooks, so 3 blocked Agents appeared "Unread" for minutes; sending hit `checkPane`, whose screen read rejected every dialog, and the assistant couldn't dismiss it either.
node now periodically reads stopped Claude screens and marks dialogs "Needs you" (`node/dialogstatus.go`); Enter-only notices can be dismissed with `dismiss_notice` before sending.
This notice appears in gateway sessions (base URLs outside anthropic.com); Claude Code has no setting to disable it. Enter records `autoModeClassifierBillingNoticeAcknowledgedAt`, and it reappears after 24 hours.

## A guide that opens on submit takes the message, and the send still succeeds (2026-11-06, #5504, 2.1.318)

Claude Code's "Teach auto mode about your environment?" (the confirm step of its auto mode setup: `How you use Claude here` / `Also scan shell history` / `Continue`, `Enter to continue · Esc to cancel`) opened right as a message from the phone was submitted.
It took the text and the Enter (its bundled code drops key presses that come right after it opens), the transcript never got the message, and since the node only checked the screen before typing, the hub answered 200 and voice waited for a reply.
Esc declines the guide with nothing chosen; Enter would start its scan of the project, shell history or other repositories. After a prompt the node now looks for a guide, declines it, and checks the transcript read fresh (the cached meta can be 3 s old and miss the message, which would type it twice);
the message is typed again once, or the send reports `delivered: "unknown"`. Only Esc-safe guides go into `screen.guides`; permissions, logins, trust and paid options stay the user's.

## A background workflow runs, but the Agent is judged done and the to-do auto-closes (#2935)

2026-11-05: an Agent used Workflow for a background multi-Agent audit, ended its main turn, and waited for notification. hub marked it "Unread", marked the to-do done, and pushed "Finished"; #1696 could also auto-close it and kill the workflow.
node recognized only Bash / Agent background tasks. Workflow returns "Workflow launched in background. Task ID: …", and its agents live under `subagents/workflows/<run>/`, not `subagents/agent-*.meta.json`, so none were found.
New Claude Code background tools (Workflow, Monitor) must be registered in `transcript.trackBackground` and node `bgMarkers`, or "turn ended, background work remains" is treated as done. `turn_duration`'s `pending*Count` fields can reveal new types.

- Don't report "not running" hosts as board source errors (2026-11-05, #3023): Agents lists already treated `host.ErrNotRunning` as "no panes", but `workspaces.list` put it in `errors`, so workspace boards on Linux computers without Orca kept showing `orca is not running`. For new source-listing RPCs, decide how "not running" differs from "error" first. Older botmux (e.g. 3.13) also lacks `observe`: no error, exit 0, help on stdout; JSON parsing yields only `schema 0`. Treat non-JSON output as not running and suggest `botmux upgrade`.

## The workflow journal updates, but client counts do not (#3433)

Background workflow journals and Agent transcripts are in separate directories, so the main transcript size (`Session.rev`) stays unchanged. Verifying only that `workflows()` reads new counts does not prove clients received them.
node `listKeySized` must include each run's task id, state, phase, and Agent count to emit `sessions.changed`. Regressions must really pass through `pollAgents` and RPC pushes, keeping the main transcript unchanged and appending only one journal result line.

Fixing the list key alone still missed "push within 3 seconds": workflows reused the 3-second `metaMinInterval`, plus one-second polling and cache timestamps set after reads finished, potentially delaying until the fourth second.
Repeated tests with a 2-second refresh sometimes still hit the third-second deadline. Workflows now use a separate 1.5-second refresh interval timed from read start, leaving polling margin; the main transcript interval is unchanged.
Aggregate phases and usage before truncating to the latest 60 Agents, or large workflow phase counts are too low.
For complete tool-call counts, scan each Agent's full transcript on its first metadata read, then only newly appended complete lines; tail-only misses older calls, and accumulating tails double-counts.
After a run has a final-state file, its result is permanently cached; on completion, bypass short Agent metadata throttling and read the final appended usage and reply before caching.

## Workflow progress: Chinese end notes, usage wiped by error lines, failures counted as done, full-file parsing (#5511)

Found reviewing #3731 against real workflow transcripts, not the hand-written fixtures it was tested with:

- A new task-note kind (`workflow`) got Chinese text on the node but no case in the hub's `taskNoteHead` and no keys in the three `hub-transcript_text.json`, so en / ja users saw Chinese where they had seen Claude Code's English summary before. Adding a kind to `transcript/tasknote.go` `noteText` means adding it to the hub too, with the zh-Hans value word for word the node's (the hub matches the node's text by it); `TestTaskNoteHeadsMatchTheNode` now checks every kind and status. A node-written Chinese line in a new field (`latest_tool.summary`) needs the hub's rewrite as well (`assistant/fleet_text.go`).
- Claude Code's own error replies (`model: "<synthetic>"`, `isApiErrorMessage`) carry a usage of all zeros, and a failed workflow agent ends with one: "latest usage" logic that takes any usage wiped the agent's tokens. The test fixture's synthetic line had no usage at all, so it passed. Write fixtures in the real shape (copy one, scrub it); every reader of usage skips `<synthetic>`, usage without input and `transcript.PlausibleContext` failures. "Without input" means input + cache read + cache creation is 0, not all four fields: a gateway's other replies in a turn are `{"input_tokens":0,"output_tokens":1}` (#3300), and a sum-of-four rule took their output count for the agent's tokens. A test with an all-zero gateway fixture passes under either rule; use `transcript/usage_test.go`'s shape.
- The error line reaches a running agent's cached entry as an incremental read of that line alone (the workflow cache rereads every 1.5 s), not as a cold read: test the failure on that path (`TestSubagentEdgesAppendedRepliesKeepUsage`, `TestWorkflowAgentFailsWhileRead`). The line-type shortcuts are checked against a parse of every line on transcripts in Claude Code's layout, cold and grown at random byte cuts (`TestSubagentEdgesRealLayout`); its fixture needs replies longer than 512 bytes, or the type is found at the start and the tail search goes untested.
- Workflow journals have `failed` lines next to `result` (7 of 361 agents in one real run); "anything but started is done" counted them as done while the protocol said results only.
- Parsing every line of every workflow agent's transcript (`ParseLine` plus a row decode) took 8–9 s for 580 transcripts / 600 MB on the first list after a node restart. Now: an agent's transcript is read whole once for its call count (a workflow agent's calls; a sub-Agent's steps since #5602, which merged with this: before it a sub-Agent read only from its end back to its last words), but only reply lines with a tool call are parsed, line types come from where Claude Code puts them (a user line's type at byte 152, a reply's within 1.5 KB of its end, so most bytes are never searched), and the few lines with the latest call / words / usage are read again by offset. Under 1.1 s; the journal is read incrementally and ended runs once. A shortcut that decides a line's type from its start must check the match is a top-level key: a tool call's input can hold `"attachment":{` or `"type":"user"` within the first 512 bytes.
- On this macOS dev machine, `go test -cpuprofile` put 70–80 % of the samples on `pread` / `fstat` while direct timing showed reading all 600 MB took 50 ms: measure stages with wall-clock timers before optimizing what the profile points at. `open()` alone took 0.3–0.5 s for 580 files there, a floor for any per-file approach.

## A retried workflow agent kept its phase running (2026-11-09, #2323)

Claude Code's workflow view can retry a stalled agent ("retry requested by user after 514s"). The journal then gets a second `started` line with the same `key` and a new `agentId`; the attempt it replaced never gets a `result` or `failed` line, only `[Request interrupted by user]` at the end of its transcript. Reading the journal by `agentId` left that attempt running for as long as the run ran: a two-probe phase showed **2/3** and stayed current in every client.

- `key` names one `agent()` call, not its content: two identical calls in one script (same prompt, same options) got different keys when measured, so a repeated key is always the same call started again. A retry of an agent that had already returned reuses the key too (seen in a real journal, both attempts with results).
- So node counts a call once, as its latest attempt, and keeps only the replaced attempt's `tokens` / `calls`. The transcript's interruption line is not needed for this, and it would not cover a retry of a finished agent.
- Dropping the replaced attempt can leave every count as it was (both attempts running, neither has called a tool), so the session list's change key (`listkey.go`) includes each listed agent's id; without it, clients kept linking the replaced attempt until something else changed. Found by review, not by the first tests.
- Hand-written fixtures had no retries, and the 58 real journals on the dev machine only three. Test journal readers against the real shapes, including user actions in Claude Code's own views.

## opencode plugin: awaiting the event loop in `setup`, subscriptions surviving cleanup, duplicate events per directory (#3070)

2026-11-05: opencode loads plugins once per project directory. Until `setup` returns, every request for that directory returns 503 ("Location plugins did not finish activating"); `setup` must return a cleanup function that aborts event subscriptions,
or the server hangs after SIGINT. The event stream is server-wide, so N directory instances receive every event N times; deduplicate by event id and let only one instance read the stream.
The plugin API doesn't expose lists or full chats, so read `opencode.db` read-only. Assistant messages are rewritten in place within a step without changing seq, and the runtime SDK doesn't repush batches without an increased `toSeq`, so push only finalized messages. See `docs/integrations/opencode.md`.
Agents herdr reports but Persona doesn't recognize were silently dropped by `if a.Agent != "claude" { continue }` (their panes invisible on the phone). They're now terminal-only rows; new Agent integrations must not rely on "unclaimed means disappears".

## OpenCode directory MCP names are not session identities (#11936)

OpenCode ACP registers client MCP servers for the directory, replacing a same-name server for other sessions in that process. Different URL/token pairs named `persona` do not isolate same-directory conversations. Give each Assistant a dedicated ACP process, retaining a shared process for ordinary Agents. In the server plugin, never choose the most recently active conversation: freeze the route owner and reject ambiguous or unavailable native inventory. Revoking a worker must also revoke its MCP URL; resuming allocates a fresh URL so a cached retired token never regains authority. The pinned native-engine contract uses a fake local model to exercise actual upstream replacement semantics rather than teaching a fake the desired answer.

## claude dies, but its hook survives (2026-11-06, #4344, 2.1.318)

Claude Code launches hook commands with `detached: true` on Linux / macOS, so `personad hook` has its own process group: when claude is killed with `kill -9`, crashes, or is OOM-killed, the hook survives, still connected to node.sock waiting for the phone; the card and "Needs you" remain until `hook_timeout_minutes`.
Don't assume "hook disconnected = Agent no longer waiting": while waiting, node checks every 2 seconds whether claude still exists using hook-supplied `agent_pid` / `agent_start` (`watchAgent`); withdraw only after two consecutive missing checks. A killed but unreaped claude is a zombie: `/proc/<pid>/stat` and its unchanged start time remain; inspect state `Z` to detect that it is gone (`procGone`).
- "Closed its finished agent" with the tab still there (2026-11-06): herdr's own log (`~/.config/herdr/herdr-server.log`, UTC) showed every `tab.close` from `session.retire` succeeding on the tab the Agent ran in then. The leftover tabs were others:
  the assistant had sent `/exit` with `agent_send`, which exits claude and leaves the shell, and once even to an Agent already closed, which resumed it in a new tab only to quit (that tab stayed).
  Before blaming a close, match the pane ids in herdr's log (`pane child spawned … pane_id=<n>`, `tab renamed … tab_id=…`) against the tabs that are left. `/exit` and `/quit` stop the Agent and keep its tab, and since #4754 nothing closes a Persona tab left with a bare shell (`docs/assistant/todos.md` ("Stop Agents of completed tasks")).

## Closing a question is a runtime operation (#5700)

`dismiss` hands a question back to the computer and leaves its runtime waiting. A close-without-answer action needs a separate resolution and an explicit runtime capability; sending Escape to a terminal is unsafe because it can fold a question pane or interrupt the turn. See `docs/protocol/protocol.md` ("Closing a question without an answer").

Claiming a resolution and persisting it are separate operations. A timeout or disconnected waiter must not write an earlier terminal outcome while the claimed resolution is still being persisted: the database keeps the first outcome, so a reconnect would otherwise lose the cancellation. Arbitrate under the claim lock and broadcast the committed result to overlapping resumable waiters.

## A node state record means "Persona started it": never write one for an observation (2026-11-06, #5510)

`takesReport`, `close`, `decorate`'s `StartedByPersona` and `markInterrupted` all read "the session has a node state record" as "Persona started it and reports through its plugin". The first ultracode server (#3782) stored its process observations by `state.get` + `state.put` on any session: the first report-only `SessionStart` (`source` `startup` / `resume`) of a Claude the user started by hand created a record, and every later `UserPromptSubmit` / `Stop` / `Notification` of that session was dropped as a duplicate of the plugin's. Existing tests sent hook input without `source`, so they never got that far.
Keep observations of sessions Persona did not start in node memory (or in a store of their own), and write a single field into a record that already exists (`stateFile.setUltracode`), never a whole `sessionState` read earlier: a copy read before `state.load` finished is nearly empty and would overwrite the launcher, directory and model in Postgres, and `merge` keeps a record the node already has. `loadState` runs in a goroutine next to lists and hooks, so anything that reads the stored state once and caches it has to wait for the load (`stateFile.loading`), not cache an empty result. An explicit request that changes the stored observation (dismissing the resume notice, a switch's verdict) waits for the load as well (`stateFile.loaded`): taken before it, it is overwritten when the stored record is read in, after the client was told it worked. Records #3782 already wrote for the user's sessions (no launcher, no directory, zero start time) are dropped when loaded (`observationOnly`): nothing writes them again, so keeping their observation would bring back a dismissed notice, or an old process's value, with every node restart.
Test report-only hooks with real input: `SessionStart` always carries `source`.

## One SIGKILL to a process group can miss a child forked meanwhile (macOS, 2026-11-06, #5942)

Revoking an assistant conversation's grants kills its whole process group (`resetAssistantPermissions`, #5936). On macOS, a process that is forking while `kill(-pgid, SIGKILL)` goes out can leave a child that missed the signal. The child is adopted by launchd, keeps the group's pipes and goes on working; Linux orders the two, so CI never saw it. `TestResetAssistantPermissionsKillsUncooperativeChild` failed two runs in three on macOS, each time with one `sleep 30` left over (PPID 1, the killed group's pgid).
After a group kill whose leftovers matter, sweep with `osproc.SweepGroup`, which kills again until `kill(-pgid, 0)` says ESRCH. Killed members count until they are reaped, which happens quickly.
Leftovers matter whenever anything waits for the group's pipes to close: the Lark CLI login's cancel (`larkcli.Runner.StartLogin`) killed its group once, and a surviving `sleep 30` held stdout, so the login reported done only 30 s later (#4004).

## Verify the actual question producer and durable consumer (#5976)

A question UI can be correct while a second producer omits its cancellation capability. Match the reported card ID and tool against both pending interactions and history. A later question with a different ID can be a newly generated triage question, not replay of the answered one. Treat explicit defer as structured option intent instead of forwarding a model-generated pause label to an Agent and causing another turn.

Closing is not durable merely because the card vanished: persist the interaction outcome and the owning turn, propagate write failures, and serialize turn writes so an earlier snapshot cannot overwrite cancellation. Replay terminal outcomes to owners after reconnect instead of asking again. Test the actual triage producer, old persisted cards, failed writes, stale snapshots, and a genuinely new turn; Agent-runtime tests alone miss this path.

## herdr still answers while it shuts down (herdr 0.9.x, measured 2026-11; re-check on the installed version)

- While herdr's server is stopping, its socket still accepts connections and `ping` still answers; only calls that need the
  server's state, such as `session.snapshot`, return `server_unavailable`. Measured shutdown time: about 0.15 s with no
  panes, about 1 s with ten, and 40–120 s when one child process hangs.
- So neither dialing the socket nor `ping` is a liveness check. `herdrAlive` (`node/herdrstart.go`) only dials and would
  call a stopping herdr alive, and `ensureHerdr` / `startHerdr` then skip starting a new one although the old one is about
  to be gone. Where the question is "may I use it", call something that needs state (`session.snapshot`); keep the dial only
  for "is this socket stale / mine".
- The long tail is a child that ignores its exit. When a start looks stuck for a minute or more, look for one hung child
  process rather than a slow herdr.

## Esc on an idle Claude input box exits claude (Claude Code 2.1.270; re-check on newer versions)

- With an empty box and no turn running, one Esc leaves claude and the pane falls back to its shell. Esc stops a running
  turn; on an idle conversation it is "quit", not "clear the input".
- Every place Persona sends `esc` to a Claude pane, checked 2026-11-07:
  - `session.interrupt` (`node/node.go`, the Claude case calling `n.keys(ctx, p.SessionID, []string{"esc"}, "")`; a Codex
    session goes to `interruptCodex` and an assistant conversation to `assistantInterrupt`, which kills the process rather
    than sending Esc): the app's stop button, the assistant's `agent_interrupt`, Feishu cards and channels all land here. It
    does not look at the session's state first, so a stop sent on a stale "working" state, or a second stop after the turn
    already ended, can hit an idle box and end the session.
  - Not guarded: the `/effort` picker (`node/modes.go`) sends `esc` when it could not recognize the picker within its own
    timeout (`_ = n.sendKeys(ctx, pane, "esc")` before returning "没打开思考强度的选择器…"), right after `/effort` was typed
    and submitted. On a version without `/effort` the box is idle at that moment — exactly the case above. It also sends
    `esc` when the picker is open but does not close.
  - Guarded: the `/model` picker (`node/model_switch.go`, Esc only once `modelPicker(s)` saw the picker), the `/rewind`
    panel (`node/rewind.go`'s `close`, which returns unless `screen.ParseRewind` still parses a panel) and the automatic
    guide (`node/sendsafe_submit.go`'s `declineGuide`, reached only after `screen.Guide` / `CheckClaude` recognized the
    screen). Each sends Esc only after its own screen was recognized.
  - Free key presses from a phone terminal, a watch answer, or a Feishu screen card's Esc button
    (`modules/lark/ask_cards.go`): the user's own key, not a decision Persona makes.
- Before sending `esc` as "stop", confirm the turn is running (session state, or the working status line above the box);
  send it at most once per stop, and never as a way to clear the box — the `/effort` path above shows how easy that is.

## Another program runs the user's Claude hooks: grok does (measured 2026-11; re-check)

- grok runs the hooks in `~/.claude/settings.json`, and setting `GROK_HOME` does not stop it: a global Persona hook, or a
  profile's, runs under grok as well. Only `GROK_CLAUDE_HOOKS_ENABLED=0` turns that off.
- Our hooks must assume a different program at the other end: no pane variable (`HERDR_PANE_ID`, `ORCA_*`), a session id
  grok's own, and possibly no `node.sock`. `hook --persona-global` already exits at once without a pane variable or socket
  (`docs/integrations/claude-integration.md` "Global report-only hooks"); keep that property for anything new.
- Read a user's global settings as "hooks that other tools may run too": a blocking entry there blocks grok's sessions and
  the other tools' sessions, not only Claude's.

## The session merge must not call another host (2026-11-07, #5816)

- `setAgentsAt` merges every host's agents after any refresh, even one of a single host. Whatever in it calls a host
  runs on every host's refresh: it asked each pane's host what runs in the pane (`sessionFromProcess`), so Orca's poll
  every second asked herdr, and a herdr refresh ran Orca's process scan (three `ps` runs, 157-211 ms on a Mac) when an
  Orca pane was due for a look. The merge now asks only the hosts that answered its refresh; another host's pane keeps
  the pid its own refresh found. A new host call in the merge needs the same scope (`fromProcess` there).
- So a pane is looked at again only by its own host's refresh: a herdr pane whose claude has not written its sessions
  file yet waits for herdr's next refresh (an event, or `slowSnapshot`), as with herdr alone, not for Orca's next poll.
- Ownership's rule 1 is the exception (`resolve`): it asks every candidate's host. Scoped like the rest, Orca's poll
  made the Orca pane the owner of a session herdr's pane also ran, because herdr had never been asked about its pane,
  and herdr's next refresh gave it back.

## One slow herdr call in forty is not a regression (herdr 0.9.5, measured 2026-11-07; re-check on newer versions)

- herdr answers about 0.1 s late now and then with nothing else asking it: on one Mac 12,000 pings over six minutes had
  p99.9 2.3 ms and one answer of 107 ms, and 2,400 more had one of 103 ms; a busier machine may see more. #5816 began
  from single ~170 ms sends in runs of 40 with Orca beside herdr, but a herdr-only run had one of 149 ms, and on fakes a
  herdr send is the same with and without Orca. Measure 400 or more per configuration and read p99 before you blame a
  change.
- Probing herdr from Python with `subprocess.run` in another thread of the same process shows a ~110 ms stall on many
  of the spawns. That is the probe, not herdr: the same spawns from a separate process show none.

## Text an agent is sent can press keys in its terminal

- Much of what a caller sends an agent ends up typed into the agent's terminal: a message, a start's prompt, a rename's
  title (`/rename`, a prompt), a card's answer; a to-do, its decisions and a knowledge-base entry are kept and typed
  later, in a prompt. Control characters in that text are keys to the terminal, and the program in it acts on them as
  on the user's own, which can change what the agent may do. A rename's tab name is not typed: it goes to the host as
  the tab's label (herdr, Orca, Herdr App), which the node does not make inert; herdr's renderer drops control
  characters when it draws one.
- So the hub refuses terminal control characters (Unicode `Cc` but newline, carriage return and tab) from callers whose
  grant is narrower than the user's, extension tokens, on the routes that type a caller's text into a terminal or keep
  it for a prompt (`server/internal/hub/terminal_controls.go`). Check kept text as it comes in, while the caller is
  still known. A new route that types or keeps a caller's text needs the same wrapper.
- Newline, carriage return and tab pass because those routes deliver text as text, where a line break stays a line
  break. A path that types text as raw keys would make them Enter and Tab, and needs a stricter rule.
- Check every string of the body as JSON decodes it, keys included, not one named field: Go's `encoding/json` matches
  a struct's field names without regard to case, so a check of `p["text"]` misses a body that spells it `"Text"`, and
  a handler that decodes only the first value of a body leaves the rest to whatever reads it next. Check the decoded
  path and query too: a route can keep text from them (a knowledge-base entry is named from its id).
- The hub's refusal covers only extension tokens; channel messages and the apps reach a prompt without one. So the
  computer also makes those characters inert in every text it prompts an agent with (`inertControls`,
  `server/internal/node/hosts.go`): an ESC becomes `<ESC>` and any other becomes `<U+XXXX>`, for every host and
  caller. That covers each paste (`hostPrompt`, the fallback paste, Codex answers) and a compatible CLI's first
  message, which is shell-quoted onto a command line that may be typed into a shell: there `^U` erases the line,
  quotes or not, and a carriage return then runs what follows. A shell's line editor acts on more than controls
  inside quotes: a tab completes the quoted word, closes the quote and leaves the rest to the shell (bash, zsh), and
  csh ends the command at a line break. So that first message also gets its tabs as spaces, and its line breaks as
  spaces unless a sh-family shell sources it from a start file (`typedArg`, `server/internal/node/start_agent.go`):
  Orca types the line itself and does not say which shell it opened, so the user's login shell is only a guess there.
  Match what the agent shows or its transcript records against that inert text, not the text as sent; a runtime is
  sent the text itself, so the echo tie accepts either.
  Keys sent as keys (`session.keys`) are the one way left to press a control key, on purpose.

## Profile acceptance must include launcher defaults

A purpose-level model override does not change the managed launcher's default. Launcher health checks invoke the launcher without that override, and small-model and subagent paths read their own environment aliases. When testing a model profile, read back the launcher's main, small, subagent and alias fields, then exercise a real health check. Preserve sealed credentials and unrelated fields; capture the before-image in the same transaction as the switch. A purpose-only readback is not evidence that every execution path changed.

## Each tool's own archive (measured 2026-11-07, codex-cli 0.163.0, Claude Code 2.1.291)

- Codex's `thread/archive` / `thread/unarchive` (and `codex archive|unarchive <id>`) are not idempotent: a second archive answers `-32600 "no rollout found for thread id …"`
  and the command exits 1 with only "Error: failed to archive session". Decide from where the rollout is (`sessions/…` or `archived_sessions/`), before and after the call, never from the answer alone.
- An archived Codex thread can't be resumed (`thread/resume` and `codex resume` say "Run `codex unarchive …` first"): restore it natively before anything resumes it.
- `codex archive` from another process fails while a Codex process still writes the thread (its cross-process writer lock); it succeeds once that process exits.
  Through the app-server that has the thread loaded, archive shuts the thread down there first. Test with a throwaway `CODEX_HOME`: `thread/start` alone materializes a thread to archive, with no model call.
- Claude Code has no archive for local sessions: its archive/unarchive is for cloud and Remote Control sessions (the sessions API). The Claude desktop app's Code sessions keep
  their own `isArchived` in the app's private session records (`claude-code-sessions/…/local_<id>.json` plus an `archived-sessions.idx`), keyed by the app's own session id: an undocumented
  store the running app writes, so Persona doesn't write it. Never move or delete `~/.claude/projects/*.jsonl` as an "archive": it breaks `--resume` and can't be told apart from a deletion.

## An Agent's environment is its engine's, not the node's (2026-11-08, #744)

- A pane runs with the environment its engine was started with. Orca had been started from the assistant's Bash tool,
  so every Agent in it had `PERSONA_ASSISTANT=1`, `CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000`, `ENABLE_TOOL_SEARCH=false`
  and the parent Claude's own markers (`CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_EFFORT`, …). As a result:
  - every Agent compacted at 200K;
  - every Agent loaded all tool schemas up front;
  - `personad launch` took them for the assistant and kept the inherited window;
  - their MCP servers listed the assistant's tools.

  Restarting the engine from launchd cleared it.
- The same machine still had an older, independent data directory next to `~/.persona`. Only the node's service
  definition set `PERSONA_HOME`, so `personad mcp` and `hook` in the Agents resolved the older directory and dialed a
  socket no node listened on.
- Anything the node starts in a pane must say itself which node it belongs to, and must not count on the pane being clean:
  - the node writes its directory into `mcp.json`, the hook commands and the typed start commands;
  - the typed commands `unset` the assistant-only variables (`node/agentenv.go`).

  A new variable the node gives only the assistant goes into `assistantOnlyEnv`.
- To see what an Agent really got, read its process environment (`ps -E -ww -o command= -p <pid>` on a Mac). Print the
  names only. Its Bash tool's `env` also shows what its own Claude added for its children.

## A Codex assistant conversation needs its own "process gone" row (2026-11-09, #2126)

`scanOffline` reads only Claude's projects and `codexSessions` leaves the assistant's conversations out of Agents, so a Codex main conversation had no row once its idle process was reaped. A client with no row can't send, so the composer turned off and the message that would resume it could not go. `codexAssistantsAtRest` in `server/internal/node/sessions_offline.go` lists them now.

## A Claude Code hint can depend on the terminal, not only the version (2026-11-09, #2467, 2.1.311)

The node knew one send-now hint, `ctrl+x ctrl+s to send now` (measured on 2.1.319), and told the user to update Claude Code when it
didn't see it. 2.1.311 binds `ctrl+x ctrl+s` and `ctrl+enter` both to `chat:sendNow`, but its hint names `ctrl+enter` only where the
terminal reports extended keys and `$TMUX` / `$STY` are unset (a herdr pane), and `ctrl+x ctrl+s` otherwise. The same binary shows
either one.

- Capture a hint in the engine the node drives (a herdr pane, not tmux or a plain terminal) before writing the string into code; a
  version number alone doesn't pin the text. The bundled code that chooses among a key's bindings shows which ones it may name.
- Press the keys the screen names. When the node doesn't recognize a hint, it reports exactly that (`send_now_unrecognized`) and
  presses nothing. Saying "update Claude Code" sent the user after a fix that didn't exist.
- To check that a key really arrives, run a control: a plain Enter on the empty prompt left the queue as it was, while `ctrl+enter`
  (herdr's key name, or the CSI u bytes `ESC [13;5u`) sent it. Confirm in the transcript (`queue-operation remove`), not only on screen.

## A fullscreen Claude Code scrolled up has its hints off screen (2026-11-11, #4537, 2.1.321)

"Send now" was refused as `send_now_unrecognized` although 2.1.321 still draws `ctrl+enter to send now`. Every machine runs
Claude Code with `"tui": "fullscreen"`, where the queued message and its hint scroll with the transcript. Scrolled up, the
screen shows only `Jump to bottom (ctrl+End) ↓` and `❯ Press up to edit queued messages`, so the node, which reads the visible
screen, saw a queue in the transcript and no hint.

- Before concluding a hint changed, read the version's bundled code and capture the screen. Here the text was built from the
  binding (`${keys} to send now`) and was unchanged from 2.1.311; what differed was where it was drawn.
- A screen-reading check has to consider that the user may have scrolled. In fullscreen, Claude Code's own `scroll:bottom`
  (ctrl+End, the key the pill names on Linux) brings the end back without sending anything, and the node reads again.
- herdr 0.9's `send-keys` rejects End, Home and the page keys (`invalid_key`). `pane.send_text` writes their xterm bytes as
  they are (`ESC [1;5F` for ctrl+End), which Claude Code reads as the key.
- Read a hint by its shape, on its own line or after a gap, never from the screen joined into one line: a queued message
  that ends in a key name ("…with ctrl+c") would otherwise lend its key to the chord.

## Claude Code put a label in front of the send-now keys (2026-11-17, #9628, 2.1.326)

"Send now" was refused as `send_now_unrecognized` on a screen showing `  ⎿  Queued · ctrl+enter to send now`. 2.1.326 labels
the hint under a queued message; the keys and the `to send now` after them did not change. The reader took keys only from a
line of their own or after the gap following a queued message, so the label in front hid them.

- When reading a hint by its shape, anchor on the keys and the words after them, and let the keys start after any of the
  line's separators (a gap, ` · `), not only at a fixed position. Words that aren't strokes between the separator and the
  keys still lend nothing.
- To date a new shape, grep each version's binary for the component's template (`Queued \xB7 ${`) instead of guessing:
  `npm pack @anthropic-ai/claude-code-<platform>@<version>` fetches one without installing it.

## Rewind positions are not message identities (#9410)

AGY reused a user step's numeric ID after rewind, so the node attached an earlier send's persisted client message ID
to a new input. The current pending bubble could not settle, and reply caches could reuse abandoned content. Keep
pagination position separate from immutable item identity: native birth time plus a cumulative replacement generation
distinguishes even repeated prompts with identical timestamps. Preserve prefix and same-step IDs, namespace tool-call
pairing too, and invalidate old cached pages once. Test normal runtime pages and full-item reads across append and
complete-file replacement; fresh sessions and distinct sequential prompts do not exercise reused identities.

## AGY exports parallel results as they finish; a late step is not a rewind, and a call without a result must not shift the rest (#11086)

Parallel calls are exported in completion order, so a call that fails at once (unknown tool, invalid arguments) lands
before its sibling's result: `228` (two calls), `230` (INVALID), `229`. Reading the lower index as a rewind popped
`230`, started a new branch and left the second call without a result. The readers also paired results with calls
through a queue that nothing reset, so one orphan call shifted every later result onto the call before its own, and the
newest calls ended with no result, which the hub does not fold and the web draws as running tools. A step whose index
no retained step has is a late arrival: insert it in step order, keep the branch (item IDs) and change the content
revision so watchers re-read. A planner step, a prompt or an error closes the calls before it. `integrations/antigravity/lib/map.js`
and `server/internal/node/agytranscript.go` must stay in step. Test with a real export's shape (the swapped lines,
an interrupted call, a system message between results) and compare each result with its own call, not only the counts.

## A send is not a history rewrite, even for steps the export has not caught up with (#12014)

The Antigravity companion shows a prompt's steps from ACP before agy exports them, and gave them their own history
token. Every send switched to it and the export switched back, so every client took each send as a rewrite: the web
read a fresh two-turn page, the main chat's earlier messages vanished for a moment, and every task card of the day
stacked above the new message until the rest came back. Projected steps that only grow are appends. Change the token
only when one of them is dropped (the export replaces them, an interrupt), and record the next token before showing
them, so a restarted companion cannot report the token clients hold them under. Test the push a send produces (same
history, `from_seq` at the previous end, no earlier turn sent again) and a restart while a reply awaits its export.

## An installed CLI is not an explicit runtime override (#9181)

Plain AGY discovery joined the shell launcher cache and shadowed its connected assistant-capable companion. New
assistant task chats then failed the launcher-agent guard. Preserve internal provenance when listing the ordinary
executable: it is a terminal fallback, while real shell aliases, functions and helper entries remain authoritative.
Use the same ownership rule for published launchers, models, starts and assistant validation. Exercise discovery plus
the normal launcher-list and assistant-send paths with the CLI installed, explicit overrides and disconnects; a runtime
test with an empty shell cache misses the collision. Never mutate the cached shell list while publishing runtime rows.

## Antigravity planner message completion can be progress narration (#3389)

A text-only `PLANNER_RESPONSE` with `status: DONE` can precede more reasoning and tools. It closes that message, not the
conversation turn. Read `conversation_summaries.db` in read-only mode. `CASCADE_RUN_STATUS_RUNNING` or `not_fully_idle`
keeps the conversation working. An idle summary must cover the latest meaningful transcript step before its reply counts as
done. Missing or unavailable summaries leave unfinished history unknown. Test the gap after narration and before the next tool
step, plus stale idle summaries, actual completion and unavailable stores, through the normal runtime inventory protocol.

## Runtime close is independent of terminal access (#3507)

A runtime can implement `session.close` while exposing no terminal controls. Carry its declared close capability to the
session row and use it in the web menu and confirmation. Antigravity must cancel its active prompt and withdraw permissions
before closing. A queued prompt or late permission request must respect closure. Test through the runtime protocol, including
a native agent that rejects `session/close`, and republish the closed inventory after a node reconnect.

## Antigravity history freshness does not establish live execution (#3678)

An externally driven AGY conversation needs a watched transcript poll even when Persona owns no ACP prompt. Native
planner records can also gain response text at an existing step index. Carry those replacements through the history
token and preserve the branch identity, then let the existing client continuity logic replace retained content. Use
the last published sequence as an append batch's exclusive lower bound. Cover both cases through `session.watch`.

A completed user-input or tool step can remain the transcript's last entry after a stopped or failed attempt. A 24-hour
freshness window kept such history working for hours. Working requires an owned ACP turn or explicit native running state.
Apply that native activity before internal-metadata filtering and history limits, and discover running native summaries even
before a brain directory or transcript is exported. Read titles, workspaces and update times from the native summary when
the legacy JSON cache is incomplete. Cover these cases through the runtime inventory protocol.

## Antigravity model directories and ACP acknowledgements (#3716)

Native model availability changes independently of Persona releases. Discover the authenticated CLI catalog and use its
settings and session metadata for defaults and current values. A fixed catalog hides new models and invents labels for
external sessions. Treat ACP configuration responses and notifications as authoritative, publish acknowledged partial
changes, and propagate failures. Native `auto` is distinct from full permission bypass.

## ACP mode success is not the applied agy mode (#10980)

agy can accept `session/set_mode` yet apply `default` because native Auto is unavailable. Treat the direct mode result or
`current_mode_update` as the answer, not a model's trailing `config_option_update`; otherwise accepted Auto can be mistaken
for a refusal. When Auto is refused, apply the existing `bypassPermissions` path through native `accept-edits`, wait for
that fallback before answering a permission, publish the final mode and emit one notice. An acknowledgement of `auto` can
precede a delayed `default`, so only a newer non-Auto choice ends that correlation. Serialize the fallback with newer
explicit mode changes, reapply an explicit mode if native settings diverge from it, and recheck interruption after every
fallback wait. Keep whether a mode was explicit or came from agy's settings: explicit choices survive settings changes,
while settings-derived sessions reread and reapply `configuredMode()` before the next prompt. A notice sent only with
`rt.append` disappears on refetch; keep it in the item projection at a stable sequence as later native steps arrive, and
retain the native step index separately for output files and client-message bindings. Cover direct and notification
refusals, repeated starts, all three asking modes, settings changes, notice reloads and en / zh-Hans / ja localization.

## Runtime availability checks need the runtime's authentication context (#3790)

An SSH node and a local companion can have different credential access. A model list and session start succeeding in
the companion do not make a login-shell health probe authenticated. Opt into runtime-owned checks, bound inference and
cleanup, deny probe tools, and hide transcripts whose temporary probe workspace has been removed. Preserve a real
workspace sharing the probe prefix while it still exists.

## AGY ACP can ignore the configured initial model (#3817)

Native settings and `session/new` can disagree even when the configured model is valid. Resolve native display names or
IDs against the catalog and apply the configured default explicitly to new sessions and availability checks. ACP can
also discard the selected model when a process restarts (#15537, #16458). Before the first prompt on the new connection,
prefer the caller's model and then the remembered selection. An older idle session can recover a model from its last
recorded turn when the catalog still advertises it. Use the configured default when there is no remembered selection.
Retry persisting the accepted model after the turn because the native brain directory can appear after `session/new`.
Leave history reads passive and announce a model only after ACP accepts it.

## A long-running agy acp keeps the account it saw (#10368)

`agy acp` kept the model list it read when the plan lapsed after the user renewed and signed in again: `session/set_model`
failed with `Unknown model ID` until the process was restarted, and each `session/new` published the stale list over the
five-minute `agy models` discovery. Treat a discovery that began after the ACP process started as the newer source, and
compare by process generation rather than wall-clock time. Pending requests do not show who uses the process: a call
between two requests, or a native read over HTTP, has none. Hold a lease for each call, turn and read, replace only when
no other lease is held, with no await between that check and the replacement, and make new users wait for it. Retry once,
and test with a fake whose ACP side reads the account once at startup.

Signals that only cover a sign-out or a missing model miss the common case (#17860): the user signs in with a second
account because the first one ran out of quota, discovery never fails, and every turn fails with `RESOURCE_EXHAUSTED`.
A caller that retries every few minutes keeps the stale process from ever idling out. On macOS the sign-in lives in the
login keychain, so a check on `antigravity-oauth-token` never fires there. Stamp where the CLI keeps its sign-in by
metadata only (file stat, keychain item dates), compare it per process, treat quota and sign-in errors as a reason to
replace, rate-limit that, and remember that terminal `agy` sessions on the old account write it back too.

## Codex speed has separate native metadata (#3865)

Reasoning effort and speed are independent. Read `serviceTiers` rather than deriving Fast support from a model name,
and keep the advertised native ID. `thread/read` omits speed even when `thread/resume` and settings notifications
report it. Preserve acknowledged speed through metadata refreshes, show missing state as unknown, and use a dedicated
node RPC so an older node cannot silently ignore a billable setting in `session.modes`.

Persona-owned Codex conversations use a separate stdio connection. Share catalog parsing across both transports and
include the owned connection in session projection, setting routing and native notifications.

The web composer keeps the tier ID through live updates and the new-conversation draft. A Fast boolean drops
Ultrafast from the pill and loses the tier needed by the first request.

## Runtime assistant identity comes from persisted state (#4202)

A runtime can rediscover an assistant conversation with its current project directory. Apply the node's trusted
session-state projection before publishing runtime rows so the original assistant or health directory and launcher
survive snapshots and reconnects. The hub uses that directory to classify assistant history. Preserve the runtime's
status, model, permission choices and capabilities while restoring owned metadata.

## A hook's allow is not an answer for every dialog Claude shows (2026-11-10, #3752, 2.1.322)

A phone's Always Allow closed a permission card while the terminal kept asking: Claude Code runs the PermissionRequest hooks for
its own safety checks ("Dangerous rm operation …", asked in bypass mode too, with a two-minute countdown) and ignores an allow for
them, plain or with `updatedPermissions`; a deny it takes. herdr kept saying working, so no screen card came, and every message was
refused as `dialog_open` with nothing on the phone to answer.

- Don't take a hook's return for the dialog's end: read the pane after an allow (`node/allowwatch.go`) and let what is on screen
  decide the state.
- Measure a hook's effect on a real pty with a throwaway `--settings` hook and an empty config directory, answering both ways: the
  same check that ignored allow honoured deny.
- `--setting-sources project` does not keep a `--settings` file out: flag settings always load, hooks, plugins and output
  style included. A launcher alias's `--settings` is a user settings file, so a process that must carry none of the user's
  setup (a guest conversation, `node/assistant_guest.go` `guestSettingsKeys`) copies only what it needs from it (env, login, model).
- A node's session state is stored by the hub's manager (`db.NodeSession`): a field it does not keep is gone after the next node
  restart. Persist what decides how a process runs (a guest's tools and kind), and fail closed when it is missing.

## A headless Claude's queue is on its stdout, not in its transcript (#4342)

`claude -p --input-format stream-json` writes `queue-operation enqueue` with no content for a message that waits, so a transcript reader
sees a wait it can't name, and a following `dequeue` pops whatever it guessed. Give each user line a `uuid` and follow the
`command_lifecycle` lines for it instead (queued, started, cancelled, completed). Take one back with `cancel_async_message`, and treat
`cancelled: false` as "it went in": writing it again would send it twice. Check such behavior against the real CLI with a short
`claude -p` script before building on it; the requests are not in the public docs.

## Completed Codex history is not the current turn's state (#8946)

The session list promoted a pane to working from its shared app-server, then demoted it to done because a materialized
paginated transcript ended on an older completed turn. Check the unambiguous live owner's cached active or waiting state
before applying stale-rollout correction, including when no current turn ID was observed. Test the published session row
with old completed history, a late join, a start notification and completion; testing only the intermediate status misses
the overwrite. Keep the terminal-only stale-turn fallback, and do not add RPC or transcript reads to the live guard.

## Herdr workspace directories come from panes (#4761)

herdr's `workspace.list` does not report a workspace cwd, even though panes do (#4761). Resolve cwd-only worktree requests from each session's `pane.list` and optional worktree metadata, with canonical deepest-prefix matching. Only associate a pane-derived directory when the workspace's panes agree: herdr creates from the workspace source, so a matching later pane must not select another repository (#5524). A linked-worktree workspace holds only its checkout, not its main repository: herdr refuses it as a worktree source. A workspace that contains the path can still belong to an ancestor repository. Use it for native creation only when its matched directory is inside the requested Git root; otherwise use Git. Use foreground directories only when the workspace has no stable pane directory. Test with workspace records that omit cwd and a matching second pane from another repository. A repository with no matching terminal workspace can still use Git worktree creation and open a plain workspace: checkout isolation remains, but terminal parent grouping is unavailable.

## A published runtime stream must end after its viewer leaves (#5247)

The Hub caches the latest live text for new subscribers. The runtime SDK's watch state can disappear before the
prompt finishes, while that published text still exists. `streamEnd` uses the stream entry as well as the current
watch to send its empty boundary, and cancels pending throttled text first. A native transcript may have a
middle-truncated copy of the reply, so text reconciliation alone cannot retire the Hub's full cached copy.
Cover unwatch before completion, a pending batch, and a later watcher in the SDK and AGY lifecycle tests.

## A `-c` moves a Codex into the pane's environment (2026-11-12, #4860, codex-cli 0.167.1)

- From 0.163, `codex` with any `-c` override other than a feature switch runs its own app-server in the pane instead
  of joining the shared daemon (`codex-rs/tui/src/daemon_startup.rs`, `config_exclusion`). The tier effort
  (`-c model_reasoning_effort=…`) counts. Besides the terminal-only route, Codex then makes its startup requests
  with the pane's environment rather than the daemon's.
- One computer reached `chatgpt.com` only through a proxy. The user's `codex` shell function set the proxy, and the
  daemon had inherited it. `personad codex-start` ran the `codex` on `PATH` (#4876 covers that part), so a tier
  with an effort made every start fail with `account/read failed during TUI bootstrap: … workspace routing discovery
  timed out`. The same launcher with no effort worked, and so did a manual `codex`.
- To tell the two modes apart, look in Codex's log database (`<CODEX_HOME>/logs_2.sqlite`, table `logs`). The span
  for `account/read` says `rpc.transport="in-process"` (its own app-server) or `unix_socket` (the daemon). The TUI
  also shows a warning that it runs without the shared background server.
- An effort the Codex already runs at is now left out (`node/codex_start_effort.go`). A different effort still needs
  `-c`, and that start still runs with the pane's environment.
- Bare `codex` may instead be a shell function preserved by #4876. Its profile or `-c` can change the effective
  effort without appearing in the node's words. Keep the tier's flag when that function exists or its resolution
  cannot be checked; an explicit executable path does not go through the function.

## herdr's published state can be a reporter's word, not the screen's (2026-11-12, #4865)

- `herdr agent get` / `agent list` / the snapshot give the pane's published `agent_status`; `herdr agent explain <pane>` runs the
  screen detection again and names the rule it matched. When they disagree (explain says `working` from `osc_title_working`,
  get says `idle`), a reporter holds the pane and its last report wins: on herdr 0.7.5 a single `pane.report_agent` with
  `state: idle` kept a running Codex `idle` until the reporter released it (#4657). Compare the two before suspecting Persona's
  own judgment, and test reporter behavior only on an isolated server (`env -i`, its own HOME and XDG directories).
- An agent Persona can judge itself must not take herdr's word alone. Claude panes already had `claudeStatus`; a Codex pane
  joined on its app-server listed Idle for a whole turn while the node knew the turn ran (its `turn/started`), and its chat
  page showed Working from the live words, so the two pages disagreed. `codexStatus` counts the app-server's turn and Codex's
  title spinner over the host's done / idle / unknown.

## Native preview exports can silently truncate assistant replies (#5644)

AGY's compact export includes `<truncated N bytes>` inside long text while its complete export retains the body.
Such previews can fit Persona's own page limit, hiding the full-text action. Read complete native records before
applying SDK page limits, and use the same source for inventory revisions, watch cursors and live pushes. A compact
cursor ahead of the complete export can skip content. Change the history scope when replacing previously cached
previews, and cache parsed files with bounded storage and file-version invalidation to keep polling responsive.
Test normal RPC pages, live pushes, delayed export writes and same-size or atomic file updates. Preserve literal
placeholder examples in complete source text.

For Herdr panes, keep the compact export's step identities and order when filling in text from the full export
(#13706). A full export can arrive late or still hold the abandoned branch after a rewind. Match the step metadata,
retained prefix and suffix, and omitted byte count before restoring a field. A late match must invalidate the settled
preview already cached by clients while keeping the reply's item ID.

## Filesystem timestamps can alias rapid native rewrites (#5659)

Nanosecond stat fields can repeat across same-size writes within one filesystem timestamp tick. Bypass the parsed
cache for recently modified exports, and cache settled histories after a quiet window. Run rewrite regressions on
the deployment filesystem as well as the development machine, including a settled cache hit and atomic replacement.

## Task closure is not user inactivity

Closing a task must not interrupt its conversation (#6819). A quiet transcript does not prove the owner is away:
a connected app may still be reading it, or a user message may be ahead of the next inventory update. Check live app
subscriptions and durable owner activity before automatic closure/retirement; re-check task ownership after asynchronous
lookups. Cancellation must use the same idle-only retirement as completion, not a forced stop or terminal-close fallback.
Keep historical ownership and an explicit stop reason so the remaining transcript explains what happened.
Input and view-exit activity are not read receipts: writing them as seen can suppress the reply the owner is waiting
for (`TestQuietPatrolAbsorbedUserQuestionPushes`). Persist them separately and keep notification regressions in the check.
The manager sees the owner's message on an Agent's own page only as a new turn. "Any turn of a planned task's Agent
reopens its accepted task" therefore undid every review-accepted plan task the moment the owner kept chatting, and its
Agent was never retired (#6819). Attribute such a turn with the owner's presence (`ownerOnSession`: viewing, or active
within the idle window) and keep an explicit task send (`noteAgentSend`) as the only override; test the direct-chat
turn, not just `agent_send`.

An owner-activity hold must leave the completion turn unjudged and retryable, including unsupervised tasks and saved
judgments replayed after a later start. Do not spend failed-judgment retries while waiting for the owner to leave.
A terminal review can encounter activity after the judge already published `pending_review`. Both conversation and
leader reviews must retract that turn's judgment, review state and queued review signal, including a refusal at the
final write boundary. Clear the saved signal too, or the sweep can replay it while the owner is still present. Keep
the turn and rework history so inactivity alone restores review without another Agent turn.
Recheck task ownership, its closure timestamp and owner activity after a stop RPC too: a check before that call
cannot protect a session linked or reopened while it is in flight. Ended terminal relays must remove their clients'
terminal viewer entries and publish the departure without clearing independent chat subscriptions.

The retirement inventory row predates the stop. Read the node's saved stop cause before native archival can remove its
live row, then retain that observed evidence in the archive. Never copy the requested cause or retain a stale positive
cause after a failed read. Recheck ownership and activity after this additional RPC too, and test the native
stop/archive/resume chain with no live inventory row; an isolated snapshot-copy test misses this handoff.
For a still-listed row, preserve its current stop provenance and terminal viewability when stripping live controls;
do not fill a missing, unknown or user cause from the archive's older positive cause.

## Stopped-session ownership is not recovery permission (#8214)

A `codex_history` source can be node-resumable while still rejecting ordinary messages. Keep the generic source-owned read-only guard; admit only the node-qualified terminal row to the explicit supervised Resume path. Persist user stops and fail closed on missing provenance instead of interpreting an old start time or a localized reason string as permission. A cached eligible row also needs a node-side recheck serialized with closes, and a new running observation must clear old eligibility. Keep this metadata in capabilities rather than enlarging the frequently copied Session row past its allocation size class.

## Claude Code keeps one gateway model list per config directory (#8417)

With `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` on, Claude Code caches the gateway's `/v1/models` in
`<config dir>/cache/gateway-models.json` for the last base URL only, refreshes it in the background, and shows it only
while that is the running gateway. Several providers on one computer overwrite each other's list, so a headless
`initialize` answer is not a stable source for a provider's gateway models. Turning discovery off to hide entries is
wrong too: it removes the gateway's list from every `/model` the user opens. Read the gateway itself
(`node/launcher_gateway_models.go`) and compare lists from several computers before concluding a list is clean.

## A Claude session without Persona's hooks still asks with AskUserQuestion (#8515)

The desktop app's sessions (and claude in other terminals) run no hook of Persona's, so their questions never became
cards; the apps showed only a "reply to answer" line, and the questions and options were nowhere to be seen. The peer
message prompt asked the model to offer numbered options in plain text instead (#5887), but models still opened
AskUserQuestion, often with several questions. Read the open call from the transcript (`node/claudeoutside_ask.go`)
rather than counting on the model to follow the prompt, and check a real transcript's `tool_use` input shape before
parsing it. Once the card worked, the prompt went back to asking with AskUserQuestion as usual (#8558): a prompt's
workaround that outlives the fix it stood in for is wrong advice to the model.

## Peer attestation needs the receiver's live permission mode (#9672)

Claude Code 2.1.326's `sessions/<pid>.json` names the live process, session and tmux pane, but not its permission mode or
bypass availability. A transcript's standalone `permission-mode` row is not rewritten on shift+tab; the next prompt records
the mode instead. Reading even the newest transcript at send time therefore states the old class immediately after a switch:
none after default became bypass is held, and stale bypass after the return to default is a mismatch. For an external tmux
session, read the footer through the pane named by the PID file before using transcript evidence. A visible composer
without a bypass or plan indicator is prompting even when a draft or dialog hides default's idle hint; do not fall back
to a stale bypass transcript just because `tuiMode` has no exact idle-text match. Question and permission dialogs draw
rules too; they are not composers and must not override transcript bypass. Plan has no single class:
Claude counts it as bypass only when bypass is available, so use the live process's argv for the enabling flag rather than
treating every plan session as prompting. Without those live sources, keep the old fail-closed outcome instead of guessing.

## A plugin's asUser prompt is the only way in that counts as the person's (Persona's Claude Code plugin)

The peer inbox frames every message as another session's, and no wording in it makes the model take a go-ahead from
it: the same "I approve: create the file" was refused through the inbox and when a plugin submitted it framed, and
accepted only through `$.prompt.submit({ asUser: true })` (Claude Code 2.1.291, 2.1.299 and 2.1.308). Do not try to
argue the model round in the inbox prompt (#5965). Things that cost time while measuring it:

- An outside Claude's tmux pane is terminal input too, and takes precedence over both plugin and inbox for a verified
  owner's ordinary message. Let it fall through to the ordinary pane checks and transcript confirmation rather than
  adding a second typing path; a message without that verified mark must never fall through when the inbox is absent.
  Keep `plugin_input` off that row because the pane, not the plugin, now carries its owner's message (#5965).

- A `claude` started from inside a desktop-app session inherits that host's provider variables
  (`CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST`, its `ANTHROPIC_BASE_URL`) but not its credentials, and answers "Not logged
  in". Start it without the parent's `CLAUDE_CODE_*` variables (`node/ccplugin_live_test.go` `childEnv`).
- A `claude -p --input-format stream-json` session is a good throwaway session for a plugin (it keeps running while
  stdin is open, and has a peer inbox), but its `sessions/<pid>.json` says `entrypoint: "sdk-cli"`, which the node
  leaves to its own host: such a claude is not "outside Persona's terminals".
- A plugin's `$.http.fetch` gives up after 30 s, so a long poll must answer sooner. On Windows, its first connections
  over a Unix socket a moment after the listener appeared failed (`FailedToOpenSocket`); a retry got through.
- `$.ui.log(text)` writes a dim row into the transcript by default; a plugin that must stay quiet passes `{ to: 'debug' }`.
- The plugin API types are written per Claude Code build (`plugin-authoring` skill, `types/claude-code.d.ts`); read the
  build you test against, not an older copy.

## A check before every message must not start a login shell (2026-11-16, #8947)

The assistant's profile check (#4310) ran the launcher probe before every message. That probe is an interactive login
shell sourcing the user's startup files, and some resume paths ran it twice. On one install every message to a running
conversation waited about a second per probe, and in three days none reached it within a second. A check on the send
path reuses its probe the way `launcherProbeCached` and `modelProbeCache` do: kept for a bounded time, and taken again at
once when the files it depends on change (`shellStamp` in `server/internal/node/assistant_profile_probe.go`). A change the
stamp cannot see waits out the bound. In a test, count the shells a send starts with a launcher that appends to a file
each time it runs, not only the send's result.

The start of a conversation's process took the same probe again (#9097): its compaction settings and, on a resume, the
model it would run on, each with a login shell of its own, and the agent, profile and model checks each kept a copy that
aged out on its own clock. A start after a quiet spell waited for four probes one after another, about two seconds each
on a busy machine. Every caller now reads one kept probe per launcher and arguments (`keptProbe`): callers asking at
once share one shell, and a probe in use is renewed in the background shortly before it ages out, so the bound still
holds but no message waits for it. A renewal is not a use, or it would keep itself going forever.


## A Paseo pin belongs to a workspace, not its project (#9300)

Paseo's “Pin chat” persists on its workspace descriptor (`pinnedAt`). Persona's Paseo space is the entire project, which
can contain multiple workspaces sharing a directory; importing a pin into `pins.spaces` would pin siblings too. Carry
the timestamp on the workspace catalog and match machine, source and source ID. An unpin must also release retained
old sessions even if pin and unpin happen before the coalesced catalog push: a session push can observe the temporary
pin while the final catalog bytes equal the earlier unpinned snapshot.

- **Separate assignment data from framework policy (#10200).** Let the manager supply matching project background once; callers add only facts it cannot resolve. Preserve attachment suffixes when a launcher falls back to first-message rules. A dedicated instruction channel must survive resume and must suppress duplicate SessionStart closeout guidance. Never replace a configured Codex developer policy to obtain a cleaner transcript: fall back conservatively. Herdr refuses apostrophes even inside quoted restore arguments; encode them as TOML Unicode escapes, and use a persistent file for Claude's multiline policy. One new closeout can carry delivery, gate and responsible party while both historical formats remain readable.

## Check owner activity at the closure commit boundary (#6819)

Checking before a write lock does not protect a slow write: messages and viewers update independently of task writes.
Automatic closure needs the same transaction-final activity guard as a reviewed closure, and an activity hold must
leave the turn unjudged so the sweep can retry. A memory-only review checks under the todo lock immediately before
changing either the closure or its review ledger, not only before assembling the replacement row.

Delivery of `fleet.clients` belongs to each manager connection, not to a shared success bit. Retain only successful
recipients, retry failed ones even when the global viewer snapshot is unchanged, and send again after reconnection.
Serialize ownership-refresh invalidation with sends so an in-flight report cannot hide the refresh.

The post-stop archive guard cannot undo an interrupt. Register an in-flight retirement before its final activity read,
cancel it on input, viewing or a read receipt, and propagate that cancellation across every RPC hop. Use the separate
`session.stop_if_idle` route: an older peer must refuse, never fall back to an unconditional stop. Refresh the node's
runtime and check cancellation and pending/recent input under its typing lock before each Ctrl+C.
Input recorded between keys prevents the second interrupt. A cancelled or refused stop grants no recovery.
Reload the stop observation for the current task completion after a manager restart. A live row from a resumed
Agent then keeps the ordinary idle window, even though its first-seen clock starts over.
Match the completion at Postgres microsecond precision. A manual edit and older JSON stop records can carry
the original nanoseconds.
Keep the new preflight on that guarded RPC: applying it to legacy stop/retire can rediscover a different engine and
erase a retained session row before its established stop/recovery path runs. Verify those full-package regressions too.
Install a test transport gate before linking or closing a task: both start background token/outcome work, so replacing
the service's hub afterwards races that work even if the stop itself is synchronized.

A deferred unsupervised judgment can outlive its inventory row or its manager. Retry its durable session/turn identity,
but verify the same closed transcript again before applying a judgment. A new/open turn, a released/relinked Agent,
reopening, an unreachable node or another working Agent is not completion evidence. Absence alone proves nothing.

Polling-only native terminal pages still need a read-only hub subscription, including when the first hello has no
`terminal_stream`. Keep page presence separate from the stream's success: retain it on unsupported, release it on page
exit, and restore it after reconnect or page return. Never request a grid or takeover for polling presence.

## A transcript directory is not the selected launcher's sign-in

When restoring an assistant conversation, finding its newest transcript does not establish that a newly selected launcher can authenticate in that profile. Check private-profile ownership before stopping the old process or writing input; a later CLI sign-in error has already accepted the wrong launch and may forward a provider-only model. A refusal should preserve the original process and session state and explain how to continue. Regression coverage needs cold resumes and live idle processes, with both an omitted model and the old provider's explicit model (#10453).

The same holds for a shell launcher's private profile, which a managed catalog can't name: its gateway token lives in the launcher's environment, so the profile never gets a sign-in, and plain claude started there answers "Not logged in" while `claude auth status` in the user's own setup says signed in. Ask Claude Code about the profile the start will actually use (`claudeAuthStatusProfile`), not the machine's default, and move the conversation to where the launcher is signed in rather than telling the user to sign in again (#11694).

## Cross-session permission evidence (#9671)

- A session-list mode is a display snapshot, not evidence of the mode in which a message was written. Capture at the source node while handling the producing call; delayed reports and retries retain that observation.
- A caller's session ID is not process identity. Resolve the nearest known Agent in the socket peer's ancestry: a nested Agent may not claim a more privileged parent. Bind external registry data to the process generation; on macOS the kernel creation time also lets an old registry file fail closed after PID reuse.
- `encoding/json` matches struct fields case-insensitively. Drop every spelling of authority fields and rebuild them from internal context. Ordinary RPCs must ignore source claims even if an older intermediary forwards extra parameters.
- RPC marshaling compacts `json.RawMessage` and escapes HTML characters. A call digest must use that representation at both ends, or a valid message containing `<` or `&` loses its evidence in transit.
- A worker's observation cannot grant a class to a later automatic judge or an unknown source in the same digest. Preserve unknown evidence rather than deriving authority from copied text, labels or cached modes. This provenance never makes Agent text the owner's input.

## Managed model discovery must tolerate a slow interactive shell (#10927)

An interactive rc file can spend the entire discovery deadline in a version manager before the launcher command begins.
A managed model probe has the command and provider settings already, so run it directly and test npm's
`#!/usr/bin/env node` with a different Node.js on the service's PATH. Keep shell discovery for aliases and functions,
and require a complete frame before caching an errored scan. A timed-out helper can print a banner inside the frame.
The Assistant picker reserves a short window after shell discovery so fallback choices still respect a node's own-service policy.

## Gateway quota errors and stopped recovery (#12095)

A gateway can report `rate_limit` without Claude Code's subscription `quotaLimits`. Its UTC available-again prose belongs to the existing usage-limit wait; temporary account capacity is transient. Classifying the turn alone does not permit restarting a terminal Agent that exited: record narrow, node-owned stop provenance too. Never overwrite explicit user/exit provenance, or reauthorize a consumed recovery attempt from a stale transcript error.

- A gateway error alone cannot prove why a terminal Agent later disappeared (#12095). Require a live observation after the failed turn and the retained original Persona pane, and recheck the pane before supervised resume. Keep cold history and externally closed terminals owner-only; a transcript error must not turn an unknown user closure into recovery permission.

## Auxiliary Assistant context must not become native commands (#12427)

Antigravity ACP expands slash commands in every plain text prompt block. A task title in the Assistant snapshot can
therefore activate its suffix-command parser and append a command to an unrelated request. Send the snapshot as an
ACP embedded text resource, keeping the user's own text in its separate text block. Separate text blocks alone are
insufficient: ACP expands each one. Preserve explicit standalone native commands, and test the captured ACP payload
for both a new conversation and later messages with command names in the snapshot.


## Embedded context has a native transcript envelope (#12813)

Moving auxiliary input out of ACP text blocks protects it from command expansion, but Antigravity serializes the
embedded resource back into native `USER_INPUT.content` with a `Content of persona-context:///snapshot.txt:` label.
Removing only the snapshot XML leaves that label visible. Check the native export before blaming replay, and clean
both the companion and pane history projections. Require the complete leading Persona resource envelope so ordinary
labels and other URI schemes survive; keep embedded-resource transport intact and version the history format.

## A persisted triage wait needs answerable evidence

A read-only session skips card creation, but must not skip evidence validation. Normalize model `blocked / user`
results with no grounded question or authorization link before storing them and when displaying old records.
Otherwise a guess appears as "Awaiting your reply" with nothing in the chat to answer.

## A herdr pane's title is its terminal's, and agy sets none (#13639)

herdr reports the title the program in a pane gives its terminal. agy gives none, so an agy pane's row kept the
shell's (`user@host: /path`, a path on a primary screen) or nothing, and the apps showed herdr's tab name (`agent-2`),
even after the node read the pane's conversation and merged its model and usage from the companion's row. When a
herdr row's conversation comes from somewhere else, so does its title: pi and opencode take their runtime's, agy the
companion's, else its own (`cache/conversation_metadata.json`), else the first message typed into it. Merging a
runtime's row into a pane's, go through every field the runtime knows better, not only the ones a report named.

## The companion's agy row can be a conversation a terminal agy has open (#13603)

The Antigravity companion lists every recent agy conversation on disk, including ones an `agy` in a terminal is
running. When the node had no pane row to merge it into (herdr-core terminals were not recognised as agy), a message to
`agy-<conversation>`, card answers included, went to the companion, which ran a second agy on the same conversation: the
terminal never saw it, and the companion's turn overwrote the stored steps from where it had last loaded them. The terminal agy holds an exclusive flock on `presence/<conversation>.lock` and leaves the file after agy exits; never
probe it by taking even a shared `flock` (`LOCK_SH|LOCK_NB`), because a starting `agy` tries `LOCK_EX|LOCK_NB` once and
runs its whole session without a lock if that fails (#14561). On Linux, read `/proc/locks` for the file's device and
inode; where no lock table can be read, fall back to the presence file plus recent transcript writes by another process.
Calls that drive the conversation go to the pane, or are refused (`agy_in_terminal`) while held. So every engine that can
run agy must list it as a pane and learn the pane's conversation: Orca did neither, and every message to an agy open in
an Orca terminal was refused (#16531).

## First tool discovery can precede the session list (#14855)

An assistant process can ask for MCP tools before its node publishes `sessions.changed`, even while the manager's
`assistant.send` is still waiting. Resolve an unknown caller's space from the node's verified process launch directory,
never a model argument or a claimed session's directory. An existing session row remains authoritative. Bind fallback
evidence to its node machine and caller, and use one session snapshot for both a discovery list and its revision;
otherwise a row arriving between those reads can leave an incomplete list cached as current. Test the order with real
`Service.Start`, returning the requested prepare directory and delaying only the node reply and session publication.
