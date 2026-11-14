import Foundation
import UserNotifications
import PersonaWire

/// `phone_notify`: post a local notification, now or after a delay.
enum NotificationsTool {
    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_notify",
                    description: """
                    Post a notification on the phone. Use it to tell the user about something that \
                    happened while they were not looking (a long task finished, a build failed). \
                    delay_seconds schedules it a few seconds or minutes later; omit it to notify \
                    immediately. The first call asks for notification permission. Returns whether the \
                    notification was scheduled.
                    """,
                    inputSchema: Schema.object([
                        "title": Schema.string("Notification title, short."),
                        "body": Schema.string("Notification body."),
                        "delay_seconds": Schema.integer("Seconds from now to deliver it. Default 0 (immediately), max 86400.", min: 0, max: 86_400),
                    ], required: ["title", "body"])
                ),
                source: .notifications,
                run: { args in try await notify(args) }
            )
        ]
    }

    static func notify(_ args: JSONValue) async throws -> [ToolContent] {
        guard let title = ToolArgs.string(args, "title") else {
            throw ToolFailure(.invalidArgument, "title is required")
        }
        guard let body = ToolArgs.string(args, "body") else {
            throw ToolFailure(.invalidArgument, "body is required")
        }
        let delay = ToolArgs.int(args, "delay_seconds", fallback: 0, min: 0, max: 86_400)

        let center = UNUserNotificationCenter.current()
        var status = await center.notificationSettings().authorizationStatus
        if status == .notDetermined {
            _ = try? await center.requestAuthorization(options: [.alert, .sound, .badge])
            status = await center.notificationSettings().authorizationStatus
            await NotificationsAccess.refresh()
        }
        #if os(iOS) || os(visionOS)
        let allowed = status == .authorized || status == .provisional || status == .ephemeral
        #else
        let allowed = status == .authorized || status == .provisional // no App Clips on a Mac
        #endif
        guard allowed else {
            throw ToolFailure(.denied,
                "通知 / Notifications. The user has turned notifications off for Persona in iOS Settings, " // i18n-ignore
                    + "so nothing was shown.")
        }

        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        content.sound = .default

        let trigger: UNNotificationTrigger? = delay > 0
            ? UNTimeIntervalNotificationTrigger(timeInterval: TimeInterval(delay), repeats: false)
            : nil
        let request = UNNotificationRequest(identifier: "persona-\(UUID().uuidString)", content: content, trigger: trigger)
        try await center.add(request)

        return Output.json(Output.object([
            ("scheduled", .bool(true)),
            ("delay_seconds", .number(Double(delay))),
        ]))
    }
}
