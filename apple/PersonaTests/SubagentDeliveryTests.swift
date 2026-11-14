import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct SubagentDeliveryTests {
    private func use(_ id: String = "a", seq: Int = 2) -> Item {
        Item(id: "use-" + id, seq: seq, role: "assistant", kind: .toolUse, toolUseID: id,
             toolName: "Agent", toolInput: .object(["description": "Synthetic review"]))
    }

    private func delivery(_ seq: Int, status: String, text: String? = nil, preview: String? = nil, wake: Bool = false, call: String = "a") -> Item {
        Item(id: "delivery-\(seq)", seq: seq, role: "user", kind: .toolResult, text: text, toolUseID: call, wake: wake,
             task: TaskNote(kind: "agent", status: status, title: "Synthetic review", taskID: "worker-" + call,
                            toolUseID: call, detail: preview))
    }

    private func call(_ items: [Item]) -> ToolCall? {
        ChatStore.buildRows(items).compactMap { row -> ToolCall? in
            guard case .tools(let steps) = row else { return nil }
            for case .call(let call) in steps { return call }
            return nil
        }.first
    }

    @Test func decodesTheExistingTaskMetadataOnToolResults() throws {
        let raw = #"{"id":"report#0","seq":15,"role":"user","kind":"tool_result","tool_use_id":"a","text":"Still checking.","task":{"kind":"agent","task_id":"worker-a","tool_use_id":"a","title":"Synthetic review","status":"running","detail":"Still checking."}}"#
        let result = try JSON.decoder.decode(Item.self, from: Data(raw.utf8))
        #expect(result.task?.status == "running")
        #expect(result.toolUseID == result.task?.toolUseID)
        #expect(result.from == nil && result.queued == nil)
        #expect(TaskIndex.build([use(), result]).reports["a"]?.map(\.id) == ["report#0"])
    }

    @Test(arguments: ["running", "waiting", "future-status"])
    func nonterminalAndUnknownStatusesDoNotEndProgress(status: String) {
        let items = [use(), delivery(3, status: status, text: "Still checking.", preview: "Still checking.")]
        let tasks = TaskIndex.build(items)
        let agent = Subagent(id: "worker-a", sessionID: "s@worker-a", toolUseID: "a", running: false)
        let call = call(items)!
        #expect(SubagentCardStatus.of(call, tasks: tasks, agent: agent) == .running)
        #expect(agent.running(in: tasks))
        #expect(SubagentResult.call(call, tasks: tasks, agent: agent) == nil)
        #expect(SubagentResult.reports(call, tasks: tasks).count == 1)
    }

    @Test func independentReportsSurviveAStatusOnlyNotification() {
        let receipt = delivery(3, status: "running", text: "Async agent launched successfully.\nagentId: worker-a")
        let interim = delivery(4, status: "running", text: "Initial checks pass.", preview: "Initial checks pass.")
        var final = delivery(5, status: "completed", text: "Final complete report.", preview: "Short preview.")
        final.fullSize = 8192
        let pointer = delivery(6, status: "completed", wake: true)
        let items = [use(), receipt, interim, final, pointer]
        let tasks = TaskIndex.build(items)
        let call = call(items)!
        #expect(call.results.map(\.id) == [receipt.id, interim.id, final.id, pointer.id])
        #expect(tasks.reports["a"]?.map(\.id) == [interim.id, final.id])
        let finished = SubagentResult.call(call, tasks: tasks, agent: nil)!
        #expect(finished.results.map(\.id) == [interim.id, final.id])
        #expect(finished.result?.text == "Final complete report.")
        #expect(finished.result?.fullSize == 8192)
        #expect(SubagentCardStatus.of(call, tasks: tasks, agent: nil) == .done)
    }

    @Test func aReportQuotingLaunchMetadataIsNotItselfAReceipt() {
        let report = delivery(3, status: "completed", text: "Async agent launched successfully.\nThe receipt was verified.", preview: "Verified the receipt.")
        let items = [use(), report]
        let tasks = TaskIndex.build(items)
        #expect(!SubagentDelivery.isReceipt(report))
        #expect(!tasks.started.contains("a"))
        #expect(tasks.reports["a"]?.map(\.id) == [report.id])
        #expect(SubagentResult.call(call(items)!, tasks: tasks, agent: nil)?.result?.id == report.id)
    }

    @Test func aTerminalPointerKeepsEarlierReportsButStopsTheSpinner() {
        let interim = delivery(3, status: "running", text: "Initial checks pass.", preview: "Initial checks pass.")
        let pointer = delivery(4, status: "failed")
        let items = [use(), interim, pointer]
        let finished = SubagentResult.call(call(items)!, tasks: TaskIndex.build(items), agent: nil)!
        #expect(finished.results.map(\.id) == [interim.id])
        #expect(!finished.isRunning && finished.isError)
    }

    @Test func aLegacyNoticeCanFinishAMetadataBearingReceipt() {
        let receipt = delivery(3, status: "running", text: "Async agent launched successfully.\nagentId: worker-a")
        let old = Item(id: "old-note", seq: 4, role: "system", kind: .notice,
                       task: TaskNote(kind: "agent", status: "failed", title: "Synthetic review", taskID: "worker-a", detail: "A check failed."))
        let items = [use(), receipt, old]
        let tasks = TaskIndex.build(items)
        #expect(SubagentCardStatus.of(call(items)!, tasks: tasks, agent: nil) == .failed)
        #expect(SubagentResult.call(call(items)!, tasks: tasks, agent: nil)?.isError == true)
    }

    @Test func wakesRemainTurnEvidenceWithoutCreatingAParentMessageCard() {
        let question = Item(id: "question", seq: 1, role: "user", kind: .text, text: "Review the fixture.")
        let first = Item(id: "first-answer", seq: 4, role: "assistant", kind: .text, text: "Review started.")
        let wake = delivery(5, status: "completed", text: "Review finished.", preview: "Review finished.", wake: true)
        let reply = Item(id: "final-answer", seq: 6, role: "assistant", kind: .text, text: "The review passed.")
        let items = [question, use(), delivery(3, status: "running"), first, wake, reply]
        let built = ChatStore.rebuild(items, settled: nil)
        guard let block = built.blocks.first, case .turn(let turn) = block else { Issue.record("expected the parent exchange"); return }
        #expect(turn.wakes.map(\.id) == [wake.id])
        #expect(turn.answer?.id == reply.id)
        let lines = ChatLines(openWork: [], openAll: true).build(built.blocks, live: nil)
        #expect(!lines.contains { line in
            if case .row(let row) = line.content { return SubagentDelivery.isWake(row) }
            if case .workStep(_, .step(.thought(let item)), _) = line.content { return item.id == wake.id }
            return false
        })
        #expect(built.rows.filter(\.isUserInput).count == 1)
    }

    @Test func anEarlierPageAttachesAlreadyRetainedReports() async throws {
        let store = ChatStore(key: SessionKey(machineID: "synthetic", sessionID: UUID().uuidString))
        let final = delivery(10, status: "completed", text: "Final report.", preview: "Final report.")
        store.merge([final], backlog: true, hasMore: true)
        for _ in 0..<100 where store.isRebuilding { try await Task.sleep(for: .milliseconds(20)) }
        #expect(store.items.map(\.id) == [final.id])
        #expect(store.rows.isEmpty)
        store.merge([use()], backlog: true, hasMore: false)
        for _ in 0..<100 where store.isRebuilding { try await Task.sleep(for: .milliseconds(20)) }
        #expect(!store.isRebuilding)
        #expect(call(store.items)?.results.map(\.id) == [final.id])
        #expect(SubagentResult.call(call(store.items)!, tasks: store.tasks, agent: nil)?.result?.text == "Final report.")
    }

    @Test func aLaterParentTurnRebuildsTheOriginalCallsReports() {
        let old = Item(id: "old-question", seq: 1, role: "user", kind: .text, text: "Start the review.")
        let answer = Item(id: "old-answer", seq: 4, role: "assistant", kind: .text, text: "Review started.")
        let next = Item(id: "next-question", seq: 5, role: "user", kind: .text, text: "Another question.")
        let nextAnswer = Item(id: "next-answer", seq: 6, role: "assistant", kind: .text, text: "Another answer.")
        let items = [old, use(), delivery(3, status: "running"), answer, next, nextAnswer]
        let before = ChatStore.rebuild(items, settled: nil)
        let report = delivery(7, status: "completed", text: "Finished the earlier review.", preview: "Finished the earlier review.")
        let after = ChatStore.rebuild(items + [report], settled: before.settled)
        #expect(after.rows.filter(\.isUserInput).count == 2)
        guard case .tools(let steps) = after.rows[1], case .call(let call) = steps[0] else {
            Issue.record("expected the original call"); return
        }
        #expect(call.results.map(\.id) == ["delivery-3", report.id])
        #expect(call.agentStatus == "completed")
    }

    @Test func cacheTailsRetainReferencedLaunchExchangesAndUnrelatedPeersStayMessages() {
        let old = Item(id: "old-question", seq: 1, role: "user", kind: .text, text: "Start the review.")
        let middle = Item(id: "middle-question", seq: 4, role: "user", kind: .text, text: "Another question.")
        let latest = Item(id: "latest-question", seq: 5, role: "user", kind: .text, text: "The latest question.")
        let final = delivery(6, status: "completed", text: "Final report.", preview: "Final report.")
        let items = [old, use(), delivery(3, status: "running"), middle, latest, final]
        #expect(ChatCache.tail(items, turns: 1).map(\.id) == items.map(\.id))
        #expect(ChatCache.tail([middle, latest, final], turns: 1).map(\.id) == [latest.id, final.id])
        var peer = Item(id: "peer", seq: 7, role: "user", kind: .text, text: "An unrelated session sent this.")
        peer.from = "general-purpose"
        #expect(ChatStore.buildRows(items + [peer]).filter(\.isUserInput).last?.id == peer.id)
        #expect(TaskIndex.build(items + [peer]).reports["a"]?.count == 1)
    }
}
