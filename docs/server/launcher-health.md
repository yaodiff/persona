# Launcher health checks and automatic repair (#2873)

Launchers on your computers break silently: the gateway rejects a bare model name (`400 Bare Claude model name cannot be routed`, and after switching models `no account match`),
the gateway key expires, the CLI is too old, or starting an Agent keeps timing out (`context deadline exceeded`). Nobody used to notice, and a dispatched Agent failed on its first message.
Now a hub manager (`server/internal/assistant/launcher_health*.go`) periodically checks every launcher on every computer, routes work around broken ones, repairs known errors itself, and records what it cannot repair as blocked.
**Record only, never push**: no notifications, nothing on the Dynamic Island; the App and the board read it themselves.
**Off by default** (decided 2026-11-05): periodic checks and automatic repair only run when the manager's `manager.json` sets `agents.launcher_health.enabled: true` (`docs/operations/config.md` ("manager.json")); when it is off, failed Agent starts are still recorded and still routed around.

## Checks

A runtime can opt into `caps.check` and answer `launchers.check` in its own authentication context. This keeps availability
checks consistent with its normal sessions when the node's SSH login shell has different credentials or keyring access.
The node passes the existing check prompt and a temporary workspace, removes that workspace afterward, and preserves
runtime errors. Shell launchers, managed launchers and older runtimes keep their existing shell-based checks.

- It reuses "Test availability" (`docs/integrations/launchers.md`): the node's `launchers.check` runs `claude -p … --max-turns 1` / `codex exec` once in an empty directory, with a 90-second timeout, spending a few tokens.
- **The check leaves nothing behind** (#2959): it runs with `--no-session-persistence` (Codex: `--ephemeral`), and removes the empty projects directory Claude Code keeps for its temp directory even so; a CLI without the flag is retried without it. A check a hundred times a day used to leave that many transcripts, filling the 80 newest offline sessions the session list keeps and pushing the user's Agents out of it. The list also skips the check's transcripts on its own — its temp directory is gone, which is what tells them from the user's own work in a directory with a similar name (an older node's name included) — and only sessions it lists count toward the 80: one in a blocked directory or a wordless probe no longer takes a slot.
- **Interactive-readiness pre-check for Claude** (#2018): before spending that request, the node reads `hasCompletedOnboarding` from Claude Code's global config (`.claude.json`, located by Claude Code's own rule, including a `CLAUDE_CONFIG_DIR` the login shell exports).
  A headless `claude -p` probe answers even while the first-run setup is unfinished, but an interactive start then stops on the first-run screens (style picker, sign-in) and ends with a generic readiness timeout; the check therefore fails at once with an error that names the unfinished setup, without spending a request. A missing config file means Claude Code has never run there and counts as unfinished; an unreadable or malformed config is not a verdict and the real probe runs anyway. An interactive start that still reaches its readiness timeout gets the same explanation appended when the flag says the setup is unfinished.
- It reuses "Test availability" (`docs/integrations/launchers.md`): the node's `launchers.check` runs `claude -p … --max-turns 1` / `codex exec` once in an empty directory, with a 150-second timeout, spending a few tokens.
- When: it looks every 5 minutes (`healthTick`); on computers that are online (`Reachable`), any launcher last checked longer ago than the interval (default 30 minutes) is rerun.
  When a computer goes from offline to online, all its launchers are checked right away. Launchers on one computer run one after another (no burst of claude processes); computers run in parallel, 12 seconds apart.
  Every computer runs the same probe prompt, and a model gateway can pin the same prompt to one upstream account: without the gap a round sent all computers' identical requests at once and the account served them behind one another until each timed out (2026-11-13). The gap also lets the later computers find the cache the first one wrote.
- A check that times out is asked once more right away, and only a second timeout in a row is recorded (`probeLauncherWithRetry`). A gateway can leave a single request without a first token while the same upstream account answers its other requests in seconds (2026-11-14: one shared account left a few percent of its requests silent for 90 seconds and longer); one such request used to mark a working launcher `failing` until the next round, half an hour later. Other errors (a 401, a model the gateway refuses) are recorded at once.
- What is checked: launchers this computer can start (`machineLaunchers`: those from ~/.zshrc minus hidden ones, plus Persona-managed ones; `kind: api` launchers do not run on a computer and are not checked).
  Runtime launchers (DSH, agy and others, whose `source` is the runtime's id) are not checked, with one exception (#10369): the main chat's launcher and the Agents' default launcher.
  While one of those has a failing record on a computer that lists it (`failing`, `repairing` or `blocked` within 6 hours, or a usage limit in force), the round checks it there like any launcher (once per interval, through the runtime's own `launchers.check`, as the on-demand probe below already does), and so does `POST /api/v1/launchers/health/check`; a failure updates the record's check time, a pass clears it.
  A runtime launcher of theirs that passed, or never failed there, is not checked again, so no runtime spends a request each interval; its record stays while the computer lists the launcher. Before, nothing rechecked such a record (a round dropped it instead, as a launcher the computer does not have), so the main chat's readiness could stay in setup for up to 6 hours after the cause was fixed.
  Asked for by name (`POST /api/v1/launchers/health/check?machine=<id>&launcher=<name>`, the alert sheet's **Test again**), any runtime launcher the computer lists is checked, whatever its record says and whoever runs on it (#14534): a person asked for that one request. Before, a runtime launcher that was neither the main chat's nor the Agents' got no check and an empty answer, so a failure a start had recorded stayed until it went stale.
  A check drops only the records of launchers the computer no longer lists (its whole list, not just the ones that run checks), so checking one launcher never erases another's record.
  The desktop apps' launchers (`claude_desktop`, `codex_app`), whose check runs no prompt, are never checked.
- A failed Agent start is recorded too: when `agent_start`, a task plan starting an Agent (`startTaskAgent`) or a repair Agent's start gets an error from `session.start`, it is recorded as a failure (source `start`),
  and a check runs right away to confirm (one network blip should not leave a launcher unavailable; if the check passes, it recovers).
  That follow-up reserves the same single-flight probe used by an immediate detour, including its 30-second throttle, so one failure does not run two checks.
  So are `shell_waiting` and `start_line_rejected` (#2570): the start ended in the new tab's shell before the launcher ran.
  A coded `codex_not_logged_in` error (the Codex start met its sign-in screen, `docs/integrations/codex.md` "Starting from the phone") is recorded as `auth` from its code (`codedLauncherErrorKind`), not from the node's words, which are in its own language; the same code makes a failed task offer another launcher.
  A coded `agent_start_stalled` error is excluded: the agent opened but its session could not be identified before the deadline. It leaves launcher health unchanged, runs no probe and ends the task without an automatic retry or detour. The node stops the abandoned start before a later retry opens a replacement (see `docs/integrations/codex.md`). An older node that returns only text retains the previous generic-failure handling.
  The App's own starts count as well (#1642, `launcher_health_starts.go`): the hub tells the manager how a start it waited for went (`session.start_ended`: a failure, or a start the node ran before answering that worked),
  and a "New Agent" answered at once (`async`) is followed in the machine's session list, from `starting` to started or to `start_error` (counted once while the node lists it: the starts counted are kept with the records, so one is not counted again after the manager restarts; the failure's `updated_at` is the node's clock and is not compared with the record's).
  A start whose outcome the hub does not know (the computer offline, the call timed out) and a request the node refused before running anything are not recorded.
  A timed-out call is one whose own context ran out or was cancelled (`context.DeadlineExceeded` / `context.Canceled`, or an uncoded relayed error with exactly those words, `startWaitEnded`, #6324): a node link stuck for the whole `taskStartTimeout` says nothing of the launcher, so it is only logged. A node's own timeout text (`session.start: context deadline exceeded`) still counts as `timeout`.
  An unanswered task start is reconciled before it fails (#6540): the hub snapshots the machine's session list before `session.start`, and when the answer never comes back (`startResultUnknown`) it lists again for up to `taskStartAdoptWait` (30 s, polling every `taskStartAdoptPoll`). A manager-started Claude start carries its own `session_id` (a v4 UUID the hub generated), and the reconciliation first matches that exact id on the machine's list — only the session that start created can carry it, so a session the owner started by hand, or another workspace-none task's session in the same directory, can never be misadopted (#6908). Only when the node is too old to honor `session_id` does the hub fall back to the heuristic (`adoptStartedSession`): a session absent from the snapshot, hub-started, with the same launcher and directory and a fresh sign of life, that no open to-do has linked yet (`todos.activeBySession`), newest wins. The open-to-do exclusion applies in every fallback, so a task can never steal another task's Agent; its remaining limit is two unanswered starts in one directory on a node that ignored `session_id`, where one could adopt the other's session (a mislabeled card, never a duplicate Agent). If the pre-start list could not be read, the machine stops answering, the to-do book cannot be read, or no matching session appears, the start fails as before and stays on its machine: an unknown result never triggers a retry or a flexible-task reroute.
- **On-demand recheck before a reroute** (#2018): a start aimed at a machine whose launcher has a failing record probes that machine once on the spot before moving elsewhere, and a fixed-machine task probes it the same way before refusing (`launcherUnusableNow`, #6324).
  If the probe passes (the launcher was fixed after the record, for example a CLI installed or a PATH corrected), the record is cleared to `ok` at once and the start stays on the asked machine.
  If it fails again, or the machine cannot answer, the usual reroute applies. Concurrent starts for the same machine × launcher wait for one probe (single flight), and a probe is not repeated
  within 30 seconds (`healthRecheckThrottle`), so a burst of starts does not turn into a burst of probes. This also covers the case where periodic checks are off: no manual button is needed.
  A `usage_limit` is also rechecked (#9386), whether its reset is known or not: quota can recover before `resets_at`, so a passing probe clears it early. Fresh limit evidence waits 30 seconds before any on-demand probe; subsequent attempts share the same throttle. A failed probe still refuses a fixed-machine start without launching an Agent.
  The record keeps what is known of the limit per model family and changes only by the rules in "Usage-limit record rules" below.
  A probe for a family limit requests that family; an older node or runtime that ignores the model answers with another family and clears nothing. A probe's result counts as current only against the version of the record it read, compared under the record's lock. Admission rereads the published record rather than trusting the raw probe answer.
  `not_in_terminal` is not rechecked this way: the probe's login shell cannot reproduce the terminal's failure; only a real successful start clears it.
- An Agent's own usage limit is recorded too (#1710, source `session`): when a turn ends on the subscription's usage limit (`docs/assistant/agent-work.md` "Usage limits")
  and the session names its launcher, that launcher on that computer is marked `usage_limit`: until the reset when it is known and still ahead; with no reset known, the families the limit names are limited with an unknown reset for at most 6 hours after the turn (#9646, "Usage-limit record rules" below). Nothing runs for it: no check (it would spend a request to be refused) and no repair.
  The main chat's conversations count as well (they run the computer's launcher under the same sign-in). Each turn a background task wakes meets the same limit again; it is recorded once.
- Stored in the settings key `launchers.health` (Postgres), one entry per computer × launcher: success / failure, error text (redacted, truncated), error category,
  check time, consecutive failure count, repair log, state. It survives hub restarts.
- **Clean error text** (`internal/textfmt`): the check's output and a start's error arrive with the terminal's colour codes and prompt records (iTerm2 shell integration,
  zsh's `PROMPT_SP`) and with the shell's own startup noise (a gitstatus restart line, `Console output during zsh initialization`). The node strips the escapes before anything
  reads the text, then redacts and summarizes it to its error line, so a URL or IP that an escape wrapped is still redacted and the category is read off the error, not off a banner
  (`[oh-my-zsh] Would you like to update?` in front of a 401 stays `auth`, not `cli_outdated`). The manager cleans what it is given too, and `GET /api/v1/launchers/health`,
  `agents_list`, the launcher lists and the apps' Test strip the escapes from records an older node or hub persisted, so a banner never reaches a reader. The web's
  `lib/ansi.ts` does the same on the client.

## Usage-limit record rules

A `usage_limit` record (#9386) keeps what is known of the subscription's limit per model family, and changes only by these rules (`recordHealth`, `foldLimit`, `launcherLimitTurnSettled`). A limit is **in force** while the record refuses starts (`unusable`).

**Each family's state.** Families are `opus`, `sonnet`, `haiku`, as the limit's words name them ("You've hit your Opus limit").

| State | Stored as | Effect |
| --- | --- | --- |
| Limited, reset known and ahead | in `limit_models`; `limit_resets[f]` = t > now | Holds until t, unless a probe of that family passes first |
| Limited, reset unknown | in `limit_models`; no `limit_resets[f]` | Holds until a probe of that family passes, or the record's evidence is 6 hours old (below) |
| Limited, reset known and past | in `limit_models`; `limit_resets[f]` ≤ now | Over as far as the record's reset goes. A new failure naming it is a new limit: the old reset is dropped first |
| A limit that names no family | `limit_models` empty; `resets_at` known or not | Any model's answer clears it |

**The record.** `resets_at` is the latest of the families' resets, and unknown while any family's reset is unknown; with no family, it is the limit's own reset. A limit with a known `resets_at` holds until then; with an unknown one, until 6 hours (`healthStale`) after the latest evidence, and in any case until each family's known reset still ahead (`familyResetAhead`): a family whose reset is unknown makes `resets_at` unknown but never shortens another family's limit. The latest evidence is `checked_at`, which an Agent's turn whose reset is unknown sets to the turn's own time, not to when the hub read it (events below). Either way an on-demand probe can clear it early, at most once per 30 seconds (`healthRecheckThrottle`) after the latest evidence. A probe asks for the first limited family.

**Events.** "Fold" is the merge in the next table.

| Event | Limit in force | What changes | rev, check time |
| --- | --- | --- | --- |
| A usage-limit failure: probe (current), start, an Agent's turn with a reset ahead | no | The record becomes this limit: the families named, each with the failure's reset when known; no family: `resets_at` is the failure's reset | advance, now |
| A usage-limit failure: probe (current), start | yes | Fold; error text, source and failure count updated (a fresh refusal is new evidence even when the fold changes nothing) | advance, now |
| A usage-limit failure: an Agent's turn with a reset ahead | yes | Fold; when the fold changes nothing, **nothing**. This is the dedupe: the turn is judged by the families it names, not by unrelated families | advance and now only on a change |
| A usage-limit failure: an Agent's turn whose reset is unknown (or further than `healthLimitMax`, taken as misread), less than 6 hours old | no | The record becomes this limit: the families named, each with an unknown reset; no family: no `resets_at` | advance; check time: the turn's time |
| The same | yes | Fold with an unknown reset; when the fold changes nothing, **nothing** (the same dedupe), unless the record's evidence is 6 hours old (only another family's known reset holds it): then the turn renews it | advance on a change or renewal; check time: the later of the record's and the turn's |
| An Agent's turn on a usage limit whose reset is past, or with no reset known and 6 hours old or older | any | Not recorded: the limit it met is over, or its evidence is stale | none |
| A usage-limit failure: probe that read an older version | yes | Fold only (error text and check time kept) | advance on a change; check time kept |
| The same | no | Dropped | none |
| A pass: probe (current) or the apps' Test | yes | Removes the families the answering model belongs to, with their resets; for a limit that names no family, everything. Nothing left: the record is `ok`. Covering no limited family: nothing | advance, now (on a change) |
| A pass: probe that read an older version | any | Dropped | none |
| Any other failure (timeout, auth, terminal) | yes | Nothing: it never reached the quota | none |
| Any other current result | no | The general rules (the record is replaced) | advance, now |

**Fold** (a failure naming families F with reset R, R possibly unknown, into the limit in force).

| Case | Result |
| --- | --- |
| F names families | Each f in F is added. If f's known reset has passed, it is dropped. With R known, f's reset becomes the later of its own and R. With R unknown, f keeps its known reset, or stays unknown. Families not in F are untouched. |
| F names families; the limit in force names none | As above, the old limit's known reset applies to F as a floor when R is known. With R unknown, F's resets are unknown |
| F empty ("usage limit reached"); the limit names families | Families unchanged. With R known, every known family reset becomes the later of its own and R. Unknown resets stay unknown |
| F empty; the limit names none | `resets_at` becomes the later of the two. An unknown one becomes R; an unknown R changes nothing |

Nothing is ever taken away by a failure. "No new information" means the fold changes neither the families, nor any family's reset, nor `resets_at`. It is compared as read (`limitModels`, `limitResets`), so loading an older format counts as no change.

**An Agent's turn with no known reset** (#9646). The turn is the same evidence a failed probe would be: a real request the subscription refused. So it is folded the same way, with an unknown reset, and ages by the same rule as every failure without a reset: 6 hours (`healthStale`), counted from the turn's time (`updated_at` of its session, the time its words are read against; never later than now). A turn the hub reads only later, a transcript an Agent is woken on long after it stopped, has that much less to say of the quota now, and one 6 hours old or older says nothing.
- No shorter fixed window: nothing says when a limit with no reset ends, and a family's limit can last days (a weekly one), so a short window would let through starts the limit refuses again. Holding the record costs little: any start probes it from 30 seconds after the evidence (on-demand recheck above), and a pass of the family clears it.
- A turn that repeats a limit in force changes nothing (no rev, no check time), so an Agent a background task keeps waking neither extends the limit nor holds off the next probe. After the limit ends (its 6 hours, or a pass), the next such turn is a new limit, as for a turn with a known reset.
- The record keeps one evidence time for all its families: an older turn never moves it back, and a pass that leaves a family moves it forward (as every pass does). When it goes stale, a family whose known reset is still ahead holds until that reset (`familyResetAhead`), so a Sonnet turn with no reset cannot end an Opus weekly limit after 6 hours. While only such a reset holds the record, a new turn with no reset is fresh evidence its families need, and renews the check time (once: the next repeat finds it fresh).

**Versions.** `rev` is a counter in the hub's memory. Every `recordHealth` write that changes a record advances it: every write in the events table marked "advance". It is not stored, so a restart starts again at 0, and probes in flight end with the process. A probe keeps the record it read. Its result is current only when both the check time and `rev` still match; otherwise it is an "older version" row. Deleting a record (a launcher's setup changed) is caught by the check time. `setHealthState` changes only records that are not usage limits.

**Stored fields and older formats.**
- Stored: `kind`, `error`, `checked_at`, `source`, `resets_at`, `limit_models`, `limit_resets`. `rev` is not stored.
- `main` before this change (9489ee4c81 and earlier) wrote neither `limit_models` nor `limit_resets`. When read, the families come from the error's words, each with `resets_at` as its reset.
- One rule reads it: no `limit_resets` and a known `resets_at` give every family that reset. In the current format the combination never arises with families: `resets_at` is known only when every family's reset is, and those are in `limit_resets`.
- This PR's first commit (a6d5d78dfb) wrote `limit_models` without `limit_resets`. It was never merged to `main` nor deployed; the same rule happens to read it.
- Of that draft format, a record with several families and one shared `resets_at` would read as every family resetting then. That gives a reset to a family whose reset was unknown. As the format never reached `main`, it is not handled.

## Error categories (`classifyLauncherError`)

| Category | Matches | Automatic repair |
| --- | --- | --- |
| `onboarding` | `hasCompletedOnboarding` missing or false: Claude Code's first-run setup is unfinished (the headless probe passes, an interactive start stalls on the first-run screens) | None, however many in a row: only a person at the machine can finish the setup. Stays `failing`; periodic and on-demand probes clear it to `ok` once setup is done |
| `usage_limit` | Checked first: Claude Code's own line (`You've hit your session limit · resets 4:20pm (Area/City)`, also after a prefix such as `Error:`; an older `Claude AI usage limit reached`), Codex's (`You've hit your usage limit`, `usage_limit_exceeded`, `usageLimitExceeded`, `usage limit reached`). A gateway's generic 429, `rate limit` or `quota` stays in its own class | None, however many in a row: nothing on the computer is broken. Unavailable until the reset (below) |
| `upstream` | Explicit upstream account-pool exhaustion (`no eligible upstream`, `no other eligible account`, `upstream attempt budget`, #13372), matched before `model_route` even when the message also names the requested model | None, however many in a row: nothing on the computer is broken |
| `model_route` | `Bare Claude model name`, `cannot be routed`, `no account match`, model does not exist / not supported | Repair Agent (reconfigure the gateway's model) |
| `auth` | 401 / 403, `invalid … api key`, `authentication_error`, `Not logged in`, expired token; a start refused with `codex_not_logged_in` (by its code) | Repair Agent (reconfigure the gateway's key) |
| `cli_outdated` | `unknown option`, a JSON-RPC `Method not found` (a CLI older than a method its runtime calls), asks to upgrade / version too old / no longer supported | First the node upgrades itself (`claude update`); if that fails, a repair Agent |
| `timeout` | `context deadline exceeded`, no answer within 150 seconds (twice: a timed-out check is asked again at once) | Counts only after two in a row; handed to a repair Agent |
| `other` | Everything else | Same: repaired only after two in a row |
| `not_in_terminal` | A start's new terminal tab could not run the launcher or its claude (`launcher_missing` / `claude_missing` with `where: "terminal"`, #1642, `docs/integrations/launchers.md` "The terminal's shell is another environment") | None: `blocked` at once, the reason says to define the launcher where the terminal's shell reads it or add it as a model provider |

## States

`ok` → on failure `failing` → while repairing `repairing` → back to `ok` when the retest after repair passes, `blocked` if the retest still fails. At any point, one passing check returns it to `ok` and clears the block.
Except `not_in_terminal`: the check runs in the node's login shell, where the launcher works, so neither the check right after the failed start nor the periodic rounds run for it, and a check asked for by hand does not clear it.
It clears when a start with that launcher on that computer works (any of the starts above: the user retrying from the App counts), or once it is older than 6 hours like any failure. It does not count against the main chat's readiness (`docs/assistant/onboarding.md`), which runs in the login shell.
`failing` / `repairing` / `blocked` all count as **unavailable** (when the latest result is within 6 hours).

The apps' **Test availability** (web: **Test**, `POST /api/v1/launchers/{name}/check?machine=`) runs the same `launchers.check` on the computer it names, and a pass counts as a passing check there (#1838):
a record of that launcher failing on that computer returns to `ok`, with the periodic checks off too, so the next start stays on the computer the user picked and a fixed task can be retried.
A failed Test is not recorded (the checks and the starts judge failures, and testing a launcher still being set up must not move starts off it), a passing one of a launcher with no record keeps none, and `not_in_terminal` still waits for a start.
The main chat's **Test** (`GET /api/v1/assistant/readiness?probe=1`, `docs/assistant/onboarding.md`) runs the same check on the main chat's computer and launcher, and updates that launcher's record there the same way (#10369): a pass clears a failing record, so the setup card goes at once; a failure updates the check time of a record still in force, so the record says the launcher fails now (a single timeout does not, as in the rounds). Without a failing record it keeps nothing.
Saving a Persona-managed provider with a new key, base URL, auth, model, headers or environment drops its failure records on every computer: it counts as never checked there until the next check or start.
A `not_in_terminal` record and a usage limit before its known reset stay; a change that touches none of these (its display name) keeps every record.

A **usage limit** (`usage_limit`) is `failing`, never `repairing` or `blocked`: the request got through to the subscription, so a block left by an earlier error is lifted and no repair round takes it. An **upstream pool outage** (`upstream`, #13372) likewise never starts a repair round (`repairNow` is false), since nothing on the computer is broken.
Its reset (`resets_at`) is read off the words when they name a time zone (`resets 4:20pm (Area/City)`), or comes from the Agent's own record of the limit (source `session`); Codex's "try again at 3:05 PM" names none.
With a reset, the launcher is unavailable until then unless a passing check proves quota recovered earlier (not bounded by the 6 hours; a reset more than 8 days off is taken as misread, as if there were none), and available again from that moment. A successful `session.start` alone does not prove the subscription can answer a model request and does not clear this record.
The periodic rounds spend no check on it before the reset (it would be refused too), nor does a forced check of all launchers; the first round from a minute after the reset checks it whatever the interval, and a passing check clears it.
A check asked for by hand of that launcher (`POST /api/v1/launchers/health/check?machine=&launcher=`, or the apps' Test availability) still runs.
Without a reset it follows the usual rules: unavailable for 6 hours (an Agent's turn: 6 hours from the turn), checked at the interval; a family of it whose reset is known holds until that reset ("Usage-limit record rules"). Until the reset it counts in the main chat's readiness as a rate limit that passes by itself (`rate_limited`), and not at all after it.

- `agents_list`: gets an extra "unavailable launchers" section at the end, one line each: computer / launcher / state / category / error; a usage limit adds `resets_at=<RFC 3339>, in …`.
- The launcher list for creating an Agent and the main chat's menu (`GET /api/v1/machines/{mid}/launchers`) leave out a launcher whose latest check failed, and carry `health` (`{state, error, kind, checked_at, resets_at?}`) on the ones they keep — a `not_in_terminal` entry stays and is grayed out ("In the App" below).
- `agent_start` and explicitly flexible `task_plan` tasks: when the launcher to use is unavailable on this computer, it is rechecked on the spot first (see "On-demand recheck before a reroute"); a passing probe keeps the start on the asked computer, otherwise it switches to another online computer where this launcher is healthy (a directory under the original computer's home becomes the new computer's home),
  and the "reason" in the answer / card says it switched and why (for a usage limit with a reset: `is at its subscription usage limit on <machine> (resets_at=…, in …)`); if no healthy one is found, start as originally planned and warn in the answer that the latest check failed. A task plan switches once when the card is produced and checks again when it actually starts.
  Flexible task plans verify the mapped directory with `fs.stat` on each candidate before changing the task, including during preview and retry. Only a confirmed directory qualifies; a missing path, file, unreadable response or unreachable node is skipped while other candidates are considered. If none qualifies after a failed start, the task keeps its original machine, directory and error; it does not retry that failure on the same machine. A passing follow-up probe clears health but does not by itself retry a failed start.
  Healthy first: last check passed > never checked; never checked is not treated as broken.
- `task_plan` defaults to `machine_policy: "fixed"`, also for saved plans without the field. Preview, start and retry retain that machine and its directory. A known unusable launcher fails on that target without starting elsewhere, after the same on-the-spot probe a reroute runs (a passing probe starts it there); normal health recovery (a passing check, including Test availability on that computer) permits a later retry there. A terminal-shell `not_in_terminal` refusal can only be cleared by a real terminal start, so an explicit task retry rechecks that shell on the same computer; it still runs the normal native authentication and startup gates. Set `machine_policy: "flexible"` only for portable work that may move. A card edit that changes the machine pins the new target unless it explicitly chooses flexible placement.
- The tool answers `Queued` while asynchronous dispatch is pending. This is not proof of an Agent start: errors appear on the same task and its failure receipt. A missing or foreign session in a start response is rejected; a `starting` session is not recorded as a successful launcher check.

## A launcher the computer doesn't have (#2640)

Model configuration › Agents (`agent.new`) is the one launcher purpose with no computer, and a model profile's tiers have none either, so their launcher can be a ~/.zshrc function or a wrapper script that only some computers have.
Health records can't catch that in time: the periodic checks are off by default, so the first sign used to be a failed start (`zsh:1: command not found: <launcher>`), and from then on a fixed task was refused there as unhealthy.
Every new Agent is now held to what its target computer can start before anything goes out (`launcher_fit.go`, `fitLauncher`): `task_plan`'s card and its dispatch (`startTaskAgent`, on the computer a flexible task lands on), `agent_start`, the App's New Agent (`agents.start_params`) and a message resuming an Agent started outside Persona (`agents.resume_params`).

- **What the computer has**: `machineLaunchers` (its own list without the hidden ones, plus Persona-managed ones), the launchers `hub.json` names for it, plain `claude` and every managed launcher (both run anywhere). The same answer backs `launcherAvailable`, which keeps the main chat off a launcher that is gone.
- **An implicit default it lacks** (the caller named no launcher or tier; only the `agent.new` binding or a profile gave the launcher): the start runs on `claude` when the computer has Claude Code (its node reported a version, or its list names `claude`), otherwise on the first launcher in its list that takes a prompt (not a runtime's), otherwise on `claude` anyway.
  The default's model and effort are dropped (the new launcher runs its own default model; an effort the caller gave stays). An explicitly chosen model is preserved instead: the fallback starts that model rather than the replacement's default.
  The task's `why` says so in the user's language ("<launcher> isn't on <computer>, so this runs on claude with its default model"; with an explicit model, it says the chosen model is preserved), and the plan card names the launcher that will run: the plan is changed when the card is made, or, when the computer's list couldn't be read then, at dispatch (the card updates).
  `agent_start` puts the same line on its confirmation and in its answer (`Note: …`).
- **An explicit tier whose launcher the computer lacks** (#12032): a tier the caller named is a caller choice, the same as naming the launcher itself, so it is refused rather than swapped — no terminal or process starts, and the tier's model, effort, model fingerprint and speed are preserved on the card instead of being cleared. The same applies to a tier a start-defaults policy selected for that start. The refusal reuses `launcher_not_on_machine` and names the launchers the computer has: `task_plan` fails that task, `agent_start` starts nothing (before the user is asked to confirm), New Agent gets a 400, and a plan whose launcher disappears between the card and `session.start` fails that task at dispatch. When the tier's launcher *is* present, its full preset (launcher, model, effort, speed) reaches the start unchanged. A tier supplied alongside an explicitly named launcher keeps the old precedence: the named launcher wins and the tier contributes nothing.
- **A launcher the caller named** that the computer lacks: refused before any tab opens, naming the ones the computer has: `task_plan` fails that task, `agent_start` starts nothing (before the user is asked to confirm), New Agent gets a 400. A plan card moved to such a computer fails that task at dispatch the same way.
- **A list that can't be read** (computer offline, a 10-second timeout, an older node) counts as having the launcher, as `launcherAvailable` always has: nothing is refused or swapped on a guess.
- **A computer whose start goes to Paseo** (#8140, `docs/integrations/paseo.md` "A launcher Paseo does not have"): Paseo runs only its own providers, so what the computer has is Paseo's list for that host (the node's `launchers.list {host}` answers `host_runtime`), without the managed launchers and plain `claude`, and the rule above changes for it: a launcher the caller named or a default gave that Paseo does not have runs on Paseo's provider of the same kind of agent (its built-in one of that kind first, else the first), the task's `why`, the card, `agent_start`'s confirmation and answer and New Agent's `launcher_note` saying so; the model goes with it only when that provider offers it. With none of that kind it is refused, naming Paseo's providers. What Paseo itself ran in place of what was asked (the session's answer's `instead`: a stand-in, its default model, a lower effort) is said the same way afterwards, and the start's health is recorded under the launcher that ran. Paseo's own refusal (`launcher_kind_missing`) is the request's: it does not count against the launcher.
- The swap comes before the fixed-policy health check, which then concerns the launcher that will actually run; a flexible task's move to another computer (above) is unchanged and the check runs where it lands.

## Automatic repair

- Only launchers covered by the periodic check are eligible for automatic repair. The latest failure must still be in effect. Other listed runtime launchers keep their records for inspection and can be checked with **Test again**.
- One repair round per computer at a time (several launchers on one computer usually share one cause: the gateway config is broken and every launcher returns 400); at most two repairs per round, then it is recorded as `blocked` and waits for a person.
  Blocked launchers are still retested at the normal interval and recover on their own when they pass.
- **Upgrade the CLI**: the node's `launchers.repair {name, action: "upgrade_cli"}` does only a fixed thing: for Claude launchers it runs `claude update` (login shell, 5-minute timeout);
  for anything else it answers "not supported". It does not accept arbitrary commands. Old nodes lack this method, which counts as "cannot upgrade".
- **Repair Agent**: when `agents.launcher_health.repair` in `manager.json` sets `skill`, it starts an Agent with a healthy launcher on `machine` (default: the computer the assistant runs on) in `cwd` (default home).
  The first message (`server/internal/prompts/agents/launcher_repair.md`) says which launchers on which computer are broken, the raw error and category, which skill to use for the repair, and to self-test with `claude -p` afterwards.
  It hangs off a **quiet to-do** (`Quiet`, no source chat): the hub sends no pushes for it, but it is visible on the Agents page and in to-dos. The link keeps the kind reported by the repair Agent's own start answer, or stays empty if the answer omits it. When it stops (neither working nor waiting for you) or after 45 minutes,
  the manager retests this round's launchers: pass means `ok`, fail means `blocked` (the reason includes the repair Agent's last message). Without `skill`, upgrading the CLI is the only repair; everything else goes straight to `blocked`.
  The skill name and the computer used for repair are configuration; the code names no specific tool or gateway.

The configuration, in the manager's `manager.json`:

```json
"agents": {
  "launcher_health": {
    "enabled": true,
    "interval_minutes": 30,
    "repair": { "skill": "<skill that repairs launchers>", "machine": "<machine id>", "cwd": "~", "launcher": "", "permission": "" }
  }
}
```

`enabled` missing or `false`: no periodic checks, no automatic repair (state can still be read; failed `agent_start` calls are still recorded, rechecked once, and routed around).
`permission` is the repair Agent's permission mode; missing means `bypassPermissions` (decided 2026-11-05: it has to run ssh to change config, and any other mode stops at an approval card waiting for a person);
set `default` if you want it to wait for your approval at every step. Member hubs in a mesh do not check (computers belong to the primary hub).

## API for the board

- `GET /api/v1/launchers/health?state=blocked&machine=`: all records (or filtered by state or computer), `{records: [{machine, launcher, state, kind, error, checked_at, source, fails, missing_fails?, repair?, blocked_reason?, resets_at?, limit_models?, limit_resets?}]}`.
- `POST /api/v1/launchers/health/check?machine=&launcher=`: check right now (without parameters, all online computers), returns this run's results. With no record in the answer for the launcher it asked about (the computer is offline, or no longer lists it), the web alert sheet reads `GET /api/v1/launchers/health` again, so it shows what the hub holds now.
- A state change broadcasts a `launcher_health` event (`{type, record}`, only to connected clients, not a push).
  The "decisions / blocked / progress" board reads `state=blocked` records, or subscribes to this event, to list what is blocked.

## In the App

Decided with the user (2026-11-11): the two model lists offer only what works now, so a launcher whose latest check failed is not listed at all (#4437). Earlier (#1643) the clients grayed out a `blocked` launcher with the reason and kept it selectable.
Decided with the owner (2026-11-24, #14535): New Agent still does not offer such a launcher, but no longer leaves it missing. It comes in the answer's `unavailable` (code `launcher_unhealthy`) with a reason in the request's language: the failure's class on that computer (key or sign-in rejected, gateway can't use the model, CLI too old, no answer, usage limit, no gateway account free, first-run setup, else failed its last check) and the record's line, clipped to one short line. The clients already draw `unavailable` after the launchers, grayed with its reason and How to fix (the provider's page, whose Test availability clears the record), and never pick it, so a person can tell a failing launcher from one the computer does not have. The main chat's list is unchanged.

- **New Agent** (iOS / Mac / visionOS `LauncherPicker` in `NewChatView`, web `NewAgent.tsx`) and the main chat's launcher menu both read `GET /api/v1/machines/{mid}/launchers`, which now leaves out a launcher whose latest check failed on that computer
  (a `failing`, `repairing` or `blocked` record within 6 hours, a usage limit until its reset) — the same records that route starts elsewhere. `failing` and `repairing` used to be offered; they are hidden too now.
  For New Agent (`purpose` empty or `agent.new`) the launchers left out come back in `unavailable` with why (#14535, above), never in both lists; the presets that name them stay out.
  A launcher that was never checked stays: no result is not a failure, and the periodic checks are off unless the manager turns them on.
- It comes back with a passing check on that computer (a periodic round when those are on, or **Test availability**), or when the record stops counting: the failure is older than 6 hours, or a usage limit's reset has passed.
  A successful start does not clear these: `recordStartSuccess` only clears `not_in_terminal`.
- `not_in_terminal` is the one failure that stays listed (dimmed, with its line, and **Try Anyway** as before): its checks pass — the node's login shell runs the launcher — and only a real start in the terminal clears it, so hiding it would leave no way to make that start.
- The launcher the running configuration names with no pick (the `default` of the purpose the form follows) stays listed too, with its record: the next Start uses it whether or not it is offered, so the form must be able to say why it may fail — the clients mark the default row and show its line.
- Because the list no longer carries the other failing entries, a client's gray-out with the reason, its **Try Anyway** and the help panel now appear for `not_in_terminal`, for that default, or with an older hub that still sends `health` on a blocked launcher (`LauncherBlockedHelp`, `NewAgent.tsx`). The list itself (iOS / Mac `AppModel.launchersForStart`, the web per dialog) is read fresh each time it opens, so a change behind any cache shows at once.
- The same route feeds **Run Agent here** and the web's hand-off of a to-do to a new agent, so they follow the same rule; the `not_in_terminal` exception is what keeps a launcher those paths must be able to start.
- A model configuration that names a hidden launcher is otherwise left alone: an explicit configuration is never silently changed.
- **Add Provider** in the help is offered only for a computer in the hub group the provider wizard saves to (the hub-level one); on another group's computer the words still say to add a provider there.
- The assistant's launcher menus (headless `claude -p` in the login shell) are unchanged: `not_in_terminal` does not apply there, and the main chat's readiness covers its model.
- A failed start whose session has `start_error_code` (#1642) shows the hub's whole sentence under "Didn't Start" in the chat (iOS `StartingNote`, web chat page) instead of clipping it after three lines; without a code it is shown as before.

## Capacity-aware notes

The health record is per machine; the account-pool gateways know a different, fleet-wide fact: how many subscription accounts of each upstream
provider are schedulable right now and when the nearest one recovers (`docs/server/usage-windows.md` "Account-pool gateways"). The manager derives a
read-only `proto.CapacitySnapshot` (`devices.CapacitySnapshot`, from the gateway inventory and the merged usage windows; no gateway or machine is
asked for it) and uses it only on the detour path (`healthDetour`, the chokepoint `agent_start` and task plans share):

- A provider whose accounts are all unavailable with **fresh** data is *saturated* (`ProviderCapacity.Saturated`); the detour line then adds
  "no schedulable `<provider>` account in the pool right now" and, when known, "the nearest one recovers …". Stale data or any schedulable account
  says nothing, and no pool configured means the original wording, unchanged.
- The launcher's provider is its health record's `agent`, falling back to a Persona-managed launcher's `agent`; an unknown provider gets no note.
- Capacity never reorders machines or blocks a start: one launcher maps to one provider, so every machine offering it sees the same
  provider-level capacity, and two different providers are not equivalent (an explicit model configuration is never silently changed,
  `docs/assistant/model-config.md`). Per-machine gateway attribution and the App/Lark entries are later work.
- **Choosing among a tier's candidates** (#14701) is where quota does reorder: only among the candidates the user listed on one Agent preset,
  in their order. The pace-based headroom there reads the merged usage accounts (`Devices.UsageAccounts`, the cache this snapshot uses) and
  maps a launcher to a provider the same way, or through its admission group's `quota_pool`; a candidate whose launcher this computer lacks or
  whose health check failed is passed over before any score (`docs/assistant/model-profiles.md` "Choosing a candidate"). A continuation keeps
  its previous candidate while it is healthy; three failed starts in a row on it re-pick (`docs/assistant/model-profiles.md` "Continuations keep the candidate").

## Execution-agent admission gate

Every execution-Agent start goes through manager placement (#13355). Task plans and `agent_start` enforce admission.
Other starts, including dispatch, successors, repair, reviewers, New Agent and resumes, report the applicable lines and
bind to the requested computer. They take no admission claim. Assistant conversations use `assistant.send`.

Available memory (total minus used), load1 and core count come from the retained node sample. The concurrency line
counts working sessions in the latest fleet snapshot on every platform, including a snapshot with zero working sessions.
Only when the manager has no session snapshot does the node's optional `claude_procs` reading stand in.
In-flight claims count toward concurrency. Defaults in `assistant.admission` are 2.5 GiB free on the hub computer,
2 GiB elsewhere, 20 working sessions, load1 at most four times the core count and eight running Agents per direction.
A direction override takes precedence over a project override. A retained computer with missing or stale metrics
(older than 30 seconds), unknown cores or ambiguous memory fails admission. Missing optional load or process readings
skip their respective lines. A computer outside the resource layer skips machine checks.

The swap line (#16757) refuses a computer whose swap is at least `max_swap_percent` used (80 by default, settable for
every computer and in `machines.<id>`; a value outside 0–100 keeps the default) while less than a quarter of its memory
is available. Past physical memory a computer falls off a cliff rather than slowing down evenly: each new Agent's
builds swap, and load1 only crosses its line long after. The memory condition keeps a computer whose swap holds stale
pages but whose memory is free admitting, which is common on long-running Linux machines; on macOS, where swap grows
in files as it is needed, available memory is usually under a quarter anyway and the swap reading decides. A sample
with no swap (none configured, or not readable) or a swap reading out of range skips the line; it never refuses. The
load multiplier stays four for every computer: memory is the swap line's job, and a load line scaled by memory size
would also hold back CPU-light Agents on a small computer that has room. A computer that needs a tighter load line
sets `machines.<id>.load_per_core`.

`persona_settings` changes `assistant.admission` behind an approval card. The card lists each line that changes, one per
row, with the old value and the new one in words: what the number bounds, on which computer, direction, project or
launcher group, and memory in binary units. A value the setting does not carry is shown as the line it falls back to,
marked as the default, the line for every computer or the hub computer's line (`admissionChangeCard`,
`server/internal/assistant/admission_card.go`). A project the setting does not name shows the cap for every direction;
a direction does too unless the setting carries project caps, where its fallback depends on the task and the row says
it has no line of its own. Lines are compared by their stored value, so two memory sizes that round to the same text
both appear with their byte counts, also when one side is the line the value falls back to, and two launcher lists
that read the same appear as stored, quoted, white space included. Names are
written on one line, so a line break inside one cannot start a row. The list is not clipped, and rows are in a fixed
order because the card is built again when it is answered and must match what was shown. The stored JSON stays under
the card's "Show input".

A refused `machine_policy=fixed` task stays put and gets the reason plus the least-loaded reachable alternative. A `flexible` task is moved to that
alternative when its launcher runs there and its directory exists (the same `fs.stat` and reroute-trail rules as the launcher detour); no alternative means refusal. The
direction cap refuses either policy, since moving machines does not free a direction slot. The check and the slot reservation happen in one critical section
(`claimIfAllowed`), so concurrent admits cannot both take the last slot; a task's claim is held from the decision until its agent is linked into the todo or the start
fails for good. The `agent_start` tool holds its claim across the start attempt.

The manager keeps each decision in `agent_placements`, including the requested machine, candidates, filter keys and
readings, scores used to pick an alternative, choice and built-in policy version. A queued attempt has no bound machine.
Task and session IDs link the decision to the work, including starts with no plan. The plan also carries its current
decision for the card and receipt. Directory and leader-trust checks on alternatives apply when a plan can move.

A plan task the gate refuses is not failed: it waits in the admission queue (`server/internal/assistant/task_queue.go`), its card reads waiting
with the reason, and the hub retries it every 90 seconds. The queue record is stored in the plan, so a hub restart resumes the wait. After 10
refusals or 30 minutes the start fails with the last reason; a refused `agent_start` tool call is answered at once, since a tool call cannot wait.

**Launcher groups** (#9674) cap tasks across launchers that draw on one shared resource, such as one subscription account pool behind several
Claude Code compatible launchers, while other launchers stay free. Off by default: without `launcher_groups` no launcher line applies. Each
group names its launchers exactly (an agent kind cannot tell a pooled launcher from a compatible one that is not) and one cap; a launcher
belongs to at most one group:

```json
{"launcher_groups": [{"name": "shared-pool", "launchers": ["launcher-a", "launcher-b"], "cap": 4}]}
```

- **What counts**: open tasks of any direction, on every computer, whose linked execution Agent is working a turn on one of the group's
  launchers (the session's launcher in the fleet's latest snapshot), plus starts admitted and not linked yet. A task counts once however many
  agents it links, and a parked task, one waiting for the owner or one held short of live is left out, as for the direction cap. Tasks on
  other launchers, the assistant's own conversations and Agents started directly with `agent_start` are not counted.
- **When full**: a plan task on a group launcher waits in the admission queue with "Launcher group … already has N tasks running, at its limit
  of M", with no attempt or time limit, and starts on a later retry once a task of the group stops working. The group is checked before the
  machine lines, so a flexible task is not moved and no other computer is recommended. If another line refuses it later, that line's 10
  attempts and 30 minutes count from then. `agent_start` on a group launcher is refused at once with the same reason.
- **Change it** like the other lines: `persona_settings` reads and sets `assistant.admission` with the phone approval card; the next start
  uses the new groups.

### Wait for a gateway's quota recovery

A launcher group can also opt in to a bounded wait before the first background launch of a plan task (#2409).
Set `quota_pool` on that group through the existing `assistant.admission` setting and its approval card.

```json
{
  "launcher_groups": [
    {
      "name": "shared-pool",
      "launchers": ["launcher-a", "launcher-b"],
      "cap": 4,
      "quota_pool": {"source": "gateway-source-id", "provider": "claude"}
    }
  ]
}
```

Use the ID of the configured relay usage source and its provider, `claude` or `codex`. Use this binding only when the
listed launchers use the same account pool that the usage source can observe across every computer where they run. Persona confirms the selected launcher and
provider on the computer before queueing. Removing the binding or switching off admission removes the reason to wait.
A group with no binding only enforces its existing concurrency cap.

The gate uses the latest cached account inventory for that source and provider. It needs a complete read, source and
enabled account observations no more than ten minutes old, and a known future recovery for every account that can
take work. It ignores disabled accounts and model-only windows. When an account has several active quota restrictions,
its latest recovery controls. The pool's estimate is the first account's recovery. Incomplete or stale evidence,
a working account, and a recovery beyond the wait budget leave the start to ordinary admission.

The task card shows the expected recovery while it waits without occupying an admission slot. The manager checks
again after at most 90 seconds or at the estimated recovery, whichever is sooner, on its existing minute sweep.
The 30-minute budget is recorded with the task plan and survives a restart. At the deadline, or sooner if the
evidence no longer warrants waiting, the manager releases the task to its ordinary start gate. A resource or
concurrency refusal can still queue it there. It retains the launch claim rules for later retries. Direct Agent starts, explicit task retries and tasks reusing an existing to-do use their ordinary start paths.

When the manager releases a task, it checks the saved to-do while committing a launch claim and again just before sending the start to the computer. A completed,
cancelled, or already linked task leaves the queue. If a restart or disconnect leaves the launch outcome unknown, the card asks the
owner to check the computer and Persona does not replay the launch automatically.

## Not done

- An Agent that starts but fails on its first turn (the gateway rejecting the model name; `session.start` succeeded) is not recorded from the session; periodic checks find it. Only a usage limit with a known reset is recorded from the session.
- Android does not read `health` yet.
- **New Agent** in the App (`agents.start_params`, on a computer the user picked) does not switch computers; Agents started by the Lark bot are not wired in yet.
