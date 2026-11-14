import SwiftUI

// Thread (#2128, docs/assistant/inbox.md「Relationships: parents and children」), as on the web (#2352): under an opened item's summary, what it grew out of (its
// parent's chain, up to a todo or an agent session) and what grew out of it (the items right under it). Each row opens
// where it lives: another item, the todo, the agent's chat. Nothing is drawn when the item has neither, nor from a hub
// without GET /api/v1/inbox/tree.

/// The rows of a line, worked out from the hub's tree (pure, for tests).
nonisolated enum InboxLine {
    enum Target: Hashable, Sendable {
        case item(InboxItem)
        case todo(String)
        case session(SessionKey)
    }

    struct Row: Hashable, Sendable, Identifiable {
        var id: String
        var kind: String // inbox | todo | session
        var label: String
        /// Empty when a non-item ancestor has no local name, or its name repeats the kind label.
        var title: String
        /// Where a tap goes; nil: nowhere left to go (an item no longer in the inbox, a session id without its halves).
        var target: Target?
        /// The lower inbox item in this row's pair: detaching removes that item's parent, never this row's parent.
        var detach: InboxParentTarget?
        /// An ancestor (what it grew out of), or an item under it (Next).
        var up: Bool
    }

    static func label(_ kind: String) -> String {
        switch kind {
        case "todo": String(localized: "To-dos")
        case "session": String(localized: "Agent Session")
        default: String(localized: "Event [Inbox Line c56020]")
        }
    }

    /// The ancestors farthest first (the chain reads top down), then the children oldest first. `todoTitle` /
    /// `sessionTitle` name a todo or a session the app knows; without a live local title only the kind is shown.
    static func rows(_ tree: InboxTree, todoTitle: (String) -> String? = { _ in nil },
                     sessionTitle: (SessionKey) -> String? = { _ in nil }) -> [Row] {
        // Find each pair's lower end in the hub's nearest-first order before reversing the rows for display.
        let ups = tree.ancestors.enumerated().map { i, a -> Row in
            let p = a.parent
            let lower = i == 0 ? tree.node : tree.ancestors[i - 1].parent
            let lowerItem = i == 0 ? tree.item : tree.ancestors[i - 1].item
            let detach = lower.kind == "inbox"
                ? InboxParentTarget(id: lower.id, manager: lowerItem?.manager ?? tree.item?.manager)
                : nil
            let title: String
            let target: Target?
            switch p.kind {
            case "todo":
                title = localTitle(todoTitle(p.id), kind: p.kind)
                target = .todo(p.id)
            case "session":
                title = localTitle(p.sessionKey.flatMap(sessionTitle), kind: p.kind)
                target = p.sessionKey.map(Target.session)
            default:
                title = a.item?.title ?? String(localized: "No longer in the Inbox")
                target = a.item.map(Target.item)
            }
            return Row(id: "up:\(p.kind):\(p.id)", kind: p.kind, label: label(p.kind), title: title, target: target,
                       detach: detach, up: true)
        }.reversed()
        let downs = tree.children.map {
            Row(id: "down:\($0.id)", kind: "inbox", label: String(localized: "Next [Inbox Line acfc4e]"), title: $0.title,
                target: .item($0), detach: InboxParentTarget(id: $0.id, manager: $0.manager ?? tree.item?.manager), up: false)
        }
        return Array(ups) + downs
    }

    /// A manager's tree response has no merged-list manager stamps. The opened item's owner applies to every item in
    /// its tree, so relationship writes and the picker keep that owner instead of falling back to the default manager.
    static func stamping(_ tree: InboxTree, manager: String?) -> InboxTree {
        func stamp(_ item: InboxItem) -> InboxItem {
            var item = item
            item.manager = manager
            return item
        }
        var stamped = tree
        stamped.item = stamped.item.map(stamp)
        stamped.ancestors = stamped.ancestors.map { ancestor in
            var ancestor = ancestor
            ancestor.item = ancestor.item.map(stamp)
            return ancestor
        }
        stamped.children = stamped.children.map(stamp)
        return stamped
    }

    /// A missing name adds nothing to the kind label; a name identical to it must not be drawn twice.
    private static func localTitle(_ candidate: String?, kind: String) -> String {
        guard let title = candidate?.trimmingCharacters(in: .whitespacesAndNewlines), !title.isEmpty, title != label(kind) else { return "" }
        return title
    }

    static func symbol(_ kind: String) -> String {
        switch kind {
        case "todo": Theme.Icon.todo
        case "session": Theme.Icon.agents
        default: Theme.Icon.inbox
        }
    }
}

/// Opens an Agent linked by an item's Thread, preserving the old destination where no inbox stack is available.
@MainActor enum InboxLineOpen {
    static func agent(_ key: SessionKey, owner: String?, model: AppModel, push: ((InboxChatRoute) -> Void)? = nil) {
        if let push {
            push(InboxChatRoute(key: model.inHub(key), manager: owner))
        } else {
            openTodoAgentChat(model.inHub(key), in: model)
        }
    }
}

/// The Thread section of an opened item (iPhone's action bar, the Mac's actions column): the rows of `tree`, which the
/// host loads with `.inboxLine(_:tree:)` (on a view that is always there: on this section, which is empty until it has
/// rows, the answer never showed). `openItem`: how another item opens here; nil pushes it on the stack the section is in.
struct InboxLineSection: View {
    @Environment(AppModel.self) private var model
    @Environment(\.pushInboxChat) private var pushChat
    var tree: InboxTree?
    var openItem: ((InboxItem) -> Void)?
    @State private var todo: TodoDetailRoute?
    /// An Agent chosen inside the to-do sheet: its chat pushes only after that sheet has finished closing.
    @State private var pendingAgent: SessionKey?
    /// One relationship write at a time; the buttons stay disabled until it has answered.
    @State private var detaching: InboxParentTarget?
    @State private var sameMatter = false

    var body: some View {
        let rows = tree.map {
            InboxLine.rows($0, todoTitle: { id in TodoStore.shared.todos.first { $0.id == id }?.title },
                           sessionTitle: { model.session(model.inHub($0))?.displayTitle })
        } ?? []
        if !rows.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text("Thread").font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                ForEach(rows) { lineRow($0) }
                #if !os(macOS)
                if tree?.item != nil {
                    Button { sameMatter = true } label: {
                        HStack(spacing: Theme.Space.s) {
                            ThemedIcon(Theme.Icon.link)
                            Text(String(localized: "Same matter as…")).buttonLabelLine()
                        }
                    }
                    .font(Theme.Font.secondaryStrong)
                    .themedButton()
                    .tapTargetHeight()
                    .accessibilityIdentifier("inbox.line.same")
                }
                #endif
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("inbox.line")
            #if !os(macOS)
            .sheet(isPresented: $sameMatter) {
                if let tree, let item = tree.item {
                    InboxSameMatterPicker(item: item, tree: tree)
                }
            }
            #endif
            .sheet(item: $todo, onDismiss: {
                guard let key = pendingAgent else { return }
                pendingAgent = nil
                InboxLineOpen.agent(key, owner: tree?.item?.manager, model: model, push: pushChat)
            }) { r in
                NavigationStack {
                    TodoDetailView(todoID: r.id, openAgent: pushChat == nil ? nil : { key in
                        pendingAgent = key
                        todo = nil
                    }).macSheetDone { todo = nil }
                }
                #if os(macOS)
                .macSheet()
                #endif
            }
        }
    }

    /// Navigation and detaching are siblings, so VoiceOver never folds the write into the row's destination.
    @ViewBuilder private func lineRow(_ r: InboxLine.Row) -> some View {
        #if os(macOS)
        row(r)
        #else
        // Measure at every width and text size: a long translation can need the vertical layout before accessibility sizes.
        ViewThatFits(in: .horizontal) {
            HStack(alignment: .center, spacing: Theme.Space.xs) {
                row(r)
                detachButton(r)
            }
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                row(r)
                detachButton(r)
            }
        }
        #endif
    }

    /// On iOS the write stays outside the row's combined navigation accessibility element.
    @ViewBuilder private func detachButton(_ r: InboxLine.Row) -> some View {
        #if !os(macOS)
        if let target = r.detach {
            Button { detach(target) } label: {
                HStack(spacing: Theme.Space.xs) {
                    Text(String(localized: "Not the same matter")).buttonLabelLine()
                    if detaching == target { WorkingIndicator() }
                }
            }
            .font(Theme.Font.secondaryStrong)
            .themedButton()
            .tapTargetHeight()
            .disabled(detaching != nil)
            .accessibilityIdentifier("inbox.line.notSame.\(target.id)")
        }
        #endif
    }

    private func detach(_ target: InboxParentTarget) {
        guard detaching == nil else { return }
        detaching = target
        Task {
            _ = await InboxParentChange.detach(target, model: model)
            detaching = nil
        }
    }

    @ViewBuilder private func row(_ r: InboxLine.Row) -> some View {
        switch r.target {
        case .item(let it)?:
            if let openItem {
                Button { openItem(it) } label: { label(r) }.buttonStyle(.plain)
            } else {
                NavigationLink(value: it) { label(r) }.buttonStyle(.plain)
            }
        case .todo(let id)?:
            Button { todo = TodoDetailRoute(id: id) } label: { label(r) }.buttonStyle(.plain)
        case .session(let key)?:
            Button { InboxLineOpen.agent(key, owner: tree?.item?.manager, model: model, push: pushChat) } label: { label(r) }.buttonStyle(.plain)
        case nil:
            label(r)
        }
    }

    private func label(_ r: InboxLine.Row) -> some View {
        let hasTitle = !r.title.isEmpty
        return HStack(spacing: Theme.Space.s) {
            ThemedIcon(InboxLine.symbol(r.kind))
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.iconXS)
            Text(r.label).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).fixedSize()
            if hasTitle {
                Text(r.title)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(r.target == nil ? Color.secondary : Color.primary)
                    .lineLimit(1)
                    .truncationMode(.tail)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                Spacer(minLength: Theme.Space.flush)
            }
            if r.target != nil {
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary)
            }
        }
        .padding(.vertical, Theme.Space.xs)
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(hasTitle ? String(localized: "\(r.label): \(r.title)") : r.label)
    }
}

extension View {
    /// Loads `item`'s Thread into `tree` (GET /api/v1/inbox/tree/inbox/{id}), again when the item is hung elsewhere; an
    /// older hub without the route leaves it nil.
    func inboxLine(_ item: InboxItem, tree: Binding<InboxTree?>) -> some View {
        modifier(InboxLineLoader(item: item, tree: tree))
    }
}

private struct InboxLineLoader: ViewModifier {
    @Environment(AppModel.self) private var model
    var item: InboxItem
    @Binding var tree: InboxTree?
    /// A successful detach or link anywhere asks every visible line to read its tree again.
    @State private var relationshipRevision = 0

    private var loadID: String {
        item.id + "|" + (item.parent.map { $0.kind + ":" + $0.id } ?? "") + "|relationship:\(relationshipRevision)"
    }

    func body(content: Content) -> some View {
        content
            .onReceive(NotificationCenter.default.publisher(for: .inboxParentChanged)) { _ in
                relationshipRevision += 1
            }
            .task(id: loadID) {
                guard let api = model.api?.manager(model.itemManager(item.manager)),
                      let loaded = try? await api.inboxTree("inbox", item.id), !Task.isCancelled else { return }
                let t = InboxLine.stamping(loaded, manager: item.manager)
                withAnimation(Theme.Motion.layout) { tree = t }
            }
    }
}
