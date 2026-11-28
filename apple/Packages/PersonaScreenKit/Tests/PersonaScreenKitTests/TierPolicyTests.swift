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

    private func signals(queue: Double, p95: Double? = nil, last: Double? = nil, delivered: Double? = nil, rising: Bool = false,
                         frames: Int = 30, bursts: [Double] = [], decode: Double? = 4) -> TierSignals {
        var s = TierSignals()
        s.frames = frames
        s.queueP50Ms = queue
        s.queueLastMs = last ?? queue
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

    /// On a display at one pixel per point the steps merge, so the ladder is shorter; the route still starts on its
    /// step's picture: the relay at 60 fps as on a Retina Mac, not a tier lower (the browser, 2026-11-15).
    func testAOnePixelPerPointDisplayStartsOnItsRoutesStep() {
        let plain = TierPolicy.Display(points: [1920, 1080], pixels: [1920, 1080], maxSize: 2868)
        func start(_ link: LinkStats) -> Int {
            let p = TierPolicy(display: plain, bias: .auto, capKbps: 12_000, maxFps: 60, link: link, now: 0)
            return p.tier.fps
        }
        XCTAssertEqual(start(LinkStats(kind: .lan, via: .wifi)), 60)
        XCTAssertEqual(start(LinkStats(kind: .relay, via: .wifi)), 60, "1.5×60 is 2×60's picture here")
        XCTAssertEqual(start(LinkStats(kind: .relay, via: .cellular)), 30, "1.5×30")
    }

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
        p.helper(timing(target: 12_000, sent: 12_000, skipped: 4))
        XCTAssertNil(p.tick(signals(queue: 200), now: 1))
        XCTAssertEqual(p.tick(signals(queue: 200), now: 1.25)?.index, 1, "no rate measured: one tier down")
    }

    /// The helper skips until a report shows 150 ms or less, and says so up to a second later: a phone that sees the queue
    /// under that, or a backlog that came in all at once (its newest frame barely waited), has nothing to step down for
    /// (2026-11-15: a stall that was over stepped a light screen down to 15 fps).
    func testABacklogThatIsGoneStepsNothingDown() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        p.helper(timing(target: 12_000, sent: 12_000, skipped: 4))
        XCTAssertNil(p.tick(signals(queue: 100), now: 1))
        XCTAssertNil(p.tick(signals(queue: 100), now: 1.25), "skipped, but the queue is under 150 ms")
        XCTAssertNil(p.tick(signals(queue: 500, last: 3, delivered: 1_000), now: 1.5))
        XCTAssertNil(p.tick(signals(queue: 500, last: 3, delivered: 1_000), now: 1.75), "a backlog that came in at once")
        p.helper(timing(target: 2_500, sent: 900, skipped: 0)) // the helper at the band's floor
        XCTAssertNil(p.tick(signals(queue: 200, last: 2, delivered: 1_000), now: 2.25), "nor at the band's floor")
        XCTAssertEqual(p.tick(signals(queue: 200, last: 180, delivered: 1_000), now: 2.5)?.reason, .linkSlowed, "one still there")
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
        p.helper(timing(target: 12_000, sent: 4_000)) // the screen using a third of the helper's ceiling, not all of it
        var now = 0.25
        while now < 30 {
            if Int(now * 4) % 4 == 0 { p.helper(timing(target: 12_000, sent: 4_000)) } // once a second, as the helper does
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
        p.helper(timing(target: 12_000, sent: 11_500)) // a fresh second of motion: proof is used once (#7842)
        var up: Double?
        let start = now
        while up == nil, now < start + 40 {
            if p.tick(signals(queue: 5), now: now) != nil { up = now - start }
            now += 0.25
        }
        XCTAssertEqual(up, 16, "16 s instead of 8 before trying 2×60 again")
    }

    /// Light content (2026-11-15): a screen sending at most half the next tier's floor never fills the bitrate, and on a
    /// fast link its keyframes come in too quickly to measure; after twice the window it steps up without proof.
    func testLightContentStepsUpAfterTwiceTheWindow() {
        var p = policy(LinkStats(kind: .relay, via: .wifi)) // 1.5×60; 2×60's floor is 6,408 kbit/s
        var now = 10.25 // past the first seconds' short window: 8 s, light content steps up after 16
        var up: Double?
        let start = now
        while up == nil, now < start + 40 {
            if Int(now * 4) % 4 == 1 { p.helper(timing(target: 12_000, sent: 600)) }
            if p.tick(signals(queue: 5), now: now) != nil { up = now - start }
            now += 0.25
        }
        XCTAssertEqual(up, 16)
        XCTAssertEqual(p.index, 0)
        XCTAssertEqual(p.last.targetKbps, p.ladder[0].minKbps, "from the new tier's floor")
        var still = policy(LinkStats(kind: .relay, via: .wifi))
        for i in 0 ..< 160 { _ = still.tick(signals(queue: 0, frames: 0), now: 10.25 + Double(i) / 4) }
        XCTAssertEqual(still.index, 1, "a still screen (no timing) shows nothing, light or not")
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
        var p = policy(LinkStats(kind: .relay, via: .wifi)) // 1.5×60
        p.helper(timing(target: 4_500, sent: 4_500))
        let sharper = p.setBias(.sharper, now: 1)
        XCTAssertEqual(sharper.map { label($0.tier) }, "2868x1862@30", "Sharper keeps the pixels at 4.5 Mbit/s")
        XCTAssertEqual(sharper?.reason, .choice)
        XCTAssertEqual(sharper?.targetKbps, 4_500)
        XCTAssertNil(p.setBias(.sharper, now: 2))
        let smoother = p.setBias(.smoother, now: 3)
        XCTAssertEqual(smoother.map { label($0.tier) }, "1512x982@60", "never richer in pixels a second than before")
    }

    /// A still screen sends no `timing`, so the helper's last word can be far under the tier's floor: a new bias
    /// starts from the floor, which the link carries, and Smoother keeps 60 fps (#7842: it fell to 15 fps).
    func testANewBiasOnAStillScreenKeepsTheFrameRate() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        p.helper(timing(target: 300, sent: 150))
        let smoother = p.setBias(.smoother, now: 2)
        XCTAssertEqual(smoother.map { label($0.tier) }, "2868x1862@60")
        XCTAssertEqual(smoother?.targetKbps, p.ladder[0].minKbps)
    }

    /// Skipped frames count until the next `timing` is due: a still screen after them steps down once, not once a
    /// second (#7842).
    func testSkippedFramesThenAStillScreenStepDownOnce() {
        var p = policy(LinkStats(kind: .lan, via: .wifi))
        p.helper(timing(target: 9_000, sent: 9_000, skipped: 2))
        var changes = 0
        var now = 0.25
        while now < 6 {
            if p.tick(signals(queue: now <= 0.5 ? 200 : 5, frames: now <= 0.5 ? 30 : 0), now: now) != nil { changes += 1 }
            now += 0.25
        }
        XCTAssertEqual(changes, 1)
        XCTAssertEqual(p.index, 1)
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

    func testExtremeValuesAndNonFiniteInputsDoNotTrapOrPoison() {
        // Huge finite bursts clamp instead of trapping Int(b / 1.5); non-finite bursts are ignored.
        var burst = policy(LinkStats(kind: .relay, via: .wifi))
        XCTAssertNil(burst.tick(signals(queue: 5, bursts: [.infinity, .nan, .infinity]), now: 0.25))
        XCTAssertNil(burst.tick(signals(queue: 5, bursts: [1e30, 1e30, 1e30]), now: 0.5))
        let steppedUp = burst.tick(signals(queue: 5), now: 3.5)
        XCTAssertEqual(steppedUp?.index, 0)
        XCTAssertEqual(steppedUp?.targetKbps, burst.ladder[0].maxKbps)

        // Decode-behind step-down with non-finite or extreme deliveredKbps never traps Int(0.85 * delivered).
        for (delivered, expectMax) in [(.nan, false), (.infinity, false), (-.infinity, false), (1e30, true)] {
            var behind = policy(LinkStats(kind: .lan, via: .wifi))
            XCTAssertNil(behind.tick(signals(queue: 5, delivered: delivered, decode: 30), now: 1))
            let down = behind.tick(signals(queue: 5, delivered: delivered, decode: 30), now: 3)
            XCTAssertEqual(down?.reason, .phoneBehind)
            XCTAssertEqual(down?.targetKbps, expectMax ? down?.tier.maxKbps : down?.tier.minKbps)
        }

        // Link-slowed step-down with negative-infinity or NaN deliveredKbps treats rate as unknown instead of trapping or stalling.
        for badDelivered in [-Double.infinity, .nan] {
            var slow = policy(LinkStats(kind: .lan, via: .wifi))
            XCTAssertNil(slow.tick(signals(queue: 500, delivered: badDelivered), now: 1))
            let down = slow.tick(signals(queue: 500, delivered: badDelivered), now: 1.25)
            XCTAssertEqual(down?.reason, .linkSlowed)
            XCTAssertEqual(down?.index, 1)
            XCTAssertEqual(down?.targetKbps, slow.ladder[1].minKbps)
        }

        // Non-finite `now` is ignored in tick without poisoning subsequent decisions.
        var clock = policy(LinkStats(kind: .lan, via: .wifi))
        XCTAssertNil(clock.tick(signals(queue: 500, delivered: 3_000), now: .nan))
        XCTAssertNil(clock.tick(signals(queue: 500, delivered: 3_000), now: .infinity))
        XCTAssertNil(clock.tick(signals(queue: 500, delivered: 3_000), now: 1))
        XCTAssertEqual(clock.tick(signals(queue: 500, delivered: 3_000), now: 1.25)?.reason, .linkSlowed)

        // Huge display dimensions never overflow pixelRate inside TierPolicy.index(rung:...).
        let huge = TierPolicy.Display(points: [Int.max, Int.max], pixels: [Int.max, Int.max], maxSize: 0)
        let hugePolicy = TierPolicy(display: huge, bias: .auto, capKbps: 12_000, maxFps: 60,
                                    link: LinkStats(kind: .relay, via: .wifi), now: 0)
        XCTAssertFalse(hugePolicy.ladder.isEmpty)
        XCTAssertLessThan(hugePolicy.index, hugePolicy.ladder.count)

        // Negative minRttMs on Tailscale falls back to finite non-negative srttMs instead of picking rung 0.
        let badRtt = LinkStats(kind: .tailscale, via: .wifi, srttMs: 60, minRttMs: -5)
        XCTAssertEqual(policy(badRtt).index, 1)

        // Non-finite or negative queueP95Ms breaks calm window and never steps up.
        for badP95 in [Double.nan, .infinity, -.infinity, -1] {
            var p = policy(LinkStats(kind: .relay, via: .wifi))
            p.helper(timing(target: 12_000, sent: 11_500))
            XCTAssertNil(p.tick(signals(queue: 5, p95: 5), now: 0.25))
            XCTAssertNil(p.tick(signals(queue: 5, p95: badP95), now: 3.5), "bad queueP95Ms \(badP95) must not step up")
            XCTAssertEqual(p.index, 1)
        }

        // Non-finite decodeP95Ms / replacedShare do not trigger false phoneBehind step-downs.
        var badDecode = policy(LinkStats(kind: .lan, via: .wifi))
        var infDecode = signals(queue: 5, decode: .infinity)
        infDecode.replacedShare = .infinity
        XCTAssertNil(badDecode.tick(infDecode, now: 1))
        XCTAssertNil(badDecode.tick(infDecode, now: 4))
        XCTAssertEqual(badDecode.index, 0)

        // Non-finite maxScale normalizes to nil in init & setBudget (idempotent); <= 0 maxScale stays non-empty & idempotent.
        for badScale in [Double.nan, .infinity, -.infinity] {
            var p = TierPolicy(display: display, bias: .auto, capKbps: 12_000, maxFps: 60, maxScale: badScale,
                               link: LinkStats(kind: .lan, via: .wifi), now: 0)
            XCTAssertNil(p.maxScale)
            XCTAssertNil(p.setBudget(capKbps: 12_000, maxFps: 60, maxScale: badScale, now: 1))
            XCTAssertNil(p.setBudget(capKbps: 12_000, maxFps: 60, maxScale: nil, now: 2))
        }
        for tinyScale in [0.0, -1.0] {
            var p = TierPolicy(display: display, bias: .auto, capKbps: 12_000, maxFps: 60, maxScale: tinyScale,
                               link: LinkStats(kind: .lan, via: .wifi), now: 0)
            XCTAssertEqual(p.ladder.count, 1)
            XCTAssertNil(p.setBudget(capKbps: 12_000, maxFps: 60, maxScale: tinyScale, now: 1))
        }

        // Non-finite timestamps in init, path, setBias, and setBudget preserve finite internal clock state.
        for badNow in [-Double.infinity, .nan, .infinity] {
            var p = TierPolicy(display: display, bias: .auto, capKbps: 12_000, maxFps: 60,
                               link: LinkStats(kind: .lan, via: .wifi), now: badNow)
            let relay = PathChange(from: LinkStats(kind: .lan, via: .wifi), to: LinkStats(kind: .relay, via: .cellular), reason: .lost)
            XCTAssertNotNil(p.path(relay, now: badNow))
            XCTAssertNotNil(p.setBias(.sharper, now: badNow))
            XCTAssertNotNil(p.setBudget(capKbps: 6_000, maxFps: 30, now: badNow))
            // Subsequent finite ticks still step up cleanly once calm + proven.
            p.helper(timing(target: 5_000, sent: 5_000))
            XCTAssertNil(p.tick(signals(queue: 5), now: 0.25))
            var climbed = false
            for step in 1 ... 40 {
                if p.tick(signals(queue: 5), now: 0.25 + Double(step) * 0.25) != nil { climbed = true; break }
            }
            XCTAssertTrue(climbed, "non-finite now \(badNow) must not poison tierSince/shortUntil")
        }
    }
}
