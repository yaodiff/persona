import Foundation
import Synchronization

/// Test-only shadow of the iOS continuity adapter. It never selects the production implementation or carries message
/// contents into diagnostics. The fixture harness supplies the same actions to native ChatStore and this shadow.
final class ChangeRecorder: @unchecked Sendable {
    let values = Mutex<[(String, UInt64)]>([])
}

private final class ShadowEffectRecorder: @unchecked Sendable {
    let effects = Mutex<[Effect]>([])
}

public final class CoreContinuityShadow: @unchecked Sendable {
    public struct Mismatch: Error, Equatable, Sendable {
        public let domain: String
        public let rule: String
        public let inputShape: String
        public init(domain: String = "continuity", rule: String, inputShape: String) {
            self.domain = domain
            self.rule = rule
            self.inputShape = inputShape
        }
    }

    public let client: PersonaCoreClient
    public let chat: String
    private let changes: ChangeRecorder
    private let effects: ShadowEffectRecorder

    public init(chat: String) throws {
        self.chat = chat
        let changes = ChangeRecorder()
        let effects = ShadowEffectRecorder()
        self.changes = changes
        self.effects = effects
        client = try PersonaCoreClient(config: Config(), callbacks: PersonaCoreCallbacks(changedChat: { chat, version in
            changes.values.withLock { $0.append((chat, version)) }
        }, run: { effect in
            effects.effects.withLock { $0.append(effect) }
        }))
    }

    public func dispatch(_ action: Action) throws { try client.dispatch(action) }
    public func drain() throws -> ChatPatch { try client.drain(chat: chat) }
    public func flush() throws { try client.flush() }
    public func wakeChatIDs() -> [String] { changes.values.withLock { $0.map(\.0) } }
    public func effectsSnapshot() -> [Effect] { effects.effects.withLock { $0 } }
    public func drainEffects() -> [Effect] { effects.effects.withLock { current in
        defer { current.removeAll() }
        return current
    } }
    public func close() { client.close() }
}
