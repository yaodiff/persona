import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A running Claude Code session's model menus offer the launcher's list (#3754), New Agent's rows for that launcher,
/// instead of the fixed Default / Opus / Sonnet / Haiku: Fable and the versions a gateway serves too.
@MainActor @Suite struct RunningModelListsTests {
    private let list = [
        AgentModel(id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", isDefault: true),
        AgentModel(id: "opus", name: "Opus", resolved: "claude-opus-5-5"),
        AgentModel(id: "claude-fable-5-1", name: "Fable 5.1", resolved: "claude-fable-5-1"),
        AgentModel(id: "claude-fable-5-2", name: "Fable 5.2", resolved: "claude-fable-5-2"),
        AgentModel(id: "claude-opus-4-8", name: "Opus 4.8"),
        AgentModel(id: "vendor/typed", name: "vendor/typed", custom: true),
    ]

    private func session(_ extra: String = "") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","status":"idle","state":"idle","pane_id":"p1"\#(extra)}"#.utf8))
    }

    @Test func theMenuIsTheList() {
        #expect(PromptItems.runningModels(nil, list: list).map(\.id) == list.map(\.id))
        let names = Dictionary(uniqueKeysWithValues: PromptItems.runningModels(nil, list: list).map { ($0.id, $0.name) })
        #expect(names["claude-fable-5-2"] == "Fable 5.2")
        // An alias is named by the version it stands for; Default and typed IDs keep their names.
        #expect(names["opus"] == "Opus 5.5")
        #expect(names["default"] == "Default (recommended)")
        #expect(names["vendor/typed"] == "vendor/typed")
        #expect(PromptItems.runningModels(nil, list: list).first { $0.id == "opus" }?.tag == "opus → claude-opus-5-5")
        #expect(PromptItems.composerModels(running: nil, list: list).last?.id == PromptItems.otherModel.id)
        // No list (not read yet, or none): the fixed aliases.
        #expect(PromptItems.runningModels(nil, list: nil).map(\.id) == PromptItems.claudeModels.map(\.id))
        #expect(PromptItems.runningModels(nil, list: []).map(\.id) == PromptItems.claudeModels.map(\.id))
    }

    /// Ticked by its exact ID, else the alias that stands for it (not Default); a version is never folded into its family.
    @Test func theRunningModelIsTickedByItsID() {
        #expect(PromptItems.runningModelID("claude-fable-5-2", list: list) == "claude-fable-5-2")
        #expect(PromptItems.runningModelID("claude-opus-5-5", list: list) == "opus")
        #expect(PromptItems.runningModelID("claude-opus-4-8", list: list) == "claude-opus-4-8")
        #expect(PromptItems.runningModelID(nil, list: list) == "default")
        #expect(PromptItems.runningModelID("<synthetic>", list: list) == "default")
        // One the list doesn't have is a row of its own, the one ticked.
        #expect(PromptItems.unlistedModel("claude-sonnet-4-6", list: list) == "claude-sonnet-4-6")
        #expect(PromptItems.runningModels("claude-sonnet-4-6", list: list).last?.id == "claude-sonnet-4-6")
        #expect(PromptItems.unlistedModel("claude-fable-5-1", list: list) == nil)
        #expect(PromptItems.unlistedModel(nil, list: list) == nil)
    }

    @Test func pickingSendsTheVersion() {
        #expect(PromptItems.modelPick("claude-fable-5-1", running: "claude-fable-5-2", list: list) == .send("/model claude-fable-5-1"))
        #expect(PromptItems.modelPick("opus", running: "claude-opus-5-5", list: list) == .keep)
        #expect(SessionModes.modelPatch("claude-opus-4-8", current: "claude-opus-5-5", list: list) == "claude-opus-4-8")
        #expect(SessionModes.modelPatch("opus", current: "claude-opus-5-5", list: list) == nil)
        #expect(SessionModes.modelPatch("default", current: "claude-fable-5-2", list: list) == "default")
    }

    /// Read once per computer and launcher; a launcher that passes its own --model, or no list, keeps the fixed aliases.
    @Test func readsTheSessionsLauncherList() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/box/launchers/claude/models", json: """
            {"agent":"claude","models":[{"id":"default","name":"Default","default":true},{"id":"claude-fable-5-2","name":"Fable 5.2"}]}
            """)
        hub.on("GET", "/api/v1/machines/box/launchers/pinned/models", json: """
            {"agent":"claude","models":[{"id":"opus","name":"Opus"}],"locked":"it passes its own --model"}
            """)
        let lists = RunningModelLists(deps: .init(api: { _ in hub.client }))
        let key = SessionKey(machineID: "box", sessionID: "s1")
        let s = try session()
        #expect(lists.models(key, s) == nil)
        await lists.load(key, s)
        #expect(lists.models(key, s)?.map(\.id) == ["default", "claude-fable-5-2"])
        await lists.load(key, s)
        #expect(hub.sent("GET", "/api/v1/machines/box/launchers/claude/models").count == 1)
        let pinned = try session(#","launcher":"pinned""#)
        await lists.load(key, pinned)
        #expect(lists.models(key, pinned) == nil)
        // A session that can't switch (Codex) has no list.
        #expect(lists.models(key, try session(#","agent":"codex""#)) == nil)
    }
}
