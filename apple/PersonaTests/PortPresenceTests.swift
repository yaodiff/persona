import Foundation
import Testing
@testable import Persona

@MainActor
struct PortPresenceTests {
    private let machine = MachineRef(id: "mac")
    private let path = "/api/v1/machines/mac/ports/5173"

    @Test func failedQuestionIsNotAskedAgainUntilReconnect() async throws {
        let hub = FakeHub() // no ports route: every question fails
        let presence = PortPresence()
        presence.ask([5173], machine: machine, listed: [], api: hub.client)
        try await Task.sleep(for: .milliseconds(300))
        #expect(hub.sent("GET", path).count == 1)
        #expect(presence.state(5173, machine: machine, listed: []) == nil)

        // The badge scrolls back into view: the failure is remembered, nothing is sent.
        presence.ask([5173], machine: machine, listed: [], api: hub.client)
        try await Task.sleep(for: .milliseconds(300))
        #expect(hub.sent("GET", path).count == 1)

        // Another group's reconnect changes nothing; this group's asks again.
        presence.reconnected(group: "other")
        presence.ask([5173], machine: machine, listed: [], api: hub.client)
        try await Task.sleep(for: .milliseconds(300))
        #expect(hub.sent("GET", path).count == 1)
        presence.reconnected(group: machine.group)
        presence.ask([5173], machine: machine, listed: [], api: hub.client)
        try await Task.sleep(for: .milliseconds(300))
        #expect(hub.sent("GET", path).count == 2)
    }

    @Test func cachedAgentPortReprobesOpenPortAfterFirstFailure() async {
        let listed = [ListeningPort(port: 5173, addr: "127.0.0.1", pid: 1, process: "node", title: "Dev")]
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/mac/ports/5173/open") { _ in
            throw APIError(status: 404, message: "nothing is listening")
        }
        let presence = PortPresence()

        // Initially trusted from agentPorts cache.
        #expect(presence.state(5173, machine: machine, listed: listed) == .live(title: "Dev"))
        #expect(!presence.shouldOpenPort(5173, machine: machine, listed: listed))

        // After first open fails, reprobe calls POST …/open and marks the port gone immediately.
        let outcome = await presence.reprobe(port: 5173, machine: machine, api: hub.client)
        #expect(outcome == .gone)
        #expect(hub.sent("POST", "/api/v1/machines/mac/ports/5173/open").count == 1)
        #expect(presence.state(5173, machine: machine, listed: listed) == .gone)
        #expect(presence.shouldOpenPort(5173, machine: machine, listed: listed))

        // Once the server comes back, reprobe restores live state.
        hub.on("POST", "/api/v1/machines/mac/ports/5173/open", json: #"{"port":5173,"listening":true,"title":"Back"}"#)
        let recovered = await presence.reprobe(port: 5173, machine: machine, api: hub.client)
        #expect(recovered == .live)
        #expect(presence.state(5173, machine: machine, listed: listed) == .live(title: "Dev"))
        #expect(!presence.shouldOpenPort(5173, machine: machine, listed: listed))
    }
}
