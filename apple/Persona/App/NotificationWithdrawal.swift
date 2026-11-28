import Foundation
import UserNotifications

/// Taking back the pushes about interactions, assistant cards, and to-do asks that are settled (#3513, #13916, #14388):
/// an approval, a question, a plan waiting for Start, a failed start, or a to-do decision / needs-user ask answered in
/// the app, on the computer, on the web or the watch, withdrawn or expired leaves the notification center with it.
/// Interaction pushes carry `interaction_id` (push_payload.go) and the session they are about; assistant cards carry
/// `card_id` and to-do decision / needs-user pushes carry `card_id` (`todo-ask:<todo id>`) and `todo_id` without
/// `interaction_id` so `stale` never sweeps unanswered assistant cards or to-do decisions. The app keeps the
/// notification center in step with its list of pending interactions and recalls to-do pushes on `todo-ask:<todo id>`;
/// while it is suspended the hub's withdrawal push names the cards and to-do asks that are over (#4349, `resolved`).
/// Pure, for the tests; AppModel reads and removes the delivered notifications.
nonisolated enum NotificationWithdrawal {
    /// Prefix of a to-do ask id in `EventFleetResolved` / `interaction_resolved` / `resolved_interaction_ids`
    /// (`assistantwire.TodoAskCardID`, `todo-ask:<todo id>`).
    static let todoAskPrefix = "todo-ask:"

    /// The to-do id named by a `todo-ask:<id>` resolution id, or nil for an interaction card id.
    static func todoID(fromResolved id: String) -> String? {
        guard id.hasPrefix(todoAskPrefix) else { return nil }
        let todo = id.dropFirst(todoAskPrefix.count).trimmingCharacters(in: .whitespacesAndNewlines)
        return todo.isEmpty ? nil : todo
    }

    /// What a delivered notification is keyed by: an interaction card (`interaction_id`) or a to-do decision /
    /// needs-user ask (`todo_id`, without `interaction_id`, #13916).
    enum Target: Sendable, Equatable {
        case interaction(String)
        case todo(String)
    }

    /// A delivered notification about an interaction or a to-do ask: its group (the push's, resolved the way a tap
    /// resolves it), its target key, and when it was delivered.
    struct Delivered: Sendable, Equatable {
        var identifier: String
        var group: String
        var target: Target
        var date: Date
        /// The occurrence its alert drew; nil from an older hub.
        var interactionCreatedAt: String? = nil
        /// A push about one of the assistant's own cards (a plan waiting for Start, a decision, a failed start: the
        /// push's `card_id`), never in the list of pending interactions. Only the hub's withdrawal push takes it back:
        /// the list saying nothing of it does not make it stale.
        var ownerCard = false

        init(identifier: String, group: String, interactionID: String, date: Date, interactionCreatedAt: String? = nil, ownerCard: Bool = false) {
            self.identifier = identifier
            self.group = group
            self.target = .interaction(interactionID)
            self.date = date
            self.interactionCreatedAt = interactionCreatedAt
            self.ownerCard = ownerCard
        }

        init(identifier: String, group: String, todoID: String, date: Date) {
            self.identifier = identifier
            self.group = group
            self.target = .todo(todoID)
            self.date = date
            self.ownerCard = true
        }

        init(identifier: String, group: String, target: Target, date: Date, interactionCreatedAt: String? = nil, ownerCard: Bool = false) {
            self.identifier = identifier
            self.group = group
            self.target = target
            self.date = date
            self.interactionCreatedAt = interactionCreatedAt
            if case .todo = target {
                self.ownerCard = true
            } else {
                self.ownerCard = ownerCard
            }
        }

        /// The interaction id when this notification is for an interaction card; nil for a to-do ask.
        var interactionID: String? {
            if case .interaction(let id) = target { return id }
            return nil
        }

        /// The to-do id when this notification is for a to-do decision or needs-user push; nil for an interaction card.
        var todoID: String? {
            if case .todo(let id) = target { return id }
            return nil
        }

        /// As `Interaction.uid`: unique across groups, just the id for the default group. Only interaction cards have
        /// an interaction uid, so interaction-list sweeps (`settled`, `stale`) never touch to-do asks.
        var uid: String? {
            guard let id = interactionID else { return nil }
            return group.isEmpty ? id : group + "|" + id
        }
    }

    /// The notifications about interactions that left the list (`gone`, by uid), unless one is pending again by now
    /// (an answer that did not go through puts its card back). Never removes assistant cards or to-do asks.
    static func settled(gone: Set<String>, pending: Set<String>, delivered: [Delivered]) -> [String] {
        let over = gone.subtracting(pending)
        return delivered.filter { d in
            guard !d.ownerCard, let uid = d.uid else { return false }
            return over.contains(uid)
        }.map(\.identifier)
    }

    /// Occurrence-aware model removal. A replacement may already have its alert before its model event arrives;
    /// only the removed occurrence's alert goes. A same-occurrence card put back after a failed answer stays.
    static func settledOccurrences(gone: [String: Interaction], pending: [String: Interaction], delivered: [Delivered]) -> [String] {
        delivered.filter { d in
            guard !d.ownerCard, let uid = d.uid, let old = gone[uid] else { return false }
            if let current = pending[uid], old.createdAtWire == nil || current.createdAtWire == old.createdAtWire {
                return false
            }
            if let oldAt = old.createdAtWire, let shownAt = d.interactionCreatedAt, oldAt != shownAt {
                return false
            }
            return true
        }.map(\.identifier)
    }

    /// The notifications a group's whole list says are over: delivered before that list was asked for (a later push
    /// can be ahead of its own event) and not pending now. Groups without such a list (`listedAt`) are left alone:
    /// the cached list of the last run says nothing about what happened since. Never removes assistant cards or to-do
    /// asks, which never appear in the interaction card list (#13916, #14388).
    static func stale(listedAt: [String: Date], pending: Set<String>, delivered: [Delivered]) -> [String] {
        delivered.filter { d in
            guard !d.ownerCard, let at = listedAt[d.group], let uid = d.uid else { return false }
            return d.date < at && !pending.contains(uid)
        }.map(\.identifier)
    }

    struct Resolved: Sendable, Equatable {
        var interactionID: String
        var interactionCreatedAt: String?
    }

    /// The notifications of the cards or to-do asks a hub's withdrawal push names (#4349, #13916, #14388), in that hub's
    /// group (nil: the push did not say whose, so matching ids in any group). An occurrence-aware interaction entry leaves
    /// a known same-ID replacement alone; an alert from an older hub has no occurrence and retains the legacy behavior.
    /// Pending or not: the hub knows they are over, and a suspended app's list is the one from before.
    static func resolvedOccurrences(_ cards: [Resolved], group: String?, delivered: [Delivered]) -> [String] {
        var overTodos: Set<String> = []
        var interactionCards: [Resolved] = []
        for card in cards {
            if let todo = todoID(fromResolved: card.interactionID) {
                overTodos.insert(todo)
                interactionCards.append(card) // Also collect an older app's interaction-shaped todo-ask alert.
            } else if !card.interactionID.isEmpty, !card.interactionID.hasPrefix(todoAskPrefix) {
                interactionCards.append(card)
            }
        }
        guard !interactionCards.isEmpty || !overTodos.isEmpty else { return [] }
        return delivered.filter { d in
            guard group == nil || d.group == group else { return false }
            switch d.target {
            case .interaction(let id):
                return interactionCards.contains(where: {
                    $0.interactionID == id &&
                        ($0.interactionCreatedAt == nil || d.interactionCreatedAt == nil ||
                         $0.interactionCreatedAt == d.interactionCreatedAt)
                })
            case .todo(let id): return overTodos.contains(id)
            }
        }.map(\.identifier)
    }

    /// Legacy ID-only withdrawal entries, including cards and to-do asks that are not pending interactions.
    static func resolved(_ ids: [String], group: String?, delivered: [Delivered]) -> [String] {
        resolvedOccurrences(ids.map { Resolved(interactionID: $0, interactionCreatedAt: nil) }, group: group, delivered: delivered)
    }

    /// What the notification center gave, read where it calls back (off the main thread): the pushes about an
    /// interaction, assistant card, or to-do ask, with the session or hub metadata whose group the app resolves on the
    /// main actor.
    struct Raw: Sendable, Equatable {
        var identifier: String
        var target: Target
        var date: Date
        /// The occurrence its alert drew; nil from an older hub or for a to-do ask.
        var interactionCreatedAt: String? = nil
        var ref: PushRef?
        var hubID: String?
        var sealedGroup: String?
        var ownerCard = false

        @MainActor func group(_ model: AppModel) -> String {
            if let ref { return ref.key(model).group }
            var info: [AnyHashable: Any] = [:]
            if let hubID { info["hub_id"] = hubID }
            if let sealedGroup { info[PushCrypto.groupKey] = sealedGroup }
            return model.pushGroup(info) ?? ""
        }
    }

    static func raw(_ notifications: [UNNotification]) -> [Raw] {
        notifications.compactMap { n in
            raw(identifier: n.request.identifier, date: n.date, userInfo: n.request.content.userInfo)
        }
    }

    /// Extracts a trackable interaction, assistant card (`card_id`), or to-do target from a delivered notification's
    /// `userInfo`.
    static func raw(identifier: String, date: Date, userInfo info: [AnyHashable: Any]) -> Raw? {
        // An assistant card's push names its card as card_id (a plan's, from before, as interaction_id only).
        if let rawCard = info["card_id"] as? String {
            let card = rawCard.trimmingCharacters(in: .whitespacesAndNewlines)
            if !card.isEmpty {
                if let todo = todoID(fromResolved: card) {
                    return Raw(identifier: identifier, target: .todo(todo), date: date, ref: PushRef(info),
                               hubID: info["hub_id"] as? String, sealedGroup: PushCrypto.group(from: info),
                               ownerCard: true)
                }
                if !card.hasPrefix(todoAskPrefix) {
                    return Raw(identifier: identifier, target: .interaction(card), date: date,
                               interactionCreatedAt: info["interaction_created_at"] as? String, ref: PushRef(info),
                               hubID: info["hub_id"] as? String, sealedGroup: PushCrypto.group(from: info),
                               ownerCard: true)
                }
            }
        }
        if let id = info["interaction_id"] as? String, !id.isEmpty {
            guard let ref = PushRef(info) else { return nil }
            return Raw(identifier: identifier, target: .interaction(id), date: date,
                       interactionCreatedAt: info["interaction_created_at"] as? String, ref: ref,
                       hubID: info["hub_id"] as? String, sealedGroup: PushCrypto.group(from: info),
                       ownerCard: false)
        }
        if let todo = todoID(in: info) {
            return Raw(identifier: identifier, target: .todo(todo), date: date, ref: PushRef(info),
                       hubID: info["hub_id"] as? String, sealedGroup: PushCrypto.group(from: info),
                       ownerCard: true)
        }
        return nil
    }

    /// To-do id carried by a decision or needs-user push: top-level `todo_id`, or `todo.id` on a decision push from
    /// an older hub before #13916.
    private static func todoID(in info: [AnyHashable: Any]) -> String? {
        if let id = info["todo_id"] as? String {
            let trimmed = id.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmed.isEmpty { return trimmed }
        }
        if let todo = info["todo"] as? [String: Any], let id = todo["id"] as? String {
            let trimmed = id.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmed.isEmpty { return trimmed }
        }
        return nil
    }
}

extension AppModel {
    /// The list lost these interactions: an `interaction_resolved`, an answer in the app, a group's whole list replacing
    /// it, a group forgotten. Their old values carry the occurrence, so a replacement's already-delivered alert stays.
    func withdrawSettled(_ gone: [String: Interaction]) {
        guard !gone.isEmpty else { return }
        withdrawDelivered { model, delivered in
            NotificationWithdrawal.settledOccurrences(gone: gone, pending: model.interactions, delivered: delivered)
        }
    }

    /// A group's whole list came (reconnected, back in front): the pushes delivered before it that it does not have.
    func withdrawStaleNotifications() {
        guard !interactionsListedAt.isEmpty else { return }
        withdrawDelivered { model, delivered in
            NotificationWithdrawal.stale(listedAt: model.interactionsListedAt, pending: Set(model.interactions.keys),
                                         delivered: delivered)
        }
    }

    /// A hub's occurrence-bound withdrawal push (#4349): the cards and to-do asks it names are over, answered or withdrawn
    /// elsewhere while the app was suspended. `done` runs on the main queue once their notifications are gone.
    func withdrawResolvedOccurrences(_ cards: [NotificationWithdrawal.Resolved], group: String?, done: @escaping @Sendable () -> Void) {
        withdrawDelivered({ _, delivered in NotificationWithdrawal.resolvedOccurrences(cards, group: group, delivered: delivered) }, done: done)
    }

    /// The legacy ID-only form from an older hub (#13916 includes to-do asks).
    func withdrawResolved(_ ids: [String], group: String?, done: @escaping @Sendable () -> Void) {
        withdrawDelivered({ _, delivered in NotificationWithdrawal.resolved(ids, group: group, delivered: delivered) }, done: done)
    }

    /// A `todo-ask:<todo id>` resolution over the socket (#13916): takes back any delivered decision or needs-user
    /// push for that to-do in `group`, without touching interaction-card state.
    func withdrawTodoAsk(_ id: String, group: String) -> Bool {
        guard NotificationWithdrawal.todoID(fromResolved: id) != nil else { return false }
        withdrawResolved([id], group: group) {}
        return true
    }

    /// Reads what the notification center shows and removes what `pick` names. The center answers on its own queue;
    /// the groups are resolved, and the list read, back on the main actor.
    private func withdrawDelivered(_ pick: @escaping @Sendable @MainActor (AppModel, [NotificationWithdrawal.Delivered]) -> [String],
                                   done: (@Sendable () -> Void)? = nil) {
        UNUserNotificationCenter.current().getDeliveredNotifications { @Sendable list in
            let raw = NotificationWithdrawal.raw(list)
            guard !raw.isEmpty else {
                if let done { DispatchQueue.main.async(execute: done) }
                return
            }
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    let model = self
                    let delivered = raw.map {
                        NotificationWithdrawal.Delivered(identifier: $0.identifier, group: $0.group(model),
                                                         target: $0.target, date: $0.date,
                                                         interactionCreatedAt: $0.interactionCreatedAt, ownerCard: $0.ownerCard)
                    }
                    let ids = pick(model, delivered)
                    if !ids.isEmpty { UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ids) }
                    done?()
                }
            }
        }
    }
}
