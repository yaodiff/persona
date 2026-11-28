import Foundation
import Testing
@testable import Persona

/// Opt-in networking against the separately launched, pre-rename fixture hub. Never uses saved pairing or a live hub.
@MainActor struct OldHubCompatibilityIntegrationTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["PERSONA_COMPAT_TEST_URL"] != nil))
    func realOldHubSupportsHelloSessionsStreamsReceiptsAndPermissionCards() async throws {
        let env = ProcessInfo.processInfo.environment
        let url = try #require(env["PERSONA_COMPAT_TEST_URL"].flatMap(URL.init(string:)))
        #expect(url.scheme == "http" && url.host == "127.0.0.1")
        guard url.scheme == "http", url.host == "127.0.0.1" else { throw URLError(.unsupportedURL) }
        let token = try #require(env["PERSONA_COMPAT_TEST_TOKEN"])
        let api = APIClient(config: ServerConfig(url: url, token: token))
        let live = LiveConnection()
        var hello: HubHello?
        var deltas: [String] = []
        live.onEvent = { event in
            switch event {
            case .hello(let value): hello = value
            case .delta(_, let text): deltas.append(text)
            default: break
            }
        }
        live.connect(api.config)
        defer { live.disconnect() }
        try await until("authenticated old hello") { hello != nil }
        #expect(hello?.features.contains(HubNaming.feature) == false)
        #expect(!live.personaContracts)
        #expect(!api.personaNaming)

        let machines = try await api.machines()
        #expect(machines.contains { $0.id == "fixture" && $0.online })
        let started = try await api.start(APIClient.StartRequest(machineID: "fixture", cwd: "/tmp", launcher: "echo"))
        let key = SessionKey(machineID: "fixture", sessionID: started.sessionID)
        let wire = try JSONSerialization.jsonObject(with: await api.request("GET", "/api/v1/sessions", query: ["machine_id": "fixture"])) as? [String: Any]
        let row = try #require((wire?["sessions"] as? [[String: Any]])?.first { $0["session_id"] as? String == key.sessionID })
        let oldOrigin = try #require(row[LegacyNames.startedByField] as? Bool)
        #expect(row["started_by_persona"] == nil, "the sandbox must really return the pre-rename contract")
        #expect(started.startedByPersona == oldOrigin)
        live.send(["type": "subscribe", "machine_id": key.machineID, "session_id": key.sessionID, "since_seq": 0])
        let id = UUID()
        let text = "Compatibility receipt fixture"
        try await api.send(key, text: text, clientMsgID: id.uuidString, opID: id)
        try await api.send(key, text: text, clientMsgID: id.uuidString, opID: id)
        try await until("echo answer") { try await api.items(key, beforeSeq: nil).items.contains { $0.text == "echo: " + text } }
        let items = try await api.items(key, beforeSeq: nil).items
        #expect(items.filter { $0.role == "user" && $0.text == text }.count == 1, "one logical write, one delivery")
        #expect(items.filter { $0.role == "assistant" && $0.text == "echo: " + text }.count == 1)
        #expect(!deltas.isEmpty, "actual WebSocket streaming, not only REST history")

        let permissionID = UUID()
        try await api.send(key, text: "ask compatibility fixture", clientMsgID: permissionID.uuidString, opID: permissionID)
        var card: Interaction?
        try await until("runtime permission card") {
            card = try await api.interactions().first { $0.sessionID == key.sessionID && $0.kind == .permission }
            return card != nil
        }
        let current = try #require(card)
        _ = try await api.resolve(current.id, Resolution(allow: true), opID: UUID(), occurrence: current.createdAtWire)
        try await until("permission-approved echo") {
            try await api.items(key, beforeSeq: nil).items.contains { $0.text == "echo: ask compatibility fixture" }
        }
        let main = try await api.startConversation(prompt: "Compatibility assistant integration fixture", launcher: "compat-assistant")
        #expect(main.assistant && main.startedByPersona, "the real old main-conversation origin marker must survive")
        let mainKey = SessionKey(machineID: "fixture", sessionID: main.sessionID)
        try await until("old-hub assistant reply") {
            try await api.items(mainKey, beforeSeq: nil).items.contains { $0.role == "assistant" && ($0.text?.contains("Compatibility assistant integration fixture") ?? false) }
        }
        live.disconnect()
        hello = nil
        #expect(!live.personaContracts)
        live.connect(api.config)
        try await until("reconnected old hello") { hello != nil }
        #expect(!live.personaContracts)
        #expect(try await api.items(key, beforeSeq: nil).items.contains { $0.text == "echo: ask compatibility fixture" })
    }

    private func until(_ label: String, seconds: TimeInterval = 30, _ condition: () async throws -> Bool) async throws {
        let deadline = Date().addingTimeInterval(seconds)
        while Date() < deadline {
            if try await condition() { return }
            try await Task.sleep(for: .milliseconds(100))
        }
        Issue.record("Timed out: \(label)")
        throw URLError(.timedOut)
    }
}
