import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct DraftFeedCursorTests {
    private let route = "/api/v1/assistant/conversations/cursor-conversation/feed"
    private let start = Date(timeIntervalSince1970: 1_800_000_000)

    private func item(_ index: Int, dismissed: Bool = true) -> FeedItem {
        var body: [String: JSONValue] = ["draft": .string("Synthetic draft")]
        if dismissed {
            body["dismissed_at"] = .string("2026-11-01T08:00:00Z")
            body["dismissal_id"] = .string("receipt_\(index)")
        }
        return FeedItem(id: "cursor_\(index)", conversation: "cursor-conversation", at: start.addingTimeInterval(Double(index)),
                        kind: .message, ref: "same-reference", body: .object(body))
    }

    private func page(_ items: [FeedItem], next: String) throws -> Data {
        struct Page: Encodable {
            var items: [FeedItem]
            var nextCursor: String
            enum CodingKeys: String, CodingKey { case items, nextCursor = "next_cursor" }
        }
        return try JSON.encoder.encode(Page(items: items, nextCursor: next))
    }

    @Test func olderUpdatedCardsDoNotStarveANewPendingDraftAndEveryPageKeepsItsOwner() async throws {
        let hub = FakeHub()
        let tasks = TasksStore(toast: { _ in })
        tasks.received(item(10_000, dismissed: false))
        let token = "opaque+/= token"
        hub.on("GET", route) { request in
            #expect(request.headers[APIClient.managerHeader] == "captured-owner")
            if let cursor = request.query["cursor"] {
                #expect(cursor == token)
                #expect(request.query == ["cursor": token])
                return try page([item(10_001, dismissed: false)], next: "")
            }
            #expect(request.query["paginate"] == "1")
            #expect(request.query["since"] != nil)
            return try page((0..<500).map { item($0) }, next: token)
        }
        await tasks.load("cursor-conversation", refreshTodos: false, api: hub.client.manager("captured-owner"))?.value
        #expect(tasks.feed("cursor-conversation").count == 502)
        #expect(tasks.feed("cursor-conversation").filter { $0.dismissedAt != nil }.count == 500)
        #expect(tasks.feed("cursor-conversation").last?.id == "cursor_10001")
        #expect(hub.sent("GET", route).count == 2)
        #expect(tasks.feedRecoveryPending.isEmpty)
    }

    @Test func aBoundedRecoveryIsExplicitlyIncompleteAndResumesTheOpaqueCursorOnRefresh() async throws {
        let hub = FakeHub()
        var count = 0
        var toasts: [Toast] = []
        let tasks = TasksStore(toast: { toasts.append($0) })
        hub.on("GET", route) { request in
            if count == 0 { #expect(request.query["paginate"] == "1") }
            else { #expect(request.query == ["cursor": "cursor-page-\(count)"]) }
            count += 1
            return try page([item(count)], next: count < FeedPaging.maxPages + 2 ? "cursor-page-\(count)" : "")
        }
        await tasks.load("cursor-conversation", refreshTodos: false, api: hub.client)?.value
        #expect(count == FeedPaging.maxPages)
        #expect(tasks.feedRecoveryPending.contains("cursor-conversation"))
        #expect(toasts.last?.style == .error)
        await tasks.load("cursor-conversation", refreshTodos: false, api: hub.client)?.value
        #expect(count == FeedPaging.maxPages + 2)
        #expect(tasks.feedRecoveryPending.isEmpty)
        #expect(Set(tasks.feed("cursor-conversation").map(\.id)).count == count)
    }

    @Test func aRepeatedCursorStopsWithoutClaimingThatRecoveryCompleted() async throws {
        let hub = FakeHub()
        let tasks = TasksStore(toast: { _ in })
        hub.on("GET", route) { _ in try page([item(0)], next: "repeated") }
        await tasks.load("cursor-conversation", refreshTodos: false, api: hub.client)?.value
        #expect(hub.sent("GET", route).count == 2)
        #expect(tasks.feedRecoveryPending.contains("cursor-conversation"))
        #expect(tasks.error != nil)
        tasks.forget()
        #expect(tasks.feedRecoveryPending.isEmpty)
    }
}
