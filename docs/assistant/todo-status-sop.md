# To-do status SOP

How the To-dos page decides where each to-do shows (#3156). The page's default list is by **status section**; the older
grouping by folder stays available as a secondary view (List › Status | Folder). The code follows this document:
`web/src/lib/todo-triage.ts` (`triage`, `triageTodos`), with tests in `web/src/lib/todo-triage.test.ts`, and native copies
(see "Clients"). The hub and web use fresh `pr_status` facts for the Waiting for merge section; native clients retain
their existing presentation and ignore the added field. Only web adds the separate Waiting for merge group to its
conversation tray; this does not change the native lifecycle or presentation.

## Principle: real signals only

A to-do's section comes from what the hub and the computers report, never from what an agent says about its own
work. An agent writing "done" in its reply does not finish anything; its turn ending, a review, a merge or a deploy does.

| Signal | Where it comes from | Values used |
| :--- | :--- | :--- |
| Agent alive and running | The linked, not released agents' sessions (herdr and other engines, via the hub's session list) | asking (`needs_you`), cut off by a model error (`turn_error`), working or starting, finished unseen (`unread`), idle, gone |
| PR / delivery | `pr_status` facts and `pr_url` on the to-do, plus delivery lifecycle (`docs/assistant/todos.md`, "Delivery") | fresh matching PR facts (checked within 15 minutes), PR open and non-draft; merged and closed are terminal PR observations |
| Review verdict | The latest `task_review` in the to-do's supervision (`docs/assistant/unified-entry.md`) | `accepted`, `rework`, `needs_user`, `cancelled`, pending review |
| Ask | The to-do's `ask` (`docs/assistant/todos.md`, "Waiting for the owner") | a decision, "cannot be done", "done", "please verify" |
| Last activity | The latest of: the to-do's created, updated, reopened and done times; its supervision's update, last report and last review; its delivery and ask times; each linked agent's session update; a restore from Shelved | a moment |

## Sections

In the page's order (#5707): what the owner has to act on first, then the unfinished work, then what was delivered,
then the folded ones, Done and Cancelled last. The same order decides which member leads a merged row ("Related to-dos").

| Section | Shown | What it holds |
| :--- | :--- | :--- |
| Waiting for you | open | Something only the owner can move: an answer, an approval, a decision, a retry |
| Working | open | An agent is running on it now |
| Waiting for merge | open | Fresh facts match its PR URL; the PR is open and non-draft, no agent is active and no owner decision is pending. Show the PR link and review/check/conflict blockers, or that it awaits a maintainer |
| Parked (web) | open | An explicitly parked task whose wait has not expired; it is unfinished, even if its last agent turn was seen |
| Other open | open | Open, recently active, none of the above (an agent stopped and seen, a reminder of the user's own) |
| Delivered · awaiting review | open | Worth a look: an unaccepted delivery, unseen turn, pending review or done / please-verify ask, or a closed concern whose completion is less than 24 hours old and unseen. An unaccepted `live` delivery stays here and open indefinitely, even after being seen; it never ages into Done or Shelved |
| Inactive (web; Shelved on native clients) | folded | No activity for 48 hours and nothing running (see "Shelving"); not an explicit parked verdict |
| Done | folded by default | Non-cancelled closed (apart from that fresh unseen closed concern), accepted or superseded work immediately; an ordinary unsupervised finished turn seen since it last moved. Unaccepted `live` work never enters this section merely through age or Seen |
| Cancelled | folded by default | Cancelled work, with its cancellation reason; never counted as Done |

In the status list, a Done group led by a fresh (less than 24 hours old) accepted or owner-ticked outcome, and a fresh
Cancelled group, start open so the outcome and its title treatment remain visible (#11677). A plain legacy review-done
group and older history stay folded (#3713). The separate Done and Cancelled folds in the other groupings do not change.

## Decision table

The first rule that holds wins.

| # | Rule | Condition | Section |
| :--- | :--- | :--- | :--- |
| 0 | `cancelled` | The API cancellation projection or a legacy cancelled supervision state | Cancelled, with its reason; this wins over closed, accepted and agent states |
| 1 | `failed_start` | The task's agent never started (`start_error`) | Waiting for you; Done once 48 hours passed |
| 1a | `superseded` | Rule 1, and its row (see "Related to-dos") has a to-do that really finished: closed without a start error (#3713) | Done immediately, behind that to-do in the row, never leading it |
| 2 | `agent_asks` | An agent waits on the owner (question, approval, plan) or was cut off by a model error | Waiting for you |
| 3 | `owner_decides` | Open, and the review says `user_check` (personal verification) or `needs_user`, or its ask is a decision or "cannot be done" | Waiting for you |
| 4 | `agent_running` | An agent is working or starting | Working |
| 5 | `closed` | `done_at` is set | Done immediately, regardless of age or Seen; explicit completion is never left open. A closed concern is the sole exception: it stays in Delivered · awaiting review while its completion is less than 24 hours old and unseen, then enters Done |
| 5a | `parked` (web) | Open and the current review is parked | Parked until the hub changes its state, including past its fallback `until`; never Done just because its agent stopped |
| 6 | `accepted` | The latest review accepted it and no review came since, even when the hub reopened it, unless a takeover started a new round (#11553; see "Accepted tasks") | Done immediately, regardless of review age or Seen; acceptance is never left open |
| 7 | `finished_unseen` | `delivery.state` is `live`, but the task has not been accepted | Delivered · awaiting review, open until main-chat acceptance or explicit user completion; no 24-hour or 48-hour timeout, including after Seen |
| 8 | `finished_unseen` | An agent finished a turn nobody has seen, the review is pending, or its ask says done / please verify; either not seen or acceptance is still required | Delivered · awaiting review |
| 9 | `pr_open` | Fresh facts of one of its PRs (`pr_status` matching `pr_url`, or an earlier PR in `other_prs`, #7935) say open and non-draft; no active agent or owner decision, and not accepted or delivered (rules 6 and 7 keep their finished semantics whatever its PR facts say; an open concern, which those rules skip, still waits for merge). Stale, absent or mismatched facts do not imply waiting for merge; merged/closed facts suppress legacy `delivery.state=pr` | Parked (merge wait) on the web; Waiting for merge on other clients |
| 9a | `seen` | Rule 8's signals, seen by the owner since it last moved (see "Seen"), with no supervisor and no pending acceptance; an open PR takes precedence | Done |
| 10 | `stale` | Shelvable (see "Shelving") and no activity for 48 hours | Shelved |
| 11 | `rest` | Anything else | Other open |

**Open concerns** (`kind = "concern"`, `done_at` unset, #3582) follow that table with these exceptions:

- Rules 1 to 4 apply unchanged: a failed start in its first 48 hours and anything waiting on the owner is Waiting on you;
  an agent working or starting is Working.
- A failed start older than 48 hours is Other open by rule `rest`, never Done.
- Rules 6 (`accepted`) and 7 (`finished_unseen` for delivery) never apply.
- Rules 8 (`finished_unseen`) and 9 (`pr_open`) apply unchanged.
- Rule 9a (`seen`) never applies: a finished turn the owner has seen leaves the concern in Other open, not Done.
- Rule 10 (`stale`) never applies: an open concern is never Shelved, however long nothing happens.
- Otherwise it is Other open by rule `rest`.

A closed concern (`done_at` set) follows rule 5 with its one visibility exception: while the completion is less than 24
hours old and the owner has not seen it since, it stays in Delivered · awaiting review under rule `closed`. Seen or older
closed concerns enter Done immediately like any other closed to-do. Every concern row, open or closed, carries one neutral
**Concern** tag next to its title, using the client's existing tag component; other rows do not change. A client whose
older hub does not send `kind` keeps its previous behavior.

On the web, the header's Parked count is the number of to-dos in the Parked section (included in open). It contains both explicit holds and ready merge waits (#9325). The `pr_open` rule and PR freshness/precedence are unchanged; only its web display section changes from `pr` to `parked`. Shared fixture expectations retain the legacy `pr` name for the hub and native clients; the web fixture reader maps that label explicitly, preserving the same rules and open total. The conversation tray and All board use this same web classification. A fallback date alone does not move a hold to Other open; an actual wake/review does.
Inactive counts only automatic 48-hour shelving. Cancelled has its own count and is never included in Done / completed.
The Parked section and review badge use the same localized name.
Apple (`apple/Shared/TodoTriage.swift`) and Android (`TodoTriage.kt` in `android/core/domain`) still need this
parked rule and naming alignment; #5929 records the parity gap. This web fix does not claim native-client delivery.

**A failed start whose work got done another way is not the owner's**: the hub closes a to-do whose agent never started
(`done_at` with its `start_error`), and rule 1 keeps it in Waiting for you so the owner can retry or remove it. When the
same piece of work finished in a related to-do (the same plan, PR, issue or title: its row), there is nothing left to
retry: rule 1a files it with that work and it counts as closed, on the page, the sidebar's badge and the assistant's
entries alike (`supersededFailedStarts` in each copy). A failed start that still waits on the owner reads as one on every
client: not ticked or struck through, why it did not start in red, and its checkbox closes it for good (`"done": true`
on it clears `start_error`: the owner closed it; `docs/assistant/todos.md`, API overview). The hub's assistant reads it
the same way (#10365): its snapshot counts such a to-do open (the open index marks it as not started) for exactly as long
as this table does (`triageCountsAsOpen`), so a start failing does not take one off the assistant's count, and
`task_plan`'s `todo_id` takes it up again, reopening it, instead of refusing it as done (`docs/assistant/todos.md`,
"Retrying on another launcher" for its other way out).

**Waiting for you is only for what the owner must do in person**: answer a question, grant a permission, decide on a
large feature. A deployed to-do's "Please verify" ask and a "done" conclusion are not: the supervising assistant
checks and accepts them by default, so they sit in Delivered · awaiting review (rule 8), their one-tap button still there for the
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
- The check cannot tell (GitHub or the hub's health unreachable, rate limit): held until it can be checked again.
  The task stays at the PR unless the upstream merge was confirmed. A failed check never establishes deployment.
- Nothing to check against: work that is not a GitHub PR, a PR in a repository the hub is not built from (the hub's
  commit is not in it), or a hub that does not say its commit (a local or Docker build). The agent's word stands,
  logged as unverified.

An open to-do that names a PR and says nothing past it (no delivery, `none` or `pr`; stage before Deploy), whatever
told it, is looked at the same way by that sweep: its PR merged and in the running hub makes it deployed (`live`:
Delivered · awaiting review), merged and not in it "merged, not deployed" with the watch above; a fresh confirmed open PR with no active work stays in Waiting for merge. Such a
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
pauses transient checks for 10 minutes), verification is unavailable, and the open to-dos' sweep leaves such a
to-do as it is (its PR may still be open). The file is checked first, GitHub only when the file cannot say.

Every manual development deployment must regenerate this receipt from the exact source commit used to build the
candidate, not from a moving branch name or an unrelated checkout:

```sh
python3 scripts/deploy/deployed_commits.py --commit <built-commit> --out <state-directory>/deployed-commits.txt
```

Run this from the build checkout, under the deployment lock, before switching the hub, and preserve the old receipt
with the rollback binary. The writer atomically replaces the file. After switching, compare its first non-comment
commit with `/api/v1/health`'s commit and check that the expected PR subjects (or proven `# pr:` heads) are present.
Do not hand-write PR entries: the generator verifies ancestry, including explicitly named `--pr` entries.
The standard deploy script generates and installs the receipt next to the binary; manual local switches must do
the equivalent. A stale receipt is ignored, not accepted as evidence of a different build.

An unavailable verification service does not mean unfinished work. Keep the task's state and retry verification
after recovery rather than requesting another merge/deployment, parking, or cancelling it solely for that reason.
Direct GitHub failures retain only a sanitized message and allowlisted status/rate-limit/request-ID metadata.
Permission errors do not pause other PRs. A transient pause reports its original failure and UTC recovery time;
it does not claim a fresh request was made. There are no automatic request retries.

**Authenticated GitHub reads**: the gate first borrows the signed-in built-in connector. When an extension has
replaced it and does not provide that transport, the gate uses `gh api --include --method GET --hostname github.com`
on the hub machine, under the hub user's existing GitHub CLI authorization. The CLI performs credential lookup and
the request itself; Persona never calls `gh auth token`, exports credentials, or falls back to anonymous HTTP.
When `gh` is not installed, the gate can instead make an authenticated HTTP GET using the hub service's explicitly
configured `GH_TOKEN` (preferred) or `GITHUB_TOKEN`. It sends this authorization only to `api.github.com` and does not
follow redirects. It does not extract CLI credentials or retry a failed CLI request through another transport.
Install `gh` on the hub service's PATH and authorize it using the CLI's normal sign-in flow if this fallback is
needed. Without either the CLI or explicit environment authorization, or after a failed request, verification stays
unavailable, not unfinished.
The service log identifies this authenticated route and records response status, quota and request ID. The existing
public-host restriction still applies to connectors. A versioned plugin delivery-snapshot capability remains a
possible follow-up, not a requirement for the authenticated CLI path.

**Deployed before the upstream merge** (#4867): a hub built from a local branch can run a PR that is still open. The PR has
no merge commit, and GitHub's compare does not know a local commit. So the file names such a PR by a commit of its own,
`# pr: <N> <sha>`: its head when the deployed commit contains it, else its newest own commit when the PR branch only merged
its base since. The hub counts the PR as deployed (stage Deploy, then verified as above) only when the file also lists that
commit, so the commit is in the deployed history.
Supervision reports this as a local deployment. Only the upstream PR response establishes whether it merged.

`scripts/deploy/deployed_commits.py --out <dir>` writes the file for a commit (`--commit`, default `HEAD`). It lists the
history, and the open PRs it runs, read with the GitHub CLI when that is signed in; `--pr <N>[=<sha>]` names more,
`--no-open-prs` skips GitHub. It writes under a temporary name and renames. It leaves out commits written before
2026-11-08 (`--since`): a `(#N)` in them is the previous tracker's number (`CLAUDE.md`, "Old issue numbers"), which the
hub would take for this repository's PR #N. One such subject once marked an open PR deployed. `scripts/deploy/deploy.sh` writes it beside
the hub's binary. A hub built and installed by hand, a local `dev` build, gets the same file from
`python3 scripts/deploy/deployed_commits.py --commit <the built commit> --out "$PERSONA_HOME/bin"`, run in the checkout it
was built from.

GitHub reads require the signed-in connector, the authorized GitHub CLI, or (when the CLI is absent) explicit
`GH_TOKEN` / `GITHUB_TOKEN` authorization in the hub service's environment, as described above. Without authorization,
even a public repository stays held as "cannot tell"; the gate never uses anonymous HTTP.

### Deployed work is verified by the supervising assistant

Once deployed (checked as above), a task's "Please verify" is accepted by the supervising assistant by default
(`by_assistant` on the ask, stage Verified, no Lark card): the owner is not asked for each one. Only a big feature, a
to-do with priority `high`, still asks the owner with the card, as before.

## Seen

Seen (#5707) files an ordinary finished turn that has no supervising assistant and needs no acceptance. It is a reading
receipt, not delivery acceptance:

- **Got it** on an ordinary, unsupervised Just finished row (a small button at the end of its status line, under the time, #6578, #7148; on iOS
  a compact accent `Chip` and a leading swipe action) marks it seen. On iOS the chip shares the status line when it fits;
  otherwise `ViewThatFits` puts it at the right of the next line, with the status's wrapping unchanged, through the
  xxxLarge interface-text cap (#6813). The row goes to Done at once, before the hub answers; a toast says so with
  **Undo**, which takes the mark off. A failed write puts the row back and says why. A supervised task or unaccepted
  delivery does not offer this action: Seen cannot accept it, so a button that says it moved to Done would be false.
- **Opening it** counts too: the iPhone and Android mark it seen when its page opens (the list behind has it in Done on the way
  back); the web when the page leaves it (another to-do, the list, another page), so its row does not move while it is
  read. Nothing is said about it. The Mac (frozen) does neither and files what another device marked.
- **In the Assistant's tray** (#10922) opening a row of its Finished entry marks the task seen on every client, and
  the row stays there, as Done, until 24 hours after the task finished: seeing a task changes only whether it is new
  (`docs/assistant/unified-entry.md` "Needs you and In progress").
- The mark is the to-do's `seen_at` on the hub (`PUT` / `DELETE /api/v1/todos/{id}/seen`, feature `todo_seen`), so every
  device agrees. It is not activity: `updated_at` stays, so it neither shelves nor unshelves anything. A client of a hub
  without `todo_seen` offers neither.
- For rule 8, a to-do is **seen** when `seen_at` is at or after its last activity. Anything that happens after the mark (a
  reopen, a new turn, a review) clears that relationship for the new activity. Seen never overrides explicit closure or
  acceptance, and never closes a task itself.
- An ordinary finished turn seen (rule 9a) is filed under Done even though the to-do is not closed: the owner has looked,
  and it no longer counts as open. A confirmed open PR that waits on a maintainer keeps its to-do open in Waiting for merge. A supervised task, pending
  review or unaccepted `live` delivery stays open and awaiting review.
- Checks: the shared cases (`*_seen*`, `supervised_finished_unseen_seen_stays_awaiting_review`,
  `unfinished_before_just_finished_and_seen_in_done`), `server/internal/assistant/todo_seen_test.go`,
  `web/src/views/Todos.seen.test.tsx`, `apple/PersonaTests/TodoTriageTests.swift` and `TodosLogicTests.swift`. In a
  browser and the simulator against the mock hub (`MOCK_TODO_LIST=1`; `MOCK_TODO_SEEN=off` is a hub without it):
  `node web/e2e/todo-seen.mjs <web URL> [--phase before|after] [--shots <dir>]` and the UI test `TodoSeenTests`
  (`TEST_RUNNER_PERSONA_TODOS_HUB`, `TEST_RUNNER_PERSONA_SHOTS`).

## Header count

The web's header reads "N open · N parked · N inactive · N done · N cancelled" beside the title on wider screens (a phone leaves it out); it counts what the list's filter shows, whatever is searched (#3740). The iOS and
Android lists have no such line (#3712): it said again what the sections say, so each section's title carries the number of
to-dos its rows list (a merged row's related ones too), in the section's state color, and the counts follow a search and the
list's filter (#3740). **Open** counts to-dos that are not closed or accepted,
and not shelved: Waiting on you, Working, Parked (including merge waits on the web), Other open, and Just finished rows still open (a finished turn
is not a closed to-do; only a review or the user closes it). A supervised task, pending review or unaccepted `live` delivery
stays open and awaiting review after Seen; an ordinary unsupervised finished turn files under Done once seen.
**Parked**, **inactive**, **done** and **cancelled** count the to-dos in their respective sections as their titles
do (a merged row's related to-dos under the row's section), so the header and each section's title say the same number;
what closed in the last 24 hours and was not seen yet is under Just finished and counted there, not as done. Cancelled
is always separate from Done. The sidebar's To-dos badge (web, #11085) counts the open to-dos that need the owner, not those Agents are on, and never more than this open count: what waits on the owner (Waiting on you, or work held for their OK) counts, a to-do its own Agent is on (Working) or whose pieces Agents are doing does not, and the rest counts (`docs/assistant/todos.md`, "Sidebar and main navigation"). Quiet tasks (#1731) are left out of it as before; the page lists and counts them. iOS and Android still badge this open count. A task whose agent never started (rule 1) counts as open while it waits on the owner.

The counts are the same whatever the list is grouped by (web audit item 12), and so is where each to-do is:

- **By folder**: the folders hold every to-do the status sections place outside Done and Cancelled (a merged row's to-dos where the
  row is), each in its own reading: a failed start waiting on the owner open, with why it did not start, and its folder
  says Needs you. An ordinary unsupervised Delivered · awaiting review row can offer **Got it** until it is seen; an unaccepted
  `live` delivery stays open even after that. The folded **Done** and **Cancelled** sections stay separate at the bottom.
- **By pinned direction**: the directions hold the open to-dos the hub's tree lists (it lists only what counts as open);
  a line says how many open ones no direction lists; the rest outside Done and Cancelled (unaccepted deliveries awaiting
  review and inactive work) keep their status sections below the directions, as Android's list does, so a to-do ticked there
  moves to Done instead of leaving the list. The folded **Done** and **Cancelled** sections stay separate.
- **By request**: a request with an open root or piece stays active, including one that also contains cancelled pieces.
  It enters **Done** only when its non-cancelled root and every piece are ordinarily done. A terminal request with a
  cancelled root or piece enters the separate **Cancelled** fold instead. Cancelled pieces never add to the Done
  numerator; a cancelled root keeps its own card and reason rather than merging into a same-title piece.

Before the first read of the to-dos lands the header shows no counts, not zeros (audit item 83).

## Shelving

A to-do is shelvable when it is a task (an agent was ever linked, or a conversation handed it out) and has no due date
ahead. The user's own to-dos without agents (reminders, errands), ones due later, and unaccepted `live` deliveries never
shelve themselves. The last case remains open under Delivered · awaiting review until it is accepted or explicitly closed.
An open concern never shelves either: it waits for its report, however long.

- Shelved to-dos are folded under **Inactive** on web (**Shelved** on native clients), not counted as open, and not changed on the hub: nothing is deleted, closed
  or released. Any new activity (an agent working, a report, a review, an edit) takes it out again at once.
- **Restore** on a shelved row counts as activity now: the to-do goes back to its section and shelves again only after
  another 48 hours without a move. The restore is kept in this browser (`localStorage`, `persona.todos.restored`);
  another browser or device still shows it shelved until it moves. The hub now stores restores too (`restored_at`,
  `PUT /api/v1/todos/{id}/restored`, `docs/assistant/todos.md` "Restores and Someday"), but today's triage still uses
  each device's own restore, and the hub's copy of the triage uses none, until the clients switch to the status the hub
  works out (`view`, `docs/design/proposals/todos-redesign.md` section 6.1), from stage 2 of #10226.

## Related to-dos

To-dos that are the same piece of work share one row, led by its most urgent member (by section, then most recent
activity); "N related" under the row opens the others. Two to-dos are related when they share any key:

- the same PR link (case and a trailing slash ignored);
- the same issue number in the title (`#412`);
- the same title once case, spaces and punctuation are dropped (six characters or more);
- the same plan (`plan_id`: tasks one request was split into).

Relations chain (A shares a PR with B, B an issue with C: one row). Open, completed and cancelled to-dos use three
separate sides: none joins a row of another state, and a cancelled task never makes a related failed start look completed.
An open concern always has a row of its own: it never merges into a related to-do's row, and no related to-do merges
into its row. A closed concern groups like any closed to-do.

## Accepted tasks

When `task_review` (or the judge's spot check) accepts a task, the hub closes it. If its conversation, its supervisor
or the background supervisor that follows it up for the main chat then sends its agent more work (`agent_send`,
`agent_interrupt` with `then`, or the owner's message on that task's card), the hub reopens it while the supervision
still says `accepted` (`docs/assistant/todos.md`). The background supervisor counts only for a task whose ledger names
the main chat: a task a side chat supervises reopens for that chat's sends, not for the background supervisor's.

The send is noted before it goes, with the task and its acceptance. The note is kept in the assistant's settings
(`assistant.sends_out`, `server/internal/assistant/tasks_sends_out.go`), so a manager restart does not lose it; when it
cannot be saved, the send is not made and says so. A later write of the notes that fails (removing a refused send's,
say) is tried again in the background until one goes through. How the send went decides:

- the agent's computer took it: the task reopens at once (`reopenTodo`, stored);
- it definitely failed (refused, or it never left the manager: `rpc.ErrNotSent`): nothing reopens;
- its answer never came (the wait ran out, the link dropped), the manager restarted before it came, or the computer
  could not confirm it (`delivered: unknown`: typing failed part way, a Claude inbox holds the text for approval): the
  text may still arrive, however late (queued behind a long turn, or let in when the person at the computer looks), so
  the agent's next turn reopens the task. The note goes once the task is no longer that agent's closed task on that
  acceptance (reopened, the agent moved to another task), once the task is accepted again (also while it is still
  closed: that acceptance answers every send before it, even one whose answer is still to come), and after a week at
  the latest.

A turn of the agent that starts while the send is still out waits for that answer (`closedTurnReopens`): if the send
definitely failed and no other send for the task is out, the turn was other work and the task stays closed through it;
otherwise the turn is the task's, its end included, also when a later send to the agent went out meanwhile or the
manager restarted before the answer (`recoverSendsOut`). A bare `/exit` or `/quit` asks for no work (the agent's
computer closes the agent) and reopens nothing; on the card of a task whose agents have all stopped it starts no new
agent (`route_agents_exited`). A card message reopens only the task whose card it was sent from;
when the task's agents have all stopped, it reopens the task once the new agent started, and leaves it closed when that
start definitely failed. That agent's other turns leave the task closed, whether or not its plan started the agent: a
wakeup it set itself, other conversations' work, the owner typing in its session (#6726, #7539). Their ends raise no
review signal either.

Gaps remain. A send whose answer is unknown and whose text never arrived reopens the task on the agent's next turn of
other work, whenever it comes within the week; one whose text the agent takes up more than a week later leaves the
task closed. A refused send whose removal cannot be written at all until the manager restarts (the database refuses
every write meanwhile) reads after the restart as a send with an unknown answer. And after a restart, a turn of other
work that was already running when the send was taken ends as the reopened task's: the conversation gets one extra
receipt for it, with no judgment and no review signal.

Earlier the page treated every reopened task as not reviewed: it showed "Follow up", its badge "Supervising", and
criteria added with the extra work (`agent_supervise` / `agent_send` replace `done_when`, so the list grew from 5 to
8) as open with a clock and counted them ("5/8").

The acceptance now stands until a newer review replaces it (`acceptanceStands` in `web/src/lib/supervision.ts`):

- with nothing of it running or asking, the task reads **Accepted** in the row and the detail, and sits in Done (rule 6);
- with its agent working or asking again, it is in Working or Waiting for you like any task, its badge still Accepted;
- criteria no review judged after the acceptance are marked "not checked" (a dash, not a clock), and the count only
  covers judged ones ("5/5"). The next review, if the supervisor runs one, judges them as usual.

A task the user reopens by hand also keeps the accepted verdict until the next review; the checkbox closes it again.

**A new Agent from a plan starts a new round, not more work** (#11553). When a plan's task takes the to-do over with
a new Agent (`task_plan` with `continues`, or with its `todo_id`), the hub keeps the earlier `accepted` or
`cancelled` review in `reviews` as history. It puts the ledger back to going on: `state` "", `round` 0, no owner wait
(`startSupervisionRound` in `server/internal/assistant/supervise.go`). Rule 6 no longer applies to it. The row shows
**Supervised**, and the to-do stays in Working, Waiting on you or Other open while the new Agent works or stops. The new
round's stops reach its supervisor like any task's. Before, the continued to-do kept the earlier round's green
**Accepted** in Working, went to Just finished whenever its new Agent stopped, and its supervisor never heard of a stop
short of done. A ledger continued before this change keeps its old state until its next review: records are not
migrated.

## Clients

Every client follows this document with its own copy of the rules, each with the same test cases (#3206):

| Client | Rules | Tests | Page |
| :--- | :--- | :--- | :--- |
| Web | `web/src/lib/todo-triage.ts` | `web/src/lib/todo-triage.test.ts`, `web/src/lib/todo-triage-fixture.test.ts` | `web/src/views/Todos.tsx` |
| iOS / Mac | `apple/Shared/TodoTriage.swift` (`TodoTriageLogic`) | `apple/PersonaTests/TodoTriageTests.swift`, `apple/PersonaTests/TodoTriageFixtureTests.swift` | `apple/Persona/Views/Todos/TodosView.swift` |
| Android | `android/core/domain/src/main/kotlin/persona/core/domain/tasks/TodoTriage.kt` | `android/core/domain/src/test/kotlin/persona/core/domain/tasks/TodoTriageTest.kt`, `TodoTriageFixtureTest.kt` | `android/feature/agents/src/main/kotlin/persona/feature/agents/TodosScreen.kt` |
| Hub | `server/internal/assistant/todo_triage.go` | `server/internal/assistant/todo_triage_test.go` | none: it tells when a leader's task is stuck (#3483) |

The shared cases in `spec/fixtures/triage` are read by all four implementations: the web app, Apple and Android fixture
suites, and the hub. A rule change adds or changes a case there first; copying its expected value into a client-only test
is not a substitute for replaying the fixture. The hub, web and Android replay every concern case. Until Apple's native
concern follow-up, its fixture suite replays every non-concern case and keeps the documented compatibility behavior.

A change to an existing rule changes all of them in the same PR. Native differences: iOS / Mac keep restores in the device's
`UserDefaults` (`todos.restored`) and Android in its local preferences, as the web keeps them per browser; the iOS list
has no Status | Folder switch (it had no folder view), and the Mac shows a merged row's members as a disclosure under the
lead. The iOS list draws a row's "N related" inside the lead's row, under its words (Android's card already had it). iOS and
Android filter and sort the list on the device (`docs/assistant/todos.md`, "List filter and sort"; the web follows). Moving the classification to the hub, so the clients only draw the section it sends, would remove the copies; it
is not done yet.

## The hub's view (#10226)

The manager works out each to-do's `view` (`schema/wire/todo-view.schema.json`: phase, section, reason, line 2,
times, marks, asks, steps and rank) and the list's counts and places, by the redesign's decision table
(`docs/design/proposals/todos-redesign.md` 6.3), in `server/internal/assistant/todo/projection`, fed by
`server/internal/assistant/todo_projection.go`. The rules' source is the shared cases: `spec/fixtures/todo-view` and
`spec/fixtures/todo-counts`, whose READMEs list every rule, field, line key (a stable key with string arguments, never
words: "Line keys"), judgement call and the reasons not produced yet. It differs from the rules above where the
redesign changes them:

- **A failed start stays open**: Needs you for 48 hours after the failure, then To do with the Inactive mark, never
  Done, though `done_at` stays stored (rule 1). One whose work finished in a related to-do is closed (rule 1a).
- **Rule 9a**: finished work the owner has seen stays in Just finished for 24 hours after `seen_at`, then is a To do to
  tick (`finished_seen`), never Done while it is open. Finished work nobody has seen stays in Just finished however
  long, accepted and deployed work included (this table's rules 6 and 7).
- **Parked is Waiting**, on the hub too (this table's rule 5a, until now the web's alone) and above finished work; an
  open PR and a usage limit wait there with it.
- **A run that waits on another Agent or an outside event it names is Waiting** (`someone_else`, line `run_waits`),
  not Working (rule 4); background work and a `/loop`, which end by themselves, stay Working, and so does a moment's
  model failure while the hub's "Continue" is on its way, which rule 2 reads as cut off.
- **Can wait**: a decision marked for the briefing (`can_wait`) and a run that ended on a plain-text question
  (`awaits_reply`) are listed under Needs you's fold, not counted in its number.
- **Supervisor handling (Web, #17114)** is the Working row label when all of a task's pending cards are held by its
  supervisor. A card the owner can answer, including one the supervisor passes up, takes priority. The label uses
  the existing Working group and filter.
- **Unfinished stops are not finished replies** (#9823): after more than one hour without supervisor follow-up,
  they need the owner (`stopped_unfinished`), even when the legacy To-dos page reads an unread reply or pending
  review as Just finished. The shadow names this decided difference; it does not change the page or its counts.
- **Inactive is a mark, not a section**: the to-do keeps its section (To do) with the time activity stopped. A restore
  counts as activity: the view reads the hub's `restored_at` ("Restores and Someday" in `docs/assistant/todos.md`),
  not each device's own.
- **Every ask says how risky answering it is** (#10465): `risk` `safe`, which a row, its card or a push may answer in
  place, or `open`, which the owner answers after a look, at its home, with `lockscreen` `open_only` so a push about it
  offers nothing on the lock screen. A decision is `open` when it cannot be undone, other people will see it, or its recommendation requires the owner’s terminal (the
  manager's `askRisk`, also written on the list's `ask`, one rule for every place that answers it); a retry is
  `safe`; a run's ask is what its pending card asks (`question`, `plan`, `tool`, `form` or `secret`, the card's id as
  `interaction`; the session's own card before Persona's about its turn, then the oldest), `safe` only when a
  notification may answer that card too (one question with options, not a Just me turn's or a health conversation's,
  and its asker did not say going ahead cannot be undone. The hub and Android omit option actions for an explicitly
  irreversible question. See `docs/protocol/protocol.md`, "Answering a question from its notification").
  A failed start of a to-do no conversation opened carries no retry, since the hub would refuse it; its row still
  says why it did not start. The table is in `spec/fixtures/todo-view/README.md` ("Risk and the lock screen"). The
  decision's push says the same rule (`lockscreen`; `open_only` sends `TODO_DECIDE_OPEN`, which offers no action,
  `docs/assistant/todos.md` "Waiting for the owner").
- **Every ask is answered by its id** (#10465): `POST /api/v1/todos/{id}/asks/{ask}/answer` takes the id an ask has in the
  row's `view` (a step's ask on its parent's row too) and an option, a fixed answer, words, Later, Don't handle or its
  Undo, or nothing for a retry or a conclusion to acknowledge. An id whose round is over is 409 `ask_stale` with the
  row as it is now, so what was answered elsewhere is never answered twice; a run's ask is answered at its card. The
  hello feature `todo_asks` says every manager takes it (`docs/assistant/todos.md` "Decisions the owner can answer").

The list carries it: every row of `GET /api/v1/todos` has its `view` and the answer the list's `counts`, worked out
once per read over the whole list (`server/internal/assistant/todo_view_routes.go`); `GET /api/v1/todos/{id}` answers
one row the same way and `GET /api/v1/todos/places` the places (`docs/protocol/protocol.md`, the to-dos rows). Every
time in a view is absolute, so the list's bytes, and its `ETag`, stay the same while neither the to-dos nor their runs
change (a working run's latest line is in its row's view). Across managers the hub only adds the counts up and joins
the places; it never works out a view. The hello features `todo_view` and `todo_places` say every manager serves them,
and only while they are listed do the counts cover every manager. The manager also pushes `{"type":"todos"}` when a
run's change or time alone changes what a row is filed under (`docs/assistant/todos.md` "Live WebSocket pushes").

No client draws from `view` by default yet: the clients switch to it in stage 2, falling back to the rules above for a
row without one. Until then every client, and the hub's own triage for the direction tree and leader reports
(`todo_triage.go`), follows the rules above. The web's Developing Persona shadow compares the two on real data and
records what the decided differences (the list above, Steps rolling up and Someday;
`web/src/features/todos/model/view-legacy.ts` `BY_DESIGN`) do not explain (`docs/web/web.md` "Developing Persona").

## The to-do's timeline (#10465)

A to-do's conversation (`docs/design/proposals/todos-redesign.md` 3.6) is its history grouped into rounds: each
instruction or answer of the owner's heads a round, and what followed folds under it (relays to its runs, one line per
run with how many turns it took, asks, decisions, results, scope changes); supervision nobody asked for (reworks, parks,
a run's reports to its supervisor, the overdue mark) forms one folded Background round per day in the owner's zone.
With it come the to-do's results (at most three: a code change with how it stands, the work going live, what the work
came to) and what changed since the owner last looked (`seen_at`), for the paper's Results and Since you looked. The
shapes are `TodoTimeline`, `TodoResult` and `TodoRecap` in `schema/wire/todo-view.schema.json`.

In stage 2 the manager derives it, with no migration, from the stores it already keeps: the conversation feed's items
about the to-do (the owner's instructions, relays, receipts, answered cards, reviews, reports), its decisions, its
supervision ledger, its brief's request and log, and its runs' links and sessions. The rules live in
`server/internal/assistant/todo/timeline`, pure as the projection is, and the shared cases in
`spec/fixtures/todo-rounds` pin them; its README says what each entry is read from, the round rules and every line key.
The paper shows the latest two rounds that are not Background ones, so a day's supervision never pushes the round that
asks the owner off it.
Receipts and relayed messages written since #10465 name their run; an older receipt goes to the one run on its
computer at its time, else to "A run". Stage 3 stores the entries themselves (`todo_activity`) and with them what the
stores cannot show today: messages sent straight to a run, the steward's replies, message states other than
delivered, and the rounds of the to-do a take-over continues.

The manager works the timeline out on each read, from the feed's items about the to-do in any conversation (the newest
1000, read through the feed's existing `assistant_feed_unsent` index), its decisions, its ledger, brief and runs' links,
the sessions of its runs and the owner's zone (`server/internal/assistant/todo_timeline.go`), and keeps nothing of it.
`GET /api/v1/todos/{id}/timeline` pages through the rounds, oldest first, back from the latest;
`GET /api/v1/todos/{id}` carries what the paper shows beside the row (the rounds above, how many there are in all, the
results and the recap), so opening the paper takes one read. Both are behind `todo_timeline`, which the hub lists only
while every manager names it (`docs/protocol/protocol.md`, their rows). There is no push of its own: a client with the
to-do open reads it again on `{"type":"todos"}` (decisions, supervision and scope changes send one) or on an
`assistant_feed` whose item's `ref` is the to-do, and since each item read works the whole list's view out, it reads
only while the paper is on screen and coalesces them.

A to-do with more than 1000 such feed items serves the rounds that began within its newest 1000: its decisions, brief
log and ledger reviews are cut at the same time, and the rounds that began earlier are left out, so every round served
is whole and keeps its id as newer items come. An extension's token reads it without the answered cards, whose records
hold what the owner typed into a card and do not say whose card it was, and not at all for a to-do that may hold the
health space's words: one opened from or written to by a health conversation, one any of whose runs ever worked there
(taken off it or not, done or not), and, since this fails closed, one with a conversation or run whose space cannot be
told (`docs/extensions/extensions.md` "Never shown to extensions").
