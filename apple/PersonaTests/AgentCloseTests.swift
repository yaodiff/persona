import Foundation
import Testing
@testable import Persona
import PersonaWire

struct AgentCloseTests {
    private func session(_ state: String, status: String = "idle") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"s1","status":"\#(status)","state":"\#(state)"}"#.utf8))
    }

    @Test func activeAndUnknownAgentsNeedConfirmation() throws {
        for state in ["working", "needs_you", "unknown"] {
            #expect(AgentCloseRules.needsConfirmation(try session(state)))
        }
        #expect(AgentCloseRules.needsConfirmation(try session("idle", status: "working")))
        var waiting = try session("idle")
        waiting.pendingInteractions = 1
        #expect(AgentCloseRules.needsConfirmation(waiting))
    }

    @Test func quietAgentsCloseWithoutInterruptConfirmation() throws {
        for state in ["idle", "unread"] {
            #expect(!AgentCloseRules.needsConfirmation(try session(state)))
        }
    }

    @Test func queuedWorkNeedsConfirmation() throws {
        var queued = try session("idle")
        queued.queued = ["下一项任务"]
        #expect(AgentCloseRules.needsConfirmation(queued))
    }

    @Test func closingSelectsTheNextThenPreviousLiveTab() {
        let a = SessionKey(machineID: "box", sessionID: "a")
        let b = SessionKey(machineID: "box", sessionID: "b")
        let c = SessionKey(machineID: "box", sessionID: "c")
        #expect(AgentCloseRules.next(after: b, in: [a, b, c], available: [a, c]) == c)
        #expect(AgentCloseRules.next(after: c, in: [a, b, c], available: [a, b]) == b)
        #expect(AgentCloseRules.next(after: b, in: [a, b, c], available: [a]) == a)
        #expect(AgentCloseRules.next(after: a, in: [a], available: [a]) == nil)
        #expect(AgentCloseRules.next(after: b, in: [], available: [c]) == c)
    }

    @Test func closeUsesTheSelectedSessionOnly() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/close", json: #"{"ok":true}"#)
        try await hub.client.close(SessionKey(group: "g1", machineID: "box", sessionID: "s1"))
        #expect(hub.sent("POST", "/api/v1/sessions/box/s1/close").count == 1)
        #expect(hub.sent("POST", "/api/v1/sessions/box/s2/close").isEmpty)
    }

    @Test func closeFailuresAreNotReportedAsSuccess() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/close") { _ in
            throw APIError(status: 503, message: "节点离线")
        }
        do {
            try await hub.client.close(SessionKey(machineID: "box", sessionID: "s1"))
            Issue.record("关闭失败必须传给调用方")
        } catch {
            #expect(error is APIError)
        }
    }
}
