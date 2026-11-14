import Foundation
import PersonaScreenKit

/// A simulated live-screen stream for the quality tiers (#6396): the helper's `RateController` and an encoder model on
/// one end, a shaped link in between, the phone's `ReceiverSignals` and `TierPolicy` on the other, all the real types.
/// The link carries a scheduled bandwidth with a one-way delay and jitter; the encoder spends 90% of its target on a
/// moving screen (each frame changes, the worst case for the link) or what lighter content needs (`content`), sends a
/// keyframe after a size change and, like the helper, every `keyframeEvery` seconds, and drops frames while it is over
/// half a second of budget in debt, the debt paid off with time, as VideoToolbox's low-latency rate control does
/// (screenbench). Frames arrive in the order they were sent, as on the stream's one connection; the phone reads what
/// arrived as it arrives, or every `readEvery` seconds (several frames to a read).
/// Policy changes and reports reach the other end after the one-way delay.
struct LinkSimulation {
    struct Link {
        /// kbit/s the link carries at time t (seconds).
        var kbps: (Double) -> Double
        /// Round trip in ms without a queue, and how much each one-way trip may add at random.
        var rttMs: Double
        var jitterMs: Double = 0
        var stats: LinkStats
    }

    struct Result {
        var changes: [(at: Double, from: String, to: String, reason: TierReason)] = []
        /// Each frame's arrival time and its queueing above the link's own delay, in ms.
        var latencies: [(at: Double, ms: Double)] = []
        var frames: [(at: Double, tier: String)] = []

        func latency(from: Double, to: Double) -> (p50: Double, p95: Double) {
            let v = latencies.filter { $0.at >= from && $0.at < to }.map(\.ms).sorted()
            guard !v.isEmpty else { return (0, 0) }
            return (v[v.count / 2], v[min(v.count - 1, Int(Double(v.count) * 0.95))])
        }

        func fps(from: Double, to: Double) -> Double { Double(frames.filter { $0.at >= from && $0.at < to }.count) / (to - from) }

        func changes(from: Double, to: Double) -> Int { changes.filter { $0.at >= from && $0.at < to }.count }

        /// Seconds spent on each tier between `from` and `to`, by its label.
        func time(from: Double, to: Double) -> [String: Double] {
            var out: [String: Double] = [:]
            var tier = frames.last { $0.at < from }?.tier ?? frames.first?.tier ?? ""
            var at = from
            for f in frames where f.at >= from && f.at < to && f.tier != tier {
                out[tier, default: 0] += f.at - at
                (tier, at) = (f.tier, f.at)
            }
            out[tier, default: 0] += to - at
            return out
        }
    }

    var display = TierPolicy.Display(points: [1512, 982], pixels: [3024, 1964], maxSize: 2868)
    var bias = TierLadder.Bias.auto
    var capKbps = 12_000
    var maxFps = 60
    /// A fixed tier instead of the policy (the A/B), by index into the ladder.
    var fixed: Int?
    var seed: UInt64 = 1
    /// Prints the helper's state once a second (debugging a scenario).
    var trace = false
    /// What the screen needs, in bits per pixel per frame: each frame is that or 90% of the target, whichever is less
    /// (scrolling text on a 1080p screen, #7842: about 0.011). nil: 90% of the target, a screen that changes everywhere.
    var content: Double?
    /// A keyframe this often, in seconds, as well as after a size change (the helper: every 10 s); nil, only then.
    var keyframeEvery: Double?
    /// A keyframe's bits per pixel (screen text, #7842: about 1).
    var keyframeBits = 0.33
    /// The phone takes what arrived every this many seconds, all of it at once (a busy phone, a link that delivers in
    /// bursts, #7842); nil: each frame as it arrives.
    var readEvery: Double?

    static func label(_ t: TierLadder.Tier) -> String {
        let s = t.scale == t.scale.rounded() ? String(Int(t.scale)) : String(t.scale)
        return "\(s)x\(t.fps)"
    }

    func run(_ link: Link, seconds: Double) -> Result {
        var result = Result()
        var policy = TierPolicy(display: display, bias: bias, capKbps: capKbps, maxFps: maxFps, link: link.stats, now: 0)
        let ladder = policy.ladder
        var running = fixed.map { ladder[$0] } ?? policy.tier
        var rate = RateController(cap: running.maxKbps)
        _ = rate.setBand(min: running.minKbps, max: running.maxKbps, target: fixed == nil ? policy.last.targetKbps : running.minKbps)
        var signals = ReceiverSignals()
        var random = seed
        func jitter() -> Double {
            random = random &* 6364136223846793005 &+ 1442695040888963407
            return Double(random >> 11) / Double(1 << 53) * link.jitterMs
        }
        let oneWay = link.rttMs / 2
        let offset: UInt64 = 7_000_000_000 // the phone's clock is far from the helper's
        func micros(_ s: Double) -> UInt64 { UInt64(max(0, s) * 1_000_000) }

        // Pending deliveries, by time: reports to the helper, tier changes to the helper.
        var reports: [(at: Double, report: ReceiverReport)] = []
        var updates: [(at: Double, change: TierPolicy.Change)] = []
        // Frames on their way to the phone, handed to its signals when they arrive.
        var inbound: [(at: Double, first: Double, capture: Double, bytes: Int, keyframe: Bool)] = []
        var linkFree = 0.0, debt = 0.0, keyframe = true, debtAt = 0.0, keyframeAt = 0.0
        var lastArrival = 0.0 // one ordered stream: no frame arrives before the one sent ahead of it
        var number: UInt32 = 0
        var sentBytes = 0, skipped = 0
        var nextTick = 0.25, nextTiming = 1.0
        var t = 0.0
        while t < seconds {
            // The helper: deliveries due by now.
            while let i = updates.indices.first(where: { updates[$0].at <= t }) {
                let c = updates.remove(at: i).change
                if c.tier.width != running.width || c.tier.height != running.height { keyframe = true }
                running = c.tier
                _ = rate.setBand(min: c.tier.minKbps, max: c.tier.maxKbps, target: c.targetKbps)
            }
            reports.sort { $0.at < $1.at }
            while let first = reports.first, first.at <= t {
                reports.removeFirst()
                _ = rate.receive(first.report, now: micros(first.at))
            }
            if trace, t >= nextTiming {
                print(String(format: "t=%.1f tier=%@ kbps=%d skipping=%d sent=%d skipped=%d backlog=%.0fms debt=%.0f",
                             t, Self.label(running), rate.kbps, rate.skipping ? 1 : 0, sentBytes * 8 / 1000, skipped,
                             max(0, linkFree - t) * 1000, debt))
            }
            if t >= nextTiming {
                let timing = TimingMessage(captureMs: 0, encodeMs: 5, networkMs: oneWay, kbps: sentBytes * 8 / 1000, fps: 0,
                                           targetKbps: rate.kbps, skipped: skipped, blockedMs: 0, pending: 0)
                if fixed == nil { policy.helper(timing) }
                sentBytes = 0
                skipped = 0
                nextTiming += 1
            }
            // The phone: what has arrived by now, then a decision every 250 ms.
            inbound.sort { $0.at < $1.at }
            while let f = inbound.first, f.at <= t {
                inbound.removeFirst()
                signals.frame(captureMicros: micros(f.capture), arrivalMicros: micros(f.at) + offset,
                              firstByteMicros: micros(f.first) + offset, bytes: f.bytes, refinement: false,
                              keyframe: f.keyframe, fps: running.fps)
                signals.decoded(ms: 4, at: micros(f.at) + offset)
                signals.shown(replaced: false, at: micros(f.at) + offset)
            }
            if t >= nextTick {
                let before = policy.tier
                if fixed == nil, let c = policy.tick(signals.take(now: micros(t) + offset), now: t) {
                    result.changes.append((t, Self.label(before), Self.label(c.tier), c.reason))
                    updates.append((t + oneWay / 1000, c))
                }
                nextTick += 0.25
            }

            // One captured frame.
            let interval = 1 / Double(running.fps)
            defer { t += interval }
            debt = max(0, debt - Double(rate.kbps) * 1000 * (t - debtAt))
            debtAt = t
            guard rate.admit(now: micros(t)) else { skipped += 1; continue }
            if let every = keyframeEvery, t - keyframeAt >= every { keyframe = true }
            let budget = Double(rate.kbps) * 1000 * interval
            let pixels = Double(running.width * running.height)
            let bits = keyframe ? pixels * keyframeBits : min(0.9 * budget, content.map { $0 * pixels } ?? .infinity)
            if !keyframe, debt > Double(rate.kbps) * 1000 * 0.5 { continue } // the encoder drops it
            let isKeyframe = keyframe
            if keyframe { keyframeAt = t }
            keyframe = false
            debt += bits
            number &+= 1
            let bytes = Int(bits / 8)
            sentBytes += bytes
            let start = max(t + 0.006, linkFree) // ~6 ms to encode
            let capacity = max(link.kbps(start), 1) * 1000
            linkFree = start + bits / capacity
            var arrival = max(linkFree + (oneWay + jitter()) / 1000, lastArrival)
            var firstByte = min(start + (oneWay + jitter()) / 1000, arrival)
            if let every = readEvery { // read at the next read after it came, as are its first bytes
                (arrival, firstByte) = ((arrival / every).rounded(.up) * every, (firstByte / every).rounded(.up) * every)
            }
            lastArrival = arrival
            result.latencies.append((arrival, (arrival - t - oneWay / 1000 - 0.006) * 1000))
            result.frames.append((arrival, Self.label(running)))
            inbound.append((arrival, firstByte, t, bytes, isKeyframe))
            let report = ReceiverReport(lastFrame: number, captureMicros: micros(t), arrivalMicros: micros(arrival) + offset,
                                        bytesSinceLast: UInt32(bytes), flags: [])
            reports.append((arrival + (oneWay + jitter()) / 1000, report))
        }
        return result
    }
}
