import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Background tasks reporting back (`Item.task`): which call each came from, and the words of their one-line rows.
@MainActor
struct TaskNoteTests {
    private func use(_ seq: Int, _ id: String, _ name: String, description: String? = nil) -> Item {
        var input: [String: JSONValue] = ["command": .string("x")]
        if let description { input["description"] = .string(description) }
        return Item(id: "\(seq)#0", seq: seq, ts: Date(timeIntervalSince1970: TimeInterval(seq * 60)), role: "assistant", kind: .toolUse,
                    text: nil, toolUseID: id, toolName: name, toolInput: .object(input), isError: nil, image: nil)
    }
    private func result(_ seq: Int, _ id: String, _ text: String) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: "user", kind: .toolResult, text: text, toolUseID: id, toolName: nil, toolInput: nil,
             isError: nil, image: nil)
    }
    private func notice(_ seq: Int, _ n: TaskNote) -> Item {
        var it = Item(id: "\(seq)#0", seq: seq, ts: Date(timeIntervalSince1970: TimeInterval(seq * 60)), role: "system", kind: .notice,
                      text: "x", toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        it.task = n
        return it
    }
    private func note(_ kind: String, _ status: String?, title: String = "t", task: String? = nil, tool: String? = nil, exit: Int? = nil,
                      detail: String? = nil) -> TaskNote {
        TaskNote(kind: kind, status: status, title: title, taskID: task, toolUseID: tool, exitCode: exit, outputFile: nil, detail: detail)
    }

    @Test func noticesFindTheirCalls() {
        let items = [
            use(1, "b", "Bash", description: "跑单元测试"), result(2, "b", "Command running in background with ID: bk2. Output is being written to: /tmp/x"),
            use(3, "m", "Monitor", description: "等 CI"), result(4, "m", "Monitor started (task mo3, timeout 600000ms). You will be notified."),
            use(5, "q", "Bash"), result(6, "q", "ok"), // in the foreground: not a background call
            notice(7, note("monitor", "event", task: "mo3", detail: "3/5")), // a Monitor event names only its task
            notice(8, note("shell", "failed", tool: "b", exit: 1)),
            notice(9, note("monitor", "ended", task: "mo3")),
        ]
        let x = TaskIndex.build(items)
        #expect(x.started == ["b", "m"])
        #expect(x.calls == ["7#0": "m", "8#0": "b", "9#0": "m"])
        #expect(x.notes["m"]?.map(\.status) == ["event", "ended"])
        #expect(x.descriptions["b"] == "跑单元测试")
        #expect(TaskNoteText.badge(x.notes["m"] ?? []).text == "后台 · 已结束 · 1 个事件")
        #expect(TaskNoteText.badge(x.notes["b"] ?? []) == ("后台 · 失败", true))
        #expect(TaskNoteText.badge([]).text == "后台 · 运行中")
        // The row: the call's description beats the notice's title; how long it ran when the call is known.
        let failed = note("shell", "failed", title: "xcodebuild test -scheme Weather", tool: "b", exit: 1)
        #expect(TaskNoteText.subject(failed, description: x.descriptions["b"]) == "跑单元测试")
        #expect(TaskNoteText.subject(failed, description: nil) == "xcodebuild test -scheme Weather")
        #expect(TaskNoteText.outcome(failed, startedAt: x.startedAt["b"], at: Date(timeIntervalSince1970: 8 * 60)) == "✗ 退出码 1 · 7 分")
        // A Monitor event reads as what happened.
        #expect(TaskNoteText.subject(note("monitor", "event", title: "等 CI", detail: "\nbuild: 3/5 passed\nmore"), description: "等 CI") == "build: 3/5 passed")
        #expect(TaskNoteText.label(note("monitor", "ended")) == "Monitor 结束")
    }

    /// Claude Code 2.1.319's receipt opens with "including the agentId below": the id is the one after "agentId:", so
    /// the subagent's notice finds its call (#6381).
    @Test func aSubagentsIdIsTheOneAfterAgentIdColon() {
        let receipt = "Async agent launched successfully. (This tool result is internal metadata — never quote or paste any part of it, "
            + "including the agentId below, into a user-facing reply.)\nagentId: a0123456789abcdef (internal ID - do not mention to user. "
            + "Use SendMessage with to: 'a0123456789abcdef' to continue this agent.)\nThe agent is working in the background."
        let items = [use(1, "a", "Agent", description: "回归"), result(2, "a", receipt),
                     notice(3, note("agent", "completed", task: "a0123456789abcdef", detail: "42 passed."))]
        let x = TaskIndex.build(items)
        #expect(x.started == ["a"])
        #expect(x.calls == ["3#0": "a"])
        #expect(x.notes["a"]?.first?.detail == "42 passed.")
        // "agentId" with no colon names nothing.
        let none = TaskIndex.build([use(1, "b", "Agent"), result(2, "b", "Async agent launched successfully, see the agentId below."),
                                    notice(3, note("agent", "completed", task: "below"))])
        #expect(none.calls.isEmpty)
    }

    /// A finished task's notice never closes its turn as "interrupted", whatever its command says.
    @Test func aNoticeAboutAnInterruptIsNotTheInterrupt() {
        var n = notice(3, note("shell", "completed", title: "检查中断恢复"))
        n.text = "后台命令完成 · 检查中断恢复"
        let user = Item(id: "1#0", seq: 1, ts: nil, role: "user", kind: .text, text: "go", toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        let answer = Item(id: "4#0", seq: 4, ts: nil, role: "assistant", kind: .text, text: "done", toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        guard case .turn(let t) = ChatBlock.build(ChatStore.buildRows([user, n, answer])).first else { Issue.record("no turn"); return }
        #expect(t.trailing.isEmpty)
    }
}
