import SwiftUI

/// A main view with an aside (a time, a row of counts) and optionally a tail (a fold chevron): the aside at the row's
/// end while the main view keeps at least half the width, or the whole of it when it needs less; otherwise the aside on
/// a line of its own under the main view, the tail staying on the main view's line. Like `StackingButtonRow` it goes by
/// measured widths, not by the text size: a short name keeps its counts beside it at any size, a long one gives way
/// at sizes where the beside layout left it a character or two (#1658).
struct BesideOrUnder: Layout {
    /// Between the main view and the aside when they share a line.
    var gap: CGFloat = Theme.Space.sm
    /// Between the aside and the tail.
    var tailGap: CGFloat = Theme.Space.s
    /// Between the lines when the aside goes under.
    var lineGap: CGFloat = Theme.Space.xs
    var alignment: VerticalAlignment = .center
    /// The share of the width the main view must get, or all it needs if that is less, for the aside to stay beside it.
    var minShare: CGFloat = 0.5

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard subviews.count >= 2 else { return subviews.first?.sizeThatFits(proposal) ?? .zero }
        let width = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? idealWidth(subviews)
        return CGSize(width: width, height: frames(width, subviews).height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard subviews.count >= 2 else { subviews.first?.place(at: bounds.origin, anchor: .topLeading, proposal: proposal); return }
        let laid = frames(bounds.width, subviews)
        for (i, frame) in laid.frames.enumerated() {
            subviews[i].place(at: CGPoint(x: bounds.minX + frame.minX, y: bounds.minY + frame.minY), anchor: .topLeading,
                              proposal: ProposedViewSize(frame.size))
        }
    }

    private func tailWidth(_ subviews: Subviews) -> CGFloat {
        guard subviews.count > 2 else { return 0 }
        let width = subviews[2].sizeThatFits(.unspecified).width
        return width > 0 ? width + tailGap : 0
    }

    private func idealWidth(_ subviews: Subviews) -> CGFloat {
        subviews[0].sizeThatFits(.unspecified).width + gap + subviews[1].sizeThatFits(.unspecified).width + tailWidth(subviews)
    }

    private func frames(_ width: CGFloat, _ subviews: Subviews) -> (frames: [CGRect], height: CGFloat) {
        let mainIdeal = subviews[0].sizeThatFits(.unspecified).width
        let aside = subviews[1].sizeThatFits(.unspecified)
        let tail = tailWidth(subviews)
        let beside = width - aside.width - gap - tail >= min(mainIdeal, width * minShare)
        let mainWidth = max(0, beside ? width - aside.width - gap - tail : width - tail)
        let main = subviews[0].sizeThatFits(ProposedViewSize(width: mainWidth, height: nil))
        var items: [(index: Int, size: CGSize, x: CGFloat)] = [(0, CGSize(width: mainWidth, height: main.height), 0)]
        let hasTail = subviews.count > 2
        let tailSize = hasTail ? subviews[2].sizeThatFits(.unspecified) : .zero
        if beside {
            items.append((1, aside, width - aside.width - tail))
            if hasTail { items.append((2, tailSize, width - tailSize.width)) }
        } else if hasTail {
            items.append((2, tailSize, width - tailSize.width))
        }
        let line = lineFrames(items, subviews, top: 0)
        var out = [CGRect](repeating: .zero, count: subviews.count)
        for (i, rect) in line.rects { out[i] = rect }
        guard !beside else { return (out, line.height) }
        let y = line.height + lineGap
        out[1] = CGRect(x: 0, y: y, width: min(aside.width, width), height: aside.height)
        return (out, y + aside.height)
    }

    /// The items on one line, each shifted so that its `alignment` guide meets the others'.
    private func lineFrames(_ items: [(index: Int, size: CGSize, x: CGFloat)], _ subviews: Subviews, top: CGFloat)
        -> (rects: [(Int, CGRect)], height: CGFloat) {
        let keys = items.map { item in
            subviews[item.index].dimensions(in: ProposedViewSize(item.size))[alignment]
        }
        let anchor = keys.max() ?? 0
        var height: CGFloat = 0
        var rects: [(Int, CGRect)] = []
        for (item, key) in zip(items, keys) {
            let y = top + anchor - key
            rects.append((item.index, CGRect(origin: CGPoint(x: item.x, y: y), size: item.size)))
            height = max(height, anchor - key + item.size.height)
        }
        return (rects, height)
    }
}
