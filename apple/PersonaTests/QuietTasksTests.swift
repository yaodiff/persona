import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Tasks the main conversation starts with "notify about main-chat tasks only in the main chat" on (#1731): the hub's `quiet` decodes (missing is
/// false, for a hub from before), and their agents stay off the Live Activity, the watch and the To-dos badge.
@MainActor
struct QuietTasksTests {
    private func decode<T: Decodable>(_ json: String, as: T.Type) throws -> T { try JSON.decoder.decode(T.self, from: Data(json.utf8)) }

    private func session(_ id: String, machine: String = "box", state: String = "working", quiet: Bool? = nil,
                         todo: String? = nil) throws -> Session {
        var o: [String: Any] = ["machine_id": machine, "session_id": id, "title": "agent \(id)", "cwd": "/code/\(id)",
                                "status": "idle", "state": state, "started_by_persona": true, "assistant": false,
                                "pending_interactions": 0]
        if let quiet { o["quiet"] = quiet }
        if let todo { o["todo"] = ["id": todo, "title": "t"] }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private func todo(_ id: String, quiet: Bool, agents: [(String, String, released: Bool)] = [], done: Bool = false) -> TodoItem {
        TodoItem(id: id, title: id, doneAt: done ? Date() : nil, origin: "main",
                 agents: agents.map { .init(machineID: $0.0, sessionID: $0.1, releasedAt: $0.released ? Date() : nil) },
                 quiet: quiet)
    }

    @Test func todoAndSessionDecodeQuietDefaultingToFalse() throws {
        let todos = try decode(#"""
        {"todos":[{"id":"t1","title":"修崩溃","agents":[],"quiet":true},{"id":"t2","title":"旧 hub 的","agents":[]}]}
        """#, as: [String: [TodoItem]].self)["todos"] ?? []
        #expect(todos.map(\.quiet) == [true, false])
        #expect(try session("s1", quiet: true).quiet)
        #expect(try !session("s2").quiet)
        // Kept through the app's own caches (StateCache, the watch summary).
        let again = try JSON.decoder.decode(TodoItem.self, from: JSON.encoder.encode(todos[0]))
        #expect(again.quiet)
        let s = try JSON.decoder.decode(Session.self, from: JSON.encoder.encode(try session("s1", quiet: true)))
        #expect(s.quiet)
    }

    @Test func assistantSettingDecodesAndIsSentOnlyWhenSet() throws {
        let on = try decode(#"{"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[],"main_tasks_quiet":true}"#,
                            as: APIClient.AssistantState.self)
        #expect(on.mainTasksQuiet == true)
        let old = try decode(#"{"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[]}"#, as: APIClient.AssistantState.self)
        #expect(old.mainTasksQuiet == nil) // no switch for a hub without it
        let off = try JSONSerialization.jsonObject(with: JSONEncoder().encode(APIClient.AssistantConfigBody(mainTasksQuiet: false))) as? [String: Any]
        #expect(off?["main_tasks_quiet"] as? Bool == false)
        let other = try JSONSerialization.jsonObject(with: JSONEncoder().encode(APIClient.AssistantConfigBody(timezone: "Asia/Shanghai"))) as? [String: Any]
        #expect(other?["main_tasks_quiet"] == nil) // changing another field leaves it alone
    }

    @Test func coversByTheHubsWordTheTodoOrItsAgents() throws {
        let q = QuietTasks([todo("t1", quiet: true, agents: [("box", "a", released: false), ("mac", "b", released: true)]),
                            todo("t2", quiet: false, agents: [("box", "c", released: false)])])
        #expect(q.todos == ["t1"])
        #expect(try q.covers(session("x", quiet: true)))      // the hub said so
        #expect(try q.covers(session("y", todo: "t1")))       // its todo is quiet
        #expect(try q.covers(session("a")))                   // a quiet todo's agent
        #expect(try q.covers(session("b", machine: "mac")))   // released after the task: its finish stays quiet
        #expect(try !q.covers(session("c", todo: "t2")))      // a task of another conversation
        #expect(try !q.covers(session("a", machine: "mac")))  // same session id on another machine
        #expect(q.covers(machine: "box", session: "a") && !q.covers(machine: "box", session: "c"))
    }

    @Test func watchListsLeaveOutQuietAgentsAndWhatTheyAsk() throws {
        let sessions = [try session("a", quiet: true), try session("b")]
        let asking = [Interaction(id: "ix1", machineID: "box", sessionID: "a", kind: .plan),
                      Interaction(id: "ix2", machineID: "box", sessionID: "b", kind: .question)]
        let (s, i) = QuietTasks.leavingOut(sessions: sessions, interactions: asking, quiet: \.quiet)
        #expect(s.map(\.sessionID) == ["b"])
        #expect(i.map(\.id) == ["ix2"])
        // Nothing quiet: the same lists.
        let (s2, i2) = QuietTasks.leavingOut(sessions: [try session("b")], interactions: asking, quiet: \.quiet)
        #expect(s2.count == 1 && i2.count == 2)
    }

    @Test func fleetLeavesOutQuietItemsAndWorksOutThePhaseAgain() {
        let ask = FleetState.Item(id: "box/a", title: "quiet", kind: .permission, interaction: "ix1")
        let work = FleetState.Item(id: "box/b", title: "quiet too", kind: .working, since: 100)
        let done = FleetState.Item(id: "box/c", title: "loud", kind: .unread)
        let fleet = FleetState(phase: .needsYou, needsYou: 1, working: 1, unread: 1, items: [ask, work, done], workingSince: 100)
        let out = fleet.leavingOut { $0.id != "box/c" }
        #expect(out.items.map(\.id) == ["box/c"])
        #expect(out.needsYou == 0 && out.working == 0 && out.unread == 1)
        #expect(out.phase == .done && out.workingSince == nil)
        // Only quiet ones: nothing left, the activity ends as for no agents.
        #expect(fleet.leavingOut { _ in true }.isEmpty)
        // None quiet: untouched (a hub that already left them out).
        #expect(fleet.leavingOut { _ in false } == fleet)
        // An agent counted but not among the items stays counted.
        let more = FleetState(phase: .working, working: 3, items: [work], workingSince: 100)
        let left = more.leavingOut { _ in true }
        #expect(left.working == 2 && left.phase == .working && left.workingSince == 100)
    }

    @Test func todoBadgeLeavesOutQuietTasks() {
        let store = TodoStore(cachedTodos: [todo("t1", quiet: true), todo("t2", quiet: false), todo("t3", quiet: false, done: true)],
                              cacheWriter: nil, hubListChanged: {})
        #expect(store.badgeCount == 1)
        #expect(store.openTodos.count == 2) // the list itself still has the quiet one
        #expect(store.quiet.todos == ["t1"])
    }
}
