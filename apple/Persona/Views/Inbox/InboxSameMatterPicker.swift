import SwiftUI

/// An inbox item whose parent relationship is changing, scoped to the manager that owns it.
nonisolated struct InboxParentTarget: Hashable, Sendable {
    var id: String
    var manager: String?

    init(id: String, manager: String?) {
        self.id = id
        self.manager = manager
    }
}

extension Notification.Name {
    static let inboxParentChanged: Notification.Name = Notification.Name("Persona.inboxParentChanged")
}

/// Writes the two reversible Thread relationships: detach a child, or hang the opened item under another open item.
@MainActor enum InboxParentChange {
    static func detach(_ target: InboxParentTarget, model: AppModel) async -> Bool {
        await update(target, to: nil, success: String(localized: "Marked as not the same matter"), model: model)
    }

    static func link(_ item: InboxItem, to parent: InboxItem, model: AppModel) async -> Bool {
        await update(InboxParentTarget(id: item.id, manager: item.manager),
                     to: InboxParent(kind: "inbox", id: parent.id),
                     success: String(localized: "Linked to “\(parent.title)”"), model: model)
    }

    private static func update(_ target: InboxParentTarget, to parent: InboxParent?, success: String,
                               model: AppModel) async -> Bool {
        guard let api = model.api?.manager(model.itemManager(target.manager)) else {
            model.notDone()
            return false
        }
        do {
            let fresh = try await api.setInboxParent(target.id, parent)
            InboxStore.shared.put(fresh, manager: target.manager)
            model.toast = Toast(success, style: .success)
            NotificationCenter.default.post(name: .inboxParentChanged, object: fresh)
            return true
        } catch is CancellationError {
            return false
        } catch {
            guard !Task.isCancelled else { return false }
            if let conflict = conflictMessage(for: error) {
                model.toast = Toast(conflict, style: .error)
                Haptics.error()
            } else {
                model.show(error)
            }
            return false
        }
    }

    /// A parent conflict can mean a newly formed cycle or too many levels; neither exposes the hub's English wording.
    nonisolated static func conflictMessage(for error: Error) -> String? {
        guard let error = error as? APIError, error.status == 409 else { return nil }
        return String(localized: "This link isn't possible. Choose something else.")
    }
}

/// The Same matter as… choices: open items from one manager, leaving out the item and everything known beneath it.
nonisolated enum InboxSameMatter {
    private static let maxParentDepth = 50

    static func candidates(items: [InboxItem], item: InboxItem, children: [InboxItem], query: String) -> [InboxItem] {
        let excluded = Set([item.id] + children.map(\.id))
        // Parent ids are local to a manager; another manager may reuse the same id for an unrelated item.
        var itemsByID: [String: InboxItem] = [:]
        for candidate in items where candidate.manager == item.manager {
            itemsByID[candidate.id] = candidate
        }
        let open = items.filter { candidate in
            !candidate.isDone && candidate.manager == item.manager && !excluded.contains(candidate.id)
                && !hasInboxAncestor(candidate, ancestorID: item.id, itemsByID: itemsByID)
        }
        let query = query.trimmingCharacters(in: .whitespacesAndNewlines)
        let matching = query.isEmpty ? open : open.filter { candidate in
            candidate.title.localizedCaseInsensitiveContains(query)
                || (candidate.`where` ?? "").localizedCaseInsensitiveContains(query)
        }
        return matching.sorted { a, b in
            let aa = a.updatedAt ?? a.startedAt ?? .distantPast
            let bb = b.updatedAt ?? b.startedAt ?? .distantPast
            return aa == bb ? a.id < b.id : aa > bb
        }
    }

    /// Follow only loaded inbox parents, stopping at corrupt cycles and bounding incomplete or malicious chains.
    private static func hasInboxAncestor(_ candidate: InboxItem, ancestorID: String,
                                         itemsByID: [String: InboxItem]) -> Bool {
        var seen: Set<String> = []
        var parent = candidate.parent
        for _ in 0..<maxParentDepth {
            guard let current = parent, current.kind == "inbox" else { return false }
            if current.id == ancestorID { return true }
            guard seen.insert(current.id).inserted else { return false }
            parent = itemsByID[current.id]?.parent
        }
        return false
    }

    /// Where and who, without repeating a direct message's name or borrowing the item's latest text.
    static func locationLine(_ item: InboxItem) -> String? {
        let place = (item.`where` ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        let sender = (item.sender ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if place.isEmpty { return sender.isEmpty ? nil : sender }
        if sender.isEmpty || place == sender { return place }
        return place + " · " + sender
    }
}

/// A system sheet for choosing the open matter under which this item belongs.
struct InboxSameMatterPicker: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    let item: InboxItem
    private let initialTree: InboxTree?

    @State private var query = ""
    @State private var tree: InboxTree?
    @State private var ready = false
    @State private var candidates: [InboxItem] = []
    @State private var busy: String?

    init(item: InboxItem, tree: InboxTree?) {
        self.item = item
        initialTree = tree
        _tree = State(initialValue: tree)
        _ready = State(initialValue: false)
    }

    var body: some View {
        NavigationStack {
            List {
                if !ready {
                    ProgressView()
                        .frame(maxWidth: .infinity)
                        .themedRow()
                } else if candidates.isEmpty {
                    Text(String(localized: "No open matters match"))
                        .font(Theme.Font.secondary)
                        .foregroundStyle(.secondary)
                        .themedRow()
                        .accessibilityIdentifier("inbox.sameMatter.empty")
                } else {
                    ForEach(candidates, id: \.key) { candidate in
                        row(candidate)
                            .themedRow()
                    }
                }
            }
            .themedGroupedPage()
            .pageSearchable(text: $query, prompt: String(localized: "Search titles and places"))
            .navigationTitle(String(localized: "Same matter as…"))
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: {
                        Text(String(localized: "Cancel")).lineLimit(1)
                    }
                    .accessibilityIdentifier("inbox.sameMatter.cancel")
                    .disabled(busy != nil)
                }
            }
            .task(id: item.key) { await loadTree() }
            .onChange(of: query) { _, _ in deriveCandidates() }
            // `itemsRevision` is intentionally unobserved; observing the snapshot invalidates this derived list.
            .onChange(of: InboxStore.shared.items) { _, items in deriveCandidates(from: items) }
        }
        .accessibilityIdentifier("inbox.sameMatter")
        .presentationDetents([.medium, .large])
        .interactiveDismissDisabled(busy != nil)
        #if os(macOS)
        .macSheet()
        #endif
    }

    private func row(_ candidate: InboxItem) -> some View {
        Button { pick(candidate) } label: {
            HStack(spacing: Theme.Space.m) {
                IconTile(Theme.Icon.inbox, tint: Theme.accent)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(verbatim: candidate.title)
                        .font(Theme.Font.body)
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                    if let location = InboxSameMatter.locationLine(candidate) {
                        Text(verbatim: location)
                            .font(Theme.Font.secondary)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if busy == candidate.key {
                    ProgressView().controlSize(.small)
                }
            }
            .padding(.vertical, Theme.Space.xs)
            .frame(maxWidth: .infinity, alignment: .leading)
            .tapTargetHeight()
            .contentShape(.rect)
            .accessibilityElement(children: .combine)
        }
        .disabled(busy != nil)
        .accessibilityIdentifier("inbox.sameMatter.row." + candidate.id)
    }

    private func loadTree() async {
        guard !Task.isCancelled else { return }
        if let initialTree, initialTree.node.kind == "inbox", initialTree.node.id == item.id {
            tree = initialTree
            ready = true
            deriveCandidates()
            return
        }
        ready = false
        candidates = []
        tree = nil
        if let api = model.api?.manager(model.itemManager(item.manager)) {
            do {
                let loaded = try await api.inboxTree("inbox", item.id)
                guard !Task.isCancelled else { return }
                tree = loaded
            } catch is CancellationError {
                return
            } catch {
                guard !Task.isCancelled else { return }
                tree = InboxTree(node: InboxParent(kind: "inbox", id: item.id))
            }
        } else {
            tree = InboxTree(node: InboxParent(kind: "inbox", id: item.id))
        }
        guard !Task.isCancelled else { return }
        ready = true
        deriveCandidates()
    }

    private func deriveCandidates(from items: [InboxItem]? = nil) {
        guard ready, let tree else {
            candidates = []
            return
        }
        candidates = InboxSameMatter.candidates(items: items ?? InboxStore.shared.items, item: item,
                                                children: tree.children, query: query)
    }

    private func pick(_ parent: InboxItem) {
        guard busy == nil else { return }
        busy = parent.key
        Task {
            let linked = await InboxParentChange.link(item, to: parent, model: model)
            busy = nil
            if linked { dismiss() }
        }
    }
}
