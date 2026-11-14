import Foundation
import UserNotifications
import PersonaWire

/// Button definitions shared by the notification service extension and the app: decoded and registered only, no credentials read, no actions run.
nonisolated enum ExtensionNotificationActions {
    static func payload(_ info: [AnyHashable: Any]) -> ExtensionNotification? {
        guard let raw = info["extension_notification"] as? [String: Any],
              let data = try? JSONSerialization.data(withJSONObject: raw),
              (try? JSONDecoder().decode(Shape.self, from: data)) != nil,
              let payload = try? JSONDecoder().decode(ExtensionNotification.self, from: data),
              segment(payload.extension), payload.ui.map(segment) ?? true else { return nil }
        return payload
    }

    static func category(identifier: String, payload: ExtensionNotification) -> UNNotificationCategory? {
        let prefix = "ext." + payload.extension + "."
        guard identifier.hasPrefix(prefix), identifier.count > prefix.count, identifier.utf8.count <= 256,
              let actions = payload.actions, !actions.isEmpty, actions.count <= 3,
              Set(actions.map(\.id)).count == actions.count else { return nil }
        var buttons: [UNNotificationAction] = []
        for action in actions {
            guard !action.id.isEmpty, action.id.count <= 128,
                  ![UNNotificationDefaultActionIdentifier, UNNotificationDismissActionIdentifier].contains(action.id),
                  !action.title.isEmpty, action.title.count <= 40,
                  action.role == nil || ["normal", "destructive"].contains(action.role!) else { return nil }
            var options: UNNotificationActionOptions = []
            if action.role == "destructive" { options.insert(.destructive) }
            if action.foreground == true { options.formUnion([.foreground, .authenticationRequired]) }
            if let input = action.input {
                buttons.append(UNTextInputNotificationAction(identifier: action.id, title: action.title, options: options,
                    textInputButtonTitle: String(localized: "Send"), textInputPlaceholder: input.placeholder ?? String(localized: "Type here")))
            } else {
                buttons.append(UNNotificationAction(identifier: action.id, title: action.title, options: options))
            }
        }
        return UNNotificationCategory(identifier: identifier, actions: buttons, intentIdentifiers: [], options: [])
    }

    static func register(content: UNNotificationContent) async {
        guard let payload = payload(content.userInfo), category(identifier: content.categoryIdentifier, payload: payload) != nil else { return }
        await ExtensionNotificationCategoryRegistry.shared.register(payload: payload, identifier: content.categoryIdentifier)
    }

    static func preserveAndRegister(_ make: @escaping @Sendable () -> Set<UNNotificationCategory>) async {
        await ExtensionNotificationCategoryRegistry.shared.mergeMade(make)
    }

    static func action(_ id: String, payload: ExtensionNotification) -> ExtensionNotifyAction? {
        guard id != UNNotificationDefaultActionIdentifier, id != UNNotificationDismissActionIdentifier else { return nil }
        return payload.actions?.first { $0.id == id }
    }

    /// Generated types decode new fields leniently; a notification's run flags are type-checked strictly, so a malformed foreground is never taken as false.
    private struct Shape: Decodable {
        var `extension`: String
        var ui: String?
        var state: String?
        var actions: [ActionShape]?
    }
    private struct ActionShape: Decodable {
        var id: String
        var title: String
        var role: String?
        var foreground: Bool?
        var input: InputShape?
    }
    private struct InputShape: Decodable { var placeholder: String? }

    private static func segment(_ value: String) -> Bool {
        let characters = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-")
        return !value.isEmpty && value.count <= 128 && value != "." && value != ".."
            && value.unicodeScalars.allSatisfy(characters.contains)
    }
}

/// Merges run one at a time within the process; the system's registered static categories and other extensions' categories are kept.
private actor ExtensionNotificationCategoryRegistry {
    static let shared = ExtensionNotificationCategoryRegistry()
    private var known: Set<UNNotificationCategory>?

    func register(payload: ExtensionNotification, identifier: String) async {
        guard let category = ExtensionNotificationActions.category(identifier: identifier, payload: payload) else { return }
        await merge([category])
    }

    func mergeMade(_ make: @Sendable () -> Set<UNNotificationCategory>) async {
        await merge(make())
    }

    private func merge(_ additions: Set<UNNotificationCategory>) async {
        // SDK objects are fetched and created only inside the actor; the notification center and categories are never passed in from the caller.
        let center = UNUserNotificationCenter.current()
        let existing = await center.notificationCategories()
        var categories: [String: UNNotificationCategory] = [:]
        for category in known ?? [] { categories[category.identifier] = category }
        // Read the system's set again to pick up categories the notification service extension just registered; on a name clash this definition wins.
        for category in existing { categories[category.identifier] = category }
        for category in additions { categories[category.identifier] = category }
        let merged = Set(categories.values)
        known = merged
        center.setNotificationCategories(merged)
    }
}
