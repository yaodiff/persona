import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Health (docs/assistant/health.md) against a FakeHub: the space's conversations come from `GET /api/v1/assistant?space=health`,
/// its memory from `?space=health`, a new one is started with `"space":"health"`; pushes go over the list, and the
/// Persona page's lists never hold a health conversation.
@MainActor
struct HealthModelTests {
    private static func session(_ id: String, space: String? = "health", archived: Bool = false, updated: TimeInterval,
                                title: String? = nil) -> Session {
        let spaceField = space.map { #","space":"\#($0)""# } ?? ""
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(title ?? id)","cwd":"/home/me/.persona/health","status":"idle",
         "state":"idle","assistant":true,"archived":\(archived)\(spaceField),
         "updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    private func deps(_ hub: FakeHub) -> AssistantDeps {
        AssistantDeps(api: { hub.client }, report: { _ in }, toast: { _ in }, open: { _ in })
    }

    @Test func loadsTheSpacesConversationsAndMemory() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant") { req in
            #expect(req.query["space"] == "health")
            return Data(#"""
                {"machine_id":"m","launcher":"ccc","cwd":"/home/me/.persona/health","conversations":[
                 {"session_id":"h1","title":"睡眠","status":"idle","state":"idle","assistant":true,"space":"health"}]}
                """#.utf8)
        }
        hub.on("GET", "/api/v1/assistant/memory") { req in
            #expect(req.query["space"] == "health")
            return Data(#"{"files":[{"name":"MEMORY.md","content":"x"},{"name":"allergy.md","content":"y"}]}"#.utf8)
        }
        let page = HealthModel(deps: deps(hub))
        await page.load(machine: "m")
        #expect(page.loaded)
        #expect(page.fetched.map(\.sessionID) == ["h1"])
        #expect(page.fetched.first?.machineID == "m") // the list's machine, when an item leaves it out
        #expect(page.fetched.first?.isHealth == true)
        #expect(page.memoryCount == 1) // MEMORY.md is the index, not a memory
    }

    @Test func aFailedListShowsWhy() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant") { _ in throw APIError(status: 500, message: "hub down") }
        let page = HealthModel(deps: deps(hub))
        await page.load(machine: "m")
        #expect(page.loaded)
        #expect(page.fetched.isEmpty)
        #expect(page.error != nil)
    }

    @Test func pushesGoOverTheListAndArchivedOnesLeave() {
        let fetched = [Self.session("a", updated: 1, title: "old"), Self.session("b", updated: 2)]
        let live = [Self.session("a", updated: 5, title: "new"), Self.session("b", archived: true, updated: 6),
                    Self.session("c", updated: 3), Self.session("main", space: nil, updated: 9)]
        let list = HealthModel.merge(fetched, live: live)
        #expect(list.map(\.sessionID) == ["a", "c"])
        #expect(list.first?.title == "new")
    }

    // Entering Health (#4329): back in the latest health conversation, as Codex does; the Health page when there is none.
    @Test func enteringHealthOpensTheLatestConversation() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant") { req in
            #expect(req.query["space"] == "health")
            return Data(#"""
                {"machine_id":"m","launcher":"ccc","cwd":"/home/me/.persona/health","conversations":[
                 {"session_id":"old","title":"步数","status":"idle","state":"idle","assistant":true,"space":"health","updated_at":"2026-11-01T08:00:00Z"},
                 {"session_id":"latest","title":"睡眠","status":"idle","state":"idle","assistant":true,"space":"health","updated_at":"2026-11-11T08:00:00Z"}]}
                """#.utf8)
        }
        let entry = await HealthModel.entry(deps: deps(hub), live: [])
        #expect(entry.opened == "latest")
    }

    @Test func aPushedConversationNewerThanTheListWinsAndArchivedOnesAreSkipped() {
        let fetched = [Self.session("a", updated: 5)]
        let live = [Self.session("b", updated: 9), Self.session("gone", archived: true, updated: 20), Self.session("main", space: nil, updated: 30)]
        #expect(HealthModel.entry(fetched, live: live).opened == "b")
    }

    @Test func enteringHealthWithNoHistoryOpensTheNewConversationPage() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant") { _ in Data(#"{"machine_id":"m","launcher":"ccc","cwd":"/home/me/.persona/health","conversations":[]}"#.utf8) }
        #expect(await HealthModel.entry(deps: deps(hub), live: []) == .new)
        #expect(HealthModel.entry([], live: [Self.session("gone", archived: true, updated: 3)]) == .new)
    }

    @Test func aListThatCantBeReadFallsBackToThePushedOnes() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant") { _ in throw APIError(status: 500, message: "hub down") }
        #expect((await HealthModel.entry(deps: deps(hub), live: [Self.session("p", updated: 1)])).opened == "p")
        #expect(await HealthModel.entry(deps: deps(hub), live: []) == .new)
    }

    @Test func newInAHealthConversationOpensTheHealthPage() {
        #expect(HealthModel.newOpensHealth(Self.session("h", updated: 1)))
        #expect(!HealthModel.newOpensHealth(Self.session("main", space: nil, updated: 1)))
        #expect(!HealthModel.newOpensHealth(nil))
    }

    @Test func theSidebarNeverListsAHealthConversation() {
        let all = [Self.session("h", updated: 5), Self.session("main", space: nil, updated: 1),
                   Self.session("old", space: nil, archived: true, updated: 2), Self.session("hOld", archived: true, updated: 3)]
        let lists = AssistantLists(all, group: "")
        #expect(lists.open.map(\.sessionID) == ["main"])
        #expect(lists.archived.map(\.sessionID) == ["old"])
        #expect(lists.health.map(\.sessionID) == ["h"])
    }

    @Test func aNewConversationOpensInTheSpace() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/conversations") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any]
            #expect(body?["space"] as? String == "health")
            #expect(body?["main"] == nil)
            return Data(#"{"session":{"machine_id":"m","session_id":"h9","status":"idle","state":"idle","assistant":true}}"#.utf8)
        }
        let s = try await hub.client.startConversation(prompt: "这周我睡得怎么样？", space: Session.healthSpace)
        #expect(s.isHealth) // marked even when the hub's answer leaves `space` out
    }

    @Test func theSpacesMemoryIsDeletedFromItself() async {
        let hub = FakeHub()
        var deleted = false
        hub.on("GET", "/api/v1/assistant/memory") { _ in
            Data((deleted ? #"{"files":[]}"# : #"{"files":[{"name":"a.md","content":"x"}]}"#).utf8)
        }
        hub.on("DELETE", "/api/v1/assistant/memory/a.md") { req in
            #expect(req.query["space"] == "health")
            deleted = true
            return Data("{}".utf8)
        }
        let page = AssistantMemoryModel(space: Session.healthSpace, deps: deps(hub))
        await page.load()
        await page.delete("a.md")
        #expect(deleted)
        #expect(page.files.isEmpty)
    }

    @Test func theAssistantsOwnMemorySendsNoSpace() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/memory") { req in
            #expect(req.query["space"] == nil)
            return Data(#"{"files":[]}"#.utf8)
        }
        let page = AssistantMemoryModel(deps: deps(hub))
        await page.load()
        #expect(page.loaded)
    }

    @Test func listsReportsAndDeletesByTheirEscapedName() async throws {
        let hub = FakeHub()
        var left = #"""
            {"reports":[
             {"name":"2026 体检-20261106-093000.pdf","title":"2026 体检","path":"/h/reports/2026 体检-20261106-093000.pdf",
              "record_path":"/h/reports/2026 体检-20261106-093000.md","size":12,"modified_at":"2026-11-06T09:30:00.123456789+08:00"},
             {"name":"血常规.md","title":"血常规","record_path":"/h/reports/血常规.md","size":3,"modified_at":"2026-11-05T09:30:00Z"}]}
            """#
        hub.on("GET", "/api/v1/machines/m/health/reports") { _ in Data(left.utf8) }
        let page = HealthReportsModel(machine: "m", deps: deps(hub))
        await page.load()
        #expect(page.reports.map(\.title) == ["2026 体检", "血常规"])
        #expect(page.reports[0].recordPath == "/h/reports/2026 体检-20261106-093000.md")
        #expect(page.reports[0].modifiedAt == JSON.parseDate("2026-11-06T09:30:00.123+08:00"))
        #expect(page.reports[1].path == nil)

        let escaped = "2026%20%E4%BD%93%E6%A3%80-20261106-093000.pdf"
        hub.on("DELETE", "/api/v1/machines/m/health/reports/\(escaped)") { _ in
            left = #"{"reports":[]}"#
            return Data("{}".utf8)
        }
        await page.delete(page.reports[0])
        #expect(page.reports.isEmpty)
    }

    @Test func aReportsErrorStaysOnThePage() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/m/health/reports") { _ in throw APIError(status: 409, message: "太旧") }
        let page = HealthReportsModel(machine: "m", deps: deps(hub))
        await page.load()
        #expect(page.loaded)
        #expect(page.error == "太旧")
    }
}

private extension HealthModel.Entry {
    /// The conversation it opens; nil for the new conversation page.
    var opened: String? { if case .open(let key) = self { key.sessionID } else { nil } }
}
