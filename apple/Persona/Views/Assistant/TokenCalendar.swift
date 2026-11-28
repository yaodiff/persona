import PersonaWire
import SwiftUI

// The token calendar on the assistant's home and its page (#9506, docs/server/machines.md "Token usage history"): every
// computer's tokens a day, the same count as today's tokens on the Machines page, in place of the row of computer
// chips that was there. Read when the page shows and when the app comes back to the front; nothing keeps running.

// MARK: - Layout

/// Where each day of the calendar goes: a column a week, Monday on top, this week last, the days to come left out;
/// today outlined; the steps from the days shown (`HeatLevels`); a month's name over the first week that starts in it,
/// and the names of Monday, Wednesday and Friday at the left. Pure, so the tests check it.
nonisolated struct TokenCalendarLayout: Equatable, Sendable {
    struct Month: Equatable, Sendable {
        var column: Int
        var name: String
    }

    /// The steps, `[column][row]` (`HeatGrid.levels`); -1 for a day to come.
    var levels: [[Int]] = []
    /// The day in each square (2006-01-02), nil for a day to come.
    var dates: [[String?]] = []
    var today: HeatGrid.Cell?
    var months: [Month] = []
    /// A label for each row, "" for the rows without one.
    var weekdays: [String] = Array(repeating: "", count: 7)

    /// The days' dates are the hub's days in its user zone: worked out in a calendar of fixed days (UTC), never the
    /// phone's zone, so a day is the hub's day wherever the phone is.
    static func calendar(locale: Locale) -> Calendar {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = .gmt
        cal.locale = locale
        return cal
    }

    static func date(_ s: String, _ cal: Calendar) -> Date? {
        let p = s.split(separator: "-").compactMap { Int($0) }
        guard p.count == 3 else { return nil }
        return cal.date(from: DateComponents(year: p[0], month: p[1], day: p[2]))
    }

    static func key(_ d: Date, _ cal: Calendar) -> String {
        let c = cal.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    /// Monday is row 0.
    static func row(_ d: Date, _ cal: Calendar) -> Int { (cal.component(.weekday, from: d) + 5) % 7 }

    /// How many weeks fit `width` (the squares' room), within `Theme.Heat`'s bounds.
    static func weeks(width: CGFloat) -> Int {
        let n = Int(((width + Theme.Heat.gap) / (Theme.Heat.cell + Theme.Heat.gap)).rounded(.down))
        return min(max(n, Theme.Heat.minWeeks), Theme.Heat.maxWeeks)
    }

    init() {}

    init(days: [UsageDay], weeks: Int, locale: Locale = .autoupdatingCurrent) {
        let cal = Self.calendar(locale: locale)
        guard let last = days.last, let today = Self.date(last.date, cal), weeks > 0 else { return }
        let totals = Dictionary(days.map { ($0.date, $0.tokens.total) }, uniquingKeysWith: +)
        let monday = cal.date(byAdding: .day, value: -Self.row(today, cal), to: today) ?? today
        let first = cal.date(byAdding: .day, value: -7 * (weeks - 1), to: monday) ?? monday
        var dates: [[String?]] = []
        var shown: [Int64] = []
        for c in 0..<weeks {
            var column: [String?] = []
            for r in 0..<7 {
                guard let d = cal.date(byAdding: .day, value: 7 * c + r, to: first), d <= today else {
                    column.append(nil)
                    continue
                }
                let k = Self.key(d, cal)
                column.append(k)
                shown.append(totals[k] ?? 0)
            }
            dates.append(column)
        }
        var steps = HeatLevels.levels(shown)[...]
        levels = dates.map { column in column.map { $0 == nil ? -1 : steps.popFirst() ?? 0 } }
        self.dates = dates
        self.today = HeatGrid.Cell(column: weeks - 1, row: Self.row(today, cal))
        // A month over the first week that starts in it; never two names closer than three weeks (the first column's,
        // of a month that began before it, gives way).
        let name = Date.FormatStyle(locale: locale, calendar: cal, timeZone: .gmt).month(.abbreviated)
        var lastMonth: Int?
        for c in 0..<weeks {
            guard let d = cal.date(byAdding: .day, value: 7 * c, to: first) else { continue }
            let starts = cal.component(.day, from: d) <= 7
            guard starts, lastMonth.map({ cal.component(.month, from: d) != $0 }) ?? true else { continue }
            if let prev = months.last, c - prev.column < 3 { months.removeLast() }
            months.append(Month(column: c, name: d.formatted(name)))
            lastMonth = cal.component(.month, from: d)
        }
        let symbols = cal.veryShortStandaloneWeekdaySymbols // Sunday first
        if symbols.count == 7 {
            weekdays = (0..<7).map { r in r % 2 == 0 && r < 6 ? symbols[(r + 1) % 7] : "" }
        }
    }

    /// The day in a square, if there is one.
    func date(at cell: HeatGrid.Cell) -> String? {
        guard dates.indices.contains(cell.column), dates[cell.column].indices.contains(cell.row) else { return nil }
        return dates[cell.column][cell.row]
    }

    /// The square of a day shown.
    func cell(of date: String) -> HeatGrid.Cell? {
        for (c, column) in dates.enumerated() {
            if let r = column.firstIndex(of: date) { return HeatGrid.Cell(column: c, row: r) }
        }
        return nil
    }
}

// MARK: - Model

/// The history's reads (MVVM, docs/apple/ios.md "Logic-layer choice"): the days for the calendar, the hours and the
/// computers for the page. A read that fails keeps what was there; with nothing there the card does not show (an older
/// hub has no such routes).
@Observable final class TokenUsageModel {
    /// What an iPhone's calendar reads: 20 weeks, more than fit; an iPad's, the year.
    static let phoneDays = 20 * 7
    static let yearDays = Theme.Heat.maxWeeks * 7

    private(set) var days: UsageDays?
    private(set) var hours: UsageHours?
    /// Today's, the last 3 and the last 7 days' tokens by project (#16530); nil from a hub without them (no section).
    private(set) var projects: UsageProjects?
    /// The page's read failed and it has nothing to show.
    private(set) var failed = false
    @ObservationIgnored private let api: () -> APIClient?

    init(api: @escaping () -> APIClient? = { AppModel.shared.api }) {
        self.api = api
    }

    /// Something to draw: the history is there and has usage in it (a hub with no computers yet shows no card).
    var hasUsage: Bool { days?.days.contains { $0.tokens.total > 0 } == true }

    func loadDays(_ n: Int) async {
        guard let api = api() else { return }
        do {
            let fresh = try await api.usageDays(n)
            if fresh != days { days = fresh }
            failed = false
        } catch {
            if error.isCancellation { return }
            if days == nil { failed = true }
        }
    }

    func loadHours() async {
        guard let api = api() else { return }
        do {
            let fresh = try await api.usageHours(7)
            if fresh != hours { hours = fresh }
        } catch {
            if error.isCancellation { return }
            if hours == nil { failed = true }
        }
    }

    /// The projects; a failed read keeps what was there, and an older hub's 404 leaves none (the section is not shown).
    func loadProjects() async {
        guard let api = api() else { return }
        if let fresh = try? await api.usageProjects(), !fresh.ranges.isEmpty, fresh != projects { projects = fresh }
    }
}

// MARK: - Words

/// The calendar's sentences and numbers, in the language's own short numbers (`TokenFormat.short`).
nonisolated enum TokenCalendarText {
    static func day(_ date: String, locale: Locale = .autoupdatingCurrent) -> String {
        let cal = TokenCalendarLayout.calendar(locale: locale)
        guard let d = TokenCalendarLayout.date(date, cal) else { return date }
        return d.formatted(Date.FormatStyle(locale: locale, calendar: cal, timeZone: .gmt).month(.abbreviated).day().weekday(.abbreviated))
    }

    /// A day said briefly, for a chart's axis: its month and day in numbers ("11/23" in English; in Chinese the same
    /// numbers, each followed by its month or day sign).
    static func shortDay(_ date: String, locale: Locale = .autoupdatingCurrent) -> String {
        let cal = TokenCalendarLayout.calendar(locale: locale)
        guard let d = TokenCalendarLayout.date(date, cal) else { return date }
        return d.formatted(Date.FormatStyle(locale: locale, calendar: cal, timeZone: .gmt).month(.defaultDigits).day())
    }

    /// An hour on the clock in the language's way (12 AM in English; the hour and its sign in Chinese and Japanese).
    static func hour(_ h: Int, locale: Locale = .autoupdatingCurrent) -> String {
        let cal = TokenCalendarLayout.calendar(locale: locale)
        let d = cal.date(from: DateComponents(year: 2000, month: 1, day: 1, hour: h)) ?? .now
        return d.formatted(Date.FormatStyle(locale: locale, calendar: cal, timeZone: .gmt).hour())
    }

    /// "Input 3.51M · Output 24.6M · Cache Write 181M · Cache Read 5.64B", in the language's short numbers.
    static func kinds(_ t: UsageTokens) -> String {
        [UsageKind.input, .output, .cacheWrite, .cacheRead]
            .map { "\($0.title) \(TokenFormat.short(t.value($0)))" }
            .joined(separator: " · ")
    }
}

// MARK: - Calendar

/// The calendar's grid with its labels: as many weeks as fit the width it is given, the picked day outlined.
struct UsageCalendar: View {
    var days: [UsageDay]
    @Binding var picked: String?
    @State private var room: CGFloat = 0
    @State private var layout = TokenCalendarLayout()

    private var gridHeight: CGFloat { 7 * (Theme.Heat.cell + Theme.Heat.gap) - Theme.Heat.gap }

    var body: some View {
        HStack(alignment: .bottom, spacing: Theme.Space.xs) {
            VStack(alignment: .trailing, spacing: Theme.Heat.gap) {
                ForEach(0..<7, id: \.self) { r in
                    Text(layout.weekdays[r])
                        .frame(height: Theme.Heat.cell)
                }
            }
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
            .fixedSize()
            .accessibilityHidden(true)
            VStack(alignment: .trailing, spacing: Theme.Space.xxs) {
                months
                Color.clear
                    .frame(maxWidth: .infinity)
                    .frame(height: gridHeight)
                    .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { room = $0 }
                    .overlay(alignment: .trailing) {
                        HeatGrid(levels: layout.levels, rows: 7, cell: CGSize(width: Theme.Heat.cell, height: Theme.Heat.cell),
                                 gap: Theme.Heat.gap, outlined: layout.today, picked: picked.flatMap(layout.cell(of:))) { cell in
                            picked = cell.flatMap(layout.date(at:))
                        }
                    }
            }
        }
        .onChange(of: LayoutKey(days: days, weeks: TokenCalendarLayout.weeks(width: room)), initial: true) { _, key in
            layout = TokenCalendarLayout(days: key.days, weeks: key.weeks)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(String(localized: "Tokens a day", comment: "The token calendar: one square a day"))
        .accessibilityValue(accessibilityValue)
        .accessibilityAdjustableAction { direction in
            let shown = layout.dates.flatMap { $0 }.compactMap { $0 }
            guard !shown.isEmpty else { return }
            let i = picked.flatMap { shown.firstIndex(of: $0) } ?? shown.count - 1
            switch direction {
            case .increment: picked = shown[min(i + 1, shown.count - 1)]
            case .decrement: picked = shown[max(i - 1, 0)]
            default: break
            }
        }
    }

    private struct LayoutKey: Equatable {
        var days: [UsageDay]
        var weeks: Int
    }

    /// The months over the weeks they start in.
    private var months: some View {
        let step = Theme.Heat.cell + Theme.Heat.gap
        let width = CGFloat(layout.levels.count) * step - Theme.Heat.gap
        return ZStack(alignment: .topLeading) {
            ForEach(layout.months, id: \.column) { m in
                Text(m.name)
                    .lineLimit(1)
                    .fixedSize()
                    .offset(x: CGFloat(m.column) * step)
            }
        }
        .font(Theme.Font.label)
        .foregroundStyle(.secondary)
        .frame(width: max(width, 0), alignment: .topLeading)
        .accessibilityHidden(true)
    }

    private var accessibilityValue: String {
        let date = picked ?? days.last?.date
        guard let date, let day = days.first(where: { $0.date == date }) else { return "" }
        return String(localized: "\(TokenCalendarText.day(date)): \(TokenFormat.short(day.tokens.total)) tokens",
                      comment: "The token calendar's day read aloud: its date and tokens")
    }
}

// MARK: - Card

/// The assistant home's card (in place of the computer chips): today's and the last 30 days' tokens over the
/// calendar; a tapped day shows its own; the line opens the page.
struct TokenCalendarCard: View {
    var days: UsageDays
    var open: () -> Void
    @State private var picked: String?

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Button(action: open) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                    TokenDayLine(days: days, picked: picked)
                    Spacer(minLength: 0)
                    ThemedIcon(Theme.Icon.disclosure)
                        .font(Theme.Font.labelStrong)
                        .foregroundStyle(.tertiary)
                }
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityHint(String(localized: "Opens token usage by day, hour and computer"))
            .accessibilityIdentifier("token-calendar-line")
            UsageCalendar(days: days.days, picked: $picked)
                .accessibilityIdentifier("token-calendar-grid")
        }
        .card(padding: 14)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("token-calendar")
        #if DEBUG
        // Screenshots (-showTokenPick busiest): the busiest of the last two weeks picked, as after a tap.
        .onAppear {
            if UserDefaults.standard.string(forKey: "showTokenPick") == "busiest" {
                picked = days.days.suffix(14).max { $0.tokens.total < $1.tokens.total }?.date
            }
        }
        #endif
    }
}

/// The line over a calendar: today's and the last 30 days' tokens, or the picked day's with its four kinds under it.
struct TokenDayLine: View {
    var days: UsageDays
    var picked: String?

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            if let picked, let day = days.days.first(where: { $0.date == picked }) {
                Text("\(Text(TokenCalendarText.day(picked)).font(Theme.Font.secondaryStrong)) · \(Text(TokenFormat.short(day.tokens.total)).font(Theme.Font.secondaryStrong)) tokens",
                     comment: "The token calendar's picked day: its date and its tokens, as in Nov 16 · 5.85B tokens")
                Text(TokenCalendarText.kinds(day.tokens))
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
            } else {
                Text("Today \(Text(TokenFormat.short(days.last(1).total)).font(Theme.Font.secondaryStrong).foregroundStyle(Color.primary)) tokens · Last 30 days \(Text(TokenFormat.short(days.last(30).total)).font(Theme.Font.secondaryStrong).foregroundStyle(Color.primary))",
                     comment: "The token calendar's line: today's tokens and the last 30 days', as in Today 3.2B tokens · Last 30 days 56.7B")
            }
        }
        .font(Theme.Font.secondary)
        .foregroundStyle(.secondary)
        .lineLimit(2)
        .monospacedDigit()
    }
}

// MARK: - Page

/// The card's page: today, the last 7 and 30 days; by project (#16530, TokenProjects.swift; none from an older hub);
/// the calendar with its key; the last 7 days by the hour; by computer, by agent (an older hub sends no agents: no
/// section) and by kind.
struct TokenUsagePage: View {
    @State private var usage = TokenUsageModel()
    /// By project's range: the last 7 days, as the page's other sections; a project's page shares it.
    @State private var projectRange = 2
    @State private var pickedDay: String?
    @State private var pickedHour: HeatGrid.Cell?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                if let days = usage.days {
                    totals(days)
                    if let projects = usage.projects {
                        UsageProjectsSection(projects: projects, range: $projectRange)
                    }
                    section(String(localized: "Each Day", comment: "Token usage page: the calendar's heading")) {
                        VStack(alignment: .leading, spacing: Theme.Space.m) {
                            // Today's and the 30 days' totals are the page's first row: here only a picked day's own.
                            if pickedDay != nil { TokenDayLine(days: days, picked: pickedDay) }
                            UsageCalendar(days: days.days, picked: $pickedDay)
                            HeatLegend(low: String(localized: "Less [Heat Legend]", comment: "A heat grid's faintest squares"),
                                       high: String(localized: "More [Heat Legend]", comment: "A heat grid's fullest squares"))
                                .frame(maxWidth: .infinity, alignment: .trailing)
                        }
                    }
                }
                if let hours = usage.hours {
                    section(String(localized: "Last 7 Days by the Hour")) { UsageHourGrid(hours: hours, picked: $pickedHour) }
                    section(String(localized: "By Computer · Last 7 Days")) { machines(hours) }
                    if hours.agents.contains(where: { $0.tokens.total > 0 }) {
                        section(String(localized: "By Agent · Last 7 Days", comment: "Token usage page: each coding agent's tokens over the last week")) {
                            agents(hours)
                        }
                    }
                    section(String(localized: "By Kind · Last 7 Days")) { kinds(hours.total) }
                    Text("Input, output, cache write and cache read added up: the same count as Today on the Machines page.")
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, Theme.Space.xs)
                }
                if usage.days == nil, usage.hours == nil {
                    if usage.failed {
                        Text("Can't reach the hub. This refreshes automatically once it's back.")
                            .font(Theme.Font.secondary)
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity)
                    } else {
                        ProgressView().frame(maxWidth: .infinity)
                    }
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .frame(maxWidth: ChatView.readableWidth)
            .frame(maxWidth: .infinity)
        }
        .themedGroupedPage()
        .navigationTitle(String(localized: "Token Usage"))
        .navigationBarTitleDisplayMode(.inline)
        .tabActiveTask {
            async let d: Void = usage.loadDays(TokenUsageModel.yearDays)
            async let h: Void = usage.loadHours()
            async let p: Void = usage.loadProjects()
            _ = await (d, h, p)
        }
    }

    private func totals(_ days: UsageDays) -> some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            stat(String(localized: "Today"), days.last(1).total)
            stat(String(localized: "Last 7 Days"), days.last(7).total)
            stat(String(localized: "Last 30 Days"), days.last(30).total)
        }
        .card(padding: 14)
    }

    private func stat(_ title: String, _ n: Int64) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(title).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            Text(TokenFormat.short(n)).font(Theme.Font.title.monospacedDigit()).lineLimit(1).minimumScaleFactor(0.7)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }

    private func section<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text(title).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).padding(.horizontal, Theme.Space.xs)
                .accessibilityAddTraits(.isHeader)
            content().card(padding: 14)
        }
    }

    private func machines(_ hours: UsageHours) -> some View {
        let top = Double(hours.machines.first?.tokens.total ?? 0)
        // A grid, so every bar starts and ends where the others do whatever the name's and the number's widths.
        return Grid(alignment: .leading, horizontalSpacing: Theme.Space.m, verticalSpacing: Theme.Space.s) {
            ForEach(hours.machines.filter { $0.tokens.total > 0 }) { m in
                GridRow {
                    Text(m.name.isEmpty ? String(localized: "Removed Computer") : m.name)
                        .font(Theme.Font.body).lineLimit(1)
                    MeterBar(fraction: top > 0 ? Double(m.tokens.total) / top : 0, color: Theme.accent)
                    Text(TokenFormat.short(m.tokens.total))
                        .font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                        .gridColumnAlignment(.trailing)
                }
            }
        }
    }

    /// Each agent's tokens, as the computers': its mark and name, a bar against the largest, the number. Usage the hub
    /// stored before it kept agents apart has no agent: a row of its own, named so, without a mark.
    private func agents(_ hours: UsageHours) -> some View {
        let top = Double(hours.agents.first?.tokens.total ?? 0)
        return Grid(alignment: .leading, horizontalSpacing: Theme.Space.m, verticalSpacing: Theme.Space.s) {
            ForEach(hours.agents.filter { $0.tokens.total > 0 }) { a in
                let kind = AgentKind(usageAgent: a.agent)
                GridRow {
                    HStack(spacing: Theme.Space.s) {
                        if let kind {
                            AgentMark(kind: kind).accessibilityHidden(true) // the name says it
                            Text(kind.name).font(Theme.Font.body).lineLimit(1)
                        } else {
                            Text(String(localized: "Not Split (Before the Update)",
                                        comment: "Token usage page, By Agent: usage the hub kept before it counted each agent apart"))
                                .font(Theme.Font.body).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                    .layoutPriority(1) // at large text sizes the bar gives way before the name does
                    MeterBar(fraction: top > 0 ? Double(a.tokens.total) / top : 0, color: Theme.accent)
                    Text(TokenFormat.short(a.tokens.total))
                        .font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                        .gridColumnAlignment(.trailing)
                }
                .accessibilityElement(children: .combine)
            }
        }
    }

    private func kinds(_ total: UsageTokens) -> some View {
        let all = Double(max(total.total, 1))
        let order: [UsageKind] = [.cacheRead, .cacheWrite, .output, .input]
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            MeterBar(parts: order.map { MeterBar.Part(share: Double(total.value($0)) / all, color: UsageColor.of($0)) },
                     thickness: Theme.Size.dotS)
            ForEach(order) { k in
                HStack(spacing: Theme.Space.s) {
                    Circle().fill(UsageColor.of(k)).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                    Text(k.title).font(Theme.Font.body)
                    Spacer(minLength: Theme.Space.s)
                    Text(TokenFormat.short(total.value(k))).font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary)
                    Text((Double(total.value(k)) / all).formatted(.percent.precision(.significantDigits(1...3))))
                        .font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary)
                }
                .lineLimit(1)
                .accessibilityElement(children: .combine)
            }
        }
    }
}

// MARK: - The last week by the hour

/// Where each hour of the last days goes: a row a day (oldest on top, today last), a column an hour on the clock; the
/// hour that comes twice on the day the clocks go back adds into its square, the one a day skips stays empty.
nonisolated struct UsageHourLayout: Equatable, Sendable {
    var dates: [String] = []
    /// Each row's name (the day, the last "Today") and the hours named under the grid.
    var rowNames: [String] = []
    var hourNames: [Int: String] = [:]
    /// `[hour][row]`, as `HeatGrid` takes columns; -1 for an hour still to come.
    var levels: [[Int]] = []
    var tokens: [[UsageTokens]] = []
    var max: Int64 = 0

    init() {}

    init(hours: [UsageHour], today: String = String(localized: "Today"),
         locale: Locale = .autoupdatingCurrent) {
        dates = hours.reduce(into: [String]()) { if $0.last != $1.date { $0.append($1.date) } }
        rowNames = dates.map { $0 == dates.last ? today : TokenCalendarText.day($0, locale: locale) }
        hourNames = Dictionary(uniqueKeysWithValues: [0, 6, 12, 18].map { ($0, TokenCalendarText.hour($0, locale: locale)) })
        let rows = dates.count
        var sums = Array(repeating: Array(repeating: UsageTokens(), count: rows), count: 24)
        var seen = Array(repeating: Array(repeating: false, count: rows), count: 24)
        let row = Dictionary(dates.enumerated().map { ($1, $0) }, uniquingKeysWith: { a, _ in a })
        for h in hours where (0..<24).contains(h.hour) {
            guard let r = row[h.date] else { continue }
            sums[h.hour][r] = sums[h.hour][r] + h.tokens
            seen[h.hour][r] = true
        }
        // Today's hours after the one under way are still to come.
        let last = hours.last.map { $0.hour } ?? 23
        var flat: [Int64] = []
        for c in 0..<24 { for r in 0..<rows where seen[c][r] || !(r == rows - 1 && c > last) { flat.append(sums[c][r].total) } }
        var steps = HeatLevels.levels(flat)[...]
        levels = (0..<24).map { c in (0..<rows).map { r in (r == rows - 1 && c > last && !seen[c][r]) ? -1 : steps.popFirst() ?? 0 } }
        tokens = sums
        max = flat.max() ?? 0
    }
}

struct UsageHourGrid: View {
    var hours: UsageHours
    @Binding var picked: HeatGrid.Cell?
    @State private var room: CGFloat = 0
    @State private var layout = UsageHourLayout()

    var body: some View {
        let rows = layout.dates.count
        let width = max((room + Theme.Heat.hourGap) / 24 - Theme.Heat.hourGap, 1)
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            pickedLine
            HStack(alignment: .top, spacing: Theme.Space.s) {
                VStack(alignment: .trailing, spacing: Theme.Heat.hourGap) {
                    ForEach(layout.rowNames.indices, id: \.self) { r in
                        Text(layout.rowNames[r])
                            .lineLimit(1)
                            .frame(height: Theme.Heat.hourHeight)
                    }
                }
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .fixedSize()
                .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Color.clear
                        .frame(maxWidth: .infinity)
                        .frame(height: CGFloat(rows) * (Theme.Heat.hourHeight + Theme.Heat.hourGap) - Theme.Heat.hourGap)
                        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { room = $0 }
                        .overlay(alignment: .leading) {
                            HeatGrid(levels: layout.levels, rows: rows, cell: CGSize(width: width, height: Theme.Heat.hourHeight),
                                     gap: Theme.Heat.hourGap, picked: picked) { picked = $0 }
                        }
                    ZStack(alignment: .topLeading) {
                        ForEach(layout.hourNames.keys.sorted(), id: \.self) { h in
                            Text(layout.hourNames[h] ?? "").fixedSize().offset(x: CGFloat(h) * (width + Theme.Heat.hourGap))
                        }
                    }
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityHidden(true)
                }
            }
            HeatLegend(low: "0", high: TokenFormat.short(layout.max))
                .frame(maxWidth: .infinity, alignment: .trailing)
            Text("Each square is an hour in your time zone, shaded by where it ranks among the week's hours.")
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
        }
        .onChange(of: hours.hours, initial: true) { _, list in layout = UsageHourLayout(hours: list) }
    }

    @ViewBuilder private var pickedLine: some View {
        if let picked, layout.tokens.indices.contains(picked.column), layout.tokens[picked.column].indices.contains(picked.row) {
            let t = layout.tokens[picked.column][picked.row]
            Text("\(layout.rowNames[picked.row]) \(TokenCalendarText.hour(picked.column)) · \(TokenFormat.short(t.total)) tokens",
                 comment: "The hour grid's picked hour: its day, hour and tokens, as in Mon, Nov 16 2 PM · 520M tokens")
                .font(Theme.Font.secondary.monospacedDigit())
                .lineLimit(1)
        }
    }
}
