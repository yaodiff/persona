# Performance on the Apple clients

The rules for keeping the iPhone, Apple Watch, Mac and Vision Pro apps (`apple/`) responsive. Every person and Agent
changing `apple/` code reads this first. A PR that touches `apple/` names the rules it followed (`P1`…`P12`) and every
deviation with its reason. A performance-sensitive change also attaches before and after measurements ("Measuring").

The rules come from this repository's own regressions (`docs/lessons/ios.md`, `docs/lessons/macos.md`,
`docs/apple/ios-state.md`, the `perf(…)` PRs) and from Apple's guidance ("Sources"). Each rule says what to do, why,
where this repository got it wrong (file:line on `main` when written, 2026-11-11), the correct pattern and how to check it.
The full list of current problems the rules found is in "Survey of the current code".

## Rules at a glance

| Rule | In one line | Checked by |
| --- | --- | --- |
| P1 | No system-framework or disk IO on the main actor; `.task` and `async` do not move work off it | isolation lint, review, Time Profiler |
| P2 | Keychain and UserDefaults: read once, off the hot path; nothing that changes often goes in UserDefaults | isolation lint, ratchet, review |
| P3 | `@State` initial values are cheap and have no side effects | review, `PerfTrace.body` counts |
| P4 | `body` only arranges views: no sorting, parsing, formatting or decoding in it | review, SwiftUI instrument |
| P5 | A view reads only what it draws; split readers into small views | `RenderBudgetTests`, `AllSessionsReadersTests` |
| P6 | Lists have stable, unique identities and cheap rows | review, `PerfTrace.body` counts |
| P7 | Coalesce high-frequency updates; no state ↔ layout feedback loops | `PerfCount` push time, review |
| P8 | Decode and downsample images off the main thread, to the pixel size drawn, and cache them | isolation lint, Allocations |
| P9 | Timers, polling and animations stop when nobody sees them | ratchet, `ContinuousMotionTests`, `HoldSourceTests` |
| P10 | Launch draws the first frame before doing anything else | App Launch template, `XCTApplicationLaunchMetric` |
| P11 | Caches and retained pages are bounded; extensions decode to the size they show | Allocations, Leaks, memory graph |
| P12 | Performance claims come with measurements from an optimized build | PR review |

## Budgets

The numbers a change must stay within. Frame and hang thresholds follow Apple's; the push and memory budgets are this
repository's.

| What | Budget | Measure with |
| --- | --- | --- |
| One main-thread work item during scrolling, typing or an animation | ≤ 16 ms (one frame at 60 Hz; 8 ms on 120 Hz ProMotion screens is the goal) | Time Profiler, Animation Hitches, `FrameStats` |
| Any single main-thread block, anywhere (first tap, page open, push) | < 100 ms; Apple calls a delay over 100 ms noticeable | `MainThreadWatchdog`, Time Profiler, Hangs instrument, `sample <pid>` |
| A hang | none ≥ 250 ms, the point where Apple's tools report a hang; one found is a bug to fix before release | Hangs instrument, Xcode Organizer, MetricKit |
| Assistant ⇄ Agents switch (`PerfAssistantTests/testTabSwitchAssistantAgentsByCue`: the app switches on a cue, nothing of XCTest's in the frames) | hitch < 10 ms/s and no frame over 100 ms. Simulator, optimized build, `assistant` suite data, 2026-11-13: 39–40 ms/s, longest frame 93–101 ms (median), main thread busy p95 8.3 ms; before #5438's fixes 74–91 ms/s, 121–135 ms and 17–19 ms. Still over: a switch swaps the pages' top bars for the moving one and back, glass and menus built each time, and the first visit to Agents builds the page (a frame of about 0.85 s in the tapping test). `testTabSwitchAssistantAgents` taps and waits through XCTest, whose accessibility snapshots are most of its 67–133 ms/s; it shows the tap's path, not this budget | `scripts/app/perf-test.sh assistant`, `sample` while switching |
| Hitch time ratio while scrolling | < 5 ms/s (Apple: good under 5, warning 5–10, critical above 10) | `XCTHitchMetric`, `scripts/app/perf-test.sh` |
| View bodies per hub push, page at rest | the numbers in `docs/apple/ios-state.md` ("Cost of one push"): Agents list 1 / 2, chats 0 | `RenderBudgetTests` |
| Main-thread time per hub push | no regression against the tables in `docs/operations/testing.md` ("Whole App (any page)") | `-perfCounts YES`, `-showPagePerf YES` |
| Launch to first frame | target < 400 ms warm on a recent iPhone (Apple's launch target); no IO, keychain or migration before it | App Launch template, `XCTApplicationLaunchMetric`, Organizer |
| Memory after leaving a page | opening and closing the same page 10 times ends within 5 MB of where it started; no leaked stores in the memory graph | Allocations (generations), memory graph |
| Extensions (widgets, share, notification service, Live Activity) | decode only at the size drawn, never a full-size picture; extensions run under far tighter memory limits than the app | Memory gauge, `XCTMemoryMetric` |

These are measured in the simulator unless a device is named. Simulator numbers compare a change against its base
(same machine, same simulator, same build settings). They are not phone numbers: report them as simulator numbers.

## Rules

### P1. No system-framework or disk IO on the main actor

**Rule.** Photos (`PHAsset.fetch…`, `PHPhotoLibrary`), Contacts, EventKit (`authorizationStatus` included), HealthKit
queries, `FileManager` listings and attributes, `Data(contentsOf:)` / `String(contentsOf:)`, large `JSONDecoder` runs and
SQLite / Core Data fetches never run on the main actor. A `.task { }` modifier and an `async` method of a MainActor type
run on the main actor: `async` does not mean off the main thread.

**Why.** App targets default to MainActor (`SWIFT_DEFAULT_ACTOR_ISOLATION: MainActor` in `apple/project.yml`), so any
call written in a view, a model or an app-target helper runs on the main thread. Many framework calls are synchronous
XPC to a system daemon: their cost depends on the daemon, not on the app. The first time a process opens the Photos
database the fetch can take over a second.

**Counter-examples.**
- `apple/Persona/Views/Chat/AttachSheet.swift:79-89`: `AttachLibrary.fetch()` runs `PHAsset.fetchAssets` and enumerates
  the result on the main actor, started by `.task { await library.load() }` at `apple/Persona/Views/Chat/AttachSheet.swift:222`.
  The first tap on the composer's + blocked the main thread for about 1.3 s while Photos opened its database (#4250).
- `apple/Persona/PhoneTools/PhotosTool.swift:64`: the hub's photo-listing tool fetches on the main actor too (`PhotosTool`
  is MainActor by default); a request from an Agent stalls whatever the user is doing.
- Fixed before: `EKEventStore.authorizationStatus` asked five times on the main thread when registering the phone's
  tools (commit `e2426cd21`).

**Do.** Put the IO in a `nonisolated` function (or `@concurrent` on Swift 6.2) and hand back a `Sendable` value; publish it
to the observable model on the main actor:

```swift
/// PHAsset is immutable and safe to pass between threads, but not marked Sendable.
struct Assets: @unchecked Sendable { let list: [PHAsset] }

@Observable final class AttachLibrary {
    private(set) var assets: [PHAsset] = []
    func fetch() async {
        assets = await Self.newest(limit: Theme.Attach.fetchLimit).list   // published on the main actor
    }
    @concurrent nonisolated static func newest(limit: Int) async -> Assets {
        // PHFetchOptions, PHAsset.fetchAssets(with:) and enumerateObjects, all on a background thread
    }
}
```

Start the work before the user needs it where it is free to do so (prefetch when the composer appears, not when + is
tapped), and show the panel at once with a placeholder.

**Check.** `scripts/test/isolation-lint.sh` reads where each Photos fetch, file read and directory listing runs and fails
when a file gains one on the main actor, or one it cannot place ("Isolation lint"). In review: every new call to a system framework or the file system names the thread it runs on. Measure with
Time Profiler (main thread, "Hide system libraries" off) or `sample <pid>` during the first tap on a cold launch.

### P2. Keychain and UserDefaults: read once, off the hot path

**Rule.** Keychain reads (`SecItemCopyMatching`) happen once per launch or change, off the main thread or before the first
frame is needed, and the result is cached in memory. Values that change often (scroll positions, drafts, split frames,
counters) never go into UserDefaults: use `StateCache` (`apple/Shared/StateCache.swift`). Heavy views read settings
through `@StoredValue` (`apple/Persona/App/StoredValue.swift`), not `@AppStorage`. Never call `synchronize()`.

**Why.** A keychain read is IPC to `securityd` and can take tens of milliseconds, more while the device is locking or
unlocking. Every UserDefaults write, for any key, makes every `@AppStorage` view in the app recompute.

**Counter-examples.**
- Fixed before: the Mac's split view wrote its column frames to UserDefaults on every frame of a window resize (180–350
  writes in 4 s) and recomputed Settings and the inbox each time (commit `627c1a802`, `docs/lessons/macos.md`).
- Fixed before: `AgentSync` wrote UserDefaults on every chat fetch (`docs/apple/ios-state.md`, "Current data flow").
- `apple/Persona/App/AppModel.swift:277`: writes UserDefaults on every set of the current conversation, changed or not.

**Do.** One reader, `Keychain` (`apple/Shared/Keychain.swift`), called from the code that loads credentials at launch or
pairing; keep the value in the model and pass it down. For new persisted state pick `StateCache` first.

**Check.** The isolation lint counts `SecItemCopyMatching` on the main actor and the ratchet counts `synchronize()`; `AppStorageRatchetTests` keeps the list of files
holding `@AppStorage` from growing. Review: no `@AppStorage` key written more than once per user action; no keychain read reachable from `body`, a row, or a push handler.

### P3. `@State` initial values are cheap and have no side effects

**Rule.** The expression after `@State var x =` must be cheap and side-effect free. Classes stored in `@State` get a
trivial `init`: no framework objects, IO, subscriptions, timers or logging. Expensive set-up runs once, in `.task` or in the
owner's model, and the view holds an optional until then.

**Why.** SwiftUI evaluates the initial value every time the view's struct is created, which is every time its parent's
`body` runs, and throws away every instance but the first. Apple's `State` documentation says so and recommends `.task`
for expensive objects.

**Counter-examples.**
- `apple/Persona/Views/Chat/ChatView.swift:4168`: `@State private var attachPanel = AttachPanel()` in `Composer`;
  `AttachPanel` was constructed 17–20 times in one launch because the composer is rebuilt with the chat.
- `apple/Persona/Views/Chat/AttachSheet.swift:173`: `@State private var library = AttachLibrary()` creates a
  `PHCachingImageManager` (`apple/Persona/Views/Chat/AttachSheet.swift:66`) every time the sheet's struct is created.
- Fixed before: `ShellState(...)` as a `@State` initial value called the pager and a DEBUG trace on every `RootView`
  rebuild, which kept the main thread at 100% (commit `d2289dee5`, `docs/lessons/ios.md`).

- An initial value is evaluated in the **parent's** `body` (that is where the struct is built), so what it reads, the
  parent follows. `ShellView`'s `@State private var shell = ShellState(page: … AppModel.shared.selectedTab …)` made
  `RootView`'s body depend on the app's tab: every switch between Assistant and Agents ran `RootView`, built a throwaway
  `ShellState` and handed the shell's whole tree new structs to compare (#5438). It reads `AppModel.selectedTabNow` now.

**Do.** An initial value reads nothing observed: take the unobserved accessor (`sessionsNow`, `selectedTabNow`) or pass
the value in. Keep the model's `init` empty and create its expensive parts lazily (`@ObservationIgnored private lazy var
manager = PHCachingImageManager()`), or own the object one level up (the page's model) and pass it in.

**Check.** Review: open every class used as a `@State` initial value and read its `init`. To measure, log or count the
`init` in DEBUG (`PerfTrace.event`) and open the page: more than one instance per page open is the bug.

Automated: `scripts/test/test_apple_state_initial_values.py` matches `@State … = T(…)` against classes declared under
`apple/`. An `init` is trivial only when empty or made of assignments without calls or blocks; an eager stored-property
initial value is non-trivial if it contains a function/constructor call or a block. This deliberately counts even cheap
value constructors. Static/lazy properties, stored closure bodies, property observers, computed properties and ordinary
methods are excluded; comments and literal string contents are ignored (interpolations count as calls). This is a lexical
check, not overload, inheritance or default-argument analysis; same-named classes are treated together. Review remains required.

### P4. `body` only arranges views

**Rule.** No sorting, filtering, grouping or counting over collections, no `DateFormatter` / `NumberFormatter` creation,
no `Regex` / `NSRegularExpression` compilation, no Markdown or `AttributedString` parsing, no JSON and no image decoding
in `body` or in computed properties `body` calls. Derive once per change in the model and store the result. Localized
words with an explicit locale (`interfaceText(_:)`, `String(localized:…, locale:)`) are resolved once per locale and kept,
not looked up on every redraw.

**Why.** `body` runs whenever anything it read changes, and in lists for every row that is laid out. Work in it is paid on
every push and every frame of a scroll.

**Counter-examples.**
- `apple/Persona/Views/Inbox/Lark/LarkInline.swift:303`: compiles a regular expression on every call, from every Lark card node's `body`.
- `apple/Persona/Views/Knowledge/KnowledgeView.swift:1092`: a new `DateFormatter` per call from `body` (`apple/Persona/Views/Knowledge/KnowledgeView.swift:826`).
- Fixed before, `apple/Shared/StateNames.swift`: `StateName.text` called `interfaceText(_:)` each time it was read. With an explicit locale,
  `String(localized:)` reads the string tables on every call (1–2 ms each on the simulator, against the cached lookup of
  the default locale). The Agents filter chips (`QuickFilterBar.plainChips` → `AgentFilter.title`) and row badges read it
  on every redraw (kept per locale now): 1153 ms of main-thread time over 20 Assistant ⇄ Agents switches, about 57 ms per switch, measured with
  `sample` on an optimized build (#5438).
- Fixed before: the Agents list grouped, sorted, folded and counted every session in `body` on every push until `AgentsFeed` and
  `AgentListProjection` moved that into the model (`docs/apple/ios-state.md`, steps 1 and 3); Markdown re-parsed finished blocks while streaming until `StreamChunks`.

**Do.** Derived data lives in the model (`AgentsFeed`, `AgentListProjection`, `AppModel.badges`), keyed by a small revision,
derived off the main actor when large (`docs/lessons/ios.md`, "Cached Agent-list projections (#1231)"). Formatters are
`static let`, or use `Date.FormatStyle`, which caches. Words drawn on every redraw are resolved once per locale:

```swift
// Wrong: a table lookup every time a chip or a badge redraws.
var text: String { interfaceText("Needs You [State Names]") }

// Right: once per interface locale, then a dictionary read (#5438).
var text: String {
    let key = interfaceTextLocale.identifier + "|" + rawValue
    if let known = Self.resolved.withLock({ $0[key] }) { return known }
    let text = interfaceText("Needs You [State Names]") // perf-ok: resolved once per locale
    Self.resolved.withLock { $0[key] = text }
    return text
}
```

**Check.** The ratchet counts `localized-lookup`: every `interfaceText(` call and every `String(localized:…, locale:)`
(63 in 5 files when the rule was added); a new one fails unless its line says why it is fine (`// perf-ok: resolved once per
locale`). The rest is review: measure with the SwiftUI instrument (Xcode 26: long view body updates) or `PerfTrace.body`
counts with `-perfCounts YES`.

### P5. A view reads only what it draws

**Rule.** Observation tracks every property a `body` reads. A view reads the narrowest value it draws: one session's cell
(`model.session(_:)`), not `allSessions`; `namedMachine(_:)`, not `machines`. When a big view needs one fast-changing value
in one place, move that read into a small child view. Actions read unobserved values when tapped (`model.sessionsNow`,
`AppModel.latest(_:)`): observed for display, unobserved for actions. Never write observable state in `body`.

**Why.** One read of a fast-changing property makes the whole view recompute on every change of it.

**Counter-examples.**
- Fixed before: `ChatView`'s footer read `model.drafts[key]`, so each keystroke redrew the cards, the dock and the
  composer's wrapper; the read moved into `DraftReader` (`apple/Persona/Views/Chat/ChatView.swift:6123`, commit `ff5aa0071`).
- Fixed before: the chat subtitle read `machines`, so every machine heartbeat recomputed the page and all its rows
  (`docs/apple/ios-state.md`, step 5); writing a trace array in `body` made `RootView` observe it and redraw for ever (commit `d2289dee5`).

- Fixed before: three reads that made whole pages run on every switch between Assistant and Agents, where nothing they
  draw changes (#5438, measured with `-perfCounts YES`, bodies per switch):
  - `AgentsList`'s body worked out whether the list is out of sight from `model.selectedTab`, the strip's `moving` and
    `model.visibleSession`, all of which change on a switch: 3–4 runs of the list's body, over every row. The reads moved
    into `AgentsCoverWatch`, a clear view in the list's background that holds the feed and carries the polling.
  - `ChatView` declared `@Environment(\.dismiss)`. SwiftUI hands out a new `DismissAction` when the presentation around
    a view changes, and a shell page going behind or coming back counts: 2–3 runs of the chat page, its footer and its
    transcript. `DismissReader` declares it now and keeps the latest in an unobserved `DismissHolder`.
  - `ChatsView`'s body read where the shell's bar is drawn (`AgentsHeader.bar`) to fade it. The bar is put back each time
    the strip rests on Agents and reports its place again: one run of the page and one of the list. The read moved
    into the bar's own modifier (`AgentsTopBarPlace`).

**Do.** Follow `docs/apple/ios-state.md` ("Keeping redraws small"). An environment value that changes with the
presentation (`dismiss`, `isPresented`, `scenePhase`, `tabIsActive`) and a model value that only an action or a side
effect needs are read by a small view or modifier of their own (`DismissReader`, `PageFrontWatcher`, `AgentsCoverWatch`),
which hands the big view nothing to redraw for. Write-only records are `@ObservationIgnored`. A store
many pages share hands each one a projection of its own key, built per change: `MeetingPhotos.Album` is one recording's
photos with each row's content worked out, so the meeting thread reads only their order, each photo row reads the rest,
and another recording's uploads redraw neither.

**Check.** `RenderBudgetTests` (views per push, and `testShellSwitch`: views per switch between Assistant and Agents) and
`AllSessionsReadersTests` (who may read `allSessions`) fail on regressions; add a `PerfTrace.body("Name")` to a new heavy
view so its count shows up. To find what made a body run, put `let _ = Self._printChanges()` at its top in a local build
and read the app's console (`xcrun simctl launch --console-pty …`): it names the property, environment value or
observed key path that changed.

### P6. Lists have stable identities and cheap rows

**Rule.** `ForEach` / `List` ids are stable, unique model ids: no `id: \.offset` over a collection that inserts, removes or
reorders, no `id: \.self` over values that change, no `UUID()` made in `body`. No `AnyView` in rows. Heavy rows are
`Equatable` (or take only `Equatable` inputs) so a push that does not change them does not redraw them. Many rows are
separate lazy rows, never one child view holding hundreds of them.

**Why.** An identity that shifts makes SwiftUI treat every row after an insertion as new: state is lost, rows animate
wrongly and all are rebuilt. `AnyView` hides the type, so SwiftUI cannot diff the row cheaply.

**Counter-examples.**
- `apple/Persona/Views/Chat/ComposerParts.swift:168`: the composer's attached images, which can be removed, use `id: \.offset`.
- `apple/Persona/Views/Inbox/Lark/LarkCard.swift:305-335`: Lark cards inside chat rows are trees of `AnyView`.
- Fixed before: one `VStack` of all rows inside a `LazyVStack` laid out hundreds of rows at once and blanked the page (`docs/lessons/ios.md`, "Chat lists and scrolling").
- Fixed before: an Agent's 33,000-character report was one row (one MarkdownUI document laid out whole, twice, with every table's decoration
  re-run per scrolled frame); long finished answers are now lazy rows of about 24 lines (`AnswerParts`, #4970).

**Do.** Give each element an `Identifiable` id from the hub (`SessionKey`, item ids). `\.offset` is fine only over a
constant array built in the same expression (a fixed set of chips).

**Check.** Review. Measure a scroll with `XCTHitchMetric` (`scripts/app/perf-test.sh`) and row body counts.

### P7. Coalesce high-frequency updates; no feedback loops

**Rule.** Streams that can arrive faster than a frame (hub pushes, streaming deltas, progress, scroll and geometry
callbacks) are coalesced before they touch observable state, and only assigned when the value changed. State written from
layout (`onGeometryChange`, preferences) is quantized, so writing it cannot change the layout that reported it.

**Why.** Each assignment of an observed property schedules a SwiftUI update; ten in one frame cost ten updates. A layout
value written back into state that changes the layout loops for ever and hangs the app.

**Counter-examples.**
- Fixed before: a card's password field hung the app because the pending cards' height and the footer's height were
  written back with different tolerances and cycled with period 4 (commit `4939f5960`).
- Fixed before: each machine's session list landed as its own SwiftUI update; they now coalesce for 25 ms
  (`AppModel.sessionsCoalesce`, `apple/Persona/App/AppModel.swift:1646`).
- Fixed before: the chat's programmatic jumps animated SwiftUI's `ScrollPosition` (`withAnimation { position.scrollTo(…) }`),
  a SwiftUI update every frame that put the content at a new fractional pixel offset and drew every text layer on screen
  again. With CoreGraphics' 1 MB glyph cache, a long Chinese answer had its glyphs rendered anew each frame (#5076,
  docs/lessons/ios.md「Animated jumps redraw every text they pass」). Jumps to the newest end now move the
  `UIScrollView` (`ChatJumpRun`); jumps to a line keep SwiftUI's (a far one measured a longer frame on the list).

- Fixed before: the composer kept where its 「+」 is in the window in `@State`
  (`.onGeometryChange { $0.frame(in: .global) } action: { plusFrame = $0 }`), for the panel a tap opens. While the
  shell's strip slides, every window frame on both pages changes on every frame: the bar's body ran about 30 times a
  switch, each run building its menus and updating its text field again (`PlatformTextFieldCoordinator.update` →
  `setSelection`, where the watchdog once caught a 195 ms block). It is an unobserved `WindowFrame` now, read on the
  tap. The Agents header's field did the same with its row's frame (about 60 runs a switch), and laid the row
  out again each frame, the filters' `ViewThatFits` with it; it goes by the row's width and the page's now. A named
  coordinate space on the `List` did not help: a row does not resolve it and falls back to the window's (#5438).
- Fixed before: a `ViewThatFits` in a list row measured every candidate on every frame the row moved on screen (a row
  has a view graph of its own, which works its size out again each time): the Agents filters, 53 ms of main-thread time
  a switch. Each candidate is wrapped in `MeasuredOnce` (`apple/Persona/DesignSystem/Components.swift`).

**Do.** A frame in the window (`.global`) moves whenever anything above the view moves: the shell's strip, the drawer, a
sheet's drag, the keyboard. Measure in the coordinate space of what the value is about (`.named(…)` on the page or the
list), take only the component needed (`minY` does not change in a slide sideways), and keep a frame that only an action
reads in an unobserved object. Batch into one main-actor hop per frame or per short window (`applyPendingSessions`); compare before assigning;
round geometry to whole points and write it back only when it moved by at least a point. Animate a long list's
programmatic scroll on its `UIScrollView` (a display link, whole pixels; `ChatJumpRun`), never by animating SwiftUI's
scroll position; put a far target a screen short first (`ChatJump`).

**Check.** `-perfCounts YES` push time and bodies; the mock hub's `/mock/churn` and `/mock/say?stream=1` drive the load
(`docs/operations/testing.md`, "Persona page (long main chat)"). The ratchet counts `window-frame`, every
`frame(in: .global)`: a new one fails unless its line says why it is fine (`// perf-ok: only y, kept unobserved`).

### P8. Decode and downsample images off the main thread

**Rule.** Pictures from data or files are decoded and downsampled to the pixel size they are drawn at, off the main
thread (ImageIO `CGImageSourceCreateThumbnailAtIndex`), and cached in an `NSCache` with a cost. `UIImage(data:)`,
`UIImage(contentsOfFile:)` and `preparingForDisplay()` do not run in `body`, a row or on the main actor.

**Why.** A decoded picture costs width × height × 4 bytes whatever its file size (a 12 MP photo is about 48 MB) and the
decode takes tens of milliseconds. `UIImage(data:)` decodes lazily at first draw, on the main thread.

**Counter-examples.**
- `apple/Persona/Persona/PersonaStore.swift:566`: `avatar(_:points:scale:)` reads and decodes the avatar file and calls
  `preparingForDisplay()` on the main actor; `apple/Persona/Persona/PersonaViews.swift:19` calls it from `body`, so the
  first draw of each avatar decodes on the main thread (cached afterwards).
- `apple/Persona/Views/Inbox/Lark/LarkAttachmentView.swift:33` and `apple/Persona/Views/Session/CompareView.swift:249`: `UIImage(data:)` in `body`, on every render.
- `apple/Persona/Views/Chat/PhotoAttach.swift:35`: picked photos decoded, scaled and encoded on the main actor.
- `apple/Persona/Views/Machines/ClipboardSection.swift:131`: the clipboard preview builds `UIImage(data:)` from the
  computer's full-size picture in `body`, so every redraw of the machine page decodes it again (#3281, listed in #5414).

**Do.** Follow `LarkMedia` (`apple/Persona/Views/Inbox/Lark/LarkMedia.swift:101-120`): download, then
`Task.detached { decode(data, maxPixel:) }` with a `nonisolated` ImageIO thumbnail, in-flight requests shared, an `NSCache`
with a byte cost. Photos thumbnails come from `PHCachingImageManager` at the cell's pixel size.

**Check.** The isolation lint counts `UIImage(data:)` / `UIImage(contentsOfFile:)` / `NSImage(data:)` on the main actor. Allocations shows
full-size `CGImage` backing stores.

### P9. Work stops when nobody sees it

**Rule.** Timers, polling, `TimelineView(.animation)` and view-scoped `Task`s run only while their page is in front and the
scene is active: `.tabActiveTask` and `.onTabVisibility` (`apple/Persona/Views/TabActivity.swift:101-112`), not
`onAppear` / `onDisappear` (hidden tabs never get `onDisappear`). Every `TimelineView(.animation…)` has a `paused:`
argument; every unending animation is listed in `ContinuousMotionTests`. Timers are invalidated; long loops check
`Task.isCancelled`; closures stored on `self` capture `[weak self]`.

**Why.** Retained tabs and pages stay mounted. Work behind them costs battery and main-thread time on every page; on the Mac
a running `TimelineView(.animation)` relayouts the window every frame (`docs/lessons/macos.md`).

**Counter-examples.**
- `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:811`: a `TimelineView(.animation(minimumInterval: 1 / 30))`
  with no `paused:`.
- `apple/PersonaWatch/PersonaWatchApp.swift:285-288`: an endless poll whose `try?` sleep swallows cancellation.
- Fixed before: hidden chats kept their transcript subscription and read owner after a tab switch (`docs/lessons/ios.md`,
  "Retained chat visibility").
- The other side of the same rule: `.tabActiveTask` restarts every time its page comes back, so what it does is paid on
  every switch. `apple/Persona/Views/Chat/ChatView.swift` refetches the user-input index (`loadUserInputIndex`, #4912) each
  time the chat is in front again, and writes it to `@State`, which redraws the chat. Work that only needs to happen when
  its input changed is keyed by that input (`.tabActiveTask(id: revision)`) and skipped when the result is already held.

**Do.** `.tabActiveTask { … }` for polling; `TimelineView(.animation(paused: !visible || reduceMotion))`; persistent
animations on the Mac in Core Animation (`MacBreathingRing`).

**Check.** The ratchet counts `TimelineView(.animation…)` without `paused:`; `ContinuousMotionTests`; `HoldSourceTests`
(no timers in model-following feeds). Leaks and the memory graph after leaving the page. Work that reruns on every return
cannot be found by text: `PerfAssistantTests/testTabSwitchAssistantAgentsByCue` measures the Assistant ⇄ Agents switch against
its budget ("Budgets"); a page with its own switch-back cost gets a test of the same shape.

### P10. Launch draws the first frame first

**Rule.** `App.init`, the app delegate's launch callbacks and the first scene's first `body` do only what the first frame
needs: read the cached snapshot (`StateCache`) and show it. Network, keychain reads beyond the hub token, migrations,
registering phone tools, extension discovery and prefetching start after the first frame, from a `.task`, off the main
actor.

**Why.** The system measures launch to the first frame (Xcode Organizer, MetricKit) and kills an app that takes too long to
launch. Everything before the first frame is time the user stares at the launch screen.

**Counter-examples.**
- `apple/Persona/App/AppModel.swift:2107` and `apple/Persona/App/AppModel.swift:3078`: the drafts, quotes and comments files are each read and decoded twice while `AppModel` initializes.
- `apple/PersonaWatch/PersonaWatchApp.swift:6`: the watch's `WatchModel()` reads the keychain and the pet manifest and starts polling in `init`.

**Do.** Show the cached state, then connect. Mark launch phases with `PerfTrace.event` so Instruments' Points of Interest
show where the time went. `Snapshot restored` follows a successfully decoded cached snapshot and publication of its values;
`First frame (root appeared)` is the once-per-process root-appearance boundary immediately before SwiftUI submits the first
frame (the system launch metric, not this proxy, is the displayed-frame timestamp); `Connected` enters `AppModel.didConnect`
after the socket reaches `.connected` and before registration and refetch work. They add no dynamic detail or file IO; their
only new state is one static Bool, and their logging cost is the existing `PerfTrace.event` path (including its lazy OSLog
setup). They compile only in DEBUG / PERF_SIGNPOST builds.

**Check.** The App Launch template in Instruments (simulator or device), `XCTApplicationLaunchMetric` in a UI test, and the
launch histogram in Xcode Organizer for TestFlight builds.

### P11. Bounded memory

**Rule.** Every cache has a bound in entries and bytes (`NSCache` with `totalCostLimit`, or a bounded LRU as in commit
`02730aa09` for Markdown highlights). Pages that stay mounted release their large data when hidden and fetch it again
(`since_seq`) when shown. Extensions (widgets, share, notification service, Live Activity) decode images only at the size
they draw and never load whole transcripts.

**Why.** iOS ends a process that grows past its memory limit without a crash report the user sees; extensions get much
lower limits than the app. A cache with no bound is a leak with extra steps.

**Do.** Give caches a cost; drop them on `UIApplication.didReceiveMemoryWarningNotification`; release retained stores on
`onTabVisibility` disappear. Full-size decodes of user pictures run one at a time and let the original go as soon as it
is downscaled (`MeetingPhotos` prepares a pick's photos in turn, and `MeetingPhotoPipeline.prepare` drops the original
before Vision reads the small copy), so four 48 MP photos picked during a recording never sit in memory at once.

**Check.** Allocations with generations (open and close a page 10 times; "Budgets"), the memory graph debugger for leaked
stores and models, `XCTMemoryMetric` for a scenario test.

### P12. Measure what you claim

**Rule.** A PR that says it makes something faster, or that touches a hot path (chat list, composer, Agents list, push
handling, launch, images), attaches before and after numbers: same machine, same simulator or device, optimized build,
at least three runs each, worst case as well as median. A change without a measurable gain says so ("less gets redrawn,
but no frame-rate gain could be measured", as commit `ff5aa0071` did).

**Why.** Debug `-Onone` numbers and single runs mislead; simulator variance between runs is large.

**Check.** PR review against "Measuring".

## Measuring

Start from a reproduction, measure the base, change one thing, measure again.

1. **Build optimized.** `SWIFT_OPTIMIZATION_LEVEL=-O SWIFT_COMPILATION_MODE=wholemodule` in a separate DerivedData, or
   `scripts/app/perf-test.sh`, which builds that way. Default Debug numbers are not comparable (`docs/operations/testing.md`,
   "Jank measurement (dropped frames)").
2. **Use your own simulator** through `scripts/app/sim-boot.sh` and an isolated mock hub (`scripts/mock/mock_hub.py` on your
   own port) with heavy data (`MOCK_AGENTS=1`, `MOCK_LONG_ASSISTANT=1`, `MOCK_LONG_HISTORY=1`).
   Before measuring, check that the app is active (`scripts/app/personactl.py`'s `state` answers `appState: active`). An app
   left inactive, for example under an unanswered system prompt (#5089), keeps its hub links suspended and measures nothing;
   restart the simulator with `sim-boot.sh --reslim`, or use `--profile stock`.
3. **Find the main thread's time.**
   - `sample <pid> 10 1 -file out.txt` while reproducing: no Instruments authorization needed; aggregate the main thread.
     In `scripts/app/perf-test.sh`, `PERF_SAMPLE` waits a fixed 25 s after the test starts, which can miss `measureFrames`
     when a test pairs, seeds state or warms a panel first (for example `PerfComposerTests/testComposerPlusPanel`). Check
     `frames.json`'s `long` `[offset_s, duration_ms]` timestamps and sample inside the measured window, then run the final
     before / after numbers without `sample` attached so `task_suspend` does not inflate frame intervals (#6235).
   - Instruments: Time Profiler (attach to the simulator app's PID); Hangs (reports main-thread blocks over the
     threshold you set, 250 ms by default; set it lower to find 100 ms blocks); SwiftUI (Xcode 26: view body updates,
     their causes and long updates); App Launch; Allocations and Leaks. Animation Hitches needs a device.
   - Signposts: `PerfTrace.event("Name", detail)` (`apple/Persona/App/PerfSignpost.swift`, subsystem `Persona.Performance`,
     Points of Interest) marks moments. For a span, use an `OSSignposter` interval in the same subsystem, compiled under
     `#if DEBUG || PERF_SIGNPOST`. The launch marks are `Snapshot restored`, `First frame (root appeared)` and `Connected`;
     the middle mark is an app-side pre-submission proxy, while the system's launch metric records the displayed frame.
4. **Count redraws.** Add `PerfTrace.body("ViewName")` at the top of a heavy `body`; launch with `-perfCounts YES`.
   `PerfCount` counts bodies and the main thread's time from each push to the next idle run loop. When a modal sheet or
   overlay hitches, check the background page's counters too (`ChatView`, `ChatTranscript.body`): presenting on the shell
   window's `rootViewController` dims `RootView` and flushes the conversation under it (`docs/lessons/ios.md`, #6235).
5. **Catch main-thread blocks.** DEBUG and PERF_SIGNPOST builds start `MainThreadWatchdog` with the root view. A background
   thread pings the main run loop every 25 ms. If a ping takes at least 100 ms, the watchdog suspends the main Mach thread,
   copies its registers and frame-pointer chain, resumes it, and symbolicates the addresses afterwards. Raw addresses and a
   `PerfTrace.event` are recorded before symbolication, so a persistent hang is visible before the main thread answers again;
   the answer's own timestamp supplies the final duration without the sampler's overhead. Healthy pings capture no stack and
   emit no event. A block appears as `main_thread_blocks` in
   `PerfCount`, and is attached to each `FrameProbe` run in `frames.json`. Reports keep the five longest blocks and 32 frames
   per stack. `PerfAssistantTests/testWelcomePageIdle` asserts that every measured run has zero such blocks; other suites
   attach the data without enforcing the budget yet.
6. **Automate it (the probe-script pattern).** A DEBUG launch argument runs the scenario inside the app and writes its
   numbers to a file (`-showAutoScroll YES` → `Documents/frames.json` from `FrameStats`; `-showPagePerf YES` →
   `frames-page-*.json`); a script launches the app, drives the mock hub (`/mock/churn`, `/mock/say?stream=1`, or
   `/mock/say?trace=markdown|prose&node=lead|trail` for a recorded model stream at the node's cadence), waits with a
   timeout, reads the file and ends with a one-line verdict. Screen probes (`-probes YES`, `docs/apple/ios-state.md`,
   "Probes and assertions") expose state for UI tests. The suites in `scripts/app/perf-test.sh` (`PerfAgentsTests`,
   `PerfAssistantTests`, `PerfComposerTests`, …) do this with `XCTHitchMetric` and report through
   `scripts/app/perf_report.py`.

   `scripts/app/perf-test.sh launch` instead runs `PerfLaunchTests` with `XCTApplicationLaunchMetric` and no frame probe.
   Cold launches reset the saved pairing and delete the development snapshot before each fresh process; the host-side test
   runner performs that deletion in its assigned simulator's container, without changing app-side cleanup. Warm launches
   first verify the large mock data and the durable snapshot from an unmeasured seed; screen probes run only in that seed,
   never in a measured launch. The measured warm launches use the saved pairing. XCTest discards its own warm-up, so these
   are fresh pairing/snapshot-state and restored-snapshot process launches with warm OS caches, not a rebooted-device cold
   series. Other persisted launch inputs (transcripts, drafts, outbox and personas) stay untouched; the lane measures their
   existing loading rather than changing it. The report keeps every retained value and shows cold / warm median, range and
   spread in milliseconds. The 400 ms recent-iPhone budget is not a simulator failure gate.
7. **Keep XCTest out of the measured frames.** `element.tap()` resolves the element from an accessibility snapshot of
   the app, and `waitForExistence` takes one per poll, all on the app's main thread: 55–70 ms each with the `assistant`
   suite's data, about three quarters of the main thread's busy time during `testTabSwitchAssistantAgents` (#5438). A
   test that taps and waits inside `measure` reports XCTest's hitches with the app's. For an animation, give the app a
   cue (a Darwin notification handled by `FrameProbe`, as `com.persona.perf.shell.switch`) and let the test only sleep
   while frames are recorded (`testTabSwitchAssistantAgentsByCue`; the list glides run inside the app the same way, `FrameProbe.runGlide`). To see the split,
   `PERF_SAMPLE=<test>` samples the app during a test: XCTest's share is everything under `__XCTPerformOnMainRunLoop`.
8. **In the field.** TestFlight builds report hangs, launch time and memory to Xcode Organizer. MetricKit is not wired yet
   (#4265).

Report the device or simulator, OS, build settings, data set, number of runs and the range, and say what was not measured.

## Automated checks

| Check | What it does | State |
| --- | --- | --- |
| `scripts/test/test_apple_perf_patterns.py` | Ratchet over Swift under `apple/` (tests excluded): `UserDefaults.synchronize()`, `TimelineView(.animation…)` without `paused:`, localization lookups with an explicit locale (`interfaceText(`, `String(localized:…, locale:)`; P4), and reads of a view's frame in the window (`frame(in: .global)`; P7). Each file's counts (`scripts/test/apple_perf_baseline.json`) may only go down; a call verified to be fine is marked `// perf-ok: <reason>`. After removing calls, run it with `--update`; `--list` prints every counted call. Runs in `scripts/ci/python-tests.py`. | Implemented |
| `RenderBudgetTests`, `AllSessionsReadersTests`, `AppStorageRatchetTests`, `HoldSourceTests`, `ContinuousMotionTests` | Views per push and per switch between Assistant and Agents (`testShellSwitch`: no page's body runs, the composer bar's at most three times), who reads `allSessions`, files still holding `@AppStorage`, no self-managed waits, every unending animation stops under Reduce Motion | Existing |
| `scripts/app/perf-test.sh` | Scroll, navigation and composer hitches per page, plus cold / warm first-frame launch timing | Existing, run by hand |
| `scripts/test/isolation-lint.sh` | Isolation lint over Swift under `apple/` (tests excluded): Photos fetches, `SecItemCopyMatching`, `Data` / `String(contentsOf:)`, directory listings and `UIImage(data:)` / `(contentsOfFile:)` / `NSImage(data:)`, each with where it runs: on the main actor, off it, or unknown. Only the first and the last count; each file's counts (`scripts/test/apple_isolation_baseline.json`) may only go down, and no comment clears a call ("Isolation lint"). Runs in `scripts/ci/apple.sh`. | Implemented, #4262 |
| `scripts/test/test_apple_state_initial_values.py` | P3 ratchet over Swift under `apple/`, including tests: class construction in `@State` with non-trivial `init` bodies or eager stored-property initial values. Each file/type count (`scripts/test/apple_state_initial_values_baseline.json`) may only go down; new entries start at zero. `--update` lowers the baseline; `--list` prints every counted site and its reason. Automatically discovered by `scripts/ci/python-tests.py` in the scripts lane. | Implemented, #4263 |
| DEBUG main-thread watchdog | A background thread pings the main run loop and logs a `PerfTrace.event` plus a main-thread sample when it is blocked over 100 ms, so UI tests and runtime CLI scenarios catch blocks without Instruments | Implemented, #4264 |
| MetricKit | Subscribe `MXMetricManager` in TestFlight builds; send hang (`MXHangDiagnostic`), launch and memory payloads into Copy Diagnostics | Proposed, #4265 |
| Launch metric | `PerfLaunchTests` measures fresh-state and snapshot-restored process launches with `XCTApplicationLaunchMetric`; `scripts/app/perf-test.sh launch` uses the large mock and `perf_report.py` reports median, range and spread without a simulator device-budget gate | Implemented, #4266; run by hand |

### Isolation lint

`scripts/test/isolation-lint.sh` builds the SwiftSyntax package in `scripts/test/isolation-lint` and runs it over `apple/`.
It reads the sources and the project, not a build: about two seconds once built. For each blocking call (P1, P2, P8) it
says where the call runs:

| Verdict | Meaning | Counted |
| --- | --- | --- |
| `main` | On the main actor in at least one target that compiles the file | yes |
| `unknown` | The lint cannot tell: nothing it can read says which thread runs the code | yes |
| `off` | Off the main actor in every target, with every use the lint found listed in the reason | no |

```sh
scripts/test/isolation-lint.sh                # the check: PASS, or FAIL with each new call and why
scripts/test/isolation-lint.sh --list --why   # every call, its verdict and the chain of reasons
scripts/test/isolation-lint.sh --update       # lower the baseline after moving calls off the main actor
scripts/test/isolation-lint.sh test           # the lint's own tests
```

The baseline may only go down: `--update` refuses when a count would rise, and a new call that is `main` or `unknown`
fails the check. There is no comment that clears a call. To fix a finding, move the work ("Do" under P1), or make the
code say where it runs in a form the lint reads; `--list --why` shows what it read.

**Where the isolation comes from.** `xcodegen dump` of `apple/project.yml` gives each target's sources and its
`SWIFT_DEFAULT_ACTOR_ISOLATION` and `SWIFT_APPROACHABLE_CONCURRENCY`; packages under `apple/` have no default isolation
unless their manifest sets one. A file that several targets compile is on the main actor if any of them puts it there;
a file no target compiles is `unknown`. Nothing is guessed from a path.

**Declarations it understands** (each checked against the compiler, Swift 6 mode with MainActor as the default):

- Unannotated declarations of a target that defaults to MainActor, explicit `@MainActor` on a type, extension,
  function, property, initializer or closure, and the SwiftUI and UIKit types that are `@MainActor` in the SDK: `main`.
- `actor` instance members and other global actors: `off`. An actor's static members, `nonisolated` members and nested
  types are not on the actor.
- `@concurrent`: `off`. `nonisolated async`: on its caller's actor where the target turns on
  `NonisolatedNonsendingByDefault` (the app targets do), `off` where it does not.
- `nonisolated` functions, properties, initializers, types and extensions run where they are called (below). The
  modifier on a type covers its members and nested types, but not a plain `extension` of it, which is MainActor again
  in a target that defaults to it.
- Types the compiler infers `nonisolated`: one that conforms to `Error`, `LocalizedError` or `CodingKey` on its own
  declaration (not in an extension), and a subclass of a `nonisolated` class.
- A member that meets a requirement of a `@MainActor` protocol, or of a protocol declared in a MainActor-by-default
  target, is `main` even inside a `nonisolated` type, unless the member itself says `nonisolated`.
- `deinit` is `unknown` (it runs on whichever thread drops the last reference) unless it is `isolated deinit`.
  A parameter's default value runs where the function is called; `async let` runs in a child task.

**Closures it understands.**

- Leave the actor: `Task.detached`, `Thread.detachNewThread`, `DispatchQueue.global().async` / `asyncAfter`, `async` /
  `asyncAfter` on a queue the same file creates with `DispatchQueue(label:)`, a task group's `addTask` /
  `addTaskUnlessCancelled` written inside the `with…TaskGroup` closure, and `{ @concurrent in }`.
- Come back: `MainActor.run`, `MainActor.assumeIsolated`, `DispatchQueue.main.async` / `asyncAfter` / `sync`,
  `OperationQueue.main.addOperation`, `RunLoop.main.perform`, `{ @MainActor in }`, and a `@MainActor` closure parameter
  of a function in the scanned sources.
- Stay where they are written: `Task { }` and `Task.immediate` (on the main actor in main-actor code, on the global
  executor in code that is not isolated), closures a collection runs before it returns (`map`, `forEach`, `filter`, …),
  `withCheckedContinuation` and the other `with…` scopes, `sync` on a queue that is not the main one, and `{ … }()`.
- An explicit `{ @Sendable in }` is not isolated to the code around it: it runs where it is called.
- A closure handed to a function of the scanned sources runs where that function calls its parameter; one kept in a
  property (assigned, or through a struct's memberwise initializer) runs where the property is called.

**Code that is not isolated runs where it is called.** The lint follows calls by name, argument labels and the declared
type of the value they are made on. One caller on the main actor makes it `main`. It is `off` only when every use found
is off the main actor and nothing can call it unseen.

**What it does not understand, and what it says instead.** It never answers `off` for these:

| Form | Answer |
| --- | --- |
| A closure handed to an SDK function not listed above (`URLSession` completion handlers, `NotificationCenter` observers, `PHImageManager` result handlers, `NSItemProvider` loaders) | `main` when written in main-actor code without `@Sendable`, otherwise `unknown` |
| A closure literal that only its destination's type makes `@Sendable` | `main` when written in main-actor code (a false positive: write `{ @Sendable in` to say so) |
| A closure kept in a local variable, or read out of a property and handed on | `unknown` outside main-actor code |
| `map`, `filter` and the like on a lazy sequence, in a file that imports Combine, or in the sequence of a `for await` | `unknown` outside main-actor code |
| A call on a value whose type no declaration states (`$0.load()`, a value out of an untyped closure) | may make the callee `unknown`, never `main` or `off` |
| `@objc` members, `#selector`, overrides of SDK classes, callbacks of SDK delegate protocols | `unknown`: the caller is outside the sources |
| A function handed on as a value, or a property named in a key path | `unknown` |
| `nonisolated` code with no call in the sources | `unknown` |
| A conformance other than `Error`, `LocalizedError` or `CodingKey` that makes the compiler infer `nonisolated` | read as MainActor |
| `SWIFT_DEFAULT_ACTOR_ISOLATION` set per build configuration | the run stops with an error |

Two limits can hide a call and are worth knowing. An async sequence's `map` or `filter` outside a `for await` header, in
a file that does not import Combine, is taken for a collection's and so for running in place. A global actor other than
`MainActor` is taken to run off the main thread. Types are matched by name across targets, which can only add callers.

The lint is syntax-only on purpose: a SourceKit index would give the compiler's own answer for a declaration, but it
needs a full build of every target for every platform first, and it still does not say where `nonisolated` code is
called from, which is most of the answers above.

## Review checklist

Paste into a PR that touches `apple/`, marking each item done, not applicable, or deviated (with the reason):

- [ ] P1 / P2: no new system-framework call, file read, keychain read or UserDefaults write on the main actor, from `body`, a row or a push handler.
- [ ] P3: every new `@State` initial value is cheap and side-effect free.
- [ ] P4 / P5: no new work in `body`; new reads of fast-changing model properties are in the smallest view that draws them.
- [ ] P6: new lists use stable model ids; no `id: \.offset` over changing data, no `AnyView` rows.
- [ ] P7 / P9: new streams are coalesced; new timers, polling and animations stop when hidden (`.tabActiveTask`, `paused:`).
- [ ] P8 / P11: new images are downsampled off the main thread and cached with a bound.
- [ ] P10: nothing new runs before the first frame.
- [ ] P12: a performance-sensitive change has before / after numbers ("Measuring"), or says why none.

## Survey of the current code

What the rules found on `main` on 2026-11-11. Each line is a problem to fix (tracked in #4267), not an exemption; a PR
that fixes one removes its line.

Grep counts are across `apple/` without tests; they are leads to read, not each a bug.

**P1: system-framework and disk IO on the main actor**

- `apple/Persona/Views/Chat/AttachSheet.swift:79-89`: `PHAsset.fetchAssets` and enumeration of the result on the main actor, from `.task` (`apple/Persona/Views/Chat/AttachSheet.swift:222`); about 1.3 s on the first tap of + (#4250).
- `apple/Persona/Views/Chat/AttachSheet.swift:586`: picking a photo runs `PHAsset.fetchAssets(withLocalIdentifiers:)` (`apple/Persona/Views/Chat/AttachSheet.swift:150`) once for every photo already picked.
- `apple/Persona/PhoneTools/PhotosTool.swift:64`: the hub's photo-listing tool fetches and enumerates on the main actor; `apple/Persona/PhoneTools/PhotosTool.swift:136-140` decodes the full photo, resizes it and encodes JPEG there too.
- `apple/Persona/PhoneTools/CalendarTool.swift:123-125`: `EKEventStore()` and the synchronous `events(matching:)` over the asked range on the main actor; a new `EKEventStore` per call (also lines 162, 232, 355, 378, 395, 404).
- `apple/Persona/Views/Assistant/Tasks/TodayCard.swift:190`: a new `EKEventStore` on the main actor each time the card's `.task` runs.
- `apple/Persona/Views/Session/ReviewModel.swift:124`: every change to a review's drafts reads and decodes the whole drafts file, then writes it (`apple/Persona/Views/Session/ReviewModel.swift:222`).
- Good: `apple/Persona/PhoneTools/ContactsTool.swift:124` (`nonisolated` fetch), `apple/Persona/Views/Chat/QuickLookFile.swift:227` (`Task.detached` file read).

**P2: keychain and UserDefaults**

- `apple/Persona/App/AppModel.swift:277`: `assistantCurrent` writes UserDefaults on every set, even when the value did not change; `apple/Persona/App/AppModel.swift:229` writes `dev.openChat` on every chat open. Each write recomputes every `@AppStorage` view (49 remain, most in `apple/Persona/Views/Session/SessionPanes.swift` and `apple/Persona/Views/Session/Terminal/TerminalExtras.swift`).
- `apple/PersonaWatch/PersonaWatchApp.swift:196`: the watch writes its whole seen-card list to UserDefaults on each refresh that finds new cards.
- `apple/Persona/Views/Machines/ScreenCredentials.swift:36`: a keychain read on the main actor when Screen Sharing opens (once per tap; minor).

**P3: `@State` initial values**

- `apple/Persona/Views/Chat/ChatView.swift:4168`: `AttachPanel()` built 17–20 times in one launch (its `init` is trivial; the waste is the repeated allocation).
- `apple/Persona/Views/Chat/AttachSheet.swift:173`: `AttachLibrary()` creates a `PHCachingImageManager` (`apple/Persona/Views/Chat/AttachSheet.swift:66`) for each discarded instance.
- `apple/Persona/Views/Settings/NotificationSettingsView.swift:16`: `ActivityAuthorizationInfo().areActivitiesEnabled`, a system query, on every init of the view.
- `apple/PersonaWatch/PersonaWatchApp.swift:6`: `WatchModel()` reads the keychain, reads and decodes the pet manifest (`apple/PersonaWatch/WatchPetView.swift:19`) and starts its polling loop in `init`, before the watch app's first frame.
- About 130 other `@State … = SomeClass()` were read: their `init`s only store values.

**P4: work in `body`**

- `apple/Persona/Views/Inbox/Lark/LarkInline.swift:303`: `cardMarkdown` compiles an `NSRegularExpression` on every call, and another per attribute (`apple/Persona/Views/Inbox/Lark/LarkInline.swift:310`), for every card node's `body` (`apple/Persona/Views/Inbox/Lark/LarkCard.swift:327`). 45 uncached `NSRegularExpression(` sites in all.
- `apple/Persona/Views/Knowledge/KnowledgeView.swift:1092`: a new `DateFormatter` per call, from `body` (`apple/Persona/Views/Knowledge/KnowledgeView.swift:826`); same in `apple/Persona/Views/Settings/ExtensionCodeViews.swift:382-389` (`ISO8601DateFormatter`, `RelativeDateTimeFormatter`).
- `apple/Shared/Models.swift:2505`: `storageText` creates a `ByteCountFormatter` on each read; 35 callers, many in rows and in a progress label (`apple/Persona/Views/Chat/VideoFileView.swift:145`).
- `apple/Persona/Views/Session/ReviewPage.swift:267-283`: rebuilds a file's diff rows, scans them for the widest line number and filters the drafts once per line, on every `body`.
- Counts: 22 `DateFormatter()`, 13 `ISO8601DateFormatter()`.
- Good: `ChatScratch`'s memoized lines (`apple/Persona/Views/Chat/ChatView.swift:880`), `TextMemo` (`apple/Shared/TextMemo.swift`), `InboxProjectionMemo`.

**P5 / P7: reads and high-frequency updates**

- `apple/Persona/Views/Chats/ChatsView.swift:4344`: every scroll frame writes `header.scrolled`, which `AgentsFoldingField` observes, so that field's `body` runs every frame, folded or not.
- `apple/Persona/App/AppModel.swift:1868-1872`: each `screen` event sets three observed properties with no pacing (who reads them not yet checked).
- Good: stream deltas are paced (`apple/Persona/App/DeltaPacer.swift`, 200 ms) and prepared off the main actor; between them only `StreamingText` redraws, a few characters a frame while text waits (`StreamRevealClock`, #6651); session lists coalesce for 25 ms; chat scroll values go to unobserved `ChatScratch`.

**P6: list identity**

- `apple/Persona/Views/Chat/ComposerParts.swift:168`: the composer's attached images use `id: \.offset`; removing one shifts every later row's identity.
- `apple/Persona/Views/Chat/MessageViews.swift:1037`: a message's Markdown chunks, and its images (`apple/Persona/Views/Chat/MessageViews.swift:332`, `apple/Persona/Views/Chat/MessageViews.swift:401`), use `id: \.offset`.
- `apple/Persona/Views/Inbox/Lark/LarkCard.swift:305-335`, `apple/Persona/Views/Inbox/Lark/LarkMessageContent.swift:176-198`: Lark cards and messages are trees of `AnyView` inside chat rows (37 of the 69 `AnyView(` uses).
- `apple/Persona/Views/Inbox/ConnectorThread/GitHubThreadView.swift:387`: re-parses a hunk's lines on every `body` and identifies them by offset.
- Counts: 131 `ForEach(Array(….enumerated()), id: \.offset)`, 145 `id: \.self`; most are over small constant arrays, which is fine.

**P8: images**

- `apple/Persona/Persona/PersonaStore.swift:566`: avatar decode and `preparingForDisplay()` on the main actor, from `body` (`apple/Persona/Persona/PersonaViews.swift:19`); `isOnPhone` checks the file system before the cache on every call.
- `apple/Persona/Views/Inbox/Lark/LarkAttachmentView.swift:33`: `UIImage(data:)` of the thumbnail in `body`, every render.
- `apple/Persona/Views/Session/CompareView.swift:249`: decodes both full images of a comparison from `body`, every render.
- `apple/Persona/Views/Chat/PhotoAttach.swift:35`: picked photos are decoded at full size, scaled and encoded to JPEG on the main actor (`MessagePhotos`); `apple/Persona/Views/Assistant/AssistantView.swift:1036-1042` does the same.
- `apple/PersonaWatch/WatchPetView.swift:169`: `UIImage(contentsOfFile:)` in a watch row's `body`.
- Good: `apple/Persona/Views/Inbox/Lark/LarkMedia.swift:110`, `apple/Persona/Views/Chat/ChatImageLoader.swift:34`, `apple/Persona/Views/Settings/AvatarUploadModel.swift:82`.

**P9: timers, loops and animations**

- `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:811`: `TimelineView(.animation)` at 30 fps with no `paused:`.
- `apple/PersonaWatch/PersonaWatchApp.swift:285-288`: an endless 8 s poll started in `init` from an unstructured `Task`; `try? await Task.sleep` swallows cancellation, so a cancelled task would spin.
- `apple/Persona/Views/Chat/ChatView.swift:669-675`: wakes the main actor every 500 ms while a dev chat is open to see whether the chrome has been idle; a single rescheduled deadline would do.
- `apple/Persona/Views/Chat/ChatPin.swift:203-206`: an 8 ms main-actor sleep loop as a hand-made display link while a pinned row rises (bounded; `CADisplayLink` would follow the screen's refresh).

**P10: before the first frame**

- `apple/Persona/App/AppModel.swift:2107`, `apple/Persona/App/AppModel.swift:3069`, `apple/Persona/App/AppModel.swift:3073` and `apple/Persona/App/AppModel.swift:3078`: the drafts, quotes and comments files are each read and decoded twice while `AppModel` initializes (six synchronous loads).
- `apple/Persona/Persona/PersonaStore.swift:157` and `apple/Persona/Persona/PersonaStore.swift:169`: persona index files read and decoded in `init`, from `AppModel.init` (`apple/Persona/App/AppModel.swift:328`).
- `apple/Persona/App/AppModel.swift:2834`: the cached snapshot (all machines, sessions and interactions) decodes on the main actor before the first frame. That is by design (it is what the first frame shows); its time grows with the number of sessions and needs a launch measurement.
- The app delegate's launch callback is light (`apple/Persona/App/PersonaApp.swift:115-125`).

**Instrumentation that exists**

`PerfTrace` / `PerfCount` (`apple/Persona/App/PerfSignpost.swift`), `MainThreadWatchdog`, `FrameStats`, `apple/PersonaUITests/PerfTestCase.swift` (`XCTHitchMetric`, scroll and navigation signpost metrics, `XCTClockMetric`), `PerfLaunchTests` (`XCTApplicationLaunchMetric`, no frame probe), `RenderBudgetTests`. The retired native Mac's instrumentation was removed with that target. No `OSSignposter` intervals, no MetricKit.

## Sources

Apple's sessions and documentation the rules distil:

- [Understanding hangs in your app](https://developer.apple.com/documentation/xcode/understanding-hangs-in-your-app): 100 ms is noticeable, tools report from 250 ms.
- [Improving app responsiveness](https://developer.apple.com/documentation/xcode/improving-app-responsiveness) and [Analyzing responsiveness issues in your shipping app](https://developer.apple.com/documentation/xcode/analyzing-responsiveness-issues-in-your-shipping-app).
- WWDC21 [Understand and eliminate hangs from your app](https://developer.apple.com/videos/play/wwdc2021/10258/), WWDC22 [Track down hangs with Xcode and on-device detection](https://developer.apple.com/videos/play/wwdc2022/10082/), WWDC23 [Analyze hangs with Instruments](https://developer.apple.com/videos/play/wwdc2023/10248/).
- WWDC21 [Demystify SwiftUI](https://developer.apple.com/videos/play/wwdc2021/10022/) (identity, lifetime, dependencies), WWDC23 [Demystify SwiftUI performance](https://developer.apple.com/videos/play/wwdc2023/10160/), WWDC25 [Optimize SwiftUI performance with Instruments](https://developer.apple.com/videos/play/wwdc2025/306/).
- WWDC23 [Discover Observation in SwiftUI](https://developer.apple.com/videos/play/wwdc2023/10149/), [Migrating from the Observable Object protocol to the Observable macro](https://developer.apple.com/documentation/swiftui/migrating-from-the-observable-object-protocol-to-the-observable-macro), [`State`](https://developer.apple.com/documentation/swiftui/state) (initial values evaluated on every view init; defer expensive objects to `.task`).
- WWDC25 [Embracing Swift concurrency](https://developer.apple.com/videos/play/wwdc2025/268/) (default MainActor isolation, `@concurrent`).
- WWDC20 [Eliminate animation hitches with XCTest](https://developer.apple.com/videos/play/wwdc2020/10077/) (hitch time ratio).
- WWDC19 [Optimizing App Launch](https://developer.apple.com/videos/play/wwdc2019/423/), [Reducing your app's launch time](https://developer.apple.com/documentation/xcode/reducing-your-app-s-launch-time).
- WWDC18 [iOS Memory Deep Dive](https://developer.apple.com/videos/play/wwdc2018/416/), WWDC21 [Detect and diagnose memory issues](https://developer.apple.com/videos/play/wwdc2021/10180/), [Gathering information about memory use](https://developer.apple.com/documentation/xcode/gathering-information-about-memory-use).
- WWDC18 [Measuring Performance Using Logging](https://developer.apple.com/videos/play/wwdc2018/405/), [Recording performance data](https://developer.apple.com/documentation/os/recording-performance-data) (signposts).
- [MetricKit](https://developer.apple.com/documentation/metrickit), WWDC20 [What's new in MetricKit](https://developer.apple.com/videos/play/wwdc2020/10081/), WWDC21 [Diagnose Power and Performance regressions in your app](https://developer.apple.com/videos/play/wwdc2021/10087/).
- [Reducing disk writes](https://developer.apple.com/documentation/xcode/reducing-disk-writes).
