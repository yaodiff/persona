#if os(iOS)
import AppIntents
import SwiftUI
import WidgetKit

/// Persona's widgets: one widget, one job (docs/apple/home-widgets.md).
enum HomeKind: String, CaseIterable, Codable, Sendable {
    case agents, today, todos, usage, computers, record, health, inbox

    /// The kind's name in WidgetKit (its configurations and reloads); never change one.
    var widgetKind: String { "persona.home.\(rawValue)" }

    var title: LocalizedStringKey {
        switch self {
        case .agents: "Agents [Home Widgets]"
        case .today: "Calendar [Home Widgets]"
        case .todos: "To-Dos [Home Widgets]"
        case .usage: "Usage [Home Widgets]"
        case .computers: "Computers [Home Widgets]"
        case .record: "Record [Home Widgets]"
        case .health: "Health [Home Widgets]"
        case .inbox: "Inbox [Home Widgets]"
        }
    }

    /// What the widget gallery says about it.
    var summary: LocalizedStringKey {
        switch self {
        case .agents: "How many agents need you, and the one waiting longest. [Home Widgets]"
        case .today: "Today, this week or this month on your calendar. [Home Widgets]"
        case .todos: "Today's to-dos. Tick them off right on the widget. [Home Widgets]"
        case .usage: "How full your subscription windows are, and today's tokens. [Home Widgets]"
        case .computers: "Your computers: how busy they are, and which are offline. [Home Widgets]"
        case .record: "Start a meeting recording with one tap. [Home Widgets]"
        case .health: "Today's steps, last night's sleep and your resting heart rate. [Home Widgets]"
        case .inbox: "Messages that need you, and the newest. [Home Widgets]"
        }
    }

    /// The sizes it comes in: one action fills no large widget, and lists start at medium.
    var families: [WidgetFamily] {
        let lock: [WidgetFamily] = [.accessoryCircular, .accessoryRectangular, .accessoryInline]
        switch self {
        case .record: return [.systemSmall, .systemMedium, .accessoryCircular, .accessoryRectangular]
        default: return [.systemSmall, .systemMedium, .systemLarge] + lock
        }
    }

    /// Inbox messages and health numbers are the user's own: the system blurs them while the phone is locked.
    var isPrivate: Bool { self == .inbox || self == .health }
}

/// What a widget draws at one moment: the snapshot, the persona's pictures, and the settings from Edit Widget.
struct HomeEntry: TimelineEntry {
    var date: Date
    var snapshot: HomeSnapshot
    var pictures: HomeFacePictures?
    /// Edit Widget › Show (Agents): nil, all of them.
    var agentKind: HomeSnapshot.AgentKind?
    /// Edit Widget › Computer (Agents, Computers): nil, every computer.
    var machine: String?
    /// Edit Widget › Account (Usage): nil, the fullest.
    var account: String?
    /// Edit Widget › View (Calendar): where it starts; what its switch and a tapped day left for it
    /// (`HomeCalendarState`), and the key they are kept under.
    var calendarView: HomeCalendarView = .today
    var calendarSlot: HomeCalendarState.Slot?
    var calendarKey = ""
    var relevance: TimelineEntryRelevance?

    var context: HomeContext {
        HomeContext(snapshot: snapshot, date: date, agentKind: agentKind, machine: machine, account: account,
                    calendarView: calendarView, calendarSlot: calendarSlot, calendarKey: calendarKey)
    }
}

/// The snapshot as it is at the entry's date, filtered by the widget's settings.
struct HomeContext {
    var snapshot: HomeSnapshot
    var date: Date
    var agentKind: HomeSnapshot.AgentKind?
    var machine: String?
    var account: String?
    var calendarView: HomeCalendarView = .today
    var calendarSlot: HomeCalendarState.Slot?
    var calendarKey = ""

    var stale: Bool { snapshot.isStale(at: date) }
    var now: Double { date.timeIntervalSince1970 }

    var agentRows: [HomeSnapshot.AgentRow] {
        snapshot.agents.rows.filter { (agentKind == nil || $0.kind == agentKind) && (machine == nil || $0.machine == machine) }
    }

    /// The counts of the rows shown; with no filter, also the ones past the cap.
    var agentCounts: HomeSnapshot.Counts {
        var c = HomeSnapshot.Counts()
        for row in agentRows {
            switch row.state {
            case .needsYou: c.needsYou += 1
            case .working: c.working += 1
            case .done: c.done += 1
            }
        }
        if agentKind == nil, machine == nil {
            c.needsYou += snapshot.agents.more.needsYou
            c.working += snapshot.agents.more.working
            c.done += snapshot.agents.more.done
        }
        return c
    }

    var machines: [HomeSnapshot.Machine] {
        machine.map { id in snapshot.machines.filter { $0.id == id } } ?? snapshot.machines
    }

    var accounts: [HomeSnapshot.Account] {
        let all = snapshot.usage?.accounts ?? []
        guard let account else { return all }
        return all.filter { $0.id == account }
    }

    /// The rest of today. The snapshot's list is the day it was written on; after midnight, until the app writes
    /// again, today's come from its days (none without them: yesterday's are not today's).
    var events: [HomeSnapshot.Event] {
        guard let today = snapshot.today else { return [] }
        let written = Date(timeIntervalSince1970: snapshot.at)
        let list = snapshot.at > 0 && !Calendar.current.isDate(written, inSameDayAs: date)
            ? today.days?.byDay[HomeDays.key(date)]?.events ?? []
            : today.events
        return list.filter { $0.allDay || $0.end > now }
    }

    /// The view and the day the Calendar widget shows now.
    var calendarShown: (view: HomeCalendarView, day: Date) {
        HomeCalendarState.shown(calendarSlot, configured: calendarView, at: date)
    }

    /// What the Calendar widget knows of a day: its events (today, the rest of them) and how many there are beyond
    /// those; nil when the snapshot does not cover it.
    func day(_ day: Date) -> (events: [HomeSnapshot.Event], more: Int)? {
        if Calendar.current.isDate(day, inSameDayAs: date) { return (events, 0) }
        let key = HomeDays.key(day)
        guard let days = snapshot.today?.days, days.covers(key) else { return nil }
        let d = days.byDay[key]
        return (d?.events ?? [], max(0, (d?.count ?? 0) - (d?.events.count ?? 0)))
    }

    /// A day's dot: it has events (today, also the ones already over).
    func hasEvents(_ day: Date) -> Bool {
        if let d = snapshot.today?.days?.byDay[HomeDays.key(day)] { return d.count > 0 }
        return Calendar.current.isDate(day, inSameDayAs: date) && !events.isEmpty
    }

    var nextEvent: HomeSnapshot.Event? { events.first { !$0.allDay } }

    /// The event a recording started now would be named after (MeetingCalendar's: under way, or about to be).
    var eventNow: HomeSnapshot.Event? { events.first { !$0.allDay && $0.start <= now + 5 * 60 } }

    /// The persona in a widget's corner: how that widget's subject is.
    func mood(_ kind: HomeKind) -> HomeMood {
        if stale || !snapshot.paired { return .offline }
        switch kind {
        case .agents:
            return HomeMood.of(agentCounts, answering: agentKind != .coding && snapshot.agents.assistantAnswering, stale: false)
        case .today:
            if let next = nextEvent, next.start - now < 15 * 60 { return .working }
            return .idle
        case .todos:
            let t = snapshot.todos ?? .init()
            return t.overdue > 0 ? .needsYou : t.count == 0 ? .done : .idle
        case .usage:
            let top = accounts.first?.windows.map(\.percent).max() ?? 0
            return top >= HomeWidgetTokens.fullAt ? .needsYou : top >= HomeWidgetTokens.warnAt ? .working : .idle
        case .computers:
            let list = machines
            if !list.isEmpty, list.allSatisfy({ !$0.online }) { return .offline }
            if list.contains(where: { $0.needsYou > 0 }) { return .needsYou }
            return list.contains(where: { $0.working > 0 }) ? .working : .idle
        case .record:
            return snapshot.recording != nil ? .working : .idle
        case .health:
            if let h = snapshot.health, let steps = h.steps, let avg = h.stepsAverage, avg > 0, steps >= avg { return .done }
            return .idle
        case .inbox:
            return (snapshot.inbox?.needsYou ?? 0) > 0 ? .needsYou : .idle
        }
    }
}

/// Where a tap goes: the page the widget comes from, the most urgent agent's chat.
enum HomeLink {
    static func url(_ kind: HomeKind, _ c: HomeContext) -> URL? {
        if kind == .agents, let top = c.agentRows.first { return session(top) }
        return URL(string: "persona://widget/\(kind.rawValue)")
    }

    static func session(_ row: HomeSnapshot.AgentRow) -> URL? {
        let parts = row.id.split(separator: "/", maxSplits: 1).map(String.init)
        guard parts.count == 2 else { return nil }
        var u = URLComponents(string: "persona://open")
        u?.queryItems = [URLQueryItem(name: "machine", value: parts[0]), URLQueryItem(name: "session", value: parts[1])]
        return u?.url
    }
}

// MARK: - Every widget

/// One widget at one size: the Home Screen families draw the kind's own view, the Lock Screen's the accessory one.
struct HomeWidgetView: View {
    var kind: HomeKind
    var entry: HomeEntry
    /// The gallery's size (a view outside a widget has no family of its own).
    var size: WidgetFamily?
    @Environment(\.widgetFamily) private var widgetFamily

    private var family: WidgetFamily { size ?? widgetFamily }

    var body: some View {
        let c = entry.context
        Group {
            switch family {
            case .accessoryCircular, .accessoryRectangular, .accessoryInline:
                HomeLockView(kind: kind, entry: entry, family: family)
            default:
                if !c.snapshot.paired {
                    VStack(alignment: .leading, spacing: 0) {
                        HomeHeader(title: kind.title, color: color)
                        Spacer(minLength: 0)
                        HomeEmpty(text: "Open Persona to connect [Home Widgets]", pictures: entry.pictures, horizontal: family != .systemSmall)
                    }
                } else {
                    home(c)
                        .opacity(c.stale ? HomeWidgetTokens.staleOpacity : 1)
                        .overlay(alignment: .topLeading) {
                            // Under the name, where every widget leaves room before its hero.
                            if c.stale { HomeStaleNote(since: Date(timeIntervalSince1970: c.snapshot.at), now: entry.date).offset(y: HomeWidgetTokens.faceCorner + HomeWidgetTokens.tight) }
                        }
                }
            }
        }
        .privacySensitive(kind.isPrivate)
        .widgetURL(HomeLink.url(kind, c))
        #if WIDGET_EXTENSION
        .environment(\.locale, LanguageStore.drawingLocale) // the app's interface language (docs/apple/ios.md「Interface language」)
        #endif
        .containerBackground(for: .widget) { HomeWidgetTokens.surface }
    }

    /// The color of the widget's name.
    private var color: Color {
        switch kind {
        case .agents: HomeWidgetTokens.needsYou.dot
        case .today: HomeWidgetTokens.today
        case .todos: HomeWidgetTokens.todos
        case .usage: HomeWidgetTokens.usageCalm
        case .computers: HomeWidgetTokens.computerCalm
        case .record: HomeWidgetTokens.recording
        case .health: HomeWidgetTokens.steps
        case .inbox: HomeWidgetTokens.inbox.dot
        }
    }

    @ViewBuilder private func home(_ c: HomeContext) -> some View {
        let p = entry.pictures
        switch kind {
        case .agents: HomeAgentsWidget(c: c, family: family, pictures: p)
        case .today: HomeTodayWidget(c: c, family: family, pictures: p)
        case .todos: HomeTodosWidget(c: c, family: family, pictures: p)
        case .usage: HomeUsageWidget(c: c, family: family, pictures: p)
        case .computers: HomeComputersWidget(c: c, family: family, pictures: p)
        case .record: HomeRecordWidget(c: c, family: family, pictures: p)
        case .health: HomeHealthWidget(c: c, family: family, pictures: p)
        case .inbox: HomeInboxWidget(c: c, family: family, pictures: p)
        }
    }
}

// MARK: - Agents

/// How many agents need you, big; the one waiting longest beside it; the rest in a word.
private struct HomeAgentsWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    private var counts: HomeSnapshot.Counts { c.agentCounts }

    /// The most urgent category with anyone in it: its number, words and color.
    private var lead: (n: Int, words: String, color: Color)? {
        if counts.needsYou > 0 { return (counts.needsYou, StateName.needsYou.text, HomeWidgetTokens.needsYou.dot) }
        if counts.working > 0 { return (counts.working, StateName.working.text, HomeWidgetTokens.working.dot) }
        if counts.done > 0 { return (counts.done, interfaceText("Just Finished [Home Widgets]"), HomeWidgetTokens.done.dot) } // perf-ok: drawn once per timeline entry, in the widget extension
        return nil
    }

    /// The categories after the lead one, in a line ("3 working · 1 just finished").
    private var rest: String {
        var parts: [String] = []
        if counts.needsYou > 0, counts.working > 0 { parts.append(StateName.count(.working, counts.working)) }
        if counts.done > 0, counts.needsYou + counts.working > 0 { parts.append(interfaceText("\(counts.done) just finished [Home Widgets]")) } // perf-ok: drawn once per timeline entry, in the widget extension
        return parts.joined(separator: " · ")
    }

    var body: some View {
        let header = HomeHeader(title: HomeKind.agents.title, color: lead?.color ?? HomeWidgetTokens.needsYou.dot,
                                mood: lead == nil ? nil : c.mood(.agents), pictures: pictures)
        VStack(alignment: .leading, spacing: 0) {
            header
            if let lead {
                switch family {
                case .systemSmall:
                    Spacer(minLength: 0)
                    count(lead)
                case .systemMedium:
                    Spacer(minLength: 0)
                    HStack(alignment: .bottom, spacing: HomeWidgetTokens.halves) {
                        count(lead).frame(maxWidth: .infinity, alignment: .leading)
                        if let top = c.agentRows.first { focus(top).frame(maxWidth: .infinity, alignment: .leading) }
                    }
                default:
                    Spacer().frame(height: HomeWidgetTokens.row)
                    stats
                    Spacer().frame(height: HomeWidgetTokens.row * 1.5)
                    VStack(alignment: .leading, spacing: HomeWidgetTokens.row) {
                        ForEach(c.agentRows.prefix(5)) { HomeAgentRowView(row: $0, now: c.date) }
                    }
                    Spacer(minLength: 0)
                }
            } else {
                Spacer(minLength: 0)
                HomeEmpty(text: "Nothing needs you [Home Widgets]", mood: c.mood(.agents), pictures: pictures, horizontal: family != .systemSmall)
                Spacer(minLength: 0)
            }
        }
    }

    private func count(_ lead: (n: Int, words: String, color: Color)) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HomeHero(text: "\(lead.n)", color: lead.color)
            Text(verbatim: lead.words).font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
            if !rest.isEmpty {
                Text(verbatim: rest).font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.secondary).padding(.top, HomeWidgetTokens.hair)
            }
        }
    }

    /// The one agent waiting longest (or working longest), as the system's Up Next.
    private func focus(_ row: HomeSnapshot.AgentRow) -> some View {
        VStack(alignment: .leading, spacing: HomeWidgetTokens.hair) {
            if let words = row.ask ?? row.detail {
                Text(verbatim: words).font(HomeWidgetTokens.captionStrong).lineLimit(1).homeTone(HomeAgentRowView.color(row.state))
            }
            Text(verbatim: row.title).font(HomeWidgetTokens.headline).lineLimit(2).homeInk(.primary)
            Text(verbatim: [row.place, row.since.map { homeAgo($0, now: c.date) }].compactMap { $0 }.joined(separator: " · "))
                .font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.tertiary)
        }
    }

    /// The large widget's three numbers side by side, as Fitness shows Move, Exercise and Stand.
    private var stats: some View {
        HStack(alignment: .top, spacing: HomeWidgetTokens.flush) {
            stat(counts.needsYou, StateName.needsYou.text, HomeWidgetTokens.needsYou.dot)
            stat(counts.working, StateName.working.text, HomeWidgetTokens.working.dot)
            stat(counts.done, interfaceText("Just Finished [Home Widgets]"), HomeWidgetTokens.done.dot) // perf-ok: drawn once per timeline entry, in the widget extension
        }
    }

    private func stat(_ n: Int, _ words: String, _ color: Color) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HomeHero(text: "\(n)", font: HomeWidgetTokens.heroMedium, color: n > 0 ? color : nil).opacity(n > 0 ? 1 : 0.3)
            Text(verbatim: words).font(HomeWidgetTokens.captionStrong).lineLimit(1).homeInk(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// An agent in the large widget's list: its state's dot, its title, what it waits for or does, how long.
private struct HomeAgentRowView: View {
    var row: HomeSnapshot.AgentRow
    var now: Date

    static func color(_ s: HomeSnapshot.AgentState) -> Color {
        switch s {
        case .needsYou: HomeWidgetTokens.needsYou.dot
        case .working: HomeWidgetTokens.working.dot
        case .done: HomeWidgetTokens.done.dot
        }
    }

    var body: some View {
        Link(destination: HomeLink.session(row) ?? URL(string: "persona://widget/agents")!) {
            HStack(alignment: .firstTextBaseline, spacing: HomeWidgetTokens.gap) {
                HomeDot(color: Self.color(row.state))
                VStack(alignment: .leading, spacing: 0) {
                    Text(verbatim: row.title).font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
                    if let words = row.ask ?? row.detail {
                        Text(verbatim: words).font(HomeWidgetTokens.body).lineLimit(1).homeInk(.secondary)
                    }
                }
                Spacer(minLength: HomeWidgetTokens.gap)
                if let since = row.since {
                    Text(verbatim: homeAgo(since, now: now)).font(HomeWidgetTokens.caption).homeInk(.tertiary)
                }
            }
        }
        .buttonStyle(.plain)
    }
}

// MARK: - Today

/// The Calendar widget: today's page (the date, then what comes next), or the week or the month
/// (`HomeCalendarViews`), switched at the top right; the small one is today's page alone.
private struct HomeTodayWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    var body: some View {
        let shown = c.calendarShown
        switch family {
        case .systemSmall:
            VStack(alignment: .leading, spacing: 0) {
                dateHead(compact: true)
                Spacer(minLength: HomeWidgetTokens.tight)
                if let next = c.nextEvent { HomeEventView(event: next, lines: 2) } else { none }
            }
        default:
            switch shown.view {
            case .today: if family == .systemMedium { mediumToday } else { largeToday }
            case .week: HomeWeekView(c: c, day: shown.day, large: family != .systemMedium, pictures: pictures)
            case .month: HomeMonthView(c: c, day: shown.day, large: family != .systemMedium, pictures: pictures)
            }
        }
    }

    /// The date, and beside it the switch over the next events, as many as fit.
    private var mediumToday: some View {
        HStack(alignment: .top, spacing: HomeWidgetTokens.halves) {
            dateHead(compact: false).frame(width: HomeWidgetTokens.ringHero, alignment: .leading)
                .frame(maxHeight: .infinity, alignment: .top)
            VStack(alignment: .trailing, spacing: HomeWidgetTokens.tight) {
                HStack(spacing: HomeWidgetTokens.tight) {
                    Spacer(minLength: 0)
                    HomeCalendarSwitch(c: c, shown: .today)
                    corner
                }
                ViewThatFits(in: .vertical) {
                    upcoming(3)
                    upcoming(2)
                    upcoming(1)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            }
        }
    }

    /// The rest of the day as a day view, under the date and the switch.
    private var largeToday: some View {
        VStack(alignment: .leading, spacing: HomeWidgetTokens.gap) {
            HStack(alignment: .top, spacing: HomeWidgetTokens.tight) {
                dateHead(compact: false)
                Spacer()
                HomeCalendarSwitch(c: c, shown: .today)
                corner
            }
            if c.events.isEmpty { Spacer(); none; Spacer() } else { HomeDayTimeline(c: c) }
        }
    }

    /// The next `count` events, and how many more there are.
    private func upcoming(_ count: Int) -> some View {
        let events = c.events
        let shown = Array(events.prefix(count))
        return VStack(alignment: .leading, spacing: HomeWidgetTokens.gap) {
            ForEach(shown, id: \.self) { HomeEventView(event: $0, lines: 1) }
            if events.isEmpty { none }
            if shown.count < events.count { more(events.count - shown.count) }
        }
    }

    /// Weekday in the widget's coral, the day big (Calendar's), the persona at the corner.
    private func dateHead(compact: Bool) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text(c.date, format: .dateTime.weekday(.wide)).font(HomeWidgetTokens.title).textCase(.uppercase).lineLimit(1).homeTone(HomeWidgetTokens.today)
                if compact {
                    Spacer(minLength: 0)
                    HomeFace(size: HomeWidgetTokens.faceCorner, mood: c.mood(.today), pictures: pictures)
                }
            }
            Text(verbatim: "\(Calendar.current.component(.day, from: c.date))").font(HomeWidgetTokens.day).lineLimit(1).homeInk(.primary)
            if !compact {
                Text(c.date, format: .dateTime.month(.wide)).font(HomeWidgetTokens.body).homeInk(.secondary)
            }
        }
    }

    /// The persona at the widget's top right corner (the medium and large ones: the small one has it beside the weekday).
    private var corner: some View {
        HomeFace(size: HomeWidgetTokens.faceCorner, mood: c.mood(.today), pictures: pictures)
    }

    @ViewBuilder private var none: some View {
        if c.snapshot.today?.source == nil {
            Text("Allow Calendars in Persona to see your events [Home Widgets]").font(HomeWidgetTokens.body).lineLimit(3).homeInk(.secondary)
        } else {
            Text("No more events today [Home Widgets]").font(HomeWidgetTokens.headline).lineLimit(2).homeInk(.secondary)
        }
    }

    private func more(_ n: Int) -> some View {
        Text("\(n) more events [Home Widgets]").font(HomeWidgetTokens.caption).homeInk(.tertiary)
    }
}

/// An event: the coral bar, its title, its time.
private struct HomeEventView: View {
    var event: HomeSnapshot.Event
    var lines: Int

    var body: some View {
        HStack(alignment: .top, spacing: HomeWidgetTokens.gap - HomeWidgetTokens.hair) {
            RoundedRectangle(cornerRadius: HomeWidgetTokens.hair).frame(width: HomeWidgetTokens.bar).homeTone(HomeWidgetTokens.today)
            VStack(alignment: .leading, spacing: 0) {
                Text(verbatim: event.title).font(HomeWidgetTokens.headline).lineLimit(lines).homeInk(.primary)
                Group {
                    if event.allDay {
                        Text("All Day [Home Widgets]")
                    } else {
                        Text(Date(timeIntervalSince1970: event.start)..<Date(timeIntervalSince1970: max(event.start, event.end)), format: .interval.hour().minute())
                    }
                }
                .font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.secondary)
            }
        }
        .fixedSize(horizontal: false, vertical: true)
    }
}

/// The rest of the day as the calendar's day view: an hour a line, each event a block as long as it lasts, a red
/// line at now.
private struct HomeDayTimeline: View {
    var c: HomeContext
    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        let cal = Calendar.current
        let firstHour = cal.component(.hour, from: c.date)
        let lastHour = min(24, max(firstHour + 6, cal.component(.hour, from: Date(timeIntervalSince1970: c.events.map(\.end).max() ?? c.now)) + 1))
        let hours = Array(firstHour...lastHour)
        GeometryReader { geo in
            let perHour = geo.size.height / CGFloat(max(1, hours.count - 1))
            let gutter = HomeWidgetTokens.ringSmall * 0.8
            let top = cal.date(bySettingHour: firstHour, minute: 0, second: 0, of: c.date) ?? c.date
            let y = { (t: Double) in CGFloat((t - top.timeIntervalSince1970) / 3600) * perHour }
            ZStack(alignment: .topLeading) {
                ForEach(hours, id: \.self) { h in
                    HStack(spacing: HomeWidgetTokens.tight) {
                        Text(cal.date(bySettingHour: h % 24, minute: 0, second: 0, of: c.date) ?? c.date, format: .dateTime.hour())
                            .font(HomeWidgetTokens.caption).monospacedDigit().lineLimit(1).homeInk(.tertiary)
                            .frame(width: gutter - HomeWidgetTokens.tight, alignment: .leading)
                        Rectangle().frame(height: HomeWidgetTokens.hairline).foregroundStyle(HomeWidgetTokens.rule)
                    }
                    .offset(y: CGFloat(h - firstHour) * perHour - HomeWidgetTokens.gap)
                }
                ForEach(c.events.filter { !$0.allDay }, id: \.self) { e in
                    let top = max(0, y(e.start)), bottom = min(geo.size.height, y(e.end))
                    // Tall enough for two lines: its title and its time; shorter (under about an hour): its title alone,
                    // on one line, never past the block (the block is at least that line tall).
                    let long = bottom - top >= HomeWidgetTokens.timelineLine * 2
                    VStack(alignment: .leading, spacing: 0) {
                        Text(verbatim: e.title).font(HomeWidgetTokens.captionStrong).lineLimit(1)
                        if long {
                            Text(Date(timeIntervalSince1970: e.start)..<Date(timeIntervalSince1970: e.end), format: .interval.hour().minute())
                                .font(HomeWidgetTokens.caption).lineLimit(1).opacity(0.8)
                        }
                    }
                    .homeTone(HomeWidgetTokens.today)
                    .padding(.horizontal, HomeWidgetTokens.gap - HomeWidgetTokens.hair)
                    .padding(.vertical, long ? HomeWidgetTokens.hair : 0)
                    .frame(width: geo.size.width - gutter, height: max(HomeWidgetTokens.timelineLine, bottom - top), alignment: long ? .topLeading : .leading)
                    .clipped()
                    .background(alignment: .leading) {
                        ZStack(alignment: .leading) {
                            // Opaque under the tint, so the hour lines do not run through an event.
                            if mode == .fullColor { HomeWidgetTokens.surface }
                            RoundedRectangle(cornerRadius: HomeWidgetTokens.tight, style: .continuous)
                                .fill(mode == .fullColor ? HomeWidgetTokens.today.opacity(0.16) : Color.white.opacity(0.18))
                            Rectangle().frame(width: HomeWidgetTokens.bar - 1).homeTone(HomeWidgetTokens.today)
                        }
                        .clipShape(RoundedRectangle(cornerRadius: HomeWidgetTokens.tight, style: .continuous))
                    }
                    .offset(x: gutter, y: top)
                }
                // Now: from the hours' column to the widget's margin, never past it.
                HStack(spacing: HomeWidgetTokens.flush) {
                    Circle().frame(width: HomeWidgetTokens.dot, height: HomeWidgetTokens.dot)
                    Rectangle().frame(height: HomeWidgetTokens.hair)
                }
                .frame(width: geo.size.width - gutter + HomeWidgetTokens.dot / 2)
                .homeTone(HomeWidgetTokens.overdue)
                .offset(x: gutter - HomeWidgetTokens.dot / 2, y: y(c.now) - HomeWidgetTokens.dot / 2)
            }
        }
        .padding(.top, HomeWidgetTokens.gap)
    }
}

// MARK: - To-dos

/// Today's to-dos: how many, big (Reminders'); the medium and large ones tick them off.
private struct HomeTodosWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    private var todos: HomeSnapshot.Todos { c.snapshot.todos ?? .init() }
    private var open: Int { todos.count - todos.items.filter { $0.tick == .ticking }.count }

    var body: some View {
        switch family {
        case .systemSmall:
            VStack(alignment: .leading, spacing: 0) {
                HStack(alignment: .top) {
                    badge
                    Spacer()
                    if open > 0 { HomeHero(text: "\(open)", font: HomeWidgetTokens.heroMedium) }
                }
                Spacer(minLength: 0)
                if open == 0 { allDone } else { name }
            }
        case .systemMedium:
            HStack(alignment: .top, spacing: HomeWidgetTokens.halves) {
                VStack(alignment: .leading, spacing: 0) {
                    badge
                    Spacer(minLength: 0)
                    if open > 0 { HomeHero(text: "\(open)", font: HomeWidgetTokens.heroMedium) }
                    name
                }
                .frame(width: HomeWidgetTokens.ringHero, alignment: .leading)
                VStack(alignment: .leading, spacing: HomeWidgetTokens.gap) {
                    ForEach(todos.items.prefix(4)) { HomeTodoRow(todo: $0) }
                    if todos.items.isEmpty { allDoneFace }
                    Spacer(minLength: 0)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        default:
            VStack(alignment: .leading, spacing: HomeWidgetTokens.row) {
                HStack(alignment: .center, spacing: HomeWidgetTokens.gap) {
                    badge
                    name
                    Spacer()
                    if open > 0 { HomeHero(text: "\(open)", font: HomeWidgetTokens.heroSmall) }
                }
                ForEach(todos.items.prefix(8)) { HomeTodoRow(todo: $0, large: true) }
                if todos.items.isEmpty { Spacer(); allDoneFace; Spacer() }
                Spacer(minLength: 0)
                if todos.count > 8 { Text("\(todos.count - 8) more [Home Widgets]").font(HomeWidgetTokens.caption).homeInk(.tertiary) }
            }
        }
    }

    /// The widget's mark, a filled orange disc as Reminders' lists have.
    private var badge: some View { HomeTodoBadge() }

    private var name: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("Today's To-Dos [Home Widgets]").font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
            if todos.overdue > 0 {
                Text("\(todos.overdue) overdue [Home Widgets]").font(HomeWidgetTokens.captionStrong).homeTone(HomeWidgetTokens.overdue)
            } else if family == .systemSmall, let first = todos.items.first {
                Text(verbatim: first.title).font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.secondary)
            }
        }
    }

    private var allDone: some View {
        HStack(alignment: .center, spacing: HomeWidgetTokens.gap) {
            HomeFace(size: HomeWidgetTokens.faceEmpty * 0.8, mood: .done, pictures: pictures)
            Text("All done for today [Home Widgets]").font(HomeWidgetTokens.headline).lineLimit(2).homeInk(.secondary)
        }
    }

    private var allDoneFace: some View {
        HomeEmpty(text: "All done for today [Home Widgets]", mood: .done, pictures: pictures, horizontal: true)
    }
}

/// The To-dos widget's mark, a filled orange disc as Reminders' lists have; the mark knocked out of the disc so it
/// still reads where the system paints the disc in one color.
private struct HomeTodoBadge: View {
    @Environment(\.widgetRenderingMode) private var mode
    var body: some View {
        let full = mode == .fullColor
        ZStack {
            Circle().fill(full ? HomeWidgetTokens.todos : .white)
            HomeWidgetTokens.symbol(HomeWidgetTokens.todoSymbol).font(HomeWidgetTokens.headline)
                .foregroundStyle(full ? Color.white : Color.black)
                .blendMode(full ? .normal : .destinationOut)
        }
        .compositingGroup()
        .frame(width: HomeWidgetTokens.faceEmpty * 0.72, height: HomeWidgetTokens.faceEmpty * 0.72)
        .homeAccent()
    }
}

/// A to-do with its box: a tap ticks it (`CompleteTodoIntent`, which runs in the app and tells the hub). Ticked, it
/// shows done at once; refused, it says so in red and a tap tries again (docs/apple/home-widgets.md「Ticking off a to-do」).
private struct HomeTodoRow: View {
    var todo: HomeSnapshot.Todo
    var large = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: HomeWidgetTokens.gap) {
            Button(intent: CompleteTodoIntent(todoID: todo.id)) {
                HomeWidgetTokens.symbol(mark).font(large ? HomeWidgetTokens.headline : HomeWidgetTokens.body).modifier(MarkTone(todo: todo))
            }
            .buttonStyle(.plain)
            .accessibilityLabel(Text("Complete [Home Widgets]"))
            VStack(alignment: .leading, spacing: 0) {
                Text(verbatim: todo.title).font(large ? HomeWidgetTokens.headline : HomeWidgetTokens.body).lineLimit(1)
                    .strikethrough(todo.tick == .ticking).homeInk(todo.tick == .ticking ? .tertiary : .primary)
                if todo.tick == .failed {
                    Text("Couldn't complete — tap to try again [Home Widgets]").font(HomeWidgetTokens.caption).lineLimit(1).homeTone(HomeWidgetTokens.overdue)
                } else if large, let due = todo.due {
                    Text(Date(timeIntervalSince1970: due), format: .dateTime.hour().minute()).font(HomeWidgetTokens.caption)
                        .modifier(DueTone(overdue: todo.overdue))
                }
            }
            Spacer(minLength: 0)
        }
    }

    private var mark: String {
        switch todo.tick {
        case .ticking: HomeWidgetTokens.todoDone
        case .failed: HomeWidgetTokens.todoFailed
        case nil: HomeWidgetTokens.todoOpen
        }
    }

    private struct MarkTone: ViewModifier {
        var todo: HomeSnapshot.Todo
        func body(content: Content) -> some View {
            switch todo.tick {
            case .failed: content.homeTone(HomeWidgetTokens.overdue)
            case .ticking: content.homeTone(HomeWidgetTokens.todos)
            case nil: if todo.overdue { content.homeTone(HomeWidgetTokens.overdue) } else { content.homeInk(.tertiary) }
            }
        }
    }

    private struct DueTone: ViewModifier {
        var overdue: Bool
        func body(content: Content) -> some View {
            if overdue { content.homeTone(HomeWidgetTokens.overdue) } else { content.homeInk(.secondary) }
        }
    }
}

// MARK: - Usage

/// How full the subscription windows are, as rings; the large one adds today's tokens by the hour.
private struct HomeUsageWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    private var usage: HomeSnapshot.Usage { c.snapshot.usage ?? .init() }
    private var top: HomeSnapshot.Account? { c.accounts.first }

    static func color(_ percent: Double) -> Color {
        percent >= HomeWidgetTokens.fullAt ? HomeWidgetTokens.usageFull : percent >= HomeWidgetTokens.warnAt ? HomeWidgetTokens.usageWarn : HomeWidgetTokens.usageCalm
    }

    var body: some View {
        let header = HomeHeader(title: HomeKind.usage.title, color: Self.color(top?.windows.map(\.percent).max() ?? 0),
                                mood: c.mood(.usage), pictures: pictures)
        VStack(alignment: .leading, spacing: 0) {
            header
            if c.accounts.isEmpty, usage.tokensToday == 0 {
                Spacer(minLength: 0)
                HomeEmpty(text: "No subscription windows yet [Home Widgets]", pictures: pictures, horizontal: family != .systemSmall)
                Spacer(minLength: 0)
            } else {
                switch family {
                case .systemSmall:
                    Spacer(minLength: 0)
                    if let top { hero(top) } else { tokensHero }
                case .systemMedium:
                    Spacer(minLength: 0)
                    HStack(alignment: .top, spacing: HomeWidgetTokens.flush) {
                        ForEach(c.accounts.prefix(3)) { HomeAccountRings(account: $0).frame(maxWidth: .infinity) }
                    }
                    Spacer(minLength: 0)
                default:
                    Spacer().frame(height: HomeWidgetTokens.row)
                    tokensHero
                    Spacer().frame(height: HomeWidgetTokens.row)
                    if !usage.hourly.isEmpty { HomeHourChart(hours: usage.hourly, now: c.date) }
                    Spacer(minLength: HomeWidgetTokens.row)
                    HStack(alignment: .top, spacing: HomeWidgetTokens.flush) {
                        ForEach(c.accounts.prefix(3)) { HomeAccountRings(account: $0).frame(maxWidth: .infinity) }
                    }
                }
            }
        }
    }

    /// The fullest window as one big ring, its percent inside; which window and when it resets beside it, the
    /// account under it.
    private func hero(_ account: HomeSnapshot.Account) -> some View {
        let w = account.windows.max { $0.percent < $1.percent }
        return VStack(alignment: .leading, spacing: HomeWidgetTokens.tight) {
            HStack(alignment: .bottom, spacing: HomeWidgetTokens.gap) {
                ZStack {
                    HomeRing(fraction: (w?.percent ?? 0) / 100, color: Self.color(w?.percent ?? 0))
                    Text(verbatim: "\(Int(w?.percent ?? 0))").font(HomeWidgetTokens.ringNumber).monospacedDigit().minimumScaleFactor(HomeWidgetTokens.heroMinScale).homeInk(.primary)
                }
                .frame(width: HomeWidgetTokens.ringHero * 0.76, height: HomeWidgetTokens.ringHero * 0.76)
                VStack(alignment: .leading, spacing: 0) {
                    if let w { Text(verbatim: HomeWindowName.of(w)).font(HomeWidgetTokens.captionStrong).lineLimit(1).homeInk(.secondary) }
                    if let reset = w?.resetsAt {
                        Text("Resets \(Date(timeIntervalSince1970: reset), format: .dateTime.hour().minute()) [Home Widgets]")
                            .font(HomeWidgetTokens.caption).lineLimit(1).minimumScaleFactor(HomeWidgetTokens.heroMinScale).homeInk(.tertiary)
                    }
                }
            }
            Text(verbatim: account.name).font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
        }
    }

    private var tokensHero: some View {
        HStack(alignment: .firstTextBaseline) {
            VStack(alignment: .leading, spacing: 0) {
                HomeHero(text: homeShort(usage.tokensToday), font: HomeWidgetTokens.heroMedium)
                Text("Tokens Today [Home Widgets]").font(HomeWidgetTokens.captionStrong).homeInk(.secondary)
            }
            Spacer()
            if let cost = usage.costToday {
                VStack(alignment: .trailing, spacing: 0) {
                    Text(cost, format: .currency(code: usage.currency)).font(HomeWidgetTokens.heroSmall).homeInk(.primary)
                    Text("Spent Today [Home Widgets]").font(HomeWidgetTokens.captionStrong).homeInk(.secondary)
                }
            }
        }
    }
}

/// A window's length in the interface language, in full ("5 hours", or its Chinese and Japanese words), the system's short form ("5h") where that
/// does not fit; the hub's own label ("5h") only for a window of unknown length.
enum HomeWindowName {
    static func of(_ w: HomeSnapshot.Window, short: Bool = false) -> String {
        guard w.minutes > 0 else { return w.label }
        let d = Duration.seconds(Double(w.minutes) * 60)
        return d.formatted(.units(allowed: [.days, .hours, .minutes], width: short ? .narrow : .wide, maximumUnitCount: 1))
    }

    /// "5 hours 86% · 7 days 61%", shortest window first.
    static func list(_ account: HomeSnapshot.Account, short: Bool) -> String {
        account.windows.sorted { $0.minutes < $1.minutes }.map { "\(of($0, short: short)) \(Int($0.percent))%" }.joined(separator: " · ")
    }
}

/// An account's windows in words, in full when they fit, else in the system's short form.
private struct HomeWindowList: View {
    var account: HomeSnapshot.Account
    var body: some View {
        ViewThatFits(in: .horizontal) {
            Text(verbatim: HomeWindowName.list(account, short: false))
            Text(verbatim: HomeWindowName.list(account, short: true))
        }
        .font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.secondary)
    }
}

/// An account as two rings, one in the other (the shortest window inside), its fullest percent in the middle, its
/// name under them: the system's Fitness rings.
private struct HomeAccountRings: View {
    var account: HomeSnapshot.Account

    var body: some View {
        let windows = account.windows.sorted { $0.minutes > $1.minutes }.prefix(2)
        let fullest = account.windows.max { $0.percent < $1.percent }
        VStack(spacing: HomeWidgetTokens.tight) {
            ZStack {
                ForEach(Array(windows.enumerated()), id: \.element) { i, w in
                    HomeRing(fraction: w.percent / 100, color: HomeUsageWidget.color(w.percent), width: HomeWidgetTokens.ringStrokeSmall)
                        .padding(CGFloat(i) * (HomeWidgetTokens.ringStrokeSmall + HomeWidgetTokens.hair))
                }
                Text(verbatim: "\(Int(fullest?.percent ?? 0))").font(HomeWidgetTokens.ringNumberSmall).monospacedDigit().homeInk(.primary)
            }
            .frame(width: HomeWidgetTokens.ringMedium, height: HomeWidgetTokens.ringMedium)
            Text(verbatim: account.name).font(HomeWidgetTokens.captionStrong).lineLimit(1).homeInk(.primary)
            HomeWindowList(account: account)
        }
        .opacity(account.stale ? HomeWidgetTokens.staleOpacity : 1)
    }
}

/// Today's tokens by the hour, midnight to now (Screen Time's chart): the current hour in the widget's blue.
private struct HomeHourChart: View {
    var hours: [Int]
    var now: Date

    var body: some View {
        let peak = max(1, hours.max() ?? 1)
        let current = Calendar.current.component(.hour, from: now)
        VStack(alignment: .leading, spacing: HomeWidgetTokens.tight) {
            HStack(alignment: .bottom, spacing: HomeWidgetTokens.hair + 1) {
                ForEach(0..<24, id: \.self) { h in
                    let n = h < hours.count ? hours[h] : 0
                    UnevenRoundedRectangle(topLeadingRadius: HomeWidgetTokens.chartBar, topTrailingRadius: HomeWidgetTokens.chartBar)
                        .frame(maxWidth: .infinity)
                        .frame(height: max(HomeWidgetTokens.hair, HomeWidgetTokens.chartHeight * CGFloat(n) / CGFloat(peak)))
                        .modifier(Tone(lit: h == current, future: h > current))
                }
            }
            .frame(height: HomeWidgetTokens.chartHeight, alignment: .bottom)
            .overlay(alignment: .bottom) { Rectangle().frame(height: HomeWidgetTokens.hairline).foregroundStyle(HomeWidgetTokens.rule) }
            HStack(spacing: HomeWidgetTokens.flush) {
                ForEach([0, 6, 12, 18], id: \.self) { h in
                    Text(Calendar.current.date(bySettingHour: h, minute: 0, second: 0, of: now) ?? now, format: .dateTime.hour())
                        .font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.tertiary).frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
    }

    private struct Tone: ViewModifier {
        var lit: Bool
        var future: Bool
        @Environment(\.widgetRenderingMode) private var mode
        func body(content: Content) -> some View {
            if lit { content.homeTone(HomeWidgetTokens.tokens) }
            else if mode == .fullColor { content.foregroundStyle(HomeWidgetTokens.tokens.opacity(future ? 0.15 : 0.45)) }
            else { content.homeInk(future ? .tertiary : .secondary) }
        }
    }
}

// MARK: - Computers

/// Each computer as a ring, as Batteries shows devices: its load, colored by what its agents do; gray dashes when it
/// is offline.
private struct HomeComputersWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    private var list: [HomeSnapshot.Machine] { c.machines }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HomeHeader(title: HomeKind.computers.title, color: HomeWidgetTokens.computerCalm, mood: c.mood(.computers), pictures: pictures)
            if list.isEmpty {
                Spacer(minLength: 0)
                HomeEmpty(text: "No computers yet [Home Widgets]", pictures: pictures, horizontal: family != .systemSmall)
                Spacer(minLength: 0)
            } else {
                switch family {
                case .systemSmall:
                    Spacer(minLength: HomeWidgetTokens.gap)
                    let cells = Array(list.prefix(4))
                    Grid(horizontalSpacing: HomeWidgetTokens.gap * 1.5, verticalSpacing: HomeWidgetTokens.gap) {
                        GridRow { cell(cells, 0); cell(cells, 1) }
                        GridRow { cell(cells, 2); cell(cells, 3) }
                    }
                    .frame(maxWidth: .infinity)
                case .systemMedium:
                    Spacer(minLength: 0)
                    HStack(alignment: .top, spacing: HomeWidgetTokens.flush) {
                        ForEach(list.prefix(4)) { HomeComputerRing(machine: $0, side: HomeWidgetTokens.ringMedium, named: true).frame(maxWidth: .infinity) }
                    }
                    Spacer(minLength: 0)
                default:
                    Spacer().frame(height: HomeWidgetTokens.row)
                    summary
                    Spacer().frame(height: HomeWidgetTokens.row * 1.5)
                    Spacer(minLength: 0)
                    Grid(alignment: .leading, horizontalSpacing: HomeWidgetTokens.halves, verticalSpacing: HomeWidgetTokens.row * 2.5) {
                        ForEach(Array(stride(from: 0, to: min(6, list.count), by: 2)), id: \.self) { i in
                            GridRow {
                                HomeComputerLine(machine: list[i])
                                if i + 1 < list.count { HomeComputerLine(machine: list[i + 1]) } else { Color.clear }
                            }
                        }
                    }
                    Spacer(minLength: 0)
                }
            }
        }
    }

    @ViewBuilder private func cell(_ cells: [HomeSnapshot.Machine], _ i: Int) -> some View {
        if i < cells.count { HomeComputerRing(machine: cells[i], side: HomeWidgetTokens.ringSmall, named: false) } else { Color.clear.frame(width: HomeWidgetTokens.ringSmall, height: HomeWidgetTokens.ringSmall) }
    }

    private var summary: some View {
        let online = list.filter(\.online).count
        let working = list.map(\.working).reduce(0, +)
        return HStack(alignment: .firstTextBaseline, spacing: HomeWidgetTokens.gap) {
            HomeHero(text: "\(online)", font: HomeWidgetTokens.heroMedium)
            Text("of \(list.count) online [Home Widgets]").font(HomeWidgetTokens.headline).homeInk(.primary)
            Spacer()
            if working > 0 { Text(verbatim: StateName.count(.working, working)).font(HomeWidgetTokens.captionStrong).homeTone(HomeWidgetTokens.working.dot) }
        }
    }
}

/// What a computer is doing, in a word and a color: needs you, working, idle or offline.
private func computerState(_ m: HomeSnapshot.Machine) -> (words: String, color: Color?) {
    if !m.online { return (interfaceText("Offline [Home Widgets]"), nil) } // perf-ok: drawn once per timeline entry, in the widget extension
    if m.needsYou > 0 { return (StateName.count(.needsYou, m.needsYou), HomeWidgetTokens.needsYou.dot) }
    if m.working > 0 { return (StateName.count(.working, m.working), HomeWidgetTokens.working.dot) }
    return (interfaceText("Idle [Home Widgets]"), nil) // perf-ok: drawn once per timeline entry, in the widget extension
}

private func computerSymbol(_ m: HomeSnapshot.Machine) -> String {
    switch m.icon {
    case "server": HomeWidgetTokens.serverSymbol
    case "desktop": HomeWidgetTokens.desktopSymbol
    default: HomeWidgetTokens.laptopSymbol
    }
}

/// A computer's ring: its mark inside, its load around, the state's color. Without its name under it (the small
/// widget), VoiceOver still reads which computer it is, what it does and its load.
private struct HomeComputerRing: View {
    var machine: HomeSnapshot.Machine
    var side: CGFloat
    var named: Bool

    var body: some View {
        let state = computerState(machine)
        let spoken = [machine.name, state.words, machine.online ? machine.load.map { interfaceText("Load \($0)% [Home Widgets]") } : nil] // perf-ok: drawn once per timeline entry, in the widget extension
            .compactMap { $0 }.joined(separator: ", ")
        VStack(spacing: HomeWidgetTokens.tight) {
            ZStack {
                HomeRing(fraction: Double(machine.load ?? 0) / 100, color: state.color ?? HomeWidgetTokens.computerCalm,
                         width: HomeWidgetTokens.ringStrokeSmall, dashed: !machine.online)
                HomeWidgetTokens.symbol(computerSymbol(machine)).font(named ? HomeWidgetTokens.headline : HomeWidgetTokens.body)
                    .homeInk(machine.online ? .primary : .tertiary)
            }
            .frame(width: side, height: side)
            if named {
                Text(verbatim: machine.name).font(HomeWidgetTokens.captionStrong).lineLimit(1).homeInk(machine.online ? .primary : .tertiary)
                Group {
                    if let color = state.color { Text(verbatim: state.words).homeTone(color) } else { Text(verbatim: state.words).homeInk(.secondary) }
                }
                .font(HomeWidgetTokens.caption).lineLimit(1)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: spoken))
    }
}

/// The large widget's computer: its ring, its name, what it does and its load.
private struct HomeComputerLine: View {
    var machine: HomeSnapshot.Machine
    var body: some View {
        let state = computerState(machine)
        HStack(spacing: HomeWidgetTokens.gap) {
            HomeComputerRing(machine: machine, side: HomeWidgetTokens.ringMedium, named: false)
            // The words take the rest of the row; beside a `Spacer` they would get only half of it and cut the name.
            VStack(alignment: .leading, spacing: 0) {
                Text(verbatim: machine.name).font(HomeWidgetTokens.headline).lineLimit(1).minimumScaleFactor(HomeWidgetTokens.nameMinScale)
                    .homeInk(machine.online ? .primary : .tertiary)
                Group {
                    if let color = state.color { Text(verbatim: state.words).homeTone(color) } else { Text(verbatim: state.words).homeInk(.secondary) }
                }
                .font(HomeWidgetTokens.caption).lineLimit(1)
                if machine.online, let load = machine.load {
                    Text("Load \(load)% [Home Widgets]").font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.tertiary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

// MARK: - Record

/// One big red button: a tap starts a meeting recording (the Record control's intent, which runs in the app and starts
/// the recording's Live Activity). While recording: the time running, and Stop.
private struct HomeRecordWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    private var recording: HomeSnapshot.Recording? { c.snapshot.recording }

    var body: some View {
        if family == .systemSmall {
            button {
                VStack(alignment: .leading, spacing: 0) {
                    HomeHeader(title: HomeKind.record.title, color: HomeWidgetTokens.recording, mood: recording == nil ? nil : .working, pictures: pictures)
                    Spacer(minLength: 0)
                    HomeRecordDisc(side: HomeWidgetTokens.recordHero * (recording == nil ? 0.82 : 0.6), recording: recording != nil)
                    Spacer(minLength: 0)
                    words(lines: 1)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        } else {
            button {
                HStack(spacing: HomeWidgetTokens.halves) {
                    HomeRecordDisc(side: HomeWidgetTokens.recordMedium, recording: recording != nil)
                    VStack(alignment: .leading, spacing: HomeWidgetTokens.tight) {
                        words(lines: 2)
                        if recording == nil {
                            Text("Audio stays on this iPhone; the transcript goes to Knowledge. [Home Widgets]")
                                .font(HomeWidgetTokens.caption).lineLimit(2).homeInk(.tertiary)
                        }
                    }
                    Spacer(minLength: 0)
                }
                .frame(maxHeight: .infinity)
            }
        }
    }

    @ViewBuilder private func button<Label: View>(@ViewBuilder label: () -> Label) -> some View {
        if let recording {
            Button(intent: StopRecordingIntent(recordingID: recording.id), label: label).buttonStyle(.plain)
        } else {
            Button(intent: StartMeetingRecordingIntent(), label: label).buttonStyle(.plain)
        }
    }

    @ViewBuilder private func words(lines: Int) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            if let recording {
                Text(timerInterval: Date(timeIntervalSince1970: recording.started)...Date.distantFuture, countsDown: false)
                    .font(HomeWidgetTokens.heroSmall).monospacedDigit().homeTone(HomeWidgetTokens.recording)
                Text(verbatim: recording.title).font(HomeWidgetTokens.headline).lineLimit(lines).homeInk(.primary)
                Text("Tap to stop [Home Widgets]").font(HomeWidgetTokens.caption).homeInk(.secondary)
            } else {
                Text("Record a Meeting [Home Widgets]").font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
                if let now = c.eventNow {
                    Text("Named “\(now.title)” [Home Widgets]").font(HomeWidgetTokens.caption).lineLimit(lines).homeInk(.secondary)
                } else if family != .systemSmall {
                    Text("Named after the event on your calendar now [Home Widgets]").font(HomeWidgetTokens.caption).lineLimit(lines).homeInk(.secondary)
                }
            }
        }
    }
}

/// The Record button: the system's recording red with the waveform; a stop square while recording.
private struct HomeRecordDisc: View {
    var side: CGFloat
    var recording: Bool
    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        let full = mode == .fullColor
        ZStack {
            Circle().fill(full ? HomeWidgetTokens.recordButton : .white)
            HomeWidgetTokens.symbol(recording ? HomeWidgetTokens.stopSymbol : HomeWidgetTokens.recordSymbol)
                .font(.system(size: side * 0.4, weight: .bold))
                .foregroundStyle(full ? Color.white : Color.black)
                .blendMode(full ? .normal : .destinationOut)
        }
        .compositingGroup()
        .frame(width: side, height: side)
        .homeAccent()
        .accessibilityLabel(Text(recording ? "Stop Recording [Home Widgets]" : "Record a Meeting [Home Widgets]"))
    }
}

// MARK: - Health

/// Today's steps, big, against the week's average; sleep and the resting heart beside them.
private struct HomeHealthWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    private var h: HomeSnapshot.Health { c.snapshot.health ?? .init() }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HomeHeader(title: HomeKind.health.title, color: HomeWidgetTokens.steps, mood: c.mood(.health), pictures: pictures)
            if h.steps == nil, h.sleepHours == nil {
                Spacer(minLength: 0)
                HomeEmpty(text: "Allow Health in Persona to see today [Home Widgets]", pictures: pictures, horizontal: family != .systemSmall)
                Spacer(minLength: 0)
            } else {
                switch family {
                case .systemSmall:
                    Spacer(minLength: 0)
                    steps(font: HomeWidgetTokens.heroMedium)
                case .systemMedium:
                    Spacer(minLength: 0)
                    HStack(alignment: .bottom, spacing: HomeWidgetTokens.halves) {
                        steps(font: HomeWidgetTokens.heroMedium).frame(maxWidth: .infinity, alignment: .leading)
                        VStack(alignment: .leading, spacing: HomeWidgetTokens.gap) { sleep; heart }
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                default:
                    Spacer().frame(height: HomeWidgetTokens.row)
                    steps(font: HomeWidgetTokens.hero, bar: false)
                    Spacer().frame(height: HomeWidgetTokens.row)
                    if !h.week.isEmpty { HomeWeekChart(days: h.week, average: h.stepsAverage, now: c.date) }
                    Spacer(minLength: HomeWidgetTokens.row)
                    HStack(alignment: .top) { sleep; Spacer(); heart }
                }
            }
        }
    }

    private func steps(font: Font, bar: Bool = true) -> some View {
        VStack(alignment: .leading, spacing: HomeWidgetTokens.tight) {
            HStack(alignment: .firstTextBaseline, spacing: HomeWidgetTokens.tight) {
                HomeHero(text: h.steps.map { $0.formatted() } ?? "–", font: font, color: HomeWidgetTokens.steps)
                Text("Steps [Home Widgets]").font(HomeWidgetTokens.headline).homeInk(.secondary)
            }
            if let steps = h.steps, let avg = h.stepsAverage, avg > 0 {
                if bar { HomeBar(fraction: Double(steps) / Double(max(steps, avg)), color: HomeWidgetTokens.steps) }
                Text("7-day average \(avg.formatted()) [Home Widgets]").font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.secondary)
            }
        }
    }

    @ViewBuilder private var sleep: some View {
        if let hours = h.sleepHours {
            measure(HomeWidgetTokens.sleepSymbol, Duration.seconds(hours * 3600).formatted(.units(allowed: [.hours, .minutes], width: .narrow)),
                    interfaceText("Asleep [Home Widgets]"), HomeWidgetTokens.sleep) // perf-ok: drawn once per timeline entry, in the widget extension
        }
    }

    @ViewBuilder private var heart: some View {
        if let bpm = h.restingHeartRate {
            measure(HomeWidgetTokens.healthSymbol, "\(bpm)", interfaceText("Resting bpm [Home Widgets]"), HomeWidgetTokens.heart) // perf-ok: drawn once per timeline entry, in the widget extension
        }
    }

    private func measure(_ symbol: String, _ value: String, _ words: String, _ color: Color) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: HomeWidgetTokens.tight) {
                HomeWidgetTokens.symbol(symbol).font(HomeWidgetTokens.captionStrong).homeTone(color)
                Text(verbatim: words).font(HomeWidgetTokens.captionStrong).lineLimit(1).homeInk(.secondary)
            }
            Text(verbatim: value).font(HomeWidgetTokens.heroSmall).lineLimit(1).minimumScaleFactor(HomeWidgetTokens.heroMinScale).homeInk(.primary)
        }
    }
}

/// Steps a day this week, today lit, the average a dashed line.
private struct HomeWeekChart: View {
    var days: [Int]
    var average: Int?
    var now: Date

    var body: some View {
        let peak = max(1, max(days.max() ?? 1, average ?? 0))
        VStack(spacing: HomeWidgetTokens.tight) {
            HStack(alignment: .bottom, spacing: HomeWidgetTokens.gap) {
                ForEach(Array(days.suffix(7).enumerated()), id: \.offset) { i, n in // a bar is its day's place in the week
                    UnevenRoundedRectangle(topLeadingRadius: HomeWidgetTokens.tight, topTrailingRadius: HomeWidgetTokens.tight)
                        .frame(maxWidth: .infinity)
                        .frame(height: max(HomeWidgetTokens.hair, HomeWidgetTokens.chartHeight * CGFloat(n) / CGFloat(peak)))
                        .modifier(Tone(today: i == min(6, days.count - 1)))
                }
            }
            .frame(height: HomeWidgetTokens.chartHeight, alignment: .bottom)
            .overlay(alignment: .bottom) {
                if let average {
                    Rectangle().stroke(style: StrokeStyle(lineWidth: 1, dash: [HomeWidgetTokens.tight, HomeWidgetTokens.tight - 1]))
                        .frame(height: HomeWidgetTokens.hairline).homeInk(.secondary)
                        .offset(y: -HomeWidgetTokens.chartHeight * CGFloat(average) / CGFloat(peak))
                }
            }
            HStack(spacing: HomeWidgetTokens.gap) {
                ForEach(0..<min(7, days.count), id: \.self) { i in
                    Text(Calendar.current.date(byAdding: .day, value: i - (min(7, days.count) - 1), to: now) ?? now, format: .dateTime.weekday(.narrow))
                        .font(HomeWidgetTokens.caption).homeInk(.tertiary).frame(maxWidth: .infinity)
                }
            }
        }
    }

    private struct Tone: ViewModifier {
        var today: Bool
        @Environment(\.widgetRenderingMode) private var mode
        func body(content: Content) -> some View {
            if today { content.homeTone(HomeWidgetTokens.steps) }
            else if mode == .fullColor { content.foregroundStyle(HomeWidgetTokens.steps.opacity(0.35)) }
            else { content.homeInk(.secondary) }
        }
    }
}

// MARK: - Inbox

/// How many messages need you, big; the newest beside it.
private struct HomeInboxWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    private var inbox: HomeSnapshot.Inbox { c.snapshot.inbox ?? .init() }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HomeHeader(title: HomeKind.inbox.title, color: HomeWidgetTokens.inbox.dot, mood: inbox.needsYou > 0 ? .needsYou : nil, pictures: pictures)
            if inbox.needsYou == 0, family == .systemSmall || inbox.latest.isEmpty {
                Spacer(minLength: 0)
                HomeEmpty(text: "No messages need you [Home Widgets]", pictures: pictures, horizontal: family != .systemSmall)
                Spacer(minLength: 0)
            } else {
                switch family {
                case .systemSmall:
                    Spacer(minLength: 0)
                    count
                case .systemMedium:
                    Spacer(minLength: 0)
                    HStack(alignment: .bottom, spacing: HomeWidgetTokens.halves) {
                        count.frame(width: HomeWidgetTokens.ringHero * 1.1, alignment: .leading)
                        if let m = inbox.latest.first { newest(m).frame(maxWidth: .infinity, alignment: .leading) }
                    }
                default:
                    Spacer().frame(height: HomeWidgetTokens.row)
                    HStack(alignment: .firstTextBaseline, spacing: HomeWidgetTokens.gap) {
                        HomeHero(text: "\(inbox.needsYou)", font: HomeWidgetTokens.heroMedium, color: HomeWidgetTokens.inbox.dot)
                        Text("Need You [Home Widgets]").font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
                        Spacer()
                    }
                    Spacer().frame(height: HomeWidgetTokens.row)
                    VStack(alignment: .leading, spacing: HomeWidgetTokens.row + HomeWidgetTokens.tight) {
                        ForEach(inbox.latest.prefix(5)) { HomeMessageRow(message: $0) }
                    }
                    Spacer(minLength: 0)
                }
            }
        }
    }

    private var count: some View {
        VStack(alignment: .leading, spacing: 0) {
            HomeHero(text: "\(inbox.needsYou)", color: HomeWidgetTokens.inbox.dot)
            Text("Need You [Home Widgets]").font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
            if family == .systemSmall, let m = inbox.latest.first {
                Text(verbatim: m.sender).font(HomeWidgetTokens.caption).lineLimit(1).homeInk(.secondary).padding(.top, HomeWidgetTokens.hair)
            }
        }
    }

    private func newest(_ m: HomeSnapshot.Message) -> some View {
        VStack(alignment: .leading, spacing: HomeWidgetTokens.hair) {
            HStack {
                Text(verbatim: m.sender).font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
                Spacer(minLength: HomeWidgetTokens.tight)
                Text(Date(timeIntervalSince1970: m.at), format: .dateTime.hour().minute()).font(HomeWidgetTokens.caption).homeInk(.tertiary)
            }
            Text(verbatim: m.summary).font(HomeWidgetTokens.body).lineLimit(2).homeInk(.secondary)
            if inbox.latest.count > 1 {
                Text("\(inbox.latest.count - 1) more [Home Widgets]").font(HomeWidgetTokens.caption).homeInk(.tertiary)
            }
        }
    }
}

/// A message in the large widget: who (a monogram), what it says, when.
private struct HomeMessageRow: View {
    var message: HomeSnapshot.Message
    @Environment(\.widgetRenderingMode) private var mode

    var body: some View {
        HStack(alignment: .top, spacing: HomeWidgetTokens.gap * 1.25) {
            Text(verbatim: String(message.sender.prefix(1)))
                .font(HomeWidgetTokens.headline)
                .frame(width: HomeWidgetTokens.faceEmpty * 0.72, height: HomeWidgetTokens.faceEmpty * 0.72)
                .background(Circle().fill(mode == .fullColor ? HomeWidgetTokens.track : HomeWidgetTokens.trackMono))
                .homeInk(.primary)
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: HomeWidgetTokens.tight) {
                    Text(verbatim: message.sender).font(HomeWidgetTokens.headline).lineLimit(1).homeInk(.primary)
                    if message.needsYou { HomeDot(color: HomeWidgetTokens.inbox.dot) }
                    Spacer(minLength: HomeWidgetTokens.tight)
                    Text(Date(timeIntervalSince1970: message.at), format: .dateTime.hour().minute()).font(HomeWidgetTokens.caption).homeInk(.tertiary)
                }
                Text(verbatim: message.summary).font(HomeWidgetTokens.body).lineLimit(1).homeInk(.secondary)
            }
        }
    }
}

// MARK: - Lock Screen

/// A widget on the Lock Screen: a number or a ring in the circle, two lines in the rectangle, one beside the clock.
struct HomeLockView: View {
    var kind: HomeKind
    var entry: HomeEntry
    var family: WidgetFamily

    private var c: HomeContext { entry.context }

    var body: some View {
        Group {
            if !c.snapshot.paired {
                unpaired
            } else {
                switch family {
                case .accessoryCircular: circular
                case .accessoryInline: Label(inline, systemImage: symbol)
                default: rectangular
                }
            }
        }
        // Old numbers step back, as on the Home Screen; the rectangle has no room for when they are from.
        .opacity(c.snapshot.paired && c.stale ? HomeWidgetTokens.staleOpacity : 1)
        .containerBackground(for: .widget) { Color.clear }
    }

    /// Before Persona is set up: the widget's mark, and where to go.
    @ViewBuilder private var unpaired: some View {
        switch family {
        case .accessoryCircular:
            ZStack {
                AccessoryWidgetBackground()
                HomeWidgetTokens.symbol(symbol).font(HomeWidgetTokens.headline)
            }
        case .accessoryInline:
            Label { Text("Open Persona to connect [Home Widgets]") } icon: { HomeWidgetTokens.symbol(symbol) }
        default:
            VStack(alignment: .leading, spacing: 0) {
                Text(kind.title).font(HomeWidgetTokens.lockTitle).lineLimit(1).homeAccent()
                Text("Open Persona to connect [Home Widgets]").font(HomeWidgetTokens.lockLine).lineLimit(2).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var symbol: String {
        switch kind {
        case .agents: HomeWidgetTokens.agentsSymbol
        case .today: HomeWidgetTokens.todaySymbol
        case .todos: HomeWidgetTokens.todoSymbol
        case .usage: HomeWidgetTokens.usageSymbol
        case .computers: HomeWidgetTokens.laptopSymbol
        case .record: HomeWidgetTokens.recordSymbol
        case .health: HomeWidgetTokens.stepsSymbol
        case .inbox: HomeWidgetTokens.inboxSymbol
        }
    }

    @ViewBuilder private var circular: some View {
        switch kind {
        case .agents:
            ZStack {
                AccessoryWidgetBackground()
                if c.agentCounts.needsYou > 0 {
                    VStack(spacing: 0) {
                        Text(verbatim: "\(c.agentCounts.needsYou)").font(HomeWidgetTokens.heroSmall).homeAccent()
                        HomeWidgetTokens.symbol(symbol).font(HomeWidgetTokens.caption)
                    }
                } else {
                    HomeFace(size: HomeWidgetTokens.faceLock * 1.25, mood: c.mood(.agents), pictures: entry.pictures, disc: false)
                }
            }
        case .usage:
            let w = c.accounts.first?.windows.max { $0.percent < $1.percent }
            Gauge(value: min(100, w?.percent ?? 0), in: 0...100) { HomeWidgetTokens.symbol(symbol) } currentValueLabel: { Text(verbatim: "\(Int(w?.percent ?? 0))") }
                .gaugeStyle(.accessoryCircularCapacity).homeAccent()
        case .health:
            let steps = c.snapshot.health?.steps ?? 0
            let avg = max(1, c.snapshot.health?.stepsAverage ?? 1)
            Gauge(value: min(1, Double(steps) / Double(avg))) { HomeWidgetTokens.symbol(symbol) } currentValueLabel: { Text(verbatim: homeSteps(steps)).minimumScaleFactor(HomeWidgetTokens.heroMinScale) }
                .gaugeStyle(.accessoryCircular).privacySensitive()
        case .record:
            Button(intent: StartMeetingRecordingIntent()) {
                ZStack {
                    AccessoryWidgetBackground()
                    HomeWidgetTokens.symbol(symbol).font(HomeWidgetTokens.recordMarkLock).homeAccent()
                }
            }
            .buttonStyle(.plain)
        case .today:
            ZStack {
                AccessoryWidgetBackground()
                VStack(spacing: 0) {
                    Text(c.date, format: .dateTime.weekday(.abbreviated)).font(HomeWidgetTokens.captionStrong).textCase(.uppercase)
                    Text(c.date, format: .dateTime.day()).font(HomeWidgetTokens.heroSmall).homeAccent()
                }
            }
        default:
            ZStack {
                AccessoryWidgetBackground()
                VStack(spacing: 0) {
                    HomeWidgetTokens.symbol(symbol).font(HomeWidgetTokens.caption)
                    Text(verbatim: circleNumber).font(HomeWidgetTokens.heroSmall).minimumScaleFactor(HomeWidgetTokens.heroMinScale).homeAccent()
                }
                .padding(HomeWidgetTokens.tight)
            }
            .privacySensitive(kind.isPrivate)
        }
    }

    private var circleNumber: String {
        switch kind {
        case .todos: "\(c.snapshot.todos?.count ?? 0)"
        case .computers: "\(c.machines.filter(\.online).count)/\(c.machines.count)"
        case .inbox: "\(c.snapshot.inbox?.needsYou ?? 0)"
        default: ""
        }
    }

    private var rectangular: some View {
        HStack(spacing: HomeWidgetTokens.gap) {
            if kind == .agents {
                HomeFace(size: HomeWidgetTokens.faceLock, mood: c.mood(.agents), pictures: entry.pictures)
            } else if kind == .record {
                Button(intent: StartMeetingRecordingIntent()) {
                    HomeWidgetTokens.symbol(symbol).font(HomeWidgetTokens.recordMarkLock).homeAccent()
                }
                .buttonStyle(.plain)
            }
            VStack(alignment: .leading, spacing: 0) {
                Text(verbatim: lockTitle).font(HomeWidgetTokens.lockTitle).lineLimit(1).homeAccent()
                ForEach(lockLines, id: \.self) { Text(verbatim: $0).font(HomeWidgetTokens.lockLine).lineLimit(1).foregroundStyle(.secondary) }
            }
            .privacySensitive(kind.isPrivate)
            Spacer(minLength: 0)
        }
    }

    private var lockTitle: String {
        switch kind {
        case .agents:
            let n = c.agentCounts
            if n.needsYou > 0 { return StateName.count(.needsYou, n.needsYou) }
            if n.working > 0 { return StateName.count(.working, n.working) }
            return interfaceText("Nothing needs you [Home Widgets]") // perf-ok: drawn once per timeline entry, in the widget extension
        case .today:
            guard let next = c.nextEvent else { return interfaceText("No more events today [Home Widgets]") } // perf-ok: drawn once per timeline entry, in the widget extension
            return Date(timeIntervalSince1970: next.start).formatted(.dateTime.hour().minute()) + " " + next.title
        case .todos:
            return interfaceText("\(c.snapshot.todos?.count ?? 0) to-dos today [Home Widgets]") // perf-ok: drawn once per timeline entry, in the widget extension
        case .usage:
            return c.accounts.first?.name ?? interfaceText("\(homeShort(c.snapshot.usage?.tokensToday ?? 0)) tokens today [Home Widgets]") // perf-ok: drawn once per timeline entry, in the widget extension
        case .computers:
            return interfaceText("\(c.machines.filter(\.online).count) of \(c.machines.count) online [Home Widgets]") // perf-ok: drawn once per timeline entry, in the widget extension
        case .record:
            return interfaceText("Record a Meeting [Home Widgets]") // perf-ok: drawn once per timeline entry, in the widget extension
        case .health:
            return interfaceText("\(c.snapshot.health?.steps ?? 0) steps [Home Widgets]") // perf-ok: drawn once per timeline entry, in the widget extension
        case .inbox:
            return interfaceText("\(c.snapshot.inbox?.needsYou ?? 0) messages need you [Home Widgets]") // perf-ok: drawn once per timeline entry, in the widget extension
        }
    }

    private var lockLines: [String] {
        switch kind {
        case .agents:
            return c.agentRows.prefix(1).map { [$0.ask ?? $0.detail, $0.title].compactMap { $0 }.joined(separator: " · ") }
        case .today:
            return c.events.filter { !$0.allDay }.dropFirst().prefix(1).map { Date(timeIntervalSince1970: $0.start).formatted(.dateTime.hour().minute()) + " " + $0.title }
        case .todos:
            return (c.snapshot.todos?.items ?? []).prefix(2).map(\.title)
        case .usage:
            guard let a = c.accounts.first else { return [] }
            return [HomeWindowName.list(a, short: true)]
        case .computers:
            return c.machines.prefix(2).map { $0.name + " · " + computerState($0).words }
        case .record:
            return c.eventNow.map { [interfaceText("Named “\($0.title)” [Home Widgets]")] } ?? [] // perf-ok: drawn once per timeline entry, in the widget extension
        case .health:
            guard let hrs = c.snapshot.health?.sleepHours else { return [] }
            return [Duration.seconds(hrs * 3600).formatted(.units(allowed: [.hours, .minutes], width: .narrow)) + " " + interfaceText("Asleep [Home Widgets]")] // perf-ok: drawn once per timeline entry, in the widget extension
        case .inbox:
            return c.snapshot.inbox?.latest.prefix(1).map { $0.sender + ": " + $0.summary } ?? []
        }
    }

    private var inline: String {
        switch kind {
        case .agents:
            let n = c.agentCounts
            let parts = [n.needsYou > 0 ? StateName.count(.needsYou, n.needsYou) : nil, n.working > 0 ? StateName.count(.working, n.working) : nil].compactMap { $0 }
            return parts.isEmpty ? interfaceText("Nothing needs you [Home Widgets]") : parts.joined(separator: " · ") // perf-ok: drawn once per timeline entry, in the widget extension
        default:
            return lockTitle
        }
    }
}
#endif
