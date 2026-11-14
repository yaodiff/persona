import XCTest
@testable import Persona

#if DEBUG
/// Edges of the DEBUG screenshot clock (`-showClockAt`): a value that cannot be read pins nothing, one string pins the
/// same moment however many threads ask, and the Inbox's default clock really is `AppClock` rather than the wall clock.
final class AppClockEdgeTests: XCTestCase {
    private let key = "showClockAt"
    private let defaults = UserDefaults.standard
    private var previous: Any?

    /// 2026-01-01T12:00:00Z, the shared showcase's `now_clock`.
    private let pinned = Date(timeIntervalSince1970: 1_767_268_800)

    override func setUp() {
        super.setUp()
        previous = defaults.object(forKey: key)
        AppClock.resetForTests()
    }

    override func tearDown() {
        defaults.set(previous, forKey: key)
        AppClock.resetForTests()
        previous = nil
        super.tearDown()
    }

    /// A value the parser does not know leaves the clock alone: the screenshot pass then shows the wall clock, not 1970.
    func testAnUnreadableValueDoesNotPinTheClock() {
        for value in ["", "  ", "tomorrow", "2026-01-01", "2026-13-01T12:00:00Z", "2026-01-01T12:00:00"] {
            defaults.set(value, forKey: key)
            AppClock.resetForTests()
            let before = Date()
            let actual = AppClock.now
            let after = Date()
            XCTAssertGreaterThanOrEqual(actual, before, "\(value.isEmpty ? "<empty>" : value)")
            XCTAssertLessThanOrEqual(actual, after, "\(value.isEmpty ? "<empty>" : value)")
        }
    }

    /// A pinned moment is the same moment on every thread: rows read it from the main actor while caches and sockets
    /// may ask elsewhere, and the parse must happen once, not per reader.
    func testEveryThreadReadsTheSamePinnedMoment() {
        defaults.set("2026-01-01T12:00:00Z", forKey: key)
        let lock = NSLock()
        let seen = Seen()
        DispatchQueue.concurrentPerform(iterations: 64) { _ in
            let value = AppClock.now
            lock.withLock { seen.values.append(value) }
        }
        XCTAssertEqual(Set(seen.values), [pinned])
    }

    /// What the threads above collect: a reference type so the concurrent closure mutates one place, behind the lock.
    private final class Seen: @unchecked Sendable {
        var values: [Date] = []
    }

    /// A new launch reads the argument again (what `resetForTests` models): the same process keeps its first value.
    func testResettingModelsANewLaunch() {
        defaults.set("2026-01-01T12:00:00Z", forKey: key)
        XCTAssertEqual(AppClock.now, pinned)
        defaults.set("2026-01-02T12:00:00Z", forKey: key)
        XCTAssertEqual(AppClock.now, pinned)
        AppClock.resetForTests()
        XCTAssertEqual(AppClock.now, pinned.addingTimeInterval(86_400))
        defaults.removeObject(forKey: key)
        AppClock.resetForTests()
        XCTAssertNotEqual(AppClock.now, pinned)
    }

    /// The list's times and the row's state sentence take the pinned clock unless a caller names its own: the showcase's
    /// rows are from the day before the fixture's `now_clock` or that same day, and none is months old.
    func testTheInboxDefaultsFollowThePinnedClock() throws {
        defaults.set("2026-01-01T12:00:00Z", forKey: key)
        let updated = try XCTUnwrap(JSON.parseDate("2026-01-01T11:21:00Z"))
        let yesterday = try XCTUnwrap(JSON.parseDate("2025-12-31T09:00:00Z"))
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!

        XCTAssertEqual(LarkTime.tier(of: updated, calendar: utc), .today)
        XCTAssertEqual(LarkTime.tier(of: yesterday, calendar: utc), .yesterday)
        XCTAssertEqual(LarkTime.short(yesterday, calendar: utc), String(localized: "Yesterday"))
        // An explicit moment still wins, as the pure tests pass one.
        XCTAssertEqual(LarkTime.tier(of: updated, now: pinned.addingTimeInterval(-10 * 86_400), calendar: utc), .older)

        var item = InboxItem(id: "waiting", source: "chat", kind: "group", account: "chat", accountName: "Team chat",
                             title: "Confirm the quote", preview: "Looking", count: 1)
        item.now = "waiting"
        // A check at 09:00 the same day reads as a time alone; a day earlier it grows its date, which is how the pinned
        // `now` shows up in the sentence at all.
        let checkAt = try XCTUnwrap(JSON.parseDate("2026-01-01T09:00:00Z"))
        item.follow = InboxItem.Follow(who: nil, next: nil, checkAt: checkAt)
        let sameDay = try XCTUnwrap(InboxNow.text(for: item, calendar: utc))
        XCTAssertTrue(sameDay.contains(InboxNow.time(checkAt, lang: interfaceTextLocale.identifier, now: pinned, calendar: utc)),
                      sameDay)
        let dayLater = try XCTUnwrap(InboxNow.text(for: item, now: pinned.addingTimeInterval(86_400), calendar: utc))
        XCTAssertNotEqual(sameDay, dayLater)
    }
}
#endif
