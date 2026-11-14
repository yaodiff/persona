import Foundation

// The phone's end of a screen session's channels (#6398): what came in, once each and in order, and what went out,
// kept until the node acknowledges it. Pure state; the app's transports feed it.

/// A channel's bytes coming in: each byte delivered once, in order, whichever transport carried it.
public struct LinkReceiver: Equatable, Sendable {
    /// The next offset expected: everything before it was delivered.
    public private(set) var offset: UInt64

    public enum Failure: Error, Equatable {
        /// Bytes past the next one expected: the sender skipped some, so this transport can't be trusted.
        case gap(expected: UInt64, got: UInt64)
    }

    public init(offset: UInt64 = 0) { self.offset = offset }

    /// The part of `bytes` (starting at `at`) not delivered yet: empty for a copy that came twice (over the old route
    /// and the new). Throws for a gap.
    public mutating func accept(_ bytes: Data, at: UInt64) throws -> Data {
        guard at <= offset else { throw Failure.gap(expected: offset, got: at) }
        let skip = offset - at
        guard skip < UInt64(bytes.count) else { return Data() }
        let fresh = bytes.dropFirst(Int(skip))
        offset += UInt64(fresh.count)
        return Data(fresh)
    }

    /// The video channel goes on at `offset` (`reset`): what lay between was dropped by the node.
    public mutating func reset(to offset: UInt64) {
        if offset > self.offset { self.offset = offset }
    }
}

/// A channel's bytes going out: kept until the node acknowledges them, and sent again from where the node is after a
/// move. At most `window` bytes wait; the caller holds back while it is full, so an outage never grows memory.
public struct LinkSender: Sendable {
    public let window: Int
    /// What the node acknowledged: `buffer` starts here.
    public private(set) var base: UInt64 = 0
    /// Where the next send starts.
    public private(set) var sent: UInt64 = 0
    private var buffer = Data()
    /// When each append's first byte came, to tell how long the oldest unacknowledged byte has waited.
    private var stamps: [(offset: UInt64, at: Double)] = []

    public init(window: Int) { self.window = window }

    public var end: UInt64 { base + UInt64(buffer.count) }
    public var unacknowledged: Int { buffer.count }
    public var hasRoom: Bool { buffer.count < window }
    public var hasUnsent: Bool { sent < end }

    public mutating func append(_ bytes: Data, at now: Double) {
        guard !bytes.isEmpty else { return }
        stamps.append((end, now))
        buffer.append(bytes)
    }

    /// The node has everything through `offset`. An offset past what was sent is ignored.
    public mutating func acknowledged(through offset: UInt64) {
        guard offset > base, offset <= end else { return }
        buffer.removeFirst(Int(offset - base))
        base = offset
        if sent < base { sent = base }
        while stamps.count > 1, stamps[1].offset <= base { stamps.removeFirst() }
        if buffer.isEmpty { stamps.removeAll() }
    }

    /// The node has everything through `offset` (`resumed` on a new transport): send on from there.
    public mutating func resume(from offset: UInt64) {
        acknowledged(through: offset)
        sent = max(base, min(offset, end))
    }

    /// The next bytes to send, at most `max`, moving `sent` past them.
    public mutating func next(max: Int) -> (offset: UInt64, bytes: Data)? {
        guard sent < end else { return nil }
        let from = Int(sent - base)
        let to = Swift.min(from + max, buffer.count)
        let out = (sent, Data(buffer[buffer.startIndex + from ..< buffer.startIndex + to]))
        sent = base + UInt64(to)
        return out
    }

    /// How long the oldest byte the node hasn't acknowledged has waited, in seconds; nil with none.
    public func oldestAge(at now: Double) -> Double? {
        guard !buffer.isEmpty, let first = stamps.first else { return nil }
        return now - first.at
    }
}

/// The video channel's bytes as whole video wire messages only: a partial message stays until it completes, and a
/// `reset` drops it, so the decoder never sees half a message from before a dropped backlog.
public struct VideoMessageAssembler {
    private var parser = ScreenVideoParser()

    public init() {}

    /// The whole messages these bytes complete, framed as they came.
    public mutating func push(_ bytes: Data) throws -> Data {
        var out = Data()
        for m in try parser.push(bytes) { out.append(ScreenVideoWire.frame(m.type, body: m.body)) }
        return out
    }

    /// Drops a partial message.
    public mutating func reset() { parser = ScreenVideoParser() }
}

/// One route's round trips as `LinkStats`: smoothed (EWMA 1/8) and minimum over 10 s, jitter (RFC 3550, 1/16), and
/// packet loss from the node's TCP counters in pongs when there are enough packets to tell.
public struct LinkMeter: Sendable {
    public static let window = 10.0
    /// Packets in the window below which loss is unknown: a few idle pings say nothing about it.
    public static let lossPackets: UInt32 = 50

    private var rtts: [(at: Double, ms: Double)] = []
    private var packets: [(at: Double, retransmitted: UInt32, sent: UInt32)] = []
    private var last: Double?
    public private(set) var srttMs: Double?
    public private(set) var jitterMs: Double?
    public private(set) var lastPongAt: Double?

    public init() {}

    /// A pong for a ping sent `rttMs` ago. `counts`: the node counts packets on this route (a direct one).
    public mutating func pong(rttMs: Double, retransmitted: UInt32, sent: UInt32, counts: Bool, at now: Double) {
        rtts.append((now, rttMs))
        if counts { packets.append((now, retransmitted, sent)) }
        srttMs = srttMs.map { $0 + (rttMs - $0) / 8 } ?? rttMs
        if let last { jitterMs = (jitterMs ?? 0) + (abs(rttMs - last) - (jitterMs ?? 0)) / 16 }
        last = rttMs
        lastPongAt = now
        prune(now)
    }

    private mutating func prune(_ now: Double) {
        rtts.removeAll { now - $0.at > Self.window }
        packets.removeAll { now - $0.at > Self.window }
    }

    public func stats(kind: LinkStats.Kind, via: LinkStats.Via, at now: Double) -> LinkStats {
        let recent = rtts.filter { now - $0.at <= Self.window }
        let counted = packets.filter { now - $0.at <= Self.window }
        let sent = counted.reduce(UInt64(0)) { $0 + UInt64($1.sent) }
        let retrans = counted.reduce(UInt64(0)) { $0 + UInt64($1.retransmitted) }
        let loss: Double? = sent >= UInt64(Self.lossPackets) ? min(1, Double(retrans) / Double(sent)) : nil
        let fresh = lastPongAt.map { now - $0 <= 3 } ?? false
        let healthy = recent.count >= 3 && fresh && (loss ?? 0) < 0.05 && (jitterMs ?? 0) < 30
        return LinkStats(kind: kind, via: via, srttMs: srttMs.map { ($0 * 10).rounded() / 10 },
                         minRttMs: recent.map(\.ms).min().map { ($0 * 10).rounded() / 10 },
                         jitterMs: jitterMs.map { ($0 * 10).rounded() / 10 }, packetLoss: loss, samples: recent.count, healthy: healthy)
    }
}
