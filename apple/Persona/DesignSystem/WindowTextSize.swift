#if os(iOS) || os(visionOS)
import SwiftUI
import UIKit

/// The interface's cap (`Theme.interfaceLargestTypeSize`) on the windows' own text size. The environment's cap does not
/// reach a sheet, a full screen cover or a popover: each takes its size from its own trait collection, which is the
/// system's (Settings was drawn at AX5 under the capped page). A trait override on the window reaches everything shown
/// in it. Reading content lifts the cap in SwiftUI with `.readingContent()` and in UIKit by overriding its own view's
/// trait with `readingCategory`. visionOS has no window cap (#7420), but its RootView caps the environment too, so a
/// lifted message's text view (`LiftSelection`) still takes `readingCategory` there, the size the chat's words draw at.
enum WindowTextSize {
    /// The system's size, not the window's (which this overrides).
    @MainActor static var system: DynamicTypeSize { DynamicTypeSize(UIApplication.shared.preferredContentSizeCategory) ?? .large }

    /// What the reader chose (the system's, moved by Small / Standard / Large): reading content draws at it.
    @MainActor static var readingCategory: UIContentSizeCategory {
        UIContentSizeCategory(AppModel.shared.textSize.applied(to: system))
    }

    #if os(iOS)
    /// On every window of every scene; RootView runs it when it shows and when the system's size changes.
    @MainActor static func apply() {
        let capped = min(system, Theme.interfaceLargestTypeSize)
        for case let scene as UIWindowScene in UIApplication.shared.connectedScenes {
            for w in scene.windows {
                if capped < system {
                    w.traitOverrides.preferredContentSizeCategory = UIContentSizeCategory(capped)
                } else {
                    w.traitOverrides.remove(UITraitPreferredContentSizeCategory.self)
                }
            }
        }
    }
    #endif
}
#endif
