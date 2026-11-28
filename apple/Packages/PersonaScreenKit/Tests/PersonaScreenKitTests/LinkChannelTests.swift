import Foundation
import XCTest
@testable import PersonaScreenKit

/// The phone's side of a screen session's channels, route measurement and eligibility (#6398).
final class LinkChannelTests: XCTestCase {
    func testTheReceiverDeliversEachByteOnceInOrder() throws {
        var r = LinkReceiver()
        XCTAssertEqual(try r.accept(Data("abc".utf8), at: 0), Data("abc".utf8))
        XCTAssertEqual(try r.accept(Data("bcde".utf8), at: 1), Data("de".utf8), "an overlap is trimmed")
        XCTAssertEqual(try r.accept(Data("ab".utf8), at: 0), Data(), "a copy delivers nothing")
        XCTAssertEqual(r.offset, 5)
        XCTAssertThrowsError(try r.accept(Data("z".utf8), at: 9)) { e in
            XCTAssertEqual(e as? LinkReceiver.Failure, .gap(expected: 5, got: 9))
        }
        XCTAssertTrue(r.reset(to: 20))
        XCTAssertEqual(r.offset, 20)
        XCTAssertFalse(r.reset(to: 20), "a reset at the offset changes nothing (#10005)")
        XCTAssertFalse(r.reset(to: 3))
        XCTAssertEqual(r.offset, 20, "a reset never goes back")
    }

    func testTheSenderKeepsWhatIsUnacknowledgedAndResends() {
        var s = LinkSender(window: 8)
        s.append(Data("hello".utf8), at: 0)
        XCTAssertTrue(s.hasRoom)
        s.append(Data("world".utf8), at: 1)
        XCTAssertFalse(s.hasRoom, "past the window the caller holds back")
        XCTAssertEqual(s.next(max: 4)?.bytes, Data("hell".utf8))
        XCTAssertEqual(s.next(max: 100)?.offset, 4)
        XCTAssertNil(s.next(max: 1))
        s.acknowledged(through: 3)
        XCTAssertEqual(s.unacknowledged, 7)
        XCTAssertEqual(s.oldestAge(at: 2), 2, "the oldest byte left came at 0")
        s.acknowledged(through: 6)
        XCTAssertEqual(s.oldestAge(at: 2), 1)
        XCTAssertEqual(s.oldestAge(at: 0), 0, "backward clock never produces negative oldestAge")
        // A move: the node has 7, so the rest goes again from there.
        s.resume(from: 7)
        XCTAssertNil(s.next(max: 0))
        XCTAssertNil(s.next(max: -5))
        // Consume 1 byte first so `from > 0` before passing `Int.max` (exercises `from + max` overflow guard).
        XCTAssertEqual(s.next(max: 1)?.bytes, Data("r".utf8))
        XCTAssertEqual(s.next(max: Int.max)?.bytes, Data("ld".utf8))
        s.acknowledged(through: 99)
        XCTAssertEqual(s.unacknowledged, 3, "an offset past what was sent is ignored")
        s.acknowledged(through: 10)
        XCTAssertNil(s.oldestAge(at: 5))
        XCTAssertTrue(s.hasRoom)
    }

    func testReceiverNearMaxOffsetAndMeterNonFiniteInputsDoNotTrapOrPoison() throws {
        var r = LinkReceiver(offset: UInt64.max - 2)
        // Non-zero startIndex Data slice exercises safe slicing when capping at UInt64.max.
        let slice = Data("xxabcde".utf8).dropFirst(2)
        XCTAssertEqual(try r.accept(slice, at: UInt64.max - 2), Data("ab".utf8))
        XCTAssertEqual(r.offset, UInt64.max)
        XCTAssertEqual(try r.accept(Data("z".utf8), at: UInt64.max), Data())

        var m = LinkMeter()
        for i in 0 ..< 3 { m.pong(rttMs: 10, retransmitted: 0, sent: 0, counts: false, at: Double(i)) }
        m.pong(rttMs: .nan, retransmitted: 0, sent: 0, counts: false, at: 3)
        m.pong(rttMs: .infinity, retransmitted: 0, sent: 0, counts: false, at: 3)
        m.pong(rttMs: -5, retransmitted: 0, sent: 0, counts: false, at: 3)
        let s = m.stats(kind: .lan, via: .wifi, at: 3)
        XCTAssertTrue(s.healthy, "non-finite/negative RTT samples must not poison EWMA or health")
        XCTAssertEqual(s.srttMs, 10)
    }

    func testTheAssemblerHandsOnWholeMessagesAndDropsAPartialOneOnReset() throws {
        let a = ScreenVideoWire.frame(4, body: Data(#"{"last":1}"#.utf8))
        let b = ScreenVideoWire.frame(3, body: Data(count: 40))
        var asm = VideoMessageAssembler()
        XCTAssertEqual(try asm.push(a + b.prefix(10)), a)
        asm.reset()
        let c = ScreenVideoWire.frame(4, body: Data(#"{"last":2}"#.utf8))
        XCTAssertEqual(try asm.push(c), c, "the half of b is gone; c comes whole")
    }

    func testTheMeter() {
        var m = LinkMeter()
        for (i, rtt) in [10.0, 12, 8, 30].enumerated() {
            m.pong(rttMs: rtt, retransmitted: 0, sent: 0, counts: false, at: Double(i))
        }
        let s = m.stats(kind: .tailscale, via: .wifi, at: 3)
        XCTAssertEqual(s.minRttMs, 8)
        XCTAssertEqual(s.samples, 4)
        XCTAssertNil(s.packetLoss, "no packets counted: unknown")
        XCTAssertTrue(s.healthy)
        XCTAssertEqual(s.srttMs!, 12.5, accuracy: 0.05) // 10 → 10.25 → 9.97 → 12.47 with gain 1/8
        XCTAssertGreaterThan(s.jitterMs!, 0)
        XCTAssertFalse(m.stats(kind: .tailscale, via: .wifi, at: 7).healthy, "no pong for over 3 s")
        XCTAssertEqual(m.stats(kind: .tailscale, via: .wifi, at: 30).samples, 0, "out of the 10 s window")

        var lossy = LinkMeter()
        for i in 0..<5 { lossy.pong(rttMs: 5, retransmitted: 4, sent: 40, counts: true, at: Double(i)) }
        let l = lossy.stats(kind: .lan, via: .wifi, at: 4)
        XCTAssertEqual(l.packetLoss!, 0.1, accuracy: 0.0001)
        XCTAssertFalse(l.healthy, "10% loss")
    }

    func testEligibility() {
        let wifi = PhoneInterface(name: "en0", via: .wifi, addresses: [.init("192.0.2.40", prefix: 24)!, .init("fd00:1:2:3::9", prefix: 64)!])
        let cell = PhoneInterface(name: "pdp_ip0", via: .cellular, addresses: [.init("198.51.100.7", prefix: 32)!])
        let tailscale = PhoneInterface(name: "utun4", via: .other, addresses: [.init("100.100.1.2", prefix: 32)!])
        let proxy = PhoneInterface(name: "utun5", via: .other, addresses: [.init("203.0.113.1", prefix: 32)!])
        let all = [cell, proxy, wifi, tailscale]
        func on(_ kind: String, _ addr: String, _ ifs: [PhoneInterface] = all) -> String? {
            RouteEligibility.interface(for: .init(kind: kind, addr: addr, prefix: 24), among: ifs)?.name
        }
        XCTAssertEqual(on("lan", "192.0.2.10"), "en0", "the same Wi-Fi subnet")
        XCTAssertNil(on("lan", "192.0.3.10"), "another subnet: maybe another network altogether")
        XCTAssertNil(on("lan", "192.0.2.10", [cell, tailscale]), "never over cellular or a tunnel")
        XCTAssertEqual(on("lan", "fd00:1:2:3::5"), "en0")
        XCTAssertEqual(on("tailscale", "100.64.0.7"), "utun4", "only through the phone's own Tailscale")
        XCTAssertEqual(on("tailscale", "fd7a:115c:a1e0::7", [PhoneInterface(name: "utun6", via: .other, addresses: [.init("fd7a:115c:a1e0::9", prefix: 128)!])]), "utun6")
        XCTAssertNil(on("tailscale", "100.64.0.7", [cell, proxy, wifi]), "Tailscale off: never dialed (no carrier, no proxy)")
        XCTAssertNil(on("tailscale", "192.0.2.10"), "a Tailscale candidate outside its ranges")
        XCTAssertNil(on("ipv6", "2001:db8::1"), "direct IPv6: not in v1")
        XCTAssertNil(on("lan", "not an address"))
    }
}

/// The route choice on synthetic timelines.
final class PathPolicyTests: XCTestCase {
    private func stats(_ kind: LinkStats.Kind, _ ms: Double?, healthy: Bool = true, loss: Double? = nil) -> LinkStats {
        LinkStats(kind: kind, via: .wifi, srttMs: ms, minRttMs: ms, jitterMs: 1, packetLoss: loss, samples: 5, healthy: healthy)
    }

    func testAtTheStartTheFirstBetterDirectRouteIsUsedAtOnce() {
        var p = PathPolicy(relay: "relay")
        XCTAssertEqual(p.decide(["relay": stats(.relay, nil, healthy: false)], now: 0), .stay)
        XCTAssertEqual(p.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 1), .move(to: "lan", reason: .faster))
        XCTAssertEqual(p.current, "lan")
    }

    func testATieGoesToTheDirectRouteButASlowerOneStaysOff() {
        var tie = PathPolicy(relay: "relay")
        XCTAssertEqual(tie.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 43)], now: 0), .move(to: "ts", reason: .faster))
        var slow = PathPolicy(relay: "relay")
        XCTAssertEqual(slow.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 120)], now: 0), .stay,
                       "Tailscale through a far relay server: the hub is faster")
    }

    /// The acceptance's Tailscale story: off mid-stream → the relay at once; back on → direct after the stable window.
    func testTailscaleOffAndBackOn() {
        var p = PathPolicy(relay: "relay")
        _ = p.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 8)], now: 0)
        XCTAssertEqual(p.current, "ts")
        XCTAssertEqual(p.decide(["relay": stats(.relay, 40)], lost: ["ts"], networkChanged: true, now: 30),
                       .move(to: "relay", reason: .networkChanged))
        var t = 31.0
        while t < 41 { // back on at 31: healthy, but not for the stable window yet
            XCTAssertEqual(p.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 8)], now: t), .stay, "t=\(t)")
            t += 1
        }
        XCTAssertEqual(p.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 8)], now: 51), .move(to: "ts", reason: .faster))
    }

    func testALostRouteFallsToTheRelayEvenUnmeasured() {
        var p = PathPolicy(relay: "relay")
        _ = p.decide(["relay": stats(.relay, nil, healthy: false), "lan": stats(.lan, 4)], now: 0)
        XCTAssertEqual(p.decide(["relay": stats(.relay, nil, healthy: false)], lost: ["lan"], now: 5), .move(to: "relay", reason: .lost))
    }

    /// LAN jumping between 8 and 12 ms around Tailscale's 10: at most one move in ten minutes (to LAN, preferred on a
    /// tie), never back and forth.
    func testTwoCloseRoutesNeverFlap() {
        var p = PathPolicy(relay: "relay")
        _ = p.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 10)], now: 0)
        var moves: [PathPolicy.Decision] = []
        for t in stride(from: 1.0, through: 600, by: 1) {
            let lan = 8 + (t.truncatingRemainder(dividingBy: 2)) * 4
            let d = p.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 10), "lan": stats(.lan, lan)], now: t)
            if d != .stay { moves.append(d) }
        }
        XCTAssertLessThanOrEqual(moves.count, 1, "\(moves)")
    }

    func testAClearlyBetterRouteWaitsForTheStableWindowAndTheDwell() {
        var p = PathPolicy(relay: "relay")
        _ = p.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 30)], now: 0) // through a far relay server
        XCTAssertEqual(p.current, "ts")
        var moved: Double?
        for t in stride(from: 1.0, through: 40, by: 1) {
            if case .move(to: "lan", reason: .faster) = p.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 30), "lan": stats(.lan, 5)], now: t) {
                moved = t
                break
            }
        }
        XCTAssertEqual(moved, 20, "the dwell (20 s since the first move) and 10 s healthy from t=1")
    }

    func testADegradedRouteIsLeftWhenAnotherIsHealthy() {
        var p = PathPolicy(relay: "relay")
        _ = p.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        var got: PathPolicy.Decision = .stay
        for t in stride(from: 10.0, through: 20, by: 1) {
            got = p.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5, loss: 0.08)], now: t)
            if got != .stay { XCTAssertEqual(t, 15, "after 5 s degraded"); break }
        }
        XCTAssertEqual(got, .move(to: "relay", reason: .degraded))
        // Without a healthy alternative it stays.
        var alone = PathPolicy(relay: "relay")
        _ = alone.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        for t in stride(from: 10.0, through: 30, by: 1) {
            XCTAssertEqual(alone.decide(["relay": stats(.relay, nil, healthy: false), "lan": stats(.lan, 5, loss: 0.08)], now: t), .stay)
        }
    }

    func testARouteThatAppearsLaterWaitsForTheStableWindow() {
        var p = PathPolicy(relay: "relay")
        XCTAssertEqual(p.decide(["relay": stats(.relay, 40)], now: 0), .stay)
        XCTAssertEqual(p.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 60), .stay, "joined Wi-Fi a minute in")
        XCTAssertEqual(p.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 70), .move(to: "lan", reason: .faster))
    }

    func testDirectOff() {
        var p = PathPolicy(relay: "relay")
        _ = p.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        XCTAssertEqual(p.directOff(now: 3), .move(to: "relay", reason: .directOff))
        XCTAssertEqual(p.directOff(now: 4), .stay)
    }

    func testThreeWayTieBandRankingIsDeterministicAndNeverSelectsLostRelay() {
        // Classic 3-cycle for pairwise tieMs (5 ms): relay=10, ts=14, lan=18.
        // Fastest measured route is 10 ms; only routes within 10+5=15 ms (relay, ts) are in the top tie band,
        // and ts beats relay deterministically regardless of dictionary ordering or extra NaN entries.
        for _ in 0 ..< 16 {
            var p = PathPolicy(relay: "relay")
            let routes: [String: LinkStats] = [
                "relay": stats(.relay, 10),
                "ts": stats(.tailscale, 14),
                "lan": stats(.lan, 18),
                "bad": stats(.lan, .nan),
            ]
            XCTAssertEqual(p.decide(routes, now: 0), .move(to: "ts", reason: .faster))
        }

        // Same-kind, same-RTT routes break ties deterministically by route id; negative RTT is ignored.
        var tieId = PathPolicy(relay: "relay")
        XCTAssertEqual(
            tieId.decide([
                "relay": stats(.relay, 40),
                "lan-b": stats(.lan, 10),
                "lan-a": stats(.lan, 10),
                "lan-neg": stats(.lan, -1)
            ], now: 0),
            .move(to: "lan-a", reason: .faster)
        )

        // If both the current direct route and the relay are reported in `lost` while still present in `routes`,
        // `best` must NOT select the lost relay.
        var lostRelay = PathPolicy(relay: "relay")
        _ = lostRelay.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        XCTAssertEqual(lostRelay.current, "lan")
        XCTAssertEqual(
            lostRelay.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], lost: ["lan", "relay"], now: 10),
            .stay
        )
    }

    func testUnhealthyFastRelayNeverMasksHealthyFallbackOrWinsUpgrade() {
        // Degraded failover: unhealthy fast relay (10 ms) must not mask healthy Tailscale (20 ms).
        var degraded = PathPolicy(relay: "relay")
        _ = degraded.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        XCTAssertEqual(degraded.current, "lan")
        var moved: PathPolicy.Decision = .stay
        for t in stride(from: 10.0, through: 16, by: 1) {
            moved = degraded.decide([
                "relay": stats(.relay, 10, healthy: false),
                "ts": stats(.tailscale, 20, healthy: true),
                "lan": stats(.lan, 5, healthy: false, loss: 0.08)
            ], now: t)
            if moved != .stay { break }
        }
        XCTAssertEqual(moved, .move(to: "ts", reason: .degraded))

        // Lost failover: prefer healthy Tailscale (20 ms) over unhealthy fast relay (10 ms).
        var lostPreferHealthy = PathPolicy(relay: "relay")
        _ = lostPreferHealthy.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        XCTAssertEqual(
            lostPreferHealthy.decide([
                "relay": stats(.relay, 10, healthy: false),
                "ts": stats(.tailscale, 20, healthy: true)
            ], lost: ["lan"], now: 10),
            .move(to: "ts", reason: .lost)
        )

        // Faster upgrade: unhealthy fast relay must neither be selected as an upgrade target nor mask healthy LAN.
        var noBadUpgrade = PathPolicy(relay: "relay")
        _ = noBadUpgrade.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 30)], now: 0)
        XCTAssertEqual(noBadUpgrade.current, "ts")
        XCTAssertEqual(
            noBadUpgrade.decide([
                "relay": stats(.relay, 5, healthy: false),
                "ts": stats(.tailscale, 30, healthy: true)
            ], now: 30),
            .stay,
            "never upgrade from a healthy route onto an unhealthy relay"
        )
        // Healthy LAN appears at t=30 and becomes stable by t=40 despite unhealthy 5 ms relay.
        _ = noBadUpgrade.decide([
            "relay": stats(.relay, 5, healthy: false),
            "ts": stats(.tailscale, 30, healthy: true),
            "lan": stats(.lan, 15, healthy: true)
        ], now: 30)
        XCTAssertEqual(
            noBadUpgrade.decide([
                "relay": stats(.relay, 5, healthy: false),
                "ts": stats(.tailscale, 30, healthy: true),
                "lan": stats(.lan, 15, healthy: true)
            ], now: 40),
            .move(to: "lan", reason: .faster)
        )

        // Non-finite timestamps in decide() do not poison state; directOff() with non-finite now preserves finite lastMove.
        var clock = PathPolicy(relay: "relay")
        XCTAssertEqual(clock.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: .nan), .stay)
        XCTAssertEqual(clock.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: .infinity), .stay)
        XCTAssertEqual(clock.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: -.infinity), .stay)
        XCTAssertEqual(clock.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0), .move(to: "lan", reason: .faster))
        XCTAssertEqual(clock.directOff(now: .nan), .move(to: "relay", reason: .directOff))
        XCTAssertEqual(clock.current, "relay")
        // Verify dwell (20 s from lastMove == 0) is strictly enforced (if lastMove were NaN, 10 - NaN < 20 would be false!).
        XCTAssertEqual(clock.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 10), .stay)
        XCTAssertEqual(clock.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 20), .move(to: "lan", reason: .faster))
        // Verify directOff(now: .infinity) does not wedge dwell with +Inf.
        XCTAssertEqual(clock.directOff(now: .infinity), .move(to: "relay", reason: .directOff))
        XCTAssertEqual(clock.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 30), .stay)
        XCTAssertEqual(clock.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 40), .move(to: "lan", reason: .faster))

        // Degraded failover still succeeds after directOff(now: .nan) (degradedDwell not wedged by NaN).
        var degradedAfterNan = PathPolicy(relay: "relay")
        _ = degradedAfterNan.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        XCTAssertEqual(degradedAfterNan.directOff(now: .nan), .move(to: "relay", reason: .directOff))
        // Use a slower healthy LAN (50 ms) so only degraded failover (not .faster) can move off relay.
        var movedAfterNan: PathPolicy.Decision = .stay
        for t in stride(from: 10.0, through: 16, by: 1) {
            movedAfterNan = degradedAfterNan.decide([
                "relay": stats(.relay, 40, healthy: false, loss: 0.08),
                "lan": stats(.lan, 50, healthy: true)
            ], now: t)
            if movedAfterNan != .stay { break }
        }
        XCTAssertEqual(movedAfterNan, .move(to: "lan", reason: .degraded))

        // Degraded failover works for both newly-healthy non-leftBad relay AND newly-healthy leftBad relay.
        var newlyHealthyRelay = PathPolicy(relay: "relay")
        _ = newlyHealthyRelay.decide(["relay": stats(.relay, nil, healthy: false), "lan": stats(.lan, 5)], now: 0)
        for t in stride(from: 10.0, through: 14, by: 1) {
            XCTAssertEqual(newlyHealthyRelay.decide(["relay": stats(.relay, nil, healthy: false), "lan": stats(.lan, 5, loss: 0.08)], now: t), .stay)
        }
        XCTAssertEqual(newlyHealthyRelay.decide(["relay": stats(.relay, 40, healthy: true), "lan": stats(.lan, 5, loss: 0.08)], now: 15),
                       .move(to: "relay", reason: .degraded))

        var leftBadRelay = PathPolicy(relay: "relay")
        _ = leftBadRelay.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        _ = leftBadRelay.decide(["lan": stats(.lan, 5)], lost: ["relay"], now: 1) // puts relay in leftBad
        for t in stride(from: 10.0, through: 14, by: 1) {
            XCTAssertEqual(leftBadRelay.decide(["relay": stats(.relay, 40, healthy: false), "lan": stats(.lan, 5, loss: 0.08)], now: t), .stay)
        }
        XCTAssertEqual(leftBadRelay.decide(["relay": stats(.relay, 40, healthy: true), "lan": stats(.lan, 5, loss: 0.08)], now: 15),
                       .move(to: "relay", reason: .degraded), "must not strand degraded direct route when relay is healthy")

        // leftBad auto-clears after stableWindow so a much later brief blip does not re-impose a 10 s penalty.
        var autoClear = PathPolicy(relay: "relay")
        _ = autoClear.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0)
        _ = autoClear.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], lost: ["ts"], now: 1)
        _ = autoClear.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5), "ts": stats(.tailscale, 20)], now: 2)
        _ = autoClear.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5), "ts": stats(.tailscale, 20)], now: 12) // leftBad cleared
        _ = autoClear.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5), "ts": stats(.tailscale, 20, healthy: false)], now: 13)
        for t in stride(from: 14.0, through: 18, by: 1) {
            _ = autoClear.decide(["relay": stats(.relay, 40, healthy: false), "lan": stats(.lan, 5, loss: 0.08)], now: t)
        }
        XCTAssertEqual(
            autoClear.decide([
                "relay": stats(.relay, 40, healthy: false),
                "lan": stats(.lan, 5, loss: 0.08),
                "ts": stats(.tailscale, 20, healthy: true)
            ], now: 19),
            .move(to: "ts", reason: .degraded)
        )

        // Healthy relay waits stableWindow for .faster upgrade, and disappearing from routes resets its healthySince.
        var fastRelay = PathPolicy(relay: "relay")
        _ = fastRelay.decide(["relay": stats(.relay, 40), "ts": stats(.tailscale, 30)], now: 0)
        XCTAssertEqual(fastRelay.current, "ts")
        // Relay disappears from routes without appearing in `lost` (e.g., ScreenLinkSession relay drop).
        XCTAssertEqual(fastRelay.decide(["ts": stats(.tailscale, 30)], now: 20), .stay)
        // Relay reappears healthy and much faster at t=25; must wait stableWindow (until t=35).
        XCTAssertEqual(fastRelay.decide(["relay": stats(.relay, 5), "ts": stats(.tailscale, 30)], now: 25), .stay)
        XCTAssertEqual(fastRelay.decide(["relay": stats(.relay, 5), "ts": stats(.tailscale, 30)], now: 34), .stay)
        XCTAssertEqual(fastRelay.decide(["relay": stats(.relay, 5), "ts": stats(.tailscale, 30)], now: 35),
                       .move(to: "relay", reason: .faster))

        // Pathological floorWindow (< 0, NaN, +Inf) stays bounded and does not break decide().
        for badWindow in [-5.0, .nan, .infinity] {
            var cfg = PathPolicy.Config()
            cfg.floorWindow = badWindow
            var p = PathPolicy(relay: "relay", config: cfg)
            XCTAssertEqual(p.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 0), .move(to: "lan", reason: .faster))
            XCTAssertEqual(p.decide(["relay": stats(.relay, 40), "lan": stats(.lan, 5)], now: 10), .stay)
        }
    }
}
