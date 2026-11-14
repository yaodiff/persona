import Foundation

/// Idle refinement (#3954, `frame` flag 2): when the screen stops changing, the last picture is encoded again at a
/// higher bitrate a few times, so text sharpens even at a low tier. Pure, so it tests anywhere; the capture source keeps
/// one on its queue, tells it about each change and each pass, and asks it on a timer whether a pass is due.
///
/// A pass goes only while the link is calm (`RateController.calm`), so refinement never builds a queue, and a pass that
/// came back queued ends the passes until the next change. Its frames' reports are kept out of the rate controller's
/// numbers (`RateController.receive(_:now:refinement:)`): a large frame sent on purpose must not read as congestion, nor
/// count as the link's delivered rate.
///
/// The picture refined is the latest ScreenCaptureKit captured, not the last one sent: frames skipped while the queue
/// drained may carry the screen's final state, and with the screen still nothing else would ever send it.
public struct Refinement: Equatable, Sendable {
    /// Quiet time after the last change before the first pass, and between passes.
    public static let firstAfterMicros: UInt64 = 300_000
    public static let betweenMicros: UInt64 = 200_000
    /// Passes per still moment. Each refines the one before (the encoder codes what is still off from the source).
    public static let passes = 3
    /// Tries per still moment, dropped ones included: VideoToolbox's low-latency rate control drops whole frames while
    /// the stream is over budget (screenbench: for seconds after a keyframe at the lowest tier), so a dropped pass is
    /// tried again 200 ms later, and the screen's final picture still goes out once the budget allows.
    public static let attempts = 12
    /// A pass's bitrate as a multiple of the stream's current target: one frame gets about this many frames' budget.
    public static let boost = 4

    private var changedAt: UInt64?
    private var lastPassAt: UInt64?
    public private(set) var done = 0
    private var tries = 0

    public init() {}

    /// The screen changed (a frame went out) at `now`, or the size did: refinement starts over.
    public mutating func changed(at now: UInt64) {
        changedAt = now
        lastPassAt = nil
        done = 0
        tries = 0
    }

    /// Nothing to refine (capture stopped, or paused): no pass until the next change.
    public mutating func reset() {
        changedAt = nil
        lastPassAt = nil
        done = 0
        tries = 0
    }

    /// A pass's frame came back queued past the rate controller's cut threshold: no more passes until the next change.
    public mutating func queued() { done = Self.passes }

    /// Whether a pass is due at `now`, the link being `calm`.
    public func due(at now: UInt64, calm: Bool) -> Bool {
        guard calm, let changedAt, done < Self.passes, tries < Self.attempts else { return false }
        if let lastPassAt { return now &- lastPassAt >= Self.betweenMicros }
        return now &- changedAt >= Self.firstAfterMicros
    }

    /// A pass went to the encoder at `now`.
    public mutating func passed(at now: UInt64) {
        lastPassAt = now
        done += 1
        tries += 1
    }

    /// The encoder dropped that pass: it doesn't count, and is tried again after `betweenMicros`.
    public mutating func dropped() { done = max(0, done - 1) }

    /// The bitrate a pass is encoded at, in kbit/s: `boost` times the target, within the helper's range.
    public static func kbps(target: Int) -> Int { min(CapturePlan.maxKbps, max(CapturePlan.minKbps, target * boost)) }
}
