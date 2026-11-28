import Foundation
import UserNotifications

/// What the activity's buttons do (FleetIntents.swift), in the app process: resolve through the hub, take the request
/// off the activity at once, and say so in a notification when it did not work (nobody is looking at the app).
nonisolated enum FleetActions {
    enum Action { case allow, deny, approvePlan }

    static func resolution(_ action: Action) -> Resolution {
        switch action {
        case .allow: NotificationActions.resolution(action: NotificationActions.allow)!
        case .deny: NotificationActions.resolution(action: NotificationActions.deny)!
        case .approvePlan: NotificationActions.resolution(action: NotificationActions.approvePlan)!
        }
    }

    /// `interactionID` as the activity has it: "group|id" for another group's request (FleetMerge), the hub's id alone
    /// for the default group's or one the hub pushed (the hub the activity's token went to: the first group).
    static func run(_ action: Action, _ interactionID: String, occurrence: String? = nil) async {
        do {
            let (named, id) = FleetMerge.split(interactionID)
            let r = resolution(action)
            try await AppModel.shared.outside(HubStore.client(group: named)) { try await $0.resolve(id, r, occurrence: occurrence) }
            await LiveActivityController.shared.resolvedLocally(interactionID, occurrence: occurrence)
        } catch {
            if (error as? APIError)?.status == 410 { // already settled elsewhere: just drop that occurrence from the activity
                await LiveActivityController.shared.resolvedLocally(interactionID, occurrence: occurrence)
            }
            let title = switch action {
            case .allow: NotificationActions.failureTitle(action: NotificationActions.allow)
            case .deny: NotificationActions.failureTitle(action: NotificationActions.deny)
            case .approvePlan: NotificationActions.failureTitle(action: NotificationActions.approvePlan)
            }
            await NotificationActions.reportFailure(id: interactionID, title: title, error: error)
        }
    }
}
