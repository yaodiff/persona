import PersonaScreenKit
import XCTest

/// Idle refinement (#3954, `frame` flag 2): 300 ms after the screen stops changing, then every 200 ms, three passes,
/// only while the link is calm, and their reports kept out of the rate controller's numbers.
final class RefinementTests: XCTestCase {
    private let ms: UInt64 = 1_000

    func testThreePassesAfterTheScreenStops() {
        var r = Refinement()
        XCTAssertFalse(r.due(at: 0, calm: true), "nothing changed yet: nothing to refine")
        r.changed(at: 1_000 * ms)
        XCTAssertFalse(r.due(at: 1_299 * ms, calm: true))
        XCTAssertTrue(r.due(at: 1_300 * ms, calm: true))
        r.passed(at: 1_300 * ms)
        XCTAssertFalse(r.due(at: 1_499 * ms, calm: true))
        XCTAssertTrue(r.due(at: 1_500 * ms, calm: true))
        r.passed(at: 1_500 * ms)
        r.passed(at: 1_700 * ms)
        XCTAssertEqual(r.done, Refinement.passes)
        XCTAssertFalse(r.due(at: 5_000 * ms, calm: true), "three passes, then nothing until the next change")
    }

    func testAChangeStartsOverAndAQueueStops() {
        var r = Refinement()
        r.changed(at: 0)
        r.passed(at: 300 * ms)
        r.changed(at: 400 * ms) // the screen moved again
        XCTAssertFalse(r.due(at: 600 * ms, calm: true), "300 ms from the new change")
        XCTAssertTrue(r.due(at: 700 * ms, calm: true))
        XCTAssertFalse(r.due(at: 700 * ms, calm: false), "never on a link with a queue")
        r.passed(at: 700 * ms)
        r.queued()
        XCTAssertFalse(r.due(at: 2_000 * ms, calm: true), "a pass came back queued: no more this time")
        r.reset()
        XCTAssertFalse(r.due(at: 9_000 * ms, calm: true), "capture stopped: nothing to refine")
    }

    /// VideoToolbox drops whole frames while over budget (screenbench, the lowest tier): a dropped pass is tried again
    /// 200 ms later and doesn't count, up to `attempts` tries per still moment.
    func testADroppedPassIsTriedAgain() {
        var r = Refinement()
        r.changed(at: 0)
        var at: UInt64 = 300 * ms, sent = 0
        for attempt in 0..<20 where r.due(at: at, calm: true) {
            r.passed(at: at)
            if attempt < 5 { r.dropped() } else { sent += 1 }
            at += 200 * ms
        }
        XCTAssertEqual(sent, Refinement.passes, "five dropped, then three that went out")
        XCTAssertEqual(r.done, Refinement.passes)

        var never = Refinement()
        never.changed(at: 0)
        var tries = 0
        at = 300 * ms
        while never.due(at: at, calm: true), tries < 100 {
            never.passed(at: at)
            never.dropped()
            tries += 1
            at += 200 * ms
        }
        XCTAssertEqual(tries, Refinement.attempts, "a budget that never clears: it stops trying")
    }

    func testABoostedBitrateStaysInTheHelpersRange() {
        XCTAssertEqual(Refinement.kbps(target: 1_000), 4_000)
        XCTAssertEqual(Refinement.kbps(target: 20_000), CapturePlan.maxKbps)
        XCTAssertEqual(Refinement.kbps(target: 10), CapturePlan.minKbps)
    }

    // MARK: - With the rate controller

    private func report(frame: UInt32, capture: UInt64, queue: UInt64, bytes: UInt32 = 20_000) -> ReceiverReport {
        ReceiverReport(lastFrame: frame, captureMicros: capture, arrivalMicros: capture + 5_000 + queue + 3_000_000_000,
                       bytesSinceLast: bytes, flags: [])
    }

    func testARefinementReportChangesNothingButAKeyframe() {
        var rate = RateController(cap: 12_000)
        _ = rate.setBand(min: 1_000, max: 12_000, target: 3_000)
        let now: UInt64 = 10_000 * ms
        _ = rate.receive(report(frame: 1, capture: now, queue: 0), now: now)
        let big = report(frame: 2, capture: now + 400 * ms, queue: 400 * ms, bytes: 400_000)
        XCTAssertEqual(rate.queueing(of: big), 400 * 1_000, "it says how queued the pass came back")
        let d = rate.receive(big, now: now + 800 * ms, refinement: true)
        XCTAssertNil(d.kbps, "a 400 ms pass is no congestion: no cut")
        XCTAssertFalse(d.skipping)
        XCTAssertTrue(rate.calm, "and the link still reads calm")
        var bad = big
        bad.flags = .decodingError
        XCTAssertTrue(rate.receive(bad, now: now + 900 * ms, refinement: true).keyframe)
    }

    func testCalmFollowsTheLastFramesQueue() {
        var rate = RateController(cap: 12_000)
        let now: UInt64 = 10_000 * ms
        XCTAssertTrue(rate.calm, "nothing heard: nothing queued")
        _ = rate.receive(report(frame: 1, capture: now, queue: 0), now: now)
        _ = rate.receive(report(frame: 2, capture: now + 20 * ms, queue: 200 * ms), now: now + 230 * ms)
        XCTAssertFalse(rate.calm)
        _ = rate.receive(report(frame: 3, capture: now + 600 * ms, queue: 100 * ms), now: now + 710 * ms)
        XCTAssertTrue(rate.calm, "under the cut threshold")
    }

    /// A still screen over a band on a 3 Mbit/s link: the passes go out (each about four frames' worth), the target and
    /// the delivered rate the next cut would use stay as the moving frames left them.
    func testAStillMomentLeavesTheRateAlone() {
        var rate = RateController(cap: 12_000)
        _ = rate.setBand(min: 1_000, max: 12_000, target: 2_400)
        var refine = Refinement()
        var now: UInt64 = 10_000 * ms
        // One second of moving screen at 30 fps, 10 KB frames: 2.4 Mbit/s, under the link.
        for i in 0..<30 {
            let capture = now + UInt64(i) * 33 * ms
            _ = rate.receive(report(frame: UInt32(i + 1), capture: capture, queue: 0, bytes: 10_000), now: capture + 10 * ms)
            refine.changed(at: capture)
        }
        now += 1_000 * ms
        let kbps = rate.kbps, delivered = rate.deliveredKbps
        // Then still: passes at 4× the target, each 40 KB, 107 ms on the link.
        var passes = 0
        var t = now
        while t < now + 2_000 * ms {
            if refine.due(at: t, calm: rate.calm) {
                refine.passed(at: t)
                passes += 1
                let r = report(frame: UInt32(100 + passes), capture: t, queue: 107 * ms, bytes: 40_000)
                if let q = rate.queueing(of: r), q > RateController.cutAboveMicros { refine.queued() }
                _ = rate.receive(r, now: t + 120 * ms, refinement: true)
            }
            t += 100 * ms
        }
        XCTAssertEqual(passes, Refinement.passes)
        XCTAssertEqual(rate.kbps, kbps, "no cut and no raise from the passes")
        XCTAssertEqual(rate.deliveredKbps, delivered, "nor a delivered rate taken from them")
    }
}
