# Assistant entry fixtures (Needs you / In progress / Finished)

Above the Assistant's composer there are two entries (#3387): **Needs you** lists everything waiting on the user, **In
progress** everything running that does not; **Finished** (#10922) lists what the conversation handed out that finished
in the last 24 hours. These cases pin which entry each thing goes to, so the web app, Android and iOS / Mac split the
same input the same way. Rows and wording are each platform's own; the split and the counts are not.

| Rule | Source | Cases |
| --- | --- | --- |
| Each thing is one row in exactly one entry; Needs you wins. A displayed open task carries its current agents, including a task made by hand: they are not also Agent rows. | `assistantEntries` (web, Android) | `running_task_once_without_its_agent`, `task_agent_question_only_in_needs_you`, `manual_task_question_has_one_task_row`, `manual_running_task_folds_current_agent` |
| Once the task is done or its link is released, a still-active Agent can be an independent row again. Historical links do not hide it. | `assistantEntries` | `done_manual_task_releases_running_agent`, `released_manual_link_does_not_hide_agent` |
| A task is in Needs you while one of its current agents asks something (a pending card), while it waits for a decision or review that is the owner's (an `ask` on an open task, never on a finished one whatever it says, as the hub's To-dos triage has it (#12974); `needs_user` on an open task; an overdue `pending_review` on Android and Apple, next row), while its turn broke off on a model error (`interrupted`), or while its failed start is still the user's to retry (the To-dos bucket says open). | `taskAwaitsDecision`, `entryStatus` | `task_waiting_for_decision_only_in_needs_you`, `interrupted_and_failed_tasks_need_you`, `owner_decisions_stay_in_needs_you_during_a_fresh_review`, `finished_tasks_with_leftover_asks_in_neither` |
| A supervisor's review is not the owner's until it is overdue: an open task in `pending_review` for less than one hour (`supervision.pending_since` against `now`) is in In progress, whatever the To-dos bucket says, and its row says its supervisor is reviewing it (`expected.supervisor_review`). At one hour and after, and with no `pending_since` or one that is no time (an older hub; `updated_at` is not read in its place), it is in Needs you. An asking agent, an open `ask` and `needs_user` are the owner's however young the review. A fresh open PR (the web's Waiting for merge, #7614) does not take a task under review out of In progress. The web lists every such row, fresh or overdue, under its own Persona reviewing entry instead (#8678, `expected.web_review`). | `supervisorReviewing` (Android, Apple); web `assistantEntries` `review` | `supervisor_review_in_progress`, `supervisor_review_with_open_pr`, `overdue_supervisor_review_needs_you`, `review_without_pending_since_needs_you`, `task_waiting_for_review_only_in_needs_you`, `owner_decisions_stay_in_needs_you_during_a_fresh_review` |
| Once answered it goes back to In progress. | | `answered_task_back_in_progress` |
| Finished work held short of live is in Needs you only when `delivery.unblock` is `user`. An owner hold on a supervised task that no review followed comes as `supervisor` (#13191): its supervisor's, in In progress with its supervisor reviewing (`expected.supervisor_review`; the web's Persona reviewing, `expected.web_review`), however old. | `supervisorReviewing` (Android, Apple); web `assistantEntries` `review` | `Only open delivery holds assigned to the owner need their attention`, `supervised_delivery_hold_with_its_supervisor` |
| A failed start whose work finished in a related to-do is closed, as the To-dos page has it (rule 1a, #3713): in neither entry; another failed start still needs the user. | `supersededFailedStarts` | `superseded_failed_start_in_neither` |
| Otherwise a task is in In progress when the To-dos page counts it open (`todoBucket` open, #3331), folded under it and not counted when shelved, and in neither when closed (done, cancelled, accepted). | `web/src/lib/todo-triage.ts` `todoBucket` | `finished_tasks_in_neither`, `stale_task_folded_not_counted` |
| A closed task the conversation handed out (`origin`; the main conversation: any conversation's) is under Finished while it finished less than 24 hours ago (`JUST_DONE_MS`), seen or not: the To-dos page's Just finished or Done section, never a failed start, finished at the moment its rule reads (`finishedAt`: its close, its accepting review, its deploy, else its last move). Newest finished first, the same moment by id. It is new while the owner has not opened it since (the To-dos page's Just finished). Exactly 24 hours is out. A to-do made by hand is not listed. | `assistantEntries` `finished` (web, Android), `AssistantEntries.split` (iOS / Mac), `finishedAt` | `finished_tasks_in_neither`, `finished_lists_the_last_day_seen_or_not`, `finished_only_the_conversations_own`, `superseded_failed_start_in_neither` |
| The main conversation has every task; another conversation only the tasks it dispatched (`origin`). | `taskCardSlots` (`everyTodo`) | `main_conversation_watches_every_task`, `manual_agents_only_in_the_main_conversation` |
| In the main conversation an Agent that belongs to no task (`dockAgents`) is a row: Needs you while it asks, In progress while it works; idle, unread and the assistant's own sessions are in neither. Other conversations have no such rows. | `dockAgents` | `manual_agent_question_only_in_needs_you`, `manual_agent_working_only_in_progress`, `manual_agents_only_in_the_main_conversation` |
| A plan waiting for Start is one Needs you row, whatever its task count; a message card waiting for Send (not sent, not filed) is one too. | | `plan_and_message_wait_for_the_user` |
| An Agent several tasks list has one home (2026-11-14, #7490 / #7531 / #7532): the task the hub ties its session to (`Session.todo`) when it lists it, else the first that lists it (oldest card first; one plan's tasks in the plan's order). Only that task's row has the Agent's cards and counts them; the others do not wait on the user for them. | `assistantEntries` (web, Android), `AssistantEntries.split` (iOS / Mac) | `shared_agent_asks_under_its_tied_task`, `shared_agent_untied_asks_under_its_first_task`, `shared_agent_untied_asks_under_its_first_plan_task` |

- Structure: `{"version": 1, "cases": [...]}`. `input` has `conversation`, `main` (the main conversation's id), `now`
  (RFC 3339, for the 48-hour shelving line), and the hub's wire objects: `todos`, `sessions` (with `state` worked out),
  `interactions` (pending cards), `plans` (by id) and `feed` (the conversation's items).
- `expected` lists row keys in order: `task:<todo id>`, `agent:<machine id>/<session id>`, `plan:<plan id>`,
  `message:<feed item id>`. `needs_you`: asking tasks and Agents first (oldest card first), then plans, then the tasks
  waiting for a decision (the To-dos page's rank, newest first), then message cards. `running`: tasks (the To-dos page's
  rank, newest first), then Agents. `shelved`: folded, not counted.
- `expected.finished` (optional, absent means none; #10922): the Finished rows, task keys in order;
  `expected.finished_new` (optional, absent means none): those of them nobody has opened since they finished, in the
  same order. Finished rows are in no other entry, and the entries' counts do not include them.
- `expected.supervisor_review` (optional, absent means none): the `running` rows that say "Awaiting supervisor
  review", in order: tasks there because their supervisor's review is under way while nothing of them runs or asks.
- `expected.web_parked` (optional, absent means none; web only): rows from `running` that the web places in its
  separate Parked tray. An old interrupted conversation cannot bring a parked task back to Needs you.
- A current replacement waiting without background work on another Agent, an external event, or a transient failure keeps an
  old interruption out of Needs you. `invalid_conversation_waiting_replacement` pins the shared transient-wait case with the
  replacement's own recoverable `turn_error`; `invalid_conversation_unfinished` keeps a permanent error with stale transient
  work interrupted. The waiting fixture's `expected.activity` is null because the replacement has no background work.
- `expected.web_review` (optional, absent means none; web only, #8678): the rows the web lists under Persona
  reviewing, in its order, instead of the `needs_you` or `running` rows above: every task waiting for its supervisor's
  review with nothing of it running or asking, however long it has waited. The web checks `needs_you`, `running` and
  `shelved` without them, and that every `supervisor_review` row is one of them; Android and Apple ignore the field.
- `expected.web_verify` (optional, absent means none; web only, #15638): the rows the web lists under its calm To
  verify entry instead of red Needs you: completed tasks awaiting optional owner verification (`user_check`) with no
  urgent owner decision or interaction card. Android and Apple ignore the field.
- Not covered here: cards of the conversation's own session (they are always Needs you rows and are added by the
  screen), message cards the user put off (`later`), Android's inbox drafts, and subtasks: Android leaves an Agent
  another one started out of the Agent rows while the Agent above it is shown (#4769, `TaskDockTest`); iOS and the web
  do not yet.
- Background activity cases (#3588) additionally carry `expected.activity` keyed by running row: `tasks`, `subagents`,
  `workflows`, and optional `phase` / `progress`; null means no background work. They cover command/Monitor counts,
  workflow summary phase and progress, subagents, mixed counts, a real external wait, user-question precedence,
  hand-opened Agents, and duplicate/non-lead task bindings. Both entry functions derive this from the same wire input.
- All data is made up.
- Run: `cd web && npx vitest run src/lib/assistant-entries.test.ts`,
  `cd android && ./gradlew :core:domain:test --tests '*AssistantEntriesFixtureTest*'` and, on Apple,
  `AssistantEntriesFixtureTests` in `PersonaTests`.
