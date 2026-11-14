import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

/// A workflow run's card (#3436, the web's `WorkflowCallCard` of #3434): its phase blocks (done / started, "+" while more
/// may start, open while running, folded once back), at most 12 rows a block, its progress in parts, what an older hub
/// (no `phase_stats`, no `agents_failed`, no agent status) still gives, and where the card sits in the chat.
@MainActor
struct WorkflowCardTests {
    private func run(_ json: String) throws -> WorkflowRun {
        try JSON.decoder.decode(WorkflowRun.self, from: Data(json.utf8))
    }

    private func agent(_ id: String, phase: String?, running: Bool = false, status: String? = nil) -> WorkflowAgent {
        WorkflowAgent(id: id, sessionID: "s@\(id)", label: id, phase: phase, running: running, status: status)
    }

    /// The protocol's example (docs/protocol.md `workflows`): a newer hub's run.
    private let newer = #"""
    {"task_id":"wdp5zxh37","run_id":"wf_c1d9daf8-cd2","name":"parity-audit","status":"running","running":true,
     "phases":["Audit","Verify"],"phase":"Verify","phase_index":2,"phase_count":2,
     "phase_stats":[{"title":"Audit","started":180,"done":167,"failed":2,"running":11},{"title":"Verify","started":5,"done":0,"running":5}],
     "tool_use_id":"toolu_1","agents_started":185,"agents_done":167,"agents_failed":2,"agents_running":16,"tokens":123400,"calls":420,
     "started_at":"2026-11-05T13:00:00Z","updated_at":"2026-11-05T13:14:00Z",
     "agents":[
       {"id":"a1","session_id":"s@a1","label":"audit:chat-rich","phase":"Audit","running":false,"status":"done",
        "started_at":"2026-11-05T13:00:00Z","ended_at":"2026-11-05T13:02:05Z","model":"anthropic/claude-example",
        "latest_tool":{"id":"toolu_x","name":"Read","summary":"Reading example.go"},"tokens":1234,"calls":8},
       {"id":"a2","session_id":"s@a2","label":"audit:inbox","phase":"Audit","running":false,"status":"failed","started_at":"2026-11-05T13:01:00Z"},
       {"id":"v1","session_id":"s@v1","label":"verify:island","phase":"Verify","running":true,"status":"running",
        "started_at":"2026-11-05T13:10:00Z","latest_tool":{"name":"Grep"}}
     ]}
    """#

    @Test func readsANewerHubsRun() throws {
        let r = try run(newer)
        #expect(r.toolUseID == "toolu_1" && r.agentsFailed == 2 && r.phaseCount == 2 && r.calls == 420)
        #expect(r.phaseStats?.map(\.title) == ["Audit", "Verify"])
        #expect(r.phaseStats?.first?.failed == 2 && r.phaseStats?.last?.failed == 0, "failed is left out when 0")
        let a1 = try #require(r.agents?.first)
        #expect(a1.runState == .done && a1.model == "anthropic/claude-example")
        #expect(a1.took == 125)
        #expect(a1.detail == "claude-example · Reading example.go")
        #expect(r.agents?[1].runState == .failed)
        #expect(r.agents?[2].detail == "Grep", "a tool without a summary says its name")
        // Counts: "+" while it runs, none once it ended.
        #expect(r.counts == "167/185+")
        #expect(r.line == "后台 workflow「parity-audit」 · Verify 2/2 · 167/185+")
        var ended = r
        ended.running = false
        ended.status = "completed"
        #expect(ended.counts == "167/185")
    }

    @Test func phaseBlocksFromTheHubsStats() throws {
        let blocks = try run(newer).phaseBlocks
        #expect(blocks.map(\.title) == ["Audit", "Verify"])
        let audit = blocks[0], verify = blocks[1]
        // Audit still has agents running: open, not lit; Verify is where the run is.
        #expect(audit.state == .current && !audit.current && audit.opensByItself)
        #expect(audit.count == "167/180+" && audit.failed == 2)
        #expect(verify.current && verify.state == .current && verify.count == "0/5+")
        // Each block's listed agents, in the order they started.
        #expect(audit.agents.map(\.id) == ["a1", "a2"] && verify.agents.map(\.id) == ["v1"])
    }

    @Test func blocksOfAnEndedRun() throws {
        // Completed: every block done, folded, no "+".
        var r = try run(newer)
        r.running = false
        r.status = "completed"
        r.phaseStats = [WorkflowPhaseStats(title: "Audit", started: 3, done: 2, running: 0, failed: 1),
                        WorkflowPhaseStats(title: "Verify", started: 2, done: 2, running: 0)]
        let done = r.phaseBlocks
        #expect(done.map(\.state) == [.done, .done])
        #expect(done.allSatisfy { !$0.opensByItself && !$0.growing && !$0.current })
        #expect(done.map(\.count) == ["2/3", "2/2"])
        #expect(done[0].failed == 1, "a phase with failures is marked")
        // Stopped part way: the phase with agents not back, and the one never started.
        r.status = "killed"
        r.phaseStats = [WorkflowPhaseStats(title: "Scan", started: 3, done: 1, running: 0),
                        WorkflowPhaseStats(title: "Fix", started: 0, done: 0, running: 0)]
        r.phase = "Scan"
        #expect(r.phaseBlocks.map(\.state) == [.stopped, .next])
    }

    @Test func thePhaseBeforeTheCurrentOneIsDoneEvenWithNoneStarted() {
        let r = WorkflowRun(taskID: "w", phases: ["Plan", "Build", "Verify"], phase: "Build", phaseIndex: 2,
                            phaseStats: [WorkflowPhaseStats(title: "Plan", started: 0, done: 0, running: 0),
                                         WorkflowPhaseStats(title: "Build", started: 2, done: 1, running: 1),
                                         WorkflowPhaseStats(title: "Verify", started: 0, done: 0, running: 0)])
        #expect(r.phaseBlocks.map(\.state) == [.done, .current, .next])
        #expect(r.phaseBlocks.map(\.current) == [false, true, false])
    }

    /// A hub from #3165 (no phase_stats, no agent status, failures counted as done): the blocks come from the listed
    /// agents, declared phases first, then undeclared ones, then those with none.
    @Test func anOlderHubsBlocksComeFromItsAgents() throws {
        let r = try run(#"""
        {"task_id":"w","status":"running","running":true,"phases":["Audit","Verify"],"phase":"Verify","phase_index":2,
         "agents_started":5,"agents_done":2,"agents_running":3,
         "agents":[{"id":"a","phase":"Audit","running":false},{"id":"b","phase":"Audit","running":false},
                   {"id":"c","phase":"Verify","running":true},{"id":"d","phase":"Extra","running":true},{"id":"e","running":true}]}
        """#)
        #expect(r.phaseStats == nil && r.toolUseID == nil && r.agentsFailed == nil)
        let blocks = r.phaseBlocks
        #expect(blocks.map(\.title) == ["Audit", "Verify", "Extra", ""])
        #expect(blocks.map(\.count) == ["2/2", "0/1+", "0/1+", "0/1+"])
        #expect(blocks.map(\.state) == [.done, .current, .current, .current])
        #expect(blocks[1].current && !blocks[2].current)
        #expect(blocks[3].agents.map(\.id) == ["e"], "the agents without a phase")
        // No phases at all and no agents: no blocks.
        #expect(WorkflowRun(taskID: "x").phaseBlocks.isEmpty)
        #expect(try run(#"{"task_id":"x","phase_stats":[{"title":"","started":0,"done":0,"running":0}]}"#).phaseBlocks.isEmpty)
        // Its tally still adds up, with nothing failed or stopped while it runs.
        #expect(r.tally == WorkflowTally(started: 5, done: 2, running: 3, failed: 0, stopped: 0))
    }

    /// Twelve quiet rows at most; a running agent is a row besides and never folds (#3434's design, the web's
    /// `phaseRows`): 12 quiet + 2 running is 14 rows.
    @Test func twelveQuietRowsAtMostRunningOnesNeverFold() {
        // 20 agents, 2 of them running late: the first 12 rows, the running ones always; the 6 quiet ones after fold.
        let agents = (1...20).map { agent("a\($0)", phase: "P", running: $0 == 15 || $0 == 18) }
        let b = WorkflowPhaseBlock(index: 0, title: "P", started: 20, done: 18, running: 2, failed: 0, agents: agents,
                                   state: .current, current: true, growing: true)
        let r = b.rows()
        #expect(r.rows.map(\.id) == (1...12).map { "a\($0)" } + ["a15", "a18"])
        #expect(r.folded == 6 && r.breakAt == 12 && r.unlisted == 0)
        let all = b.rows(all: true)
        #expect(all.rows.count == 20 && all.folded == 0)
        // Few enough: nothing folds.
        let small = WorkflowPhaseBlock(index: 0, title: "P", started: 3, done: 3, running: 0, failed: 0,
                                       agents: Array(agents.prefix(3)), state: .done, current: false, growing: false)
        #expect(small.rows() == WorkflowPhaseRows(rows: Array(agents.prefix(3)), folded: 0, breakAt: 3, unlisted: 0))
    }

    /// A fan-out with every listed agent still running: all of them are rows and nothing folds (none is quiet); once
    /// they are back, 12 rows and the rest folded.
    @Test func aBlockWhoseAgentsAllRun() {
        let running = (1...60).map { agent("a\($0)", phase: "P", running: true) }
        let b = WorkflowPhaseBlock(index: 0, title: "P", started: 60, done: 0, running: 60, failed: 0, agents: running,
                                   state: .current, current: true, growing: true)
        #expect(b.rows() == WorkflowPhaseRows(rows: running, folded: 0, breakAt: 60, unlisted: 0))
        let back = (1...60).map { agent("a\($0)", phase: "P") }
        let d = WorkflowPhaseBlock(index: 0, title: "P", started: 60, done: 60, running: 0, failed: 0, agents: back,
                                   state: .done, current: false, growing: false)
        #expect(d.rows() == WorkflowPhaseRows(rows: Array(back.prefix(12)), folded: 48, breakAt: 12, unlisted: 0))
    }

    /// The hub lists a run's newest 60: a block it cut into shows its last 12 (they stay put as the cap moves on) and
    /// says how many earlier ones are not listed.
    @Test func aBlockTheHubsCapCutIntoShowsItsLastRows() {
        let agents = (126...180).map { agent("a\($0)", phase: "Audit", running: $0 > 169) }
        let b = WorkflowPhaseBlock(index: 0, title: "Audit", started: 180, done: 169, running: 11, failed: 0, agents: agents,
                                   state: .current, current: false, growing: true)
        let r = b.rows()
        #expect(r.unlisted == 125)
        #expect(r.rows.first?.id == "a169" && r.rows.last?.id == "a180" && r.rows.count == 12)
        #expect(r.folded == 43 && r.breakAt == 0, "the hidden ones come before the rows")
    }

    @Test func theTallyOfARun() throws {
        #expect(try run(newer).tally == WorkflowTally(started: 185, done: 167, running: 16, failed: 2, stopped: 0))
        // Ended: those not back are stopped.
        let killed = try run(#"{"task_id":"k","status":"killed","running":false,"agents_started":6,"agents_done":3,"agents_failed":1,"agents_running":2}"#)
        #expect(killed.tally == WorkflowTally(started: 6, done: 3, running: 0, failed: 1, stopped: 2))
        let shares = killed.tally.shares
        #expect(shares.map(\.part) == [.done, .running, .failed, .stopped])
        let expectedShares: [Double] = [0.5, 0, 1.0 / 6, 2.0 / 6] // typed: untyped, this literal took 5 s to type-check
        #expect(shares.map(\.share) == expectedShares)
        #expect(WorkflowRun(taskID: "none").tally.started == 0)
    }

    @Test func whereARunIsNow() throws {
        #expect(try run(newer).now.map { "\($0.phase)|\($0.agent)" } == "Verify|verify:island")
        #expect(WorkflowSubline.nowText(("Verify", "verify:island")) == "当前 Verify：verify:island")
        #expect(WorkflowSubline.nowText(("Verify", "")) == "当前 Verify")
        #expect(WorkflowSubline.nowText(("", "a")) == "正在跑 a")
        var ended = try run(newer)
        ended.running = false
        #expect(ended.now == nil)
    }

    // MARK: Where the card sits

    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", ts: Date? = nil, text: String? = nil,
                      tool: String? = nil, id: String? = nil, fold: Fold? = nil) -> Item {
        var i = Item(id: "\(seq)#0", seq: seq, ts: ts, role: role, kind: kind, text: text, toolUseID: id, toolName: tool,
                     toolInput: nil, isError: nil, image: nil)
        i.fold = fold
        return i
    }

    /// Listed Workflow calls keep their own card inside the manually opened work summary.
    @Test func aListedRunsCallStaysOutOfChipsButInsideTheFold() {
        let items = [
            item(1, .text, role: "user", text: "审一遍"),
            item(2, .toolUse, tool: "Grep", id: "g"), item(3, .toolResult, role: "user", text: "ok", id: "g"),
            item(4, .toolUse, tool: "Workflow", id: "wf"), item(5, .toolResult, role: "user", text: "launched", id: "wf"),
            item(6, .toolUse, tool: "Read", id: "r"), item(7, .toolResult, role: "user", text: "ok", id: "r"),
            item(8, .text, text: "放到后台了"),
        ]
        let blocks = ChatStore.rebuild(items, settled: nil).blocks
        func lines(listed: Set<String>, open: Bool) -> ChatLines {
            ChatLines(openWork: open ? ["w-1#0"] : [], chipRuns: true,
                      foldsIntoRun: { !WorkflowCallCard.staysOut($0, listed: listed) })
        }
        // Folded: even listed workflow calls stay inside the summary.
        let folded = lines(listed: ["wf"], open: false).build(blocks, live: nil)
        #expect(folded.count == 3)
        #expect(!folded.contains { if case .agentOut = $0.content { true } else { false } })
        // Open: a step of its own between the other calls, which stay single (no run of one chip).
        let open = lines(listed: ["wf"], open: true).build(blocks, live: nil)
        let steps = open.compactMap { line -> String? in
            switch line.content {
            case .workStep(_, .step(.call(let c)), _): c.use.toolUseID
            case .toolRun: "run"
            default: nil
            }
        }
        #expect(steps == ["g", "wf", "r"])
        // Not listed: it folds into the chips with the others, and nothing stays out.
        let plain = lines(listed: [], open: true).build(blocks, live: nil)
        #expect(plain.contains { if case .toolRun(_, let calls, _, _) = $0.content { calls.count == 3 } else { false } })
        #expect(!lines(listed: [], open: false).build(blocks, live: nil).contains { if case .agentOut = $0.content { true } else { false } })
        #expect(!WorkflowCallCard.staysOut(ToolCall(use: item(9, .toolUse, tool: "Agent", id: "wf")), listed: ["wf"]))
    }

    /// A chat opened while a run goes on has the turn that launched it folded by the hub: the fold whose span holds the
    /// run's start is the one to fetch; a call already loaded needs none.
    @Test func theFoldHoldingARunsCall() {
        let t0 = Date(timeIntervalSince1970: 1_793_883_600)
        let fold = { (seq: Int, from: Double, until: Double) in
            self.item(seq, .fold, ts: t0.addingTimeInterval(from),
                      fold: Fold(items: 6, tools: 3, thoughts: 0, until: t0.addingTimeInterval(until), throughSeq: seq + 5, throughID: "x"))
        }
        let early = fold(1, 0, 50), holding = fold(10, 60, 120), after = fold(20, 130, 200)
        let run = WorkflowRun(taskID: "w", toolUseID: "wf", startedAt: t0.addingTimeInterval(120))
        #expect(WorkflowRun.foldsHoldingCalls([early, holding, after], runs: [run]).map(\.seq) == [10])
        // Its call already on the page, a run without its call or its start: nothing to fetch.
        let call = item(30, .toolUse, tool: "Workflow", id: "wf")
        #expect(WorkflowRun.foldsHoldingCalls([early, holding, call], runs: [run]).isEmpty)
        #expect(WorkflowRun.foldsHoldingCalls([holding], runs: [WorkflowRun(taskID: "w", startedAt: t0.addingTimeInterval(90))]).isEmpty)
        #expect(WorkflowRun.foldsHoldingCalls([holding], runs: [WorkflowRun(taskID: "w", toolUseID: "wf")]).isEmpty)
    }

    /// A fold that did not come (the hub restarting, offline, not connected) is asked for again after a wait that
    /// doubles up to a minute, the others not again; it stops once all have come, or when its task is cancelled.
    @Test func aFoldThatFailedIsAskedAgainLater() async {
        let t0 = Date(timeIntervalSince1970: 1_793_883_600)
        let a = item(1, .fold, ts: t0, fold: Fold(items: 6, tools: 3, thoughts: 0, until: t0, throughSeq: 6, throughID: "x"))
        let b = item(10, .fold, ts: t0, fold: Fold(items: 6, tools: 3, thoughts: 0, until: t0, throughSeq: 15, throughID: "y"))
        var asked: [Int] = []
        var waits: [Double] = []
        var failsLeft = 5
        await WorkflowCallCard.fetchFolds([a, b], fetch: { f in
            asked.append(f.seq)
            if f.seq == 10, failsLeft > 0 { failsLeft -= 1; return false }
            return true
        }, sleep: { waits.append($0) })
        #expect(asked == [1, 10, 10, 10, 10, 10, 10], "only the one that failed is asked again")
        #expect(waits == [5, 10, 20, 40, 60], "the wait doubles, up to a minute")
        // All came at once: no wait at all.
        waits = []
        await WorkflowCallCard.fetchFolds([a], fetch: { _ in true }, sleep: { waits.append($0) })
        #expect(waits.isEmpty)
        // The chat closed (the task cancelled) while it waits: it stops.
        let tries = Tally()
        let task = Task { @MainActor in
            await WorkflowCallCard.fetchFolds([b], fetch: { _ in tries.n += 1; return false },
                                              sleep: { _ in withUnsafeCurrentTask { $0?.cancel() } })
        }
        await task.value
        #expect(tries.n == 1)
    }

    /// The card reads its run by its call (`AppModel.workflow(_:call:)`): drawn again when that run changes, not for
    /// another run of the session nor the rest of it.
    @Test func theCardFollowsOnlyItsOwnRun() throws {
        let model = AppModel()
        let machine = MachineRef(group: "", id: "m")
        func session(_ runs: [WorkflowRun], title: String = "t") throws -> Session {
            var s = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"m","session_id":"s","title":"\#(title)","cwd":"/c","status":"done","state":"working"}"#.utf8))
            s.workflows = runs
            return s
        }
        let a = WorkflowRun(taskID: "a", toolUseID: "call-a", agentsStarted: 185, agentsDone: 169)
        let b = WorkflowRun(taskID: "b", toolUseID: "call-b", agentsStarted: 3, agentsDone: 1)
        model.applySessions([machine: [try session([a, b])]])
        let key = SessionKey(machineID: "m", sessionID: "s")
        #expect(model.workflow(key, call: "call-a")?.counts == "169/185+")
        #expect(model.workflow(key, call: "nope") == nil)
        nonisolated(unsafe) var changed = 0
        withObservationTracking { _ = model.workflow(key, call: "call-a") } onChange: { changed += 1 }
        var b2 = b
        b2.agentsDone = 2
        model.applySessions([machine: [try session([a, b2], title: "renamed")]])
        #expect(changed == 0, "another run moved, the session's title changed")
        var a2 = a
        a2.agentsDone = 170
        model.applySessions([machine: [try session([a2, b2], title: "renamed")]])
        #expect(changed == 1)
        #expect(model.workflow(key, call: "call-a")?.counts == "170/185+")
    }

    @Test func aWorkflowCallsRowWhenTheHubListsNoRun() {
        let c = ToolCall(use: Item(id: "1", seq: 1, ts: nil, role: "assistant", kind: .toolUse, text: nil, toolUseID: "wf", toolName: "Workflow",
                                   toolInput: .object(["script": .string("export const meta = { name: 'parity-audit', phases: [] }")]),
                                   isError: nil, image: nil))
        #expect(WorkflowCallCard.isWorkflow(c))
        #expect(ToolPresentation.verb("Workflow") == "运行 workflow")
        #expect(ToolPresentation.subject(c) == "parity-audit")
        #expect(ToolPresentation.symbol("Workflow") == Theme.Icon.workflow)
        // No name and none in the script: the script file's, as the node has it (without ".js" and its run id).
        let byPath = ToolCall(use: Item(id: "2", seq: 2, ts: nil, role: "assistant", kind: .toolUse, text: nil, toolUseID: "wf2",
                                        toolName: "Workflow",
                                        toolInput: .object(["scriptPath": .string("/Users/me/.claude/workflows/parity-audit-wf_c1d9daf8.js")]),
                                        isError: nil, image: nil))
        #expect(ToolPresentation.subject(byPath) == "parity-audit")
        #expect(ToolPresentation.workflowScriptName("C:\\w\\sweep.js") == "sweep")
        #expect(ToolPresentation.workflowScriptName("-wf_only.js") == "-wf_only", "a name that is all run id is kept")
        #expect(ToolPresentation.workflowScriptName("plain") == "plain")
    }

    /// Two blocks with one title keep their open / folded choices apart.
    @Test func blocksWithOneTitleFoldApart() {
        let one = WorkflowPhaseBlock(index: 0, title: "Audit", started: 1, done: 1, running: 0, failed: 0, agents: [], state: .done,
                                     current: false, growing: false)
        var two = one
        two.index = 1
        #expect(WorkflowPhaseList.key("c", one) != WorkflowPhaseList.key("c", two))
    }

    /// The Agents list's line: the name is the part a narrow row cuts; the phase, the counts and the end stay whole.
    @Test func theLinesNameApartFromItsCounts() throws {
        let r = try run(newer)
        #expect(r.lineName == "后台 workflow「parity-audit」")
        #expect(r.lineDetails() == ["Verify 2/2", "167/185+"])
        #expect(r.lineDetails(phase: false) == ["167/185+"])
        #expect(r.line == "后台 workflow「parity-audit」 · Verify 2/2 · 167/185+")
    }
}

/// A count a task's closures add to (`aFoldThatFailedIsAskedAgainLater`).
@MainActor private final class Tally { var n = 0 }
