import PersonaKit
import SwiftUI

// The row above the assistant's composer on iPhone, iPad and Mac (#3393, docs/assistant/unified-entry.md "Needs you and In
// progress: each thing once"): two entries, Needs You and In Progress, each thing in exactly one of them, and Finished
// while the conversation has tasks that finished in the last 24 hours (#10922). The split is
// `AssistantEntries`, a port of the web's `assistantEntries` (web/src/lib/assistant-entries.ts, Android's
// AssistantEntries.kt, #3387) replaying the shared fixture spec/fixtures/entries/cases.json. Each entry opens a tray above
// the bar, one at a time; the entries are drawn as the old dock's capsules (`DockCapsule`), so the bar is as low. In the
// main conversation with low-distraction mode on, Held Pushes (#7782, PushInterceptsView.swift) comes after them, and its
// tray opens in the same place (#8223).

/// One of the bar's entries, and the tray it opens. Finished is there only while it lists something, or its tray is open.
nonisolated enum ActionTray: String, CaseIterable, Identifiable, Sendable {
    case needsYou = "needs-you", running, finished

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

    /// A task the conversation handed out that finished in the last 24 hours (Finished, #10922): `fresh` while the
    /// owner has not opened it since it finished (the To-dos page's Just Finished), so it is drawn as new; seeing it
    /// changes only that. `at`: when it finished (`TodoTriageLogic.finishedAt`).
    struct Finished: Equatable, Sendable, Identifiable {
        var row: Row
        var fresh: Bool
        var at: Date

        var id: String { row.id }
    }

    struct Result: Equatable, Sendable {
        var needsYou: [Row] = []
        var running: [Row] = []
        /// Open tasks with no progress for 48 hours and nothing running: folded under In Progress, not counted.
        var shelved: [Row] = []
        /// Handed-out tasks that finished in the last 24 hours (`TodoTriageLogic.justDone`, the To-dos page's Just
        /// Finished window), seen or not, newest finished first: the Finished entry (#10922). How a task ended is told
        /// here, not by a card in the chat (#8237). Not open work: its count is its rows. Until #10922 only the unseen
        /// ones were listed, as a group under In Progress that went as soon as one was opened.
        var finished: [Finished] = []
        /// To-do IDs with no Agent up whose dispatch is still in flight (#7552). Their status and open rank stay notStarted.
        var starting: Set<String> = []
    }

    /// A task's status, failed too when only its plan says the start failed (a hub from before #1077): the web's
    /// `entryStatus` over `todoStatus`.
    static func status(_ todo: TodoItem, lookup: TodosLogic.SessionLookup?, plans: [String: TaskPlan]) -> Status {
        let st = todoStatus(todo, lookup: lookup)
        guard st == .notStarted, todo.agents.isEmpty, let plan = todo.planID.flatMap({ plans[$0] }),
              let error = plan.tasks.first(where: { $0.todoID == todo.id })?.error, !error.isEmpty else { return st }
        return .failed
    }

    /// Whether an Agent is being started for a to-do with none up yet (#7552): the web's `startInFlight`.
    static func startInFlight(_ todo: TodoItem, plans: [String: TaskPlan]) -> Bool {
        if todo.doneAt != nil || todo.failedToStart { return false }
        if todo.agents.isEmpty, let plan = todo.planID.flatMap({ plans[$0] }),
           let error = plan.tasks.first(where: { $0.todoID == todo.id })?.error, !error.isEmpty { return false }
        if todo.planID != nil { return true }
        return plans.values.contains { plan in
            plan.state != .dismissed && plan.tasks.contains { $0.todoID == todo.id && ($0.error ?? "").isEmpty }
        }
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
            else if error, stopped, TurnErrorReason.invalid(s) { interrupted = true }
            else if active.count > 1, st != .working, s?.waitsToBeWoken == true { waiting = true }
            else if error, stopped { interrupted = true }
            else if st == .unread { unread = true }
            else if s?.waitsToBeWoken == true { waiting = true }
            else if st == .working || s?.starting == true { working = true }
        }
        if interrupted, !working, !waiting, todo.supervision?.state != "parked" { return .interrupted }
        if limit { return .usageLimit }
        if unread { return .unread }
        if working { return .working }
        if waiting { return .waiting }
        return .followUp
    }

    /// A task's status when some of its Agents ask under another task (`own`: its status from its own Agents alone): it
    /// does not wait on the user for them, so a Needs You that only those bring is the task at work instead (the web's
    /// `withNeeds`).
    static func withNeeds(_ all: Status, own: Status) -> Status {
        all == .needsYou && own != .needsYou ? (own == .notStarted ? .working : own) : all
    }

    /// The open ask, as the web reads it (`openAsk`): one with a verdict and a time.
    static func openAsk(_ todo: TodoItem) -> TodoAsk? {
        guard let a = todo.ask, !(a.verdict ?? "").isEmpty, !a.askedAt.isEmpty else { return nil }
        return a
    }

    /// It waits for the user's review while nothing of it runs or asks (the web's `awaitsReview`): rank 2.
    static func rank(_ todo: TodoItem, _ status: Status) -> Double {
        // TaskState has no interrupted / usage-limit cases: keep the raw status's rest semantics instead of letting
        // their display-only follow-up mapping reclassify an active error as accepted or awaiting review (#5732).
        let resting = status == .followUp || status == .unread || status == .notStarted
        if resting || status == .usageLimit, todo.acceptedAtRest(status.taskState) { return 8 }
        if todo.waitsForOwner { return 0 }
        if resting, todo.deliveredAwaitingReview(status.taskState) { return 2 }
        return status.rank
    }

    /// Whether a Finished row is new. Seen clears that marker without filing a delivery that still awaits review (#5732).
    static func showsJustFinished(_ todo: TodoItem, _ triage: TodoTriage, now: Date) -> Bool {
        let completedAt: Date?
        switch triage.rule {
        case .closed: completedAt = todo.doneAt
        case .accepted: completedAt = todo.supervision?.latestReview?.at
        case .finishedUnseen: completedAt = TodoTriageLogic.finishedAt(todo, triage)
        default: completedAt = nil
        }
        guard let completedAt else { return false }
        return now.timeIntervalSince(completedAt) < 24 * 3600 && (todo.seenAt.map { $0 < completedAt } ?? true)
    }

    /// How long a task's review is its supervisor's alone (docs/assistant/unified-entry.md "Needs you and In progress"):
    /// past it the review is overdue and the task is the owner's, under Needs You. The web's `REVIEW_OVERDUE_MS`.
    static let reviewOverdueAfter: TimeInterval = 60 * 60

    /// The task waits for its supervisor's review, which is not the owner's to do yet (the web's `supervisorReviewing`):
    /// open, in `pending_review` for less than `reviewOverdueAfter`. False past that, and when the hub does not say since
    /// when (an older hub): the review is then the owner's, as it was. An open ask or an asking Agent is the owner's
    /// whatever this says. An owner hold its Agent reported that the supervisor has not read yet (#13191) is the
    /// supervisor's however old.
    static func supervisorReviewing(_ todo: TodoItem, now: Date) -> Bool {
        if todo.doneAt == nil, todo.delivery?.heldForSupervisor == true { return true }
        guard todo.doneAt == nil, todo.supervision?.state == "pending_review", let since = todo.supervision?.pendingSince else { return false }
        return now.timeIntervalSince(since) < reviewOverdueAfter
    }

    /// The task waits for the user's decision or review (the web's `taskAwaitsDecision`): an open task's ask, or its
    /// supervisor's `needs_user` on an open task, or a `pending_review` that is the owner's: one waiting an hour or more,
    /// or one the hub gives no start for. A younger review is its supervisor's. A finished task never waits, whatever ask
    /// it still carries: the hub's To-dos triage files it with the finished ones, and its row here has nothing to answer
    /// (`TodoItem.openAsk` is nil once it is done), so it would keep the badge up for good (#12974).
    static func awaitsDecision(_ todo: TodoItem, now: Date) -> Bool {
        guard todo.doneAt == nil else { return false }
        if openAsk(todo) != nil { return true }
        let state = todo.supervision?.state
        return state == "user_check" || state == "needs_user" || (state == "pending_review" && !supervisorReviewing(todo, now: now))
    }

    /// The main conversation names only a current owner question or acceptance, never a supervisor's pending review.
    /// Other conversations keep the legacy `awaitsDecision` rule above; Agents and To-dos are separate surfaces.
    static func directUserDecision(_ todo: TodoItem) -> Bool {
        guard todo.doneAt == nil else { return false }
        if let ask = openAsk(todo) {
            let hasQuestions = !(ask.question ?? "").isEmpty || !(ask.options ?? []).isEmpty
            if hasQuestions || ask.outcome == "decide" || (ask.outcome ?? "").isEmpty || !(ask.whyOwner ?? "").isEmpty { return true }
        }
        let state = todo.reviewState
        return state == .needsUser || state == .userCheck
    }

    /// The task's row among the tasks in progress says its supervisor is reviewing it: nothing of it runs or asks, and
    /// the review is not the owner's (the web's `supervisorReview`).
    static func saysSupervisorReview(_ todo: TodoItem, status: Status, now: Date) -> Bool {
        let resting = status == .followUp || status == .unread || status == .notStarted
        return resting && !awaitsDecision(todo, now: now) && supervisorReviewing(todo, now: now)
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
                                 decisionHidden: (TodoItem) -> Bool = { _ in false }, directUserOnly: Bool = false) -> Result {
        let lookup = TodosLogic.makeSessionLookup(sessions)
        let isMain = !(main ?? "").isEmpty && main == conversation
        let directAttention = directUserOnly && isMain
        // A function, not a closure value: Swift 6.2.4 (Xcode 26.3) rejects capturing a closure value in the `listed`
        // closure below ("sending 'pair' risks causing data races").
        func pair(_ m: String, _ s: String) -> String { m + "/" + s }
        var asksOf: [String: [Interaction]] = [:]
        for ix in interactions.sorted(by: { ($0.createdAt ?? .distantPast, $0.id) < ($1.createdAt ?? .distantPast, $1.id) }) {
            if directAttention && !Supervision.directUserRequest(ix) { continue }
            asksOf[pair(ix.machineID, ix.sessionID), default: []].append(ix)
        }

        // The conversation's tasks (the web's `taskCardSlots`): the main conversation's every to-do, another's the ones it
        // dispatched, plus those its plans and feed name. `at` is when its card came: its plan's, else the to-do's; `slot`
        // is the web's slot id, `plan:<plan>:<index>` for a plan's task (its place in the plan), else `task:<to-do>`.
        var at: [String: Date] = [:], slot: [String: String] = [:]
        for plan in plans.values.sorted(by: { $0.id < $1.id }) where plan.conversation == conversation {
            for (index, t) in plan.tasks.enumerated() {
                if let id = t.todoID, at[id] == nil { at[id] = plan.createdAt ?? .distantPast; slot[id] = "plan:\(plan.id):\(index)" }
            }
        }
        let byID = Dictionary(todos.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        for item in feed where [.task, .receipt, .review, .report].contains(item.kind) {
            if let id = item.ref, at[id] == nil { at[id] = byID[id]?.createdAt ?? item.at }
        }
        for todo in todos where (isMain || todo.origin == conversation) && at[todo.id] == nil {
            at[todo.id] = todo.createdAt ?? .distantPast
        }

        // One home per request (docs/design/DESIGN_SYSTEM.md 4.8, the web's #7490): an Agent several of these tasks list
        // asks under one of them, the task the hub ties its session to (`Session.todo`) when it lists it, else the first
        // that lists it in the web's slot order (oldest card first, then slot id: one plan's tasks in the plan's order).
        // The others neither repeat its cards nor wait on the user for them.
        var home: [String: String] = [:]
        let slotOrder = todos.compactMap { t in at[t.id].map { (todo: t, at: $0, slot: slot[t.id] ?? "task:" + t.id) } }
            .sorted { ($0.at, $0.slot) < ($1.at, $1.slot) }
        for (todo, _, _) in slotOrder where todo.doneAt == nil {
            for a in todo.activeAgents {
                let k = pair(a.machineID, a.sessionID)
                if home[k] == nil || lookup(a.machineID, a.sessionID)?.todo?.id == todo.id { home[k] = todo.id }
            }
        }

        typealias Ranked = (row: Row, rank: Double, at: Date)
        /// The agents of the tasks listed as rows: folded into them, never rows of their own, whoever made the to-do.
        var folded = Set<String>()
        var asking: [(row: Row, first: Date?)] = []
        var deciding: [Ranked] = [], open: [Ranked] = [], shelved: [Ranked] = [], finished: [Finished] = []
        var starting: Set<String> = []
        // failed starts whose work finished in a related to-do: closed, as the To-dos page has them (#3713)
        let superseded = TodoTriageLogic.supersededFailedStarts(todos)
        for todo in todos {
            guard let when = at[todo.id] else { continue }
            let active = todo.doneAt != nil ? [] : todo.activeAgents
            let own = active.filter { home[pair($0.machineID, $0.sessionID)] == todo.id }
            let all = Self.status(todo, lookup: lookup, plans: plans)
            // what the task waits on the user for is its own Agents' (an Agent another task holds asks there)
            var mine = todo
            mine.agents = own
            let status = own.count < active.count ? withNeeds(all, own: todoStatus(mine, lookup: lookup)) : all
            if status == .notStarted && Self.startInFlight(todo, plans: plans) { starting.insert(todo.id) }
            // a card the task's supervising conversation holds (delegating) is that conversation's until it passes it up
            let routed = own.flatMap { asksOf[pair($0.machineID, $0.sessionID)] ?? [] }
            let asks = routed.filter { $0.delegating != true }
            // a supervised task's failed start is its supervisor's to retry or remedy, and an Agent whose every card its
            // supervisor holds waits on that conversation, not on the user (the web's `assistantEntries`)
            let supervised = !(todo.supervision?.supervisor ?? "").isEmpty
            let held = supervised && !routed.isEmpty && asks.isEmpty
            let row = Row.task(id: todo.id, status: status, agents: active.count)
            let listed = { folded.formUnion(active.map { pair($0.machineID, $0.sessionID) }) }
            let triage = TodoTriageLogic.triage(todo, lookup: lookup, now: now, restoredAt: restored[todo.id],
                                                superseded: superseded.contains(todo.id))
            let bucket = TodoTriageLogic.bucket(todo, triage)
            // First keep the legacy slot. Direct-user filtering removes rows from Needs You only; it never moves an
            // excluded permission, retry, failed start or supervisor review into In Progress.
            let legacyWaits = status == .failed ? bucket == .open && !supervised
                : todo.doneAt == nil && (status == .needsYou || status == .interrupted) && !held
            let legacyDecision = awaitsDecision(todo, now: now)
            let legacyDelivery = todo.doneAt == nil && todo.delivery?.waitsForOwner == true
            let waits = !directAttention && legacyWaits
            let decision = directAttention ? (status != .failed && directUserDecision(todo)) : legacyDecision
            let deliveryApproval = (!directAttention || status != .failed) && legacyDelivery
            if !asks.isEmpty, !directAttention || status != .failed { asking.append((row, asks.map { $0.createdAt ?? .distantPast }.min())); listed() }
            else if legacyWaits || legacyDecision || legacyDelivery {
                if waits || decision || deliveryApproval {
                    // Delivery acceptance is its own gate. A dismissed direct decision leaves; a live direct question stays.
                    let hidden = directAttention ? decisionHidden(todo) : (status != .needsYou && decision && decisionHidden(todo))
                    if deliveryApproval || !hidden {
                        let needsStatus: Status = deliveryApproval && status != .interrupted ? .needsYou : status
                        deciding.append((.task(id: todo.id, status: needsStatus, agents: active.count), rank(todo, status), when))
                    }
                }
                listed()
            }
            // its supervisor's review is under way (not the owner's yet): in progress, whatever the To-dos page files it under
            else if bucket == .open || supervisorReviewing(todo, now: now) { open.append((row, rank(todo, status), when)); listed() }
            else if bucket == .shelved { shelved.append((row, rank(todo, status), when)); listed() }
            // finished work handed out by a conversation, for a day after it finished, seen or not (#10922); a failed start
            // that a related to-do finished is history, not news
            else if triage.section == .justDone || triage.section == .done, status != .failed, !(todo.origin ?? "").isEmpty {
                let ended = TodoTriageLogic.finishedAt(todo, triage)
                if ended > .distantPast, now.timeIntervalSince(ended) < TodoTriageLogic.justDone {
                    finished.append(Finished(row: row, fresh: showsJustFinished(todo, triage, now: now), at: ended))
                }
            }
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
                let first = asksOf[pair(agent.key.machineID, agent.key.sessionID)]?.first?.createdAt
                if !directAttention || first != nil { asking.append((.agent(agent), first)) }
            } else {
                running.append(.agent(agent))
            }
        }

        // a plan waiting for Start is the owner's own decision: Main lists every open one but Health's (as the web's
        // assistant-entries), another conversation its own
        let waitingPlans = plans.values.filter { ($0.conversation == conversation || isMain && $0.space != "health") && $0.state == .open }
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
                      shelved: stable(shelved, newest).map(\.row),
                      // newest finished first; the same moment by id, so every client lists them in one order (the web's)
                      finished: finished.sorted { a, b in a.at != b.at ? a.at > b.at : a.row.id < b.row.id },
                      starting: starting)
    }
}

/// What the trays list: the split, and in front of Needs You the conversation's own cards (Persona asking, an
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
    /// The not-started to-dos whose dispatch is still in flight; computed with the split, not again in each row's body.
    var starting: Set<String> = []
    /// Folded under In Progress, not counted.
    var shelved: [AssistantEntries.Row] = []
    /// The Finished tray's rows, newest finished first (`AssistantEntries.Result.finished`).
    var finished: [AssistantEntries.Finished] = []
    /// Needs You's rows grouped by supervising conversation (`grouped`); empty when the tray is one list, as it always
    /// was. Grouping never changes the rows or the count.
    var needsYouGroups: [NeedsYouGroup] = []

    init() {}

    init(_ entries: AssistantEntries.Result, asks: [Ask] = []) {
        needsYou = asks.map(Row.ask) + entries.needsYou.map(Row.entry)
        running = entries.running.map(Row.entry)
        starting = entries.starting
        shelved = entries.shelved
        finished = entries.finished
    }

    func rows(_ tray: ActionTray) -> [Row] {
        switch tray {
        case .needsYou: needsYou
        case .running: running
        case .finished: finished.map { .entry($0.row) }
        }
    }

    /// The number its entry shows: its rows (shelved ones are not counted). Finished's is finished work, never open work.
    func count(_ tray: ActionTray) -> Int { rows(tray).count }

    /// The entries the bar shows: Needs You (always visible) and In Progress always; Finished is not persistent on the
    /// collapsed bar and appears on the bar only while its tray is open (reachable from any open tray's header).
    func entries(open: ActionTray?) -> [ActionTray] {
        ActionTray.allCases.filter { $0 != .finished || open == .finished }
    }

    /// Whether the open tray header offers a one-tap link into the 24-hour Finished list.
    func offersFinishedLink(for tray: ActionTray) -> Bool {
        tray != .finished && !finished.isEmpty
    }

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
        case .finished: String(localized: "Finished [Action Bar]")
        }
    }

    var empty: String {
        switch self {
        case .needsYou: String(localized: "All Caught Up")
        case .running: String(localized: "Nothing in progress right now")
        case .finished: String(localized: "Nothing finished in the last 24 hours")
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
    /// The pager over the composer (#12831): its To Decide entry comes first, and one of it and a tray shows at a time.
    var dock: QuestionDockState? = nil
    /// The conversation's own cards the pager holds (with its tasks' questions, read here).
    var dockOwn: [DockCard] = []
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
    /// Bumped once a minute while the bar is in front, so the split reads the clock again: a supervisor's review turns
    /// the owner's at the hour while nothing else changes (`AssistantEntries.reviewOverdueAfter`; the web and Android
    /// tick the same way).
    @State private var minute = 0
    /// The main conversation's held pushes (#7782), made the first time the bar reads them there, and whether their tray
    /// is the one open: in the trays' place, so never with `open` (#8223).
    @State private var held: PushInterceptsModel?
    @State private var heldOpen = false
    /// Whether the main conversation's Inbox wrap-up tray is open in the shared tray slot (mutually exclusive with
    /// `open`, `heldOpen`, and `dock`).
    @State private var wrapUpOpen = false
    private var store: TasksStore { .shared }

    private var entryRow: AnyLayout { AnyLayout(HStackLayout(spacing: Theme.Space.s)) }

    var body: some View {
        let _ = minute
        let content = Self.content(conversation, key: key)
        let row = entryRow
        let inMain = conversation == AppModel.shared.assistantMain?.sessionID && AppModel.shared.api != nil
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let open {
                tray(open, content)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            } else if wrapUpOpen, inMain {
                WrapUpTray(conversation: conversation) { withAnimation(Theme.Motion.state) { wrapUpOpen = false } }
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            } else if heldOpen, let held {
                PushInterceptsTray(model: held) { withAnimation(Theme.Motion.state) { heldOpen = false } }
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            // Wider than the bar (Wrap Up, Held Pushes, the computer's pill, or a large text size), the entries scroll
            // sideways instead of running off its edge, as on the web and Android (#10922); while they fit nothing moves.
            ScrollView(.horizontal, showsIndicators: false) {
                row {
                    if let dock {
                        let ids = QuestionDock.cards(own: dockOwn, conversation: conversation).map(\.id)
                        if !ids.isEmpty {
                            QuestionDockEntry(count: ids.count, arrived: QuestionDockLogic.arrived(ids, seen: dock.seen),
                                              open: dock.showing(ids)) {
                                Haptics.tap()
                                withAnimation(Theme.Motion.state) {
                                    if dock.showing(ids) {
                                        dock.fold(ids)
                                    } else {
                                        open = nil
                                        wrapUpOpen = false
                                        heldOpen = false
                                        dock.show()
                                    }
                                }
                            }
                        }
                    }
                    ForEach(content.entries(open: open)) { entry($0, count: content.count($0)) }
                    if inMain {
                        WrapUpEntry(conversation: conversation, open: wrapUpOpen) {
                            withAnimation(Theme.Motion.state) {
                                open = nil
                                heldOpen = false
                                wrapUpOpen.toggle()
                            }
                        }
                    }
                    if inMain, let held {
                        PushInterceptsEntry(model: held, open: heldOpen) {
                            withAnimation(Theme.Motion.state) {
                                open = nil
                                wrapUpOpen = false
                                heldOpen.toggle()
                            }
                        }
                    }
                    // A Mac the model operates (the screen-share module's pill), as the entries.
                    ForEach(Modules.registry.views(at: .chatFooter).filter { $0.shows(.init(activity: computer)) }) {
                        $0.view(.init(activity: computer)).background(Theme.pageBackground, in: .capsule)
                    }
                }
                .padding(.horizontal, Theme.Space.xs)
            }
            .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
            .padding(.top, Theme.Space.s)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        // No fade of its own: the chat runs on behind the bar and fades out there (`FooterFog`, #3696).
        .onChange(of: conversation) { open = nil; wrapUpOpen = false; heldOpen = false; expanded = []; drawn = []; planChoices = [:] }
        // One tray at a time: Needs You's, In Progress' or Finished's opening closes Wrap Up and Held Pushes.
        .onChange(of: open) { if open == nil { expanded = []; drawn = []; planChoices = [:] } else { wrapUpOpen = false; heldOpen = false } }
        // The pager steps aside while a tray is open (#12831).
        .onChange(of: open != nil || wrapUpOpen || heldOpen, initial: true) { _, tray in dock?.trayOpen = tray }
        // The held pushes are read in the main conversation while its tab is in front, every 30 s, with ln mode off too,
        // so the entry comes when the mode is turned on; the bar reads none of the model's values itself.
        .tabActiveTask(id: inMain) {
            guard inMain, let api = AppModel.shared.api else { return }
            let model = held ?? PushInterceptsModel(api: api)
            if held == nil { held = model }
            await model.run()
        }
        .onChange(of: inMain) { _, inMain in if !inMain { held = nil; heldOpen = false; wrapUpOpen = false } }
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
        .tabActiveTask {
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(60)) } catch { return }
                minute &+= 1
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("action-bar")
    }

    static func content(_ conversation: String, key: SessionKey? = nil) -> ActionTrayContent {
        let store = TasksStore.shared
        let model = AppModel.shared
        let isMain = !(model.assistantMain?.sessionID ?? "").isEmpty && conversation == model.assistantMain?.sessionID
        let todos = store.todos.values.sorted { ($0.createdAt ?? .distantPast, $0.id) > ($1.createdAt ?? .distantPast, $1.id) }
        // Only the tasks' own agents, which is all a task's status reads, and the agents going on as the dock had them: the
        // bar must not follow every agent's push (AllSessionsReadersTests, #3446).
        let entries = AssistantEntries.split(
            conversation: conversation, main: model.assistantMain?.sessionID, group: model.hub?.group ?? "", todos: todos,
            sessions: todos.filter { $0.doneAt == nil || $0.failedToStart }.flatMap { store.sessions($0) },
            interactions: model.aggregatePendingInteractions, plans: store.plans,
            feed: store.feed(conversation), restored: TodoRestores.load(), going: model.goingAgents,
            // the hub's fields, the inbox's word on the item, and Later
            messageWaits: { item in
                guard item.kind == .message, let id = item.ref, !store.isLater(item) else { return false }
                return store.messageState(item, itemID: id) == .open
            },
            decisionHidden: { store.decisionHidden($0) }, directUserOnly: isMain)
        let asks = (key.map { model.interactions(for: $0) } ?? [])
            .filter { !isMain || Supervision.directUserRequest($0) }
            .sorted { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }
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
                if content.offersFinishedLink(for: tray) {
                    Button {
                        Haptics.tap()
                        withAnimation(Theme.Motion.state) { open = .finished }
                    } label: {
                        HStack(spacing: Theme.Space.xs) {
                            if content.finished.contains(where: \.fresh) {
                                StatusDot(state: .unread, size: Theme.Size.dot)
                            }
                            Text(ActionTray.finished.title)
                            if let text = CountBadge.text(content.count(.finished)) { Text(text).monospacedDigit() }
                        }
                        .dockCapsule()
                        .fixedSize()
                        .tapTargetHeight()
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("action-tray.finished-link")
                }
                // The board has everything; of what finished, the trays list only the last 24 hours' (Finished).
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
            if rows.isEmpty && (tray != .running || content.shelved.isEmpty) {
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
                                ForEach(group.rows) { row($0, starting: content.starting) }
                            }
                        } else if tray == .finished {
                            ForEach(content.finished) { finishedRow($0) }
                        } else {
                            ForEach(rows) { row($0, starting: content.starting) }
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

    @ViewBuilder private func row(_ row: ActionTrayContent.Row, starting: Set<String>) -> some View {
        switch row {
        case .ask(let ask):
            // The card itself is right below the bar: the row closes the tray to show it.
            Button {
                withAnimation(Theme.Motion.state) {
                    open = nil
                    dock?.show(ask.id) // the pager, on that card (#12831)
                }
            } label: {
                line(title: ask.title, detail: StateName.needsYou.text, tint: AgentState.needsYou.tone.text, trail: .open) {
                    ThemedIcon(Theme.Icon.askFirst).font(Theme.Font.labelStrong).foregroundStyle(AgentState.needsYou.tone.text)
                }
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("action-tray.ask")
        case .entry(let entry):
            self.entry(entry, starting: starting)
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

    @ViewBuilder private func entry(_ row: AssistantEntries.Row, starting: Set<String>) -> some View {
        switch row {
        case .task(let id, let status, let agents):
            if let todo = store.todos[id] {
                let state = status.taskState
                let isStarting = starting.contains(id)
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
                    VStack(alignment: .leading, spacing: 0) {
                        Button {
                            open = nil
                            store.detailTask = id
                        } label: {
                            line(title: todo.title,
                                 detail: activity.isActive && status == .waiting ? StateName.working.text : detail(todo, status: status, agents: agents, starting: isStarting),
                                 tint: tint(status), subtitle: subtitle(todo, status: status, lead: lead, activity: activity, starting: isStarting),
                                 subtitleTint: status == .failed || status == .interrupted ? Theme.danger : nil,
                                 subtitleLines: TurnErrorReason.invalid(lead) ? nil : 1,
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
                                } else if status == .notStarted, isStarting {
                                    StartingMark().accessibilityIdentifier("action-tray.starting-mark")
                                } else if status == .notStarted {
                                    StatusDot(state: .offline, size: Theme.Size.dotL)
                                        .accessibilityIdentifier("action-tray.not-started-mark")
                                } else {
                                    TaskMark(state: state)
                                }
                            }
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("action-tray.task")
                        .contextMenu { TaskRemoveMenuItem(todo: todo, state: state, pending: $removing) }
                        actions(todo, status: status, lead: lead)
                    }
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
    private func detail(_ todo: TodoItem, status: AssistantEntries.Status, agents: Int, starting: Bool) -> String {
        let review = todo.reviewState
        let resting = status == .followUp || status == .unread || status == .notStarted
        let label = status == .notStarted && !starting ? status.taskState.todoLabel : status.taskState.label
        var parts: [String]
        if status == .interrupted, TurnErrorReason.invalid(store.leadSession(todo)) {
            parts = [String(localized: "Invalid conversation")]
        }
        else if status == .interrupted { parts = [String(localized: "Interrupted [Chat Layout]")] }
        else if status == .usageLimit { parts = [String(localized: "Usage Limit")] }
        else if AssistantEntries.saysSupervisorReview(todo, status: status, now: .now) { parts = [ReviewBadge.supervisorReview] }
        else if resting, review == .pendingReview || review == .needsUser { parts = [todo.reviewBadge?.label ?? label] }
        else { parts = [label] }
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
    private func subtitle(_ todo: TodoItem, status: AssistantEntries.Status, lead: Session?, activity: AssistantTrayActivity, starting: Bool) -> String? {
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
            return one(TurnErrorReason.of(lead) ?? lead?.turnError)
        case .notStarted:
            return starting ? String(localized: "Starting the agent") : nil
        case .failed:
            // in plain words; the raw error is the task's details' (#8237)
            return store.startFailureRow(todo)
        case .needsYou:
            return TaskAsk.waitingLine(store.interactions(todo)) ?? (todo.delivery?.waitsForOwner == true ? one(todo.delivery?.gate) : nil)
        case .working, .waiting:
            return activity.isActive ? nil : one(TaskNow(lead: lead, working: status == .working)?.text)
        case .unread, .followUp:
            // stopped short of done: what the completion check said is missing (#3180), else the Agent's last words
            if let why = todo.stoppedReason(working: false) {
                return why.isEmpty ? String(localized: "Stopped, not done yet") : String(localized: "Stopped, not done yet · \(why)")
            }
            return one(lead?.lastText)
        case .done:
            return one(lead?.lastText)
        }
    }

    /// What a row offers right under its words (#8237; the receipt card's buttons until then): Retry and Remove for a start
    /// that failed, Continue for a turn the model service cut off (or a usage limit nothing continues by itself).
    @ViewBuilder private func actions(_ todo: TodoItem, status: AssistantEntries.Status, lead: Session?) -> some View {
        let retry = status == .failed && store.state(todo) == .failed
        let invalid = TurnErrorReason.invalid(lead)
        let resume = !invalid && (status == .interrupted || status == .usageLimit && lead.map { !$0.usageLimitContinues } == true)
        if retry {
            HStack(spacing: Theme.Space.s) {
                Color.clear.scaledFrame(width: Theme.Size.markM, height: 0).accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if let task = store.planTask(todo) { TaskShellQuestion(todo: todo, task: task) }
                    TaskRetryButtons(todo: todo, idPrefix: "action-tray", remove: { store.askRemove(todo, pending: $removing) })
                }
            }
            .font(Theme.Font.captionStrong)
            .padding(.horizontal, Theme.Space.m)
            .padding(.bottom, Theme.Space.s)
        } else if resume || status == .failed {
            let busy = store.busyRetries.contains(todo.id)
            HStack(spacing: Theme.Space.s) {
                // under the words: as wide as the row's mark and the gap after it
                Color.clear.scaledFrame(width: Theme.Size.markM, height: 0).accessibilityHidden(true)
                if resume {
                    Button { Task { await store.retry(todo) } } label: {
                        Text("Continue").lineLimit(1)
                            .opacity(busy ? 0 : 1).overlay { if busy { ProgressView().controlSize(.small) } }
                    }
                    .themedButton(prominent: true)
                    .disabled(busy)
                    .accessibilityIdentifier("action-tray.continue")
                }
                if status == .failed {
                    Button { store.askRemove(todo, pending: $removing) } label: {
                        Text("Remove [Task Sheets 6135d4]").lineLimit(1)
                    }
                    .themedButton()
                    .accessibilityIdentifier("action-tray.remove")
                }
            }
            .font(Theme.Font.captionStrong)
            .padding(.horizontal, Theme.Space.m)
            .padding(.bottom, Theme.Space.s)
        }
    }

    /// A Finished row (#10922; under In Progress as Just Finished until then, #8237): the done mark, the title, Done and
    /// what it did. One nobody opened since it finished is new: Just Finished's word and the finished-unseen green dot,
    /// as the To-dos page names it. Opening it shows its details and counts as seeing it (#5707): it stays, as Done.
    @ViewBuilder private func finishedRow(_ item: AssistantEntries.Finished) -> some View {
        if case .task(let id, _, _) = item.row, let todo = store.todos[id] {
            Button {
                open = nil
                store.detailTask = id
                if item.fresh { see(todo) }
            } label: {
                line(title: todo.title, detail: item.fresh ? TodoSection.justDone.title : TaskState.done.label,
                     tint: item.fresh ? AgentState.unread.tone.text : nil, subtitle: finishedLine(todo), trail: .open) {
                    if item.fresh {
                        StatusDot(state: .unread, size: Theme.Size.dotL).accessibilityIdentifier("action-tray.new-mark")
                    } else {
                        TaskMark(state: .done)
                    }
                }
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier(item.fresh ? "action-tray.finished-row.new" : "action-tray.finished-row")
            .contextMenu { TaskRemoveMenuItem(todo: todo, state: .done, pending: $removing) }
        }
    }

    /// What a finished task did, in a line (the web's finished row): its accepting review's note, why it closed, its
    /// receipt's summary, else its Agent's last words.
    private func finishedLine(_ todo: TodoItem) -> String? {
        let receipt = store.feed(todo.origin ?? conversation).last { $0.kind == .receipt && $0.ref == todo.id && !$0.failed }?.summary
        for text in [todo.supervision?.latestReview?.note, todo.doneReason, receipt, store.leadSession(todo)?.lastText] {
            let t = Fmt.plain(text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            if !t.isEmpty { return t }
        }
        return nil
    }

    /// Opening a new Finished row counts as seeing it (#5707), where the hub keeps seen marks: it stays in Finished without
    /// its Just Finished mark (#10922); a failure leaves the mark.
    private func see(_ todo: TodoItem) {
        guard AppModel.shared.hubFeatures.contains(TodoSeen.feature) else { return }
        // the task's own Agents, as the row has them: the bar never reads every session (AllSessionsReadersTests)
        let t = TodoTriageLogic.triage(todo, lookup: TodosLogic.makeSessionLookup(store.sessions(todo)), now: .now)
        guard AssistantEntries.showsJustFinished(todo, t, now: .now) else { return }
        Task { try? await TodoStore.shared.setSeen(id: todo.id, true, after: t.last) }
    }

    /// What follows a row's words on the right.
    private enum Trail { case none, open, folded, expanded }

    private func line<Mark: View>(title: String, detail: String, tint: Color? = nil, subtitle: String? = nil, subtitleTint: Color? = nil, subtitleLines: Int? = 1,
                                  trail: Trail = .open, activity: AssistantTrayActivity = .init(),
                                  @ViewBuilder mark: () -> Mark) -> some View {
        HStack(spacing: Theme.Space.s) {
            mark().scaledFrame(width: Theme.Size.markM)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(spacing: Theme.Space.s) {
                    Text(title).font(Theme.Font.body).lineLimit(1)
                    Spacer(minLength: Theme.Space.s)
                    // Its words, never wider than a part of the row: a long wait line cut at the tail, not the row (and the
                    // tray with it) wider than the screen (#13656).
                    Text(detail).font(Theme.Font.caption).lineLimit(1).truncationMode(.tail)
                        .scaledFrame(maxWidth: Theme.Size.box160, alignment: .trailing)
                        .layoutPriority(1)
                        .foregroundStyle(tint.map { AnyShapeStyle($0) } ?? AnyShapeStyle(.secondary))
                }
                if let subtitle {
                    Text(subtitle).font(Theme.Font.caption).lineLimit(subtitleLines)
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
