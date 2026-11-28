import AppIntents
import Foundation
import OSLog

// The buttons on the Fleet activity (docs/apple/live-activity.md). LiveActivityIntent runs `perform` in the app's process
// (launched in the background if needed), so the extension only needs the types to draw `Button(intent:)`; the bodies
// that talk to the hub are app-only. Saying yes needs the phone unlocked, saying no does not, as on notifications.

struct AllowPermissionIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Allow [Notification Actions ce7ef2]"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .requiresAuthentication }

    @Parameter(title: "Interaction") var interactionID: String?
    @Parameter(title: "Interaction Occurrence") var interactionCreatedAt: String?

    nonisolated init() {}
    nonisolated init(interactionID: String?, interactionCreatedAt: String? = nil) {
        self.interactionID = interactionID
        self.interactionCreatedAt = interactionCreatedAt
    }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        guard let interactionID, !interactionID.isEmpty else {
            Logger(subsystem: "persona", category: "live-activity").warning("AllowPermissionIntent performed without interactionID")
            return .result()
        }
        await FleetActions.run(.allow, interactionID, occurrence: interactionCreatedAt)
        #endif
        return .result()
    }
}

struct DenyPermissionIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Deny"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Interaction") var interactionID: String?
    @Parameter(title: "Interaction Occurrence") var interactionCreatedAt: String?

    nonisolated init() {}
    nonisolated init(interactionID: String?, interactionCreatedAt: String? = nil) {
        self.interactionID = interactionID
        self.interactionCreatedAt = interactionCreatedAt
    }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        guard let interactionID, !interactionID.isEmpty else {
            Logger(subsystem: "persona", category: "live-activity").warning("DenyPermissionIntent performed without interactionID")
            return .result()
        }
        await FleetActions.run(.deny, interactionID, occurrence: interactionCreatedAt)
        #endif
        return .result()
    }
}

/// Approves a plan in acceptEdits, like the notification button: from the lock screen you have not read the plan in
/// full, so commands still ask. "Approve & Auto-Run" stays in the app.
struct ApprovePlanIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Approve [Notification Actions 8cbe69]"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .requiresAuthentication }

    @Parameter(title: "Interaction") var interactionID: String?
    @Parameter(title: "Interaction Occurrence") var interactionCreatedAt: String?

    nonisolated init() {}
    nonisolated init(interactionID: String?, interactionCreatedAt: String? = nil) {
        self.interactionID = interactionID
        self.interactionCreatedAt = interactionCreatedAt
    }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        guard let interactionID, !interactionID.isEmpty else {
            Logger(subsystem: "persona", category: "live-activity").warning("ApprovePlanIntent performed without interactionID")
            return .result()
        }
        await FleetActions.run(.approvePlan, interactionID, occurrence: interactionCreatedAt)
        #endif
        return .result()
    }
}
