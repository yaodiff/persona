import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A plan's (TodoWrite's) progress in the one sentence the web says it too (#5901).
@MainActor
struct PlanProgressTests {
    private func plan(_ status: [String]) -> [JSONValue] {
        status.enumerated().map { n, s in .object(["content": .string("step \(n)"), "status": .string(s)]) }
    }

    @Test func theCurrentStepIsTheFirstInProgressElseTheFirstToDo() {
        #expect(PlanProgress.text(plan(["completed", "completed", "in_progress", "pending"])) == String(localized: "Step \(3) of \(4) [Plan Progress]"))
        // Nothing in progress (it waits for an answer): the first still to do.
        #expect(PlanProgress.text(plan(["completed", "pending", "pending", "pending"])) == String(localized: "Step \(2) of \(4) [Plan Progress]"))
        // One in progress after one left to do: the one in progress.
        #expect(PlanProgress.text(plan(["pending", "in_progress"])) == String(localized: "Step \(2) of \(2) [Plan Progress]"))
    }

    @Test func allDoneSaysSo() {
        #expect(PlanProgress.text(plan(["completed", "completed", "completed", "completed"])) == String(localized: "All \(4) steps done [Plan Progress]"))
        #expect(PlanProgress.text([]) == "")
    }

    private func use(_ seq: Int, _ id: String, _ status: [String]) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: "assistant", kind: .toolUse, text: nil, toolUseID: id, toolName: "TodoWrite",
             toolInput: .object(["todos": .array(plan(status))]), isError: nil, image: nil)
    }

    /// The chat's newest plan: its latest TodoWrite of two or more steps that did not fail, or the list a fold kept.
    @Test func theNewestPlanIsTheLatestListThatWentThrough() {
        let first = use(1, "a", ["completed", "in_progress", "pending"])
        let failed = use(2, "b", ["completed", "completed", "completed"])
        let error = Item(id: "3#0", seq: 3, ts: nil, role: "user", kind: .toolResult, text: "bad", toolUseID: "b", toolName: nil,
                         toolInput: nil, isError: true, image: nil)
        let one = use(4, "c", ["in_progress"]) // a list of one is no plan
        let latest = ChatPlan.latest([first, failed, error, one])
        #expect(latest?.callID == first.id)
        #expect(latest.map(PlanProgress.summary) == String(localized: "Step \(2) of \(3) [Plan Progress]") + " · step 1")
        // Folded with its work: the fold's list.
        var fold = Item(id: "fold:5#0", seq: 5, ts: nil, role: "assistant", kind: .fold, text: nil, toolUseID: nil, toolName: nil,
                        toolInput: nil, isError: nil, image: nil)
        fold.fold = Fold(items: 4, tools: 2, thoughts: 0, until: nil, throughSeq: 8, throughID: "8#0",
                         tasks: [FoldTasks(replace: "todo", entries: [.init(id: "todo:0", subject: "建", status: "completed"),
                                                                      .init(id: "todo:1", subject: "测", status: "completed")],
                                           sourceItemID: "6#0")])
        let folded = ChatPlan.latest([first, fold])
        #expect(folded?.callID == "6#0")
        #expect(folded?.allDone == true)
        #expect(folded.map(PlanProgress.summary) == String(localized: "All \(2) steps done [Plan Progress]"))
    }
}
