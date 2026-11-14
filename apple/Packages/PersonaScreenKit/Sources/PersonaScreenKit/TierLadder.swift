import Foundation

/// The live screen's quality tiers (#6396): a ladder of sizes and frame rates, best first, each with a bitrate band.
/// A tier is a scale (pixels per point of the Mac's display) and a frame rate. Every tier keeps at least one pixel per
/// point, what a non-Retina Mac shows, so text stays readable at every tier; below the lowest only the bitrate drops,
/// and idle refinement (`Refinement`) sharpens text again once the screen is still.
///
/// The band comes from pixels × frames a second, the way Moonlight sizes its default bitrate but for screen content:
/// `floorBitsPerPixel` to `ceilingBitsPerPixel` bits per pixel per frame, the ceiling clamped to the phone's cap. A tier
/// whose floor is above the cap is left out, and tiers that come out the same on a display (a 1× display at 2×, 1.5× and
/// 1×; a frame-rate cap) merge. Pure, so it tests anywhere.
public enum TierLadder {
    /// What ⋯ › Quality chose: Auto alternates between the two; Sharper keeps the pixels and gives up frame rate first;
    /// Smoother keeps the frame rate and gives up pixels first. All three still follow the link.
    public enum Bias: String, CaseIterable, Sendable {
        case auto, sharper, smoother
    }

    public struct Tier: Equatable, Sendable {
        /// The scale asked of the helper (`update.scale`): the ladder's step, which the display's pixels and the phone's
        /// long-edge cap may cut down.
        public var scale: Double
        public var fps: Int
        public var width: Int
        public var height: Int
        /// The bitrate band in kbit/s: the helper adapts inside it (`RateController.setBand`).
        public var minKbps: Int
        public var maxKbps: Int
        /// The scale the picture really has: its width over the display's in points (#7842). On a display at one pixel
        /// per point every step is 1; under the long-edge cap, less than the step. What Connection Stats shows and what
        /// Data Saver's one pixel per point is held to.
        public var pixelsPerPoint: Double

        public init(scale: Double, fps: Int, width: Int, height: Int, minKbps: Int, maxKbps: Int, pixelsPerPoint: Double? = nil) {
            self.scale = scale
            self.fps = fps
            self.width = width
            self.height = height
            self.minKbps = minKbps
            self.maxKbps = maxKbps
            self.pixelsPerPoint = pixelsPerPoint ?? scale
        }

        /// The pixels encoded a second.
        public var pixelRate: Int { width * height * fps }
    }

    /// Bits per pixel per frame at a band's floor and at its ceiling. Calibrated on the encoding bench: the floor keeps
    /// scrolling text at SSIM ≥ 0.98 against what was encoded.
    public static let floorBitsPerPixel = 0.02
    public static let ceilingBitsPerPixel = 0.08

    /// How many more bits per pixel a codec needs than HEVC for the same picture (#6558: a browser without HEVC gets
    /// H.264). 1 for both until the encoding bench measures H.264 against the floor's SSIM; spec/fixtures/screen-tiers
    /// carries the codec, so the browser's ladder follows when it changes.
    public static func bitsFactor(_ codec: String) -> Double {
        switch codec.lowercased() {
        case "h264": 1.0
        default: 1.0
        }
    }

    /// The ladder's steps for a bias, best first, before a display and the phone's caps shape them.
    public static func steps(_ bias: Bias) -> [(scale: Double, fps: Int)] {
        switch bias {
        case .auto: [(2, 60), (1.5, 60), (1.5, 30), (1, 30), (1, 15)]
        case .sharper: [(2, 60), (2, 30), (2, 15), (1.5, 15), (1, 15)]
        case .smoother: [(2, 60), (1.5, 60), (1, 60), (1, 30), (1, 15)]
        }
    }

    /// The ladder for a display `points` in size and `pixels` in pixels, under the phone's long-edge cap `maxSize`,
    /// bitrate cap `capKbps` and frame-rate cap `maxFps`, for the stream's `codec` (`bitsFactor`). Never empty: the last
    /// tier stays even when its floor is above the cap (its floor is then the cap).
    public static func make(points: (width: Int, height: Int), pixels: (width: Int, height: Int), maxSize: Int,
                            bias: Bias, capKbps: Int, maxFps: Int, codec: String = "hevc") -> [Tier] {
        let cap = CapturePlan.capKbps(capKbps)
        let factor = bitsFactor(codec)
        var tiers: [Tier] = []
        for step in steps(bias) {
            let size = CapturePlan.size(points: points, pixels: pixels, scale: step.scale, maxSize: maxSize)
            let fps = CapturePlan.fps(min(step.fps, maxFps))
            if tiers.contains(where: { $0.width == size.width && $0.height == size.height && $0.fps == fps }) { continue }
            let perSecond = Double(size.width * size.height * fps)
            let floor = max(CapturePlan.minKbps, Int((perSecond * floorBitsPerPixel * factor / 1000).rounded()))
            let ceiling = min(cap, max(floor, Int((perSecond * ceilingBitsPerPixel * factor / 1000).rounded())))
            let real = points.width > 0 ? Double(size.width) / Double(points.width) : step.scale
            tiers.append(Tier(scale: step.scale, fps: fps, width: size.width, height: size.height, minKbps: floor,
                              maxKbps: ceiling, pixelsPerPoint: real))
        }
        // A tier the phone's cap can't carry at its floor is left out; the lowest stays, its floor the helper's own.
        if var last = tiers.popLast() {
            tiers.removeAll { $0.minKbps > cap }
            last.minKbps = CapturePlan.minKbps
            last.maxKbps = max(last.maxKbps, last.minKbps)
            tiers.append(last)
        }
        return tiers
    }
}
