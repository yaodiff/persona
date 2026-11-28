import Foundation
import Observation
import PersonaWire
import UIKit

/// The unified inbox as the app knows it (docs/assistant/inbox.md): the hub's items, kept on disk so the list paints at once
/// (offline too), refreshed when the hub says `inbox` and on (re)connect. Marking done applies at once and is sent
/// in the background; a failure puts the item back.
@MainActor @Observable
final class InboxStore {
    private static var instance: InboxStore?
    private static weak var launchModel: AppModel?

    static var shared: InboxStore {
        if let instance { return instance }
        let store = InboxStore()
        instance = store
        if let launchModel { store.restoreLaunchCache(for: launchModel) }
        return store
    }

    /// AppModel's registry and links are ready. If counting badges already requested the inbox during
    /// startup, install its saved rows now; otherwise the first reader loads them as before.
    static func bindAfterLaunch(to model: AppModel) {
        launchModel = model
        instance?.restoreLaunchCache(for: model)
    }

    private(set) var items: [InboxItem] {
        didSet { itemsRevision &+= 1 }
    }
    /// A synchronous cache stamp for every assignment and in-place optimistic item mutation.
    @ObservationIgnored private(set) var itemsRevision: UInt64 = 0
    /// How triage stands on the hub (new items held back until the model sorts them), with every list; nil from an
    /// older hub, and until the first list of this run (it is not kept on disk: an old count must not show offline).
    private(set) var triage: InboxTriageStatus?
    /// Core's retained Lark/mail facts for this hub, kept only in memory; nil means unknown, not an empty archive.
    private(set) var storedSources: InboxStoredSources?
    private(set) var loading = false
    /// A listing came from the hub since launch: what is cached on disk may be from before a source was added.
    private(set) var listed = false
    var error: String?
    /// Identifies this visit, even when the owner switches back to the same hub.
    @ObservationIgnored private(set) var hubVisit: UInt64 = 0
    @ObservationIgnored private var reloading: Task<Void, Never>?
    private let cacheFolder: URL?
    @ObservationIgnored private var cacheOwner: () -> String?
    /// Done marks the hub may not have yet, by `InboxItem.key`, laid over every list that comes in (#2201): a list from
    /// before the hub took a mark must not bring a swiped-away item back.
    @ObservationIgnored private var marks = InboxMarks()
    /// The list on disk paints a cold start: written at most once a minute (3.3 MB on a busy hub, #4740), and when the
    /// app leaves. The cursor travels in this same record only while the items are exactly the hub list it names.
    @ObservationIgnored private let saver = Throttle(gap: .seconds(60))
    private static let stateName = "inbox-items"
    nonisolated struct Saved: Codable, Sendable {
        let items: [InboxItem]
        let cursor: String?
        let hub: URL?
        /// Independent of cursor eligibility: optimistically changed items still belong to their pairing.
        var owner: String? = nil

        /// The old record identified its source only alongside a clean cursor. Unattributed rows must be
        /// refreshed before they can be shown under a pairing's name.
        func forHub(group: String?, registry: HubRegistry, demo: Bool) -> Saved {
            let empty = Saved(items: [], cursor: nil, hub: nil)
            guard let group else { return empty }
            if let owner {
                if demo { return owner == "demo" ? self : empty }
                return registry.members(group).contains { owner == Self.owner(of: $0) } ? self : empty
            }
            guard !demo, let hub, cursor != nil else { return empty }
            let groups = Set(registry.hubs.filter { $0.config.url == hub }.map(\.group))
            return groups == [group] ? self : empty
        }

        static func owner(of pairing: PairedHub) -> String { "pairing:" + pairing.id }

        var storedCursor: (value: String, hub: URL)? {
            guard let cursor, let hub else { return nil }
            return (cursor, hub)
        }
    }
    /// The hub's tag of the list `items` came from (`ETag`, #4739): sent with the next reload, which then costs a 304
    /// while nothing changed. Good only for that hub and while nothing changed `items` here since (`itemsRevision`); not
    /// kept on disk, where a done mark the hub never got could outlive it.
    @ObservationIgnored private var listTag: (etag: String, hub: URL, revision: UInt64)?
    /// The cursor of the list held, for that hub (docs/protocol/protocol.md "Inbox delta", #4740): a reload asks for only
    /// what changed since. It is kept on disk with the items when they are exactly the hub list at this cursor.
    @ObservationIgnored private var cursor: (value: String, hub: URL)?
    /// The revision of the exact hub list `cursor` names; nil after a local edit or a delta built on an unclean base.
    @ObservationIgnored private var cursorRevision: UInt64?
    /// Hubs that answered without a cursor (older ones): they are asked with the tag instead.
    @ObservationIgnored private var withoutDeltas: Set<URL> = []

    init(saved: Saved, cacheFolder: URL?, cacheOwner: @escaping () -> String? = { nil }) {
        self.cacheFolder = cacheFolder
        self.cacheOwner = cacheOwner
        items = saved.items
        if !saved.items.isEmpty, let stored = saved.storedCursor {
            cursor = stored
            cursorRevision = itemsRevision
        }
    }

    private convenience init() {
        self.init(saved: .init(items: [], cursor: nil, hub: nil), cacheFolder: nil)
        let leaving = [UIApplication.didEnterBackgroundNotification, UIApplication.willTerminateNotification]
        for name in leaving {
            NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { @Sendable _ in
                MainActor.assumeIsolated { InboxStore.shared.saver.flush() }
            }
        }
    }

    private func restoreLaunchCache(for model: AppModel) {
        cacheOwner = { [weak model] in
            guard let model else { return nil }
            if model.isDemo { return "demo" }
            guard let group = model.hub?.group else { return nil }
            return model.registry.primary(group).map(Saved.owner(of:))
        }
        let saved = Self.loadSaved().forHub(group: model.hub?.group, registry: model.registry, demo: model.isDemo)
        guard !saved.items.isEmpty else { return }
        items = saved.items
        cursor = saved.storedCursor
        cursorRevision = cursor == nil ? nil : itemsRevision
        teachLarkSync()
    }

    /// The current record, or the bare item array written before cursors were kept. One key keeps each pair atomic.
    static func loadSaved(in folder: URL? = nil) -> Saved {
        if let saved = StateCache.load(Self.stateName, as: Saved.self, in: folder) { return saved }
        return Saved(items: StateCache.load(Self.stateName, as: [InboxItem].self, in: folder) ?? [], cursor: nil, hub: nil)
    }

    /// A locally edited or overlaid list deliberately has no cursor: the next launch then reads the whole hub list.
    static func makeSaved(items: [InboxItem], cursor: (value: String, hub: URL)?, cursorRevision: UInt64?,
                          itemsRevision: UInt64, marksEmpty: Bool, owner: String? = nil) -> Saved {
        let keep = !items.isEmpty && marksEmpty && cursorRevision == itemsRevision
        return Saved(items: items, cursor: keep ? cursor?.value : nil, hub: keep ? cursor?.hub : nil, owner: owner)
    }

    /// A delta certifies its result only when its exact base stayed unchanged while the request was out. A whole list
    /// replaces that base, so it needs only the absence of marks laid over it.
    static func listingIsCleanForDisk(isDelta: Bool, baseWasClean: Bool, baseUnchanged: Bool, marksEmpty: Bool) -> Bool {
        marksEmpty && (!isDelta || (baseWasClean && baseUnchanged))
    }

    static func since(cursor: (value: String, hub: URL)?, for hub: URL) -> String {
        cursor.flatMap { $0.hub == hub ? $0.value : nil } ?? InboxListing.noCursor
    }

    var open: [InboxItem] { items.filter { !$0.isDone } }

    /// The items the pages list: the current assistant's, on a hub with several (`AppModel.assistantScope`,
    /// docs/assistant/managers.md "Pages"). `items` stays whole, so another assistant's item still opens as itself from a
    /// notification or a link.
    var scopedItems: [InboxItem] {
        guard let scope = AppModel.shared.assistantScope else { return items }
        return items.filter { scope.has($0.manager) }
    }

    // MARK: Done's history (#2200)

    /// Done items older than the list's few days, from the hub's history (GET /api/v1/inbox/done), newest done first:
    /// the current assistant's, as the history is one manager's (its cursor is, docs/protocol/protocol.md "Paged lists").
    private(set) var history: [InboxItem] = []
    /// The hub may have more history: true until a page comes back without a next one, or a hub without it (404).
    private(set) var historyMore = true
    private(set) var historyLoading = false
    @ObservationIgnored private var historyNext: InboxDonePage.Cursor?
    /// Moves when the history starts over for another assistant (`scopeChanged`, `forget`): a page asked of the one
    /// before doesn't land.
    @ObservationIgnored private var historyEpoch = 0

    /// Whom Done's history is asked of, and what its items are stamped with: the current assistant, named unless it
    /// is the default (which a request without a manager reaches); no stamp with one manager.
    private var historyOwner: (api: APIClient, manager: String?)? { AppModel.shared.inboxHistoryOwner }

    /// One more page of Done's history (the first when Done is shown). A failure (offline) is tried again next time.
    func loadOlderDone() {
        guard historyMore, !historyLoading, let asked = historyOwner else { return }
        historyLoading = true
        let epoch = historyEpoch
        Task {
            defer { if epoch == historyEpoch { historyLoading = false } }
            do {
                let page = try await asked.api.inboxDone(after: historyNext, connectors: Self.drawsConnectors)
                guard epoch == historyEpoch else { return }
                let items = Self.stamped(page.items, manager: asked.manager)
                let known = Set(history.map(\.key))
                history += items.filter { !known.contains($0.key) }
                historyNext = page.next
                historyMore = page.next != nil
            } catch let e as APIError where e.status == 404 || e.status == 405 {
                if epoch == historyEpoch { historyMore = false } // a hub from before the history: Done shows what the list has
            } catch {}
        }
    }

    /// History items of the manager asked, stamped as the merged list stamps them, so an item in both is one key.
    nonisolated static func stamped(_ items: [InboxItem], manager: String?) -> [InboxItem] {
        guard let manager else { return items }
        return items.map { it in
            guard it.manager == nil else { return it }
            var it = it
            it.manager = manager
            return it
        }
    }

    /// Another assistant is current, or the hub's roster came or went (`AppModel.followScope`): Done's history and its
    /// day counts were the one before's. They go, a page of them still on its way doesn't land, and the day counts are
    /// read again for the one shown now, as when the list first came.
    func scopeChanged() {
        historyEpoch += 1
        history = []
        historyMore = true
        historyNext = nil
        historyLoading = false
        doneDays = nil
        groupLoading = []
        groupComplete = []
        groupNext = [:]
        if listed { loadDoneDays() }
    }

    // MARK: Done's day counts (#2969)

    /// The current assistant's done items per calendar day in this device's zone ("YYYY-MM-DD" → count; GET
    /// /api/v1/inbox/done/days, #2967), as the history is. nil: not known yet, or a hub without them.
    private(set) var doneDays: [String: Int]?
    /// Done groups loading a page of their own, by group key.
    private(set) var groupLoading: Set<String> = []
    /// Done groups whose items have all come: a page of theirs reached the group's start, or the history ended.
    private(set) var groupComplete: Set<String> = []
    @ObservationIgnored private var groupNext: [String: InboxDonePage.Cursor] = [:]

    /// The day counts, so Done lists every day's group with its count before any of its items load. A hub without them
    /// (404 / 405, or 400 for a zone it doesn't know) gets the history's first page instead, as before; offline, nothing
    /// changes and the next time Done shows asks again.
    func loadDoneDays() {
        guard let asked = historyOwner else { return }
        let epoch = historyEpoch
        Task {
            do {
                let days = try await asked.api.inboxDoneDays(zone: TimeZone.current.identifier, connectors: Self.drawsConnectors)
                if epoch == historyEpoch { doneDays = days }
            } catch let e as APIError where e.status == 404 || e.status == 405 || e.status == 400 {
                guard epoch == historyEpoch else { return }
                doneDays = nil
                if history.isEmpty { loadOlderDone() }
            } catch {}
        }
    }

    /// One page of a Done group's own items: from the group's end (a moment: the items done before it), then from where
    /// its last page ended. The group is complete when a page reaches its start or the history ends; items of older
    /// groups a page also brings stay in `history` for theirs.
    func loadDoneGroup(_ key: String, range: (start: Date?, end: Date?)) {
        guard !groupLoading.contains(key), !groupComplete.contains(key), let asked = historyOwner else { return }
        groupLoading.insert(key)
        let epoch = historyEpoch
        Task {
            defer { if epoch == historyEpoch { groupLoading.remove(key) } }
            do {
                let page = try await asked.api.inboxDone(after: groupNext[key], before: range.end, connectors: Self.drawsConnectors)
                guard epoch == historyEpoch else { return }
                let items = Self.stamped(page.items, manager: asked.manager)
                var complete = page.next == nil
                if let last = items.last?.doneAt, let start = range.start, last < start { complete = true }
                if items.last?.doneAt == nil { complete = true }
                groupNext[key] = complete ? nil : page.next
                if complete { groupComplete.insert(key) }
                let known = Set(history.map(\.key))
                history += items.filter { !known.contains($0.key) }
            } catch {}
        }
    }

    /// Move Back on an item only the history has (done before the list's days): the hub takes it back into its list.
    private func reopenFromHistory(_ item: InboxItem) {
        guard let api = AppModel.shared.api?.manager(item.manager), history.contains(where: { $0.key == item.key }) else { return }
        Haptics.tap()
        let epoch = historyEpoch
        Task {
            guard epoch == historyEpoch else { return }
            do {
                try await api.setInboxDone(item.id, false)
                guard epoch == historyEpoch else { return }
                history.removeAll { $0.key == item.key }
                reload()
            } catch {
                if epoch == historyEpoch { AppModel.shared.show(error) }
            }
        }
    }

    /// Switch away before another hub's first reload, including entering or leaving Demo Mode.
    func forget() {
        hubVisit &+= 1
        reloading?.cancel()
        reloading = nil
        loading = false
        listed = false
        listTag = nil
        marks = InboxMarks()
        sending.removeAll()
        moving.removeAll()
        unsentReplies.removeAll()
        cursor = nil
        cursorRevision = nil
        items = []
        scopeChanged()
        triage = nil
        storedSources = nil
        error = nil
        save()
    }

    /// The current assistant's open items that still need the user, including ones already read: the Inbox widget's
    /// Need You and the dot on the persona's face. The tab's badge and the sidebar's Inbox row count only the unread ones
    /// (`unreadCount`).
    var actionCount: Int { scopedItems.filter { !$0.isDone && $0.triage == .action }.count }

    /// Unread open items in Needs you. Counted once per change of the items or of the current assistant and kept: the
    /// sidebar's Inbox row reads it in its body, and a turn of an iPhone Duo in the background drew that body many times
    /// over; a Debug build filtering (and copying) every item each time ran out of the scene's 10 seconds (0x8BADF00D,
    /// #4206). Reading `items` and the scope here keeps the readers redrawn when either changes.
    var unreadCount: Int {
        let scope = AppModel.shared.assistantScope
        let revision = itemsRevision
        if let memo = unreadMemo, memo.revision == revision, memo.scope == scope { _ = items.count; return memo.count }
        var count = 0
        for item in items where item.isUnreadAction && (scope?.has(item.manager) ?? true) { count += 1 }
        unreadMemo = (revision, scope, count)
        return count
    }
    @ObservationIgnored private var unreadMemo: (revision: UInt64, scope: AssistantScope?, count: Int)?

    /// Pulls the list (bursts of `inbox` events collapse into one request).
    func reload() {
        guard let api = AppModel.shared.api else { return }
        reload(from: api)
    }

    @discardableResult
    func reload(from api: APIClient) -> Task<Void, Never> {
        if let reloading { return reloading }
        let visit = hubVisit
        let task = Task {
            guard visit == hubVisit else { return }
            defer { if visit == hubVisit { reloading = nil } }
            await fetchListing(api, visit: visit)
        }
        reloading = task
        return task
    }

    /// `reload`'s request: with the held list's tag, so a list the hub still has costs a 304, nothing decoded or written.
    private func fetchListing(_ api: APIClient, visit: UInt64) async {
        let hub = api.config.url
        let held = listTag.flatMap { $0.hub == hub && $0.revision == itemsRevision ? $0.etag : nil }
        let baseRevision = itemsRevision
        let baseWasClean = cursorRevision == baseRevision && marks.isEmpty && cursor?.hub == hub
        loading = true
        let seq = marks.beginList()
        defer {
            if visit == hubVisit {
                loading = false
                marks.endList(seq)
            }
        }
        do {
            if withoutDeltas.contains(hub) {
                let answer = try await api.inboxListing(connectors: Self.drawsConnectors, etag: held)
                guard visit == hubVisit else { return }
                if let listing = answer.listing {
                    apply(marks.lay(listing.items, seq: seq))
                    if listing.triage != triage { triage = listing.triage }
                    if storedSources != listing.storedSources { storedSources = listing.storedSources }
                }
                listTag = answer.etag.map { (etag: $0, hub: hub, revision: itemsRevision) }
            } else {
                let since = Self.since(cursor: cursor, for: hub)
                let listing = try await api.inboxListing(connectors: Self.drawsConnectors, since: since)
                guard visit == hubVisit else { return }
                let isDelta = listing.delta == true && since != InboxListing.noCursor
                let baseUnchanged = itemsRevision == baseRevision && cursor?.hub == hub && cursor?.value == since
                let clean = Self.listingIsCleanForDisk(isDelta: isDelta, baseWasClean: baseWasClean,
                                                       baseUnchanged: baseUnchanged, marksEmpty: marks.isEmpty)
                if isDelta {
                    apply(marks.lay(listing.applied(to: items), seq: seq))
                } else {
                    apply(marks.lay(listing.items, seq: seq))
                }
                if storedSources != listing.storedSources { storedSources = listing.storedSources }
                if listing.triage != triage { triage = listing.triage }
                let oldCursor = cursor
                let oldRevision = cursorRevision
                if let next = listing.cursor {
                    cursor = (next, hub)
                    cursorRevision = clean ? itemsRevision : nil
                } else { // a hub without deltas: the tag from now on
                    cursor = nil
                    cursorRevision = nil
                    withoutDeltas.insert(hub)
                }
                // apply can precede the cursor decision, and an unchanged legacy list has no save of its own.
                if oldCursor?.value != cursor?.value || oldCursor?.hub != cursor?.hub || oldRevision != cursorRevision {
                    save()
                }
            }
            listed = true
            error = nil
        } catch {
            if visit == hubVisit, let shown = error.shown { self.error = shown }
        }
    }

    /// Pull to refresh: the hub collects Feishu and mail now. Its whole list is none a cursor names: the next reload asks
    /// for a whole one again.
    /// With another assistant than the default current it is that one that collects: named, it answers with its own
    /// list alone, so the merged list is read after it.
    func refresh() async {
        guard let api = AppModel.shared.api else { return }
        let visit = hubVisit
        cursor = nil
        cursorRevision = nil
        save()
        if let manager = AppModel.shared.assistantManagerID {
            // A collection that failed must not look like one that worked (the hub answers 502 for it): its words show
            // as a failed refresh's do. An offline assistant is said on the page itself.
            var failed: String?
            do {
                _ = try await api.manager(manager).refreshInbox(connectors: Self.drawsConnectors)
            } catch {
                if (error as? APIError)?.code != "assistant_offline" { failed = error.shown }
            }
            guard visit == hubVisit else { return }
            await fetchListing(api, visit: visit)
            if visit == hubVisit, let failed { self.error = failed }
        } else {
            await fetch(api, visit: visit)
        }
    }

    /// The connectors' items (GitHub…) are drawn on iOS and visionOS (#2611, #2736); the watch fetches its own (WatchModel, #2727).
    private static let drawsConnectors = true

    private func fetch(_ api: APIClient, visit: UInt64) async {
        loading = true
        let seq = marks.beginList()
        defer {
            if visit == hubVisit {
                loading = false
                marks.endList(seq)
            }
        }
        do {
            let listing = try await api.refreshInbox(connectors: Self.drawsConnectors)
            guard visit == hubVisit else { return }
            apply(marks.lay(listing.items, seq: seq))
            if storedSources != listing.storedSources { storedSources = listing.storedSources }
            listed = true
            if listing.triage != triage { triage = listing.triage }
            error = nil
        } catch {
            if visit == hubVisit, let shown = error.shown { self.error = shown }
        }
    }

    /// Capture items and their cursor eligibility together before the throttle retains the write.
    private func save() {
        let saved = Self.makeSaved(items: items, cursor: cursor, cursorRevision: cursorRevision,
                                   itemsRevision: itemsRevision, marksEmpty: marks.isEmpty, owner: cacheOwner())
        saver.run { [cacheFolder] in StateCache.save(Self.stateName, saved, in: cacheFolder) }
    }

    private func apply(_ list: [InboxItem]) {
        guard list != items else { return }
        items = list
        teachLarkSync()
        save()
        AppModel.shared.summariesChanged()
    }

    /// Mark All Seen / Clear All (a section), Mac's multi-select, and every Undo: one request for them all on the Done request
    /// (docs/assistant/inbox.md「Clean up all」), one per manager when they are several managers'. Shown at once; the hub files each
    /// as the list showed it, so one that took new messages since stays open (it goes back here too), and a failure puts
    /// back what that request held. Filing ends with a toast and its Undo, which brings back what was filed.
    func setDone(_ list: [InboxItem], _ done: Bool, undoable: Bool = true) {
        guard let api = AppModel.shared.api else { return }
        let roster = AppModel.shared.managerRoster
        run(list, done: done, action: .done) { targets in
            var sent = try await InboxBulk.byManager(Self.managers(targets)) { manager, mine in
                try await self.fileDone(mine, done, api: api.manager(roster.map { $0.naming(manager) } ?? manager))
            }
            if undoable, !sent.result.changed.isEmpty { sent.undo = .legacy(sent.result.changed) }
            return sent
        }
    }

    /// Clear All (the view's open items, its filters and search): filed as dismissed in the hub's bulk request
    /// (`POST /api/v1/inbox/bulk`, docs/assistant/inbox.md「Clean up all」); on a hub without it the Done request instead (recorded as
    /// handled there). Same as above: shown at once, put back on a failure, a toast whose Undo sends the hub's `undo`.
    func clean(_ list: [InboxItem]) {
        guard let api = AppModel.shared.api else { return }
        let roster = AppModel.shared.managerRoster
        let action = InboxBulk.Action.dismiss
        run(list, done: true, action: action) { targets in
            let groups = Self.managers(targets)
            if groups.count == 1, let g = groups.first {
                let api = api.manager(roster.map { $0.naming(g.key) } ?? g.key)
                let (result, undo) = try await InboxBulk.perform(action, targets, manager: g.key,
                                                                 bulk: { try await api.bulkInbox($0.rawValue, $1) },
                                                                 legacy: { try await self.fileDone($0, true, api: api) })
                return InboxBulk.Sent(result: result, undo: undo)
            }
            // Several managers' items: each its own request. Each manager keeps its own `undo`, so Undo brings back what
            // moved with the Done request.
            var sent = try await InboxBulk.byManager(groups) { manager, mine in
                let api = api.manager(roster.map { $0.naming(manager) } ?? manager)
                return try await InboxBulk.perform(action, mine, bulk: { try await api.bulkInbox($0.rawValue, $1) },
                                                   legacy: { try await self.fileDone($0, true, api: api) }).result
            }
            if !sent.result.changed.isEmpty { sent.undo = .legacy(sent.result.changed) }
            return sent
        }
    }

    private func run(_ list: [InboxItem], done: Bool, action: InboxBulk.Action,
                     send: @escaping ([InboxItem]) async throws -> InboxBulk.Sent) {
        let current = Dictionary(items.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        let targets = InboxBulk.targets(list.compactMap { current[$0.key] }, done: done)
        guard !targets.isEmpty else { return }
        let before = Dictionary(targets.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        let now = Date.now
        items = InboxBulk.marking(items, keys: Set(before.keys), done: done, now: now, by: action)
        let tokens = before.keys.map { ($0, marks.put($0, .init(doneAt: done ? now : nil, doneBy: done ? action.doneBy : nil))) }
        done ? Haptics.success() : Haptics.tap()
        let visit = hubVisit
        Task {
            guard visit == hubVisit else { return }
            do {
                let sent = try await send(targets)
                guard visit == hubVisit else { return }
                items = InboxBulk.settle(items, before: before, result: sent.result)
                // The managers that failed: their items as they were; the others' stay filed (#1850).
                let lost = Set(sent.lost.map(\.key))
                // taken: what it moved and what was already so; the ones it left (new messages, gone) or a failed
                // manager's are as the hub has them
                let left = Set(sent.result.skipped.map(\.key)).union(lost)
                let fresh = Dictionary(sent.result.changed.map { ($0.key, $0) }, uniquingKeysWith: { _, b in b })
                for (key, token) in tokens { marks.settle(key, token: token, taken: !left.contains(key), hub: fresh[key]) }
                if !lost.isEmpty { items = InboxBulk.rollback(items, before: before.filter { lost.contains($0.key) }) }
                save()
                if InboxBulk.changedSince(sent.result) > 0 { reload() }
                let button = sent.undo.map { u in Toast.Action(title: String(localized: "Undo")) { [weak self] in
                    guard let self, visit == self.hubVisit else { return }
                    self.undo(u, done: done)
                } }
                let head = InboxBulk.message(done: done, result: sent.result, action: action)
                if let failure = sent.failure, !lost.isEmpty {
                    AppModel.shared.toast = Toast(InboxBulk.partly(head, lost: lost.count, error: failure), style: .error, action: button)
                    Haptics.error()
                } else {
                    AppModel.shared.toast = Toast(head, style: .success, action: button)
                }
            } catch {
                guard visit == hubVisit else { return }
                for (key, token) in tokens { marks.settle(key, token: token, taken: false) }
                items = InboxBulk.rollback(items, before: before.filter { !marks.out($0.key) })
                AppModel.shared.show(error)
            }
        }
    }

    /// The toast's Undo: the hub's `undo` as it came (only what that request changed) to the manager that answered it,
    /// or the Done request back.
    private func undo(_ undo: InboxBulk.Undo, done: Bool) {
        switch undo {
        case .legacy(let moved):
            setDone(moved, !done, undoable: false)
        case .bulk(let u, let manager):
            guard let api = AppModel.shared.api?.manager(AppModel.shared.itemManager(manager)) else { return }
            let keys = Set(u.ids.map { InboxItem.key(id: $0, manager: manager) })
            run(items.filter { keys.contains($0.key) }, done: false, action: .done) { _ in
                InboxBulk.Sent(result: InboxBulk.result(try await api.undoInboxBulk(u)).stamped(manager))
            }
        }
    }

    /// Items by the manager each belongs to (docs/assistant/managers.md): a manager knows only its own, so each gets its
    /// own request.
    nonisolated static func managers(_ items: [InboxItem]) -> [(key: String?, items: [InboxItem])] {
        var order: [String?] = []
        var by: [String?: [InboxItem]] = [:]
        for it in items {
            if by[it.manager] == nil { order.append(it.manager) }
            by[it.manager, default: []].append(it)
        }
        return order.map { ($0, by[$0] ?? []) }
    }

    /// The Done request for many, one at a time on a hub from before it.
    private func fileDone(_ targets: [InboxItem], _ done: Bool, api: APIClient) async throws -> InboxBulkResult {
        do {
            return try await api.setInboxDone(targets, done)
        } catch let e as APIError where e.status == 404 || e.status == 405 {
            return try await setEach(targets, done, api: api) // a hub from before the bulk request
        }
    }

    /// The bulk request one item at a time, for an older hub: no versions there, so each is filed as it is.
    private func setEach(_ targets: [InboxItem], _ done: Bool, api: APIClient) async throws -> InboxBulkResult {
        var changed: [InboxItem] = []
        for it in targets {
            try await api.setInboxDone(it.id, done)
            var moved = it
            moved.doneAt = done ? .now : nil
            moved.doneBy = done ? "user" : nil
            changed.append(moved)
        }
        return InboxBulkResult(changed: changed, unchanged: [], skipped: [])
    }

    func setDone(_ item: InboxItem, _ done: Bool, haptic: Bool = true, api injected: APIClient? = nil) {
        guard let i = items.firstIndex(where: { $0.key == item.key }) else {
            if !done { reopenFromHistory(item) }
            return
        }
        let before = items[i]
        items[i].doneAt = done ? .now : nil
        items[i].doneBy = done ? "user" : nil
        let key = item.key
        let token = marks.put(key, .init(doneAt: items[i].doneAt, doneBy: items[i].doneBy))
        if haptic { Haptics.tap() }
        let visit = hubVisit
        let api = injected ?? AppModel.shared.api?.manager(AppModel.shared.itemManager(item.manager))
        Task {
            do {
                try await api?.setInboxDone(item.id, done)
                guard visit == hubVisit else { return }
                marks.settle(key, token: token, taken: true)
            } catch {
                guard visit == hubVisit else { return }
                marks.settle(key, token: token, taken: false)
                // back to the done state it had, on the copy now shown (a list meanwhile may be fresher), unless a newer mark is out
                if !marks.out(key), let j = items.firstIndex(where: { $0.key == key }) {
                    items[j].doneAt = before.doneAt
                    items[j].doneBy = before.doneBy
                }
                AppModel.shared.show(error)
            }
        }
    }

    /// Files an outbox-restored hand-off whose item may not be listed yet. The optimistic path is used when it is;
    /// otherwise the same single-item request goes directly to its manager.
    func setDone(id: String, manager: String?, done: Bool, api: APIClient) {
        if let item = items.first(where: { $0.key == InboxItem.key(id: id, manager: manager) }) {
            setDone(item, done, haptic: false, api: api)
            return
        }
        let visit = hubVisit
        Task {
            do { try await api.setInboxDone(id, done) }
            catch { if visit == hubVisit { AppModel.shared.show(error) } }
        }
    }

    /// Mute This Repository / Session / Sender (the row's menu): the item and the others of its source it covers are done at once;
    /// the hub keeps the mute, so later ones do not come in. A failure puts them back.
    func mute(_ item: InboxItem, _ option: InboxItem.MuteOption) {
        let before = items
        let covers = { (it: InboxItem) in
            it.source == item.source && it.manager == item.manager && !it.isDone && (it.mutes ?? []).contains { $0.tag == option.tag && $0.value == option.value }
        }
        var tokens: [(String, Int)] = []
        for i in items.indices where covers(items[i]) {
            items[i].doneAt = .now
            items[i].doneBy = "read"
            items[i].reason = String(localized: "Muted: \(option.name)")
            tokens.append((items[i].key, marks.put(items[i].key, .init(doneAt: items[i].doneAt, doneBy: "read", reason: items[i].reason))))
        }
        Haptics.success()
        let visit = hubVisit
        Task {
            guard visit == hubVisit else { return }
            do {
                _ = try await AppModel.shared.api?.manager(AppModel.shared.itemManager(item.manager)).muteInbox(item.id, tag: option.tag)
                guard visit == hubVisit else { return }
                for (key, token) in tokens { marks.settle(key, token: token, taken: true) }
                AppModel.shared.toast = Toast(String(localized: "Muted “\(option.name)”. It won’t come to the Inbox anymore."), style: .success)
            } catch {
                guard visit == hubVisit else { return }
                for (key, token) in tokens { marks.settle(key, token: token, taken: false) }
                // back to how they were, on the copies now shown, unless a newer mark is out
                let was = Dictionary(before.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
                let marked = Set(tokens.map(\.0))
                items = items.map { it in
                    guard marked.contains(it.key), let w = was[it.key], !marks.out(it.key) else { return it }
                    var it = it
                    it.doneAt = w.doneAt
                    it.doneBy = w.doneBy
                    it.reason = w.reason
                    return it
                }
                AppModel.shared.show(error)
            }
        }
    }

    /// A rule's tier shown on the items it reaches before the hub answers, and put back as they were when it fails
    /// (InboxPolicyModel): items are matched by `InboxItem.key`, ones that left the list meanwhile stay out.
    func showRuleChange(_ changed: [InboxItem]) {
        guard !changed.isEmpty else { return }
        let byKey = Dictionary(changed.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        items = items.map { byKey[$0.key] ?? $0 }
    }

    /// Items whose reply is being sent from the list, by `InboxItem.key` (the row shows a spinner, a second tap does nothing).
    private(set) var sending: Set<String> = []

    /// Replies that did not go out, by `InboxItem.key`: the reply sheet opens with that text again.
    var unsentReplies: [String: String] = [:]

    /// Send on the AI Twin's draft: the hub replies in the chat / by mail / in the document (moving the rows' status
    /// in `changes` first) and files the item as done. Filed at once (the reply takes Feishu / the mail server a few
    /// seconds); a failure puts the item back and keeps the text for the reply sheet. `card`: sent from that message card.
    @discardableResult
    func send(_ item: InboxItem, text: String, changes: [InboxItem.DocChange] = [], card: String? = nil,
              api injected: APIClient? = nil) async -> Bool {
        let key = item.key
        guard let api = injected ?? AppModel.shared.api?.manager(AppModel.shared.itemManager(item.manager)), !sending.contains(key) else { return false }
        let visit = hubVisit
        sending.insert(key)
        defer { if visit == hubVisit { sending.remove(key) } }
        let before = items.first { $0.key == key }
        if let i = items.firstIndex(where: { $0.key == key }) {
            items[i].doneAt = .now
            items[i].doneBy = "replied"
        }
        let token = marks.put(key, .init(doneAt: .now, doneBy: "replied"))
        unsentReplies[key] = nil
        Haptics.tap()
        do {
            try await api.inboxReply(item.id, text: text, changes: changes, card: card)
            guard visit == hubVisit else { return false }
            marks.settle(key, token: token, taken: true)
            Haptics.success()
            AppModel.shared.toast = Toast(String(localized: "Sent and marked as done"), style: .success)
            return true
        } catch {
            guard visit == hubVisit else { return false }
            marks.settle(key, token: token, taken: false)
            if let before, !marks.out(key), let i = items.firstIndex(where: { $0.key == key }) {
                items[i].doneAt = before.doneAt
                items[i].doneBy = before.doneBy
            }
            if !text.isEmpty { unsentReplies[key] = text }
            if let shown = error.shown {
                AppModel.shared.toast = Toast(String(localized: "Reply not sent: \(shown)"), style: .error)
                Haptics.error()
            }
            return false
        }
    }

    // MARK: Merge and Add to To-Dos (#6480)

    /// What a merge or a to-do may act on: the open rows the list has now, in the list's order.
    nonisolated static func actionable(_ list: [InboxItem], in items: [InboxItem]) -> [InboxItem] {
        let keys = Set(list.map(\.key))
        return items.filter { keys.contains($0.key) && !$0.isDone }
    }

    /// Merge needs two or more open rows of one manager (a manager knows only its own).
    nonisolated static func canMerge(_ list: [InboxItem]) -> Bool {
        let open = list.filter { !$0.isDone }
        return open.count >= 2 && Set(open.map(\.manager)).count == 1
    }

    /// Merge: the newest row stands for the rest; the members leave the list when the hub answers, and the toast has Undo.
    /// `done` runs once it went through (selecting ends).
    func merge(_ list: [InboxItem], done: @escaping @MainActor () -> Void = {}) {
        let targets = Self.actionable(list, in: items)
        guard Self.canMerge(targets), let first = targets.first, let api = AppModel.shared.api else { return }
        let manager = first.manager
        let visit = hubVisit
        Task {
            guard visit == hubVisit else { return }
            await runMerge(targets, manager: manager, api: api.manager(AppModel.shared.itemManager(manager)), visit: visit, done: done)
        }
    }

    private func runMerge(_ targets: [InboxItem], manager: String?, api: APIClient, visit: UInt64, done: @MainActor () -> Void) async {
        do {
            let r = try await api.mergeInbox(targets)
            guard visit == hubVisit else { return }
            var survivor = r.item
            if survivor.manager == nil { survivor.manager = manager }
            if survivor.version == nil { survivor.version = items.first { $0.key == survivor.key }?.version }
            let gone = Set(r.members.map { InboxItem.key(id: $0, manager: manager) })
            items = items.filter { !gone.contains($0.key) }.map { $0.key == survivor.key ? survivor : $0 }
            save()
            Haptics.success()
            let button = r.undoID.map { id in Toast.Action(title: String(localized: "Undo")) { [weak self] in
                guard let self, visit == self.hubVisit else { return }
                self.undoMerge(id, manager: manager)
            } }
            AppModel.shared.toast = Toast(String(localized: "Merged \(r.members.count + 1) rows into one"), style: .success, action: button)
            done()
        } catch {
            guard visit == hubVisit else { return }
            AppModel.shared.show(error)
            reload()
        }
    }

    private func undoMerge(_ id: String, manager: String?) {
        guard let api = AppModel.shared.api?.manager(AppModel.shared.itemManager(manager)) else { return }
        let visit = hubVisit
        Task {
            guard visit == hubVisit else { return }
            do {
                let changed = try await api.undoInboxMerge(id)
                guard visit == hubVisit else { return }
                for var it in changed {
                    if it.manager == nil { it.manager = manager }
                    if let i = items.firstIndex(where: { $0.key == it.key }) { items[i] = it }
                }
                reload() // the members are listed again
                AppModel.shared.toast = Toast(String(localized: "Split into separate rows again"), style: .success)
            } catch {
                guard visit == hubVisit else { return }
                AppModel.shared.show(error)
            }
        }
    }

    /// Add to To-Dos: one to-do per row, all with the same due. The rows count as handled (they leave the open list when the
    /// hub answers), and the toast has Undo. `done` runs once it went through (selecting ends).
    func addTodos(_ list: [InboxItem], due: InboxDue, done: @escaping @MainActor () -> Void = {}) {
        let targets = Self.actionable(list, in: items)
        guard !targets.isEmpty, let api = AppModel.shared.api else { return }
        let visit = hubVisit
        Task {
            guard visit == hubVisit else { return }
            var made: [(manager: String?, made: [InboxTodoReply.Made])] = []
            var failure: Error?
            var count = 0
            for (manager, mine) in Self.managers(targets).map({ ($0.key, $0.items) }) {
                do {
                    let r = try await api.manager(AppModel.shared.itemManager(manager)).todoInbox(mine, due: due)
                    guard visit == hubVisit else { return }
                    for var it in r.changed {
                        if it.manager == nil { it.manager = manager }
                        if let i = items.firstIndex(where: { $0.key == it.key }) {
                            if it.version == nil { it.version = items[i].version }
                            items[i] = it
                        }
                    }
                    count += r.todos.count
                    if !r.todos.isEmpty { made.append((manager, r.todos)) }
                } catch {
                    guard visit == hubVisit else { return }
                    failure = error
                }
            }
            save()
            if let failure {
                AppModel.shared.show(failure)
                Haptics.error()
                reload()
            }
            guard count > 0, failure == nil else { return }
            Haptics.success()
            let button = Toast.Action(title: String(localized: "Undo")) { [weak self] in
                guard let self, visit == self.hubVisit else { return }
                self.undoTodos(made)
            }
            AppModel.shared.toast = Toast(String(localized: "Added \(count) to your to-dos"), style: .success, action: button)
            done()
        }
    }

    private func undoTodos(_ groups: [(manager: String?, made: [InboxTodoReply.Made])]) {
        guard let api = AppModel.shared.api else { return }
        let visit = hubVisit
        Task {
            guard visit == hubVisit else { return }
            do {
                for g in groups {
                    let changed = try await api.manager(AppModel.shared.itemManager(g.manager)).undoInboxTodo(g.made)
                    guard visit == hubVisit else { return }
                    for var it in changed {
                        if it.manager == nil { it.manager = g.manager }
                        if let i = items.firstIndex(where: { $0.key == it.key }) { items[i] = it }
                    }
                }
                save()
                AppModel.shared.toast = Toast(String(localized: "Taken out of your to-dos"), style: .success)
            } catch {
                guard visit == hubVisit else { return }
                AppModel.shared.show(error)
                reload()
            }
        }
    }

    /// The hub's copy of one item, as an answer of `manager` (the one asked, the item's stamp) returned it (acting on its
    /// suggestion): replaces the list's, kept on disk.
    func put(_ item: InboxItem, manager: String?) {
        // An answer carries no `version` of the listing, nor the manager its list stamped: keep the ones listed, so a
        // bulk Done still sees the version and what is done to it still goes to its manager.
        var fresh = item
        if fresh.manager == nil { fresh.manager = manager }
        guard let i = items.firstIndex(where: { $0.key == fresh.key }) else { return }
        if fresh.version == nil { fresh.version = items[i].version }
        guard items[i] != fresh else { return }
        items[i] = fresh
        save()
    }

    /// Status moves being written, by "<item key>|<row>|<field>" (the menu shows a spinner).
    private(set) var moving: Set<String> = []

    /// Moves a row's status in its document: shown at once, written by the hub, the item replaced by the hub's fresh
    /// read; a failure puts the old value back.
    func setDocStatus(_ item: InboxItem, row: InboxItem.DocRow, status: InboxItem.DocStatus, to value: String) {
        guard value != status.value, let api = AppModel.shared.api?.manager(AppModel.shared.itemManager(item.manager)) else { return }
        let key = item.key + "|" + row.ref + "|" + status.field
        guard !moving.contains(key), let i = items.firstIndex(where: { $0.key == item.key }) else { return }
        let before = items[i]
        items[i].doc?.set(row: row.ref, field: status.field, to: value)
        moving.insert(key)
        Haptics.tap()
        let visit = hubVisit
        Task {
            guard visit == hubVisit else { return }
            defer { if visit == hubVisit { moving.remove(key) } }
            do {
                var fresh = try await api.inboxDocStatus(item.id, .init(ref: row.ref, field: status.field, value: value))
                guard visit == hubVisit else { return }
                if fresh.manager == nil { fresh.manager = item.manager }
                if let j = items.firstIndex(where: { $0.key == item.key }) { items[j] = fresh }
                save()
                AppModel.shared.toast = Toast(String(localized: "Changed “\(status.name)” to \(value)"), style: .success)
            } catch {
                guard visit == hubVisit else { return }
                if let j = items.firstIndex(where: { $0.key == item.key }) { items[j] = before }
                AppModel.shared.show(error)
            }
        }
    }

    /// Feishu chats in the inbox are the ones background sync keeps fresh (their pages open at once).
    private func teachLarkSync() {
        LarkSync.shared.know(items.compactMap(\.larkConversation))
    }
}

extension InboxItem.Doc {
    mutating func set(row ref: String, field: String, to value: String) {
        guard let r = rows?.firstIndex(where: { $0.ref == ref }), let f = rows?[r].status?.firstIndex(where: { $0.field == field }) else { return }
        rows?[r].status?[f].value = value
    }

    /// The row and column a change names, for showing it ("Status: Active → Done").
    func describe(_ change: InboxItem.DocChange) -> (row: InboxItem.DocRow, status: InboxItem.DocStatus)? {
        guard let row = rows?.first(where: { $0.ref == change.ref }), let st = row.status?.first(where: { $0.field == change.field }) else { return nil }
        return (row, st)
    }
}

extension InboxItem {
    /// The Feishu chat to open, at the first new message.
    var larkConversation: LarkConversation? {
        guard let l = lark else { return nil }
        var c = LarkConversation(id: l.feedID, name: title, avatar: avatar, isGroup: l.isGroup, isTopic: l.isTopic,
                                 unread: isDone ? 0 : count, atMe: kind == "mention")
        c.avatarURL = avatarURL
        c.updatedAt = updatedAt
        c.readPosition = l.readPosition
        c.account = l.olAccount
        // 「飞书 · <name>」 while the hub reads more than one account.
        if let name = accountName.split(separator: " · ", maxSplits: 1).dropFirst().first { c.accountName = String(name) }
        if !isDone, let p = l.startPosition { c.focus = p }
        return c
    }

    /// The newest mail of the thread, for the mail reader.
    var mailSummary: MailSummary? {
        guard let m = mail, let uid = m.uids.last else { return nil }
        return MailSummary(accountID: m.accountID, mailbox: m.sourceMailbox ?? "INBOX", uid: uid, messageID: m.messageID, subject: title,
                           from: m.from, date: updatedAt, snippet: preview, seen: isDone || read == true)
    }
}

extension MailSummary {
    init(accountID: String, mailbox: String, uid: UInt32, messageID: String?, subject: String?, from: MailAddress?, date: Date?,
         snippet: String?, seen: Bool) {
        self.accountID = accountID
        self.mailbox = mailbox
        self.uid = uid
        self.messageID = messageID
        self.subject = subject
        self.from = from
        self.to = nil
        self.date = date
        self.snippet = snippet
        self.seen = seen
        flagged = false
        hasAttachments = false
    }
}
