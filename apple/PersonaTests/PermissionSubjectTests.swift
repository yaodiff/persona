import Foundation
import Testing
@testable import Persona
import PersonaWire

/// What a permission card and the rows that point to it show for its call (#3472): the hub's summary, else the command or
/// path the tool acts on, never the raw JSON of its input.
struct PermissionSubjectTests {
    private func permission(tool: String?, summary: String? = nil, input: JSONValue?) -> Interaction {
        Interaction(
            id: "ix", machineID: "m1", sessionID: "s1", sessionTitle: nil, cwd: nil,
            kind: .permission, createdAt: nil, expiresAt: nil, toolName: tool, toolUseID: nil, questions: nil,
            plan: nil, toolInput: input, summary: summary, screen: nil, dialog: nil
        )
    }

    @Test func theHubsSummaryWins() {
        let ix = permission(tool: "Bash", summary: "swift test", input: .object(["command": .string("swift build")]))
        #expect(ix.permissionSubject == "swift test")
    }

    @Test func bashWithoutSummaryShowsItsCommandNotItsDescription() {
        let input = JSONValue.object([
            "command": .string("python3 -m unittest discover"),
            "description": .string("验证格式转换与元数据保留"),
        ])
        #expect(permission(tool: "Bash", input: input).permissionSubject == "python3 -m unittest discover")
        #expect(permission(tool: "Bash", summary: "  ", input: input).permissionSubject == "python3 -m unittest discover")
    }

    @Test func fileToolsShowTheirPath() {
        let input = JSONValue.object(["file_path": .string("/work/app/Sources/Net.swift"), "old_string": .string("a")])
        #expect(permission(tool: "Edit", input: input).permissionSubject == "/work/app/Sources/Net.swift")
    }

    @Test func otherToolsPickAKnownFieldElseListTheirInput() {
        #expect(permission(tool: "mcp__docs__fetch", input: .object(["url": .string("https://example.com/a")])).permissionSubject
            == "https://example.com/a")
        let input = JSONValue.object(["count": .number(3), "dry": .bool(true), "tags": .array([.string("a")])])
        #expect(permission(tool: "mcp__x__y", input: input).permissionSubject == "count: 3\ndry: true\ntags: [\"a\"]")
    }

    @Test func nothingToShowIsNil() {
        #expect(permission(tool: "Bash", input: nil).permissionSubject == nil)
        #expect(permission(tool: "Bash", input: .object([:])).permissionSubject == nil)
    }

    /// The list's row, the Inbox and the Mac menu bar read the same line whether the hub sent a summary or only the input.
    @Test func rowsShowWhatTheCallRunsAsIfTheHubHadSummedItUp() {
        let input = JSONValue.object(["command": .string("python3 -m unittest discover"), "description": .string("跑单测")])
        let bare = permission(tool: "Bash", input: input)
        let summed = permission(tool: "Bash", summary: "python3 -m unittest discover", input: nil)
        #expect(bare.permissionTarget == "python3 -m unittest discover")
        #expect(bare.askLine(includeToolName: true) == summed.askLine(includeToolName: true))
        #expect(bare.askLine(includeToolName: true)?.hasPrefix("Bash") == true)
        #expect(bare.askLine(includeToolName: false) == "python3 -m unittest discover")
    }

    /// A one-line row never lists the input: without a field to pick it keeps the tool's name, and only the card lists it.
    @Test func rowsNeverListTheInput() {
        let ix = permission(tool: "mcp__x__y", input: .object(["count": .number(3)]))
        #expect(ix.permissionTarget == nil)
        #expect(ix.askLine(includeToolName: true) == "mcp__x__y")
        #expect(ix.askLine(includeToolName: false) == "mcp__x__y")
        #expect(ix.permissionSubject == "count: 3")
    }
}
