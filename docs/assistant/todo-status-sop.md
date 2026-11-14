# To-do status SOP

How the To-dos page decides where each to-do shows (#3156). The page's default list is by **status section**; the older
grouping by folder stays available as a secondary view (List › Status | Folder). The code follows this document:
`web/src/lib/todo-triage.ts` (`triage`, `triageTodos`), with tests in `web/src/lib/todo-triage.test.ts`, and its
native copies (see "Clients"). A change to the rules changes all of them in the same PR.

## Principle: real signals only

A to-do's section comes from what the hub and the computers report, never from what an agent says about its own
work. An agent writing "done" in its reply does not finish anything; its turn ending, a review, a merge or a deploy does.

| Signal | Where it comes from | Values used |
| :--- | :--- | :--- |
| Agent alive and running | The linked, not released agents' sessions (herdr and other engines, via the hub's session list) | asking (`needs_you`), cut off by a model error (`turn_error`), working or starting, finished unseen (`unread`), idle, gone |
| PR / delivery | `delivery.state` and `pr_url` on the to-do (`docs/assistant/todos.md`, "Delivery") | none, PR open (`pr`, or a `pr_url` with no delivery past it), merged not deployed (`merged`, `off`), deployed (`live`) |
| Review verdict | The latest `task_review` in the to-do's supervision (`docs/assistant/unified-entry.md`) | `accepted`, `rework`, `needs_user`, `cancelled`, pending review |
| Ask | The to-do's `ask` (`docs/assistant/todos.md`, "Waiting for the owner") | a decision, "cannot be done", "done", "please verify" |
| Last activity | The latest of: the to-do's created, updated, reopened and done times; its supervision's update, last report and last review; its delivery and ask times; each linked agent's session update; a restore from Shelved | a moment |

## Sections

In the page's order (#5707): what the owner has to act on first, then the unfinished work, then what just finished,
then the folded ones, Done last. The same order decides which member leads a merged row ("Related to-dos").

| Section | Shown | What it holds |
| :--- | :--- | :--- |
| Waiting on you | open | Something only the owner can move: an answer, an approval, a decision, a retry |
| Working | open | An agent is running on it now |
| PR to merge | open | Its work stopped at a pull request that is still open and nothing runs: however old, it stays here, with the PR link and how long since it moved |
| Parked (web) | open | An explicitly parked task whose wait has not expired; it is unfinished, even if its last agent turn was seen |
| Other open | open | Open, recently active, none of the above (an agent stopped and seen, a reminder of the user's own) |
| Just finished | open | Worth a look: closed, accepted or deployed in the last 24 hours, or a turn nobody has seen / a review not done yet (those stay until seen or reviewed); the owner has not seen it since (see "Seen") |
| Inactive (web; Shelved on native clients) | folded | No activity for 48 hours and nothing running (see "Shelving"); not an explicit parked verdict |
| Done | folded | Closed, accepted or deployed more than 24 hours ago or seen since; a finished turn the owner has seen since it last moved |

## Decision table

The first rule that holds wins.

| # | Rule | Condition | Section |
| :--- | :--- | :--- | :--- |
| 1 | `failed_start` | The task's agent never started (`start_error`) | Waiting on you; Done once 48 hours passed |
| 1a | `superseded` | Rule 1, and its row (see "Related to-dos") has a to-do that really finished: closed without a start error (#3713) | Just finished for 24 hours or until seen, then Done; behind that to-do in the row, never leading it |
| 2 | `agent_asks` | An agent waits on the owner (question, approval, plan) or was cut off by a model error | Waiting on you |
| 3 | `owner_decides` | Open, and the review says `needs_user`, or its ask is a decision or "cannot be done" | Waiting on you |
| 4 | `agent_running` | An agent is working or starting | Working |
| 5 | `closed` | `done_at` is set | Just finished for 24 hours or until seen, then Done |
| 5a | `parked` (web) | Open and the current review is parked | Parked until its `until`; after that, Other open while awaiting review, never Done just because its agent stopped |
| 6 | `accepted` | The latest review accepted it and no review came since, even when the hub reopened it (see "Accepted tasks") | Just finished for 24 hours after the review or until seen, then Done |
| 7 | `deployed` | `delivery.state` is `live` | Just finished for 24 hours or until seen, then Done |
| 8 | `finished_unseen` | An agent finished a turn nobody has seen, the review is pending, or its ask says done / please verify; not seen since it last moved | Just finished |
| 9 | `pr_open` | Its PR is open (rule "PR / delivery" above); a `pr_url` whose stage is Merge or later is a merged PR, never this (#3205) | PR to merge |
| 9a | `seen` | Rule 8's signals, seen by the owner since it last moved (see "Seen"); not an open concern | Done |
| 10 | `stale` | Shelvable (see "Shelving") and no activity for 48 hours | Shelved |
| 11 | `rest` | Anything else | Other open |

On the web, the header's Parked count is the number of to-dos in the Parked section (included in open).
Inactive counts only automatic 48-hour shelving. The Parked section and review badge use the same localized name.
Apple (`apple/Shared/TodoTriage.swift`) and Android (`TodoTriage.kt` in `android/core/domain`) still need this
parked rule and naming alignment; #5929 records the parity gap. This web fix does not claim native-client delivery.

**A failed start whose work got done another way is not the owner's**: the hub closes a to-do whose agent never started
(`done_at` with its `start_error`), and rule 1 keeps it in Waiting on you so the owner can retry or remove it. When the
same piece of work finished in a related to-do (the same plan, PR, issue or title: its row), there is nothing left to
retry: rule 1a files it with that work and it counts as closed, on the page, the sidebar's badge and the assistant's
entries alike (`supersededFailedStarts` in each copy). A failed start that still waits on the owner reads as one on every
client: not ticked or struck through, why it did not start in red, and its checkbox closes it for good (`"done": true`
on it clears `start_error`: the owner closed it; `docs/assistant/todos.md`, API overview).

**Waiting on you is only for what the owner must do in person**: answer a question, grant a permission, decide on a
large feature. A deployed to-do's "Please verify" ask and a "done" conclusion are not: the supervising assistant
checks and accepts them by default, so they sit in Just finished (rule 8), their one-tap button still there for the
owner who wants to accept it themselves.

A merged but not deployed delivery (`merged`, `off`) has no section of its own: the row says where it is held and
who can unhold it, and the to-do falls under rules 10 and 11.

### Deployed needs evidence

"Deployed" means the running hub contains the work: the commit of the hub's `/api/v1/health` (`commit`, else the
`-g<hash>` of `version`) includes the PR's merge commit, which GitHub's compare answers (#3205,
`server/internal/assistant/deploy_check.go`). The hub checks it before it moves a task to the Deploy stage, sets
`delivery.state = live` or asks "Please verify", whether the claim came from the agent's reply ("已部署",
"交付：已上线并实测") or from the completion judge:

- In the running hub: deployed (`live`, stage Deploy), then verified.
- Merged, not in the running hub yet: held at "merged, not deployed" (`delivery.state = merged`, stage Merge, the gate
  "等部署：线上 hub（<commit>）还没包含合并提交 <commit>", who unholds it: other). No "Please verify". A watch looks
  again every minute the hub's commit has changed (GitHub is asked only then); once the deploy lands the task moves
  to `live` and only then is verified.
- The check cannot tell (GitHub or the hub's health unreachable, rate limit): held the same way ("等确认部署"), asked
  again every 10 minutes. A failed check never says deployed.
- Nothing to check against: work that is not a GitHub PR, a PR in a repository the hub is not built from (the hub's
  commit is not in it), or a hub that does not say its commit (a local or Docker build). The agent's word stands,
  logged as unverified.

An open to-do that names a PR and says nothing past it (no delivery, `none` or `pr`; stage before Deploy), whatever
told it, is looked at the same way by that sweep: its PR merged and in the running hub makes it deployed (`live`:
Just finished), merged and not in it "merged, not deployed" with the watch above; an open PR stays a PR to merge. Such a
to-do is asked about again only when the hub's commit changes, or every 10 minutes while its PR is open. It gets no
"Please verify": no turn said it was deployed.

**Without GitHub**: a hub that cannot reach GitHub decides from a file the deploy writes, `deployed-commits.txt`, in
the hub's state directory (or its compatible predecessor) or next to the hub's binary:

```
# persona deployed commits v1
# repo: <owner>/<name>
# pr: <N> <full sha>
<full sha> <subject>
```

the deployed commit first, then its ancestors newest first, as `git log --max-count=3000 --format='%H %s' <commit>`
prints them. It is believed only when its first commit is the one the running hub reports, and only for PRs of its
repository. A PR is found by its merge's subject (a squash merge's `… (#N)`, a merge commit's `Merge pull request #N …`):
found, it is deployed, with no network. Not found, GitHub is asked; when GitHub cannot answer (no route, a failure
pauses it for 10 minutes), it is "not in the running hub" (held, "等部署"), and the open to-dos' sweep leaves such a
to-do as it is (its PR may still be open). The file is checked first, GitHub only when the file cannot say.

**Deployed before the upstream merge** (#4867): a hub built from a local branch can run a PR that is still open. The PR has
no merge commit, and GitHub's compare does not know a local commit. So the file names such a PR by a commit of its own,
`# pr: <N> <sha>`: its head when the deployed commit contains it, else its newest own commit when the PR branch only merged
its base since. The hub counts the PR as deployed (stage Deploy, then verified as above) only when the file also lists that
commit, so the commit is in the deployed history.

`scripts/deploy/deployed_commits.py --out <dir>` writes the file for a commit (`--commit`, default `HEAD`). It lists the
history, and the open PRs it runs, read with the GitHub CLI when that is signed in; `--pr <N>[=<sha>]` names more,
`--no-open-prs` skips GitHub. It writes under a temporary name and renames. It leaves out commits written before
2026-11-08 (`--since`): a `(#N)` in them is the previous tracker's number (`CLAUDE.md`, "Old issue numbers"), which the
hub would take for this repository's PR #N. One such subject once marked an open PR deployed. `scripts/deploy/deploy.sh` writes it beside
the hub's binary. A hub built and installed by hand, a local `dev` build, gets the same file from
`python3 scripts/deploy/deployed_commits.py --commit <the built commit> --out "$PERSONA_HOME/bin"`, run in the checkout it
was built from.

GitHub is read through the hub's GitHub connector when it is signed in, else with `GH_TOKEN` / `GITHUB_TOKEN` from the assistant's environment when set, else anonymously (enough
for a public repository; a private one without a token stays held as "cannot tell").

### Deployed work is verified by the supervising assistant

Once deployed (checked as above), a task's "Please verify" is accepted by the supervising assistant by default
(`by_assistant` on the ask, stage Verified, no Lark card): the owner is not asked for each one. Only a big feature, a
to-do with priority `high`, still asks the owner with the card, as before.

## Seen

Just finished used to empty itself only by time: a to-do the owner had already looked at stayed there for 24 hours, on
top of the list (#5707). Now the owner files it:

- **Got it** on a Just finished row (on the web a small button at the end of its status line, under the time, #6578; on
  iOS a button under its words, moving to the web's place in #7148, and a leading swipe action) marks it seen. The row
  goes to Done at once, before the hub answers; a toast says so with **Undo**, which takes the mark off. A failed write
  puts the row back and says why.
- **Opening it** counts too: the iPhone marks it seen when its page opens (the list behind has it in Done on the way
  back); the web when the page leaves it (another to-do, the list, another page), so its row does not move while it is
  read. Nothing is said about it. The Mac (frozen) and Android do neither yet; both file what another device marked.
- The mark is the to-do's `seen_at` on the hub (`PUT` / `DELETE /api/v1/todos/{id}/seen`, feature `todo_seen`), so every
  device agrees. It is not activity: `updated_at` stays, so it neither shelves nor unshelves anything. A client of a hub
  without `todo_seen` offers neither.
- A to-do is **seen** when `seen_at` is at or after the moment that put it in Just finished: its `done_at` (rules 1a and
  5), its accepting review (6), its delivery (7), or its last activity (8). Anything that happens after the mark (a
  reopen, a new close, a new turn, a review) brings it back to Just finished until it is seen again or its 24 hours pass.
- A finished turn seen (rule 9a) is filed under Done even though the to-do is not closed: the owner has looked, and it
  no longer counts as open. A PR still waiting on the owner keeps it in PR to merge, an open concern in Other open.
- Checks: the shared cases (`*_seen*`, `unfinished_before_just_finished_and_seen_in_done`), `server/internal/assistant/todo_seen_test.go`,
  `web/src/views/Todos.seen.test.tsx`, `apple/PersonaTests/TodoTriageTests.swift` and `TodosLogicTests.swift`. In a
  browser and the simulator against the mock hub (`MOCK_TODO_LIST=1`; `MOCK_TODO_SEEN=off` is a hub without it):
  `node web/e2e/todo-seen.mjs <web URL> [--phase before|after] [--shots <dir>]` and the UI test `TodoSeenTests`
  (`TEST_RUNNER_PERSONA_TODOS_HUB`, `TEST_RUNNER_PERSONA_SHOTS`).

## Header count

The web's header reads "N open · N parked · N inactive · N done" beside the title on wider screens (a phone leaves it out); it counts what the list's filter shows, whatever is searched (#3740). The iOS and
Android lists have no such line (#3712): it said again what the sections say, so each section's title carries the number of
to-dos its rows list (a merged row's related ones too), in the section's state color, and the counts follow a search and the
list's filter (#3740). **Open** counts to-dos that are not closed, not accepted or deployed,
and not shelved: Waiting on you, Working, PR to merge, Other open, and Just finished rows still open (a finished turn
is not a closed to-do; only a review or the user closes it; once the owner has seen it, rule 9a, it is in Done and not
counted). **Parked**, **inactive** and **done** count the to-dos in the Parked, Shelved and Done sections as their titles
do (a merged row's related to-dos under the row's section), so the header and each section's title say the same number;
what closed in the last 24 hours and was not seen yet is under Just finished and counted there, not as done. The
sidebar's To-dos badge shows the same open count without quiet tasks (#1731), as before; the page lists and counts them. A task whose agent never started (rule 1) counts as open while it waits on the owner.

The counts are the same whatever the list is grouped by (web audit item 12), and so is where each to-do is:

- **By folder**: the folders hold every to-do the status sections place outside Done (a merged row's to-dos where the
  row is), each in its own reading: a failed start waiting on the owner open, with why it did not start, and its folder
  says Needs you; a to-do closed in the last 24 hours struck through, with **Got it**, until it is seen. The folded
  **Done** at the bottom holds the Done section's.
- **By pinned direction**: the directions hold the open to-dos the hub's tree lists (it lists only what counts as open);
  a line says how many open ones no direction lists; the rest outside Done (closed in the last 24 hours, accepted,
  deployed, shelved) keep their status sections below the directions, as Android's list does, so a to-do ticked there
  moves to Just finished instead of leaving the list; the folded **Done** holds the Done section's.

Before the first read of the to-dos lands the header shows no counts, not zeros (audit item 83).

## Shelving

A to-do is shelvable when it is a task (an agent was ever linked, or a conversation handed it out) and has no due date
ahead. The user's own to-dos without agents (reminders, errands) and ones due later never shelve themselves.

- Shelved to-dos are folded under **Shelved**, not counted as open, and not changed on the hub: nothing is deleted, closed
  or released. Any new activity (an agent working, a report, a review, an edit) takes it out again at once.
- **Restore** on a shelved row counts as activity now: the to-do goes back to its section and shelves again only after
  another 48 hours without a move. The restore is kept in this browser (`localStorage`, `persona.todos.restored`), not
  on the hub; another browser or device still shows it shelved until it moves.

## Related to-dos

To-dos that are the same piece of work share one row, led by its most urgent member (by section, then most recent
activity); "N related" under the row opens the others. Two to-dos are related when they share any key:

- the same PR link (case and a trailing slash ignored);
- the same issue number in the title (`#412`);
- the same title once case, spaces and punctuation are dropped (six characters or more);
- the same plan (`plan_id`: tasks one request was split into).

Relations chain (A shares a PR with B, B an issue with C: one row). A closed to-do never joins an open one's row.

## Accepted tasks

When `task_review` accepts a task, the hub closes it. If its agent is then sent more work, the hub reopens it
(`taskWorking` → `closedTurnReopens` → `reopenTodo`, `docs/assistant/todos.md`) while the supervision still says
`accepted`. For a task with no plan (an adopted agent, often kept on for other work), only the task's conversation
sending more (`agent_send`, or the owner's message on its card) reopens it; that agent's other turns leave it closed
(#6726). Earlier the
page treated every reopened task as not reviewed: it showed "Follow up", its badge "Supervising", and criteria added
with the extra work (`agent_supervise` / `agent_send` replace `done_when`, so the list grew from 5 to 8) as open
with a clock and counted them ("5/8").

The acceptance now stands until a newer review replaces it (`acceptanceStands` in `web/src/lib/supervision.ts`):

- with nothing of it running or asking, the task reads **Accepted** in the row and the detail, and sits in Just finished
  or Done (rule 6);
- with its agent working or asking again, it is in Working or Waiting on you like any task, its badge still Accepted;
- criteria no review judged after the acceptance are marked "not checked" (a dash, not a clock), and the count only
  covers judged ones ("5/5"). The next review, if the supervisor runs one, judges them as usual.

A task the user reopens by hand also keeps the accepted verdict until the next review; the checkbox closes it again.

## Clients

Every client follows this document with its own copy of the rules, each with the same test cases (#3206):

| Client | Rules | Tests | Page |
| :--- | :--- | :--- | :--- |
| Web | `web/src/lib/todo-triage.ts` | `web/src/lib/todo-triage.test.ts` | `web/src/views/Todos.tsx` |
| iOS / Mac | `apple/Shared/TodoTriage.swift` (`TodoTriageLogic`) | `apple/PersonaTests/TodoTriageTests.swift` | `apple/Persona/Views/Todos/TodosView.swift` |
| Android | `android/core/domain/src/main/kotlin/persona/core/domain/tasks/TodoTriage.kt` | `android/core/domain/src/test/kotlin/persona/core/domain/tasks/TodoTriageTest.kt` | `android/feature/agents/src/main/kotlin/persona/feature/agents/TodosScreen.kt` |
| Hub | `server/internal/assistant/todo_triage.go` | `server/internal/assistant/todo_triage_test.go` | none: it tells when a leader's task is stuck (#3483) |

The shared cases in `spec/fixtures/triage` are read by the web app (`web/src/lib/todo-triage-fixture.test.ts`) and the
hub; a rule change adds or changes a case there first.

A change to the rules changes all of them in the same PR. Native differences: iOS / Mac keep restores in the device's
`UserDefaults` (`todos.restored`) and Android in its local preferences, as the web keeps them per browser; the iOS list
has no Status | Folder switch (it had no folder view), and the Mac shows a merged row's members as a disclosure under the
lead. The iOS list draws a row's "N related" inside the lead's row, under its words (Android's card already had it). iOS and
Android filter and sort the list on the device (`docs/assistant/todos.md`, "List filter and sort"; the web follows). Moving the classification to the hub, so the clients only draw the section it sends, would remove the copies; it
is not done yet.
