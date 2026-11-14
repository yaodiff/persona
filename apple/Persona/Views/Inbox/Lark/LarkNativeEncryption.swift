import CryptoKit
import Foundation
import PersonaWire

nonisolated struct LarkEncryptedDraft: Codable, Hashable, Sendable {
    var cover = ""
    /// A source's sealed message put back for sealing again after a send that may have gone (#2138): sent again as
    /// the same message (its client_id), while its words and quote are still the ones put back (`digest`).
    var again: Again? = nil

    struct Again: Codable, Hashable, Sendable {
        var id: UUID
        var digest: String

        static func digest(text: String, reply: String?) -> String { LarkOutboxIdentity.digest(text + "\u{0}" + (reply ?? "")) }
    }
}

nonisolated struct LarkNativeEncryptionStatus: Decodable, Sendable {
    var accountId: String
    var configured: Bool
    var locked: Bool
    var unifiedPassword: Bool
}

nonisolated struct LarkEncryptedPayload: Codable, Equatable, Sendable {
    struct Message: Codable, Equatable, Sendable { var text: String }
    var accountId: String
    var feedId: String
    var messages: [Message]
    var cover: String?

    func clientID(base: String, index: Int) -> String {
        if index == messages.count - 1 { return base }
        var bytes = Array(SHA256.hash(data: Data("persona:olk:\(base):\(index)".utf8)).prefix(16))
        bytes[6] = (bytes[6] & 0x0f) | 0x80
        bytes[8] = (bytes[8] & 0x3f) | 0x80
        let hex = bytes.map { String(format: "%02x", $0) }.joined()
        let chars = Array(hex)
        return [String(chars[0..<8]), String(chars[8..<12]), String(chars[12..<16]), String(chars[16..<20]), String(chars[20..<32])].joined(separator: "-")
    }
}

extension APIClient {
    private func larkEncryptionRequest<T: Decodable>(_ method: String, _ path: String,
        body: (any Encodable)? = nil, timeout: TimeInterval = 30) async throws -> T {
        do {
            return try JSON.decoder.decode(T.self, from: await request(method, path, body: body, timeout: timeout))
        } catch let error as APIError where [404, 405].contains(error.status)
            && ["", "not_found", "method_not_allowed"].contains(error.code?.lowercased() ?? "") {
            throw APIError(status: 426, message: String(localized: "Update the hub to use encrypted messages. Open the hub in Settings and check for updates, then check again to continue."),
                           fields: ["code": "lark_hub_upgrade_required"])
        }
    }

    func larkEncryptionStatus() async throws -> LarkNativeEncryptionStatus {
        try await larkEncryptionRequest("GET", "/api/v1/lark/encryption")
    }
    func larkUnlock(account: String, password: String) async throws -> LarkNativeEncryptionStatus {
        try await larkEncryptionRequest("POST", "/api/v1/lark/encryption/unlock", body: ["account_id": account, "password": password])
    }
    func larkEncryptionCovers(account: String, feed: String) async throws -> [String] {
        struct Covers: Decodable { var lines: [String] }
        let result: Covers = try await larkEncryptionRequest("POST", "/api/v1/lark/conversations/\(feed)/encryption/covers", body: ["account_id": account])
        return result.lines
    }
    func larkPrepareEncryption(account: String, feed: String, text: String, cover: String) async throws -> LarkEncryptedPayload {
        var result: LarkEncryptedPayload = try await larkEncryptionRequest("POST", "/api/v1/lark/conversations/\(feed)/encryption/prepare",
            body: ["account_id": account, "text": text, "cover": cover])
        guard result.accountId == account, result.feedId == feed, !result.messages.isEmpty else { throw LarkOutboxIdentity.Failure.changed }
        result.cover = cover
        return result
    }
    func larkSendEncrypted(_ payload: LarkEncryptedPayload, clientID: String, replyID: String?) async throws -> LarkSendReceipt {
        guard !payload.messages.isEmpty else { throw LarkOutboxIdentity.Failure.unavailable }
        var receipt = LarkSendReceipt(messageID: nil)
        for (index, message) in payload.messages.enumerated() {
            var body = ["account_id": payload.accountId, "ciphertext": message.text, "client_id": payload.clientID(base: clientID, index: index)]
            if index == payload.messages.count - 1 { body["reply_id"] = replyID }
            receipt = try await larkEncryptionRequest("POST", "/api/v1/lark/conversations/\(payload.feedId)/encrypted-messages", body: body, timeout: Self.larkSendTimeout)
        }
        return receipt
    }
}

/// Authored encrypted drafts and optimistic text never enter a plain JSON snapshot.
nonisolated struct LarkOutboxSealer: Sendable {
    private static let prefix = Data("OLKD1\n".utf8)
    private static let lock = NSLock()
    private static let account = "lark.outbox.seal.v1"
    var key: @Sendable (Bool) throws -> Data = systemKey

    enum Failure: LocalizedError {
        case unavailable
        var errorDescription: String? { String(localized: "Couldn't read encrypted drafts on this device. Unlock it and try again.") }
    }
    private static func systemKey(create: Bool) throws -> Data {
        lock.lock()
        defer { lock.unlock() }
        switch Keychain.read(account: account) {
        case .found(let data):
            guard data.count == 32 else { throw Failure.unavailable }
            return data
        case .missing:
            guard create else { throw Failure.unavailable }
            let data = SymmetricKey(size: .bits256).withUnsafeBytes { Data($0) }
            guard Keychain.save(data, account: account, thisDeviceOnly: true), Keychain.read(account: account) == .found(data) else { throw Failure.unavailable }
            return data
        case .failed: throw Failure.unavailable
        }
    }
    func seal(_ data: Data) throws -> Data {
        guard let combined = try AES.GCM.seal(data, using: SymmetricKey(data: key(true))).combined else { throw Failure.unavailable }
        return Self.prefix + combined
    }
    func open(_ data: Data) throws -> Data {
        guard data.starts(with: Self.prefix) else { return data }
        return try AES.GCM.open(AES.GCM.SealedBox(combined: data.dropFirst(Self.prefix.count)), using: SymmetricKey(data: key(false)))
    }
}
