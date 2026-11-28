import Foundation
import UserNotifications

/// Buttons on interaction pushes. The hub sends `category: INTERACTION_<KIND>` (server/internal/hub/push.go), and
/// `SESSION_DONE` on a finished turn; these are the categories it names. Anything that says yes to the agent needs the
/// phone unlocked; saying no does not.
nonisolated enum NotificationActions {
    static let allow = "IX_ALLOW"
    static let deny = "IX_DENY"
    static let approvePlan = "IX_APPROVE_PLAN"
    static let rejectPlan = "IX_REJECT_PLAN"
    /// A finished turn's push: a message to the session, typed on the notification (#3449).
    static let reply = "SESSION_REPLY"
    static let doneCategory = "SESSION_DONE"
    /// A decision's push (#2056, `decideCategory` in server/internal/hub/push_payload.go): its one button takes the
    /// recommendation the push names.
    static let decideCategory = "TODO_DECIDE"
    static let takeRecommendation = "TODO_TAKE_RECOMMENDATION"

    static func category(_ kind: InteractionKind) -> String { "INTERACTION_" + kind.rawValue.uppercased() }

    static var categories: Set<UNNotificationCategory> {
        let permission = UNNotificationCategory(
            identifier: category(.permission),
            actions: [
                UNNotificationAction(identifier: allow, title: String(localized: "Allow [Notification Actions ce7ef2]"), options: [.authenticationRequired],
                                     icon: UNNotificationActionIcon(systemImageName: "checkmark")),
                UNNotificationAction(identifier: deny, title: String(localized: "Deny"), options: [.destructive],
                                     icon: UNNotificationActionIcon(systemImageName: "xmark")),
            ],
            intentIdentifiers: [], options: [])
        let plan = UNNotificationCategory(
            identifier: category(.plan),
            actions: [
                UNNotificationAction(identifier: approvePlan, title: String(localized: "Approve [Notification Actions 8cbe69]"), options: [.authenticationRequired],
                                     icon: UNNotificationActionIcon(systemImageName: "checkmark")),
                UNTextInputNotificationAction(identifier: rejectPlan, title: String(localized: "Send Back [Notification Actions cd3150]"), options: [.authenticationRequired],
                                              icon: UNNotificationActionIcon(systemImageName: Theme.Icon.undo),
                                              textInputButtonTitle: String(localized: "Send Back [Notification Actions a20f13]"), textInputPlaceholder: String(localized: "Tell the agent what to change…")),
            ],
            intentIdentifiers: [], options: [])
        // Questions and terminal prompts need the screen: no buttons, a tap opens the session. Registered anyway so
        // the system knows the identifiers the hub sends.
        let question = UNNotificationCategory(identifier: category(.question), actions: [], intentIdentifiers: [], options: [])
        let screen = UNNotificationCategory(identifier: category(.screen), actions: [], intentIdentifiers: [], options: [])
        // An MCP form or link (#2433) is filled in or opened in the app: no buttons either, a tap opens its conversation.
        let elicitation = UNNotificationCategory(identifier: category(.elicitation), actions: [], intentIdentifiers: [], options: [])
        // A message is an instruction to the agent: unlocked, like approving a plan.
        let done = UNNotificationCategory(
            identifier: doneCategory,
            actions: [
                UNTextInputNotificationAction(identifier: reply, title: String(localized: "Reply"), options: [.authenticationRequired],
                                              icon: UNNotificationActionIcon(systemImageName: Theme.Icon.reply),
                                              textInputButtonTitle: String(localized: "Send"), textInputPlaceholder: String(localized: "What next…")),
            ],
            intentIdentifiers: [], options: [])
        // A decision that recommends an option: taking it answers the to-do's round, so unlocked, like approving a plan.
        let decide = UNNotificationCategory(
            identifier: decideCategory,
            actions: [
                UNNotificationAction(identifier: takeRecommendation, title: String(localized: "Take Recommendation"), options: [.authenticationRequired],
                                     icon: UNNotificationActionIcon(systemImageName: Theme.Icon.done)),
            ],
            intentIdentifiers: [], options: [])
        // Pairing receipts (#4199): Not you? Revoke, It's Mine / Not Mine (PairReceiptActions).
        var all = Set([permission, plan, question, screen, elicitation, done, decide, WaterReminderActions.notificationCategory] + PairReceiptActions.categories)
        #if os(iOS)
        // A calendar meeting's reminder (#8731 G2): Start (MeetingReminders).
        all.insert(MeetingReminders.notificationCategory)
        #endif
        return all
    }

    /// What a button sends to the hub, or nil for a plain tap (and anything we don't know).
    /// Plans approve in acceptEdits: from a notification you have not read the plan in full, so edits go through but
    /// commands still ask. "Approve & Auto-Run" stays an in-app choice.
    static func resolution(action: String, text: String? = nil) -> Resolution? {
        switch action {
        case allow: Resolution(allow: true)
        case deny: Resolution(allow: false)
        case approvePlan: Resolution(approve: true, mode: "acceptEdits")
        case rejectPlan: Resolution(approve: false, feedback: text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "")
        default: nil
        }
    }

    /// Whether a button may settle the displayed occurrence: not one its task's supervisor holds, and not a same-ID
    /// replacement the app already knows about. An unknown card is left to the hub, with the occurrence header when present.
    static func answerable(_ id: String, occurrence: String? = nil, among cards: some Sequence<Interaction>) -> Bool {
        for card in cards where card.id == id {
            if let occurrence, card.createdAtWire != occurrence { return false }
            if !card.answerableHere { return false }
        }
        return true
    }

    /// What Take Recommendation on a decision's push answers: the to-do, the round it was asked in (sent back exactly as
    /// the push carried it, so an older round's button cannot answer a newer one) and the manager whose to-do it is.
    struct Decision: Equatable, Sendable {
        var todoID: String
        var askedAt: String
        var manager: String?
    }

    /// The decision a button answers, from the push's `todo` (decidePayloadIn); nil for any other button, and for a push
    /// without the to-do or its round.
    static func decision(action: String, info: [AnyHashable: Any]) -> Decision? {
        guard action == takeRecommendation, let todo = info["todo"] as? [String: Any],
              let id = todo["id"] as? String, !id.isEmpty,
              let askedAt = todo["asked_at"] as? String, !askedAt.isEmpty else { return nil }
        let manager = (todo["manager"] as? String).flatMap { $0.isEmpty ? nil : $0 }
        return Decision(todoID: id, askedAt: askedAt, manager: manager)
    }

    /// Takes the recommendation (`POST /api/v1/todos/{id}/accept`), naming the to-do's manager so it reaches the right
    /// one whichever is the default; a failure (a 409: the round was answered or asked again) comes back as a local
    /// notification.
    static func take(_ decision: Decision, group: String?) async {
        do {
            var client = try HubStore.client(group: group)
            if let manager = decision.manager { client.managerID = manager }
            try await AppModel.shared.outside(client) { try await $0.acceptTodo(decision.todoID, askedAt: decision.askedAt) }
            await AppModel.shared.withdrawResolved([NotificationWithdrawal.todoAskPrefix + decision.todoID], group: group) {}
        } catch {
            await reportFailure(id: decision.todoID, title: failureTitle(action: takeRecommendation), error: error)
        }
    }

    /// The message a Reply on a finished turn's push sends, nil for anything else or an empty one.
    static func replyText(action: String, text: String?) -> String? {
        guard action == reply, let t = text?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        return t
    }

    /// Sends a reply typed on the notification to its session, as the composer would (a new client_msg_id, so a
    /// retry by the system is typed once); a failure comes back as a local notification.
    static func sendReply(_ key: SessionKey, text: String) async {
        let id = UUID()
        do {
            try await AppModel.shared.outside(HubStore.client(group: key.group)) {
                try await $0.send(key, text: text, clientMsgID: id.uuidString, opID: id)
            }
        } catch {
            await reportFailure(id: id.uuidString, title: failureTitle(action: reply), error: error)
        }
    }

    /// Posts "Couldn't allow · This was already handled." and the like: buttons run while nobody looks at the app.
    static func reportFailure(id: String, title: String, error: Error) async {
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = IntentFailure.sentence(for: error, host: HubStore.load().hubs.first?.config.url.host())

        try? await UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "resolve-failed-\(id)", content: content, trigger: nil))
    }

    /// Takes the session's notifications out of Notification Center once it is on screen (#3449): its pushes carry
    /// `thread-id` = the session id, and `machine_id` tells two machines' sessions apart.
    static func clearDelivered(_ key: SessionKey) {
        // Called back on the center's own queue: nothing in it needs the main actor.
        UNUserNotificationCenter.current().getDeliveredNotifications { @Sendable delivered in
            let ids = delivered.filter {
                $0.request.content.threadIdentifier == key.sessionID
                    && ($0.request.content.userInfo["machine_id"] as? String).map { $0 == key.machineID } != false
            }.map(\.request.identifier)
            if !ids.isEmpty { UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ids) }
        }
    }

    /// Shown (as a local notification) when a button could not be carried out: the phone was locked in a drawer
    /// while the agent moved on, the hub was unreachable, and so on.
    static func failureTitle(action: String) -> String {
        switch action {
        case allow: String(localized: "Couldn't allow")
        case deny: String(localized: "Couldn't deny")
        case approvePlan: String(localized: "Couldn't approve the plan")
        case rejectPlan: String(localized: "Couldn't send back the plan")
        case reply: String(localized: "Couldn't send the reply")
        case takeRecommendation: String(localized: "Couldn't take the recommendation")
        default: String(localized: "Couldn't complete the action")
        }
    }
}
