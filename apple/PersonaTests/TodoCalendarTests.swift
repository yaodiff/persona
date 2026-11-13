import Foundation
import Testing
@testable import Persona

/// The to-dos' calendar (#2029): which to-dos it shows and on what day, the days of a day / week / month, side by side
/// where blocks overlap, what a month's day fits, where a tap on the axis lands, where a dragged to-do lands and what
/// moving it saves (#2045), and the headings.
struct TodoCalendarTests {
    /// Monday-first, in a fixed time zone: Monday 2026-11-09 is "today" in these tests.
    private var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "Asia/Shanghai")!
        c.firstWeekday = 2
        c.locale = Locale(identifier: "en_US")
        return c
    }

    private func at(_ day: Int, _ hour: Int, _ minute: Int = 0) -> Date {
        cal.date(from: DateComponents(year: 2026, month: 11, day: day, hour: hour, minute: minute))!
    }

    private func entry(_ id: String, _ start: Date, allDay: Bool = false, done: Bool = false) -> CalendarEntry {
        CalendarEntry(source: .todo(id), title: id, start: start, end: nil, allDay: allDay, state: .notStarted,
                      stateLabel: "", done: done, overdue: false, highPriority: false, reminding: false)
    }

    @MainActor @Test func toDosBecomeEntries() {
        let now = at(9, 14, 20)
        let timed = TodoItem(id: "timed", title: "Ship", dueAt: at(9, 18), remindAt: at(9, 17), priority: "high")
        let day = TodoItem(id: "day", title: "Rent", dueDate: "2026-11-11")
        let late = TodoItem(id: "late", title: "HEIC", dueAt: at(9, 11, 30))
        let done = TodoItem(id: "done", title: "PR", doneAt: at(9, 10), dueAt: at(9, 10), priority: "high")
        let pushed = TodoItem(id: "pushed", title: "Pushed", dueAt: at(10, 9), remindAt: at(9, 8), remindedAt: at(9, 8))
        let none = TodoItem(id: "none", title: "Someday")

        let e = TodoCalendar.entry(timed, state: .notStarted, calendar: cal, now: now)!
        #expect(!e.allDay && e.start == at(9, 18) && e.highPriority && e.reminding && !e.overdue && e.end == nil)
        let d = TodoCalendar.entry(day, state: .notStarted, calendar: cal, now: now)!
        #expect(d.allDay && d.start == at(11, 0))
        #expect(TodoCalendar.entry(late, state: .needsYou, calendar: cal, now: now)!.overdue)
        let gone = TodoCalendar.entry(done, state: .done, calendar: cal, now: now)!
        #expect(gone.done && !gone.overdue && !gone.highPriority)
        #expect(!TodoCalendar.entry(pushed, state: .notStarted, calendar: cal, now: now)!.reminding)
        #expect(TodoCalendar.entry(none, state: .notStarted, calendar: cal, now: now) == nil)

        // The source keeps what falls in the range, and leaves done ones out when asked.
        let week = TodoCalendar.range(.week, anchor: now, calendar: cal)
        let all = [timed, day, late, done, pushed, none]
        let shown = TodoEntrySource(todos: all, state: { _ in .notStarted }, showDone: true).entries(in: week, calendar: cal, now: now)
        #expect(Set(shown.map(\.id)) == ["todo:timed", "todo:day", "todo:late", "todo:done", "todo:pushed"])
        let open = TodoEntrySource(todos: all, state: { _ in .notStarted }, showDone: false).entries(in: week, calendar: cal, now: now)
        #expect(!open.contains { $0.id == "todo:done" })
        let nextWeek = TodoCalendar.range(.week, anchor: at(16, 9), calendar: cal)
        #expect(TodoEntrySource(todos: all, state: { _ in .notStarted }, showDone: true).entries(in: nextWeek, calendar: cal, now: now).isEmpty)

        // Those without a due date are counted under the calendar; done ones are not.
        let doneUndated = TodoItem(id: "x", title: "Old", doneAt: now)
        #expect(TodoCalendar.undatedCount(all + [doneUndated], calendar: cal) == 1)
    }

    @Test func daysOfEachMode() {
        let anchor = at(11, 15)
        #expect(TodoCalendar.days(.day, anchor: anchor, calendar: cal) == [at(11, 0)])
        let week = TodoCalendar.days(.week, anchor: anchor, calendar: cal)
        #expect(week.count == 7 && week.first == at(9, 0) && week.last == at(15, 0))
        // November 2026 starts on a Sunday: Monday-first, its weeks run from October 26 to December 6.
        let month = TodoCalendar.days(.month, anchor: anchor, calendar: cal)
        #expect(month.count == 42)
        #expect(month.first == cal.date(from: DateComponents(year: 2026, month: 10, day: 26)))
        #expect(month.last == cal.date(from: DateComponents(year: 2026, month: 12, day: 6)))
        // A Sunday-first region starts the same week a day earlier.
        var sunday = cal
        sunday.firstWeekday = 1
        #expect(TodoCalendar.days(.week, anchor: anchor, calendar: sunday).first == at(8, 0))
        // Simplified Chinese and the Chinese mainland start on Monday though Apple's data says Sunday; a first weekday
        // the user chose wins; elsewhere it is the region's.
        func first(_ id: String, chosen: Bool = false) -> Int {
            var c = Calendar(identifier: .gregorian)
            c.locale = Locale(identifier: id)
            return TodoCalendar.firstWeekday(locale: Locale(identifier: id), calendar: c, chosen: chosen)
        }
        #expect(first("zh_CN") == 2 && first("zh-Hans_US") == 2 && first("en_CN") == 2)
        #expect(first("zh_TW") == 1 && first("en_US") == 1 && first("ja_JP") == 1 && first("de_DE") == 2)
        #expect(first("zh_CN@fw=sun") == 1 && first("en_US@fw=mon") == 2)
        #expect(first("zh_CN", chosen: true) == 1)
        // A month that fits in five weeks gets five.
        let february = cal.date(from: DateComponents(year: 2027, month: 2, day: 10))!
        #expect(TodoCalendar.days(.month, anchor: february, calendar: cal).count == 28)

        let range = TodoCalendar.range(.week, anchor: anchor, calendar: cal)
        #expect(range.start == at(9, 0) && range.end == at(16, 0))
        #expect(TodoCalendar.step(anchor, mode: .day, by: 1, calendar: cal) == at(12, 15))
        #expect(TodoCalendar.step(anchor, mode: .week, by: -1, calendar: cal) == at(4, 15))
        #expect(TodoCalendar.step(anchor, mode: .month, by: 1, calendar: cal) == cal.date(from: DateComponents(year: 2026, month: 12, day: 11, hour: 15)))
    }

    @Test func aDaysEntriesInDrawingOrder() {
        let entries = [entry("late", at(9, 18)), entry("rent", at(9, 0), allDay: true), entry("done", at(9, 10), done: true),
                       entry("open", at(9, 10)), entry("tomorrow", at(10, 9))]
        #expect(TodoCalendar.on(at(9, 12), entries, calendar: cal).map(\.title) == ["rent", "open", "done", "late"])
    }

    @Test func overlappingBlocksSideBySide() {
        // A block is 50 minutes: two at 18:00 and one at 18:30 share a cluster of three lanes; 20:00 is on its own.
        let a = entry("a", at(9, 18)), b = entry("b", at(9, 18)), c = entry("c", at(9, 18, 30)), d = entry("d", at(9, 20))
        let placed = TodoCalendar.lanes([d, c, b, a], minutes: 50)
        let byID = Dictionary(uniqueKeysWithValues: placed.map { ($0.entry.title, $0) })
        #expect(byID["a"]?.lane == 0 && byID["b"]?.lane == 1 && byID["c"]?.lane == 2)
        #expect(byID["a"]?.lanes == 3 && byID["c"]?.lanes == 3)
        #expect(byID["d"]?.lane == 0 && byID["d"]?.lanes == 1)
        // 18:50 starts where 18:00's block ends: a lane is free again.
        let e = entry("e", at(9, 18, 50))
        let again = Dictionary(uniqueKeysWithValues: TodoCalendar.lanes([a, e], minutes: 50).map { ($0.entry.title, $0) })
        #expect(again["e"]?.lane == 0 && again["e"]?.lanes == 1)

        #expect(TodoCalendar.clusters([a, b, c, d], minutes: 50).map { $0.map(\.title) } == [["a", "b", "c"], ["d"]])
        // An entry with an end (a calendar event, later) is as long as it lasts.
        var long = entry("long", at(9, 9))
        long.end = at(9, 12)
        #expect(TodoCalendar.clusters([long, entry("x", at(9, 11))], minutes: 50).count == 1)
    }

    @Test func aMonthDayFits() {
        let five = (0..<5).map { entry("t\($0)", at(20, 9 + $0)) }
        let fit = TodoCalendar.fit(five, lines: 3)
        #expect(fit.shown.count == 2 && fit.more == 3)
        #expect(TodoCalendar.fit(Array(five.prefix(3)), lines: 3).more == 0)
        #expect(TodoCalendar.fit([], lines: 3).shown.isEmpty)
    }

    @Test func aTapOnTheAxis() {
        #expect(TodoCalendar.slot(day: at(9, 0), minutes: 15 * 60 + 44, snap: 30, calendar: cal) == at(9, 15, 30))
        #expect(TodoCalendar.slot(day: at(9, 0), minutes: -5, snap: 30, calendar: cal) == at(9, 0))
        #expect(TodoCalendar.slot(day: at(9, 0), minutes: 24 * 60 + 10, snap: 30, calendar: cal) == at(9, 23, 30))
        #expect(TodoCalendar.minutes(at(9, 14, 20), in: at(9, 0), calendar: cal) == 14 * 60 + 20)

        // The axis opens where the moment and the first to-do are in sight, else the morning.
        #expect(TodoCalendar.firstHour(days: [at(9, 0)], timed: [], now: at(9, 14, 20), calendar: cal) == 12)
        #expect(TodoCalendar.firstHour(days: [at(9, 0)], timed: [], now: at(9, 1), calendar: cal) == 0)
        #expect(TodoCalendar.firstHour(days: [at(9, 0)], timed: [entry("x", at(9, 10))], now: at(9, 14, 20), calendar: cal) == 9)
        #expect(TodoCalendar.firstHour(days: [at(10, 0)], timed: [entry("x", at(10, 9, 30))], now: at(9, 14), calendar: cal) == 8)
        let week = TodoCalendar.days(.week, anchor: at(11, 0), calendar: cal)
        #expect(TodoCalendar.firstHour(days: week, timed: [entry("x", at(12, 7)), entry("y", at(20, 3))], now: at(1, 9), calendar: cal) == 6)
        #expect(TodoCalendar.firstHour(days: [at(11, 0)], timed: [], now: at(9, 14), calendar: cal) == TodoCalendar.morning)
    }

    // MARK: Moving one (#2045)

    /// The axis: the block's top edge to the nearest quarter hour, within the day (the web's `dropOnAxis`).
    @Test func aDropOnTheAxis() {
        #expect(TodoCalendar.dropOnAxis(day: at(10, 9), minutes: 14 * 60 + 7, calendar: cal) == CalendarDrop(day: at(10, 0), at: at(10, 14)))
        #expect(TodoCalendar.dropOnAxis(day: at(10, 0), minutes: 14 * 60 + 8, calendar: cal).at == at(10, 14, 15))
        #expect(TodoCalendar.dropOnAxis(day: at(10, 0), minutes: 14 * 60 + 7.5, calendar: cal).at == at(10, 14, 15))
        #expect(TodoCalendar.dropOnAxis(day: at(10, 0), minutes: -40, calendar: cal).at == at(10, 0))
        // never past the day's last quarter hour
        #expect(TodoCalendar.dropOnAxis(day: at(10, 0), minutes: 24 * 60 + 30, calendar: cal).at == at(10, 23, 45))
    }

    /// A whole day (the month, the All Day row): the same time there, or all day.
    @Test func aDropOnADay() {
        let timed = entry("timed", at(9, 14, 30))
        let allDay = entry("rent", at(9, 0), allDay: true)
        #expect(TodoCalendar.dropOnDay(timed, day: at(12, 8), calendar: cal) == CalendarDrop(day: at(12, 0), at: at(12, 14, 30)))
        #expect(TodoCalendar.dropOnDay(timed, day: at(12, 8), allDay: true, calendar: cal) == CalendarDrop(day: at(12, 0), at: nil))
        #expect(TodoCalendar.dropOnDay(allDay, day: at(13, 0), calendar: cal) == CalendarDrop(day: at(13, 0), at: nil))
        // the time keeps its seconds, as on the web
        let odd = entry("odd", at(9, 14).addingTimeInterval(20))
        #expect(TodoCalendar.dropOnDay(odd, day: at(11, 0), calendar: cal).at == at(11, 14).addingTimeInterval(20))
    }

    /// What is saved: nothing for a drop where it already is; `due_at` or `due_date`; what Undo puts back; the to-do
    /// drawn at once with its other due field cleared; how it is drawn where it would land.
    @MainActor @Test func whatAMoveSaves() {
        let timed = entry("timed", at(9, 14))
        let allDay = entry("rent", at(9, 0), allDay: true)
        #expect(!TodoCalendar.moves(timed, to: CalendarDrop(day: at(9, 0), at: at(9, 14)), calendar: cal))
        #expect(TodoCalendar.moves(timed, to: CalendarDrop(day: at(9, 0), at: at(9, 14, 15)), calendar: cal))
        #expect(TodoCalendar.moves(timed, to: CalendarDrop(day: at(9, 0), at: nil), calendar: cal))
        #expect(!TodoCalendar.moves(allDay, to: CalendarDrop(day: at(9, 0), at: nil), calendar: cal))
        #expect(TodoCalendar.moves(allDay, to: CalendarDrop(day: at(10, 0), at: nil), calendar: cal))
        #expect(TodoCalendar.moves(allDay, to: CalendarDrop(day: at(9, 0), at: at(9, 9)), calendar: cal))

        #expect(TodoCalendar.due(CalendarDrop(day: at(12, 0), at: at(12, 9)), calendar: cal) == .at(at(12, 9)))
        #expect(TodoCalendar.due(CalendarDrop(day: at(12, 0), at: nil), calendar: cal) == .day("2026-11-12"))
        #expect(TodosLogic.dayKey(at(1, 0), calendar: cal) == "2026-11-01")

        let atMoment = TodoItem(id: "a", title: "A", dueAt: at(9, 14))
        let onDay = TodoItem(id: "d", title: "D", dueDate: "2026-11-11")
        #expect(TodoCalendar.due(of: atMoment) == .at(at(9, 14)))
        #expect(TodoCalendar.due(of: onDay) == .day("2026-11-11"))
        #expect(TodoCalendar.due(of: TodoItem(id: "n", title: "N")) == .none)
        let toDay = TodoCalendar.moved(atMoment, to: .day("2026-11-12"))
        #expect(toDay.dueAt == nil && toDay.dueDate == "2026-11-12")
        let toMoment = TodoCalendar.moved(onDay, to: .at(at(12, 9)))
        #expect(toMoment.dueAt == at(12, 9) && toMoment.dueDate == nil && toMoment.title == "D")

        // A late one dragged to tomorrow is no longer drawn late there; dropped on an earlier hour today it is.
        var late = entry("late", at(9, 11))
        late.overdue = true
        let now = at(9, 14)
        let tomorrow = TodoCalendar.landed(late, at: CalendarDrop(day: at(10, 0), at: at(10, 9)), calendar: cal, now: now)
        #expect(tomorrow.start == at(10, 9) && !tomorrow.allDay && !tomorrow.overdue)
        let today = TodoCalendar.landed(entry("x", at(9, 16)), at: CalendarDrop(day: at(9, 0), at: at(9, 10)), calendar: cal, now: now)
        #expect(today.overdue)
        let allToday = TodoCalendar.landed(late, at: CalendarDrop(day: at(9, 0), at: nil), calendar: cal, now: now)
        #expect(allToday.allDay && allToday.start == at(9, 0) && !allToday.overdue)
        var done = entry("done", at(9, 11), done: true)
        done.overdue = false
        #expect(!TodoCalendar.landed(done, at: CalendarDrop(day: at(8, 0), at: at(8, 9)), calendar: cal, now: now).overdue)
    }

    /// Where on the timeline a finger is: the column under it (the nearest over the hours), on the axis the block's
    /// top edge `grab` above it with the scroll counted, the All Day row above the axis, nowhere above or below.
    @Test func whereOnTheTimelineAFingerIs() {
        // 60 pt of hours and seven 40 pt columns; the row from y 50 to 80, the axis in sight from 81 to 581, scrolled
        // 8 hours down (52 pt an hour).
        let map = TimelineDropMap(width: 340, gutter: 60, days: 7, allDay: CGRect(x: 0, y: 50, width: 340, height: 30),
                                  axis: CGRect(x: 0, y: 81, width: 340, height: 500), scrolled: 8 * 52, hour: 52)
        #expect(map.day(10) == 0 && map.day(61) == 0 && map.day(101) == 1 && map.day(339) == 6 && map.day(500) == 6)
        // a finger 20 pt into the block, 26 pt (half an hour) below the axis's top: the block's top is at 8:30 − 20 pt
        // (the minutes as a typed constant: inside `#expect`, Xcode 26.0 reads the literals as Int)
        let top: Double = (8 * 52 + 26 - 20) / 52 * 60
        #expect(map.spot(CGPoint(x: 150, y: 81 + 26), grab: 20) == .axis(day: 2, minutes: top))
        #expect(map.spot(CGPoint(x: 150, y: 60), grab: 20) == .allDay(day: 2))
        #expect(map.spot(CGPoint(x: 150, y: 40), grab: 20) == nil)
        #expect(map.spot(CGPoint(x: 150, y: 600), grab: 20) == nil)
        // the axis scrolls near its edges, and on below it; not over the row
        #expect(map.autoscroll(CGPoint(x: 150, y: 90), edge: 44) == -1)
        #expect(map.autoscroll(CGPoint(x: 150, y: 570), edge: 44) == 1)
        #expect(map.autoscroll(CGPoint(x: 150, y: 640), edge: 44) == 1)
        #expect(map.autoscroll(CGPoint(x: 150, y: 300), edge: 44) == 0)
        #expect(map.autoscroll(CGPoint(x: 150, y: 60), edge: 44) == 0)
    }

    /// The month: the day under the finger with the scroll counted, none off the grid.
    @Test func whichDayOfTheMonthAFingerIsOver() {
        let map = MonthDropMap(grid: CGRect(x: 0, y: 100, width: 350, height: 500), scrolled: 0, cell: 100, rows: 5)
        #expect(map.day(CGPoint(x: 10, y: 110)) == 0)
        #expect(map.day(CGPoint(x: 349, y: 110)) == 6)
        #expect(map.day(CGPoint(x: 120, y: 350)) == 2 * 7 + 2)
        #expect(map.day(CGPoint(x: 120, y: 90)) == nil)
        var scrolled = map
        scrolled.scrolled = 100
        #expect(scrolled.day(CGPoint(x: 120, y: 350)) == 3 * 7 + 2)
        #expect(scrolled.day(CGPoint(x: 120, y: 590)) == nil) // past the last week
        // five weeks of 100 fit in 500: nothing to scroll
        #expect(map.autoscroll(CGPoint(x: 120, y: 595), edge: 44) == 0)
        // six don't (a phone on its side): down near the bottom or below it, up near the top or above it
        var tall = map
        tall.rows = 6
        #expect(tall.scrollRange == 100)
        #expect(tall.autoscroll(CGPoint(x: 120, y: 580), edge: 44) == 1)
        #expect(tall.autoscroll(CGPoint(x: 120, y: 700), edge: 44) == 1)
        #expect(tall.autoscroll(CGPoint(x: 120, y: 120), edge: 44) == -1)
        #expect(tall.autoscroll(CGPoint(x: 120, y: 50), edge: 44) == -1)
        #expect(tall.autoscroll(CGPoint(x: 120, y: 350), edge: 44) == 0)
    }

    /// A move is drawn at once and saved as `due_date` / `due_at`; a failed save puts only that to-do back.
    @MainActor @Test func aMoveIsDrawnAtOnceAndPutBackWhenItFails() async throws {
        let a = TodoItem(id: "a", title: "A", dueAt: at(9, 14))
        let b = TodoItem(id: "b", title: "B", dueDate: "2026-11-10")
        let store = TodoStore(cachedTodos: [a, b], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()
        hub.on("PATCH", "/api/v1/todos/a") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any]
            #expect(body?["due_date"] as? String == "2026-11-12" && body?["due_at"] == nil)
            return Data(#"{"todo":{"id":"a","title":"A","due_date":"2026-11-12"}}"#.utf8)
        }
        let saving = store.reschedule("a", to: .day("2026-11-12"), api: hub.client)
        #expect(store.todo(id: "a")?.dueDate == "2026-11-12" && store.todo(id: "a")?.dueAt == nil) // drawn at once
        #expect(await saving.value)
        #expect(store.todo(id: "a")?.dueDate == "2026-11-12")

        // The fake hub has no PATCH route for b: it answers 404.
        let failing = store.reschedule("b", to: .at(at(12, 9)), api: hub.client)
        #expect(store.todo(id: "b")?.dueAt == at(12, 9))
        #expect(await failing.value == false)
        #expect(store.todo(id: "b") == b)
        #expect(store.todo(id: "a")?.dueDate == "2026-11-12") // the other move stays
        #expect(hub.sent("PATCH", "/api/v1/todos/b").count == 1)
    }

    @Test func headings() {
        let en = Locale(identifier: "en_US")
        #expect(TodoCalendar.title(.month, anchor: at(9, 12), calendar: cal, locale: en) == "November 2026")
        let day = TodoCalendar.title(.day, anchor: at(9, 12), calendar: cal, locale: en)
        #expect(day.contains("Mon") && day.contains("Nov") && day.contains("9"))
        let week = TodoCalendar.title(.week, anchor: at(11, 12), calendar: cal, locale: en)
        #expect(week.contains("Nov") && week.contains("9") && week.contains("15"))
        #expect(TodoCalendar.title(.month, anchor: at(9, 12), calendar: cal, locale: Locale(identifier: "zh-Hans")).contains("11"))

        #expect(TodoCalendar.weekdaySymbols(calendar: cal, narrow: false).first == "Mon")
        var sunday = cal
        sunday.firstWeekday = 1
        #expect(TodoCalendar.weekdaySymbols(calendar: sunday, narrow: false).first == "Sun")
        #expect(TodoCalendar.weekdaySymbols(calendar: cal, narrow: true).count == 7)
    }
}
