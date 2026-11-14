import Foundation
import SwiftUI
import Testing
@testable import Persona
import PersonaWire

/// The hub's direction tree (#4352): how it decodes, how it is read, and the rows the sidebar, the To-dos page and the
/// Agents page draw from it. The app groups nothing itself: these check that it keeps the tree's order and words.
@Suite(.serialized)
@MainActor
struct DirectionTreeTests {
    private static let json = """
    {"directions":[
      {"id":"lead-a","name":"Product","counts":{"working":1,"needs_you":1,"stuck":0,"review":1},"running":true,
       "tasks":[
         {"id":"t1","title":"Ship it","state":"needs_you","agents":[
           {"machine_id":"m","session_id":"s1","title":"Builder","agent":"claude","state":"needs_you","children":[
             {"machine_id":"m","session_id":"s1@sub","title":"Explore","agent":"claude","state":"working"}]}]},
         {"id":"t2","title":"Check it","state":"review","agents":[]},
         {"id":"t3","title":"Later","state":"thinking"}]},
      {"id":"lead-b","name":"Ops","counts":{"working":0,"needs_you":0},"tasks":[]},
      {"id":"main","name":"","secretary":true,"counts":{"working":1,"needs_you":0,"stuck":1,"review":0},"running":false,
       "tasks":[{"id":"t4","title":"","state":"stuck","agents":[{"machine_id":"m","session_id":"s4","state":""}]}]}
    ]}
    """

    private static func tree() throws -> DirectionTree {
        try JSON.decoder.decode(DirectionTree.self, from: Data(json.utf8))
    }

    private func session(_ id: String, _ state: String, machine: String = "m") -> Session {
        let json = """
        {"machine_id":"\(machine)","session_id":"\(id)","title":"\(id)","cwd":"/tmp/p","status":"idle","state":"\(state)"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    // MARK: Decoding and reading

    @Test func decodesTheTreeLeniently() throws {
        let t = try Self.tree()
        #expect(t.directions.map(\.id) == ["lead-a", "lead-b", "main"])
        let a = t.directions[0]
        #expect(a.name == "Product" && !a.secretary)
        #expect(a.counts == DirectionTree.Counts(working: 1, needsYou: 1, stuck: 0, review: 1))
        #expect(a.tasks.map(\.state) == [.needsYou, .review, .working], "a state this app does not know reads as working")
        #expect(a.tasks[2].agents.isEmpty, "agents left out are none")
        let builder = try #require(a.tasks[0].agents.first)
        #expect(builder.id == "m/s1" && builder.agentState == .needsYou)
        #expect(builder.children.map(\.sessionID) == ["s1@sub"])
        #expect(builder.children[0].children.isEmpty)
        #expect(t.directions[1].counts == DirectionTree.Counts(), "missing counts are 0")
        let secretary = t.directions[2]
        #expect(secretary.secretary && secretary.tasks[0].agents[0].agentState == .unknown)
        #expect(try JSON.decoder.decode(DirectionTree.self, from: Data("{}".utf8)).directions.isEmpty)
    }

    /// The hub's state values as the wire spells them (`working`, not the old `doing`).
    @Test func decodesEachStateValue() throws {
        let states = try JSON.decoder.decode([DirectionTree.TaskState].self,
                                             from: Data(#"["needs_you","review","stuck","working","planned"]"#.utf8))
        #expect(states == [.needsYou, .review, .stuck, .working, .planned])
        #expect(DirectionTree.TaskState.working.rawValue == "working")
        #expect(DirectionTree.TaskState.planned.rawValue == "planned")
    }

    /// #5860: a leader's piece no Agent has worked on is `planned`, with its own count; an older hub omits both.
    @Test func plannedPiecesDecodeAsNotStartedAndOlderHubsReadZero() throws {
        let withPlanned = """
        {"directions":[{"id":"lead","name":"Product",
          "counts":{"working":1,"needs_you":0,"stuck":0,"review":0,"planned":2},
          "tasks":[{"id":"t1","title":"Ship","state":"working"},
                   {"id":"t2","title":"Piece","state":"planned"}]}]}
        """
        let tree = try JSON.decoder.decode(DirectionTree.self, from: Data(withPlanned.utf8))
        let lead = try #require(tree.directions.first)
        #expect(lead.counts.planned == 2)
        #expect(lead.tasks.map(\.state) == [.working, .planned])
        #expect(lead.tasks[1].state.text == "未开始")
        // The row's words ignore planned, as on the web; nothing waits on you here.
        #expect(lead.counts.label == nil)
        let older = try JSON.decoder.decode(DirectionTree.Counts.self, from: Data(#"{"working":1}"#.utf8))
        #expect(older.planned == 0)
    }

    /// #6626: a direction runs when an Agent under it is at work, as the hub sends it, whatever its counts; from a hub
    /// without `running`, when it counts a working to-do (what the rows read before).
    @Test func runningIsTheHubsAndAnOlderHubReadsTheWorkingCount() throws {
        func running(_ direction: String) throws -> Bool {
            try JSON.decoder.decode(DirectionTree.Direction.self, from: Data(direction.utf8)).running
        }
        #expect(try running(#"{"id":"a","counts":{"working":3},"running":false}"#) == false)
        #expect(try running(#"{"id":"a","counts":{"needs_you":1},"running":true}"#) == true)
        #expect(try running(#"{"id":"a","counts":{"working":1}}"#) == true)
        #expect(try running(#"{"id":"a","counts":{"needs_you":1}}"#) == false)
        #expect(try running(#"{"id":"a"}"#) == false)
    }

    @Test func fetchesTheTreeAndAnOlderHubHasNone() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/directions/tree", json: Self.json)
        let tree = try #require(try await hub.client.directionsTree())
        #expect(tree.directions.count == 3)
        #expect(hub.sent("GET", "/api/v1/assistant/directions/tree").count == 1)
        // A hub before the tree: no route, a 404.
        #expect(try await FakeHub().client.directionsTree() == nil)
    }

    @Test func theDirectionsEventIsDecoded() throws {
        guard case .directions = try #require(ServerEvent.decode(Data(#"{"type":"directions"}"#.utf8))) else {
            Issue.record("not .directions")
            return
        }
    }

    // MARK: Words

    /// The owner's words for a to-do's state and a direction's counts (the scheme runs in zh-Hans).
    @Test func wordsAreTheOwners() throws {
        #expect(DirectionTree.TaskState.working.text == "在做")
        #expect(DirectionTree.TaskState.needsYou.text == "等你")
        #expect(DirectionTree.TaskState.stuck.text == "卡住")
        #expect(DirectionTree.TaskState.review.text == "待验收")
        #expect(DirectionTree.TaskState.planned.text == "未开始")
        // A direction's words are the sidebar's lead-row words (#4331): what waits on you, never 「在做 N」 (#6626: what
        // runs is the row's spinner alone).
        #expect(DirectionTree.Counts(working: 2, needsYou: 1).label == "等你 1")
        #expect(DirectionTree.Counts(working: 3).label == nil)
        #expect(DirectionTree.Counts(needsYou: 4, stuck: 9).label == "等你 4")
        #expect(DirectionTree.Counts(stuck: 1, review: 1).label == nil)
        let t = try Self.tree()
        #expect(t.directions[2].title() == "书记官")
        #expect(t.directions[1].title() == "Ops")
        #expect(DirectionTree.Direction(id: "x").title(fallback: "Named") == "Named")
        #expect(t.directions[2].tasks[0].displayTitle == "未命名待办")
    }

    // MARK: The sidebar

    /// The sidebar lists the leaders alone (the main conversation's row stands for the secretary group), with the
    /// names, counts and running the lead rows show.
    @Test func leadersNamesAndCountsForTheSidebar() throws {
        let t = try Self.tree()
        #expect(t.leader("lead-a")?.name == "Product")
        #expect(t.leader("main") == nil, "the secretary group is not a leader's")
        #expect(t.leaders.map(\.id) == ["lead-a", "lead-b"])
        #expect(t.leaderNames == ["lead-a": "Product", "lead-b": "Ops"])
        #expect(t.leader("lead-a")?.counts == DirectionTree.Counts(working: 1, needsYou: 1, stuck: 0, review: 1))
        #expect(t.leader("lead-a")?.running == true)
        #expect(t.leader("lead-b")?.running == false, "an older hub's direction with no working to-do")
        #expect(t.withTasks.map(\.id) == ["lead-a", "main"])
    }

    /// A to-do's Agents fold: a sub-Agent shows only while its parent is open; every node is open on the Agents page.
    @Test func agentLinesFollowTheFolds() throws {
        let task = try Self.tree().directions[0].tasks[0]
        #expect(task.agentLines(opened: []).map(\.node.sessionID) == ["s1"])
        #expect(task.agentLines(opened: []).first?.hasChildren == true)
        let open = task.agentLines(opened: ["m/s1"])
        #expect(open.map(\.node.sessionID) == ["s1", "s1@sub"] && open.map(\.depth) == [0, 1])
        #expect(task.agentLines().count == 2)
        let twice = DirectionTree.TaskNode(id: "t", agents: [.init(machineID: "m", sessionID: "a"), .init(machineID: "m", sessionID: "a")])
        #expect(twice.agentLines().count == 1, "an Agent shows once")
    }

    @Test func eachDirectionRemembersItsFoldApart() {
        #expect(AssistantSidebarDirection<EmptyView>.foldKey("lead-a") != AssistantSidebarDirection<EmptyView>.foldKey("lead-b"))
        #expect(AssistantSidebarDirection<EmptyView>.foldKey("lead-a").hasPrefix("assistant.direction.open."))
    }

    // MARK: The To-dos page

    @Test func todoSectionsKeepTheTreesOrder() throws {
        let t = try Self.tree()
        let todos = Dictionary(uniqueKeysWithValues: ["t1", "t3", "t4"].map { ($0, TodoItem(id: $0, title: "Title \($0)")) })
        let sections = DirectionTodoSections.build(t, todos: todos)
        #expect(sections.map(\.id) == ["lead-a", "main"], "a direction without to-dos is left out")
        #expect(sections[0].todos.map(\.id) == ["t1", "t3"], "a to-do this device does not hold is dropped")
        let searched = DirectionTodoSections.build(t, todos: todos) { $0.id == "t4" }
        #expect(searched.map(\.id) == ["main"], "a direction the search empties is left out")
    }

    @Test func todosGroupingFallsBackToStatusWithoutATree() {
        #expect(TodosGrouping.resolved(.direction, hasTree: false) == .status, "kept from another hub, an older one has no tree")
        #expect(TodosGrouping.resolved(.direction, hasTree: true) == .direction)
        #expect(TodosGrouping.resolved(.status, hasTree: true) == .status)
        #expect(TodosGrouping.resolved(.status, hasTree: false) == .status)
    }

    // MARK: The Agents page

    @Test func agentSectionsPlaceTheTreesAgentsAndKeepTheRest() throws {
        let t = try Self.tree()
        let s1 = session("s1", "needs_you"), s4 = session("s4", "working"), loose = session("loose", "working")
        let pinned = session("pinned", "idle")
        let shown = [s1, s4, loose]
        let known = Set((shown + [pinned]).map(\.key))
        let layout = DirectionAgentSections.build(t, shown: shown, known: known, group: "", isMuted: { _ in false })
        #expect(layout.entries.map(\.key) == ["direction:lead-a", "direction:main"])
        let ship = layout.entries[0].tasks[0]
        #expect(ship.lines.map(\.node.sessionID) == ["s1", "s1@sub"])
        #expect(ship.lines[0].session?.key == s1.key)
        #expect(ship.lines[1].session == nil && ship.lines[1].depth == 1, "a sub-Agent the list does not have is drawn from the tree")
        #expect(layout.entries[0].tasks.map(\.task.id) == ["t1", "t2", "t3"], "a to-do with no Agent still shows")
        #expect(layout.entries[1].tasks[0].lines.map(\.session?.key) == [s4.key])
        #expect(layout.others.flatMap(\.sessions).map(\.sessionID) == ["loose"], "no Agent disappears")
        #expect(layout.others.first { $0.key == "todo:none" }?.folded == true, "Unassigned folds under the directions")
        #expect(layout.foldIDs == ["direction:lead-a", "direction:main", "todo:none"])
    }

    @Test func aFilterLeavesOutWhatItHides() throws {
        let t = try Self.tree()
        let s4 = session("s4", "working")
        // s1 is known but filtered out (another machine picked, a search): it stays out, and so does its to-do.
        let known = Set([session("s1", "needs_you"), s4].map(\.key))
        let layout = DirectionAgentSections.build(t, shown: [s4], known: known, group: "", filtering: true, isMuted: { _ in false })
        #expect(layout.entries.map(\.key) == ["direction:main"])
        #expect(layout.entries[0].tasks.map(\.task.id) == ["t4"])
    }

    @Test func unassignedStaysOpenWhileOneNeedsYou() throws {
        let waiting = session("waiting", "needs_you")
        let layout = DirectionAgentSections.build(try Self.tree(), shown: [waiting], known: [waiting.key], group: "", isMuted: { _ in false })
        #expect(layout.others.first { $0.key == "todo:none" }?.folded == false)
    }

    @Test func theMacsArrowsFollowTheSections() throws {
        let t = try Self.tree()
        let s1 = session("s1", "needs_you"), s4 = session("s4", "working"), loose = session("loose", "working")
        let layout = DirectionAgentSections.build(t, shown: [s1, s4, loose], known: Set([s1, s4, loose].map(\.key)), group: "",
                                                  isMuted: { _ in false })
        func rows(_ folded: Set<String>) -> [String] {
            AgentListRows(order: AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false }), grouping: .direction,
                          isFolded: { folded.contains($0) })
                .directions(layout) { folded.contains($0.key) }.map(\.sessionID)
        }
        #expect(rows([]) == ["s1", "s4", "loose"])
        #expect(rows(["direction:lead-a"]) == ["s4", "loose"])
        #expect(rows(["todo:none"]) == ["s1", "s4"])
    }

    @Test func byPinnedIsOfferedOnlyWithATree() {
        #expect(!DevGrouping.offered(supportsTodos: true).contains(.direction))
        #expect(DevGrouping.offered(supportsTodos: true, supportsDirections: true).last == .direction)
        #expect(DevGrouping.resolved(.direction, supportsTodos: true) == .todo)
        #expect(DevGrouping.resolved(.direction, supportsTodos: false) == .space)
        #expect(DevGrouping.resolved(.direction, supportsTodos: true, supportsDirections: true) == .direction)
        #expect(DevGrouping.standard == .repo, "not the default")
        #expect(DevGrouping.direction.title == "按常驻")
    }
}
