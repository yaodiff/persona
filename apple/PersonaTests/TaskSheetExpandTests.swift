import Foundation
import Testing
@testable import Persona

/// Full Screen in the assistant's agent sheet: the sheet goes down (both of its sources cleared, so it does not rise
/// again) and the agent is handed to the assistant page to push.
@MainActor
struct TaskSheetExpandTests {
    private func store() -> TasksStore {
        TasksStore(todoStore: TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}))
    }

    @Test func expandingAnAgentClosesItsSheetAndAsksForThePush() {
        let tasks = store()
        let key = SessionKey(machineID: "box", sessionID: "agent-1")
        tasks.detailAgent = key
        tasks.expand(key)
        #expect(tasks.detailAgent == nil)
        #expect(tasks.detailTask == nil)
        #expect(tasks.fullAgent == key)
    }

    @Test func expandingFromATaskClosesTheTaskSheet() {
        let tasks = store()
        let key = SessionKey(machineID: "box", sessionID: "agent-2")
        tasks.detailTask = "todo-1"
        tasks.expand(key)
        #expect(tasks.detailTask == nil)
        #expect(tasks.fullAgent == key)
    }

    @Test func dispatchPushesOnlyItsResolvedAgent() {
        let tasks = store()
        let key = SessionKey(group: "hub", machineID: "computer", sessionID: "dispatched")
        tasks.detailTask = "unrelated"
        tasks.detailAgent = SessionKey(machineID: "other", sessionID: "other")
        tasks.openDispatchedTask("new", agent: key, pushAgent: true)
        #expect(tasks.fullAgent == key)
        #expect(tasks.detailTask == nil)
        #expect(tasks.detailAgent == nil)
        #expect(tasks.showBoard == false)
    }

    @Test func dispatchWithoutAnAgentOpensOnlyItsTaskDetail() {
        let tasks = store()
        tasks.expand(SessionKey(machineID: "old", sessionID: "old"))
        tasks.openDispatchedTask("starting", agent: nil, pushAgent: true)
        #expect(tasks.fullAgent == nil)
        #expect(tasks.detailTask == "starting")
        #expect(tasks.detailAgent == nil)
    }

    @Test func macDispatchDetailTargetsTheCorrespondingSession() {
        let tasks = store()
        let key = SessionKey(group: "hub", machineID: "computer", sessionID: "agent")
        tasks.openDispatchedTask("new", agent: key, pushAgent: false)
        #expect(tasks.detailTask == "new")
        #expect(tasks.detailTaskAgent == key)
        #expect(tasks.fullAgent == nil)
        #expect(tasks.detailAgent == nil)
    }

    @Test func eachMacPlanLinkTargetsItsOwnSession() {
        let tasks = store()
        let first = SessionKey(group: "hub", machineID: "computer", sessionID: "first")
        let second = SessionKey(group: "hub", machineID: "another", sessionID: "second")
        for (todo, key) in [("first-task", first), ("second-task", second)] {
            tasks.openDispatchedTask(todo, agent: key, pushAgent: false)
            #expect(tasks.detailTask == todo)
            #expect(tasks.detailTaskAgent == key)
        }
        tasks.detailTask = "ordinary-task"
        #expect(tasks.detailTaskAgent == nil, "an ordinary task must not inherit a dispatch's session")
    }

    @Test func macStartingTaskHasNoInventedSessionTarget() {
        let tasks = store()
        tasks.openDispatchedTask("old", agent: SessionKey(machineID: "computer", sessionID: "old"), pushAgent: false)
        tasks.openDispatchedTask("starting", agent: nil, pushAgent: false)
        #expect(tasks.detailTask == "starting")
        #expect(tasks.detailTaskAgent == nil)
    }

    @Test func expandingADispatchClearsItsDetailSessionTarget() {
        let tasks = store()
        let key = SessionKey(machineID: "computer", sessionID: "agent")
        tasks.openDispatchedTask("task", agent: key, pushAgent: false)
        tasks.expand(key)
        #expect(tasks.fullAgent == key)
        #expect(tasks.detailTask == nil)
        #expect(tasks.detailTaskAgent == nil)
    }

    @Test func forgettingDropsADispatchedDetailSession() {
        let tasks = store()
        tasks.openDispatchedTask("task", agent: SessionKey(group: "hub", machineID: "computer", sessionID: "agent"), pushAgent: false)
        tasks.forget()
        #expect(tasks.detailTask == nil)
        #expect(tasks.detailTaskAgent == nil)
    }

    /// The Sidebar layout's page takes a task's detail as a value on its path (#13156): the request is cleared as it is
    /// taken, so no presenter pushes it by view as well.
    @Test func takingATaskDetailClearsTheRequest() {
        let tasks = store()
        let key = SessionKey(group: "hub", machineID: "computer", sessionID: "agent")
        tasks.openDispatchedTask("task", agent: key, pushAgent: false)
        #expect(tasks.takeDetail() == TaskDetailRoute(todo: "task", taskAgent: key))
        #expect(tasks.detailTask == nil)
        #expect(tasks.detailTaskAgent == nil)
        #expect(tasks.takeDetail() == nil)
    }

    @Test func takingAnAgentDetailClearsTheRequest() {
        let tasks = store()
        let key = SessionKey(machineID: "box", sessionID: "agent-4")
        tasks.detailAgent = key
        #expect(tasks.takeDetail() == TaskDetailRoute(agent: key))
        #expect(tasks.detailAgent == nil)
        #expect(tasks.takeDetail() == nil)
    }

    @Test func aTaskDetailWinsOverAnAgentDetail() {
        let tasks = store()
        tasks.detailAgent = SessionKey(machineID: "box", sessionID: "agent-5")
        tasks.detailTask = "todo-2"
        #expect(tasks.takeDetail() == TaskDetailRoute(todo: "todo-2"))
        #expect(tasks.detailTask == nil && tasks.detailAgent == nil)
    }

    @Test func forgettingDropsAPendingPush() {
        let tasks = store()
        tasks.expand(SessionKey(machineID: "box", sessionID: "agent-3"))
        tasks.forget()
        #expect(tasks.fullAgent == nil)
    }
}

/// The task sheet's edge swipe (#5603): only a mostly horizontal, long enough swipe from the leading edge closes it.
struct TaskSheetEdgeSwipeTests {
    @Test func rightSwipeFromTheEdgeCloses() {
        #expect(TaskDetailSheet.edgeSwipeCloses(start: CGPoint(x: 5, y: 300), translation: CGSize(width: 120, height: 10)))
    }
    @Test func otherDragsDoNot() {
        #expect(!TaskDetailSheet.edgeSwipeCloses(start: CGPoint(x: 100, y: 300), translation: CGSize(width: 200, height: 0)))
        #expect(!TaskDetailSheet.edgeSwipeCloses(start: CGPoint(x: 5, y: 300), translation: CGSize(width: 30, height: 0)))
        #expect(!TaskDetailSheet.edgeSwipeCloses(start: CGPoint(x: 5, y: 300), translation: CGSize(width: 120, height: 200)))
        #expect(!TaskDetailSheet.edgeSwipeCloses(start: CGPoint(x: 5, y: 300), translation: CGSize(width: -120, height: 0)))
    }
}
