import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Delete Chat (issue #1749): which conversations offer it, what the confirmation says, the request it sends.
struct DeleteSessionTests {
    private func session(status: String, extra: String = "") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"s1","status":"\#(status)","state":"offline"\#(extra)}"#.utf8))
    }

    @Test func onlyConversationsThatAreNotRunningCanBeDeleted() throws {
        // What the hub accepts and then hides (server/internal/hub/deleted_sessions.go `stillDeleted`).
        // Idle (finished and seen) too; an "interrupted" from a newer hub reads as unknown.
        for status in ["offline", "done", "idle", "unknown", "interrupted"] {
            #expect(try session(status: status).canDelete, "\(status)")
        }
        // Running or asking: close it first.
        for status in ["working", "blocked"] {
            #expect(!(try session(status: status).canDelete), "\(status)")
        }
    }

    @Test func startingAndAssistantConversationsCannotBeDeleted() throws {
        #expect(!(try session(status: "offline", extra: #","starting":true"#).canDelete))
        // The assistant's conversations archive instead.
        #expect(!(try session(status: "offline", extra: #","assistant":true"#).canDelete))
        // A start that failed goes like any other offline one.
        #expect(try session(status: "offline", extra: #","start_error":"claude 没有启动""#).canDelete)
    }

    @Test func theConfirmationSaysTheRecordStays() {
        #expect(DeleteSessionText.message(title: "修登录", machine: "工作站", resume: "claude --resume")
            == "「修登录」会从各端的列表里移除。工作站 上的对话记录不会删除，之后在电脑上用 claude --resume 继续，它会重新出现。")
        #expect(DeleteSessionText.message(title: "修登录", machine: nil, resume: nil)
            == "「修登录」会从各端的列表里移除。电脑上的对话记录不会删除，之后再用到它，它会重新出现。")
    }

    @Test func deleteSendsTheSessionsDelete() async throws {
        let hub = FakeHub()
        hub.on("DELETE", "/api/v1/sessions/box/s1", json: #"{"ok":true}"#)
        try await hub.client.deleteSession(SessionKey(group: "g1", machineID: "box", sessionID: "s1"))
        #expect(hub.sent("DELETE", "/api/v1/sessions/box/s1").count == 1)
    }

    @Test func theHubsRefusalReachesTheCaller() async throws {
        let hub = FakeHub()
        hub.on("DELETE", "/api/v1/sessions/box/s1") { _ in
            throw APIError(status: 409, message: "这个 Agent 还在运行，先关闭再删除")
        }
        do {
            try await hub.client.deleteSession(SessionKey(machineID: "box", sessionID: "s1"))
            Issue.record("删除失败必须传给调用方")
        } catch {
            #expect(error.localizedDescription == "这个 Agent 还在运行，先关闭再删除")
        }
    }
}
