import Foundation
import PersonaWire

/// A To-dos route the hub (or the manager behind it) does not have yet (#10226; docs/protocol/protocol.md, the to-dos
/// rows of the REST table): an older hub answers 405 for `GET /api/v1/todos/{id}` and `GET /api/v1/todos/places`, since
/// `PATCH` and `DELETE /api/v1/todos/{id}` match their path, and 404 for the others. A 404 that says
/// `todo_not_found` is the to-do itself gone (or another manager's), not the route: `gone`.
nonisolated enum TodoNotThere {
    static let goneCode = "todo_not_found"

    static func matches(_ error: Error) -> Bool {
        guard let e = error as? APIError else { return false }
        return e.status == 405 || (e.status == 404 && e.code != goneCode)
    }

    static func gone(_ error: Error) -> Bool {
        guard let e = error as? APIError else { return false }
        return e.status == 404 && e.code == goneCode
    }

    /// The hub's own 404 for an assistant it does not know: about the request's assistant, not the to-do.
    static let noManager = "no_manager"

    /// A 404 from a write on a to-do's own route (`PATCH` or `DELETE …/todos/{id}`, `…/someday`): the to-do is gone,
    /// deleted here or on another device. The manager answers those routes `not_found`, the item route `todo_not_found`;
    /// only the hub's `no_manager` is about something else.
    static func deleted(_ error: Error) -> Bool {
        guard let e = error as? APIError else { return false }
        return e.status == 404 && e.code != noManager
    }
}

/// What the asks route's refusals say (`POST /api/v1/todos/{id}/asks/{ask}/answer`, docs/protocol/protocol.md, its
/// row in the REST table): a hub whose manager reconnected at an older version answers 404 or 405 with neither of the
/// route's own codes, which means the route is not there, so the answer goes through today's routes instead.
nonisolated enum TodoAskRefusal {
    static let stale = "ask_stale"
    static let notFound = "ask_not_found"

    /// The route is not there: 405, or a 404 that names neither the to-do nor the ask.
    static func routeMissing(_ error: Error) -> Bool {
        guard let e = error as? APIError else { return false }
        return e.status == 405 || (e.status == 404 && e.code != TodoNotThere.goneCode && e.code != notFound)
    }

    /// The ask no longer waits: answered anywhere, a newer round, started again (409 `ask_stale`), or it is gone with its
    /// to-do (404 `todo_not_found`, `ask_not_found`). The first answer anywhere wins; this one is let go quietly.
    static func answeredElsewhere(_ error: Error) -> Bool {
        guard let e = error as? APIError else { return false }
        return (e.status == 409 && e.code == stale) || (e.status == 404 && (e.code == TodoNotThere.goneCode || e.code == notFound))
    }
}

/// One to-do as `GET /api/v1/todos/{id}` answers it: the row and its view, worked out over the whole list.
nonisolated struct ViewedTodo: Equatable, Sendable {
    var todo: TodoItem
    var view: TodoSummary?
}

/// The To-dos redesign's routes (#10465), beside the list's (`APIClient.todos(etag:)`) and today's writes
/// (`TodoModels.swift`). Ids go in the path with every `/`, `?` and `#` escaped: a run's or a step's ask id carries a
/// session's ids (`run:<machine>/<session>…`), and `APIClient.encodedPath` takes a path that has escapes as it is.
nonisolated extension APIClient {
    /// How long a message to a to-do may take: the hub relays it for up to five minutes (its forward's deadline) while
    /// the node takes it or a new Agent starts for it, so the call waits a little longer than that.
    static let todoRelayWait: TimeInterval = 5 * 60 + 20

    /// `id` escaped for one segment of a path.
    static func todoPath(_ id: String) -> String {
        id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/?#"))) ?? id
    }

    private struct TodoAnswer: Decodable, Sendable { var todo: TodoItem }

    /// `GET /api/v1/todos/{id}`: the row as the list has it, its view included. The answer names no manager: the caller
    /// keeps the list's stamp (it asked through the to-do's manager). `TodoNotThere` when the hub has no such route.
    func todo(id: String) async throws -> ViewedTodo {
        struct Answer: Decodable, Sendable {
            var todo: TodoItem
            var view: TodoSummary?
            enum Outer: String, CodingKey { case todo }
            enum Inner: String, CodingKey { case view }
            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: Outer.self)
                todo = try c.decode(TodoItem.self, forKey: .todo)
                view = (try? c.nestedContainer(keyedBy: Inner.self, forKey: .todo).decodeIfPresent(TodoSummary.self, forKey: .view)) ?? nil
            }
        }
        let answer = try await getDecodedOffMain("/api/v1/todos/\(Self.todoPath(id))", as: Answer.self)
        return ViewedTodo(todo: answer.todo, view: answer.view)
    }

    /// `GET /api/v1/todos/places`: All To-dos, Today, Upcoming and each project with open to-dos, and the list's
    /// counts. Asked only while the hub lists `todo_places`; `TodoNotThere` from a hub without it.
    func todoPlaces() async throws -> TodoPlaces {
        try await getDecodedOffMain("/api/v1/todos/places", as: TodoPlaces.self)
    }

    /// Someday (`PUT …/someday`): back on `until`, or with none on no set day. The hub answers the to-do; its `view`
    /// comes with the next read of the list. A closed to-do is refused (409 `todo_closed`). `opID`: the list's held
    /// write's (`TodoOutbox`), the same on every send of it.
    func setSomeday(id: String, until: Date?, opID: UUID? = nil) async throws -> TodoItem {
        struct Body: Encodable { var until: Date? }
        let data = try await request("PUT", "/api/v1/todos/\(Self.todoPath(id))/someday", body: Body(until: until), opID: opID)
        return try JSON.decoder.decode(TodoAnswer.self, from: data).todo
    }

    /// Back from Someday (`DELETE …/someday`).
    func clearSomeday(id: String, opID: UUID? = nil) async throws -> TodoItem {
        let data = try await request("DELETE", "/api/v1/todos/\(Self.todoPath(id))/someday", opID: opID)
        return try JSON.decoder.decode(TodoAnswer.self, from: data).todo
    }

    // The list's held writes (`TodoOutbox`), each with its op id. A tick, Someday or a delete sent again is the same
    // write again. A new to-do is not: the hub keeps no receipt for one without images, so the outbox never sends one
    // again on its own once it may have reached the hub.

    /// The tick (`PATCH …/todos/{id}` with `done`), which also stops the follow-ups of its Agents, or a reopen.
    /// Closing a to-do with active Agent work may block up to 45 s on the hub's confirmation card (`todoCloseConfirmWait`).
    func setTodoDone(id: String, done: Bool, opID: UUID) async throws -> TodoItem {
        struct Body: Encodable { var done: Bool }
        let timeout: TimeInterval = done ? Self.todoCloseConfirmWait : 30
        let data = try await request("PATCH", "/api/v1/todos/\(Self.todoPath(id))", body: Body(done: done), timeout: timeout, opID: opID)
        return try JSON.decoder.decode(TodoAnswer.self, from: data).todo
    }

    /// `DELETE …/todos/{id}`.
    func deleteTodo(id: String, opID: UUID) async throws {
        _ = try await request("DELETE", "/api/v1/todos/\(Self.todoPath(id))", opID: opID)
    }

    /// `POST /api/v1/todos`: a new to-do with its title and notes, named by the hub. Made in a To-dos place, its due day
    /// or moment and its project go in the same write (#10465), so a retry never leaves it outside the place.
    func createTodo(title: String, notes: String?, dueAt: Date? = nil, dueDate: String? = nil, project: String? = nil,
                    opID: UUID) async throws -> TodoItem {
        struct Body: Encodable {
            var title: String
            var notes: String?
            var dueAt: Date?
            var dueDate: String?
            var project: String?
            enum CodingKeys: String, CodingKey { case title, notes, project, dueAt = "due_at", dueDate = "due_date" }
        }
        let body = Body(title: title.trimmingCharacters(in: .whitespacesAndNewlines),
                        notes: notes?.trimmingCharacters(in: .whitespacesAndNewlines), dueAt: dueAt, dueDate: dueDate, project: project)
        let data = try await request("POST", "/api/v1/todos", body: body, opID: opID)
        return try JSON.decoder.decode(TodoAnswer.self, from: data).todo
    }

    /// An answer to one of a to-do's own asks (`POST …/todos/{id}/asks/{ask}/answer`, while the hub lists `todo_asks`),
    /// by the id its view lists, escaped as one path segment (a step's ask carries a `/`). `opID` rides as
    /// `X-Persona-Op-Id`: a repeat after an unknown outcome gets the first one's answer. The answer carries the row as it
    /// reads after it, which says whether the ask still waits.
    func answerAsk(todo id: String, ask: String, _ answer: TodoAskAnswer, opID: UUID) async throws -> TodoAskAnswered {
        let data = try await request("POST", "/api/v1/todos/\(Self.todoPath(id))/asks/\(Self.todoPath(ask))/answer", body: answer,
                                     timeout: Self.todoRelayWait, opID: opID)
        return try JSON.decoder.decode(TodoAskAnswered.self, from: data)
    }

    /// Restore on an inactive to-do (`PUT …/restored`): activity for its status, never an edit.
    func restore(id: String) async throws -> TodoItem {
        let data = try await request("PUT", "/api/v1/todos/\(Self.todoPath(id))/restored")
        return try JSON.decoder.decode(TodoAnswer.self, from: data).todo
    }

    /// Tells a to-do's work something through its origin conversation (`POST …/conversations/{origin}/route`); with
    /// `askedAt`, as the answer to the decision of that round. `opID` rides as `X-Persona-Op-Id`, so a send repeated
    /// after an unknown outcome gets the first one's answer; the answer is the conversation's new `routed` item.
    func route(conversation: String, todoID: String, text: String, askedAt: String?, opID: UUID) async throws -> FeedItem {
        struct Body: Encodable {
            var todoID: String
            var text: String
            var askedAt: String?
            enum CodingKeys: String, CodingKey { case text, todoID = "todo_id", askedAt = "asked_at" }
        }
        struct Answer: Decodable { var item: FeedItem }
        let body = Body(todoID: todoID, text: HubNaming.outbound(text, canonical: personaNaming), askedAt: askedAt)
        let data = try await request("POST", "/api/v1/assistant/conversations/\(Self.todoPath(conversation))/route", body: body,
                                     timeout: Self.todoRelayWait, opID: opID)
        return try JSON.decoder.decode(Answer.self, from: data).item
    }
}
