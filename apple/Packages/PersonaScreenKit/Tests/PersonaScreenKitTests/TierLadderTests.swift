import PersonaScreenKit
import XCTest

/// The quality tiers' ladder (#6396): sizes and frame rates per bias, bands from pixels × fps, caps and merging.
final class TierLadderTests: XCTestCase {
    /// A 14" MacBook Pro (Retina) on a large iPhone (2868-pixel long edge), as in the design.
    private let points = (width: 1512, height: 982), retina = (width: 3024, height: 1964)

    private func summary(_ tiers: [TierLadder.Tier]) -> [String] {
        tiers.map { "\($0.width)x\($0.height)@\($0.fps) \($0.minKbps)-\($0.maxKbps)" }
    }

    func testAutoOnARetinaMacOverWiFi() {
        let tiers = TierLadder.make(points: points, pixels: retina, maxSize: 2868, bias: .auto, capKbps: 12_000, maxFps: 60)
        XCTAssertEqual(summary(tiers), [
            "2868x1862@60 6408-12000",
            "2268x1472@60 4006-12000",
            "2268x1472@30 2003-8012",
            "1512x982@30 891-3563",
            "1512x982@15 200-1782",
        ])
    }

    func testSharperKeepsPixelsAndSmootherKeepsFrames() {
        let sharper = TierLadder.make(points: points, pixels: retina, maxSize: 2868, bias: .sharper, capKbps: 12_000, maxFps: 60)
        XCTAssertEqual(sharper.map(\.fps), [60, 30, 15, 15, 15])
        XCTAssertEqual(sharper.map(\.scale), [2, 2, 2, 1.5, 1])
        let smoother = TierLadder.make(points: points, pixels: retina, maxSize: 2868, bias: .smoother, capKbps: 12_000, maxFps: 60)
        XCTAssertEqual(smoother.map(\.fps), [60, 60, 60, 30, 15])
        XCTAssertEqual(smoother.map(\.scale), [2, 1.5, 1, 1, 1])
    }

    func testEveryTierKeepsAPixelPerPoint() {
        for bias in TierLadder.Bias.allCases {
            for tier in TierLadder.make(points: points, pixels: retina, maxSize: 2868, bias: bias, capKbps: 12_000, maxFps: 60) {
                XCTAssertGreaterThanOrEqual(tier.width, points.width, "\(bias) \(tier)")
                XCTAssertLessThanOrEqual(tier.minKbps, tier.maxKbps, "\(bias) \(tier)")
            }
        }
    }

    func testACellularCapLeavesOutWhatItCantCarry() {
        let tiers = TierLadder.make(points: points, pixels: retina, maxSize: 2868, bias: .auto, capKbps: 4_000, maxFps: 60)
        XCTAssertEqual(summary(tiers), [
            "2268x1472@30 2003-4000",
            "1512x982@30 891-3563",
            "1512x982@15 200-1782",
        ], "2×60 needs 6.4 and 1.5×60 4.006 Mbit/s at their floors")
        let lowData = TierLadder.make(points: points, pixels: retina, maxSize: 2868, bias: .auto, capKbps: 1_500, maxFps: 60)
        XCTAssertEqual(summary(lowData), ["1512x982@30 891-1500", "1512x982@15 200-1500"])
        let tiny = TierLadder.make(points: points, pixels: retina, maxSize: 2868, bias: .auto, capKbps: 300, maxFps: 60)
        XCTAssertEqual(summary(tiny), ["1512x982@15 200-300"], "never empty: the lowest stays")
    }

    func testAFrameRateCapMerges() {
        let tiers = TierLadder.make(points: points, pixels: retina, maxSize: 2868, bias: .auto, capKbps: 12_000, maxFps: 15)
        XCTAssertEqual(tiers.map(\.scale), [2, 1.5, 1], "Low Power Mode: 1.5×60 and 1.5×30 are both 1.5×15")
        XCTAssertEqual(tiers.map(\.fps), [15, 15, 15])
    }

    func testANonRetinaDisplayMerges() {
        let plain = (width: 1920, height: 1080)
        let tiers = TierLadder.make(points: plain, pixels: plain, maxSize: 2868, bias: .auto, capKbps: 12_000, maxFps: 60)
        XCTAssertEqual(summary(tiers), [
            "1920x1080@60 2488-9953",
            "1920x1080@30 1244-4977",
            "1920x1080@15 200-2488",
        ], "2×, 1.5× and 1× are the same size on a 1× display")
    }
}
