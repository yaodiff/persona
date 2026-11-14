import Foundation

/// What the phone measures of the video stream itself for the quality tiers (#6396), frame by frame: the queueing delay,
/// the rate that arrived, bursts that show the link's headroom, frames arriving late, and whether decoding and showing
/// keep up. The route's own round trip, jitter and loss come from `LinkMeter` (#6398); nothing here repeats them.
///
/// Queueing is a frame's one-way delay (the phone's arrival less the helper's capture time) above the lowest one of the
/// last 10 s, the same rule as the helper's `RateController`, so the two clocks needn't agree. Idle refinement's frames
/// are left out of the queue and the delivered rate (a large frame sent on purpose is no congestion), but their bursts
/// count: they are the cleanest probe of what the link can carry. Times are µs on the phone's monotonic clock.
///
/// The rate that arrived (`deliveredKbps`) is what the link carried while it was busy: over frames that came in queued
/// behind the one before (both over 50 ms of queueing), their bytes over the time between their arrivals, which is the
/// link's capacity. Only when no frames queued does it fall back to everything that came over the last second, which is
/// then the screen's demand. Averaging in the quiet stretches (the helper skipping, a still screen) would read a busy
/// link as a slow one.
public struct ReceiverSignals: Sendable {
    /// Frames this big or bigger, spread over several reads, say how fast the link moved them (`burstKbps`).
    public static let burstBytes = 32_000
    static let burstMinSpanMicros: UInt64 = 5_000
    static let baselineMicros: UInt64 = 10_000_000

    private var baseline: [(at: UInt64, delay: Int64)] = []
    /// Each frame's arrival, queueing in ms, whether it was late and its bytes, over the last two seconds.
    private var queue: [(at: UInt64, ms: Double, late: Bool, bytes: Int)] = []
    static let busyMs = 50.0
    /// Bytes that arrived, by arrival, over the last second.
    private var arrivals: [(at: UInt64, bytes: Int)] = []
    private var bursts: [Double] = []
    private var decodes: [(at: UInt64, ms: Double)] = []
    private var shown: [(at: UInt64, replaced: Bool)] = []

    public init() {}

    /// A frame arrived whole at `arrival`, its first bytes at `firstByte` (when it spanned several reads). `fps` is the
    /// frame rate asked for, which says when a frame is late (over two frame intervals queued).
    public mutating func frame(captureMicros: UInt64, arrivalMicros: UInt64, firstByteMicros: UInt64?, bytes: Int,
                               refinement: Bool, fps: Int) {
        if bytes >= Self.burstBytes, let first = firstByteMicros, arrivalMicros > first,
           arrivalMicros - first >= Self.burstMinSpanMicros {
            bursts.append(Double(bytes) * 8 * 1000 / Double(arrivalMicros - first))
        }
        guard !refinement else { return }
        let delay = Int64(truncatingIfNeeded: arrivalMicros) - Int64(truncatingIfNeeded: captureMicros)
        while let last = baseline.last, last.delay >= delay { baseline.removeLast() }
        baseline.append((arrivalMicros, delay))
        while baseline.count > 1, arrivalMicros &- baseline[0].at > Self.baselineMicros { baseline.removeFirst() }
        let ms = Double(delay - baseline[0].delay) / 1000
        let interval = 1000 / Double(max(fps, 1))
        queue.append((arrivalMicros, ms, ms > 2 * interval, bytes))
        arrivals.append((arrivalMicros, bytes))
        prune(arrivalMicros)
    }

    /// A picture took `ms` to decode.
    public mutating func decoded(ms: Double, at now: UInt64) {
        decodes.append((now, ms))
        prune(now)
    }

    /// A decoded picture reached the screen, or was `replaced` by a newer one before it did.
    public mutating func shown(replaced: Bool, at now: UInt64) {
        shown.append((now, replaced))
        prune(now)
    }

    /// The stream moved to another route (#6398): its one-way delay is its own, so the floor starts over.
    public mutating func pathChanged() {
        baseline.removeAll()
        queue.removeAll()
        arrivals.removeAll()
    }

    private mutating func prune(_ now: UInt64) {
        queue.removeAll { $0.at + 2_000_000 < now }
        arrivals.removeAll { $0.at + 1_000_000 < now }
        decodes.removeAll { $0.at + 2_000_000 < now }
        shown.removeAll { $0.at + 2_000_000 < now }
    }

    /// Within the last `window` µs of `now`.
    private static func within(_ at: UInt64, _ now: UInt64, _ window: UInt64) -> Bool { at + window >= now }

    /// The signals at `now`; the bursts are handed over once.
    public mutating func take(now: UInt64) -> TierSignals {
        prune(now)
        defer { bursts.removeAll() }
        let recent = queue.filter { Self.within($0.at, now, 500_000) }.map(\.ms)
        var s = TierSignals()
        s.frames = recent.count
        s.queueP50Ms = Self.percentile(recent, 0.5)
        s.queueP95Ms = Self.percentile(recent, 0.95)
        let early = queue.filter { Self.within($0.at, now, 1_000_000) && !Self.within($0.at, now, 750_000) }.map(\.ms)
        let late = queue.filter { Self.within($0.at, now, 250_000) }.map(\.ms)
        if let a = Self.mean(early), let b = Self.mean(late) {
            s.queueRising = b - a > 10
            s.queueFalling = b - a < -10
        }
        var busyBytes = 0, busyMicros: UInt64 = 0, busyFrames = 0
        for (a, b) in zip(queue, queue.dropFirst()) where a.ms > Self.busyMs && b.ms > Self.busyMs && b.at > a.at {
            busyBytes += b.bytes
            busyMicros += b.at - a.at
            busyFrames += 1
        }
        if busyFrames >= 3, busyMicros >= 50_000 {
            s.deliveredKbps = Double(busyBytes) * 8 * 1000 / Double(busyMicros)
        } else if arrivals.count >= 4, let first = arrivals.first, let last = arrivals.last, last.at &- first.at >= 200_000 {
            let bytes = arrivals.dropFirst().reduce(0) { $0 + $1.bytes }
            s.deliveredKbps = Double(bytes) * 8 * 1000 / Double(last.at - first.at)
        }
        s.burstKbps = bursts
        let window = queue.filter { Self.within($0.at, now, 500_000) }
        s.lateShare = window.isEmpty ? nil : Double(window.filter(\.late).count) / Double(window.count)
        s.decodeP95Ms = Self.percentile(decodes.map(\.ms), 0.95)
        s.replacedShare = shown.isEmpty ? nil : Double(shown.filter(\.replaced).count) / Double(shown.count)
        return s
    }

    static func percentile(_ values: [Double], _ p: Double) -> Double? {
        guard !values.isEmpty else { return nil }
        let s = values.sorted()
        return s[min(s.count - 1, Int((Double(s.count - 1) * p).rounded()))]
    }

    static func mean(_ values: [Double]) -> Double? { values.isEmpty ? nil : values.reduce(0, +) / Double(values.count) }
}

/// The phone's view of the stream for one tier decision (`TierPolicy.tick`), from `ReceiverSignals.take`.
public struct TierSignals: Equatable, Sendable {
    /// Frames that arrived in the last half second; 0 on a still screen, when nothing below says anything.
    public var frames = 0
    public var queueP50Ms: Double?
    public var queueP95Ms: Double?
    /// The queue grew over the last second (its last quarter's mean over 10 ms above its first's), or shrank.
    public var queueRising = false
    public var queueFalling = false
    /// What the link carried while busy (its capacity), else what came over the last second.
    public var deliveredKbps: Double?
    /// How fast large frames (keyframes, refinement passes) came in since the last take.
    public var burstKbps: [Double] = []
    public var lateShare: Double?
    public var decodeP95Ms: Double?
    public var replacedShare: Double?

    public init() {}
}
