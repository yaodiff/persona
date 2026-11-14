import Foundation
import Testing
@testable import Persona
import PersonaWire

struct DispatchedTasksTests {
    private func task(_ title: String, id: String? = nil, error: String? = nil) -> PlanTask {
        PlanTask(title: title, prompt: "Work", machine: "computer", cwd: "/home/user/project", todoID: id, error: error)
    }

    private func plan(_ tasks: [PlanTask], conversation: String = "main", state: TaskPlan.State = .started) -> TaskPlan {
        TaskPlan(id: "plan", conversation: conversation, tasks: tasks, state: state)
    }

    private func feed(_ kind: FeedItem.Kind, ref: String = "plan", body: JSONValue? = nil) -> FeedItem {
        FeedItem(id: "feed", conversation: "main", at: .now, kind: kind, ref: ref, body: body)
    }

    @Test func macDetailSelectionOpensTheRequestedSessionNotAnotherLead() {
        let first = SessionKey(group: "hub", machineID: "computer", sessionID: "first")
        let second = SessionKey(group: "hub", machineID: "computer", sessionID: "second")
        #expect(DispatchedTasks.selectedAgent(first, available: [first, second], lead: second) == first)
        #expect(DispatchedTasks.selectedAgent(second, available: [first, second], lead: first) == second)
    }

    @Test func macDetailSelectionRejectsAStaleOrOtherHubTarget() {
        let current = SessionKey(group: "hub", machineID: "computer", sessionID: "same-id")
        let otherHub = SessionKey(group: "other-hub", machineID: "computer", sessionID: "same-id")
        #expect(DispatchedTasks.selectedAgent(otherHub, available: [current], lead: current) == current)
        #expect(DispatchedTasks.selectedAgent(otherHub, available: [current], lead: otherHub) == nil)
    }

    @Test func macDetailSelectionWithoutAnAgentStaysOnTaskDetails() {
        let missing = SessionKey(machineID: "computer", sessionID: "missing")
        #expect(DispatchedTasks.selectedAgent(missing, available: [], lead: missing) == nil)
    }

    @Test func oneTaskKeepsItsExactTodoTarget() throws {
        let entries = try #require(DispatchedTasks.entries(for: feed(.task, ref: "new"), plans: [:],
                                                          titleOf: { $0 == "new" ? "Current title" : nil }, fallback: "Task"))
        #expect(entries == [.init(id: "todo:new", todoID: "new", title: "Current title", conversation: "main")])
    }

    @Test func severalTasksKeepPlanOrderAndLiveTitles() throws {
        let p = plan([task("First", id: "one"), task("Second", id: "two"), task("Failed", error: "Unavailable")])
        let entries = try #require(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p],
                                                          titleOf: { $0 == "two" ? "Renamed task" : nil }, fallback: "Task"))
        #expect(entries.map(\.title) == ["First", "Renamed task", "Failed"])
        #expect(entries.map(\.todoID) == ["one", "two", nil])
        #expect(entries.map(\.id) == ["plan:plan:0", "plan:plan:1", "plan:plan:2"])
    }

    @Test func planTasksHaveNoDuplicateFeedLink() {
        let p = plan([task("Planned", id: "one")])
        #expect(DispatchedTasks.entries(for: feed(.task, ref: "one"), plans: [p.id: p], titleOf: { _ in nil }, fallback: "Task") == [])
    }

    @Test func anotherConversationDoesNotHideAStandaloneLink() throws {
        let p = plan([task("Elsewhere", id: "one")], conversation: "other")
        let entries = try #require(DispatchedTasks.entries(for: feed(.task, ref: "one"), plans: [p.id: p],
                                                          titleOf: { _ in "Standalone" }, fallback: "Task"))
        #expect(entries.map(\.todoID) == ["one"])
        #expect(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p], titleOf: { _ in nil }, fallback: "Task") == nil)
    }

    @Test func aMissingTodoRetainsItsTargetUntilItLoads() throws {
        let entries = try #require(DispatchedTasks.entries(for: feed(.task, ref: "loading"), plans: [:], titleOf: { _ in nil }, fallback: "Task"))
        #expect(entries.first?.todoID == "loading")
        #expect(entries.first?.title == "Task")
    }

    @Test func aPlanInTheFeedBodyWorksBeforeItsEventArrives() throws {
        let p = plan([task("Body title", id: "one")])
        let value = try JSON.decoder.decode(JSONValue.self, from: JSONEncoder().encode(p))
        let entries = try #require(DispatchedTasks.entries(for: feed(.plan, body: .object(["plan": value])), plans: [:],
                                                          titleOf: { _ in nil }, fallback: "Task"))
        #expect(entries.map(\.todoID) == ["one"])
    }

    @MainActor @Test func aDispatchDoesNotAbsorbItsRoutedHandoff() {
        let task = feed(.task, ref: "one")
        var routed = feed(.routed, ref: "one", body: .object(["from": .string("persona"), "text": .string("Work instructions")]))
        routed.id = "handoff"
        routed.at = task.at.addingTimeInterval(1)
        let lines = ChatLines.mergeFeed([], [task, routed])
        #expect(lines.count == 2)
        #expect(DispatchedTasks.entries(for: routed, plans: [:], titleOf: { _ in nil }, fallback: "Task") == nil)
    }

    private func agent(_ session: String, at: Date, error: String? = nil) -> TodoItem.Agent {
        TodoItem.Agent(machineID: "computer", sessionID: session, title: "Layout \(session)", cwd: nil, agent: nil,
                       startError: error, releasedAt: nil, linkedAt: at, source: nil, via: nil)
    }

    @Test func oneAgentIsOneCardThatOpensThatAgent() {
        let at = Date(timeIntervalSince1970: 1_700_000_000)
        let todo = TodoItem(id: "t", title: "Task", agents: [agent("agent", at: at)])
        let cards = DispatchedTasks.cards(of: .init(id: "todo:t", todoID: "t", title: "Task", wake: "created", conversation: "main"), todo: todo)
        #expect(cards.count == 1)
        #expect(cards[0].id == "computer/agent")
        #expect(cards[0].machineID == "computer")
        #expect(cards[0].sessionID == "agent")
        #expect(cards[0].agentTitle == "Layout agent")
        #expect(cards[0].wake == "created")
        #expect(cards[0].todoID == "t")
    }

    @Test func agentsLinkedTogetherAreEachACardAndALaterOneIsNot() {
        let first = Date(timeIntervalSince1970: 1_700_000_000)
        let todo = TodoItem(id: "t", title: "Task", agents: [
            agent("a", at: first),
            agent("b", at: first.addingTimeInterval(60)),
            agent("later", at: first.addingTimeInterval(180)),
        ])
        let cards = DispatchedTasks.cards(of: .init(id: "todo:t", todoID: "t", title: "Task"), todo: todo)
        #expect(cards.map(\.sessionID) == ["a", "b"])
    }

    @Test func anAgentThatDidNotStartLeavesTheTaskCard() {
        let todo = TodoItem(id: "t", title: "Task", agents: [agent("a", at: .now, error: "offline")])
        let entry = DispatchedTasks.Entry(id: "todo:t", todoID: "t", title: "Task", error: "offline")
        #expect(DispatchedTasks.cards(of: entry, todo: todo) == [entry])
        #expect(DispatchedTasks.cards(of: entry, todo: nil) == [entry])
    }

    @Test func theTitleIsTheSessionsThenTheAgentsThenTheTasks() {
        #expect(DispatchedTasks.cardTitle(sessionTitle: "Claude Code", agentTitle: "Layout", taskTitle: "Task") == "Claude Code")
        #expect(DispatchedTasks.cardTitle(sessionTitle: "  ", agentTitle: " Layout ", taskTitle: "Task") == "Layout")
        #expect(DispatchedTasks.cardTitle(sessionTitle: nil, agentTitle: " ", taskTitle: "Task") == "Task")
    }

    @Test func aPlanTasksStayNewWhenTheirTaskItemSaysOtherwise() throws {
        let p = plan([task("Planned", id: "one")])
        let entries = try #require(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p],
                                                          titleOf: { _ in "Planned" }, fallback: "Task"))
        #expect(entries.allSatisfy { $0.wake == nil })
        let active = feed(.task, ref: "one", body: .object(["wake": .string("active")]))
        #expect(DispatchedTasks.entries(for: active, plans: [p.id: p], titleOf: { _ in nil }, fallback: "Task") == [])
        let alone = try #require(DispatchedTasks.entries(for: feed(.task, ref: "other", body: .object(["wake": .string("resumed")])),
                                                         plans: [:], titleOf: { _ in "Other" }, fallback: "Task"))
        #expect(alone.first?.wake == "resumed")
    }

    @Test func decisionsAndDismissedPlansKeepTheirNormalCards() {
        for state in [TaskPlan.State.open, .dismissed, .unknown] {
            let p = plan([task("Proposed")], state: state)
            #expect(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p], titleOf: { _ in nil }, fallback: "Task") == nil)
        }
        #expect(DispatchedTasks.entries(for: feed(.message), plans: [:], titleOf: { _ in nil }, fallback: "Task") == nil)
    }
}

/// A failed start says why once, on the receipt when the conversation has one for the task (the web's `withFailureBelow`).
@MainActor
struct DispatchHandoffFailureTests {
    private func feed(_ json: String) throws -> FeedItem { try JSON.decoder.decode(FeedItem.self, from: Data(json.utf8)) }

    @Test func aFailedReceiptForTheTaskTakesTheReasonOffTheHandoffCard() throws {
        let failed = try feed(#"{"id":"r1","conversation":"c","at":"2026-11-13T09:00:00Z","kind":"receipt","ref":"t1","body":{"state":"failed","title":"x"}}"#)
        let done = try feed(#"{"id":"r2","conversation":"c","at":"2026-11-13T09:00:00Z","kind":"receipt","ref":"t2","body":{"state":"done","title":"x"}}"#)
        #expect(DispatchHandoffCard.hasFailedReceipt([failed, done], todo: "t1"))
        #expect(!DispatchHandoffCard.hasFailedReceipt([failed, done], todo: "t2"))
        #expect(!DispatchHandoffCard.hasFailedReceipt([], todo: "t1"))
    }

    @Test func aFailedStartWithAReceiptBelowIsOnlyDidntStart() {
        #expect(DispatchHandoffCard.failedMeta("boom", receiptBelow: true) == .failedBelow)
        #expect(DispatchHandoffCard.failedMeta("boom", receiptBelow: false) == .failed("boom"))
    }

    @Test func theUsageLimitBranchNeedsAStoppedAgentAndAnOpenTaskThatDoesNotNeedYou() throws {
        let limited = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"mac","session_id":"s","title":"s","cwd":"/p","state":"idle","status":"idle","updated_at":"2026-11-13T09:00:00Z","turn_error":"You've hit your usage limit.","turn_error_kind":"usage_limit"}"#.utf8))
        #expect(DispatchHandoffCard.limited(state: .working, closed: false, lead: limited))
        #expect(!DispatchHandoffCard.limited(state: .needsYou, closed: false, lead: limited))
        #expect(!DispatchHandoffCard.limited(state: .working, closed: true, lead: limited))
    }
}
