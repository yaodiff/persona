import Foundation
import Testing
@testable import Persona

struct PermissionInputTests {
    @Test func secureFieldsRoundTripAndDisableSiriApproval() throws {
        let raw = Data(#"{"id":"ix_1","machine_id":"server","session_id":"s1","kind":"permission","fields":[{"key":"password","label":"密码","secret":true}]}"#.utf8)
        let ix = try JSONDecoder().decode(Interaction.self, from: raw)
        #expect(ix.requiresInput)
        #expect(ix.fields?.first?.secret == true)
        #expect(ApproveFirstIntent.resolution(for: ix) == nil)
        let resolution = Resolution(allow: true, values: ["password": "example-password"])
        let decoded = try JSONDecoder().decode(Resolution.self, from: JSONEncoder().encode(resolution))
        #expect(decoded.values?["password"] == "example-password")
    }
}

@MainActor
struct PermissionAlwaysScopeTests {
    @Test func bashNamesEveryCommandAndDirectoryRulesNameTheirScope() throws {
        let raw = Data(#"{"id":"ix_scope","machine_id":"computer","session_id":"chat","kind":"permission","tool_name":"Bash","always_keys":["tool:Bash"]}"#.utf8)
        var ix = try JSONDecoder().decode(Interaction.self, from: raw)
        #expect(ix.alwaysScopeDetail == String(localized: "All commands in this conversation"))
        let decoded = try JSONDecoder().decode(Interaction.self, from: JSONEncoder().encode(ix))
        #expect(decoded.alwaysKeys == ["tool:Bash"])

        ix.toolName = "Read"
        ix.alwaysKeys = ["tool:Read(//workspace/docs/**)"]
        #expect(ix.alwaysScopeDetail == String(localized: "All reads in this directory in this conversation"))
        for tool in ["Edit", "Write", "MultiEdit", "NotebookEdit"] {
            ix.toolName = tool
            ix.alwaysKeys = ["tool:Edit(//workspace/docs/**)"]
            #expect(ix.alwaysScopeDetail == String(localized: "All file edits in this directory in this conversation"))
        }

        ix.alwaysKeys = nil
        #expect(ix.alwaysScopeDetail == String(localized: "All uses of this tool in this conversation"))
        ix.alwaysKeys = ["lark:POST /conversations/*/messages"]
        #expect(ix.alwaysScopeDetail == String(localized: "The approved scope remains allowed in this conversation"))
    }

    @Test func anotherAgentsNarrowScopeIsPreserved() {
        var ix = Interaction(id: "ix_scope", machineID: "computer", sessionID: "chat", kind: .permission,
                             toolName: "Bash", agent: "codex", alwaysLabel: "Only this command")
        #expect(ix.alwaysScopeDetail == "Only this command")
        ix.alwaysLabel = nil
        #expect(ix.alwaysScopeDetail == nil)
    }

    /// #3590: Always only where it is kept. The manager says so on its cards (false on its own, which keep nothing;
    /// true where it remembers); without the field (an older hub, an Agents session's card) the old rule holds.
    @Test func alwaysOnlyWhereTheManagerKeepsIt() throws {
        func card(_ extra: String) throws -> Interaction {
            let raw = #"{"id":"ix_a","machine_id":"computer","session_id":"chat","kind":"permission","tool_name":"iphone_do""# + extra + "}"
            return try JSONDecoder().decode(Interaction.self, from: Data(raw.utf8))
        }
        let always = String(localized: "Always Allow in Session")
        let labels = { (ix: Interaction) in BUIPermissionForm.choices(ix).map(\.label) }

        let absent = try card("")
        #expect(absent.alwaysHonored == nil && absent.offersAlways && labels(absent).contains(always))
        let own = try card(#","always_honored":false,"tool_title":"iPhone control""#)
        #expect(own.alwaysHonored == false && !own.offersAlways && !labels(own).contains(always))
        #expect(own.toolDisplayName == "iPhone control" && absent.toolDisplayName == "iphone_do")
        let kept = try card(#","always_honored":true"#)
        #expect(kept.offersAlways && labels(kept).contains(always))
        let again = try JSONDecoder().decode(Interaction.self, from: JSONEncoder().encode(own))
        #expect(again.alwaysHonored == false && again.toolTitle == "iPhone control")

        // No conversation, no field: nothing to keep it in, as before.
        var bare = absent
        bare.sessionID = ""
        #expect(!bare.offersAlways)
        // Another agent's card offers only what it says, unless the manager says otherwise.
        var codex = Interaction(id: "ix_c", machineID: "computer", sessionID: "chat", kind: .permission,
                                toolName: "Bash", agent: "codex", alwaysLabel: "Only this command")
        #expect(codex.offersAlways && labels(codex).contains(String(localized: "Always Approve")))
        codex.alwaysHonored = false
        #expect(!codex.offersAlways && !labels(codex).contains(String(localized: "Always Approve")))
        // One the manager keeps (its keys remembered) offers it even without a label, as on the web.
        codex.alwaysHonored = true
        codex.alwaysLabel = nil
        #expect(codex.offersAlways && labels(codex).contains(String(localized: "Always Approve")))
        #expect(codex.alwaysScopeDetail == nil)

        var agyWrite = Interaction(id: "ix_agy", machineID: "computer", sessionID: "antigravity-1", kind: .permission,
                                   toolName: "Write", agent: "antigravity", alwaysKeys: ["tool:Edit(//workspace/docs/**)"],
                                   alwaysHonored: true)
        #expect(agyWrite.offersAlways)
        #expect(agyWrite.alwaysScopeDetail == String(localized: "All file edits in this directory in this conversation"))
        #expect(BUIPermissionForm.choices(agyWrite).first(where: { $0.label == String(localized: "Always Approve") })?.detail == agyWrite.alwaysScopeDetail)
        agyWrite.toolName = "Bash"
        agyWrite.alwaysKeys = ["tool:Bash"]
        #expect(agyWrite.alwaysScopeDetail == String(localized: "All commands in this conversation"))
    }

    @Test func noHandBackDecodesAndRoundTripsWithoutChangingOlderCards() throws {
        let headlessRaw = Data(#"{"id":"ix_hb","machine_id":"computer","session_id":"chat","kind":"permission","tool_name":"Bash","no_hand_back":true}"#.utf8)
        let headless = try JSONDecoder().decode(Interaction.self, from: headlessRaw)
        #expect(headless.noHandBack == true)
        let encoded = try JSONEncoder().encode(headless)
        let decoded = try JSONDecoder().decode(Interaction.self, from: encoded)
        #expect(decoded.noHandBack == true)

        let legacyRaw = Data(#"{"id":"ix_legacy","machine_id":"computer","session_id":"chat","kind":"permission","tool_name":"Bash"}"#.utf8)
        let legacy = try JSONDecoder().decode(Interaction.self, from: legacyRaw)
        #expect(legacy.noHandBack == nil)
    }
}
