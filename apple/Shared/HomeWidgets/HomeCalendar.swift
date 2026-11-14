import Foundation
#if os(iOS)
import WidgetKit
#endif

/// The Calendar widget's Week and Month views (docs/apple/home-widgets.md「Calendar: Today, Week and Month」): the days
/// the app writes into the snapshot, and the view and day each widget shows.

/// Which of its views a Calendar widget shows: Edit Widget › View sets where it starts, the switch at its top right
/// changes it (`ShowCalendarViewIntent`).
nonisolated enum HomeCalendarView: String, Codable, CaseIterable, Identifiable, Sendable {
    case today, week, month

    var id: String { rawValue }
}

/// Days as the snapshot keys them: "2026-11-18", the phone's own day (its time zone; the digits are the Gregorian
/// date's, whatever calendar the phone shows). The range around today the Week and Month views can show.
nonisolated enum HomeDays {
    /// How many events a day keeps (the most a day's list shows), and how long a title may be.
    static let perDay = 4
    static let titleCap = 60
    /// Days after today always covered, so the week and the days after midnight are there without the app.
    static let ahead = 14

    static func key(_ date: Date, calendar: Calendar = .current) -> String {
        var gregorian = Calendar(identifier: .gregorian)
        gregorian.timeZone = calendar.timeZone
        let c = gregorian.dateComponents([.year, .month, .day], from: date)
        func two(_ n: Int?) -> String { (n ?? 0) < 10 ? "0\(n ?? 0)" : "\(n ?? 0)" }
        return "\(c.year ?? 0)-\(two(c.month))-\(two(c.day))"
    }

    /// The start of the day a key names, in the phone's time zone.
    static func date(_ key: String, calendar: Calendar = .current) -> Date? {
        let parts = key.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3 else { return nil }
        var gregorian = Calendar(identifier: .gregorian)
        gregorian.timeZone = calendar.timeZone
        return gregorian.date(from: DateComponents(year: parts[0], month: parts[1], day: parts[2]))
    }

    /// The seven days of the week that holds `day`, from the phone's first weekday.
    static func week(_ day: Date, calendar: Calendar = .current) -> [Date] {
        let start = calendar.dateInterval(of: .weekOfYear, for: day)?.start ?? calendar.startOfDay(for: day)
        return (0..<7).compactMap { calendar.date(byAdding: .day, value: $0, to: start) }
    }

    /// The month of `day` as the calendar draws it: whole weeks, nil for the days of the months around it.
    static func month(_ day: Date, calendar: Calendar = .current) -> [[Date?]] {
        guard let month = calendar.dateInterval(of: .month, for: day) else { return [] }
        let first = week(month.start, calendar: calendar).first ?? month.start
        var rows: [[Date?]] = []
        var cursor = first
        while cursor < month.end {
            rows.append((0..<7).map { i in
                let d = calendar.date(byAdding: .day, value: i, to: cursor) ?? cursor
                return d >= month.start && d < month.end ? d : nil
            })
            cursor = calendar.date(byAdding: .day, value: 7, to: cursor) ?? month.end
        }
        return rows
    }

    /// The days the snapshot covers: this month's grid (its whole first and last weeks) and the `ahead` days after
    /// today, whichever ends later. At most about seven weeks.
    static func range(around now: Date, calendar: Calendar = .current) -> DateInterval {
        let today = calendar.startOfDay(for: now)
        let month = calendar.dateInterval(of: .month, for: today) ?? DateInterval(start: today, duration: 86_400)
        let start = week(month.start, calendar: calendar).first ?? month.start
        let lastDay = calendar.date(byAdding: .day, value: -1, to: month.end) ?? today
        let gridEnd = week(lastDay, calendar: calendar).last.flatMap { calendar.date(byAdding: .day, value: 1, to: $0) } ?? month.end
        let aheadEnd = calendar.date(byAdding: .day, value: ahead + 1, to: today) ?? gridEnd
        return DateInterval(start: start, end: max(gridEnd, aheadEnd))
    }

    /// Each day of `range` that has events: how many, and the first `perDay` (all-day ones first, then by start), with
    /// titles cut to `titleCap`. An event over several days counts on each.
    static func build(_ events: [HomeSnapshot.Event], range: DateInterval, calendar: Calendar = .current) -> HomeSnapshot.Days {
        var byDay: [String: [HomeSnapshot.Event]] = [:]
        for event in events {
            let start = Date(timeIntervalSince1970: event.start), end = Date(timeIntervalSince1970: max(event.start, event.end))
            var day = calendar.startOfDay(for: max(start, range.start))
            while day < range.end {
                guard let next = calendar.date(byAdding: .day, value: 1, to: day) else { break }
                // On a day when it overlaps it; an event of no length on the day it starts.
                let on = start < next && (end > day || (end == start && start >= day))
                if on { byDay[key(day, calendar: calendar), default: []].append(event) }
                if end <= next { break }
                day = next
            }
        }
        var days = HomeSnapshot.Days(from: key(range.start, calendar: calendar),
                                     to: key(calendar.date(byAdding: .day, value: -1, to: range.end) ?? range.start, calendar: calendar))
        for (k, list) in byDay {
            let sorted = list.sorted { ($0.allDay ? 0 : 1, $0.start) < ($1.allDay ? 0 : 1, $1.start) }
            days.byDay[k] = .init(count: sorted.count, events: sorted.prefix(perDay).map { e in
                var e = e
                if e.title.count > titleCap { e.title = String(e.title.prefix(titleCap)) }
                return e
            })
        }
        return days
    }

    /// The next midnight after `date`: the date and the grids move on then, without the app.
    static func nextMidnight(after date: Date, calendar: Calendar = .current) -> Date {
        calendar.date(byAdding: .day, value: 1, to: calendar.startOfDay(for: date)) ?? date.addingTimeInterval(86_400)
    }
}

#if os(iOS)
/// What the switch and a tapped day left for each Calendar widget. WidgetKit gives a widget no identity of its own, so
/// a widget's place is its size and its Edit Widget › View (`slot`): widgets of different sizes or Views keep their own,
/// and two alike switch together (docs/apple/home-widgets.md「Calendar: Today, Week and Month」).
nonisolated struct HomeCalendarState: Codable, Hashable, Sendable {
    nonisolated struct Slot: Codable, Hashable, Sendable {
        /// The view the switch was left on; nil: Edit Widget's.
        var view: HomeCalendarView?
        /// The day tapped in the week or the month, and when.
        var day: String?
        var pickedAt: Double?
    }

    var slots: [String: Slot] = [:]

    /// A picked day goes back to today after this long, and at midnight.
    static let pickLasts: TimeInterval = 10 * 60

    static func slot(_ family: WidgetFamily, _ configured: HomeCalendarView) -> String {
        let size = switch family {
        case .systemLarge: "large"
        case .systemExtraLarge: "extraLarge"
        case .systemSmall: "small"
        default: "medium"
        }
        return "\(size).\(configured.rawValue)"
    }

    /// The view and the day a widget shows at `date`.
    static func shown(_ slot: Slot?, configured: HomeCalendarView, at date: Date, calendar: Calendar = .current) -> (view: HomeCalendarView, day: Date) {
        let view = slot?.view ?? configured
        let day = picked(slot, at: date, calendar: calendar).flatMap { HomeDays.date($0, calendar: calendar) }
        return (view, day ?? calendar.startOfDay(for: date))
    }

    /// The day picked within the last `pickLasts`, the same day as `date`. Read at the moment of the pick, the stored
    /// time can come back a hair later than the clock (a `Date` through seconds since 1970), so only the end is checked.
    static func picked(_ slot: Slot?, at date: Date, calendar: Calendar = .current) -> String? {
        guard let day = slot?.day, let at = slot?.pickedAt else { return nil }
        let when = Date(timeIntervalSince1970: at)
        guard date.timeIntervalSince(when) < pickLasts, calendar.isDate(when, inSameDayAs: date) else { return nil }
        return day
    }

    /// When a picked day stops counting, if that is still to come (the timeline redraws then).
    static func pickEnds(_ slot: Slot?, after date: Date) -> Date? {
        guard slot?.day != nil, let at = slot?.pickedAt else { return nil }
        let end = Date(timeIntervalSince1970: at + pickLasts)
        return end > date ? end : nil
    }

    mutating func show(_ view: HomeCalendarView, in slot: String) {
        slots[slot, default: .init()].view = view
    }

    /// Picks a day; picking today, or the day already picked, goes back to today.
    mutating func pick(_ day: String, in slot: String, at now: Date, calendar: Calendar = .current) {
        var s = slots[slot, default: .init()]
        if day == HomeDays.key(now, calendar: calendar) || day == Self.picked(s, at: now, calendar: calendar) {
            s.day = nil
            s.pickedAt = nil
        } else {
            s.day = day
            s.pickedAt = now.timeIntervalSince1970
        }
        slots[slot] = s
    }
}

/// `Widgets/calendar.json` in the App Group, next to the snapshot: written by the Calendar widget's buttons (in the
/// widget extension, or the app when the system runs them there), read by its timeline. Tests pass a directory.
nonisolated struct HomeCalendarStore: Sendable {
    static let fileName = "calendar.json"
    var root: URL?

    static var shared: HomeCalendarStore { HomeCalendarStore(root: HomeSnapshotStore.shared.root) }

    /// Redraws the Calendar widgets (the system does after a widget's button too; from the app it must be asked).
    @MainActor static func reload() { WidgetCenter.shared.reloadTimelines(ofKind: HomeKind.today.widgetKind) }

    /// The state as last written; a fresh one when there is none. Named apart from the many `read`s, so the isolation
    /// lint can tell its callers (#7252): the widget reads it off the main actor (`HomeTimeline.calendarState`).
    func savedState() -> HomeCalendarState {
        guard let url = root?.appending(path: Self.fileName), let data = try? Data(contentsOf: url),
              let state = try? JSONDecoder().decode(HomeCalendarState.self, from: data) else { return HomeCalendarState() }
        return state
    }

    func update(_ change: (inout HomeCalendarState) -> Void) {
        guard let root else { return }
        var state = savedState()
        change(&state)
        do {
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            try JSONEncoder().encode(state).write(to: root.appending(path: Self.fileName),
                                                 options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        } catch {
            HomeSnapshotStore.log.error("calendar widget state not written: \(error.localizedDescription, privacy: .public)")
        }
    }
}
#endif
