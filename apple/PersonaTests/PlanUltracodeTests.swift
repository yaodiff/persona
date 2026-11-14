import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Claude Code's ultracode on plan cards and to-dos (#12): the card shows each task's and may change it before Start,
/// Start sends only the tasks whose choice changed (a plain Start sends no `tasks`), a computer whose Claude Code is too
/// old starts without it, and a to-do carries the Agent list row's mark. The switch's own rules are `UltracodeTests`.
@MainActor
struct PlanUltracodeTests {
    private func decode<T: Decodable>(_ json: String, as: T.Type) throws -> T { try JSON.decoder.decode(T.self, from: Data(json.utf8)) }

    private func task(_ title: String, machine: String = "box", ultracode: Bool? = nil, todo: String? = nil) -> PlanTask {
        PlanTask(title: title, prompt: "…", machine: machine, cwd: "/code/app", ultracode: ultracode, todoID: todo)
    }

    private func plan(_ tasks: [PlanTask]) -> TaskPlan {
        TaskPlan(id: "plan_1", conversation: "c1", tasks: tasks, state: .open)
    }

    private func session(_ id: String, ultracode: Bool?, state: String = "working", agent: String? = nil,
                         assistant: Bool = false) throws -> Session {
        var o: [String: Any] = ["machine_id": "box", "session_id": id, "title": "t", "cwd": "/code/app", "status": "working",
                                "state": state, "assistant": assistant]
        if let ultracode { o["ultracode"] = ultracode }
        if let agent { o["agent"] = agent }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private func todo(agents: [TodoItem.Agent] = [], done: Bool = false, startError: String? = nil) -> TodoItem {
        TodoItem(id: "todo_1", title: "Fix the login crash", doneAt: done ? Date() : nil, planID: "plan_1", startError: startError,
                 agents: agents)
    }

    private func agent(_ id: String, released: Bool = false) -> TodoItem.Agent {
        TodoItem.Agent(machineID: "box", sessionID: id, releasedAt: released ? Date() : nil)
    }

    // MARK: What Start sends

    @Test func toSendLeavesOutTheAssistantsOwnChoice() {
        let on = task("a", ultracode: true), off = task("b", ultracode: false), unset = task("c")
        #expect(Ultracode.toSend(planned: on, on: true, blocked: false) == nil)
        #expect(Ultracode.toSend(planned: on, on: false, blocked: false) == false)
        #expect(Ultracode.toSend(planned: off, on: true, blocked: false) == true)
        #expect(Ultracode.toSend(planned: unset, on: false, blocked: false) == nil)
        #expect(Ultracode.toSend(planned: unset, on: true, blocked: false) == true)
        // A computer that can't: the assistant's on goes out as off, a choice of on is not sent.
        #expect(Ultracode.toSend(planned: on, on: true, blocked: true) == false)
        #expect(Ultracode.toSend(planned: off, on: true, blocked: true) == nil)
    }

    @Test func choicesAreTheCardsWhereItHasOne() {
        let p = plan([task("a", ultracode: true), task("b"), task("c", ultracode: false)])
        #expect(Ultracode.choices(p, card: [:]) == [true, false, false])
        #expect(Ultracode.choices(p, card: [0: false, 2: true]) == [false, false, true])
    }

    @Test func aPlainStartSendsNoTasks() {
        let p = plan([task("a", ultracode: true), task("b")])
        #expect(Ultracode.cardEdits(p, choices: Ultracode.choices(p, card: [:]), blocked: { _ in false }) == nil)
        // Turned back to what it was: still nothing to send.
        #expect(Ultracode.cardEdits(p, choices: Ultracode.choices(p, card: [0: true, 1: false]), blocked: { _ in false }) == nil)
    }

    @Test func cardStartCarriesUltracodeOnlyOnChangedTasks() throws {
        let p = plan([task("a", ultracode: true), task("b"), task("c", ultracode: true)])
        let edits = try #require(Ultracode.cardEdits(p, choices: [true, true, false], blocked: { _ in false }))
        #expect(edits.map(\.ultracode) == [nil, true, false])
        #expect(edits.map(\.title) == ["a", "b", "c"])
    }

    @Test func aBlockedComputerStartsWithoutIt() throws {
        let p = plan([task("a", machine: "old", ultracode: true), task("b", machine: "new", ultracode: true)])
        let edits = try #require(Ultracode.cardEdits(p, choices: Ultracode.choices(p, card: [:]), blocked: { $0 == "old" }))
        #expect(edits.map(\.ultracode) == [false, nil])
    }

    @Test func editStartsFromTheCardsChoices() {
        let p = plan([task("a", ultracode: true), task("b")])
        let start = Ultracode.editsOf(p, choices: Ultracode.choices(p, card: [0: false, 1: true]))
        #expect(start.map(\.ultracode) == [false, true])
        #expect(start.map(\.title) == ["a", "b"])
        // Opened and started as it was: nothing of ultracode goes out.
        let unchanged = Ultracode.editsOf(p, choices: Ultracode.choices(p, card: [:]))
        #expect(Ultracode.editsToSend(p, edits: unchanged, blocked: { _ in false }).map(\.ultracode) == [nil, nil])
    }

    @Test func editsToSendLeavesOutTheAssistantsOwn() {
        let p = plan([task("a", ultracode: true), task("b")])
        var edited = p.tasks
        edited[0].title = "a, reworded"
        edited[1].ultracode = true
        let sent = Ultracode.editsToSend(p, edits: edited, blocked: { _ in false })
        #expect(sent.map(\.ultracode) == [nil, true])
        #expect(sent[0].title == "a, reworded")
        // Moved to a computer that can't: off goes out for the one the assistant had on.
        edited[0].machine = "old"
        #expect(Ultracode.editsToSend(p, edits: edited, blocked: { $0 == "old" }).map(\.ultracode) == [false, true])
    }

    @Test func startPlanBodyLeavesOutUnsetUltracode() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/task-plans/plan_1/start",
               json: #"{"plan":{"id":"plan_1","conversation":"c1","state":"started","tasks":[]}}"#)
        let p = plan([task("a", ultracode: true), task("b")])
        let edits = try #require(Ultracode.cardEdits(p, choices: [false, false], blocked: { _ in false }))
        _ = try await hub.client.startPlan("plan_1", edits: edits)
        _ = try await hub.client.startPlan("plan_1")
        let sent = hub.sent("POST", "/api/v1/task-plans/plan_1/start")
        #expect(sent.count == 2)
        let firstBody = try #require(sent.first?.body)
        let first = try #require(try JSONSerialization.jsonObject(with: firstBody) as? [String: Any])
        let tasks = try #require(first["tasks"] as? [[String: Any]])
        #expect(tasks.count == 2)
        #expect(tasks[0]["ultracode"] as? Bool == false)
        #expect(tasks[1]["ultracode"] == nil)
        let plainBody = try #require(sent.last?.body)
        let plain = try #require(try JSONSerialization.jsonObject(with: plainBody) as? [String: Any])
        #expect(plain["tasks"] == nil)
    }

    // MARK: The to-do mark

    @Test func todoMarkFollowsItsAgentsThenThePlan() throws {
        let on = try session("s1", ultracode: true), off = try session("s2", ultracode: false)
        let lookup = TodosLogic.makeSessionLookup([on, off])
        #expect(Ultracode.todoShown(todo(agents: [agent("s2"), agent("s1")]), lookup: lookup))
        #expect(!Ultracode.todoShown(todo(agents: [agent("s2")]), lookup: lookup))
        // A released agent is no longer the to-do's.
        #expect(!Ultracode.todoShown(todo(agents: [agent("s1", released: true)]), lookup: lookup))
        // An agent the app has not seen yet shows nothing, whatever the plan said.
        #expect(!Ultracode.todoShown(todo(agents: [agent("s9")]), lookup: lookup, planned: task("a", ultracode: true)))
        // Before an agent started: the plan task's choice.
        #expect(Ultracode.todoShown(todo(), lookup: lookup, planned: task("a", ultracode: true)))
        #expect(!Ultracode.todoShown(todo(), lookup: lookup, planned: task("a")))
        #expect(!Ultracode.todoShown(todo(), lookup: lookup))
    }

    @Test func todoMarkIsTheAgentRowsMark() throws {
        // The same agents as `UltracodeTests`' row mark: offline, Codex and the assistant's never mark a to-do.
        for s in [try session("s1", ultracode: true, state: "offline"), try session("s1", ultracode: true, agent: "codex"),
                  try session("s1", ultracode: true, assistant: true)] {
            #expect(!Ultracode.rowMark(s))
            #expect(!Ultracode.todoShown(todo(agents: [agent("s1")]), lookup: TodosLogic.makeSessionLookup([s])))
        }
    }

    @Test func storeMarksATodoFromItsPlanBeforeItsAgentStarts() {
        let item = todo()
        let store = TasksStore(todoStore: TodoStore(cachedTodos: [item], cacheWriter: nil, hubListChanged: {}),
                               viewStore: AssistantViewsStore())
        #expect(!store.ultracode(item))
        store.received(plan([task("a", ultracode: false, todo: "todo_0"), task("b", ultracode: true, todo: "todo_1")]))
        #expect(store.ultracode(item))
        store.received(plan([task("a", ultracode: true, todo: "todo_0"), task("b", ultracode: false, todo: "todo_1")]))
        #expect(!store.ultracode(item))
    }

    @Test func noMarkOnADoneOrFailedTodo() throws {
        let lookup = TodosLogic.makeSessionLookup([try session("s1", ultracode: true)])
        #expect(!Ultracode.todoShown(todo(agents: [agent("s1")], done: true), lookup: lookup))
        #expect(!Ultracode.todoShown(todo(startError: "no such folder"), lookup: lookup, planned: task("a", ultracode: true)))
    }

    // MARK: Decoding

    @Test func planTaskDecodesWithAndWithoutUltracode() throws {
        let p = try decode(#"""
        {"id":"plan_1","conversation":"c1","state":"open","tasks":[
          {"title":"a","prompt":"…","machine":"box","cwd":"/code/app","ultracode":true},
          {"title":"b","prompt":"…","machine":"box","cwd":"/code/app","ultracode":false},
          {"title":"c","prompt":"…","machine":"box","cwd":"/code/app"}]}
        """#, as: TaskPlan.self)
        #expect(p.tasks.map(\.ultracode) == [true, false, nil])
    }
}
