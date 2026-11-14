#if os(iOS)
import SwiftUI

/// A recording page's Summary for minutes that carry an overview (#6563, docs/assistant/meeting-minutes.md "Summary card
/// (#6563)", docs/design/DESIGN_SYSTEM.md "Minutes cards (#5026)"): one summary card (the headline, the facts the page's
/// header does not show, the Core Conclusions box on the AI tint, what is happening to the minutes), then one card per
/// topic under its group's heading, each in its group's tint, then Action Items and Open Questions as the four cards
/// draw them, then a note that AI wrote it. Points and decisions are not shown again in cards of their own. Sections of
/// the page's List, a card each, as the four cards (`RecordingMinutesSummary`); all of it comes worked out
/// (`RecordingMinutesCards.overview`, docs/apple/performance.md P4).
struct RecordingMinutesOverview: View {
    var entry: RecordingEntry
    var model: RecordingMinutesModel
    /// The hub has this revision of its text (Regenerate needs the whole transcript there).
    var synced: Bool
    var summary: RecordingMinutesCards.Summary
    var overview: RecordingMinutesCards.Overview
    /// A time tapped: the page shows that line in its Transcript.
    var show: (Double) -> Void

    var body: some View {
        // The headline, else the minutes' own title where it says something the page's header does not.
        let heading = overview.headline ?? summary.title.flatMap { $0 == entry.displayTitle ? nil : $0 }
        let updates = RecordingMinutesUpdates.shows(entry, model, synced: synced)
        if heading != nil || overview.facts != nil || overview.attendees != nil || !overview.conclusions.isEmpty || updates {
            Section {
                MinutesSummaryCard(heading: heading, overview: overview, recording: entry.id, show: show) {
                    if updates { RecordingMinutesUpdates(entry: entry, model: model, synced: synced, boxed: true) }
                }
            }
        }
        ForEach(overview.groups) { group in
            ForEach(group.topics) { topic in
                if let title = group.title, topic.id == group.topics.first?.id {
                    Section {
                        MinutesTopicCard(topic: topic, recording: entry.id, show: show).id(topic.says)
                    } header: {
                        MinutesGroupHeading(title: title, hue: group.hue).accessibilityIdentifier("minutes-\(group.id)")
                    }
                } else {
                    Section {
                        MinutesTopicCard(topic: topic, recording: entry.id, show: show).id(topic.says)
                    }
                }
            }
        }
        // What happens next, then what is still open: the page ends on them, as the Markdown does.
        ForEach([RecordingMinutesCards.Kind.actions, .open], id: \.self) { kind in
            if let card = summary.cards.first(where: { $0.kind == kind }) {
                Section {
                    MinutesCard(card: card, recording: entry.id, offered: summary.offered, addable: summary.addable,
                                asking: model.asking, add: add, show: show, circles: true)
                }
            }
        }
        Section {
            EmptyView()
        } footer: {
            MinutesAINote()
        }
    }

    /// Makes action items to-dos (`RecordingMinutesModel.add`): one, or all those left.
    private func add(_ ids: [String]) {
        Task { await model.add(ids, to: entry, synced: synced) }
    }
}

/// The summary card: the headline (a heading), when · where and who was there where the page's header does not say them,
/// the Core Conclusions box, then `below` (Updating, Regenerate).
private struct MinutesSummaryCard<Below: View>: View {
    var heading: String?
    var overview: RecordingMinutesCards.Overview
    var recording: UUID
    var show: (Double) -> Void
    @ViewBuilder var below: Below

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            if heading != nil || overview.facts != nil || overview.attendees != nil {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    if let heading {
                        // The card's heading: interface text, which stops growing at xxxLarge, as the four cards' title.
                        Text(heading).font(Theme.Font.title)
                            .fixedSize(horizontal: false, vertical: true)
                            .accessibilityAddTraits(.isHeader)
                            .accessibilityIdentifier("minutes-headline")
                    }
                    if let facts = overview.facts {
                        // The calendar with a time, the pin for a place alone, as the page's header (`RecordingMeetingFacts`).
                        MinutesFact(text: facts, symbol: overview.factsSymbol).accessibilityIdentifier("minutes-when")
                    }
                    if let attendees = overview.attendees { MinutesFact(text: attendees, symbol: Theme.Icon.people) }
                }
            }
            if !overview.conclusions.isEmpty {
                MinutesConclusions(items: overview.conclusions, recording: recording, show: show)
            }
            below
        }
        .minutesCard()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-card-summary")
    }
}

/// Core Conclusions: where the meeting landed, in the AI's words, so on the AI tint with the lightbulb. An inset well in
/// the summary card, never a `.surface` (its shadow is cut off in a list row). A conclusion shows its words with their
/// bold places, the photo it cites and its time; no transcript opens under it, since it sums up more than one line.
/// Its time's chip is the AI's fill with primary words: the AI color as words is under 4.5:1 on the well (`StyleSpecTests`).
private struct MinutesConclusions: View {
    /// A conclusion's time: primary words on the AI's soft fill, a step deeper than the well.
    static var chip: Chip.Look { .tint(Theme.aiTint, text: .primary) }

    var items: [RecordingMinutesCards.Item]
    var recording: UUID
    var show: (Double) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            // The mark in the AI's color; the words in the primary ink, which reads on the tint in every style.
            Label {
                Text("Core Conclusions")
            } icon: {
                ThemedIcon(Theme.Icon.conclusions).foregroundStyle(Theme.ai)
            }
            .font(Theme.Font.secondaryStrong)
            .foregroundStyle(.primary)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Text("Core Conclusions"))
            .accessibilityAddTraits(.isHeader)
            ForEach(items) { item in
                MinutesItemRow(item: item, recording: recording, open: false, toggle: {}, show: show, chip: Self.chip, byWidth: true) {
                    EmptyView()
                }
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("minutes-conclusion")
            }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.aiTint, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-conclusions")
    }
}

/// A group's heading over its first topic card: a dot in the group's tint (decoration) and its name, which wraps.
private struct MinutesGroupHeading: View {
    var title: String
    var hue: Int

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.dot).font(Theme.Font.heading).imageScale(.small)
                .foregroundStyle(MinutesTint.of(hue))
                .accessibilityHidden(true)
            // `Color.primary`: a section header's `.primary` is its own secondary gray.
            Text(title).font(Theme.Font.heading).foregroundStyle(Color.primary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .textCase(nil)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}

extension RecordingMinutesCards.Topic {
    /// What the topic says. Its card is identified by it as well as by its place: rewritten minutes that say something
    /// else there start the card folded with no bullet open; a transcript refresh or a to-do change keeps its state.
    var says: [String] { [title] + bullets.map(\.text) }
}

/// A topic's card: its icon on a tile in its group's tint and its title (the heading; the tile is decoration), then its
/// bullets, a point with a dot and a decision with the decision mark, each an item as the four cards' (its time, its
/// transcript lines in place). The first `topic.shown` show; Show N More opens the rest in place and Show Less folds them
/// again. The tile sits beside the title, and above it at the accessibility sizes (`tileAbove`, the contract's largest
/// text sizes), where the title keeps the card's whole width. Which bullets are open, and the fold, are the card's own
/// state.
struct MinutesTopicCard: View {
    var topic: RecordingMinutesCards.Topic
    var recording: UUID
    var show: (Double) -> Void
    @State private var open: Set<String> = []
    @State private var unfolded = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dynamicTypeSize) private var size
    /// The size the reader chose: the interface's (`size`) stops at xxxLarge (#6813), so it never says accessibility.
    @Environment(\.readingTypeSize) private var reading

    /// Whether a topic's tile goes above its title rather than beside it: when the reader chose an accessibility size
    /// (docs/assistant/meeting-minutes.md "Summary card (#6563)": the largest text sizes), where the bullets' words have
    /// grown past the interface and the title takes the card's whole width.
    static func tileAbove(_ chosen: DynamicTypeSize) -> Bool { chosen.isAccessibilitySize }

    var body: some View {
        let tint = MinutesTint.of(topic.hue)
        let bullets = unfolded ? topic.bullets : Array(topic.bullets.prefix(topic.shown))
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            header(tint)
            ForEach(bullets) { bullet in
                MinutesItemRow(item: bullet, recording: recording, open: open.contains(bullet.id), toggle: { toggle(bullet.id) },
                               show: show, mark: bullet.decision ? .decision(tint) : .point(tint), byWidth: true) { EmptyView() }
                    .transition(.opacity)
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier(bullet.decision ? "minutes-decision" : "minutes-point")
            }
            if topic.folded > 0 { fold }
        }
        .minutesCard()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-\(topic.id)")
    }

    private func header(_ tint: Color) -> some View {
        let layout = Self.tileAbove(reading ?? size)
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: Theme.Space.s))
            : AnyLayout(HStackLayout(alignment: .center, spacing: Theme.Space.m))
        return layout {
            ThemedIcon(topic.symbol).font(Theme.Font.secondaryStrong).foregroundStyle(tint)
                .scaledFrame(width: Theme.Size.wellS, height: Theme.Size.wellS)
                .background(Theme.softTint(tint), in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .accessibilityHidden(true)
            // The heading VoiceOver reads (the tile beside it is hidden).
            Text(topic.title).font(Theme.Font.heading)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("minutes-topic-title")
        }
    }

    /// Show N More, or Show Less once open: one line, in the accent, a finger's height. VoiceOver hears which topic.
    private var fold: some View {
        let more = String(localized: "Show \(topic.folded) More")
        return Button {
            withAnimation(reduceMotion ? nil : (unfolded ? Theme.Motion.conceal : Theme.Motion.reveal)) { unfolded.toggle() }
        } label: {
            Text(unfolded ? String(localized: "Show Less") : more).lineLimit(1).minimumScaleFactor(0.8)
        }
        .font(Theme.Font.captionStrong)
        .buttonStyle(.plain)
        .foregroundStyle(Theme.accent)
        .tapTargetHeight()
        .accessibilityLabel(unfolded ? String(localized: "Show less of \(topic.title)") : String(localized: "Show \(topic.folded) more in \(topic.title)"))
        .accessibilityIdentifier(unfolded ? "minutes-topic-less" : "minutes-topic-more")
    }

    /// Opens or closes a bullet's lines, as the four cards' items do.
    private func toggle(_ id: String) {
        var next = open
        let opening = next.insert(id).inserted
        if !opening { next.remove(id) }
        withAnimation(reduceMotion ? nil : (opening ? Theme.Motion.reveal : Theme.Motion.conceal)) { open = next }
    }
}

/// A group's topic tint by its place (`Theme.Hue.minutesGroups`).
enum MinutesTint {
    static func of(_ hue: Int) -> Color {
        let tints = Theme.Hue.minutesGroups
        return tints[((hue % tints.count) + tints.count) % tints.count]
    }
}

/// An added action's to-do circle, once the app holds that to-do (`TodoStore`): its done circle, which ticks it as the
/// To-dos list does, the same state included (a failed start still waiting on the owner reads open, #3713). A to-do
/// not read yet keeps the column empty; a suggested action never shows a box. Only this view reads the store, so a
/// change to any to-do redraws the circles, not the card (docs/apple/performance.md P5).
struct MinutesTodoCircle: View {
    var id: String?
    /// The action's words: VoiceOver names the switch by them.
    var action: String

    /// Whether the circle shows `todo` open: the To-dos list's checkbox (`TodoTriageLogic.checksAsOpen`), with the triage
    /// the list gives it worked out only for a closed failed start, the one case it changes.
    static func open(_ todo: TodoItem, now: Date) -> Bool {
        TodoTriageLogic.checksAsOpen(todo, among: { TodoStore.shared.todos },
                                     lookup: { TodosLogic.makeSessionLookup(AppModel.shared.sessionsNow) },
                                     now: now, calendar: TodoCalendar.userCalendar)
    }

    var body: some View {
        if let id, let todo = TodoStore.shared.byID[id] {
            // A minute's tick moves a failed start across the list's 48 h line while the page is open, as the To-dos
            // list's clock does; a tap works out the state again, so it always sends what the list would.
            TimelineView(.everyMinute) { context in
                circle(todo, open: Self.open(todo, now: context.date))
            }
        } else {
            Color.clear.scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.hairline)
                .accessibilityHidden(true)
        }
    }

    private func circle(_ todo: TodoItem, open: Bool) -> some View {
        Button { TodoStore.shared.toggleDone(todo, done: Self.open(todo, now: .now)) } label: {
            // The column is a finger wide; the hit area grows above and below, the row's layout does not.
            ThemedIcon(open ? Theme.Icon.unchecked : Theme.Icon.successFilled)
                .font(Theme.Font.heading)
                .foregroundStyle(open ? AnyShapeStyle(.secondary) : AnyShapeStyle(Theme.positive))
                .scaledFrame(width: Theme.Size.tapTarget)
                .tapTargetOutset()
        }
        .buttonStyle(.plain)
        .accessibilityRepresentation {
            Toggle(String(localized: "Done: \(action) [Minutes To-Do]"), isOn: Binding(get: { !open }, set: { _ in
                TodoStore.shared.toggleDone(todo, done: Self.open(todo, now: .now))
            }))
        }
        .accessibilityIdentifier(open ? "minutes-todo-open" : "minutes-todo-done")
    }
}

/// The note under the summary card's layout: AI wrote these minutes, check what matters (the same words as the hub's
/// Markdown ends on).
private struct MinutesAINote: View {
    var body: some View {
        Label {
            Text("AI-generated from the recording. Check anything important.")
                .fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(Theme.Icon.ai).foregroundStyle(Theme.ai)
        }
        .font(Theme.Font.caption)
        .foregroundStyle(.secondary)
        .textCase(nil)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("minutes-ai-note")
    }
}
#endif
