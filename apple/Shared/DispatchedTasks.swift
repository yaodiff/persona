import Foundation

/// The dispatch's own links, not the conversation's running-task list. This uses the existing feed contract shared
/// with web and Android (#3718): task.ref, and a started plan's tasks[].todo_id in plan order. No new wire fields.
nonisolated enum DispatchedTasks {
    struct Entry: Identifiable, Equatable, Sendable {
        var id: String
        var todoID: String?
        var title: String
        /// How the Agent came to the task (`task` items' `wake`): created, active or resumed; nil reads as created.
        var wake: String? = nil
        /// Why a plan's task did not start, before there is a to-do to say so.
        var error: String? = nil
        /// The conversation the card is in: its failed receipt, if any, says why a task did not start.
        var conversation: String? = nil
        /// The Agent this card opens. Empty while the card still stands for the task.
        var machineID: String? = nil
        var sessionID: String? = nil
        /// The Agent row's title, used until the session has one of its own.
        var agentTitle: String? = nil
    }

    /// Agents linked within this of the first belong to the same hand-off. A later one is its own card (the web's
    /// `SAME_HANDOFF_MS`, #5096).
    static let sameHandoff: TimeInterval = 120

    /// The card's title, as the web's (`session.title`, then the Agent row, then the task). A session titled "Claude Code"
    /// stays: the web does not skip that name (`Handoff.tsx`).
    static func cardTitle(sessionTitle: String?, agentTitle: String?, taskTitle: String) -> String {
        let session = sessionTitle?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !session.isEmpty { return session }
        let agent = agentTitle?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !agent.isEmpty { return agent }
        return taskTitle
    }

    /// One card per Agent this hand-off started (#5110). No Agent yet: the one task card, which says it is starting,
    /// why it did not, or that it closed.
    static func cards(of entry: Entry, todo: TodoItem?) -> [Entry] {
        let started = (todo?.agents ?? []).filter { ($0.startError ?? "").isEmpty && !$0.sessionID.isEmpty }
        let shown = handoffAgents(started)
        guard let todo, !shown.isEmpty else { return [entry] }
        return shown.map { agent in
            var card = entry
            card.id = "\(agent.machineID)/\(agent.sessionID)"
            card.todoID = todo.id
            card.machineID = agent.machineID
            card.sessionID = agent.sessionID
            card.agentTitle = agent.title
            return card
        }
    }

    /// The Agents of one hand-off: those linked within [sameHandoff] of the first. One with no time counts as earliest.
    static func handoffAgents(_ started: [TodoItem.Agent]) -> [TodoItem.Agent] {
        guard !started.isEmpty else { return [] }
        let at = { (agent: TodoItem.Agent) in agent.linkedAt ?? .distantPast }
        let first = started.map(at).min() ?? .distantPast
        return started.filter { at($0).timeIntervalSince(first) <= sameHandoff }
    }

    /// The task detail's actual session selection. A requested target must still belong to this task; neither a
    /// stale request nor a stale lead can send the page to another task/hub. Pure so Mac routing is testable unlocked
    /// or locked, independently of the native UI test host.
    static func selectedAgent(_ requested: SessionKey?, available: [SessionKey], lead: SessionKey?) -> SessionKey? {
        if let requested, available.contains(requested) { return requested }
        if let lead, available.contains(lead) { return lead }
        return nil
    }

    /// nil means this is not a dispatch (keep its normal card); [] means a task already named by its plan.
    static func entries(for item: FeedItem, plans: [String: TaskPlan], titleOf: (String) -> String?, fallback: String) -> [Entry]? {
        switch item.kind {
        case .plan:
            guard let ref = item.ref, let plan = plans[ref] ?? item.plan,
                  plan.conversation == item.conversation, plan.state == .started else { return nil }
            return plan.tasks.enumerated().map { index, task in
                // A plan's tasks are New. A later `task` item for one of them adds no card, so its wake cannot change this.
                Entry(id: "plan:\(plan.id):\(index)", todoID: task.todoID,
                      title: task.todoID.flatMap(titleOf) ?? task.title, wake: nil, error: task.error, conversation: item.conversation)
            }
        case .task:
            guard let id = item.ref else { return [] }
            guard !plans.values.contains(where: { plan in
                plan.conversation == item.conversation && plan.tasks.contains(where: { $0.todoID == id })
            }) else { return [] }
            return [Entry(id: "todo:" + id, todoID: id, title: titleOf(id) ?? fallback, wake: item.wake, conversation: item.conversation)]
        default: return nil
        }
    }
}
