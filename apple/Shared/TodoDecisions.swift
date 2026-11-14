import Foundation
import PersonaWire

/// One call made on a todo (#1618, docs/assistant/todos.md "Decision records"): written by hand, the user's answer to a
/// question card of an agent on it, or noted when its task finished. The current ones go along when the todo is handed
/// to an agent; a revised one stays, with `supersededBy` set, for the history.
nonisolated struct TodoDecision: Decodable, Hashable, Identifiable, Sendable {
    var id: String
    var todoID: String
    var question: String
    var choice: String
    var reason: String
    /// manual, card, outcome, agent or thread; any other value reads as written by hand.
    var source: String
    var sourceMachine: String
    var sourceSession: String
    /// The question card (interaction id) it was answered in; empty for one written by hand.
    var sourceMessage: String
    /// The earlier decision this one revises.
    var supersedesID: String
    /// The newest decision that revises this one; empty while it is current.
    var supersededBy: String
    var createdAt: Date?

    var isCurrent: Bool { supersededBy.isEmpty }

    enum CodingKeys: String, CodingKey {
        case id, question, choice, reason, source
        case todoID = "todo_id", sourceMachine = "source_machine", sourceSession = "source_session",
             sourceMessage = "source_message", supersedesID = "supersedes_id", supersededBy = "superseded_by",
             createdAt = "created_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func text(_ key: CodingKeys) throws -> String { try c.decodeIfPresent(String.self, forKey: key) ?? "" }
        id = try c.decode(String.self, forKey: .id)
        todoID = try text(.todoID)
        question = try text(.question)
        choice = try text(.choice)
        reason = try text(.reason)
        source = try text(.source)
        sourceMachine = try text(.sourceMachine)
        sourceSession = try text(.sourceSession)
        sourceMessage = try text(.sourceMessage)
        supersedesID = try text(.supersedesID)
        supersededBy = try text(.supersededBy)
        createdAt = try c.decodeIfPresent(Date.self, forKey: .createdAt)
    }
}

/// How the Decisions list is shown (#6447): the first few, each time a to-do opens; every one; or none, folded under
/// its title.
nonisolated enum TodoDecisionsDisplay: Sendable, Equatable {
    case first, all, folded
}

/// The decision log's rules, shared with the web's todo page (`web/src/lib/todos.ts`).
nonisolated enum TodoDecisionsLogic {
    /// How many decisions show before Show All (the web's `DECISIONS_FIRST`).
    static let firstCount = 5

    static func current(_ all: [TodoDecision]) -> [TodoDecision] {
        all.filter(\.isCurrent)
    }

    /// What `display` shows of `list` (the current decisions, or the full history), in its order (the web's
    /// `shownDecisions`).
    static func shown<T>(_ list: [T], _ display: TodoDecisionsDisplay) -> [T] {
        switch display {
        case .first: Array(list.prefix(firstCount))
        case .all: list
        case .folded: []
        }
    }

    /// The decisions `d` may be marked as revising: earlier ones (in the hub's order, oldest first) that are current,
    /// not the one it already revises.
    static func supersedeCandidates(for d: TodoDecision, in all: [TodoDecision]) -> [TodoDecision] {
        let at = all.firstIndex { $0.id == d.id } ?? 0
        return all[..<at].filter { $0.isCurrent && $0.id != d.supersedesID }
    }

    /// Where a decision came from, as the todo's page says it: the agent that asked, by its title when known.
    static func sourceLabel(_ d: TodoDecision, todo: TodoItem, lookup: TodosLogic.SessionLookup? = nil) -> String {
        switch d.source {
        case "outcome": return String(localized: "Recorded when the agent finished")
        case "agent": return String(localized: "Noted by an agent while it worked")
        case "thread": return String(localized: "Said in the Lark topic")
        case "card": break
        default: return String(localized: "Added by hand")
        }
        let agent = todo.agents.first { $0.machineID == d.sourceMachine && $0.sessionID == d.sourceSession }
        let title = [lookup?(d.sourceMachine, d.sourceSession)?.title, agent?.title]
            .compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }
            .first { !$0.isEmpty }
        guard let title else { return String(localized: "Answered in an agent's question") }
        // A session's title is often its first message: keep the label to a glance.
        let short = title.count > 24 ? String(title.prefix(24)) + "…" : title
        return String(localized: "Answered in a question from “\(short)”")
    }

    /// When it was recorded: the time alone today, with the date on other days.
    static func when(_ date: Date, now: Date = Date(), calendar: Calendar = .current) -> String {
        calendar.isDate(date, inSameDayAs: now)
            ? date.formatted(date: .omitted, time: .shortened)
            : date.formatted(.dateTime.month(.abbreviated).day().hour().minute())
    }

    static func listPath(todoID: String) -> String {
        "/api/v1/todos/\(TodosLogic.pathComponent(todoID))/decisions"
    }

    static func path(todoID: String, id: String) -> String {
        "\(listPath(todoID: todoID))/\(TodosLogic.pathComponent(id))"
    }
}

extension APIClient {
    struct TodoDecisionsResponse: Decodable, Sendable {
        var decisions: [TodoDecision]?
    }

    struct TodoDecisionResponse: Decodable, Sendable {
        var decision: TodoDecision
    }

    struct AddTodoDecisionRequest: Encodable, Sendable {
        var question: String
        var choice: String
        var reason: String?
        var supersedesID: String?

        enum CodingKeys: String, CodingKey {
            case question, choice, reason
            case supersedesID = "supersedes_id"
        }
    }

    struct TodoDecisionSupersedesRequest: Encodable, Sendable {
        var supersedesID: String

        enum CodingKeys: String, CodingKey {
            case supersedesID = "supersedes_id"
        }
    }

    /// Every decision of a todo, oldest first, the revised ones included (`all=1`).
    func todoDecisions(todoID: String) async throws -> [TodoDecision] {
        try await get(TodoDecisionsLogic.listPath(todoID: todoID), query: ["all": "1"], as: TodoDecisionsResponse.self).decisions ?? []
    }

    /// Records a decision; with `supersedesID`, one that revises that earlier decision. An empty reason is not sent.
    func addTodoDecision(todoID: String, question: String, choice: String, reason: String = "",
                         supersedesID: String = "") async throws -> TodoDecision {
        let reason = reason.trimmingCharacters(in: .whitespacesAndNewlines)
        let body = AddTodoDecisionRequest(question: question.trimmingCharacters(in: .whitespacesAndNewlines),
                                          choice: choice.trimmingCharacters(in: .whitespacesAndNewlines),
                                          reason: reason.isEmpty ? nil : reason,
                                          supersedesID: supersedesID.isEmpty ? nil : supersedesID)
        return try await post(TodoDecisionsLogic.listPath(todoID: todoID), body: body, as: TodoDecisionResponse.self).decision
    }

    /// Marks decision `id` as revising the earlier `supersedesID`; "" undoes it.
    func setTodoDecisionSupersedes(todoID: String, id: String, supersedesID: String) async throws -> TodoDecision {
        let data = try await request("PATCH", TodoDecisionsLogic.path(todoID: todoID, id: id),
                                     body: TodoDecisionSupersedesRequest(supersedesID: supersedesID))
        return try JSON.decoder.decode(TodoDecisionResponse.self, from: data).decision
    }
}
