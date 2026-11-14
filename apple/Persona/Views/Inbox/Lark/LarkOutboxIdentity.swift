import CryptoKit
import Foundation
import PersonaWire

/// A routed URL or a rotated device token is not an account identity.
nonisolated struct LarkOutboxIdentity: Hashable, Sendable {
    var group: String
    var account: String

    static func digest(_ value: String) -> String {
        SHA256.hash(data: Data(value.utf8)).map { String(format: "%02x", $0) }.joined()
    }

    static func groupDirectory(_ group: String, base: URL) -> URL {
        base.appending(path: "lark-outbox-v2/" + digest(group))
    }

    func directory(feed: String, base: URL) -> URL {
        Self.groupDirectory(group, base: base).appending(path: Self.digest(account) + "/" + Self.digest(feed))
    }

    enum Failure: LocalizedError {
        case unavailable, changed, removed
        var errorDescription: String? {
            switch self {
            case .unavailable: String(localized: "Can't confirm your Lark account right now. Check the connection and try again.")
            case .changed: String(localized: "Your Lark account changed, so the message wasn't sent. Switch back to the original account and try again.")
            case .removed: String(localized: "Disconnected from this hub. The message wasn't sent.")
            }
        }
    }
}

extension APIClient {
    /// The agent route reports the account selected by the Hub's gateway account header.
    /// A primary account elsewhere in the list must never override the selected account.
    func larkAccountID() async throws -> String {
        if config.isDemo { return "demo" }
        if let source = messagingSource { return try await messagingCall { try await messagingUser(source, refresh: true) } }
        struct Account: Decodable {
            var id: String
            var userId: String?
            var signedIn: Bool
            var active: Bool?
        }
        struct Accounts: Decodable { var accounts: [Account]; var activeId: String? }
        let response: Accounts = try await get("/api/v1/lark/raw/accounts")
        let selected: Account?
        if let id = response.activeId { selected = response.accounts.first { $0.id == id } }
        else {
            let active = response.accounts.filter { $0.active == true }
            selected = active.count == 1 ? active.first : nil
        }
        guard let selected, selected.signedIn, let user = selected.userId, !user.isEmpty else {
            throw LarkOutboxIdentity.Failure.unavailable
        }
        return user
    }
}

/// Last verified identity is only for opening local drafts offline. Every send verifies again. A named manager's chat
/// (`manager`) may be signed in as another user: its own file, in the same group's directory, so it is forgotten with the
/// group. nil: the default's, the file it always was.
actor LarkOutboxAccounts {
    static let shared = LarkOutboxAccounts()
    private(set) var revision = 0

    private static func file(_ manager: String?) -> String {
        guard let manager, !manager.isEmpty else { return "account.json" }
        return "account@" + LarkOutboxIdentity.digest(manager) + ".json"
    }

    func cached(group: String, manager: String? = nil, base: URL) -> String? {
        let file = LarkOutboxIdentity.groupDirectory(group, base: base).appending(path: Self.file(manager))
        guard let bytes = try? Data(contentsOf: file) else { return nil }
        return try? JSON.decoder.decode(String.self, from: bytes)
    }

    func remember(_ account: String, group: String, manager: String? = nil, base: URL, expectedRevision: Int? = nil) throws {
        guard expectedRevision == nil || expectedRevision == revision else { throw LarkOutboxIdentity.Failure.removed }
        var directory = LarkOutboxIdentity.groupDirectory(group, base: base)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                               attributes: [.posixPermissions: 0o700])
        var values = URLResourceValues(); values.isExcludedFromBackup = true
        try directory.setResourceValues(values)
        let url = directory.appending(path: Self.file(manager))
        try JSON.encoder.encode(account).write(to: url, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
    }

    func remove(groups: Set<String>?, base: URL) throws {
        revision += 1
        let directories = groups.map { $0.map { LarkOutboxIdentity.groupDirectory($0, base: base) } }
            ?? [base.appending(path: "lark-outbox-v2"), base.appending(path: "lark-outbox-v1")]
        for directory in directories where FileManager.default.fileExists(atPath: directory.path) {
            try FileManager.default.removeItem(at: directory)
        }
    }
}
