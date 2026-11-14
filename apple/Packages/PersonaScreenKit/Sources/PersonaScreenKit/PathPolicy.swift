import Foundation

/// Which route a screen session uses (#6398, the design's "Probing and choosing"): pure, on a timeline the caller
/// feeds, so every rule is tested without a network. The caller measures each open route (`LinkMeter`), reports
/// routes it lost, and gets back whether to move.
///
/// - Routes rank by minimum round trip (a busy route and an idle one compare fairly on it); within `tieMs` a direct
///   route beats the relay, LAN beats Tailscale.
/// - The stream starts on the relay; within `startWindow` the first healthy direct route that ranks better is used at
///   once.
/// - Later a route must be healthy for `stableWindow`, beat the current one clearly, and `dwell` must have passed since
///   the last move; from the relay to a direct route a tie is enough.
/// - A lost route is left at once for the best healthy one (the relay always qualifies while it is open); a degraded
///   one when another is healthy. A route left that way needs `stableWindow` healthy before it is used again.
public struct PathPolicy: Sendable {
    public struct Config: Sendable {
        public var tieMs = 5.0
        public var betterMs = 10.0
        public var betterShare = 0.25
        public var stableWindow = 10.0
        public var dwell = 20.0
        public var degradedLoss = 0.05
        public var degradedRiseMs = 50.0
        public var degradedFor = 5.0
        public var degradedDwell = 5.0
        public var floorWindow = 60.0
        /// How long after the stream's first look a route may be taken without the stable window.
        public var startWindow = 15.0

        public init() {}
    }

    public enum Decision: Equatable, Sendable {
        case stay
        case move(to: String, reason: PathChange.Reason)
    }

    public let relay: String
    public let config: Config
    /// The route in use.
    public private(set) var current: String
    private var lastMove: Double?
    private var healthySince: [String: Double] = [:]
    /// Routes left as lost or degraded: they need `stableWindow` healthy again.
    private var leftBad: Set<String> = []
    private var floors: [String: [(at: Double, ms: Double)]] = [:]
    private var degradedSince: Double?
    private var startedAt: Double?

    public init(relay: String, config: Config = Config()) {
        self.relay = relay
        self.config = config
        current = relay
    }

    /// One look at the routes: `routes` holds every open route's stats now (the relay included while open), `lost`
    /// the routes that failed since the last look, `networkChanged` whether the phone's networks changed meanwhile.
    public mutating func decide(_ routes: [String: LinkStats], lost: Set<String> = [], networkChanged: Bool = false,
                                now: Double) -> Decision {
        startedAt = startedAt ?? now
        for id in lost {
            healthySince[id] = nil
            leftBad.insert(id)
        }
        for (id, s) in routes where !lost.contains(id) {
            if s.healthy { healthySince[id] = healthySince[id] ?? now } else { healthySince[id] = nil }
            if let ms = s.minRttMs {
                floors[id, default: []].append((now, ms))
                floors[id]?.removeAll { now - $0.at > config.floorWindow }
            }
        }

        // Lost: at once to the best route that qualifies.
        if lost.contains(current) || routes[current] == nil {
            degradedSince = nil
            guard let to = best(routes, excluding: current, now: now, stable: false) else { return .stay }
            return move(to, networkChanged ? .networkChanged : .lost, now: now, leaving: true)
        }
        let here = routes[current]!

        // Degraded: losing packets or slower than its own floor, with a healthy alternative.
        if degraded(current, here) {
            degradedSince = degradedSince ?? now
            if now - degradedSince! >= config.degradedFor, lastMove.map({ now - $0 >= config.degradedDwell }) ?? true,
               let to = best(routes, excluding: current, now: now, stable: false), routes[to]?.healthy == true {
                return move(to, .degraded, now: now, leaving: true)
            }
        } else {
            degradedSince = nil
        }

        // Faster: right away near the stream's start; later only after the stable window and the dwell.
        let first = lastMove == nil && now - startedAt! <= config.startWindow
        if !first, let last = lastMove, now - last < config.dwell { return .stay }
        guard let to = best(routes, excluding: nil, now: now, stable: !first), to != current, let there = routes[to],
              better(there, than: here) else { return .stay }
        return move(to, .faster, now: now, leaving: false)
    }

    /// The user turned direct connections off: back to the relay.
    public mutating func directOff(now: Double) -> Decision {
        guard current != relay else { return .stay }
        return move(relay, .directOff, now: now, leaving: false)
    }

    private mutating func move(_ to: String, _ reason: PathChange.Reason, now: Double, leaving bad: Bool) -> Decision {
        if bad { leftBad.insert(current) }
        leftBad.remove(to)
        current = to
        lastMove = now
        degradedSince = nil
        return .move(to: to, reason: reason)
    }

    private func degraded(_ id: String, _ s: LinkStats) -> Bool {
        if let loss = s.packetLoss, loss >= config.degradedLoss { return true }
        if let ms = s.minRttMs, let floor = floors[id]?.map(\.ms).min(), ms >= floor + config.degradedRiseMs { return true }
        return false
    }

    /// Whether a route may be moved to now: healthy (the relay: open), healthy for the stable window when that's asked
    /// or when it was left as bad.
    private func qualifies(_ id: String, _ s: LinkStats, now: Double, stable: Bool) -> Bool {
        if id == relay { return true }
        guard s.healthy, let since = healthySince[id] else { return false }
        if stable || leftBad.contains(id) { return now - since >= config.stableWindow }
        return true
    }

    /// The best qualifying route by rank.
    private func best(_ routes: [String: LinkStats], excluding: String?, now: Double, stable: Bool) -> String? {
        routes.filter { $0.key != excluding && qualifies($0.key, $0.value, now: now, stable: stable) }
            .sorted { a, b in ranks(a.value, before: b.value) || (!ranks(b.value, before: a.value) && a.key < b.key) }
            .first?.key
    }

    private func order(_ k: LinkStats.Kind) -> Int {
        switch k {
        case .lan: 0
        case .tailscale: 1
        case .ipv6: 2
        case .relay: 3
        }
    }

    /// a ranks before b: clearly lower minimum round trip, or within the tie the preferred kind. A route not measured
    /// yet ranks after measured ones of the same kind order.
    private func ranks(_ a: LinkStats, before b: LinkStats) -> Bool {
        switch (a.minRttMs, b.minRttMs) {
        case let (x?, y?) where abs(x - y) > config.tieMs: return x < y
        case (nil, _?) where a.kind == .relay: return false
        case (_?, nil) where b.kind == .relay: return true
        default: return order(a.kind) < order(b.kind)
        }
    }

    /// Whether moving from a (in use) to b is worth it.
    private func better(_ b: LinkStats, than a: LinkStats) -> Bool {
        guard let y = b.minRttMs else { return false }
        guard let x = a.minRttMs else { return b.kind != .relay } // the relay not measured yet: a direct route wins
        if a.kind == .relay, b.kind != .relay { return y <= x + config.tieMs }
        if y <= x - max(config.betterMs, config.betterShare * x) { return true }
        return abs(x - y) <= config.tieMs && order(b.kind) < order(a.kind) && b.kind != .relay
    }
}
