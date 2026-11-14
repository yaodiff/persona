#if os(iOS)
import SwiftUI
import WidgetKit

/// The Calendar widget's week and month, and the switch at its top right (docs/apple/home-widgets.md「Calendar: Today,
/// Week and Month」). Every day and every part of the switch is a button that changes only this widget, at once,
/// without the app; the rest of the widget opens the app as before.

extension HomeCalendarView {
    /// Its name on the switch and in Edit Widget.
    var title: LocalizedStringKey {
        switch self {
        case .today: "Today [Home Widgets]"
        case .week: "Week [Home Widgets]"
        case .month: "Month [Home Widgets]"
        }
    }
}

/// The week's and the month's top line: the month's name in the widget's coral, the switch, the persona.
struct HomeCalendarTop: View {
    var c: HomeContext
    var day: Date
    var shown: HomeCalendarView
    var pictures: HomeFacePictures?

    var body: some View {
        HStack(alignment: .center, spacing: HomeWidgetTokens.tight) {
            Text(day, format: .dateTime.month(.wide)).font(HomeWidgetTokens.title).textCase(.uppercase).lineLimit(1)
                .homeTone(HomeWidgetTokens.today)
            Spacer(minLength: HomeWidgetTokens.tight)
            HomeCalendarSwitch(c: c, shown: shown)
            HomeFace(size: HomeWidgetTokens.faceCorner, mood: c.mood(.today), pictures: pictures)
        }
        .frame(height: HomeWidgetTokens.segmentHeight)
    }
}

/// Today · Week · Month, as a segmented control: each part changes this widget's view (`ShowCalendarViewIntent`).
struct HomeCalendarSwitch: View {
    var c: HomeContext
    var shown: HomeCalendarView
    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        let full = mode == .fullColor
        HStack(spacing: HomeWidgetTokens.flush) {
            ForEach(HomeCalendarView.allCases) { view in
                let on = view == shown
                Button(intent: ShowCalendarViewIntent(slot: c.calendarKey, view: view)) {
                    Text(view.title).font(HomeWidgetTokens.segment).lineLimit(1).minimumScaleFactor(HomeWidgetTokens.nameMinScale)
                        .padding(.horizontal, HomeWidgetTokens.segmentPad)
                        .frame(maxHeight: .infinity)
                        .background {
                            if on {
                                RoundedRectangle(cornerRadius: HomeWidgetTokens.segmentCorner - HomeWidgetTokens.segmentInset, style: .continuous)
                                    .fill(full ? HomeWidgetTokens.segmentOn : HomeWidgetTokens.segmentOnMono)
                            }
                        }
                        .homeInk(on ? .primary : .secondary)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
        .padding(HomeWidgetTokens.segmentInset)
        .frame(height: HomeWidgetTokens.segmentHeight)
        .background {
            RoundedRectangle(cornerRadius: HomeWidgetTokens.segmentCorner, style: .continuous)
                .fill(full ? HomeWidgetTokens.track : HomeWidgetTokens.trackMono)
        }
    }
}

/// The week that holds the chosen day, a dot under each day with events; under it the day's events (medium) or the
/// days from it on (large).
struct HomeWeekView: View {
    var c: HomeContext
    var day: Date
    var large: Bool
    var pictures: HomeFacePictures?

    var body: some View {
        VStack(alignment: .leading, spacing: HomeWidgetTokens.flush) {
            HomeCalendarTop(c: c, day: day, shown: .week, pictures: pictures)
            HomeWeekStrip(c: c, selected: day).padding(.top, large ? HomeWidgetTokens.gap : HomeWidgetTokens.hair)
            if large {
                HomeRule().padding(.vertical, HomeWidgetTokens.gap)
                HomeAgenda(c: c, from: day).frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            } else {
                HomeDayList(c: c, day: day).padding(.top, HomeWidgetTokens.tight)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            }
        }
    }
}

/// The month that holds the chosen day, a dot under each day with events; the day's events under it (large) or beside
/// it (medium).
struct HomeMonthView: View {
    var c: HomeContext
    var day: Date
    var large: Bool
    var pictures: HomeFacePictures?

    var body: some View {
        if large {
            VStack(alignment: .leading, spacing: HomeWidgetTokens.flush) {
                HomeCalendarTop(c: c, day: day, shown: .month, pictures: pictures)
                HomeMonthGrid(c: c, selected: day, compact: false).padding(.top, HomeWidgetTokens.gap)
                HomeRule().padding(.top, HomeWidgetTokens.tight).padding(.bottom, HomeWidgetTokens.gap - HomeWidgetTokens.hair)
                HomeDayHead(c: c, day: day).padding(.bottom, HomeWidgetTokens.hair)
                HomeDayList(c: c, day: day).frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            }
        } else {
            VStack(alignment: .leading, spacing: HomeWidgetTokens.hair) {
                HomeCalendarTop(c: c, day: day, shown: .month, pictures: pictures)
                HStack(alignment: .top, spacing: HomeWidgetTokens.row) {
                    HomeMonthGrid(c: c, selected: day, compact: true).frame(width: HomeWidgetTokens.monthGridCompact)
                    VStack(alignment: .leading, spacing: HomeWidgetTokens.tight) {
                        HomeDayHead(c: c, day: day)
                        HomeDayList(c: c, day: day)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                }
            }
        }
    }
}

/// Seven days in a row, each its weekday's letter, its number and its dot.
struct HomeWeekStrip: View {
    var c: HomeContext
    var selected: Date
    @Environment(\.locale) private var locale

    var body: some View {
        let calendar = Calendar.current
        HStack(spacing: HomeWidgetTokens.flush) {
            ForEach(HomeDays.week(selected, calendar: calendar), id: \.self) { day in
                HomeDayButton(c: c, day: day, selected: calendar.isDate(day, inSameDayAs: selected), size: .week,
                              letter: HomeCalendarWords.weekday(day, locale: locale))
            }
        }
    }
}

/// The month as the calendar draws it: the weekdays' letters, then a row a week. The medium widget's fits its rows in
/// the height it has.
struct HomeMonthGrid: View {
    var c: HomeContext
    var selected: Date
    var compact: Bool
    @Environment(\.locale) private var locale

    var body: some View {
        let rows = HomeDays.month(selected, calendar: .current)
        if compact {
            GeometryReader { geo in
                let room = (geo.size.height - HomeWidgetTokens.weekdayRowCompact) / CGFloat(max(1, rows.count))
                grid(rows, row: min(HomeWidgetTokens.monthRowCompact, room))
            }
        } else {
            grid(rows, row: HomeWidgetTokens.monthRow)
        }
    }

    private func grid(_ rows: [[Date?]], row: CGFloat) -> some View {
        let calendar = Calendar.current
        let letters = HomeDays.week(selected, calendar: calendar).map { HomeCalendarWords.weekday($0, locale: locale) }
        return VStack(spacing: HomeWidgetTokens.flush) {
            HStack(spacing: HomeWidgetTokens.flush) {
                ForEach(letters.indices, id: \.self) { i in
                    Text(verbatim: letters[i]).font(compact ? HomeWidgetTokens.weekdayCompact : HomeWidgetTokens.weekday)
                        .lineLimit(1).homeInk(.secondary).frame(maxWidth: .infinity)
                }
            }
            .frame(height: compact ? HomeWidgetTokens.weekdayRowCompact : HomeWidgetTokens.weekdayRow, alignment: .top)
            ForEach(rows.indices, id: \.self) { r in
                HStack(spacing: HomeWidgetTokens.flush) {
                    ForEach(rows[r].indices, id: \.self) { i in
                        if let day = rows[r][i] {
                            HomeDayButton(c: c, day: day, selected: calendar.isDate(day, inSameDayAs: selected),
                                          size: compact ? .compact(row) : .month)
                        } else {
                            Color.clear.frame(maxWidth: .infinity)
                        }
                    }
                }
                .frame(height: row)
            }
        }
    }
}

/// A day: a tap picks it in this widget (`PickCalendarDayIntent`); a tap on today, or on the day picked, goes back to
/// today. Today is the widget's coral, a picked day a filled disc, days gone by step back.
struct HomeDayButton: View {
    enum Size: Equatable {
        case week, month
        /// The medium month's, in a row this tall.
        case compact(CGFloat)
    }

    var c: HomeContext
    var day: Date
    var selected: Bool
    var size: Size
    var letter: String?

    var body: some View {
        let today = Calendar.current.isDate(day, inSameDayAs: c.date)
        let past = !today && day < c.date
        let known = c.day(day)
        Button(intent: PickCalendarDayIntent(slot: c.calendarKey, day: HomeDays.key(day))) {
            Group {
                switch size {
                case .compact(let row):
                    ZStack(alignment: .bottom) {
                        HomeDayNumber(day: day, today: today, selected: selected, past: past,
                                      disc: min(HomeWidgetTokens.monthDiscCompact, row), font: HomeWidgetTokens.monthNumberCompact)
                        dot(today: today, past: past, onDisc: selected, side: HomeWidgetTokens.dayDotCompact)
                            .padding(.bottom, HomeWidgetTokens.hairline)
                    }
                case .week, .month:
                    VStack(spacing: HomeWidgetTokens.hairline) {
                        if let letter {
                            Text(verbatim: letter).font(HomeWidgetTokens.weekday).lineLimit(1).homeInk(.secondary)
                        }
                        HomeDayNumber(day: day, today: today, selected: selected, past: past,
                                      disc: size == .week ? HomeWidgetTokens.weekDisc : HomeWidgetTokens.monthDisc,
                                      font: size == .week ? HomeWidgetTokens.weekNumber : HomeWidgetTokens.monthNumber)
                        dot(today: today, past: past, onDisc: false, side: size == .week ? HomeWidgetTokens.dayDot : HomeWidgetTokens.dayDotMonth)
                    }
                }
            }
            .frame(maxWidth: .infinity)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text(day, format: .dateTime.weekday(.wide).month(.wide).day()))
        .accessibilityValue(Text("\((known?.events.count ?? 0) + (known?.more ?? 0)) events [Home Widgets]"))
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    /// The dot of a day with events; on a disc, in the number's color.
    @ViewBuilder private func dot(today: Bool, past: Bool, onDisc: Bool, side: CGFloat) -> some View {
        let dot = Circle().frame(width: side, height: side)
        Group {
            if onDisc { dot.foregroundStyle(HomeWidgetTokens.surface) } else { dot.homeTone(HomeWidgetTokens.today) }
        }
        .opacity(c.hasEvents(day) ? (past ? HomeWidgetTokens.pastDot : 1) : 0)
    }
}

/// A day's number: today's in a coral disc when it is the one shown, else in coral; a picked day in a filled disc.
/// Where the system paints in one color, today's disc is in the accent group with the number knocked out of it.
struct HomeDayNumber: View {
    var day: Date
    var today: Bool
    var selected: Bool
    var past: Bool
    var disc: CGFloat
    var font: Font
    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        let full = mode == .fullColor
        let number = Text(Calendar.current.component(.day, from: day), format: .number).font(font).monospacedDigit().lineLimit(1)
        Group {
            if today && selected {
                ZStack {
                    Circle().fill(full ? HomeWidgetTokens.today : Color.white)
                    number.foregroundStyle(full ? Color.white : Color.black).blendMode(full ? .normal : .destinationOut)
                }
                .compositingGroup()
                .homeAccent()
            } else if selected {
                ZStack {
                    Circle().fill(full ? Color.primary : HomeWidgetTokens.segmentOnMono)
                    number.foregroundStyle(full ? HomeWidgetTokens.surface : Color.white)
                }
            } else if today {
                number.homeTone(HomeWidgetTokens.today)
            } else {
                number.homeInk(past ? .secondary : .primary)
            }
        }
        .frame(width: disc, height: disc)
    }
}

/// "Today Wed, Nov 18": the day the list under it is for, in coral when it is today.
struct HomeDayHead: View {
    var c: HomeContext
    var day: Date

    var body: some View {
        let calendar = Calendar.current
        let today = calendar.isDate(day, inSameDayAs: c.date)
        let tomorrow = calendar.isDate(day, inSameDayAs: HomeDays.nextMidnight(after: c.date))
        let words = HStack(spacing: HomeWidgetTokens.tight) {
            if today { Text("Today [Home Widgets]") } else if tomorrow { Text("Tomorrow [Home Widgets]") }
            Text(day, format: .dateTime.weekday(.abbreviated).month(.abbreviated).day())
        }
        .font(HomeWidgetTokens.captionStrong).lineLimit(1)
        if today { words.homeTone(HomeWidgetTokens.today) } else { words.homeInk(.secondary) }
    }
}

/// A day's events, as many as fit, and how many more; or why there are none.
struct HomeDayList: View {
    var c: HomeContext
    var day: Date

    var body: some View {
        if let known = c.day(day), !known.events.isEmpty {
            ViewThatFits(in: .vertical) {
                rows(known, fit: 5)
                rows(known, fit: 4)
                rows(known, fit: 3)
                rows(known, fit: 2)
                rows(known, fit: 1)
            }
        } else {
            HomeDayNone(c: c, day: day)
        }
    }

    private func rows(_ known: (events: [HomeSnapshot.Event], more: Int), fit: Int) -> some View {
        let total = known.events.count + known.more
        let shown = total <= fit ? known.events : Array(known.events.prefix(fit - 1))
        return VStack(alignment: .leading, spacing: HomeWidgetTokens.flush) {
            ForEach(shown.indices, id: \.self) { HomeEventLine(event: shown[$0]) }
            if total > shown.count {
                Text("\(total - shown.count) more events [Home Widgets]").font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.tertiary)
            }
        }
    }
}

/// The large week's list: the chosen day, then the days after it that have events, for a week, as many lines as fit.
struct HomeAgenda: View {
    var c: HomeContext
    var from: Date

    private typealias DayEvents = (day: Date, events: [HomeSnapshot.Event])

    var body: some View {
        let groups = days()
        ViewThatFits(in: .vertical) {
            lines(groups, budget: 16)
            lines(groups, budget: 14)
            lines(groups, budget: 12)
            lines(groups, budget: 10)
            lines(groups, budget: 8)
            lines(groups, budget: 6)
            lines(groups, budget: 4)
            lines(groups, budget: 2)
        }
    }

    /// The chosen day (even with nothing on it), then each day after it with events, for seven days.
    private func days() -> [DayEvents] {
        let calendar = Calendar.current
        return (0..<7).compactMap { i -> DayEvents? in
            guard let day = calendar.date(byAdding: .day, value: i, to: from) else { return nil }
            let events = c.day(day)?.events ?? []
            guard i == 0 || !events.isEmpty else { return nil }
            return (day, events)
        }
    }

    /// The days that fit in `budget` lines, a day's name counting as one.
    private func lines(_ groups: [DayEvents], budget: Int) -> some View {
        var left = budget
        var shown: [DayEvents] = []
        for g in groups {
            guard left >= 2 else { break }
            let take = min(left - 1, g.events.count)
            shown.append((g.day, Array(g.events.prefix(take))))
            left -= 1 + max(1, take)
        }
        let fitting = shown
        return VStack(alignment: .leading, spacing: HomeWidgetTokens.flush) {
            ForEach(fitting.indices, id: \.self) { i in
                HomeDayHead(c: c, day: fitting[i].day).padding(.top, i == 0 ? HomeWidgetTokens.flush : HomeWidgetTokens.gap - HomeWidgetTokens.hair)
                if fitting[i].events.isEmpty {
                    HomeDayNone(c: c, day: fitting[i].day)
                } else {
                    ForEach(fitting[i].events.indices, id: \.self) { HomeEventLine(event: fitting[i].events[$0]) }
                }
            }
        }
    }
}

/// Why a day lists nothing: no calendar access, nothing left today, only today known (the hub's meetings), a day the
/// snapshot does not cover yet, or a free day.
struct HomeDayNone: View {
    var c: HomeContext
    var day: Date

    var body: some View {
        Text(words).font(HomeWidgetTokens.body).lineLimit(3).homeInk(.secondary)
    }

    private var words: LocalizedStringKey {
        let source = c.snapshot.today?.source
        if source == nil { return "Allow Calendars in Persona to see your events [Home Widgets]" }
        if Calendar.current.isDate(day, inSameDayAs: c.date) { return "No more events today [Home Widgets]" }
        if source == .hub { return "Allow Calendars in Persona to see other days [Home Widgets]" }
        if c.day(day) == nil { return "Open Persona to see this day [Home Widgets]" }
        return "No events [Home Widgets]"
    }
}

/// An event on one line: the coral bar, its title, when it starts.
struct HomeEventLine: View {
    var event: HomeSnapshot.Event

    var body: some View {
        HStack(alignment: .center, spacing: HomeWidgetTokens.gap - HomeWidgetTokens.hair) {
            RoundedRectangle(cornerRadius: HomeWidgetTokens.hair)
                .frame(width: HomeWidgetTokens.lineBar, height: HomeWidgetTokens.lineBarHeight)
                .homeTone(HomeWidgetTokens.today)
            Text(verbatim: event.title).font(HomeWidgetTokens.lineTitle).lineLimit(1).homeInk(.primary)
            Spacer(minLength: HomeWidgetTokens.tight)
            Group {
                if event.allDay {
                    Text("All Day [Home Widgets]")
                } else {
                    Text(Date(timeIntervalSince1970: event.start), format: .dateTime.hour().minute())
                }
            }
            .font(HomeWidgetTokens.caption).monospacedDigit().lineLimit(1).homeInk(.secondary)
        }
    }
}

/// A line between the grid and the list.
struct HomeRule: View {
    var body: some View {
        Rectangle().frame(height: HomeWidgetTokens.hairline).foregroundStyle(HomeWidgetTokens.rule)
    }
}

/// The words the calendar draws from the interface language: a weekday's letter.
enum HomeCalendarWords {
    static func weekday(_ day: Date, locale: Locale) -> String {
        var calendar = Calendar.current
        calendar.locale = locale
        let symbols = calendar.veryShortStandaloneWeekdaySymbols
        let i = calendar.component(.weekday, from: day) - 1
        return symbols.indices.contains(i) ? symbols[i] : ""
    }
}
#endif
