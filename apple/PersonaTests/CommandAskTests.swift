import Foundation
import Testing
@testable import Persona

/// Claude Code's permission prompt for a shell command read off its terminal (#5266): what the screen card says of it in
/// plain words, from the hub's dialog.
struct CommandAskTests {
    /// The dialog as the hub sends it for Claude Code 2.1.322's safety check (server/internal/screen, #5265).
    private static let hubJSON = """
    {"question":"Do you want to proceed?","rev":"44597c89094b",
     "detail":"Bash command\\nCount the example names in first-screen chunks\\ncd ./dist && ls",
     "warning":"Dangerous rm operation not ruled out — the shell may read this command differently than it was parsed",
     "deadline":"Claude Code will automatically block this request in 1:44, to avoid holding up an unattended session",
     "command":"cd ./dist && ls","description":"Count the example names in first-screen chunks","risk":"delete",
     "deadline_action":"deny","deadline_at":"2026-11-12T03:00:00Z",
     "options":[{"key":"1","label":"Yes","keys":["1"]},{"key":"2","label":"No","keys":["2"]}]}
    """

    private func dialog(_ json: String) throws -> ScreenParser.Dialog {
        try JSON.decoder.decode(ScreenDialog.self, from: Data(json.utf8)).parsed
    }

    @Test func readsTheHubsCommandPrompt() throws {
        let d = try dialog(Self.hubJSON)
        let ask = try #require(d.commandAsk)
        #expect(ask.command == "cd ./dist && ls")
        #expect(ask.description == "Count the example names in first-screen chunks")
        #expect(ask.risk == "delete" && !ask.deadlineAllows)
        #expect(ask.deadlineAt == ISO8601DateFormatter().date(from: "2026-11-12T03:00:00Z"))
        #expect(ask.riskLine == String(localized: "Can't confirm this command won't delete files"))
        // Claude Code's own words stay one tap away.
        #expect(ask.original?.hasPrefix("Dangerous rm operation not ruled out") == true)
        #expect(ask.original?.contains("automatically block this request in 1:44") == true)
        let pair = d.allowDeny
        #expect(pair?.allow.key == "1" && pair?.deny.key == "2")
    }

    @Test func anOlderHubsDialogIsAQuestionAsBefore() throws {
        let d = try dialog("""
        {"question":"Do you want to proceed?","rev":"r1","detail":"Bash command\\nrm -rf build",
         "options":[{"key":"1","label":"Yes","keys":["1"]},{"key":"2","label":"No","keys":["2"]}]}
        """)
        #expect(d.commandAsk == nil && d.allowDeny == nil)
        #expect(d.detail == "Bash command\nrm -rf build")
    }

    /// More choices than Yes and No ("Yes, and don't ask again for …"): they keep their own labels.
    @Test func moreChoicesKeepTheirLabels() throws {
        let three = Self.hubJSON.replacingOccurrences(
            of: #"{"key":"2","label":"No","keys":["2"]}"#,
            with: #"{"key":"2","label":"Yes, and don't ask again for ls commands","keys":["2"]},{"key":"3","label":"No","keys":["3"]}"#)
        let d = try dialog(three)
        #expect(d.commandAsk != nil && d.options.count == 3 && d.allowDeny == nil)
    }

    @Test func theWarningInPlainWords() {
        let other = CommandAsk(command: "ls", warning: "Command contains something unusual")
        #expect(other?.riskLine == String(localized: "Claude Code warns about this command"))
        #expect(CommandAsk(command: "ls")?.riskLine == nil)
        #expect(CommandAsk(command: "  ") == nil)
    }

    @Test func theCountdownsLine() throws {
        let now = Date(timeIntervalSince1970: 1_000_000)
        let deny = try #require(CommandAsk(command: "ls", deadlineAction: "deny", deadlineAt: now.addingTimeInterval(104)))
        #expect(deny.countdownLine(now: now) == String(localized: "Denied automatically in \("1:44")"))
        #expect(deny.countdownLine(now: now.addingTimeInterval(103.5)) == String(localized: "Denied automatically in \("0:01")"))
        #expect(deny.countdownLine(now: now.addingTimeInterval(104)) == deny.expiredLine)
        #expect(deny.expiredLine == String(localized: "Time's up: Claude Code denies it"))
        let allow = try #require(CommandAsk(command: "ls", deadlineAction: "allow", deadlineAt: now.addingTimeInterval(61)))
        #expect(allow.countdownLine(now: now) == String(localized: "Allowed automatically in \("1:01")"))
        #expect(CommandAsk(command: "ls")?.countdownLine(now: now) == nil)
    }

    /// A long command folds in the card to its lines, fewer when the card is short, cut between lines, never under its
    /// fewest (#6605); short words are drawn whole.
    @Test func aLongCommandFoldsBetweenLines() {
        let line: CGFloat = 16
        let cut = line * CGFloat(CommandAsk.foldedLines)
        let floor = line * 2
        #expect(CardFoldCut.height(full: 40, cut: cut, line: line, floor: floor, offered: nil) == 40)
        #expect(CardFoldCut.height(full: 900, cut: cut, line: line, floor: floor, offered: nil) == cut)
        #expect(CardFoldCut.height(full: 900, cut: cut, line: line, floor: floor, offered: 1000) == cut)
        // Offered less: the whole lines that fit, an offer rounded a hair under a line count keeping that line.
        #expect(CardFoldCut.height(full: 900, cut: cut, line: line, floor: floor, offered: 70) == line * 4)
        #expect(CardFoldCut.height(full: 900, cut: cut, line: line, floor: floor, offered: line * 7 - 0.01) == line * 7)
        // Never under its fewest lines; words shorter than those, whole.
        #expect(CardFoldCut.height(full: 900, cut: cut, line: line, floor: floor, offered: 10) == floor)
        #expect(CardFoldCut.height(full: 20, cut: cut, line: line, floor: floor, offered: 10) == 20)
        // Not measured yet: whole.
        #expect(CardFoldCut.height(full: 900, cut: 0, line: 0, floor: 0, offered: 70) == 900)
        // Hiding fewer than three lines is no fold: a three-line command offered two, and ten lines under an eight-line
        // cut, are whole; eleven fold to eight. Only the start of a text (`more`) folds however short.
        #expect(CardFoldCut.height(full: line * 3, cut: cut, line: line, floor: floor, offered: line * 2 + 4) == line * 3)
        #expect(CardFoldCut.height(full: line * 10, cut: cut, line: line, floor: floor, offered: nil) == line * 10)
        #expect(CardFoldCut.height(full: line * 11, cut: cut, line: line, floor: floor, offered: nil) == cut)
        #expect(CardFoldCut.height(full: line * 10, cut: cut, line: line, floor: floor, offered: nil, more: true) == cut)
    }

    /// A terminal's Yes / No (or a command's Allow / Deny) stay under the card's scroll area, in view; a question's
    /// options with descriptions, many options or ticks scroll with the question (#6605).
    @Test func shortChoicesStayInView() throws {
        let hub = try dialog(Self.hubJSON)
        #expect(ScreenForm.pinsChoices(hub))
        func options(_ list: [(String, String?)], multi: Bool = false) -> ScreenParser.Dialog {
            var d = ScreenParser.Dialog(header: nil, question: "Do you want to proceed?", detail: nil,
                                        options: list.enumerated().map { i, o in
                                            .init(key: "\(i + 1)", label: o.0, detail: o.1, kind: .plain, checked: false, text: nil)
                                        })
            d.multi = multi
            return d
        }
        #expect(ScreenForm.pinsChoices(options([("Yes", nil), ("No", nil)])))
        #expect(!ScreenForm.pinsChoices(options([("Yes", "Run it now"), ("No", nil)])))
        #expect(!ScreenForm.pinsChoices(options((1...5).map { ("Option \($0)", nil) })))
        #expect(!ScreenForm.pinsChoices(options([("A", nil), ("B", nil)], multi: true)))
    }

    /// The needs-you row and the folded card say what it is for, not "Do you want to proceed?".
    @Test func askLineIsTheAgentsWords() throws {
        let d = try JSON.decoder.decode(ScreenDialog.self, from: Data(Self.hubJSON.utf8))
        let ix = Interaction(id: "scr_1", machineID: "m1", sessionID: "s1", sessionTitle: nil, cwd: nil, kind: .screen,
                             createdAt: nil, expiresAt: nil, toolName: nil, toolUseID: nil, questions: nil, plan: nil,
                             toolInput: nil, summary: nil, screen: nil, dialog: d)
        #expect(ix.askLine(includeToolName: false) == "Count the example names in first-screen chunks")
    }
}
