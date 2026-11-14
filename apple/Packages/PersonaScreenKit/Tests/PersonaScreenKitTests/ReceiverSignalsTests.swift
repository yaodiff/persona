import PersonaScreenKit
import XCTest

/// What the phone measures of the stream for the tiers (#6396): queueing above the window's floor (any clock offset),
/// the rate that arrived, bursts, late frames, decoding and showing.
final class ReceiverSignalsTests: XCTestCase {
    private let offset: UInt64 = 9_000_000_000

    func testQueueingIsAboveTheFloorWhateverTheClocks() {
        var r = ReceiverSignals()
        for i in 0..<60 { // a second at 60 fps, the last five frames queued 100 ms more
            let capture = UInt64(i) * 16_667
            let queue: UInt64 = i >= 55 ? 100_000 : 0
            r.frame(captureMicros: capture, arrivalMicros: capture + 20_000 + queue + offset, firstByteMicros: nil, bytes: 10_000,
                    refinement: false, fps: 60)
        }
        let s = r.take(now: 59 * 16_667 + 120_000 + offset)
        XCTAssertEqual(s.queueP95Ms, 100)
        XCTAssertEqual(s.queueP50Ms, 0, "most of the last half second had no queue")
        XCTAssertTrue(s.queueRising)
        XCTAssertEqual(s.lateShare ?? 0, 5.0 / 24, accuracy: 0.001, "5 of 24: 100 ms is over two frame intervals at 60 fps")
        XCTAssertFalse(s.queueFalling)
    }

    func testTheRateThatArrived() {
        var r = ReceiverSignals()
        for i in 0..<20 { // 25 KB every 50 ms: 4 Mbit/s
            let at = UInt64(i) * 50_000
            r.frame(captureMicros: at, arrivalMicros: at + 30_000 + offset, firstByteMicros: nil, bytes: 25_000, refinement: false, fps: 20)
        }
        let s = r.take(now: 19 * 50_000 + 30_000 + offset)
        XCTAssertEqual(s.deliveredKbps ?? 0, 4_000, accuracy: 1)
        XCTAssertEqual(s.frames, 11)
    }

    /// A busy link's rate is its capacity: frames queued behind one another arrive one transfer apart, and the quiet
    /// stretch after (the helper skipping) doesn't dilute it.
    func testABusyLinksRateIsItsCapacity() {
        var r = ReceiverSignals()
        r.frame(captureMicros: 0, arrivalMicros: 10_000 + offset, firstByteMicros: nil, bytes: 1_000, refinement: false, fps: 60)
        var arrival: UInt64 = 1_000_000
        for i in 0..<10 { // 30 KB frames every 16.7 ms into a 3 Mbit/s link: each 80 ms after the last
            arrival += 80_000
            r.frame(captureMicros: 900_000 + UInt64(i) * 16_667, arrivalMicros: arrival + offset, firstByteMicros: nil, bytes: 30_000,
                    refinement: false, fps: 60)
        }
        // Then one frame a quarter second, no queue: the helper skipping while it drains.
        for i in 1...3 {
            let capture = arrival + UInt64(i) * 250_000
            r.frame(captureMicros: capture - 10_000, arrivalMicros: capture + offset, firstByteMicros: nil, bytes: 2_000,
                    refinement: false, fps: 60)
        }
        let s = r.take(now: arrival + 760_000 + offset)
        XCTAssertEqual(s.deliveredKbps ?? 0, 3_000, accuracy: 1)
    }

    /// A backlog read three frames at a time (#7842): every frame's bytes count, so the rate is the link's.
    func testFramesReadTogetherAllCount() {
        var r = ReceiverSignals()
        r.frame(captureMicros: 0, arrivalMicros: 10_000 + offset, firstByteMicros: nil, bytes: 1_000, refinement: false, fps: 60)
        var arrival: UInt64 = 1_000_000
        for read in 0..<6 { // three 30 KB frames per read, a read every 240 ms: 3 Mbit/s
            arrival += 240_000
            for i in 0..<3 {
                r.frame(captureMicros: 900_000 + UInt64(read * 3 + i) * 16_667, arrivalMicros: arrival + offset, firstByteMicros: nil,
                        bytes: 30_000, refinement: false, fps: 60)
            }
        }
        XCTAssertEqual(r.take(now: arrival + offset).deliveredKbps ?? 0, 3_000, accuracy: 1)
    }

    /// A keyframe's wake (#7842): it and the frames queued behind it are left out of the p95 that says whether the
    /// link is calm, but not out of the p50 or the bursts; frames after it count again.
    func testAKeyframesWakeIsNotABusyLink() {
        var r = ReceiverSignals()
        var capture: UInt64 = 1_000_000
        func frame(_ delayMs: UInt64, bytes: Int = 4_000, firstByte: UInt64? = nil, keyframe: Bool = false) {
            r.frame(captureMicros: capture, arrivalMicros: capture + delayMs * 1000 + offset, firstByteMicros: firstByte.map { $0 + offset },
                    bytes: bytes, refinement: false, keyframe: keyframe, fps: 30)
            capture += 33_333
        }
        for _ in 0..<5 { frame(10) }
        let keyAt = capture + 130_000
        frame(130, bytes: 200_000, firstByte: keyAt - 80_000, keyframe: true) // 80 ms to come in: 20 Mbit/s
        frame(100)
        frame(70)
        frame(10)
        var s = r.take(now: capture + offset)
        XCTAssertEqual(s.queueP95Ms ?? -1, 0, accuracy: 0.01, "the wake is left out")
        XCTAssertEqual(s.burstKbps, [20_000])
        for _ in 0..<12 { frame(10) }
        frame(90)
        s = r.take(now: capture + offset)
        XCTAssertEqual(s.queueP95Ms ?? -1, 80, accuracy: 0.01, "a late frame after the wake counts")
    }

    func testRefinementIsABurstButNoQueue() {
        var r = ReceiverSignals()
        r.frame(captureMicros: 0, arrivalMicros: 10_000 + offset, firstByteMicros: nil, bytes: 1_000, refinement: false, fps: 60)
        // A 100 KB pass that took 80 ms to come in: 10 Mbit/s, and 400 ms above the floor.
        r.frame(captureMicros: 1_000_000, arrivalMicros: 1_410_000 + offset, firstByteMicros: 1_330_000 + offset, bytes: 100_000,
                refinement: true, fps: 60)
        let s = r.take(now: 1_420_000 + offset)
        XCTAssertEqual(s.burstKbps, [10_000])
        XCTAssertNil(s.queueP50Ms, "no moving frame in the last half second")
        XCTAssertTrue(r.take(now: 1_430_000 + offset).burstKbps.isEmpty, "a burst is handed over once")
    }

    func testSmallOrSingleReadFramesSayNothingOfBursts() {
        var r = ReceiverSignals()
        r.frame(captureMicros: 0, arrivalMicros: 50_000 + offset, firstByteMicros: 10_000 + offset, bytes: 20_000, refinement: false, fps: 60)
        r.frame(captureMicros: 0, arrivalMicros: 60_000 + offset, firstByteMicros: nil, bytes: 90_000, refinement: false, fps: 60)
        r.frame(captureMicros: 0, arrivalMicros: 70_000 + offset, firstByteMicros: 69_000 + offset, bytes: 90_000, refinement: false, fps: 60)
        XCTAssertTrue(r.take(now: 80_000 + offset).burstKbps.isEmpty)
    }

    func testDecodingAndShowing() {
        var r = ReceiverSignals()
        for i in 0..<20 {
            r.decoded(ms: i >= 18 ? 40 : 4, at: UInt64(i) * 50_000)
            r.shown(replaced: i % 2 == 0, at: UInt64(i) * 50_000)
        }
        let s = r.take(now: 1_000_000)
        XCTAssertEqual(s.decodeP95Ms, 40)
        XCTAssertEqual(s.replacedShare, 0.5)
    }

    func testANewRouteStartsTheFloorOver() {
        var r = ReceiverSignals()
        r.frame(captureMicros: 0, arrivalMicros: 5_000 + offset, firstByteMicros: nil, bytes: 1_000, refinement: false, fps: 60)
        r.pathChanged()
        r.frame(captureMicros: 20_000, arrivalMicros: 20_000 + 205_000 + offset, firstByteMicros: nil, bytes: 1_000, refinement: false, fps: 60)
        XCTAssertEqual(r.take(now: 230_000 + offset).queueP50Ms, 0, "the new route's 200 ms longer path is its floor")
    }
}
