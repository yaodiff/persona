import Foundation

// The live screen's network routes (#6398, docs/extensions/computer-use.md "Live screen video (#3954)"): what the phone
// measures on each route with its own pings, and the event of moving the stream from one route to another. The route
// choice and the quality tiers (#6396) both read these, so there is one set of numbers. Queueing, delivered and burst
// rate, frames lost or late and decode time are the tiers' own, from the frames; nothing here repeats them.

/// One route's measured state, over a 10 s window of round trips between the phone and the node.
public struct LinkStats: Equatable, Sendable {
    /// Which route: the same local network, Tailscale, a direct IPv6 route over the internet, or the hub's relay.
    public enum Kind: String, Codable, CaseIterable, Sendable {
        case lan, tailscale, ipv6, relay
    }

    /// The phone's interface the route leaves on, so the relay over Wi-Fi tells apart from the relay over cellular.
    public enum Via: String, Codable, CaseIterable, Sendable {
        case wifi, cellular, wired, other
    }

    public var kind: Kind
    public var via: Via
    /// Smoothed round trip in ms (EWMA with gain 1/8, as TCP's SRTT); through the hub on the relay.
    public var srttMs: Double?
    /// The window's lowest round trip in ms: the route's delay with no queue. Routes are compared on it, so a busy
    /// route and an idle one compare fairly.
    public var minRttMs: Double?
    /// Smoothed change between consecutive round trips in ms (RFC 3550's interarrival jitter, gain 1/16).
    public var jitterMs: Double?
    /// 0…1: the node's TCP packets retransmitted ÷ sent on this route over the window. nil when unknown (the relay:
    /// TCP over two legs hides it, and the delay shows it instead). Packets, not frames.
    public var packetLoss: Double?
    /// Round trips in the window.
    public var samples: Int
    /// Enough recent round trips, low loss and low jitter (the route choice's rule); a route that isn't is never moved to.
    public var healthy: Bool

    public init(kind: Kind, via: Via, srttMs: Double? = nil, minRttMs: Double? = nil, jitterMs: Double? = nil,
                packetLoss: Double? = nil, samples: Int = 0, healthy: Bool = false) {
        self.kind = kind
        self.via = via
        self.srttMs = srttMs
        self.minRttMs = minRttMs
        self.jitterMs = jitterMs
        self.packetLoss = packetLoss
        self.samples = samples
        self.healthy = healthy
    }
}

/// The stream's route changed: once when it opens (`from` nil, `.start`), after every move to another route, and when
/// the route's interface changes without a move (Wi-Fi to cellular under Tailscale, `.networkChanged`). The one-way
/// delay changes with it, so delay baselines on both ends start again.
public struct PathChange: Equatable, Sendable {
    public enum Reason: String, Codable, CaseIterable, Sendable {
        /// The stream's first route.
        case start
        /// Another route proved faster for a stable window.
        case faster
        /// The route failed: its connection closed, its interface went away, or it stopped answering.
        case lost
        /// The route kept losing packets or got slower while another was healthy.
        case degraded
        /// The phone's networks changed (Wi-Fi, cellular, Tailscale on or off).
        case networkChanged = "network_changed"
        /// The user turned direct connections off.
        case directOff = "direct_off"
    }

    /// The route before, nil for the stream's first.
    public var from: LinkStats?
    public var to: LinkStats
    public var reason: Reason

    public init(from: LinkStats?, to: LinkStats, reason: Reason) {
        self.from = from
        self.to = to
        self.reason = reason
    }
}
