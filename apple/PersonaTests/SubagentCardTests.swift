import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A subagent's card in the chat (#5602): how it is doing by its call's result (a background one by its notice), and
/// the line under its title — what it does while it runs, how it went with its steps and time once over.
@MainActor
struct SubagentCardTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil,
                      id: String? = nil, input: [String: JSONValue]? = nil, error: Bool? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: id, toolName: tool,
             toolInput: input.map { .object($0) }, isError: error, image: nil)
    }

    private func call(_ items: [Item]) -> ToolCall {
        let use = items.first { $0.kind == .toolUse }!
        return ToolCall(use: use, result: items.first { $0.kind == .toolResult })
    }

    @Test func aForegroundSubagentGoesByItsResult() {
        let use = item(1, .toolUse, tool: "Agent", id: "a", input: ["description": "回归"])
        #expect(SubagentCardStatus.of(call([use]), tasks: TaskIndex.build([use]), agent: nil) == .running)
        // Its report ends with its agentId (to go on with SendMessage): done, not one sent to the background.
        let done = [use, item(2, .toolResult, role: "user", text: "42 passed.\nagentId: x1 (use SendMessage with to: 'x1')", id: "a")]
        let tasks = TaskIndex.build(done)
        #expect(!tasks.started.contains("a"))
        #expect(SubagentCardStatus.of(call(done), tasks: tasks, agent: Subagent(id: "x1", sessionID: "s@x1", toolUseID: "a", running: true)) == .done)
        // Whatever its report says: "background" in it is not a receipt (only "moved to the background (ID: …)" is).
        let worded = [use, item(2, .toolResult, role: "user", text: "Set the background to gray.\nagentId: x1 (use SendMessage with to: 'x1')", id: "a")]
        let wordedTasks = TaskIndex.build(worded)
        #expect(!wordedTasks.started.contains("a"))
        #expect(SubagentCardStatus.of(call(worded), tasks: wordedTasks, agent: Subagent(id: "x1", sessionID: "s@x1", toolUseID: "a", running: false)) == .done)
        let failed = [use, item(2, .toolResult, role: "user", text: "502", id: "a", error: true)]
        #expect(SubagentCardStatus.of(call(failed), tasks: TaskIndex.build(failed), agent: nil) == .failed)
    }

    @Test func aBackgroundSubagentGoesByItsNotice() {
        let use = item(1, .toolUse, tool: "Agent", id: "a", input: ["description": "全量", "run_in_background": .bool(true)])
        let launched = [use, item(2, .toolResult, role: "user", text: "Async agent launched successfully.\nagentId: x2", id: "a")]
        var agent = Subagent(id: "x2", sessionID: "s@x2", toolUseID: "a", running: true)
        #expect(SubagentCardStatus.of(call(launched), tasks: TaskIndex.build(launched), agent: agent) == .running)
        agent.running = false // gone quiet without a word: stopped
        #expect(SubagentCardStatus.of(call(launched), tasks: TaskIndex.build(launched), agent: agent) == .stopped)
        var note = item(3, .notice, role: "system", text: "Agent \"全量\" failed")
        note.task = TaskNote(kind: "agent", status: "failed", title: "全量", toolUseID: "a")
        #expect(SubagentCardStatus.of(call(launched), tasks: TaskIndex.build(launched + [note]), agent: agent) == .failed)
        #expect(SubagentCardStatus.report("a", tasks: TaskIndex.build(launched)) == nil)
        // Its report is its notice's detail (a SubagentHandback's too, #5886), not the launch receipt.
        note.task?.detail = "3 failed: checkout"
        #expect(SubagentCardStatus.report("a", tasks: TaskIndex.build(launched + [note])) == "3 failed: checkout")
    }

    @Test func theOpenCardShowsItsLatestCallsWithTheirResults() {
        let items = [item(1, .text, role: "user", text: "跑回归"),
                     item(2, .toolUse, tool: "Bash", id: "t1"), item(3, .toolResult, role: "user", text: "ok", id: "t1"),
                     item(4, .toolUse, tool: "Read", id: "t2"), item(5, .toolResult, role: "user", text: "…", id: "t2"),
                     item(6, .thinking, text: "接着查筛选"),
                     item(7, .toolUse, tool: "Grep", id: "t3"), item(8, .toolResult, role: "user", text: "3 matches", id: "t3"),
                     item(9, .toolUse, tool: "Bash", id: "t4")]
        let calls = SubagentRecent.calls(items)
        #expect(calls.map(\.use.toolUseID) == ["t2", "t3", "t4"], "the latest three, oldest first")
        #expect(calls.map(\.isRunning) == [false, false, true], "each with its result once it is in")
        #expect(SubagentRecent.calls([item(1, .text, role: "user", text: "跑回归")]).isEmpty)
    }

    /// The hub folds the run it replays: a short fold is opened for the latest calls, a long one is not fetched.
    @Test func theOpenCardOpensAShortFoldForItsLatestCalls() {
        func fold(_ tools: Int) -> Item {
            var f = item(2, .fold)
            f.fold = Fold(items: tools * 2, tools: tools, thoughts: 0, until: nil, throughSeq: 8, throughID: "8#0")
            return f
        }
        let head = item(1, .text, role: "user", text: "跑回归")
        #expect(SubagentRecent.foldToOpen([head, fold(5), item(9, .toolUse, tool: "Bash", id: "t9")])?.kind == .fold)
        #expect(SubagentRecent.foldToOpen([head, fold(SubagentRecent.foldLimit + 1)]) == nil, "too long to fetch for three rows")
        let after = (0..<SubagentRecent.count).map { item(10 + $0, .toolUse, tool: "Read", id: "r\($0)") }
        #expect(SubagentRecent.foldToOpen([head, fold(5)] + after) == nil, "enough calls after it")
        #expect(SubagentRecent.foldToOpen([head]) == nil)
    }

    @Test func theLineSaysWhatItDoesThenHowItWent() {
        var agent = Subagent(id: "x", sessionID: "s@x", running: true)
        #expect(SubagentCardText.line(.running, agent: agent, took: nil) == String(localized: "Starting… [Subagent Card]"))
        agent.steps = 3
        agent.lastTool = .init(id: "t3", name: "Bash", summary: "跑筛选用例")
        #expect(SubagentCardText.line(.running, agent: agent, took: nil).hasPrefix("跑筛选用例…"))
        agent.lastTool?.summary = nil
        #expect(SubagentCardText.line(.running, agent: agent, took: nil).hasPrefix("Bash…")) // no summary: the tool's name
        let done = SubagentCardText.line(.done, agent: agent, took: 80)
        #expect(done.hasPrefix(String(localized: "Done [Subagent Card]")) && done.contains(TaskNoteText.took(80)))
        #expect(!SubagentCardText.line(.failed, agent: nil, took: nil).contains("·")) // nothing more to say
    }

    /// The running work line names the subagents it waits on: running, launched in the foreground, its call unanswered.
    @Test func theWorkLineSaysWhichSubagentsItWaitsOn() {
        let calls = [item(1, .toolUse, tool: "Agent", id: "a"), item(2, .toolUse, tool: "Agent", id: "b"),
                     item(3, .toolResult, role: "user", text: "done", id: "b"), item(4, .toolUse, tool: "Agent", id: "c")]
        var a = Subagent(id: "x1", sessionID: "s@x1", toolUseID: "a", running: true)
        a.lastTool = .init(id: "t1", name: "Bash", summary: "跑订单单测")
        a.updatedAt = Date(timeIntervalSince1970: 20)
        var c = Subagent(id: "x3", sessionID: "s@x3", toolUseID: "c", running: true)
        c.lastTool = .init(id: "t3", name: "Read", summary: nil)
        c.updatedAt = Date(timeIntervalSince1970: 30)
        let b = Subagent(id: "x2", sessionID: "s@x2", toolUseID: "b", running: true) // answered: not waited on
        var bg = Subagent(id: "x4", sessionID: "s@x4", toolUseID: "d", running: true)
        bg.background = true
        let w = AgentsWorking.of([a, b, c, bg], tasks: TaskIndex.build(calls))
        #expect(w == AgentsWorking(count: 2, doing: ["Read", "跑订单单测"])) // newest call first
        #expect(AgentsWorking.of([b, bg], tasks: TaskIndex.build(calls)) == nil)
        let full = SubagentGridText.working(count: 2, doing: w!.doing, short: false)
        #expect(full.hasPrefix(String(localized: "\(2) subagents working [Work Line]") + " — Read…"))
        #expect(full.hasSuffix(String(localized: "\(1) more [Work Line]")))
        #expect(SubagentGridText.working(count: 2, doing: w!.doing, short: true) == String(localized: "\(2) subagents working [Work Line]"))
    }

    @Test func aGridSaysHowManyRunThenHowManyRanInHowManySteps() {
        #expect(SubagentGridText.head(count: 3, running: 2, steps: 9) == String(localized: "Running \(2) agents [Subagent Grid]"))
        #expect(SubagentGridText.head(count: 3, running: 0, steps: 9)
                == String(localized: "Ran \(3) agents [Subagent Grid]") + " · " + String(localized: "\(9) steps in all [Subagent Grid]"))
        #expect(SubagentGridText.head(count: 3, running: 0, steps: 0) == String(localized: "Ran \(3) agents [Subagent Grid]"))
    }
}
