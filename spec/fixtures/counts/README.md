# Counts: the surface table and the agreement report

Every place that shows a number, which count it draws, and whether each client's replay of a count's fixture agrees
with it. This is step 4 of `docs/plans/plan-derived-counts.md` (section 3.5, "The agreement check"; #11900). Nothing
here changes a number anyone sees: the table records today's rules, and the report compares what the replays already
count (`docs/design/PRINCIPLES.md`, "Consistency first" and "Rules are checkable").

## The surface table

`surfaces.json` is `{"version": 1, "counts": {...}, "replays": {...}, "surfaces": [...]}`.

- `counts`: each count id with its `rule` (one sentence, or `null` while a decision has yet to give it one), the fixture
  that pins it (`pinned_by`, or `null`), the function in the manager that writes it into the counts view (`producer`,
  `{file, symbol}`, or `null` while the view does not carry it; step 7a, D18) and the decisions it waits on
  (`waits_on`). The ids are the plan's (section 3.1): `agents_needs_you` (pinned by `spec/fixtures/needs-you`),
  `inbox_unread`, `inbox_action`, `todos_needs_you` (pinned by `spec/fixtures/todo-counts`) and `persona_cards`
  (pinned by `spec/fixtures/persona-cards` under D9/D11). A count refined after step 4 lists the decision in
  `refined_by` (D18). `agents_needs_you` names D11, which excludes sessions whose only cards are held.
- `surfaces`: one row per place a count is drawn on one client, from the plan's section 2 (the inventory, read at
  e4308ecc23) and checked against the code this table names. A row's id is `<client>.<surface>`.

| Field | Meaning |
| --- | --- |
| `client` | Whose code computes the number: `ios` (the Apple apps, the watch and the widgets included), `android`, `web`, `desktop`, `server` (the manager or the hub, drawn as sent) or `harmony` |
| `surface` | The place, the same name on every client that has it (`agents_tab`, `agents_section`, `switcher`, …) |
| `label` | What people read the number as. `matches` rows with one label draw one count |
| `shown_on` | Where it is drawn |
| `count` | The count ids it draws, added up (the app icon draws three) |
| `local` | A part the client still counts by today's rule, apart from the view (the switchers' Persona conversations until D9: the plan's section 3.3, item 7) |
| `scope` | Whose things it counts: `assistant` (the current one), `unscoped` (every assistant's), `default_manager`, `manager` (each manager its own), `each_assistant` (one number per assistant), `all_hubs` (every paired hub) or `list` (what the list holds) |
| `code` | The functions that compute it, `{file, symbol}`; a dotted symbol is a member of a type |
| `tests` | The tests that hold it, `{file, symbol}`; empty where none does today |
| `status` | `matches`: drawing the count from the manager's view would show today's number on every hub. `differs`: it would not |
| `decisions` | For `differs`, the decisions of the plan's section 6 that settle it, the first the one that changes its number. A row that draws another row's number (the app icon adds up the tabs' badges) names that row's decisions too, which only review can check; a lane's mark in a replayed case names its decision on that lane's row, which the guard checks |
| `why` | In a sentence, what the client counts that the count does not, or why it matches |
| `view` | Only once the client draws the view (the plan's steps 8 to 10): `{"fallback": {file, symbol}}`, the frozen fallback that today's rule moves into |

`differs` rows today: 27 (26 recorded at step 4, and the web's assistant switcher since D11). They may only go down: a
row that comes to match leaves the list, and the guard refuses a new one, except in the change that records a decided
refinement of a count the row draws (`refined_by`), where a row may come to differ naming it. Of the 32 rows, the 5 that
match are the roster's Inbox part, the Inbox tab on iOS, Android and the web, and the manager's To-dos counts
(`GET /api/v1/todos`). When D11 was recorded, every row that draws `agents_needs_you` counted a session whose only cards
are held, so each names D11 (the web's switcher, which matched before, by it alone).

- `replays`: for each fixture that clients replay with the report switch, the `lanes` that report on it and, per
  surface, the keys of a case's `expected` that hold its number (`value`; a list counts its rows) and its rows (`keys`).

## The guard

`scripts/test/test_counts_surfaces.py` holds the table to the code and to the plan:

- the shape: known clients, scopes and count ids; a `differs` row names decisions the plan's section 6 lists, a
  `matches` row none; a row that draws a count with no rule differs, naming the decision it waits on;
- every `code` symbol is declared in its file (each part of a dotted one, or in Go a method of the type before the
  dot), and every `tests` file names its symbol;
- every count the counts view carries (the fields of `counts` and `totals` in `schema/wire/counts.schema.json`) names
  its `producer`, a function declared in a Go file of an adapter package in `server/`, and no other count names one; a
  field the schema has that `counts` lacks fails too. The producer's own package test checks that it writes exactly
  the counts that name it (`TestViewWritesTheCountsThatNameIt` in `server/internal/assistant/fleetcount/fleetcountadapt`);
- `matches` rows with the same label draw the same count;
- each replay's surfaces are rows of every lane that reports them, read from keys every case's `expected` has;
- a lane's mark in a replayed case (`known_differences.<lane>`) that changes a key a surface reads makes that lane's
  row differ and name the mark's decision;
- a replay's surfaces that one lane draws from the same count show one number in every case's `expected` (principle 4):
  a needs-you case gives `agents_tab` and `agents_section` the same number, the badge and the header (D6), while a
  mark keeps today's split;
- the `differs` rows only go down: the count above is the table's, and against the base commit (`CI_BASE_SHA`, else
  the merge base with `origin/main`) no row differs that did not, but a row that names a decision this change adds to
  the `refined_by` of a count it draws; once the base lists the refinement, no further row comes to differ by it;
- a row with `view` names its fallback symbol in no source file of its client but the fallback's own (tests aside);
- check-fast runs the guard for this directory, the plan, the view's schema, and every file the table names, producers
  included (`scripts/ci/check_fast_map.py` reads the table for them, so a new row needs no edit there).

Its unit tests run it on the table, then break each rule in a small tree of their own and check that it is caught.
Run: `python3 -B scripts/test/test_counts_surfaces.py`.

## Lane reports

Each client's replay of `spec/fixtures/needs-you` has a report switch. Off (the default), the replay runs as before.
On, it also writes one JSON line per case and surface to `<directory>/<lane>.jsonl`, before its assertions:

```json
{"lane": "android", "fixture": "needs-you", "case": "cards_of_one_session_are_one_row", "surface": "agents_tab", "value": 1, "keys": [{"id": "agent:studio/a", "cards": ["ix-1"]}]}
```

`value` is the number the surface shows and `keys` its rows, in order, as the table's `replays` names them
(`agents_tab`: the badge and its rows; `agents_section`: the number on the section's header and Needs you filter, and
the section's rows). The directory is
`PERSONA_COUNTS_REPORT`, absolute or relative to the repository root:

- Android: `cd android && PERSONA_COUNTS_REPORT=<directory> ./gradlew :core:domain:test --tests '*AgentsNeedsYouFixtureTest*' --max-workers=4`.
  The build passes it to the test as `persona.counts.report`; a run with the switch on always runs and stores nothing
  in the build cache (`android/core/domain/build.gradle.kts`).
- iOS: `xcodebuild test … -only-testing:PersonaTests/AgentsNeedsYouFixtureTests` with
  `TEST_RUNNER_PERSONA_COUNTS_REPORT=<directory>` in its environment (xcodebuild hands it to the test as
  `PERSONA_COUNTS_REPORT`).

A replay reports what its client counts, marks included: Android's `a_directory_hidden_card_counts_nowhere` reports 2
(`spec/fixtures/needs-you/README.md`, "Known differences").

## The agreement report

`scripts/test/counts_agreement.py` reads the lane reports beside the fixture and the table's `replays`, and prints, per
case and surface, the fixture's number and each lane's:

- agrees: the lane's value and rows are the fixture's;
- known: they are what the lane's mark in the case says it counts today (`known_differences.<lane>`), with its decision;
- unexplained: anything else, or the fixture's value on a case the lane's mark says otherwise (a stale mark).

It then says, per lane, how many cases agree, which are known differences, and on which cases each two lanes differ.
A report that misses a case or surface its lane replays, names one the table does not, says one twice or cannot be
read fails too. A lane with no report is listed as not run; `--require-all` fails on it. Exit codes: 0 every value
agrees or is a known difference, 1 something is unexplained, 2 the reports could not be read.

```
python3 scripts/test/counts_agreement.py [--require-all] [--json <out.json>] <directory or lane report> ...
```

At step 4 both lanes report all 14 cases on both surfaces: iOS agrees on every one; Android agrees on 13 and differs on
`a_directory_hidden_card_counts_nowhere`, its known difference (D1), so the two lanes differ on that one case. With
D6 and D11 recorded (#12888) the fixture has 16 cases, and both lanes carry the same marks on three of them: D6 on the
header of `muted_and_quiet_agents_are_rows_but_not_on_the_badge` and `a_quiet_tasks_card_is_a_row_but_not_on_the_badge`,
D11 on `a_held_card_is_listed_but_counts_nowhere`. The two lanes still differ on the hidden case alone. Since the owner
confirmed D11 for routed terminal questions (2026-11-23) the fixture has 17 cases: iOS leaves a card its task's
supervisor holds out of every row (`a_routed_terminal_question_is_held`,
`a_supervisor_held_card_beside_an_open_one_leaves_the_row`), Android carries D11 marks on both, so the lanes differ on
those two cases as well.
`scripts/test/test_counts_agreement.py` holds the differ to these rules, and reads the real fixture's marks as known
differences.

## View samples

`view_samples.json` holds wire samples of the per-manager counts view (`schema/wire/counts.schema.json`, the contract
in `docs/protocol/counts.md`; step 6 of the plan, #15512): `{"version": 1, "cases": [...]}`, each case with a `name`, a
`description`, an `input` and `expected.view`, the view as the manager writes it.

- A case whose `input.needs_you` names a case of `spec/fixtures/needs-you` holds, as `counts.agents_needs_you`, the rows
  `fleetcount` decides for that case on the Agents list: the counted rows in `keys`, the muted and held ones in `held`,
  each reason as the adapter spells it, and a quiet task's agent in neither. `TestViewSamplesAreFleetcountsRows`
  (`server/internal/assistant/fleetcount/fleetcountadapt`) checks it, and that `totals` repeats `n`.
- A case whose `input.todo_counts` also names a case of `spec/fixtures/todo-counts` holds, as `counts.todos_needs_you`,
  that list's `needs_you` with no row listed (all of `n` is `more`), and the same test checks it; a case that names none
  is a view from a manager that has not counted its to-dos yet, without `todos_needs_you`.
- A case whose `input.persona_cards` names a case of `spec/fixtures/persona-cards` contains one `card:<id>` key per
  eligible Persona card, with the current manager's Agents rows beside it. The same test checks both counts and their
  totals. A current manager sends a zero when its Persona card tally is empty.
- A case without `input` shows a shape no fixture case gives: rows past the listed `keys` (`more`), and a manager
  build that does not work a count out (absent, never 0).

`scripts/test/test_wire_schema.py` holds every sample to the schema, and the generated types write each back unchanged
in Go (`server/internal/assistantwire/counts_gen_test.go`), Swift
(`apple/Packages/PersonaWire/Tests/PersonaWireTests/CountsWireTests.swift`), Kotlin
(`android/core/contracts/src/test/kotlin/persona/core/contracts/CountsWireTest.kt`) and the web
(`web/src/lib/counts-wire.test.ts`). A new count id adds samples here with its schema field.

## Kept views

`views.json` holds what a device keeps of the managers' counts views (D13 of the plan; `docs/protocol/counts.md`, "The
hub's relay"; #15893): `{"version": 1, "cases": [...]}`, each case with a `name`, a `description`, the views the device
keeps (`given.kept`), one transport event (`when`), and the views it keeps after it with their totals (`then`).

- `kept` lists `{hub, manager, view}`: one view per hub and manager, listed by hub, then manager. `hub` and `manager`
  are made-up ids (the same manager id on two paired hubs is two sources); `view` is a `CountsView`, which
  `scripts/test/test_wire_schema.py` holds to the schema like the samples above.
- `when.event` is one of the transport events a device sees:

| `event` | Fields | What the device saw |
| --- | --- | --- |
| `arrived` | `hub`, `manager`, `view` | A `counts` message with a view (`docs/protocol/protocol.md`, "Counts view"), live or as it connected |
| `arrivedEmpty` | `hub`, `manager` | A `counts` message with `view: null`: the manager's connection to the hub ended |
| `wentOffline` | `hub`, `manager` | The roster (`GET /api/v1/managers`) lists the manager offline |
| `restarted` | `hub` | The hub restarted, was redeployed or switched versions: the socket ended and a new `hello` came |
| `leftRoster` | `hub`, `manager` | The roster no longer lists the manager |
| `signedOut` | `hub` | The device signed out of the hub |

- `then.totals` is each count id's `n` added up over every view the device keeps after the event. A count no kept view
  carries is absent: unknown, never 0. Which managers a surface adds up, and whether it draws views at all while a
  manager in its scope is absent (D10), are the surface's (`surfaces.json`, `docs/protocol/counts.md`), not this file's.

The rules the cases hold: a view stays through `view: null`, going offline and a hub restart; a view with a new epoch
replaces the kept one whatever its rev; within an epoch a lower rev loses and a higher one replaces; the view goes when
the roster no longer lists its manager, and every view of a hub goes when the device signs out of it; a null view from
a manager the device never kept a view of keeps nothing, and a device with no view after a hub restart keeps none until
the managers connect again. D10's explicit absence (a hello without `counts_view`, a connected manager's entry
without the flag) sends a surface to its fallback; it is not a transport event here, and a client step whose reducer
also drops kept views on it adds the event and its cases.

Who replays it:

- each client's reducer from steps 8 to 10 of the plan, through its adapter (keep, replace, lose, drop as above), and
  none of them writes this file;
- the hub, for the restart cases (`TestTheHubReplaysTheRestartCases` in `server/internal/hub/counts_view_test.go`): a
  hub that just restarted sends a device that connects no `counts` message and still lists every kept manager in its
  roster, so each `restarted` case's `then` keeps its `given`; and when a manager connects again and tells its view,
  the device gets it as `the_managers_reconnect_after_a_hub_restart` says.

## CI and check-fast

CI's lanes already select these files: `spec/**` runs the Android and Apple lanes, whose replays carry the switch,
and the scripts lane (`**`) runs both suites. check-fast runs the guard and the differ's tests for this directory and
`spec/fixtures/needs-you`, and the guard for the plan and every file the table names (`scripts/ci/check_fast_map.py`);
for `views.json` it also runs the server packages that read the shared fixtures, the hub's among them.
Running the report in the scheduled main verification (the plan's section 3.5) is not part of step 4.

## Changing the table

- A client changes how it counts: update its row (`status`, `decisions`, `why`) in the same change; when it comes to
  match, remove `decisions` and lower the count above.
- A count is renamed or moved: update `code` or `tests`; the guard names any symbol it cannot find.
- A new place shows a count: add its row. It must match, or the guard refuses it as a new difference.
- A decision refines a count (the plan's D18): add it to the count's `refined_by`, change `rule` and the fixture that
  pins it, and, in the same change, add it to the `decisions` of every row that counts by the old rule, a row that
  matched turning `differs`.
- A client starts drawing the view (steps 8 to 10): add `view` with its fallback.
