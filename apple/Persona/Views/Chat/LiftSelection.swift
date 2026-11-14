import MarkdownUI
import PersonaKit
import SwiftUI
#if os(iOS) || os(visionOS)
import CoreText
import UIKit
import UIKit.UIGestureRecognizerSubclass
#endif

// MARK: - A lifted message's words, selected where they are (#603)

// A second long press on a lifted message, or Select Text in its bar, selects its words where they are, the system's
// way: handles to drag, the edit menu with Copy, Quote and Look Up. The chat draws words with MarkdownUI, which cannot
// select a range on iOS, so while they are selected a text view laid out like them (`ReplyText`, with the chat's
// spacing from `ReplyMetrics`) lies over them: its words are clear, only its highlight and handles show, and the words
// on screen stay the chat's own, where they were (#3188; drawn by the text view instead, they moved: its layout is
// close to the chat's, not the same). Lists keep their markers' room, tables are a picture's room that copies as its
// Markdown. The bar steps aside for the edit menu; a tap beside the words, or one that clears the selection, brings it
// back; Copy and Quote close the whole thing, as the bar's own buttons do. The Mac selects in the chat itself and
// keeps its Select Text window.

/// The spacing of a reply's Markdown in the chat (`MarkdownUI.Theme.persona`), also used to lay out the same words for
/// selecting (`ReplyText`). em values are times the text size.
nonisolated enum ReplyMetrics {
    /// Between a paragraph's lines, em (the body's line height, `Theme.Level.body`).
    static let lineSpacing: CGFloat = 0.26
    /// Under a paragraph.
    static let paragraphBottom: CGFloat = 10
    /// The first heading level: its size (em) and the space above and below it.
    static let heading1 = (size: CGFloat(1.3), top: CGFloat(16), bottom: CGFloat(8))
    /// The second level, at the text's size.
    static let heading2 = (top: CGFloat(14), bottom: CGFloat(6))
    /// The third level.
    static let heading3 = (top: CGFloat(12), bottom: CGFloat(4))
    /// The fourth to sixth (#3863): a step smaller (the secondary level's size, em), semibold in the quieter ink
    /// (`Theme.Reply.secondaryInk`), with the third's space above and below, so they read as headings under it.
    static let heading4 = (size: Theme.Level.secondary.size / Theme.Level.body.size, top: Theme.Space.m, bottom: Theme.Space.xs)
    /// Above a list item, em.
    static let itemTop: CGFloat = 0.2
    /// Around a code block.
    static let codeTop: CGFloat = 4
    static let codeBottom: CGFloat = 12
    /// Between a code block's lines, em of its own size.
    static let codeLineSpacing: CGFloat = 0.2
    /// A code block's title bar over its code (`ReplyCodeBlock`): Copy's touch height, drawn past the bar's edges.
    static let codeBar: CGFloat = Theme.Size.tapTarget - 2 * Theme.Space.sm
    /// Inline code, and an agent's words between its steps (`QuietMarkdown`), em.
    static let inlineCode: CGFloat = 0.88
    static let quiet: CGFloat = 0.88

    /// A list item's marker: its room, at least 1.5 em (MarkdownUI's markers, right-aligned in it), then the system
    /// `Label`'s gap before the words, the same at every text size (34, 41 and 80 pt to the words at the default size,
    /// xxxLarge and AX5, #6552).
    static func markerRoom(of size: CGFloat) -> CGFloat { points(1.5, of: size) }
    static let markerGap: CGFloat = 8

    /// A size in em as the chat draws it: MarkdownUI sets every run's font at a whole point (`round(size × scale)`),
    /// so inline code at 0.88 em of 17 is 15, not 14.96. Over a line of code the difference came to most of a point (#3188).
    static func points(_ em: CGFloat, of size: CGFloat) -> CGFloat { (em * size).rounded() }
}

/// What starts selecting a lifted message's words: a long press on them at a point (in the window), or Select Text
/// (all of them).
struct InPlaceSelect: Equatable {
    var at: CGPoint?
    var id = UUID()
}

extension EnvironmentValues {
    /// Set on the lifted copy of a message while its words are selected (`MessageLiftLayer`); nil everywhere else.
    @Entry var liftSelect: InPlaceSelect? = nil
}

/// How a message's words are drawn in the chat, for the text view that stands in for them.
enum LiftWords: Equatable {
    /// Your own message, as typed, in its bubble's text color.
    case plain(Color)
    /// A reply's Markdown; quiet: an agent's words between its steps (smaller and gray).
    case reply(quiet: Bool)
}

extension View {
    /// Words a long press lifts (#603): in the lifted copy, while they are selected, a text view laid out like them in
    /// which any part can be selected and copied. Everywhere else they are drawn as they are. parts: an answer drawn in
    /// parts (a lazy stack of them), the lines its parts start on (`AnswerParts.Part.line`): the text view holds all
    /// of its words, its lines on the chat's in each part laid out (`ShownWords.segments`).
    func selectableInLift(_ text: String, as words: LiftWords, parts: [Int]? = nil) -> some View {
        modifier(SelectableInLift(text: text, words: words, parts: parts))
    }
}

private struct SelectableInLift: ViewModifier {
    @Environment(\.liftSelect) private var select
    @Environment(\.liftCopy) private var lifted
    @Environment(MessageMenu.self) private var menu: MessageMenu?
    @Environment(AppModel.self) private var model: AppModel?
    @Environment(\.fileScope) private var scope
    var text: String
    var words: LiftWords
    var parts: [Int]?
    /// The width the words were offered, which they wrap at (they may take less): the text view wraps there too.
    @State private var place = LiftPlace()

    func body(content: Content) -> some View {
        #if os(iOS) || os(visionOS)
        if lifted {
            offered(content.accessibilityHidden(select != nil)) // the text view's words, while it is over them
            // The text view over them, its lines on theirs (`LiftLayout.snap`), a folded quote's hidden ones aside.
            .readingShownWords { shown in
                if let select, let menu {
                    GeometryReader { geo in
                        // Past the words by a handle's reach on every side, the words where they are (its inset): a
                        // knob over the first line or under the last is in it, to be dragged.
                        let reach = Theme.Lift.handleReach
                        LiftTextView(text: source, words: words, parts: parts ?? [], select: select, menu: menu, place: place,
                                     segments: shown.segments)
                            .frame(width: geo.size.width + 2 * reach, height: geo.size.height + 2 * reach)
                            .offset(x: -reach, y: -reach)
                    }
                }
            }
        } else {
            content
        }
        #else
        content
        #endif
    }

    /// What the words are offered (`place`). Parts in a lazy stack, as wide as the copy: nothing between the stack and
    /// the scroll view's own layout, which lays out only what is near the screen.
    @ViewBuilder private func offered(_ words: some View) -> some View {
        if parts == nil { RecordsOffer(place: place) { words } } else { words }
    }

    /// The words as the chat draws them: a reply's paths that are there, written as code, are linked as words in the
    /// body's font, not as code (`LinkedMarkdown.linkable`, `PathMarkdown.linked(plainCode:)`), and so laid out here.
    private var source: String {
        #if os(iOS)
        guard case .reply = words, let scope else { return text }
        let paths = PathMarkdown(text)
        let there = LinkedMarkdown.linkable(paths, model: model, scope: scope)
        guard !there.isEmpty else { return text }
        return paths.linked(plainCode: true) { there.contains($0) }.markdown
        #else
        return text
        #endif
    }
}

#if os(iOS) || os(visionOS)
#if os(iOS)
/// A long press on the lifted message: where it landed, in the window, for its words to be selected there.
/// iOS only: `UIGestureRecognizerRepresentable` is not available on visionOS.
struct LiftPress: UIGestureRecognizerRepresentable {
    var enabled: Bool
    var action: (CGPoint) -> Void

    func makeUIGestureRecognizer(context: Context) -> UILongPressGestureRecognizer {
        let g = UILongPressGestureRecognizer()
        g.minimumPressDuration = 0.35
        return g
    }

    func updateUIGestureRecognizer(_ recognizer: UILongPressGestureRecognizer, context: Context) {
        recognizer.isEnabled = enabled
    }

    func handleUIGestureRecognizerAction(_ recognizer: UILongPressGestureRecognizer, context: Context) {
        if recognizer.state == .began { action(recognizer.location(in: nil)) }
    }
}
#endif

/// Follows one finger on a view, in the window, and nil once it is up, without taking it from anything: it never
/// recognizes and nothing prevents it, so a handle's drag, the scrolling around it and taps all go on as before.
private final class FingerFollower: UIGestureRecognizer {
    private let moved: (CGPoint?) -> Void

    init(_ moved: @escaping (CGPoint?) -> Void) {
        self.moved = moved
        super.init(target: nil, action: nil)
        cancelsTouchesInView = false
        delaysTouchesEnded = false
    }

    override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
        super.touchesBegan(touches, with: event)
        guard event.allTouches?.count == 1, let t = touches.first else { return finish() }
        moved(t.location(in: nil))
    }

    override func touchesMoved(_ touches: Set<UITouch>, with event: UIEvent) {
        super.touchesMoved(touches, with: event)
        if let t = touches.first { moved(t.location(in: nil)) }
    }

    override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent) {
        super.touchesEnded(touches, with: event)
        finish()
    }

    override func touchesCancelled(_ touches: Set<UITouch>, with event: UIEvent) {
        super.touchesCancelled(touches, with: event)
        finish()
    }

    private func finish() {
        state = .failed
        moved(nil)
    }

    override func canPrevent(_ preventedGestureRecognizer: UIGestureRecognizer) -> Bool { false }
    override func canBePrevented(by preventingGestureRecognizer: UIGestureRecognizer) -> Bool { false }
}

private struct LiftTextView: UIViewRepresentable {
    var text: String
    var words: LiftWords
    /// The lines an answer's parts start on; empty: drawn whole.
    var parts: [Int]
    var select: InPlaceSelect
    var menu: MessageMenu
    var place: LiftPlace
    /// The chat's lines, its folded quotes, its line breaks and its tables: in all of the words, or in each part laid
    /// out (`ShownWords.segments`).
    var segments: [LiftSegment]

    func makeUIView(context: Context) -> LiftUITextView {
        let v = LiftUITextView()
        v.holdWords()
        let reach = Theme.Lift.handleReach
        v.textContainerInset = UIEdgeInsets(top: reach, left: reach, bottom: reach, right: reach)
        return v
    }

    func updateUIView(_ v: LiftUITextView, context: Context) {
        let menu = menu
        v.place = place
        v.show(text, words, design: UIFontDescriptor.SystemDesign(StyleSpec.current.fontDesign), // the app's, as RootView sets it
               parts: parts)
        v.snap(to: segments)
        if let bar = menu.lift?.bar,
           let quote = menu.selectionQuote(of: SelectableText(text: bar.text, plain: bar.isUser, msg: bar.id, quotable: !bar.pending)) {
            v.quote = { picked in
                quote(picked)
                menu.close()
            }
        } else {
            v.quote = nil
        }
        v.ended = { menu.endSelection() }
        v.copied = { menu.close() }
        v.begin(select)
    }

    /// Over the words: as large as they are.
    func sizeThatFits(_ proposal: ProposedViewSize, uiView: LiftUITextView, context: Context) -> CGSize? {
        guard let width = proposal.width, let height = proposal.height, width.isFinite, height.isFinite else { return nil }
        return CGSize(width: width, height: height)
    }
}

extension UIFontDescriptor.SystemDesign {
    /// The style's design (`StyleSpec.fontDesign`, set on the whole app): Clay's words are rounded.
    init(_ design: Font.Design) {
        self = switch design {
        case .rounded: .rounded
        case .serif: .serif
        case .monospaced: .monospaced
        default: .default
        }
    }
}

/// The words in a read-only text view laid out by `ReplyLayout` (TextKit 1) over the chat's own, as wide as they are
/// (`place`: the width they were offered), each line breaking where theirs does (`LiftLayout.follow`) and on theirs
/// (`LiftLayout.snap`). Clear: only the highlight and handles show.
final class LiftUITextView: UITextView, UITextViewDelegate, UIEditMenuInteractionDelegate {
    private let storage = NSTextStorage()
    private let layout = ReplyLayout()
    /// What the words under it were offered (`RecordsOffer`).
    var place: LiftPlace?
    private var design = UIFontDescriptor.SystemDesign.default
    /// Its lines on the chat's (`LiftLayout.snap`): the chat's baselines, its folded quotes' cards (their lines past the
    /// fold are hidden) and where its lines break (`LiftLayout.follow`), in all of the words or in each part laid out;
    /// and the delegate that puts each line there.
    private var segments: [LiftSegment] = []
    private let snapper = LineSnap()
    /// An answer drawn in parts: the lines its parts start on, and where each part's words are in the text.
    private var parts: [Int] = []
    private var partRanges: [Int: NSRange] = [:]
    /// The menu shown over the words a press picked. The text view's own menu only comes up on its own gestures, and
    /// its interaction is not among `interactions`; this one shows the same items.
    private lazy var menuInteraction = UIEditMenuInteraction(delegate: self)
    private var source: (text: String, words: LiftWords)?
    /// The tables' sizes as the copy draws them, by part (`LiftTables`): kept for a part no longer laid out.
    private var tables: [Int: [CGSize]] = [:]
    /// The width the words were laid out for (a table's picture and columns depend on it).
    private var laidOut: CGFloat = 0
    /// Its lines are to go on the chat's (`resnap`), once, in its next layout: the words and the chat's lines change
    /// in the same frame, several times as the copy settles.
    private var snapPending = false
    /// Selecting starts a step a frame (`start`): its words, then their lines on the chat's, then the selection, then
    /// the menu. On a long answer each takes tens of milliseconds, too long together for one frame (#5291); nothing
    /// shows before the selection (the words are clear).
    private let steps = LiftFrames()
    /// Its words wait for `start`'s first step, and their lines (`resnap`) for the step after (`holdWords`).
    private var wordsHeld = false
    private var snapHeld = false
    private var begun: UUID?
    /// Something is selected: when that goes, the selecting is over.
    private var picked = false
    var quote: ((String) -> Void)?
    var ended: () -> Void = {}
    var copied: () -> Void = {}
    /// A finger on the words (in the window), where it went down, the selection then, and which end of it the finger
    /// drags: the handle it went down on, or the end a change of the selection moved (a press's, word by word); held at
    /// an edge it scrolls on (`edgeTick`).
    private var finger: CGPoint?
    private var down: CGPoint?
    private var touched: NSRange?
    private var draggedEnd: LiftLayout.End?
    private var edgeLink: CADisplayLink?

    init() {
        let container = NSTextContainer(size: CGSize(width: 0, height: CGFloat.greatestFiniteMagnitude))
        container.widthTracksTextView = false // the words' wrapping width, set in `layoutSubviews`
        container.lineFragmentPadding = 0
        layout.addTextContainer(container)
        layout.delegate = snapper
        storage.addLayoutManager(layout)
        super.init(frame: .zero, textContainer: container)
        isEditable = false
        isSelectable = true
        isScrollEnabled = false
        textContainerInset = .zero
        backgroundColor = .clear
        clipsToBounds = false
        linkTextAttributes = [.foregroundColor: UIColor.clear]
        delegate = self
        accessibilityIdentifier = "lift-text"
        addInteraction(menuInteraction)
        addGestureRecognizer(FingerFollower { [weak self] in self?.follow($0) })
        // Reading content: not under the window's cap (`WindowTextSize`).
        traitOverrides.preferredContentSizeCategory = WindowTextSize.readingCategory
        registerForTraitChanges([UITraitPreferredContentSizeCategory.self, UITraitUserInterfaceStyle.self]) { (v: LiftUITextView, _) in
            v.render(width: v.laidOut)
        }
    }

    required init?(coder: NSCoder) { fatalError("not from a nib") }

    /// Its words wait for Select Text's first step (`start`): the frame it is tapped in does not lay them out. A view
    /// not held lays them out as they come.
    func holdWords() { wordsHeld = true }

    func show(_ text: String, _ words: LiftWords, design: UIFontDescriptor.SystemDesign = .default, parts: [Int] = []) {
        guard source?.text != text || source?.words != words || self.design != design || self.parts != parts else { return }
        source = (text, words)
        self.design = design
        self.parts = parts
        if !wordsHeld { render(width: laidOut > 0 ? laidOut : wrapWidth) }
    }

    /// Laid out once its width is known (the width the words were offered, `place`, or its own).
    private func render(width: CGFloat) {
        guard let source, width > 0 else { return }
        laidOut = width
        textContainer.size = CGSize(width: width, height: .greatestFiniteMagnitude)
        let look = ReplyText.Look(source.words, design: design, traits: traitCollection)
        let words: NSAttributedString = switch source.words {
        case .plain: ReplyText.plain(source.text, look: look)
        case .reply: ReplyText.attributed(source.text, look: look, width: width, tables: tables, parts: parts)
        }
        let shown = ReplyText.clear(words)
        partRanges = [:]
        if !parts.isEmpty {
            shown.enumerateAttribute(.replyPart, in: NSRange(location: 0, length: shown.length)) { value, range, _ in
                guard let n = value as? Int else { return }
                partRanges[n] = partRanges[n].map { NSUnionRange($0, range) } ?? range
            }
        }
        // Before anything lays them out: setting a text view's text may lay it out then and there.
        followChat(in: shown)
        attributedText = shown
        snapSoon()
        #if DEBUG
        setNeedsLayout() // `LiftLines`
        #endif
    }

    /// The chat's lines, for this view's to go on, its folded quotes' cards, where its lines break and its tables: in
    /// all of the words, or in each part laid out.
    func snap(to segments: [LiftSegment]) {
        guard !Self.same(segments, self.segments) else { return }
        self.segments = segments
        var tables = tables
        for s in segments where !s.tables.isEmpty { tables[s.part ?? 0] = s.tables }
        if tables != self.tables {
            self.tables = tables
            fitTables()
        }
        snapSoon()
    }

    /// The same lines, within a quarter of a point.
    private static func same(_ a: [LiftSegment], _ b: [LiftSegment]) -> Bool {
        func near(_ x: CGFloat, _ y: CGFloat) -> Bool { abs(x - y) <= 0.25 }
        func near(_ x: ClosedRange<CGFloat>, _ y: ClosedRange<CGFloat>) -> Bool { near(x.lowerBound, y.lowerBound) && near(x.upperBound, y.upperBound) }
        return a.count == b.count && zip(a, b).allSatisfy { x, y in
            x.part == y.part && x.texts == y.texts && x.tables == y.tables
                && x.rows.count == y.rows.count && zip(x.rows, y.rows).allSatisfy(near)
                && x.folds.count == y.folds.count && zip(x.folds, y.folds).allSatisfy(near)
                && (x.box == nil) == (y.box == nil) && (x.box.flatMap { a in y.box.map { near(a, $0) } } ?? true)
        }
    }

    /// Its tables as large as the copy draws them (`tables`), in place: a part that comes into view brings its tables'.
    private func fitTables() {
        guard storage.length > 0, laidOut > 0 else { return }
        var seen: [Int: Int] = [:]
        storage.beginEditing()
        storage.enumerateAttribute(.attachment, in: NSRange(location: 0, length: storage.length)) { value, range, _ in
            guard let a = value as? NSTextAttachment, storage.attribute(.replySource, at: range.location, effectiveRange: nil) != nil else { return }
            let part = storage.attribute(.replyPart, at: range.location, effectiveRange: nil) as? Int ?? 0
            let at = seen[part, default: 0]
            seen[part] = at + 1
            guard let sizes = tables[part], at < sizes.count, sizes[at].width > 0, sizes[at].height > 0 else { return }
            let room = ReplyText.room(scrolled: sizes[at], width: laidOut)
            guard abs(a.bounds.width - room.width) > 0.5 || abs(a.bounds.height - room.height) > 0.5 else { return }
            a.bounds = CGRect(origin: .zero, size: room)
            storage.edited(.editedAttributes, range: range, changeInLength: 0)
        }
        storage.endEditing()
    }

    /// Its lines go on the chat's in its next layout (`snapPending`).
    private func snapSoon() {
        snapPending = true
        setNeedsLayout()
    }

    /// Lays the words out breaking where the chat's do (`LiftLayout.follow`), then again with each line on the chat's
    /// line for it (`LiftLayout.snap`). An answer drawn in parts, part by part: the lines of each part laid out go on
    /// the chat's in it, from its first; those of a part not laid out have no lines of the chat's to go on yet, and stay
    /// out of sight (hidden, between the parts around it) until it is (#5291).
    private func resnap() {
        let all = NSRange(location: 0, length: storage.length)
        snapper.targets = [:]
        snapper.hidden = []
        snapper.shifts = [:]
        let split = !parts.isEmpty
        // A line TextKit 1 laid out once and now breaks elsewhere can keep a stale end, its last glyph's box at the
        // line's right edge, zero wide (docs/lessons/ios.md): when the chat's lines come after a layout, all of the
        // glyphs are made again, in no more time than the layout takes.
        if followChat() {
            layout.invalidateGlyphs(forCharacterRange: all, changeInLength: 0, actualCharacterRange: nil)
        }
        layout.invalidateLayout(forCharacterRange: all, actualCharacterRange: nil)
        guard segments.contains(where: { !$0.rows.isEmpty }), storage.length > 0 else { return }
        layout.ensureLayout(for: textContainer)
        let text = storage.string as NSString
        // Each line with words: its first glyph, its part, and what it is.
        var found: [(start: Int, part: Int, line: LiftLayout.SnapLine)] = []
        var codes: Set<Int> = []
        var shifts: [Int: CGFloat] = [:]
        layout.enumerateLineFragments(forGlyphRange: layout.glyphRange(for: textContainer)) { rect, _, _, range, _ in
            let chars = self.layout.characterRange(forGlyphRange: range, actualGlyphRange: nil)
            let s = text.substring(with: chars)
            guard s.contains(where: { !$0.isWhitespace }) else { return }
            shifts[range.location] = self.wrappedStart(rect, range, chars.location, text) ?? self.narrowedBracket(range, chars.location, text)
            let block = self.storage.attribute(.replyBlock, at: chars.location, effectiveRange: nil) as? ReplyBlock
            let id = block.map { ObjectIdentifier($0).hashValue }
            // A code block's first line, under its title bar: on the chat's next line, wherever the bar put it.
            let fresh = block?.kind == .code && id.map { codes.insert($0).inserted } == true
            let part = split ? self.storage.attribute(.replyPart, at: chars.location, effectiveRange: nil) as? Int ?? 0 : 0
            found.append((range.location, part, LiftLayout.SnapLine(baseline: rect.minY + self.layout.location(forGlyphAt: range.location).y,
                                                                    block: s.contains("\u{FFFC}"), quote: block?.kind == .quote ? id : nil,
                                                                    fresh: fresh)))
        }
        let reach = UIFont.preferredFont(forTextStyle: .body, compatibleWith: traitCollection).pointSize * 0.6
        var targets: [Int: CGFloat] = [:], hidden: Set<Int> = []
        for s in segments where !s.rows.isEmpty {
            let mine = split ? found.filter { $0.part == s.part } : found
            guard !mine.isEmpty else { continue }
            // A part's words start on its first line, wherever the parts before them ended up.
            let from = split ? mine.first(where: { !$0.line.block }).map { s.rows[0] - $0.line.baseline } ?? 0 : 0
            let snapped = LiftLayout.snap(mine.map(\.line), to: s.rows, folds: s.folds, reach: reach, moved: from)
            for (l, at) in zip(mine, snapped) {
                targets[l.start] = at.at
                if at.hidden { hidden.insert(l.start) }
            }
        }
        if split {
            let boxes = segments.compactMap { s in s.part.flatMap { p in s.box.map { (part: p, box: $0) } } }
            for l in found where !boxes.contains(where: { $0.part == l.part }) {
                let before = boxes.last(where: { $0.part < l.part })?.box.upperBound ?? 0
                let after = boxes.first(where: { $0.part > l.part })?.box.lowerBound ?? max(inside.height, before)
                targets[l.start] = (before + after) / 2
                hidden.insert(l.start)
            }
        }
        snapper.targets = targets
        snapper.shifts = shifts
        snapper.hidden = hidden
        layout.invalidateLayout(forCharacterRange: all, actualCharacterRange: nil)
        layout.ensureLayout(for: textContainer)
        setNeedsDisplay()
    }

    /// Where its lines break, as the chat's do (`LiftLayout.follow`), in all of the words (`text`, by default its own)
    /// or in each part laid out: set before its words are first laid out (`render`). true: they changed.
    @discardableResult private func followChat(in text: NSAttributedString? = nil) -> Bool {
        let text = text ?? storage
        let all = NSRange(location: 0, length: text.length)
        let split = !parts.isEmpty
        let paragraphs = Self.wrapping(in: text)
        var held = IndexSet(), breaks = IndexSet()
        func range(of s: LiftSegment) -> NSRange? {
            guard let p = s.part else { return all }
            return split ? partRanges[p] : nil
        }
        for s in segments {
            guard let r = range(of: s) else { continue }
            let mine = paragraphs.filter { $0.location >= r.location && NSMaxRange($0) <= NSMaxRange(r) }
            let f = LiftLayout.follow(s.texts, paragraphs: mine)
            held.formUnion(f.held)
            breaks.formUnion(f.at)
        }
        guard held != snapper.held || breaks != snapper.breaks else { return false }
        (snapper.held, snapper.breaks) = (held, breaks)
        return true
    }

    /// How far back a wrapped line goes to start at its paragraph's indent, as the chat's does; nil: it does. Laid out
    /// in Chinese (the app's language), TextKit 1 starts the line after one that ends in a closing bracket (」, ）) half
    /// an em in, and one that starts with an opening bracket a little; SwiftUI (and TextKit 2) at the indent.
    private func wrappedStart(_ rect: CGRect, _ glyphs: NSRange, _ first: Int, _ text: NSString) -> CGFloat? {
        guard first > 0, first < text.length,
              let before = UnicodeScalar(text.character(at: first - 1)), !CharacterSet.newlines.contains(before) else { return nil }
        let indent = (storage.attribute(.paragraphStyle, at: first, effectiveRange: nil) as? NSParagraphStyle)?.headIndent ?? 0
        let size = (storage.attribute(.font, at: first, effectiveRange: nil) as? UIFont)?.pointSize ?? 0
        let past = rect.minX + layout.location(forGlyphAt: glyphs.location).x - indent
        return past > 0.5 && past <= size * 0.6 ? -past : nil
    }

    /// How far right a line that starts with a CJK opening bracket goes to put its words after the bracket where the
    /// chat's are; nil: it starts with none, or it is not narrower. Laid out in Chinese, TextKit 1 draws that bracket
    /// narrower than the chat does (6.1 pt of its 8.6 at 17 pt), the rest of the line that much to the left: a selection
    /// there was off the words, and its end handle short of the last one (#5982). Moved, the bracket starts that much in,
    /// the rest of the line is on the chat's.
    private func narrowedBracket(_ glyphs: NSRange, _ first: Int, _ text: NSString) -> CGFloat? {
        guard glyphs.length > 1, first < text.length, let c = UnicodeScalar(text.character(at: first)), LiftLayout.opensCJK(c),
              layout.characterIndexForGlyph(at: glyphs.location) == first,
              let font = storage.attribute(.font, at: first, effectiveRange: nil) as? UIFont else { return nil }
        var glyph = layout.cgGlyph(at: glyphs.location), advance = CGSize.zero
        CTFontGetAdvancesForGlyphs(font as CTFont, .horizontal, &glyph, &advance, 1)
        let lost = advance.width - (layout.location(forGlyphAt: glyphs.location + 1).x - layout.location(forGlyphAt: glyphs.location).x)
        return lost > 0.5 && lost < font.pointSize * 0.6 ? lost : nil
    }

    /// The paragraphs of `words` that wrap as words, a list item's past its marker: not a code block's lines (which do
    /// not wrap), a table's picture or a rule.
    private static func wrapping(in words: NSAttributedString) -> [NSRange] {
        let text = words.string as NSString
        var out: [NSRange] = []
        text.enumerateSubstrings(in: NSRange(location: 0, length: text.length), options: [.byParagraphs, .substringNotRequired]) { _, r, _, _ in
            guard r.length > 0 else { return }
            if let b = words.attribute(.replyBlock, at: r.location, effectiveRange: nil) as? ReplyBlock, b.kind != .quote { return }
            if text.range(of: "\u{FFFC}", range: r).location != NSNotFound { return }
            var marker = NSRange()
            if words.attribute(.replyMarker, at: r.location, longestEffectiveRange: &marker, in: r) != nil {
                if NSMaxRange(marker) < NSMaxRange(r) { out.append(NSRange(location: NSMaxRange(marker), length: NSMaxRange(r) - NSMaxRange(marker))) }
            } else {
                out.append(r)
            }
        }
        return out
    }

    /// Where the words under it wrap: at the width they were offered, or (none known) at their own.
    private var wrapWidth: CGFloat {
        if let w = place?.offer?.width, w.isFinite, w > 0 { return w }
        return inside.width
    }

    /// Its bounds inside its inset: where the words are.
    private var inside: CGRect { bounds.inset(by: textContainerInset) }

    override func layoutSubviews() {
        guard !wordsHeld else { return super.layoutSubviews() }
        let w = wrapWidth
        if w > 0, abs(w - laidOut) > 0.5, !picked { render(width: w) }
        if keepsAllLaidOut() { snapPending = true }
        if snapPending, !snapHeld {
            snapPending = false
            resnap()
        }
        super.layoutSubviews()
        if keepsAllLaidOut() {
            if snapHeld { snapPending = true } else { resnap() }
        }
        #if DEBUG
        if LiftLines.on { LiftLines.shared.select = LiftLines.lines(of: self, layout: layout) }
        publishPicked()
        #endif
    }

    /// Its words all laid out, however tall: a text view that does not scroll fits its container to its own height as
    /// its frame is set, and its last line (the line spacing under it past the words' frame) went. true: it had.
    private func keepsAllLaidOut() -> Bool {
        guard laidOut > 0, textContainer.size.height < .greatestFiniteMagnitude / 2 || abs(textContainer.size.width - laidOut) > 0.5 else {
            return false
        }
        textContainer.size = CGSize(width: laidOut, height: .greatestFiniteMagnitude)
        return true
    }

    /// The place between two characters nearest a point, on the lines as they show (`LineSnap`): in the last line that
    /// starts at or above it (the first, above them all), the nearest place, its end before its line break. A folded
    /// quote's hidden lines are not where they lie. The system's lookup missed the moved lines: a second long press
    /// picked a word above the one pressed, a handle dragged onto the line under a table ended before the table (#6099).
    /// A moved line can start inside the one before it (a table picture's, taller here than the chat's table), where the
    /// layout manager's lookup takes the earlier line too.
    override func closestPosition(to point: CGPoint) -> UITextPosition? {
        guard let place = place(nearest: point) else { return super.closestPosition(to: point) }
        return position(from: beginningOfDocument, offset: place.offset)
    }

    /// `closestPosition(to:)`'s place, and the side of it the point is on: `before` when the point is on the character
    /// before it (left of the place, not in front of its line's first word) or past its line's last word.
    private func place(nearest point: CGPoint) -> (offset: Int, before: Bool)? {
        guard storage.length > 0, let container = layout.textContainers.first else { return nil }
        let p = CGPoint(x: point.x - textContainerInset.left, y: point.y - textContainerInset.top)
        // Not the first line holding the point: the lines are in order, but one can start above the end of the one before.
        var line: (rect: CGRect, used: CGRect, glyphs: NSRange)?
        layout.enumerateLineFragments(forGlyphRange: layout.glyphRange(for: container)) { rect, used, _, glyphs, _ in
            guard !self.snapper.hidden.contains(glyphs.location), line == nil || rect.minY <= p.y else { return }
            line = (rect, used, glyphs)
        }
        guard let line else { return nil }
        let text = storage.string as NSString
        let chars = layout.characterRange(forGlyphRange: line.glyphs, actualGlyphRange: nil)
        var end = NSMaxRange(chars)
        if end > chars.location, UnicodeScalar(text.character(at: end - 1)).map(CharacterSet.newlines.contains) == true { end -= 1 }
        var best = chars.location, bestX = CGFloat.infinity, distance = CGFloat.infinity, at = chars.location
        while at <= end {
            let x = at < end ? line.rect.minX + layout.location(forGlyphAt: layout.glyphIndexForCharacter(at: at)).x : line.used.maxX
            if abs(x - p.x) < distance { (best, bestX, distance) = (at, x, abs(x - p.x)) }
            at = at < end ? min(NSMaxRange(text.rangeOfComposedCharacterSequence(at: at)), end) : end + 1
        }
        return (best, best == end ? best > chars.location : best > chars.location && bestX > p.x)
    }

    override func closestPosition(to point: CGPoint, within range: UITextRange) -> UITextPosition? {
        guard let at = closestPosition(to: point) else { return nil }
        if compare(at, to: range.start) == .orderedAscending { return range.start }
        return compare(at, to: range.end) == .orderedDescending ? range.end : at
    }

    /// The selection's highlight, the same on every line: a line it covers to its end reaches across the whole width,
    /// a table's line and the last line as much as a paragraph's (the system's stopped at a table's edge and at the
    /// last word). Where the selection starts or ends inside a line, the highlight does too; the end handle stays after
    /// the last selected word.
    override func selectionRects(for range: UITextRange) -> [UITextSelectionRect] {
        let start = offset(from: beginningOfDocument, to: range.start), end = offset(from: beginningOfDocument, to: range.end)
        let text = storage.string as NSString
        guard end > start, end <= text.length, let container = layout.textContainers.first else { return super.selectionRects(for: range) }
        let inset = textContainerInset, right = inset.left + min(container.size.width, inside.width)
        var out: [LineSelectionRect] = []
        let glyphs = layout.glyphRange(forCharacterRange: NSRange(location: start, length: end - start), actualCharacterRange: nil)
        layout.enumerateLineFragments(forGlyphRange: glyphs) { line, used, _, lineGlyphs, _ in
            guard !self.snapper.hidden.contains(lineGlyphs.location) else { return } // a folded quote's, past its fold
            let chars = self.layout.characterRange(forGlyphRange: lineGlyphs, actualGlyphRange: nil)
            let lineEnd = NSMaxRange(chars)
            let from = max(chars.location, start), to = min(lineEnd, end)
            guard to > from else { return }
            // Where the words of this line end (before its line break), and where a character of it starts.
            let wordsEnd = lineEnd > chars.location && text.character(at: lineEnd - 1) == 0x0A ? lineEnd - 1 : lineEnd
            func x(_ at: Int) -> CGFloat {
                guard at < wordsEnd else { return inset.left + used.maxX }
                return inset.left + line.minX + self.layout.location(forGlyphAt: self.layout.glyphIndexForCharacter(at: at)).x
            }
            let top = inset.top + line.minY, height = line.height
            let left = from == chars.location ? inset.left : x(from)
            func add(_ a: CGFloat, _ b: CGFloat, start: Bool, end: Bool) {
                out.append(LineSelectionRect(CGRect(x: a, y: top, width: max(0, b - a), height: height), start: start, end: end))
            }
            if to < end {
                add(left, right, start: from == start, end: false) // it goes on below: the line to its end
            } else if to == lineEnd {
                // it ends with the line: across the whole width, the end handle after the line's last word
                let at = x(to)
                add(left, at, start: from == start, end: true)
                if at < right { add(at, right, start: false, end: false) }
            } else {
                add(left, x(to), start: from == start, end: true)
            }
        }
        // A selection starting or ending in a folded quote's hidden lines: its handle on the nearest line that shows.
        if let first = out.first, !out.contains(where: \.containsStart) { out[0] = first.marked(start: true) }
        if let last = out.last, !out.contains(where: \.containsEnd) { out[out.count - 1] = last.marked(end: true) }
        return out.isEmpty ? super.selectionRects(for: range) : out
    }

    /// Selects the word at the press (or everything) once the view is in its window, and shows the edit menu: a step
    /// a frame (`steps`).
    func begin(_ select: InPlaceSelect) {
        guard begun != select.id else { return }
        begun = select.id
        picked = false
        start(select.at, tries: 10)
    }

    private func start(_ at: CGPoint?, tries: Int) {
        var tries = tries
        var range: UITextRange?
        steps.run { [weak self] in
            guard let self else { return false }
            guard window != nil, inside.width > 0 else {
                tries -= 1
                return tries > 0
            }
            if wordsHeld { // its words
                wordsHeld = false
                snapHeld = true
                render(width: wrapWidth)
                return true
            }
            if snapHeld { // their lines on the chat's
                snapHeld = false
                setNeedsLayout()
                layoutIfNeeded()
                return true
            }
            guard let picking = range else { // the selection
                layoutIfNeeded()
                guard let r = at.flatMap(word(at:)) ?? textRange(from: beginningOfDocument, to: endOfDocument) else { return false }
                _ = becomeFirstResponder()
                selectedTextRange = r
                picked = !r.isEmpty
                range = r
                return true
            }
            showMenu(for: picking) // the menu
            return false
        }
    }

    /// The word under a point in the window; where there is none (a space, a mark), the character there. The word on
    /// the point's side of the nearest place: a press on the right half of a word's last character found the place
    /// after it and took the next word, which Chinese has with no space between (#6552).
    private func word(at point: CGPoint) -> UITextRange? {
        let local = convert(point, from: nil), near = place(nearest: local)
        guard let pos = near.flatMap({ position(from: beginningOfDocument, offset: $0.offset) }) ?? closestPosition(to: local) else {
            return nil
        }
        for direction in near?.before == true ? [UITextStorageDirection.backward, .forward] : [.forward, .backward] {
            if let w = tokenizer.rangeEnclosingPosition(pos, with: .word, inDirection: UITextDirection(rawValue: direction.rawValue)) {
                return w
            }
        }
        guard let next = position(from: pos, offset: 1) else { return nil }
        return textRange(from: pos, to: next)
    }

    private func showMenu(for range: UITextRange) {
        guard let box = menuTarget(range) else { return }
        menuInteraction.presentEditMenu(with: UIEditMenuConfiguration(identifier: nil, sourcePoint: CGPoint(x: box.midX, y: box.minY)))
    }

    func editMenuInteraction(_ interaction: UIEditMenuInteraction, menuFor configuration: UIEditMenuConfiguration,
                             suggestedActions: [UIMenuElement]) -> UIMenu? {
        textView(self, editMenuForTextIn: selectedRange, suggestedActions: suggestedActions)
    }

    /// The menu goes above or below the selection, never over it (#6005). Given only a point (its top's middle), the
    /// system had put it under that point, over the selected words and their end handle.
    func editMenuInteraction(_ interaction: UIEditMenuInteraction, targetRectFor configuration: UIEditMenuConfiguration) -> CGRect {
        selectedTextRange.flatMap(menuTarget) ?? CGRect(origin: configuration.sourcePoint, size: .zero)
    }

    /// What the menu stays off: the selected words that show, with room for their handles' knobs (the start's over
    /// the first line, the end's under the last).
    private func menuTarget(_ range: UITextRange) -> CGRect? {
        let rects = selectionRects(for: range).map(\.rect).filter { !$0.isEmpty && $0.width > 0 }
        guard let first = rects.first else { return nil }
        let box = rects.dropFirst().reduce(first) { $0.union($1) }.insetBy(dx: 0, dy: -Theme.Lift.handleReach)
        let shown = box.intersection(shownBounds)
        return shown.isNull || shown.isEmpty ? nil : shown
    }

    /// The part of it the lifted message shows: inside its scroll view, on screen.
    private var shownBounds: CGRect {
        var shown = bounds
        var v = superview
        while let view = v, !(view is UIScrollView) { v = view.superview }
        if let s = v { shown = shown.intersection(convert(s.bounds, from: s)) }
        if let w = window { shown = shown.intersection(convert(w.bounds, from: w)) }
        return shown
    }

    func textViewDidChangeSelection(_ textView: UITextView) {
        #if DEBUG
        publishPicked()
        #endif
        if let touched, draggedEnd == nil, selectedRange.length > 0, selectedRange != touched {
            draggedEnd = selectedRange.location != touched.location ? .start : .end
        }
        if selectedRange.length > 0 {
            picked = true
        } else if picked {
            picked = false
            ended()
        }
    }

    // MARK: Scrolling on at an edge (#3188)

    // A text view that scrolls itself scrolls on while a selection handle is held at its edge. This one does not (the
    // lifted message around it does, `MessageLiftLayer`), and UIKit leaves that scroll view alone: so it is scrolled
    // here, and the dragged end of the selection follows the words under the finger as they move.

    private func follow(_ at: CGPoint?) {
        if finger == nil {
            touched = selectedRange
            down = at
            // Since iOS 27 a dragged handle moves the selection only when the finger lifts (#5982): no change tells it.
            draggedEnd = at.flatMap(handle(at:))
        }
        finger = at
        if at == nil {
            down = nil
            touched = nil
            draggedEnd = nil
        }
        if edgeSpeed() == 0 {
            edgeLink?.invalidate()
            edgeLink = nil
        } else if edgeLink == nil {
            let link = CADisplayLink(target: self, selector: #selector(edgeTick(_:)))
            link.add(to: .main, forMode: .common)
            edgeLink = link
        }
    }

    /// The lifted message's scroll view, the first one around this view, when it has more than it shows.
    private var scroller: UIScrollView? {
        var v = superview
        while let view = v, !(view is UIScrollView) { v = view.superview }
        guard let s = v as? UIScrollView, s.isScrollEnabled, s.contentSize.height > s.bounds.height + 1 else { return nil }
        return s
    }

    /// The end of the selection whose handle is at a point in the window (`LiftLayout.handle`).
    private func handle(at point: CGPoint) -> LiftLayout.End? {
        guard selectedRange.length > 0, let range = selectedTextRange else { return nil }
        let rects = selectionRects(for: range)
        return LiftLayout.handle(at: convert(point, from: nil), start: rects.first(where: \.containsStart)?.rect,
                                 end: rects.last(where: \.containsEnd)?.rect, reach: Theme.Size.tapTarget)
    }

    /// Points a second the message scrolls on, for a handle held where the finger is (`LiftLayout.edgeScroll`), once it
    /// has moved from where it went down.
    private func edgeSpeed() -> CGFloat {
        guard draggedEnd != nil, let finger, let s = scroller else { return 0 }
        if let down, !LiftLayout.moved(finger, from: down) { return 0 }
        return LiftLayout.edgeScroll(at: s.convert(finger, from: nil).y - s.bounds.minY, height: s.bounds.height,
                                     band: Theme.Lift.edgeScroll, speed: Theme.Lift.edgeScrollSpeed)
    }

    @objc private func edgeTick(_ link: CADisplayLink) {
        let speed = edgeSpeed()
        guard speed != 0, let s = scroller, let finger else { return follow(finger) }
        let inset = s.adjustedContentInset
        let lowest = -inset.top, highest = max(lowest, s.contentSize.height + inset.bottom - s.bounds.height)
        let y = min(max(s.contentOffset.y + speed * (link.targetTimestamp - link.timestamp), lowest), highest)
        guard y != s.contentOffset.y else { return }
        s.contentOffset.y = y
        extendSelection(to: finger)
    }

    /// The dragged end of the selection to the words under the finger; the ends never cross.
    private func extendSelection(to finger: CGPoint) {
        guard let draggedEnd, let r = selectedTextRange, let at = closestPosition(to: convert(finger, from: nil)) else { return }
        let next: UITextRange? = switch draggedEnd {
        case .start: compare(at, to: r.end) == .orderedAscending ? textRange(from: at, to: r.end) : nil
        case .end: compare(at, to: r.start) == .orderedDescending ? textRange(from: r.start, to: at) : nil
        }
        guard let next, compare(next.start, to: r.start) != .orderedSame || compare(next.end, to: r.end) != .orderedSame else { return }
        selectedTextRange = next
    }

    override func willMove(toWindow newWindow: UIWindow?) {
        super.willMove(toWindow: newWindow)
        if newWindow == nil { follow(nil) }
    }

    func textView(_ textView: UITextView, editMenuForTextIn range: NSRange, suggestedActions: [UIMenuElement]) -> UIMenu? {
        let picked = ReplyText.copyText(attributedText.attributedSubstring(from: range)).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !picked.isEmpty, let quote else { return UIMenu(children: suggestedActions) }
        let ours = UIMenu(options: .displayInline, children: [
            UIAction(title: String(localized: "Quote"), image: UIImage(systemName: Theme.Icon.quote)) { _ in quote(picked) },
        ])
        return UIMenu(children: [ours] + suggestedActions)
    }

    /// The words as the chat shows them; a table in it as its Markdown.
    override func copy(_ sender: Any?) {
        guard selectedRange.length > 0 else { return super.copy(sender) }
        UIPasteboard.general.string = ReplyText.copyText(attributedText.attributedSubstring(from: selectedRange))
        Haptics.success()
        copied()
    }
}

#if DEBUG
extension LiftUITextView {
    /// The selection's lines and its handles, in the window (`LiftLines.picked`), once they have settled.
    func publishPicked() {
        guard LiftLines.on else { return }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.4) { [weak self] in
            guard let self else { return }
            var lines: [CGRect] = [], handles: [CGRect] = []
            if let r = selectedTextRange, !r.isEmpty {
                lines = selectionRects(for: r).map(\.rect).filter { !$0.isEmpty && $0.width > 0 }.map { convert($0, to: nil) }
                func walk(_ v: UIView) {
                    for s in v.subviews {
                        if s is UITextSelectionHandleView, !s.isHidden, s.alpha > 0.01 { handles.append(s.convert(s.bounds, to: nil)) }
                        walk(s)
                    }
                }
                walk(window ?? self)
            }
            let shown = convert(shownBounds, to: nil)
            func code(_ rs: [CGRect]) -> String {
                rs.map { String(format: "%.1f,%.1f,%.1f,%.1f", $0.minX, $0.minY, $0.width, $0.height) }.joined(separator: ";")
            }
            let s = lines.isEmpty ? "" : "lines=\(code(lines))/handles=\(code(handles))/shown=\(code([shown]))"
            if LiftLines.shared.picked != s { LiftLines.shared.picked = s }
        }
    }
}

extension LiftLines {
    /// The text view's lines (`LiftUITextView`), as the chat's are written: a picture counts as a glyph.
    static func lines(of v: UITextView, layout: NSLayoutManager) -> String {
        guard on else { return "" }
        let c = v.textContainer, inset = v.textContainerInset
        let edge = v.convert(CGPoint(x: v.bounds.maxX - inset.right, y: 0), to: nil).x
        if shared.edge != edge { shared.edge = edge }
        layout.ensureLayout(for: c)
        var out: [Line] = []
        let text = v.textStorage
        let hidden = (layout.delegate as? LineSnap)?.hidden ?? []
        layout.enumerateLineFragments(forGlyphRange: layout.glyphRange(for: c)) { rect, _, _, range, _ in
            guard !hidden.contains(range.location) else { return } // a folded quote's, past its fold
            var first = -1, last = -1, word = -1, n = 0
            for g in range.location..<NSMaxRange(range) {
                let kind = layout.propertyForGlyph(at: g)
                if kind.contains(.controlCharacter) || kind.contains(.null) { continue }
                let at = layout.characterIndexForGlyph(at: g)
                if at < text.length, text.attribute(.attachment, at: at, effectiveRange: nil) != nil { return } // a table: not a line of words
                if at < text.length, text.attribute(.replyMarker, at: at, effectiveRange: nil) != nil { continue }
                if first < 0 { first = g }
                last = g
                if !kind.contains(.elastic) { word = g }
                n += 1
            }
            guard first >= 0 else { return }
            let at = layout.location(forGlyphAt: first)
            func right(_ g: Int) -> CGFloat { layout.boundingRect(forGlyphRange: NSRange(location: g, length: 1), in: c).maxX }
            let p = v.convert(CGPoint(x: inset.left + rect.minX + at.x, y: inset.top + rect.minY + at.y), to: nil)
            // A code line longer than the box runs on under its edge, as the chat's does.
            let x1 = min(v.convert(CGPoint(x: inset.left + right(last), y: 0), to: nil).x, edge)
            let w1 = word >= 0 ? min(v.convert(CGPoint(x: inset.left + right(word), y: 0), to: nil).x, edge) : x1
            let end = text.attributedSubstring(from: NSRange(location: layout.characterIndexForGlyph(at: last), length: 1)).string.unicodeScalars.first
            let start = text.attributedSubstring(from: NSRange(location: layout.characterIndexForGlyph(at: first), length: 1)).string.unicodeScalars.first
            out.append((n, p.x, p.y, x1, w1, end.map(Self.closesCJK) ?? false, start.map(LiftLayout.opensCJK) ?? false))
        }
        return encode(out)
    }
}
#endif

extension NSAttributedString.Key {
    /// A list item's marker and the tab after it (not words of its line).
    nonisolated static let replyMarker = NSAttributedString.Key("PersonaReplyMarker")
    /// What `ReplyLayout` draws behind a run of lines (a `ReplyBlock`).
    nonisolated static let replyBlock = NSAttributedString.Key("PersonaReplyBlock")
    /// What a picture in the words copies as (a table's Markdown).
    nonisolated static let replySource = NSAttributedString.Key("PersonaReplySource")
    /// Which part of an answer drawn in parts a line of its words is in (`AnswerParts.Part`), 0 for one drawn whole.
    nonisolated static let replyPart = NSAttributedString.Key("PersonaReplyPart")
}

/// Behind a run of lines: a code block's background, a quote's bar, a rule. One object per block, so a block's lines
/// are one run of the attribute.
final class ReplyBlock: NSObject {
    enum Kind { case code, quote, rule }
    let kind: Kind
    /// How far its background reaches above and below its lines.
    let pad: CGFloat

    init(_ kind: Kind, pad: CGFloat = 0) {
        self.kind = kind
        self.pad = pad
    }

    func draw(lines: CGRect, x: CGFloat, width: CGFloat) {
        switch kind {
        case .code:
            UIColor(Theme.codeBackground).setFill()
            let box = CGRect(x: x, y: lines.minY - pad, width: width, height: lines.height + 2 * pad)
            UIBezierPath(roundedRect: box, cornerRadius: Theme.Radius.control).fill()
        case .quote: // as the chat's quote card (#2984, `ReplyQuote`): the accent bar on a faint accent card
            let card = UIBezierPath(roundedRect: CGRect(x: x, y: lines.minY, width: width, height: lines.height), cornerRadius: Theme.Radius.badge)
            UIColor(Theme.quoteBackground).setFill()
            card.fill()
            UIGraphicsGetCurrentContext()?.saveGState()
            card.addClip()
            UIColor(Theme.accent).setFill()
            UIRectFill(CGRect(x: x, y: lines.minY, width: Theme.Size.bar, height: lines.height))
            UIGraphicsGetCurrentContext()?.restoreGState()
        case .rule:
            UIColor.separator.setFill()
            UIRectFill(CGRect(x: x, y: lines.midY, width: width, height: Theme.Size.hairline))
        }
    }
}

/// Lays out the words with TextKit 1; where their blocks (`ReplyBlock`) are is read from it.
nonisolated final class ReplyLayout: NSLayoutManager {
    /// Each block with the lines it spans, in the text container's coordinates.
    @MainActor func blocks() -> [(ReplyBlock, CGRect)] {
        guard let storage = textStorage else { return [] }
        var out: [(ReplyBlock, CGRect)] = []
        storage.enumerateAttribute(.replyBlock, in: NSRange(location: 0, length: storage.length)) { value, range, _ in
            guard let block = value as? ReplyBlock else { return }
            var lines = CGRect.null
            enumerateLineFragments(forGlyphRange: glyphRange(forCharacterRange: range, actualCharacterRange: nil)) { _, used, _, _, _ in
                lines = lines.union(used)
            }
            guard !lines.isNull else { return }
            if let i = out.firstIndex(where: { $0.0 === block }) { out[i].1 = out[i].1.union(lines) } else { out.append((block, lines)) }
        }
        return out
    }
}

/// What is behind the words' blocks (a code block's background, a quote's bar, a rule), under the text view's
/// selection highlight (`LiftUITextView.behind`).
final class ReplyBlocksView: UIView {
    weak var layout: ReplyLayout?

    override func draw(_ rect: CGRect) {
        guard let layout, let container = layout.textContainers.first else { return }
        for (block, lines) in layout.blocks() { block.draw(lines: lines, x: 0, width: container.size.width) }
    }
}

/// A line of the selection's highlight (`LiftUITextView.selectionRects(for:)`).
/// Puts each line of a text view where `targets` says its baseline goes (by the line's first glyph); the rest as laid out.
nonisolated final class LineSnap: NSObject, NSLayoutManagerDelegate {
    var targets: [Int: CGFloat] = [:]
    /// Lines (by their first glyph) of a folded quote past its fold: put at the card's bottom, they get no highlight
    /// and are not measured (`LiftUITextView.selectionRects`, `LiftLines`); copied, they are there.
    var hidden: Set<Int> = []
    /// Where the chat's lines break (`LiftLayout.follow`): in `held`, a line breaks only before a character in `breaks`.
    var held = IndexSet()
    var breaks = IndexSet()
    /// Wrapped lines (by their first glyph) TextKit started past their paragraph's indent, and how far back they go
    /// (`LiftUITextView.wrappedStart`).
    var shifts: [Int: CGFloat] = [:]

    func layoutManager(_ layoutManager: NSLayoutManager, shouldBreakLineByWordBeforeCharacterAt charIndex: Int) -> Bool {
        !held.contains(charIndex) || breaks.contains(charIndex)
    }

    func layoutManager(_ layoutManager: NSLayoutManager, shouldSetLineFragmentRect lineFragmentRect: UnsafeMutablePointer<CGRect>,
                       lineFragmentUsedRect: UnsafeMutablePointer<CGRect>, baselineOffset: UnsafeMutablePointer<CGFloat>,
                       in textContainer: NSTextContainer, forGlyphRange glyphRange: NSRange) -> Bool {
        let dx = shifts[glyphRange.location] ?? 0
        let dy = targets[glyphRange.location].map { $0 - (lineFragmentRect.pointee.origin.y + baselineOffset.pointee) } ?? 0
        guard abs(dx) > 0.01 || abs(dy) > 0.01 else { return false }
        lineFragmentRect.pointee.origin.x += dx
        lineFragmentRect.pointee.origin.y += dy
        lineFragmentUsedRect.pointee.origin.x += dx
        lineFragmentUsedRect.pointee.origin.y += dy
        return true
    }
}

final class LineSelectionRect: UITextSelectionRect {
    private let box: CGRect
    private let start: Bool
    private let end: Bool

    init(_ box: CGRect, start: Bool, end: Bool) {
        self.box = box
        self.start = start
        self.end = end
    }

    /// The same, holding the selection's start or end too.
    func marked(start: Bool = false, end: Bool = false) -> LineSelectionRect {
        LineSelectionRect(box, start: self.start || start, end: self.end || end)
    }

    override var rect: CGRect { box }
    override var writingDirection: NSWritingDirection { .leftToRight }
    override var containsStart: Bool { start }
    override var containsEnd: Bool { end }
    override var isVertical: Bool { false }
}

/// A reply's Markdown laid out as the chat draws it (`MarkdownUI.Theme.persona`, `ReplyMetrics`), for selecting: what
/// the chat shows, not its marks. Paragraphs keep their lines (a single line break is one, as in the chat), headings,
/// list markers, inline code and links keep their look, code blocks and quotes their background and bar, tables are
/// the chat's own table as a picture (copied as Markdown). Formulas, diagrams and cards stay as written.
enum ReplyText {
    /// The fonts and colors of the words where they were.
    struct Look {
        var body: UIFont
        var code: UIFont
        var inlineCode: UIFont
        /// A quote's room on the right for its mark (`ReplyQuote.markRoom`, scaled as a caption).
        var quoteMarkRoom: CGFloat
        var color: UIColor
        var link: UIColor
        /// The fourth to sixth heading levels' ink (`ReplyMetrics.heading4`).
        var secondaryInk: UIColor
        var traits: UITraitCollection
        /// The style's design (Clay: rounded), as the chat draws its words: not code's.
        var design: UIFontDescriptor.SystemDesign

        init(_ words: LiftWords, design: UIFontDescriptor.SystemDesign = .default, traits: UITraitCollection) {
            self.design = design
            var base = Self.designed(UIFont.preferredFont(forTextStyle: .body, compatibleWith: traits), design)
            var quiet = false
            switch words {
            case .plain(let c): color = UIColor(c) // your bubble: the body style
            case .reply(let q):
                quiet = q
                color = q ? .secondaryLabel : .label
                base = base.withSize(Self.replySize(traits)) // MarkdownUI's size, not the body style's (#6552)
            }
            body = quiet ? base.withSize(ReplyMetrics.points(ReplyMetrics.quiet, of: base.pointSize)) : base
            code = .monospacedSystemFont(ofSize: ReplyMetrics.points(Theme.Level.code.size / Theme.Level.body.size, of: body.pointSize), weight: .regular)
            inlineCode = .monospacedSystemFont(ofSize: ReplyMetrics.points(ReplyMetrics.inlineCode, of: body.pointSize), weight: .regular)
            quoteMarkRoom = UIFontMetrics(forTextStyle: .caption1).scaledValue(for: Theme.Space.xl, compatibleWith: traits)
            link = UIColor(Theme.accent)
            secondaryInk = UIColor(Theme.Reply.secondaryInk)
            self.traits = traits
        }

        var size: CGFloat { body.pointSize }

        /// The size of a reply's words in the chat at these traits: MarkdownUI scales the theme's size as `@ScaledMetric`
        /// relative to body does (`UIFontMetrics`), and draws it at a whole point. That is not the body text style's
        /// size past the default: 22 pt where body is 23 at xxxLarge, 48 where it is 53 at AX5. A text view in the body
        /// style wrapped the words onto more lines than the chat, and a press near the end of a long answer picked words
        /// lines above it (#6552).
        static func replySize(_ traits: UITraitCollection) -> CGFloat {
            UIFontMetrics(forTextStyle: .body).scaledValue(for: Theme.Level.body.size * Theme.Font.scale, compatibleWith: traits).rounded()
        }

        /// `font` at weight `w`, in the words' design (code stays monospaced).
        func weight(_ font: UIFont, _ w: UIFont.Weight) -> UIFont {
            font.fontDescriptor.symbolicTraits.contains(.traitMonoSpace)
                ? .monospacedSystemFont(ofSize: font.pointSize, weight: w) : Self.designed(.systemFont(ofSize: font.pointSize, weight: w), design)
        }

        static func designed(_ font: UIFont, _ design: UIFontDescriptor.SystemDesign) -> UIFont {
            guard design != .default, let d = font.fontDescriptor.withDesign(design) else { return font }
            return UIFont(descriptor: d, size: font.pointSize)
        }
    }

    /// The words clear, for a text view over the chat's own (`LiftUITextView`): no color, no background, a picture's room only.
    static func clear(_ words: NSAttributedString) -> NSAttributedString {
        let out = NSMutableAttributedString(attributedString: words)
        let all = NSRange(location: 0, length: out.length)
        out.removeAttribute(.backgroundColor, range: all)
        out.addAttributes([.foregroundColor: UIColor.clear, .underlineColor: UIColor.clear, .strikethroughColor: UIColor.clear], range: all)
        out.enumerateAttribute(.attachment, in: all) { value, _, _ in
            guard let a = value as? NSTextAttachment, a.bounds.width > 0, a.bounds.height > 0 else { return }
            a.image = blank // the attachment's bounds keep its room
        }
        return out
    }

    /// Your own message, as its bubble shows it: no Markdown.
    static func plain(_ text: String, look: Look) -> NSAttributedString {
        let style = NSMutableParagraphStyle()
        style.lineSpacing = look.size * ReplyMetrics.lineSpacing
        return NSAttributedString(string: text, attributes: [.font: look.body, .foregroundColor: look.color, .paragraphStyle: style])
    }

    /// A reply laid out for `width` (what a table's picture is drawn at). parts: the lines its parts start on, for an
    /// answer drawn in parts (`AnswerParts.Part.line`): each line of the words says its part (`replyPart`).
    /// tables: its tables' sizes as the lifted copy draws them (`LiftTables`), by part, in order: each is left that much
    /// room, not drawn again; one whose part is not laid out gets about the room it takes (`tableRoom`). nil: each
    /// table is drawn as a picture.
    static func attributed(_ markdown: String, look: Look, width: CGFloat, tables: [Int: [CGSize]]? = nil,
                           parts: [Int] = []) -> NSAttributedString {
        let b = Builder(look: look, width: width, tables: tables, parts: parts)
        b.read(markdown)
        return b.finish()
    }

    /// A table's picture's room in the words, drawn that large: no wider than the words, scaled down to them.
    static func room(_ drawn: CGSize, width: CGFloat) -> CGSize {
        drawn.width > width && width > 0 ? CGSize(width: width, height: drawn.height * width / drawn.width) : drawn
    }

    /// A table's room in the words, the copy drawing it that large: one wider than the words scrolls sideways in them,
    /// as tall as it is.
    static func room(scrolled drawn: CGSize, width: CGFloat) -> CGSize {
        CGSize(width: width > 0 ? min(drawn.width, width) : drawn.width, height: drawn.height)
    }

    /// About the room a table of `rows` lines (its header's too) takes, before it is laid out: the words' width, a
    /// line and its cell's padding a row.
    static func tableRoom(rows: Int, look: Look, width: CGFloat) -> CGSize {
        CGSize(width: width, height: CGFloat(max(rows, 1)) * (look.body.lineHeight + 2 * Theme.Space.sm))
    }

    /// A picture's room in the words that are clear (`clear`): drawn as large as the room, it draws nothing.
    static let blank = UIGraphicsImageRenderer(size: CGSize(width: 1, height: 1)).image { _ in }

    /// The text of a part of the words, a picture in it as what it copies as.
    static func copyText(_ part: NSAttributedString) -> String {
        var out = ""
        part.enumerateAttribute(.replySource, in: NSRange(location: 0, length: part.length)) { value, range, _ in
            out += (value as? String) ?? (part.string as NSString).substring(with: range)
        }
        return out
    }

    /// A block's spacing: above and below it (the larger of two neighbours' wins, as in MarkdownUI), and inside its
    /// background.
    private struct Block {
        var top: CGFloat = 0
        var bottom: CGFloat = ReplyMetrics.paragraphBottom
        var pad: CGFloat = 0
        /// More room over its first line inside its background: a code block's title bar.
        var head: CGFloat = 0
    }

    private struct Line {
        var text: NSMutableAttributedString
        /// Its block; nil for a blank line between blocks.
        var block: Int?
        var style = NSMutableParagraphStyle()
        /// The first line of a list item (the space above items).
        var item = false
        /// What its line break carries when it has no text of its own (an empty line of code: its font and background).
        var ending: [NSAttributedString.Key: Any]?
        /// The answer's part it is in (`replyPart`).
        var part = 0
    }

    private enum Open {
        case paragraph, list, quote(ReplyBlock), code(ReplyBlock, fence: Character?, count: Int), math(close: String)
    }

    private final class Builder {
        let look: Look
        let width: CGFloat
        /// The tables' sizes as the copy draws them, by part, in order; nil: each drawn as a picture (`ReplyTable`).
        let tables: [Int: [CGSize]]?
        /// The lines the answer's parts start on; empty: one part.
        let parts: [Int]
        /// The part being read, the next one's index, and how many tables each part had so far.
        private var part = 0
        private var nextPart = 1
        private var tablesRead: [Int: Int] = [:]
        var lines: [Line] = []
        var blocks: [Block] = []
        var open: Open?
        /// The open list's items: the indent of each level's marker and where its text starts.
        var levels: [(marker: Int, text: CGFloat)] = []
        var afterBlank = false

        init(look: Look, width: CGFloat, tables: [Int: [CGSize]]? = nil, parts: [Int] = []) {
            self.look = look
            self.width = width
            self.tables = tables
            self.parts = parts
        }

        private var markerWidth: CGFloat { ReplyMetrics.markerRoom(of: look.size) }
        private var markerGap: CGFloat { ReplyMetrics.markerGap } // the words where the chat's lists put them (#6552)

        func read(_ markdown: String) {
            // Bold next to CJK punctuation is read as the chat reads it (`CJKEmphasis`, line for line); a table still
            // copies as it was written.
            let written = markdown.components(separatedBy: "\n")
            let source = CJKEmphasis.apply(markdown).components(separatedBy: "\n")
            var i = 0
            while i < source.count {
                while nextPart < parts.count, i >= parts[nextPart] {
                    part = nextPart
                    nextPart += 1
                }
                let raw = source[i]
                i += 1
                let trimmed = raw.trimmingCharacters(in: .whitespaces)
                let indent = Self.indent(raw)
                let deep = indent >= 4
                switch open {
                case .code(let block, let fence?, let count):
                    if indent < 4, trimmed.count >= count, trimmed.allSatisfy({ $0 == fence }) {
                        open = nil
                    } else {
                        code(raw, block)
                    }
                    continue
                case .math(let close):
                    add(verbatim(raw, look.body), in: blocks.count - 1, lineSpacing: true)
                    if trimmed == close { open = nil }
                    continue
                default: break
                }
                if trimmed.isEmpty {
                    blank()
                    continue
                }
                defer { afterBlank = false }
                if case .code(let block, nil, _) = open {
                    if deep { code(String(raw.dropFirst(min(indent, 4))), block); continue }
                    open = nil
                }
                if !deep, let f = Self.fence(trimmed) {
                    let block = ReplyBlock(.code, pad: Theme.Space.m)
                    blocks.append(Block(top: ReplyMetrics.codeTop, bottom: ReplyMetrics.codeBottom, pad: Theme.Space.m, head: ReplyMetrics.codeBar))
                    open = .code(block, fence: f.0, count: f.1)
                    levels = []
                    continue
                }
                if trimmed == "$$" || trimmed == "\\[" {
                    blocks.append(Block())
                    open = .math(close: trimmed == "$$" ? "$$" : "\\]")
                    add(verbatim(raw, look.body), in: blocks.count - 1, lineSpacing: true)
                    continue
                }
                let inList: Bool = if case .list = open { true } else { false }
                // Four spaces in, after a blank line and not in a list: code, as written.
                if deep, !inList, open == nil || afterBlank {
                    let block = ReplyBlock(.code, pad: Theme.Space.m)
                    blocks.append(Block(top: ReplyMetrics.codeTop, bottom: ReplyMetrics.codeBottom, pad: Theme.Space.m, head: ReplyMetrics.codeBar))
                    open = .code(block, fence: nil, count: 0)
                    code(String(raw.dropFirst(min(indent, 4))), block)
                    continue
                }
                if !deep, trimmed.contains("|"), i < source.count, Self.isDelimiterRow(source[i]) {
                    let first = i - 1
                    var rows = [raw, source[i]]
                    i += 1
                    while i < source.count, source[i].contains("|"), !source[i].trimmingCharacters(in: .whitespaces).isEmpty {
                        rows.append(source[i])
                        i += 1
                    }
                    let drawn = rows.joined(separator: "\n")
                    table(drawn, copied: written.count == source.count ? written[first..<i].joined(separator: "\n") : drawn)
                    continue
                }
                if !deep, Self.isRule(trimmed) {
                    blocks.append(Block(top: Theme.Space.m, bottom: Theme.Space.m))
                    let line = verbatim(trimmed, look.body)
                    line.addAttributes([.foregroundColor: UIColor.clear, .replyBlock: ReplyBlock(.rule)], range: NSRange(location: 0, length: line.length))
                    add(line, in: blocks.count - 1)
                    open = nil
                    levels = []
                    continue
                }
                if !deep, let (level, title) = Self.heading(trimmed) {
                    let m: (top: CGFloat, bottom: CGFloat) = level == 1 ? (top: ReplyMetrics.heading1.top, bottom: ReplyMetrics.heading1.bottom)
                        : level == 2 ? ReplyMetrics.heading2 : level == 3 ? ReplyMetrics.heading3
                        : (top: ReplyMetrics.heading4.top, bottom: ReplyMetrics.heading4.bottom)
                    blocks.append(Block(top: m.top, bottom: m.bottom))
                    let font: UIFont = level == 1 ? look.weight(look.body.withSize(ReplyMetrics.points(ReplyMetrics.heading1.size, of: look.size)), .bold)
                        : level <= 3 ? look.weight(look.body, .semibold)
                        : look.weight(look.body.withSize(ReplyMetrics.points(ReplyMetrics.heading4.size, of: look.size)), .semibold)
                    add(inline(title, font: font, color: level > 3 ? look.secondaryInk : nil), in: blocks.count - 1)
                    open = nil
                    levels = []
                    continue
                }
                if !deep, trimmed.hasPrefix(">") {
                    var rest = trimmed.dropFirst()
                    if rest.first == " " { rest = rest.dropFirst() }
                    let block: ReplyBlock
                    if case .quote(let b) = open { block = b } else {
                        block = ReplyBlock(.quote)
                        blocks.append(Block(pad: Theme.Space.s))
                        open = .quote(block)
                        levels = []
                    }
                    // As the chat's quote card (`ReplyQuote`): after its bar, clear of its quote mark on the right.
                    let line = inline(String(rest), font: look.body, color: .secondaryLabel)
                    line.addAttribute(.replyBlock, value: block, range: NSRange(location: 0, length: line.length))
                    let lead = Theme.Size.bar + Theme.Space.m, mark = look.quoteMarkRoom
                    add(line, in: blocks.count - 1, lineSpacing: true) { s in
                        s.firstLineHeadIndent = lead
                        s.headIndent = lead
                        s.tailIndent = -mark
                    }
                    continue
                }
                if let (marker, text) = Self.item(trimmed), inList || !deep {
                    item(marker: marker, text: text, indent: indent)
                    continue
                }
                if inList, !afterBlank || indent > (levels.last?.marker ?? 0) {
                    // More of the item above: under its words.
                    let at = levels.last?.text ?? 0
                    add(inline(trimmed, font: look.body), in: blocks.count - 1, lineSpacing: true) { s in
                        s.firstLineHeadIndent = at
                        s.headIndent = at
                    }
                    continue
                }
                if case .paragraph = open, !afterBlank {} else {
                    blocks.append(Block())
                    open = .paragraph
                    levels = []
                }
                add(inline(trimmed, font: look.body), in: blocks.count - 1, lineSpacing: true)
            }
        }

        private func blank() {
            switch open {
            case .code(let block, _?, _):
                code("", block)
                return
            case .list:
                break // a blank line inside a list: the list goes on if an item or an indented line follows
            case .code(_, nil, _):
                break // indented code goes on after a blank line while lines stay indented
            default:
                open = nil
            }
            afterBlank = true
            lines.append(Line(text: NSMutableAttributedString(string: ""), block: nil, part: part))
        }

        private func code(_ text: String, _ block: ReplyBlock) {
            let attrs: [NSAttributedString.Key: Any] = [.font: look.code, .foregroundColor: look.color, .replyBlock: block]
            add(NSAttributedString(string: text, attributes: attrs), in: blocks.count - 1) { s in
                s.lineSpacing = self.look.code.pointSize * ReplyMetrics.codeLineSpacing
                s.lineBreakMode = .byClipping // the chat's code scrolls sideways, its lines whole
                s.firstLineHeadIndent = Theme.Space.m
                s.headIndent = Theme.Space.m
                // No room on the right: the box's padding scrolls with the code, so a long line runs on to its edge.
            }
            lines[lines.count - 1].ending = attrs
        }

        private func item(marker: String, text: String, indent: Int) {
            if case .list = open {
                while let last = levels.last, indent < last.marker, levels.count > 1 { levels.removeLast() }
                if let last = levels.last, indent > last.marker + 1 {
                    levels.append((indent, last.text + markerWidth + markerGap))
                } else if levels.isEmpty {
                    levels = [(indent, markerWidth + markerGap)]
                }
            } else {
                blocks.append(Block())
                open = .list
                levels = [(indent, markerWidth + markerGap)]
            }
            let level = levels.count - 1
            let start = levels[level].text - markerWidth - markerGap
            let shown: String
            if marker.first?.isNumber == true {
                shown = marker
            } else {
                shown = ["•", "◦", "▪"][min(level, 2)]
            }
            let markerSize = (shown as NSString).size(withAttributes: [.font: look.body]).width
            let line = NSMutableAttributedString(string: shown + "\t", attributes: [.font: look.body, .foregroundColor: look.color, .replyMarker: true])
            line.append(inline(text, font: look.body))
            let textAt = levels[level].text
            add(line, in: blocks.count - 1, lineSpacing: true, item: true) { s in
                s.firstLineHeadIndent = max(start + self.markerWidth - markerSize, start)
                s.headIndent = textAt
                s.tabStops = [NSTextTab(textAlignment: .left, location: textAt)]
                s.defaultTabInterval = textAt
            }
        }

        private func table(_ source: String, copied: String) {
            blocks.append(Block())
            open = nil
            levels = []
            let at = tablesRead[part, default: 0]
            tablesRead[part] = at + 1
            let a = NSTextAttachment()
            let room: CGSize
            if let tables {
                // Its room only, as large as the copy draws it (about, its part not laid out): the words over it are
                // clear (`ReplyText.clear`).
                a.image = ReplyText.blank
                if let sizes = tables[part], at < sizes.count, sizes[at].width > 0, sizes[at].height > 0 {
                    room = ReplyText.room(scrolled: sizes[at], width: width)
                } else {
                    room = ReplyText.tableRoom(rows: source.components(separatedBy: "\n").count - 1, look: look, width: width)
                }
            } else {
                guard let image = ReplyTable.picture(source, width: width, traits: look.traits) else {
                    add(verbatim(copied, look.body), in: blocks.count - 1, lineSpacing: true)
                    return
                }
                a.image = image
                room = ReplyText.room(image.size, width: width)
            }
            a.bounds = CGRect(origin: .zero, size: room)
            let line = NSMutableAttributedString(attachment: a)
            line.addAttributes([.replySource: copied, .font: look.body], range: NSRange(location: 0, length: line.length))
            add(line, in: blocks.count - 1)
        }

        private func add(_ text: NSAttributedString, in block: Int, lineSpacing: Bool = false, item: Bool = false,
                         _ configure: (NSMutableParagraphStyle) -> Void = { _ in }) {
            var line = Line(text: NSMutableAttributedString(attributedString: text), block: block, item: item, part: part)
            if lineSpacing { line.style.lineSpacing = look.size * ReplyMetrics.lineSpacing }
            configure(line.style)
            lines.append(line)
        }

        private func verbatim(_ s: String, _ font: UIFont) -> NSMutableAttributedString {
            NSMutableAttributedString(string: s, attributes: [.font: font, .foregroundColor: look.color])
        }

        /// The lines joined, each with its spacing: inside a block its own (list items 0.2 em apart), between two blocks
        /// the larger of their margins plus what each keeps inside its background.
        func finish() -> NSAttributedString {
            let real = lines.indices.filter { lines[$0].block != nil }
            for (k, i) in real.enumerated() {
                let line = lines[i], b = blocks[line.block!]
                if k == 0 { line.style.paragraphSpacingBefore = b.pad + b.head }
                guard k + 1 < real.count else {
                    line.style.paragraphSpacing = b.pad
                    continue
                }
                let next = lines[real[k + 1]], nb = blocks[next.block!]
                if next.block == line.block {
                    line.style.paragraphSpacing = next.item ? look.size * ReplyMetrics.itemTop : 0
                } else {
                    let gap = max(b.bottom, nb.top) + b.pad + nb.pad + nb.head
                    line.style.paragraphSpacing = max(0, gap - line.style.lineSpacing)
                }
            }
            // A blank line between blocks takes no room: the spacing above is the blocks'. It stays a line break, so a
            // copied part keeps its paragraphs apart.
            let none = NSMutableParagraphStyle()
            none.minimumLineHeight = 0.01
            none.maximumLineHeight = 0.01
            let tiny = UIFont.systemFont(ofSize: 0.01)
            let out = NSMutableAttributedString()
            for (n, line) in lines.enumerated() {
                let text = line.text
                if line.block == nil {
                    text.setAttributes([.font: tiny, .paragraphStyle: none], range: NSRange(location: 0, length: text.length))
                }
                if n < lines.count - 1 {
                    var attrs = line.ending ?? (text.length > 0 ? text.attributes(at: text.length - 1, effectiveRange: nil)
                        : (line.block == nil ? [.font: tiny] : [.font: look.body]))
                    attrs[.attachment] = nil // a table's line break is not more of its picture
                    attrs[.replySource] = nil
                    text.append(NSAttributedString(string: "\n", attributes: attrs))
                }
                let style = line.block == nil ? none : line.style
                text.addAttributes([.paragraphStyle: style, .replyPart: line.part], range: NSRange(location: 0, length: text.length))
                out.append(text)
            }
            // Blank lines at the start or the end of the reply take no part.
            while out.length > 0, out.string.hasSuffix("\n") { out.deleteCharacters(in: NSRange(location: out.length - 1, length: 1)) }
            return out
        }

        // MARK: Inline

        /// Code spans (left to the Markdown parser) and formulas (kept as written) in a line.
        private static let spans = try! NSRegularExpression(pattern: #"(`+).+?\1|\$\$.+?\$\$|\$[^\s$](?:[^$\n]*?[^\s$])?\$|\\\(.+?\\\)"#)

        /// Bold, italics, inline code, strikethrough and links of one line; formulas as written.
        func inline(_ line: String, font: UIFont, color: UIColor? = nil) -> NSMutableAttributedString {
            let color = color ?? look.color
            let out = NSMutableAttributedString()
            let ns = line as NSString
            var pending = "", at = 0
            for m in Self.spans.matches(in: line, range: NSRange(location: 0, length: ns.length)) {
                pending += ns.substring(with: NSRange(location: at, length: m.range.location - at))
                let span = ns.substring(with: m.range)
                at = m.range.location + m.range.length
                if span.hasPrefix("`") { pending += span; continue }
                out.append(markdown(pending, font: font, color: color))
                pending = ""
                out.append(NSAttributedString(string: span, attributes: [.font: font, .foregroundColor: color]))
            }
            pending += ns.substring(from: at)
            out.append(markdown(pending, font: font, color: color))
            return out
        }

        private func markdown(_ text: String, font: UIFont, color: UIColor) -> NSAttributedString {
            guard !text.isEmpty else { return NSAttributedString() }
            guard let parsed = try? AttributedString(markdown: text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)) else {
                return NSAttributedString(string: text, attributes: [.font: font, .foregroundColor: color])
            }
            let out = NSMutableAttributedString()
            for run in parsed.runs {
                let intent = run.inlinePresentationIntent ?? []
                var f = font
                var attrs: [NSAttributedString.Key: Any] = [.foregroundColor: color]
                if intent.contains(.code) {
                    f = look.inlineCode.withSize(ReplyMetrics.points(ReplyMetrics.inlineCode, of: font.pointSize))
                    attrs[.backgroundColor] = UIColor(Theme.inlineCodeBackground)
                }
                if intent.contains(.stronglyEmphasized) { f = look.weight(f, .semibold) }
                if intent.contains(.emphasized), let d = f.fontDescriptor.withSymbolicTraits(f.fontDescriptor.symbolicTraits.union(.traitItalic)) {
                    f = UIFont(descriptor: d, size: 0)
                }
                if intent.contains(.strikethrough) { attrs[.strikethroughStyle] = NSUnderlineStyle.single.rawValue }
                if run.link != nil { attrs[.foregroundColor] = look.link } // not followed: a tap here is about the selection
                attrs[.font] = f
                out.append(NSAttributedString(string: String(parsed[run.range].characters), attributes: attrs))
            }
            return out
        }

        // MARK: Lines

        /// Leading spaces, a tab counting as four.
        static func indent(_ line: String) -> Int {
            var n = 0
            for c in line {
                if c == " " { n += 1 } else if c == "\t" { n += 4 } else { break }
            }
            return n
        }

        /// An opening fence: three or more backticks or tildes (a backtick fence's info string has no backtick).
        static func fence(_ trimmed: String) -> (Character, Int)? {
            guard let c = trimmed.first, c == "`" || c == "~" else { return nil }
            let n = trimmed.prefix { $0 == c }.count
            guard n >= 3 else { return nil }
            if c == "`", trimmed.dropFirst(n).contains("`") { return nil }
            return (c, n)
        }

        /// `---`, `* * *`, `___`: a rule, not a list item.
        static func isRule(_ trimmed: String) -> Bool {
            let marks = trimmed.filter { $0 != " " && $0 != "\t" }
            guard let c = marks.first, "-*_".contains(c), marks.count >= 3 else { return false }
            return marks.allSatisfy { $0 == c }
        }

        /// `## Title`: its level and words.
        static func heading(_ trimmed: String) -> (Int, String)? {
            let hashes = trimmed.prefix { $0 == "#" }.count
            guard (1...6).contains(hashes) else { return nil }
            let rest = trimmed.dropFirst(hashes)
            guard rest.isEmpty || rest.first == " " || rest.first == "\t" else { return nil }
            return (hashes, rest.trimmingCharacters(in: .whitespaces))
        }

        /// `- words`, `* words`, `+ words`, `5. words`, `5) words`: the marker and the words.
        static func item(_ trimmed: String) -> (String, String)? {
            if let c = trimmed.first, "-*+".contains(c), trimmed.dropFirst().first == " " {
                return (String(c), String(trimmed.dropFirst(2)))
            }
            let digits = trimmed.prefix { $0.isASCII && $0.isNumber }
            guard (1...9).contains(digits.count) else { return nil }
            let rest = trimmed.dropFirst(digits.count)
            guard let mark = rest.first, mark == "." || mark == ")", rest.dropFirst().first == " " else { return nil }
            return (String(trimmed.prefix(digits.count + 1)), String(rest.dropFirst(2)))
        }

        /// A table's delimiter row: `| --- | :-: |`.
        static func isDelimiterRow(_ line: String) -> Bool {
            let t = line.trimmingCharacters(in: .whitespaces)
            guard t.contains("|"), t.contains("-") else { return false }
            return t.allSatisfy { "|-: \t".contains($0) }
        }

    }
}

/// A Markdown table drawn as the chat draws it (`FittedTable` at the width it gets), for the words being selected.
@MainActor enum ReplyTable {
    static func picture(_ source: String, width: CGFloat, traits: UITraitCollection) -> UIImage? {
        guard width > 0 else { return nil }
        let fontSize = UIFont.preferredFont(forTextStyle: .body, compatibleWith: traits).pointSize * ReplyMetrics.inlineCode
        let m = MarkdownTableText.measure(source, fontSize: fontSize)
        let n = CGFloat(m.natural.count)
        let plan = TablePlan.fit(natural: m.natural, minimum: m.minimum,
                                 available: width - n * (FittedTable.cellPadding + FittedTable.border) - FittedTable.border * 3)
        // The table's style inside the theme: the nearer one wins, and the theme's own table scrolls (a renderer draws no
        // scroll views at all).
        let table = Markdown(source)
            .markdownBlockStyle(\.table) { config in
                FittedTable.drawn(config.label)
                    .environment(\.tableColumnWidths, plan.widths)
                    .environment(\.tableColumnAlignments, m.alignments)
            }
            .markdownTheme(.persona)
            // At the columns' planned widths: no clear space beside a narrow table (it would be selected as part of it);
            // one wider than the words is scaled down to them where it is attached.
            .fixedSize()
            .environment(\.colorScheme, traits.userInterfaceStyle == .dark ? .dark : .light)
            .environment(\.dynamicTypeSize, DynamicTypeSize(traits.preferredContentSizeCategory) ?? .large)
        let r = ImageRenderer(content: table)
        r.proposedSize = .unspecified
        r.scale = traits.displayScale > 0 ? traits.displayScale : 3
        return r.uiImage
    }
}
#endif
