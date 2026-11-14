#if DEBUG && os(iOS) && canImport(PersonaScreenKit)
import Foundation
import PersonaScreenKit

/// The live screen's routes on a real network (#6398, docs/operations/runtime-cli.md): what each route measures and
/// which one is in use, and making a direct route break or go silent from the phone's side, so falling back can be
/// tried without touching the Mac's or the network's settings.
extension RuntimeCLI {
    static let screenRouteCommands: [String: Command] = [
        "screenRoutes": { _ in screenRoutes() },
        "screenRoute": { a in await screenRoute(a) },
    ]

    /// Each live screen's routes: kind, smoothed and minimum round trip, jitter, packet loss, samples, healthy; the
    /// one in use; the last change and why. Kinds and numbers only, never addresses.
    private static func screenRoutes() -> Reply {
        var screens: [[String: Any]] = []
        for (_, l) in ScreenRoutes.latest {
            let routes: [[String: Any]] = l.routes.map { id, s in
                ["route": id == ScreenLinkSession.relayRoute ? "relay" : s.kind.rawValue, "inUse": id == l.current,
                 "srttMs": s.srttMs as Any, "minRttMs": s.minRttMs as Any, "jitterMs": s.jitterMs as Any,
                 "packetLoss": s.packetLoss as Any, "samples": s.samples, "healthy": s.healthy, "via": s.via.rawValue]
            }
            var screen: [String: Any] = ["routes": routes]
            if let c = l.change { screen["lastChange"] = ["to": c.to.kind.rawValue, "reason": c.reason.rawValue] }
            screens.append(screen)
        }
        return ["screens": screens]
    }

    /// `action`: drop (the direct route's connection breaks), mute (it goes silent: nothing it carries arrives) or
    /// unmute; `kind`: lan or tailscale.
    private static func screenRoute(_ a: Args) async -> Reply {
        guard let kind = (a["kind"] as? String).flatMap(LinkStats.Kind.init(rawValue:)), let action = a["action"] as? String else {
            return ["error": "kind (lan | tailscale) and action (drop | mute | unmute)"]
        }
        let pairs = Array(ScreenRoutes.latest.keys)
        for pair in pairs {
            guard let s = ScreenLinkRegistry.shared.session(for: pair) else { continue }
            switch action {
            case "drop": await s.dropDirect(kind: kind)
            case "mute": await s.muteDirect(kind: kind, true)
            case "unmute": await s.muteDirect(kind: kind, false)
            default: return ["error": "action: drop | mute | unmute"]
            }
        }
        return ["screens": pairs.count, "action": action, "kind": kind.rawValue]
    }
}
#endif
