import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Which Agents get the Terminal button over the composer (#6170): a CLI in a terminal pane the node has no reader for.
/// The node marks those with `read_only_reason` (server/internal/node sessions.go `otherAgent`, pi.go, opencode.go).
struct TerminalOnlyAgentTests {
    private func session(_ extra: String = "") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"s1","status":"working","state":"working"\#(extra)}"#.utf8))
    }
    private let reason = #","read_only_reason":"no reader""#

    @Test func anAgentTheNodeCannotReadInATerminalIsTerminalOnly() throws {
        for agent in ["kiro", "gemini", "pi", "omp", "opencode"] {
            #expect(try session(#","agent":"\#(agent)""# + reason).isTerminalOnly, "\(agent) without a reader")
        }
    }

    @Test func piAndOpencodeWithTheirExtensionKeepTheirChat() throws {
        // The node leaves read_only_reason empty once the extension / plugin serves the pane: it takes messages.
        for agent in ["pi", "omp", "opencode", "kiro"] {
            #expect(!(try session(#","agent":"\#(agent)""#).isTerminalOnly), "\(agent) with a reader")
        }
    }

    @Test func otherSessionsKeepTheMenuEntry() throws {
        #expect(!(try session(reason).isTerminalOnly)) // an older hub names no agent: Claude Code
        for agent in ["claude", "codex", "traex"] {
            #expect(!(try session(#","agent":"\#(agent)""# + reason).isTerminalOnly), "\(agent)")
        }
        #expect(!(try session(#","agent":"kiro","assistant":true"#+reason).isTerminalOnly))
        // A runtime's session (pi-web, cindy, paseo…) has its own chat.
        #expect(!(try session(#","agent":"pi","source_kind":"runtime""#+reason).isTerminalOnly))
        #expect(!(try session(#","agent":"kiro","capabilities":{"read":true}"#+reason).isTerminalOnly)) // no terminal
    }
}
