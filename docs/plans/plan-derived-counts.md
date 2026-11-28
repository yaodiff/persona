# Plan: the counts people see, computed once in the managers and relayed by the hub (#11679)

**Status: decided 2026-11-22 (section 6 is the record; one case of D11 stays open until the owner confirms it); steps 1
to 4 have landed.** It is the Stage C item "Derived counts computed on the hub and shipped as generated view types" of
#10613. #5974 owns what each count means; this plan covers where each count is computed and how it reaches the clients.
Each step becomes its own issue and PR. A step that changes a number people see is marked **visible**, applies a named
decision (section 6) and carries the principles self-check. **Who it serves:** everyone: Assistant users see one number
for each name on every screen and device, Agents users can trust that badge, list and Lock Screen agree, and nothing new
is drawn.

**How the facts were taken:** read on `origin/main` at e4308ecc23 (2026-11-20); nothing was built or run. Open-PR
overlaps are from the 171 PRs open that day; GitHub lists at most 100 files per PR and 23 reach that cap, so overlaps
may be undercounted. Steps find code by name, since line numbers drift. Paths that do not exist yet are plain text.
Section 6 and the lines it changes were re-checked on `origin/main` at 81361c1dd1 (2026-11-22).

## 1. Why

- **The managers send few counts, and the hub computes almost none.** What travels: the Live Activity's FleetState, from
  the default manager only (`hub/fleet_link.go` fleetLiveOf); Persona Link's summary; the roster's `inbox_needs_you`;
  TodoCounts on `GET /api/v1/todos`, which `hub/todo_view.go` adds up across managers; the direction tree's counts; and
  per-row numbers (`pending_interactions`, the children rollup). The hub's own counts are only the sessionlist state
  maps and the catalog (`counts=1`, `with_counts=1`, `catalog=1`). The hello, health and every push carry no count:
  nothing in `server/internal/hub` sets `aps.badge`, and no Android code calls `setNumber`.
- **So every badge is counted by the client that draws it, from raw lists.** "Agents that need you" alone is counted by
  **19 functions in five codebases**. Server: `needsYou` with `liveCounts` (`assistant/fleet_live.go`), `linkAttention`
  (`assistant/fleet_attention.go`), `addCount` (`sessionlist/query.go`). iOS: `AgentsNeedsYou.badge`,
  `AgentsNeedsYou.rows` for the section, `AssistantBadges.waiting`, `WatchPet.status`, `IntentPicker.waiting`. Android:
  `ShellCounts.agentRows`, `agentNeedsYouRows`, `ManagerRoster.waiting`, `NeedsYou.kt` `needsYou`. Web: `stateCounts`,
  manager-attention's `waitingAgents`, `islandState`, `filterCounts`, the Machines page's `agentCounts`, and inbox.ts's
  `waitingAgents` (cards, labelled agents). Rust core: `sort::counts`.
- **The same label shows different numbers for the same data:**

  | One thing waiting | iPhone / Android Agents badge | Web title, favicon, Dock | Persona Link | Live Activity |
  | --- | --- | --- | --- | --- |
  | A muted agent | 0 (on a hub with bounded lists, 1 for a card in a muted space whose row is not loaded) | 1 | 1 | 0 |
  | A quiet task's agent, hub with bounded lists | 0 (iPhone: on the Persona tab if it has a card; Android: 1 if it has a card and its row is not loaded) | 1 | 0 | 0 |
  | A directory-hidden session (an Agent-folder hide rule) | iPhone 0; Android 1 with a card on any hub, and with no card on a hub without bounded lists | 0 | 0 | 0 |
  | A card on no session (`personad ask` outside herdr) | 1 | 0 | 0 | 0 |
  | A Persona conversation with a card | 0 (iPhone Persona tab 1) | 0 | 0 | 1 |

  The switcher (several assistants) counts by the rule `docs/assistant/managers.md` ("Counts") writes, not the Agents
  badge's: a Persona conversation with a card counts 1 there on every client; the iPhone counts a waiting child apart
  from its parent, and Android each card of a session it does not hold (section 2).

- **The Inbox has three units.** Every client's tab or sidebar badge counts unread open action items (iOS `unreadCount`,
  web `badgeCount`, Android `inboxUnreadCount`). The roster's `inbox_needs_you`, the Apple Inbox widget, the face dot
  and the watch count open action items, read or not (`tellInboxNeedsYou`, `actionCount`). The Inbox bar's Needs you
  adds pending cards, one per card.
- **The To-dos badge has four rules:** the web's `ownerBadge`, what waits on the owner (#11085); every open, non-quiet
  to-do, shelved ones too, on iOS release builds and Android (`TodoStore.badgeCount`, `ShellCounts.todos`); the hub's N
  on iOS behind the V1 gate (`TodoFeatures.released = false`); to-dos due today or overdue, across every assistant, on
  the Apple widget.
- **Text in the code and docs contradicts the code.** `InboxStore.actionCount`'s comment, `docs/apple/home-widgets.md`
  ("What each number counts") and the `assistantwire.InboxNeedsYou` comment each say they are "the Inbox badge"; the
  badge reads `unreadCount`. `TodoCounts.needs_you`'s schema text says the To-dos row shows it; no release client reads
  it. `DESIGN_SYSTEM.md` 3.8 says the tab badge is "the same number the row and the list's Needs You show", while
  `spec/fixtures/needs-you` pins a split (`muted_and_quiet_agents_are_rows_but_not_on_the_badge`).
- **Why it happened.** Each surface got its rule when it shipped. The fixtures pin single surfaces (needs-you: the
  Agents tab; attention: Link; todo-counts: the list), some pin splits on purpose (`TestLiveActivityKeepsItsOwnCount`),
  and no test compares surfaces, which `docs/design/proposals/owner-at-a-glance.md` G3 asks for. The one hub-sent count
  clients could use, TodoCounts, is summed across managers while the pages show one assistant: iOS recounts with several
  managers, and the web and Android never adopted it. Lesson: ship per-manager values; leave the scope to the client.

## 2. Inventory

Paths in the server rows are under `server/internal`. Every row was checked at e4308ecc23.

| Count | Computed by | From | Shown on | Semantics |
| --- | --- | --- | --- | --- |
| Live Activity `needsYou` / `working` / `unread` | manager: `assistant/fleet_live.go` buildFleetIn → needsYou(liveCounts, cardStands), foldSidesIn | decorated rows, visible pending cards, mutes | Live Activity (APNs); the open Apple and Android apps (`{type:fleet}`); Android's ongoing notification, widget and tile; the hub's widget wakes (`hub/widget_wake.go` widgetCountsOf) | One per agent. Quiet and directory-hidden rows are skipped (fleetInputsInto); muted agents and subtasks are out; sides count under their parent. A Persona conversation counts only by a standing card. An elicitation does not stand. No card on no session. `working` includes rows that only wait in the background. Default manager only |
| Persona Link `needs_you` | manager: `assistant/fleet_attention.go` linkAttention | same inputs | Persona Link | Rows in needs_you, not the assistant's and not quiet. Muted agents and delegating cards count. At most 20 items plus `more`. Equal to the web badge's rule (`spec/fixtures/attention`) |
| Roster `inbox_needs_you` | manager: `assistant/inbox_store.go` tellInboxNeedsYou; hub: `hub/managers.go` | inbox items' status() | every client's switcher | Open action items, read or not. Told on every change; the hub drops repeats and coalesces the push over 1 s |
| Paged session counts and catalog | hub: `sessionlist/query.go` addCount; `hub/sessions_catalog.go` | the sessionlist index | web badge on bounded hubs; Android paged chips; the Rust core's aggregates | A state map over the whole filtered list. No mutes or quiet applied. `background_waiting` is a subset of working, `just_finished` of idle |
| TodoCounts and places | manager: `assistant/todo/projection/list.go` Counts; hub sum: `hub/todo_view.go` | to-dos, linked sessions, cards | iOS only, behind the V1 gate (TodoCountsSource) | N is the Needs you section, each to-do once; quiet and Can wait are out; a step counts with its parent |
| Direction tree counts | manager: `assistant/direction_tree.go` | todo_triage.go (triageCountsAsOpen) | web and Android leader rows | Tasks by tree state; quiet tasks included, unlike TodoCounts |
| Row `pending_interactions`, state, children | manager: `assistant/fleet_sessions.go` decorateInventory, `assistant/fleet_fold.go` | pending cards, host status | every row | Per row; the basis of every state count. Possible gap, read from code only: the row count takes `s.fleet.interactions`, while pendingCards (`assistant/fleet_ix.go`) also merges session-review cards from the review store |
| iOS Persona tab | `AppModel.countBadges` | cards in scope | tab badges (iPad, visionOS, iPhone Duo) | Cards on Persona conversations and on quiet tasks' agents (#1731), one each; muted and directory-hidden cards out |
| iOS Agents tab | `AgentsNeedsYou.badge` | cards plus the sessions the app holds | Agents tab, top switch, sidebar, Home Agents widget | One per waiting session; a card on no session counts 1; Persona, quiet, muted and directory-hidden are out (hidden cards through directoryVisibleInteractions, hidden waits in `AgentsNeedsYou.waiting`). On a hub with bounded lists, sessions that wait with no card count only if loaded, and a card in a muted space counts if its row is not loaded (`TabBadges.place` reads the space from the rows the app holds; quiet falls back to the to-do list); the full read (readNeedsYou) feeds only the switcher, and only with several assistants |
| iOS Agents section and filter | `AgentsNeedsYou.rows` (ChatsView) | the same | list header, filter chip | The badge's rows with muted and quiet kept; follows the machine and hub filters |
| iOS Inbox | `InboxStore.unreadCount` / `actionCount` | inbox items in scope | tab, drawer row, icon / Inbox widget, face dot | Unread open action items / open action items, read or not |
| iOS To-dos row | `TodoStore.sidebarCount` | to-dos in scope | drawer and sidebar | `badgeCount` (open and not quiet, any section) unless the V1 gate is on |
| iOS switcher | `AssistantBadges.waiting` + `counts` | sessions, cards, roster | switcher, avatar dot | With several assistants: sessions once, each card on the session it names (`sessionKey`, not `requestHome`), so a waiting child counts apart from its parent; Persona conversations by their cards (`.assistant` counts); quiet, muted and directory-hidden out; a card on no session 1; plus `inbox_needs_you`. Where a bounded list is a window, it reads the hub's sessions in Needs you (readNeedsYou) |
| iOS app icon | `AppIconBadge.count` | TabBadges, unreadCount | home screen | Persona + Agents + Inbox unread; set by the app only (`setBadgeCount`), so it goes stale while the app is not running |
| Watch, Siri, visionOS | `WatchPet.status`; `IntentPicker.waiting`; `VisionShell` ornament | phone summary or own reads | frog and complication; Siri; ornament | Watch: card sessions plus needs_you sessions, muted and Persona included, unscoped. Siri: cards, one each. Ornament: Persona plus Agents |
| Android Agents tab | `ShellCounts.agentRows` | cards, sessions | Agents badge | The iOS rule (`spec/fixtures/needs-you`) over the rows the app holds, except that Android reads no `hidden_by_directory`: a directory-hidden session's cards count on every hub (`fleet.interactions` carries every pending card, and HubInteractionCenter keeps them all), and its wait with no card counts on a hub without bounded lists, whose inventory keeps the marked rows (bounded lists leave them out). On a hub with bounded lists, until the Agents page is shown in a run, those are the 50 most recently updated (`HubSessionsRepository` firstPage, Mode.Paged; the cold-start snapshot is cut to 50 too): only that page asks for the complete list (`requiresCompleteMembership`, set while it is observed). Outside them, a session that waits with no card is not counted, and a card counts even if its task is quiet or its space muted, both read from the row |
| Android Persona tab | `Root.kt` assistantRequests.size | cards | not drawn on Glass | Cards on Persona conversations only; a quiet task's card is on no tab |
| Android page and others | `agentNeedsYouRows`; `inboxUnreadCount`; `ShellCounts.todos`; `ManagerRoster.waiting` | | Agents header; sidebar Inbox and To-dos; switcher | Muted and quiet kept; unread action items, minus read Lark rows; open and not quiet; with several assistants, sessions once, Persona conversations counted (no `assistant` check, for cards or for sessions in Needs you), quiet and muted out as the tab reads them, a card of a session the app does not hold 1 each (outside the 50-row window, one per card), plus `inbox_needs_you`. The header and the switcher keep directory-hidden sessions, as the tab does |
| Web Agents badge | `web/src/lib/sessions.ts` stateCounts / useCounts; `web/src/lib/current-assistant.ts` useScopedCounts | rows, or the hub's `state.needs_you` | tab title, favicon, Electron Dock (unscoped); sidebar Home, ShellSwitch (scoped) | Without bounded lists: quiet out, muted in, no sessionless cards. With them (the default): the hub's number, quiet and muted in (`quietCounted: true`) |
| Web Inbox, To-dos, switcher | `web/src/lib/inbox.ts` badgeCount; `web/src/lib/todo-triage.ts` ownerBadge; `web/src/lib/manager-attention.ts` | items, to-dos, rows | sidebar rows, switcher | Unread action items; what waits on the owner; with several assistants, sessions once from the complete list (useFullAgentSessions), children folded, Persona conversations by their cards, a card on no session 1, quiet, muted and directory-hidden out, plus `inbox_needs_you` |
| Desktop | `desktop/src/badge.ts`; `web/src/lib/desktop-island.ts` | the web's numbers | Dock and taskbar; notch island | The Dock shows the web's unscoped Agents number, and nothing resets it on sign-out. The island counts quiet and muted agents and background waits as working |
| HarmonyOS and the Rust core | `native/app-core/crates/persona-core/src/sort/mod.rs` | projection input | HarmonyOS group headers only | Muted counted; background waits as idle; no quiet field. No badges on HarmonyOS |

## 3. Target

### 3.1 One definition per count, computed once in the manager

- **A new strict core**, server/internal/assistant/fleetcount (D15), under the domain rules (the "Domain model"
  section that #12899 adds to `docs/server/architecture.md`): its adapter fleetcount/fleetcountadapt builds typed Agent
  and Card values from proto, and its Tally reports through a visitor; no records, no proto inside. So its tests run on
  their own (1.5 s against 118 s for package assistant in a developer's own run), though check-fast gains nothing,
  since package assistant imports it. Each agents count is one domain service over the values the adapter builds from
  the inputs fleetInputsInto already gathers, and the Live Activity and Persona Link call it; the roster's Inbox count
  stays a function over the Inbox items' status(), as tellInboxNeedsYou computes it today.
  *Revised 2026-11-22 in #10613:* a core with an adapter and a visitor in place of a package returning records.
- **Each count has an id and a one-line definition** in a new doc, docs/protocol/counts.md (principle 3). An id starts
  with the rule of the client it replaces, so no number changes before its decision.
- **The pilot is the Agents count.** `agents_needs_you` is `spec/fixtures/needs-you`'s badge rule as iOS computes it
  (Android keeps directory-hidden sessions, section 2): one row per waiting session, folded to its parent; Persona
  conversations, quiet tasks' agents, muted agents and directory-hidden sessions (`docs/protocol/protocol.md`, "Agent
  directory visibility") left out; a card on no session counts 1. Its unit, one per session, is the one
  `docs/design/DESIGN_SYSTEM.md` ("One count, per session") takes from `docs/assistant/managers.md` ("Counts"). That
  bullet writes the switcher's rule, which also counts Persona conversations by their cards and names no hide rule, so
  the switcher draws this count plus a part of its own (section 3.3 item 7). Every count follows
  `docs/protocol/agent-states.md`: a background-only wait is not counted as working, and Just finished changes no count.
- **Later ids come one at a time, each after its decision:** `inbox_unread`, `inbox_action`, `todos_needs_you` (from the
  same Counts() call `GET /api/v1/todos` uses, pinned by `spec/fixtures/todo-counts` and `spec/fixtures/todo-view`),
  `persona_cards`, and the Live Activity's and Link's numbers.
- **The composer's Needs you is not a CountsView id.** It stays the `assistant_needs` payload that
  `docs/design/proposals/todos-redesign.md` (7.3, Attention) and `docs/design/DECISIONS.md` (11-19) define:
  `{todos: N, elsewhere: M, groups}`. That payload carries the per-to-do count unit, gates the rolling upgrade (a client
  counts each to-do once only where it is present), and is what the watch, widgets and Live Activity read for it there.
  CountsView's `{n, keys, held, more}` cannot carry its parts, and `schema/wire/todo-view.schema.json` reserves its
  type, so one number keeps one carrier. If both are built, the manager package computes them from the same snapshot.

### 3.2 One small generated view per manager, relayed as is

```
CountsView {                       // per manager; schema/wire, generated for Go, TS, Swift and Kotlin
  epoch: string, rev: int,         // a new epoch (manager restart) resets; a lower rev loses to a higher one
  as_of: int,                      // ms; only to say how old a kept view is
  counts: { <id>: { n: int,
                    keys: [{id, cards[]}],   // counted rows, agent:<machine>/<session> or card:<id>, at most 200
                    held: [{id, why}],       // rows the list keeps but the count leaves out: muted, held (D11)
                    more: int } },
  totals: { <id>: int }            // optional (D12): each count's n, copied raw by the hub for pushes and widget wakes
}
```

The schema follows section 6. In `agents_needs_you`, `held` lists muted agents and rows whose only cards are held
(D11). The header draws them in uncounted groups (D6). It draws quiet agent rows from the client's own list.
The live round supplies quiet agents exclusively to `persona_cards`, which lists their eligible cards
individually. Each client names the ids it draws (D5).

- **Manager to hub.** The manager names the capability `counts_view` and tells a one-way `counts.view` event, built in
  liveLoop's round after tellLive from the same fleetInputs, so `agents_needs_you`, FleetState and Link's summary come
  from one snapshot. It is deduplicated by bytes as tellLive does, re-told after each handshake (liveChanged(force)),
  and sent after the round's list changes, so a badge is never ahead of its list.
- **The hub stays a relay.** It keeps each manager's latest view as raw JSON on the bus connection, beside
  `inboxNeedsYou` in `hub/bus.go`; it checks only that the view is an object with `epoch` and `rev` of at most 16 KB,
  and never decodes the counts. It puts the view on the roster entry (`GET /api/v1/managers`), with `counts_view: true`
  on the entry of a connected manager that named the capability, and pushes `{type:"counts", manager, view}` to apps
  that name the client feature `counts_view`, with `view: null` when the manager's connection ends (section 3.3 item 5
  says what a client does with it). `counts` is listed in hubMessageTypes, so no manager can broadcast it. The hub
  feature `counts_view` is a constant in features()' static list, listed always (D10). There is no new hub route and no
  hub arithmetic: clients add up the managers in a surface's scope. Managers' sessions and cards are disjoint; a test
  checks that a key two managers list (mesh, a migration) counts once.
- **Only full-role apps get views.** A view lists every waiting session and card of a manager, so it reaches only what
  may read them all. toWanting checks no role, and `wants()` honours `?features=` for every role, so hub/counts_view.go,
  at connect and on each change, sends only to clients with `role == roleFull` that want the feature, not through
  toWanting alone, as broadcasts skip scoped clients for everything but `sessions` (`hub/http.go`). Scoped device tokens
  get no push; the scoped writer's allowlist (`hub/scoped_tokens.go` scopedSocketEvent) would also drop a `counts`
  frame, a second line, not the rule. Extension tokens open only the routes `extwire.RouteScopes` lists, which has
  neither the roster nor `/api/v1/ws`, and scoped device tokens are refused the roster for the same reason. Answer-only
  devices (the watch) open neither (`answerReads`); whether they may is D12. Persona Link and peer credentials open only
  their link (`allowed` in `hub/devicetokens.go`).
- **Generation stays a pure addition (#9117).** Only CountsView is generated, because no client wrote it by hand.
  FleetState (an ActivityKit ContentState), link.Attention, sessionlist.Counts and InboxItem stay hand-written.
  CountsView does not `$ref` todo-view, since `scripts/gen/extension_schema.py` loads each schema alone.

### 3.3 What clients still compute, and why

1. **The owner's own acts (principle 7).** An overlay, generalising iOS TodoCountsSource's answered asks, holds answered
   or dismissed card ids, Inbox items marked done or read, and local mute changes (which move a key into `held`). The
   number drawn is `n` minus its matches, in the same frame as the tap. Each entry goes pending → received (the hub's
   receipt) → confirmed (its key leaves the view); a failed act, or a key still listed 30 s after receipt, drops the
   entry, so the number honestly comes back and the card's own error says why.
2. **Scope:** which managers and paired hubs a surface adds up (D2).
3. **List-local counts:** filters, search and folds intersect `keys`. Their unfiltered value equals their label's count,
   or is a `differs` row (section 3.5).
4. **Drawing:** hidden at 0, `99+`, the favicon's `9+`, tone, dot or number.
5. **Cold start and gaps:** the last view per hub and manager is persisted (Apple StateCache, Android DataStore, web
   storage) and drawn on the first frame as it is, with no new look; stale is a data state for diagnostics. `view: null`
   and a disconnected manager mean unknown, not old: the last view stays
   (`docs/assistant/north-star.md` "Unknown, never zero"; D13), so a manager restart or redeploy never switches the
   rule. With a view, no client recounts from a partial paged list.
6. **The frozen fallback:** today's rule, moved unchanged into one file per client. It is used only on an explicit
   absence: the hub's hello lists no `counts_view` feature (an old or rolled-back hub), or a connected manager's roster
   entry has no `counts_view: true` (an old or rolled-back manager), or a manager has never sent a view this client
   kept. A surface uses it while any manager in its scope is absent (D10), so one number never mixes two rules.
7. **The switcher's Persona part (D9).** The server part of step 12 supplies `persona_cards` beside
   `agents_needs_you`. Clients adopt it in their step 12 changes. The existing local rule covers this part until
   adoption and during rollout when a manager omits the field.

### 3.4 Old clients and old hubs

| Case | What happens |
| --- | --- |
| Old app, new hub | It never names the feature and ignores the unknown roster field. FleetState, link.Attention and `inbox_needs_you` keep their values |
| New app, old hub, or a manager without the capability | No view for that manager: the fallback, as today |
| New manager, old hub | The hub drops the unknown event, as it would `inbox.needs_you` |
| Manager restarts or is redeployed | `view: null`, then a view with a new epoch; meanwhile its last view is drawn as it was and no rule changes (D13) |
| Manager or hub rolled back | The hub's hello lists no `counts_view`, or the reconnected manager's roster entry has no `counts_view: true`: the fallback from that message on |
| Several paired hubs | Each hub on its own; the app adds them up |

New WebSocket types and features also need their rows in `docs/protocol/protocol.md` (`scripts/test/test_doc_wire.py`),
and only those, since 51 open PRs touch it; the contract lives in docs/protocol/counts.md, and
`docs/protocol/rolling-upgrade-compatibility.md` gets this table.

### 3.5 The agreement check

- **The definition** is `spec/fixtures/needs-you`, replayed by the manager package (authoritative) and by the iOS and
  Android fallbacks. A fallback is today's rule, so its replay keeps its step-3 marks until step 13 deletes both; a
  client that draws the view also replays a view-path case (the manager package's view for the fixture case, decoded and
  drawn) that asserts the fixture value. **The surface table**, spec/fixtures/counts/surfaces.json, has one row per
  surface in section 2 with its count id, scope, each client's code symbol and test, and per client `matches` (drawing
  the view would show today's number) or `differs` (with the decision that settles it). A guard,
  scripts/test/test_counts_surfaces.py, checks that every symbol exists, `matches` rows with the same label share one
  id, the `differs` list only shrinks, and a hub-fed surface calls its fallback nowhere outside the fallback file.
- **Replays:** each client's replay drives the functions its views call and, with a report switch, writes
  `{lane, case, surface, value}`; scripts/test/counts_agreement.py compares the lanes, reading a fallback lane's marked
  case as its known difference, not a failure. The scheduled main verification runs it
  (`docs/operations/main-verification.md`). A live mock-hub check stays optional: it needs new tooling, since
  `scripts/app/android.sh shots` writes images, not drawn numbers.

## 4. Steps

Steps 2 to 7b change no number. Steps 2 to 4 waited on no decision; step 5 follows D15, and 6 to 7b build on it. Step 2
left `DESIGN_SYSTEM.md` 3.8 to D6, which leaves it as it is (section 6.3). Steps 8 on apply named decisions, whether or
not they change a number: a client that draws views builds in D10's mixing rule and D13's handling of a missing view.
Section 6.2 lists what each decision adds to a step, and the client fixes that come before the views. Each step
re-checks open PRs on its files before it is pushed and leaves byte-identical the lines an open PR edits; the four
automated "continue #8584 verification repair" PRs (#10564, #10593, #10599, #10920), which touched many count files,
closed unmerged on 2026-11-22. *Revised 2026-11-22 in #10613:* the lines open PRs edit stay as they are, instead of
steps waiting for those PRs.

**1. This plan.** *Change:* this document, one CLAUDE.md index entry, comments on #10613 and #5974 (proposing that the
fixtures of steps 3 and 4 carry #5974's planned count fixture; the definitions stay #5974's). *Conflict:* CLAUDE.md is
in 10 open PRs; one entry in one cell. *Gate:* docs tests, privacy scan. *Rollback:* revert. *Done:* merged; steps 2 to
4 can start, and 5 to 7b once D15 is taken.

**2. Comments and docs that claim the wrong number.** *Change:* correct section 1's claims about what the code counts,
except `DESIGN_SYSTEM.md` 3.8 against the needs-you fixture's split, since choosing between them is D6. Also correct the
needs-you README's sentence that Android's section counts cards (it counts rows, `agentNeedsYouRows`) and the side-fold
README, which names `assistant/fleet_fold_test.go` while the test is in `assistant/fleet_side_fold_test.go`. TodoCounts'
description is schema text, so its four outputs are regenerated. *Conflict:* `InboxStore.swift` has 2 open PRs (#7495,
#4512), `assistantwire/inbox.go` 3 (#9782d, #6539d, #4512), the rest none. *Gate:* none. *Rollback:* revert. *Done:*
`scripts/gen/extension_schema.py --check` and the docs tests pass.

*Landed (#11832):* every correction above, none skipped (no open PR rewrites those lines), plus the same claims in the
`HomeInbox` comment, `docs/apple/ios.md` ("Counts"), `docs/assistant/managers.md` ("Badges") and the web side-fold
replay's comment.

**3. The Agents definition, pinned.** *Change:* every `spec/fixtures/needs-you` case gets `expected.badge_rows`, plus
six cases: a child's card under its parent, a side folded under its parent, a quiet task's card, a directory-hidden card
(counted nowhere), a delegating card (counts today; D11), an elicitation-only card (its session waits). The iOS and
Android replays assert `badge_rows`. A replay that skips a filter its client's badge applies is changed to apply it,
which changes no number: iOS's replay counts the badge from every pending card, while countBadges first drops
directory-hidden ones. A client whose own counting fails a case is not fixed here, since that would change what it
draws: the case is marked as that client's known difference, asserted at today's value and naming D1. The replay
becomes the frozen fallback's (section 3.5), so the mark stays until step 13 deletes the fallback; the client's step (8
or 9) adds a view-path case that asserts the fixture value instead. Android fails the directory-hidden card this way
(it reads no `hidden_by_directory`).
*Conflict:* the fixture and both replays have 0 open PRs. *Gate:* none. *Rollback:* revert. *Done:* Android
`:core:domain:test --tests '*AgentsNeedsYouFixtureTest*'` (`--max-workers=4`) and iOS AgentsNeedsYouFixtureTests pass,
each marked case at today's value, iOS on the implementer's own simulator.

*Landed (#11823):* `badge_rows` on every case and the six cases; iOS's replays (with `HomeWidgetTests`) now drop
directory-hidden cards and fold sides as the app does, and the one mark is Android's on
`a_directory_hidden_card_counts_nowhere` (D1); iOS needs none.

**4. The surface table and the agreement report** (section 3.5). *Change:* spec/fixtures/counts (README, surfaces.json),
the guard, the report switch in the existing replays, the lane differ, and entries in `scripts/ci/paths.py` and
`scripts/ci/check_fast_map.py`. *Conflict:* new files; `spec/README.md` has 1 open PR (#5969d), `paths.py` and
`check_fast_map.py` 0. *Gate:* none. *Rollback:* delete. *Done:* the guard passes and fails on each seeded violation
(unit tests); the baseline count of `differs` rows is in the README.

*Landed (#11900):* 32 rows, 26 of them `differs` (the baseline, in the README); the guard
(`scripts/test/test_counts_surfaces.py`), `PERSONA_COUNTS_REPORT` in both needs-you replays, the differ
(`scripts/test/counts_agreement.py`: iOS agrees on every case, Android on all but its D1 mark) and check-fast's rule.
`scripts/ci/paths.py` needed no entry: CI's lanes already select every file (`spec/**`, and `**` for scripts).

**5. The counting core.** *Change:* the rules of needsYou, liveCounts and cardStands (`assistant/fleet_live.go`) and
linkAttention's predicate (`assistant/fleet_attention.go`) move into fleetcount, with fleetcountadapt building its
inputs and rendering its Tally (section 6.2); add `agents_needs_you`, replaying the needs-you fixture through the
adapter. fleetInputsInto and foldSidesIn stay. *Conflict:* the four repair PRs, whose `fleet_live.go` hunks were in
fleetInputsInto (lines 170 and 180), closed unmerged on 2026-11-22; #12986 (a draft) edits needItem (line 485), one of
the moved names; the moved functions start at line 254. `fleet_attention.go` has 0. *Gate:* D15, #12899's domain
rules, and agentref, the shared session identity that follows them (section 6.2). *Rollback:* revert. *Done:*
TestLinkAttentionFollowsTheSharedFixtures, TestLiveActivityKeepsItsOwnCount, TestFleetCountsCodexRootsOnly,
TestFleetCountsASideUnderItsParent and TestSnapshotNeedsYouIsTheState pass unchanged; FleetState and Link bytes are
identical on the fixtures; the core's tests run on their own (`go test -p 4`, `GOFLAGS=-trimpath`), and the core and
its adapter pass their domain-rules hooks; the whole round's benchmark holds the gate in section 6.2; the known
internal/assistant failures on main are unchanged. *Revised 2026-11-22 in #10613:* a core and its adapter, gated on
the domain rules and agentref rather than a domain-package list, with an allocation gate on the whole round.

*Landed (#15250):* `server/internal/assistant/fleetcount` (core) decides every surface's rows in `AgentsNeedsYou`,
`fleetcount/fleetcountadapt` (adapter) builds its agents and cards from proto and fills `FleetState`'s and Persona
Link's needs-you rows, `fleetcount/fleetcounttest` (testkit) writes a tally down for tests, and `assistant/fleetview`
(library) writes the text the needs-you and working rows share. The needs-you fixture replays in `fleetcountadapt`
(`TestAgentsNeedsYouFollowsTheSharedFixtures`, every case), and TestFleetMatchesOracle's 1500 random fleets keep
FleetState and Persona Link's bytes on the agents `fleetInputsInto` hands over. What differs from section 6.2, and why:
`fleetInputsInto`'s quiet line asks `fleetcountadapt.Visibility(sess).LeftOut()`, since the four repair PRs were still
closed when this landed; Persona Link's summary types moved to a contract (`internal/linkwire`, aliased in `link`),
because an adapter returns only contracts; `i18n`, `textfmt`, `compat`, `lazyre` and `deeplink` declare the `library`
layer the adapter and `fleetview` import them as; `transcript` stays undeclared (it imports the legacy core `tokenuse`),
so package assistant hands the renderers an agent's title before it is clipped; `Holding` has a third value,
`HoldingHeldBySupervisor`, for the cards D11's 2026-11-23 confirmation keeps out of every row; the badge's rows tell
their further cards (`VisitCardUnder`), which the fixture's rows list; an unknown update time is not refused (the kit's
zero Instant is unknown, and an agent sent without one still waits); `Agent` has no `Turn` stage and the adapter's
`Agent` no turn start, since `workingSince` stays here until step 11; the adapter's `Agent` and `Card` take the caller's
place as an index and mint its Slot, since only an adapter or a testkit may mint one (`out-slot-mint`); `Agent`'s one
exported predicate is `Counts(Surface)`: no caller asks whether one agent needs the owner (the Tally tells the rows, and
the working rows and `foldSidesIn` read the wire state until step 11), so it has no `NeedsOwner()`, and `LeftOut()` is
`Visibility`'s, since `fleetInputsInto` drops a session before it has an Agent and the exclusion reads the visibility
alone; `Counts` asks it on the Live Activity's and Persona Link's surfaces, so the core leaves those agents and their
cards out whatever it is handed and one list of agents serves every surface (the fixture replay does not apply it: the
badge lists a quiet agent's row and leaves a directory-hidden one out itself); `Card`'s `Standing` stage also takes a
`Telling` (`TellingTold`, `TellingUntold`) and the adapter's `Card` whether the owner was told, since Persona Link rings
for an agent only once one of its cards is the owner's and was rung for, which `Holding` does not say (a card can be the
owner's and not rung for yet); on Persona Link's surface `WhyHeld` is any row none of whose agent's cards was handed to
the owner, a terminal dialog with no card included, since that summary keeps one bit per row, whether it rings, and
carries no why word, while on the Agents list's surface `WhyHeld` is D11's held only; the fixture replay reads the
badge's tally through a test visitor, since no wire shape carries those rows before steps 6 and 7a; the benchmark
measures the core's copies instead of printing `unsafe.Sizeof`, which the domain rules refuse beside a core; and
`agentKey` and `keyOf` stay with `foldSidesIn`, which reads the Live Activity's rule through `liveCounted`. The gate
(`BenchmarkRound`, 60 agents, 12 cards, a kept round as `liveLoop` runs it): 284 to 255 allocs/op and 56,512 to 30,008
B/op, the latter including the core's own copies (12,800 B/op: per surface 60 agents in a 5,376-byte block and 12 cards
in a 1,024-byte one). `BenchmarkLiveRound` (500 agents, no card): 134 to 136 allocs/op and 62.8 to 161.8 KB/op, the two
surfaces' copies of the agents (two 49,152-byte blocks) and nothing else.

**6. The schema and generated types.** *Change:* schema/wire/counts.schema.json (`x-out` ts, swift, go, kotlin;
`x-guard` arkts, rust), its four outputs, `.gitattributes` rows and a `schema/wire/README.md` row; samples from step 5's
Go output. The schema has no `surfaces` map (D5) and an optional `totals` (D12); `held` has the muted and held reasons
(D11) and no quiet one (section 3.2). *Conflict:* schema/wire, the generator and `scripts/test/test_wire_schema.py` have
0 open PRs. *Gate:* step 5. *Rollback:* revert; nothing reads the types. *Done:* `--check` and test_wire_schema.py pass;
round trips pass in Go, Kotlin, Swift (`swift test` in PersonaWire) and the web.

*Landed (#15512):* `schema/wire/counts.schema.json` with its four outputs (Go in `assistantwire`), the contract in
`docs/protocol/counts.md` and the samples in `spec/fixtures/counts/view_samples.json`, which
`TestViewSamplesAreFleetcountsRows` (`fleetcountadapt`) holds to fleetcount's rows for the needs-you cases they name.
What differs from section 3.2, and why: each count id is a named, optional field of `counts` and of `totals`, not a
`{<id>: …}` map, so every client reads a typed field (the generator keeps a map whose keys are not all required a map)
while the JSON stays the same and a new id stays an optional addition; the view names its `build`, which D18's
diagnostics read; a row's `id` and its cards are ID types (`CountsRowKey`, `CountsCardID`, since a wire schema cannot
`$ref` `ids.schema.json` yet) and `why` the closed enum `CountsHeldReason` (`muted`, `held`) with an explicit unknown;
`keys` may list fewer than 200 rows, `n` being the rows it lists plus `more`, because 200 rows of UUID-length session
and card ids come to about 22 KB, past the 16 KB the hub is to keep (section 3.2), so step 7a or 7b settles the bound
and the limit together; `held` has the same bound; `protocol.md` gets a pointer section only, since this step adds no
message or feature (step 7b adds their rows).

**7a. The manager tells the view.** *Change:* the capability constant in a new assistantwire/counts.go (avoiding
`assistantwire/bus.go`, 9 open PRs), named in the hello's capability list in `assistant/bus.go` (10 open PRs; one
identifier on one line); the tell from liveLoop. *Gate:* steps 5 and 6. *Rollback:* revert. *Done:* on one round,
`agents_needs_you.n` equals the fixture rule over the same inputs; a quiet fleet tells nothing; a hub built from
`origin/main` drops the event; the round benchmark stays within 5% of step 5's.

*Landed (#15650):* `CapabilityCountsView` (`counts_view`), `EventCountsView` (`counts.view`) and `CountsViewMaxBytes` in
`assistantwire/counts.go`; the capability on the hello's list; and the tell from liveLoop (`assistant/counts_view.go`):
package assistant calls `fleetcount.AgentsNeedsYou` on the badge surface, then `fleetcountadapt`'s `Fleet.View`, a
TallyVisitor of the adapter's own that writes `counts` and `totals` in one `Accept` pass (D12) and alone spells the
`why` word (D11); package assistant adds the epoch (one per run), the rev (up one only when the counts change), `as_of`
and `build`, tells nothing for a round that changed nothing, and tells the last view again after each handshake.
`TestCountsViewFollowsTheSharedFixtures` holds `n` and the keys to the fixture's badge over a round's inputs,
`TestTheCountsViewIsToldWhenItChanges` the rest; `busEvents` in `hub/bus.go` has no `counts.view`, so a hub from
`origin/main` drops it. The bound and the limit are settled together: a view takes at most 16 KB, the most the hub will
keep, and `keys` and `held` stop at 200 rows or at the first row past that bound, the counted rows first (`more` carries
the rest). What differs, and why: `View` is a method of the adapter's `Fleet`, since a tally's Slots are places in that
fleet; the view is decided on the fill Persona Link's summary uses (its cards ordered by time, then id), so the two
share one copy of the core's values (`Fleet.Agents`, `Fleet.Cards`); `Tally.On(surface)` lets `View` refuse another
surface's tally; a count's `producer` is keyed to the view's schema rather than to an empty `waits_on`: every count the
schema carries names its producer and no other does, since `inbox_action` waits on no decision but joins the view only
with step 12 and its fixture (`docs/protocol/counts.md`, "Later ids"), and `TestViewWritesTheCountsThatNameIt` is the
code map's check; the Inbox's and the to-dos' counts enter as inputs of `View` once the schema carries them, so none is
added here; and `badgeRows` now cuts its lists from four arrays (and makes none when nothing waits), which took the
view's share of the round from 47 allocations to 11. The gate, measured against `origin/main` on one machine
(`BenchmarkRound`, 60 agents and 12 cards, the round now building the view too): 255 to 266 allocs/op (+4.3%, within 5%)
and 30,008 to 35,096 B/op (+17%, past 5%): the badge decision takes 4 allocations and 3,776 B (its tally and the scratch
the core makes each round rather than keep between rounds) and `View` 7 and 1,312 B, measured apart.
`BenchmarkLiveRound` (500 agents, none waiting): 136 to 139 allocs/op and about 161.8 KB/op either way. Telling an
unchanged view allocates nothing.

**7b. The hub relays it** (section 3.2). *Change:* a new hub/counts_view.go and one field in `hub/bus.go` (1 open PR,
#6924d); one line each in bus_broadcast.go (3) and features() in extensions_link.go (5); the kept views go out in the
connect sequence in `hub/http.go` (7) and after a client hello in `hub/clientmeta.go` (0). Rows in protocol.md,
docs/protocol/counts.md with its CLAUDE.md entry, and a mock domain module under scripts/mock registered with one line
in `scripts/mock/mock_domains.py`'s DOMAINS (0), so `mock_hub.py` (24) is untouched. *Gate:* step 7a (the capability
name). *Rollback:* revert; clients see no feature. *Done:* hub tests: kept raw; sent only to full-role apps that want
it, with a scoped client that asks `?features=counts_view` queued no `counts` frame (checked before the scoped writer's
allowlist, so the role check is tested on its own) and the roster refusing scoped, answer and extension tokens;
`counts_view: true` only on a connected manager that named it; `null` on disconnect; oversized views refused; connect
order hello, machines, counts, then session lists; TestBroadcastTypes, `test_doc_wire.py` and the hub's architecture
guard pass, apart from `TestHubAnswersOnlyItsOwnRoutes`, which already fails on main (#9485) and is compared with
`origin/main`.

*Landed (#15893):* the relay (`hub/counts_view.go`): the hub keeps each manager's last view on its bus connection,
compacted and checked only for its object, `epoch`, `rev` and bound, and sends it to full-role apps that ask, at connect
between `machines` and the session lists, after a `client_hello` that names the feature, and as `view: null` when the
connection ends; relays and connect-time sends share `h.mu`, so an app never gets a manager's view after a newer one.
`spec/fixtures/counts/views.json` writes D13's cases as given, one transport event (`arrived`, `arrivedEmpty`,
`wentOffline`, `restarted`, `leftRoster`, `signedOut`) and then, with its section in the fixtures' README;
`TestTheHubReplaysTheRestartCases` replays its restart cases through the hub, and `test_wire_schema.py` holds each of
its views to the schema. What differs, and why: the hub feature is appended from `proto/features_counts.go`, the proto
feature seam (`docs/server/architecture.md` "Registering a domain's entries"), which heads features()' static list, so
it is listed always and no line of `hub/extensions_link.go` changes; the roster carries the `counts_view` flag and not
the view, so a client has one source of views, the WebSocket, and the roster read every `managers` push asks for does
not grow by up to 16 KB per manager, which also leaves no view on the roster to keep from scoped, answer-only and
extension tokens; the mock domain module waits for step 8, the first step that draws a view and screenshots it.

**8. Android reads `agents_needs_you`.** *Change:* ShellCounts and the switcher's agents share read the view, with the
overlay and the frozen fallback, and the switcher adds its Persona-conversation part by today's rule (section 3.3 item
7), so a Persona conversation's card stays on it; HubManagers.kt decodes the generated type. Step 3's Android mark
stays on the fallback's replay, which is unchanged, and a view-path case asserts the fixture value. **Visible** in two
cases (section 2, Android rows). Wherever an Agent-folder hide rule matches a session that waits, on any hub: its
cards, and on hubs without bounded lists its wait with no card, leave the badge and the switcher. On hubs with bounded
lists while the app holds its 50-row window: sessions outside it that wait with no card now count, cards on unloaded
quiet tasks' agents or in muted spaces leave, and the switcher counts such a session once, not once per card. That is
the written rule (the needs-you fixture, and `docs/protocol/protocol.md` ("Agent directory visibility") for hidden
sessions), applied under D1, with a self-check. *Conflict:* `ShellCounts.kt` and `HubManagers.kt` 0;
`Root.kt` (#11660d, #5969d) is not touched. *Gate:* steps 3 to 7b, D1, D10 (mixed support) and D13 (a missing view),
and the Inbox plan's step A4, which brings the Kotlin domain kit after the client domain guard and #13024 (section
6.2). *Rollback:* revert, or turn off the hub feature. *Done:* ShellCountsTest, with a paged case (a wait with no card
outside the window: counted from the view, not by the fallback), a hidden case (a directory-hidden session's card: left
out by the view, counted by the fallback) and a switcher case (a Persona conversation's card counts with a view and
without), AgentsNeedsYouFixtureTest (the marked case at today's value, the view-path case at the fixture's) and the
surfaces replay pass; `scripts/app/android.sh shots` in three styles, light and dark, shows today's numbers where the
list is complete and no hide rule matches a waiting session. *Revised 2026-11-22 in #10613:* it also waits for the
Inbox step that brings the domain kit its reducer is built on.

**9. iOS reads `agents_needs_you`.** *Change:* the tab badge and the switcher's agents share read the view, and the
Home Agents widget's number, for the whole hub (D2), as is the app icon's; the switcher adds its
Persona-conversation part by today's rule (section 3.3 item 7). Any iOS mark from step 3 stays on the fallback's
replay, with a view-path case beside it. **Visible** in two cases. On hubs with bounded lists, the tab badge: sessions
that wait with no card now count when not loaded, and a card in a muted space whose row is not loaded leaves. With
several assistants, the switcher: where a session and a child folded under it both wait, it counts 1, not 2 (section
2, iOS switcher). Both are the written rule, applied under D1, with a self-check. *Conflict:*
`AppModel.swift` 4 (#11456d, #11282, #7517, #2051), `AssistantScope.swift` the four repair PRs, `HomeWidgetWriter.swift`
and `AppIconBadge.swift` 0; `AgentsNeedsYouRows.swift`, which the four repair PRs touch, stays as it is, since the
fallback and the list's section keep reading it. *Gate:* steps 3 to 7b, D1, D10 and D13, and PersonaDomainKit with
the client domain guard (section 6.2); D2 for the widget. *Rollback:* revert. *Done:* AgentsNeedsYouFixtureTests
(any marked case at today's value, the view-path case at the fixture's), AppIconBadgeTests, AssistantBadgesTests (a
Persona conversation's card counts with a view and without) and
HomeWidgetTests pass; the PR names the `docs/apple/performance.md` rules followed (P1, P4, P6) with countBadges timings
before and after from an optimized build, light and dark screenshots, and the Xcode used. *Revised 2026-11-22 in
#10613:* it also waits for the domain kit its reducer is built on.

**10. Web and desktop read `agents_needs_you`.** **Visible:** muted agents leave the tab title, favicon and Dock; cards
on no session join; quiet agents leave on bounded hubs. Under D17 the same number also adds Persona's questions and
unread Inbox items, for the whole hub. *Change:* useCounts and useScopedCounts keep their signatures, so views do not
change; the switcher's agents share (`waitingAgents`) reads the view and its Persona-conversation part stays counted by
today's rule (section 3.3 item 7). Its rule already matches the view's otherwise (section 2, web switcher row), so its
number does not change. Separately, in a bug issue of its own, the Dock resets to 0 on sign-out and hub change.
*Conflict:* `sessions.ts` and `current-assistant.ts` meet the repair PRs; `manager-attention.ts` has
#10920, #10396d; `attention-signals.ts` #8290, #1995d; `SidebarParts.tsx` (12) is untouched. *Gate:* steps 3 to 7b,
D1, D2, D10 and D13, and the web's domain kit with its lint rules (section 6.2). *Rollback:* revert, or turn off the
hub feature. *Done:* vitest through check-fast, with a
switcher case (a Persona conversation's card counts with a view and without); `npm run build` after merging
`origin/main` (FIRST_SCREEN_BUDGET); light and dark screenshots; the desktop e2e reads the Dock equal to the web's
number, and 0 after sign-out. *Revised 2026-11-22 in #10613:* it also waits for the domain kit its reducer is built on.

**11. Persona Link and the Live Activity on the same definition** (server only, **visible**). *Change:* Link's and
FleetState's `needsYou` become `agents_needs_you`, with Persona conversations with a standing card as a named part (D4);
wire shapes stay; `spec/fixtures/attention` and TestLiveActivityKeepsItsOwnCount are rewritten; the widget waker decides
on the number the widgets draw. *Conflict:* `fleet_attention.go` 0, `widget_wake.go` 0, `docs/operations/link.md` 1
(#10759). *Gate:* D1, D3, D4. *Rollback:* revert; only numbers change. *Done:* the Go tests and the web
attention-fixture test pass; the lane differ is green for Link.

**12. The next counts, one at a time.** *Change:* each is one server PR (the id, its fixture, its counts.md line), then
one PR per client: the Inbox after D7; To-dos after D8 (`todos_needs_you` per manager, tested equal to that manager's
`GET /api/v1/todos` counts on `spec/fixtures/todo-counts`, which retires TodoCountsFallback's multi-manager case); the
Persona tab, the app icon and the switchers' Persona-conversation part after D9; numbers outside the running app
(alert pushes, the iOS Notification Service Extension, widget wakes) after D12. *Conflict:*
`assistant/todo_projection.go` and `todo/projection` have 4 open PRs (#11179, #11168, #10809d, #9823), `todo-triage.ts`
3; draft PRs #10809 and #9777 change what Needs you holds and must be reconciled. *Gate:* the named decision.
*Rollback:* revert. *Done:* the count's rows in surfaces.json turn `matches`; the lane differ is green.

*Landed, the To-dos' server part (#15896):* `todos_needs_you` is an optional field of the view's `counts` and `totals`
(`schema/wire/counts.schema.json`, its four outputs regenerated; no client reads it yet), written by `fleetcountadapt`'s
`Fleet.View`, which `surfaces.json` names as its `producer`. Package assistant hands `Fleet.View` the counts of its last
read of the whole to-do list, the `Counts()` `GET /api/v1/todos` answers with (`server/internal/assistant/counts_view.go`):
every read of the list, of one to-do or of the places keeps them; when no read follows a `{"type":"todos"}` within a
second, the manager reads the list itself, one read at a time; until a run has counted its to-dos, each round asks for
a count, and the view leaves it out. `TestCountsViewCountsTheToDosAsTheListDoes` holds the view's number to the list's
`counts.needs_you` on every `spec/fixtures/todo-counts` case, `TestCountsViewFollowsTheToDoList` to `GET /api/v1/todos`
on a manager's own list, and `TestCountsViewRecountsOneReadAtATime` keeps a slow read from starting a second one.
What differs, and why: the count lists no rows (`keys` absent, all of `n` is `more`), since `Counts()` gives numbers
only; the to-do projection (`todo/projection`) is neither declared nor given a record or an exported field while #9823,
#11168 and #11179 are open, and `assistant/todo_projection.go` is untouched; `Fleet.View` takes the counts as the wire
has them (`assistantwire.TodoCounts`, in an `opt.Option`), and once the projection is a core they come in as its value,
`Fleet.View` staying the producer. TodoCountsFallback's multi-manager case retires with the iOS client's step, and the
clients' To-dos rows stay `differs` until each draws N. `BenchmarkRound` with the to-dos counted: 266 to 267 allocs/op
and 35,096 to 35,184 B/op against `origin/main` on one machine, the to-dos count's one allocation; `BenchmarkLiveRound`
(no to-dos counted) and telling an unchanged view are unchanged (139 allocs/op; 0).

*Server Persona cards (P6 of #15397):* the optional `persona_cards` count and total carry a card tally from
`fleetcount.PersonaCards`. The live round supplies quiet agents for this tally alone, before any surface folds sides.
`Fleet.View` writes the oldest card keys that fit after the Agents rows and accounts for the rest in `more`.
`spec/fixtures/persona-cards` pins the D9/D11 rule, including forms, original asking sessions, held cards and display
copies. The live-round test checks the view's revisions and the quiet-agent exclusions for Agents, Live Activity and
Persona Link. Client adoption and pushes remain steps under D9 and D12.

**13. Retire the fallbacks.** *Change:* when D14's window ends, delete each client's fallback file with its fixture
replay and step-3 marks (the view-path cases stay); the hub keeps FleetState, `inbox_needs_you` and Link for old apps;
the guard fails if a fallback file returns, as a branch that edited it can keep it through the delete (#11476).
*Gate:* D14, `count_skew` at 0. *Rollback:* restore from history. *Done:* every `differs` row is gone or links a
decision.

## 5. How we will know it worked

| Measure | Now | Target |
| --- | --- | --- |
| Functions counting "agents that need you" | 19 (section 1) | 1 in the manager, plus one frozen fallback per client until step 13 (the switchers' Persona-conversation part stays local until step 12, D9) |
| `differs` rows in surfaces.json | baseline recorded in step 4 | only shrinks; 0, or each row links a decision |
| Cross-client agreement on the fixtures | not measured: the web replays no needs-you case, no lane compares surfaces | every lane's report equal, in the scheduled main verification |
| `count_skew` per device-day: a drawn number unlike view plus overlay for over 5 s, per surface, no content, in Developing Persona packages (`docs/operations/developing-persona.md`) | no diagnostic | 0 before step 13 |
| Tap to the tapping device's own badge (principle 7) | not measured | the same frame (Apple: under 16 ms on the main thread, optimized build) |
| Card raised to the number drawn on another device | not measured | p95 at most 1 s on the mock hub, reported apart from execution latency |
| Manager cost per liveLoop round | not measured | no added allocations in step 5; within 5% after step 7a |
| Cost of a decided rule change | the server, three clients and the fixtures | one server PR, the id kept (D5, D18) |
| Open mismatch issues | #5974, #11085 open | closed |

## 6. Decisions

Decided 2026-11-22: the owner approved the recommended answers to D1 to D16 and to two questions new with the approval,
D17 and D18, except one case of D11 whose evidence was wrong: routed terminal questions, which stay counted, and open
here, until the owner confirms them. This section is the record of those answers (#12843), each written in short; the
options D1 to D16 weighed are in the open list it held before #12843, in this file's history. D1, D6 to D9,
D11 and D16 settle what a count includes, the questions #5974 and the To-dos proposals hold; the others are this plan's
own: which count each surface draws, its scope, and how views travel and are drawn. The ones that change what people see
are also in `docs/design/DECISIONS.md` (11-22). A decision is reopened only by a new line here that says what changed.
Section 6.2 says what the decisions change in the steps, and 6.3 which docs change with them.

### 6.1 The decided list

- **D1** The one Agents rule (#5974). *Decided 2026-11-22:* `spec/fixtures/needs-you`'s badge rule
  (`badge_rows`) counts every surface named "Agents that need you", the switchers' agents part included. Its other homes
  are `docs/design/DESIGN_SYSTEM.md` 4.8, the 11-10 entry "Needs You counts sessions" in `docs/design/DECISIONS.md` and
  `docs/protocol/protocol.md` ("Agent directory visibility"); `docs/assistant/managers.md` ("Counts") writes the
  switcher's rule and is not its source. #12645's rule joins the fixture: a `mirror_of` copy counts nowhere. With one
  assistant and nothing muted, quiet, hidden or sessionless, no number changes.
- **D2** Scope of the numbers seen outside one assistant's pages. *Decided 2026-11-22:* the whole hub. The iOS
  app icon and the Home widgets change; the web's title and favicon, the desktop Dock and island, the watch, Siri and
  the To-dos widget already count every assistant. Persona Link stays per computer. The Live Activity stays on the
  default manager, a stated exception: merging FleetState across managers would be hub arithmetic (section 1's lesson),
  and the owner kept Fleet unchanged (`docs/design/DECISIONS.md`, 11-18); it is reopened only on a reported need, with
  the merge done in a manager. The icon adds one value per assistant, `persona_cards + agents_needs_you + inbox_unread`,
  which D1, D7 and D9 make that assistant's tab badges added up and its switcher count; never AppModel's
  `assistantWaiting`, which mixes three units.
- **D3** Persona Link's muted agents. *Decided 2026-11-22:* listed but not counted in `needs_you`. Cards on no
  session are counted (D1) and listed with an empty route, so a consumer opens the app; counted items come first, so
  `more` is counted overflow. Nothing draws Link's number today (`desktop/src/link-contract.cts` passes on neither
  `needs_you` nor its items), so nobody sees a change.
- **D4** The Live Activity's headline. *Decided 2026-11-22:* `agents_needs_you` plus each Persona conversation
  with a standing card, counted once per conversation as today; FleetState keeps its shape (no part field). Cards on no
  session are counted and listed as `card:<id>` items: older apps open `persona://open` for them (`FleetLink.url` falls
  back when an id has no `/`), and Allow / Deny still work through the interaction id. Held cards follow D11, the scope
  D2. `docs/apple/live-activity.md` writes down the two differences left against the app icon: quiet tasks' questions,
  and one count per conversation rather than per card.
- **D5** The `surfaces` map. *Decided 2026-11-22:* no map. Each client names the ids it draws, and
  `spec/fixtures/counts/surfaces.json` with its guard stays the one table: a `{surface: id}` map carries neither a scope
  nor a sum (the app icon draws three ids). Under D7 both Inbox ids stay ones clients know, so no remap is pending.
  Reopened only for a decided remap that must reach every device at once; the field would then be a pure optional
  addition.
- **D6** The Agents section header and filter against the badge. *Decided 2026-11-22:* the header and the
  filter count the badge's rows, as `docs/design/DESIGN_SYSTEM.md` 3.8 and 4.8 and the To-dos proposal's 3.7 already
  say; today the fixture's own case draws "Needs you 2" over five rows (principle 4). The rows the count leaves out stay
  listed inside the Needs you section, open by default, in uncounted groups named for why, in words the apps already
  use: **Muted**, and **Assistant is looking** for quiet tasks' agents and held cards (D11). Not "Can wait", which
  To-dos uses for what the owner can answer later.
- **D7** The Inbox unit. *Decided 2026-11-22:* unread action items for the iOS face dot and every switcher's
  Inbox part (web, iOS, Android), so an assistant's switcher count is its tab badges added up. The face dot says
  "Something in the Inbox needs you" and opens a sidebar whose Inbox row counts unread, so after reading, the dot stays
  while the row is empty. The Inbox widget keeps Need You (open action items, read or not): its label, dots and empty
  text say that unit (`docs/apple/home-widgets.md`, "What each number counts"), as the Inbox page's Needs you section
  does. The roster keeps `inbox_needs_you` for old apps.
- **D8** The To-dos badge. *Decided 2026-11-22:* N, the owner's decision 4 (a) in
  `docs/design/proposals/todos-redesign.md`: open to-dos whose phase is needs_you, each once, per manager
  (`todos_needs_you`, from the list's Counts()), the number of the composer's To-dos group, the To-dos Needs you section
  and the widget (principle 4). It narrows #11085 (what waits on the owner): personal to-dos no Agent was ever on (the
  phase table's To do) leave the badge; counting them would be one row of the hub's phase table, not a client rule. The
  To-dos widget's number moves from due today or overdue to N, while it still lists Today. Each client switches when it
  draws the hub's phases; until then the web keeps `ownerBadge`.
- **D9** The Persona tab, the app icon and the switchers' Persona part. *Decided 2026-11-22:* `persona_cards`
  counts the cards on Persona conversations and on quiet tasks' agents (a quiet task's question is the main
  conversation's to tell), one each; muted, directory-hidden and held (D11) cards and `mirror_of` copies are left out.
  Android's Persona tab takes this iOS rule; today it counts a quiet task's question on no tab. The icon stays the sum
  of the tabs (`docs/design/DESIGN_SYSTEM.md` 3.8, 11-12 in `docs/design/DECISIONS.md`), and every switcher's Persona
  part reads `persona_cards`. Moving the tab to the composer's Needs you is left to #5974. The Live Activity's Persona
  part (D4) is not `persona_cards`, and the docs say so.
- **D10** Mixed support on one hub. *Decided 2026-11-22:* per surface, views are drawn only while no manager
  in its scope is absent. The hub always lists `counts_view`, a constant in features()' static list (hub features reach
  clients only in the hello, so a gated list would flip the rule on every redeploy), and each roster entry carries
  `counts_view: true` only while that manager is connected and capable. A manager is absent when the hello lacks the
  feature, its entry is online without the flag, or this client never kept a view from it; offline, or `view: null`
  after a disconnect, is not absence (D13). An assistant's surface depends on its own manager, a whole-hub surface on
  every manager, each hub on its own. The kill switch is the manager's capability: reverting step 7a sends every client
  to the fallback.
- **D11** Delegating and supervisor-held cards. *Decided 2026-11-22, routed terminal questions excepted:*
  held: listed and answerable under Assistant is looking (D6), and left out of every count until first handed to the
  owner, as `docs/design/DESIGN_SYSTEM.md` 4.8 ("A question the assistant sees first"),
  `docs/assistant/unified-entry.md` ("Asks go to the supervisor first") and north-star's `supervisor_held` already treat
  them. The state path follows: a session whose only pending cards are held is not counted; agentState itself does not
  change. *Unconfirmed: routed terminal questions.* A parsed terminal question from an agent of a supervised task goes
  to its supervising conversation with or without leaders (askLadder in `server/internal/assistant/ask_route.go`), so
  default installs see it. The owner approved D11 on the evidence it was put with: that such a question moves up to the
  owner after `askHoldFor` (5 minutes). That evidence was wrong when it was taken: since #11254, already on main at
  e4308ecc23 where this plan read its facts, askTick skips terminal questions, so one stays with its supervisor until an
  explicit `ask_escalate`, with no time limit. Held, it would leave a supervised task's blocked terminal Agent on no
  badge anywhere for as long as that lasts, where iOS and Android count it today. The case stays open here, and the
  fixture PR before step 5 puts it to the owner. Until the owner confirms it, a routed terminal question (a `delegating`
  card of kind `screen`) is not a held card for any count, and neither is the terminal dialog behind it: that fixture
  PR pins it counted, and step 5 gives it no held Why. A confirmation, or a decided bound on the hold, is a new line
  here; the state path then also leaves out a session whose needs_you comes only from a terminal dialog with a held
  screen card.
  *2026-11-23 owner confirmed: routed terminal questions are held.* A card a task's supervisor holds (`delegating` on
  any kind but `question`: a permission, a plan, or a routed terminal question of kind `screen`) is the supervisor's
  until `ask_escalate` hands it to the owner: it is not listed in the owner's Needs you and counts nowhere (list, header,
  filter and badges), the clients offer no way to answer it, and a session whose pending cards are all so held is no
  row, the terminal dialog behind a held screen card included. A `delegating` question (the assistant deciding whether
  it may answer for the owner) stays as decided above. `spec/fixtures/needs-you` pins it in
  `a_routed_terminal_question_is_held` and `a_supervisor_held_card_beside_an_open_one_leaves_the_row`; iOS follows,
  Android carries D11 marks until its own change.
- **D12** Numbers outside the running app. *Decided 2026-11-22:* never a plain `aps.badge` (the relay's alert
  template carries none, and one push group's badge would overwrite the others' share); Android sets no notification
  number; the watch reads no views (`answerReads` unchanged; the phone fills WatchSummary from views at step 9). Numbers
  outside the app travel in sealed or direct alert and withdraw pushes, only to devices that register the `badge_counts`
  feature, and the iOS Notification Service Extension composes the icon with AppIconBadge's shared code. The view gets
  an optional top-level `totals: {<id>: n}` that the hub copies raw, so it never decodes `counts`. The push member is
  defined after D9 and D2 are built; its first version sets the badge only on devices with one push group, and
  PersonaNotify gets no App Group.
- **D13** A disconnected manager, and `view: null`. *Decided 2026-11-22:* the last view per hub and manager is
  kept, persisted, and drawn as it is, with no new look; a new epoch replaces it. `view: null`, going offline and a hub
  restart keep it, so a restart or a blue-green switch stays invisible and unknown never shows as zero; stale is a data
  state for diagnostics (`as_of` feeds `count_skew`). The view is dropped only when the roster no longer lists the
  manager, at sign-out, or on D10's explicit absence; every client, web storage and desktop included, clears persisted
  views at sign-out, as they carry machine and session ids.
- **D14** The fallback window before step 13. *Decided 2026-11-22:* conditions, not dates. The floor is the
  first stable release with steps 7a and 7b, recorded when it ships; `proto.Protocol` is not bumped. Step 13 waits until
  the floor has been stable for 90 days, the client has drawn views in a released build for 30 days, `count_skew` is 0
  and `counts_shadow` clean for 30 days on opt-in devices the step-13 issue names (two or more per client), and
  `count_fallback` is 0 for 14 days for the reasons step 13 removes (an old hub, an old manager online); managers
  offline that never sent a view are reported apart. An absent manager leaves its surfaces without a number and the
  switcher marks it, never a sum that silently leaves it out. `count_skew` stays for good.
- **D15** The package's name and home. *Decided 2026-11-22:* server/internal/assistant/fleetcount, holding
  only the agents' and cards' count rules; "attention" already names Link's rule and fixture, the web's signals and the
  To-dos proposal's composer payload. It is declared `//persona:layer core`, with its adapter
  fleetcount/fleetcountadapt declared `//persona:layer adapter`, rather than pinned by a test of its own; its shape is
  in 6.2, step 5. It gives check-fast nothing, since package assistant imports it; a developer's own run of its tests
  took 1.5 s against 118 s for package assistant. *Revised 2026-11-22 in #10613:* the layer declarations replace a
  line in a list of domain packages; the name and home stay.
- **D16** The direction tree's `needs_you`. *Decided 2026-11-22:* never a view id: the tree already computes
  its counts once in the manager and every client draws them as sent (`server/internal/assistant/direction_tree.go`). It
  converges in place in the To-dos rework PR that moves the tree onto the projection's phase; that PR adds a test that
  the directions' `needs_you` add up to TodoCounts' and states the tree's one exception: a worker task nested under its
  leader's Agent is not counted on its own. Open PRs there: #11821 and #12557, both drafts.
- **D17** The desktop and web number outside the window (new with the approval). *Decided 2026-11-22, with low
  confidence:* the Electron Dock and taskbar badge, the browser tab's title and the favicon keep one number between them
  (today `useCounts().badge`, the Agents count, in `web/src/lib/attention-signals.ts`), and it becomes the app icon's
  sum from `docs/design/DESIGN_SYSTEM.md` 3.8 and 11-12: `persona_cards + agents_needs_you + inbox_unread`, for the
  whole hub (D2). Once step 10 applies D1, a quiet task's question, a Persona question and unread Inbox items would
  otherwise reach no desktop number outside the window while the iPhone's icon shows all three, and the Electron app
  replaces the native Mac app whose Dock 3.8 defines. The alternative stays on record: only the Dock and taskbar take
  the sum, the title and favicon stay Agents-only, and 3.8 gets a sentence saying so. Reopened, in favour of the
  alternative, if step 10's review or people's reports find the summed number misleading in a browser tab.
- **D18** Count ids when a definition changes (new with the approval). *Decided 2026-11-22:* an id names a meaning
  on screen ("agents that need you"). A decided refinement keeps its id (D1's mirror rule, D3, D4, D11):
  docs/protocol/counts.md records the change and the manager build that applies it, and skew during a rollout is
  accepted, since the built-in manager updates with the hub and diagnostics name an external manager's build. A change
  of unit, such as unread against action items, is a new id. Under D5 clients name their ids, so a new id for every
  refinement would cost a client release each time.
- **D19** The main conversation's waiting count and its exact list (new with the owner's 2026-11-22 scope revision):
  only current questions the user must answer, authorizations only the user can give and deliveries explicitly
  requiring user acceptance count. Unstarted or failed-start tasks, agent permission requests, terminal dialogs and
  superseded questions stay with the supervisor and do not inflate this number. Selecting it opens exactly those
  counted items on iPhone and the shared web/Mac UI. This surface does not change D1's Agents-list badge, D8's To-dos
  count or their rows, and it does not settle D11's open routed-terminal case for those other surfaces.

### 6.2 What the decisions change in the steps

- **Before step 5**, one PR: the needs-you fixture records the decided definitions: `expected.header` equals the badge's
  rows (D6), held cards count nowhere while a routed terminal question still counts, in a case of its own (D11), and a
  `mirror_of` copy counts nowhere once #12645 lands (D1). Its README allows a decided definition change, which each
  fallback's replay keeps at today's value behind a mark. Its PR puts D11's open case to the owner: confirm it, or bound
  the hold.
- **Client fixes before views**, each its own PR, with the UI self-check in light and dark where it is drawn:
  - Android, after #12645 or rebased on it: it reads `hidden_by_directory` into its Session contract and drops those
    sessions where sessions enter its lists, before grouping and counts (D1). That removes the fixture's one
    known-difference mark; dropping them only in the badge would leave them in the other groupings.
  - Android: a quiet task's question counts on the Persona tab (Brief and Board; Glass draws no Persona tab badge), and
    the switcher's parts equal the tab badges (D9, D7).
  - iOS: the face dot (`apple/Persona/DesignSystem/ShellComponents.swift`) counts unread Inbox items, and the switcher
    unread Inbox items and `persona_cards` (D7, D9). `surfaces.json`'s `ios.inbox_widget` row, which holds both the
    widget and the face dot today, splits: the widget draws `inbox_action`, the dot `inbox_unread`.
  - Web: the switcher counts each assistant's tab badges added up (D7, D9).
  - Web, Android in its three styles, and iOS, one PR each, after the fixture PR: the Needs you header and filter count
    the badge's rows, with the Muted and Assistant is looking groups (D6); `surfaces.json` gains a `web.agents_section`
    row. HarmonyOS is out of scope.
- **Step 5** follows D15 and lands after #12899's domain rules and agentref, the session identity after them; the domain
  slice scaffold writes both packages' skeletons first. fleetcount imports no proto and has no type parameters, and its
  values are always valid: closed sets for State, Role, Lineage, Muting, Visibility, Placement (its own row, or under an
  `agentref.Ref`), CardStanding, Holding, Why and Surface; Agent and Card are built only through staged builders that
  start from an `agentref.Ref` or an interaction id and a Slot (the caller's place in its own list, which the core never
  reads) and refuse every zero part; Agents and Cards copy their input, so the core owns its arrays. Why has
  WhyNeedsYou(), WhyHeld(), WhyMuted() and WhyQuiet(), and Counted() says whether a row counts (D11); a routed terminal
  question gets no held value until D11's unconfirmed case is decided. AgentsNeedsYou(surface, agents, cards) replaces
  needsYou's callbacks with the Surface (badge, Live Activity, Link) and returns a Tally whose rows are unexported:
  `Tally.Accept` tells each row, oldest first, with its Slots and Why, then `VisitTotal`. It folds on its own, keying
  every session and card by `folded_under`, children and sides alike (foldSidesIn folds only sides, so
  `a_childs_card_counts_under_its_parent` would get the worker's key), and a card is a row of its own only when it has
  no asker (an empty `session_id`), since quiet agents' cards survive visiblePendingCards. The quiet and
  directory-hidden exclusion is an Agent's LeftOut() and the fixture replay applies it. fleetcountadapt builds the
  values from proto (a rejected session is logged and counted as skipped, never defaulted), and its renderer, a
  TallyVisitor, fills FleetState's and `link.Attention`'s needs-you rows by Slot; the proto-to-contract formatting it
  shares with package assistant's working rows moves to a small library package both import. The step leaves
  fleetInputsInto's loop (`assistant/fleet_live.go` lines 167-185) byte-identical while an open PR edits it, the adapter
  running on fleetInputsInto's result, and LeftOut replaces the quiet line once none does: the four repair PRs that did
  closed on 2026-11-22, and the step re-measures with `git merge-tree` against its own head, as the Inbox plan's section
  4 does. foldSidesIn and workingSince stay until step 11. archguard gets a names-only movedClusters row and no
  contracts.txt entry. The fixture replay decodes `spec/fixtures/needs-you` into proto values and runs the adapter, the
  core and the renderer, without compiling package assistant. **The gate:** one benchmark in package assistant runs the
  whole round (build the values, decide, render FleetState and `link.Attention`) before and after, with 60 agents (a
  third each needs-you, working and unread) and 12 cards. allocs/op must not rise. B/op must not rise except by the
  core's own copies of its agents and cards, which the benchmark prints (`unsafe.Sizeof` of Agent times agents plus Card
  times cards) and the PR shows. ns/op is not gated on a shared machine.
  *Revised 2026-11-22 in #10613:* builders, Slots, closed sets and a Tally visitor in place of records and the
  caller's values inside them; the open PRs' lines are left alone instead of waiting for them; the allocation gate is on
  the whole round; agentref comes after #12899, not with it.
- **Step 6** has no `surfaces` map (D5) and an optional top-level `totals` (D12); `held` carries the muted and held
  reasons (D11). Quiet rows reach the view only at section 3.2's cost, so until then a client draws them from its own
  list. docs/protocol/counts.md records each refinement with the manager build that applies it (D18).
- **Step 7a:** fleetcountadapt, called from package assistant, maps fleetcount's Tally to the generated CountsView and
  takes `totals` from the Tally's own `VisitTotal` in the same pass (D12); only it writes the wire `why` word (D11), and
  neither fleetcount nor the hub knows the wire shape. Until the Inbox and the to-dos have cores of their own, their
  counts enter as the adapter's own inputs (today's status() fold and the projection's Counts()), and no record is added
  for them. Each count in `surfaces.json` gets a `producer` beside `pinned_by`, naming the one adapter function that
  produces it, and the step's test checks every producer against the adapter's code map: a count with an empty
  `waits_on` and no producer fails, as do a code-map id the table lacks and a planted table missing a producer (D18).
  `persona_cards`, `inbox_unread` and `todos_needs_you` wait on D9, D7 and D8's steps and may have none until then.
  *Revised 2026-11-22 in #10613:* the core's adapter, not package assistant, maps the Tally.
- **Step 7b:** `counts_view` always listed, and the per-manager roster flag (D10). It creates
  spec/fixtures/counts/views.json with D13's four cases (a disconnect keeps the number, a removed manager drops it, a
  new epoch replaces it, a new device after a hub restart shows the fallback until the managers reconnect) and adds a
  section on it to `spec/fixtures/counts/README.md`; the hub's relay test replays the restart case.
- **Steps 8 to 10:** each client keeps views through a reducer that is a domain module beside its adapter (Android
  :core:counts:domain in android/core/counts; Swift PersonaCountsDomain and PersonaCountsAdapt in
  apple/Packages/PersonaCounts; the web and desktop's features/counts/domain and adapt), which replays views.json
  through its adapter (keep on null, offline or a hub restart; replace on a new epoch; a lower rev loses; drop on roster
  removal or sign-out) and does not create the file. The domain module alone builds its events, and each event gives the
  next set of kept views; persisted views are data in the adapter, and one that fails to restore is dropped with the
  `count_fallback` diagnostic. Each client clears persisted views at sign-out (D13) and draws a surface from views only
  under D10. Step 9 also moves the app icon and the Home widgets to the whole hub (D2), or step 12 does. Step 10 also
  draws D17's number on the Dock, the taskbar, the tab title and the favicon, and carries D3's Link part, since
  `spec/fixtures/attention` and its web replay change there anyway, so the fixture is rewritten once (else step 11
  carries it). Each step waits for its platform's domain kit and checks, which the Inbox plan's client steps bring
  (`docs/plans/plan-inbox-vertical-slice.md`, sections 3.3 and 4): Android for :core:domainkit, which Inbox A4 creates
  with :core:inbox:domain after A2, the client domain guard and #13024 (an unused result is a compile error in every
  Android module); iOS for PersonaDomainKit (Inbox I1, after the same guard); the web and desktop for web/src/domain-kit
  and its lint rules (Inbox W1).
  *Revised 2026-11-22 in #10613:* domain modules with adapters in place of one small pure reducer, after their
  platform's domain kit.
- **Step 11:** D4's headline, with Lock Screen screenshots of a sessionless item.
- **Step 12:** the Inbox after D7; To-dos after D8, the server's `todos_needs_you` after steps 5 to 7 and each client
  when it draws the hub's phases, with screenshots of the To-dos widget's new number and the outcome commented on
  #11085; the Persona parts after D9; pushes after D12, carrying D9's value, the badge first on single-group devices,
  and widget wakes reading `totals`.
- **Step 13** waits on D14's gates. After it, iOS and Android give a hub without `counts_view` a HubCompat reason of its
  own, with its own dismiss key and words (for example "Update the hub to see counts"), never `hubTooOld`, whose notice
  is dismissed for good (`docs/operations/hub-update.md`); `counts_shadow` and `count_fallback` go with the fallback.
- **The direction tree** (D16) is not a step: its test comes with the To-dos rework PR.

### 6.3 Docs that change with the steps

Recording the decisions changes no rule. Where a written rule disagrees with a decision today, the PR that implements
the decision changes it:

- `docs/design/DESIGN_SYSTEM.md` 3.8, "App icon badge": the sum of the tab bar's badges, on the iPhone's Home Screen and
  the Mac's Dock. Step 9 (or 12) writes the whole hub (D2); step 10 writes the desktop Dock, taskbar, tab title and
  favicon (D17).
- `docs/design/DESIGN_SYSTEM.md` 3.4, the web To-dos page's Up to you paragraph (`ownerBadge`): when the web draws the
  hub's phases, at step 12 (D8).
- `docs/design/DESIGN_SYSTEM.md` 4.8: the uncounted Muted and Assistant is looking groups, in D6's client PRs, which
  also draw held cards there (D11). Its header rule, and 3.8's, already match D6.
- `docs/assistant/managers.md` ("Unchanged"), which keeps the widgets on the default manager: at step 9 (D2). "Counts"
  stays the switcher's rule (D1).
- `docs/assistant/inbox.md`: its sentence on the roster's `inbox_needs_you` keeping a "separate meaning" is narrowed to
  old apps, with D7's first iOS PR.
- `docs/apple/home-widgets.md` ("What each number counts"): the widgets' scope at step 9 (D2), the To-dos widget's
  number at step 12 (D8).
- `docs/apple/live-activity.md`: D4's two differences, at step 11.
- `docs/protocol/agent-states.md` and `spec/fixtures/needs-you/README.md`: how a row in an uncounted group reads its
  state word (D6) and D11's state path, with the fixture PR before step 5; `docs/apple/ios.md` (the Agents row of the
  tabs table) with D6's iOS PR.

## 7. Out of scope

- **Deciding what a count includes** beyond section 6 (#5974, U7, todos-redesign 3.7, #11085, #10787, #9731): each
  lands once, in the manager package.
- **The composer's Needs you and its carrier:** `assistant_needs` stays todos-redesign's payload, gate and readers
  (section 3.1).
- **How badges look:** tones and the marks vocabulary (#4538, #4547, #4653).
- **List-local counts and totals:** chips, section and group headers, the Machines page, "N pending", "N more", the
  sessionlist counts and catalog that filter lists (#994), feed and metric totals (Today, brief,
  `docs/assistant/north-star.md` interruptions, `docs/assistant/agent-work.md` "Daily confirmation counts"), Lark and
  personal-agent unread counts, extension badges.
- **Regenerating any hand-written type** (FleetState, link.Attention, InboxItem; #9117 and the Inbox plan's D12).
- **Other Stage C parts and new badges:** the Rust core (#6793); HarmonyOS, Wear OS (#8554), Android's launcher.
- **Splitting `docs/protocol/protocol.md` or `docs/design/DESIGN_SYSTEM.md`:** #10613's docs decision.
