import Foundation

/// Why the tier last changed (#6396), for the stats overlay's plain words.
public enum TierReason: String, Equatable, Sendable {
    /// The stream's first tier, from its route.
    case start
    /// The link slowed down: queueing grew past what the bitrate alone could absorb.
    case linkSlowed = "link_slowed"
    /// The link stayed steady with room to spare.
    case linkSteady = "link_steady"
    /// The route or the phone's network changed (#6398), or Low Data Mode / cellular changed the cap.
    case networkChanged = "network_changed"
    /// Low Power Mode or the phone's heat capped the frame rate.
    case phoneWarm = "phone_warm"
    /// The phone couldn't decode or show pictures as fast as they came.
    case phoneBehind = "phone_behind"
    /// The user chose another bias in ⋯ › Quality.
    case choice
}

/// Picks the live screen's quality tier on the phone (#6396), every 250 ms, from the stream's own signals
/// (`ReceiverSignals`), the helper's `timing` and the route's `LinkStats` (#6398). The helper's `RateController` moves
/// the bitrate inside the tier's band on every frame; this moves to another tier only when the band no longer fits.
///
/// Down fast: when the helper's bitrate is already near the band's floor and queueing stays over 150 ms, or queueing
/// passes 400 ms for a quarter second, or the helper is skipping frames while the queue is over 150 ms, the newest frame
/// still queued (a backlog now, not one that just came in at once), and the tier's floor doesn't fit 80% of the rate
/// that arrived (a tier that fits drains a backlog by itself: the helper cuts toward its floor, under the link): the
/// helper has already cut the bitrate to what arrived, and the tier goes straight to the highest one whose floor fits
/// 80% of that rate (at least one down). Another
/// step down waits 1 s and a queue that isn't draining (a backlog drains at the link's rate whatever the tier; stepping
/// down again for it would land too low), or comes at once only for a runaway queue, over twice what it was at the last
/// step and 100 ms more (a new size's keyframe alone makes the queue grow for a moment). When decoding or showing can't
/// keep up for 2 s, the next tier with a lower frame rate.
///
/// Up slow: one tier, after a stable window (8 s; 3 s in the stream's first 10 s or after a route change) of queueing
/// under 50 ms (a keyframe's wake left out: its queue is its size, and its burst is the proof, #7842), nothing skipped
/// and decoding keeping up, and only with proof of headroom: the link carried, calmly, at
/// least 1.25× the next tier's floor (the screen using most of the helper's bitrate, so it was a test of the link), or
/// three large frames arrived at 1.5× that floor. Light content, sending at most half the next tier's floor, never fills
/// the bitrate and on a fast link its keyframes come in too quickly to measure: after twice the window it steps up
/// without proof. A tier fits a link when its floor is at most 80% of what the link
/// carries, the same rule both ways, so a step up is never one the down rule would undo on the same numbers. A step up
/// undone within 15 s doubles that tier's window (to 60 s at most); two minutes on it start the window over.
///
/// The helper's `timing` comes once a second while frames flow and not at all on a still screen, so what it said is
/// used only while fresh: its skipped frames until the next one is due (1.25 s), its bitrate as proof once (#7842; a
/// still screen kept stepping down, once a second, on the last second's skipped frames). A new bias or budget starts from at least the current tier's floor, which the link
/// is carrying, never from a still screen's low numbers.
///
/// Times are seconds on any monotonic clock; pure, so it tests with any timeline.
public struct TierPolicy: Sendable {
    public struct Change: Equatable, Sendable {
        public var tier: TierLadder.Tier
        public var index: Int
        public var reason: TierReason
        public var targetKbps: Int
    }

    public struct Display: Equatable, Sendable {
        public var points: [Int]
        public var pixels: [Int]
        public var maxSize: Int

        public init(points: [Int], pixels: [Int], maxSize: Int) {
            self.points = points
            self.pixels = pixels
            self.maxSize = maxSize
        }

        var pointSize: (width: Int, height: Int) { (points.first ?? 0, points.dropFirst().first ?? 0) }
        var pixelSize: (width: Int, height: Int) { (pixels.first ?? 0, pixels.dropFirst().first ?? 0) }
    }

    static let downQueueMs = 150.0
    static let emergencyQueueMs = 400.0
    static let emergencyHold = 0.25
    static let upQueueMs = 50.0
    static let exhausted = 1.15
    static let downSpacing = 1.0
    static let decodeHold = 2.0
    static let stableWindow = 8.0
    static let earlyWindow = 3.0
    static let early = 10.0
    static let undoWithin = 15.0
    static let longestWindow = 60.0
    static let windowReset = 120.0
    static let burstProofs = 3
    /// Light content (#6396, 2026-11-15): a screen sending at most this share of the next tier's floor can step up after
    /// `lightWindows` calm windows without proof; it never fills the bitrate, and on a fast link its keyframes come in
    /// too quickly to measure, so no proof ever comes.
    static let lightShare = 0.5
    static let lightWindows = 2.0
    static let helperStale = 1.25

    public private(set) var ladder: [TierLadder.Tier]
    public private(set) var index: Int
    public private(set) var bias: TierLadder.Bias
    public private(set) var capKbps: Int
    public private(set) var maxFps: Int
    /// The most pixels per point the phone allows (Data Saver on mobile data: 1); nil, no limit.
    public private(set) var maxScale: Double?
    public private(set) var last: Change
    public let display: Display

    private var shortUntil: Double
    private var lastDownAt: Double?
    private var queueAtDown = 0.0
    private var emergencySince: Double?
    private var decodeBadSince: Double?
    private var calmSince: Double?
    /// Headroom shown in this calm stretch: what the screen sent with the helper at its ceiling, and large frames that
    /// came in at 1.5× the next tier's floor (with the slowest of them, over 1.5).
    private var motionKbps: Int?
    private var bursts = 0
    private var burstKbps: Int?
    /// The most the helper sent in a second of this calm stretch (fresh `timing` only); nil before one came.
    private var heaviestSent: Int?
    /// The window before stepping up into each tier, by index; doubled when a step up into it was undone.
    private var windows: [Int: Double] = [:]
    private var lastUp: (at: Double, into: Int)?
    private var tierSince: Double
    private var helperTarget: Int?
    private var helperSent: Int?
    private var helperSkipped = 0
    /// A `timing` came since the last tick, and when the last one was first ticked on.
    private var helperFresh = false
    private var helperAt: Double?

    /// The stream's first tier: from the route (`link`; nil before one is measured, read as the relay over Wi-Fi), or
    /// the tier this computer last settled on over the same route (`remembered`, an index into the same ladder).
    public init(display: Display, bias: TierLadder.Bias, capKbps: Int, maxFps: Int, maxScale: Double? = nil, link: LinkStats?,
                remembered: Int? = nil, now: Double) {
        self.display = display
        self.bias = bias
        self.capKbps = capKbps
        self.maxFps = maxFps
        let normalizedScale = Self.normalizedScale(maxScale)
        self.maxScale = normalizedScale
        let tiers = Self.tiers(display: display, bias: bias, capKbps: capKbps, maxFps: maxFps, maxScale: normalizedScale)
        ladder = tiers
        let start = remembered.map { min(max($0, 0), tiers.count - 1) }
            ?? Self.index(rung: Self.rung(link), in: tiers, display: display, bias: bias, maxFps: maxFps, maxScale: normalizedScale)
        let startNow = now.isFinite ? now : 0
        index = start
        shortUntil = startNow + Self.early
        tierSince = startNow
        last = Change(tier: tiers[start], index: start, reason: .start, targetKbps: tiers[start].minKbps)
    }

    public var tier: TierLadder.Tier { ladder[index] }

    // MARK: Inputs

    /// The helper's `timing` (#6396 fields): its target, what went out and the frames it skipped that second.
    public mutating func helper(_ t: TimingMessage) {
        helperTarget = t.targetKbps
        helperSent = t.kbps
        helperSkipped = t.skipped ?? 0
        helperFresh = true
    }

    /// One decision, every 250 ms. A change is returned for the app to send as an `update`.
    public mutating func tick(_ s: TierSignals, now: Double) -> Change? {
        guard now.isFinite else { return nil }
        let fresh = helperFresh
        helperFresh = false
        if fresh { helperAt = now }
        let skipping = helperSkipped > 0 && helperAt.map { now - $0 < Self.helperStale } ?? false
        let queue = s.queueP50Ms.flatMap { $0.isFinite && $0 >= 0 ? $0 : nil } ?? 0
        // A backlog now: the newest frame still waited. Every step down needs one: one that came in all at once after a
        // stall leaves the p50 high for a moment though it is gone, and the helper has cut to its floor by then.
        let backlog = (s.queueLastMs.flatMap { $0.isFinite && $0 >= 0 ? $0 : nil } ?? queue) > Self.upQueueMs

        // Down, fast: a long queue, or skipped frames while a queue is still there, for a quarter second. The helper
        // skips until a report shows 150 ms or less, and its `timing` says so up to a second later: once the phone sees
        // the queue under that, the backlog is gone and so is the reason (2026-11-15: a stall that was over stepped a
        // light screen down to 15 fps).
        if backlog, queue > Self.emergencyQueueMs || (skipping && queue > Self.downQueueMs) {
            let since = emergencySince ?? now
            emergencySince = since
            if now - since >= Self.emergencyHold, outgrown(s.deliveredKbps), canDown(now, s), index < ladder.count - 1 {
                return down(to: fitting(s.deliveredKbps), delivered: s.deliveredKbps, queue: queue, reason: .linkSlowed, now: now)
            }
        } else {
            emergencySince = nil
        }

        // Down: the bitrate is at the band's floor and the queue still won't drain.
        if backlog, queue > Self.downQueueMs, let target = helperTarget, Double(target) <= Self.exhausted * Double(tier.minKbps),
           outgrown(s.deliveredKbps), canDown(now, s), index < ladder.count - 1 {
            return down(to: fitting(s.deliveredKbps), delivered: s.deliveredKbps, queue: queue, reason: .linkSlowed, now: now)
        }

        // The phone can't keep up: a lower frame rate.
        let interval = 1000 / Double(max(tier.fps, 1))
        let behind = (s.decodeP95Ms.map { $0.isFinite && $0 > 1.5 * interval } ?? false)
            || (s.replacedShare.map { $0.isFinite && $0 > 1.0 / 3 } ?? false)
        if behind {
            let since = decodeBadSince ?? now
            decodeBadSince = since
            if now - since >= Self.decodeHold, let slower = ladder.indices.first(where: { $0 > index && ladder[$0].fps < tier.fps }) {
                decodeBadSince = nil
                return down(to: slower, delivered: s.deliveredKbps, queue: queue, reason: .phoneBehind, now: now)
            }
        } else {
            decodeBadSince = nil
        }

        // Up, slow: a calm window with proof of headroom. A still screen (no frames) neither proves nor breaks it.
        if s.frames > 0 {
            let calm = (s.queueP95Ms.map { $0.isFinite && $0 >= 0 && $0 < Self.upQueueMs } ?? true) && !skipping && !behind
            if !calm {
                forgetProof()
                return nil
            }
        }
        if calmSince == nil { calmSince = now }
        if index > 0 {
            let next = ladder[index - 1]
            if fresh, let sent = helperSent { heaviestSent = max(heaviestSent ?? 0, sent) }
            if fresh, let target = helperTarget, let sent = helperSent, Double(sent) >= 0.8 * Double(target),
               Double(sent) >= 1.25 * Double(next.minKbps) {
                motionKbps = max(motionKbps ?? 0, sent)
            }
            for b in s.burstKbps where b.isFinite && b >= 1.5 * Double(next.minKbps) {
                if bursts < Self.burstProofs { bursts += 1 }
                burstKbps = min(burstKbps ?? .max, TierLadder.clampedInt(b / 1.5))
            }
        }
        if let up = lastUp, up.into == index, now - tierSince >= Self.windowReset { windows[index] = nil }
        guard index > 0, let calmSince else { return nil }
        let window = window(into: index - 1, now: now)
        guard now - calmSince >= window else { return nil }
        let proven = max(motionKbps ?? 0, bursts >= Self.burstProofs ? burstKbps ?? 0 : 0)
        if proven > 0 { return up(proven: proven, now: now) }
        // Light content: a step up adds little, and the down rule and the doubled window cover one that doesn't hold.
        let next = ladder[index - 1]
        if let heaviest = heaviestSent, Double(heaviest) <= Self.lightShare * Double(next.minKbps),
           now - calmSince >= Self.lightWindows * window {
            return up(proven: next.minKbps, now: now)
        }
        return nil
    }

    /// The stream moved to another route, or the phone's interface changed (#6398). A start tier below the current one
    /// is taken at once; otherwise the tier stays and the next step up waits only the short window.
    public mutating func path(_ change: PathChange, now: Double) -> Change? {
        let at = now.isFinite ? now : tierSince
        forgetProof()
        shortUntil = at + Self.early
        let start = Self.index(rung: Self.rung(change.to), in: ladder, display: display, bias: bias, maxFps: maxFps, maxScale: maxScale)
        guard start > index else { return nil }
        return move(to: start, target: ladder[start].minKbps, reason: .networkChanged, now: at)
    }

    /// ⋯ › Quality chose another bias: the new ladder's tier that the current rate fits.
    public mutating func setBias(_ b: TierLadder.Bias, now: Double) -> Change? {
        guard b != bias else { return nil }
        bias = b
        return rebuild(reason: .choice, now: now.isFinite ? now : tierSince)
    }

    /// The phone's caps changed (cellular, Low Data Mode, Low Power Mode, heat, Data Saver): a new ladder, the tier the
    /// rate fits.
    public mutating func setBudget(capKbps cap: Int, maxFps fps: Int, maxScale scale: Double? = nil, now: Double) -> Change? {
        let normalized = Self.normalizedScale(scale)
        guard cap != capKbps || fps != maxFps || normalized != maxScale else { return nil }
        let reason: TierReason = fps < maxFps ? .phoneWarm : .networkChanged
        capKbps = cap
        maxFps = fps
        maxScale = normalized
        return rebuild(reason: reason, now: now.isFinite ? now : tierSince)
    }

    private static func normalizedScale(_ scale: Double?) -> Double? {
        scale.flatMap { $0.isFinite ? $0 : nil }
    }

    // MARK: Moves

    private mutating func forgetProof() {
        calmSince = nil
        motionKbps = nil
        bursts = 0
        burstKbps = nil
        heaviestSent = nil
    }

    private func canDown(_ now: Double, _ s: TierSignals) -> Bool {
        guard let lastDownAt else { return true }
        if now - lastDownAt >= Self.downSpacing, !s.queueFalling { return true }
        let queue = s.queueP50Ms.flatMap { $0.isFinite && $0 >= 0 ? $0 : nil } ?? 0
        return s.queueRising && queue > 2 * queueAtDown + 100
    }

    /// The tier's floor doesn't fit 80% of the rate that arrived (or that rate is unknown): its band can't get under the
    /// link.
    private func outgrown(_ delivered: Double?) -> Bool {
        guard let d = delivered, d.isFinite, d >= 0 else { return true }
        return Double(tier.minKbps) > 0.8 * d
    }

    /// The best tier below this one whose floor fits 80% of the rate that arrived; the next one down when it's unknown.
    private func fitting(_ delivered: Double?) -> Int {
        guard let d = delivered, d.isFinite, d >= 0 else { return index + 1 }
        return ladder.indices.first { $0 > index && Double(ladder[$0].minKbps) <= 0.8 * d } ?? ladder.count - 1
    }

    private func window(into i: Int, now: Double) -> Double {
        let base = now < shortUntil ? Self.earlyWindow : Self.stableWindow
        return max(base, windows[i] ?? 0)
    }

    private mutating func down(to i: Int, delivered: Double?, queue: Double, reason: TierReason, now: Double) -> Change {
        // A step up undone soon: that tier waits twice as long next time.
        if let up = lastUp, up.into == index, now - up.at < Self.undoWithin {
            windows[index] = min(Self.longestWindow, max(Self.stableWindow, windows[index] ?? 0) * 2)
        }
        lastDownAt = now
        queueAtDown = queue
        emergencySince = nil
        let t = ladder[i]
        let target = delivered.flatMap { $0.isFinite && $0 >= 0 ? min(t.maxKbps, max(t.minKbps, TierLadder.clampedInt(0.85 * $0))) : nil } ?? t.minKbps
        return move(to: i, target: target, reason: reason, now: now)
    }

    private mutating func up(proven: Int, now: Double) -> Change {
        let i = index - 1
        lastUp = (now, i)
        let t = ladder[i]
        return move(to: i, target: min(t.maxKbps, max(t.minKbps, proven)), reason: .linkSteady, now: now)
    }

    private mutating func move(to i: Int, target: Int, reason: TierReason, now: Double) -> Change {
        index = i
        tierSince = now
        forgetProof()
        decodeBadSince = nil
        last = Change(tier: ladder[i], index: i, reason: reason, targetKbps: target)
        return last
    }

    private mutating func rebuild(reason: TierReason, now: Double) -> Change {
        let current = tier
        // At least the current tier's floor: the link carries it (the tier would have stepped down otherwise), and a still
        // screen's bitrate says nothing about the link (#7842: Smoother on a still screen fell to 15 fps).
        let rate = max(helperTarget ?? last.targetKbps, current.minKbps)
        ladder = Self.tiers(display: display, bias: bias, capKbps: capKbps, maxFps: maxFps, maxScale: maxScale)
        windows.removeAll()
        lastUp = nil
        // The best tier the current rate fits, no richer in pixels a second than the one before (a new bias moves
        // within what the link has shown, not past it).
        let i = ladder.indices.first { ladder[$0].minKbps <= rate && ladder[$0].pixelRate <= current.pixelRate } ?? ladder.count - 1
        let t = ladder[i]
        return move(to: i, target: min(t.maxKbps, max(t.minKbps, rate)), reason: reason, now: now)
    }

    // MARK: Start

    /// Which of the bias's steps a route starts on: the same network 0; Tailscale (or direct IPv6) 0 under 30 ms, else 1;
    /// the relay 1 over Wi-Fi, 2 over cellular. Nothing measured yet: the relay over Wi-Fi.
    static func rung(_ link: LinkStats?) -> Int {
        guard let link else { return 1 }
        switch link.kind {
        case .lan: return 0
        case .tailscale, .ipv6:
            let rtt = [link.minRttMs, link.srttMs].compactMap { $0 }.first { $0.isFinite && $0 >= 0 } ?? .infinity
            return rtt < 30 ? 0 : 1
        case .relay: return link.via == .cellular ? 2 : 1
        }
    }

    /// The ladder under the phone's caps, without the tiers whose picture has more than `maxScale` pixels per point
    /// (Data Saver: 1× only, which on a display at one pixel per point is every tier; every ladder ends on 1× tiers, so it
    /// never empties).
    static func tiers(display: Display, bias: TierLadder.Bias, capKbps: Int, maxFps: Int, maxScale: Double?) -> [TierLadder.Tier] {
        let all = TierLadder.make(points: display.pointSize, pixels: display.pixelSize, maxSize: display.maxSize, bias: bias,
                                  capKbps: capKbps, maxFps: maxFps)
        guard let maxScale, maxScale.isFinite else { return all }
        let kept = all.filter { $0.pixelsPerPoint <= maxScale + 1e-9 }
        return kept.isEmpty ? Array(all.suffix(1)) : kept
    }

    /// The tier of `ladder` for a rung: the bias's step of that rank (`TierLadder.steps`) on this display, under the
    /// phone's frame-rate cap and `maxScale`, and the best tier no richer in pixels a second. Counted on the steps, not
    /// on the ladder: where steps merge (a display at one pixel per point) the ladder is shorter, and the relay started
    /// a tier lower on such a display than on a Retina one (30 fps instead of 60).
    static func index(rung: Int, in ladder: [TierLadder.Tier], display: Display, bias: TierLadder.Bias, maxFps: Int,
                      maxScale: Double? = nil) -> Int {
        guard !ladder.isEmpty else { return 0 }
        let steps = TierLadder.steps(bias)
        let step = steps[min(max(rung, 0), steps.count - 1)]
        let scale = maxScale.flatMap { $0.isFinite ? min(step.scale, $0) : nil } ?? step.scale
        let size = CapturePlan.size(points: display.pointSize, pixels: display.pixelSize, scale: scale, maxSize: display.maxSize)
        let ref = TierLadder.pixelRate(width: size.width, height: size.height, fps: CapturePlan.fps(min(step.fps, maxFps)))
        return ladder.indices.first { ladder[$0].pixelRate <= ref } ?? ladder.count - 1
    }
}
