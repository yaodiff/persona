import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct MessageDraftActionsTests {
    private func card(_ id: String = "feed_1", dismissed: Bool = false) -> FeedItem {
        var body: [String: JSONValue] = ["todo_id": .string("todo_1"), "draft": .string("Original draft")]
        if dismissed {
            body["dismissed_at"] = .string("2026-11-01T08:00:00Z")
            body["dismissal_id"] = .string("receipt_1")
        }
        return FeedItem(id: id, conversation: "c1", at: Date(timeIntervalSince1970: 100), kind: .message,
                        ref: "lark:item_1", body: .object(body))
    }

    private func response(_ item: FeedItem, receipt: Bool = false) throws -> Data {
        var object: [String: Any] = ["item": try JSONSerialization.jsonObject(with: JSON.encoder.encode(item))]
        if receipt { object["dismissal_id"] = "receipt_1" }
        return try JSONSerialization.data(withJSONObject: object)
    }

    @Test func dismissUsesOriginalDraftAndReceiptUndoClearsTheWholeBody() async throws {
        let hub = FakeHub()
        var toasts: [Toast] = []
        let store = TasksStore(toast: { toasts.append($0) })
        let original = card()
        store.received(original)
        hub.on("POST", "/api/v1/assistant/feed/feed_1/dismiss") { _ in try response(card(dismissed: true), receipt: true) }
        hub.on("POST", "/api/v1/assistant/feed/feed_1/undo-dismiss") { _ in try response(original) }
        #expect(await store.dismissMessage(original, api: hub.client.manager("owner")))
        #expect(store.messageCard(original).dismissedAt != nil)
        #expect(TasksStore.messageState(store.messageCard(original), inbox: nil, sentHere: false, refused: false) == .dismissed)
        let request = try #require(hub.sent("POST", "/api/v1/assistant/feed/feed_1/dismiss").first)
        #expect(request.headers[APIClient.managerHeader] == "owner")
        let body = try JSONSerialization.jsonObject(with: #require(request.body)) as? [String: String]
        #expect(body == ["draft": "Original draft"])
        #expect(toasts.last?.action != nil)
        #expect(toasts.last?.duration == Theme.Motion.draftUndo)
        #expect(await store.undoDismissMessage(original, receipt: "receipt_1", api: hub.client.manager("owner")))
        #expect(store.messageCard(original).dismissedAt == nil)
        #expect(store.messageCard(original).dismissalID == nil)
        let undo = try #require(hub.sent("POST", "/api/v1/assistant/feed/feed_1/undo-dismiss").first)
        #expect(undo.headers[APIClient.managerHeader] == "owner")
        #expect(try JSONSerialization.jsonObject(with: #require(undo.body)) as? [String: String] == ["dismissal_id": "receipt_1"])
        #expect(hub.sent("POST", "/api/v1/inbox/lark:item_1/reply").isEmpty)
        #expect(hub.sent("POST", "/api/v1/inbox/lark:item_1/done").isEmpty)
        #expect(hub.requests.count == 2)
    }

    @Test func theLoadedCardOwnerIsPinnedForDismissAndTheToastUndo() async throws {
        let hub = FakeHub()
        let original = card()
        var toasts: [Toast] = []
        let store = TasksStore(toast: { toasts.append($0) })
        let snapshot = String(decoding: try JSON.encoder.encode(APIClient.FeedResponseFixture(items: [original])), as: UTF8.self)
        hub.on("GET", "/api/v1/assistant/conversations/c1/feed", json: snapshot)
        hub.on("GET", "/api/v1/assistant/conversations/c2/feed", json: #"{"items":[]}"#)
        hub.on("POST", "/api/v1/assistant/feed/feed_1/dismiss") { _ in try response(card(dismissed: true), receipt: true) }
        hub.on("POST", "/api/v1/assistant/feed/feed_1/undo-dismiss") { _ in try response(original) }
        await store.load("c1", refreshTodos: false, api: hub.client.manager("owner"))?.value
        #expect(await store.dismissMessage(original))
        await store.load("c2", refreshTodos: false, api: hub.client.manager("other"))?.value
        let action = try #require(toasts.last?.action)
        action.run()
        for _ in 0..<1000 where hub.sent("POST", "/api/v1/assistant/feed/feed_1/undo-dismiss").isEmpty { await Task.yield() }
        #expect(hub.sent("POST", "/api/v1/assistant/feed/feed_1/dismiss").first?.headers[APIClient.managerHeader] == "owner")
        #expect(hub.sent("POST", "/api/v1/assistant/feed/feed_1/undo-dismiss").first?.headers[APIClient.managerHeader] == "owner")
    }

    @Test func dismissedCardStaysDismissedWithAnOpenInboxAndNewProgressStillWaits() {
        let dismissed = card(dismissed: true)
        let item = InboxItem(id: "lark:item_1", source: "lark", kind: "dm", account: "example", accountName: "Example",
                             title: "Example", preview: "", count: 1)
        #expect(TasksStore.messageState(dismissed, inbox: item, sentHere: false, refused: false) == .dismissed)
        #expect(TasksStore.messageState(card("feed_2"), inbox: item, sentHere: false, refused: false) == .open)
        let store = TasksStore(toast: { _ in })
        store.later(dismissed.id)
        #expect(store.laterMessages.contains(dismissed.id))
        #expect(!store.laterMessages.contains("feed_2"))
    }

    @Test func refreshedSnapshotRestoresPersistedDismissalAndUndo() async throws {
        let hub = FakeHub()
        let original = card()
        let filed = card(dismissed: true)
        hub.on("GET", "/api/v1/assistant/conversations/c1/feed", json: String(decoding: try JSON.encoder.encode(APIClient.FeedResponseFixture(items: [filed])), as: UTF8.self))
        let store = TasksStore(toast: { _ in })
        await store.load("c1", refreshTodos: false, api: hub.client)?.value
        #expect(store.messageCard(original).dismissedAt != nil)
        store.received(original)
        #expect(store.messageCard(original).body == original.body)
        #expect(store.feed("c1").count == 1)
    }

    @Test func failedDismissAndUndoLeaveThePreviousStateAndAllowRetry() async throws {
        let hub = FakeHub()
        var toasts: [Toast] = []
        let store = TasksStore(toast: { toasts.append($0) })
        let original = card()
        store.received(original)
        hub.on("POST", "/api/v1/assistant/feed/feed_1/dismiss") { _ in throw APIError(status: 503, message: "Unavailable") }
        #expect(!(await store.dismissMessage(original, api: hub.client)))
        #expect(store.messageCard(original).dismissedAt == nil)
        #expect(store.busyMessages.isEmpty)
        #expect(toasts.last?.style == .error)
        hub.on("POST", "/api/v1/assistant/feed/feed_1/dismiss") { _ in try response(card(dismissed: true), receipt: true) }
        #expect(await store.dismissMessage(original, api: hub.client))
        hub.on("POST", "/api/v1/assistant/feed/feed_1/undo-dismiss") { _ in throw APIError(status: 503, message: "Unavailable") }
        #expect(!(await store.undoDismissMessage(original, receipt: "receipt_1", api: hub.client)))
        #expect(store.messageCard(original).dismissedAt != nil)
        #expect(store.busyMessages.isEmpty)
        #expect(hub.sent("POST", "/api/v1/inbox/lark:item_1/reply").isEmpty)
    }

    @Test func concurrentSendAndDismissOnlyAdmitOneOperation() async throws {
        let gate = DraftGate()
        var api = FakeHub().client
        api.transport = { try await gate.answer($0) }
        let store = TasksStore(toast: { _ in })
        let original = card()
        store.received(original)
        let first = Task { await store.dismissMessage(original, api: api) }
        for _ in 0..<1000 where gate.pending == nil { await Task.yield() }
        #expect(store.busyMessages.contains(original.id))
        await store.sendMessage(original, itemID: original.ref!, text: "Local edit", api: api)
        #expect(!(await store.dismissMessage(original, api: api)))
        gate.finish(try response(card(dismissed: true), receipt: true))
        #expect(await first.value)
        #expect(gate.requests.count == 1)
        #expect(store.busyMessages.isEmpty)
    }

    @Test func aReadStartedBeforeDismissCannotResurrectTheDraft() async throws {
        let hub = FakeHub()
        let gate = DraftGate()
        var readAPI = hub.client
        readAPI.transport = { try await gate.answer($0) }
        let store = TasksStore(toast: { _ in })
        let original = card()
        store.received(original)
        let loading = store.load("c1", refreshTodos: false, api: readAPI)
        for _ in 0..<1000 where gate.pending == nil { await Task.yield() }
        hub.on("POST", "/api/v1/assistant/feed/feed_1/dismiss") { _ in try response(card(dismissed: true), receipt: true) }
        #expect(await store.dismissMessage(original, api: hub.client))
        gate.finish(try JSON.encoder.encode(APIClient.FeedResponseFixture(items: [original])))
        await loading?.value
        #expect(store.messageCard(original).dismissedAt != nil)
    }

    @Test func anAuthoritativePushWinsOverAnOlderReadEvenWhenItsBodyIsUnchanged() async throws {
        let original = card()
        for noOp in [false, true] {
            let gate = DraftGate()
            var api = FakeHub().client
            api.transport = { try await gate.answer($0) }
            let store = TasksStore(toast: { _ in })
            store.received(original)
            let loading = store.load("c1", refreshTodos: false, api: api)
            for _ in 0..<1000 where gate.pending == nil { await Task.yield() }
            let authoritative = noOp ? original : card(dismissed: true)
            let stale = noOp ? card(dismissed: true) : original
            store.received(authoritative)
            gate.finish(try JSON.encoder.encode(APIClient.FeedResponseFixture(items: [stale])))
            await loading?.value
            #expect(store.messageCard(original).body == authoritative.body)
        }
    }

    @Test func leavingTheHubFencesLateReceiptsAndUndo() async throws {
        let gate = DraftGate()
        var api = FakeHub().client
        api.transport = { try await gate.answer($0) }
        var toasts: [Toast] = []
        let store = TasksStore(toast: { toasts.append($0) })
        let original = card()
        store.received(original)
        let first = Task { await store.dismissMessage(original, api: api) }
        for _ in 0..<1000 where gate.pending == nil { await Task.yield() }
        store.forget()
        gate.finish(try response(card(dismissed: true), receipt: true))
        #expect(!(await first.value))
        #expect(store.feed("c1").isEmpty)
        #expect(store.busyMessages.isEmpty)
        #expect(toasts.isEmpty)
    }
}

private extension APIClient {
    struct FeedResponseFixture: Encodable { var items: [FeedItem] }
}

@MainActor
private final class DraftGate {
    var requests: [LocalRequest] = []
    var pending: CheckedContinuation<Data, any Error>?
    func answer(_ request: LocalRequest) async throws -> Data {
        requests.append(request)
        return try await withCheckedThrowingContinuation { pending = $0 }
    }
    func finish(_ data: Data) { pending?.resume(returning: data); pending = nil }
}
