import Foundation
import Observation
import UIKit
import UserNotifications

/// The number on the app's icon on the home screen (#5824; Apple Vision Pro runs the same).
/// It is the tab bar's badges added up: the questions waiting in Persona's conversations (`TabBadges.assistant`), the
/// agents in Needs You (`TabBadges.agents`) and the Inbox's unread items (`InboxStore.unreadCount`). So the icon never
/// says more or less than the tabs (docs/design/PRINCIPLES.md, "Consistency first"): what they leave out (muted agents,
/// an agent's finished turn not yet opened, open to-dos, an extension's badge) it leaves out too.
nonisolated enum AppIconBadge {
    /// How long a change waits for the ones after it before the icon is set: pushes land in bursts, and a number on
    /// the icon is not watched tick by tick (docs/apple/performance.md P7).
    static let coalesce: TimeInterval = 0.25

    /// Nothing while no hub is paired: a signed-out app keeps no number from the hub it left.
    static func count(_ tabs: TabBadges, inboxUnread: Int, paired: Bool) -> Int {
        guard paired else { return 0 }
        return max(0, tabs.assistant) + max(0, tabs.agents) + max(0, inboxUnread)
    }

    /// The Dock's label: none at zero.
    static func label(_ n: Int) -> String? { n > 0 ? "\(n)" : nil }

    /// One serial queue: numbers set one after another reach the system in that order. Off the main actor, as
    /// `setBadgeCount` is a call to the notification daemon (docs/apple/performance.md P1). Without the user's
    /// permission for badges (asked with the notifications, `PushRegistrar`) the system shows nothing.
    private static let queue = DispatchQueue(label: "persona.app-icon-badge", qos: .utility)

    @MainActor static func show(_ n: Int) {
        queue.async { UNUserNotificationCenter.current().setBadgeCount(n) }
    }
}

/// Keeps the icon's number in step with the tabs while the app runs, window open or not. The app sets it itself, so a
/// build without remote push has it too; while the app is not running the icon keeps the last number set.
@MainActor final class AppIconBadgeWatcher {
    static let shared = AppIconBadgeWatcher(wait: AppIconBadge.coalesce, show: AppIconBadge.show)

    private let wait: TimeInterval
    private let show: @MainActor (Int) -> Void
    private var read: (() -> Int)?
    /// The number last handed to the system; nil before the first.
    private(set) var shown: Int?

    init(wait: TimeInterval, show: @escaping @MainActor (Int) -> Void) {
        self.wait = wait
        self.show = show
    }

    func start(_ model: AppModel) {
        start {
            #if os(iOS)
            let inbox = model.hiddenNavigationPages.contains(.inbox) ? 0 : InboxStore.shared.unreadCount
            #else
            let inbox = InboxStore.shared.unreadCount
            #endif
            return AppIconBadge.count(model.badges, inboxUnread: inbox, paired: model.isPaired)
        }
    }

    /// Reads the number now and again `wait` after anything it read changes; the system hears only a number that moved.
    /// `model.badges` is published only when one of its numbers changes, so a push that moves none costs nothing here.
    func start(reading read: @escaping () -> Int) {
        guard self.read == nil else { return }
        self.read = read
        observe()
        // A number set before the user allowed badges (the question comes after pairing, when the counts arrive) is
        // dropped; the answer, or the Badges switch turned on in Settings, brings the app back to the front.
        NotificationCenter.default.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) {
            @Sendable [weak self] _ in MainActor.assumeIsolated { self?.resend() }
        }
    }

    /// Tells the system the current number again, moved or not.
    func resend() {
        guard let shown else { return }
        show(shown)
    }

    private func observe() {
        guard let read else { return }
        let n = withObservationTracking(read) { [weak self, wait] in
            // Called once, before the change lands: whatever else changes until the read below is taken with it.
            DispatchQueue.main.asyncAfter(deadline: .now() + wait) { MainActor.assumeIsolated { self?.observe() } }
        }
        guard n != shown else { return }
        shown = n
        show(n)
    }
}
