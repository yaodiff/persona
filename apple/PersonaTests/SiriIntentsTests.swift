import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Pure logic behind AskPersonaIntent / AgentTaskIntent / AgentsStatusIntent.
struct SiriIntentsTests {
    private func item(_ seq: Int, role: String, kind: String = "text", text: String?) -> Item {
        var obj: [String: Any] = ["id": "i\(seq)", "seq": seq, "role": role, "kind": kind]
        if let text { obj["text"] = text }
        let data = try! JSONSerialization.data(withJSONObject: obj)
        return try! JSON.decoder.decode(Item.self, from: data)
    }

    @Test func answerJoinsAssistantTextOnly() {
        let items = [
            item(1, role: "user", text: "几点开会"),
            item(2, role: "assistant", kind: "thinking", text: "hmm"),
            item(3, role: "assistant", kind: "tool_use", text: nil),
            item(4, role: "assistant", text: "  下午三点。 "),
            item(5, role: "assistant", text: "在 3 楼。"),
        ]
        #expect(AskPersonaText.answer(in: items) == "下午三点。\n\n在 3 楼。")
        #expect(AskPersonaText.answer(in: [item(1, role: "user", text: "x")]) == nil)
    }

    @Test func turnOverOnlyWhenNotActive() {
        #expect(!AskPersonaText.turnOver(status: nil))
        #expect(!AskPersonaText.turnOver(status: .working))
        #expect(!AskPersonaText.turnOver(status: .blocked))
        #expect(AskPersonaText.turnOver(status: .idle))
        #expect(AskPersonaText.turnOver(status: .done))
    }

    @Test func spokenFallsBackAndClips() {
        #expect(AskPersonaText.spoken(nil) == AskPersonaText.pending)
        #expect(AskPersonaText.spoken(String(repeating: "字", count: 400)).count == 301)
    }

    private let agents = [
        PersonalAgentEntity(id: "ob:a", title: "写周报", provider: "claude", unread: 0, canSend: false),
        PersonalAgentEntity(id: "ob:b", title: "查数据", provider: "codex", unread: 2),
        PersonalAgentEntity(id: "ob:c", title: "日程", provider: "claude", unread: 1),
    ]

    @Test func pickerOrdersUnreadFirstAndSkipsReadOnly() {
        #expect(PersonalAgentIntentPicker.suggested(agents).map(\.id) == ["ob:b", "ob:c", "ob:a"])
        #expect(PersonalAgentIntentPicker.defaultTarget(agents)?.id == "ob:b")
        #expect(PersonalAgentIntentPicker.matching("codex", in: agents).map(\.id) == ["ob:b"])
        #expect(PersonalAgentIntentPicker.matching(" ", in: agents).count == 3)
    }

    @Test func statusWording() {
        #expect(PersonalAgentIntentText.status([]) == "没有个人 Agent 会话。")
        #expect(PersonalAgentIntentText.status(agents) == "2 个会话共 3 条未读：「查数据」2 条，「日程」1 条")
        let read = [PersonalAgentEntity(id: "x", title: "甲")]
        #expect(PersonalAgentIntentText.status(read) == "没有未读。最近的是：「甲」")
    }

    /// A Spotlight result opens its conversation (#4519): the session it hands over until the hub's copy comes.
    @Test func spotlightResultBecomesItsSession() {
        let e = PersonalAgentEntity(id: "ob:agent:x:a1:main", title: "日程", provider: "claude", unread: 2, canSend: false)
        let s = e.session
        #expect(s.id == "ob:agent:x:a1:main" && s.source == "ob" && s.sessionKey == "agent:x:a1:main")
        #expect(s.title == "日程" && s.provider == "claude" && s.unreadCount == 2 && !s.canSend)
        #expect(PersonalAgentEntity(id: "plain", title: "甲").session.sessionKey == "plain")
    }

    /// The personal Agent intents and New To-Do go to the group the app shows (#1924): the key Make Current Hub writes.
    @MainActor @Test func intentsReadTheGroupTheAppSelected() throws {
        let name = "SiriIntentsTests." + UUID().uuidString
        let d = try #require(UserDefaults(suiteName: name))
        defer { d.removePersistentDomain(forName: name) }
        #expect(IntentText.hubGroupKey == AppModel.hubGroupKey)
        #expect(IntentSupport.selectedGroup(d) == "")
        d.set("work", forKey: AppModel.hubGroupKey)
        #expect(IntentSupport.selectedGroup(d) == "work")
    }

    @Test func sentWording() {
        #expect(PersonalAgentIntentText.sent(status: "sent", to: "甲", error: nil) == "已交给「甲」")
        #expect(PersonalAgentIntentText.sent(status: "failed", to: "甲", error: "boom") == "发给「甲」失败：boom")
        #expect(PersonalAgentIntentText.name(title: " ", id: "ob:x") == "ob:x")
    }

    // MARK: New To-Do

    @Test func addTodoPostsTrimmedTitleAndNotes() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"todo_1","title":"买牛奶","agents":[]}}"#)
        let todo = try await AddTodoFlow.add(hub.client, title: "  买牛奶 ", notes: " \n ")
        #expect(todo.id == "todo_1")
        let sent = hub.sent("POST", "/api/v1/todos")
        #expect(sent.count == 1)
        let body = try #require(sent.first?.body)
        let json = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(json["title"] as? String == "买牛奶")
        #expect(json["notes"] == nil)
    }

    @Test func addTodoNeedsATitleAndExplainsAnOldHub() async throws {
        let hub = FakeHub()
        await #expect(throws: IntentFailureSentence.self) { try await AddTodoFlow.add(hub.client, title: "  ", notes: nil) }
        #expect(hub.requests.isEmpty)
        do {
            _ = try await AddTodoFlow.add(hub.client, title: "x", notes: nil)
            Issue.record("an old hub (no todos route) should fail")
        } catch {
            #expect(error.localizedDescription == TodoIntentText.oldHub)
        }
    }

    // MARK: Ask Claude / Ask Persona

    @Test func askClaudeFlowSendsClientMsgIDAndIntentViaToRuntimeSession() async throws {
        let hub = FakeHub()
        let sessionsJSON = """
        {"sessions":[{"machine_id":"mac","session_id":"agy-live","title":"Runtime Chat","cwd":"/tmp","status":"idle","state":"idle","source_kind":"runtime","capabilities":{"read":true,"send":true}}]}
        """
        let msgPath = "/api/v1/sessions/mac/agy-live/messages"
        hub.on("GET", "/api/v1/sessions", json: sessionsJSON)
        hub.on("POST", msgPath) { req in
            let body = try #require(req.body)
            let fields = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            let id = fields["client_msg_id"] as? String ?? ""
            guard !id.isEmpty else {
                throw APIError(status: 400, message: "missing client_msg_id", fields: ["code": "codex_send_guard", "delivered": "none"])
            }
            return Data("{}".utf8)
        }

        let expectedID = UUID()
        let session = try await AskClaudeFlow.sendToLatest(hub.client, machineID: "mac", group: "",
                                                           machineName: "Mac", text: "Check build status",
                                                           clientMsgID: expectedID)
        #expect(session.sessionID == "agy-live")

        let sent = hub.sent("POST", msgPath)
        try #require(sent.count == 1)
        let body = try #require(sent[0].body)
        let fields = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(fields["text"] as? String == "Check build status")
        #expect(fields["via"] as? String == "intent")
        #expect(fields["client_msg_id"] as? String == expectedID.uuidString)
        #expect(sent[0].headers[OpRetry.header] == expectedID.uuidString.lowercased())
    }
}
