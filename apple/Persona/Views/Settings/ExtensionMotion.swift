import SwiftUI
import PersonaWire

// What an extension's View shows when it is drawn again (#37, as on the web and Android): under the same
// key, a `metric`, `stat_grid` or row value whose numbers alone changed rolls to the new ones (`numericText`), and any
// other change of a value or a badge's words fades in; a badge that changes tone changes color; a block whose key is
// new fades in and one that went fades out; a chart's bars and lines move to the new values. Nothing moves on the first
// draw or with Reduce Motion, and at rest every value is exactly the extension's text.

/// How what an extension draws again goes over to the new View: a value to its new text, the blocks to their new set.
nonisolated enum ExtensionMotion {
    enum Change: Equatable, Sendable {
        /// The same text: nothing moves.
        case same
        /// Only its numbers changed (`2/8` → `6/8`, `¥1.20` → `¥36.20`, `1.2M` → `8.4M`): the digits roll, downward
        /// when the first number that changed went down.
        case roll(down: Bool)
        /// Anything else (`Working` → `Waiting on you`, `—` → `3`, `+5%` → `-5%`): the new text fades in.
        case fade
    }

    static func change(from old: String, to new: String) -> Change {
        guard old != new else { return .same }
        let a = parts(old), b = parts(new)
        guard !b.numbers.isEmpty, a.text == b.text else { return .fade }
        let first = zip(a.numbers, b.numbers).first { $0 != $1 }
        return .roll(down: first.map { $1 < $0 } ?? false)
    }

    /// A value's numbers and the text around them: "¥1,234.5/8" is ["¥", "/", ""] around [1234.5, 8]. A sign, a
    /// currency, a unit and anything else stay text, so two values of the same kind have the same text.
    static func parts(_ s: String) -> (text: [String], numbers: [Double]) {
        var text: [String] = []
        var numbers: [Double] = []
        var at = s.startIndex
        for m in s.matches(of: number) {
            text.append(String(s[at..<m.range.lowerBound]))
            numbers.append(Double(m.output.filter { $0 != "," }) ?? 0)
            at = m.range.upperBound
        }
        text.append(String(s[at...]))
        return (text, numbers)
    }

    /// A number in a value: 1,234.5, 12 or 0.75. ASCII digits only: `\d` would take other scripts' digits, which
    /// `Double` does not read. Built once: a literal in the function is parsed again on every call.
    nonisolated(unsafe) private static let number = #/[0-9]{1,3}(?:,[0-9]{3})+(?:\.[0-9]+)?|[0-9]+(?:\.[0-9]+)?/#

    static func transition(_ change: Change) -> ContentTransition {
        if case .roll(let down) = change { .numericText(countsDown: down) } else { .opacity }
    }

    /// The animation a value's change is drawn with: none when nothing changed or with Reduce Motion.
    static func animation(_ change: Change, reduced: Bool) -> Animation? {
        change == .same || reduced ? nil : Theme.Motion.state
    }

    /// The animation a redraw is committed with: blocks that came or went fade in and out and the rest moves, in a list
    /// too, whose rows follow the transaction that changed them rather than a modifier inside it. None on the first draw,
    /// when only values changed (they move by themselves), with Reduce Motion, or on a code page (`wantsCodePage`), whose
    /// trees, code and diffs are not these components.
    static func layout(from old: ExtensionView?, to new: ExtensionView, reduced: Bool) -> Animation? {
        guard let old, !reduced, !old.wantsCodePage, !new.wantsCodePage, blocks(old) != blocks(new) else { return nil }
        return Theme.Motion.layout
    }

    /// Commits a redraw (`apply` puts the new View in place) with `layout`'s animation.
    @MainActor static func redraw(from old: ExtensionView?, to new: ExtensionView, _ apply: () -> Void) {
        withAnimation(layout(from: old, to: new, reduced: Theme.Motion.reduced), apply)
    }

    /// What a View's blocks are, with the identities the renderers draw them under (by key, by place without one): when
    /// this changes on a redraw, blocks came or went.
    static func blocks(_ view: ExtensionView) -> [String] {
        var out: [String] = []
        for (i, s) in view.sections.enumerated() {
            out.append("s" + ExtensionViewStack.SectionEntry.id(s, index: i))
            for (j, c) in s.items.enumerated() { out.append(ExtensionParts.Item.id(c, section: i, index: j)) }
        }
        return out
    }
}

/// A value an extension may draw again with new words (a metric's, a stat's, a row's): its numbers roll or its words
/// fade in (`ExtensionMotion`). The first draw and Reduce Motion show it at once; at rest it is exactly `text`.
struct ExtensionChangingText: View {
    var text: String
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    /// The text as last drawn, and how it came to be: the frame after a change keeps that change's transition.
    @State private var drawn: String
    @State private var last = ExtensionMotion.Change.same

    init(_ text: String) {
        self.text = text
        _drawn = State(initialValue: text)
    }

    var body: some View {
        let now = ExtensionMotion.change(from: drawn, to: text)
        Text(text)
            .contentTransition(ExtensionMotion.transition(now == .same ? last : now))
            .animation(ExtensionMotion.animation(now, reduced: reduceMotion), value: text)
            .onChange(of: text) { old, new in
                last = ExtensionMotion.change(from: old, to: new)
                drawn = new
            }
    }
}
