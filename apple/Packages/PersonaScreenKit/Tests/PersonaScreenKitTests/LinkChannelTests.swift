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
        r.reset(to: 20)
        XCTAssertEqual(r.offset, 20)
        r.reset(to: 3)
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
        // A move: the node has 7, so the rest goes again from there.
        s.resume(from: 7)
        XCTAssertEqual(s.next(max: 100)?.bytes, Data("rld".utf8))
        s.acknowledged(through: 99)
        XCTAssertEqual(s.unacknowledged, 3, "an offset past what was sent is ignored")
        s.acknowledged(through: 10)
        XCTAssertNil(s.oldestAge(at: 5))
        XCTAssertTrue(s.hasRoom)
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
}
