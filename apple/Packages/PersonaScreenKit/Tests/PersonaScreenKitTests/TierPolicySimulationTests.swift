import PersonaScreenKit
import XCTest

/// The quality tiers on simulated links (#6396's acceptance): settling within a few seconds, stepping down within about
/// a second of a drop, no back and forth on a steady link, and the same links against fixed tiers. `LinkSimulation`
/// runs the real `RateController`, `ReceiverSignals` and `TierPolicy`. Each test prints its numbers for the issue.
final class TierPolicySimulationTests: XCTestCase {
    private func report(_ name: String, _ r: LinkSimulation.Result, _ phases: [(String, Double, Double)]) {
        var lines = ["\(name): \(r.changes.count) changes"]
        for c in r.changes { lines.append(String(format: "  %.2f s %@ → %@ (%@)", c.at, c.from, c.to, c.reason.rawValue)) }
        for (label, from, to) in phases {
            let l = r.latency(from: from, to: to)
            let tiers = r.time(from: from, to: to).sorted { $0.value > $1.value }.map { String(format: "%@ %.0f s", $0.key, $0.value) }
            lines.append(String(format: "  %@ %.0f–%.0f s: queueing p50 %.0f ms, p95 %.0f ms, %.1f fps, %@", label, from, to, l.p50, l.p95,
                                r.fps(from: from, to: to), tiers.joined(separator: ", ")))
        }
        print(lines.joined(separator: "\n"))
    }

    func testASteadyFastLinkNeverMoves() {
        let link = LinkSimulation.Link(kbps: { _ in 40_000 }, rttMs: 4, jitterMs: 2, stats: LinkStats(kind: .lan, via: .wifi))
        let r = LinkSimulation().run(link, seconds: 600)
        report("same network, 40 Mbit/s, 10 min", r, [("all", 0, 600)])
        XCTAssertTrue(r.changes.isEmpty, "\(r.changes)")
        XCTAssertLessThan(r.latency(from: 5, to: 600).p95, 30)
    }

    func testTheRelaySettlesWithinSeconds() {
        let link = LinkSimulation.Link(kbps: { _ in 25_000 }, rttMs: 40, jitterMs: 6, stats: LinkStats(kind: .relay, via: .wifi))
        let r = LinkSimulation().run(link, seconds: 300)
        report("relay over Wi-Fi, 25 Mbit/s, 5 min", r, [("settling", 0, 15), ("settled", 15, 300)])
        XCTAssertEqual(r.changes.first?.to, "2x60")
        XCTAssertLessThan(r.changes.first?.at ?? .infinity, 8, "settled within a few seconds")
        XCTAssertEqual(r.changes(from: 15, to: 300), 0, "and stays")
    }

    func testADropIsFollowedWithinASecond() {
        // 25 Mbit/s, then 3 for a minute from 30 s, then 25 again.
        let link = LinkSimulation.Link(kbps: { $0 >= 30 && $0 < 90 ? 3_000 : 25_000 }, rttMs: 30, jitterMs: 4,
                                       stats: LinkStats(kind: .lan, via: .wifi))
        let r = LinkSimulation().run(link, seconds: 240)
        let phases = [("before", 0.0, 30.0), ("dropped", 30.0, 90.0), ("after", 90.0, 240.0)]
        report("same network, 25 → 3 → 25 Mbit/s", r, phases)
        let down = r.changes.first { $0.at >= 30 }
        XCTAssertNotNil(down)
        XCTAssertLessThan((down?.at ?? .infinity) - 30, 1.5, "down within about a second")
        XCTAssertLessThanOrEqual(r.changes(from: 35, to: 90), 1, "then it holds")
        XCTAssertLessThan(r.latency(from: 35, to: 90).p95, 250)
        XCTAssertGreaterThan(r.changes(from: 90, to: 240), 0, "and climbs back once the link does")

        // The A/B: the same link with the best tier fixed, and with the lowest fixed.
        let best = LinkSimulation(fixed: 0).run(link, seconds: 240)
        report("fixed 2×60, same link", best, phases)
        XCTAssertGreaterThan(best.latency(from: 35, to: 90).p95, 2 * r.latency(from: 35, to: 90).p95,
                             "a fixed best tier queues where Auto doesn't")
        let lowest = LinkSimulation(fixed: 4).run(link, seconds: 240)
        report("fixed 1×15, same link", lowest, phases)
    }

    func testAJitteryLinkAtATiersEdgeDoesntFlap() {
        // About 7 Mbit/s, each 100 ms anywhere from 5 to 9, through the relay: around 1.5×60's floor.
        var seed: UInt64 = 42
        var schedule: [Double] = []
        for _ in 0..<6_000 {
            seed = seed &* 6364136223846793005 &+ 1442695040888963407
            schedule.append(5_000 + Double(seed >> 11) / Double(1 << 53) * 4_000)
        }
        let link = LinkSimulation.Link(kbps: { schedule[min(schedule.count - 1, Int($0 * 10))] }, rttMs: 35, jitterMs: 15,
                                       stats: LinkStats(kind: .relay, via: .wifi))
        let r = LinkSimulation().run(link, seconds: 600)
        report("relay, 5–9 Mbit/s jittering, 10 min", r, [("settling", 0, 30), ("settled", 30, 600)])
        XCTAssertLessThanOrEqual(r.changes(from: 30, to: 600), 2, "\(r.changes)")
    }

    func testCellularStaysUnderItsCap() {
        let link = LinkSimulation.Link(kbps: { _ in 6_000 }, rttMs: 70, jitterMs: 12, stats: LinkStats(kind: .relay, via: .cellular))
        var sim = LinkSimulation()
        sim.capKbps = 4_000
        let r = sim.run(link, seconds: 120)
        report("relay over cellular, 6 Mbit/s link, 4 Mbit/s cap", r, [("all", 0, 120)])
        XCTAssertTrue(r.changes.isEmpty, "1.5×30 is the cap's best: \(r.changes)")
        XCTAssertLessThan(r.latency(from: 5, to: 120).p95, 60)
    }

    /// #7842: a 1080p display at one pixel per point, scrolling text (light content) and the helper's keyframe every
    /// 10 s, as on a real Mac; the phone reads several frames at once when `batched`.
    private func lightScreen(batched: Bool) -> LinkSimulation {
        var sim = LinkSimulation()
        sim.display = TierPolicy.Display(points: [1920, 1080], pixels: [1920, 1080], maxSize: 2868)
        sim.content = 0.011
        sim.keyframeEvery = 10
        sim.keyframeBits = 1
        sim.readEvery = batched ? 0.025 : nil
        return sim
    }

    func testAKeyframeOnASlowLinkIsNotASlowerLink() {
        // 25 Mbit/s, then 3 for a minute from 30 s, then 25 again: 2×60's floor at 1080p60 (2.5 Mbit/s) doesn't fit 80% of
        // 3, 1.5×30's (1.2) does, so one step down and no more, whatever the keyframes queue.
        let link = LinkSimulation.Link(kbps: { $0 >= 30 && $0 < 90 ? 3_000 : 25_000 }, rttMs: 6, jitterMs: 2,
                                       stats: LinkStats(kind: .lan, via: .wifi))
        for batched in [false, true] {
            let r = lightScreen(batched: batched).run(link, seconds: 180)
            report("1× display, light content, keyframes, 25 → 3 → 25 Mbit/s\(batched ? ", frames read together under a backlog" : "")", r,
                   [("before", 0, 30), ("capped", 30, 90), ("after", 90, 180)])
            let down = r.changes.filter { $0.at >= 30 && $0.at < 90 }
            XCTAssertEqual(down.map(\.to), ["1.5x30"], "one step down, batched \(batched): \(r.changes)")
        }
    }

    func testLightContentClimbsBackAfterADip() {
        let link = LinkSimulation.Link(kbps: { $0 >= 30 && $0 < 90 ? 3_000 : 25_000 }, rttMs: 6, jitterMs: 2,
                                       stats: LinkStats(kind: .lan, via: .wifi))
        for batched in [false, true] {
            let r = lightScreen(batched: batched).run(link, seconds: 180)
            let back = r.changes.first { $0.at >= 90 && $0.to == "2x60" }
            XCTAssertNotNil(back, "back to the best tier, batched \(batched): \(r.changes)")
            XCTAssertLessThan((back?.at ?? .infinity) - 90, 30, "within half a minute")
        }
    }
}
