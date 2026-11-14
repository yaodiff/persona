import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A task whose agent ended its turn but waits on its own background work or on the hub's wake-up is Waiting, not
/// Follow-up (#3274); only one nothing brings back is Follow-up. The same rule as the hub's taskState and the web's.
@MainActor struct TaskWaitingTests {
    private func session(_ json: String) throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private func todo(_ ids: String...) -> TodoItem {
        TodoItem(id: "t", title: "等 CI", agents: ids.map { TodoItem.Agent(machineID: "m", sessionID: $0) })
    }

    @Test func runningWaitingAndStoppedAreThreeStates() throws {
        let running = try session(#"{"machine_id":"m","session_id":"run","state":"working"}"#)
        let background = try session(#"""
        {"machine_id":"m","session_id":"bg","state":"working","background_working":true,
         "work":{"phase":"blocked","on":"external","line":"后台 1 个任务在跑，跑完会自己接着做"}}
        """#)
        let woken = try session(#"{"machine_id":"m","session_id":"ci","state":"idle","work":{"phase":"blocked","on":"external","wait":"CI 跑完"}}"#)
        let stopped = try session(#"{"machine_id":"m","session_id":"stop","state":"idle","work":{"phase":"done"}}"#)
        let onUser = try session(#"{"machine_id":"m","session_id":"user","state":"idle","work":{"phase":"blocked","on":"user"}}"#)
        let lookup = TodosLogic.makeSessionLookup([running, background, woken, stopped, onUser])

        #expect(TodosLogic.todoStatus(todo("run"), lookup: lookup) == .working)
        #expect(TodosLogic.todoStatus(todo("bg"), lookup: lookup) == .waiting)
        #expect(TodosLogic.todoStatus(todo("ci"), lookup: lookup) == .waiting)
        #expect(TodosLogic.todoStatus(todo("stop"), lookup: lookup) == .followup)
        #expect(TodosLogic.todoStatus(todo("user"), lookup: lookup) == .followup)
        #expect(TodosLogic.todoStatus(todo("bg", "run"), lookup: lookup) == .working)
        #expect(TodosLogic.todoStatus(todo("stop", "ci"), lookup: lookup) == .waiting)

        // the task cards and the trays read the same from the sessions they have
        #expect(TaskState(todo: todo("bg"), sessions: [background]) == .waiting)
        #expect(TaskState(todo: todo("run"), sessions: [running]) == .working)
        #expect(TaskState(todo: todo("stop"), sessions: [stopped]) == .followup)

        #expect(woken.waitLine == "CI 跑完" && background.waitLine == "后台 1 个任务在跑，跑完会自己接着做")
        #expect(stopped.waitLine == nil && running.waitLine == nil)
    }

    @MainActor @Test func waitingIsUnderWayNotStopped() {
        #expect(TaskState.waiting.label == "等待中" && TaskState.waiting.label != TaskState.followup.label)
        #expect(TaskState.waiting.priority == TaskState.working.priority)
        #expect(!TaskState.waiting.isSettled && !TaskState.waiting.isRemovable)
        #expect(TaskState.working.todoRank < TaskState.waiting.todoRank && TaskState.waiting.todoRank < TaskState.followup.todoRank)
    }
}
