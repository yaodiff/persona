# Assistant (the "Persona" tab, formerly "Chat")

Persona in the "Persona" tab is Claude Code running **headlessly** in `~/.persona/assistant` on the hub machine (`claude -p` + stream-json; see
`docs/integrations/claude-integration.md` ("Headless assistant")). A Codex launcher uses `codex app-server` instead ("Codex" below); a launcher for an assistant-capable Agent runtime (DSH) runs in that runtime ("Agent runtime (DSH)" below). This doc covers what it can do, how it works, and the trade-offs behind its design; what it lacked before the 2026-10-26 capability upgrade is in `docs/archive/assistant/assistant-records.md`.

## Scoped outward messages (#9762)

Web Settings > Persona > **Outward messages** selects **Bot** or **Me** for Lark
via lark-cli. The default remains Bot; automatic sending remains off. Other
connectors and direct CLI writes retain their existing behavior and approval
rules. The sender selector does not grant account permissions.

To enable automatic sending:

1. Choose a sender and approve its change on the permission card, if changing it.
2. Enter an exact Lark group/chat ID, or explicitly select every chat in this
   channel. Choose **Suggested Inbox replies** or **Assistant-authored messages**.
3. Request approval. The permission card names channel, target, scenario, sender,
   audit, and revocation. Nothing takes effect until the owner's own approval.
4. The settings page shows that the request is waiting for approval. The card
   stays available for 24 hours, across a page reload or a hub restart. Once
   approved, the page refreshes **Standing authorizations**. Revoke it there at any
   time. Revocation stops future sends, not an already started transport call.

The same configuration is available through `persona_settings`:
`assistant.outbound_identity` is `"bot"` or `"user"`. A new
`assistant.standing_authorizations` entry may instead contain
`messages: {channel: "larkcli", target: "oc_example", scenario: "inbox_reply",
identity: "bot"}`, with `actions: ["send"]`, `reversible: true`, empty conditions,
and no computer/directory. An empty target explicitly authorizes all chats in
that one channel. Read the current registry revision and preserve unrelated
entries. Existing IDs are immutable; a changed scope needs a new approval.
Computer/directory grants never authorize this message path.

The assistant uses `message_send {channel, target, text, idempotency_key}` rather
than asking `ask_user` and then invoking a CLI. Unmatched sends get a normal
permission card; matched sends use their approved identity. Inbox automation
only consumes a persisted, current, plain-text reply suggestion: never handbacks,
handed-off conversations' suggestions, document changes, tasks, or conclusions.
Changing the default sender cannot broaden a grant bound to another identity.
A pending send card is also bound to its displayed sender, recipient, and text.
Changing the default before that card's send begins refuses the old approval.

Before a controlled send, the manager persists its complete attempted text,
recipient, identity, scenario, and approval/grant ID in the existing Sent ledger.
An audit failure prevents sending. Outcomes are `pending`, `sent`,
`failed_or_unknown`, or `already_delivered`; pending is not proof of delivery.
An extension manager's already-delivered send ID means this attempt sent nothing,
not that its attempted text was delivered. The ledger and error distinguish that
case from a fresh send. A durable send claim
survives manager reconstruction. A failed/uncertain automatic attempt is not
silently retried, even if an Inbox reply to the same incoming message is reworded. Inspect the
ledger and original destination before a deliberate manual retry.

Sender overrides require the extension manager's `inbox.message_policy`
preflight. An older manager cannot silently ignore the selected identity.
A bot-not-in-chat error asks the owner to add the bot; no failure changes to
the user's account. This feature never auto-approves tool-permission cards.

## Claude assistant profile compatibility (#3345)

Claude CLI assistant starts explicitly select a config directory per conversation, rather than inheriting the node service's `CLAUDE_CONFIG_DIR`. Terminal Agents, Codex and external runtimes retain their existing launch rules. A scoped node stays in its own profile.

- **Existing conversations:** retain their recorded `config_dir` across process/node restarts and launcher or service-default changes. Without a record, the node uses the profile containing the session's actual transcript (the existing newest-transcript lookup across discovered directories), then saves that selection. Private profiles, the machine directory, `~/.claude`, remembered/discovered profiles and recorded directories participate in discovery. If no transcript is found, it uses the old resolution rules: the CLI's interactive-shell/service environment, overridden by a launcher's own profile or plain Claude's machine `claude_start_dir`. It never guesses a new private directory for an old ID.
- **New conversations:** default to `<PERSONA_HOME>/profiles/<launcher>`; an explicitly profile-selecting launcher retains its own profile. The service-level variable remains available as a **legacy default**, not a new-conversation default. No credentials or transcripts are copied. Before the first process starts, the node runs `claude auth status --json` against an automatically selected private profile: an explicit signed-out answer refuses the start with `not_logged_in`, `delivered: "none"` and profile-specific sign-in detail, while signed-in, unknown, old-version, unreadable and timeout results continue. Explicit profiles, resumes, scoped nodes, launcher-owned profiles and managed providers skip this preflight. An empty fresh profile may require authentication; to retain an existing account/profile for a new conversation, explicitly select it or use `config_dir: "legacy"` when creating the conversation. The conversations the hub starts by itself (the background supervisor, a scheduled task's new conversation, `conversation_new`) have nobody in front of them to sign in: they start in `legacy`, as a meeting's room and follow-up do. Only Claude Code has profiles: one whose launcher runs Codex or a runtime starts with none. The hub leaves the profile out for a launcher it knows runs another agent (#6467), one hidden from the pickers included; for one it cannot tell (the machine's launcher list did not come), the node refuses the profile before starting anything (`config_dir is only supported for Claude assistant conversations`) and the hub starts it again without one, as on a node too old to be told a profile (#6884). A background supervisor started before that in an empty profile, whose last turn ended `not_logged_in`, counts as gone and is replaced by one that starts signed in (#6015). A scheduled task's own conversation from before that, whose last turn ended `not_logged_in`, is resumed only in `legacy`: the node refuses it when it is in another profile, and the run starts a new conversation that the task keeps (#6186). A Codex or runtime conversation is resumed as before, also when the node lists it with no agent: the node refuses the profile for it, and the run goes on in it without one (#6884). The node reports that ending only in the turn's words: `turn_error` while the conversation's process runs, and only its last words (`last_text`) once it is listed from its transcript with no process, as a task's conversation between runs and every conversation after a node restart are. `session.get` answers the node's session as it is, so these checks read the turn error the way the lists do (`latestTurnError`).
- **Explicit selection:** `assistant.start` / `assistant.send` and `assistant.warm` accept `config_dir`: an absolute path, a `~/…` path on the target machine, `"legacy"` (resolve the old rules once and save the result), or `"default"` (Claude's unset-directory behavior). `session.send` honors a selection for a headless conversation, but cannot move an existing one. `"default"` preserves the distinction between `~/.claude/projects` with `~/.claude.json` and an explicit `~/.claude` profile with its own `.claude.json`. This is a per-request advanced API option, not a hidden persistent preference or a migration that runs automatically. A conflicting wrapper or a request to change an existing conversation's profile is refused before starting it. Warm-process reuse also requires the same profile.
- **Choosing it ahead of time (#4633):** the Main chat's `config_dir` in Model settings (“New chats' Claude profile”) sets which profile a conversation the user starts gets when it names none: Persona's own (`private`, the default above) or this computer's existing setup (`legacy`), for Claude Code only. A machine whose only sign-in is in the default profile (a gateway in `~/.claude/settings.json`'s `env`, an OAuth login) chooses `legacy` there instead of signing the private profile in. Nothing is copied, no existing conversation moves, and the hub's own conversations and explicit selections keep theirs (`docs/assistant/model-config.md`, “Main chat's profile for new conversations”).
- **Rolling upgrades:** nodes advertise `assistant_config_dir`. A manager refuses a nonempty explicit selection on an older node before sending the request; omitted selection retains that node's old behavior. Session selection is an additive JSON field in the existing state metadata, not a database schema migration. An older manager may drop the field; a newer node can rediscover the unmodified transcript on its next start.
- **Rollback:** old conversations and their files are never moved. Before downgrading a node, stop its assistant processes and preserve the selected directories. For conversations created in a new private or explicitly selected profile, configure the old version's service or a profile-specific launcher with `CLAUDE_CONFIG_DIR=<saved directory>` before resuming the same ID. Several profiles require matching launchers; one global variable cannot select them all at once. For `"default"`, leave the variable unset. Old binaries do not understand `config_dir`; merely downgrading without selecting the profile is **not** an automatic migration. The transcript remains in Claude's unchanged `projects/<project>/<id>.jsonl` format and opens through the old environment-based path. Do not delete profiles on downgrade.

Coverage: `TestAssistantProfileCompatibility` runs CLI fixture processes for old service/home/machine/discovered/missing/recorded sessions, new defaults and explicit/legacy selections, including a changed service default after a process restart. `TestAssistantNewPrivateProfileMustBeSignedIn` verifies the exact preflight directory and that a signed-out profile starts no model process; `TestAssistantNewProfileAuthFailsOpen` covers unreadable status, managed providers and launcher-owned profiles. `TestAssistantProfileSelectionBoundaries` covers wrappers, scoped nodes and warm spares; `TestAssistantSpareIsAdoptedOnlyInTheStartsProfile` warms and starts through the RPCs with the profile changed between them, either way, and kept; `TestAssistantProfileCannotMoveExistingConversation` refuses relocation; `TestAssistantProfileRollbackKeepsTheOriginalTranscript` exercises the old env-only lookup. `TestNodeConfigDirIsPersisted` verifies the real Postgres metadata round trip; `TestMachineCallGatesAssistantConfigDir` verifies old-node refusal. These fixtures do not claim credentialed live-model or deployed downgrade verification.

**What a private profile takes of `~/.claude` (#7318).** A conversation in a profile Persona created (below
`<data dir>/profiles`, where new conversations start) reads that profile's settings and instructions, not the user's.
Its directory below the home still makes Claude Code read `~/.claude/CLAUDE.md` and every `~/.claude/rules` file as
the home's project instructions, which are written for coding Agents, so its start settings exclude both
(`claudeMdExcludes`, for the home as written and as its symlinks resolve, merged with any excludes already there). The
assistant's own `CLAUDE.md` and the profile's are kept. Instructions from the global file that the assistant still
needs belong in Settings › Assistant › About me, which its instructions carry. The skills the user turned off in their
own `settings.json` (`skillOverrides` set to `"off"`) stay off; their own is the profile plain `claude` starts in on
that machine (`machines[].claude_start_dir`, else the node's `CLAUDE_CONFIG_DIR`, else `~/.claude`). A name the
conversation's own settings set keeps its value, and nothing else of that file (env, hooks, permissions, plugins) comes
along. The skill picker reads the same list. Every other profile is one the user or a launcher set up (`~/.claude`,
the node's `CLAUDE_CONFIG_DIR`, `claude_start_dir`, a wrapper's `CLAUDE_CONFIG_DIR`, an explicit `config_dir`
elsewhere) and nothing changes there; choosing `default` or `legacy` for a new conversation keeps the global file.
Guests keep their own isolation (`node/assistant_user_setup.go`; `TestPrivateProfileExcludesTheHomeClaudeInstructions`,
`TestPrivateProfileCarriesTheUserSkillSwitchesOff`, `TestClaudeStartDirIsTheUserProfile`,
`TestSkillCatalogMatchesThePrivateProfileSettings`, `TestGuestSettingsIgnoreThePrivateProfileAdditions`).

## Cold launch preparation

A headless Claude assistant starts through the user's interactive shell. Before the assistant produces valid protocol
output, launch preparation has a separate 60-second allowance from process creation. The first protocol output starts
an ordinary 15-second settings acknowledgment budget; shell banners and stderr diagnostics do not count as readiness.
Controls are written immediately because some CLI versions produce no output until they read one.

A startup control timeout is retried up to three times before failure, within the caller's cancellation/deadline.
Retries happen before any user input is written. Explicit settings refusals and caller cancellation are not retried.
An empty conversation allocation follows the same preparation rule without sending a model turn. Effort remains
unconfirmed until a control ACK: `--effort` cannot express automatic effort, and launcher environment or policy can
override a startup flag. Initializing the in-memory field alone would silently skip that check.

Logs record shell initialization completion and elapsed milliseconds. Timeouts distinguish shell initialization,
assistant startup output, blocked settings writes and missing settings acknowledgments, and identify node-initiated
termination. Exhausted startup attempts report their count, elapsed wait and final cause rather than presenting the
node's termination as an assistant crash. Failed-message persistence and later redelivery are separate concerns.

## Work computers and personal context

Set `machines[].trust` to `"work"` in `hub.json` to keep personal assistant data off that computer (see [config.md](../operations/config.md)).
Ordinary Agents can still run there and receive tasks from personal computers and the assistant. Their task prompts do not receive the automatic knowledge-base project background or previous project outcomes. The task text the user or assistant explicitly supplies is still sent.

Assistant conversations, including guest conversations and the Inbox AI twin, cannot start on a work computer. The manager refuses before preparing the assistant directory or sending instructions, memory locations, connector skills, or the first message. An Inbox twin already associated with a computer later marked `work` receives no further notes and is marked failed; its conversation is not adopted after a restart. Select a personal computer for the twin to start new handling conversations.

Omitting `trust`, or setting it to `"personal"`, preserves the existing task-context and Inbox placement behavior.

The manager verifies the calling machine for `mcp.tools`, `mcp.call`, `assistant.context`, and `assistant.model`. The node's `assistant` flag only requests the assistant tool set. The configured assistant computer and Inbox twin computer can bootstrap sessions before runtime session IDs are available; a known ordinary Agent or host pane does not acquire assistant privileges. A conversation explicitly started by the manager on another personal computer is authorized during startup and then by its recorded machine and session. Runtime tool discovery without a session ID can rediscover the tool schemas on that computer after startup; execution still requires its recorded session. Guest conversations retain their restricted tool whitelist and receive no live context. `assistant.lark` gives the command gates, their credentials and the extensions' MCP servers only to the configured assistant computer; another personal computer gets the gate rules and lark-cli's name without its profile, so a conversation the manager starts there (a Lark `/agent`) has its lark-cli calls classified by risk level instead of all confirmed; a work computer gets nothing.

Changing trust does not erase context already delivered to a computer or terminate an existing process. New personal-data tool calls and Inbox deliveries are refused; ordinary Agent supervision and question / approval cards remain available.

## Current structure

| Capability | Implementation |
| --- | --- |
| Ask on the phone | MCP `ask_user` → hub creates a `question` interaction (`tool_name: ask_user`, session is this chat) → existing App question card + push ("Persona Has a Question") → answer returned as tool result. The card waits up to 2 hours (`askUserWait` in `assistant/tools.go`, #9), longer than the Lark nudge's default 60 minutes, so an owner who is away is nudged while it still waits. The turn that asked is blocked on the answer (a message the user sends meanwhile would wait behind it), so when the user writes in the conversation (`UserSpoke`) the question is withdrawn at once: the tool returns "the user wrote a new message instead; it is delivered when you end this turn, so end it", and, with the interaction queue, the card closes as dismissed with `reason: "replied_in_chat"` (Lark: "Replied in the chat"; what the user types next is a message, not a late answer to it). Without the queue (the hub keeps the cards) the question just disappears from the snapshot. The take-back happens when the message is received, before it is sent: if the send then fails, the question is already gone. Only the conversation's own `ask_user` is taken back, not an Agent's card or a permission. The question is not resumable: a manager restart ends it, tells the hub each card it ended (`fleet.interaction_resolved`, `restoreInteractions`), and sends the originating conversation one invalidation notice with the interaction ID, a minimal safe summary of the original question, confirmation that nothing was applied, and instructions to create a fresh `ask_user` card if still needed (#4568, #13159). An originating conversation can also withdraw its own pending `ask_user` card when obsolete via `ask_user_withdraw` (with an optional short reason); cross-origin cards and user-owned approval/plan/screen interactions are strictly rejected, and repeated withdrawal of an already-terminal card is idempotent (#13159). A card that expires unanswered becomes a to-do waiting for the owner's call (in the to-do list, the live snapshot and the briefing's "Can wait"); the tool says so, and the owner's later decision on it comes to the asking conversation as a new message (`docs/assistant/todos.md`, "Questions nobody answered", #1617). With `wait: false` the tool does not wait: the card is raised, the turn ends, and the answer comes as a message ("Asking without holding the turn" below, #4928). A waiting question that a message from elsewhere is queued behind lets go of the turn after two minutes and becomes such a question, its card unchanged (same section, #7392) |
| Push | `notify_user` → APNs (arrives with the App closed), tapping opens this chat; no duplicate "completion" push at the end of the same turn. Also sends to Lark if the Lark bot's "Send notifications to Lark too" is on, and to plugin channels with "Push" on (sent by the assistant itself, `docs/extensions/bots.md`, `docs/extensions/channels.md`). The completion push body is the turn's answer from its first words on, cut to 200 characters: every text of the turn in order, except a passing line of at most two sentences said right before a tool call (`transcript.AnswerParts`, the same rule as the Lark answer), so a report written before a tool call leads the push rather than the short line after it (node `assistantAnswer`, the conversation row's `last_text`) |
| Chat in Lark | Private chat with the Lark bot = talking in main chat; answers return to Lark, questions / approvals use Lark card buttons (`docs/extensions/bots.md`) |
| Return on a schedule | `schedule_followup` (`at` once; `every: "30m"` repeats by interval) / `list_followups` / `cancel_followup`; when due, hub sends the prompt (starting with "⏰") into the original chat (`assistant.send` uses `--resume`), or starts a new one on failure |
| Scheduled tasks | One table ("Scheduled tasks" below), default "Morning briefing" on weekdays 09:00 enabled, "Evening review" weekdays 21:30 disabled; normal "completion" push afterward (with the task's `facts` on, as `docs/extensions/bots.md` "Morning and evening briefings" says, Persona computes what waits and what failed, and the push is the counts). When told "every day / on weekday X at…", the assistant creates one with `schedule_routine` (marked "Created by Persona" in the App), deletes with `delete_routine`. With `facts` on, the briefing is also the daily digest of handed-out work, told from each task's brief (#3822, `docs/assistant/unified-entry.md`, "Briefs"): "On <date> you said: <their words>" (or who started it and its goal), where it stands in a line, what is needed from the owner, overdue and scope changes since the last briefing; what ended since is listed once with each criterion's result, and work that needs nothing and has not changed is left out. The mark of the last briefing is the setting `assistant.brief_digest_at`; a list has at most 8 rows |
| Run history | Every run (chat started successfully / failed to start / skipped because downtime missed it) is stored in settings `assistant.runs` (latest 200), `GET /api/v1/assistant/runs[?ref=]`; scheduled tasks include `last_status` |
| Due time | Scheduling sets a timer for the next due time (sleeps at most 30 seconds, recalculates on list changes), not 20-second polling. Follow-ups missed during hub downtime run once on return, with lateness stated in the prompt. A run into a conversation the owner is talking in waits for them, 5 minutes past its time at most ("Messages from the system wait for the owner") |
| Background tasks / Monitor | The assistant can use Bash `run_in_background` and Monitor to watch events, then end its turn; command completion / events make claude start another turn itself (node considers it working, with normal push on completion). node learns running tasks from stream-json `system/background_tasks_changed`: don't reclaim the process while tasks exist (up to 12 hours); task lists live in `node_sessions.tasks`. personad restart (deployment) kills tasks with the process; on startup node sends these chats an explanation so the assistant decides whether to restart them; the task list is cleared only once that message went, so a failed one is sent again by the next connection (#6306) |
| Tools without approval | The assistant process allows Persona's MCP tools (notify_user, schedule_followup, agents_list, kb_*…) without phone approval each time; the registered and allowed namespaces follow the hub capability and resume policy in "Built-in MCP namespaces" below. Actions representing the user (mail_send, phone writes) are confirmed by hub itself; Lark writes are confirmed by a Bash gate ("Lark: skill supplied by the Lark extension" below). Other Bash / Monitor calls follow the session permission mode; assistant conversations default to `bypassPermissions` when no manager or conversation mode is selected |
| Exact owner-action batch approval | Opt-in `owner_action_prepare` shows complete canonical connector calls and managed built-in before/after changes as durable batch and item cards. An authenticated exact reply or direct Allow grants only those original payloads once. Built-in `persona_settings` writes and profile switch/rollback execute in order at their approved revisions; the first failure stops later grants and reports the partial point. Edited connector text/recipients, relayed claims and wrong locations execute nothing before a fresh ordinary card. Denied or consumed exact built-in calls cannot bypass their durable decision. Optional persistent proposals survive deadline/caller/restart until Allow or Deny. Both switches are off by default; no Always or standing grant is created. See [Approval clarification](approval-clarification.md#exact-replies-for-an-owner-action-batch) |
| Agents | `agents_list` / `agent_read` / `agent_send` / `agent_start`: use hub session state (`decorated`) and node `session.items` / `session.send` / `session.start` directly, not ssh. With `worktree.branch`, `agent_start` first has the computer's engine make the worktree through node `worktree.prepare` / `worktree.create` (the workspace found by `cwd`; herdr's or Orca's own, so Orca and the app list it), then starts the Agent in it, instead of the Agent running `git worktree add` itself (#5325, `assistant/agent_worktree.go`) |
| Answer questions on the user's behalf | `interaction_answer`: when the owner says "answer that question for me, choose A", the assistant answers other Agents' **question-type** interactions. Only `kind: question`, excluding the assistant's own chat; `permission` / `plan` / `screen` are always rejected with an explanation that only the owner can answer in the App (no proxy approvals is a hard boundary). Validate every question: all must be answered, keys must be exact question text, choices must be listed (an answer that writes out exactly one option, its label then its own description as `label：description`, with `:`, a dash or the description in brackets, quotes or spaces around it, counts as that label, #3929), single-select exactly one, multi-select joined as `A, B` like the App; reject free text (answer in the App or use `agent_send`). Expired, answered, dismissed, cancelled, and nonexistent interactions each return their own error (the latter four consult history). `Resolution.AnsweredBy = "assistant"` accompanies interaction history (`GET /api/v1/interactions/history`) and `interaction_resolved` broadcasts; the App marks "Answered by Persona". On Agent questions, the assistant first decides whether to answer, see "Assistant answers questions first" below. `agents_list`'s waiting rows include `interaction_id` and options for every question, avoiding another lookup (tools and rules all belong to the assistant: `toolInteractionAnswer` in `assistant/tools_apps.go`, tests `assistant/tools_apps_test.go`; the interaction queue also lives in the manager: `interactions.get` returns the interaction or its historical outcome if no longer waiting; `interactions.answer` records validated answers as assistant answers, directly in-process, `assistant/fleet_routes.go`) |
| Interrupt, adopt, report | `agent_interrupt`: stop an Agent's current turn immediately, optionally followed by a new instruction (`agent_send` queues behind the turn); `agent_switch`: when the user asks (a subscription's usage limit stopped Agents, say), move a running Claude Code Agent to another launcher on its computer and go on with it, now or not at all (a busy Agent is refused with nothing typed, #5973, `docs/assistant/agent-work.md` "Usage limits"); `agents_switch`: when the user asks, move many running Claude Code Agents to another launcher at once (a preview first, one confirmation; usage-limited ones continue, idle ones wait there, busy ones are queued until their turn ends, stopped ones resume there, #6008); `agent_supervise`: bring an Agent on no open task under the caller's supervision (the main chat, a direction's lead within its bounds, or a live side conversation for an Agent it started or a task it already supervises; not an Agent waiting for the user, on another conversation's open task, or busy on work Persona did not start; #12953, #13657); the Agents' `report_to_supervisor`: an Agent on a supervised task reports a needed decision, blocker, completion with evidence, or progress into the supervision message. See `docs/assistant/unified-entry.md` ("Supervision", #5112) |
| Archive Agent sessions | `agent_archive` / `agent_unarchive`: when the user asks, archive or restore a coding Agent's session with the same manager-owned, reversible archive the apps use (`setAgentArchive`, `docs/protocol/protocol.md` ("Agent session archives")); nothing is ever deleted. Archive and restore change nothing on the computer and need no confirmation (like `conversation_archive`). `agent_archive` with `close: true` is "Archive and close" (`archiveAndCloseAgentSession`): a busy Agent is refused before anything is asked, then the user confirms on a phone permission card every time (also outside the health space, as `persona_settings`), and a declined card changes nothing. The result states what happened for the assistant to repeat: archived or not, the native archive (`native: done / pending / unsupported`), and the close outcome (`closed`, `not_running`, or `refused` / `offline` / `failed` with the localized reason: the session stays archived and still open). Busy Agents are refused with nothing changed; an Agent awaiting the user's reply is not busy. `agents_list` leaves archived sessions out unless `state` is `archived` (only those) or `all` (marked `archived`). Persona conversations are refused (`conversation_archive`). `assistant/agent_archive_tools.go` |
| Dispatch tasks | `task_plan`: hand Agent work to the user, one task per to-do (`origin` = this chat). Read-only and user-remembered tasks start directly; others become plan cards awaiting "Start" without blocking the turn. Progress appears in the live snapshot's "Tasks started from chat"; completion receipts, answered questions, and direct task messages enter chat activity ([unified-entry.md](unified-entry.md), `assistant/tasks.go`) |
| Telegram | Available only with the Telegram extension (extension market `nwverse/persona-extensions`, #1704, #2057): read-only MCP `telegram_chats` / `telegram_messages` / `telegram_search`; more flexible reads and writes use the bundled telegram skill (Bash `tg …`, i.e. `personad tool --cli telegram`, through hub, writes confirmed on the phone). `tg` is on PATH only while Telegram is logged in (`docs/protocol/connector-protocol.md` ("Command line")) |
| Lark | The open-source version uses official lark-cli ("Lark: lark-cli" below). With a Lark-gateway extension installed, mainly use its skill (extension commands in Bash, both reads and writes), see "Lark: skill supplied by the Lark extension"; MCP `lark_chats` / `lark_messages` / `lark_search` also come from the extension (since #1571 no Lark gateway client in hub, `docs/plans/plan-extensions.md` ("Step 3")). The old read-only `lark_api` and `lark_send` were removed as duplicates of the skill |
| Control Persona itself | `persona_settings` (`list` first, an assistant tool, `assistant/settings_tool.go`: About Me, direction notes, default launcher, and `main_tasks_quiet` are assistant-owned settings, read / written by it with its own phone confirmation cards; other settings belong to hub, listed through `app.call settings.list`, read / written through `app.tool persona_settings`, with hub confirmation): read / write triage rules (global `inbox.rules` and per-plugin-source `inbox.rules.<plugin>` for sources declaring `triage_rules`, overriding global when present), every configurable extension's form settings (`extension.<id>`: `get` lists each field, `set` changes only the fields given, each shown before → after on the card, #2385), inbox task-linking rules (`inbox.agent_rules`: which running task a needs-you item belongs to; what the AI twin takes on is inbox policies. Reading or changing it says when the AI twin is off, #5865), About Me, direction notes (`direction.<id>.prompt`: readable in any assistant conversation, set only by the main conversation as a whole string or a unique `old` / `new` fragment replacement; `docs/assistant/unified-entry.md` "Direction notes"), default launcher, "Main chat tasks notify only in main chat" (`assistant.main_tasks_quiet`), per-machine muted Claude config directories (`agents.blocked_claude_dirs`), push grading for every device (`notifications.low_distraction`, the compatibility key for the hub's push grading switch, #8157; `docs/server/server.md` "Push grading (push urgency filtering, `hub/push_grading.go`)"), and add / remove mail accounts. Changes first show a phone confirmation card; added-account passwords / authorization codes are entered on the phone card, never through the model or chat / interaction history. After approval, hub tests IMAP / SMTP and encrypts / saves only if both work. Gmail requires Google sign-in in the App; launchers are written to unified model configuration while retaining existing model and effort |
| Live snapshot | Each message to the assistant starts with `<persona-context>` (not system prompt, see "Live snapshots and prefix caching"): time (phone timezone), online computers, Needs you and Unread Agents plus the count of working ones (`agents_list` lists them on demand, #11261), unread connector chats and @me, today's upcoming meetings, scheduled follow-ups, and up to five open Inbox matters being followed by check time. Agents and tasks include web links: hub `public_url` (default `http://<hostname>:18660` when unset) plus `/s/<machine>/<session id>`, `/todos?id=<to-do id>`; the assistant uses Markdown links when mentioning them. Omit if hub can't supply the address (#2194). The main conversation's task lists (task details, open-task index, waiting to be verified or approved) hold every task; a leader's or side conversation's hold only the ones it handed out or supervises (#8666, `docs/assistant/todos.md`). Include only changes since this chat's previous snapshot (time always included): changed sections, and inside the task details, the Agents section and the unread chats only the rows that changed (#9078). hub gives each source 3 seconds, reuses reads for 45 seconds, and messages wait at most 1.5 seconds for fresh data, not at all while reads take longer than that (#8948) |
| Faster | When the App displays a new-chat page, `POST /api/v1/assistant/warm` starts a spare node process (`spare`, valid for 10 minutes), used directly by new chats. `personad mcp` marks the Persona tools assistant conversations use often `anthropic/alwaysLoad`; the rest, and other MCP servers' tools, keep Claude Code's ordinary ToolSearch deferral (#5838, #7317): the manager picks them (`coreTools`, `assistant/tool_loading.go`), adding `leader_progress`, `todo_create`, `ask_escalate` and `direction_name` for current leader conversations from their session id; tool loading does not expand caller permissions. `assistant.load_all_tools` (`persona_settings`, off by default) loads every Persona tool up front again, in running conversations too: the change wakes each `personad mcp`'s tool watch (`assistant/mcp_tools_changes.go`), which sends the new list (`notifications/tools/list_changed`); a `personad mcp` older than tool refresh keeps its list until its process restarts. On Anthropic's own API the assistant's processes carry `ENABLE_TOOL_SEARCH=true`, so a conversation that once ran without ToolSearch defers too instead of loading every tool until its next compaction; behind a gateway they carry `false` and load every tool, and a gateway's refusal of tool search is resent once that way and remembered (#10365, `docs/integrations/claude-integration.md` "Tool search behind a gateway (#10365)") |
| Memory | Claude Code auto memory (`~/.claude/projects/<encoded directory>/memory/*.md`); App Settings can view / edit / delete it (node `assistant.memory`). Codex and DSH read / write the same memory ("Codex", "Agent runtime (DSH)" below), so changing launchers doesn't lose it, and a Claude conversation in any profile is told to use it too ("One memory" below). Health chats have a separate health space and separate memory (`docs/assistant/health.md`) |
| About Me | App edits go to settings `assistant.notes`, override `assistant.notes` in the manager's `manager.json`, and append to CLAUDE.md |
| Phone not in foreground | No phone connected during a `phone_*` call → hub sends a silent push (`content-available`, `persona_wake`) → App connects WS in the background to execute, disconnects after 25 seconds; wait up to 20 seconds. The App proactively disconnects WS on backgrounding, or a suspended socket makes hub think it is online and calls wait 2 minutes. Once disconnected, nothing else in the background reopens it (network recovery, refresh completion, reconnect backoff, `LiveConnection.suspended`, 2026-11-02); only this silent push (`wake()`) connects. The same push with `persona_wake: "widgets"` wakes a phone whose Home Screen widgets show other agent counts than the hub's (`server/internal/hub/widget_wake.go`, #5721): the app connects, writes the widgets again and disconnects |

`agent_start`, `agent_send` and an immediately dispatchable single-task `task_plan` accept optional integer `wait_seconds` (0 by default, capped at 50). This observation budget begins after the existing launch/send receipt: dispatch and owner feedback run first. Approval cards, multi-task plans and task-leader worker plans keep their immediate receipts. Negative and non-integer values are rejected before dispatch.

A wait appends an `Inline observation` JSON record with `input_id`, known machine/session/link, the last observed `agent_state` when available, and one state: `completed` with this input's final `reply`, `question` with the existing question and `interaction_id`, or `timeout`, `cancelled`, `unavailable`, `interrupted`, `failed`. An exact input echo and fresh idle/done snapshot are required. Historical text and old idle snapshots cannot satisfy it. Missing echo support, a large exchange outside the bounded recent window, or later ambiguous input can time out. A question remains unanswered. Expiry, cancellation and a lost connection never resend input or cancel dispatched work. Supervision, conclusion delivery and `task_review` continue unchanged; an inline reply does not accept the task.


The "their computers" section in the instructions (`~/.persona/assistant/CLAUDE.md`) reflects the assistant's **actual machine** (fixed 2026-10-31; previously recognized only kind local):
- For the assistant's own computer, regardless of kind: "This is the machine you are on; run commands directly."
- `ssh_host` is an ssh alias on the hub machine, so commands `ssh <alias> '<command>'` for other ssh computers are supplied only when the assistant runs locally on hub (kind local).
- Otherwise (assistant on another computer, or a link target without ssh), don't provide unusable commands; explain that direct execution isn't possible and use `agent_start` / `agent_send` for work.

These instructions are written on that computer when the assistant prepares its directory (`assistant.prepare`: new chats, App new-chat prewarming, AI twin; `claudeMD` / `prepare` in `assistant/start.go`),
based on hub facts: `devices.list` (machine kind, ssh alias, home, data directory), knowledge base and other note directories (since 2026-11-04 the knowledge base belongs to the assistant, previously hub `kb.dirs`), `connectors.signed_in` (signed-in connectors, their CLIs, whether the manager's manager.json configures lark-cli), and `plugins.assistant` (plugin instructions and skills). A running claude process
uses what it read at startup; new instructions take effect on its next start: after idle reclamation (20 minutes; the main chat's follows `assistant.idle_release_minutes`), or rebuilding that node connection, the next message starts a fresh process.

The node keeps the signed-in connector list and CLI mappings from `assistant.prepare` **per assistant directory** (#5831), resolving symlinks so prepare and the running session use the same key.
Preparing another directory, or signing a connector out there, leaves this directory's connector permissions and command wrappers intact. Skill synchronization uses the destination directory's list;
connector CLI calls first identify the assistant process and then check its session directory's list. An unprepared directory has no connector permissions.
The default assistant keeps `assistant-bin/` under the node's data directory; other assistant directories use `assistant-bins/<directory hash>/`, with that directory first on their process PATH.
The node's `skills.list` accepts an optional `cwd`. Without it, the node uses the default assistant directory if it has been prepared; otherwise, if exactly one directory has been prepared, it uses that directory. With no prepared directories or several non-default ones, it still uses the default directory's empty connector list. No new configuration is required.

Only the assistant gets these tools: node's inline-JSON MCP configuration includes `PERSONA_ASSISTANT=1`, `PERSONA_SESSION_ID=<chat id>`, and `personad mcp` carries both to node.
node does not take the flag on the server's word, because every process started with the assistant's environment repeats it. It forwards assistant=true only when
the socket's peer process or one of its ancestors is an assistant process node runs (a chat, the warm spare, a Codex process before its thread exists), with that chat's session id,
or a connected runtime that declared `caps.assistant`, with the session the request names when it can be one of that runtime's assistant chats ([runtimes.md](../integrations/runtimes.md) ("Assistant (optional)")).
Where the system does not tell the peer process (Windows), the request must name a session node runs for the assistant, or an assistant chat of a connected runtime that declared `caps.assistant`;
those ids cannot be guessed, but any process of the same user can read them from the assistant's command lines, so there the check stops only inherited environments. Any other request goes as an Agent's (`node/clicall.go` `mcpAssistantOf`).
hub lists these tools only for assistant=true and forwards calls to the assistant (`assistant/tools.go`). Ordinary Agents in herdr can't see them, even with the assistant's environment;
a program run under the assistant's own process (its Bash tool) counts as the assistant, as for `personad tool`, and so does an Agent in an engine whose server that shell started without detaching it.

## Role-scoped supervision instructions (#8251)

**Audience: assistant supervisors.** All ordinary assistant conversations share a working directory and the generated `CLAUDE.md` / `AGENTS.md`. Those files contain common interaction and task-planning instructions, not a role-specific supervision manual. Never rewrite them per conversation; switching the main/background supervision mode leaves their bytes unchanged.

The manager derives `supervision_instructions` from settings and the task ledger, overwriting caller-supplied values on `assistant.send`, internal `session.send`, warm starts and model resolution (`SendParams`, including `assistant.model`). Its nonempty value is the exact rendering of `prompts/assistant/supervision_instructions.md`, with no time, session, title, task or event interpolation; the one addition is a current leader's own direction notes (#10086, `docs/assistant/unified-entry.md` "Direction notes"): what the owner wrote for that direction and its kb project's name and goal, appended after the fragment from `prompts/assistant/direction_notes.md`, only while the notes are not empty. Editing them changes that leader's policy alone, so the rules below restart that one process (busy turns first finish) and no other:

- Main receives it on the main route’s computer to supervise its own work and handle leads’ owner-decision escalations. The legacy `main_supervises_own_tasks` switch no longer changes new-task ownership. Main does not directly drive another lead’s Agents.
- Current `assistant.leaders` receive it on the assistant's computer. The persisted background supervisor receives it there too, even after a setting switch, so it can finish already-delivered work.
- An ordinary side chat receives it while an open task's ledger names it as supervisor (#4669). A former direction with such tasks retains it for that actual responsibility; after its last task closes or moves, it loses the fragment. A missing inventory is not proof that an authoritative ledger assignment disappeared. Known non-assistant, health and guest conversations receive none. If ownership cannot be read, dispatch fails rather than silently removing the side chat's required rules.
- A new main and its warm spare receive the main role before an id exists. The first background start is explicitly marked by `startBackground`; requesting model purpose `assistant.supervise` alone is not authorization. An ordinary new conversation receives none.

Node applies the bytes with Claude CLI `--append-system-prompt` or Codex `developerInstructions` on both `thread/start` and `thread/resume` (after its existing memory instructions). The policy belongs to the execution fingerprint, so identical policy with new task facts reuses a process, a different role resumes in a fresh process, and a warm spare cannot cross role boundaries. A role-changing send while a turn or background work is active is refused as busy, without killing work or delivering input on the old system policy; manager-generated messages remain queued for an idle retry. Other model-only changes keep their existing busy-turn behavior. Tool authorization remains manager-enforced throughout. Per-session state retains the policy for node-local resume; explicit empty manager metadata clears it even without a model fingerprint. Compacting a stopped conversation refreshes it from the manager when available.

Custom assistant runtimes expose user-message context only unless they explicitly opt into a dedicated policy channel via `caps.supervision_policy: true` (reflected on non-sub-Agent session rows as `capabilities.supervision_policy: true`, #11173). Absent or unknown capability is treated conservatively as unsupported:

- **Runtimes without `supervision_policy`** (such as DSH and older runtimes) keep the exact fail-closed boundary from [#10338](https://github.com/nwverse/persona/issues/10338) and [#11121](https://github.com/nwverse/persona/issues/11121). A runtime-backed conversation without `supervision_policy` takes ordinary replies on its own runtime, even if it appears in `assistant.leaders` or owns a task from an earlier release. A new plan or explicit transfer to such a direction lead is refused until its runtime supports supervision policy; it must not silently hand the lead's work to main. Other incompatible side-conversation plans retain the compatible main route. The planning conversation keeps its task card. If that route has no compatible reviewer, the plan stays available to start after the supervision setup is corrected. A legacy unmarked task such a conversation already supervises (handed out by an earlier release) is followed up by that same route: its supervision messages, live and already queued, go to the background supervisor while it is on, else to the main conversation (`digestTarget`). A current persisted plan/transfer assignment instead waits for its assigned lead to become compatible. Because such a runtime has no separate policy channel, the node refuses the manager's own supervision message sent through one (`assistant_supervision_runtime`), drops a policy that arrives with any other message (an owner's reply the hub could not tell was for a runtime conversation) and delivers the message without it, and never sends `supervision_instructions` over the wire. The hub retires a supervision message refused with `assistant_supervision_runtime`, as it does one refused for good with any other code ("Between conversations" below), after that one try, tells the owner how to follow up using the saved task and worker reports, and does not move the message to another conversation (#11393).
- **Runtimes declaring `supervision_policy`** (such as the Antigravity CLI companion) receive manager-derived `supervision_instructions` explicitly on `session.start` (when nonempty) and `session.send` (including explicit `""` on demotion). The hub allows capable runtime sessions to act as main, side-chat, leader or background supervisors; the node tracks persisted `SupervisionInstructions`, refuses busy role changes with `busy` before dispatching, and preserves coded runtime refusals (`assistant_supervision_runtime`). Capable runtimes must apply `supervision_instructions` out of band (never concatenated into user message text and never rewriting the shared `~/.persona/assistant/AGENTS.md`) and fail closed with `assistant_supervision_runtime` if the policy cannot be applied. Antigravity implements this by atomically writing per-session isolated rule files under `<agyDir>/persona-supervision/<rawId>` (splitting across `AGENTS.md` and `GEMINI.md` up to 48,000 bytes) and attaching that directory exclusively to the supervised session via ACP `additionalDirectories` (`integrations/antigravity/lib/policy.js`).

Supervision event bodies contain facts only, including the signal kind and any question-routing timeout/mode. Direction/background role context and the background startup notice likewise contain role facts, not another supervision manual. All fixed and kind-conditioned handling lives in the static fragment, not in each event. `TestSupervisionInstructionsDispatchIsolation`, `TestSupervisionInstructionsFirstStartAndWarm`, `TestAssistantSupervisionStartupResumeIsolation`, `TestCodexSupervisionDeveloperInstructionsStartResume`, `TestSupervisionWarmSpareIsolation`, `TestSupervisionCapableRuntimePolicyLifecycleAndFailClosed`, `TestCapableRuntimeSideChatSupervisesOwnTasks`, `TestCapableRuntimeLeaderAndBackgroundSupervisor` and `TestCapableRuntimeFailClosedRefusalStopsRelay` exercise dispatch, startup/resume, and capable vs. non-capable runtime isolation. The instruction and event goldens pin the bytes; the size ratchet measures the fragment once along with shared instructions and event payloads.

## Per-criterion acceptance progress (#3635)

`todo_supervision.done_when` remains the ordered list of criterion text. The additive
`criteria` JSON field stores the latest state of each entry: `criterion`,
`state` (`pending`, `met`, `not_met`), `evidence`, `at` (UTC RFC 3339), and `by`
(`agent:<machine>/<session>`, `supervisor:<conversation>`, or `judge`).
An old row with only `done_when` reads as all pending, without invented timestamps
or confirmers; reviews and other ledger fields are not rewritten. Reordering
unchanged text keeps its evidence; adding or changing text starts pending.

An Agent uses the existing `report_to_supervisor`, authenticated as the working
Agent of that task, to report one or more entries by their **one-based** position:

```json
{
  "kind": "progress",
  "text": "The tests pass; the PR is still pending.",
  "criteria": [
    {"number": 2, "state": "met", "evidence": "go test ./internal/db: PASS"}
  ]
}
```

`met` and `not_met` require nonempty evidence (up to 600 characters); `pending`
resets the entry. Out-of-range or duplicate numbers, invalid states, and missing
evidence reject the whole batch. Omitted entries keep their state. The ledger is
persisted before the tool succeeds and the normal to-do change event refreshes
the web count immediately. A progress report still does not wake the assistant;
the recorded states and evidence accompany the next supervision event. A
question, blocker or completion report follows its existing delivery rules.

The supervisor's `task_review.checks` writes each matching `done_when` entry back
as `met` / `not_met`, with the review time and actual reviewing conversation.
Unchecked entries are preserved; unmatched checks stay in the review history.
Existing accepted reviews from the done judge also record their checks.
These reports are evidence, not acceptance: no count closes a task, changes
dispatch, runs tests, or substitutes for the supervisor's own review.

To-do API responses include `supervision.criteria` for all clients. The web task
cards and to-do list keep their existing N/M badge. The task detail's existing
`review-checklist` renders persisted criteria through the same `checklist()` and
`MARK` states and existing translations; older hubs fall back to review checks.
Evidence, timestamp and the recorded reporter stay in that existing detail
section. No new component, panel, disclosure or translation is added, and the
existing checklist and marks live with the lazy to-do page rather than the
first-screen card badges. The first-screen byte budget stays unchanged. Mobile UI changes,
automatic CI observation, and criterion dependency gates are outside this change.

## Boundary with hub (2026-11-04, #1446)

hub is only a conduit: the assistant runs as its own process (`personad assistant`), connected by the bus (`docs/protocol/protocol.md` ("Bus")). hub forwards App requests, caches assistant snapshots, delivers pushes and App messages, and doesn't run assistant code:
`go list -deps ./internal/hub` excludes `internal/assistant`; shared code is only message types, method names, error codes, and the assistant route table (`server/internal/assistantwire`), plus small text helpers (`server/internal/textfmt`).

All assistant business logic lives in `server/internal/assistant`, which doesn't import hub: its settings (machine, launcher, timezone, About Me, `main_tasks_quiet`, `GET/PUT /api/v1/assistant`, those `persona_settings` entries); main chat (creation, compaction, retained background from older chats, title "Persona", no renaming); chat creation (directory preparation, CLAUDE.md, prewarming, memory API, `POST /api/v1/assistant/conversations`); launcher and model per message; chats (archive, inter-chat messaging, `conversation*` tools); chat titles; scheduled tasks (scheduler, run history, `schedule_*`, `/api/v1/assistant/tasks` and related routes); dispatched tasks (plan cards, chat activity, receipts, auto-completion decisions, closing finished Agents, "Today" card, `/api/v1/task-plans`, `/api/v1/task-trust`); to-dos (moved from hub 2026-11-04: storage, all `/api/v1/todos…` routes, automatic project links and derived following, herdr markers, decision records; `docs/assistant/todos.md` ("Who owns what")); Agent management (moved from hub 2026-11-04: stopped-turn triage, auto-continue, wakeups, its cards and answers, four switches, two `session_work` tables, statistics; new Agent tab names, launcher and model for new / resumed Agents, `assistant/agents_*.go`; `docs/assistant/agent-work.md` ("Responsibilities")); all its MCP tools (`ask_user`, `notify_user`, `agents_*`, `task_plan`… including `interaction_answer` using hub's interaction queue and Inbox `inbox_draft`: parameters, rules, and model-visible text belong to the assistant, hub supplies only data, `assistant/tools_apps.go`); message-prefix live snapshots; model configuration (moved from hub 2026-11-05: records for four categories, Persona-added providers and keys, presets, rate-limit pauses, `/api/v1/model-config…` and `/api/v1/launchers…`, every model call, `assistant/models*.go`; `docs/assistant/model-config.md` ("Who owns models")); channels (moved from hub 2026-11-04: the chat half of Lark bot and plugin channels, target chats, turn tracking, answers and questions sent back, commands, push and sync, `/api/v1/channels*`, `assistant/channels*.go`, `feishu*.go`; hub retains only transport, see "Channels" below and `docs/extensions/channels.md` ("Responsibilities")),
Inbox (moved from hub 2026-11-04: items and states, triage, event synthesis, muting, AI twin, item translation and generic translation API, all `/api/v1/inbox…` and `/api/v1/translate*` routes; hub handles only sources, `docs/assistant/inbox.md` ("Ownership: the manager owns the Inbox")),
and knowledge base (moved from hub 2026-11-04: engine, loops, files in the data directory and index tables, `kb_*`, all `/api/v1/kb…` routes; hub handles only sources, `docs/assistant/knowledge.md` ("Who owns what")).

**The assistant accesses hub through one interface**, `assistant.Hub` (`assistant/hub.go`), implemented by bus client `assistant.BusHub` (`assistant/bus.go`). Every method corresponds to a bus method:

| Method | Parameters → result | How hub answers (`assistantLink` in `hub/assistant_link.go`) |
| --- | --- | --- |
| `manager.hello` | `{id, host?, os?, arch?, deploy?, version?, capabilities}` → `{assistant:{machine_id, machines, standby?}, done_delay_ms, persona_view?, html_views?, models?, notify?, locale?, mutes?, legacy_config?}` (`owner`, `owner_notes` empty; `legacy_config` only to a built-in manager the hub did not start, [managers.md](managers.md) ("Configuration")) | What the hub knows that the assistant needs: `machines` are the computers this manager owns, `assistant.machine_id` the hub's default machine (its own, else the first, `defaultAssistantMachine`) when this manager owns it, else its first own one (`placedFor`; don't use a stored assistant machine absent from the list), `standby` is `isMember` (member hub in a linked group; primary runs scheduled tasks), `done_delay_ms` is hub.json notify completion delay (how long a task Agent must stop to count as stopped) and `notify` its push timings; `persona_view` and `html_views` are always true, for a manager from before #5509 that still reads them (the cards are always on; a current manager reads neither); `models.launchers` come from this manager's machines' `machines[].launchers`. `capabilities` names `config.own`: the assistant reads its own sections from its `manager.json` (`docs/operations/config.md` ("manager.json")) and lays them over the answer, as the defaults for its settings: `assistant.owner`, `assistant.notes`, `assistant.machine` (used when it is one of this manager's machines), `assistant.launcher`; `kb` (knowledge storage, `docs/assistant/knowledge.md` ("Who owns what")); `agents` (triage limits and initial switches, `docs/assistant/agent-work.md`); the models `ai`, `inbox.triage_ai` / `judge_ai`, `kb.ai`, `inbox.agent.machine` (legacy model baseline, `docs/assistant/model-config.md` ("Who owns models")); `hook_timeout_minutes`. An older hub still sends these from its hub.json, and a section of `manager.json` replaces them. The assistant remembers answers; changes send `manager.updated`, and every answer has it read its `manager.json` again |
| `device.call` | `{device, method, params}` → that computer's result | node methods (`session.items`, `session.rename`, `session.send`, `session.start`, `session.retire`, `todo.sync_session_tokens`, `assistant.prepare`, `assistant.send`, `assistant.warm`, `assistant.memory`, `assistant.compact`, `assistant.handoff`, `launchers.list`, `launchers.probe`) forwarded unchanged to its node (`Machine.call`). hub answers `devices.list` (`device` is `*`: machines' id, name, kind, ssh alias, home, data directory, online state; phone online, `machinesNow`, `phoneClient`) and `interactions.list` (`device` is `*` for all of the manager's pending cards, or a machine id for that machine's plus machineless global cards; answered in-process by `localFleetDeviceCall` when the manager owns the fleet, else by the hub from `pendingInteractions`, never forwarded to a node), `sessions.list` is the App's session list (`decorated`) plus `muted` (sessions with push disabled, which triage won't open cards for); `session.get` is node's listed entry (`Machine.session`). Missing machine returns `assistant.ErrNoDevice` |
| `app.call` | `{app_id, method, params, timeout_ms?}` → application result (`timeout_ms`: assistant wait, also stopped by hub when due, e.g. a model call) | `interactions` (queue in the manager since 2026-11-05, answered directly in-process, `assistant/fleet_routes.go`; `interactions.owner_action` still belongs to hub, redeeming owner actions before channel answers): `interactions.get {interaction_id}` → `{interaction, ok}` (`ok` is false when no longer waiting, `status` is historical resolved / dismissed / expired / cancelled, empty if nonexistent); `interactions.answer {machine_id, session_id, interaction_id, answers}` → `{ok, status}` (answer still-pending questions for that session from `answers`, recorded as assistant answers; `ok` is false if no longer waiting; errors for non-question or wrong session). `connectors`: `connectors.snapshots` (live signed-in connector state: waiting chats, today's meetings, for snapshots and Today cards, `connectorSnapshots`), `connectors.signed_in` (enabled, signed-in connectors, CLIs, manager.json lark-cli configuration); knowledge sources (2026-11-04, `kbSourcesCall` in `extmgr/kb_sources.go`, types from `internal/kb`, `internal/connector`): `connectors.archives {}` → `{archives:[{id, poll_ms?, sources?, media?, history?, history_anchors?}], items:[id]}` (connectors to read now: signed in, knowledge ingestion on, with docs / meetings / calendars), `connectors.changes {id, cursor, limit}` → `{batches, next, more?}`, `connectors.archive_media {id, ref}` → `{media:{mime, data}}` (up to 20 MB), `connectors.items {id}` → `{items}`, `connectors.doc_title {id, url}` → `{title}`; importing older history, read-only, for a connector declaring it (`docs/protocol/connector-protocol.md` "History"): `connectors.history_chats {id}` → `{chats}`, `connectors.history {id, query}` → `{messages?, older?, newer?, more?, unreadable?, pace?}`, `connectors.history_anchors {id, query}` → `{anchors?, next?, more?, pace?}`. `plugins`: `plugins.assistant` (instructions and machine-specific skills; already-installed skills include only name and summary), `plugins.skills_need` (node reports missing skills, include files next time); model-config plugin calls (2026-11-05, `hub/assistant_models.go`, removed when plugin management moved into a manager): `plugins.presets` → `{presets}` (provider presets), `plugins.voice` → `{plugins:[{id, name, models, default, error?}]}`, `plugins.need_triage` → `{names}` (enabled plugins needing information filtering). `settings`: `settings.list` (hub-owned settings readable / writable through `persona_settings`). `inbox` (hub sources, `inboxAppCall` in hub `inbox.go`; items belong to assistant): `inbox.collect {round}` (pull-to-refresh: hub reads sources immediately, delivers via `inbox.collected`, then answers), `inbox.kick` (request a round soon), `inbox.config` → `{rules, agent_rules, agent, lark_account, translate, extensions}` (triage / AI twin config, `lark.account`, requested translation languages, plugin connectors; assistant computes the translation-model fingerprint); `inbox.mail_body {account_id, uid, mailbox, readable?}` → `{from, to, cc, date, subject, text}` (full mail, no read marking; `readable` preserves paragraphs / links for HTML-only translation); `inbox.mail_flags {account_id, mailbox, uids}` → `{seen, unseen}` (which of at most 200 mails are still in the mailbox, read or not; one in neither is gone; nothing is marked; the Inbox's mail off the latest page, #5993); `inbox.reply_mail {account_id, uid, mailbox, text, send_id}` → `{already?}` (reply using the receiving account); `inbox.reply {source, reply, send_id}` → `{already?}` (connector / Lark plugin replies or doc-status changes, "Can't reply" if connector absent). The same `send_id` sends only once (App-shared `sent_messages`; the records do not expire); repeats return `already`, interrupted partial sends return `assistant_in_doubt` and aren't resent. `inbox.settle {source, threads}` → `{over}`, `inbox.group_tool {source, call}` → `{result}` (avatars removed, truncated to 8 KB; connector `unknown_method` preserved), `inbox.group_fallback {source, fallback}`, `inbox.group_append {source, append, append_id}` → `{thing}` (repeated `append_id` uses the plugin's previous answer without another write). `app.tool {name, arguments, machine_id, session_id}` → `{content, is_error}`: only settings `persona_settings` remains, handling hub-owned settings (triage rules, muted config directories, mail); assistant-owned entries live in `assistant/settings_tool.go` |
| `notify.request` | `{notification:{id, kind:"assistant", title, body, subtitle, device_id, session_id, asked?}}` → `{ok, devices?, kept?, held?, reason?}` (where it went: `docs/protocol/protocol.md` "Bus"; `notify_user` tells the model whether a device took it) | Push, same per-device settings as Agents (`notifyDone`); `asked` means `notify_user` explicitly requested it, so this chat's turn won't also push "completion" (`notified`). The assistant sends to the Lark bot and plugin channels itself (`channelsPushed` in `assistant/channels_handler.go`); the hub does not |
| `inbox.changed` | `{items:[{id, source, kind, title}]}`, one-way | Items changed and were saved (up to 50 per report): hub sends plugins `inbox.changed` and App `{"type":"inbox"}` (even without items, for triage-state changes). Runs outside the bus read loop (slow plugins don't block other assistant messages); if backlog exceeds 256, skip plugin delivery for that batch but still push to the App |
| `ws.broadcast` | `{message}`, one-way | Live App updates, `message` sent unchanged through `broadcast`: `assistant_schedule`, `assistant_feed`, `task_plan` |
| `manager.snapshot` | `{epoch, seq, at, partial?, sessions:[{machine_id, session_id, title?, archived?, tags?, queued?}], interactions:[…], tasks:[…], work:{on, sessions:[{machine_id, session_id, work}]}, profile:{machine_id, main?, launcher, timezone?, keep:[{machine_id, session_id}]}, todos:{links:[{machine_id, session_id, todo_id, title, task?}], quiet:[{machine_id, session_id}]}}` → `{ok, seq}` | hub retains only the newest (within an epoch, non-increasing seq returns `stale_snapshot`). `decorated` fills session titles (main chat "Persona"), archives, tags, queued messages, a chat's model pick (`model_pick`, [model-config.md](model-config.md) "A conversation's own model"); changes push that machine's session list once. `interactions` are assistant phone questions (`ask_user`, `persona_settings` for its own settings; headless Claude lacks AskUserQuestion), displayed / pushed / expired as normal cards (`waitInteraction`); answers return via `manager.action`, missing entries are withdrawn. `tasks` is `null` if unreadable; the hub no longer keeps it (the model configuration that read it is the manager's now). `profile` contains assistant settings needed by hub features (if unreadable, `null`, retaining the previous copy), below. `todos` (`assistantwire.TodoReport`) maps Agents to unfinished to-dos (`task` means chat-dispatched task) and quiet Agents; `decorated` fills `todo`, `quiet`, `withTask` labels cards, `quietSession` suppresses push (`hub/assistant_todos.go`); unreadable means `null` / incomplete snapshot and retains prior state. Changes push that machine's list. `work` is each Agent's latest-turn work state (`docs/assistant/agent-work.md`); `decorated` applies `work` and derived `state` only to sessions still on that turn; unreadable `null` retains prior state. `interactions` also includes triage cards (`tool_name: persona_triage`, asking about that Agent session; if disconnected, hub stores the answer and delivers after reconnect) |

**Computers belong to the assistant** (2026-11-04, [machines.md](../server/machines.md) ("Who connects to computers")): the assistant process also manages all computers, with `internal/devices` connecting each (hub no longer does).
Messages with hub live in `server/internal/devicewire`, over the same bus (`docs/protocol/protocol.md` ("Computers (devices.*)")); `assistant/devices.go` hands hub `devices.*` calls and events to it. `BusHub` has only a few calls:
node-owned methods in the table's `device.call` go directly in-process to that computer's node, without hub (errors remain `apierr.ErrOffline`, `rpc.ErrClosed`, node `rpc.Error`); hub-owned methods (`assistantwire.HubDeviceMethods`: `devices.list`, `interactions.list`, `sessions.list`, `session.get`, channel `session.follow` / `session.unfollow` / `session.send_once`, and guarded `assistant.answer_again`) still ask hub.
node requests `assistant.model` and `assistant.context` are also answered directly in-process (`Service.NodeAnswer`), not forwarded by hub; the manager tells nodes the snapshot's `profile.keep` (above).

**hub accesses the assistant only through** `assistant.Handler` (`assistant/handler.go`; hub's `assistantRemote`, same messages), one message per method: App requests (assistant `assistantwire.Routes`, bus `http.request`, forwarded unchanged; caller role in `X-Persona-Role`, overriding any client-supplied value),
`assistant.tools` (hub lists them alongside its own tools for assistant chats), `assistant.tool`, `assistant.context` (node's pre-message live snapshot), `manager.action` (user answered an assistant question), `assistant.todo_handoff` (`POST /api/v1/sessions` or `…/messages` with `todo_id`: it decides whether the to-do exists, is done, and can be handed over; prepends effective decisions when handing to an Agent), `assistant.todo_link` (link the Agent to the to-do after handoff, taking a title from the initial prompt if absent),
`assistant.start` (hub features starting an assistant chat; AI twin, Lark bot, and channels start their own since 2026-11-04, so hub no longer uses it; `{text, images?, launcher?, permission_mode?, effort?, purpose?, machine_id?, main?, untitled?}` → `{session}`; AI twin uses `untitled`, avoiding assistant-style titles),
`assistant.send_params` (`{machine_id, session_id, params}` → `{params}`: launcher and model for a message to an assistant chat, the chat's own pick when the message has none (a pick that can't run refuses it with `launcher_out_of_scope` / `launcher_own_service` / `launcher_missing`, passed on as they are); a new main chat starts empty and its first message includes the old main chat's handoff. Phone and node-generated messages (`assistant.model`) ask it; Lark bot / channels compute internally. For an unlisted machine that isn't main chat, return unchanged; malformed requests return `bad_request`, mapped by hub to 400),
`assistant.rename` (`{machine_id, session_id, params}` → node result: App renames assistant chats, main chat rejects with `bad_request`; Codex changes only the assistant's stored title, while node renames still use its `session.rename`),
`models.usable` (`{purpose}` → `{usable}`: hub plugins ask whether information filtering can be called), `models.launchers` (`{}` → `{launchers: [{name, base_url}]}`: gateway-address launchers for extension `gateway.launchers`, no keys), `models.launcher_http` (`{name, machine?, http?}`: authenticated requests for usage plugins and launcher panels, `docs/integrations/launchers.md` ("Quota"); absent launcher `no_launcher`, absent gateway `no_gateway`), `agents.start_params` (`{machine_id, params, name_from?}` → `{params}`: launcher, model, tab name for App-created Agents; unusable configuration `bad_request`, hub 400), `agents.resume_params` (`{machine_id, session_id, params}` → `{params}`: launcher / model when resuming sessions with unknown launcher), `agents.tab_name` (`{prompt}` → `{name}`),
knowledge base (2026-11-04, `assistant/kb_bus.go`): `kb.context {chat_id}` → `{context}` (AI twin chat context, `null` if absent), `kb.projects {machine_id, cwd}` → `{projects:[{slug, name}]}` (projects whose directories hold cwd on that machine; it and `kb.context` still answer, but nothing calls them now), `kb.forget {source}` → `{chats, people}` (delete source data, indexes, project references, cursors); plugin posts handed to hub (after checking knowledge ingestion is on, forwarded with connector name): `kb.posts_put {connector, sources, posts}` → `{statuses}`, `kb.posts_comments {connector, id, count, comments}` → `{}`, `kb.posts_pending {connector, limit}` → `{posts}`, `kb.archived_chats {connector, sources, source}` → `{chats}`,
one-way `kb.kick {connector?, scope?}` (source changed: run a round after 5 seconds and read that connector's chats, or the items, at that round even before their poll, at most once a minute; a manager before #1562 sends `{}`: only the round), `kb.agent_turn {machine_id, session_id, cwd, title?, line}` (Agent turn completed, append to its cwd project's timeline; hub queues while assistant disconnected and delivers on reconnect),
hub-observed events: `session.user_spoke`, `session.user_stopped` (user stopped an assistant chat in the App: Lark /agent chats stop their dispatched tasks too, #3308), `session.turn_started`, `session.turn_ended`, `sessions.changed` (a machine's session list drives inter-chat messages, automatic project / derived to-do links, herdr markers, Agent titles), `interaction.resolved` (user handled an Agent card; record in task chat, and linked Agent choice questions become decisions), `todos.space_renamed` (hub renamed a workspace), `interaction.raised` (quiet task Agent asks, #1731), `session.seen` (user viewed a session, for triage statistics), `manager.updated` (handshake answers changed); removed on 2026-11-04: `assistant.tasks`, `assistant.card_sent`, `assistant.cards_sent`, `inbox.message`, because Inbox moved into the assistant,
and `manager.resync` (a request for a full snapshot; the assistant still answers it, but the hub no longer sends it).
Inbox (`assistant/inbox_bus.go`): `inbox.collected` (hub hands over a source round: `{epoch, seq, round, sources:[{id, name, off?, extension?, migrated?, group?, meta?, meta_description?, settles?, kinds?, things, read_error?}], mail?: {accounts, failed?: [{id, email, read_error}]}, names}`; assistant synthesizes / triages items then answers; `epoch` identifies this hub run, `seq` the round, discard older already-used rounds; unreadable stored Inbox returns `inbox_unavailable`, retried next round), `inbox.item {id}` → `{item, ok}` (stored JSON for plugin detail slots), `inbox.forget {source}` → `{deleted}`, `inbox.repo_mutes {source, set?, add?, repos?}` → `{repos}` (GitHub settings "Muted repositories"); `inbox.mutes {source?}` → `{mutes}` and `inbox.unmute {id}` → `{}` (the Inbox Sources pages' Muted list, #5278); `inbox.post {on?}` → `{on}` (the Assistant source's switch on those pages, #6430: whether the assistant may put its conversations into the Inbox; on unless turned off); `inbox.triage_preview {source, name, kinds?, things}` → `{verdicts: [{tier?, summary?, reason?, urgent?, state?, why?}]}` (the pages' Try Triage with the Model: at most 3 things, each asked about once as a round's item would be, nothing kept). Snapshots add `inbox: {agents:[{machine_id, session_id}]}` for AI twin chats, suppressing hub completion push (`null` if unreadable, retain previous).
Hub errors cross the bus with codes (`no_device`, `machine_offline`, `node_closed`, `stale_snapshot`, `assistant_in_doubt` for interrupted partial Inbox replies), restored by the assistant to original errors with unchanged decisions. App routes, events, fields, and pushes are unchanged.

**State** uses the assistant's `assistant.Store` (`assistant/store.go`): settings `assistant` (its own profile, below), model config `model_config.*`, `launchers.managed`, `launchers.hidden`, `launchers.zshrc` (`docs/assistant/model-config.md` ("Who owns models")), `assistant.main_handoff`, `assistant.titles`, `assistant.archived`, `assistant.conversation_tags`, `assistant.direction_sessions` (the owner's per-computer/session direction filing book, including explicit on-its-own entries, #11391), `assistant.model_picks`, `assistant.relay`, `assistant.quotes`, `assistant.handovers`, `assistant.tasks`, `assistant.runs`, `tasks.trust`, Inbox `inbox_mutes` (legacy `assistant.routines` / `assistant.followups` still migrate only on first read), channel `channels`, `channels.state`, `bots.feishu.state` (since 2026-11-04; `bots.feishu` credentials, pairing, and switches still belong to hub); tables `task_plans`, `assistant_feed`, `todos`, `todo_spaces`, `todo_agents`, `todo_decisions`, `inbox_items`.
Knowledge state is files in `~/.persona/data`, the manager's `handles.key` (`~/.persona/managers/assistant/`), and `kb_entities` (`docs/assistant/knowledge.md`).
This is hub's same Postgres, with unchanged keys / tables and no data migration; the assistant connects itself using the same `database_url`. Only it reads / writes these keys and tables; hub doesn't touch them.
Scheduled-task and finished-task Agent-closing loops belong to the assistant (`Service.Run`, beginning only after first hub connection and handshake).

**Assistant settings** (settings `assistant`, exclusively read / written by the assistant since 2026-11-04, `assistant/profile.go`): chat machine, launcher, user timezone (phone `PUT /api/v1/assistant`; `timezone_picked` when a client says the user picked it by hand, which devices then leave alone, #1855), About Me, main chat, replaced old main chats, `main_tasks_quiet`, `approval_mirror_main` (on by default for new cards: non-private write-operation permissions from other Assistant conversations also appear in the main conversation, `docs/assistant/approval-clarification.md` "Main-conversation and direct-chat copies"), `approval_mirror_tools` (the configurable write allowlist for those main and direct-bot copies, with read confirmations excluded by default), `agent_tasks` (off by default, `docs/assistant/unified-entry.md` "Agent tasks"), `inbox_signals` (off by default: new Inbox messages on a supervised task reach its supervisor, `docs/assistant/unified-entry.md` "Supervision (#3124)"), `main_supervises_own_tasks` (a compatibility switch, off by default: new tasks belong to the planning lead or main, and `background_supervisor` names only a legacy conversation finishing older work, `docs/assistant/unified-entry.md` "Background supervisor"), `background_supervisor_keeps_context` (off by default: each of the background supervisor's rounds starts from its first turn with only that round's facts and tools; on, it is one long conversation as before #7315; `persona_settings` only for now, `docs/assistant/unified-entry.md` "Each round starts clean"), `limit_fallbacks` (empty by default: the launchers a conversation the owner relies on moves to when a usage limit stops it, and back from after the reset; `persona_settings` only, `docs/assistant/agent-work.md` "Usage limits: a backup launcher"), `machine_fallbacks` and `machine_fallback_minutes` (empty by default = off, threshold 10 minutes: ordered backup computers that the main conversation, direction leaders and unbound default scheduled routines fail over to when the primary computer stays offline past the threshold; `persona_settings` only, `docs/assistant/agent-work.md` "Offline computer: automatic machine failover").
`assistant.owner` / `notes` / `machine` / `launcher` in the manager's `manager.json` supply the defaults (laid over the hub's handshake answer); JSON matches the old hub-written form without migration. `assistant.supervision_batch_minutes` is an administrator pin instead: when present, including explicit `0`, it overrides the saved supervision interval; when omitted, the saved setting is unchanged. Reads are cached for 5 seconds (only it writes; `manager.updated` triggers immediate reread).
`GET/PUT /api/v1/assistant` also reads and saves `default_permission_mode` in this manager’s settings. Reads identify the answering manager with `manager`. New non-guest conversations without an explicit `permission_mode` inherit it, including new main chats, scheduled new conversations and side chats. A stopped Claude Code process resumes with the current default unless the conversation has an explicit mode; a live process keeps its current mode. An explicit session mode, including `default`, always wins over the manager default. Legacy Claude state and transcript modes without that explicit marker are inherited values, not a pin. Non-Claude launchers keep their legacy `AppMode` / in-memory / transcript precedence and do not receive the manager's resume default, even when their declared modes include it. An unset preference selects the built-in `bypassPermissions` default. Web Settings › Persona and Android's native Assistant settings expose the control; on the web, Direction notes links to the same control and Settings search finds it by either the permission or direction name.

The same endpoint exposes `readonly_mode` (`sandbox` by default, or the opt-in `prompt`), also available in Web Settings
and `persona_settings` as `assistant.readonly_mode`. It controls new read-only task starts and retries, not ordinary
conversation permissions or running processes. `prompt` is an instruction-only constraint, not a security boundary;
Codex keeps its normal launcher configuration and Claude keeps its existing dispatch behavior. Writes require an explicit
matching manager and full role; Agent-initiated changes require owner confirmation. Details and rollback:
`docs/assistant/todos.md`.

To change the default, name the target explicitly with `X-Persona-Manager: <manager id>` or `?manager=<manager id>`, including the built-in `assistant`. Capture that id when opening the settings and use it for every read and save; do not read a later global page selection. Conflicting header and query targets, an omitted target, an unknown target, an invalid mode, an empty string or `null` are refused without saving mixed fields. Omitting the field preserves it; sending `default` restores ordinary approval mode. Existing role checks apply. Web and Android assistant/model configuration pages show the selected assistant and keep requests and responses scoped to it. Native runtime policy remains subject to node version and managed restrictions; a saved preference alone does not prove the next turn accepted it.

The answering manager includes `permission_defaults: {"version":1,"manager":"<manager id>","modes":["default","acceptEdits","plan","auto","bypassPermissions"]}` in its configuration response. This describes its implemented scoped persistence and new-session inheritance; the Hub forwards it without synthesizing capabilities for older managers. Android enables the permission control only for version 1, a matching nonempty owner, and all five supported modes. Absent, partial, mismatched or unknown-version contracts leave it unavailable. Neither a manager identity nor an omitted optional setting proves support. Its save path validates the PUT owner and value, then separately reads GET and validates both again; responses from an earlier settings load cannot replace the current state. Other manager-scoped settings and navigation remain available.

Every snapshot includes `profile`, the subset hub needs. hub retains the latest (`assistantProfile` in `assistant_profile.go`) and persists changes under its own `hub.assistant_profile` key (hub-only):

- Identify phone messages to main chat (`isMainOn`), machine for model configuration's "Main chat" purpose, default launcher (`launcher`: assistant setting, else `manager.json`'s `assistant.launcher`, else claude), AI twin default machine, machine selection for `/api/v1/skills`, launcher tests and usage, and `assistant.lark` only for the assistant machine.
- User timezone (`userLocation`, `hub/usertz.go`): all hub time reads use it.
- `keep`: sessions nodes must list regardless of inactivity (main chat, replaced old main chats, scheduled-task chats, Agents linked to unfinished to-dos, #4510; the background supervisor; current directions' lead conversations, on the computer their direction record names, else the assistant's, #11392; the task leaders' Agents that opened or supervise unfinished tasks, and, 50 at a time until each is noted or given up, the chats to-dos name whose space was never noted, #10941, [extensions.md](../extensions/extensions.md) "Never shown to extensions" (while such a chat is kept, the assistant's conversation list has it too, quiet as it is); the sessions placed under current directions, #11515: per direction the owner's live filings, newest filing first, then the conversations its lead conversation opened, newest first, at most 50, on the computer each names; [unified-entry.md](unified-entry.md) ("The direction tree")). The part placed under directions is sorted by computer and session, so an unchanged list is not sent again. A listed row does not keep its transcript file. `keep` is left out (`null`: the nodes keep the list they were last given) when the scheduled tasks, the to-dos, the direction filing book, the recorded conversation openers, the conversation deletion record or the direction records cannot be read. `keep` does not change the age window of Codex Agent threads (a node lists them from rollouts written in the last 14 days; a kept one only gets past its agent filter, `server/internal/node/codex_registry.go`) or how other runtimes list their sessions. Since 2026-11-04 the assistant process connects computers ([machines.md](../server/machines.md) ("Who connects to computers")):
  on snapshot reporting, its device layer (`internal/devices` `SetKeep`) sends `node.configure keep_sessions` once to changed machines, all machines on the first snapshot; also supplied on node connection (`configureNode`). hub only retains this copy.
- **The first second or two after hub restart, before the assistant reports**: hub uses its persisted copy (`loadAssistantProfile`, before plugins and connectors start). Startup plugins need user timezone;
  an in-memory-only copy means they get hub timezone during these seconds and keep it until their own restart.
  (Knowledge base and Lark bot formerly depended on it too: the knowledge base reordered everything in hub timezone on each restart, then back two minutes later; Lark messages arriving then created a new main chat replacing the original. Since 2026-11-04 both belong to the assistant; knowledge startup reads its timezone directly and the assistant chooses Lark chats.)
  The first startup after moving settings lacks this copy: read once from assistant settings (machine, main chat, timezone in settings `assistant`) and save a hub-owned copy, never reading it again. `keep` is unknown then, so node connections omit `keep_sessions` (nodes retain their prior value).
  With no copy at all (fresh hub): the hub's own machine (else the first), no main chat, hub timezone, launcher `claude` (the hub no longer reads `assistant.machine` / `assistant.launcher`: they are the manager's).

**Channels** (moved from hub 2026-11-04, `docs/extensions/channels.md` ("Responsibilities")): the assistant owns the "chat with Persona" half of Lark bot and plugin channels—target chat, commands, sending into chats, turn tracking,
progress cards / answers, questions / approvals (cards, text) and answers, push / sync, source markers, and `/api/v1/channels*`. hub handles only transport: Lark's developer-platform persistent connection, credentials, pairing, owner checks, plugin `channel` programs.
The bus adds these (`assistantwire` channel types):

| Direction | Method / event | Purpose |
| --- | --- | --- |
| hub → assistant, call | `channels.receive {channel, message, action}`, `feishu.message {event}` → `{}` | Owner message (plugin channel; raw Lark private-chat event). Assistant returns once accepted / deduplicated by id, then processes internally. If disconnected, hub waits up to 60 seconds and retries dropped / timed-out calls; only then tells the owner "Assistant temporarily unreachable". A temporary dependency failure returns `try_again`, and hub redelivers |
| | `feishu.card_action {event, action}` → card callback answer | Owner tapped a bot card (verified by hub), Lark waits 3 seconds: if unfinished after 1.5 seconds, assistant first replies "Processing…", then edits the card on completion. `action` is one-time owner authorization for this card only |
| | `feishu.view {}` → `{target, agents}` | "Send to now" and bound Agent in `GET /api/v1/bots/feishu` |
| hub → assistant, event | `interaction.announced {interaction}`, `interaction.ended {interaction, resolution?}` | New cards (except quiet tasks), ended cards (answers included if answered): Lark cards and text questions |
| | `session.items`, `session.delta` | New items and streaming text for followed chats (`session.follow`): progress cards |
| | `feishu.owner {paired, greeted?}`, `channels.noted {channel, machine_id, session_id}` | Bot paired / unpaired; channel-origin message entered a chat through App API (voice `voice-…`) |
| Assistant → hub, `device.call` | `session.follow` / `session.unfollow {session_id}`, `session.send_once {…session.send parameters}` | Follow a chat (hub releases all on disconnect, assistant re-follows on reconnect); send with App-shared send-once protection (`sent_messages`) |
| Assistant → hub, `app.call` | `feishu.status`, `feishu.post`, `feishu.text`, `feishu.update_card`, `feishu.react`, `feishu.unreact`, `feishu.messages`, `feishu.resource`, `feishu.save`, `feishu.user_name`, `feishu.configure`, `feishu.test` | Lark transport: send / reply / edit cards and text (owner private chat only), reactions, read messages, download images, save files on the computer, switches and features |
| | `channels.list`, `channels.send`, `channels.typing`, `channels.enable`, `channels.unbind`, `interactions.resolve {interaction_id, resolution, action, channel}` | Plugin transport and channel list (no targets or plugin feature switches); answer cards from channel replies with owner actions issued by hub in this channel (no proxy approvals); reject missing actions or actions from another channel. Credential-bearing binding is hub's route, bypassing assistant |
| Snapshot | `channels: {marks:[{machine_id, session_id, channels}], turns:[{machine_id, session_id, answered?}]}` | Per-chat source channels (session-list `channels`), turns being / just answered in channels (hub suppresses completion push); `null` retains previous |

**Why model it around the bus**: interfaces are wire messages; hub's `assistantLink` is the only implementation, with just encoding / decoding added on the bus. hub no longer computes assistant state (titles, archives, queues, questions display exactly as reported), or understands assistant semantics (tool lists, prompts, question meanings belong to the assistant).
Benefits: clear ownership; assistant crashes / hangs don't bring down hub, and vice versa; independent restart / upgrade; the assistant can run elsewhere, eventually more than one; hub no longer gains state with every assistant feature.

**Extensions, connectors, and bots also belong to the assistant process** (2026-11-05, [extensions.md](../extensions/extensions.md) ("Responsibilities")): `internal/extmgr` runs extensions, connectors, mail, Lark and WeChat bots in-process;
`assistant/extensions.go` hands over hub `extensions.*` calls / events. Assistant `app.call` to `inbox`, `feishu`, `channels`, `connectors`, `plugins` (Inbox / knowledge sources, channel transport, connector CLIs and extension skills) is answered directly in-process, not via hub;
`inbox.changed`, `kb.changed`, `kb.forgotten`, `todo.changed`, `todo.decision` destined for hub first go there for extension subscriptions. Its hub needs (cards, push, model-config queries) use `app.call` app `hub`. Channel messages described above as "hub forwards to assistant" (`channels.receive`, `feishu.message`, `feishu.card_action`) and Inbox `inbox.collected` now arrive from it in-process, with unchanged methods / parameters.

**Still in hub** (move later):

- App rename API (`renameSession`): assistant chats go to `assistant.rename`, others unchanged.
- Assistant-only tools doing hub work: hub-owned `persona_settings` entries (triage rules, muted config directories) return through `app.tool` (mail and source-specific triage rules belong to the manager's extensions; hub forwards `extensions.hub_call settings.*`). Lark tools belong to its connector plugin, never the assistant.

**No longer in hub**: `findAgent` / `machineByName`. The assistant resolves Agents and machines over the bus (`devices.list` / `sessions.list`) and keeps `conversationName` after moving the Lark bot; extmgr keeps its own `machineByName` (`extmgr/machines.go`).
The text helpers have one implementation (`server/internal/textfmt`): the hub aliases `clip`, `oneLine`, `planTitle` (`hub/tools_shared.go`), the assistant also `firstLine`, `ago`, `short`, `agentLabel`, `stateNames` (`assistant/text.go`); `kb`, `extmgr` and `usagesource` alias `Clip`, `Cut` (no ellipsis) and `Short` the same way.

Observable differences:

- When a queued message enters a running chat after its turn ends, immediately push the session list (queued entry disappears), rather than waiting for the next working-state push.
- While assistant is disconnected (hub startup, assistant restart), APIs / tools fail with `503 assistant_offline`, "The assistant is temporarily unreachable, try again later". Lists retain its last snapshot; during the first second or two after hub startup, assistant chats lack titles.
- `ws.broadcast` is re-encoded by the hub: it drops `manager` and any `event_seq` the assistant set (the event log assigns its own; an `extension_operation` skips the log and carries none), so keys go out in alphabetical order rather than in the assistant's field order.
- After settings / main chat moved on 2026-11-04, these also return `503 assistant_offline` while disconnected: `GET/PUT /api/v1/assistant`, prewarming, memory, new chat, compaction, new main chat, phone sends and renames for assistant chats;
  Lark bot / channel main-chat sends and new chats reply "Assistant temporarily unreachable" in-channel; the AI twin item fails with the same error. node-generated messages fall back to the chat's own launcher if it can't ask (same as asking an old hub).
- If Android creates an empty main chat and the first message comes from Lark / a channel, include the old main chat's handoff too (formerly only phone sends did; Lark missed it, leaving background pending until the next phone message).
- After scheduled tasks get their own chats, replaced old main chats stay listed by node too (the assistant's old `keep_sessions` omitted them until node reconnect).
- After Agent management moved to assistant (2026-11-04, `docs/assistant/agent-work.md` ("Responsibilities")): creation waits up to 60 seconds only on hub startup before the assistant has ever connected. If unavailable then, or disconnected after connecting, create / resume returns `503 assistant_offline` (it connects computers, [machines.md](../server/machines.md) ("Who connects to computers")). hub stores card answers and delivers after a new snapshot; triage pauses, then the next check within 15 seconds catches turns finished but unread during absence. Triage switches, statistics, `POST /api/v1/tabs/autoname` return 503; lists retain the last work state. In the first second or two after hub restart, triaged sessions lack `work` and display Unread.
- After knowledge base moved (2026-11-04): disconnected assistant means `/api/v1/kb…` 503, `kb_*` failures, delete-source API 503 with no deletion. Archiving waits without loss (its cursors, plugin redelivery next round, Agent timeline lines queued by hub); `docs/assistant/knowledge.md` ("Who owns what").
- After to-dos moved (2026-11-04): disconnected assistant means to-do APIs and create / send with `todo_id` return 503; automatically project-linked / derived-following Agents gain to-do fields in the next push, not the same one. `docs/assistant/todos.md` ("Who owns what").
- After Inbox moved (2026-11-04): disconnected assistant means `/api/v1/inbox…` and `/api/v1/translate*` return 503 `assistant_offline` (rejected replies write no receipt, can retry later). hub keeps reading sources on schedule and buffering for assistant (merged, bounded, memory-only); reconnect triggers a complete collection including buffered data, preserving items that slide out of windows like latest 40 mail / latest 30 iMessages.
  Disconnected / timed-out in-flight replies return 502 `assistant_in_doubt`, `delivered: "unknown"`: they may have sent. No receipt is written; retries use the same `send_id`, so hub doesn't resend completed sends.
  Plugin Inbox detail slots can't render then; GitHub Inbox settings (CI, muted repositories) are entirely hidden (providing settings without muted repositories would clear them on save); connector-data deletion reports an Inbox error. `docs/assistant/inbox.md` ("Ownership: the manager owns the Inbox").
  Previously hub collected immediately on startup; now it waits for assistant connection (hub starts its child, usually a second or two).
- After channels moved (2026-11-04): disconnected assistant means `/api/v1/channels*` 503 except hub-owned `POST …/bind`; Lark bot / plugin receivers keep receiving, owner messages wait up to 60 seconds before "Assistant temporarily unreachable" (plugins: "Not sent: the assistant is temporarily unreachable, try again later"),
  and Lark buttons get the same toast. Lark bot settings (`/api/v1/bots/feishu*`) still work except target / bound Agent. Questions appear only in the App during absence and catch up in channels on return; first startup seconds lack session `channels` (completion push waits up to 10 extra seconds for the first report). The assistant sends the successful "Paired" card with help; hub sends a short version if absent. Images / files handed to assistant total at most 40 MB per message (bus limit, formerly 50 MB per file). `docs/extensions/channels.md` ("When the assistant is away").

## Process (2026-11-04, #1446)

`personad assistant` is the assistant: it reads hub.json (`~/.persona/hub.json`, same environment as hub: `PERSONA_DATABASE_URL`, `PERSONA_LISTEN`) and its own sections from `~/.persona/managers/assistant/manager.json` (`docs/operations/config.md` ("manager.json")), connects to the same Postgres and hub `/api/v1/bus` with a bus token,
handshakes, reports a full snapshot, then starts scheduled tasks. On disconnect it reconnects (1 second, doubling to 1 minute), reporting a full snapshot on every connection.

- **Default (no user changes)**: `personad hub` generates a fresh assistant token with `createBusToken`, starts `personad assistant` as a child from its own binary, and writes the token to stdin (not argv, invisible to `ps`);
  a hub run as `persona-hub` starts `persona-manager assistant` beside it instead, when that is the same build (`version -v`), else the `personad` beside it (`hub.managerBinary`, [../server/server.md](../server/server.md) ("Programs"));
  `PERSONA_ASSISTANT_HUB` is hub's local address. Child output joins hub stderr logs with `assistant:` prefix. On exit, hub restarts it with backoff from 1 second to 1 minute, resetting to 1 second after a run of at least 1 minute.
  On hub shutdown, SIGTERM first, kill after 10 seconds. If hub disappears (including crashes), stdin closes and the assistant exits too, leaving no unreachable orphan. No systemd, Docker image, or `deploy.sh` changes needed.
- **Run elsewhere**: set `"assistant": {"external": true}` in hub.json to stop hub spawning it (`docs/operations/config.md`); on the machine where it will run, generate a token with `personad hub assistant-token`,
  then `PERSONA_ASSISTANT_TOKEN=<token> PERSONA_ASSISTANT_HUB=<hub address> personad assistant`. It must reach hub's Postgres.
  For an `https` hub with its own self-signed certificate, add `PERSONA_ASSISTANT_FINGERPRINT=<fp>` (the fingerprint `personad hub pair` prints): the assistant pins it instead of checking the system's CAs (`docs/operations/config.md`).
  The knowledge base follows it, in that computer's data directory (copy the hub machine's old `data/` into its data directory and `managers/assistant/handles.key` and `secret.key` into its `managers/assistant/`, before the manager first starts there, to retain them; [managers.md](managers.md) ("The built-in manager's own files")); `docs/assistant/knowledge.md` ("Who owns what").
  So does its configuration: `~/.persona/managers/assistant/manager.json` on that computer. The connector, extension, bot, Lark and Inbox sections move into it once from the hub.json there; the owner, notes, Agents, `kb`, `ai`, the Inbox's models and a card's wait come from the hub's `hub.json`, which the hub hands over at the handshake for it to take over once (that computer's own `hub.json` copies of these are not used) ([managers.md](managers.md) ("Configuration"), `docs/operations/config.md` ("manager.json")).
- Disconnected-assistant hub behavior is at the end of the previous section. In child mode the hub changes the token at each startup, so an assistant elsewhere needs `external`; a service of its own on the hub's machine is bound once instead (`assistant.mode` `service`, [managers.md](managers.md) ("The built-in manager as its own service")).
- It also manages all computers (2026-11-04, [machines.md](../server/machines.md) ("Who connects to computers")): after receiving handshake `devices`, connect hub.json machines (hub-local `node --stdio` is now its child),
  and a second WebSocket `GET /api/v1/bus/relay` with the same token receives Persona Link and other-hub node streams forwarded by hub. On shutdown, hand online machines to the successor (warm start), then disconnect.
  While absent, computers show offline and machine APIs return `503 assistant_offline`; on return it reconnects them itself.

- **Other managers** (2026-11-06): managers created in the App run the same program as `personad manager run --dir <its directory>`. Identity, hub address, and token live in `manager.json` in that directory;
  the knowledge base, `secret.key`, and extensions also live there, with its own database (a Postgres of its own that install makes in Docker, never the hub's). The handshake reports its own id. Installation, directories, database, and revocation: [managers.md](managers.md) ("A second manager").

## Lark: skill supplied by the Lark extension

> History: this route began with hub directly connecting the Lark gateway, with an unchanged upstream skill copy (`SKILL.md`, `references/`, command scripts) and an upstream sync script in the repo.
> On 2026-11-04 (#1468), the gate became a generic "command gate". Since #1571 / 3c (#1520), the copy, sync script, and hub-generated gate are removed; skills, instructions, gate rules, and credentials come only from extensions.
> Since 2026-11-05 (#1317), credentials don't enter the assistant process either ("Credentials" below).

Requested: the assistant must not be limited to MCP `lark_*` tools (too restrictive), but directly read / write with the gateway's bundled skill; copy just the skill, not the entire upstream repo.

The current mechanisms are generic; specific commands, routes, and variables are in each extension's instructions (such as the Lark extension):

- **Delivery to the assistant**: extension contribution `assistant` (`docs/extensions/extensions.md` ("Assistant")) supplies a skill directory and instructions. Skills **aren't in personad** (upstream redistribution isn't allowed, `docs/operations/hub-update.md` ("1. Skills move out of the binary"));
  node places them on the assistant machine and links them into `~/.persona/assistant/.claude/skills/` at each process start. User skills of the same name in `~/.persona/skills/` take precedence.
  Gated commands get a one-line wrapper in the assistant directory's command bin (the default assistant uses `~/.persona/assistant-bin/<command>`), first on the process PATH. Without the skill there is no command, and assistant CLAUDE.md mentions only MCP `lark_*`.
- **Credentials**: hub gives them only to the assistant node (RPC `assistant.lark`), from extension `assistant.credentials`. They stay in node's local proxy (`node/gateproxy.go`);
  the assistant gets only a local-only address that still requires write confirmation. `env` / `printenv` can't expose credentials.
- **Gate** (`node/larkgate.go`): the assistant uses separate hooks (plugin `assistant-hooks`, or `assistant-settings.json` for older Claude Code, `docs/integrations/claude-integration.md` ("Hooks")), with an extra Bash PreToolUse hook (herdr Agents unaffected).
  It reads the whole command:
  - With only gated commands (and output-only commands such as jq / head / grep / echo / cd / `timeout N`), check each: reads allowed directly; self-only actions (mark read, complete own to-dos) allowed;
    other writes (send / reply / recall / forward, reactions, docs, schedules, approvals, mail, group settings…) show a phone "Lark" approval card with action, chat name (extension `assistant.summary`),
    method, path, and body to send (reads `@file`). Denial returns user feedback as the reason; "Always allow in this session" remembers this route type with ids stripped.
    For a gate without `Auth` in default mode, an `@file` body never selects a body-scoped `allow` (and cannot silently clear an earlier body-scoped restriction): the call is confirmed or denied by fallback rules because no proxy can verify the eventual request body at send time. Inline bodies, body-independent rules without an earlier body-scoped restriction, authenticated proxy gates (where the proxy enforces rules on the received body), and bypass mode are unchanged (#8413).
    `@file` body and `--url-query @file` reads pin the resolved parent directory with `os.OpenRoot`, open without blocking or following a final symlink, accept only regular files up to 1 MiB (with `os.DevNull` kept as an empty `--url-query` sentinel), reject lexical and resolved `/proc`, `/dev` and `/sys` paths, and keep the 4000-character whole-character clip for bodies.
    `sort -o` / `--output` / `--compress-program`, `date -s` / `--set` / positional clock-setting operands, and `uniq` with a second output-file operand are not treated as output-only filters.
    On Claude Code, local `assistant_environment` runtimes (such as Antigravity), and the node's MCP command-gate tool (`assistantGateTool`), one static authenticated send/reply with a top-level text body declares `editable_text`: the web may replace that text on a plain Allow or attach a denial note. When the text changes, the node skips granting the original call, validates the edited body against the current gate and rules, and grants only the edited call once with all other fields and call identity unchanged (rebuilding `updatedInput.command` on Claude Code, or rewriting the JSON body inside `gateProxy` on local `assistant_environment` runtimes and `assistantGateTool`). Codex, remote/non-`assistant_environment` Bash runtimes, lark-cli, dynamic/file bodies and other routes keep ordinary Allow/Deny.
  - A write that went out through the proxy (the API answered 2xx) is kept in the sent-as-user ledger (`docs/extensions/connectors.md` "Sent as the user"): the node tells the manager (`lark.sent`) how the gate let it through. Not in it: lark-cli writes and gates without `auth`.
  - A health conversation's writes are confirmed each time, rules' allow included (`docs/assistant/health.md` "Outbound"), at both entrances: `larkCheck` turns an allow into a card, and the proxy address opened for a process started in the health space
    lets a write through only by taking the grant that card left (a conversation's own: a new Codex conversation's addresses are given its id once its thread starts, and take nothing before). Both decide the space from a record made when
    the process is started, not from the directory a hook reports (the model's `cd` moves it); a hook directory that is a health space's counts too, so a mix-up can only ask more. Reads go as everywhere.
  - Credential variables, `/proc/*/environ`, direct gateway curl, or flags exposing headers such as `-v` / `--trace` / `-w`: reject directly.
  - Other commands (piped to python, `$(…)`) bypass this gate and follow ordinary Bash approval in the permission mode; local redirect targets do not change a call's classification. Any gated write inside is rejected with instructions to send it separately:
    the proxy allows only the exact call read and confirmed by the gate, so whole-command approval still can't send it (before #1317, the whole command showed a card).
  - Rules are configuration: `connector_gate` in the manager's manager.json, same format as other connector writes; `connector` is `lark`, `action` is "METHOD path" (`*` one path segment, `**` all remaining,
    `"*"` any call), `match` against top-level body fields, `decision` is `allow|confirm|deny`. They precede extension rules; first match wins.
    For example, stop confirming sends: `{"connector":"lark","action":"POST /conversations/*/messages","decision":"allow"}`.
    Legacy `lark_gate` (`{method, path, body?, action}`) still works after `connector_gate` Lark rules; hub combines both for node (`connector.LarkRules`, 2026-11-03 migration M5).

## Lark: lark-cli (L2 of #273)

A hub without a Lark gateway can still let the assistant read / write Lark through official developer-platform lark-cli (`@larksuite/cli`); plan and later steps: `docs/plans/plan-lark-cli.md`.
The assistant and the L2 read-only connector are available; Inbox, knowledge base, and `/agent` still require the Lark gateway.

- **Enable**: `lark.cli` in the manager's manager.json (`docs/operations/config.md` ("manager.json")), lark-cli installed on the assistant machine; the user runs `lark-cli auth login --domain all` once, or `lark-cli --profile <profile> auth login --domain all` when a profile is configured.
  A gateway can coexist; `lark.prefer` sets prompt precedence.
- **Delivery**: before process startup, node asks hub for configuration (RPC `assistant.lark` adds `cli`, `prefer`; CLI-only doesn't query the gateway; hub no longer supplies gateway tokens anyway),
  writes a one-line `lark-cli` wrapper in that assistant directory's command bin, invoking configured lark-cli (resolve nonabsolute paths in a login shell) with configured `--profile`,
  and adds its directory to PATH (npm lark-cli uses `env node`, so node must be findable). No wrapper if `lark.cli` absent or lark-cli not found.
  With a profile configured, the node also sets `LARKSUITE_CLI_PROFILE` to it in the assistant's environment, which lark-cli reads when no `--profile` is given:
  Claude Code runs each Bash command through the user's shell snapshot, which puts the login shell's PATH back and may find another lark-cli before the wrapper (#9123).
  Credentials remain in its own `~/.lark-cli`; hub / node don't touch them.
  On another personal computer (a conversation the manager starts there, such as a Lark `/agent`), `cli` carries only the command name (the base name of an absolute `bin`) and no profile: the wrapper runs that computer's lark-cli with its default profile, and its gate reads that lark-cli's risk levels (#5479).
- **Instructions**: no skill copied into the assistant directory. CLAUDE.md (`prompts/assistant/claude_md.md`) adds a section directing `lark-cli skills read lark-<domain>` first for bundled,
  version-matched domain instructions, with parameters from `--help` / `lark-cli schema`. CLI-only explains no unread / Moments; when the built-in connector is signed in, its read-only `larkcli_*` tools are also available. Find actionable messages with
  `im +messages-search --is-at-me / --chat-type p2p`. With both sources, divide according to `lark.prefer` (goldens `assistant_claude_md.lark_cli`, `.both`).
  Send / reply through the app bot (`--as bot`): user identity requires app permission `im:message.send_as_user`, absent in the default app (measured 2026-11-04); other reads / writes use user identity.
  In CLI-only setups, a leftover gateway skill (copied by old personad) doesn't count: no gateway is available, so CLAUDE.md must not mention its commands
  (encountered deploying to the hub machine 2026-11-03: instructions recommended gateway commands then said no gateway existed).
- **Gate** (`node/larkcligate.go`, integrated with the `ol` gate in the same `larkCheck`, also used by Codex): lark-cli knows write commands; each `--help` prints
  `Risk: read / write / high-risk-write`. node asks once on first seeing a command (cached per command, about 50 ms):
  - `read` allowed directly; outside assistant bypass, `write`, `high-risk-write`, and unknown risk show a phone "Lark" card explaining actor (`--as bot`: "as the bot", otherwise "as you"), action (send, reply, react, recall…), and full command;
    high / unknown risk is separately marked. Denial returns user feedback; "Always allow in this session" remembers the command.
  - A command lark-cli does not have runs without a card, so lark-cli itself answers "unknown subcommand" (#10845). lark-cli prints the parent group's help for it: no `Risk:` line, a `Usage:` naming exactly the command without its last word, and an `Available Commands:` list without that word; all three must hold. That answer is cached like a risk level. A command that exists but prints no `Risk:` line, and a lark-cli that cannot run or fails, still get the card (not cached).
  - Output-only allowed directly: `--help`, `--version`, `--dry-run`, `schema`, `skills`, `whoami`, `doctor`, `auth status / check / scopes / list`, `event list / schema / status`;
    `lark-cli api` uses HTTP methods: GET / HEAD allowed, other methods follow the permission mode and action classification (method, path, `--data` on a confirmation card).
  - For a gated message send or reply, the approval summary shows the conversation name (id when unavailable), the original sender and a short excerpt for a reply, topic status, and the outgoing text, rather than internal JSON fields such as `clientId` or `expectedSenderId`. `node/larkgate_card_context.go` reads that metadata with read-only Lark CLI calls (1.5 s per call); if it cannot be read, the old method / path / body summary is kept, and no gate decision changes (#5776).
  - Reject `auth login / logout` (user handles login), `config`, `profile`, `update`, custom `--profile`, setting or unsetting `LARKSUITE_CLI_PROFILE` (reading it, `$LARKSUITE_CLI_PROFILE` or `printenv LARKSUITE_CLI_PROFILE`, is not refused), or access to `~/.lark-cli/`.
  - The gate classifies individual calls in loops and literal shell scripts where it can. Presentation-only environment assignments such as `NO_COLOR=1` and `NO_PROGRESS=1`, local output redirection to any target and read-only filters (`jq`, `grep`, `head`) preserve a classified read. When the command names the CLI, executable-loading environment variables such as `BASH_ENV`, `NODE_OPTIONS` and `PATH`, unresolved CLI arguments and command substitutions in those arguments require whole-command confirmation. Exception: a read's argument substituted from filters alone (`$(jq …)`, `$(head …)`) or, in lark-cli's arguments only, from a file read (`$(cat file)`, `$(< file)`, #9101; in another command's argument, curl's, it stays unsure). A write with such an argument is still confirmed whole. `docs +fetch` remains a known read even without configured CLI risk discovery, and when the lookup gives no level a command named as a read (shortcuts such as `+agenda`, `+fetch`, `+search`, `+…-list`, `+get-…`; generated `list` / `get` / `search` methods) is one too, while every other name stays a write (#11193). A `$?` in a literal loop's body does not stop the loop's values from being bound into its lark-cli calls.
  - Literal scalar assignments can supply a command name or a multiword interpreter prefix for classification (#11307). Only literal fields are substituted; shell punctuation, substitutions, globs and values the node cannot prove stay unknown. The same bindings are followed through literal `bash -c` / `sh -c` scripts, literal for-list iterations and command groups, with every compound statement classified separately so a later write still asks. A literal GET / HEAD route's unmatched query-string glob remains a read only as the final argument; contact user search and chat-message listing are known reads when CLI risk metadata is unavailable, while reported write risk always wins.
  - A trusted assistant in `bypassPermissions` runs classified reads and ordinary writes without a card. The gate protects Lark outbound sends as the user (messages, replies, comments and mail) and deletion, including inside compound commands. Ordinary non-Lark commands such as `rm`, `git push` and `tee` remain outside this gate. Unclassifiable execution requires confirmation when the command text names `lark-cli` (including an absolute path or interpreter, `sh -c` or `eval` code), or a variable, command substitution or other expansion supplies a command name. This also covers unresolved CLI arguments. Text naming `lark-cli` where nothing can run it is not a call (#8119): here-documents written to a file (also after a literal `cd`, in `&&` / `||` lists and with `~` targets), filter patterns such as `grep`'s, a `perl -pi -e 's/…/…/'` substitution that evaluates no code, and a `for` list's values. It still counts in a program's own code, when piped through filters into a program, when written to a file while another program runs, and when the command uses the CLI and also runs a program whose code it does not show (a script, a shell or interpreter reading its input, inline code that starts programs or loads code); inline `python -c`, `node -e`, `awk` and `sed` code that does none of that is judged by its own text, so `lark-cli api GET … | python3 -c '…json…'` runs. The gate also reads what a command shows where its splitter used to stop (#8148): a `python3 -` or `node -` program in a here-document with a quoted delimiter is read as the same code given with `-c` / `-e`; inline Python that imports only pure standard modules (`json`, `re`, `sys`…), calls by bare name only safe builtins or what it defines or imports itself, and names no process, dynamic-lookup or private attribute holds `lark-cli` as data in its strings (it may still write them to a file); a `( … )` or `{ …; }` group of one command that assigns nothing and names neither `lark-cli` nor a gate is judged as that command; `$((…))` is arithmetic, not a subshell (it stays unsure; its own `$(…)` are classified); a command path assigned with `~/` or `$HOME/` resolves like an absolute one, also when that command's output is piped. In a plain `;` / `&&` / `||` list only a program after a file was written can run it, and a note (`.md`, `.txt`, `.log`, `.csv`, `.tsv`) only a program that names it. `ssh [options] host 'command'` is classified as that command: lark-cli reads there run, and its writes and anything unclassifiable are confirmed; options that run commands (`ProxyCommand` and the like), `-F` and login shells are not followed. Literal assignments earlier in the command resolve a lark-cli route (also across pipes), and a variable left in a read's flag value keeps it a read, except after `--dry-run` or `--help`, which an unquoted variable could turn off. Outside straight-line code (a loop, an `&&` list, `cd`), a variable in a gated call's route resolves only when it is constant over the whole command (#16491): assigned once, as `name=value` in a top-level statement that always runs (first, or after `cd /tmp &&` as the command's first statement), to a literal of `[A-Za-z0-9_-]` under an ordinary name (lowercase, or one capital letter, and not one bash or zsh sets itself), and used only as `$name` / `${name}` (not `$name:…` or `$name[…]`). Every command must be a gated one, `lark-cli`, or a known program or inert builtin, every assignment must be to an ordinary name, and nothing may evaluate arithmetic, a subscript or a test other than a string or file test; otherwise the route stays unresolved and the whole command is confirmed. Variables bash or zsh maintain (`PWD`, `OLDPWD`, `BASH_REMATCH`, `match`, `_`, …) are not bound by literal loop expansion either. A `PATH` made only of directories already on the node's `PATH`, the login shell's `PATH` (where the assistant's Bash resolves programs, asked once and cached) or the usual system ones does not count as replacing the executable. Such a whole-command card names Lark or a gate only when the command shows it (it names `lark-cli`, even as a variable's value, or calls the gate); a command named only by an expansion, such as a local script's `$ADMIN POST /api/…`, is asked as an ordinary Bash card showing the whole command, and declining it does not tell the model it was a Lark write (#4890). When every identified call is an ordinary read, a whole-command card says read rather than write even when an opaque non-Lark part still needs confirmation; mixed, unknown, outbound and deletion cases keep the existing wording (#10229). Local redirect targets do not change call classification. Command substitutions in redirect targets are classified recursively. A literal formatter stays outside the gate, while a dynamic command or Lark send still requires confirmation. The literal test commands `[` and `[[` are not expansions. Wrapper prefixes and their option values are parsed recursively from a shared table, including nested `env`, `sudo`, `nice`, `timeout`, `xargs` and `exec` calls. Unknown wrapper options require confirmation if remaining words contain expansions or a CLI name. Shell `-c` scripts (including combined options such as `-lc`, common shell names such as `ash`, `mksh`, `csh` and `rbash`, their path and numeric-version forms, and busybox shell applets) and literal `eval` arguments are classified recursively; dynamic shell scripts and dynamic `eval`, `source` or `.` arguments require confirmation. Ordinary interpreter calls, `awk`, `sed`, `find`, wrappers with ordinary executable names, script files, argument substitutions such as `echo $(date)` and brace arguments such as `echo {a,b}` return no gate decision when they do not name the CLI; the conversation permission mode handles them. Script-file contents are not inspected: a script invoked without a CLI name in the command text can still call the CLI internally. This is an accepted residual risk of this gate's scope. The action and bot-exemption tables in `node/larkgate_rules.go` classify these boundaries; only the documented message send/reply shortcuts may use the bot exemption, with the last `--as` value taking effect. Raw API sends, mail, comments and deletion still require confirmation with `--as bot`. Explicit deny rules still refuse them. Proxy-authenticated writes inside compound commands are refused and must be sent as standalone calls. In `bypassPermissions` only, two more shapes are read (#15860): a `for` loop over a file's words (`$(cat file…)`, `$(< file)`, either piped only through output filters) whose body runs nothing but known lark-cli reads, alone or under `timeout`, piped only into output filters or a `sed` that only prints and redirected only to files outside `/dev`, `/proc` and `/sys` whose path no variable starts, with every expansion the whole double-quoted value of a long flag other than `--as`, `--method`, `--request`, `--profile`, `--output`, `--jq` and `--format`; and a here-document or note written through a variable assigned a literal earlier in a plain list (`d=~/notes; cat > $d/note.md <<'EOF'`), also into a directory `mkdir` makes first. Any other loop over a substitution, a nested one, a variable in a command word, a flag name or `--as`, and running the written file keep the whole-command card (`node/larkgate_loops.go`, `larkFileReadLoops`). Other permission modes and health conversations retain their confirmation rules.
  - Known residual execution risks: `find -exec`, `find -execdir`, `find -ok`, `su -c`, `runuser`, `setpriv` and `unshare` are not parsed as command wrappers. Dynamic commands in those positions can pass without a card when the command text does not name the CLI. These shapes remain outside the shell-name correction; script-file contents also remain uninspected.
  - Match manager.json rules first (deny remains final; allow does not waive the bypass boundary checks above): `connector_gate` `{"connector":"lark","action":"CLI /im/+messages-send","decision":"allow"}` (legacy `lark_gate`
    `{"method":"CLI","path":"/im/+messages-send","action":"allow"}` also works).
- **Not done**: runtime assistant (DSH, pi) prompts and extension-gate tool declarations still only know the gateway; settings-page lark-cli status and missing permissions (L2).

## Built-in MCP namespaces

For a new or resumed Claude, Codex app-server or guest assistant session, a hub announcing `persona_contracts_v1` selects only the
`persona` MCP namespace and matching automatic tool permissions. This avoids sending duplicate tool definitions and
starting a second copy of the same built-in handler. Old or unknown hubs keep the compatibility alias. A real Claude Code
2.1.322 resume verified that a transcript containing `mcp__jarvis__*` tool calls can continue with only `persona` offered;
existing transcript readers, hooks and read-only-worker supervision continue recognizing both names. The generated
assistant instructions map old names in stored routines to `mcp__persona__*`. Only `persona_settings` is advertised;
the legacy `jarvis_settings` call is still normalized and runs. The shared `mcp.json` for ordinary Agent launches and
automatic resumes remains dual-namespace.
See [rolling-upgrade-compatibility.md](../protocol/rolling-upgrade-compatibility.md) ("MCP namespace / tools") for the
negotiation boundary. Extension permissions, guest tool restrictions and tool-specific confirmation gates remain in force.

Ordinary Claude starts also pass a built-in allow-list (`--tools`): Bash, Read, Write,
Edit, Monitor, Skill, Agent, ListAgents, SendMessage, TaskStop, WebFetch and WebSearch. MCP tools are unaffected. This removes the
Claude-specific cron, worktree, notebook, LSP, design, workflow, code-review and resource-tool surfaces; Persona's own
scheduling and question tools remain. The merged start settings disable bundled skills and the development-only
feature-dev, frontend-design, mattpocock, gopls, context7 and HUD plugins, with overrides for the remaining development
skills. User/project setting sources, auto-memory, Persona and connector skills are not disabled. Guest conversations
keep their stricter `--tools ""` and `--strict-mcp-config` behavior. No setting is required.

### Manage which updates reach your work

In the main Persona conversation, ask which work receives updates from a source, or ask to change it. For example,
“Which to-do gets updates from this pull request?” or “Send updates from the release group to the Launch direction
and ask me before creating a to-do.” Reading your routing rules needs no approval. Before changing them, Persona
shows the source, destination and proposed behavior on the usual approval card. New automatic forwarding and
automatic to-do creation require your approval.

Ask Persona to restore the previous rule, or use the same routing controls in Inbox, work details or Settings.
Direction supervisors continue managing their work in their own conversations. The main conversation manages rules
that can affect multiple directions. See [moving existing forwarding rules](inbox.md#move-existing-forwarding-rules).

## Extension MCP servers (Claude assistant only)

Extensions can contribute `mcp_servers` (`docs/extensions/extensions.md` ("MCP servers")): when the named CLI is installed on the assistant computer, node attaches it as another MCP server for the **Claude assistant**
(`mcp__<id>__*`, `node/assistant_mcp.go`; not Codex / runtime assistants). Commands read their own configuration; Persona neither reads nor passes credentials. Listed read-only tools are allowed directly,
named denied tools are never callable; others request phone permission according to chat mode. For example, a Lark extension attaches upstream personal-Agent control tools for scheduling personal Agents and inspecting pending requests
(approvals only forwarded to the user, never approved on their behalf; Herdr / Orca tools overlapping Persona `mcp__persona__agents_*` are disabled). Specific tools / switches are in extension instructions.

## Its own directory without a card (2026-11-23)

An assistant conversation (the main one, a space's, a track's) reads and writes files in the directory it started in without a permission card, whatever its permission mode except plan: the node's `PermissionRequest` hook answers allow for Read, Edit, Write, MultiEdit, NotebookEdit, Glob and Grep on a path inside it (`node/assistant_workspace_allow.go`). Both sides are compared as they resolve (the longest existing ancestor, so a file not written yet compares too): the node starts the conversation in `~/.persona/assistant`, where `~/.persona` may be a link to an older install's state directory, and Claude Code names the files it writes by the resolved path. Still asked: the directory's own agent configuration (`.claude`, `.codex`, `.agents`, `.git`, `.mcp.json`), a link inside it that leads out, another space's files, a guest's conversation and the Inbox's. Deny rules (`node/healthdeny.go`) are Claude Code's and hold before the hook is asked. The health space's `reports/` allow rule names both spellings of the directory for the same reason.

## Always allow in this session (2026-11-06, #2202, #5867)

Tapping "Always allow in this session" on an assistant permission card remains effective throughout the conversation, including process restarts and scheduled follow-ups. The card explains the scope before approval: Claude file tools grant that tool in the file's directory; Bash grants **all commands in this conversation**, not just the displayed command. Other Claude tools grant all calls to that tool. Codex command and file approvals retain their narrower command/path scopes; runtime-provided labels describe that runtime's scope.

- Node supplies `always_keys` on assistant cards (Agents-page sessions keep their existing behavior): Claude `tool:<tool name>` for a whole tool, or a directory rule such as `tool:Read(//workspace/project/**)` for file tools; Codex `codex-command:<command>`, `codex-edit:<file>` / `codex-root:<directory>`, and `codex-permissions:<permission>`; Lark `lark:<route>` with IDs stripped. Whole-command gate confirmations are not remembered.
- The manager stores approved keys in Postgres `assistant_always` by conversation ID and key SHA-256. Every requested key must match to auto-allow a later card. Directory keys are exact matches; a subdirectory can prompt separately. Only explicit permission grants are remembered; denials, one-time allows, and unknown keys still prompt. Automatically replayed answers do not write grants back, so an in-flight answer cannot resurrect a deleted grant. A remembered grant also answers the covered cards already waiting in the conversation (parallel tool calls produce several). Such an answer carries `remembered: true` (`proto.Resolution`), so the node can tell it from the answer to a card: the sent-as-user ledger records a Lark write let through this way as `conversation_always` (`docs/extensions/connectors.md` "Sent as the user"). When the main conversation is replaced (`POST /api/v1/assistant/main/new`, `/clear`, or changing `main` in settings), the manager copies the replaced main conversation's grants once to the new main conversation (`handOverMainAlways`, #16158); grants from leader or Inbox-origin conversations are never copied, and revoking a carried-over grant on the new main conversation sticks permanently without affecting the archived conversation's history.
- Cards say whether this is offered (`always_honored`, #3590, `docs/protocol/protocol.md` "Always allow"): `true` on a node's card whose keys the manager remembers, `false` on the manager's own cards (the extensions' tools, `persona_settings`, its to-do and agent confirmations), which keep no grant, so the clients show only Allow and Deny there. Those cards are titled with their tool's name for people (`tool_title`).
- For assistant conversations, Claude no longer receives `addRules … destination: session`; Codex command/file approvals are one-shot and extended permissions use turn scope; the Lark gate consults the manager instead of keeping its own grants. The manager remains the authority across processes. Agents-page approvals are unchanged.

On the web, open the conversation's **Session info → Always allowed in this conversation** to view and revoke individual grants. Each row shows the tool and approval time. The existing table stores only a key hash, so exact historical directories/commands cannot be reconstructed; the UI says the detailed scope is unavailable. No migration or wider replacement grant is required. iOS and Mac show the same list in **Session Info** and ask before revoking (`AlwaysGrantsSection`); a hub without these routes or a device that may not use them shows no section there. Android shows it in the Persona tab's **Session info** for the open conversation, also asking before revoking (`AlwaysGrants.kt`, `AssistantManagement.alwaysGrants` / `revokeAlwaysGrant`; #890); a hub without the routes or a device that may not use them shows only the conversation there.

The full-access API is relayed by the hub to the conversation's manager:

- `GET /api/v1/assistant/conversations/{id}/always` → `{"grants":[{"key_hash","tool_name","created_at"}]}`; an empty list is `[]`.
- `DELETE /api/v1/assistant/conversations/{id}/always/{hash}` → `{"ok":true,"restart_required":false}`. Deletion is idempotent and scoped to that conversation. Extension tokens and answer-only devices cannot use these endpoints.

Revocation first deletes the persisted grant, then calls node `assistant.permissions.reset` to clear cached gate rules and terminate the conversation's existing Claude/Codex process group and wait for exit. This also removes session rules installed before this change and Codex turn permissions. **Revoking stops the current response**; send another message to resume the same transcript. Work already authorized or completed cannot be undone. Unrelated stored grants remain effective when the conversation resumes; explicit bypass modes and configured allow rules are separate settings.

If the node is offline, too old, fails to acknowledge the reset, or the conversation belongs to an external runtime without a permission-reset protocol, deletion still succeeds with `restart_required: true`. The web retains a warning: **revocation takes effect after the conversation restarts**. Restart the conversation's process in that runtime before continuing; merely reopening the web page does not clear process permissions. Retrying the DELETE also retries the reset even when the row is already gone.

Assistant-owned cards that can prepare their operation also offer **Have a question** on Web and Feishu. The question leaves the same card pending and returns a non-final Clarifying tool result; only a later fresh owner Allow can execute it once. Owner routing, stale rechecks, the 24-hour answer-time budget, terminal delivery, restart behavior, and the `fleet.ask_prepared` compatibility protocol are specified in [Approval clarification](approval-clarification.md).

## Password card (#1137)

When the main chat needs a password only the user knows (typically a computer's sudo password), the Agent never asks for it in the chat. The assistant-only tool `secret_run` (`server/internal/assistant/secret_run.go`, description in `prompts/mcp/assistant/secret_run.md`) proposes the exact commands; the user reads them in the Persona app (iPhone, Mac, Vision Pro, Android or the web), types the password into the card and taps Run. The target computer's node runs exactly those commands, giving the password to the one that runs `sudo -S`. The Agent gets exit codes and output that never held the password, never the password.

For a one-off authenticated LAN/CLI action that is not sudo, explicit `delivery: "env"` instead puts the password in every approved command's `PERSONA_SECRET` environment, with empty stdin and never in argv. This is a per-run opt-in, not a setting or a reusable credential. The default remains strict `sudo -S`-only stdin. Every env card warns after its commands that each one receives the secret and that encoded output can evade literal redaction; Watch and generic fallback views carry the same warning in the server summary. An approval of that card must also carry `X-Persona-Features: secret_run_env`, proving the client rendered the new warning. Older specialized clients ignore `delivery` and suppress the summary when commands exist, so their approval is refused with 403 `secret_run_env_elsewhere` before anything reaches the node; only a plain decline passes.

- **Checks before the card** (code, not prompt): the machine resolves by id or name (`machine` must be given), and its node answers `secret.run {"check":true}` (an offline computer, a node without the `secret_run` capability and a Windows node fail here). `delivery` is omitted/`"stdin"` or explicit `"env"`; anything else is refused. Env preflight carries `delivery` and needs the separate `secret_run_env` capability, so an older node that would silently ignore the field gets `node_outdated` before the user types anything; stdin stays compatible with older `secret_run` nodes. `purpose` is one line of at most 300 visible characters: no control, format (zero-width, bidirectional), line or paragraph separator, private-use or unassigned characters. 1 to 5 commands, each one line of at most 1000 characters of printable ASCII (`proto.SecretRunCommandProblem`): anything else could make a command look other than what runs (a zero-width space in a file name, a no-break space inside a word, a look-alike letter), and the error names the command and the character. A command that calls `sudo` (where a command starts: `proto.SecretRunSudoOf`) must pass `-S` (`sudo -S -k -p ''`, as the node has no terminal for sudo to ask on), call it once (stdin has the password once) and not after a pipe (sudo would read the pipe). `timeout_seconds` covers the whole run: 300 by default, at most 1800.
- **The card** is an ordinary `permission` interaction, so every client and safeguard already handles it: `tool_name: "secret_run"`, `tool_input {machine, machine_name, purpose, commands, delivery}`, a `summary` that repeats the computer, the purpose and every command after `$ ` (for clients that don't know the tool), one `secret` field `password`, and a 10-minute expiry. `machine_name` (and the summary) carries the computer's id too, as `Mac mini (macmini-2)`, when another computer of the manager has the same name or that name as its id or ssh alias. A card with fields has no quick approve anywhere. Only the tool makes this card: one from a node (a hook, a runtime, a daemon, a Codex list) or an extension (`fleet.ask`) with `tool_name: "secret_run"` is refused before it reaches the clients (`reservedCard`), since the clients show it as the trusted card without an Agent's name.
- **Who can fill it in**: only a paired app's answer from the card's own form: the iPhone, the Mac, Vision Pro, Android and the web show the password field and Run. The watch shows the commands and offers only Decline: the hub treats it as an answer-only device and refuses `values` from it. The clients keep the password only in the card's own state, never stored or restored (no drafts, storage, saved state or password-manager saving), and clear it on Run, on Decline and when the card goes or another takes its place. These answers run nothing and give `refused`: one the assistant wrote (`answered_by`), a remembered "Always allow", one without the password, one carrying the history's `<redacted>` marker (a replay), and a password with a control character (sudo would take only its first line). Lark and plugin channels can only decline such a card (`channelResolve`).
- **The run** (`node/secretrun.go`): the manager sends the password to the node in the `secret.run` params, in memory over the existing connection. Each command runs with `/bin/sh -c` in the home directory with `PATH=/usr/bin:/bin:/usr/sbin:/sbin` (a `sudo` earlier in the user's own PATH is not what runs; other tools go by their absolute path), in its own session and process group, stdout and stderr read together. With omitted or `"stdin"` delivery, a command that runs one `sudo -S` gets the password and a newline on its stdin, then the pipe closed; every other command gets an empty stdin (`/dev/null`). Where a sudoers rule says `NOPASSWD`, sudo does not read stdin and the command it runs gets the password instead. With explicit `"env"` delivery every command gets empty stdin and a child-only `PERSONA_SECRET`, replacing any inherited value for that child without changing the node process environment; the password is never an argument. The run stops at the first command that exits non-zero, cannot start or runs out of time. At the deadline the command's group gets SIGTERM (sudo passes it on to the command it runs as root, which the node cannot signal), 5 seconds to exit, then SIGKILL until the group is empty (`osproc.SweepGroup`); a root command that ignores SIGTERM can outlive the run, and the result tells the Agent to check its state before asking for a new card. A command whose shell exited while a background child still holds its output is stopped the same way at the deadline, child included (#4363); a command whose turn comes after the time is used up does not run and gets no result. The node masks the password in the output as it is read (holding back only the last `len(password)-1` bytes, which may begin an occurrence the next read completes), keeps the last 256 KiB of the masked output and returns the last 16 KiB. A command whose output held the password returns none of it (`output_withheld`): where a masked password sits (`/Users/***/`) could give a weak one away. The manager checks the output again and gives the Agent the last 4000 characters of each.
- **Result**: one JSON object, `{status, machine, message, results}`. `status` is `ran`, `failed`, `declined` (Decline, or the card closed in an app before its deadline, as "Hand back to computer" does), `expired` (not answered by the deadline, or withdrawn or cleared before anyone answered it: its message says which, and that the user did not decline it) or `refused`; `results` (`command`, `exit_code`, `timed_out`, `output`, `truncated`, `output_withheld`) come only with `ran` and `failed`, and the commands after the first failure are absent. Errors before the card are plain-text tool errors.
- **One use**: the first answer claims the card; a second gets the existing "already answered" reply and runs nothing. The password lives only in the waiting tool call and the node's handler. A hub, manager or node restart drops it: the interrupted call returns an error, and the Agent has to ask again with a new card. The tool call comes over the node connection of the computer the conversation runs on (`mcp.call`): when that connection ends, the call ends with it, the card is withdrawn and nothing runs. The node is not told when the tool call stops waiting; it stops the commands at the run's timeout.
- **For every card with secret fields** (adding a mail account too): `interaction.resolved` / `interaction.ended` (the channels, the task feeds) carry the history's redacted answer; values for keys a card does not declare are dropped when it is answered; an answer to a card with a secret field is never held for redelivery (`settleAnswer`); and the hub's operation receipt for `resolve` (`client_ops.digest`) covers the answer as the hub reads it (`proto.Resolution`, field names in any case) with each value replaced by its key, and one fixed body for any that is not an answer, since the sha256 of a short password can be guessed offline.

Threat model and limits:

- Protects against the password reaching the database (interaction history, operation receipts, task feeds), logs, the Agent's transcript, the Agent's environment, files, process arguments or tool result, channel messages and push payloads. Explicit env delivery is the deliberate exception only for the approved child commands: they receive `PERSONA_SECRET`, but the Agent and node environments do not.
- Does not protect against a process that attacks Persona as the same user on the hub, the manager's or the target machine (it can read process memory, or use the hub token as an app, #813). An Agent with full shell access on the hub machine is in that class if it sets out to attack Persona: the card stops accidental exposure, not a hostile Agent.
- A command on the card that reveals the password in another form (`base64`, a file, a remote host) shows that form: only the literal password is found. The user's check is the card, which shows every command verbatim, and nothing else runs.
- Go strings cannot be wiped. The password slices the tool call and the node's handler hold (the bytes handed to the node, the decoded password, the node's request params and its output buffers) are zeroed when the run ends; request bodies, RPC frames, the JSON copies on the way and `exec`'s own copy buffers are left to the garbage collector. The password crosses the existing TLS / ssh connections in plaintext; end-to-end encryption to the target node is a follow-up.

## Assistant answers questions first (2026-11-05, #3029)

With over ten Agents running, most mid-task questions concern internal choices the user can't answer (technical approach, default definitions, reruns, branch choice). Agent questions therefore reach the assistant first: answer what it can, send only red-line and goal decisions to the user. `agents.delegate` in the manager's `manager.json` (**off by default**, enable with `enabled: true`; examples in `docs/operations/config.md`).

- **Eligible questions**: `kind: question`, including hook AskUserQuestion, Codex questions, and triage "Your decision" cards (`persona_triage`, also `waitInteraction` → `announce`). Permissions, plan approvals, terminal dialogs, forms, assistant `ask_user`, and questions within assistant chats go directly to users; triage cards requiring authorization links or risky actions such as push / deployment aren't answered either. Don't wait if assistant disconnected.
- **Answer only real questions, once per question** (2026-11-06): Agent requests (AskUserQuestion, Codex) already await input; triage cards are inferred from stopped-turn text. Delegate only if a question contains `?` / `？` or at least two Agent-authored options (not manager defaults like "Okay, do as you said" / "Leave it for now") (`asksForInput`). Declarative stops ("Waiting for runner recovery and 0.14.0 release") get only fallback choices; formerly the assistant selected "Okay", the Agent repeated itself, another card appeared, and delegation repeated—one board Agent answered three times in a minute. Remember delegated questions per session (compare after removing spaces / punctuation, or bigram similarity ≥ 0.8; `delegated` in `session_work_events`, reloaded on restart): the same Agent repeating within 24 hours goes to the user, recording `delegate_repeat`.
- **Flow** (`assistant/fleet_delegate.go`; queue in manager since 2026-11-05, #3246): manager `announce` first reports the card to hub with `delegating: true` (visible / answerable in the App, no push or bot card), then evaluates in the background for up to `timeout_seconds` (default 3 minutes). Assistant answer → same `interaction_answer` path (`answerQuestion` → `resolve`, `answered_by: "assistant"` + `reason`), with reason / answers in history and `interaction_resolved`. User handoff, failure, or timeout → remove `delegating`, attach `delegate` context, and use the normal push / bot / channel / quiet-task path; hub appends "Assistant suggests: X" to push body (`hub/push_payload.go`). If the user answers during evaluation, first answer wins and the assistant result is discarded.
- **Decision** (`assistant/delegate.go`, prompt `prompts/agents/delegate.md`, separate model-config category "Question delegation", `agent.delegate`: before selection uses the information-filtering provider and Opus 5.5; change on that Settings › Model Configuration page, `docs/assistant/model-config.md`): one model call reads user messages / final Agent text from the latest three turns, its to-do (title, notes, decisions), cwd project, question, and choices. Prompt rules: answer internal choices with a short reason; send irreversible actions (deploy, release, merge, delete data, force-push, external messages), socially visible actions (MR comments, reviewers, approvals / tickets, contacting people, creating groups, touching others' MRs), and goal changes to the user, supplying "what this is, why ask now, recommended answer, cost of not choosing, why you must decide". Default deadline 24 hours, or earlier card expiry. Two hard checks follow: every question answered using listed choices (same `interaction_answer`, no free text; the model sometimes copies an option's whole `label：description` line from the input, which counts as that label, while its own words after a label or text that reads as two options go to the user, #3929); selected labels / descriptions containing `riskyWords` (push, deploy, merge, send messages…) are handed to the user as recommendations, except short "don't do it" choices such as "Don't merge yet" / "Don't send". User `rules` can broaden internal choices but never red lines.
- **Why one model call, not a persistent Claude chat**: handle questions from over ten Agents concurrently within 3 minutes; headless chat startup alone takes a minute or two and its tools create permission cards. Single-call input / output is easier to test and inspect in goldens. Insufficient context goes to the user (uncertainty handoff is a prompt rule).
- **Why not an extension**: it attaches to hub `announce` and interaction `resolve`, both core flows. No "intercept new interaction" extension point exists; extraction first needs one (manifest validation, permission / consent to answer for users, process protocol, timeout fallback), not a smaller change.
- **Audit and changing answers**: triage answers sent to Agents explain "The assistant answered for me under my rules (reason: …); I may change my answer later" (`prompts/agents/answer.md`); task chat activity says "Assistant answered: X, because Y". Don't record delegation as to-do decisions (those are the user's). To change: `POST /api/v1/interactions/{id}/reanswer {answers}` (hub forwards to manager; card-answer-only devices can't use it). The Agent has already acted, so send the new answer as a message (`prompts/agents/reanswer.md`, including original assistant answer), broadcast `interaction_reanswered`, and record the user answer in task history / decisions.
- **Statistics**: each evaluation writes `session_work_events` (`delegate_answered` / `delegate_asked` / `delegate_failed`), shown with triage statistics.
- **Manager restart while deciding** (#2587): `delegating` and the background decision live in the manager's memory, and a restored card comes back with neither (`restoreInteractions` does not know whether it rang). So `delegate` records `delegate_start` (the card id) in `session_work_events`, and `delegateNow` records `delegate_end` once it has decided: after the assistant's answer, and before the user is told on a hand-over, so a restart right after does not push twice. No migration. As the next manager starts, `resumeDelegations` takes the restored cards whose latest mark is `delegate_start` (within 24 hours) and that do not find a supervisor route again (`restoreRoute`, "Asks go to the supervisor first" in `docs/assistant/unified-entry.md`); a hook that claims its card before the hub's handshake gets the same, from `waitInteraction`. within `timeout_seconds` of that start the assistant decides again for the time left (the card is `delegating` in the hub's first full list); past it, or when delegation is now off or the question repeats, the card goes to the user through the usual push / bot / channel path (`escalate`), recording `delegate_failed` with reason `restart` when it was the time. Restored cards without an open start were told before the restart and are not told again. Only resumable hook cards (Claude Code) are restored this way; a Codex card a node lists again after a restart goes through `announce` again (`codexAnnounced` starts empty), so it is delegated anew.
- **Client UI**: web has "Assistant is looking", the `delegate` context and "Change answer", plus the delegated answers in **Session info** (#544, `docs/web/web.md` "Pages"); iOS / Mac have the same (`docs/apple/ios.md`, "Information architecture"). Android has the same pending-card looking/context presentation (#544, `docs/android/android.md` "Session controls, session origin and capabilities") and already has Change Answer.

## Asking without holding the turn (#4928)

Decision rights apply before either wait mode publishes a card. Internal execution questions name
`decision_scope: "internal"` and actively wake their designated supervisor. Owner-only questions supply `why_owner`
or `owner_action`; a personal unlock uses `owner_action: "personal_unlock"`, without asking for the credential itself.
Calls with no decision-right metadata are rejected for clarification. Both modes keep that metadata in `ToolInput`;
asynchronous records restore the same routing after restart. Supervisor answers are attributed to the assistant in
the receipt, never to the owner. An internal cancellation or expiry returns unanswered and takes no default or
owner to-do. The owner-card lifecycle below applies to questions explicitly addressed to the owner.

`ask_user` takes `wait`, a boolean that defaults to true (hold the turn until the answer, as described under "Ask on the phone" above). With `wait: false` the card is raised and the tool returns at once; the turn ends, and whatever ends the card reaches the asking conversation as one message. Step Q1 of `docs/design/proposals/main-chat-availability.md` (3c); code in `assistant/ask_later.go`.

- **The card is the same card**: the same `question` interaction (`tool_name: ask_user`), push, Lark / channel cards, 2-hour expiry (`askUserWait`), `deadline` / `reversible` / `recommend` handling and duplicate-question check. The one difference is `resumable: true`, which the queue already knows from hook cards: nothing waits on it in memory, so it must outlive a manager restart and its node's connection.
- **What the tool says** (`prompts/agents/question_asked_later.md`): the card is up, the answer comes later as a new message, end the turn without guessing or going ahead, and until when the card waits.
- **How it ends**, each told once with `prompts/assistant/ask_later.md` (when it was asked, the questions, then the end). The apps show that message as one line ("You answered: …", "The question timed out unanswered", "The question was closed without an answer"), never as the user's bubble (#6421; `docs/protocol/protocol.md`, "Messages injected by Persona"):

  | The card | The conversation is told |
  | --- | --- |
  | Answered (an app, the watch, a Lark or channel card: all resolve the same interaction) | Each question with its answer; with several questions, that the ones not listed got none |
  | Closed unanswered (dismissed or cancelled, or cancelled by a restart that could not take it back) | That it ended unanswered: follow an answer the owner already gave in the chat, otherwise take no option and do not ask again |
  | Expired, and the owner wrote nothing in the asking conversation while it was up | What a waiting `ask_user` is told: it is now a to-do (`docs/assistant/todos.md`, "Questions nobody answered"), or, past a reversible deadline, the recommended options were taken by default. The owner's later decision on the to-do takes that section's path |
  | Expired after the owner wrote in the asking conversation | That it ran out unanswered and made no to-do and took no default: go by what the owner wrote if it answered the question, otherwise take no option and ask again if the answer is still needed |

- **Delivery** is the late-answer path of `ask_todo.go` (`relayToAsker`): sent at once to an idle conversation, queued behind a running turn in the queue `conversation_send` uses (kept across a restart), to the main conversation when the asking one is gone; the turn it starts is pushed like any report. In that queue it has the first rank, and the owner's answer is not held while they are present in the conversation (`relayMsg.Owner`); a card that closed or ran out waits for them as any system message that waits on a decision does ("Messages from the system wait for the owner").
- **The owner writes in the chat instead**: nothing is withdrawn (`withdrawAskUser` skips these: no turn is held behind the card). The card stays answerable, and a later tap on it is delivered as its one message. What the owner wrote may itself have been the answer, or a "no", and only the model can tell. So that they wrote is kept on the question's record (`askLaterSpoke`, from `UserSpoke`; kept across a restart), and a card that then runs out unanswered makes no to-do and takes no deadline default: a waiting question is taken back at that point, and a default must not be taken over the owner's typed "no". The conversation is told once (the table's last row), and the Lark card closes as "Replied in the chat" instead of "Moved to to-dos". Until it runs out the card still counts as waiting for the owner (the reminder and the Lark nudge still fire): the assistant has no tool to take a no-wait card back.
- **Channels**: a channel run (`checkChannelRun`) is not held by such a card: the turn that asked has ended, so its closing words are sent back as the answer (`hasPending` leaves out `Interaction.AskedWithoutWaiting`; any other card of the conversation still holds the run). In a text-ask channel (WeChat) a waiting card takes the owner's next message as its answer whatever it says; a no-wait card may be up for 2 hours while the conversation looks idle, so it takes only a reply that names one of its options (`channel.ReadsAsAnswer`, `channel.Ask.Later`). Other words answer a card asked before it that does hold a turn, else they are an ordinary message to the conversation (which counts as the owner writing there, above). A voice call still asks the card aloud and takes the caller's next words as its answer (`docs/assistant/voice.md`): there the question was just said and the capture lasts one exchange.
- **Told once**: the question stays among the manager's questions until its message went or is queued. An end that could not be told (no conversation to tell) makes the hand-over fail, so it is tried again (`keepAnswer`, `askOnPhone`); the message is kept on the record when the card ends, so a retry says the same. A message that still cannot go 24 hours after the card's expiry is dropped (`askLaterKeep`).
- **Restart**: the card as raised is kept in the assistant's settings (`assistant.ask_later`, written before the card goes up; removed once the conversation was told; no new table). The interaction history keeps a resumable card pending across a restart (`ExpireStaleInteractions`), and the next manager reads the records back with its first snapshot and reports the same card id, so the queue claims the card again without a second push (`waitOwnedQuestion`), or replays the end it came to meanwhile. A card whose time ran out while no manager ran is ended as an expiry (to-do, message). So the conversation is never left waiting for an answer that cannot come.
- **At most 20** such questions are out at once (`askLaterMax`); one more is refused and the tool says so.
- **Who uses it**: the assistant directory's instructions (`prompts/assistant/claude_md.md`) tell every conversation (the main one, leaders, the background supervisor, scheduled-task conversations and the others) to pass `wait: false` for anything that does not gate the very next step, and to keep the default for a confirmation the same turn needs (approving a message before it is sent). Until #7392 only the main conversation was told so, and the others always waited: a question in a leader conversation held its turn for up to 2 hours, with every message other conversations sent it waiting behind that turn. The tool result and the message that ends the card say all a conversation needs ("asked, the answer comes later as a message"), so the instructions now say it once for all of them. Codex and runtime assistants get the same tool and the same message.
- **A message waits behind a waiting question** (#7392, `assistant/ask_release.go`): a question asked with the default holds its turn, so whatever else is sent to the conversation meanwhile waits in its relay queue: a message from another conversation (`conversation_send`), a supervision message, a task's notice, the answer to an earlier question. The owner writing there takes the question back ("Ask on the phone" above); a message from anywhere else does not, since the owner did not answer. The question lets go of the turn instead, once it has been up for `askReleaseAfter` (2 minutes, about as long as a system message waits while the owner talks in a conversation, "Messages from the system wait for the owner"), so a confirmation answered in a minute or two keeps its turn. Letting go makes it a question asked without waiting in place: its record is kept (`assistant.ask_later`) and its card marked resumable on the queue and in the interaction history (`db.MarkInteractionResumable`), then the call returns (`prompts/agents/question_released.md`: the card stays up and is not asked again, end the turn, the answer comes as a new message) and the queued message goes in when the turn ends. The card is the same one: same id, expiry and deadline, no second push; the hub is shown it again as resumable, so the channels and the update treat it as one asked without waiting. From then on everything in this section applies to it. It stays as it was, and the message waits behind the turn as before, when it was answered or taken back meanwhile, when 20 such questions are out already, or when its card can't be kept across a restart (the history does not have it as pending). Only the conversation's own `ask_user` lets go; a confirmation card another tool waits on (a to-do change, `persona_settings`) keeps its call.
- **Clients**: no change. The card is an ordinary question card; the tool call shows its questions without picks, as an unanswered one does; the message appears in the conversation as the late answer of a question's to-do does.

## Interjecting while it answers (#3278)

A message sent while a Claude chat is still answering is written to the running `claude -p`, and Claude Code takes it up only at the next
tool boundary: behind a long foreground command (a download without a timeout, a wait loop) the user waits minutes for any reaction. Codex
merges such a message into the turn with `turn/steer` ("Codex" below); a Claude in a terminal pane has Send Now (#1097).

- **Switch**: `interject_background` in the assistant's settings (`GET/PUT /api/v1/assistant`, `persona_settings` key
  `assistant.interject_background`), **off by default**. The manager adds `interject_background: true` to `assistant.send` and to the
  `session.send` parameters it fills in (`SendParams`) while it is on. It decides for rounds the owner started.
- **A round the owner did not start is always preempted** (#4926, `assistant/owner_preempt.go`). The default in one sentence:
  Persona answers you first when you write into a round you did not start. On a round of the background supervisor
  ([unified-entry.md](unified-entry.md) "Each round starts clean"), your message first waits for the round's turn to end, up to
  30 seconds, so it goes in with every tool; only past that is the running command moved to the background. The owner's own message (typed in an app, sent through a
  channel, or a view action's) carries `interject_background: true` whatever the switch says while the conversation is on a round
  started by a supervision message, a scheduled task or follow-up, another conversation's message (`conversation_send` /
  `conversation_new`) or a task's notice. Persona continuing the owner's own work (a usage-limit reset, a wake for what it waits on)
  is not such a round, as under "Background rounds", and neither is a message the manager writes for itself (a channel's "answer
  again"). This case has no switch of its own: the moved work goes on and reports when done, so nothing is lost by it.
  - **The mark.** The manager keeps one mark per conversation, in memory, for the message that started the round it is on. It is
    set when a background message goes into a conversation the last session list did not show working, and it lasts as long as the
    round: every message of the owner's during it preempts, and a message that failed to go out uses nothing up. It ends when a
    session list shows the conversation stopped (idle, done or interrupted), or when another message of the manager's starts a round
    there. A mark no list shows working within 1 minute is dropped (a round that never ran, or ended between two lists), and one no
    list ever ends is forgotten after 24 hours.
  - **A background message into a working conversation** joins the round in flight, whose starter stands. If Claude has no tool
    boundary left to take it in at, it runs as a turn of its own once that round ends: the manager remembers the message and makes
    it the mark when a list shows the round ended, and drops it again if the conversation stays stopped for 1 minute or the owner
    speaks first (their message starts the round).
  - **The answer is the owner's.** The supervision push hold is released (`UserSpoke`) and the answer pushes and mirrors as
    "A user message absorbed mid-turn" says. Whether a command still runs is the node's check below.
  - **Limits.** After a manager restart the switch decides until the next background round. A round woken by a background task's
    report belongs to nobody's message: the switch decides. When one turn ends and the next starts between two session lists, the
    manager sees one round: a waiting background message's turn is then not marked, and a marked round's mark covers the turn that
    follows it. Codex needs none of this: `turn/steer` merges the message.
- **Node** (`node/assistant_interject.go`): when such a message reaches a chat that is busy, runs Claude on the CLI (not a runtime) and
  whose `system/init` named `claude_code_version` 2.1.139 or later, the message gets `prompts/assistant/interject.md`
  after its live snapshot, and once it is written the node sends the control request `{"subtype":"background_tasks"}` (Ctrl+B: every
  in-flight foreground Bash command and subagent goes on in the background). The model then reads the message right after the tool that
  was running returns; the backgrounded work reports back as a task notification, as `run_in_background` does. 2.1.139 is the first
  release whose control protocol has `background_tasks`.
- **Best effort**: an older or unknown version gets no control request; a refused or failed request is logged and the message
  still waits for the next tool boundary, as before. An idle chat is unaffected.
- **The note goes with every mid-turn message** (#4883): whatever the switch and the version, a message that reaches a busy Claude on
  the CLI (`midTurn`) carries `prompts/assistant/interject.md`. It asks the model, before its next tool call, for one sentence in the
  user's language on what it is doing and how long it will take or what it waits for, then to answer the message; the sentence about
  work moved to the background is there only when it was. Without it an owner's "what happened?" got six minutes of tool calls and no
  word. Tests: `TestAssistantInterjectOffOrOldClaude`, `TestInterjectNote` (`node/assistant_interject_test.go`).
- **A model recovery's resend is no interjection** (#10178): the bounded recovery's resend (`prompts/assistant/model_retry`) finds the
  chat busy because the recovery holds the failed turn, but nobody sent it mid-turn: it carries neither the note nor the
  `background_tasks` request. Test: `TestAssistantRecoveryResendIsNoInterjection` (`node/assistant_recovery_test.go`).

## Interactive Claude Code (#10058)

Claude assistant conversations run `claude -p`, which Claude Code reports with entrypoint `sdk-cli`. People report that on a
Claude subscription such runs use the 5-hour window faster than the same work in interactive Claude Code (about 1.7–2.9×;
anthropics/claude-code#97074). Anthropic documents no such weighting, so this is an observation, not a rule.

- **Switch**: `claude_interactive` in the assistant's settings (`GET/PUT /api/v1/assistant`, `persona_settings` key
  `assistant.claude_interactive`), **off by default**. While it is on the manager adds `claude_interactive: true` to every
  `assistant.send`, `assistant.warm` and `session.send` it sends or fills in (`SendParams`); the node's `modelExecution.Interactive`
  stays with a message that picks no model (a supervision message, a relay: it runs on what the conversation ran) and
  is part of the execution fingerprint, so an idle conversation resumes in the other kind of process with its next message and a
  busy one finishes its turn first.
- **Risk, said where it is turned on**: Persona then drives a program made for a person. Anthropic may read that as a different
  kind of use than it is, and an account restriction is possible; another project declined the same backend for that reason
  (yetone/magpie#707). Nothing disguises what runs: no `CLAUDE_CODE_ENTRYPOINT`, no changed headers or config files. The process
  is interactive Claude Code (entrypoint `cli`), started by Persona.
- **How** (`node/assistant_tui*.go`): the conversation's process is `personad assistant-tui`. Its first stdin line is its spec:
  the launcher command with the same flags (`--append-system-prompt`, `--settings`, `--plugin-dir`, `--mcp-config`, `--allowedTools`,
  `--tools`, `--model`, `--session-id` / `--resume`), less the print-only ones, plus `--permission-mode` and `--effort`; and the
  print-mode command. It runs the interactive one in a tmux server on a private socket, attached through a pseudo-terminal of its
  own with `destroy-unattached`, so Claude Code ends with the bridge however it ends. The server runs in the foreground
  (`tmux -D`, tmux 3.2 or later) as the bridge's child, its session created with `-N` so no other server can take it, so Claude Code, its MCP server and its hooks descend from the
  process the node runs for the conversation, as claude -p's do: the node takes the MCP server for the assistant's by
  that (`mcpAssistantOf`), and under a daemonized server the main conversation got an Agent's four tools. The pane gets
  the `PWD` claude -p gets (the node's, usually none), not the folder as tmux was given it: Claude Code takes its working
  directory from `PWD`, and a symlinked home's other spelling became a second working directory; the print-mode fallback
  gets the node's environment for the same reason. The pane runs a script in the bridge's
  private directory that holds the command: tmux takes at most 16 KB for a new session's command and refuses more ("command too
  long"), and an assistant's start, with its system prompt and tool rules, is often longer. It answers the start-up screens a terminal
  start answers (`claudeready.go`: the folder's trust, first-run screens, the Bypass Permissions confirmation of a start that asked
  for bypass, a guide), then speaks the stream-json `claude -p` speaks: a message is pasted (bracketed) and submitted once the
  input box takes it; the transcript becomes `system/init` as a turn starts, a `stream_event` per assistant message (usage, model,
  its text), `compact_boundary`, and `result` at `turn_duration` or an interruption; running background work is read off the mode
  line (`background_tasks_changed`). A turn the screen shows over without a line ending it (a local command such as `/compact`)
  ends after 15 quiet seconds. An `interrupt` control request is Esc and `background_tasks` is Ctrl+B, as in a terminal; the apps' Stop
  still ends the process, as it does for claude -p (background tasks included), and the next message resumes the conversation.
- **What changes**: live text arrives a content block at a time (an interactive Claude Code writes no partial messages); a long
  message (any message with its live snapshot) reaches the model inside Claude Code's own `<pasted_content>` wrapper, as a long
  message typed into an Agent's terminal does (the transcript readers drop it); an image is saved to a private file and the
  message names its path; Claude Code's tmux variables are unset in the pane, so it is never taken for an Agent in tmux; queue-strip "Send now" and cancelling a queued message answer with an
  error; permission mode and effort are start flags, so changing one resumes the conversation in a new process (as
  `bypassPermissions` always has; a busy one finishes its turn first). Guests, meeting rooms, supervision rounds (`--resume-session-at`
  is print-only), plan mode, warm spares (none is warmed while the switch is on) and Codex keep running as before.
- **Starts no message makes**: the manager also answers `claude_interactive` on `assistant.model`, so a process the node starts
  by itself (compacting a stopped conversation, resending what lost background tasks were doing after a restart) runs in the
  same kind of Claude Code as a send would start, and the next message does not restart it into the other kind.
- **A message that cannot be typed is never dropped quietly**: when the bridge cannot type a message (an image that does not
  decode, an input box that takes nothing for 10 minutes), its turn fails with the reason, as a send claude -p could not deliver
  does, and the messages already waiting behind it fail with it instead of reaching Claude Code out of order. Sent while a turn
  runs, it fails that turn as it ends. A control request that comes during the start is answered by the bridge and never reaches
  the print-mode fallback; only messages do.
- **Visible fallback**: when the interactive start fails (no tmux, Claude Code exiting, a start-up screen Persona does not
  answer, no input box within 2 minutes, a Claude Code that keeps no transcript because it was started from inside another Claude
  Code session), the bridge runs the print-mode command in its place and, once it runs, prints
  `{"type":"system","subtype":"persona_tui_fallback","reason":…}` (a command that cannot run gets no notice; the node never takes
  the notice for a start, so a claude -p that exits before printing anything fails the send as it would without the bridge), handing it the messages written meanwhile: nothing is lost. The reason names what ended
  and its last words: the pane's last lines when Claude Code exited, or what the tmux client printed when tmux itself would not
  start (`tmux exited before Claude Code was ready (status 1): command too long`). The node logs it, the
  conversation's execution report (`assistant.execution`; for the main chat Settings › Assistant › Runs on shows "Main chat runs
  with: Claude Code (print mode)" and the reason under it) carries `mode: "print"` with the reason in `mode_note`, and new starts on
  that node stay in print mode for 10 minutes before trying again. A process running as asked reports `mode: "interactive"`
  ("Claude Code (interactive)"); with the switch off `mode` is absent. Windows never tries: its conversations run in print mode and
  say so.
- **Web**: Settings › Assistant has the switch under Runs on, its footer saying what it is for and the risk; a hub without the
  setting shows no switch (`AssistantSettings.claude-interactive.test.tsx`). The apps do not show it yet; the assistant's
  `persona_settings` tool changes it from any client.
- Tests: `TestTUITranslatorReadsATranscriptAsStreamJSON`, `TestTUIArgsDropPrintFlagsAndCarryTheStartModes`,
  `TestTUIProcessRestartsForModeAndEffort`, `TestInteractiveStartGoesThroughTheBridgeAndItsFallbackIsVisible`,
  `TestPrintModeStartNeverRunsTheBridge`, `TestBridgeFallsBackToPrintModeVisibly`, `TestUndeliveredMessagesFailTheirTurn`,
  `TestFallbackForwardsOnlyMessages`, `TestCompactingAStoppedConversationKeepsInteractive`, `TestFallbackThatDiesIsAFailedStart`,
  `TestFallbackThatCannotRunSaysNothing`, `TestALongInteractiveCommandStartsInTmux`, `TestATmuxThatRefusesToStartSaysWhy`,
  `TestTUIScriptQuotesEveryArgument`, `TestAMessageWithNoModelPickKeepsInteractive`,
  `TestInteractiveClaudeCodeDescendsFromTheBridge`, `TestTheInteractivePaneGetsThePrintModePWD`, `TestFallbackGetsThePrintModePWD`
  (`node/assistant_tui_test.go`),
  `TestSendParamsClaudeInteractive`, `TestClaudeInteractiveSetting` (`assistant/claude_interactive_test.go`).

## Next-message suggestions (#4644)

The strip over an empty composer (#2978) reads `Session.suggestion`. Click or Tab fills the composer. The arrow sends a suggestion eligible for direct send. The manager considers these sources in order (`server/internal/assistant/suggest_next.go`).

- **An Agent's explicit offer** (#11874). A completed final work report can supply one optional `next` reply. It is available even if the owner opens the chat after the turn ends. The turn stays done and the owner decides whether to answer. A new message or turn spends the offer. Blocked or unfinished reports, pending decisions, quoted examples and free-form questions do not produce this suggestion.

- **Claude Code in a terminal** keeps the suggestion Claude Code draws in its own input box, read off the screen by the node
  (`server/internal/node/suggestion.go`). The manager never calls a model for such a session.
- **Every other chat** (Persona's own conversations, Codex and the other runtimes, a Claude session with no terminal) gets one from the
  `suggest.next` model purpose (Model configuration, under information processing; it follows that category's model, like chat titles),
  while the switch is on.

Rules for the hub-made one:

- **Switch**: `next_suggestions` in the assistant's settings (`GET/PUT /api/v1/assistant`, Settings › Persona on the web and Settings ›
  Assistant on iOS/iPadOS and Android), **off by default**. A native client keeps the value nil and hides the row when an older hub omits the field.
  Default in one sentence: no chat gets a model-made suggestion until you turn it on. Claude Code's own suggestion
  (`promptSuggestionEnabled`) is untouched by it.
- **When**: once per turn, when a turn ends well (working to done or idle in the node's list) and an app has the chat open at that moment
  (the hub watches the session for it: `Devices.Watched`). Every app with the chat open shows the same one. A new turn or a message going
  in drops it and cancels a call still on its way. Failed turns (a turn error), interrupted turns and subagents get none.
- **What the model reads**: at most the last 8 visible user and assistant text messages, 12,000 characters in all, oldest first
  (`prompts/tools/next_message_input.md`). No tool calls or results, no thinking, no images, no messages Persona wrote for the model. The
  call is a plain completion with no tools, MCP or memory.
- **Nothing sensible, nothing shown**: `NONE`, an empty answer, more than one line or more than 200 characters shows nothing; there is no
  stand-in and no retry.
- **Risky suggestions** (all sources): one that names a command, a path, a link, a secret-looking string or one of the risky actions
  of the Agents' safety boundary (`riskyWords`: delete, push, deploy, send…) carries `suggestion_risky: true`. The apps fill it in but
  show no one-tap send.
- **Clients**: the web, iOS/iPadOS and Android show the strip in Agent and assistant conversations, the main Persona conversation included,
  through their existing shared composer. A risky one has no one-tap send: iOS/iPadOS retains tap / Fill In and the preview but omits
  Send Now, the VoiceOver direct-send action and its long-press-send hint; Android retains tap-to-fill but omits its long-press menu and
  direct-send accessibility action. Filling never sends, and the ordinary composer Send remains available after review. Non-risky actions
  are unchanged; Android treats an absent `suggestion_risky` as safe and keeps retained suggestion text and its risk from the same row.

## Main chat, knowledge base, and context flow

Prompts live in `server/internal/prompts/assistant/` (`docs/assistant/prompts.md`): CLAUDE.md is `claude_md.md`, snapshots `snapshot.md` (main-chat sentence in `context.md`), due-task messages `task_run.md`, default morning briefing / evening review `routine_*.md`.


- **Main chat**: the first chat becomes main chat (assistant settings `assistant.main`), opened by default in the Persona tab. Snapshot begins "This is the main chat"; prompts require continuing prior work.
  node offline lists only the latest 80 sessions within 7 days; on a busy machine, a main chat quiet for two days was crowded out, removing "Main chat" from the sidebar (2026-11-01).
  The assistant reports these sessions in profile `keep`; its device layer (hub before 2026-11-04) supplies `node.configure` with `keep_sessions` on node connection and changes, listing them regardless of age.
  Handoffs from other pages (Agents long-press "Hand to Persona", knowledge "Ask Persona") enter main chat as `<persona-context>`.
- **Knowledge base** (`docs/assistant/knowledge.md`): `kb_search` / `kb_list` / `kb_get` / `kb_update`; snapshots include key projects / chats; prompts require writing conclusions back
  (project progress to timelines, code directories to `dirs` for Agent-project matching, read project profiles before dispatch).
- **AI twin**: item-processing prompts include the chat and its top 3 project profiles; afterward write new conclusions back to projects.
- Assistant and AI twin default launchers: `assistant.launcher` / `inbox.agent.launcher` in the manager's `manager.json` (local specifics in `CLAUDE.local.md`).
- The launcher in Settings › Model Configuration › Main chat (stored `model_config.assistant`, legacy settings `assistant.launcher` compatible; removed from Settings › Assistant since #1291) controls main and new chats: used for hub main-chat sends and when the App doesn't specify a launcher.
  If node sees a mismatch with the running process, it uses the new launcher with `--resume` on the same chat (fixed 2026-11-01: previously only new chats changed, main chat retained the initial launcher,
  appearing ineffective). Every main-chat message follows this setting, not the previous turn's launcher: #1355 briefly preserved the running launcher, so a follow-up changing it prevented returning (#1518).
  But a chat can resume only with its original Agent type. If settings select another type (Codex main chat → Claude launcher, vice versa, or into / out of a runtime), node uses the chat's launcher; if its remembered launcher is also the wrong type (overwritten by a failed start), use same-type `claude` / `codex`, ignoring the hub model for this message (`assistantResumeLauncher` in `node/assistant_agent.go`).
  Previously node rejected only while the process lived, then tried `claude --resume <thread>` for exited Codex threads: every message returned `No conversation found`, main chat showed "Session ended", messages stayed "Couldn't confirm delivery" (2026-11-05). To change Agent type, create a new main chat.
  That swap covers the setting only. A launcher of the other kind *picked* for a message (the web chip, the iOS Launcher menu) is refused before anything is sent: `409 launcher_other_kind` with `conversation_agent`, `launcher`, `launcher_agent` and `delivered: "none"` in the body, and a localized message saying the conversation can't change its agent. Before that, the node ran the message on the conversation's own launcher while the app kept showing the pick, and a model picked with it was remembered under a launcher that never ran it (#4662). The manager checks this in `sendModel` (`assistant/launcher_kind.go`) only when both the conversation's `agent` and the launcher's kind are `claude` or `codex`; a runtime, a conversation the machine doesn't list yet, or a launcher list that can't be read lets the message go as before.
  `schedule_followup` returns to the original chat using the same rules (main chat follows settings, other chats keep their own), not the Agents category (`sendModel` in `assistant/start.go`, hub asks `assistant.send_params`). Scheduled-task chats (morning briefing, etc.) also use the "Main chat" category, following settings per message and follow-up (#1544). The top-bar "Launcher" menu changes only this chat's next message; opening a launcher also allows selecting a model from its model list (the `/model` list),
  sent as message `model`, which hub uses for the execution fingerprint and node process restart; unselected means the launcher default.
  The menu (`?purpose=assistant`) and the check before each message list and accept only launchers that can run the assistant: both use `assistantLaunchers` (`modelProviderCompatible` in `assistant/models_edit.go`; the check is `mainChatFit` in `assistant/launcher_mainchat.go`).
  A Persona-managed Codex launcher counts like the computer's own Codex since #12569.
  A runtime's launcher counts only when it says `assistant`. A Paseo provider is no shell command, so it would fail every message with "launcher missing".
  A message that picks such a launcher is refused with the reason. A chat left on one moves to the main chat's launcher, and the web chips drop a stale pick and say so.
  When the main chat's configured launcher itself can't run there (gone from the computer, or not one for the assistant), it gives way only to a launcher of the same agent kind (`finishMainChatModel`, #4659): for Claude, plain `claude` first when the computer reported Claude Code and the user did not hide it, then the main chat's list in order; never Codex for Claude or the other way round. A launcher whose latest health check failed (signed out, out of quota) is skipped. The configured model stays when the substitute's model list has it. With no such launcher the message is refused (`assistant-start.launcher_no_substitute`), pointing to Model configuration › Main chat. A hidden launcher is not gone: the configuration that names it keeps running it.
- **Don't switch while busy** (#1554): while a process answers or has background tasks, changed launcher / model messages still go to that running process (session state records the actual one);
  restart with the new selection on the first idle message. Previously it rejected with "Model configuration changed; wait until the current task ends", trapping users when the assistant kept creating background tasks.
  node's post-restart "Background tasks were lost" message also first asks hub (`assistant.model`, forwarded to assistant `assistant.send_params`, same as phone sends), not starting from the session's last model.

## Permission mode and effort changes (2026-11-06, #5947)

For a running Claude conversation, the node matches each `set_permission_mode` or `apply_flag_settings` request to its
`control_response.request_id`. Writing the request to stdin does not mean Claude accepted it: `session.modes` waits for
the matching response before confirming the change. The same check runs before sending a message when a new or resumed
process needs its settings restored.

- Accepted fields update the process, the session list, and the persisted conversation settings. If a request changes
  both mode and effort, they are applied in that order; a successful mode change remains applied if effort is refused.
- A refused field returns to its previous value. The HTTP request fails with `409` and code `assistant_control_failed`,
  with a localized explanation containing Claude's original reason and the original reason in `detail`. Existing clients
  show the failure and restore their optimistic selection; no configuration is needed. An environment override such as
  `CLAUDE_CODE_EFFORT_LEVEL` or a model that cannot use the requested effort can cause a refusal.
- Unknown or duplicate response IDs cannot confirm another change. A process exit, a failed write, or an unconfirmed
  request also reports failure instead of claiming that the setting was applied.
  If the outcome is uncertain, the node stops the child process so a late acknowledgment cannot leave it running with
  a setting the app reported as failed; the next message resumes the conversation with its recorded settings.
- Settings chosen while the conversation has no running process are kept for its next start. The existing
  `bypassPermissions` restart rule remains: a process not started with it cannot switch to it through a control request;
  a busy process finishes its work before the restart. Ultracode remains available only in Agent sessions.

## Explicit skill mentions (Web only, #5357)

The Web assistant's existing mention menu reads `GET /api/v1/assistant/skills?machine_id=…&session_id=…&launcher=…`.
The owning manager resolves the conversation and effective model/launcher, supplies its own assistant directory and
identity, and asks that computer's `skills.list` in catalog mode. Client `cwd`, profile and file paths are not accepted
by this endpoint. Foreign/non-assistant sessions, guest chats and external runtimes without an inventory contract
cannot expose a supported catalog. The legacy `GET /skills` and non-catalog `skills.list` remain unchanged.

Codex uses the selected launcher's real app-server `skills/list` (`cwds`, `forceReload`), reusing its live connection
when possible or initializing a short-lived connection without a thread/model. This preserves launcher wrappers and
their configuration instead of guessing from `~/.persona/skills`. Persona's prepared `.claude/skills` extra root is
the same as normal Codex startup. Enabled entries with real regular instruction files are accepted; discovery errors
mark the inventory incomplete. At send time the resolved file becomes a typed `skill` input in `turn/start` or
`turn/steer`, alongside the unchanged reference text.

Claude resolves the same effective profile as `assistantSend`, then reads only immediate skill directories under
that profile's `skills` and the working directory's ancestor `.claude/skills` roots (at most 64 ancestors, 512 entries,
16 KiB frontmatter per file). Names/descriptions are data, not executed YAML or shell substitutions; bodies are not
read for the menu. `user-invocable: false` and local `skillOverrides: off` are respected.
Nonempty `user-invocable` scalar forms other than `true` are conservatively omitted by this bounded parser.
Known strict-plugin policy or `Skill` deny/ask rules refuse this adapter rather than work around native
customization/approval gates.
The launcher's existing `--settings` file or inline object is checked through the same settings reader as startup;
its values are never returned in the catalog or diagnostics.
The administrator's settings files are read and checked the same way (#9862): `managed-settings.json` and the `*.json`
drop-ins in `managed-settings.d` (hidden files skipped) under `/Library/Application Support/ClaudeCode`,
`/etc/claude-code` and `%ProgramFiles%\ClaudeCode`, each system's in Claude Code's merge order (the file, then the
drop-ins alphabetically). They apply last, so a managed `skillOverrides: off` cannot be turned back on by the profile,
project or launcher settings. Besides `strictPluginOnlyCustomization` and `Skill` deny/ask rules, a managed
`policyHelper` refuses too: its output would replace these files, and the node never runs it. A managed file that is
missing is skipped; one that cannot be read or parsed refuses. A file with only unrelated keys (for example only
`hooks`) leaves the menu listed. The profile's server-policy cache (`remote-settings.json`) still refuses by presence
alone: it may carry credentials and is never read. MDM configuration profiles and the Windows registry are not checked.
This bounded adapter deliberately does **not** claim to enumerate plugin, bundled or remote skills.
It sends an explicit, server-resolved exact-file reading instruction under existing permissions rather than a native
slash command, which could resolve a same-name skill elsewhere or apply frontmatter tool/permission overrides.

References are hashes of assistant, computer, working directory, launcher/runtime/profile and skill identity.
The manager and execution node revalidate selected identities against the current environment. Paths never come
from a client, and names never serve as fallback lookup keys. Deleted or changed-environment selections return
`skill_unavailable`, `delivered: none`, `retryable: false`; ordinary text does not query the catalog. Skill bodies
remain subject to existing runtime permissions and higher-priority instructions.

Offline contracts: `server/internal/assistant/skill_mentions_test.go`,
`server/internal/node/skill_mentions_test.go`, `server/internal/assistantskills/references_test.go`,
and `web/src/views/chat/MentionSkill.test.tsx`. Opt-in real runtime checks:
`node scripts/test/skill-runtime-smoke.mjs codex` (actual inventory, no model),
`node scripts/test/skill-runtime-smoke.mjs codex --model`, and
`node scripts/test/skill-runtime-smoke.mjs claude --model`. Model runs use disposable read-only-request fixtures and
existing authentication; optional `--model-id <existing-model-id>` selects a model for this invocation only.
Claude also accepts `--settings <current-profile-settings-file>` without copying its contents or changing the default.
Its Go test bridge uses production catalog, opaque-reference validation and instruction rendering against the
current profile (`CLAUDE_CONFIG_DIR`, or Claude's unset/default profile), explicitly keeping the catalog partial.
It confirms an explicit model override against the runtime's initialization event, then requires an actual
exact-file `Read`/successful tool-result pair and the random marker stored only in
the disposable instruction body, not merely a successful chat reply. Go must be installed for that bridge.
Output is allowlisted; arbitrary replies, catalogs and credentials are not printed. A successful runtime
smoke is not a production Web-to-hub end-to-end test. Synthetic browser fixture:
`PORT=25357 python3 scripts/mock/skill_mentions.py`.

Inspected browser captures use only that fictional fixture, not the requester's reference image or account data:
[light](../web/screenshots/skill-mentions/light-menu.png),
[dark](../web/screenshots/skill-mentions/dark-menu.png),
[narrow large-text light](../web/screenshots/skill-mentions/narrow-light-menu.png),
[narrow large-text dark](../web/screenshots/skill-mentions/narrow-dark-menu.png),
[selected](../web/screenshots/skill-mentions/narrow-selected.png),
[loading](../web/screenshots/skill-mentions/narrow-loading.png),
[unavailable](../web/screenshots/skill-mentions/narrow-error.png), and
[partial](../web/screenshots/skill-mentions/narrow-light-partial.png).
These demonstrate rendered interaction, not production connectivity or model execution.

Runtime contracts were checked against the official
[Codex app-server skills documentation](https://developers.openai.com/codex/app-server/#skills) and
[Claude skills documentation](https://code.claude.com/docs/en/skills).
No native client feature or build is part of this Web-only change.

## Workspace mentions (2026-11-06, #5594)

In the assistant's composer (web, and the iPhone/iPad shell composer's `ComposerMentionMenu` on #2599), `@` also lists **workspaces**: the discovered workspaces of `GET /api/v1/workspaces` (host spaces, current session folders and folders bound on the workspace board, on every computer; see [workspace-board.md](../web/workspace-board.md), "Workspaces and status"). The list is read each time the menu opens, works whether or not the board is turned on, shows one row per computer and folder, and leaves out host spaces without a folder. Agents' composers don't offer it: an Agent already works in one folder. On iOS the rows follow the folder's entries and precede sessions, match by title, folder, computer or branch, put title prefixes first and available before unavailable, and cap at eight; Health, visionOS and the frozen native Mac don't offer them. A failed workspace read (including an old hub's 404 or a manager's 503) leaves the other rows unchanged.

- **Picking one** adds a "Workspace" chip. On send it goes in front of the message like a session quote, as `<persona-context kind="workspace" title="…" url="" machine="<machine id>" cwd="<folder>" branch="…">` with a short human-readable body.
- **The manager fills it in** (`assistant/workspace_mention.go`): in `SendParams` for messages to the assistant's conversations and in `POST /api/v1/assistant/conversations` for a new one's first message (guest conversations excepted), each workspace block among the context blocks at the start of the message is replaced by `prompts/assistant/workspace_mention.md`. That block says to use that machine / cwd for `task_plan` and `agent_start` and when answering unless the message says otherwise, and lists the computer (and whether it is offline), folder, branch, the knowledge-base projects for the folder (`ProjectsForWork`) and the latest 5 Agents working in it or below (`sessions.list`, waited on for at most 3 s; the assistant's own conversations left out).
- Blocks further into the message (inside quoted text) are never taken for mentions, and a block naming a computer the hub doesn't know stays as the client wrote it. The transcript keeps the filled-in block, so the chat's context row shows what the assistant was given.
- **Not yet**: the native Mac, Android, and BeautifulUI's separate legacy iOS `PromptComposer` menu. They only need to list the same endpoint and write the same tag; the manager side already handles their messages.

## Pictures in replies (#9473)

A reply that names a picture by address (Markdown `![alt](https://…)`, often what an Agent found on the web) is drawn by the apps from the manager, not from the picture's host: `GET /api/v1/remote-image` (`assistant/remote_image.go` over `server/internal/remoteimage`, feature `remote_images`) fetches it from the open internet only, checks and scales it, and keeps it in `cache/remote-images/` (a cache, at most 512 MiB). The host sees the manager's address and never a device's, and the apps get the size they draw. Limits, refusals and the answer are in `docs/protocol/protocol.md` ("Remote images (#9473)"). The user's own Agents' and assistant's replies load at once; content other people wrote (Inbox, mail, Lark, channels) keeps a tap in the apps, since its pictures may be tracking pixels.

## Pitfalls

- Models call ask_user **in parallel** with other tools (measured: ask_user and schedule_followup in the same millisecond), so ask_user can't be the sole "confirm before acting" gate;
  genuinely side-effecting tools (`lark_send`, `mail_send`) request approval themselves in hub.
- Don't catch up very late scheduled tasks: after a morning of hub downtime, no noon morning briefing (skip if over 30 minutes late, record only).
- Scheduled tasks, follow-ups, and run history count as loaded only after successful database reads. Failures never fall back to the two default tasks; related APIs return 503, tools fail, the scheduler skips the round, and nothing is written;
  retry reading on the next call. Previously failure still marked loading complete, and the next save overwrote real tasks / follow-ups (review finding 2026-11-02, `TestScheduleReadFailureNeverOverwrites`).
- Silent pushes are system-rate-limited and don't reach force-quit Apps; failed wakeups tell the model to ask the user to open the App.
- Snapshots formerly used `--append-system-prompt`, assuming restarts refreshed them. Claude Code resume actually retains the system prompt from the chat's first start (next section),
  so the assistant kept seeing the first day's time / unread state. Now every message carries a snapshot.
- Go zero time `0001-01-01T00:00:00Z` appears in `last_run` / `next_run`; App `Date.realDate` treats it as absent.
- **Unattended runs must not block on approval** (measured 2026-10-30): default assistant permission mode asked phone approval even for `notify_user`, `schedule_followup`;
  nighttime follow-ups and Monitor events stopped there, pushing only "Approval needed". Persona tools are now preallowed (table above).
- **systemd restarting hub (compatibility unit `personad`) kills the whole cgroup**: assistant claude, background commands, and Monitor disappear before node can clean up, so running task lists
  must be persisted continuously (`node_sessions.tasks`), not only on shutdown.
- **Failed process startup swallows messages** (2026-11-05): node wrote a new process's stdin and immediately told hub "sent", while the launcher had already exited (e.g. `personad launch opus: no persona node`),
  losing the message. The App stayed "Delivering", logs only contained a zsh theme-plugin notice with `error`. A first send to a new process now waits for its first output line (up to `assistantStartWait`, 8 seconds):
  early exit reports "<launcher> didn't start; message not delivered: <last stderr lines>", and App shows retryable send failure. No output after 8 seconds counts as started (slow SessionStart hooks).
  The process stores stderr tail with ANSI and blank lines removed (`watchAssistantStderr`).
  Exit before writing formerly returned a pipe error (`send to claude: write |1: file already closed`); now wait for exit and drained stderr, reporting the same "Didn't start (exited after N seconds): …".
  For shell command-not-found (exit 127 / `command not found`, usually Claude Code auto-update reinstall removes `claude` for about 2.5 seconds),
  restart after `assistantStartRetryDelay` (2 seconds), up to `assistantStartRetries` (3), invisibly to users; other startup failures aren't retried (#2581, `docs/lessons/server.md`).
- **Another process replaces node's socket file** (2026-11-05): another node listened on the same path and didn't unlink on exit (`SetUnlinkOnClose(false)`), leaving an unserved socket;
  every `personad launch <Persona-managed launcher>` and hook got `connection refused`. The watchdog once checked only existence; now every 15 seconds it checks ownership (`os.SameFile`),
  dials if different, and recreates only when unanswered (`sockGone`). A responding newer node keeps the socket; don't take it back.
- In Chinese locales, `.hour(.twoDigits(amPM: .omitted))` is 12-hour (17:00 appears "05:00"); always use `AssistantScheduleText.hm` (fixed `HH:mm`), and `AssistantScheduleText.clock` for a daily task's `HH:MM` (#7499).

## Live snapshots and prefix caching

**Why snapshots can't be in system prompts**: prompt caching matches prefixes. A request consists of system prompt (including tool definitions), project context (CLAUDE.md, memory),
then the whole chat. Any earlier byte change prevents reading the later history cache, requiring recomputation at cache-write price and speed. Assistant processes are reclaimed after 20 idle minutes (the main chat's after `assistant.idle_release_minutes`, "Main chat release time and threshold" below), then restarted with
`--resume`, so only startup-invariant content belongs at the front. Snapshots (time, unread, meetings) always change and must accompany messages at the chat's end.

**Reality is more complicated** (Claude Code 2.1.314 measured on hub machine, same launcher):

- Resumed Claude Code chats **don't use** new `--append-system-prompt`; they retain the system prompt from the first request until compaction (documentation "System prompt flags in
  resumed conversations"; after changing text, the model still answered the first secret phrase and read the whole cache). Old snapshots therefore didn't invalidate cache: they **stayed the first day's snapshot**.
- What really breaks post-restart cache reads is **changed tool definitions**: copy a real 275k-token assistant chat (new session id, temporary directory, same exported Persona definitions),
  then restart; results below. Schema key order doesn't matter; removing one MCP tool rewrites everything. Persona definitions come from prompt files (deployment changes them)
  and phone-reported `phone_*` (the last connected device and its permissions), so node now compares tool lists at each start, logging
  "tools changed since its last start" on changes and cache read / rewrite counts for the first request after startup (`assistant_context.go`, `readAssistant`).
- Idle over an hour (cache lifetime) always rewrites; only smaller context helps then, see "Compaction" below.

| Main-chat copy (275k tokens) | First token | cache_read | cache_creation |
| --- | --- | --- | --- |
| Cold start (expired cache) | 9.8 s | 0 | 275,373 |
| Restart, unchanged | 7.0 s | 275,327 | 91 |
| Restart, changed `--append-system-prompt` text | 5.2 s | 275,418 | 46 |
| Restart, reordered tool schema keys | 7.7 s | 275,464 | 46 |
| Restart, one MCP tool removed | 8.5 s | 0 | 275,385 |
| `/compact` (warm cache) | 35 s to compact | 275,393 → 3,507 (compacted history) | |
| Cold start after compaction | 5.3 s | 0 | 36,195 |
| Another restart after compaction | 4.1 s | 32,886 | 3,354 |

Pre-change production records (first transcript reply after over 20 minutes idle): the long chat begun 10-28 had five 39–59-minute idle periods on 10-29–10-30 (cache still valid),
with `cache_read` = 0, rewriting 300k–370k tokens and taking 10–320 seconds for the first response segment, while other equally idle runs read cache. Every idle period over an hour rewrote fully, the last 510k tokens.
Current main chat's first message on 11-01 after 46 idle hours: 254k rewritten, 12.6 seconds.

**Implementation**:

- Before each message, node requests a hub snapshot (`assistant.context`, parallel with process startup), compares sections (unindented header plus indented lines) against the last sent snapshot for this chat,
  and includes only changes and time in `<persona-context>` (`prompts/assistant/turn_context.md`; readers also recognize the old name's tag, which old nodes and history carry: [rolling-upgrade-compatibility.md](../protocol/rolling-upgrade-compatibility.md), #3182) as the first text block. Prior content is remembered in node memory,
  cleared after compaction (`compact_boundary`; Codex `contextCompaction`), so the next message is full; node restart also sends one full snapshot. App, web, titles, and knowledge base remove it in
  transcript parsing (`transcript.ContextRe`), hiding it from users. Scheduled tasks, Lark `/agent`, and post-restart lost-background-task explanations all use `assistant.send`
  and include it too.
- Changed rows, not whole sections (#9078, `node/assistant_context.go`). A section is one subject whatever it says at the moment: its key is the header up to the first full-width colon or parenthesis, without a trailing count, so a count or a note in the header does not make it another section. Inside the sections whose rows carry an id of their own, a block of changes lists only the rows that are new or changed:

  | Section | A row's id |
  | --- | --- |
  | Task details (the main conversation's, and a leader's or side conversation's own tasks) | The to-do id the row prints after the title |
  | Agents | The label the row starts with (Needs you, Unread, Waiting, ...; one row per label) |
  | Unread chats | The label (@me, others) |

  Such a section says so on its header (`- <section>（只列有变化和新增的行，没列的行照旧）：`, "only the changed and new rows are listed, the others stand"; the same note is the first row under a header that itself changed), so the listed rows are never read as all of them; rows that are gone are named by id (`（已移除的行：…）`, "rows removed"), and the rows' order is given (`（行现在的顺序：…）`, "the rows' order now") only when it is not the old order with the new rows last. A section that left the snapshot is named in a closing list (`- 这份快照里已经没有的段落（它们之前的内容作废）：`, "sections this snapshot no longer has; what they said before is void"), where it used to be left standing. So the blocks a conversation has read add up to the snapshot each was made from, byte for byte (`TestContextDeltaBlocksAddUpToTheSnapshot`).
  Sent whole, as before: a section without row ids (one line of items joined by `；`, which their free text may hold too; Inbox follow-ups, which show a title and no id; the knowledge digest, whose rows sit under headings of its own), a section where some row has no id or two share one, and a section that is no shorter told by rows. A line before the snapshot's own sections (what the conversation is, a one-time notice) is never reported gone.
  The open to-do index (`- 开放待办索引…：todo_1「…」；todo_2…`, one line, ids only when the snapshot is short of room) is the exception (#13807): its items each start with a to-do id, so a block of changes lists only the to-dos that came (and, while both snapshots render titles, the items whose text changed) and names the ones that went, with no order list; the count of all open to-dos is its own section (`- 开放待办：共 N 件`). A change of rendering alone (ids only ↔ ids and titles) is not told, so for this section the blocks add up to the same to-dos, not the same bytes. An index whose items cannot be told apart (a title holding `；`) is sent whole. Before, any change to it re-sent all of it, which with 60 open to-dos was every message.
  A full block uses the same item comparison when the last raw index survives compaction: no additions or removals means no index section, and a change lists only its changed, added, and removed ids. After a restart there is no baseline, so the block omits the index rather than calling every current id new; the separate total still points to `todo_list`. This exception applies only to the global open to-do index, never to the current conversation's own task sections (#15196).
  Measured on two synthetic conversations shaped like the two recorded ones in #9078, rendered by the real templates (`TestContextDeltaBytesBeforeAndAfter`, after #11261 dropped the working-agent row): the 3 blocks after the first went from 3,768 B to 1,605 B, the 12 blocks of the longer one from 41,684 B to 9,701 B; unchanged rows repeated went from 1,752 B and 30,773 B to none.
- No `--append-system-prompt` remains; Codex `developerInstructions` contains only memory (changes with its index; resumed handling not measured).
- hub snapshot: reuse data for 45 seconds; refresh stale data in the background, wait at most 1.5 seconds per message, then use the prior snapshot if within 30 minutes. While the last read took longer than those 1.5 seconds, a message does not wait at all: it goes with the prior snapshot, and the read it starts is there for the next message (otherwise every message after a quiet 45 seconds waited the whole 1.5 seconds for nothing, #8948). A read slower than that is logged (`live snapshot: the read took …`) with the connectors' share, the usual slow source. Time is always current; ended items removed.
- App versions (#3672): one line names the Persona app on each kind of device with the version it last reported to the hub (`iPhone App 1.4.0 (build 52)（iOS 26.1）`; iPad, Apple Watch, Mac, Vision Pro and Android too), from `devices.list`'s `apps` ([protocol.md](../protocol/protocol.md) "Client capabilities and heartbeat"), so it holds while the app is in the background. Per kind the most recent device is shown, a real one before a simulator (`assistant/client_apps.go`). The line carries no time, so a reconnect does not make it a changed section.
- Size budget (#3203): a rendered snapshot carries at most 6000 characters (`snapshotBudget` in `assistant/context.go`). Measured on one install over 14 days before it, a full snapshot was a median 8.3k characters (12.5 KB) and the changed sections a message carried a median 6.6k; tasks, the Agents section and the knowledge digest were most of it, resent on almost every message because one of their items changed.
  Working agents render only their count in the Agents heading (`详情用 agents_list`), and done agents (24 hours) and muted agents render only their counts and the exact `agents_list` filter (`state=done` / `state=muted`) that lists them on demand (#11261); unrelated background session titles and machine-wide background workflow lists stay out of the snapshot, while a task opened from chat still carries its own running workflow on its task row. Needs-you and awaiting-reply agents, results waiting for acceptance, delivery approvals and today's meetings stay detailed.
  Free text is clipped where the snapshot is taken (an agent's line 160 characters, a plan step 60, a task's title 60 and current step 80, a report 160, an approval's title and gate 60, scheduled follow-ups the next eight and Inbox follow-ups the next five). Past the budget, sections are trimmed in order, least needed first: waiting agents, daily routines, Inbox follow-ups, scheduled follow-ups, other unread chats, the knowledge digest, unanswered questions, results waiting for verification, the open-to-do index's titles and links (keeping its IDs), tasks, unread agents, approvals.
  A section left out keeps its heading and its count and names the tool that lists it (`assistant/snapshot_omitted.md`), or says to open the Inbox in the App for Inbox follow-ups: never "none", never missing, since the node sends only changed sections and a missing one would read as unchanged. Never left out: agents waiting for the user or for a reply, plan cards waiting for "Start", failed scheduled runs, the calendar and @me chats. If that is still too long, the block is cut and ends with `assistant/snapshot_cut.md`.
- The raw hub snapshot puts the open-to-do total and `todo_list` entrance immediately after the time, before Agent details, so a hard cut still leaves a way to find the work. After the urgent Agent lists it carries an ID/title index of up to 100 open to-dos, regardless of origin or age (a question to-do is left to its own snapshot line only while that line shows it; once its ask record is pruned it is listed like any other); beyond that it keeps the total and points to `todo_list`. Under budget pressure titles and links are dropped from this index before the separate task-detail section is omitted. Full task details remain limited to 12, with open work ahead of recent closed conversation tasks. Showing a personal to-do does not enroll it for supervision or start an Agent.

## Local request source diagnostics

For people maintaining an Assistant installation, `scripts/diag/assistant_token_report.py --request-breakdown`
adds a read-only, local source-size report to the existing token diagnostic (#10151). It is off by default: no hub
setting, background capture, network request or runtime logging is added. Omit the flag to get the existing report.
The primary Assistant screens and model behavior are unchanged.

The token report continues past malformed transcript records. Invalid Claude usage rows are omitted, and unavailable
Codex counters still appear as unknown. The report covers the records it can read from the selected files.

```sh
python3 scripts/diag/assistant_token_report.py --request-breakdown --json scripts/diag/testdata/request_breakdown.jsonl
python3 scripts/diag/assistant_token_report.py --request-breakdown --json /path/to/local/transcripts
```

The first command is a synthetic capture with fresh, follow-up, resume and compaction samples. The resume sample's
1,300-byte tool result dominates; the follow-up includes observed cache reads. The fresh sample deliberately has
unknown provider usage. These are reproducible fixtures, not measurements of a live installation.

Two boundaries are reported separately:

- `anthropic_request_content` / `responses_request_content`: a complete request object exported locally by the caller.
  Each JSONL line has `type: "request"`, `runtime: "anthropic"` (Messages) or `"responses"` (OpenAI Responses), and
  `request` containing the unmodified request object. Optional `usage` is the corresponding provider response's usage
  object; `lifecycle` may be `fresh`, `followup`, `resume` or `compaction`; optional ISO `timestamp` supports `--since`.
  This script does not intercept requests or configure a runtime to export them. Use a caller's existing local export
  when available; some runtimes do not expose their assembled request. Captures contain private material and stay local;
  only the aggregate report is suitable for sharing. No request ids, tool names, arguments, messages, attachment names
  or URLs are emitted.
- `claude_transcript_inventory` / `codex_transcript_inventory`: the content visible in a transcript, counted once per
  observed block (Claude duplicate response blocks are deduplicated; Codex event mirrors are ignored). This is an
  inventory across the selected file/time window, **not** a reconstruction of any request's retained context. A
  compaction marker says `contains_compaction`; it does not prove which history survives. Runtime-owned instructions,
  tool schemas, current request and retained history remain unknown (`null`, or `?` in text). Codex's recorded base
  instructions and system/developer messages are exposed only as `observed_instruction_bytes`, a partial inventory.
  Existing per-kind provider usage stays alongside the inventory in the token report, without inferred joins to requests.

Byte definitions (separate from provider token counters):

- Text is UTF-8 bytes; structured values use compact JSON with sorted keys, Unicode preserved, and no extra spaces.
  The measurement is content size, not HTTP wire size or tokenizer output. Message role/id envelopes, transport
  options and provider-owned additions are excluded; request wire bytes and provider context tokens remain unknown.
- `instructions` measures exported system/developer instructions; `tool_schemas` measures the exported `tools` array.
  Omitted fields in a complete request export count as zero; unavailable transcript fields count as unknown.
- `snapshot_or_events` measures `<persona-context>…</persona-context>` blocks including their wrappers.
  `current_message` is the last user message's remaining text; `history` is other message text. Both may contain
  Persona event text outside those explicit blocks: no arbitrary content is guessed to be an event or instruction.
- `tool_results` measures returned text/structured content, excluding separately counted attachments. The largest
  result is reported only as the stable `tool_results` category and its byte size, never by tool name or call id.
- `attachments` measures serialized image/file blocks (references, metadata or encoded data as recorded), not decoded
  media size or image tokens. `other_content` covers remaining blocks, including tool calls and their arguments.
  Source buckets are disjoint; `observed_content_bytes` is their observed sum (including separately reported observed
  instructions for an incomplete transcript inventory). `items` counts measured text chunks/structured values, not
  model calls (the tools array is one value). `dominant_sources` lists the largest observed buckets, ties included;
  it may include partially observed instructions and does not rank unavailable runtime-owned content.
- Provider usage counters are copied only when observed as nonnegative integers. Missing counters are unknown, not
  zero. Anthropic input and cache counters keep their native definitions; Responses input includes cached tokens.
  They are not derived from byte sizes or made comparable by assuming a bytes-per-token ratio.

Compare runs with the same boundary and input scope. Changing one synthetic source changes only that source's size;
`python3 scripts/diag/test_request_breakdown.py` checks this, known-size inputs, lifecycle samples, unknowns and privacy.

## Compaction

Long assistant chats can grow to hundreds of thousands of tokens. After the prompt cache expires, restarting rewrites the whole context; compacting while the cache is warm keeps that restart affordable.

**Rule (2026-11-14, #7314; leads since 2026-11-25, #15318)**: every assistant conversation follows what its launcher declares, else the model, with one exception: the main conversation and each direction's lead conversation (`assistant.leaders`) on a launcher Persona manages are held to `assistant.main_compact_window` (400,000 tokens by default; `0` follows the launcher) when the launcher declares a larger window. The main conversation runs all day, so every turn re-reads its context and a start after the prompt cache expired rewrites all of it; on a 1M model it was compacted only at about 750k (idle `/compact`) or near 1M (Claude Code's own). Replaying recorded usage estimates a 35–45% cut in the main conversation's input (estimated, not measured). A lead runs on the main chat's launcher and is woken by the supervision messages of the tasks it hands out, so it was seldom idle long enough for the idle `/compact`: measured on one install over three days on a 1M launcher, two leads started their rounds at a median 500k and 634k tokens and were compacted only at about 750k, 810k and 970k (the last by Claude Code at the limit), while the main chat was compacted at about 310k each time. What drops out of their context early stays in the task ledger and the old conversation. The other conversations (one-off chats, scheduled-task conversations, Lark `/agent`, guests, Inbox twins, the legacy background supervisor, whose rounds start from a clean context) and every Agent keep the launcher's window. History: until #3066 (2026-11-10) the main conversation was pinned to `CLAUDE_CODE_AUTO_COMPACT_WINDOW=400000` and the others to `200000`, whatever the launcher declared, so an ordinary conversation on a 1M model compacted at about 167k; #3066 removed both targets.

- **Declared values**: a managed launcher's `CompactWindow` (else its provider model's `ContextWindow`), `MaxContext`, and `CLAUDE_CODE_AUTO_COMPACT_WINDOW` / `CLAUDE_CODE_MAX_CONTEXT_TOKENS` in its env or settings; for any launcher, what its own environment and settings, the node's environment, the user's `~/.claude/settings.json` and the project's `.claude/settings*.json` set. Where several sources disagree, the smallest counts, and the compaction window never exceeds the declared context window.
- **What a start writes**: only for a managed launcher. Its environment is Persona's (`providerEnv`), and an assistant start leaves the compact window out of the launch (`perStartEnv`), so the node writes the launcher's declared `AUTO_COMPACT_WINDOW` / `MAX_CONTEXT_TOKENS` into the process environment and the merged Claude settings, preserving launcher settings and hooks. `providerEnv` follows the same rule for agents and assistant conversations: `CompactWindow`, else the model's window, else nothing. Any other launcher (a shell function such as one that only exports a gateway URL and runs `claude --model opus`) gets nothing written: what it declares reaches Claude Code as it is, and when it declares nothing, Claude Code compacts by the window it knows the model to have (2.1.321: `min(model window, AUTO_COMPACT_WINDOW)`; `result.modelUsage.<model>.contextWindow` reports it, 1M for `opus`). Confirmed previously in `-p` stream-json: compaction before the next message emits `system/status` `compacting` → `compact_boundary`, `trigger: auto`; that turn waits for the summary.
- **Main conversation and lead window (#7314, #15318)**: the manager sends `main_compact_window` (the setting in effect) with the main conversation's starts and messages, next to `is_main`, and with a current lead's (a conversation in `assistant.leaders`; an outside direction's Agent is no assistant conversation and gets none): `sendModel` (the apps' messages), relays, the manager's internal `session.send` messages (a lead's supervision digests among them) and the main chat's warm spare in `Service.call`, and the node's own messages through `assistant.model`. No other conversation's carries it, and a value a caller set for one is dropped. Which conversations get it is the manager's call: the node applies the window that comes with any assistant conversation but a guest, only on a managed launcher. When the declared window (the compaction window, else the context window) is larger, the compaction window becomes the setting's, written as `CLAUDE_CODE_AUTO_COMPACT_WINDOW` (Claude Code) or `model_auto_compact_token_limit` (Codex); the context window stays as declared, a smaller declared window stays, and nothing declared stays nothing. On a managed launcher the window is part of the execution fingerprint, so a process started with another window is resumed in a fresh one on the conversation's next message while it is idle: a conversation made a lead takes the window that way, and a lead that ends (Unpin) goes back to the launcher's window from its next message. On any other launcher the node drops it before the fingerprint, so sending it restarts nothing there. The warm spare drops it the same way, so the main conversation's next start still adopts it. A message the node sends by itself gets the window from the manager through `assistant.model`; the node also keeps it, like `is_main`, with the conversation's in-memory record for a Compact context with no process running, and the context meter's auto-compact threshold follows it. Absent or `0` means no cap: an older manager with a newer node, or the reverse, keeps the launcher's window (a node before #15318 applies a window only with `is_main`, so a lead there keeps the launcher's window). Change it with `persona_settings` `assistant.main_compact_window` (`0` or 100,000–10,000,000; below 100k it would compact every few turns, since the fixed part of the conversation is tens of thousands of tokens and Claude Code compacts about 33k short of the window) or `PUT /api/v1/assistant` `{"main_compact_window": N}`; it is one setting for the main conversation and the leads. A conversation already past the window compacts on its first message after the change (1–6 minutes); a Compact context while it is idle avoids that wait.
- **Idle compaction**: Claude Code's own idle compaction runs only interactively, not `-p`. Before reclaiming an idle process, the node sends `/compact` once the context reaches 75% of the conversation's window: the declared one, else the window Claude Code reports at the end of each turn for the conversation's model (the last reply's model, else the entry that read the most; a later report moves it up or down), and the smaller of the two when both are known. A 1M model is compacted at 750k, a 200k one at 150k, the main conversation and the leads held to 400k at 300k. Until a window is known the threshold is 150k (75% of 200k, the smallest window Claude Code holds a current model to, and the one it assumes for a model it does not know); Claude Code reports the window at the end of every turn, before a process can be idle, so the default only stands for an agent that reports none. For the main chat, `assistant.idle_release_minutes` and a saved `assistant.idle_auto_compact_tokens` move the reclaim time and replace this threshold ("Main chat release time and threshold" below). Don't compact this process again; reclaim normally next idle. This isn't a chat turn: App state unchanged, no push; transcript `/compact` displays "Compact context".
- **Codex**: a managed launcher's declared window is passed as `-c model_auto_compact_token_limit`, capped by declared `model_context_window` values (launcher arguments, `config.toml`; the smallest across profiles); for the main conversation and the leads, also held to `assistant.main_compact_window` (above), which is passed even when the launcher declares only a context window. Otherwise nothing is passed, and Codex compacts by what the launcher's own flags and `config.toml` set, else its model's default. Do not raise Codex's `model_context_window`. `thread/compact/start` exists; idle compaction is still not integrated. **A turn whose compactions make no room is stopped (#10278)**: when what survives a compaction (instructions, tool declarations, the retained request) is above the model's compaction limit, Codex compacts before every step and the turn never ends (one recorded turn: 42 compactions in 31 minutes, each request after one 166–173k tokens against a 124,518-token window). The node reads each request's usage (`thread/tokenUsage/updated`: `last.inputTokens`, `modelContextWindow`); a request right after an automatic compaction that still takes 90% of the window or more made no room, and the second such compaction in a row interrupts the turn (`turn/interrupt`, as Stop) and ends it as the user's to act on (`turn_error_kind: user`, the reason in `node-codex-assistant.compaction_loop_stopped`). If the turn ran on a model the bounded recovery switched to, that switch is dropped, so the next message goes to the configured model instead of the same fallback (`assistant_codex_compaction_loop.go`). A compaction that made room resets the count; the user's own Compact context is not counted.
- **Scope history (#6481, #3008, #3028, #7314, #15318)**: the assistant manager still sends `is_main` with starts and messages (relays get it in `Service.call`). Alone it changes no compaction limit; the main conversation's window comes with `main_compact_window` (above), and since #15318 a lead's comes the same way without `is_main`. The node records both with the conversation, and both are part of the execution fingerprint (the window only on a managed launcher), so warm spares and restarts still match by main scope and window.
- **Rolling upgrades and activation**: the rule is the node's; the main conversation's and the leads' window also needs the manager that sends it, and a lead's needs a node from #15318 on. Update **the resident node's personad binary**; the new limits apply on the next assistant-process start; an idle existing process is resumed with the new policy on its next marked message, while active background tasks are not killed. A hub-only deploy or a download-channel publication without installing the node update is not enough. Publishing and deployment remain separate, approval-gated operations.

### Idle-aware unattended delivery

The main chat can receive supervision, scheduled follow-ups and messages from other conversations while the user is
away. Before delivering one of these non-user messages, a Claude node can compact the existing conversation in place
(#8658). This does not change the launcher's window or replace the model's automatic compaction or the idle-process
reclamation rule above. Other conversations, Codex and external runtimes keep their existing behavior.

Three advanced assistant settings are available through `persona_settings` (changes need the usual user approval),
and through the assistant settings API:

| Setting | Default | Allowed values |
| --- | --- | --- |
| `assistant.idle_auto_compact` | `true` | `true` or `false`; `false` disables this additional compaction |
| `assistant.idle_auto_compact_minutes` | `30` | Whole minutes, 1-1440 |
| `assistant.idle_auto_compact_tokens` | `300000` | Whole tokens, 10000-10000000 |

Both limits must be reached. Idle means time since the last **user message received**, not time since the assistant
last answered: unattended rounds do not restart that clock. Context means the last actual main-model request's
`input_tokens + cache_read_input_tokens + cache_creation_input_tokens`, or the post-compaction token count, not the
cumulative totals in `result.usage` or `modelUsage`. Unknown context never triggers compaction.

The manager attaches `idle_compaction: {enabled, idle_minutes, context_tokens}` to the main chat's `assistant.send`
and `session.send` calls (with `release_minutes` / `release_tokens` once saved, next section). The node uses structured `message_source` (or the existing protected source metadata), not
keywords in the message, to distinguish unattended messages. Older nodes ignore the optional policy; a missing
policy from an older manager leaves delivery unchanged. Upgrade the main chat's node as well as the manager to use
this feature.

The node sends `/compact` through the same path as manual compaction and reports it in `session.compaction`. The
unattended message is written only after the compaction command ends, not merely when its boundary is reported.
A failed compaction still allows the original message through. Delivery waits at most 30 seconds, below the manager's
90-second relay deadline; a still-running compaction returns a retryable `compacting` refusal and leaves the original
message unsent in the sender's existing queue. Further unattended retries refuse immediately while compaction is in
flight, without issuing another `/compact` or consuming another attempt. Delivery can resume after the real result,
not merely its boundary. The compaction itself retains its separate ten-minute limit. A cancelled request or exited
process also leaves the message unsent for the existing retry path.

The idle clock, last actual context and attempt latch are kept in the node's existing Postgres-backed session
record. User activity resets the clock on receipt, even while a send lock is held. A context reduction also restarts
the idle interval. Dropping below the token threshold rearms compaction; context must then grow back over it and the
new interval must elapse. When Claude Code explicitly reports that the summary was empty, the next eligible
unattended delivery at least five minutes later can try once more. Other failures leave the existing attempt limit in
place. The attempt and its retry are kept across node restarts. The manager carries this node-owned state
opaquely through `db.NodeSession` and the existing
`node_model` settings row, including the policy, idle timestamp, last actual context and attempt latch; no new table
or migration is needed. A newly observed conversation or a changed policy starts a conservative fresh idle interval rather
than guessing when the user last spoke.

### Main chat release time and threshold

The compaction before an idle process is let go (the "Idle compaction" bullet above) is the cheap one: the process is
still running and the prompt cache, which lasts about an hour, still holds its context, so the summary reads the cache
and whoever writes next starts from a small context. In the main chat, the session reports whether the compaction
finished or failed and preserves Claude Code's reason when available. Both release settings can be changed (#12008):

| Setting | Unset | Saved |
| --- | --- | --- |
| `assistant.idle_release_minutes` | The node's 20 minutes | Whole minutes, 5-55, after the process's last activity |
| `assistant.idle_auto_compact_tokens` | 75% of the conversation's window | The same number of tokens, whatever the window |

- **Decoupled from the window**: a saved token count replaces the 75%-of-window threshold outright, above or below it.
  The main chat can use a 1M window mid-conversation (`assistant.main_compact_window`) and still be compacted at 200K
  before it is let go. A saved threshold at or above Claude Code's own auto-compaction point is never reached.
- **One token setting for both idle compactions**: once saved, `assistant.idle_auto_compact_tokens` is the threshold
  before unattended delivery (above) and before release. `assistant.idle_auto_compact` turns off only the former;
  compaction before release ran before it and stays. While the token setting is unset, release keeps 75% of the window,
  so a hub that never saved it behaves exactly as before. In `persona_settings`, saving the default 300000 while unset
  is therefore a change (it gets an approval card), not "unchanged".
- **Below the cache lifetime**: 55 minutes is the upper bound, so the `/compact` (sent on the reaper's next one-minute
  sweep) still reads the one-hour cache. Past an hour there is nothing left to save: the next start rewrites the
  context whatever its size, and the pre-delivery compaction covers unattended messages.
- **Wire and rolling upgrades**: the manager adds `release_minutes` and `release_tokens` to the main chat's
  `idle_compaction` only when the setting is saved (`omitempty`), so the policy is byte-identical to before otherwise.
  The node keeps the policy on the conversation's record (as for unattended delivery) and the reaper reads it from
  there each minute, so it outlives a node restart. A value out of range is ignored. Side conversations keep 20 minutes
  and 75% of the window; a process with background tasks is still kept up to 12 hours; supervision rounds are still
  never compacted. A Codex main chat follows the release time but, as before, is not compacted before release. An
  older node ignores both fields; an older manager sends neither. Both must be updated to use it.

### Impossible gateway usage (2026-11-06, #3300)

Claude Code estimates context from the last reply's `usage`. Some gateways report zero for every reply in a turn, then total all request inputs in the last reply (85k-token chat reported 1.7M):
before the next message, Claude Code thinks the window is exceeded and auto-compacts—successful compaction needlessly loses context; a failed summary returns "Prompt is too long · automatic compaction failed", repeated forever on every message.

- **Impossible usage** (`transcript.PlausibleContext`): request input over 1M (largest context window), or over transcript bytes by more than 200k (at least roughly one byte per token, with 200k allowance for system prompt, tools, memory).
  Exclude such values from context usage (`ContextTokens`) and node `p.context`, so they don't trigger idle compaction.
- **Repair** (`node/assistant_usage.go`): after each claude turn `result`, inspect transcript (whole file on the process's first check, then from the previous position), replacing impossible `input_tokens` / `cache_*_input_tokens` in-place with 0
  (pad with spaces, preserving size, inode, reader offsets; subagent rows untouched), first saving `<transcript>.usage-backup-<time>` beside it. Values remain in process memory, so exit after repair (don't exit with background tasks; still repair for next startup).
- **Retry**: if the turn was "Prompt is too long", continue after repair with `assistant/usage_retry` (only a `<persona-context>` block, no App bubble); at most once per chat per 10 minutes. Don't retry without impossible usage (genuinely too long, retain error).
- Measurement (Claude Code 2.1.318, transcript copy + fake API): unchanged resume attempted compaction (`pre_tokens` 1.7M), empty summary returned "Prompt is too long"; zeroed usage skipped compaction and sent directly.
  Previously undelivered user messages remain in transcript; Claude Code combines them with the new message into one user turn (synthetic error replies excluded), so retry needn't include their text again.

### Memory recovery after compaction

For the main and direction conversations, the first input after a compaction carries a recovery context of at most
6 KiB of UTF-8, including its markup. Task workers use at most 3 KiB. The node persists a checkpoint when the runtime
reports a compaction and consumes it only when an input carrying the matching recovery block is delivered. Failed
sends retain it; a restart retains it; an older delivery cannot clear a newer checkpoint. A snapshot prefetched before
idle-aware compaction is refreshed at delivery only when that input originally requested context. Intentionally
omitted guest/public context never triggers a recovery fetch. The block accompanies the next real input and does not start a turn.

The manager collects scoped to-do state, direction instructions, project `STATE.md` (falling back to the existing
project summary), effective user decisions and recent user inputs. Main and direction recovery includes the last seven
days of decisions; a worker keeps its own standing decisions without that age limit. Agent decisions and reported
outcomes are never labeled as user decisions. Each quote keeps a durable source reference and is included whole or
omitted; only state prose can be shortened. XML-sensitive source text is escaped. The deterministic assembler accepts
source records separately from collection so a unified ledger can replace the legacy sources without changing delivery.
Guest, work-trust, clean scheduled rounds, unrelated machine/session pairs and unmanaged conversations receive no recovery.

Summary instructions follow the configured user language. Simplified Chinese explicitly forbids drifting to English;
English and Japanese have their own instructions. All require user decisions verbatim with references, running task and
session IDs, non-negotiable rules, and items waiting for the user. Claude reads these instructions from the generated
instructions and receives them on explicit/idle compaction; Codex receives them through its `compact_prompt` setting.
Claude SessionStart(compact) has a separate recovery handler, including headless Assistant conversations. It waits
at most 1.5 seconds for the manager, using scoped records prepared asynchronously during regular context reads and
fresh user quotes. Prepared records expire after two minutes; recovery on a platform input reads the source records
afresh. A socket reply consumes its matching checkpoint only after a successful write. Hook-first and
stream-boundary-first ordering share that checkpoint; timeout leaves it pending for the next platform input.
Report-only hooks, guests, subagents and sessions not owned by the platform cannot use this handler.

### Manual compaction (2026-11-04, #1727)

User selects "Compact context" in any open Assistant conversation's menu, including direction and other persistent side
conversations: `POST /api/v1/assistant/conversations/{id}/compact` → node `assistant.compact` (`node/assistant_compact.go`).
Web, iOS and Android offer it when the hub advertises `assistant_compact`; progress belongs to the conversation on
screen. "Clear context" and "New main chat" remain main-only. This manual action does not change automatic compaction
windows or any main-only automatic policy.

- Same path as idle compaction: write `/compact` into an existing process, or start with `--resume` (chat's previous launcher / model); Codex uses `thread/compact/start`; runtime chats unsupported.
  A Codex Agent on an app-server takes the same call when sent `/compact` alone (#10188, [codex.md](../integrations/codex.md) "Sessions on the daemon (app-server route)").
  A guest conversation (a meeting's room too, which the manager compacts in long meetings: [meeting-minutes.md](meeting-minutes.md) "Long meetings: lean batches and compaction (#8253)") resumes as on any message, with its guest flags and settings from its record; one whose launcher runs another agent by now is refused, nothing started (#8253). The manager compacting a room sends `only_running: true`: a process that is not running is refused with `not_running` instead of being started only to be compacted (an older node ignores the field).
  Answering or already compacting returns 409, not queued: users can't see when a `/compact` queued behind a turn begins.
- Progress lives in node session state (hub Postgres), sent with session lists as `compaction`: `running` → `done` on `compact_boundary` (Codex `thread/compacted` / `contextCompaction`);
  if turn `result` arrives first without a boundary, `failed` with Claude Code's original error (e.g. "Not enough messages to compact" for short chats);
  10 minutes without result, process exit, or node restart means `failed` (`reapAssistants` checks each minute). Automatic compaction triggered within a turn doesn't change it.
- Not a chat turn: no Working state, push, or unarchive. Transcript `/compact` and boundary remain "Compact context" / "Context compacted" notices;
  clients draw one divider per compaction: "Content above compacted · Manual · Nk tokens before compaction".

### New main chat (2026-11-04, #1727)

A long-used main chat accumulates large context; users can replace it: `POST /api/v1/assistant/main/new` (assistant route, `assistant/start.go`).

1. Reject if the old main chat is answering or compacting (409 `busy`); App supplies its observed main-chat id (`expect`), rejecting if another device already changed it (409 `main_changed`) rather than replacing the new one again.
2. Carry background (`carry: summary`, default): node `assistant.handoff` extracts the latest compaction summary from the old transcript (Claude `isCompactSummary`, Codex `compacted`, up to 12k characters)
   plus subsequent text messages (up to 12, 6000 characters, `<persona-context>` removed), stored in settings `assistant.main_handoff`.
3. Clear `assistant.main`, add old id to `assistant.former_main` (up to 10, in profile `keep`, so hub ensures node always lists them),
   rename "Old main chat · Through M/D" (user-assigned, not overridden by autonaming), archive—all in one assistant operation.
4. The next main-chat message creates a new session (App `POST /api/v1/assistant/conversations` with `main:true`, Lark bot / plugin main-chat sends created by assistant with `main`, all via `startMain`),
   with `assistant/main_handoff.md` prepending background as `<persona-context kind="context" title="接上旧主对话">` (`title` means "Continue old main chat"; App context tag), then clearing it;
   `carry: none` still includes the old main-chat id, allowing `conversation_read`.
   Android first creates an empty `prompt`, then sends separately: bind background to the new session id (`for`) and include it with the first `POST …/messages` or Lark / channel message, all through `assistant.send_params`, not as a separate turn.
   An empty or whitespace-only prompt with no images reserves an idle conversation without fetching a live snapshot or
   sending model input. Its first real message carries the snapshot and any runtime initialization instructions;
   those instructions survive a node restart or a refused send until delivery succeeds. An image-only prompt still
   starts a turn. The same rule applies to Claude Code, Codex and assistant-capable runtimes.
5. The old main chat's open tasks follow (#4866, `assistant/main_handover.go`). When the new main chat starts, each task's `supervision.supervisor` and `origin` move to it together, as a closed leader's workers are rehomed. The ledger stays as it was: criteria, reviews, round, parking and owner waits.
   A task waiting for review is signalled again to the new main chat. The planning direction lead supervises its own tasks and main supervises other dispatches; only a previously assigned legacy background notice may still finish in the old background conversation. The card of each task the new main chat now supervises goes there with an active hand-off badge; the archived chat keeps its history.
   A leader keeps supervising its tasks; only their `origin` moves. Closed tasks, the owner's own to-dos and other conversations' tasks stay where they are.
   Making another conversation the main one in the settings (`PUT /api/v1/assistant` `main`) hands over the same way.
   The supervision loop also moves whatever a former main chat (`assistant.former_main`) still holds. That covers switches made before tasks moved, and a move a restart cut short.

Why:

- **Don't create immediately**: without a user's first message, we'd have to invent one, making the first message machine-authored. Deferring matches the existing "no main chat yet" flow all clients support.
- **Carry compaction summary, not knowledge base**: people, groups, projects, memory, live snapshots already exist for every assistant chat (CLAUDE.md, tools, message snapshots); carrying them duplicates content.
  What's lost is chat-specific conclusions and ongoing work, exactly the compaction summary. Without compaction, carry only recent messages; confirmation suggests compacting first.
- **Don't delete old chats**: archive only removes from lists; sending again automatically restores them (`unarchiveOnSend`).
- **Tasks follow the main chat**: the new main chat owns both the ledger and supervision of the open tasks it inherits; a legacy background conversation only finishes notices already assigned to it, and nobody reads an archived chat.
  Every check that compares a task's supervisor with a conversation would treat the new main chat as an outsider:
  its `agent_send` would reach the agent as a side conversation, the done judge would discount the agent's next turn, and `agent_supervise` would refuse.
  Receipts and reviews would also go to the archived chat's feed.

iOS / macOS (#1762): avatar top-bar menu (same Mac menu, also sidebar main-chat context menu) adds "Compact context" / "New main chat…", shown only with hub features `assistant_compact` / `assistant_main_new`.
Status above input (`MainConversationActionsView.swift`): "Compacting context…", `done` shows "Context compacted" for 4 seconds, `failed` shows dismissible "Compaction failed: …";
show `done` / `failed` only if this page observed `running` transition or completion is recent (within 1 / 10 minutes), never ancient results on opening (`MainConversationActions.shown`, same as web).
New-chat confirmation is a form sheet, "Carry the old chat's summary" on by default; success returns to empty main-chat page, and the first message starts it via the no-main-chat flow. Divider wording matches web / Android.

### Clear context (2026-11-12, #5095)

"Clear context" (web: `/clear` in the main chat and its ⋯ menu) is Cindy's `/clear` for the main chat: the context is cleared in place and the next message starts with a fresh one.
`POST /api/v1/assistant/main/clear` (hub feature `assistant_main_clear`, `clearMain` in `assistant/start.go`). Past the check it runs to the end even when the app stops waiting (`context.WithoutCancel`):

1. Under the same lock as `main/new` and `startMain`: `expect` as for `main/new` (another device's change since is refused, 409 `main_changed`); a background an earlier `main/new` left waiting (`assistant.main_handoff`) is dropped; the conversation is put away as `main/new` puts it (`putAwayMain`): first in `former` (so its machine keeps listing it), renamed "旧主对话 · 至 M月D日" and archived.
2. After the lock (the node may be slow, and every main-chat message waits on that lock): a turn is no refusal (Cindy stops it too). Persona's own process for the conversation is closed (`session.close`; the node stops it with what it ran in the background, so nothing starts another turn there) and a question it waits on is withdrawn. A conversation the user resumed in a terminal (a pane) is not closed: the node's `session.close` would close that terminal (`closeMain` is in the terminal guard's list as a user action; `session.stop` refuses assistant conversations). Unlike the Stop button it does not stop a Feishu request the conversation reports on (`UserStopped`): that would interrupt the Agents on the tasks it handed out, and a clear leaves that work running. A failed close is logged.
3. Nothing is carried over: no summary, not even the old id. The next main-chat message starts a new conversation without `--resume` (`startMain`) and with that message alone.

Why archive and not delete or hide in place: the old conversation's id is what other flows keep: to-dos and tasks it handed out (their origin, reports and briefs), `conversation_read`, the knowledge base's reading of past conversations and the run history. Archiving keeps all of them working and the messages reachable under **All chats › Archived**; sending to it again restores it (`unarchiveOnSend`). Why a new conversation and not the same id: the session id is the runtime's own (Claude Code's transcript, Codex's thread), and a fresh context is a new one.
`former` keeps the last 10 main conversations put away, by New main chat and Clear context alike: someone who clears often pushes older ones out sooner, and those are then ordinary archived conversations (still listed while their machine lists them, still found by id).
Why no confirmation: nothing is lost (`docs/design/DESIGN_SYSTEM.md` 4.4, the reversible rung), and Cindy's `/clear` asks nothing either. "New main chat…" stays for carrying a summary over.

Android offers it in the main chat's title menu and as `/clear` in its composer (`docs/android/android.md`). iOS offers it in the avatar menu and the sidebar main-chat long press, and as `/clear` where that composer offers commands (#5100). Without `assistant_main_clear` none of it shows. After it, the page stays an empty conversation (the same top bar and composer); the first message starts the new main chat. A 409 `main_changed` refreshes and shows what the hub has.

## Scheduled tasks

Previously two systems: routines (daily time) and follow-ups (one-shot wakeups from `schedule_followup`), in separate settings;
App "Scheduled tasks" listed only routine switches. For "check every 30 minutes", the assistant scheduled the next follow-up at each turn's end:
removed from the list when the turn began, so an hour blocked on unanswered Bash approval left the page empty, impossible to inspect / edit / stop.

Now one `Task` table (`assistant/schedule.go`, settings `assistant.tasks`), four `repeat` types. Model input retains its runtime role but carries persisted message-source metadata: `once` is `source.kind="followup"`, `daily` / `interval` / `cron` are `source.kind="patrol"`. The same source accompanies an existing-chat run, reuse of a task's own chat, and a new-chat fallback. Clients retain the legacy `injected` detail but exclude these messages from user bubbles and user-only navigation ([protocol.md](../protocol/protocol.md), "Message source").

- `once`: one-shot follow-up, disabled after running and retained one day (hidden in App) to link "schedule the next one", then removed;
- `daily`: daily / selected weekday time; over 30 minutes late skips with a record;
- `interval`: every N minutes (5 minutes–7 days), `at` is next run, advanced on original cadence; missed during downtime runs once on return, then counts from now.
- `cron`: a standard 5-field cron expression (`minute hour day-of-month month day-of-week`, or `@hourly` / `@daily` / `@midnight` / `@weekly` / `@monthly` / `@yearly` / `@annually`) in `cron`, read in the user's time zone (`assistant/schedule_cron.go`, #4399). Runs like `daily`: the times missed during downtime count once, as the latest of them, which runs when it is under 30 minutes late and is skipped with a record otherwise.

**Cron expressions** are parsed by `github.com/robfig/cron/v3`; the hub adds the rest:

- Matched on the wall clock of the user's time zone, so a clock change never runs one time twice: the hour a fall-back repeats runs once (its first pass), and a time the clock skips (spring forward) runs shifted by the gap (02:30 → 03:30), at most once even when that is also a time it runs anyway.
- Day of month and day of week follow Vixie cron: when both are restricted (neither starts with `*`), a day matching either runs; otherwise both must match (`0 9 */2 * 1` is odd days that are Mondays). Day of week 0 and 7 are both Sunday.
- Two runs are at least 5 minutes apart (as `interval`), checked from the minute and hour fields. `@every`, a seconds field, `TZ=` / `CRON_TZ=` and an expression that matches no date in five years are refused with the field and what it takes (`assistant-schedule.cron_*`; English for the tools).
- The words for an expression (`cron_text`, the preview's `text`) come from `github.com/lnquy/cron` in en / zh-Hans / ja; none when a day field is a step from `*` and the other is restricted, since the words would read Vixie's "both" as "either". `next_runs` always shows what it does.
- `schedule_routine` takes `cron` instead of `time` / `weekdays`, `schedule_followup` instead of `at` / `every` (a run of a one-off that reschedules itself with `cron` turns it into a cron task). Their replies give the expression, its words, the time zone and the next 5 runs, so the model can check them against what was asked.
- A routine can set `run_if` through `schedule_routine` (with the owner's approval) or through `PUT /api/v1/assistant/tasks/{id}`. Give a shell `command`, an optional `timeout_s` (default 30, from 1 to 60), and `on_error` (`run` by default or `skip`). The node runs it in the owner's shell before any briefing facts or conversation turn. Exit 0 proceeds, exit 1 skips and records the first line of output, and other failures (including timeout or an unreachable node) follow `on_error`. If the run proceeds after a check failure, its history entry includes `run_if_error`. A skip sends no push and does not advance briefing state. One-off follow-ups, **Run now**, and automatic retries of an already admitted run bypass the check. Omit `run_if` to keep it, or send null to remove it.
- Older apps: iOS reads an unknown `repeat` as `once`, and Android and the web send it back without `cron`; a `PUT` without `cron` keeps a cron task's rule and saves the rest (title, prompt, on / off, permission mode). The legacy `GET /api/v1/assistant/followups` lists cron tasks with the other non-daily ones. Clients show cron tasks when the hub has `task_cron`.

`until` is optional on a repeating task. It is stored and compared in UTC and shown in the user's time zone. One-off tasks ignore it, and saving a one-off clears it. Omitting `until` on update keeps the stored time; `null` clears it. Once that time is reached the task is turned off and a skipped run is recorded instead of running.

**Skip one occurrence (#12075).** In Web Scheduled tasks, open the actions for an active daily, interval or
cron task and choose **Skip next run**. The row shows the scheduled time you selected. **Cancel skip**
is available before the chosen time. The hub checks the selected time and the saved skip
before applying a change, so an outdated page cannot clear a later skip.

The occurrence appears in history as **Skipped by you** and does not count as a failed run. The next
scheduled occurrence follows the existing rule and its usual catch-up behavior after a restart.
**Run now** is still available and leaves the scheduled skip pending.

Editing the schedule's time, rule, or enabled state can replace the selected occurrence. After
a conflict, review the current time before trying again. Older managers keep their existing schedule
actions. To verify the Web flow, run the mock hub with `MOCK_SKIP_OCCURRENCE=1` and use
`node web/e2e/task-skip-occurrence.mjs`. Use `MOCK_SKIP_OCCURRENCE=legacy` and the script's
`--legacy` option to check older-manager behavior.

**Last run** (#10182): the scheduler saves a run's `last_run` before the run starts (a restart in between loses the run rather than repeating it), while `last_status` / `last_error` change only when the run is recorded (sent, failed to start, skipped, its turn failed). The task also stores when the run that outcome is of started (`status_run`: the recorded run's own start, an automatic retry's being the retry's, so a Run now that ended without starting its conversation never lends its time to another run's outcome; a task saved before it has none, which means `last_run`). Every view shows the outcome with that time: the live snapshot's daily tasks, `list_followups`, and `GET /api/v1/assistant/tasks`, which sends it as `last_run` and never sends `status_run`. So a run that is still starting shows the previous run with its own time and outcome, not its own start with the previous outcome: a morning briefing read "last run today 08:45 failed" about the day before's failure while it was starting, and told the owner its run had been recorded as failed. A run lost to a restart leaves the previous run shown until the next one; nothing marks a run as running. Recording an outcome makes the next message take a new live snapshot.

**Tasks it hands out** (#8045): a round's `task_plan` follows the plan rules ([unified-entry.md](unified-entry.md) "Plans"), and `user_requested` counts for nothing in it. A writable task waits on its card unless the owner let this scheduled task start its kind, on the card of one of its own rounds: the task's `start_grant`, shown with Remove on its page and dropped when the task changes. A task with a grant runs each round in a new conversation without the live snapshot, and a round that reads anything from outside (mail, Inbox, web pages, a shell) keeps the card; a task given facts, the report or a state note never offers a grant ([unified-entry.md](unified-entry.md) "A scheduled task's own start grant").

**Wait for main-chat setup** (#4253, `assistant/schedule_ready.go`, design `docs/assistant/onboarding.md` 4.4): due runs check readiness without probing / tokens; if a required step (computer, Claude Code, model) is blocked, skip with "Computer or model not connected yet; skipped", no push / retry. Check only what's needed: follow-ups to another computer don't await assistant computer; tasks using another launcher don't await main-chat Claude / model. "Run now" isn't blocked (user must see actual errors), nor one-shot tasks (reminders, assistant follow-ups): they disable on due, so skipping loses them; send normally, push failures, retry once. If a user-action setup step (no computer, not logged in, invalid key…) causes a skip, store `routines_waiting` in `assistant.readiness` (not sleeping / offline computers; wakeup isn't news), clear before the first full-ready run (`mode: full`), and add a main-chat activity `notice`. Default morning briefing stays enabled.

Readiness skips carry the optional persisted `not_ready` flag (#1857). The failure-streak counter ignores them, including temporary sleep / offline skips: they do not increase or reset the streak, and cannot trigger its notification. Other failures and skips still count as before. Existing history without the flag is not reclassified from its localized error text; the ready-again notice above is unchanged.

**Failure and retry**: node accepts `assistant.send` before model output, so runs ending in API errors (quota, removed model, upstream 503) formerly counted successful.
Before a turn is allowed to finish, node's bounded transient recovery may keep it working through a same-model retry (scheduled task execution does not switch models; [../integrations/claude-integration.md](../integrations/claude-integration.md) "Headless assistant"). The watcher therefore does not mistake an intermediate provider error for the run's outcome.
`watchScheduledRun` now follows the final turn, reads final text, records API errors as failed, pushes "Scheduled task failed: …", and retries after 3 minutes once
(`Retried`, no further push on repeat failure). Retry uses the task **then** in the table, not the startup copy (before #2313, users followed failure push and disabled / deleted it,
yet it silently reran the old prompt 3 minutes later): deleted tasks don't retry, disabled repeating tasks don't retry, once follow-ups disabled on due still retry, edited prompts use new text.

### Task execution context

The web task editor's **Advanced**, before the prompt, is an advanced group for people who need a task to use
a particular assistant backend, model, computer or directory. Its collapsed summary shows the launcher name, actual
agent backend, model and thinking effort. Every untouched control explicitly follows Assistant, with known inherited
values shown; its title and summary share one line. Task rows use the same summary from their effective `execution`,
while older responses without it keep their legacy display. After a computer's catalog loads, its rows show the saved
launcher ID for entries missing from that catalog. The saved fields stay unset so future Assistant changes
still apply. It reuses the chat's
assistant-compatible launcher list (`purpose=task`), model picker and capability-derived thinking choices; it never changes Main chat.
Only the explicit task purpose computes computer-scoped task inheritance; `purpose=assistant` keeps the ordinary
Assistant picker default. Every editor discovery request names the same chosen manager as the task save. Defaults,
launcher lists and model catalogs are scoped to that manager as well as the computer and launcher, including while
requests are loading or fail; a former manager's late answer cannot replace them.
Plain-text prompts work independently of rich input and content references.

`POST /assistant/tasks` and `PUT /assistant/tasks/{id}` accept sparse `launcher`, `model`, `effort`,
`execution_machine_id` and `cwd` fields. Omission keeps a saved choice; an empty string restores inheritance.
`execution_machine_id` is the execution computer, distinct from `machine_id`, the source conversation's computer.
Model overrides retain the existing `model_config.overrides.v1.task.<id>` record and `task.<id>` purpose. Saves that
change both records use the existing settings transaction and publish neither on failure. `model_revision` is the
model record's optimistic revision; a stale supplied revision returns 409. Execution-aware clients include it for
deliberate changes, including a choice equal to today's default. Old clients that omit these fields keep them.
An unversioned launcher/effort-only echo whose effective binding is unchanged preserves the saved record and creates
no provider or computer pin. Explicit clears remain clears. Old dormant `model_config.task.<id>` records remain inert.

Inheritance is resolved on the execution computer, then uses the existing `mergeModel` contract. A shared managed
provider retains its binding across computers in scope; a local launcher on another computer takes that computer's
compatible launcher defaults, even when its alias matches Main chat's. Its model, effort and profile are not
copied from another local provider. The picker reports the same computer-scoped defaults. A different explicit provider
starts from its own defaults. A model or effort override pins the effective provider so a later
Main chat switch cannot pair it with another agent. Task-specific choices require the task's own conversation. Turn off
**Continue the original chat** or enable **Start a new chat for each run** on a repeating task. That setting keeps its
source available for later reference. Agent means a real headless-capable launcher, including compatible
runtime launchers, rather than an orchestration role. Work-only computers remain excluded.
This includes pinning the computer already inherited from Assistant when the save omits `execution_machine_id`.
An explicit empty computer restores computer inheritance; clearing the launcher/model/effort and computer restores
full inheritance. An untouched task stays sparse. The editor shows a validation error and
disables Save and Run now while source-chat continuation and explicit execution choices coexist. Empty patches
can clear historical choices without requiring a live computer; toggling continuation back on revalidates them.
Existing source tasks may retain their unchanged legacy launcher/effort-only metadata, which did not affect the source
chat's execution. Both title edits and runs keep that source's actual context; new execution choices still require
the task's own conversation. A saved model, directory or execution computer does not qualify for this compatibility.

The shared `TaskExecution` contract (`assistant/schedule_execution.go`, web `lib/routines.ts`) is returned as task
`execution` and stored on each dispatched run as `execution`. It contains `machine_id`, `launcher`, `agent`, `model`,
`effort`, `cwd`, `permission_mode` and a non-secret configuration `fingerprint`. Older run snapshots may retain
`runtime` as compatibility evidence: a former Agent SDK task context starts a new conversation; new Claude runs
follow the current CLI-only policy. Assistant-capable runtime launchers remain independently supported.
Empty model/effort means the launcher's own default; it does not claim an unobserved concrete model.
`execution_error` reports an unresolved
configuration on the task view. A task's `launcher`, `model`, `effort` and `model_revision` describe its saved override.
Content-reference consumers must use this same selected computer, launcher/runtime and directory for discovery and
execution, rather than making a parallel context or reading files on the web host.

`cwd` accepts an existing absolute or `~/` directory on the execution computer. Leading or trailing whitespace is
refused instead of changing the chosen path. `assistant.directory` resolves its
symlinks and checks directory read/search access there, with the workspace resolver's protected-path restrictions.
Saving and each run validate it; the node checks again before sending. Validation never creates the requested
directory, changes permissions, or writes assistant instructions into it. Automatic built-in skill synchronization
also stays confined to prepared assistant directories; project-owned skill files are preserved. An empty path uses the prepared assistant
space as before. The task records the physical directory used, so a changed symlink target requires a fresh chat.
A node must announce `task_execution`; unsupported nodes refuse the new execution request with `node_outdated`.
For a scheduled run this is a definite pre-delivery refusal, so the same marshaled request waits for the update instead of
repeating model preparation or conversation bookkeeping. The default bound is 600 seconds, configurable as
`assistant.task_execution_wait_seconds` (0–3600) in `manager.json`. Zero records the refusal immediately. A successful update releases exactly one send with
every required option unchanged. If the bound expires, the run records `task_execution_wait_timeout` as a definite
not-sent deadline and does not arm the ordinary one-off retry; there is no late queued attempt. Uncertain delivery keeps
its existing no-replay classification.

Only an attested inherited ordinary task may use the old-node omission. Setting
`assistant.task_execution_legacy_fallback: false` disables it and makes those tasks wait too. Explicit computer,
directory, launcher, model, effort, Health and strict-snapshot choices never qualify. Actual omission adds
`compatibility_fallback: "legacy_task_execution"` to the run's history; `execution.legacy_compatible` alone means only
that the task was eligible, not that the fallback happened. The web history appends **Used legacy task execution** to
the run's ordinary outcome and timing detail.

The scheduler and **Run now** share `runTask` and the frozen resolved binding. A reusable conversation must match its
computer, launcher/agent, model/effort/runtime fingerprint and physical directory. The fingerprint includes the execution
computer's advertised launcher identity; a changed local gateway starts a new chat. A runtime-owned conversation can
resume without a local Claude/Codex state record: the node validates the owning assistant-capable runtime, its launcher
and physical directory from runtime metadata. Missing metadata for a known owner is uncertain and cannot trigger a
replacement send. Permission changes are applied
on the next send. An unknown legacy context with explicit choices starts fresh. Replacements preserve previous
conversations and run history; each run records `conversation` (`source`, `resumed`, `new`) and, when replaced,
`conversation_reason`. For task-owned execution, offline computers, unavailable models and missing directories fail with their reason, with
no substitute computer, model or directory. A failed send with uncertain delivery is not sent again as a new chat.
This also covers a new conversation's first send: a lost reply or a node reporting delivery as unknown/already accepted
keeps the one-off failure notice but does not arm its automatic retry. The run's `delivery_unknown` flag also cancels
an older retry still pending when a later manual run loses its receipt. A request confirmed never sent still retries once;
model snapshot and default-directory preparation failures retain that notification and retry behavior.
A temporarily unreadable launcher list does not prove a launcher is gone: saving and running an undiscovered custom
launcher preserve its exact binding while retaining model, effort, capability and computer-scope validation;
an authoritative missing or incompatible launcher remains a failure, including a successful empty node list. A missing
or null node catalog and an unreadable managed-provider catalog remain unknown, rather than evidence of deletion.
Configured wrappers and managed providers participate in the merged list. Unknown backend metadata stays unset.
An unconfigured source-chat follow-up can use its own conversation when the source computer is missing/offline,
model preparation fails before sending, or the node confirms the source is gone. Source sends use `resume_only`;
the fallback records `source_unavailable`. A timeout, closed link or other unclassified send failure records failure
without a replacement send or automatic one-off retry. The one-off failure notice is still pushed.
Scheduled headless runs retain bounded same-model transient recovery but never advance to another model.

The web advertises this editor through the `task_execution` feature and shows run configuration under each history
entry. Native clients can continue editing legacy fields without clearing execution choices; their task-specific
execution controls are not added here. Acceptance uses isolated RPC/CLI and browser fixtures, with no real model call.

### Files and skills in scheduled prompts

In the Web task editor, type `@` to choose a file or skill, or `/` to choose a skill or reusable prompt.
The editor uses Assistant's composer and keyboard menus. Selections appear as removable chips. Save writes
the text and its leading context tags together and marks the task as containing selections. Reopening the task restores both. Plain text uses the existing
task format, including when an older hub supplies the editor.

The task's effective computer, launcher and working directory determine the available references. When the
selected computer needs an update for file and skill selections, the editor shows a message. Text and reusable
prompts still work there. A changed
execution environment can require choosing a reference again. At save time the selected node checks the
references. At run time the headless send checks them again. File references identify files in the working
directory, and skill references identify entries in the active catalog. A scheduled run and **Run now** use
the current resources and the task's usual permissions. A selection does not store a copy of a file or skill.

### Push after running (#2396)

Every run completes a turn in a chat; hub pushes "Finished" for every turn (`notifyFinished` in `assistant/fleet_push.go`). Tasks like morning briefings always have results to read, so that's fine;
but watchers like "Tell me when new mail arrives" also push "New mail · No new mail" when nothing happens. Prompt edits can't fix it: hub pushes "Done" even if the assistant says nothing.

Each task therefore has `push`:

- `""` (every run, default): push each run's answer as before.
- `"asked"` (only when it explicitly notifies): only `notify_user` pushes reach the phone; no news means no push. Run messages explain this (`task_run.md`).
  Errors (`TurnError`), model-truncated answers, blockers (⏳), "Scheduled task failed: …", and user-answer cards still push normally.

**Implemented per chat, not per turn.** `asked` tasks use their own chat unless they go on in the chat they came from (`session_id`), and never
main chat (`ownConversationIfQuiet`: selecting `asked` clears a `session_id` that is, or may be, main chat; `runTask` sends a run whose chat became main to its own). Snapshot tasks include `push` (`TaskView.Push`); if the finished push (`quietTaskFinish` in `assistant/fleet_push.go`, by the tasks of the manager's last snapshot) recognizes an `asked` task's own chat
(`run_session_id`) and a normal completion (`assistantwire.NormalFinish`, based on pushed text: final work explanation or last answer), suppress "Finished"
but still finish through `settle`. Channel forwarding (Lark, plugin sync and task pushes) skips under the same `quietTaskTurn` rule.

**In the chat it came from** (`session_id` set, such as a leader conversation's patrol or daily round, #3673), the run writes `push: "asked"` into its persisted
message source. A normal completion is quiet when the turn's input (its last user text that is neither queued nor a wake) has that source, the run answered
after it, and nothing of the user's follows it (`quietTurnItems`):

- The run answered when it wrote text after its input, or made tool calls after it and the transcript has that turn's end (`turn_duration`, #3934).
  A patrol that only calls tools, `notify_user` among them, is therefore quiet. A turn with no output at all, or whose end is not written yet, pushes.
- A user message absorbed mid-turn (a queued text or image item before the final answer) makes the answer the user's too, so it pushes and mirrors.
- A user message held until the turn ends starts the next turn. That turn's input has no scheduled push mode, so its answer pushes and mirrors.
- A background wake after the patrol input stays quiet only when the patrol's own turn launched that task (its call id, or its task id in a call's result).
  The result of a task the user started earlier, a loop firing, or a peer's message pushes.

Only conversations an asked task posts into are read (`askedTaskPostsInto`). After reading the transcript, the manager checks that the session's status, revision, and
update time did not change, so a turn that started during the read is not hidden. It does not compare `last_text`, which the node collapses and cuts to 200 runes.
Errors, cut-offs, stuck turns, `notify_user`, and escalated asks reach the user as before. A source without `push`, including one from an older manager, keeps the old
every-answer behavior.

Cost (own chat): user-authored messages in that chat also don't push on completion. People generally don't chat in task-owned chats; if they do, they're likely viewing the App.

Boundaries (`assistantwire.QuietTaskFinish`, shared by hub / channel forwarding):

- **Only Claude Code and headless Codex chats** (session `agent` empty or `codex`): must distinguish "nothing happened" from failure. Claude errors appear in final text,
  recognized by hub (truncation); headless Codex failures report session `turn_error` from node (#2571, enabled since #2932). Runtime failures (`agent` is runtime id) aren't in session state,
  so runtime asked tasks still push "Finished" rather than swallowing failures as no news.
- **Main chat is never quiet**: if a user makes a task chat main chat, normal pushes resume and the next task run creates another chat.
  If main-chat identity is uncertain (assistant settings read failure, hub hasn't received profile), treat it as possibly main: push / forward normally and don't reuse it for the task.
- **Other tasks don't enter quiet chats** (an asked task's own chat, `run_session_id`; a chat an asked task only posts into is not one): `schedule_followup` from an asked-task chat runs in its own chat; existing tasks targeting it also move to their own on running (they require every-run push).
- Decide after `settle`, not as "already pushed": background-pending turns remain suspended, their wakeup turn is judged separately, errors push normally.
- Run-message explanations ("It's time again…", "This task only notifies when you use notify_user…") are stripped when the assistant renews with the whole message (`stripScheduleHeader`),
  preserving task identity, `push`, and prompt.
- On hub restart before snapshots, or the first run before a new chat id is reported, hub doesn't know chat ownership and pushes normally.
  After receiving a snapshot, assistant disconnection still uses the last copy.

Why not suppress only "this run's turn": three per-turn designs were reviewed (input `quiet`, node tracking whether all inputs were quiet and completion successful,
reported to hub); each had races swallowing user pushes: Claude processes queued messages after background turns; compaction completion reuses prior results;
failed Codex steer becoming start is claimed by another input's `turn/started`; old push callbacks read new-turn markers if hub misses intermediate Working.
Correctness requires correlating every input / completion id in core node message paths. Per-chat decisions avoid those races and have a fixed answer.

App `PUT /api/v1/assistant/tasks/{id}` omitting `push`, `inbox` or `facts` preserves it (old Apps); old `PUT /api/v1/assistant/routines` always preserves all.

### Put it in the Inbox when done (#6430)

Push says whether a run reaches the phone; `inbox` says whether it leaves a row in the Inbox, so a result is not missed behind a quiet unread dot. The two are independent. On the edit page (web `TaskEditor.tsx`, iPhone `AssistantTaskEditor`; shown when the hub has `task_inbox`) the setting is **When done, put it in the Inbox**: **Don't** (`""`, the default; a task from before keeps behaving as it did), **Only if worth a look** (`worthy`: the run puts a row there itself with `inbox_post` when it finds something worth the user's time, tier Needs you) and **Every time** (`every`: after each run that answered, the manager adds a row, tier FYI, titled by the task, saying what the run answered; `assistantTurnSettled` → `inboxPostEvery`). A run's start message says which one applies (`task_run`). `schedule_routine` / `schedule_followup` take `inbox` (`off` / `worthy` / `every`, left out keeps it) and their descriptions say to set it only when the user asked. `inbox_post` itself (`assistant/inbox_post.go`, `prompts/mcp/inbox/inbox_post.md`) is for the user's own request too ("tell me if there is anything worth a look"): its description allows it only on the user's explicit request or a task's setting, and `reason` (required) names which, shown on the row. A Just me meeting turn and a health conversation refuse it, like `notify_user`. The rows and their source: `docs/assistant/inbox.md` ("Assistant rows").
`null`, non-string, unknown values return 400. `schedule_routine` / `schedule_followup` accept optional `push` (`every` / `asked`); omission defaults new tasks to every run,
preserves existing values. `schedule_routine` with `id` / `push` can change only this field on any task. Web, iOS / Mac, Android editors include the switch (#2570, #2676: shown with hub `task_push` or an already-asked task; "Only when it notifies" hides "Continue original chat", list marks "Push only on notification"; tasks without prior `push` omit it on save for old hubs).

Each task can separately set **launcher (model), permission mode, effort** (empty = assistant default, unchanged), editable only in the App, not assistant tools
(no self-escalation). `bypassPermissions` requires startup `--permission-mode` (control requests can't switch): node doesn't use a spare on new startup,
and restarts existing processes in another mode (`node/assistant.go`). Persona's mode-menu "Allow Everything" works likewise (#2416):
stop idle processes immediately, resume next message; busy processes wait until the turn ends. App mode persists in hub session state over old transcript values. "Allow Everything" first confirms, then marks the picker red with consequences.
Tasks continuing original chats (`session_id`) also switch those chats to these settings.

**Same task, same chat** (requested 2026-11-01: "Run the same scheduled task in the same session"): tasks without `session_id`
(App morning briefing / checks, `schedule_routine`) formerly created new chats each time, unable to compare "with yesterday" or "is last time's item still there?". Now:

- First run creates a chat and stores `run_machine_id` / `run_session_id` / `run_launcher` with task settings, surviving hub restart;
  later runs `assistant.send` into it. node keeps these listed like main chat (`keptSessions`), even weekly tasks, so they open and busy state is knowable.
- Continued messages say "⏰ Title (scheduled task)", lateness, and "This is still this task's own chat; previous results are above; explain changes from last time",
  then the task prompt (possibly edited / compacted, included every time). Knowledge instructions and "No need to schedule the next run" appear only initially (`Continued` in `task_run.md`).
- Failed resume creates and records a new task chat: missing chat (Claude's 30-day transcript cleanup, never written: hub `resume_only`; if neither process nor transcript exists, node errors rather than silently creating blank history under the old id),
  offline machine, send failure, task / assistant launcher type change (claude ↔ codex can't resume), assistant machine change, a chat whose last turn ended `Not logged in`
  in a profile other than `legacy` (resumed with `config_dir: "legacy"`, which the node refuses for it; #6186). Log the reason.
- If due again while the previous run is running or awaiting approval / answer, **skip**, recording "Previous run still running; skipped", not queuing in the same chat
  (would answer twice, the second seeing half-finished work). One-shot follow-ups still send (skip loses them); App "Run now" returns 409 with reason when busy.
  Only one simultaneous send per task (`schedule.sending`), preventing due / Run now overlap.
- Chat-scheduled tasks (`session_id`) send to their originating chat first. Recurring tasks created in the main chat run in a task-owned chat. Failures to resume fall back to a task-owned chat, recorded and reused.
- App Scheduled tasks "Chat" section says "Runs in the same chat", opening it in Persona in both layouts / shells. "Start a new chat next time"
  (`POST /api/v1/assistant/tasks/{id}/fresh`) clears the stored chat for large context / restart; old chat remains in Chats. Never-run tasks say "A new chat starts on the first run".

In the Web Scheduled tasks editor, expand **Run continuity** to edit a recurring task's state note or select **Start a new chat for each run**.
The hub includes the last saved note in each run. Keep cursors and a short record of what has already been reported, up to 4,096 UTF-8 bytes.
A run can stage a replacement with `schedule_state_set`, passing the run key from its own prompt; the hub checks it against the conversation's latest scheduled-task prompt, so a run in a reused chat cannot use an earlier run's key, while a background task's notice, a supervision message or another conversation's message that comes in during the run does not end it. Only a prompt the hub sent for a run hands out a key, on a line of its own: text the user typed, pasted or quoted, another conversation's message and the saved note quoted in the prompt do not, whatever they say (#9043). Staging is temporary. The hub saves the replacement only if it can track the run through a successful turn and write the note. When the run's turn ends with nothing staged yet while work it sent to the background (a subagent launched async, a background command, a workflow) has not reported back, the run stays open until each has and the conversation is idle, within the run's 30-minute watch (#8913). A run that staged before its turn ended, or sent nothing to the background, is saved when its turn ends.
If the hub cannot register the dispatched run or monitor its conversation, it discards the draft and the next run reads the last saved note.
A user edit takes precedence over updates from a run that started before that edit. The editor sends the note's
revision when changing its text. If a run already saved a newer note, the hub rejects the stale edit so the user can
review the new note before changing it. Saving another task field keeps the current note.

**Progress on a linked to-do** (#9755): `schedule_followup` with `every` or recurring `cron`, and `schedule_routine`,
accept an explicit `todo_id`. Only a conversation allowed to supervise that open to-do can set the association;
titles and the latest task never imply a link. Omission on an edit keeps it; an empty string clears it and its
progress state. One-shot follow-ups cannot opt in. Existing unlinked schedules stay unchanged; attach an existing
schedule through an authorized `schedule_routine` edit. This is not permission to create or widen recurring work.

Each dispatched linked run starts with its to-do ID. When its watched turn settles, the hub records `todo_progress`
on its `ScheduleRun` in `assistant.runs`: `baseline`, `progress`, `working`, `no_progress`, `unknown` or `inactive`,
with the to-do ID, consecutive assessable no-progress count and a reason. The reason of `baseline`, `working` and
`no_progress` is the hub's evidence; `progress` and `unknown` quote the run's explanation. Skipped or unstarted
attempts are `not_run`; a newer completed run makes a late older unassessed result `superseded`. A still-running,
unassessed run is `pending`.

The explanation is the run's own reply (#11553): the last reply after the prompt that carries the run's key, up to the
next message that opened a turn. A run in its source conversation shares that conversation with its other work:
supervision messages, the owner's words, and other schedules' runs. There the explanation is never just the latest turn's
reply. Two linked runs due in the same minute in one supervising conversation each keep their own reply, although both
watchers see the conversation settle only after the second answer. When the run's turn is not its own (its prompt was
queued into another turn, or another message was queued into its turn), or its prompt is not on the page, the
explanation is the hub's evidence line ("no new Agent reply, criterion change or progress report"). A run in a
conversation of its own falls back to that conversation's latest turn.

The first run after the link only records the baseline (`baseline`, #11587). Compared with a snapshot taken just
before it was sent, it judged only its own few seconds, and an Agent working without words was counted at once.
Each later run compares with the previous assessment's evidence. Observable progress is
any changed nonempty last Agent answer on a current unreleased, undismissed task Agent, a state change of an
existing acceptance criterion, or changed content/steps in an explicit `report_to_supervisor` `progress` report.
Answers are read from transcript text, not truncated session previews; runtime API errors, the schedule's own
conversation, timestamps, notes, evidence-only criterion edits, display paraphrases and repeated
identical reports do not count. These are observable changes, not a judgment that an artifact is useful or that the
work is accepted. Criterion rewording alone does not count.
Tool calls are work (#11587, `schedule_todo_evidence.go`). The check reads each Agent's latest turn and keeps its distinct
calls (name and input, hashed) and when it read them. A call made after the baseline was read that does not wait,
poll or check CI (the activity classes the `no_progress` signal uses) and repeats no call seen before (in that turn
at the baseline, or earlier since) makes the run `working`. That restarts the streak like progress, and its reason says
how many calls came since when. Waits, polls, repeats and tool results alone do not count.

The first no-progress run sends its supervisor a `check` signal (`report: no_progress`). It names the task Agent's
computer and session, not the conversation the run went to, and reads as the hub's check, never as the Agent's own
report. A failed assessment write sends `check` with `report: error`. The owner is asked once the streak has at least
two runs and the task has gone without progress for the `no_progress` signal's floor: max(30 minutes,
1.5 × `expect_minutes`), from when the streak's baseline was read to the current run's read. The existing actionable
`needs_user` review puts **Change approach / Keep waiting**, the hub's evidence and the cost of waiting in Needs you,
including for `push: asked`. The evidence says since when no reply, report or criterion change came, and whether the
Agents made no tool calls or how many calls only waited or repeated. It never quotes the run's own summary, which
often says the work is going fine.
The option the owner takes on that decision, recommended or not, is recorded in the to-do's decisions. It goes to the
task's supervising conversation as `check` with `report: decided`, the decision as asked and each option's result,
not to the Agent at work. This covers the card, the push's OK, the to-do's accept route, `task_decided` (whose reply
says the choice went to the supervisor) and an exact option the owner names in the Agent's chat (#11747). The Agent
hears only what the supervisor then sends it, and the card says the choice went to the supervisor. An answer already
in the Agent's chat (named there, or `task_decided` with `told_agent`) tells the supervisor the Agent has it rather
than "not told", and its card names the option without saying the Agent did not get it. An answer in the owner's own
words still goes the ordinary way, to the Agent with no `check` signal, and its card says so. Choosing another
approach asks the supervisor for a proposal within existing permissions, not permission to spend, send or widen
scope. The decision's 24-hour deadline only revisits it; the hub never applies the proposed strategy change by
default. Existing notification preferences still apply.
For a worker supervised by a task-leader Agent, the conversation above that leader reviews the decision through
its existing authority and receives the owner's option; the worker stays supervised by its leader.
Another pending owner decision is never overwritten; a failed escalation is retried on a later assessment.
The first escalation attempt's run identity is saved with the assessment before creating its review. If the
review succeeded but the final escalation-marker write failed, later assessments or restart replays recognize
that exact pending review and repair the marker without replacing the decision or claiming another owner question.

Observed progress or work resets the streak. Unreadable evidence is `unknown`: it does not increment or clear the
previous known count, and it preserves the last readable evidence. An initially unreadable baseline must become
readable before it can establish a no-progress run; later recovery can still prove progress against known evidence.
Closed, cancelled, superseded or paused work is inactive, clears the streak and is not reopened or escalated.
The latest evidence, run identity, count and escalation markers live with the schedule in `assistant.tasks`,
survive restart and do not depend on the shared 200-run display history. Replaying a completion does not count
it twice. Writes must succeed before escalation; signal and owner-card delivery reuse their existing mechanisms,
not a separate scheduler outbox.
A failed run-history write retains the assessment already committed with its schedule and retries the history
write without counting the run again. Only an assessment that could not be committed is recorded as `error`.

Deployment: update hub and node together (`resume_only` is new; old node ignores it and creates a new chat under a missing old id).

**A turn schedules its successor** ("Finally use schedule_followup with at=30m and the same prompt"): hub recognizes the task from run history;
same title or prompt doesn't create another. One-shot relative-time (`30m`) renewal becomes a 30-minute `interval`; later identical requests update the prompt without changing time,
and don't re-enable tasks disabled in the App. Copied leading "⏰ … (your scheduled follow-up)" lines are removed to prevent growth.
A task deleted while its run was going is not brought back by that run asking for it again (#4792): it is told so and nothing is
scheduled. The exception is the run's own conversation having deleted it (`cancel_followup` / `delete_routine`, e.g. to change its rule):
asking for it again then makes a new task (#9090). Which conversation deleted a task is kept in memory for 30 minutes; after a hub
restart, a deletion is taken as the user's.
`interval` messages say "It will run again on schedule; no need to arrange the next run", and assistant instructions recommend `every`.

Migration: first startup without `assistant.tasks` moves `assistant.routines` / `assistant.followups` (ids, times, prior statuses unchanged),
then clears old follow-ups so an older hub can't rerun them; thereafter reads only the new table. Failed Postgres reads save nothing (formerly defaults overwrote).
Legacy App `routines` / `followups` APIs still read / write this table (`docs/protocol/protocol.md`).

## Missing final replies (#10427)

When a user message in the main or an ordinary Assistant conversation completes with no written reply, Persona
asks the Assistant once for a reply based on the results it already has. The chat shows a system line for the follow-up.
If the follow-up still has no answer, or delivery fails, a saved chat notice tells the user to try sending the
message again. This recovery is automatic.

The manager reads the settled transcript. A blank or tool-only ending counts as missing, as does a whole reply
that matches the existing channel list: `No response requested`, `no content`, `(no content)`, or `[no content]`,
ignoring capitalization, whitespace around the reply, and trailing sentence punctuation. An ordinary answer,
including a refusal with an explanation, stands on its own. A successfully displayed question or notification
also counts as a user-visible outcome. Stops, existing model errors, queued work, and inputs from supervision,
scheduled tasks or other conversations are excluded. The original transcript stays available.

The input must be tied to a verified app send or to the verified start and first transcript page of a new
conversation. Direct app requests authenticated with the older owner credential qualify for reply recovery.
Secret hand-offs still require their full-device provenance. If that evidence is unavailable for recovery,
for example across a manager restart before the first look,
recovery may be skipped. An intentional request for silence or for exactly
one of the listed strings can cause one extra model turn. Other vague or unhelpful replies are outside the check.
The follow-up asks for text only and tells the model to avoid tools and repeated work. Normal tool permissions
still apply. The send is tied to the original turn and the node checks that the conversation is still idle at
the same transcript tail before delivering it.

## Background rounds

Many main-chat rounds are not started by the user: another Persona conversation hands work over with `conversation_send` /
`conversation_new` (a supervisor message), a scheduled task or follow-up runs, and the hub's supervision wakes the chat.
They used to fill the main chat and push the user's own exchanges out of sight. Since #4314 the apps fold the first two
the way quiet supervision rounds already were (#1988, `docs/assistant/unified-entry.md` "Supervision"). #6595 applies the
same folding to the leader conversations (`AssistantInfo.leaders`, "Leader conversations" in
`docs/assistant/unified-entry.md`): a leader's patrol, supervision and relayed (`conversation`) rounds use the same
folding and visibility rule as the main chat. The owner's messages and the answers to them stay open.
It is the same `assistant.main_background_folded` switch (on unless turned off); an ordinary side conversation is
unchanged. The hub's mid-turn interject note (`prompts/assistant/interject.md`,
`<persona-context kind="context" title="插话">`) is model instructions and never renders as a quote in the owner's
bubble on any client. It stays in the model's input.

A known runtime placeholder at the end of a verified background input is projected as folded work on transcript
pages and live item updates. The transcript keeps the original words and the item ID. A normal background
completion with only a placeholder or no written reply adds no finished push. Substantive replies and
explicit user-facing actions still follow the usual visibility rules.

- **Which rounds**: in the main conversation, a round whose first input has the message source (`Item.source.kind`, else
  the `persona-context kind="message-source"` metadata or the hub's `injected` mark, read by each client's `sourceOf`)
  `conversation`, `followup` or `patrol`. Web also folds `notice` rounds such as delivery PR follow-ups under the same
  setting. A `followup` counts only as a scheduled follow-up (the hub gives those the
  task's title): Persona continuing the user's own work after a usage limit reset (`injected.state: limit_continue`) or on a backup launcher (`limit_fallback`),
  waking the chat for what it waits on (`wake`) or an untitled follow-up is not a background round, so the answer to
  the user's question never folds away. Like a supervision round it lasts until the next genuine input; a queued user
  message never joins it, and recaps or compactions inside it stay inside. Other conversations and Agent sessions are not
  affected: a conversation another one opened shows its first message as before.
- **Folded**: one muted line, `@<source> · Background round` (`· Running in the background` while it runs), that the
  user can open and close at any time to see the full transcript, history included (super rule 2.4). A scheduled task's
  prompt is never shown in the main chat while folding is on: its source block says only which task ran, closed or open.
  A supervisor message shows its text when opened.
- **Completed silent rounds**: in the main conversation, a completed round with a successful silent finish, no live
  work and no promotion leaves no timeline row and contributes no unread count or badge. Its inline tool calls and
  its `task`, `routed` and `answered` feed cards inside the same half-open round interval stay out too; plans,
  message cards, notices and other decisions always remain visible. **Background activity**, opened from either the
  conversation menu or Inbox, retains each such round with its input, replies, tool activity, feed cards and finish
  receipt, using the same paged history and full-record renderer. Opening it marks nothing seen. Full transcript and
  feed APIs are unchanged. The hub suppresses unread only for the matching latest silent round: earlier genuinely
  visible unread work remains unread, working and needs-you states win, and a failed transcript read fails open.
  An accepted review is internal only when the hub resolves the task's current plan with `user_requested=false` and
  finds at least one source: a current-round completed (`clean` or `blocking`) `auto_review`, `round>0` after rework,
  or a nonempty `parent` for a leader's internal subtask. Its successful `task_review` receipt carries
  `background=true`; the receipt, tool call and reply stay in Background activity. `user_check`, `needs_user`, an
  unmarked legacy review, or missing/ambiguous source metadata stays visible. The attention-cache version changes
  with this classification so an older visible verdict is recomputed.
  Existing daily-briefing totals keep their meaning; a separate exact silently-handled daily total is out of scope
  because round metrics do not record visibility.
- **Not folded** (the visibility rule supervision rounds use, `supervisionVisible` / `fold.supervision_user` from
  `server/internal/transcript/fold_supervision.go`): a round that used `ask_user` or `notify_user` successfully, a pending
  `ask_user` while it runs, a `task_review` with an unmarked `accepted` or any `needs_user` verdict card, or a `supervision_finish`
  with user visibility. A completed round with a nonblank final reply also stays open, even if delivery tools were
  unavailable, unless it settled with silent visibility. The full reply uses the ordinary reply bubble so its
  formatting and links remain available. Its work keeps the usual collapsed summary. Intermediate narration,
  generated quiet-heartbeat acknowledgements and empty turns stay folded. A successful silent finish keeps its round
  folded unless an explicit user-visible action promoted it. The owner can also enable **Fold routine relay replies**
  in Assistant settings. With both folding switches on, a routine `conversation_send` reply in the main chat stays
  inside its expandable round and skips the ordinary finished notification. A direct answer or an explicitly promoted
  report still gets its ordinary treatment even if another conversation has already sent its next input. The manager
  checks the completed turn for Claude, Codex and runtime chats. Errors, interrupted turns and work waiting on the
  owner keep their alerts. Leaders and scheduled rounds keep their usual reply display. Delivery and hop/answer
  processing happen for every relay.
- **Needs you** keeps its entry and its count. While folding is on and its rows come from more than one supervising
  conversation (a task's `origin`), they are grouped under that conversation's title, this conversation's own first;
  the rows and their order within a group are unchanged.
- **Switch**: Settings › Assistant › **Fold background rounds** (`assistant.main_background_folded`, `PUT /api/v1/assistant`,
  on unless turned off; Persona's settings tool changes it only after the user confirms the card). Off, the main chat and
  Needs you show the expanded view. A hub that omits this field uses the default of on. **Fold routine relay replies**
  (`assistant.fold_routine_relay_replies`) is saved independently and defaults to off, including on older hubs.
  A user who previously enabled background folding sees ordinary relay replies until they explicitly enable the new switch.

## Messages from the system wait for the owner (#4925)

The owner comes first in every assistant conversation, the main chat and leader conversations alike
(`docs/design/proposals/main-chat-availability.md` 3b rules 1 and 3; `server/internal/assistant/relay_presence.go`).
What the system puts into a conversation (supervision messages, task notices, late `ask_user` answers, messages from
other conversations) goes through one per-conversation queue (`assistant.relay`, "Managing its own conversations"):

- **Owner present, system messages wait.** The owner is present in a conversation for 2 minutes after their last message
  to it (`ownerPresentFor`); the time is taken from `session.user_spoke` and stored as `assistant.relay`'s `spoke`. While
  they are present nothing from the system is delivered there, whether the conversation is idle or not: a message that
  would have gone straight into an idle conversation is queued instead.
- **Nothing hangs.** A message that waits on a decision is held for a present owner at most 5 minutes from when it queued
  (`relayDecisionMax`, the time a card stays on one rung, `askHoldFor`), however long the owner keeps talking. These are a
  late `ask_user` answer, a supervision message carrying a signal somebody waits on (the queued message's `urgent`,
  `waitsOnDecision`: the signals supervision sends in its 2-minute gap, `docs/assistant/unified-entry.md` "Supervision",
  except `started` and a `report` that is neither a question nor blocked, which are told soon but wait on nobody), and a
  task notice of state `needs`, `interrupted` or `failed`.
- **What ends the hold.** A timer per conversation drains its queue when the first hold ends. The supervision tick (30
  seconds) drains any conversation whose hold ended without a timer: after a manager restart (the queue and `spoke` are
  in settings, the timers are not), or when the conversation list could not be read at that moment. A conversation that is
  on a turn then gets its queue when the turn ends, as before.
- **The queue is ranked.** The next message to go is the best rank among those not held, the one that queued first
  within a rank: (1) late `ask_user` answers, (2) messages that wait on a decision, (3) outcomes to report to the owner
  (a task notice of state `done`; a concern's outcome reaches the conversation this way, since a concern has no message
  of its own), (4) in a direction's lead conversation, the owner's words handed to it (a hand-over,
  `docs/assistant/unified-entry.md` "Across a relay"; in any other conversation one ranks with everything else),
  (5) everything else. One message goes per turn, as before. A supervision message or task notice that
  replaces the one still queued for the same conversation or task keeps the time the first one queued (`queued_at`,
  the time the queue wait of "Availability metrics" below counts from), so merging never restarts its wait; one that
  only then starts to wait on a decision counts its 5 minutes from then (`decision_at`), its queue wait still from when
  the first queued. A message that
  could not go in (its agent's computer unreachable for a `needs` notice's recheck, a delivery that failed) is passed
  over at the next drain while another may go, so it holds up nothing but itself.
- **Scheduled runs wait too.** A follow-up or scheduled task that runs in a conversation the owner is present in (the
  one it came from, else its own) is not started: it stays due, nothing is recorded or skipped, and the scheduler looks
  again when the hold ends (`runHold`; nothing is stored, so a restart only looks again). It waits at most 5 minutes past
  its time (`relayDecisionMax` again: a run has a time the owner set). Not held: "Run now", a run that starts a new
  conversation, and the first run of a daily or cron task (its time is only looked back for 2 minutes, so waiting would
  lose it).
- **The owner's own answer is not held.** The answer the owner gives on a card asked with `wait: false`
  ("Asking without holding the turn") is the owner speaking, not the system: the queued message carries `owner`, no
  hold applies to it, and with its first rank it is the next message into the conversation (after a running turn, as
  ever). Typically the owner wrote, the turn raised the card and ended, and they tap it within the 2 minutes; the
  conversation takes the answer up at once. That such a card was closed or ran out is the system's to tell and waits
  as any late answer does, as does the owner's later decision on a question's to-do.
- **Compatibility.** `urgent`, `decision_at`, `owner`, `handover`, `answers` and `spoke` are optional fields; the queue's
  shape is otherwise unchanged. A previous version reads the queue as before and drops the fields on its next save, after
  which those messages rank and wait as ordinary ones and no hold applies; a hand-over then stays `queued` until it
  expires, and the one a reply answered stays `delivered`. A message queued before `queued_at` existed counts its 5 minutes from
  its own time (`at`).
- **The owner's whole round comes first (#6394).** On top of the two-minute presence window stands the owner-turn
  gate (`relayBook.ownerTurns`, in memory only). When the owner sends a message to a conversation, the gate arms for
  it and every queued message waits for the whole of the round that starts for them, whatever its rank — including a
  late `ask_user` answer that the five-minute decision cap would otherwise let through. The owner's own message never
  enters this queue, and `interject_background` still moves a running background command out of its way
  ("Interjecting while it answers"). A session list showing the conversation working confirms the round; one showing
  it stopped (`idle`, `done` or `interrupted`) releases the gate, and the queue drains by rank again. Two safety
  limits keep a gate from sticking when the lists say nothing: `ownerTurnStartWait` (one minute) when the round is
  never seen working (a refused send, a conversation the list dropped), and `ownerTurnMax` (30 minutes) as a hard cap
  for a round seen working but never seen stopped; the owner speaking again refreshes both. A restart loses the
  in-memory gates, after which the persisted presence window alone holds as before. The owner's cards stay
  unblocked: a gate holds the queue, never the owner's own answers (`relayMsg.Owner`).
- **Not covered**: a meeting's feed (`meeting_feed.go`) is the live words of a meeting the owner had the conversation
  attend, so it is not held. Interrupting a round already running is F2 of the proposal.

## Replies where it wrote as the user (#10777)

A conversation of the assistant (the main chat, a leader or a side conversation) that wrote into a Lark chat in the
user's name used to hear that the other side answered only when a follow-up or a patrol happened to look again: the
answer reached Persona through the Inbox alone, and none of the Inbox's routes lead back to the conversation that wrote
(`server/internal/assistant/chat_reply.go`).

- **What opens it.** The send itself, as the sent-as-user ledger records it (`docs/extensions/connectors.md` "Sent as
  the user"): a message the node's gate let into a chat (`POST /conversations/<chat>/messages`, or `images`, `files`,
  `attachments`), or a connector's send whose target names the chat, with the machine and conversation that asked. A
  recalled message counts as not sent. A reply to one message (`/messages/<id>/replies`) is recorded without the chat
  and opens nothing; neither does a reaction, an edit or a read receipt.
- **What tells it.** The Lark connector's (`lark`) batches of other people's new messages in a chat
  (`docs/assistant/inbox.md` "Event grouping"), read in `collectSource` before they are related to events; a bot's chat
  is left out. Each conversation of this manager's machine that wrote a message into the batch's chat in the last 24
  hours (`chatReplyWindow`), before the batch's newest message, gets the messages it was not told yet. Not told: a
  conversation of another machine, an archived or deleted one, the Inbox's own conversations (the AI twin hears of the
  chat through its item) and the background supervisor.
- **How.** Through the conversation's relay queue, as `chat_reply` ("Messages from the system wait for the owner"): it
  waits for a running turn and for the owner, ranks with everything else, and is no hop of any chain. The text
  (`server/internal/prompts/assistant/chat_reply.md`, in the owner's language) names the chat, when the conversation last
  wrote there, how many new messages there are and, in a group, who wrote the newest, and puts the messages in a
  `<persona-context kind="quoted-message">` block as other people's words: not the owner's instructions, nor their
  consent to anything. To write there again the conversation goes through the gate and its confirmation as before. The
  queue above the composer shows it as the chat's new messages, without their words.
- **Once, and spaced.** Each message reaches a conversation once, by the connector's message ids: the connector hands a
  batch again until it is in an event, and a batch can grow meanwhile, so only its new messages count, and the text says
  when it repeats some told before. One conversation hears of one chat at most once every 10 minutes (`chatReplyGap`): a
  batch within that waits in the queue until the 10 minutes are over (the message's `due`), and the batches after it
  merge into the one waiting (the count added up, the texts put together, the newest 3000 characters kept). What was
  told is remembered in memory: after a manager restart, a batch not yet in an event may be told once more; one still
  queued is not, since the queued message keeps its message ids (`chat` in `assistant.relay`).
- **Unchanged.** The Inbox's own handling of the batch (triage, the AI twin, `inbox_signals`), the connector's
  hand-over timing, the gate and its cards. The window and the spacing are fixed. One setting, `assistant.chat_reply_relay` (`persona_settings`, owner's phone approval; **off by default**), turns the whole thing on. While it is off nothing is queued or told; turning it off drops the `chat_reply` messages still queued (one going in right then is dropped by the drain). A second setting, `assistant.chat_reply_relay_muted_chats` (`persona_settings`, `get` / `add` / `remove`, owner's phone approval), stops forwarding for individual Lark chats by chat ID even while `assistant.chat_reply_relay` is on, dropping queued `chat_reply` messages for that chat and listing recent active chats alongside muted ones in `get`. In addition, when a batch arrives for a chat, Persona checks recent messages via `messages_around` and reconciles them against `sent_as_user`: if the owner personally spoke in that chat after the assistant's send, forwarding stops automatically for that chat (dropping queued `chat_reply` messages for sends at or before the owner's reply, persisted in `assistant.chat_reply_takeover` for the 24-hour window, and reopened naturally only if the assistant sends a newer message there later; if `messages_around` is unavailable, forwarding fails open rather than guessing). To make the feature findable, the first conversation that wrote into a chat as the user and has others' replies there gets one hint, once ever (`chat_reply_hinted`, no one's words, no push of its own): it tells the owner in one sentence that replies are not passed on and that they can ask to turn it on.
- **Not covered.** Task Agents that write as the user (#9763 wakes a task's Agent on follow-ups in a topic bound to the
  task; they are left out here so that one answer does not wake the same Agent twice), a single topic within a chat,
  replies to one message, and the lark-cli source, whose ledger rows name a different chat id.
- **Compatibility.** `chat` is an optional field of a queued message: a previous version drops it on its next save, after
  which the message goes in as it is and a later batch queues a message of its own.

## Messages that could not go in (#10194)

A logical (segmented) conversation's own delivery queue uses the same row field and bubbles: each of its inputs that
has not gone in is listed with why (`reason`), an attempt whose receipt stays unknown expires after a bounded deadline
instead of fencing the queue, and its owner picks Retry or Delete (#17431, docs/operations/logical-input-reconciliation.md
"Delivery deadline").

The owner's own message must never vanish. When the owner writes to one of the assistant's conversations while it
cannot take the message (its process did not start, as a cold start killed on a loaded computer, #10192; its computer is
away), nothing was typed. The hub used to answer the app with the failure and forget the message; a replay of the app's
operation then read as accepted and the bubble waited for the transcript and went.

- **Kept.** The hub hands such a message (a 5xx with nothing delivered, or a transport outcome whose delivery is
  unknown, with a `client_msg_id`) to the conversation's manager, `assistant.keep_undelivered`, with the app's own fields
  (`text`, `images`, `launcher`, `model`, `effort`), never what `assistant.send_params` added. An unknown first attempt
  is marked `delivery_unknown` and carries `tried_at`, the original send's start: the manager only looks for it, never
  sends it again by itself (below). The manager keeps it in its Postgres (setting `assistant.undelivered`,
  `server/internal/assistant/undelivered.go`). The hub answers 202 `{"delivered":"later"}`, records the send as `later`
  (a retry of the id is answered the same and sends nothing) and tells the chat's subscribers `kept`. A manager that
  cannot keep it leaves the failure as before.
- **Busy while its role changes.** A conversation at work (a turn, or background tasks a new process would kill) whose
  task-follow-up role changed refuses the message with 409 `busy` (`assistantwire.CodeConversationBusy`): taking it
  would need a new process with the new role, and the old process must not get a message meant for the new role.
  Nothing was typed, so the hub keeps it like a 5xx instead of answering the owner "try again later" on every send.
  It shows "Waits for this conversation's current work to finish" (`wait_idle`): a session list that still shows the
  conversation working does not wake it, one that shows it settled sends it at once, and that send resolves the role
  again, so it starts the process on the new role. Other 409 refusals are answered as before.
- **Shown everywhere.** The row's `undelivered` (`proto.Session.Undelivered`) lists them; web, iOS and Android draw each as the
  owner's bubble marked "Not delivered", with why and "It will be sent automatically", and Delete; one that gave up
  in red with Retry and Delete. Every device sees it, not only the one that sent it (`web/src/lib/undelivered.ts`;
  Android `android/feature/chat/src/main/kotlin/persona/feature/chat/ChatUndeliveredBubble.kt`). On the web the line
  under the bubble, Retry and Delete are a chunk of their own (`web/src/views/chat/UndeliveredBubbles.tsx`, strings in
  `chat-undeliveredbubbles`), off the first screen; until it is in, the message shows as the owner's failed bubble alone,
  so it never blinks out (#10357).
- **Sent by itself, once.** The manager tries again with backoff (15 s, 30 s, 1 min, 2 min, then every 5 min), at once
  when the conversation is seen running again on a session list, oldest first, one pass per conversation at a time.
  A try runs `SendParams` again (the live snapshot and the pick of now) and the node's `session.send` under the
  message's own `client_msg_id`: its transcript line is tied to the id, so the bubble turns into the real line. Before
  each try the manager saves the try's start (`tried_at`); when that save fails, the try is not made and waits for the
  next backoff step, so a manager stopped during a try always finds the mark. A try refused before anything was typed
  (the process did not start, the computer is away) clears the mark and is tried again by itself.
- **A try in doubt is never sent again by itself (#10669).** A try that may have gone in (no answer in time, the link
  dropped, the manager stopped or restarted during it, or the hub's own first try unknown) cannot be settled by
  sending again: the node remembers ids only in memory for ten minutes (`server/internal/node/sendsafe.go`), and the
  transcript can be late or unreadable. Such a message is only looked for in the conversation's latest turns, by id,
  or by its words written since the try where the node forgot the tie (it restarted), at 15 s, 45 s, 1 min 45 s and
  3 min after the doubt began (`undeliveredLookFor`: long enough for a line a busy computer writes late or a read
  that fails once, short enough that the owner hears within minutes). Found, it went in and leaves the list. Not
  found by then (the transcript could not be read, a message of pictures alone has no words to know it by once the id
  is forgotten, or it is not there), it is marked failed with "It may already have gone in. Check the conversation
  before you retry", and the owner decides.
- **In order.** While a conversation has kept messages, `assistant.send_params` answers `undelivered_ahead` for a newer
  owner message, and the hub keeps it behind them instead of letting it overtake.
- **Gives up after a day.** A message not delivered within 24 hours is marked failed; the owner picks Retry (a new day
  of tries) or Delete. A conversation that was deleted fails it at once. Retry sends the message even after a try in
  doubt (the owner looked and said so; a line found by a last look still counts as delivered), and a Retry that cannot
  be saved answers an error and changes nothing. Until the message goes in, each try of the Retry first looks for the
  earlier try, also when the computer was still away at the Retry and comes back later (a try that typed nothing keeps
  the Retry and the look), and the look always goes from the first try in doubt: `tried_at` is never moved later, so a
  line that try wrote before the node forgot its id is still found by its words. If the Retry's own try ends in doubt,
  it is again only looked for and then left to the owner.
- **Device signed out** (#10708). The hub gives the manager the sending device's credential ID when it keeps a
  message. Signing out that device fails its kept messages. Their rows say why and offer Delete. Messages kept
  before the hub recorded the sending credential have no device to match.
- **Messages that need no reply** (#11614, #12867, `server/internal/assistant/relay_later.go`). `conversation_send` with
  `wake: false` is for acknowledgements and information the receiver does not have to act on. The message is kept
  for the receiver (`relayState.Later`, kept across a restart, at most 20 per conversation and 7 days) and goes in,
  as a context block in front (`prompts/assistant/relay_later.md`), with the next message delivered to that
  conversation for anything else: a supervision message, another conversation's message, a scheduled follow-up,
  a notice, or an owner message sent from an app or web client (`assistant.send_params` and `session.assistant_sent`,
  or kept undelivered retries). It starts no turn of its own, costs no hop and no place in the relay rate, and is
  dropped (publishing the refreshed queue state immediately) only once the message carrying it went in. Before a manager
  send with a `once_id`, its quiet-message context (including an empty one) is saved in `relayState.LaterSends` for
  seven days and reused on retries, including after restart. For an owner send with a `client_msg_id` (unnamed sends
  without a `client_msg_id` leave `Later` untouched so every carry has a durable settlement key), a reservation is saved
  only when quiet messages are actually waiting to be carried (empty queues do zero relay writes), is deleted in the
  same save once delivery is confirmed (or when a kept undelivered message is deleted), and expires after 24 hours
  (`ownerLaterKeep`, matching `undeliveredGiveUp`) if never settled; reusing a `client_msg_id` for a non-carrying send
  (such as a slash command or approval reply) clears any unkept stale reservation without dropping `Later`, and an owner
  Retry of an earlier in-doubt kept send only reuses an existing reservation without picking up later arrivals. If the
  relay state cannot be read on an owner send, the send degrades and delivers the owner's own message without quiet
  context (leaving `Later` untouched even if storage recovers before settlement), while saving a carry reservation for
  waiting quiet messages stays fail-closed. Nothing is guessed from
  the words: only the explicit argument decides. A `quote` (the owner's request) always wakes, so the two together are
  refused. While kept, a user-facing message shows above the composer (`queuedRelays`) and disappears once consumed.
  Internal-only relays (the card ledger and non-owner card routing notes) stay in the delivery state but are not listed.
- **Per-sender wake cap and deferred wakes** (#13888, #14352, `server/internal/assistant/conversation_fanout.go`,
  `server/internal/assistant/delayed_waits.go`). To keep one broadcast from waking every leader at once, a sender may
  immediately wake at most `fanoutWide - 1` (2) distinct conversations within `fanoutWindow` (10 min); a conversation
  already woken in the window and a `quote` of the owner's request still wake immediately. Wake-intended messages past
  the cap are kept in `relayState.Later` (`DeferredWake: true`) and get a durable deferred wake in
  `assistant.delayed_waits` (`conversation_fanout_wake`) at the back of the sender's wake queue. The tool answer states
  plainly that the message was kept and will wake the receiver automatically in about N minutes. When its window slot
  opens, the deferred wake counts against the sender's cap, promotes the kept message(s) atomically from `Later` to the
  back of `relayState.Queue`, and drains it — keeping broadcasts paced over time even across hub restarts (recent wake
  timestamps are also persisted in `relayState.Fanout`). If the receiver wakes for any other reason first and reads the
  kept messages, the deferred wake is dropped so it never double-wakes. Messages sent with explicit `wake: false` keep
  `DeferredWake: false` and are never woken later.
- **Between conversations.** `conversation_send` to a conversation that cannot take the message now no longer fails:
  the message goes into its relay queue and is tried again under the same `once_id`. A conversation that failed to
  take a relayed message is tried again after `relayRetryGap` (30 s), not on every session list.

  What a failed relay send means is decided once, by `relayFailureOf` (`server/internal/assistant/relay_refusal.go`,
  #11393), for every path that relays into an assistant conversation: `conversation_send`, the queue's drain,
  supervision messages, `relayOrQueue` (task ends, spawn notices, chat replies), the owner's card answers, approval
  results and quiet tasks' notices.

  | Outcome | Errors | `conversation_send` | Every other path |
  | --- | --- | --- | --- |
  | In doubt | the time ran out, or the link dropped while it went (`relayInDoubt`) | queued | looked for in the conversation, then tried again under the same `once_id` (#10690) |
  | Not now | a node error with no code, `internal`, `machine_offline` (also in the shapes the bus and the manager's own computers give it), `node_closed` | queued | stays queued |
  | Not now, by a code | `busy`, `adapter_busy`, `compacting`, `runtime_away`, `assistant_control_failed`, `adapter_timeout`, `adapter_unavailable`, `adapter_revision_mismatch`, `handover`, `node_restarting`, `deadline` | returned to the sender until decision 1 of #11393 is answered | stays queued |
  | The owner fixes the setup | `claude_missing`, `launcher_missing`, `no_device`, `node_outdated` (the manager's devices layer answers it before the node is reached), the conversation's model pick that cannot run there (`launcher_out_of_scope`, `launcher_own_service`, `launcher_other_kind`, `assistant_launcher_profile_switch`), and every other refusal of an external launcher's connection (`adapter_disabled`, `adapter_credentials_missing`, `adapter_config_conflict` and the rest of `relayAdapterSetupCodes`) | returned | stays queued and goes in once the setup is fixed |
  | Gone | the conversation "is gone" | returned | dropped |
  | The manager's own | an error the manager raises without a code (a pick, model or connection that is gone or cannot be read) | returned | stays queued |
  | Refused for good | any other code, `assistant_supervision_runtime` included | returned | leaves delivery once, by its kind (below) |

  The "not now" and setup codes were read from the refusals the node and the manager's devices layer give on
  `assistant.send`, not observed: a code missing from both lists reads as refused for good. A message refused for good
  is handled in one place, `relayRefused`, by its kind, and is reported once:

  | Message | Then |
  | --- | --- |
  | Supervision message | One try, then the owner's push ("Message could not be delivered"; with `assistant_supervision_runtime` it asks for a Claude Code or Codex supervisor, otherwise it says the follow-up was refused). It is not moved to another conversation, and its task stays open for review. Whether it should move to the main conversation's supervision route is decision 2 of #11393. |
  | The owner's answer to a card | It moves to the main conversation, as it does when the conversation that asked is gone (`askTarget`), and a turn the main conversation holds with its own question lets go for it, as for any message queued there. One the main conversation refuses as well stays queued there. |
  | Approval result | It stays queued for the conversation the approval came from: approval results have no fallback. One refused with `assistant_supervision_runtime` ends with the owner's push instead, as it did before. |
  | Persona's notices: a task's notice or end, a spawned conversation's notice, a chat's reply, the line to a sender | They leave the queue. A quiet task's "needs you" notice sends the push of the card it stood for (`relayMsg.Card`), a "failed" one the failed start's push. Every other one sends the owner one push saying the conversation refused an update (with `assistant_supervision_runtime`, the push that code always had). A chat's reply refused for good counts as gone for the gap between the chat's batches, so later batches wait and merge into one, and its push is sent once per conversation until a chat reply goes into it again or the day-long reply window ends. |
  | A message from another conversation (`conversation_send`, `conversation_new`) | It leaves the queue. The sending conversation gets one line saying the message was not delivered and will not be retried (`prompts/assistant/relay_refused.md`). A hand-over of the owner's words it was becomes `not_delivered` with the refusal's code (`docs/assistant/unified-entry.md`, "Across a relay"); the line is still the only thing the sender is told. |

  Each refusal for good is logged once; no work event records it yet (#11399), which is why Persona's own notices
  end with a push. A queued hand-over dropped because its receiver is gone becomes `not_delivered` with `gone`.

### Messages to an Agent whose computer is away (#10661)

A laptop connected through Persona Link is often closed. A message to one of its Agents (Claude Code, Codex…; not a
Persona chat, which keeps its own as above) used to fail with "your computer is offline" and nothing was kept. Now the
manager that connects the computer holds it and sends it once the computer is back
(`server/internal/devices/held.go`):

- **Held only when nothing was written.** The hub marks the owner's message from a full device (a person's, not an
  extension's, a pairing code's or the hub token's) whose app declares `agent_message_hold` (client_hello or
  `X-Persona-Features`: the web today; an app that does not draw held messages keeps today's failure) to an Agent it
  lists, with a `client_msg_id` and none of a to-do, a fresh to-do start's first message or a launcher switch, as one
  that may be held (`NodeCall.hold`). The devices manager holds it when the call provably wrote nothing to the node
  (not connected, closed or suspended before the write), or when older messages of the same Agent are held still
  waiting, being sent or with an unclear outcome (it never overtakes them). It keeps the call exactly as the hub sent it, who sent it, the
  submission and the share authority with it, in its Postgres (table `held_operations`, kind `message`), and answers
  `held`; the hub answers the app 202 `{"delivered":"later","undelivered":{…}}`, as for a Persona chat, and records the
  send as `later` (a retry of the id is answered the same and sends nothing). An older manager ignores the mark, and a
  manager that cannot store it gives the failure as before.
- **Never overtaken by an app that cannot show them.** A person's message from an app that does not declare the feature
  (iOS, Android and HarmonyOS today) is never held, but carries `NodeCall.ordered`: while older messages of the same
  Agent are held (waiting, being sent, unclear, or being changed) the manager refuses it without typing anything
  (409 `held_ahead`, "Earlier messages to this Agent are still waiting to be sent. Send this one after them."), so it
  never goes ahead of them; otherwise it goes as before.
- **Shown everywhere.** The row's `undelivered` lists held messages with `computer` (the computer's name) and the whole
  line to show in `error`: "Will send when <computer> is back", "Sending…", "Waits behind the message above", or why it
  was not sent, with Delete, and Retry once it failed. Every client that draws `undelivered` shows them. The web and iOS send
  Retry and Delete to `…/sessions/{mid}/{sid}/held/{msg}`, and older apps' calls to the assistant's routes reach them
  too. Only a person's own full device may (the hub's `X-Persona-Person` on the relayed request): a Retry sends the
  message with its sender's provenance again. An Agent with held messages cannot be deleted until they are (409). On the web and iOS, the message box of an Agent whose computer is offline stays open on a supported hub and says "<computer> is offline. Sends when it's back."
- **Sent when the computer and the Agent are back, once.** A held message goes while its computer's node is connected
  and its current session list has the Agent (a runtime Agent comes back a moment after the node; a node connected for
  two minutes without it gets the message anyway, and its answer decides). Each Agent's messages go oldest first, under
  their own `client_msg_id`, one at a time; Agents are independent. A message is marked as being sent in Postgres
  before the call, and not sent when that fails; a call that proves nothing was written (the node dropped again before
  it) leaves it waiting.
- **Never sent twice by itself.** A try whose outcome is unknown (the connection dropped during it, no answer in time,
  the node said it may have typed it, the manager stopped during it) is not tried again: it fails with "Might not have
  arrived. Retry checks the chat first", and the Agent's later held messages wait behind it until the owner picks Retry
  or Delete. Once per connection that lists the Agent, the manager checks whether the node saw a line tied to the
  message's `client_msg_id`. A match removes the message and lets the next ones go. Retry keeps the message waiting
  without expiring until the check can run (the Agent is listed again, or the node has been connected for two minutes).
  If the Agent is idle and the node can confirm from the transcript that the message remained unseen through a turn
  that ended after the send, the same Retry sends the message after a second check (#10790). With an unknown result
  or an older node, a matching ID in the latest turns still proves
  arrival. Otherwise the owner sees "Couldn't confirm it arrived. Retry to send it anyway, or delete it", with the
  later messages still waiting. On iOS, once the manager acknowledges Retry, the same instruction keeps Retry disabled
  across reconnects and app launches. Delete stays available. A changed instruction on that operation lets the owner
  choose again. That next Retry sends as before. Three failed reads in a row also leave the message
  unconfirmed after Retry. During a check, another Retry returns a conflict and Delete is allowed. A node that refused it
  (nothing typed), or a message that could not be made ready to send before the node was asked, fails with the reason
  and the next one goes; once the node took a message it counts as delivered, whatever came after. A message the owner
  is retrying or deleting holds the later ones back meanwhile.
- **Store and memory kept in step.** A write to Postgres that failed, or a claim whose answer was lost, leaves the
  row as decided in memory and holds the Agent's later messages back; the manager writes it again on its next look,
  never sending it as part of that, and the owner's Retry or Delete works once no try of it can still be running. A hold
  Postgres may have kept though neither its answer nor a look-up after it said so (the app was told it failed) is
  dropped on that next look while it waits untried, unless the app sent the same message again meanwhile.
- **At most 50 per Agent.** A message that would be the 51st held for one Agent is refused ("Too many messages are
  waiting for this Agent…", `held_full`, nothing written); the owner's failed ones are never dropped to make room. The
  manager keeps in memory only what the rows show; a message's pictures are read from Postgres when it goes.
- **Gives up after a day.** A message still waiting for its computer 24 hours after it was held fails with "Not sent:
  <computer> was away for 24 hours"; Retry starts a new day, Delete drops it. Delete is refused while that message is
  being sent. One held behind a message that may have arrived waits for the owner instead: it does not expire, and it
  has nearly a day once the owner decides.
- **Known gap** (#10702): a runtime Agent's row leaves the list when its node shuts down cleanly, or after the manager
  restarts while its computer is away; its held messages then have no row to show on until it is listed again. They
  are still sent.
- **Device signed out** (#10708). Signing out the sending device fails its held messages. The rows say why and
  offer Delete. The manager checks with the hub before delivering a held message, including after reconnecting.
- **Limits**: Held messages stay with the manager that
  held them: moving the computer to another manager (or one
  computer absorbed into another) while some are held leaves them unsent there. A message the computer refused does
  not hold back the later ones, so a Retry of it arrives after them. Two messages to one Agent sent at the same moment
  as the computer comes back may go out in either order. While the manager cannot read its database it holds nothing
  and lets every message go as before, so one may overtake messages held earlier. Messages that are not a person's
  (the assistant's, an extension's, the hub token's) carry no `ordered` mark and may go ahead of a person's held
  ones. A manager rolled back to a version without this keeps the rows it was told to hold but neither shows nor
  sends them; the apps were told they go later.

### Starting a new Agent on an offline computer (#10674)

Starting a new Agent (`POST /api/v1/sessions`, without `resume_session_id`) on a paired computer that is offline
follows the same durable-hold model in `held_operations` (`kind = "start"`, `session_id = ""`,
`server/internal/devices/held_starts.go`, `server/internal/hub/held_starts.go`, `server/internal/node/start_op.go`):

- **Held after validation.** When a person's full device declaring `agent_start_hold` (`client_hello` or
  `X-Persona-Features`) sends `POST /api/v1/sessions` with a UUID `X-Persona-Op-Id` targeting a trusted, paired
  computer that is currently offline, the hub runs static, auth, image-limit, work-session, and trust checks first,
  then asks the computer's manager to hold the start (`devices.hold_start`). The manager persists the original start
  payload in Postgres and the hub answers **202** `{"delivered":"later","held_start":{…}}` (saved in `client_ops` so
  HTTP retries replay the same 202 idempotently). Clients that do not declare `agent_start_hold`, resumes, non-person
  tokens, or requests without `X-Persona-Op-Id` keep the existing offline failure. At most 50 held starts per computer
  are allowed (`409 held_start_full`).
- **List, Retry, Cancel, and live updates.** Full person devices list a computer's held starts with
  `GET /api/v1/machines/{mid}/held-starts`, retry a failed one with `POST /api/v1/machines/{mid}/held-starts/{op}/retry`,
  and cancel/delete a waiting or failed one with `DELETE /api/v1/machines/{mid}/held-starts/{op}` (409 while actively
  starting). Transitions (`waiting`, `starting`, `failed`, `started` with `session_id`, `deleted`) push `held_start`
  WebSocket events only to full devices declaring `agent_start_hold`.
- **Replayed once through the real start path.** Once the computer reconnects (configured and listed, with the bus to
  the hub up), the manager replays each waiting held start once via `devices.replay_held_start` on the hub, running
  `startSessionWith` fresh (`todoHandoff`, `agentStartParams`, `WorkSession` checks, `NodeInfo.EngineHosts`, start
  options/speed/goal, `TodoLink`, `h.fresh`, `autoTabName`). Revalidation refusal moves the row to `failed`
  (`refused`) with the owner-readable reason; a held start waiting 24 hours expires (`expired`); revoking the sending
  device credential marks it `failed` (`signed_out`, `delete_only: true`). Independent held starts on the same
  computer do not block each other.
- **Node-side `op_id` recording and reconciliation.** Every new-Agent `session.start` / `session.start.work` carries
  `op_id`. Nodes announcing `start_op_id_v1` deduplicate concurrent/repeat `op_id` calls, persist `start_op_id` on the
  session's state (`state.put` / `state.load`), and answer `start.status {op_id}` (`started` with `session_id`,
  `pending`, or `unknown`). If a start disconnects mid-flight or the manager restarts while it is `sending`, the row
  becomes `failed` (`unclear`) and is never blindly auto-retried: reconnect reconciliation checks `start.status` first,
  completing and linking cleanly with zero duplicates if the session already started, or leaving the decision to the
  owner (Retry checks once more → `unconfirmed` if still unknown/unsupported, then a second Retry starts it).
- **Not yet held**: starting a new Agent on an offline computer (#10674) and dispatching a to-do to one (#10675).

## Availability metrics (#4924)

Step M1 of `docs/design/proposals/main-chat-availability.md`: four numbers that say whether the main chat stays
available to the owner while background work grows. They are measured for every assistant conversation and read with
`GET /api/v1/assistant/availability?hours=168` (full devices only, `docs/protocol/protocol.md`); nothing shows them in
an app or a briefing yet. Each number in one sentence:

- **Reply wait** (`reply_wait`): the seconds from the manager taking the owner's message for a conversation to the
  first reply text after it, split by what the conversation was doing when the message came (`idle`, on a round the
  owner started `owner_round`, on a round they did not `background_round`).
- **Background share** (`background_share`): per day and conversation, the round time and the number of rounds by
  what started the round (`owner`, `supervision`, `followup`, `patrol`, `conversation`, `notice`, `other`), and, of
  the time whose round's start is known (all but `other`), the share the owner did not start.
- **Queue wait** (`queue_wait`): for each system message queued for a conversation (behind its turn, or held while
  the owner talks in it), the seconds from queueing to delivery and the queue's length when it was added, by kind
  (median, 90th percentile, longest), for all conversations and for the main one alone, plus what is queued right now.
- **Ask outcome** (`ask_outcome`): per card kind, how many cards each of the owner, a supervisor, the delegate, a
  standing authorization or the default at a decision's deadline answered, and after how many seconds.

How they are recorded (`server/internal/assistant/availability.go`): each is a row in `session_work_events`, kept 30
days as the triage statistics are, with no new table, no model call and no transcript read. One row per answered
owner message (`reply_wait`), per ended round (`round`), per delivered queued message (`queue_wait`) and per ended
card (`ask_outcome`); the read joins `ask_outcome` with the `delegate_start` and `standing_authorized` events that
already existed and with the task cards of the acceptance book.

Against the proposal's acceptance rows: A1 reads `reply_wait.main_days`, where each day carries its 90th percentile
and `open_tasks`, the most tasks open when one of that day's messages came; A2 reads the main conversation's
`background_share`; A3 reads `queue_wait.main_by_kind` (`max_seconds`) and `queued_now` (`oldest_seconds`), which do
not know whether the owner was away; A4 is #4601's number and is not part of this read.

What the numbers do and do not say:

- **Reply text** is the first change of the conversation's last text in its node's session list once nothing of the
  owner's is still queued behind the turn, so it is about a second coarse and is the first written text block, not the
  first streamed token. A reply the list never showed changing counts when its round ends (`by: turn_end`), and a
  message with no reply within 30 minutes is recorded `unanswered` at that bound. Several messages sent before a
  reply count as one wait, from the first. The first message of a new conversation is not measured.
- **A message that comes during a round**: text counts as its reply only after the node showed the message queued
  or a new turn started; before that the text is the running round's own. Only a headless Claude Code that reports
  its queue ("Interjecting while it answers" above) shows a message being taken into the
  running round. On Codex, a runtime or an older Claude Code the list shows no queue, so a
  message taken into the running round is counted to that round's end (`by: turn_end`), which overstates its wait;
  one that waits for the round to end is counted to the first text of the round that follows.
- **What is no wait**: a message the hub reports as not sent (`session.assistant_sent` failed, or the manager
  refused it) or that started no turn in an idle conversation within 5 minutes is dropped, not counted `unanswered`
  (failed sends are the north star's availability, `docs/assistant/north-star.md`); a replay of a message already
  delivered (the same `client_msg_id` within an hour) opens no wait. A message from a channel or the inbox whose send
  failed while a background round wrote text within those 5 minutes is still counted, against that text.
- **Rounds**: the manager names a round after the last input it sent or accepted before the round started. `notice`
  is a task notice, a late `ask_user` answer or a spawned conversation's report; `other` is a round whose input the
  manager did not see (a new conversation's first message, which is the owner's, except the background supervisor's,
  whose first round is `supervision`; a background task reporting back,
  which is not; a round under way when the manager restarted, which also reads `unknown_round` for a wait), so it is
  listed but left out of the share. A message taken into a running round (interjecting) does not start a round of its
  own. A round that stopped without a turn's end (interrupted, its process gone) is recorded up to the stop, with
  `stopped` in its row. A recorded meeting's batches are side exchanges and are not rounds.
- **Queue wait** counts delivered messages only: a notice dropped because its card was answered meanwhile is not a
  wait. A supervision message or notice that replaces a queued one keeps the first one's queueing time. Since #4925 a
  message also queues while the owner is talking in an idle conversation, and the queue goes out by rank: the time a
  message is held for the owner and the time it waits behind better-ranked ones are both part of its wait ("Messages
  from the system wait for the owner" above).
- **Ask outcome**: `dismissed` is a card that ended without an answer, `expired` one that ended because nobody
  answered it in time (where the card's end says so; a hub that keeps the cards itself sends a bare dismissal, which
  reads `dismissed`), `remembered` an "Always allow in this session" kept earlier. An answer the assistant gave counts
  as the `delegate`'s when the delegate had taken the card up (`delegate_start`, looked for up to 48 hours before the
  read's window), as a `standing_authorization`'s when one answered it, else as a `supervisor`'s (a conversation's
  `interaction_answer`); a card the delegate handed on and a supervisor then answered reads `delegate`. The task
  cards (`decision`, `conclusion`, `verify`) are those the acceptance book still holds, so a card whose task was
  closed or asked again has left the count.
- Rounds and waits under way are in the manager's memory: a manager restart loses those, not the recorded rows.

The route is a separate one, not part of `GET /api/v1/assistant/north-star` as the proposal has it: the north star
reads one day from the database alone (also from the command line, with no manager running), while these numbers
cover a window of days and read the queues and the acceptance book of the running manager. The four top-level fields
do not collide with the north star's, so they can be served from that read later. The north star's own
`availability` is a different number: whether a conversation could be started or resumed at all. The reply wait
starts when the hub takes the owner's message (`session.user_spoke`), before the node accepts it; the hub's message
outcome (`session.assistant_sent`) only takes back the wait of a message that was not sent.

## Recommended setup (#5085)

Step R3 (section 4c) of `docs/design/proposals/main-chat-availability.md`. The mechanisms that cut asks and
background rounds ship off or empty, because each spends tokens or acts for the owner. Once a week a briefing says,
from this install's own records, which of them would have removed how many of the last 7 days' asks or background
rounds, what it costs in one sentence, and where it is turned on. Code: `server/internal/assistant/setup_suggest.go`.
One line is of another kind, a lower tier for the background supervisor's model ("A lower tier for the background
supervisor" below, `setup_suggest_tier.go`); every rule of this section holds for it too.

- **Computed, not written.** Every number is counted by the manager from what is recorded anyway; no model call makes
  or changes one, and there is no table of its own. The model is given the finished lines and told to copy them
  unchanged (`prompts/assistant/routine_facts.md`).
- **Nothing is turned on.** The block only reads. Its one write is its own book (setting
  `assistant.setup_suggestions`: the week it was last told in, and when each mechanism was last named). The model is
  told not to turn any of them on and not to call the settings tool for it; the owner does it where the line says.
- **Which briefing.** There is no weekly routine, so the block goes with the first briefing (a routine with `facts`
  on, `docs/extensions/bots.md` ("Morning and evening briefings")) that reaches the owner in a week, Monday first,
  in the owner's time zone. "Reaches" is the run's turn ending normally, with its `brief` item (which carries the
  block) in the conversation, or its Lark card going out, whichever comes first. It does not depend on the finished
  push: that push is dropped when the owner has the conversation open, when it is muted, for a task that pushes only
  what its runs send and for a health task, and the block was shown all the same. A run that failed marks nothing, so
  its retry, or the next briefing, carries the block. Every other briefing of the week has none, also when the first
  one had nothing to name. Without a briefing routine with `facts` on there is no block.
- **Where it shows.** Two places draw the computed lines themselves: a section of the Lark briefing card after "In
  progress", each line with a link to its settings page in the web app when it has one and hub.json has a
  `public_url`; and the web app's briefing card, from the `setup` list of the run's `brief` item
  (`docs/protocol/protocol.md` ("Tasks and the chat feed")), with an **Open settings** button on the lines that have
  a page (`docs/web/web.md`). The model is also given the lines and copies them at the end of its answer when no
  Lark card goes out, as it does the briefing's other rows. The Android app draws the `setup` list on its briefing
  card too, with **Open settings** on the lines whose mechanism has a page there (#5802, `docs/android/android.md`
  "Rule suggestions, held decisions, recommended setup and purpose descriptions"), and so do the Apple apps (#5804,
  `docs/apple/ios.md` "Briefing card"), with a button on the lines whose mechanism has a page there (Session Triage,
  the background supervisor's model). An app that does not draw `setup` leaves its owner, without
  the Lark card, to read the lines in the model's answer, which is the model's copy and not checked against the
  computed lines.
- **Where a mechanism is turned on.** Session triage's three switches and the remembered kinds of task are on the
  assistant's own settings page in the web app (`/settings?tab=persona`; iOS Settings › General, Android Settings › AI).
  Standing authorizations are listed there too, and Apple (#7002) and Android (#9247) also list and revoke them from
  Settings and the assistant's profile. The lines name the switch by the label it has on the web page. The background
  supervisor's model is its purpose's page of Model Settings (`/settings?tab=models&purpose=assistant.supervise`). Leader
  conversations: **Pin** in a conversation's menu. The spot-check share: asking Persona, confirmed on a card. The
  assistant answering agents' questions and the independent review have no switch in any app: their lines say so and
  name the key and value for the assistant's configuration file (`docs/operations/config.md`).
- **Once a month.** A mechanism the block named is not named again for 30 days, whatever its count.
- **Not named**: a mechanism that is on, and a count under 3 (10 rounds for leader conversations).

Each number, in one line. "Last week" is the 7 days before the run starts; "before" looks back 30 days, as long as
the work events are kept.

| Mechanism (`id`, setting) | The number | Named when |
| --- | --- | --- |
| Assistant answers agents' questions (`delegate`, `agents.delegate`) | Question cards a supervisor passed up to the owner with a recommendation (`ask_escalated`) that the owner answered once last week, with exactly that recommendation. Only a leader conversation's supervisor passes a question up, so without leader conversations this is always 0 | `agents.delegate` is off |
| Standing authorizations (`standing_authorizations`, `assistant.standing_authorizations`) | The owner's go-aheads last week for merge, push or deploy-test (read as "A rule after the same answer three times" reads them) in a directory no grant covers, where they had given the same go-ahead before and never said no to it | Always; a covered directory is not counted |
| Start directly (`task_trust`, `tasks.trust`) | Plans made last week that the owner started, every file-changing kind of which (same computer and repository, or directory outside one) they had started before, never dismissed, and `tasks.trust` does not hold | Always; a trusted kind is not counted |
| Triage: card for decisions (`triage_decide`, `agents.triage.decide`) | Stops waiting on the owner that only `decide` being off kept from being a card (not muted, no authorization link), and that went on by hand (`resumed` with `hand` `decide`) | The triage is on and `decide` is off |
| Triage: continue after errors (`triage_auto_continue`, `agents.triage.auto_continue`) | Stops on a usage limit whose CLI does not go on by itself, which only `auto_continue` being off kept from being continued at the reset, and that went on by hand (`resumed` with `hand` `auto_continue`) | The triage is on and `auto_continue` is off |
| Triage: wake on schedule (`triage_wake`, `agents.triage.wake`) | Stops waiting on something named (CI, a deploy, another agent) with no risky next step, which only `wake` being off kept from being woken, and that went on by hand (`resumed` with `hand` `wake`) | The triage is on and `wake` is off |
| Leader conversations (`leaders`, `assistant.leaders`) | The main conversation's rounds last week that a supervision message started (the `round` rows of "Availability metrics" above), and their minutes | No leader conversation exists |
| Spot-check reviews (`review_sample`, `assistant.review_sample_percent`) | Supervised tasks whose first and only review, last week, was the supervisor's own `accepted` with every `done_when` criterion met with evidence, no `user_check`, the work live, and never reopened | The share is 100 |
| Independent review (`independent_review`, `agents.review.launcher`) | Reworks a supervisor sent last week after the done judge had called the work done (`review_rework` with `judged_done`) | No reviewer launcher is set |
| A lower tier for the background supervisor (`supervisor_tier`, the purpose `assistant.supervise`) | The background supervisor's settled acceptances of the last 30 days on the model its purpose is bound to now, and how many of them were reopened ("A lower tier for the background supervisor" below) | At least 20 settled, at most 1 in 20 of them reopened, and a lower tier that is not the smallest is known |

What the numbers do and do not say:

- **They are lower bounds of what was certain, not forecasts.** A go-ahead counts only as a repeat, a plan only when
  all of its kinds were started before, a review only when the judge's own conditions held; the delegate, the judge
  and an independent reviewer still decide case by case once they are on, so a mechanism may remove fewer.
- **Leader conversations move rounds, they do not remove them.** The number is the main conversation's time spent on
  follow-ups; a leader spends that time in its own conversation, for the tasks of its direction only.
- **The triage's own switch (`enabled`) is never named.** With the triage off nothing about a stopped turn is
  recorded (reading one is the model call the switch costs), so there is no number to give, and its three actions
  are counted only while it is on. A stop on a transient model error is not counted either: `transient_retry`
  continues those by default (`docs/assistant/agent-work.md` ("Switches")), so the proposal's example for
  `auto_continue` became the usage-limit count.
- **"By hand" is what Persona did not do.** The triage notes, when it leaves a stop alone, the one action that its
  switch being off kept it from (the same conditions the switch itself checks, so a `/loop`, a background task, a
  wait whose next step is risky and a usage limit the CLI resumes itself are never noted). When that session works
  again the `resumed` row carries the action as `hand` only if no message of this manager's went to it in the two
  minutes before (the triage's own, a supervisor's rework, a task's follow-up, the assistant's session tool) and the
  node does not say the agent woke itself (a background task's report, a `/loop`). What is left is a message from
  outside Persona: the owner's, in an app or typed into the agent's terminal. The manager cannot tell the two apart
  from anything else typed there, which is why the lines say "by hand" and not "you wrote".
- **The triage's notes are not kept over a restart of the manager.** A stop triaged before a restart and resumed
  after it is not counted. Rows from before this version carry no `hand` and are not counted either.
- **A hello that fails names nothing it cannot read.** Whether the delegate and the independent review are on comes
  from the hub's hello; when it cannot be read neither is named, as the triage's three are not when its switches
  cannot be read.

### A lower tier for the background supervisor (#5805)

The block's last line, from the numbers of "Supervision by model" below (section 9c of the proposal): the reviews the
background supervisor made on its current model held, and its provider has a tier below that one. It is computed like
the other lines, named at most once in 30 days, and changes no binding: the owner picks another model for the purpose
**Background supervisor** in Model Settings, or does not. Code: `server/internal/assistant/setup_suggest_tier.go`.
All four must hold:

- **The background supervisor is on and did the reviews.** `assistant.main_supervises_own_tasks` is not on, the
  background supervisor exists, and only `task_reviewed` rows with role `background` are read. A review by the main
  conversation, a leader or a task leader's agent says nothing about this binding.
- **Enough settled acceptances: 20.** Counted over the last 30 days (as long as work events are kept; 7 days rarely
  hold enough to judge a reviewer), on exactly the model the purpose `assistant.supervise` resolves to now (its own
  binding, else the main chat's, else the provider's default model; a `[1m]` marker aside, any effort). Reviews made
  on another model, before the binding was changed, are not counted, so a changed binding starts from zero. The row's
  model is what the node listed for the conversation; when a node lists another form of the name than the binding
  has (an alias, a gateway's own id), nothing matches and nothing is suggested.
- **Reopened over settled is at most 1 in 20 (5 percent).** The share is `reopened` over `settled`, by the one rule
  of "Supervision by model": never over `accepted`, because an acceptance younger than 24 hours has not had its day
  to be reopened in and would only water the share down. A reopen is held against the background supervisor's latest
  acceptance of the task before it even when another conversation accepted the task in between, which can only make
  the share higher.
- **A lower tier that is not the smallest is known.** All the manager knows of tiers is the tier models of a provider
  added in Persona (`opus_model`, `sonnet_model`, `haiku_model`, `fable_model` of the provider's record, which the
  owner or the import of their shell alias filled in; smallest first: `haiku_model`, `sonnet_model`, `opus_model`,
  `fable_model`). The smallest slot is read as the node runs it: `haiku_model`, else `small_model`, which is where
  the web app's provider form keeps its light tier. The supervisor's model must be one of them; the tier below is the nearest filled slot under it with
  another model; and under that one there must be yet another filled slot with a third model. So on a provider with
  three tier models only the largest gets the line, pointing at the middle one. The smallest tier is never suggested
  (an acceptance is a judgment call: `docs/assistant/model-config.md` "Models for each role"), nor is a tier the
  manager cannot tell is not the smallest. Not known, and so never suggested: plain `claude` and an rc-file launcher
  (nothing is stored about their models), a provider with fewer than three distinct tier models, a model that is none
  of the provider's tier models, a direct API provider, and Agent presets (`docs/assistant/model-profiles.md`), which
  have names and descriptions but no order.

The line names the two models as the owner's provider has them (no model is named in code): by the name the
provider's record has for the model (`models[].name`, which the model picker of Model Settings lists it under), else
by its id. It gives the two numbers, both counts of acceptances, not of tasks (a task accepted, reopened after its
day and accepted again is two), and in one sentence the cost and the risk: a lower tier spends less on every review,
but the numbers say how the current model reviewed, not how the lower one will. It says its own 30 days, and the
block's introduction says the numbers are of the last 7 days "unless a line names its own period" and that each item
"stays as it is until you change it yourself", which holds for a mechanism that is off and for a binding alike (the
same sentence on the Lark card and, from the web app's own strings, on its briefing card). The `brief` item's `count` is the settled acceptances and `setting` the
purpose id. Out of scope: the reverse. Nothing suggests a stronger tier, whatever is reopened; the table of "Models
for each role" says when to raise it. Once the owner has moved the supervisor down, the same rule may name the next
tier down after its own 20 settled acceptances, but never the smallest.

## Card ledger (decision rights, rule 4)

The owner decides only six kinds of things (decision-rights design of 2026-11-23): 1 goals and direction, 2 what only
they know, 3 what goes out under their name, 4 money, 5 what cannot be undone and they never said, 6 what only they can
do in person (a login, a second factor, a password or form, the experience check). Every card or push the manager sends
to the owner is recorded with its source, the kind of the six it is (0: none of the six) and its owner
(`assistant/card_layers.go`). The source is a code constant named where the push is made (`withCard` on the ctx of
`push`/`pushResult`/`ask`, `deliverCard`, or `inputCardSource` for a card: an Agent's by its kind, the manager's own by
what `ask` was told or by its tool), and its kind comes from one table, `cardCategories`, never from a model: secret and
form cards are 6; a connector's write left to the owner (mail, a chat message, a comment) is 3; a supervisor's
`needs_user`, its decision, an explicit `ask_escalate` and what the owner set up themself (their reminders, water
reminders, minutes, the day recap, the briefing fallback, finished-turn pushes) are 1; everything else, an Agent's
approvals, plans and questions, `confirmOutbound`, the main conversation's own `persona_settings` cards and an untagged
push included, is 0, and every push of kind 0 is a bug. A test holds every source constant to having a kind. Counts are
kept per day of the owner's time zone in the setting `assistant.card_ledger` (eight days; in memory only without a
database). Once a day, from the morning briefing's time (`acceptLoop`'s tick), the day before's count goes to the main
conversation as a message kept without waking it (`keepRelayLater`, setting `assistant.card_ledger_told`): in all, each
of the six kinds, and kind 0 by source. It is never pushed to the owner and the briefing does not carry it. The ledger
only records; which cards are pushed is decided where they are made. Its report is internal-only: it is kept for
the main conversation but left out of the queue shown above the composer.

## Supervision by model (#5086)

Step K1 of the same proposal (section 9c): what supervision costs per accepted task, by the model the supervisor ran
on, so a cheaper or a stronger model for the background supervisor (`assistant.supervise`,
`docs/assistant/model-config.md` "Models for each role") can be judged on this install. It is the `supervision` field
of `GET /api/v1/assistant/availability`; no app shows it, and nothing acts on it. The weekly block reads the same rows
for its one line about a lower tier ("A lower tier for the background supervisor" above), which changes nothing
either. Each number in one sentence:

- **Accepted** (`by_model[].accepted`): the acceptances in the window, each a `task_review` that accepted a task and
  closed it, by the model and effort the reviewer ran on when it reviewed; a task accepted, reopened and accepted
  again counts twice.
- **Settled** (`by_model[].settled`): those of the acceptances that were made at least 24 hours before the read, whose
  day to be reopened in is over.
- **Reopened** (`by_model[].reopened`): the settled acceptances whose task was reopened within 24 hours of the
  acceptance, whichever way (by hand, a change asked on its "Please verify" card, its agent set to work again). The
  rate to compare between models is `reopened` over `settled`.
- **Rework rounds** (`by_model[].rework_rounds`, `rework_rounds_per_task`): the times the accepted tasks were sent
  back between their previous acceptance (or their start) and this one, whoever sent them back, in all and divided
  by the acceptances.
- **Output tokens per day** (`days[].output_tokens`, next to `days[].accepted`): per day and supervisor conversation,
  the output tokens the conversation generated in the rounds that ended that day, beside the tasks it accepted that
  day.

**Tokens are not attributed to tasks.** Nothing recorded says which task a supervisor's turn was spent on: one
supervision message gathers several tasks' signals, the token usage the nodes report (`docs/server/usage-windows.md`,
the usage buckets) is per computer, model and launcher with no conversation, and a conversation's own count is one
running total. So the read does not give "tokens per accepted task"; it puts a supervisor conversation's day of output
next to that day's acceptances and leaves the division to the reader, who knows what else the conversation did that
day. The count is **output tokens only** (no input, no cache reads or writes, which are most of a long conversation's
cost) and is **the conversation's whole output**: for the main conversation that includes its talk with the owner, for
the background supervisor nearly all of it is supervision.

How they are recorded (`server/internal/assistant/availability_supervision.go`), as work events with no new table:

- One `task_reviewed` row per `task_review` (any verdict). An acceptance is written once it closed the task: not for
  a task that was closed already, nor when the close failed or closed nothing (a concern, which only its report
  closes), so a retry is not a second acceptance. The row has the task, the verdict as written, the task's rework
  rounds in all (`round`) and since its previous acceptance (`rework_rounds`, counted from the ledger's reviews, of
  which it keeps the last 20), the reviewer, its role (`main`, `background`, `leader`, `conversation` for a side
  conversation reviewing what it handed out, or `task_leader` for a task leader's agent reviewing its workers) and
  the model and effort it ran on. Those are what the reviewer's node lists for it (`Session.model`,
  `Session.effort`); for the main conversation and the background supervisor, what the node does not say is filled
  from their purpose's binding. Both read `""` when neither says (the launcher's own default, which the manager
  cannot name).
- The `round` row of "Availability metrics" also carries the conversation's `model`, `effort` and `output_tokens` as
  its node listed them when the round ended; `output_tokens` is the conversation's running total
  (`Session.output_tokens`), and the read counts the difference between two rounds.
- A reopen is the `todo_reopened` row that already existed (`docs/assistant/agent-work.md` "Evaluation"); it counts
  against the task's latest acceptance before it, once, when it came within 24 hours of that acceptance. That is the
  same day the work stats' `wrong_done.reopened` gives a close by the done judge, so "reopened" means a reopen within
  24 hours in both; the two count different closes (a review's here, the judge's there).

What the numbers do and do not say:

- Only a conversation's own `task_review` counts. A task the done judge closed for its supervisor
  (`review_sample_percent` below 100) has no reviewing conversation and is in none of these numbers.
- `by_model` is keyed by model and effort alone: the main conversation, the background supervisor, leaders and task
  leaders' agents on the same model and effort share a row. `days` has one row per conversation with its role, for
  telling them apart; a task leader's agent has no `days` row, because an agent's rounds are not recorded as an
  assistant conversation's and there is no output count to put beside its acceptances.
- A model's name is whatever the node lists (an alias, a gateway's own id), so one model may appear under two names
  when a binding was changed from one form to the other.
- The first round recorded for a conversation counts no tokens: there is nothing to take the difference from. Rounds
  from up to 48 hours before the window supply that for the window's first round. A count that goes down (a new
  transcript) counts from zero again. A node that lists no output count (a runtime that reports none) gives 0.
- A day's `model` and `effort` are those of the conversation's last round that day; a conversation whose model was
  changed during the day has one row for it.
- Every row's reopens are counted by one rule, so a model in use for three days and one in use for a month compare:
  an acceptance younger than 24 hours is in `accepted` but in neither `settled` nor `reopened` yet, and a reopen
  later than 24 hours after the acceptance is not held against it. An acceptance from before the window that is
  reopened inside it is not counted.

## Chat titles

Requested: "Generate titles after starting new chats; standardize as ‘MMDD | Type | Topic’." Implemented as follows:

- **Active direction exception**: every client shows the existing canonical direction name as its session title,
  without a date or task-kind prefix. `conversation_rename` accepts a plain `name` from the main conversation and uses
  the existing direction-name operation; user-chosen names require `user_asked`. The ordinary stored title is retained
  and returns when the direction ends. Explicit name changes publish immediately.
- Ordinary title = `MMDD | Type | Topic`. MMDD is the chat's start date in **user timezone** (`Hub.userLocation()`; hub runs UTC, but a 00:30 start uses the user's local date).
  Types: Feature / Design / Fix / Optimization / Release / Exploration / Docs / Research / Review / Scheduled; topic ≤ 14 characters, no project / repo names. Example: `1101 | 修复 | 搜索框和顶栏重叠` ("1101 | Fix | Search and top bar overlap").
- **When generated**: new hub chats (App `POST /api/v1/assistant/conversations`) are recorded "pending title". After the first turn (working → done / idle),
  the assistant uses the "Chat title" purpose model in the background (`assistant.title`, Model Configuration › Information processing), reading first message / reply to produce "Type | Topic". No interruption, not delegated to the assistant chat itself, replies don't wait.
  Budget (#1532): 45 s per call, 2 minutes per naming, `max_tokens` 1024. The 2–3 s first measured no longer holds: with a thinking model behind the gateway a title-sized call measured p50 6 s, p90 21.6 s, and its thinking spends the same `max_tokens` (at 200, replies stopped after 188–192 thinking and 4–8 output tokens: half a topic that still parsed). The old 15 s cut-off left about a fifth of new chats on the fallback title.
  Scheduled-task chats immediately get `MMDD | Scheduled | Task name`, no model call, retained as the task reuses its chat (previous section).
  AI twin chats immediately get `MMDD | Inbox | Item title` (#3264; formerly untitled, sidebar rows began "This is an Inbox item; you…"); Inbox is hub-only, not a model-selectable type.
- **Validation** (`parseTitle`): take first line, tolerate quotes, fullwidth pipes, redundant model dates; invalid type / long topic retries once with "Previous result didn't match format";
  second valid type with long topic truncates to 14 characters; otherwise fallback `MMDD | Exploration | First 14 characters of first message` (tags / Markdown stripped). Same fallback without model config.
  Each way an answer goes wrong gets its own one more call within the naming's time: no answer (timed out, connection failed, gateway 5xx: `ai.Transient`), a reply stopped at `max_tokens` (half a title even when it parses: twice the room; `ai.Reply.Truncated`) and the broken format above.
  When the naming ends for want of an answer (or the first exchange couldn't be read) the fallback is only a stand-in (`tries` in the record): the next turn's end names the chat again from its first turn (`after_seq: 0`, not the newest), three namings at most; a user or assistant rename meanwhile stays. A model that isn't set up or turns the request down (4xx, refusal) answers the same next time: that fallback is final.
- **Storage**: assistant store (hub settings `assistant.titles`: session id → title, author, start time, `tries` for a stand-in, an extension's `hint`), snapshot to hub, replaces App session-list `title` (`decorated`).
  No transcript writes; same for Claude / Codex, no node changes.
- **Once only, user wins**: App Rename (drawer long-press chat › Rename…, `POST /api/v1/sessions/{mid}/{sid}/rename`) records user-authored titles, never overwritten;
  even before the first turn ends. Codex renames are stored only in hub (node would write Claude custom-title lines into Codex rollout).
  A Claude rename goes to the node first and is stored once it succeeds. Before Claude has written the transcript (the Feishu /agent renames its conversation
  right after starting it), the node keeps the title and writes the custom-title line when the transcript appears (#5866).
- **An extension's rename is a hint** (#3758): when the rename comes with an extension's token (the hub's auth says so and passes the extension on as
  `assistant.rename`'s `extension`; a client's own word never counts), the node is renamed as usual, so the chat shows that title until it is named, but the
  record keeps it as `hint`, not as the user's. A chat the hub started is still named after its first turn, and the model reads the hint with the first
  exchange (`prompts/tools/session_title_input.md`); the stand-in topic falls back on it when there is no first message. A chat already named keeps its name.
  A chat the hub never noted shows the hint (`by: extension`) until someone renames it. An older hub sends no `extension`, and an older manager takes the
  rename as the user's, as before. Extensions that call the hub with the full token (`hub_api`) are the user as far as the hub can tell: their renames are the user's.
- No backfill for old chats lacking pending-title records, except old AI twin chats gain `MMDD | Inbox | …` while backfilling origins ("Chat origins" below).
- **Main chat has no title** (#1313): App session-list `title` is always "Persona" (`mainTitle`, settings `assistant.main`),
  not node's first-message title (headless transcripts lack Claude ai-title), no `MMDD | Type | Topic` (discard pending-title on first-turn completion without model call),
  App rename returns 400 "Main chat has no title to rename" (`conversation_rename` already rejects). Top bars show the avatar: iOS / Mac / Android avatar name (default "Persona"), web "Persona".
- App drawer rows draw `MMDD | Type | ` in secondary color, topic primary, truncating topic end in narrow space (`ConversationTitle`); other titles unchanged.
- **A title that is a context tag** (web, 2026-11-14): a chat whose title is still its first message's `<persona-context …>` tag (one started from picked context
  before it was named) shows what was typed after the tag, else the tag's `title` with its entities decoded, else **New chat**, in the sidebar, the
  header, All chats, ⌘K and Rename (`conversationTitle`, `web/src/views/assistant/conversations.ts`); before, the raw tag showed. A leader's header says its
  direction's name, as its sidebar row does ("Leader conversations" in `docs/assistant/unified-entry.md`), not its `MMDD | Type | ` title.

## Guest conversations (2026-11-06, #5195)

A proxy reply the bot writes for a group is one of these too: the hub starts it itself, with a single hub-owned tool, `bot_group_reply`, and the question never enters a conversation of the owner's (`docs/extensions/bots.md`).

A teammate's question that group access lets through is a third kind the hub starts itself (`bot_group`, `docs/extensions/bots.md` "Group access for teammates"). It is one conversation per chat and thread, registered before the node starts it, and it lists `bot_group_reply` plus the connector tools the owner named (`guest_tools`), nothing else: no snapshot, no memory, no skills, no knowledge-base tools, and never a work machine. The question is another person's words and is not stored with the owner's.

A conversation an extension opens to answer **someone else** for the user (for example a Lark extension's opt-in auto replies to @-mentions). Someone else's message goes into it and its answer goes out as the user, so it must have none of the assistant's powers.
An ordinary extension conversation is an assistant conversation (the assistant directory, its CLAUDE.md and memory, every `mcp__persona` tool allowed up front, `agent_start` / `agent_send` asking the phone only in the health space), where only the prompt stands between the message and those tools; hence this kind.

- **Starting one**: `POST /api/v1/assistant/conversations` with `guest: {"tools": ["lark_agent_reply"]}`. `tools` are 1–8 **connector** tools (an extension's; the assistant's own, the phone's, Cua's and ego's are refused with 400). It can't be combined with `main` or `space`, and `report_tasks` is ignored (it hands nothing out).
  Conversations without `guest` are exactly as before. An extension first asks `GET /api/v1/assistant/capabilities` (`{"guest_conversations": true}`): an older hub would ignore `guest` and start an ordinary conversation, so on 404 the extension starts none.
- **Directory**: the hub has the node's `assistant.guest_prepare` write `.persona/guest` with only a guest CLAUDE.md / AGENTS.md (`prompts/assistant/guest_claude_md.md`), leaving the assistant's connectors, CLIs and skills alone. A node that doesn't know the method (too old) gets a 400 and nothing starts.
- **Node** (`node/assistant_guest.go`): Claude Code on the CLI (Codex and agent runtimes such as DSH are refused), never the warm spare process.
  Every CLI process it runs in (start, resume, compaction, model switch) has `--tools ""`, `--strict-mcp-config` (only Persona's own MCP server, none of the user's or the extensions'), and `--allowedTools` naming only its tools.
  None of the owner's Claude Code setup reaches it either (#5786), as for a meeting's room ([meeting-minutes.md](./meeting-minutes.md) "Nothing of the owner's Claude Code setup"): every CLI process also has `--setting-sources project` and the guest's own `--settings` (`node/assistant_guest.go` `guestSettingsFile`), which excludes the CLAUDE.md and AGENTS.md files and `.claude/rules` of every directory above its own and of the user's profile, turns auto memory off, and keeps only the environment, login and model keys of the launcher's and the profile's settings. Without the excludes, `--setting-sources project` would still read `~/.claude/CLAUDE.md` and every `~/.claude/rules` file as the home directory's project files.
  A launcher's own arguments still reach it, as they reach every process the launcher starts (for example a shell function that appends a system prompt file): run guest conversations on a launcher without them.
  The manager registers the guest's session id and tools before starting the node, as it does for a meeting's room:
  Claude Code lists the Persona MCP server's tools as it starts, and a first list asked for before the registration
  would be empty. A start that fails drops that registration.
  The guest flag and tools live in its session state (`sessionState.Guest`) and follow a fork; a later message naming another launcher or runtime is ignored and it runs on what it started on. It gets no live snapshot and no gate proxy addresses. The node relays its MCP calls with `guest: true`.
- **Hub** (`assistant/guest.go`, setting `assistant.guests`, kept 30 days): `mcp.tools` lists and `mcp.call` runs only its tools; `assistant.context` gives it an empty snapshot; `SendParams` drops a launcher, model, runtime or permission mode picked for a message.
  While the record can't be read, every tool call is refused (the user's own conversations keep their snapshot and picks: the node enforces those for a guest itself), and a conversation the node calls a guest but the hub doesn't know gets no tools.

## Chat origins (#3264, 2026-11-06)

Feedback: many AI twin chats flooded sidebar "Recent", hiding user chats; automatically created chats need origin labels and default collapse, while remaining openable.
Each assistant chat records origin (`assistant/origins.go`, settings `assistant.origins`), snapshot to hub session `origin` / `origin_ref`; values, backfill, client-sidebar rules: `docs/protocol/protocol.md` ("Origin of assistant chats").

- **Recording**: every creation path uses `start()`, with caller origin: App `POST /api/v1/assistant/conversations` is `user` (extension tokens `extension`, `lark.agent` model `lark_agent`;
  extension identity from hub-forwarded `X-Persona-Extension`, discarding client headers), channel `/new` `channel`, AI twin `inbox`, scheduled task `task`, `conversation_new` `spawned`; main chat always `user`.
- **Backfill**: each machine list (`SessionsChanged`, node's raw list with first-message titles) triggers background backfill for assistant chats lacking origin. Evidence order: AI twin session recorded in Inbox items,
  task-owned chat (`run_session_id`, runs not sent to another chat), hub-assigned title (Scheduled / Inbox), first-message prefix (Inbox, `⏰` task); if title insufficient but hub-created (pending / automatic title),
  read first message to recognize forwarded-chat and `/agent` `<persona-context>` (node titles strip these blocks). Otherwise `user`. Creation-time records always override inferred backfill, never changed by it.
- **Rerun**: `POST /api/v1/assistant/origins/backfill` immediately processes the assistant machine list; `redo` reevaluates inferred records, but never changes a recognized origin back to `user` (hub-decorated list titles are no longer first messages).

## Managing its own conversations

Requested: "Can the Persona assistant manage its own chat history—rename title, delete (archive), new, send messages to sessions / threads?"
Assistant MCP tools (`assistant/conversations.go`, descriptions `prompts/mcp/assistant/conversation*.md`), identical for Claude / Codex:

| Tool | Purpose |
| --- | --- |
| `conversations_list` | Sidebar chats: id, title, last activity, main / current / running / Codex flags, last text, tags; `query` filters title / last text; `tag` keeps only chats carrying one exact tag; archived excluded by default (`include_archived`) |
| `conversation_read` | Read another chat's latest N turns (default 2, max 10), same `agent_read` truncation (`transcriptText`: one line per tool call, only failed results, at most the final 40000 bytes, starting at a UTF-8 rune boundary; the earlier-part-cut marker is added separately) |
| `conversation_rename` | Set title with `kind` + `topic`; hub formats `MMDD \| Type \| Topic` (start date, previous section). User titles stay unless explicitly requested (`user_asked`, then user-owned); assistant titles marked `assistant`, no autonaming override, App user renames still win. Main chat has no title to rename |
| `conversation_tags_set` | Replace one own-space chat's whole tag set (`tags: []` clears it); the calling chat may tag itself; limits and normalization below |
| `conversation_archive` / `conversation_unarchive` | Archive / unarchive, below |
| `conversation_new` | Start a chat with an opening message, return id immediately without waiting; normal auto-title after first turn, excluding opening source context; optional `tags` apply the same limits and are saved with the new chat before the answer, an invalid set refusing the whole call |
| `conversation_send` | Send to another chat, not itself |

- **Archive only hides**: hub settings `assistant.archived` (session id → machine, time, actor), session list `archived: true`;
  `GET /api/v1/assistant` excludes by default, includes with `?archived=1`. No transcript deletion / hard delete. Main chat can't archive (API 400, tool error); replace through "New main chat" ("Compaction" above).
  - **Archive itself**: allowed ("We're done, archive this" in a side chat is natural). Finish the turn normally; an open chat on iOS, Android or the web stays open and is only hidden from lists;
    new user messages restore it (`unarchiveOnSend` in `sendMessage`), as does another chat's `conversation_send`.
  - node lists only recent sessions: old archived chats can disappear from Archived (as they do from normal lists), but archive records remain and `conversation_unarchive` can retrieve by id.
- **Delete a conversation** (#6172, server and web): after confirmation, an ordinary or archived former main chat disappears permanently from Persona lists and history reads. A Postgres setting (`assistant.deleted_conversations`) retains only machine/session identity, space and deletion time. Old links say **Conversation deleted**; task plans, to-dos, run records, saved briefs and extracted knowledge keep their references and content, and delegated Agents continue untouched. The computer's transcript file is retained; this is not an erasure of previously derived knowledge or a filesystem cleanup. A new node inventory, send or unarchive never restores a deleted chat. Active turns, compaction, queued messages and pending requests must finish or be stopped first. A chat that still owns or supervises an unfinished task also refuses deletion (409); finish or move that task first. Former-main work uses the existing handover once a successor main exists, so a historical reference alone does not block deletion; an idle process is allowed because later Persona sends, tool calls and reads are refused. The main chat, the built-in supervisor and a current direction's lead conversation (Pinned) cannot be deleted (400 `conversation_protected`); a lead conversation deletes as any other once it is unpinned (#11392). To remove the main chat's previous history, use **Clear context**, then delete the archived former main chat. Native deletion controls are not included in this phase; native Clear context remains #5100.
- **Conversation tags** (#9719, server and web): user-defined labels on a chat, zero or more short strings each. They live only in the assistant's settings store as `assistant.conversation_tags` (session id → machine id, tag list, change time, actor `user` / `assistant`), the same shape and lazy-loaded book as `assistant.archived`; no database table or migration. Every write trims surrounding whitespace, drops empty entries and de-duplicates to the first occurrence; a tag is at most 32 runes and a chat carries at most 10 tags, an over-limit write refused (HTTP 400 / tool error) before anything changes. An empty tag set deletes the record; deleting the chat deletes its tags with it. The health space and the assistant's own space cannot tag each other's chats. Decorated session rows carry the tags over the same snapshot / push path as titles and archives, so the apps' lists update live; `GET /api/v1/assistant?tag=<tag>` filters the list to one exact tag, and `PUT /api/v1/assistant/conversations/{id}/tags` (full-role only) replaces a chat's tags with `{"tags": [...]}`. The web "All chats" page shows one filter chip per tag in use, each row's tags, and an "Edit tags" dialog from the row menu (the main chat is taggable; the built-in supervisor is not); the sidebar is unchanged. Default: no chat has tags.
- **Inter-chat messages** (`conversation_send`, `conversation_new` opening):
  - Prepend `<persona-context kind="context" title="来自对话「…」">` (`title`: "From chat ‘…’", `prompts/assistant/relay.md`), explaining it's not user-authored, source chat, hop count;
    if a reply is needed, use `conversation_send`; do not reply merely to acknowledge receipt. `Item.source.kind="conversation"` retains the stable sender id and title, separately from the runtime role and native peer `from`. Clients show a source message rather than a user bubble; the relay's instructions remain inspectable as Context. Explicit source metadata is persisted in a separate `message-source` context, not a selectable Context card. Full canonical legacy relay contexts remain recognizable; user-pasted templates are protected at the entry point.
  - **The owner's words travel with it** (#3822): `quote: "latest"` carries the sole owner message or verified incoming quote in the current turn. If several fit, choose an id or the owner's exact words. An id or at least four characters of exact wording can also select a passage from the last ten turns. The receiver's brief preserves the original wording and source, with `request.part: true` for a passage. Just me turns and the marked questions themselves keep those words in the conversation (`docs/assistant/unified-entry.md`, "Briefs (#3822)").
  - **A spawned chat reports back** (#4669, `assistant/spawned.go`): a chat `conversation_new` opened is followed (`assistant.relay`'s `spawned`, kept 7 days) until it sends to its opener with `conversation_send`. A turn of it that fails, or one that ends without it ever having reported, leaves one notice in the opener (`OriginRef`; `prompts/assistant/spawn_notice.md`) with its id, link and error or last words. The notice is a message from the spawned chat under the same hop and rate limits as its own `conversation_send`; past them the log says the opener was not told.
  - **A failed turn is a failure** (#4669): the node makes a Claude turn whose result is `is_error`, or whose whole answer is an auth / upstream error line (`assistantwire.ErrorOnlyText`, sharing `AuthErrorWords` with the triage's `userErrorWords`), the session's `turn_error`: the apps show it failed, the push is ⚠️ and goes out however quick the turn was, and the hub logs `assistant <id>: turn failed (launcher …, kind …, code …)` without the error's words.
  - **Queue while running**: if target is running, already queued, or the owner is talking in it, persist in hub settings `assistant.relay` (survives restart), displayed above the input like typed queues
    (session `queued`). After its turn ends (no longer working) and the owner's hold is over, hub sends one at a time, by rank ("Messages from the system wait for the owner"). Don't write into running processes: Codex merges through `turn/steer`, not queueing.
    They come after the user's own queued messages, and `queued_relayed` counts them: the apps only show and copy them.
  - **A queued send in doubt (#10690) and direct supervision confirmation (#12567).** The manager saves the attempt time before sending. If the answer is lost or
    the manager stops, it checks the most recent three turns for the relay before sending again. It makes up to
    three checks, each with a 15-second timeout, and waits 10 seconds between checks. A match removes the relay
    from the queue. If the last check fails or still finds no match, the manager retries with the same `once_id`.
    Direct and queued supervision sends apply the same transcript-confirmation boundary even when `assistant.send`
    returns success: confirmation reads bypass the database history cache on the active conversation source (leaving
    retired frozen segments on durable history) and allow a brief bounded settle window on successful RPC returns for
    asynchronous transcript flushes. If the rendered supervision user item is still not visible or the transcript
    cannot be read, the relay (including tried PR, decision-reply, and unhandled-inbox digests) is persisted with its
    exact rendered payload and attempt state under the same `once_id` for bounded reconciliation and retry. Untried special digests
    continue to restore for revalidation, and an unresolved supervision relay (tried, durable-doubt, or withdrawn)
    keeps decision-reply interjections pending until that relay is reconciled.
    An uncertain supervision digest stays queued if rechecking its tasks would change the text or destination.
    The node keeps that id for ten minutes or until it restarts. When there is no owner to ask, a possible duplicate
    is preferable to silently losing the relay.
- **The user's own queue** (#4342): a message the user sends while a chat works goes to its headless Claude at once and waits in Claude Code's
  queue until the next step takes it in. The transcript notes that wait without the words, so the node gives each message an id and follows
  Claude Code's `command_lifecycle` for it (`server/internal/node/assistant_queue.go`); the waiting ones are the session's `queued`. The queue
  strip works as in an Agent chat: take back to edit, delete and move withdraw them (`cancel_async_message`, the moved ones written again in
  the new order, never one that went in meanwhile), and Send now is Claude Code's own (`interrupt` with `send_now`). A Codex chat has no
  queue (`turn/steer`).
  - **Loop prevention**: each chat tracks the current message's hops from the user's input (user 0, a request +1, expires after 1 hour); don't send beyond 3 hops.
    A hop is a request, not a message (#7391, `hopLocked`): a chat's first message back to the chat whose request it works on is the answer and keeps
    that hop; a second message back, a message to a third chat, the message after an answer and `conversation_new` cost one. An answer to a question
    of its own leaves the answer it owes free. So a main chat that hands work to others goes back and forth with each of them three times
    (main → worker at hop 1, its answer at 1, … the third request and its answer at 3); counting every message refused the worker's second answer
    (hop 4). Two chats that keep writing to each other still stop after 2 × 3 messages however slowly they write, which the rate limit alone would not do.
    Per chat, at most 20 sends to the same target and 30 total within 10 minutes, a cost cap only (memory only, reset on hub restart). User messages reset hops to 0.
  - **The main chat dispatches** (#10099, `relayAllowedOn`): the limits above guard against loops (per pair), spread (hops) and fan-out (per
    sender); none of them should leave the owner's dispatch center stuck. So a request from the main chat is never refused for its hop: past
    3 it goes at hop 3, where the receiver may answer it but sends nothing further. Requests *to* the main chat cost hops as before, so a
    leader can't keep the main chat going (leader ↔ main still stops after 2 × 3 messages). The main chat's sends to its leader chats count
    per pair (20 per leader in 10 minutes) but not against its 30-per-sender cap, which stays for every other target.
  - **Rates queue, loops are round trips, the owner's words are free** (#10400, `relayAdmit`). A fixed 3 per pair in 10 minutes refused
    one-way traffic (the main chat forwarding the owner's requests one after another) while a slow ping-pong never tripped it. Now:
    - Past the per-pair or per-sender cap a `conversation_send` is not refused: it gets the first time the window has room (`Due`),
      waits in the receiver's relay queue held until then (`holdLocked`; after a restart `drainHeldRelays` finds it) and goes in once,
      in order. The sender's result says it is queued and at about what time. At most 5 per pair wait this way (`relayQueueMax`);
      past that the send is refused. `conversation_new` is not queued: a new chat starting minutes later is not what was asked.
    - The loop guard counts turns: two chats whose messages change direction more than 2 × 6 times in an hour (6 round trips,
      `relayRoundsMax`, `turnsLocked`) are cut, the main chat included, until the owner speaks in one of them.
    - A send whose `quote` the hub verified (#3822), written in the last hour, is the owner's request: no rate, no round trip, hop 1.
      Only the first send of a quoted message to a target is free (`quoted`); the same quote to the same chat again counts as usual.
  - **A scheduled run starts a new chain** (`scheduledWake`): a follow-up or routine that goes into an existing chat clears its hop, as a
    run in a fresh chat always did. At most once per chat per 10 minutes, so a chat cut off by the hops can't schedule itself a minute
    ahead over and over to keep a loop alive; the rates hold either way.
  - **`conversation_new` has its own budget**: 3 per chat in 10 minutes, 10 for the main chat, and a start that fails gives its place back
    (`relayRefund`). It has no per-target count: every target is a new chat; the budget is the fan-out guard.
  - Sending to archived chats restores them.
- **App**: swipe left to Archive in sidebar (not main chat; also long-press). Sidebar changes `ScrollView` + `LazyVStack` to `List`, removing insets, separators, backgrounds
  with unchanged appearance, enabling system swipes. With drawer open, a left drag starting on a **swipeable chat row** goes to the row (`SwipeRowMark`; `DrawerPan` makes row gestures wait for it to fail);
  other left drags still close the drawer. Bottom "Archived (N)" if nonzero, also in Recent ⋯; opens `ArchivedConversationsView`, tap to open,
  swipe / long-press Unarchive. Mac shares code (two-finger trackpad swipe).
- Calls remain tool cards with localized Chinese source names (`ToolPresentation`: New chat, Send to chat, Rename chat, Archive chat…).
- Measurement (2026-11-01, isolated test hub on hub machine: fake HOME, separate port / database, copied launcher pointing to fake Claude config): assistant performed new → list → rename → send → archive → list including archived → unarchive → read, all 8 succeeded. Both new-chat messages included source and hop 1; it answered only "OK", no relay reply. First-turn untitled chats were formerly "assistant" in lists / source context (directory-name title), now "New chat".

## Dispatch policy

The assistant can run under an optional dispatch policy (`internal/assistant/dispatch_policy.go`): an organization
rule that keeps a conversation class from handing out work or relaying it to the main or a leader conversation. With
no policy stored, or one past its expiry, nothing changes. The owner reads and writes it as the assistant setting
`dispatch_policy` (`GET`/`PUT /api/v1/assistant`), and an extension granted the `assistant.dispatch_policy` hub scope
uses `GET`/`PUT /api/v1/assistant/dispatch-policy`; an extension-written policy must expire within one hour, so a
stopped or uninstalled extension leaves nothing in force. The full wire shape and validation rules are in
`docs/protocol/protocol.md` ("Dispatch policy").

- **Classes**, resolved most specific first: the main conversation, a leader conversation, the background
  supervisor, `scheduled` (a scheduled task's own conversation, matched on the task's run session) or `other`. A
  scheduled conversation later made the main or a leader keeps that higher class.
- **Denied actions**: `task_plan` and `agent_start` (handing out work), `start_conversation` (`conversation_new`, so
  the limit cannot be bypassed by opening another chat), and `conversation_send` to the main conversation
  (`send_to_main`) or a leader conversation (`send_to_leader`). A send to any other conversation is not limited.
  A limited conversation also cannot cancel or delete its own scheduled task, which would remove its own class while
  the turn is still running. The scheduled class itself is anchored in the conversation origin book (`OriginTask`),
  not only the live schedule, so a run whose task was deleted or whose run session is not yet adopted stays limited.
- **Where the check runs**: only the model's tool-call entry, `Service.Tool`, and the conversation tools before
  their rate-limit bookkeeping. The hub's own internal starts (an inbox hand-off) go through `runTool` and are not
  affected, and no rule that does not name a class limits it.
- **The owner's words still get through**: a limited conversation may relay to the main conversation when the
  current turn started with the owner's own message — a user text item with a client message id and no source
  marking it as a scheduled run, a supervision message, a relay or a system notice — or when the sent text starts
  with one of the owner's messages verbatim. A transcript that cannot be read fails closed. The exception never
  applies to a leader target.
- **What the model sees**: a localized refusal starting with "The current organization policy requires…", followed
  by the rule's own note when it has one, and a matching sentence in the `task_plan`, `agent_start`,
  `conversation_new` and `conversation_send` tool descriptions.

## Explicit dispatch guard

`internal/assistant/dispatch_guard.go` is a separate, opt-in guard for explicitly named conversations. It does not
change the class-based dispatch policy above. A policy has
`{"enabled":true,"expires_at":"…","targets":[{"machine_id":"…","session_id":"…","deny":["task_plan"]}]}`;
`deny` may contain only `task_plan`, `agent_start`, `conversation_new` and `conversation_send`. Targets match the
machine and session exactly; ordinary Agent start parameters are untouched.

- **One decision for listing and calls**: `guardRefusal` runs at the model tool entry before business arguments are
  parsed. `guardTools` applies the same decision to the real `sessionMCPTools` list, and `ToolsFor` exposes the same
  filtering for `assistant.tools`. A stale client or a direct call still meets the call guard. A policy write calls
  `mcpChanges.notify`, so revision-aware discovery cannot keep a pre-change list. `--allowedTools` is an approval rule,
  not proof that a tool is hidden.
- **Defaults and fallback**: no policy, `enabled:false`, expiry, a non-matching target and an empty target list all
  leave existing behavior unchanged. Untargeted tool lists and other conversations keep their previous contents.
  Requests must name `dispatch_guard`; duplicate or unknown outer, policy or target fields, a missing/null policy and
  a non-empty policy without an explicit `enabled` are rejected without touching the Store or publishing a list
  change. Duplicate keys are rejected while walking the complete JSON, before any `RawMessage` occurrence can be
  discarded. Field names are case-sensitive: a differently cased alias such as `DISPATCH_GUARD` or `TARGETS`, which
  `encoding/json` would otherwise match to the same field, is rejected as an unknown field at every level. Only an explicit `enabled:false` or `targets:[]` clears, and empty targets are normalized to disabled
  with no expiry even when the request leaves `enabled:true`. Validation responses use localized typed errors in
  English, Simplified Chinese and Japanese. Natural expiry and recovery from a Setting read failure are included in
  the discovery cursor and rechecked at heartbeat. Each returned list and its revision come from the same captured
  effective guard decision, so a transient second-read failure cannot bind a fail-closed list to a healthy revision.
  An unreadable or invalid enabled policy fails closed for the four dispatch tools; unrelated tools remain available.
  The localized refusal contains no storage error, target, tool name, arguments, policy or credential.
- **Scope and expiry**: `GET` / `PUT /api/v1/assistant/dispatch-guard` requires the `dispatch.guard` scope for an
  extension. An extension-written enabled policy must expire within one hour; full credentials may read, write and
  clear it. The legacy every-scope grant does not include `dispatch.guard`. An unauthorized write changes neither
  the policy nor existing starts.
- **Boundary**: layer / preview and push-layer integration remain follow-up work. This slice adds no preview, client
  UI, push integration or production deployment.

## Start defaults

What a task Agent starts with beyond what its caller named can come from an optional start-defaults policy
(`internal/assistant/start_defaults.go`): rules matched by the kind of work, each saying which Agent presets to try,
which thinking effort, whether ultracode is on, whether a goal is set from the task's acceptance criteria, and whether
the prompt asks the Agent to plan first. With no policy stored, one past its expiry, or no rule that fits a task, the
task starts exactly as it would without the feature, except for one built-in default: a new Codex task Agent whose task
has acceptance criteria starts with them as its goal (below, "The goal"). A policy's values belong to the one start they
were applied to: every try of a task starts again from what the task named itself, whichever rule fits it now, and for
whatever a policy filled in (a preset's launcher, model, effort, tier and the permission mode its agent sets; the
policy's effort or ultracode) the task's plan keeps the task's own value (its why names a preset the policy picked), so
a remedy, a queued start, a retry from the card or a manager restart, which read the task back from its plan, never take
an expired policy's values for the task's own, and the card does not show them as asked for. A rule that fits but fills
nothing in leaves the start and what the plan keeps exactly as without the feature. The Agent's session shows what it
actually runs with. The owner and an extension granted the `assistant.start_defaults` hub scope read and write it with
`GET`/`PUT /api/v1/assistant/start-defaults` (the owner also as `start_defaults` in `PUT /api/v1/assistant`); an
extension-written policy must expire within one hour, so a stopped or uninstalled extension leaves nothing in force. A
rule must set something; a rule whose `then` names nothing is refused.
`GET /api/v1/assistant/worker-presets?machine=<id>` (same scope) lists the presets a computer can start. The wire shape
is in `docs/protocol/protocol.md` ("Start defaults"). On the wire the rule's match is `applies_when` and its preset
list `then.candidates`, each candidate `{"name","priority"?,"weight"?}`; the pre-convergence names `when` and
`presets` are still accepted on every decode (stored policies included), reads answer both names with the same values,
and a body whose two names disagree is refused (#11719). Candidate names are trimmed of surrounding whitespace before
validation and storage, so the dual-name read remains writable under the same body limit. Candidate names containing NUL are refused before storage.
`POST /api/v1/assistant/start-defaults/preview` (same scope, read-only) answers what a policy would do for a task
without doing any of it: the rule that fits (an omitted `task.role` is `worker`), the winning priority group's usable
candidates and the passed-over reasons (each with the agent its own launcher runs), the extras, and which of them the
caller's own values win where the rule actually offers them. The illustrative start uses the same read-only
resolve → machine fit → speed validation as a real plan edit, including explicit-hidden launcher metadata and machine
fallback, never a free-form agent input; goal, effort and ultracode follow the fitted agent: effort and ultracode only
for Claude Code, the goal through `startGoalSource` (so a rule's goal with no criteria sets nothing, and a new Codex
start takes the default goal exactly when no rule names one and `defaultGoalRefusedOn` does not exclude that launcher).
`caller.effort` is checked against the fitted agent's vocabulary, so Codex's `minimal` and `ultra` remain valid; a
launcher whose agent the hub cannot tell (no live list, nothing last seen) is not assumed to be Claude Code and takes
any Claude Code or Codex level, as the real start sends it unchanged and the node checks it. A rule's
`ultracode: false` is never applied, so the preview leaves it out. Deterministic resolve/fit refusals are 400, with coded
refusals keeping their code and data; operational store/configuration read failures are 500. An empty `rules` draft
previews the no-policy start without clearing the stored policy. It runs the same matching and candidate selection as
a real start and writes nothing — a draft policy in the body is only validated, never stored.

- **Candidate source** (`internal/assistant/start_candidates.go`): selection reads one small interface,
  `startCandidateSource`, which answers only the rule's requested presets with their availability, reason and real
  launcher-agent metadata on a machine. The built-in source reads the active model profile with the machine's launcher
  list and health, the same data `GET worker-presets` serves, so a preview and a start cannot disagree. One ordinary
  inventory read covers visible candidates; each distinct requested hidden launcher gets at most one unhidden read per
  evaluation, with usability and metadata cached per launcher for that evaluation only. A dynamic candidates source
  implements that interface and is set on the Service at construction; rule matching, priority/weight selection and
  the per-try retry reset do not change.

- **Matching**, first rule that fits wins (the goal alone is decided apart, below): `supervisor` names the class of the
  conversation that follows the task up, with the dispatch policy's classes (`main`, `leader`, `background_supervisor`,
  `scheduled`, `other`; a task leader's worker takes the class of the conversation that follows its leader's task up,
  and a task no conversation follows up matches only a rule naming no class), `role` is `worker` or `leader`, `readonly`
  matches read-only or writable tasks.
- **Applied at the task's start** (`startTaskAgent`, after the launcher fit), so retries, remedies, queued starts and
  `task_spawn` children get the same: the highest-priority candidate group with anything usable wins, within it the
  first preset whose launcher the computer has and that is not at its usage limit (weights share starts inside that
  group, rolled from `crypto/rand`; an omitted priority is 0, so a list without priorities behaves exactly as the old
  ordered list did, and a group that cannot run falls through to the next); a preset passed over is named in the
  task's why. Then the effort, ultracode on Claude Code only, and the goal on Claude Code and Codex. A caller's own
  effort, ultracode choice, tier, model or launcher is never replaced, and an effort the caller's tier gives wins over
  the policy's.
- **The goal** is the task's `done_when` as one checkable sentence (`prompts/assistant/task_goal.md`), set by the node
  after the first message is in the transcript, as Claude Code's `/goal` or Codex's own `/goal`
  ([agent-goal.md](../integrations/agent-goal.md), "A goal set at the start"). When Codex's `/goal` cannot be set
  (an older Codex, a screen not ready, an unconfirmed goal), the node delivers the same sentence as an ordinary
  message and reads it back from the rollout instead; no delivery outcome fails the start. A computer whose node
  cannot set it starts the Agent without it once, and the task's why says so, as for ultracode. Only a new start (never a resume) of
  a task with `done_when` takes one (`startGoalSource`). The goal is decided apart from the rest of the rule: the first
  fitting rule that names `goal` decides, even when an earlier fitting rule silent on `goal` supplies the presets,
  effort and the rest. `done_when` sets it on Claude Code and Codex, `off` on neither. When no fitting rule names
  `goal` (no policy, no rule that fits, or every fitting rule silent on it), a Codex start gets the `done_when` goal by
  default and a Claude Code start gets none. Codex has no Stop hook, so without a goal it stops after one turn and only
  the supervisor's rework brings it back (#10510). To turn the default off, add a rule with `"then":{"goal":"off"}`:
  with an empty `when` and placed after the rules that name a goal for some tasks, it turns the default off for every
  other task, without hiding any other rule's presets or effort; clearing the policy brings the default back. The
  default is never sent where the hub already knows the start cannot take a goal (an engine runtime such as Paseo, the
  Claude desktop or Codex app launcher), and when a node refuses it anyway (a screenless host, an older node) the
  Agent starts again without it and the refusal is only logged, not put in the task's why: the owner did not ask for
  that goal. Giving Claude Code the same default is a possible follow-up.
- **Plan first** adds `prompts/agents/plan_first_task.md` to the prompt: for a launch whose model does not act on an
  effort setting, the prompt is the only lever.

## Chat search (#6282)

Deleted chats (#6172) are excluded from titles, message hits and index progress. Overlapping manager instances use fresh database deletion checks; tombstones are merged one entry at a time, and the settings-row transaction lock orders index commits before atomic deletion and cleanup. A previously cached empty map cannot restore an entry or overwrite another deletion. Deletion removes their search rows and cursor before acknowledging success; a retry finishes interrupted cleanup. Index writes cannot recreate a tombstoned chat, and restart cleanup does not wait for the normal missing-chat grace period or for a nonempty inventory. Saved task briefs and knowledge remain separate. A search already serialized when deletion wins at the hub returns `409 search_changed` with `retryable: true`; retry the same query. Unchanged multi-chat replies remain unchanged.

The apps find an assistant chat by what was said in it through `GET /api/v1/assistant/search` (`docs/protocol/protocol.md` ("Assistant chat search (#6282)"), the design in `docs/design/proposals/assistant-search.md`). The manager answers it from an index in its own database (`internal/assistant/search_index.go`, `search_routes.go`, and the text rules in `chatsearch/text.go`; tables `assistant_search_items` and `assistant_search_cursors`, migration `0511_assistant_search.sql`).

- **What it keeps**: the user's own words and the assistant's replies of the chats "All chats" lists (archived ones too, never the health space), as the chat shows them: the shared rule of `spec/fixtures/chat-search` (its `index`). No thoughts, tools, system events, quoted contexts, hidden tags or messages Persona injected for the model; a card is its fallback text. A reply is kept without Markdown's markup (`searchPlain`, #7072): emphasis, code backticks, link and image syntax, heading, quote and list markers, task boxes, table pipes, rules and code fence lines go, their words stay, so a snippet never shows a marker and words the markup split are found together; the user's words stay as typed. Bold that holds code, spans two lines or has no partner loses its `**` too, and markup around code pairs across it; a `**` that reads as an expression (`2 ** 3`, `a**b`) stays (#7195). Migration 0513 emptied the index built before, as `assistant search-reindex` does; 0514 instead rewinds only the chats whose indexed replies may still hold markup, to just before the first such reply, so they are read again from there while the rest stays found.
- **Keeping up**: `searchLoop` (started with the service) sweeps when the computers' session lists change (`setSessions` wakes it) or every 30 s. A chat whose `rev` moved is read after its cursor (`session.items` with `after_seq`, unfolded, about 500 items a page); a working chat at most every 5 s. Chats never read are read newest first with a pause between pages, and the cursors let a restart go on from there; until then searches say `partial`. A new `history` token (the transcript replaced or rewritten) drops the chat's rows and reads it again from 0. A read that fails (the computer offline) waits a minute. A chat missing from the list for a day leaves the index (never while the list is empty: the computer may be offline).
- **Searching**: a substring scan of the folded text (NFKC, lower case), every word in one message, at most 3 newest hits a chat (`db.SearchAssistantItems`), with a 2 s limit; titles are matched in memory. No `pg_trgm`: the proposal's measurements show two-character Chinese queries cannot use a trigram index, and a personal install's text scans in tens of milliseconds.
- **Privacy**: full devices only (a read-only role gets 403), not an extension scope, not a tool the model can call; the query is never logged. The rows go with their chat; dropping the manager's database drops them.
- **Reading it again** (#6340): `personad assistant search-reindex` (the built-in manager; `persona-manager assistant search-reindex`) or `personad manager search-reindex [--dir D]` (a manager made in the app, `--dir` left out when the computer has one) empties both tables in the database the manager runs on and counts the reset (`db.ResetSearch`, the manager setting `assistant_search.reset`). The built-in manager's database is its own once its data moved there, else the hub's (`builtinDatabaseNow`, as `builtinDatabase` decides at its start); another manager's is its `manager.json`'s, never the hub's. A running manager sees the count move at its next sweep (`searchLoad`): it empties what it wrote meanwhile from its old cursors, drops the cursors it held and reads every chat again from the start, so it need not be stopped; a stopped one reads them when it starts. A database without the index (a manager from before chat search) is left as it is. There is no separate `--purge`: the manager always fills the index again from the transcripts, so emptying it and rebuilding it are the same act.
- **Not yet**: the earlier stretches of a chat that moved between Claude Code and Codex are not indexed, only the session it runs in now.

## One memory

The assistant has one memory, the one the Memory page shows (#9440). Claude Code keeps auto memory under the config
directory, and assistant conversations run in more than one Claude profile ("Claude assistant profile compatibility"
above), so without this each profile would keep its own.

- **The directory** (`node/memory.go` `memoryDir`): for the assistant's own directory it is always
  `<the node's default Claude config directory>/projects/<encoded assistant directory>/memory`, whichever directories
  exist. The Memory page, Codex and the other runtimes (`memory_dir`) and dreaming resolve it there.
- **Claude Code**: every assistant start's settings carry `autoMemoryDirectory` with that directory
  (`assistantCompactionSettings`). Claude Code takes the key from `--settings` before the profile's own settings, and
  knows it from 2.1.74; an older one ignores it and keeps writing to its profile's copy, which the merge below still
  brings in. Writes to `.md` files in the directory need no permission, as in the default location (read from Claude
  Code 2.1.325's permission rule; not measured in a live conversation).
- **Not changed**: a guest conversation and a meeting's room get no directory and keep auto memory off. The Health
  space keeps its own memory where Claude Code puts it (`docs/assistant/health.md`), with no new setting and no merge.
- **The copies other profiles hold** (`node/memory_merge.go`) are merged before the directory is handed to a
  conversation, the Memory page or dreaming, under the lock the memory API takes. The check runs each time (under a
  millisecond with twenty notes), because a conversation that was already running keeps the settings it started with
  until it restarts. A note only a copy has is copied in with its modification time and its `MEMORY.md` line. The
  same file name with the same bytes is left. The same file name with different content keeps the one modified
  later. Notes with different names about the same matter are both kept: reconciling them is not this merge's job.
- **Nothing is deleted.** The other copies are never written. Each run first saves the memory as it was (`before/`;
  a memory directory that is itself a link is followed, so the backup holds its notes),
  then for each note the copy's version (`copies/<n>/`) before it is merged and the version it took the place of
  (`replaced/<n>/`), in `<data dir>/memory-merge/<encoded directory>/<UTC time>/`. The run's manifest records each note
  as soon as its merge, backups and `MEMORY.md` line are done. A note whose bytes a run has recorded is skipped, so each
  version is compared once: a note deleted or changed on the Memory page does not come back, a second run does nothing,
  and undoing a merge by hand (copy `before/` back over the memory directory, older times and all) stays undone.
- **Failures**: a note that cannot be merged is not recorded and is tried again ten minutes later, by itself; the notes
  after it and the other copies are merged meanwhile. A run interrupted part way leaves its unrecorded notes to the next.
- **Concurrent writers**: the model writes memory with its own file tools, outside the node's lock. The merge creates a
  new note only if the name is still free, replaces a note only if it has not changed since it was compared (time, size
  and bytes, read again just before the rename), and re-reads `MEMORY.md` just before replacing it. A new note is
  written and synced under a temporary name first and published by a hard link, or a rename that refuses to replace
  where there are no links, so it appears whole; a file system with neither gets it written in place, and a failed
  write takes it away again. A note still changing after three attempts is recorded as the memory's only if the
  memory holds a version not older than the copy's; if not, it is left for the next check. A `MEMORY.md` line
  belongs to the note its link points to (the whole file name), not to a note its description mentions.
- **Telling the user** (`assistant/memory_merge.go`): every ten minutes the manager asks the node for its runs
  (`assistant.memory`, op `merges`). For a run it has not told about (`persona_settings` key `assistant.memory_merge`, the
  last run told for each computer, so moving the assistant to another computer and back tells only what is new) it
  writes `memory-merge.md` into the memory directory in the user's language and sends one P1 push. The report lists
  the notes brought in and, for each conflict, both modification times, which version was kept and the full text of
  the other. It is not in `MEMORY.md`, so no conversation loads it; the Memory page lists it like any note, and the
  user deletes it when done. A node that does not know the op answers an error and nothing is told until it does.
- **Rollback**: without the setting each profile reads its own directory again, exactly as it left it.

## Memory dreaming

Set `"assistant": {"memory_dreaming": true}` in the assistant manager's `manager.json` to enable a background pass inspired by
[pi-memory-dreaming](https://github.com/yukukotani/pi-memory-dreaming). Every five minutes the manager checks whether the main
chat on the selected personal computer is idle. It makes at most one attempt per day when it finds new user-authored text
in the latest eight turns. It sends those messages, the memory index and the previous dreaming note to Persona's configured
Data processing model. The pass excludes Health, other conversations, tool output and automated messages.
It reads eligible messages from oldest to newest, up to 12,000 bytes of user text per pass. Long messages are sent as
marked excerpts of up to 3,000 bytes. A message that does not fit in the remaining batch waits for the next pass
while it is within the latest eight turns.

The model consolidates durable preferences and explicit corrections into `dreaming.md` in the existing assistant memory,
visible and editable in the app. It may return no change. The node updates the dedicated note and its index link only if
the note and index still match the version reviewed by the model. The assistant stores the last attempt time and checkpoints processed messages so they survive a restart. If the transcript is replaced, the new history starts a fresh checkpoint.

## Codex (2026-11-01, `codex app-server`)

Selecting a Codex launcher in Settings › Assistant (or top-bar menu / scheduled tasks) replaces `claude -p` with Codex's persistent IDE JSON-RPC service,
`codex app-server` (stdio, one JSON line per message; protocol from `codex app-server generate-ts [--experimental]`, codex-cli 0.159.3 on hub machine).
Code: `server/internal/node/assistant_codex.go`; `assistant_agent.go` chooses Claude / Codex from launcher-probed agent (`launcherProbeCached`). Assistant-capable runtime launchers go to that runtime (next section); other types (internal Codex variants…) reject with explanation.

**Startup**: node runs the launcher function in interactive zsh, replacing `codex` with `codex <launcher arguments> app-server <Persona -c>` (`codexShell`,
including `command codex …`), retaining environment, `-c model_provider=…`, `-m`. One app-server per chat, one thread inside;
thread id is session id (assigned by Codex, known only after new-chat `thread/start`), rollout is history (`docs/integrations/codex.md`), App displays normally.
Handshake: `initialize` (experimentalApi) → `skills/extraRoots/set` → `thread/start` / `thread/resume`.
Assistant conversations with no selected permission mode now default to Allow Everything. An explicitly selected mode continues to use the native mapping below, including managed restrictions. An explicit mode, including Default, still uses the mapping below; transcript policy readback is not treated as a selection. Inherited workspace policies keep their own network and writable-root settings, so memory access outside those roots may require approval. Existing persisted choices remain explicit.
Snapshots accompany each message as Claude does (first `turn/start` / `turn/steer` input); `developerInstructions` holds memory, any split instruction remainder and the authorized supervision policy.

| Capability | Claude (`claude -p`) | Codex (`codex app-server`) |
| --- | --- | --- |
| Streaming | `stream_event` text_delta | `item/agentMessage/delta` → same `session.delta` |
| Continue / after restart | `--resume <id>` | Reuse live process, otherwise start a new one with `thread/resume` (verified after hub / node restart) |
| Stop | Kill process, next message `--resume` | `turn/interrupt`, retain process, then `thread/backgroundTerminals/clean`; interrupt alone doesn't stop commands (measured `sleep 120` remained backgrounded) |
| Interject during reply | Queued message absorbed mid-turn | `turn/steer` into current turn (same effect, verified within its reply); start a turn if it just ended |
| Persona MCP | `--mcp-config` + `--allowedTools mcp__persona` | `-c mcp_servers.persona.*`; Codex asks via `mcpServer/elicitation/request` (`codex_approval_kind: mcp_tool_call`); node always allows compatibility server `persona`. `tool_timeout_sec` one day (default 60 seconds times out ask_user phone waits) |
| ask_user | Blocking MCP | Same tool, `wait: false` included (the answer then arrives as a message, "Asking without holding the turn"). MCP starts before thread id exists; environment carries `proc-…` key, replaced by node with session id on call (`resolveAssistantKey`) |
| Native request_user_input | — | `item/tool/requestUserInput` → phone question card, answers keyed by question id |
| Command / file permissions | PermissionRequest hook → phone | `item/commandExecution/requestApproval` / `item/fileChange/requestApproval` / `item/permissions/requestApproval` → same phone `ask` |
| Lark write confirmation | Bash PreToolUse (`larkgate.go`) | When command approvals are enabled, `larkCheck` classifies the original command through the same recursive wrapper and shell classifier as Bash PreToolUse: reads allowed, writes show Lark card, token access denied. Full access skips command approval callbacks; connector proxies and gated CLI wrappers still enforce business confirmations. Codex decline has no reason; phone feedback / gate explanation reaches model through `turn/steer`, shown as "(Not executed)…" |
| Permission mode | `--permission-mode` / control requests | Each `turn/start` uses `codexPolicy`: Default / Accept edits = `untrusted` + workspace-write with network; Auto = `on-request` + workspace-write without network, escalation for sandbox escape / network passes gate; Plan = `untrusted` + read-only; Allow Everything = `never` + danger-full-access. The same mapping applies to `thread/start` and `thread/resume`. Switches apply at the next `turn/start`; an active turn keeps its existing policy. Persisted per-session choices win over older rollout settings, including historical `untrusted` + danger-full-access turns. No machine defaults or other session settings are rewritten. Managed policy restrictions can still reject native settings and are returned as errors |
| Effort | `--effort` / control | `turn/start` `effort`, low…max unchanged, gateway support depends on model |
| Images | base64 image blocks | Files in `~/.persona/uploads/<date>/`, `localImage`; rollout path becomes App thumbnail |
| Instructions | Bounded shared `CLAUDE.md` plus the complete remainder in `.claude/rules/`; authorized supervision policy via `--append-system-prompt` | Bounded shared `AGENTS.md` (identical to `CLAUDE.md`), with the complete remainder supplied through `developerInstructions`; authorized supervision policy uses the same channel; also user `~/.codex/AGENTS.md` |
| Extension skills | `.claude/skills/<name>` | Same directory as extra skill root (same format, visible in `skills/list`); gated commands on PATH, credentials in node proxy (previous environment tokens required `shell_environment_policy.ignore_default_excludes=true`, or Codex stripped `*TOKEN*` from command environments) |
| Memory | Assistant-specific Claude auto memory | **Same files** (chosen 2026-11-01): `developerInstructions` gives directory, one-file-per-entry + `MEMORY.md` format, current index (`codexMemoryPrompt`); directory added to sandbox `writableRoots`. apply_patch and memory-only reads (cat / head / rg…, `readsOnlyMemory`) don't ask, but Plan writes do. Disable Codex's machine-wide rollout-derived `~/.codex/memories` for assistant (`features.memories=false`) |
| Background / Monitor | `background_tasks_changed`, claude wakes on completion | Read `thread/backgroundTerminals/list` after every turn, record tasks and retain process, report lost tasks after restart; **completion doesn't wake chat**, no Monitor |
| Prewarmed spare | Yes | Yes, pre-start thread reused (measured 50 ms return) |

**Bounded instruction files (#14425).** The manager's rendered text remains the complete, unchanged source, including the user's About me bytes. At 19 KiB or below the node writes the historical one-file form. Above it, the identical `AGENTS.md` / `CLAUDE.md` entry holds the first lossless UTF-8 chunk and every later chunk is written twice: as a flat `.agents/rules/persona-assistant-NNNNNN.md` file with `trigger: always_on` for Antigravity, and as an unconditional `.claude/rules/` file for Claude Code. Every generated file stays below 20,000 bytes, leaving headroom under Antigravity's documented 24,000-byte per-file truncation boundary. Concatenating the entry and chunks in numeric order reproduces the manager's bytes exactly; generated envelopes add no replacement or summary. Codex receives the exact remainder through `developerInstructions` on both `thread/start` and `thread/resume`. Other generic runtimes receive it as context on every non-command message because their native rule-directory behavior is not part of the runtime contract. The instruction digest includes the remainder so a later-section change restarts Claude just like an entry change. Cleanup removes only numbered files carrying Persona's generated marker; a same-named user file is preserved and blocks replacement rather than being overwritten. Older nodes still receive the complete `claude_md` field and retain their previous behavior.

User-visible differences:

- **More prompts in Default**: Codex `untrusted` allows only its own read-only command list; even `timeout 20 head …` asks on the phone (Claude file reads don't). Use Auto if too many.
- Background completion doesn't wake chat; use `schedule_followup` to revisit.
- Denial reasons appear as "(Not executed)…" messages (native Codex denial carries no text).
- User `~/.codex/config.toml` MCP servers, `hooks.json`, `AGENTS.md` load too, like Claude user settings; other MCP calls need phone approval outside Allow Everything.
- The ChatGPT apps of a ChatGPT sign-in (`codex_apps`, every connector of that account) do not: the assistant's Codex starts with `features.apps=false` (#10275). Code mode declares every tool in full in each request (in its `exec` tool's description, nothing loaded on demand); on one install the apps were 295 tools, ~140k tokens of a ~195k request, and the same request without them ~51k (captured from `codex exec --ephemeral` against a local mock, no model called). The assistant has Persona's own tools; the computer's own Codex keeps its apps.

Measurement (2026-11-01, isolated hub: fake HOME, separate port / database, real Codex launcher and Lark gateway): new chat (snapshot, `agents_list` no approval), resume,
ask_user answer, `ol GET /me` allowed, `ol POST …/messages` Lark card denied (model stopped without retry, no send),
Allow Everything commands without prompts, same-turn interjection, immediate interrupt stopping `sleep`, context retained after hub + node restart, effort / mode restored, streaming around 120 ms per segment,
image color recognition, prewarmed spare. Memory follow-up that day: preset "preferred tea" read in Default without a card;
"peanut allergy" created a file / updated index, memory API listed it, new chat answered from index without file read, no writes to `~/.codex/memories`. Unit tests: `assistant_codex_test.go`, fake app-server replaying actual frames for handshake, turn/start / steer / interrupt,
approvals / gate, request_user_input.

## Agent runtime (DSH) (2026-11-02, issue #282)

Selecting a launcher declaring assistant capability (currently DSH plugin `dsh`, `launchers.list` `assistant: true`) runs chats in that runtime:
node `server/internal/node/assistant_runtime.go` drives `session.start` / `session.send` / `session.interrupt`,
with live snapshot (`context`), permission mode (`mode`), memory directory per message. Content, streaming, permissions, and questions use existing runtime paths (`docs/integrations/runtimes.md` ("Assistant")).
This layer contains no runtime-specific code; any runtime declaring `caps.assistant` and implementing the contract can serve as assistant.

Same as Claude / Codex: live snapshots, `ask_user` / Persona tools (assistant chats only), all permissions / questions go to phone first, permission modes, shared memory
(first message includes directory, format, index via `prompts/assistant/runtime_instructions.md`), assistant instruction files / skills (`.agents/skills/`),
AI twin / Lark `/agent` / scheduled tasks (all `assistant.send`).

Differences:

- **Lark**: credentials never enter runtime. Model uses extension-gate declared tools (`proto.CommandGate.Tool`, arguments are command words); node applies `larkCheck`, then
  runs skill scripts locally, credentials only in node proxy. Direct shell commands can't obtain credentials or bypass gates.
- **Model / effort** selected in runtime (DSH Web GUI); model configuration doesn't select a specific model for these launchers. **Prewarming** unnecessary (persistent runtime).
- Closing stops only the current turn, leaving the chat in runtime. Changing between runtime and Claude / Codex requires a new chat; another launcher specified on send still goes to the original runtime (`assistantResumeLauncher`).
- Disconnected runtime (`dsh web` not running) returns `runtime_away` without sending anything.

DSH details (permission preset mapping, persistent `dsh web`): `docs/integrations/dsh.md` ("Assistant"); end-to-end `integrations/dsh/e2e/assistant.mjs`.

## Profile and avatar

- Persona tab top-right avatar opens `AssistantProfileView`: identity / machine, Online / Working, three "Up Next" items,
  proactive work (scheduled tasks, run history), knowing you (About Me, memory), settings (Skills, Run on, Working directory;
  next-message suggestions on a supporting hub, off by default and hidden when the field is absent; model in Settings › Model
  Configuration › Main chat, #1291), accessible data. Settings › Assistant opens the same page.
- Scheduled tasks: "Up Next" timeline (large times on the left, like Reminders), "Scheduled tasks" (daily / interval, natural rules "Every weekday at 09:00", "Every 30 minutes"; a daily rule's time is 24-hour in every language, like the next run under it and the web, #7499),
  "One-time follow-ups"; rows show icon, next run, nondefault execution ("claude-fast · Auto · Low effort", Allow Everything red), last result, switch; swipe delete, long-press Run now,
  recent runs. Shared editor: repeat (Once / Daily / Interval), prompt, execution (model, permission, effort), Continue original chat (toggle off for new chats each run),
  Run now, history.
- Task-created chats live on Scheduled tasks (#2838, same web #2824 rules, `AssistantTaskChats`): iOS / Mac Recent excludes them (except the open chat,
  which keeps a row; pinned unchanged). Each task shows up to 3 links like "Today 09:03". Identify by `run_session_id` / `last_session_id`
  and `/assistant/runs` `session_id`, excluding task `session_id` (continued originating chats are user-owned). History retains 200 runs; older chats match hub titles
  `MMDD | Scheduled | Task name` (mutual prefixes accepted for truncated names; Scheduled alone isn't enough, since model can classify user chats that way). Reread tasks / runs on sidebar appearance and chat-count changes.
- Avatar is SwiftUI (`AssistantAvatar`), not bitmap. Default since 2026-11-03 is **Mochi** (white waving dumpling in the App icon, icon colors independent of theme; `docs/apple/persona.md` ("Built-in drawing"));
  the alternatives below follow theme accent. Four earlier candidates (`-tab assistant -openAssistantPage avatars` compares):
  **Visor** (default before 2026-11-03, now optional: accent disc + dark mask, two glowing eyes, upper-right spark echoing App icon; working eyes scan side to side),
  **Circuit** (B2 selected from tech-style icon candidates 2026-10-31, made optional not default 11-01: on a dark accent circle, traces from three left pads
  flow into a two-eyed core, representing computer Agents converging in Persona; working signals flow along traces),
  Spark (App-icon spark in a dark circle, too similar to the icon and button-like small), Halo (broken HUD ring + glowing core, cool but cold, details blur at 28pt).
  Not beside messages (full-width assistant replies would lose a column), nor notifications (requires Notification Service Extension + communication notification, separate work).
- Since 2026-10-31 avatar / name can change (Settings › Appearance › Avatar, `docs/apple/persona.md`; avatar colors used only with "Match Avatar" theme). Visor is default, Circuit next and selectable. Both animate by top-bar state:
  idle blinks, thinking looks up with thought bubble, working Visor scans / Circuit signals flow along three traces, Needs you red breathing outer ring, offline gray.

## Android entry points

Android management lives in Settings › Assistant (`docs/android/android.md` ("Assistant settings")): Settings "Assistant › Persona", Persona title-menu "Assistant settings", top-right avatar, and drawer "Profile" all open it. Content: Run on (machine, working directory, machine switch; launcher / model in Model Configuration › Main chat, #1291), data location, knowing you (About Me, memory), timezone (phone default), accessible data; proactive work (scheduled tasks, follow-ups, run history) is separate, added here after integration.

Unlike iOS, Android can switch the assistant machine. Warn "Memory and chat history stay on the old computer and won't follow", and send `"main":"-"` in the same `PUT /api/v1/assistant` to clear main chat. hub doesn't change `main` on machine changes; without clearing, the App combines old `main` with the new machine and seeks nonexistent history.

## Tests

Running them is in `docs/operations/testing.md`; the scheduler's tests are in `server/internal/assistant/schedule_test.go`
(`TestNextRun`, `TestParseWhen`, `TestScheduleAcrossHubRestart`), the app's in `AssistantScheduleTests`, `AssistantRoutinesModelTests`
and the UI suite `AssistantProfileTests`, and the 2026-10-26 to 2026-10-30 measurements in `docs/archive/assistant/assistant-records.md`.

A user can ask main chat to cancel and archive an Agent stuck at a terminal confirmation. `agent_archive` with
`force: true` closes that blocked Agent and rejects or cancels its pending confirmations, without approving a tool
or plan. It preserves the transcript and refuses other busy states. Normal archive behavior is unchanged.

### Assistant permission defaults

Non-guest assistant conversations start in `bypassPermissions` when the manager has no configured default. Main chats, leader conversations and the assistant's own scheduled-task chats use this default; ordinary Agents dispatched with `task_plan` keep their existing task permission selection. `default_permission_mode` in the manager settings overrides it for new conversations and stopped Claude Code resumes without an explicit conversation choice. A conversation or task with an explicit mode keeps that choice, a live process is not interrupted by a default change, non-Claude launchers retain their legacy resume precedence, and guest tool restrictions remain unchanged. Resumed turns with no persisted selection also use bypass, including `schedule_routine` and `schedule_followup` turns.

The command gate honors bypass for classified reads and ordinary writes without creating a tool permission card or persisting an Always grant. It still confirms outbound sends as the user (messages, replies, comments and mail), deletion and writes it cannot classify. It extracts individual calls from compound commands when possible; all must be classified as reads or ordinary writes to bypass confirmation. Proxy-authenticated writes must be standalone, parseable calls so credentials remain isolated. The sent-as-user ledger records `let_through.kind: "permission_mode"` for a proxied call allowed by bypass. Credential guards, explicit deny rules and the health space's separate confirmation rules still apply. Ordinary Agent sessions retain their existing permission behavior.

Bypass controls tool execution, not authorization for an action. The assistant must use `ask_user` before production writes, external sends, deletion or spending unless the user has already authorized that exact action. These questions remain pending until the user answers; bypass does not answer them or make an irreversible recommendation take effect at a deadline.


### Main conversation execution on the web

The welcome and main conversation headers, and Settings › Assistant's **Runs on** group, read
`GET /api/v1/assistant?view=main-runtime`, with the same manager selection and access as the ordinary assistant view.
Its `machine_id` and the optional `main_machine_id` in the ordinary view are routing identities. The main route is kept separately from the default for new conversations.
Execution location is reported separately: a native process uses its execution environment's hostname; a deployment
can explicitly name its container via `PERSONA_EXECUTION_CONTAINER` on that node. The hostname alone does not prove
the presence of a container. An external runtime can opt in with `caps.execution` and answer
`session.execution {session_id}` with `{state, location?: {host?, container?}}` for that exact session. The connecting
node is never substituted for the runtime's execution location. Older nodes and runtimes can leave the location
unknown. A stopped or offline main can show its last observed location; a missing observation remains unknown.

The header line is for everyday people (super rules 0 and 4): the product's name for the conversation, **Main chat**
(`主对话`, `メインチャット`), as the sidebar and Settings call it, and its state in plain words: "Main chat · Running",
"Main chat · Offline", "Main chat · Stopped" or "Main chat · Starting…". Where and with what it runs (the execution
host or container, a proxy, Claude Code / Codex) is engineering detail, so it is not on that screen:
Settings › Assistant's **Runs on** group lists it under the computer, as **Main chat runs in** (the container or host;
"Last ran on <location>" while stopped or offline; "Via <proxy>" for a proxy without a location, never named as the
location) and **Main chat runs with** (the harness). What is not known is left out rather than reported as missing: no
location, no row for it. When nothing about the main chat's execution is known (an older hub without the view, a failed
read, a report for another conversation or routed through another node, the `unknown` state) the header line is not
drawn at all and Settings adds no row. The line used to read "Main conversation · Runtime not reported" to everyday
users, and named the host, container and harness when they were known (web usability audit 2026-11-14, item 80;
`web/src/lib/main-runtime.ts`, `web/src/views/assistant/MainRuntimeBadge.tsx`,
`web/src/views/settings/AssistantSettings.tsx`).


### Hiding ambient Agent sessions

For people using Agents with noisy workspace trees, `agents.hidden_dirs` holds reversible per-computer Agent
folder rules, shared across runtimes: the parallel list to `agents.blocked_claude_dirs`, with the same pattern syntax
and get / add / remove. Each rule blocks (the default for a new rule: that computer stops collecting the sessions, so
nothing of them reaches Persona, #10053) or hides (nothing stops being collected). Rules are kept with each computer
in the devices layer, live in advanced Agent settings and default to empty.
For a hide rule, and for a block rule on a node that does not block yet, the manager retains all sessions but
excludes matched ambient sessions from Assistant observations and reminders. Open supervised task agents remain visible, preserving the supervisor's completion and approval
flow; after supervision ends, the rule applies again. See `docs/protocol/protocol.md` ("Agent directory visibility")
for matching, persistence, app inspection and the confirmed `persona_settings` contract.

## Durable assistant history (#11936, phase 1)

For people using Assistant, history remains available from the manager's database when a runtime is away. The
manager stores normalized messages from main, supervisor and side assistant conversations, archived conversations
included. Ordinary coding-Agent sessions are not enrolled: their project transcripts need a separate retention,
access and quota decision. Runtime kind does not decide enrollment.

Database-first history is enabled when the `assistant.history` setting is absent. The full-role advanced endpoint
`GET /api/v1/assistant/history` reports its effective `enabled` value and aggregate `stats`; PUT with
`{"enabled":false}` is the rollback switch, and `{"enabled":true}` resumes collection and catch-up. This setting
changes neither a launcher nor a model. An in-flight ingestion transaction may finish during rollback; later pages
check the setting again. Rollback retains the tables and original runtime files; it never removes runtime history.

The manager pulls unfolded pages after a durable source cursor, woken by session revisions and item notifications,
without subscribing to every chat or requiring an open app. Each sweep reads at most eight pages of a conversation;
the next sweep continues a long import. A periodic source probe also detects rewrites with unchanged byte counts.
Notifications are only hints: replaying an old notification cannot restore an abandoned generation. Covered ranges,
the known source end and item completeness are distinct; a page answering a cursor past the source's end records
that end, never the cursor. Missing older ranges fall back to the runtime; a cached
offline page says `partial` when coverage or freshness is uncertain. Source failures never manufacture complete
empty history. The disposable text-search index can be rebuilt without deleting durable messages.
Legacy conversations on current nodes are imported lazily using the same source IDs. A node/runtime that omits
the protocol's history-generation token keeps the runtime read path; the manager does not invent durable coverage
that could mix a rewritten transcript with an older one. It can be indexed once its normalized source contract is
available. This is a source-data compatibility fallback, not an engine-kind restriction.

Historical reads and main handoffs prefer covered database content. Handoffs retain the source engine's stored
compaction summary and bounded recent complete messages; no model is called to populate this store. An incomplete
cached window or handoff tries the source first; a source failure retains available data marked `partial`.
Handoffs require both covered history and a verified summary checkpoint before bypassing an available source. Strict source
exports and user-input position projections still use the runtime. A stale source revision is checked before old
coverage is reused. Database failures retain the runtime read path.

Each stored item has a 64 KiB serialized JSON limit enforced in the application and database. Raw JSON bytes retain
tool parameters and output, including escaped NUL. Larger tools/attachments or nested metadata keep bounded
previews, provenance and a source item reference marked incomplete. Lazy attachments and runtime-truncated text
are also incomplete; a reference is not a promise of offline access. The existing full-item/image routes recover
source content when available. Complete stored copies cannot be downgraded by replaying a preview.

`stats.conversations` counts stored runtime histories, `items` counts distinct source item IDs within them,
`payload_bytes` counts stored normalized-item JSON bytes, `summary_bytes` counts source compaction-summary bytes,
and `incomplete` counts item references whose complete content is not stored. These are not tokens, physical
database size, WAL or backups. Phase 2 introduces logical conversations with several runtime segments.
Deleting an assistant conversation deletes its message rows, coverage, compaction summary and references in the
same transaction as its deletion marker. Concurrent or late ingestion cannot recreate deleted content, and a read
that overlaps deletion discards its response before returning it.

## Logical assistant conversations

A conversation keeps its public machine/session identity, task origins and supervision role while its execution
moves between engines. The manager stores physical segments, source generations and frozen physical line ranges in
Postgres. Integer transcript cursors compose those ranges; only later segments namespace item IDs. Reads, full/image
references, live events, subscriptions, reconnect backlog, inventory and the derived search index use that same
logical projection. Retired tails remain physical history and never shift later offsets.
A source that renumbers its lines under a new history (agy, when a turn's export replaces its live steps) can leave
a reader's cursor past its end. The projection reads the source's answer to that cursor as an empty span at the
source's end, so the page, live event or reconnect backlog still reaches the reader with the conversation's new
history, and readers, the search index included, start over instead of failing. The search index also starts over
after a read that the history moved under on every try (#14354).

When configured device entries for one computer are absorbed into another entry, authenticated callbacks resolve
through the current physical fleet aliases. Durable segment/history keys and the public conversation identity stay
unchanged. Conflicting roots fail closed; absorbed aliases cannot reveal private segments or bypass deletion markers.
Provider scopes still compare physical computers, independently of retained public transport identities.

The explicit `assistant_segments` operation is separate from ordinary model picks. It prepares an empty execution
session, freezes the source, and activates the target through a database epoch comparison. Pending approvals or active
work make a switch retryably busy. Permission mode carries over; unknown engines and unsupported supervision policy
are refused before preparation. The first queued input carries policy and handoff together, with the original client
message ID. Source compaction summaries take priority; the fallback uses the manager's existing model chain and waits
through provider pauses. When knowledge processing has no usable model (`model_unavailable`), retrying cannot help, so
the switch goes ahead without a new summary: the recent turns and any earlier summary carry over, marked partial.
Bounded or incomplete history is marked partial.

The source stays recoverable until the target accepts its first turn. A definite refusal restores the source at a
new epoch, invalidates the abandoned target's history projection and releases the unsent queue there; an ambiguous
start/send requires a positive receipt and is never blindly
repeated. Retired callers lose manager tool authority immediately. Physical approval/timer work must settle before
activation; the node's retirement latch prevents recovery and idle-compaction work on that segment. Closure is retried
when offline nodes reconnect. Deleting a conversation cascades through segment history and queue content, retaining
only deletion markers and minimal offline closure identities.

Each manager advances at most four conversations at a time, rotating across the remaining work on later sweeps.
Preparation and receipt calls run after Postgres releases its connection. The durable start claim and operation ID
prevent duplicate starts, and mapping the candidate rechecks the current operation and epoch under the row lock.
If a cancellation or deletion wins during a node call, the sweep uses the saved candidate identity to close the
unused session.

`conversations_switch` and `POST /api/v1/assistant/conversation-switches` coordinate an explicit set of these logical
switches without adding another engine protocol. The settings-backed batch stores each conversation's before-image,
target, expected epoch and operation ID before starting anything. Busy items wait for their current turn and pending
work; no interrupt or synthetic message completes a batch. When an activated batch item has no durable input pending,
the coordinator accepts the completed switch immediately and keeps its handoff for the conversation's next input
(#14488): that input goes through the queue, which attaches the handoff once, so the target still learns what went
before. If real input is pending, the first-turn acceptance/refusal rules above remain unchanged. A first-turn batch
(`first_turn`, the ones the usage-limit fallback makes, `docs/assistant/agent-work.md` "Usage limits: a backup
launcher") is never accepted idle: its activated items wait for their next input, which carries the handoff, and that
input's acceptance or refusal settles them. A late preparation failure cannot return an activated operation to
`in_doubt`; reconciliation continues from the activated state. The existing history loop and session-list changes wake
queued items after a restart.

A switch that has not activated its target (`preparing`, `prepared`, or a preparation whose receipt stays unknown,
`in_doubt` at the expected epoch) is bounded (#15317). Its retries keep the latest failure on the operation; 30 minutes
after the operation started (`logicalSwitchPrepareLimit`, counted from its creation, so retries and restarts do not
extend it) the sweep rejects it through the ordinary `rejected` outcome. The source stays active at the same epoch, a
prepared target is abandoned and closed, the source's node is told it is not retired, and a batch item waiting on it
becomes `refused` with a reason naming the phase and the last error. The cancel shares the conversation's row lock and
epoch fence with activation, so an activation that wins is left alone. Activated switches, first-turn delivery and
ambiguous input delivery are never timed out.

The batch response distinguishes queued, switching, switched and refused items; one refusal does not cancel independent
items. `restore_batch_id` creates a new, itself-restorable batch toward the original per-conversation settings rather
than a global default. It only includes completed items whose current epoch and execution still match that batch's
target. An item still `switching` whose operation has not activated is restored by cancelling that operation instead:
only while the conversation is at the item's expected epoch, on its saved before-image, with the operation's source
still active. The restore item (`cancel`) is saved with its batch before the cancel runs, so a restart finishes it
once; the restore batch reports it `switched` with a reason, the original item becomes `refused`, and nothing
is prepared. If the switch activates first (or while the MCP confirmation is open), the item is refused and the switch
finishes; restore it again afterwards. The usage-limit fallback's automatic restore does not cancel. A
conversation changed independently is reported and left alone. Empty requested model/effort retain launcher-
default behavior, while a saved empty permission mode restores explicit `default` instead of inheriting the fallback.
Preview creates neither state nor a phone confirmation; MCP execution uses one existing whole-batch outbound
confirmation and the caller's space-scoped conversation lookup. Full-role HTTP routes and batch status are documented in
[../protocol/protocol.md](../protocol/protocol.md).

`GET/PUT /api/v1/assistant/segments` controls **new switches**, enabled by default. Disabling it keeps segment routing,
reads and recovery running. A manager binary from before segment routing is **not a safe downgrade after a switch**:
it could resume the retired first segment and fork the conversation. Roll back with a segment-aware manager and the
switch setting disabled. The phase-1 history-read setting does not disable logical routing.

Logical-segment queue retention: an accepted input keeps its client ID, SHA-256
content digest, delivery state and ordering metadata; its duplicate payload and
transport provenance are removed after the receipt commits. A rejected first
input that rollback requeues keeps its payload until the restored source accepts
it. Ambiguous deliveries retain their content for positive-receipt reconciliation.
Terminal switch records discard their handoff summary, except the one an idle
acceptance keeps until the conversation's next input is accepted, or a later
switch is. Deleting the logical chat
cascades to receipts, segments, inputs and switch context. Physical device aliases
are locked together during first adoption, preventing concurrent managers from
creating two logical roots for the same physical chat.

## Idle cache keepalive (#12786)

Settings > Assistant > Keep idle chats ready is on by default. Only the main
conversation and ids registered in `assistant.leaders` may refresh a provider
cache. Background supervision, ordinary conversations, workers and subagents are
excluded. Native Claude-family launchers are supported; Codex and other runtimes
are not supported yet. The default interval is 55 minutes (1–55), with at most 24
consecutive requests (1–144). Genuine input resets the count. Turning the switch
off stops refreshes without breaking an already proxied conversation.

The node resolves the launcher's original endpoint using its existing configuration
resolution, then places a loopback `ANTHROPIC_BASE_URL` in that process's environment
and final settings env. Policy or launcher settings that override it simply bypass
the proxy and cannot produce refreshes. A Persona-managed launcher's `personad launch`
puts the provider's own `ANTHROPIC_BASE_URL` back into the process environment, so on
such a launcher only the settings env names the proxy; check the start's settings file
(`launcher-settings/assistant-compact-<id>.json`) or the node's loopback listeners, not
the process environment, to see whether a conversation is proxied.

The proxy is mounted only when a process starts, so the role must come with whatever
starts it. The manager attests it on every send it makes into an existing conversation
(`assistant.send`, `session.send`, `session.send_once`, A2A delivery): the owner's
messages, relays, a leader's supervision digests and Inbox continuations alike. Any
value a caller put there is replaced. A cold compaction restart reads the current role
from `assistant.model`. A process started without the role (by an older manager)
runs unproxied until its next start. The manager reads a node's retained inventory to
revoke ids that are no longer root assistant conversations; a node never sets
`Session.Assistant` in its own rows (only the hub's decorated list does), so the row is
judged by its directory, as agent naming does (#13879). Ordinary API requests and streamed replies
are forwarded without changing payload bytes. Provider errors are returned as-is;
transport failures return a generic 502 without logging upstream error text.

Only a successful request with a reported cache read/write and exclusively one-hour
cache declarations can be retained. Claude Code declares a one-hour cache only for a
subscriber or with `ENABLE_PROMPT_CACHING_1H`; on an API key or a gateway it sends a
bare five-minute `{"type":"ephemeral"}`. A start that mounts the proxy therefore also
sets `ENABLE_PROMPT_CACHING_1H=1` in its settings env; other starts keep the CLI's
default. The proxy asks the provider for identity encoding on `/messages`, since a
compressed event stream would hide `message_start` from the response tap. Mixed TTL requests
are skipped to avoid rewriting an expired five-minute history behind a cache hit on
the system prefix.
The provider response's message id must also appear on the CLI's root `message_start`;
a child Agent's `parent_tool_use_id` never confirms a snapshot. This prevents warming
an inherited subagent request. Request bodies and necessary headers stay in memory,
never in a transcript or log. Idle CLI release does not discard the snapshot; explicit
close, archive, compaction, role removal, or changed model/launcher does. Before
refreshing, the node rechecks the manager's current role, policy and execution.
Missing authorization stops refreshes. Compaction requires a subsequent genuine input
to rearm capture. A restarted node starts empty.

Refreshes replay the captured bytes with only top-level `max_tokens` and `stream`
changed, preserving whitespace and every tools/system/messages/thinking/cache_control
byte. No prompt is sent to the CLI, no output is executed, and no conversation,
notification or supervision event is emitted. Disabled or adaptive thinking uses
`max_tokens: 1`. Manual thinking keeps the exact budget and uses `budget_tokens + 1`:
a 32,768-token budget can therefore spend up to **32,769 output tokens per refresh**,
or **786,456 across 24 refreshes**. This is an upper bound, not an observed charge.
Unknown or unsupported request shapes are skipped. No retry is made after a refresh
error or a response without a positive cache read; a genuine request must rearm it.

Refresh requests carry `X-Persona-Request-Kind: cache-keepalive`. The node log contains
only `cache_keepalive`, session id, UTC time, and cache-read/cache-creation/output token
counts. The TTL clock starts at provider request start. Expired snapshots are never
refreshed. Linux fake-upstream tests prove transport and isolation; actual provider
cache hits require post-deployment gateway usage evidence and are not inferred from
those tests. No gateway changes or deployment are part of this feature.

Provider references: [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
and [manual thinking constraints](https://platform.claude.com/docs/en/build-with-claude/extended-thinking).
