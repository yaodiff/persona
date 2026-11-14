import SwiftUI

/// Equal-width columns in one row while every cell's content fits its share whole, and two columns (the cells wrapping
/// onto further rows) when one does not, so a number or a title is never cut for want of width. Like `StackingButtonRow`
/// it goes by the measured widths, not by the text size: a short value keeps its row at sizes where a long one, or a
/// long translation, drops to two columns. A cell tells how narrow it can be through its ideal width, which a
/// `lineLimit(1)` text reports whole, and through `equalColumnShrink` when its text may scale down (a value with
/// `minimumScaleFactor`); the words still never get cut. One or two cells always share one row.
struct EqualColumns: Layout {
    var spacing: CGFloat = Theme.Space.m
    var rowSpacing: CGFloat = Theme.Space.m
    /// The columns when the cells do not fit side by side: two by default, one to stack them.
    var fallbackColumns = 2

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard !subviews.isEmpty else { return .zero }
        let width = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? idealWidth(subviews)
        let heights = rowHeights(width, subviews)
        return CGSize(width: width, height: heights.reduce(0, +) + rowSpacing * CGFloat(heights.count - 1))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard !subviews.isEmpty else { return }
        let count = columns(bounds.width, subviews)
        let each = columnWidth(bounds.width, count)
        let heights = rowHeights(bounds.width, subviews)
        var y = bounds.minY
        for (row, height) in heights.enumerated() {
            for column in 0..<count {
                let i = row * count + column
                guard i < subviews.count else { break }
                subviews[i].place(at: CGPoint(x: bounds.minX + CGFloat(column) * (each + spacing), y: y), anchor: .topLeading,
                                  proposal: ProposedViewSize(width: each, height: height))
            }
            y += height + rowSpacing
        }
    }

    /// Equal columns want every column as wide as the widest cell, which is what a `ViewThatFits` around this layout asks.
    private func idealWidth(_ subviews: Subviews) -> CGFloat {
        let widest = subviews.map { $0.sizeThatFits(.unspecified).width * $0[EqualColumnShrink.self] }.max() ?? 0
        return widest * CGFloat(subviews.count) + spacing * CGFloat(max(subviews.count - 1, 0))
    }

    private func columnWidth(_ width: CGFloat, _ count: Int) -> CGFloat {
        (width - spacing * CGFloat(count - 1)) / CGFloat(count)
    }

    private func columns(_ width: CGFloat, _ subviews: Subviews) -> Int {
        let n = subviews.count
        guard n > 2 else { return n }
        let each = columnWidth(width, n)
        return subviews.allSatisfy { $0.sizeThatFits(.unspecified).width * $0[EqualColumnShrink.self] <= each } ? n : min(fallbackColumns, n)
    }

    private func rowHeights(_ width: CGFloat, _ subviews: Subviews) -> [CGFloat] {
        let count = columns(width, subviews)
        let offer = ProposedViewSize(width: columnWidth(width, count), height: nil)
        return stride(from: 0, to: subviews.count, by: count).map { start in
            subviews[start..<min(start + count, subviews.count)].map { $0.sizeThatFits(offer).height }.max() ?? 0
        }
    }
}

/// How far a cell's text may scale down in an `EqualColumns` before it counts as not fitting (1: not at all).
nonisolated struct EqualColumnShrink: LayoutValueKey {
    static let defaultValue: CGFloat = 1
}

extension View {
    /// The least scale this cell's text may take (its `minimumScaleFactor`) for `EqualColumns` to keep it in a row.
    func equalColumnShrink(_ scale: CGFloat) -> some View {
        layoutValue(key: EqualColumnShrink.self, value: scale)
    }
}
