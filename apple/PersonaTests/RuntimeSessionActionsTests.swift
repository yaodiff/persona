import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Sessions without a terminal (an agent runtime's, such as Paseo's) rename, close, switch their model and rewind where
/// their node says so (#6294: `capabilities.close`, `rename`, `model`, `rewind`, `rewind_code`); a row that does not say
/// (an older node) keeps the terminal rules. Present true, present false and absent for each.
struct RuntimeSessionActionsTests {
    private func session(_ fields: String = "", state: String = "idle") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","status":"idle","state":"\#(state)"\#(fields)}"#.utf8))
    }

    /// A runtime's row (Paseo's): no terminal, what its runtime declared.
    private func runtime(_ caps: String, agent: String = "claude", extra: String = "", state: String = "idle") throws -> Session {
        try session(#","source_kind":"runtime","agent":"\#(agent)","capabilities":{"read":true,"send":true,"interrupt":true,"approve":true\#(caps)}\#(extra)"#,
                    state: state)
    }

    @Test func decodesWhatTheNodeSaysAndLeavesOutWhatItDoesNot() throws {
        let said = try runtime(#","close":true,"rename":false,"model":true,"rewind":true,"rewind_code":false"#)
        #expect(said.capabilities?.close == true)
        #expect(said.capabilities?.rename == false)
        #expect(said.capabilities?.model == true)
        #expect(said.capabilities?.rewind == true)
        #expect(said.capabilities?.rewindCode == false)
        let older = try runtime("")
        #expect(older.capabilities?.close == nil && older.capabilities?.rename == nil && older.capabilities?.model == nil)
        #expect(older.capabilities?.rewind == nil && older.capabilities?.rewindCode == nil)
        // A value this app cannot read is as if not said, and the row still reads.
        let odd = try runtime(#","close":"yes","rename":{}"#)
        #expect(odd.capabilities?.close == nil && odd.capabilities?.rename == nil && odd.capabilities?.send == true)
    }

    @Test func closesAndRenamesByTheNodesWordElseByATerminal() throws {
        let both = try runtime(#","close":true,"rename":true"#)
        #expect(both.canClose && both.canRename && !both.canTerminal)
        let neither = try runtime(#","close":false,"rename":false"#)
        #expect(!neither.canClose && !neither.canRename)
        let older = try runtime("") // an older node: no terminal, so neither, as before
        #expect(!older.canClose && !older.canRename)
        // The user's own Agent in a terminal keeps both; a refusal its node states holds there too.
        let own = try session(#","pane_id":"w1:p1""#)
        #expect(own.canClose && own.canRename)
        let refused = try session(#","pane_id":"w1:p1","capabilities":{"read":true,"send":true,"terminal":true,"close":false,"rename":false}"#)
        #expect(refused.canTerminal && !refused.canClose && !refused.canRename)
    }

    @Test func switchesTheModelWhereTheNodeTakesOne() throws {
        let taken = try runtime(#","model":true"#)
        #expect(taken.switchesModel && taken.picksModelByModes)
        #expect(try runtime(#","model":true"#, agent: "codex").switchesModel, "whatever the agent: the node takes it")
        #expect(!(try runtime(#","model":false"#)).switchesModel)
        #expect(!(try runtime("")).switchesModel, "an older node: no pane to type /model into")
        let readOnly = try session(#","source_kind":"runtime","agent":"claude","capabilities":{"read":true,"model":true}"#)
        #expect(!readOnly.switchesModel, "never one that cannot send")
        #expect(!(try runtime(#","model":true"#, extra: #","assistant":true"#)).switchesModel, "never the assistant's")
        // Claude Code in a pane keeps typing /model; Codex there does not switch.
        let pane = try session(#","pane_id":"w1:p1""#)
        #expect(pane.switchesModel && !pane.picksModelByModes)
        #expect(!(try session(#","pane_id":"w1:p1","agent":"codex""#)).switchesModel)
        #expect(!(try session(#","pane_id":"w1:p1","agent":"traex""#)).switchesModel)
    }

    @Test func rewindsWhereTheNodeSaysSo() throws {
        #expect(Rewind.block(try runtime(#","rewind":true"#)) == "")
        let working = Rewind.block(try runtime(#","rewind":true"#, state: "working"))
        #expect(working != nil && working != "", "offered, off while it works")
        #expect(Rewind.block(try runtime(#","rewind":false"#)) == nil)
        #expect(Rewind.block(try runtime("")) == nil, "an older node: a source offers none")
        #expect(Rewind.block(try runtime(#","rewind":true"#, extra: #","assistant":true"#)) == nil)
        // A Codex thread its node can rewind; one it cannot keeps today's word.
        let codex = #","agent":"codex","pane_id":"w1:p1","capabilities":{"read":true,"send":true,"terminal":true"#
        #expect(Rewind.block(try session(codex + #","rewind":true}"#)) == "")
        let cannot = Rewind.block(try session(codex + #","rewind":false}"#))
        #expect(cannot != nil && cannot != "")
        // Not declared: Claude's rule, as before.
        #expect(Rewind.block(try session()) == "")
        let other = Rewind.block(try session(#","agent":"codex""#))
        #expect(other != nil && other != "")
    }

    @Test func restoresFilesOnlyWhereTheNodeSaysSo() throws {
        #expect(!(try runtime(#","rewind":true"#)).rewindsCode)
        #expect(try runtime(#","rewind":true,"rewind_code":true"#).rewindsCode)
        #expect(try session().rewindsCode, "a Claude terminal declares nothing and keeps both choices")
        #expect(Rewind.offer(try runtime(#","rewind":true"#)) == Rewind.Offer(declared: true, code: false))
        #expect(Rewind.offer(try runtime(#","rewind":true,"rewind_code":true"#)) == Rewind.Offer(declared: true, code: true))
        #expect(Rewind.offer(try session()) == Rewind.Offer(declared: false, code: true))
        #expect(Rewind.offer(nil) == Rewind.Offer())
    }

    /// A runtime numbers its own lines (`<seq>#<n>`): they are messages where its node declares rewind, as the web reads them.
    @Test func aRuntimesOwnLinesAreMessagesWhereItDeclaresRewind() {
        let items = [
            Item(id: "4#0", seq: 4, role: "user", kind: .text, text: "Go on"),
            Item(id: "5#0", seq: 5, role: "assistant", kind: .text, text: "Done"),
            Item(id: "6#0", seq: 6, role: "user", kind: .text, text: "Change it"),
            Item(id: "6#1", seq: 6, role: "user", kind: .text, text: "second line"),
        ]
        #expect(Rewind.entry(of: "4#0") == nil, "Claude's lines are uuids")
        #expect(Rewind.entry(of: "4#0", anyLine: true) == "4")
        #expect(Rewind.entry(of: "#0", anyLine: true) == nil)
        #expect(Rewind.entry(of: "pending-1", anyLine: true) == nil)
        #expect(Rewind.target(items, id: "4#0") == nil)
        #expect(Rewind.target(items, id: "4#0", anyLine: true) == .init(prompt: "Go on", occurrence: 1, promptsAfter: 1))
        #expect(Rewind.target(items, id: "6#0", anyLine: true) == .init(prompt: "Change it\nsecond line", occurrence: 1, promptsAfter: 0))
        let cut = [Item(id: "7#0", seq: 7, role: "user", kind: .text, text: "Long…", fullSize: 9999)]
        #expect(Rewind.cutItem(cut, id: "7#0") == nil)
        #expect(Rewind.cutItem(cut, id: "7#0", anyLine: true) == "7#0")
    }
}
