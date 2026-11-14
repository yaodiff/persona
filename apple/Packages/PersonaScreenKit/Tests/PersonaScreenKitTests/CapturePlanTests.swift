import PersonaScreenKit
import XCTest

/// The capture source's decisions from the phone's `start` (#3954): codec, size, frame rate, bitrate.
final class CapturePlanTests: XCTestCase {
    func testCodecIsThePhonesFirstChoiceTheHelperCanEncode() {
        XCTAssertEqual(CapturePlan.codec(offered: ["hevc", "h264"]), "hevc")
        XCTAssertEqual(CapturePlan.codec(offered: ["h264", "hevc"]), "h264")
        XCTAssertEqual(CapturePlan.codec(offered: ["av1", "HEVC"]), "hevc", "unknown codecs skipped, case ignored")
        XCTAssertEqual(CapturePlan.codec(offered: ["hevc"], available: ["h264"]), "h264", "no HEVC encoder: H.264")
    }

    func testCodecFallsBackToH264() {
        XCTAssertEqual(CapturePlan.codec(offered: []), "h264")
        XCTAssertEqual(CapturePlan.codec(offered: ["av1", "vp9"]), "h264")
    }

    func testSizeCapsTheLongEdgeAndKeepsTheShape() {
        XCTAssertTrue(CapturePlan.size(width: 3840, height: 2160, maxSize: 1920) == (1920, 1080))
        XCTAssertTrue(CapturePlan.size(width: 2160, height: 3840, maxSize: 1920) == (1080, 1920), "portrait")
        XCTAssertTrue(CapturePlan.size(width: 3456, height: 2234, maxSize: 1920) == (1920, 1240))
    }

    func testSizeNeverUpscales() {
        XCTAssertTrue(CapturePlan.size(width: 1920, height: 1080, maxSize: 3840) == (1920, 1080))
        XCTAssertTrue(CapturePlan.size(width: 1920, height: 1080, maxSize: 0) == (1920, 1080), "0: no cap")
        XCTAssertTrue(CapturePlan.size(width: 1920, height: 1080, maxSize: -1) == (1920, 1080))
    }

    func testSizeIsEvenAndAtLeastTwo() {
        let odd = CapturePlan.size(width: 1001, height: 777, maxSize: 0)
        XCTAssertTrue(odd == (1000, 776), "\(odd)")
        let scaled = CapturePlan.size(width: 1366, height: 768, maxSize: 1001)
        XCTAssertEqual(scaled.width % 2, 0)
        XCTAssertEqual(scaled.height % 2, 0)
        XCTAssertLessThanOrEqual(scaled.width, 1001)
        XCTAssertTrue(CapturePlan.size(width: 0, height: 0, maxSize: 100) == (2, 2), "no display size")
        XCTAssertTrue(CapturePlan.size(width: 4000, height: 1, maxSize: 400) == (400, 2), "a sliver stays 2 high")
    }

    /// Quality tiers (#6396): a scale is pixels per point of the display, never past its pixels, then the phone's cap.
    func testSizeFromAScale() {
        let points = (width: 1512, height: 982), retina = (width: 3024, height: 1964)
        XCTAssertTrue(CapturePlan.size(points: points, pixels: retina, scale: 2, maxSize: 0) == (3024, 1964), "native pixels")
        XCTAssertTrue(CapturePlan.size(points: points, pixels: retina, scale: 2, maxSize: 2868) == (2868, 1862), "the phone's cap")
        XCTAssertTrue(CapturePlan.size(points: points, pixels: retina, scale: 1.5, maxSize: 2868) == (2268, 1472))
        XCTAssertTrue(CapturePlan.size(points: points, pixels: retina, scale: 1, maxSize: 2868) == (1512, 982))
        XCTAssertTrue(CapturePlan.size(points: points, pixels: retina, scale: 3, maxSize: 0) == (3024, 1964), "never past the pixels")
        XCTAssertTrue(CapturePlan.size(points: points, pixels: retina, scale: nil, maxSize: 1920) == (1920, 1246),
                      "no scale: the pixels, as before")
        let plain = (width: 1920, height: 1080)
        XCTAssertTrue(CapturePlan.size(points: plain, pixels: plain, scale: 2, maxSize: 2868) == (1920, 1080), "a 1× display")
        XCTAssertTrue(CapturePlan.size(points: plain, pixels: plain, scale: 0.01, maxSize: 0) == (480, 270), "clamped to 0.25")
        XCTAssertTrue(CapturePlan.size(points: (0, 0), pixels: plain, scale: 1, maxSize: 0) == (1920, 1080), "no points: pixels")
    }

    func testBandFloor() {
        XCTAssertEqual(CapturePlan.floorKbps(2_000, cap: 8_000), 2_000)
        XCTAssertEqual(CapturePlan.floorKbps(50, cap: 8_000), CapturePlan.minKbps, "never below the helper's floor")
        XCTAssertEqual(CapturePlan.floorKbps(9_000, cap: 8_000), 8_000, "never above the ceiling")
    }

    func testFrameRateStaysWithinOneToSixty() {
        XCTAssertEqual(CapturePlan.fps(60), 60)
        XCTAssertEqual(CapturePlan.fps(120), 60)
        XCTAssertEqual(CapturePlan.fps(0), 1)
        XCTAssertEqual(CapturePlan.fps(15), 15)
    }

    func testBitrateCapAndStart() {
        XCTAssertEqual(CapturePlan.capKbps(12_000), 12_000)
        XCTAssertEqual(CapturePlan.capKbps(50), CapturePlan.minKbps, "a cap below the floor is the floor")
        XCTAssertEqual(CapturePlan.capKbps(0), CapturePlan.maxKbps, "0: no cap from the phone")
        XCTAssertEqual(CapturePlan.capKbps(999_999), CapturePlan.maxKbps)
        XCTAssertEqual(CapturePlan.initialKbps(cap: 12_000), CapturePlan.startKbps, "starts below a high cap")
        XCTAssertEqual(CapturePlan.initialKbps(cap: 1_500), 1_500, "Low Data Mode: starts at the cap")
    }
}
