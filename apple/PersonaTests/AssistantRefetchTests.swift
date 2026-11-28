import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The test's own clock for `TodoStore.Pushes.sleep`: a wait ends only when the test moves time on, either all at once
/// (`advance`) or by the clock's own time (`tick`).
@MainActor
private final class PushClock {
    private var waiting: [(until: Duration, wake: CheckedContinuation<Void, Never>)] = []
    private(set) var naps: [Duration] = []
    /// The time since the test began.
    private(set) var now: Duration = .zero
    var sleeping: Int { waiting.count }

    func sleep(_ d: Duration) async throws {
        naps.append(d)
        let until = now + d
        await withCheckedContinuation { waiting.append((until, $0)) }
        try Task.checkCancellation()
    }

    /// The waits so far are over, however long they were.
    func advance() {
        let woken = waiting
        waiting = []
        for w in woken { w.wake.resume() }
    }

    /// Time moves on by `d`: the waits that end by then are over.
    func tick(_ d: Duration) {
        now += d
        let due = waiting.filter { $0.until <= now }
        waiting.removeAll { $0.until <= now }
        for w in due { w.wake.resume() }
    }
}

@MainActor
private final class Front { var shown = false }

/// A hub whose to-do list moves with `version`, counting the list and revision reads the store makes.
@MainActor
private final class PushedHub {
    var version = 0
    var listReads = 0
    var revisionReads = 0
    /// Whether the list answers carry the revision header (a hub listing `todo_revision`).
    var tagged = false

    var list: [TodoItem] { [TodoItem(id: "v\(version)", title: "Version \(version)")] }
    var tag: String { "r\(version)" }

    func read(_: APIClient, _: String?) async throws -> TodosPage {
        listReads += 1
        await Task.yield() // a read takes a moment
        return TodosPage(list: list, etag: "e\(version)", revision: tagged ? tag : nil)
    }

    func revision(_: APIClient, _ known: String) async throws -> TodoRevisionAnswer {
        revisionReads += 1
        return known == tag ? .unchanged : .changed(tag)
    }
}

/// The Persona page's refetch storm (iOS battery audit): each `todos` push and each task / report feed item read the
/// whole to-do list again (megabytes on a busy hub, up to 17 times a minute), and each direction tree push read the
/// assistant settings and the directions again while one direction's conversation stayed unlisted.
@Suite(.serialized)
@MainActor
struct AssistantRefetchTests {
    private func store(_ hub: PushedHub, clock: PushClock? = nil, revision: Bool = false,
                       shown: @escaping @MainActor () -> Bool = { true }) -> TodoStore {
        var pushes = TodoStore.Pushes(gap: .seconds(2), maxGap: .seconds(10), hiddenWait: .seconds(30), revisionListed: { revision },
                                      shown: shown, revision: { try await hub.revision($0, $1) })
        if let clock { pushes.sleep = { try await clock.sleep($0) } }
        return TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, read: { try await hub.read($0, $1) },
                         pushes: pushes)
    }

    /// Waits until the pushed reads' loop has read and sleeps out its gap (or is done).
    private func cooling(_ clock: PushClock, _ store: TodoStore) async throws {
        try await waitForRefreshTest { clock.sleeping == 1 && !store.loading }
    }

    /// 51 pushes in six bursts, each changing the list, as a busy Agent's reports do: one read per wait rather than one
    /// per push, the wait doubling from 2 s to at most 10 s while they keep coming, and the last list lands once they stop.
    @Test func aBurstOfPushesReadsTheListOncePerGapAndLandsTheLast() async throws {
        let hub = PushedHub(), clock = PushClock()
        let store = store(hub, clock: clock)
        defer { store.forget() }
        let api = FakeHub().client
        hub.version = 1
        store.pushed(api: api)
        try await cooling(clock, store)
        #expect(hub.listReads == 1, "the first push reads at once")
        for _ in 0..<5 {
            for _ in 0..<10 {
                hub.version += 1
                store.pushed(api: api)
                await Task.yield()
            }
            #expect(clock.sleeping == 1, "pushes during the gap wait for it")
            clock.advance()
            try await cooling(clock, store)
        }
        clock.advance()
        await store.pushesSettled()
        #expect(hub.version == 51)
        #expect(hub.listReads == 6, "51 pushes: one read, then one per wait (was one per push)")
        #expect(store.todos.map(\.id) == ["v51"], "the state once the pushes stop is the latest")
        #expect(clock.naps == [2, 4, 8, 10, 10, 10].map { Duration.seconds($0) })
    }

    /// A minute of pushes, one a second, each changing the list (an Agent reporting steadily): the reads come at 0, 2, 6,
    /// 14, 24, 34, 44 and 54 s and once more after the pushes stop, 9 instead of 60, and that last one has the last list.
    /// After a whole quiet wait the next push reads at once and waits 2 s again.
    @Test func aMinuteOfPushesReadsAFewTimesAndLandsTheLast() async throws {
        let hub = PushedHub(), clock = PushClock()
        let store = store(hub, clock: clock)
        defer { store.forget() }
        let api = FakeHub().client
        func settled() async throws {
            try await waitForRefreshTest { !store.loading && (clock.sleeping == 1 || !store.pushing) }
        }
        var readAt: [Duration] = []
        var seen = 0
        func note() {
            if hub.listReads > seen { readAt.append(clock.now); seen = hub.listReads }
        }
        for _ in 0..<60 {
            hub.version += 1
            store.pushed(api: api)
            try await settled()
            note()
            clock.tick(.seconds(1))
            try await settled()
            note()
        }
        while store.pushing {
            clock.tick(.seconds(1))
            try await settled()
            note()
        }
        #expect(hub.listReads == 9, "60 pushes in a minute read the list \(hub.listReads) times")
        #expect(readAt == [0, 2, 6, 14, 24, 34, 44, 54, 64].map { Duration.seconds($0) })
        #expect(store.todos.map(\.id) == ["v60"], "the state once the pushes stop is the last")

        // A whole 10 s wait went by without a push: the next one reads at once and waits 2 s again.
        let naps = clock.naps.count
        hub.version += 1
        store.pushed(api: api)
        try await settled()
        #expect(hub.listReads == 10)
        #expect(clock.naps.dropFirst(naps).first == .seconds(2))
        clock.advance()
        await store.pushesSettled()
        #expect(store.todos.map(\.id) == ["v61"])
    }

    /// With `todo_revision`, a push whose revision did not move reads nothing; one that moved reads the list once.
    @Test func anUnchangedRevisionReadsNoList() async throws {
        let hub = PushedHub(), clock = PushClock()
        hub.tagged = true
        let store = store(hub, clock: clock, revision: true)
        defer { store.forget() }
        let api = FakeHub().client
        hub.version = 1
        await store.refresh(api: api) // the page's own read: the rows and their revision
        #expect(store.revision == "r1")
        for _ in 0..<20 {
            store.pushed(api: api)
            try await cooling(clock, store)
            clock.advance()
            await store.pushesSettled()
        }
        #expect(hub.listReads == 1, "nothing changed: no push read the list")
        #expect(hub.revisionReads == 20)

        hub.version = 2
        store.pushed(api: api)
        try await cooling(clock, store)
        clock.advance()
        await store.pushesSettled()
        #expect(hub.listReads == 2)
        #expect(store.todos.map(\.id) == ["v2"])
        #expect(store.revision == "r2")
    }

    /// The revision asked before the list stands for the list when the list's answer names none (a write raced it).
    @Test func theAskedRevisionStandsInForAnUntaggedList() async throws {
        let hub = PushedHub(), clock = PushClock()
        let store = store(hub, clock: clock, revision: true)
        defer { store.forget() }
        let api = FakeHub().client
        hub.version = 1
        hub.tagged = true
        await store.refresh(api: api)
        hub.tagged = false
        hub.version = 2
        store.pushed(api: api)
        try await cooling(clock, store)
        clock.advance()
        await store.pushesSettled()
        #expect(store.revision == "r2")
        #expect(store.todos.map(\.id) == ["v2"])
    }

    /// While the Persona tab is not in front, pushes wait for it; as it comes back the latest list is read once.
    @Test func pushesWhileThePagesAreHiddenAreReadOnceTheyComeBack() async throws {
        let hub = PushedHub(), front = Front()
        let store = store(hub, shown: { front.shown })
        defer { store.forget() }
        let api = FakeHub().client
        for _ in 0..<20 {
            hub.version += 1
            store.pushed(api: api)
            await Task.yield()
        }
        for _ in 0..<50 { await Task.yield() }
        #expect(hub.listReads == 0, "hidden: nothing read yet")
        front.shown = true
        store.shownChanged()
        try await waitForRefreshTest { hub.listReads == 1 && !store.loading }
        #expect(store.todos.map(\.id) == ["v20"])
        store.forget() // ends the gap's real sleep
    }

    /// The owner's own reads never wait for the pushes' gap.
    @Test func aManualReadDoesNotWaitForTheGap() async throws {
        let hub = PushedHub(), clock = PushClock()
        let store = store(hub, clock: clock)
        defer { store.forget() }
        let api = FakeHub().client
        hub.version = 1
        store.pushed(api: api)
        try await cooling(clock, store)
        hub.version = 2
        await store.refresh(api: api)
        #expect(hub.listReads == 2)
        #expect(store.todos.map(\.id) == ["v2"])
        clock.advance()
        await store.pushesSettled()
    }
}

/// The direction tree's pushes: a direction whose conversation the settings never list reads them again once, not on
/// every push.
@Suite(.serialized)
@MainActor
struct DirectionTreeRereadTests {
    private func model(_ hub: FakeHub) throws -> AppModel {
        let transport = try #require(hub.client.transport)
        let folder = FileManager.default.temporaryDirectory.appending(path: "direction-reread-" + UUID().uuidString)
        let model = AppModel(stateFolder: folder)
        model.linkHooksForTests = (transport: transport, socket: { SyncSocket() })
        model.connectOnly(.demo)
        return model
    }

    @Test func anUnlistedDirectionReadsTheSettingsOnce() async throws {
        let chosen = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(chosen, forKey: "assistant.current") }
        let zone = TimeZone.current.identifier
        let hub = FakeHub()
        // "archived" leads a direction, but its conversation is not among the page's (archived, or on another computer).
        hub.on("GET", "/api/v1/assistant", json: #"""
        {"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[],"main":"main",
         "timezone":"\#(zone)","leaders":["archived"]}
        """#)
        hub.on("GET", "/api/v1/assistant/leaders", json: #"{"leaders":[{"id":"archived","name":"Archived"}]}"#)
        hub.on("GET", "/api/v1/assistant/directions/tree", json: #"""
        {"directions":[{"id":"archived","name":"Archived","counts":{},"tasks":[]}]}
        """#)
        let model = try model(hub)
        await model.refreshAssistant()
        let settled = hub.sent("GET", "/api/v1/assistant").count
        let leaders = hub.sent("GET", "/api/v1/assistant/leaders").count
        for _ in 0..<20 { await model.refreshDirectionTree() }
        #expect(hub.sent("GET", "/api/v1/assistant/directions/tree").count >= 20)
        #expect(hub.sent("GET", "/api/v1/assistant").count == settled + 1,
                "20 tree pushes: one settings read for the unlisted direction (was one per push)")
        #expect(hub.sent("GET", "/api/v1/assistant/leaders").count == leaders + 1)

        hub.on("GET", "/api/v1/assistant/directions/tree", json: #"""
        {"directions":[{"id":"archived","name":"Archived","counts":{},"tasks":[]},
                       {"id":"fresh","name":"Fresh","counts":{},"tasks":[]}]}
        """#)
        await model.refreshDirectionTree()
        await model.refreshDirectionTree()
        #expect(hub.sent("GET", "/api/v1/assistant").count == settled + 2, "a new direction is read for, once")
    }
}
