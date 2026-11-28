import Foundation
import PersonaWire
import Testing
@testable import Persona

/// Settings › Lark Bot › Group Access (#16848): the same hub route and rules as the web's section.
@MainActor struct FeishuGroupAccessModelTests {
    private static let path = "/api/v1/bots/feishu/group-access"

    /// An allowlist with one person, one group, one recent request and one tool. Names are made up.
    private static let view = """
    {"revision":3,"paired":true,
     "policy":{"mode":"allowlist",
               "allowed_users":[{"open_id":"ou_example1","name":"Teammate A"}],
               "allowed_chats":[{"chat_id":"oc_example1","name":"Project group"}],
               "blocked_users":[],"blocked_chats":[],
               "guest_tools":["wiki_search"],"per_chat_hourly":20},
     "available_tools":[{"name":"wiki_search","connector":"Wiki"},{"name":"docs_read","connector":"Docs"}],
     "recent_requests":[{"open_id":"ou_example2","name":"Teammate B","chat_id":"oc_example1","chat_name":"Project group","at":1795000000}],
     "limits":{"list_max":200,"guest_tools_max":8,"per_chat_hourly_min":1,"per_chat_hourly_max":120,"per_chat_hourly_default":20}}
    """

    private func model(_ hub: FakeHub) -> FeishuGroupAccessModel {
        FeishuGroupAccessModel(deps: .init(api: { hub.client }))
    }

    private func body(_ req: LocalRequest) throws -> [String: Any] {
        try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:]
    }

    @Test func decodesTheView() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        let m = model(hub)
        await m.load()
        let a = try #require(m.access)
        #expect(m.available && a.revision == 3 && a.policy.mode == .allowlist)
        #expect(a.policy.allowedUsers == [FeishuGroupUser(openID: "ou_example1", name: "Teammate A")])
        #expect(a.policy.allowedChats == [FeishuGroupChat(chatID: "oc_example1", name: "Project group")])
        #expect(a.policy.guestTools == ["wiki_search"] && a.policy.perChatHourly == 20)
        #expect(a.availableTools.map(\.name) == ["wiki_search", "docs_read"])
        #expect(a.recentRequests.first?.openID == "ou_example2")
        #expect(FeishuGroupAccessText.toolLabel(a.availableTools[0]) == "Wiki · wiki_search")
        #expect(FeishuGroupAccessText.modeDetail(.ownerOnly).isEmpty == false)
        #expect(m.loadError == nil && m.error == nil)
    }

    /// A missing list or bounds is empty or the default, and an unknown mode is owner only.
    @Test func aSparseAnswerStillDecodes() throws {
        let json = """
        {"revision":1,"paired":true,"policy":{"mode":"something_new","allowed_users":[{"open_id":"ou_x"}]}}
        """
        let a = try JSON.decoder.decode(FeishuGroupAccess.self, from: Data(json.utf8))
        #expect(a.policy.mode == FeishuGroupAccessMode.ownerOnly)
        #expect(a.policy.allowedUsers == [FeishuGroupUser(openID: "ou_x")] && a.policy.allowedChats.isEmpty)
        #expect(a.policy.guestTools.isEmpty && a.policy.perChatHourly == 20)
        #expect(a.limits == FeishuGroupAccessLimits.fallback && a.availableTools.isEmpty && a.recentRequests.isEmpty)
        #expect(FeishuGroupAccessText.validID("  ou_x ") == "ou_x")
        #expect(FeishuGroupAccessText.validID("ou x") == nil && FeishuGroupAccessText.validID("") == nil)
        #expect(FeishuGroupAccessText.hourlyChoices(20) == [5, 10, 20, 40, 60, 120])
        #expect(FeishuGroupAccessText.hourlyChoices(7) == [5, 7, 10, 20, 40, 60, 120])
    }

    /// An older hub (404): nothing is drawn, and nothing is wrong. Anything else is a real failure.
    @Test func anOlderHubHidesTheSection() async {
        let older = FakeHub()
        let m = model(older)
        await m.load()
        #expect(m.access == nil && m.loadError == nil && !m.available)

        let failing = FakeHub()
        failing.on("GET", Self.path) { _ in throw APIError(status: 500, message: "boom") }
        let f = model(failing)
        await f.load()
        #expect(f.access == nil && f.loadError?.contains("boom") == true)
    }

    /// Each edit writes the whole policy at the revision on screen, and the page shows what the hub answered.
    @Test func eachEditSendsTheWholePolicyAndTheRevision() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        var sent: [[String: Any]] = []
        hub.on("PUT", Self.path) { req in
            let b = try self.body(req)
            sent.append(b)
            var answer = try JSONSerialization.jsonObject(with: Data(Self.view.utf8)) as? [String: Any] ?? [:]
            answer["revision"] = (b["revision"] as? Int ?? 0) + 1
            answer["policy"] = b["policy"]
            return try JSONSerialization.data(withJSONObject: answer)
        }
        let m = model(hub)
        await m.load()

        await m.setMode(.everyoneInAllowedChats)
        await m.setPerChatHourly(40)
        await m.setTool("docs_read", on: true)
        await m.setTool("wiki_search", on: false)
        m.adding = .allowedPerson
        #expect(await m.add(id: "  ou_new ", name: " Teammate C "))
        #expect(m.adding == nil)
        await m.removeAllowedGroup("oc_example1")
        await m.allow(try #require(m.access?.recentRequests.first))
        await m.block(try #require(m.access?.recentRequests.first))

        #expect(sent.count == 8)
        #expect(sent.map { $0["revision"] as? Int } == Array(3..<11))
        func policy(_ i: Int) -> [String: Any] { sent[i]["policy"] as? [String: Any] ?? [:] }
        #expect(policy(0)["mode"] as? String == "everyone_in_allowed_chats")
        #expect(policy(0)["allowed_users"] != nil && policy(0)["guest_tools"] != nil, "the whole policy goes, not the change")
        #expect(policy(1)["per_chat_hourly"] as? Int == 40)
        #expect(policy(2)["guest_tools"] as? [String] == ["wiki_search", "docs_read"])
        #expect(policy(3)["guest_tools"] as? [String] == ["docs_read"])
        let added = (policy(4)["allowed_users"] as? [[String: Any]])?.last
        #expect(added?["open_id"] as? String == "ou_new" && added?["name"] as? String == "Teammate C")
        #expect((policy(5)["allowed_chats"] as? [[String: Any]])?.isEmpty == true)
        #expect((policy(6)["allowed_users"] as? [[String: Any]])?.contains { $0["open_id"] as? String == "ou_example2" } == true)
        #expect((policy(6)["allowed_chats"] as? [[String: Any]])?.contains { $0["chat_id"] as? String == "oc_example1" } == true)
        #expect((policy(7)["blocked_users"] as? [[String: Any]])?.contains { $0["open_id"] as? String == "ou_example2" } == true)
        #expect(m.access?.revision == 11 && m.error == nil && !m.saving)
    }

    /// An id the hub would refuse is not sent, and the sheet stays open.
    @Test func aBadIDIsNotSent() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        let m = model(hub)
        await m.load()
        m.adding = .blockedGroup
        #expect(await m.add(id: "oc with space", name: "") == false)
        #expect(m.adding == .blockedGroup)
        #expect(hub.sent("PUT", Self.path).isEmpty)
    }

    /// A stale revision: the hub answers 409, the page reloads once and says so, and keeps what the hub now has.
    @Test func aConflictReloadsOnce() async throws {
        let hub = FakeHub()
        var reads = 0
        hub.on("GET", Self.path) { _ in
            reads += 1
            return Data((reads == 1 ? Self.view : Self.view.replacingOccurrences(of: "\"revision\":3", with: "\"revision\":9")).utf8)
        }
        hub.on("PUT", Self.path) { _ in throw APIError(status: 409, message: "changed", fields: ["code": "group_access_conflict"]) }
        let m = model(hub)
        await m.load()
        await m.setMode(.ownerOnly)
        #expect(m.access?.revision == 9 && m.access?.policy.mode == .allowlist, "the page shows what the hub has, not the change")
        #expect(reads == 2)
        #expect(m.error?.isEmpty == false)
    }

    /// Any other failure leaves the page exactly as it was.
    @Test func aFailedSaveLeavesThePageAsItWas() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        hub.on("PUT", Self.path) { _ in throw APIError(status: 400, message: "not a read-only tool") }
        let m = model(hub)
        await m.load()
        let before = m.access
        await m.setTool("docs_read", on: true)
        #expect(m.access == before)
        #expect(m.error?.contains("not a read-only tool") == true && !m.saving)
    }

    /// A tool the hub no longer offers is dropped from the policy that is written, so one stale name cannot block a save.
    @Test func aRemovedToolIsDroppedOnSave() async throws {
        let hub = FakeHub()
        let stale = Self.view.replacingOccurrences(of: "\"guest_tools\":[\"wiki_search\"]", with: "\"guest_tools\":[\"wiki_search\",\"gone_tool\"]")
        hub.on("GET", Self.path, json: stale)
        var sent = ""
        hub.on("PUT", Self.path) { req in
            sent = String(decoding: req.body ?? Data(), as: UTF8.self)
            return Data(stale.utf8)
        }
        let m = model(hub)
        await m.load()
        await m.setPerChatHourly(10)
        #expect(sent.contains("gone_tool") == false)
        #expect(sent.contains("wiki_search"))
    }
}
