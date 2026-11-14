# To-do status SOP fixtures

The To-dos page's sections (docs/assistant/todo-status-sop.md) are worked out by every client's own copy of the rules and,
since #3483, by the hub (`server/internal/assistant/todo_triage.go`), which needs them to tell when a leader's task is
stuck. These cases hold the copies to one definition: the web app (`web/src/lib/todo-triage-fixture.test.ts`) and the
hub (`TestTriageFollowsTheSharedCases`, `TestTriageTodosFollowsTheSharedCases`,
`TestTriageRelatedKeysFollowTheSharedCases` in `server/internal/assistant/todo_triage_test.go`) read them.

| File | Rule | Cases |
| --- | --- | --- |
| `triage.json` | The decision table, first rule that holds wins; the last activity; open concerns stay Working / Other open except for the rules that still apply, and close only by their report | every case of `triage` in `web/src/lib/todo-triage.test.ts`, one assertion a case; `derived` ones cover the agent states that table reads (failed start, interrupted, usage limit, waiting in the background, released, a session without `state`, a session not listed); `concern_*` covers each concern exception, a same-age ordinary-task shelving control, and a fresh / old reported close |
| `groups.json` | Related to-dos share a row led by the most urgent; closed and open never share one; an open concern always has its own row while a closed concern groups normally; the header's counts; quiet tasks for the badge; a failed start whose row's work finished (rule 1a, #3713) | `triageTodos` in the same test file |
| `related.json` | The keys that relate two to-dos | `relatedKeys` |

- Structure: `{"version": 1, "cases": [...]}`. `input.now` is the moment the case is read at; `input.sessions` are
  sessions as the hub sends them to the apps (`state` already worked out); `input.todo` / `input.todos` are to-dos as
  `GET /api/v1/todos` lists them (with `ask` while one waits); `input.restored_at` a restore from Shelved;
  `input.include_quiet` false leaves quiet tasks out (the sidebar's badge).
- `expected` for `triage.json`: `section` and `rule`, and `last` where the case is about it. For `groups.json`: the rows
  of each section as their members' ids, lead first (a section not named has no rows), and `open`, `shelved`,
  `finished`. For `related.json`: `keys` in order.
- A due day is read in UTC by the hub's test; the web reads it in the machine's zone, so a case must not hang on the
  zone.
- All data is made up.
- Run: `cd server && go test ./internal/assistant -run Triage` and `cd web && npx vitest run src/lib/todo-triage-fixture.test.ts`.
