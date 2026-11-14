import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A todo's decision log (#1618): the hub's JSON, which decisions are in effect and which an entry may be marked as
/// revising, where each came from, and the Decisions section's model against a FakeHub.
@Suite(.serialized)
@MainActor
struct TodoDecisionsTests {
    /// As the hub sends them (`server/internal/db/todo_decisions.go`), oldest first: d3 revises d1.
    private static let list = #"""
        {"decisions":[
         {"id":"d1","todo_id":"t1","question":"Which chart library?","choice":"Charts","reason":"","source":"manual",
          "source_machine":"","source_session":"","source_message":"","supersedes_id":"","superseded_by":"d3",
          "created_at":"2026-11-06T08:19:00.123456789Z"},
         {"id":"d2","todo_id":"t1","question":"Units?","choice":"Celsius","reason":"Most readers use it","source":"card",
          "source_machine":"m1","source_session":"s1","source_message":"i1","supersedes_id":"","superseded_by":"",
          "created_at":"2026-11-06T09:00:00Z"},
         {"id":"d3","todo_id":"t1","question":"Which chart library?","choice":"Swift Charts","reason":"Native","source":"outcome",
          "source_machine":"m1","source_session":"s1","source_message":"","supersedes_id":"d1","superseded_by":"",
          "created_at":"2026-11-06T10:00:00Z"}]}
        """#

    private static let path = "/api/v1/todos/t1/decisions"

    private final class Sink {
        var errors: [String] = []
        var toasts: [Toast] = []
    }

    private func make(_ hub: FakeHub, _ sink: Sink = Sink()) -> TodoDecisionsModel {
        let model = TodoDecisionsModel(deps: .init(api: { hub.client }, report: { sink.errors.append($0.localizedDescription) },
                                                   toast: { sink.toasts.append($0) }))
        model.select("t1")
        return model
    }

    private func hub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.list)
        return hub
    }

    private func decisions() async throws -> [TodoDecision] {
        try await hub().client.todoDecisions(todoID: "t1")
    }

    private func body(_ req: LocalRequest?) throws -> [String: String] {
        let data = try #require(req?.body)
        return try #require(try JSONSerialization.jsonObject(with: data) as? [String: String])
    }

    private func session(machine: String, id: String, title: String) -> Session {
        let json = """
        {"machine_id":"\(machine)","session_id":"\(id)","title":"\(title)","cwd":"/tmp","status":"idle","state":"idle",
         "updated_at":"2026-11-06T09:00:00Z"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private func decision(source: String, machine: String = "", session: String = "") throws -> TodoDecision {
        let json = #"{"id":"x","question":"q","choice":"c","source":"\#(source)","source_machine":"\#(machine)","source_session":"\#(session)"}"#
        return try JSON.decoder.decode(TodoDecision.self, from: Data(json.utf8))
    }

    // MARK: Decoding

    @Test func decodesTheHubsListWithTheFullHistory() async throws {
        let hub = hub()
        let list = try await hub.client.todoDecisions(todoID: "t1")
        #expect(hub.sent("GET", Self.path).first?.query == ["all": "1"], "replaced ones too, for the history")
        #expect(list.map(\.id) == ["d1", "d2", "d3"])
        let d1 = list[0], d2 = list[1], d3 = list[2]
        #expect(!d1.isCurrent && d1.supersededBy == "d3")
        #expect(d2.isCurrent && d2.source == "card" && d2.sourceMachine == "m1" && d2.sourceSession == "s1" && d2.sourceMessage == "i1")
        #expect(d2.reason == "Most readers use it" && d2.todoID == "t1")
        #expect(d3.isCurrent && d3.supersedesID == "d1" && d3.choice == "Swift Charts")
        let at = try #require(d1.createdAt)
        #expect(abs(at.timeIntervalSince1970 - 1_793_953_140.123) < 0.001, "nanoseconds read as far as a Date keeps them")
    }

    @Test func missingFieldsReadAsEmptyAndANullListAsNone() async throws {
        let d = try JSON.decoder.decode(TodoDecision.self, from: Data(#"{"id":"d9","question":"q","choice":"c"}"#.utf8))
        #expect(d.reason.isEmpty && d.source.isEmpty && d.supersedesID.isEmpty && d.isCurrent && d.createdAt == nil)

        let hub = FakeHub()
        hub.on("GET", Self.path, json: #"{"decisions":null}"#)
        #expect(try await hub.client.todoDecisions(todoID: "t1").isEmpty)
    }

    // MARK: Logic

    @Test func inEffectAndWhatEachMayRevise() async throws {
        let all = try await decisions()
        #expect(TodoDecisionsLogic.current(all).map(\.id) == ["d2", "d3"])
        // Earlier and in effect, not the one it already revises.
        #expect(TodoDecisionsLogic.supersedeCandidates(for: all[2], in: all).map(\.id) == ["d2"])
        #expect(TodoDecisionsLogic.supersedeCandidates(for: all[1], in: all).isEmpty, "d1 is already revised")
        #expect(TodoDecisionsLogic.supersedeCandidates(for: all[0], in: all).isEmpty, "nothing comes before the first")
    }

    @Test func whereEachCameFrom() throws {
        let long = "Chart the weather for the next ten days"
        let todo = TodoItem(id: "t1", title: "Weather", agents: [TodoItem.Agent(machineID: "m2", sessionID: "s2", title: "Blog post")])
        let lookup = TodosLogic.makeSessionLookup([session(machine: "m1", id: "s1", title: long)])
        let label = { (d: TodoDecision) in TodoDecisionsLogic.sourceLabel(d, todo: todo, lookup: lookup) }

        let asked = label(try decision(source: "card", machine: "m1", session: "s1"))
        #expect(asked.contains(String(long.prefix(24)) + "…") && !asked.contains(long), "a long title is cut to a glance")
        #expect(label(try decision(source: "card", machine: "m2", session: "s2")).contains("Blog post"), "the todo's agent, when the session is gone")
        let unknown = label(try decision(source: "card", machine: "m3", session: "s3"))
        #expect(unknown == label(try decision(source: "card")) && !unknown.contains("Blog post"))

        let kinds = try ["manual", "outcome", "agent", "thread", "card"].map { label(try decision(source: $0)) }
        #expect(Set(kinds).count == kinds.count, "each source says where it came from")
        #expect(label(try decision(source: "something-new")) == kinds[0], "a source this build does not know reads as by hand")
    }

    @Test func pathsEncodeTheIDs() {
        #expect(TodoDecisionsLogic.listPath(todoID: "t1") == Self.path)
        #expect(TodoDecisionsLogic.path(todoID: "a/b c", id: "d 1") == "/api/v1/todos/a%2Fb%20c/decisions/d%201")
    }

    @Test func todayShowsTheTimeAndAnotherDayTheDateToo() throws {
        let now = Date()
        let earlier = try #require(Calendar.current.date(byAdding: .day, value: -3, to: now))
        #expect(TodoDecisionsLogic.when(now, now: now) == now.formatted(date: .omitted, time: .shortened))
        #expect(TodoDecisionsLogic.when(earlier, now: now) != earlier.formatted(date: .omitted, time: .shortened))
    }

    // MARK: Model

    @Test func loadsInEffectFirstAndTheHistoryOnRequest() async {
        let model = make(hub())
        #expect(model.all == nil, "loading")
        await model.load()
        #expect(model.failed == nil && !model.unsupported)
        #expect(model.current.map(\.id) == ["d2", "d3"] && model.revisedCount == 1)
        #expect(model.shown.map(\.id) == ["d2", "d3"])
        model.showAll = true
        #expect(model.shown.map(\.id) == ["d1", "d2", "d3"])
        #expect(model.decision("d1")?.choice == "Charts")
    }

    @Test func aFailedLoadSaysWhyUntilARetryWorks() async {
        let hub = FakeHub()
        hub.on("GET", Self.path) { _ in throw APIError(status: 500, message: "hub down") }
        let model = make(hub)
        await model.load()
        #expect(model.all == nil && !model.unsupported)
        #expect(model.failed?.contains("hub down") == true)

        hub.on("GET", Self.path, json: Self.list)
        await model.load()
        #expect(model.failed == nil && model.all?.count == 3)
    }

    @Test func aHubWithoutDecisionsHidesTheSection() async {
        let model = make(FakeHub())
        await model.load()
        #expect(model.unsupported && model.failed == nil)
    }

    @Test func switchingTodosForgetsTheLastList() async {
        let model = make(hub())
        await model.load()
        model.showAll = true
        model.form = TodoDecisionForm()
        model.select("t1")
        #expect(model.all?.count == 3 && model.showAll, "the same todo keeps what it has")
        model.select("t2")
        #expect(model.all == nil && !model.showAll && model.form == nil)
    }

    @Test func recordingSendsTrimmedTextAndReloads() async throws {
        let hub = hub()
        hub.on("POST", Self.path) { _ in Data(#"{"decision":{"id":"d4","question":"Q","choice":"C"}}"#.utf8) }
        let sink = Sink()
        let model = make(hub, sink)
        let saved = await model.add(question: "  Q ", choice: " C\n", reason: "  ", supersedes: nil)
        #expect(saved)
        #expect(try body(hub.sent("POST", Self.path).first) == ["question": "Q", "choice": "C"], "no empty reason or supersedes_id")
        #expect(hub.sent("GET", Self.path).count == 1, "the list is read again")
        #expect(sink.toasts.map(\.style) == [.success] && sink.errors.isEmpty)
    }

    @Test func revisingSendsWhatItReplaces() async throws {
        let hub = hub()
        hub.on("POST", Self.path) { _ in Data(#"{"decision":{"id":"d4","question":"Units?","choice":"Kelvin"}}"#.utf8) }
        let model = make(hub)
        await model.load()
        let d2 = try #require(model.decision("d2"))
        #expect(await model.add(question: "Units?", choice: "Kelvin", reason: "Science", supersedes: d2))
        #expect(try body(hub.sent("POST", Self.path).first) == ["question": "Units?", "choice": "Kelvin", "reason": "Science", "supersedes_id": "d2"])
    }

    @Test func aFailedSaveReportsAndKeepsTheForm() async {
        let hub = hub()
        hub.on("POST", Self.path) { _ in throw APIError(status: 400, message: "question and choice cannot be empty") }
        let sink = Sink()
        let model = make(hub, sink)
        #expect(await model.add(question: "Q", choice: "C", reason: "", supersedes: nil) == false)
        #expect(sink.errors.count == 1 && sink.toasts.isEmpty)
    }

    @Test func aBlankQuestionOrChoiceIsNotSent() async {
        let hub = hub()
        let model = make(hub)
        #expect(await model.add(question: " ", choice: "C", reason: "", supersedes: nil) == false)
        #expect(await model.add(question: "Q", choice: "\n", reason: "", supersedes: nil) == false)
        #expect(hub.sent("POST", Self.path).isEmpty)
    }

    @Test func markingAndUndoingARevisionPatchesIt() async throws {
        let hub = hub()
        let one = Self.path + "/d3"
        hub.on("PATCH", one) { _ in Data(#"{"decision":{"id":"d3","question":"q","choice":"c"}}"#.utf8) }
        let sink = Sink()
        let model = make(hub, sink)
        await model.load()
        let d3 = try #require(model.decision("d3"))
        await model.markSupersedes(d3, old: "d2")
        await model.markSupersedes(d3, old: "")
        let sent = hub.sent("PATCH", one)
        #expect(try sent.map(body) == [["supersedes_id": "d2"], ["supersedes_id": ""]], "\"\" undoes it and is always sent")
        #expect(hub.sent("GET", Self.path).count == 3, "read again after each")
        #expect(sink.toasts.count == 2 && Set(sink.toasts.map(\.message)).count == 2 && sink.errors.isEmpty)
    }

    @Test func aFailedRevisionReports() async throws {
        let hub = hub()
        hub.on("PATCH", Self.path + "/d3") { _ in throw APIError(status: 400, message: "can't supersede a later decision") }
        let sink = Sink()
        let model = make(hub, sink)
        await model.load()
        await model.markSupersedes(try #require(model.decision("d3")), old: "d2")
        #expect(sink.errors.count == 1 && sink.toasts.isEmpty)
    }

    // MARK: How many show (#6447)

    /// `n` decisions in effect, oldest first.
    private static func long(_ n: Int) -> String {
        let items = (1...n).map { i in
            #"{"id":"d\#(i)","todo_id":"t1","question":"Q\#(i)?","choice":"C\#(i)","source":"manual","supersedes_id":"","superseded_by":""}"#
        }
        return #"{"decisions":["# + items.joined(separator: ",") + "]}"
    }

    private func loaded(_ n: Int) async -> (FakeHub, TodoDecisionsModel) {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.long(n))
        let model = make(hub)
        await model.load()
        return (hub, model)
    }

    @Test func showsTheFirstFiveEveryOneOrNone() {
        let list = Array(1...12)
        #expect(TodoDecisionsLogic.firstCount == 5)
        #expect(TodoDecisionsLogic.shown(list, .first) == [1, 2, 3, 4, 5])
        #expect(TodoDecisionsLogic.shown(list, .all) == list)
        #expect(TodoDecisionsLogic.shown(list, .folded).isEmpty)
        #expect(TodoDecisionsLogic.shown([1, 2], .first) == [1, 2], "a short list shows whole")
    }

    @Test func opensAtTheFirstFiveWithShowAllUnderThem() async {
        let (_, model) = await loaded(12)
        #expect(model.display == .first && !model.folded)
        #expect(model.shown.map(\.id) == ["d1", "d2", "d3", "d4", "d5"])
        #expect(model.hasMore)
        model.toggleMore()
        #expect(model.shown.count == 12 && model.hasMore, "Show First 5 under them all")
        model.toggleMore()
        #expect(model.shown.count == 5)
    }

    @Test func foldsAwayAndShowsThemWholeWithTheCountKeptAndNoRead() async {
        let (hub, model) = await loaded(12)
        model.toggleFold()
        #expect(model.folded && model.shown.isEmpty && !model.hasMore)
        #expect(model.current.count == 12, "the header's count stays")
        model.toggleFold()
        #expect(!model.folded && model.display == .all && model.shown.count == 12)
        #expect(hub.sent("GET", Self.path).count == 1, "folding asks nothing of the hub")
    }

    @Test func aShortListHasNoShowAllAndAnEmptyOneNothingToFold() async {
        let (_, short) = await loaded(3)
        #expect(short.shown.count == 3 && !short.hasMore)
        short.toggleFold()
        #expect(short.folded && short.shown.isEmpty)

        let hub = FakeHub()
        hub.on("GET", Self.path, json: #"{"decisions":[]}"#)
        let empty = make(hub)
        await empty.load()
        empty.display = .folded
        #expect(!empty.folded && !empty.hasMore, "nothing in the list: nothing is folded")
    }

    @Test func anotherTodoOpensAtTheFirstFiveAgain() async {
        let (_, model) = await loaded(12)
        model.toggleFold()
        model.select("t1")
        #expect(model.folded, "the same to-do keeps how it is shown")
        model.select("t2")
        #expect(model.display == .first)
    }

    @Test func recordingOneShowsTheWholeList() async {
        let (hub, model) = await loaded(12)
        hub.on("POST", Self.path) { _ in Data(#"{"decision":{"id":"d13","question":"Q","choice":"C"}}"#.utf8) }
        #expect(await model.add(question: "Q", choice: "C", reason: "", supersedes: nil))
        #expect(model.display == .all, "the new one, last, is in view")
    }
}
