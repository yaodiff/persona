import Foundation
import PersonaWire
import Testing
@testable import Persona

@MainActor
struct AgentAutoRenameTests {
    private func session() throws -> Session {
        let json = #"{"machine_id":"m","session_id":"s","title":"Original","cwd":"/work","status":"idle","state":"idle"}"#
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    @Test func featureOffersOneActionToAgentsAndPreservesTheMenuOrder() throws {
        var agent = try session()
        let original = AgentActionFacts(session: agent).items
        let enabled = AgentActionFacts(session: agent, autoRename: true)
        #expect(enabled.items.filter { $0.id != .autoRename } == original)
        #expect(enabled.layout.top.contains { $0.id == .autoRename })
        #expect(enabled.layout.topLevelCount <= AgentActionLayout.topLimit)
        agent.assistant = true
        #expect(!AgentActionFacts(session: agent, autoRename: true).items.contains { $0.id == .autoRename })
    }

    @Test(arguments: ["Later manual name", "Original", nil] as [String?])
    func pendingIsImmediateAndOnlyTheSessionFeedChangesTheName(laterTitle: String?) async throws {
        let hub = FakeHub()
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        model.registry = HubRegistry(hubs: [PairedHub(id: "test", config: hub.client.config, deviceID: "phone")])
        model.buildLinks()
        model.apiForTests = hub.client
        let agent = try session()
        model.applySessions([agent.machineRef: [agent]])
        #expect(model.autoRenameAgent(agent) == nil)
        model.link("")?.received(HubHello(features: [APIClient.agentAutonameFeature]))
        let path = "/api/v1/sessions/m/s/autoname"
        let refusal = "There is no conversation to name yet."
        hub.on("POST", path) { request in
            #expect(request.body == nil && request.timeout == 60)
            guard let laterTitle else { throw APIError(status: 409, message: refusal, fields: ["code": "no_dialogue"]) }
            var newer = agent
            newer.title = laterTitle
            model.applySessions([agent.machineRef: [newer]])
            return Data(#"{"ok":true,"title":"Earlier automatic name","changed":true,"title_source":"auto"}"#.utf8)
        }
        let request = try #require(model.autoRenameAgent(agent, fromWorktreeSubmenu: true))
        #expect(model.toast?.message.hasPrefix(String(localized: "Renaming…")) == true)
        let pendingTitle = model.pendingAgentTitle(agent.key)
        let regular = SlotRow(session: agent, pendingTitle: pendingTitle)
        let compact = CompactSlotRow(session: agent, pendingTitle: pendingTitle)
        #expect(regular != SlotRow(session: agent))
        #expect(compact != CompactSlotRow(session: agent))
        #expect(regular.rowTitle == String(localized: "Renaming…"))
        #expect(compact.rowTitle == String(localized: "Renaming…"))
        #expect(model.session(agent.key)?.title == "Original")
        #expect(model.autoRenameAgent(agent) == nil)
        await request.value
        #expect(hub.sent("POST", path).count == 1)
        #expect(model.pendingAgentTitle(agent.key) == nil)
        #expect(model.session(agent.key)?.title == (laterTitle ?? "Original"))
        if laterTitle == nil {
            #expect(model.toast?.message.hasPrefix(refusal) == true)
            #expect(model.toast?.message.contains("Original") == true)
        } else {
            #expect(model.toast?.message == String(localized: "Renamed"))
        }
    }
}
