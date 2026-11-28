# To-do waiting reason fixtures (`spec/fixtures/todo-wait/`)

Shared contract cases for `schema/wire/todo-wait.schema.json` (#16168): the server computes a strongly-typed `TodoWait`
(`wait` on `GET /api/v1/todos` and `GET /api/v1/todos/{id}`) whenever a to-do is projected into `waiting`, and omits
`wait` in every other phase. Clients decode and display `wait` directly without deriving waiting state themselves.

## Kinds (`TodoWaitKind`)

| Kind | Produced when |
| --- | --- |
| `person_reply` | Parked waiting on a person's reply (`evidence.kind == "person_reply"`, or legacy `waiting_on == "someone_else"`). |
| `merge_review` | Parked waiting on a named reviewer to review or merge a ready PR/MR (`evidence.kind == "merge_review"`). |
| `pr_merge` | Open non-draft PR waiting to merge (`pr_open`, or a `todos_v2` external PR blocker). |
| `external_event` | Parked on an external event (`evidence.kind == "external_event"` / `waiting_on == "external_event"`), an Agent run blocked on `external` with a named wait, or a `todos_v2` external blocker. |
| `date` | Parked until a specific date/time (`evidence.kind == "date"` / `waiting_on == "date"`). Sets both `wake_at` and `until`. |
| `todo_handoff` | Parked waiting on another to-do's handoff check (`waiting_on == "todo_handoff"` or `handoff != nil`). Sets `handoff_todo_id`. |
| `agent` | An Agent run blocked waiting on another Agent (`run_waits` with `work.on == "agent"`). |
| `usage_limit` | An Agent run stopped on a subscription usage limit that has not reset yet (`usage_limit`). |
| `parked` | Legacy or generic park without a narrower kind (for example legacy `owner_priority`). |

## Rules

- Every timestamp (`wake_at`, `until`, `since`, `contact.sent_at`) is an absolute RFC 3339 UTC time so responses stay
  ETag-stable while time passes.
- When a parent to-do's leading child step rolls up into `waiting`, the parent inherits the step's `TodoWait` with
  `step` set to the step's title.
- Non-waiting to-dos (working, needs_you, ready, todo, someday, done) emit no `wait` (`null` in fixtures).
