import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A tool result's `outcome` (docs/protocol/protocol.md "Tool outcomes", #4303): read whole, in part or not at all
/// without dropping the item, and only a call whose run failed is drawn as a failure.
@MainActor
struct ToolOutcomeTests {
    private func result(_ outcome: String? = nil, isError: Bool? = nil) throws -> Item {
        var raw = #"{"id":"r1#0","seq":2,"role":"user","kind":"tool_result","tool_use_id":"toolu_1","text":"output""#
        if let isError { raw += #","is_error":\#(isError)"# }
        if let outcome { raw += #","outcome":\#(outcome)"# }
        return try JSON.decoder.decode(Item.self, from: Data((raw + "}").utf8))
    }

    private func call(_ result: Item?) -> ToolCall {
        ToolCall(use: Item(id: "u1#0", seq: 1, ts: nil, role: "assistant", kind: .toolUse, text: nil, toolUseID: "toolu_1",
                           toolName: "Bash", toolInput: .object(["command": .string("go test ./...")]), isError: nil, image: nil),
                 result: result)
    }

    @Test func readsWhatItDrawsAndLeavesTheRest() throws {
        let item = try result(#"{"status":"failed","exit_code":1,"tail":"FAIL\tpkg/x","duration_ms":12,"files":[{"path":"a"}],"future":{"x":1}}"#,
                              isError: true)
        #expect(item.outcome == ToolOutcome(status: "failed", exitCode: 1, tail: "FAIL\tpkg/x"))
        #expect(try result(#"{"status":"denied"}"#).outcome == ToolOutcome(status: "denied"))
        #expect(try result().outcome == nil)
        #expect(try JSON.decoder.decode(Item.self, from: JSON.encoder.encode(item)) == item)
    }

    @Test func aMalformedOutcomeNeverDropsTheItem() throws {
        for value in ["null", "false", "42", #""failed""#, "[]", #"{"status":42,"exit_code":"one","tail":false}"#,
                      #"{"status":"failed","exit_code":1.5}"#] {
            let item = try result(value, isError: true)
            #expect(item.text == "output")
            #expect(item.isError == true)
            #expect(item.outcome?.exitCode == nil)
            #expect(call(item).failed)
        }
        #expect(try result(#"{"status":"failed","exit_code":"one"}"#).outcome?.status == "failed")
    }

    @Test func onlyAFailedRunIsDrawnAsAFailure() throws {
        for status in ["denied", "blocked", "interrupted"] {
            let c = call(try result(#"{"status":"\#(status)"}"#, isError: true))
            #expect(c.isError) // the model still got an error back
            #expect(!c.failed)
            #expect(ToolRunSummary.chips([c]).allSatisfy { !$0.isError })
        }
        let failed = call(try result(#"{"status":"failed","exit_code":2}"#, isError: true))
        #expect(failed.failed)
        #expect(ToolRunSummary.chips([failed]).contains { $0.isError })
        // An older node sends no outcome: an error stays a failure, as before.
        #expect(call(try result(isError: true)).failed)
        #expect(!call(try result(#"{"status":"timed_out"}"#)).failed)
        #expect(!call(nil).failed)
    }

    @Test func theLineOverTheResultSaysWhatHappened() {
        let exit = ToolOutcomeText.headline(ToolOutcome(status: "failed", exitCode: 1))
        #expect(exit?.failed == true)
        #expect(exit?.symbol == Theme.Icon.errorFilled)
        #expect(exit?.text.contains("1") == true)
        #expect(ToolOutcomeText.headline(ToolOutcome(status: "failed")) == nil) // the result block's own words
        for (status, symbol) in [("denied", Theme.Icon.denied), ("blocked", Theme.Icon.didNotRun), ("interrupted", Theme.Icon.interrupt),
                                 ("timed_out", Theme.Icon.time)] {
            let head = ToolOutcomeText.headline(ToolOutcome(status: status))
            #expect(head?.failed == false)
            #expect(head?.symbol == symbol)
            #expect(head?.text.isEmpty == false)
        }
        for status in ["ok", "no_match", "background", "a-later-status"] {
            #expect(ToolOutcomeText.headline(ToolOutcome(status: status, exitCode: 0)) == nil)
        }
        #expect(ToolOutcomeText.headline(nil) == nil)
    }
}
