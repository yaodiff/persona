import Foundation
import Testing
@testable import Persona

@MainActor struct LarkUnreadTests {
    private func chat(_ id: String, unread: Int = 0, marked: Bool = false) -> LarkConversation {
        var row = LarkConversation(id: id, name: id, isGroup: true, unread: unread)
        row.markedUnread = marked
        return row
    }

    private func page(_ rows: [LarkConversation], next: String? = nil, owner: String = "one") -> APIClient.LarkConversations {
        .init(conversations: rows, nextCursor: next, accountID: owner)
    }

    private func deps(_ rows: [LarkConversation] = []) -> LarkConversationsModel.Deps {
        .init(page: { _, _ in page(rows) }, pinned: { page([]) },
              status: { .init(configured: true, connected: true) }, cached: { nil }, save: { _ in })
    }

    @Test func unreadUsesItsOwnFeedCursorAndDoesNotOverwriteAllCache() async {
        var saved: [LarkConversationsModel.Snapshot] = []
        var requests: [String?] = []
        var d = deps([chat("read"), chat("unread", unread: 2)])
        d.pinned = { page([chat("read-pin"), chat("unread-pin", unread: 1)]) }
        d.save = { if let value = $0 { saved.append(value) } }
        d.unreadPage = { cursor, _ in
            requests.append(cursor)
            return page([chat(cursor == nil ? "unread-pin" : "older", unread: 1)], next: cursor == nil ? "unread-next" : nil)
        }
        let store = LarkConversationsModel(deps: d)
        #expect(store.filter == .all)
        await store.refresh()
        await store.select(.unread)
        await store.more()
        #expect(requests == [nil, "unread-next"])
        #expect(store.conversations.map(\.id) == ["unread-pin", "older"])
        #expect(saved.count == 1)
        #expect(saved.first?.conversations.map(\.id) == ["read", "unread"])
        await store.select(.all)
        #expect(store.conversations.map(\.id) == ["read", "unread"])
        #expect(store.pinned.map(\.id) == ["read-pin", "unread-pin"])
    }

    @Test func genericFallbackKeepsPagingPastReadOnlyPagesAndIncludesManualUnread() async {
        var d = deps()
        d.page = { cursor, _ in
            cursor == nil ? page([chat("read")], next: "older")
                          : page([chat("manual", marked: true), chat("counted", unread: 3), chat("also-read")])
        }
        let store = LarkConversationsModel(deps: d)
        await store.select(.unread)
        #expect(store.conversations.isEmpty)
        #expect(store.nextCursor == "older")
        await store.more()
        #expect(store.conversations.map(\.id) == ["manual", "counted"])
        #expect(store.nextCursor == nil)
    }

    @Test func serverUnreadScopeIsAuthoritativeWhenAnOlderGatewayOmitsTheManualFlag() async {
        var d = deps()
        d.unreadPage = { _, _ in page([chat("manual-from-scope")]) }
        let store = LarkConversationsModel(deps: d)
        await store.select(.unread)
        #expect(store.conversations.map(\.id) == ["manual-from-scope"])
    }

    @Test func lateAllRefreshCannotPopulateUnread() async {
        var gate: CheckedContinuation<Void, Never>?
        var d = deps()
        d.page = { _, _ in
            await withCheckedContinuation { gate = $0 }
            return page([chat("late-read")], next: "all-next")
        }
        d.unreadPage = { _, _ in page([chat("unread", unread: 1)]) }
        let store = LarkConversationsModel(deps: d)
        let all = Task { await store.refresh() }
        while gate == nil { await Task.yield() }
        let unread = Task { await store.select(.unread) }
        while store.filter != .unread { await Task.yield() }
        #expect(store.conversations.isEmpty)
        gate?.resume()
        await all.value
        await unread.value
        #expect(store.conversations.map(\.id) == ["unread"])
        #expect(store.nextCursor == nil)
    }

    @Test func supersededAllPaginationDoesNotPageTheNewFilter() async {
        var gate: CheckedContinuation<Void, Never>?
        var unreadRequests = 0
        var d = deps()
        d.page = { cursor, _ in
            if cursor != nil { await withCheckedContinuation { gate = $0 } }
            return page([chat(cursor ?? "all")], next: cursor == nil ? "all-next" : nil)
        }
        d.unreadPage = { _, _ in
            unreadRequests += 1
            return page([chat("unread", unread: 1)], next: "unread-next")
        }
        let store = LarkConversationsModel(deps: d)
        await store.refresh()
        let more = Task { await store.more() }
        while gate == nil { await Task.yield() }
        let unread = Task { await store.select(.unread) }
        while store.filter != .unread { await Task.yield() }
        gate?.resume()
        await more.value
        await unread.value
        #expect(unreadRequests == 1)
        #expect(store.nextCursor == "unread-next")
        #expect(store.conversations.map(\.id) == ["unread"])
    }

    @Test func unreadEventsRecheckTheLoadedTailAndRemoveReadChats() async {
        var read = false
        var d = deps()
        d.unreadPage = { cursor, _ in
            if cursor == nil { return page([chat("head", unread: 1)], next: "tail") }
            return page(read ? [] : [chat("tail", unread: 1)])
        }
        let store = LarkConversationsModel(deps: d)
        await store.select(.unread)
        await store.more()
        read = true
        await store.refreshHead(["tail"])
        #expect(store.conversations.map(\.id) == ["head"])
    }

    @Test func newUnreadHeadDoesNotRefetchEveryLoadedPage() async {
        var requests = 0
        var d = deps()
        d.pinned = { requests += 1; return page([]) }
        d.unreadPage = { cursor, _ in
            requests += 1
            return cursor == nil ? page([chat("head", unread: 1)], next: "tail") : page([chat("tail", unread: 1)])
        }
        let store = LarkConversationsModel(deps: d)
        await store.select(.unread)
        await store.more()
        requests = 0
        await store.refreshHead(["head"])
        #expect(requests == 2)
        #expect(store.conversations.map(\.id) == ["head", "tail"])
    }

    @Test func accountSwitchInUnreadClearsThePreviousOwnersAllCache() async {
        var owner = "one"
        var cleared = false
        var d = deps([chat("old")])
        d.pinned = { page([], owner: owner) }
        d.unreadPage = { _, expected in page([chat(expected, unread: 1)], owner: owner) }
        d.save = { if $0 == nil { cleared = true } }
        let store = LarkConversationsModel(deps: d)
        await store.refresh()
        owner = "two"
        await store.select(.unread)
        #expect(cleared)
        #expect(store.accountID == "two")
        #expect(store.conversations.map(\.id) == ["two"])
    }

    @Test func unreadFailureKeepsTheFilterAndRetryDoesNotShowAll() async {
        var offline = true
        var d = deps([chat("read")])
        d.unreadPage = { _, _ in
            if offline { throw URLError(.notConnectedToInternet) }
            return page([chat("unread", unread: 1)])
        }
        let store = LarkConversationsModel(deps: d)
        await store.refresh()
        await store.select(.unread)
        #expect(store.filter == .unread && store.failed && store.conversations.isEmpty)
        offline = false
        await store.refresh()
        #expect(store.filter == .unread && !store.failed)
        #expect(store.conversations.map(\.id) == ["unread"])
    }

    @Test func accountMismatchOnAnUnreadTailClearsRowsAndRemainsRetryable() async {
        var d = deps()
        d.unreadPage = { cursor, _ in
            cursor == nil ? page([chat("unread", unread: 1)], next: "tail")
                          : page([chat("another-owner", unread: 1)], owner: "two")
        }
        let store = LarkConversationsModel(deps: d)
        await store.select(.unread)
        await store.more()
        #expect(store.filter == .unread && store.pageFailed)
        #expect(store.accountID == nil && store.conversations.isEmpty && store.nextCursor == nil)
    }

    @Test func genericWireManualUnreadSurvivesProjectionAndPageFiltering() throws {
        let wire: [String: Any] = ["conversations": [
            ["id": "manual", "name": "Manual", "kind": "group", "unread": 0, "marked_unread": true],
            ["id": "read", "name": "Read", "kind": "group", "unread": 0]], "next_cursor": "older"]
        let mapped = MessagingShape.conversations(wire, source: "example", account: nil, user: "one")
        let result = try JSON.decoder.decode(APIClient.LarkConversations.self, from: JSONSerialization.data(withJSONObject: mapped)).unreadOnly()
        #expect(result.conversations.map(\.id) == ["manual"])
        #expect(result.nextCursor == "older")
        #expect(result.accountID != nil)
    }
}
