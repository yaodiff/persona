import Foundation
import Observation
import PersonaWire

/// A conversation belongs to its paired hub group, not to every hub with the same opaque conversation id.
nonisolated struct AssistantViewScope: Hashable, Sendable {
    var group: String
    var conversation: String
}

/// Current live-card snapshots (#2024). The feed keeps creation positions; updates replace only the referenced view.
/// The server persists the source of truth. This observable cache never writes frequent updates to UserDefaults.
@MainActor @Observable
final class AssistantViewsStore {
    static let shared = AssistantViewsStore()

    private(set) var snapshots: [AssistantViewScope: [String: ExtensionAssistantView]] = [:]
    /// Reply records survive a card update. Keys are opaque: neither slashes nor prefixes are interpreted here.
    private(set) var actions: [AssistantViewScope: [String: ExtensionViewAction]] = [:]
    private(set) var attempts: [AssistantViewScope: [String: AssistantViewAttempt]] = [:]
    private(set) var epoch: UInt = 0
    private(set) var registeredBlocks: [AssistantViewScope: Set<String>] = [:]
    @ObservationIgnored private var presentations: [AssistantViewScope: [String: AssistantViewModel]] = [:]

    /// Drafts and card navigation outlive a lazy row, but stay in memory and belong to this hub/conversation.
    func presentation(key: String, scope: AssistantViewScope, view: ExtensionView, version: Int64,
                      complete: Bool, registered: Bool, deps: AssistantViewModel.Deps) -> AssistantViewModel {
        if let model = presentations[scope]?[key] { return model }
        let model = AssistantViewModel(key: key, scope: scope, view: view, version: version, complete: complete,
                                       registered: registered, store: self, deps: deps)
        presentations[scope, default: [:]][key] = model
        return model
    }

    func isRegistered(_ key: String, scope: AssistantViewScope) -> Bool { registeredBlocks[scope]?.contains(key) == true }
    func receiveBlocks(_ keys: [String], conversation: String, group: String) {
        guard !conversation.isEmpty else { return }
        let scope = AssistantViewScope(group: group, conversation: conversation)
        registeredBlocks[scope, default: []].formUnion(keys.filter { !$0.isEmpty })
    }

    func attempt(_ key: String, scope: AssistantViewScope) -> AssistantViewAttempt? { attempts[scope]?[key] }

    /// The lock belongs to the card, not to a row SwiftUI may drop while scrolling.
    func begin(_ submission: AssistantViewSubmission, scope: AssistantViewScope) -> Bool {
        guard action(submission.key, conversation: scope.conversation, group: scope.group) == nil else { return false }
        if let current = attempts[scope]?[submission.key] {
            guard !current.sending, current.submission == submission else { return false }
        }
        attempts[scope, default: [:]][submission.key] = AssistantViewAttempt(submission: submission)
        return true
    }

    func finish(_ submission: AssistantViewSubmission, scope: AssistantViewScope, error: String?, uncertain: Bool) {
        guard attempts[scope]?[submission.key]?.submission == submission else { return }
        if uncertain, action(submission.key, conversation: scope.conversation, group: scope.group) == nil {
            attempts[scope, default: [:]][submission.key] = AssistantViewAttempt(submission: submission, sending: false, error: error)
        } else {
            attempts[scope]?[submission.key] = nil
        }
    }

    func action(_ key: String, conversation: String, group: String) -> ExtensionViewAction? {
        actions[AssistantViewScope(group: group, conversation: conversation)]?[key]
    }

    func view(_ id: String, conversation: String, group: String) -> ExtensionAssistantView? {
        snapshots[AssistantViewScope(group: group, conversation: conversation)]?[id]
    }

    /// Ignore invalid ownership and stale or repeated versions, including old creation events after newer updates.
    @discardableResult
    func receive(_ view: ExtensionAssistantView, conversation: String, group: String) -> Bool {
        guard !view.id.isEmpty, !conversation.isEmpty, view.conversation == conversation, view.version > 0 else { return false }
        let scope = AssistantViewScope(group: group, conversation: conversation)
        if let current = snapshots[scope]?[view.id], current.version >= view.version { return false }
        snapshots[scope, default: [:]][view.id] = view
        return true
    }

    /// Feed snapshots are full even when its items use `since`. Cards cannot be deleted through this protocol, so
    /// missing entries in a snapshot that raced a new event must not remove that event's newer card.
    func receive(_ views: [String: ExtensionAssistantView], conversation: String, group: String) {
        for (id, view) in views where id == view.id { receive(view, conversation: conversation, group: group) }
    }

    func receive(_ item: FeedItem, group: String) {
        guard let view = item.assistantView, item.ref == view.id else { return }
        receive(view, conversation: item.conversation, group: group)
    }

    /// Pending/sent/failed are ordered by the server revision, not by arrival time or by a status ranking.
    /// A legacy record without a revision may establish a lock, but cannot overwrite an already cached record.
    @discardableResult
    func receive(_ action: ExtensionViewAction, conversation: String, group: String) -> Bool {
        guard !conversation.isEmpty, !action.key.isEmpty, !action.action.isEmpty,
              !action.clientMsgId.isEmpty, !action.status.isEmpty,
              action.revision.map({ $0 > 0 }) ?? true else { return false }
        let scope = AssistantViewScope(group: group, conversation: conversation)
        if let current = actions[scope]?[action.key] {
            guard current.clientMsgId == action.clientMsgId, current.action == action.action,
                  (current.page ?? "") == (action.page ?? ""), (current.values ?? [:]) == (action.values ?? [:]),
                  (current.kind ?? "action") == (action.kind ?? "action"), current.version == action.version,
                  (current.text ?? "") == (action.text ?? ""),
                  (current.revision ?? 0) < (action.revision ?? 0) else { return false }
        }
        actions[scope, default: [:]][action.key] = action
        attempts[scope]?[action.key] = nil
        return true
    }

    func receiveActions(_ records: [String: ExtensionViewAction], conversation: String, group: String) {
        for (key, record) in records where key == record.key { receive(record, conversation: conversation, group: group) }
    }

    func forget() { epoch &+= 1; snapshots = [:]; actions = [:]; attempts = [:]; registeredBlocks = [:]; presentations = [:] }
}
