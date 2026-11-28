import MarkdownUI
import SwiftUI
#if os(iOS)
import UIKit
#endif

// MARK: - Where words can be tapped (docs/apple/ios.md "Word lookup (text_lookup)", #10977)

/// What a reading surface hands the words under it: the model, and the surface's name, sent as `source` when a word is
/// saved. Nil (the default) everywhere else: under a button, a row that opens something, an input or a code block the
/// words stay only words.
struct TextLookupScope: Equatable {
    let model: TextLookupModel
    let source: String

    static func == (a: Self, b: Self) -> Bool { a.model === b.model && a.source == b.source }
}

extension EnvironmentValues {
    @Entry var textLookup: TextLookupScope? = nil
}

extension View {
    /// Words under this view (drawn with the persona Markdown theme, or `lookUpWords(plain:)`) open a lookup card when
    /// tapped, while an enabled `text_lookup` extension answers. Put it on reading surfaces only, never on a row or a
    /// button whose tap opens something. `source` names the surface (chat, agent, inbox, todo, knowledge, card); an
    /// empty one turns lookup off below it.
    func textLookupSurface(_ source: String) -> some View { modifier(TextLookupSurface(source: source)) }

    /// A Markdown block's words (`MarkdownUI.Theme.persona`'s paragraphs and headings): tappable under a
    /// `textLookupSurface`, otherwise as they were.
    func lookUpWords(_ content: MarkdownContent) -> some View { modifier(LookUpWords(words: .markdown(content))) }

    /// A plain `Text`'s words, given as the string it draws.
    func lookUpWords(plain text: String) -> some View { modifier(LookUpWords(words: .plain(text))) }
}

private struct TextLookupSurface: ViewModifier {
    @Environment(AppModel.self) private var app: AppModel?
    let source: String

    func body(content: Content) -> some View {
        // Reads only `active`: the surface redraws when lookup turns on or off, not on every saved word.
        // One structure whatever the state: only the environment value changes. A branch here rebuilt the whole page
        // under it (a chat dropped its subscription) when lookup turned on after the first read.
        content.environment(\.textLookup, scope)
    }

    /// Nil turns lookup off below (an empty source: a picture of a card; or no extension answering).
    private var scope: TextLookupScope? {
        guard !source.isEmpty, let model = app?.textLookup, model.active else { return nil }
        return TextLookupScope(model: model, source: source)
    }
}

/// The words a block draws: Markdown (its plain text is what MarkdownUI's `Text` holds) or a plain string.
enum LookupSource {
    case markdown(MarkdownContent)
    case plain(String)

    /// The characters the block's `Text` draws, worked out on a tap, never in `body` (P4).
    var plainText: String {
        switch self {
        case .markdown(let content): content.renderPlainText()
        case .plain(let text): text
        }
    }

    /// Where the block's links and inline code are, in the plain text's UTF-16 offsets: a tap there is the link's
    /// (MarkdownUI's own handling) or code, never a lookup. nil when that cannot be told for sure (then nothing in the
    /// block is looked up where a link or code may be).
    func blocked(in plain: String) -> [NSRange]? {
        guard case .markdown(let content) = self else { return [] }
        let markdown = content.renderMarkdown()
        guard markdown.contains("[") || markdown.contains("`") || markdown.contains("<") || markdown.contains("](") else { return [] }
        guard let parsed = try? AttributedString(markdown: markdown, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)),
              String(parsed.characters).utf16.count == plain.utf16.count else { return nil }
        var out: [NSRange] = []
        for run in parsed.runs where run.link != nil || run.inlinePresentationIntent?.contains(.code) == true {
            out.append(NSRange(run.range, in: parsed))
        }
        return out
    }
}

#if os(iOS)
/// One block's words, tappable (#10977). Off a lookup surface, or lifted, it adds nothing at all. On one, it reads where
/// the block's `Text` laid out its glyphs (`Text.LayoutKey`, the real layout, no second one), and a quick tap (a
/// recognizer that never takes the touch from links, long presses, selection or scrolling) on a glyph of a word opens
/// the card over that word. Saved words are underlined when the extension asks for it.
private struct LookUpWords: ViewModifier {
    @Environment(\.textLookup) private var scope
    @Environment(\.liftCopy) private var lifted
    let words: LookupSource

    func body(content: Content) -> some View {
        if let scope, !lifted {
            content.modifier(LookUpTaps(scope: scope, words: words))
        } else {
            content
        }
    }
}

/// Where a block's glyphs are, kept for a tap; not observed (it changes with layout, and nothing draws from it).
@MainActor final class LookupGlyphs {
    var placed: [(origin: CGPoint, layout: Text.Layout)] = []
}

/// The saved words' places to underline, observed by the underline alone.
@Observable final class LookupUnderlines {
    var rects: [CGRect] = []
}

private struct LookUpTaps: ViewModifier {
    let scope: TextLookupScope
    let words: LookupSource
    @State private var glyphs = LookupGlyphs()
    @State private var underlines = LookupUnderlines()

    func body(content: Content) -> some View {
        content
            .backgroundPreferenceValue(Text.LayoutKey.self) { layouts in
                GeometryReader { geo in
                    // Only local positions are read here, so scrolling does not run this again (no global frame).
                    let placed = PlacedLayouts(layouts.map { (geo[$0.origin], $0.layout) })
                    Color.clear.onChange(of: placed, initial: true) { _, new in place(new) }
                }
            }
            .onChange(of: scope.model.underlines ? scope.model.savedRevision : -1) { _, _ in place(PlacedLayouts(glyphs.placed)) }
            .overlay(alignment: .topLeading) { LookupUnderlineView(underlines: underlines) }
            .gesture(LookupTap { tap(at: $0, window: $1) })
    }

    private func place(_ new: PlacedLayouts) {
        glyphs.placed = new.items
        let rects: [CGRect]
        if scope.model.underlines {
            let text = words.plainText
            let ranges = TextLookupWords.occurrences(of: scope.model.saved, in: text)
            rects = ranges.flatMap { LookupGeometry.lineRects(for: $0, in: new.items) }
        } else {
            rects = []
        }
        if rects != underlines.rects { underlines.rects = rects }
    }

    /// `point` in the block, `window` the same touch in the window (which places the card).
    private func tap(at point: CGPoint, window inWindow: CGPoint) {
        guard let index = LookupGeometry.characterIndex(at: point, in: glyphs.placed) else { return }
        let text = words.plainText
        let length = (text as NSString).length
        // The layout and the plain text must be the same characters; when they are not (a picture in the line), no
        // word is guessed.
        guard index < length, LookupGeometry.lastIndex(in: glyphs.placed).map({ $0 < length }) ?? false else { return }
        guard let found = TextLookupWords.word(in: text, at: index) else { return }
        guard let blocked = words.blocked(in: text) else { return }
        if blocked.contains(where: { NSIntersectionRange($0, found.range).length > 0 }) { return }
        let rect = LookupGeometry.lineRects(for: found.range, in: glyphs.placed).first ?? CGRect(origin: point, size: .zero)
        Haptics.selection()
        // A UIKit popover over the word: it grows with the card as the lookup loads (SwiftUI's kept the loading size).
        guard let window = TextLookupMenu.pageWindow() else { return }
        let card = TextLookupCard(model: scope.model, word: found.word, context: TextLookupWords.context(in: text, around: found.range),
                                  source: scope.source)
        TextLookupMenu.present(card, from: window, rect: rect.offsetBy(dx: inWindow.x - point.x, dy: inWindow.y - point.y))
    }
}

/// The laid-out texts of a block, compared to know when they moved.
struct PlacedLayouts: Equatable {
    let items: [(origin: CGPoint, layout: Text.Layout)]
    init(_ items: [(origin: CGPoint, layout: Text.Layout)]) { self.items = items }
    static func == (a: Self, b: Self) -> Bool {
        a.items.count == b.items.count && zip(a.items, b.items).allSatisfy { $0.origin == $1.origin && $0.layout == $1.layout }
    }
}

/// Glyph geometry over `Text.Layout`.
enum LookupGeometry {
    /// A character index as an offset into the text's characters (UTF-16, as the layout counts them).
    static func offset(_ i: Text.Layout.CharacterIndex) -> Int {
        // `CharacterIndex` is a frozen wrapper of one Int (its offset); there is no public accessor.
        unsafeBitCast(i, to: Int.self)
    }

    /// The character under `point`, if a glyph is there: the line whose box holds the point, then the glyph across it.
    static func characterIndex(at point: CGPoint, in placed: [(origin: CGPoint, layout: Text.Layout)]) -> Int? {
        for (origin, layout) in placed {
            for line in layout {
                let box = line.typographicBounds.rect.offsetBy(dx: origin.x, dy: origin.y)
                guard point.y >= box.minY, point.y <= box.maxY else { continue }
                for run in line {
                    for i in run.indices {
                        let glyph = run[i]
                        let r = glyph.typographicBounds.rect.offsetBy(dx: origin.x, dy: origin.y)
                        if point.x >= r.minX, point.x <= r.maxX, let c = glyph.characterIndices.first { return offset(c) }
                    }
                }
            }
        }
        return nil
    }

    /// The last character any glyph draws.
    static func lastIndex(in placed: [(origin: CGPoint, layout: Text.Layout)]) -> Int? {
        var last: Int?
        for (_, layout) in placed {
            for line in layout {
                for run in line {
                    if let m = run.characterIndices.max() { last = max(last ?? 0, offset(m)) }
                }
            }
        }
        return last
    }

    /// The boxes the characters of `range` take, one per line.
    static func lineRects(for range: NSRange, in placed: [(origin: CGPoint, layout: Text.Layout)]) -> [CGRect] {
        var out: [CGRect] = []
        for (origin, layout) in placed {
            for line in layout {
                var box: CGRect?
                for run in line {
                    for i in run.indices {
                        let glyph = run[i]
                        guard let c = glyph.characterIndices.first, NSLocationInRange(offset(c), range) else { continue }
                        let r = glyph.typographicBounds.rect.offsetBy(dx: origin.x, dy: origin.y)
                        box = box.map { $0.union(r) } ?? r
                    }
                }
                if let box { out.append(box) }
            }
        }
        return out
    }
}

/// A short tap that takes nothing from anyone (#10977): it recognizes alongside every other recognizer, never cancels
/// or delays their touches, and lets go of a touch held past `Theme.TextLookup.tapLimit` (a long press, a selection) or moved past
/// a few points (a scroll).
private struct LookupTap: UIGestureRecognizerRepresentable {
    /// Where the tap was in the view, and in the window.
    var action: (CGPoint, CGPoint) -> Void

    final class Recognizer: UITapGestureRecognizer, UIGestureRecognizerDelegate {
        private var began: TimeInterval = 0
        override init(target: Any?, action: Selector?) {
            super.init(target: target, action: action)
            cancelsTouchesInView = false
            delaysTouchesBegan = false
            delaysTouchesEnded = false
            delegate = self
        }
        override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
            began = event.timestamp
            super.touchesBegan(touches, with: event)
        }
        override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent) {
            if event.timestamp - began > Theme.TextLookup.tapLimit { state = .failed; return }
            super.touchesEnded(touches, with: event)
        }
        func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool { true }
        override func canPrevent(_ preventedGestureRecognizer: UIGestureRecognizer) -> Bool { false }
    }

    func makeUIGestureRecognizer(context: Context) -> Recognizer { Recognizer(target: nil, action: nil) }

    func handleUIGestureRecognizerAction(_ recognizer: Recognizer, context: Context) {
        guard recognizer.state == .ended else { return }
        action(context.converter.localLocation, recognizer.location(in: nil))
    }
}

/// The saved words' underlines: a hairline under each, in the accent at a low strength, drawn over the words by Core
/// Graphics in one pass (nothing per word in the view tree, nothing while scrolling).
private struct LookupUnderlineView: View {
    let underlines: LookupUnderlines

    var body: some View {
        if !underlines.rects.isEmpty {
            Canvas { ctx, _ in
                for r in underlines.rects {
                    let y = r.maxY - Theme.TextLookup.underlineInset
                    ctx.fill(Path(CGRect(x: r.minX, y: y, width: r.width, height: Theme.TextLookup.underline)), with: .color(Theme.TextLookup.underlineInk))
                }
            }
            .allowsHitTesting(false)
            .accessibilityHidden(true)
        }
    }
}
/// Look Up in <provider> in a text view's edit menu (#10977): where a word cannot be tapped (the chat's selected
/// words, which a text view stands in for) or for VoiceOver, which selects rather than taps. Only for one plain word,
/// while an extension answers; the card comes up as a popover over the selection.
enum TextLookupMenu {
    static func action(for picked: String, in view: UITextView, range: NSRange, source: String) -> UIAction? {
        let model = AppModel.shared.textLookup
        guard model.active, let word = TextLookupWords.plainWord(picked) else { return nil }
        let context = TextLookupWords.context(in: view.text ?? "", around: range)
        return UIAction(title: String(localized: "Look Up in \(model.providerName)"), image: UIImage(systemName: Theme.Icon.lookUpWord)) { [weak view] _ in
            guard let view else { return }
            var rect = view.bounds
            if let start = view.position(from: view.beginningOfDocument, offset: range.location),
               let end = view.position(from: start, offset: range.length), let r = view.textRange(from: start, to: end) {
                rect = view.firstRect(for: r)
            }
            present(TextLookupCard(model: model, word: word, context: context, source: source), from: view, rect: rect)
        }
    }

    /// The window the app's pages are in (not a pass-through overlay such as the toasts'): the key one of the normal
    /// level that shows a controller.
    static func pageWindow() -> UIWindow? {
        let windows = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
            .filter { $0.activationState == .foregroundActive }.flatMap(\.windows)
            .filter { $0.windowLevel == .normal && !$0.isHidden && $0.rootViewController != nil }
        return windows.first(where: \.isKeyWindow) ?? windows.first
    }

    static func present(_ card: TextLookupCard, from view: UIView, rect: CGRect) {
        guard var top = (view as? UIWindow)?.rootViewController ?? view.window?.rootViewController else { return }
        while let next = top.presentedViewController, !next.isBeingDismissed { top = next }
        let host = UIHostingController(rootView: card)
        host.sizingOptions = .preferredContentSize
        host.modalPresentationStyle = .popover
        if let p = host.popoverPresentationController {
            p.sourceView = view
            p.sourceRect = rect
            p.delegate = PopoverStaysPopover.shared
        }
        top.present(host, animated: true)
    }

    /// Keeps the card a popover on the iPhone too (as `presentationCompactAdaptation(.popover)`).
    final class PopoverStaysPopover: NSObject, UIPopoverPresentationControllerDelegate {
        static let shared = PopoverStaysPopover()
        func adaptivePresentationStyle(for controller: UIPresentationController, traitCollection: UITraitCollection) -> UIModalPresentationStyle { .none }
    }
}
#else
private struct LookUpWords: ViewModifier {
    let words: LookupSource
    func body(content: Content) -> some View { content }
}
#endif

// MARK: - The card

/// The lookup card (#10977): the word at once with a spinner, then its pronunciation and senses, Save / Saved, and the
/// extension's name. A failed lookup says why with Retry; it never blocks reading (a tap outside closes it).
struct TextLookupCard: View {
    let model: TextLookupModel
    let word: String
    let context: String
    let source: String

    enum Phase: Equatable {
        case loading
        case loaded(TextLookupEntry)
        case failed(String)
    }

    @State private var phase = Phase.loading
    @State private var saving = false
    @State private var saveError: String?
    @State private var attempt = 0

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            header
            switch phase {
            case .loading:
                HStack(spacing: Theme.Space.s) {
                    ProgressView().controlSize(.small)
                    Text("Looking up…").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                .accessibilityElement(children: .combine)
            case .failed(let why):
                Text(why).font(Theme.Font.secondary).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                Button { attempt += 1 } label: {
                    Label { Text("Retry").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.retry) }
                }
                .themedButton()
                .accessibilityIdentifier("textLookup.retry")
            case .loaded(let entry):
                if entry.found, !entry.senses.isEmpty {
                    senses(entry)
                } else {
                    Text("No entry for this word.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
            }
            if let saveError {
                Text(saveError).font(Theme.Font.caption).foregroundStyle(Theme.danger).fixedSize(horizontal: false, vertical: true)
            }
            footer
        }
        // One width, so its height is measured at the width it is drawn: the popover takes the card's ideal size, and
        // a flexible width measured the senses on one line each and cut the card off at the top and bottom.
        .scaledFrame(width: Theme.Size.box300 - 2 * Theme.Space.m, alignment: .leading)
        .fixedSize(horizontal: false, vertical: true)
        .padding(Theme.Space.m)
        .animation(Theme.Motion.layout, value: phase)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("textLookup.card")
        .task(id: attempt) { await load() }
    }

    private var entry: TextLookupEntry? {
        if case .loaded(let e) = phase { return e }
        return nil
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(verbatim: entry?.word ?? word)
                .font(Theme.Font.title)
                .lineLimit(1)
                .minimumScaleFactor(Theme.TextLookup.minimumScale)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("textLookup.word")
            if let phonetic = entry?.phonetic, !phonetic.isEmpty {
                Text(verbatim: phonetic).font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
        }
    }

    private func senses(_ entry: TextLookupEntry) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            ForEach(Array(entry.senses.prefix(Theme.TextLookup.senseLimit).enumerated()), id: \.offset) { _, sense in
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                    if let pos = sense.pos, !pos.isEmpty {
                        Text(verbatim: pos).font(Theme.Font.captionStrong).foregroundStyle(.secondary)
                    }
                    Text(verbatim: sense.text).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
            }
        }
    }

    private var footer: some View {
        HStack(spacing: Theme.Space.s) {
            Text(verbatim: model.providerName)
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            Spacer(minLength: Theme.Space.s)
            if let entry, entry.found {
                let saved = model.isSaved(entry)
                Button { toggle(entry, saved: saved) } label: {
                    Label { Text(saved ? "Saved [Text Lookup]" : "Save [Text Lookup]").lineLimit(1) } icon: {
                        ThemedIcon(saved ? Theme.Icon.savedWord : Theme.Icon.saveWord)
                    }
                }
                .themedButton(prominent: !saved)
                .disabled(saving)
                .accessibilityIdentifier("textLookup.save")
                .accessibilityValue(saved ? Text("Saved [Text Lookup]") : Text(verbatim: ""))
            }
        }
    }

    private func load() async {
        phase = .loading
        do {
            phase = .loaded(try await model.lookup(word, context: context))
        } catch is CancellationError {
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    private func toggle(_ entry: TextLookupEntry, saved: Bool) {
        saving = true
        saveError = nil
        Task {
            do {
                if saved { try await model.remove(entry) } else { try await model.save(entry, context: context, source: source) }
                Haptics.selection()
            } catch {
                saveError = error.localizedDescription
            }
            saving = false
        }
    }
}
