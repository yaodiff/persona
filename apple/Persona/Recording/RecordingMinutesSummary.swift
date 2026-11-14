#if os(iOS)
import SwiftUI

/// A recording page's Summary (#5026, docs/apple/recording.md「Minutes」, docs/design/DESIGN_SYSTEM.md "Minutes cards
/// (#5026)"): a header card (the minutes' own title when it differs from the recording's, when · where, who was there, and
/// what is happening to them: Updating, Regenerate, #4062), then one card per group with something in it: Key Points,
/// Decisions, Open Questions, Action Items, each headed by its icon, title and count. An item's time shows that line in the
/// Transcript, lit; its disclosure opens the transcript lines around it in place, so the details read without leaving the
/// Summary. An item that cites a meeting photo shows its thumbnail (`MinutesPhoto`, #3862 phase 3); an action item offers
/// Add to To-Dos (#4062). Sections of the page's List, a card each: the list keeps its lazy rows
/// (docs/apple/performance.md, P6), and the cards come worked out (`RecordingMinutesCards.summary`, P4). Minutes with an
/// overview (#6563) show the summary card instead (`RecordingMinutesOverview`): which one was settled when the cards were
/// worked out (`summary.overview`), never here.
struct RecordingMinutesSummary: View {
    var entry: RecordingEntry
    var model: RecordingMinutesModel
    /// The hub has this revision of its text (Regenerate needs the whole transcript there).
    var synced: Bool
    var summary: RecordingMinutesCards.Summary
    /// A time tapped: the page shows that line in its Transcript.
    var show: (Double) -> Void

    var body: some View {
        if let overview = summary.overview {
            RecordingMinutesOverview(entry: entry, model: model, synced: synced, summary: summary, overview: overview, show: show)
        } else {
            cards
        }
    }

    /// The four cards (#5026), as they were.
    @ViewBuilder private var cards: some View {
        // The recording's name: the minutes' own title shows only when it says something else.
        let title = summary.title.flatMap { $0 == entry.displayTitle ? nil : $0 }
        let updates = RecordingMinutesUpdates.shows(entry, model, synced: synced)
        if title != nil || summary.facts != nil || summary.attendees != nil || summary.cards.isEmpty || updates {
            Section {
                MinutesHeaderCard(title: title, facts: summary.facts, attendees: summary.attendees, empty: summary.cards.isEmpty) {
                    if updates { RecordingMinutesUpdates(entry: entry, model: model, synced: synced, boxed: true) }
                }
            }
        }
        ForEach(summary.cards) { card in
            Section {
                MinutesCard(card: card, recording: entry.id, offered: summary.offered, addable: summary.addable,
                            asking: model.asking, add: add, show: show)
            }
        }
    }

    /// Makes action items to-dos (`RecordingMinutesModel.add`): one, or all those left.
    private func add(_ ids: [String]) {
        Task { await model.add(ids, to: entry, synced: synced) }
    }
}

extension View {
    /// A row of the recording page's List drawn on the page's canvas (the switch and Copy / Share above the Summary): no
    /// row background, separator or side insets, so the control is the row, as wide as the page's other sections.
    func minutesCanvasRow() -> some View {
        listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets(top: Theme.Space.sm, leading: Theme.Space.flush, bottom: Theme.Space.sm, trailing: Theme.Space.flush))
            .listRowSeparator(.hidden)
    }

    /// One of the Summary's cards: the one row of a section of its own, padded as a card. The page's list draws it, with
    /// its other sections' background and corners in every style: a `.surface` drawn in a row had its shadow cut off
    /// square by the row's edges.
    func minutesCard() -> some View {
        frame(maxWidth: .infinity, alignment: .leading)
            .listRowInsets(EdgeInsets(top: Theme.Space.l, leading: Theme.Space.l, bottom: Theme.Space.l, trailing: Theme.Space.l))
    }
}

/// The Summary's first card: what the minutes say the meeting was, and what is happening to them (`below`). With nothing
/// in the groups it also says so, so the Summary never shows an empty page.
private struct MinutesHeaderCard<Below: View>: View {
    var title: String?
    var facts: String?
    var attendees: String?
    var empty: Bool
    @ViewBuilder var below: Below

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if let title {
                Text(title).font(Theme.Font.heading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let facts { MinutesFact(text: facts, symbol: Theme.Icon.calendarEvent).accessibilityIdentifier("minutes-when") }
            if let attendees { MinutesFact(text: attendees, symbol: Theme.Icon.people) }
            if empty {
                Text("Nothing to note: no points, decisions or action items came up.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            below
        }
        .minutesCard()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-card-header")
    }
}

/// A line of the minutes' facts with its icon (when · where, who was there), read as its words alone (the icon's own name,
/// "calendar day…", says nothing here).
struct MinutesFact: View {
    var text: String
    var symbol: String

    var body: some View {
        Label {
            Text(text).fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(symbol)
        }
        .font(Theme.Font.secondary).foregroundStyle(.secondary)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(text))
    }
}

/// A group's card: its header (read as one heading: the title and how many), then its items with a hairline between them;
/// the Action Items' card ends on Add All while more than one is left (#4062). Which items are open is this card's own
/// state, for as long as the page shows it. In the summary card's layout (`circles`, #6563) an added action starts with
/// its to-do's done circle once the app holds the to-do (`MinutesTodoCircle`, the only part that reads the store).
struct MinutesCard: View {
    var card: RecordingMinutesCards.Card
    /// The recording, whose photos the items cite.
    var recording: UUID
    /// The minutes' action items can become to-dos (the hub numbers them).
    var offered: Bool
    /// The action items left to add: Add All sends them.
    var addable: [String]
    /// A to-do is being made: no other one starts meanwhile.
    var asking: Bool
    var add: ([String]) -> Void
    var show: (Double) -> Void
    /// The summary card's layout: added actions show their to-do's circle (`MinutesTodoCircle`).
    var circles = false
    @State private var open: Set<String> = []
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        // The circles' column once an action is added (empty until the app reads its to-do), from the prepared items: the
        // four cards' items, and a card with only suggestions, keep their place.
        let column = circles && card.kind == .actions && card.items.contains { $0.todo != nil }
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            header
            ForEach(card.items) { item in
                if item.id != card.items.first?.id { Divider() }
                MinutesItemRow(item: item, recording: recording, open: open.contains(item.id), toggle: { toggle(item.id) }, show: show,
                               mark: column ? .todo(item.todo, action: item.text) : .none, byWidth: circles) {
                    if card.kind == .actions { todo(item) }
                }
            }
            if card.kind == .actions, offered, addable.count > 1 {
                Button { add(addable) } label: {
                    Label("Add All", systemImage: Theme.Icon.add).lineLimit(1).minimumScaleFactor(0.8)
                }
                .themedButton()
                .disabled(asking)
                .accessibilityIdentifier("minutes-add-all")
            }
        }
        .minutesCard()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-card-\(card.kind.rawValue)")
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(card.kind.symbol).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                .accessibilityHidden(true)
            Text(card.kind.title).font(Theme.Font.heading)
            Spacer(minLength: Theme.Space.s)
            Chip(text: card.items.count.formatted(), look: .neutral)
        }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }

    /// An action item as a to-do the user confirms (docs/assistant/meeting-minutes.md, #4062): Added once it is one, else
    /// Add to To-Dos when the hub names it. A hub that doesn't name its actions offers neither.
    @ViewBuilder private func todo(_ item: RecordingMinutesCards.Item) -> some View {
        if item.added {
            Chip(text: String(localized: "Added [Recording Minutes a4062d]"), look: .tint(Theme.positive), compact: true)
                .accessibilityIdentifier("minutes-added")
        } else if offered, let id = item.action {
            Button { add([id]) } label: {
                Label("Add to To-Dos", systemImage: Theme.Icon.add).lineLimit(1).minimumScaleFactor(0.8)
            }
            .themedButton()
            .disabled(asking)
            .accessibilityLabel(String(localized: "Add to To-Dos: \(item.text)"))
            .accessibilityIdentifier("minutes-add")
        }
    }

/// Opens or closes an item's lines: they fade in on the reveal curve and out on the conceal one, at once with Reduce
    /// Motion.
    private func toggle(_ id: String) {
        var next = open
        let opening = next.insert(id).inserted
        if !opening { next.remove(id) }
        withAnimation(reduceMotion ? nil : (opening ? Theme.Motion.reveal : Theme.Motion.conceal)) { open = next }
    }
}

/// What starts an item in the summary card's layout (#6563): a point's dot or a decision's mark in its topic's tint, or
/// an action's to-do circle (`MinutesTodoCircle`, named by the action's words; its column, empty for a suggestion). The
/// dot and the decision mark are decoration for VoiceOver: a decision's line says Decision. The four cards' items have
/// none.
enum MinutesMark {
    case none
    case point(Color)
    case decision(Color)
    case todo(String?, action: String)
}

/// An item: its words (and for an action, who and by when), the photo it cites, its time as a chip and the disclosure for
/// its lines, then `below` (an action item's Add to To-Dos) under all of it. At the accessibility reading sizes the time
/// and the disclosure go under the words, which then keep the card's whole width. In the summary card (#6563) a `mark`
/// starts it, its words have their bold places (`item.words`), a conclusion's time has the AI's fill (`chip`), on its
/// box, and its parts are laid out by the width that is there (`byWidth`, `MinutesItemLayout`).
struct MinutesItemRow<Below: View>: View {
    var item: RecordingMinutesCards.Item
    var recording: UUID
    var open: Bool
    var toggle: () -> Void
    var show: (Double) -> Void
    var mark: MinutesMark = .none
    /// The time chip's look: the accent's, else a conclusion's (`MinutesConclusions.chip`).
    var chip: Chip.Look? = nil
    /// The summary card's layout: the time and the disclosure beside the words while they have room, else on the line
    /// under them with who said it (`MinutesItemLayout`). The four cards' items keep their own layout.
    var byWidth = false
    @ViewBuilder var below: Below
    /// The size its words read at: the layout follows them, not the interface's cap.
    @Environment(\.readingTypeSize) private var reading

    var body: some View {
        if case .none = mark {
            row
        } else {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                lead
                row
            }
        }
    }

    private var row: some View {
        let layout = reading?.isAccessibilitySize == true
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: Theme.Space.xs))
            : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: Theme.Space.s))
        return VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if byWidth {
                MinutesItemLayout {
                    text.layoutValue(key: MinutesItemLayout.Part.self, value: .words)
                    if let byline = item.byline { bylineText(byline).layoutValue(key: MinutesItemLayout.Part.self, value: .byline) }
                    controls.layoutValue(key: MinutesItemLayout.Part.self, value: .controls)
                }
                if let photo = item.photo { MinutesPhoto(recording: recording, n: photo) }
            } else {
                layout {
                    words.frame(maxWidth: .infinity, alignment: .leading)
                    controls
                }
            }
            if open, !item.context.isEmpty {
                MinutesContext(lines: item.context)
                    .transition(.opacity)
            }
            below
        }
    }

    /// The mark in a column of its own, so the words of a card's items line up.
    @ViewBuilder private var lead: some View {
        switch mark {
        case .none:
            EmptyView()
        case .point(let tint):
            Text(verbatim: "•").font(Theme.Font.bodyStrong).foregroundStyle(tint)
                .scaledFrame(width: Theme.Size.markM)
                .accessibilityHidden(true)
        case .decision(let tint):
            ThemedIcon(Theme.Icon.decided).font(Theme.Font.body).imageScale(.small).foregroundStyle(tint)
                .scaledFrame(width: Theme.Size.markM)
                .accessibilityHidden(true)
        case .todo(let id, let action):
            MinutesTodoCircle(id: id, action: action)
        }
    }

    private var words: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            text
            if let byline = item.byline { bylineText(byline) }
            // The photo it cites, as the minutes' sections show it: a tap opens it full screen; Photo removed once gone.
            if let photo = item.photo { MinutesPhoto(recording: recording, n: photo) }
        }
    }

    /// Its words: with their bold places in the summary card.
    @ViewBuilder private var text: some View {
        if let bold = item.words {
            Text(bold).readingContent().font(Theme.Font.body).textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
        } else {
            Text(item.text).readingContent().font(Theme.Font.body).textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// Who and by when; a decision's word Decision, a step stronger.
    private func bylineText(_ byline: String) -> some View {
        Text(byline).font(item.decision ? Theme.Font.captionStrong : Theme.Font.caption).foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    @ViewBuilder private var controls: some View {
        if let at = item.at {
            HStack(spacing: Theme.Space.flush) {
                time(at)
                if !item.context.isEmpty { disclosure(at) }
            }
        }
    }

    /// The time it was said: a tap switches the page to its Transcript and lights that line (the page's jump).
    private func time(_ at: Double) -> some View {
        let clock = RecordingFormat.clock(at)
        return Button { show(at) } label: {
            Chip(text: clock, look: chip ?? .tint(Theme.accent), compact: true)
                .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Show at \(clock) in the transcript"))
        .accessibilityIdentifier("minutes-time")
    }

    /// Opens the transcript lines around it under the item. A chevron beside the time, so an item stays one line taller
    /// than its words; VoiceOver hears what it opens and whether it is open.
    private func disclosure(_ at: Double) -> some View {
        Button(action: toggle) {
            ThemedIcon(open ? Theme.Icon.collapse : Theme.Icon.expand)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
                .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Transcript around \(RecordingFormat.clock(at))"))
        .accessibilityValue(open ? Text("Expanded") : Text("Collapsed"))
        .accessibilityIdentifier("minutes-item-context")
    }
}

/// A summary-card item's words, who said it (or Decision) and its time and disclosure (#6563), arranged by the width that
/// is there (docs/design/DESIGN_SYSTEM.md 1.2), never by the text size: the time and the disclosure beside the words'
/// first line while the words keep `Theme.Recording.itemWordsColumn` times their line's height of width, who said it
/// under the words; else the words across the item and who said it, the time and the disclosure on the line under them,
/// as the contract's sketch draws a topic's bullets. The interface stops growing at xxxLarge and the words do not (#6813),
/// so it is the words' room that decides. Each part says which it is (`Part`); any but the words may be missing.
struct MinutesItemLayout: Layout {
    enum Role { case words, byline, controls }

    struct Part: LayoutValueKey {
        static let defaultValue = Role.words
    }

    var spacing: CGFloat = Theme.Space.s
    var gap: CGFloat = Theme.Space.xxs

    private struct Parts {
        var words: LayoutSubview?
        var byline: LayoutSubview?
        var controls: LayoutSubview?
    }

    private struct Frames {
        var words = CGRect.zero
        var byline: CGRect?
        var controls: CGRect?
        var height: CGFloat = 0
    }

    private func parts(_ subviews: Subviews) -> Parts {
        var p = Parts()
        for s in subviews {
            switch s[Part.self] {
            case .words: p.words = s
            case .byline: p.byline = s
            case .controls: p.controls = s
            }
        }
        return p
    }

    /// Whether the time goes beside the words at `width`: the words' one line, unwrapped, is as tall as their line.
    private func beside(_ width: CGFloat, _ p: Parts) -> Bool {
        guard let controls = p.controls, let words = p.words else { return true }
        let line = words.sizeThatFits(.unspecified).height
        return width - controls.sizeThatFits(.unspecified).width - spacing >= Theme.Recording.itemWordsColumn * line
    }

    private func frames(_ width: CGFloat, _ p: Parts) -> Frames {
        var f = Frames()
        let controls = p.controls?.dimensions(in: .unspecified)
        if beside(width, p) {
            let column = controls.map { max(0, width - $0.width - spacing) } ?? width
            let offer = ProposedViewSize(width: column, height: nil)
            let words = p.words?.dimensions(in: offer)
            // The time's first line on the words' first.
            let wb = words?[VerticalAlignment.firstTextBaseline] ?? 0, cb = controls?[VerticalAlignment.firstTextBaseline] ?? 0
            let top = controls == nil ? 0 : max(0, cb - wb)
            f.words = CGRect(x: 0, y: top, width: column, height: words?.height ?? 0)
            var bottom = f.words.maxY
            if let byline = p.byline {
                let h = byline.sizeThatFits(offer).height
                f.byline = CGRect(x: 0, y: bottom + gap, width: column, height: h)
                bottom += gap + h
            }
            if let controls {
                f.controls = CGRect(x: width - controls.width, y: max(0, wb - cb), width: controls.width, height: controls.height)
            }
            f.height = max(bottom, f.controls?.maxY ?? 0)
            return f
        }
        // The words across; under them who said it, then the time and the disclosure at the end of that line.
        let words = p.words?.sizeThatFits(ProposedViewSize(width: width, height: nil)).height ?? 0
        f.words = CGRect(x: 0, y: 0, width: width, height: words)
        let line = words + gap
        let room = max(0, width - (controls?.width ?? 0) - spacing)
        let byline = p.byline?.dimensions(in: ProposedViewSize(width: room, height: nil))
        let bb = byline?[VerticalAlignment.firstTextBaseline] ?? 0, cb = controls?[VerticalAlignment.firstTextBaseline] ?? 0
        let base = max(bb, cb)
        if let byline { f.byline = CGRect(x: 0, y: line + base - bb, width: room, height: byline.height) }
        if let controls { f.controls = CGRect(x: width - controls.width, y: line + base - cb, width: controls.width, height: controls.height) }
        f.height = max(f.words.maxY, f.byline?.maxY ?? 0, f.controls?.maxY ?? 0)
        return f
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let p = parts(subviews)
        let width = proposal.width.flatMap { $0.isFinite ? $0 : nil }
            ?? (p.words?.sizeThatFits(.unspecified).width ?? 0) + spacing + (p.controls?.sizeThatFits(.unspecified).width ?? 0)
        return CGSize(width: width, height: frames(width, p).height)
    }

    /// Its first line is the words' first line: a bullet's dot or decision mark sits on it.
    func explicitAlignment(of guide: VerticalAlignment, in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews,
                           cache: inout ()) -> CGFloat? {
        let p = parts(subviews)
        guard guide == .firstTextBaseline, let words = p.words else { return nil }
        let f = frames(bounds.width, p)
        return bounds.minY + f.words.minY + words.dimensions(in: ProposedViewSize(f.words.size))[VerticalAlignment.firstTextBaseline]
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let p = parts(subviews)
        let f = frames(bounds.width, p)
        func put(_ s: LayoutSubview?, _ r: CGRect?) {
            guard let s, let r else { return }
            s.place(at: CGPoint(x: bounds.minX + r.minX, y: bounds.minY + r.minY), proposal: ProposedViewSize(r.size))
        }
        put(p.words, f.words)
        put(p.byline, f.byline)
        put(p.controls, f.controls)
    }
}

/// An opened item's transcript lines on an inset well, as the meeting thread's opened lines: the line its time picks in the
/// primary color and the stronger weight (so it never rides on color alone), its neighbours in the secondary color.
struct MinutesContext: View {
    var lines: [RecordingMinutesCards.Line]
    /// The size its words read at: the layout follows them, not the interface's cap.
    @Environment(\.readingTypeSize) private var reading

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            ForEach(lines) { line in row(line) }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("minutes-item-lines")
    }

    private func row(_ line: RecordingMinutesCards.Line) -> some View {
        let stamp = Text(RecordingFormat.clock(line.start)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
        let words = Text(line.text).readingContent()
            .font(line.said ? Theme.Font.secondaryStrong : Theme.Font.secondary)
            .foregroundStyle(line.said ? HierarchicalShapeStyle.primary : HierarchicalShapeStyle.secondary)
            .textSelection(.enabled)
            .fixedSize(horizontal: false, vertical: true)
        return Group {
            if reading?.isAccessibilitySize == true {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) { stamp; words }
            } else {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) { stamp; words }
            }
        }
        .accessibilityElement(children: .combine)
    }
}
#endif
