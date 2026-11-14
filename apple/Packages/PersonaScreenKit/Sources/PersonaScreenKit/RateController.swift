import Foundation

/// Bandwidth adaptation from the phone's receiver reports (#3954, docs/extensions/computer-use.md "Adaptation"): it
/// turns each report into a queueing delay and cuts the bitrate 30% over 150 ms, skips frames over 500 ms, and raises
/// it 10% after 2 s under 50 ms, never past the phone's cap.
///
/// The phone stamps arrivals on its clock and the helper stamps captures on its own, so a report's one-way delay
/// carries an unknown offset. The queueing delay is that delay above the lowest one seen over the last 10 s: the offset
/// cancels, and a slow drift between the clocks ages out of the window.
///
/// A phone that picks quality tiers (#6396) sets a bitrate band (`setBand`): the bitrate then moves only inside it, and
/// the phone changes the tier when the band no longer fits the link. With a band set, a cut goes to 85% of the rate the
/// reports say actually arrived over the last second (when they say enough), so a link that dropped is followed in one
/// step, not in several 30% steps; and the first 10 s raise 25% after each calm second (slow start) until the first
/// cut. Without a band (an app before #6396) it adapts exactly as before.
///
/// Times are microseconds on the helper's monotonic clock (`now`). The struct holds no clock of its own, so a test
/// drives it with any timeline.
public struct RateController {
    /// What one report changed.
    public struct Decision: Equatable {
        /// The new target bitrate in kbit/s, or nil when it stays.
        public var kbps: Int?
        /// Frames are being skipped until the queue drains (`admit` says which ones still go).
        public var skipping: Bool
        /// The phone could not decode a frame: the next one should be a keyframe.
        public var keyframe: Bool

        public init(kbps: Int? = nil, skipping: Bool = false, keyframe: Bool = false) {
            self.kbps = kbps
            self.skipping = skipping
            self.keyframe = keyframe
        }
    }

    public static let cutAboveMicros: Int64 = 150_000
    public static let skipAboveMicros: Int64 = 500_000
    public static let raiseBelowMicros: Int64 = 50_000
    /// How long the queue must stay under `raiseBelowMicros` before each raise.
    public static let calmMicros: UInt64 = 2_000_000
    /// After a cut, the next waits this long: one congested moment cuts once, not once for every report it delays.
    public static let cutHoldMicros: UInt64 = 500_000
    /// While skipping, one frame still goes this often, so reports keep coming and say when the queue has drained.
    public static let probeMicros: UInt64 = 250_000
    /// The window the one-way delay's floor is taken over.
    public static let baselineMicros: UInt64 = 10_000_000

    /// With a band: how long slow start lasts from the first report, and how calm a second must be before each raise.
    public static let slowStartMicros: UInt64 = 10_000_000
    public static let slowStartCalmMicros: UInt64 = 1_000_000
    /// With a band: the arrivals the delivered rate is measured over (by the phone's clock), and what makes it a
    /// measurement rather than a guess: at least this many reports spread over at least this long.
    public static let deliveredWindowMicros: UInt64 = 1_000_000
    public static let deliveredMinReports = 4
    public static let deliveredMinSpanMicros: UInt64 = 200_000

    /// The current target bitrate in kbit/s.
    public private(set) var kbps: Int
    /// The phone's cap in kbit/s: the band's ceiling.
    public private(set) var cap: Int
    /// The band's floor in kbit/s: the helper's own minimum until the phone sets a band.
    public private(set) var floorKbps = CapturePlan.minKbps
    /// The phone set a band (#6396): cuts follow the delivered rate and the first seconds raise faster.
    public private(set) var tiered = false
    public private(set) var skipping = false
    /// The last report's queueing delay in µs (refinement frames' excluded); nil before the first.
    public private(set) var lastQueueMicros: Int64?

    /// The window's one-way delays, increasing, each with when it came: the first is the window's lowest.
    private var baseline: [(at: UInt64, delay: Int64)] = []
    private var lastCut: UInt64?
    private var calmSince: UInt64?
    private var lastProbe: UInt64?
    private var firstReport: UInt64?
    private var everCut = false
    /// The last second's reports by arrival (the phone's clock) and the bytes each says came since the one before.
    private var arrivals: [(at: UInt64, bytes: UInt32)] = []

    /// - Parameter cap: the phone's `max_kbps`; the stream starts at `CapturePlan.initialKbps(cap:)`.
    public init(cap: Int) {
        self.cap = CapturePlan.capKbps(cap)
        kbps = CapturePlan.initialKbps(cap: cap)
    }

    /// A new cap from the phone (`update.max_kbps`). A cap below the current bitrate takes effect at once and is
    /// returned; a higher one is reached by raising as usual.
    public mutating func setCap(_ requested: Int) -> Int? {
        setBand(min: nil, max: requested, target: nil)
    }

    /// The phone's bitrate band (#6396) from `start` or `update`: any of its floor (`min_kbps`, which turns the band
    /// on), its ceiling (`max_kbps`) and the bitrate to go to now (`target_kbps`). The bitrate is kept inside the band;
    /// the new one is returned when it changed.
    public mutating func setBand(min floor: Int?, max ceiling: Int?, target: Int?) -> Int? {
        if let ceiling { cap = CapturePlan.capKbps(ceiling) }
        if let floor {
            tiered = true
            floorKbps = CapturePlan.floorKbps(floor, cap: cap)
        } else {
            floorKbps = Swift.min(floorKbps, cap)
        }
        let next = Swift.min(Swift.max(target ?? kbps, floorKbps), cap)
        guard next != kbps else { return nil }
        kbps = next
        if target != nil { calmSince = nil } // a bitrate the phone chose earns its own calm stretch before a raise
        return kbps
    }

    /// The stream moved to another network path (#6398, the phone's `update.path`): the one-way delay changes with the
    /// path, so the 10 s floor starts over from the next report, and so do the delivered rate and the calm stretch (the
    /// old path's say nothing of the new one). The bitrate stays; the phone sets a tier for the new path.
    public mutating func pathChanged() {
        baseline.removeAll()
        arrivals.removeAll()
        calmSince = nil
        lastQueueMicros = nil
    }

    /// The queueing delay of a report with this one-way delay, in µs, after adding it to the window.
    private mutating func queueing(delay: Int64, now: UInt64) -> Int64 {
        while let last = baseline.last, last.delay >= delay { baseline.removeLast() }
        baseline.append((now, delay))
        while baseline.count > 1, now &- baseline[0].at > Self.baselineMicros { baseline.removeFirst() }
        return delay - baseline[0].delay
    }

    /// Adds a report's arrival to the delivered-rate window. An arrival before the last one (the phone's clock moved)
    /// starts the window over.
    private mutating func arrived(_ report: ReceiverReport) {
        if let last = arrivals.last, report.arrivalMicros < last.at { arrivals.removeAll() }
        arrivals.append((report.arrivalMicros, report.bytesSinceLast))
        let newest = report.arrivalMicros
        while arrivals.count > 1, newest &- arrivals[0].at > Self.deliveredWindowMicros { arrivals.removeFirst() }
    }

    /// The rate that arrived over the last second, in kbit/s: the bytes reported after the window's first arrival,
    /// over the time from it to the last. nil when too few reports or too short a time say it.
    public var deliveredKbps: Int? {
        guard arrivals.count >= Self.deliveredMinReports, let first = arrivals.first, let last = arrivals.last else { return nil }
        let span = last.at &- first.at
        guard span >= Self.deliveredMinSpanMicros else { return nil }
        let bytes = arrivals.dropFirst().reduce(UInt64(0)) { $0 + UInt64($1.bytes) }
        return Int(bytes * 8 * 1000 / span)
    }

    /// The link has no queue to speak of: not skipping, and the last report's queueing under the cut threshold. Idle
    /// refinement (`Refinement`) sends only then.
    public var calm: Bool { !skipping && (lastQueueMicros ?? 0) <= Self.cutAboveMicros }

    /// The queueing delay a report shows against the window's floor, without adding it to anything: for a refinement
    /// frame's report, which says whether that pass queued but must not move the floor or the rate.
    public func queueing(of report: ReceiverReport) -> Int64? {
        guard let floor = baseline.first?.delay else { return nil }
        return Int64(truncatingIfNeeded: report.arrivalMicros) - Int64(truncatingIfNeeded: report.captureMicros) - floor
    }

    /// One receiver report, received at `now`. A refinement frame's report (`refinement`, idle refinement's passes) asks
    /// for a keyframe if the phone couldn't decode it and changes nothing else: a large frame sent on purpose, while
    /// the screen is still, says nothing about the link's rate and must not read as congestion.
    public mutating func receive(_ report: ReceiverReport, now: UInt64, refinement: Bool = false) -> Decision {
        guard !refinement else {
            return Decision(skipping: skipping, keyframe: report.flags.contains(.decodingError))
        }
        let delay = Int64(truncatingIfNeeded: report.arrivalMicros) - Int64(truncatingIfNeeded: report.captureMicros)
        let queue = queueing(delay: delay, now: now)
        lastQueueMicros = queue
        arrived(report)
        if firstReport == nil { firstReport = now }
        var decision = Decision(keyframe: report.flags.contains(.decodingError))

        if queue > Self.skipAboveMicros {
            skipping = true
        } else if skipping, queue <= Self.cutAboveMicros {
            skipping = false
            lastProbe = nil
        }

        if queue > Self.cutAboveMicros {
            calmSince = nil
            if lastCut.map({ now &- $0 >= Self.cutHoldMicros }) ?? true {
                lastCut = now
                everCut = true
                let cut = Swift.max(floorKbps, cutTarget())
                if cut != kbps {
                    kbps = cut
                    decision.kbps = cut
                }
            }
        } else if queue < Self.raiseBelowMicros {
            let since = calmSince ?? now
            calmSince = since
            let slowStart = tiered && !everCut && now &- (firstReport ?? now) < Self.slowStartMicros
            if now &- since >= (slowStart ? Self.slowStartCalmMicros : Self.calmMicros) {
                calmSince = now // the next raise needs another calm stretch
                let raised = Swift.min(cap, slowStart ? (kbps * 5 + 3) / 4 : (kbps * 11 + 9) / 10)
                if raised != kbps {
                    kbps = raised
                    decision.kbps = raised
                }
            }
        } else {
            calmSince = nil
        }
        decision.skipping = skipping
        return decision
    }

    /// Where a cut goes: 30% down; with a band and a delivered rate, 85% of that rate, and at least 15% down.
    private func cutTarget() -> Int {
        guard tiered, let delivered = deliveredKbps else { return kbps * 7 / 10 }
        return Swift.min(kbps * 85 / 100, delivered * 85 / 100)
    }

    /// Whether a frame captured at `now` should be encoded: always, but while skipping only one per `probeMicros`.
    public mutating func admit(now: UInt64) -> Bool {
        guard skipping else { return true }
        if let last = lastProbe, now &- last < Self.probeMicros { return false }
        lastProbe = now
        return true
    }
}
