import Foundation
import Testing
@testable import Persona
import PersonaWire

/// What an inbox item can be muted by, as the hub lists it (`mutes`), and the menu's words for it.
struct InboxMuteModelTests {
    private func item(_ json: String) throws -> InboxItem {
        try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    @Test func mutesDecodeAndReadForTheMenu() throws {
        let it = try item(#"{"id":"github:1","source":"github","kind":"mention","account":"github","account_name":"GitHub","title":"x","preview":"","count":1,"mutes":[{"tag":"repo","value":"example-org/example-repo","name":"example-org/example-repo"},{"tag":"sender","value":"alice","name":"alice"}]}"#)
        let m = try #require(it.mutes)
        #expect(m.map(\.title) == ["静音这个仓库", "静音这个发件人"])
        #expect(m.map(\.shortName) == ["example-repo", "alice"]) // a repository without its owner: one line in the menu
    }

    @Test func anOlderHubSaysNothing() throws {
        let it = try item(#"{"id":"mail:1","source":"mail","kind":"mail","account":"a","account_name":"a","title":"x","preview":"","count":1}"#)
        #expect(it.mutes == nil)
    }

    @Test func aMuteReadsItsKind() throws {
        let m = try JSON.decoder.decode(InboxMute.self, from: Data(#"{"id":"m1","source":"telegram","source_name":"Telegram","tag":"chat","value":"u1","name":"示例群","created_at":"2026-11-03T01:00:00Z"}"#.utf8))
        #expect(m.tagName == "会话")
        #expect(m.createdAt != nil)
    }
}
