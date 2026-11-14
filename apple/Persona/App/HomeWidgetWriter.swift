#if os(iOS)
import Foundation
import WidgetKit

/// Writes the Home Screen widgets' snapshot (docs/apple/home-widgets.md「Data: the snapshot」): what the app shows, in
/// the few numbers and rows the widgets draw, into the App Group. The widget extension reads only that file: it never
/// reaches the hub and never reads the keychain.
///
/// - Coalesced: what the watch's summary follows (`AppModel.summariesChanged`) asks for a write; the write comes at most
///   every `pause`, built from the app's state as it is then (P7).
/// - Only on change: the bytes are compared without the time; the same content is not written again, except once a
///   `heartbeat` so the widgets do not call it stale while the app runs. Then only the kinds whose part changed reload
///   (`WidgetCenter` budgets reloads; the heartbeat runs only in the foreground, where reloads are not budgeted).
/// - Off the main thread: the file is written on a background task.
@MainActor
final class HomeWidgetWriter {
    static let shared = HomeWidgetWriter()
    static let pause: Duration = .seconds(5)
    static let heartbeat: TimeInterval = 20 * 60

    /// What to write: the app's state (`AppModel.homeSnapshot()`); DEBUG `-showHomeWidgetSample` writes the sample.
    var source: () -> HomeSnapshot? = { nil }
    /// The meeting being recorded, as the recording's Live Activity follows it (`RecordingActivity`); a change writes at
    /// once, so the Record widget turns into Stop as soon as the recording starts.
    var recording: HomeSnapshot.Recording? {
        didSet { if recording != oldValue { flush() } }
    }

    #if DEBUG
    /// `-showHomeWidgetSample YES`: write the gallery's made-up snapshot instead of the app's; `ticked`: the same with
    /// two to-dos just ticked on the widget, one on its way and one the hub refused.
    private var sample: String?

    func showSample(_ which: String) {
        sample = which
        flush()
    }
    #endif

    private var pending: Task<Void, Never>?
    private var written: (body: Data, parts: [HomeKind: Data], at: Date)?
    private let store: HomeSnapshotStore

    init(store: HomeSnapshotStore = .shared) {
        self.store = store
    }

    /// Whether the hub knows this device has the widgets (nil: not told since launch).
    private var toldHub: Bool?

    /// Tells the hub whether any of the widgets is on this device's screens (`widgets` in its prefs), when that changed
    /// since it last told it: the hub wakes the app for them while it is not open (#5721). From the foreground.
    func tellHub() {
        guard let api = AppModel.shared.api, !api.config.isDemo else { return }
        let kinds = Set(HomeKind.allCases.map(\.widgetKind))
        let byName = Dictionary(uniqueKeysWithValues: HomeKind.allCases.map { ($0.widgetKind, $0) })
        WidgetCenter.shared.getCurrentConfigurations { result in
            guard case .success(let infos) = result else { return }
            let has = infos.contains { kinds.contains($0.kind) }
            let shown = Set(infos.compactMap { byName[$0.kind] })
            Task { @MainActor in
                HomeWidgetData.shared.installed = shown
                HomeWidgetData.shared.refresh()
                let writer = HomeWidgetWriter.shared
                guard writer.toldHub != has else { return }
                do {
                    try await api.setHasWidgets(device: PushRegistrar.deviceID, has)
                    writer.toldHub = has
                } catch {
                    HomeSnapshotStore.log.error("telling the hub about the widgets: \(error.localizedDescription, privacy: .public)")
                }
            }
        }
    }

    /// Asks for a write within `pause`.
    func schedule() {
        guard pending == nil else { return }
        pending = Task { [weak self] in
            try? await Task.sleep(for: Self.pause)
            guard !Task.isCancelled else { return }
            self?.pending = nil
            self?.write()
        }
    }

    /// Writes now (the app is going to the background, a recording started or stopped).
    func flush() {
        pending?.cancel()
        pending = nil
        write()
    }

    private func write(now: Date = .now) {
        #if DEBUG
        let zh = LanguageStore.shared.launched.hasPrefix("zh")
        let made: HomeSnapshot? = switch sample {
        case nil: source()
        case "ticked": HomeSamples.ticked(zh: zh, now: now)
        default: HomeSamples.busy(zh: zh, now: now)
        }
        #else
        let made = source()
        #endif
        guard var snapshot = made else { return }
        snapshot.recording = recording ?? snapshot.recording
        snapshot.at = 0
        guard let body = Self.encode(snapshot) else { return }
        if let written, written.body == body, now.timeIntervalSince(written.at) < Self.heartbeat { return }
        let parts = Self.parts(snapshot)
        let changed = HomeKind.allCases.filter { written?.parts[$0] != parts[$0] }
        let kinds = written?.body == body ? HomeKind.allCases : changed
        snapshot.at = now.timeIntervalSince1970
        guard let data = Self.encode(snapshot) else { return }
        self.written = (body, parts, now)
        let store = store
        Task.detached(priority: .utility) {
            do {
                try store.write(data)
            } catch {
                HomeSnapshotStore.log.error("widget snapshot not written: \(error.localizedDescription, privacy: .public)")
                return
            }
            await MainActor.run {
                for kind in kinds { WidgetCenter.shared.reloadTimelines(ofKind: kind.widgetKind) }
            }
        }
    }

    nonisolated static func encode(_ s: HomeSnapshot) -> Data? {
        let e = JSONEncoder()
        e.outputFormatting = .sortedKeys
        return try? e.encode(s)
    }

    /// Each kind's part of the snapshot, to tell which widgets changed. Pairing and the persona count for all.
    nonisolated static func parts(_ s: HomeSnapshot) -> [HomeKind: Data] {
        func data<T: Encodable>(_ value: T) -> Data { (try? JSONEncoder.sorted.encode(value)) ?? Data() }
        struct Common: Encodable { var paired: Bool; var look: HomeSnapshot.Look }
        let common = data(Common(paired: s.paired, look: s.look))
        let parts: [HomeKind: Data] = [
            .agents: data(s.agents) + data(s.machines.map { [$0.id, $0.name] }),
            .today: data(s.today),
            .todos: data(s.todos),
            .usage: data(s.usage),
            .computers: data(s.machines),
            .record: data(s.recording) + data(s.today),
            .health: data(s.health),
            .inbox: data(s.inbox),
        ]
        return parts.mapValues { common + $0 }
    }
}

nonisolated private extension JSONEncoder {
    static var sorted: JSONEncoder {
        let e = JSONEncoder()
        e.outputFormatting = .sortedKeys
        return e
    }
}

/// `persona://widget/<kind>`: a tap on a widget opens the page it comes from.
enum HomeWidgetLink {
    static func kind(_ url: URL) -> HomeKind? {
        guard url.scheme == "persona", url.host() == "widget" else { return nil }
        return HomeKind(rawValue: url.lastPathComponent)
    }
}

extension AppModel {
    /// What the widgets' writer writes (`HomeWidgetWriter.source`).
    func homeSnapshotSource() -> HomeSnapshot? { homeSnapshot() }

    /// Where a widget's tap lands: Agents and the Inbox on their tabs, the Record widget on the recordings; the others
    /// on the app as it was (their pages come with their widgets).
    func openWidget(_ kind: HomeKind) {
        switch kind {
        case .agents: selectedTab = .chats
        case .inbox: selectedTab = .mail
        case .record: MeetingRecorder.openRecordings()
        case .today, .todos: showShellPage(.todos)
        case .usage, .computers: showShellPage(.machines)
        case .health: showShellPage(.health)
        }
    }

    /// A page of the Persona page in front (the iPhone's shell, or the iPad's sidebar), as the recordings open: launched
    /// by the widget, the app may have no shell yet, so it tries again for a few seconds.
    func showShellPage(_ page: ShellPage) {
        shellSettings = false
        selectedTab = .assistant
        func route() -> Bool {
            if let shell = tabRouter as? ShellState {
                shell.show(page)
                shell.drawer.close()
                return true
            }
            if let pad = tabRouter as? PadRouter {
                pad.show(page)
                return true
            }
            return false
        }
        guard !route() else { return }
        Task {
            for _ in 0..<10 {
                try? await Task.sleep(for: .milliseconds(500))
                if route() { return }
            }
        }
    }

    /// The widgets' snapshot of what the app shows now: the same rows and counts as the Agents tab and the Inbox
    /// (docs/apple/home-widgets.md「What each number counts」). Nil in Demo Mode: the widgets say to connect.
    func homeSnapshot() -> HomeSnapshot? {
        var s = HomeSnapshot()
        s.paired = isPaired && !isDemo
        guard s.paired else { return s }
        let look = fleetLook
        s.look = .init(name: persona.name, package: look.package)
        let sessions = scoped(allSessions)
        let byKey = Dictionary(sessions.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        let names = Dictionary(machines.map { ($0.id, $0.name) }, uniquingKeysWith: { a, _ in a })
        let tasks = TasksStore.shared
        func place(_ key: SessionKey) -> TabBadges.Place {
            TabBadges.place(key, session: byKey[key], quiet: isQuiet(key), mutes: pinsStore(key.group)?.mutes)
        }
        s.agents = HomeAgents.build(
            needsYou: AgentsNeedsYou.rows(cards: scopedInteractions, waiting: AgentsNeedsYou.waiting(sessions)),
            sessions: sessions, place: place, session: { byKey[$0] }, machineName: { names[$0] },
            isTask: { tasks.task(of: $0) != nil })
        // Persona answering in one of its own conversations: the face thinks (`FleetPersona`'s `assistant`).
        s.agents.assistantAnswering = sessions.contains { $0.assistant && $0.state == .working }
        let loads = Dictionary(mergedFleet().stats?.loads.map { ($0.id, $0.percent) } ?? [], uniquingKeysWith: { a, _ in a })
        s.machines = machines.prefix(8).map { m in
            let rows = s.agents.rows.filter { $0.machine == m.id }
            return .init(id: m.id, name: m.name, online: m.online, working: rows.count { $0.state == .working },
                         needsYou: rows.count { $0.state == .needsYou }, load: loads[m.id], icon: m.icon)
        }
        s.inbox = HomeInbox.build(InboxStore.shared)
        let data = HomeWidgetData.shared
        let now = Date()
        s.today = HomeToday.build(calendar: data.events, days: data.days, hub: HomeToday.hubMeetings(TasksStore.shared, now: now), now: now)
        s.todos = HomeTodos.build(TodoStore.shared.todos, ticks: HomeWidgetActions.ticks, now: now)
        let stats = mergedFleet().stats
        s.usage = HomeUsage.build(UsageWindows.live(UsageWindowsStore.shared.accounts, now: now), tokens: stats?.tokens ?? 0,
                                  tpm: stats?.tpm ?? 0, hourly: data.hourly, spent: data.spentToday(now))
        s.health = data.health
        return s
    }
}

/// Today's events: the phone's calendar when Persona may read it, else the hub's meetings on today's card (Feishu's),
/// else none, which the widget tells how to change.
enum HomeToday {
    /// `days`, the phone's calendar over the weeks around today, goes only with its own events: the hub's meetings are
    /// today's alone.
    static func build(calendar: [HomeSnapshot.Event]?, days: HomeSnapshot.Days? = nil, hub: [HomeSnapshot.Event]?, now: Date) -> HomeSnapshot.Today? {
        if let calendar { return .init(events: calendar, source: .calendar, days: days) }
        if let hub { return .init(events: hub, source: .hub) }
        return .init(events: [], source: nil)
    }

    /// The rest of today: the events on today not yet over, all-day ones first, then by start, at most `Today.cap`.
    nonisolated static func rest(_ events: [HomeSnapshot.Event], now: Date, calendar: Calendar = .current) -> [HomeSnapshot.Event] {
        let start = calendar.startOfDay(for: now).timeIntervalSince1970
        let end = HomeDays.nextMidnight(after: now, calendar: calendar).timeIntervalSince1970
        let t = now.timeIntervalSince1970
        return Array(events
            .filter { $0.start < end && ($0.end > start || $0.start >= start) && ($0.allDay || $0.end > t) }
            .sorted { ($0.allDay ? 0 : 1, $0.start) < ($1.allDay ? 0 : 1, $1.start) }
            .prefix(HomeSnapshot.Today.cap))
    }

    /// The meetings on today's card in any conversation's feed (`TodayBody.meetings`); nil when no card is today's.
    static func hubMeetings(_ store: TasksStore, now: Date) -> [HomeSnapshot.Event]? {
        let day = now.formatted(.iso8601.year().month().day())
        guard let today = store.feeds.values.lazy.flatMap({ $0 }).compactMap(\.today).first(where: { $0.date == day }) else { return nil }
        return (today.meetings ?? []).filter { $0.end > now }.sorted { $0.start < $1.start }.prefix(HomeSnapshot.Today.cap).map {
            .init(title: $0.title, start: $0.start.timeIntervalSince1970, end: $0.end.timeIntervalSince1970)
        }
    }
}

/// Today's to-dos as the To-dos page's Today filter holds them (`TodoListLogic.matches`: due today, and open ones
/// overdue), only the open ones: overdue first, then by when they are due, the high-priority ones before the rest.
enum HomeTodos {
    static let shown = 8

    static func build(_ todos: [TodoItem], ticks: [String: HomeSnapshot.Tick], now: Date, calendar: Calendar = .current) -> HomeSnapshot.Todos {
        let open = todos.filter { !$0.isDone && TodoListLogic.matches($0, .today, now: now, calendar: calendar) }
        func key(_ t: TodoItem) -> (Int, Date, Int) {
            (TodosLogic.isOverdue(t, now: now, calendar: calendar) ? 0 : 1,
             TodoListLogic.due(t, calendar: calendar) ?? .distantFuture, t.priority == "high" ? 0 : 1)
        }
        let sorted = open.sorted { key($0) < key($1) }
        return .init(items: sorted.prefix(shown).map { t in
            .init(id: t.id, title: t.title, due: t.dueAt?.timeIntervalSince1970,
                  overdue: TodosLogic.isOverdue(t, now: now, calendar: calendar), tick: ticks[t.id])
        }, count: open.count, overdue: open.count { TodosLogic.isOverdue($0, now: now, calendar: calendar) })
    }
}

/// The subscription windows as the Machines page lists them (live ones, the fullest first, stale ones marked), today's
/// tokens and tokens a minute as the island's, and the hours.
enum HomeUsage {
    /// The widgets draw three at most; Edit Widget lists every account kept here (as the snapshot keeps eight computers).
    static let kept = 8

    static func build(_ accounts: [UsageAccount], tokens: Int, tpm: Int, hourly: [Int], spent: Double? = nil) -> HomeSnapshot.Usage? {
        guard !accounts.isEmpty || tokens > 0 else { return nil }
        return .init(accounts: accounts.prefix(kept).map { a in
            .init(id: a.id, name: a.name, windows: a.windows.map {
                .init(label: $0.label, minutes: $0.windowMinutes, percent: $0.usedPercent, resetsAt: $0.resetsAt?.timeIntervalSince1970)
            }, stale: a.stale)
        }, tokensToday: tokens, tpm: tpm, hourly: hourly, costToday: spent)
    }

    /// The launchers whose quota may carry money: those with a gateway (an address here, or on a computer for an rc
    /// file's), but not the built-in claude, a direct API connection (it has no quota) or a hidden one.
    static func gatewayLaunchers(_ all: [LauncherView]) -> [String] {
        all.filter { l in
            guard !l.builtin, l.kind != "api", l.hidden != true else { return false }
            let here = l.baseURL.map { !$0.isEmpty } ?? false
            return here || (l.details ?? [:]).values.contains { $0.baseURL.map { !$0.isEmpty } ?? false }
        }
        .map(\.name)
    }

    /// Today's money as the gateways report it (docs/apple/home-widgets.md「What each number counts」): the `cost_usd` of
    /// the usage entries that say they are the day's, never an entry known only by its label or an estimate. A key two
    /// launchers share counts once (the same gateway and key name); nil when no gateway says.
    static func spentToday(_ answers: [LauncherQuotaAnswer]) -> Double? {
        var seen = Set<String>()
        var total: Double?
        for (i, q) in answers.compactMap(\.quota).enumerated() {
            let key = q.keyName.flatMap { $0.isEmpty ? nil : q.gateway + "\u{1}" + $0 } ?? "#\(i)"
            guard seen.insert(key).inserted else { continue }
            for u in q.usage ?? [] where u.period == LauncherQuota.Usage.day {
                if let cost = u.costUSD { total = (total ?? 0) + cost }
            }
        }
        return total
    }
}

/// Ticking a to-do off from a widget (`CompleteTodoIntent`, docs/apple/home-widgets.md「Ticking off a to-do」): shown
/// done at once, then the hub is told; a refusal marks it failed, and the widget says so and offers the tap again.
enum HomeWidgetActions {
    /// To-dos ticked on a widget that the hub has not taken yet (`ticking`) or refused (`failed`).
    static var ticks: [String: HomeSnapshot.Tick] = [:]

    static func complete(todoID: String) async {
        ticks[todoID] = .ticking
        HomeWidgetWriter.shared.flush()
        do {
            _ = try await TodoStore.shared.update(id: todoID, done: true)
            ticks[todoID] = nil
        } catch {
            ticks[todoID] = .failed
            HomeSnapshotStore.log.error("ticking a to-do off from a widget: \(error.localizedDescription, privacy: .public)")
        }
        HomeWidgetWriter.shared.flush()
    }
}

/// The Agents widget's rows, as the Agents tab lists them: who waits for you first (oldest first, one row per session,
/// `AgentsNeedsYou`), then who works (longest first), then who finished and is unread (newest first); only what the
/// tab's badge counts (`TabBadges.place`: no muted agents, no quiet task's agents). Past `HomeSnapshot.Agents.cap` rows
/// are counted, not listed.
enum HomeAgents {
    static func build(needsYou: [AgentsNeedsYou.Row], sessions: [Session], place: (SessionKey) -> TabBadges.Place,
                      session: (SessionKey) -> Session?, machineName: (String) -> String?,
                      isTask: (SessionKey) -> Bool) -> HomeSnapshot.Agents {
        var rows: [HomeSnapshot.AgentRow] = []
        for r in needsYou where place(r.key) == .agents {
            let s = session(r.key)
            rows.append(.init(id: r.key.machineID + "/" + r.key.sessionID, title: s?.displayTitle ?? r.cards.first?.title ?? "",
                              state: .needsYou, kind: isTask(r.key) ? .assistant : .coding, machine: r.key.machineID,
                              place: machineName(r.key.machineID), since: r.since?.timeIntervalSince1970,
                              ask: r.cards.first?.title ?? InteractionKind.screen.title))
        }
        let listed = Set(rows.map(\.id))
        func others(_ state: AgentState) -> [Session] {
            sessions.filter { !$0.assistant && $0.countedState == state && place($0.key) == .agents
                && !listed.contains($0.machineID + "/" + $0.sessionID) }
        }
        let working = others(.working).sorted { ($0.stateSince ?? .distantFuture) < ($1.stateSince ?? .distantFuture) }
        let done = others(.unread).sorted { ($0.stateSince ?? .distantPast) > ($1.stateSince ?? .distantPast) }
        for (list, state) in [(working, HomeSnapshot.AgentState.working), (done, .done)] {
            for s in list {
                rows.append(.init(id: s.machineID + "/" + s.sessionID, title: s.displayTitle, state: state,
                                  kind: isTask(s.key) ? .assistant : .coding, machine: s.machineID,
                                  place: machineName(s.machineID), since: (s.stateSince ?? s.updatedAt)?.timeIntervalSince1970,
                                  detail: state == .working ? s.activity : nil))
            }
        }
        var out = HomeSnapshot.Agents()
        out.rows = Array(rows.prefix(HomeSnapshot.Agents.cap))
        for row in rows.dropFirst(HomeSnapshot.Agents.cap) {
            switch row.state {
            case .needsYou: out.more.needsYou += 1
            case .working: out.more.working += 1
            case .done: out.more.done += 1
            }
        }
        return out
    }
}

/// The Inbox widget's numbers and its newest messages, as the Inbox counts them (`InboxStore.actionCount`, its badge).
enum HomeInbox {
    static let shown = 5

    static func build(_ store: InboxStore) -> HomeSnapshot.Inbox {
        let open = store.scopedItems.filter { !$0.isDone }.sorted { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }
        return .init(needsYou: store.actionCount, unread: store.unreadCount, latest: open.prefix(shown).map { item in
            .init(id: item.id, sender: item.sender ?? item.title, summary: item.summary ?? item.preview ?? item.title,
                  at: (item.updatedAt ?? item.startedAt ?? .now).timeIntervalSince1970, urgent: item.urgent == true,
                  needsYou: item.triage == .action)
        })
    }
}
#endif
