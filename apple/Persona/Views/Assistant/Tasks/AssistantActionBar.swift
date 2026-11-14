import PersonaKit
import SwiftUI

// The row above the assistant's composer on iPhone, iPad and Mac (#3393, docs/assistant/unified-entry.md "Needs you and In
// progress: each thing once"): two entries, Needs You and In Progress, each thing in exactly one of them. The split is
// `AssistantEntries`, a port of the web's `assistantEntries` (web/src/lib/assistant-entries.ts, Android's
// AssistantEntries.kt, #3387) replaying the shared fixture spec/fixtures/entries/cases.json. Each entry opens a tray above
// the bar, one at a time; the entries are drawn as the old dock's capsules (`DockCapsule`), so the bar is as low.

/// One of the bar's entries, and the tray it opens.
nonisolated enum ActionTray: String, CaseIterable, Identifiable, Sendable {
    case needsYou = "needs-you", running

    var id: String { rawValue }
}

/// Which entry each task, Agent, plan and message card goes to (#3387). A port of the web's `assistantEntries`; a change
/// to the rule changes all three platforms and the shared fixture.
nonisolated enum AssistantEntries {
    /// A task's status as the web's `todoStatus` reads it (first that holds wins).
    enum Status: String, Sendable {
        case done, needsYou = "needs_you", interrupted, usageLimit = "usage_limit", unread, working, waiting
        case followUp = "follow_up", notStarted = "not_started", failed

        /// Where it sorts (the web's `TODO_STATUS_RANK`): what the user must act on first.
        var rank: Double {
            switch self {
            case .needsYou: 0
            case .interrupted: 1
            case .usageLimit: 1.5
            case .failed: 3
            case .working: 4
            case .waiting: 4.5
            case .notStarted: 5
            case .unread: 6
            case .followUp: 7
            case .done: 8
            }
        }

        /// The app's task state for the row's mark and words.
        var taskState: TaskState {
            switch self {
            case .done: .done
            case .needsYou: .needsYou
            case .interrupted, .usageLimit, .followUp: .followup
            case .unread: .unread
            case .working: .working
            case .waiting: .waiting
            case .notStarted: .notStarted
            case .failed: .failed
            }
        }
    }

    enum Row: Equatable, Sendable, Identifiable {
        /// A task: its status and how many agents its row carries (they are never rows of their own).
        case task(id: String, status: Status, agents: Int)
        /// An Agent that belongs to no task (the main conversation's only).
        case agent(DockAgent)
        /// A plan waiting for Start, one row whatever its task count.
        case plan(id: String)
        /// A message card waiting for Send: its feed item, and the inbox item the reply goes to.
        case message(feedID: String, itemID: String)

        /// The fixture's row key.
        var id: String {
            switch self {
            case .task(let id, _, _): "task:" + id
            case .agent(let a): "agent:\(a.key.machineID)/\(a.key.sessionID)"
            case .plan(let id): "plan:" + id
            case .message(let feedID, _): "message:" + feedID
            }
        }
    }

    struct Result: Equatable, Sendable {
        var needsYou: [Row] = []
        var running: [Row] = []
        /// Open tasks with no progress for 48 hours and nothing running: folded under In Progress, not counted.
        var shelved: [Row] = []
    }

    /// A task's status, failed too when only its plan says the start failed (a hub from before #1077): the web's
    /// `entryStatus` over `todoStatus`.
    static func status(_ todo: TodoItem, lookup: TodosLogic.SessionLookup?, plans: [String: TaskPlan]) -> Status {
        let st = todoStatus(todo, lookup: lookup)
        guard st == .notStarted, todo.agents.isEmpty, let plan = todo.planID.flatMap({ plans[$0] }),
              let error = plan.tasks.first(where: { $0.todoID == todo.id })?.error, !error.isEmpty else { return st }
        return .failed
    }

    static func todoStatus(_ todo: TodoItem, lookup: TodosLogic.SessionLookup?) -> Status {
        if todo.failedToStart { return .failed }
        if todo.doneAt != nil { return .done }
        let active = todo.activeAgents
        if active.isEmpty { return .notStarted }
        var interrupted = false, limit = false, unread = false, working = false, waiting = false
        for a in active {
            if let e = a.startError, !e.isEmpty { continue } // a failed start counts as stopped
            let s = lookup?(a.machineID, a.sessionID)
            let st = s?.state ?? .offline
            if st == .needsYou { return .needsYou }
            let stopped = st == .unread || st == .idle
            let error = !(s?.turnError ?? "").isEmpty
            if error, s?.turnErrorKind == "usage_limit", stopped { limit = true }
            else if error, stopped { interrupted = true }
            else if st == .unread { unread = true }
            else if s?.waitsToBeWoken == true { waiting = true }
            else if st == .working || s?.starting == true { working = true }
        }
        if interrupted { return .interrupted }
        if limit { return .usageLimit }
        if unread { return .unread }
        if working { return .working }
        if waiting { return .waiting }
        return .followUp
    }

    /// The open ask, as the web reads it (`openAsk`): one with a verdict and a time.
    static func openAsk(_ todo: TodoItem) -> TodoAsk? {
        guard let a = todo.ask, !(a.verdict ?? "").isEmpty, !a.askedAt.isEmpty else { return nil }
        return a
    }

    /// It waits for the user's review while nothing of it runs or asks (the web's `awaitsReview`): rank 2.
    static func rank(_ todo: TodoItem, _ status: Status) -> Double {
        let resting = status == .followUp || status == .unread || status == .notStarted
        return resting && (todo.reviewState == .pendingReview || openAsk(todo) != nil) ? 2 : status.rank
    }

    /// The task waits for the user's decision or review: an open ask (a decided one only while the task is open), or its
    /// supervisor's `pending_review` / `needs_user` on an open task (the web's `taskAwaitsDecision`).
    static func awaitsDecision(_ todo: TodoItem) -> Bool {
        if let ask = openAsk(todo) { return !(todo.doneAt != nil && ask.outcome == "decide") }
        let state = todo.supervision?.state
        return todo.doneAt == nil && (state == "pending_review" || state == "needs_user")
    }

    /// The message card still waits for Send by the hub's fields alone: not sent, not filed (the web's `messageWaits`).
    static func messageWaits(_ item: FeedItem) -> Bool {
        item.kind == .message && item.ref != nil && item.sentAt == nil && item.filedAt == nil
    }

    /// Splits a conversation's things into the two entries (the web's `assistantEntries`). `main`: the main
    /// conversation's id (it watches every task and the Agents opened by hand); `messageWaits`: whether a message card
    /// still waits for Send (the app adds the inbox's word and Later); `decisionHidden`: a decision put off with Later or
    /// taken out with Don't handle (the web's `deferred` and `dismissal_id`); `restored`: tasks brought back from shelved;
    /// `going`: the agents going on (`AppModel.goingAgents`), else read from `sessions`, which then must be every session.
    @MainActor static func split(conversation: String, main: String?, group: String = "", todos: [TodoItem], sessions: [Session],
                                 interactions: [Interaction], plans: [String: TaskPlan], feed: [FeedItem], now: Date = .now,
                                 restored: [String: Date] = [:], going: [DockAgent]? = nil,
                                 messageWaits: (FeedItem) -> Bool = AssistantEntries.messageWaits,
                                 decisionHidden: (TodoItem) -> Bool = { _ in false }) -> Result {
        let lookup = TodosLogic.makeSessionLookup(sessions)
        let isMain = !(main ?? "").isEmpty && main == conversation
        // A function, not a closure value: Swift 6.2.4 (Xcode 26.3) rejects capturing a closure value in the `listed`
        // closure below ("sending 'pair' risks causing data races").
        func pair(_ m: String, _ s: String) -> String { m + "/" + s }
        var asksOf: [String: [Interaction]] = [:]
        for ix in interactions.sorted(by: { ($0.createdAt ?? .distantPast, $0.id) < ($1.createdAt ?? .distantPast, $1.id) }) {
            asksOf[pair(ix.machineID, ix.sessionID), default: []].append(ix)
        }

        // The conversation's tasks (the web's `taskCardSlots`): the main conversation's every to-do, another's the ones it
        // dispatched, plus those its plans and feed name. `at` is when its card came: its plan's, else the to-do's.
        var at: [String: Date] = [:]
        for plan in plans.values.sorted(by: { $0.id < $1.id }) where plan.conversation == conversation {
            for t in plan.tasks { if let id = t.todoID, at[id] == nil { at[id] = plan.createdAt ?? .distantPast } }
        }
        let byID = Dictionary(todos.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        for item in feed where [.task, .receipt, .review, .report].contains(item.kind) {
            if let id = item.ref, at[id] == nil { at[id] = byID[id]?.createdAt ?? item.at }
        }
        for todo in todos where (isMain || todo.origin == conversation) && at[todo.id] == nil {
            at[todo.id] = todo.createdAt ?? .distantPast
        }

        typealias Ranked = (row: Row, rank: Double, at: Date)
        /// The agents of the tasks listed as rows: folded into them, never rows of their own, whoever made the to-do.
        var folded = Set<String>()
        var asking: [(row: Row, first: Date?)] = []
        var deciding: [Ranked] = [], open: [Ranked] = [], shelved: [Ranked] = []
        // failed starts whose work finished in a related to-do: closed, as the To-dos page has them (#3713)
        let superseded = TodoTriageLogic.supersededFailedStarts(todos)
        for todo in todos {
            guard let when = at[todo.id] else { continue }
            let status = status(todo, lookup: lookup, plans: plans)
            let active = todo.doneAt != nil ? [] : todo.activeAgents
            let asks = active.flatMap { asksOf[pair($0.machineID, $0.sessionID)] ?? [] }
            let row = Row.task(id: todo.id, status: status, agents: active.count)
            let listed = { folded.formUnion(active.map { pair($0.machineID, $0.sessionID) }) }
            let bucket = TodoTriageLogic.bucket(todo, lookup: lookup, now: now, restoredAt: restored[todo.id],
                                                superseded: superseded.contains(todo.id))
            // a failed start carries done_at (the hub ends it), yet it is the user's to retry while its bucket says open
            let waits = status == .failed ? bucket == .open : todo.doneAt == nil && (status == .needsYou || status == .interrupted)
            if !asks.isEmpty { asking.append((row, asks.map { $0.createdAt ?? .distantPast }.min())); listed() }
            else if waits || awaitsDecision(todo) {
                // Only the review decision is put off or dismissed (Later / Don't handle); a live Agent question keeps its home.
                if !(status != .needsYou && awaitsDecision(todo) && decisionHidden(todo)) { deciding.append((row, rank(todo, status), when)) }
                listed()
            }
            else if bucket == .open { open.append((row, rank(todo, status), when)); listed() }
            else if bucket == .shelved { shelved.append((row, rank(todo, status), when)); listed() }
        }

        // Agents that belong to no task, the main conversation's only: asking ones wait on the user, working ones run. The
        // web's `dockAgents` leaves out the agents of conversations' tasks; an agent linked to a to-do made by hand is that
        // to-do's row here too (the main conversation lists every to-do), so it is left out as well: one thing, one row.
        let taskAgents = Set(todos.filter { !($0.origin ?? "").isEmpty && $0.doneAt == nil }.flatMap { t in
            t.activeAgents.map { SessionKey(group: group, machineID: $0.machineID, sessionID: $0.sessionID) }
        })
        let manual = Set(todos.filter { ($0.origin ?? "").isEmpty }.map(\.id))
        let dock = DockAgent.shown(going ?? DockAgent.going(sessions), conversation: conversation, main: main, group: group,
                                   taskAgents: taskAgents, manualTodos: manual)
        var running: [Row] = []
        for agent in dock where !folded.contains(pair(agent.key.machineID, agent.key.sessionID)) {
            if agent.isWaiting {
                asking.append((.agent(agent), asksOf[pair(agent.key.machineID, agent.key.sessionID)]?.first?.createdAt))
            } else {
                running.append(.agent(agent))
            }
        }

        let waitingPlans = plans.values.filter { $0.conversation == conversation && $0.state == .open }
            .sorted { ($0.createdAt ?? .distantPast, $0.id) < ($1.createdAt ?? .distantPast, $1.id) }
            .map { Row.plan(id: $0.id) }
        let messages = feed.filter(messageWaits).compactMap { item in item.ref.map { Row.message(feedID: item.id, itemID: $0) } }
        // stable sorts, as the web's: equal rows keep the order they came in; no time is the earliest (`byTime`)
        func stable<T>(_ xs: [T], _ before: (T, T) -> Bool?) -> [T] {
            xs.enumerated().sorted { a, b in before(a.element, b.element) ?? (a.offset < b.offset) }.map(\.element)
        }
        let newest = { (a: Ranked, b: Ranked) -> Bool? in a.rank != b.rank ? a.rank < b.rank : (a.at != b.at ? a.at > b.at : nil) }
        let epoch = Date(timeIntervalSince1970: 0)
        let byAsk = stable(asking) { a, b in
            let x = a.first ?? epoch, y = b.first ?? epoch
            return x != y ? x < y : nil
        }.map(\.row)
        return Result(needsYou: byAsk + waitingPlans + stable(deciding, newest).map(\.row) + messages,
                      running: stable(open, newest).map(\.row) + running,
                      shelved: stable(shelved, newest).map(\.row))
    }
}

/// What the two trays list: the split, and in front of Needs You the conversation's own cards (Persona asking, an
/// approval), which the screen adds as the web does.
nonisolated struct ActionTrayContent: Equatable, Sendable {
    /// A card the conversation itself shows, answered below the bar.
    struct Ask: Equatable, Sendable, Identifiable {
        var id: String
        var title: String
    }

    enum Row: Equatable, Sendable, Identifiable {
        case ask(Ask)
        case entry(AssistantEntries.Row)

        var id: String {
            switch self {
            case .ask(let a): "ask:" + a.id
            case .entry(let r): r.id
            }
        }
    }

    /// Needs You's rows from one supervising conversation (docs/apple/ios.md "Background rounds").
    struct NeedsYouGroup: Equatable, Sendable, Identifiable {
        /// The supervising conversation's id; "" for the conversation's own group.
        var id: String
        /// Its header; nil for the conversation's own group, which comes first.
        var title: String?
        var rows: [Row]
    }

    var needsYou: [Row] = []
    var running: [Row] = []
    /// Folded under In Progress, not counted.
    var shelved: [AssistantEntries.Row] = []
    /// Needs You's rows grouped by supervising conversation (`grouped`); empty when the tray is one list, as it always
    /// was. Grouping never changes the rows or the count.
    var needsYouGroups: [NeedsYouGroup] = []

    init() {}

    init(_ entries: AssistantEntries.Result, asks: [Ask] = []) {
        needsYou = asks.map(Row.ask) + entries.needsYou.map(Row.entry)
        running = entries.running.map(Row.entry)
        shelved = entries.shelved
    }

    func rows(_ tray: ActionTray) -> [Row] {
        switch tray {
        case .needsYou: needsYou
        case .running: running
        }
    }

    /// The number its entry shows: its rows (shelved ones are not counted).
    func count(_ tray: ActionTray) -> Int { rows(tray).count }

    /// Groups Needs You (`grouped`) when `enabled` (the main conversation folding background rounds); otherwise one list.
    mutating func groupNeedsYou(_ enabled: Bool, conversation: String, origin: (String) -> String?, title: (String) -> String?) {
        needsYouGroups = enabled ? Self.grouped(needsYou, conversation: conversation, origin: origin, title: title) : []
    }

    /// Needs You's rows by the conversation that supervises each task (its `origin`), the conversation's own group first
    /// and the others in the order their first row comes. Rows that are not tasks (asks, agents, plans, messages) and
    /// tasks without another origin are the conversation's own. Within a group rows keep their order. Empty with only
    /// one group: the tray then looks as it always did. `title` names another conversation; "Another conversation"
    /// when it has no name.
    static func grouped(_ rows: [Row], conversation: String, origin: (String) -> String?,
                        title: (String) -> String?) -> [NeedsYouGroup] {
        var order: [String] = [""]
        var byGroup: [String: [Row]] = [:]
        for row in rows {
            var group = ""
            if case .entry(.task(let id, _, _)) = row, let o = origin(id), !o.isEmpty, o != conversation { group = o }
            if byGroup[group] == nil, !group.isEmpty { order.append(group) }
            byGroup[group, default: []].append(row)
        }
        let ids = order.filter { byGroup[$0]?.isEmpty == false }
        guard ids.count > 1 else { return [] }
        return ids.map { id -> NeedsYouGroup in
            guard !id.isEmpty else { return NeedsYouGroup(id: id, title: nil, rows: byGroup[id] ?? []) }
            let name = title(id)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return NeedsYouGroup(id: id, title: name.isEmpty ? String(localized: "Another conversation") : name, rows: byGroup[id] ?? [])
        }
    }
}

extension ActionTray {
    var title: String {
        switch self {
        case .needsYou: String(localized: "Needs You [Action Bar]")
        case .running: String(localized: "In Progress [Action Bar]")
        }
    }

    var empty: String {
        switch self {
        case .needsYou: String(localized: "All Caught Up")
        case .running: String(localized: "Nothing in progress right now")
        }
    }
}

/// The bar and the tray open over it.
struct AssistantActionBar: View {
    var conversation: String
    /// The conversation's own session: its cards (Persona asking, an approval) wait in Needs You.
    var key: SessionKey? = nil
    /// The conversation's model operating a Mac (#1343): its pill after the entries.
    var computer: ComputerActivity? = nil
    @State private var open: ActionTray?
    /// The open tray's rows, measured: the tray is as tall as they are, up to its cap.
    @State private var listHeight: CGFloat = 0
    @State private var removing: TodoItem?
    /// Needs You's rows opened in place into their answerable card (`Row.id`), and the ones whose card was drawn once:
    /// a card stays in the tree (folded to nothing) after it closes, so a PlanCard's remember choice and a MessageCard's
    /// edit are still there when the row opens again.
    @State private var expanded: Set<String> = []
    @State private var drawn: Set<String> = []
    /// What the user picked on each plan row's card; the tray draws Start, Edit and Don't Start of the open one under its
    /// list (`PlanActions`), so a tall card never scrolls them away.
    @State private var planChoices: [String: PlanChoices] = [:]
    private var store: TasksStore { .shared }

    private var entryRow: AnyLayout { AnyLayout(HStackLayout(spacing: Theme.Space.s)) }

    var body: some View {
        let content = Self.content(conversation, key: key)
        let row = entryRow
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let open {
                tray(open, content)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            row {
                ForEach(ActionTray.allCases) { entry($0, count: content.count($0)) }
                if ComputerActivityPill.shows(computer) {
                    ComputerActivityPill(activity: computer).background(Theme.pageBackground, in: .capsule) // as the entries
                }
            }
            .padding(.horizontal, Theme.Space.xs)
            #if !os(macOS)
            .padding(.top, Theme.Space.s)
            #endif
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        // No fade of its own: the chat runs on behind the bar and fades out there (`FooterFog`, #3696).
        .onChange(of: conversation) { open = nil; expanded = []; drawn = []; planChoices = [:] }
        .onChange(of: open) { if open == nil { expanded = []; drawn = []; planChoices = [:] } }
        #if DEBUG
        .onAppear {
            if let value = UserDefaults.standard.string(forKey: "showActionTray"), let tray = ActionTray(rawValue: value) {
                open = tray
            }
        }
        // -openNeedsRow <Row.id prefix, e.g. plan: or task:>: opens the tray's first row of that kind in place, once the
        // tasks and plans it lists have come in.
        .task {
            guard let prefix = UserDefaults.standard.string(forKey: "openNeedsRow") else { return }
            for _ in 0..<60 {
                if let first = Self.content(conversation, key: key).needsYou.map(\.id).first(where: { $0.hasPrefix(prefix) }) {
                    open = .needsYou
                    drawn.insert(first)
                    expanded.insert(first)
                    planChoices[first] = PlanChoices()
                    return
                }
                try? await Task.sleep(for: .milliseconds(500))
            }
        }
        #endif
        .confirmTaskRemoval($removing)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("action-bar")
    }

    static func content(_ conversation: String, key: SessionKey? = nil) -> ActionTrayContent {
        let store = TasksStore.shared
        let model = AppModel.shared
        let todos = store.todos.values.sorted { ($0.createdAt ?? .distantPast, $0.id) > ($1.createdAt ?? .distantPast, $1.id) }
        // Only the tasks' own agents, which is all a task's status reads, and the agents going on as the dock had them: the
        // bar must not follow every agent's push (AllSessionsReadersTests, #3446).
        let entries = AssistantEntries.split(
            conversation: conversation, main: model.assistantMain?.sessionID, group: model.hub?.group ?? "", todos: todos,
            sessions: todos.filter { $0.doneAt == nil || $0.failedToStart }.flatMap { store.sessions($0) },
            interactions: model.pendingInteractions, plans: store.plans,
            feed: store.feed(conversation), restored: TodoRestores.load(), going: model.goingAgents,
            // the hub's fields, the inbox's word on the item, and Later
            messageWaits: { item in
                guard item.kind == .message, let id = item.ref, !store.laterMessages.contains(item.id) else { return false }
                return store.messageState(item, itemID: id) == .open
            },
            decisionHidden: { store.decisionHidden($0) })
        let asks = (key.map { model.interactions(for: $0) } ?? []).sorted { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }
        var content = ActionTrayContent(entries, asks: asks.map { .init(id: $0.id, title: $0.askLine(includeToolName: false) ?? $0.title) })
        // The main conversation, while it folds background rounds, groups Needs You by supervising conversation.
        content.groupNeedsYou(conversation == model.assistantMain?.sessionID && model.mainBackgroundFolded,
                              conversation: conversation, origin: { store.todos[$0]?.origin },
                              title: { id in
                                  if let name = model.directionNames[id] { return name }
                                  let title = model.assistantConfig?.conversations.first(where: { $0.sessionID == id })?.title
                                  return title == "Claude Code" ? nil : title
                              })
        return content
    }

    private func entry(_ tray: ActionTray, count: Int) -> some View {
        let selected = open == tray
        // Needs You with something in it takes the waiting pill's tint; empty, it is as quiet as In Progress.
        let look: DockCapsule.Look = selected ? .selected : (tray == .needsYou && count > 0 ? .waiting(.needsYou) : .plain)
        return Button {
            Haptics.tap()
            withAnimation(Theme.Motion.state) { open = selected ? nil : tray }
        } label: {
            HStack(spacing: Theme.Space.xs) {
                Text(tray.title)
                if let text = CountBadge.text(count) { Text(text).monospacedDigit() }
            }
            .dockCapsule(look)
            // On the page under its see-through fill: the chat fading out behind the row does not read through the
            // words (#3696); with nothing behind, the same pixels as before.
            .background(Theme.pageBackground, in: .capsule)
            .fixedSize()
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("action-bar.\(tray.rawValue)")
    }

    private func tray(_ tray: ActionTray, _ content: ActionTrayContent) -> some View {
        let rows = content.rows(tray)
        return VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Theme.Space.s) {
                Text(tray.title).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                // Finished things are in neither tray: the board has everything.
                Button {
                    open = nil
                    store.showBoard = true
                } label: {
                    // A finger's height around the capsule (#5392).
                    Text("All").dockCapsule().fixedSize()
                        .tapTargetHeight()
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("action-tray.board")
                Button {
                    withAnimation(Theme.Motion.state) { open = nil }
                } label: {
                    ThemedIcon(Theme.Icon.close).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                        .tapTargetFrame()
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Close")
                .accessibilityIdentifier("action-tray.close")
            }
            // No vertical padding: the 44 pt targets are the row's height, as the capsule and its padding were.
            .padding(.leading, Theme.Space.m)
            if rows.isEmpty && (tray == .needsYou || content.shelved.isEmpty) {
                Text(tray.empty).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.bottom, Theme.Space.m)
                    .accessibilityIdentifier("action-tray.empty")
            } else {
                let cap = min(listHeight, listCap(pinned: tray == .needsYou && pinnedPlan(rows) != nil))
                // As tall as its rows; past the cap they scroll inside the tray, the bar stays where it is.
                ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        if tray == .needsYou, !content.needsYouGroups.isEmpty {
                            ForEach(content.needsYouGroups) { group in
                                if let title = group.title {
                                    // The tray's own header style: a group reads as a section of the tray.
                                    Text(title).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                                        .padding(.horizontal, Theme.Space.m)
                                        .padding(.top, Theme.Space.s)
                                        .accessibilityAddTraits(.isHeader)
                                        .accessibilityIdentifier("action-tray.group")
                                }
                                ForEach(group.rows) { row($0) }
                            }
                        } else {
                            ForEach(rows) { row($0) }
                        }
                        if tray == .running, !content.shelved.isEmpty { shelvedLine(content.shelved.count) }
                    }
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { listHeight = $0 }
                }
                .scrollBounceBehavior(.basedOnSize)
                // A row that opened in place scrolls to the top of the tray: its card is taller than the tray.
                .onChange(of: expanded) { old, new in
                    guard let id = new.subtracting(old).first else { return }
                    withAnimation(Theme.Motion.state) { proxy.scrollTo(id, anchor: .top) }
                }
                .frame(height: cap)
                // a row the cap cuts fades out above the bar instead of being sliced
                .mask {
                    let fade = listHeight > cap && cap > 0 ? min(1, Theme.Space.xxl / cap) : 0
                    LinearGradient(stops: [.init(color: .black, location: 1 - fade), .init(color: .black.opacity(fade == 0 ? 1 : 0), location: 1)],
                                   startPoint: .top, endPoint: .bottom)
                }
                }
                .padding(.bottom, Theme.Space.xs)
                if tray == .needsYou, let (plan, choices) = pinnedPlan(rows) {
                    Divider().padding(.horizontal, Theme.Space.m)
                    PlanActions(plan: plan, choices: choices)
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.vertical, Theme.Space.s)
                        .accessibilityIdentifier("action-tray.plan-actions")
                }
            }
        }
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("action-tray.\(tray.rawValue)")
    }

    @ViewBuilder private func row(_ row: ActionTrayContent.Row) -> some View {
        switch row {
        case .ask(let ask):
            // The card itself is right below the bar: the row closes the tray to show it.
            Button {
                withAnimation(Theme.Motion.state) { open = nil }
            } label: {
                line(title: ask.title, detail: StateName.needsYou.text, tint: AgentState.needsYou.tone.text, trail: .open) {
                    ThemedIcon(Theme.Icon.askFirst).font(Theme.Font.labelStrong).foregroundStyle(AgentState.needsYou.tone.text)
                }
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("action-tray.ask")
        case .entry(let entry):
            self.entry(entry)
        }
    }

    /// Folded rows get a short list; an opened card a taller one, and with the plan bar under it room for the next row whole.
    private func listCap(pinned: Bool) -> CGFloat {
        expanded.isEmpty ? Theme.Size.box280 : Theme.Size.box380 + (pinned ? Theme.Size.box56 : 0)
    }

    /// The open plan whose row is opened (only one is at a time): its buttons stay under the list.
    private func pinnedPlan(_ rows: [ActionTrayContent.Row]) -> (TaskPlan, PlanChoices)? {
        for row in rows {
            guard case .entry(.plan(let id)) = row, expanded.contains("plan:" + id), let choices = planChoices["plan:" + id],
                  let plan = store.plans[id], plan.state == .open else { continue }
            return (plan, choices)
        }
        return nil
    }

    /// A Needs You row that opens in place: folded it is a line (mark, words, a chevron), opened it is the card that answers
    /// it, right under the line. Another row opening does not close it (except a plan, there is one plan bar), and a message or
    /// brief card drawn once stays in the tree, folded to nothing, so its edit is still there.
    private func opening<Mark: View, Card: View>(_ id: String, title: String, detail: String, subtitle: String?,
                                                 @ViewBuilder mark: () -> Mark, @ViewBuilder card: () -> Card) -> some View {
        let isOpen = expanded.contains(id)
        return VStack(alignment: .leading, spacing: 0) {
            Button {
                Haptics.tap()
                if !isOpen {
                    drawn.insert(id)
                    if id.hasPrefix("plan:"), planChoices[id] == nil { planChoices[id] = PlanChoices() }
                }
                withAnimation(Theme.Motion.state) {
                    if isOpen {
                        expanded.remove(id)
                        // a plan card is not kept: its choices are in `planChoices`, and a folded card would go on reading the stores
                        if id.hasPrefix("plan:") { drawn.remove(id) }
                    } else {
                        // one plan at a time: its Start / Edit / Don't Start are the bar under the list, and there is one bar
                        if id.hasPrefix("plan:") {
                            for other in expanded where other.hasPrefix("plan:") { expanded.remove(other); drawn.remove(other) }
                        }
                        expanded.insert(id)
                    }
                }
            } label: {
                line(title: title, detail: detail, tint: AgentState.needsYou.tone.text, subtitle: isOpen ? nil : subtitle, trail: isOpen ? .expanded : .folded, mark: mark)
            }
            .buttonStyle(.plain)
            .accessibilityValue(isOpen ? String(localized: "Expanded") : String(localized: "Collapsed"))
            .accessibilityIdentifier(isOpen ? "action-tray.row.open" : "action-tray.row")
            if drawn.contains(id) {
                card()
                    .frame(height: isOpen ? nil : 0, alignment: .top)
                    .clipped()
                    .opacity(isOpen ? 1 : 0)
                    .allowsHitTesting(isOpen)
                    .accessibilityHidden(!isOpen)
            }
        }
        .id(id)
    }

    @ViewBuilder private func entry(_ row: AssistantEntries.Row) -> some View {
        switch row {
        case .task(let id, let status, let agents):
            if let todo = store.todos[id] {
                let state = status.taskState
                let activity = AssistantTrayActivity(sessions: store.sessions(todo))
                if let ask = todo.openAsk, todo.doneAt == nil {
                    // an owner decision or a to-do to verify: folded to a line, opened it is its Accept row, or the answerable
                    // decision row when the hub sent its options (`TodoAskRow`)
                    let brief = ask.row(for: todo)
                    opening(row.id, title: todo.title, detail: brief.isDecision ? String(localized: "Decision") : String(localized: "To Verify"),
                            subtitle: ask.verdict, mark: { TaskMark(state: .needsYou) }) {
                        TodoAskRow(todo: todo, ask: ask, group: AppModel.shared.hub?.group ?? "", deferrable: true, showsTitle: false)
                            .padding(.horizontal, Theme.Space.m)
                    }
                } else {
                    let lead = store.leadSession(todo)
                    Button {
                        open = nil
                        store.detailTask = id
                    } label: {
                        line(title: todo.title,
                             detail: activity.isActive && status == .waiting ? StateName.working.text : detail(todo, status: status, agents: agents),
                             tint: tint(status), subtitle: subtitle(todo, status: status, lead: lead, activity: activity),
                             subtitleTint: status == .failed || status == .interrupted ? Theme.danger : nil,
                             trail: .open, activity: activity) {
                            if open != .needsYou && (activity.isActive || status == .working) {
                                WorkingIndicator().accessibilityIdentifier("action-tray.working-mark")
                            } else if status == .waiting {
                                ThemedIcon(Theme.Icon.backgroundWaiting).font(Theme.Font.labelStrong)
                                    .foregroundStyle(StatusPalette.backgroundWaiting.dot)
                                    .accessibilityIdentifier("action-tray.waiting-mark")
                            } else if status == .usageLimit {
                                ThemedIcon(Theme.Icon.backgroundWaiting).font(Theme.Font.labelStrong).foregroundStyle(Theme.caution)
                                    .accessibilityIdentifier("action-tray.limit-mark")
                            } else if status == .interrupted {
                                ThemedIcon(Theme.Icon.infoFilled).font(Theme.Font.labelStrong).foregroundStyle(Theme.danger)
                                    .accessibilityIdentifier("action-tray.interrupted-mark")
                            } else if status == .notStarted {
                                StartingMark().accessibilityIdentifier("action-tray.starting-mark")
                            } else {
                                TaskMark(state: state)
                            }
                        }
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("action-tray.task")
                    .contextMenu { TaskRemoveMenuItem(todo: todo, state: state, pending: $removing) }
                }
            }
        case .agent(let agent):
            Button {
                open = nil
                store.detailAgent = agent.key
            } label: {
                line(title: agent.title,
                     detail: agent.isWaiting ? StateName.needsYou.text : (agent.backgroundWaiting && !agent.activity.isActive ? StateName.taskWaiting.text : StateName.working.text),
                     tint: agent.isWaiting ? AgentState.needsYou.tone.text : nil, trail: .open, activity: agent.activity) {
                    if !agent.isWaiting && (agent.activity.isActive || !agent.backgroundWaiting) {
                        WorkingIndicator().accessibilityIdentifier("action-tray.working-mark")
                    } else {
                        StatusDot(state: agent.state, size: Theme.Size.dotL, waiting: agent.backgroundWaiting)
                            .accessibilityIdentifier(agent.backgroundWaiting ? "action-tray.waiting-mark" : "action-tray.state-mark")
                    }
                }
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("action-tray.agent")
        case .plan(let id):
            // folded to a line; opened it is the plan card itself: Start, Edit and Don't Start are right there
            if let plan = store.plans[id], let first = plan.tasks.first {
                opening(row.id, title: first.title, detail: plan.tasks.count > 1 ? String(localized: "\(plan.tasks.count) tasks") : String(localized: "Plan"),
                        subtitle: [TaskPlace.machineName(first.machine), first.project].filter { !$0.isEmpty }.joined(separator: " · "),
                        mark: { ThemedIcon(Theme.Icon.taskPlan).font(Theme.Font.labelStrong).foregroundStyle(Theme.accent) }) {
                    PlanCard(plan: plan, choices: planChoices[row.id], actionsPinned: planChoices[row.id] != nil)
                        .padding(.horizontal, Theme.Space.s)
                        .padding(.bottom, Theme.Space.s)
                        .accessibilityIdentifier("action-tray.plan")
                }
            }
        case .message(let feedID, let itemID):
            // folded to a line; opened it is the message card itself: Send, Edit and Later are right there
            if let item = store.feed(conversation).first(where: { $0.id == feedID }) {
                opening(row.id, title: [item.sender, item.chat].compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: " · "),
                        detail: String(localized: "Draft Reply"), subtitle: (item.text ?? "").split(separator: "\n").first.map(String.init),
                        mark: { ThemedIcon(item.source == "mail" ? Theme.Icon.mail : Theme.Icon.groupChat).font(Theme.Font.labelStrong).foregroundStyle(Theme.loop) }) {
                    MessageCard(item: item, itemID: itemID)
                        .padding(.horizontal, Theme.Space.s)
                        .padding(.bottom, Theme.Space.s)
                        .accessibilityIdentifier("action-tray.message")
                }
            }
        }
    }

    /// The open tasks with no progress for 48 hours: one quiet line under In Progress, not counted; the board lists them.
    private func shelvedLine(_ count: Int) -> some View {
        Button {
            open = nil
            store.showBoard = true
        } label: {
            line(title: String(localized: "Shelved"), detail: "\(count)", trail: .none) {
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
            }
            .foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("action-tray.shelved")
    }

    /// A task row's words: its status, what a waiting task waits for (#3274), the review it waits for when its status
    /// does not say, and how many agents it has when more than one (they are listed in its details, never as rows).
    private func detail(_ todo: TodoItem, status: AssistantEntries.Status, agents: Int) -> String {
        let review = todo.reviewState
        let resting = status == .followUp || status == .unread || status == .notStarted
        var parts: [String]
        if status == .interrupted { parts = [String(localized: "Interrupted [Chat Layout]")] }
        else if status == .usageLimit { parts = [String(localized: "Usage Limit")] }
        else if resting, review == .pendingReview || review == .needsUser { parts = [todo.reviewBadge?.label ?? status.taskState.label] }
        else { parts = [status.taskState.label] }
        if status == .waiting, let waits = store.waitLine(todo) { parts.append(waits) }
        if agents > 1 { parts.append(String(localized: "\(agents) agents")) }
        return parts.joined(separator: " · ")
    }

    /// A task row's status word colour: red when it needs a retry or a continue, orange on a usage limit, the Needs You
    /// colour while it waits for the user (and everything in that tray does); nil is secondary.
    private func tint(_ status: AssistantEntries.Status) -> Color? {
        switch status {
        case .failed, .interrupted: Theme.danger
        case .usageLimit: Theme.cautionText
        case .needsYou: AgentState.needsYou.tone.text
        default: open == .needsYou ? AgentState.needsYou.tone.text : nil
        }
    }

    /// The row's second line (docs/design/task-cards.md §3.2): why a stopped task stopped and when it goes on, what a
    /// starting one is doing, what a waiting one waits for, else its last words; nil when there is nothing to add.
    private func subtitle(_ todo: TodoItem, status: AssistantEntries.Status, lead: Session?, activity: AssistantTrayActivity) -> String? {
        func one(_ text: String?) -> String? {
            let t = Fmt.plain(text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            return t.isEmpty ? nil : t
        }
        switch status {
        case .usageLimit:
            let reason = one(lead?.turnError)
            let when = lead?.usageLimitWhen()
            return [reason, when].compactMap { $0 }.joined(separator: " · ").nilIfEmpty
        case .interrupted:
            return one(lead?.turnError.flatMap { TurnErrorReason.of($0) ?? $0 })
        case .notStarted:
            return String(localized: "Starting the agent")
        case .failed:
            return one(store.startFailure(todo))
        case .needsYou:
            return TaskAsk.waitingLine(store.interactions(todo))
        case .working, .waiting:
            return activity.isActive ? nil : one(TaskNow(lead: lead, working: status == .working)?.text)
        case .unread, .done, .followUp:
            return one(lead?.lastText)
        }
    }

    /// What follows a row's words on the right.
    private enum Trail { case none, open, folded, expanded }

    private func line<Mark: View>(title: String, detail: String, tint: Color? = nil, subtitle: String? = nil, subtitleTint: Color? = nil,
                                  trail: Trail = .open, activity: AssistantTrayActivity = .init(),
                                  @ViewBuilder mark: () -> Mark) -> some View {
        HStack(spacing: Theme.Space.s) {
            mark().scaledFrame(width: Theme.Size.markM)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(spacing: Theme.Space.s) {
                    Text(title).font(Theme.Font.body).lineLimit(1)
                    Spacer(minLength: Theme.Space.s)
                    Text(detail).font(Theme.Font.caption).lineLimit(1).fixedSize()
                        .foregroundStyle(tint.map { AnyShapeStyle($0) } ?? AnyShapeStyle(.secondary))
                }
                if let subtitle {
                    Text(subtitle).font(Theme.Font.caption).lineLimit(1)
                        .foregroundStyle(subtitleTint.map { AnyShapeStyle($0) } ?? AnyShapeStyle(.secondary))
                        .accessibilityIdentifier("action-tray.subtitle")
                }
                if activity.isActive {
                    AssistantTrayActivityLine(activity: activity)
                }
            }
            if trail != .none {
                ThemedIcon(trail == .expanded ? Theme.Icon.expand : Theme.Icon.disclosure)
                    .font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                    .accessibilityHidden(true)
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }
}

/// A task whose Agent is on its way up: a hollow circle with the caution dot in it.
private struct StartingMark: View {
    var body: some View {
        Circle().strokeBorder(.secondary, lineWidth: Theme.Size.lineMedium)
            .scaledFrame(width: Theme.Size.dotL, height: Theme.Size.dotL)
            .overlay { Circle().fill(Theme.caution).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS) }
            .accessibilityLabel(String(localized: "Starting [Task Views d14c3a]"))
    }
}

private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}

extension AssistantTrayActivity {
    /// Workflow phase first, with other work on a separate line when activity is mixed.
    var subtitle: String { [workflowSubtitle, otherSubtitle].filter { !$0.isEmpty }.joined(separator: " · ") }

    var workflowSubtitle: String {
        guard workflowCount > 0 else { return "" }
        return ([String(localized: "Background workflow")] + (workflows.first.map { [$0.phase ?? $0.title] } ?? []))
            .joined(separator: " · ")
    }

    var otherSubtitle: String {
        var parts: [String] = []
        if backgroundTasks > 0 { parts.append(String(localized: "\(backgroundTasks) background tasks running")) }
        if subagents > 0 { parts.append(String(localized: "\(subagents) subagents running")) }
        if parts.isEmpty, workflowCount == 0, reportedBackgroundWork { return String(localized: "Working (Background)") }
        return parts.joined(separator: " · ")
    }
}

/// A tray's activity subtitle: even a long phase cannot cut the workflow's done/started counts.
struct AssistantTrayActivityLine: View {
    var activity: AssistantTrayActivity

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            if activity.workflowCount > 0 {
                HStack(spacing: Theme.Space.xs) {
                    Text(activity.workflowSubtitle).lineLimit(1).truncationMode(.tail)
                    if let counts = activity.workflows.first?.counts {
                        Text("· " + counts).monospacedDigit().lineLimit(1).fixedSize()
                            .layoutPriority(1)
                            .accessibilityIdentifier("action-tray.workflow-progress")
                    }
                    if activity.workflowCount > 1 {
                        Text(String(localized: " · and \(activity.workflowCount - 1) more")).lineLimit(1).fixedSize()
                    }
                }
            }
            if !activity.otherSubtitle.isEmpty {
                Text(activity.otherSubtitle).lineLimit(1)
            }
        }
        .font(Theme.Font.caption)
        .foregroundStyle(.secondary)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("action-tray.activity")
    }
}
