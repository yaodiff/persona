import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// MARK: - Your own long message, folded (#5676)

/// When your own message folds, as the web folds it (`BubbleText` in web/src/views/chat/SegmentView.tsx): past `limit`
/// lines it shows its first `shown`, the last one fading, with Show More at the bubble's bottom. At the accessibility
/// text sizes ten lines are taller than a phone's screen, so there it folds past nine lines to five (the web has no such
/// sizes). The lines are the ones drawn, at the bubble's width.
nonisolated enum BubbleFoldRule: Equatable, Sendable {
    case regular, large

    init(_ size: DynamicTypeSize) { self = size.isAccessibilitySize ? .large : .regular }

    /// Past this many lines it folds (the web's BUBBLE_MAX_LINES).
    var limit: Int { self == .regular ? 14 : 9 }
    /// Folded, this many show.
    var shown: Int { self == .regular ? 10 : 5 }

    /// Characters to a line of the narrowest bubble at the body's default size (wide CJK characters in an iPad's
    /// narrowest split), scaled down as the text grows: a message within `limit` lines even so cannot fold, and is drawn
    /// as before, never measured (the web's NARROWEST_CHARS).
    static let narrowest: CGFloat = 9
    /// The same, at a phone's bubble, for whether it folds before it is measured (the web's CHARS_PER_LINE).
    static let typical: CGFloat = 16
    /// The body's size at the default text size, which `narrowest` and `typical` are for.
    static let bodySize: CGFloat = 17
    /// Folded, a long message lays out only its start: its first `limit + 2` lines as they wrap at the bubble's width
    /// (`start(of:lines:ems:)`), this many characters at most. A start that does not run past `limit` lines after all is
    /// drawn whole instead (`Folding`).
    static let startCharacters = 3000
    /// A character's width in ems, for where the start's lines wrap: CJK, kana, hangul, full-width forms and emoji take
    /// an em; letters, digits and spaces less than any of them does on average, so the start errs long.
    static func ems(_ c: Character) -> CGFloat { (c.unicodeScalars.first?.value ?? 0) >= 0x2E80 ? 1 : 0.4 }

    /// DEBUG `-showWholeBubbles YES`: every bubble whole, as before #5676 (before / after screenshots and measurements
    /// from one build).
    /// DEBUG `-showPlainBubbleOpen YES`: opening without the fade-in, as before #6481 (before / after recordings from one build).
    #if DEBUG
    static let whole = UserDefaults.standard.bool(forKey: "showWholeBubbles")
    static let plainOpen = UserDefaults.standard.bool(forKey: "showPlainBubbleOpen")
    #else
    static let whole = false
    static let plainOpen = false
    #endif

    /// Characters to a line, `base` at the default size, at `size` (the body's point size now).
    static func perLine(_ base: CGFloat, size: CGFloat) -> Int { max(1, Int((base * bodySize / max(size, 1)).rounded(.down))) }

    /// How many lines `lengths` (each line's characters) take at `perLine` characters a line.
    static func lines(_ lengths: [Int], perLine: Int) -> Int {
        lengths.reduce(0) { $0 + max(1, ($1 + perLine - 1) / perLine) }
    }

    #if !os(macOS)
    /// The height of `lines` lines of body text at `size`, as `.lineHeight(.body)` spaces them: the font's line height each,
    /// and the level's extra leading between them. From the font's metrics, not a hidden text measured in the bubble
    /// (its lines were read as the bubble's own by what reads a message's lines, `readingShownWords`).
    @MainActor static func height(lines: Int, size: DynamicTypeSize) -> CGFloat {
        if let h = heights[Key(lines: lines, size: size)] { return h }
        let traits = UITraitCollection(preferredContentSizeCategory: UIContentSizeCategory(size))
        let line = UIFont.preferredFont(forTextStyle: .body, compatibleWith: traits).lineHeight
        let spacing = Theme.Level.body.extraLeading * Theme.textScale(size)
        let h = (CGFloat(lines) * line + CGFloat(max(lines - 1, 0)) * spacing).rounded()
        heights[Key(lines: lines, size: size)] = h
        return h
    }
    private struct Key: Hashable { var lines: Int; var size: DynamicTypeSize }
    /// Twelve sizes, two rules.
    @MainActor private static var heights: [Key: CGFloat] = [:]
    #endif

    /// Each line's characters, once per text.
    static func lengths(_ text: String) -> [Int] {
        lengthsMemo(text) { $0.split(separator: "\n", omittingEmptySubsequences: false).map(\.count) }
    }
    private static let lengthsMemo = TextMemo<[Int]>(limit: 400)

    /// The width of a long bubble's words, as last laid out (`Folding`, from Show More's row, which spans it): where the
    /// next one's start wraps. nil until one is; that one's start is cut by its lines alone.
    @MainActor static var wordsWidth: CGFloat?

    /// The start a folded bubble draws under `rule`, `ems` to a line (nil: not known yet), once per text and width: a
    /// bubble that appears again as the chat scrolls does not walk its characters again.
    static func shownStart(of text: String, rule: BubbleFoldRule, ems: CGFloat?) -> String? {
        // Half ems, rounded up: a start a little long rather than short.
        let half = ems.map { Int(($0 * 2).rounded(.up)) }
        let key = "\(rule.limit)/\(half.map(String.init) ?? "-")/" + text
        return starts(key) { _ in start(of: text, lines: rule.limit + 2, ems: half.map { CGFloat($0) / 2 }) }
    }
    private static let starts = TextMemo<String?>(limit: 200)

    /// The start of `text` a folded bubble draws: its first `lines` lines, wrapped at `ems` to a line when given (by
    /// `ems(_:)`), and `startCharacters` at most; nil when that is all of it.
    static func start(of text: String, lines: Int, ems: CGFloat? = nil) -> String? {
        var count = 0
        var breaks = 0
        var x: CGFloat = 0
        var end = text.startIndex
        for i in text.indices {
            if count >= startCharacters { break }
            let c = text[i]
            if c == "\n" {
                breaks += 1
                x = 0
                if breaks >= lines { break }
            } else if let ems {
                let w = Self.ems(c)
                if x > 0, x + w > ems {
                    breaks += 1
                    x = 0
                    if breaks >= lines { break }
                }
                x += w
            }
            count += 1
            end = text.index(after: i)
        }
        return end == text.endIndex ? nil : String(text[..<end])
    }
}

/// What a bubble needs to fold its words: where its open state is kept (`OpenReports`, in the chat's scope) and the key
/// its row is pinned by (`chatPinned`).
struct BubbleFoldKey {
    var id: String
    /// The row's `chatPinned` key: opening and folding keep the bubble's top where it is on screen.
    var pin: String { "bubble-" + id }

    /// Where its open state is kept (`OpenReports`), in the chat's `scope`.
    func openKey(_ scope: String) -> String { scope + "/" + pin }

    /// The bubbles on screen folded now, by `openKey` (`Folding` keeps it): what a long press asks as it begins. Not
    /// SwiftUI state: a bubble that folds as it appears redraws nothing around it (a `@State` in the row redrew the whole
    /// row once more for every long message scrolled in).
    @MainActor static var folded: Set<String> = []
    /// Their reveal state, by `openKey`: a long press opens a bubble with the same fade-in as Show More.
    @MainActor static var unfolds: [String: Binding<Unfolding>] = [:]

    /// A long press on the bubble (`messageLongPress(opens:)`): folded, it opens, as Show More does, and takes the press.
    @MainActor func openOnPress(scope: String, pin chat: ChatPin?) -> Bool {
        let key = openKey(scope)
        guard Self.folded.contains(key) else { return false }
        Self.flip(key, pinKey: pin, open: false, pin: chat, unfold: Self.unfolds[key])
        return true
    }

    /// Opens or folds it in place (`toggleInPlace`), as Show More / Show Less do: its top stays where it is on screen,
    /// at the newest end too (no rise to the top bar, the owner's ask).
    @MainActor static func flip(_ key: String, pinKey: String, open: Bool, pin: ChatPin?, unfold: Binding<Unfolding>? = nil) {
        Haptics.tap()
        toggleInPlace(pin, pinKey, animation: Theme.Motion.state, unfold: unfold, closing: open, rises: false) { OpenReports.shared.set(key, !open) }
    }
}

/// The words of your own bubble, folded past the rule's lines (`BubbleFoldRule`) with Show More / Show Less under them,
/// in the bubble. A message that cannot run past the rule's lines even in the narrowest bubble is drawn as before, with
/// nothing measured; one that can is measured once its words are laid out (`onGeometryChange` on their size, which
/// scrolling does not change), not as it scrolls, and drawn again only when that changes what its characters said.
/// Folded, only its start is laid out (`BubbleFoldRule.start`: a few lines past the rule's, as they wrap). The lifted
/// copy (`liftCopy`) is always whole.
struct BubbleFold<Words: View>: View {
    var text: String
    var fold: BubbleFoldKey?
    /// The color of Show More: the bubble's link color (`UserBubbleInk.link`).
    var tint: Color
    @ViewBuilder var words: (String) -> Words

    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.liftCopy) private var lifted
    @ScaledMetric(relativeTo: .body) private var bodySize = BubbleFoldRule.bodySize

    var body: some View {
        #if os(macOS)
        words(text) // the Mac app is frozen (docs/apple/mac.md): its bubbles stay whole
        #else
        let rule = BubbleFoldRule(typeSize)
        let lengths = BubbleFoldRule.lengths(text)
        if let fold, !lifted, !BubbleFoldRule.whole,
           BubbleFoldRule.lines(lengths, perLine: BubbleFoldRule.perLine(BubbleFoldRule.narrowest, size: bodySize)) > rule.limit {
            let guess = BubbleFoldRule.lines(lengths, perLine: BubbleFoldRule.perLine(BubbleFoldRule.typical, size: bodySize)) > rule.limit
            Folding(text: text, fold: fold, rule: rule, cut: BubbleFoldRule.height(lines: rule.shown, size: typeSize), guess: guess,
                    ems: BubbleFoldRule.wordsWidth.map { $0 / bodySize }, tint: tint, words: words)
        } else if lifted {
            // The lifted copy is offered what the chat offered the row (`Offers`): a bubble opened since it was laid out
            // folded was offered its folded height, and its words were cut to "…" there (#6481). Its own height, always.
            words(text).fixedSize(horizontal: false, vertical: true)
        } else {
            words(text)
        }
        #endif
    }
}

/// A bubble's words that may run past the rule's lines: measured, and cut at `rule.shown` lines while folded.
private struct Folding<Words: View>: View {
    var text: String
    var fold: BubbleFoldKey
    var rule: BubbleFoldRule
    /// The height of `rule.shown` body lines: what a folded bubble shows.
    var cut: CGFloat
    /// Whether it folds by its characters, until it is measured.
    var guess: Bool
    /// Body ems to a line of the words (`BubbleFoldRule.wordsWidth`), where its start wraps; nil: not known yet.
    var ems: CGFloat?
    var tint: Color
    var words: (String) -> Words

    /// Measured past the rule's lines or not, where that differs from `guess`; nil: as guessed. Written only when the
    /// measuring changes the answer, so the usual long message is drawn once.
    @State private var measured: Bool?
    /// Its start reached no further than the rule's lines (narrow letters at a wide bubble): it is drawn whole.
    @State private var whole = false
    /// Just opened: what opens fades in under the lines already shown, as a tool's details do (`toggleInPlace`, `.unfolding`).
    @State private var unfold = Unfolding()
    @Environment(\.rowMemoScope) private var scope
    @Environment(\.chatPin) private var pin

    private var key: String { fold.openKey(scope) }
    private var long: Bool { measured ?? guess }
    /// Words this tall run past the rule's lines: that many lines and a half of `cut`'s.
    private var past: CGFloat { cut * (CGFloat(rule.limit) + 0.5) / CGFloat(rule.shown) }

    var body: some View {
        let open = OpenReports.shared.isOpen(key)
        let folded = long && !open
        let start = folded && !whole ? BubbleFoldRule.shownStart(of: text, rule: rule, ems: ems) : nil
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            words(start ?? text)
                .fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self) { $0.size.height.rounded() } action: { h in
                    if start != nil, h <= past {
                        whole = true // only its start was laid out, and it did not run past the rule's lines: all of it
                    } else if long != (h > past) {
                        measured = h > past
                    }
                }
                .frame(height: folded ? cut : nil, alignment: .top)
                .clipped()
                .contentShape(.rect) // what is folded away takes no touches (the words run on past the fold)
                .modifier(FoldFade(on: folded, revealing: unfold.revealing, cut: cut, line: cut / CGFloat(max(rule.shown, 1))))
                // Folded, one element as tall as what shows (its words all read out): the words' own frame runs on past
                // the fold, and VoiceOver's box with it.
                .accessibilityElement(children: folded ? .combine : .contain)
                .accessibilityAddTraits(folded ? .isStaticText : [])
            if long {
                FoldToggle(open: open, prefix: "bubble", tint: tint, showMore: true) {
                    BubbleFoldKey.flip(key, pinKey: fold.pin, open: open, pin: pin, unfold: $unfold)
                }
                // Its row spans the words' width: where the next long message's start wraps. Not state: nothing redraws.
                .onGeometryChange(for: CGFloat.self) { $0.size.width.rounded() } action: { w in
                    if w > 0 { BubbleFoldRule.wordsWidth = w }
                }
            }
        }
        // Only a bubble that can fold reports where it is as the chat scrolls (what opening and folding keep in place).
        .chatPinned(fold.pin)
        .onAppear { keep(folded); BubbleFoldKey.unfolds[key] = $unfold }
        .onChange(of: folded) { _, now in keep(now) }
        .onDisappear { BubbleFoldKey.folded.remove(key); BubbleFoldKey.unfolds[key] = nil }
    }

    /// What a long press asks (`BubbleFoldKey.folded`).
    private func keep(_ folded: Bool) {
        if folded { BubbleFoldKey.folded.insert(key) } else { BubbleFoldKey.folded.remove(key) }
    }
}

/// Folded, the words are opaque down to the last line shown, which fades out. Just opened, what was shown stays as it
/// was and the rest fades in over the space it takes (the same `Theme.Motion.reveal` as a tool's details), so the words
/// do not pop in whole. At rest, open, no mask at all: a mask is drawn off screen first, every frame the bubble moves.
private struct FoldFade: ViewModifier {
    var on: Bool
    var revealing: Bool
    /// The height of the lines shown folded.
    var cut: CGFloat
    /// One line's height: the fade.
    var line: CGFloat

    /// While `revealing`: false on its first frame, then true (animated): what was folded away starts clear.
    @State private var grown = false
    @Environment(\.accessibilityReduceMotion) private var still

    func body(content: Content) -> some View {
        if on {
            content.mask { fade }
        } else if revealing, !still, !BubbleFoldRule.plainOpen {
            content
                .mask {
                    ZStack(alignment: .top) {
                        Rectangle().opacity(grown ? 1 : 0)
                        fade.frame(height: cut, alignment: .top)
                    }
                }
                .onAppear { grow() }
                .onChange(of: revealing) { _, now in if now { grow() } else { grown = false } }
        } else {
            content
        }
    }

    private var fade: some View {
        VStack(spacing: 0) {
            Rectangle()
            LinearGradient(colors: [.black, .clear], startPoint: .top, endPoint: .bottom).frame(height: line)
        }
    }

    private func grow() {
        withAnimation(Theme.Motion.reveal) { grown = true }
    }
}
