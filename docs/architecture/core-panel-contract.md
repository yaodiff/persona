# Core-to-panel contract

Who this serves: people who change Persona Core or build a panel on it. The default web panel is the executable
reference consumer; an alternative panel uses the same hub endpoints and wire data, not web UI code.

Last reviewed: 2026-11-25, against `origin/main` `2f5e26f5fd7879d78cdf0dedb940c994206a5c7a`.
This is a source-verified description of the current contract, not a new compatibility promise or a freeze on every
field. HTTP routes and WebSocket names in the tables are guarded against source drift by
`scripts/test/test_core_panel_contract.py`.

## Scope and reading rules

- A panel talks only to the hub. The hub answers from its own snapshots or relays to the manager that owns the data;
  a panel does not import a manager or a feature module.
- Server-first logic, thin clients: any logic that defines shared product truth, domain rules, or must agree across
  clients (state transitions, validation/idempotency, querying/windowing, projections/counts, permissions/affordances,
  orchestration, and cross-device state) belongs in the Go server; panels own presentation, local transient UI state,
  device-native capabilities, and offline/low-latency UX (`docs/architecture/server-first-logic.md`).
- Credential requirements are route-specific. Rows call out `full`-only operations where that restriction is explicit.
  Scoped `agents.read`, `agents.control`, and `agents.permission_mode` credentials open only their registered subsets;
  an answer-only watch has its own smaller allowlist. Authentication and scope rules are in `docs/protocol/protocol.md`.
- JSON fields are snake_case and times are RFC3339. `{mid}` and `{sid}` are the machine and session path parameters as
  registered in Go's `http.ServeMux`; they are not optional literal text. Query switches are described separately from
  the registered path.
- "Snapshot" means current authoritative state. "Delta", "hint", and "receipt" are different things: none is, by
  itself, proof that model work finished.
- The contract below covers sessions, pending requests / asks, tasks, to-dos, panel actions, and their realtime
  transport. Feature-specific pages outside those domains are not made Core by this document.

## Sessions

A `proto.Session` is one machine-owned row, not a transcript. Identity fields are `machine_id` and `session_id`;
hierarchy adds `parent_session_id`, `root_session_id`, `is_subagent`, folded-under metadata, and cross-machine child
references. Execution and display data include `title`, `cwd`, `workspace_cwd`, `repo_root`, `worktree`, `branch`,
`agent`, `launcher`, `model`, `requested_model`, `permission_mode`, `effort`, `service_tier`, `model_pick`,
`pending_switch`, `capabilities`, pane and tab fields, `archived`, and `archive_changed_at`.

There are two state dimensions. The host status is one of `idle`, `working`, `blocked`, `done`, `unknown`, `offline`,
or `interrupted`. The unified panel-facing `state` is one of `needs_you`, `working`, `unread`, `idle`, `offline`, or
`unknown`, with derived background and list flags. `status` is the runtime's host state; `state` is the computed panel
state. The declarations and values are in `server/internal/proto/session.go`, while the default web row type is in
`web/src/lib/types.ts`.

### Reads, detail, and selection

| Operation | HTTP route | Data and state contract | Source evidence |
| --- | --- | --- | --- |
| Read the current inventory | `GET /api/v1/sessions` | Without paging parameters, returns credential-visible current rows, optionally restricted by `machine_id`. Rows are bounded legacy projections and non-default-manager rows carry `manager`. | `server/internal/hub/session_routes.go`, `server/internal/hub/http.go` |
| Read one exact session row | `GET /api/v1/sessions` | With `machine_id`, `session_id`, no `since`, and no `cursor`, performs an exact lookup. The default web validates one row, zero removals, a stable revision, and matching identity; it does not accept an unrelated first row. | `server/internal/hub/session_routes.go`, `server/internal/hub/sessions_list.go`, `web/src/lib/runtime-session.ts` |
| Read transcript history | `GET /api/v1/sessions/{mid}/{sid}/items` | Forwards `session.items`; supports paging with `before_seq`, `after_seq`, `from_seq`, `through_seq`, `turns`, `limit`, lazy images, folded work, strict reads, and a bounded user-input index. Returns items, sequence coverage, and backlog markers. | `server/internal/hub/session_routes.go`, `server/internal/hub/http.go` |
| Read one item whole | `GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full` | Returns the complete item when a page clipped long text. A strict read returns 409 when the node cannot guarantee the same source. | `server/internal/hub/session_routes.go`, `server/internal/hub/http.go` |
| Read an item image | `GET /api/v1/sessions/{mid}/{sid}/items/{iid}/image` | Returns immutable image bytes; an item image never changes, so the client may cache it. | `server/internal/hub/session_routes.go`, `server/internal/hub/http.go` |
| Read an item visualization | `GET /api/v1/sessions/{mid}/{sid}/items/{iid}/visualization` | Returns the self-contained HTML fragment for a visualization item. | `server/internal/hub/session_routes.go`, `server/internal/hub/http.go` |
| Read legacy runtime detail | `GET /api/v1/sessions/{mid}/{sid}/info` | Forwards `session.info`. The default web uses it only when exact paged lookup is unsupported; it never treats this response as a replacement list row. | `server/internal/hub/session_routes.go`, `web/src/lib/runtime-session.ts` |
| Read archived sessions | `GET /api/v1/sessions/archived` | Returns recoverable archive rows with newest activity first. The optional machine filter and merged-manager rules are described in `docs/protocol/protocol.md`; archives are excluded from normal lists, deltas, and paged indexes. | `server/internal/assistant/agent_archives.go` |

There is no server route for merely selecting or opening a row in the panel. That selection is client-side navigation
state (`web/src/lib/router.ts`, `web/src/lib/panel.ts`). Server state changes only when the panel starts, attaches,
changes modes, switches logical segments, sends input, archives, or deletes.

### Starting and switching

| Operation | HTTP route | Data and state contract | Source evidence |
| --- | --- | --- | --- |
| Start or resume an Agent | `POST /api/v1/sessions` | Body includes machine, working directory, launcher and agent type, optional prompt, model, permission mode, effort, speed, ultracode, goal, tab name, images, resume id, workspace and project references, and async start. It uses the `start` operation receipt; an accepted async start finishes even if the caller disconnects. | `server/internal/hub/session_routes.go`, `server/internal/hub/http.go`, `web/src/lib/new-agent-delivery.ts` |
| Read logical switch availability | `GET /api/v1/assistant/conversations/{id}/segments` | Full role only; `machine_id` is required. Returns `enabled`, current `epoch`, any in-flight switch, and whether supervision applies. An unavailable or private segment is refused rather than guessed from a launcher. | `server/internal/assistant/logical_switch.go` |
| Switch a logical conversation | `POST /api/v1/assistant/conversations/{id}/switch` | Full role only. Body carries `operation_id`, `expected_epoch`, source and target machines, launcher, model, effort, and permission mode. The epoch is an optimistic concurrency fence; preparing does not send model input. | `server/internal/assistant/logical_switch.go`, `web/src/lib/conversation-switch.ts` |
| Read switch operation state | `GET /api/v1/assistant/conversations/{id}/switch/{operation}` | Full role only. Returns the operation phase, expected epoch, and error so a retry after uncertainty keeps the same operation rather than starting another. | `server/internal/assistant/logical_switch.go` |
| Change a running session's modes | `POST /api/v1/sessions/{mid}/{sid}/modes` | Changes model, permission, effort, service tier, ultracode, or launcher/engine fields. Launcher and engine switches can become `pending_switch` until the next message; the launcher `from` token detects stale or in-flight switches. | `server/internal/hub/session_routes.go`, `web/src/lib/session-panel.ts` |

The default web prepares a logical switch only when the segment endpoint reports an enabled feature and a valid
positive epoch. The same operation ID is retained across retries; the request does not carry a prompt.

### Archive and seen state

| Operation | HTTP route | Data and state contract | Source evidence |
| --- | --- | --- | --- |
| Archive | `POST /api/v1/sessions/{mid}/{sid}/archive` | Moves the row out of normal lists without deleting the transcript. Query `close=1` also closes the Agent; `force=1` is limited to an eligible stuck confirmation and never approves its permission or plan. Busy sessions return 409. | `server/internal/assistant/agent_archives.go`, `web/src/lib/agent-archives.ts` |
| Restore from archive | `POST /api/v1/sessions/{mid}/{sid}/unarchive` | Restores normal visibility without recreating source files. Repeated visibility requests preserve the requested state. | `server/internal/assistant/agent_archives.go`, `web/src/lib/agent-archives.ts` |
| Mark a session seen | `POST /api/v1/sessions/{mid}/{sid}/seen` | Records the owner's read observation and returns `{ok:true}`. It changes seen state, not the transcript. | `server/internal/assistant/fleet_routes.go`, `web/src/lib/store.ts` |

Input, interrupt, attach, keys, rewind, close, queue, rename, deletion, and screen reads are panel actions and have
their idempotency and concurrency rules in Actions below. Realtime list, transcript, deletion, and send-state events
are in Realtime events below.


## Pending requests / asks

A pending request is a `proto.Interaction`: its panel-facing identity and rendering data include `id`, `machine_id`,
`session_id`, `kind`, `questions`, `fields`, `elicitation`, `can_cancel`, `can_clarify`, `resumable`, `expires_at`,
and `seen_at`. Answers use `proto.Resolution`; it is a union of explicit actions, answers, approval fields, edited
text, keys, options, and values, not a free-form command channel. The wire declarations are in
`server/internal/proto/interaction.go`.

| Operation | HTTP route | Data and state contract | Source evidence |
| --- | --- | --- | --- |
| List pending | `GET /api/v1/interactions` | Returns `{interactions}` after credential and privacy filtering. This is a current snapshot, not a cursor. | `server/internal/hub/http.go` |
| Read ended and delegated cards | `GET /api/v1/interactions/history` | Returns interaction history, including the reanswer flow; it does not make an ended card pending again. | `server/internal/assistant/fleet_routes.go` |
| Answer, approve, cancel, or dismiss | `POST /api/v1/interactions/{id}/resolve` | Accepts a `Resolution`. A repeated compatible resolution can return `200` with `already:true`; an expired, withdrawn, or otherwise ended card returns `410` with its known status. A changed prompt returns `409 prompt_changed` without applying the answer. | `server/internal/assistant/fleet_routes.go` |
| Ask a clarification | `POST /api/v1/interactions/{id}/clarify` | Body is only `{text,client_msg_id}`. Success is `{ok:true,pending:true}`: the approval remains pending. Concurrent questions serialize, and a final-answer race waits for the current clarification attempt. | `server/internal/assistant/fleet_routes.go`, `server/internal/assistant/approval_clarify.go` |
| Choose a notified option | `POST /api/v1/interactions/{id}/choose` | Body is `{option?,options?,rev,client_id}`; the two option forms are mutually exclusive. `rev` must match the current question or the hub returns `409 ask_changed`; an ended card returns `409 not_pending`. | `server/internal/hub/fleet_link.go`, `server/internal/hub/choose.go` |
| Change an assistant answer | `POST /api/v1/interactions/{id}/reanswer` | Body is `{answers}`. A hash of the card and canonical answer suppresses duplicate delivery only in process memory; this is not a durable operation receipt. | `server/internal/assistant/fleet_routes.go`, `server/internal/assistant/fleet_delegate.go` |
| Mark a timed card seen | `POST /api/v1/interactions/{id}/seen` | Records the full-device observation and returns `{ok:true}`; it does not resolve the request. | `server/internal/assistant/fleet_routes.go` |

The default web answer path permits one in-flight answer per card: an identical answer shares its promise, while a
different answer is refused locally until that attempt finishes. See `web/src/lib/card-answer.ts`,
`web/src/lib/approval-question.ts`, and `web/src/lib/delegate.ts`.

## Tasks

### Plans, task cards, and feed reads

A `TaskPlan` contains `space?`, `id`, `conversation`, `tasks`, `state`, `created_at`, `updated_at`, and optional computed
`suggestion`. Its durable states are `open`, `started`, and `dismissed`; withdrawal is represented as `dismissed`.
`PlanTask` carries execution fields (`title`, `prompt`, `machine`, `cwd`, `launcher`, `model`, `permission`, `workspace`,
`role`, `parent`), supervision, scope, organization, and post-start fields such as `todo_id`, `error`, and `queue`.
The declarations are in `server/internal/assistant/tasks.go`; the default web's smaller wire view is in
`web/src/lib/feed.ts`.

Plans originate from the assistant's `task_plan` tool, not from a panel `POST /task-plans` endpoint. A plan handed to
the owner is persisted and broadcast as `task_plan`, but nothing has started until Start succeeds.

| Operation | HTTP route | Data and state contract | Source evidence |
| --- | --- | --- | --- |
| Read a conversation feed | `GET /api/v1/assistant/conversations/{id}/feed` | Returns `items`, `plans`, `task_cards`, `views`, `view_actions`, and `view_blocks`; main-chat reads can add `pending_plan_ids`. `paginate=1` adds an opaque `next_cursor`; later pages use `cursor` without `since`. Invalid or cross-conversation cursors are rejected. | `server/internal/assistant/tasks.go` |
| Read a plan | `GET /api/v1/task-plans/{id}` | Returns `{plan:TaskPlan}`. | `server/internal/assistant/tasks.go` |
| Start a plan | `POST /api/v1/task-plans/{id}/start` | Full role only. Optional `tasks`, `remember`, and `schedule_grant` start the already authored plan. Card edits cover execution settings, not `goal`, `out_of_bounds`, or `scope_revision`. Only one compare-and-set from `open` can win; another start returns 409. Starting binds or creates to-dos before Agents start asynchronously. | `server/internal/assistant/tasks.go`, `server/internal/assistant/tasks_start.go`, `server/internal/assistant/tasks_plan.go` |
| Dismiss or withdraw a plan | `POST /api/v1/task-plans/{id}/dismiss` | Full role only. Only an `open` plan changes state; repeating after start, dismissal, or withdrawal returns 200 with the current plan. | `server/internal/assistant/tasks.go` |
| Preview plan workspaces | `POST /api/v1/task-plans/{id}/workspace-preview` | Runs the separate workspace-selection preview used by the default panel's workspace controls. | `server/internal/assistant/tasks.go`, `web/src/views/assistant/TaskWorkspaceControls.tsx` |
| Retry a task | `POST /api/v1/todos/{id}/retry` | Full role only. An empty body retries as planned; optional `launcher` and `model` change execution. Returns `{todo:Todo}`. | `server/internal/assistant/tasks.go` |
| Answer a retained start question | `POST /api/v1/todos/{id}/start/answer` | Full role only. `{choice:"n"}` answers the failed start's retained question and returns `{todo:Todo}`. | `server/internal/assistant/tasks.go` |
| Assign a supervisor | `POST /api/v1/assistant/agents/{mid}/{sid}/supervisor` | Full role only. `{supervisor?,title?}` creates a supervised task for an unsupervised Agent or moves an existing task without changing its to-do identity. An empty supervisor selects main. | `server/internal/assistant/agent_route.go` |
| Reorganize tasks | `POST /api/v1/todos/reorganize` | Full role only. `ids`, optional `part_of` and `direction_id`, plus required `include_children` and `transfer_supervision` return updated to-dos and optional transferred IDs. | `server/internal/assistant/todo_reorganize.go` |
| Route a task-directed message | `POST /api/v1/assistant/conversations/{id}/route` | Full role only. Requires `todo_id` and nonempty `text`; optional `asked_at` selects decision-answer handling. Supports the to-do operation receipt described below. | `server/internal/assistant/tasks.go` |

`TaskCard` is the feed projection `{id,todo_id?,plan_id,index,at,updated_at,state,reason?,receipt_id,report_id}`.
Implemented states include `not_started`, `working`, `waiting`, `failed`, `cancelled`, `done`, `accepted`, `timed_out`,
`needs_you`, `interrupted`, `usage_limit`, `unread`, and `followup`. Card pages are upserts, not deletion snapshots;
an empty page does not remove cards. The server projection is in `server/internal/assistant/task_cards.go`, and the
web merge is in `web/src/lib/task-cards.ts`.

### Supervision, acceptance, and scope

`Todo.supervision` is optional. Its Core data includes the supervisor identity, `done_when`, `criteria`,
`expect_minutes`, review `state`, parking, rounds, review entries, waits, and structured progress. Review states are
not one boolean: `pending_review`, `user_check`, `accepted`, `rework`, `needs_user`, `cancelled`, parked, and the
timeout states `timeout_closing` and `timed_out` have distinct meanings. See `server/internal/assistant/supervise.go`.

A completed stretch enters `pending_review`, not immediate success. Ordinary accepted or cancelled verdicts close the
to-do; accepted plus `user_check` deliberately leaves it open until the owner passes or gives feedback. Duplicate
closure is a no-op. Reopening changes the effective state without erasing the review ledger. These transitions are in
`server/internal/assistant/supervise_events.go`, `server/internal/assistant/supervise_review.go`, and
`server/internal/db/todo_supervision.go`.

| Operation | HTTP route | Acceptance and concurrency contract | Source evidence |
| --- | --- | --- | --- |
| Accept or answer an owner check | `POST /api/v1/todos/{id}/accept` | Full role only. `asked_at` is required and fences the review round; optional `option`, `response`, or expired-question `answers` supply the answer. Stale or already handled rounds return 409. A recorded but undelivered answer reports `delivered:"none"`; uncertain delivery is explicit rather than safe to repeat blindly. | `server/internal/assistant/todo_accept_route.go`, `server/internal/assistant/acceptance_reply.go` |
| Answer a structured task ask | `POST /api/v1/todos/{id}/asks/{ask}/answer` | Full role only. At most one of `option`, `response`, `text`, or `outcome`; `outcome:"undo"` also requires `receipt`. Returns the ask, outcome, optional delivery and receipt, and current to-do. Supports the to-do operation receipt and `todo_send_state`. | `server/internal/assistant/todo_asks_routes.go`, `server/internal/assistantwire/todo_view_gen.go` |
| Dismiss an owner decision | `POST /api/v1/todos/{id}/decision-dismiss` | `{review_at}` hides only the current open `needs_user` review and returns `{review_at,dismissal_id}`. Repeating the same dismissal returns its receipt; it does not close the task. | `server/internal/assistantwire/bus.go`, `server/internal/assistant/task_review_dismissals.go` |
| Undo a decision dismissal | `POST /api/v1/todos/{id}/decision-undo-dismiss` | `{review_at,dismissal_id}` restores that review. An old receipt or changed review returns 409; repeated Undo is idempotent. Dismiss, Undo, and acceptance share per-task action locking. | `server/internal/assistantwire/bus.go`, `server/internal/assistant/task_review_dismissals.go` |
| Check or apply completion | `POST /api/v1/todos/{id}/done-check` | Returns diagnostic `verdict`, `reason`, `would_close`, `skip`, and `applied`, with optional replies, signals, delivery, and stage data. The default web sends `{apply:true}`. | `server/internal/assistant/done.go`, `web/src/lib/task-completion-check.ts` |

Scope fields are `goal`, `out_of_bounds`, `done_when`, `expect_minutes`, `continues`, `continues_why`,
`scope_revision`, and Core's generated `brief`. **There is no direct panel HTTP scope editor and no separate panel
cancel endpoint.** After dispatch, scope changes arrive through supervisor `task_scope` events or task-directed Agent
messages; cancellation is a supervisor review verdict, not `PATCH done` or DELETE.

Scope commits and their propagation are ordered so an older writer cannot overwrite newer criteria. A criteria change
can reopen accepted or cancelled work. `scope_revision` fences continuations: when it changes, the panel or supervisor
must re-read and re-plan rather than reuse the old brief. See `server/internal/assistant/task_brief_scope.go`,
`server/internal/assistant/task_brief_plan.go`, and `server/internal/assistant/task_brief.go`.

The default web consumes plans and controls through `web/src/lib/feed.ts`, `web/src/lib/supervision.ts`, and
`web/src/lib/brief.ts`. Its current supervision and pull-request TypeScript types are narrower than Core's additive
wire data, and its local busy flags are only duplicate-click protection, not server idempotency keys.


## To-dos

### Rows, reads, and CRUD

A to-do row carries identity, title and notes, scheduling, dependency and direction fields, optional supervision,
completion, delivery, observations, and repository-related data. List rows can additionally carry
`prerequisite_blockers`, `ask`, `decision_review`, `orphaned`, and `view`. The server declaration is in
`server/internal/assistant/todos.go`; the default web mirror is in `web/src/lib/todos.ts`.

Unless paginated, the hub merges the list across managers and stamps non-default-manager rows with `manager`. Detail
and mutation routes resolve the owning manager before forwarding. A non-default-manager WebSocket broadcast likewise
carries a top-level `manager`; the built-in assistant's broadcasts omit it (`server/internal/hub/ownership_lists.go`,
`server/internal/hub/assistant_link.go`, and `server/internal/hub/bus_broadcast.go`).

| Operation | HTTP route | Data and state contract | Source evidence |
| --- | --- | --- | --- |
| List to-dos | `GET /api/v1/todos` | Complete mode returns `todos`, `counts`, and optional `shared_blockers`, newest `created_at` first with ID ascending for ties. `limit` or `cursor` selects the page contract below. | `server/internal/assistant/todos.go`, `server/internal/assistant/todo_view_routes.go` |
| Read the lightweight revision | `GET /api/v1/todos/revision` | Returns `{revision}` or 304 on `If-None-Match`; the tag also appears in `ETag` and `X-Persona-Todos-Revision`. This is not the page cursor. | `server/internal/assistant/todos.go`, `server/internal/assistant/todo_revision.go` |
| Read one to-do | `GET /api/v1/todos/{id}` | Returns `{todo}`; optional detail adds `rounds`, `rounds_total`, `recap`, and `results`. | `server/internal/assistant/todo_view_routes.go` |
| Read sidebar places | `GET /api/v1/todos/places` | Returns sidebar places and whole-list counts. | `server/internal/assistant/todo_view_routes.go` |
| Read supervision timeline | `GET /api/v1/todos/{id}/timeline` | `before` and `limit` page rounds and optional `more`. `before` is a stable round ID, not the to-do list cursor. | `server/internal/assistant/todo_timeline.go` |
| Create | `POST /api/v1/todos` | Accepts title, notes, images, project, kind, acceptance requirement, repository bindings, responsible party, scheduling, priority, audience, and direction fields. Returns `{todo}`. With images, `X-Persona-Op-Id` deduplicates creation; image-less create has no idempotency receipt and can duplicate on retry. | `server/internal/assistant/todos.go`, `server/internal/hub/todo_receipts.go` |
| Edit | `PATCH /api/v1/todos/{id}` | Pointer and optional fields change only when supplied, including title, notes, `done`, dependencies, acceptance, responsibility, scheduling, audience, and direction. Ordinary PATCH has no expected-revision precondition; supplied text fields replace values rather than three-way merge. | `server/internal/assistant/todos.go`, `server/internal/db/todos.go` |
| Delete | `DELETE /api/v1/todos/{id}` | Full role only. Deletes the to-do and links, not Agent sessions. Repeated deletion of an absent row returns 404 rather than an idempotent 200. | `server/internal/assistant/todos.go` |
| Link an Agent | `PUT /api/v1/todos/{id}/agents/{mid}/{sid}` | Links an existing Agent and returns `{ok:true}`; a completed to-do refuses with 409. | `server/internal/assistant/todos.go` |
| Unlink an Agent | `DELETE /api/v1/todos/{id}/agents/{mid}/{sid}` | Soft-releases the link with release or dismissal metadata and returns `{ok:true}`. | `server/internal/assistant/todos.go` |
| Link a space | `PUT /api/v1/todos/{id}/spaces/{mid}/{space...}` | Links the whole space, returns `{ok:true,linked:N}`, and refuses completed rows with 409. | `server/internal/assistant/todos.go` |
| Unlink a space | `DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}` | Removes the space link and returns `{ok:true}`. | `server/internal/assistant/todos.go` |
| Mark seen | `PUT /api/v1/todos/{id}/seen` | Sets `seen_at` without changing `updated_at`, returns `{todo}`, and emits the `todos` invalidation. | `server/internal/assistantwire/bus.go`, `web/src/lib/todo-seen.ts` |
| Clear seen | `DELETE /api/v1/todos/{id}/seen` | Clears `seen_at` without changing `updated_at`, returns `{todo}`, and emits the same invalidation. | `server/internal/assistantwire/bus.go`, `web/src/lib/todo-seen.ts` |
| Bind a chat thread | `PUT /api/v1/todos/{id}/thread` | Binds `{channel,chat_id,thread_id,title?}` and returns `{todo}`. | `server/internal/assistant/todo_thread.go` |
| Unbind a chat thread | `DELETE /api/v1/todos/{id}/thread` | Removes the thread binding and returns `{todo}`; this is the thread operation exposed by the current default web. | `server/internal/assistant/todo_thread.go`, `web/src/lib/todo-thread.ts` |
| Mark restored | `PUT /api/v1/todos/{id}/restored` | Changes the restored mark. This registered Core mutation is not called by the current default web. | `server/internal/assistant/todo_marks.go` |
| Move to someday | `PUT /api/v1/todos/{id}/someday` | Sets the someday mark. This registered Core mutation is not called by the current default web. | `server/internal/assistant/todo_marks.go` |
| Clear someday | `DELETE /api/v1/todos/{id}/someday` | Clears the someday mark. This registered Core mutation is not called by the current default web. | `server/internal/assistant/todo_marks.go` |
| Retitle a batch | `POST /api/v1/todos/retitle` | Applies the batch retitle operation. This registered Core mutation is not called by the current default web. | `server/internal/assistant/todo_titles.go` |
| Import reminders | `POST /api/v1/todos/import/reminders` | Runs the reminders import. This registered Core mutation is not called by the current default web. | `server/internal/assistant/todo_imports.go` |

### Stable page cursor

Page mode starts when `limit` or `cursor` is present. The default limit is 50 and the valid range is 1–200. The cursor
is unpadded base64url JSON containing exactly `created_at` and `id`; malformed, oversized, unknown-field, or trailing
content is rejected. Ordering is `created_at` descending and `id` ascending. The next page accepts older rows or
same-time rows with a greater ID.

This is keyset pagination, not an offset and not a frozen snapshot. A deleted anchor remains valid, and this cursor has
no expiry check. Responses carry `todos`, whole-projection `counts`, optional `shared_blockers`, `next_cursor`,
`has_more`, and optional `oversized`. A page is bounded by both its row limit and a 3 MiB row budget; one oversized row
is returned whole with `oversized:true`. See `server/internal/assistant/todo_pagination.go`.

Pages are never merged across managers. The caller selects one with `X-Persona-Manager` or `?manager=`, except when
exactly one installed manager exists. Otherwise the hub returns `400 todos_pagination_manager_required`; an older
manager returns `501 todos_pagination_unsupported` (`server/internal/hub/ownership_lists.go`). The current default web
still requests the complete merged list without `limit` or `cursor` in `web/src/lib/store.ts`, so it does not yet
consume this stable cursor contract.

### Repository, issue, and pull-request associations

| Operation | HTTP route | Association and concurrency contract | Source evidence |
| --- | --- | --- | --- |
| Read associations | `GET /api/v1/todos/{id}/repositories` | Returns a `TaskSet` with `schema_version`, `manager`, `todo_id`, `revision`, and `repositories`. | `server/internal/assistant/todo_repositories.go`, `server/internal/repositorywire/task.go` |
| Replace all associations | `PUT /api/v1/todos/{id}/repositories` | Requires `expected_revision`, `expected_catalog_revision`, and `repositories`; atomically replaces the set. | `server/internal/assistant/todo_repositories.go`, `server/internal/repositorywire/task.go` |
| Replace one repository's links | `PUT /api/v1/todos/{id}/repositories/{repo_id}/links` | Requires `expected_revision` and `links`; replaces only that repository's issue and pull-request links. | `server/internal/assistant/todo_repositories.go`, `server/internal/repositorywire/task.go` |

Repository input includes `repo_id`, optional `directory_alias`, `requested_ref`, and optional `links`. Omitted `links`
preserve existing values; explicit `[]` clears them. A link accepts a URL or structured `kind:"issue"` /
`kind:"pull_request"` with a decimal-string `number`, plus optional `title` and `role`. `delivery` is the default role;
`reference` is context and never proves completion (`server/internal/repositorywire/task.go`).

Association writes use exact-revision compare-and-set. A mismatch returns
`task_repositories_revision_conflict` or `task_repositories_catalog_conflict`, normally 409; replaying the stale write
is not success. The default editor uses GET plus whole-set PUT and requires the returned revision to increment by one
(`web/src/lib/task-repositories.ts`). Legacy `pr_url`, `pr_status`, and `other_prs` remain separate forge observations;
they do not advance lifecycle state or prove acceptance (`server/internal/assistant/pr_status.go`).

### Completion and current panel gaps

`done_at` is a closure timestamp, not proof of success: a failed start also closes with `done_at` plus `start_error`.
`PATCH done:true` stamps closure once; `done:false` reopens. Acceptance and closure are different, especially for
accepted plus `user_check`. Cancellation is exposed through `cancelled_at`, `cancelled_reason`, or legacy supervision
state; response formatting can retain `done_by:"review"` for compatibility. Stage, delivery, checks, and pull-request
observations do not replace these fields (`server/internal/db/todos.go`, `server/internal/assistant/todos.go`, and
`web/src/lib/todos.ts`).

Only image-bearing create, structured `/asks/{ask}/answer`, and conversation `/route` have the to-do operation receipt
wrapper (`server/internal/hub/todo_receipts.go`). `todo_send_state` reports transport for those operations, not task
execution, and is not replayed. The current production web does not declare or consume `todo_send_state`; its `/accept`
and `/route` calls do not send operation IDs, and it currently uses `/accept` rather than the newer structured ask
route. These are default-panel coverage gaps, not extra promises inferred from the presence of a Core route.


## Actions

### Operation receipts and message identity

Selected writes accept `X-Persona-Op-Id: <UUID>`. The operation kind, normalized path and query, and normalized JSON
parameters form its digest. Reusing the ID with different parameters or from another device returns
`409 op_id_reused`. Without the header, legacy behavior remains; this is not a universal idempotency wrapper on every
route.

`server/internal/hub/clientops.go` and `server/internal/db/client_ops.go` define the receipt protocol:

- an atomic insert lets only one caller execute; a concurrent duplicate waits instead of executing again;
- a duplicate waits at most 10 seconds, then receives `202 {state:"pending"}`;
- a stored `done` or `failed` result replays its original status and body; an uncertain outcome is
  `202 {state:"unknown"}` and must not be replaced with a fresh operation;
- startup turns abandoned `pending` receipts into `unknown` without re-executing them; receipts are retained for 30
  days.

The default web `apiOp` creates the UUID outside its retry loop and reuses it only for transient transport gaps in
`web/src/lib/ops.ts` and `web/src/lib/api.ts`.

| Operation | HTTP route | Data and state contract | Source evidence |
| --- | --- | --- | --- |
| Read an operation receipt | `GET /api/v1/ops/{id}` | Returns `{id,device_id,kind,target,digest,state,result,created_at,updated_at}`. `result` stores the original HTTP status, body bytes, and optional content type. A different device cannot read it. | `server/internal/hub/http.go`, `server/internal/db/client_ops.go` |

Messages have a second boundary, `client_msg_id`. Concurrent sends with that ID join one in-process flight, while the
durable `sent_messages` ledger records `sending`, `delivered`, or `unknown`; a definite no-send settlement releases
the claim so retry remains possible. `message_send_state` is advisory transport feedback, not execution state. See
`server/internal/hub/send.go` and `server/internal/db/sent.go`.

### Session controls

The following table is the panel-to-Core action surface not already listed as a read or an ask answer. "Receipt"
means the opt-in operation protocol above, not an unconditional guarantee for callers that omit the header.

| Action | HTTP route | Data and concurrency contract | Source evidence |
| --- | --- | --- | --- |
| Send input | `POST /api/v1/sessions/{mid}/{sid}/messages` | Carries text, optional images and launcher/model choices, and `client_msg_id`. Uses a `message` receipt plus message-ID dedupe. | `server/internal/hub/session_routes.go`, `server/internal/hub/send.go` |
| Send to a terminal Agent | `POST /api/v1/sessions/{mid}/{sid}/agent-messages` | Full-device Agent-only send with `{text,hold_when_away}`; the hold declaration is validated before delivery. This route requires the operation ID used by its `message` receipt. | `server/internal/hub/session_routes.go`, `server/internal/hub/send.go` |
| Interrupt a turn | `POST /api/v1/sessions/{mid}/{sid}/interrupt` | Stops the current turn and retires pending follow-ups. Uses an `interrupt` receipt. | `server/internal/hub/session_routes.go` |
| Attach or focus | `POST /api/v1/sessions/{mid}/{sid}/attach` | Focuses the existing session. It has no operation receipt; repeating it focuses the same pane. | `server/internal/hub/session_routes.go` |
| Send terminal keys | `POST /api/v1/sessions/{mid}/{sid}/keys` | Forwards terminal keys with a `keys` receipt so a retry does not press them twice. | `server/internal/hub/session_routes.go` |
| Rewind | `POST /api/v1/sessions/{mid}/{sid}/rewind` | Body selects the prompt occurrence and whether to restore the conversation or code and conversation. Full devices only; uses a `rewind` receipt. | `server/internal/hub/session_routes.go`, `server/internal/hub/rewind.go` |
| Restore the configured model | `POST /api/v1/sessions/{mid}/{sid}/restore-model` | Restores the configured model after a fallback and returns the node's restore result. Uses a `restore_model` receipt. | `server/internal/hub/session_routes.go`, `server/internal/hub/restore_model.go` |
| Continue in a new chat | `POST /api/v1/sessions/{mid}/{sid}/continue-in-new-chat` | Starts from the prior summary and recent messages. Uses a `start` receipt; an unknown result must not trigger a replacement start. | `server/internal/hub/session_routes.go` |
| Fork | `POST /api/v1/sessions/{mid}/{sid}/fork` | Forks at `at_item_id` and returns the new `session_id`. Uses a `fork` receipt. | `server/internal/hub/session_routes.go` |
| Close the Agent | `POST /api/v1/sessions/{mid}/{sid}/close` | Closes the running Agent while retaining its conversation. Uses a `close` receipt. | `server/internal/hub/session_routes.go` |
| Replace queued input | `POST /api/v1/sessions/{mid}/{sid}/queue` | `{keep}` replaces the queue in the supplied order. There is no operation receipt; queue races are reported as `queue_taken` or `queue_changed`. The web serializes edits behind sends and checks observed queue IDs. | `server/internal/hub/session_routes.go`, `web/src/views/chat/QueuePanel.tsx` |
| Send queued input now | `POST /api/v1/sessions/{mid}/{sid}/queue/send-now` | Sends the current queued item with a `send_now` receipt, preventing duplicate delivery. | `server/internal/hub/session_routes.go` |
| Rename | `POST /api/v1/sessions/{mid}/{sid}/rename` | Changes `title` and/or `tab_name`. Uses a `rename` receipt. | `server/internal/hub/session_routes.go` |
| Delete a session | `DELETE /api/v1/sessions/{mid}/{sid}` | Deletes only an eligible offline session. Running, starting, blocked, assistant, unknown, and held-message cases are refused. The persisted deletion mark is idempotent in effect, without an operation receipt. | `server/internal/hub/session_routes.go`, `server/internal/hub/deleted_sessions.go` |
| Change modes | `POST /api/v1/sessions/{mid}/{sid}/modes` | Changes permission, model, effort, service tier, ultracode, or launcher fields. Uses a `modes` receipt. Launcher `from` is an optimistic-concurrency token; a stale or in-flight switch returns `switch_stale`. | `server/internal/hub/session_routes.go`, `server/internal/hub/ultracode.go`, `server/internal/node/launcher_switch.go` |
| Mark a session seen | `POST /api/v1/sessions/{mid}/{sid}/seen` | Sets the seen mark and returns `{ok:true}`. It is naturally idempotent and has no operation receipt. | `server/internal/assistant/fleet_routes.go` |
| Read earlier screen content | `POST /api/v1/sessions/{mid}/{sid}/screen/before` | Requests read-only scrollback; it changes no session state and has no receipt. | `server/internal/hub/session_routes.go` |
| Archive | `POST /api/v1/sessions/{mid}/{sid}/archive` | Persists archived visibility without deleting the transcript. `close=1` also requests close; `force=1` handles an eligible stuck confirmation without approving it. Archive/close completion is generation-fenced so an older close cannot overwrite a newer restore. | `server/internal/assistant/agent_archives.go` |
| Restore an archived session | `POST /api/v1/sessions/{mid}/{sid}/unarchive` | Restores normal-list visibility without recreating source files. Repeated archive and restore requests preserve the requested visibility. | `server/internal/assistant/agent_archives.go` |
| Request an automatic name | `POST /api/v1/sessions/{mid}/{sid}/autoname` | Concurrent requests for one session share a model call and result. A hand-written rename racing that call returns `409 renamed_meanwhile`. | `server/internal/assistant/agent_naming_request.go` |

## Realtime events

### Connection and panel-to-Core frames

The endpoint is `GET /api/v1/ws`; every application frame is a JSON object with a `type`. WebSocket protocol keepalive
and the application `ping` / `pong` below are different mechanisms.

| Purpose | HTTP route | Contract | Source evidence |
| --- | --- | --- | --- |
| Open the application socket | `GET /api/v1/ws` | Authenticates the device and enters the snapshot, replay, and subscription flow below. | `server/internal/hub/http.go` |

| Direction | WS event | Data and synchronization contract | Source evidence |
| --- | --- | --- | --- |
| Panel to Core | `client_hello` | Carries device, platform, version, build, OS, features, locale, and optional heartbeat settings. A later hello replaces the feature set. | `server/internal/hub/http.go`, `server/internal/hub/clientmeta.go` |
| Panel to Core | `subscribe` | `{machine_id,session_id,since_seq?,lazy_images?,fold_work?}` subscribes to transcript items. `since_seq` asks for later items; without it, the hub replays the latest two turns. | `server/internal/hub/http.go` |
| Panel to Core | `unsubscribe` | `{machine_id,session_id}` removes this connection's transcript watch. | `server/internal/hub/http.go` |
| Panel to Core | `seen` | `{machine_id,session_id}` has the same effect as the HTTP seen action. | `server/internal/hub/http.go` |
| Panel to Core | `interaction_seen` | `{interaction_id}` forwards a full-device card observation. | `server/internal/hub/http.go` |
| Panel to Core | `ping` | Application keepalive with an echoed string `id`. | `server/internal/hub/http.go`, `server/internal/hub/clientmeta.go` |
| Panel to Core | `terminal_subscribe` | Starts the separate terminal transport with machine/session identity and optional `epoch` and `after_seq`; it does not use transcript `since_seq` or the broadcast event log. | `server/internal/hub/http.go` |

### Core-to-panel events

| Direction | WS event | Data and synchronization contract | Source evidence |
| --- | --- | --- | --- |
| Core to panel | `hello` | Includes `event_stream`, `event_seq`, and `resumed`, plus negotiated features and module flags. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `client_hello_ack` | Acknowledges the applied client capabilities and heartbeat negotiation. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `pong` | Answers the application `ping` with the same `id`. | `server/internal/hub/bus_broadcast.go`, `server/internal/hub/clientmeta.go` |
| Core to panel | `error` | Reports a connection or request error; it is not a domain state transition. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `machines` | Sends the full computer snapshot used to locate session-owning managers. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `sessions` | `{machine_id,sessions}` is the full session set for one machine. Initial snapshots are connection state, not replayed history. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `sessions_delta` | `{machine_id,upsert,remove}` carries whole changed rows and removed IDs. It is opt-in and is not in the numbered broadcast log; reconnect starts from a fresh snapshot. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `sessions_list` | A revision invalidation hint for the paged list, with `since` and `changed` when negotiated. It is not itself a baseline and is not replayed. | `server/internal/hub/bus_broadcast.go`, `server/internal/hub/sessions_list.go` |
| Core to panel | `interaction` | `{interaction}` creates or replaces a pending card by ID. Current pending snapshots are authoritative for elicitation cards. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `interaction_resolved` | `{id}` and optional answer metadata remove the pending card. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `interaction_reanswered` | `{id,resolution:{answers}}` reports a changed assistant answer. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `revs` | `{machine_id,revs}` gives transcript-growth hints by session, not transcript content. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `items` | Carries transcript items plus `history`, sequence coverage, backlog, and pagination markers. It has its own replay path and is not in the broadcast event log. | `server/internal/hub/bus_broadcast.go`, `server/internal/hub/http.go` |
| Core to panel | `delta` | `{machine_id,session_id,text}` carries current whole live text; an empty string ends generation. It is transient and is replaced by transcript items. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `live` | Carries the negotiated live-session view for subscribers of `session_live`. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `message_send_state` | Advisory `{machine_id,session_id,client_msg_id,state,at}` transport feedback: `processing`, `delivered`, `failed`, `unknown`, or `kept`. It is not replayed and is not proof of model progress. | `server/internal/proto/message_send.go` |
| Core to panel | `todo_send_state` | Advisory delivery state for a to-do answer or route, distinct from the to-do's own work and acceptance state. | `server/internal/proto/message_send.go` |
| Core to panel | `conversation_deleted` | `{machine_id,session_id}` is a permanent deletion tombstone; the panel drops cached transcript and outgoing state and refuses further replay. | `server/internal/hub/bus_broadcast.go` |
| Core to panel | `todos` | A revision-aware invalidation after a to-do, link, or seen-state change. The manager coalesces writes for 100 milliseconds; the frame is not the complete to-do list, so the panel refreshes with its current revision. | `server/internal/hub/bus_broadcast.go`, `web/src/lib/todo-pushes.ts` |
| Core to panel | `task_plan` | Carries the complete `plan` when a plan is handed out, started, dismissed or withdrawn, or a task start fails. The panel upserts it by plan ID. | `server/internal/hub/bus_broadcast.go`, `server/internal/assistant/tasks_start.go` |
| Core to panel | `assistant_feed` | Carries `conversation` and one feed `item` for plans, task cards, views, receipts, and related assistant-feed changes. The panel merges by item ID. | `server/internal/hub/bus_broadcast.go`, `server/internal/assistant/tasks.go` |
| Core to panel | `directions` | A direction-tree invalidation without a replacement tree; the panel re-reads the task direction hierarchy. | `server/internal/hub/bus_broadcast.go`, `web/src/lib/store.ts` |

The default web transport and merge logic live in `web/src/lib/store.ts`, `web/src/lib/reconnect.ts`,
`web/src/lib/sessions-delta.ts`, and `web/src/lib/complete-sessions.ts`.

### Snapshot order, replay, and reconciliation

A new connection sends `hello`, any successfully resumed missed broadcasts, the `machines` snapshot, either
`sessions_list` or full `sessions` snapshots, current pending `interaction` snapshots, and then desktop presence and
client frames. Pending interaction snapshots are queued under the same hub lock as their broadcasts, so a resolution
cannot be queued before the pending card it resolves (`server/internal/hub/http.go` and
`server/internal/hub/elicitation.go`).

Numbered broadcasts increase `event_seq` within `hello.event_stream`. A reconnect supplies that stream and its last
sequence in the WebSocket query. `resumed:true` means missed broadcasts follow in order before live events. Resume
fails for another stream, an invalid cursor, a missing prefix, or more than 400 missed events. The log retains at most
2000 events, 8 MiB, and 15 minutes. After any failure the panel refreshes from the new snapshot and cursor. A graceful
hub restart can hand the log through Postgres; a crash or stale handover starts a new stream. These rules are
implemented in `server/internal/hub/eventlog.go`.

Replay is not one universal stream. `sessions_delta`, `sessions_list`, `items`, `delta`, terminal frames, and send-state
feedback have their own snapshot or cursor rules. Elicitation interactions come from the current pending snapshot, not
from replay. The web cursor is in memory, advances only on numbered broadcasts, and resets on credential change.

For HTTP list synchronization, the same session route supports full reads, frozen-snapshot pagination, and deltas:

| Operation | HTTP route | Data and synchronization contract | Source evidence |
| --- | --- | --- | --- |
| Read or incrementally synchronize sessions | `GET /api/v1/sessions` | Full and paged responses carry `{sessions,remove,has_more,next_cursor?,revision,total}`. `since=<revision>` returns changed rows and removals. All pages of one cursor share a frozen revision and total. | `server/internal/hub/session_routes.go`, `server/internal/hub/sessions_list.go` |

A page cursor expires after five minutes or eviction with `409 cursor_expired`; an expired delta checkpoint returns
`409 revision_expired`. The web advances its checkpoint only after the last delta page, rejects mixed revisions, and
rebuilds from a baseline after either expiry (`web/src/lib/complete-sessions.ts`).

Transcript `items` uses `history` to identify the transcript generation and `from_seq` / `to_seq` to prove continuous
coverage. Catch-up is bounded at eight pages or 4000 items; beyond that, the final batch sets `more_after:true` and the
web falls back to the latest page. A slow subscriber's 512-message queue is closed rather than silently dropping an
item and later advancing past the gap (`server/internal/hub/http.go`, `server/internal/hub/sessions_push.go`, and
`server/internal/hub/heartbeat.go`).


## Existing data when a module is off

Switching a module off removes its writers, routes, tools, jobs, and feature-gated entry points. It **must not hide,
delete, or rewrite existing user data**. Core keeps the read path and the search index; deletion remains an explicit
user action, and configured retention remains a separate user setting. "Read-only boundary" therefore means that old
data stays readable without the module, not that every Core write becomes forbidden.

Recordings are the current concrete implementation of this boundary:

| Data | HTTP route | Module-off contract | Source evidence |
| --- | --- | --- | --- |
| Recording list | `GET /api/v1/kb/{kind}` | With `kind=recordings`, Core lists every stored recording as before. This is the registered generic route, not a separate literal `/kb/recordings` registration. | `server/internal/assistant/kb_routes.go` |
| Recording detail | `GET /api/v1/kb/{kind}/{id}` | With `kind=recordings`, Core returns the stored transcript, minutes, recap, and other detail bytes as before. | `server/internal/assistant/kb_routes.go` |
| Knowledge search | `GET /api/v1/kb-search` | Stored recording text remains searchable and indexed; `kind=recordings` selects that kind. | `server/internal/assistant/kb_routes.go` |
| Stored photo | `GET /api/v1/recordings/{id}/photos/{n}` | Core serves the stored bytes with their content type and private, no-store headers; adding and deleting photos belong to the module. | `server/internal/assistant/kb_recording_media.go` |
| Generic metadata edit | `PUT /api/v1/kb/{kind}/{id}` | Core's generic tags, pinned facts, and project metadata edit remains available unless the separate knowledge-base read-only mode applies. Module-off alone does not disable it. | `server/internal/assistant/kb_routes.go` |
| Explicit delete | `DELETE /api/v1/kb/recordings/{id}` | Core performs the user's explicit deletion. Turning the module off never performs this action by itself. | `server/internal/assistant/kb_recording_routes.go` |

With no recording provider, `recordingwire.NoProvider` makes the minutes, title, recap, summary, and audio-provider
calls no-ops. An interrupted upload or pending generated content stays as stored; a read derives
`paused:"module_off"` without writing that marker back. Configured retention and crash recovery still follow Core's
own rules. See `server/internal/kb/recording_port.go` and the black-box module-off checks in
`server/internal/kb/recording_module_off_test.go` and `server/internal/assistant/recording_module_off_test.go`.

A conforming panel must keep using these Core reads when the module flag is absent and must not interpret the flag as
permission to hide old data.

**Current default-web gap:** the server boundary above is real, but the default web panel does not yet meet its
UI-level visibility rule. `web/src/modules/recording/module.ts` declares recordings absent from Knowledge while off,
and `web/src/core/ModuleKBDetail.tsx` makes `useKBKindShown("recordings")` false. The list, search, and detail routes
then suppress old recordings even though Core still returns them; `web/src/modules/recording/module.test.tsx` pins that
current UI behavior. This document does not claim the gap is fixed.


## Drift guard

`scripts/test/test_core_panel_contract.py` reads the HTTP route, WS event, and Source evidence columns above. It checks
that each cited path exists and that the exact route or event string occurs in one of that row's cited source files.
Its negative tests mutate the real document with an invented route, event, and source path and require each to fail.
The general path and section checks remain `scripts/test/test_doc_paths.py` and `scripts/test/test_doc_sections.py`.
