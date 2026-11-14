import Foundation

/// One filtered inbox snapshot. Its rows retain the hub's order; only Done has a separate display order.
@MainActor
final class InboxProjection {
    nonisolated struct Filter: Equatable {
        var source = ""
        var kind = ""
        var query = ""
        /// The current assistant, on a hub with several (`AppModel.assistantScope`): only its items are in the snapshot,
        /// its source chips included.
        var scope: AssistantScope?
        // Searching and the Moments @Me alias use the current localization, not a cached language's result.
        var localeIdentifier = Locale.current.identifier
        var mentionLabel = String(localized: "@Me")

        func matches(_ item: InboxItem) -> Bool {
            if !source.isEmpty && item.account != source { return false }
            if !kind.isEmpty && !(item.kind == kind || kind == "group" && item.kind == "bot" || kind == "mention" && (item.kind == "doc" || item.kind == "moment" && item.kindLabel == mentionLabel)) { return false }
            if query.isEmpty { return true }
            return item.title.localizedCaseInsensitiveContains(query)
                || (item.where ?? "").localizedCaseInsensitiveContains(query)
                || (item.sender ?? "").localizedCaseInsensitiveContains(query)
                || item.preview.localizedCaseInsensitiveContains(query)
                || (item.summary ?? "").localizedCaseInsensitiveContains(query)
        }
    }

    nonisolated struct Source: Identifiable, Equatable, Sendable {
        let id: String
        let name: String
    }

    let openItems: [InboxItem]
    let byTier: [InboxItem.Tier: [InboxItem]]
    let doneItems: [InboxItem]
    let connectorSources: [Source]
    // Closed Done sections never sort. Expanding, closing and expanding again reuse this snapshot's order.
    private(set) lazy var sortedDoneItems = doneItems.sorted {
        ($0.doneAt ?? .distantPast) > ($1.doneAt ?? .distantPast)
    }

    init(items: [InboxItem], filter: Filter) {
        var open: [InboxItem] = []
        var tiers: [InboxItem.Tier: [InboxItem]] = [:]
        var done: [InboxItem] = []
        var sources: [Source] = []
        var seen = Set<String>()
        for item in items where filter.scope.has(item.manager) {
            // Source chips include archived and currently filtered-out items, first occurrence wins.
            if item.connector != nil && seen.insert(item.account).inserted {
                sources.append(Source(id: item.account, name: item.accountName))
            }
            guard filter.matches(item) else { continue }
            if item.isDone {
                done.append(item)
            } else {
                open.append(item)
                tiers[item.triage, default: []].append(item)
            }
        }
        openItems = open
        byTier = tiers
        doneItems = done
        connectorSources = sources
    }
}

/// Page-local, unobserved derived data: lookup is synchronous so edits, optimistic filing and rollback paint immediately.
@MainActor
final class InboxProjectionMemo {
    private var items: [InboxItem] = []
    private var filter: InboxProjection.Filter?
    private var projection: InboxProjection?
    private var revision: UInt64?

    func callAsFunction(_ items: [InboxItem], filter: InboxProjection.Filter, revision: UInt64? = nil) -> InboxProjection {
        if let projection, self.filter == filter {
            if let revision, self.revision == revision { return projection }
            if revision == nil, self.revision == nil, self.items == items {
                self.items = items
                return projection
            }
        }
        let fresh = InboxProjection(items: items, filter: filter)
        // The store's revision avoids retaining its COW storage or scanning unchanged payloads on redraw.
        // Standalone callers without a complete mutation stamp retain the full-value comparison fallback.
        self.items = revision == nil ? items : []
        self.revision = revision
        self.filter = filter
        projection = fresh
        return fresh
    }
}

/// The inbox's sections as the list draws them (the current assistant's items, `InboxProjection.Filter.scope`): Needs
/// You with its waiting agents first, In Progress, FYI, Collapsed, and Done.
nonisolated struct InboxShare {
    var byTier: [InboxItem.Tier: [InboxItem]]
    var agents: [Interaction]
    /// Its done items in the list's order (a page sorts them only while the section is open).
    var done: [InboxItem]
}

/// Which inbox sections are open: Needs You, In Progress and FYI start open, Collapsed and Done closed. Only what the
/// user flipped is kept.
nonisolated struct InboxFolds: Equatable {
    private static let startOpen: Set<String> = ["action", "working", "fyi"]
    private var flipped: Set<String> = []

    /// `section`: a tier or "done".
    func isOpen(_ section: String) -> Bool {
        guard InboxItem.Tier(rawValue: section)?.folds != false else { return true } // In Progress stays open
        return Self.startOpen.contains(section) != flipped.contains(section)
    }

    mutating func toggle(_ section: String) {
        guard InboxItem.Tier(rawValue: section)?.folds != false else { return }
        if flipped.contains(section) { flipped.remove(section) } else { flipped.insert(section) }
    }
}
