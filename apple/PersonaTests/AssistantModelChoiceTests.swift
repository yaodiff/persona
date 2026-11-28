import Foundation
import Testing
@testable import Persona
import PersonaWire

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

    @Test func aDefaultModelWinsEvenWhenTheRunningProcessUsesAnotherLauncher() {
        let r = response(default: def(launcher: "team-api", model: "claude-sonnet-5-5"))
        let e = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: nil,
                                               pick: nil, response: r)
        #expect(e.launcher == "team-api")
        #expect(e.model == "claude-sonnet-5-5")
        #expect(e.source == .defaultValue)
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

    @Test func anOrdinarySideConversationKeepsWhatItStartedWith() {
        let main = response(default: def(launcher: "team-api", model: "claude-opus-5-5", effort: "high"))
        let e = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: "low",
                                               pick: nil, response: main, rule: .own)
        #expect(e == AssistantModelChoice.Effective(launcher: "claude", model: "claude-haiku-5-5",
                                                    effort: "low", source: .own))
        let picked = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: "low",
                                                    pick: ModelPick(launcher: "fast", model: "swift"), response: main, rule: .own)
        #expect(picked.launcher == "fast" && picked.model == "swift" && picked.source == .pick)
    }

    @Test func theSupervisorUsesItsOwnPurposeInsteadOfMainChat() {
        let main = response(default: def(launcher: "claude", model: "claude-haiku-5-5"))
        let supervise = response(default: def(launcher: "team-api", model: "claude-opus-5-5", effort: "high"))
        let e = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: nil,
                                               pick: nil, response: main, rule: .supervise, superviseResponse: supervise)
        #expect(e == AssistantModelChoice.Effective(launcher: "team-api", model: "claude-opus-5-5",
                                                    effort: "high", source: .defaultValue))
        // While the separate purpose is loading it still inherits Main chat, as the hub does until set apart.
        let loading = AssistantModelChoice.effective(launcher: "claude", model: "claude-haiku-5-5", effort: nil,
                                                     pick: nil, response: main, rule: .supervise)
        #expect(loading.model == "claude-haiku-5-5")
        // An answered purpose with no override falls back to this session, not to an unrelated Main-chat model.
        let empty = AssistantModelChoice.effective(launcher: "side", model: "side-model", effort: "low",
                                                   pick: nil, response: main, rule: .supervise, superviseResponse: response())
        #expect(empty.launcher == "side" && empty.model == "side-model" && empty.effort == "low")
    }

    @Test func onlyTheSupervisorPanelReadsTheSupervisePurpose() async {
        let key = SessionKey(group: "", machineID: "box", sessionID: "s")
        let main = response()
        let supervise = response(default: def(launcher: "team-api", model: "claude-opus-5-5"))
        var superviseReads = 0
        func deps() -> AssistantModelPanelModel.Deps {
            .init(launchers: { _, _ in main }, supervise: { _, _ in superviseReads += 1; return supervise },
                  models: { _, _, _ in LauncherModels(agent: "claude", models: []) },
                  keep: { _, _ in true }, drop: { _ in true })
        }
        let mainPanel = AssistantModelPanelModel(key: key, machine: key.machineRef, deps: deps())
        await mainPanel.load()
        #expect(superviseReads == 0)
        let supervisorPanel = AssistantModelPanelModel(key: key, machine: key.machineRef, defaultRule: .supervise, deps: deps())
        await supervisorPanel.load()
        #expect(superviseReads == 1)
        #expect(supervisorPanel.superviseResponse?.default?.launcher == "team-api")
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

    @Test func equivalentAliasAndConfiguredRowsAreNoOps() {
        let current = AssistantModelChoice.Effective(launcher: "claude", model: "claude-opus-5-5", effort: "high",
                                                     source: .defaultValue)
        let list = LauncherModels(agent: "claude", models: [
            AgentModel(id: "default", name: "Default", resolved: "claude-opus-5-5", isDefault: true),
            AgentModel(id: "opus", name: "Opus", resolved: "claude-opus-5-5")
        ], locked: nil, machine: nil)
        let aliases = AssistantModelChoice.selectionAliases(list)
        #expect(AssistantModelChoice.pick(launcher: "claude", model: "opus", effort: "high", current: current,
                                          aliases: aliases) == nil)
        #expect(AssistantModelChoice.pick(launcher: "claude", model: nil, effort: "high", current: current,
                                          aliases: aliases) == ModelPick(launcher: "claude", model: nil, effort: "high"))

        let inherited = AssistantModelChoice.Effective(launcher: "team-api", model: nil, effort: nil, source: .defaultValue)
        #expect(AssistantModelChoice.pick(launcher: "team-api", model: "claude-sonnet-5-5", effort: nil,
                                          current: inherited, configuredModel: "claude-sonnet-5-5") == nil)
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

    @Test func resolvedModelsTickTheirAliasAndDefaultRows() {
        let aliases = LauncherModels(agent: "claude", models: [
            AgentModel(id: "opus", name: "Opus", resolved: "claude-opus-5-5"),
            AgentModel(id: "haiku", name: "Haiku", resolved: "claude-haiku-5-5")
        ], locked: nil, machine: nil)
        let e = AssistantModelChoice.Effective(launcher: "claude", model: "CLAUDE-OPUS-5-5", effort: nil, source: .pick)
        #expect(AssistantModelChoice.selectedModel(
            e, launcher: "claude", aliases: AssistantModelChoice.selectionAliases(aliases)) == "opus")
        #expect(AssistantModelChoice.pick(launcher: "claude", model: "opus", effort: nil, current: e,
                                          aliases: AssistantModelChoice.selectionAliases(aliases)) == nil)

        let withDefault = LauncherModels(agent: "claude", models: [
            AgentModel(id: "default", name: "Default", resolved: "claude-opus-5-5", isDefault: true),
            AgentModel(id: "opus", name: "Opus", resolved: "claude-opus-5-5")
        ], locked: nil, machine: nil)
        #expect(AssistantModelChoice.selectedModel(
            e, launcher: "claude", aliases: AssistantModelChoice.selectionAliases(withDefault)) == "opus")
    }

    @Test func theModelRouteBackActionNamesWhatTheConversationFollows() {
        #expect(AssistantModelChoice.followsDefault(isMain: true, supervisor: false, leader: false, origin: nil))
        #expect(AssistantModelChoice.followsDefault(isMain: false, supervisor: true, leader: false, origin: nil))
        #expect(AssistantModelChoice.followsDefault(isMain: false, supervisor: false, leader: true, origin: nil))
        #expect(AssistantModelChoice.followsDefault(isMain: false, supervisor: false, leader: false, origin: "task"))
        #expect(!AssistantModelChoice.followsDefault(isMain: false, supervisor: false, leader: false, origin: "manual"))
        #expect(AssistantModelChoice.defaultRule(isMain: true, supervisor: false, leader: false, origin: nil) == .main)
        #expect(AssistantModelChoice.defaultRule(isMain: false, supervisor: false, leader: true, origin: nil) == .main)
        #expect(AssistantModelChoice.defaultRule(isMain: false, supervisor: true, leader: false, origin: nil) == .supervise)
        #expect(AssistantModelChoice.defaultRule(isMain: false, supervisor: false, leader: false, origin: "manual") == .own)
    }

    @Test func everyAssistantConversationCanPickWithoutChangingTheOlderHubGate() {
        #expect(AssistantModelChoice.pickerAvailable(assistant: true, keepsPick: true))
        #expect(!AssistantModelChoice.pickerAvailable(assistant: false, keepsPick: true))
        #expect(!AssistantModelChoice.pickerAvailable(assistant: true, keepsPick: false))
    }

    @Test func foldedAliasesTickTheConcreteRowAndMakeItANoOp() {
        let list = LauncherModels(agent: "claude", models: [
            AgentModel(id: "default", name: "Default", resolved: "claude-opus-5-5", isDefault: true),
            AgentModel(id: "claude-opus-5-5", name: "Opus 5.5", aliases: ["opus", "fable", "sonnet", "haiku"]),
            AgentModel(id: "kiro/claude-opus-5-5", name: "Opus 5.5 (kiro)"),
        ], locked: nil, machine: nil)
        let aliases = AssistantModelChoice.selectionAliases(list)
        let canonical = AssistantModelChoice.Effective(launcher: "claude", model: "claude-opus-5-5", effort: "high", source: .pick)
        #expect(AssistantModelChoice.selectedModel(canonical, launcher: "claude", aliases: aliases) == "claude-opus-5-5",
                "a concrete exact ID wins over Default resolving to the same model")
        for saved in ["opus", "fable", "sonnet", "haiku"] {
            let current = AssistantModelChoice.Effective(launcher: "claude", model: saved, effort: "high", source: .pick)
            #expect(AssistantModelChoice.selectedModel(current, launcher: "claude", aliases: aliases) == "claude-opus-5-5")
            #expect(AssistantModelChoice.pick(launcher: "claude", model: "claude-opus-5-5", effort: "high",
                                              current: current, aliases: aliases) == nil)
        }
        let current = AssistantModelChoice.Effective(launcher: "claude", model: "sonnet", effort: "high", source: .pick)
        #expect(AssistantModelChoice.pick(launcher: "claude", model: "kiro/claude-opus-5-5", effort: "high",
                                          current: current, aliases: aliases)
                == ModelPick(launcher: "claude", model: "kiro/claude-opus-5-5", effort: "high"))

        let collision = LauncherModels(agent: "claude", models: [
            AgentModel(id: "claude-opus-5-5", name: "Folded", aliases: ["sonnet"]),
            AgentModel(id: "sonnet", name: "Exact"),
        ], locked: nil, machine: nil)
        #expect(AssistantModelChoice.selectionAliases(collision)["sonnet"] == "sonnet", "an exact row ID beats an alias")
    }

    @Test func lockedLaunchersKeepOnlyTheirSelectableDefault() {
        let list = LauncherModels(agent: "claude", models: [
            AgentModel(id: "default", name: "Default", isDefault: true),
            AgentModel(id: "opus", name: "Opus")
        ], locked: "The launcher fixes its model", machine: nil)
        #expect(AssistantModelChoice.visibleModels(list).map(\.id) == ["default"])
        #expect(AssistantModelChoice.visibleModels(nil).isEmpty)
        var unlocked = list
        unlocked.locked = nil
        #expect(AssistantModelChoice.visibleModels(unlocked).count == 2)
    }

    @Test func anEmptyCatalogFallsBackToTheConfiguredModel() {
        let launcher = Launcher(name: "team-api", model: "claude-sonnet-5-5")
        #expect(AssistantModelChoice.configuredFallback(launcher, models: [], locked: nil) ==
                AssistantModelChoice.ConfiguredFallback(id: "claude-sonnet-5-5", sendsDefault: false))
        #expect(AssistantModelChoice.configuredFallback(launcher, models: [], locked: "locked") ==
                AssistantModelChoice.ConfiguredFallback(id: "claude-sonnet-5-5", sendsDefault: true))
        let models = [AgentModel(id: "opus", name: "Opus")]
        #expect(AssistantModelChoice.configuredFallback(launcher, models: models, locked: nil) == nil)

        let inherited = AssistantModelChoice.Effective(launcher: "team-api", model: nil, effort: nil, source: .defaultValue)
        #expect(AssistantModelChoice.selectedModel(inherited, launcher: "team-api",
                                                   configuredModel: "claude-sonnet-5-5") == "claude-sonnet-5-5")
        #expect(AssistantModelChoice.pick(launcher: "team-api", model: nil, effort: nil, current: inherited,
                                          configuredModel: "claude-sonnet-5-5", configuredIsDefault: true) == nil)
    }

    @Test func onlyKnownDifferentConversationEnginesAreRefused() {
        #expect(AssistantModelChoice.isOtherEngine(sessionAgent: "claude", launcherAgent: "codex"))
        #expect(AssistantModelChoice.isOtherEngine(sessionAgent: "codex-cli", launcherAgent: "claude-code"))
        #expect(AssistantModelChoice.isOtherEngine(sessionAgent: "codex", launcherAgent: nil))
        #expect(!AssistantModelChoice.isOtherEngine(sessionAgent: "claude", launcherAgent: "claudecode"))
        #expect(!AssistantModelChoice.isOtherEngine(sessionAgent: "dsh", launcherAgent: "codex"))
        #expect(!AssistantModelChoice.isOtherEngine(sessionAgent: "claude", launcherAgent: "dsh"))
        #expect(!AssistantModelChoice.isOtherEngine(sessionAgent: nil, launcherAgent: "codex"))
        #expect(AssistantModelChoice.isOtherEngine(sessionAgent: "", launcherAgent: "codex"))
        #expect(AssistantModelChoice.otherEngineName("codex-cli") == "Codex")
        #expect(AssistantModelChoice.otherEngineName("Codex") == "Codex")
        #expect(AssistantModelChoice.otherEngineName("claude") == "Claude Code")
    }

    @Test func codexSourceAlsoDrivesTheConversationEngine() throws {
        let json = #"{"machine_id":"box","session_id":"s1","source_kind":"codex_app_server"}"#
        let session = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        #expect(AssistantModelChoice.sessionEngine(session) == "codex")
        #expect(AssistantModelChoice.isOtherEngine(sessionAgent: AssistantModelChoice.sessionEngine(session),
                                                   launcherAgent: "claude"))
    }

    @Test func explicitSwitchRecognizesOtherEnginesAndRuntimeConnectionIdentity() throws {
        let claude = try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","launcher":"claude","agent":"claude"}"#.utf8))
        let agy = Launcher(name: "agy", model: nil, agent: "antigravity", source: "path")
        #expect(AssistantModelChoice.switchNeeded(session: claude, target: agy))
        #expect(!AssistantModelChoice.switchNeeded(
            session: claude, target: Launcher(name: "other-claude", model: nil, agent: "claude", source: "zshrc")))

        let hosted = Launcher(name: "hosted-claude", model: nil, agent: "claude", source: "runtime-one")
        #expect(AssistantModelChoice.switchNeeded(session: claude, target: hosted))
        let runtime = try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s2","launcher":"old","agent":"claude","source_kind":"runtime","source_instance_id":"runtime-one"}"#.utf8))
        #expect(!AssistantModelChoice.switchNeeded(session: runtime, target: hosted))
        #expect(AssistantModelChoice.switchNeeded(
            session: runtime, target: Launcher(name: "hosted-elsewhere", model: nil, agent: "claude", source: "runtime-two")))
    }

    @Test func supervisionEligibilityRequiresACapableRuntimeWithoutChangingNativeClaudeOrCodex() {
        #expect(AssistantModelChoice.launcherSupervisionPolicy(Launcher(name: "claude")))
        #expect(AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "codex", agent: "codex", source: "path")))
        #expect(AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "managed", agent: "claude", source: "persona")))
        #expect(AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "legacy-managed", agent: "codex", source: LegacyNames.product)))
        #expect(!AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "persona_ext_runtime", agent: "claude", assistant: true, supervisionPolicy: true, source: "persona")))
        #expect(!AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "agy", agent: "antigravity", assistant: true, source: "agy")))
        #expect(!AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "agy", agent: "antigravity", assistant: true, source: "path")))
        #expect(!AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "agy", agent: "antigravity", assistant: true, supervisionPolicy: true, source: "path")))
        #expect(!AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "managed-agy", agent: "antigravity", assistant: true, supervisionPolicy: true, source: "persona")))
        #expect(!AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "ordinary", agent: "antigravity", supervisionPolicy: true, source: "agy")))
        #expect(AssistantModelChoice.launcherSupervisionPolicy(
            Launcher(name: "agy", agent: "antigravity", assistant: true, supervisionPolicy: true, source: "agy")))
    }

    @Test func explicitSwitchUsesPaseoProvidersAndExtensionConnectionIDs() throws {
        let paseo = try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s3","launcher":"grok","agent":"paseo"}"#.utf8))
        let cursor = Launcher(name: "cursor", model: nil, agent: "paseo", source: "runtime-one")
        #expect(AssistantModelChoice.sessionEngine(paseo) == "grok")
        #expect(AssistantModelChoice.switchNeeded(session: paseo, target: cursor))
        #expect(!AssistantModelChoice.switchNeeded(
            session: paseo, target: Launcher(name: "grok", model: nil, agent: "paseo", source: "runtime-one")))

        let extensionSession = try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s4","launcher":"persona_ext_first","agent":"claude"}"#.utf8))
        #expect(AssistantModelChoice.switchNeeded(
            session: extensionSession, target: Launcher(name: "persona_ext_second", model: nil, agent: "claude", source: "persona")))
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
        let builtin = AssistantModelChoice.Effective(launcher: "claude-desktop", model: nil, effort: nil, source: .pick)
        #expect(AssistantModelChoice.displayName(builtin, response: r) == "Claude Desktop")
    }

    @Test func anOlderNodesDefaultPlaceholderIsNoExplicitModel() {
        let legacy = Launcher(name: "claude", model: "默认")
        let empty = Launcher(name: "claude")
        #expect(AssistantModelChoice.configuredFallback(legacy, models: [], locked: nil) ==
                AssistantModelChoice.configuredFallback(empty, models: [], locked: nil))

        let effective = AssistantModelChoice.Effective(launcher: "claude", model: nil, effort: nil, source: .defaultValue)
        let oldWords = AssistantModelChoice.displayName(effective, response: response(launchers: [legacy]))
        #expect(oldWords == AssistantModelChoice.displayName(effective, response: response(launchers: [empty])))
        #expect(oldWords != "默认")
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

    @Test func aMalformedModelPickDoesNotDropTheSession() throws {
        for pick in [#"{"launcher":123}"#, #""odd""#] {
            let json = """
            {"machine_id":"box","session_id":"s1","title":"Main","cwd":"/tmp","status":"offline",
             "assistant":true,"model_pick":\(pick)}
            """.data(using: .utf8)!
            let s = try JSON.decoder.decode(Session.self, from: json)
            #expect(s.sessionID == "s1")
            #expect(s.modelPick == nil)
        }
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

    @Test func launcherDecodesFriendlyNameAndRuntimeKind() throws {
        let json = """
        {"name":"team-api","display_name":"Team API","model":"claude-sonnet-5-5","agent_type":"claude",
         "assistant":true,"supervision_policy":true}
        """.data(using: .utf8)!
        let launcher = try JSON.decoder.decode(Launcher.self, from: json)
        #expect(launcher.displayName == "Team API")
        #expect(launcher.agentType == "claude")
        #expect(launcher.runtimeAgent == "claude")
        #expect(launcher.assistant == true && launcher.supervisionPolicy == true)

        let fallback = try JSON.decoder.decode(Launcher.self, from: Data(#"{"name":"cx","agent_type":"","agent":"codex"}"#.utf8))
        #expect(fallback.runtimeAgent == "codex")
        #expect(fallback.supervisionPolicy == nil)

        let odd = try JSON.decoder.decode(Launcher.self, from: Data(#"{"name":"cx","display_name":{},"agent_type":123,"agent":"codex"}"#.utf8))
        #expect(odd.name == "cx")
        #expect(odd.displayName == nil && odd.agentType == nil)
        #expect(odd.runtimeAgent == "codex")
    }

    @Test func assistantStateUsesTheConfirmedMainMachine() throws {
        let old = try JSON.decoder.decode(APIClient.AssistantState.self, from: Data("""
        {"machine_id":"default","launcher":"claude","cwd":"/home","main":"main-session","conversations":[]}
        """.utf8))
        #expect(old.mainKey() == SessionKey(machineID: "default", sessionID: "main-session"))

        let routed = try JSON.decoder.decode(APIClient.AssistantState.self, from: Data("""
        {"machine_id":"default","main_machine_id":"route-box","launcher":"claude","cwd":"/home",
         "main":"main-session","conversations":[]}
        """.utf8))
        #expect(routed.mainKey() == SessionKey(machineID: "route-box", sessionID: "main-session"))

        let empty = try JSON.decoder.decode(APIClient.AssistantState.self, from: Data("""
        {"machine_id":"default","main_machine_id":"","launcher":"claude","cwd":"/home",
         "main":"main-session","conversations":[]}
        """.utf8))
        #expect(empty.mainKey() == SessionKey(machineID: "default", sessionID: "main-session"))
    }

    @Test func aMainRouteMoveMigratesOnlyTheConversationBeingViewed() {
        let old = SessionKey(machineID: "default", sessionID: "main")
        let updated = SessionKey(machineID: "route", sessionID: "main")
        let side = SessionKey(machineID: "default", sessionID: "side")
        #expect(AppModel.followingMainRoute(current: old, previous: old, updated: updated, mainID: "main") == updated)
        #expect(AppModel.followingMainRoute(current: side, previous: old, updated: updated, mainID: "main") == side)
        #expect(AppModel.followingMainRoute(current: nil, previous: nil, updated: updated) == nil)

        // A fresh install may have the persisted current key but no cached previous AssistantState.
        #expect(AppModel.followingMainRoute(current: old, previous: nil, updated: updated, mainID: "main") == updated)
        #expect(AppModel.followingMainRoute(current: side, previous: nil, updated: updated, mainID: "main") == side)
        #expect(AppModel.followingMainRoute(current: old, previous: nil, updated: updated, mainID: "other") == old)
        #expect(AppModel.followingMainRoute(current: old, previous: nil, updated: updated, mainID: nil) == old)

        // The persisted current key may be ahead of a throttled snapshot after another confirmed route move.
        let stalePrevious = SessionKey(machineID: "first", sessionID: "main")
        let currentRoute = SessionKey(machineID: "second", sessionID: "main")
        let newestRoute = SessionKey(machineID: "third", sessionID: "main")
        #expect(AppModel.followingMainRoute(current: currentRoute, previous: stalePrevious, updated: newestRoute,
                                            mainID: "main") == newestRoute)
        #expect(AppModel.followingMainRoute(current: currentRoute, previous: stalePrevious, updated: newestRoute,
                                            mainID: nil) == currentRoute)
        #expect(AppModel.followingMainRoute(current: currentRoute, previous: stalePrevious, updated: currentRoute,
                                            mainID: "main") == currentRoute)
    }

    @Test func assistantAndAgentLauncherListsHaveSeparateRequestsAndCaches() async throws {
        let hub = FakeHub()
        let path = "/api/v1/machines/box/launchers"
        hub.on("GET", path) { request in
            let launcher = request.query["purpose"] == "assistant"
                ? #"{"name":"team","model":"shell-model","source":"path"}"#
                : #"{"name":"team","model":"runtime-model","source":"paseo"}"#
            return Data("{\"launchers\":[\(launcher)]}".utf8)
        }
        let model = AppModel()
        model.apiForTests = hub.client
        let machine = MachineRef(id: "box")
        #expect(try await model.launchers(machine).first?.model == "runtime-model")
        #expect(try await model.assistantLaunchers(machine).launchers.first?.model == "shell-model")
        #expect(try await model.launchers(machine).first?.model == "runtime-model")
        #expect(try await model.assistantLaunchers(machine).launchers.first?.model == "shell-model")
        #expect(hub.sent("GET", path).map(\.query) == [[:], ["purpose": "assistant"]])
    }

    @Test func supervisorLauncherPurposeHasItsOwnCacheEntry() async throws {
        let hub = FakeHub()
        let path = "/api/v1/machines/box/launchers"
        hub.on("GET", path) { request in
            let name = request.query["purpose"] == "assistant.supervise" ? "supervisor-model" : "main-model"
            return Data("{\"launchers\":[{\"name\":\"team\",\"model\":\"\(name)\"}]}".utf8)
        }
        let model = AppModel()
        model.apiForTests = hub.client
        let machine = MachineRef(id: "box")

        #expect(try await model.assistantLaunchers(machine).launchers.first?.model == "main-model")
        #expect(try await model.assistantLaunchers(machine, purpose: "assistant.supervise").launchers.first?.model == "supervisor-model")
        #expect(try await model.assistantLaunchers(machine).launchers.first?.model == "main-model")
        #expect(model.cachedAssistantLaunchers(machine, purpose: "assistant.supervise")?.launchers.first?.model == "supervisor-model")
        #expect(hub.sent("GET", path).map(\.query) == [["purpose": "assistant"], ["purpose": "assistant.supervise"]])
    }

    @Test func anExplicitAssistantCapabilityWinsOverAgentKind() {
        #expect(!Launcher(name: "runtime-codex", agent: "codex", assistant: false).runsAssistant)
        #expect(!Launcher(name: "runtime-claude", agent: "claude", assistant: false).runsAssistant)
        #expect(Launcher(name: "runtime-other", agent: "other", assistant: true, source: "runtime").runsAssistant)
    }

    @Test func olderHubEligibilityKeepsShellLaunchersAndRejectsRuntimeOnlyProviders() {
        #expect(Launcher(name: "claude").runsAssistant)
        #expect(Launcher(name: "shell-codex", agent: "codex", source: "path").runsAssistant)
        #expect(Launcher(name: "shell-claude", agent: "claude", source: "zshrc").runsAssistant)
        #expect(Launcher(name: "managed-claude", agent: "claude", source: "persona").runsAssistant)
        // Managed Codex runs the assistant as the hub and the web have it since #12569.
        #expect(Launcher(name: "managed-codex", agent: "codex", source: "persona").runsAssistant)
        #expect(Launcher(name: "managed-codex", agent: "codex", source: LegacyNames.product).runsAssistant)
        // The hub's configured kind wins over the node's agent, as the web's `agent_type || agent`.
        #expect(!Launcher(name: "provider", agent: "claude", agentType: "unknown", source: "persona").runsAssistant)
        #expect(Launcher(name: "provider", agentType: "codex", source: "persona").runsAssistant)
        #expect(!Launcher(name: "agy", agent: "agy", source: "zshrc").runsAssistant)
        #expect(Launcher(name: "agy", agent: "agy", assistant: true, source: "agy").runsAssistant)
        #expect(!Launcher(name: "runtime-codex", agent: "codex", source: "paseo").runsAssistant)
        #expect(!Launcher(name: "runtime-claude", agent: "claude", source: "paseo").runsAssistant)
    }

    @Test func assistantModelRequestsKeepTheirPurposeAndRefresh() async throws {
        let hub = FakeHub()
        let path = "/api/v1/machines/box/launchers/team/models"
        hub.on("GET", path, json: #"{"agent":"claude","models":[{"id":"shell-model","name":"Shell"}]}"#)
        let _: LauncherModels = try await hub.client.launcherModels(machine: "box", launcher: "team", refresh: true, purpose: "assistant")
        let _: [AgentModelOption] = try await hub.client.launcherModels(machine: "box", launcher: "team", purpose: "assistant")
        let _: LauncherModels = try await hub.client.launcherModels(machine: "box", launcher: "team")
        #expect(hub.sent("GET", path).map(\.query) == [
            ["purpose": "assistant", "refresh": "1"], ["purpose": "assistant"], [:],
        ])
    }

    @Test func conversationModelBodyEncodesMachineIdSnakeCase() throws {
        let body = APIClient.ConversationModelBody(launcher: "claude", model: "opus", effort: "high", machineID: "box")
        let data = try JSON.encoder.encode(body)
        let raw = try JSONDecoder().decode([String: String].self, from: data)
        #expect(raw == ["launcher": "claude", "model": "opus", "effort": "high", "machine_id": "box"])
    }

    @Test func starterOffersTheSelectedLaunchersDefaultModelEffortsOnly() {
        let response = response(launchers: [Launcher(name: "fast", model: "fast-model")])
        let list = LauncherModels(agent: "runtime", models: [
            AgentModel(id: "fast-model", name: "Fast", isDefault: true, efforts: ["low", "high", "xhigh"]),
            AgentModel(id: "other-model", name: "Other", efforts: ["max", "ultra"]),
        ], locked: nil)
        let options = AssistantStarterEffort.options(launcher: "fast", response: response, list: list)
        #expect(options.map(\.id) == ["low", "high", "xhigh"])
        #expect(AssistantStarterEffort.retained("max", options: options) == "")
        #expect(AssistantStarterEffort.retained("xhigh", options: options) == "xhigh")
        #expect(AssistantStarterEffort.retained("", options: options) == "")
    }

    @Test func starterUsesTheConfiguredMainModelInsteadOfTheLaunchersOtherDefault() {
        let response = response(launchers: [Launcher(name: "team", model: "fast-model")],
                                default: def(launcher: "team", model: "strong-model"))
        let list = LauncherModels(agent: "codex", models: [
            AgentModel(id: "fast-model", name: "Fast", isDefault: true, efforts: ["low", "high"]),
            AgentModel(id: "strong-model", name: "Strong", efforts: ["high", "ultra"]),
        ], locked: nil)
        #expect(AssistantStarterEffort.options(launcher: "team", response: response, list: list).map(\.id) == ["high", "ultra"])
    }

    @Test func starterNeverGuessesEffortsForAnUnknownOrUnlistedModel() {
        let response = response(launchers: [Launcher(name: "team", model: "unlisted")])
        let list = LauncherModels(agent: "claude", models: [AgentModel(id: "other", name: "Other", isDefault: true, efforts: ["max"])], locked: nil)
        #expect(AssistantStarterEffort.options(launcher: "team", response: response, list: list).isEmpty)
        #expect(AssistantStarterEffort.options(launcher: "team", response: response, list: nil).isEmpty)
        let none = LauncherModels(agent: "runtime", models: [AgentModel(id: "unlisted", name: "No reasoning")], locked: nil)
        #expect(AssistantStarterEffort.options(launcher: "team", response: response, list: none).isEmpty)
    }

    @Test func starterLoadsCapabilitiesWithoutTreatingAnUnreadLauncherAsUnsupported() async {
        let hub = FakeHub()
        let launchers = response(launchers: [Launcher(name: "strong", model: "strong-model"), Launcher(name: "fast", model: "fast-model")])
        hub.on("GET", "/api/v1/machines/box/launchers/strong/models", json: #"{"agent":"codex","models":[{"id":"strong-model","name":"Strong","default":true,"efforts":["high","max"]}]}"#)
        hub.on("GET", "/api/v1/machines/box/launchers/fast/models", json: #"{"agent":"runtime","models":[{"id":"fast-model","name":"Fast","default":true,"efforts":["high","xhigh"]}]}"#)
        let model = AssistantStarterModels(deps: .init(launchers: { _, _, _ in launchers }, api: { _ in hub.client }, cache: AssistantStarterCache()))
        let box = MachineRef(id: "box")
        let strong = AssistantStarterModels.source(box, launcher: "strong", fallback: nil)
        let fast = AssistantStarterModels.source(box, launcher: "fast", fallback: nil)
        await model.load(box, launcher: "strong", fallback: nil)
        #expect(model.retained("max", source: strong) == "max")
        #expect(model.retained("max", source: fast) == "max") // Unknown capabilities preserve the saved choice.
        #expect(model.options(for: fast).isEmpty)
        await model.load(box, launcher: "fast", fallback: nil)
        #expect(model.options(for: fast).map(\.id) == ["high", "xhigh"])
        #expect(model.retained("max", source: fast) == "")
        #expect(model.retained("xhigh", source: fast) == "xhigh")
        #expect(hub.sent("GET", "/api/v1/machines/box/launchers/fast/models").first?.query == ["purpose": "assistant"])
    }
    @Test func mainCatalogChangesOnlyItsDefaultAndEffort() {
        let shared = response(launchers: [Launcher(name: "claude")],
                              default: def(launcher: "claude", model: "shared", effort: "low"))
        let own = response(launchers: [Launcher(name: "not-an-eligible-launcher")],
                           default: def(launcher: "claude", model: "main", effort: "high"))
        let merged = AssistantModelChoice.mainCatalog(shared: shared, own: own)
        #expect(merged.launchers.map(\.name) == ["claude"])
        #expect(shared.default?.model == "shared")
        let effective = AssistantModelChoice.effective(launcher: "claude", model: "running", effort: "low", pick: nil, response: merged)
        #expect(effective.model == "main" && effective.effort == "high")
        let picked = AssistantModelChoice.effective(launcher: "claude", model: "running", effort: "low",
            pick: ModelPick(launcher: "claude", model: "picked", effort: "medium"), response: merged)
        #expect(picked.model == "picked" && picked.effort == "medium")
        let legacy = AssistantModelChoice.mainCatalog(shared: shared, own: response())
        #expect(legacy.default?.model == "shared")
    }

    @Test func mainCatalogCacheCannotReplaceNewConversationDefaults() {
        let shared = AppModel.AssistantLauncherScope(machine: MachineRef(id: "box"), manager: "assistant")
        let main = AppModel.AssistantLauncherScope(machine: MachineRef(id: "box"), manager: "assistant", main: true)
        #expect(shared != main)
        #expect(Set([shared, main]).count == 2)
    }

    @Test func mainCatalogTransportUsesTheIndependentPurpose() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/box/launchers", json: #"{"launchers":[],"default":{"provider":"launcher.claude","model":"main-model","effort":"high"}}"#)
        let result = try await hub.client.mainAssistantLaunchers(machine: "box")
        #expect(result.default?.model == "main-model")
        #expect(hub.sent("GET", "/api/v1/machines/box/launchers").first?.query == ["purpose": "assistant.main"])
    }

}
