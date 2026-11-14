#if os(iOS) && canImport(PersonaScreenKit)
import Foundation
import PersonaScreenKit

/// One video stream's quality tiers on the phone (#6396, docs/extensions/computer-use.md "Quality tiers"): what the
/// frames say (`ReceiverSignals`) and the tier chosen from it (`TierPolicy`). The stream feeds it from its own task and
/// ticks it every 250 ms; the link hands it the user's choice, the phone's budget and route changes from the main actor.
/// Locked, so either side may call. Nothing here runs per frame on the main actor.
///
/// It starts when the helper's `hello` lists "tiers" with the display's points and pixels; before that, and with a
/// helper from before tiers, it changes nothing and the stream runs as it always has.
nonisolated final class ScreenVideoTiers: @unchecked Sendable {
    /// What the link knows when the stream opens.
    struct Context: Equatable, Sendable {
        var bias: TierLadder.Bias
        var budget: ScreenVideoBudget
        /// The picture's long edge at most this many pixels (`ScreenVideoLink.maxSize`).
        var maxSize: Int
        var link: LinkStats?
        /// The tier this computer last settled on over the same kind of route, in this app run.
        var remembered: Int?
    }

    private let lock = NSLock()
    private var context: Context
    private var signals = ReceiverSignals()
    private var policy: TierPolicy?

    init(_ context: Context) { self.context = context }

    /// The tiers run: a helper with them said hello.
    var active: Bool { lock.withLock { policy != nil } }

    /// The stream's `hello`. A helper with tiers starts the policy; its first tier is returned to send.
    func hello(_ h: HelloMessage, now: Double) -> TierPolicy.Change? {
        guard h.features?.contains("tiers") == true, let points = h.points, let pixels = h.pixels,
              points.count == 2, pixels.count == 2 else { return nil }
        return lock.withLock {
            let c = context
            let p = TierPolicy(display: .init(points: points, pixels: pixels, maxSize: c.maxSize), bias: c.bias,
                               capKbps: c.budget.maxKbps, maxFps: c.budget.fps, maxScale: c.budget.maxScale, link: c.link,
                               remembered: c.remembered, now: now)
            policy = p
            signals = ReceiverSignals()
            return p.last
        }
    }

    func frame(_ f: VideoFrame, arrival: UInt64, firstByte: UInt64?, bytes: Int) {
        lock.withLock {
            guard let policy else { return }
            signals.frame(captureMicros: f.captureMicros, arrivalMicros: arrival, firstByteMicros: firstByte, bytes: bytes,
                          refinement: f.flags.contains(.refinement), fps: policy.tier.fps)
        }
    }

    func decoded(ms: Double, at now: UInt64) { lock.withLock { if policy != nil { signals.decoded(ms: ms, at: now) } } }

    func shown(replaced: Bool, at now: UInt64) { lock.withLock { if policy != nil { signals.shown(replaced: replaced, at: now) } } }

    func helper(_ t: TimingMessage) { lock.withLock { policy?.helper(t) } }

    /// The 250 ms decision.
    func tick(at micros: UInt64) -> TierPolicy.Change? {
        lock.withLock {
            guard policy != nil else { return nil }
            let s = signals.take(now: micros)
            return policy?.tick(s, now: Double(micros) / 1_000_000)
        }
    }

    func setBias(_ b: TierLadder.Bias, at micros: UInt64) -> TierPolicy.Change? {
        lock.withLock {
            context.bias = b
            return policy?.setBias(b, now: Double(micros) / 1_000_000)
        }
    }

    func setBudget(_ b: ScreenVideoBudget, at micros: UInt64) -> TierPolicy.Change? {
        lock.withLock {
            context.budget = b
            return policy?.setBudget(capKbps: b.maxKbps, maxFps: b.fps, maxScale: b.maxScale, now: Double(micros) / 1_000_000)
        }
    }

    /// Another route (#6398): the phone's delay floor starts over, and the policy looks again.
    func path(_ change: PathChange, at micros: UInt64) -> TierPolicy.Change? {
        lock.withLock {
            context.link = change.to
            signals.pathChanged()
            return policy?.path(change, now: Double(micros) / 1_000_000)
        }
    }

    /// A tier as the `update` that sets it on the helper.
    static func update(_ c: TierPolicy.Change) -> UpdateMessage {
        UpdateMessage(fps: c.tier.fps, maxKbps: c.tier.maxKbps, scale: c.tier.scale, minKbps: c.tier.minKbps,
                      targetKbps: c.targetKbps)
    }
}
#endif
