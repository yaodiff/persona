import Foundation

/// The phone's side of a UDP flow's video (#7873), as pure state on a timeline the caller feeds: fragments in, whole
/// messages out in order, and what to ask the node for. Reed-Solomon parity rebuilds a block as soon as any `data` of
/// its shards are there; a block still short is asked for again (`nack`) once all of it was sent (a later message came,
/// or it went quiet); a message known only from a later one is asked for by its place (`want`); and a frame that has
/// held the picture back too long is given up (`skip`), after which the node says where the stream goes on (`reset`).
public struct ScreenUDPReceiver: Sendable {
    /// Maximum framed message size accepted on the UDP video channel (4-byte length prefix + `ScreenVideoWire.maxMessageBytes`).
    /// Prevents a forged `UInt32` header length from triggering multi-GB reservations or millions of NACK entries.
    public static let maxMessageLength = 4 + ScreenVideoWire.maxMessageBytes

    public struct Config: Sendable {
        /// A message gone this long without a fragment is taken as fully sent (the node paces its packets); at least
        /// twice the flow's jitter, so a link that delivers in bursts doesn't read as loss.
        public var quiet = 0.006
        /// Ask again for the same thing after this many round trips, and no sooner than `againMin`.
        public var againRtts = 1.5
        public var againMin = 0.010
        /// A frame holding the picture back this long past a round trip is given up.
        public var giveUp = 0.100

        public init() {}
    }

    public let channel: UInt8
    public let config: Config
    /// The next byte of the channel the decoder needs: everything before it was delivered or given up.
    public private(set) var expected: UInt64
    /// Data shards rebuilt from parity (they never had to be asked for).
    public private(set) var rebuilt = 0
    /// Frames given up.
    public private(set) var givenUp = 0

    private struct Block {
        var data: Int
        var parity = 0
        var shards: [Int: [UInt8]] = [:]
        var whole: [[UInt8]]?
    }

    private struct Message {
        var length: Int
        var droppable: Bool
        var blocks: [Int: Block] = [:]
        var lastAt: Double
        var asked: [Int: Double] = [:] // block << 16 | index
    }

    private var messages: [UInt64: Message] = [:]
    private var wantAt = -Double.infinity
    /// Since when a later message is there while the one at `expected` isn't whole.
    private var blockedSince: Double?
    /// The offset given up and when, until the node's reset (or the stream) moves past it.
    private var skipped: (offset: UInt64, at: Double)?

    public init(channel: UInt8, expected: UInt64, config: Config = Config()) {
        self.channel = channel
        self.expected = expected
        self.config = config
    }

    public struct Delivery: Equatable, Sendable {
        /// Whole messages now next in line, in order, with their offsets.
        public var messages: [(offset: UInt64, bytes: Data)] = []
        /// Say where the phone is: something was delivered, or a copy of something already had came (the node's tail
        /// probe, or an answer to a request asked twice).
        public var ack = false

        public static func == (a: Delivery, b: Delivery) -> Bool {
            a.ack == b.ack && a.messages.map(\.offset) == b.messages.map(\.offset) && a.messages.map(\.bytes) == b.messages.map(\.bytes)
        }
    }

    /// One fragment from the node.
    public mutating func fragment(_ h: ScreenUDPFragmentHead, _ fragment: Data, at now: Double) -> Delivery {
        guard h.channel == channel else { return Delivery() }
        if h.offset < expected { return Delivery(ack: true) }
        guard h.length > 0, h.length <= UInt32(Self.maxMessageLength) else { return Delivery() }
        let length = Int(h.length)
        guard h.offset <= UInt64.max - UInt64(length),
              h.data > 0, Int(h.data) + Int(h.parity) <= 256,
              Int(h.index) < Int(h.data) + Int(h.parity) else { return Delivery() }
        let layout = ScreenUDPLayout.blocks(length: length)
        guard Int(h.block) < layout.count,
              layout[Int(h.block)].data == Int(h.data),
              fragment.count <= layout[Int(h.block)].shardLength else { return Delivery() }
        var m = messages[h.offset] ?? Message(length: length, droppable: h.droppable, lastAt: now)
        guard m.length == length else { return Delivery() }
        m.lastAt = now
        var b = m.blocks[Int(h.block)] ?? Block(data: Int(h.data))
        b.parity = max(b.parity, Int(h.parity))
        if b.whole == nil, b.shards[Int(h.index)] == nil {
            let shard = [UInt8](fragment)
            b.shards[Int(h.index)] = shard
            // A keyframe is never given up (giving one up asks for another as large): its first shard says so, even
            // from a node that marks every frame droppable, whether shard 0 arrived directly or was rebuilt by parity.
            if h.block == 0, h.index == 0, Self.isKeyframeHeader(shard) { m.droppable = false }
            if b.shards.count >= b.data {
                complete(&b, layout[Int(h.block)])
                if h.block == 0, let first = b.whole?.first, Self.isKeyframeHeader(first) { m.droppable = false }
            }
        }
        m.blocks[Int(h.block)] = b
        messages[h.offset] = m
        if h.offset > expected, blockedSince == nil { blockedSince = now }
        return deliver(at: now)
    }

    private static func isKeyframeHeader(_ bytes: [UInt8]) -> Bool {
        bytes.count > 17 && bytes[4] == 3 && bytes[17] & 1 != 0
    }

    /// Rebuilds a block that has enough shards.
    private mutating func complete(_ b: inout Block, _ layout: ScreenUDPLayout.Block) {
        let count = max(b.data + b.parity, (b.shards.keys.max() ?? 0) + 1)
        guard count <= 256 else { return }
        var shards = [[UInt8]?](repeating: nil, count: count)
        var missing = 0
        for i in 0 ..< count {
            guard var s = b.shards[i] else {
                if i < b.data { missing += 1 }
                continue
            }
            if s.count < layout.shardLength { s += [UInt8](repeating: 0, count: layout.shardLength - s.count) }
            shards[i] = s
        }
        guard (try? ReedSolomon.reconstruct(&shards, data: b.data)) != nil else { return }
        rebuilt += missing
        b.whole = shards[0 ..< b.data].map { $0 ?? [] }
    }

    private func bytes(_ m: Message) -> Data? {
        let blockCount = ScreenUDPLayout.blocks(length: m.length).count
        for i in 0 ..< blockCount {
            guard m.blocks[i]?.whole != nil else { return nil }
        }
        var out = [UInt8]()
        out.reserveCapacity(m.length)
        for i in 0 ..< blockCount {
            guard let whole = m.blocks[i]?.whole else { return nil }
            for s in whole { out += s }
        }
        return Data(out.prefix(m.length))
    }

    private mutating func deliver(at now: Double) -> Delivery {
        var d = Delivery()
        while let m = messages[expected], let b = bytes(m) {
            d.messages.append((expected, b))
            messages[expected] = nil
            let (nextExpected, overflow) = expected.addingReportingOverflow(UInt64(m.length))
            expected = overflow ? .max : nextExpected
            blockedSince = messages.keys.contains { $0 > expected } ? now : nil
        }
        if !d.messages.isEmpty {
            d.ack = true
            if let s = skipped, expected > s.offset { skipped = nil }
        }
        return d
    }

    /// The node's reset: the stream goes on at `offset`; what lies before it is dropped.
    public mutating func reset(to offset: UInt64, at now: Double) -> Delivery {
        guard offset >= expected else { return Delivery(ack: true) }
        expected = offset
        messages = messages.filter { $0.key >= offset }
        skipped = nil
        blockedSince = messages.keys.contains { $0 > expected } ? now : nil
        var d = deliver(at: now)
        d.ack = true
        return d
    }

    public struct Requests: Equatable, Sendable {
        public var nack: [ScreenUDPNack] = []
        public var want: (from: UInt64, to: UInt64)?
        /// Give the message at this offset up (send `skip`).
        public var skip: UInt64?

        public static func == (a: Requests, b: Requests) -> Bool {
            a.nack == b.nack && a.want?.from == b.want?.from && a.want?.to == b.want?.to && a.skip == b.skip
        }
    }

    /// What to ask the node for at `now`, with the flow's round trip and jitter in seconds.
    public mutating func requests(at now: Double, rtt: Double, jitter: Double) -> Requests {
        var r = Requests()
        let again = max(config.againRtts * rtt, config.againMin)
        let quiet = max(config.quiet, 2 * jitter)
        let offsets = messages.keys.sorted()

        if let s = skipped {
            // Given up and the reset hasn't come: say it again after a while.
            if now - s.at >= again { r.skip = s.offset; skipped = (s.offset, now) }
            return r
        }
        // A frame that has held the picture back too long goes.
        if let since = blockedSince, now - since >= config.giveUp + rtt, let head = messages[expected], head.droppable {
            r.skip = expected
            skipped = (expected, now)
            givenUp += 1
            return r
        }
        // Messages known only from a later one.
        if let first = offsets.first, first > expected, now - wantAt >= again, let since = blockedSince, now - since >= quiet {
            r.want = (expected, first)
            wantAt = now
        }
        for (i, off) in offsets.enumerated() {
            guard var m = messages[off] else { continue }
            let later = i < offsets.count - 1
            guard later || now - m.lastAt >= quiet else { continue }
            for (bi, l) in ScreenUDPLayout.blocks(length: m.length).enumerated() {
                let b = m.blocks[bi]
                if b?.whole != nil { continue }
                var have = b?.shards.count ?? 0
                for j in 0 ..< l.data where have < l.data {
                    if b?.shards[j] != nil { continue }
                    let key = bi << 16 | j
                    have += 1 // asked for now or lately: counts toward what the block lacks
                    if let at = m.asked[key], now - at < again { continue }
                    m.asked[key] = now
                    r.nack.append(ScreenUDPNack(offset: off, block: UInt16(bi), index: UInt16(j)))
                }
            }
            messages[off] = m
        }
        return r
    }

    /// The data shard counts of the blocks of the messages in hand, for sizing parity (`ScreenUDPParity`).
    public var blockSizes: [Int] { messages.values.flatMap { m in ScreenUDPLayout.blocks(length: m.length).map(\.data) } }
}

/// The parity a phone asks for (#7873): three times the loss it measures, from 5 % up to the cap (40 % on Wi-Fi, 15 %
/// while on cellular, Tailscale over cellular included); none while the loss stays under 0.2 %. Each block also gets
/// one parity shard more than the ratio, which is a lot for the few-shard frames of a low bitrate, so the ratio is the
/// largest whose parity on the recent blocks stays within the cap's share of their data, and none when even the
/// smallest would not.
public enum ScreenUDPParity {
    public static let wifiCap = 4000
    public static let cellularCap = 1500
    public static let lossFloor = 0.002
    public static let lowest = 500

    public static func ratio(loss: Double?, cap: Int, recentBlocks: [Int]) -> Int {
        guard let loss, !loss.isNaN, loss >= lossFloor, cap > 0 else { return 0 }
        let scaled = (loss * 3 * 10000).rounded()
        var r = scaled >= Double(cap) ? cap : min(max(Int(scaled), lowest), cap)
        var data: UInt64 = 0
        for k in recentBlocks where k > 0 {
            let (sum, ov) = data.addingReportingOverflow(UInt64(k))
            data = ov ? .max : sum
        }
        guard data > 0 else { return r }
        func shareExceedsCap(_ r: Int) -> Bool {
            var parity: UInt64 = 0
            for k in recentBlocks where k > 0 {
                let (sum, ov) = parity.addingReportingOverflow(UInt64(ScreenUDPLayout.parity(data: k, ratio: r)))
                parity = ov ? .max : sum
            }
            let prod = parity.multipliedFullWidth(by: 10000)
            let capProd = data.multipliedFullWidth(by: UInt64(cap))
            // floor(parity * 10000 / data) > cap  <==>  parity * 10000 >= (cap + 1) * data
            let (capPlusOneHigh, capPlusOneLow): (UInt64, UInt64) = {
                let (low, ov) = capProd.low.addingReportingOverflow(data)
                let (high, ov2) = capProd.high.addingReportingOverflow(ov ? 1 : 0)
                return ov2 ? (.max, .max) : (high, low)
            }()
            if prod.high != capPlusOneHigh { return prod.high > capPlusOneHigh }
            return prod.low >= capPlusOneLow
        }
        while r > 0, shareExceedsCap(r) {
            r = r > 1 ? r * 3 / 4 : 0
        }
        return r
    }

    /// The share of wire bytes beyond the video's own (parity and resends) the budget must leave room for: from the
    /// node's counters over a stretch, data > 0.
    public static func overhead(dataBytes: UInt64, parityBytes: UInt64, resentBytes: UInt64) -> Double {
        guard dataBytes > 0 else { return 0 }
        return (Double(parityBytes) + Double(resentBytes)) / Double(dataBytes)
    }
}

/// A flow's packet loss before recovery, from the node's packet numbers over the last 10 s: numbers missing between the
/// lowest and highest seen. Nil until enough packets say.
public struct ScreenUDPLoss: Sendable {
    public static let window = 10.0
    public static let enough = 100
    private var seen: [(at: Double, number: UInt64)] = []

    public init() {}

    public mutating func received(_ number: UInt64, at now: Double) {
        guard now.isFinite else { return }
        seen.append((now, number))
        seen.removeAll { now - $0.at > Self.window }
    }

    /// Over the last `window` seconds (10 by default), with at least `enough` packets to say.
    public func loss(at now: Double, window: Double = ScreenUDPLoss.window, enough: Int = ScreenUDPLoss.enough) -> Double? {
        let recent = seen.filter { now - $0.at <= window }
        guard recent.count >= enough, let lo = recent.map(\.number).min(), let hi = recent.map(\.number).max() else { return nil }
        let span = Double(hi - lo) + 1.0
        return max(0, 1 - Double(recent.count) / span)
    }
}

/// The pacing a phone asks the node for (#7873): 1.25 × the fastest rate a message's packets arrived at over the last
/// 3 s, between 1 and 150 Mbit/s; before any measurement, where the route starts. The node paces its packets; past a
/// slow bottleneck they arrive at the bottleneck's rate, so the request settles just above it, and below one the
/// arrival rate is the pacing itself, so the request grows a quarter at a time until it meets the bottleneck.
public struct ScreenUDPPace: Sendable {
    public static let lowest: UInt32 = 1_000
    public static let highest: UInt32 = 150_000
    /// Packets of one message before its arrival rate counts.
    public static let train = 8
    public static let window = 3.0

    private struct Train { var first: Double; var last: Double; var bytes: Int; var packets: Int }
    private var trains: [UInt64: Train] = [:]
    private var rates: [(at: Double, kbps: Double)] = []

    public init() {}

    /// A data packet of `bytes` (whole, on the wire) for the message at `offset`.
    public mutating func packet(offset: UInt64, bytes: Int, at now: Double) {
        guard bytes >= 0, now.isFinite else { return }
        guard var t = trains[offset] else {
            trains[offset] = Train(first: now, last: now, bytes: 0, packets: 1)
            if trains.count > 64, let oldest = trains.keys.min() { trains[oldest] = nil }
            return
        }
        let (nextPackets, pOv) = t.packets.addingReportingOverflow(1)
        t.packets = pOv ? .max : nextPackets
        let (nextBytes, bOv) = t.bytes.addingReportingOverflow(bytes)
        t.bytes = bOv ? .max : nextBytes
        t.last = now
        trains[offset] = t
        if t.packets >= Self.train, t.last > t.first {
            let kbps = Double(t.bytes) * 8 / (t.last - t.first) / 1000
            if !kbps.isNaN {
                rates.append((now, kbps))
                rates.removeAll { now - $0.at > Self.window }
            }
        }
    }

    public func request(at now: Double, start: UInt32) -> UInt32 {
        guard let best = rates.filter({ now - $0.at <= Self.window && !$0.kbps.isNaN }).map(\.kbps).max() else { return start }
        return UInt32(min(max(1.25 * best, Double(Self.lowest)), Double(Self.highest)))
    }
}

/// Loss as a congestion signal for the video's rate (#7873): the helper adapts on the delay of frames that arrived and
/// the quality tiers don't read loss, so a flow losing a tenth of its packets or more (a queue overflowing) cuts the
/// cap the phone gives the helper by half the loss, at most every 2 s, and below 2 % it recovers by 5 % a second.
public struct ScreenUDPCongestion: Sendable {
    public static let congested = 0.10
    public static let clear = 0.02
    public static let lowest = 0.1
    public private(set) var scale = 1.0
    private var lastCut = -Double.infinity
    private var lastLook = -Double.infinity

    public init() {}

    public mutating func look(loss: Double?, at now: Double) {
        guard now.isFinite else { return }
        defer { if now > lastLook { lastLook = now } }
        guard let loss, !loss.isNaN else { return }
        if loss >= Self.congested {
            if now - lastCut >= 2 {
                scale = max(Self.lowest, scale * (1 - 0.5 * loss))
                lastCut = now
            }
        } else if loss < Self.clear, lastLook.isFinite {
            let dt = now - lastLook
            if dt > 0 { scale = min(1, scale * pow(1.05, min(dt, 2))) }
        }
    }
}

