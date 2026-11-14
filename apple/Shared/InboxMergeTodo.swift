import Foundation

// Merge and Add to To-Dos on the Inbox (#6480; docs/assistant/inbox.md "Merge rows and Add to to-dos"): the due choices, the
// requests and their answers. The hub hides a merged row's members and files a row turned into a to-do as done (done_by todo).

/// When a to-do made from a row is due: an hour from now, today (all day), a picked moment, or never.
nonisolated enum InboxDue: Equatable, Sendable {
    case hour, today, at(Date), none

    /// The request's due fields in the user's calendar: a moment as RFC 3339 (`due_at`), a day as 2006-01-02 (`due_date`, all day).
    func fields(now: Date = .now, calendar: Calendar = .current) -> (at: String?, date: String?) {
        switch self {
        case .hour: (Self.moment(now.addingTimeInterval(3600)), nil)
        case .today: (nil, Self.day(now, calendar))
        case .at(let d): (Self.moment(d), nil)
        case .none: (nil, nil)
        }
    }

    static func moment(_ d: Date) -> String { d.formatted(.iso8601) }

    static func day(_ d: Date, _ calendar: Calendar) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    /// A to-do suggestion's due as the card's fields: the day and, for a moment, its time; nil day: no due.
    static func parts(_ due: String?, calendar: Calendar = .current) -> (day: Date?, time: Date?) {
        guard let due, !due.isEmpty else { return (nil, nil) }
        let f = DateFormatter()
        f.calendar = calendar
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = calendar.timeZone
        f.dateFormat = "yyyy-MM-dd"
        if due.count == 10, let d = f.date(from: due) { return (d, nil) }
        guard let m = try? Date(due, strategy: .iso8601) else { return (nil, nil) }
        return (calendar.startOfDay(for: m), m)
    }

    /// The due the card sends: a day alone (all day) is 2006-01-02, with a time a moment; no day: nil.
    static func string(day: Date?, allDay: Bool, time: Date, calendar: Calendar = .current) -> String? {
        guard let day else { return nil }
        if allDay { return Self.day(day, calendar) }
        var c = calendar.dateComponents([.year, .month, .day], from: day)
        let t = calendar.dateComponents([.hour, .minute], from: time)
        c.hour = t.hour; c.minute = t.minute
        return calendar.date(from: c).map(moment) ?? Self.day(day, calendar)
    }
}

private nonisolated struct InboxMergeBody: Encodable, Sendable {
    var items: [InboxBulkBody.Target]
}

private nonisolated struct InboxMergeUndoBody: Encodable, Sendable {
    var id: String
}

private nonisolated struct InboxTodoBody: Encodable, Sendable {
    struct Target: Encodable, Sendable { var id: String; var version: String? }
    var items: [Target]
    var dueAt: String?
    var dueDate: String?

    enum CodingKeys: String, CodingKey {
        case items
        case dueAt = "due_at", dueDate = "due_date"
    }
}

private nonisolated struct InboxTodoUndoBody: Encodable, Sendable {
    struct Target: Encodable, Sendable { var id: String; var todoID: String
        enum CodingKeys: String, CodingKey { case id, todoID = "todo_id" }
    }
    var items: [Target]
}

/// `POST /api/v1/inbox/merge`: the row that stands for the rest, the members it took in (hidden now) and the undo to send back.
nonisolated struct InboxMergeReply: Decodable, Sendable, Equatable {
    var item: InboxItem
    var members: [String]
    var undoID: String?

    private enum CodingKeys: String, CodingKey { case item, members, undo }
    private struct Undo: Decodable { var id: String }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        item = try c.decode(InboxItem.self, forKey: .item)
        members = try c.decodeIfPresent([String].self, forKey: .members) ?? []
        undoID = try c.decodeIfPresent(Undo.self, forKey: .undo)?.id
    }

    init(item: InboxItem, members: [String], undoID: String?) {
        self.item = item; self.members = members; self.undoID = undoID
    }
}

/// `POST /api/v1/inbox/todo`: the rows it filed (as the hub has them now), the to-do made of each, and the undo to send back.
nonisolated struct InboxTodoReply: Decodable, Sendable, Equatable {
    nonisolated struct Made: Decodable, Sendable, Equatable {
        var itemID: String
        var todoID: String
        private enum CodingKeys: String, CodingKey { case item, todo }
        private struct Ref: Decodable { var id: String }
        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            itemID = try c.decode(Ref.self, forKey: .item).id
            todoID = try c.decode(Ref.self, forKey: .todo).id
        }
        init(itemID: String, todoID: String) { self.itemID = itemID; self.todoID = todoID }
    }
    var changed: [InboxItem]
    var skipped: [InboxBulkResult.Skip]
    var todos: [Made]

    private enum CodingKeys: String, CodingKey { case changed, skipped, todos }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        changed = try c.decodeIfPresent([InboxItem].self, forKey: .changed) ?? []
        skipped = try c.decodeIfPresent([InboxBulkResult.Skip].self, forKey: .skipped) ?? []
        todos = try c.decodeIfPresent([Made].self, forKey: .todos) ?? []
    }

    init(changed: [InboxItem] = [], skipped: [InboxBulkResult.Skip] = [], todos: [Made] = []) {
        self.changed = changed; self.skipped = skipped; self.todos = todos
    }
}

private nonisolated struct InboxChanged: Decodable, Sendable { var changed: [InboxItem] }

extension APIClient {
    /// Merges the rows into one (the newest stands for the rest); each with the version it was listed at.
    func mergeInbox(_ items: [InboxItem]) async throws -> InboxMergeReply {
        try await post("/api/v1/inbox/merge", body: InboxMergeBody(items: items.map { .init(id: $0.id, version: $0.version) }), as: InboxMergeReply.self)
    }

    /// Undoes a merge: the members get their rows back (the rows it changed, as the hub has them now).
    func undoInboxMerge(_ id: String) async throws -> [InboxItem] {
        try await post("/api/v1/inbox/merge/undo", body: InboxMergeUndoBody(id: id), as: InboxChanged.self).changed
    }

    /// One to-do per row, all with the same due.
    func todoInbox(_ items: [InboxItem], due: InboxDue) async throws -> InboxTodoReply {
        let f = due.fields()
        return try await post("/api/v1/inbox/todo", body: InboxTodoBody(items: items.map { .init(id: $0.id, version: $0.version) }, dueAt: f.at, dueDate: f.date), as: InboxTodoReply.self)
    }

    /// Takes the to-dos out again and reopens the rows.
    func undoInboxTodo(_ made: [InboxTodoReply.Made]) async throws -> [InboxItem] {
        try await post("/api/v1/inbox/todo/undo", body: InboxTodoUndoBody(items: made.map { .init(id: $0.itemID, todoID: $0.todoID) }), as: InboxChanged.self).changed
    }
}
