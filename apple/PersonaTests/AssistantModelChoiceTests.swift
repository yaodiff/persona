import Foundation
import Testing
@testable import Persona

/// The main chat's conversation-scoped model choice (#6542; hub pick #5033): what the next message runs on, the row
/// ticked in the sheet, the pick a row keeps, and the wire types it decodes.
@MainActor
struct AssistantModelChoiceTests {
    private func response(launchers: [Launcher] = [], default def: LauncherDefault? = nil,
                          unavailable: [UnavailableLauncher]? = nil) -> APIClient.LaunchersResponse {
        APIClient.LaunchersResponse(launchers: launchers, default: def, unavailable: unavailable)
    }

    private func def(launcher: String?, model: String?, effort: String? = nil) -> LauncherDefault {
        LauncherDefault(provider: launcher.map { "launcher.\($0)" }, model: model, effort: effort, runtime: nil)
    }

    // MARK: Effective resolution

    @Test func aPickWinsOverTheDefaultAndTheRunningProcess() {
        let pick = ModelPick(launcher: "team-api", model: "claude-sonnet-5-5", effort: "low")
        let r = response(launchers: [Launcher(name: "claude", model: "opus")],
                         default: def(launcher: "claude", model: "claude-opus-5-5", effort: "high"))
        let e = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: "high",
                                               pick: pick, response: r)
        #expect(e == AssistantModelChoice.Effective(launcher: "team-api", model: "claude-sonnet-5-5",
                                                    effort: "low", source: .pick))
        #expect(e.picked)
    }

    @Test func aPickWithoutEffortKeepsTheRunningEffort() {
        let pick = ModelPick(launcher: "claude", model: nil, effort: nil)
        let e = AssistantModelChoice.effective(launcher: "claude", model: "claude-opus-5-5", effort: "medium",
                                               pick: pick, response: response())
        #expect(e.effort == "medium")
        #expect(e.model == nil) // no model in the pick: the launcher's default runs
    }

    @Test func withNoPickTheMainChatDefaultNamesTheEffectiveModel() {
        let r = response(launchers: [Launcher(name: "claude", model: "opus")],
                         default: def(launcher: "claude", model: "claude-opus-5-5", effort: "high"))
        // The 11-13 incident: the running process still says Haiku; the effective default is Opus.
        let e = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: "high",
                                               pick: nil, response: r)
        #expect(e.launcher == "claude")
        #expect(e.model == "claude-opus-5-5")
        #expect(e.source == .defaultValue)
        #expect(!e.picked)
    }

    /// A default that names only a launcher runs that launcher's own default model: the running process's model belongs
    /// to its own launcher (the web's `assistantSetup`). A default that names a model runs it, as above.
    @Test func theRunningModelIsIgnoredWhenTheDefaultNamesAnotherLauncher() {
        let r = response(default: def(launcher: "team-api", model: nil))
        let e = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: nil,
                                               pick: nil, response: r)
        #expect(e.launcher == "team-api")
        #expect(e.model == nil) // the running model belongs to claude, not the default launcher
    }

    @Test func beforeThePickerAnswersTheRunningProcessIsAllThereIs() {
        let e = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: "low",
                                               pick: nil, response: nil)
        #expect(e == AssistantModelChoice.Effective(launcher: "claude", model: "claude-haiku-5-5",
                                                    effort: "low", source: .running))
    }

    @Test func anEmptyRunningLauncherFallsBackToClaude() {
        let e = AssistantModelChoice.effective(launcher: nil, model: nil, effort: nil, pick: nil, response: response())
        #expect(e.launcher == "claude")
        #expect(e.source == .defaultValue)
    }

    // MARK: Sheet ticking and pick building

    @Test func selectedModelTicksOnlyTheEffectiveLauncher() {
        let e = AssistantModelChoice.Effective(launcher: "claude", model: "opus", effort: nil, source: .pick)
        #expect(AssistantModelChoice.selectedModel(e, launcher: "claude") == "opus")
        #expect(AssistantModelChoice.selectedModel(e, launcher: "team-api") == nil)
    }

    @Test func aNilModelTicksTheDefaultRow() {
        let e = AssistantModelChoice.Effective(launcher: "claude", model: nil, effort: nil, source: .defaultValue)
        #expect(AssistantModelChoice.selectedModel(e, launcher: "claude") == "")
    }

    @Test func pickingTheCurrentSetupKeepsNothing() {
        let current = AssistantModelChoice.Effective(launcher: "claude", model: "opus", effort: "high", source: .pick)
        #expect(AssistantModelChoice.pick(launcher: "claude", model: "opus", effort: "high", current: current) == nil)
    }

    @Test func aNewModelBuildsAPickAndADefaultRowSendsNoModel() {
        let current = AssistantModelChoice.Effective(launcher: "claude", model: "opus", effort: "high", source: .defaultValue)
        let switched = AssistantModelChoice.pick(launcher: "claude", model: "sonnet", effort: "high", current: current)
        #expect(switched == ModelPick(launcher: "claude", model: "sonnet", effort: "high"))
        let toDefault = AssistantModelChoice.pick(launcher: "claude", model: nil, effort: "high", current: current)
        #expect(toDefault == ModelPick(launcher: "claude", model: nil, effort: "high"))
    }

    @Test func switchingLauncherBuildsAPickEvenWhenTheModelIdMatches() {
        let current = AssistantModelChoice.Effective(launcher: "claude", model: "opus", effort: nil, source: .defaultValue)
        let pick = AssistantModelChoice.pick(launcher: "team-api", model: "opus", effort: nil, current: current)
        #expect(pick?.launcher == "team-api")
    }

    // MARK: Display name

    @Test func displayNameUsesTheModelThenTheLaunchersOwnModelThenTheLauncher() {
        let r = response(launchers: [Launcher(name: "claude", model: "claude-opus-5-5")])
        let named = AssistantModelChoice.Effective(launcher: "claude", model: "claude-haiku-5-5", effort: nil, source: .running)
        #expect(AssistantModelChoice.displayName(named, response: r) == "Haiku 5.5")
        let onlyLauncherModel = AssistantModelChoice.Effective(launcher: "claude", model: nil, effort: nil, source: .defaultValue)
        #expect(AssistantModelChoice.displayName(onlyLauncherModel, response: r) == "Opus 5.5")
        let unknown = AssistantModelChoice.Effective(launcher: "team-api", model: nil, effort: nil, source: .pick)
        #expect(AssistantModelChoice.displayName(unknown, response: r) == "team-api")
    }

    // MARK: Wire decoding

    @Test func sessionDecodesModelPick() throws {
        let json = """
        {"machine_id":"box","session_id":"s1","title":"Main","cwd":"/tmp","status":"offline",
         "assistant":true,"launcher":"claude","model_pick":{"launcher":"team-api","model":"claude-sonnet-5-5","effort":"low"}}
        """.data(using: .utf8)!
        let s = try JSON.decoder.decode(Session.self, from: json)
        #expect(s.modelPick == ModelPick(launcher: "team-api", model: "claude-sonnet-5-5", effort: "low"))
    }

    @Test func launchersResponseDecodesDefaultAndUnavailable() throws {
        let json = """
        {"launchers":[{"name":"claude","model":"opus"}],
         "default":{"provider":"launcher.claude","model":"claude-opus-5-5","effort":"high"},
         "unavailable":[{"name":"far-gw","display_name":"Far gateway","kind":"launcher",
                         "code":"launcher_out_of_scope","reason":"out of scope"}]}
        """.data(using: .utf8)!
        let r = try JSON.decoder.decode(APIClient.LaunchersResponse.self, from: json)
        #expect(r.default?.launcher == "claude")
        #expect(r.default?.model == "claude-opus-5-5")
        #expect(r.unavailable?.first?.name == "far-gw")
        #expect(r.unavailable?.first?.reason == "out of scope")
        #expect(r.unavailable?.first?.displayName == "Far gateway")
    }

    @Test func conversationModelBodyEncodesMachineIdSnakeCase() throws {
        let body = APIClient.ConversationModelBody(launcher: "claude", model: "opus", effort: "high", machineID: "box")
        let data = try JSON.encoder.encode(body)
        let raw = try JSONDecoder().decode([String: String].self, from: data)
        #expect(raw == ["launcher": "claude", "model": "opus", "effort": "high", "machine_id": "box"])
    }
}
