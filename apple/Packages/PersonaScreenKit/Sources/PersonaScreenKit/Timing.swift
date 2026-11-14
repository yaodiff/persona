import Foundation

/// What the helper puts in `timing` (#3954): per frame, how long capture and encoding took and when it went out; per
/// receiver report, how long that frame took to come back as one. For the phone's tier choice (#6396) it also counts
/// the frames skipped, the time frames waited to be written, and the most frames the encoder held at once. Pure, so it
/// tests anywhere; the capture source keeps one on its queue and takes it on its once-a-second tick.
public struct TimingWindow {
    /// Frames remembered for matching reports to (a report names one frame; older ones are forgotten).
    public static let remembered = 256

    private var sentAt: [UInt32: UInt64] = [:]
    private var order: [UInt32] = []
    private var capture: [Double] = [], encode: [Double] = [], network: [Double] = []
    private var frames = 0, bytes = 0
    private var since: UInt64?
    private var skipped = 0, blockedMicros: Int64 = 0, pending = 0

    public init() {}

    /// Frame `number` went out at `now` (µs on the helper's clock), `bytes` long, after capture and encoding took these.
    /// `blockedMicros` is how long writing it to the stream took: the link pushing back when it is more than a moment.
    /// A refinement pass (`refinement`) counts only its bytes, in what went out: it is no screen change (fps), its
    /// encode at a boosted bitrate isn't the stream's, and its round trip carries its own size, not the network's.
    public mutating func sent(number: UInt32, at now: UInt64, captureMicros: Int64, encodeMicros: Int64, bytes count: Int,
                              blockedMicros: Int64 = 0, refinement: Bool = false) {
        if since == nil { since = now }
        guard !refinement else {
            bytes += count
            return
        }
        self.blockedMicros += max(0, blockedMicros)
        if sentAt.updateValue(now, forKey: number) == nil { order.append(number) }
        while order.count > Self.remembered { sentAt[order.removeFirst()] = nil }
        capture.append(Self.ms(captureMicros))
        encode.append(Self.ms(encodeMicros))
        frames += 1
        bytes += count
    }

    /// The phone's report on frame `frame` came at `now`: half the round trip is the network's share. Counted once.
    public mutating func reported(frame: UInt32, at now: UInt64) {
        guard let at = sentAt.removeValue(forKey: frame), now >= at else { return }
        network.append(Double(now - at) / 2000)
    }

    /// A captured frame was left out: the queue was over half a second (`RateController.admit`).
    public mutating func skippedFrame() { skipped += 1 }

    /// The encoder holds `count` frames now; the most of the second goes out as `pending`.
    public mutating func encoderHolds(_ count: Int) { pending = max(pending, count) }

    /// The medians since the last take, and what went out over that time; nil when no frame went out (a still screen,
    /// or paused). With `targetKbps` (#6396) the message also carries it and the second's skipped frames, blocked time
    /// and most frames pending. Starts the next window either way.
    public mutating func take(now: UInt64, targetKbps: Int? = nil) -> TimingMessage? {
        defer {
            capture = []
            encode = []
            network = []
            frames = 0
            bytes = 0
            skipped = 0
            blockedMicros = 0
            pending = 0
            since = now
        }
        guard frames > 0, let since, now > since else { return nil }
        let seconds = Double(now - since) / 1_000_000
        var t = TimingMessage(captureMs: Self.median(capture), encodeMs: Self.median(encode), networkMs: Self.median(network),
                              kbps: Int((Double(bytes) * 8 / 1000 / seconds).rounded()),
                              fps: Int((Double(frames) / seconds).rounded()))
        if let targetKbps {
            t.targetKbps = targetKbps
            t.skipped = skipped
            t.blockedMs = Self.ms(blockedMicros)
            t.pending = pending
        }
        return t
    }

    /// Milliseconds to a tenth, never below 0 (ScreenCaptureKit hands frames over before their display time).
    private static func ms(_ micros: Int64) -> Double { (Double(max(0, micros)) / 100).rounded() / 10 }

    static func median(_ values: [Double]) -> Double? {
        guard !values.isEmpty else { return nil }
        let s = values.sorted(), mid = s.count / 2
        let m = s.count % 2 == 1 ? s[mid] : (s[mid - 1] + s[mid]) / 2
        return (m * 10).rounded() / 10
    }
}
