import Foundation
import Testing
@testable import Persona

@MainActor
struct SessionRefreshTests {
    nonisolated private struct Row: Decodable, Equatable, Sendable {
        var id: String
        var text: String
    }
    private typealias Page = SessionListPage<Row>

    private final class Gate {
        private(set) var starts = 0
        private var pending: [CheckedContinuation<Page, any Error>] = []
        func read() async throws -> Page {
            starts += 1
            return try await withCheckedThrowingContinuation { pending.append($0) }
        }
        func finish(_ page: Page) throws {
            let next = try #require(pending.first)
            pending.removeFirst()
            next.resume(returning: page)
        }
        func fail() throws {
            let next = try #require(pending.first)
            pending.removeFirst()
            next.resume(throwing: APIError(status: 409, message: "List snapshot changed"))
        }
        func close() {
            let remaining = pending
            pending = []
            for next in remaining { next.resume(throwing: CancellationError()) }
        }
    }

    private func page(_ text: String, revision: String? = nil, more: Bool? = nil) -> Page {
        Page(sessions: [Row(id: "row", text: text)], remove: [], hasMore: more,
             nextCursor: more == true ? "cursor" : nil, revision: revision,
             total: revision == nil ? nil : 100)
    }

    private func counts() -> SessionListCounts {
        SessionListCounts(total: 100, machine: [:], project: [:], group: [:], state: [:], revision: "counts")
    }

    /// A clock the test moves: `sleep` records what it was asked for and returns only when the test lets it.
    @MainActor private final class FakeClock {
        var now = ContinuousClock.now
        private(set) var asked: [Duration] = []
        private var sleepers: [CheckedContinuation<Void, any Error>] = []
        var sleeping: Int { sleepers.count }
        var length = HintWindow.length
        func window() -> HintWindow {
            HintWindow(length: { [unowned self] in length }, now: { [unowned self] in now }, sleep: { [unowned self] wait in
                asked.append(wait)
                try await withCheckedThrowingContinuation { sleepers.append($0) }
                now = now.advanced(by: wait)
            })
        }
        func wake() { let all = sleepers; sleepers = []; for s in all { s.resume() } }
        func cancel() { let all = sleepers; sleepers = []; for s in all { s.resume(throwing: CancellationError()) } }
    }

    @Test func aHintWindowLetsTheFirstReadGoAndHoldsTheNextToItsEnd() async throws {
        let clock = FakeClock()
        let window = clock.window()
        try await window.enter()
        #expect(clock.asked.isEmpty, "the first read after a quiet spell starts at once")
        clock.now = clock.now.advanced(by: .milliseconds(500))
        #expect(window.remaining == .milliseconds(1500))
        let second = Task { try await window.enter() }
        try await waitForRefreshTest { clock.sleeping == 1 }
        #expect(clock.asked == [.milliseconds(1500)])
        clock.wake()
        try await second.value
        clock.now = clock.now.advanced(by: .seconds(3))
        try await window.enter()
        #expect(clock.asked.count == 1, "a read a whole window after the last waits for nothing")
        window.reset()
        try await window.enter()
        #expect(clock.asked.count == 1, "a new query starts its own windows")
    }

    /// The hub's lists read at most once a window for hints (#9487, the web's #9042): a burst inside the window shares
    /// one read at its end.
    @Test func hintsInsideAWindowShareOneReadAtItsEnd() async throws {
        let clock = FakeClock()
        var reads: [String?] = []
        let list = SessionListModel<Row>(id: { $0.id }, removalID: { $0 }, fetch: { _, _, since in
            reads.append(since)
            return page("r\(reads.count)", revision: "r\(reads.count)", more: false)
        }, counts: { _ in counts() }, waitForHints: {}, hintWindow: clock.window())
        defer { list.cancel(); clock.cancel() }
        await list.load()
        #expect(reads == [nil])
        list.invalidate(revision: "h1")
        try await waitForRefreshTest { reads.count == 2 }
        #expect(clock.asked.isEmpty, "the first hint reads at once")
        for i in 2...20 { list.invalidate(revision: "h\(i)") }
        try await waitForRefreshTest { clock.sleeping == 1 }
        #expect(reads.count == 2, "hints inside the window wait for its end")
        clock.wake()
        try await waitForRefreshTest { reads.count == 3 }
        await Task.yield()
        #expect(reads == [nil, "r1", "r2"], "one read for the window's 19 hints, from the last revision held")
        #expect(clock.asked.count == 1)
    }

    /// A zero-length window reads every hint as it comes, and `releaseHintWindow` lets a read waiting for a window go at
    /// once (docs/apple/ios-state.md "Timely"). An Agents list in sight has its own short window
    /// (`SessionHintsInSightTests`, #16758).
    @Test func aZeroWindowReadsAsHintsComeAndAReleaseLetsTheWaitGo() async throws {
        let clock = FakeClock()
        var reads = 0
        let list = SessionListModel<Row>(id: { $0.id }, removalID: { $0 }, fetch: { _, _, _ in
            reads += 1
            return page("r\(reads)", revision: "r\(reads)", more: false)
        }, counts: { _ in counts() }, waitForHints: {}, hintWindow: clock.window())
        defer { list.cancel(); clock.cancel() }
        await list.load()
        clock.length = .zero
        for i in 1...3 {
            list.invalidate(revision: "h\(i)")
            try await waitForRefreshTest { reads == 1 + i }
        }
        #expect(clock.asked.isEmpty)
        clock.length = HintWindow.length // the next hint inside the window waits
        list.invalidate(revision: "h4")
        try await waitForRefreshTest { clock.sleeping == 1 }
        #expect(reads == 4)
        list.releaseHintWindow() // an Agents list came into sight
        try await waitForRefreshTest { reads == 5 }
    }

    @Test func aBurstDuringALegacyReadProducesOnlyOneTrailingRead() async throws {
        let gate = Gate()
        var hints = 0
        let list = SessionListModel<Row>(id: { $0.id }, removalID: { $0 }, fetch: { _, _, _ in
            try await gate.read()
        }, counts: { _ in counts() }, waitForHints: { hints += 1 })
        defer { list.cancel(); gate.close() }
        let load = Task { await list.load() }
        try await waitForRefreshTest { gate.starts == 1 }
        for _ in 0..<50 { list.invalidate() }
        try await waitForRefreshTest { hints == 1 }
        await Task.yield()
        #expect(gate.starts == 1)
        try gate.finish(page("old"))
        try await waitForRefreshTest { gate.starts == 2 }
        var entered = false
        let manual = Task { entered = true; await list.refresh() }
        defer { manual.cancel() }
        try await waitForRefreshTest { entered }
        try gate.finish(page("latest"))
        await manual.value
        await load.value
        #expect(gate.starts == 2 && list.rows.first?.text == "latest")
    }

    @Test func manualRefreshAbsorbsAHintButDoesNotJoinThePreHintDelta() async throws {
        let gate = Gate()
        let hints = AsyncStream<Void>.makeStream()
        var calls = 0
        var hintEntered = false
        let list = SessionListModel<Row>(id: { $0.id }, removalID: { $0 }, fetch: { _, _, since in
            calls += 1
            if since == nil { return page("initial", revision: "r0", more: false) }
            if calls == 2 { return try await gate.read() }
            return page("latest", revision: "r2", more: false)
        }, counts: { _ in counts() }, waitForHints: {
            hintEntered = true
            for await _ in hints.stream { break }
        })
        defer { list.cancel(); gate.close(); hints.continuation.finish() }
        await list.load()
        let beforeHint = Task { await list.synchronize() }
        try await waitForRefreshTest { gate.starts == 1 }
        list.invalidate()
        try await waitForRefreshTest { hintEntered }
        var manualEntered = false
        let manual = Task { manualEntered = true; await list.refresh() }
        defer { manual.cancel() }
        try await waitForRefreshTest { manualEntered }
        #expect(calls == 2)
        try gate.finish(page("old", revision: "r1", more: false))
        await manual.value
        await beforeHint.value
        #expect(calls == 3 && list.revision == "r2" && list.rows.first?.text == "latest")
    }

    @Test func aLoadJoiningExpiredPaginationDoesNotSkipItsBaselineRecovery() async throws {
        let gate = Gate()
        var calls = 0
        let list = SessionListModel<Row>(id: { $0.id }, removalID: { $0 }, fetch: { _, cursor, _ in
            calls += 1
            if cursor != nil { return try await gate.read() }
            return page(calls == 1 ? "initial" : "recovered", revision: calls == 1 ? "r0" : "r2", more: calls == 1)
        }, counts: { _ in counts() }, waitForHints: {})
        defer { list.cancel(); gate.close() }
        await list.load()
        let next = Task { await list.next() }
        try await waitForRefreshTest { gate.starts == 1 }
        var entered = false
        let joined = Task { entered = true; await list.load() }
        defer { joined.cancel() }
        try await waitForRefreshTest { entered }
        try gate.fail()
        await next.value
        await joined.value
        #expect(calls == 3 && list.loaded && list.revision == "r2")
        #expect(list.rows.first?.text == "recovered" && !list.loading)
    }
}
