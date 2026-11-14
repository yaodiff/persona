import Foundation
import XCTest
@testable import PersonaScreenKit

/// The iPhone's client frames every session message as the node does: spec/fixtures/screen-link, also checked by
/// server/internal/screenlink.
final class ScreenLinkWireTests: XCTestCase {
    private struct Doc: Decodable {
        struct Case: Decodable {
            struct Input: Decodable { var frame_hex: String }
            var name: String
            var input: Input
            var expected: [String: JSONValue]
            var exact: Bool
        }
        var version: Int
        var cases: [Case]
    }

    /// Just enough JSON to read the fixture's `expected`.
    private enum JSONValue: Decodable, Equatable {
        case number(Double), string(String), array([JSONValue]), object([String: JSONValue]), bool(Bool), null

        init(from decoder: Decoder) throws {
            let c = try decoder.singleValueContainer()
            if c.decodeNil() { self = .null } else if let b = try? c.decode(Bool.self) { self = .bool(b) }
            else if let n = try? c.decode(Double.self) { self = .number(n) }
            else if let s = try? c.decode(String.self) { self = .string(s) }
            else if let a = try? c.decode([JSONValue].self) { self = .array(a) }
            else { self = .object(try c.decode([String: JSONValue].self)) }
        }

        var number: Double? { if case let .number(n) = self { n } else { nil } }
        var string: String? { if case let .string(s) = self { s } else { nil } }
        var array: [JSONValue]? { if case let .array(a) = self { a } else { nil } }
    }

    private func loadCases() throws -> [Doc.Case] {
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent() // the package
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent() // the repository
            .appendingPathComponent("spec/fixtures/screen-link/messages.json")
        let doc = try JSONDecoder().decode(Doc.self, from: Data(contentsOf: url))
        XCTAssertEqual(doc.version, 1)
        XCTAssertFalse(doc.cases.isEmpty)
        return doc.cases
    }

    func testTheSharedCases() throws {
        for c in try loadCases() {
            let bytes = Data(hex: c.input.frame_hex)
            var parser = ScreenVideoParser()
            let raws = try parser.push(bytes)
            XCTAssertEqual(raws.count, 1, c.name)
            let message = try ScreenLinkMessage.decode(type: raws[0].type, body: raws[0].body)
            let want = try expected(c.expected, name: c.name)
            XCTAssertEqual(message, want, c.name)
            if c.exact {
                XCTAssertEqual(want.framed(), bytes, c.name)
            } else {
                // A JSON body: what this side encodes decodes to the same message.
                var again = ScreenVideoParser()
                let raw = try again.push(want.framed())[0]
                XCTAssertEqual(try ScreenLinkMessage.decode(type: raw.type, body: raw.body), want, c.name)
            }
        }
    }

    func testShortBodiesThrow() {
        XCTAssertThrowsError(try ScreenLinkMessage.decode(type: 1, body: Data([1, 0, 0])))
        XCTAssertThrowsError(try ScreenLinkMessage.decode(type: 8, body: Data(count: 10)))
        XCTAssertThrowsError(try ScreenLinkMessage.decode(type: 7, body: Data(count: 12)))
        XCTAssertThrowsError(try ScreenLinkMessage.decode(type: 3, body: Data("{}".utf8)))
    }

    private func expected(_ e: [String: JSONValue], name: String) throws -> ScreenLinkMessage {
        func n(_ k: String) -> UInt64 { UInt64(e[k]?.number ?? -1) }
        func ch() -> UInt8 { UInt8(n("channel")) }
        func entries() -> [ScreenLinkOffset] {
            (e["entries"]?.array ?? []).map { pair in
                let p = pair.array ?? []
                return ScreenLinkOffset(UInt8(p[0].number ?? 0), UInt64(p[1].number ?? 0))
            }
        }
        switch e["type"]?.string {
        case "data": return .data(channel: ch(), offset: n("offset"), bytes: Data(hex: e["bytes_hex"]?.string ?? ""))
        case "ack": return .ack(channel: ch(), offset: n("offset"))
        case "close": return .close(channel: ch(), offset: n("offset"))
        case "reset": return .reset(channel: ch(), offset: n("offset"))
        case "use": return .use(entries())
        case "resumed": return .resumed(entries())
        case "ping": return .ping(n("value"))
        case "pong": return .pong(n("value"), retransmitted: UInt32(n("retransmitted")), sent: UInt32(n("sent")))
        case "attached": return .attached
        case "attach": return .attach(token: e["token"]?.string ?? "")
        case "unknown": return .unknown(type: UInt8(n("code")), body: Data([0xab]))
        case "paths":
            guard case let .object(p)? = e["paths"] else { break }
            let cands = (p["candidates"]?.array ?? []).map { v -> ScreenLinkPaths.Candidate in
                guard case let .object(c) = v else { return .init(kind: "", addr: "") }
                return .init(kind: c["kind"]?.string ?? "", addr: c["addr"]?.string ?? "", prefix: c["prefix"]?.number.map { Int($0) })
            }
            return .paths(ScreenLinkPaths(version: Int(p["version"]?.number ?? 0), token: p["token"]?.string ?? "",
                                          cert: p["cert"]?.string, port: p["port"]?.number.map { Int($0) }, candidates: cands,
                                          unavailable: p["unavailable"]?.string))
        default: break
        }
        XCTFail("\(name): no message for \(e)")
        return .attached
    }
}

extension Data {
    init(hex: String) {
        var out = Data()
        var i = hex.startIndex
        while i < hex.endIndex, let j = hex.index(i, offsetBy: 2, limitedBy: hex.endIndex), let b = UInt8(hex[i..<j], radix: 16) {
            out.append(b)
            i = j
        }
        self = out
    }
}
