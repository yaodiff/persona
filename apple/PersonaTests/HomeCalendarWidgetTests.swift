import Foundation
import Testing
import WidgetKit
@testable import Persona

/// The Calendar widget's week and month (docs/apple/home-widgets.md「Calendar: Today, Week and Month」): the days the
/// app writes, the view and the day each widget shows, and today's events after midnight.
@MainActor
struct HomeCalendarWidgetTests {
    /// A Monday-first Gregorian calendar in UTC, so the dates below mean the same everywhere.
    private static let calendar: Calendar = {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC") ?? .current
        c.firstWeekday = 2
        return c
    }()

    private func at(_ y: Int, _ m: Int, _ d: Int, _ h: Int = 0, _ min: Int = 0, calendar: Calendar = Self.calendar) -> Date {
        calendar.date(from: DateComponents(year: y, month: m, day: d, hour: h, minute: min)) ?? .distantPast
    }

    private func event(_ title: String, _ start: Date, minutes: Double, allDay: Bool = false) -> HomeSnapshot.Event {
        .init(title: title, start: start.timeIntervalSince1970, end: start.timeIntervalSince1970 + minutes * 60, allDay: allDay)
    }

    @Test func dayKeysNameThePhonesDay() {
        let c = Self.calendar
        #expect(HomeDays.key(at(2026, 11, 18, 23, 59), calendar: c) == "2026-11-18")
        #expect(HomeDays.key(at(2026, 1, 5), calendar: c) == "2026-01-05")
        #expect(HomeDays.date("2026-11-18", calendar: c) == at(2026, 11, 18))
        #expect(HomeDays.date("not a day", calendar: c) == nil)
    }

    /// This month's grid, whole weeks from the first weekday, and the two weeks after today, whichever ends later.
    @Test func theRangeCoversTheMonthAndTwoWeeksAhead() {
        let c = Self.calendar
        let mid = HomeDays.range(around: at(2026, 11, 18, 13, 20), calendar: c)
        #expect(mid.start == at(2026, 10, 26)) // the Monday before November 1st, a Sunday
        #expect(mid.end == at(2026, 12, 7)) // the end of the grid's last week (Sunday December 6th)
        let late = HomeDays.range(around: at(2026, 11, 30, 9), calendar: c)
        #expect(late.end == at(2026, 12, 15)) // two weeks after November 30th, past the grid
        #expect(HomeDays.month(at(2026, 11, 18), calendar: c).count == 6)
        #expect(HomeDays.month(at(2026, 11, 18), calendar: c).first?.last == at(2026, 11, 1))
        #expect(HomeDays.week(at(2026, 11, 18), calendar: c).map { HomeDays.key($0, calendar: c) }.first == "2026-11-16")
    }

    /// An event counts on each day it covers; a day keeps its first few, all-day ones first, and how many it has.
    @Test func eventsLandOnEveryDayTheyCover() {
        let c = Self.calendar
        let range = HomeDays.range(around: at(2026, 11, 18), calendar: c)
        let long = String(repeating: "x", count: HomeDays.titleCap + 20)
        var events = [
            event("Trip", at(2026, 11, 27), minutes: 3 * 24 * 60, allDay: true),
            event("Late show", at(2026, 11, 20, 22), minutes: 4 * 60),
            event("Reminder", at(2026, 11, 21, 8), minutes: 0),
            event(long, at(2026, 11, 19, 7), minutes: 30),
            event("Long ago", at(2026, 9, 1), minutes: 60),
        ]
        events += (0..<6).map { event("Meeting \($0)", at(2026, 11, 24, 9 + $0), minutes: 30) }
        events.append(event("Day off", at(2026, 11, 24), minutes: 24 * 60, allDay: true))
        let days = HomeDays.build(events, range: range, calendar: c)
        #expect(days.from == "2026-10-26" && days.to == "2026-12-06")
        #expect(["2026-11-27", "2026-11-28", "2026-11-29"].allSatisfy { days.byDay[$0]?.count == 1 })
        #expect(days.byDay["2026-11-30"] == nil) // the trip ends at midnight
        #expect(days.byDay["2026-11-20"]?.count == 1 && days.byDay["2026-11-21"]?.count == 2) // overnight, and a moment
        #expect(days.byDay["2026-11-19"]?.events.first?.title.count == HomeDays.titleCap)
        let busy = days.byDay["2026-11-24"]
        #expect(busy?.count == 7 && busy?.events.count == HomeDays.perDay)
        #expect(busy?.events.first?.title == "Day off")
        #expect(days.byDay.keys.allSatisfy(days.covers))
        #expect(!days.covers("2026-12-07"))
    }

    /// Each widget's place is its size and its Edit Widget › View; the switch's view stays there.
    @Test func theViewIsKeptPerSizeAndView() {
        let now = at(2026, 11, 18, 13, calendar: .current)
        var state = HomeCalendarState()
        #expect(HomeCalendarState.slot(.systemMedium, .today) != HomeCalendarState.slot(.systemLarge, .today))
        #expect(HomeCalendarState.slot(.systemMedium, .today) != HomeCalendarState.slot(.systemMedium, .week))
        let medium = HomeCalendarState.slot(.systemMedium, .today)
        #expect(HomeCalendarState.shown(state.slots[medium], configured: .today, at: now).view == .today)
        state.show(.month, in: medium)
        #expect(HomeCalendarState.shown(state.slots[medium], configured: .today, at: now).view == .month)
        let large = HomeCalendarState.slot(.systemLarge, .week)
        #expect(HomeCalendarState.shown(state.slots[large], configured: .week, at: now).view == .week)
    }

    /// A picked day lasts ten minutes and never past midnight; picking today or the same day again goes back to today.
    @Test func aPickedDayGoesBackToToday() {
        let cal = Calendar.current
        let now = at(2026, 11, 18, 13, calendar: cal)
        let tomorrow = HomeDays.key(at(2026, 11, 19, calendar: cal), calendar: cal)
        var state = HomeCalendarState()
        state.pick(tomorrow, in: "medium.week", at: now, calendar: cal)
        let slot = state.slots["medium.week"]
        #expect(HomeCalendarState.shown(slot, configured: .week, at: now.addingTimeInterval(5 * 60), calendar: cal).day == at(2026, 11, 19, calendar: cal))
        #expect(HomeCalendarState.shown(slot, configured: .week, at: now.addingTimeInterval(11 * 60), calendar: cal).day == at(2026, 11, 18, calendar: cal))
        #expect(HomeCalendarState.pickEnds(slot, after: now) == now.addingTimeInterval(HomeCalendarState.pickLasts))
        // Read at the moment of the pick, even when the stored time comes back a hair later than the clock.
        #expect(HomeCalendarState.shown(slot, configured: .week, at: now.addingTimeInterval(-0.001), calendar: cal).day == at(2026, 11, 19, calendar: cal))
        #expect(HomeCalendarState.pickEnds(slot, after: now.addingTimeInterval(HomeCalendarState.pickLasts)) == nil)

        let late = at(2026, 11, 18, 23, 55, calendar: cal)
        state.pick(tomorrow, in: "large.month", at: late, calendar: cal)
        let afterMidnight = at(2026, 11, 19, 0, 1, calendar: cal)
        #expect(HomeCalendarState.shown(state.slots["large.month"], configured: .month, at: afterMidnight, calendar: cal).day == at(2026, 11, 19, calendar: cal))
        #expect(HomeCalendarState.picked(state.slots["large.month"], at: afterMidnight, calendar: cal) == nil)

        state.pick(tomorrow, in: "medium.week", at: now.addingTimeInterval(60), calendar: cal)
        #expect(state.slots["medium.week"]?.day == nil) // the same day again
        state.pick(tomorrow, in: "medium.week", at: now, calendar: cal)
        state.pick(HomeDays.key(now, calendar: cal), in: "medium.week", at: now, calendar: cal)
        #expect(state.slots["medium.week"]?.day == nil) // today
    }

    @Test func theStateIsWrittenAndReadBack() throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "home-calendar-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = HomeCalendarStore(root: dir)
        #expect(store.savedState() == HomeCalendarState())
        store.update { $0.show(.week, in: "large.today") }
        #expect(store.savedState().slots["large.today"]?.view == .week)
        #expect(HomeCalendarStore(root: nil).savedState() == HomeCalendarState())
    }

    /// The rest of today: on today, not over yet, all-day ones first; yesterday's never.
    @Test func theRestOfTodayIsWhatIsLeft() {
        let c = Self.calendar
        let now = at(2026, 11, 18, 13, 20)
        let rest = HomeToday.rest([
            event("Over", at(2026, 11, 18, 9), minutes: 30),
            event("Later", at(2026, 11, 18, 16), minutes: 30),
            event("Now", at(2026, 11, 18, 13), minutes: 60),
            event("Birthday", at(2026, 11, 18), minutes: 24 * 60, allDay: true),
            event("Yesterday", at(2026, 11, 17), minutes: 24 * 60, allDay: true),
            event("Tomorrow", at(2026, 11, 19, 9), minutes: 30),
        ], now: now, calendar: c)
        #expect(rest.map(\.title) == ["Birthday", "Now", "Later"])
        let e = event("Meeting", now, minutes: 30)
        let days = HomeDays.build([e], range: HomeDays.range(around: now, calendar: c), calendar: c)
        #expect(HomeToday.build(calendar: [e], days: days, hub: nil, now: now)?.days == days)
        #expect(HomeToday.build(calendar: nil, days: days, hub: [e], now: now)?.days == nil) // the hub's are today's alone
    }

    /// After midnight, before the app writes again, today's events come from the days; without them, none (yesterday's
    /// list is not today's).
    @Test func afterMidnightTodayComesFromTheDays() {
        let cal = Calendar.current
        let written = at(2026, 11, 18, 23, 0, calendar: cal)
        let morning = at(2026, 11, 19, 8, 0, calendar: cal)
        let yesterdays = event("Yesterday's all day", at(2026, 11, 18, calendar: cal), minutes: 24 * 60, allDay: true)
        let todays = event("Dentist", at(2026, 11, 19, 9, calendar: cal), minutes: 30)
        var s = HomeSnapshot(at: written.timeIntervalSince1970, paired: true)
        s.today = .init(events: [yesterdays], source: .calendar,
                        days: HomeDays.build([yesterdays, todays], range: HomeDays.range(around: written, calendar: cal), calendar: cal))
        #expect(HomeContext(snapshot: s, date: morning).events.map(\.title) == ["Dentist"])
        #expect(HomeContext(snapshot: s, date: written).events.map(\.title) == ["Yesterday's all day"])
        s.today?.days = nil
        #expect(HomeContext(snapshot: s, date: morning).events.isEmpty)
    }

    /// What a day shows: its events and how many more; nil past the snapshot's days; today's dot counts the ones over.
    @Test func aDayIsKnownOnlyInsideTheRange() {
        let cal = Calendar.current
        let now = at(2026, 11, 18, 13, 20, calendar: cal)
        let over = event("Stand-up", at(2026, 11, 18, 9, calendar: cal), minutes: 15)
        let many = (0..<6).map { event("Meeting \($0)", at(2026, 11, 20, 9 + $0, calendar: cal), minutes: 30) }
        var s = HomeSnapshot(at: now.timeIntervalSince1970, paired: true)
        s.today = .init(events: [], source: .calendar,
                        days: HomeDays.build([over] + many, range: HomeDays.range(around: now, calendar: cal), calendar: cal))
        let c = HomeContext(snapshot: s, date: now)
        #expect(c.day(at(2026, 11, 20, calendar: cal))?.events.count == HomeDays.perDay)
        #expect(c.day(at(2026, 11, 20, calendar: cal))?.more == 6 - HomeDays.perDay)
        #expect(c.day(at(2026, 11, 21, calendar: cal))?.events.isEmpty == true)
        #expect(c.day(at(2027, 2, 1, calendar: cal)) == nil)
        #expect(c.hasEvents(now) && c.day(now)?.events.isEmpty == true)
        #expect(!c.hasEvents(at(2026, 11, 21, calendar: cal)))
    }
}
