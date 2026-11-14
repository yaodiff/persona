import SwiftUI

// MARK: - Long words folded to a few lines

/// Words drawn as Markdown, folded to `lines` lines of body text, with Expand / Collapse under them when there is more
/// (#2010: a long message from another session). `lineLimit` does not reach into Markdown's blocks, so the words are
/// cut at the height of that many body lines and fade out over the last one; words that fit are drawn as they are,
/// with no button. Open or not is kept by `id` (`OpenReports`, in the chat's scope), so a row the lazy list builds
/// again stays as it was left; `id` is also the key the row is pinned by (`chatPinned`), which keeps its top where it
/// is on screen while it opens or folds.
struct LineFold<Words: View>: View {
    var lines: Int
    var id: String
    @ViewBuilder var words: () -> Words
    /// The words' whole height, and the height of `lines` body lines.
    @State private var full: CGFloat = 0
    @State private var cut: CGFloat = 0
    @Environment(\.rowMemoScope) private var scope
    @Environment(\.chatPin) private var pin

    private var key: String { scope + "/" + id }
    /// Something to open or fold: the words run past their lines.
    private var folds: Bool { cut > 0 && full > cut + 1 }

    var body: some View {
        let open = OpenReports.shared.isOpen(key)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            measured
                .frame(height: folds && !open ? cut : nil, alignment: .top)
                .clipped()
                // What is folded away takes no touches: the words run on past the fold, and the chat's upside-down list
                // draws this row over the one below it, whose taps they took (#6645: a work line there never opened).
                .contentShape(.rect)
                .mask { if folds && !open { FoldFadeMask(line: cut / CGFloat(max(lines, 1))) } else { Rectangle() } }
                .background(alignment: .top) { FoldLinesProbe(lines: lines, font: Theme.Font.body, level: .body) { cut = $0 } }
            if folds { FoldToggle(open: open, prefix: "message", action: { flip(open) }) }
        }
    }

    /// The words at their own height, which is read.
    private var measured: some View {
        words()
            .fixedSize(horizontal: false, vertical: true)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { full = $0 }
    }

    private func flip(_ open: Bool) {
        Haptics.tap()
        toggleInPlace(pin, id, animation: Theme.Motion.state, closing: open) { OpenReports.shared.set(key, !open) }
    }
}

/// `lines` lines of the words' type, unseen and measured: the height a fold cuts its words at (`LineFold`, `CardFold`).
struct FoldLinesProbe: View {
    var lines: Int
    var font: Font
    /// The line height the words are drawn with (`lineHeight(_:)`); nil: the font's own (code).
    var level: Theme.Level?
    var measured: (CGFloat) -> Void

    var body: some View {
        spaced(Text(verbatim: Array(repeating: "X", count: max(lines, 1)).joined(separator: "\n")).font(font))
            .fixedSize(horizontal: false, vertical: true)
            .hidden()
            .accessibilityHidden(true)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { measured($0) }
    }

    @ViewBuilder private func spaced(_ text: some View) -> some View {
        if let level { text.lineHeight(level) } else { text }
    }
}

/// A fold's mask: opaque down to the last line shown, which fades out (`LineFold`, `CardFold`).
struct FoldFadeMask: View {
    /// One line's height: the fade.
    var line: CGFloat

    var body: some View {
        VStack(spacing: 0) {
            Rectangle()
            LinearGradient(colors: [.black, .clear], startPoint: .top, endPoint: .bottom).frame(height: line)
        }
    }
}
