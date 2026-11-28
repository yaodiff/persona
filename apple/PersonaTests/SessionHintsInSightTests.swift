import Foundation
import Testing
@testable import Persona
import PersonaWire

/// An Agents list in sight shares the `sessions_list` hints of a short window too (#16758; #9487 out of sight, the web's
/// #9042 / #15910): a busy fleet's burst costs one read when it starts and one when the window ends, not one per hint,
/// and the last change of the burst is always read.
@MainActor
struct SessionHintsInSightTests {
    private typealias Page = SessionListPage<Session>

    /// The hub's one row, titled with the revision it was read at.
    private static func row(_ title: String) -> Session {
        let json = """
        {"machine_id":"m","session_id":"s","title":"\(title)","cwd":"/tmp/p","status":"working","state":"working"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    /// A clock the test moves. A window's `sleep` ends when the test moves the clock past its deadline, which then reads
    /// as the time it woke at.
    @MainActor private final class ReplayClock {
        private(set) var now = ContinuousClock.now
        private var sleepers: [(deadline: ContinuousClock.Instant, waiter: CheckedContinuation<Void, any Error>)] = []
        private(set) var waits: [Duration] = []

        func window(_ length: @escaping @MainActor () -> Duration) -> HintWindow {
            HintWindow(length: length, now: { [unowned self] in now }, sleep: { [unowned self] wait in
                waits.append(wait)
                let deadline = now.advanced(by: wait)
                try await withCheckedThrowingContinuation { sleepers.append((deadline, $0)) }
            })
        }

        /// Moves the clock by `step`, waking each sleeper at its own deadline on the way.
        func advance(by step: Duration) async {
            let end = now.advanced(by: step)
            while let next = sleepers.indices.min(by: { sleepers[$0].deadline < sleepers[$1].deadline }),
                  sleepers[next].deadline <= end {
                let sleeper = sleepers.remove(at: next)
                now = sleeper.deadline
                sleeper.waiter.resume()
                await SessionHintsInSightTests.drain()
            }
            now = end
        }

        func cancel() { let all = sleepers; sleepers = []; for s in all { s.waiter.resume(throwing: CancellationError()) } }
    }

    /// Lets the main actor's hint and read tasks run until nothing more is ready.
    private static func drain() async { for _ in 0..<200 { await Task.yield() } }
    private func settle() async { await Self.drain() }

    /// A fake hub: its revision moves with every hint, and a read answers what it holds at that moment.
    @MainActor private final class Hub {
        var revision = "h0"
        private(set) var reads = 0
        func list(_ windowed: HintWindow) -> SessionListModel<Session> {
            SessionListModel<Session>(id: { $0.id }, removalID: { _ in nil }, fetch: { [unowned self] _, _, _ in
                reads += 1
                return Page(sessions: [SessionHintsInSightTests.row(revision)], remove: [], hasMore: false, nextCursor: nil,
                            revision: revision, total: 1)
            }, counts: { _ in
                SessionListCounts(total: 1, machine: [:], project: [:], group: [:], state: [:], revision: "counts")
            }, waitForHints: {}, hintWindow: windowed)
        }
    }

    /// The regression (#16758): with an Agents list in sight, ten hints a tenth of a second apart read the list once at
    /// the first and once at the window's end, not ten times, and the list ends at the last change.
    @Test func aListInSightSharesABurstsHintsAndEndsCurrent() async throws {
        let clock = ReplayClock()
        let hub = Hub()
        let sessionLists = RuntimeSessionLists()
        sessionLists.makeHintWindow = { clock.window($0) }
        sessionLists.listInSight(UUID(), true)
        let list = hub.list(sessionLists.hintWindow())
        sessionLists.setForTests(list, group: "hub")
        defer { list.cancel(); sessionLists.forget(); clock.cancel() }
        await list.load()
        #expect(hub.reads == 1)

        for i in 1...10 {
            if i > 1 { await clock.advance(by: .milliseconds(100)) }
            hub.revision = "h\(i)"
            sessionLists.invalidate("hub", revision: hub.revision)
            await settle()
        }
        #expect(hub.reads == 2, "the burst's first hint reads at once and the rest wait for the window's end")
        await clock.advance(by: HintWindow.inSight)
        #expect(hub.reads == 3, "one trailing read for the rest of the burst")
        #expect(list.revision == "h10" && list.rows.first?.title == "h10", "the last change is read")
        #expect(clock.waits.allSatisfy { $0 <= HintWindow.inSight }, "nothing in sight waits longer than its window")

        await clock.advance(by: .seconds(5))
        #expect(hub.reads == 3, "a quiet hub reads nothing more")
    }

    /// Out of sight the window stays the longer one, and a list coming into sight lets what waits read at once.
    @Test func outOfSightKeepsTheLongerWindowAndComingIntoSightReleasesIt() async throws {
        let clock = ReplayClock()
        let hub = Hub()
        let sessionLists = RuntimeSessionLists()
        sessionLists.makeHintWindow = { clock.window($0) }
        let list = hub.list(sessionLists.hintWindow())
        sessionLists.setForTests(list, group: "hub")
        defer { list.cancel(); sessionLists.forget(); clock.cancel() }
        await list.load()

        hub.revision = "h1"
        sessionLists.invalidate("hub", revision: hub.revision)
        await settle()
        #expect(hub.reads == 2)
        await clock.advance(by: .milliseconds(100))
        hub.revision = "h2"
        sessionLists.invalidate("hub", revision: hub.revision)
        await settle()
        #expect(clock.waits.contains(HintWindow.length - .milliseconds(100)), "out of sight a hint waits for the 2 s window")
        await clock.advance(by: HintWindow.inSight)
        #expect(hub.reads == 2, "still waiting past the in-sight window")

        sessionLists.listInSight(UUID(), true)
        await settle()
        #expect(hub.reads == 3 && list.rows.first?.title == "h2", "coming into sight reads what waits at once")
    }

    /// Reads a minute of hints `gap` apart costs, the window `length` long; `length` zero is the in-sight list before
    /// #16758. The before / after table in the PR comes from these numbers.
    private func readsPerMinute(gap: Duration, length: Duration) async -> (reads: Int, last: String?) {
        let clock = ReplayClock()
        let hub = Hub()
        let list = hub.list(clock.window { length })
        defer { list.cancel(); clock.cancel() }
        await list.load()
        let start = hub.reads
        var elapsed = Duration.zero
        var i = 0
        while elapsed < .seconds(60) {
            i += 1
            hub.revision = "h\(i)"
            list.invalidate(revision: hub.revision)
            await settle()
            await clock.advance(by: gap)
            elapsed += gap
        }
        await clock.advance(by: .seconds(3))
        return (hub.reads - start, list.rows.first?.title)
    }

    @Test(arguments: [250, 500, 1000, 2400])
    func aMinuteOfHintsInSightReadsAtMostOnceASecond(gapMilliseconds: Int) async throws {
        let gap = Duration.milliseconds(gapMilliseconds)
        let hints = Int((Duration.seconds(60) / gap).rounded(.up))
        let before = await readsPerMinute(gap: gap, length: .zero)
        let after = await readsPerMinute(gap: gap, length: HintWindow.inSight)
        print("hint reads in a minute, hints every \(gapMilliseconds) ms (\(hints) hints): before \(before.reads), after \(after.reads)")
        #expect(before.reads == hints, "before, an Agents list in sight read every hint")
        #expect(after.reads <= 61, "in sight, at most one read a second")
        #expect(after.reads <= before.reads)
        if gap >= HintWindow.inSight { #expect(after.reads == before.reads, "hints a window apart or more each read at once") }
        #expect(after.last == "h\(hints)", "the last change is read")
    }
}
