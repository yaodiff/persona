# Agent work state: working / blocked on a named prerequisite / done (#1686)

The user's words (2026-11-04): "Persona isn't proactive enough right now. I see lots of unread sessions… Every session should either be working, explicitly waiting for a prerequisite,
or done… Minimize waiting time as much as possible."

## Why

The unified states in `docs/protocol/agent-states.md` describe the **host's perspective**: Needs you / Working / Unread / Seen. "Unread" means only that a turn ended and the user has not read it,
not how far the session has progressed **in its work**. The Agents page ends up with a row of green dots that must each be opened and read to find out:

- Which tasks are done and ready to wrap up;
- Which are waiting for a user decision ("Should I push to beta?", "Which of D2 / D4 / D10?"), yet show only a green dot in the list;
- Which are waiting for an external event (deployment completion before testing, CI, an OAuth link), with nothing to wake them when the event occurs;
- Which stopped only because of a transient error such as `API Error: 503`, and simply need a "continue" message;
- Authorization links that expire within minutes and are already invalid by the time the user sees them.

## All participants stopped (#8400)

For people delegating work through Assistant, the existing supervision watch raises `all_stopped` after every current
participant has been inactive for **30 minutes**, independent of the task's estimate. It joins a task leader's own
To-do and its workers through `todo_agents.source = worker` / `via = <leader task id>` and supervisor references,
including assistant conversations linked as Agents. Each open task's supervisor receives the group's last stated waits;
the leader's own task also notifies its supervisor. This is a supervision message, not an automatic restart or a push.

Missing/offline sessions remain the orphan signal's responsibility. Finished, parked and review-pending tasks do not
participate; `needs_user` alone does not suppress this check when there is no answerable card. Pending interactions,
Awaiting Reply, task-decision cards, queued input, active loops, automatic error resumes and future `session_work.wake_at`
exclude the group. The manager overlays its work ledger on the session list so newly scheduled wakes and cards count.

Background work must show activity within the same 30-minute interval: running subagents use `updated_at`, workflows use
their own and their agents' activity times, and background tasks without activity timestamps use `started_at`. If those
timestamps are absent too, the Agent's known stop time bounds the age: its background work started before it stopped.
This also covers summary-only background flags and runtime children; missing activity times do not exempt them forever.
A monitor with a future `expires_at` is a scheduled wake. A long-running shell may therefore need supervision despite still being
alive; the message tells the supervisor to verify before restarting anything. No node-side output polling is added.

The latest state, transcript, activity, work-turn, past wake or background-activity timestamp starts each participant's
interval. A sorted `machine/session@timestamp` list is the group's key: unchanged inactivity is told once, and actual
new activity rearms it. Delivery rechecks the whole group and key, dropping a message invalidated by progress, a card,
a wake or a parked task. Failed session, work-ledger or card-ledger reads stay pending for retry and are not marked sent.
Existing signal and persisted fingerprint deduplication are reused: the inactivity fingerprint does not expire with the
30-minute text-deduplication window. Each task and destination keeps only its latest group key, removed by the watch
when the task closes or is deleted. A failed delivery rolls the reservation back.

## Automatic session names (#4812)

Settings › Assistant › Agents can correct recognizable placeholder names with the model configured for
**Information screening** (`inbox.triage`, the same purpose as Inbox sorting). The default is one successful
correction per Agent, with periodic updates off; it does not classify every meaningful title with a model.
Turning automatic naming off prevents further calls but keeps already chosen names and saved advanced options.

The manager owns the title override in Postgres setting `agent.naming.v1`, keyed by computer and session
as an encoded pair. It decorates `session.title` on session lists and lookups, so the existing web, iOS,
Android, watch, Mac and Vision selectors all see the same name. Automatic naming never writes into the
Agent's input or transcript, and never renames herdr/Orca tabs. `todo.title` and plan titles name separate work
objects and are unchanged. Native-title comparisons ignore leading terminal spinner frames and whitespace;
animation does not hide a saved name or discard an in-flight result (#5254). Explicit Claude sessions with proven `prompt` source also keep a saved name when switching between a tool
label and a copied or truncated first-message title, including openings absent from the instruction-prefix
list (#7337). The node's provenance proves the copy; it is not inferred from text alone. This exception does
not treat another runtime/tool, a meaningful `auto`/`runtime` title or unknown provenance as the same fallback,
and it never resets the successful-change count or pays for a replacement name.

Candidates are empty names, tool-only names (including terminal spinners), plain directory names,
new-chat placeholders, titles over 40 characters, common instruction/template openings in Chinese,
English or Japanese, and titles whose `prompt` provenance proves they are copied from the first user message
(including a short exact copy or a truncated one). Meaningful `auto`/`runtime` titles are refreshed only with
periodic naming enabled. Native custom titles are protected even
when they look like placeholders. Old nodes and runtimes that cannot establish title provenance are skipped
rather than guessing that a title was not chosen by the user. Claude nodes provide `title_source`,
`title_manual` and cached cumulative `output_tokens`; unknown sources do not establish eligibility.

`agent_auto_naming_max` is a total successful-change limit (1–1000), including periodic refreshes.
`agent_auto_naming_interval_hours` is 0 by default; a positive integer (1–8760) enables refresh after that
interval only while the Agent is working, its activity is recent, and cumulative output grew since its last
name. A meaningful original title starts with an observation baseline when periodic naming is enabled.
Changing the maximum does not erase the ledger. A native or app rename stops automatic naming permanently
for that session; app/API rename intent is persisted before contacting the node, even when its reply is uncertain.

**Named on request (#7432).** `POST /api/v1/sessions/{mid}/{sid}/autoname` is the same model call asked for by the
user from an Agent's menu (**Auto Rename**), for one session, answered with the new title
(`docs/protocol/protocol.md`, "Naming an Agent on request"). It shares the dialogue, prompt and validity check with
the background pass (`agentNameFromModel`) and skips the background's guards, because the request is the user's own
wish: the change limit, the placeholder test, the automatic-naming switch, and the protection of custom titles.
The ledger record is marked `requested` and is no longer `manual`. Such a name also stands over a native custom
title or a title of unknown provenance, and its row is published with `title_source: "auto"`; it holds until the
native title changes, or until the user renames the session by hand, exactly as an automatic name does. The
background's change count is left as it is (a request neither spends nor resets it), its failure count and
cooldown are cleared by a successful request, and a failed request records nothing. Requests for one session
share one model call. Persona's own conversations are refused.

The assistant-only `agent_autoname` tool is a narrow second entry to that same request, for an explicitly requested
name in a Persona conversation. It accepts only an exact machine and full session id, never a URL, credential or
arbitrary request. The usual assistant-source check applies, so a task Agent cannot call it. Every invocation asks
for phone confirmation before the model call, including when the title was hand-written; declining or leaving the
card unanswered changes nothing. The result is the same title or coded refusal in a text JSON object. The assistant
tool is not a client dependency: iOS and other clients use the HTTP endpoint above (`agent_naming_tool.go`).

The assistant-only read tool `agent_autoname_receipt` reads the calling conversation's existing `agent_autoname`
permission records and one exact target's current naming snapshot, through the owning manager's existing stores
(`agent_naming_receipt.go`). It takes an exact machine and full session id, with an optional bounded RFC3339 window
`[since,before)` (at most 24 hours, 50 records per page, at most two pages). It never decorates the target's list, invokes a model,
writes settings, answers a card, renames or publishes. A single card plus complete coverage returns only its id,
status, `allow` and `resolved_at`; multiple cards return `ambiguous`; missing records, read errors or partial
coverage return `unknown`. Its own four-field query is not confused with the original two-field autoname input:
only the card's exact target is compared, independent of JSON field order or layout. The first history read and all
subsequent pages use the exclusive upper bound and the `(created_at,id)` cursor, including equal timestamps at the
window's lower bound. The snapshot separately reports `requested`, `title`, `named_at` and `observed_at`.
Neither is the original model call's outcome: call ids are not propagated, an existing requested name may predate
the request or belong to another caller, and `last_failure` can belong to background naming. Nothing in the result
says it is safe to retry. Ordinary coding Agents, extensions, work machines and other assistant callers cannot read
this conversation's cards. The tool is for evidence only and is not part of the iOS HTTP contract.

Cost controls: list events coalesce into one worker, dispatching at most one candidate per 30-second pass.
Queue-local one-shot wakes drain existing matches even without another list event, then stop; they do not
install a timer on any node. A successful check that keeps the name advances its interval baseline without
counting another name change, so a daily choice does not retry hourly.
Input keeps at most 2000 characters of the first user message and 4000 of recent dialogue; images and tool
payloads are excluded. Calls time out after 45 seconds and request at most 256 output tokens on every protocol.
Responses relays that refuse the output bound fail closed, without retrying unbounded. A persisted ten-minute
lease precedes dispatch; failures back off from ten minutes and stop after three consecutive failures.
Each failed attempt logs its computer/session and a static reason code (for example `no_dialogue`,
`model_call_failed`, `invalid_name`, `model_output_truncated` or `naming_timeout`), even if a subsequent title
or settings recheck discards the result. `model_not_configured` (no model set up for Sorting) is logged once
and not again until a naming call reaches a model (#10435); each session still records it. Provider error
bodies, prompts and model replies never enter these logs. The ledger retains `last_failure` until a successful naming clears it; old silent failures have no
recoverable reason. A deployed hub does not add missing provenance to old nodes: those nodes remain excluded
until an explicitly authorized compatible node update reports it.
Before committing, the worker checks settings, counters, current provenance, native title and eligibility again.

Only web settings controls are added here. iOS and Android can read effective fields from
`GET /api/v1/assistant` and send sparse `PUT /api/v1/assistant` updates with the selected assistant's
`X-Persona-Manager` header. They must check all three fields are present before showing controls,
and verify the returned manager and effective values before showing a save as confirmed.

## Model

In work terms, each session is in exactly one of three states (`proto.Session.work.phase`):

| phase | Meaning | List preview |
| --- | --- | --- |
| `working` | The host is running (`state = working`) | Keep using `activity` / `last_text` |
| `blocked` | Stopped, waiting for a **named** prerequisite (`on`) | "What it is waiting for" |
| `done` | The task is complete and delivered | One-line conclusion + artifacts |

A `blocked` prerequisite has four types (`work.on`):

| on | Examples | Who advances it |
| --- | --- | --- |
| `user` | Choose a plan, decide whether to push to beta, provide information, click an authorization link, expired login | User: Persona turns the question and options into a "Needs you" card; one tap sends the answer back to the Agent. |
| `agent` | "Wait for the neighboring Agent to merge, then rebase" | Persona: wakes it when that Agent stops. |
| `external` | Wait for deployment / CI / review / a time; background commands and `/loop` wake themselves | Persona: wakes it on schedule to check for itself (no wake needed for background commands or `/loop`). |
| `transient` | `API Error: 503 / 529 overloaded / Connection error` | Persona: automatic continuation follows the transient retry policy. |

Relationship to unified states: `state` values **do not change** (old Apps display them as before); the algorithm simply also considers work state:

| Work state (this turn) | `state` | Explanation |
| --- | --- | --- |
| `done` | `idle` | No longer occupies "Unread"; conclusion is in `work.line` and included in pushes. A turn nobody had seen is listed as Just finished (`work.just_finished`) for three hours ([agent-states.md](../protocol/agent-states.md) "Just finished", #9751). |
| `blocked` · `user`, with options | `needs_you` | Persona created a question card (`pending_interactions` increases by one). |
| `blocked` · `agent` / `external` / `transient`, with Persona waiting (`wake_at` or a target Agent) | `idle` | New Apps show "Waiting" + what it is waiting for; old Apps show Seen. |
| Not triaged, triage failed, or unclear (`unsure`) | Unchanged (`unread`) | Better to leave a green dot than hide something not understood. |

"Unread" then remains only while triage is pending (a dozen or so seconds after a turn ends) or when the model cannot tell;
what the triage found done and nobody has looked at yet waits under Just finished instead of folding away with the seen sessions.

## Responsibilities (in the manager since 2026-11-04)

Triage belongs to the **manager** (currently just one: the assistant process `personad assistant`, `server/internal/assistant/agents_*.go`). The hub is only a transport
(`hub/agents_link.go`; bus methods are in `docs/protocol/protocol.md`, "Bus" → "Agents"):

| Component | Responsibilities |
| --- | --- |
| Manager | Post-turn triage (rules + model `agent.triage`, through the hub's `app.call models.json`), tool-loop checks, automatic continuation, wakeups, cards and answers, triage switches (settings `agents.triage`), tables `session_work` / `session_work_events`, statistics; labels for new Agents (`agent.title`), and launchers / models for new and resumed Agents ("Starting" below). |
| hub | Reports what happened: turn starts / ends (`session.turn_started` / `session.turn_ended`, already sent), a user views a session in the App (`session.seen`). Displays the manager's snapshot: session-list `work` (snapshot `work`), its cards (snapshot `interactions`, displayed, pushed, and expired like assistant questions, with answers returned through `manager.action`). Messages to Agents forward to the node through `device.call session.send`. |

- **Session-list `work`**: the manager's snapshot reports each session's latest turn's work state (`work.turn_at` is when that turn ended). The hub displays it only if the session is still on that turn.
  Working (`{"phase":"working"}`), the Agent asking its own question (its cards precede the manager's), and a terminal dialog are reported by the hub from the current session state without waiting for the manager. No `work` when the main switch is off.
- **Starting**: before `POST /api/v1/sessions`, the hub asks the manager for `agents.start_params`. It fills launcher, model, thinking effort, and fingerprint from **Model Settings › Agents** (the launcher / model selected for this start override them; `docs/assistant/model-config.md`),
  then gives an unnamed tab a local `tabSlug` placeholder without calling a model. Direct, planned-task, dispatch and Lark starts follow the same rule. Once the start has a session, `agents.tab_name_started` (or the manager's direct equivalent) asks `agent.title` in a tracked background job and calls `session.rename_tab_if_current` with the expected placeholder, so a manual rename wins. An async answer with no tab id yet is resolved within that background job, never by holding the start. Because nothing waits on it, that job gives `agent.title` a bounded 30-second model budget (45 seconds for the whole job), longer than the 8-second cap the synchronous `agents.tab_name` and batch autonaming callers keep, so a cold CLI launcher and its first reasoning turn do not lose the name; a timeout or error still leaves the local placeholder and sends no rename. `agents.tab_name` remains the synchronous batch autonaming call. Before sending to a stopped session with an unknown launcher (resuming it), ask `agents.resume_params`.
  `POST /api/v1/tabs/autoname` also belongs to the manager.

**When the manager is disconnected** (the hub just started and it has not handshaken; the second or two during its restart; it crashed and is being relaunched):

- Starting an Agent or resuming a session with an unknown launcher: if the manager has never connected since hub startup, wait for it (until 60 seconds after hub startup, no longer than the request's own deadline). Requests queued on the port and App replays arrive immediately after a hub restart,
  so they still get parameters from the manager. If it connected before and is now absent, do not wait.
- If it does not arrive, the Agent cannot start: since 2026-11-04, the manager connects to computers ([machines.md](../server/machines.md), "Who connects to computers"). Without it nothing can reach the machine, so starts return `503 assistant_offline`,
  as do messages to stopped sessions (resuming them). Previously, the hub applied **Model Settings › Agents** itself and could still start them.
- Triage pauses: turn-end events are not sent to it (the hub does not queue them). Due continuations / wakeups wait until it returns, with the usual rechecks before sending. On connection / reconnection, catch up: the next check (within 15 seconds) begins triaging turns that ended during the outage,
  remain unread, and are within 24 hours (up to 3 at a time, with further batches each minute; the same applies at hub startup, because its bus opens last and nodes connect first). Do not catch up turns already seen during the outage (in the App or herdr on the computer), as before after hub restarts.
  Cards for sessions that resumed working during the outage are removed at the next check (every 15 seconds).
- The session list retains its last reported `work` (still only for sessions remaining on that turn). If a wakeup is over 5 minutes late, it no longer counts as "Waiting" (`wake_at` is removed from the list, returning to Unread).
- Its cards remain visible and answerable: the hub records the answer first and delivers it after the manager returns and reports a snapshot. If the manager temporarily cannot accept it (for example, it has not restored its state), retain it and retry after 10 seconds until it accepts,
  says the card no longer exists, or the card's expiry is reached. If the hub also restarts, this answer is lost and the card reappears.
- Switch / statistics endpoints (`/api/v1/agents/triage`, `/api/v1/agents/work/stats`, `/api/v1/tabs/autoname`) return `503 assistant_offline`.
- Manager-only restart: cards still held by the hub remain, without another push. As when triage lived in the hub, remove them only on answer, resumed Agent work, or expiry.
- Hub restart (the manager is its child process and restarts with it): for the second or two before the manager's first snapshot, the list has no `work` (triaged sessions briefly return to Unread). Cards reappear after the computer reconnects (same id, another push, as with previous hub restarts).
  Remove a card only if the Agent has progressed (resumed work or a later turn). A session merely looking different just after reconnection (seen and now idle, waiting for its own question, or a restarted node not yet having read the transcript timestamp) does not count. Retain the card and restore it at the next check (`cardBack`).
  This card also stays pending in interaction history: at startup the hub marks pending interactions left by the previous process as cancelled, except those referenced by `session_work.interaction_id` (`db.ExpireStaleInteractions`). The manager marks as cancelled only those it does not restore.
  Previously (before 2026-11-06), every hub restart removed decision cards (including safety-critical deployment and merge cards): sessions looked "seen" or "on another turn" in the first seconds after reconnection, so the manager forgot the cards, and interaction history had already marked them cancelled.

If a card is answered but the message cannot reach the Agent (its computer just disconnected), recreate the card with a new id and push again so the user can answer again (previously the App's answer attempt returned an error).
If the computer answers that it no longer has the session (`not_found`: deleted there, or listed under another id since), the card is withdrawn instead: put up again, it could never be answered (#6709).
If the manager's snapshot still reports a card whose answer was delivered to it (but not accepted), the hub restores the card after 30 seconds.

## Triage

### When

- A turn ends (working → done / idle in `setSessions`, at the same point as the "Completed" push). If it remains inactive for `triage_settle_seconds` (default 15 seconds), begin triage.
  If work resumes during that interval (a Stop hook continues it, or another message arrives immediately), do not triage that turn.
- After every manager connection and once per minute afterward, catch up stopped sessions whose turns ended within 24 hours, are still unread, and have not yet been triaged (including turns that ended after deployment or while the manager was disconnected; see "Responsibilities").
- Do not triage assistant chats (`assistant`, including AI twins and Lark /agent) or sessions outside the host (`offline`). Muted sessions are still triaged, but no cards are created.

### Rules first, then the model

Apply in order; on a match, do not call the model:

1. Pending interactions / host `blocked`: already Needs you, `blocked · user`, with the interaction's question as `line`.
2. This turn was interrupted by a model API error (`turn_error`):
   - Source: Claude Code writes the error as its last words (`API Error: …`); the hub recognizes the prefix of `last_text`. Codex does not write it as a reply. Its error is in this turn's
     `task_complete.error` (rollout) or `turn/completed` with `status: failed` + `turn.error` (app-server). The node reads and reports `turn_error` directly,
     plus `turn_error_kind` based on the Codex error type (`codex_error_info`) (#2185, "Codex error types" below).
   - Use `turn_error_kind` when present; otherwise use the text:
   - A subscription's usage limit (`usage_limit`) → an `external` wait for its reset, or a card when the reset is not known ("Usage limits" below); never `transient`;
   - Transient (408, 429, 500, 502, 503, 504, 529, `overloaded`, `Connection error`, `Connection lost mid-response`, `timed out`, `socket hang up`, `ECONNRESET`, `ECONNREFUSED`, `fetch failed`, `no eligible upstream`, `upstream`) → `transient`, subject to the automatic continuation policy below;
   - Other errors (`Failed to authenticate`, `OAuth token has expired`, `Invalid API key`, `Not logged in · Please run /login`—Claude Code's synthetic reply when not logged in—400 / 401 / 403 / 413, `invalid_request_error`, a prompt or context too long, selected model unavailable, `Request was aborted` after a user interrupt) → `blocked · user`, with a card "The model service needs your attention: <original text>" and options "Fixed, continue" / "Leave it for now". These words win when an error also has transient ones (`transientError`).

   **Codex error types** (`transcript/codex_error.go`; `server_overloaded` in rollout and `serverOverloaded` in app-server mean the same thing):

   | Codex reports | `turn_error_kind` |
   | --- | --- |
   | `server_overloaded`, `internal_server_error`, `rate_limit_exceeded` | `transient` |
   | `http_connection_failed`, `response_stream_connection_failed`, `response_stream_disconnected`, `response_too_many_failed_attempts`: no status code, or 408 / 429 / 5xx | `transient` |
   | Same types, other status codes (401, 403, 400…) | `user` |
   | `usage_limit_exceeded` | `usage_limit` ("Usage limits" below) |
   | `context_window_exceeded`, `unauthorized`, `model_not_found`, policy errors, and future new types | `user` (continuation cannot resolve them) |
   | `other`, no type | Not reported; use text |

   Stopping with Esc (`turn_aborted`, `status: interrupted`) is not an error. The node rereads a changing rollout at most every 3 seconds. If the file is written again during throttling,
   omit the cached `turn_error` (the continued turn may already have completed normally; an old error would continue it again). Better to be a few seconds late.

   **A newer confirmed success retires an obsolete model failure and its retry attention (#6221).** Confirmed success is a newer turn's `turn_duration` after at least one visible,
   non-synthetic model reply, with no newer model error in the settled transcript. Starting a turn, receiving an unclosed reply, or finding that the process exited is not enough. On that
   closing evidence the node drops an older error-only `last_text` preview (a silent BotMux turn, or one with only tools and thinking, may have no newer words), so the manager's legacy
   preview fallback cannot recreate `turn_error`. A pending retry with no such success stays answerable, and a newer failure replaces the old one. The historical reply and error items stay
   in the transcript unchanged. A later stopped row whose direct or preview-derived error is gone retires the transient retry state without waiting for model triage, including inside the timestamp tolerance
   and when a manager restart restores a card the hub kept up. The old card and wake go; a silent turn may remain work-state `unsure` rather than inventing a textual conclusion.
3. Background commands / sub-Agents still running (`background`, `subagents` contain running entries) → `external`, "N background tasks running"; do not register a wakeup (results wake it themselves).
4. `/loop` running (`loop`) → `external`, "Scheduled loop, next at HH:MM"; do not register a wakeup.

   Rules 3 and 4 give way to the turn's own closeout when it says the turn stopped on a person (#14899,
   `closeoutWaitsOnPerson`): `blocked` / `not_done` with `unblock: user` or `unblock: other`, or a `done` that asks the
   owner (below). Background work the agent left running (an old poll, say) is then no word that it goes on by itself:
   the manager uses the report directly, and the background work no longer keeps the task's turn from settling
   (`taskAgentSettled`), so its receipt, judgment and supervision signal go out as for any stop. A closeout that waits
   on the agent's own work (`unblock: agent`, no `unblock`, or none at all) keeps rules 3 and 4.
5. Structured closeout → settle the turn from the report ("Closeout" below).
6. Plain-text terminal reply → fall back to the model.

### Model

- Purpose `agent.triage`, "Session Triage" (**Settings › Model Settings**, under "Information filtering"; defaults to inheriting the inexpensive Inbox triage model).
- Input: Agent name, directory / project, the user's request for this turn (`lastTurn` ask), final reply (first and last 1500 characters), other working Agents on the same computer (name + short id), current time, and up to 20 resolved user question/answer pairs from the session's latest 100 interactions, newest first. Each question and answer is capped at 200 runes. Non-question interactions, assistant-authored answers, cancellations and deferrals (including localized canned pause/wait/hold answers) are excluded. A history read failure leaves triage retryable. This history is decision context, not permission to send an automatic wake.
- The model's input is read once per turn (#8132): a turn whose model call failed is tried again (an unread one within a day, every 10 minutes), and the retry uses what the first attempt read rather than asking the node again (only a read that failed is read again). A session not running is not triaged, and a read its runtime refuses because it would start the agent (`not_loaded`: Paseo loads an Agent it let go to read it) counts as read with nothing in it: the row's last words stand in for the reply, and the retries ask nothing either.
- JSON output (`server/internal/prompts/agents/triage.md`):

  ```json
  {"phase":"done|blocked|unsure", "on":"user|agent|external",
   "line":"At most 30 characters: conclusion or what it is waiting for",
   "questions":[{"question":"…","header":"…","options":[{"label":"…","description":"…"}],"multi_select":false}],
   "link":{"url":"https://…","minutes":10},
   "wait":{"what":"What it is waiting for","then":"What to do afterward","check_minutes":10,"agent":"<short id>","risky":false},
   "artifacts":[{"kind":"pr|commit|doc|build|url|file","ref":"…"}]}
  ```

- Decision principles in the prompt:
  - An optional offer at the end such as "Would you also like me to…?" does not mean blocked. If the task is complete, it is `done`; include the offer in the conclusion.
  - The model identifies unanswered requests for a user decision, information, or action. Arrangements, status reports, and acknowledgments are not questions. The hub keeps a non-empty `questions[].question` verbatim from the final reply, including requests without question punctuation. An authorization or login link can require user action on its own.
  - **Pushing code, deploying / releasing, sending messages / comments to others, or merging / modifying someone else's MR / PR** still receives no automatic wake unless it is approved (see "Safety boundary"). Blocking automation does not invent a request for permission.
  - If uncertain whether the task is complete, use `unsure`, not `done`.

### Where the conclusion goes

- The `session_work` table ("Storage" below), sent with the session list as `work`;
- When done: add `完成：<结论>` ("Completed: <conclusion>") to the knowledge-base project timeline for this directory (`kbAgentDone` used to record the last words of every turn; now it records the conclusion when available);
- A supervised worker's `report_to_supervisor(kind=done)` and the committed `WorkDone` also share one structured outcome writer. It appends a typed outcome, every model-extracted decision with its question and reason, and every pitfall to the project's `memory.jsonl`, deduplicated by todo, agent, turn, transcript item and per-kind index. New records carry `recorded_at` and `verification=agent_reported`; the dispatch prompt marks them as not supervisor-accepted. Ownership is the todo's live explicit project, then its unique direction's live project through `Supervision.Supervisor` / `leaderAbove`, then `ProjectsForWork(machine, cwd)`; without any, nothing is written and a title is never treated as ownership. Nothing is written (and no model is called) for a to-do whose history holds a health or sensitive space's words or whose track forbids knowledge-base writes; one whose verdict cannot be told yet waits and is retried as project timeline outcome lines are. If the model is unavailable, the original short conclusion and links are kept with no invented decisions or pitfalls. A failed append logs a warning and remains retryable; the base outcome is appended after its typed records so a partial retry cannot claim a complete batch. This record does not change the existing `log.md` conclusion. Later dispatches inject its bounded contents as described in [todos.md](todos.md) "Record conclusions on completion";
- Replace the "Completed" push body with the conclusion; the main chat's live snapshot adds "Done" and "Waiting" sections ("What Persona knows" below).

### Planned task assignments and instructions

For people delegating work from Assistant, `task_plan.prompt` contains the assignment, boundaries and new facts. Optional `context` supplies pertinent facts the manager cannot resolve. Do not copy knowledge-base project background into either field: the manager appends the matching project and direction context once, followed by the to-do's effective decisions. Set `skip_project_memory: true` on a task to omit that automatic project/direction memory for this start alone; the supplied assignment, criteria, read-only constraints, to-do decisions and framework rules remain. Omitted or false keeps the default. `agent_start` has the same per-start switch. Project text is user context, never system policy. Attachments remain last.

With a node announcing `task_instructions_v1`, the manager sends stable handoff, role, delivery and closeout rules separately as `task_instructions`. A positively identified Claude launcher forwards an append-system-prompt file; node state and Herdr's restore command retain that file. Its SessionStart hook does not ask for a duplicate closeout already supplied by the task. A clean native Codex launch uses the developer-instructions config channel and retains it for resume. Configured Codex profiles, project/home config files, remote daemons, uncertain wrappers and overlong restore commands use a first-message fallback, preserving their existing policy. Runtimes and desktop launchers likewise receive required rules after the assignment. If a previously dedicated Claude policy cannot be restored by a newly selected launcher, an explicit continuation carries it once; a bare Resume is refused before creating a terminal, with instructions to send a continuation or choose the original launcher. Older nodes receive the combined first message; no required rule is silently dropped. Historical sessions keep their original instructions and closeout parsing.

## Closeout

A closeout is a small record an Agent ends a turn with, so that a stopped session answers "done or not, merged or not, deployed or not, what is missing" without anyone reading its transcript or asking it (#3612). It is the Agent's own claim; nothing in it has been checked by the hub.

On a node that announces `closeout.markdown.v1`, the Agent writes an ordinary Markdown report. A terminal shows the heading and fields directly. This is a complete example:

```markdown
### Persona work report

- Status: blocked
- Merged: no
- Deployed: n/a
- Did: Checked three of the four sample entries.
- Artifact: doc notes/review.md
- Verified: Three entries matched the checklist.
- Missing: The fourth sample entry.
- Waiting: The sample owner will supply the fourth entry.

---
```

The other supported headings are `### Persona 工作报告` and `### Persona 作業報告`. Each report ends the reply with a standalone `---`, after its required Status, Merged and Deployed lines, in that order. Optional list fields are Did, Artifact, Delivery, Unblock, Verified, Missing, Waiting and Next. On a completed turn with no outstanding need, a single `- Next: Shortlist the three candidates.` line offers that reply in the composer. It passes through the older-client projection and leaves the work state done. Field names and enumerated values stay fixed across languages. Ordinary headings/lists, quotations, code examples, reports without a closing rule and reports followed by further remarks remain ordinary text. The runtime's trailing citation footer may follow a report. Explicit delivery still needs the latest task-owned, complete assistant reply and its unambiguous PR artifact. A document-only completion still needs an artifact and explicit `n/a` for both merge and deployment.

Archived XML records remain readable. A destination with unknown capability, including an older node, receives the following form:

```text
<closeout status="done|not_done|blocked" merged="yes|no|n/a" deployed="yes|no|n/a">
did: <what changed, one sentence; up to three such lines>
artifact: <pr|commit|branch|doc|build|url|file> <reference>
delivery: <live|pr|merged|off|none; only when the task asks for delivery>
unblock: <user|agent|other; only with a delivery gate>
verified: <what was run or measured and its reading, or "untested">
missing: <what it still needs from someone, if anything>
waiting: <for status="blocked": who or what it waits for>
next: <optional on done: one short reply naming the proposed action in the owner's language>
</closeout>
```

- **Reading it** (`server/internal/closeout`): the last block in the final reply counts. The attribute names, the line keys and the status values are fixed; the text after each colon is in the user's language. A block with no status, or one that is not `done`, `not_done` or `blocked`, is no closeout. Bounds: three `did` lines of 200 characters, five artifacts, 300 characters for `verified` and `missing`, 120 for `waiting`. `n/a` and an unsaid `merged` / `deployed` are left empty.
- **External waits**: before an idle supervision wake, a current stopped Agent's `blocked` closeout can be automatically parked when its `waiting` and source evidence meet the existing parking contract. Dates and named deployment/event sources can be read directly; an optional `park` JSON line carries the existing typed contact/review evidence, never a fabricated sent request. See `docs/assistant/todos.md`, "Automatic closeout admission", for admission, the third-attempt escalation, existing event/deadline wakes and the daily supervisor summary.
- **Optional next step** (#11874). An Agent that has finished and offers follow-up work may add one `next` line in the owner's voice, for example `next: Shortlist the three candidates.` A complete final report with status `done` and no outstanding `missing` or `waiting` value can supply the suggested reply above the empty composer. The owner can fill it in, edit it, send it, or leave it unanswered. The line is optional and limited to 180 Unicode characters. Duplicate, multiline and overlong values are discarded. A question in ordinary prose does not produce a report-based suggestion.
- **Asking for it** (the `closeout` switch, off by default): when an Agent's Stop hook fires, the node holds the stop once (`decision: block`, `server/internal/node/closeout_stop.go`) if all of these hold: the session has Persona's own hooks (sessions the user started in a terminal only report and are never held); it is not an assistant conversation, a subagent or a review's reviewer; the stop is not already a held one (`stop_hook_active`); the last reply is not empty, not a model API error and has no closeout; and the turn changed files or ran commands (a `tool_use` of `Edit`, `Write`, `MultiEdit`, `NotebookEdit` or `Bash` since the turn's prompt, read from the last 2 MiB of the transcript). The node then asks the manager (`agents.closeout.check`, 5 seconds), which answers with the reason only while the main switch and `closeout` are on (`server/internal/assistant/agents_closeout.go`, prompt `prompts/agents/closeout_required.md`). The Agent is told to repeat its conclusion with a closeout and to start nothing new. A turn that cannot be read, a manager that does not answer, and an Agent that ignores the request all end as before: at most one held stop per turn. The work event `closeout_asked` records each one.
- **Telling it up front** (the same switch; `server/internal/assistant/agents_closeout_brief.go`, prompt `prompts/agents/closeout_brief.md`, which shares its filling rules with the held stop's through `prompts/agents/closeout_rules.md`): a held stop costs the Agent one more turn, so an Agent is told the form before it works, in two places.
  - *With its task.* The prompts of `task_plan`, `agent_start` and `dispatch_start` end with the form and with when to write it: at the very end of the final reply of every turn that changed files or ran commands. A turn that only answered needs none, except one that ends with a question that is only conversation or an offer: it writes `status="done"` with no `missing`, so the turn is not read as awaiting the user's reply (#4110, "Awaiting your reply" below). The held stop and the status question still cover only turns that did work. This is part of the prompt, so it reaches every runtime, Codex included. When a planned task also asks for the delivery standard (`docs/assistant/todos.md` "Delivery"), new replies use only this block: `delivery` records live, pr, merged, off or none; `missing` records the gate, `unblock` its responsible party and `waiting` the concrete dependency. `merged` and `deployed` still record facts, so deployed-but-disabled is `deployed="yes"` with `delivery: off`. Historical delivery lines and blocks without these fields remain readable; an explicit historical delivery line keeps its precedence.
  - *As a session starts.* A Claude Code session with Persona's own hooks is given the form as the SessionStart hook's `additionalContext` (`server/internal/node/closeout_start.go`): an Agent opened from an app with no task prompt, a resumed one, and one whose context was compacted or cleared. The node asks the manager once per session (`agents.closeout.brief`, 1.5 seconds) and keeps the answer, so later starts of that session ask nothing; a manager that does not answer in time tells nothing, and the session then learns the form only if a stop of its is held. Nothing is asked for a session that only reports (its hook is async and cannot answer), one outside an engine pane, a subagent, or an assistant conversation; the manager answers nothing while a switch is off and for a review's reviewer (by its session once linked, by the review's worktree before). A task's Agent on Claude Code is therefore told twice, in the same words; the session's copy does not mention the delivery line.
  - Sessions the user started in a terminal, and Codex sessions started without a task prompt, are told nothing up front: the first only report, and Persona neither holds a Codex stop nor adds starting context through Codex hooks (`docs/integrations/codex.md`, "Closeout").
- **Triage** settles a structured closeout by rule. A completed turn shows the first `did` line. A `done` report with
  `unblock: user`, a `waiting` or `missing` line, and `delivery` none or absent still waits on the owner (#14899).
  A delivery hold reaches the owner through the delivery workflow. An unfinished report that explicitly names
  the owner can supply the owner card from its `waiting` or `missing` line. An unfinished report that asks someone
  else to act shows an external dependency, as does an older `blocked` report with a `waiting` line. A closeout
  records these dependencies without scheduling a wakeup.
- **Awaiting your reply** (#4110): a final reply whose closeout says `done` with no `missing` and no `waiting` line asks the user nothing (`closeout.WaitsOnNobody`, read by `closeout.AskOf`), however it ends, so the session is not `awaits_reply` even before triage runs or with triage off; triage already calls the same turn done by rule. Any other closeout, or none, leaves the reply's words to decide as before (`../protocol/agent-states.md` "Awaiting your reply").
- **Not finished** is the list state of an incomplete closeout without a concrete wait, or one whose merge or deployment claim the hub disproved ("Checked by the hub"). Its work state is published with an empty `phase`, the closeout, and a line in the user's language (`closeoutLine`): "Not finished: …" with what the closeout says is missing, else what it waits for, else its first `did` line; for a disproved claim, which claim is not so ("Says merged, but the pull request is not merged"). The session's `state` stays Unread. Why an empty phase and not a fourth value: every client already draws a `work` with a line and a phase that is neither `working` nor `done` as that plain line, with no check mark and no wait (web `workLine`, Android `Session.workLine`, Apple `Session.workLine`), and counts as waiting only a `blocked` phase; a new value would reach apps that type `phase` as `working | blocked | done`, and `done` or `blocked` would move the row out of Unread or start a wait nobody ends. A turn without a closeout that the model cannot read is still not published. Stats count both as `unsure`.
- **Asking afterwards** (the `closeout_ask` switch, off by default, #3616): the held stop does not reach every turn (a runtime whose hook cannot hold a stop, a session without Persona's hooks, an Agent that ignored the request). When triage finishes for such a turn, the manager schedules one status question (`server/internal/assistant/agents_closeout_ask.go`, prompt `prompts/agents/closeout_ask.md`) if all of these hold: the main switch and `closeout_ask` are on; the final reply has no closeout; the result is unsure, or `done` on the model's word with no artifact; the turn did work (an edit, a command or a subagent, as `turnDidWork` reads the turn's items) and the user did not stop it; the turn has no wakeup or card of its own; and the session is an Agent's own that nobody follows: not an assistant conversation, a review's reviewer, a session owned by a source, an archived or read-only one, not on a to-do with a supervisor (the supervisor follows it), and not on a completed to-do without an open one. The question asks where the work already done stands, in the closeout form, and tells the Agent to start nothing new and not to push, merge, deploy or message anyone. The tick sends it after the rechecks every automatic action gets ("Avoid conflicting with manual user actions"), and checks again that nobody supervises the Agent; a turn older than a day is not asked. It is asked once per turn: the turn's `session_work` row holds `closeout_ask` (`pending`, then `sent`, written before the message goes), so a manager restart neither loses a scheduled question nor repeats a sent one. The turn that answers is triaged as any turn, so its closeout becomes the list line; it is never asked again, even when it has no closeout either (a transient error that cuts the answer off passes this on to the turn that continues it). Work events: `closeout_ask_sent`, then `closeout_ask_answered` when the next turn carried a closeout or `closeout_ask_ignored` when it did not. Only an Agent Persona started is asked: nothing is typed unasked into a session the user started by hand in a terminal.
- **Where it goes**: `work.closeout` on the session ("Protocol" below). The session lists carry only where it stands (`status`, `merged`, `deployed`, `waiting`, `missing`); `did` and `verified` come with the session itself.
- **Supervision messages** carry the closeout of a task's Agent as its own lines, marked as the Agent's claim that nobody checked, with the block taken out of "what it said last" (`supervise_digest.go`, `prompts/assistant/supervise.md`); the supervisor is told to verify merged, deployed and verified as for any report.
- **Checked by the hub** (`checkCloseout`): when the closeout names a GitHub pull request by its link, triage looks at it with the deploy check that deliveries use (`deploy_check.go`: the pull request's merge, and whether the running hub contains the merge commit) and keeps the result beside the claim as `closeout.checked {merged, deployed}`, each `yes`, `no` or absent when it could not tell. `deployed` is about the running hub, so it is known only for the hub's own repository; for another repository only `merged` is. A closeout that says merged or deployed when the check says it is not is never `done`: the turn is "Not finished" (above), Unread with the closeout and the finding kept and a line naming the claim. Supervision messages show the finding under the closeout.
- **Snapshot**: in the main chat's live snapshot an Agent's line ends with what its closeout claims of merging and deploying, marked as its own account, and with the hub's finding where that differs from the claim (`prompts/assistant/snapshot_closeout.md`, `snapshotCloseout`); nothing is added when the closeout says neither. A turn that is not finished is under "Unread" with its line.
- **Delivery**: a reply with a closeout and no delivery line of its own (`docs/assistant/todos.md` "Delivery") states its delivery through the closeout: deployed `yes` is live, merged `yes` is merged and not live, merged `no` with a pull request is held at the PR, and a done closeout that says neither is "nothing to ship". A delivery line in the same reply wins.
- **Owner holds go to the supervisor first** (#13191): a closeout's `unblock: user` (or the done judge's) records a delivery hold for the owner, but on a supervised task it is often only an announcement ("the owner approves the test send after the deploy") with nothing to answer yet. While the task has a supervisor and no review came after the hold, the to-do's `delivery.unblock` reads `supervisor` (`currentDelivery`, `server/internal/assistant/delivery.go`): not the owner's wait, not in Needs you, not among the snapshot's approvals and not `owner_wait` in `tasks_snapshot`. The apps list it with the tasks in progress as its supervisor's review (the web under Persona reviewing). After `deliveryHoldSettle` (10 minutes) the supervisor gets one `delivery_hold` signal (`supervise_delivery_hold.go`) to open a `needs_user` decision, park the wait or send the work on; the mark (`supervisor_told`, stored with the hold in `assistant.todo_stages`, never on the wire) keeps a restart or the same hold reported again from telling it twice, and a hold recorded after a later review is told anew. The supervisor's `needs_user` makes the hold the owner's again (`unblock: user`); a park on someone else, an event or a date keeps today's `other`. An unsupervised task keeps its owner hold (#10787): its Needs you row says "Finished, waiting for your OK to ship" (not the permission card's words), with **Open task**, the pull-request link when one is known, and a two-step **Mark done** that leaves **Undo** on the toast.
- **Not done yet** (follow-ups of #3612): the web (`docs/web/web.md`, "Pages": **Closeout**; the web and Android also have `closeout_ask`, #3616), Android (`docs/android/android.md`, "Closeout, decisions and tasks nobody works on") and the iOS app ("Clients" below) draw the closeout and have the switch, the Mac app and the watch do not; only Android gives a turn that is not finished a mark of its own (on the web and iOS it shows as the plain line of an Unread row, with a claim the hub found untrue marked in the suffix); Codex has no held stop and no starting context, so a Codex session started without a task prompt writes a closeout only if it is asked to (one it writes is read all the same); a session already running when the switch is turned on learns the form at its next start or held stop; the form reaches a task's Agent on Claude Code twice (prompt and starting context).

## Files a task hands over (#10511)

A task whose result is a file — a report, a PDF, an image, an archive — must not have its content pasted into the reply: the conversation sees a clipped copy of the text (1500 characters in a quiet task's notice, 600 in the receipt, 24 KiB in a paged item), and a binary file cannot be pasted at all.

- **Asking for it** (always, independent of the closeout switch): every task prompt ends with `prompts/agents/task_files.md` (started by `startTaskAgent` and `startTaskSuccessor`), which asks the Agent to write one `file: <absolute path>` line per file (at most five) in its final reply, and never to paste the file's content. A closeout may name the same file as `artifact: file <path>`.
- **Copying it** (`server/internal/assistant/task_files.go`): when a task's turn ends, `taskTurnSettled` reads the whole final reply (the paged item again with `session.item` when the node cut it), takes the paths, and copies each from the Agent's computer to the computer of the conversation that hears of the task (`taskReceiver`: its supervisor, the reporting conversation that opened it, the main conversation for a quiet one, else its origin). It uses the same node RPCs as the user's attachments in the other direction: `file.chunk` (the node's path guard refuses credentials, iCloud and the hub's own secrets) and `file.upload` with `dest=file`, which lands in `uploads/files/<date>/` and is kept, not swept (`dest=terminal` when the node is older). The same computer copies nothing. Limits: explicit paths only, regular files, 50 MB each and 100 MB a turn, five a turn; a copy that fails says why in the notice and never fails the task. The copy is the same trust as the reply it comes with: an Agent can already name any file it can read, and the read guard is what stops a credential from coming along.
- **Telling the conversation**: the task notice (`prompts/assistant/task_notice.md`), the receipt body (`files`) and the supervision message (`prompts/assistant/supervise.md`) list the files by name and path on the conversation's computer, outside the text clip. The assistant is told to give the user the path in backticks and to read the file itself when it needs the content; never to ask the Agent to paste it or resend it in pieces.
- **A file it did not hand over**: ask the Agent for it with `file: <path>` (its next reply carries the line, and the copy happens then) — never for the file's content, which would be clipped in the reply. An on-demand tool for pulling a path the Agent already named is a follow-up: it must free its prompt bytes from the assistant's instructions, which the size ratchet keeps from growing.
- **Clients** need no change: the copy is on the conversation's computer, so the existing backticked-path links (web, iOS, Android) open it in the app's file viewer.

## Automatic actions

An Agent still linked to a completed to-do, with no open to-do linked to it, receives no automatic continuation,
wakeup or triage card. Triage can record its work state, but checks the current to-do links before acting; pending
wakes are checked again before sending. Released and dismissed links do not count. A later manual message is allowed;
to resume follow-ups while keeping the completed link, reopen the to-do or link the Agent to an open to-do (see
`docs/assistant/todos.md`, "Stop Agents of completed tasks"). If the to-do state cannot be read, triage and wakes retry
later without sending.

### Transient errors: automatic continuation

- An unrecoverable conversation error overrides a runtime's generic transient kind and any upstream status.
  Account-sealed native compaction is `turn_error_code: compaction_sealed`, `turn_error_kind: unrecoverable`:
  the context belongs to another account, so the conversation is invalid. The wire kind or code is sufficient even
  when the raw error text is unrecognized: every task row uses the same invalid label and full reason, directs the
  owner to a new conversation and omits Continue. The manager neither schedules another retry nor
  sends a retry queued before the cause was recognized, including the timed-out continuation path. Adding permanent causes extends
  `proto.UnrecoverableTurnErrorCode` and the clients' error parsers, with positive and negative regression cases.
- An old interrupted participant does not override another participant that is working or waiting to resume, including
  a participant waiting on agent/external/transient work without background activity. Interrupted task rows stay out
  of Needs you when the task is parked or closed. Archive visibility still follows
  new activity; it is not a substitute for the task-state filter.
- On by default and independent of the main switch (`transient_retry`, #3208): it is a rule, with no triage model call. With the main switch off, only
  turns that a transient error ended are followed (`workActive`), and the lists show only the continuation on its way (`transientPending`); everything else stays as without triage.
- Assistant conversations and managed Agents share the manager's durable due-wake scheduler (Refs #10444). Temporary network failures, timeouts, throttling and recoverable server errors continue indefinitely until cancelled, with no attempt budget. Authentication, permissions, invalid requests, missing models, usage limits, user decisions and repeated-tool-loop stops never enter this outage retry loop. Existing bounded unavailable-model, safeguards-refusal, gateway-quota and tool-search recovery keeps its own behavior.
- For every session, explicit upstream account-pool exhaustion has a separate opt-in budget. Coding Agents, task-leader Agents and every Assistant conversation use the same policy: main chat, leader conversations, background supervision, side conversations, scheduled-task and health conversations, and meeting-room conversations including guests. Only main-chat, leader-conversation and background-supervision protected continuations re-resolve the configured launcher and model. Matching recognizes only these case-insensitive substrings: `no other eligible account`, `no eligible upstream`, `no other eligible upstream`, `upstream attempt budget is exhausted`, and `upstream attempt budget exhausted`. Unrecognized messages remain ordinary transient errors.
- `agents.triage.account_exhaustion_max_retries` in the advanced manager configuration defaults to `0`, which sends no automatic continuation for this class. A positive value permits account-exhaustion continuations while the existing consecutive transient retry count (continuations already sent) is below that bound. Earlier ordinary transient sends therefore count if a later failure in the same sequence is account exhaustion, although ordinary transient failures themselves remain unbounded by this setting. Any turn that does not end in a transient error resets the count; a turn that does work and then ends in a transient error does not, and neither does a manual Continue. Resetting the setting to `0` restores the default and cancels a pending continuation at its due recheck. The current error and budget are checked both when scheduling and immediately before sending.
- When this policy stops a session, the session retains `blocked/transient`, the original model error and no wake or retry countdown. For a coding Agent, the Agents list leaves its wire `state` unread or idle, a linked to-do reads as interrupted and waiting for the user to continue it, and dispatch supervision writes its interrupted receipt with the original error and Continue rather than waiting for another retry (its notification still goes out). Manual Retry and explicit Continue are unchanged and are not blocked by the budget. Stopping uses the existing `transient_retry`-off state; it does not create a decision card when `decide` is on. Primary screens have no new setting.
- **Persistent upstream 5xx / account-pool stall visibility** (#13372): a single non-pool-exhaustion `5xx` turn error keeps `turn_error_code` empty so ordinary transient retries run without noise. When a turn explicitly reports upstream account-pool exhaustion (`modelretry.AccountExhausted`) or a session accumulates `>= 2` consecutive upstream `5xx` failed turns with no substantive assistant work (reply or tool execution) in between, `turn_error_code` becomes `upstream_unavailable` while `turn_error_kind` stays transient and wire `state` stays `unread` / `idle`. Nodes compute the consecutive streak from Claude Code and Codex transcripts (including incremental `bgScan` across large request blobs and terminal rewind replay in `Tail.Settle`, surviving hub restarts), and the hub maintains a bounded 48-hour fallback over observed stopped rows for rolling upgrades and non-transcript sessions; any substantive assistant work or non-upstream outcome resets the streak immediately. Stalled rows surface in `Needs you` in the Assistant live snapshot (with distinct retrying vs stopped wording and the consecutive turn count when `>= 2`) and in `agents_list` (`active` and `needs_you`, labeled `上游不可用` with `· upstream_unavailable` and `attempts=N`), including external `herdr` `/loop` sessions.
- Exponential backoff uses a 15-second base (`retry_base_seconds`), doubled per failed continuation; throttling starts at four times the base. Equal jitter selects between half of the exponential ceiling and that ceiling, capped at 30 minutes. The legacy `max_retries` configuration remains readable but no longer limits temporary model failures. Any turn that does not end in a transient error resets the count.
- Send `继续` ("Continue") into the existing session history, without resubmitting the original task or its completed tool calls. Every hub-generated transient or usage-limit continuation, including the default continuation sent with a launcher switch, carries `message_source.kind=auto-continue` and the existing persisted `message-source` context, so it is not mistaken for the owner's bare Continue. Manual Retry, explicit Continue and custom launcher-switch text remain unmarked. Due timestamps persist in `session_work`; waiting uses the existing manager tick, without a waiting execution subprocess. Old pending retries survive database loading and pruning.
- Stop and a bare `/exit` or `/quit` retire the pending wake durably. The hub reports every known session's interrupt to the manager, and node guards reject known stop causes that forbid supervisor resume. New input, changed turns, queued input and pending interactions prevent automatic continuation. Concurrent ticks share a session claim. A stable `client_msg_id` and restored transcript receipt prevent repeating a delivered continuation; runtime tool history remains the authority for completed side effects.
- Herdr can report a task Agent idle after passively observing its failed turn. For an Agent linked to an open to-do, that observation does not cancel its mechanical transient retry; it still cancels every other wake, and the ordinary-session behavior is unchanged.
- Retry IDs normalize turn timestamps to PostgreSQL microsecond precision before the first send. A manager crash after delivery but before acknowledgement therefore reuses the same ID after loading the pending row. Hub receipts in `sending` or `unknown` remain blocked across restarts; the scheduler retains the pending wake, but cannot infer delivery or automatically resolve an uncertain receipt.
- `TestModelRetryProcessRestart` uses a disposable PostgreSQL database and separate test processes for Assistant and managed Agents: pending sends, delivery before acknowledgement, unknown delivery, process exit during sending, repeated concurrent due events and Stop followed by restart. `TestSendOnceProcessRestart` separately exercises production hub send guards with a fake RPC node. These probes do not exercise a real model provider or app UI.
- The manager logs the attempt and delay when scheduling and sending. An attempt count is diagnostic only, never a reason to ask the user to continue.

### Usage limits: continue at the reset

A subscription's usage limit gets no backoff "Continue" (each would be refused at once): Persona continues the work about a minute after the limit resets, unless the CLI goes on by itself ("Usage limits" below).

### Waiting: scheduled wakeups

- `external`: register `wake_at` using the model's `check_minutes` (2 minutes to 4 hours, default 10 minutes); double the interval for each further wait (up to 1 hour), with at most 6 wakeups (`max_wakes`).
  When the wait itself names CI and a GitHub PR is identifiable, Persona first polls its checks without another model turn. The active GitHub connector reads the PR head's check runs and commit statuses, including all pages, and checks the head again; the node's current-branch PR reader is the fallback when it can prove the result complete and the PR matches. Empty, incomplete, or still-running checks never count as complete; an empty set that persists until the original scheduled wake falls back to that wake. Rate limits defer polling, and source errors are retried with backoff; after three consecutive attempts where no source yields usable checks, the ordinary scheduled wake takes over. A valid pending check or explicit rate limit resets that error count and remains monitored. After all reported checks finish, Persona waits two minutes and verifies the same check set on the same head before waking the agent. This observation window reduces early wakes, but a provider can register checks later and no finite window proves it will not. Polling backs off to a capped interval and continues while checks are available, including after a long offline period; it falls back to the scheduled wake if neither source can read the PR; work machines and guest conversations use scheduled wakes to preserve their privacy boundary. The wake contains a localized result and bounded, escaped failing check names explicitly marked as untrusted data, without logs. The watch and its pending message are persisted, and the hub's once-only send guard avoids duplicate messages on retry; an uncertain delivery is not resent. A new turn, a user message or viewing that turn in an app cancels it, and the wake switch controls it.
  For other waits, or when no check source works, Persona sends a wakeup message (`prompts/agents/wake.md`): what the previous turn said it was waiting for, what to do afterward, and a request to check for itself. If ready, continue; if not, say how much longer.
  Triage the resulting turn as usual; register again if still waiting. After 6 wakeups without completion, show a card "Still waiting for 'X' after 6 wakeups", with options "Keep waiting" / "Stop waiting".
- `agent`: the model identifies the target among the working Agents shown to it (short id). Wake this Agent when the target's turn ends (or it goes offline), with `check_minutes` as a fallback.

A GitHub extension may opt into machine-readable polling with the read tool `github_ci_snapshot_v1`, called with `{"repo":"owner/repo","number":12}`. Its JSON response has `version: 1`, `host: "github.com"`, matching `repo` and `number`, `head` and `head_after` containing the same full commit hash, `complete: true`, and both `check_runs` and `statuses` arrays. Each check run has `id`, `name`, `status`, and `conclusion`; each commit status has `context` and `state`. The extension must read every page of both sources, then verify the head again; it must not include logs. Unknown versions or fields, missing arrays, duplicate items, mismatched targets or heads, and incomplete results are rejected. An extension offering only the existing human-readable `github_checks` does not provide this contract: Persona uses the node fallback or scheduled wake instead.

### User decisions: cards

- Persona creates a `question` interaction (`tool_name: persona_triage`, `summary` is `line`; the manager reports it in snapshot `interactions`, and the hub displays, pushes, and expires it accordingly), using the model's questions and options. The App, web, Android, Watch, Lark cards, Inbox's "Agents waiting for you", and main-chat task dock already render and answer them; no client changes needed.
  Push is time-sensitive, like other questions.
- Normal selections (or typing an "Other answer") reach the manager through `manager.action`, which formats them as a message (`prompts/agents/answer.md`: `关于你上面的问题：Q → A`, "Regarding your question above: Q → A"), and sends it to that Agent with `session.send`.
- Triage cards advertise `can_cancel:true`. Closing one commits its turn as deferred and clears its scheduled wakeup without sending an Agent message or interrupting the task. A model-generated single-choice option with `action:"defer"` has the same effect when selected; its label remains the answer stored in interaction history. Only options actually offered on the card carry this intent, not arbitrary free text. Legacy persisted pause labels have a narrow compatibility path.
- A deferred turn stays quiet after refresh, reconnect, and manager restart; a genuinely new turn can ask a new question. Repeated owner snapshots replay the original terminal outcome instead of registering the same question again. Persistence failures remain retryable rather than acknowledging a durable close.
- For authorization links (`link`), the auth relay goes first ([auth-relay.md](../operations/auth-relay.md)): when an extension's `auth_relay` contribution names the link and it was printed by the Agent's own command on one of your computers, the extension completes it with the account it is signed in with, the Agent is told to go on and no card goes up. Otherwise the card's first question contains the link and validity period (plus one line on why Persona didn't do it, when it tried), with options "Authorized, continue" / "Not now". Push as soon as triage finishes (15 seconds after the turn stops plus a second or two for the model), not after the 60-second "Completed" push delay.
- Remove a card (without sending anything) when the Agent resumes work (you replied directly on the computer or in the App), a new turn starts, the session disappears, or 24 hours pass without an answer.
  - *A new turn* includes a later turn whose start Persona did not hear of: its triage replaces the session's earlier card, so a session has one card at a time.
  - *The session disappears* means it has been missing from its computer's list for 10 minutes while the computer is online and listing, or its computer refused an answer because it no longer has the session (#6709). The wait covers a node, an engine's bridge or the engine itself coming back and listing the session again. An engine away for longer takes its sessions' cards with it, and the turn's work line stays on the session for when it is back. Before #6709 a Paseo Agent's id moved with each fork Paseo made of its Claude session, and every API error left a card under an id nothing listed any more ([paseo.md](../integrations/paseo.md) "Session IDs").

## Repeated tool calls

The optional `tool_loop` triage switch is off by default and requires the main switch. It can be set as an initial value in `manager.json` under `agents.triage` and is exposed in the triage settings API. While an agent is working, its node reports a candidate containing only the tool name, repetition count and start time. The manager asks the Session Triage model at most three times per turn, with a five-second timeout; an error, timeout or unrecognized answer leaves the agent running. At 30 repetitions it asks the user without another model call. The node excludes recognized waits, terminal-output polls, log-tail reads, CI status checks and calls spaced 30 seconds or more apart; other polling forms may not be recognized. For shell commands, every component must be recognized as waiting or observation; commands with shell expansion or redirection remain eligible for triage. To bound memory, the node scans at most an 8 MiB transcript tail and skips lines larger than that limit; large calls or rapid writes can therefore be missed. The question offers **Interrupt** and **Let it run**; the latter silences the current candidate. The card, the silence and the model-check budget are saved separately from `session_work` and restored after a manager or hub restart. The first working session list after a manager restart, or a working list after an offline period, is an observation of the current turn and does not by itself clear its restored card or budget. A temporarily unavailable node or missing candidate does not by itself withdraw a restored card. Once this manager has seen the same candidate and its transcript revision, a larger revision with no candidate withdraws the old card. After reconnection, an unconfirmed missing candidate is retained; two subsequent revision increases with no candidate withdraw the card but keep the same-tool silence. The revision is the transcript's size, so unrelated transcript writes can also advance it; without an explicit node scan-completion signal this distinction remains an approximation. After reconnection or manager restart, a transcript tail rescan can change the candidate's start time. If a previously asked or silenced candidate comes back with the same tool and a different start time, Persona withdraws any old card and stays quiet until a new turn or different tool makes the change unambiguous. This can suppress a genuinely new same-tool loop in the same turn, because the summary does not contain enough information to distinguish it from a rescan. A new message, queued input or turn removes the card. A user's choice is saved before it is acknowledged; on a storage failure the card stays retryable. An interrupt is marked consumed before the existing `session.interrupt` call, so an uncertain delivery is not automatically repeated after a restart. If the request fails while the same candidate remains, the question reappears with a new id: a repeated delivery of the old answer cannot interrupt again, and the user can retry. The manager rechecks the candidate immediately before interrupting, but direct terminal input can still start another turn in the short interval before the runtime receives the interrupt. Older clients can update other switches without resetting this one.

## Usage limits

A subscription's 5-hour or weekly usage limit (#1710) is no failure to report and nothing for the user to fix: the turn waits for the limit to reset, then goes on.

- **Where the facts come from** (`proto.Session`: `turn_error_kind: "usage_limit"`, `turn_error_resets_at`, `turn_error_resumes`, `turn_error_after_work`): the agent's own record first (Claude Code's refused reply with `error: "rate_limit"` and `quotaLimits.resetsAt`; the reset of a Codex rollout's own rate-limit window), the words as the fallback for an older node or CLI (`transcript.UsageLimitText` / `UsageLimitReset`, completed on the session lists by `decorateLimit`): Claude Code's "You've hit your … limit · resets 4:20pm (Area/City)". Claude Code writes the time alone when the reset is at most 24 hours off: one not after the line is tomorrow's, except on a 5-hour limit's line, where a tomorrow more than 5 hours off means the reset that has just passed, so the limit is over (#9386). A time without a time zone is never read (Codex's "try again at 3:05 PM" is some machine's local time), and a subagent's limit line (`isSidechain`) says nothing of the session.
- **Gateway quota waits** (#12095): the anchored error `Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at …` (optionally prefixed by `API Error:`) is also `usage_limit`. Its `HH:MM UTC` reset is placed relative to the error timestamp (tomorrow if that clock has passed); `YYYY-MM-DD HH:MM UTC` is read literally. Invalid/missing times give no reset. The capacity error `Request rejected (429) · upstream account capacity is temporarily unavailable` is `transient`, with no reset. Other gateway/API-key 429s keep their existing backoff policy.
- **An exited Agent** with either recognized gateway error gets node-owned stop provenance `usage_limit` or `transient_error` only when the node saw it running after that error and its original Persona terminal remains. History first found after the process or terminal disappeared stays unknown and owner-only. Closing or replacing that terminal also revokes a previously listed gateway resume. Existing `agent_send` with `resume: true`, `resume_why` and supervision may resume the same session on its original launcher and permission mode; it no longer requires the owner's terminal-page Resume. This does not enable plain sends or automatic relays to an exited Agent. Still-running Agents use the existing continuation policy below. Explicit user stops and exits always win; consuming resume permission prevents an old transcript error from authorizing another uncertain start.
- **Triage** (a rule, before transient errors): with its reset known, `blocked · external` with `resets_at` and no `wait` (the error is the session's `turn_error`; an external wait with nothing named is one that ends by itself, as every client's list reads it); the line says when: "continues by itself at 16:20" when the CLI said so, else "resets at 16:20" (the user's time zone; with month and day, `01-02 16:20`, when the reset is on another day). Without a known reset: a card, "The subscription's usage limit is used up, and it didn't say when it resets: …", with options "Try again" / "Leave it". Never `transient`.
- **The CLI goes on by itself** (Claude Code's "Usage limit reached · continuing automatically at …"): no wake and no card, also when its reset is not known (the line then says it goes on after the reset). Typing into the session would cancel its own continuation. Claude Code writes the notice once, at the first refused turn; the node keeps its word while nothing in the transcript's tail took it back (a message typed, a reply, its "Automatic continue …" notices), however far the wait's request lines push the notice out of the tail it reads.
- **Otherwise Persona continues it** about a minute after the reset, spread over 30 seconds so that the agents of one account do not all start in the same second (`limitContinueAt`), with `prompts/agents/limit_continue.md`; event `limit_continued`. It is the `auto_continue` switch's, not `wake`'s: off (or turned off before the continuation went), the line stays and nothing is sent; the lists then name the line as the wait's `wait`, so they keep the session for the user instead of showing a wait that ends by itself. The apps show the continuation as one line ("Usage limit reset: Persona told it to go on", `injected` state `limit_continue`). None of a wait's other checks apply (risky next step, `check_minutes`): it goes on with what the limit stopped. Right before sending, the session is read again: when it now says it goes on by itself, nothing is sent; when its limit now resets later, the continuation moves to then.
- **At most 3 continuations without a productive turn** (`maxLimitContinues`): a reset read too early is refused again at once. Then a card, "It was continued automatically 3 times after the usage limit reset, and hit the limit again each time", with options "Try again" / "Leave it"; the card keeps `resets_at`, so a turn refused again while it is up (a background task's report woke it) is the user's too. A turn that did some work before the limit refused it again (`turn_error_after_work`), or a later reset (the next window: the earlier reset was read right), starts the count again; an ordinary external wait never takes over a usage limit's count, nor the other way round.
- **The user's actions cancel it** as any automatic action ("Avoid conflicting with manual user actions"): a message, looking at it in herdr, queued input, a pending card.
- **Moving it to another provider instead of waiting** (`agent_switch`, #5973): when the user asks, the main conversation moves a Claude Code agent its limit stopped to another launcher on its computer and sends it `prompts/agents/limit_continue.md` there, at once ([claude-integration.md](../integrations/claude-integration.md) "Switching a running Agent to another provider"). It is now or not at all: a busy agent is refused with nothing typed and no switch left waiting. The continuation that was due is not sent afterwards: the agent's turn moved on, and `workGuard` drops the wake. Persona never switches on its own; agents_list marks the agents a limit stopped (`usage_limit`) and their launcher. `agents_switch` (#6008) does the same for many agents in one call: the ones a limit stopped are switched and continued, idle ones switched with nothing sent, busy ones queued until their turn ends, stopped ones Persona started pointed at the new launcher for their next Resume.
- **No misleading notices while it waits** (`limitTakenUp`: the reset is known and at most 10 minutes past, and the CLI said it goes on by itself, or Persona's continuation or its card is set): no "Completed" or "Interrupted" push for the turn. The turn stays followed, so the end after the reset is the one pushed, timed from the turn's start. A task dispatched from the main chat gets no receipt meanwhile; it is looked at again when the wait is over, so a turn that did not go on after all (its continuation cancelled) still gets its "Interrupted" receipt. A limit nothing takes up (the switches off and a CLI that does not go on by itself, or no reset known) is told as the turn ends, as a usage limit ("Usage limit reached; continue it once the limit resets: …"), never as "the model service failed". For the main assistant, a task waiting for the reset reads as `usage_limit` in its snapshot, not `interrupted`. A sign-in that is gone ("Please run /login · …", "Login expired · …") is no usage limit: it reads as interrupted (`turn_error_code` `auth_failed` / `not_logged_in`), never as "Completed".
- **Scheduled tasks** (`watchScheduledRun`): a run whose conversation stopped on a usage limit with a known reset, or on another terminal provider failure carrying a trustworthy future recovery time (structured reset metadata, `Retry-After`, explicit UTC/RFC 3339 availability times, or explicit `try again in ...` durations) once no in-conversation transient-recovery wake remains, is retried a minute or so after that recovery time instead of after 3 minutes; missing, malformed, or past recovery metadata keeps the 3-minute fallback. That wait is not cut by the watcher's 30 minutes, and `retryStillDue` decides at that time as before (a retry that fails again is not retried); a conversation that went on meanwhile (its turn is no longer the one the failure stopped) or has a pending transient-recovery wake is not retried, so what the run does is not done twice. A conversation that goes on by itself after a usage limit is not retried. The absolute retry deadline, failed run (including briefing occurrence `due`) and target turn are persisted before the wait and restored after a restart. The manager loads persisted conversation work before dispatching a restored retry. If that read fails, the retry stays pending for another check. A claimed retry whose dispatch outcome is unknown after a restart is not sent again: its original run records the unknown outcome and the ordinary failure notification explains it. The initial failure push is unchanged.
- **Known limits**: the waits are kept in `session_work` like any wakeup and come back after a restart, but only for turns that ended within the last two days (a weekly limit can be longer). Scheduled-run retries and delayed task receipt rechecks use the manager's durable wait backlog and resume at their original deadlines, promptly when already overdue. Receipt rechecks are fenced to the same task-agent link and reopen generation; the durable turn ledger and stable receipt ID prevent duplicate completion when a recheck is replayed.

### Usage limits: a backup launcher

An owner who relies on the assistant can name backup launchers (#14435): when a usage limit stops a conversation they depend on, it goes on with a backup at once instead of waiting for the reset, and comes back once the limit is over. Nobody taps anything; choosing the list is the owner's one confirmation.

- **The setting**: `assistant.limit_fallbacks` (`persona_settings` only, with its own phone card; `limit_fallback.go`): launcher names of the assistant's machine, first choice first, at most 8. Empty, the default, leaves everything above as it was.
- **Which conversations**: Assistant conversations of the assistant's own space that are not a guest's or a reviewer's: the main conversation, the leaders and the background supervisor (the ones continued after a limit, #7824), and a side conversation whose stopped round was the owner's own message. Coding Agents (`agent_switch` above, on request), a space's conversations, and a side conversation's round a schedule or another conversation started wait for the reset as before.
- **When**: the turn that settled ended on a usage limit whose reset is known and at least 10 minutes away (`limitFallbackMinWait`; a sooner one is waited out, a switch would come straight back). No reset known: nothing moves, and the card above stays the way.
- **Which launcher**: the first listed that is not the one it runs on, is not unusable by its health on that computer ([launcher-health.md](../server/launcher-health.md)), and the switch takes for the conversation (a registered engine; a supervision policy for a leader or the supervisor). It runs with that launcher's default model and effort; the permission mode carries over. None can: it waits for the reset, and the log names each one passed over and why.
- **How it moves**: a one-item conversation-switch batch ([assistant.md](assistant.md) "Logical assistant conversations"), durable before anything starts, with the before-image, the epoch fence and the busy queue of any batch. It is a first-turn batch: the switch is done only when the backup takes its first input, which carries the summary of what went before; it is never accepted idle. That input is Persona's continuation (`prompts/agents/limit_fallback_continue.md`, the auto-continue source, its own `client_msg_id` so up to 5 tries are one message): go on with what the limit stopped. An input already waiting (the owner's message, a supervision message) goes first with the summary instead, and no continuation is sent. The apps show the continuation as one line ("Usage limit reached: Persona moved it to another launcher to go on", `injected` state `limit_fallback`). The continuation that was due at the reset is not sent: the turn moved on, and `workGuard` drops it.
- **Back after the reset**: about a minute after it (`limitContinueAt`), a restore batch (the `restore_batch_id` of the fallback's) moves the conversation back to its exact saved machine, launcher, model, effort and permission mode. It is a first-turn batch too: the summary of the backup's work goes with the conversation's next input, usually the owner's next message. Until that input, a permission-mode change, a Resume or an answer-again is refused as busy and can be retried after it. While the original launcher's health still shows it unusable, the restore waits for that later reset (plus about a minute), or 15 more minutes when none is known (`limitFallbackRecheck`). If the node disconnects before the automatic restore starts, the manager keeps the return pending. Once the node reconnects it rechecks the conversation's current settings before starting the restore.
- **Never over someone's choice**: the restore's fence leaves a conversation alone whose execution changed meanwhile (an explicit switch, a pick in its model menu): that choice stands, including a switch away and back to the same backup settings. If the limit resets before a queued fallback starts, Persona calls it off even when the conversation is idle on the next check. Each fallback and its restore stay in the switch state for 7 days.
- **A limit soon after a return** (#14557): if the same launcher reaches its limit again less than an hour after an automatic return, the next return waits an hour past that limit's reset. Repeats wait two hours, then four, then six at most. Once an automatic return lasts an hour, a later limit starts with the usual timing. The manager records the repeat count with the fallback and uses the acceptance time of the return's first input, including across a restart.
- **Known limits**: one level: while a conversation is on a backup, or about to be, it does not fall back again; a backup that meets its own limit waits for that reset, and the restore still comes at the first one. A fallback refused for a conversation is not tried again until that limit resets. Only a usage limit with a known reset moves anything; transient errors keep their backoff. The switch's summary is the source's own compaction summary when it has one, else the knowledge-processing model's: while that model is paused, by the same limit for example, the switch waits for it as any switch does, so knowledge processing on another provider keeps the fallback quick. Once moved, a conversation runs on the launcher and model of its current segment, as after any explicit switch, also after the restore: a later change of Model configuration › Main chat applies from the next new main chat, and the conversation's own model menu still changes it within the same engine.

### Offline computer: automatic machine failover

An owner with more than one non-work computer can configure ordered backup computers (#15119): when the computer hosting the main conversation, an active direction leader, or unbound default scheduled routines stays offline past a configurable threshold, Persona moves them to an eligible online backup computer so chat and supervision continue without interruption.

- **The settings**: `assistant.machine_fallbacks` (ordered list of up to 8 non-work computer IDs; empty by default = disabled) and `assistant.machine_fallback_minutes` (offline duration threshold in minutes; default 10, range 1–1440), both managed through `persona_settings` with owner confirmation (`machine_fallback.go`).
- **Offline timing**: reuses `node_recovery.go`'s per-machine `offlineAt` tracking. A computer offline for less than `assistant.machine_fallback_minutes` never triggers failover.
- **Candidate selection**: walks `assistant.machine_fallbacks` in order and picks the first computer that is physically online, not the current offline computer, not a work computer (`!isWorkMachine`), and has a healthy compatible launcher that supports `SupervisionPolicy` (preferring the conversation's current launcher, then the default launcher, then `assistant.limit_fallbacks`, then other registered non-external launchers on that computer).
- **Offline-source segment switch**: uses `logical_switch.go` with `AllowOfflineSource` enabled internally for failover batches. When the source computer is offline, segment adoption (`AdoptConversationAliases`), tail boundary (`PrepareConversationSegment`), and handoff context (`logicalSwitchHandoff`) read persisted Postgres `ConversationHistory` (`History`, `TailSeq`, `Summary`) and mark the handoff `Partial` when needed. The public `(machine_id, session_id)` identity stays unchanged so apps keep the same conversation and supervision ownership stays intact.
- **Queue drainage**: once the failover batch switches and rebinds, Persona immediately wakes and drains queued `assistant.undelivered` owner messages and `assistant.relay` supervision messages to the newly activated segment.
- **Split-brain prevention and manual-change protection**: the retired source segment is marked `ClosePending` and fenced by `logicalCaller` and `NodeEvent`; when the original computer reconnects (`NodeConnected`), `logicalSweep` is woken immediately to close retired segments. Every failover batch carries an `ExpectedEpoch` fence and execution snapshot so manual user changes are never overwritten.
- **Default scheduled routines**: in `schedule_execution.go` (`taskMachine`), routines without an explicit `execution_machine_id` or `cwd` (and not continuing a source conversation) fall back to the first eligible online backup computer when the default assistant computer has been offline past the threshold. Routines pinned to a specific computer or directory stay pinned.

## Safety boundary

Persona **sends to Agents on its own** the following kinds of message, none introducing new intent:

1. Continuation after a transient error: like "Retry" on the App's error card, it resumes the interrupted task; likewise the continuation once a subscription's usage limit reset (`prompts/agents/limit_continue.md`: go on with what you were doing, without redoing what is done), and the same on the backup launcher a usage limit moved an Assistant conversation to ("Usage limits: a backup launcher" above);
2. A wakeup after a registered wait: asks only whether the awaited event is ready, and explicitly says `接下来要推代码、部署、给别人发消息、合并或改别人的 MR 时先停下来问用户，不要自己做` ("Before pushing code, deploying, messaging others, or merging / modifying someone else's MR, stop and ask the user; do not do it yourself");
3. The answer the user selected on a card;
4. After the auth relay completed the authorization link it stopped at ([auth-relay.md](../operations/auth-relay.md)): go on with what it was doing;
5. A status question after a turn that did work and ended without a closeout, to an Agent nobody supervises ("Closeout", the `closeout_ask` switch): it asks only for a report on work already done, in the closeout form, and says to start nothing new and not to push, merge, deploy or message anyone. Once per turn, and never to the turn that answers it.
6. The supervision message a task leader's agent gets about its workers (`prompts/agents/worker_supervise.md`, [unified-entry.md](unified-entry.md), "Task leaders"): the workers' reports and stops, with nothing to do unless the leader decides; it queues behind a running turn like any message.

Any next step involving **pushing code, deploying / releasing, messaging others, or touching someone else's MR / PR** must not receive an unapproved automatic wake:

- The prompt marks such waits with `wait.risky`; without an approval the hub registers no wakeup and does not invent a question for the user. A card still requires an actual unanswered request in the Agent's reply; missing request evidence is left uncertain, not labeled done.
- Code fallback: if `wait.then` / `line` contains push, deploy, `部署` (deploy), `发布` (release), `上线` (go live), merge, `合并` (merge), `发消息` (send a message), `回复` (reply), `评论` (comment), testflight, release, `发送` (send), `删除` (delete), `购买` / `付费` / `续费` / `充值` (spend), or `生产` / production, do not register a wakeup unless the same wait and step are approved;
- An approval comes from the user's "Do it once it's ready" on a card the Agent asked, or from a task that authorizes the step (#3246). The wake then goes out with the approval, and no second card is opened;
- When the user chooses "Do it" on the card, the Agent receives the user's answer, which counts as user authorization.

**A step its task already asked for is not asked again** (#3246, `agents_work_authorized.go`): before the card goes up, the hub reads the title and notes of the Agent's open to-do (supervised or not). When the to-do authorizes the step, the hub treats it as the user's "Do it once it's ready": no card and no push, a normal wakeup once the wait is due, the line `等「X」，任务已授权，好了就去「Y」`, and the work event `auto_approved` (to-do id, whether supervised, wait, step) as the trace. The wake tells the Agent that its task authorized the step. A new wait (the next release node) is checked the same way, so it is not asked again either. The to-do authorizes the step only when every risky word in it passes:

- Never: sending or replying to people (`发消息`, `回复`, `评论`, `发送`, send, comment), deleting (`删除`, delete), spending (`购买`, `付费`, `续费`, `充值`), changing production (`生产`, production). These remain the user's whatever the to-do says.
- Pushing its own branch (push, `推送`, `推到`): allowed for any to-do.
- Merging, releasing, deploying (`合入`, `发布`, `上线`, `部署`, merge, release…): only when the to-do text contains the same word and no negation (`不要`, `别`, `禁止`, don't…) precedes it in the same clause.

With no open to-do, or with a step its to-do does not authorize, the hub neither wakes it nor opens a card of its own: a card only carries a question the Agent actually wrote (#3290). With the wake switch off nothing is woken either, and a card the Agent wrote still goes up. With more than one question or an authorization link, the task does not auto-approve the step.

### Standing authorization answers

Before supervisor routing, `announce` checks a hook-waited question or plan against the persisted
[standing authorizations](unified-entry.md#standing-authorizations). A matching answer says “Already authorized,
continue,” with the grant ID, original words and source, without showing a pending card or notifying the supervisor
or user. The current task must be open, and both its bound directory and the node's current session directory must match the computer and scope;
the card's own `cwd` is not trusted. Explicit task prohibitions and read-only launches block this path.

Simple single-action confirmations use a deterministic fast path. Natural questions (including PR numbers and
free-form choice descriptions) and multi-step plans use the existing `agent.delegate` model purpose to classify the
complete action set and choose exact affirmative labels. The manager checks every action against one grant; local
read/edit/verification steps may accompany an authorized external action. A merge grant cannot approve a plan that
also pushes, deploys or deletes. Failed, incomplete or ambiguous classification keeps the original route. Selected
wait/cancel/defer or runtime-action options are rejected; an unselected defer option does not block a valid answer.
Forms, passwords, assistant `ask_user`, runtime-local dialogs and extra tool-input fields keep their existing route.
The judge uses the model configuration for `agent.delegate`, including its selected managed provider and model;
it never starts an unconfigured CLI. The synchronous authorization attempt has a five-second classification and evidence-read context budget
(including classification and scope/evidence reads), reduced from sixty seconds so a slow model promptly falls back
to the normal card route. This favors manual confirmation over waiting for a slow automatic answer; it does not
introduce background approval or change resolution persistence. If that model is unavailable, the card follows the existing supervisor/user
route, and counts as a user confirmation only when that route actually notifies the user.
A conservative code scan adds sensitive effects even if classification misses them; unspecified deployments remain
production-sensitive. Every explicit card path is independently resolved and checked against repository scope, including relative paths
(`../other`, `./server`, `server/internal`), `~`, `$HOME` and `$PWD` (including braced forms). Relative paths use the
node's session directory; home expansion and physical Git identity come from that node. Unknown session variables,
shell substitutions, globs, quoted variable/home expansions, ambiguous quoting and failed identity reads keep confirmation manual. Paths are kept literally, including trailing dots and parent segments;
sentence punctuation on an unquoted path may therefore require manual confirmation. A node must advertise
`path_resolved: true`, proving it resolves symlinks before parent segments; older nodes keep path cards manual.
Resolution persistence has its own timeout and is outside the five-second classification budget. The judge's
`within_task` can veto a card but cannot prove that a path is authorized. The model's output never grants tool permissions or replaces condition evidence.

Tool `permission` cards are never approved by standing authorization. Production deployment, external sending,
deletion (including force-pushing or replacing remote history) and spending each require their own explicit action and `reversible: true`; task notes alone still cannot
authorize the `userOnlyWords` actions described above. A plan answer explicitly uses runtime mode `default`, so it
does not grant bypass permissions. Existing per-session tool grants are separate and unchanged.

`ci_green` and `no_conflicts` gate every action in the grant: the manager reads the current branch through `git.pr`, requires an
open, non-draft PR with a head commit, and binds explicit PR URLs or numbers to the returned PR. Multiple distinct PR targets remain manual. CI requires a complete,
nonempty list of fewer than 100 checks, all passing; pending, failed or skipped checks do not pass. No conflicts
requires `mergeable: MERGEABLE`. A condition written in other words is saved and injected but remains manual until
there is an independent verifier for it. A card's claim that checks passed is never evidence. Conditions are checked
at answer time. Push and test deployment with these conditions also require the node's current local HEAD to match
the checked PR head, so old remote CI cannot approve new unpushed commits. This does not replace the repository's merge-time quality gates or protect against a later head change.

The registry is re-read after those checks. A database transaction locks the grant against revocation, verifies the
task is still open and unchanged since the checks started (including its decisions), and commits the interaction resolution, `source: agent` task decision with authority, and automatic
count together before the waiting hook receives its answer. Failed audit writes leave the card pending. This path
neither changes a risky wait's existing wake rules nor invents a new task or permission.

### Daily confirmation counts

Morning and evening briefing facts, cards, text fallback and feed include the current local day's confirmation counts,
from the user's calendar-day start to the briefing snapshot. Counts group by computer, Agent session, task and card
kind (`question`, `permission`, `plan`, and any other recorded kind). Each interaction counts once when `tellUser`
first dispatches it to the user layer; cards held by a supervisor or shown only on apps are excluded. Several cards
sharing one push each count as a card. Muting may suppress the push while channels still receive the card: these
numbers measure dispatch to the user layer, not APNs delivery receipts. Automatic standing card answers count separately; existing task-note risky waits that never create a card are outside this card count.

The interaction table keeps the first notification and automatic-answer timestamps and grant/task IDs across
restarts. A card created yesterday and first notified today counts today. History from versions without these markers
is not guessed or backfilled. A failed lookup is displayed as unknown, never as zero.

## Avoid conflicting with manual user actions

**Immediately before** each automatic action (continue, wake, status question, create a card), reread the session and proceed only if all conditions hold:

- It is still the triaged turn (`updated_at` unchanged), stopped (not working), with no pending interaction or queued input (`queued`);
- The host is `done`, not `idle`: if you focused this tab in herdr (watching it at the computer), leave it alone and only record work state. Hosts that keep reporting `idle` after stopping therefore get no automatic actions (better not to interfere);
- The computer is online;
- It is not linked to a completed to-do without an open to-do as well (released and dismissed links do not count).

Sending a message yourself or typing on the computer starts the session working: all retries, wakeups, and cards for that turn become invalid. Triage again after the new turn ends.
The same applies to "Retry" on an App error card (work begins, invalidating queued automatic retries). Persona records continuations, wakeups and status questions in `session_work`, sending each only once per turn.

## Declining a task-progress reply draft (#3743)

A `message` card in a task's conversation is a reply drafted from the task's progress,
not an Agent permission or question card. **Send** explicitly replies through the
Inbox (`POST /api/v1/inbox/{ref}/reply`, with `card` and `text`) and records `sent_at`.
The existing **Later** action only collapses the inbox ref in that client's memory;
it does not record a decision and does not survive a reload.

**Don't handle** records a decision on this exact draft without sending anything.
It removes only this card from **Needs you**. The Inbox item stays open, and the task,
its Agents, supervision, permissions and unrelated pending questions are unchanged.
The card retains its original draft and adds `dismissed_at` and `dismissal_id`; it
must not be presented as sent or as completed work.

Both routes go to the conversation's manager (`X-Persona-Manager` or `?manager=`):

| Method | Path | Body | Success |
| --- | --- | --- | --- |
| POST | `/api/v1/assistant/feed/{id}/dismiss` | `{"draft":"<original body.draft>"}` | `{"item":FeedItem,"dismissal_id":"<decision id>"}` |
| POST | `/api/v1/assistant/feed/{id}/undo-dismiss` | `{"dismissal_id":"<decision id>"}` | `{"item":FeedItem}` |

The draft comparison uses the unedited original card text, not textarea edits. The
card ID binds the immutable draft version. A duplicate dismissal returns its
existing receipt without writing another decision. **Undo** removes `dismissed_at`
and `dismissal_id` and returns the same unsent card to pending. It writes a decision
superseding the dismissal. An old Undo cannot replace a newer dismissal or restore
an older card once a newer draft for the same Inbox ref exists in the conversation.
Ordinary Agent output does not invalidate dismissal or toast Undo: an exact old
card can still be declined without affecting any newer pending card. Dismissal
preserves its original progress generation; only legacy cards establish a baseline.
A changed draft, sent or filed item, or stale Undo decision returns
`409 task_draft_stale`; missing or non-message cards return `404`; read-only roles
return `403`. Sending a dismissed card from a stale client returns
`409 task_draft_dismissed`, with `delivered: "none"`.

The manager stores the body change and the task's `todo_decisions` audit (`source:
card`) in one Postgres transaction. It broadcasts the same `assistant_feed` item ID
only after commit. Incremental feed reads also return older cards whose dismissal or
Undo changed after `since`, in original `at, id` order. The indexed `updated_at`
column and retained `body.dismissal_updated_at` recover Undo after its dismissal
fields have been removed; the original card's conversation position never moves.

Clients opt into stable feed pagination with `GET /api/v1/assistant/conversations/{id}/feed?paginate=1&since=<RFC3339Nano>`
(`since` is optional for a cold read). The response retains all existing fields and
always adds `next_cursor`: an opaque string, or `""` when complete. Fetch subsequent
pages from the same path with only `cursor=<URL-encoded next_cursor>`; a cursor
implies pagination. Do not advance `since` using the last card's original `at`: a
full page of older dismissal updates would repeat and starve new progress.
The cursor preserves the initial `since`, freezes an upper bound `until`, and
advances only the `(at, id)` position. Each page therefore returns older updates
once while retaining new entries in original conversation order. Updates after
`until` remain eligible for a later incremental traversal. An invalid cursor, one
from another conversation, or `cursor` combined with `since` returns `400`.
Legacy requests without `paginate=1` or a cursor retain their existing response;
clients that receive no `next_cursor` from an older server can use legacy paging.

Draft submission records `message_version` and an opaque `progress_version` based
on the active task Agents' transcript revision, last output and tool activity. It
never uses polling timestamps or the task's decision-update timestamp. Submitting
another draft for a dismissed source message and the same progress generation is
suppressed before insertion or broadcast, including reworded drafts and after a
manager reload. Missing Agent progress is not evidence of a new generation. New
Agent progress or a genuinely new source message can produce a new pending card for
the same Inbox ref. The periodic handling sweep does not itself create draft cards;
creation remains at `inbox_suggest` / `inbox_draft` submission.

## Switches (requested 2026-11-05: all off by default)

Triage calls a model after every stopped turn; continuation and wakeups also make the Agent run another turn on its own model. All consume the user's tokens. Therefore **everything defaults to off**,
except two rules that need no triage model: `reply_reminder` and, since #3208, `transient_retry` (continuing a turn a transient error cut off only finishes what the user already asked for).
Users enable them under **Settings › Session Triage**. The switches are stored in settings `agents.triage` (read and written only by the manager); tool-loop detection is configured on the web:

| Switch | Controls | Whose tokens it uses |
| --- | --- | --- |
| `enabled` Session Triage | Main switch: triage after a turn stops, show what it is waiting for, and stop counting completed work as Unread | Triage model (**Settings › Model Settings › Session Triage**, `agent.triage`, defaults to the Inbox triage model) |
| `decide` Create decision cards | Turn user decisions into "Needs you" cards | No extra tokens |
| `auto_continue` Continue automatically on errors | Automatically send continuation once a subscription's usage limit resets | Agent's model (resumes the interrupted turn) |
| `transient_retry` Continue after transient errors (#3208) | Automatically send continuation after transient model API errors ("Transient errors: automatic continuation") | Agent's model. **On by default**, independent of the main switch |
| `wake` Scheduled wakeups | Wake Agents waiting for deployment / CI / another Agent so they check for themselves | Agent's model (one turn per wakeup) |
| `tool_loop` Detect tool loops | Offer to interrupt an agent repeating tool calls, excluding polling | Triage model (at most three checks per turn) |
| `closeout` Ask for a closeout | Hold an Agent's stop once when a turn that did work ends without a closeout ("Closeout") | Agent's model (one more turn when the closeout is missing) |
| `closeout_ask` Ask for the status afterwards (#3616) | Send one status question to an Agent nobody supervises whose turn did work, ended without a closeout and could not be called finished ("Closeout") | Agent's model (one turn per question, at most one per turn) |
| `reply_reminder` Remind when seen, unanswered (#5096) | One push when an Agent asking the user has waited `reply_reminder_minutes` (default 30) since they looked ([agent-states.md](../protocol/agent-states.md) "Awaiting your reply") | None: rules only. **On by default**, independent of the main switch |

- Subswitches work only when the main switch is on (but `reply_reminder` and `transient_retry`). Disabling a switch takes effect immediately: Persona-created cards are removed, and queued retries / wakeups / status questions are not sent.
- `GET / PUT /api/v1/agents/triage` (`{enabled, auto_continue, wake, decide, tool_loop, closeout, closeout_ask, reply_reminder, reply_reminder_minutes, transient_retry}`, forwarded by the hub to the manager); changes broadcast `{"type":"agents.triage"}`. A PUT without `tool_loop`, `closeout`, `closeout_ask`, `reply_reminder`, `reply_reminder_minutes` (or with minutes 0) or `transient_retry` keeps their values for older apps; negative minutes go back to the default 30. While the saved switches cannot be read, such a PUT saves their defaults.
- A `GET` whose first read of the database fails answers `503` with an `error` in the request's language (Chinese, English or Japanese) saying why, instead of returning the defaults as if they were the saved switches (#4553). Failures are not cached: the next request reads again. Switches once read or saved stay cached in memory; `manager.json`'s initial values apply only when the database really has no setting. Triage itself still falls back to the defaults when its own read fails, and `PUT` saves as before.
- `agents.triage` in the manager's `manager.json` (`docs/operations/config.md` ("manager.json"); in hub.json before 2026-11-06) retains limits: legacy `max_retries` (retained but unused for temporary failures), `retry_base_seconds` (default 15), `account_exhaustion_max_retries` (default 0; the all-conversation account-pool policy in "Transient errors: automatic continuation"), `max_wakes` (default 6), `settle_seconds` (default 15), and `just_finished_hours` (default 3).
  Hand edits take effect on manager restart (a hub restart for the built-in manager) or the next hello (a reconnect or `manager.updated`), as described by "Edits" in the configuration manual. Its triage switches are initial values only when settings have never been saved in the App (omitted means off). The manager reads this section itself and lays it over the hub's handshake reply (`assistantwire.Hello.Agents`); the hub no longer reads or sends it.

## Storage

A process can be redeployed and cleared at any time, so triage results, retry counts, wakeup times, and cards are persisted (`0331_session_work.sql`, read and written only by the manager):

- `session_work`: one row per session, with the latest turn's work state (`work` JSON), turn-end time `turn_at`, next action `wake_at`, retry / wakeup counts, and card id.
  On startup the manager restores recent turns and, beyond the usual 48-hour window, CI watches with a pending wake, including prepared messages, and recently retired turns. The 30-day cleanup retains active CI watches until they finish or are cancelled, and retains recent retirement markers even when their original turn is older. A todo that ends while its agent's turn is settling records its retirement and, when available, that stopped turn's node timestamp, preventing it from registering a new wake without comparing the node's clock to the manager's. Before scheduling automatic follow-ups or sending a wake, the manager also checks the current todo links, so a failed retirement-state write cannot revive follow-ups for a still-linked completed todo unless there is an open todo too. If todo state cannot be read during triage, the turn stays untriaged and the usual backfill retries it after ten minutes while it remains unread and within the 24-hour catch-up window; a later read failure for an existing watch postpones its next attempt. A later turn-start event or a newer node turn verified against the retired turn can clear the retirement marker, but automatic follow-ups still require that no completed todo blocks them. Reopening the todo or linking the Agent to an open todo allows new follow-ups, including after a restart. A persisted CI proof from an older manager never overrides a currently completed todo. If the retired turn's identity and a new turn-start event are both unavailable, the manager conservatively waits for another start. Continue due actions and restore unanswered cards only if still the same stopped turn, with the same id; wait for the computer to connect before restoring cards.
- `session_work_events`: one row per triage, automatic continuation, wakeup, card creation, card answer, and wait completion (30 days), for evaluation below. A `resumed` row (the agent went on after a stop: `after`, `phase`, `on`, `by` `user` or `hub`) also carries `hand` (`decide`, `auto_continue` or `wake`) when that triage action alone, its switch being off, would have gone on with the stop and the session then went on by hand: with no message of the manager's before the turn and not by the agent itself; the weekly setup block counts by it (`docs/assistant/assistant.md` ("Recommended setup")). The availability metrics add `reply_wait`, `round`, `queue_wait` and `ask_outcome` rows for the assistant's conversations (`docs/assistant/assistant.md` ("Availability metrics")).

## What Persona knows

The main chat's live snapshot (`prompts/assistant/snapshot*.md`):

- Each Agent under "Needs you" includes what it is waiting for;
- Adds "Waiting" (Persona is waiting for it: retrying, deployment, another Agent). Working agents render only their count in the Agents heading (#11261), and "Done (within 24 hours)" and "Muted" carry only counts; `agents_list` returns working agents' names, session ids and running workflows (`state=working`) and done or muted agents' conclusions (`state=done` / `state=muted`) when the assistant needs them;
- "Unread" contains only sessions not yet successfully triaged, and those whose closeout says the turn is not finished, with that line ("Closeout");
- An Agent's line ends with what its closeout claims of merging and deploying, and the hub's finding where it differs.

For tasks dispatched from the main chat (`internal/assistant/tasks_follow.go`), if the Agent stops on a transient error and Persona will still continue automatically (`work.on = transient` with `wake_at`), do not yet write an "Interrupted" receipt. Wait until retries are exhausted. The same holds while an Agent waits for a usage limit to reset ("Usage limits"); the snapshot shows its task as waiting for the reset, not as interrupted.

## Protocol

`proto.Session` adds `work` (`docs/protocol/protocol.md`, "Data types"):

```json
"work": {"phase":"blocked","on":"external","line":"Wait for deployment, then run production tests",
         "wait":"Deployment completes","then":"Run production tests","wake_at":"…","attempts":1,"max":6,
         "agent":{"machine_id":"…","session_id":"…","label":"…"},
         "link":{"url":"…","expires_at":"…"},
         "artifacts":[{"kind":"pr","ref":"https://github.com/…/pull/1"}],
         "closeout":{"status":"done|not_done|blocked","waiting":"…","did":["…"],"merged":"yes|no","deployed":"yes|no",
                     "verified":"…","missing":"…","checked":{"merged":"yes|no","deployed":"yes|no"}},
         "turn_at":"…","at":"…","by":"rule|model"}
```

- While working, only `{"phase":"working"}`; pending interactions use `{"phase":"blocked","on":"user","line":<question>}`; sessions not triaged omit `work`.
- `"phase":""` with a `closeout` and a `line`: a turn that is not finished ("Closeout"): the Agent's closeout says so, or the hub disproved its merged / deployed claim. `state` stays Unread; a client that does not know the closeout shows the line as it is.
- `closeout`: what the Agent said of its own work as the turn ended ("Closeout"); `merged` and `deployed` are absent when they do not apply or were not said. Session lists omit `did` and `verified`.
- `resets_at`: the turn stopped on a subscription's usage limit that resets then ("Usage limits"): on `external` the wait for it, on the card after the automatic continuations when it resets.
- `GET /api/v1/agents/work/stats?hours=24`: evaluation metrics (below).

## Evaluation

`GET /api/v1/agents/work/stats` (forwarded by the hub to the manager) and manager logs (prefixed `assistant:` in hub logs, `triage <machine>/<session>: done (model 1.2s): …`):

- Distribution of post-turn triage results (done / blocked types / unsure / failure), split by rule and model;
- "Unread" count: every 5 minutes the manager samples the number of non-assistant sessions with `state = unread` (`sample` in `session_work_events`), reporting average, maximum, and current count for the interval;
- Wait duration: median / p90 from turn end to user action (viewed, replied, answered a card), split by "Done", "Waiting for your decision", and "Not triaged";
- Automatic continuation: number sent and proportion followed by normal completion; wakeups: count and proportion completed after waking;
- Cards: created, answered, wait before answer, and removed (because you replied directly);
- Closeouts, in `actions`: `closeout_asked` (stops held for one), `closeout_ask_sent` (status questions sent), and for the turns that followed a question `closeout_ask_answered` (carried a closeout) and `closeout_ask_ignored` (did not);
- Decisions the owner could not answer (#3804): `owner_cannot_answer` is `{"<supervisor conversation>": {"decide_yourself": n, "not_what_i_asked": n, "explain_first": n}}`, one count per fixed answer the owner gave a supervised task's decision instead of an option (work event `owner_cannot_answer`), so a conversation that keeps asking what the owner cannot answer shows (`docs/assistant/todos.md`, "Decisions the owner can answer");
- A wrong "done" (#2586), in `wrong_done: {"reopened": N, "rework": N}`: two cheap labels for the done judge (`docs/assistant/todos.md`, "Automatic completion") closing work too early, each to-do counted once in the window.
  - `reopened`: to-dos the judge closed that were reopened within 24 hours of the close (24 hours exactly still counts). Closed by the judge means `done_by = auto`, or a supervised task the judge accepted for its supervisor (`docs/assistant/todos.md`, "Spot-check reviews"). Any reopen counts: by hand, a change asked for on the "Please verify" card, or the task's Agent set to work again. A to-do closed by hand or accepted by a supervisor never counts.
  - `rework`: supervised tasks the judge found done (waiting for review) that `task_review` sent back with `rework`, including one past the last round that goes to the user instead. A rework sent while the task was not waiting for review does not count.
  - Both are work events without a session in `session_work_events` (kept 30 days, no migration, `assistant/agents_work_wrong_done.go`): `todo_reopened` on every reopen of a done to-do (`todo`, `done_by` as it was, `judge`, `after` in seconds since the close), `review_rework` on every `task_review` rework (`todo`, `judged_done`). Added in 2026-11; older hubs omit `wrong_done`, and a client that does not know it ignores it.

Pre-launch baseline (evening of 2026-11-04, non-assistant sessions on the hub): 5 unread (4 within 24 hours), with a median wait already around 10 minutes.

## Clients

- Web (#1837): the list preview (`sessionPreview`: overview, Machines page, knowledge-base details, sidebar hover tooltip) switches to work state when the session is stopped and the hub has triaged it:
  "✓ Conclusion" for completed work, "What it is waiting for (wake in N minutes / retrying now)" for waits. Sessions the hub will wait for go into "Waiting" (`hubWaits`, `web/src/lib/work.ts`).
  The switches are under **Settings › General › Session triage**, with a footer link to **Model settings › Session triage** to choose the model. Screenshots use a fake hub with `MOCK_WORK=1`.
  A subscription usage limit (`turn_error_kind` `usage_limit`) is not shown as Interrupted: task cards, agent rows and the chat say "Usage limit · resumes <time>" when the CLI or the hub will continue it, and "Usage limit · resets <time>" when nothing will (the user's time zone; the date as well when that is another day). An older hub that does not send the fields keeps the interrupted text. Screenshots use `MOCK_USAGE_LIMIT=1`.
- Android (#1837): the Agents list and knowledge-base "Agents working on this project" rows use the same preview (`Session.workLine`, `WorkLine.kt` in `core:domain`). Sessions the hub waits for go into "Waiting";
  **Settings › AI › Session triage** has the main switch, triage model (link to model settings), and three action switches.
  Closeout (#3612, `docs/android/android.md` "Closeout, decisions and tasks nobody works on"): a stopped Agent's page has the closeout card above its transcript, with the hub's finding beside merged and deployed, and the reply is drawn without the block the card shows; a list row keeps its line and says "merged ✓", "not deployed" as facts under it, with a mark beside the title for a claim the hub found untrue and for a turn that is not finished; **Session triage** has the **Ask for a closeout** switch on a hub that sends it and **Ask for the status afterwards** (`closeout_ask`) beside it when the hub sends that field, both disabled while triage is off or a save is in flight.
- iOS / Mac (#2895): Agents-list and space-page card previews (`Session.listPreview`) switch to the same line when the session is stopped and the hub has triaged it (`Session.workLine`, `apple/Shared/Models.swift`).
  Host-provided reasons for needing the user (botmux) and failure notices still take precedence. Sessions the hub waits for go into "Waiting", not collapsed into idle (`WorkState.hubWaits`, `Session.isWaiting`). Unit tests: `SessionWorkTests`.
  **Settings › Session Triage** (below the assistant, `TriageSettingsView` / `TriageSettingsModel`) has the main switch, triage model (opens `agent.triage` in Model Settings), and three action switches (disabled when the main switch is off). Failed changes revert to the old value with an explanation; old hubs (404) report unsupported. Unit tests: `TriageSettingsTests`; DEBUG `-tab settings -openTriageSettings YES`.
- iOS (#3612; iPad and Vision through the same views, not the frozen Mac app): a stopped Agent session's closeout is a card in its chat's header, above the transcript (`CloseoutCard`): the status, "The agent's own report", what changed, artifacts, merged and deployed each with the hub's finding (confirmed, contradicted in the caution color with the warning mark, or not checked), verified and still needs. While it shows, the last reply is drawn without its raw closeout record (`ChatLines.hidingCloseout`). List rows keep their line and add where the closeout stands ("merged ✓ · not deployed", "✗ says merged, isn't"; `Session.closeoutPreview`); a turn with an empty phase and a line shows that line. **Settings › Session Triage** has **Ask for a Closeout** (`closeout`; no row on a hub that does not name the switch), followed by **Ask for the Status Afterwards** (`closeout_ask`, #3616): a stopped → one question → report example, disabled only by the main switch rather than by `closeout`, and likewise hidden and unencoded when its field is absent. Details, tests and the DEBUG samples: `docs/apple/ios.md` ("Closeout, orphaned tasks and answerable decisions (#3612, #3618, #3804)").
- Watch: list rows do not show last words; work previews are not displayed yet.

## Steps

| Step | Scope |
| --- | --- |
| 1 | This document; hub (moved to the manager since 2026-11-04, see "Responsibilities"): `session_work` table, triage (rules + model), automatic continuation, wakeups, cards, `state` algorithm, `work` field, statistics endpoint; snapshot; task interruption receipts |
| 2 | Move switches into App settings, all off by default; web displays `work.line`, Waiting, and switches (#1837) |
| 3 | Android (#1837); iOS / Mac work previews (#2895) and settings page (#3078) |

## Independent review of a session's work (#1808)

An Agent session's web menu offers **Review this work** only when the hub advertises `session_review`. Nothing starts at
rest: opening ⋯ reads `GET /api/v1/sessions/{mid}/{sid}/review` for `{running,has_changes,started_at}`, and only the
explicit choice POSTs. A busy or starting Agent, a running review, or no change disables the entry with the reason.
The source session's own Claude launcher starts the fresh reviewer with `permission_mode: "plan"`; Codex and other
launchers are refused before a snapshot or reviewer starts.

The source directory is only read. `git.review` supplies the branch change (falling back to uncommitted when no default
branch or merge base exists), and `worktree.snapshot` makes a disposable copy of its current HEAD and uncommitted change.
The reviewer runs in that copy. This is the safety boundary established for #2218: if the engine later recovers the
reviewer outside its plan-mode launch, it still cannot edit the source working tree. The reviewer's session ID is named
and persisted before `session.start`; no unacknowledged node `state.put` participates in identity or cleanup.

The prompt contains the genuine recent user asks (bounded together with the diff) and the real diff, never the source
Agent's replies, reasoning, injected follow-ups or peer messages. It asks for prioritized `path:line` findings and short
fix suggestions, ending in `<review-findings count="N" blocking="M"/>`. The manager removes the marker and uses its count;
an empty or unmarked answer is a failed review, not a blank card.

The manager's `assistant.session_reviews` setting holds running and finished records with acknowledged, revisioned writes.
A failed read never becomes an empty book, and memory advances only after persistence succeeds. At most one review runs
per source. A reviewer turn end finishes its run; the periodic review tick also recovers a reviewer that stopped while
the manager was away, fails runs older than 30 minutes, and expires result cards after seven days. A newer result
atomically replaces the older pending result for the same source. Reviewer sessions and their snapshot directories stay
out of published Agent lists and triage; cleanup stops the reviewer with `session.stop` (keeping its terminal) and removes
only the disposable snapshot.

The result is a manager-owned **question** interaction with `tool_name: "persona_session_review"`, so older clients do not
need a new kind decoder. Its additive `review_report`, `review_count` and `reviewer_session_id` fields hold the report,
count and correlation ID. **Send to agent** resolves the card through the normal answer path and forwards the persisted
report once to the source (an explicit resume when stopped), using the review ID for send deduplication. A failed send
returns a conflict and leaves the same card answerable. **Dismiss** resolves without sending. Manager reconstruction
restores the same pending card and its answers remain durable. Go coverage is in `agents_session_review_test.go`;
web coverage is in `ChatView.session-review.test.tsx` and `Interactions.session-review.test.tsx`.
