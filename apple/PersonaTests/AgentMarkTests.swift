import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Which mark an agent's row draws and whether it carries the command line's </> badge (#6390), read off the session
/// as the hub sends it.
struct AgentMarkTests {
    private static func session(_ fields: String) -> Session {
        let json = #"{"machine_id":"m","session_id":"s","title":"t","cwd":"/w","status":"idle","state":"idle"\#(fields)}"#
        return try! JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    @Test func claudeAndAnOlderHubAreClaudesMark() {
        for fields in ["", #","agent":"claude""#, #","agent":"""#] {
            let k = AgentKind(Self.session(fields))
            #expect(k.brand == Theme.Brand.claude && k.name == "Claude Code", "\(fields)")
        }
    }

    @Test func codexFromATerminalOrItsAppIsOpenAIsMark() {
        for fields in [#","agent":"codex""#, #","agent":"claude","source_kind":"codex_history""#, #","source_kind":"codex_app_server""#] {
            let k = AgentKind(Self.session(fields))
            #expect(k.brand == Theme.Brand.openAI && k.name == "Codex", "\(fields)")
        }
    }

    @Test func traeHasItsMarkAndTraexItsLetter() {
        #expect(AgentKind(Self.session(#","agent":"trae""#)).brand == Theme.Brand.trae)
        #expect(AgentKind(Self.session(#","agent":"trae-cli""#)).brand == Theme.Brand.trae)
        let traex = AgentKind(Self.session(#","agent":"traex""#))
        #expect(traex.brand == nil && traex.letter == "T" && traex.name == "TRAEX")
        let dsh = AgentKind(Self.session(#","agent":"dsh","agent_label":"DSH""#))
        #expect(dsh.brand == nil && dsh.letter == "D")
    }

    @Test func theClientComesFromAgentSurface() {
        #expect(AgentKind(Self.session(#","agent":"claude","agent_surface":"cli""#)).surface == .cli)
        #expect(AgentKind(Self.session(#","agent":"codex","agent_surface":"desktop""#)).surface == .desktop)
        // A client the app does not know, and an older node that says nothing: no badge.
        #expect(AgentKind(Self.session(#","agent":"codex","agent_surface":"sdk""#)).surface == nil)
        #expect(AgentKind(Self.session(#","agent":"codex""#)).surface == nil)
    }

    @Test func theMarkSaysTheClient() {
        #expect(AgentKind(brand: nil, name: "Codex", surface: nil).spoken == "Codex")
        #expect(AgentKind(brand: nil, name: "Codex", surface: .cli).spoken != "Codex")
        #expect(AgentKind(brand: nil, name: "Codex", surface: .desktop).spoken != AgentKind(brand: nil, name: "Codex", surface: .cli).spoken)
    }
}
