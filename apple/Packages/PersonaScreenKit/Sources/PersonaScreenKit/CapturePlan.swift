import Foundation

/// The decisions the capture source makes from the phone's `start` / `update` (#3954): which codec, what size, what
/// frame rate and bitrate. Kept apart from ScreenCaptureKit and VideoToolbox so they test on any platform.
public enum CapturePlan {
    /// The codecs this helper can encode, best first: HEVC Main, else H.264 High (docs/extensions/computer-use.md).
    public static let encodable = ["hevc", "h264"]

    /// What this helper can do beyond protocol version 1, said in `hello.features`. "tiers" (#6396): `start` and `update`
    /// may carry `scale`, `min_kbps` and `target_kbps`, a size change applies to the running stream, and `encoding` says
    /// what took effect.
    public static let features = ["tiers"]

    /// The scales (pixels per point) the helper takes; a request outside is clamped.
    public static let scales: ClosedRange<Double> = 0.25 ... 4

    /// The lowest and highest bitrate the helper will encode at, in kbit/s. The phone's `max_kbps` is clamped into
    /// this range, and adaptation never goes below the floor.
    public static let minKbps = 200
    public static let maxKbps = 50_000

    /// The bitrate a stream starts at before any receiver report: the phone's cap, but no more than 4 Mbit/s, so a
    /// fresh stream doesn't flood a relay before adaptation has seen how the link behaves. Adaptation raises it from
    /// there while the link stays calm (`RateController`).
    public static let startKbps = 4_000

    /// The phone's first choice that `available` can encode. With no overlap (an empty or unknown list) it is H.264,
    /// which every phone and browser decodes.
    public static func codec(offered: [String], available: [String] = encodable) -> String {
        offered.map { $0.lowercased() }.first(where: available.contains) ?? "h264"
    }

    /// The encoded size for a display `width` × `height` pixels: the long edge at most `maxSize` (0 or less: no cap),
    /// the aspect ratio kept, never larger than the display, both sides even (both codecs need it) and at least 2.
    public static func size(width: Int, height: Int, maxSize: Int) -> (width: Int, height: Int) {
        guard width > 0, height > 0 else { return (2, 2) }
        func even(_ v: Int) -> Int { max(2, v & ~1) }
        let long = max(width, height), short = min(width, height)
        guard maxSize > 0, long > maxSize else { return (even(width), even(height)) }
        // In integers, so the long edge is exactly maxSize and the short one is rounded, not floored by float error.
        let scaledShort = (short * maxSize + long / 2) / long
        return width >= height ? (even(maxSize), even(scaledShort)) : (even(scaledShort), even(maxSize))
    }

    /// The encoded size for a display `points` in size and `pixels` in pixels at `scale` pixels per point: never more
    /// than its pixels, then capped by `maxSize` like `size(width:height:maxSize:)`. No scale (a phone before #6396): the
    /// display's pixels, as before.
    public static func size(points: (width: Int, height: Int), pixels: (width: Int, height: Int), scale: Double?,
                            maxSize: Int) -> (width: Int, height: Int) {
        guard let scale, scale.isFinite, points.width > 0, points.height > 0 else {
            return size(width: pixels.width, height: pixels.height, maxSize: maxSize)
        }
        let s = min(max(scale, scales.lowerBound), scales.upperBound)
        let width = min(Int((Double(points.width) * s).rounded()), max(pixels.width, 1))
        let height = min(Int((Double(points.height) * s).rounded()), max(pixels.height, 1))
        return size(width: width, height: height, maxSize: maxSize)
    }

    /// The frame rate to capture at: the phone's, kept within 1–60.
    public static func fps(_ requested: Int) -> Int { min(max(requested, 1), 60) }

    /// The phone's bitrate cap, kept within `minKbps`–`maxKbps`. 0 or less means no cap from the phone.
    public static func capKbps(_ requested: Int) -> Int {
        requested <= 0 ? maxKbps : min(max(requested, minKbps), maxKbps)
    }

    /// The bitrate a stream starts at under the phone's cap.
    public static func initialKbps(cap: Int) -> Int { min(capKbps(cap), startKbps) }

    /// The bitrate band's floor the phone asked for (#6396), kept within `minKbps` and the ceiling `cap`.
    public static func floorKbps(_ requested: Int, cap: Int) -> Int { min(max(requested, minKbps), capKbps(cap)) }
}
