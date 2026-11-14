import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Session Info's context usage (#3289, the web's #2430): what the session carries and the share the bar draws.
struct ContextUsageTests {
    private func session(_ extra: String) throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"s1","status":"idle","state":"idle"\#(extra)}"#.utf8))
    }

    @Test func theSessionCarriesTheNodesNumbers() throws {
        let s = try session(#","context_tokens":172000,"context_window":200000"#)
        #expect(s.contextTokens == 172_000 && s.contextWindow == 200_000)
        let none = try session("")
        #expect(none.contextTokens == nil && none.contextWindow == nil)
        // An odd value drops the number, never the session.
        let odd = try session(#","context_tokens":"lots","context_window":200000"#)
        #expect(odd.contextTokens == nil && odd.contextWindow == 200_000)
    }

    @Test func theShareAndTheWarning() throws {
        let u = try #require(ContextUsage(try session(#","context_tokens":172000,"context_window":200000"#)))
        #expect(u.used == 86 && u.left == 14 && u.warns)
        #expect(u.label == "还剩 14%")
        let calm = try #require(ContextUsage(tokens: 31_000, window: 200_000))
        #expect(calm.used == 16 && !calm.warns)
        #expect(try #require(ContextUsage(tokens: 160_000, window: 200_000)).warns) // 80% used is where it warns
        #expect(!(try #require(ContextUsage(tokens: 158_000, window: 200_000)).warns))
        #expect(try #require(ContextUsage(tokens: 300_000, window: 200_000)).used == 100) // past the window: full
    }

    @Test func unknownWindowsDrawNothing() {
        #expect(ContextUsage(tokens: 48_000, window: nil) == nil)
        #expect(ContextUsage(tokens: 0, window: 200_000) == nil)
        #expect(ContextUsage(tokens: nil, window: nil) == nil)
        #expect(ContextUsage(nil) == nil)
    }

    @Test func shortNumbers() {
        #expect(ContextUsage.short(172_000) == "172K")
        #expect(ContextUsage.short(1_000_000) == "1M")
        #expect(ContextUsage.short(1_260_000) == "1.3M")
        #expect(ContextUsage.short(800) == "800")
        // Values that round up to the next unit move to it.
        #expect(ContextUsage.short(999) == "999")
        #expect(ContextUsage.short(999_499) == "999K")
        #expect(ContextUsage.short(999_500) == "1M")
        #expect(ContextUsage.short(999_600) == "1M")
        #expect(ContextUsage.short(1_960_000) == "2M")
    }
}
