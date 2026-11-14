import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The assistant's ask_user in the chat (issue 677): it reads as Question with its question, not the tool's raw name
/// beside an empty chip, and once answered it keeps what was asked and what the user picked.
@MainActor
struct AskUserToolTests {
    private func question(_ q: String, _ options: [String], multi: Bool? = nil) -> JSONValue {
        var o: [String: JSONValue] = ["question": .string(q), "header": .string("下一步"),
                                      "options": .array(options.map { .object(["label": .string($0)]) })]
        if let multi { o["multiSelect"] = .bool(multi) }
        return .object(o)
    }

    private func call(_ questions: [JSONValue], result: String?) -> ToolCall {
        let use = Item(id: "1#0", seq: 1, ts: Date(timeIntervalSince1970: 1), role: "assistant", kind: .toolUse, text: nil,
                       toolUseID: "t1", toolName: "mcp__persona__ask_user", toolInput: .object(["questions": .array(questions)]),
                       isError: nil, image: nil)
        let res = result.map {
            Item(id: "2#0", seq: 2, ts: Date(timeIntervalSince1970: 2), role: "user", kind: .toolResult, text: $0,
                 toolUseID: "t1", toolName: nil, toolInput: nil, isError: nil, image: nil)
        }
        return ToolCall(use: use, result: res)
    }

    @Test func namedLikeAQuestionWithItsFirstQuestion() {
        let c = call([question("先处理哪项？", ["看日程", "回邮件", "看未读消息", "都不用"])], result: nil)
        #expect(ToolPresentation.verb(c.name) == "问你") // Persona's own ask_user; AskUserQuestion stays 「提问」
        #expect(ToolPresentation.verb("AskUserQuestion") == "提问")
        #expect(ToolPresentation.subject(c) == "先处理哪项？") // was "": an empty chip beside "ask_user"
        #expect(ToolPresentation.symbol(c.name) == ToolPresentation.symbol("AskUserQuestion"))
    }

    @Test func readsThePickBackFromTheHubsAnswer() {
        let qs = AskUserAnswers.questions(.object(["questions": .array([question("先处理哪项？", ["看日程", "回邮件", "看未读消息"])])]))
        let answers = AskUserAnswers.parse("The user answered:\n- 先处理哪项？ → 看未读消息\n", questions: qs)
        #expect(answers == ["先处理哪项？": "看未读消息"])
        #expect(AskUserAnswers.picked("看未读消息", answer: answers["先处理哪项？"], multiSelect: false))
        #expect(!AskUserAnswers.picked("看日程", answer: answers["先处理哪项？"], multiSelect: false))
        #expect(AskUserAnswers.typed(qs[0], answer: answers["先处理哪项？"]) == nil)
    }

    @Test func severalQuestionsMultiSelectAndATypedAnswer() {
        let qs = AskUserAnswers.questions(.object(["questions": .array([
            question("发给谁？", ["组里", "老板", "客户"], multi: true),
            question("什么时候？", ["现在", "明早"]),
        ])]))
        let text = "The user answered:\n- 发给谁？ → 组里, 客户\n- 什么时候？ → 下午三点以后\n再说一句\n"
        let answers = AskUserAnswers.parse(text, questions: qs)
        #expect(answers["发给谁？"] == "组里, 客户")
        #expect(answers["什么时候？"] == "下午三点以后\n再说一句") // a typed answer may run over lines
        #expect(AskUserAnswers.picked("组里", answer: answers["发给谁？"], multiSelect: true))
        #expect(AskUserAnswers.picked("客户", answer: answers["发给谁？"], multiSelect: true))
        #expect(!AskUserAnswers.picked("老板", answer: answers["发给谁？"], multiSelect: true))
        #expect(AskUserAnswers.typed(qs[0], answer: answers["发给谁？"]) == nil)
        #expect(AskUserAnswers.typed(qs[1], answer: answers["什么时候？"]) == "下午三点以后\n再说一句")
    }

    /// #5937: Claude Code answers its AskUserQuestion as `"<question>"="<answer>"` pairs; the row ticks those too.
    @Test func readsThePickBackFromClaudesAnswer() {
        let qs = AskUserAnswers.questions(.object(["questions": .array([
            question("发给谁？", ["组里", "老板", "客户"], multi: true),
            question("什么时候？", ["现在", "明早"]),
        ])]))
        let text = #"User has answered your questions: "发给谁？"="组里, 客户", "什么时候？"="明早". "#
            + "You can now continue with the user's answers in mind."
        let answers = AskUserAnswers.parse(text, questions: qs)
        #expect(answers == ["发给谁？": "组里, 客户", "什么时候？": "明早"])
        #expect(AskUserAnswers.picked("客户", answer: answers["发给谁？"], multiSelect: true))
        #expect(!AskUserAnswers.picked("老板", answer: answers["发给谁？"], multiSelect: true))
        #expect(AskUserAnswers.picked("明早", answer: answers["什么时候？"], multiSelect: false))
        // A typed answer keeps the quotes inside it.
        let typed = AskUserAnswers.parse(#"User has answered your questions: "什么时候？"="下午 "三点" 以后"."#,
                                         questions: [qs[1]])
        #expect(AskUserAnswers.typed(qs[1], answer: typed["什么时候？"]) == #"下午 "三点" 以后"#)
    }

    /// #5889: a Claude Code dialog only its computer can answer: the chat shows what it asks while it has no result,
    /// and nothing once answered or once another tool ran after it; the Needs You row says it waits on the computer.
    @Test func pendingClaudeQuestionUntilAnswered() throws {
        func item(_ seq: Int, _ kind: ItemKind, tool: String? = nil, id: String) -> Item {
            Item(id: "\(seq)#0", seq: seq, ts: nil, role: kind == .toolUse ? "assistant" : "user", kind: kind, text: nil,
                 toolUseID: id, toolName: tool,
                 toolInput: tool == nil ? nil : .object(["questions": .array([question("要发布吗？", ["发布", "先不"])])]),
                 isError: nil, image: nil)
        }
        let ask = item(1, .toolUse, tool: "AskUserQuestion", id: "q")
        #expect(AskUserAnswers.pending([ask])?.map(\.options) == [["发布", "先不"]])
        #expect(AskUserAnswers.pending([ask, item(2, .toolResult, id: "q")]) == nil)
        #expect(AskUserAnswers.pending([ask, item(2, .toolUse, tool: "Bash", id: "b")]) == nil)

        func session(_ caps: String) throws -> Session {
            let json = #"{"machine_id":"m","session_id":"s","title":"t","cwd":"/w","status":"blocked","state":"needs_you","source_kind":"claude_cli","attention_reason":"Only the computer can answer it","last_text":"Ship it?","capabilities":"# + caps + "}"
            return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        }
        let outside = try session(#"{"read":true,"send":true,"terminal":false}"#)
        #expect(NeedsYouWaitingCard.title(outside) == String(localized: "Waiting on Your Computer"))
        #expect(NeedsYouWaitingCard.line(outside) == "Only the computer can answer it")
        let pane = try session(#"{"read":true,"send":true,"terminal":true}"#)
        #expect(NeedsYouWaitingCard.title(pane) == InteractionKind.screen.title)
    }

    @Test func dismissedOrExpiredHasNoAnswers() {
        let qs = AskUserAnswers.questions(.object(["questions": .array([question("删掉吗？", ["删", "不删"])])]))
        let text = "The user did not answer (dismissed or timed out). Don't go ahead with what you asked about; say what is waiting for them."
        #expect(AskUserAnswers.parse(text, questions: qs).isEmpty)
        #expect(AskUserAnswers.parse("questions are required", questions: qs).isEmpty)
    }
}
