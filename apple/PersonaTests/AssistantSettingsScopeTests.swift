import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Routing and ownership stay fixed while an assistant settings page has unsaved edits.
@MainActor
struct AssistantSettingsScopeTests {
    private let roster = ManagerRoster(managers: [
        HubManager(id: "assistant", name: "Default", online: true, isDefault: true),
        HubManager(id: "other", name: "Other", online: true)
    ])

    private func machine(_ id: String, manager: String? = nil, group: String? = nil) throws -> Machine {
        var machine = try JSON.decoder.decode(Machine.self, from: Data(
            #"{"id":"\#(id)","name":"Computer","kind":"desktop","online":true}"#.utf8))
        machine.manager = manager
        machine.hubGroup = group
        return machine
    }

    @Test func anAssistantsComputersExcludeTheDefaultAndEveryOtherHub() throws {
        let scope = AssistantSettingsRequestScope(group: "", manager: "other", roster: roster)
        let all = try [machine("default"), machine("owned", manager: "other"),
                       machine("remote", manager: "other", group: "second"), machine("unknown", manager: "removed")]
        #expect(scope.machines(all).map(\.id) == ["owned"])
    }

    @Test func theDefaultKeepsLegacyAndUnknownOwnershipButNotAnotherAssistantsComputer() throws {
        let scope = AssistantSettingsRequestScope(group: "", manager: nil, roster: roster)
        let all = try [machine("legacy"), machine("named", manager: "assistant"),
                       machine("unknown", manager: "removed"), machine("other", manager: "other")]
        #expect(scope.machines(all).map(\.id) == ["legacy", "named", "unknown"])
    }

    @Test func aLegacyHubStillExcludesOtherPairedHubs() throws {
        let scope = AssistantSettingsRequestScope(group: "second", manager: nil)
        #expect(scope.machines(try [machine("first"), machine("second", group: "second")]).map(\.id) == ["second"])
    }

    @Test func aRememberedAssistantFiltersComputersBeforeItsRosterArrives() throws {
        let scope = AssistantSettingsRequestScope(group: "", manager: "other")
        #expect(scope.machines(try [machine("default"), machine("owned", manager: "other")]).map(\.id) == ["owned"])
    }

    private static let detail = #"{"purpose":{"id":"agent.new","category_id":"agent.new","name":"Agents","group":"Models","kind":"launcher","uses":[],"override":{},"effective":{"model":"model-a"},"revision":0,"fields":["provider","model"]},"providers":[]}"#

    @Test func ordinaryModelSummariesDoNotExposeProviderOrExecutionMode() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        let page = ModelConfigurationModel(deps: .init(api: { hub.client }))
        await page.load(purposeID: "agent.new")
        var purpose = try #require(page.purpose)
        purpose.effective.provider = "launcher.provider"
        purpose.effective.runtime = AssistantRuntime.agentSDK
        #expect(page.modelSummary(purpose) == "model-a")
        purpose.effective.model = nil
        #expect(page.modelSummary(purpose) == String(localized: "Default [Prompt Composer 844b8c]"))
    }

    @Test func modelReadsAndWritesKeepTheirOriginalAssistantAfterTheRequestSourceChanges() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        hub.on("PATCH", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        var source = hub.client.showing("other")
        let page = ModelConfigurationModel(deps: .init(api: { source }))
        await page.load(purposeID: "agent.new")
        source = hub.client.showing(nil)
        page.draft.model = "model-b"
        #expect(await page.save())
        await page.load(purposeID: "agent.new")
        #expect(hub.requests.count == 3)
        #expect(hub.requests.allSatisfy { $0.headers[APIClient.managerHeader] == "other" })
        #expect(page.requestAPI?.managerID == "other")
    }

    @Test func theDefaultNeverInheritsALaterShownAssistantsRouting() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        hub.on("PATCH", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        var source = hub.client
        let page = ModelConfigurationModel(deps: .init(api: { source }))
        await page.load(purposeID: "agent.new")
        source = hub.client.showing("other")
        page.draft.model = "model-b"
        #expect(await page.save())
        #expect(hub.requests.count == 2)
        #expect(hub.requests.allSatisfy { $0.headers[APIClient.managerHeader] == nil })
    }
}
