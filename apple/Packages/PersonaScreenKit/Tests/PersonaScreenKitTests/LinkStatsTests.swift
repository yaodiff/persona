import Foundation
import XCTest
@testable import PersonaScreenKit

/// The route measurement shared with the quality tiers (#6398, #6396), and `update.path`, which tells the helper the
/// stream moved to another route.
final class LinkStatsTests: XCTestCase {
    /// The names on the wire are part of the protocol: the helper and the app may come from different releases.
    func testWireNames() {
        XCTAssertEqual(LinkStats.Kind.allCases.map(\.rawValue), ["lan", "tailscale", "ipv6", "relay"])
        XCTAssertEqual(LinkStats.Via.allCases.map(\.rawValue), ["wifi", "cellular", "wired", "other"])
        XCTAssertEqual(PathChange.Reason.allCases.map(\.rawValue),
                       ["start", "faster", "lost", "degraded", "network_changed", "direct_off"])
    }

    func testPathFromLinkStats() {
        let link = LinkStats(kind: .lan, via: .wifi, srttMs: 6, minRttMs: 4, jitterMs: 1.5, packetLoss: 0, samples: 10,
                             healthy: true)
        let path = UpdateMessage.Path(link)
        XCTAssertEqual(path, UpdateMessage.Path(kind: "lan", via: "wifi", minRttMs: 4))
        XCTAssertEqual(path.linkKind, .lan)
        XCTAssertEqual(path.linkVia, .wifi)
    }

    func testPathJSONUsesSnakeCaseAndLeavesTheRestOut() throws {
        let framed = ScreenVideoMessage.update(UpdateMessage(path: .init(kind: "relay", via: "cellular", minRttMs: 48))).framed()
        let object = try JSONSerialization.jsonObject(with: framed.dropFirst(5)) as? [String: Any]
        XCTAssertEqual(Set(object?.keys.map { $0 } ?? []), ["path"], "an update about the route changes nothing else")
        let path = object?["path"] as? [String: Any]
        XCTAssertEqual(path?["kind"] as? String, "relay")
        XCTAssertEqual(path?["via"] as? String, "cellular")
        XCTAssertEqual(path?["min_rtt_ms"] as? Double, 48)
        XCTAssertNil(path?["minRttMs"])
    }

    /// A newer app may name a route this helper doesn't know: the update still decodes, with the kind kept as said.
    func testUnknownKindStillDecodes() throws {
        let body = Data(#"{"max_kbps":4000,"path":{"kind":"quic","via":"satellite"}}"#.utf8)
        let decoded = try ScreenVideoMessage.decode(type: ScreenVideoWire.Kind.update.rawValue, body: body)
        guard case let .update(m) = decoded else { return XCTFail("\(decoded)") }
        XCTAssertEqual(m.maxKbps, 4000)
        XCTAssertEqual(m.path?.kind, "quic")
        XCTAssertNil(m.path?.linkKind)
        XCTAssertNil(m.path?.linkVia)
        XCTAssertNil(m.path?.minRttMs)
    }

    /// An app before #6398 sends no path, and a helper before it never reads one.
    func testUpdateWithoutPath() throws {
        let body = Data(#"{"paused":true}"#.utf8)
        guard case let .update(m) = try ScreenVideoMessage.decode(type: ScreenVideoWire.Kind.update.rawValue, body: body) else {
            return XCTFail()
        }
        XCTAssertNil(m.path)
        XCTAssertEqual(m.paused, true)
    }

    func testFirstChangeHasNoFrom() {
        let relay = LinkStats(kind: .relay, via: .wifi)
        let change = PathChange(from: nil, to: relay, reason: .start)
        XCTAssertNil(change.from)
        XCTAssertEqual(change.to.kind, .relay)
        XCTAssertNil(relay.packetLoss, "the relay's loss is unknown until measured")
        XCTAssertFalse(relay.healthy, "a route is healthy only once measured")
    }
}
