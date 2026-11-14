import Foundation
import XCTest
@testable import PersonaScreenKit

/// The video wire's shared cases (spec/fixtures/screen-video, #6558): the browser's client
/// (web/src/screen/video-wire.ts) frames every message as these do. The cases are written from the messages below:
/// `PERSONA_WRITE_VIDEO_FIXTURES=1 swift test --filter ScreenVideoFixtureTests` writes the file; otherwise the committed
/// file must be exactly what they write now (a change on this side fails until the file and the browser's side follow),
/// and every case decodes back to its message.
final class ScreenVideoFixtureTests: XCTestCase {
    private struct Sample {
        let name: String
        let description: String
        let message: ScreenVideoMessage
    }

    private static let samples: [Sample] = [
        Sample(name: "hello_tiers", description: "A helper with quality tiers: the display's rectangle, points and pixels.",
               message: .hello(HelloMessage(version: 1, codec: "hevc", display: 2, width: 1920, height: 1080,
                                            rect: [0, 0, 1920, 1080], features: ["tiers"], points: [1512, 982],
                                            pixels: [3024, 1964]))),
        Sample(name: "hello_old", description: "A helper from before tiers, H.264: no rect, features, points or pixels.",
               message: .hello(HelloMessage(version: 1, codec: "h264", display: 0, width: 800, height: 600))),
        Sample(name: "format_hevc", description: "HEVC's VPS, SPS and PPS (made-up bytes).",
               message: .format(FormatMessage(parameterSets: [Data([0x40, 0x01, 0x0c]), Data([0x42, 0x01, 0x01, 0x01]),
                                                               Data([0x44, 0x01, 0xc1])]))),
        Sample(name: "format_h264", description: "H.264's SPS and PPS (made-up bytes).",
               message: .format(FormatMessage(parameterSets: [Data([0x67, 0x64, 0x00, 0x1f]), Data([0x68, 0xee, 0x3c, 0x80])]))),
        Sample(name: "frame_keyframe", description: "A keyframe: one length-prefixed NAL unit.",
               message: .frame(VideoFrame(number: 42, captureMicros: 1_000_000_007, flags: [.keyframe],
                                          sample: Data([0, 0, 0, 4, 0x26, 0x01, 0xaf, 0x09])))),
        Sample(name: "frame_refinement", description: "An idle refinement frame, sent on purpose (not congestion).",
               message: .frame(VideoFrame(number: 43, captureMicros: 1_000_033_340, flags: [.refinement],
                                          sample: Data([0, 0, 0, 2, 0x02, 0x01])))),
        Sample(name: "heartbeat", description: "A second with no frame: the last frame's number.",
               message: .heartbeat(HeartbeatMessage(last: 43))),
        Sample(name: "paused_locked", description: "Capture stopped: the Mac is locked.",
               message: .paused(PausedMessage(reason: "locked"))),
        Sample(name: "resumed", description: "Capture goes on; hello, format and a keyframe follow.", message: .resumed),
        Sample(name: "timing_full", description: "A second of frames, with the helper's own adaptation (#6396).",
               message: .timing(TimingMessage(captureMs: 0, encodeMs: 5.5, networkMs: 7.25, kbps: 1800, fps: 60,
                                              targetKbps: 4000, skipped: 0, blockedMs: 12.5, pending: 2))),
        Sample(name: "timing_old", description: "A helper before the segments were measured: rate and frame count only.",
               message: .timing(TimingMessage(captureMs: nil, encodeMs: nil, networkMs: nil, kbps: 0, fps: 0))),
        Sample(name: "encoding", description: "What the encoder runs at after a tier change.",
               message: .encoding(EncodingMessage(width: 1600, height: 1000, fps: 30, scale: 1.5, minKbps: 1200,
                                                  maxKbps: 6000))),
        Sample(name: "start_tiered", description: "A client with tiers: the band and the target inside it.",
               message: .start(StartMessage(display: 1, maxSize: 1600, fps: 30, maxKbps: 12000, codecs: ["hevc", "h264"],
                                            scale: 1.5, minKbps: 1200, targetKbps: 4000))),
        Sample(name: "start_h264_only", description: "A browser that decodes only H.264, on the main display, untiered.",
               message: .start(StartMessage(display: nil, maxSize: 1920, fps: 60, maxKbps: 4000, codecs: ["h264"]))),
        Sample(name: "report", description: "How a frame arrived; it failed to decode.",
               message: .report(ReceiverReport(lastFrame: 42, captureMicros: 1_000_000_007, arrivalMicros: 1_000_050_000,
                                               bytesSinceLast: 40000, flags: [.decodingError]))),
        Sample(name: "keyframe", description: "Ask for a keyframe.", message: .keyframe),
        Sample(name: "update_band", description: "Another tier: scale, band and target.",
               message: .update(UpdateMessage(fps: 30, maxKbps: 6000, scale: 1.0, minKbps: 800, targetKbps: 2500))),
        Sample(name: "update_paused", description: "Pause capture (the client's page went to the background).",
               message: .update(UpdateMessage(paused: true))),
        Sample(name: "update_path", description: "The route changed (#6398).",
               message: .update(UpdateMessage(path: .init(kind: "relay", via: "wifi", minRttMs: 18.5)))),
        Sample(name: "unknown_type", description: "A type this side doesn't know: skipped by its length.",
               message: .unknown(type: 200, body: Data([7, 7, 7]))),
    ]

    private static var file: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent() // the package
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent() // the repository
            .appendingPathComponent("spec/fixtures/screen-video/messages.json")
    }

    func testTheSharedCases() throws {
        let written = try Self.document()
        if ProcessInfo.processInfo.environment["PERSONA_WRITE_VIDEO_FIXTURES"] == "1" {
            try written.write(to: Self.file)
            return
        }
        let committed = try Data(contentsOf: Self.file)
        XCTAssertEqual(String(decoding: committed, as: UTF8.self), String(decoding: written, as: UTF8.self),
                       "spec/fixtures/screen-video is out of date: run PERSONA_WRITE_VIDEO_FIXTURES=1 swift test --filter ScreenVideoFixtureTests, and bring web/src/screen/video-wire.ts along")
        for sample in Self.samples {
            var parser = ScreenVideoParser()
            let raws = try parser.push(try Self.canonical(sample.message))
            XCTAssertEqual(raws.count, 1, sample.name)
            XCTAssertEqual(try ScreenVideoMessage.decode(type: raws[0].type, body: raws[0].body), sample.message, sample.name)
        }
    }

    /// The file: `{"version": 1, "cases": [...]}`, keys sorted, two-space indents.
    private static func document() throws -> Data {
        let cases = try samples.map { s -> [String: Any] in
            let framed = try Self.canonical(s.message)
            let (expected, exact) = try Self.expected(s.message)
            return ["name": s.name, "description": s.description, "input": ["frame_hex": framed.hex],
                    "expected": expected, "exact": exact]
        }
        var data = try JSONSerialization.data(withJSONObject: ["version": 1, "cases": cases],
                                              options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes])
        data.append(0x0a)
        return data
    }

    /// The message framed as the file has it: as `framed()` does, but a JSON body with its keys sorted, since
    /// `JSONEncoder` orders them differently from run to run (JSON cases are not byte-exact anyway).
    private static func canonical(_ m: ScreenVideoMessage) throws -> Data {
        let framed = m.framed()
        guard try expected(m).1 == false else { return framed }
        let body = framed.dropFirst(5)
        let object = try JSONSerialization.jsonObject(with: body)
        let sorted = try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys, .withoutEscapingSlashes])
        return ScreenVideoWire.frame(framed[framed.startIndex + 4], body: sorted)
    }

    /// What a case says the message is: the JSON body's own fields for JSON messages (`exact: false`, since an encoder may
    /// order and space them differently), named fields for the binary ones (`exact: true`: byte for byte both ways).
    private static func expected(_ m: ScreenVideoMessage) throws -> ([String: Any], Bool) {
        func json(_ type: String) throws -> ([String: Any], Bool) {
            var body = m.framed()
            body.removeFirst(5) // past length + type
            var object = try JSONSerialization.jsonObject(with: body) as? [String: Any] ?? [:]
            object["type"] = type
            return (object, false)
        }
        switch m {
        case .hello: return try json("hello")
        case let .format(f): return (["type": "format", "parameter_sets_hex": f.parameterSets.map(\.hex)], true)
        case let .frame(f):
            return (["type": "frame", "number": Int(f.number), "capture_micros": Int(f.captureMicros),
                     "flags": Int(f.flags.rawValue), "sample_hex": f.sample.hex], true)
        case .heartbeat: return try json("heartbeat")
        case .paused: return try json("paused")
        case .resumed: return (["type": "resumed"], true)
        case .timing: return try json("timing")
        case .encoding: return try json("encoding")
        case .start: return try json("start")
        case let .report(r):
            return (["type": "report", "last_frame": Int(r.lastFrame), "capture_micros": Int(r.captureMicros),
                     "arrival_micros": Int(r.arrivalMicros), "bytes_since_last": Int(r.bytesSinceLast),
                     "flags": Int(r.flags.rawValue)], true)
        case .keyframe: return (["type": "keyframe"], true)
        case .update: return try json("update")
        case let .unknown(type, body): return (["type": "unknown", "code": Int(type), "body_hex": body.hex], true)
        }
    }
}

private extension Data {
    var hex: String { map { String(format: "%02x", $0) }.joined() }
}
