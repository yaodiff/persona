import Foundation
import Observation
import PersonaWire
import UIKit

/// Hub-wide todo list management (docs/assistant/todos.md, issue #401):
/// The hub's todos, cached locally in StateCache, refreshed on {"type":"todos"} WS event and (re)connect.
/// Supports 404 gracefully (older hubs without todos support: hides entrance, no error).
@MainActor @Observable
final class TodoStore {
    static let shared = TodoStore(managers: .live, pushes: .live, followsScene: true)
    /// The app's own store once made, told when the tab in front changes (`frontChanged`) without being made for it.
    private static weak var sceneStore: TodoStore?

    private(set) var todos: [TodoItem] = [] {
        didSet {
            byID = Self.index(todos)
            let q = QuietTasks(todos)
            if q != quiet { quiet = q } // read by the badges and the Mac's menu bar: told only when it changes
            let f = Self.taskFollowers(todos)
            if f != taskFollowers { taskFollowers = f } // read by Move To…: told only when it changes (P5)
        }
    }
    /// The Agents that follow their task (#11391, rule 1 of the placement): the active Agents of the open to-dos a
    /// conversation supervises (a direction's leader or the main conversation), as "machine/session". Move To… is not
    /// offered for them or the sub-agent sessions under them; the hub's 409 `direction_supervised_task` covers a lag.
    private(set) var taskFollowers: Set<String> = []
    private(set) var byID: [String: TodoItem] = [:]
    /// The quiet tasks and their agents (#1731), which stay off the island, notifications and badges.
    private(set) var quiet = QuietTasks()
    private(set) var loading = false
    /// false when GET /api/v1/todos returned 404 (old hub without todos)
    private(set) var isSupported = true
    private(set) var spacesSupported = false
    private(set) var changingSpaces = false
    var error: String?

    // MARK: The V1 list's data (#10465, docs/apple/ios.md "To-dos (V1, #10465)")

    /// Each row's view by to-do id, as the last read of the list had them; none from an older manager. Kept apart from
    /// `todos`: a change to a view alone (a run's latest line) redraws nothing that reads the facts, and tells the watch
    /// and the widgets nothing (`hubListChanged`).
    private(set) var views: [String: TodoSummary] = [:]
    /// The list's counts as the hub sent them; nil from a hub without them. Pages read `countsSource`.
    private(set) var counts: TodoCounts?
    /// The places (`GET /api/v1/todos/places`), read with the list while V1 is on and the hub lists `todo_places`.
    private(set) var places: TodoPlaces?
    /// When the hub last answered a read of the list (kept with the views): the list says it while the hub is out of reach.
    private(set) var readAt: Date?
    /// How many reads of the list have begun: a write the hub answered is let go once a read begun after it has landed
    /// (`TodoOutbox`).
    @ObservationIgnored private(set) var readsBegun = 0
    /// The `readAt` the views' cache entry was last written with.
    @ObservationIgnored private var readAtKept: Date?
    /// Whether the views are known: kept from the last run, or a read of the list ended (answered or failed). Until
    /// then the V1 list draws nothing rather than filing its rows by the app's rules first and by the views a moment
    /// later.
    private(set) var viewsRead = false
    /// Every To-dos number the pages show (docs/apple/ios.md "Filing and counts"), counted while V1 is on.
    let countsSource: TodoCountsSource

    @ObservationIgnored private let reads = CoalescedRefresh()
    /// How a push is answered (`pushed`): the app's store waits between them, asks the revision first, and waits for the
    /// pages while they are not in front; a test's own store reads at once unless it says otherwise.
    @ObservationIgnored private let pushes: Pushes
    /// A push came after the pushed reads' loop last looked: it reads once more.
    @ObservationIgnored private var pushDirty = false
    @ObservationIgnored private var pushAPI: APIClient?
    @ObservationIgnored private var pushLoop: (id: Int, task: Task<Void, Never>)?
    @ObservationIgnored private var pushLoops = 0
    /// The wait for the pages while they are not in front: cancelled when one comes back.
    @ObservationIgnored private var hiddenWait: Task<Void, Never>?
    /// The hub-wide to-do revision the rows held are current at (#13804): from the list's own header, or a revision
    /// read made before the list read that landed. nil: not known, and the next push reads the list.
    @ObservationIgnored private(set) var revision: String?
    /// The revision a pushed read was told before it read the list, and how many reads had begun then: a read begun
    /// after that is at least as new, when its answer names none of its own.
    @ObservationIgnored private var revisionAsked: (tag: String, readsBefore: Int)?
    @ObservationIgnored private var mutationRevision = 0
    @ObservationIgnored private var activeReadRevision: Int?
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var lastPushed: [String: TodoItem]
    @ObservationIgnored private let cacheWriter: (@MainActor ([TodoItem]) -> Void)?
    @ObservationIgnored private let hubListChanged: @MainActor () -> Void
    @ObservationIgnored private let managers: Managers
    @ObservationIgnored private let saver = Debouncer(.seconds(1))
    @ObservationIgnored private let v1: V1
    /// The views' cache entry is rewritten as a run's line moves: at most once per `Timing.saveGap`, and at once when the
    /// scene leaves the front.
    @ObservationIgnored private let viewSaver: Throttle
    /// The moments the rows' views change by time alone (their `next_change_at`), soonest first, parsed once per
    /// change of the views while V1 is on (P4).
    @ObservationIgnored private var changes: [Date] = []
    @ObservationIgnored private var changeTimer: Task<Void, Never>?
    @ObservationIgnored private var timedFor: Date?
    /// The last moment the timer gave up on: its reads still said it (the hub's clock behind this one), so a push files it.
    @ObservationIgnored private var firedFor = Date.distantPast
    /// The moment the timer last read for, and how many times: a read that still says it is tried again later.
    @ObservationIgnored private var timerReads: (moment: Date, count: Int)?
    @ObservationIgnored private var lastTimerRead = Date.distantPast
    /// Ends the first V1 launch's wait for the views (`viewsRead`) when the first read is slow.
    @ObservationIgnored private var holdTimer: Task<Void, Never>?
    /// The hub listed `todo_places` in this run: its places are read on when a later hello leaves it out (a manager
    /// reconnecting, as `TodosV1` keeps the view), so the place the owner picked never widens under them; only a 404 or
    /// 405, or another hub, takes them away (plan S2-1).
    @ObservationIgnored private var placesListed = false
    /// The client the places route answered "not there" (404, 405): asked again only once the hello lists `todo_places`
    /// anew, or for another hub or assistant, not on every read of the list (a 304 included).
    @ObservationIgnored private var placesRefused: APIClient?
    /// Whether the last hello read listed `todo_places`: one that lists it again asks the route again.
    @ObservationIgnored private var placesFeature = false
    /// Counts again each `Timing.rulesTick` while a row is filed by the app's own rules, which read the clock.
    @ObservationIgnored private var rulesClock: Task<Void, Never>?
    /// The scene is in front: the timer runs only then (P9).
    @ObservationIgnored private var active = true
    @ObservationIgnored private var sceneObservers: [NSObjectProtocol] = []
    private static let stateName = "todo-items"
    static let viewStateName = "todo-view"

    /// What the V1 list needs besides the rows (#10465): whether V1 is on, the hub's to-do features, the sessions to
    /// file a row without a view, the views' own cache entry (written only while V1 is on), the client the
    /// `next_change_at` timer reads through (nil: the app's hub, as every automatic read), and how long things wait.
    struct V1 {
        var isOn: @MainActor () -> Bool
        var features: @MainActor () -> TodoFeatures
        /// The sessions of the given rows' Agents, each read through its own cell (`AppModel.session(_:)`), so the
        /// counts watch only theirs.
        var lookup: @MainActor ([TodoItem]) -> TodosLogic.SessionLookup?
        var loadCache: @MainActor () -> TodoViewCache?
        /// nil: take the entry away.
        var saveCache: (@MainActor (TodoViewCache?) -> Void)?
        /// The client the store's own reads go through (the timer's, V1 coming on); nil: the app's hub.
        var api: @MainActor () -> APIClient? = { nil }
        /// The time the rows are filed at: the app's own rules read it (tests move it).
        var now: @MainActor () -> Date = { Date() }
        /// The list's writes still on this device (`TodoOutbox.pending`): the rows and the numbers show them at once.
        var pending: @MainActor () -> [String: TodoCountsSource.Pending] = { [:] }
        /// The answers still on this device (`TodoOutbox.answering`): their asks count as answered at once.
        var answering: @MainActor () -> [String: Set<String>] = { [:] }
        /// The hub answered read `n` of the list (`readsBegun`), which has landed: the list's writes it shows are let go,
        /// those that could not reach it go again. Only while V1 is on: no write is made without it.
        var answered: @MainActor (_ read: Int) -> Void = { _ in }
        var timing = Timing()

        /// How long the store waits (tests make these short).
        struct Timing {
            /// How long after a row's `next_change_at` it is read again: the hub's clock may be a little behind this one.
            var slack: TimeInterval = 1
            /// A moment the timer's read still showed is read again after each of these, then left to the next push.
            var retries: [TimeInterval] = [5, 30]
            /// The timer never reads sooner than this after its last read, whatever moments the hub sends.
            var floor: TimeInterval = 5
            /// How long the first V1 launch without kept views waits for the first read before the rows are drawn by
            /// the app's own rules.
            var hold: TimeInterval = 1
            /// The views' cache entry: written this long after a quiet spell, then at most once per `saveGap`.
            var saveFirst: Duration = .seconds(2)
            var saveGap: Duration = .seconds(15)
            /// How often rows filed by the app's own rules are counted again, as today's list files them again.
            var rulesTick: TimeInterval = 60
        }

        static var live: V1 {
            .init(isOn: { TodosV1.shared.isOn }, features: { AppModel.shared.todoFeatures },
                  lookup: { rows in
                      let app = AppModel.shared
                      return TodosLogic.makeSessionLookup(TodosLogic.linkedSessions(of: rows, hubGroup: app.hub?.group ?? "",
                                                                                    session: { app.session($0) }))
                  },
                  loadCache: { StateCache.load(TodoStore.viewStateName, as: TodoViewCache.self) },
                  saveCache: { cache in
                      if let cache { StateCache.save(TodoStore.viewStateName, cache) } else { StateCache.remove(TodoStore.viewStateName) }
                  },
                  pending: { TodoOutbox.shared.pending },
                  answering: { TodoOutbox.shared.answering },
                  answered: { TodoOutbox.shared.hubAnswered(read: $0) })
        }
    }

    /// How the store answers the hub's `todos` pushes and the assistant's task feed (`pushed`). On a busy hub every
    /// report of a running Agent pushes, and the whole list can be megabytes: read on each, the Persona page kept the
    /// radio and a core busy. The other reads (connecting, switching hubs, a write's confirmation, pulling to refresh)
    /// never wait for any of this.
    struct Pushes {
        /// How long after a pushed read ends the next may begin: the pushes meanwhile are one read, made after it.
        var gap: Duration = .zero
        /// While pushes keep coming, each trailing read waits twice as long as the last, up to this (never below `gap`);
        /// a whole wait without a push goes back to `gap`. A busy Agent's reports, each changing the list, would
        /// otherwise read it every `gap` for as long as it runs.
        var maxGap: Duration = .zero
        /// While no page that shows to-dos is in front (`shown`), a push waits for one to come back, at most this long,
        /// so the counts and widgets still catch up. Zero: no wait.
        var hiddenWait: Duration = .zero
        /// Whether the hub's hello lists `todo_revision` (every manager answers `GET /api/v1/todos/revision`).
        var revisionListed: @MainActor () -> Bool = { false }
        /// Whether a page that shows to-dos (the Persona tab: its chat, To-dos and their details) is in front.
        var shown: @MainActor () -> Bool = { true }
        /// Asks whether the revision is still `known`; tests answer it themselves.
        var revision: @MainActor (APIClient, String) async throws -> TodoRevisionAnswer = { try await $0.todoRevision(known: $1) }
        /// Waits; tests wait on their own clock. Throws when cancelled.
        var sleep: @MainActor (Duration) async throws -> Void = { try await Task.sleep(for: $0) }

        /// Every push reads the list (a test's own store, unless it says otherwise).
        static var immediate: Pushes { Pushes() }
        /// The app's.
        static var live: Pushes {
            .init(gap: .seconds(2), maxGap: .seconds(10), hiddenWait: .seconds(30),
                  revisionListed: { AppModel.shared.hub?.hello.features.contains(TodoStore.revisionFeature) == true },
                  shown: { AppModel.shared.selectedTabNow == .assistant })
        }
    }
    static let revisionFeature = "todo_revision"

    /// Whose a to-do is on a hub with several managers (docs/assistant/managers.md "Pages"): the list is the hub's merged
    /// one, each to-do stamped with its `manager`; the pages list the current assistant's, a new one is made for it, and
    /// a change to one goes to its own manager.
    struct Managers {
        /// The current assistant's scope; nil with one manager (nothing is filtered or named).
        var scope: @MainActor () -> AssistantScope?
        /// The manager a request about a thing stamped with one names (`AppModel.itemManager`): none for the default's.
        var naming: @MainActor (String?) -> String?
        /// Whether the hub answered its roster in this run (`AppModel.rosterAsked`): before it has, a new to-do names
        /// no manager (`AppModel.namedAssistant`).
        var known: @MainActor () -> Bool = { true }
        /// The app's, asked each time: the store is first made while `AppModel.shared` restores what it saved.
        static var live: Managers {
            .init(scope: { AppModel.shared.assistantScope }, naming: { AppModel.shared.itemManager($0) },
                  known: { AppModel.shared.rosterAsked })
        }
        /// As `model` has them (the app's; a test's own model).
        static func of(_ model: AppModel) -> Managers {
            .init(scope: { [weak model] in model?.assistantScope }, naming: { [weak model] in model?.itemManager($0) },
                  known: { [weak model] in model?.rosterAsked == true })
        }
        /// One manager: a store of a test's own, which goes by no app's roster.
        static var one: Managers { .init(scope: { nil }, naming: { _ in nil }) }
    }

    /// Whose a new to-do is: the current assistant's, or the assistant's whose thing (an agent, a project's computer)
    /// it is made for, by that thing's `manager` stamp.
    enum Owner {
        case current
        case of(String?)
    }

    /// The manager a new to-do of `owner` is made for and stamped with; nil with one manager, and before the roster is
    /// read.
    func manager(of owner: Owner) -> String? {
        guard managers.known(), let scope = managers.scope() else { return nil }
        switch owner {
        case .current: return scope.current
        case .of(let stamp): return scope.owner(stamp)
        }
    }

    /// The open to-dos a thing stamped `manager` (an agent, a project's computer) can be linked to: its own
    /// assistant's, whichever is current (docs/assistant/managers.md "Pages"). All of them with one manager.
    func openTodos(of manager: String?) -> [TodoItem] {
        guard let scope = managers.scope() else { return todos.filter { !$0.isDone } }
        let owner = scope.owner(manager)
        return todos.filter { !$0.isDone && scope.owner($0.manager) == owner }
    }

    /// Reads the list unless it is still the one tagged (`APIClient.todos(etag:)`); tests answer it themselves.
    typealias Read = @MainActor (APIClient, String?) async throws -> TodosPage
    private let read: Read
    /// The last list read, its views and counts, and its tag: when the hub says it did not change (304), they are
    /// applied again, as a full read of them would have been, without reading them (#9590).
    private var lastRead: (etag: String, page: TodosPage)?

    /// `followsScene`: the app's own store, whose `next_change_at` timer stops while the scene is not in front.
    init(cachedTodos: [TodoItem]? = nil,
         cacheWriter: (@MainActor ([TodoItem]) -> Void)? = { StateCache.save(TodoStore.stateName, $0) },
         hubListChanged: @escaping @MainActor () -> Void = {
        AppModel.shared.summariesChanged() // its questions name their tasks
    }, managers: Managers = .one, read: @escaping Read = { try await $0.todos(etag: $1) }, v1: V1 = .live,
         pushes: Pushes = .immediate, followsScene: Bool = false) {
        self.read = read
        self.pushes = pushes
        self.managers = managers
        self.v1 = v1
        viewSaver = Throttle(first: v1.timing.saveFirst, gap: v1.timing.saveGap)
        countsSource = TodoCountsSource(scope: managers.scope, lookup: v1.lookup, now: v1.now, pending: v1.pending,
                                        answering: v1.answering)
        let initial = cachedTodos ?? StateCache.load(Self.stateName, as: [TodoItem].self) ?? []
        let initialIndex = Self.index(initial)
        todos = initial
        byID = initialIndex
        quiet = QuietTasks(initial)
        taskFollowers = Self.taskFollowers(initial) // as `todos`' didSet would, which a stored initial value skips
        lastPushed = initialIndex
        self.cacheWriter = cacheWriter
        self.hubListChanged = hubListChanged
        // The views kept with the rows they were read with. When none of them is for these rows (another hub's, or the
        // rows were forgotten and the views not yet), nothing of the entry is used.
        if v1.isOn(), let kept = v1.loadCache() {
            let mine = kept.views.filter { initialIndex[$0.key] != nil }
            if !mine.isEmpty || (kept.views.isEmpty && initial.isEmpty) {
                views = mine
                counts = kept.counts
                places = kept.places
                readAt = kept.readAt
                readAtKept = kept.readAt
                changes = Self.changeMoments(views)
                viewsRead = true
                // The numbers the last run showed, until this one counts (the current assistant is the app model's).
                countsSource.seed(kept.shownCounts, places: kept.shownPlaces)
            }
        }
        if followsScene {
            Self.sceneStore = self
            active = UIApplication.shared.applicationState == .active
            let center = NotificationCenter.default
            sceneObservers.append(center.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) {
                @Sendable [weak self] _ in MainActor.assumeIsolated { self?.sceneActive(true) }
            })
            sceneObservers.append(center.addObserver(forName: UIApplication.willResignActiveNotification, object: nil, queue: .main) {
                @Sendable [weak self] _ in MainActor.assumeIsolated { self?.sceneActive(false) }
            })
        }
        boundHold()
        // Switching assistants counts again inside the source: the clock follows whatever moved `byRules`.
        countsSource.byRulesChanged = { [weak self] in self?.armRulesClock() }
        // Counted once the app is made: the current assistant is the app model's, which may be making this store now.
        Task { @MainActor [weak self] in self?.recount() }
    }

    private static func index(_ list: [TodoItem]) -> [String: TodoItem] {
        var result: [String: TodoItem] = [:]
        for todo in list { result[todo.id] = todo }
        return result
    }

    /// `taskFollowers` of `list`: the hub's `openSupervisedTask` (not done, a supervisor named) and its active Agents.
    nonisolated static func taskFollowers(_ list: [TodoItem]) -> Set<String> {
        var out: Set<String> = []
        for todo in list where todo.doneAt == nil && !(todo.supervision?.supervisor ?? "").isEmpty {
            for agent in todo.agents where agent.isActive { out.insert(agent.machineID + "/" + agent.sessionID) }
        }
        return out
    }

    /// The to-dos the pages list: the current assistant's, on a hub with several. `todos` stays whole: an agent's to-do
    /// and a quiet task are found whichever assistant's they are, and another assistant's opens from a link as itself.
    var scopedTodos: [TodoItem] {
        guard let scope = managers.scope() else { return todos }
        return todos.filter { scope.has($0.manager) }
    }
    var openTodos: [TodoItem] { scopedTodos.filter { !$0.isDone } }
    /// The To-dos badge (the drawer, the Mac's sidebar): the current assistant's open todos, quiet tasks left out (#1731,
    /// the main conversation tells about them). The list itself still has them.
    var badgeCount: Int { scopedTodos.count(where: Self.countsForOwner) }

    /// Whether an open to-do counts in the badge: one the owner made (no `origin`), or one a conversation handed out that
    /// waits on the owner (a decision, a check only they can do, a delivery for them to accept). A handed-out task nothing
    /// works on is its supervisor's to pick up, not the owner's, so it is not counted, as the web's `ownerNeeds` leaves
    /// it out (docs/designs cards rule 2, #13702).
    static func countsForOwner(_ todo: TodoItem) -> Bool {
        guard !todo.isDone, !todo.quiet else { return false }
        if (todo.origin ?? "").isEmpty { return true }
        return todo.waitsForOwner || todo.reviewState == .userCheck || todo.delivery?.waitsForOwner == true
    }

    /// The manager a request about to-do `id` names: its own, as the list stamped it, whichever assistant is current
    /// (docs/protocol/protocol.md "Per-manager routing"); none for the default's, or one the list doesn't have.
    func named(_ id: String) -> String? { managers.naming(byID[id]?.manager) }

    /// The client a change to to-do `id` goes through: the one given (tests) or the hub's, naming the to-do's manager.
    private func client(for id: String, _ injected: APIClient?) -> APIClient? {
        (injected ?? AppModel.shared.api)?.manager(named(id))
    }

    /// An answer about one to-do comes from its manager without a stamp: it keeps the one the list gave it.
    private func stamped(_ answer: TodoItem, as manager: String?) -> TodoItem {
        guard answer.manager == nil, let manager else { return answer }
        var answer = answer
        answer.manager = manager
        return answer
    }

    func forget() {
        generation += 1
        lastRead = nil
        reads.reset()
        pushLoop?.task.cancel()
        pushLoop = nil
        hiddenWait?.cancel()
        hiddenWait = nil
        pushDirty = false
        pushAPI = nil
        revision = nil
        revisionAsked = nil
        activeReadRevision = nil
        loading = false
        changingSpaces = false
        todos = []
        error = nil
        isSupported = true
        spacesSupported = false
        persist([])
        views = [:]
        counts = nil
        places = nil
        placesListed = false
        placesRefused = nil
        placesFeature = false
        readAt = nil
        readAtKept = nil
        viewsRead = false
        changes = []
        firedFor = .distantPast
        timerReads = nil
        armTimer()
        if v1.isOn() { countsSource.reset() }
        armRulesClock()
        // Whether V1 is on or not (it may have been for the last hub), and now: a write of the last hub's views still
        // waiting is replaced, so it cannot land after this.
        if let save = v1.saveCache {
            viewSaver.run { save(nil) }
            viewSaver.flush()
        }
        boundHold()
    }

    /// V1 came on (a hello listed the view, or the owner picked a hub whose last hello did) or went off (another hub's
    /// pages, without it): what only V1 reads is brought up to date from what the last read had, or let go.
    func v1Changed() {
        let on = v1.isOn()
        changes = on ? Self.changeMoments(views) : []
        armTimer()
        if on {
            recount()
            persistView()
            boundHold()
            reload(api: v1.api()) // and the places, which only V1 reads
        } else {
            countsSource.reset()
            holdTimer?.cancel()
            armRulesClock()
        }
    }

    /// Automatic invalidations share in-flight reads, without adding delay to unclassified todo/feed hints.
    /// The returned task covers this invalidation and at most one dirty follow-up, not an endless event pump.
    @discardableResult
    func reload(api: APIClient? = nil) -> Task<Void, Never> {
        reads.invalidate { [self] in await fetch(api: api) }
    }

    /// The hub pushed `todos`, or the assistant's feed brought a task, receipt, review or report (`TasksStore`): the
    /// list may have changed. Answered by `pushes`: the first push after a quiet spell reads at once; the pushes during
    /// a read and the wait after it are one read after that wait (so the last state always lands), the wait doubling
    /// from `gap` to `maxGap` while they keep coming and back to `gap` after a whole wait without one. While the hub
    /// lists `todo_revision` the cheap revision is asked first and an unchanged one reads nothing; while no page that
    /// shows to-dos is in front, a push waits for one (`hiddenWait`).
    func pushed(api: APIClient? = nil) {
        if let api { pushAPI = api }
        pushDirty = true
        guard pushLoop == nil else { return }
        pushLoops += 1
        let id = pushLoops, requested = generation
        let task = Task { [self] in
            defer { if pushLoop?.id == id { pushLoop = nil } }
            var wait = pushes.gap
            while pushDirty, generation == requested, !Task.isCancelled {
                if !pushes.shown(), pushes.hiddenWait > .zero {
                    let held = Task { [self] in _ = try? await pushes.sleep(pushes.hiddenWait) }
                    hiddenWait = held
                    await held.value
                    if hiddenWait == held { hiddenWait = nil }
                    guard generation == requested, !Task.isCancelled else { return }
                }
                pushDirty = false
                await pushedRead(api: pushAPI, generation: requested)
                guard generation == requested, !Task.isCancelled else { return }
                // The pushes during the read and this wait are one read after it.
                if wait > .zero { try? await pushes.sleep(wait) }
                // Still coming: the next read waits longer. A whole wait without one ends the loop, and the next push
                // starts again from `gap`.
                if pushDirty { wait = min(wait * 2, max(pushes.maxGap, pushes.gap)) }
            }
        }
        pushLoop = (id, task)
    }

    /// Whether a pushed read is under way or waiting (tests).
    var pushing: Bool { pushLoop != nil }

    /// Waits until the pushed reads are done (tests).
    func pushesSettled() async {
        while let loop = pushLoop { await loop.task.value }
    }

    /// The tab in front changed (`AppModel.selectedTab`): a push waiting for the pages reads now if they are back.
    static func frontChanged() { sceneStore?.shownChanged() }

    func shownChanged() {
        guard pushes.shown() else { return }
        hiddenWait?.cancel()
    }

    /// One pushed read: the revision first while the hub has it and one is known (unchanged: nothing to read; a failure
    /// of any kind, an older manager's 409 included: the list), then the list as every automatic read does.
    private func pushedRead(api injected: APIClient?, generation requested: Int) async {
        if pushes.revisionListed(), let known = revision, let api = (injected ?? AppModel.shared.api)?.manager(nil) {
            let answer = try? await pushes.revision(api, known)
            guard generation == requested, !Task.isCancelled else { return }
            switch answer {
            case .unchanged?: return
            case .changed(let tag?)?: revisionAsked = (tag, readsBegun)
            default: break
            }
        }
        await reload(api: injected).value
    }

    /// Manual refresh shares an eligible read, without starting a parallel GET or adding delay.
    func refresh(api: APIClient? = nil) async {
        let staleRead = activeReadRevision.map { $0 != mutationRevision } ?? false
        await reads.refresh(afterCurrent: staleRead) { [self] in await fetch(api: api) }
    }

    private func confirmWrite(api: APIClient) async {
        mutationRevision += 1
        await reads.refresh(afterCurrent: true) { [self] in await fetch(api: api) }
    }

    /// The hub's merged list, every manager's: read with none named, also after a change that named one.
    private func fetch(api injected: APIClient? = nil) async {
        guard let api = (injected ?? AppModel.shared.api)?.manager(nil) else { return }
        let requestedGeneration = generation
        let requestedRevision = mutationRevision
        readsBegun += 1
        let readNumber = readsBegun
        activeReadRevision = requestedRevision
        loading = true
        defer {
            if generation == requestedGeneration {
                loading = false
                activeReadRevision = nil
            }
        }
        do {
            let answer = try await read(api, lastRead?.etag)
            guard generation == requestedGeneration, mutationRevision == requestedRevision, !Task.isCancelled else { return }
            let page: TodosPage
            if answer.list != nil {
                page = answer
                lastRead = answer.etag.map { ($0, answer) }
            } else if let kept = lastRead?.page {
                page = kept
            } else {
                return // a 304 to a tag this store did not send
            }
            // The revision these rows are current at: the answer's own, else one asked before this read began; none
            // when neither (the next push reads the list).
            let asked = revisionAsked.flatMap { readNumber > $0.readsBefore ? $0.tag : nil }
            revision = answer.revision ?? asked
            if asked != nil { revisionAsked = nil }
            let list = page.list ?? []
            isSupported = true
            spacesSupported = TodosLogic.supportsSpaces(list)
            let at = v1.now()
            readAt = at
            apply(list, recounting: false)
            applyView(page.views, page.counts)
            // When it was read is kept with the views, at least once a minute when nothing else changed: a relaunch while
            // offline says the time the hub last answered, not the last time a view changed.
            if readAtKept.map({ at.timeIntervalSince($0) >= 60 }) ?? true { persistView() }
            recount()
            error = nil
            viewsRead = true
            // Facts only: a view's change alone is no news for the watch and the widgets.
            if byID != lastPushed {
                lastPushed = byID
                hubListChanged()
            }
            await readPlaces(api, listChanged: answer.list != nil, generation: requestedGeneration)
            if v1.isOn(), generation == requestedGeneration { v1.answered(readNumber) }
        } catch let apiErr where TodosLogic.isOldHub(apiErr) {
            guard generation == requestedGeneration, mutationRevision == requestedRevision, !Task.isCancelled else { return }
            // Older hub without todos: hide entrance without error
            isSupported = false
            spacesSupported = false
            error = nil
            viewsRead = true
        } catch {
            guard generation == requestedGeneration, mutationRevision == requestedRevision, !Task.isCancelled else { return }
            if let shown = error.shown { self.error = shown }
            viewsRead = true
        }
    }

    private func apply(_ list: [TodoItem], recounting: Bool = true) {
        guard list != todos else { return }
        todos = list
        persist(list)
        if recounting { recount() }
    }

    /// The views and counts of a read, each set only when it changed. Kept while V1 is off too, so V1 coming on in this
    /// run has them; their moments are parsed, and they are written down, only while it is on.
    private func applyView(_ newViews: [String: TodoSummary], _ newCounts: TodoCounts?) {
        let viewsChanged = newViews != views, countsChanged = newCounts != counts
        if viewsChanged { views = newViews }
        if countsChanged { counts = newCounts }
        guard v1.isOn() else { return }
        if viewsChanged { changes = Self.changeMoments(newViews) }
        if viewsChanged || countsChanged { persistView() }
        armTimer()
    }

    /// The places, read with the list while V1 is on and the hub has listed them in this run: again only when the list
    /// changed (a 304 changes none of its numbers). A hub or manager without the route ("not there"), or a hub that has
    /// not listed them, has none (All only), and is not asked again until its hello lists them anew; any other failure
    /// keeps the last until the next read.
    private func readPlaces(_ api: APIClient, listChanged: Bool, generation requested: Int) async {
        guard v1.isOn() else { return }
        let listed = v1.features().places
        if listed, !placesFeature { placesRefused = nil }
        placesFeature = listed
        if listed { placesListed = true }
        guard placesListed else {
            guard places != nil else { return }
            places = nil
            persistView()
            recount()
            return
        }
        guard !api.sameSettingsScope(as: placesRefused), listChanged || places == nil else { return }
        do {
            let read = try await api.todoPlaces()
            guard generation == requested, !Task.isCancelled, read != places else { return }
            places = read
        } catch let error where TodoNotThere.matches(error) {
            guard generation == requested else { return }
            placesRefused = api
            guard places != nil else { return }
            places = nil
        } catch {
            return
        }
        persistView()
        recount()
    }

    /// Read when the write is made, so a burst of reads is written once, with the numbers they ended on.
    private func persistView() {
        guard v1.isOn(), let save = v1.saveCache else { return }
        readAtKept = readAt
        viewSaver.run { [weak self] in
            guard let self else { return }
            save(TodoViewCache(views: views, counts: counts, places: places, shownCounts: countsSource.counts,
                               shownPlaces: countsSource.places, readAt: readAt))
        }
    }

    /// The first V1 launch without kept views waits for the first read, so its rows are not filed twice, but no longer
    /// than `Timing.hold` (a hub that does not answer, or no connection yet): then the rows are drawn by the app's own
    /// rules, and filed again when the views come.
    private func boundHold() {
        holdTimer?.cancel()
        holdTimer = nil
        guard v1.isOn(), !viewsRead else { return }
        let requested = generation
        holdTimer = Task { [weak self, wait = v1.timing.hold] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled, let self, generation == requested else { return }
            viewsRead = true
        }
    }

    /// Whether the minute clock for rows filed by the app's own rules runs (tests read it).
    var rulesClockRuns: Bool { rulesClock != nil }

    /// Counts again while V1 is on (nothing reads the numbers before).
    private func recount() {
        guard v1.isOn() else { return }
        countsSource.update(.init(rows: todos, views: views, counts: counts, places: places, now: v1.now()))
        armRulesClock()
    }

    /// While a counted row is filed by the app's own rules (an older manager's, or a view this build cannot read), it
    /// is counted again each `Timing.rulesTick` the scene is in front: those rules read the clock, and no
    /// `next_change_at` says when.
    private func armRulesClock() {
        let due = v1.isOn() && active && countsSource.byRules
        guard due != (rulesClock != nil) else { return }
        rulesClock?.cancel()
        rulesClock = nil
        guard due else { return }
        rulesClock = Task { [weak self, tick = v1.timing.rulesTick] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(tick))
                guard !Task.isCancelled, let self else { return }
                countsSource.tick()
            }
        }
    }

    // MARK: Rows that change by time alone (docs/apple/ios.md "The data")

    /// The `next_change_at` moments of `views`, soonest first.
    private static func changeMoments(_ views: [String: TodoSummary]) -> [Date] {
        views.values.compactMap { $0.nextChangeAt.flatMap(JSON.parseDate) }.sorted()
    }

    /// One timer, to the earliest moment a row's view changes by time alone, then the list is read again (coalesced
    /// with any other read) through the app's hub: only while V1 is on and the scene is in front. A moment already
    /// passed (the scene was away) is read at once. A read that still says it (the hub's clock behind this one) is
    /// tried again after each of `Timing.retries`, then left to the next push; and the timer never reads sooner than
    /// `Timing.floor` after its last read.
    private func armTimer() {
        let due = v1.isOn() && active ? changes.first { $0 > firedFor } : nil
        guard due != timedFor else { return }
        changeTimer?.cancel()
        timedFor = due
        guard let due else {
            changeTimer = nil
            return
        }
        let timing = v1.timing
        let tries = timerReads?.moment == due ? timerReads?.count ?? 0 : 0
        var at = due.addingTimeInterval(timing.slack)
        if tries > 0, tries <= timing.retries.count { at = max(at, lastTimerRead.addingTimeInterval(timing.retries[tries - 1])) }
        at = max(at, lastTimerRead.addingTimeInterval(timing.floor))
        let wait = max(0, at.timeIntervalSinceNow)
        changeTimer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled, let self else { return }
            lastTimerRead = Date()
            if tries >= timing.retries.count {
                firedFor = due
                timerReads = nil
            } else {
                timerReads = (due, tries + 1)
            }
            timedFor = nil
            changeTimer = nil
            reload(api: v1.api())
        }
    }

    /// The scene came to the front or left it: the timer stops while it is away, and the views' cache is written now.
    func sceneActive(_ active: Bool) {
        guard active != self.active else { return }
        self.active = active
        if !active { viewSaver.flush() }
        armTimer()
        armRulesClock()
    }

    private func persist(_ list: [TodoItem]) {
        guard let cacheWriter else { return }
        saver.run { cacheWriter(list) }
    }

    func todo(id: String) -> TodoItem? {
        byID[id]
    }

    /// The id to-do `id`'s view lists for its decision asked at `round` (the accept route's `asked_at`), so an answer
    /// from the Needs You tray moves the numbers as one from its row does; "" when its view lists none.
    func askID(todo id: String, round: String) -> String {
        let moment = JSON.parseDate(round)
        return views[id]?.asks?.first { ask in
            guard let r = ask.round else { return false }
            return r == round || (moment != nil && JSON.parseDate(r) == moment)
        }?.id ?? ""
    }

    /// A new to-do is the current assistant's (or `owner`'s): its manager is named, and the answer stamped with it.
    func create(title: String, notes: String? = nil, due: APIClient.TodoPlanChange.Due? = nil, owner: Owner = .current,
                api injected: APIClient? = nil) async throws -> TodoItem {
        let made = manager(of: owner)
        guard let api = (injected ?? AppModel.shared.api)?.manager(managers.naming(made)) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        let created = try await stamped(api.createTodo(title: title, notes: notes, due: due), as: made)
        guard generation == targetGeneration else { return created }
        mutationRevision += 1
        var updated = todos
        updated.insert(created, at: 0)
        apply(updated)
        return created
    }

    func update(id: String, title: String? = nil, notes: String? = nil, done: Bool? = nil, api injected: APIClient? = nil) async throws -> TodoItem {
        guard let api = client(for: id, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        let owner = byID[id]?.manager
        let res = try await stamped(api.updateTodo(id: id, title: title, notes: notes, done: done), as: owner)
        guard generation == targetGeneration else { return res }
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == id }) {
            updated[idx] = res
        } else {
            updated.insert(res, at: 0)
        }
        apply(updated)
        return res
    }

    /// Changes a todo's due date, reminder or priority (#1683).
    @discardableResult
    func updatePlan(id: String, _ change: APIClient.TodoPlanChange, api injected: APIClient? = nil) async throws -> TodoItem {
        guard let api = client(for: id, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        let owner = byID[id]?.manager
        let res = try await stamped(api.updateTodoPlan(id: id, change), as: owner)
        guard generation == targetGeneration else { return res }
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == id }) {
            updated[idx] = res
        } else {
            updated.insert(res, at: 0)
        }
        apply(updated)
        return res
    }

    /// Moves a to-do to another due date (#2045: dragged on the calendar, or its "Change Due Date…"): drawn there at
    /// once, before the hub answers; a failed save puts it back and says why. The task ends with whether it was saved.
    @discardableResult
    func reschedule(_ id: String, to due: APIClient.TodoPlanChange.Due, api: APIClient? = nil) -> Task<Bool, Never> {
        guard let before = todo(id: id) else { return Task { false } }
        let targetGeneration = generation
        // Optimistic, as toggleDone: a read already in flight cannot replace it.
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == id }) {
            updated[idx] = TodoCalendar.moved(before, to: due)
            apply(updated)
        }
        return Task {
            guard generation == targetGeneration else { return false }
            do {
                try await updatePlan(id: id, APIClient.TodoPlanChange(due: due), api: api)
                return true
            } catch {
                putBack(before, generation: targetGeneration, error)
                return false
            }
        }
    }

    /// Files to-do `id` under a knowledge-base project (its slug, "" for none; the To-dos list's menu, #10465): drawn there
    /// at once, as `reschedule` moves a due date; a failed save puts it back and says why. The task ends with whether it
    /// was saved.
    @discardableResult
    func refile(_ id: String, project: String, api: APIClient? = nil) -> Task<Bool, Never> {
        guard let before = todo(id: id) else { return Task { false } }
        let targetGeneration = generation
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == id }) {
            updated[idx].project = project.isEmpty ? nil : project
            apply(updated)
        }
        return Task {
            guard generation == targetGeneration else { return false }
            do {
                try await updatePlan(id: id, APIClient.TodoPlanChange(project: project), api: api)
                return true
            } catch {
                putBack(before, generation: targetGeneration, error)
                return false
            }
        }
    }

    /// Surfaces a failed explicit to-do write (`toggleDone`, `reschedule`, `refile`, `delete`): `AppModel.show`
    /// silences timeouts under `isQuiet` (intended for retried background reads), so a timed-out write that rolled
    /// back instead shows an explicit error toast.
    static func reportWriteFailure(_ error: Error,
                                   show: @MainActor (Error) -> Void = { AppModel.shared.show($0) },
                                   toast: @MainActor (Toast) -> Void = { AppModel.shared.toast = $0 }) {
        if error.isTimeout {
            let message = (error as? APIError)?.businessMessage
                ?? String(localized: "The request timed out. Try again later.")
            toast(Toast(message, style: .error))
            Haptics.error()
            return
        }
        show(error)
    }

    /// A change drawn before the hub answered failed: the to-do back as it was (only it, so what else changed meanwhile
    /// stays), and why, felt as well as shown (offline has its own haptic, in `notDone`).
    private func putBack(_ before: TodoItem, generation targetGeneration: Int, _ error: Error) {
        guard generation == targetGeneration else { return }
        mutationRevision += 1
        var back = todos
        if let idx = back.firstIndex(where: { $0.id == before.id }) {
            back[idx] = before
            apply(back)
        }
        Self.reportWriteFailure(error)
    }

    /// `api` is the client to write through (tests pass a fake hub's); the app uses the current hub connection.
    /// Closes an open to-do or reopens a closed one. `done` says which when the row knows better than `doneAt`: a failed
    /// start waiting on the owner reads open and is closed for good (#3713, `TodoTriageLogic.checksAsOpen`).
    func toggleDone(_ todo: TodoItem, done: Bool? = nil, api: APIClient? = nil,
                    onFailure: (@MainActor (Error) -> Void)? = nil) {
        let targetGeneration = generation
        let newDone = done ?? !todo.isDone
        let before = byID[todo.id] ?? todo
        // Optimistic update; a read already in flight cannot replace it.
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == todo.id }) {
            updated[idx].doneAt = newDone ? Date() : nil
            updated[idx].startError = nil // closed by the owner, or reopened: no longer failed (the hub does the same)
            apply(updated)
        }
        Task {
            guard generation == targetGeneration else { return }
            do {
                _ = try await update(id: todo.id, done: newDone, api: api)
            } catch {
                // Revert only this to-do on failure so concurrent updates to other to-dos during a long confirmation
                // wait stay intact.
                guard generation == targetGeneration else { return }
                mutationRevision += 1
                var back = todos
                if let idx = back.firstIndex(where: { $0.id == before.id }) {
                    back[idx] = before
                    apply(back)
                }
                if let onFailure { onFailure(error) } else { Self.reportWriteFailure(error) }
            }
        }
    }

    /// Marks a to-do seen (Got It, or opening it: #5707, docs/assistant/todo-status-sop.md "Seen") or, with `seen` false,
    /// takes the mark off (Undo). The list moves it at once (principle 7); the hub's mark takes the place of the local
    /// one, and a failure puts back the mark it had and throws. `after`: the moment that put it in Just finished, so the
    /// mark shown before the hub answers is not earlier than it on a clock behind the hub's. Only the mark is taken from
    /// the answer: the list's own fields (its ask) are not in it.
    func setSeen(id: String, _ seen: Bool, after: Date = .distantPast, api injected: APIClient? = nil) async throws {
        guard let api = client(for: id, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        let before = byID[id]?.seenAt
        markSeen(id, seen ? max(Date(), after) : nil)
        do {
            let res = try await api.setTodoSeen(id: id, seen: seen)
            guard generation == targetGeneration else { return }
            markSeen(id, res.seenAt)
        } catch {
            if generation == targetGeneration { markSeen(id, before) }
            throw error
        }
    }

    /// Sets to-do `id`'s seen mark in the list; a read already in flight cannot put the old one back.
    private func markSeen(_ id: String, _ at: Date?) {
        guard let idx = todos.firstIndex(where: { $0.id == id }) else { return }
        mutationRevision += 1
        var updated = todos
        updated[idx].seenAt = at
        apply(updated)
    }

    func delete(id: String, api injected: APIClient? = nil) async throws {
        guard let api = client(for: id, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        try await api.deleteTodo(id: id)
        guard generation == targetGeneration else { return }
        mutationRevision += 1
        var updated = todos
        updated.removeAll { $0.id == id }
        apply(updated)
    }

    /// Someday on to-do `id`, back on `until` (nil: on no set day); `clearSomeday` brings it back now (#10226,
    /// docs/assistant/todos.md "Restores and Someday"). Waits for the hub; the row's view follows with the read that
    /// confirms it. Only for a hub that lists `todo_view` (an older one answers 404).
    func setSomeday(id: String, until: Date?, api injected: APIClient? = nil) async throws {
        guard let api = client(for: id, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        try await confirmed(id, api: api) { try await $0.setSomeday(id: id, until: until) }
    }

    func clearSomeday(id: String, api injected: APIClient? = nil) async throws {
        guard let api = client(for: id, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        try await confirmed(id, api: api) { try await $0.clearSomeday(id: id) }
    }

    /// Applies a write's returned to-do immediately, then confirms it with the same list read as other confirmed writes.
    func applyConfirmed(_ result: TodoItem, api: APIClient) async throws {
        try await confirmed(result.id, api: api) { _ in result }
    }

    /// Restore on an inactive to-do: the hub's, so every device sees it (the app's own rules keep theirs on the device,
    /// `TodoRestores`, for hubs without `todo_view`).
    func restore(id: String, api injected: APIClient? = nil) async throws {
        guard let api = client(for: id, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        try await confirmed(id, api: api) { try await $0.restore(id: id) }
    }

    /// A write whose answer is the to-do: it takes the row's place (keeping the list's stamp), and the list is read again
    /// for the view, which the answer does not carry.
    private func confirmed(_ id: String, api: APIClient, _ write: (APIClient) async throws -> TodoItem) async throws {
        let targetGeneration = generation
        let res = try await stamped(write(api), as: byID[id]?.manager)
        guard generation == targetGeneration else { return }
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == id }) { updated[idx] = res } else { updated.insert(res, at: 0) }
        apply(updated)
        await confirmWrite(api: api)
    }

    /// One of the list's writes held on the device (`TodoOutbox`), sent with its op id through the to-do's own assistant
    /// (`manager`, its stamp when the list no longer has it; a new one's, the assistant it is made for). Its answer takes
    /// the row's place, and the list is read again for the view. Answers the new to-do's id for a create.
    /// `answered`: told as the hub answers, before the row is put in its place, with the new to-do's id and how many reads
    /// had begun: the outbox keeps it on disk as answered (never sent again), and lets it go once a later read lands.
    func write(_ write: TodoOutbox.Write, todo id: String, manager stamp: String?, opID: UUID,
               answered: @MainActor (TodoOutbox.Answer) -> Void = { _ in }, api injected: APIClient? = nil) async throws -> String? {
        let owner = byID[id]?.manager ?? stamp
        guard let api = (injected ?? AppModel.shared.api)?.manager(managers.naming(owner)) else {
            throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]"))
        }
        let targetGeneration = generation
        var created: String?
        switch write {
        case .done(let done):
            let res = try await stamped(api.setTodoDone(id: id, done: done, opID: opID), as: owner)
            answered(.init(read: readsBegun))
            guard generation == targetGeneration else { return nil }
            replace(res)
        case .someday(let on):
            let res = try await stamped(on ? api.setSomeday(id: id, until: nil, opID: opID) : api.clearSomeday(id: id, opID: opID),
                                        as: owner)
            answered(.init(read: readsBegun))
            guard generation == targetGeneration else { return nil }
            replace(res)
        case .delete:
            do {
                try await api.deleteTodo(id: id, opID: opID)
            } catch where TodoNotThere.deleted(error) {
                // Gone already (deleted on another device, or a repeat whose first answer was lost): what this one asked for.
            }
            answered(.init(read: readsBegun))
            guard generation == targetGeneration else { return nil }
            mutationRevision += 1
            apply(todos.filter { $0.id != id })
        case .create(let title, let notes, let dueAt, let dueDate, let project):
            let res = try await stamped(api.createTodo(title: title, notes: notes, dueAt: dueAt, dueDate: dueDate, project: project,
                                                       opID: opID), as: stamp)
            // Before its row is put in: the one recount that follows sees the hub's row and no longer the drawn one.
            answered(.init(created: res.id, read: readsBegun))
            guard generation == targetGeneration else { return res.id }
            created = res.id
            replace(res)
        case .answer(let a):
            try await answer(a, todo: id, opID: opID, api: api)
            answered(.init(read: readsBegun))
            guard generation == targetGeneration else { return nil }
        }
        await confirmWrite(api: api)
        return created
    }

    /// An answer to one of to-do `id`'s own asks (#10465; proposal 3.7): through the asks route while the hub lists
    /// `todo_asks`, else today's accept route (a decision, by its round) or retry route. A manager that reconnected at an
    /// older version answers the asks route 404 or 405 without the route's codes (`TodoAskRefusal.routeMissing`): today's
    /// routes then, which take no op id. A step's ask lifted onto its parent's row goes to the asks route on the parent,
    /// which names it, and to today's routes on the step itself. The view that shows it comes with the read after it.
    private func answer(_ a: TodoOutbox.AskAnswer, todo id: String, opID: UUID, api: APIClient) async throws {
        if v1.features().asks, !a.ask.isEmpty {
            do {
                _ = try await api.answerAsk(todo: id, ask: a.ask, a.body, opID: opID)
                return
            } catch where TodoAskRefusal.routeMissing(error) {}
        }
        let target = a.step ?? id
        if a.kind == OwnerAskKind.retry {
            _ = try await api.retryTodo(target)
            return
        }
        guard let round = a.round else {
            // No round to answer by (an ask from a newer manager the accept route cannot name): nothing went.
            throw APIError(status: 409, message: String(localized: "Already handled"), fields: ["code": TodoAskRefusal.stale])
        }
        try await api.acceptTodo(target, askedAt: round, option: a.option, response: a.response)
    }

    /// A fixed answer from the Needs You tray ("You Decide", …) to to-do `id`'s decision asked at `round`, which is not
    /// held: through the asks route with an op id of its own while the hub lists `todo_asks` and the view names the
    /// decision, else (or where the manager lacks the route) today's accept route.
    func respond(todo id: String, round: String, response: String, api injected: APIClient? = nil) async throws {
        guard let api = (injected ?? AppModel.shared.api)?.manager(managers.naming(byID[id]?.manager)) else {
            throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]"))
        }
        let ask = askID(todo: id, round: round)
        if v1.features().asks, !ask.isEmpty {
            do {
                _ = try await api.answerAsk(todo: id, ask: ask, TodoAskAnswer(response: response), opID: UUID())
                return
            } catch where TodoAskRefusal.routeMissing(error) {}
        }
        try await api.acceptTodo(id, askedAt: round, response: response)
    }

    /// `item` in its row's place, a new one first; a read already in flight cannot put the old one back.
    private func replace(_ item: TodoItem) {
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == item.id }) { updated[idx] = item } else { updated.insert(item, at: 0) }
        apply(updated)
    }

    func linkAgent(todoID: String, machineID: String, sessionID: String, api injected: APIClient? = nil) async throws {
        guard let api = client(for: todoID, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        do {
            try await api.linkTodoAgent(todoID: todoID, machineID: machineID, sessionID: sessionID)
        } catch {
            if generation == targetGeneration {
                await confirmWrite(api: api)
            }
            throw error
        }
        if generation == targetGeneration {
            await confirmWrite(api: api)
        }
    }

    func unlinkAgent(todoID: String, machineID: String, sessionID: String, api injected: APIClient? = nil) async throws {
        guard let api = client(for: todoID, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        do {
            try await api.unlinkTodoAgent(todoID: todoID, machineID: machineID, sessionID: sessionID)
        } catch {
            if generation == targetGeneration {
                await confirmWrite(api: api)
            }
            throw error
        }
        if generation == targetGeneration {
            await confirmWrite(api: api)
        }
    }

    /// Membership changes immediately; the hub then supplies the authoritative Agent attachments.
    @discardableResult
    func setSpace(todoID: String, space: TodoItem.Space, linked: Bool, api injected: APIClient? = nil) async throws -> Bool {
        guard !changingSpaces else { throw APIError(status: 409, message: String(localized: "Updating projects. Try again in a moment.")) }
        guard let api = client(for: todoID, injected) else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        changingSpaces = true
        defer { if generation == targetGeneration { changingSpaces = false } }
        mutationRevision += 1
        let original = todos
        let via = "\(space.machineID)/\(space.space)"
        var optimistic = todos
        for index in optimistic.indices {
            guard linked || optimistic[index].id == todoID else { continue }
            optimistic[index].spaces?.removeAll { $0.machineID == space.machineID && $0.space == space.space }
            if !linked || optimistic[index].id != todoID {
                optimistic[index].agents.removeAll { $0.source == "space" && $0.via == via }
            }
            if linked && optimistic[index].id == todoID {
                optimistic[index].spaces = (optimistic[index].spaces ?? []) + [space]
            }
        }
        apply(optimistic)
        do {
            if linked {
                try await api.linkTodoSpace(todoID: todoID, machineID: space.machineID, space: space.space)
            } else {
                try await api.unlinkTodoSpace(todoID: todoID, machineID: space.machineID, space: space.space)
            }
            guard generation == targetGeneration else { return true }
            await confirmWrite(api: api)
            return true
        } catch {
            guard generation == targetGeneration else { throw error }
            mutationRevision += 1
            apply(original)
            if TodosLogic.isLegacySpacesRoute(error) {
                spacesSupported = false
                let upgrade = APIError(status: 404, message: String(localized: "This hub can't attach a whole group yet. Update the hub and try again."))
                self.error = upgrade.message
                throw upgrade
            }
            let message = error.shown
            self.error = message
            await confirmWrite(api: api)
            guard generation == targetGeneration else { throw error }
            self.error = message
            throw error
        }
    }

    func link(_ target: TodoLinkTarget, to todoID: String, api: APIClient? = nil) async throws {
        switch target {
        case .space(let project):
            try await setSpace(todoID: todoID, space: project, linked: true, api: api)
        case .agent(let machine, let session, _):
            try await linkAgent(todoID: todoID, machineID: machine, sessionID: session, api: api)
        }
    }

    func unlink(_ target: TodoLinkTarget, from todoID: String, api: APIClient? = nil) async throws {
        switch target {
        case .space(let project):
            try await setSpace(todoID: todoID, space: project, linked: false, api: api)
        case .agent(let machine, let session, _):
            try await unlinkAgent(todoID: todoID, machineID: machine, sessionID: session, api: api)
        }
    }

    /// Creation failure throws; a later link failure returns a message that preserves the created todo.
    func createAndLink(title: String, target: TodoLinkTarget, owner: Owner = .current, api: APIClient? = nil) async throws -> String? {
        if target.isSpace && changingSpaces {
            throw APIError(status: 409, message: String(localized: "Updating projects. Try again in a moment."))
        }
        let targetGeneration = generation
        let created = try await create(title: title, owner: owner, api: api)
        guard generation == targetGeneration else { return nil }
        do {
            try await link(target, to: created.id, api: api)
            return nil
        } catch {
            return TodosLogic.createdLinkFailure(error, isSpace: target.isSpace)
        }
    }
}

/// What the V1 list draws besides the rows, kept on disk beside them (`TodoStore.viewStateName`, written only while V1
/// is on): a launch draws the list filed as the hub filed it, with its numbers, before the first read.
nonisolated struct TodoViewCache: Codable, Equatable, Sendable {
    var views: [String: TodoSummary] = [:]
    var counts: TodoCounts?
    var places: TodoPlaces?
    /// The numbers and places the pages showed (`TodoCountsSource`, the current assistant's), drawn in the first frame.
    var shownCounts: TodoCounts?
    var shownPlaces: [TodoPlace]?
    /// When the hub last answered a read of the list (`TodoStore.readAt`).
    var readAt: Date?
}
