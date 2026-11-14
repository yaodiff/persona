import Foundation
import Observation
import PersonaWire

nonisolated struct LarkChatPin: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var messageID: String
    var message: LarkMessage?
    var operatorName: String?
    var createdAt: Date?
    enum CodingKeys: String, CodingKey {
        case id, message
        case messageID = "message_id", operatorName = "operator_name", createdAt = "created_at"
    }
    var canRemove: Bool { message?.canUnpin == true }
}

nonisolated struct LarkChatPinsPage: Decodable, Sendable {
    var accountID: String
    var items: [LarkChatPin]
    var nextCursor: String?
    var total: Int
    enum CodingKeys: String, CodingKey { case items, total; case accountID = "account_id", nextCursor = "next_cursor" }
}

nonisolated struct LarkChatPinTarget: Decodable, Identifiable, Sendable {
    var message: LarkMessage
    var root: LarkMessage?
    var page: APIClient.LarkMessages
    var id: String { message.id }
}

extension APIClient {
    private func chatPinRequest<T: Decodable>(_ method: String, _ path: String,
        query: [String: String] = [:], body: (any Encodable)? = nil) async throws -> T {
        do { return try JSON.decoder.decode(T.self, from: await request(method, path, query: query, body: body)) }
        catch let error as APIError where [404, 405].contains(error.status)
            && ["", "not_found", "method_not_allowed"].contains(error.code?.lowercased() ?? "") {
            throw APIError(status: 426, message: String(localized: "Update the hub to use pinned messages. Open the hub in Settings and check for updates."), fields: ["code": "lark_hub_upgrade_required"])
        }
    }

    func larkChatPins(_ feed: String, account: String?, cursor: String?) async throws -> LarkChatPinsPage {
        if let source = messagingSource {
            let user = try await messagingCall { try await messagingUser(source) }
            let acct = larkAccount.map(String.init)
            return try await messagingCall {
                try await messagingGet(source, "conversations/\(feed)/pins", query: ["limit": "50", "cursor": cursor ?? ""]) { raw in
                    guard var o = raw as? [String: Any] else { return raw }
                    o["items"] = (o["items"] as? [[String: Any]] ?? []).map { it in
                        var x = it
                        if let m = it["message"] as? [String: Any] { x["message"] = MessagingShape.message(m, source: source, account: acct) }
                        return x
                    }
                    o["account_id"] = user
                    if o["total"] == nil { o["total"] = (o["items"] as? [Any])?.count ?? 0 }
                    return o
                }
            }
        }
        var q = ["limit": "50"]
        q["account_id"] = account; q["cursor"] = cursor
        return try await chatPinRequest("GET", "/api/v1/lark/conversations/\(feed)/pins", query: q)
    }
    func larkPinTarget(_ feed: String, message: String, account: String, topic: Bool) async throws -> LarkChatPinTarget {
        struct Result: Decodable { var account_id: String; var target: LarkChatPinTarget }
        if let source = messagingSource {
            let user = try await messagingCall { try await messagingUser(source) }
            let acct = larkAccount.map(String.init)
            let result: Result = try await messagingCall {
                try await messagingGet(source, "conversations/\(feed)/pins/\(message)", query: ["topic": topic ? "1" : "0"]) { raw in
                    guard let o = raw as? [String: Any], let t = o["target"] as? [String: Any] else { return raw }
                    var target: [String: Any] = [:]
                    if let m = t["message"] as? [String: Any] { target["message"] = MessagingShape.message(m, source: source, account: acct) }
                    if let r = t["root"] as? [String: Any] { target["root"] = MessagingShape.message(r, source: source, account: acct) }
                    target["page"] = MessagingShape.page(t["page"] ?? [:], source: source, account: acct, user: user)
                    return ["account_id": user, "target": target] as [String: Any]
                }
            }
            guard result.account_id == account, result.target.id == message else { throw LarkChatPinsModel.accountChanged }
            return result.target
        }
        let result: Result = try await chatPinRequest("GET", "/api/v1/lark/conversations/\(feed)/pin-messages/\(message)",
            query: ["account_id": account, "topic": topic ? "1" : "0"])
        guard result.account_id == account, result.target.id == message else { throw LarkChatPinsModel.accountChanged }
        return result.target
    }
    func larkChangePin(_ feed: String, message: String, pin: String?, account: String) async throws {
        struct Body: Encodable { var account_id: String; var message_id: String; var pin_id: String?; var remove: Bool }
        if let source = messagingSource {
            struct M: Encodable { var message_id: String; var pin_id: String?; var remove: Bool }
            struct R: Decodable { var pinned: Bool }
            let unknown = APIError(status: 502, message: String(localized: "Couldn't confirm the result. Reload the pinned messages to check."), fields: ["code": "lark_pin_result_unknown"])
            guard try await messagingCall({ try await messagingUser(source, refresh: true) }) == account else { throw LarkChatPinsModel.accountChanged }
            let raw: Data
            do {
                raw = try await messagingCall {
                    try await request("POST", messagingPath(source, "conversations/\(feed)/pins"), query: messagingQuery(),
                                      body: M(message_id: message, pin_id: pin, remove: pin != nil))
                }
            } catch let e as APIError where e.code == "result_unknown" {
                throw unknown // the list is checked again before the next change
            }
            guard try JSON.decoder.decode(R.self, from: raw).pinned == (pin == nil) else { throw unknown }
            return
        }
        struct Result: Decodable { var account_id: String; var pinned: Bool }
        let result: Result = try await chatPinRequest("POST", "/api/v1/lark/conversations/\(feed)/pins",
            body: Body(account_id: account, message_id: message, pin_id: pin, remove: pin != nil))
        guard result.account_id == account else { throw LarkChatPinsModel.accountChanged }
        guard result.pinned == (pin == nil) else {
            throw APIError(status: 502, message: String(localized: "Couldn't confirm the result. Reload the pinned messages to check."), fields: ["code": "lark_pin_result_unknown"])
        }
    }
}

/// List reads mark pins read upstream. This model belongs to an explicitly opened
/// panel; it never subscribes or prefetches from the home page. No pin plaintext is written to disk.
@MainActor @Observable final class LarkChatPinsModel {
    struct Deps {
        var page: (String?, String?) async throws -> LarkChatPinsPage
        var change: (String, String?, String) async throws -> Void
        var locate: (String, String) async throws -> LarkChatPinTarget
        static func live(api: APIClient, feed: String, topic: Bool) -> Self {
            Self(page: { try await api.larkChatPins(feed, account: $0, cursor: $1) },
                 change: { try await api.larkChangePin(feed, message: $0, pin: $1, account: $2) },
                 locate: { try await api.larkPinTarget(feed, message: $0, account: $1, topic: topic) })
        }
    }
    private(set) var items: [LarkChatPin] = []
    private(set) var total = 0
    private(set) var nextCursor: String?
    private(set) var accountID: String?
    private(set) var loading = false
    private(set) var loadingMore = false
    private(set) var changing = false
    private(set) var locating: String?
    private(set) var problem: String?
    private(set) var pageProblem: String?
    private(set) var blocked = false
    private(set) var requiresRecheck = false
    private(set) var loaded = false
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var pages = 1
    @ObservationIgnored private var revision = 0
    @ObservationIgnored private var pendingRefresh = false
    @ObservationIgnored private var stopped = false
    @ObservationIgnored private var seenCursors = Set<String>()
    var busy: Bool { loading || loadingMore || changing || locating != nil }
    static var accountChanged: APIError { APIError(status: 409, message: String(localized: "Your Lark account changed. Open the chat again."), fields: ["code": "lark_account_changed"]) }

    init(account: String? = nil, deps: Deps) { accountID = account; self.deps = deps }

    func stop() { stopped = true; revision += 1 }

    func refresh() async {
        guard !stopped, !blocked else { return }
        if busy { pendingRefresh = true; return }
        repeat {
            pendingRefresh = false; loading = true
            let request = revision
            do {
                var rows: [LarkChatPin] = [], cursor: String?, owner = accountID
                var count = 0, read = 0
                var cursors = Set<String>()
                repeat {
                    let page = try await deps.page(owner, cursor)
                    try checkOwner(page.accountID, expected: owner)
                    owner = page.accountID; read += 1; count = page.total
                    rows = Self.merged(rows, page.items)
                    cursor = page.nextCursor.flatMap { $0.isEmpty ? nil : $0 }
                    if let cursor, !cursors.insert(cursor).inserted { throw Self.incomplete }
                } while read < pages && cursor != nil
                guard request == revision, !Task.isCancelled else { loading = false; return }
                seenCursors = cursors
                accountID = owner; items = rows; total = max(count, rows.count); nextCursor = cursor; pages = read
                problem = nil; pageProblem = nil; loaded = true; requiresRecheck = false
            } catch { if request == revision { failed(error) } }
            loading = false
        } while pendingRefresh && !blocked && !stopped && !Task.isCancelled
    }

    func more() async {
        guard !busy, !blocked, !stopped, let cursor = nextCursor else { return }
        loadingMore = true; pageProblem = nil
        let request = revision
        do {
            let page = try await deps.page(accountID, cursor)
            try checkOwner(page.accountID, expected: accountID)
            guard request == revision, !Task.isCancelled else { loadingMore = false; return }
            let next = page.nextCursor.flatMap { $0.isEmpty ? nil : $0 }
            if let next, !seenCursors.insert(next).inserted { throw Self.incomplete }
            items = Self.merged(items, page.items); total = max(page.total, items.count); nextCursor = next; pages += 1
        } catch {
            if request == revision { failed(error, paging: true) }
        }
        loadingMore = false
        if pendingRefresh { await refresh() }
    }

    func change(message: String, pin: String? = nil) async -> Bool {
        guard !busy, !blocked, !requiresRecheck, !stopped, let account = accountID else { return false }
        changing = true; problem = nil
        let request = revision
        do {
            try await deps.change(message, pin, account)
            guard request == revision, !stopped else { changing = false; return false }
            changing = false
            requiresRecheck = true
            await refresh()
            return true
        } catch {
            if request == revision {
                requiresRecheck = (error as? APIError)?.code == "lark_pin_result_unknown" || error.isTimeout || error.isOffline
                failed(error)
            }
            changing = false
            return false
        }
    }

    func locate(_ pin: LarkChatPin) async -> LarkChatPinTarget? {
        guard !busy, !blocked, !stopped, let account = accountID else { return nil }
        locating = pin.id; problem = nil
        let request = revision
        defer { locating = nil }
        do {
            let target = try await deps.locate(pin.messageID, account)
            guard target.id == pin.messageID, !stopped, request == revision, !Task.isCancelled else { return nil }
            return target
        } catch { if request == revision { failed(error) }; return nil }
    }

    private func checkOwner(_ owner: String, expected: String?) throws {
        guard !owner.isEmpty, expected == nil || expected == owner else { throw Self.accountChanged }
    }
    private func failed(_ error: Error, paging: Bool = false) {
        guard !(error is CancellationError), !Task.isCancelled else { return }
        let e = error as? APIError
        if e?.code == "lark_account_changed" || LarkLoadProblem.isNotLoggedIn(error) {
            items = []; nextCursor = nil; total = 0; blocked = true; revision += 1
        }
        let text = Self.problemText(error)
        if paging { pageProblem = text } else { problem = text }
    }
    static func problemText(_ error: Error) -> String {
        if let e = error as? APIError, e.code?.hasPrefix("lark_pin") == true || e.code?.hasSuffix("upgrade_required") == true {
            return e.message
        }
        return LarkLoadProblem.of(error, subject: String(localized: "pinned messages")).message
    }
    private static var incomplete: APIError { APIError(status: 502, message: String(localized: "The pinned list is incomplete. Reload it.")) }
    private static func merged(_ old: [LarkChatPin], _ new: [LarkChatPin]) -> [LarkChatPin] {
        var seen = Set<String>(); return (old + new).filter { seen.insert($0.id).inserted }
    }
}
