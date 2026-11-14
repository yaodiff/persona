import Foundation
import Testing
import UserNotifications
@testable import Persona
import PersonaWire

@MainActor struct ExtensionNotificationActionsTests {
    @Test func aCategoryKeepsTextInputAndDestructiveForegroundOptions() throws {
        let payload = ExtensionNotification(extension: "sample", ui: "home", state: "range=day", actions: [
            .init(id: "reply", title: "回复", input: .init(placeholder: "说点什么")),
            .init(id: "stop", title: "停用", role: "destructive", foreground: true)])
        let category = try #require(ExtensionNotificationActions.category(identifier: "ext.sample.hash", payload: payload))
        #expect(category.actions.count == 2)
        let input = try #require(category.actions.first as? UNTextInputNotificationAction)
        #expect(input.textInputPlaceholder == "说点什么" && input.textInputButtonTitle == "发送")
        #expect(category.actions[1].options.contains(.destructive))
        #expect(category.actions[1].options.contains(.foreground) && category.actions[1].options.contains(.authenticationRequired))
        #expect(ExtensionNotificationActions.category(identifier: "INTERACTION_PERMISSION", payload: payload) == nil)
    }

    @Test func invalidCategoriesAndReservedSystemActionIDsAreRejected() {
        let repeated = ExtensionNotification(extension: "sample", actions: [.init(id: "same", title: "一"), .init(id: "same", title: "二")])
        #expect(ExtensionNotificationActions.category(identifier: "ext.sample.hash", payload: repeated) == nil)
        let reserved = ExtensionNotification(extension: "sample", actions: [.init(id: UNNotificationDefaultActionIdentifier, title: "执行")])
        #expect(ExtensionNotificationActions.category(identifier: "ext.sample.hash", payload: reserved) == nil)
        let unknown = ExtensionNotification(extension: "sample", actions: [.init(id: "run", title: "执行", role: "unknown")])
        #expect(ExtensionNotificationActions.category(identifier: "ext.sample.hash", payload: unknown) == nil)
    }

    @Test func malformedExecutionFlagsAreNotDecodedAsBackgroundButtons() {
        let malformed: [AnyHashable: Any] = ["extension_notification": ["extension": "sample", "ui": "home", "actions": [
            ["id": "run", "title": "执行", "foreground": "true"]]]]
        #expect(ExtensionNotificationActions.payload(malformed) == nil)
    }

    @Test func registrationAndAppStartupPreserveExistingCategories() async throws {
        let center = UNUserNotificationCenter.current()
        let identifier = "ext.sample." + UUID().uuidString
        let content = UNMutableNotificationContent()
        content.categoryIdentifier = identifier
        content.userInfo = ["extension_notification": ["extension": "sample", "ui": "home", "actions": [["id": "reply", "title": "回复"]]]]
        nonisolated(unsafe) let ready = content.copy() as! UNNotificationContent
        await ExtensionNotificationActions.register(content: ready)
        await ExtensionNotificationActions.preserveAndRegister { NotificationActions.categories }
        let categories = await center.notificationCategories()
        #expect(categories.contains { $0.identifier == identifier && $0.actions.first?.identifier == "reply" })
        #expect(NotificationActions.categories.allSatisfy { item in categories.contains { $0.identifier == item.identifier } })
    }

    @Test func payloadDoesNotAllowPathSegmentsToEscapeTheirExtension() {
        #expect(ExtensionNotificationActions.payload(["extension_notification": ["extension": "sample", "ui": "home"]])?.ui == "home")
        #expect(ExtensionNotificationActions.payload(["extension_notification": ["extension": "../../admin", "ui": "home"]]) == nil)
        #expect(ExtensionNotificationActions.payload(["extension_notification": ["extension": "sample", "ui": "../settings"]]) == nil)
    }
}
