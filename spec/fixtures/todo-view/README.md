# To-do view fixtures

The view the manager works out for each to-do (`view` on a row of `GET /api/v1/todos`, #10226): its phase, section,
reason, line 2, times, marks, asks and steps, by the decision table of `docs/design/proposals/todos-redesign.md` 6.3.
The shape is `TodoSummary` in `schema/wire/todo-view.schema.json`. The manager's projection
(`server/internal/assistant/todo/projection`) answers every case exactly, and its `next_change_at` is the moment the
view reads differently (`server/internal/assistant/todo/projection/fixtures_test.go`);
`scripts/test/test_wire_schema.py` holds every expected view to the schema, and the generated types read each one back
unchanged (Go `server/internal/assistantwire/todo_view_gen_test.go`, Kotlin
`android/core/contracts/src/test/kotlin/persona/core/contracts/TodoViewWireTest.kt`, Swift
`apple/PersonaTests/TodoViewWireTests.swift`).

| File | What it pins | Cases |
| --- | --- | --- |
| `todo_view.json` | One to-do, its runs' sessions and pending cards → its view | 65: every phase and section at least once, every reason the hub produces, each kind of ask's risk, and compatibility edge cases, unfinished closeout and pending acceptance |

## Format

`{"version": 1, "cases": [...]}`. A case is `{name, description, source, input, expected}`, as in
`spec/fixtures/triage`; the descriptions are English.

- `input.now`: the moment the case is read at.
- `input.todo`: the to-do exactly as `GET /api/v1/todos` lists it today (the hub's `listedTodo`: every field Go always
  writes, `ask` while one waits). Its agents' titles equal their sessions' titles, so no case hangs on which one a run's
  title comes from.
- `input.sessions`: the linked sessions as the hub sends them to the apps (`state` already worked out), with the fields
  the case reads, as the triage fixtures carry them.
- `input.interactions` (optional, added here): the pending cards of its runs as the hub holds them
  (`proto.Interaction`: `id`, `machine_id`, `session_id`, `kind`, `questions`, `fields`, `secret`, `private`,
  `reversible`, …; the manager marks a health conversation's card `private`). A run that asks is asked by one of its
  own: the session's card before Persona's card about its turn (`tool_name` `persona_triage`), then the oldest, then
  the lesser `id`. A card of a session none of its runs is on is not its.
- `input.restored_at` (optional): the owner's latest restore of it, which counts as activity.
- `input.someday_until` (optional): until when the owner put it off (Someday).
- `input.loc` (optional): the owner's time zone (an IANA name), for a due day; UTC when absent.
- `input.steps` (optional, added here): its steps, the to-dos whose `part_of` names it, as the list carries them. A row
  does not list its own pieces, so `steps {done, total}` cannot be worked out from the to-do alone. Their runs'
  sessions are in `input.sessions` too.
- `input.superseded` (optional, added here): the to-do is a failed start whose work finished in a related to-do, one
  closed without a start error that has the same plan, PR, issue or title
  (`docs/assistant/todo-status-sop.md` "Related to-dos"). The hub works it out over the whole list, as
  `spec/fixtures/todo-counts` shows.
- `expected.view`: the `TodoSummary`. Compare as JSON values: a field that is absent must stay absent. Optional
  booleans (`seen`, `can_wait`) are written only when true, an empty `args` is left out, and every time is RFC 3339 as
  Go writes it (`2026-11-10T09:00:00Z`, no `.000`).

All data is made up.

## The rules the cases pin

"Last activity" is today's (`docs/assistant/todo-status-sop.md` "Principle: real signals only"; `triageLastActivity`
in `server/internal/assistant/todo_triage.go`), with `restored_at` counting, and `someday_until` once it has passed. A
failed start reads as open although `done_at` is stored: `done` holds exactly when the to-do is closed some other way.

| Case | View: phase · section · reason | Proposal 6.3 | Today's hub: section · rule |
| --- | --- | --- | --- |
| `failed_start_recent` | needs_you · needs_you · `failed_start` | row 1 | waiting · failed_start |
| `failed_start_supervised` | working · working · `persona_working`, line `failed_start`, no ask | row 1, supervised | waiting · failed_start |
| `failed_start_after_two_days` | todo · todo · `rest`, with `inactive` | rows 1, 13, 14 | done · failed_start |
| `failed_start_edited_since` | todo · todo · `rest` | rows 1, 13, 14 | waiting · failed_start |
| `failed_start_due_ahead` | todo · todo · `rest`, with `inactive` | rows 1, 13, 14 | done · failed_start |
| `agent_asks_question` | needs_you · needs_you · `agent_asks` | row 2 | waiting · agent_asks |
| `agent_asks_held_by_supervisor` | working · working · `persona_working`, the card `delegating` | rows 2, 5 | waiting · agent_asks |
| `interrupted_turn` | needs_you · needs_you · `interrupted` | row 2 | waiting · agent_asks |
| `owner_decides_decision` | needs_you · needs_you · `owner_decides` | row 3 | waiting · owner_decides |
| `can_wait_decision` | needs_you · can_wait · `can_wait` | row 4 | waiting · owner_decides |
| `awaits_reply` | needs_you · can_wait · `awaits_reply` | row 4 | just_done · finished_unseen |
| `agent_running_with_steps` | working · working · `agent_running` | row 5 | working · agent_running |
| `parked_external` | waiting · waiting · `parked` | row 8 | other · rest |
| `finished_unseen` | ready · just_finished · `finished_unseen` | row 9 | just_done · finished_unseen |
| `finished_seen_recently` | ready · just_finished · `finished_recent` | row 9 | done · seen |
| `finished_seen_over_a_day` | todo · todo · `finished_seen` | row 11 | done · seen |
| `closed_unseen` | done · just_finished · `closed_unseen` | row 6 | just_done · closed |
| `canceled_seen` | done · done · `canceled` | row 7 | done · closed |
| `someday_until` | someday · someday · `deferred` | row 12 | other · rest |
| `rest_restored` | todo · todo · `rest` | rows 13, 14 | other · rest |
| `pr_open_waiting_for_merge` | waiting · waiting · `pr_open` | row 10 | pr · pr_open |
| `usage_limit_until_reset` | waiting · waiting · `usage_limit` | row 10 | other · rest |
| `starting_run` | working · working · `starting` | row 5 | working · agent_running |
| `background_work` | working · working · `background` | row 5 | working · agent_running |
| `persona_working` | working · working · `persona_working` | row 5 | working · agent_running |
| `superseded_failed_start` | done · done · `superseded` | row 7 | done · superseded |
| `reported_concern` | done · done · `reported` | row 7 | done · closed |
| `concern_never_inactive` | todo · todo · `rest` | rows 13, 14 | other · rest |
| `accepted_reopened_unseen` | ready · just_finished · `finished_unseen` | row 9 | done · accepted |
| `deployed_seen_recently` | ready · just_finished · `finished_recent` | row 9 | done · deployed |
| `review_pending` | ready · just_finished · `finished_unseen` | row 9 | just_done · finished_unseen |
| `cannot_be_done` | needs_you · needs_you · `owner_decides` | row 3 | waiting · owner_decides |
| `parked_on_someone_else` | waiting · waiting · `someone_else` | rows 8, 10 | other · rest |
| `step_decision_rolls_up` | needs_you · needs_you · `owner_decides`, from its step | row 3, 3.4 "Steps roll up" | shelved · stale |
| `someday_no_date` | someday · someday · `deferred` | row 12 | other · rest |
| `due_day_in_loc` | todo · todo · `rest` | rows 13, 14 | other · rest |
| `step_and_parent_both_ask` | needs_you · needs_you · `agent_asks`, from its step, with both asks | row 2, 3.4 "Steps roll up" | waiting · owner_decides |
| `steps_working_and_limited` | working · working · `agent_running`, from its step | row 5, 3.4 "Steps roll up" | shelved · stale |
| `finished_with_a_limited_run` | ready · just_finished · `finished_unseen` | rows 2, 9, 10 | other · rest |
| `someday_back` | todo · todo · `rest` | rows 12, 13, 14 | shelved · stale |
| `run_waits_on_agent` | waiting · waiting · `someone_else` | row 10 | working · agent_running |
| `run_waits_on_external` | waiting · waiting · `someone_else` | row 10 | working · agent_running |
| `transient_retry_stays_background` | working · working · `background` | rows 2, 5 | waiting · agent_asks |
| `working_and_waiting_runs` | working · working · `agent_running` | rows 5, 10 | working · agent_running |
| `usage_limit_before_run_waits` | waiting · waiting · `usage_limit` | rows 2, 10 | other · rest |
| `loop_stays_background` | working · working · `background` | row 5 | working · agent_running |
| `run_waits_unseen_without_wake` | ready · just_finished · `finished_unseen` | rows 9, 10 | just_done · finished_unseen |
| `decision_safe_choice` | needs_you · needs_you · `owner_decides`, its ask `safe` as the manager lists it | row 3, 3.7 | waiting · owner_decides |
| `decision_irreversible_open` | needs_you · needs_you · `owner_decides`, its ask `open` as the manager lists it | row 3, 3.7, 5 decision 4(a) | waiting · owner_decides |
| `decision_visible_to_others_open` | needs_you · needs_you · `owner_decides`, its ask `open` as the manager lists it | row 3, 3.7, 5 decision 4(a) | waiting · owner_decides |
| `retry_is_safe` | needs_you · needs_you · `failed_start`, from its step, its retry `safe` | row 1, 3.4 "Steps roll up", 3.7 | other · rest |
| `failed_start_without_origin_has_no_retry` | needs_you · needs_you · `failed_start`, no ask | row 1 | waiting · failed_start |
| `run_question_with_options_safe` | needs_you · needs_you · `agent_asks`, a `question` by its card, `safe` | row 2, 3.7 | waiting · agent_asks |
| `run_plan_open_with_card` | needs_you · needs_you · `agent_asks`, a `plan` by its card, `open` | row 2, 3.7 | waiting · agent_asks |
| `run_permission_is_tool` | needs_you · needs_you · `agent_asks`, a `tool` by its card, `open` | row 2, 3.7 | waiting · agent_asks |
| `can_wait_with_agent_asks` | needs_you · needs_you · `agent_asks`, both asks listed (run first, `can_wait` second) | rows 2, 4 | waiting · agent_asks |
| `supervisor_held_vs_real_owner_ask` | needs_you · needs_you · `agent_asks`, only the real owner ask listed | rows 2, 5 | waiting · agent_asks |
| `multi_agent_asks_same_todo` | needs_you · needs_you · `agent_asks`, two asking runs sorted oldest first | row 2 | waiting · agent_asks |
| `steps_with_superseded_child` | working · working · `agent_running`, superseded failed-start step counted done (`2/3`) | row 5, 3.4 "Steps roll up", Rule 1a | other · rest |
| `delivery_merged_not_live` | todo · todo · `rest`, `merged` does not settle into Just finished | rows 9, 13, 14 | other · rest |
| `new_activity_after_seen_at` | ready · just_finished · `finished_unseen`, `seen` cleared by newer turn | row 9 | just_done · finished_unseen |
| `parked_until_expired_stays_waiting` | waiting · waiting · `parked`, expired `until` sets no `next_change_at` | row 8 | parked · parked |
| `parked_real_wake_agent_running` | working · working · `agent_running`, real wake beats lingering park | rows 5, 8 | working · agent_running |

The last column is what `triageTodo` answers for the same input, for the web's shadow comparison; the view differs
from it only where 6.3 changes a rule. It was computed with `triageTodo` as of commit `d477f040e2` (the last change to
`todo_triage.go` these cases were checked against), with the case's `loc`, `restored_at` and `superseded`; nothing pins
it, so a later change to `triageTodo` can make it stale. The cases with steps give today's hub the to-do alone: it reads
no steps.

### Fields

- `since`, when the reason began: `done_at` for `failed_start` and for closed to-dos; the deciding run's `state_since`
  (else its `updated_at`) for `agent_asks`, `interrupted`, `awaits_reply`, `persona_working`, `agent_running`,
  `starting`, `background`, `usage_limit` and a run's `someone_else`; the moment the work finished for the three
  `finished_*` reasons (the unread run's `state_since`, the accepting review's `at`, the delivery's `at`, the
  supervision's `pending_since` for a review not done yet, else the ask's `asked_at`); the ask's `asked_at` for
  `owner_decides` and `can_wait` (the review's `at` when a `needs_user` review has no ask listed); the parking review's
  `at` for `parked` and a park's `someone_else`. Absent for `pr_open`, `deferred` and `rest`.
- `next_change_at`, the next moment time alone changes the view (any field but `rank`): the first end of a window the
  rules read, of the to-do and of its open steps, at which the view reads differently. The windows: `done_at` + 48
  hours for a failed start (then To do); `done_at` + 24 hours for closed work (then Done); `seen_at` + 24 hours for
  finished work (then `finished_seen`); the reset + 10 minutes of any run's usage limit (then the run reads as cut off,
  `interrupted`, in Needs you), whichever row holds the to-do now; the `wake_at` + 5 minutes of a retry the hub has
  scheduled (then the hub drops the wake as long overdue and the run reads as cut off); each PR's `checked_at` + 15
  minutes (then the facts are stale); `someday_until` (then back in its place); the last activity + 48 hours, or the
  end of its due day in `loc` (its `due_at`) when that is later, for a task that can still get the Inactive mark (any
  failed start can).
  Absent when none of them changes the view: a park's end, the hub's next wake of a run that waits, a decision's
  default and Someday with no date change nothing by themselves. Windows are half open: at `next_change_at` the new
  view holds.
- `rank`: 0 in every case here, since each case lists one row. `spec/fixtures/todo-counts` pins the order within each
  section across a list (`ranked_list`, `ranked_steps`, `waiting_ranked_by_wake`).
- `seen`: `seen_at` is at or after the moment that put the to-do where it is (`done_at` for a closed one, the last
  activity otherwise; the SOP's "Seen"). On a row a step leads or adds asks to, the latest activity of the to-do and
  of those steps.
- `inactive`: the last activity, set when no row above 13 placed the to-do, nothing moved for 48 hours, and it is
  shelvable as the SOP's "Shelving" says (an Agent was linked or a conversation handed it out, no due date ahead, not
  an open concern) or its start failed. Never while a computer one of its runs is on did not answer the hub (see
  "Judgement calls"; the cases give every session, so none shows it).
- `can_wait`: true in the Can wait section.
- `asks`: only on rows in Needs you or Can wait that an ask holds there, the most urgent first: a failed start's retry;
  then every run that waits on the owner, the oldest first; then the to-do's own decision, when one also waits.
  - `id`: whose ask it is, `@`, and when it was asked, so a later ask of the same kind never takes an earlier one's id:
    `todo:<kind>@<round>` for the to-do's own ask with a round (`todo:decision@2026-11-10T09:00:00Z`);
    `todo:retry@<done_at>` for a failed start, since a retry that fails again stores a new `done_at`;
    `run:<machine_id>/<session_id>@<state_since>` for a run's ask (its `updated_at` when it has no `state_since`, as for
    `since`), since a session gets a new `state_since` each time it comes to wait on the owner. Each time is written as
    the input lists it.
  - A to-do's decision: `round` is the ask's `asked_at` exactly as listed, `text` its `question`, `recommend` and
    `options` as listed, `can_wait` its `for_briefing`, `default_at` its `deadline` when its `default` is `taken`,
    `risk` the listed ask's `risk` (`open` when it lists none, or a value this projection does not know). The cases
    copy what the manager lists: `TestTodoFixturesListTheManagersRisk` (`server/internal/assistant/todo_ask_test.go`)
    checks every listed `risk` in these cases and in `spec/fixtures/todo-counts` against the manager's rule wherever
    what the ask lists tells it.
  - A failed start of a to-do with an `origin`: one `retry` ask on the to-do, with no round and no text (line 2 carries
    the error), `safe`. A failed start with no `origin` has no ask: the hub starts again only a to-do a conversation
    opened (`POST /api/v1/todos/{id}/retry` refuses the others), so its row only says why it did not start.
  - A run that waits on the owner: one ask with `run`, and `text` from the session's `asks` (else its
    `attention_reason`). Its `kind` and `interaction` come from the run's pending card, as `input.interactions` says
    (see "Risk and the lock screen"); with no card, a `question` with no `interaction`.
  - `risk` and `lockscreen` on every ask: `lockscreen` is `open_only` exactly when `risk` is `open`, else `answer`.
  - A conclusion that says the work cannot be done: a `decision` with its round and no text or options.
  - A step's ask on its parent's row: `step:<step id>/` and the step's own id.
  - A to-do with steps: its row, when in Needs you or Can wait, lists its own asks and those of every open step in
    Needs you or Can wait, since a step is never a row of its own: first the asks of the part whose line the row
    shows (the to-do's own or the leading step's), then the others, Needs you's before Can wait's, the oldest `since`
    first, the to-do's own before its steps' on a tie; each part's asks keep their own order.
- `steps`: `done` counts the closed steps (a failed start is open), `total` all of them; absent when there are none.
  An open to-do is led by its most urgent open step when that is ahead of its own section (proposal 3.4 "Steps roll
  up", 6.2 invariant 6): the first step in Needs you, else Working, else Waiting, else Can wait, the oldest step on a
  tie; a step's Can wait leads only a to-do in To do, Just finished or Someday. The to-do then takes the step's phase,
  section, reason, `since`, `can_wait` and line, has no Inactive mark, and lists asks as `asks` says.

### Risk and the lock screen

How risky answering an ask is (proposal 3.7 "Answers and taps per surface", 5 decision 4 (a)): `safe`, the row, its
card and a push may answer it in place; `open`, the owner answers it after a look, at its home, and a push about it
offers nothing on the lock screen (`lockscreen` `open_only`).

| Ask | `kind` | `risk` |
| --- | --- | --- |
| The to-do's decision, or a conclusion that says it cannot be done | `decision` | the listed ask's `risk`: the manager's rule (`askRisk` in `server/internal/assistant/todo_ask.go`) makes it `open` when `why_owner` is `irreversible` or `visible_to_others`, its recommendation requires the owner’s terminal, or Persona would take it at a deadline that cannot be undone, also once that deadline was handled and the list no longer shows it; else `safe` |
| A failed start's retry | `retry` | `safe` |
| A run's card a notification may answer: one question with options (single or multiple choice), no fields, not `private` | `question` | `safe` |
| The same card when its asker says going ahead cannot be undone (`reversible` false) | `question` | `open` |
| A run's card: several questions, a question to answer in words, a Just me turn's or a health conversation's question (`private`), or a dialog on the screen | `question` | `open` |
| A run's card: a tool approval | `tool` | `open` |
| A run's card: a plan | `plan` | `open` |
| A run's card: fields to fill in, or an MCP server's form | `form` | `open` |
| A run's card that asks for a password (on the screen, or a field to fill in) | `secret` | `open` |
| A run that asks with no card the hub holds | `question` | `open` |

### Line keys

`line {key, args}`: clients say each key in their own words from stage 2 on; no hub catalog carries text. Args are
strings, and a time is absolute RFC 3339. The keys are stable identifiers: never renamed or reused.

| Key | Reason | Args |
| --- | --- | --- |
| `failed_start` | `failed_start` | `error`: the to-do's `start_error`, the hub's text (see "Judgement calls") |
| `agent_asks` | `agent_asks` | `run`: the asking run's title; `text`: what it asks (as its ask's `text`), absent when nothing says |
| `interrupted` | `interrupted` | `run` |
| `stopped_unfinished` | `stopped_unfinished` | `run`, optional `text` |
| `decision` | `owner_decides` or `can_wait`, for a decision | `text`: the question (a `needs_user` review's when no ask is listed); `recommend`, absent when none is recommended |
| `decision_default` | `can_wait`, for a decision Persona takes at its deadline | `text`, `recommend`, `default_at` |
| `cannot` | `owner_decides`, for a conclusion that says it cannot be done | none |
| `awaits_reply` | `awaits_reply` | `run`; `text`: the session's `asks`, absent when it has none |
| `working` | `agent_running` | `run`; `text`: the run's `activity`, else the first line of its `last_text`; absent when neither |
| `persona_working` | `persona_working` | `text`, as for `working` |
| `starting` | `starting` | `run` |
| `background` | `background` | `run` |
| `parked` | `parked`, `someone_else` | `reason`: the park's reason; `until`: the park's end |
| `finished` | `finished_unseen`, `finished_recent` | `run`: the run whose turn finished; absent when a review not done yet or the ask says it finished |
| `accepted` | `finished_unseen`, `finished_recent`, for accepted work | none |
| `deployed` | `finished_unseen`, `finished_recent`, for deployed work | none |
| `pr_open` | `pr_open` | none: a row never names a PR |
| `usage_limit` | `usage_limit` | `run`; `resets_at`: when the limit resets, absent when not known |
| `run_waits` | `someone_else`, for a run whose turn ended blocked on another Agent or a named outside event | `run`: the waiting run's title; `on`: `agent` (another Agent) or `external` (an outside event) |
| `finished_seen` | `finished_seen` | `at`: when it finished (its `since`) |
| `completed` | `closed_unseen` or `completed`, a to-do closed any other way | `by`: its `done_by` (`auto`, `review` for an accepting review), absent when the owner closed it |
| `canceled` | `closed_unseen` or `canceled`, a cancelled to-do: `done_by` `review` with the ledger's `state` `cancelled` (or a `cancelled_at`) | `reason`: the first line of the cancelling review's `note`, else its `cancelled_reason`; absent when neither |
| `reported` | `closed_unseen` or `reported`, a concern closed by its report | none |
| `superseded` | `closed_unseen` or `superseded`, a failed start whose work finished in a related to-do | none |
| `someday` | `deferred` | `until`: `someday_until`, absent for Someday with no date |

A `rest` row has no line: line 2 of a plain To do is the first line of its notes, which every client already has. A
parent that carries a step's state (see `steps`) takes the step's line with one more argument, `step`: the step's
title.

## Judgement calls

Where the proposal and today's code leave a choice open, these cases make it; change a case and this list together.

- **Can wait comes before the owner's decision.** Row 3 (a decision) would otherwise always hold before row 4 (an ask
  marked can_wait), so a decision with `for_briefing` is read as row 4.
- **A run's ask is what its card asks.** The session list says that a run waits, not which card it shows; the manager
  hands over the pending cards it holds, and the run's own (the session's card before Persona's card about its turn,
  as the session list reads them, then the oldest, then the lesser id, so two reads name the same one) says the kind
  and carries its id (`interaction`), which clients answer through `POST /api/v1/interactions/{id}/resolve`. A run whose card the hub does not hold (it raised it
  on another manager, or the card ended while the session still says it waits) asks a `question`, `open`.
- **A run's ask is safe exactly when a notification may answer its card.** Choosing one of the answers offered is what
  a row or a push can do without the run's page, and the hub's push offers them for one question with options, no
  fields, nothing private and no asker who said going ahead cannot be undone, the card's twin of a decision that
  cannot be undone (`proto.Interaction.OneTapQuestion`, which the push and the view share); a plan, a tool approval, a
  form or a secret needs a look at what the run wants, and so do several questions, a question to answer in words and
  a dialog on the screen. What a tool would do is not read yet.
- **The decision's risk is the manager's, read off the list.** The list hides a deadline once it was handled and does
  not carry whether taking a decision at its deadline can be undone, so the rule cannot be worked out from the row; the
  manager writes it on the listed ask (`risk`), one rule for the list, the view and every place that answers it.
- **No ask for an interrupted turn or a reply awaited in text**: neither has a card to answer; the owner continues or
  replies in the run's session.
- **A park's end sets no `next_change_at`**: it passing changes nothing by itself (SOP rule 5a); the hub's wake at that
  time is a new fact, and so is a decision's default being taken at its deadline. The hub's next wake of a run that
  waits (`work.wake_at`) sets none either: the run working again is a new fact, and the hub dropping a wake long
  overdue changes only the rank. A retry's wake is the exception (see the retry below).
- **A failed start's 48 hours run from the failure** (`done_at`), not from its last activity as today's hub counts
  them, so an edit or a restore after the failure neither keeps it in Needs you longer nor brings it back there. After
  them it is a To do, and the Inactive mark follows its last activity like any task's, so a later edit or restore holds
  the mark back. Unlike other tasks it gets the mark with a due date ahead or with no Agent and no origin: a failed
  start waits for nothing.
- **A failed start after 48 hours has no line**: it is a `rest` row (row 14); its page still shows why it did not
  start.
- **`failed_start`'s `error` is the hub's text**, written once in the language of the moment the start failed: the
  to-do stores no error code (the plan keeps one). It is the only argument a client shows as it is. When the to-do keeps
  the code, a `code` argument (a key of its own) comes beside it, and clients say that instead.
- **A run's ask id follows the session's `state_since`**, the only moment per ask the session list carries. A session
  that asks twice without leaving Needs you keeps one id, as it keeps one ask in this list; a hub restart resets
  `state_since`, which gives an open ask a new id, so a client shows it again rather than hide a new one. The card's
  own id rides beside it (`interaction`): it names the card to answer, not the ask, since a session may end one card
  and show another while it keeps waiting.
- **A line's run text** prefers the run's `activity` (what it does now) to its `last_text`.
- **Several runs:** the first that holds of asks, a usage limit, cut off, awaiting a reply, unread, waiting on
  another Agent or a named outside event, background, starting and running decides each run's state. The rows pick a
  run by its state, in the table's order (Working takes a run that works, else one that starts, else one in the
  background; Waiting takes a run on a usage limit, else one that waits on another Agent or an outside event), and
  among runs in the same state the first in the list's order (the newest link first), except that every asking run is
  an ask, the oldest first. So a run that works keeps its to-do in Working while another of its runs waits
  (`working_and_waiting_runs`). A run the hub does not list reads as offline and places nothing.
- **A computer that does not answer** the hub's session list leaves its runs offline, and the to-do gets no Inactive
  mark (nor a `next_change_at` toward one): a run there may be working on it, as the direction tree judges no task
  stuck then.
- **`persona_working` is one of Persona's own conversations** (a session with `assistant: true`) linked as the
  to-do's run and working; the line says it as Persona's, without the conversation's title.
- **A run that waits on another Agent or an outside event is Waiting** (decided in #10226): its turn ended blocked
  on another Agent (`work.on` `agent`) or on an outside event it names (`external` with a `work.wait`), such as a
  deploy, CI or a time, and the hub wakes it to check, when its wake switch is on. 3.4 files "what wakes it" waits
  under Waiting, so the owner sees what the to-do waits for instead of a spinner: reason `someone_else`, line
  `run_waits` (`run_waits_on_agent`, `run_waits_on_external`). It is row 10's last reason, after an open PR and a
  usage limit (`usage_limit_before_run_waits`), as 6.3 lists them. A usage limit's own wait (`external` with a
  `resets_at`) is never one: the rows read the limit itself. Today's hub files them as Working.
- **`background` is work that brings the run back by itself with nothing named to wait for**: background work still
  running after its turn ended (`background_working`); a turn that ended blocked on `external` with no `work.wait` and
  no `work.agent`, which the hub writes for background commands and for a `/loop` between its ticks, and which ends by
  itself and is never woken (the hub's `managerWaits`; `loop_stays_background`); or the hub sending "Continue" after a
  moment's model failure (`work.on` `transient`). All stay in Working under their own reason. Filing a `/loop` under
  Waiting, with its next tick as the wake, would be a decision of its own, with an `on` of its own.
- **A named wait nobody has seen reads as finished** (`run_waits_unseen_without_wake`): with the wake switch off (the
  default) the hub schedules no wake for it and leaves its turn `unread`, and unread comes before a wait, as row 9
  comes before row 10. The to-do is in Just finished, line `finished`, until the owner sees the run; then it is
  Waiting.
- **A moment's model failure the hub retries is not cut off** (`transient_retry_stays_background`): the session keeps
  its `turn_error`, but while the hub's "Continue" is on its way (`work.on` `transient` with a `wake_at`) the run reads
  as `background`, as the hub writes no Interrupted receipt until its retries run out
  (`docs/assistant/agent-work.md` "Transient errors: automatic continuation"). The hub drops a wake 5 minutes after it
  was due (`wakeGrace`), and so does the view, which names that moment as `next_change_at`: with no `wake_at` the run
  reads as cut off while its `turn_error` stays, and with neither nothing brings it back, so it places nothing. Today's
  hub reads it as cut off at once.
- **"Cannot be done" is the owner's decision** (row 3): an ask on the to-do, kind `decision`, with nothing to choose
  from on the row.
- **Finished work stays in Just finished while nobody has seen it**, however long: accepted (even when the hub reopened
  it), deployed, a run's unread turn, a review not done yet, or the ask saying done or please verify (row 9). Today's
  hub sends accepted and deployed work to Done 24 hours after it finished. Accepted or deployed work never reads as
  `pr_open`, as today; an open concern is never accepted or deployed.
- **A plain "Please verify" is a finish, not `verify`**: it stays in Just finished as the SOP's "Decision table" keeps
  it. A supervisor's explicit `accepted` + `user_check` is optional owner verification: #15680 (#15638) keeps it out of
  the Needs you row and its urgent count and surfaces it as the calm "to verify" layer (the direction tree's `review`
  state plus `counts.user_check`, and a calm web tray). Its listed `ask` still supplies the existing round-bound Pass
  action. An older hub can still send the Needs you view with reason `verify`; clients honor that authoritative view
  while the rolling upgrade lasts.
- **A park on someone else is `someone_else`**, with the park's line: row 8 places it, and its `waiting_on` names the
  reason row 10 has for it.
- **Steps roll up only into Needs you, Working, Waiting and Can wait**, and only when ahead of the parent's own
  section; the oldest step in the most urgent of those leads. A step's Can wait comes after live work: it is listed,
  not counted, so it never pulls a parent that works or waits into the fold (`steps_working_and_limited`), and its ask
  is then on its own page only. A closed parent stays done (6.2 invariant 1), and its open steps are rows of their own
  then. A step whose finished work nobody has seen does not make its parent look finished.
- **A to-do and its steps that all ask show every ask on one row** (`step_and_parent_both_ask`): the row's line and
  first ask are the leading part's, and the count is one (3.4 "Two asks show '2 asks' and the more urgent one", 3.7).
- **Someday with no date** is `someday_until` at `9999-12-31T00:00:00Z` or later, as the hub stores it: no `until` and
  no `next_change_at`. Someday is row 12, so a failed start put off within its 48 hours still needs the owner, and
  finished work still shows as finished.
- **Coming back from Someday counts as activity** (`someday_back`): a task put off for weeks returns without the
  Inactive mark, which it gets 48 hours after its return if nothing moves.
- **A cancellation is read off the ledger**, as the web reads it: the list sends a cancelled to-do's `done_by` as
  `review`, and its supervision's `state` says `cancelled` (`canceled_seen`). An owner's close of a task once cancelled
  and reopened is the owner's (`done_by` empty).
- **A start that fails on a to-do already closed** keeps the close as its `done_at` (FailTodo leaves a set one), so it
  reads as a failed start, open, its 48 hours counted from the close; today's hub reads it the same way.
- **A due day ends at midnight in `loc`**, and a task due ahead gets no Inactive mark (`due_day_in_loc`, where the day
  has ended in UTC but not in the owner's zone).

## Not produced yet

The projection never answers these reasons, since today's list row carries no input for them; each comes with the
fact it needs, adding a case here:

- `steps_proposed`, `signin` and `duplicate`: no such ask exists on the hub yet.
- `queued`: a start the admission gate holds is kept in the plan (`PlanTask.Queue`), not on the listed to-do.
- `computer_offline`: Start When It's Back has no state yet. An open to-do whose runs' computer is away (`orphaned`
  with reason `offline`) waits for nothing that wakes it, so it stays where the other rows put it.
- `scheduled_report`: the to-do keeps no report schedule.

## Run

```sh
(cd server && go test ./internal/assistant/todo/projection)                   # the projection answers every case
python3 scripts/test/test_wire_schema.py                                      # every view holds to TodoSummary
(cd server && go test ./internal/assistantwire -run TodoView)                 # Go reads and writes each one back
```

### Closeout follow-up (#9823, aligned with #10174)

The projection reads the session's structured closeout and `work.turn_at`; an older snapshot without either keeps its
compatibility reading, including the upstream unread/wait judgement call below. A known unfinished stop, or a seen
missing-closeout stop with a recorded turn time, is To do during its
follow-up window, then Needs you (`stopped_unfinished`) only after a full hour. Explicit owner asks, actual foreground,
child/background work, a named Agent/external wait (#10411), an intentional park and settled work keep their own homes. The latest evidenced stopped run
wins over an older closeout. The clock uses the turn and `supervision.followed_up_at` or an explicit review (empty
`by`), never a read receipt, an automatic review or the ledger's `updated_at`. `next_change_at` names the strict
one-hour boundary plus one nanosecond.

A reported `done` is ready, never closed, even after its reply was read. Its finish time stays the recorded turn time;
Just finished's seen window uses that result rather than a later read-receipt timestamp. The shared cases
`stopped_unfinished_closeout_needs_followup` and `reported_done_seen_awaits_acceptance` pin those two classifications;
`projection/closeout_test.go` also checks the exact boundary, follow-up provenance, resumed work, parks, acceptance,
the seen window and counts (Needs you 1, Waiting 1, uncounted Just finished 1; Open 3).
