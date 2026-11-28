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
        guard now.isFinite else { return .stay }
        startedAt = startedAt ?? now
        for id in lost {
            healthySince[id] = nil
            leftBad.insert(id)
        }
        let floorWindow = config.floorWindow.isFinite ? max(0, config.floorWindow) : 0
        for (id, s) in routes where !lost.contains(id) {
            if s.healthy {
                let since = healthySince[id] ?? now
                healthySince[id] = since
                if leftBad.contains(id), now - since >= config.stableWindow {
                    leftBad.remove(id)
                }
            } else {
                healthySince[id] = nil
            }
            if let ms = s.minRttMs, ms.isFinite, ms >= 0 {
                floors[id, default: []].append((now, ms))
                floors[id]?.removeAll { now - $0.at > floorWindow }
            }
        }
        let live = routes.filter { !lost.contains($0.key) }
        for id in Array(healthySince.keys) where live[id] == nil {
            healthySince[id] = nil
        }

        // Lost: at once to the best healthy route; only fall back to an unhealthy/unmeasured relay if none is healthy.
        if lost.contains(current) || live[current] == nil {
            degradedSince = nil
            guard let to = best(live, excluding: current, now: now, stable: false, requireHealthy: true)
                ?? best(live, excluding: current, now: now, stable: false, requireHealthy: false) else { return .stay }
            return move(to, networkChanged ? .networkChanged : .lost, now: now, leaving: true)
        }
        let here = live[current]!

        // Degraded: losing packets or slower than its own floor, with a healthy alternative.
        if degraded(current, here) {
            degradedSince = degradedSince ?? now
            if now - degradedSince! >= config.degradedFor, lastMove.map({ now - $0 >= config.degradedDwell }) ?? true {
                let fallback = best(live, excluding: current, now: now, stable: false, requireHealthy: true)
                    ?? (current != relay && live[relay]?.healthy == true ? relay : nil)
                if let to = fallback {
                    return move(to, .degraded, now: now, leaving: true)
                }
            }
        } else {
            degradedSince = nil
        }

        // Faster: right away near the stream's start; later only after the stable window and the dwell.
        let first = lastMove == nil && now - startedAt! <= config.startWindow
        if !first, let last = lastMove, now - last < config.dwell { return .stay }
        guard let to = best(live, excluding: nil, now: now, stable: !first, requireHealthy: true), to != current, let there = live[to],
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
        if now.isFinite { lastMove = now }
        degradedSince = nil
        return .move(to: to, reason: reason)
    }

    private func degraded(_ id: String, _ s: LinkStats) -> Bool {
        if let loss = s.packetLoss, loss.isFinite, loss >= config.degradedLoss { return true }
        if let ms = s.minRttMs, ms.isFinite, ms >= 0, let floor = floors[id]?.map(\.ms).min(), ms >= floor + config.degradedRiseMs { return true }
        return false
    }

    /// Whether a route may be moved to now: healthy (or the relay as last-resort fallback when `!requireHealthy`),
    /// and healthy for `stableWindow` when stability is required or it was previously left as bad.
    private func qualifies(_ id: String, _ s: LinkStats, now: Double, stable: Bool, requireHealthy: Bool) -> Bool {
        if id == relay, !requireHealthy { return true }
        guard s.healthy, let since = healthySince[id] else { return false }
        if stable || leftBad.contains(id) { return now - since >= config.stableWindow }
        return true
    }

    /// The best qualifying route: within `tieMs` of the fastest measured route, the preferred kind wins (then lower
    /// minimum round trip, then route id). Unmeasured routes rank after measured ones, by kind order then route id.
    private func best(_ routes: [String: LinkStats], excluding: String?, now: Double, stable: Bool, requireHealthy: Bool) -> String? {
        let candidates = routes.filter { $0.key != excluding && qualifies($0.key, $0.value, now: now, stable: stable, requireHealthy: requireHealthy) }
        guard !candidates.isEmpty else { return nil }
        let measured = candidates.compactMap { id, stats -> (id: String, stats: LinkStats, ms: Double)? in
            guard let ms = stats.minRttMs, ms.isFinite, ms >= 0 else { return nil }
            return (id, stats, ms)
        }
        if let fastestMs = measured.map(\.ms).min() {
            let cutoff = fastestMs + max(0, config.tieMs)
            return measured.filter { $0.ms <= cutoff }.min { a, b in
                let oa = order(a.stats.kind), ob = order(b.stats.kind)
                if oa != ob { return oa < ob }
                if a.ms != b.ms { return a.ms < b.ms }
                return a.id < b.id
            }?.id
        }
        return candidates.min { a, b in
            let oa = order(a.value.kind), ob = order(b.value.kind)
            if oa != ob { return oa < ob }
            return a.key < b.key
        }?.key
    }

    private func order(_ k: LinkStats.Kind) -> Int {
        switch k {
        case .lan: 0
        case .tailscale: 1
        case .ipv6: 2
        case .relay: 3
        }
    }

    /// Whether moving from a (in use) to b is worth it.
    private func better(_ b: LinkStats, than a: LinkStats) -> Bool {
        guard let y = b.minRttMs, y.isFinite, y >= 0 else { return false }
        guard let x = a.minRttMs, x.isFinite, x >= 0 else { return b.kind != .relay } // the relay not measured yet: a direct route wins
        let tie = max(0, config.tieMs)
        let betterMs = max(0, config.betterMs)
        let betterShare = max(0, config.betterShare)
        if a.kind == .relay, b.kind != .relay { return y <= x + tie }
        if y <= x - max(betterMs, betterShare * x) { return true }
        return abs(x - y) <= tie && order(b.kind) < order(a.kind) && b.kind != .relay
    }
}
