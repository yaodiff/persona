import CryptoKit
import Foundation
import Observation

/// The browser shares the inbox's routes and chat UI, with one account-bound list snapshot.
@MainActor @Observable
final class LarkConversationsModel {
    enum Filter: String { case all, unread }

    nonisolated struct Snapshot: Codable, Sendable {
        var accountID: String
        var conversations: [LarkConversation]
        var pinned: [LarkConversation]
        var nextCursor: String? = nil
        var loadedPages: Int = 1
    }

    struct Deps {
        var page: (String?, String) async throws -> APIClient.LarkConversations
        var pinned: () async throws -> APIClient.LarkConversations
        var status: () async throws -> LarkStatus
        var cached: () -> Snapshot?
        var save: (Snapshot?) -> Void
        var unreadPage: ((String?, String) async throws -> APIClient.LarkConversations)? = nil

        /// account: the Feishu account to list (`ol_account`); nil: the hub's own. Its rows carry it (`LarkConversation.account`).
        static func live(_ config: ServerConfig, account: Int? = nil, accountName: String? = nil) -> Self {
            let api = APIClient(config: config).lark(account)
            let cache = LarkConversationCache(name: cacheName(config, account: account))
            let stamp = { (page: APIClient.LarkConversations) -> APIClient.LarkConversations in
                var page = page
                for i in page.conversations.indices {
                    page.conversations[i].account = account
                    page.conversations[i].accountName = accountName
                }
                return page
            }
            return Self(page: { stamp(try await api.larkConversationPage(cursor: $0, accountID: $1)) },
                        pinned: { stamp(try await api.larkPinnedSnapshot()) },
                        status: { try await api.larkStatus() }, cached: { cache.load() }, save: { cache.save($0) },
                        unreadPage: { stamp(try await api.larkConversationPage(cursor: $0, accountID: $1, scope: "unread")) })
        }
    }

    private(set) var conversations: [LarkConversation] = []
    private(set) var filter: Filter = .all
    private(set) var pinned: [LarkConversation] = []
    private(set) var accountID: String?
    private(set) var loading = false
    private(set) var loadingMore = false
    private(set) var nextCursor: String?
    private(set) var status: LarkStatus?
    private(set) var failed = false
    private(set) var pageFailed = false
    private(set) var needsUpgrade = false
    @ObservationIgnored var willChange: () -> Void = {}
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var loadedPages = 1
    @ObservationIgnored private var revision = 0
    @ObservationIgnored private var scrolling = false
    @ObservationIgnored private var pending: Snapshot?
    @ObservationIgnored private var pageWaiters: [CheckedContinuation<Void, Never>] = []

    /// The rows under the pinned shelf. The hub's pages start with the pinned chats, out of time order; Feishu lists
    /// them under the shelf as well, each where its latest message puts it (2026-11-04: removing them was wrong). A
    /// pinned chat older than the last loaded row waits for the page that reaches its time, unless no page is left.
    /// A search hides the shelf and keeps the hub's rows as they are.
    nonisolated static func listed(_ conversations: [LarkConversation], pinned: [LarkConversation], query: String,
                                   more: Bool = false) -> [LarkConversation] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard q.isEmpty else {
            return conversations.filter { $0.name.localizedStandardContains(q) || $0.preview.localizedStandardContains(q) }
        }
        guard !pinned.isEmpty else { return conversations }
        let shelf = Set(pinned.map(\.id))
        let rest = conversations.filter { !shelf.contains($0.id) }
        let loaded = Dictionary(conversations.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        let oldest = rest.last?.updatedAt
        var pins = pinned.map { loaded[$0.id] ?? $0 }.filter { pin in
            guard more else { return true }
            guard let time = pin.updatedAt, let oldest else { return false }
            return time >= oldest
        }
        pins.sort { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }
        var out: [LarkConversation] = []
        out.reserveCapacity(rest.count + pins.count)
        var next = pins.startIndex
        for row in rest {
            let time = row.updatedAt ?? .distantPast
            while next < pins.endIndex, (pins[next].updatedAt ?? .distantPast) >= time {
                out.append(pins[next]); next += 1
            }
            out.append(row)
        }
        out.append(contentsOf: pins[next...])
        return out
    }

    init(deps: Deps) {
        self.deps = deps
        if let saved = deps.cached(), !saved.accountID.isEmpty {
            accountID = saved.accountID
            conversations = saved.conversations
            pinned = saved.pinned
            nextCursor = saved.nextCursor
            loadedPages = max(1, saved.loadedPages)
        }
    }

    static func cacheName(_ config: ServerConfig, account: Int? = nil) -> String {
        let digest = SHA256.hash(data: Data((config.url.absoluteString + "\n" + config.token).utf8))
        // Old snapshots have no owner. Do not import them into an account-bound shelf.
        return "lark-conversations-v2-" + digest.map { String(format: "%02x", $0) }.joined() + (account.map { "-a\($0)" } ?? "")
    }

    /// A filter owns its cursor and requests. Only All uses the existing disk cache; switching never reads disk.
    func select(_ value: Filter) async {
        guard value != filter else { return }
        revision += 1
        filter = value
        willChange()
        pending = nil
        conversations = []
        pinned = []
        nextCursor = nil
        loadedPages = 1
        failed = false
        pageFailed = false
        needsUpgrade = false
        await refresh()
    }

    private func fetchPage(_ cursor: String?, _ owner: String) async throws -> APIClient.LarkConversations {
        if filter == .unread, let unread = deps.unreadPage { return try await unread(cursor, owner) }
        let result = try await deps.page(cursor, owner)
        return filter == .unread ? result.unreadOnly() : result
    }

    /// Fetch the complete shelf and every loaded page before replacing the snapshot.
    /// A newer refresh invalidates the previous result even if its HTTP cancellation arrives late.
    func refresh() async {
        revision += 1
        let request = revision
        while loading || loadingMore {
            await withCheckedContinuation { pageWaiters.append($0) }
            guard !Task.isCancelled, request == revision else { return }
        }
        guard !Task.isCancelled, request == revision else { return }
        loading = true
        defer { loading = false; wakePageWaiters() }
        do {
            let shelf = try await deps.pinned()
            try check(request)
            let owner = try Self.owner(shelf)
            if let accountID, accountID != owner { clearAccount() }
            // Record the new owner immediately; no old account's screen may stay open while pages load.
            accountID = owner
            var rows: [LarkConversation] = []
            var cursor: String?
            var seen = Set<String>()
            var count = 0
            let pageCount = pending?.loadedPages ?? loadedPages
            repeat {
                let page = try await fetchPage(cursor, owner)
                try check(request)
                guard try Self.owner(page) == owner else { throw LoadError.accountChanged }
                rows = Self.merge(rows, page.conversations)
                count += 1
                cursor = page.nextCursor.flatMap { $0.isEmpty || !seen.insert($0).inserted ? nil : $0 }
            } while cursor != nil && count < pageCount
            let fresh = Snapshot(accountID: owner, conversations: rows, pinned: Self.merge([], shelf.conversations),
                                 nextCursor: cursor, loadedPages: count)
            failed = false
            pageFailed = false
            needsUpgrade = false
            publish(fresh)
        } catch {
            guard request == revision, !Self.cancelled(error) else { return }
            handle(error)
            failed = true
        }
        if !Task.isCancelled {
            let value = try? await deps.status()
            if request == revision { status = value }
        }
    }

    /// Events named these chats (feed ids of this list's account). A new message puts its chat on page 1, so the shelf
    /// and page 1 alone are read and those chats move up there: two requests, where `refresh` reads every loaded page
    /// and the status too. The shelf is read every time: an event names a pinned chat for its count and for the shelf's
    /// own changes alike.
    /// When page 1 lacks one of them (read elsewhere, done, left: it may have left the window or changed in place in
    /// it), the whole window is read again (#3006; #530: a refresh of page 1 alone left the older rows' unread counts stale).
    func refreshHead(_ feeds: Set<String>) async {
        guard !feeds.isEmpty else { return }
        guard let owner = accountID, !failed, !current.conversations.isEmpty else { return await refresh() }
        revision += 1
        let request = revision
        while loading || loadingMore {
            await withCheckedContinuation { pageWaiters.append($0) }
            guard !Task.isCancelled, request == revision else { return }
        }
        guard !Task.isCancelled, request == revision else { return }
        loading = true
        var whole = false
        do {
            let shelf = try await deps.pinned()
            try check(request)
            let page = try await fetchPage(nil, owner)
            try check(request)
            // Another account signed in: the whole list is read for it (`refresh` clears the old one first).
            if try Self.owner(shelf) != owner || Self.owner(page) != owner || accountID != owner { whole = true }
            else if feeds.isSubset(of: page.conversations.map(\.id)) {
                // Rows past page 1 keep their place, and so does the cursor: a chat that moved up from past the window
                // comes again on the next page, and `merge` keeps it once.
                let base = current
                publish(Snapshot(accountID: owner, conversations: Self.raise(base.conversations, page.conversations),
                                 pinned: Self.merge([], shelf.conversations), nextCursor: base.nextCursor,
                                 loadedPages: base.loadedPages))
                failed = false
                pageFailed = false
            } else {
                whole = true
            }
        } catch {
            if request == revision, !Self.cancelled(error) {
                handle(error)
                // An account change cleared the list: read the new account's at once, as `refresh` does.
                if accountID == nil { whole = true } else { failed = true }
            }
        }
        loading = false
        wakePageWaiters()
        if whole, request == revision, !Task.isCancelled { await refresh() }
    }

    func more() async {
        // A refresh landing while the page is on its way drops it. The spinner asking for it keys on the cursor, which
        // the refresh leaves as it was, so it would not ask again: ask here once the refresh is done.
        let selected = filter
        while selected == filter, await morePage(selected) == .superseded {}
    }

    private enum PageOutcome { case done, superseded }

    private func morePage(_ selected: Filter) async -> PageOutcome {
        // Only a refresh holds the page back. A moving list asks at once (#2823: waiting for the fling to rest left the
        // spinner at the end for a whole round trip after it stopped); what comes back waits in `pending` like a refresh.
        while loading {
            await withCheckedContinuation { pageWaiters.append($0) }
            guard !Task.isCancelled, selected == filter else { return .done }
        }
        let base = current
        guard selected == filter, let cursor = base.nextCursor, let owner = accountID, !loadingMore, !Task.isCancelled else { return .done }
        let request = revision
        loadingMore = true
        defer { loadingMore = false; wakePageWaiters() }
        do {
            let page = try await fetchPage(cursor, owner)
            try check(request)
            guard try Self.owner(page) == owner, accountID == owner else { throw LoadError.accountChanged }
            // A snapshot held while this page was on its way (`pending`) is the newer base, as long as it still ends here.
            let now = current
            guard now.nextCursor == cursor else { return .done }
            let fresh = Snapshot(accountID: owner, conversations: Self.merge(now.conversations, page.conversations), pinned: now.pinned,
                                 nextCursor: page.nextCursor.flatMap { $0.isEmpty || $0 == cursor ? nil : $0 }, loadedPages: now.loadedPages + 1)
            pageFailed = false
            publish(fresh)
        } catch {
            if request != revision, !Task.isCancelled { return .superseded }
            guard !Self.cancelled(error) else { return .done }
            handle(error)
            pageFailed = true
        }
        return .done
    }

    /// Keep a moving list stable. The newest atomic snapshot lands as soon as scrolling rests.
    /// Account changes bypass this hold and immediately remove the former user's data.
    func holdScrolling(_ value: Bool) {
        scrolling = value
        if !value {
            if let fresh = pending { pending = nil; apply(fresh) }
            wakePageWaiters()
        }
    }

    /// The newest snapshot: one held back for a moving list, or what is on screen.
    private var current: Snapshot {
        pending ?? Snapshot(accountID: accountID ?? "", conversations: conversations, pinned: pinned, nextCursor: nextCursor,
                            loadedPages: loadedPages)
    }

    private func apply(_ fresh: Snapshot) {
        guard fresh.accountID == accountID else { return }
        willChange()
        conversations = fresh.conversations
        pinned = fresh.pinned
        nextCursor = fresh.nextCursor
        loadedPages = fresh.loadedPages
        if filter == .all { deps.save(fresh) }
    }

    private func publish(_ fresh: Snapshot) {
        if scrolling {
            pending = fresh
            if filter == .all { deps.save(fresh) }
        } else { apply(fresh) }
    }

    private func clearAccount() {
        willChange()
        accountID = nil
        pending = nil
        conversations = []
        pinned = []
        nextCursor = nil
        loadedPages = 1
        deps.save(nil)
    }

    private func handle(_ error: Error) {
        if error as? LoadError == .upgradeRequired { needsUpgrade = true }
        if error as? LoadError == .accountChanged || (error as? APIError)?.code == "lark_account_changed"
            || (error as? APIError)?.code == "lark_not_logged_in" { clearAccount() }
    }

    private func check(_ request: Int) throws {
        try Task.checkCancellation()
        if request != revision { throw CancellationError() }
    }

    private static func owner(_ page: APIClient.LarkConversations) throws -> String {
        guard let id = page.accountID, !id.isEmpty else { throw LoadError.upgradeRequired }
        return id
    }

    private enum LoadError: Error { case accountChanged, upgradeRequired }

    private func wakePageWaiters() {
        let waiting = pageWaiters
        pageWaiters = []
        waiting.forEach { $0.resume() }
    }

    private static func cancelled(_ error: Error) -> Bool {
        error is CancellationError || (error as? URLError)?.code == .cancelled || Task.isCancelled
    }

    /// Page 1 on top, then the other rows in their order: a chat page 1 holds leaves its old place.
    static func raise(_ rows: [LarkConversation], _ head: [LarkConversation]) -> [LarkConversation] {
        let top = merge([], head)
        let moved = Set(top.map(\.id))
        return top + rows.filter { !moved.contains($0.id) }
    }

    static func merge(_ existing: [LarkConversation], _ fresh: [LarkConversation]) -> [LarkConversation] {
        var rows = existing
        var indices = Dictionary(existing.enumerated().map { ($0.element.id, $0.offset) }, uniquingKeysWith: { first, _ in first })
        for row in fresh {
            if let index = indices[row.id] { rows[index] = row }
            else { indices[row.id] = rows.count; rows.append(row) }
        }
        return rows
    }
}

/// Serialize disk mutations so clearing a switched account cannot race an older asynchronous save.
/// Only this browser uses the file; no frequent UserDefaults writes or unowned legacy cache import.
nonisolated private struct LarkConversationCache: Sendable {
    let name: String
    private static let writer = DispatchQueue(label: "persona.lark-conversation-cache", qos: .utility)
    private var url: URL? {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first?
            .appending(path: "state").appending(path: name + ".json")
    }
    func load() -> LarkConversationsModel.Snapshot? {
        Self.writer.sync {
            guard let url, let data = try? Data(contentsOf: url) else { return nil }
            return try? JSON.decoder.decode(LarkConversationsModel.Snapshot.self, from: data)
        }
    }
    func save(_ value: LarkConversationsModel.Snapshot?) {
        Self.writer.async {
            guard let url, let data = try? JSON.encoder.encode(value) else { return }
            try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            try? data.write(to: url, options: .atomic)
        }
    }
}
