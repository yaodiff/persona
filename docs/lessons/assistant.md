# Lessons: assistant (headless Claude / Codex)

- A presence window is not an owner-turn guarantee (#6394). The two-minute `ownerPresentFor` hold and the
  five-minute `relayDecisionMax` cap let queued relayed/decision messages start new rounds behind the owner's back
  once the window expires, even while the owner is still waiting on their own round. Keep the ranked queue and the
  presence window, but add an in-memory per-conversation gate armed when the owner speaks (`relayBook.ownerTurns`)
  that holds every rank until a session list shows the round stopped. Give it two safety releases — a short
  `ownerTurnStartWait` when the round is never seen working (refused send, dropped conversation) and a hard
  `ownerTurnMax` once seen working but never stopped — and make an expired gate return immediately rather than fall
  back onto the presence window, or the cap is not authoritative. The gate is memory-only on purpose: a restart
  degrades to the persisted presence window. Update the time-window tests by shortening the new limits in lockstep
  with the presence timers instead of waiting real minutes.

- Inbox handling chats are implementation details, not user-created conversations (#5723). `Untitled` suppresses
  automatic naming but does not hide a chat, and `titleFixed` still gives it the item's subject. Apply default
  archiving in the common session snapshot, not only after starting or finishing: restored chats and follow-up
  instructions must stay hidden too. Preserve item-chain links and the main conversation.

- Count repeated conditional instructions at the runtime-send boundary, not just in a prompt golden (#5665).
  Two adjacent decision blocks repeated the same handling rules, but the newer one also required recording the choice.
  Remove the older copy, retain the newer obligations, and check both decision gating and the actual synthetic
  `DeviceAssistantSend` text. This proves literal deduplication, not a stable-policy refactor or provider-context savings.


- Rechecking an Inbox task link must not replay event handling (#5680). Existing processing conversations and saved
  drafts skip the initial task judge; resetting their processing fields to force another pass can duplicate work.
  Use the task-link maintenance action instead: validate the message and candidate after judgment, persist through
  Inbox rollback-aware writes, then publish the draft's task card and supervisor signal. A model failure is distinct
  from a successful no-match result.

- Proven `prompt` source can describe either a terminal tool label or a first-message fallback (#5269).
  Ordinary Claude inventory falls back to the first message for a bare tool label but not its spinner-prefixed
  form, so spinner normalization alone still lets a generated name disappear. Accept recognizable bad fallback
  changes only with explicit Claude runtime and prompt provenance; reject other tool/runtime names, meaningful
  titles, manual/custom titles and unknown sources. Apply this comparison at display and late-result boundaries,
  and test both directions plus service reconstruction without a new model call or counter reset.

- A to-do's PR reference may exist only in `PRURL`, without appearing in its title or notes (#5458). Duplicate hints must read that link too; an open to-do still represents ongoing work when its linked Agent is idle.

- Terminal spinner frames are presentation, not native-title changes (#5254). Comparing a saved `raw_title`
  byte for byte with a later inventory hid a successful automatic name while leaving its one-shot count spent.
  Use the same leading-spinner/whitespace comparison for display decoration and the model-result recheck.
  Preserve exact substantive-title differences, manual/custom protection and unknown-provenance skips; never
  clear counters or call the model again just to repair display. Test every frame, a stopped label and a frame
  change during the request, alongside genuine title changes and manual/unknown sources.

- Measure acknowledgement and execution separately (#5258). A resident `claude -p` process does not imply immediate
  visible feedback: context preparation, control ACKs and cold-start confirmation all happen before a send returns.
  Independent context and mode preparation may overlap, but both must finish before input is written; preserve the
  original mode/effort for a retry when an unconfirmed control rolls back local state. A hub receipt is not durable
  delivery or model execution, and must not change an already working turn. Test the HTTP/WS boundary, stale snapshots,
  echo-before-response, failed sends and unknown delivery separately; label fake-CLI timings as such. Production request
  totals without stage logs cannot establish which stage was slow, and absent logs are never a measured zero.

- Automatic Agent names need title provenance, not a string heuristic alone (#4812). A user can deliberately choose
  a tool-looking name, and an old custom-title record can fall outside a quick tail window. Treat native custom titles
  as protected and unknown provenance as ineligible. Keep automatic names in the manager's own ledger, persist manual
  intent before dispatching a rename, and decorate both list and single-session reads. Count cumulative output tokens,
  never changing input-context size or transcript bytes, for periodic naming; request a bounded Responses output too.

- A task trust key combines a machine and directory with an unescaped delimiter (#4421). Either field can contain
  that delimiter, and an old ID need not obey current registration rules, so a split can assign the rule to the wrong
  place. Keep its exact key for matching and deletion; mark an ambiguous listing and give older clients the key as a
  path-escaped identifier, so a path-component title retains it. A failed settings read is not an empty list:
  propagate it to the list caller while retaining the fail-closed admission and no-overwrite behavior of writers.
  Exercise the handler after service reconstruction.

- Reusing a topic to-do must retire the previous plan's execution binding (#3985). Released or dismissed Agents let
  a later plan take over the same to-do, while the old plans stay in storage. A historical `TodoID` match cannot
  impose that plan's read-only constraint on the current session. Clear old execution bindings under the takeover
  reservation before launching the replacement; keep `ReuseTodo` as history. Plan creation order is not start order,
  and retries can change `UpdatedAt`. Cover both directions of permission changes, memory, storage and restart.

- Unavailable context is not nonexistent data (#3780, #3862). When a privacy switch excludes earlier meetings, carry
  that policy into both answer prompts: do not promise earlier minutes or render an empty lookup as “none with
  minutes.” Keep goldens for disabled, enabled-with-results and enabled-without-results modes. Tool descriptions must
  also distinguish expired transcripts from meeting minutes retained forever by default.

- A to-do's `origin` is what makes it a task, for the hub and every app (#1852, #1909). The hub tests `origin != ""` in some 70 places (receipts, the done judgment, reopening on a new turn, retiring a finished task's Agents, retitling, outcome records, Today, MCP tool text), and web, iOS and Android pick a conversation's tasks by it. A to-do that only relates to a conversation, such as a question nobody answered, keeps that conversation in its own record, not in `origin`: an `isAskTodo` skip in `todosOf` hid it from the snapshot alone, while the apps showed it as a task whose Agent was starting and a hand-off got it closed by the judge. Before giving a new kind of to-do an origin, read every `t.Origin` use and the apps' task filters.

- A read-modify-write of shared assistant state needs a lock and a read that can fail (#2192). `ensureToday` looked for the day's card and then appended one, and `assistant_feed` has no unique key, so concurrent opens of a chat each made a card. A plan's tasks are one blob that `movePlan` writes whole, so tasks whose starts failed together wrote back each other's old errors. `trustKinds` read `tasks.trust` through a read that answered an empty map on error and wrote that back over every kind remembered before. Serialize the writers of one key (`cardLocks`, `patchPlanTask`, `trustMu`), read again under the lock instead of writing back a copy read earlier, skip the write when the read failed, and answer from the rows that were saved (`saveWrites`), not from memory. Where DST starts at midnight, `time.Date(y, m, d, 0, 0, 0, 0, loc)` is 23:00 the day before: take the date from `now` and the day's start from `dayStart`.

- A disconnected start caller does not prove the Agent was never created (#3251). A runtime bridge can finish `agent.start` after the hub loses its reply; directory and title are not unique operation identities. Recover an interrupted task from its persisted plan, to-do and agent links, and fail visibly when no durable start key or session identity can make replay safe. Do not invent a kept pane to enter start remedying. Read all candidate plans before recording failures: patching one task advances the whole plan's update time and can otherwise hide its siblings from a startup time window.

- Model role is not message authorship (#2125). Cross-conversation relays, scheduled work and supervision still use the runtime's user role, but carry explicit `Item.source` in a persisted message-source context. Preserve the sender's stable id and title through queued relay recovery; never use a native peer's display name as that id. The pending-send echo cache expires after a day, so it cannot hold permanent provenance. Prefer metadata and only recognize complete legacy machine templates. Protect user-pasted templates at both new-chat and send entry points before echo matching, and exclude source metadata blocks from selectable Context cards and automated input from user-only navigation. Source JSON and the user-authored sentinel are fixed-size context, never water-filled with ordinary context bodies: near the 24 KiB page boundary a partial JSON block loses authorship while a full-item read restores it. Test the context-clipping boundary as well as large messages that take the plain-cut path.

- A completed onboarding step must not hide recovery from a later failure (#1894). `first_message` stays `ok` after one answer, so key and model errors need their own `test_assistant` alternative on the model step. Verify the real readiness handler after a successful answer and a later error, then failed and successful probes, while retaining the current chat and the existing machine / launcher evidence guards.

- A runnable-computer list cannot explain pending confirmation (#1890). Readiness must intersect the hub's pending IDs with the manager's own devices, which already carry display names; the pending list can include another manager's computers. `devices.Summary.Trust` means personal/work policy, not pairing confirmation. Read the current pending snapshot for recovery UI, and keep pending computers out of preparation, probes and starts.

- Disabling the Inbox twin must stop admission without skipping reconciliation (#1847). Existing queued and running items otherwise stay `looking` / `agent` forever and disappear from Needs you counts. Release them through rollback-aware writes, clear pending notes and retry metadata, and cover direct `tellSoon` delivery as well as the periodic handling pass: suggestion actions can reach delivery before the pass. Return disabled suggestion actions to Needs you before answering, and prevent late sub-chat starts from restoring Working. Preserve completed suggestions and already confirmed replies waiting on the other side.

- Inbox policy admission checks do not revoke queued work (#1848). Queuing already marks an item as handled, so neither tier reapplication nor the unhandled-item escalation check sees a later `never` rule. Recheck the effective policy at the start boundary, before backoff, capacity and conversation adoption, and persist releases through the shared rollback path. A policy release uses the item's tier; marking it failed would incorrectly promote FYI to Needs you. Keep already running or starting conversations outside this queue-only rule.

- An Inbox item's `source` is not always a connector (#5537). Source subscriptions deliver under their own id (`sub_…`) with a generic `connector` block, so every client opened them like a connector's thread and showed the hub's 404 `no connector "sub_…"` with a Retry that could never work. An endpoint keyed by an item's source must answer for subscriptions too, and "there is nothing to open" must be an answer every client already takes quietly (a page whose `chat.kind` it does not draw), not an error: old apps keep showing every error they get.

- Connector sign-out must invalidate the channel's pending state at the shared lifecycle boundary (#1837). Clearing it only in the Channels page's Unbind route leaves the Connectors page and automatic credential revocation able to reuse old questions after rebinding. Notify the assistant synchronously before accepting a later message; WeChat's credential save/delete hook covers its automatic unbind, while its localized, truncated account label is not a stable identity. A completed sign-in must retry cleanup before reporting success, because automatic revocation can only log a storage failure. Clear late-answer reminders too, and check the binding generation when recording an ask or turn after its send finishes, so an operation in flight during unbinding cannot add old state back afterward.

- A quiet skip must remain quiet through aggregate notifications (#1857). Readiness gating recorded a skipped run without pushing directly, but `recordRun` counted its third occurrence toward the failure-streak push. Persist the semantic reason before recording (`not_ready`), and exclude it in the shared streak predicate rather than matching localized `Error` text or suppressing only the immediate push. Cover several scheduled skips, restart persistence, temporary offline computers, and genuine failures interleaved with readiness skips; ignored skips must not reset the genuine failure streak.

- A stopped Agent belongs to a task, but its latest turn may answer a different conversation (#1988). Keep input provenance through completion judgment, never infer that a later question means the assigned task was finished, and require current-task evidence before treating a side turn as delivery. A pre-judgment receipt says checking, not pending review. Quiet pushes and quiet conversation rendering are separate controls: fold the whole triage turn while retaining its audit transcript, and preserve explicit user-visible tool outcomes through server work folds. Register the supervision context before dispatch and exclude that dispatch from the normal send's release path, or a fast finish tool loses its context.

- Retiring a to-do's existing wake is not a lasting check on automatic follow-ups (#1921). A new turn clears the retirement marker, and a failed retirement write can leave a wake in storage. Check the current unreleased, undismissed to-do links both before triage acts and before a wake sends; an open link wins over completed links. A user-completed to-do stays done after a manual message, whereas `taskWorking` reopens hub-completed or accepted tasks (`auto` / `review`). Cancellations must remain final too (#1851): persist `done_by=cancelled` even when there is no supervision ledger, and exclude older `review` completions whose ledger says `cancelled`. Keep the API value `review` for existing clients' cancellation labels and reasons. Do not infer cancellation from a localized reason; explicit user reopening still restores task tracking. Do not mistake a new turn or an old CI watch proof for a reopened to-do. A failed to-do read must leave triage retryable and postpone a wake, not treat the session as unlinked or permanently cancel it.

- A shared completion value does not imply the same handling-chain explanation (#1908). Mutes retain `done_by=read` for compatibility, but must say they were muted, not read elsewhere. Cover existing items, new connector-item admission and grouped events in every hub language, and keep an ordinary-read control.

- Health notification redaction must cover every notification route (#1914). `notify_user` passes through `runTool`, but `phone_notify` goes directly through `mcpCall` to the phone. Apply the existing caller-space check and `healthNotice` before phone dispatch, and assert the forwarded title and body in tests. Device notification-preview settings and a prompt recommendation cannot enforce the space boundary. Keep explicitly requested, phone-confirmed reminders separate from generic notices.

- A PR's check-run endpoint alone is not the complete CI result: commit statuses are separate, and both must be paged. Check the PR head again after reading and reject incomplete or inconsistent pages. The node's older PR reader cannot attest complete rollups and can classify a rerun by its old conclusion; only nodes explicitly marking a complete, status-aware result can be used for a CI wake. Test a failing check alongside a pending one, a changing head, and rate limits.

- Reproduce settings chrome through both production entry paths (#1433): the profile button on the assistant page and the settings navigation entry. Use a manager whose localized name matches the page title, then count the navigation title and switcher separately; a differently named test manager hides duplicate title regressions. Cover absent rosters, a single manager and legacy configuration responses. Prove the assertion catches the original row by temporarily restoring it, then remove that mutation. Released source ancestry and a download test do not prove which build a user's phone is running.

- Permission-default support must come from the manager that owns the setting (#1433). A newer Hub, an owner id, or an optional saved value cannot establish support on an older manager. Return the versioned contract with the manager's configuration and validate its owner on both PUT and independent GET. Test through the production Android adapter rather than overriding a capability method only in test doubles; a default-false adapter can otherwise disable every production manager while tests pass. Persistence and node-native policy are separate checks: verify both Claude startup/control requests and Codex thread/turn policies.

- Stage user-filing handling closure in the row being saved and publish it only after that row commits (#1313). Clearing drafts and pending notes on the live item before saving requires more than restoring its done marker, and a concurrent delivery pass can send the stop note before rollback. Exclude pending filing markers from unrelated saves, including delivery acknowledgements. A bulk unchanged acknowledgement also requires a committed state; return pending ids as failed so clients retry. Filters must include every pending open state that can still commit, including intermediate reopens between closed endpoints, and retain older pending ownership when a newer save fails. Give filing its own pending ownership so an unrelated parent edit cannot prevent rollback; keep concurrent chain entries and test batch fallback with mixed row failures, nested draft aliases and delivery during the write.

- Schedule admission must be durable before dispatch (#1287). Save a separate candidate task list while holding the schedule lock, then publish it and broadcast only after success. A failed user edit or due-run marker leaves the prior tasks recoverable. Results and conversation ownership from work already sent are different: keep them in memory when persistence fails, preserve their history IDs so watchers can still update them, and retry the current lists on later saves. A late result still belongs only to its run; it must neither overwrite a newer run's status nor recreate a deleted task.

- A list bounded by a retention window is not bounded (2026-11-12, #5549). `GET /api/v1/inbox` listed every item done within three days, so one Clean up all of a few thousand items made it about 10,000 items and 21 MB: every app downloaded it again on each `inbox` push, and Android's bounded reader (4 MiB, 100,000 structural tokens) turned it down. Cap the done side by count on a deterministic key (`done_at`, then id, the history's order: a just-filed item is always kept, and the same items give the same bytes for the merged validator), let the delta report what falls past the cut as `removed`, keep it reachable by id and through the history, and test with more items than the cap. Clients that took the window as complete without a page (iOS `listDoneSpan`, the web's former `LIST_DONE_MS`) under-show until they page it too.
- A one-at-a-time background pass guarded by `TryLock` must not drop the calls made while it runs (2026-11-07, #64). `handInbox` returned when a pass was running, and every caller is an event: a group event that told an event's conversation of the other side's answer called it while a pass was past its delivery step, so the note waited for the next unrelated event (and `TestInboxSuggestionConfirmReply` failed most runs under `-race`). Mark the request, let the goroutine holding the lock run passes while the mark is set, and check the mark again after unlocking.

- Keep task receipts ahead of completion judgment (#4893). Moving `autoFinishTask` before the receipt adds a model call to its latency and sends supervision signals before the receipt. The review hint names who follows up next, not a `pending_review` verdict: `superviseJudged` signals Persona for finished work (`sigReview`) or unfinished work (`sigIdle`); a task already in `needs_user` is waiting on the user. Read the latest to-do at receipt time and include the hint for an open supervised task unless `Supervision.state` is `needs_user`. That helper treats reopened accepted/cancelled tasks as ongoing. Test that the receipt precedes judgment, including zero criteria, and that ordinary, completed, failed and interrupted tasks do not get the hint.

- Owned triage questions require a durable interaction history Store before acknowledging an answer (#5994). A bus integration test that previously used no Store must supply its manager with the isolated Postgres fixture and assert `InteractionOutcome` after resolution; never weaken the production missing-store failure merely to keep the test green. The shared hub fixture does not truncate `session_work`, so explicitly prune old work in this isolated fixture before connecting: otherwise a repeated run can restore the previous answered turn and suppress the new card as a duplicate.

Pitfalls, unusual behavior, and decisions at the time (formerly `CLAUDE.md`; index `docs/lessons/README.md`). Add lessons to relevant sections, or a new section if none fits.

- Push delivery is per destination, not just per conversation (#1896). A shared `notify_user` mark lets one successful channel suppress another channel's failed delivery, while an unmarked extension push duplicates its turn-end mirror. Key delivery by channel, machine and session, record only confirmed sends, and apply the same check to main-chat mirrors, scheduled-task mirrors and Lark mirror retries. Test mixed successes and failures, including text split across several sends.

- A one-shot send that forgets its record on failure loses what it was for (2026-11-07, #7). The "Please verify" card is asked once, when a task's deploying turn ends; a failed or timed-out send used to drop the claim, and nothing asked the owner again. Keep the record (the ask, without a message id) and retry from a sweep, under a key per round (`uuid`) so a timeout that did land is not a second card, and only within the hour Feishu remembers a key (`acceptPostWindow`, as the nudge's `nudgeRetryWindow`); past it, fall back to the card-less path the briefing and board already show. Whatever a sweep or a load reads back may hold a claim saved mid-flight (no time) or a null entry: clean the map that was read, before it becomes the book, and never dereference an entry unchecked.

- An agent stopping does not clear its task's pending review (#4889). The Today card must check `Supervision.state(t)` before grouping by `taskState`: a pending-review task may look stopped, unread, or even still working in the agent snapshot. Keep it in one review list, and preserve the open-to-do exclusion from the finished list (#6159). Test with a done receipt as well as conflicting agent states to catch both omissions and duplicate counts.

- A byte limit can split UTF-8 even when each message was clipped by runes (2026-11-07, #5948). `transcriptText`, shared by `agent_read` and `conversation_read`, limits the assembled transcript to its last 40000 bytes. Advance the suffix start past continuation bytes with `utf8.RuneStart` to keep the byte budget and whole characters. Test every cut position in two-, three-, and four-byte characters, cuts already at rune boundaries, and lengths below, at, and above the limit; assert valid UTF-8 and the exact retained suffix.

- A successful control-request write is not Claude's acceptance (2026-11-06, #5947). Match `control_response.request_id` before changing the process's acknowledged mode / effort or the persisted `AppMode`, `AppEffort`, and `ModelEffort`. Register the waiter before writing and use the value returned by the atomic request counter; reading the counter separately can reuse another concurrent request's ID. Serialize setting changes, keep response handling off the settings lock, and ignore unknown / duplicate replies. Return failures through the existing `session.modes` HTTP path so clients can restore their selection. A plain `i18n.Err` loses its type over node RPC: send a stable error code and localize at the HTTP boundary, including the upstream reason in the summary because some clients do not display `detail`. Test with fake SDK responses, including partial success, refused changes, unmatched IDs, and process exit; do not use a real Claude configuration for these cases.

- Key assistant prepare state and generated CLI wrappers by the same canonical directory (2026-11-06, #5831). Per-directory connector and CLI maps alone still leave a shared `assistant-bin` vulnerable: preparing another manager can remove a command or replace its connector when both use the same command name. Scope skill synchronization and process PATH too, and check CLI permissions using the authenticated assistant process's directory. Test two directories with disjoint connectors and a shared CLI name, then prepare or sign out one and verify the other's permissions and wrapper remain unchanged; include symlink aliases and unprepared directories. For `skills.list` callers that omit `cwd`, preserve discovery when the only prepared directory is non-default: prefer a prepared default, then the sole prepared directory, and never choose arbitrarily among several. An empty connector list still counts as prepared.

- Reintroducing a retired model purpose must not read its dormant settings record (2026-11-06, #5921). Lark /agent uses a new override namespace and revision sequence, inheriting Agents until a new user save. Keep category-default assignment from creating an opt-in binding, include overrides in provider-in-use checks, and determine client editability from writable fields rather than `category_id != id`. Tests must seed the old record directly, then verify new conversation startup parameters before override, after save, and after reset; merely asserting the resolved binding misses startup precedence.

- A second opt-in model override shows where the first was keyed by ID (#4232). Setting the AI twin apart like Lark /agent needed no new server path, but every client special-cased `id == "lark.agent"` for its status words, reset label and effort rule, so the twin's row and editor would have kept a category's wording (no "Separately configured", "Restore Default" instead of "Follow Agents") while being an override. Recognize such a row by data (`parent: "agent.new"` and writable `fields`) and keep ID checks for wording that really is one purpose's. Server tests start the twin through its own path (`startInboxAgent`), not the conversation route, and check that a save writes only its own key.

- Model-settings HTTP labels must use `requestCtxLang(ctx)`, not `s.lang(ctx)` (2026-11-06, #5057). The latter reads only the hub's language, so catalog-driven launcher role labels can still ignore a client's `Accept-Language`. Test sequential requests with a different hub language, both discovered-model and fallback-model paths, and unchanged model IDs. MCP-only refusals remain original model text; they must not become exemptions for real HTTP, push or approval-card copy.

- A database-read failure for session triage settings must not return `200` with defaults (2026-11-06, #4553). Toggling one switch makes the client `PUT` the entire settings group; a failed read disguised as All Off would overwrite other enabled values. Preserve the read error and return `503` with a localized reason from the settings API. Only internal automatic triage keeps a default fallback. Do not cache failures; the next retry must read the database again. Regression tests inject read errors through a fake Store and check API failure, saved values after recovery, and unaffected internal fallback and `PUT`, without stopping the shared test database.

- Assistant-card boundary validation must cover more than common syntax (2026-11-05, #2018). Duplicate-key errors were overwritten by later chart / Markdown checks returning nil; reference-style images / links bypassed inline-only regex. Shared validation rejects duplicate keys first, then components. Safe subset explicitly rejects reference definitions; regression tests include images, autolinks, URLs without hostnames. Remove empty sections after stripping HTML, or compact limits reject emptied cards as oversized. Live-card version must stay within JavaScript safe integers, not merely Go int64.

- Single-session lookup must use trusted assistant classification too (2026-11-05, #2019). Lists classify by dedicated directory / pane, but session.get returned raw node records usually lacking Assistant. Rich-UI tools / outbox falsely rejected real assistants. Reuse isAssistant on lookup; pane takes precedence as ordinary Agent. Tests must use real hub query path for headless / pane / normal directory, not manually Assistant=true. Body registration shares classification.
- Go JSON decoding ignores field-name case (2026-11-05, #2019): Fallback populates Go Fallback, but Swift / Kotlin require schema fallback. Validate exact keys before converting assistant raw blocks to Go types; successful decoding is not cross-client conformance.
- Preserve encoded paths over HTTP bus (2026-11-05, #2019). Body-card keys contain /, item IDs often #. Clients encoded correctly, but forwarding `r.URL.Path` made assistant treat them as separators / fragments, actions all 404. Use `EscapedPath()`, test real hub→assistant relay; Service routes alone miss this layer.

- AskUserQuestion unavailable in headless `claude -p`, tool disabled rather than disconnected hook. Phone questions need blocking MCP `ask_user` (`docs/assistant/assistant.md`). Model may call it alongside other tools; side-effecting tools require their own hub approval.
- Claude Code ends a stdio MCP tool call silent for 30 minutes (`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`, Claude Code 2.1.3xx; remote servers 5 minutes), whatever the server's own deadline (#1498, 2026-11-08). A wait as long as that limit races it: the phone confirmation's 30 minutes lost 2 of 3 times, and the model got "sent no response or progress for 1800s; aborting" instead of the hub's "check before calling again". `notifications/progress` for the call's `_meta.progressToken` restarts the clock (`personad mcp` `keepAlive`); the per-server `timeout` / `MCP_TOOL_TIMEOUT` is a separate hard cap progress does not extend (default 1e8 ms). Also check `personad mcp`'s own `CallTimeout` when a wait grows: it was 40 minutes under ask_user's 2 hours. Tested end to end with a fake node socket: without progress the call ended at the idle limit, with it the 150-second answer came through. Live wait kinds (#6253) must stay explicitly opt-in: the local socket historically had one response line, so only a token-bearing new client may receive interim `wait_kind` lines; no capability or an unknown enum keeps `Still waiting`, and the final response must exclude all later updates. Map the actual interaction kind at every shared ask entry point (a direct permission is not an `ask_user` question), and carry only the opaque call correlator across nested RPCs such as hub-owned settings and LCU elicitations: their fresh RPC contexts otherwise silently lose the outer wait state. Negotiate that correlation at every delegated hop, not just on the upstream connection: a target without `mcp_wait_v1` gets no id and yields `unknown` for that RPC, then restores the previous state. Presentation and semantics also differ—a native permission with four choices is still `approval`, while a genuine enum question is `answer`. Finally, namespace selection is an assistant-startup snapshot but wait opt-in is a live per-call hub-feature read; conflating them breaks upgrades and rollbacks in a running assistant.
- Default-mode headless assistant even needs phone approval for Persona MCP notify_user / `schedule_followup`, stalling nighttime follow-ups / Monitor (tested 2026-10-30). Pass `--allowedTools mcp__persona`; hub asks separately for acting-on-user-behalf tools (`docs/assistant/assistant.md`).
- A tray's Waiting status describes the parent turn, not whether its background work is idle (#3591, 2026-11-10). Reuse the header's activity data for a Working spinner and phase/progress or running counts; reserve the clock for no background activity. A lightweight `DockAgent` projection must carry that data too: otherwise a same-state workflow update leaves manual-Agent rows stale. Aggregate all linked task agents, deduplicate session references and compatibility workflow markers, and keep progress digits outside truncated phase text.
- Background tasks / Monitor are Claude child processes; idle recycling or systemd personad restart killing cgroup kills them, leaving chat waiting forever. Node reads `stream-json` `system/background_tasks_changed`, does not recycle with active tasks, persists task list continuously, tells chat what was lost after restart.
- `background_tasks_changed` comes only when the set changes, never at start (Claude Code's schema, 2.1.311): a process that never ran a background task has reported no list, which is not the same as an empty one. A repeated `initialize` control request is answered with the current set right behind its success, empty included; the first one is not, and node sends none of its own, so asking means sending two (#3495). A fake CLI that prints an empty list at start hides this: recovery's model switch passed its tests and never ran on the real CLI.
- Resolve read-only permission from the actual launcher at planning and again at dispatch (#1841, #4864). Claude starts in `bypassPermissions` and receives the read-only constraint in its first prompt; Codex retains `plan` and explicit sandbox flags. Persist a changed permission with the resolved launcher even when machine and why are unchanged, and keep the constraint out of the saved prompt so retries do not duplicate it. Ordinary Codex terminal starts discarded `plan`: map it to explicit sandbox and approval CLI flags, check both alias and managed launcher arguments for conflicts, and retain the constraint across later resumes. Runtime bridges can ignore the mode or fall back to another provider; refuse plan starts without an enforcement guarantee before sending the prompt.
- Scheduled assistant work needs one table only (2026-10-31, `docs/assistant/assistant.md` “Scheduled tasks”). Separate schedules / follow-ups forced repeated checks to `schedule_followup` each round; entry disappeared on start, invisible / unstopppable during hour-long approval waits. Repeating work uses every; self-reschedule recognized as same task. Only user changes task model / permission in app; assistant cannot elevate itself. Headless `bypassPermissions` only at launch.
- Lark assistant uses gateway skill (copy formerly `server/internal/assistantskills/`, embedded personad, installed `.claude/skills` at assistant startup, upstream sync script; since 2026-11-04 in Lark plugin). Token formerly process env; since #1317 not even env, below. Node Bash PreToolUse `node/larkgate.go` applies only `assistant-hooks` / assistant-settings.json. Unparsed commands containing `ol POST/PUT/PATCH/DELETE` still require phone confirmation, otherwise bypass permits; credential-proxy gate now rejects them outright.
  Test hub uses own HOME / port / DB (`/tmp/jt`), never live personad. SSH pkill -f pattern can match own SSH command; use [/]tmp/…. [x] works only if literal absent elsewhere in same SSH command: 2026-11-01 `pkill -f "[d]rive.py x"` followed by `python3 drive.py x …`, and `pgrep -f persona_args` then process-group kill both killed SSH, exit 255. Safest anchor full path, `pgrep -f '^/tmp/jt/…/personad'`, get PID / kill, only that task per SSH.
- Keep credentials out of Agent env, not protected by read-command blacklists (#1317,2026-11-05). Env, split-variable-name printenv, `cat /proc/self/env*`, ${!prefix*}, `python3 -c 'import os…'` bypass; review also nohup env, `compgen -e`, ps eww, eval-built names, `env -u HOME printenv`—unpatchable. Worse, token-bearing scripts bypass write confirmation directly. Credentials stay in node; local proxy checks every actual request against same rules (`node/gateproxy.go`). Proxy belongs to entire persistent `assistantKeeper` process, not per-connection Node: reconnect replaces Node, surviving chat retains old proxy address, new approved calls cannot reach old proxy. One approval allows method + path + body once. Writes in unparsed loops / scripts must be refused and sent as standalone commands; never open timed allow-all windows, which permit unrelated later commands in same chat.
- Codex assistant uses `codex app-server` (`docs/assistant/assistant.md` “Codex”). MCP approval arrives as `mcpServer/elicitation/request` with `_meta.codex_approval_kind: mcp_tool_call`, not command approval. Default tool timeout 60 seconds needs larger `tool_timeout_sec` for ask_user. Default command env strips `*KEY*` / `*TOKEN*` (ol lost token). `turn/interrupt` does not stop running commands; `thread/backgroundTerminals/clean` additionally, experimental generate-ts --experimental. Codex owns thread ID, unknown when MCP starts. Lark gate relies on approval arriving; never `approvalPolicy: never`.
- Codex / Claude share assistant auto-memory (`docs/assistant/assistant.md`). Untrusted Codex approves even head; without separate allowance every memory read prompts phone (tested 2026-11-01). Memory outside assistant directory needs writableRoots. Do not enable machine-wide `~/.codex/memories` for assistant.
- Mac Codex sandbox rejects symlinked writable roots except top-level `/tmp` `/var`, before every command: symlinked writable roots are not supported (#1751). Legacy `~/.persona`→`.persona` and dotfiles `~/.claude` trigger it. Resolve cwd **and** memory path for `codexDirs`, not cwd alone. `allow_symlinked_codex_home` affects CODEX_HOME only; do not broaden. Verify same parameters with `command/exec` and `thread/fork` with `ephemeral: true`, pwd, preserving original session.
- Fake-HOME hub tests: `~/.local/bin/codex` wrapper resolves installation via `$HOME/.npm-global`; symlink into fake HOME or app-server exits immediately.
- Fake HOME is not isolation. Node follows launcher-selected `CLAUDE_CONFIG_DIR`; hardcoded profile launcher writes real transcripts, and cwd suffix `/.persona/assistant` makes real assistant chats appear in test hub, editable by test assistant (2026-11-01 chat-management tests). Copy launcher into fake HOME, redirect `CLAUDE_CONFIG_DIR` there, copy settings.json, define `_claude_launchers` in fake .bashrc, prompt only own created chats; delete full fake HOME afterward, keys included.
- Unknown `assistant.send` session ID starts **blank new chat** rather than error if no transcript. Resume-or-report-failure calls (scheduled chats 2026-11-01) use `resume_only`; no process / transcript errors, hub creates / records new chat (`docs/assistant/assistant.md` “Scheduled tasks”). Busy state comes from hub working / needs_you and only listed sessions; retain long-lived chats in `keptSessions`, or 80 new transcripts on busy machine evict them.
- **No per-message changing data in system prompt** (2026-11-01, `docs/assistant/assistant.md` “Live snapshots and prefix caching”). Prefix caching puts system / tools before history; one byte change rewrites hundreds of thousands of tokens after restart, first output 10 seconds to minutes. Snapshots accompany messages. Claude resume actually retains first system prompt until compaction, so old snapshot stale, not cache-invalidating. Real restart cache miss was **changed tools**: one removed MCP tool rewrites all, schema key order does not. Diagnose experimentally: copy real chat with new ID / temporary directory, change one thing per restart, inspect `message_start` cache_read_input_tokens.
- Per-start compaction belongs in both the process environment and the final merged settings (#6481). Claude settings `env` can override the process, and the SDK recomputes provider environment after spawn. Preserve launcher settings / hooks, then pin the computed AUTO and MAX values last in both paths. A launcher AUTO value is not evidence of backend capacity. Match warm spares and restarts by explicit main-chat scope, not the assistant directory or model purpose; scheduled chats share both. Don't turn "nothing declared" into a Persona number (#3008, #3066): a guessed 200k, then fixed 200k / 400k targets, made 1M conversations compact at about 167k. Follow what the launcher declares, write nothing otherwise (Claude Code already holds AUTO to its own model window), and read the real window from `result.modelUsage.<model>.contextWindow` where Persona needs it (the idle threshold). Write values only where Persona owns the environment (a managed launcher): over a shell launcher they would override its own declarations.

- Headless `claude -p` auto-compacts via `CLAUDE_CODE_AUTO_COMPACT_WINDOW`; idle compaction only interactive. `stream-json` `/compact` manually compacts. First resumed-process message sometimes does not compact, second does; one miss not proof unsupported.
- `ssh_host` aliases exist only hub ~/.ssh/config. Giving remote assistant ssh <alias> supplies unusable command.
- Npm lark-cli has `#!/usr/bin/env node`; nvm paths missing systemd personad PATH (2026-11-03,#273). Command name not found; absolute script still fails env node. Node login-shell `command -v` finds absolute path, prepends its directory to PATH (`node/larkcligate.go`); assistant wrappers same.
- Lark-cli user sending requires app `im:message.send_as_user`, absent in default app (2026-11-04,#273). `auth login --domain all` gets 200+ scopes but not it; `im +messages-send --as user` `missing_scope`, `auth login --scope …` suggested by the error cannot grant app-disabled permission. Bot `im:message` exists, so assistant send / reply `--as bot`; phone card states identity from --as. Failed upstream only after phone approval: gate confirms intent, success depends permissions.
- Session `last_text` is node preview, whitespace collapsed /200 characters (`node/sessions.go` snippet), not renderable Markdown (2026-11-04,#1508). Completion-card summaries became one-line lists / headings with truncated ** / backticks and web plaintext symbols. Full completion cards read `lastTurn` via `session.items`, `clipMarkdown` by lines without splitting markup. One/two-line progress / pushes strip Markdown (`textfmt.PlainMarkdown` / `textfmt.PushText` in `server/internal/textfmt`, web `lib/plain-markdown.ts`).
- Hub feed append-only does not mean later Sent status can stay client-memory (2026-11-04,#1519). GitHub comment sent, refresh reverted card to Draft / Send, nearly duplicate. Persist body.sent_at, rebroadcast same ID, replace body by ID; incremental fetch includes later updates. Same investigation found prompt `查不到的用【】标出来` (“mark unknowns in brackets”), sending literal `【预计今晚 / 几点前】` (“[expected tonight / by what time]”). One-tap drafts must not leave blanks for user completion.
- MCP `tools/list` failure must not return empty list (2026-11-04,#504). Personad mcp declares `listChanged: false`; Claude lists once per chat. Temporary node failure / startup order returned empty, permanently removing Persona tools without connectivity indication. Retry 15 seconds then JSON-RPC error, visible `/mcp` / logs. Node `{"tools":[],"error":…}` is failure too; only valid empty means no tools.
- Nor may one part of the list failing drop that part (2026-11-13, #6422). The manager's `mcpTools` asked the hub for the phone's tools, logged a failure and listed the rest, so a conversation that started while the hub restarted had no `phone_*` for its whole life and the model told the user the phone couldn't set a reminder. Ask again briefly, then list what the hub last gave (`phoneTools`, `server/internal/assistant/fleet_tools.go`); each call is still the phone's to refuse.
- Separate assistant process (2026-11-04,#1446, `docs/assistant/assistant.md` “Process”) can be unavailable to every hub inquiry. Startup connector registration asked each assistant tool name before connection,10 seconds each,60-second test. Startup / registration read last `assistantToolsNow`; wait only when node requests tools for assistant chat. Assistant schedule loop waits first handshake machine / timezone reply before running due tasks, or starts with empty config and fails all.
- Bus events synchronously invoke read-loop callbacks (`internal/rpc`); slow handler stalls connection. Assistant queues in order, handles `session.turn_ended` separately (model query / waiting task Agent stop). To wait in tests, call assistant after event on same connection, then await empty queue (`BusHub.Settle`).
- Hub default `os.Executable() assistant` launches test binary in tests, rerunning entire suite. Tests replace `assistantCommand` in init; subprocess tests detect env in `TestMain` and act as assistant.
- `zsh -ic` expands existing aliases before script execution (#1674). Codex alias makes `codex() { … }` definition parse fail; earlier unalias in same string too late. Use function codex { … }, calls \codex to prevent recursive alias expansion. Real zsh regression with temporary ZDOTDIR, no user shell read / writes.
- Session-only Agent approval lives in process (2026-11-05,#2202): Claude `addRules … destination: session`, Codex `acceptForSession`, Lark node memory lost on assistant recycle / deploy / launcher switch / node restart, not restored by `--resume`, causing repeated prompts despite Always Allow. Cross-process assistant state belongs in hub DB: cards `always_keys`, hub `assistant_always` auto-answers matches (`docs/assistant/assistant.md` “Always allow in this session”).
- Chat Agent type comes from transcript, not current message launcher (2026-11-05). Main chat selects Model Configuration › Main Chat each message (#1518); changing Codex→Claude rejected only while process live. After exit, Claude resumed Codex thread, No conversation found, message Unconfirmed Delivery / Session Ended, broken 19:39 through late night. Failed launch overwrote state launcher, losing original Codex name. `assistantResumeLauncher` now checks transcript type, chooses original or same-type claude / codex on mismatch. Investigate `rg <id> ~/.persona/node-attach.log` for launcher / failure start.
- Stopping headless assistant leaves no `已中断` (“Interrupted”) transcript (2026-11-06,#3308). `assistantInterrupt` kills process without [Request interrupted by user]; plugins reading items may send partial final passage as answer. Hub-only signal: assistant-target interrupt emits `session.user_stopped`; assistant records, plugins `GET /api/v1/assistant/reporters`.
- Gateway usage may cover entire turn, not request (2026-11-06,#3300). Some return 0 intermediate, accumulated inputs final. Claude estimates context from final, compacts unnecessarily; failed summary leaves all messages Prompt is too long. All usage consumers use `transcript.PlausibleContext`; node repairs damaged transcript in place with 0 (`docs/assistant/assistant.md` “Impossible gateway usage”). `stream-json` `result.usage` legitimately sums whole turn, can exceed window, not context metric.
- Paginated answers must not search again after finding first message (2026-11-06,#3497). Folded two messages on different pages: later search found second and discarded earlier portion, `RunAnswer` waited for missing first until QueuedMost 10 minutes, then final-text fallback. Search only until found, append all later pages (`channel.ReadTurn`, `TestReadTurnFoldedAcrossPages`).
- Folded-message answers cannot look only at the physically last row of each segment (2026-11-06, #4120). A reply is followed by `turn_duration`; without skipping it, `endsInWords` treated an already answered earlier turn as intermediate work and returned only the final turn.
  To determine whether a turn ends in text, skip thinking, empty text, `TurnDuration != nil`, and compaction notices just as `Stopped` does. Compaction may have a `Compact` field or only fixed legacy text. Apply compaction detection only to notices without `Task`; do not skip background tasks, errors, interruptions, or all system rows. Regressions cover durations for two turns, cross-page reading, compaction, and termination boundaries that must not be crossed, preserving the rest of `RunAnswer`'s stitching rules.
- Uncertain question-option matching must pass original text to Agent (2026-11-06,#3497 review). Initial option-name-plus≤3 extra words rule mapped `备份后删除` (“delete after backup”) to `删除` (“delete”), `SQLite，加密` (“SQLite, encrypted”) to SQLite, dropping conditions. Substring all matched Small fix; choices 1/3/5 made “3” third option 5. Wrong option worse than original answer: only grammatical filler beyond label, English word-start matching, reject question marks / numeric ambiguity (`channel.MatchOption`, `docs/extensions/channels.md` “Questions and approvals”).
- A failed start's background health check and its immediate detour must reserve the same probe before spawning the worker (#2428). Joining only after the goroutine starts leaves a scheduling gap that runs two checks; publishing health after waking waiters lets routing read the stale failure. A mapped home directory is only a path transformation: verify it on the destination before persisting a flexible task's move, and keep the original start error when every candidate fails that check.
- Completed task Agent / exited Claude can leave Opening Agent forever (2026-11-06,#4510). Node lists only 80 latest exited transcripts machine-wide; thousands daily evict it, hub lacks session, client assumes Agent without session. Unfinished to-do Agents now profile keep, listed regardless age. Also five-minute start timeout cleanup reused expired context, failed DB write, task neither Agent nor failure, still Opening. Cleanup needs fresh taskFailTimeout. Compare `todo_agents` vs `GET /api/v1/sessions`, then hub `task <id>: start:` logs.

- Deleting `assistant_always` does not revoke Claude's `addRules … destination: session` (#5867): the process can bypass PermissionRequest completely. Codex session/turn permissions and the Lark gate also cache grants. Keep assistant approvals in the manager, reset and wait for an existing process when revoking, and report restart-required when reset cannot be confirmed. A graceful stop can reap the launcher before its agent child exits and cancel the delayed group kill; permission reset must terminate the entire group, tested with a child that ignores SIGTERM. Never treat an empty reply from an old node as an acknowledged reset. Automatically answered parallel cards must not reinsert a grant deleted while their answer was in flight. Historical keys are hashes, so do not invent a path or command scope from `tool_name`.
- An active `todo_agents` row does not mean the agent works for an open todo (#2904): marking a todo done keeps its agents' rows `released_at IS NULL` and only drops them from `activeBySession`. A guard that treats any active row as "works for another todo" froze automatic links of a done todo's agent (store), while memory listed it on both todos and a later rename / reopen of the done todo moved the report back. Check `done_at` with the row; links that move an agent release the done todo's row, as a link by hand does.

- Blocking personal MCP tools is not enough to protect a work computer (#5061): `assistant.prepare` sends owner notes, knowledge-base locations, and plugin instructions before the first tool call. Refuse assistant starts and directory preparation before sending any of that; apply the same machine policy to Inbox conversation adoption and later note delivery, not just new starts. Project background enters ordinary Agent prompts through the shared `projectContext` helper, so gate that helper to cover assistant tools, planned tasks, and app handoffs together. MCP discovery can arrive before `assistant.send` returns its session ID; keep a manager-owned start authorization until the conversation origin is recorded.

- Machine trust updates can reach the manager placement and device configuration at different times (#5061). Treat either authoritative work restriction as sufficient until both have caught up, including physical aliases and current mesh paths. Keep new restrictions even when rejecting an invalid assistant placement during a handshake: existing node connections survive that rejection. Runtime `mcp.tools` discovery may omit the session even after startup: allow schema discovery from a recorded manager start, while keeping execution bound to its session and refusing node-derived backfilled origins.

- **A space-wide rule must be at every entrance to the write** (#5278): the health space's "confirm every write, whatever a rule says" was in `larkCheck` (the gated command) only; the gate proxy, which a script reaches with no command in front, let a write through when a rule allowed it, with no card. Now the address is marked health when it is opened and takes only the grant a card left. When a rule is added to one check, look for the other route to the same write, and test it by calling the address itself. Both must decide the conversation's space from the same place: the process's own, noted when its address is opened. The hook's directory moves with the model's `cd`; and a grant kept under a conversation id needs the address to have that id, which a new Codex conversation gets only when its thread starts (`codexConn.start`).

- **`confirmOutbound` is not a general confirmation**: it asks on the phone only in the health space and returns true at once everywhere else (`assistant/health.go`). A tool that must always ask the user (closing an Agent with `agent_archive` and `close`, changing an assistant setting) builds its own permission card through `s.ask`, as `ownSettingAction` and `archiveAndCloseTool` do. A dismissed, expired or unanswered card changes nothing, but it is no decline: only the user's own answer counts (`Resolution.UserAllowed` / `UserDenied`; one with `AnsweredBy` set was given for them), and the tool says nobody answered. Reporting an expired card as "the user declined" made a leader conversation tell its supervisor the user had refused a change they never saw (#3722).

- **A selection that does not read is not the defaults** (#1523): the knowledge base's default selection stops writing plain group messages, so falling back
  to it while the settings table is unavailable would silently drop what chats send (the connectors' cursors move on regardless). `kbSelectionNow` keeps
  everything (every rule on, days null: the behaviour before the selection) until the document reads again, and keeps the last document read while the
  database is away. That stand-in (`kb.KeepEverything`) is a stopgap, not a document: pruning under it would cut every line to `text_days`, including a
  mention the user keeps ten years, so `Engine.Prune` skips while `Selection.Stopgap`, and the history import neither revokes nor records an approval by it.
  A failed read with a document cached pushes the next try a minute out, or every ingest during an outage waits on the database in turn.
- **A whole read plan does not say `mine`** (#1523): `Selection.ReadChat` returns `whole` without the personal flags, since everything is read anyway. The
  history import still runs the filter around the owner's messages in a whole read, or the lines around them get the chat's rule (say 30 days) instead of the
  `mine` rule's longer days; `TestHistoryWholeReadKeepsTheOwnersContext` holds it.
- **Approve what is read, not which chats** (#1523): the import's approval is a fingerprint of every chat's read plan (`historyPlan.Approved`), compared with
  `ReadPlan.Wider` before every call, so a selection edit (a `PUT`, or `kb_history_plan`'s `set`) that reads more takes the approval back while one that reads less goes on. A chat's
  state keeps the narrowest plan it ran under: recording the plan a read started with would let a later widening take a half-narrowed read as complete.
- **A search hit knows more than the page** (#1523): a source's search says a hit mentions the owner, but the page read around it may not set `mentioned` on
  the message, and the selection would then drop the very message the window was read for. Positional reads take the hit's word for its own message, as
  `keepAnchorHits` does for unreadable chats. Only for the mention, never for who sent it: a hit with neither flag counts as the owner's for the window
  read around it, but setting `mine` on the page's message would write someone else's words as the owner's. The window (`near`) is all it needs.
- **A mention kept on its own is not near the owner** (#1523): `kb.AnchorFilter` keeps a lone mention without a window (`Keep`), so marking every message
  it hands out as within the `mine` context made a lone mention `mentions+mine` (the longer days) where daily ingestion says `mentions`. Its `Kept` hook
  names those; `pick` leaves them out of `near`.
- **A small-task model call is sized by what the model is today, not by the first measurement** (#1532): chat titles had 15 s per call and `max_tokens` 200 from a 2–3 s measurement. Once "Chat title" inherited a thinking flash model behind the gateway, a title-sized call took p50 6 s / p90 21.6 s and its thinking ate the 200 tokens (188–192 thinking + 4–8 output, `stop_reason: max_tokens`), so about a fifth of new chats kept "explore | the first message cut short" and some kept a two-character half topic that still parsed. A call off the user's path gets minutes, room for the thinking, and `Chat` instead of `Complete` so a cut-off reply (`Reply.Truncated`) is not taken as an answer; retry only what another try can change (`ai.Transient`: no answer, 5xx; not a 4xx or a model that isn't set up), each kind of failure once, so one doesn't use up another's retry; a naming that got no answer is a stand-in, done again later, and "again later" must read the first turn (`after_seq: 0`): `turns: 1` is the newest. To find which, match the hub's `context deadline exceeded` lines with the gateway's request log: the hub's cancellations sit at the call's timeout to the millisecond (14 996 ms).

- **Model listing and saving must agree on online launcher selection** (#1840). A cached provider's first machine can be offline even when another machine successfully lists its models. For a purpose without a bound machine, reuse `pickMachine` for model validation; if no reported candidate is online, allow saving without a live check. Keep known bound-machine validation and managed-launcher behavior intact. Cover the HTTP save and profile-apply paths, including effort-only edits, rejected online models, exact RPC destinations, and unchanged storage after rejection.

- A successful provider probe does not establish runtime compatibility (#2252). Direct-API calls resolve through `apiConfig`, while launcher tests also support transport settings that the node can honor. Refuse unsupported direct-API fields on saves, effective stored/preset probes, and runtime resolution; validate retained sealed headers without decrypting or rewriting the record, and allow explicit clearing. Preserve legacy anonymous Messages probes and real launcher settings: protocol alone does not distinguish a Messages launcher from a direct-API provider. Count synthetic upstream requests to prove rejected probes and runtime calls stop before sending.

- **A goroutine asleep through a delay costs a stack per event; keep only the fields that are read** (#2472). The finished push, a task's receipt, the launcher's usage-limit mark and the triage each started a goroutine per turn end that slept through its window (60 s by default): with a few turn ends a second the manager held about 900 sleepers, roughly 16 KB of stack each (StackInuse 6 to 21 MB in a soak). Schedule the unchanged body with `time.AfterFunc` (`afterSettle`, `notifyFinished`), reading the delay when the turn ends as before; keep the synchronous function for its callers and tests. The same goes for memory kept between lists: a `proto.Session` is about 1.5 KB, so a by-value copy of every machine's list kept for a few fields (the previous list's turn fields, the reply reminders' rows, the herdr marks' four fields) is megabytes at a few thousand sessions. Keep a struct of what is read (`turnPrev`, `tokenRow`), or only the rows a reader can pick, and decorate one row (`decoratedSession`) rather than a machine's list to read one. A test of such a change compares against the full path's row (`TestDecoratedSessionIsTheListsRow`) and counts goroutines in a `synctest` bubble rather than sleeping.
- **A hub deployed with new prompts does not reach a running assistant by itself** (#3235; #3068's ring cards kept coming out
  as stacked bars). The assistant's and the health space's CLAUDE.md were written only by `assistant.prepare` when a
  conversation started or a spare warmed; messages to an existing conversation go through `session.send` and never wrote
  it, and the long-lived claude process reads CLAUDE.md only at start anyway. Now the hub compares both right after it
  starts and every minute (`instructionsLoop`) and rewrites what differs, and the node remembers a digest of the
  CLAUDE.md each process started with and resumes the conversation in a fresh process on the next idle message once it
  changed (`instructionsStale`). For "the new prompt has no effect" look first at the CLAUDE.md's modification time in
  the assistant's directory on its machine and at `wrote the CLAUDE.md` in the hub's log.
- **What is handed out only once must not be dropped by a shortcut** (#4256). `plugins.assistant` gives a machine a
  skill's files once and counts them as sent when it answers, but the minute refresh asked for them before deciding that
  an unchanged CLAUDE.md needed no `assistant.prepare`, and dropped them: after an upgrade that changed only a skill, the
  assistant's machine kept the old files for over an hour. The node then reported the skill missing
  (`plugin_skills_need`), the files were queued to go again, and the next refresh dropped them the same way. Now the
  refresh compares the skills' names and digests too, never skips an answer that brings files, and prepares a directory
  again on its next round when the node reported skills missing there. For "the new skill has no effect", compare the
  modification time of `~/.persona/plugin-skills/<name>/.persona-plugin` on the assistant's machine with the upgrade's.

- Inbox fixtures must wait for the first asynchronous roster count before writing directly to their items (#3459). `inboxReady` returns after scheduling `tellInboxNeedsYou`, whose goroutine holds the Inbox mutex while reading the map and status fields. Legacy fixture seeding bypassed that mutex and could crash the whole test process, even without the race detector. Use `waitNeedsYouIdle` at the fixture readiness boundary; later concurrent mutations must still use the Inbox mutex. Keep the real teller enabled and verify the complete Inbox test group under `-race`.
  Observing a fake send is not the end of `tellSoon` (#7035). Before restoring a swapped `Service.store`, hold the fixture's handling pass and call `deliverNotes` synchronously for the item: its per-item lock waits through the background save, while `toldConv` returns as soon as the send is recorded.
  Scheduling is not completion for transcription readiness either (#7232): after a fixture calls `transcriptionReadinessChanged`, `hubTr.wg.Wait` must precede a direct `Service.store` replacement because `transcriptionJobsCheck` reads settings through that store.
- **An optional object in a tool's schema can come filled with zero values** (#3386): runtimes that fill in every field
  of a tool's schema send `agent_send`'s `task` as `{"title":"","done_when":[],"expect_minutes":0}` instead of leaving it
  out, and the server took any `task` as a hand-off: a reply to another conversation's agent was refused, and one to an
  agent on no task made a supervised task. A handler that branches on whether an optional object is there treats one
  with nothing in it as absent (`givenTask`), keeps anything else as it was (another type, a wrongly typed field or an
  unknown key such as a misspelled `title` must not turn a hand-off into an untracked reply), and its schema allows null
  and requires none of the object's fields the handler does not.

- A report written before a tool call vanished from the push and the Lark answer. A main-chat turn wrote a long report,
  called `agent_send`, then closed with one short line; the push and the Lark bot showed only that line, and the owner
  took it for another conversation's message. Both read "the answer" as the last text (`Meta.LastText`) or the texts
  after the last tool call (`channel.Answer`), but the model often reports first and acts after. The hub's answer is now
  every text of the turn from the first, minus a passing line of at most two sentences right before a tool call
  (`transcript.AnswerParts`), for the push body and the Lark answer alike. The chat pages still fold all but the last
  text (super rule 2); changing them needs the administrator's approval.

- A source the manager hands to the Inbox itself, rather than a registered connector, must be named in every round's `names`, the Feishu-chats rounds included (#2039). The Inbox keeps a source's held-back count and read error only while rounds name it (`recordHeld`, `recordReads`), and a chats-only round, which follows every Lark event, would otherwise drop a source subscription's "waiting for triage" count within seconds. Name such sources cheaply in those rounds (no items read) and hand in their things only in rounds that read the connectors (`extmgr` `readSubscriptions`).

- **`/stop` has to read the conversation's state, and a state that may be stale is not "nothing running"** (#3793). The
  command and the progress card's Stop button shared `stopConversation`, which answered "stopped the turn" whenever the
  conversation existed and sent `session.interrupt`; for an ordinary herdr agent the node answers that by pressing Escape
  in the agent's terminal (`node/node.go`), so an idle window got a keystroke and the owner was told a turn was stopped
  that had already finished. Refuse only when the state says the last turn ended (`unread` / `idle`, and not `Starting`)
  — a state that cannot be told still tries, the owner asked — and let a live run this chat sent into that conversation
  win (`channel.Run.Seen` false: the turn may just not be read as working yet), or a `/stop` sent right after the message
  stops nothing.

- **A table row is split at every unescaped `|`, inside a code span too** (#3852). GFM cuts a cell at a pipe the row did
  not escape, so `| `a || b` | 或 |` really is a row with four cells while its header has two; GFM draws only the
  header-width cells and ignores the excess, and `answerTables` (`feishubot/answer.go`) built a row the same way, by
  looping over the header's columns alone, so everything past the header's width — here "b`" and "或" — never reached
  the card, with nothing said. The rich-text fallback kept those words, so the same answer read differently as a card
  and as a refused card's text. Those words are still the row's: keep the surplus cells in the last column, joined with
  " | " and with empty ones skipped (a trailing `||` adds no junk), clipped and escaped like any other cell. When
  changing how a table row is laid out, compare its card with its fallback.

- **Text a client draws as Markdown is cut with `clipMarkdown`, not `clip`** (#3818). The briefing card quoted the model's answer through `textfmt.Clip` (exactly n runes plus an ellipsis), so an answer over 1500 runes was cut inside whatever it was in: an unclosed code fence turned the rest of the quote into code, and a cut inside `**`, a code span or a link left half a marker or half a URL. `clipMarkdown` (`assistant/mdtext.go`) cuts on a line break where it can, closes a code block the cut lands in, and moves the cut back out of an unclosed span, emphasis or link; it also flattens a `persona-card` block to its title (`wire.AssistantCardText`), so draw charts first with `feishubot.PersonaCards` and cut after, as `answerCards` does for the bot's answers. Plain fallback text (the push body, a notice's fallback) keeps `clip` / `textfmt.PlainMarkdown`; only the Markdown element takes `clipMarkdown`. Test through the same builder the card uses, not the clippers alone, and assert the shape (`fences % 2 == 0`, no `](https://…` left) rather than an exact string. The same trap caught the task report's item body (#4093): it drew the answer with `CardMarkdown` *after* `clipMarkdown`, which flattens a chart to its title on its own, so the owner read the report with no numbers. Any Markdown a client draws that is clipped first must have `PersonaCards` run before the clip.

- **A finished run's last write into the progress card has to wait out a card rate limit** (#3915). The progress tick
  answers 230020 by pausing its updates (`prog.quietUntil`), but the answer's write comes right after a tick's, and a
  card update (PATCH) gets no retry of its own on 230020 (`feishubot/api.go` leaves that wait to the caller, because the
  tick may be holding the run's progress lock): the answer was then posted as a card of its own while nothing wrote the
  progress card again — the run had ended — so it kept saying it was still working, and a quiet end wrote nothing at
  all. `botUpdateAnswerCard` (`feishu_turn.go`) waits `answerCardWait` (5 seconds, the first progress backoff) and
  tries once more before the answer falls back to a card of its own; another refusal still falls back at once. Waiting
  synchronously is the whole fix here: the run is over, so no tick can write that card later.

- **A quiet ending says nothing when the run has no card left, so a timeout must carry a note** (#3930). `channel.Run.Check`
  returned the 2-hour held timeout (`HeldMost`) with no note, and `checkChannelRun` turns an empty note into `Quiet`; the
  Lark sink answers a quiet ending only by rewriting the card it still has (`feishu_turn.go`), and a held run has none —
  `finishChannelRun` gives the card back with the message (`delete(r.Ext, "card")`) — so the owner's first answer stayed
  the last word and the chat never learned the wait was over. Both limits now end with `channel-turns.gave_up`, as the
  docs promise ("Timed out, see the app"); a run with no card gets that line as a card of its own. Plugin channels, which
  send a note as plain text, stop being silent the same way. When a step ends a run without an answer, give it a note, or
  check what the sink does with a quiet end and no card.

- **A quiet ending still needs a card to land on** (#4101). `channel.Run.Decide` ends a held run whose background work
  reported back in a turn without words as `Quiet`, and the Feishu sink answered a quiet end only by rewriting the run's
  card — but a held run gave its card back with its message, so the owner was told "N background tasks are still running"
  and then heard nothing when they finished. The sink now builds the ending line for every quiet end and, with no card,
  posts it as a card of its own (the same rule #3930's timeout relies on). The result stays quiet: the line is
  "Done · took X", not an empty answer. With no card left it is posted for every ending except a message the owner
  recalled, which gets no card of its own (that one only gets the card the run still has rewritten), and the line says
  how the wait ended (#4228); and the quiet branch must not swallow errors the way it first did (#4229): a card update
  refused for anything but a rate limit falls back to that card of its own, and an unreachable failure is returned so
  `finishChannelRun` keeps the run and tries again — posting under an idempotency key made from the run id (`endKey`),
  because the first attempt may have landed before its answer was lost.

- **A card nests a list by four spaces per level, not by the Markdown rule** (#4180). Lark's Markdown component documents
  one nesting level as four leading spaces (a list's sub-items are indented four spaces per level), while CommonMark — and
  so a model writing an answer — indents a sub-item by its parent marker's width: two spaces under "- ", three under
  "1. ". The answer's Markdown was passed through unchanged, so every nested list arrived as a flat list of siblings and
  the hierarchy the answer was written with was gone. `CardMarkdown` now maps each item's observed indentation to a level
  and writes four spaces per level (`listIndent` in `feishubot/card.go`) in one pass over the whole answer, so the levels
  carry across a fenced code block and a list goes on around it; a list item's continuation lines, a list inside a
  blockquote, and four-or-more-space indented code at the left margin stay as written, and a paragraph at the left margin
  ends the list. Test the conversion through `CardMarkdown` and through the answer builder (`answerCards`).

- **A list shown to a client holds the thing it names, not the records that produce it** (#4184). `feishu.view`'s
  `agents` is "the Agents you have talked to in Lark", but it was built by walking the chat's thread bindings — and the
  chat binds *every* card it posts about a conversation (the list card "Chat here" was pressed on, each progress card,
  each answer card, each finished card), so one Agent talked to for a while filled the settings section with dozens of
  copies of itself, in map order, keyed alike on both clients (web and iOS key those rows by session id). Fold the
  bindings by machine + session, let the newest name the row, and sort it (rows of the same time by session): the same
  source, read as what the field means. A test that binds three cards of one Agent along with two other Agents, and
  loads the view ten times, shows it. The settings pages are the only readers — web and iOS key the rows by session id,
  Android draws them in order — so none of them changes.

- A read-only worker already in plan mode must be able to report without changing it (#4201). New Claude read-only tasks start in `bypassPermissions` (#4864), but Codex and legacy plan sessions still need this path. Denying `ExitPlanMode` does not remove
  the provider's separate permission request for `report_to_supervisor`. Recognize both built-in namespaces with
  `compat.MCPToolNames`: Paseo preserves the requested name, including the compatibility alias (#4318). Allow only
  that tool for a supervised task with a current started read-only plan binding. Return `Allow` without `Always`, `Approve`, or `Mode`;
  the terminal hook and Paseo then send no permission or mode update. Reuse `PlanTask.TodoID`, whose old bindings are
  retired on takeover, rather than treating a historical read-only plan as authority for a new writable worker.

- **A quiet ending is still an ending: it has to say how the run ended** (#4228). `channel.Run.Decide` returned a held
  run's quiet result (`&Result{Quiet: true}`) from an early branch, before the `End` below it was computed — and it left
  `Stopped` empty too — so `End` stayed `EndDone` whatever had happened, and once #4101 posted that line as a card of its
  own the owner was told "Done" about a turn that was interrupted, hit a model API error, or lost its computer. Both
  endings now come from one `endOf(rd, l)` (the turn's last notice, the failed turn, an offline machine), and the sink
  posts the card of a quiet end unless it was recalled (`end != channel.EndRecalled`), not only when it ended as done:
  gating the post on `EndDone` would have made every bad quiet ending silent instead. A branch that answers a run owes
  it the same ending fields as the normal path.

- **A model asked for an option's label copies whatever the input put next to it** (#3929). `delegate_input.md` lists an option as `label：description`, and with Chinese descriptions the delegation model often answered the whole line (`表驱动：和这个包现有的测试一致`): #3923's live runs saw it in 4 of 4, 2 of 5 and 1 of 4 runs of three cases. `interactionAnswers` found no such option, so a choice the assistant had decided to answer went to the user. `optionLabel` (`assistant/tools_apps.go`) now maps an answer to a label only when it writes out exactly one option, its label then a separator (`：`, `:`, a dash, or brackets) and that option's own description, with quotes or spaces; anything else is still refused. Don't widen it to prefixes or the model's own words after a label: those are free text, and a refused answer only costs the user one question. When a prompt shows options with extra text, expect it back and test the hub's check with the exact line the prompt renders.

- **A new scheduled-task `repeat` must survive the apps that don't know it** (#4399). iOS decodes an unknown `repeat`
  as `once`, and Android and the web send it back as they got it, all without the new rule's field. A `PUT` without
  `cron` therefore keeps a cron task's `repeat` and `cron` and saves only the rest; otherwise switching such a task
  off on an older iPhone turned it into a one-off with no time. Keep the new rule's own field out of a body whose
  task doesn't have one (`cron` is sent only when set), and gate the editor on the hello feature (`task_cron`).

## Cancel confirmation-blocked Agents without granting approval (#4277)

Keep force archive separate from ordinary busy checks. A cancellation path must close the provider or terminal,
reject or withdraw pending cards, and keep other work protected. A cached blocked inventory after close can undo
a newly saved archive unless the closed timestamp fences that old snapshot. Persist the archive and a future
close timestamp in one write before closing, then replace it with the real close time. A failed close removes
the provisional record with an independent bounded context, including when the request was cancelled.
Paseo's CLI `archive --force` skips
a client-side busy check; its archive request has no force field and closes the provider runtime. Check the actual
archive receipt before removing the Agent from a bridge's local list.

## Standing authorization and notification evidence (#4398)

A quoted instruction in task notes is not a durable grant. Keep explicit grants in manager storage, require the user's
own approval when adding them, and compare revisions when a setting approval can race App revocation. Recheck the
grant after condition reads and serialize its final answer against deletion in the database. Commit the answer and
its task decision together before sending it to a hook. A plan approval must select `default` explicitly: an empty
mode can fall back to bypass permissions on the node. Count confirmation dispatch at `tellUser`, not interaction
creation or app presentation; quiet tasks and supervisor routing also show cards without ringing the user.

## A card the chat waits on must be in the chat (#4883)

A Persona conversation's first Bash waited three and a half minutes for approval while its card was only inside the
closed Needs you tray on the web (Android too) and on the Home overview; the owner typed "what happened?" twice and
thought the assistant had hung. A request that blocks the turn is drawn above the composer on every client, like an
Agent's; a tray or overview may count it and link to it, never be its only place. The same incident: a message typed
mid-turn got no reply for minutes because the mid-turn note went in only with the optional background setting on, and
the only line between tool calls was English to a Chinese owner. The node now always adds the note to a busy Claude
(`midTurn`), asking for one sentence in the user's language before the next tool call, and the CLAUDE.md asks for the
user's language in process text too.

- Match Inbox PR/issue comments by full task URLs before the AI twin gate (#5578). A to-do may record its link only in `pr_url`, its title or its notes. Keep host, repository and PR/issue kind in the identity, leave ambiguous matches to the judge, and use a separate per-message signal cursor: the twin's `Of` cursor tracks handling, which does not run while it is off. Persist associations before forwarding them, and retain the existing supervision opt-in and exclusions. A URL-only Agent must still obey the age limit before its first automatic handling; creating its task association is not permission to process old backlog. On load, seed previously linked items' signal cursors from the stored snapshot so a restart is not paid-for news; count only batches observed after that snapshot.

## Disable ToolSearch per server, not for the whole assistant process (#5838)

Setting `ENABLE_TOOL_SEARCH=false` for Persona's assistant also forced every claude.ai connector's tools into each request (one connector alone was hundreds of tools and most of the context), causing repeated compaction. `personad mcp` now marks only Persona tools with `_meta: {"anthropic/alwaysLoad": true}` when `PERSONA_ASSISTANT=1`, and the assistant no longer disables ToolSearch globally. Keep other servers on Claude Code's normal deferred loading. A regression test checks both the metadata and the process environment.
## A leader writes to an owner who has seen nothing else (#5842)

Leaders spend most of their turns with the main chat, other leaders, scheduled runs and supervision messages (on one
hub, 20 of the 70 messages a leader got in a day were the owner's), and they compact often. Their replies to the owner
then carried that world along: plan item codes from their own files, shorthand from a compaction summary, "as we
aligned today" about something settled in another conversation, and "approve it?" for a change the owner had not been
shown. The owner could not follow and stopped trusting the replies. The CLAUDE.md now opens its interaction section
with the rule that everything written for the owner (replies, cards, `notify_user`, `leader_progress`) assumes they
have seen nothing else: plain names instead of codes, one sentence on where an unseen thing came from, and an approval
only for something they were shown, with a link.

## `--tools` filters ToolSearch too (#5839)

With tool search back on (#5838), the other MCP servers' tools are deferred, and only `ToolSearch` loads them. The
assistant names its built-ins with `--tools`, and that list filters `ToolSearch` like any other built-in: without it the
request lists the deferred tools by name but nothing can load them (the transcript's `deferred_tools_delta` says
`toolSearchAbsent: true`), so the user's own MCP servers and claude.ai connectors were unusable from the assistant.
Measured on Claude Code 2.1.321 with a 256-tool connector: with `--tools Bash,Read` the connector's tools were
unreachable; with `ToolSearch` added the model loaded one on request, and the first request stayed ~15K tokens.

## A leader's split must be somewhere the owner can see (#5846)

Owners lost track of their directions because a leader kept its breakdown in its own files and turns: the direction tree
only listed tasks with an Agent, so the pieces nobody had started yet were invisible, and the owner could only ask the
leader and read an answer full of its own labels. A leader's `todo_create` now opens a to-do under its direction, and the
tree shows pieces no Agent has worked on as `planned`. Two traps found on the way: such a piece has an `origin`, so the
To-dos SOP shelves it after 48 h untouched like any agent-made to-do (it leaves the tree; the leader touching it keeps it),
and the stuck rule must not count a piece that never started, or every planned piece older than a day reported stuck.

- Match Inbox PR/issue comments by full task URLs before the AI twin gate (#5578). A to-do may record its link only in `pr_url`, its title or its notes. Keep host, repository and PR/issue kind in the identity, leave ambiguous matches to the judge, and use a separate per-message signal cursor: the twin's `Of` cursor tracks handling, which does not run while it is off. Persist associations before forwarding them, and retain the existing supervision opt-in and exclusions.
## A source read by pages must settle what left the page (#5993)

The Inbox's mail rounds read each account's newest 40 INBOX messages, and every rule that files a mail item (read
elsewhere, the 7-day window) ran only for threads on that page. Mail archived or deleted in the mail client, or pushed off
by newer mail, left its item open for good: nothing else files an open item, so the open side grew with the mailbox
(600 open items after 30 days of 60 mails a day, all read the same evening). A rule that only fires on what a bounded read
returns needs a second path for what it no longer returns: here past-window items are filed without asking, and the rest are
looked up by UID once a round (`inbox.mail_flags`), bounded, with a failed lookup changing nothing. Tests of such a round
must wait for the background triage pass (`waitTriage`) before setting a tier by hand, or the fallback tier overwrites it.

- Literal `for` lists need per-value classification (#6266). Straight-line assignment tracking cannot resolve a loop's route segments or executable, and accepting GET alone misses action routes such as `/documents/delete`. Bind only literal lists in a completely inspected shell tree, retain quoting and reject unquoted values that could split or glob. A later assignment, state-changing builtin, executable-environment change or nonliteral nested list invalidates the entire attempt; never keep a partially expanded prefix. Resolve command positions before unwrapping so a variable cannot become a gate, CLI, shell or wrapper. Bound nested expansion and classify every resulting call through the existing read and hard-boundary rules. Both Claude and Codex adapter tests must count actual fake-hub approval cards; classification fixtures must never execute the supplied commands.

## Clear a "tell it later" record only after the tell went (#6306)

After a node restart, `recoverAssistantTasks` cleared the record of a conversation's lost background tasks (written to
the hub) before sending the message about them. When that one send failed (the launcher not up yet, the 1-minute
deadline), the record was gone and no later connection told the conversation: an assistant that promised to report on CI
waited forever. A record that exists only so something is said later is cleared after the saying succeeded, and only if it
still holds what was said (the woken conversation may already have re-armed new tasks). Being told twice after a send
that timed out but was delivered is the cheaper failure.

## A "which of these is it" model picks one even when none is (#6424)

The Inbox judge was asked which open task an item belonged to and answered with an id the code only checked was among
the candidates. Given seven unrelated tasks, it linked a GitHub bug report to a broad task whose notes shared a few
words with it, and the link stuck. A selection model needs two guards: narrow the candidates by something exact first
(the repository, the thread), skipping the call when none is left, and accept an answer only with evidence the code can
check, such as a verbatim quote from the chosen candidate.

- A legacy `bot` Inbox kind is not enough to reject a human Lark conversation (#5680). Check its explicit sender
  identity, keep forwarded notification text as content, and apply one eligibility predicate before both URL routing
  and the model judge. Correct old excluded links through the bounded maintenance write, with rollback on save failure.

- A streamed reply is only as smooth as its slowest stage, and the model's output is bursty (#6651): Claude's
  `text_delta`s come a few dozen characters at a time about every 350 ms, up to 2 s apart. A leading-edge throttle
  ("send if 80 ms passed since the last send") holds back the rest of every burst until the next one, and a client that
  draws whatever arrived shows one jump per burst however fast it renders. Throttles that drop in-between values need a
  trailing send; a typewriter effect has to pace by the stream's average rate, not the last burst, or it finishes each
  burst early and waits. Measure on recorded streams (`scripts/mock/stream_traces/`, `/mock/say?trace=…`) and report the
  gaps between visible text changes and the added lag, not only frames: an evenly timed mock stream hides all of this.

## "Busy" must mean a request is out, and one probe must not strand the rest (#6698)

A report said Inbox triage stayed "the model is at its concurrency limit" forever after a short upstream outage,
with nothing in flight, and suspected a slot released only on success. The gate (`server/internal/ai/limit.go`)
pairs every `admit` with a `done` and no error path skips it (`TestEveryExitGivesItsSlotBack`); what kept the
status wrong was how a pause ended. One request went out to find out and every other was put off at once, until
that request's whole streamed answer had been read: a round right after a pause sorted one item and reported the
rest as busy until a later round, and a 404 or 5xx during the outage showed as busy too, because only the probe
reached the upstream. When one request finds something out for many, let the others wait for the finding (bounded
by their own deadlines) and act on the first signal that settles it (the 200), not on the end of the work. Before
fixing a suspected leak, count: a stress test over every exit path with the counter read afterwards settles it
faster than reading the code again.

## A send whose answer came too late may have gone in (2026-11-12)

The manager waits 90 s for a relayed message (`drainRelays`) and kept it queued when no answer came. The node had
written it after the first of two session-list rebuilds inside the send and was still building the second when the
time ran out, so every later drain wrote it again: one conversation read the same message 32 times in an hour, and the
messages queued behind it waited. A send in doubt is not a failure. Relays now carry one `once_id` per message
(`relayMsgID`), which the node sends once (`sentLog`); a try in doubt first looks for the message's line in the
conversation's latest turns (`relayArrived`: context blocks left out, no line older than the try, the sending
conversation as its source), and one not found then is looked for again before the next try (`relayLanded`), which
covers a line that lands late and a node restarted in between; and the send pushes the session list in the background, waiting for it only briefly
(`notifySessionsSoon`, `listPushWait`). The id is deliberately not a `client_msg_id`: a line tied to one reads as the
owner's own message everywhere (its source label, task briefs, user-input counts, extensions). The web had the same
blind spot for the owner's own sends: a send answered 502 `delivered: "unknown"` stayed a failed bubble unless the hub
tied a transcript line to its id, so the owner sent it again; a send in doubt now also settles by its words written
after it (`inDoubt` in `web/src/lib/outbox.ts`; `isEcho` in `web/src/lib/echo.ts` allows 5 s for the clocks, not
`CLOCK_SLACK_MS`); one whose copy arrived before the answer did was already settled while it went (`flight.echoed()`
in ChatView). A refusal never settles by words.

## Name the thing an event is about, and keep what was done for it (#5718)

Completion handling started only on a session list going from `working` to `done` / `idle`, and remembered "this stretch
had its receipt" in memory. A node whose inventory never said `working` for Codex left finished turns with no receipt, no
judgment and no supervision message; a manager restart forgot the receipt and wrote a second one when the same end was told
again; a stale `working` row arriving after the end judged the same reply twice. Three faults, one cause: the work was
keyed to the event that told of the turn, not to the turn.

- Key the side effect to what it is about (here to-do, machine, session and the runtime's own turn end), give its row an
  id derived from that key, and let the database refuse the second write.
- Keep each later step on that key too (judged, told), so a process that stops between two steps resumes at the next one
  instead of starting over or stopping for good.
- Do not make a status transition the only trigger for something that must happen. Statuses are another component's
  guess; reconcile from the record of what happened (the transcript) in a sweep that already runs, and bound what the
  sweep reads per tick.
- A reservation taken before a check must be given back on every path that leaves without doing the work, or one failed
  check consumes the work for good (the second look at the task's agents failing on a dropped link).
- When such a ledger is new, decide what it does with everything that happened before it existed. Here a turn that ended
  before the ledger was first kept is recorded as seen and nothing is made of it; otherwise the first sweep after an
  upgrade would have judged every stopped task again.
- A recovery path changes state last. The first version took the start nobody heard (stage back, check cleared, review
  wait cleared) and then ran the end's handler, which could still refuse: a cancelled task or a usage-limit wait lost its
  state for an end that was never handled. Run every refusal of the path being recovered first, and only then the
  missing step.
- Every way a recovery look can end without doing the work needs an answer to "will looking again help?". Never (the
  task refuses this end): record it and stop. Not until something changes (a usage-limit wait, a computer that is away):
  do not look. Perhaps (the node did not answer): back off, and start over when the row moves. A boolean "not done"
  that retries every tick reads the same transcript for hours.
- Compare rows by what says the thing changed. The transcript's size says it was written to; the row's time can move
  on a later list with nothing written, and a row compared on both was never the same row twice.
- A mutex that is held across database calls must not be reachable from under a hot lock. The ledger's mutex is
  held across its reads and writes; a counter that happened to live under it was read and set with the task lock
  held, so one stuck write would have parked every user of the task lock behind it. Before putting a field under a
  lock, ask who reads it and what they hold: in-memory state that a hot path needs gets a lock that is held across
  nothing, and the order is written on the struct in one sentence a reader can check. Test it by stalling the store,
  not by reading the code.

## Reconciling a lost start needs an exact id, not a shape match (2026-11-13, #6908)

When `session.start` never answers, the hub lists the machine and adopts the session its start probably created. The first version guessed from shape: not in the pre-start snapshot, `StartedByPersona`, same launcher and cwd, a fresh sign of life, newest wins. Three rows are indistinguishable by shape: a session the owner started by hand also carries `StartedByPersona` (the app's start path stamps it), and two workspace-none tasks run in the same directory with no workspace claim between them, so each could adopt the other's session — and nothing excluded a session already linked to another open todo. A heuristic over mutable, shared signals (`UpdatedAt`, cwd) cannot identify one specific start. The fix carries the identity instead: the hub generates a v4 UUID and sends it on the existing `session.start` `session_id`, the node starts Claude with `--session-id <id>` on every Claude path (the sync terminal path had been passing an empty id), and adoption matches that exact id first with no other condition. The heuristic is only the fallback for a node too old to honor the field, and even then it skips every session in `todos.activeBySession` and fails closed when the todo book cannot load. Lesson: when an action's result can be lost, make the request carry a unique correlation token and match on that; keep a heuristic only as a version-compatibility fallback, and have it exclude resources already claimed elsewhere rather than merely rank candidates.

## A hidden effective cadence overrides the interval the user configured (#7619)

#3811 gave interval follow-ups in leader conversations a hidden 10-minute cadence while an open supervised todo had an active agent: `nextWithLeaderCadence` derived extra runs from `LastRun` while leaving the stored `At` / `EveryMinutes` on the original rule, and the run copy's `cadenceEvery` made `taskPrompt` print "每 10 分钟". The stored task still showed 30 minutes / 1 hour in `list_followups`, so nothing visible explained why a patrol the user asked for every 30 minutes fired every 10 minutes from its first run, and it became the main chat's biggest token sink. Lesson: a scheduler may have exactly one effective cadence, and it must be the one stored on the task and shown in every view and run prompt; an "accelerate while busy" rule that is invisible to both storage and UI is indistinguishable from a bug. If such a rule is ever wanted again, it must be an explicit, user-visible setting with its own stored field, not a second rule derived inside the scheduler. Keep interval advancement in one place (`runDue` advancing `At` by `EveryMinutes`); tests assert the prompt text states the configured interval even while supervised work is open.
