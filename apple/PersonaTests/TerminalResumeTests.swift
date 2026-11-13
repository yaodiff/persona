import Foundation
import Testing
@testable import Persona

/// Resume in Terminal (#5521): what the chat's ⋯ offers for an Agent that isn't running, the same rule as the web's
/// `terminalResume` (web/src/lib/agent-resume.test.ts has the same cases). Offered for a stopped Agent, Claude Code or a
/// Codex thread nothing has open; disabled, saying why, where its computer enables no terminal engine; never while it
/// runs (in Persona's terminal or in one of its own), for an assistant conversation, a desktop app's session or a row
/// an older node sent.
@MainActor struct TerminalResumeTests {
    /// A Claude Code typed into a terminal of its own, closed since; with `stoppedCaps` as the node lists it stopped.
    private func session(_ extra: String, status: String = "offline", agent: String = "claude", surface: String = "cli") -> Session {
        let json = """
        {"machine_id":"m","session_id":"s1","title":"Tidy the notes","cwd":"/tmp/p","status":"\(status)","state":"offline",\
        "agent":"\(agent)","agent_surface":"\(surface)"\(extra)}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private func machine(_ hosts: String?) -> Machine {
        let json = #"{"id":"m","name":"Mac mini","kind":"ssh","online":true"# + (hosts.map { #","engine_hosts":"# + $0 } ?? "") + "}"
        do { return try JSON.decoder.decode(Machine.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private let stoppedCaps = #","capabilities":{"read":true,"terminal":true},"read_only_reason":"stopped""#

    @Test func aStoppedAgentIsOffered() {
        let herdr = machine(#"["herdr"]"#)
        #expect(session(stoppedCaps).terminalResume(on: herdr) == .available)
        #expect(session(stoppedCaps, status: "interrupted").terminalResume(on: herdr) == .available) // read as unknown
        let codex = session(#","source_kind":"codex_history""# + stoppedCaps, agent: "codex")
        #expect(codex.isStopped, "a Codex thread nothing has open is a stopped Agent too")
        #expect(codex.terminalResume(on: herdr) == .available)
        #expect(session(stoppedCaps).terminalResume(on: machine(nil)) == .available, "a hub before engine sets: the node's word holds")
        #expect(session(stoppedCaps).terminalResume(on: nil) == .available)
    }

    @Test func noTerminalEngineDisablesItSayingWhy() {
        let none = machine("[]")
        #expect(session("").terminalResume(on: none) == .noEngine, "listed there without capabilities, as the node does")
        #expect(session(#","source_kind":"codex_history","capabilities":{"read":true}"#, agent: "codex").terminalResume(on: none) == .noEngine)
        #expect(session(stoppedCaps).terminalResume(on: none) == .noEngine, "engines turned off since the row was listed")
    }

    @Test func notWhileItRuns() {
        let herdr = machine(#"["herdr"]"#), none = machine("[]")
        #expect(session("", status: "idle").terminalResume(on: herdr) == .hidden)
        let elsewhere = session(#","source_kind":"claude_cli","capabilities":{"read":true,"send":true}"#, status: "idle")
        #expect(elsewhere.terminalResume(on: herdr) == .hidden, "a second claude would write the same transcript")
        #expect(elsewhere.terminalResume(on: none) == .hidden)
        #expect(session(stoppedCaps + #","starting":true"#).terminalResume(on: herdr) == .hidden)
    }

    @Test func leftOutRows() {
        let herdr = machine(#"["herdr"]"#), none = machine("[]")
        #expect(session(stoppedCaps + #","assistant":true"#).terminalResume(on: herdr) == .hidden)
        #expect(session(#","assistant":true"#).terminalResume(on: none) == .hidden)
        #expect(session("", surface: "desktop").terminalResume(on: none) == .hidden, "a message reopens it in the app")
        #expect(session(#","source_kind":"runtime","capabilities":{"read":true}"#, agent: "dsh").terminalResume(on: none) == .hidden)
        #expect(session("").terminalResume(on: herdr) == .hidden, "an older node's row does not say it can")
        let appThread = session(#","source_kind":"codex_history","capabilities":{"read":true}"#, agent: "codex")
        #expect(!appThread.isStopped, "a Codex App thread has no terminal")
        #expect(appThread.terminalResume(on: herdr) == .hidden)
    }
}
