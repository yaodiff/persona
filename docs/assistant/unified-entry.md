# Unified main-chat entry point (#930)

Complete all work in the assistant's main chat: say what you want, and Persona breaks it into tasks, assigns suitable Agents, watches progress, and interrupts only when it needs you.
Personal information (calendar, reminders, email, Lark) and work Agents share this entry point. Many tasks can run at once, and the conversation can continue indefinitely.

Interaction prototype: `docs/assistant/prototypes/unified-entry.html`. Open it directly in a browser; `#step=5&theme=dark` jumps to step 5 in dark mode.
The prototype is the UI and interaction standard; compare screen by screen during implementation.

## Boundaries

- **Do not change the Agents page or the top-bar "Assistant | Agents" switch at all**, or move them behind any secondary entry point (requested 2026-11-03). Tasks started from the main chat are ordinary
  Agent sessions that appear normally in Agents; both sides share the same data. This feature changes only the assistant side.
- No separate task system: **a task is a to-do** with attached Agents (`docs/assistant/todos.md`), plus a record of which chat created it.
- No second Lark client or source-specific browsing pages (`docs/extensions/feishu.md`): messages appear in the chat only in relation to tasks.

## Three layers

| Layer | What it is | Data source |
| --- | --- | --- |
| Chat stream | Your conversation with Persona plus hub-inserted system entries (plan cards, task cards, receipts, answered questions, messages routed to tasks), all ordered by time | Transcript + chat activity (see "Chat activity" below) |
| Task dock | A row of pills above the input bar: working / waiting tasks started from this chat; in the main chat, manually started working / waiting Agents follow the tasks. "All N" at the far left (text only) opens the task board | To-dos + attached Agent state; manual Agents come from the Agents page's session list |
| Needs you | When a task's Agent needs approval or an answer, a question card appears at the bottom of the chat for an inline response | Existing interactions (`Interaction`) |

## Interaction specification

### Dispatch: express intent, do not choose a project

- Persona splits multiple requests in one sentence into tasks and passes them to hub through `task_plan`. Each task includes its inferred computer, directory (project), launcher / model, permissions,
  an optional host (`agy` for the Antigravity companion, which an agy launcher gets by default where the companion runs it, or an engine host; `docs/integrations/launchers.md`, "Work agents") and a one-line rationale. Inference uses knowledge-base project-to-directory mappings, the computer recently working in that directory (`agents_list`), the default launcher, and your past choices.
- Three cost tiers determine whether to show a plan first:

  | Tier | Examples | Behavior |
  | --- | --- | --- |
  | Instant | Add a reminder, create an event, check weather | Not a task; Persona calls phone / mail tools directly. Render a result card (with Undo) in the chat, without adding it to the dock |
  | Auto-start | Read-only: research, read code to answer a question | `task_plan` queues dispatch with `permission: bypassPermissions` for Claude, agy on its Antigravity companion, and a Paseo provider whose approval is Auto Accept (#8141), `plan` for Codex, and no mode for TRAE CLI and omp (#7559); pi, cursor and agy in a terminal have no read-only mode, so `task_plan` refuses the task (#8316). The first prompt carries the read-only constraint. The plan card collapses to "Started N tasks" |
  | Plan first | Change code, start multiple Agents, first start in this project | Show a plan card in the chat, with editable labels for each item; start only after "Start" |

- **A supervisor's own plan starts at once** (decision rights, 2026-11-23): a plan from the main conversation or a direction's lead is that supervisor's own release, so it starts without a card, read-only or writable, whether or not you asked for that very task; `user_requested` then only records where the work came from. This also applies in scheduled follow-up and check-in rounds running inside the main conversation or a direction lead's own conversation. It still waits for your Start when an item needs you: a request gate, a track whose rules confirm every hand-off (health), or an item whose `owner_step` says it spends money (`money`), speaks outward as you (`outward`) or takes an irreversible step you have not agreed to (`irreversible`). Such a card's push is counted as that kind of the six in the card ledger (`plan_money`, `plan_outward`, `plan_irreversible`). Standalone scheduled tasks and ordinary conversations follow the rules below.
- Tasks you explicitly request in the chat (a direct instruction, "have an Agent do it," "go ahead and start," #2559) also start directly. Persona sets `user_requested: true` for that `task_plan` item, and the card shows progress only. A direction's lead's own plan starts at once also after a hand-over of your quoted words was sent to it (decision rights; "Across a relay": **Start in a lead after a hand-over**).
  Inferred tasks you did not request still wait for Start. If any item in a plan needs to wait, the whole plan waits. "Start tasks I request in chat directly" under Settings › Assistant (`assistant.start_requested_tasks`, on by default) can disable this.
- The plan card offers "Start These Directly," remembered per "computer × repository (or directory) × read-only / file changes." Similar tasks no longer ask. Stored in settings `tasks.trust`; entries can be viewed and deleted in Settings.
  An entry covers its directory and every directory under it (#8095, `assistant/task_trust_scope.go`). Directories are compared as the computer
  names them: `~` is its home, extra and trailing slashes and `.` / `..` are cleaned, and the home as its links resolve (`home_real` in
  `node.info`) is the home as the node names it, so `/home/me/x` and `/data/home/me/x` are one directory when `/home/me` links there. An entry
  for `/`, a directory right under it or the computer's home covers only that directory. Entries keep the spelling they were written in, so
  ones remembered before keep matching. `persona_settings` key `tasks.trust` lists them (`get`) and adds or removes one
  (`add` / `remove`, `{"machine", "directory", "access": "edit" | "read"}`) after the owner approves its card; `add` refuses those three
  too-broad directories.
  A task in a Git repository is remembered by the repository, named by its main checkout, so every worktree of it and its main checkout are one kind (#8259): the hub asks the task's computer (`worktree.status`) about the folder the task runs in (the workspace it resolved to, which can be in another repository than the plan's `cwd`) when the plan is made and again at Start, and keeps the answer as the task's hub-owned `repo`, never what `task_plan` or a card edit says. The box is remembered once the task's workspace is admitted: the hub resolves `repo` again from the admitted folder (an existing workspace, or the checkout the engine made), saves it with that `cwd`, and remembers that repository, or that exact folder when the computer names none; never the repository the plan was made from. A folder in no repository, or one whose computer cannot say (offline, an older node), is remembered as that folder, as every entry was before. A task starts without asking when an entry covers its repository or its own folder; different repositories, different computers, and reading versus file changes stay separate kinds.
- Read-only tasks keep `permission: bypassPermissions` for Claude and an Auto Accept provider, and `permission: plan` for Codex, after card edits and on retries. Their first prompt says not to change files, commit, or write externally. Health-space plans still wait for Start. Runtime support and the current card labels are described in [To-dos](todos.md#read-only-planned-tasks).
- `task_plan` **does not block**: once the plan is handed to you, the turn ends immediately and you can continue talking about something else. When you tap Start, hub starts the Agents directly; Persona sees the result in the next message's live snapshot.

### Task workspace policy (#3659)

`task_plan.tasks[].workspace` is a task-level choice, separate from `cwd` (the source directory before dispatch):

| Request | Meaning |
| --- | --- |
| `"new"` | Prepare an isolated task folder on the selected computer |
| `{"new":{"branch":"task/fix-login"}}` | Use the supplied branch name for a single repository. Reuse its checkout when the node can verify it is available. |
| `{"existing":"/code/app"}` | Reuse an existing directory (`~/…` is expanded on the selected machine) |
| `{"existing":"/code/app","queue":true}` | Reuse an existing directory, queueing FIFO behind an occupying task instead of failing |
| `{"existing":"ws_…"}` | Select a workspace from that machine's catalog; node-local and board-qualified IDs are accepted |
| `"none"` | Create no workspace; run at `cwd` (the default for read-only/non-Git tasks) |

Settings › Assistant › Dispatch tasks offers **When dispatching tasks, prefer**:
`task_workspace_preference: "prefer-existing"` (default) reuses an available source directory;
`"prefer-new"` gives each writable Git task a new checkout. A read-only task defaults to `none`.
A writable Git task whose directory is occupied defaults to `new`, regardless of preference.
On a supported computer, the node uses the [workspace naming rules](todos.md#backend-workspace-naming).
For a linked local Git workspace it uses the source checkout's commit. An explicit `new` in a plain
directory can prepare a task folder with zero repositories, even with Git unavailable. Older nodes
keep their engine path. If that engine cannot prepare checkouts (`worktree_unsupported`), an inferred
`new` reuses a free source directory or shares an occupied one with `workspace_resolved.shared: true`.
An explicit `new` on that older node requires engine checkout support.
A Git source with no starting commit cannot produce a checkout (#5548). An inferred `new` then reuses
a free source directory and refuses an occupied one. An explicit `new` returns the missing-commit error.
An explicit `workspace` overrides inference. When a caller knows a particular checkout, it should name it with `existing`.
When an explicit `existing` workspace is occupied by another open task and the caller either sets `workspace.queue: true` or names that occupying task in `after`, the hub parks the task in a durable workspace queue (`TaskQueue` with key `workspace_occupied`) instead of failing into a start-error card (#13142). When the occupying task completes, is accepted/cancelled, or otherwise releases the workspace, the hub deterministically dequeues and auto-starts the next FIFO waiter on that `(machine, workspace)` whose `after` prerequisites are satisfied, without polling timers. Without `queue: true` or a matching `after` prerequisite, occupancy still refuses immediately naming the occupying task.
Saved repository bindings may also declare an optional `task_workflow` preference (`"isolated"` / unset by default, or `"shared_branch"`, #13204). When `workspace` is omitted on a writable task targeting a `"shared_branch"` repository, default resolution reuses the checkout with `queue: true` (cooperating with #13142 instead of splitting to `new` or failing on occupancy) and uses a `container` workspace when a to-do binds multiple repositories (cooperating with #13143); `"isolated"` or unset preserves the existing defaults unchanged, and any explicit `workspace` overrides inference.
For an explicit `new.branch`, admission first asks the node whether that branch already has a checkout in the same repository.
It reuses that checkout only after a successful clean-tree check, complete live-Agent inventory, and the hub's unfinished-task
ownership check. Dirty, occupied, changed or unverifiable checkouts are refused with their path and known owner; nothing is forced.
Older nodes omit branch lookup and keep the existing creation/refusal behavior.
On the older-node path, an unnamed `new` tries to reuse the to-do's own `task/<todo id>` branch (#10365).
The lookup must find an idle checkout inside the engine's checkouts folder with no tracked or staged
changes. A safely reused checkout keeps untracked drafts in place. A task that already has a
created or reused checkout keeps that directory on retry, including after a node upgrade.
Agents keep temporary files and drafts outside repository checkouts, for example in the operating system's temporary directory.
Since the node names the directory an Agent starts in by a linked home's own spelling (`workspace.resolve` with `terminal`,
#10365), while a claim or an Agent from before, or from an older node, has the resolved one (`home_real`), the hub compares
task directories with the two spellings folded (`taskDirKey` in `server/internal/assistant/tasks_workspace.go`): a directory
occupied under one spelling is occupied under the other. The leftover a refusal names for `{"existing": "<path>"}` gets portable
git links before the refusal, and any linked worktree `workspace.resolve` hands to a terminal gets them too, so git works
there in a terminal that sees only the home's spelling.
Inference uses the node's `worktree.status`; if the node cannot answer, the plan reports the error and asks for an explicit choice.
The same preference is available through `persona_settings` as `assistant.task_workspace_preference`.

The open web plan card has New / Use existing / Not needed, a machine-local workspace picker, manual existing path/ID,
an optional new branch, and the resolved absolute path. “Edit” also supports changing machine/cwd and recomputes the preview.
`POST /api/v1/task-plans/{id}/workspace-preview` applies editable fields to the saved plan without saving or creating anything.
Start sends those choices through the existing `/start` request; clients cannot supply trusted resolution metadata.
Task details and the started plan show the chosen policy and actual path, read-only.

The plan reserves a to-do ID for preview. Start persists the naming operation on the selected computer,
prepares the folder there, and records the actual `cwd` in the plan and in `todo_agents.cwd`. The default
base directory is `{home}/workspace` and the default task branch is `persona/{task-id}`. The engine
receives the prepared directory for launch. The same operation and computer are used on a retry.
Older nodes use their engine's checkout directory and the `task/<todo id>` default branch.
A preparation or lookup failure appears on the task before any Agent starts. A safely reused checkout
is recorded as `workspace_resolved.reused: true` with its actual path and computer.

An existing workspace on the same machine and canonical path cannot be reused while another unfinished task holds it.
The refusal names that task's ID/title and suggests waiting or choosing New (v1 has no queue).
Admission also covers concurrent starts before the agent link exists. Ended/released tasks do not retain a claim.
Read-only tasks using `none` do not claim a workspace.

The saved `PlanTask.workspace_resolved` is a hub-owned object, separate from the policy.
The examples show the default naming rules and omit unrelated fields.

`task_plan` itself still returns a textual plan/dispatch acknowledgement. The response fragments are saved tasks read from
`GET /api/v1/task-plans/{id}` or the conversation feed **after dispatch completes**, not confirmation from the initial queued reply:

```json
{"workspace":"new","cwd":"/code/app"}
{"workspace":"new","cwd":"/home/example/workspace/task-todo_example","workspace_resolved":{"machine":"computer","path":"/home/example/workspace/task-todo_example","branch":"persona/todo_example","operation_id":"op_example","backend_workspace_id":"ws_example","created":true}}

{"workspace":{"existing":"/code/app"},"cwd":"/code/app"}
{"workspace":{"existing":"/code/app"},"cwd":"/code/app","workspace_resolved":{"machine":"box","path":"/code/app"}}

{"workspace":"none","cwd":"/code/app","readonly":true}
{"workspace":"none","cwd":"/code/app","workspace_resolved":{"machine":"box","path":"/code/app"}}
```

The engine registers new workspaces in the existing App/catalog/board view, with its existing project/branch/kind metadata.
This v1 does not add multi-repository entries, change `agent_start`, or change acceptance semantics.
After acceptance, workspaces are **silently retained**: no cleanup, recycling, or migration. This is the Git checkout
and its branch. A herdr leader task's terminal workspace is separate: it closes after the top-level task and its Agents
retire, while the checkout is retained (`docs/integrations/engines.md` "Task workspaces").

### Needs you and In progress: each thing once (#3387)

Above the Assistant's composer, **Needs you** lists the user's turn and **In progress** the work that does not need
them. On the web, **Parked** is their peer for external waits, including ready review/merge waits (#9325), not a fold
inside active work. Each thing is one row in exactly one entry, Needs you first; task badges use the shared open-count
rule below. Native clients keep their existing two-entry presentation until they adopt this grouping:

- **Needs you**: a task whose current agent asks something (one row, its cards under the task's name), a task waiting
  for a decision that is the owner's (an `ask` on an open task; `needs_user` on an open task; on Android and Apple also a
  `pending_review` that is overdue, next bullet), one whose turn broke off on a model error (`interrupted`) or whose start failed while it is
  still the user's to retry; in the main chat, an Agent opened by hand (no task) that asks something; a plan waiting
  for Start (one row per plan); a message card waiting for Send; the conversation's own cards. A finished task is never
  here, whatever `ask` it still carries (a supervisor's accepting review can leave its "please verify" behind): the
  To-dos page files it with the finished ones and the tray has nothing to answer, so until #12974 such rows kept the
  badge up for good while the list the owner opened showed none of them. On the web, a task review's current owner
  decision is answerable in the supervising conversation's tray and in the main tray, where a direction's decision is
  grouped under its supervisor. A quiet task's decision can appear there while the global To-dos badge still skips it.
- **A supervisor's review is not the owner's.** A supervised task judged finished waits in `pending_review` for its
  supervisor (the background supervisor, a leader, or the conversation that handed it out; "Supervision" and
  "Background supervisor" below), who closes it with `task_review`. That review is Persona's to do, so the Needs you
  badge does not count it. It comes to the owner only when something of it is theirs: its agent asks, it has an open
  `ask` (please verify), or the review ends in `needs_user`. Unchanged by this: the To-dos page's sections, the
  direction tree's `review` state, the Today card's "Waiting for your review" and pushes. Before 2026-11-15 every
  `pending_review` task was listed under Needs you, a rule older than the background supervisor (#4927): most of what
  the owner was shown there was a review Persona finished on its own minutes later.
  - **Web: Persona reviewing** (#8678, zh 等 Persona 验收, ja Persona の確認待ち): every such task with nothing of it
    running (its status a finished turn, `awaitsReview`) is a row in an entry of its own, after In progress, however
    long the review has waited and whether or not the hub sends `pending_since`. The entry is there only while one
    waits (or its tray is open), is drawn neutral (a grey `ink-3` dot and `ink-2` label, never the attention or
    working colour) since nothing in it is the user's, and counts its rows like the others. Its rows say "Supervisor
    reviewing" (zh 督办验收中, ja 監督の確認待ち) and open the task. A task under
    review whose PR is open and waits for a maintainer is here, not in Parked (`supervisor_review_with_open_pr`). A
    review whose agent is at work again is In progress with it. Until #8678 the web put a review overdue by an hour
    (or with no `pending_since`) in Needs you, as Android and Apple still do: users opened such a row, found only the
    Agent's finished conversation and nothing to answer, and could not tell what was wanted of them. The entry keeps a
    stalled or missing supervisor's review in sight without making it the owner's.
  - **Android and Apple** (until they adopt the entry): the task is **In progress**, its row saying "Supervisor
    reviewing", for the first hour of the review, and in Needs you after that (`supervision.pending_since`; the
    threshold is `REVIEW_OVERDUE_MS` on Android, `AssistantEntries.reviewOverdueAfter` on Apple), or at once from a hub
    that does not send `pending_since`. The row stays in In progress whatever the To-dos page files the task under,
    each client re-reads the clock once a minute, and a task under review with an open PR is In progress, not Waiting
    for merge, until the review is overdue. `spec/fixtures/entries` pins this split and, in `web_review`, the rows the
    web lists under Persona reviewing instead.
- **Parked** (web only, #9325): explicit supervisor holds and ready review/merge waits share one peer group. A fresh,
  matching open non-draft PR with nothing active or awaiting the owner keeps the existing `pr_open` rule, now displayed
  inside Parked. Generic person-reply holds do not become merge waits. The To-dos status list and All board use the
  same classification. Multiple PRs keep their own links/blockers but the task has one row and count. Overdue holds stay
  here until the hub actually wakes/reviews them, never auto-shelved or silently filed as finished. The entry appears
  while it has rows or its tray is open; a hold's neutral clock replaces the Agent's finished-turn check. This slice
  keeps the existing until/reason line; contact evidence, waiting age/order and reminders are separate work in #9289.
  Android and iOS still use their earlier presentation and do not adopt this web group in this PR.
- **In progress**: every other task the To-dos page counts open (#3331), and in the main chat each working Agent opened by
  hand. A task's agents are never rows of their own: the task's row carries their state and progress meter, and opens the
  task's details, which switch between its agents' chats. Shelved tasks fold under one uncounted line. An unaccepted
  review still counts after Seen (#5732). The chip badge reads the row's `data-counts-open` flag, so counted to-dos across
  Needs you, In progress, and on the web Parked and Persona reviewing equal the To-dos header's "Not done", for the same
  scope. Live questions, owner decisions and active work outrank both types of wait; the supervisor's review keeps its
  own entry. Existing acceptance/deployed precedence, delivered-awaiting-review behavior and PR freshness stay unchanged.
- Finished tasks (done, cancelled, accepted) are in neither. The ones the conversation handed out that finished in the
  last 24 hours are under **Finished** (next bullet); the board (**All**) has every finished task. An answered card moves its task
  back to In progress. An entry with nothing counted (no number on it), Needs you or In progress, is grey and still opens:
  to the rows it lists uncounted, or to its line ("All caught up", "Nothing in progress"); the web drew such an In
  progress in its working blue and ink until 2026-11-14.
- **Finished** (#10922, every client; zh-Hans 已完成, ja 完了): the tasks the conversation handed out (in the main
  conversation every handed-out task, as for the other entries) that finished in the last 24 hours, seen or not, newest
  finished first and the same moment by id. Finished means the To-dos page's Just finished or Done section (closed,
  cancelled, accepted, deployed, or a finished turn the owner has already seen; never a failed start), and a task
  finished at the moment its triage rule reads (`finishedAt`: its close, its accepting review, its deploy, else its last
  move). The window is `JUST_DONE_MS`, the one the To-dos page keeps Just finished by, so a row nobody has opened since
  it finished is exactly that section's: it keeps Just finished's look (the unread green as a plain dot and the word
  "Just finished" / 刚做完待看 / 完了したばかり). Opening a row opens the task and marks it seen (`seen_at`, #5707),
  which changes only that mark: the row stays, as Done (Accepted, Cancelled), until its 24 hours pass. The entry comes
  after In progress (on the web after Parked and Persona reviewing), only while it has rows or its tray is open, and shows
  how many rows it has. That number is finished work, not open work: the Needs you and In progress counts and the
  To-dos page's "Not done" do not change. It is drawn quiet (secondary words; on the web and Android the done check's
  green as its mark), and its tray's header has **All** like the others. Before #10922 the finished rows were an
  uncounted Just finished group under In progress that held only the unseen ones, so opening one took it out of the tray
  and only the board still had it. The split is each client's `finished` (web `assistantEntries`, Android
  `assistantEntries`, iOS `AssistantEntries.split`), pinned by `spec/fixtures/entries` (`expected.finished`,
  `expected.finished_new`).
- **One home for an Agent several tasks list** (every client, 2026-11-14: web #7490, Android #7531, iOS / Mac #7532): an
  Agent that more than one of the conversation's tasks lists asks under one of them, the task the hub ties its session
  to (`Session.todo`), else the first that lists it (oldest card first; one plan's tasks in the plan's order, the web's
  slot id `plan:<plan>:<index>`). Its cards are that task's row alone and are
  counted once; the other tasks do not wait on the user for them and are In progress, unless a decision of their own
  keeps them in Needs you (without the Agent's cards). Before, each task listing the Agent showed and counted the same
  approval. The shared cases (`spec/fixtures/entries`) pin it; the whole-client Needs you rule is #5974's.

An open task whose delivery is held for the owner appears in **Needs you** with the reason for the hold. Only a hold whose `unblock` is `user` does: on a supervised task an owner hold no review followed comes as `supervisor` and stays with the tasks in progress as its supervisor's review until the supervisor opens a decision (#13191, `docs/assistant/agent-work.md` "Closeout"). The web row says "Finished, waiting for your OK to ship", offers **Open task**, the pull-request link when one is known, and a two-step **Mark done** that asks once more and leaves **Undo** on the toast.
A structured decision already shown for the task keeps its controls. A wait assigned to CI, a reviewer or an Agent
continues to use the normal task placement.

The split is one pure function per client: web `assistantEntries` (`web/src/lib/assistant-entries.ts`), Android
`assistantEntries` (`persona.core.domain.tasks`), iOS / iPad / Mac `AssistantEntries.split`
(`apple/Persona/Views/Assistant/Tasks/AssistantActionBar.swift`, #3393). All three replay `spec/fixtures/entries`, which pins
which entry every case goes to, which tasks Finished lists and which of those are new, and which In progress rows say their supervisor is reviewing them (Apple:
`AssistantEntriesFixtureTests`); the web reads those rows, and the overdue reviews, from `web_review` as its Persona
reviewing entry (#8678). The older Pending / Progress / Agents / To start entries
below are the earlier presentation (#2878, #3162).

- **Starting means a dispatch is in flight (Apple, #7552):** with no Agent up, an In progress to-do says Starting only when its `plan_id` names a start that has not failed, or a loaded non-dismissed plan holds it as a task with no error (including a takeover whose to-do has no `plan_id`). A plain to-do nobody dispatched says Not started with a still/offline mark and no working rank. It remains one open row, so the tray and To-dos counts are unchanged. Plan slots without a to-do keep their existing presentation.

### Background activity in In progress (#3588)

A task's row and a hand-opened Agent's row use the **existing Working spinner** whenever background commands,
Monitor, workflows or subagents are active. They do not use separate gear, workflow or subagent icons in the tray.
Their state text is Working, even when the parent turn ended to wait for that work. The subtitle names the activity:
`Background tasks running: N`, `Subagents running: N`, or `Background workflow · <phase> · <done>/<started>+`,
using the same phase and counts as the Agent's running footer. Mixed activity remains explicit; multiple workflows
show the newest run plus the number of other runs. The `+` means more agents may still start, not a fixed planned total.
Only a genuine wait with no background activity retains the waiting clock. Pending questions and decisions still win
and put the task in Needs you; finished and released bindings never contribute activity to an open task.

The existing pure entry functions carry derived `activity` alongside each running row. They sum distinct active
Agent bindings, so activity on a non-lead Agent is not lost and duplicate bindings do not inflate counts. Session list
summaries use `background_tasks_count`, `running_subagents`, `running_workflows` and `workflow_progress` (the
compact projection in `docs/protocol/protocol.md`, "Summary rows"); complete Session responses use their
existing `background`, `subagents` and `workflows` arrays. Missing fields on older hubs retain a generic activity line
rather than fabricate phase or progress. Newer summary revisions win over cached chat detail, including same-state
phase/count updates and completion; a row projection is not installed as an open chat's complete state.

Apple (iPhone, iPad, Mac; #3591) decodes `background_tasks_count` and `workflow_progress` with the existing
`WorkflowRun` model and applies the same spinner and subtitle rules (`AssistantTrayActivity` in `apple/Shared/Tasks.swift`).
A summary row's counts, including omitted zeros, win over retained chat arrays; a later complete response clears the
summary hints. `phase_count` gives the phase position without `phases`. An older hub's complete response that only sets
`background_working` shows Working with a generic line. Task rows read only their linked sessions; manual-Agent rows
carry a value-only activity snapshot in `DockAgent`, so the composer does not observe every session.
`AssistantEntriesFixtureTests` replays `expected.activity` from the shared fixtures. The opt-in mock
`MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASK_TRAY_ACTIVITY=1|summary` (`summary`: compact fields only) supplies four
rows for `AssistantTrayActivityUITests` on iPhone and Mac. No new endpoint, task storage or model call is required.

### Progress tray: one task, one row (#2878)

The approved simplified Assistant presentation places a task's **only card in the Progress tray**, not in the conversation.
Planning, running, blocked progress, PR references and acceptance update that same task row in place. Tapping the row opens
the existing task details. On iOS and Android the dispatch answer is one hand-off card per Agent a dispatch started or a task took over (#5110; iOS `DispatchHandoffCard`: title, New / Already Active / Resumed, when that Agent last moved and its state, ↗). A card with an Agent opens that Agent's page. The web draws the same cards, which open that Agent in the side panel or as its page, as Settings says ("Hand-off cards (web)" below). Historical plan/task/receipt/review/report cards use the same lightweight entry or are omitted from the chat; receipts, reviews and reports are not drawn at all since #8237 (Android: #8312; "Receipts" below). Persona's hand-offs, message cards and the hub's notice lines stay in the conversation (`docs/plans/android-ios-alignment/chats.md` C13).

**Pending** holds plans that still require Start and acceptance decisions, alongside ordinary questions/approvals. Drafts sent
on the user's behalf are a separate group and require their own explicit Send; Approve all never sends drafts or chooses an
answer to a question. **Progress** also has a separate quiet supervision/scheduled-task group, with inspectable background
records (Android's Glass style keeps a supervision round in the chat as one folded "Supervision record" line instead, as iOS does). System housekeeping notices are not conversation messages. Tools and intermediate narration stay manually
inspectable under a single collapsed summary; source metadata, not title/emoji guesses, identifies background turns.

A report's row shows a paraphrase, not the Agent's own words (#3181): those are often in another language than the user's,
in Markdown, and carry internal ids. After keeping a report as the task's progress, the hub has the triage's small model
(purpose `agent.triage`, prompt `agents/report_line.md`) write one sentence in the user's language, following the work
line's rule (at most 30 characters, 60 outside Chinese and Japanese; no subject such as "Agent"; no pane, session, batch
or `todo_…` ids, while PR and issue numbers stay), from the whole report, off the Agent's tool call, and keeps it as
`supervision.progress.line`. It is written only while the progress is still that report: a newer report wins and a late
answer is dropped. With no model, an error or an empty answer there is no line, and the row shows `text` as before.
`text` keeps the Agent's words in one line; `detail` keeps the whole report (Markdown, at most 2000 characters) when it
differs from `text`. Tapping the row opens the task's details, which show the report as written (`detail ?? text`) with
Markdown rendered (web: over the Agent's chat in the task's side panel, folding to its title line, and on the to-do's page; Android:
in the task sheet). Neither field reaches a prompt, the
supervision message, a briefing, a card or a push: the supervisor still reads the report from its signal. Report feed
items stay as they were. Mock: `MOCK_TASKS=1 MOCK_TASKS_REPORTS=1` has an English report with a Chinese line.

The shared feed API adds stable `task_cards` upserts (fields and compatibility rules in
[the protocol](../protocol/protocol.md#tasks-and-the-chat-feed)). The original event log remains unchanged. Live task/session
state still updates the row even when no new feed event is written. Android implements the conversation projection and
all three trays in #2878; web implements the unique Progress row, dispatch hint and relocated planning/acceptance actions,
retaining its existing turn-source presentation. Apple's independent client work can consume the same additive contract.
The older card-oriented descriptions below document
the legacy presentation and the retained detail actions; they do not require duplicate cards in the simplified chat.

**A stopped task says why it is still open (#3180).** When a task's Agents all stop, the hub's completion check keeps its
verdict and reason on the to-do (`check`, [To-dos](todos.md#automatic-completion)). While the task is neither working nor
done and that check is current with a verdict other than `done` / `unsure`, the web (`ProgressTaskRow`) and Android
(`trayProgress`) row reads "Stopped, not done yet · <reason>" ("停下了，还没完成 · …"), in place of the supervisor's or the
Agent's last line; an Agent's open question still comes first. A new turn or a reopen clears the check, so the row goes back
to the live text. iOS reads the same check (`TodoItem.stoppedReason`, the row's second line in `AssistantActionBar`) since #8237. Screenshots: `web/e2e/assistant-task-trays.py` (the stopped task), Android
`AssistantTraysShotsTest.aStoppedTaskSaysWhyItIsNotDoneInEveryStyle`, and the mock hub with `MOCK_TASKS=1 MOCK_TASK_CHECK=1`
(the blog move waits for its deploy).

### Task cards

- Once a plan starts, its card collapses to "Started N tasks," followed by N task cards. Task cards **update in place**: state (spinner / orange dot / green check), title,
  "computer · project · model," the current step (the Agent's latest message or a summary of its latest tool call), progress bar, and elapsed time.
- **Current step** (#1108): when an Agent keeps calling tools without writing text (reading code, testing, waiting for CI), `last_text` can remain unchanged for minutes to tens of minutes
  (2026-11-04 statistics from two days of transcripts on one machine: 6069 segments with ≥3 consecutive tool calls between text blocks, median 84 seconds, p90 about 6 minutes, p99 about 20 minutes), making the card look stuck.
  Node therefore extracts the latest tool call from the transcript and summarizes it in one line (`transcript.Activity`: "Running tests," "Reading tasks.go," "Waiting for CI," "Opening a PR," etc.),
  delivering `activity` / `activity_at` with the session list (only when the tool call is newer than the last text block). While the Agent works, the card shows that step and its duration (redrawn every 30 seconds);
  new text replaces it, and once the Agent stops, the last text block is shown. Client rules live in `TaskNow` (iOS), `taskNow` (web, `web/src/lib/todos.ts`), and `TaskNow` (Android `core:domain`).
  All three clients and the snapshot use the same rules. An Agent that has never written text and stopped on a tool call also shows that step (without a timer), not "No activity yet."
- The last message must not disappear because the transcript is too long (#1583). Node reads only the final 384 KB. After an Agent writes text and continues calling tools, tool results and hook receipts quickly exceed 384 KB,
  pushing that text out of the window and leaving `last_text` empty (observed: a 1.5 MB transcript, last text at 665 KB, card showing "No activity yet" for 7 minutes).
  Now, if the tail window contains no text, reuse the last text read (the file is append-only; newly written data since the last read is in the window). With no prior value, search backward in chunks, up to 8 MB.
- Running sub-Agents (`running` in `subagents`) appear on a separate "Sub-Agents running: A, B" line as supplementary information. The main line always shows the main Agent's own text or current step.
  Sub-Agent transcripts are separate files (`<session>/subagents/agent-*.jsonl`) and never enter the main Agent's `last_text` / `activity`.
- Snapshot tasks (`snapshotTasks`) also include the latest text, current working step and duration, and running sub-Agents: "Working, <machine> session_id=…: latest text; now running tests (3 minutes); sub-Agents running: …".
- Link latency (measured on the hub machine 2026-11-04): Claude Code writes a line in 0.1–3 seconds; node rebuilds the list every second and rereads a growing transcript at most every 3 seconds (`metaMinInterval`).
  List changes (including new `activity`) are pushed to hub immediately, hub broadcasts directly, and web replaces on receipt. There is no model-generated summary in this path: cards and snapshots use original text; completion receipts also truncate the Agent's final text block.
- Cards read sessions through `AppModel.sessionWithLine`, not `session(_:)`. To reduce redraws, the latter ignores pushes changing only `last_text` / `updated_at` / `rev`,
  which would leave the card stuck on the message from the last state change (`docs/lessons/ios.md`).
- Progress **does not insert new chat messages**. Only two cases append at the bottom: needs your answer (question card) and answered (collapsed line); how a task ended is its row, not a receipt card (#8237, "Receipts" below).
- Card footers offer "Message" (targeting this task; see "Routing" below) and "Stop" (interrupt, with confirmation first). Tapping the card opens task details.
- When the to-do has a PR URL (`pr_url`, #2768), the card adds a "PR #N" line under the current step; tapping it opens the PR in the in-app link sheet, matching "Open PR" on the Feishu task report card (#5033). Cards without a PR are unchanged.
- Task cards from previous days collapse to one line (state dot + title + result); tap to expand.

### Needs you

- When a task's Agent has a pending interaction (question, tool approval, plan approval), a question card appears at the bottom of the main chat. Its header names the task; below it is the current
  `InteractionPanel` content, answered inline. The corresponding task card gains an orange border, and its dock pill becomes "xx · Waiting for you," sorted first.
- Tapping an orange pill scrolls to the question card and briefly flashes its border.
- Multiple waiting items appear at the bottom in arrival order. Answered cards collapse to a line such as "✓ Login crash · Approved," retained as a chat record (`answered` chat-activity entry). Tap to expand the full "question → answer" (multiple questions listed separately, selectable and copyable), tap again to collapse, and tap the task name to open task details (#1231).
- Pushes remain unchanged: the title is the task name; opening the notification goes to the main chat at the question card.

### Receipts

**Written, not drawn (#8237, 2026-11-15).** A long-running task whose Agent stops to wait for its own work ends a turn
many times, and each turn's receipt was a card: the main chat filled with "<task> stopped, not done yet". Now, in every
assistant conversation (the main one, side ones and leaders, history included), a task has one card in the chat, the
hand-off or plan card from when it was handed out, and that card updates in place: its state word follows the task, a
failed start reads "Didn't start: <why>" and a turn the model service cut off "Interrupted · <why>", in the danger color.
A failed start's why is in plain words (`StartFailureWords`): a raw error naming a known cause becomes the cause ("rpc:
connection closed" → "The connection to this computer dropped"; a computer offline, a start that timed out, one the
computer refused, a Persona that needs an update), a sentence the hub already wrote for people stays, and anything else
reads "Didn't start. Open it for details."; the raw error is only in the task's details.
Receipts, reviews and reports are no longer drawn, nothing in the chat says where progress went, and no push or prompt
changed. The hub writes every receipt as before: the completion judgment, supervision signals, the "Today" card, the
interruption bookkeeping below and the trays' order still read them. How a task goes on is its row:

- **In Progress** lists each open task with its state and latest line ("Stopped, not done yet · <why>" from its completion
  check, else its Agent's last words). What finished is under **Finished** ("Needs you and In progress" above, #10922):
  the handed-out tasks that finished in the last 24 hours, seen or not, newest first, each with what it did (its
  accepting review's note, why it closed, its receipt's summary, else its Agent's last words). Opening one shows the task
  and counts as seeing it (`seen_at`, #5707): it stays, without the Just finished mark. Until #10922 these rows were a
  Just Finished group under In Progress that kept only the unseen ones.
- **Needs You** keeps a failed start's row with its reason, **Retry** and **Remove**, and an interrupted task's with its
  reason and **Continue** (`POST /api/v1/todos/{id}/retry`), which only the receipt card had; a usage limit nothing
  continues by itself gets Continue on its In Progress row. A tap on the row opens the task's details, where a receipt's
  **Look** went.
- iOS (`ChatLines.drawnFeed`, `AssistantEntries.Result.finished`, `DispatchHandoffCard.interruption`,
  `StartFailureWords`) and the web (`drawnFeed` in `lib/feed.ts`, `finished` in `lib/assistant-entries.ts`,
  `lib/start-failure.ts`, `WaitingTaskRow`) since #8237; Android in #8312. Mock: `MOCK_TASKS=1 MOCK_TASKS_FINISHED=1` (three
  tasks for Finished: two nobody opened yet, one opened already), with `MOCK_TASK_CHECK=1` (a stopped task's reason), `MOCK_TASKS_CUT=1` (an interrupted one) and `MOCK_MANAGERS=1` (one
  manager, so the MacBook's Agents are the default page's), with `MOCK_TASKS_NO_ASKS=1` for a chat no question card
  covers; `QuietReceiptsUITests` checks and shoots them.

What follows is what the hub writes and how the clients drew receipts before #8237:

- When a task finishes (an attached Agent ends a turn with no pending interaction), append a green receipt at the bottom: task name, what was done (summary of the Agent's final text block),
  number of changed files, tests, PR, and next actions below: review changes, merge, tell the person who asked, etc.
- Hub generates receipts without waiting for Persona to reply. Persona sees the event in the next message's snapshot and can continue if needed.
- When sending a receipt, hub also uses the judgment model to check whether the task's work was delivered. `done` keeps the to-do open as **Delivered · awaiting review**; a clear owner action or `needs_user` moves it to **Waiting for you** and pushes once. Neither path writes `done_at`: only explicit user completion or the current supervisor's `task_review accepted` closes it (#5732).
- Agent failed to start (#1077): if the machine connection closes (`rpc: connection closed`, temporarily offline), retry once after 3 seconds; fail if it still does not work.
  On failure, hub ends the task's to-do (`done_at` + `start_error`), records the reason in the plan card, and appends a red "Didn't Start" receipt.
  The snapshot reports "Didn't Start"; Inbox and Today cards no longer treat it as working. The App's task card turns red with "Didn't Start" and a direct Remove action; it does not enter the dock.
- Removal: any non-running task (failed to start, finished, stopped, completed, or "Starting" that hub never reported back on) can be removed, following the Agents page's Close interaction.
  Long-press a task card / pill / collapsed row for "Remove task," swipe left on the task board for Remove, or use ⋯ at the top right of task details. Removal deletes the to-do.
  Confirm first if it ever had an Agent (the Agent and transcript remain in Agents); a task that never started is removed directly.
- Web behaves the same (#1142): failed tasks are red with "Didn't Start" and Remove on the card. Right-click a task card / pill / collapsed row for "Remove task…," use Remove on the task's Needs you row (#5096), or ⋯ at the top right of the task's side panel (#5424).
  Confirmation uses browser `confirm()`. "Starting" tasks left by older hubs are also recognized as "Didn't Start" from the error recorded in the plan card.
- Retry (#1396): failed-start cards have Retry beside Remove, also available in task details. Hub retries that task exactly as specified in the plan
  (machine, directory, prompt, launcher / model, permissions, title), using the same to-do: immediately reopen it, clear the error (including red text on the plan card), and change the card in place to "Starting."
  No extra dock item is added. Repeated taps start only once (hub uses a conditional to-do update to block the second attempt, and clients send no further request until the first returns). If the target machine is offline, reject directly,
  saying "‘Computer’ is offline; retry when it reconnects," leaving the task unchanged. A new failure ends the to-do and writes the new error as usual, but adds no second receipt:
  the failure receipt follows the to-do, showing the latest error and Retry while it has not started, then collapsing to gray "Restarting" / "Started after retry" once reopened. Tap to open the task.
  Web says it only once (#4145): when the chat contains the full task card (today's task), only that card shows the cause and Retry / Remove; the plan card no longer repeats red text below it.
  The failure receipt collapses to one red line, "‘xx’ failed to start; see the task card above" (tap to open the task). Without a full card (earlier-day tasks shown only as a line), keep the cause and Retry as before.
  API: `POST /api/v1/todos/{id}/retry` under "To-dos" in `docs/protocol/protocol.md`.

### Interrupted receipts after the fact (#3834)

A turn the model service cut off gets an `interrupted` receipt (#1516). The hub, not the apps, says what became of it and
keeps one outage from filling the chat:

- **Resolution**: on every feed read the hub works out `resolution` for each interrupted receipt from its to-do: `cancelled`
  (the to-do was cancelled, or removed), `rerouted` (cancelled and dispatched again as a new task with the same title from the
  same chat, or another agent linked to it after the interruption), `accepted` (closed by review), `done` (closed otherwise),
  `resumed` (one of its agents started a turn after the interruption, or a later receipt of the same task exists), else
  `pending`. When a to-do changes or one of its agents starts a turn, the hub sends the receipts whose body changed again as
  `assistant_feed` (same id), so a card turns grey without a reload. The hub follows a chat's interrupted receipts for 48 hours.
- **Batch**: an interrupted receipt with the same `cause` (`model_error`, `rate_limited`, `auth`, `model_route`, `no_output`,
  from the error text; `stopped` for a turn Persona's Stop interrupted before it had any output, #10187) as one written in the same chat less than 10 minutes earlier joins that one's batch: it names the first
  receipt in `group`. The first receipt carries `batch` (`total`, `pending`, `resolved`, `machine` when all ran on one
  computer, and `tasks` with each task's resolution), recomputed on every read; a page read with `since` after the first
  receipt still brings it along.
- **Drawing** (`interruptLook`; no client draws a receipt in an assistant conversation since #8237, Android #8312, "Receipts" above): a receipt that names another in `group` is not drawn; a batch is one card,
  "<computer> <cause>, N tasks interrupted → handled a / pending b", opening to each task with its result, each opening the
  task; a handled single receipt is a grey line "“<title>” was interrupted · <result>" that opens the task; a pending one is
  the red card as before. Only a batch with something pending, or a pending receipt, uses the danger color. In the main chat,
  where receipts are otherwise left out (#3700), these interruption records are drawn; the pending task keeps its row in
  **Needs you**, which stays where it is answered. A receipt from a hub before #3834 has no `resolution` and is drawn as ever.

### Routing

- On the web, **Answer in chat** on a task decision also addresses its original `asked_at` round. The existing
  routing chip says the next message answers that decision and is saved on the task; cancel the chip to discuss
  without deciding. The route sends the owner's exact words, records their task decision, and ends that round
  only after delivery and persistence succeed. A stale round or failed delivery leaves the decision pending.
  Ordinary chat messages and ordinary task routing do not implicitly answer a decision.
- By default, messages go to Persona, which decides whether to answer, start a task, or forward to an existing task with `agent_send`. A follow-up that only asks about work a task has already done (how far it got, its conclusion, a PR link, why it stopped) is answered from the snapshot and `agent_read`, not sent to the agent: a send queues behind its turn or resumes a closed agent, and the user waits for that extra turn (#2124). The snapshot's line for tasks and the `agent_read` tool description say the same, so the assistant is not pointed at `agent_send` there. When the input contains text, a gray label above it says "Persona decides who handles this."
- To speak directly to a task, type `@` for a list (tasks, computers, projects, people) and select a task, or tap Message on its card. A purple routing label appears above the input,
  "To ‘xx’ ×." The App sends directly to that Agent, bypassing Persona. The chat bubble carries the same label (`routed` chat-activity entry).

### Messages to agents (#1542)

What Persona itself sends to a task's agent (`agent_send` with `task`, `agent_interrupt` with `then`, a supervision rework) is
also a `routed` entry, marked `from: "assistant"`. It is never drawn as the user's bubble: those instructions run to dozens of
lines, and in the user's own color and alignment they read as something the user said.

- **On the task's card**: a row "N instructions sent" lists every message Persona sent to that task (time and first line);
  tapping one opens its whole text with Copy (the web's text is also selectable). Tapping these rows does not open the task.
- **The hand-off sent with the card** (Persona's message to the same task in the same conversation at most 60 seconds after
  the card's `task` entry; `agent_send` with `task` writes the card and then the text) is drawn only on the card, not as a line.
  Only a card drawn in full (from today) takes it in: a task card from another day is a one-line row with no list, so its
  hand-off stays a folded line under it (#1668).
- **Any other message of Persona's** (a rework, a later instruction, one whose task has no card in this chat) is one folded
  line on the left, styled like the answered and review lines: a send icon (a stop when Persona cut the turn short first),
  "Sent to ‘xx’" (tap to open the task) and the first line; tapping the line opens the whole text with Copy. Persona
  writes these in Markdown: the web's folded line is the first line with words, without its marks (`**`, `` ` ``, list
  and heading marks), and the opened text is drawn as Markdown (bold, code, lists), never with raw marks showing, and
  line by line as Persona's replies are: a single line break stays one (web usability audit 2026-11-14, item 7); Copy
  copies the text as written.
- What the user sends to a task (its card's Message, or the composer's routing label) has no `from` and keeps the bubble.
- Older entries: migration `0441_feed_routed_from.sql` marks those Persona wrote before the field existed (see "Chat activity").
  Older apps ignore `from` and keep drawing the bubble.
- Apple: `AgentSentLine` (`apple/Persona/Views/Assistant/Tasks/SentViews.swift`; `TaskSentList` was removed in #5569; iOS no longer applies `takenIn`, so a task's hand-off is drawn in the chat like any other message of Persona's), the pure rules in
  `SentToAgents` (`apple/Shared/Tasks.swift`); web: `web/src/views/assistant/Sent.tsx`, `takenIn` / `sentTo` in `web/src/lib/feed.ts`;
  Android (#1623): `SentLine`, `TaskSentList` in `android/feature/chat/.../AssistantTasks.kt`, the pure rules in `SentToAgents`
  (`android/core/domain/.../tasks/SentToAgents.kt`), the chat page filtering the feed with `SentToAgents.shown`; checked by
  `SentToAgentsTest` and `SentToAgentsShotsTest` (every style, light and dark, folded and opened).
  Mock: `MOCK_TASKS=1 MOCK_TASKS_SENT=1 MOCK_TASKS_NO_ASKS=1` (a hand-off after its card, a rework to an older task, the
  user's own word); UI test `SentToAgentTests`.

### Task details

- Tap a task card, dock pill, or Review changes in a receipt to open task details from the bottom, like link cards (`LinkSheet`): initially full-screen;
  pull down to 0.6 so it no longer blocks the main chat (scrolling and typing remain available), then pull farther to close.
  At regular width (iPad, unfolded iPhone Duo), do not float a sheet: the system sheet is a small centered form there. Push task details onto the chat's stack, filling the right detail column,
  like opening an Agent from the Agents left column. ‹ at the top left returns to the chat; switching chats also dismisses the details (#2816, `detailPresentation`).
- On phones, floated details offer "Full Screen" beside ✕ at the top left (#3743). Dismiss the details and push the Agent page onto the assistant page's stack, using the same page as opening it from the Agents tab.
  Back (‹ or a right swipe from the left edge) returns to the main chat without reopening the sheet. Use a push, not `fullScreenCover`: Agents already uses a push, giving the same back gesture
  and stack-based top-bar / shell hiding (`atRoot`). A full-screen cover requires a custom close control, its downward dismissal conflicts with chat scrolling, and presenting it over a sheet returns to that sheet on dismissal.
- The page is this Agent's chat (`ChatView(style: .dev)`, with everything available in Agents). The top has only one layer (#3255; the user found the previous
  "task header + session header + same-space tab bar" too complex). The embedded chat header is the sheet header (`HostedChat`): ✕ on the left (‹ when pushed into the detail column),
  task name in the middle, with "state dot · state · computer · project" below; goal and loop / background tasks are small icons beside the title; ⋯ on the right.
  The top of ⋯ belongs to the task: if session title and task name differ, show the session title as a section heading, then "Remove Task…" and "Open in Agents," followed by all the usual chat-page ⋯ actions.
- Opening from the main chat shows only this Agent: no tab bar for other Agents in the space and no switching here. To see others in the same space, use "Open in Agents"
  (dismiss the sheet, switch to Agents, and open this Agent with the normal tab bar).
- If a task has multiple Agents, add ⌄ beside the title. Tapping the title lists the task's Agents for switching (plus "Session Info"; web does the same, #3333).
- Without an Agent (starting or failed to start), there is no chat page; the sheet uses its own header: state, task name, ⋯, Close.
- On the web, that header and the empty panel say Starting only for a pending dispatch, using the same rule as the task tray.
  Without one, the panel explains that no Agent is currently working on the task; a start failure keeps its reason and retry controls.
- **What a dispatch opens (#3700, #3711, contract in #3718, card since #5569, one card per Agent #5110)**: each Agent a dispatch started or a task took over is one hand-off card
  (iOS `DispatchHandoffCard`, in a `DispatchHandoffCards` list; no "Dispatched N" heading). These cards name only that dispatch's Agents,
  never the whole In Progress tray. The ids are already on the client: a standalone `task` feed item's `ref` is the
  to-do and its `wake` (`created`, `active`, `resumed`) is the badge; a started plan's `tasks[].todo_id` identify its tasks
  (a plan's tasks are New). Live to-do titles override plan titles. A plan task without a to-do keeps its title as a card
  with no link, red "Didn't start: <reason>" from the plan task's `error`, and its per-task feed items do not add
  duplicate cards. Agents linked within 120 s of the first are each a card; a later Agent is not on this hand-off. The web
  anchors "the first" at the card's feed line (#11854, below); iOS and Android still take the to-do's earliest Agents.
  The title is the session's, then the Agent row's, then the task's. The second line is that Agent's `activity_at` (else `updated_at`) as a relative time and
  its state; before there is an Agent it says "Starting the agent", a task closed without one "Closed
  before an Agent started", a task the list no longer has "This task no longer exists".
  Apple renders this with `DispatchHandoffCards` and `DispatchedTasks`: on iPhone and iPad that Agent is pushed using
  `TasksStore.expand`, and Back returns to the same Assistant conversation without switching to Agents. In the Sidebar
  layout too it is pushed on the page's path, not made a root: the sidebar's choice stays on the conversation (#9971). On Mac the
  task's own detail hosts the Agent chat, carrying that `SessionKey` into its initial selection (validated
  against that task's active sessions); while no Agent is available, the detail shows its starting/failure state.
  The card is one accessibility element and button (`dispatch.task.<todo id>`); the badge never wraps.
  Open plans keep Start / Edit / Don't Start. No new hub field or setting is needed. A dispatch has no full task
  card instruction list, so Persona's routed hand-offs remain separate folded feed lines instead of being hidden.
  Tests: `DispatchedTasksTests`, `TaskCardsFusionTests`, `TaskSheetExpandTests`, `DispatchedTaskCardTests`; synthetic hub:
  `apple/PersonaUITests/Fixtures/dispatch_hub.py` (`PERSONA_MOCK_HUB`, screenshots with `PERSONA_SHOTS`).
- On the web, where task details open is the browser's setting (#5424, "Where everything opens" below): the side panel
  (`TaskPanel`, the task's Agent's chat under the task's title, its report and the question it waits on; the default), or
  the task's Agent's page.

### Hand-off cards (web, #5096)

The owner asked that this part of the web chat follow cindy exactly (its `SessionHandoffCard`, 2026-11-12). The cards
are drawn the same whichever way they open; the owner then asked for the side panel back as the default, with cindy's
page one setting away (#5424):

- **One card per Agent.** Each Agent a dispatch started or a task took over is one full-width card where the dispatch is in the chat, stacked by plan order, with no heading over them. This covers a started plan, a standalone `task` item, and a message that started a new Agent for its task (`routed` with `new_agent`).
  - A task's line shows the Agents the hand-off went to: the first Agents linked from its feed line's time on (for a standalone `task` line an Agent linked up to 30 s before still counts, since that line is written just after its link; a plan line has no look-behind, since its Agents start after it, so a previous continuation's Agent linked just before never stands in for the plan's own), within two minutes of each other. That covers the plan's several Agents and a retry. An Agent linked later has its own card where that happened (#11854, `handoffAgents` in `web/src/views/assistant/Handoff.tsx`).
  - After a plan continues a to-do (task_plan's `continues`), the card under that plan is the Agent it started; until that Agent is linked the card says it is starting, never an older Agent. Cards under earlier plans keep the Agents those plans started: a plan whose `todo_id` the continuation cleared finds its to-do again through its `continues` / `reuse_todo`, or as the one to-do whose `plan_id` is that plan.
  - A line with nothing linked since (a task that took over an Agent already running, an earlier plan with no Agent of its own) or with no usable time shows the newest Agents still linked, else the one released last.
  - **Head:** a round ↳ mark, the Agent session's title (the task's until it has one) and a badge: **New**, **Already active** when the task took over an Agent that was running already, or **Resumed** when the hand-off's message resumed a closed one.
  - **Second line:** "last active …" and the Agent's state word. The state shows that an Agent needs you without opening anything.
  - **End:** ↗.
  - cindy also shows a short session id. The Assistant's chat keeps ids out of sight (super rule 4), so here the id is only in the card's tooltip.
- **The badge's data.** A `task` feed item made of an Agent the task did not start carries `body.wake`. It is `"resumed"` when agent_send with a task resumed a closed Agent. It is `"active"` for agent_supervise, the other agent_send cases, an Agent that became a task when its turn ended, and a task moved to another conversation (`taskCard` in `server/internal/assistant/agent_control.go`). Items without it are New, as is every item from before the field. A stopped, read-only Agent (#4754) is never resumed this way: a task whose Agents all stopped gets a new one, which is New.
- **Before an Agent is up.** The card stands for the task. Its second line says the Agent is starting, why it didn't start (red), that the task closed before an Agent started, or that the task no longer exists.
  - While the to-do exists, the card opens the task (its side panel, or the to-do's page).
  - A task that didn't start has **Retry** and **Remove** on its Needs you row, where it waits on the user.
- **Where everything opens** (#5424). Settings › General › Agent preferences › **How Agents open** picks it, for this
  browser only (`web/src/lib/agent-open.ts`, localStorage `agent.open`; nothing is written for the default). Every entry
  goes through `openTask` / `openAgent` in `web/src/lib/handoff.ts`: the hand-off cards, the dock's pills, Needs you and
  In progress rows, task lines, the board, decisions, the Today card, injected task lines, the workspace board and links.
  - **Side panel** (the default): a task opens `TaskPanel` (its Agent's chat, ⌄ to switch between several Agents, ⋯ with
    Open in new window and Remove task…; the task's starting or failed state while no Agent is up), an Agent `AgentPanel`.
    The chat stays beside it on a wide window; a phone-sized one has the panel over the chat, and Back closes it. Both are
    a lazy chunk (`views/SidePanel.tsx`). Leaving a Persona chat keeps its panel for when you come back (`lib/panel-memory.ts`).
    The header's chat bubble before × (**Open Agent page**, `PanelChrome.page`) opens the Agent the panel shows (the one ⌄
    picked) as its own page, the page the Agents list opens, and closes the panel; a task with no Agent up has no bubble.
    That page has the way back below, and returning opens the panel again.
  - **Full page**: a task opens its most pressing Agent's page (`mostUrgentAgent`), or the to-do's page while no Agent is
    on it; an Agent that belongs to no task opens its own page, with the way back below.
  - `/s/…` and `/todos?id=…` links in a reply open in the side panel on a wide window while it is chosen, else as those
    pages (a phone-sized window has no room beside the chat).
- **The way back** (Full page, and a side panel's Open Agent page). The Agent's page shows cindy's "Came from <conversation> · click to return" strip above the chat; ✕ puts it away. Clicking it, or Back, returns to the conversation where its chat was left: the conversation's history entry keeps the first line in view and its offset (`web/src/lib/handoff.ts`), and the chat opens there once instead of at its end. Left from a task's or an Agent's side panel, the entry also keeps that panel (`sidePanel`), which Back opens again. An Agent's page opened any other way (the Agents list, a link) has no strip.
- Code: `web/src/views/assistant/Handoff.tsx`, `web/src/lib/handoff.ts`, `web/src/lib/agent-open.ts`, `web/src/views/assistant/TaskPanel.tsx`, `web/src/views/chat/HandoffBack.tsx`. Tests: `Handoff.test.tsx`, `lib/handoff.test.ts`, `TaskPanel.test.tsx`, `AgentOpenSetting.test.tsx`, `HandoffBack.test.tsx`, `AssistantActionBar.test.tsx`; browser: `web/e2e/handoff-cards.py` (both settings). iOS and Android draw the same cards in the chat (#5110).

### Task board

- Tap All at the far right of the dock. Group by **Waiting for you / Working / Completed today / Scheduled**. Each row has a state dot, title, and "computer · project · latest message."
- This is the to-do list ("tasks are to-dos"); future to-do entry points also lead here.

### Chat structure

- Group by day, with a date line above each day's first entry.
- The morning briefing (scheduled task "Morning briefing") posts to the main chat as a Today card: tasks completed overnight, calendar events, messages needing replies, reminders, with an action per row.
- For Inbox messages related to active tasks, Persona drafts replies using task progress and shows a message card in the chat (quoted original, linked task, draft, Send).

### Quiet tasks (#1731)

Settings › Assistant › "Notify main-chat tasks only in the main chat" (`main_tasks_quiet` in settings `assistant`, `assistant.main_tasks_quiet` in `persona_settings`, **on by default**).
When enabled, tasks dispatched from the main chat (`task_plan`; also `agent_start`, below) are **quiet**: to-do `quiet: true`, set at creation and stored in `todos.quiet`. Later setting changes do not affect existing tasks.

- **No interruptions**: hub sends no task pushes for their Agents (completion, needs you, errors), no `notify` events, and no Lark bot / channel cards. They do not enter Live Activities / Dynamic Island (`liveActivities.snapshot` skips them).
  Session lists mark their Agents `quiet: true` (retained even after task completion). Clients exclude them from global badges: web sidebar Overview / To-dos, Android Agents tab and To-dos drawer, and Android notifications (also suppressing ones from older hubs).
  Agents page, main-chat task cards, dock, question cards, and live snapshots remain normal.
- **The main chat speaks instead**: on completion / interruption (`taskTurnEnded`), failed start (`startPlanTask`), or waiting for the user (hub emits `interaction.raised`), the assistant injects a notice into the main chat
  (`prompts/assistant/task_notice.md`: "🔔 任务「xx」做完了（电脑）"—task "xx" finished (computer)—plus the Agent's last text or question, and an explanation that this is a task notice and the assistant decides how to tell the user).
  The main chat replies accordingly, with its reply pushed to the phone normally. This notice is not rendered as a user bubble: hub adds `injected` when delivering it; clients show only a line such as "Task ‘xx’ finished" (`docs/protocol/protocol.md`, "Messages injected by Persona").
- **Coalescing**: wait 15 seconds after a task's first change (`quietNoticeDelay`), retaining only the latest change in that interval and noting "It also waited for the user during this period."
  If the user has answered before a waiting notice is sent, skip it. While the main chat is running, queue the notice between chat turns (the same queue as `conversation_send`, showing "Notice for task ‘xx’" above the input), then inject it after the current turn ends.
  If the queue already has a notice for this task, replace it in place instead of adding another. Implementation: `server/internal/assistant/quiet.go`.
- **`agent_start`**: when called from the main chat with the setting enabled, hub creates a quiet to-do (`origin` = main chat, no plan), attaches the Agent, inserts a task card, and thereafter treats it like a `task_plan` task. The link keeps the Agent kind from the start answer or the adopted session after a lost answer. If missing, it uses the resolved launcher kind, or stays empty when neither is known.
  With the setting off or calls from other chats, it remains a standalone Agent as before, unless Agent tasks are on ("Agent tasks" below).
- **Reporting chats** (#3192): tasks (`task_plan`, `agent_start`) dispatched from chats created with `report_tasks: true` (the extension's Lark /agent) ignore this setting and are always quiet.
  Notices go into **the chat that dispatched them**, not the main chat (the `Reporting` wording follows the instructions at that chat's start). Registered in `assistant/reporters.go` (`docs/extensions/lark-agent.md`).

### Supervision (#3124)

Routine reminders skip tasks awaiting the owner's acceptance, parked tasks, and tasks already waiting for the owner.
They also skip a task for two minutes after verified owner input to one of its linked Agent sessions. An interruption
while the owner already has the next action does not need its own turn. A recent owner message to that same Agent skips
the generic owner-return reminder while it stays recent. A card that needs reissuing still follows the direct path.
Completed work for review, explicit Agent reports, questions, permission cards, decisions, launch failures, escalations,
and an open task's PR conflicts keep their direct routes under the automatic supervision mode.

In **Settings › Assistant profile › Agent follow-up**, **Review routine follow-ups in briefings** is off by default because model-based
deferral and its background model calls are opt-in. When enabled, Persona asks the configured **Sorting** model
(`inbox.triage`) about eligible routine reminders and PR side signals in batches. It uses the verdict immediately.
An inconclusive, unavailable, or invalid result goes to the briefing. The model can choose only whether to wake the
supervisor or save the signal for review. The manager records each verdict with the signal, destination, reason, and
time in Postgres (`supervision_triage`). A delivery can retry without changing a recorded verdict.
The settings tool uses the key `assistant.supervision_triage` with the Boolean value `true` or `false` and asks the
owner to approve a change. The manager logs how many routine signals the deterministic rules suppress in a batch.

The morning and evening briefings with facts enabled list saved follow-ups from the last 24 hours and all older
follow-ups that have yet to appear in a briefing. The saved verdicts remain available after being listed. Turning off
triage stops asking the model about new signals and leaves the saved follow-ups in the briefings.

Queued supervision digests retain their structured signals in the manager's relay setting (#10424). At drain time,
the manager loads the task ledger, drops signals that no longer hold, and renders the remaining entries using the
same rules as a new digest. An entirely obsolete digest leaves the queue without starting a conversation turn;
a mixed digest contains only actionable entries. Changed conversation supervisors receive their entries through
the existing relay path, with the source removal and destination queue saved together. Ledger or checkpoint failure
keeps the original work for recovery. Refreshing a queued payload preserves its persisted once-only delivery ID, including after restart.
An uncertain send whose text or destination would change stays queued rather
than acquiring a second delivery identity. Reassignment to an Agent supervisor is retained for recovery rather than
sent through an assistant conversation. Older text-only queued records keep the legacy behavior: task facts are not
inferred from their prose. A later structured replacement can be rechecked after restart.

The planning direction lead or live side conversation **supervises its own dispatched tasks** (`task_plan`, `agent_start`, and `agent_send` carrying a new `task` on an Agent it started or supervises), falling back to main only when the side conversation is gone, archived, or not an assistant conversation (#13362, #13657). The dispatch chat remains the task's `origin`; signals, cards, startup remedies and `task_review` follow the current supervisor. Explicit `agent_transfer` assignments survive restarts (#13344). Main chat keeps a read-only overview of side-supervised tasks with each task's origin conversation (id, title and link) and receives explicit owner-decision escalations, without taking over `task_review`: exactly one conversation can review a task at a time, including during a hand-over. When a side conversation is archived or gone, its open tasks move once to the main chat's follow-up with a notice explaining why (`server/internal/assistant/side_handover.go`), preserving the original conversation attribution on `SuperviseEntry`. The supervisor owns completion and quality.

- **Supervised by default**: all three dispatch paths create or reuse one to-do and its ledger. An ordinary follow-up without a new `task` creates no second card; a session opened manually on the Agents page is not adopted. Two existing topologies remain: a manually opened Agent explicitly assigned to a leader keeps that leader, and a task leader supervises its own workers. Unmarked legacy side-chat work is migrated to main without overriding current plan/transfer assignments, with queued notices preserved; a direction lead's own unmarked work from before assignments recorded their source (a task of a plan that assigned the lead, or an Agent on a task the lead opened) is marked as that lead's plan assignment instead, so an upgrade does not hand it to main (#13619); no new background-supervisor stream is created. `start_paths_test.go` lists every function that starts an Agent or opens a conversation and requires each to make a ledger or be on its opt-out list with a reason, so a new entry point cannot skip supervision silently.

- **Ledger**: each `task_plan` task may include `done_when` (up to 6 acceptance criteria) and `expect_minutes` (estimated minutes). Criteria are appended to the assignment prompt, asking the Agent to provide evidence for each when finished.
  A task may also give `todo_id` (#5279): the to-do it runs on instead of a new one. Only one this chat opened or supervises, that is not done and has no live Agent on it is taken (a stopped, archived or gone Agent is let go when the task starts, #6427) (a Lark topic's to-do, `docs/assistant/todos.md` "Opening a to-do from a topic"); the check is made when the plan is made and again when it starts, and a task that fails it gets the reason as its `error` and starts nothing. The to-do keeps its title, notes and task card; the plan adds no second card, only the task's `done_when` / `expect_minutes` on its supervision.
  A task with `continues` (#6427) whose earlier to-do is still open and has no live Agent runs on that to-do as if it gave it as `todo_id`: a handover after an Agent stopped carries on in the same to-do instead of opening another. A closed earlier to-do is reopened with exactly the same original to-do id before the replacement starts. If its Agent may still run (its computer unreachable) or this chat may not take it, the continuation is refused without starting anything; it never silently creates a successor. A `continues` of a to-do that another open to-do already carries on (directly or down its chain) is refused when planned and again on Start (a card left waiting), naming that to-do and pointing at `todo_id`; the duplicate hint also looks at continuations, leaving out only the to-do they continue. Only explicit `agent_supervise` adoption creates the linked new-card lineage described below.
  The to-do records its current supervisor and assignment source (separately from its dispatch `origin`), criteria, estimated duration, and review state in `todo_supervision` (one row per supervised to-do, deleted with it). To-do JSON adds `supervision` (`docs/protocol/protocol.md`).
  Review states: in progress → `pending_review` → `accepted` (to-do done, `done_by = review`) / `rework` (`round` tracks iterations) / `needs_user` (awaiting user decision) / `parked` (waits on purpose until a time, below) / `cancelled` (to-do done). An unattended stalled terminal task instead goes through `timeout_closing` to `timed_out` and stays open for explicit continuation (below).
  A task that takes a to-do over with a new Agent (`todo_id` or `continues`) starts a new round (#11553). An earlier `accepted` or `cancelled` review stays in `reviews`, while `state` goes back to in progress and `round` to 0. No client or hub rule then reads the earlier round's acceptance as the current one (`docs/assistant/todo-status-sop.md`, "Accepted tasks").
- **Delivery, never automatic closure**: when the hub judges a to-do `done`, it leaves `done_at` empty and marks it **Delivered · awaiting review** (`pending_review`), with its conclusion and result link still visible in the open / attention surface. Only explicit user completion or an authorized supervisor's `task_review accepted` closes it. A final reply requiring owner action, or a `needs_user` verdict, records an owner wait and goes to the task's supervisor, not the owner (decision rights, `noteOwnerWait`): no push, no Needs you, and the work cannot be accepted while the wait holds. The supervisor passes it on with `task_review needs_user` only when it is the owner's (goals, what only they know, speaking as them, spending, an irreversible step not agreed, an action only they can do); otherwise it decides and tells the Agent, or withdraws it (`withdraw_needs_user`). Other unfinished verdicts stay open for follow-up.
- **A stop that waits on the owner is not accepted** (#1277): when the judge says a stretch of work stopped on a decision only the owner can make (`needs_user`, such as drafts waiting for the owner to pick one), the ledger keeps it as `waits` (`{work, at, reason}`, persisted with the ledger, `todo_supervision.waits`) and sets its state to `needs_user`. While it holds, `task_review accepted` is refused (`user_check` included), because closing the task would lose the decision. The refusal tells the supervisor to review it `needs_user` as a decision the owner can answer, to send the owner's answer to the Agent if they already gave it, or to cancel it. The Agent working again, or any review written, clears it. The live snapshot labels such a task "等你处理" ("waiting for you"), and the hub sends one deduplicated push with its reason and task link.
  Completion is about the assigned task and its `done_when`, not whichever side question the Agent answered last. A known peer, cross-conversation or inspection turn cannot enter pending review on its answer alone. Only a fresh, successful `report_to_supervisor` done call with evidence tied to this task can establish the completion candidate from such a turn; the supervisor must still verify it. Unknown-source fallbacks never imply that a new question replaced a finished task. Ordinary delivery turns continue through the existing judge without requiring that tool.
  Ordinary `agent_send` from a conversation other than the task's supervisor carries a `side-conversation` context marker; the supervisor's instructions and explicit task handoffs retain their existing behavior. A text of only `/exit` / `/quit` (no attachments) from such a conversation, as `agent_send` or as `agent_interrupt`'s `then`, is refused before anything is stopped, sent or confirmed on the phone, and again after the phone answers should the agent have become such a task meanwhile (#3537): the node closes an agent only for the bare command, and the agent a task runs on is not another conversation's to close (the user can close it in the app, or with `agent_archive` and `close` once it is idle).
- **Supervisor loop** (`server/internal/assistant/supervise_signals.go`, no model calls): every 30 seconds, inspect supervised tasks plus task events and derive signals:
  `review` pending review, `idle` stopped unfinished, `needs_you` waiting for an answer over 3 minutes (immediately for quiet tasks, since the user received no push), `stalled` working without progress (no session update) for more than max(20 minutes, estimate × 1.5), or in any other state (waiting on background work, a wake, stopped) with nothing new in its transcript for more than max(2 hours, estimate × 1.5), so a wait nothing ends is still heard of (#5124), `no_progress` working with its session moving but no real progress, only waits, polls or the same command again (below), `timed_out` after a stalled terminal Agent was stopped (below), `interrupted` by model-service error, `failed` to start, `orphaned` when nobody works on it any more (below), `inbox` new Inbox messages from other people about it (below, off by default), `check` news from a recurring schedule linked to it, the hub's and never the Agent's (`report`: `no_progress`, `error`, or `decided` for the owner's option on the decision it raised; `docs/assistant/assistant.md`, "Scheduled tasks").
  **`no_progress`** (#10089, `server/internal/assistant/supervise_progress.go`): `stalled` reads only whether the session moved, and an Agent looping on `sleep` plus a status check writes to its transcript every minute. The hub keeps, in memory, a stretch per Agent since its last real progress. Progress is a `report_to_supervisor` with evidence or a later step, a `done_when` criterion whose state or evidence changed, the Agent's own plan with more steps done or another current step, a tool call that changes something (editing or writing a file, a commit, a push, a PR opened or merged, a release, a deploy), and a message from the user or the supervisor (`agent_send`); transcript writes, reports without evidence, and reading, searching, building or testing are not. Each tool call it sees (a new `activity_at`, from the session lists and the supervision pass) counts once, and is a wait when its activity is waiting (`sleep` / `until` / `while` / `wait`, ScheduleWakeup, Monitor, background output), checking CI, or the same activity line as an earlier call of the stretch. A working Agent is told of once the stretch is max(30 minutes, estimate × 1.5) old, the hub saw at least 4 calls in it, at least four in five were waits, and the latest started within max(20 minutes, estimate × 1.5) (one silent longer is `stalled`'s). One signal per stretch (key: session and stretch start), dropped before delivery if progress came meanwhile, never alongside `stalled` for the same Agent, and none for a parked task or one with the owner or waiting for review. After a manager restart a stretch starts over, so the signal can come later than the threshold, never sooner; a short call between two observations can be missed. The signal itself does not stop the Agent.
  **Automatic closure of stalled terminal Agents** (#4784, #15598, `server/internal/assistant/tasks_stalled.go`): when `stalled` or `no_progress` remains unattended until `max(60 minutes, expect_minutes × 3)` after the later supervisor action or real progress, the manager stops the Agent. Live background work (running workflows, running subagents, unexpired monitors, and active non-waiting/non-stalled background tasks) keeps an Agent out of `stalled`, `no_progress`, and timeout closure, and observable background workflow/subagent progress (phase, phase index, started/done/failed counts, calls, subagent steps, and timestamps) counts as real progress. When a supervisor handles a `stalled` or `no_progress` wake-up (including judging it a normal wait via `supervision_finish` silent/user or promoting the turn via `notify_user`/`ask_user`/`task_review`), that stall stretch is durably marked handled (`assistant.supervision_stalls_handled`) and does not proceed to `timed_out`, including across restarts; a later new stall stretch is supervised normally. A silent stall uses the last session activity (including background workflow/subagent timestamps); a spinning one uses its no-progress stretch, so sleeping and polling do not postpone closure forever. The manager saves `timeout_closing` before calling `session.stop` with `stop_cause: "timeout"`, then records `timed_out` and sends one supervision notice. Because timeout stops carry `stop_cause: "timeout"` rather than `user`, the assistant can deliberately resume the stopped session with `agent_send` (`resume: true`, clearing `timed_out`), while automatic event routing still defers to the supervisor. A failed call or final write keeps that intent for recovery after the session is observed stopped; it does not claim a successful timeout. The to-do stays open with its Agent links, transcript and workspace, and automatic completion, CI wakes and orphan reminders stay held until a new turn. `POST /api/v1/todos/{id}/retry` continues with `session.start` and `resume_session_id`, the user's Resume path, not a replacement Agent; no archive or workspace deletion is requested. Only Persona-started terminal panes are eligible. Tasks waiting for review, the owner or a park, assistant conversations, non-Persona sessions and other tools are excluded. Runtime-owned sessions are never stopped by this mechanism and keep their existing `stalled` / `no_progress` reminders.
  These follow the task's current Agents (#6483, `supersededAgents` in `server/internal/assistant/task_completion.go`): an unreleased link whose session is offline or no longer listed, while a link made after it is listed and not offline, is an Agent the task was handed over from. It is left out of the stop receipt and its judgment (the task settles once the current Agent stops, so `idle` reaches the supervisor) and out of `needs_you` / `stalled`. A lone offline Agent with no later one is still the task's Agent.
  The same signal for the same task is reported only once within the same state segment. A second guard suppresses an unchanged summary for the same task, Agent and signal kind, regardless of new turn timestamps or elapsed-minute counters: for 30 minutes for signals that wait on a decision, and **until the task closes** for the routine `idle`, `unjudged`, `stalled`, `no_progress`, `review`, `report` (except question and blocked reports), `orphaned`, `interrupted` and `overdue` (#11614; before, the same unchanged state woke the supervisor again every 30 minutes). Their fingerprint is the Agent's last words, its cards, its report and step, so a new turn with other words, a new card or a new report is still told at once. Its hashed fingerprints and timestamps live in Postgres settings (`assistant.supervision_dedup`), survive manager restarts, and are reserved before dispatch; failed dispatch and queue admission release the reservation. An unreadable or unwritable reservation is retried rather than treated as empty history. Changed evidence, questions, summary, task, Agent or status can wake again. Signals from multiple tasks are combined into **one "📋 督办" (supervision) message** per current supervisor (`prompts/assistant/supervise.md`: task, signal, a fixed pointer to the criteria on the to-do (`todo_get`), question, Agent's last text, evidence and links; no handling instructions).
  The first waits 15 seconds to coalesce; routine messages then wait out the per-recipient cooldown below. Decision-bearing signals bypass both that cooldown and the shared message spacing, retaining only the 15-second gather. Explicit deferred failed-start batch deadlines still apply. If the receiving supervisor is running, queue between turns ("Supervision message" above the input); replace any queued, undelivered supervision message with the merged one. A supervision message also waits while the owner is talking in the conversation, a decision-waiting one at most 5 minutes, and the queue goes out by rank (`docs/assistant/assistant.md` "Messages from the system wait for the owner").
  **Routine cooldown per recipient** (#11614, `superviseRoutineCooldown`): after a supervision message went into a conversation (or queued for it), routine signals for that conversation wait until 10 minutes after it and then go out together as one message. Every wake re-reads the supervisor's whole context, and before this several tasks' `idle`, `report` and `all_stopped` signals woke it one after another, 2 minutes apart. The signals listed under batching below, question/blocked reports, and a due `parked_due` never wait for it and take the held routine ones with them. The cooldown is in memory: after a manager restart the first message goes out without it. Tests that shorten the spacing get a cooldown five times the shortest spacing.
  **Optional per-recipient routine batching**: `assistant.supervision_batch_minutes` in `persona_settings` (also `supervision_batch_minutes` in `GET/PUT /api/v1/assistant`) is a whole number from 0 to 1440 minutes, default **0**, which preserves the cadence above. Above zero, the shared 10/2-minute spacing is replaced by an independent batch for each final recipient conversation, anchored at that recipient's first routine signal; the ordinary 15-second gather still applies. `needs_you`, `ask`, `escalated`, `decide`, `decision_reply`, `park_decided`, `owner_cannot_answer`, `recheck`, `undecidable`, `failed`, `parked_due` and question/blocked reports do not wait for a batch, and any one of them releases that recipient's accumulated routine signals in the same digest. Other recipients keep their own deadlines. A due batch enters the existing persistent relay queue when its recipient is busy, merging with an undelivered supervision message rather than creating another wake path. Setting zero releases waiting signals through the legacy scheduler; changing one nonzero value to another keeps existing batch anchors and applies the new interval to later batches. An administrator can instead pin `assistant.supervision_batch_minutes` in the assistant manager's `manager.json`: a present value, including explicit `0`, takes precedence over the saved setting, while an omitted value leaves it unchanged. The pin follows the same manager.json reread on hello / `manager.updated`; removing it restores the saved value. Out-of-range file values are configuration errors, so a running manager keeps its last readable configuration.
  **Automatic supervision messages** (`auto_supervision_mode` in `GET/PUT /api/v1/assistant`, `persona_settings` key `assistant.auto_supervision_mode` (`get`, and `set` approved on the phone), one three-option picker in Settings › Assistant › Agent follow-up on the web; `server/internal/assistant/supervise_switch.go`, #16417): a per-assistant mode, saved with the assistant's settings so it survives a restart and applies at once. **all** (the default) sends every supervision message. **outcomes** sends only the one-shot wake-ups a supervisor has to act on and drops the routine checks made while a task runs (`supervisionSignalAllowed`): it sends `review`, `handoff_result`, `parent_closeout`, `pr_merged`, `idle`, `unjudged`, `failed`, `interrupted`, `timed_out`, `all_stopped`, `orphaned`, `recovered`, `needs_you`, `ask`, `decide`, `escalated`, `decision_reply`, `undecidable`, `delivery_hold`, `owner_cannot_answer`, `cardless_wait`, `needs_user_external_info`, `park_decided`, `parked_due`, `recheck`, `owner_present`, `outage_recovery`, `todo_ready`, a `report` of `done`, `blocked` or `question` and a `check` whose decision was answered; it drops `stalled`, `no_progress`, `leader_stuck`, `estimate_due`, `overdue`, `review_nudge`, `started`, other `check`s, `parked_summary`, `pr_conflict`, `draft_pr`, `pr_event` and `inbox` (a `progress` report never wakes anyone in any mode), and a kind it does not list is sent. A queued or direct supervision message goes when at least one of its signals is sent (one queued before messages carried signals goes unless off); a task leader Agent's message the same. Switching to outcomes withdraws only what it drops: those signals from the gather and the durable outbox, and a queued message made only of them (an in-flight head stays marked withdrawn with nothing to replay); a queued message that carries an outcome stays whole, the deferred failed starts stay, and no node is asked to cancel anything. **off** is the older switch's off, described next. The older switch stays for older clients and downgrades: `auto_supervision_messages` reads `true` unless the mode is off, and writing it (in `PUT` or as `assistant.auto_supervision_messages`) sets the mode to all (`true`) or off (`false`); a write of the mode keeps the saved switch in step, and an unset mode follows a switch an older hub saved (unset or `true` is all, `false` is off). Off, the assistant sends none of the follow-up messages that wake the main, a leader or the background supervisor conversation, and none of a task leader Agent's: `signal`, `queueSignal` and `restoreDigest` gather nothing, no digest timer is armed, a timer or drain already running re-checks before it takes the gather, queues, or sends (`sendDigest`, `queueRelayDurable`, `sendRelay` (the one send step under `deliverRelay` and the direct and queued supervision paths), `sendToLeader`), and the background supervisor's migration tells nothing again. Turning it off withdraws once what waits: the gather and its timer, the durable outbox, every queued `Tool=supervise` message (an in-flight head stays marked withdrawn with no signals left, so its drain removes it without retry or replay) and the deferred failed starts; a queue restored after a restart, a node that reconnects, or a drain that was already running ends the same way. The outcome is **suppressed** (`errSupervisionSuppressed`): not sent, not queued, not retried, not pushed, not a refusal, and no dedup, turn or acceptance receipt is written. The nodes are asked to cancel (`assistant.cancel_supervision`, `docs/protocol/protocol.md`) the supervision input a conversation has queued but not started, by its structured message source; a turn that has begun and every transcript stay as they are. Turning it back on (to all or outcomes), or going from outcomes back to all, takes only what happens afterwards: nothing withdrawn is replayed (a task that is still in a state the watch reports may be told once, as a new event). Not affected: the task ledger and states (judge, review state, auto-review), the owner's own questions and `task_review`, approval and permission cards and their answers, and the safety checks. Not covered: input a task Agent's terminal already queued by keys, which has no message id to cancel.
  **No main chat set** (#10126): on a hub with no main chat yet (an owner who has only used other chats), what would go to the main chat (a `failed` or `started` start, a task waiting for the owner, a task whose own chat is gone) goes to the chat that dispatched the task instead: the ledger's `supervisor`, else the to-do's `origin`, while that is a live assistant conversation (`routeTo`, `server/internal/assistant/supervise_digest.go`). A side chat's or leader's own signals go to it as always, and a task leader's workers' to their leader; with that leader unreachable, to the chat above it (its task's supervisor) as the decision it would get anyway, and with no chat above it either, they wait. Only a signal no such chat can be told waits for a main chat, as everything does while the assistant's computer is unknown (#5420); the log says so once and then hourly while one waits, counting only those, and never for signals that went out. With a main chat set nothing changes. Retrying a start in its kept tab (`task_retry`) belongs to the current supervisor (main may also intervene).
  Supervised tasks no longer emit "🔔 任务…" (task notices) from `quiet.go`; all appear in supervision messages. Reporting-origin tasks keep their quiet notification to the origin while main also supervises them; legacy unsupervised work is unchanged.
  **A turn that ended unheard** (#5718, `server/internal/assistant/task_turns.go`): the same loop takes up a supervised task's turn that the runtime closed while no session list said `working` (an inventory that says `idle`, `done` or `unknown` throughout, a manager down or disconnected at the time), and finishes what a manager before a restart left of one (the judgment, the message). `review`, `idle`, `unjudged` and `interrupted` are kept on the turn's row in `assistant.task_turns` with when they were told, so each completed turn has one receipt, one judgment and one message across restarts and repeated or reordered lists. Key, bound and cost: `docs/assistant/todos.md`, "Automatic completion".
  **`orphaned`** (#3618, `server/internal/assistant/tasks_orphan.go`): every Agent of the open task is closed or its computer is offline, and no wake, continuation, card or queued input is pending for any of them (`docs/assistant/todos.md`, "Nobody is working on this"). The message says why (closed after a model error with the error, closed, computer offline) and the instructions give three ways out: send the Agent a message to resume it, start another Agent and cancel this task, or close the task. One state segment is the same set of Agents with no new step from any of them, so the signal is told once however the reason changes meanwhile (a closed Agent's computer going to sleep), and again only after an Agent worked and was lost again. It is a decision-waiting signal (2-minute minimum), is dropped from a waiting message once an Agent is back, and is never raised for a task waiting for review or for the user.
  **`inbox`** (#4293, `server/internal/assistant/supervise_inbox.go`): off unless `inbox_signals` is on in the assistant's settings (`GET/PUT /api/v1/assistant`, `persona_settings` key `assistant.inbox_signals`, approved on the phone; Settings › Assistant › Agent follow-up on the web), because each one is a supervisor turn the user pays for. With it on, when an Inbox pass takes new messages from other people into an item linked to a supervised task by an exact URL or the judge ("Message cards link to tasks" below), the task's supervisor gets the signal through the same path as every other: pending, deduplicated by the item and its newest message, routed to the task's leader conversation or the main chat, gathered and spaced at the normal (not the decision-waiting) cadence. The entry names the item (id, title, who wrote the newest message, one line of it) and how many messages came; several about one task before the message goes out are one entry, the newest item's, with all of them counted. The instructions say the item's text is other people's data, not the user's instructions: pass what the Agent needs on with `agent_send`, ask the user before changing the task on someone else's word, and leave replies to the AI twin's draft. Never raised for an unsupervised task, a task waiting for the owner's decision (`needs_user`), a task leader's worker (a coding agent without permission prompts gets no outside text), Persona's own confirmed reply, or a bot chat (where Persona's own bot posts its pushes and cards), so nothing a supervisor does can come back as a signal; the twin's handling chat writes nothing into the Inbox. A signal still waiting is dropped if the switch goes off or the task starts waiting for the owner. An item linked to a task that was closed and carried on by exactly one open task (`continues`) is linked to that task first and signals it (#6423, `docs/assistant/inbox.md`); a message is counted as told only when admitted for delivery.
  **GitHub delivery without a redundant wake** (#7346, #9916): a GitHub PR or issue discussion goes straight to the task's Agent through `sendTaskInput(..., wait=true)`. A confirmed send with its durable external-event record counts as delivered, including a successful resume. The existing `feedRouted` task entry and delivery log keep the note and URL; active work (empty supervision state or `rework`, with no owner wait) gets no separate `inbox` signal. State is checked before and after sending. A task parked or awaiting review/decision at either check still gets the supervisor entry, marked as a copy after delivery. No reachable Agent, a stopped/offline Agent, or a failed send/resume/event-record write retains the unmarked supervisor fallback. If the Agent accepted the event but a routed-feed append fails, the durable event remains and the feed error is logged; an accepted event is not resent merely to recreate that feed row. Other sources stay supervisor-only; idle/review supervision is unchanged.
  **Scope and authority**: the existing semantic judge is the task Agent reading the full discussion against the original task, not a new model call or keyword rule over the clipped Inbox preview. In-scope review fixes and merging main may proceed under the original rules. A scope/requirements change, someone claiming to speak for the owner, or an action needing authorization must instead use `report_to_supervisor(kind="question", text=..., evidence=[thread URL])` before acting, at default urgency (not `urgent:false`). This wakes the supervisor with `signal=report`; it does not recreate the redundant `inbox` copy. External text is not owner authorization. A task already in `needs_user` remains excluded from Inbox forwarding as before; its existing owner-decision route remains authoritative.
- **Empty external-event turns** (#10050, `server/internal/assistant/inbox_no_output.go`): a successful send of a GitHub Inbox note is only transport delivery, not handling. The existing turn ledger keeps the source, sender, recipient and a server-authored delivery identity. If that exact prompt ends without a visible reply or tool call, the hub redelivers it once. Another empty turn or a failed redelivery raises an `inbox` signal naming the still-unhandled source, at the ordinary Inbox cadence; distinct failures cannot absorb each other or a successful-delivery copy. Output acknowledges only the matching event, not task completion. Duplicate ends and manager reloads do not replenish the retry budget; a retry whose outcome was lost is reported after its five-minute send window, not blindly sent again. An unrelated owner instruction or explicit user stop never acknowledges or automatically retries the event. Closed tasks, owner waits, stopped Agents and changed assignments retain their guards. This does not change the feature switch, task linking, successful-delivery copy policy, parking admission, or supervisor-only routing for other sources.
  Once sent, that source's escalation is deduplicated until the task closes, independently of manager restarts, ordinary Inbox messages, recipient changes and later replies. A busy or unreachable supervisor leaves the source pending for revalidation, not in a rendered relay queue; an unaccepted dispatch releases the reservation. The failure notice names the recorded recipient, not a successor Agent or a different turn's last reply. Recovery tracks each recorded recipient's look and backoff independently, with reads serialized within a task; one broken transcript cannot block another recipient. A temporary owner wait never acknowledges an unconfirmed retry: every sweep can restore its source-bearing notice, and the durable delivery fingerprint, not the early in-memory sent cache, decides whether it was delivered.
- **Levels and visibility** (#1988): `info` reports update task progress only and never wake the assistant. Other automatic signals are `triage`: they wake it but the web folds the whole turn into a muted supervision record by default, including the input, trace and answer. Expanding the record reveals the original content; transcripts are never deleted. Older supervision messages default to triage too. The fold ends at the next genuine user message, so that user's exchange remains visible.
  `supervision_finish {visibility: silent|user, summary}` gives the assistant an explicit disposition. Silent means finish without a user-facing reply; user explicitly reveals the turn without adding a push. Successful `notify_user`, `ask_user`, and user-facing `task_review` conclusions also promote it, monotonically: a later silent disposition cannot hide them. Server work folds preserve promotion via `fold.supervision_user`.
  Native clients need corresponding whole-turn folding, promotion and `checking` receipt rendering; this change modifies server and web only.
- **No push by default (main, leads and info-level rounds)**: when a turn initiated by a supervision message in the main chat, a direction lead, or the legacy background supervisor ends normally, send no completion push (`fleet_push.go`) or channel mirror (the snapshot marks `SessionView.supervising`). Errors and interruptions still push normally. In a side conversation, user-level supervision rounds (a decision, a review to accept, or an agent waiting for an answer) are **not** push-held and push the owner with the task and conversation links, while info-level rounds stay unpushed (#13657). Normal behavior resumes when the user speaks in the conversation or another message is injected. Use `notify_user` to tell the user and `ask_user` for decisions.
  Active holds and their promotion flags survive a hub restart in the assistant's Postgres settings (#15549).
  The manager clears the hold on turn completion or before a new ordinary message goes in. Orphaned holds
  expire after two hours. A legacy background round fork uses its base supervisor's hold.
- **Its Agents' turns are not pushed either** (#8270): a turn of an Agent on a supervised to-do (one whose ledger names a supervisor: the main chat, followed up by the background supervisor or not, a leader, a task leader, another chat that supervises what it hands out, or the main chat through `agent_supervise`) gets no "finished" push and no Lark card, a failed or cut-off turn neither: the supervisor follows the stop up (the done judge, its signals) and tells the owner what is theirs with `notify_user`, `ask_user`, the decision push and the conclusion card, as for quiet tasks. A turn whose to-do was closed while the push waited for its delay counts too (`supervisedTurn`, `server/internal/assistant/fleet_push.go`). The to-do's bound topic still gets its line (`docs/assistant/todos.md`, "Posting into the thread"); the extensions hear no `agent.finished` for the turn, which goes with the push. An Agent on no to-do, or on one nobody supervises (such as a New Agent started from the Agents page), is pushed as before.
- **Review**: MCP tool `task_review {todo_id, verdict, checks:[{criterion, met, evidence}], note, outcome?, reason?, link?, recommend?, why_owner?, options?, cost_of_waiting?, can_wait?, user_check?, deadline?, reversible?, until?, waiting_on?}`. `accepted` closes the to-do (then closes the Agent according to "Close Agents for finished tasks"), except `user_check: true`: the to-do stays open in `user_check` (**Awaiting your verification**) with its Agent available until the owner's Pass. A newer owner instruction in a linked Agent session blocks a bare acceptance until a later complete `done` report or a `task_scope` criteria revision; `user_check` is the explicit owner-confirmation path. Change feedback returns to that same Agent as ongoing work; see [todos.md](todos.md) "Waiting for the owner".
  `accepted` and `needs_user` send the owner one conclusion card (`docs/extensions/bots.md` ("Conclusions")): `outcome` done / cannot, `reason` one sentence, `link` one link, and for `needs_user` the `recommend`ed option, which "OK" takes.
  `rework` sends the itemized gaps and instructions (`prompts/assistant/supervise_rework.md`) to the task's Agent (`session.send`, first restoring a closed Agent with its original launcher), leaves a `routed` entry in the chat, and judges again when the Agent stops. **At most 2 rounds**; a third `rework` is refused unless it carries a decision for the owner (below), and then becomes `needs_user`.
  **`needs_user` is a decision the owner can answer, or it is refused** (#3804, `docs/assistant/todos.md` "Decisions the owner can answer"): `why_owner`, `reason`, `recommend`, `options` (2 to 4, each `{label, leads_to}`) and `cost_of_waiting` are required; the hub adds where the task came from and where it stands; a reversible one is taken by default after 24 hours, and `irreversible` / `visible_to_others` never are.
  Three signals keep a decision from rotting, each told to the task's supervisor once: **`recheck`** (it waited 24 hours: confirm its facts and re-issue it, or close it), **`undecidable`** (a `needs_user` task with no ask or no recommendation: restate it, or close it) and **`owner_cannot_answer`** (the owner answered "You decide", "This isn't what I asked for" or "Tell me the background first" instead of an option). While a recheck or undecidable hand-back is current, the stale round leaves every owner queue and cannot be answered or pushed; a fresh `needs_user` review makes a new answerable round.
  **`parked` is for a task that only waits** (#3804, `docs/assistant/todos.md` "Parked tasks", `server/internal/assistant/parked.go`): `reason` (what it waits for) and `waiting_on` (`owner_priority`, `someone_else`, `external_event`, `date`) are required, `until` is a duration or a time (24 hours by default, at most 30 days). The task stays open and is not in the owner's Needs you: no ask, no push, no conclusion card, and no `stalled` / `idle` / `orphaned` / `overdue` / `undecidable` signal while it is parked. At `until` its supervisor gets one **`parked_due`** signal (deduplicated as the others, told once across manager restarts, and resent up to three times an hour apart while no review follows it, #14048): decide, park again with a new reason, or close. An Agent turn ends the parking, and so does a decision of the owner's recorded on a task parked for `owner_priority` or `someone_else`, which its supervisor hears as one **`park_decided`** signal carrying the to-do's decisions (#8044), even when it reviewed or parked the task again before that went out (only closing the task drops it), and a ledger write that failed then is made good by the watch from the stored decision; three re-parks in a row with no Agent turn in between are taken and the fourth is refused. The refusals of a `needs_user` that is no decision now offer parking.
  After `needs_user`, no more supervision signals are emitted except those three; the conclusion card asks the user (without the Lark bot, the assistant must ask them). `cancelled` closes the to-do (stored as `done_by = cancelled`, reason "取消：…", meaning "Cancelled: …"). Later Agent turns leave it closed and supervision off; only an explicit user **Reopen** resumes task tracking. An accepted task reopens only when its own conversation, its supervisor or the background supervisor sends its Agent more after the acceptance; the Agent's other turns leave it closed (`docs/assistant/todos.md` "State calculation rules"). Every review inserts a `review` chat-activity entry and is recorded in ledger `reviews`. Exactly one conversation — the task's current supervisor — can review a task at a time, including during a hand-over; main chat cannot review a task another conversation supervises (#13657).
- **Independent review** (#2173, `server/internal/assistant/supervise_autoreview.go`): off unless `manager.json` `agents.review.launcher` is set (`docs/operations/config.md`). When the judge says a supervised task is done, it does not enter `pending_review` at once: a fresh reviewer agent reviews the change first.
  - **What it sees**: the task's title and notes (its ask), its `done_when`, and the change from node `git.review` on the working agent's directory (branch against the default branch, uncommitted when there is no base; per-file diff blocks within budgets). Never the working agent's transcript, its last answer or its directory (`prompts/agents/review.md`). It answers with P0 / P1 / P2 findings with `path:line` evidence and a last line `<review-findings count="N" blocking="M"/>` (M: P0 and P1).
  - **Where it runs**: node `worktree.snapshot` checks out the working agent's `HEAD` detached under the computer's checkouts directory and applies its staged, unstaged and untracked change there (`node/reviewsnapshot.go`). The reviewer starts in that copy with `agents.review`'s launcher, model and permission mode (default `plan`), so what it edits, formats or builds — even after an engine restores it without the read-only mode — stays in the copy. When the run ends the reviewer is closed (`session.close`) and the copy removed (`worktree.remove`, forced); the tick retries both until they are gone. The copy is a plain worktree on every engine, so the node removes it as one on a computer whose engine is Paseo too, never through Paseo, which made no workspace of it (#8135). The same boundary now backs #1808's user-triggered session reviewer (`agents_session_review.go`, [agent-work.md](agent-work.md) "Independent review of a session's work"); a node without `worktree.snapshot` fails the run, and the task waits for review as without one.
  - **What follows**: blocking findings become the next rework round (`prompts/assistant/supervise_review_rework.md`, the same `round` and 2-round limit as `task_review` rework, recorded in `reviews` with `by: "reviewer"`); no blocking findings, or no round left, and the task enters `pending_review` with the result attached: the supervision message shows it under the task, and the to-do's `supervision.auto_review` carries it to the apps (the web shows it under the criteria). A run that cannot get a copy, a reviewer or an answer, or runs past `timeout_minutes`, fails, and the task waits for review as before.
  - **State**: the run is a sub-state of the ledger, not a review state: the task stays in progress (or `rework`) while it runs, so clients that know only the review states are unchanged. `todo_supervision.auto_review` (jsonb) keeps the latest run: `{id, state: running|clean|blocking|failed|cancelled, work, round, judged, machine_id, cwd, session_id, worktree, started_at, ended_at, findings, count, blocking, error}`. Every transition is a compare-and-set on the run's id while it runs, so a turn end, the 30-second tick after a manager restart, and a cancellation (the agent works again, the supervisor decided, the to-do closed) each decide it once.
  - **The reviewer session** is linked to the to-do in `todo_agents` with `source: "review"`, `via` the run's id, released from the start: it is never one of the task's agents (judge, rework, closing). It is left out of the session lists the apps get, kept quiet (no pushes, badges or Live Activity), and its turns are not triaged.
- **Assignment deduplication**: `task_plan` compares new tasks with ongoing to-dos and working / waiting Agents. The same issue / PR number, or the same computer and directory (including nested directories) with similar titles, or very similar titles, yields "Possible duplicate" in the result. The assistant is told to inspect before deciding whether to merge or cancel; dispatch is not blocked.
- **Snapshot**: supervised tasks under "Tasks started from this chat" include "Supervision: normal / stalled N minutes / waiting for an answer N minutes / delivered, awaiting review / rework round N / waiting for you / parked until a time, on what and why / accepted / cancelled, with its reason" and a criteria count. Cancelled tasks are separate from completed ones. Items needing action come first.
- **Assistant workflow**: common task planning stays in the shared `prompts/assistant/claude_md.md`; detailed supervision handling lives once in `prompts/assistant/supervision_instructions.md`. The manager sends this static fragment as per-conversation system/developer instructions only to authorized supervisors, never by rewriting their shared directory. The planning direction lead supervises its own tasks, main supervises other dispatches, and explicit transfers override that initial assignment; legacy background conversations keep only previously assigned work. Event messages contain task facts only. See `docs/assistant/assistant.md`, "Role-scoped supervision instructions (#8251)", for role scope, startup/resume isolation and the custom-runtime compatibility boundary.
- **In charge of any agent** (#5112, `assistant/agent_control.go`):
  - **Levels of interruption**: `agent_send` queues behind the agent's turn; `agent_interrupt` stops the turn at once (`session.interrupt`: Esc in a herdr pane, the runtime's or Codex's own cancel) and keeps the session; with `then` it sends the new instruction right after (steering). Work already done is not undone. Not for assistant conversations or read-only sources (`capabilities.interrupt` false). An agent that is not working (including one still starting) is not stopped; `then` is still sent (queued if it is busy). Closing an agent is not offered to the model (accepted / cancelled tasks are retired as before).
  - **Adoption**: `agent_supervise`, from the main conversation, a direction’s lead, or a live side conversation for an Agent it started or a task it already supervises (#13657). An agent on no todo becomes a task of the conversation: a new todo (its `title` when given, cleaned as `task_plan` titles are; else the session's title), the agent linked, the supervisor set, a task card in the feed; from then on it is like a `task_plan` task (signals, pending review, `task_review`). On one of its own tasks it sets `done_when` / `expect_minutes`. Another conversation's task or the user's own todo is refused and stays theirs. If the todo is made but the agent cannot be linked, the todo is closed at once (no empty task is left); one adoption runs at a time. With no role, the agent is not told: the answer reminds the assistant to tell it with `agent_send` if it should work to the criteria or report.
  - **Handing work to a running agent**: `agent_send` with `task {title?, done_when?, expect_minutes?}` from any assistant conversation. The calling direction lead — or a live side conversation for an Agent it started or a task it already supervises — supervises the new or reused to-do, otherwise main does, while the caller remains its origin (#13362, #13657). A `task` that says nothing (null, or an object of only those keys with no `title`, no non-blank `done_when` and no positive `expect_minutes`, which runtimes that fill in every field of a tool's schema send for a reply) is no task (#3386): the send is a plain one from any conversation and changes no to-do. Anything else stays a hand-off, with its refusals: another type, a wrongly typed field, or a key the task does not have (a misspelled `title`). Otherwise the agent is adopted as above before the text is sent (the text is the new todo's notes; with `title` empty it is named from the text as `task_plan` tasks are: cleaned at once, then named by the model, `docs/assistant/todos.md` "To-do titles"); the task card and a `routed` line with the text go into the feed once it is sent. An agent already on one of the conversation's own tasks stays on it when `title` is empty or that task's title (no second todo; `done_when` / `expect_minutes` are written after the send); another title is refused, so other work is not filed under a task that may be closed without it. An agent on the user's own todo or a conflicting existing task, and a text of only `/exit` / `/quit`, are refused before the user is asked to confirm; a side conversation itself is not a refusal. nothing is sent. If the send fails, the todo made for it is deleted (a closed task would keep the agent and retire it later), unless the machine only did not answer in time (a resume can go on after the wait): then the task stays, with its card. Work the agent passes on follows the task without a new tool when its launcher passes the herdr marker on (`persona_from`) or it dispatches through `botmux dispatch` (`docs/assistant/todos.md` "Following spawned Agents"): every agent it starts that way while it is on the task joins it, for this work or not, and only once the node has written the marker to its pane (sent when the link changes, reconciled every 30 seconds). Any other agent is moved onto the task by a link by hand (`PUT /api/v1/todos/{id}/agents/{mid}/{sid}`). The handing agent stays linked until the task is closed, so a long-lived agent is on one task at a time; when the task is closed, it is stopped like any task's agent if Persona started it, its terminal kept (read-only from then on: the user's Resume or the assistant's explicit resume, `agent_send` with `resume`, brings it back), while one the user started by hand is left running (`docs/assistant/todos.md` "Stop Agents of completed tasks").
  - **Agents report to their supervisor**: agents get the MCP tool `report_to_supervisor {kind: progress|question|blocked|done, text, evidence?, step?, steps?, urgent?}`. Only an agent of a supervised task has someone to receive it; any other gets "nobody receives this" (not an error). With Agent tasks on ("Agent tasks" below) an agent on any open to-do has a receiver, and only an agent on no to-do gets that answer. A question with `urgent: false` is no signal: it waits in the queue for the next morning or evening briefing (`docs/extensions/bots.md` ("Questions that can wait")). A report becomes the supervisor signal `report`: `question` / `blocked` / `done` are triage (the 2-minute cadence), while `progress` is info and never creates a main-chat wake; reports of the same kind waiting for the same message are joined (both questions kept, evidence merged) instead of replacing each other; the message lists the kind, the step, what it said and its evidence. `done` does not mark the task pending review (the hub's judge still decides). At most 10 reports per session in 10 minutes. Claude Code agents with the Persona MCP have the tool; an Agent without it (Codex and the other terminal families) calls it through `personad tool report_to_supervisor` from its pane or Codex thread (#12096), and the task handoff note names that entry and the arguments' shape when it verified the node's executable (`prompts/agents/task_handoff.md`, #12136).
  - **Progress and the feed** (step 2): the latest report is the task's progress, `supervision.progress {kind, step?, steps?, text, line?, detail?, at}` on the todo the apps get (`line` and `detail`: "Progress tray" above). It is a live reading kept in memory only (the next report brings it back after a restart) and written apart from the ledger: no database write, and `updated_at`, which names the review a task waits for, does not move. The snapshot's task line says "它最近向你汇报（N 分钟前）：…，第 3/5 步：…". `question` / `blocked` / `done` reports also go into the supervisor's conversation as a feed item of kind `report` (body: `kind`, `text`, `evidence`, `step`, `steps`), so the user can follow the handoff; progress reports only update the card. An `agent_interrupt` with `then` on an agent of one of the conversation's tasks adds a `routed` line with `interrupted: true` (a bare stop adds nothing). Clients that don't know the `report` kind skip it (web, iOS and Android all draw nothing for an unknown kind); those that don't know `interrupted` show the line as an ordinary message to the task. The web draws the progress under the task card's current step (`progressLine`), the report as "「任务」向 Persona 汇报" with the text and evidence, and the interrupting line with a stop icon. Mock: `MOCK_TASKS=1 MOCK_TASKS_REPORTS=1`. Without a report, the agent's own task list is the default progress (#5302, `Session.plan`, docs/integrations/agent-goal.md "Plan"): the snapshot's task line says "计划 3/5：<current>" and the card has a "3/5 · current" line; a report newer than the plan stands alone.
  - **Whose call it is**: an agent's MCP server has no session id of its own. `personad mcp` passes its pane (`HookPane`) to the node, and **only for the agents' own tools** (`report_to_supervisor`, the to-do's `todo_decisions`, `todo_decision_add`, `todo_bind_thread` and repository tools, from the list the manager reads too, `assistantwire.AgentToolNames`, #9089; dispatch and a task leader's `task_plan`: `callerTools`) the node resolves the session from it (`node/hooks.go` `paneCaller`: the agent the host shows in the pane now, else the hooks' binding). Other tools still call without a session (a connector's approval card is not attached to the agent). An agent run by a runtime without a pane gets "nobody receives this". The terminal entry resolves the same tools the same way (#12096): `personad tool <tool>` from an Agent's pane, or with Codex's per-turn `CODEX_THREAD_ID` (which wins over the pane), goes to the hub as that Agent's call, `assistant: false`, so an Agent without the Persona MCP (Codex and the other terminal families, a continuation's replacement among them) reads its own to-do's decisions and, leading its task, plans and reviews workers. The hub decides each call for that session exactly as over MCP: a worker's `task_plan` is refused, `todo_decisions` reads only the caller's active open to-do, a released Agent (the one a continuation replaced) has no to-do left. A call that names no known Agent session, and every other tool, keeps the assistant-only check ("Separate sessions within a supervised task (#5751)" below).
- **Attachments** (#2437, `assistant/attachments.go`): pictures and files the user gave the assistant reach the agent through `attachments` on a `task_plan` task, `agent_start` or `agent_send`: absolute paths on the computer the conversation runs on. Only paths the user gave in that conversation are accepted: the directory where Claude Code saved the conversation's pictures (`…/<conversation id>/images/`), the node's `uploads` directory, or a path one of the user's messages carries (the app puts a file sent with Files there). Anything else is refused, so the tool cannot move arbitrary files between computers. On the same computer the paths go as they are. On another one the manager copies each file there first (`file.chunk` from the assistant's node, `file.upload.*` with `dest=terminal` on the agent's: `uploads/terminal/<date>/`, swept after 7 days), at most 8 and 50 MB each, also to computers marked `work`. A `task_plan` task's are checked when the plan is made (`PlanTask.Attachments`, from `AttachFrom`, the conversation's computer) and copied when it starts. A model does not always use the parameter, so a path the prompt or text names counts too when it is where the user's files land (the conversation's pictures, the node's `uploads`, or a file the app uploaded, `<name>-YYYYMMDD-HHMMSS`, that a message of the user's carries; never any other path the user wrote, which may be a repository's file the agent should open where it is). The text then names the copies. The agent's first message (or the sent text) ends with the files, the named ones that could not come and why, and the pictures in an `<attached_images>` block, as a picture sent straight to an agent ends. A copy that fails never stops the hand-off; the tool's answer says what was handed.
- **Clients**: web and Android task cards / to-do details show acceptance criteria and review state; iOS / Mac are separate work (sub-issues of #3124). Clients unaware of `supervision` continue normally, showing pending-review tasks as "Stopped / Unread."
- **Closing preserves active conversations** (#6819): `task_review` `accepted` and `cancelled` refuse while the owner recently viewed or messaged a linked Agent (ten minutes), or an app still has its conversation open. Cancellation closes only the task, not the Agent: its conversation stays usable and its pending runtime confirmations are not silently answered. Completed and cancelled task Agents share safe deferred retirement after both Agent and owner inactivity; their terminals and transcripts remain, the archive is reversible, and the read-only reason explains that the task closed (`docs/assistant/todos.md` "Stop Agents of completed tasks").

### Briefs (#3822)

A task's notes are the prompt its Agent got. When an Agent fails and another takes over, the notes are the hand-over
prompt, written by the supervisor, and what the owner asked for is nowhere on the task. A **brief** fixes that: one
record per piece of work, made when the work is handed out and never rewritten (`server/internal/assistant/task_brief.go`).

**What it holds.**

| Field | From | Meaning |
| --- | --- | --- |
| `request` | the hub | The owner's words that led to the task. `text` keeps their original wording and spacing, up to 2000 characters (`truncated` if clipped). `part: true` marks a selected passage. `at`, `conversation` and `message_id` identify the original message. `via` lists the conversations that relayed it, oldest first. Absent when the owner did not ask for it |
| `started_by`, `trigger`, `trigger_title`, `reason` | the hub, `reason` from the model | Who started it: `owner`, `assistant`, `leader`, `routine`, `hub`, or `unknown`; what the turn followed when it was not the owner (`supervision`, `followup`, `routine`, `conversation`, `notice`, `meeting`, `wake`); and why (`task_plan`'s `started_why`) |
| `goal` | the model | The work in one sentence (the task's title when a writable task gives none) |
| `done_when`, `out_of_bounds` | the model | What done means and what the Agent must not do |
| `expect_minutes` | the model, optional override | About how long it should take; omitted uses the hub's automatic estimate |
| `estimate_source` | the hub | `learned` for a comparable history estimate, `system_default` for the 60-minute default, or `override` for an explicit value. A dispatch records the source on the brief's log |
| `estimate_evidence` | the hub | The automatic estimate's minutes, number of samples and chosen cohort. It accompanies a new plan and its recorded dispatch |
| `reconstructed`, `handover` | the hub | A brief made later for a task from before briefs ("Backfill" below) |

**Who asked.** The hub reads the conversation that made the tool call (its last three turns) and goes by the latest
message in it. One the owner wrote is the request. One Persona or another conversation put there (a supervision
message, a task notice, a follow-up, a routine's run, a relayed message, a background task's wake) means the owner did
not just ask: the work is recorded as started by the assistant (a leader conversation: `leader`; a routine's run:
`routine`) with what it followed, and no older message of the owner's is attributed to it. A conversation that cannot
be read records `unknown`. The hub's own starts (an Agent that became a task, an inbox hand-off) record `hub`. The model
never writes `request`: a `brief` in the tool call is dropped.

**Across a relay.** When the main conversation hands the owner's request to a leader, the leader's latest message is
the relay, so its brief would only say a conversation started it. `conversation_send` and `conversation_new` take an
optional `quote`. With `"latest"`, the hub uses the one owner message or verified incoming quote in **this turn**.
A queued owner message belongs to the turn already running. A background notice or wake continues it. If the turn
contains several candidates, the tool lists their ids and first words so the sender can choose. If the turn contains
none, or its start is outside the transcript page, the sender can give the exact words of an earlier request or omit
`quote`.

A Codex transcript page can show a message steered into a running turn with the same shape as the turn's first
message. On Codex, or when the manager cannot identify the session, select the message by id or exact words. In
the list of choices, copy the displayed id to select the whole entry; the text next to it is a preview.

A message id or the owner's exact words can select from the last ten turns. An id takes precedence. Text matches
the newest owner message or verified incoming quote that contains it, with at least four characters and runs of
whitespace compared as a single space. The receiver gets the original spacing. A selected passage sets
`request.part` and keeps the source message's time and id. A marked Just me turn cannot quote anything, and a
Just me question remains ineligible afterwards. A refusal uses no relay allowance.

The hub keeps the verified words for 14 days, up to 64 entries in `assistant.quotes`, keyed by sender and the id
in the receiver's `<persona-context kind="owner-request" message_id="…">` block. A whole message keeps its id.
Each passage has its own stable reference so different receivers can pass on their own passage using the id in
their block. A source message's id selects the whole message when the receiver has it. The receiver's brief takes
the stored words by that reference and the relay item's source. On upgrade, the manager discards retained quote
records whose source predates the shareability check. The owner can select an eligible original message again. Only a
block directly following the canonical relay context counts (`prompts.ParseRelayQuoteID`). The brief keeps
`started_by` as `leader` or `assistant`, trigger `conversation`, and the conversation ids in `request.via`.
A scope change requiring the owner's approval still needs a message they wrote in the conversation making it.

**Hand-overs** (#11699, `server/internal/assistant/relay_handover.go`). A `conversation_send` that carries a verified
quote and has a direction's lead conversation (`assistant.leaders`, not a task leader Agent) on either end is one
hand-over: from the main conversation to a lead, from a lead back to it, or between a lead and any other conversation.
The hub records it once the send is accepted, sent at once or queued (setting `assistant.handovers`, by id, kept like
the quotes: 14 days, at most 64, the oldest dropped first): the sending and receiving conversation, the receiver's
title, where the owner wrote the words (conversation and message id), its state and code, and when it was made and
last changed. A send refused at once (the sending model has the error), one without a quote and one between two
conversations neither of which is a lead are none. Its state follows what the relay does with the message, is written
only once it is true and never goes back, and each change is one log line:

- `queued`: the message waits in the receiver's queue.
- `delivered`: the message went in: sent at once, from the queue, or a try in doubt later found in the receiver's
  transcript. A try in doubt is never `delivered` on its own.
- `answered`: the receiver's first `conversation_send` back to the sender since the hand-over was delivered went into
  the sender, however much later, while the hand-over is kept.
- `not_delivered`: the message will not be tried again; its code is the refusal's when it was refused for good after
  it was queued, `gone` when it was dropped because the receiver is gone. The sender still gets the line that says so
  (`docs/assistant/assistant.md`, "Between conversations").

A try in doubt that did go in starts the receiver's turn before any look finds it, so an answer sent in that turn
goes in while the hand-over is still `queued`: it counts once the look finds the try, which makes the hand-over
`delivered` and then `answered`. If the looks never find it and the message is sent again, an answer named before then
does not count, whether it went in already, waits in the sender's queue or is on its way; the same holds when the
message moves to another receiver.
A hand-over past its 14 days is no longer answered or changed. One queued for a background supervisor that is gone
follows its message to where it waits now (the new background supervisor or the main conversation), receiver and title
included, also one whose `queued` was not written yet; with nowhere to move it is `not_delivered` with `gone`. An end
or a move is saved before the queue without the message is.

An answer is matched on the hand-overs, not on the relay's hop record, which forgets after an hour, when the owner
speaks in the receiver, or when a third conversation's request reaches it. The reply names the hand-over it answers
(`answers` on the queued message): of the pair's hand-overs that no other reply on its way still answers (one named
before the hand-over was sent again or moved does not), the oldest delivered one, else the oldest still in doubt; one
that waits untried is never named.
A reply that is refused or dropped leaves its hand-over `delivered` for the next one. In a lead's queue a hand-over goes
after the outcomes to report and before the rest (`docs/assistant/assistant.md`, "Messages from the system wait for the
owner"). Besides the log, only the start rule below reads the hand-overs (a hand-over past its 14 days marks no lead): no
route, event or client shows them.

**Start in a lead after a hand-over** (#11699, `server/internal/assistant/relay_lead_hold.go`). Since decision rights
(2026-11-23) this no longer holds a lead's plan back: a lead's own plan is its release and starts at once ("A supervisor's
own plan starts at once"), since words handed to the wrong lead start work in the wrong place, which is reversible and the
supervisor's to set right. The mark described below is still kept. Before then: once a hand-over of the
owner's quoted words is sent into a direction's lead conversation, `task_plan` there ignores `user_requested`, whatever
opened the turn: a writable task it plans waits for the owner's Start. The main conversation picks which lead gets the
owner's words and can pick the wrong one, and a writable task would then start in the wrong repository before the owner
sees it.

- **From when:** the lead's wait is saved with the relay state (`handed` in `assistant.relay`) before the send leaves,
  so a try that ends in doubt, or a manager restart during the send, has it before the turn the message opened plans
  anything. It is taken back only when the try was definitely not taken and the lead had no wait before. A hand-over
  still queued and never tried, or past its 14 days, sets none.
- **Until when:** the owner's next message to that conversation, when it is submitted (before it is delivered, and from
  any device or extension that posts as the owner). It has no expiry, and it holds after the conversation stops being a
  lead.
- **What it covers:** `task_plan`'s `user_requested`, narrowed by #12953 so a lead starts and closes its own work
  without the owner or the main conversation starting it. A task whose brief carries the owner's words as the hub
  verified them, from a hand-over delivered into this very lead, keeps `user_requested` and starts as the main
  conversation's would (the owner chose this over a Start card for every relayed request; the brief shows the words).
  Any other task there (a turn a task's report or a follow-up opened, where `user_requested` is only the model's word)
  keeps it only for a kind (`trustKey`: the machine, the repository or exact folder, reading or writing) that a started
  plan of the same lead already ran; work in a place new to the lead waits for the owner's Start. Read-only tasks and kinds the owner trusts
  (`tasks.trust`) start as before, and the verified words still go into the brief's `request`, so the card shows them. Still starting
  work with no card: `agent_start`, `agent_send` with `resume`, the lead's own tools on its computer, and passing the
  words on to a conversation that is not a lead.
- **Nothing else changes:** an unquoted message to a lead, an answer, a conversation that is not a lead and the main
  conversation behave as before. A previous version drops `handed` on its next save; plans in that lead then start as
  before.

**Required.** `task_plan` refuses a writable task (`readonly` false) without `done_when` (at least one criterion),
and a read-only task without `goal`; the refusal names what is missing and nothing is planned. `expect_minutes` is an
optional override. The hub uses the median of recent accepted comparable rounds when it has enough examples.
It records `learned` for that estimate, `system_default` when it uses 60 minutes, and `override` for an explicit value.
The tool reports the estimate and its evidence. The plan's task carries the brief (`tasks[i].brief`)
from the moment the plan is made, so the card can show the owner's words, the criteria and the time, and "Start"
confirms them. The brief is kept when the task starts, dated from the start. Only `task_plan` dispatches create this
automatic estimate: manually started terminal/herdr Agents, manual `agent_start`, adopted Agents and direct
human-to-Agent work keep their existing optional ledger values but get no `estimate_source`, no `estimate_due` clock
and no calibration history. A continuation or reuse through `task_plan` is a new automatic dispatch even when its
brief began as manual or legacy work. The web, iOS and Android draw `tasks[i].brief` on the plan card; the web also draws
the to-do's `brief` and `lineage` on its page (the criteria stay in the page's one list, the ledger's; a finished task adds the
dropped ones and `also_done` under it) (`docs/web/web.md`, "Pages": **A task's brief**). Web, iOS and Android task pages all expose the
lineage's previous / next cards and cancellation reason (#5732). Android task-detail briefs and receipts remain later work
(`docs/android/android.md`, "Unified main-chat entry").

**How the estimate learns.** Persona groups work by its existing task kind, read-only mode, role and tier. It looks
at the newest 256 accepted samples of that kind in the past 90 days, within one manager. It uses the most specific
group with at least five samples, trying the same machine and repository, then the same machine, then the broader
kind. The median uses up to the newest 32 values in that group and rounds up to the minute, capped at one day.
When there are too few samples, the evidence says how many were available. Unavailable history also uses the default.

A new dispatch from `task_plan` records the cohort once, including plans the owner starts from their card.
Direct owner starts keep their existing brief behavior. An Agent must link successfully in the recorded cohort. The first recorded
completion supplies wall-clock minutes from the dispatch. That duration becomes a sample only when the supervisor
or its judge accepts the round. Reopening a to-do removes its prior round from learning. A manual re-close cannot
reuse that acceptance. Canceled, unstarted, placement-changed and older rounds without a recorded cohort supply no samples. Health-space plans use the default and keep their work out of calibration.
The next comparable plan checks up to 32 recent rounds whose review was accepted while the sample write was
interrupted, and recovers them from the durable admission, completion and acceptance. It skips expired and
reopened work. Estimates and their evidence are fixed on the plan. A continuation of an automatic brief inherits its current
estimate unless given an override. The learned minutes run the same one-shot review clock.

**Lineage.** Same-card continuation and explicit adoption are different operations (#5732, F31):

- `task_plan.continues` (with `continues_why`) continues earlier work on the **same original to-do id**: a retry on another computer, a rework round, or a replacement Agent. It keeps the original brief, criteria and clock; its prompt is task-specific and does not replace that brief. The replacement prompt supplies the goal, authorization boundaries and new information; the manager adds the effective criteria once. For an owner-authorized change, call `task_scope` first and pass its `scope_revision` with `continues`. A stale revision is refused at planning, and the manager rechecks the planned revision at Start and before dispatch. Replan a refused continuation from the current brief. At Start, still-active prior Agents on that to-do are stopped and released, and a closed to-do is reopened before the replacement starts. The original request, scope log and to-do survive a failed start. No successor to-do is created, so there is no `superseded_by` link and no second card. Unauthorized takeovers, an unreachable prior Agent that cannot be stopped, and an open legacy successor duplicate are refused rather than worked around.
- Explicit `agent_supervise` adoption of a stopped or replacement Agent after a closed card creates a new to-do linked to the old one: the old card exposes `superseded_by`, the new card exposes `continues`, and both navigate to each other in the clients. Reusing an existing Agent link does not duplicate the card. For these adoption chains, a to-do's `lineage` is the chain, oldest first: to-do id, title, Agent, machine, session, `started`, `ended`, `why` it stopped, `continues`, `superseded_by`. The to-do that joined the brief last carries the work: the clock, result and briefing go by it, so closing a superseded to-do ends nothing.

**Repository-linked duplicates.** A new task with neither `continues` nor `todo_id` is refused when a GitHub issue/PR
or GitLab issue/MR URL in its `title`, `goal` or `prompt` matches an explicit repository link of an open to-do.
The comparison uses canonical URLs, including provider, host, repository, link kind and number; comment anchors and
tracking queries do not change that identity. Issue/PR URLs with credentials or invalid query parameters are refused
before canonicalization, including for continuations and reused to-dos; errors never repeat the unsafe URL.
Closed to-dos do not block planning. Notes, legacy PR metadata, bare
numbers, similar titles and Agent activity are not evidence for this refusal, and admission reads do not import links.
The refusal names the existing to-do and link: use `continues` with `continues_why`, or `todo_id`, for the same work.
For separate work, supply `distinct_from: [{"todo_id": "<existing to-do>", "reason": "<why this is different work>"}]`
for every conflicting to-do. Each entry needs a unique to-do ID and a nonempty reason; declarations do not replace
continuation/reuse authorization. The reasons are kept in the new task's brief, including after restart.
Task leaders use the same check and may explain separate worker scopes with `distinct_from`.
The manager checks before saving a plan and again at Start, before creating any to-do or dispatching any Agent, so a
waiting card cannot bypass newly added associations or a conflicting prompt edit. An unavailable association store
refuses admission rather than assuming no match. Other "Possible duplicates" hints remain advisory.

**Scope changes are events.** After a task started, `done_when` and `expect_minutes` change in one way: `task_scope
{todo_id, change, why, done_when?, expect_minutes?}`, from the task's supervisor only (the main conversation also for a
task an earlier main conversation supervised). It appends to the brief's `log` (`kind: "scope"`, with `added`,
`dropped`, the criteria it leaves) and applies them to the ledger of every open to-do on the brief. `agent_supervise`
and `agent_send` with a `task` on one's own task go through the same code. A change whose words or added criteria name
a step from the hub's risky words (deploy, merge, release, send, comment, delete, pay, production: `riskyWords`; the
Agent's own pushes aside) that the work did not name before is refused, with a pointer to ask the owner. It is taken
when the owner's own message started the turn that makes it, and that message is kept on the entry (`approval`). At
most 40 scope changes per brief. The response distinguishes the original brief, effective criteria and dropped
criteria, and reports the new revision.

**The estimate review clock.** Every automatic dispatch appends a `dispatch` event to the brief's log with its plan,
round id, estimate and source. At `estimate_clock_at + expect_minutes_now`, the supervisor gets one `estimate_due`
review cue through the existing supervision digest (urgent spacing, and the usual queue when the supervisor is busy).
It is a request to inspect progress and acceptance criteria, not an overdue judgment. A new dispatch or an actual
`expect_minutes` change starts a new estimate clock; a criteria-only `task_scope` does not re-arm it. The `estimate_due`
event is written only after direct delivery or durable queue admission, including all three leader-delivery paths, so
a restart before delivery re-derives the cue and a restart after it does not repeat. A done report, pending review,
owner decision, parked task, closed task or superseded leg suppresses it; a queued old revision is dropped.

**The clock.** A brief runs to `max(expect_minutes × 3, 24 h)` from when the work was handed out (of the brief, not of
the latest take-over), or from its latest scope change. Past that with no end (not done, not waiting for review, not
waiting for the owner's decision), the supervisor gets one `overdue` signal in a supervision message: close it,
re-scope it with `task_scope`, or say why it is still right (also `task_scope`, with a new time). The mark is in the
log (`kind: "overdue"`, `overdue_at`), so a restart does not raise it again; only a scope change starts the clock again.

**Reports against the brief.** The done judge is given the owner's words and the criteria as the scope changes left
them, with the dropped ones named, not only the latest prompt. `task_review` checks are written against the same
criteria; an accepted or cancelled review records the brief's `result` (a delivery judgment alone is not acceptance): each
criterion `met`, `not_met`, `unchecked` or `dropped` (with the scope change that dropped it), then `also_done`. The
review's feed item carries it (`body.result`), and the tool's answer says it.

**Actual duration.** The first active completion of each automatic dispatch round appends a `completion` event with
`round_id`, `source` (`report_to_supervisor`, `task_review` or `judge`) and `actual_minutes`, rounded up from the
dispatch timestamp (or `reopened_at` when the owner reopened the to-do later) to the report timestamp. An Agent's
`report_to_supervisor(kind=done)` records immediately and suppresses any queued `estimate_due`; supervisor acceptance
and the judge's own acceptance are fallbacks when no report arrived. Later reviews and duplicate reports do not
rewrite the first event. A superseded leg, manual/legacy work without a new `task_plan` dispatch, and a failed start
produce no sample. The latest `actual_minutes` and `completion_reported_at` are also on the brief; the per-round events
remain in its log for the follow-up calibration PR.

Two more places concern the criteria (`server/internal/assistant/task_brief_result.go`):

- **The done receipt** (feed kind `receipt`, `state: "done"`) carries `criteria`: each criterion of the brief as it
  stood when the Agent stopped, `{criterion, state, evidence?, change?, changed_at?}` with the states above. They are
  read from the supervision ledger's per-criterion state (`supervision.criteria`, #3635, `docs/assistant/assistant.md`,
  "Per-criterion acceptance progress": what the Agent reported and what a review or the judge checked), not worked
  out again: `met` / `not_met` with its evidence, `pending` there is `unchecked` here, then the `dropped` ones, which
  the ledger no longer lists (from the brief's log). At
  most 8; `criteria_more` counts the rest. Absent for a to-do without a brief or criteria (no brief is made for a
  receipt), so a receipt from before reads as it did. `review {state, criteria}` (the count) is unchanged.
- **The conclusion card** (`docs/extensions/bots.md`, "Conclusions") names only the criteria the work did not meet
  (#10380): one **Not met** line on a done or cannot-be-done conclusion, the first three and how many more, nothing when
  every one was met or only some were not checked, and nothing on a decision. The task's page keeps every criterion
  with its result, the dropped ones with their scope change, and what was done beyond the brief (the brief's recorded
  `result`, the review's own checks at the moment the work ended, which is also what the briefing tells).

**The briefing.** The morning and evening routines' facts (`docs/assistant/assistant.md`, "Scheduled tasks") list, from
the briefs: for each open piece of work that needs the owner, is overdue, or whose scope changed since the last
briefing, "On <date> you said: <their words>" (or who started it and its goal), where it stands in a line (the latest
Agent's closeout when it wrote one), what is needed from the owner; and each piece of work that ended since the last
briefing, once, with every criterion's result. A task parked for the owner's priority call (`task_review parked` with
`waiting_on: owner_priority`, #3804) is listed too, as parked, waiting for their priority call, with its reason; tasks
parked for anything else are not. Work that needs nothing and has not changed is not listed. At most 8
rows a list, 80 characters of the owner's words a row.

**Backfill.** A task from before briefs gets one the first time it is needed (the supervision watch, a scope change, a
review, `continues`): its title as the goal, its notes as `handover` (the prompt at hand-over, not the owner's words),
its creation time, its conversation, its ledger's criteria; `reconstructed: true`, `started_by: "unknown"`, no
`request`. Nothing else is invented.

**Storage.** `task_briefs` (`id`, `brief` written once, `state`: the log and the result) and `task_brief_todos`
(`todo_id` → `brief_id`, `continues`, `reason`); migration `0498_task_briefs.sql`.

### Leader conversations (#3133)

Off by default: with no leaders, only the main chat supervises. A direction lead supervises the tasks it plans or starts from their first dispatch (#13362). Main supervises its own work and ordinary side-chat work; it receives a lead’s owner-decision escalations rather than driving that lead’s Agents.

- **Opening one.** A New chat is an ordinary conversation. Ask the main chat to open one on an Agent preset with `conversation_new {text, tier}`. To make it a direction, include `pin: true`. After its first turn the owner gets the usual `assistant.leaders` approval card. The conversation becomes a direction only on approval. The owner can **Pin** any eligible side conversation to make it a direction and **Unpin** to end it (below, "Pin a chat as a direction"). The owner's choice takes effect immediately. The assistant can also propose a change to `assistant.leaders` through `persona_settings` (`set` with the full list of conversation IDs from `conversations_list`), which requires the owner's approval on the phone. Up to 20 side conversations on the assistant's computer can be leaders. When one is removed (Unpin, or an approved `assistant.leaders` without it), its open tasks go to the main chat's supervision, as the Unpin confirmation says (#11394): each moves as `agent_transfer` moves one ("Transfers and cards" below), so the ledger names main with `supervisor_source: "explicit"`, its supervisor-held cards and queued signals go along, and a restart keeps it. Its open tasks are those whose ledger names it (planned by it, moved to it, or assigned to it by hand), a failed start that still waits included. Closed tasks, other leads' and main's own tasks, the legacy background supervisor's and the owner's own to-dos stay as they are; a task leader's workers stay with their leader Agent, and what it sends up goes to main with its task. Pinning it again does not take them back: main or the owner moves one back with Move to. A task that cannot be moved then (its ledger or the card checkpoint not writable) keeps its supervisor and is logged; the direction ends all the same, and Move to moves it.
- **Directions for work already running elsewhere (#8236)**: an authorized automation or a person using the local script CLI can create a direction for an existing knowledge-base project without opening a Persona conversation. `POST /api/v1/assistant/directions` requires `name` and `project`; an optional `machine_id` / `session_id` pair names an existing coding Agent on this manager, and the pair must be supplied together. The manager generates an opaque `dir_…` id instead of reusing that Agent's session id, so it cannot collide with a Persona conversation or the same session id on another computer. Creation starts no model turn, conversation or to-do. `leaders.write` opens this create and the progress write only; `leaders.read` remains the scope for reads and events, and neither scope implies the other. The local equivalents are `personad create-direction` and `personad write-progress` (`docs/integrations/script-cli.md`).
  This is the **reporting level**, not outside leadership: the referenced Agent is not made the direction's leader or supervisor, receives no supervision messages, and its linked to-dos are not newly aggregated under the direction. A targetless direction is valid too. The optional Agent pair is navigation metadata. Only a direction with a Persona conversation can be chosen to supervise an Agent. If a task already records an outside direction as its supervisor, the main chat receives its review reminders and the task keeps its recorded supervisor and review permissions. A temporarily offline supervisor conversation keeps its own queue. The direction record, outside marker and current-leader membership commit together (manager table `assistant_outside_directions`), and survive ordinary settings rewrites and manager restarts.
- **What a leader gets**: its context identifies it as a direction lead. Its `task_plan` work, direct starts and task hand-offs are supervised by that lead from creation, including ordinary signals, question/permission/plan cards, failed starts, `task_start_screen`, `task_start_keys`, `task_retry`, `task_review` and cancellation. Another side conversation has no authority over them. Main may explicitly select a current lead with `task_plan(supervisor=...)`; otherwise main-created work stays with main. Ordinary side conversations still use main. A persisted `supervisor_source: "plan"` distinguishes current planned ownership from old unmarked ledgers.
- **What goes to main**: main’s own tasks, a lead’s explicit `needs_user` escalations, the open tasks of a direction that ended ("Opening one" above), and tasks handed over once when a side conversation is archived or gone (#13657). An ordinary signal does not change recipient merely because a task waits for the owner. A temporarily unavailable assigned lead keeps its queue. Main keeps a read-only overview of tasks supervised by other conversations and cannot call `task_review` on them.
- **Transfers and cards** (#13344): `agent_transfer` persists `supervisor_source: "explicit"`. Immediate signals, queue rechecks, retries and restart recovery honor the new supervisor. Supervisor-held unanswered question, permission and plan cards rebuild their route, including cards already delivered to the former supervisor; owner-escalated cards retain their state. Repeated transfers create no signal, queued asks are reused on restart, and moves never extend approval deadlines. Current assigned leads receive cards directly; timeout handling does not turn main into another worker supervisor. The lead uses `ask_escalate` when an owner decision is needed.
- **Repeated final failures**: the first final failed start is immediate. For different tasks of one current manual leader with the same canonical computer, actual launcher and whitespace-normalized reason, later final failures accepted within 30 minutes wait in the versioned `assistant.supervision_failed_batches` manager setting and join one supplementary digest at the fixed boundary, with every affected task listed. A new attempt of an already notified task, a remediable kept tab, and main or ordinary side conversations stay immediate and ungrouped. Unknown machine, launcher or reason fails open to the old per-task behavior. Deferred members recover after a manager restart; accepted relay members stay the relay's responsibility.
- **Depth cap**: an agent linked to an open task (a worker) cannot call `task_plan` or `agent_start`, even when it runs as an assistant conversation: it is told to do the work itself with its in-session subagents and to ask its supervisor (`report_to_supervisor`) if it needs another task. Coding agents never had those tools, apart from a task leader's restricted `task_plan` ("Task leaders" below); the main chat and leaders are not limited.
- **In the clients** (#4331): the sidebars call leaders **常驻** (en "Pinned"; the code, API and events keep leader / direction) and list them under Persona chats after the main chat, each by its direction's name (on the web the conversation's header says the same name, not the hub's `MMDD | kind | ` title, and so offers no in-place rename of it: ⋯ Rename renames the conversation's own title, as the row's menu does; `useDirectionName`, `web/src/lib/direction-names.ts`) with the direction tree's Working and Waiting on you counts ("The direction tree"), followed by Recent, the most recently active other chats whoever opened them, the ones Persona opened with their source's mark (#4873, #5195; `docs/protocol/protocol.md` "Sidebar (same on every client)"); a conversation's menu has **设为常驻** / **取消常驻** (`…/make` / `…/end`). The clients read the tree again on its `directions` message, so an open sidebar follows a leader made or ended anywhere at once (a sixth leader once stayed hidden until a reload: the clients had read the leaders only when they loaded).
- **Quiet by default** (#6595): a leader's patrol (定时巡检), supervision (督办) and relayed (`conversation_send`) rounds follow the main chat's folding and visibility rule (`docs/assistant/assistant.md` "Background rounds"), under the same `assistant.main_background_folded` switch. A completed final reply stays visible unless the round finished silent without a user-visible action. The owner can open quiet rounds to inspect the transcript. The hub's interject note (`prompts/assistant/interject.md`, title `插话`) never renders as an owner-bubble quote.
- **Scheduling**: a leader is woken by its supervision messages; for a standing loop over a to-do pool, give it a recurring `schedule_followup` (`every`).
- **Compaction** (#15318): those wakes keep a lead from being idle long enough for the idle compaction, so on a launcher Persona manages it is held to the main chat's compaction window (`assistant.main_compact_window`, 400,000 tokens by default) as the main chat is; a lead that ends goes back to the launcher's window from its next message (`docs/assistant/assistant.md` "Compaction").
- **Protected while it leads** (#11392): a current leader's conversation is kept on its computer's session list however long it is quiet (`keptSessions`, on the computer its direction record names, else the assistant's), like the main chat and the background supervisor, so a direction left alone for a week keeps its Pinned row and its tasks' supervision messages. Keeping the row listed does not keep its transcript file. It cannot be deleted while it leads: `DELETE /api/v1/assistant/conversations/{id}` answers 400 `conversation_protected` with a message that says to unpin it first; once unpinned it deletes as any other conversation. The direction tree still lists a direction whose conversation is not listed or was deleted, with its name and tasks.
- **Approving the list** (#11392): approving an assistant-proposed `assistant.leaders` compares the whole current list with the one the card was raised against, not the card's text (which clips both lists to 120 characters, so a pin or unpin after the third id went unseen): any difference refuses the approval as stale and changes nothing.

### Background supervisor (#4927)

Direction leads supervise their own newly dispatched tasks, and main supervises its own work (#13362). No new background supervisor or task stream is created; an existing background conversation only finishes work already assigned to it. Pending and queued legacy notices move to the main chat without loss. The rest of this section documents that compatibility behavior, not a second default supervisor. Code: `server/internal/assistant/supervise_background.go`.

- **What it is**: a legacy assistant conversation on the assistant's computer, listed however quiet (`keptSessions`); no new one is started. Its id remains in the assistant's settings (`background_supervisor`, with `background_supervisor_machine_id` and `background_supervisor_at`; read with `GET /api/v1/assistant`, never written by `PUT`). It is not in `assistant.leaders`, is not a direction, takes none of the 20 leader slots, and cannot be archived or made a leader or a direction. Its context explains that only old work remains (`prompts/assistant/context.md`).
- **The legacy setting**: `main_supervises_own_tasks` remains in `GET/PUT /api/v1/assistant` and the clients for compatibility. It no longer chooses a supervisor for new work: the planning lead or main supervises it, and the background conversation only finishes existing assignments.
- **Keeping the legacy context** (#7315): `background_supervisor_keeps_context` ("The background supervisor keeps every round in context": `persona_settings` key `assistant.background_supervisor_keeps_context` only, not yet in `GET/PUT /api/v1/assistant` or the clients), also off by default, brings back the one long conversation of before #7315 from the next round ("Each round starts clean" below): the next supervision message asks for no round, so the node replaces the idle round's process, which had only the round's tools and was never compacted when idle, by a plain resume of the latest round with every tool and the usual idle compaction. While a round's process still works (answering, a turn Claude Code started, running background tasks, compacting, or a message Claude Code queued), and for a second after that, its row says working: the manager keeps the supervision message in its persisted relay queue and sends it once the process settled, so it replaces the process with every tool. The owner's message, which no queue holds back, waits up to 30 s for a working round's turn and then goes into a plain resume with every tool; past that, or with only background tasks running, it goes into the round's process as Claude Code queues it and runs once on the round's tools, that process is let go once it settled, and the next message starts with every tool ("Each round starts clean" below).
- **Each round starts clean** (#7315, `server/internal/assistant/supervise_round.go`, `server/internal/node/assistant_round.go`): it is still one conversation with one id, because the clients' row, its rights over the tasks (`backgroundSupervises`), the announcement's receipt and the persisted relay queue all key on that id. But every supervision message to it is a **round** that resumes at the end of the conversation's first turn instead of after the round before: the manager sends it with `supervise_round` (only when the text carries the round marker and the switch is off, so a message queued before a restart or a change of the switch is decided by its text and the switch when it goes in), and the node stops the idle process and starts `claude -p --resume <id> --resume-session-at <uuid of the start turn's last end_turn line>`. The model then reads the system prompt (with the static supervision instructions appended, `prompts/assistant/supervision_instructions.md`: a round's process starts with them as any of its processes does), CLAUDE.md, the start brief and its answer, the whole live snapshot (the node forgets what the conversation had seen) and this round's message, not every earlier round. The message is rendered Fresh (`prompts/assistant/supervise.md`, `docs/assistant/prompts.md`): like every supervision message it carries the task facts and no handling rules, and it adds a line saying that what crosses rounds is on the tasks (brief, scope changes, decisions, the last review; `todo_get`), the marker line `<persona-supervision-round />` and each task's last review (verdict, time, reason or note in 200 characters), so a round does not ask or send back again what an earlier one settled. The round's process lists only the tools supervision uses: Persona's own MCP server, started with `PERSONA_TOOL_SET=supervise` so that `mcp.tools` narrows the list to `roundToolNames` (with `task_decided`, which a `decision_reply` entry asks for, and `todo_list`, where the snapshot sends it for what it leaves out; no phone, mail, inbox, view, model, settings, leader or direction tool and no other to-do writing; no `ask_user`, `conversation_send`, `task_start_screen`, `task_start_keys` or `task_retry`; no extension server, `--strict-mcp-config`), and the built-ins `Bash,Read,Write,Skill,ToolSearch`. The supervision instructions' background line and `roundToolsWithheld` keep the instructions and a round's tools in step: every tool the instructions name that a round does not list is withheld there with why, the background line sends that work elsewhere (owner decisions are `task_review` conclusion cards, never `ask_user`; `failed`, `started`, `escalated` and `leader_stuck` go to the main chat), and every tool a round's own message names, for any signal it gets, is listed (`TestRoundToolsAgreeWithSupervisionInstructions`, `TestRoundListsTheToolsItsMessageNames`). The descriptions of a round's tools and the background supervisor's context name the others only in passing: a stopped Agent's answers (`agent_send`, a failed resume, `agent_interrupt`) send its replacement to `task_plan` with `continues` (the descriptions of `agent_send` and `task_review` still name `agent_start` beside it), and the owner's decision to `task_review needs_user` beside `ask_user`, and the background supervisor's snapshot leaves out the main conversation's plan cards waiting for Start, which only the main conversation withdraws, the owner's unread chats and the iPhone line (`backgroundSnapshot`; `TestAStoppedAgentsAnswersOfferARoundItsTools`, `TestBackgroundSnapshotLeavesOutWhatOnlyMainActsOn`). The node does this only for Claude Code on the CLI, 2.1.322 or later (the version `--resume-session-at` was measured on), an idle process (no turn, background task or compaction running, no message Claude Code queued or has not reported on yet, and no output for a second) and an anchor on the transcript's latest chain; anything else (Codex, a busy process, an older Claude Code, no anchor, a node from before #7315) goes into the long context as before, and a round whose process does not start (Claude Code refusing the anchor) is sent once more as before. The message is the same round render either way: there, too, it carries the marker, and its header line says earlier rounds are usually, not always, out of view. Its answer says which (`context: "fresh" | "kept"`, `why`, `transcript_bytes`), and the manager logs one line per round. A round's process is never compacted when idle (that would cut the first turn off the chain). Every send says which tool set it needs: `supervise_round` asks for the round's, and any other message (the owner's own, a supervision message with the switch on, one from a manager before #7315, a round the node keeps as before for another reason than `busy`) for every tool. The node compares that with the set the process was started with. A round's process that has settled (no turn, not even one Claude Code has only just started, no background task, no compaction, the user's included, no message Claude Code queued or has not reported on yet, and no output for a second since the line reader was last done with a line, `assistantSettle`) is replaced by a plain resume of the latest round with every tool, compacted when idle like any other process. The last look and the stop are one step (`stopSettled`): taken under the process's lock, which the line reader takes to mark a turn, while the conversation's send lock keeps any send from starting one, so a turn that starts meanwhile is never cut; a fresh round stops the process the same way. Until it settled, a round's process reports its row as working, so the manager keeps supervision messages for it in its persisted relay queue (a wake while a round runs merges into the queued message) and sends them once it settled; the node keeps no queue of its own. A message that needs every tool and comes anyway (the owner's, or a relay racing the row) while the round's process runs a turn waits for that turn up to 30 s (`awaitRoundTurn`, `roundTurnWait`), then goes into a plain resume with every tool; a Stop meanwhile ends the round at once and the message goes in once that process exited, never into the one going away (`stopAwaited`). Past 30 s, with only background tasks running, or as Claude Code starts a turn of its own after the wait looked, it goes into the working process as before: as Claude Code queues it (the owner's, on a background round, preempting it as `server/internal/assistant/owner_preempt.go` says), it runs once on the round's tools, and the process is marked to retire and let go once it settled (`roundSettled`), so the next message starts a plain process with every tool. A round kept as `busy` joins the process as the round's own and does not retire it. So the owner's own message between rounds plain-resumes the latest round with every tool, and the next round starts from the first turn again.
- **New task ownership**: a new task’s ledger names the planning direction lead, or main for other conversations, and its `origin` stays the chat that planned it. An old ledger that explicitly names the configured background conversation is left there during rehome so that conversation can still accept its old task. Ordinary signals follow the task’s assigned supervisor; `ownSupervisor` is the default for main-owned work. Only signals already queued with an explicit legacy background target may still go to that conversation; if it is gone or has moved, they are regathered or moved to main. Task cards, the dock, receipts, to-do events and the direction tree therefore keep one stable owner instead of pointing at a replaceable background conversation.
- **Routing** (`digestTo`, `server/internal/assistant/supervise_leader.go`): ordinary signals for new work go to its planning lead or main. An explicit transfer overrides that default for all ordinary signals, including failed starts, retried starts and signals while the task waits for the user. Explicit owner-decision `escalated` notices go to main; ordinary `leader_stuck` notices follow the assigned supervisor. Without an explicit move, existing manual-leader and legacy background compatibility routes retain their rules. Legacy side-supervised open tasks are rehomed to main, and a queued message for a retired background conversation is regathered or moved to main rather than dropped.
- **Gathered but not yet delivered**: `assistant.relay.supervise_pending` is the durable outbox for signals before they enter a conversation's persisted relay queue. It is restored only after the to-dos load, so an unread task list cannot invalidate a signal. Direct delivery or queue admission acknowledges the exact signal id and stretch key; a newer stretch is never acknowledged by an older delivery. Reviewed, answered and `/stop`-silenced signals are removed. This at-least-once hand-off, together with supervision deduplication, preserves a pre-digest `needs_user` even though the watch intentionally produces no replacement signal in that state.
- **What it may do**: `task_review` (accepted, rework, cancelled, needs_user, parked), `task_scope`, `agent_read`, `agent_send` (as the task's supervisor, not as a side conversation; an explicit send to an accepted task's agent reopens it under the same rules as the main supervisor, #7539), `interaction_answer` and `ask_escalate`, only for an old task whose ledger names the background conversation or for which a real old notice was delivered or queued to it (`backgroundSupervises`; that grant is kept in `assistant.relay` across restarts). Merely being configured gives no authority over newly assigned work. The main chat may still review or message any task.
- **What the main chat gets**: an accepted task takes no main-chat turn: the conclusion card goes to the owner as before, the review is an entry in the chat activity of the chat that planned the task, and the outcome is in the main chat's next snapshot. A `needs_user` sends the decision card as before and the `escalated` entry a leader's sends, which is a supervision message to the main chat (folded, not pushed); one that can wait (`can_wait`, `docs/assistant/todos.md` "Decisions that can wait") sends that entry only when it is pushed after all, as a leader's does. A concern's outcome is still a main-chat turn. A failed start is ended only by the turn of the conversation whose supervision message carried it (the main chat's or the task's current supervising conversation's): a background supervisor's or another conversation's turn that ends meanwhile does not give the start up (`remedyGoesTo`, `server/internal/assistant/start_remedy.go`). The answers of `agent_supervise` and `agent_send` with a task say who follows the task up, as `task_plan`'s does.
- **Legacy announcement**: if an old background conversation still starts, the main chat's context says only that its previously assigned tasks continue there and new work stays with main. Delivery receipts for that line retain the existing `background_supervisor` item id and acknowledgment behavior.
- **Model**: purpose `assistant.supervise`, which follows Main chat until it is given its own (`docs/assistant/model-config.md`). It applies when the conversation starts and on every message sent to it.
- **Gone or unable to start**: a legacy background conversation that is gone (its connected computer lists the assistant conversations without it, after a 2-minute grace) or left on another computer is not replaced for new work. Its pending signals are told again to main, and a queued message whose signals are no longer in memory moves to main intact (`backgroundLost`; the watch loop also checks this with `backgroundStranded`). An existing conversation may finish only the notices already assigned to it. No legacy signal is dropped. A legacy round that reports `no_anchor`, or a restarted round whose transcript exceeds `backgroundTranscriptLimit` (32 MiB), is likewise treated as unable to continue: at the next idle digest its queued legacy work moves to main, without archiving the conversation or creating a replacement.
- **Its own queue and its rounds**: the owner comes first there as in any assistant conversation (`docs/assistant/assistant.md` "Messages from the system wait for the owner"): what waits for it goes in by rank, and the owner is present there only for 2 minutes after a message they wrote to it themselves, so their talking in the main chat holds nothing that is for the background supervisor. In the availability metrics its rounds are `supervision` rounds with `main: false`, the one its start begins included (`availStartedBy`); a task notice delivered to it is a `notice` round as anywhere.
- **Pushes**: its turns are held from the pushes as any supervision round is (a normal finish is not pushed; a failed turn is). It counts as an assistant conversation everywhere else: not triaged, not in the dock's agents, no badge; a round of it shows as "assistant answering" on the Live Activity, as a supervision round in the main chat does.
- **Its reviews say so**: the `review` entry of a review the legacy background supervisor wrote carries `by: "background"` (`reviewFeedBody.By`, #5084); ordinary planning-chat, leader and main-chat reviews have no `by`. Such a review took no turn of the chat that planned the task, so a client may keep the entry in that chat as the only record of it there. Historical sampled judge reviews remain readable but no new ones are written (#5732).
- **In the clients today**: it has `origin: "task"` with no `origin_ref`, so the apps list it where they list the scheduled tasks' conversations, titled "Background supervisor" (zh 后台督办, ja バックグラウンド監督). The web (#5082, `docs/web/web.md` "Background supervisor") marks the conversation built in with a menu that only copies its id, has the switch in Settings › Assistant, lists the model purpose after Main chat in Model settings, and drew each `review` entry marked `by: "background"` as one line in the main chat until #8237 (a review is no line in the chat since: the task's row says how it went). So do the Apple apps for the setting and the model (#5083, `docs/apple/ios.md` "Background supervisor"): the switch in Settings › Assistant, the model use in Settings › Models, and no Archive, Pin or End on the conversation's row; and Android, as the next entry says. Its Fresh rounds still show in that one row in file order: every round hangs from the first turn, and the transcript reader keeps a marked round in view instead of taking it for a rewind (`docs/protocol/protocol.md` "Continuity of items").
- **On Android** (#5084, `docs/android/android.md` "The background supervisor (#5084)"): the same rule, in that app's drawer: it has no group of its own for scheduled tasks' conversations, they are under All chats with every other conversation Persona opened itself, and so is this one; never under 常驻 or Recent, named by the app and without Pin, Archive or End chat. Settings › Assistant has the switch for `main_supervises_own_tasks` and a row for the `assistant.supervise` model, which Settings › Model configuration lists too. A `review` entry marked `by: "background"` is one line in the conversation that opens the task; in Brief and Board the others stay in the background record as before, and Glass draws every review (`docs/plans/android-ios-alignment/chats.md` C13).

### Task leaders (#3656)

The depth is dot (the main chat or a leader conversation) → task leader → worker. Every newly planned task explicitly declares `role: "worker"` or `role: "leader"`; a missing or unknown role is refused before anything is planned. Only a leader changes the ordinary worker behavior. A role-less task in a legacy stored plan remains an ordinary worker and can still start or retry.

- **Marking one**: `task_plan` requires `role: "worker"` or `role: "leader"` per task. Any conversation may choose leader (its own reports need a supervisor to reach); a task leader planning another leader is refused before anything is planned. The role is part of the saved plan task, so a retried or restarted start keeps it, and the apps' card edits never change it. When the agent is linked, its `todo_agents` row gets `source: leader`; no migration, as `source: review` marks a reviewer. Its `agent` snapshot comes from the start answer's `session.agent`, else the explicitly resolved launcher kind. The leader Agent’s own task is supervised by the direction lead that planned it, or by main for main-created work; its `report_to_supervisor`, reviews and cards follow that assignment. Its workers are supervised by the leader Agent.
- **Adopting a leader** (#13357): The main conversation or a direction’s lead can use `agent_supervise` with `role: "leader"` on an Agent with no open task. Adoption keeps the existing session and its permissions, requires a checkable `done_when`, and refuses a work-trust computer. The Agent is linked with `source: leader` before it receives the handoff and leader notes. A Codex terminal session gets the node’s executable for `tool task_plan` and `tool task_review`. A delivery error identifies the adopted task; repeating the call retries the note on that task. Open workers keep their role. Moving another conversation’s task still requires `agent_transfer`.
- **What a leader gets**: its first message says it leads and how its workers reach it (`prompts/agents/task_leader.md`; a leader whose verified entry is the terminal one, with no Persona MCP, is also told to run `tool task_plan` / `tool task_review` through the executable its handoff note names, #12136), and the MCP tools list (`mcp.tools`: the node names the agent by its pane, `node/hooks.go`) adds a restricted `task_plan` and `task_review` (`mcp/leader/`, listed under those names). Discovery is refreshable: `personad mcp` advertises `tools.listChanged`, and task-link changes wake a cursor-based `mcp.tools` long poll. Only a changed tool list sends `notifications/tools/list_changed`. A registration after the first list is therefore not limited by a startup wait window. The node re-resolves the calling pane on each request, discarding the old cursor when the session changes; a 30-second heartbeat also recovers late identity. Watches have a 45-second client deadline, retry failures with 1–30 second exponential backoff, keep the last list during an outage, and do not prevent an idle manager restart. A fresh manager cursor forces a new snapshot after restart. The manager, node and the new session's MCP binary must all support this protocol; older nodes/managers retain one-shot discovery. Other computers require their own node upgrade. A continuation uses the new saved plan's role when linking its replacement Agent: `todo_agents.source`, not a role column on the original to-do, is authoritative. The list is only the listing: `mcp.call` runs only for the agent of an open task whose row says `leader` (`leaderTaskPlan`, `leaderTaskReview`, `server/internal/assistant/task_leader.go`); any other agent gets "unknown tool", a worker is told why for `task_plan` (the depth cap below).
- **What it may plan**: new worker tasks beneath its own open task, nothing else. The arguments are read strictly: `todo_id`, `attachments`, `project`, `user_requested` and any other field outside the tool's schema are refused. `role` is required and must be `worker`, so the call cannot take over or reuse another to-do or make another leader; every task is a worker. `machine` and `cwd` default to the leader's own computer and directory; `done_when` is the worker's and `expect_minutes` is an optional override of the automatic estimate. At most 8 workers of one leader are open at a time. The workers start at once: there is no plan card and no Start for the user, no push, no feed item in any conversation.
- **A worker is a supervised task whose supervisor is its leader's agent**: its to-do has the leader's agent session as the ledger's `supervision.supervisor` (what the to-do API shows, kept across restarts) and its `done_when`, and the leader's session as `origin`, so no conversation shows it. Its agent's row has `source: worker` and `via` the leader's task id, with `agent` from its own start answer's `session.agent`, else the explicitly resolved launcher kind; a leader started again has a new session, so the current agent of that task is the one reached. The supervisor is told apart from a conversation by being a leader agent's `machine/session` (`isLeaderRef`; both, since a session id alone is not unique across computers, and a worker's to-do `origin` is the same reference): `digestTo`, which would send anything else to the main chat, is never asked. A leader started again on another computer is reached through its task's current agent. The done judge does not close it, as for any supervised task, and there is no spot-check close path (#5732): it waits for its leader's review. A worker's own question cards stay the user's (`fleet_ix.go` does not route them up the conversations' ladder in `ask_route.go`, and upgrading worker permission or plan cards to the leader is another task); a failed start of a worker has no remedy tools (they are the main chat's) and is told to the leader. The leader's own `report_to_supervisor` goes to its own supervisor, as always.
- **Signals and reports**: every signal about a worker, the worker's `report_to_supervisor` included (`urgent: false` too: a task leader has no briefing to wait for), is one message into the leader agent's session on the leader's own computer (`sendDigest` hands them to `deliverToLeaderAgent`: gathering, deduplication and spacing as for a conversation, `session.send` so it queues behind a running turn and resumes a closed agent), starting with "Persona · supervision" (`prompts/agents/worker_supervise.md`): what stopped, stalled or waits for review, its `done_when`, what it said and its evidence. **Nothing is dropped.** A failed send puts the signals back for the next digest. A leader agent that is not reachable (its row was released) turns each signal into a `decide` entry for the conversation above the leader, naming the worker (so that conversation can `task_review` it) and saying its task leader cannot be reached; a closed leader agent whose background resume fails is escalated the same way and its message is retried on its own context, with a growing wait, while the leader's task is open, whether or not anyone is above it. So is a listed leader agent that has stopped (read-only, #4754), whose node refuses the send with `agent_stopped` (#8518): the conversation above hears of it once, and the leader is tried again with the growing wait, not at every digest, so a leader the user resumes still gets the message. **A leader whose own task closed** leaves its open workers to the conversation above it: `rehomeLeaderlessWorkers` (every digest and the supervise loop, idempotent) moves the ledger's `supervisor` and the to-do's `origin` together to that conversation (its notices, receipts and listings follow the origin), sends a review signal for a worker that waited, and with no conversation above cancels the worker with a reason. A worker's own `report_to_supervisor` in that window rehomes it first, so the report is told to the conversation above, or the worker is told it was cancelled; it is never answered "nobody receives this" and lost.
- **Reviewing**: the leader's `task_review` takes `todo_id`, `verdict` (accepted, rework, needs_user, cancelled), `note`, `checks`, `reason` and `recommend`, and reaches its direct workers only (the ledger names this leader; its own task, any other task and a worker's siblings are refused). accepted closes the worker by review, cancelled closes it without immediately stopping its agent, rework sends the gaps to the worker (two rounds, then it goes up); no conclusion card, "Please verify" or push goes to the user. needs_user goes to the conversation that handed the leader its task, as the signal `decide` ("它的 leader（一个编码 Agent）判断要用户决定", with the leader's reason and recommendation); that conversation asks the user or decides, and reviews the worker with its own `task_review`, which does not take the worker over. The leader cannot message a worker or read its transcript: for a worker that did not start or is stuck it plans a new worker and cancels the old one.
- **Permissions**: a task that is not read-only (`readonly: false`) takes the default mode as elsewhere (the owner's default, else the launcher's). Claude-family launchers always start in `bypassPermissions`, for both read-only and writable tasks, and so do Paseo providers whose approval is Auto Accept (their launcher says `auto_accept`, #8141); an agy worker starts in it too when its task names no mode, and keeps a mode the task names (`docs/integrations/launchers.md`, "Work agents"). Whether a launcher is Claude-family, or such a provider, comes from the computer's launcher list; when that list can't be read in time (a busy computer), the last list the hub saw there decides, so a Claude wrapper script does not fall back to `default` and stop on its first Bash approval (#7376). Codex read-only tasks keep plan mode and the read-only sandbox. TRAE CLI and omp take no permission mode or effort (the node refuses a start that names one): the hub sends neither, so a writable task runs without the default mode or its tier's effort and a read-only one relies on the first-prompt constraint alone; a mode or effort the call names for them is refused when the task is planned, and an effort `agent_start` names is refused before anything starts (#7559). The work agents pi, cursor and agy (`proto.WorkAgentTypes`) are started the same way, but the node ignores a mode or effort for them rather than refusing the start (#8151), so the hub drops one the call names instead of refusing it. Every other agent (Codex, a Paseo provider, one the hub can't name) keeps what the start sends, for the node or its runtime to judge (`assistant/launcher_modes.go`). If a read-only task is already in plan mode, its `ExitPlanMode` request is denied automatically before a card is registered, with instructions to return findings and report completion to its supervisor (#3985).
- **Work-trust computers**: a `role: "leader"` task is refused with a readable reason at planning, at Start, on a retry, in `startTaskAgent` (so a remedy or recovered start) and in the health detour (`healthyPlace` never moves a flexible task leader there): a work-trust computer's agents get only the reporting tools, so the role could not work, and its privacy boundary is not widened.

### Separate sessions within a supervised task (#5751)

**Audience: coding Agents and their supervisors.** A supervised Agent can hand part of its existing assignment to a
separate session with `task_spawn {title, prompt}`. Codex and other pane-based Agents without Persona MCP can use the
same tool through the local node, with no hub credential:

```sh
personad tool task_spawn '{"title":"Check the regression","prompt":"Inspect the tests and return evidence."}'
```

- **Ownership**: the node resolves the Agent currently in the calling pane. For Codex shell tools, the runtime's
  per-turn `CODEX_THREAD_ID` takes precedence over a shared daemon's potentially stale pane environment: it must
  match exactly one thread in the node's registry (ambiguous or unknown IDs are refused, not matched by directory).
  The manager verifies its active link to
  an open supervised to-do, starts the child on that computer in that directory, and records `source: spawned`,
  `via: <parent machine>/<parent session>` on the **same to-do** before returning the child, to-do and supervisor IDs.
  The child participates in the existing task's progress, completion review and retirement. No new task, task-leader
  role or supervision level is created. An ordinary worker still cannot use a leader's `task_plan` or `task_review`.
- **Scope**: only `title` and `prompt` are accepted, and the assignment must remain within the current task. Task,
  supervisor, parent, machine, directory and permissions cannot be supplied by the Agent. The current launcher and
  effort are reused. For the model, an original plan assignment that names one wins for its own Agent while it still
  runs on that assignment's launcher (the default launcher when it named none), instead of carrying over a model
  switched in the app after the start. Otherwise a managed parent's `requested_model` supplies its exact launch ID;
  an explicitly empty value keeps the launcher's own default. For older nodes and observed sessions, an exact offered
  ID is kept. A reply that uniquely resolves one concrete offered ID uses that ID. An ambiguous, unmatched or unknown
  catalog keeps the reported model (#10557). Permission comes from
  the live parent, or its persisted original plan assignment when Codex does not expose it; unknown permission stays in
  plan mode, never a writable owner default. Another spawned session with unknown permission cannot borrow the original
  Agent's plan permission.
- **Failure**: an unbound pane, unrelated Agent, completed task, guest or work-trust computer cannot dispatch this
  way. A known Codex thread needs no pane. The terminal entry also runs the Agent's other own tools (`callerTools`,
  #12096): its to-do's `todo_decisions`, `todo_decision_add`, `todo_bind_thread` and repository tools,
  `report_to_supervisor`, dispatch, and a task leader's `task_plan` and `task_review`, each decided by the hub for the
  resolved session as over MCP ("Whose call it is" above). A call whose pane or Codex thread names no known Agent
  session is refused as an unrelated process (`这个命令只能在助手的对话里用`, unless it is the assistant's own process),
  and every other `personad tool` call retains the assistant-only connector gate.
  If ownership changes while the child
  starts, the manager refuses the link and asks the node to stop the child; the error names its session and stop
  result. An uncertain start must not be retried automatically.
- **What the Agent is told** (#12136): the assignment's handoff note (`prompts/agents/task_handoff.md`) names this
  entry by the node's verified executable, not a bare `personad`, and the Agent's own tools it also runs, each as
  `tool <name>` and one JSON object: `todo_decisions` with `{}`, `todo_decision_add`, `report_to_supervisor`. It says
  the entry is not a general one: tools that are not the Agent's own are refused. A task leader's note adds `tool task_plan` / `tool task_review`
  beside the MCP names; a worker is never told it can plan. An Agent with the Persona MCP, or whose executable could
  not be verified, gets the text it got before. Wherever an entry is offered, MCP or terminal, the note opens with
  the handoff's terms (#12995): it hands only a separate part of the assignment to a new session, never all of it,
  and the caller does the rest; that session works in the caller's directory with its permissions and reports to the
  supervisor, not to the caller, so the caller leaves the files it changes alone and does not poll it, read its
  transcript or wait for it. An Agent with no verified entry is told to continue the work itself, as before.
- **Runtime new-conversation tools are different**: a standalone Codex `create_thread` can be recorded as
  `thread_source: user` with no parent. Persona cannot securely infer its task from a matching directory, title or
  returned thread ID. Use `task_spawn` for supervised handoffs; ask the supervisor for unrelated work rather than
  silently opening an unmanaged conversation. Existing unmanaged conversations are not retroactively adopted.
- **Native sidecars**: Codex `source.subagent.thread_spawn.parent_thread_id` / `parent_thread_id` establish session
  ancestry (`is_subagent`, `parent_session_id`, `root_session_id`). The web folds known children under their parent,
  and the task tree can show those helpers. This is display/runtime ancestry, **not a `todo_agents` link**.
  Automatic task links require the existing `persona_from` / `spawn_from` marker, a linked workspace or the botmux
  dispatch relationship (see [Following spawned Agents](todos.md#following-spawned-agents)). Native sidecars without
  such a link remain bounded in-session helpers, not independently reviewed Persona tasks or leader workers.
- **Credentials**: assignment prompts explicitly forbid reading `~/.persona/hub.json`, extracting its hub token or
  calling privileged `/api/v1` endpoints to manage tasks. A refusal is not permission to bypass the scoped tool.
  The existing [agent sandbox](../server/agent-sandbox.md) can mask those credential files for supported starts,
  but is opt-in and does not wrap an already-running external Codex daemon. This change neither enables it nor
  claims an OS security boundary for unrestricted processes running as the hub's user.

#### Standing authorizations

The manager keeps explicit user grants in its Postgres settings key `assistant.standing_authorizations`.
The main conversation reads and changes them through `persona_settings` with that key. A change requires the
user's own permission-card approval; an Agent cannot turn its own quoted authorization into a grant. An unchanged
list asks nothing. The setting value is `{revision, authorizations}`; writes include the revision just read and the
whole list. New entries omit `id` and `granted_at`, which the server assigns. Existing IDs are immutable: changing a
scope means removing its entry and adding a new one. A stale approval cannot restore a grant revoked meanwhile.

Each authorization has `id`, `machine_id`, `directory`, `actions`, `conditions`, `quote`, `source`, `granted_at`
(RFC 3339), and `reversible`. Actions are `merge`, `push`, `deploy-test`, `deploy-production`, `send`, `delete`,
and `spend`. Conditions retain the user's wording; `ci_green` and `no_conflicts` have independent runtime checks
(see [Agent work](agent-work.md#standing-authorization-answers)). The original words and source remain inspectable.
Directories are specific absolute paths on one computer, matched by path segment: `/code/example` includes its
subdirectories but not `/code/example-copy`. Roots, single-level paths and wildcards are rejected. The node verifies repository identity through local Git metadata: matching common Git directories cover linked worktrees outside that path.
Origin URLs are descriptive metadata, never authorization evidence: an arbitrary `git init` can copy a remote URL.
Independent clones require separate registration. A subdirectory grant retains its relative directory
boundary across worktrees. Different common Git directories and similar path prefixes do not match; repository basenames never
establish identity. If an older or unavailable node cannot return identity, only the exact registered directory
matches. A child directory may contain an independent repository, so a path prefix alone cannot extend a grant.

At actual `task_plan` dispatch and `agent_send` with `task`, the manager reads matching grants and appends the prompt
template containing scope, actions, conditions and the user's words. No other repository receives them. Pending plans
read again when started, and a failed lookup stops dispatch. Ordinary chat messages are unchanged. Explicit task
limits and the read-only prompt constraint remain stronger than a standing grant. Revocation affects future dispatches and answers;
it cannot undo an instruction already delivered or work already executed.

App API (the conversation's manager, with full-access role):

| Method | Path | JSON response |
| --- | --- | --- |
| GET | `/api/v1/assistant/standing-authorizations` | `{"revision": 1, "authorizations": [{"id": "sa_example", "machine_id": "example-node", "directory": "/code/example", "actions": ["merge"], "conditions": ["ci_green", "no_conflicts"], "quote": "Merge after checks pass without asking again.", "source": "Main conversation", "granted_at": "2026-11-11T07:36:00Z", "reversible": true}]}` |
| DELETE | `/api/v1/assistant/standing-authorizations/{id}` | `{"ok": true, "revision": 2}` |

DELETE is idempotent; deleting a missing entry keeps the revision. A concurrent update returns 409 and the caller
refreshes before retrying; storage unavailable returns 503, and insufficient role returns 403. There is no HTTP grant
creation endpoint: additions use the main conversation's approval flow. The empty default list authorizes nothing.
The one other way a grant is made is the owner's **Always** on a suggested rule, below. Apple (#7002) and Android (#9247) list and revoke standing authorizations from Settings and the assistant's profile; the web app does too.

#### A rule after the same answer three times (#4929)

When the owner gave the same answer to the same kind of card for the same scope 3 times in a row within 14 days, the
next card of that kind and scope carries one more line, a `suggestion`, with two actions: **Always** makes the rule,
**Not now** hides the line for that kind and scope for 30 days. The line is computed by the manager with no model
call; nothing is turned on for the owner without the tap.

| Repeated answer | Counted from | Shown on | Always creates |
| --- | --- | --- | --- |
| **Start** on plan cards for tasks that change files in one repository (or, outside one, one directory) of one computer (the kind "Start These Directly" remembers) | Started plans made within the 14 days whose tasks carry `owner_started`; a dismissed plan starts the count again, and a plan the hub started without asking, or one its conversation withdrew (`task_plan_withdraw`), counts neither way | The next open plan with a task of that kind (`TaskPlan.suggestion`) | The `tasks.trust` entry, as "Start These Directly" does |
| The go-ahead to `merge`, `push` or `deploy-test` in one directory of one computer: Yes to a plain confirmation ("Merge the PR now?"), or a decision's option that is the action itself ("Merge") | The to-do's decisions of source `card` (a question card the owner answered) and those of source `manual` that have an `owner_decided` work event (the option of a `needs_user` decision the owner chose; the event keeps the decision's `why_owner`); a No to the same confirmation starts the count again | The next `needs_user` decision that offers that go-ahead (`ask.suggestion` on `GET /api/v1/todos`), or the next such question a supervisor passes up (`interaction.suggestion`) | A standing authorization for that computer, directory and action, with the owner's answer as its `quote` (shown as `scope.quote`): the option they chose, after the question only when that is a plain confirmation ("Merge the PR now? → Yes"), never the free text an Agent or a supervisor wrote around a decision; a merge carries `ci_green` and `no_conflicts` |
| A question a supervisor passed up (`ask_escalate` with a recommendation), answered with what it recommended | The card's decision and the `ask_escalated` work event, which keeps the card, its kind and the recommendation | The next question a supervisor passes up, while `agents.delegate` is off | Nothing: the suggestion names the setting `agents.delegate` for the app to open |

- **Whose answers count**: only the owner's. A supervisor's answer, a standing authorization's, the delegate's and a
  default taken at a deadline are decisions of source `agent` and count nothing. `owner_started` is set by the plan's
  Start route alone; `task_plan` arguments cannot set it.
- **Never suggested**: `deploy-production`, `send`, `delete` and `spend`; a decision whose `why_owner` is
  `irreversible` or `visible_to_others` (the line is not shown on one, and the answer to one is not counted); a
  question whose text mentions force, production, release, deleting, sending or paying; tool permission cards and plan
  approvals (their answers are not counted either); the health space. A decision typed into a to-do by hand
  (`POST /api/v1/todos/{id}/decisions`) is not an answer to a card and counts nothing. An Agent's question card has no
  `why_owner`: for it the list of actions and the words above are the whole check.
- **The scope** of a go-ahead is the directory the task's plan named (the repository, whichever worktree its agent
  got), else its agent's directory; a directory an existing standing authorization covers, or a kind already in
  `tasks.trust`, is not suggested.
- **Always is the owner's own approval**, as Start is: it is an app route for a device with the full-access role, and
  no MCP tool reaches it, so a model or an Agent cannot make a rule this way. The manager counts again when the tap
  arrives and makes the rule only if the answers still earn it, so a made-up suggestion id makes nothing. The grant
  goes through the same checks and revision-checked store as every standing authorization.
- **Where the rules are listed and removed**: where their kind already is, `GET` / `DELETE /api/v1/task-trust` and
  `GET` / `DELETE /api/v1/assistant/standing-authorizations/{id}`. After Always the line also stays hidden for 30 days,
  so removing the rule does not bring it straight back.
- **Stored**: when each suggestion was last answered, in the setting `assistant.rule_suggestions`; no table of its own.
  The count is read at most every 30 seconds.
- **A card already rung** keeps the suggestion it went out with (a plan and a to-do's decision are worded each time
  they are read). When a suggestion is answered, the manager takes it off every pending card that carries it and
  sends those cards again, so the line is gone on every device.
- **Drawn by**: the Apple apps (#5802's Apple part, `docs/apple/ios.md` "Suggested standing rule and held decisions"):
  one line under the plan card, the to-do's decision and the question card, with what Always creates, Always and Not
  Now, and after Always where the rule is listed and removed. Apple lists them on the assistant's profile and in
  Settings (#7002), and Android lists them the same way (#9247); after Always, those apps say where that list is.
  Android draws the suggestion line too (#5802, `docs/android/android.md` "Rule suggestions, held
  decisions, recommended setup and purpose descriptions"). No event is sent for a plan when its suggestion is
  answered, so the Apple apps take the line off the plans they hold themselves (`TasksStore.ruleSuggestionSettled`).
- **Two taps on Always at once** make one rule: the grant is stored only if no standing authorization covers the
  directory and action yet, checked under the lock every change of the list takes; the second tap gets 404.

App API (full-access role; 403 otherwise):

| Method | Path | JSON response |
| --- | --- | --- |
| POST | `/api/v1/assistant/rule-suggestions/{id}/always` | `{"ok": true, "kind": "standing_authorization", "creates": "standing_authorization", "authorization": {…}, "revision": 2}`; for `plan_start` `{"ok": true, "kind": "plan_start", "creates": "tasks.trust", "key": "…"}`; for `delegate` `{"ok": true, "kind": "delegate", "creates": "", "setting": "agents.delegate"}` |
| POST | `/api/v1/assistant/rule-suggestions/{id}/not-now` | `{"ok": true}` |

404: the answers do not earn this suggestion (any more), or it was already answered. 409: the standing
authorizations changed meanwhile; read again and retry. 503: storage unavailable. The `suggestion` object is in
`docs/protocol/protocol.md`, "Tasks and the chat feed".

Clients (#5802): the web draws the line under the plan card, the decision's row and the passed-up question, with
what Always stores, the two actions and the result in the same place (`docs/web/web.md` "Suggested rules"); the Apple
apps (`docs/apple/ios.md` "Suggested standing rule and held decisions") and the Android app
(`docs/android/android.md` "Rule suggestions, held decisions, recommended setup and purpose descriptions") draw it too.

#### Asks go to the supervisor first (#3589)

Internal execution questions from `ask_user` declare `decision_scope: "internal"`. An owner decision instead carries
`why_owner` (the existing decision reasons) or `owner_action` (`personal_unlock`, `login`, `second_factor`,
`experience_check`, or `spending`). An unclassified call is rejected before publishing a card. A personal unlock stays
in the owner's own app: question cards never collect passwords or private keys. These rights are kept in the original
tool input, including `wait: false`, so restart does not change who may answer. Internal expiry authorizes nothing,
takes no recommended default and creates no owner decision. Internal held-card reminders emit only the work event and
supervisor signal; they never construct an owner notification. Ordinary owned-Agent sensitive delegation uses the same
reviewer gate; isolated-track boundaries still require the owner and external connector writes keep their own gates.

With leaders configured (`assistant.leaders` not empty), a supervised task's questions, permissions and plans go to its
supervisor first. Without leaders, a supervised task's parsed terminal dialogs (#11195) and non-six-class agent
permissions and questions go to its supervising conversation (the main conversation) through the same active `ask` route
(#16291), while six-class owner cards and plans go directly to the owner. Code: `server/internal/assistant/ask_route.go`.

- **What goes up**: a card raised by an agent of a supervised task (not waiting for the user, not closed): a question
  (not `ask_user`, not a form or a password card), a tool permission or a plan, and a parsed terminal dialog
  (`supervisedScreen`): an ordinary choice, or a command approval Claude Code asks on its own screen, warnings and
  countdowns included ("Dangerous rmdir operation on working directory or its ancestor": Claude Code asks it even in
  `bypassPermissions`, so no start mode avoids it). For a terminal dialog the supervisor receives the agent, machine,
  question, command, warning and option labels, and picks the option itself (`interaction_answer`: answers keyed by the
  question, or `allow` for Yes/No), kept as the assistant's answer and the task's decision; a dialog that trusts a
  folder, accepts Bypass Permissions, may delete files (`Risk: delete`) or acts in the owner's name is the owner's
  (`userScreen`), and only an explicit `ask_escalate` rings the owner about it. Unparsed screens and password or private
  input keep their existing owner route. Internal cards raised by a direction lead now go to the main conversation. The main
  conversation's own internal cards use an independent background reviewer; if none is registered, the manager starts
  that reviewer through the existing audited conversation lifecycle. Failure to obtain a reviewer closes the request
  unanswered. This does not grant a new task supervisor or authorize the source to answer its own card.
  An extension source label is not maintenance authority: routing and execution use the same explicit operation
  predicate. Unknown operations retain their owner gate. Before Assistant setup, manual node questions keep their
  existing interaction; manager-owned internal questions never use that fallback.
  `handNonOwnerCard` uses the active durable relay queue for internal questions, permissions and maintenance approvals:
  a busy or offline recipient is retried, delivery and failure are work events, and `interaction_answer` attributes the
  decision to the assistant. Sensitive delegation approvals are bound to the exact proposal, track and decision
  authority. Source membership is checked again after approval, and the target is looked up again before sending;
  a changed or unavailable boundary cannot reuse an internal approval. The interaction's expiry is never extended.
  Restart rebuilds its route and wake-up;
  stale relay generations cannot act on the rebuilt route. The card is shown on the apps
  marked `delegating` and rings nothing: no push, no bot card, no Persona Link notification. A card the supervisor
  holds (any kind but a question) is not the owner's: the apps count no session that waits only on such cards in Needs
  you (the hub's `supervisor_held` count) and offer no buttons to answer it (2026-11-23, D11 of
  `docs/plans/plan-derived-counts.md`).
- **Permissions without an open to-do** (#16093): An ordinary tool permission from an Agent gives the main
  conversation a turn when it is available. After a hold of up to five minutes, the next routing tick makes a still
  pending card answerable in the Agent's chat without a push, bot card or Persona Link notification. The hold ends
  earlier for a short-lived permission so there is time to answer. A private permission or one with very little time
  left goes straight to the app. Only a still-pending card can start a turn.
- **Who gets it**: the task’s assigned supervisor as an `ask` entry in its "📋 督办" message (the planning lead or main;
  legacy background assignments retain their compatibility route). An unavailable current lead keeps its queue, with the card's `interaction_id`, what it asks and the
  task's decisions in effect. Concurrent cards of one task are one entry. The agent's `needs_you` signal is not sent
  while its card is on its way up.
- **What the supervisor does**: the supervisor owns its agents' cards (the decision rights: the owner decides only the
  goal and direction, what only they know, what goes out in their name, spending, what cannot be undone and they have
  not said, and what only they can do). It answers a question that is the agent's own call with `interaction_answer`
  (with `reason`), and allows or denies a tool permission or a plan the same way (`interaction_answer` with `allow`):
  the agent gets the answer marked as the assistant's (`answered_by: assistant`, with the reason), never as the user's,
  and it is kept as the task's decision (`source: agent`, the supervisor's conversation). Denying any permission or plan
  remains the supervisor's: it executes nothing and returns the reason to the agent. Only allowing an owner-only
  permission or plan is refused (`interaction_answer` on `allow: true` names `ask_escalate`). What stays the user's for
  an affirmative answer — a card with a form, a password or a secret, an owner action, a permission or plan that would
  send, reply or comment in the user's name, spend money or delete (the matching `userApprovalWords`; pushing, merging and
  deploying are the supervisor's) — and a goal change or a conflict with a decision already made are handed up with
  `ask_escalate {todo_id, reason, recommend}`: every card of the task the user has not been told of rings at once, as
  one push (the first card pushes; the others go to the apps and channels without one), each carrying
  `delegate.reason` / `delegate.recommend`. `userApproval` also recognizes outward, deletion and spending tool actions
  structurally with neutral payloads; known local file-edit tools (`Write`, `Edit`, `MultiEdit`, `NotebookEdit`,
  `apply_patch`) never classify their file contents as the requested action. Antigravity's `File <path>` permission
  display title gets the same treatment only with a recognized string write/edit field in its JSON input, not from a
  path-like title alone. `Skill` only loads instructions, so its arguments are not action evidence; subsequent tool
  calls are classified independently. Only the task's supervisor and the main chat may escalate. A leader's or the
  background supervisor's escalation adds an `escalated` entry to the main chat with the cards, the reason and the
  recommendation, so it does not ask again. The main chat may answer a card either of them still holds, and its answer
  is kept as the task's decision too.
- **Answers**: the user's answers go to the agents as any card's; a question's is kept as every card's is
  (`source: card`), and an escalated permission or plan becomes a `card` decision too ("Allow …" → "Allowed"), so a
  later card cannot quietly go the other way.
- **Timeouts on supervised tasks**: a card still waiting after 5 minutes (`askHoldFor`) goes up a rung:
  from a leader to the main chat (its entry marked as raised). The 5 minutes do not count while the conversation
  holding the card is on a turn: a supervisor working through a long round is not neglecting it, and its time starts
  when the turn ends. A conversation still on a turn an hour (`askBusyMax`) after the card came is counted as stuck
  instead, and the card moves on all the same. If the conversation holding a permission or plan itself is
  `needs_you`, the card moves to the next rung on the 30-second routing tick instead of staying behind another
  decision; the last rung needing the user does not hand it to them. No timeout hands a card to the user, with the
  hub's push tiering on or off: past its last rung a card goes back to the task's supervisor and stays there
  (parked), the entry saying it came back; the supervisor is reminded each `askHoldFor`, at most `askRemindMax` (3)
  times, the card stays on the apps marked `delegating`, and only `ask_escalate` hands it to the user. A reminder
  records its work event and wakes the supervisor only. It constructs no owner notice, regardless of push grading
  or attention settings.
  `ask_escalate` takes `why_owner` and `p0`: with the switch on only `irreversible`, `visible_to_others`,
  `only_owner_knows` or `p0` push, the rest go to the Needs you list and the briefing without a push, bot card or
  channel message.
- **Limits**: the route lives on the pending card, not in the store. After a manager restart the card is back on the
  apps without a push (as every card is); its route is made again from the task's supervisor, so the supervisor can
  still answer, allow or escalate it, and its time starts again from restoration. Whether the user was told of the
  card before the restart is not kept either, so a restored question escalated without context only goes back on the
  apps (it may have rung before). A timeout neither answers a card nor becomes a decision. The supervisor may answer a
  question the user was already asked, and an explicit `ask_escalate` rings — that is the supervisor saying the user
  must decide now. A Codex card listed again once its node reconnected is on the apps without a push too; the
  supervisor still hears of it through the task's `needs_you` signal, and `ask_escalate` takes it. A card the
  assistant is deciding on for the user (agents.delegate) is not escalated: that decision tells the user itself.
  Without leaders, six-class owner cards and plans go to the owner as they come, while routed supervised cards can be
  handed to the owner with `ask_escalate`. Cards raised after an escalation go to the supervisor again rather than
  joining the escalated push.

**Directions and reports up (#3484, #3485).** Each leader has a record, `assistant.directions` (`directions.go`): the kb
project it is filed under, who filed it, since when, and when it ended (it left `assistant.leaders`; nothing is
deleted). Extensions granted `leaders.read` are told each change once (`leader.changed`), and the hub reports from the
task ledger, with no model call (`leader_reports.go`, `leader.report`): `reached` when a review accepts a leader's task,
`decision` when a review needs the owner, and `stuck` when, by the To-dos SOP's Go copy (`todo_triage.go`), the task sits
in Waiting on you or stopped unfinished with no move for max(2 hours, `expect_minutes` × 1.5); a missing estimate uses 2 hours. Stuck is told at most once a day, and only by the
10-minute loop, because it asks the agents' machines. What was told is kept in `assistant.leader_reports`, so a restart
tells nothing again. Reviews from before reports started on the hub are not told. A stuck task is also one
main-chat entry (`leader_stuck`, a supervision signal to the main chat naming the direction, which the secretary turns
into one visible line with no push), and the morning and evening briefings' facts carry one line per leader (its open
tasks, those waiting for the owner, those past that stuck threshold, and its leader's summary of the day) (#3609).

**The prompts (P1, #3609).** The main chat's context lists the current directions (id and name) and its CLAUDE.md
makes it the secretary: it routes work to a direction with `task_plan supervisor`, records what the owner wants an
answer to as a concern and closes it with `concern_report` only after telling the owner, and reports by sending a
work-log extension's progress card when one offers a tool for it, not by writing the report by hand. A leader's
context names its direction and gives its rules: report up only reached, decision and stuck; a change counts only once
deployed and checked live; send `leader_progress` for each reached task and each report cycle. See `docs/extensions/extensions.md` "Leader events".

**A leader's pieces are to-dos (#5846).** How a leader split its direction used to live only in its own files and turns,
so the owner could not see it. Now a leader records each piece with `todo_create` (a plain title saying what the piece
achieves): in a leader conversation that tool opens the to-do under the direction (`origin` and ledger `supervisor` the
leader, no `done_when`, no Agent; `createDirectionTodo` in `todo_plan.go`) instead of the owner's own to-do, and its answer
gives the id. The leader runs an Agent on a piece with `task_plan todo_id` (no second to-do) and closes one finished
without an Agent, or dropped (why in its notes), with `todo_complete`; when the owner asks how the work was split or how
far it got, it answers from these to-dos, its files holding only details. The direction tree lists every piece, and one
no Agent has worked on yet as `planned` (未开始), so the owner sees the whole split and what has not started. Like any
to-do with an origin, a piece nothing touches for 48 h is shelved by the To-dos SOP and leaves the tree; a piece not
started is never reported stuck (it has nothing to be stuck on) unless it waits on the owner.
The main chat and other conversations still create the owner's own to-dos.

**Pin a chat as a direction (#4331, #11700).** A New chat is an ordinary chat. Pin makes it a direction.
The app sends the first message with `POST /api/v1/assistant/conversations`. Creating the conversation makes
zero calls to `assistant.route` and creates zero direction offers. The owner can change the conversation's role later.

| Owner action | Endpoint | Result |
| --- | --- | --- |
| Pin | `POST /api/v1/assistant/directions/{id}/make` | Makes an eligible side conversation a leader. Accepts an optional `project` and `title`. |
| Unpin | `POST /api/v1/assistant/directions/{id}/end` | Removes it from `assistant.leaders`, marks its direction record ended and hands its open tasks to the main chat's supervision ("Leader conversations", "Opening one"). Pin again takes none back. |

The direction records the owner's choice as `by: "user"`. The owner's filing stays in effect and changes emit
`leader.changed`. A direction created without a project stays that way until the owner chooses one.

Managers upgraded from the original #3607 implementation can still read, accept or dismiss stored offers for
seven days from creation. `GET /api/v1/assistant/directions/offers` returns `{"offers":[]}` when none are current.
`POST /api/v1/assistant/directions/{id}/start` accepts a current offer using its suggested project and title,
with optional overrides. It returns 404 and points to `…/make` if the offer is absent or expired.
`DELETE /api/v1/assistant/directions/offers/{id}` reports whether it removed a current offer. Taking a current
offer broadcasts `direction_offer` with `offer: null`.

**New agent goes under a leader (#3608).** The remaining leader-supervision exception is an Agent the owner starts by hand
(`agent_route.go`): the New agent sheet asks `POST /api/v1/assistant/route/agent` for its **Under** field (the leader of
the kb project that owns the directory; else the leader `assistant.route` matches the prompt to, prompt
`assistant/route_agent.md`, 2 s; else Persona), and once the agent runs,
`POST /api/v1/assistant/agents/{mid}/{sid}/supervisor` makes it that conversation's supervised task (as
`agent_supervise` does for the main chat; the card goes into that conversation). Called again with another
conversation, it moves the task there (**Move to…**) and its card follows (the old conversation keeps its history).
Ordinary signals go to that leader (`digestTo`) and the leader reviews the task.

`task_plan` defaults to its planning direction lead, or main for other conversations. Main may explicitly select a
current lead with `supervisor`. Current planned ownership is persisted before dispatch, including a failed start, and
is not treated as an unmarked legacy plan when the manager reloads. Main-created plans without an explicit override
remain with main.

**Main or a direction lead moves an existing task**: `agent_transfer {machine, session_id, supervisor}` uses the same Move to
operation as the App, without adoption. Main may move any open supervised task; a current direction lead may hand off
only an open task it supervises (taking another supervisor's task is refused, naming the current supervisor and who
can move it). The destination is a current direction or the main conversation. The task, brief, criteria, estimate,
origin and Agent session stay. Its supervision and task card move; no message, interruption or restart is sent.
Other callers, owner-created to-dos, assistant sessions and Agents without an open task are refused.


A plan of a leader's that waits for the owner's Start is still a `decision` report (`leader.report` with `plan`, told
once), as the owner ruled after H1. The waiting plans are read from the plans themselves every 10 minutes and right
after a leader's plan is made, not on every change.

**Names and progress (#3679).** Active directions use their canonical name as the shared session title on every
client: conversation lists, headers and sidebars show the same project-supervisor identity, without an automatic
date or task-kind prefix. `conversation_rename` accepts `name` for a direction through the existing main-only
`direction_name` operation; ordinary conversations still use `kind` / `topic`. The stored ordinary title is preserved
and shown again when the direction ends. Explicit name changes publish the updated session title immediately. The App’s existing Rename action also updates
the canonical direction name, marked as user-chosen, while retaining its ordinary stored title.

A direction is shown by its `name` (`direction_progress.go`): the one the owner gave
it (`PUT /api/v1/assistant/directions/{id}/name`, or Start direction's offered name) or the secretary gave it
(`direction_name`, main chat only), else its kb project's name, else its title without the `MMDD | kind | ` prefix (and without a `你是「X」…` role wrapping: just `X`, #3744).
`leader.changed` and `GET /api/v1/assistant/leaders` carry it. Besides the hub's own reached / decision / stuck
reports, a leader writes its direction's progress itself with `leader_progress` (leaders only). An authorized caller
outside Persona writes the same fields with `POST /api/v1/assistant/directions/{id}/progress` or
`personad write-progress`, for an outside or conversation-backed current direction: what was reached, what
is being done and where it is stuck, what the owner has to do, and evidence links. Every source uses the same
normalization and durable report; the write emits `leader.report` kind `progress`, and the latest stays on the
direction (listed by `GET /api/v1/assistant/leaders`). It never messages the owner: the work-log card shows it.
A failed save changes no cached progress and emits no success event; once a report is durable, failure to decorate
its response does not turn the committed write into an HTTP failure.

### Direction notes (#10086)

A direction carries **notes** the owner writes for it: its rules, the readings that prove progress, its terms
(`direction_notes.go`; web: Settings › Persona › Direction notes; `PUT /api/v1/assistant/directions/{id}/notes`, at most
4,000 characters, `""` clears them; `GET /api/v1/assistant/leaders` lists `notes` and `notes_at`). They are the owner's
content, not memory: memory stays one for every conversation (#9440), and a lesson the owner wants a direction to keep
goes into its notes. What accepted tasks teach by themselves (with "Learn from finished tasks" on) is kept apart, in the
direction's project, and never written into the notes (`docs/assistant/todos.md` "Lessons from accepted tasks").

On the web, the same settings group has **Direction and new-chat permissions**, a link to the existing
`default_permission_mode` control. Directions and new assistant conversations share that default; for Claude Code,
changing it also affects stopped-process resumes without an explicit conversation choice. It does not interrupt a live
process, override an explicit mode, or change non-Claude launchers' legacy resume precedence. Settings search indexes
both the Direction notes and permission labels, so either name reaches the control.

The assistant uses the same record through `persona_settings`: `list` exposes each current direction as
`direction.<id>.prompt` with its name, and `get` returns the complete notes to any assistant conversation. Only the main
conversation may `set` one. A string replaces the whole notes; `{"old":"one exact unique fragment","new":"replacement"}`
changes one fragment without resending the rest (an empty `new` deletes it). An empty, missing or repeated `old` and a
malformed object are explicit errors before any card or write; unchanged text needs no card. A change uses
the existing phone permission card with the direction's name and before → after text, and only the owner's approval
applies it. The proposal captures `notes_at`; approval calls `setCurrentDirectionNotes` against that baseline while
holding the settings change lock and, for the membership/conflict check and write, the direction lock. A newer app or
tool edit, or a direction end that commits first, refuses the stale proposal instead of overwriting newer state or
reporting a change no current lead can receive. No second storage or assistant
HTTP route exists: the app route and the tool share persistence, sanitation, conflict checks and the delivery below.

- **The leader** gets them in its own system prompt: they are appended, with the name and goal of the kb project the
  direction is filed under, to its role policy (`supervision_instructions`, which the node passes as
  `--append-system-prompt` or a Codex thread's developer instructions; template `assistant/direction_notes`). The node
  keeps that policy with the conversation and compares it on every send, so the notes survive a restart, a resume and a
  compaction, and changing them restarts only that leader's process: the shared `CLAUDE.md` and every other
  conversation's prompt cache are untouched. The project's one-line status, which a summary rewrite changes, goes into
  the leader line of the per-message context instead, so it never restarts the leader.
- **Applied when the leader is idle** (#11392): saved notes reach the leader's policy on the first send that finds it
  idle (not working, waiting on the owner, starting, compacting or running background work, as its computer lists it;
  `notesInEffect`, `leadIdle`). Until then every send keeps the notes block it has, so saving notes while the leader
  works no longer gets the owner's next message refused (the node refuses a send that would change the policy of a
  process at work). The block in effect is kept with the direction's record (`assistant.directions`,
  `notes_in_effect`), so a manager restart does not change it. Before any send has recorded it (a leader from before,
  or one never sent to since), the first save or clear records the block of the notes it replaces, which is what
  every send so far gave the leader. A conversation that is no leader any more is given no notes block, and an idle
  send records that, so a direction ended and made again does not bring back the block it had before. The node can
  see work before the manager's session row shows it (a moment, or longer on a computer with many sessions); in that
  window its refusal stays as the safety net, and a block counts as in effect only once the node took the send that
  carried it (`notesSendOutcome`).
- **Settled by the send that carried it** (#11392): the moves of the block still in flight are kept per conversation,
  each with the sends that carry its block, and an outcome settles only its own send's move. A send the node took
  keeps its block, and the moves before it stay too. A send not taken drops its move once no other send in flight
  carries that block: the block before it is put back when no later move is in flight, else the later move counts
  from it, so only that one message is refused, not every message until the turn ends. Only the sends below move the
  block. A send whose outcome nobody reports takes the last confirmed block, the one before the oldest move still in
  flight, not a block such a move gave: the node's own request for what its message runs on (`assistant.model`), and
  the parameters the manager makes before it sends them through `call`.
- **The manager's own sends** (`call`: `assistant.send`, `assistant.warm`, `session.send`, `session.send_once`,
  `assistant.answer_again`) are settled by the node's answer, and as not taken when refused before they leave (a
  launcher out of the computer's scope) or when they provably never reached the computer (`sendNotTaken`, which reads
  the error as `relayFailureOf` does): a setup to fix, such as `node_outdated` from the manager's devices layer or
  `no_device`; a conversation that is gone; the computer offline; the bus down or suspended; a call cancelled or not
  written before it left. A failure that may have gone in keeps the block: a deadline or a closed link with the
  request out, a node error without a code, a cancel after the request was written.
- **The owner's messages** are settled by the hub's `session.assistant_sent` notice, which names the message by its
  `client_msg_id`, and at once as not taken when their `assistant.send_params` is refused (a retry's refusal settles
  nothing, below). A notice settles only the message it names; one that names none settles the oldest message without
  an id still waiting for one. Only while the hub has not named a message in a notice on the connection that notice
  came on (an older hub names none) does such a notice settle the oldest message waiting when none without an id
  waits: a hub that names messages sends one without an id only for a message sent without one, such as one sent just
  before the manager restarted. Each event from the hub counts for the connection it was read on, the one whose hello
  was answered last (`busEvent.conn`); a new connection, which may be to another version of the hub, shows it anew,
  and a notice of the connection before it that is handled after the new hello says nothing of the new one.
- **The hub's retry**: a second `assistant.send_params` for a message the manager already gave parameters for is the
  hub's retry, and moves nothing (the manager keeps each conversation's latest 64 ids). The hub sends a retry only
  when the first attempt reached nothing (it stopped before it claimed it, or it typed nothing), and else answers it
  from its record without sending it or telling how it went. So while the first attempt still waits for its notice,
  the retry is tied to it, and the one notice that comes settles the first attempt's move. While that move is in
  flight the retry carries the newest block in flight, the block in effect: the first attempt's own while its move is
  the newest, else the block of a later message that moved it again, which the lead runs if that message went in.
  When the first attempt's move is no longer in flight it carries the last confirmed block. A retry the hub sends
  waits the whole 10 minutes again. A retry whose `assistant.send_params` is refused settles nothing, and the first
  attempt keeps waiting on its own clock: the hub sends nothing for the retry, and the refusal says nothing of the
  first attempt. Once nothing waits for that message (settled, or past the wait), the retry takes the last confirmed
  block and waits for no notice.
- **An outcome never learned keeps the block**. An owner's message whose notice never comes (the hub did not send it
  after all, as for a retry the manager did not know it had seen; it stopped while sending it; or the notice was lost
  while the manager's connection was down) waits 10 minutes, longer than the hub waits for any send. Meanwhile it
  takes no other message's notice once the hub has named a message on the connection; before that, a notice without an
  id may settle it (below). Until then its move stays in flight, so a send nobody reports takes the block
  before it; after that it counts as an outcome never learned, and its block stays in effect. The moves in flight are
  in memory only: after a manager restart between a send and its outcome the new block stays too, as it does for the
  oldest move or owner's message beyond eight in flight for one conversation. When the node had in fact refused that
  send, the lead's process still runs the block before it: until the lead is idle, every send that finds it at work
  carries the new block and the node refuses it, and the first send that finds it idle gives the process the new
  block.
- **Known and left**: a notice names the message, not the attempt. When the first attempt typed nothing and its notice
  reaches the manager only after the hub asked for the retry's parameters (on a framed connection an event can wait
  behind later requests, and events wait in the manager's own queue), that notice settles the move as not taken
  while the retry tied to it still carries the moved block; if the node takes the retry, the lead runs that block and
  the rest of its turn is refused until it is idle. Telling the attempts apart needs the hub to name the attempt. For
  the same reason a retry that carries a later message's block, because that message's move was newer, is settled as
  the first attempt: taken, only the first attempt's move counts as given and the later message's own outcome still
  decides its block, so if that message is then refused, its block is taken back though the retry gave it.
  On a new connection, until the hub's first notice that names a message, a notice without an id still settles the
  oldest message waiting: when a manager that just restarted gets the notice of a message sent without an id before
  the restart first, it settles a message sent with an id since. The hub says nothing else about naming its messages,
  and holding a hub that never names them to the rule would leave each of its messages waiting the whole 10 minutes.
  A manager's own `session.send_once` or `assistant.answer_again` retry that the hub's once-guard answers from its
  record counts as taken.
- **Two editors** (#11392): a save may say which `notes_at` it started from, as `GET /api/v1/assistant/leaders`
  lists it (`null` there and here: there were no notes); when the
  notes changed since, it is refused with 409 `notes_changed` and the stored `notes` and `notes_at`, so a client can
  show both, and nothing changes. `notes_at` is written at millisecond precision and compared as an instant to the
  millisecond, so a client that parses the value and writes it again still matches; a value is never issued twice,
  also across a clear (`notes_last_at` on the record). A save without it is not checked.
- **New Agents for the direction** get them before the project background (template `todo/direction`): an Agent the
  leader starts (`agent_start`) or supervises (a task it plans, a to-do it supervises handed to a new Agent), and an
  Agent whose directory belongs to the direction's kb project, whoever starts it. At most two directions' notes, the
  supervising leader's first; an ended direction's never; an Agent on a work machine gets none.
- **Without notes nothing changes**: the leader's policy and context are byte for byte as before (a project's goal or
  status alone is never added), and no block goes to Agents.
- **Checks**: `server/internal/assistant/direction_notes_test.go` (the route, the leader's policy and context, new
  Agents), `server/internal/assistant/lead_conversations_test.go` (`notes_at`, the idle rule across a restart, the
  outcome of the send that carried a block, in both orders and for every send that moves one, the hub's notice by the
  message it names, a retry the hub sends, answers from its record or refuses, a retry while a later move is in
  flight, the error shapes of a send that never reached the computer, notices with and without ids and by the
  connection they came on, an unknown outcome), `server/internal/hub/assistant_sent_test.go` (the
  notice names its message) and the
  goldens `direction_notes.*` / `todo_direction.*`; the web editor's browser check runs against the mock
  hub with `MOCK_LEADERS=1`: `node web/e2e/direction-notes.mjs` (desktop and phone width, light and dark).

### Agent tasks (#3617)

Assistant-dispatched tasks (`task_plan`, `agent_start`, and `agent_send` with a new `task`) are supervised by their planning direction lead or live side conversation — falling back to main only when the side conversation is gone, archived, or not an assistant conversation — regardless of this legacy switch (#13362, #13657). **Agent tasks** now covers only manually linked user to-dos and reporting-Agent compatibility; it no longer adopts an Agent opened by hand on the Agents page after that Agent did work. Code: `server/internal/assistant/agent_tasks.go`.

- **Switch**: `agent_tasks` in the assistant's settings (`GET/PUT /api/v1/assistant`, `persona_settings` key `assistant.agent_tasks`, approved on the phone), **off by default**. It controls the legacy manually linked user-to-do supervision below; assistant-dispatched tasks do not depend on it. Supervising a manual to-do costs model calls the user pays for, so it remains the user's choice. To-dos that already have a supervisor keep it when the switch goes off.
- **`agent_start` from any chat** makes the quiet to-do at once, as from the main chat (`origin` the calling chat, its card in that chat, title from the prompt), also when `main_tasks_quiet` is off. A reporting chat's start (#3192) is unchanged.
- **An app's New Agent** (`POST /api/v1/sessions` without `todo_id`) is not automatically adopted, even after a turn edits files or runs commands. The owner can explicitly put it under a leader, or main can deliberately adopt it with `agent_supervise`; an ordinary turn end creates no to-do and no second card (#5732).
- **Left alone**: Agents the user starts by hand in a terminal (not `started_by_persona`), assistant conversations, sessions an agent runtime or another source lists, a review's reviewer, archived sessions, and an Agent that is or ever was on a to-do (a closed task's Agent stays that task's: an accepted one reopens when its Agent works again, a cancelled one stays closed). An assistant conversation is told by more than the row's `assistant` flag, which is true only in this manager's directory as the computer's home names it (#10801): a headless row in any manager's assistant or health directory (another manager's, left on a computer that moved here) and every conversation this manager's assistant started (the main chat, the background supervisor, any with a noted origin: the apps' new chats, `POST /api/v1/assistant/conversations`, a channel's or an extension's `/agent`, scheduled tasks, guests) are left alone, and `agent_supervise`, `agent_send` with a task and the other adoptions refuse them (`assistantsConversation`).
- **Supervisor**: assistant-dispatched work goes to main, with only the explicit manual-leader and task-leader-worker exceptions. A legacy user-created to-do linked to an Agent likewise gets main when the switch is on. An app-opened manual Agent gets no ledger merely because it did work.
- **A to-do the user made** gets a supervisor when an Agent is linked to it (`PUT /api/v1/todos/{id}/agents/{mid}/{sid}`, a start or message with `todo_id`): the ledger's `supervisor` only, no `done_when`. It stays the user's: not quiet, `origin` empty, closed only by the user (the done judge still skips it, and `task_review` and `agent_supervise` still refuse it); the supervisor gets its signals (stopped unfinished, waiting for an answer, stalled) and its Agent's reports.
- **`report_to_supervisor` has a receiver** for an Agent on any open to-do: an ordinary to-do without a supervisor gets one on the first report (as above, no `done_when`). An active reporting chat's task (#3192) stays unsupervised, but its Agent's report returns through that chat's own machine and notice queue, without a ledger that would change its quiet notices or stop behavior; an expired reporting registration falls back to the ordinary main-chat supervisor. A question with `urgent:false` waits in the reporting chat's own feed without waking it, not in the main briefing queue. "Nobody receives this" remains for an Agent on no to-do, and for a stopped reporting chat whose notices stay suppressed.
- **Not noisier**: assistant-created quiet to-dos stay quiet ("Quiet tasks" above): no pushes, no Live Activity, not counted in global badges; their signals go to the supervisor, which decides what the user hears (`notify_user`, `ask_user`). This includes an app-started Agent's own completion and needs-you pushes once it is explicitly adopted as a task. A manually opened Agent that was not adopted keeps its ordinary notifications.
- **Closing follows**: these Agents are tasks' Agents, so "Stop Agents of completed tasks" (`docs/assistant/todos.md`) stops them after review, keeping their terminals.
- **Depth cap unchanged**: an Agent that became a task this way is a worker and hands out no Persona tasks ("Leader conversations" above).
- **Clients**: none changed. The to-dos carry `quiet` and `supervision` as other tasks do; showing them under their Agents is separate work.

### The direction tree (#4352)

Each direction (a leader conversation, or an outside direction created for reporting) opens into the open tasks it supervises, each task into the Agents doing it, each Agent into the Agents under it. The hub builds this one tree (`server/internal/assistant/direction_tree.go`, `GET /api/v1/assistant/directions/tree`, `docs/protocol/protocol.md`); the sidebar, the To-dos page's grouping by direction and the Agents page's direction grouping on iOS and Android (the web's went with #6662: its sidebar's 常驻 rows unfold to the same tree) all draw it and never work out the grouping or a task's state themselves. The owner-facing name of the group is 常驻 (en "Pinned", ja 常駐); field names, the route and the event keep `direction`.

- **Directions**: every current leader (`assistant.leaders` without the main chat), in the order `GET /api/v1/assistant/leaders` lists the current ones, named as it names them; then the secretary group (书记官, en "Secretary", ja 書記官), always last and always present, with the main chat's id and no name. An outside direction adds `external: true` and, when bound, its `machine_id` / `session_id` target; these fields only identify its kind and navigation target. Its external Agent's existing task links do not change tree ownership or aggregation. Ended directions are not listed: their open tasks are the secretary's again, as their signals are (leader reports).
- **Tasks**: exactly the to-dos the To-dos page counts open (`triageCountsAsOpen`, the #3340 rule; quiet tasks, a failed start still waiting, ask to-dos and concerns included, as the page includes them). Closed, cancelled, accepted and shelved ones are never listed. An unaccepted `live` delivery is still open and remains listed until main accepts it or the user explicitly completes it (#5732).
- **Whose**: the ledger's `supervisor`. A current leader's id → that direction. The main chat's id, `""`, no ledger, an ended or unknown conversation → the secretary. A task leader's Agent (`"machine/session"`, "Task leaders" above) → a worker: it is not listed or counted as a task of its own, and its active Agents are the `children` of that leader Agent inside the leader's task (a leader started again: the current leader Agent of the task its worker row names). When the leader's task is not open, the worker is listed as an ordinary task of the conversation above the leader (`supervisorAboveTask`). Workers whose leaders lead one another in a loop are each listed on their own (the same on every build).
- **State** (first match wins): `needs_you` — the SOP's Waiting on you (an Agent asks or was interrupted, the owner decides, incl. `needs_user`, a failed start); `review` — waits for acceptance (`pending_review`, or an open acceptance ask) with nothing of it running; `planned` — no Agent has ever been on it and it is not a concern (shown as 未开始, en "Not started", ja 未着手; #5846: a leader's piece not handed out yet); `stuck` — the leader reports' stuck rule (`taskStuck`: Waiting on you, PR to merge, Other open or Shelved, nothing moved for 24 h, every machine of its Agents answered; a task no Agent has worked on counts only while it waits on you); `working` — every other open task (shown as 在做).
- **Counts**: per direction, how many of its listed tasks are in each state. A direction row shows `counts.needs_you` as 「等你 N」 (en "N need you", ja 「対応待ち N」); the working count is not shown (#6626, below).
- **Running** (#6626): `running` is whether anything under the direction is at work right now — an Agent of one of its listed tasks, or one under it (a spawned Agent, a worker's Agent, a running sub-agent, a Codex sub-agent session), whose session state is `working` (the state its own row shows), whatever the task's state. It is not `counts.working`: a working task may have every Agent stopped (followup, unread, waiting), and a task that waits on the user may still have another Agent at work. It is the one rule for a direction row's activity mark on web, iOS and Android: the row spins while its leader conversation's own turn runs or `running` is true, and shows nothing while neither; the leader's own needs-you / unread dot keeps priority. The spinner is the only way working is said on a direction row (the sidebar's 常驻 row and its folded tally, the sidebar's "By pinned" tree, the To-dos page's 常驻 sections): no 「在做 N」 beside it, since one meaning gets one expression; the tasks listed under a direction each still say their state word. Always sent (false too); a client reading a tree without it, from an older hub, spins on `counts.working > 0` as the rows did before. `counts.planned` is 0 from a hub before #5846. The five counts of all groups add up to the To-dos page's open count for the same data, less the worker tasks nested under a leader Agent. The tree is the default manager's alone (it is not merged over managers the way `GET /api/v1/todos` is, `hub/ownership_lists.go`), so on a hub with more than one manager the sum matches only that manager's to-dos.
- **Order**: tasks needs_you, review, stuck, working, planned; within a state the newest activity first, then id.
- **Agents**: a task's active Agents (not released, not dismissed); under each, the Agents of the task it spawned (`source: spawned`, `via` its `machine/session`), its workers' Agents, the in-session sub-agents in its current snapshot (`session_id` `"<parent>@<id>"`, title the description, `working` while running and `idle` once stopped, nested under their launcher while it is also reported) and its Codex sub-agent sessions still going. An Agent appears once in its task. Its `state` is its session's as the apps get it on session rows (`""` when the session was not found). The tree asks each computer for its sessions once per build.
- **Session placement (#11391)**, first match wins, decided by one function the write's limit, the single-direction read and the tree share (`directionPlacements`, `server/internal/assistant/direction_sessions_read.go`): (1) an Agent on an open supervised task follows its task: a task not done or cancelled (a failed start still waiting is not done) with a supervisor, whatever the To-dos page files it under, shelved and seen tasks included (`openSupervisedTask`, #11458); so does a Codex sub-agent session under such an Agent, by its row's parent or, while it is not listed, the parent kept when it was filed. It is placed in no direction by a filing, and its earlier filing does not count while the task is not done; filing it is refused 409, and the existing `POST /api/v1/assistant/agents/{mid}/{sid}/supervisor` remains the operation that moves its supervised task. (2) The owner's filing: the session's one entry (an entry kept under a computer id another computer absorbed since is the session's when it is its newest; the session's next write keeps only one, under the canonical id). An on-its-own entry keeps the session out of every direction, a leader's recorded origin included. (3) A conversation the direction's leader conversation opened, from that recorded origin, without a book entry. The main conversation, the background supervisor, any direction's leader conversation and a conversation deleted on any computer are never placed. Neither a folder, a kb project, nor a model's judgement is placement evidence.
- **Owner filing**: `PUT /api/v1/assistant/sessions/{mid}/{sid}/direction`: `{"direction":"<leader conversation id>"}` files the session there, `""` sets it on its own (so does a body without `direction`), `null` takes its entry out of the book (#11458). The answer `{"direction","previous","changed"}` gives what the book holds for the session now and held before, `null` for no entry, so Undo is the same write with `previous`, right after the write it undoes. Each entry keeps the one its write replaced, and an entry taken out is kept, marked, for its Undo (it places nothing, holds no place, the session's next write replaces it, and it goes after seven days): a write that puts back exactly that entry (its direction, title and filing time) is accepted without the session's row and into an ended or full direction, with the checks that need neither (not the main conversation, the background supervisor or a leader conversation, not an Agent that follows its task, a direction that exists). Undo of a first filing writes `null` and gives back exactly the placement before it (a chat a leader opened is listed with source `leader` again, the direction's count is what it was, the book has no entry that places it), even when that direction has ended or is full since. The manager stores the canonical computer and session id, the owner as source, the filing time, the session title at that time and, for a Codex sub-agent session, its parent. A direction holds at most 50 filed sessions, checked when a session is filed and counted as the read places them then: on-its-own entries and the entries of a session that follows its task, became a leader conversation or was deleted do not count. A direction can therefore hold more later, when a filed Agent stops following its task or an Undo puts an entry back. Taking an entry out asks nothing of the session, so a filing whose session its computer no longer lists, or whose computer left, can always be taken out; such a session can also be set on its own (with every check that needs no row), while any other move into a direction needs it listed (its space is checked). The write decides (an Undo or not, the row, ended, space and limit checks) and saves under the book's lock from the book as it is then, so writes sent together never decide from a book another one changed, and asks the computers before, outside that lock: a filing that fits succeeds however many others land meanwhile (#11490). A write that changes the book then publishes the snapshot, once that lock is let go, so the profile's `keep` follows it at once (Kept listed, below; #11515). A rollback to a manager from before #11463 reads an entry taken out as a filing, and its first write drops `removed_at`, `was` and `parent` from every entry (`docs/protocol/rolling-upgrade-compatibility.md` "Direction session filings (#11463, #11490)"). The hub sends this route to the explicitly selected or default manager, not to whichever manager owns the session's computer, so that selected manager's 404 is authoritative for a session it cannot see.
- **Filing refusals and effects**: every refusal carries a stable `code` and a reason in en, zh-Hans and ja, and leaves the book unchanged; a failed durable save likewise leaves both the stored and effective placement unchanged. 400 `bad_request`: the body is not JSON, or `direction` is neither a string nor null. 403 `full_access_required`: not a full device, with a sentence of its own (the hub already keeps answer-only devices, 403 `forbidden`, and extensions, 403 `permission_denied`, from the route). 404 `machine_not_found`: the computer is not one of this manager's; `session_not_found`: its computer does not list the session (one with an entry can still be taken out or set on its own), it is another manager's conversation, or the id is not a session id; `direction_not_found`: no such direction. 409 `direction_ended`, `direction_full` (50 placed), `direction_main_conversation`, `direction_background_supervisor`, `direction_leader_conversation` (any direction's), `direction_space_mismatch` (a conversation in another space), `direction_supervised_task` (an Agent on an open supervised task, or a sub-agent session under one). 503 `direction_unavailable`: the book, the to-dos, the directions, or, for a move into a direction, the recorded openers or the conversation deletion record could not be read, or the save failed. A computer whose list could not be read answers with that error. A successful filing only records placement and publishes the snapshot (its computer keeps the session listed): no model call, message or Agent send, task creation or move, wake, push, MCP tool or prompt, and no automatic filing or refiling when a computer merely reports sessions. The separate Agent-supervisor route keeps its existing task behavior and is not a filing alias.
- **One direction read**: `GET /api/v1/assistant/directions/{id}` reads a current or ended direction's `id`, `name`, optional `ended_at`, `notes`, optional `notes_at` and optional `progress`, plus `sessions`, `ended_tasks` and `more`. Full devices only: the hub refuses answer-only devices (the watch) with 403 `forbidden`, and extensions have no scope for it (403 `permission_denied`). `sessions` includes quiet as well as active placements from owner filings and leader origins. Each row has `machine_id`, `session_id`, `title`, `source` `owner` or `leader`, placement time `at`, and `state` (below). `ended_tasks` are done or cancelled tasks under the same ownership rule as the tree, newest first, so ending a task does not move it to another direction. The page defaults to 20, `limit` is capped at 100, `before` is an ended-task id cursor, `more` marks another page, and optional `title` filters case-insensitively. Only the requested direction's sessions are placed and only their computers asked; the deletion record is read once, so a page costs the same however many conversations other directions have (#11458). Unknown direction 404 `direction_not_found`; a `before` that names none of this direction's ended tasks (with the same `title`) 409 `ended_task_not_found`; the to-dos, the titles or the direction's record not readable 503 `direction_unavailable`. A computer that does not answer never fails the read, and when what places sessions (the book, the recorded openers, the deletion record) cannot be read the read still answers, with `sessions` empty and `sessions_unavailable: true`, the ended tasks as always. The literal `tree` and `offers` routes remain distinct.
- **Kept listed (#11515)**: a session placed under a current direction stays in its computer's list however quiet it is, so the read can show its state and a write can move it. A computer lists a quiet Claude Code transcript only for seven days, and only the newest of those, and the automatic archive takes a closed Agent off the lists after `archive_closed_days`; without this a quiet placed session read `not_listed`, and moving it was refused `session_not_found`. The profile's `keep` (`docs/assistant/assistant.md` "Boundary with hub") takes, per current direction, the owner's live filings, newest filing first, then the conversations its leader conversation opened, newest first, and at most 50 of them, each on the computer its entry or recorded origin names (an opened conversation without one: the assistant's). They are placed as the read places them (`directionKeep`, `server/internal/assistant/direction_sessions_keep.go`), from the manager's own records: no computer is asked. Not kept: entries taken out, on-its-own entries (which also stop a conversation a leader opened from being kept), the sessions of an ended direction (their entries stay in the book), and a conversation that was deleted or that an owner entry places elsewhere. A session on a computer that is not this manager's is in the list and reaches no node (`SetKeep` tells only this manager's computers). The main conversation, the background supervisor and the leader conversations are kept, or not, as before. When the book, the recorded openers, the deletion record or the direction records cannot be read, the profile leaves `keep` out and the nodes keep the list they were last given: a failed read never takes a row out. A write that changes the book, ending or making a direction, opening a conversation and deleting one each publish the snapshot, so `keep` follows at once. The automatic archive leaves every session the owner filed under a current direction, past the 50 too, and archives nothing in a pass while the book cannot be read (`docs/assistant/todos.md` "Closed sessions archive themselves"); the owner's own Archive works as before. Only the row is kept, not the transcript: when Claude Code's own cleanup removes the file (30 days by default), the computer lists nothing, the read says `not_listed`, and opening the session gets the node's `not_found` with its reason. What `keep_sessions` does not reach, and would need a node change: Codex Agent threads, which a node lists from rollouts written in the last 14 days whatever it says (`server/internal/node/codex.go`; it reads `keep_sessions` only to list a thread its agent filter would hide), so after a node restart a filed Codex thread quiet for longer is not listed (read from the code, not run); and other runtimes' sessions. The desktop app's sessions: a node skips that app's archived and deleted checks for the sessions it keeps (`server/internal/node/sessions_offline.go`), so a filed desktop-app session stays listed while it is filed, even after it is archived or deleted in that app (read from the code, not run); taking the filing out restores the usual behavior.
- **Where a session stands** (`state`, #11458): the session's own state as on session rows when its computer is connected and lists it (`offline` among them: stopped on a connected computer); `computer_offline` when its computer is not connected, so what it does is not known; `list_unknown` when its computer is connected but has not given its list on this connection (not yet, or the call failed), so whether it lists the session is not known; `not_listed` when its computer is connected, gave its list, and the session is not in it. Being quiet alone no longer does that (Kept listed, above), so it means one of these: the session is archived; its transcript is gone; it is beyond its direction's 50 kept; it is a kind of session the keep list does not reach (a Codex Agent thread, another runtime's session); it is placed in an ended direction; or its computer leaves it out for another reason, such as its folder blocked or outside the session scope since, or the session going on in another engine under a new id; `computer_removed` when its computer is no longer one of the manager's computers. Connected is what the manager's devices say, as everywhere else in the fleet, never whether a list call failed: while they still show a computer as before after its node left (the reconnect grace, a resident node's reload or a transport waiting to resume, the warm start after a manager restart: seconds, up to about 30) it counts as connected and its sessions keep their last listed state, where an Agent can still read working; after that it reads `computer_offline`, and the manager builds the tree again when the reconnect grace, a reload or the warm start ends (the devices tell it nothing then). What the manager cannot know: an Agent the owner deleted on the hub (`DELETE /api/v1/sessions/{mid}/{sid}`, kept in the hub's `sessions.deleted`) is not told to it, so it reads its own state while its computer lists it, which while it is kept is as long as its transcript lasts, and `not_listed` once that computer does not; its filing stays.
- **Sessions in the tree**: each current direction gains additive `sessions`, omitted when empty: its placed sessions whose computer is connected and lists them as `working` or `needs_you`. Quiet sessions, and those whose computer is offline or gone or whose list is not known or lacks them, remain in the single-direction read instead. When what places sessions cannot be read for a while, the tree keeps the sessions it told last. `counts`, `running` and `tasks` are unchanged by filings. A session the tree lists under a task (an Agent, one under it, its sub-agent sessions) is not repeated in `sessions`. An Agent on the owner's own to-do may be filed and appears in the direction read, but in the tree it remains only under that to-do in the secretary group.
- **Lifecycle**: permanently deleting an assistant conversation deletes its filing with its other per-conversation records, whichever computer it is on. The hub's reversible Agent deletion is invisible to the manager (above), so that filing survives. Ending a direction keeps its filings and its ended read; filing another session into it is refused while ended, and making the same conversation a direction again restores the placements. While it is ended its sessions are not kept listed and the automatic archive may take its filed Agents again. Made a direction again, its sessions are kept again (Kept listed, above): the ones that only dropped out of a computer's list are listed again, while a filed Agent the automatic archive took while the direction was ended, or before #11515, stays archived until the owner restores it. A deleted filing never decides placement by folder or project afterward.
- **Push**: `{"type":"directions"}` whenever the tree's content changed: a to-do, the sessions of a computer one of its Agents, a filed session or a conversation a current direction's leader opened is on, such a computer disconnecting or no longer being shown as before, a direction, its name or the leader list changed, an owner filing or leader-opened placement changed, or (every 10 minutes) a task may have become stuck; gathered for one second, compared with a digest of the last tree told, so an unchanged tree tells nothing. Clients read the tree again on it and on (re)connect; a hub without the route answers 404 and the clients keep the flat direction rows.

## Data and APIs (hub)

### Task = to-do

Add two columns to `todos`:

- `origin`: the assistant chat that created it (session ID), empty for manually created to-dos. The dock and board include only items whose `origin` equals the current chat (the main chat also includes manual Agents; see "Manual Agents in the main-chat dock" below).
- `plan_id`: the plan that created it, nullable.

### Plans

The manager records how each Agent start was placed (#13355). The owner or trust gate and any quota wait come before
reservation. Admission and workspace waits use the existing task queue. A flexible task can move to a healthy
launcher or to a computer with lower load per core, with available memory breaking a tie. Its task keeps the reason
shown on the card. Starts from other entries such as New Agent and Resume report placement on the requested computer.
See `docs/server/launcher-health.md`, “Execution-agent admission gate”.

Table `task_plans`: `id`, `conversation`, `tasks` (JSON: each item's `title`, `notes`, `machine`, `machine_policy`, `cwd`, `launcher`, `model`, `permission`, `ultracode`, `readonly`, `user_requested`, `why`, `role` and `parent` for task leaders and their workers),
`state` (`open` / `started` / `dismissed`), `created_at`, `updated_at`. A task also carries its brief's fields (`goal`, `out_of_bounds`,
`continues`, `continues_why`, `started_why`, and the hub's `brief`): "Briefs" above.

- MCP tool `task_plan` (assistant only): accepts a task list. From the main conversation or a direction's lead, start directly unless an item has a request gate or an `owner_step`, or the track confirms every hand-off ("A supervisor's own plan starts at once" above). Elsewhere, start directly if every item is read-only, `user_requested` (with `assistant.start_requested_tasks` enabled; not in a direction's lead that a hand-over of the owner's quoted words was sent into, until the owner next writes there), covered by a `tasks.trust` entry (its directory or one above it), or, in a round the hub started for a scheduled task, covered by that scheduled task's own start grant ("A scheduled task's own start grant" below); otherwise save an `open` plan and emit chat activity.
  Return the plan ID and each task's state (text). Automatic dispatch is reported as queued, not as proof of an Agent start. `machine_policy` defaults to `fixed` (including old saved plans); only explicit `flexible` tasks may move off an unhealthy machine. The policy survives storage, card edits and retry. Fixed tasks fail on their own target and can retry there after health recovery.
- `POST /api/v1/task-plans/{id}/start`: may include user-edited fields. Create a to-do per task (`origin`, `plan_id`), then start its Agent (`startSession` with `todo_id`). An owner API client can set `close_tab_on_accept: true` on a new task in `tasks` (see `docs/assistant/todos.md` "Close a task's tab after acceptance").
  With `remember: true`, record this kind (`machine|<repository or folder>|read-or-edit`, "Start These Directly" above) in `tasks.trust`.
  With `schedule_grant: true` on a plan a scheduled task's round made, add the same kind to that scheduled task's start grant instead (below). `POST …/dismiss`: do not start.
- MCP tool `task_plan_withdraw` (assistant only, #6739): the conversation that put up an `open` plan takes it down with a
  one-sentence `reason`: `dismissed` as `POST …/dismiss` leaves it (the apps are told), with a `task_plan_withdrawn`
  work event (plan, conversation, reason, task titles). Another conversation's plan, a plan not `open`, and an empty
  reason are refused. A withdrawn plan is no answer of the owner's: the "Start These Directly" count and the setup
  suggestions leave it out. The snapshot's plans waiting to start give each plan's id for it.
- A plan made in a round the hub started for a scheduled task names it: each task's `schedule` (its id) and `schedule_title`, the hub's alone (a tool call's or an edit's are dropped). Never in the health space.
- `ultracode` (#12): the assistant sets it per task, as on `agent_start`, rather than writing the word into the prompt; the Agent starts with Claude Code's ultracode on (`session.start` `ultracode: true`). On start, `true` / `false` from the card overrides it and leaving it out keeps the assistant's choice.

### A scheduled task's own start grant

A round the hub starts for a scheduled task (a routine, a follow-up, Run now) has no message from the owner, so a
writable task it hands out used to wait on its card however much the owner wanted the schedule to do that work
(#8045). The owner's consent to a schedule is now recorded on the schedule itself (`assistant/schedule_grant.go`):

- **Set**: only by the owner, on the card of one of that schedule's own rounds: "Let “<scheduled task>” start these by
  itself" (`schedule_grant: true` on Start). Each writable task's kind, as "Start These Directly" names it (computer +
  repository or folder + edit, resolved once admission fixed the folder that runs), is added to the task's
  `start_grant`. No tool call (`schedule_routine`, `schedule_followup`, `task_plan`), no app body (`PUT` / `POST` on
  the task) and no Start that is not the owner's sets or widens it, and it is not added to `tasks.trust`. The plan keeps
  the schedule as its round had it (`schedule_basis`): a Start after the schedule changed grants nothing.
- **Only for schedules that read nothing from outside**: a schedule whose runs get the briefing's facts (Inbox
  matters), the north star's report or a saved state note (an earlier run wrote it), that runs into the conversation it
  came from, or that is in the health space never offers or uses one.
- **Only in a clean round**: a schedule with a grant starts each round in a new conversation of its own, under an id
  the hub picks before it starts (Claude Code only: another agent picks its own id, so its rounds are never clean and
  their cards don't offer a grant), and that conversation's context leaves out the live snapshot (Lark messages,
  calendar, the Inbox matters followed, agents' lines: `Context`). The assistant's own instructions and memory in its
  directory still load, as in every conversation of the assistant's. An automatic retry of a failed round goes on in
  that round's conversation, not a clean one, so what it plans asks. `task_plan` there starts a writable task whose kind
  the grant covers (as a remembered kind covers its folder tree) without the card only when the hub noted the round as
  it sent it (in memory, so not after a hub restart), the turn began with that schedule's own prompt written after
  that, and the whole conversation, as its machine reads it, is that prompt and the agent's own work with Persona's
  own tools that read nothing from outside (`grantSafeTools`: planning, listing computers and repositories,
  `notify_user`, Claude Code's `ToolSearch` and `TodoWrite`), ending with this `task_plan` call: the round's first
  (a second one's answer names other work, which may come from outside). Mail, Inbox, web pages, shell commands,
  files (the knowledge base included), agent transcripts, a relayed message or a picture keep the card, and so does
  anything the hub cannot read or tell (a node that does not say the page is whole).
- **`user_requested` in a round**: a round the hub started has no message of the owner's, so `task_plan` ignores
  `user_requested` in a turn a scheduled task started, with or without a grant: while the hub has a round under way in
  that conversation, or when its latest turn began with a scheduled prompt (after a hub restart too). An owner who
  writes into that turn gets the card. A direction's lead that a hand-over of the owner's quoted words was sent into
  ignores it the same way until the owner next writes there ("Across a relay": **Start in a lead after a hand-over**); the check
  sits next to this one in `task_plan`.
- **Everything else keeps the card**: other scheduled tasks' rounds, every turn the hub did not start for that
  schedule, kinds outside the grant.
- **Lifetime**: no expiry. It holds while the schedule's prompt, target (the computer and conversation its runs go
  to), space, `facts` and `report` are the ones it was given under; editing any of them drops it, and so does deleting
  the task. The scheduled task's page lists the kinds with Remove (`DELETE /api/v1/assistant/tasks/{id}/start-grant`).

### Chat activity

Hub-inserted chat entries do not enter Claude's transcript (that would trigger another turn and later be compacted away). Store separately in `assistant_feed`:
`id`, `conversation`, `at`, `kind`, `ref`, `body` (JSON), plus `sent_at` (when a message-card reply was sent, written alongside `body.sent_at` so `since` queries can use an index; 0390).

| kind | Trigger | ref / body |
| --- | --- | --- |
| `plan` | `task_plan` hands you a plan | Plan ID |
| `task` | A task starts | To-do ID |
| `answered` | A task interaction is handled | To-do ID; one-line summary (question, your answer), and full question / answer `items` per question |
| `receipt` | Task finishes / fails to start / is interrupted by a model-service error (`interrupted`, #1516) | To-do ID; summary, changed-file count, tests, PR; interrupted receipts include original error `error`; a turn's receipt (done or interrupted) names its run in `machine_id` / `session_id` (#10465; not on failed starts or older receipts) |
| `start_remedy` | A supervised task's agent did not start and its tab is kept (#2570): each step the main or current supervising conversation takes (`screen` read, `keys` typed, `retry`), the owner's answer to the tab's startup question from the app (`answer`, #10365; `answer_not_typed` when the question had left the tab before it came and nothing was typed) and how it ended (`started`, `gave_up`) | To-do ID; `action`, `by` (`assistant`, `user`, `hub`), `attempt`, `keys` / `text` typed, `reason` in the user's language, `suggestion`, `launcher_from` / `launcher_to` (a retry on another launcher of the same computer, #10365), `choice` (the id of the answer given, never what a prompt asked for, #10365). The tab's screen is never stored here. Clients that don't know the kind show nothing; the live task cards (#2365) list these steps in the card's history |
| `routed` | You speak directly to a task, or Persona sends its agent work (`agent_send` with `task`, `agent_interrupt` with `then`, a rework) | To-do ID; original text; `interrupted` (agent_interrupt); `from: "assistant"` when Persona wrote it ("Messages to agents"); `relay: "answer"` when the hub worded an answer of yours for the Agent (a recommendation taken, an option chosen, a reply to a card; #3804), drawn like your own message; `machine_id` / `session_id`: the run the line reached, whoever wrote it (#10465; older entries have none, and a message that started an Agent whose start named no session has only `new_agent_machine`). Migration 0441 marked older ones Persona wrote: an `interrupted` key, the rework prompt's opening line, or a `task` card of the same to-do at most 5 seconds before; the case it can't tell (agent_send with `task` to an agent already on that task, which writes no card) keeps the bubble |
| `message` | AI twin drafts a reply to an Inbox message related to a task | Inbox item ID; task, source, chat, sender, original message, draft |
| `today` | First opening of this chat that day (App calls `POST …/today`) | Date; tasks completed overnight, waiting tasks, open supervised tasks awaiting review (`pending_review`, excluded from waiting / working / done), working count, remaining Lark meetings today, chats mentioning me, other unread counts, unsuccessful items since yesterday at 18:00 (failed-start tasks, failed / skipped scheduled tasks, #4124) |

- The web Today card shows **Waiting for your review** when `pending_review` is nonempty (#4889): the task title for one item, or the count for several. **View** opens the first task, ordered by creation time like waiting tasks. The field is omitted when empty; older clients, including the unchanged iOS and Android Today cards, ignore it. Supervision state `needs_user` keeps its existing behavior.
- `GET /api/v1/assistant/conversations/{id}/feed?since=`; WebSocket `assistant_feed` pushes new entries. The App merges them with the transcript by time.
- Live plan-card / task-card state is not stored in the feed. Cards read current plan / to-do / session state by `ref`, updating in place.

### Hooks

- **Finished**: when `Machine.setSessions` finds an Agent attached to a to-do with `origin` changing from working to done / idle with no pending interaction (at the same point as `notifyFinished`),
  write `receipt`. A supervised task's turn that ended with no `working` seen is found by the supervisor loop instead, within its next tick (`docs/assistant/todos.md`, "Automatic completion"). A task with multiple Agents is finished only when all stop. If an Agent stops on a model API error (`turn_error`), write an `interrupted` receipt without judging completion (#1516).
  Continue on the card uses `POST /api/v1/todos/{id}/retry`; hub sends the Agent "继续" (continue). Once it runs, the task returns to working; a normal turn ending emits another done receipt.
- **Waiting for you**: no feed entry. The App directly uses the attached Agents' `pending_interactions`; handling (`resolve`) writes `answered`.
- **Speak to a task**: `POST /api/v1/assistant/conversations/{id}/route {todo_id, text}`; hub uses `session.send` to send to the task's highest-priority Agent and writes `routed`.
- **Persona learns about it**: the live snapshot adds "Tasks started from this chat," with each task's state, latest text, and changes since the previous snapshot (finished, waiting for you). No extra turn is triggered. A conversation other than the main one sees only the tasks it handed out or supervises there (#8666, `docs/assistant/todos.md`).

## App (iOS)

- The assistant's `ChatView(style: .assistant)` merges chat activity (inserted by time in `ChatLines`). New row types: plan card, task card, receipt, answered question, routed bubble, Persona's folded message to an agent (without the hand-offs the cards take in, `SentToAgents.shown`).
  Instant phone-tool calls (`phone_reminders_create`, `phone_calendar_create_event`, etc.) are extracted from the "Tools N" row and rendered as result cards.
- `mainFooter` adds a question-card queue (task interactions with task names) and dock above `InteractionPanel`; the input gains `@` and routing labels.
- New components belong in the design system (`apple/Persona/DesignSystem`), using `AgentState.tone` colors and `Theme.Motion` animations.
  No hardcoded values in pages (`CLAUDE.md`, "Design system (iOS)"). Logic lives in an `@Observable` `TasksModel` (dependencies from `Deps`, tests with `FakeHub`);
  chat-activity merging is a pure function covered by unit tests.
- Screenshots: add mock plan, task, and activity data to the mock hub (`scripts/mock/mock_hub.py`). DEBUG argument `-openUnifiedStep <n>` stops at the corresponding prototype step;
  capture light and dark for each step and compare to the prototype.

## Implementation (iOS, step 3)

| Part | Location |
| --- | --- |
| Data types (plans, chat activity, to-dos, task state) and APIs | `apple/Shared/Tasks.swift` |
| To-do list, local cache, ID index | `apple/Persona/App/TodoStore.swift` (`todos` events and reconnect share refresh logic; in-flight refreshes are coalesced with a follow-up fetch) |
| Plans, per-chat activity, routing target, opened task | `apple/Persona/App/TasksStore.swift` (to-dos read from the `TodoStore` index; `assistant_feed` / `task_plan` events update plans and activity; reconnect refetches each chat's feed) |
| Plan cards, task cards, receipts, answered questions, routed bubbles, dock, question cards, routing labels, `@` list | `apple/Persona/Views/Assistant/Tasks/TaskViews.swift` |
| Task details (embedded Agent chat), task board | `apple/Persona/Views/Assistant/Tasks/TaskSheets.swift` |
| Feed inserted into chat at the end of its turn (before the next message) | `ChatLines.mergeFeed`, `ChatLine.Content.feed` |
| Above the input: question card (earliest only, with "N more"), `@` list, dock, routing label; routed messages go directly to the task | `ChatView.taskFooter`, `Composer.sendText` |
| Action bar on iPhone, iPad and Mac: Needs You and In Progress in place of the dock, each opening its tray, each thing in one of them (#3393, the Apple side of #3387); in the main conversation with push grading on, Held Pushes after them, its tray in the same place (#7782, #8223) | `apple/Persona/Views/Assistant/Tasks/AssistantActionBar.swift` (`ActionTrayContent`, `DockCapsule`), `apple/Persona/Views/Assistant/Tasks/PushInterceptsView.swift` |

- Reuse Agent state colors (`AgentState.tone`: Needs you red, Working amber, Unread green), not the prototype's orange waiting state, to match Agents.
- If dock pills do not fit, truncate the title but never "Waiting for you."
- **Action bar on iPhone, iPad and Mac (#3393, replacing #2879's Pending / To Start / Progress / Agents and the Mac's
  dock)**: Needs You and In Progress, and Finished (#10922) while it has rows, as "Needs you and In progress: each thing
  once" above defines them. The split is
  `AssistantEntries.split`, a port of the web's `assistantEntries`; `ActionTrayContent` puts the conversation's own cards
  in front of Needs You (their row closes the tray onto the card below). The web (`OwnAskRow` in `AssistantActionBar`,
  the cards in `ChatView`'s dock) and Android (`panelRequests`, the chat's `InteractionPanel`) do the same since #4883:
  the conversation's own cards are above its composer, never only inside the closed tray. On the web, a card its own turn
  waits on (`TurnAsk`, `lib/turn-asks.ts`, #11794) stands open above the capsules instead and Needs you leaves it out;
  only the cards of sessions folded under the conversation keep a row there. An Agent linked to a to-do made by hand (no
  `origin`) is that to-do's row in the main chat, not a second row of its own (#3387's `dockAgents` leaves out only the
  agents of conversations' tasks, so web and Android list it twice; the fixture has no such case yet). Message cards also leave Needs You once the inbox
  has the item handled or the user put the card off with Later. Rows: a task opens its details (right-click or long press
  removes one that is not running) and says its status and, past one, its Agent count; an Agent opens its page; a plan or
  a message card is the card itself, Start or Send right there; shelved tasks are one uncounted line under In Progress that
  opens the board. Each tray's All opens the board.
  The entries are the dock's capsule (`DockCapsule`, also the trays' All): caption medium (13 pt at the default text size,
  Dynamic Type), 12 pt horizontal and 6 pt vertical padding, a full capsule radius, `Theme.Fill.chip` behind secondary
  text, 28 pt tall at the default size, 8 pt apart. Open is the filter chip's on look in the same size (accent fill, white
  semibold); Needs You with rows takes the waiting pill's tint, empty it is the plain grey capsule and opens "All Caught Up".
  In the main conversation with push grading on, Held Pushes (#7782) is an entry in the same capsule after
  them; its tray of today's held pushes opens in the trays' place, above the row at the bar's width, and one tray is open
  at a time (#8223; Android's `AssistantTray.HeldPushes` shares the slot the same way, the web opens a dialog).
  The entries (and Held Pushes and the computer pill after them) are one row, Needs You first. When they do not fit the
  bar (Finished beside the two with Held Pushes, a long translation, the largest text size the interface allows, #6813),
  the row scrolls sideways, as the web's and Android's do, instead of making the chat page wider than the screen with
  the header and the composer cut off at both edges (#5702); while they fit, nothing moves (#10922). From #5884 until
  #7083 a `FlowLayoutRow` wrapped them at the accessibility text sizes, which the interface no longer reaches.
  Tests: `AssistantEntriesFixtureTests` (the shared fixture), `AssistantActionBarTests`, UI `AssistantActionBarUITests`
  (`MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1`; `testEntriesStayOnScreenAtEveryTextSize` in zh-Hans, English and Japanese at
  the default and the largest size, light and dark), UI `PushInterceptsUITests` (`MOCK_LN=1`; the held pushes tray's
  place and width, the three entries on one row, one tray at a time).
- Task details have one header layer: the embedded Agent page takes over the task name, Close, and task ⋯ (#3255; see "Task details" above).
- Screenshots: `MOCK_TASKS=1`, `MOCK_TASKS_AT`, `-showTaskDetail`, `-showTaskBoard` (`docs/operations/testing.md`).

#### Manual Agents in the main-chat dock (#1113)

Agents started manually in Agents have no to-do, or a to-do with empty `origin`, so no chat's dock sees them. The main chat must watch all tasks, so only its dock and board include them too:

- Included: all Working / Waiting for you Agents (excluding Persona's own chats), minus those already represented by tasks: Agents attached to unfinished tasks (nonempty `origin`),
  and sessions marked by hub with a non-manual to-do (including new tasks not yet refreshed into the to-do list). Agents attached to manually created to-dos still count as manual.
  Tasks from other chats remain excluded from the main chat, and their Agents do not reappear as manual duplicates.
- Subtasks (Android, #4769): an Agent another one started (a Codex subagent, or an agent another Paseo agent created, #4494) is no pill of its own while the dock shows the Agent above it, as its pill or as its task's; that row's background line counts the ones still running where the host reports them (a Paseo creator does). One that waits for you stays, and so does a working one whose creator the dock does not show (finished, or let go). iOS still gives each its own pill; the shared fixture (`spec/fixtures/entries`) has no such case yet.
  The web (#5120, after cindy) folds every child the hub folds (`folded_under`), the one that waits for you too: it is never a pill or a row of its own, its creator's entry carries its state (needs you while it waits, in progress while it works), and its card is grouped under its creator and answered on the creator's page. Android and iOS follow up in #5140.
- Ordering: task pills first, manual Agents afterward (waiting first; others by most recent entry into their current state). Their pills add the Agents page's `</>` mark; waiting pills use the same Needs you red and "Waiting for you" as tasks.
- Disappearance: remove from the dock when finished (Unread), idle, or offline. Unlike tasks, Unread manual Agents do not remain in the dock (they remain in Agents).
- "All N" includes them; the board lists them after tasks in Waiting for you / Working. Tapping a pill or board row opens the Agent's chat in the task-details sheet.
- Data: `AppModel.goingAgents` derives from the existing session list and updates only when pill-visible content changes. No new polling, and no dock redraw on every push.
  Filtering: `DockAgent.shown` (`apple/Shared/Tasks.swift`); unit tests `DockAgentTests`; UI tests `TaskDockAgentTests` (`MOCK_TASKS_DOCK=pair` for two pills, otherwise six).
- No chat-stream cards for manual Agents: they were not started from the chat.
- Web does the same (#1275): `dockAgents` (`web/src/lib/todos.ts`) filters and sorts; tapping a pill opens that Agent in the side panel (`AgentPanel`) or as its page, as Settings says (#5424); "All N" goes to To-dos.
- Step 4 (#996 and this step's iOS work):
  - **Day sections**: `ChatLines.daySections` adds a date above each day's first row (Today / Yesterday / month, day, weekday), only in assistant chats.
  - **Old task cards collapse**: non-today `task` entries become one line (`TaskLine`: state, title, progress); tapping opens details.
  - **Instant result cards**: successful `phone_reminders_create` / `phone_calendar_create_event` results render below that turn's work row (`QuickResult`,
    `ChatLines.quickResults`). Undo deletes locally through EventKit (`RemindersTool.remove`, `CalendarTool.remove`), without hub.
    Successful `phone_location_current`, `phone_location_search` and `phone_maps_search_nearby` results render there as a map (`PlaceMapResult`, `PlaceMapCard`, #5543):
    the phone's dot or up to ten numbered pins in result order (the numbers the answer uses), the first three places listed with Directions to Apple Maps,
    and the whole map in a sheet. The chat draws a cached `MKMapSnapshotter` still; only the sheet's map is live.
    A route `phone_maps_route` worked out renders there as its line from the phone's dot to the destination, "Walking · 6 min · 420 m" and Start in Maps (#5544);
    transit has no line, only the destination with its time and distance.
    Directions `phone_maps_navigate` handed to Apple Maps (`opened: maps`) render there too (#5545): the destination on the same map, with Maps' icon,
    the mode and "Opened in Maps", and a button that opens the same directions again; Maps does the navigating.
  - **Today**: first main-chat display each day calls `POST …/today` (`TasksStore.loadToday`), placing the card at midnight: completed tasks, waiting tasks, working count,
    Lark meetings (Prepare sends a message for you), chats mentioning you (Review); if the App already has reminder permission, also include reminders due today (no permission prompt for this).
  - **Direct-start tasks**: Persona profile's Proactive work section lists remembered kinds, with swipe-left deletion.
- **Message cards link to tasks** (hub): first, PR/issue URLs in the item's connector link, title, preview and context are matched against open to-dos' saved repository links (including task-owned closeout and structured work-artifact PRs, #9725), legacy PR URL, title and notes (`server/internal/assistant/inbox_task_links.go`, #5578). Comment fragments and query parameters are ignored; host, repository, PR/issue kind and number must agree (on github.com the kind is left out: one number is one issue or pull request there). Within a GitHub item's repository (#6507) a task also names the item when it writes `owner/repo#N`, when it writes a bare `#N` and belongs to that repository by #6424's rule (it names `owner/repo` or its Agent works in the repository's directory), or when the PR is one its Agents named before the current one (kept as `prs` next to `pr` in `assistant.todo_stages`, at most 20); an issue no task names links through a pull request of its repository whose own Inbox item says it closes the issue (`Closes`/`Fixes`/`Resolves #N`), without a request to GitHub. Only a unique task is linked, and an existing link is kept while its task is open; a closed task's link moves to the one open task that continues it (#6423), on handled items too, which raise no signal (#6546). This step needs neither a model nor the AI twin. Linking old content does not bypass the twin's age limit when it is later enabled. With the twin enabled, an unmatched or ambiguous item still goes to the Inbox's judge (`inbox/judge.md`), which sees active tasks and may return `task_id` (only an ID shown to it), with `evidence` copied verbatim (at least 4 characters, white space and case folded) from that task's title or notes; an answer without such evidence links nothing (#6424).
  Before the judge, the candidates are narrowed deterministically (`inbox_task_scope.go`): a GitHub item (its `repo` tag or an `owner/repo#N` thread) is offered only tasks whose title, notes or PR URL name that `owner/repo`, or whose Agent's working directory or space has a directory named after the repository (`repo` or `repo-…`); with none, the judge is not called and the item stays unlinked. A chat-thread item (`…:chat:<id>:thread:<id>`, its `chat` tag) is offered only the tasks that name its thread or chat id when any does, and every task otherwise. On every pass, an existing link of an open GitHub item to an open task that does not name its repository is cleared and persisted (logged, nothing sent); a link to a closed task is left alone.
  The AI twin's first message includes that task's progress (`inbox/agent.md`), so the draft states actual progress. Draft submission
  (`inbox_draft`) inserts a `message` entry into the chat that started the task. The App renders `MessageCard`: original message, attached task (live), draft (Edit inline), Send (Inbox sending; send by ID if the item is not local), Later (collapse for now).
  When the linked task is supervised and `inbox_signals` is on, every new batch of messages from other people is admitted for task delivery ("Supervision (#3124)" above, #4293), even with the twin disabled or the item not yet triaged. Confirmed GitHub delivery to active work stays in the routed task feed without a separate supervisor wake (#9916); other eligible events still raise `inbox`. The existing forwarding switch, supervision exclusions and per-message deduplication still apply. Loading previously linked content after a restart does not forward it again; newly collected batches after loading still follow this delivery policy.
  Already-replied cards show "Replied on your behalf" and the sent text.
  - **Sent state on the card** (#1519): when a reply for this item is sent (from a card, Inbox, or any client), hub writes `sent_at` and `sent` (the sent text) into the previously inserted message-card body,
    then rebroadcasts `assistant_feed` with the same ID; clients replace the body by ID. `GET …/feed?since=` also includes cards sent after `since`. Written once, never cleared:
    reopening an item with a new message and having the AI twin draft again creates a new card; the old one remains sent. For cards predating this record, reads backfill from the Inbox item (`done_by=replied`, later than the card), using the draft as text.
    Finding unsent cards (`db.MarkFeedSent`) uses partial index `assistant_feed_unsent` (`kind, ref, at`, predicate: body has no `sent_at`, migration 0392, #2617. Only message cards write `sent_at`; other kinds remain in the index, so it is not small).
    Keep this UPDATE's condition aligned with the index predicate or each reply scans the whole table again.
  - **Send once**: Send includes the card ID (`POST /inbox/{id}/reply {text, card}`). Hub locks per card and never resends a sent card (double taps, one tap on each of two clients, and stale pages all behave the same).
    Drafts submitted after the reply was sent no longer insert cards.
  - **No placeholders**: drafts are sent verbatim with one tap. `inbox/agent.md` and `inbox/draft.md` require omitting unknown information (no more 【】 markers for the user to fill).
    `inbox_draft` rejects drafts containing 【…】 / {{…}} and asks the AI twin to resubmit. Web and Android cards refuse to send drafts still containing 【…】 and ask for edits.
  - iOS still uses local records + the Inbox list item's `done`, not `sent_at` (the list retains processed items only for 3 days, at most the newest 200; reopening an item makes old cards show Send again). To be fixed.

## Steps

| Step | Scope |
| --- | --- |
| 1 | This document and prototype |
| 2 ✅ | hub: migrations (`todos.origin` / `plan_id`, `task_plans`, `assistant_feed`), `task_plan` tool and prompts, start / dismiss plan APIs, feed APIs and pushes, completion / answer / routing hooks, tasks in snapshots |
| 3 ✅ | iOS: models and `FakeHub`, feed merging, plan cards, task cards, dock, question cards, receipts, `@` and routing, task-details sheet, task board |
| 4 ✅ | Instant result cards, main-chat Today card, day sections and collapsing, `tasks.trust` Settings page (message-card task links are separate work) |
| 5 | Web ✅ (`docs/web/web.md`, "Unified main-chat entry (#930)"); Watch ✅ (questions carry `task_title`, displaying "Task · xx" in Needs you; working / waiting counts already exist in Live Activities and Watch and include task Agents); Android ✅ (`docs/android/android.md`, "Unified main-chat entry (#930)") |

## Decisions (2026-11-03)

1. Auto-start boundaries follow the three tiers above; plan cards can remember "Start this kind directly next time."
2. Tasks are to-dos; To-dos and the task board share one view.
3. Main chat uses day sections; task progress stays out of the chat; previous-day task cards collapse to a line. Distant context belongs in compaction and the knowledge base. Side chats remain, with less prominent entry points.
4. Watch and lock screen show only the dock and Needs you.
5. Polish the interface, UI, and interactions to chart-topping App quality: compare light / dark screenshots to the prototype at every step, with smooth state transitions and animations.
6. Do not change or demote Agents or the top-bar switch.
