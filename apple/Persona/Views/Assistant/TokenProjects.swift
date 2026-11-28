import Charts
import PersonaWire
import SwiftUI

// Token usage by project (#16530, docs/server/machines.md "Token usage by project"): on the Token Usage page, today's,
// the last 3 and the last 7 days' tokens by project across computers, development (git repositories) and
// non-development (folders, the home folder, temporary folders, the assistant) apart, largest first. A project opens
// its page: by day (by the hour for today), its folders and worktrees, by computer, Agent, model and launcher. What a
// project is, its share and its computers are the hub's (PersonaWire UsageProjects); this draws them. The web's
// TokenProjects.tsx is the same.

// MARK: - Words

enum TokenProjectText {
    /// How a project is named: its own name, or the words for what has none (never a folder that would show the
    /// account's name).
    nonisolated static func name(_ p: UsageProject) -> String {
        switch p.kind {
        case .home: String(localized: "Home Folder [Token Project]", comment: "Token usage by project: sessions started in the home folder itself")
        case .temp: String(localized: "Temporary Folders", comment: "Token usage by project: sessions started in a temporary folder")
        case .assistant: String(localized: "Assistant [Token Project]", comment: "Token usage by project: the Persona assistant's own conversations")
        case .unknown: String(localized: "Unknown Folder", comment: "Token usage by project: usage whose folder no computer could tell")
        default: p.name.isEmpty ? String(localized: "Unknown Folder", comment: "Token usage by project: usage whose folder no computer could tell") : p.name
        }
    }

    nonisolated static func icon(_ kind: UsageProjectKind) -> String {
        switch kind {
        case .repo: Theme.Icon.gitRepository
        case .local: Theme.Icon.gitNoUpstream
        case .folder, .unknown: Theme.Icon.folder
        case .home: Theme.Icon.home
        case .temp: Theme.Icon.time
        case .assistant: Theme.Icon.ai
        }
    }

    /// Development takes the accent, non-development its own color, the rest grey.
    static func color(_ group: UsageProjectGroup) -> Color {
        switch group {
        case .development: Theme.accent
        case .other: UsageColor.projectOther
        case .unknown: Color.secondary
        }
    }

    /// A share as a percentage: whole at 10% and up, one decimal below.
    nonisolated static func share(_ s: Double) -> String {
        if s > 0 && s < 0.001 { return "<" + (0.001).formatted(.percent.precision(.fractionLength(1))) }
        return s.formatted(.percent.precision(.fractionLength(0...(s >= 0.1 ? 0 : 1))))
    }

    /// A launcher as Settings › Models names it: its display name, else its own name; none (a session typed in a
    /// terminal) said so.
    nonisolated static func launcher(_ key: String, names: [String: String]) -> String {
        if key.isEmpty {
            return String(localized: "Opened in a Terminal [Launcher]", comment: "Token usage by project: sessions typed in a terminal, with no launcher")
        }
        return names[key].flatMap { $0.trimmingCharacters(in: .whitespaces).isEmpty ? nil : $0 } ?? key
    }

    nonisolated static func range(_ i: Int) -> String {
        switch i {
        case 0: String(localized: "Today")
        case 1: String(localized: "3 Days [Token Project]", comment: "Token usage by project: the range of the last 3 days")
        default: String(localized: "7 Days [Token Project]", comment: "Token usage by project: the range of the last 7 days")
        }
    }
}

/// Today, 3 Days, 7 Days.
struct ProjectRangePicker: View {
    @Binding var range: Int

    var body: some View {
        Picker(String(localized: "Range [Token Project]", comment: "Token usage by project: the range picker's label"), selection: $range) {
            ForEach(UsageProjects.rangeDays.indices, id: \.self) { i in Text(TokenProjectText.range(i)).tag(i) }
        }
        .pickerStyle(.segmented)
    }
}

// MARK: - The list

/// The page's By Project section: the range picker, the development / non-development split, each group's projects
/// (six, then Show N More), and what is not split.
struct UsageProjectsSection: View {
    let projects: UsageProjects
    @Binding var range: Int
    @State private var expanded: Set<UsageProjectGroup> = []
    private static let first = 6

    private var current: UsageProjectRange? { projects.ranges.indices.contains(range) ? projects.ranges[range] : projects.ranges.last }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text("By Project", comment: "Token usage page: the tokens of each project")
                .font(Theme.Font.sectionHeader).foregroundStyle(.secondary).padding(.horizontal, Theme.Space.xs)
                .accessibilityAddTraits(.isHeader)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                ProjectRangePicker(range: $range)
                if let r = current { list(r) }
            }
            .card()
        }
    }

    @ViewBuilder private func list(_ r: UsageProjectRange) -> some View {
        let all = Double(max(r.total.total, 1))
        let top = Double(max(r.projects.map(\.tokens.total).max() ?? 0, r.notSplit.total, 1))
        if r.projects.isEmpty && r.notSplit.total == 0 {
            VStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.folder).font(Theme.Font.title).foregroundStyle(.secondary)
                Text(r.days == 1 ? String(localized: "Nothing Used Today Yet", comment: "Token usage by project: today has no usage")
                     : String(localized: "Nothing Used in These Days", comment: "Token usage by project: the range has no usage"))
                    .font(Theme.Font.bodyStrong)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, Theme.Space.m)
        } else {
            let rest = r.total(of: .unknown) + r.notSplit.total
            MeterBar(parts: [
                MeterBar.Part(share: Double(r.total(of: .development)) / all, color: TokenProjectText.color(.development)),
                MeterBar.Part(share: Double(r.total(of: .other)) / all, color: TokenProjectText.color(.other)),
                MeterBar.Part(share: Double(rest) / all, color: TokenProjectText.color(.unknown)),
            ], thickness: Theme.Size.dotS)
            .accessibilityHidden(true)
            group(.development, r, all: all, top: top)
            group(.other, r, all: all, top: top)
            ForEach(r.projects(in: .unknown)) { p in row(p, top: top) }
            if r.notSplit.total > 0 {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.s) {
                        Text(String(localized: "Not Split (Before the Update)", comment: "Token usage page, By Agent: usage the hub kept before it counted each agent apart"))
                            .font(Theme.Font.body).foregroundStyle(.secondary).lineLimit(1)
                        Spacer(minLength: Theme.Space.s)
                        numbers(r.notSplit.total, share: Double(r.notSplit.total) / all)
                    }
                    MeterBar(fraction: Double(r.notSplit.total) / top, color: TokenProjectText.color(.unknown))
                }
                .accessibilityElement(children: .combine)
            }
        }
    }

    @ViewBuilder private func group(_ g: UsageProjectGroup, _ r: UsageProjectRange, all: Double, top: Double) -> some View {
        let list = r.projects(in: g)
        if !list.isEmpty {
            let sum = r.total(of: g)
            HStack(spacing: Theme.Space.s) {
                Circle().fill(TokenProjectText.color(g)).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                Text(g == .development ? String(localized: "Development [Token Project]", comment: "Token usage by project: git repositories")
                     : String(localized: "Non-development", comment: "Token usage by project: folders, the home folder and the assistant, not in a git repository"))
                Spacer(minLength: Theme.Space.s)
                Text("\(TokenFormat.short(sum)) · \(TokenProjectText.share(Double(sum) / all))").monospacedDigit()
            }
            .font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
            .padding(.top, Theme.Space.xs)
            .accessibilityElement(children: .combine)
            let shown = expanded.contains(g) ? list : Array(list.prefix(Self.first))
            ForEach(shown) { p in row(p, top: top) }
            if list.count > shown.count {
                Button(String(localized: "Show \(list.count - shown.count) More", comment: "Token usage by project: shows the group's other projects")) {
                    expanded.insert(g)
                }
                .font(Theme.Font.secondary).lineLimit(1)
                .padding(.leading, Theme.Space.l + Theme.Space.s)
            }
        }
    }

    private func row(_ p: UsageProject, top: Double) -> some View {
        NavigationLink {
            UsageProjectPage(project: p, range: $range)
        } label: {
            UsageProjectRow(project: p, top: top)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
    }

    private func numbers(_ tokens: Int64, share: Double) -> some View {
        UsageProjectNumbers(tokens: tokens, share: share)
    }
}

/// A row's tokens and share: never cut (the name gives way first).
struct UsageProjectNumbers: View {
    let tokens: Int64
    let share: Double

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            Text(TokenFormat.short(tokens))
            Text(TokenProjectText.share(share))
        }
        .font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
        .fixedSize()
        .layoutPriority(1)
    }
}

/// One project in the list: its mark, name and computers, its tokens and share, a bar against the largest. At large
/// text sizes (xxxLarge and up) the name takes a line of its own and the numbers the next, so a name is never cut to a
/// few letters. Its own view, so only it reads the text size (docs/apple/performance.md: no ViewThatFits measuring
/// in a list row).
struct UsageProjectRow: View {
    @Environment(\.dynamicTypeSize) private var size
    let project: UsageProject
    let top: Double

    var body: some View {
        let p = project
        let stacked = size >= .xxxLarge
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(TokenProjectText.icon(p.kind)).foregroundStyle(TokenProjectText.color(p.group)).font(Theme.Font.secondary)
                Text(TokenProjectText.name(p)).font(Theme.Font.body).foregroundStyle(.primary).lineLimit(stacked ? 2 : 1)
                if p.machines > 1 {
                    HStack(spacing: Theme.Space.xxs) {
                        ThemedIcon(Theme.Icon.laptop)
                        Text(p.machines, format: .number)
                    }
                    .font(Theme.Font.label).foregroundStyle(.secondary)
                    .fixedSize()
                    .accessibilityLabel(String(localized: "\(p.machines) computers"))
                }
                Spacer(minLength: Theme.Space.s)
                if !stacked {
                    UsageProjectNumbers(tokens: p.tokens.total, share: p.share)
                    disclosure
                }
            }
            if stacked {
                HStack(spacing: Theme.Space.s) {
                    UsageProjectNumbers(tokens: p.tokens.total, share: p.share)
                    Spacer(minLength: Theme.Space.s)
                    disclosure
                }
                .padding(.leading, Theme.Space.l + Theme.Space.s)
            }
            MeterBar(fraction: Double(p.tokens.total) / top, color: TokenProjectText.color(p.group))
                .padding(.leading, Theme.Space.l + Theme.Space.s)
        }
        .contentShape(.rect)
    }

    private var disclosure: some View {
        ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary)
    }
}

// MARK: - One project

/// A project's breakdown, read when its page opens (and when the tab comes back to the front).
@Observable final class UsageProjectModel {
    private(set) var detail: UsageProjectDetail?
    private(set) var failed = false
    /// The launchers' names as Settings › Models shows them (their display names), by launcher; empty before the list
    /// comes, or from a hub that will not give it: a launcher then shows its name.
    private(set) var launcherNames: [String: String] = [:]
    @ObservationIgnored private let key: String
    @ObservationIgnored private let api: () -> APIClient?

    init(key: String, api: @escaping () -> APIClient? = { AppModel.shared.api }) {
        self.key = key
        self.api = api
    }

    func load() async {
        guard let api = api() else { return }
        if launcherNames.isEmpty, let list = try? await api.allLaunchers() {
            launcherNames = Dictionary(list.map { ($0.name, $0.title) }, uniquingKeysWith: { a, _ in a })
        }
        do {
            let fresh = try await api.usageProjectDetail(key)
            if fresh != detail { detail = fresh }
            failed = false
        } catch {
            if error.isCancellation { return }
            if detail == nil { failed = true }
        }
    }
}

struct UsageProjectPage: View {
    let project: UsageProject
    @Binding var range: Int
    @State private var model: UsageProjectModel
    /// The chart's column under a finger now, and the last one touched (kept); nil: the largest one shows.
    @State private var touchedDay: String?
    @State private var pickedDay: String?
    @State private var touchedHour: Int?
    @State private var pickedHour: Int?

    init(project: UsageProject, range: Binding<Int>) {
        self.project = project
        _range = range
        _model = State(initialValue: UsageProjectModel(key: project.key))
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                ProjectRangePicker(range: $range)
                if let detail = model.detail, let b = detail.ranges.indices.contains(range) ? detail.ranges[range] : detail.ranges.last {
                    summary(detail.project, b)
                    section(range == 0 ? String(localized: "By Hour", comment: "Token usage by project: today's tokens by the hour")
                            : String(localized: "Each Day", comment: "Token usage page: the calendar's heading")) {
                        chart(detail)
                    }
                    if !b.places.isEmpty {
                        section(String(localized: "Folders and Worktrees", comment: "Token usage by project: each folder or git worktree the project's sessions started in")) {
                            places(b.places)
                        }
                    }
                    if !b.machines.isEmpty {
                        section(String(localized: "By Computer", comment: "Token usage by project: each computer's tokens")) {
                            parts(b.machines) { Text($0.name.isEmpty ? String(localized: "Removed Computer") : $0.name) }
                        }
                    }
                    if !b.agents.isEmpty {
                        section(String(localized: "By Agent", comment: "Token usage by project: each coding agent's tokens")) {
                            parts(b.agents) { part in
                                HStack(spacing: Theme.Space.s) {
                                    if let kind = AgentKind(usageAgent: part.key) {
                                        AgentMark(kind: kind).accessibilityHidden(true)
                                        Text(kind.name)
                                    } else {
                                        Text(String(localized: "Not Split (Before the Update)", comment: "Token usage page, By Agent: usage the hub kept before it counted each agent apart"))
                                            .foregroundStyle(.secondary)
                                    }
                                }
                            }
                        }
                    }
                    if !b.models.isEmpty {
                        section(String(localized: "By Model", comment: "Token usage by project: each model's tokens")) {
                            // The name the replies use ("Opus 5.5"), the model's id with a pointer over it.
                            parts(b.models) { Text(replyModelName($0.key)).help($0.key) }
                        }
                    }
                    if !b.launchers.isEmpty {
                        section(String(localized: "By Launcher", comment: "Token usage by project: each launcher's tokens")) {
                            parts(b.launchers) { part in
                                Text(TokenProjectText.launcher(part.key, names: model.launcherNames)).help(part.key)
                            }
                        }
                    }
                } else if model.failed {
                    Text("Can't reach the hub. This refreshes automatically once it's back.")
                        .font(Theme.Font.secondary).foregroundStyle(.secondary).frame(maxWidth: .infinity)
                } else {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .frame(maxWidth: ChatView.readableWidth)
            .frame(maxWidth: .infinity)
        }
        .themedGroupedPage()
        .navigationTitle(TokenProjectText.name(project))
        .navigationBarTitleDisplayMode(.inline)
        .tabActiveTask { await model.load() }
    }

    private func section<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text(title).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).padding(.horizontal, Theme.Space.xs)
                .accessibilityAddTraits(.isHeader)
            content().card()
        }
    }

    private func summary(_ p: UsageProject, _ b: UsageProjectBreakdown) -> some View {
        let all = Double(max(b.total.total, 1))
        let order: [UsageKind] = [.cacheRead, .cacheWrite, .output, .input]
        return VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(TokenProjectText.icon(p.kind)).foregroundStyle(TokenProjectText.color(p.group))
                if !p.host.isEmpty { Text(p.host) }
                if p.machines > 1 {
                    HStack(spacing: Theme.Space.xxs) {
                        ThemedIcon(Theme.Icon.laptop)
                        Text(String(localized: "\(p.machines) computers"))
                    }
                }
            }
            .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
            Text(TokenFormat.short(b.total.total)).font(Theme.Font.display.monospacedDigit()).lineLimit(1).minimumScaleFactor(0.7)
            HStack(alignment: .top, spacing: Theme.Space.m) {
                stat(String(localized: "Share [Token Project]", comment: "Token usage by project: the project's part of every project's tokens"), TokenProjectText.share(b.share))
                stat(String(localized: "Requests [Token Project]", comment: "Token usage by project: how many model replies"), b.total.requests.formatted())
                stat(String(localized: "Cache Hits", comment: "Token usage by project: cache reads over all input"),
                     b.cacheHit.formatted(.percent.precision(.fractionLength(0))), good: b.cacheHit >= 0.8)
            }
            MeterBar(parts: order.map { MeterBar.Part(share: Double(b.total.value($0)) / all, color: UsageColor.of($0)) },
                     thickness: Theme.Size.dotS)
            Grid(alignment: .leading, horizontalSpacing: Theme.Space.m, verticalSpacing: Theme.Space.xs) {
                ForEach([[order[0], order[1]], [order[2], order[3]]], id: \.self) { pair in
                    GridRow {
                        ForEach(pair) { k in
                            HStack(spacing: Theme.Space.xs) {
                                Circle().fill(UsageColor.of(k)).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                                Text(verbatim: "\(k.title) \((Double(b.total.value(k)) / all).formatted(.percent.precision(.significantDigits(1...2))))")
                            }
                        }
                    }
                }
            }
            .font(Theme.Font.label).foregroundStyle(.secondary)
        }
        .card()
    }

    private func stat(_ title: String, _ value: String, good: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(title).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            Text(value).font(Theme.Font.bodyStrong.monospacedDigit()).foregroundStyle(good ? Theme.positive : .primary).lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }

    /// The last days by day (the range's), or today by the hour, with a line over them: the largest one's day (or hour)
    /// and tokens at first, then the one touched (kept until another is).
    @ViewBuilder private func chart(_ d: UsageProjectDetail) -> some View {
        let days = UsageProjects.rangeDays.indices.contains(range) ? UsageProjects.rangeDays[range] : 7
        if days == 1 {
            let largest = d.hours.max { $0.tokens.total < $1.tokens.total }?.hour
            let shown = touchedHour ?? pickedHour ?? largest
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                chartLine(shown.flatMap { h in d.hours.first { $0.hour == h } }.map {
                    String(localized: "\(String(localized: "Today")) \(TokenCalendarText.hour($0.hour)) · \(TokenFormat.short($0.tokens.total)) tokens")
                })
                Chart(d.hours) { h in
                    BarMark(x: .value("Hour", h.hour), y: .value("Tokens", h.tokens.total))
                        .foregroundStyle(h.hour == shown ? Theme.accent : Theme.accent.opacity(Theme.Heat.unpicked))
                }
                .chartXSelection(value: $touchedHour)
                .chartXAxis { AxisMarks(values: [0, 6, 12, 18]) { v in AxisValueLabel { Text(TokenCalendarText.hour(v.as(Int.self) ?? 0)) } } }
                .chartYAxis(.hidden)
                .scaledFrame(height: Theme.Size.box120)
                .onChange(of: touchedHour) { _, h in if let h { pickedHour = h } }
            }
        } else {
            let list = Array(d.days.suffix(days))
            let largest = list.max { $0.tokens.total < $1.tokens.total }?.date
            let shown = touchedDay ?? pickedDay ?? largest
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                chartLine(shown.flatMap { s in list.first { $0.date == s } }.map {
                    String(localized: "\(TokenCalendarText.day($0.date)) · \(TokenFormat.short($0.tokens.total)) tokens")
                })
                Chart(list) { day in
                    BarMark(x: .value("Day", day.date), y: .value("Tokens", day.tokens.total))
                        .foregroundStyle(day.date == shown ? Theme.accent : Theme.accent.opacity(Theme.Heat.unpicked))
                }
                .chartXSelection(value: $touchedDay)
                .chartXAxis {
                    AxisMarks { v in
                        AxisValueLabel {
                            let date = v.as(String.self) ?? ""
                            Text(date == list.last?.date ? String(localized: "Today") : TokenCalendarText.shortDay(date))
                        }
                    }
                }
                .chartYAxis(.hidden)
                .scaledFrame(height: Theme.Size.box120)
                .onChange(of: touchedDay) { _, day in if let day { pickedDay = day } }
            }
        }
    }

    /// The line over the chart: one day's (or hour's) tokens; the room kept while there is none.
    private func chartLine(_ text: String?) -> some View {
        Text(text ?? " ")
            .font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
            .accessibilityHidden(text == nil)
    }

    private func places(_ list: [UsagePlace]) -> some View {
        let top = Double(max(list.map(\.tokens.total).max() ?? 0, 1))
        return VStack(alignment: .leading, spacing: Theme.Space.m) {
            ForEach(list) { pl in
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                        ThemedIcon(pl.worktree ? Theme.Icon.gitBranch : Theme.Icon.folder).foregroundStyle(Theme.accent)
                            .accessibilityLabel(pl.worktree ? String(localized: "Linked Worktree", comment: "Token usage by project: a git worktree that is not the main checkout") : "")
                        Text(pl.place).font(Theme.Font.secondary)
                        Spacer(minLength: Theme.Space.s)
                        Text(TokenFormat.short(pl.tokens.total)).font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary)
                    }
                    Text(pl.machineName.isEmpty ? String(localized: "Removed Computer") : pl.machineName)
                        .font(Theme.Font.label).foregroundStyle(.secondary)
                        .padding(.leading, Theme.Space.l + Theme.Space.s)
                    MeterBar(fraction: Double(pl.tokens.total) / top, color: Theme.accent)
                        .padding(.leading, Theme.Space.l + Theme.Space.s)
                }
                .accessibilityElement(children: .combine)
            }
        }
    }

    /// Rows of a name, a bar against the largest and the number, as the page's By Computer.
    private func parts<Name: View>(_ list: [UsagePart], @ViewBuilder name: @escaping (UsagePart) -> Name) -> some View {
        let top = Double(max(list.map(\.tokens.total).max() ?? 0, 1))
        return Grid(alignment: .leading, horizontalSpacing: Theme.Space.m, verticalSpacing: Theme.Space.s) {
            ForEach(list) { part in
                GridRow {
                    name(part).font(Theme.Font.body).lineLimit(1).layoutPriority(1)
                    MeterBar(fraction: Double(part.tokens.total) / top, color: Theme.accent)
                    Text(TokenFormat.short(part.tokens.total))
                        .font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                        .gridColumnAlignment(.trailing)
                }
                .accessibilityElement(children: .combine)
            }
        }
    }
}
