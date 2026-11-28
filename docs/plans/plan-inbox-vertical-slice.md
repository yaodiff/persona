# Plan: the Inbox as the first vertical domain slice (#11309)

**Status: decided 2026-11-22; section 6 is the record (#12841).** It is the Stage B item "Inbox as a vertical pilot" of
#10613 and follows #7891's package-split rules. S2, S3 and W2 have landed; each other step becomes its own issue and PR
and lands under section 4's rules. No step changes what a user sees (no UI, setting, default or wire change), so the
design-principles self-check does not apply.

**How the numbers were taken**, as of `origin/main` at 287db61638 (2026-11-20), re-checked at 81361c1dd1 and
13c0e93f6f (2026-11-22):
- "14 days" is `git log --since=2026-11-06T00:00:00Z` on 287db61638, counted by exact path. History starts on
  2026-10-24, so the window covers most of it, repository-wide sweeps included. Those commit counts are not re-taken.
- Open-PR facts are as of 81361c1dd1, against the 287 PRs open on 2026-11-22 (155 on 2026-11-20); the
  `docs/android/android.md` count in A1 and A2 is from the 288 open later that day. File:line references and file and
  line counts were found again by name at 13c0e93f6f: since 81361c1dd1, #12859 moved the snapshot and turn-settled
  lines in `assistant/service.go` and #12878 moved W1's literal in `scripts/test/test_check_fast_inputs.py`. A landed
  step's Change cites the code it moved where it was before the move; its Landed note says where it went. GitHub lists
  at most 100 files per PR and 24 of them reach that cap, so overlaps may be undercounted. Hunk positions are on each
  PR's own base. Line numbers drift, so each step finds its lines again by name and re-checks open PRs before it is
  pushed (section 4).
- The conflict measures in sections 4 and 6 (merge-tree and a merged `go vet` against every open PR head) were taken on
  51e333fc90 across 241 open PRs, and the timings in D3 were measured for the decisions, on the same machine. Other
  build or test speedups below are expected, not measured. Paths that do not exist yet are plain text, not code, so the
  docs path checks stay meaningful.
- Passages marked "Revised 2026-11-22 in #10613" bring the plan in line with the domain rules; the facts they state
  were re-checked on 8d3dce7b6a (open PRs as of that day), the rest of the passage's numbers are as above. The client
  steps' gates, the Apple adapter's inputs and I1's CI lines were corrected after that (#13052), with their facts
  re-checked on 134686adc4; the way the Apple adapter answers and step A4 followed, re-checked on 600d6a1af8.

## 1. Why

The Inbox is the busiest single domain on every side, and most of its changes also edit lists every other feature edits.

- **Server.** The Inbox code in `server/internal/assistant` is 60 non-test files (`inbox*.go`, `supervise_inbox.go`,
  `tasks_inbox.go`) with 17,135 lines, plus 145 test files with 36,984 lines. 149 commits touched the non-test files in
  14 days; of those, 26 also touched `assistantwire/inbox.go`, 16 `assistant/service.go`, 13
  `server/internal/hub/testdata/route_scopes.txt`, 12 the `mcp_tools.assistant` golden, 11 `extwire/scopes.go`, and
  8 each `assistant/tools.go` and `assistant/bus.go`.
- **Registration is spread over central files.** `assistant/service.go` has the fields `inboxExt` (77) and `inbox`
  (287), `s.inbox.lang` (350), `s.inboxRoutes()` (416), `inboxReport` in the snapshot (952-953) and the turn-settled
  hook (1045); `assistant/bus.go` the needs-you tell after each handshake (292-298) and the dispatch to `inboxRequest`
  (696); `assistant/tools.go` five names in `toolNames` (38-39) and five `runTool` cases (221-230); `extwire/scopes.go`
  the 31 Inbox scope entries (22-52); `assistant/handler.go` four Inbox methods on `Handler` (99-105), called only from
  `assistant/inbox_bus.go`.
- **Ten files outside the Inbox core** (`inbox*.go` less the task bridge's files) **reach its in-memory state**
  (`inboxBook`, `s.inbox`). Five write no item: `assistant/context.go` (`followingInboxItems`, 529-532),
  `assistant/tasks_draft_decisions.go` (88-89, 111-118), `assistant/service.go` (350), and through `b := &s.inbox`
  `assistant/origins.go` (`gatherOriginEvidence`, 219) and `assistant/inbox_workbench.go` (`workbenchItem`; it was in `feishu_workbench.go` until that moved into the lark module). Five
  are the task bridge's (section 2), and they also write items under the book's lock: `assistant/supervise_inbox.go`
  (51, 154; writes at 179-186), `assistant/tasks_inbox.go` (27, 65, 85; writes at 86-96; `checkTaskInbox`, a method on
  `*inboxBook`, at 43), `assistant/inbox_task_links.go` (211, 281; writes at 232-235, 247-252 and 305-313),
  `assistant/inbox_task_recheck.go` (37; writes at 56-58 and 110-115) and `assistant/inbox_todo.go` (91, 281;
  `fileTodo`, a writing method on `*inboxBook`, at 29, which the core's `assistant/inbox_bulk.go:212` calls). Only the
  first two of those are outside `inbox*.go`. `assistant/briefing.go` only calls `s.followingInboxItems`, and the lock
  in `assistant/inbox_no_output.go` is the tasks' event ledger (`s.tasks.turns`), not the book. 124 test files in the
  package touch `.inbox.mu` (1,245 times) or `.inbox.items` (589 times).
- **Build cost** (#10613): a one-line edit in `internal/assistant` costs 58 s to compile its test binary, and check-fast
  tests the whole `./internal/assistant/...` subtree for any change under it. That selection stays (#10615; D3).
- **Web.** `web/src/lib/inbox.ts` (1,607 lines since W2, 1,721 before) is on every first screen
  (`web/src/lib/inbox-page.boundary.test.ts`). Of the 134 commits that touched `web/src/lib/inbox*` or
  `web/src/views/inbox` in 14 days, 47 edited it and 6 raised `FIRST_SCREEN_BUDGET` in `web/src/startup-preload.ts`.
  26 source files outside those paths import it (27 before W2), many for helpers that are not the Inbox's.
- **Android.** `android/core/data/build.gradle.kts` depends on `:feature:inbox` (a core module on a feature module), and
  7 core:data main files use `persona.feature.inbox`; feature:chat and feature:knowledge depend on it too.
  `android/feature/inbox` had 102 commits, `RealInboxRepository.kt` 32 and the feature's `InboxRepository.kt` 27.
- **Apple.** `apple/Persona/Views/Inbox` is 73 Swift files (21,351 lines, 118 commits). Of the Inbox's logic only the
  Lark change gate runs under `swift test` (`apple/Packages/PersonaKit/Sources/PersonaKit/LarkChangeGate.swift`).
- **Wire.** The item type is written by hand in Go, Swift, TypeScript and Kotlin; HarmonyOS reads keys one by one, and
  `scripts/mock/mock_hub.py` writes the JSON by hand. A maintainer ended generating it in #9117 (section 3.2).

Per-domain registration was proposed as a stage of #7891 (comment of 2026-11-18); one domain, measured, shows whether
it pays off before others repeat it.

## 2. What the Inbox domain owns

Ownership follows `docs/assistant/inbox.md` ("Ownership: the manager owns the Inbox") and the rule that the hub is only
a communication center. "Inbox" is the user-facing Inbox, not #7563's durable inbox for assistant rounds.

**Owned (the manager's assistant process):**
- Items and their state (admission, triage, rules, policies, mutes, folders, grouping, events and the handling chain,
  following up, done and history, merge, parents, bulk actions, handled elsewhere); the AI twin (judging, processing
  conversations, drafts, suggestions, message cards, hand-off with write-back); the assistant's own rows; translation,
  with `/api/v1/translate`, `/translate/message` and `/translate/mail` (D11); the tools inbox_suggest, inbox_draft,
  inbox_folders, inbox_move and inbox_post.
- The 34 routes in `assistantwire.InboxRoutes`, the 8 `inbox.*` bus methods, the events inbox.changed and
  inbox.needs_you, the table `inbox_items`, and the settings keys inbox_mutes, inbox_policies, inbox_folders and
  inbox_mail_baselines.
- The model purposes inbox.triage, inbox.judge, inbox.draft, inbox.agent and translate; the prompts in
  `server/internal/prompts/inbox` and `server/internal/prompts/mcp/inbox`; the locales assistant-inbox* and
  inbox-errors.
- On clients: the list store with its delta and cursor, the section, filter, bar and row rules pinned by
  `spec/fixtures/inbox`, and the Inbox pages, the Lark chat pages among them as the Inbox's Lark sub-area (D9).

**Not owned:**
- **Sources.** Mail, connectors and subscriptions belong to `server/internal/extmgr` and `server/internal/subscriptions`
  (send-once in `sent_messages`, the answer to inbox.config) and reach the Inbox only through inbox.collected and the
  AppInbox calls. No step moves their code, so `docs/design/proposals/inbox-sources-migration.md` ("Order") goes on as
  designed: the issues of P1-P5, M1 and M2 are closed; M3 (Lark) and M4 (mail) remain.
- **The hub**, which no step changes. It relays the routes, merges, pages and localizes lists, merges deltas, caches
  answers, and keeps reply and hand-off receipts and the roster's needs-you count. `hub/bus_forward.go` picks Inbox
  answers by path (`strings.HasPrefix(r.URL.Path, "/api/v1/inbox")` at 245, with `/api/v1/translate` beside it);
  comments in `hub/inbox_answers.go`, `hub/inbox_delta.go` and `hub/inbox_forms.go` name it. The answer-only regexes in
  `hub/devicetokens.go` are a security boundary and stay hand-edited.
- **The task bridge** (`supervise_inbox.go`, `tasks_inbox.go`, `inbox_no_output.go`, `inbox_task_*.go`,
  `inbox_todo.go`): To-dos code that consumes Inbox events and writes task links onto items. It stays with tasks (D5).
- **In-process readers:** the Lark workbench, the briefing, the main prompt's following section, the knowledge base.

## 3. Target on each side

### 3.1 Server

New packages, all under the manager's tree (D1; feature modules live outside it, in server/internal/modules/<id>, as D1's 2026-11-23 amendment says): server/internal/assistant/kernel/seam (the registration seam, D2; its parent package kernel hosts the feature modules, `docs/architecture/core-and-modules.md`),
server/internal/assistant/inbox/item (the item's lifecycle core) with its adapter inbox/item/itemadapt (the stored
item, S8) and, only if D3's gates pass, server/internal/assistant/inbox (the stateful module behind inbox.Host).
Archguard's `managerPart` and the hub's forbidden prefixes cover them with no new entries. Package assistant keeps the
glue (the Inbox module, the Host adapter, the forwarders, the task bridge); extwire gets the Inbox scopes in a file of
their own. Each new package states its layer in its package comment (`//persona:layer core`, `adapter`,
`application seam` and so on). What each layer may hold and import is not restated here: it is the domain rules, the
"Domain model" section that #12899's first PR adds to `docs/server/architecture.md`.

The kernel is a typed registration seam (D2). `Kernel` is an opaque struct with unexported fields, and only
`(*Registry).Register` makes one. Its members are either the few services every module uses (its routes, the access
check, body errors and publishing events, which moved from S4 to #15222) or slots that each replace a central-list
line; never a domain method and never a store. A module takes its own dependencies through its own constructor, from
the composition root in assistant.New. saved is its first user, so it starts with the services and no slots:

```go
type Module interface {
	Name() ModuleName  // from NewModuleName("saved")
	Register(k Kernel) // once, from assistant.New, where the module's lines are today
}

// In assistant.New; the ports are package assistant's own, typed:
reg := kernel.NewRegistry(routes, access, bodies) // RouteTable, Access, BodyErrors; a nil port is a defect
reg.Register(saved.NewModule(store, events))       // a module's dependencies come through its constructor
reg.Seal()                                          // registering after Seal is a defect

// Services, as Kernel methods:
func (k Kernel) Route(r Route) // r from NewRoute(pattern, fn): a relayed route, in assistantwire.Routes or InboxRoutes
func (k Kernel) Full(r *http.Request) bool
func (k Kernel) BodyError(w http.ResponseWriter, err, bad error)
func (k Kernel) Publish(ctx context.Context, ev assistantwire.Event) // #15222, moved from S4: a struct per event kind

// Slots, added in steps S4 and S5.
func (k Kernel) Bus(m BusMethod) // m from NewBusMethod[P, R](name, fn): params decoded into P once
func (k Kernel) Tool(t Tool)     // t from NewTool(name, after, run), placed after its anchor
// Inside turnEnded's !meeting branch, with the ref turnEnded parsed once:
func (k Kernel) TurnSettled(fn func(ctx context.Context, ref agentref.Ref))
// Per handshake, on its own goroutine, after client.Wait:
func (k Kernel) Connected(fn func(ctx context.Context))
// Where snap.Inbox is filled today:
func (k Kernel) Snapshot(fn func(ctx context.Context, snap *assistantwire.Snapshot))
```

- **Checked where it is made.** A registration mistake is a defect (the domain kit's defect), raised at the caller's
  line at start-up by the value that holds the mistake: an empty, malformed or duplicate name (ModuleName, BusMethod,
  ToolName, a route's pattern), a pattern outside the relayed tables, a nil function, a second hook from the same module
  (hooks have no name, so they are keyed by module), anything after Seal, and any method of a zero Kernel. Route
  handlers registered through the kernel run under the one listed catch site, kernel/seam/dispatch.go: a defect there is
  reported and answered with 500, and any other panic goes on to net/http. Tables run in registration order, which is
  today's line order, and dispatch goes through the registry, which never hands out its slices.
- **Hosts stay with their modules.** `saved.Host` and `saved.Register` stay as saved's adapter: a new saved/module.go
  holds the dependencies `saved.NewModule` was given and builds `saved.Host` from them and the Kernel's methods, so
  `server/internal/assistant/saved/saved.go` is untouched. saved's own untyped Host stays inside package saved until
  its open drafts merge or idle 14 days; its typed ports then replace it (#15222). The Inbox module takes typed ports
  through its NewModule from the start, never saved's `func() any` store or map events.
- **One registry.** The same registry backs #7891's registry stage: in-package domains register routes, loops and tools
  from their own files through package-level front doors taking the same typed names, which New feeds in before the
  modules, so there is one duplicate rule and one tool placement function. `Kernel.Bus` is the seam for any domain's
  new bus methods, and each central list a step migrates gets an archguard name freeze. The kernel is a seam, not a
  domain core: it is declared `//persona:layer application seam`.
- **Hook order is part of the contract.** Each hook runs where its line runs today: the snapshot hook before
  `snap.Work` (`fleetTakeSnapshot` in `assistant/fleet_snapshot.go` reads `snap.Inbox` back), the handshake hook after
  `client.Wait`, the turn hook inside `!meeting`, the Inbox's bus methods after `conversationsRequest`; each tool's
  anchor keeps the MCP tool list and its goldens byte-identical. The Inbox has no loop in `Service.Run`, so the pilot
  does not test the loop slot the registry stage adds.
- **The item slice** is a core and its adapter (D1). inbox/item is the item's lifecycle, strict from its first line: a
  Decider whose commands are named for their meaning (marking done, a reopen that records its chain entry, clearing
  Read on new mail, undoing a filing, filing) and whose decision carries the next item and its events. itemadapt owns
  the stored item: `InboxItem` moves there unchanged as Record, json tags kept, because shared fixtures and stored blobs
  decode straight into it; Restore makes an item from a Record (a stored row that fails is quarantined, never
  defaulted) and Store writes a decision back. The order is the lifecycle core first, InboxItem's methods next, then
  the stored item (S8). `TestMovedClustersStayOut` (`server/internal/archguard/split_clusters_test.go`) fails when the
  old package declares a name the subpackage exports or gets back a file with a moved prefix, so package assistant
  forwards the moved names from one file whose name does not start with "inbox", aliases_inbox.go, and the first S8a
  PR changes the guard to accept exactly those forwarders (D4), as in
  `docs/server/architecture.md` ("Contract packages").
- **The stateful module** (S9 only) sits behind inbox.Host, small role interfaces (store, sources, notify, models,
  owner, conversations, feed, machines, privacy, tasks, knowledge) over the roughly 45 `*Service` methods defined
  outside `inbox*.go` that the core (bridge excluded) calls. Package inbox cannot import assistant, so Host speaks
  contract types and an adapter converts.
- **Other code uses the Inbox through three surfaces:** a Reader (following items, an item copy, the card-send lock,
  origin evidence), Actions for the Lark workbench, and Policy (the model-call gate, the manager's origin decision,
  agent started), which stays explicit calls, not kernel hooks. The Reader is read-only: it serves section 1's
  readers, not the bridge.
- **The task bridge** is renamed `task_inbox_*` / `todo_inbox`, sits behind an inboxTasks interface, and registers as
  the module "inbox-tasks", which owns `/inbox/todo`, `/inbox/todo/undo` and the task-link recheck route. It writes
  items (a task link and its removal, a closed task's continuation, filing for a finished task, the supervisor's
  in-memory signal watermark). While it shares package assistant with the core it keeps writing as it does today:
  under the book's lock, rechecking the item and its link if it let go of the lock since reading them; a saved write
  goes `beginWrite`, change, `settle`, unlock, then `saveWrites`; and it never takes the to-dos' lock while holding the
  book's (`assistant/inbox_task_links.go:301`). Only if S9 takes the lock out of package assistant does it get a fourth
  surface, Links: one method for each read, check or write the bridge makes under the lock today, keeping those steps
  inside the module (D5).

*Revised 2026-11-22 in #10613:* the kernel takes typed ports, typed names and contract events where it took an
untyped store, untyped events and plain strings; its turn hook takes the session identity value `agentref.Ref`, not a
wire type; registration mistakes are defects with one catch site; each package declares its layer, so no central list
of domain packages is kept; and the stored item is a record in the item core's adapter, never in the core.

### 3.2 Contracts and wire

- `assistantwire.InboxRoutes`, `server/internal/assistantwire/inbox.go` and `server/internal/connectorwire/inbox.go`
  stay.
- **No wire generation.** #9117 (stage 5 of #7891) ended it for the Inbox by maintainer decision: a generated type
  would not be a pure replacement. In #9117's words, Go's `InboxItem` is the persisted struct (about 60 fields, half
  never listed, with `omitempty`, `*time.Time` and an embedded wire type), and Swift's has a hand-written decoder with
  `Date` fields, computed properties and nested types. No step generates, and D12 keeps it that way.
- Two tests come first, so "byte-identical" is checked rather than assumed (step S2, landed): a reflection test that
  fills every exported `InboxItem` field and checks the keys the unexported `InboxItem.listed()` emits, and a
  Go-produced "every block" case in `spec/fixtures/wire/inbox.json`.

### 3.3 Clients

Each client's domain module follows the domain rules' client part, which the client domain guard adds after #12899: a
later step of #10613 that finds the Swift and Kotlin domain modules by name and checks them (the web's rules come with
W1). No client step adds a domain module, an adapter or a store package before it lands, so W1, I1, A2 and A4 wait
for it.

- **Web.** A feature folder, web/src/features/inbox (D8a), takes the section bar's rules first, into
  features/inbox/domain with features/inbox/adapt beside it (W1), then the store and API client from
  `web/src/lib/inbox.ts`, then the page-only modules; `web/src/views/inbox` stays the route-level UI, and helpers that
  are not the Inbox's leave `lib/inbox.ts` (W2). One boundary test for every feature, web/src/features/boundary.test.ts,
  generalizes `web/src/lib/inbox-page.boundary.test.ts`: code outside a feature imports only its listed entries; the
  domain folder imports nothing outside itself and the domain kit, and only the feature's adapt folder reaches into it;
  there is no index barrel; page-only modules are imported only from the feature's lazy page; new feature namespaces
  stay out of `web/src/i18n/startup-namespaces.json` unless a first-screen exception reports its bytes; re-export shims
  are on a shrink-only list. These boundaries, not a per-domain byte slice, hold the first screen, so one
  `FIRST_SCREEN_BUDGET` stays (D8b). New Inbox mock endpoints go to an Inbox module in `scripts/mock/mock_domains.py`
  (#11016), not `scripts/mock/mock_hub.py`. Later web domains, #10613's web slices among them, use
  web/src/features/<domain>, with domain and adapt folders for their rules.
- **Android.** The Inbox's UI-free home is one JVM module, :core:inbox (`persona.jvm.library` with serialization, on
  :core:contracts; :core:domain joins at A3), not a package in core:contracts, where any edit reruns nearly every unit
  test (D10a). Its build file sets `explicitApi()` and `allWarningsAsErrors`. It holds persona.core.inbox.store (A2's
  `@Serializable` reply and hand-off records, moved unchanged) and persona.core.inbox.adapt. The Inbox's domain is the
  Gradle subproject :core:inbox:domain (persona.core.inbox.domain, depending only on the domain kit's
  :core:domainkit), created when the bar's rules move (A4): Kotlin's `internal` is module-wide, so only a module
  boundary keeps the adapter out of the domain. Also in the module: A3's page-facing repository and A4's bar facade,
  outside those packages, and later a strangler step that moves the rest of `persona.core.domain.inbox` in (458 lines,
  116 of them the bar's, which A4 takes), with feature:assistant gaining a direct dependency. Because they share the
  module, the rule on what the adapter returns reads its internal declarations as well as its public ones (A4). The
  transports core:hub implements, the wire InboxItem and the legacy facade `api/InboxRepository` stay in core:contracts
  until measured. Domain types live only in :core:inbox:domain and are never `@Serializable`; persisted shapes stay
  `@Serializable` records in the store package. @StringRes members become keys, and Compose-backed members stay in
  feature:inbox. Then core:data stops depending on feature:inbox, and a source scan keeps core modules off feature
  modules.
- **Apple.** A Swift package, apple/Packages/PersonaInbox, made once the client domain guard is in, holds UI-free Inbox
  logic under `swift test` (D10b). Its Domain target, PersonaInboxDomain, imports no Foundation and depends only on
  PersonaDomainKit (a new package with the domain kit's Swift values); PersonaInboxAdapt, on the domain and
  PersonaWire, builds the domain's values. `apple/Packages/PersonaWire` is unchanged, and no package can see Shared's
  types: `apple/Shared` is a source folder each app target compiles (`apple/project.yml`), not a module. So an
  app-side facade, compiled with Shared, hands the adapter plain values, such as the section words (`InboxItem.Tier`'s
  raw values) and the counts InboxProjection keeps; the adapter parses them into the domain's InboxSection and
  InboxChoice. Answers never come back as plain values: the adapter's public functions return nothing or a domain
  value (a parse's outcome), never a word, a number, a tuple or a struct of its own. For each answer the facade passes
  one closure per case of the domain's visitor, the adapter visits and calls each with that case's section word, count
  or emptiness word, and the facade builds the app's `InboxItem.Tier`, InboxChoice and `InboxPage.EmptyKind` from the
  calls without reading anything back (the domain rules' way of acting on a decision). `InboxItem.Tier`, the item's
  triage fallback (the hub's status, else the tier) and the tier's localized title stay in Shared, as do
  `apple/Shared/InboxModels.swift`, `InboxEvents.swift` and `InboxNow.swift`: the watch compiles them, and the item's
  Codable form is the StateCache record and the WatchSync payload. The item moves only with two-way compatibility tests
  (an old blob decodes in the new build, and the reverse). The views stay in `apple/Persona/Views/Inbox` until
  PersonaDesign (#10613), then move to apple/Persona/Features/Inbox.
- **Lark (D9).** The Lark chat pages stay in the Inbox as its Lark sub-area on every client (`LarkThread.tsx` and the
  rest on the web, 47 files in `apple/Persona/Views/Inbox/Lark`, `LarkScreen.kt` on Android): Messages reuses the
  Inbox's chat view (`docs/extensions/lark-migration.md`, "Entry point"), and the two change together. This is
  re-decided when source migration M3 lands or the pages become extension UI.
  - Web, a first byte-neutral PR separate from W1: `web/src/views/inbox/lark.svg`, the only brand mark outside
    `web/src/ui/brands`, moves there (`web/src/ui/BrandMark.tsx` and `web/src/views/inbox/InboxParts.tsx` import it),
    and a boundary test lists every outside importer of `web/src/views/inbox`: the lazy route in `web/src/App.tsx`,
    `web/src/modules/lark/Messages.tsx`'s LarkThread (the designed exception), the 7 importers of InboxParts,
    `web/src/modules/recording/RecordingHandoff.tsx` (HandOffTargets), `web/src/lib/inbox-actions.test.ts` and
    `web/src/views/extensions/ExtensionSlots.managers.test.tsx`. The Inbox's Chip moves to `web/src/ui` only with a
    `docs/design/DESIGN_SYSTEM.md` row; its Count stays, since converging on `web/src/ui/CountBadge.tsx` changes pixels
    and is a UI PR of its own (DS-21) with light and dark screenshots.
  - Android: A1's scan pins `ChatLinkHost.kt`, `ChatLinks.kt` and `MeetingHandoff.kt`; the pure parsing in
    `LarkDocument.kt` moves to :core:inbox after A2.
  - Apple: only the outside users of the Lark types are listed, with no folder move before PersonaDesign; PersonaKit's
    `LarkChangeGate` is Inbox-Lark logic.
- **HarmonyOS.** Unchanged: `harmony/entry/src/main/ets/models/InboxWorkspace.ets` reads raw keys and has no typed item
  to replace.

*Revised 2026-11-22 in #10613:* each client's Inbox rules are a domain module beside an adapter, so the web's model
folder becomes domain and adapt folders, Android's model package becomes the :core:inbox:domain subproject (A4) beside
store and adapt packages, and the tier stays in Shared rather than moving into PersonaWire; since no package can see
Shared, an app-side facade hands the Apple adapter plain values and takes answers only through closures the adapter
calls; the Android adapter's results are checked on its internal declarations too, since its module also holds the
repository and the facade; and the client steps wait for the client domain guard.

## 4. Steps

**Rules for every step** (#7891):
- **Pure moves, one cluster per PR**, each with its own issue linking #10613 (and #7891 for server moves). Wire JSON,
  prompt and MCP goldens, and `route_scopes.txt` stay byte-identical.
- **Landing beside open PRs** (X6, decided 2026-11-22). Before each push of a pure-move or seam PR: run `git merge-tree`
  against every open PR head that touches the files and count new conflict hunks against main; for Go, run a merged
  `go vet` of the affected package for every such head and compare it with main merged with the same head. Activity is
  judged by the head commit's age (7 days or less is active), never by updatedAt, which automation bumps. A line that
  fails stays where it is and moves in a follow-up. Docs moves use the same overlap measure (no open-PR edit within 5
  lines), re-run on the PR's day, and list the checked PRs in the body. CLAUDE.md's 24-hour rule for claims is a
  different rule and stays.
- **Moved clusters stay out:** a `movedClusters` row for every moved Go cluster. Path-keyed baselines (fonts, tokens,
  isolation lint, doc paths) move with their files at identical counts and are never raised.
- **Evidence:** Go with GOFLAGS=-trimpath and `-p 4`, Gradle with `--max-workers=4`, no booted simulator for package
  tests. The PR says what was run and what was not. Rollback is a revert of the PR unless the table or the step says
  more. The tracks (server, web, Android, Apple) are independent; the table's open PRs are as of 2026-11-22.
- **Judged against main.** `TestHubAnswersOnlyItsOwnRoutes` passes on main (81361c1dd1), so a step meets it in full.
  Goldens are judged against main's own result: a golden failure the step's base also has is reported, not fixed
  (`TestGoldenMCPTools` failed on main over `task_plan.golden` until #12595 on 2026-11-21, and passes at 81361c1dd1). A
  base that does not build is not evidence: the hub build broke with #12466 until #12496 reverted it.

| Step | Change | Conflict cost | Gate |
| --- | --- | --- | --- |
| S1 | Kernel package; saved registers through it | Low: 34 open PRs touch `assistant/service.go`; S1 and S4 together made no open PR conflict more (51e333fc90). Of three drafts on `assistant/saved`, only #12113 edits `assistant/saved/saved.go`, inside its Register, which S1 leaves alone | after #12899's domain rules (Windows) |
| S2 | Guards and contract tests | None: new files | landed (#11650) |
| S3 | Seams in quiet files | None: the four files had no open PR | landed (#11536) |
| S4 | Routes, bus methods, hooks, snapshot through the kernel | Low: 34 open PRs on `assistant/service.go` and 12 on `assistant/bus.go`, none on `assistant/handler.go`; with S1, no open PR conflicts more, and merged with #12508 and with #6539 the package vets clean. Since the Inbox moved into the assistant (a8b9516e1a, 2026-11-04) only two commits changed the lines it moves (8833b4d530, 5a3a8e5025), so it saves few conflicts itself: it is the kernel's first full user for the registry stage | after S1 and agentref, the shared session identity that follows #12899; no window and no wait for the registry stage (Windows) |
| S5 | The five tools through the kernel | Medium: 15 open PRs on `assistant/tools.go`; #6539 and #9782 append next to inbox_post (39 and 230), and S5 adds one conflicted file to each | after #6539 and #9782 (Windows), with or after the registry stage's tool placement (#7891) |
| S6 | Inbox scopes in their own extwire file | Low: 4 open PRs on `extwire/scopes.go`, #4512 with three Inbox scopes; the move made no open PR conflict more | now, as one PR with the registry stage's scope step (D7) |
| S7 | The task bridge renamed; the core calls it through one interface | Medium: #6206 (a draft), #9744, #10808, #10894, #12508 and #12524 (a repair of #9744) edit bridge files, all task-side work | its own hunk gate under X6 (D5) |
| S8 | The item slice: S8b, the lifecycle core and the stored types' methods; S8a, the stored item as a record in the core's adapter | High: `assistant/inbox.go` has 5 open PRs (#4512, #9790 with its repairs #10580 and #10587, #12508); 12 open PRs touch non-test `inbox*.go`, 11 of them with heads from the last 7 days; the text move broke 7 open PRs (51e333fc90). Rollback: later steps first | under D3's gates, S8b first and S8a after it (Windows) |
| S9 | The stateful module | Very high: every open server Inbox PR, and 124 test files rewritten. Rollback: before later PRs add files to the package | not planned; only if every D3 gate passes |
| W1 | Web feature folder, starting with the section bar | None: the moved files and the edited lines have no open PR; `web/src/startup-preload.ts` (16 open PRs) and the startup namespaces (26) are untouched | after the client domain guard (section 3.3; D8a, Windows) |
| W2 | Non-Inbox helpers leave `lib/inbox.ts` | Low: its open PRs miss the moved ranges | landed (#11538) |
| A1 | Android module-edge ratchet | Low: a new test file, and a note in `docs/android/android.md`, which 11 open PRs touch, none in its "Modules and code" section | now (D10a) |
| A2 | `:core:inbox` with the pure reply and hand-off types | Low: no open PR on the moved types' file (#10396, a draft, touches only `InboxTopBar.kt` and strings in the feature), but it is hot; none of the 11 open PRs on `docs/android/android.md` edits its module table | after A1 and the client domain guard (section 3.3; Windows), landed fast |
| A3 | The page-facing repository into `:core:inbox`; core:data off feature:inbox | Medium: `RealInboxRepository.kt` (#12645), the feature's `InboxRepository.kt` and the app's `InboxModule.kt` (#11918, #12509) are hot (32, 27, 21 commits); about 70 test files implement the repository, one of them (`InboxShowcaseShotsTest.kt`) in #9823 and its repairs #11168 and #11179 | after A2, when `RealInboxRepository.kt` and `InboxModule.kt` pass X6 |
| A4 | The bar's rules into `:core:inbox:domain`, on `:core:domainkit` | Low: `InboxBar.kt` in :core:domain has no open PR, and of its callers only `InboxScreen.kt` has one (#12896); `android/settings.gradle.kts` has none | after A2, the client domain guard and #13024 (an unused result is a compile error in every Android module), while `InboxBar.kt` has no open PR (section 3.3; Windows) |
| I1 | The PersonaInbox package, on PersonaDomainKit | Low: `apple/Shared/InboxModels.swift` (#4512, #7495) and `InboxView.swift` (#4512, #7495, #11282) are not edited, the latter behind an app-side facade; `InboxBar.swift` has no open PR; `apple/project.yml` (2 open PRs) only gains entries, and `scripts/ci/apple.sh` (2 open drafts, #12316 and #1756) two lines | after the client domain guard (section 3.3; D10b, Windows) |

### S1. Kernel package, with saved as its first user
- **Change:** server/internal/assistant/kernel, declared `//persona:layer application seam` and landing after #12899's
  domain rules, with Module, Registry, the opaque Kernel, ModuleName and Route, the services Route, Full and BodyError,
  and no slots yet (section 3.1). `saved.Register(saved.Host{…})` (`assistant/service.go:399`) becomes
  `reg.Register(saved.NewModule(…))` at the same spot, between `NewRegistry` and `Seal`, passing the store and event
  functions saved takes today. saved.Host stays as saved's adapter, built in the new saved/module.go. Archguard gets
  the list of production files that call `recover()` and the list of places that may defer the kernel's catch, each line
  with its reason. Unit tests show that a nil port, an empty, invalid or duplicate module name, a duplicate route, a
  pattern outside the relayed tables, a nil handler, registering after Seal and a zero Kernel's method each raise a
  defect, and that a defect inside a registered route answers 500 while the process goes on.
- **Done when:** `go test -p 4 ./internal/assistant/kernel/... ./internal/assistant/saved/... ./internal/archguard`, the
  saved tests, `TestRoutesAreTheWireOnes` and
  `go test ./internal/hub -run 'TestRouteScopes|TestHubAnswersOnlyItsOwnRoutes'` pass with `route_scopes.txt` unchanged;
  the kernel passes its domain-rules hook; `go vet ./internal/assistant/...` is clean; X6's measures are in the PR.
- *Revised 2026-11-22 in #10613:* typed ports and names, defects and the catch site in place of an untyped Infra and
  panics, and a layer declaration in place of a line in a domain-package list.
- *Built in #15182:* the seam is package `seam` under server/internal/assistant/kernel, because package kernel already
  hosts the feature modules and declares no layer yet, and NewRegistry also takes a Defects port, which the catch site
  reports to (`docs/server/architecture.md` "Validated and sealed").

### S2. Guards and contract tests
- **Change:** an archguard parse-only test: the book (`s.inbox.*` selectors, `b := &s.inbox`, methods on `*inboxBook`)
  is reached only from the core (`inbox*.go` less the bridge's files), from the bridge's five files in section 1, named
  in the guard so that S7's renames move their entries and add none, and from a shrink-only allowlist of section 1's
  five readers; and `toolNames` and `extwire/scopes.go` gain no Inbox entry beyond main's and those open PRs add
  (#4512's three scopes, #6539's inbox_replay); the hint names the seam. Also the reflection test of `listed()`
  (section 3.2), the every-block case for `TestWireSamples` (the existing case's bytes unchanged), and an agreement
  test between a per-route answer-only table and `hub/devicetokens.go`.
- **Done when:** the new tests pass; a planted `s.inbox.items` read in a non-Inbox file fails the guard (shown in the
  PR); the guard passes on a local `git merge-tree` of each open Inbox PR's head, commands in the PR body; Android's
  `WireFixturesTest` still decodes `spec/fixtures/wire/inbox.json`.
- **Landed (#11650):** `server/internal/archguard/inbox_slice_test.go`. `TestInboxBookStaysInTheInbox` parses the
  package's production files and counts each place that reaches the book: a selector through the Service's `inbox`
  field, its address, the name `inboxBook`, or a call of a function that returns one. Re-derived on 2cba124967 (and
  unchanged at 44e9dadce0), it is as section 1 says (S3 moved none of it): the 52 core files, the bridge's five files by
  name, and the five readers with pinned counts (`context.go` 3, `feishu_workbench.go` 1, `origins.go` 1, `service.go`
  2, `tasks_draft_decisions.go` 5). The bridge's other three files reach nothing. `TestCentralListsGainNoInboxEntry`
  treats as the Inbox's a tool named `inbox_*` or described in `server/internal/prompts/mcp/inbox`, and a route under
  `/api/v1/inbox` or `/api/v1/translate` or with an Inbox scope. It allows main's five tools and 31 scopes, plus what
  open PRs add: `inbox_replay` (#6539), `message_send` (#9782, described there, staying in `toolNames` at S5) and
  #4512's three scopes. Two open PRs add files that reach the book, allowed apart with their counts until they land or
  close: `outbound_messages.go` (#9782, a reader) and `tasks_inbox_existing.go` (#9744, the bridge's). In
  `server/internal/assistant/inbox_listed_keys_test.go`, `TestListedKeys` fills every exported field by reflection and
  pins the 43 key paths `listed()` leaves out, with `tier_was` (#4512) and `reply_approval` (#9790, #10580, #10587)
  added ahead of their PRs. `inbox_every_block` follows `inbox_list` in `spec/fixtures/wire/inbox.json`, whose bytes
  stay unchanged, and Android's `WireFixturesTest` decodes it. Deferred: the agreement test with `hub/devicetokens.go`,
  because no per-route answer-only table exists and D7 adds none; a check that `inbox_every_block` sets every block,
  which would make #4512 (a new `progress` block) edit the sample; and the two Reader methods S3 deferred, which are
  production code for a step of their own.

### S3. Seams in quiet files
- **Change:** pure moves in package assistant, in files no open PR touches. `clipRunesTo`
  (`assistant/inbox_merge.go:50`, also used by the urgent-call files) and `cardLocks` (`assistant/inbox_bus.go:144`,
  also used by `assistant/tasks.go`) move to neutral files. The Inbox half of `assistant/extension_context.go`
  (`inboxExtensionCache`, `inboxExtensionContext`) moves to an inbox-prefixed file; `projectExtensionContext` stays for
  `assistant/todo_outcome.go:542`. `assistant/tasks_draft_decisions.go` reads the book through the first two Reader
  methods (the card-send lock, an item copy) and leaves the allowlist. Deferred until their PRs land: `replyInDoubt`
  (`assistant/inbox_triage.go:470`, 7 open PRs), `modelPaused` and the purpose constants (`assistant/inbox_hub.go`, 4),
  `assistant/context.go` (7), `assistant/inbox_workbench.go` (formerly `feishu_workbench.go`, 4), `assistant/origins.go` (#2049, idle since
  2026-11-09).
- **Done when:** `go build ./...` succeeds; the full `./internal/assistant` package passes once, with its duration, and
  `./internal/archguard` passes; the allowlist is shorter; `scripts/test/test_doc_go_paths.py` passes.
- **Landed (#11536):** byte-for-byte moves of `clipRunesTo` to `assistant/text.go`, `cardLocks` to
  `assistant/card_locks.go`, and the Inbox half of `assistant/extension_context.go` (its cache, `inboxExtensionContext`
  and the never-quote check, which only the Inbox's drafts use) to `assistant/inbox_extension_context.go`; its tests
  stay in `assistant/extension_context_test.go`, which #10891 and #10915 edit. Deferred: the two Reader methods, because
  S2's allowlist was not on main yet and `listed()` blanks the `Last` that `decideDraft` reads, so the item copy's shape
  is chosen with S2; and the comments in `assistant/service.go` (34 open PRs) and `assistant/inbox_triage.go` (7) that
  still name `extension_context.go`, whose header now points to the Inbox file.

### S4. Routes, bus methods, hooks and snapshot through the kernel
- **Change:** the kernel gains Bus, TurnSettled, Connected, Snapshot and Publish (Publish moved to #15222, as built
  below); it lands after agentref, the shared session identity after #12899. An Inbox module in package assistant registers through the sealed kernel the unchanged
  `inboxRoutes` body, the 8 bus methods (`inboxRequest` split per method, each a `NewBusMethod` over the existing
  assistantwire request and reply types), `inboxAgentTurnSettled` (its hook takes `agentref.Ref`: turnEnded parses the
  ref once, and a turn whose ids do not parse calls no hook and is logged, since that is bad input, not a defect), the
  needs-you tell and `inboxReport`, each at its current place (section 3.1); nothing registers after Seal. The Inbox's
  events become one assistantwire contract struct per kind, published through the kernel, with a golden per kind
  proving the JSON byte-identical to today's. Where the Inbox's older code still needs strings it gets the wire value
  from agentref's adapter, and those call sites are a count that only falls. 43 lines in 20 test files build
  `&Service{}` without New, so any table those tests must see stays package-level, registered from init(), and the PR
  names such cases. The four Inbox methods leave `Handler`. `s.inbox.lang` stays while an open PR edits the line above
  it (the #8584 repair PRs that did, #10564, #10593, #10599 and #10920, closed unmerged on 2026-11-22; X6 re-measures on
  the day). A guard then allows no Inbox line in `assistant/bus.go` or `assistant/handler.go`, and in
  `assistant/service.go` only the two fields, the register line and `s.inbox.lang`.
- **Done when:** `go test ./internal/assistant -run 'TestRoutesAreTheWireOnes|Kernel|Bus|Inbox|Snapshot'`, then the
  full package, pass; `go test ./internal/hub -run 'TestRouteScopes|TestGoldenMCPTools|TestHubAnswersOnlyItsOwnRoutes'`
  passes with every golden unchanged; `./internal/archguard` passes; X6's measures are in the PR.
- *Revised 2026-11-22 in #10613:* the turn hook takes `agentref.Ref`, bus methods and events are typed, and the repair
  PRs' closing is recorded; agentref comes after #12899, not with it.
- *Built in #15418:* the slots are `Kernel.Bus` (values from `seam.NewBusMethod`), `Kernel.TurnSettled`,
  `Kernel.Connected` and `Kernel.Snapshot` (`docs/server/architecture.md` "Validated and sealed"), and the Inbox is the
  module "inbox" (`server/internal/assistant/inbox_module.go`), registered where `s.inboxRoutes()` was; its route
  functions stay as they were and register through its Kernel (`routesThrough`). The bus methods' params and answers
  that had no type became `server/internal/assistantwire/inbox_bus.go`, whose JSON a test compares with the maps the
  switch built. Two parts of the text above did not hold on main, so they did not land: the Inbox in package
  `assistant` builds no `map[string]any` app event (`inbox.changed` and `inbox.needs_you` already carry assistantwire
  structs, and the extension manager builds the apps' `{"type":"inbox"}`), so `Kernel.Publish`, `assistantwire.Event`
  and the golden per event kind moved to their first user, saved's events (#15222); and `TestDomainLayers` with a
  base refuses a count line the merge base lacks, so no `app-bridge-call` count starts: the Inbox's hook matches an open item's Agent link with `agentref.Ref.IsSame`
  and goes on with that link's ids. None of the 57 lines in 26 test files that build `&Service{}` without `New`
  reaches a path that reads the kernel (`Service.snapshot`, `BusHub.request`, `BusHub.onHello`, `turnEnded`), so
  nothing stays package-level. A bus method's name must be one a domain lists as a module's (`addModuleMethods`,
  `server/internal/assistantwire/module_methods.go`), so a name the central dispatch answers is a defect at
  `NewBusMethod`; the seam cannot see the central switch's cases at run time without a second list kept by hand, so
  that no listed name is one of them is archguard's `TestModuleBusMethodsAreNoCentralCase`, which reads the cases of
  `BusHub.request`, of the functions it hands the name to and of the extension manager's and the devices' `Request`.
  The Inbox's own guard is archguard's `TestCentralFilesNameNoInboxCode`: `service.go` keeps, besides the two fields,
  the register line and `s.inbox.lang`, the Inbox names of `Service.call` and the conversation views, in its
  shrink-only list.

### S5. The Inbox's tools through the kernel
- **Change:** the five tools register as one block after ask_escalate, through the tool placement the registry stage
  shares with `Kernel.Tool`, and leave `toolNames` (`assistant/tools.go:38-39`) and `runTool` (221-230);
  `toolInboxDraft` moves out of `assistant/tools_apps.go`. The model-call gate (`assistant/tools.go:81`),
  `inboxAgentStarted` after agent_start and the read-only lists in `assistant/inbox_policy.go` stay. A rebase note for
  #6539 and #9782: inbox_replay registers with the Inbox, message_send stays in `toolNames`. The order the models see
  does not change, because the block is inserted right after its anchor.
- **Done when:** `go test ./internal/hub -run TestGoldenMCPTools` passes with every `mcp_tools.*.golden` unchanged, and
  `go test ./internal/assistant -run 'Tool|InboxSuggest|InboxDraft|InboxFolders|InboxPost|InboxPolicy'` and
  `go test ./internal/node -run TestInbox` (the node's Inbox model policy) pass.

### S6. Inbox scopes in their own extwire file
- **Change:** the 31 entries at `extwire/scopes.go:22-52` move to a new extwire file and join `RouteScopes` from
  `init()` through addRouteScopes (the pattern `assistantwire/board.go`, `assistantwire/models.go` and
  `assistantwire/agents.go` use for `Routes`); a duplicate key panics. It is one PR with the registry stage's scope
  step, which gives scopes.go a name freeze (D7). The translate routes stay absent, so they keep needing full
  credentials.
- **Done when:** `go test ./internal/extwire/... ./internal/extmgr -run Scope` and
  `go test ./internal/hub -run TestRouteScopes` pass with `route_scopes.txt` byte-identical, a test shows the
  duplicate-key panic, and no `/api/v1/inbox` line is left in `extwire/scopes.go`.

### S7. The task bridge, named as such, called through one interface
- **Change:** `git mv` the bridge files (`inbox_no_output.go`, `inbox_task_*.go`, `inbox_todo.go`, their tests) to
  task_inbox_* and todo_inbox names, so the inbox prefix means the core only. The core calls the bridge's entry points
  only through inboxTasks (confirmTodo, linkInboxTaskURLs, scopeInboxTasks, superviseInbox), and the bridge registers
  as "inbox-tasks", so `assistant/inbox_api.go` stops calling inboxTodoRoutes and inboxTaskLinkRoutes. The other
  bridge code the core calls today moves to core files by a pure move: the item helpers `inboxTaskLinkEligible`,
  `inboxScopeOf` (with its type), `judgeEvidence`, `receivedSince` and `checkSuggestedDue`, and `fileTodo`, the book
  method `assistant/inbox_bulk.go:212` calls; `taskOpen` moves to a tasks file. The bridge's own reads and writes of
  the book stay as they are (section 3.1): the Reader is read-only, so no surface is added here, and Links come only
  with S9 (D5).
- **Done when:** `ls server/internal/assistant/inbox*.go` lists only core files; an AST guard finds no call from the
  core to a bridge function outside inboxTasks; S2's guard passes with the bridge's entries renamed, none added, and the
  reader allowlist unchanged; the Inbox and task tests, then the full package, pass; the docs naming the files are
  updated and `scripts/test/test_doc_go_paths.py` passes.

### S8. The item slice
Every part waits for D3's gates, in this order.
- **S8b, first: the lifecycle core and the stored types' methods.** Its first PR creates
  server/internal/assistant/inbox/item, declared `//persona:layer core` and strict from its first line (section 3.1):
  the item's id, a sealed state (open; done with its time and actor; filed with its folder), commands and events, and a
  Decide that returns the next item with its events. Go cannot define methods on an alias of a non-local type, so
  package assistant could not keep calling the stored types' methods after a move, and they leave first: InboxItem's 11
  (open, tiered, listed, Listed, arrived, arrivedAfter and done in `assistant/inbox.go`; status and handled in
  `assistant/inbox_events.go`; unfiled in `assistant/inbox_folders.go`; nowKey in `assistant/inbox_follow.go`), and
  those of `InboxAgent` and `InboxMail`, become core behaviour or application functions, one file per PR when its file
  is quiet (X6).
- **S8a, once the stored types have no methods:** the stored item moves unchanged, json tags kept, to
  inbox/item/itemadapt, declared `//persona:layer adapter`, as Record, with the method-free declarations it stores
  (its nested types, 25 today) and the Kind const block. SessionRef and MailAddress (already aliases of assistantwire
  types) and the generic helper `first` stay out. itemadapt gets Restore (a stored row that fails is quarantined and
  logged) and Store, which writes a decision back onto the previous Record and keeps its stored time bytes where the
  instant is unchanged; a Restore-then-Store byte golden over real stored blobs lands first. Package assistant keeps
  forwarders in aliases_inbox.go only (D4): `type InboxItem = itemadapt.Record` and same-name ones for the rest, a guard
  checking every right-hand side and that each forwarder except vars carries `//go:fix inline`, so quiet callers switch
  with go fix under X6. The file may only shrink, and nothing forces removal. This PR changes
  `TestMovedClustersStayOut` to accept those forwarders and adds Go replays of the server-side `spec/fixtures/inbox`
  operations (hub order, item state, follow time), decoded into Record and run through the adapter.
- **Then the direct writes:** the 8 `DoneAt` and `DoneBy` writes in the Inbox's files switch to the core's Decide,
  each in its own PR when its file is quiet. Until then a count holds such writes in application packages and only
  falls.
- **Done when:** the item tests pass on their own; `TestWireSamples` and S2's reflection test pass with
  `spec/fixtures/wire/inbox.json` byte-identical; the assistant package and `./internal/archguard` pass; the core and
  its adapter pass their domain-rules hooks, and the core imports only what the domain rules allow it; the item tests'
  compile time is measured next to the 58 s.
- *Revised 2026-11-22 in #10613:* the lifecycle core comes first and the methods leave the stored types before they
  move, so the stored item is a record in the core's adapter and no codemod renames methods, where the method-free
  declarations were to move into a package listed by property.

### S9. The stateful module (not planned; D3)
- **Change:** the remaining core files move to server/internal/assistant/inbox; `*Service` methods become `*Module`
  methods behind inbox.Host, and Service keeps one field and calls the Reader, Actions and Policy surfaces. The
  bridge's reads, checks and writes under the book's lock become calls to Links (section 3.1), with `checkTaskInbox`
  moving into the module behind one, so the bridge's five files leave S2's list. A listed wire type separate from the
  stored item (D12) goes to assistantwire; the item's lifecycle is already the core's from S8. The book-reaching unit
  tests move with a fake Host; a movedClusters row with the "inbox" prefix. One codemod PR, because slicing it would
  split the book's locks across two packages, and a short Inbox PR freeze, which then needs the administrator because
  the PR watcher merges continuously. *Revised 2026-11-22 in #10613:* the lifecycle writes and the item's layer come
  with S8, not here.
- **Done when:** the package's tests pass alone, then the assistant suite, hub goldens, `TestWireSamples`, archguard and
  the four docs tests; timings of a one-line Inbox edit before and after (package compile, test-binary compile,
  check-fast) are in the PR and in #10613's table.

### W1. Web feature folder, starting with the section bar
- **Change:** the rules of `web/src/lib/inbox-bar.ts` (pure; its test replays `spec/fixtures/inbox` bar*.json) become
  web/src/features/inbox/domain, under the domain rules, with features/inbox/adapt mapping the wire item to it and the
  web's domain kit (web/src/domain-kit) landing with it; the test replays the fixtures through the adapter.
  `web/src/views/inbox/InboxBar.tsx` and `web/src/lib/inbox-view-memory.ts` read the bar through the adapter.
  `lib/inbox-bar.ts` stays a shim with its old API, reaching the new model through the adapter, on the shrink-only
  list, so `web/src/views/inbox/InboxList.tsx`, which #1995, #4512, #4653 and #12508 edit, is untouched. The PR updates
  the literal in `scripts/test/test_check_fast_inputs.py` (1125), the line in `docs/web/web.md` (862) and the comment in
  `web/src/views/inbox/InboxList.bar.test.tsx` (5). It adds section 3.3's boundary test and the domain rules' lint
  blocks for the domain, adapt and domain-kit folders, which also keep effects out of the domain folder: the clock,
  randomness, the network, storage, timers, the console, and imports of the store and API modules. It lands after the
  client domain guard (section 3.3) and adds the web's paragraph to the client part of the domain rules that guard
  brings.
- **Done when:** `cd web && npm test && npm run build` passes, again after merging the latest `origin/main`; the
  first-screen room under `FIRST_SCREEN_BUDGET` is reported before and after (not raised); tsc and eslint are clean.
- *Revised 2026-11-22 in #10613:* domain and adapt folders in place of a model folder the bar moved into unchanged,
  after the client domain guard.

### W2. Non-Inbox helpers leave lib/inbox.ts
- **Change:** `withToken` (390), `shortTime` (397), `conversationRoute` (555), and the Lark message types with
  `mergeMessages` and `comparePosition` (592-667) move to their own lib modules. `lib/inbox.ts` re-exports them, so none
  of the 27 importers changes here; later PRs flip quiet importers and drop the re-exports. The interaction helpers
  (495-530 then, 474-510 now) wait for #4512, whose hunk touches them.
- **Done when:** npm test and the build pass, with the first-screen room unchanged or its difference reported; tsc and
  eslint are clean; `git diff --stat` shows no importer changed.
- **Landed (#11538):** `withToken` moved to `web/src/lib/media-url.ts`, a new module (in `web/src/lib/api.ts` its
  default token would come from that module's own `getToken`, which the tests that mock `lib/api` cannot replace);
  `shortTime` to `web/src/lib/format.ts`, after `clock`; `conversationRoute` to `web/src/lib/conversation-page.ts`; the
  Lark message types with `mergeMessages`, `comparePosition` and `LarkMention` (a sent message's @s, from the file's
  end) to `web/src/lib/lark-messages.ts`, a new module; and `MailAccount` to `web/src/lib/mail.ts`, its one importer
  outside the Inbox, which no longer imports `lib/inbox.ts`. Each block moved verbatim, and `lib/inbox.ts` re-exports
  every name, so no other importer changed. Each language's first screen is 129 B smaller (40 B left under
  `FIRST_SCREEN_BUDGET` before, 169 B after, ja): the build follows the re-export, so `mergeMessages` and
  `comparePosition` now ship only in the Lark chat's lazy chunk.
- **Deferred:** `interactionMuted`, `interactionTitle` and `interactionAsk`. Re-checked on 2026-11-20, it is #1995 (a
  draft) whose hunk edits `interactionTitle` and reaches `interactionAsk`'s comment; #4512's hunks do not touch them.
  They move once #1995 lands or closes. The rest stays: the Inbox's own (`waitingAgents` among them), or imported by no
  source outside the Inbox (`tightCJK`, the Inbox's `contextXML`).

### A1. Android module-edge ratchet
- **Change:** only the source scan, with no module (D10a): a plain JUnit test in the app's tests next to
  `InboxFacadeTest` that counts imports and fully qualified references. core:data main files using
  `persona.feature.inbox` stay a subset of today's 7, and other features may use it only from `ChatLinkHost.kt`,
  `ChatLinks.kt` (fully qualified `LarkNativeLink`, 230-232) and `MeetingHandoff.kt`. A note in
  `docs/android/android.md` ("Modules and code").
- **Done when:** the scan passes in the app's unit tests (`--max-workers=4`); a planted import fails with the hint.

### A2. :core:inbox with the pure reply and hand-off types
- **Change:** the module, created with real contents (D10a): its settings include, the core:data dependency on it, its
  row in the module table of `docs/android/android.md`, and its build file (section 3.3). ReplyTarget (sealed: Inbox,
  Lark), ReplyRequest, ReplyDraft, ReplyDelivery, InboxHandOffRequest, SuggestionRequest, SuggestionAction,
  SuggestionOutcome, InboxDoneCursor and InboxDoneDay leave the feature's `InboxRepository.kt` for
  persona.core.inbox.store. It stays a pure move: the persisted outbox types remain `@Serializable` records, unchanged,
  each pinning @SerialName to its old fully qualified name, because the reply outbox stores ReplyTarget as polymorphic
  JSON; so files written by either build decode in the other, which is also the rollback. Their UUID defaults are frozen
  by name in the client domain guard's Android scan until a later PR, with its own old-blob test, passes ids in, so A2
  lands after that guard (section 3.3). `InboxReplyJournal.kt`, `InboxReplyOutbox.kt` and `InboxReplyReceipt.kt` use
  only these types, so the ratchet's baseline goes from 7 to 4. No domain module is made for records:
  :core:inbox:domain, with the domain kit's :core:domainkit, comes when the bar's rules move (A4).
- *Revised 2026-11-22 in #10613:* the records go to a store package, not to a package of the Inbox's rules, after the
  client domain guard.
- **Done when:** the `:core:inbox`, `:core:data` and `:feature:inbox` unit tests pass; a test decodes an outbox entry
  written under the old names; the PR says whether `android/app/src/main/baseline-prof.txt` was regenerated.

### A3. The page-facing repository into :core:inbox, and core:data off feature:inbox
- **Change:** :core:inbox gains :core:domain and takes the page-facing `InboxRepository` and its pure types, outside the
  store and adapt packages. LarkFailure, InboxMuteOption.title and InboxPolicyTier move from @StringRes to keys mapped
  in feature:inbox; ReplyComposers stays out. It also takes the helpers core:data borrows: inboxDocRef, larkThreadSource
  (without core:hub and R), larkInteractivePart, larkAvatar, inboxMuteOptions, applyPolicyTier and inboxPersonaText.
  Typealiases left in the feature are on a shrink-only list. The `:feature:inbox` line leaves
  `android/core/data/build.gradle.kts`, the scan forbids core-to-feature edges, and the test fakes (60 in feature:inbox,
  7 app, 2 chat, 1 settings) change imports.
- **Done when:** `scripts/app/android.sh test` and `lint` pass; `scripts/app/android.sh shots` shows no changed Inbox
  screenshot; core:data depends on no feature module.
- *Revised 2026-11-22 in #10613:* the module's packages are now named store and adapt (section 3.3).

### A4. The bar's rules into :core:inbox:domain, on :core:domainkit
- **Change:** after A2, the client domain guard and #13024 (an unused result is a compile error in every Android
  module), while `android/core/domain/src/main/kotlin/persona/core/domain/inbox/InboxBar.kt` (116 lines) has no open PR
  (0 on 2026-11-22): the domain kit's Kotlin module :core:domainkit, with no dependency, and the Gradle subproject
  :core:inbox:domain (section 3.3), on :core:domainkit alone, whose build file sets `explicitApi()`,
  `allWarningsAsErrors` and `-Xreturn-value-checker=full`, so a caller that drops one of its answers does not compile.
  The bar's rules leave `InboxBar.kt` for it as values with private state: built by parse or a staged builder, changed
  only by becoming the next value, drawn through a visitor, with the search text as a value of its own;
  persona.core.inbox.adapt maps the wire and store types to them. A facade in :core:inbox, outside its store and adapt
  packages, keeps `InboxBar`'s API for its callers in feature:inbox and the app, whose imports change; as I1's, it
  passes one closure per case of the domain's visitor and never reads an answer back, and it is on a shrink-only list.
  The facade takes `buildInboxList`'s groups, so :core:inbox gains :core:domain here if A3 has not added it. The guard's
  Android scan has no adapter-result rule either, so A4 adds one for every .adapt package unless the guard has it by
  then. It reads more than I1's: the Swift facade is another module and reaches only the adapter's public API, but this
  facade and A3's repository share :core:inbox with persona.core.inbox.adapt, and Kotlin's `internal` is module-wide, so
  an `internal fun` there that returns a String is a getter they can call with no opt-in. Every non-private function
  and property in .adapt, internal ones included, therefore returns nothing, a domain value (a parse's outcome), a
  core:contracts wire type or a store record, never a String, a number, a Boolean, a Pair or Triple, a collection of
  them or a type the package declares; a helper that produces a word is `private` to its file. The bar's tests replay
  `spec/fixtures/inbox` bar*.json through the domain's public API. `docs/android/android.md` gets the two modules' rows
  and a pointer to the domain rules.
- **Done when:** the client domain guard covers both new modules with no finding, and its adapter-result rule fails on
  a planted `internal` .adapt function that returns a String and that the facade calls, and passes once that helper is
  `private` to its file; `scripts/app/android.sh test` and `lint` pass (`--max-workers=4`), the bar fixtures included;
  `scripts/app/android.sh shots` shows no changed Inbox screenshot.
- *Revised 2026-11-22 in #10613:* a step of its own, so the domain kit that the counts plan's Android reducer is built
  on (`docs/plans/plan-derived-counts.md`, section 6.2) has a named step and gate; its adapter-result rule reads the
  adapter's internal declarations too, since the facade and the repository share its module.

### I1. The PersonaInbox package with the bar's decisions
- **Change:** after the client domain guard (section 3.3), a new package apple/Packages/PersonaDomainKit (the domain
  kit's Swift values; a target without Foundation, an adapt target with the Foundation bridges and the system clock, and
  their tests), and apple/Packages/PersonaInbox with three targets: PersonaInboxDomain (only PersonaDomainKit, no
  Foundation, the domain rules' Swift settings), PersonaInboxAdapt (PersonaInboxDomain and PersonaWire) and the domain's
  tests. Every target of both packages, tests included, sets `-warnings-as-errors` in its own Swift settings, as
  PersonaCore's do, so the flag also holds when xcodebuild builds the app; `scripts/ci/apple.sh` gets one
  `xcrun swift test --disable-testable-imports` line per package in its packages lane, beside PersonaWire's. PersonaWire
  is unchanged. The domain owns the bar's InboxSection and InboxChoice. No package can import Shared (section 3.3), so
  an app-side InboxBar facade, compiled with Shared, is PersonaInboxAdapt's one caller: it hands the adapter plain
  values, such as the section words (`InboxItem.Tier`'s raw values), the counts `InboxProjection` keeps by each item's
  `triage` and the whole list's emptiness from `InboxPage.emptyKind`; the adapter parses them into the domain's values.
  The facade never reads an answer back: for each one it passes a closure per case of the domain's visitor, the adapter
  visits and calls each with that case's section word, count or emptiness word, and from those calls the facade builds
  `InboxItem.Tier`, the app's InboxChoice and `InboxPage.EmptyKind` (section 3.3). No planned check enforces this: the
  client domain guard reads the public API of Domain targets only and has no Swift counterpart of the Go guard's
  adapter-exported-result rule, so an adapter function returning a word would pass it. I1 therefore adds that rule to
  the guard for every Adapt target, unless the guard has it by then: their public functions and properties return
  nothing, a domain value or a PersonaWire type, never a String, a number, a Bool, a tuple, a collection of them or a
  type the target declares. `InboxItem.Tier`, the item's triage fallback (`apple/Shared/InboxModels.swift`, which the
  watch also reads) and the tier's localized title stay in Shared, so cache and watch bytes do not change. The domain
  takes the pure section-bar decisions from `apple/Persona/Views/Inbox/InboxBar.swift` (`Rows`, entries, shown, empty,
  draws and `InboxBarState`) as values with private state: built by parse, a staged builder for Rows' counts or a
  restore that returns an outcome; changed only by becoming the next value; drawn through a visitor; with the search
  text as a value of its own. `empty` takes the whole list's emptiness as an input where it called `InboxPage.emptyKind`
  itself, and the facade's closures for its cases build `InboxPage.EmptyKind`, so
  `apple/Persona/Views/Inbox/InboxView.swift`, with its `EmptyKind`, its `emptyKind(source:kind:query:)` and three open
  PRs, stays untouched; the facade is on a shrink-only list. The empty-state titles, messages and views, the landing,
  `waiting` (it reads Shared's `Interaction` and `SessionKey`), the Rows built from `InboxProjection` and
  `InboxBarLanding` stay in the app. The Rows unit cases move to `swift test`; the `bar*.json` replay goes through
  `InboxProjection`, so it stays in PersonaTests. The packages declare `.macOS(.v15)`. PersonaInbox is linked into
  Persona and PersonaVision (the Vision link goes if PersonaVision is retired) and joins the ALWAYS list in
  `scripts/test/test_apple_dev_build.py`; `docs/operations/testing.md` gets its line, and `docs/apple/ios.md` a pointer
  to the domain rules. The PR names the next candidates for the package.
- **Done when:** PersonaInboxDomain depends on PersonaDomainKit alone; `swift test --disable-testable-imports` passes
  in the new packages through their `scripts/ci/apple.sh` lines, and `swift test` in PersonaWire, without a simulator;
  the client domain guard passes on the new targets; iOS and watch builds pass with `-jobs 4`, a generic destination
  and `scripts/persona_config.py xcconfig`, naming the Xcode used or "26.0 not verified", which also shows that the
  domain's compiler settings hold under xcodebuild; the guard's adapter-result rule fails on a planted Adapt-target
  function that returns a String and passes the facade's closure shape; the PR carries the `docs/apple/performance.md`
  checklist; `scripts/i18n/apple_l10n.py` sync shows no string change.
- *Revised 2026-11-22 in #10613:* the bar's rules get a Domain target without Foundation and an Adapt target, and the
  tier stays in Shared rather than becoming a wire type; since no package can see Shared, the app-side facade hands the
  adapter plain values and the whole list's emptiness, and takes answers only through closures the adapter calls,
  which a Swift adapter-result rule holds; every target keeps `-warnings-as-errors`, with an apple.sh line per package;
  the step waits for the client domain guard; the heading drops the tier.

**Later, not planned in detail:** the web store, API and page modules as they go quiet; the Apple views (after
PersonaDesign) and the item (with the compatibility tests); the rest of `persona.core.domain.inbox` into :core:inbox
and Android's other feature edges; the deferred S3 moves and the two Reader methods; the Lark steps in section 3.3.

**Risks:** a "pure" registration step can change behaviour silently (hook order, `!meeting`, the handshake goroutine,
tool order), which byte-identical goldens, `TestWireSamples`, `TestRoutesAreTheWireOnes` and a full package run catch;
the S2 guards catch new entries in the old lists while both ways exist; the kernel grows only with slots that replace
central-list lines, under review; S9 and S8b stay behind D3's gates. Moving code into a subpackage of
`internal/assistant` speeds up a developer's own `go test` but not check-fast, which compiles package assistant's test
binary for every server change on purpose: the cross-package contract tests there catch real breakages (#10615).

## 5. How we will know it worked

| Measure | Now (287db61638; re-checked at 81361c1dd1 and 13c0e93f6f) | Target |
| --- | --- | --- |
| Inbox server commits (of 149 in 14 days) also touching `assistant/service.go`, `assistant/tools.go`, `assistant/bus.go`, `extwire/scopes.go` | 16, 8, 8, 11 | 0 for new routes, tools and bus methods in the 30 days after S6 |
| Files outside the Inbox core reaching the book: readers; the bridge's, all of which write items | 5; 5 | `assistant/service.go` only; the bridge's stay on S2's named list unless S9 gives them Links |
| Compile cost of a one-line Inbox edit | 58 s test-binary compile for `internal/assistant` (#10613) | measured for the item package if S8a happens; check-fast at most a third of main's on an S9 prototype before S9 (D3) |
| Web: commits editing `lib/inbox.ts` / raising `FIRST_SCREEN_BUDGET` (of 134) | 47 / 6 | falling / 0 |
| Web: importers of `lib/inbox.ts` outside the Inbox | 27; 26 after W2 | falling as re-exports are dropped |
| Android: core:data files using `persona.feature.inbox`; core-to-feature edges | 7; 1 | 0; 0 |
| Apple: Inbox logic under `swift test` | the Lark change gate (PersonaKit) | the bar's decisions (I1), then more |

## 6. Decisions

Decided 2026-11-22: the owner approved the recommended answers to D1 to D12, the windows and X6. This section is the
record of those answers (#12841); the options they weighed are in the open list it held before #12841, in this file's
history. Each row is what the plan now does; sections 3 and 4 carry the details.

| # | Decision |
| --- | --- |
| D1 | **Package home:** server/internal/assistant/inbox, with the item at server/internal/assistant/inbox/item. It does not wait for #10615: under the manager's tree, archguard's `managerPart` and the hub's forbidden prefixes cover it with no new entries, and check-fast's map still runs the importer's tests. The item package is the item's lifecycle, declared `//persona:layer core` and strict from its first line; its adapter inbox/item/itemadapt (`//persona:layer adapter`) holds the stored item, moved unchanged as Record at S8a, with Restore and Store (section 3.1, S8). *Revised 2026-11-22 in #10613:* a core and its adapter in place of one package listed by property. *Amended 2026-11-23 in #13408 for feature modules (#13430):* this home is for domains that stay in the core. An optional feature module lives outside the manager's tree in server/internal/modules/<id> and reaches the core only through server/internal/modapi; server/internal/assistant/kernel exists now as the host that serves modapi.Host to those modules, and the typed registry of D2 for in-tree domains joins it there when S-steps need it (`docs/architecture/core-and-modules.md`) |
| D2 | **Kernel scope:** a typed registration kernel plus module-owned Hosts, and the registry #7891's registry stage builds on (section 3.1). `Kernel` is an opaque struct made only by `Registry.Register`, whose ports are typed and come from package assistant; a member is a service every module uses (routes, the access check, body errors, `Publish` of contract events) or a slot that replaces a central-list line, never a domain method or a store; a module takes its own dependencies through its constructor; names, routes and bus methods are values checked where they are made; `TurnSettled` takes `agentref.Ref`; `saved.Host` stays as saved's adapter, built in saved/module.go; hooks are keyed by module; a registration mistake is a defect, tables run in registration order, and kernel/dispatch.go is the catch site; each migrated central list gets an archguard name freeze; the kernel is declared `//persona:layer application seam`. *Revised 2026-11-22 in #10613:* typed ports, names, events and hook in place of an untyped Infra |
| D3 | **S9 and S8b are not planned.** They are reconsidered only when all of these hold, measured on main at the time. G1: a deliberate decision to narrow check-fast's Go selection to the changed subpackage's own subtree, accepting that the parent's integration tests then do not run for those edits (not adopted; the guard job keeps compiling package assistant's test binary, so the possible gain is skipping the assistant suite, 204 s partitioned and 540 s serial, not the 47 s compile). G3: on a throwaway S9 prototype, check-fast's wall time for a one-line Inbox body edit is at most a third of the same edit on main (median of 3 alternating runs). G4: no more than 3 open PRs with heads from the last 7 days touch non-test `inbox*.go` (11 on 2026-11-22), and the codemod is a script open PRs can replay |
| D4 | **Forwarders:** allowed only in aliases_inbox.go in package assistant, same-name and exactly the moved declaration, with `type InboxItem = itemadapt.Record` the one renamed, each except vars carrying `//go:fix inline`; the file may only shrink, and nothing forces removal. They cover only method-free declarations (S8a): the stored types' methods leave first (S8b), so no codemod is needed. The `TestMovedClustersStayOut` change lands with the first S8a PR. *Revised 2026-11-22 in #10613:* the stored item's forwarder points into the core's adapter, and its methods leave before it moves |
| D5 | **Task bridge:** stays with tasks as the module "inbox-tasks". S7 renames it and routes the core's calls through inboxTasks; its writes to the book stay under the lock, as today; Links are built only if S9 happens. S7 waits only for its own X6 gate |
| D6 | **Model purposes:** the table stays central (`modelPurposes` and `modelCategoryIDs` in `assistant/models.go`), ordered as the settings page shows it. Renaming inbox.triage is out of scope. If the registry stage ever moves purposes per domain, the whole table moves at once |
| D7 | **No route manifest.** S6 lands now as one PR with the registry stage's scope step and shows `route_scopes.txt` byte-identical and a duplicate-key panic. Any typed per-route table waits for a registry decision covering every domain; the answer-only regexes in `hub/devicetokens.go` stay hand-edited |
| D8a | **Web folder:** web/src/features/inbox, with its rules in features/inbox/domain and their adapter in features/inbox/adapt (W1). Later web domains, #10613's web slices among them, use web/src/features/<domain>. *Revised 2026-11-22 in #10613:* domain and adapt folders, not `web/src/features/todos`' layout |
| D8b | **One `FIRST_SCREEN_BUDGET`.** Structural boundary tests hold the domains (section 3.3); W1 reports the room before and after and never raises the budget |
| D9 | **Lark:** the Lark chat pages stay in the Inbox as its Lark sub-area, re-decided when source migration M3 lands or the pages become extension UI; the first steps are in section 3.3 |
| D10a | **Android:** :core:inbox is the Inbox's UI-free home, with the scope in section 3.3: its store and adapt packages, and the Inbox's domain as the Gradle subproject :core:inbox:domain; A1 is only the source scan, A2 creates the module, A3's typealiases are shrink-only, A4 creates the domain subproject with the bar's rules. *Revised 2026-11-22 in #10613:* the domain is a subproject of its own, not a package of the module, made by a step of its own |
| D10b | **Apple:** a PersonaInbox package rather than a folder first, landing once the client domain guard does, with a Domain target without Foundation on PersonaDomainKit and an Adapt target that an app-side facade feeds and that answers it only through the facade's closures, `-warnings-as-errors` on every target of both packages, one `scripts/ci/apple.sh` `swift test --disable-testable-imports` line per package, and PersonaWire unchanged (I1); the views stay until PersonaDesign, then move to apple/Persona/Features/Inbox. *Revised 2026-11-22 in #10613:* Domain and Adapt targets, with no tier moved into the wire package and no plain value returned from the adapter, after the client domain guard instead of now |
| D11 | **Translation:** `/api/v1/translate`, `/translate/message` and `/translate/mail` stay in InboxRoutes, and the Inbox module registers them. Revisited when non-Inbox translation changes reach 3 or more in 30 days: they then split on the manager side as their own kernel module, with InboxRoutes kept as the relay list, so the wire and the hub stay unchanged |
| D12 | **Inbox wire generation is not reopened**; #9117's decision stands, on the conditions below |
| Windows | S1 once #12899's domain rules land, then S4 in the same shape once agentref, the shared session identity that follows them, does, with no window and no wait for the registry stage. S6 and A1 now. W1, I1 and A2 once the client domain guard lands (section 3.3), and A4 after A2, that guard and #13024. S5 keeps its gate on #6539 and #9782. S8 under D3's gates, S8b first and S8a after it. Every push runs X6's measures (section 4). *Revised 2026-11-22 in #10613:* S1 and S4 wait for the domain rules and agentref they are written to, W1, I1 and A2 for the client domain guard, A4 also for #13024, and S8's order |
| X6 | **Landing beside open PRs** is section 4's rule, for every pure-move or seam step here (and the registry stage's): merge-tree and a merged `go vet` per open PR head, activity by head commit age; a failing line stays and moves in a follow-up |

**D12 in detail.** #9117 ended Inbox wire generation, and nothing in this plan depends on it. Since #9117 the generator
(`scripts/gen/extension_schema.py`, its `schema/wire/` channel) writes Go into assistantwire
(`server/internal/assistantwire/todo_view_gen.go`) and emits `omitempty` and `omitzero`. A generated item type still
waits for four things:
- a per-field Swift Date type;
- uint32 integers (InboxMail.UIDs; the generator emits only `int` and `int64`, and `Int` is 32-bit on arm64_32
  watches);
- a listed wire type separate from the persisted InboxItem, proven equal to `listed()` by S2's reflection test;
- a partial-sample fixture mode, for the behaviour fixtures' partial items.

Whatever generates it must also leave the StateCache record, the WatchSync payload and the field order unchanged (delta
cursors and ETags hash the listed bytes). #9117 estimated leaf types only (web and Swift, Go untouched) at a day or
less, the full item at several days.

## 7. Out of scope

- **Any behaviour, wire, UI, setting or default change.** For example, the roster counts Needs you with `status()`,
  while the Lark workbench uses `wbNeedsYou` (tier or urgent); unifying them is for #5974 or a bug issue of its own.
- **Sources** (extmgr, subscriptions), the hub's relay code, HarmonyOS, and splitting `docs/protocol/protocol.md` or
  `docs/assistant/inbox.md` (the docs split is its own item of #10613).
- **A known test bug, for an issue of its own:** `android/app/src/test/java/persona/android/di/InboxFacadeTest.kt` looks
  for a declaration text `android/app/src/main/java/persona/android/di/InboxModule.kt` no longer has (the facade is at
  line 201), so it scans the file up to the assistant facade and checks less than it says.
