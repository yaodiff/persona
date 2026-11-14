import PersonaScreenKit
import XCTest

/// The phone's tier choice (#6396): where a stream starts, how it steps down (fast) and up (slow, with proof), and what a
/// route change, a new bias or a new budget does.
final class TierPolicyTests: XCTestCase {
    private let display = TierPolicy.Display(points: [1512, 982], pixels: [3024, 1964], maxSize: 2868)

    private func policy(_ link: LinkStats?, cap: Int = 12_000, fps: Int = 60, remembered: Int? = nil) -> TierPolicy {
        TierPolicy(display: display, bias: .auto, capKbps: cap, maxFps: fps, link: link, remembered: remembered, now: 0)
    }

    private func label(_ t: TierLadder.Tier) -> String { "\(t.width)x\(t.height)@\(t.fps)" }

    private func signals(queue: Double, p95: Double? = nil, delivered: Double? = nil, rising: Bool = false, frames: Int = 30,
                         bursts: [Double] = [], decode: Double? = 4) -> TierSignals {
        var s = TierSignals()
        s.frames = frames
        s.queueP50Ms = queue
        s.queueP95Ms = p95 ?? queue
        s.deliveredKbps = delivered
        s.queueRising = rising
        s.burstKbps = bursts
        s.decodeP95Ms = decode
        s.replacedShare = 0
        return s
    }

    private func timing(target: Int, sent: Int, skipped: Int = 0) -> TimingMessage {
        TimingMessage(captureMs: 0, encodeMs: 5, networkMs: 10, kbps: sent, fps: 60, targetKbps: target, skipped: skipped,
                      blockedMs: 0, pending: 0)
    }

    // MARK: Start

    func testTheRouteSetsTheStart() {
        XCTAssertEqual(policy(LinkStats(kind: .lan, via: .wifi)).index, 0, "the same network: the best tier")
        XCTAssertEqual(policy(LinkStats(kind: .tailscale, via: .wifi, minRttMs: 18)).index, 0)
        XCTAssertEqual(policy(LinkStats(kind: .tailscale, via: .cellular, minRttMs: 60)).index, 1)
        XCTAssertEqual(policy(LinkStats(kind: .relay, via: .wifi)).index, 1)
        XCTAssertEqual(label(policy(LinkStats(kind: .relay, via: .cellular)).tier), "2268x1472@30")
        XCTAssertEqual(policy(nil).index, 1, "nothing measured yet: as the relay over Wi-Fi")
        XCTAssertEqual(policy(nil, remembered: 3).index, 3, "where this computer last settled")
        let cellular = policy(LinkStats(kind: .relay, via: .cellular), cap: 4_000)
        XCTAssertEqual(label(cellular.tier), "2268x1472@30", "the cellular cap's ladder starts there too")
        XCTAssertEqual(cellular.last.reason, .start)
        XCTAssertEqual(cellular.last.targetKbps, cellular.tier.minKbps)
    }

    // MARK: Down

    func testALongQueueJumpsToWhatArrived() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        XCTAssertNil(p.tick(signals(queue: 500, delivered: 3_000), now: 1))
        let c = p.tick(signals(queue: 500, delivered: 3_000), now: 1.25)
        XCTAssertEqual(c.map { label($0.tier) }, "2268x1472@30", "the best tier whose floor fits 80% of 3 Mbit/s")
        XCTAssertEqual(c?.targetKbps, 2_550, "85% of what arrived")
        XCTAssertEqual(c?.reason, .linkSlowed)
    }

    func testSkippedFramesCountAsALongQueue() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        p.helper(timing(target: 7_000, sent: 7_000, skipped: 4))
        XCTAssertNil(p.tick(signals(queue: 100), now: 1))
        XCTAssertEqual(p.tick(signals(queue: 100), now: 1.25)?.index, 1, "no rate measured: one tier down")
    }

    func testAnExhaustedBandStepsDownToWhatArrived() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        p.helper(timing(target: 10_000, sent: 10_000))
        XCTAssertNil(p.tick(signals(queue: 200), now: 1), "the bitrate can still come down inside the band")
        p.helper(timing(target: 6_500, sent: 6_500))
        let c = p.tick(signals(queue: 200, delivered: 5_500), now: 1.25)
        XCTAssertEqual(c?.index, 1, "4,006 fits 80% of 5.5 Mbit/s")
        XCTAssertEqual(c?.targetKbps, 4_675)
        p.helper(timing(target: 4_100, sent: 4_100))
        XCTAssertNil(p.tick(signals(queue: 200), now: 1.75), "the next step down waits a second")
        var draining = signals(queue: 200)
        draining.queueFalling = true
        XCTAssertNil(p.tick(draining, now: 2.5), "and a queue that isn't draining: a backlog drains whatever the tier")
        XCTAssertEqual(p.tick(signals(queue: 200), now: 2.75)?.index, 2)
        p.helper(timing(target: 2_100, sent: 2_100))
        XCTAssertNil(p.tick(signals(queue: 280, rising: true), now: 3), "a new size's keyframe makes the queue grow a moment")
        XCTAssertEqual(p.tick(signals(queue: 520, rising: true), now: 3.25)?.index, 3, "a runaway queue: at once")
    }

    /// A backlog drains at the link's rate whatever the tier: a tier whose floor fits what arrives stays, and the helper
    /// cuts toward that floor, under the link.
    func testATierThatFitsWhatArrivesRidesOutABacklog() {
        var p = policy(LinkStats(kind: .relay, via: .wifi))
        p.helper(timing(target: 4_100, sent: 4_100, skipped: 3))
        for i in 1...12 { XCTAssertNil(p.tick(signals(queue: 700, delivered: 5_500), now: Double(i) * 0.25), "4,006 fits 80% of 5.5") }
        p.helper(timing(target: 4_100, sent: 4_100))
        XCTAssertNotNil(p.tick(signals(queue: 700, delivered: 4_000), now: 3.25), "4,006 doesn't fit 80% of 4")
    }

    func testAPhoneThatCantKeepUpGetsFewerFrames() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        XCTAssertNil(p.tick(signals(queue: 5, decode: 30), now: 1))
        XCTAssertNil(p.tick(signals(queue: 5, decode: 30), now: 2.75))
        let c = p.tick(signals(queue: 5, decode: 30), now: 3)
        XCTAssertEqual(c.map { label($0.tier) }, "2268x1472@30", "the next tier with a lower frame rate")
        XCTAssertEqual(c?.reason, .phoneBehind)
    }

    // MARK: Up

    func testUpNeedsACalmWindowAndProof() {
        var p = policy(LinkStats(kind: .relay, via: .wifi))
        p.helper(timing(target: 12_000, sent: 300)) // typing: the helper at its ceiling, the screen using little of it
        var now = 0.25
        while now < 30 {
            XCTAssertNil(p.tick(signals(queue: 5), now: now), "no proof of headroom at \(now)")
            now += 0.25
        }
        p.helper(timing(target: 12_000, sent: 11_000)) // the screen moving: the link carried 11 Mbit/s
        var up: TierPolicy.Change?
        var at = now
        while up == nil, at < now + 20 {
            up = p.tick(signals(queue: 5), now: at)
            at += 0.25
        }
        XCTAssertEqual(up?.index, 0)
        XCTAssertEqual(up?.reason, .linkSteady)
        XCTAssertEqual(up?.targetKbps, 11_000, "the new tier starts at the rate just proven")
    }

    func testTheFirstSecondsWaitLessForAStepUp() {
        var p = policy(LinkStats(kind: .relay, via: .wifi))
        p.helper(timing(target: 12_000, sent: 11_500))
        var now = 0.25, up: Double?
        while up == nil, now < 20 {
            if p.tick(signals(queue: 5), now: now) != nil { up = now }
            now += 0.25
        }
        XCTAssertEqual(up, 3.25, "3 s from the first calm tick")
    }

    func testLargeFramesProveHeadroomToo() {
        var p = policy(LinkStats(kind: .relay, via: .wifi))
        p.helper(timing(target: 12_000, sent: 200))
        XCTAssertNil(p.tick(signals(queue: 5, bursts: [20_000]), now: 0.25))
        XCTAssertNil(p.tick(signals(queue: 5, bursts: [8_000]), now: 0.5), "under 1.5× the next tier's floor: no proof")
        XCTAssertNil(p.tick(signals(queue: 5, bursts: [16_000, 18_000]), now: 0.75))
        var up: TierPolicy.Change?, now = 1.0
        while up == nil, now < 10 {
            up = p.tick(signals(queue: 5), now: now)
            now += 0.25
        }
        XCTAssertEqual(up?.index, 0, "three large frames at 1.5× the floor")
        XCTAssertEqual(up?.targetKbps, 10_666, "the slowest of them over 1.5")
    }

    func testAStepUpUndoneSoonDoublesThatTiersWindow() {
        var p = policy(LinkStats(kind: .relay, via: .wifi))
        p.helper(timing(target: 12_000, sent: 11_500))
        var now = 0.25
        while p.index != 0 { _ = p.tick(signals(queue: 5), now: now); now += 0.25 } // up at 3.25 s
        // Ten seconds later the link can't hold it.
        now += 10
        _ = p.tick(signals(queue: 600, delivered: 7_000), now: now)
        XCTAssertEqual(p.tick(signals(queue: 600, delivered: 7_000), now: now + 0.25)?.index, 1)
        now += 0.5
        var up: Double?
        let start = now
        while up == nil, now < start + 40 {
            if p.tick(signals(queue: 5), now: now) != nil { up = now - start }
            now += 0.25
        }
        XCTAssertEqual(up, 16, "16 s instead of 8 before trying 2×60 again")
    }

    func testAStillScreenNeitherProvesNorBreaksTheWindow() {
        var p = policy(LinkStats(kind: .relay, via: .wifi))
        p.helper(timing(target: 12_000, sent: 11_500))
        XCTAssertNil(p.tick(signals(queue: 5), now: 0.25))
        for i in 2...10 { XCTAssertNil(p.tick(signals(queue: 0, frames: 0), now: Double(i) * 0.25)) }
        XCTAssertNotNil(p.tick(signals(queue: 5), now: 3.25))
    }

    // MARK: Route, bias, budget

    func testASlowerRouteStepsDownAtOnce() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        let relay = PathChange(from: LinkStats(kind: .lan, via: .wifi), to: LinkStats(kind: .relay, via: .cellular), reason: .lost)
        let c = p.path(relay, now: 5)
        XCTAssertEqual(c.map { label($0.tier) }, "2268x1472@30")
        XCTAssertEqual(c?.reason, .networkChanged)
        let back = PathChange(from: relay.to, to: LinkStats(kind: .lan, via: .wifi), reason: .faster)
        XCTAssertNil(p.path(back, now: 30), "a faster route keeps the tier; the next step up waits the short window")
    }

    func testANewBiasTakesTheTierTheRateFits() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        p.helper(timing(target: 3_000, sent: 3_000))
        let sharper = p.setBias(.sharper, now: 1)
        XCTAssertEqual(sharper.map { label($0.tier) }, "2868x1862@15", "Sharper keeps the pixels at 3 Mbit/s")
        XCTAssertEqual(sharper?.reason, .choice)
        XCTAssertEqual(sharper?.targetKbps, 3_000)
        XCTAssertNil(p.setBias(.sharper, now: 2))
        let smoother = p.setBias(.smoother, now: 3)
        XCTAssertEqual(smoother.map { label($0.tier) }, "1512x982@30", "never richer in pixels a second than before")
    }

    /// Data Saver on mobile data (#6396): at most one pixel per point, so only the 1× tiers.
    func testDataSaverKeepsToOnePixelPerPoint() {
        let p = TierPolicy(display: display, bias: .auto, capKbps: 1_500, maxFps: 60, maxScale: 1,
                           link: LinkStats(kind: .relay, via: .cellular), now: 0)
        XCTAssertEqual(p.ladder.map(label), ["1512x982@30", "1512x982@15"])
        var q = policy(LinkStats(kind: .relay, via: .cellular), cap: 4_000)
        q.helper(timing(target: 2_500, sent: 2_500))
        let c = q.setBudget(capKbps: 1_500, maxFps: 60, maxScale: 1, now: 1)
        XCTAssertEqual(c.map { label($0.tier) }, "1512x982@30", "choosing Data Saver moves the stream at once")
        XCTAssertEqual(c?.targetKbps, 1_500)
        XCTAssertEqual(q.setBudget(capKbps: 4_000, maxFps: 60, maxScale: nil, now: 2)?.reason, .networkChanged)
    }

    func testAFrameRateCapIsThePhoneBeingWarm() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        p.helper(timing(target: 9_000, sent: 9_000))
        let c = p.setBudget(capKbps: 12_000, maxFps: 15, now: 1)
        XCTAssertEqual(c.map { label($0.tier) }, "2868x1862@15")
        XCTAssertEqual(c?.reason, .phoneWarm)
        XCTAssertEqual(c?.targetKbps, 6_408, "inside the new band")
        XCTAssertEqual(p.setBudget(capKbps: 1_500, maxFps: 15, now: 2)?.reason, .networkChanged)
    }
}
