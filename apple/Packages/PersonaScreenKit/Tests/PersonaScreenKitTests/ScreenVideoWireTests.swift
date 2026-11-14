import Foundation
import XCTest
@testable import PersonaScreenKit

final class ScreenVideoWireTests: XCTestCase {
    /// A representative message in each direction, for round-trip and streaming tests.
    private let samples: [ScreenVideoMessage] = [
        .hello(HelloMessage(version: 1, codec: "hevc", display: 2, width: 1920, height: 1080, rect: [0, 0, 1920, 1080])),
        .hello(HelloMessage(version: 1, codec: "h264", display: 0, width: 800, height: 600, rect: nil)),
        .hello(HelloMessage(version: 1, codec: "hevc", display: 1, width: 1920, height: 1080, features: ["tiers"])),
        .format(FormatMessage(parameterSets: [Data([1, 2, 3]), Data([4, 5]), Data()])),
        .frame(VideoFrame(number: 42, captureMicros: 1_000_000_007, flags: [.keyframe], sample: Data([0, 0, 0, 4, 9, 9, 9, 9]))),
        .frame(VideoFrame(number: 43, captureMicros: 2, flags: [.refinement], sample: Data())),
        .heartbeat(HeartbeatMessage(last: 43)),
        .paused(PausedMessage(reason: "locked")),
        .resumed,
        .timing(TimingMessage(captureMs: 0, encodeMs: 5.2, networkMs: 7.5, kbps: 1800, fps: 60)),
        .timing(TimingMessage(captureMs: nil, encodeMs: nil, networkMs: nil, kbps: 0, fps: 0)),
        .start(StartMessage(display: 1, maxSize: 1600, fps: 30, maxKbps: 12000, codecs: ["hevc", "h264"])),
        .start(StartMessage(display: nil, maxSize: 900, fps: 15, maxKbps: 1500, codecs: ["h264"])),
        .report(ReceiverReport(lastFrame: 42, captureMicros: 1_000_000_007, arrivalMicros: 1_000_050_000, bytesSinceLast: 40000, flags: [.decodingError])),
        .keyframe,
        .update(UpdateMessage(maxKbps: 4000, paused: true)),
        .update(UpdateMessage(display: 3)),
        .update(UpdateMessage(path: .init(kind: "tailscale", via: "cellular", minRttMs: 12.5))),
        // Quality tiers (#6396).
        .hello(HelloMessage(version: 1, codec: "hevc", display: 1, width: 2268, height: 1472, features: ["tiers"],
                            points: [1512, 982], pixels: [3024, 1964])),
        .start(StartMessage(display: nil, maxSize: 2868, fps: 60, maxKbps: 12_000, codecs: ["hevc"], scale: 1.5,
                            minKbps: 4_000, targetKbps: 6_000)),
        .update(UpdateMessage(fps: 30, maxKbps: 8_000, scale: 1.5, minKbps: 2_000, targetKbps: 3_000)),
        .timing(TimingMessage(captureMs: 0, encodeMs: 5, networkMs: 9, kbps: 2_900, fps: 30, targetKbps: 3_000, skipped: 2,
                              blockedMs: 12.5, pending: 1)),
        .encoding(EncodingMessage(width: 2268, height: 1472, fps: 30, scale: 1.5, minKbps: 2_000, maxKbps: 8_000)),
        .encoding(EncodingMessage(width: 1920, height: 1080, fps: 60, minKbps: 200, maxKbps: 12_000)),
        .unknown(type: 200, body: Data([7, 7, 7])),
    ]

    func testRoundTrip() throws {
        for message in samples {
            var parser = ScreenVideoParser()
            let raws = try parser.push(message.framed())
            XCTAssertEqual(raws.count, 1, "\(message)")
            let decoded = try ScreenVideoMessage.decode(type: raws[0].type, body: raws[0].body)
            XCTAssertEqual(decoded, message)
        }
    }

    func testStreamedByteByByte() throws {
        var all = Data()
        for message in samples {
            all.append(message.framed())
        }
        var parser = ScreenVideoParser()
        var decoded: [ScreenVideoMessage] = []
        for byte in all {
            for raw in try parser.push(Data([byte])) {
                decoded.append(try ScreenVideoMessage.decode(type: raw.type, body: raw.body))
            }
        }
        XCTAssertEqual(decoded, samples)
    }

    func testLengthPrefixIsBigEndian() {
        // A keyframe is just its type byte: length 1, big-endian.
        XCTAssertEqual(ScreenVideoMessage.keyframe.framed(), Data([0, 0, 0, 1, 18]))
    }

    func testFormatBodyLayout() {
        // u8 count, then (u16 length + bytes) each.
        let framed = ScreenVideoMessage.format(FormatMessage(parameterSets: [Data([0xAA]), Data([0xBB, 0xCC])])).framed()
        // length = 1 (type) + 1 (count) + (2+1) + (2+2) = 9
        XCTAssertEqual(Array(framed.prefix(5)), [0, 0, 0, 9, 2])
        // body: count 2, then (len 1, 0xAA), (len 2, 0xBB 0xCC)
        XCTAssertEqual(Array(framed.dropFirst(5)), [2, 0, 1, 0xAA, 0, 2, 0xBB, 0xCC])
    }

    func testTooLongThrows() {
        var parser = ScreenVideoParser()
        let header = Data([0x7F, 0xFF, 0xFF, 0xFF, 1]) // ~2 GB length
        XCTAssertThrowsError(try parser.push(header)) { error in
            XCTAssertEqual(error as? ScreenVideoError, .messageTooLong(0x7FFFFFFF))
        }
    }

    func testTruncatedFrameThrows() {
        // A `frame` type with a body too short for its fixed fields (needs 4+8+1 bytes).
        let body = Data([0, 0, 0, 1, 0, 0]) // 6 bytes
        XCTAssertThrowsError(try ScreenVideoMessage.decode(type: ScreenVideoWire.Kind.frame.rawValue, body: body)) { error in
            XCTAssertEqual(error as? ScreenVideoError, .truncated)
        }
    }

    func testBadJSONThrows() {
        let body = Data("not json".utf8)
        XCTAssertThrowsError(try ScreenVideoMessage.decode(type: ScreenVideoWire.Kind.hello.rawValue, body: body)) { error in
            XCTAssertEqual(error as? ScreenVideoError, .badJSON)
        }
    }

    /// A helper and an app from before #6396 keep working with these: the new keys are left out when unset, so an old
    /// side sees the JSON it knows, and JSON without them decodes with them nil.
    func testTierFieldsAreOptionalBothWays() throws {
        let old = Data(#"{"display":null,"max_size":1920,"fps":60,"max_kbps":12000,"codecs":["hevc"]}"#.utf8)
        guard case let .start(start) = try ScreenVideoMessage.decode(type: 16, body: old) else { return XCTFail("not start") }
        XCTAssertNil(start.scale)
        XCTAssertFalse(start.tiered)

        let hello = Data(#"{"version":1,"codec":"hevc","display":1,"width":1920,"height":1080}"#.utf8)
        guard case let .hello(h) = try ScreenVideoMessage.decode(type: 1, body: hello) else { return XCTFail("not hello") }
        XCTAssertNil(h.points)
        XCTAssertNil(h.pixels)

        let timing = Data(#"{"capture_ms":0,"encode_ms":5,"network_ms":7,"kbps":900,"fps":30}"#.utf8)
        guard case let .timing(t) = try ScreenVideoMessage.decode(type: 7, body: timing) else { return XCTFail("not timing") }
        XCTAssertNil(t.targetKbps)

        let body = ScreenVideoMessage.start(StartMessage(display: 1, maxSize: 1920, fps: 60, maxKbps: 12_000, codecs: ["hevc"]))
            .framed().dropFirst(5)
        let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertEqual(Set(object.keys), ["display", "max_size", "fps", "max_kbps", "codecs"], "nothing new for an old helper")
    }

    func testTierFieldsUseSnakeCase() throws {
        let update = ScreenVideoMessage.update(UpdateMessage(minKbps: 2_000, targetKbps: 3_000)).framed().dropFirst(5)
        let u = try XCTUnwrap(try JSONSerialization.jsonObject(with: update) as? [String: Any])
        XCTAssertEqual(u["min_kbps"] as? Int, 2_000)
        XCTAssertEqual(u["target_kbps"] as? Int, 3_000)
        let encoding = ScreenVideoMessage.encoding(EncodingMessage(width: 2, height: 2, fps: 1, minKbps: 200, maxKbps: 300))
        XCTAssertEqual(encoding.framed()[4], 8, "type 8")
        let timing = ScreenVideoMessage.timing(TimingMessage(captureMs: nil, encodeMs: nil, networkMs: nil, kbps: 0, fps: 0,
                                                             targetKbps: 1, skipped: 0, blockedMs: 0, pending: 0))
        let t = try XCTUnwrap(try JSONSerialization.jsonObject(with: timing.framed().dropFirst(5)) as? [String: Any])
        XCTAssertNotNil(t["target_kbps"])
        XCTAssertNotNil(t["blocked_ms"])
    }

    func testStartJSONUsesSnakeCase() throws {
        let framed = ScreenVideoMessage.start(StartMessage(display: 1, maxSize: 1600, fps: 30, maxKbps: 12000, codecs: ["hevc"])).framed()
        let body = framed.dropFirst(5) // past length + type
        let object = try JSONSerialization.jsonObject(with: body) as? [String: Any]
        XCTAssertEqual(object?["max_size"] as? Int, 1600)
        XCTAssertEqual(object?["max_kbps"] as? Int, 12000)
        XCTAssertNil(object?["maxSize"])
    }

    /// `hello.features` (#6396) is new: an older helper's hello has none and reads as nil, and nil stays off the wire.
    func testHelloFeaturesAreOptional() throws {
        let old = ScreenVideoWire.frame(ScreenVideoWire.Kind.hello.rawValue,
                                        body: Data(#"{"version":1,"codec":"hevc","display":0,"width":800,"height":600}"#.utf8))
        var parser = ScreenVideoParser()
        let raw = try XCTUnwrap(try parser.push(old).first)
        guard case let .hello(h) = try ScreenVideoMessage.decode(type: raw.type, body: raw.body) else { return XCTFail("not a hello") }
        XCTAssertNil(h.features)
        let body = ScreenVideoMessage.hello(h).framed().dropFirst(5)
        let object = try JSONSerialization.jsonObject(with: body) as? [String: Any]
        XCTAssertNil(object?["features"])
    }
}
