# Lessons: assistant (headless Claude / Codex)

- A channel topic may have two routing keys during a Labs rollback (#17664). Reserve both keys at admission, use the active
  binding under the other key on a lookup miss, and refresh both known bindings after delivery. A `/new` can supply only one
  key. Clear the other bindings for the same conversation so the next message starts fresh.
  Keep the accepted routing reference through queued commands and delivery (#17762). Recomputing it after a Labs switch can
  make a queued topic reset change the channel's default target. Use a synchronized clock in rollback tests because
  background snapshots also read the clock, including from managers that have been reloaded.

- A start failure awaiting retry is open for supervision handover even though the stored row has `done_at`
  (#17273). Use `closedForGood` to distinguish it from completed or cancelled work.

- Node preparation can call back into the hub for settings (#16065). Release the database connection before the RPC
  and use the existing durable start claim and candidate receipt to reconcile it. Recheck the epoch when mapping the
  receipt and keep late candidate identities for cleanup. A batch larger than the pool should exercise a real
  same-pool read while the node is still waiting.

- A supervision transcript fixture must not use push policy as its write switch (#15498). Side conversations can
  receive user-level supervision with `hold_pushes=false`; it still writes a user item. Select fake writes by message
  source, keep missing-item injection independent, and name push-held fixtures explicitly. Test both notification
  policies without relaxing transcript confirmation, once-ID or retry assertions.

- Mixed core/module fixtures must keep testing the core when a module is absent (#15500). Check generic scheduled
  failure routing first, then make the optional briefing hook assert absence or its normal fallback. Do not skip the
  whole test, weaken the default-build assertion or wait for a notification from a module that is not loaded.

- Timer completion accounting belongs to the registered callback wrapper (#15499). A direct call of a stopped
  callback guard has no matching `WaitGroup.Add`; decrementing there can panic and strand teardown on a negative
  counter. Keep the real timer's Add/Done pairing and prove shutdown drains an in-flight callback without rearming.

- A single-reviewer guard must preserve conversationless supervisor fallbacks (#15497). A legacy reporting-only
  direction can retain its recorded assignment while its review reminder goes to main. Authorize that same reviewer
  without authorizing an unrelated conversation or taking over a real supervisor's offline queue; test direct, busy
  and restart-restored delivery with the assignment unchanged.


- A scheduled-workout fixture must clear unrelated default routines before admission (#15442). `acceptWorkout`
  appends its task to the existing schedule; unlike `setTasks`, it does not replace the morning and evening routines.
  Near their due time, a catalog test can count both the workout report and a briefing, while waiting for only the
  first recorded run can hide the second dispatch. Isolate the fixture, verify the workout's `RefID`, and reproduce
  with a due default routine and settled asynchronous work; keep the single-send and requested-binding assertions.

- `assistant.New` must not read the database (#13408). Many tests build the Service on a fake store that embeds a nil
  `Store` and implements only the methods it needs; one new `Setting` call in `New` (reading the feature-module switch)
  panicked dozens of unrelated tests. Read what the constructor needs where the manager starts (`main.go`, next to the
  real store) and pass it in through `Options` (`ModulesOff`, `docs/architecture/core-and-modules.md` "The switch").

- Claude Code's permission modes do not come from a runtime's optional capability list (#13233). `proto.Launcher.PermissionModes` is defined for non-Claude agents; applying a partial list to a Claude-compatible custom launcher drops an inherited bypass choice and silently returns the conversation to the profile's settings. Resolve the backend first: Claude accepts `proto.PermissionModes`, while a non-Claude runtime's declared list stays authoritative. Cover the manager's start params and a non-Claude control. Existing nonempty assistant profiles are not reseeded, so their `permissions.defaultMode` and unrelated settings survive node upgrades; pin that boundary with a real temporary profile rather than rewriting it to make a test pass.

- Persist permission-mode provenance, not just the effective value (#13653). The same saved `auto` can be an inherited default or a deliberate choice, and those cannot be distinguished later from `AppMode` or a transcript. Carry the manager default separately from `permission_mode`, mark explicit choices, and test legacy resume, current-default resume, a live process and an explicit choice. Apply the manager default only to Claude Code resumes: another runtime declaring the same mode does not make its saved mode an inherited Claude value. Older peers omit the provenance fields and keep the old behavior.

- A command that returns text to the message pipeline must keep its explicit destination (#12700). In the official bot DM, resetting `current` for `/new <first message>` was insufficient: pending-card handlers could consume the text, and parent/root bindings could restore an old target. Carry the fresh Persona target past those handlers while retaining reply metadata for quoted context. Test through `FeishuMessage` with old Persona/Agent bindings, an answerable card and duplicate delivery; keep bare `/new` and ordinary replies as controls.

- A launcher name does not identify an execution backend (#12791). Scheduled Assistant conversations must pass
  `purpose: assistant` to model discovery and argument-forwarding checks, just as ordinary Assistant messages do.
  On an exclusive Agent engine, a same-named provider can advertise different models and thinking levels from the
  shell CLI that actually runs the conversation. Keep their cache partitions separate; do not lower a saved effort
  to match the wrong catalog. Test the due-run, retry, workout admission and task-edit paths with conflicting
  catalogs. A structured, deterministic pre-dispatch configuration refusal retains one failure/fallback notice and
  must not arm an unchanged automatic retry. Similar provider prose and unavailable discovery remain retryable or
  unknown; retry log counts alone do not establish how many requests reached a model.

- Tool-result images use the user role in transcripts (#12573). A held channel run must check an image's `ToolUseID` before treating it as owner input; otherwise reading a screenshot silently discards the continuation answer. Apply that distinction in the shared classifier used by polling and live session items, and retain the unrelated-turn guard for actual owner text and photos, even after a wake or tool image.

- The node process's PATH is not the assistant's shell PATH (#12483, 2026-11-21). A node started by a service manager has a minimal PATH, while the assistant's Bash runs through the user's login shell, whose PATH restores version-manager directories. `trustedPATH` then saw every `export PATH="<version manager>/bin:$PATH"; lark-cli …` as replacing the executable and asked for a whole-command card, one per call, even for `--help` reads. Ask the login shell for its PATH once (a marked line, cached; retried after a failed ask) and trust those directories; a directory outside that set and every write still ask. Tests seed the Node's login-shell directories instead of starting a shell.

- Threaded extension input needs ordering at admission, before its receipt returns (#12616). Independent receive goroutines can both see an unbound thread and start separate conversations, or restore a binding after a later `/new`. Claim seen, capture the binding generation and reserve the position in one critical section before persisting the receipt; roll the claim and reservation back together if persistence fails. Keep commands, route lookup, start/send and binding ordered without holding the shared state mutex across RPCs. Include the generation in the queue key so rebinding does not wait for an old RPC, and carry that accepted generation through the guarded thread/default binding writes and run creation. A late old response may complete its node request but cannot restore channel state. Keep other keys and unthreaded input independent and remove drained entries. Exercise the public `ChannelReceive` entry: calling the forwarding helper directly does not verify admission.
- `--allowedTools` and `--disallowedTools` govern approval, not a reliable tool-visibility boundary (#11736). Per-conversation MCP narrowing must filter the actual `sessionMCPTools` response and repeat the same decision at the assistant's call entry (`guardRefusal`). Changing only launch flags can leave stale clients or direct calls with the old tool set; hiding a tool is not a substitute for call admission.

- Automatic-continuation triage and channel mirrors observe the same finished turn concurrently (#11848). The retry wake timestamp is not yet durable when the first mirror runs; gate Feishu and extension mirrors on the session's transient classification and the transient-retry switch instead. Cover several transient attempts, the later terminal error, and delivery with retry disabled.

- Snapshot sequence numbers must order application as well as computation (#10514). `publish` numbered snapshots under `pubMu` but applied their interaction lists later, so a delayed older list could withdraw a prepared approval installed by a newer one. Recheck the sequence under `pubMu` immediately before `askOnPhone`; skip only superseded interaction lists and leave current omissions and explicit lifecycle endings unchanged. Force two real publishers across an existing leaf-mutex gate, then prove the owner's Allow returns once and applies once. Sequence ordering alone is insufficient: a snapshot computed before registration can omit the card without a newer sequence. Only the original prepared caller may cancel its operation; withdrawing an auxiliary snapshot waiter returns a dismissal to that waiter while leaving the original call and owner card pending. Cover direct snapshot withdrawal, the original-caller cancellation control, final-result sharing and the real stale-handle integration.

- A publisher gate must not hold a mutex needed to create the state the test waits for (#11572). The out-of-order snapshot fixture held `tasks.quiet.mu`; after prepared-card registration gained a cache-miss session lookup, that lookup waited on the same mutex and the test spun before the fresh publisher. Seed the already-observed session list with `fleetKeepPrevious` so the real prepared path bypasses that unrelated lookup, and keep the production ordering guard proved by mutation rather than weakening the race assertions.

- Moving temporary model outages to a durable manager retry must not swallow the node's other bounded recovery paths (#10444). Keep unavailable-model chains, recent transient 404s, safeguards refusals, gateway-quota recovery and tool-search resends on their existing contracts; only the outage classes owned by the new scheduler change. Tests of node-internal recovery can use a recoverable missing-model failure, while scheduled failure-fallback tests need a permanent error such as 401. Run the combined recovery tests after merging newer origin-attestation and delivery changes. Exercise the real hub interrupt and owner-message paths too: a direct `UserStopped` test misses a hub that notifies only Assistant, and `/exit` or `/quit` starts no turn that could cancel the wake.

- Negotiate external runtime supervision policy explicitly, and isolate per-session rule layers outside `~/.persona/assistant` (#11173). Hardcoding `SourceKind == SourceRuntime` as unsupported blocks capable runtimes like Antigravity CLI, while dropping the guard or concatenating `supervision_instructions` into user message text weakens compaction durability and role isolation (#10338, #11121). Gate policy delivery on explicit `caps.supervision_policy` / `capabilities.supervision_policy` (defaulting absent or unknown to unsupported so DSH and older runtimes keep exact fail-closed behavior). On `agy acp`, verify append vs. replace semantics empirically before choosing a channel: `_meta.antigravity.systemPrompt.append` accumulates across `session/resume` calls and cannot be cleared on demotion, whereas per-session managed directories under `<agyDir>/persona-supervision/<rawId>` passed via `additionalDirectories` load into `<user_rules>` on every turn, reflect atomic edits and deletions on the next turn, leave shared `~/.persona/assistant/AGENTS.md` untouched, keep concurrent ordinary chats isolated, and fail closed with `assistant_supervision_runtime` when ACP lacks `additionalDirectories`, disk writes fail, or policy exceeds the two-file 48,000-byte (`AGENTS.md` + `GEMINI.md`) cap.

- Recover an untold completion by native turn identity, not transcript revision (#10686). Trailing transcript bookkeeping must not suppress an already judged signal. Revalidate the complete current ledger entry under its lock and enqueue without rewriting it, so an older recovery snapshot cannot replace a newer turn or judgment. Exercise both PostgreSQL-backed manager restart and stale-snapshot rejection, with replay disabled as a negative control.

- A rendered prompt used as a source-snapshot fingerprint must compare with the call's captured language (#6272). Re-reading the current language while committing a valid model answer makes a preference change look like a concurrent source edit. Freeze the wrapper language, keep the source comparison, and test both a locale-only change and real source changes. Audit natural-language parsers too: a conservative Chinese uncertainty check must recognize uncertainty in newly supported English and Japanese outputs. Shared recording inputs must keep their recording-language contract rather than inheriting a non-recording wrapper accidentally.
- Persist a complete delivery round before its first external send (#10393). Saving only when the call returns handles a returned error but loses the result if the manager exits inside the call, after task acceptance has committed. Keep the original timestamp/key and acceptance metadata on disk while retaining an in-memory claim to block overlapping sends. Every other book save must preserve that prepared record too; retry must not send from memory when its preparation could not be saved. Use a child-process exit at the real provider boundary and a fresh manager on the same disposable database to prove recovery, with the returned-error case as a control.

- Prove a reported PR's ownership before asking its repository provider (#9725). A provider-specific address is eligible when a successful native creation record names that exact plain URL; an unstructured CLI URL is not equivalent proof. Recheck the original address's report turn and active Agent after asynchronous resolution, even when the provider canonicalizes an alias. Share only provider metadata across pending reports, never their authorization.
- A reply can name a dependency's merged or deployed PR (#9725). Use the same ownership proof or the task's recorded PR for progress, delivery judgment, and the receipt's PR field. If a reply describes several PRs, its merge or deploy words cannot establish the task PR's state. A separate check of the task PR can still establish delivery.
- Deterministic task admission must use saved repository links, not advisory duplicate hints (#9725). Check the original title, goal and prompt before clipping, and recheck waiting cards before Start. Read associations without importing legacy metadata; a store failure is not proof that no match exists. A distinct-work override must identify every conflicting to-do and preserve its reason in the brief. Validate original URLs before discarding query parameters: canonicalization must not hide credentials from the repository parser or echo them in an error.
- Saving an Agent's PR association is only half the feedback path (#9725). Inbox routing must read the same saved links, not only the legacy current PR or transcript preview, and its repository-scope check must retain that exact association. Import explicit work artifacts only for the session's current open task and a turn at or after its Agent link; reuse report receipts so retries cannot resurrect links the user removed. A final closeout's PR artifacts outrank incidental background links in the reply.
- Inbox handoffs may continue an existing Agent without making a plan (#9725). Apply the same persisted-source inference to `agent_send` and `task_scope`; refuse ambiguity before sending or logging scope. Record the task backlink and Agent chain link, not the coding Agent as the item's processing conversation, and preserve the task's original Inbox source. After delivery or a queued receipt, save the association with a fresh bounded context independent of the send deadline and caller cancellation. Report a real association-save failure as partial success so it cannot cause a blind duplicate send.
- Native PR-create and push records must match the report being processed, not just its Agent link (#9725). Cut at the latest prompt, reject side exchanges, and bind report text and its recorded timestamp or the persisted artifact's turn end. Compare stored timestamps at Postgres microsecond precision. Missing or mismatched turn evidence needs an independent author, branch and commit readback; positive fixtures must provide the same turn identity. Include the turn timestamp in preview deduplication so identical text in a new turn can still prove fresh ownership.
- Inbox preflight is not a reservation across Agent delivery (#9725). Recheck against the actual adopted task before sending, and reconcile a later conflicting claim by removing only a task just created for that handoff, without stopping its Agent or changing the competing owner. Keep an accurate partial-delivery receipt; a failed rollback must name the task that remains, never invite a resend or claim that nothing happened.
- An omitted Inbox source is resolved from the persisted user handoff, not model text (#9725). Exactly one eligible unarchived item handed to the calling conversation and computer is a safe default; multiple eligible items require `inbox_item`. Filter the existing link policy before counting candidates, so excluded bot handoffs neither block unrelated work nor create false ambiguity. Persist that exact handoff on the plan and keep Start checks; a later handoff must not retarget a waiting card.
- A review wake is not a completion verdict (#8373). PR events and judged-done candidates share the same signal. Keep its wording neutral, preserve the actual reason and criterion evidence, and leave acceptance gates and display grouping unchanged.
- A parked wait can cite a PR while waiting for a separate deployment or verification (#9775). Enroll the exact PR from an explicit event binding. Keep the source receipt and review transition together and identify the notification by that park. Test both a stage PR with terminal facts and a PR named only in the park.

- Detect missing final replies on the settled exchange (#10427). A session preview can carry the previous answer
  through a tool-only turn. Check the current transcript and verified input provenance, then test that a follow-up
  cannot pass the send guard after newer input or an explicit stop. Keep its attempt identity and visible failure
  notice tied to the original input.

- Snapshot display projections must not initialize the knowledge base (#10108). Publication already holds
  `pubMu`; read an already-open engine and retain the canonical cached project-name fallback rather than calling
  an accessor that may initialize storage. Let direction synchronization refresh project names asynchronously.
  Keep a regression proving that building a snapshot leaves an unopened knowledge base unopened.

- An owner-visible wait must be durable before its turn is marked judged (#5732). If writing `needs_user` fails but the final `Judged` receipt survives, startup recovery skips the turn and neither the visible state nor its one-time push can be reconstructed. Persist `OwnerWait` first and leave the turn unjudged on failure; replaying the same work then creates the wait idempotently and delivers exactly one push. Test this with an isolated real database and an injected one-write failure rather than a store-less unit test.
- Owner-action phrase matching is occurrence-local, not a property of the whole reply (#5732). Chinese “是否” makes the adjacent marker narrative, and “如果 / 假如 / 倘若 / 若” opens a conditional clause; neither may hide a genuine instruction in a later clause. Evaluate quotation, negation and conditional scope at each marker occurrence, preserve full-width sentence boundaries, and keep controls where a real instruction follows earlier narrative or conditional text.

- A terminal review covers its criteria, not every later scope (#6880). Explicitly replacing criteria on an open, previously accepted or cancelled task must invalidate its terminal supervision state; otherwise unfinished or unjudged follow-up work is silently suppressed. Keep review history and unchanged criteria's evidence. Metadata-only changes and ordinary follow-up conversation do not invalidate the review, and existing stale records still need a real supervisor review rather than an automatic migration.

- A nearby empty turn is not proof that a PR event reached an Agent (#10050). Match the actual input and source association: an owner may have explicitly asked for no reply. Record external delivery provenance before sending, keep transport distinct from acknowledgement, and persist the bounded retry before making it. A feed write failing after the node accepts delivery does not undo that receipt: retain the event and wait for its actual turn, not a false redelivery failure. Pin redelivery and completion to the recorded recipient, including recovery while a peer works; another Agent's newer row is not this event's turn. Trim paged items to the current turn before interpreting a stop. A still-unhandled source must survive both duplicate completions and a manager reload, independently of ordinary Inbox signals; ordinary owner input must never spend its retry.

- External-event escalations must keep their source and recipient identity (#10050). Render the failed source against that recorded recipient, not whichever Agent now has the newest reply. Its escalation also needs a task-bounded, per-source fingerprint: a process-local sent cache and short semantic window replay failures after a restart, and a successor Agent or ordinary Inbox message must not change that identity. Reuse delivery reservation rollback and keep unsent sources pending for revalidation, rather than merging them into rendered relay queues whose enqueue result does not prove persistence. Recover every eligible recorded recipient independently, with per-recipient look/backoff and serialized task reads; the first unreadable transcript must not monopolize recovery. An owner wait may discard a rendered signal, not its persisted source: restore it on the next sweep and deduplicate actual delivery durably instead of trusting a sent marker written before validation.

- Process lifetime is not model-health evidence (#8480). Keep an observed completed answer and its original timestamp independently of the live session row. Otherwise releasing an idle process drops the only recovery evidence while an older failed probe remains, reviving an already repaired authentication error. Capture completed-turn evidence without requiring a client poll, scope it to the model binding, retire superseded probes and let newer failures win. Failed persistence must remain retryable.
- A retry that puts a failed send back for the next digest must first ask whether the next try can succeed (#8518).
  The task leader path picked "resume and escalate" from the listed status (`StatusOffline`), but a stopped, read-only
  agent is also listed `interrupted` when a restart cut it off, and the node refuses every send to it
  (`agent_stopped`). Put back as an ordinary failure, that became a node round trip every 30 seconds that nobody above
  the leader heard of. Classify by the node's refusal code, not by status or its words: a refusal that holds until
  someone acts goes the escalation path once, with a growing retry; transient failures keep the digest retry.

- A finite startup wait cannot make a one-shot MCP tool list reliable (#8470). Registering a task leader and resolving
  its pane can both happen after discovery. Capture a change cursor before building the list, wake it from persisted
  task-link changes, and refresh only when the tool definitions actually differ. Re-resolve identity on heartbeat,
  invalidate cursors across manager restarts, back off on outages, and exclude discovery watches from idle accounting.
  Leadership is the active `todo_agents.source`, set from the new plan even for `continues`; there is no to-do role
  column to update. A sharing `node_receipt=identity_missing` instead concerns creation/incarnation receipts, not MCP
  pane identity, and is not evidence of the latter's failure.

- A GitHub number or a saved PR reference is not proof of this task's delivery (#8248). Confirm numbered references through issue metadata, then distinguish an explicit task-owned final closeout PR artifact from ordinary links, documents, quotes and earlier turns. Never promote unproven reference history after removing an issue-as-PR record: a related, unmerged PR can create the same acceptance deadlock without returning 404. A completed non-PR closeout must explicitly say merge/deploy are not applicable before clearing a contaminated delivery; genuine explicit PRs still pass the merge/deployment gate. Do not treat missing evidence or a masked permission 404 as proof of absence. Persist correction before publishing it, and keep item-level misses out of unrelated PR checks' outage cooldown.

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

- A delivered Inbox note is not automatically a reason to wake its supervisor (#9916). Keep the routed task feed,
  but suppress the successful copy only for active work, checking waits before and after the send. Preserve failed
  delivery and parked/review fallbacks. Scope and claimed-owner requests need the Agent's full-thread judgment and
  an urgent-by-default supervisor question, not a keyword classifier over a clipped preview.

- Inbox handling chats are implementation details, not user-created conversations (#5723). `Untitled` suppresses
  automatic naming but does not hide a chat, and `titleFixed` still gives it the item's subject. Apply default
  archiving in the common session snapshot, not only after starting or finishing: restored chats and follow-up
  instructions must stay hidden too. Preserve item-chain links and the main conversation.

- An owner's bot handoff is not a third-party task update (#8351). Exclude it in the shared task-link policy, not by
  asking the judge to pick no task. Apply that policy when admitting supervisor signals and following task
  continuations too: a persisted association can outlive the router that created it. A link-only correction must
  preserve ordinary human messages and the handoff's handling, suggestions and history.

- Count repeated conditional instructions at the runtime-send boundary, not just in a prompt golden (#5665).
  Two adjacent decision blocks repeated the same handling rules, but the newer one also required recording the choice.
  Remove the older copy, retain the newer obligations, and check both decision gating and the actual synthetic
  `DeviceAssistantSend` text. This proves literal deduplication, not a stable-policy refactor or provider-context savings.

- Prompt-size checks read the MCP catalog goldens, not newly added tool files (#9762). Regenerate every assistant
  catalog and check `TestGoldenMCPTools` before trusting the footprint. Measure changed tool and shared-prompt sources
  too, so stale goldens cannot hide growth. Fitting the total budget does not register a new per-tool baseline key;
  keep that remaining gate explicit when baseline edits are not authorized.

- Bind manual send approvals as tightly as standing grants (#9762). After the card
  resolves, serialize the sender recheck and transport with identity changes.
  Handed-off Inbox suggestions stay manual even when their scope matches a grant.
  A transport's already-delivered ID proves only a prior send, not delivery of a
  new attempted text; audit that outcome separately and never report a fresh send.

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

- A remembered "kind" has to repeat in how the owner really works (#8259). "Start These Directly" keyed a kind by the task's exact folder, and the owner's work on one repository happens in a new worktree per branch, so the next task was always a new kind: ticked again and again, it never skipped a card. The kind is now the repository (its main checkout, from the node's `worktree.status`), resolved by the hub, not taken from the tool call; whatever the node cannot say leaves the exact folder, so a lookup failure can only ask more, never less. Before keying a preference on a path, check whether the next occurrence would have the same path. And key it on the path that actually runs: a task's workspace (an existing one picked on the card, or a checkout the engine makes) can be in another repository than the folder the plan was made from, so the repository is resolved again at admission and remembered from there, not from the plan's cwd or a status answer cached for it.

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

- Retiring a to-do's existing wake is not a lasting check on automatic follow-ups (#1921). A new turn clears the retirement marker, and a failed retirement write can leave a wake in storage. Check the current unreleased, undismissed to-do links both before triage acts and before a wake sends; an open link wins over completed links. A user-completed to-do stays done after a manual message, whereas `taskWorking` reopens a task the hub's judge closed (`auto`) on any turn and an accepted one (`review`) only after its own conversation sent the agent more (`closedTurnReopens`). An exemption for agents a plan started reopened accepted tasks on the agent's own wakeups and showed them as waiting for review again (#7539). Once that exemption went, every way the task's conversation sends its agent more has to record the send (`noteAgentSend`; `agent_interrupt`'s `then` had not), and a new agent started for a closed task has no send on record, so the card message reopens the task once that agent started, not when the start definitely failed. Who may ask for more is not only the ledger's origin and supervisor: the background supervisor accepts and reworks the main conversation's tasks while their ledger names the main conversation (`backgroundSupervises`), so check its standing too, when the send goes, and only for the tasks it stands in for (not a side conversation's own). A send recorded only in memory is lost when the manager restarts before the turn starts, or before the send's answer comes back: store it before it goes, with the task and its acceptance (`tasks_sends_out.go`). Refuse the send when that record cannot be stored: sent anyway, it is lost with the next restart. Tell the outcomes apart: taken (reopen at once), definitely failed (`rpc.ErrNotSent` marks a request that never left the manager; a timeout alone does not say that), and unknown (reopen on the agent's next turn). Do not let an unknown send stop counting when the call's wait runs out: the agent may read the text much later (queued behind a long turn, held in a Claude inbox for approval), so keep it until that turn, until the task moves (reopened, accepted again, the agent on another task) or for days. An acceptance of a task that is still closed changes neither its `done_at` nor anything else such a record matches on, so drop the records it answers when it is made. And retry a failed write of the records: one that would have removed a refused send's record leaves it to come back after a restart as an unknown answer. An error is not always a failure: read the node's `delivered` in its data too, since `unknown` (typing failed part way, a Claude inbox that has not confirmed the text) means it may still arrive. Do not reopen on a turn that starts while the send is still out either: hold that turn, and its end, for the answer, keep one record per send so that a later send to the same agent cannot hide the held turn (and once the answer reopens the task, the agent is no longer the closed task's: mark the held turn as being taken up, or a cleanup that drops records of tasks no longer closed drops the turn's end with it), and bound every such send (one that may hold a turn for its answer) with its own wait, since a runtime's tool call has none; leave other sends on their caller's wait, since a node can take minutes and still type the text. Decide from what is sent and for which task, too: a bare `/exit` closes the agent and asks for nothing (on every path: to a task whose agents all stopped it must not start a new one), and a card message reads its task's agents before the send, so by then the agent may belong to another task. Cancellations must remain final too (#1851): persist `done_by=cancelled` even when there is no supervision ledger, and exclude older `review` completions whose ledger says `cancelled`. Keep the API value `review` for existing clients' cancellation labels and reasons. Do not infer cancellation from a localized reason; explicit user reopening still restores task tracking. Do not mistake a new turn or an old CI watch proof for a reopened to-do. A failed to-do read must leave triage retryable and postpone a wake, not treat the session as unlinked or permanently cancel it.

- A shared completion value does not imply the same handling-chain explanation (#1908). Mutes retain `done_by=read` for compatibility, but must say they were muted, not read elsewhere. Cover existing items, new connector-item admission and grouped events in every hub language, and keep an ordinary-read control.

- Health notification redaction must cover every notification route (#1914). `notify_user` passes through `runTool`, but `phone_notify` goes directly through `mcpCall` to the phone. Apply the existing caller-space check and `spaceNotice` before phone dispatch, and assert the forwarded title and body in tests. Device notification-preview settings and a prompt recommendation cannot enforce the space boundary. Keep explicitly requested, phone-confirmed reminders separate from generic notices.

- A PR's check-run endpoint alone is not the complete CI result: commit statuses are separate, and both must be paged. Check the PR head again after reading and reject incomplete or inconsistent pages. The node's older PR reader cannot attest complete rollups and can classify a rerun by its old conclusion; only nodes explicitly marking a complete, status-aware result can be used for a CI wake. Test a failing check alongside a pending one, a changing head, and rate limits.

- Reproduce settings chrome through both production entry paths (#1433): the profile button on the assistant page and the settings navigation entry. Use a manager whose localized name matches the page title, then count the navigation title and switcher separately; a differently named test manager hides duplicate title regressions. Cover absent rosters, a single manager and legacy configuration responses. Prove the assertion catches the original row by temporarily restoring it, then remove that mutation. Released source ancestry and a download test do not prove which build a user's phone is running.

- Permission-default support must come from the manager that owns the setting (#1433). A newer Hub, an owner id, or an optional saved value cannot establish support on an older manager. Return the versioned contract with the manager's configuration and validate its owner on both PUT and independent GET. Test through the production Android adapter rather than overriding a capability method only in test doubles; a default-false adapter can otherwise disable every production manager while tests pass. Persistence and node-native policy are separate checks: verify both Claude startup/control requests and Codex thread/turn policies.

- Stage user-filing handling closure in the row being saved and publish it only after that row commits (#1313). Clearing drafts and pending notes on the live item before saving requires more than restoring its done marker, and a concurrent delivery pass can send the stop note before rollback. Exclude pending filing markers from unrelated saves, including delivery acknowledgements. A bulk unchanged acknowledgement also requires a committed state; return pending ids as failed so clients retry. Filters must include every pending open state that can still commit, including intermediate reopens between closed endpoints, and retain older pending ownership when a newer save fails. Give filing its own pending ownership so an unrelated parent edit cannot prevent rollback; keep concurrent chain entries and test batch fallback with mixed row failures, nested draft aliases and delivery during the write.

- Schedule admission must be durable before dispatch (#1287). Save a separate candidate task list while holding the schedule lock, then publish it and broadcast only after success. A failed user edit or due-run marker leaves the prior tasks recoverable. Results and conversation ownership from work already sent are different: keep them in memory when persistence fails, preserve their history IDs so watchers can still update them, and retry the current lists on later saves. A late result still belongs only to its run; it must neither overwrite a newer run's status nor recreate a deleted task.
- A durable timer needs a fixed deadline and a crash policy, not just a saved callback (#1811). Persist scheduled usage-limit retries and task receipt rechecks before arming their local timers, restore overdue work promptly, and fence dispatch with a compare-and-swap claim. Reset every captured result on each CAS retry: a loser whose first attempt claimed the document must not return success from that stale attempt. Once a scheduled retry may have been sent, recover its claim as unknown rather than resending; receipt rechecks can replay through the durable turn ledger, but only while their exact task-agent link and reopen generation still match. Keep the original settlement's unreachable result instead of probing twice—the second probe can succeed and wrongly discard the retry.

- A list bounded by a retention window is not bounded (2026-11-12, #5549). `GET /api/v1/inbox` listed every item done within three days, so one Clean up all of a few thousand items made it about 10,000 items and 21 MB: every app downloaded it again on each `inbox` push, and Android's bounded reader (4 MiB, 100,000 structural tokens) turned it down. Cap the done side by count on a deterministic key (`done_at`, then id, the history's order: a just-filed item is always kept, and the same items give the same bytes for the merged validator), let the delta report what falls past the cut as `removed`, keep it reachable by id and through the history, and test with more items than the cap. Clients that took the window as complete without a page (iOS `listDoneSpan`, the web's former `LIST_DONE_MS`) under-show until they page it too.
- A capped display list is not an idempotency record (2026-11-15, #8424). A connector's `replied` state report repeats until new messages come, and its dedupe looked for a `self_replied` chain entry at the report's `last`; the chain keeps the newest 50 entries for display, so a conversation's later steps at the same position could push that entry out and the same report counted as another owner reply: another entry, note and wake, and a reset follow-up, suggestion and draft. Keep the key a dedupe needs in a field of its own (`InboxItem.RepliedOf`), read the old marker only for rows stored before it, and test by evicting the marker before replaying the report.
- A one-at-a-time background pass guarded by `TryLock` must not drop the calls made while it runs (2026-11-07, #64). `handInbox` returned when a pass was running, and every caller is an event: a group event that told an event's conversation of the other side's answer called it while a pass was past its delivery step, so the note waited for the next unrelated event (and `TestInboxSuggestionConfirmReply` failed most runs under `-race`). Mark the request, let the goroutine holding the lock run passes while the mark is set, and check the mark again after unlocking.

- Keep task receipts ahead of completion judgment (#4893). Moving `autoFinishTask` before the receipt adds a model call to its latency and sends supervision signals before the receipt. The review hint names who follows up next, not a `pending_review` verdict: `superviseJudged` signals Persona for finished work (`sigReview`) or unfinished work (`sigIdle`); a task already in `needs_user` is waiting on the user. Read the latest to-do at receipt time and include the hint for an open supervised task unless `Supervision.state` is `needs_user`. That helper treats reopened accepted/cancelled tasks as ongoing. Test that the receipt precedes judgment, including zero criteria, and that ordinary, completed, failed and interrupted tasks do not get the hint.

- Owned triage questions require a durable interaction history Store before acknowledging an answer (#5994). A bus integration test that previously used no Store must supply its manager with the isolated Postgres fixture and assert `InteractionOutcome` after resolution; never weaken the production missing-store failure merely to keep the test green. The shared hub fixture does not truncate `session_work`, so explicitly prune old work in this isolated fixture before connecting: otherwise a repeated run can restore the previous answered turn and suppress the new card as a duplicate.

Pitfalls, unusual behavior, and decisions at the time (formerly `CLAUDE.md`; index `docs/lessons/README.md`). Add lessons to relevant sections, or a new section if none fits.

- Push delivery is per destination, not just per conversation (#1896). A shared `notify_user` mark lets one successful channel suppress another channel's failed delivery, while an unmarked extension push duplicates its turn-end mirror. Key delivery by channel, machine and session, record only confirmed sends, and apply the same check to main-chat mirrors, scheduled-task mirrors and Lark mirror retries. Test mixed successes and failures, including text split across several sends.

- A copy sent beside a filtered push must follow the filter's answer, and the answer about it must count the copy (#8268). `notify_user` copied every notice to the Feishu bot and the channels after `notify.request`, without reading `held`, so push grading held nothing for an owner who reads Persona through the bot; and with that owner's phone pushes off the tool answered "the user was not alerted" for a notice whose Feishu copy had landed, and the conversation sent it again. Gate the copy on the hub's answer for the same push (not on the assistant's own reading of the switch: the hub levels by conversation and related key), and say in the tool's answer which copies went out, failed or are not confirmed yet.

- A one-shot send that forgets its record on failure loses what it was for (2026-11-07, #7). The "Please verify" card is asked once, when a task's deploying turn ends; a failed or timed-out send used to drop the claim, and nothing asked the owner again. Keep the record (the ask, without a message id) and retry from a sweep, under a key per round (`uuid`) so a timeout that did land is not a second card, and only within the hour Feishu remembers a key (`acceptPostWindow`, as the nudge's `nudgeRetryWindow`); past it, fall back to the card-less path the briefing and board already show. Whatever a sweep or a load reads back may hold a claim saved mid-flight (no time) or a null entry: clean the map that was read, before it becomes the book, and never dereference an entry unchecked.

- An agent stopping does not clear its task's pending review (#4889). The Today card must check `Supervision.state(t)` before grouping by `taskState`: a pending-review task may look stopped, unread, or even still working in the agent snapshot. Keep it in one review list, and preserve the open-to-do exclusion from the finished list (#6159). Test with a done receipt as well as conflicting agent states to catch both omissions and duplicate counts.

- A byte limit can split UTF-8 even when each message was clipped by runes (2026-11-07, #5948). `transcriptText`, shared by `agent_read` and `conversation_read`, limits the assembled transcript to its last 40000 bytes. Advance the suffix start past continuation bytes with `utf8.RuneStart` to keep the byte budget and whole characters. Test every cut position in two-, three-, and four-byte characters, cuts already at rune boundaries, and lengths below, at, and above the limit; assert valid UTF-8 and the exact retained suffix.

- A successful control-request write is not Claude's acceptance (2026-11-06, #5947). Match `control_response.request_id` before changing the process's acknowledged mode / effort or the persisted `AppMode`, `AppEffort`, and `ModelEffort`. Register the waiter before writing and use the value returned by the atomic request counter; reading the counter separately can reuse another concurrent request's ID. Serialize setting changes, keep response handling off the settings lock, and ignore unknown / duplicate replies. Return failures through the existing `session.modes` HTTP path so clients can restore their selection. A plain `i18n.Err` loses its type over node RPC: send a stable error code and localize at the HTTP boundary, including the upstream reason in the summary because some clients do not display `detail`. Test with fake SDK responses, including partial success, refused changes, unmatched IDs, and process exit; do not use a real Claude configuration for these cases.

- Key assistant prepare state and generated CLI wrappers by the same canonical directory (2026-11-06, #5831). Per-directory connector and CLI maps alone still leave a shared `assistant-bin` vulnerable: preparing another manager can remove a command or replace its connector when both use the same command name. Scope skill synchronization and process PATH too, and check CLI permissions using the authenticated assistant process's directory. Test two directories with disjoint connectors and a shared CLI name, then prepare or sign out one and verify the other's permissions and wrapper remain unchanged; include symlink aliases and unprepared directories. For `skills.list` callers that omit `cwd`, preserve discovery when the only prepared directory is non-default: prefer a prepared default, then the sole prepared directory, and never choose arbitrarily among several. An empty connector list still counts as prepared.

- Reintroducing a retired model purpose must not read its dormant settings record (2026-11-06, #5921). Lark /agent uses a new override namespace and revision sequence, inheriting Agents until a new user save. Keep category-default assignment from creating an opt-in binding, include overrides in provider-in-use checks, and determine client editability from writable fields rather than `category_id != id`. Tests must seed the old record directly, then verify new conversation startup parameters before override, after save, and after reset; merely asserting the resolved binding misses startup precedence.

- A second opt-in model override shows where the first was keyed by ID (#4232). Setting the AI twin apart like Lark /agent needed no new server path, but every client special-cased `id == "lark.agent"` for its status words, reset label and effort rule, so the twin's row and editor would have kept a category's wording (no "Separately configured", "Restore Default" instead of "Follow Agents") while being an override. Recognize such a row by data (`parent: "agent.new"` and writable `fields`) and keep ID checks for wording that really is one purpose's. Server tests start the twin through its own path (`startInboxAgent`), not the conversation route, and check that a save writes only its own key.

- Model recovery provenance belongs to a turn and a chain, not to conversation-wide mutable flags (#10909). Persist the original execution identity before recovery can overwrite session state; do not inherit it across an exhausted chain boundary. Classify a new pick without ending the old marker or hold, and commit that ending only at input delivery. A busy process must retain its own used/retry budget even when newer input joins it, then release that ownership when its chain finishes. For legacy markers, fallback-mutated session effort is not the original effort. Cover fresh/expired/aged/cold records, exact old-fallback picks, rejected and real newer delivery, older `from` and `to` turns, restart and launcher-scoped unavailable evidence.

- Model-settings HTTP labels must use `requestCtxLang(ctx)`, not `s.lang(ctx)` (2026-11-06, #5057). The latter reads only the hub's language, so catalog-driven launcher role labels can still ignore a client's `Accept-Language`. Test sequential requests with a different hub language, both discovered-model and fallback-model paths, and unchanged model IDs. MCP-only refusals remain original model text; they must not become exemptions for real HTTP, push or approval-card copy.

- A database-read failure for session triage settings must not return `200` with defaults (2026-11-06, #4553). Toggling one switch makes the client `PUT` the entire settings group; a failed read disguised as All Off would overwrite other enabled values. Preserve the read error and return `503` with a localized reason from the settings API. Only internal automatic triage keeps a default fallback. Do not cache failures; the next retry must read the database again. Regression tests inject read errors through a fake Store and check API failure, saved values after recovery, and unaffected internal fallback and `PUT`, without stopping the shared test database.

- Assistant-card boundary validation must cover more than common syntax (2026-11-05, #2018). Duplicate-key errors were overwritten by later chart / Markdown checks returning nil; reference-style images / links bypassed inline-only regex. Shared validation rejects duplicate keys first, then components. Safe subset explicitly rejects reference definitions; regression tests include images, autolinks, URLs without hostnames. Remove empty sections after stripping HTML, or compact limits reject emptied cards as oversized. Live-card version must stay within JavaScript safe integers, not merely Go int64.

- Single-session lookup must use trusted assistant classification too (2026-11-05, #2019). Lists classify by dedicated directory / pane, but session.get returned raw node records usually lacking Assistant. Rich-UI tools / outbox falsely rejected real assistants. Reuse isAssistant on lookup; pane takes precedence as ordinary Agent. Tests must use real hub query path for headless / pane / normal directory, not manually Assistant=true. Body registration shares classification.
- Go JSON decoding ignores field-name case (2026-11-05, #2019): Fallback populates Go Fallback, but Swift / Kotlin require schema fallback. Validate exact keys before converting assistant raw blocks to Go types; successful decoding is not cross-client conformance.
- Preserve encoded paths over HTTP bus (2026-11-05, #2019). Body-card keys contain /, item IDs often #. Clients encoded correctly, but forwarding `r.URL.Path` made assistant treat them as separators / fragments, actions all 404. Use `EscapedPath()`, test real hub→assistant relay; Service routes alone miss this layer.

- An inline answer to an expired question must commit the whole answer set, its terminal record, to-do completion and
  durable relay together before sending. Per-question commits leak partial manual decisions; sending first makes a
  failed terminal write look successful and allows a duplicate after restart. Publish decision events only after commit,
  and reread durable state before releasing a claim or applying a concurrent relay completion when the commit reply is
  lost (#9731). Give decisions distinct microsecond timestamps so the ledger preserves the question order.

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
- Proving a shell variable constant needs allowlists, not denylists (#16491, 2026-11-27). The gate parses with a bash grammar, but the assistant's Bash may be zsh, which sets variables through builtins and modifiers that leave no assignment in the syntax tree (`print -v`, `zstyle -s`, `set -A`, `emulate -c`, `noglob eval`, `repeat`), and evaluates arithmetic in places bash does not (`-t` / `-v` test operands, `$name[expr]`, integer specials like `HISTSIZE=expr`, `$~name` glob qualifiers). Eight adversarial review rounds each found new bypasses until the rule became: only listed commands and test operators, ordinary names, no arithmetic, `cd /tmp` alone. Extend the gate's reading of the shell only with allowlists, and have a reviewer check each claim in real bash and zsh.
- Keep credentials out of Agent env, not protected by read-command blacklists (#1317,2026-11-05). Env, split-variable-name printenv, `cat /proc/self/env*`, ${!prefix*}, `python3 -c 'import os…'` bypass; review also nohup env, `compgen -e`, ps eww, eval-built names, `env -u HOME printenv`—unpatchable. Worse, token-bearing scripts bypass write confirmation directly. Credentials stay in node; local proxy checks every actual request against same rules (`node/gateproxy.go`). Proxy belongs to entire persistent `assistantKeeper` process, not per-connection Node: reconnect replaces Node, surviving chat retains old proxy address, new approved calls cannot reach old proxy. One approval allows method + path + body once. Writes in unparsed loops / scripts must be refused and sent as standalone commands; never open timed allow-all windows, which permit unrelated later commands in same chat.
- Codex assistant uses `codex app-server` (`docs/assistant/assistant.md` “Codex”). MCP approval arrives as `mcpServer/elicitation/request` with `_meta.codex_approval_kind: mcp_tool_call`, not command approval. Default tool timeout 60 seconds needs larger `tool_timeout_sec` for ask_user. Default command env strips `*KEY*` / `*TOKEN*` (ol lost token). `turn/interrupt` does not stop running commands; `thread/backgroundTerminals/clean` additionally, experimental generate-ts --experimental. Codex owns thread ID, unknown when MCP starts. Lark gate relies on approval arriving; never `approvalPolicy: never`.
- Codex / Claude share assistant auto-memory (`docs/assistant/assistant.md`). Untrusted Codex approves even head; without separate allowance every memory read prompts phone (tested 2026-11-01). Memory outside assistant directory needs writableRoots. Do not enable machine-wide `~/.codex/memories` for assistant.
- Mac Codex sandbox rejects symlinked writable roots except top-level `/tmp` `/var`, before every command: symlinked writable roots are not supported (#1751). Legacy `~/.persona`→`.persona` and dotfiles `~/.claude` trigger it. Resolve cwd **and** memory path for `codexDirs`, not cwd alone. `allow_symlinked_codex_home` affects CODEX_HOME only; do not broaden. Verify same parameters with `command/exec` and `thread/fork` with `ephemeral: true`, pwd, preserving original session.
- Fake-HOME hub tests: `~/.local/bin/codex` wrapper resolves installation via `$HOME/.npm-global`; symlink into fake HOME or app-server exits immediately.
- Fake HOME is not isolation. Node follows launcher-selected `CLAUDE_CONFIG_DIR`; hardcoded profile launcher writes real transcripts, and cwd suffix `/.persona/assistant` makes real assistant chats appear in test hub, editable by test assistant (2026-11-01 chat-management tests). Copy launcher into fake HOME, redirect `CLAUDE_CONFIG_DIR` there, copy settings.json, define `_claude_launchers` in fake .bashrc, prompt only own created chats; delete full fake HOME afterward, keys included.
- Unknown `assistant.send` session ID starts **blank new chat** rather than error if no transcript. Resume-or-report-failure calls (scheduled chats 2026-11-01) use `resume_only`; no process / transcript errors, hub creates / records new chat (`docs/assistant/assistant.md` “Scheduled tasks”). Busy state comes from hub working / needs_you and only listed sessions; retain long-lived chats in `keptSessions`, or 80 new transcripts on busy machine evict them.
- **No per-message changing data in system prompt** (2026-11-01, `docs/assistant/assistant.md` “Live snapshots and prefix caching”). Prefix caching puts system / tools before history; one byte change rewrites hundreds of thousands of tokens after restart, first output 10 seconds to minutes. Snapshots accompany messages. Claude resume actually retains first system prompt until compaction, so old snapshot stale, not cache-invalidating. Real restart cache miss was **changed tools**: one removed MCP tool rewrites all, schema key order does not. Diagnose experimentally: copy real chat with new ID / temporary directory, change one thing per restart, inspect `message_start` cache_read_input_tokens.
- Per-start compaction belongs in both the process environment and the final merged settings (#6481). Claude settings `env` can override the process. Preserve launcher settings / hooks, then pin the computed AUTO and MAX values last in both. A launcher AUTO value is not evidence of backend capacity. Match warm spares and restarts by explicit main-chat scope, not the assistant directory or model purpose; scheduled chats share both. Don't turn "nothing declared" into a Persona number (#3008, #3066): a guessed 200k, then fixed 200k / 400k targets, made 1M conversations compact at about 167k. The one Persona number left is explicit and narrow: `assistant.main_compact_window` (#7314) caps only the main conversation and the directions' lead conversations (#15318), only on a managed launcher that declares a larger window, and only when the manager sends it (absent or 0: no cap). The manager decides which conversations get it; the node applies what comes with a non-guest start, so a new kind of conversation is added in the manager alone. Before #15318 the node also required `is_main`, and leads woken all day by supervision grew to about 970k on a 1M launcher before anything compacted them. Follow what the launcher declares, write nothing otherwise (Claude Code already holds AUTO to its own model window), and read the real window from `result.modelUsage.<model>.contextWindow` where Persona needs it (the idle threshold). Write values only where Persona owns the environment (a managed launcher): over a shell launcher they would override its own declarations.

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
- A full queue must not hand each event to a goroutine that waits for room (2026-11-26, #15906). Past 1024 waiting, the assistant's event queue did that: nothing bounded the goroutines, they reached the queue in the order the scheduler ran them (a turn's start, a stop and a seen mark of one conversation out of order), and they stayed blocked for good once the consumer's context ended. Blocking the caller is no way out either: it is the read loop, which also brings the answers the handlers wait for. The queue is now one ordered list (`eventQueue`, `server/internal/assistant/bus_events.go`): while more than `eventsRoom` wait, an event of which only the newest matters (the mutes, a resync request) replaces the waiting one of its key at the back of the line, every other event waits in arrival order up to `eventsMost`, and what comes past that is counted and made up for by dropping the connection once the line reaches that point, so the reconnection path reads everything again. "Says the whole of something" is not enough to let a newer event replace a waiting one: the handler must also read nothing of the one before. `manager.updated` and `sessions.changed` are whole states, but their handlers go by what changed since the previous one (`trustChanged` gives keys to the computers a hello lists and the one before did not; `followStarts` and `taskStageSessions` go by consecutive lists), so replacing one loses a transition for good; each of them waits itself. And after a loss the line waits for the new connection's hello answer before it takes what is behind the note (`waitHelloAfter`, `helloWait` at most, once per connection): a lost `manager.updated` leaves the kept settings older than the events that follow. Hold the consumer in tests with `BusHub.taking`, not a sleep.
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

- **A space-wide read rule must be at every way the data leaves** (#10941): the health space's "an extension never sees its words" was on a to-do's timeline only (#10942); its row (a run's question, title, directory, report, the decision line its review stood on), its decisions, the answer of a write, `todo.changed` and `todo.decision` and the outcome a task wrote to a knowledge-base project still carried them, and the ids the row kept opened other routes. Hiding fields one by one left the next field open; leave the whole thing out instead (`todosHidden`, `todo_scope.go`: one verdict for the list, every route under `/api/v1/todos/{id}` through `s.handle`, the events and the outcome log), and keep what the verdict reads from being erased (a link's directory, a space's links). Test with a list of the space's words over every answer and event an extension's token can get, not with the fields the fix touched. The same records leave by other scopes too: a conversation's feed (`assistant.ask`) carries the to-do's history the timeline hides, `GET /api/v1/assistant` lists the space's conversations, and `agent.needs_you` (no scope at all) a health card's question and id. List every scope's routes and every event, not only the area's own, and do not put off a route because "no route hands out its id": another one will.

- **A fail-closed rule must not read only what is live** (#10941): the health space's verdict told a conversation by its computer's list, and a computer lists a quiet assistant conversation for a week and only its 100 newest, and nothing until it reconnects after a restart. Every ordinary task of a quiet chat, every task a quiet direction supervised and, in the restart window, the main conversation's own vanished from extensions, their events were dropped for good, and the owner's own project timeline lost its outcome line. Note what the verdict needs when it is true (a conversation's space when it starts and whenever it is listed, `assistant_conversation_spaces`, `conversation_space.go`) and read that before failing closed, and backfill what was there before the note: every record the verdict reads, done ones and history included, not only the open ones (`conversation_space_backfill.go` has the computers list each never noted chat a to-do names until it is noted). Such a backfill reads the whole store, so run it on its own goroutine, never inside the snapshot that every owner's write waits behind; and "not noted" is not "not there": give a chat up only once every computer that may hold it was on for the whole wait, and never one a computer listed while its note could not be written. Keep "known to be the space's" apart from "cannot be told" (`todoVerdicts`): hide both from the outsider, and hold, rather than drop or send, what would tell of the second, including the owner's own records that an outsider can read (the outcome line on a project's timeline, which `kb.read` serves). Test with the conversation unlisted, the list empty and the manager restarted on the same store, not only with everything listed.

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
- **Short naming calls must switch thinking off and still guard their write-back against reasoning leaks** (#17207): giving title calls more token room (#1532) did not stop a Direct API thinking model whose compatible gateway surfaces cut-off monologue as ordinary text deltas — first lines like `We need answer title` became todo titles, `we-need-answer-title` tab slugs, and (behind a known kind prefix) assistant topics. `ai.Request.DisableThinking` forces the protocol's existing "off" encoding (`ApplyReasoningEffort`: Messages `thinking.type=disabled` with the model-specific fallbacks, Chat `reasoning_effort=none` with its rejection cache, Responses `reasoning.effort=none`) for `todo.title` / `agent.title` / `assistant.title` only; long-reasoning purposes keep the connection effort. The parsers stay strict (Messages skips thinking blocks/deltas, Chat reads only `delta.content`, never `reasoning_content`, Responses only `output_text`), and `title_guard.go` is the second line: strip `<think>` blocks, reject a bar-delimited topic/first line that pairs a monologue opener with a second meta-reasoning signal, and never overwrite on a reject. An opener alone is not enough (`We need new icons`, `I should answer Sarah's email`, `我应该回复客户邮件` are titles); keep those near-misses in the guard test corpus.

- **Model listing and saving must agree on online launcher selection** (#1840). A cached provider's first machine can be offline even when another machine successfully lists its models. For a purpose without a bound machine, reuse `pickMachine` for model validation; if no reported candidate is online, allow saving without a live check. Keep known bound-machine validation and managed-launcher behavior intact. Cover the HTTP save and profile-apply paths, including effort-only edits, rejected online models, exact RPC destinations, and unchanged storage after rejection.

- A successful provider probe does not establish runtime compatibility (#2252, #11075). Direct-API calls resolve through `apiConfig`, while launcher tests can use transport settings that the selected runtime cannot honor. Carry validated custom headers through every direct protocol's save, test, discovery and inference, including launcher HTTP fallback and Send Through, and preserve the no-header defaults. Refuse unsupported proxy fields and runtime/model combinations before network I/O, while keeping Codex's real `env_http_headers` channel and Antigravity's limitation explicit. Seal values, return only header names, redact errors and diagnostics, retain headers on same-origin redirects and drop them after cross-origin hops; probes still never follow redirects. Protocol alone does not distinguish a Messages launcher from a direct-API provider.

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
  A confirmed send also opens the processing conversation's next turn in the background (#16974): `tellSoon` delivers the sent note through `deliverNotes`, which issues a new `ProcessingTurn`. A test that then plays the conversation's later turn (`suggestTool` reads the live turn) holds the handling pass and calls `deliverNotes` for the item first; otherwise the delivery can land between reading the turn and the suggestion's check, and the call is refused as superseded.
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
  header-width cells and ignores the excess, and `answerTables` (`botcard/answer.go`) built a row the same way, by
  looping over the header's columns alone, so everything past the header's width — here "b`" and "或" — never reached
  the card, with nothing said. The rich-text fallback kept those words, so the same answer read differently as a card
  and as a refused card's text. Those words are still the row's: keep the surplus cells in the last column, joined with
  " | " and with empty ones skipped (a trailing `||` adds no junk), clipped and escaped like any other cell. When
  changing how a table row is laid out, compare its card with its fallback.

- **Text a client draws as Markdown is cut with `clipMarkdown`, not `clip`** (#3818). The briefing card quoted the model's answer through `textfmt.Clip` (exactly n runes plus an ellipsis), so an answer over 1500 runes was cut inside whatever it was in: an unclosed code fence turned the rest of the quote into code, and a cut inside `**`, a code span or a link left half a marker or half a URL. `textfmt.ClipMarkdown` (`textfmt/mdtext.go`) cuts on a line break where it can, closes a code block the cut lands in, and moves the cut back out of an unclosed span, emphasis or link; it also flattens a `persona-card` block to its title (`wire.AssistantCardText`), so draw charts first with `feishubot.PersonaCards` and cut after, as `answerCards` does for the bot's answers. Plain fallback text (the push body, a notice's fallback) keeps `clip` / `textfmt.PlainMarkdown`; only the Markdown element takes `clipMarkdown`. Test through the same builder the card uses, not the clippers alone, and assert the shape (`fences % 2 == 0`, no `](https://…` left) rather than an exact string. The same trap caught the task report's item body (#4093): it drew the answer with `CardMarkdown` *after* `clipMarkdown`, which flattens a chart to its title on its own, so the owner read the report with no numbers. Any Markdown a client draws that is clipped first must have `PersonaCards` run before the clip.

- **A finished run's last write into the progress card has to wait out a card rate limit** (#3915). The progress tick
  answers 230020 by pausing its updates (`prog.quietUntil`), but the answer's write comes right after a tick's, and a
  card update (PATCH) gets no retry of its own on 230020 (`feishubot/api.go` leaves that wait to the caller, because the
  tick may be holding the run's progress lock): the answer was then posted as a card of its own while nothing wrote the
  progress card again — the run had ended — so it kept saying it was still working, and a quiet end wrote nothing at
  all. `botUpdateAnswerCard` (`server/internal/modules/lark/turn.go`) waits `answer_card_wait` (5 seconds, the first progress backoff) and
  tries once more before the answer falls back to a card of its own; another refusal still falls back at once. Waiting
  synchronously is the whole fix here: the run is over, so no tick can write that card later.

- **A quiet ending says nothing when the run has no card left, so a timeout must carry a note** (#3930). `channel.Run.Check`
  returned the 2-hour held timeout (`HeldMost`) with no note, and `checkChannelRun` turns an empty note into `Quiet`; the
  Lark sink answers a quiet ending only by rewriting the card it still has (`server/internal/modules/lark/turn.go`), and a held run has none —
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
  and writes four spaces per level (`listIndent` in `botcard/card.go`) in one pass over the whole answer, so the levels
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

## Supervisor assignment must respect the policy channel (#10338)

A runtime-backed chat can plan work and receive owner replies. A task reviewer needs a separate policy channel on every supervised turn. Check the session's execution source when dispatch assigns the reviewer, including plans that waited for approval and scheduled chats listed as leaders. A permanent policy refusal ends that relay. The task and its worker reports stay available for review. Routing only at dispatch is not enough: tasks a runtime chat supervised before that, and digests already queued for it, must route around it when they are delivered (`digestTarget`), and the node must never shut the owner out because a policy came with an ordinary message: it refuses only the manager's supervision message (#11121).

## The supervision fingerprint only sees the agent's words (#6027)

`digestFingerprint` (`supervise_dedup.go`) folds a signal into the one told in the last 30 minutes when its agent's
last words are unchanged, because a new turn or a periodic sweep is not news. Some signals carry news that is not in the
agent's words: the owner's answer to a decision's round, a recheck or undecidable review of a new round, a start
attempt's failure or start. They were dropped: the owner's "You decide" after "Tell me the background first" ended the
card but never reached the supervisor, and a retry that failed with the first try's error was never told although
`task_retry` promised it. Such kinds now hash their key too (`keyIsNews`), and a failed start's key names its attempt.
A new signal kind whose key names one event, rather than moving with every turn, belongs in `keyIsNews`; test it with
the agent's words unchanged, which is the case that hides the drop (the task's agent is usually stopped while it waits).

## New closeout admission needs startup reconciliation (#9914)

Screening a new idle signal does not revisit a stop whose receipt was already told before rollout.
Reconcile old stopped tasks from the existing supervision sweep, before producing new idle signals,
with the same current-turn, owner, queue, scheduled-wake and source-freshness checks. Bound transcript
reads per sweep and remember completed inspections; do not consume an inspection while inventory
or PR facts are missing, or when the ledger write fails. Persist the stopped-turn identity so a
restart neither spends another automatic attempt nor hides an already-delivered wake.
Bind a passive review wait to its final closeout's PR/MR artifact, not a link in an earlier closeout.

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

## A card marked delegating needs someone woken for it and a way up (#15360)

A leader's Bash approval (a Lark doc write the node's gate asked about) showed "Supervisor handling" for 21 minutes
with nobody told. The non-owner route had handed it to the main conversation: marked `delegating`, so the apps hid it
from Needs you and refused the owner's answer, and parked at once, so no timeout moved it. The main conversation got
only a quiet note (`keepRelayLater`) that starts no turn, and `ask_escalate` takes a task, which the card had none of.
Without a background supervisor the main conversation's own card went to itself. Before a route marks a card
delegating, check that the conversation it goes to is woken for it, can answer or hand it up, and is not the one
waiting on it. A new route also keeps the exceptions of the existing ones: `askLadder` already left the supervising
conversations' cards with the user, and the new route had to share that rule (`supervisorConversation`).

For an Agent's permission without an open to-do (#16093), use the active relay queue and keep the card available
in the app once its hold ends. Tie the queued message to the card's current route. A queued wake can outlive its card
or the manager that routed it, so check the route before sending and drop stale wakes even if their target stays offline.

For an internal question that outlives its manager (#17823), restore the task and its worker link before publishing
the question in the restart fixture. The real store already has that assignment. Capture the relay while its send is
blocked and again after the queue removal is durable. A pending card gets a fresh wake in both cases. Check the queued
wake against the current route before looking for an earlier send in the transcript, so the previous route's retry
timer cannot delay the fresh wake.

## One `todos` event per write, and nothing that copies every finished task on a timer (#5995)

Every `{"type":"todos"}` makes every app read the whole `GET /api/v1/todos` (about 1 MB at ~500 to-dos), and one step of
a task writes its to-do from several places milliseconds apart (the to-do, its report, its check, its supervision, its
leader): one supervised task sent 16 events. The event now goes through `todosPushSoon` and waits `todosSettle` (100 ms)
for the rest of the write, so each step sends one. Two traps: `askChanged` and the orphan pass broadcast `todos` on
their own and kept a step at two until they used the same path, so never call `s.event` with `todos` directly; and tests
that count events right after a write must wait with `todosTold` first. A pass that runs on a timer and looks only at
open tasks takes them with `todosWhere`, not `todosOf(ctx, "*")`, which deep-copies every finished one too (27 MB per
30 s at 4,000). The direction tree is not such a pass: a finished task decides whether a failed start in its row
still counts, so it reads them all. The To-dos view's pushes (`todo_view_push.go`) need the same answer without that
copy: their recheck and clock read only the to-dos they are about and take supersession from each to-do's title, PR,
plan and close alone (`projection.Superseded`).

## Disable ToolSearch per server, not for the whole assistant process (#5838)

Setting `ENABLE_TOOL_SEARCH=false` for Persona's assistant also forced every claude.ai connector's tools into each request (one connector alone was hundreds of tools and most of the context), causing repeated compaction. Never set it to false on Anthropic's own API (behind a gateway it is the right value: #10365, below). `personad mcp` marks only Persona tools with `_meta: {"anthropic/alwaysLoad": true}` when `PERSONA_ASSISTANT=1` (since #7317 only those the manager did not defer), and other servers keep Claude Code's normal deferred loading. The assistant's processes carry `ENABLE_TOOL_SEARCH=true` instead (#7317, below), which keeps that deferral. A regression test checks both the metadata and the process environment.
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

## Persona's rarely used tools wait behind ToolSearch, and so do held-off conversations' (#7317)

Marking every Persona tool `alwaysLoad` sent 96 full tool schemas with every request, about 49K tokens (Claude's
tokenizer), while in 30 days of one install's assistant conversations 20 of them made 95% of the Persona calls and 51
were never called. The manager now defers all but the tools called at least 5 times in that window, less the two whose
every call failed for want of setup (`ask_escalate`, `mail_accounts`): `coreTools` in `assistant/tool_loading.go`,
pinned by `TestCoreToolsPinned`. A new tool is deferred until it is added there. Deferring all but the 36 tools called
at least 5 times made a new conversation's request about 22.5K tokens smaller (measured); the shipped set defers those
two as well, about 23K (estimated). The manager sets the flag on every list itself, so a tool that arrives deferred
(a connector program's own `defer_loading`) still loads up front for a guest, an agent or while
`assistant.load_all_tools` is on. A guest conversation is never deferred: its `--tools ""` has no `ToolSearch` to load
one with. A deferred tool the model calls by name before loading it fails; `assistant.load_all_tools` puts every tool
back up front, in running conversations too: changing it wakes every `personad mcp`'s tool watch
(`assistant/mcp_tools_changes.go`), so it does not wait for a process restart or for the next task-link change, which
would otherwise refresh the list at a time nobody can predict. `inbox_suggest`, which the inbox's processing
conversations must call every round, is deferred too (it had few calls on the measured install), so each such
conversation spends one ToolSearch call before its first suggestion. It cannot stay up front for those conversations only: a process lists its
tools when it starts (a spare before it has a conversation), before the manager notes where the conversation came from
(`originSet`, which `assistant/start.go` calls once the node has answered). Add it to `coreTools` if the AI twin's
conversations show failed or extra calls.

Claude Code also holds tool search off for a conversation that once ran without `ToolSearch` ("Tool search disabled:
this conversation has run without ToolSearch, and stays so until it is compacted or cleared"), so conversations started
before #5839 sent every tool, other servers' too, until their next compaction: about 8.7K tokens more per request on
Claude Code 2.1.322. Any non-empty `ENABLE_TOOL_SEARCH` lifts the hold-off; the assistant sets exactly `true`, which
keeps the default deferral (`false` is #5838). Correction (#10365): `true` is the same mode as unset only on Anthropic's
own API. Any value also lifts Claude Code's rule that keeps tool search off behind a Base URL that is not
`api.anthropic.com`, so `true` for every conversation (#7371) sent deferred tools to gateways that refuse them; the
"endpoint-refused fallback" #7371 relied on is not in the Claude Code the machines ran (2.1.291). The assistant now sets
`true` only there (next lesson).

## `ENABLE_TOOL_SEARCH=true` also lifts Claude Code's gateway rule (#10365)

Claude Code keeps tool search off by itself when `ANTHROPIC_BASE_URL` is not `api.anthropic.com`, but only while the
variable is unset: `true` (or any value) overrides that rule. #7371 set `true` on every assistant process, and on a
gateway whose edge cannot represent `defer_loading` tools, `tool_reference` blocks or the `ToolSearch` tool, every
conversation on some models failed with `400 request is not representable on this edge: ToolSearch is not supported`.
Nothing caught it: the node's recovery takes a 400 as the user's, the hub had no code for it (the card said the request
was malformed), and Test passed, since `claude -p` checks set nothing and kept Claude Code's rule. Set the variable by
where the requests go (`toolSearchFor`): `true` on Anthropic's own API, `false` behind a known gateway (also in a managed
provider's own environment, which may opt back in), nothing where the start cannot tell. Decide per (launcher, model):
one gateway carried deferral for its Claude model and refused it for two others. Read the version you rely on: a
behaviour measured on one Claude Code (2.1.322) is not proof for the one the machines run.

## A leader's split must be somewhere the owner can see (#5846)

Owners lost track of their directions because a leader kept its breakdown in its own files and turns: the direction tree
only listed tasks with an Agent, so the pieces nobody had started yet were invisible, and the owner could only ask the
leader and read an answer full of its own labels. A leader's `todo_create` now opens a to-do under its direction, and the
tree shows pieces no Agent has worked on as `planned`. Two traps found on the way: such a piece has an `origin`, so the
To-dos SOP shelves it after 48 h untouched like any agent-made to-do (it leaves the tree; the leader touching it keeps it),
and the stuck rule must not count a piece that never started, or every planned piece older than a day reported stuck.

- Match Inbox PR/issue comments by full task URLs before the AI twin gate (#5578). A to-do may record its link only in `pr_url`, its title or its notes. Keep host, repository and PR/issue kind in the identity, leave ambiguous matches to the judge, and use a separate per-message signal cursor: the twin's `Of` cursor tracks handling, which does not run while it is off. Persist associations before forwarding them, and retain the existing supervision opt-in and exclusions.

## Size a tool's text in tokens, not characters (#7316)

Claude Code refuses an MCP tool's output past 25,000 tokens (`MAX_MCP_OUTPUT_TOKENS`, which Persona does not set) and
saves it to a file instead, so a result over it costs the call and the reads that recover it: on one install, 54 `kb_get`
failures in 30 days, all on projects, then about 70 Bash / Read / sub-agent calls. Characters are a poor measure. With
Claude's tokenizer on knowledge-base text, CJK alone was 1.08 tokens a character, but Chinese mixed with English words,
digits and markdown (summaries, logs, chat messages) cost up to a third more than "one token a CJK character, two ASCII
characters a token"; indented JSON was 1.8 characters a token, numeric ids 2.2, English prose 3.1. "One token a CJK
character, 3.5 ASCII characters a token" put whole `kb_get` results at two thirds of their real count. `kb_get`'s
ceiling (`estTokens`, `assistant/kb_get_fit.go`) counts a rune past ASCII as 1.25 tokens and an ASCII byte as half of
one, which stayed within 15% under the real count on all of these, and stops at 20,000. The size came from the data,
not the model: linking a group chat to a project adds all its members to the project's people, so one project's
meta.json held 3,708 ids, 92% of a 129,000-character result. Fold such lists in the tool's text rather than count on
the store staying small.

## A source read by pages must settle what left the page (#5993)

The Inbox's mail rounds read each account's newest 40 INBOX messages, and every rule that files a mail item (read
elsewhere, the 7-day window) ran only for threads on that page. Mail archived or deleted in the mail client, or pushed off
by newer mail, left its item open for good: nothing else files an open item, so the open side grew with the mailbox
(600 open items after 30 days of 60 mails a day, all read the same evening). A rule that only fires on what a bounded read
returns needs a second path for what it no longer returns: here past-window items are filed without asking, and the rest are
looked up by UID once a round (`inbox.mail_flags`), bounded, with a failed lookup changing nothing. Tests of such a round
must wait for the background triage pass (`waitTriage`) before setting a tier by hand, or the fallback tier overwrites it.

- Literal `for` lists need per-value classification (#6266). Straight-line assignment tracking cannot resolve a loop's route segments or executable, and accepting GET alone misses action routes such as `/documents/delete`. Bind only literal lists in a completely inspected shell tree, retain quoting and reject unquoted values that could split or glob. A later assignment, state-changing builtin, executable-environment change or nonliteral nested list invalidates the entire attempt; never keep a partially expanded prefix. Resolve command positions before unwrapping so a variable cannot become a gate, CLI, shell or wrapper. Bound nested expansion and classify every resulting call through the existing read and hard-boundary rules. Both Claude and Codex adapter tests must count actual fake-hub approval cards; classification fixtures must never execute the supplied commands.

## Deleting a chat is more than removing its list row (#6172)

Keep the tombstone in the owning manager and carry an identity-only row through the existing fleet transport. Persisted
map keys use the existing JSON tuple encoding, since Postgres jsonb rejects NUL separators; test the database roundtrip.
List filtering alone leaves direct transcript requests, WebSocket replay, already open subscriptions, tool reads and runtime
resume paths reachable. Check every referenced identity on a start, including `resume_session_id` alongside a new id,
and discard a read that finishes after deletion. A fleet notification is not an applied acknowledgement: apply the manager's successful deletion to the hub before returning HTTP 200. Fence the final HTTP header commit per session, suppress late broadcasts at enqueue, and redact retained replay entries; do not claim to retract bytes already committed before the acknowledgement. A pending main-chat handoff also contains old turns: suppress it when
its source is deleted. Task origins and previously saved knowledge remain separate records; deletion must not stop the
Agents those tasks started. Tests use synthetic conversations, references and node events, never a live user's history. A current bus connection is not a single database writer: service-to-child handover can overlap, and a replaced manager can reconnect. Cache-only deletion checks and whole-map writes fail across those instances; use fresh guard reads, atomic entry merges and a transaction fence shared with index writes. Search indexes also mirror the transcript: purge the rows and cursor, fence late index writes, and recheck search output. An aggregate reply serialized before deletion must be refused at header commit rather than forwarded with stale snippets or guessed progress counts.

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

The count did have one way to go wrong that the stress test could not meet: a panic unwinding through a request
skipped `done`. Where the caller dies with it nothing is left to strand, but the knowledge base loop (`kbRun`) and
its job pool recover and carry on, so the request stayed counted as out for good. As the request finding out, nobody
found out after it; as any other, the next busy answer capped the connection at what was "in flight". Either way every
later request was put off as busy with nothing sent and nothing in the log, the state the report describes
(`TestInboxTriageRecoversAfterAPanicOnItsConnection`). What would panic there is not known. A count that a recovered
panic can leave wrong is given back with `defer`; a random walk over the events (`TestTheGateCountsRightInAnyOrder`)
checks the orders a stress test meets only by chance; and a status that says "at its limit" needs a number in the log
that can say otherwise: the requests out, the cap, and how long the longest has been out, since none outlives its
caller's deadline.

## Channel image downloads reuse the bytes already read (#12654)

An inline-size check must not discard a resource that its file fallback immediately needs (#12654). The official Feishu bot read a 4 MiB image twice: the 3 MiB inline decision used the 50 MiB downloader, then saving fetched it again. Decide inline versus file inside the same download lifecycle and upload the existing bytes; pass the image-count decision into that call too. Keep the file ceiling on both routes, distinguish download and upload failures, and turn only the size sentinel into the existing skipped-file result so accompanying words survive. Count actual response-body reads and uploads at the production transport entry, and separately test the asynchronous message entry.

## A send whose answer came too late may have gone in (2026-11-12)

The manager waits 90 s for a relayed message (`drainRelays`) and kept it queued when no answer came. The node had
written it after the first of two session-list rebuilds inside the send and was still building the second when the
time ran out, so every later drain wrote it again: one conversation read the same message 32 times in an hour, and the
messages queued behind it waited. A send in doubt is not a failure. Relays now carry one `once_id` per message
(`relayMsgID`), which the node sends once while it remembers the id (`sentLog`). Save the attempt with the queued
message before sending (#10690). Look for its line in the latest turns (`relayArrived`) using its text, time and
sending conversation. A context-only relay also carries its `once_id` in its message-source metadata, so it can be
found even if the transcript reader removes its original context. A failed read or
a late transcript write can hide the line, so check again on a bounded schedule before retrying. The send pushes
the session list in the background, waiting for it only briefly
(`notifySessionsSoon`, `listPushWait`). The id is deliberately not a `client_msg_id`: a line tied to one reads as the
owner's own message everywhere (its source label, task briefs, user-input counts, extensions). The web had the same
blind spot for the owner's own sends: a send answered 502 `delivered: "unknown"` stayed a failed bubble unless the hub
tied a transcript line to its id, so the owner sent it again; a send in doubt now also settles by its words written
after it (`inDoubt` in `web/src/lib/outbox.ts`; `isEcho` in `web/src/lib/echo.ts` allows 5 s for the clocks, not
`CLOCK_SLACK_MS`); one whose copy arrived before the answer did was already settled while it went (`flight.echoed()`
in ChatView). A refusal never settles by words.

## A node's error keeps only its words past the bus, and a call can fail after the node did it (2026-11-19, #10465)

A to-do's message or answer that fails has to say whether it typed anything, or a phone offers to send again what the
Agent already has. Two readings looked right and were not. An RPC error that crosses the bus keeps its code and its
message, not its Go type: a deadline that ran out past the bus reaches the manager as an `*rpc.Error` whose message is
`context deadline exceeded`, which `errors.Is(err, context.DeadlineExceeded)` does not match. Read it by that exact
message (`noSendAnswer` in `sendUnknown`, or `apierr.NodeCode`, which reads it as `timeout`), never `errors.Is` alone.
And `Service.call` can fail after the node answered OK: it checks the deletion ledger again and decodes the answer.
Such an error of a `session.send` or `session.start` is `completedNodeWriteError` (`afterNodeWrite`), which the to-do
routes read as typed (`writeDelivered`, `server/internal/assistant/todo_write_outcome.go`). #11274 found the same two
pitfalls from the Agent-send side at the same time, and the two changes now share `sendUnknown` and that type. The
to-do routes read a little more as in doubt than `sendUnknown` does (a connection that ended mid-answer, any other
network error). A refusal said "nothing typed" wrongly is the unsafe side: when in doubt, say `unknown`.

After a conversation switch claims segment preparation, a failed receipt lookup can only tell us about that lookup
(#16066). Keep checking the same operation until its preparation deadline. Starting a replacement before confirming
the original could leave two prepared segments.

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

## A drifted workspace and its launch require separate checks (#17087)

Retry replays a frozen operation, while a continuation with changed inputs can collide with the previous head. Confirm
Git drift against the exact Node receipt, then let the Node decide whether its launch has stopped. Replace the manager
head under the to-do lock and prepare a distinct directory, keeping the old checkout available for inspection.

## A hidden effective cadence overrides the interval the user configured (#7619)

#3811 gave interval follow-ups in leader conversations a hidden 10-minute cadence while an open supervised todo had an active agent: `nextWithLeaderCadence` derived extra runs from `LastRun` while leaving the stored `At` / `EveryMinutes` on the original rule, and the run copy's `cadenceEvery` made `taskPrompt` print "每 10 分钟". The stored task still showed 30 minutes / 1 hour in `list_followups`, so nothing visible explained why a patrol the user asked for every 30 minutes fired every 10 minutes from its first run, and it became the main chat's biggest token sink. Lesson: a scheduler may have exactly one effective cadence, and it must be the one stored on the task and shown in every view and run prompt; an "accelerate while busy" rule that is invisible to both storage and UI is indistinguishable from a bug. If such a rule is ever wanted again, it must be an explicit, user-visible setting with its own stored field, not a second rule derived inside the scheduler. Keep interval advancement in one place (`runDue` advancing `At` by `EveryMinutes`); tests assert the prompt text states the configured interval even while supervised work is open.

## Replay must respect the decision that owns the task now (#5732)

A parked task intermittently regained an old idle signal when `watchTurn` replayed an untold turn-ledger entry. The guards in `superviseJudged` and the completion path could not catch it because recovery called `signal` directly. Every producer, including ledger and durable-outbox replay, must respect the review decision that owns the task now. When parking supersedes an old stop, acknowledge that exact signal with `turnsTold` rather than emitting it or leaving it for every later sweep. A regression must seed the real untold ledger and retain an unparked replay control; changing model or retry timing can expose the interleaving but is not the root cause.

## Retract a signal from every durable form, not only the outbox (#5732)

A confirmed `start-unknown` can still replay after pending memory, the durable outbox and the turn ledger have all been cleared: queue admission has already copied its structured signal into a relay message, with a second in-memory index in `supervise.queued`. Retraction therefore has to cover the queued digest too, using its exact Todo/Kind/Key rather than deleting every signal for that task. Removing a mixed digest means regathering its other signals without overwriting a newer durable stretch. An actively draining head owns its original text only for the current try: mark it withdrawn with the remaining structured signals, but do not regather those signals before the send's outcome is known. A successful original send already carried them; regather only when a later drain actually removes that same withdrawn head after failure or restart. Before replaying, check an earlier in-doubt send with `relayLanded`: a late-visible original line also means the other signals were delivered. The send's `DoubtedAt` must stay on the durable queue head across restart and head rewrites, and its in-memory doubt is forgotten only when that head is actually removed. A withdrawn or doubtful head cannot be replaced or have its other signals regathered before that check, including when confirmation arrives only after the send settles. Selective `/stop` uses the same withdrawal: once any signal is removed, the old mixed text must not be retried. Keep the remaining slice non-nil even when empty: `signals:[]` is a durable detached marker, whereas `signals:null` reloads as a legacy text message and can rehome the withdrawn task again. If another confirmation changed the head, the old copy must not replay signals from a queue entry it did not remove. Hold `dedupMu` across retraction so `sendDigest` cannot admit a new queue message between confirmation and withdrawal. Test acknowledged, in-doubt-late and failed in-flight sends, including restart, a changed-head control and a different-Key control, not just the pending map.

Cleanup in `watchTurn` is too late when the production restart path drains an idle supervisor first: `SessionsChanged` used to call `drainRelays` before exact creation-id linking, so even a persisted told receipt could not stop the stale queue head from being sent. Reconcile told receipts and the current list's non-offline exact creation ids before that drain, and hold the drain when the relay or ledger cannot be read or written. A failed first relay read must not fall through to the drain's successful second read. An empty queue is not a shortcut: the next digest can restore the same told signal from the durable outbox alone. Hold `pendingLoadMu` before `dedupMu` across the whole confirmation batch; otherwise `restoreSupervisePending` can snapshot the old outbox before cleanup and insert that stale snapshot afterwards, or a digest can slip between two confirmed ids. Cover these through `SessionsChanged` with actually delivered stale-message assertions, an outbox-only control and a first-read-failure control, not only by calling `watchTurn` directly.

## Closing a task and recording delivery need one admission boundary (#5732)

Checking `DoneAt` after a push returns, even again under `supervise.writeMu`, does not make a receipt atomic with `editTodo`: the user close does not take that mutex and can land after the guard but before the stored receipt. The guarded receipt must take `todos.writing` before `writeMu`, matching other todo writers, and hold it through the guard, database write and in-memory mirror. Close then either commits first and makes the guard refuse the receipt, or waits and commits after it. A regression needs a real store gate after the guard; closing while a notification RPC is in flight or merely clearing an earlier `writeMu` does not cover this interleaving.

## Scheduled runs need observable to-do progress (#9755)

- A successful scheduled run is not progress on its linked to-do (#9755). Compare transcript output, criterion
  states and explicit progress-report content, not updated timestamps or the follow-up's own summary. Keep the
  streak and baseline with the schedule, not only in capped shared history; test restart, history eviction,
  replayed completions and failed persistence before emitting a supervisor signal or an owner decision. Keep the
  first escalation attempt's identity across runs: its committed owner review repairs a lost final marker without
  duplicating that decision or mistaking an unrelated owner question for its own.


## A stopped leader's own To-do does not list its workers (#8400)

Workers planned by a task leader have separate To-dos: their `todo_agents.source` is `worker` and `via` is the leader's
To-do ID. A one-To-do scan can mistake a leader waiting on an active worker for a stopped group, or miss workers waiting
on each other. Join the persisted links in both directions, require every current member to be observed, and reuse
`supersededAgents` before checking inactivity. A background label is not evidence of recent activity; use timestamps and
state the start-time approximation when a shell has no activity field. Include the resulting group key in semantic
deduplication so identical last words do not erase a later, genuinely new period of inactivity. The short semantic
fingerprint window cannot guarantee once per stopped group across restarts: keep only the latest group key until the
task closes. A failed state read must remain retryable, not become a moot signal marked sent. Runtime children need the
same starting and queued-input guards as their parent, even when their row says idle. Missing background activity times
must not grant an indefinite exemption: the launching Agent's stop time bounds the age, including summary-only flags.

## A signal told once needs somewhere to go when its reader never acts (#8043)

Before #5732, a side conversation supervised the tasks it handed out, and "waits for review" was told once per review. A side conversation that had stopped, or whose turn ended without `task_review`, left its task awaiting review for good: the send-time fallback to the main conversation covers only a conversation that is gone, the orphan check leaves out a task waiting for review on purpose, and the stuck-queue remedy covers only the background supervisor. When a signal is told once to a reader that may never act, give it a bounded wait and a second home (`handOverUnreviewed`, `server/internal/assistant/side_handover.go`), and record the move where a restart finds it (here the ledger's `supervisor`), so it happens once.

Telling the same review to another conversation is not a new signal to the deduplication window (`superviseDedupWindow`): its fingerprint is the task, the kind and the agent's last words, not who was told. A hand-over inside the window is dropped as "said the same" unless its fingerprint is forgotten first (`forgetSupervision`, as a gone background supervisor's queue does). Check "is this conversation in a turn" against the machine's list the way `backgroundThere` does: a manager that has just started has an empty list, which says nothing about a conversation being gone.

## A lark-cli risk lookup can fail quietly, and then every read is a write (#8389)

The node asks `lark-cli <command> --help` for each command's risk. When the hub config has no `lark.cli.bin`, the binary
comes from an interactive login shell, and that can be a user's wrapper script that looks for the real lark-cli on `PATH`.
The lookup runs with the node's own `PATH`, which may not contain the real lark-cli. The wrapper then exits without a
`Risk:` line, the failure is not cached, and every shortcut, plain chat reads included, goes to the phone as an
unknown-risk write. Stable read shortcuts are listed in `larkCLIKnownReads`, which is consulted only when the lookup gives
nothing, so lark-cli's own answer still wins. When a card names a read as a write, check the lookup in the node's
environment first, before changing the classifier. A literal `cd` before a command variable (`cd /tmp; L=/abs/lark-cli; $L …`)
now resolves only absolute values: a relative one is looked up from the new directory.
The short list was not enough (#11193): `calendar +agenda` still went to the phone as "建 / 改日程". When the lookup gives
nothing, a command named as a read also counts as one (`larkCLINamedRead`): a shortcut whose name is, ends in or (get /
list only) starts with fetch, agenda, search, list, get or mget, and a generated method named list, get, search, mget or
batch_get. Every other name, sends, replies, reactions, create and update included, stays a write.

## One `$?` in a literal loop made its lark-cli reads writes (#11193)

`literalLarkLoopCommands` binds a literal `for` list into each lark-cli call so a variable path such as
`documents/$d/raw_content` can be read. It gave up on the whole loop at any other parameter, and `echo "$d exit=$?"` was
enough to leave `lark-cli api GET …/$d/…` unbound, unclassifiable and carded as a write. `$?` is now kept as inert text:
it is always a number the shell sets, so it can neither name a program nor become a lark-cli word the classifier trusts
(a path holding it is still unknown). When a loop of reads is carded, check every parameter in its body, not only the
loop variable.

## A rejected answer must not consume Don't handle (#7907)

Validate the ask round and the answer before changing its dismissal. Keep the persisted receipt in force while an
answer is being sent; only complete the undo after it is taken. A retryable send failure must leave the dismissal
unchanged. A recorded choice handed to the supervisor when no Agent can receive it is already taken, not a failed
answer to retry. If the final dismissal write fails after delivery, retain its receipt and do not make the already-sent
answer retryable. Tests must check both the in-memory receipt and stored one, plus stale rounds and failed sends.

Restore the previous `DueAt` on acceptance-save failure, not an empty time: an owner message in the Agent chat uses
that marker to pause the default. A later rejected answer must preserve both that pause and the dismissal.

## Task-close guards must not wait for activity storage (#6819)

Publish owner input in the activity cache before saving it, and keep the cache mutex out of database I/O. A review's
transaction can hold the connection that an activity save is waiting for: its final guard must read caches only, not
wait for that save. Serialize activity snapshots separately so a slower save cannot overwrite newer input.

The judge's unsampled acceptance and a leaderless worker's cancellation need the same atomic ledger-and-closure
commit as `task_review`. A refused close must leave no accepted/cancelled ledger or checked criteria on an open task.
Defer the judge's completed turn on late owner activity so it can be judged again after inactivity; return a
cancellation only when it actually committed. Regress the real owner bus event while storage is blocked, and check
both the stored ledger and memory at the ledger, closure and final transaction boundaries.

Lifecycle tests wait for the operation they exercise: acknowledge `task_review` before sending the next owner turn,
and wait for `fleet.clients` notifications until the viewer snapshot matches. A terminal error can arrive before that
snapshot. Fixed delays hide this ordering and grow the sleep ratchet; keep bounded failure deadlines, not guessed sleeps.

- An opt-in terminal cleanup must not replace a guarded task retirement with an unconditional close (#6819, #10355).
  Keep cancellation and the final input check through both irreversible boundaries: stopping the Agent and closing
  the shell it left. Exercise the setting on and off, including input arriving during terminal inspection.

- A terminal task review must commit its closure before publishing review effects (#6819). Activity can arrive after the initial guard, while another writer holds the ledger or task lock, or during a database write. Validate on a private ledger, save the ledger and closure in one transaction, and recheck activity immediately before commit so a refusal rolls both back. Only then record the brief result, review feed, decision changes and supervision signals; apply the same boundary to a task leader's review and tasks without a ledger.

## To-do topology writes share the review lock order (#17693)

Reopen, terminal review, guarded park and topology writes take the to-do writer before the supervision writer.
An idle-close fence waits with both writers free. Reorganize, split and merge validate the graph before any
side effects, commit the rows and their related records in PostgreSQL, then publish one in-memory batch.
A split retry uses the existing child identities, and a merge's source closes only after its target's changes.
Auto-seen checks both the completion and reopen timestamps. A reopen clears the previous round's seen and Undo marks.

## Reserve a conversation for replacement in the same step as the check that nothing goes in (#7315)

The background supervisor is replaced when its rounds outgrow it, and the first version checked that no queued message
was going into the old one, released the relay book's lock, and only then started the replacement, which takes seconds.
A session list pushed in that gap could drain the old one's queue into it (found in review): that round would run in a
conversation archived moments later and no longer the background supervisor, so its `task_review` of a main-chat task
would be refused, and a message queued before a manager restart, whose signals were not in memory, would leave the queue
as delivered instead of moving to the replacement. A check that a resource is free is only good while the lock it was read under is held:
mark the conversation as being replaced under that same lock (`relayBook.replacing`), have every drain and every direct
delivery respect the mark (the direct ones queue), and clear it only where what waited has moved or the replacement
failed, draining it again then (`TestNothingGoesIntoABackgroundSupervisorBeingReplaced`).

## Personal verification is unfinished work (#10047)

A supervisor's accepted review is not the owner's personal verification. Keep an explicit `user_check` task open
until Pass, so both completed-task and ordinary idle retirement leave its Agent available. Feedback starts a new
work round even though the task was already open: otherwise its old Pass can verify rework. Test open counts and
the actual retirement passes as well as the card; a still-pending card alone never proves its task is open.
The acceptance record and completion are separate durable writes: recover a saved owner Pass after a restart,
including the verified stage if the todo was already closed.
Recovery follows the current round's owner Pass, not only a `user_check` ledger: completion and the ledger's
transition to `accepted` can each precede a crash before verification. Retry only unfinished steps, keep ledger
write failures recoverable, and do not repeat completion or verification notifications on subsequent sweeps.
Serialize the round change, not an ordinary message's Agent-start wait: a second message must still join the
Agent already starting (`TestStoppedTaskSecondMessageGoesToTheStartingAgent`), rather than wait behind the
first sender's action lock.
Cover the real database too: an ordinary `done: false` edit of an already-open todo intentionally does not advance
`reopened_at`. Personal-verification feedback must explicitly restart that work round in the store; an in-memory
timestamp alone lets an old Pass verify the rework after a manager restart.
Reset the raw `user_check` ledger on every reopen, including a closed task's PATCH, since its effective state
already reads accepted while closed. A fresh personal-verification review reopens before writing its new ledger.
Commit the reopen and ledger reset together: a partial write leaves the task waiting on a Pass it can no longer use.
On startup and the acceptance sweep, rebuild a missing ask from its saved review. Save the ask before posting the
card, and check the round after taking the task action lock so a queued Pass cannot complete a newer check.
Do not restart a waiting verification until its replacement review passed validation and was saved. Release the
round's action lock before feedback delivery, and bind the eventual reply receipt to that ask's timestamp so a slow
delivery cannot swallow a newer verification.
An ordinary edit that repeats `done: false` is not a reopen. A saved replacement review also makes the old Pass
stale if a later work-round write fails, so a restart cannot verify rework through that old card.

## A queued supervision digest is not a snapshot of work that still needs review (#10424)

An interruption and an unjudged turn can wait behind a busy supervisor while that task recovers and is accepted.
Replaying only the rendered text then starts obsolete work or retries a refusal forever. Persist the structured signals
with the relay, recheck the loaded task ledger immediately before delivery, and render only the surviving entries.
A failed ledger read or checkpoint is unknown: keep the original queue. Move a changed conversation destination and
remove its source entries in the same saved relay state. Do not change an uncertain send's text or destination: its
old once-only identity may already have reached the node. Test both an entirely obsolete digest and a mixed one,
including a real database reload into a new service; the in-memory signal map alone does not survive restart.

An Agent's PR artifact is a claim, not proof of ownership. Deployment reports and
evidence often name other Agents' PRs, sometimes under the same GitHub account.
Automatic repository links need a native successful creation record or an
independent node read matching author, head branch and published commit (a native
push can establish the actor for a shared PR). Exclude another open to-do's PR in
the same transaction, including concurrent reports, and apply the result to legacy
stage PRs too; otherwise feedback routing and deterministic duplicate admission
inherit the false association (`todo_repository_ownership_test.go`).
Native CLI evidence must exclude help, web and dry-run modes, including Boolean
`=true` flags, bundled short options and arguments whose shell expansion is unknown.
The node's `gh` lookup covers GitHub PRs. A manual review can proceed when another host
supplies no verified artifact. Any already recorded GitHub PR still goes through the delivery
gate. Reuse the stored canonical URL after a verified import. Apply GitLab's legacy MR
alias only on its known host so unclaimed review URLs keep their own meaning.

## Manual compaction is not a main-only policy (#10064)

The manual HTTP and node paths already accept side conversations, but all three client menus hid the action behind
the main-chat check. Android also read and displayed only the main chat's progress. When extending a conversation
action, trace its eligibility, request target and progress source together; progress must reset when the selected
conversation changes. Keep automatic main-chat policy and the main-only clear/new actions separate. Menu regressions
must exercise a real non-main selection and fail when the old guard is restored.

## A check that passes must have sent what the chat sends (#10365)

"Try it" ran `launchers.check` with the Main chat launcher's name alone: the launcher's default model, the login shell's
profile, no tools, no tool search. The chat ran a picked or configured model in its own profile with Persona's tools,
and failed with `model_route`; the check passed, its newer pass cleared the readiness card, and the next message failed
again. Evidence about a model is about one request shape: resolve the probe's target with the send path's own code
(`sendModel`, read-only), have the node build the check as the turn (`launchers.check` `assistant_turn`), and key the
evidence and its reuse by machine, launcher and model, so a pass of something else, or of an older plain check, never
outranks the real turn's failure (`TestReadinessProbeOtherModelDoesNotClearModelRoute`).

## An agent's own slash command works only as the whole prompt (#10950)

In an Antigravity conversation, sending `/compact` reached the model as `/compact/compact` and compacted nothing. The
web composer was not at fault: the hub's `sent_messages` held `/compact` once for every send. The node put the
`<persona-context>` block in front of the text, and `agy` runs a command only when it is the whole prompt; with a block
before it, it records the line as plain text. Reproduced with `agy acp` alone: the block plus `/compact` is recorded as
`/compact/compact`, the bare `/compact` writes a compaction checkpoint. `assistantSendRuntime` now sends a one-line
slash-only message on its own, leaves the snapshot unsent for the next message, and after `/compact` forgets the
snapshot the conversation has seen (`TestAssistantOnARuntimeSendsASlashCommandOnItsOwn`). The chat bubble showed the
doubled text because the chat is drawn from the agent's own transcript: when a user bubble looks wrong, compare it with
the hub's record of what was sent before looking at the client.

## Permanent conversation errors outrank silent-turn hints (#10399)

When merging no-output turn handling, keep account-sealed compaction ahead of `no_output` / `stopped` handling in triage
and clients. A generic kind must not restore Continue for a permanent error; ordinary silent turns keep their own text
and Continue. Test both paths together, including the clients' independent invalid and plain-text flags.

## The hub's codes reach the manager in two shapes (#11393)

A relay refusal was classified with `errors.As(err, &rpc.Error{})` alone. That misses the hub's own codes: over the bus,
`busError` gives `machine_offline`, `node_closed` and `no_device` back as a `codedError` that wraps a Go error kind
(`apierr.ErrOffline`, `rpc.ErrClosed`, `ErrNoDevice`), not an `*rpc.Error`, and the manager's own machines answer
`apierr.ErrOffline` with no code at all. Read from the code, `conversation_send` to a computer that was away returned
the error instead of queueing the message as #10194 meant it to. Read a code from either shape (`relayErrCode`) and
test each failure in the shape production delivers it (`busError(&rpc.Error{…})`), not only as a bare `*rpc.Error`.

## A plan's new Agent is a new round, and a shared conversation's latest turn is nobody's reply (#11553)

A continuation (`task_plan` `continues`) reopened an accepted to-do and refreshed its ledger's criteria, but left
`state: accepted` with the earlier round's acceptance as the latest review. Every client and the hub's triage read
"open + accepted + latest review accepted" as an acceptance that stands, a rule made for more work sent to an accepted
task's Agent (#3156). So the continued to-do showed **Accepted** in Working, went to Just finished whenever its new
Agent stopped, and `superviseJudged` dropped its stops. Reset the terminal verdict where the new round starts
(`takeTodoForTask`) and keep the history. Leave the send-after-acceptance path alone, and pin both directions in tests.

A linked scheduled run read its explanation from the latest turn of the conversation it went to. In a source
conversation shared with other work, two runs due in the same minute both read the second run's answer, and the first
to-do's no-progress decision quoted another task. Find the run's own prompt by its run key (`runKeyIn`) and take the
reply after it, up to the next message that opened a turn. Refuse turns that a queued message mixed. Test with real
transcript pages (`runChat`), not bare item lists.

## Direct reply recovery across a hub switch (#10427)

A node can lose a live conversation during a hub switch without publishing a normal turn end. Claude can append a
synthetic placeholder only when the conversation resumes. Keep the latest verified app input ID alongside the
recovery claim, and check that input when the node lists the conversation again. Read the current transcript,
since indexed history can predate the echo that identifies the app input. A guarded send that finds the same
transcript but no process leaves a retry notice. A Stop or later owner input makes the earlier attempt obsolete.
An accepted Assistant input supersedes an older explicit Stop or Exit. Clear only the mark captured before the send,
since an interrupt can record its intent before it gets the typing lock. Session metadata saved during delivery must
preserve that newer mark.

## A node's own session rows never carry the assistant mark (#13879)

`Session.Assistant` is set only when the hub decorates a list for the apps (`hub/assistant_link.go`). The manager's
`Devices.Session` returns the node's raw row, so a check like `!c.Assistant` there is true for every main and lead.
Cache keepalive revoked every role this way and never mounted a proxy, while its tests passed because they built the
inventory with `Assistant: true`. Judge a raw row with `fleetMachine.isAssistant` (its directory), and build test
inventories the way a node sends them. The same feature also needed Claude Code's `ENABLE_PROMPT_CACHING_1H`: without
it the CLI declares only five-minute markers on an API key or a gateway, whatever TTL the gateway applies later.

## Tool discovery can invalidate an entire warm conversation (#13719)

Adding an always-loaded tool to the top-level list can invalidate a warm Claude conversation prefix. Test
the full ordered list before and after discovery and reconnection, including a different upstream order.

## A supervision hold belongs to the turn that ended (#15549)

Save the hold timestamp and promotion together. Clear the active record at the end of its turn and carry that
turn's decision to the delayed push and channel mirror. Reading the conversation's current hold at notification
time can pick up the next round. A restart test for promotion must show the hold still exists before it checks
that a silent finish is refused.

## A wait the Agent names outranks the state the hub guessed (#14899)

A supervised Agent reported `blocked` / `unblock: other` ("someone has to fill in the web form") while two of its old
polling shells were still running. Triage's background rule read the turn as "N background tasks running, it wakes
itself" before it looked at the closeout, and the same background work kept the task's turn from settling, so no
receipt, judgment or supervision signal went out. The task also sat in `needs_user` from an earlier decision the owner
had dismissed, and `superviseJudged` returned early in that state. Nobody heard of the wait for three and a half hours.
A rule that runs before the closeout is read must give way when the closeout names a person to wait for, and
holding a decision open must not silence the task's later turns. The supervisor decides whether the news replaces the
decision. A `done` closeout that still names the owner as the one to unblock it (`unblock: user` with a `waiting`
line and no delivery) is a question, not finished work.

Settling such a turn right after triage must not read the work state from the session lists: they pick up the
triage's state only on the next publish, so the settle saw the previous turn's state and put the turn off again, with
nothing left to wake it. The settle takes the triage's own entry for that turn (`withPersonWait`).

## An inbox row can carry more than one provider event (#15517)

Keep the deduplication key independent of the Inbox row and the binding that matched. Two readers can report
the same comment, and the row can collect a later comment while the first is waiting for its digest. Commit the
source receipt with the Inbox write. Reserve only one action owner and pin the delivery before contacting the
Agent. Record uncertain delivery against that recipient so a restarted worker does not try another one.

## Check-time `@file` content is not what an Auth-less gate sends (#8413)

A command gate without `Auth` has no local proxy to inspect the HTTP body at send time, so check-time `@file` content
cannot grant a silent body-scoped `allow` or bypass an earlier body-scoped restriction in default mode. Read `@file`
bodies and `--url-query @file` inputs through a pinned `os.OpenRoot` with `openKnowledgeFile` (`O_NONBLOCK|O_NOFOLLOW`),
reject final symlinks, non-regular files, files over 1 MiB, and lexical or resolved `/proc`, `/dev` and `/sys` paths.
Output-filter whitelists also need argument checks: `sort -o` / `--output` / `--compress-program`, `date -s` / `--set` /
positional timestamp operands, and `uniq IN OUT` write files, execute programs, or mutate system state rather than only
formatting output.

## A loop started with the run's cancellation taken off outlives the run (#16215)

`FleetUp` handed `fleetStart` `context.WithoutCancel(ctx)` so its one-time reads would finish, and `fleetStart` started
`liveLoop` and `expireLoop` on the same context. When a manager's run ended without the process (its token revoked, a
restart in place for a new binary), `run()` closed the store, but both loops went on against the old Service: every
15 s `liveLoop` read the settings through the devices' time zone hook and logged `assistant: settings: closed pool`,
for days, and each restart left another pair. Take the cancellation off only the one-time reads; a loop gets the
run's context. Test it by finding the loop's goroutine in the stacks, ending the run, and waiting until it is gone
(`server/internal/assistant/fleet_loops_run_test.go`).

## A start path of its own skips what the shared start decides (#14887)

`a2a_send`'s new Agent and `dispatch_start` built their own `session.start`, so they never got two things `agent_start`
and `task_plan` decide before a start goes out: the start host (an agy launcher on the computer's Antigravity companion,
`companionStartHost`, #13447) and the read-only refusal (`readonlyLauncherRefusal`, #8316). An agy launch, in `plan` by
default, went to a pane, where the node refused it after its A2A task was recorded. The failed task then said "Persona
could not confirm the Agent started.", the words for an unknown outcome, about a definite refusal with a reason. A path
that starts an Agent readies its start with the same steps as the others (`agentStartedHost`,
`server/internal/assistant/start_host.go`), refuses what it can before anything is recorded, and keeps a definite
refusal's reason; only an outcome that is really unknown says it is unknown.

