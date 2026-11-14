import Foundation
import UserNotifications
import PersonaWire

/// Takes a value-type snapshot on the delegate callback thread so the system's mutable userInfo never reaches the MainActor.
nonisolated struct ExtensionNotificationEvent: Sendable {
    var notificationID: String
    var payload: ExtensionNotification
    var action: ExtensionNotifyAction?
    var values: [String: String]
    var hubID: String?
    var sealedGroup: String?
    var dismissed: Bool
    var valid: Bool

    init?(id: String, info: [AnyHashable: Any], category: String, actionID: String, text: String?) {
        guard let payload = ExtensionNotificationActions.payload(info) else { return nil }
        self.notificationID = id
        self.payload = payload
        hubID = info["hub_id"] as? String
        sealedGroup = info[PushCrypto.groupKey] as? String
        dismissed = actionID == UNNotificationDismissActionIdentifier
        action = ExtensionNotificationActions.action(actionID, payload: payload)
        values = action?.input != nil ? ["text": text ?? ""] : [:]
        valid = dismissed || actionID == UNNotificationDefaultActionIdentifier
            || (action != nil && ExtensionNotificationActions.category(identifier: category, payload: payload) != nil)
        if action != nil && payload.ui == nil { valid = false }
        if (text?.utf8.count ?? 0) > 16_384 || (payload.state?.utf8.count ?? 0) > 16_384 { valid = false }
    }

    /// Once the source is known it never falls back to the current hub; with no source marked, only a single group is allowed.
    func group(hubGroups: [String: String], groups: [String]) -> String? {
        if let hubID {
            guard let group = hubGroups[hubID], groups.contains(group), sealedGroup == nil || sealedGroup == group else { return nil }
            return group
        }
        if let sealedGroup { return groups.contains(sealedGroup) ? sealedGroup : nil }
        return groups.count == 1 ? groups.first : nil
    }
}
