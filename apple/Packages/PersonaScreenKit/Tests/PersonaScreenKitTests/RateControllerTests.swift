import PersonaScreenKit
import XCTest

/// Bandwidth adaptation from receiver reports (#3954, docs/extensions/computer-use.md "Adaptation"): cut 30% over
/// 150 ms of queueing, skip frames over 500 ms, raise 10% after 2 s under 50 ms, never past the phone's cap.
final class RateControllerTests: XCTestCase {
    /// A report whose one-way delay is `offset` (the clocks' difference, any size) plus `queue` ms of queueing.
    private func report(at now: UInt64, queue ms: Int64, offset: Int64 = 7_000_000_000, decodingError: Bool = false) -> ReceiverReport {
        let capture = now
        let arrival = UInt64(Int64(capture) + offset + ms * 1000)
        return ReceiverReport(lastFrame: 1, captureMicros: capture, arrivalMicros: arrival, bytesSinceLast: 0,
                              flags: decodingError ? .decodingError : [])
    }

    private let second: UInt64 = 1_000_000

    func testStartsAtTheStartBitrateUnderACap() {
        XCTAssertEqual(RateController(cap: 12_000).kbps, 4_000)
        XCTAssertEqual(RateController(cap: 1_500).kbps, 1_500)
    }

    func testRaisesTenPercentAfterTwoCalmSecondsNeverPastTheCap() {
        var rate = RateController(cap: 4_500)
        var now: UInt64 = 10 * second
        var raises: [Int] = []
        for _ in 0..<(7 * 60) { // 7 s of reports at 60 fps, all calm
            now += second / 60
            if let k = rate.receive(report(at: now, queue: 5), now: now).kbps { raises.append(k) }
        }
        XCTAssertEqual(raises, [4_400, 4_500], "+10% each 2 calm seconds, then held at the cap")
        XCTAssertEqual(rate.kbps, 4_500)
    }

    func testTheClocksOffsetDoesNotCount() {
        var rate = RateController(cap: 12_000)
        var now: UInt64 = 10_000 * second // late enough that a negative offset keeps arrivals positive
        // A huge constant offset, negative even: the floor cancels it, so this is calm, not congested.
        for _ in 0..<130 {
            now += second / 60
            let decision = rate.receive(report(at: now, queue: 0, offset: -3_600_000_000), now: now)
            XCTAssertFalse((decision.kbps ?? 4_000) < 4_000, "never cut")
        }
        XCTAssertGreaterThan(rate.kbps, 4_000, "raised after 2 calm seconds")
    }

    func testCutsThirtyPercentOnceForOneCongestedMoment() {
        var rate = RateController(cap: 12_000)
        var now: UInt64 = 10 * second
        _ = rate.receive(report(at: now, queue: 0), now: now) // the floor
        var cuts: [Int] = []
        for _ in 0..<20 { // 20 reports over ~330 ms, all 200 ms queued
            now += second / 60
            if let k = rate.receive(report(at: now, queue: 200), now: now).kbps { cuts.append(k) }
        }
        XCTAssertEqual(cuts, [2_800], "one cut of 30%, held for 500 ms")
        for _ in 0..<20 { // still congested past the hold: cut again
            now += second / 60
            if let k = rate.receive(report(at: now, queue: 200), now: now).kbps { cuts.append(k) }
        }
        XCTAssertEqual(cuts, [2_800, 1_960])
    }

    func testNeverCutsBelowTheFloor() {
        var rate = RateController(cap: 300)
        var now: UInt64 = 10 * second
        _ = rate.receive(report(at: now, queue: 0), now: now)
        var kbps: [Int] = []
        for _ in 0..<40 { // 4 s of congestion, inside the 10 s window, so the floor stays the 0 ms one
            now += 100_000
            if let k = rate.receive(report(at: now, queue: 300), now: now).kbps { kbps.append(k) }
        }
        XCTAssertEqual(kbps, [210, CapturePlan.minKbps], "cut to 210, then clamped at the floor and held")
        XCTAssertEqual(rate.kbps, CapturePlan.minKbps)
    }

    func testSkipsFramesOverHalfASecondAndProbesUntilItDrains() {
        var rate = RateController(cap: 12_000)
        var now: UInt64 = 10 * second
        _ = rate.receive(report(at: now, queue: 0), now: now)
        XCTAssertTrue(rate.admit(now: now))
        now += 10_000
        XCTAssertTrue(rate.receive(report(at: now, queue: 600), now: now).skipping)

        // While skipping, one frame per 250 ms goes, so reports keep coming.
        var admitted = 0
        for _ in 0..<60 { // one second of 60 fps frames
            now += second / 60
            if rate.admit(now: now) { admitted += 1 }
        }
        XCTAssertEqual(admitted, 4, "a probe each 250 ms")

        now += 10_000
        XCTAssertTrue(rate.receive(report(at: now, queue: 300), now: now).skipping, "300 ms: still draining")
        now += 10_000
        XCTAssertFalse(rate.receive(report(at: now, queue: 100), now: now).skipping, "under 150 ms: drained")
        XCTAssertTrue(rate.admit(now: now + 1) && rate.admit(now: now + 2), "every frame goes again")
    }

    func testADecodingErrorAsksForAKeyframe() {
        var rate = RateController(cap: 12_000)
        let now: UInt64 = 10 * second
        XCTAssertFalse(rate.receive(report(at: now, queue: 0), now: now).keyframe)
        XCTAssertTrue(rate.receive(report(at: now + 1_000, queue: 0, decodingError: true), now: now + 1_000).keyframe)
    }

    func testALowerCapTakesEffectAtOnce() {
        var rate = RateController(cap: 12_000)
        XCTAssertEqual(rate.setCap(1_500), 1_500, "below the current bitrate: lowered now")
        XCTAssertEqual(rate.kbps, 1_500)
        XCTAssertNil(rate.setCap(12_000), "a higher cap is reached by raising")
        XCTAssertEqual(rate.kbps, 1_500)
    }

    func testAnOldFloorAgesOut() {
        var rate = RateController(cap: 12_000)
        let start: UInt64 = 10 * second
        _ = rate.receive(report(at: start, queue: 0), now: start) // the floor
        // 180 ms above that floor, once a second: congested, so it cuts each time, down to the minimum...
        for s in 1...10 {
            let now = start + UInt64(s) * second
            _ = rate.receive(report(at: now, queue: 180), now: now)
        }
        XCTAssertEqual(rate.kbps, CapturePlan.minKbps)
        // ...until the floor is more than 10 s old: then 180 ms is the floor itself (a path or clock that moved), and
        // the queue reads empty, so there is no cut, and after 2 calm seconds a raise.
        for s in 11...12 {
            let now = start + UInt64(s) * second
            XCTAssertNil(rate.receive(report(at: now, queue: 180), now: now).kbps, "second \(s): no cut")
        }
        let now = start + 13 * second
        XCTAssertEqual(rate.receive(report(at: now, queue: 180), now: now).kbps, 220, "raised 10% after 2 calm seconds")
    }

    // MARK: - A band from the phone (#6396)

    /// Frames captured every `interval` µs, `bytes` each, over a link that carries `kbps`: each arrives once the link
    /// has carried the ones before it. `now` is when the helper gets each report (on its own clock).
    private func overLink(count: Int, from start: UInt64, interval: UInt64 = 20_000, bytes: UInt32, kbps: UInt64)
        -> [(report: ReceiverReport, now: UInt64)] {
        let transmit = UInt64(bytes) * 8 * 1000 / kbps
        var last: UInt64 = 0
        return (0..<count).map { i in
            let capture = start + UInt64(i) * interval
            let arrival = max(capture + 5_000, last + transmit)
            last = arrival
            let report = ReceiverReport(lastFrame: UInt32(i + 1), captureMicros: capture, arrivalMicros: arrival + 7_000_000_000,
                                        bytesSinceLast: bytes, flags: [])
            return (report, arrival + 5_000)
        }
    }

    func testABandHoldsTheBitrateAndTurnsOnTiers() {
        var rate = RateController(cap: 12_000)
        XCTAssertFalse(rate.tiered)
        XCTAssertEqual(rate.setBand(min: 2_000, max: 8_000, target: 9_000), 8_000, "a target above the band: its ceiling")
        XCTAssertTrue(rate.tiered)
        XCTAssertEqual(rate.floorKbps, 2_000)
        XCTAssertEqual(rate.cap, 8_000)
        XCTAssertEqual(rate.setBand(min: nil, max: nil, target: 100), 2_000, "below it: its floor")
        XCTAssertNil(rate.setBand(min: 2_000, max: 8_000, target: nil), "the same band: no change")
        XCTAssertEqual(rate.setBand(min: 3_000, max: nil, target: nil), 3_000, "a higher floor lifts the bitrate")
        XCTAssertEqual(rate.setBand(min: nil, max: 1_000, target: nil), 1_000, "a ceiling under the floor wins")
        XCTAssertEqual(rate.floorKbps, 1_000)
    }

    func testWithABandACutGoesToWhatArrived() {
        // 8 Mbit/s of frames over a 3 Mbit/s link: the queue grows 33 ms a frame and passes 150 ms on the sixth.
        var rate = RateController(cap: 12_000)
        _ = rate.setBand(min: 1_000, max: 12_000, target: 8_000)
        var cuts: [Int] = []
        for (r, now) in overLink(count: 8, from: 10 * second, bytes: 20_000, kbps: 3_000) {
            if let k = rate.receive(r, now: now).kbps { cuts.append(k) }
        }
        XCTAssertEqual(cuts, [2_550], "85% of the 3 Mbit/s that arrived, in one step")
    }

    func testWithoutABandACutIsThirtyPercentAsBefore() {
        var rate = RateController(cap: 12_000)
        var cuts: [Int] = []
        for (r, now) in overLink(count: 8, from: 10 * second, bytes: 20_000, kbps: 3_000) {
            if let k = rate.receive(r, now: now).kbps { cuts.append(k) }
        }
        XCTAssertEqual(cuts, [2_800], "an app before #6396: 30% of the 4 Mbit/s start")
    }

    func testWithABandButTooFewReportsACutIsThirtyPercent() {
        var rate = RateController(cap: 12_000)
        _ = rate.setBand(min: 1_000, max: 12_000, target: 8_000)
        let now: UInt64 = 10 * second
        _ = rate.receive(report(at: now, queue: 0), now: now)
        XCTAssertEqual(rate.receive(report(at: now + 20_000, queue: 200), now: now + 20_000).kbps, 5_600,
                       "two reports say nothing of the link")
    }

    func testABandsCutStopsAtItsFloor() {
        var rate = RateController(cap: 12_000)
        _ = rate.setBand(min: 4_000, max: 12_000, target: 8_000)
        for (r, now) in overLink(count: 8, from: 10 * second, bytes: 20_000, kbps: 3_000) { _ = rate.receive(r, now: now) }
        XCTAssertEqual(rate.kbps, 4_000, "the phone moves to a lower tier from here")
    }

    func testWithABandTheFirstSecondsRaiseFaster() {
        var rate = RateController(cap: 12_000)
        _ = rate.setBand(min: 1_000, max: 12_000, target: 2_000)
        var now: UInt64 = 10 * second
        var raises: [Int] = []
        for _ in 0..<270 { // 4.5 s of calm 60 fps reports
            now += second / 60
            if let k = rate.receive(report(at: now, queue: 5), now: now).kbps { raises.append(k) }
        }
        XCTAssertEqual(raises, [2_500, 3_125, 3_907, 4_884], "+25% each calm second")
    }

    func testSlowStartEndsWithTheFirstCut() {
        var rate = RateController(cap: 12_000)
        _ = rate.setBand(min: 1_000, max: 12_000, target: 2_000)
        var now: UInt64 = 10 * second
        _ = rate.receive(report(at: now, queue: 0), now: now)
        now += 10_000
        XCTAssertEqual(rate.receive(report(at: now, queue: 200), now: now).kbps, 1_400)
        var raises: [Int] = []
        for _ in 0..<(3 * 60) { // 3 calm seconds
            now += second / 60
            if let k = rate.receive(report(at: now, queue: 5), now: now).kbps { raises.append(k) }
        }
        XCTAssertEqual(raises, [1_540], "back to +10% after 2 calm seconds")
    }

    func testSlowStartEndsAfterTenSeconds() {
        var rate = RateController(cap: 50_000)
        _ = rate.setBand(min: 200, max: 50_000, target: 200)
        var now: UInt64 = 10 * second
        var raises: [UInt64] = []
        for _ in 0..<(16 * 60) {
            now += second / 60
            if rate.receive(report(at: now, queue: 5), now: now).kbps != nil { raises.append(now) }
        }
        // Nine fast raises about a second apart while slow start lasts, then one every 2 calm seconds.
        let gaps = zip(raises.dropFirst(), raises).map { $0 - $1 }
        XCTAssertEqual(raises.count, 12, "\(raises)")
        XCTAssertTrue(gaps.prefix(8).allSatisfy { $0 < 1_100_000 }, "\(gaps)")
        XCTAssertTrue(gaps.suffix(3).allSatisfy { $0 >= 2_000_000 }, "\(gaps)")
    }

    func testANewPathStartsTheDelayFloorOver() {
        var moved = RateController(cap: 12_000), stayed = RateController(cap: 12_000)
        var now: UInt64 = 10 * second
        _ = moved.receive(report(at: now, queue: 0), now: now)
        _ = stayed.receive(report(at: now, queue: 0), now: now)
        moved.pathChanged()
        // The new path is 200 ms longer each way: its own floor, not queueing.
        now += 20_000
        XCTAssertNil(moved.receive(report(at: now, queue: 200), now: now).kbps)
        XCTAssertEqual(stayed.receive(report(at: now, queue: 200), now: now).kbps, 2_800, "without it: read as queueing")
    }
}
