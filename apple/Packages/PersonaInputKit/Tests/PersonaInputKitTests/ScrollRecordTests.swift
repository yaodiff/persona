import Foundation
import Testing
@testable import PersonaInputKit

/// The pixel scroll record against the shared fixture (spec/fixtures/screen-input/records.json, also read by the node),
/// and a stream of records in pieces.
struct ScrollRecordTests {
    struct Fixture: Decodable {
        struct Valid: Decodable { let name, hex: String; let kind, phase, momentum: UInt8; let dy, dx: Float }
        struct Invalid: Decodable { let name, hex, error: String }
        let valid: [Valid]
        let invalid: [Invalid]
    }

    private func fixture() throws -> Fixture {
        // Tests/PersonaInputKitTests → the repository's spec/fixtures/screen-input.
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .appendingPathComponent("../../../../../spec/fixtures/screen-input/records.json").standardizedFileURL
        return try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: url))
    }

    private func bytes(_ hex: String) -> [UInt8] {
        stride(from: 0, to: hex.count, by: 2).map { i in
            let a = hex.index(hex.startIndex, offsetBy: i)
            return UInt8(hex[a..<hex.index(a, offsetBy: 2)], radix: 16)!
        }
    }

    @Test func validRecordsDecodeAndEncodeBack() throws {
        let f = try fixture()
        #expect(f.valid.count >= 9)
        for v in f.valid {
            let r = try ScrollRecord.decode(bytes(v.hex))
            #expect(r.kind.rawValue == v.kind && r.phase.rawValue == v.phase && r.momentum.rawValue == v.momentum, "\(v.name)")
            #expect(r.dy == v.dy && r.dx == v.dx, "\(v.name)")
            // Encoding writes the reserved bytes as zero; otherwise byte for byte.
            var expected = bytes(v.hex)
            expected[5] = 0; expected[6] = 0; expected[7] = 0
            #expect(r.encoded() == expected, "\(v.name)")
        }
        #expect(ScrollRecord.hello.encoded() == bytes(f.valid[0].hex))
    }

    @Test func invalidRecordsAreRefusedByTheRightCheck() throws {
        for v in try fixture().invalid {
            var which = ""
            do {
                _ = try ScrollRecord.decode(bytes(v.hex))
            } catch let e as ScrollRecord.DecodeError {
                switch e {
                case .length: which = "length"
                case .type: which = "type"
                case .version: which = "version"
                case .kind: which = "kind"
                case .phase: which = "phase"
                case .momentum: which = "momentum"
                }
            }
            #expect(which == v.error, "\(v.name)")
        }
    }

    /// Records split anywhere, several in one piece, or one in many: each comes out once, whole, in order.
    @Test func aStreamInPiecesGivesEachRecordOnce() throws {
        let records = try fixture().valid.map { try ScrollRecord.decode(bytes($0.hex)) }
        let stream = records.flatMap { $0.encoded() }
        for cut in [1, 3, 7, 15, 16, 17, 40, stream.count] {
            var reader = ScrollRecordReader()
            var got: [ScrollRecord] = []
            var at = 0
            while at < stream.count {
                got += try reader.push(stream[at..<min(at + cut, stream.count)])
                at += cut
            }
            #expect(got == records, "pieces of \(cut)")
        }
    }
}
