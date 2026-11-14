import SwiftUI

// The pieces of the to-dos' calendar (#2029, docs/assistant/todos.md "Calendar"): an entry's block and chip, the day
// view's week strip, the time axis of a day or a week, and the month. What goes where is `TodoCalendar` (Shared).

/// Where a new to-do made on the calendar is due: at `time`, or all day on `day` when it is nil.
struct TodoCalendarSlot: Identifiable, Equatable {
    var day: Date
    var time: Date?
    var id: Date { time ?? day }
}

/// An entry's colors, as the list draws its to-do: its state's, red once overdue (before the state: what is late is
/// what to see first), faded when done.
struct CalendarEntryLook {
    var entry: CalendarEntry

    var color: Color { entry.overdue ? Theme.danger : entry.state.todoTone.dot }
    var text: Color { entry.overdue ? Theme.danger : entry.state.todoTone.text }
    var ground: Color { color.opacity(Theme.CalendarGrid.tint) }
    var fade: Double { entry.done ? Theme.CalendarGrid.doneFade : 1 }

    /// "18:00", or "Was due 11:30" once it has passed.
    var time: String {
        let t = entry.start.formatted(date: .omitted, time: .shortened)
        return entry.overdue ? String(localized: "Was due \(t)") : t
    }

    /// What VoiceOver reads: the title, its state and when.
    var spoken: String {
        let when = entry.allDay ? String(localized: "All Day") : time
        return [entry.title, entry.stateLabel, when].joined(separator: ", ")
    }
}

/// High priority and a pending reminder, as on the list's row.
struct CalendarEntryMarks: View {
    var entry: CalendarEntry

    var body: some View {
        if entry.highPriority {
            ThemedIcon(Theme.Icon.highPriority)
                .foregroundStyle(Theme.caution)
        }
        if entry.reminding {
            ThemedIcon(Theme.Icon.reminder)
                .foregroundStyle(.secondary)
        }
    }
}

/// A to-do on the time axis: its top edge is when it is due. `narrow` (a phone's week) keeps the title alone.
struct CalendarEntryBlock: View {
    var entry: CalendarEntry
    var narrow = false

    var body: some View {
        let look = CalendarEntryLook(entry: entry)
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            if narrow {
                Text(entry.title)
                    .font(Theme.Font.labelStrong)
                    .lineLimit(2)
                    .strikethrough(entry.done)
                if entry.highPriority {
                    ThemedIcon(Theme.Icon.highPriority)
                        .font(Theme.Font.label)
                        .foregroundStyle(Theme.caution)
                }
            } else {
                HStack(spacing: Theme.Space.xs) {
                    Text(entry.title)
                        .font(Theme.Font.captionStrong)
                        .lineLimit(1)
                        .strikethrough(entry.done)
                    CalendarEntryMarks(entry: entry)
                        .font(Theme.Font.label)
                }
                // by the block's width: state and time, else the time alone, else nothing (never a cut-off state and time)
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: Theme.Space.xs) {
                        Text(entry.stateLabel)
                            .foregroundStyle(look.text)
                        Text(verbatim: "· \(look.time)")
                            .foregroundStyle(entry.overdue ? Theme.danger : Color.secondary)
                    }
                    Text(verbatim: look.time)
                        .foregroundStyle(entry.overdue ? Theme.danger : Color.secondary)
                    EmptyView()
                }
                .font(Theme.Font.label)
                .lineLimit(1)
            }
        }
        .padding(.leading, Theme.Space.s)
        .padding(.trailing, Theme.Space.xxs)
        .padding(.vertical, Theme.Space.xs)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(alignment: .leading) {
            Rectangle().fill(look.color).frame(width: Theme.Size.bar)
        }
        .background(look.ground)
        .clipShape(.rect(cornerRadius: Theme.Radius.badge))
        .opacity(look.fade)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(look.spoken)
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("todo.calendar.entry")
    }
}

/// Several to-dos whose blocks would overlap in a phone's narrow week column: one block that says how many, in the
/// colors of each; tapping it opens their day.
struct CalendarGroupBlock: View {
    var entries: [CalendarEntry]

    var body: some View {
        let first = CalendarEntryLook(entry: entries[0])
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            HStack(spacing: Theme.Space.xxs) {
                ForEach(entries.prefix(Theme.CalendarGrid.dots)) { e in
                    Circle().fill(CalendarEntryLook(entry: e).color)
                        .frame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                }
            }
            Text("\(entries.count) to-dos")
                .font(Theme.Font.labelStrong)
                .lineLimit(1)
        }
        .padding(.leading, Theme.Space.s)
        .padding(.vertical, Theme.Space.xs)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(alignment: .leading) {
            Rectangle().fill(first.color).frame(width: Theme.Size.bar)
        }
        .background(first.ground)
        .clipShape(.rect(cornerRadius: Theme.Radius.badge))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(entries.map(\.title).joined(separator: ", "))
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("todo.calendar.group")
    }
}

/// An entry in a row of its own: the all-day row, a day of the month. `compact` (a phone's week or month) is a few
/// characters with a thin bar.
struct CalendarEntryChip: View {
    var entry: CalendarEntry
    var compact = false
    var showsTime = false

    var body: some View {
        let look = CalendarEntryLook(entry: entry)
        // the time goes first only while it and the whole title fit the cell; otherwise the title alone (a time wrapped over
        // three lines beside a cut title told neither)
        ViewThatFits(in: .horizontal) {
            if showsTime && !entry.allDay { row(look: look, time: true) }
            row(look: look, time: false)
        }
        .font(compact ? Theme.Font.label : Theme.Font.caption)
        .padding(.leading, compact ? Theme.Space.xs : Theme.Space.s)
        .padding(.trailing, Theme.Space.xxs)
        .padding(.vertical, Theme.Space.xxs)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(alignment: .leading) {
            Rectangle().fill(look.color).frame(width: compact ? Theme.Size.rule : Theme.Size.bar)
        }
        .background(look.ground)
        .clipShape(.rect(cornerRadius: compact ? Theme.Radius.thumbnail : Theme.Radius.badge))
        .opacity(look.fade)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(look.spoken)
        .accessibilityIdentifier("todo.calendar.entry")
    }

    private func row(look: CalendarEntryLook, time: Bool) -> some View {
        HStack(spacing: Theme.Space.xs) {
            if time {
                Text(entry.start.formatted(date: .omitted, time: .shortened))
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
                    .lineLimit(1)
            }
            Text(entry.title)
                .foregroundStyle(entry.overdue ? Theme.danger : Color.primary)
                .strikethrough(entry.done)
                .lineLimit(1)
            if !compact {
                CalendarEntryMarks(entry: entry)
            }
        }
    }
}

/// The day view's week: tap a day to show it; under each date a dot for each of its open to-dos (a few), in their colors.
struct TodoCalendarWeekStrip: View {
    var anchor: Date
    var entries: [CalendarEntry]
    var pick: (Date) -> Void

    private let calendar = TodoCalendar.userCalendar

    var body: some View {
        let days = TodoCalendar.days(.week, anchor: anchor, calendar: calendar)
        let names = TodoCalendar.weekdaySymbols(calendar: calendar, narrow: true)
        HStack(spacing: Theme.Space.flush) {
            ForEach(Array(days.enumerated()), id: \.offset) { i, day in
                let picked = calendar.isDate(day, inSameDayAs: anchor)
                let today = calendar.isDateInToday(day)
                let open = TodoCalendar.on(day, entries, calendar: calendar).filter { !$0.done }
                Button { pick(day) } label: {
                    VStack(spacing: Theme.Space.xxs) {
                        Text(names[i])
                            .font(Theme.Font.label)
                            .foregroundStyle(.secondary)
                        Text(verbatim: String(calendar.component(.day, from: day)))
                            .font(picked ? Theme.Font.bodyStrong : Theme.Font.body)
                            .foregroundStyle(picked ? Theme.onSolid : today ? Theme.accent : Color.primary)
                            .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                            .background {
                                if picked { Circle().fill(Theme.accent) }
                            }
                        HStack(spacing: Theme.Space.xxs) {
                            ForEach(open.prefix(Theme.CalendarGrid.dots)) { e in
                                Circle().fill(CalendarEntryLook(entry: e).color)
                                    .frame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                            }
                        }
                        .frame(height: Theme.Size.dotXS)
                    }
                    .frame(maxWidth: .infinity)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(day.formatted(date: .complete, time: .omitted))
                .accessibilityValue(open.isEmpty ? "" : String(localized: "\(open.count) to-dos"))
                .accessibilityAddTraits(picked ? .isSelected : [])
            }
        }
        .padding(.horizontal, Theme.Space.s)
        .padding(.bottom, Theme.Space.s)
        .accessibilityIdentifier("todo.calendar.strip")
    }
}

/// A day or a week on a time axis: the all-day row on top, then the hours, each to-do at its due time, side by side
/// where they overlap; a red line at the moment. Tapping the empty axis offers a new to-do due there.
struct TodoCalendarTimeline: View {
    var days: [Date]
    var entries: [CalendarEntry]
    /// A phone's week: the hours alone in the gutter, a few characters a block, overlapping ones as one.
    var narrow: Bool
    /// The week's row of days (the day view has its strip instead).
    var showsDayHeader: Bool
    var open: (CalendarEntry) -> Void
    var pickDay: (Date) -> Void
    var add: (TodoCalendarSlot) -> Void
    /// Moving a to-do to another time or day (#2045); nil: nothing is dragged (the Mac).
    var moves: CalendarMoves? = nil

    @Environment(\.displayScale) private var displayScale
    private let calendar = TodoCalendar.userCalendar
    private let hour = Theme.CalendarGrid.hour
    /// A to-do being dragged, and the parts' frames and the axis's scroll that say where it would land.
    @State private var dragging: CalendarDragging?
    @State private var width: CGFloat = 0
    @State private var allDayFrame = CGRect.zero
    @State private var axisFrame = CGRect.zero
    @State private var scrolled: CGFloat = 0
    @State private var scroll = ScrollPosition()
    private static let space = "todo.calendar.timeline"
    /// One device pixel: the grid's lines (0.5 pt is nothing on a 1x screen).
    private var pixel: CGFloat { 1 / displayScale }
    private var gutter: CGFloat { narrow ? Theme.CalendarGrid.gutterNarrow : Theme.CalendarGrid.gutter }
    /// 1 at the default text size: the minutes an hour's label gives way around the now line grow with its height.
    @ScaledMetric(relativeTo: .caption) private var labelRoomScale = 1.0
    /// How long a to-do's block is on the axis.
    private var blockMinutes: Double { Double(Theme.Size.tapTarget / hour) * 60 }

    var body: some View {
        VStack(spacing: Theme.Space.flush) {
            if showsDayHeader { dayHeader }
            allDayRow
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .named(Self.space)) } action: { allDayFrame = $0 }
            Rectangle().fill(Theme.CalendarGrid.lineStrong).frame(height: pixel)
            ScrollViewReader { proxy in
                ScrollView(.vertical) {
                    TimelineView(.everyMinute) { context in
                        axis(now: context.date)
                    }
                }
                .scrollIndicators(.hidden)
                // a carried to-do moves, not the axis under it (near its edges it scrolls by itself)
                .scrollDisabled(dragging != nil)
                .scrollPosition($scroll)
                .onScrollGeometryChange(for: CGFloat.self) { $0.contentOffset.y + $0.contentInsets.top } action: { _, y in scrolled = y }
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .named(Self.space)) } action: { axisFrame = $0 }
                .onAppear { proxy.scrollTo(firstHour, anchor: .top) }
                .onChange(of: days.first) { proxy.scrollTo(firstHour, anchor: .top) }
                // the page may open before the to-dos are read: once they are, the morning's come into sight
                .onChange(of: timed.isEmpty) { _, empty in if !empty { proxy.scrollTo(firstHour, anchor: .top) } }
            }
        }
        .coordinateSpace(.named(Self.space))
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { width = $0 }
        // a tick at each new quarter hour or day it would land on
        .onChange(of: landing) { old, new in if old != nil, new != nil { Haptics.selection() } }
        .task(id: autoscroll) { await scrollWhileHeld(autoscroll) }
    }

    // MARK: Moving a to-do (#2045)

    private var dropMap: TimelineDropMap {
        TimelineDropMap(width: width, gutter: gutter, days: days.count, allDay: allDayFrame, axis: axisFrame,
                        scrolled: scrolled, hour: hour)
    }

    /// Where the dragged to-do would land now: nowhere until it has moved.
    private var landing: CalendarDrop? {
        guard let d = dragging, d.moved, !days.isEmpty, let spot = dropMap.spot(d.point, grab: d.grab) else { return nil }
        switch spot {
        case .allDay(let i): return TodoCalendar.dropOnDay(d.entry, day: days[i], allDay: true, calendar: calendar)
        case .axis(let i, let minutes): return TodoCalendar.dropOnAxis(day: days[i], minutes: minutes, calendar: calendar)
        }
    }

    /// The dragged to-do as it would be drawn where it would land.
    private var landed: CalendarEntry? {
        guard let d = dragging, let landing else { return nil }
        return TodoCalendar.landed(d.entry, at: landing, calendar: calendar, now: .now)
    }

    private var autoscroll: Int {
        dragging.map { dropMap.autoscroll($0.point, edge: Theme.Size.tapTarget) } ?? 0
    }

    /// Scrolls the axis a step each frame while the dragged to-do is held near its edge `way` (-1 up, 1 down).
    private func scrollWhileHeld(_ way: Int) async {
        guard way != 0 else { return }
        while !Task.isCancelled {
            let bottom = max(0, hour * 24 - axisFrame.height)
            let y = min(max(scrolled + CGFloat(way) * Theme.CalendarGrid.autoscrollStep, 0), bottom)
            if y != scrolled { scroll.scrollTo(y: y) }
            try? await Task.sleep(for: .milliseconds(16))
        }
    }

    /// An entry's block or chip, a button that opens it, picked up and carried on this timeline.
    private func carried(_ content: some View, _ e: CalendarEntry) -> some View {
        content.calendarDraggable(e, in: Self.space, moves: moves, lifted: dragging?.entry.id == e.id,
                                  open: { open(e) },
                                  began: { p, grab in
                                      dragging = CalendarDragging(entry: e, point: p, grab: grab)
                                      moves?.carrying(true)
                                      Haptics.tap()
                                      #if DEBUG
                                      let m = dropMap
                                      CalendarDragTrace.shared.note("began \(CalendarDragTrace.point(p)) grab \(Int(grab)) allDay \(CalendarDragTrace.rect(m.allDay)) axis \(CalendarDragTrace.rect(m.axis)) scrolled \(Int(m.scrolled)) width \(Int(m.width))") // i18n-ignore: DEBUG trace
                                      #endif
                                  },
                                  moved: { p in
                                      dragging?.point = p
                                      dragging?.moved = true
                                  },
                                  ended: { dropped in
                                      guard let d = dragging else { return }
                                      let drop = dropped ? landing : nil
                                      #if DEBUG
                                      let spot = dropMap.spot(d.point, grab: d.grab).map { "\($0)" } ?? "none"
                                      CalendarDragTrace.shared.note("ended \(dropped ? "let go" : "cancelled") moved \(d.moved) at \(CalendarDragTrace.point(d.point)) scrolled \(Int(scrolled)) spot \(spot) drop \(drop.map { "\($0)" } ?? "none")") // i18n-ignore: DEBUG trace
                                      #endif
                                      dragging = nil
                                      moves?.carrying(false)
                                      if let drop { moves?.drop(d.entry, drop) }
                                  })
    }

    private var timed: [CalendarEntry] { entries.filter { !$0.allDay } }

    private var firstHour: Int {
        TodoCalendar.firstHour(days: days, timed: timed, now: .now, calendar: calendar)
    }

    // MARK: Header and all-day row

    private var dayHeader: some View {
        let names = TodoCalendar.weekdaySymbols(calendar: calendar, narrow: narrow)
        return HStack(spacing: Theme.Space.flush) {
            Spacer().frame(width: gutter)
            ForEach(Array(days.enumerated()), id: \.offset) { i, day in
                let today = calendar.isDateInToday(day)
                Button { pickDay(day) } label: {
                    let number = Text(verbatim: String(calendar.component(.day, from: day)))
                        .font(today ? Theme.Font.bodyStrong : Theme.Font.body)
                        .foregroundStyle(today ? Theme.onSolid : Color.primary)
                        .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                        .background {
                            if today { Circle().fill(Theme.accent) }
                        }
                    Group {
                        if narrow {
                            VStack(spacing: Theme.Space.xxs) {
                                Text(names[i]).font(Theme.Font.label).foregroundStyle(.secondary)
                                number
                            }
                            .frame(maxWidth: .infinity)
                        } else {
                            HStack(spacing: Theme.Space.xs) {
                                Text(names[i]).font(Theme.Font.caption).foregroundStyle(.secondary)
                                number
                            }
                            .padding(.leading, Theme.Space.s)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(day.formatted(date: .complete, time: .omitted))
                .accessibilityIdentifier("todo.calendar.dayHeader")
            }
        }
        .padding(.bottom, Theme.Space.xs)
    }

    private var allDayRow: some View {
        HStack(alignment: .top, spacing: Theme.Space.flush) {
            // "All" over "Day" when the column is too narrow for the one line at this text size
            Text("All Day")
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .lineLimit(2)
                .multilineTextAlignment(.trailing)
                .minimumScaleFactor(0.6)
                .padding(.trailing, Theme.Space.sm)
                .frame(width: gutter, alignment: .trailing)
                .padding(.top, Theme.Space.xs)
            ForEach(Array(days.enumerated()), id: \.offset) { _, day in
                let all = TodoCalendar.on(day, entries, calendar: calendar).filter(\.allDay)
                let fit: (shown: [CalendarEntry], more: Int) = narrow ? TodoCalendar.fit(all, lines: 2) : (all, 0)
                let ghost = landed.flatMap { $0.allDay && calendar.isDate($0.start, inSameDayAs: day) ? $0 : nil }
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    if let ghost {
                        CalendarEntryChip(entry: ghost, compact: narrow)
                            .calendarLanding()
                    }
                    ForEach(fit.shown) { e in
                        carried(CalendarEntryChip(entry: e, compact: narrow), e)
                    }
                    if fit.more > 0 {
                        Text(verbatim: "+\(fit.more)")
                            .font(Theme.Font.label)
                            .foregroundStyle(.secondary)
                    }
                }
                .padding(.horizontal, Theme.Space.xxs)
                .padding(.vertical, Theme.Space.xs)
                .frame(maxWidth: .infinity, minHeight: Theme.Size.iconS, alignment: .topLeading)
                .background(ghost != nil ? Theme.accent.opacity(Theme.CalendarGrid.tint) : .clear)
                .contentShape(.rect)
                .onTapGesture { add(TodoCalendarSlot(day: day, time: nil)) }
            }
        }
    }

    // MARK: The axis

    private func axis(now: Date) -> some View {
        let todayShown = days.contains { calendar.isDate($0, inSameDayAs: now) }
        let nowMinutes = TodoCalendar.minutes(now, in: now, calendar: calendar)
        // where a dragged to-do would land on the axis: its time in the gutter, over the hour's
        let dropAt = landed.flatMap { $0.allDay ? nil : $0.start }
        let dropMinutes = dropAt.map { TodoCalendar.minutes($0, in: $0, calendar: calendar) }
        func near(_ minutes: Double?, _ h: Int) -> Bool {
            minutes.map { abs(Double(h * 60) - $0) < Double(TodoCalendar.nowLabelRoom) * labelRoomScale } ?? false
        }
        return ZStack(alignment: .topLeading) {
            VStack(spacing: Theme.Space.flush) {
                ForEach(0..<24, id: \.self) { h in
                    hourRow(h, hideLabel: (todayShown && near(nowMinutes, h)) || near(dropMinutes, h))
                        .id(h)
                }
            }
            GeometryReader { geo in
                let column = (geo.size.width - gutter) / CGFloat(max(days.count, 1))
                ForEach(Array(days.enumerated()), id: \.offset) { i, day in
                    dayColumn(day, width: column, now: now)
                        .calendarPlaced(CGRect(x: gutter + column * CGFloat(i), y: .zero, width: column, height: hour * 24))
                }
            }
        }
        .frame(height: hour * 24)
        // over the axis, so its height (half above the line) moves nothing under it
        .overlay(alignment: .topLeading) {
            // a dragged to-do's time near it takes its place
            if todayShown && !(dropMinutes.map { abs($0 - nowMinutes) < Double(TodoCalendar.nowLabelRoom) * labelRoomScale } ?? false) {
                gutterLabel(now, color: Theme.danger)
                    .offset(y: y(nowMinutes))
            }
        }
        .overlay(alignment: .topLeading) {
            if let dropAt, let dropMinutes {
                gutterLabel(dropAt, color: Theme.accent)
                    .offset(y: y(dropMinutes))
                    .accessibilityHidden(true)
            }
        }
    }

    /// A moment's time in the hours' column, centered on its line: the now line's, a dragged to-do's.
    private func gutterLabel(_ date: Date, color: Color) -> some View {
        // a phone's week keeps to the axis's 24-hour numbers ("15:02"), which fit the column; the day view has room for the 12-hour form
        Text(narrow ? clock(date) : date.formatted(date: .omitted, time: .shortened))
            .font(Theme.Font.labelHeavy)
            .foregroundStyle(color)
            .lineLimit(1)
            .minimumScaleFactor(0.6)
            .padding(.trailing, Theme.Space.xs)
            .frame(width: gutter, alignment: .trailing)
            .alignmentGuide(.top) { d in d[VerticalAlignment.center] }
    }

    /// An hour of the axis: its line at the top, its time centered on the line. Both over the row, so the hour's line is
    /// exactly where the to-dos due at that hour start, with a label or without.
    private func hourRow(_ h: Int, hideLabel: Bool) -> some View {
        Color.clear
            .frame(height: hour)
            .overlay(alignment: .top) {
                Rectangle().fill(Theme.CalendarGrid.line)
                    .frame(height: pixel)
                    .padding(.leading, gutter)
            }
            .overlay(alignment: .topLeading) {
                if h > 0 && !hideLabel {
                    Text(hourLabel(h))
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                        .lineLimit(1)
                        .minimumScaleFactor(0.6)
                        .padding(.trailing, Theme.Space.sm)
                        .frame(width: gutter, alignment: .trailing)
                        .alignmentGuide(.top) { d in d[VerticalAlignment.center] }
                }
            }
    }

    private func clock(_ date: Date) -> String {
        let c = calendar.dateComponents([.hour, .minute], from: date)
        return String(format: "%d:%02d", c.hour ?? 0, c.minute ?? 0)
    }

    private func hourLabel(_ h: Int) -> String {
        let date = calendar.date(bySettingHour: h, minute: 0, second: 0, of: days.first ?? .now) ?? .now
        // a phone's week has the hour alone ("14", not the formatter's hour with its Chinese unit, which the narrow gutter cut short)
        return narrow ? String(h) : date.formatted(date: .omitted, time: .shortened)
    }

    private func y(_ minutes: Double) -> CGFloat { CGFloat(minutes / 60) * hour }

    /// A block's place in its day's column: its top edge at the time it is due.
    private func blockFrame(_ start: Date, in day: Date, x: CGFloat, width: CGFloat) -> CGRect {
        CGRect(x: x, y: y(TodoCalendar.minutes(start, in: day, calendar: calendar)) + pixel,
               width: width, height: Theme.Size.tapTarget - Theme.Space.xxs)
    }

    /// A phone's week: the ones that would overlap share one block, the column wide.
    private func groupFrame(_ group: [CalendarEntry], in day: Date, width: CGFloat) -> CGRect {
        blockFrame(group[0].start, in: day, x: Theme.Space.xxs, width: width - Theme.Space.xs)
    }

    /// A wider column: the ones that would overlap side by side.
    private func laneFrame(_ placed: TodoCalendar.Placed, in day: Date, width: CGFloat) -> CGRect {
        let lane = (width - Theme.Space.xs) / CGFloat(placed.lanes)
        return blockFrame(placed.entry.start, in: day, x: Theme.Space.xxs + lane * CGFloat(placed.lane), width: lane - Theme.Space.xxs)
    }

    @ViewBuilder private func dayColumn(_ day: Date, width: CGFloat, now: Date) -> some View {
        let mine = TodoCalendar.on(day, timed, calendar: calendar)
        let groups = narrow ? TodoCalendar.clusters(mine, minutes: blockMinutes) : []
        let lanes = narrow ? [] : TodoCalendar.lanes(mine, minutes: blockMinutes)
        let blocks = groups.map { groupFrame($0, in: day, width: width) } + lanes.map { laneFrame($0, in: day, width: width) }
        ZStack(alignment: .topLeading) {
            Color.clear
                .accessibilityIdentifier("todo.calendar.axis")
            if days.count > 1 {
                Rectangle().fill(Theme.CalendarGrid.line).frame(width: pixel)
            }
            ForEach(groups, id: \.first?.id) { group in
                Group {
                    if group.count == 1 {
                        // one to-do can be moved; several sharing a block open their day
                        carried(CalendarEntryBlock(entry: group[0], narrow: true), group[0])
                    } else {
                        Button { pickDay(day) } label: { CalendarGroupBlock(entries: group) }
                            .buttonStyle(.plain)
                    }
                }
                .calendarPlaced(groupFrame(group, in: day, width: width))
            }
            ForEach(lanes) { placed in
                carried(CalendarEntryBlock(entry: placed.entry), placed.entry)
                    .calendarPlaced(laneFrame(placed, in: day, width: width))
            }
            // where a dragged to-do would land, over what is there, the whole column wide
            if let landed, !landed.allDay, calendar.isDate(landed.start, inSameDayAs: day) {
                CalendarEntryBlock(entry: landed, narrow: narrow)
                    .calendarLanding()
                    .calendarPlaced(blockFrame(landed.start, in: day, x: Theme.Space.xxs, width: width - Theme.Space.xs))
            }
            if calendar.isDate(now, inSameDayAs: day) {
                let at = y(TodoCalendar.minutes(now, in: day, calendar: calendar))
                Rectangle().fill(Theme.danger)
                    .frame(height: Theme.Size.lineMedium)
                    .offset(y: at)
                    .allowsHitTesting(false)
                Circle().fill(Theme.danger)
                    .frame(width: Theme.Size.dot, height: Theme.Size.dot)
                    .offset(x: -Theme.Size.dot / 2, y: at - Theme.Size.dot / 2 + Theme.Size.lineMedium / 2)
                    .allowsHitTesting(false)
            }
        }
        // A tap where no to-do is adds one due then. The column's, around its blocks, as a month's day is around its
        // chips: on a view beside the blocks it took an iPad's finger from them (#2045). Simultaneous, so a tap on a
        // block is the block's whichever gesture ends first.
        .contentShape(.rect)
        .simultaneousGesture(SpatialTapGesture().onEnded { tap in
            #if DEBUG
            CalendarDragTrace.shared.note("axis tap \(CalendarDragTrace.point(tap.location))") // i18n-ignore: DEBUG trace
            #endif
            guard !blocks.contains(where: { $0.contains(tap.location) }) else { return }
            let minutes = Double(tap.location.y / hour) * 60
            add(TodoCalendarSlot(day: day, time: TodoCalendar.slot(day: day, minutes: minutes, snap: Theme.CalendarGrid.snapMinutes, calendar: calendar)))
        })
    }
}

/// A month: the weeks it touches, each day with its first entries and how many more; tapping a day opens it.
struct TodoCalendarMonth: View {
    var anchor: Date
    var entries: [CalendarEntry]
    var wide: Bool
    var open: (CalendarEntry) -> Void
    var pickDay: (Date) -> Void
    /// Moving a to-do to another day (#2045); nil: nothing is dragged (the Mac).
    var moves: CalendarMoves? = nil

    @Environment(\.displayScale) private var displayScale
    private let calendar = TodoCalendar.userCalendar
    /// One device pixel: the grid's lines.
    private var pixel: CGFloat { 1 / displayScale }
    /// A to-do being dragged, and the grid's frame, scroll and day height that say which day it is over.
    @State private var dragging: CalendarDragging?
    @State private var dropMap = MonthDropMap()
    @State private var scroll = ScrollPosition()
    private static let space = "todo.calendar.month"

    private var shownDays: [Date] { TodoCalendar.days(.month, anchor: anchor, calendar: calendar) }

    /// The day the dragged to-do would land on, at its time there: nowhere until it has moved.
    private var landing: CalendarDrop? {
        guard let d = dragging, d.moved, let i = dropMap.day(d.point) else { return nil }
        let days = shownDays
        guard i < days.count else { return nil }
        return TodoCalendar.dropOnDay(d.entry, day: days[i], calendar: calendar)
    }

    private var landed: CalendarEntry? {
        guard let d = dragging, let landing else { return nil }
        return TodoCalendar.landed(d.entry, at: landing, calendar: calendar, now: .now)
    }

    private var autoscroll: Int {
        dragging.map { dropMap.autoscroll($0.point, edge: Theme.Size.tapTarget) } ?? 0
    }

    /// Scrolls the weeks a step each frame while the dragged to-do is held near the grid's edge `way` (-1 up, 1 down),
    /// when they don't all fit (a phone on its side).
    private func scrollWhileHeld(_ way: Int) async {
        guard way != 0 else { return }
        while !Task.isCancelled {
            let y = min(max(dropMap.scrolled + CGFloat(way) * Theme.CalendarGrid.autoscrollStep, 0), dropMap.scrollRange)
            if y != dropMap.scrolled { scroll.scrollTo(y: y) }
            try? await Task.sleep(for: .milliseconds(16))
        }
    }

    /// An entry's chip, picked up and carried on this month: a button that opens it on an iPad (`wide`), on a phone
    /// part of its day, which opens on a tap.
    private func carried(_ content: some View, _ e: CalendarEntry) -> some View {
        content.calendarDraggable(e, in: Self.space, moves: moves, lifted: dragging?.entry.id == e.id,
                                  open: wide ? { open(e) } : nil, accessible: wide,
                                  began: { p, grab in
                                      dragging = CalendarDragging(entry: e, point: p, grab: grab)
                                      moves?.carrying(true)
                                      Haptics.tap()
                                      #if DEBUG
                                      CalendarDragTrace.shared.note("began month \(CalendarDragTrace.point(p)) grid \(CalendarDragTrace.rect(dropMap.grid)) cell \(Int(dropMap.cell)) scrolled \(Int(dropMap.scrolled))") // i18n-ignore: DEBUG trace
                                      #endif
                                  },
                                  moved: { p in
                                      dragging?.point = p
                                      dragging?.moved = true
                                  },
                                  ended: { dropped in
                                      guard let d = dragging else { return }
                                      let drop = dropped ? landing : nil
                                      #if DEBUG
                                      CalendarDragTrace.shared.note("ended month \(dropped ? "let go" : "cancelled") moved \(d.moved) at \(CalendarDragTrace.point(d.point)) day \(dropMap.day(d.point).map(String.init) ?? "none") drop \(drop.map { "\($0)" } ?? "none")") // i18n-ignore: DEBUG trace
                                      #endif
                                      dragging = nil
                                      moves?.carrying(false)
                                      if let drop { moves?.drop(d.entry, drop) }
                                  })
    }

    var body: some View {
        let days = shownDays
        let rows = max(days.count / 7, 1)
        let names = TodoCalendar.weekdaySymbols(calendar: calendar, narrow: !wide)
        VStack(spacing: Theme.Space.flush) {
            HStack(spacing: Theme.Space.flush) {
                ForEach(names.indices, id: \.self) { i in
                    Text(names[i])
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: wide ? .leading : .center)
                        .padding(.leading, wide ? Theme.Space.s : Theme.Space.flush)
                }
            }
            .padding(.bottom, Theme.Space.xs)
            GeometryReader { geo in
                let cell = max(Theme.CalendarGrid.monthCell, geo.size.height / CGFloat(rows))
                ScrollView(.vertical) {
                    VStack(spacing: Theme.Space.flush) {
                        ForEach(0..<rows, id: \.self) { r in
                            HStack(spacing: Theme.Space.flush) {
                                ForEach(0..<7, id: \.self) { c in
                                    dayCell(days[r * 7 + c])
                                        .frame(maxWidth: .infinity)
                                        .frame(height: cell)
                                        .overlay(alignment: .leading) {
                                            if c > 0 { Rectangle().fill(Theme.CalendarGrid.line).frame(width: pixel) }
                                        }
                                }
                            }
                            .overlay(alignment: .top) {
                                Rectangle().fill(Theme.CalendarGrid.line).frame(height: pixel)
                            }
                        }
                    }
                }
                .scrollIndicators(.hidden)
                .scrollDisabled(cell * CGFloat(rows) <= geo.size.height || dragging != nil)
                .scrollPosition($scroll)
                .onScrollGeometryChange(for: CGFloat.self) { $0.contentOffset.y + $0.contentInsets.top } action: { _, y in dropMap.scrolled = y }
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .named(Self.space)) } action: { dropMap.grid = $0 }
                .onChange(of: cell, initial: true) { _, c in dropMap.cell = c }
                .onChange(of: rows, initial: true) { _, r in dropMap.rows = r }
            }
        }
        .coordinateSpace(.named(Self.space))
        // a tick at each new day it would land on
        .onChange(of: landing) { old, new in if old != nil, new != nil { Haptics.selection() } }
        .task(id: autoscroll) { await scrollWhileHeld(autoscroll) }
    }

    private func dayCell(_ day: Date) -> some View {
        let inMonth = calendar.isDate(day, equalTo: anchor, toGranularity: .month)
        let today = calendar.isDateInToday(day)
        // where a dragged to-do would land, first in its day, which then shows a line fewer
        let ghost = landed.flatMap { calendar.isDate($0.start, inSameDayAs: day) ? $0 : nil }
        let lines = Theme.CalendarGrid.monthLines - (ghost == nil ? 0 : 1)
        let fit = TodoCalendar.fit(TodoCalendar.on(day, entries, calendar: calendar), lines: lines)
        return VStack(alignment: wide ? .leading : .center, spacing: Theme.Space.xxs) {
            Text(verbatim: String(calendar.component(.day, from: day)))
                .font(today ? Theme.Font.captionHeavy : Theme.Font.caption)
                .foregroundStyle(today ? Theme.onSolid : inMonth ? Color.primary : Color.secondary)
                .scaledFrame(width: Theme.Size.iconS, height: Theme.Size.iconS)
                .background {
                    if today { Circle().fill(Theme.accent) }
                }
                .frame(maxWidth: .infinity, alignment: wide ? .leading : .center)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                if let ghost {
                    CalendarEntryChip(entry: ghost, compact: !wide, showsTime: wide)
                        .calendarLanding()
                }
                ForEach(fit.shown) { e in
                    carried(CalendarEntryChip(entry: e, compact: !wide, showsTime: wide), e)
                }
                if fit.more > 0 {
                    Text(wide ? String(localized: "\(fit.more) more") : "+\(fit.more)")
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .padding(.leading, Theme.Space.xs)
                }
            }
        }
        .padding(.horizontal, Theme.Space.xxs)
        .padding(.top, Theme.Space.xs)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .opacity(inMonth || ghost != nil ? 1 : Theme.CalendarGrid.outsideFade)
        .background(ghost != nil ? Theme.accent.opacity(Theme.CalendarGrid.tint) : .clear)
        .contentShape(.rect)
        .onTapGesture { pickDay(day) }
        .accessibilityElement(children: wide ? .contain : .combine)
        .accessibilityLabel(day.formatted(date: .complete, time: .omitted))
        .accessibilityIdentifier("todo.calendar.monthDay")
    }
}
