import BeautifulUI
import PersonaKit
import SwiftUI

// Quotes, as Telegram's replies (#986): what goes with the next message sits in the composer's box over the field — a
// rule, who or what is quoted, a line or two of it, × — and the sent bubble opens with the same block, which leads
// back to what it quotes. Every quote is a `ContextRef` (`<persona-context>` before the message); the hub's own
// context of a Feishu /agent request (instructions, documents) is not a quote and stays folded (`ContextGroup`).

nonisolated extension ContextRef {
    /// Quote on a message of this chat: who wrote it (「我」 for the user's own, as the agent reads it), its id, and the
    /// part quoted (`selectionPart`, or nil for the whole message).
    static func quote(_ text: String, from: String, msg: String, part: String? = nil) -> ContextRef {
        ContextRef(kind: "quote", title: from, url: "", text: text, msg: msg, part: part)
    }

    /// `part` of a quote of words picked in a message (Select Text › Quote, #4549).
    static let selectionPart = "selection"

    /// `refs` with `ref` added, or nil when it is there already: a message goes over the box once as a whole and once
    /// per picked part (a message without an id every time).
    static func adding(_ ref: ContextRef, to refs: [ContextRef]) -> [ContextRef]? {
        let sameKind = ref.kind == "quote" || ref.kind == ForwardMessage.kind
        let there = sameKind && !ref.msg.isEmpty && refs.contains {
            $0.kind == ref.kind && $0.msg == ref.msg && $0.part == ref.part && (ref.part == nil || $0.text == ref.text)
        }
        return there ? nil : refs + [ref]
    }

    /// A row picked in the @ menu, or nil when it is already over the box. Workspaces have an empty URL like an
    /// ordinary quote, so their dedupe is kind + URL + body (the web's rule), never that shared URL alone. Other
    /// mention kinds keep their existing URL-only behavior among themselves.
    static func addingMention(_ ref: ContextRef, to refs: [ContextRef]) -> [ContextRef]? {
        let there = refs.contains { old in
            if ref.kind == "workspace" || old.kind == "workspace" {
                return ref.kind == "workspace" && old.kind == "workspace" && old.url == ref.url && old.text == ref.text
            }
            return old.url == ref.url
        }
        return there ? nil : refs + [ref]
    }

    /// What the user picked to go with a message (a message, a page's text, a workspace, a session, a recording…),
    /// shown as a quote.
    var isQuote: Bool { ["quote", "context", "edit", "workspace", ForwardMessage.kind].contains(kind) }

    var isForward: Bool { kind == ForwardMessage.kind }

    /// A message as it goes: the blocks one per line, a blank line, then what was typed (spec/fixtures/forward/several.json).
    static func message(_ refs: [ContextRef], body: String) -> String {
        refs.isEmpty ? body : refs.map(\.xml).joined(separator: "\n") + "\n\n" + body
    }

    /// A message's contexts as quotes, or nil when the hub put them there (a Feishu /agent request): those fold.
    static func quotes(_ refs: [ContextRef]) -> [ContextRef]? {
        refs.isEmpty || !refs.allSatisfy(\.isQuote) ? nil : refs
    }

    /// An @-picked session as context (not a forward, whose link names its message's session too).
    var isSession: Bool {
        guard kind != ForwardMessage.kind, let components = URLComponents(string: url) else { return false }
        return HubNaming.accepts(scheme: components.scheme) && components.host == "session" && components.path.hasPrefix("/")
    }

    #if DEBUG
    /// `-showSampleQuotes YES`: what Add as Context on a page and @ on a session put over the box.
    static let samples = [
        ContextRef(kind: "context", title: "WidgetKit 时间线", url: "https://example.com/docs/widgetkit/timeline", // i18n-ignore
                   text: "时间线里的每一项都有一个日期，系统按这个日期换上新的内容；刷新策略决定时间线用完以后什么时候再要一条。"), // i18n-ignore
        ContextRef(kind: "context", title: "照片同步支持 HEIC", url: "persona://session/studio/a1f3c2d4-0000-4000-8000-000000000005", // i18n-ignore
                   text: "另一个会话：照片同步支持 HEIC（Claude）\n目录：~/code/photo-sync\n它最近说：计划写好了，等你批准后开始改。"), // i18n-ignore
    ]
    #endif

    /// A knowledge note or a to-do from Add as Context (#3721): its link leads back to it in the app
    /// (`persona://kb/…`, `persona://task/…`); it quotes the item, not a selection.
    private var isAppItem: Bool {
        guard let components = URLComponents(string: url) else { return false }
        return HubNaming.accepts(scheme: components.scheme) && (components.host == "kb" || components.host == "task")
    }

    /// The page quoted as a whole: the text is the marker the link sheet puts there, not the user's words.
    private var isWholePage: Bool { text == "（整篇）" } // i18n-ignore: marker

    /// The quote's first line: whom or what it quotes.
    var quoteHeading: String {
        switch kind {
        case "quote":
            let who = title.isEmpty ? String(localized: "Messages [Quote Views 4da199]") : title
            if part == Self.selectionPart { return String(localized: "Quoting selected text · \(who)") }
            let mine = title == "我" // i18n-ignore: the sender name Lark gives the user's own messages
            if mine { return String(localized: "Quoting your message") }
            return String(localized: "Quoting \(who)")
        case "edit": return String(localized: "Text to change · \(label)")
        case "workspace": return String(localized: "Workspace · \(label)")
        case ForwardMessage.kind: return ForwardMessage.heading(self)
        default:
            if isSession { return String(localized: "Quoting session \(label)") }
            if !url.isEmpty, !isAppItem { return isWholePage ? String(localized: "Quoting \(label)") : String(localized: "Quoting selected text · \(label)") }
            return String(localized: "Quoting \(label)")
        }
    }

    /// A line or two of what is quoted, its line breaks as spaces.
    var quoteSummary: String {
        if isWholePage { return host }
        var flat = text.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        if isSession || kind == "workspace", flat.count > 1 { flat.removeFirst() } // the heading names it already
        let joined = flat.joined(separator: " ")
        return joined.isEmpty ? String(localized: "No content") : String(joined.prefix(240))
    }

    /// A forward's words in its card: its own lines, blank ones dropped.
    var forwardSummary: String {
        let lines = text.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        return lines.isEmpty ? String(localized: "No content") : String(lines.joined(separator: "\n").prefix(1200))
    }
}

extension Theme {
    /// Quotes' colors (`QuoteBar`, `QuoteBlock`).
    nonisolated enum Quote {
        /// The block's fill under its rule and words: a tint of the rule's color (a forward's card, Inbox replies); in
        /// the user's bubble a tint of the accent or a shade (`UserBubbleInk.quoteFill`).
        static let fillOpacity = 0.12
        /// The band left on the message a quote led back to, as it starts to fade.
        static let flashOpacity = 0.18
        /// How long the band stays before it fades (`Theme.Motion.layout`): a quote's tap, where you just looked.
        static let flashHold: Duration = .milliseconds(700)
        /// The band on a message a chat opened at (a search result, a hub link, #6284): the chat has just appeared, so
        /// it stays long enough to find, as the web's ring does (#6446).
        static let landingHold: Duration = .milliseconds(1600)
    }

    /// The user's own bubble as it is drawn, and the colors of what sits in it (#5204). Persona's main conversation
    /// draws the new layout's pale box under the primary words (`Theme.ChatBox.user`, whatever the style), the other
    /// chats the style's bubble under its words. Quotes, forwards, text to change and comment blocks read their colors
    /// from here, never from the style alone: the style's white words of a colored bubble were near invisible on the box.
    /// Held to the floors in every style and Theme Color by `StyleSpecTests`.
    nonisolated struct UserBubbleInk {
        /// The box's or the bubble's color; a colored bubble deepens toward its bottom (`Theme.deepening`).
        var bubble: Color
        var deepens: Bool
        /// The words, and a block's rule, heading and words.
        var words: Color
        /// A path linked in the words (underlined): the accent, as in replies, on a pale bubble; the words' color on a
        /// colored one, where the accent would not show.
        var link: Color
        /// A block's fill: the accent's tint on a pale bubble; on a colored one a shade, as the bubble's own deepening,
        /// since a veil of its light words would lighten what they sit on.
        var quoteFill: Color

        var fill: AnyShapeStyle { deepens ? AnyShapeStyle(Theme.deepening(bubble)) : AnyShapeStyle(bubble) }

        /// `boxed`: the new layout's box (`ChatLayout.on`).
        static func of(_ spec: StyleSpec, boxed: Bool) -> UserBubbleInk {
            let tint = spec.accent.opacity(Quote.fillOpacity)
            if boxed {
                return UserBubbleInk(bubble: Theme.ChatBox.user, deepens: false, words: .primary, link: spec.accent, quoteFill: tint)
            }
            let colored = spec.deepensBubble
            return UserBubbleInk(bubble: spec.userBubble, deepens: colored, words: spec.userBubbleText,
                                 link: colored ? spec.userBubbleText : spec.accent,
                                 quoteFill: colored ? Color.black.opacity(Quote.fillOpacity) : tint)
        }
    }
}

/// The rule, the heading and the summary: one quote's words, in a bar over the box or in a bubble.
private struct QuoteLines: View {
    var heading: String
    /// A forward's arrow before the heading (#3807).
    var icon: String? = nil
    /// A forward's computer and time, under the heading.
    var meta: String? = nil
    var summary: String
    var lines: Int
    /// Several forwarded together (#3838): a line each ("Claude: the words"), in place of `summary`.
    var rows: [String] = []
    /// A forward's route over the box: where it comes from and where it goes.
    var note: String? = nil
    var tint: Color
    var ink: Color
    /// The rule is drawn by the block's own shape (BeautifulUI's card), not beside the words.
    var rule = true

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            if rule { Capsule().fill(tint).frame(width: Theme.Size.bar) }
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(spacing: Theme.Space.xs) {
                    if let icon { ThemedIcon(icon).font(Theme.Font.captionStrong).foregroundStyle(tint).accessibilityHidden(true) }
                    Text(heading).font(Theme.Font.captionStrong).foregroundStyle(tint).lineLimit(1).truncationMode(.tail)
                }
                if let meta { Text(meta).font(Theme.Font.label).foregroundStyle(ink).lineLimit(1) }
                if rows.isEmpty {
                    Text(summary).font(Theme.Font.caption).foregroundStyle(ink).lineLimit(lines).truncationMode(.tail)
                } else {
                    ForEach(Array(rows.prefix(lines).enumerated()), id: \.offset) { _, row in
                        Text(row).font(Theme.Font.caption).foregroundStyle(ink).lineLimit(1).truncationMode(.tail)
                    }
                    if rows.count > lines {
                        Text(String(localized: "\(rows.count - lines) More [Forwarded Messages]")).font(Theme.Font.label).foregroundStyle(ink)
                    }
                }
                if let note {
                    // Two lines: a computer's and an Agent's names are both in it, and a cut in the middle hides one.
                    Text(note).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2).truncationMode(.tail)
                        .accessibilityIdentifier("composer.forwardRoute")
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .fixedSize(horizontal: false, vertical: true)
    }
}

/// What goes with the next message, over the composer's field inside its box: a bar for each quote, stacked; × drops
/// one. One quote shows two lines of itself, several one line each.
struct QuoteBar: View {
    @Binding var refs: [ContextRef]
    /// Feishu's pages draw their own blue; elsewhere the style's accent.
    var tint: Color?

    var body: some View {
        // Several forwarded together (#3838) are one row with a line each, and one × takes them all.
        let runs = ForwardMessage.runs(refs).map { $0.map { refs[$0] } }
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            ForEach(runs, id: \.[0].id) { run in
                let r = run[0]
                if run.count > 1 {
                    QuoteBarRow(heading: ForwardMessage.groupHeading(run), summary: "", lines: runs.count == 1 ? 3 : 2,
                                rows: run.map(ForwardMessage.line), tint: tint, icon: Theme.Icon.forwardMessage, note: r.route,
                                removeLabel: String(localized: "Remove Forward")) {
                        let ids = Set(run.map(\.id))
                        withAnimation(Theme.Motion.state) { refs.removeAll { ids.contains($0.id) } }
                    }
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                } else {
                    QuoteBarRow(heading: r.quoteHeading, summary: r.quoteSummary, lines: refs.count == 1 ? 2 : 1, tint: tint,
                                icon: r.isForward ? Theme.Icon.forwardMessage : nil, note: r.route,
                                removeLabel: r.isForward ? String(localized: "Remove Forward") : String(localized: "Remove Quote")) {
                        withAnimation(Theme.Motion.state) { refs.removeAll { $0.id == r.id } }
                    }
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("composer.quotes")
    }
}

/// One quote over the box: its words and ×. Liquid glass and the other styles draw Telegram's bar (a rule and the
/// words on the box itself); BeautifulUI a quiet inset card with the rule along its edge.
struct QuoteBarRow: View {
    var heading: String
    var summary: String
    var lines = 2
    /// Several forwarded together: a line each (`QuoteLines.rows`).
    var rows: [String] = []
    var tint: Color?
    var icon: String? = nil
    /// A forward's route (`ContextRef.route`), under its words.
    var note: String? = nil
    /// What × says to VoiceOver (Feishu's reply: Cancel Reply).
    var removeLabel = String(localized: "Remove Quote")
    var remove: () -> Void
    @Environment(\.buiTheme) private var theme

    var body: some View {
        let bui = AppStyle.current.usesBeautifulUI
        let tint = tint ?? (bui ? theme.colors.accent : Theme.accent)
        HStack(spacing: Theme.Space.xs) {
            QuoteLines(heading: heading, icon: icon, summary: summary, lines: lines, rows: rows, note: note, tint: tint,
                       ink: bui ? theme.colors.ink2 : .secondary, rule: !bui)
                .padding(.vertical, bui ? Theme.Space.s : Theme.Space.xxs)
                .padding(.leading, bui ? Theme.Space.m : 0)
            Button(action: remove) {
                ThemedIcon(Theme.Icon.close).font(Theme.Font.labelStrong)
                    .foregroundStyle(bui ? theme.colors.ink3 : Color.secondary)
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                    .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(removeLabel)
            .accessibilityIdentifier(removeLabel)
        }
        .background {
            if bui {
                RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous).fill(theme.colors.inset)
                    .overlay(alignment: .leading) { Rectangle().fill(tint).frame(width: Theme.Size.bar) }
                    .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
            }
        }
        .accessibilityElement(children: .contain)
    }
}

extension EnvironmentValues {
    /// A quote's block in a sent bubble was tapped: the chat brings back what it quotes (the message, highlighted) and
    /// says whether it could; when not, the block opens to the whole text.
    @Entry var openQuote: ((ContextRef) -> Bool)? = nil
}

/// A quote at the top of a sent bubble: the same words as over the box, in the bubble's colors (`ink`). A tap leads
/// back to the message it quotes (or opens the page it is from); with nothing to go back to, it opens to the whole text.
struct QuoteBlock: View {
    var ref: ContextRef
    var ink: Theme.UserBubbleInk
    @Environment(\.openQuote) private var openQuote
    @Environment(\.openLink) private var openLink
    @Environment(\.chatPin) private var pin
    @State private var open = false
    /// What the chat's pin knows it by (a quote has no id of its own; this view's is kept while it is drawn).
    @State private var pinKey = "quote-" + UUID().uuidString

    /// A forward shows this many lines of the original until it is opened.
    static let forwardLines = 4

    var body: some View {
        if ref.isForward {
            QuoteCard(heading: ref.quoteHeading, icon: Theme.Icon.forwardMessage, meta: ForwardMessage.meta(ref),
                      summary: open ? ref.text : ref.forwardSummary, lines: open ? .max : Self.forwardLines,
                      tint: ink.words, ink: ink.words, fill: ink.quoteFill, action: tap)
                .accessibilityHint(String(localized: "Opens the original message"))
                .accessibilityIdentifier("bubble.forward")
                .chatPinned(pinKey)
        } else {
            QuoteCard(heading: ref.quoteHeading, summary: open ? ref.text : ref.quoteSummary, lines: open ? .max : 2,
                      tint: ink.words, ink: ink.words, fill: ink.quoteFill, action: tap)
                .accessibilityHint(ref.msg.isEmpty ? "" : String(localized: "Jumps to the original message"))
                .chatPinned(pinKey)
        }
    }

    private func tap() {
        Haptics.tap()
        if openQuote?(ref) == true { return }
        if !ref.isSession, let u = URL(string: ref.url), u.scheme?.hasPrefix("http") == true, let openLink {
            openLink(u)
            return
        }
        // In the chat its top stays where it is and the text opens under it (#3757).
        toggleInPlace(pin, pinKey, animation: Theme.Motion.state, closing: open) { open.toggle() }
    }
}

/// A quote's block in a bubble, as a button: liquid glass and the other styles draw Telegram's (a rule beside the
/// words on a tint), BeautifulUI a card with the rule along its edge.
struct QuoteCard: View {
    var heading: String
    var icon: String? = nil
    var meta: String? = nil
    var summary: String
    var lines = 2
    var tint: Color
    var ink: Color
    /// Under the words: a tint of `tint` unless given (the user's bubble gives its own, `Theme.UserBubbleInk`).
    var fill: Color? = nil
    var action: () -> Void

    var body: some View {
        let bui = AppStyle.current.usesBeautifulUI
        Button(action: action) {
            QuoteLines(heading: heading, icon: icon, meta: meta, summary: summary, lines: lines, tint: tint, ink: ink, rule: !bui)
                .quoteCardFill(tint, fill: fill)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel([heading, meta, summary].compactMap { $0 }.joined(separator: ": "))
        .accessibilityIdentifier("bubble.quote")
    }
}

extension View {
    /// A quote card's padding and fill in a sent bubble: `fill`, else a tint of `tint`; its rule along the edge in
    /// BeautifulUI.
    func quoteCardFill(_ tint: Color, fill: Color? = nil) -> some View {
        let bui = AppStyle.current.usesBeautifulUI
        let shape = RoundedRectangle(cornerRadius: bui ? Theme.Radius.control : Theme.Radius.badge, style: .continuous)
        return padding(.vertical, Theme.Space.sm)
            .padding(.leading, bui ? Theme.Space.m : Theme.Space.s)
            .padding(.trailing, Theme.Space.m)
            .background {
                shape.fill(fill ?? tint.opacity(Theme.Quote.fillOpacity))
                    .overlay(alignment: .leading) {
                        if bui { Rectangle().fill(tint).frame(width: Theme.Size.bar) }
                    }
                    .clipShape(shape)
            }
    }
}

/// Several messages forwarded together (#3838) in a sent bubble: one card naming the chat and the computer, then each
/// message with who wrote it and when and its first lines, in order. A tap on one leads back to it; past `shown`, the
/// rest open with "N More". In the bubble's colors (`ink`), as the other blocks in it.
struct ForwardGroupBlock: View {
    var refs: [ContextRef]
    var ink: Theme.UserBubbleInk
    @Environment(\.openQuote) private var openQuote
    @State private var open = false

    static let shown = 3
    static let lines = 2

    var body: some View {
        let bui = AppStyle.current.usesBeautifulUI
        let tint = ink.words
        let words = ink.words
        let visible = open ? refs : Array(refs.prefix(Self.shown))
        HStack(spacing: Theme.Space.s) {
            if !bui { Capsule().fill(tint).frame(width: Theme.Size.bar) }
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    HStack(spacing: Theme.Space.xs) {
                        ThemedIcon(Theme.Icon.forwardMessage).font(Theme.Font.captionStrong).foregroundStyle(tint).accessibilityHidden(true)
                        Text(ForwardMessage.groupHeading(refs)).font(Theme.Font.captionStrong).foregroundStyle(tint)
                            .lineLimit(1).truncationMode(.tail)
                    }
                    if let machine = refs[0].extra?["machine"], !machine.isEmpty {
                        Text(machine).font(Theme.Font.label).foregroundStyle(words).lineLimit(1)
                    }
                }
                .accessibilityElement(children: .combine)
                ForEach(visible) { r in
                    Button { tap(r) } label: {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(ForwardMessage.byline(r)).font(Theme.Font.labelStrong).foregroundStyle(words).lineLimit(1)
                            Text(r.forwardSummary).font(Theme.Font.caption).foregroundStyle(words)
                                .lineLimit(Self.lines).truncationMode(.tail)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(ForwardMessage.byline(r) + ": " + r.forwardSummary)
                    .accessibilityHint(String(localized: "Opens the original message"))
                }
                if refs.count > visible.count {
                    Button(String(localized: "\(refs.count - visible.count) More [Forwarded Messages]")) {
                        withAnimation(Theme.Motion.state) { open = true }
                    }
                    .buttonStyle(.plain)
                    .font(Theme.Font.labelStrong).foregroundStyle(tint).lineLimit(1)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .fixedSize(horizontal: false, vertical: true)
        .quoteCardFill(tint, fill: ink.quoteFill)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("bubble.forwardGroup")
    }

    private func tap(_ r: ContextRef) {
        Haptics.tap()
        if openQuote?(r) == true { return }
        withAnimation(Theme.Motion.state) { open = true }
    }
}

/// The band a quote's tap leaves on the message it led back to, fading on its own.
struct QuoteFlash: View {
    var hold: Duration = Theme.Quote.flashHold
    @State private var on = true

    var body: some View {
        Rectangle().fill(Theme.accent.opacity(on ? Theme.Quote.flashOpacity : 0))
            .allowsHitTesting(false)
            .task {
                try? await Task.sleep(for: hold)
                withAnimation(Theme.Motion.layout) { on = false }
            }
    }
}

/// A column as wide as its widest part at its own size (at most what it is offered), every part laid out at that
/// width: a bubble with a quote is as wide as the longer of the two, the quote's block across all of it.
struct WidestColumn: Layout {
    var spacing: CGFloat = 0

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = width(proposal, subviews)
        let heights = subviews.map { $0.sizeThatFits(ProposedViewSize(width: width, height: nil)).height }
        return CGSize(width: width, height: heights.reduce(0, +) + spacing * CGFloat(max(subviews.count - 1, 0)))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        let offer = ProposedViewSize(width: bounds.width, height: nil)
        for s in subviews {
            let h = s.sizeThatFits(offer).height
            s.place(at: CGPoint(x: bounds.minX, y: y), proposal: ProposedViewSize(width: bounds.width, height: h))
            y += h + spacing
        }
    }

    private func width(_ proposal: ProposedViewSize, _ subviews: Subviews) -> CGFloat {
        let ideal = subviews.map { $0.sizeThatFits(.unspecified).width }.max() ?? 0
        guard let offered = proposal.width else { return ideal }
        return min(ideal, offered)
    }
}

/// The words of the user's bubble under the quotes it opens with (none: the plain bubble as it always was).
struct UserBubbleBody: View {
    var quotes: [ContextRef]
    var text: String
    /// The paths in `text`: linked where they are there on the machine (`PathText`).
    var paths: [PathRef] = []
    /// The comment blocks the message led with (`MessageComments.inBubble`), and the plain quote before them.
    var comments: [MessageComment] = []
    var lead: String?
    /// Folds the words past the web's lines (`BubbleFold`, #5676): the chat's bubbles. nil: whole (the lifted copy, the
    /// first message of a conversation starting).
    var fold: BubbleFoldKey? = nil
    /// The new conversation layout (#1666, `ChatLayout`): the user's box is pale blue under the primary text color
    /// (`Theme.UserBubbleInk`).
    @Environment(\.chatLayout) private var layout

    var body: some View {
        // The box or the style's bubble, and everything in it in its colors (#5204).
        let ink = Theme.UserBubbleInk.of(.current, boxed: layout.on)
        Group {
            if quotes.isEmpty, comments.isEmpty, lead == nil {
                words(ink).padding(.horizontal, Theme.Space.l).padding(.vertical, Theme.Space.m)
                    .background(ink.fill, in: .rect(cornerRadius: Theme.Radius.bubble, style: .continuous))
            } else {
                quoted(ink)
            }
        }
        .readingContent()
    }

    private func words(_ ink: Theme.UserBubbleInk) -> some View {
        BubbleFold(text: text, fold: fold, tint: ink.link) { shown in
            // Folded, only the start is drawn: the paths in it.
            PathText(text: shown, refs: paths.isEmpty || shown == text ? paths : paths.filter { NSMaxRange($0.range) <= (shown as NSString).length },
                     link: ink.link)
                .font(Theme.Font.body).lineHeight(.body).foregroundStyle(ink.words)
                .selectableInLift(text, as: .plain(ink.words)) // #603
                #if os(macOS)
                .textSelection(.enabled) // drag to select and ⌘C, as on the web (#2542)
                #endif
        }
    }

    private func quoted(_ ink: Theme.UserBubbleInk) -> some View {
        WidestColumn {
            if !quotes.isEmpty || !comments.isEmpty || lead != nil {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    ForEach(ForwardMessage.runs(quotes).map { $0.map { quotes[$0] } }, id: \.[0].id) { run in
                        if run.count > 1 { ForwardGroupBlock(refs: run, ink: ink) } else { QuoteBlock(ref: run[0], ink: ink) }
                    }
                    if let lead { QuoteBlock(ref: .quote(lead, from: "", msg: ""), ink: ink) }
                    ForEach(Array(comments.enumerated()), id: \.offset) { i, c in CommentQuote(comment: c, number: i + 1, ink: ink) }
                }
                .padding(.horizontal, Theme.Space.s)
                .padding(.top, Theme.Space.s)
                .padding(.bottom, text.isEmpty ? Theme.Space.s : 0)
            }
            if !text.isEmpty {
                words(ink)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, Theme.Space.l)
                    .padding(.vertical, Theme.Space.m)
            }
        }
        .background(ink.fill, in: .rect(cornerRadius: Theme.Radius.bubble, style: .continuous))
    }
}

/// Which line to light up after a quote's tap; a new mark each tap, so tapping again lights it again.
struct QuoteFlashMark: Equatable {
    var line: String
    var id = UUID()
    /// How long it stays lit before fading: `Theme.Quote.flashHold`, or `landingHold` where a chat opened at it.
    var hold: Duration = Theme.Quote.flashHold
}

/// Plain text (a message you sent) with the paths that are there on the conversation's machine as underlined links
/// (docs/apple/ios.md「File viewer」); a tap opens the file viewer at the path's line (`PathLinkURL`, the chat's `openURL`). Until
/// the machine answers, and outside a chat, the text as it is: the same words in the same place.
struct PathText: View {
    @Environment(AppModel.self) private var model: AppModel?
    @Environment(\.fileScope) private var scope
    var text: String
    var refs: [PathRef]
    var link: Color

    var body: some View {
        if let linked {
            Text(linked).tint(link)
        } else {
            Text(text)
        }
    }

    private var linked: AttributedString? {
        // Only a message with paths reads the presence (and is drawn again when an answer comes).
        guard !refs.isEmpty, let model, let scope else { return nil }
        let picked = PathRefs.pick(refs) { model.filePresence.linkable($0, in: scope) }
        return picked.isEmpty ? nil : Self.attributed(text, picked, link: link)
    }

    /// `text` with `refs` (in order, none overlapping) as links.
    static func attributed(_ text: String, _ refs: [PathRef], link: Color) -> AttributedString {
        let ns = text as NSString
        var out = AttributedString()
        var at = 0
        for r in refs where r.range.location >= at {
            guard let url = PathLinkURL.make(r.path, line: r.line) else { continue }
            out += AttributedString(ns.substring(with: NSRange(location: at, length: r.range.location - at)))
            var a = AttributedString(ns.substring(with: r.range))
            a.link = url
            a.foregroundColor = link
            a.underlineStyle = .single
            out += a
            at = NSMaxRange(r.range)
        }
        out += AttributedString(ns.substring(from: at))
        return out
    }
}
