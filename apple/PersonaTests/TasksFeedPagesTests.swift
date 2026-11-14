import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A conversation's feed longer than one page of the hub's (500, tasks.go `feedPage`) is read whole (#4642).
@MainActor
struct TasksFeedPagesTests {
    private static let route = "/api/v1/assistant/conversations/conversation/feed"

    private func store() -> TasksStore {
        TasksStore(todoStore: TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}), viewStore: AssistantViewsStore())
    }

    @Test func aColdStartReadsEveryPage() async {
        let hub = FakeHub()
        let feed = FakeFeed(count: 1201)
        feed.serve(hub, route: Self.route)
        let tasks = store()
        await tasks.load("conversation", refreshTodos: false, api: hub.client)?.value
        let shown = tasks.feed("conversation")
        #expect(shown.count == 1201)
        #expect(shown.first?.id == "feed_0" && shown.last?.id == "feed_1200")
        // 500 + 500 + 201: the short third page is the last.
        #expect(hub.sent("GET", Self.route).count == 3)
    }

    @Test func aPushAfterTheLoadLeavesNothingBehindOnReconnect() async throws {
        let hub = FakeHub()
        let feed = FakeFeed(count: 1201)
        feed.serve(hub, route: Self.route)
        let tasks = store()
        await tasks.load("conversation", refreshTodos: false, api: hub.client)?.value

        // A new item comes as an event, then the socket drops and comes back with three more the app missed.
        feed.add(4)
        tasks.received(try JSON.decoder.decode(FeedItem.self, from: feed.json(1201)))
        for refresh in tasks.reconnected(api: hub.client) { await refresh.value }

        let shown = tasks.feed("conversation")
        #expect(shown.count == 1205)
        #expect(Set(shown.map(\.id)).count == 1205 && shown.last?.id == "feed_1204")
    }

    @Test func aHubThatNeverRunsOutStopsAtTheBound() async {
        let hub = FakeHub()
        let feed = FakeFeed(count: 0, endless: true)
        feed.serve(hub, route: Self.route)
        let tasks = store()
        await tasks.load("conversation", refreshTodos: false, api: hub.client)?.value
        #expect(hub.sent("GET", Self.route).count == 40)
    }
}

/// A conversation's feed on the fake hub: message items one second apart, answered as tasks.go does (after `since`,
/// oldest first, 500 at most). `endless`: every request gets a full page of new items.
private final class FakeFeed: @unchecked Sendable {
    private let lock = NSLock()
    private var count: Int
    private let endless: Bool
    private let start = Date(timeIntervalSince1970: 1_800_000_000)
    private let time: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    init(count: Int, endless: Bool = false) {
        self.count = count
        self.endless = endless
    }

    func add(_ n: Int) { lock.withLock { count += n } }

    private func object(_ i: Int) -> [String: Any] {
        ["id": "feed_\(i)", "conversation": "conversation", "kind": "message",
         "at": time.string(from: start.addingTimeInterval(TimeInterval(i)))]
    }

    func json(_ i: Int) throws -> Data { try JSONSerialization.data(withJSONObject: object(i)) }

    func serve(_ hub: FakeHub, route: String) {
        hub.on("GET", route) { [self] req in
            let page: [[String: Any]] = lock.withLock {
                let first = req.query["since"].flatMap { time.date(from: $0) }
                    .map { Int($0.timeIntervalSince(start)) + 1 } ?? 0
                if endless { count = first + 500 }
                return (first..<max(first, min(count, first + 500))).map(object)
            }
            return try JSONSerialization.data(withJSONObject: ["items": page])
        }
    }
}
