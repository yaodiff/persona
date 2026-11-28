import Foundation
import Observation

/// visionOS has no Live Activities or Dynamic Island. The hub's fleet state is kept here instead, for the Needs You
/// ornament (docs/apple/vision.md「Mapping from iOS」); nothing is started or ended.
@Observable final class LiveActivityController {
    static let shared = LiveActivityController()

    /// The hub's latest fleet summary (what the island would show).
    private(set) var fleet: FleetState?
    /// The device's notification settings, as the hub keeps them.
    private(set) var prefs: NotifyPrefs?
    /// Interactions settled from this device, hidden until the hub's next fleet push agrees.
    private var resolved: Set<String> = []

    /// No activity runs, so the diagnostics page has nothing to compare.
    var runningLook: FleetLook?? { nil }

    func start() {}
    func settings(_ prefs: NotifyPrefs) { self.prefs = prefs }
    func apply(_ hubState: FleetState) {
        fleet = hubState
        resolved = []
    }
    func resolvedLocally(_ interactionID: String, occurrence: String? = nil) { resolved.insert(interactionID) }
    func isResolvedLocally(_ interactionID: String) -> Bool { resolved.contains(interactionID) }
    func endAll() { fleet = nil }
    func look(_ look: FleetLook, force: Bool = false, faces: () -> FleetFaces?) {}
    func showSample(_ phase: String, details: Bool, island spec: String?, count: Int = 0) {}
}
