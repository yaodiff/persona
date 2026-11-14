import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Rewind to Here (#2915): which message the node is told about, when the chat offers it, the request it sends.
struct RewindTests {
    private let a = "11111111-1111-1111-1111-111111111111"
    private let b = "22222222-2222-2222-2222-222222222222"
    private let c = "33333333-3333-3333-3333-333333333333"
    private let d = "44444444-4444-4444-4444-444444444444"

    private func item(_ id: String, _ text: String, role: String = "user", kind: ItemKind = .text, queued: Bool? = nil, cut: Bool = false) -> Item {
        Item(id: id, seq: 0, role: role, kind: kind, text: text, fullSize: cut ? 9999 : nil, queued: queued)
    }

    @Test func countsTheSameTextFromTheEnd() {
        let items = [
            item("\(a)#0", "继续"),
            item("\(a)#1", "好的", role: "assistant"),
            item("\(b)#0", "改一下"),
            item("\(c)#0", "继  续"), // whitespace does not tell messages apart
            item("\(d)#0", "最后"),
        ]
        #expect(Rewind.target(items, id: "\(a)#0") == .init(prompt: "继续", occurrence: 2, promptsAfter: 3))
        #expect(Rewind.target(items, id: "\(c)#0") == .init(prompt: "继  续", occurrence: 1, promptsAfter: 1))
        #expect(Rewind.target(items, id: "\(d)#0") == .init(prompt: "最后", occurrence: 1, promptsAfter: 0))
    }

    @Test func linesOfOneEntryAreOneMessage() {
        let items = [
            item("\(a)#0", "第一行"),
            item("\(a)#1", "第二行"),
            item("\(b)#0", "", kind: .image), // an image alone is a message too, without words
        ]
        #expect(Rewind.target(items, id: "\(a)#1") == .init(prompt: "第一行\n第二行", occurrence: 1, promptsAfter: 1))
        #expect(Rewind.target(items, id: "\(b)#0") == nil)
    }

    @Test func queuedAndUnsentMessagesAreNotInClaudesList() {
        let items = [
            item("\(a)#0", "早的"),
            item("\(b)#0", "排队的", queued: true),
            item("pending-1", "还没发出去"),
        ]
        #expect(Rewind.target(items, id: "\(a)#0")?.promptsAfter == 0)
        #expect(Rewind.target(items, id: "\(b)#0") == nil)
        #expect(Rewind.target(items, id: "pending-1") == nil)
    }

    @Test func aCutMessageIsFetchedWhole() {
        let items = [item("\(a)#0", "很长…", cut: true), item("\(b)#0", "后面")]
        #expect(Rewind.cutItem(items, id: "\(a)#0") == "\(a)#0")
        #expect(Rewind.cutItem(items, id: "\(b)#0") == nil)
        #expect(Rewind.target(items, id: "\(a)#0", full: "很长的全文")?.prompt == "很长的全文")
    }

    private func session(_ extra: String = "", state: String = "idle") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"s1","status":"idle","state":"\#(state)"\#(extra)}"#.utf8))
    }

    @Test func offeredOnlyInAClaudeAgentsChatAndOnlyWhenIdle() throws {
        #expect(Rewind.block(try session()) == "")
        #expect(Rewind.block(try session(state: "unread")) == "")
        #expect(Rewind.block(try session(state: "working")) == "工作中")
        #expect(Rewind.block(try session(state: "needs_you")) == "需要你")
        #expect(Rewind.block(try session(#","pending_interactions":1"#)) == "需要你")
        #expect(Rewind.block(try session(state: "offline")) == "未运行")
        #expect(Rewind.block(try session(#","starting":true"#)) == "未运行")
        #expect(Rewind.block(try session(#","agent":"codex""#)) == "仅 Claude")
        #expect(Rewind.block(try session(#","assistant":true"#)) == nil)
        #expect(Rewind.block(nil) == nil)
    }

    @Test func theToastSaysWhatWentBack() {
        #expect(Rewind.done(.init(codeRestored: true, inputCleared: true), asked: .codeAndConversation) == "已回退代码和对话")
        #expect(Rewind.done(.init(codeRestored: false), asked: .codeAndConversation) == "已回退对话（这条之后没有代码改动）")
        #expect(Rewind.done(.init(codeRestored: false), asked: .conversation) == "已回退对话")
    }

    @Test func refusalsInWords() {
        #expect(Rewind.failure(APIError(status: 404, message: "not found")) == "这个 hub 还不支持回退，需要更新")
        #expect(Rewind.failure(APIError(status: 403, message: "x")) == "回退要用完整权限的设备")
        #expect(Rewind.failure(APIError(status: 409, message: "Claude 的列表和这条消息对不上", fields: ["code": "rewind_mismatch"]))
            == "Claude 的列表和这条消息对不上")
    }

    @Test func sendsTheFieldsTheHubReads() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/rewind", json: #"{"prompt":"继续","restore":"conversation","code_restored":false,"input_cleared":true}"#)
        let data = try await hub.client.rewind(SessionKey(machineID: "box", sessionID: "s1"), prompt: "继续", occurrence: 2, promptsAfter: 3, restore: "conversation")
        let result = try JSON.decoder.decode(Rewind.Result.self, from: data)
        #expect(result.inputCleared == true && result.codeRestored == false)
        let sent = try #require(hub.sent("POST", "/api/v1/sessions/box/s1/rewind").first)
        let raw = try #require(sent.body)
        let body = try #require(try JSONSerialization.jsonObject(with: raw) as? [String: Any])
        #expect(body["prompt"] as? String == "继续")
        #expect(body["occurrence"] as? Int == 2)
        #expect(body["prompts_after"] as? Int == 3)
        #expect(body["restore"] as? String == "conversation")
        #expect(body.count == 4)
    }
}
