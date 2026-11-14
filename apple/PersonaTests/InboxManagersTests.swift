import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The inbox with several managers (#1850): two managers' items with one id are two items (`InboxItem.key`), a bulk
/// filing where one manager fails keeps the others' filed and puts back only the failed one's (the toast says how many
/// and why), and a request about one item names its manager unless that is the default. What the list shows of them
/// (the current assistant's alone) is in `AssistantScopeTests`. Made-up values.
@MainActor
struct InboxManagersTests {
    private func item(_ id: String, _ manager: String?, tier: String = "action", done: Bool = false) throws -> InboxItem {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        let filed = done ? #","done_at":"2026-11-04T08:00:00Z","done_by":"read""# : ""
        return try JSON.decoder.decode(InboxItem.self, from: Data(#"""
        {"id":"\#(id)","source":"mail","kind":"mail","account":"a1","account_name":"me@example.com","title":"\#(id)","preview":"",
         "count":1,"version":"v1","tier":"\#(tier)"\#(stamp)\#(filed)}
        """#.utf8))
    }

    /// What a manager answers: the items filed, with no `manager` (an answer is no merged list).
    private func filed(_ items: [InboxItem]) -> InboxBulkResult {
        InboxBulkResult(changed: items.map { var it = $0; it.manager = nil; it.doneAt = .now; it.doneBy = "user"; return it },
                        unchanged: [], skipped: [])
    }

    private static let roster = ManagerRoster(managers: [
        HubManager(id: "assistant", name: "助手", online: true, isDefault: true),
        HubManager(id: "m-1", name: "工作", online: true),
        HubManager(id: "m-2", name: "家里", online: false),
    ], defaultID: "assistant")

    /// A model on one made-up hub: no socket, its requests go to `hub`.
    private func model(_ hub: FakeHub, roster: ManagerRoster?) throws -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "inbox-managers-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(.demo)
        model.managerRosters[""] = roster
        return model
    }

    /// A request about one item (its details, Done, Mute, a reply) names the item's manager unless that is the default,
    /// as the web and Android do (docs/assistant/managers.md "Grouping"); once another is the default, the one before is
    /// named.
    @Test func anItemsRequestNamesNoneForTheDefault() throws {
        let model = try model(FakeHub(), roster: Self.roster)
        #expect(model.itemManager("assistant") == nil && model.itemManager(nil) == nil)
        #expect(model.itemManager("m-1") == "m-1" && model.itemManager("gone") == "gone")
        model.managerRosters[""] = ManagerRoster(managers: Self.roster.managers, defaultID: "m-1")
        #expect(model.itemManager("m-1") == nil && model.itemManager("assistant") == "assistant", "the default moved")
        model.managerRosters[""] = nil
        #expect(model.itemManager("m-1") == "m-1" && model.itemManager("assistant") == "assistant",
                "no roster yet: its stamp, as the default may have moved")
    }

    /// The same from an item's page: its hand-off reads the item first.
    @Test func anItemsPageNamesNoneForTheDefault() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/x") { _ in throw APIError(status: 500, message: "stop here") }
        let model = try model(hub, roster: Self.roster)
        for stamp in ["assistant", "m-1", nil] {
            _ = await InboxHandOffModel.live(try item("x", stamp), model: model).send(ask: "Reply for me")
        }
        #expect(hub.sent("GET", "/api/v1/inbox/x").map { $0.headers["X-Persona-Manager"] } == [nil, "m-1", nil])
    }

    @Test func oneIdUnderTwoManagersIsTwoItems() throws {
        let a = try item("x", "m-1"), b = try item("x", "m-2"), old = try item("x", nil)
        #expect(a.key != b.key && old.key == "x")
        #expect(InboxBulk.targets([a, b, a], done: true).map(\.key) == [a.key, b.key])
        let shown = InboxBulk.marking([a, b], keys: [a.key], done: true, now: .now)
        #expect(shown[0].isDone && !shown[1].isDone, "filing one manager's leaves the other's")

        // The hub answered for m-1 only; m-2 left its "x" (new messages since).
        var result = filed([a]).stamped("m-1")
        result.skipped = [.init(id: "x", reason: "changed", manager: "m-2")]
        let both = InboxBulk.marking([a, b], keys: [a.key, b.key], done: true, now: .now)
        let settled = InboxBulk.settle(both, before: [a.key: a, b.key: b], result: result)
        #expect(settled.map(\.manager) == ["m-1", "m-2"], "an answer's item keeps the manager asked")
        #expect(settled[0].isDone && !settled[1].isDone)
    }

    @Test func aManagerThatFailsKeepsItsItemsAndTheOthersStayFiled() async throws {
        let a = try item("x", "m-1"), b = try item("x", "m-2"), c = try item("y", "m-1")
        var asked: [String?] = []
        let sent = try await InboxBulk.byManager(InboxStore.managers([a, b, c])) { manager, mine in
            asked.append(manager)
            if manager == "m-2" { throw APIError(status: 500, message: "down") }
            return self.filed(mine)
        }
        #expect(asked == ["m-1", "m-2"], "each manager its own request")
        #expect(sent.result.changed.map(\.key) == [a.key, c.key])
        #expect(sent.lost.map(\.key) == [b.key] && sent.failure == "down")

        // As the store shows it: m-1's stay filed, m-2's goes back as it was.
        let before = [a.key: a, b.key: b, c.key: c]
        let shown = InboxBulk.marking([a, b, c], keys: Set(before.keys), done: true, now: .now)
        let lost = Set(sent.lost.map(\.key))
        let after = InboxBulk.rollback(InboxBulk.settle(shown, before: before, result: sent.result),
                                       before: before.filter { lost.contains($0.key) })
        #expect(after.map(\.isDone) == [true, false, true])
        #expect(after[1] == b)
        let head = InboxBulk.message(done: true, result: sent.result)
        #expect(InboxBulk.partly(head, lost: 1, error: "down") == "已将 2 件标为已处理；1 件没改成：down")
        #expect(InboxBulk.partly(head + "。", lost: 1, error: "down") == "已将 2 件标为已处理；1 件没改成：down", "one stop, not two")
    }

    @Test func whenEveryManagerFailsTheFilingFails() async throws {
        let a = try item("x", "m-1"), b = try item("x", "m-2")
        await #expect(throws: APIError.self) {
            _ = try await InboxBulk.byManager(InboxStore.managers([a, b])) { _, _ in throw APIError(status: 500, message: "down") }
        }
    }

    @Test func anUndoGoesToTheManagerThatAnswered() async throws {
        let a = try item("x", "m-2")
        let (_, undo) = try await InboxBulk.perform(.dismiss, [a], manager: "m-2",
            bulk: { _, _ in InboxBulkReply(action: "dismiss", changed: [], unchanged: [], undo: .init(action: "reopen", ids: ["x"])) },
            legacy: { _ in .none })
        #expect(undo == .bulk(.init(action: "reopen", ids: ["x"]), manager: "m-2"))
    }
}
