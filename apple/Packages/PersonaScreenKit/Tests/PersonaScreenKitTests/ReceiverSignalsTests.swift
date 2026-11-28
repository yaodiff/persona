@testable import PersonaScreenKit
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

    func testBackwardArrivalTimestampDoesNotUnderflowAndRecoversAfterPrune() {
        var r = ReceiverSignals()
        // Three normal frames followed by a backward arrival timestamp must not trap in `take(now:)`,
        // nor wrap `arrivalMicros &- baseline[0].at` and evict the valid delay floor.
        for i in 0..<3 {
            let at = 1_000_000 + UInt64(i) * 50_000
            r.frame(captureMicros: at, arrivalMicros: at + offset, firstByteMicros: nil, bytes: 10_000, refinement: false, fps: 20)
        }
        r.frame(captureMicros: 850_000, arrivalMicros: 900_000 + offset, firstByteMicros: nil, bytes: 10_000, refinement: false, fps: 20)
        let afterStepBack = r.take(now: 1_150_000 + offset)
        XCTAssertNil(afterStepBack.deliveredKbps, "last.at <= first.at returns nil without underflow")
        XCTAssertEqual(afterStepBack.queueLastMs ?? -1, 50, accuracy: 0.01, "backward arrival must not evict the delay floor")

        // Once pre-step-back entries age out (> 1 s), monotonic frames over >= 200 ms report deliveredKbps cleanly (25 KB / 100 ms = 2,000 kbps).
        for i in 0..<4 {
            let at = 2_200_000 + UInt64(i) * 100_000
            r.frame(captureMicros: at, arrivalMicros: at + offset, firstByteMicros: nil, bytes: 25_000, refinement: false, fps: 10)
        }
        let recovered = r.take(now: 2_500_000 + offset)
        XCTAssertEqual(recovered.deliveredKbps ?? 0, 2_000, accuracy: 1)
    }

    func testHighTimestampsNearUInt64MaxAndLargeClockDeltaDoNotOverflow() {
        // (a) `arrival = 1 << 63, capture = 1`: signed `Int64.min - 1` would trap without wrapping subtraction.
        var wrap = ReceiverSignals()
        let signBit: UInt64 = 1 << 63
        wrap.frame(captureMicros: 1, arrivalMicros: signBit, firstByteMicros: nil, bytes: 10_000, refinement: false, fps: 30)
        XCTAssertEqual(wrap.take(now: signBit).frames, 1)

        // (b) `baseline[0].delay == Int64.min` followed by `delay == 1000`: signed `1000 - Int64.min` would trap without `UInt64(bitPattern: delay &- baseline[0].delay)`.
        var hugeSpread = ReceiverSignals()
        hugeSpread.frame(captureMicros: 0, arrivalMicros: signBit, firstByteMicros: nil, bytes: 10_000, refinement: false, fps: 30)
        hugeSpread.frame(captureMicros: signBit, arrivalMicros: signBit + 1_000, firstByteMicros: nil, bytes: 10_000, refinement: false, fps: 30)
        let spread = hugeSpread.take(now: signBit + 2_000)
        XCTAssertEqual(spread.frames, 2)
        XCTAssertEqual(spread.queueLastMs ?? -1, Double(signBit + 1_000) / 1000, accuracy: 1e6)

        // (c) Keyframe at `UInt64.max - 1_000` (saturates `wakeUntil`) plus `prune` / `within` at `UInt64.max`.
        var r = ReceiverSignals()
        let base = UInt64.max - 500_000
        for i in 0..<4 {
            let capture = UInt64(i) * 60_000
            let arrival = base + capture
            r.frame(captureMicros: capture, arrivalMicros: arrival, firstByteMicros: nil, bytes: 15_000, refinement: false, fps: 30)
            r.decoded(ms: 5, at: arrival)
            r.shown(replaced: false, at: arrival)
        }
        let nearMax = UInt64.max - 1_000
        r.frame(captureMicros: nearMax - base, arrivalMicros: nearMax, firstByteMicros: nearMax - 10_000,
                bytes: 40_000, refinement: false, keyframe: true, fps: 30)
        let s = r.take(now: UInt64.max)
        XCTAssertEqual(s.frames, 5)
        XCTAssertEqual(s.queueP50Ms ?? -1, 0, accuracy: 0.01)
        XCTAssertEqual(s.decodeP95Ms, 5)
        XCTAssertEqual(s.replacedShare, 0)
        XCTAssertFalse(s.burstKbps.isEmpty)
    }

    func testNonFiniteDecodeNegativeBytesAndUnboundedBurstsAreGuarded() {
        var r = ReceiverSignals()
        r.decoded(ms: 4, at: 100_000 + offset)
        r.decoded(ms: .nan, at: 150_000 + offset)
        r.decoded(ms: .infinity, at: 200_000 + offset)
        r.decoded(ms: -.infinity, at: 250_000 + offset)
        r.decoded(ms: -10, at: 300_000 + offset)
        r.decoded(ms: 20, at: 350_000 + offset)

        // Negative bytes must not turn deliveredKbps negative.
        for i in 0..<6 {
            let at = 100_000 + UInt64(i) * 60_000
            r.frame(captureMicros: at, arrivalMicros: at + offset, firstByteMicros: nil, bytes: -50_000, refinement: false, fps: 30)
        }
        let s = r.take(now: 450_000 + offset)
        XCTAssertEqual(s.decodeP95Ms, 20)
        XCTAssertEqual(s.deliveredKbps ?? -1, 0, accuracy: 0.001)

        // Direct percentile/mean guards for non-finite values and out-of-range p.
        XCTAssertEqual(ReceiverSignals.percentile([.nan, 10, .infinity, 20], 0.5), 20)
        XCTAssertNil(ReceiverSignals.percentile([.nan, .infinity], 0.5))
        XCTAssertNil(ReceiverSignals.percentile([1, 2, 3], .nan))
        XCTAssertNil(ReceiverSignals.percentile([1, 2, 3], .infinity))
        XCTAssertEqual(ReceiverSignals.percentile([1, 2, 3], -1), 1)
        XCTAssertEqual(ReceiverSignals.percentile([1, 2, 3], 2), 3)
        XCTAssertEqual(ReceiverSignals.mean([.nan, 10, .infinity, 30]), 20)

        // Bursts cap at ReceiverSignals.maxBursts (dropping oldest 50 and keeping newest 256) if frame() runs without take().
        var bursty = ReceiverSignals()
        let totalBursts = ReceiverSignals.maxBursts + 50
        for i in 0..<totalBursts {
            let at = UInt64(i + 1) * 20_000
            bursty.frame(captureMicros: at, arrivalMicros: at + 10_000, firstByteMicros: at,
                         bytes: 40_000 + i * 1_000, refinement: true, fps: 30)
        }
        let burstValues = bursty.take(now: 10_000_000).burstKbps
        XCTAssertEqual(burstValues.count, ReceiverSignals.maxBursts)
        XCTAssertEqual(burstValues.first ?? -1, Double(40_000 + 50 * 1_000) * 8 * 1000 / 10_000, accuracy: 0.001)
        XCTAssertEqual(burstValues.last ?? -1, Double(40_000 + (totalBursts - 1) * 1_000) * 8 * 1000 / 10_000, accuracy: 0.001)
    }

    func testReedSolomonBoundsAndRouteEligibilityEdgeCases() throws {
        // Overflow-safe parity & dimension bounds in encode/invert/reconstruct.
        XCTAssertThrowsError(try ReedSolomon.encode([], parity: 1)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        XCTAssertThrowsError(try ReedSolomon.encode([[1, 2]], parity: Int.max)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        XCTAssertThrowsError(try ReedSolomon.encode([[1, 2]], parity: -1)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        XCTAssertThrowsError(try ReedSolomon.encode([[1, 2], [3]], parity: 1)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        let k256 = Array(repeating: [UInt8(7)], count: 256)
        XCTAssertEqual(try ReedSolomon.encode(k256, parity: 0), [])
        XCTAssertThrowsError(try ReedSolomon.encode(k256, parity: 1)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        XCTAssertEqual(ReedSolomon.inv(0), 0)
        XCTAssertThrowsError(try ReedSolomon.invert([])) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        XCTAssertThrowsError(try ReedSolomon.invert([[1, 0], [0]])) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        XCTAssertThrowsError(try ReedSolomon.invert([[1, 1], [1, 1]])) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        var emptyShards: [[UInt8]?] = [[1]]
        XCTAssertThrowsError(try ReedSolomon.reconstruct(&emptyShards, data: 0)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        var tooManyShards: [[UInt8]?] = Array(repeating: [1], count: 257)
        XCTAssertThrowsError(try ReedSolomon.reconstruct(&tooManyShards, data: 1)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        // Uniform shard-length check runs even when all k data shards are present.
        var mismatchedPresent: [[UInt8]?] = [[1, 2], [3]]
        XCTAssertThrowsError(try ReedSolomon.reconstruct(&mismatchedPresent, data: 2)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }
        var mismatchedExtraParity: [[UInt8]?] = [[1, 2], nil, [3, 4], [5]]
        XCTAssertThrowsError(try ReedSolomon.reconstruct(&mismatchedExtraParity, data: 2)) { e in
            XCTAssertEqual(e as? ReedSolomon.Failure, .badBlock)
        }

        // /0 prefix and interface-side unspecified/loopback addresses (even with /1) must NEVER match LAN candidates.
        let defaultRouteWifi = PhoneInterface(
            name: "en0",
            via: .wifi,
            addresses: [
                .init("0.0.0.0", prefix: 0)!,
                .init("192.0.2.40", prefix: 0)!,
                .init("::", prefix: 0)!,
                .init("0.0.0.0", prefix: 1)!,
                .init("127.0.0.1", prefix: 1)!,
                .init("::", prefix: 1)!
            ]
        )
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "1.2.3.4", prefix: 24), among: [defaultRouteWifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "198.51.100.1", prefix: 24), among: [defaultRouteWifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "2001:db8::1", prefix: 64), among: [defaultRouteWifi]))

        // Embedded NUL byte must not be truncated by C inet_pton; 0.0.0.0/8, loopback, and IPv4-mapped equivalents are refused.
        let thisNetV4 = [0, 0, 0, 1].map(String.init).joined(separator: ".")
        let thisNetV4Mapped = "::ffff:\(thisNetV4)"
        let wifi = PhoneInterface(
            name: "en0",
            via: .wifi,
            addresses: [
                .init("192.0.2.40", prefix: 24)!,
                .init("127.0.0.1", prefix: 8)!,
                .init("::1", prefix: 128)!,
                .init(thisNetV4, prefix: 8)!,
                .init(thisNetV4Mapped, prefix: 96)!
            ]
        )
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "192.0.2.10\0.evil.example", prefix: 24), among: [wifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "127.0.0.2", prefix: 8), among: [wifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "0.0.0.0", prefix: 24), among: [wifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: thisNetV4, prefix: 8), among: [wifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "::1", prefix: 128), among: [wifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "::ffff:127.0.0.1", prefix: 96), among: [wifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: "::ffff:0.0.0.0", prefix: 96), among: [wifi]))
        XCTAssertNil(RouteEligibility.interface(for: .init(kind: "lan", addr: thisNetV4Mapped, prefix: 96), among: [wifi]))
        XCTAssertEqual(RouteEligibility.interface(for: .init(kind: "lan", addr: "192.0.2.10", prefix: 24), among: [wifi])?.name, "en0")

        // Direct IPBytes.sameNetwork prefix boundary checks (-1, 0, 1, 7, 8, 9, 32, 33, 128, 129, Int.max).
        let v4a: [UInt8] = [192, 0, 2, 1]
        let v4b: [UInt8] = [192, 0, 2, 2]
        let v4c: [UInt8] = [193, 0, 2, 1] // differs in bit 8 (LSB of first octet)
        XCTAssertFalse(IPBytes.sameNetwork(v4a, v4b, prefix: -1))
        XCTAssertTrue(IPBytes.sameNetwork(v4a, v4c, prefix: 0))
        XCTAssertTrue(IPBytes.sameNetwork(v4a, v4c, prefix: 1))
        XCTAssertTrue(IPBytes.sameNetwork(v4a, v4c, prefix: 7))
        XCTAssertFalse(IPBytes.sameNetwork(v4a, v4c, prefix: 8))
        XCTAssertTrue(IPBytes.sameNetwork(v4a, v4b, prefix: 9))
        XCTAssertTrue(IPBytes.sameNetwork(v4a, v4a, prefix: 32))
        XCTAssertFalse(IPBytes.sameNetwork(v4a, v4b, prefix: 32))
        XCTAssertFalse(IPBytes.sameNetwork(v4a, v4a, prefix: 33))
        XCTAssertFalse(IPBytes.sameNetwork(v4a, v4a, prefix: Int.max))
        let v6a = Array(repeating: UInt8(0xaa), count: 16)
        XCTAssertTrue(IPBytes.sameNetwork(v6a, v6a, prefix: 128))
        XCTAssertFalse(IPBytes.sameNetwork(v6a, v6a, prefix: 129))
    }
}
