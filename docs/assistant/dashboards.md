# Dashboards

Dashboards are pages the assistant keeps up to date for the owner and the owner comes back to: a morning-run streak, a
reading list, a weekly watch on prices. A conversation drafts one; the owner keeps it with one tap in the chat; it then
lives in the sidebar under **Dashboards**, and its buttons talk back to the conversation that keeps it. Issue #15017;
phase 1 is described first, phase 2 (pages a routine refreshes, with alerts: #13020) in "Phase 2: watch pages (#13020)".

## Who it serves

Everyday people first (the Assistant side): nothing to configure beyond one switch, no Agent, runtime or repository to
pick. The owner asks in plain words ("make me a page to track my runs"), sees a preview, taps **Keep in Dashboards**. The
page reads as one of Persona's own, drawn by the apps' design systems, with a fixed line under its header saying it is
kept up to date by Persona, which the model cannot change.

What a dashboard is not: a one-off answer or this conversation's progress (a live card, `assistant-views.md`), a to-do
or a request waiting for the owner (To-dos and Needs you: a page links to them and never copies their numbers), a
program (no HTML, scenes or images, no code, no network access).

## The switch

Settings › Assistant › **Let Persona make dashboards** (`dashboards` in `GET` / `PUT /api/v1/assistant`; absent or
false is off, and phase 1 ships off). Only a device with full access changes it, and only the owner: the assistant can
read it (`persona_settings` key `assistant.dashboards`, action `get` only) and never change it, so no phone card is ever
asked for it. The apps show the switch only while the hub lists the feature `assistant_pages`, which the built-in
manager announces in its hello whenever it keeps pages, the switch on or off (`docs/protocol/protocol.md`, "Health
check and feature flags").

Off: the `page` tool is listed to no conversation and refuses every call, the places list has no Dashboards row, and
`POST /api/v1/extensions/-pages/-/ui/…` answers 404. Pages already kept stay stored and come back when it is turned on.
On with no kept page, nothing on any screen changes.

## The flow

1. **Draft.** A conversation calls `page create` with a title, an icon, a one-line `why` and the page as a View
   (`server/internal/assistant/pages_tool.go`). The hub checks it against the page floor (`wire.ParseAgentPage`: what a
   live card may draw, at most UI version 10, no html, scene, image, badge or danger tone, no action id starting with
   `persona.`), the title rules and the icon list, stores it as a draft (`db.CreateAssistantPage`), and puts a preview
   card in the calling conversation through `view_show`'s path: the page read-only (buttons, forms and anything else to
   tap left out, its first three sections at most), the `why`, and three choices: **Keep in Dashboards**, **Keep and pin
   to the sidebar**, **Not now**. The tool answers `{id, version: 1, status: "draft"}`, or `{error: "exists", id}` when
   a kept page already has the title, so the Agent updates that one. The model is told to end its turn after create.
2. **Keep.** The owner's tap on a choice is an ordinary card action (`POST /api/v1/assistant/conversations/{id}/views/
   {key}/actions`). The hub applies it inside that request (`applyPageOffer` in `commitViewAction`), before storing the
   card's answer: Keep makes the draft a kept page (`db.KeepAssistantPage`, idempotent by the card), Keep and pin also
   pins it while fewer than three are pinned, then the apps are told (`assistant_pages`) and the sidebar's arrangement
   gets the Dashboards key, and the pinned page's (`navigation`, so the web does not push them under More) before the
   request is answered. Not now declines the draft. Only telling the conversation what the owner chose is asynchronous,
   through the card's outbox as for any card: the place appears even while that conversation's computer is offline and
   the card's answer waits for it. A choice that cannot be made (Dashboards off, 20 pages kept, a kept page with the
   same title) answers 409 with a sentence and leaves the card unanswered. A draft not kept within 7 days expires; its
   card then reads **Expired** and its choices are gone (a stale tap gets the card as it is now).
3. **Use.** The owner opens Dashboards (the index: Pinned, All, Recently removed) or a pinned page from the sidebar.
   A tap on one of the page's own buttons is received in its request (**Received**), then delivered to the conversation
   that keeps the page as the owner's message ending in `<persona-action view="-pages/pg_…" action="…">{"values":…}
   </persona-action>` (the tag the apps already hide), queued after the turn under way: never `ownerInterjects`, never
   `interject_background`. The footer follows it: **Delivered to Persona**, **Persona is working** when that turn
   starts, then nothing once the turn updated the page, or **Persona replied in the chat** with a link.
4. **Update.** The conversation answers with `page update`: the whole View again, with the version it read
   (`version_conflict` otherwise). The apps showing the page draw it again (`extension_ui` invalidate); nothing is pushed.

### Keep a live card as a dashboard

An owner can ask the conversation to keep a card it already showed. For example, after displaying a reading list,
the conversation can offer that list as a dashboard without generating the entries again:

```json
{
  "action": "create",
  "title": "Weekend reading",
  "why": "Come back to the books you picked for the weekend.",
  "from_view": {"id": "view_0123456789abcdef", "version": 2}
}
```

Use the live card ID and the version the owner saw. The manager reads that version from the calling conversation on
its machine and checks the contents against the dashboard rules. If the card has changed, inspect it again before
offering the updated content. An unsupported card produces an error before any draft is created.

The usual preview offers **Keep in Dashboards**. It contains a snapshot of the card's contents. Later edits to the
card and answers submitted to it stay in the conversation.

## Ownership and writers

| Page of | Who writes it | Where taps go |
| --- | --- | --- |
| `main` | Whatever conversation is the main one now: a new main conversation takes over the old one's pages | The main conversation now |
| `conversation` | The ordinary assistant conversation that drafted it, direction leaders included (its machine and public id, `logical_authority.go`) | That conversation; when it is gone, the main one, and the page says so |

Who calls is only the MCP route's machine and conversation, never an argument. The tool is listed only to a
conversation that may write (`guardTools`) and each call checks again. Refused, with a sentence saying why: Inbox
handling chats (`inboxReadOnlyTools`), conversations that write to other people (`outwardOrigins`: Inbox, Lark /agent,
an extension's, and any an extension's token opened), meeting guests, the background supervisor and its rounds
(`roundToolNames` does not list `page`), a scheduled routine's own conversation, conversations on work computers,
spaces (the Health space included), and every conversation of a manager other than the built-in one.

`page list` gives the pages the caller writes; the main conversation also reads the others' kept pages (read only), to
answer what the owner keeps. `page get` gives the page, its version and its last 10 taps. `page remove` needs a reason,
which the owner sees in Recently removed; removing a draft withdraws it (its card says so). Only the owner deletes a
page for good. A deleted conversation's pages go to Recently removed in the deletion's own transaction
(`db.RemoveConversationAssistantPagesTx` in `PutConversationDeletion`) and come back as the main conversation's when
restored; its drafts go unlisted.

Once kept, a page's title and icon are the owner's (the page's **Rename**); an Agent changes the contents only. After
the owner paused a page or restored an older version, writes are refused (`paused_by_owner`) and the model is told to
ask first. The owner can resume updates from the footer.

## Version history

Open **Version history** from a kept dashboard's footer to see when each retained version was written and who wrote it. Select an earlier version to inspect its title and content. Confirming a restore creates a new version and pauses updates. Use **Resume updates** when the conversation or routine should start writing again.

Restoring a version changes the title and page content. Readings, alerts, routine settings and sidebar placement keep their current values. If another device updates the page before confirmation, reopen history and review the new current version before restoring. A title already used by another kept dashboard must be freed before restoring that title.

## Titles and icons

1 to 40 characters on one line, without control, direction or invisible characters, and never passing for one of the
app's own places: the reserved names are the three apps' sidebar names in every language
(`server/internal/i18n/locales/*/assistant-pages.json`, kept in line by `scripts/test/test_dashboards_reserved_names.py`),
compared after NFKC and case folding. An Agent's title is refused when it has one in it: anywhere for a name in Chinese
or Japanese ("看收件箱"), as a whole word for a name in Latin letters ("Inbox (2)", "Inbox · work" and "週のToDo" are
refused; "Personal budget" and "Todoist export" are not). The owner's rename is refused only for a title that is a
reserved name. No two kept pages share a title. Icons come from `wire.AgentPageIcons`: the sidebar icons without
settings, security and the app's own places' icons.

## Limits

| Limit | Value |
| --- | --- |
| Kept pages | 20; Recently removed keeps 100, each 30 days |
| Drafts | 3 waiting per conversation, 10 in all, 5 drafted in any 24 hours (declined and expired drafts count) |
| Pinned | 3 |
| A page | 64 KB, 8 detail pages, 300 components a page |
| Writes | 60 an hour a page, 600 an hour for all pages together (both survive restarts); past them `rate_limited` |
| Taps | 10 a minute a page; the same tap within 30 seconds is the first |
| Tap records | 90 days, 200 a page |
| Versions | the latest 20 per page, including the current version |

## Events

`assistant_pages` (`{}`) when the places change: a page kept, pinned, unpinned, renamed, removed, restored or deleted,
the switch turned on or off. `extension_ui` `{extension: "-pages", id, invalidate: true}` when what a page (or the
index) draws changed: each page's at most once a second, the index's times at most once a minute, every `-pages` event
together within 150 in 15 minutes (`pages_events.go`), well inside what the hub's event log replays. Never a whole page
in an event, never a badge, never a push: neither drafts nor updates count in the card ledger (`assistant.card_ledger`).

## Measurement

Principle 7 (`docs/design/PRINCIPLES.md`): receipt, delivery and execution are three words on the page and three times
in the tap's row (`at`, `delivered_at`, `turn_started_at`, `turn_ended_at`). Once a day the manager logs the day's taps,
queue time (tap to delivered) and turn wait (delivered to the turn that took it up) as p50 / p95 / max, and the turns a
tap preempted, which must stay 0 (`pageTapDayLine`). The tests pin the rest: Keep works with the computer offline, a
tap is received within its request, a thousand writes stay within the event budget.

## Where it lives

| Part | Code |
| --- | --- |
| Storage, limits | `server/internal/db/assistant_pages.go`, `assistant_page_actions.go`, `assistant_page_stats.go` |
| Page floor, titles, icons | `sdk/go/wire/agentpage.go` |
| Places and pages over the extensions' routes | `server/internal/extmgr/pages_hook.go` (reserved id `-pages`, HTTP only) |
| Drawing, owner actions | `server/internal/assistant/pages_ui.go` |
| Taps, delivery, upkeep | `server/internal/assistant/pages_actions.go` |
| Events | `server/internal/assistant/pages_events.go` |
| The tool, Keep, callers | `server/internal/assistant/pages_tool.go`; prompt `server/internal/prompts/mcp/assistant/page.md` |
| Feature flag | `server/internal/hub/assistant_pages.go` |
| Phase 2: watch, readings, grants (storage) | `server/internal/db/assistant_page_watch.go`, `assistant_page_readings.go`, `assistant_page_grants.go` |
| Phase 2: the watch, the reading, the rule's trips | `sdk/go/wire/agentpagewatch.go` |
| Phase 2: drawing a watch, the footer, Refresh now | `server/internal/assistant/pages_watch_ui.go` |
| Phase 2: alerts | `server/internal/assistant/pages_alert.go` |
| Phase 2: Overview cards | `server/internal/assistant/pages_overview.go` |
| Phase 2: `page patch` | `server/internal/assistant/pages_patch.go` |
| Phase 2: the bound routine, its grant and run header | `server/internal/assistant/pages_routine.go`; prompt `server/internal/prompts/assistant/page_routine_run.md` |

## Phase 2: watch pages (#13020)

Phase 2 adds pages that check something for the owner on a schedule: "check X for me every day" becomes a dashboard
with a **watch** (one question, one of four displays, an optional alert rule) and one scheduled routine that refreshes
it. #13020 ("watch cards") is this phase; a watch card is a dashboard page bound to a routine, on the same storage, not
a separate card store. The contract below is what the hub, the mock hub and the three apps build against; the shared
cases are `spec/fixtures/dashboards/watch.json`.

Who it serves: everyday owners. The owner says "check the rent on Elm Street every morning and tell me when it drops
under 2,000"; Persona drafts the page and its daily check; the preview says what will be checked, how often, how it is
shown and when it would alert; the owner keeps it. Nothing to configure, no Agent to pick.

### The watch

A page has at most one watch, given with `page create` (or `page update`) by a conversation that writes the page, never
by a routine run:

| Field | Value |
| --- | --- |
| `question` | 1–200 characters on one line: what is checked, in the owner's words ("Rent for a 1-bed on Elm Street") |
| `display` | `number`, `status` (a status light), `table` or `trend` (a number with a small trend line); fixed once the page is kept (`watch_display_fixed`) |
| `unit` | `number` and `trend`: 0–8 characters ("USD", "kg") |
| `columns` | `table`: 1–4 column titles, each 1–40 characters |
| `alert` | optional rule, by display: `{"when": "above" \| "below", "value": <number>}` for `number` and `trend`; `{"when": "status", "value": "warning" \| "problem"}` for `status` (trips at that level or worse); `{"when": "rows", "value": 1–100}` for `table` (trips at that many rows or more) |

With a watch, `view` is optional: the page draws the watch first and then the view's own sections, if any. The watch
is stored on the page (`assistant_pages`); changing `question`, `unit`, `columns` or `alert` is a write like any other,
and changing `alert` turns the owner's alert off (they switched on the rule they read, not a new one).

### Readings and `page patch`

A **reading** is one answer to the question. `page patch` writes one, and nothing else of the page:

| Display | Reading |
| --- | --- |
| `number`, `trend` | `{"value": <finite number, abs ≤ 1e12>}` |
| `status` | `{"status": "ok" \| "warning" \| "problem", "text": "<0–80 characters, optional>"}` |
| `table` | `{"rows": [[cell, …], …]}`: 0–20 rows, each with exactly as many cells as `columns`, each cell 0–80 characters |

`page patch` takes `{id, reading?, items?, alert_text?, note?}`: at least one of `reading` and `items`. `items` (at
most 50) replaces components of the page's own view by `key`: each must name a key the stored view (or one of its
detail pages) has, with the same `type`, and the page after the patch must pass the page floor; nothing is added or
removed. `alert_text` (0–120 characters) is words the alert push may carry if the manager finds the rule tripped; the
Agent never says that it tripped. `note` (0–120) is the history line. The hub stamps the reading's time; a page keeps
its latest 30 readings (trimmed in the write's transaction), which the trend draws. A patch is a write: it counts in
the page's 60 an hour and the 600 an hour of all pages, and is refused on a page the owner paused (`paused_by_owner`),
a removed page (`removed`) and a draft (`not_kept`; a draft is shown, not checked).

Who may call it: a conversation that writes the page (phase 1's writers), and **the run of the routine bound to the
page**, under its data-only grant. That grant is checked again on every call, from the hub's records, never an
argument: the routine is the page's (`routine_id`), the grant row exists, the page is kept and not paused, and the
caller is the conversation of the routine's latest run within 2 hours of that run's start; anything else is refused
with a sentence (`not_current_run`, `no_grant`). A routine run is listed `page` with `get` and `patch` of its own page
only; every other action, and every other page, is refused as in phase 1.

### The routine: `schedule_routine.page_id`

`schedule_routine` takes `page_id`: the routine refreshes that page. Only a conversation that writes the page may
bind one; a page has at most one (a second without its `id` is refused with the first's id, so the Agent edits that
one), and a routine's `page_id` never changes. A bound routine:

- runs at most once an hour: a cron rule with two runs less than 60 minutes apart, or an interval under 60 minutes, is
  refused (`too_often`), in the tool and in the app's task editor alike; a daily rule always passes;
- never pushes and never posts to the Inbox: its push is `asked`, `notify_user` is not listed to its runs, its
  failures show on the page footer instead of a push, and its Inbox setting is off; `report`, `todo_id` and `push` /
  `inbox` other than those are refused;
- runs in a conversation of its own (never the main conversation), whose prompt tells it which page it keeps, the
  question, and the reading's shape for the display;
- is skipped, without a push, while its page is a draft, paused or removed (the run history says so).

**With a draft.** The conversation calls `page create` (with the watch) and then, in the same turn,
`schedule_routine` with the draft's `page_id`. The routine is stored but held: it does not run, and the draft's
preview card is drawn again with its frequency. **Keep** gives the routine its data-only grant (`assistant_page_grants`,
granted by the owner's Keep, with the device) and arms it; **Not now**, the draft's expiry and the Agent's `page remove`
of the draft delete the routine. So draft → preview → Keep makes one page and one routine.

**With a kept page.** A conversation that writes the page may bind a routine to it; the grant is then given by that
conversation (it already writes the whole page; the routine gets less), and the page footer shows the routine from
then on.

**Afterwards.** Removing the page (the owner, or the Agent with a reason) turns the routine off; restoring the page
turns it on again. Deleting the page for good (Delete now, the 30-day purge of Recently removed) deletes the routine
and the grant in the same step, so deleting removes both. Deleting the routine in the app's Scheduled tasks unbinds the
page: the grant goes, the page stays, the footer loses Refresh now. A routine whose page no longer exists is deleted
when the hub starts.

### The preview card

A watch draft's preview card (phase 1's card, still read-only and in the drafting conversation) adds, above the
choices, in this order: **Checks** (the question), **How often** (the routine's rule in words, or "Not scheduled yet"
until `schedule_routine` binds one), **Shows** (a number, a status light, a table, a small trend: the display drawn
empty, as on the page), and, with an alert rule, **Alert me when …** (the rule in words) as a switch, off. A **Show on
Overview** switch, off, comes next. The choices stay Keep in Dashboards / Keep and pin to the sidebar / Not now; Keep
reads the two switches with the tap (the card's fields), so turning the alert on and putting the page on Overview are
the owner's, in the same tap.

### How a watch is drawn

The manager draws the watch, never the model: the page's first section (key `persona.watch`), the page's row on the
Dashboards index (its glance) and its Overview card are made from the watch and the readings, in the request's
language and the owner's time zone. `spec/fixtures/dashboards/watch.json` ("draws") has every display empty, normal and
tripped, in English with exact components; the hub's tests and the mock hub both read it.

| Display | The page's first section | Tripped |
| --- | --- | --- |
| `number` | a `metric`: label the question, value the number with grouping and the unit, detail "Checked today 08:02" | `tone: caution`, icon `warning`, the rule in words before the detail |
| `status` | a `metric` with a status light: icon `check` / `warning` / `warning` and icon color `green` / `amber` / `red` for ok / warning / problem; value the reading's text or "All good" / "Needs a look" / "Problem" | the rule in words before the detail (the light already says it; no tone with a color) |
| `table` | a `metric` with the row count ("2 rows", "No rows"), then a `table` with the columns and rows (none when there are no rows) | the metric as for `number` |
| `trend` | a `metric` as for `number` with a `spark` of up to the latest 30 readings (from 2), `fill: true` | as for `number`, and the spark's tone `caution` |

Empty (not checked yet): the metric's value is "—" and its detail "Not checked yet" (a status light is `info`,
`gray`). A tripped watch draws as tripped whether or not the owner switched the alert on: the page shows where the
line is; only the push waits for the switch.

**The index row** (the read-only display on every app): the kept page's row gets the reading as its `value` ("—"
before the first), the trend's spark, and for a status light its icon and color; a tripped watch's row has icon
`warning` in `amber`. Never a badge or a count.

**Overview (web).** A kept watch page the owner put on Overview has an `overview_card` place: id `g_` + the page id's
16 hex digits, opening the page; it draws the watch section alone (a table's first 5 rows) with the page's title.
These places are listed only to a request that asks with `?pages_overview=1` on `GET /api/v1/extensions/-/ui`, which
the web sends and the iPhone and Android apps do not, so they never show up in the apps' Settings, where those apps
draw extensions' Overview cards. The web Overview arranges and hides them as any card; the page footer has a **Show on
Overview** switch, drawn only for a request whose `context.platform` is the web or the desktop app.

### The footer

A kept page's footer (phase 1's, the hub's own) adds, before Pin / Rename / Remove:

- with a routine: a line "Checked today 08:02 · next check today 20:00" (instead of "Updated …"), and **Refresh now**,
  which runs the routine now (owner action `persona.refresh`). It is answered in its request with the line "Checking
  now…" (received), which stays while the run works and clears when the run writes a reading; a run that ended without
  one leaves "The check didn't update the page", a failed one "The check failed" with **Retry**. At most 6 an hour a
  page, and never while one is checking ("Already checking"). The tap, the run's start and the reading are timed apart
  (principle 7) and logged once a day as tap to start and start to reading, p50 / p95 / max (`pageRefreshDayLine`);
- **Pause updates** (`persona.pause`): the page is paused by the owner; the routine's runs are skipped and every write
  is refused (`paused_by_owner`) until **Resume updates** (`persona.resume`). Paused, the footer says "Updates paused"
  and has no Refresh now. Any kept page has Pause updates, with a routine or not;
- with an alert rule: a row **Alert me**, the rule in words under it, with a switch (`persona.alert`, values
  `{on: "true" | "false"}`); only the owner changes it;
- for the web and the desktop app only: a row **Show on Overview** with a switch (`persona.overview`, values
  `{overview: "true" | "false"}`).

### Alerts

The only push a page ever makes, decided by the manager from the owner's switch and the readings, never by the model:

- after each reading the manager evaluates the rule ("trips": `spec/fixtures/dashboards/watch.json`), and keeps whether
  the latest reading tripped;
- it pushes when the alert is on, the new reading trips, the reading before it did not (or there was none), and the
  page has not pushed in the last 12 hours. Staying tripped never pushes again; a reading that does not trip re-arms
  it; turning the switch on while tripped does not push ("pushes" in the fixture);
- the push: source `page_alert` (kind 1 of the card ledger: the owner switched it on, as their own reminders), level
  P1 (held for the briefing in low-distraction mode), the page's title, the question and the reading in words, then
  `alert_text` if the reading came with one; related key `page-alert:<page id>`. Tapping it opens the app, not yet the
  page: a push carries no destination for now (#15417).

Drafts, updates, patches, readings, refreshes and failed checks never push and never count in the card ledger, as in
phase 1.

### Storage

A new migration adds to `assistant_pages` the watch (`watch` jsonb), the alert switch and state (`alert` jsonb, phase
1's column: `{on, tripped, pushed_at}`), Overview (`on_overview`), the routine (`routine_id`) and the refresh's state;
the readings (`assistant_page_readings`, the latest 30 a page); and the grants (`assistant_page_grants`: page,
grantee kind `routine`, the routine's id, rights `data`, who granted it and when), deleted with the page.

### Later

Not in phase 2 (they stay with Dashboards, #15417): opening a page from a card's link, the number of pages in the
apps' delete-conversation confirmations, labelling a tap's message "from a dashboard", an alert push that opens its
page, Overview's heading for these cards (they sit under Extensions on the web), and whether the switch turns on by
default. Phase 3 attaches pages to spaces, directions and to-dos, gives task Agents a data-only `todo_page`, and adds
the Apple Watch glance.
