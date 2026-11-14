import Foundation
import Testing
@testable import Persona

/// Merge and Add to To-Dos (#6480): the due choices as the hub is asked, the requests and their answers against a hub in memory,
/// where a chain entry with a to-do link goes, what the to-do suggestion sends, and when a merge is offered.
@MainActor
struct InboxActionsTests {
    private static let zone = TimeZone(identifier: "Asia/Shanghai")!
    private static var calendar: Calendar { var c = Calendar(identifier: .gregorian); c.timeZone = zone; return c }
    /// 2026-11-14 09:30 in Shanghai.
    private static let now = calendar.date(from: DateComponents(year: 2026, month: 11, day: 14, hour: 9, minute: 30))!

    private static func item(_ id: String, manager: String? = nil, done: Bool = false) throws -> InboxItem {
        var it = try JSON.decoder.decode(InboxItem.self, from: Data(#"{"id":"\#(id)","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"t \#(id)","preview":"p","count":1,"version":"v-\#(id)"}"#.utf8))
        it.manager = manager
        if done { it.doneAt = .now }
        return it
    }

    private func body(_ r: LocalRequest) throws -> [String: JSONValue] {
        guard case .object(let o) = try JSON.decoder.decode(JSONValue.self, from: try #require(r.body)) else { return [:] }
        return o
    }

    // MARK: Due

    @Test func theDueChoicesAreWorkedOutInTheUsersCalendar() {
        let c = Self.calendar
        let hour = InboxDue.hour.fields(now: Self.now, calendar: c)
        #expect(hour.date == nil)
        #expect(try! Date(hour.at!, strategy: .iso8601) == Self.now.addingTimeInterval(3600))
        #expect(InboxDue.today.fields(now: Self.now, calendar: c) == (nil, "2026-11-14"))
        #expect(InboxDue.none.fields(now: Self.now, calendar: c) == (nil, nil))
        let picked = Self.now.addingTimeInterval(86_400 * 3)
        #expect(try! Date(InboxDue.at(picked).fields(now: Self.now, calendar: c).at!, strategy: .iso8601) == picked)
    }

    @Test func aSuggestionsDueGoesToTheCardsFieldsAndBack() {
        let c = Self.calendar
        let day = InboxDue.parts("2026-11-20", calendar: c)
        #expect(day.day == c.date(from: DateComponents(year: 2026, month: 11, day: 20)) && day.time == nil)
        #expect(InboxDue.parts(nil, calendar: c) == (nil, nil))
        let moment = c.date(from: DateComponents(year: 2026, month: 11, day: 20, hour: 15, minute: 0))!
        let parts = InboxDue.parts(InboxDue.moment(moment), calendar: c)
        #expect(parts.time == moment && parts.day == c.date(from: DateComponents(year: 2026, month: 11, day: 20)))
        // All Day on: the day alone; off: the day with the time as a moment; no day: none
        #expect(InboxDue.string(day: parts.day, allDay: true, time: moment, calendar: c) == "2026-11-20")
        #expect(InboxDue.string(day: parts.day, allDay: false, time: moment, calendar: c) == InboxDue.moment(moment))
        #expect(InboxDue.string(day: nil, allDay: true, time: moment, calendar: c) == nil)
    }

    // MARK: Requests

    @Test func mergingSendsTheRowsWithTheirVersionsAndReadsTheAnswer() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/inbox/merge", json: #"{"item":{"id":"a","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"t a","preview":"p","count":2,"merged_from":[{"id":"b","source":"mail"}]},"members":["b"],"undo":{"id":"a"}}"#)
        let reply = try await hub.client.mergeInbox([Self.item("a"), Self.item("b")])
        #expect(reply.members == ["b"] && reply.undoID == "a" && reply.item.mergedFrom?.map(\.id) == ["b"])
        guard case .array(let rows)? = try body(try #require(hub.sent("POST", "/api/v1/inbox/merge").first))["items"] else { Issue.record("no items"); return }
        #expect(rows.count == 2)
    }

    @Test func addingToTodosSendsTheDueAndReadsEachTodo() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/inbox/todo", json: #"{"changed":[],"todos":[{"item":{"id":"a"},"todo":{"id":"t1"}},{"item":{"id":"b"},"todo":{"id":"t2"}}],"undo":{"items":[]}}"#)
        let reply = try await hub.client.todoInbox([Self.item("a"), Self.item("b")], due: .none)
        #expect(reply.todos == [.init(itemID: "a", todoID: "t1"), .init(itemID: "b", todoID: "t2")])
        let sent = try body(try #require(hub.sent("POST", "/api/v1/inbox/todo").first))
        #expect(sent["due_at"] == nil && sent["due_date"] == nil)
        hub.on("POST", "/api/v1/inbox/todo/undo", json: #"{"changed":[]}"#)
        _ = try await hub.client.undoInboxTodo(reply.todos)
        guard case .array(let back)? = try body(try #require(hub.sent("POST", "/api/v1/inbox/todo/undo").first))["items"] else { Issue.record("no items"); return }
        #expect(back.count == 2)
    }

    // MARK: Offers and links

    @Test func mergeNeedsTwoOpenRowsOfOneManager() throws {
        let a = try Self.item("a"), b = try Self.item("b")
        #expect(InboxStore.canMerge([a, b]))
        #expect(!InboxStore.canMerge([a]))
        #expect(!InboxStore.canMerge([a, try Self.item("c", done: true)]))
        #expect(!InboxStore.canMerge([a, try Self.item("d", manager: "other")]))
    }

    @Test func aChainEntryWithATodoLinkOpensTheTodo() {
        let e = InboxItem.ChainEntry(id: "c", kind: "done", text: "已转成待办", link: .init(type: "todo", id: "t1"))
        #expect(InboxChainTarget.of(e) == .todo("t1") && InboxChainTarget.todo("t1").navigates)
        #expect(InboxChainTarget.of(.init(id: "c2", kind: "done", text: "x", link: .init(type: "todo"))) == .nothing)
    }

    @Test func aTodoSuggestionConfirmsWithItsTitleAndDue() async throws {
        let hub = FakeHub()
        let path = "/api/v1/inbox/lark:oc_demo1:100/suggestion"
        hub.on("POST", path, json: InboxEventsTests.answered)
        let client = hub.client
        let m = InboxSuggestionModel(itemID: "lark:oc_demo1:100", send: { id, body in try await client.inboxSuggestion(id, body) }, apply: { _ in })
        let s = InboxItem.Suggestion(version: 4, kind: "todo", text: "回复报价", due: "2026-11-20")
        _ = await m.confirm(s, text: "回复新报价", due: "2026-11-21")
        let sent = try body(try #require(hub.sent("POST", path).first))
        #expect(sent["text"] == .string("回复新报价") && sent["due"] == .string("2026-11-21") && sent["action"] == .string("confirm"))
        #expect(InboxEventText.actions(s) == [.confirm(String(localized: "Add to To-Dos")), .reject])
    }
}
