import Foundation
import Testing
@testable import Persona
import PersonaWire

/// `Interaction.muted` (#4360) is optional on the wire: the hub sends it `omitempty`, older hubs not at all, and the
/// app's own snapshot cache and watch transfer encode and decode cards again (#4763).
struct InteractionMutedCodingTests {
    private let loud = #"{"id":"ix1","machine_id":"m","session_id":"s","kind":"permission","tool_name":"Bash"}"#

    @Test func aCardWithoutMutedDecodesAsLoud() throws {
        let ix = try JSON.decoder.decode(Interaction.self, from: Data(loud.utf8))
        #expect(ix.id == "ix1" && ix.kind == .permission && ix.toolName == "Bash")
        #expect(!ix.muted)
        // The list of GET /api/v1/interactions, a loud card next to a muted one.
        let list = try JSON.decoder.decode([Interaction].self, from: Data("""
        [\(loud),{"id":"ix2","machine_id":"m","session_id":"s","kind":"question","muted":true}]
        """.utf8))
        #expect(list.map(\.muted) == [false, true])
    }

    @Test func mutedDecodesAsSent() throws {
        let on = try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"x","machine_id":"m","session_id":"s","kind":"question","muted":true}"#.utf8))
        #expect(on.muted)
        let off = try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"x","machine_id":"m","session_id":"s","kind":"question","muted":false}"#.utf8))
        #expect(!off.muted)
    }

    @Test func encodingAndDecodingAgainKeepsTheCard() throws {
        var ix = try JSON.decoder.decode(Interaction.self, from: Data(loud.utf8))
        ix.group = "g2"
        let again = try JSON.decoder.decode(Interaction.self, from: JSON.encoder.encode(ix))
        #expect(again == ix && !again.muted && again.group == "g2")
        // A loud card goes out the way the hub sends it: no key at all.
        let object = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(ix)) as? [String: Any])
        #expect(object["muted"] == nil)

        ix.muted = true
        let mutedAgain = try JSON.decoder.decode(Interaction.self, from: JSON.encoder.encode(ix))
        #expect(mutedAgain == ix && mutedAgain.muted)
    }
}
