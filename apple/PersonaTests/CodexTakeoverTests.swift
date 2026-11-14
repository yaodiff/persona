import Foundation
import Testing
@testable import Persona
import PersonaWire

struct CodexTakeoverTests {
    @Test func sessionCapabilitiesDecodeTakeoverAndRemainCompatible() throws {
        let current = try JSON.decoder.decode(SessionCapabilities.self, from: Data(#"{"read":true,"takeover":{"running":true,"can_resume":false,"reason":"sealed_context"}}"#.utf8))
        #expect(current.read)
        #expect(current.takeover?.running == true)
        #expect(current.takeover?.canResume == false)
        #expect(current.takeover?.reason == "sealed_context")
        let old = try JSON.decoder.decode(SessionCapabilities.self, from: Data(#"{"read":true}"#.utf8))
        #expect(old.takeover == nil && !old.send)
    }

    @Test func takeoverPreservesIndependentSourceCapabilities() throws {
        let data = Data(#"{"read":true,"close":false,"rename":false,"model":true,"rewind":true,"rewind_code":true,"takeover":{"running":true,"can_resume":false}}"#.utf8)
        let caps = try JSON.decoder.decode(SessionCapabilities.self, from: data)
        #expect(caps.read && !caps.send)
        #expect(caps.close == false && caps.rename == false)
        #expect(caps.model == true && caps.rewind == true && caps.rewindCode == true)
        #expect(caps.takeover?.running == true && caps.takeover?.canResume == false)

        let encoded = try JSONEncoder().encode(caps)
        let again = try JSON.decoder.decode(SessionCapabilities.self, from: encoded)
        #expect(again == caps)
    }

    @Test @MainActor func takeoverAPIUsesExpectedPathAndBody() async throws {
        let hub = FakeHub()
        let path = "/api/v1/machines/mac%20one/codex/thread%2F1/takeover"
        hub.on("GET", path, json: #"{"running":false,"can_resume":true,"token":"secret-token","cwd":"/work","transcript_path":"/tmp/transcript","continuation_prompt":"Continue"}"#)
        hub.on("POST", path, json: #"{"session":{"machine_id":"m","session_id":"new","title":"Codex","cwd":"/work","status":"working"}}"#)
        var api = hub.client
        api.group = "work"
        let info = try await api.codexTakeoverInfo(machine: "mac one", session: "thread/1")
        #expect(!info.running && info.canResume && info.token == "secret-token")
        let adopted = try await api.codexTakeover(machine: "mac one", session: "thread/1", token: info.token, confirmStop: false)
        #expect(adopted.group == "work" && adopted.key.group == "work")
        let req = try #require(hub.sent("POST", path).first)
        let body = try #require(req.body)
        let json = try #require(JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(json["token"] as? String == "secret-token")
        #expect(json["confirm_stop"] as? Bool == false)
        _ = try await api.codexTakeover(machine: "mac one", session: "thread/1", token: info.token, confirmStop: true)
        let confirmedData = try #require(hub.sent("POST", path).last?.body)
        let confirmed = try #require(JSONSerialization.jsonObject(with: confirmedData) as? [String: Any])
        #expect(confirmed["confirm_stop"] as? Bool == true)
    }

    @Test @MainActor func newCodexStartCarriesContinuationPromptAndTodo() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions", json: #"{"session":{"machine_id":"m","session_id":"s","title":"Codex","cwd":"/work","status":"working"}}"#)
        _ = try await hub.client.start(.init(machineID: "m", cwd: "/work", launcher: "codex", prompt: "Continue this task",
                                            async: false, todoID: "todo-1"))
        let data = try #require(hub.sent("POST", "/api/v1/sessions").first?.body)
        let json = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(json["launcher"] as? String == "codex")
        #expect(json["prompt"] as? String == "Continue this task")
        #expect(json["async"] as? Bool == false)
        #expect(json["continuation_prompt"] == nil)
        #expect(json["todo_id"] as? String == "todo-1")
    }

    @Test @MainActor func continueOnlyTakeoverRefusalMapsToEncryptedContinuation() throws {
        let original = APIClient.CodexTakeoverInfo(running: true, canResume: true, reason: nil, token: "token",
                                                   cwd: "/work", transcriptPath: "/tmp/transcript", continuationPrompt: "Continue")
        let error = APIError(status: 409, message: "", fields: ["code": "codex_takeover_continue_only"])
        let continuation = try #require(CodexTakeoverCoordinator.continuationOnlyInfo(for: error, original: original))

        #expect(!continuation.canResume)
        #expect(continuation.reason == "encrypted_context")
        #expect(continuation.running == original.running)
        #expect(continuation.token == original.token)
        #expect(continuation.cwd == original.cwd)
        #expect(continuation.transcriptPath == original.transcriptPath)
        #expect(continuation.continuationPrompt == original.continuationPrompt)
    }

    @Test @MainActor func unrelatedTakeoverErrorsKeepExistingHandling() {
        let original = APIClient.CodexTakeoverInfo(running: true, canResume: true, reason: nil, token: "token",
                                                   cwd: "/work", transcriptPath: "/tmp/transcript", continuationPrompt: "Continue")
        let apiError = APIError(status: 409, message: "", fields: ["code": "other_conflict"])
        let plainError = NSError(domain: "test", code: 1)

        #expect(CodexTakeoverCoordinator.continuationOnlyInfo(for: apiError, original: original).map(\.canResume) == nil)
        #expect(CodexTakeoverCoordinator.continuationOnlyInfo(for: plainError, original: original).map(\.canResume) == nil)
    }
}
