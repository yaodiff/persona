import Foundation
import XCTest
@testable import PersonaScreenKit

/// The tests seal with the cipher the app uses.
typealias TestAESGCM = ScreenUDPAESGCM
extension ScreenUDPAESGCM {
    init(_ key: Data) { self.init(key: key) }
}

private extension Data {
    var hex: String { map { String(format: "%02x", $0) }.joined() }
}

/// The shared cases of the video over UDP (spec/fixtures/screen-udp, #7873): the node's internal/modules/screen-share/screenlink checks the
/// same file, so both ends seal, number, cut and rebuild alike.
final class ScreenUDPFixtureTests: XCTestCase {
    private static var file: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("spec/fixtures/screen-udp/cases.json")
    }

    private func load() throws -> [String: Any] {
        try XCTUnwrap(try JSONSerialization.jsonObject(with: Data(contentsOf: Self.file)) as? [String: Any])
    }

    private static func number(_ v: Any?) -> UInt64 { (v as! NSNumber).uint64Value }

    private static func message(_ e: [String: Any]) -> ScreenUDPMessage {
        let n = { (k: String) in number(e[k]) }
        let entries = { (e["entries"] as! [[NSNumber]]).map { $0.map(\.uint64Value) } }
        switch e["kind"] as! String {
        case "hello_phone": return .helloPhone(echo: n("echo"))
        case "hello_node": return .helloNode(challenge: n("challenge"), validated: n("validated") != 0)
        case "data":
            return .data(ScreenUDPFragmentHead(channel: UInt8(n("channel")), offset: n("offset"), length: UInt32(n("length")),
                                               block: UInt16(n("block")), index: UInt16(n("index")), data: UInt8(n("data")),
                                               parity: UInt8(n("parity")), flags: UInt8(n("flags"))),
                         fragment: Data(hex: e["fragment_hex"] as! String))
        case "ack": return .ack(channel: UInt8(n("channel")), offset: n("offset"))
        case "skip": return .skip(channel: UInt8(n("channel")), offset: n("offset"))
        case "reset": return .reset(channel: UInt8(n("channel")), offset: n("offset"))
        case "nack":
            return .nack(channel: UInt8(n("channel")), entries().map { ScreenUDPNack(offset: $0[0], block: UInt16($0[1]), index: UInt16($0[2])) })
        case "want": return .want(channel: UInt8(n("channel")), from: n("from"), to: n("to"))
        case "use": return .use(entries().map { ScreenLinkOffset(UInt8($0[0]), $0[1]) })
        case "resumed": return .resumed(entries().map { ScreenLinkOffset(UInt8($0[0]), $0[1]) })
        case "ping": return .ping(micros: n("phone_us"), parity: UInt16(n("parity")), paceKbps: UInt32(n("pace_kbps")))
        case "pong": return .pong(micros: n("echo"), dataBytes: n("data_bytes"), parityBytes: n("parity_bytes"), resentBytes: n("resent_bytes"))
        default: fatalError("kind \(e["kind"]!)")
        }
    }

    func testPacketsSealAndReadAsTheNodeDoes() throws {
        let doc = try load()
        let exporter = try XCTUnwrap(doc["exporter"] as? [String: Any])
        XCTAssertEqual(exporter["label"] as? String, ScreenUDPWire.exporterLabel)
        let keys = try XCTUnwrap(ScreenUDPKeyMaterial(Data(hex: exporter["material_hex"] as! String)))
        let toPhone = exporter["node_to_phone"] as! [String: String], toNode = exporter["phone_to_node"] as! [String: String]
        XCTAssertEqual(keys.toPhoneKey.hex, toPhone["key_hex"])
        XCTAssertEqual(keys.toPhoneIV.hex, toPhone["iv_hex"])
        XCTAssertEqual(keys.toNodeKey.hex, toNode["key_hex"])
        XCTAssertEqual(keys.toNodeIV.hex, toNode["iv_hex"])
        for c in try XCTUnwrap(doc["packets"] as? [[String: Any]]) {
            let name = c["name"] as! String
            let fromNode = c["direction"] as! String == "node_to_phone"
            let body = Data(hex: c["body_hex"] as! String)
            let m = Self.message(c["expected"] as! [String: Any])
            XCTAssertEqual(m.encoded().hex, body.hex, name)
            XCTAssertEqual(try ScreenUDPMessage.decode(body, fromNode: fromNode), m, name)
            let cipher = TestAESGCM(fromNode ? keys.toPhoneKey : keys.toNodeKey)
            let iv = fromNode ? keys.toPhoneIV : keys.toNodeIV
            let flow = Data(hex: c["flow_hex"] as! String), number = Self.number(c["packet_number"])
            let sealed = Data(hex: c["sealed_hex"] as! String)
            XCTAssertEqual(ScreenUDPPacket.seal(body, cipher: cipher, iv: iv, flow: flow, number: number)?.hex, sealed.hex, name)
            let head = try XCTUnwrap(ScreenUDPPacket.read(sealed), name)
            XCTAssertEqual(head.flow, flow, name)
            XCTAssertEqual(head.number, number, name)
            XCTAssertEqual(ScreenUDPPacket.open(sealed, cipher: cipher, iv: iv, number: number), body, name)
            // The other direction's key doesn't open it.
            let other = TestAESGCM(fromNode ? keys.toNodeKey : keys.toPhoneKey)
            XCTAssertNil(ScreenUDPPacket.open(sealed, cipher: other, iv: fromNode ? keys.toNodeIV : keys.toPhoneIV, number: number), name)
        }
    }

    func testMessagesAreCutAsTheNodeCutsThem() throws {
        for c in try XCTUnwrap(try load()["layout"] as? [[String: Any]]) {
            let name = c["name"] as! String
            let blocks = ScreenUDPLayout.blocks(length: Int(Self.number(c["message_length"])))
            let want = c["blocks"] as! [[String: NSNumber]]
            XCTAssertEqual(blocks.count, want.count, name)
            for (b, w) in zip(blocks, want) {
                XCTAssertEqual(b.first, w["first"]!.intValue, name)
                XCTAssertEqual(b.data, w["data"]!.intValue, name)
                XCTAssertEqual(b.shardLength, w["shard_length"]!.intValue, name)
            }
            for (ratio, counts) in c["parity"] as! [String: [NSNumber]] {
                XCTAssertEqual(blocks.map { ScreenUDPLayout.parity(data: $0.data, ratio: Int(ratio)!) }, counts.map(\.intValue), "\(name) at \(ratio)")
            }
        }
    }

    func testReedSolomonMatchesTheNode() throws {
        for c in try XCTUnwrap(try load()["reed_solomon"] as? [[String: Any]]) {
            let name = c["name"] as! String
            let data = (c["data_hex"] as! [String]).map { [UInt8](Data(hex: $0)) }
            let parity = (c["parity_hex"] as! [String]).map { [UInt8](Data(hex: $0)) }
            XCTAssertEqual(try ReedSolomon.encode(data, parity: parity.count), parity, name)
            var shards: [[UInt8]?] = data + parity
            for i in c["lost"] as! [Int] { shards[i] = nil }
            try ReedSolomon.reconstruct(&shards, data: data.count)
            XCTAssertEqual(Array(shards[0 ..< data.count]).compactMap { $0 }, data, name)
        }
    }

    func testTheLinkMessages() throws {
        for c in try XCTUnwrap(try load()["link"] as? [[String: Any]]) {
            let name = c["name"] as! String
            var parser = ScreenVideoParser()
            let raws = try parser.push(Data(hex: c["frame_hex"] as! String))
            XCTAssertEqual(raws.count, 1, name)
            let m = try ScreenLinkMessage.decode(type: raws[0].type, body: raws[0].body)
            let e = c["expected"] as! [String: Any]
            switch m {
            case .udpOpen:
                XCTAssertEqual(e["type"] as? String, "udp_open", name)
                XCTAssertEqual(m.framed().hex, c["frame_hex"] as? String, name)
            case let .udpReady(r):
                XCTAssertEqual(e["type"] as? String, "udp_ready", name)
                XCTAssertEqual(r.port, (e["port"] as? NSNumber)?.intValue, name)
                XCTAssertEqual(r.flow, e["flow"] as? String, name)
                XCTAssertEqual(r.unavailable, e["unavailable"] as? String, name)
                if r.flow != nil { XCTAssertEqual(r.flowID?.count, 8, name) }
            default:
                XCTFail("\(name): \(m)")
            }
        }
    }
}

final class ScreenUDPTests: XCTestCase {
    func testTheReplayWindow() {
        var w = ScreenUDPReplayWindow()
        let cases: [(UInt64, Bool)] = [
            (0, true), (0, false), (2, true), (1, true), (1, false), (2, false),
            (5000, true), (5000 - 4096, false), (5000 - 4096 + 1, true), (4999, true), (4999, false),
            (1 << 40, true), (5000, false), ((1 << 40) - 1, true), (UInt64.max, false),
        ]
        for (pn, want) in cases { XCTAssertEqual(w.accept(pn), want, "packet \(pn)") }
    }

    func testASealerNeverRepeatsAndStopsAtTheLimit() {
        var s = ScreenUDPSealer(cipher: TestAESGCM(Data(count: 32)), iv: Data(count: 12), flow: Data(count: 8))
        var numbers = Set<UInt64>()
        for _ in 0 ..< 1000 {
            let p = s.seal(Data([9]))!
            numbers.insert(ScreenUDPPacket.read(p)!.number)
        }
        XCTAssertEqual(numbers, Set(0 ..< 1000))
        s = ScreenUDPSealer(cipher: TestAESGCM(Data(count: 32)), iv: Data(count: 12), flow: Data(count: 8), next: ScreenUDPWire.packetLimit - 1)
        XCTAssertNotNil(s.seal(Data([9])))
        XCTAssertNil(s.seal(Data([9])))
    }

    // MARK: Receiving

    /// The packets of `msg` at `off` as the node sends them, with `ratio` parity.
    private func packets(_ msg: Data, at off: UInt64, ratio: Int, droppable: Bool = true) -> [(ScreenUDPFragmentHead, Data)] {
        let bytes = [UInt8](msg)
        var out: [(ScreenUDPFragmentHead, Data)] = []
        for (bi, b) in ScreenUDPLayout.blocks(length: bytes.count).enumerated() {
            let m = ScreenUDPLayout.parity(data: b.data, ratio: ratio)
            var shards: [[UInt8]] = []
            for j in 0 ..< b.data {
                let from = (b.first + j) * ScreenUDPWire.fragment
                shards.append(Array(bytes[from ..< min(from + ScreenUDPWire.fragment, bytes.count)]))
            }
            let padded = shards.map { $0 + [UInt8](repeating: 0, count: b.shardLength - $0.count) }
            let parity = try! ReedSolomon.encode(padded, parity: m)
            let head = { (i: Int) in
                ScreenUDPFragmentHead(channel: 2, offset: off, length: UInt32(bytes.count), block: UInt16(bi), index: UInt16(i),
                                      data: UInt8(b.data), parity: UInt8(m), flags: droppable ? ScreenUDPWire.droppable : 0)
            }
            for (j, s) in shards.enumerated() { out.append((head(j), Data(s))) }
            for (i, p) in parity.enumerated() { out.append((head(b.data + i), Data(p))) }
        }
        return out
    }

    private func pattern(_ n: Int, seed: Int = 1) -> Data { Data((0 ..< n).map { UInt8(truncatingIfNeeded: $0 * 31 + seed) }) }

    func testParityRebuildsWhatWasLostWithoutAsking() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        let msg = pattern(20_000)
        var delivered = Data()
        for (i, p) in packets(msg, at: 0, ratio: 2000).enumerated() where i != 1 && i != 7 { // two data shards lost
            delivered += r.fragment(p.0, p.1, at: 0).messages.map(\.bytes).reduce(Data(), +)
        }
        XCTAssertEqual(delivered, msg)
        XCTAssertEqual(r.rebuilt, 2)
        XCTAssertEqual(r.expected, UInt64(msg.count))
        XCTAssertEqual(r.requests(at: 1, rtt: 0.01, jitter: 0), ScreenUDPReceiver.Requests())
    }

    func testWhatParityCannotCoverIsAskedForOnceItWasAllSent() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        let first = pattern(5000), second = pattern(800, seed: 2)
        let lost = Set([0, 2])
        for (i, p) in packets(first, at: 0, ratio: 0).enumerated() where !lost.contains(i) { _ = r.fragment(p.0, p.1, at: 0) }
        // Not yet: the message may still be on its way.
        XCTAssertTrue(r.requests(at: 0.001, rtt: 0.01, jitter: 0).nack.isEmpty)
        // A later message shows it was all sent.
        for p in packets(second, at: UInt64(first.count), ratio: 0) { _ = r.fragment(p.0, p.1, at: 0.002) }
        let asked = r.requests(at: 0.003, rtt: 0.01, jitter: 0)
        XCTAssertEqual(asked.nack, lost.sorted().map { ScreenUDPNack(offset: 0, block: 0, index: UInt16($0)) })
        // Not twice in a row; again after a round trip and a half.
        XCTAssertTrue(r.requests(at: 0.004, rtt: 0.01, jitter: 0).nack.isEmpty)
        XCTAssertEqual(r.requests(at: 0.020, rtt: 0.01, jitter: 0).nack.count, 2)
        var delivered = Data()
        for (i, p) in packets(first, at: 0, ratio: 0).enumerated() where lost.contains(i) {
            delivered += r.fragment(p.0, p.1, at: 0.03).messages.map(\.bytes).reduce(Data(), +)
        }
        XCTAssertEqual(delivered, first + second)
    }

    func testAMessageLostWholeIsAskedForByItsPlace() {
        var r = ScreenUDPReceiver(channel: 2, expected: 100)
        for p in packets(pattern(300), at: 400, ratio: 0) { _ = r.fragment(p.0, p.1, at: 0) }
        XCTAssertNil(r.requests(at: 0.001, rtt: 0.01, jitter: 0).want)
        let want = r.requests(at: 0.010, rtt: 0.01, jitter: 0).want
        XCTAssertEqual(want?.from, 100)
        XCTAssertEqual(want?.to, 400)
    }

    func testAFrameHoldingThePictureBackIsGivenUpAndTheStreamGoesOnAtTheReset() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        let stuck = pattern(3000), next = pattern(500, seed: 3)
        let p = packets(stuck, at: 0, ratio: 0)
        _ = r.fragment(p[0].0, p[0].1, at: 0) // the rest never comes
        for q in packets(next, at: 3000, ratio: 0) { _ = r.fragment(q.0, q.1, at: 0.001) }
        XCTAssertNil(r.requests(at: 0.05, rtt: 0.01, jitter: 0).skip)
        XCTAssertEqual(r.requests(at: 0.12, rtt: 0.01, jitter: 0).skip, 0)
        XCTAssertEqual(r.givenUp, 1)
        XCTAssertNil(r.requests(at: 0.121, rtt: 0.01, jitter: 0).skip) // once, until the reset or a while
        XCTAssertEqual(r.requests(at: 0.136, rtt: 0.01, jitter: 0).skip, 0) // retried while waiting for reset
        XCTAssertEqual(r.givenUp, 1) // retry does not count as another give-up
        let d = r.reset(to: 3000, at: 0.14)
        XCTAssertEqual(d.messages.map(\.bytes), [next])
        XCTAssertEqual(r.expected, 3500)
    }

    func testAMessageThatMustArriveIsNeverGivenUp() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        let format = packets(pattern(2000), at: 0, ratio: 0, droppable: false)
        _ = r.fragment(format[0].0, format[0].1, at: 0)
        for q in packets(pattern(300), at: 2000, ratio: 0) { _ = r.fragment(q.0, q.1, at: 0.001) }
        XCTAssertNil(r.requests(at: 1, rtt: 0.01, jitter: 0).skip)
        XCTAssertFalse(r.requests(at: 2, rtt: 0.01, jitter: 0).nack.isEmpty)
    }

    func testACopyOfSomethingDeliveredIsAnsweredWithTheAck() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        let p = packets(pattern(100), at: 0, ratio: 0)
        XCTAssertTrue(r.fragment(p[0].0, p[0].1, at: 0).ack)
        let again = r.fragment(p[0].0, p[0].1, at: 1) // the node's tail probe
        XCTAssertTrue(again.ack)
        XCTAssertTrue(again.messages.isEmpty)
    }

    func testBurstsWithinTheJitterAreNotTakenForLoss() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        let p = packets(pattern(5000), at: 0, ratio: 0)
        for q in p.prefix(3) { _ = r.fragment(q.0, q.1, at: 0) }
        XCTAssertTrue(r.requests(at: 0.008, rtt: 0.04, jitter: 0.005).nack.isEmpty) // quiet is 2 × 5 ms here
        XCTAssertFalse(r.requests(at: 0.011, rtt: 0.04, jitter: 0.005).nack.isEmpty)
    }

    /// A keyframe is never given up, even when the node marked it droppable (a node before it knew better): its first
    /// shard says it is one.
    func testAKeyframeIsNeverGivenUp() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        var body = [UInt8](repeating: 0, count: 13 + 3000)
        body[12] = 1 // the keyframe flag
        let key = ScreenVideoWire.frame(3, body: Data(body))
        let p = packets(key, at: 0, ratio: 0, droppable: true)
        _ = r.fragment(p[0].0, p[0].1, at: 0) // the first shard only
        for q in packets(pattern(300), at: UInt64(key.count), ratio: 0) { _ = r.fragment(q.0, q.1, at: 0.001) }
        XCTAssertNil(r.requests(at: 1, rtt: 0.01, jitter: 0).skip)
        XCTAssertEqual(r.givenUp, 0)
    }

    /// Even when a multi-block keyframe loses block 0's shard 0 and rebuilds it via Reed-Solomon parity while a later
    /// block is still incomplete, the receiver must recognize the keyframe header and never give it up.
    func testAMultiBlockKeyframeWithParityRebuiltFirstShardIsNeverGivenUp() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        var body = [UInt8](repeating: 0, count: 13 + 150_000)
        body[12] = 1 // the keyframe flag
        let key = ScreenVideoWire.frame(3, body: Data(body))
        let all = packets(key, at: 0, ratio: 1000, droppable: true)
        // Drop shard 0 of block 0 (parity rebuilds it) and all shards after shard 0 of block 1 (keeps message incomplete).
        for (h, d) in all {
            if h.block == 0, h.index == 0 { continue }
            if h.block >= 1, h.index > 0 { continue }
            _ = r.fragment(h, d, at: 0)
        }
        XCTAssertEqual(r.rebuilt, 1, "block 0 shard 0 should have been reconstructed from parity")
        for q in packets(pattern(300), at: UInt64(key.count), ratio: 0) { _ = r.fragment(q.0, q.1, at: 0.001) }
        XCTAssertNil(r.requests(at: 1, rtt: 0.01, jitter: 0).skip)
        XCTAssertEqual(r.givenUp, 0)
        XCTAssertFalse(r.requests(at: 2, rtt: 0.01, jitter: 0).nack.isEmpty)
    }

    /// After a reset, a lone partial frame at `expected` with no later frame buffered behind it is not blocking anything
    /// and must be recovered via NACK rather than falsely given up (`skip`).
    func testResetWithOnlyPartialMessageAtExpectedDoesNotTriggerFalseSkip() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        let first = packets(pattern(3000, seed: 1), at: 0, ratio: 0, droppable: true)
        let afterReset = packets(pattern(3000, seed: 2), at: 3000, ratio: 0, droppable: true)
        _ = r.fragment(first[0].0, first[0].1, at: 0)
        // Shard 0 of the post-reset frame arrives before the reset packet itself, with no later frame behind it.
        _ = r.fragment(afterReset[0].0, afterReset[0].1, at: 0.13)
        let d = r.reset(to: 3000, at: 0.14)
        XCTAssertTrue(d.messages.isEmpty)
        XCTAssertEqual(r.expected, 3000)

        let req = r.requests(at: 0.30, rtt: 0.01, jitter: 0)
        XCTAssertNil(req.skip, "a lone partial frame at expected with no later frame must not be skipped")
        XCTAssertEqual(r.givenUp, 0)
        XCTAssertFalse(req.nack.isEmpty, "missing shards of the lone frame should still be NACKed")

        // Once a later frame actually arrives behind it, give-up still applies after giveUp + rtt.
        for q in packets(pattern(400, seed: 3), at: 6000, ratio: 0, droppable: true) { _ = r.fragment(q.0, q.1, at: 0.31) }
        XCTAssertEqual(r.requests(at: 0.45, rtt: 0.01, jitter: 0).skip, 3000)
        XCTAssertEqual(r.givenUp, 1)
    }

    func testThePaceSettlesJustAboveABottleneckAndGrowsBelowOne() {
        var p = ScreenUDPPace()
        XCTAssertEqual(p.request(at: 0, start: 40_000), 40_000, "before any measurement: where the route starts")
        // Packets of one message arriving at 3 Mbit/s (1,200 bytes every 3.2 ms): past a 3 Mbit/s bottleneck.
        for i in 0 ..< 20 { p.packet(offset: 100, bytes: 1200, at: 1 + Double(i) * 0.0032) }
        XCTAssertEqual(Double(p.request(at: 1.1, start: 40_000)), 3750, accuracy: 60)
        // Gone after the window: the start again.
        XCTAssertEqual(p.request(at: 5, start: 40_000), 40_000)
        // A message too short to say anything.
        var q = ScreenUDPPace()
        for i in 0 ..< 5 { q.packet(offset: 7, bytes: 1200, at: Double(i) * 0.001) }
        XCTAssertEqual(q.request(at: 0.01, start: 8_000), 8_000)
    }

    func testLossCutsTheRateAndItComesBackSlowly() {
        var c = ScreenUDPCongestion()
        c.look(loss: 0.0, at: 0)
        XCTAssertEqual(c.scale, 1)
        c.look(loss: 0.4, at: 1)
        XCTAssertEqual(c.scale, 0.8, accuracy: 1e-9) // half the loss
        c.look(loss: 0.4, at: 2)
        XCTAssertEqual(c.scale, 0.8, accuracy: 1e-9, "at most every 2 s")
        c.look(loss: 0.4, at: 3)
        XCTAssertEqual(c.scale, 0.64, accuracy: 1e-9)
        c.look(loss: 0.05, at: 4)
        XCTAssertEqual(c.scale, 0.64, accuracy: 1e-9, "between the two: held")
        c.look(loss: 0.0, at: 5)
        XCTAssertEqual(c.scale, 0.64 * 1.05, accuracy: 1e-9)
        for t in 6 ..< 60 { c.look(loss: 0, at: Double(t)) }
        XCTAssertEqual(c.scale, 1, "back to the whole cap")
        for t in 60 ..< 200 where t % 2 == 0 { c.look(loss: 0.9, at: Double(t)) }
        XCTAssertEqual(c.scale, ScreenUDPCongestion.lowest, accuracy: 1e-9)
    }

    // MARK: Parity

    func testParityFollowsTheLossWithinTheCap() {
        XCTAssertEqual(ScreenUDPParity.ratio(loss: nil, cap: 4000, recentBlocks: []), 0)
        XCTAssertEqual(ScreenUDPParity.ratio(loss: 0.001, cap: 4000, recentBlocks: []), 0)
        XCTAssertEqual(ScreenUDPParity.ratio(loss: 0.01, cap: 4000, recentBlocks: []), 500)
        XCTAssertEqual(ScreenUDPParity.ratio(loss: 0.05, cap: 4000, recentBlocks: []), 1500)
        XCTAssertEqual(ScreenUDPParity.ratio(loss: 0.3, cap: 4000, recentBlocks: []), 4000)
        XCTAssertEqual(ScreenUDPParity.ratio(loss: 0.3, cap: 1500, recentBlocks: []), 1500)
    }

    /// On cellular at a low bitrate the frames are a few shards each: the extra parity shard per block would far pass
    /// the 15 % cap, so the ratio drops, to none when even one shard per block is too much.
    func testParityStaysWithinTheCapOnTheRecentFrames() {
        func share(_ r: Int, _ blocks: [Int]) -> Double {
            Double(blocks.map { ScreenUDPLayout.parity(data: $0, ratio: r) }.reduce(0, +)) / Double(blocks.reduce(0, +))
        }
        let small = [Int](repeating: 3, count: 60) // 1.5 Mbit/s at 60 fps: about three shards a frame
        XCTAssertEqual(ScreenUDPParity.ratio(loss: 0.05, cap: ScreenUDPParity.cellularCap, recentBlocks: small), 0)
        let large = [Int](repeating: 20, count: 30)
        let r = ScreenUDPParity.ratio(loss: 0.05, cap: ScreenUDPParity.cellularCap, recentBlocks: large)
        XCTAssertGreaterThan(r, 0)
        XCTAssertLessThanOrEqual(share(r, large), 0.15)
        let wifi = ScreenUDPParity.ratio(loss: 0.05, cap: ScreenUDPParity.wifiCap, recentBlocks: large)
        XCTAssertEqual(wifi, 1500)
        XCTAssertLessThanOrEqual(share(wifi, large), 0.40)
    }

    func testLossComesFromThePacketNumbers() {
        var l = ScreenUDPLoss()
        for n in 0 ..< 200 where n % 20 != 7 { l.received(UInt64(n), at: 0) }
        XCTAssertEqual(l.loss(at: 0) ?? -1, 0.05, accuracy: 0.001)
        XCTAssertNil(ScreenUDPLoss().loss(at: 0))
    }

    func testPacketAndLayoutBoundsRejectMalformedInputsWithoutTrapping() {
        let cipher = TestAESGCM(Data(count: 32))
        let iv = Data(count: 12)
        let flow = Data(count: 8)
        // Short/wrong-length IV or short packet must return empty/nil instead of trapping out of bounds.
        XCTAssertEqual(ScreenUDPPacket.nonce(iv: Data([1, 2, 3]), number: 7), Data())
        XCTAssertNil(ScreenUDPPacket.seal(Data([1]), cipher: cipher, iv: Data([1, 2]), flow: flow, number: 0))
        XCTAssertNil(ScreenUDPPacket.seal(Data([1]), cipher: cipher, iv: iv, flow: Data([1, 2]), number: 0))
        XCTAssertNil(ScreenUDPPacket.open(Data(), cipher: cipher, iv: iv, number: 0))
        XCTAssertNil(ScreenUDPPacket.open(Data(count: 10), cipher: cipher, iv: iv, number: 0))
        XCTAssertNil(ScreenUDPPacket.open(Data(count: 40), cipher: cipher, iv: Data([1]), number: 0))
        // Wrong version byte is rejected even on a long packet.
        var badVer = Data(count: 40)
        badVer[0] = 99
        XCTAssertNil(ScreenUDPPacket.open(badVer, cipher: cipher, iv: iv, number: 0))

        // Invalid Reed-Solomon shard counts (> blockShards or data+parity > 256) are rejected on decode.
        let tooManyData = ScreenUDPMessage.data(
            ScreenUDPFragmentHead(channel: 2, offset: 0, length: 100, block: 0, index: 0, data: 129, parity: 0, flags: 0),
            fragment: Data([1])
        ).encoded()
        XCTAssertThrowsError(try ScreenUDPMessage.decode(tooManyData, fromNode: true))

        let tooManyTotal = ScreenUDPMessage.data(
            ScreenUDPFragmentHead(channel: 2, offset: 0, length: 100, block: 0, index: 0, data: 128, parity: 129, flags: 0),
            fragment: Data([1])
        ).encoded()
        XCTAssertThrowsError(try ScreenUDPMessage.decode(tooManyTotal, fromNode: true))

        // Layout handles negative, zero, and extreme sizes without negative shardLength or Int overflow.
        XCTAssertTrue(ScreenUDPLayout.blocks(length: -5).isEmpty)
        XCTAssertEqual(ScreenUDPLayout.blocks(length: 0), [ScreenUDPLayout.Block(first: 0, data: 1, shardLength: 0)])
        XCTAssertEqual(ScreenUDPLayout.parity(data: 256, ratio: 4000), 0)
        XCTAssertEqual(ScreenUDPLayout.parity(data: Int.max, ratio: 4000), 0)
    }

    func testReceiverAndMetricsRejectOversizedLengthsOverflowsAndBackwardClocks() {
        var r = ScreenUDPReceiver(channel: 2, expected: 0)
        // Forged 4 GiB length, zero length, oversized fragment, and offset overflow must be ignored cleanly.
        let huge = ScreenUDPFragmentHead(channel: 2, offset: 0, length: UInt32.max, block: 0, index: 0, data: 128, parity: 0, flags: 0)
        XCTAssertTrue(r.fragment(huge, Data(count: 10), at: 0).messages.isEmpty)
        XCTAssertTrue(r.requests(at: 1.0, rtt: 0.01, jitter: 0).nack.isEmpty)

        let zeroLen = ScreenUDPFragmentHead(channel: 2, offset: 0, length: 0, block: 0, index: 0, data: 1, parity: 0, flags: 0)
        XCTAssertTrue(r.fragment(zeroLen, Data(), at: 0).messages.isEmpty)

        let oversizedShard = ScreenUDPFragmentHead(channel: 2, offset: 0, length: 10, block: 0, index: 0, data: 1, parity: 0, flags: 0)
        XCTAssertTrue(r.fragment(oversizedShard, Data(count: 100), at: 0).messages.isEmpty)

        var nearMax = ScreenUDPReceiver(channel: 2, expected: UInt64.max - 5)
        let overflowMsg = ScreenUDPFragmentHead(channel: 2, offset: UInt64.max - 5, length: 10, block: 0, index: 0, data: 1, parity: 0, flags: 0)
        XCTAssertTrue(nearMax.fragment(overflowMsg, Data(count: 10), at: 0).messages.isEmpty)

        // ScreenUDPLoss span `hi - lo == UInt64.max` must not overflow `hi - lo + 1`.
        var loss = ScreenUDPLoss()
        loss.received(0, at: 1)
        loss.received(UInt64.max, at: 1)
        XCTAssertNotNil(loss.loss(at: 1, window: 10, enough: 2))

        // ScreenUDPParity overhead & ratio must not trap on UInt64 overflow, +Inf/NaN loss, or huge recentBlocks.
        XCTAssertGreaterThan(ScreenUDPParity.overhead(dataBytes: 10, parityBytes: UInt64.max, resentBytes: UInt64.max), 1.0)
        XCTAssertEqual(ScreenUDPParity.ratio(loss: .infinity, cap: 4000, recentBlocks: []), 4000)
        XCTAssertEqual(ScreenUDPParity.ratio(loss: .nan, cap: 4000, recentBlocks: []), 0)
        XCTAssertEqual(ScreenUDPParity.ratio(loss: 0.05, cap: 4000, recentBlocks: [Int.max, Int.max]), 1500)

        // ScreenUDPPace handles extreme byte sums & tiny intervals without overflow or NaN traps.
        var pace = ScreenUDPPace()
        for i in 0 ..< 10 { pace.packet(offset: 1, bytes: Int.max / 2, at: 1.0 + Double(i) * 1e-9) }
        XCTAssertEqual(pace.request(at: 1.1, start: 10_000), ScreenUDPPace.highest)

        // ScreenUDPCongestion must not shrink scale on a backward clock during clear conditions.
        var cong = ScreenUDPCongestion()
        cong.look(loss: 0.4, at: 10)
        let afterCut = cong.scale
        cong.look(loss: 0.0, at: 5) // backward timestamp
        XCTAssertEqual(cong.scale, afterCut, accuracy: 1e-12)
        cong.look(loss: 0.0, at: 11) // forward from 10, not from 5
        XCTAssertEqual(cong.scale, afterCut * 1.05, accuracy: 1e-9)
    }
}
