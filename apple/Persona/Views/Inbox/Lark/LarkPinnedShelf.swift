import SwiftUI

/// The compact shelf and its expanded rows use the same ordering and unread state. It holds the pinned chats and the
/// pinned personal Agents (`LarkAgentFeed.shelf`), each with its own face.
struct LarkPinnedShelf: View {
    let conversations: [LarkFeedRow]
    @Binding var interaction: LarkPinnedInteraction
    @Binding var expandable: Bool
    let open: (LarkFeedRow) -> Void
    @ScaledMetric private var cellWidth = Theme.Size.box56
    @State private var width: CGFloat = 0

    private var columns: Int { max(2, Int((width + Theme.Space.s) / (cellWidth + Theme.Space.s))) }
    private var canExpand: Bool { conversations.count > columns }
    private var firstCount: Int { canExpand ? columns - 1 : columns }
    private var hiddenUnread: Int { conversations.dropFirst(firstCount).reduce(0) { $0 + $1.unread } }

    var body: some View {
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(minimum: 0), spacing: Theme.Space.s, alignment: .top), count: columns),
                  alignment: .leading, spacing: Theme.Space.l) {
            ForEach(conversations.prefix(firstCount)) { pin($0) }
            if canExpand {
                Button { withAnimation(Theme.Motion.expand) { interaction.toggle() } } label: {
                    VStack(spacing: Theme.Space.xs) {
                        Image(systemName: interaction.isExpanded ? Theme.Icon.collapse : Theme.Icon.expand)
                            .font(Theme.Font.heading).foregroundStyle(.secondary)
                            .scaledFrame(width: Theme.Size.avatarXL, height: Theme.Size.avatarXL)
                            .background(Theme.Fill.well, in: Circle())
                            .overlay(alignment: .topTrailing) {
                                if !interaction.isExpanded, hiddenUnread > 0 {
                                    CountBadge(count: hiddenUnread).dynamicTypeSize(...Theme.Shell.topBarLargestTypeSize)
                                }
                            }
                            .dynamicTypeSize(...Theme.Shell.topBarLargestTypeSize)
                        Text(interaction.isExpanded ? String(localized: "Collapse") : String(localized: "Expand")).font(Theme.Font.caption)
                            .foregroundStyle(.secondary).lineLimit(1)
                    }.frame(maxWidth: .infinity).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(interaction.isExpanded ? String(localized: "Collapse pinned chats") : String(localized: "Expand pinned chats"))
                .accessibilityValue("\(conversations.count - firstCount) chats, \(hiddenUnread) unread")
                .accessibilityIdentifier("feishu-pins-toggle")
            }
            if interaction.isExpanded { ForEach(conversations.dropFirst(firstCount)) { pin($0) } }
        }
        .padding(.vertical, Theme.Space.m)
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { width = $0 }
        .accessibilityIdentifier("feishu-pins")
        .onChange(of: canExpand, initial: true) { _, can in
            expandable = can
            if !can { interaction = LarkPinnedInteraction() }
        }
        .onDisappear { expandable = false }
    }

    private func pin(_ row: LarkFeedRow) -> some View {
        Button { open(row) } label: {
            VStack(spacing: Theme.Space.xs) {
                face(row)
                    .dynamicTypeSize(...Theme.Shell.topBarLargestTypeSize)
                    .overlay(alignment: .topTrailing) {
                        if row.unread > 0 { CountBadge(count: row.unread).dynamicTypeSize(...Theme.Shell.topBarLargestTypeSize) }
                    }
                Text(name(row)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }.frame(maxWidth: .infinity).contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Pinned: \(name(row))"))
        .accessibilityValue(row.unread > 0 ? String(localized: "\(row.unread) unread [Lark Pinned Shelf be6134]") : String(localized: "No unread messages"))
        .accessibilityIdentifier(identifier(row))
    }

    @ViewBuilder private func face(_ row: LarkFeedRow) -> some View {
        switch row {
        case .chat(let c): LarkAvatarImage(name: c.name, url: c.picture, account: c.account, size: Theme.Size.avatarXL)
        case .agent(let s): PersonalAgentAvatar(session: s, size: Theme.Size.avatarXL, showsCharge: true)
        }
    }

    private func name(_ row: LarkFeedRow) -> String {
        switch row {
        case .chat(let c): c.name
        case .agent(let s): s.title
        }
    }

    private func identifier(_ row: LarkFeedRow) -> String {
        switch row {
        case .chat(let c): "feishu-pin-" + c.id
        case .agent(let s): "feishu-pin-agent-" + s.id
        }
    }
}

/// Offset compensation belongs to the scroll view, not every frame of the page's state.
@MainActor final class LarkConversationScroll {
    let reader = ReaderAnchor()
    weak var list: UIScrollView?
    var shelfHeight: CGFloat = 0
    var compensateCollapse = false

    func resetShelf() {
        shelfHeight = 0
        compensateCollapse = false
    }

    func attach(_ list: UIScrollView) {
        self.list = list
        reader.shown = { [weak self] in
            guard let list = self?.list, list.window != nil else { return nil }
            let frame = list.convert(list.bounds, to: nil)
            let top = frame.minY + list.adjustedContentInset.top
            let bottom = frame.maxY - list.adjustedContentInset.bottom
            guard bottom > top else { return nil }
            return (top...bottom, list.contentOffset.y <= -list.adjustedContentInset.top + Theme.Size.line)
        }
        reader.scroll = { [weak self] in self?.move(by: $0) }
        reader.moving = { [weak self] in
            guard let list = self?.list else { return false }
            return list.isTracking || list.isDragging || list.isDecelerating
        }
    }

    func shelfChanged(_ height: CGFloat) {
        let delta = height - shelfHeight
        shelfHeight = height
        if compensateCollapse, delta < 0 {
            compensateCollapse = false
            move(by: delta)
        }
    }

    private func move(by delta: CGFloat) {
        guard let list else { return }
        let low = -list.adjustedContentInset.top
        let high = max(low, list.contentSize.height + list.adjustedContentInset.bottom - list.bounds.height)
        list.contentOffset.y = min(high, max(low, list.contentOffset.y + delta))
    }

    struct Finder: UIViewRepresentable {
        let scroll: LarkConversationScroll
        func makeUIView(context: Context) -> Probe {
            let view = Probe(); view.scroll = scroll; view.isUserInteractionEnabled = false; return view
        }
        func updateUIView(_ view: Probe, context: Context) { view.scroll = scroll; view.find() }
        final class Probe: UIView {
            weak var scroll: LarkConversationScroll?
            override func didMoveToWindow() { super.didMoveToWindow(); find() }
            func find() {
                var current = superview
                while let view = current, !(view is UIScrollView) { current = view.superview }
                if let list = current as? UIScrollView { scroll?.attach(list) }
            }
        }
    }
}
