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

    @Test func traeAndTraeCLIKeepTheirExistingMarks() {
        #expect(AgentKind(Self.session(#","agent":"trae""#)).brand == Theme.Brand.trae)
        for id in ["traex", "traecli", "trae-cli"] {
            let session = Self.session(",\"agent\":\"\(id)\"")
            let mark = AgentKind(session)
            #expect(session.agentLabel == "TRAE CLI" && mark.name == "TRAE CLI" && mark.letter == "T")
            #expect(mark.brand == Theme.Brand.trae)
        }
        let dsh = AgentKind(Self.session(#","agent":"dsh","agent_label":"DSH""#))
        #expect(dsh.brand == Theme.Brand.dsh)
    }

    @Test func namesTheBuiltinLaunchersAndKeepsCustomTitles() {
        #expect(Launcher(name: "traex", baseURL: "(traex)").title == "TRAE CLI")
        for displayName in [nil, "TRAEX", "TraeCLI"] as [String?] {
            let launcher = Launcher(name: "traex", displayName: displayName, agent: "traex", source: "path")
            #expect(launcher.title == "TRAE CLI")
            #expect(LauncherChoices.shown([launcher], query: "TRAE CLI") == [launcher])
            let stored = LauncherView(name: "traex", source: "path", displayName: displayName, agent: "traex")
            #expect(stored.title == "TRAE CLI")
        }
        #expect(Launcher(name: "traex", displayName: "My CLI", agent: "traex").title == "My CLI")
        #expect(Launcher(name: "team-cli", agent: "traex").title == "team-cli")
        for (id, expected) in [
            ("claude", "Claude Code"), ("codex", "Codex"),
            ("claude-desktop", "Claude Desktop"), ("codex-app", "Codex App"),
            ("agy", "Antigravity CLI"), ("omp", "Oh My Pi"),
            ("cursor", "Cursor"), ("gemini", "Gemini CLI"),
        ] {
            #expect(Launcher(name: id).title == expected)
            #expect(Launcher(name: id, displayName: id).title == expected)
            #expect(Launcher(name: id, displayName: "Custom Name").title == "Custom Name")
            #expect(LauncherView(name: id, source: "zshrc").title == expected)
        }
    }

    @Test func namesPaseoLaunchersByProductOrCustomTitle() {
        for id in ["traex", "traecli", "trae-cli"] {
            for displayName in [nil, "TRAEX", "TraeCLI", "My CLI"] as [String?] {
                let title = displayName == "My CLI" ? "My CLI" : "TRAE CLI"
                let launcher = Launcher(name: id, displayName: displayName, agent: "paseo", source: "paseo")
                #expect(launcher.title == title)
                #expect(LauncherChoices.shown([launcher], query: title) == [launcher])
                let stored = LauncherView(name: id, source: "paseo", displayName: displayName, agent: "paseo")
                #expect(stored.title == title)
            }
        }
    }

    @MainActor @Test func sharesAndExportsUseBrandMarksConsistently() async throws {
        let menu = MessageMenu()
        for (fields, name, letter, terminal) in [
            (#","agent":"traex""#, "TRAE CLI", false, true),
            (#","agent":"paseo","launcher":"traex","agent_label":"TRAEX""#, "TRAE CLI", false, false),
            (#","agent":"traecli""#, "TRAE CLI", false, false),
            (#","agent":"trae","agent_label":"TRAE""#, "TRAE", false, false),
        ] {
            let session = Self.session(fields)
            menu.session = { session }
            let share = menu.shareMessage(text: "Example", isUser: false, date: nil)
            #expect(share.agent == name && share.prefersLetterMark == letter && share.showsTerminalMark == terminal)

            let source = ForwardMessage.Source(key: session.key, item: "m", role: .agent, author: session.agentLabel,
                                               chat: "Example", project: nil, machine: nil, time: nil)
            let exported = try await ChatExportRenderer.messages([.init(source: source, raw: "Example", full: nil)],
                                                                 prefersLetterMark: AgentKind.prefersLetterMark(for: session))
            let message = try #require(exported.first)
            #expect(message.author == name && message.prefersLetterMark == letter)
        }
    }

    /// A Paseo provider that is neither Claude Code nor Codex (`agent` "paseo", #8144): named by its label, else its
    /// launcher, never the engine; a known provider (TRAE) draws its mark, any other its letter, never Claude's.
    @Test func aPaseoProviderIsNamedAndMarkedByItsProvider() {
        let grok = AgentKind(Self.session(#","agent":"paseo","agent_label":"Grok","launcher":"grok""#))
        #expect(grok.brand == Theme.Brand.grok && grok.name == "Grok")
        // The node's fallback ("Paseo", the engine's name) and a row with no label: the launcher, Paseo's provider id.
        let engine = AgentKind(Self.session(#","agent":"paseo","agent_label":"Paseo","launcher":"grok""#))
        #expect(engine.brand == Theme.Brand.grok && engine.name == "grok")
        #expect(AgentKind(Self.session(#","agent":"paseo","launcher":"kimi""#)).name == "kimi")
        // TRAE by its label, or by its launcher when the row names only the engine.
        for label in ["Trae CLI", "TRAEX", "TraeCLI", "Paseo"] {
            let session = Self.session(",\"agent\":\"paseo\",\"agent_label\":\"\(label)\",\"launcher\":\"traecli\"")
            let mark = AgentKind(session)
            #expect(session.agentLabel == "TRAE CLI" && mark.name == "TRAE CLI" && mark.brand == Theme.Brand.trae)
        }
        let customTraeCLI = Self.session(#","agent":"paseo","agent_label":"My CLI","launcher":"traecli""#)
        #expect(customTraeCLI.agentLabel == "My CLI")
        let trae = AgentKind(Self.session(#","agent":"paseo","launcher":"traecli""#))
        #expect(trae.brand == Theme.Brand.trae && trae.name == "TRAE CLI")
        // A runtime's reported agent keeps its own mark even when its launcher names a different product.
        let dsh = AgentKind(Self.session(#","agent":"dsh","agent_label":"DSH","launcher":"trae""#))
        #expect(dsh.brand == Theme.Brand.dsh && dsh.name == "DSH")
    }

    @Test func namesPaseoSessionsByProductOrCustomTitle() {
        for id in ["traex", "traecli", "trae-cli"] {
            let base = ",\"agent\":\"paseo\",\"launcher\":\"\(id)\""
            #expect(Self.session(base).agentLabel == "TRAE CLI")
            for label in ["Trae CLI", "TRAEX", "TraeCLI", "Paseo", "My CLI"] {
                let title = label == "My CLI" ? "My CLI" : "TRAE CLI"
                let session = Self.session(base + ",\"agent_label\":\"\(label)\"")
                let mark = AgentKind(session)
                #expect(session.agentLabel == title)
                #expect(mark.name == title)
                if id != "traex" { #expect(mark.brand == Theme.Brand.trae) }
            }
        }
    }

    @Test func knownNamesMapToTheirMarks() {
        #expect(AgentKind.brand(named: "TRAE") == Theme.Brand.trae)
        #expect(AgentKind.brand(named: "trae-cli") == Theme.Brand.trae)
        #expect(AgentKind.brand(named: "Claude Code") == Theme.Brand.claude)
        #expect(AgentKind.brand(named: "Codex") == Theme.Brand.openAI)
        #expect(AgentKind.brand(named: "TRAE CLI") == Theme.Brand.trae)
        #expect(AgentKind.brand(named: "Grok · Official") == Theme.Brand.grok)
        #expect(AgentKind.brand(named: "Grok Build") == Theme.Brand.grok)
        #expect(AgentKind.brand(named: "Google Antigravity") == Theme.Brand.agy)
        #expect(AgentKind.brand(named: "Kimi CLI") == Theme.Brand.kimi)
        #expect(AgentKind.brand(named: "GitHub Copilot CLI") == Theme.Brand.copilot)
        #expect(AgentKind(Self.session(#","agent":"opencode""#)).brand == Theme.Brand.opencode)
        #expect(AgentKind(Self.session(#","agent":"pi","agent_label":"My pi profile""#)).brand == Theme.Brand.pi)
        #expect(AgentKind(Self.session(#","agent":"agy","launcher":"my-remote-launcher""#)).brand == Theme.Brand.agy)
        #expect(AgentKind.brand(named: "Unknown Harness") == nil)
        #expect(AgentKind.brand(named: nil) == nil)
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
