import Foundation
import Observation
import PersonaWire

/// Task presentation for the hub-level group's assistant conversations (docs/assistant/unified-entry.md): todos come from
/// TodoStore's shared index; plans and each conversation's feed stay here. Feeds refresh when shown and on
/// reconnect, with assistant_feed and task_plan events keeping them current. A task's agents come from AppModel's
/// sessions and interactions, so a card follows them like the Agents page does.
@MainActor @Observable
final class TasksStore {
    static let shared = TasksStore(viewStore: .shared)

    var todos: [String: TodoItem] { todoStore.byID }
    private(set) var plans: [String: TaskPlan] = [:]
    /// Conversation id → its feed, oldest first.
    private(set) var feeds: [String: [FeedItem]] = [:]
    /// Conversation id → the task the next message goes straight to (Message “xx”), set by @ or Message.
    var routes: [String: String] = [:]
    /// The task whose detail sheet is open, and whether the board is.
    var detailTask: String? {
        didSet { detailTaskAgent = nil }
    }
    /// A dispatch's resolved Agent, carried into its task detail. Ordinary task opens clear this selection.
    private(set) var detailTaskAgent: SessionKey?
    /// The agent (not a task's, #1113) whose page the detail sheet shows instead.
    var detailAgent: SessionKey?
    /// The agent whose page the assistant page pushes full screen (the sheet's Full Screen): the page takes it and clears it.
    var fullAgent: SessionKey?
    var showBoard = false
    /// A task or an agent picked on the board, opened when the board has gone down.
    var openAfterBoard: String?
    var openAgentAfterBoard: SessionKey?
    /// Feed card IDs put off with Later, sent here, or refused as handled elsewhere (409 inbox_item_done).
    /// A new draft with the same Inbox reference has its own decision state. Private so a view can only ask by the card
    /// itself (`isLater`), never by its Inbox reference (#6002).
    private var laterMessages: Set<String> = []
    private(set) var sentMessages: Set<String> = []
    private(set) var filedMessages: Set<String> = []
    /// Decision rounds put off with Later, for this session only (`decisionKey`), and the ones Don't handle took out of
    /// Needs You, with their receipts, until the hub's list says so itself.
    private(set) var laterDecisions: Set<String> = []
    private(set) var dismissedDecisions: [String: String] = [:]
    /// Tasks whose decision is being dismissed or restored, and whose retry is out (their buttons take no second tap).
    private(set) var busyDecisions: Set<String> = []
    private(set) var busyRetries: Set<String> = []
    /// Quick results (QuickResult.callID) the user took back with Undo.
    private(set) var undone: Set<String> = []
    /// Plans being started or dismissed (their card shows it and takes no second tap).
    private(set) var busyPlans: Set<String> = []
    /// Send, dismissal and Undo share admission for the exact feed card.
    private(set) var busyMessages: Set<String> = []
    /// A bounded read stopped before its frozen recovery cursor completed; the next refresh resumes it.
    private(set) var feedRecoveryPending: Set<String> = []
    var error: String?

    @ObservationIgnored private var feedCursors: [String: String] = [:]
    @ObservationIgnored private var feedAPIs: [String: APIClient] = [:]
    @ObservationIgnored private var messageWrites: [String: UInt] = [:]
    @ObservationIgnored private let showToast: (Toast) -> Void
    @ObservationIgnored private let todoStore: TodoStore
    @ObservationIgnored private let viewStore: AssistantViewsStore
    @ObservationIgnored private var generation: UInt = 0
    @ObservationIgnored private var loading: [String: Task<Void, Never>] = [:]
    /// Conversation id → the day its Today card was asked for (once a day).
    @ObservationIgnored private var todayAsked: [String: String] = [:]

    init(todoStore: TodoStore = .shared, viewStore: AssistantViewsStore = AssistantViewsStore(),
         toast: @escaping (Toast) -> Void = { AppModel.shared.toast = $0 }) {
        self.todoStore = todoStore
        self.viewStore = viewStore
        self.showToast = toast
    }

    /// Entering or leaving Demo Mode, or another hub: nothing of the other side may show.
    func forget() {
        generation &+= 1
        for task in loading.values { task.cancel() }
        loading = [:]
        viewStore.forget()
        plans = [:]
        feeds = [:]
        routes = [:]
        detailTask = nil
        detailAgent = nil
        fullAgent = nil
        openAfterBoard = nil
        openAgentAfterBoard = nil
        showBoard = false
        undone = []
        error = nil
        todayAsked = [:]
        busyPlans = []
        busyMessages = []
        laterMessages = []
        laterDecisions = []
        dismissedDecisions = [:]
        busyDecisions = []
        busyRetries = []
        sentMessages = []
        filedMessages = []
        feedAPIs = [:]
        feedCursors = [:]
        feedRecoveryPending = []
        messageWrites = [:]
    }

    /// Full Screen in the detail sheet: the sheet goes down and the agent's page is pushed onto the assistant page, the
    /// page an agent opened from the Agents tab is. Its back comes to the conversation, not to the sheet.
    func expand(_ key: SessionKey) {
        detailTask = nil
        detailAgent = nil
        fullAgent = key
    }

    /// A dispatch link opens only its task. On iPhone its Agent is pushed on the Assistant stack; elsewhere, or
    /// before an Agent is available, the task's own detail hosts the chat/starting state. Never open In Progress.
    func openDispatchedTask(_ id: String, agent: SessionKey?, pushAgent: Bool) {
        if pushAgent, let agent {
            expand(agent)
        } else {
            fullAgent = nil
            detailAgent = nil
            detailTask = id
            detailTaskAgent = agent
        }
    }

    /// The detail asked for (`detailTask` with its dispatch's Agent, or `detailAgent`) as a value for a stack's path, the
    /// request taken (#13156): the Sidebar layout's page pushes it on its path, so it stays with the root it was opened
    /// on (`SidebarPageRoots`) instead of being pushed by view over whatever the sidebar picks next. Nil when none is asked.
    func takeDetail() -> TaskDetailRoute? {
        let route: TaskDetailRoute
        if let id = detailTask {
            route = TaskDetailRoute(todo: id, taskAgent: detailTaskAgent)
        } else if let key = detailAgent {
            route = TaskDetailRoute(agent: key)
        } else {
            return nil
        }
        detailTask = nil
        detailAgent = nil
        return route
    }

    // MARK: Loading

    func reloadTodos(api: APIClient? = nil) {
        todoStore.reload(api: api)
    }

    /// Pulls a conversation's feed (what came since the newest item it has, page after page: FeedPaging) and the todos.
    @discardableResult
    func load(_ conversation: String, refreshTodos: Bool = true, api injected: APIClient? = nil) -> Task<Void, Never>? {
        guard let api = injected ?? feedAPIs[conversation] ?? AppModel.shared.api else { return nil }
        // Pin the owner while this conversation is shown; Undo must not follow a later manager selection.
        feedAPIs[conversation] = api.manager(api.managerID ?? api.shownManager)
        if let task = loading[conversation] { return task }
        if refreshTodos { reloadTodos(api: injected) }
        let first = feeds[conversation]?.last?.at
        let ticket = generation
        let task = Task {
            defer { if generation == ticket { loading[conversation] = nil } }
            var since = first
            var cursor = feedCursors[conversation]
            var seenCursors: Set<String> = []
            for _ in 0..<FeedPaging.maxPages {
                let writes = messageWrites
                guard let res = try? await api.feed(conversation: conversation, since: since, cursor: cursor),
                      !Task.isCancelled, generation == ticket else { return }
                // A legacy hub ignores cursor; restart its timestamp request rather than applying that unrelated page.
                if cursor != nil, res.nextCursor == nil {
                    cursor = nil
                    feedCursors[conversation] = nil
                    continue
                }
                // The cards' snapshot comes whole with every page: what an event missed comes back here.
                viewStore.receive(res.views ?? [:], conversation: conversation, group: api.group)
                viewStore.receiveActions(res.viewActions ?? [:], conversation: conversation, group: api.group)
                viewStore.receiveBlocks(res.viewBlocks ?? [], conversation: conversation, group: api.group)
                let items = res.items.filter {
                    $0.conversation == conversation && writes[$0.id] == messageWrites[$0.id]
                }
                for item in items { viewStore.receive(item, group: api.group) }
                for (id, plan) in res.plans ?? [:] where plan.conversation == conversation && plans[id] != plan { plans[id] = plan }
                merge(items, into: conversation)
                if let next = res.nextCursor {
                    guard !next.isEmpty else {
                        feedCursors[conversation] = nil
                        feedRecoveryPending.remove(conversation)
                        return
                    }
                    feedRecoveryPending.insert(conversation)
                    guard next != cursor, seenCursors.insert(next).inserted else {
                        feedCursors[conversation] = nil
                        incompleteFeed()
                        return
                    }
                    cursor = next
                    feedCursors[conversation] = next
                    continue
                }
                guard let next = FeedPaging.next(after: res.items, since: since) else {
                    feedRecoveryPending.remove(conversation)
                    return
                }
                since = next
            }
            if cursor != nil { incompleteFeed() }
        }
        loading[conversation] = task
        return task
    }

    private func incompleteFeed() {
        let message = String(localized: "Task updates aren't fully loaded. Refresh to continue.")
        error = message
        showToast(Toast(message, style: .error))
    }

    /// The (re)connect: every conversation shown so far, again.
    @discardableResult
    func reconnected(api: APIClient? = nil) -> [Task<Void, Never>] {
        feeds.keys.compactMap { load($0, refreshTodos: false, api: feedAPIs[$0] ?? api) }
    }

    func received(_ item: FeedItem, group: String = "") {
        // An authoritative push or write receipt wins over a snapshot already in flight, including a no-op Undo.
        if item.kind == .message { messageWrites[item.id, default: 0] &+= 1 }
        viewStore.receive(item, group: group)
        merge([item], into: item.conversation)
        // As a `todos` push: spaced, and only when the hub's revision moved (`TodoStore.pushed`).
        if item.kind == .task || item.kind == .receipt || item.kind == .review || item.kind == .report { todoStore.pushed() }
    }

    func received(_ plan: TaskPlan) {
        if plans[plan.id] != plan { plans[plan.id] = plan }
    }

    /// A suggested rule was answered, or is no longer offered (#5802): the plans held here drop it. The hub sends no
    /// plan event for it and a feed read brings only what came since, so nothing else would take it off them.
    func ruleSuggestionSettled(_ id: String) {
        for (key, plan) in plans where plan.suggestion?.id == id {
            plans[key] = plan.withoutSuggestion
        }
    }

    private func merge(_ items: [FeedItem], into conversation: String) {
        guard !items.isEmpty else {
            if feeds[conversation] == nil { feeds[conversation] = [] }
            return
        }
        var list = feeds[conversation] ?? []
        // Replace complete items, including receipt updates (#3834) and fields removed by draft Undo.
        var indexes = Dictionary(list.enumerated().map { ($0.element.id, $0.offset) }, uniquingKeysWith: { a, _ in a })
        var changed = false
        for item in items {
            if let index = indexes[item.id] {
                if list[index] != item { list[index] = item; changed = true }
            } else {
                indexes[item.id] = list.count
                list.append(item)
                changed = true
            }
        }
        guard changed else { return }
        list.sort { ($0.at, $0.id) < ($1.at, $1.id) }
        feeds[conversation] = list
    }

    // MARK: What a conversation shows

    func feed(_ conversation: String) -> [FeedItem] { feeds[conversation] ?? [] }

    /// The tasks the conversation opened, newest first.
    func tasks(of conversation: String) -> [TodoItem] {
        todos.values.filter { $0.origin == conversation }
            .sorted { ($0.createdAt ?? .distantPast) > ($1.createdAt ?? .distantPast) }
    }

    /// The agents going on that the conversation's action bar and board show besides its tasks (#1113): in the main
    /// conversation the ones no task stands for (opened by hand on the Agents page); in the others none.
    func agents(of conversation: String) -> [DockAgent] {
        let model = AppModel.shared
        guard let main = model.assistantMain?.sessionID, conversation == main else { return [] }
        let group = model.hub?.group ?? ""
        let tasks = todos.values.filter { !($0.origin ?? "").isEmpty && $0.doneAt == nil }
        let keys = Set(tasks.flatMap { t in
            t.activeAgents.map { SessionKey(group: group, machineID: $0.machineID, sessionID: $0.sessionID) }
        })
        let manual = todos.values.filter { ($0.origin ?? "").isEmpty }.map(\.id)
        return DockAgent.shown(model.goingAgents, conversation: conversation, main: main, group: group,
                               taskAgents: keys, manualTodos: Set(manual))
    }

    /// The task (a todo from an assistant conversation, not done) an agent works for.
    func task(of key: SessionKey) -> TodoItem? {
        guard key.group == (AppModel.shared.hub?.group ?? "") else { return nil }
        return todos.values.first { t in
            t.origin != nil && t.doneAt == nil && t.activeAgents.contains { $0.machineID == key.machineID && $0.sessionID == key.sessionID }
        }
    }

    /// The task's agents as the app knows them now (the Agents page's sessions), with their last line and step: a card
    /// follows each new step of an agent that only runs tools (`AppModel.sessionWithLine(_:)`).
    func sessions(_ todo: TodoItem) -> [Session] {
        let model = AppModel.shared
        let group = model.hub?.group ?? ""
        return todo.activeAgents.compactMap { model.sessionWithLine(SessionKey(group: group, machineID: $0.machineID, sessionID: $0.sessionID)) }
    }

    func state(_ todo: TodoItem) -> TaskState {
        let state = TaskState(todo: todo, sessions: sessions(todo))
        // A hub from before #1077 leaves the todo open when the agent did not start; its plan still says why.
        if state == .notStarted, todo.agents.isEmpty, planError(todo) != nil { return .failed }
        return state
    }

    /// Why the task's agent did not start: the todo's own word, else its plan's (nil while it may still start).
    func startFailure(_ todo: TodoItem) -> String? {
        if let e = todo.startError, !e.isEmpty { return e }
        return todo.agents.isEmpty ? planError(todo) : nil
    }

    private func planError(_ todo: TodoItem) -> String? {
        guard let error = planTask(todo)?.error, !error.isEmpty else { return nil }
        return error
    }

    /// The plan task behind this to-do, including one a plan took over without leaving `plan_id` on the to-do.
    func planTask(_ todo: TodoItem) -> PlanTask? { TaskStartRemedy.planTask(of: todo, in: plans) }

    func retryLauncher(_ todo: TodoItem, features: Set<String> = AppModel.shared.hubFeatures) -> String? {
        TaskStartRemedy.retryLauncher(planTask(todo), features: features)
    }

    func startQuestion(_ todo: TodoItem, features: Set<String> = AppModel.shared.hubFeatures, now: Date = .now) -> ShellQuestion? {
        TaskStartRemedy.taskQuestion(planTask(todo), features: features, now: now)
    }

    func startFailureRow(_ todo: TodoItem) -> String? {
        let task = planTask(todo)
        if let line = TaskStartRemedy.failureLine(code: task?.errorCode, args: task?.errorArgs) { return line }
        return startFailure(todo).map(StartFailureWords.rowLine)
    }

    func startFailureCard(_ todo: TodoItem, canOpen: Bool) -> String? {
        let task = planTask(todo)
        if let line = TaskStartRemedy.failureLine(code: task?.errorCode, args: task?.errorArgs) {
            return String(localized: "Didn't start: \(line) [Tasks Store 38bbb7]")
        }
        return startFailure(todo).map { StartFailureWords.cardLine($0, canOpen: canOpen) }
    }

    /// The detail page leads with the coded short line; its raw hub error remains available under Details.
    func startFailureSummary(_ todo: TodoItem) -> String? {
        let task = planTask(todo)
        return TaskStartRemedy.failureLine(code: task?.errorCode, args: task?.errorArgs) ?? startFailure(todo)
    }

    /// A direct to-do link may arrive before its conversation's feed; read it so the coded remedy fields can appear.
    func loadStartRemedy(_ todo: TodoItem) async {
        guard planTask(todo) == nil, startFailure(todo) != nil, let origin = todo.origin, !origin.isEmpty else { return }
        await load(origin, refreshTodos: false)?.value
    }

    /// What a waiting task waits for (#3274): the first of its agents that waits to be woken, in its own words.
    func waitLine(_ todo: TodoItem) -> String? { sessions(todo).lazy.compactMap(\.waitLine).first }

    /// The task's agent to open first: the one that waits for the user, then the one working, then the latest.
    func leadSession(_ todo: TodoItem) -> Session? {
        sessions(todo).min { a, b in
            a.state.priority != b.state.priority ? a.state.priority < b.state.priority : (a.updatedAt ?? .distantPast) > (b.updatedAt ?? .distantPast)
        }
    }

    /// What the task's agents are asking the user, oldest first.
    func interactions(_ todo: TodoItem) -> [Interaction] {
        let keys = Set(sessions(todo).map(\.key))
        return AppModel.shared.aggregatePendingInteractions.filter { keys.contains($0.sessionKey) }
    }

    /// The main conversation's Today card, once a day: the hub makes it on the first ask and answers it after that.
    @discardableResult
    func loadToday(_ conversation: String, api injected: APIClient? = nil) -> Task<Void, Never>? {
        let day = Date.now.formatted(.iso8601.year().month().day())
        guard todayAsked[conversation] != day, let api = injected ?? AppModel.shared.api else { return nil }
        todayAsked[conversation] = day
        let ticket = generation
        return Task {
            guard generation == ticket else { return }
            let item = try? await api.today(conversation: conversation)
            guard generation == ticket else { return }
            if let item { received(item, group: api.group) } else { todayAsked[conversation] = nil }
        }
    }

    // MARK: Actions

    /// Send and dismiss admit only one operation per displayed card, before their first suspension.
    func sendMessage(_ card: FeedItem, itemID: String, text: String, api injected: APIClient? = nil) async {
        guard messageState(card, itemID: itemID) == .open, !busyMessages.contains(card.id),
              let api = injected ?? feedAPIs[card.conversation] else { return }
        let ticket = beginMessage(card.id)
        defer { endMessage(card.id, generation: ticket) }
        // Keep the Inbox's optimistic filing, saved failed reply and item-wide Send lock on the existing path.
        if injected == nil, let item = cardItem(itemID, conversation: card.conversation) {
            let sent = await InboxStore.shared.send(item, text: text, card: card.id, api: api)
            guard generation == ticket else { return }
            if sent { sentMessages.insert(card.id) } else { InboxStore.shared.reload() }
            return
        }
        do {
            try await api.inboxReply(itemID, text: text, card: card.id)
            guard generation == ticket else { return }
            sentMessages.insert(card.id)
            Haptics.success()
            InboxStore.shared.reload()
        } catch let e as APIError where e.code == "inbox_item_done" {
            guard generation == ticket else { return }
            filedMessages.insert(card.id)
            InboxStore.shared.reload()
        } catch {
            guard generation == ticket else { return }
            showToast(Toast(String(localized: "Reply not sent: \(error.localizedDescription)"), style: .error))
        }
    }

    @discardableResult
    func dismissMessage(_ card: FeedItem, api injected: APIClient? = nil) async -> Bool {
        guard card.kind == .message, let id = card.ref, messageState(card, itemID: id) == .open,
              !busyMessages.contains(card.id), let api = injected ?? feedAPIs[card.conversation] else { return false }
        let ticket = beginMessage(card.id)
        defer { endMessage(card.id, generation: ticket) }
        do {
            let result = try await api.dismissDraft(card.id, original: card.draft ?? "")
            guard generation == ticket else { return false }
            received(result.item, group: api.group)
            Haptics.success()
            showToast(Toast(String(localized: "Draft dismissed. Nothing was sent."), style: .success,
                            action: Toast.Action(title: String(localized: "Undo")) { [weak self] in
                                Task { await self?.undoDismissMessage(card, receipt: result.dismissalID, api: api, expectedGeneration: ticket) }
                            }, duration: Theme.Motion.draftUndo))
            return true
        } catch {
            guard generation == ticket else { return false }
            showToast(Toast(String(localized: "Couldn't dismiss draft: \(error.localizedDescription)"), style: .error))
            if let e = error as? APIError, e.status == 409 { load(card.conversation, refreshTodos: false, api: api) }
            return false
        }
    }

    @discardableResult
    func undoDismissMessage(_ card: FeedItem, receipt: String, api: APIClient, expectedGeneration: UInt? = nil) async -> Bool {
        guard !receipt.isEmpty, !busyMessages.contains(card.id),
              expectedGeneration == nil || generation == expectedGeneration else { return false }
        let ticket = beginMessage(card.id)
        defer { endMessage(card.id, generation: ticket) }
        do {
            let item = try await api.undoDraftDismissal(card.id, receipt: receipt)
            guard generation == ticket else { return false }
            received(item, group: api.group)
            laterMessages.remove(card.id)
            Haptics.tap()
            return true
        } catch {
            guard generation == ticket else { return false }
            showToast(Toast(String(localized: "Couldn't restore draft: \(error.localizedDescription)"), style: .error))
            if let e = error as? APIError, e.status == 409 { load(card.conversation, refreshTodos: false, api: api) }
            return false
        }
    }

    private func beginMessage(_ id: String) -> UInt {
        busyMessages.insert(id)
        messageWrites[id, default: 0] &+= 1
        return generation
    }

    private func endMessage(_ id: String, generation ticket: UInt) {
        guard generation == ticket else { return }
        messageWrites[id, default: 0] &+= 1
        busyMessages.remove(id)
    }

    func later(_ cardID: String) { laterMessages.insert(cardID) }
    /// Put off with Later, by the card's own id: Needs You and the chat's feed both ask this, so a card whose Inbox item
    /// (`ref`) differs from its id leaves both (#6002).
    func isLater(_ card: FeedItem) -> Bool { laterMessages.contains(card.id) }
    /// A card put off with Later, opened again (a hand-off card's one line, #5932).
    func unlater(_ cardID: String) { laterMessages.remove(cardID) }

    // MARK: Decisions (Later / Don't handle on a decision row)

    /// One round of one task's decision, as the web keys it (`decisionLaterKey`): a new round is a new question, so
    /// Later and Don't handle do not follow the task to it. Nil without a round to name. All timestamp sources use the
    /// same round-trip token, so a snapshot gaining latestReview does not rename the same round. Invalid old strings
    /// remain usable as-is; API requests still send the hub's original `review_at` string.
    nonisolated static func decisionKey(_ todo: TodoItem) -> String? {
        func canonical(_ raw: String) -> String {
            JSON.parseDate(raw).map { String($0.timeIntervalSince1970) } ?? raw
        }
        let round: String
        if let at = todo.supervision?.latestReview?.at {
            round = String(at.timeIntervalSince1970)
        } else if let raw = todo.decisionReview?.reviewAt, !raw.isEmpty {
            round = canonical(raw)
        } else if let raw = todo.ask?.askedAt, !raw.isEmpty {
            round = canonical(raw)
        } else {
            return nil
        }
        return "decision:\(todo.manager ?? ""):\(todo.id):\(round)"
    }

    /// Later puts the round off until the app starts again; nothing is written.
    func laterDecision(_ todo: TodoItem) {
        guard let key = Self.decisionKey(todo) else { return }
        laterDecisions.insert(key)
        Haptics.tap()
    }

    /// Whether Needs You leaves this decision out: put off, or dismissed (here, or elsewhere: the hub's `dismissal_id`).
    func decisionHidden(_ todo: TodoItem) -> Bool {
        guard let key = Self.decisionKey(todo) else { return false }
        return laterDecisions.contains(key) || dismissedDecisions[key] != nil || todo.decisionDismissed
    }

    /// Don't handle: the hub keeps the round dismissed and the task open (`POST /todos/{id}/decision-dismiss`); a toast
    /// offers Undo for a few seconds. 409: the round moved on, so the list is read again.
    @discardableResult
    func dismissDecision(_ todo: TodoItem, api injected: APIClient? = nil) async -> Bool {
        guard let round = todo.decisionReview?.reviewAt, let key = Self.decisionKey(todo), !busyDecisions.contains(todo.id),
              let api = (injected ?? AppModel.shared.api)?.manager(todoStore.named(todo.id)) else { return false }
        busyDecisions.insert(todo.id)
        let ticket = generation
        defer { if generation == ticket { busyDecisions.remove(todo.id) } }
        do {
            let result = try await api.dismissDecision(todo.id, reviewAt: round)
            guard generation == ticket else { return false }
            dismissedDecisions[key] = result.dismissalID ?? ""
            laterDecisions.remove(key)
            reloadTodos(api: injected)
            Haptics.success()
            let receipt = result.dismissalID ?? ""
            showToast(Toast(String(localized: "Decision dismissed. The task stays open."), style: .success,
                            action: receipt.isEmpty ? nil : Toast.Action(title: String(localized: "Undo")) { [weak self] in
                                Task { await self?.undoDecisionDismissal(todo, receipt: receipt, api: api, expectedGeneration: ticket) }
                            }, duration: Theme.Motion.draftUndo))
            return true
        } catch {
            guard generation == ticket else { return false }
            showToast(Toast(String(localized: "Couldn't dismiss: \(error.localizedDescription)"), style: .error))
            if let e = error as? APIError, e.status == 409 { reloadTodos(api: injected) }
            return false
        }
    }

    @discardableResult
    func undoDecisionDismissal(_ todo: TodoItem, receipt: String, api: APIClient, expectedGeneration: UInt? = nil) async -> Bool {
        guard let round = todo.decisionReview?.reviewAt, let key = Self.decisionKey(todo), !receipt.isEmpty, !busyDecisions.contains(todo.id),
              expectedGeneration == nil || generation == expectedGeneration else { return false }
        busyDecisions.insert(todo.id)
        let ticket = generation
        defer { if generation == ticket { busyDecisions.remove(todo.id) } }
        do {
            try await api.undoDecisionDismissal(todo.id, reviewAt: round, receipt: receipt)
            guard generation == ticket else { return false }
            dismissedDecisions[key] = nil
            laterDecisions.remove(key)
            reloadTodos()
            Haptics.tap()
            return true
        } catch {
            guard generation == ticket else { return false }
            showToast(Toast(String(localized: "Couldn't restore: \(error.localizedDescription)"), style: .error))
            if let e = error as? APIError, e.status == 409 { reloadTodos() }
            return false
        }
    }

    /// Retry on a task whose agent did not start, Continue on one a model error cut off: `POST /todos/{id}/retry`.
    /// An alternative launcher is sent only to a hub that advertises it; both variants share one busy ticket.
    @discardableResult
    func retry(_ todo: TodoItem, launcher: String? = nil, features: Set<String> = AppModel.shared.hubFeatures,
               api injected: APIClient? = nil) async -> Bool {
        guard !busyRetries.contains(todo.id), let api = (injected ?? AppModel.shared.api)?.manager(todoStore.named(todo.id)) else { return false }
        if launcher != nil, !features.contains(TaskStartRemedy.retryLauncherFeature) {
            showToast(Toast(String(localized: "This hub can't start it on another launcher yet. Update Persona on the hub, or Retry."), style: .error))
            return false
        }
        busyRetries.insert(todo.id)
        let ticket = generation
        defer { if generation == ticket { busyRetries.remove(todo.id) } }
        do {
            let result = try await api.retryTodo(todo.id, launcher: launcher)
            guard generation == ticket else { return false }
            try await todoStore.applyConfirmed(result, api: api)
            guard generation == ticket else { return false }
            Haptics.success()
            return true
        } catch {
            guard generation == ticket else { return false }
            showToast(Toast(String(localized: "Couldn't start it again: \(error.localizedDescription)"), style: .error))
            if let e = error as? APIError, e.status == 409 { reloadTodos(api: injected) }
            return false
        }
    }

    /// Answers the task's startup question. The hub restarts the task itself, so no chat message is resent here.
    func answerStart(_ todo: TodoItem, choice: String, features: Set<String> = AppModel.shared.hubFeatures,
                     api injected: APIClient? = nil) async throws {
        guard features.contains(TaskStartRemedy.answerFeature) else {
            throw APIError(status: 0, message: String(localized: "This hub can't answer this startup question yet."))
        }
        guard let api = (injected ?? AppModel.shared.api)?.manager(todoStore.named(todo.id)) else {
            throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]"))
        }
        let ticket = generation
        do {
            let result = try await api.answerTaskStart(todo.id, choice: choice)
            guard generation == ticket else { return }
            if let result { try await todoStore.applyConfirmed(result, api: api) } else { reloadTodos(api: injected) }
            guard generation == ticket else { return }
            Haptics.success()
        } catch {
            guard generation == ticket else { return }
            throw error
        }
    }

    enum MessageState { case open, sent, filed, dismissed }

    func messageCard(_ card: FeedItem) -> FeedItem {
        feeds[card.conversation]?.first { $0.id == card.id } ?? card
    }

    private func cardItem(_ itemID: String, conversation: String) -> InboxItem? {
        let owner = AppModel.shared.itemManager(feedAPIs[conversation]?.managerID)
        return InboxStore.shared.items.first {
            $0.id == itemID && AppModel.shared.itemManager($0.manager) == owner
        }
    }

    func messageState(_ card: FeedItem, itemID: String) -> MessageState {
        Self.messageState(messageCard(card), inbox: cardItem(itemID, conversation: card.conversation),
                          sentHere: sentMessages.contains(card.id), refused: filedMessages.contains(card.id))
    }

    /// A dismissal belongs to this card even while its Inbox reference is still open; a future card is independent.
    nonisolated static func messageState(_ card: FeedItem, inbox item: InboxItem?, sentHere: Bool, refused: Bool) -> MessageState {
        if sentHere || card.sentAt != nil { return .sent }
        if card.dismissedAt != nil { return .dismissed }
        if let item { return item.isDone ? .filed : .open }
        return card.filedAt != nil || refused ? .filed : .open
    }

    /// Undo on a quick result: the reminder or event comes off the phone.
    func undo(_ result: QuickResult) async {
        do {
            switch result.kind {
            case .reminder: _ = try RemindersTool.remove(id: result.itemID)
            case .event: _ = try CalendarTool.remove(id: result.itemID)
            }
            undone.insert(result.callID)
            Haptics.success()
        } catch {
            AppModel.shared.toast = Toast(String(localized: "Couldn't undo: \(error.localizedDescription)"), style: .error)
        }
    }

    /// The hub's client for a plan's own routes: they name no conversation, so the plan's conversation's manager is
    /// named (`AppModel.feedManager`), whichever assistant is current.
    private func client(for plan: TaskPlan) -> APIClient? {
        let model = AppModel.shared
        return model.api?.manager(model.feedManager(of: plan.conversation, group: model.hub?.group ?? ""))
    }

    func start(_ plan: TaskPlan, edits: [PlanTask]? = nil, remember: Bool = false, scheduleGrant: Bool = false) async {
        guard let api = client(for: plan), !busyPlans.contains(plan.id) else { return }
        let ticket = generation
        busyPlans.insert(plan.id)
        defer { if generation == ticket { busyPlans.remove(plan.id) } }
        do {
            let started = try await api.startPlan(plan.id, edits: edits, remember: remember, scheduleGrant: scheduleGrant)
            guard generation == ticket else { return }
            received(started)
            reloadTodos()
            Haptics.success()
        } catch {
            guard generation == ticket else { return }
            self.error = error.localizedDescription
            AppModel.shared.toast = Toast(String(localized: "Didn't start: \(error.localizedDescription) [Tasks Store 38bbb7]"), style: .error)
        }
    }

    /// Remove on a task that does not run: the todo goes (its agents and their chats stay on the Agents page), and with
    /// it the task's card, pill and line.
    func remove(_ todo: TodoItem) async {
        guard let api = AppModel.shared.api else { return }
        let ticket = generation
        do {
            try await todoStore.delete(id: todo.id, api: api)
            guard generation == ticket else { return }
            for (conversation, id) in routes where id == todo.id { routes[conversation] = nil }
            if detailTask == todo.id { detailTask = nil }
            Haptics.success()
        } catch {
            guard generation == ticket else { return }
            showToast(Toast(String(localized: "Couldn't remove: \(error.localizedDescription) [Tasks Store ab04c7]"), style: .error))
        }
    }

    func dismiss(_ plan: TaskPlan) async {
        guard let api = client(for: plan), !busyPlans.contains(plan.id) else { return }
        let ticket = generation
        busyPlans.insert(plan.id)
        defer { if generation == ticket { busyPlans.remove(plan.id) } }
        do {
            let dismissed = try await api.dismissPlan(plan.id)
            guard generation == ticket else { return }
            received(dismissed)
        } catch {
            guard generation == ticket else { return }
            showToast(Toast(error.localizedDescription, style: .error))
        }
    }

    /// Sends text straight to the task the conversation is routed to; false when there was none (send it as usual).
    func sendRouted(_ conversation: String, text: String) -> Bool {
        guard let todoID = routes[conversation], let api = AppModel.shared.api else { return false }
        routes[conversation] = nil
        let ticket = generation
        Task {
            guard generation == ticket else { return }
            do {
                let item = try await api.route(conversation: conversation, todoID: todoID, text: text)
                guard generation == ticket else { return }
                received(item, group: api.group)
            } catch {
                guard generation == ticket else { return }
                AppModel.shared.toast = Toast(String(localized: "Couldn't deliver: \(error.localizedDescription)"), style: .error)
            }
        }
        return true
    }
}

/// How a conversation's feed is read (#4642): the hub answers at most `pageSize` items after `since`, oldest first
/// (tasks.go `feedPage`), so a feed longer than that comes in pages, each after the newest of the one before, as the
/// web's `loadFeed` (#2171 / #2272) and the watch's cards read it. Without this a cold start showed only the oldest
/// 500, and the first push after it moved `since` past the rest for good.
nonisolated enum FeedPaging {
    static let pageSize = 500
    /// A bound on one load: a hub that kept answering full pages does not keep it going forever.
    static let maxPages = 40

    /// Where the next page starts after `page`, the answer to a request after `since`; nil when this was the last:
    /// a page that is not full, or one whose newest item is no later than `since` (it would come back the same).
    static func next(after page: [FeedItem], since: Date?) -> Date? {
        guard page.count >= pageSize, let newest = page.map(\.at).max() else { return nil }
        if let since, newest <= since { return nil }
        return newest
    }
}

/// A task's detail page (or an Agent's no task stands for) pushed by value (`TasksStore.takeDetail`, #13156).
nonisolated struct TaskDetailRoute: Hashable, Sendable {
    var todo: String?
    var agent: SessionKey?
    /// A dispatch's resolved Agent, shown first on the task's page.
    var taskAgent: SessionKey?
}
