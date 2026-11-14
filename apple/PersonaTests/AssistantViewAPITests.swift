import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct AssistantViewAPITests {
    @Test func opaqueIDsAreEncodedOnceAndDeclaredReplyCannotBeOverridden() async throws {
        let hub = FakeHub()
        let path = "/api/v1/assistant/conversations/conversation%2F%23%25%3F/views/item%2F%23%252F%3F%2F0/actions"
        hub.on("POST", path, json: #"{"action":{"key":"item/#%2F?/0","action":"choose","client_msg_id":"client","status":"pending","revision":1,"version":7}}"#)
        let input = ExtensionViewActionInput(action: "choose", values: ["task": "读书"], clientMsgId: "client", confirmed: true, expectedVersion: 7)
        let result = try await hub.client.assistantViewAction(conversation: "conversation/#%?", key: "item/#%2F?/0", input: input)
        #expect(result.key == "item/#%2F?/0" && result.version == 7)
        let request = try #require(hub.requests.last)
        let data = try #require(request.body)
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(Set(body.keys) == ["action", "values", "client_msg_id", "confirmed", "expected_version"])
        #expect(body["expected_version"] as? Int == 7 && body["reply"] == nil)
    }
    @Test func HTMLHasItsOwnGeneratedInputAndEndpoint() async throws {
        let hub = FakeHub()
        let path = "/api/v1/assistant/conversations/conversation/views/view_example/html-actions"
        hub.on("POST", path, json: #"{"action":{"key":"view_example","action":"drawing","client_msg_id":"client","status":"sent","revision":2,"kind":"html","version":3,"text":"选 B"}}"#)
        let input = ExtensionAssistantHTMLActionInput(kind: "reply", componentKey: "drawing", page: "detail", expectedVersion: 3,
                                                       text: "选 B", clientMsgId: "client", confirmed: true)
        let result = try await hub.client.assistantHTMLReply(conversation: "conversation", key: "view_example", input: input)
        #expect(result.kind == "html" && result.action == "drawing" && result.text == "选 B")
        let request = try #require(hub.requests.last)
        let data = try #require(request.body)
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(body["component_key"] as? String == "drawing" && body["page"] as? String == "detail")
        #expect(body["expected_version"] as? Int == 3 && body["confirmed"] as? Bool == true)
        #expect(body["action"] == nil && body["reply"] == nil)
    }
    @Test func HTMLOpenReturnsTheVersionBoundTarget() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/conversations/conversation/views/view_example/html-actions", json: #"{"open":"https://example.com/card","version":9}"#)
        let result = try await hub.client.assistantHTMLOpen(conversation: "conversation", key: "view_example",
            input: ExtensionAssistantHTMLActionInput(kind: "open", componentKey: "drawing", expectedVersion: 9,
                                                     url: "https://example.com/card", clientMsgId: "client", confirmed: true))
        #expect(result.open == "https://example.com/card" && result.version == 9)
    }
    @Test func versionConflictKeepsItsSpecificErrorCode() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/conversations/conversation/views/view_example/actions") { _ in
            throw APIError(status: 409, message: "卡片已更新", fields: ["code": "view_changed"])
        }
        do {
            _ = try await hub.client.assistantViewAction(conversation: "conversation", key: "view_example",
                input: ExtensionViewActionInput(action: "choose", clientMsgId: "client", confirmed: true, expectedVersion: 1))
            Issue.record("a stale confirmation was accepted")
        } catch let error as APIError { #expect(error.status == 409 && error.code == "view_changed") }
    }
    @Test func warmWithoutALauncherOmitsTheFieldSoTheHubUsesItsDefault() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/warm", json: "{}")
        try await hub.client.warmAssistant(launcher: nil)
        let request = try #require(hub.requests.last)
        let data = try #require(request.body)
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(body["launcher"] == nil)
    }
    @Test func warmWithAnEmptyLauncherOmitsTheField() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/warm", json: "{}")
        try await hub.client.warmAssistant(launcher: "")
        let request = try #require(hub.requests.last)
        let data = try #require(request.body)
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(body["launcher"] == nil)
    }
    @Test func warmWithALauncherSendsIt() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/warm", json: "{}")
        try await hub.client.warmAssistant(launcher: "codex")
        let request = try #require(hub.requests.last)
        let data = try #require(request.body)
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(body["launcher"] as? String == "codex")
    }
    @Test func newConversationWithoutALauncherOmitsTheField() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/conversations",
               json: #"{"session":{"session_id":"s1","status":"working","state":"working","started_by_persona":true,"assistant":true,"pending_interactions":0}}"#)
        let session = try await hub.client.startConversation(prompt: "你好")
        #expect(session.sessionID == "s1")
        let request = try #require(hub.requests.last)
        let data = try #require(request.body)
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(body["prompt"] as? String == "你好")
        #expect(body["launcher"] == nil)
    }
    @Test func newConversationWithALauncherSendsIt() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/conversations",
               json: #"{"session":{"session_id":"s2","status":"working","state":"working","started_by_persona":true,"assistant":true,"pending_interactions":0}}"#)
        _ = try await hub.client.startConversation(prompt: "你好", launcher: "codex")
        let request = try #require(hub.requests.last)
        let data = try #require(request.body)
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(body["launcher"] as? String == "codex")
    }
    @Test func executionBudgetNeverGrantsAThirdCardAndReleaseIsOwnerScoped() {
        let budget = HTMLExecutionBudget(), first = UUID(), second = UUID(), third = UUID()
        #expect(budget.acquire(first) && budget.acquire(second))
        #expect(budget.acquire(first) && !budget.acquire(third))
        budget.release(UUID())
        #expect(budget.owners.count == 2)
        budget.release(first)
        #expect(budget.acquire(third) && budget.owners == [second, third])
    }
}
