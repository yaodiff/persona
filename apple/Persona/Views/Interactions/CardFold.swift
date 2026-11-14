import MarkdownUI
import SwiftUI

// MARK: - A pending card's long words, folded (#6605)

/// A pending card's form as one scroll area over its answers (#6605), as a question card keeps Submit since #4839: what
/// is asked (the tool, the command, the plan) scrolls in the room the panel's cap (`cardFormCap`) leaves over the answers,
/// and the answers (Allow / Deny, Yes / No, a plan's buttons) sit under it, in view without scrolling. Words taller than
/// that room first give up lines of their long part (`CardBodyStack`: a command or a plan folded by `CardFold`); only
/// past its fewest lines do they scroll. A long command once scrolled in its own area inside the card, and the card in
/// the cards' area too: two scrolls, the answers last.
struct CardForm<Words: View, Answers: View>: View {
    @Environment(\.cardFormCap) private var formCap
    @ViewBuilder var words: () -> Words
    @ViewBuilder var answers: () -> Answers
    @State private var wordsHeight: CGFloat = 0
    @State private var answersHeight: CGFloat = 0

    var body: some View {
        let cap = wordsCap
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            ScrollView {
                CardBodyStack(cap: cap, spacing: Theme.Space.m) { words() }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { wordsHeight = $0 }
            }
            .scrollBounceBehavior(.basedOnSize)
            .frame(height: cap.isFinite ? min(wordsHeight == 0 ? cap : wordsHeight, cap) : nil)
            VStack(alignment: .leading, spacing: Theme.Space.m) { answers() }
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { answersHeight = $0 }
        }
    }

    /// What the words may take: the form's cap less the answers and the gap over them; at least a tap target. Unbounded
    /// where the card has no cap (before the page is measured).
    private var wordsCap: CGFloat {
        guard formCap > 0, formCap.isFinite else { return .infinity }
        return max(formCap - answersHeight - Theme.Space.m, Theme.Size.tapTarget)
    }
}

/// The child of `CardBodyStack` that gives up room when the words are taller than their cap: a `CardFold`. Not on the
/// main actor: the layout reads it off its subviews.
nonisolated struct CardBodyGives: LayoutValueKey {
    static let defaultValue = false
}

extension View {
    /// This part of a card's words gives up lines when the card is short (`CardBodyStack`).
    func givesCardRoom() -> some View { layoutValue(key: CardBodyGives.self, value: true) }
}

/// A card's words, one under another as a VStack lays them out. Taller than `cap`, the part marked `givesCardRoom()` is
/// offered what the others leave of it (a fold then shows fewer lines, never fewer than its floor), so the card keeps
/// its height and its answers their place. Not a VStack: in the scroll area it is offered no height to share out.
struct CardBodyStack: Layout {
    var cap: CGFloat
    var spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? subviews.map { $0.sizeThatFits(.unspecified).width }.max() ?? 0
        let heights = heights(width, subviews)
        return CGSize(width: width, height: heights.reduce(0, +) + gaps(subviews))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for (subview, height) in zip(subviews, heights(bounds.width, subviews)) {
            subview.place(at: CGPoint(x: bounds.minX, y: y), anchor: .topLeading,
                          proposal: ProposedViewSize(width: bounds.width, height: height))
            y += height + spacing
        }
    }

    private func gaps(_ subviews: Subviews) -> CGFloat { spacing * CGFloat(max(subviews.count - 1, 0)) }

    /// Each part as tall as it wants; over the cap, the part that gives as tall as what the others leave.
    private func heights(_ width: CGFloat, _ subviews: Subviews) -> [CGFloat] {
        var heights = subviews.map { $0.sizeThatFits(ProposedViewSize(width: width, height: nil)).height }
        let total = heights.reduce(0, +) + gaps(subviews)
        guard cap.isFinite, total > cap, let i = subviews.firstIndex(where: { $0[CardBodyGives.self] }) else { return heights }
        let room = max(cap - (total - heights[i]), 0)
        heights[i] = subviews[i].sizeThatFits(ProposedViewSize(width: width, height: room)).height
        return heights
    }
}

/// Long words in a pending card, folded: at most `lines` lines of them, fewer when the card is short (`CardBodyStack`
/// offers less; never fewer than two), cut between lines, the last one fading as a long message's does (`LineFold`), and
/// under them `title` (`FoldToggle`), which opens the whole text (`open`): a full-screen sheet where it scrolls. Words
/// that fit are drawn whole, with nothing under them.
struct CardFold<Words: View>: View {
    var lines: Int
    /// The words' type: what a line is (`FoldLinesProbe`).
    var font: Font = Theme.Font.code
    var level: Theme.Level? = nil
    var box: CardFoldBox = .code
    /// The words are only the start of the text: there is more than they show, however tall they are.
    var more = false
    /// Reading content (a plan): the words, and the lines they are cut at, grow past the interface's cap
    /// (`readingContent()`); its toggle stays capped as the interface is.
    var reading = false
    var title: String
    /// The toggle's accessibility identifiers, "<id>.expand".
    var id: String
    var open: () -> Void
    @ViewBuilder var words: () -> Words

    /// Folded, never fewer lines than this.
    static var fewestLines: Int { 2 }

    @State private var full: CGFloat = 0
    @State private var shown: CGFloat = 0
    @State private var cut: CGFloat = 0

    private var line: CGFloat { cut / CGFloat(max(lines, 1)) }
    private var folds: Bool { cut > 0 && shown > 0 && (more || full > shown + 1) }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            boxed(clip)
            if folds {
                FoldToggle(open: false, prefix: id, title: title) {
                    Haptics.tap()
                    open()
                }
            }
        }
    }

    private var clip: some View {
        FoldClip(cut: cut, line: line, floor: line * CGFloat(Self.fewestLines), more: more) {
            sized(words())
                .fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { full = $0 }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .clipped()
        .contentShape(.rect) // what is folded away takes no touches
        .mask { if folds { FoldFadeMask(line: line) } else { Rectangle() } }
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { shown = $0 }
        .background(alignment: .top) { sized(FoldLinesProbe(lines: lines, font: font, level: level) { cut = $0 }) }
    }

    @ViewBuilder private func sized(_ content: some View) -> some View {
        if reading { content.readingContent() } else { content }
    }

    @ViewBuilder private func boxed(_ content: some View) -> some View {
        switch box {
        case .none: content
        case .code:
            content.padding(Theme.Space.m)
                .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        case .well:
            content.padding(Theme.Space.m)
                .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        }
    }
}

/// What a `CardFold`'s words sit in: nothing, a command's code box, or the well a terminal's words had.
enum CardFoldBox { case none, code, well }

/// What a `CardFold` shows of its words: as tall as they are, up to `cut`; offered less, whole lines (`line` each) of what
/// it is offered, down to `floor`. The words are laid out whole at their own height; the clip around it cuts them.
private struct FoldClip: Layout {
    var cut: CGFloat
    var line: CGFloat
    var floor: CGFloat
    var more: Bool

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let words = subviews.first else { return .zero }
        let size = words.sizeThatFits(ProposedViewSize(width: proposal.width, height: nil))
        return CGSize(width: proposal.width ?? size.width,
                      height: CardFoldCut.height(full: size.height, cut: cut, line: line, floor: floor, offered: proposal.height, more: more))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(at: bounds.origin, anchor: .topLeading, proposal: ProposedViewSize(width: bounds.width, height: nil))
    }
}

/// How tall a `CardFold`'s words show: `full` tall, at most `cut` (its lines); offered less, the whole lines (`line`
/// each) that fit, never under `floor` (its fewest lines) unless the words are shorter. A fold that would hide fewer
/// than `leastHidden` lines shows the words whole instead (#6605: a three-line command cut to two with Show All for one
/// line was more trouble than the line), unless they are only the start of the text (`more`). Not measured yet: whole.
nonisolated enum CardFoldCut {
    static let leastHidden = 3
    /// Rounding the cut may ignore (points).
    static let slack: CGFloat = 0.5

    static func height(full: CGFloat, cut: CGFloat, line: CGFloat, floor: CGFloat, offered: CGFloat?, more: Bool = false) -> CGFloat {
        guard cut > 0, line > 0 else { return full }
        var shown = min(full, cut)
        // Whole lines of what is offered, half a point either way: placing the fold rounds its toggle to the screen's
        // pixels, and an offer a hair under seven lines lost the seventh, which left a line of empty room under it.
        if let offered, offered < shown { shown = max(((offered + slack) / line).rounded(.down) * line, min(full, floor)) }
        // Hidden lines, from heights that carry a little rounding: under `leastHidden` and a half less, none at all.
        if !more, full - shown < (CGFloat(leastHidden) - 0.5) * line { return full }
        return shown
    }
}

/// A command (or a terminal's words) in a pending card: code type in its box, folded to `lines` lines (`CardFold`), and
/// Show All N Lines, as the chat's tool output says it, opening it whole in the full-text sheet (`FullOutputSheet`),
/// where it scrolls and Copy All copies it.
struct CardCode: View {
    var text: String
    /// The sheet's title: the tool, the command's own words.
    var title: String
    var lines = CardCode.lines
    var box: CardFoldBox = .code
    /// The terminal's words under its question: in the secondary color, as they were.
    var secondary = false
    var id = "card-code"
    @State private var showsAll = false

    /// A command's lines before Show All, when the card has the room (6–8, the owner's call for #6605).
    static let lines = 8

    var body: some View {
        let all = ToolTextMemo.codeLines(text)
        // Only its start is laid out: a command can be thousands of lines, and the card shows a few of them. Past
        // `lines` and `leastHidden` more it folds whatever their width; up to that the words measured decide.
        let start = all.count > lines + CardFoldCut.leastHidden ? all.prefix(lines + CardFoldCut.leastHidden).joined(separator: "\n") : text
        // A count of lines only where there are more lines than it shows; one long line wrapped says Show All Lines.
        let toggle = all.count > lines ? String(localized: "Show All \(all.count) Lines [Tool Views 3aed77]") : String(localized: "Show All Lines")
        CardFold(lines: lines, box: box, more: start.count < text.count, title: toggle, id: id, open: { showsAll = true }) {
            Text(start)
                .font(Theme.Font.code)
                .foregroundStyle(secondary ? .secondary : .primary)
                .textSelection(.enabled)
                .accessibilityIdentifier(id)
        }
        .sheet(isPresented: $showsAll) { FullOutputSheet(text: text, title: title) }
        .givesCardRoom()
    }
}

/// A plan in its approval card: the first lines of it (`CardFold`, 8 body lines when the card has the room, #6605), and
/// Show Full Text opening it whole (`open`: the card's plan sheet). It scrolled in a box of its own inside the card's scroll.
struct CardPlan: View {
    var plan: String
    var lines = CardPlan.lines
    var open: () -> Void

    static let lines = 8
    /// The plan's first lines laid out in the card: more than its lines take even as a list of short items.
    private static let startLines = 30

    var body: some View {
        let all = ToolTextMemo.codeLines(plan)
        let start = all.count > Self.startLines ? all.prefix(Self.startLines).joined(separator: "\n") : plan
        CardFold(lines: lines, font: Theme.Font.body, level: .body, box: .none, more: start.count < plan.count, reading: true,
                 title: String(localized: "Show Full Text"), id: "card-plan", open: open) {
            Markdown(MarkdownMemo.content(start))
                .markdownTheme(.persona)
                .markdownTextStyle { ForegroundColor(Theme.primaryText) }
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .givesCardRoom()
    }
}
