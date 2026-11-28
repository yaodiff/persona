import SwiftUI

/// Pure decisions behind an Assistant conversation's model sheet (#6542; the hub's per-conversation pick is #5033): what the next
/// message runs on, where that comes from, and the pick a row keeps. No UI types, so the logic is unit-tested on its own.
nonisolated enum AssistantModelChoice {
    /// Where the setup the next message runs on comes from.
    enum Source: Equatable {
        /// The conversation's own pick (`model_pick`).
        case pick
        /// The effective value of the model purpose this conversation follows.
        case defaultValue
        /// Another Persona conversation keeps the setup it started with.
        case own
        /// Only what the running process last reported, while the picker has not answered.
        case running
    }

    /// Which setup a conversation with no saved pick runs on. Main chat, leaders and task conversations follow Main
    /// chat; the supervisor has its own model purpose; every other Persona conversation keeps its own setup.
    enum DefaultRule: Equatable { case main, own, supervise }

    /// What the conversation's next message runs on.
    struct Effective: Equatable {
        var launcher: String
        /// Nil: the launcher's own default model.
        var model: String?
        var effort: String?
        var source: Source
        var picked: Bool { source == .pick }
    }

    /// The setup the next message runs on, as the hub decides it (the web's `assistantSetup`). A pick wins; with none,
    /// the conversation's default rule decides between Main chat, `assistant.supervise`, and what a side chat started
    /// with. Before the picker answers, the running session is all there is.
    static func effective(session: Session?, response: APIClient.LaunchersResponse?,
                          rule: DefaultRule = .main, superviseResponse: APIClient.LaunchersResponse? = nil) -> Effective {
        effective(launcher: session?.launcher, model: session?.model, effort: session?.effort,
                  pick: session?.modelPick, response: response, rule: rule, superviseResponse: superviseResponse)
    }

    /// Core resolution on plain fields, so the rule is unit-tested without building a whole `Session`.
    static func effective(launcher: String?, model: String?, effort: String?, pick: ModelPick?,
                          response: APIClient.LaunchersResponse?, rule: DefaultRule = .main,
                          superviseResponse: APIClient.LaunchersResponse? = nil) -> Effective {
        if let pick, !pick.launcher.isEmpty {
            return Effective(launcher: pick.launcher, model: pick.model?.emptyAsNil,
                             effort: pick.effort?.emptyAsNil ?? effort, source: .pick)
        }
        if rule == .own {
            // A side conversation does not silently change when Main chat's setting changes. Its session's launcher
            // and model remain authoritative, including before or without a picker answer.
            return Effective(launcher: launcher?.emptyAsNil ?? "claude", model: model?.emptyAsNil,
                             effort: effort?.emptyAsNil, source: .own)
        }
        let def: LauncherDefault?
        switch rule {
        case .main: def = response?.default
        case .supervise:
            // Nil means the separate purpose has not answered yet, so Main chat remains the temporary inherited value.
            // An answered purpose with no default falls back to the session, never to an unrelated Main-chat model.
            def = superviseResponse.map(\.default) ?? (superviseResponse == nil ? response?.default : nil)
        case .own: def = nil // returned above
        }
        let provider = def?.launcher?.emptyAsNil
            ?? launcher?.emptyAsNil
            ?? "claude"
        // The default's model only names a running one while the session is on the same launcher, the web's rule too.
        let defaultModel = def?.model?.emptyAsNil
            ?? (launcher == provider ? model : nil)
        return Effective(launcher: provider, model: defaultModel,
                         effort: def?.effort?.emptyAsNil ?? effort,
                         source: response == nil ? .running : .defaultValue)
    }

    /// Which way back the model-route remedy names: these conversations follow a default; a side conversation undoes its pick.
    static func followsDefault(isMain: Bool, supervisor: Bool, leader: Bool, origin: String?) -> Bool {
        isMain || supervisor || leader || origin == "task"
    }

    static func defaultRule(isMain: Bool, supervisor: Bool, leader: Bool, origin: String?) -> DefaultRule {
        if supervisor { return .supervise }
        return followsDefault(isMain: isMain, supervisor: false, leader: leader, origin: origin) ? .main : .own
    }

    /// Every Assistant conversation may choose its own model on a hub that keeps picks; main-chat identity is irrelevant.
    static func pickerAvailable(assistant: Bool, keepsPick: Bool) -> Bool { assistant && keepsPick }

    /// The shared catalog owns eligibility; only the main-purpose default is overlaid.
    static func mainCatalog(shared: APIClient.LaunchersResponse, own: APIClient.LaunchersResponse) -> APIClient.LaunchersResponse {
        var result = shared
        result.default = own.default ?? shared.default
        return result
    }

    /// The model id ticked for `launcher` in the sheet: nil when the setup runs on another launcher, "" on its Default
    /// row, else the picked model's id. A hub-resolved model also ticks its alias row.
    static func selectedModel(_ e: Effective, launcher: String, aliases: [String: String] = [:],
                              configuredModel: String? = nil) -> String? {
        guard e.launcher == launcher else { return nil }
        let model = e.model?.emptyAsNil
        guard let model else { return configuredModel?.emptyAsNil ?? "" }
        return aliases[model.lowercased()] ?? model
    }

    /// Resolved-ID → row-ID lookup, built once when a catalog lands rather than searched in a view's body.
    static func selectionAliases(_ list: LauncherModels?) -> [String: String] {
        var out: [String: String] = [:]
        for model in list?.models ?? [] {
            let row = model.isDefault ? "" : model.id
            let key = model.id.lowercased()
            if out[key] == nil || !model.isDefault { out[key] = row }
            if let resolved = model.resolved {
                // Default and its concrete alias often resolve to the same ID: the concrete alias wins, as on the web.
                let resolvedKey = resolved.lowercased()
                if out[resolvedKey] == nil || !model.isDefault { out[resolvedKey] = row }
            }
        }
        // Folded spellings select their containing row too. IDs and resolved values win over aliases, including a
        // synthetic collision where an early alias is also a later row's own ID; aliases never become new rows.
        for model in list?.models ?? [] {
            let row = model.isDefault ? "" : model.id
            for alias in model.aliases ?? [] {
                let key = alias.lowercased()
                if out[key] == nil { out[key] = row }
            }
        }
        return out
    }

    /// The engine the existing conversation actually runs on, including Codex threads identified by their source.
    static func sessionEngine(_ session: Session?) -> String? {
        guard let session else { return nil }
        if session.isCodex { return "codex" }
        return providerAgent(session.agent, provider: session.launcher ?? "")
    }

    /// Matches Web's explicit-switch rule: another runtime, another runtime instance, or a different connection
    /// identity needs staging even when both sides happen to run the same engine kind.
    static func switchNeeded(session: Session, target: Launcher) -> Bool {
        if session.launcher == target.name { return false }
        if agentID(sessionEngine(session)) != agentID(providerAgent(target.runtimeAgent, provider: target.name)) { return true }
        let extensionPrefix = "persona_ext_"
        if session.launcher?.hasPrefix(extensionPrefix) == true || target.name.hasPrefix(extensionPrefix) { return true }
        let runtimeTarget = !["persona", LegacyNames.product].contains(target.source ?? "")
            && !["", "zshrc", "path", "builtin"].contains(target.source ?? "")
        if session.sourceKind == "runtime" {
            return !runtimeTarget || session.sourceInstanceID != target.source
        }
        return runtimeTarget
    }

    /// Web's descriptive picker eligibility; the hub re-attests it before executing the switch. Native Claude Code and
    /// Codex keep their existing behavior. Every other runtime needs both Assistant and explicit supervision support.
    static func launcherSupervisionPolicy(_ target: Launcher) -> Bool {
        guard !target.name.isEmpty, !target.name.hasPrefix("persona_ext_") else { return false }
        let source = target.source ?? ""
        let native = source.isEmpty || ["zshrc", "path", "builtin", "persona", LegacyNames.product].contains(source)
        let agent = target.runtimeAgent ?? ""
        if native { return agent.isEmpty || agent == "claude" || agent == "codex" }
        return target.assistant == true && target.supervisionPolicy == true
    }

    private static func providerAgent(_ raw: String?, provider: String) -> String? {
        raw?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() == "paseo" && !provider.isEmpty ? provider : raw
    }

    /// The same runtime aliases Web folds before comparing two engines. Unknown runtime IDs remain distinct values.
    private static func agentID(_ raw: String?) -> String {
        let id = raw?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() ?? ""
        let key = id.filter { !$0.isWhitespace && $0 != "." && $0 != "_" && $0 != "-" }
        let aliases = [
            "claudecode": "claude", "codexcli": "codex", "geminicli": "gemini",
            "cursoragent": "cursor", "piweb": "pi", "traecli": "trae",
        ]
        return aliases[key] ?? (id.isEmpty ? "claude" : id)
    }

    /// An existing Claude/Codex conversation keeps its engine, matching the hub and web. Unknown runtimes stay
    /// selectable because refusing them would be a guess; the hub remains authoritative.
    static func isOtherEngine(sessionAgent: String?, launcherAgent: String?) -> Bool {
        guard let session = engine(sessionAgent), let launcher = engine(launcherAgent, nilIsClaude: true) else { return false }
        return session != launcher
    }

    /// A locked launcher keeps only its default entry, as on the web. Its lock explains why concrete model IDs are absent;
    /// choosing no model remains valid and the hub accepts it.
    static func visibleModels(_ list: LauncherModels?) -> [AgentModel] {
        (list?.models ?? []).filter { list?.locked == nil || $0.isDefault }
    }

    /// The configured-model row for an empty catalog. A locked launcher's configured model is sent as Default (nil),
    /// because the hub accepts the launcher's own fixed model only without an overriding model ID.
    struct ConfiguredFallback: Equatable {
        var id: String
        var sendsDefault: Bool
    }

    /// The configured-model row for a launcher with no readable catalog or no models of its own.
    static func configuredFallback(_ launcher: Launcher, models: [AgentModel], locked: String?) -> ConfiguredFallback? {
        guard models.isEmpty, let id = launcher.model?.emptyAsNil, id != "默认" else { return nil } // i18n-ignore: legacy wire value, never displayed
        return ConfiguredFallback(id: id, sendsDefault: locked != nil)
    }

    /// The user-facing destination in the cross-engine warning, normalized from `codex-cli` / case variants.
    static func otherEngineName(_ runtimeAgent: String?) -> String {
        engine(runtimeAgent, nilIsClaude: true) == "codex" ? "Codex" : "Claude Code"
    }

    private static func engine(_ raw: String?, nilIsClaude: Bool = false) -> String? {
        guard let normalized = raw?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() else {
            return nilIsClaude ? "claude" : nil
        }
        switch normalized {
        case "", "claude", "claude-code", "claudecode": return "claude"
        case "codex", "codex-cli": return "codex"
        default: return nil
        }
    }

    /// The pick a row keeps: nil for the setup already in force (the hub would store a no-op). A Default row sends no
    /// model; `effort` carries the setup's current effort so the pick keeps it. Resolved aliases and a configured
    /// fallback are compared by what runs, not just by their spelling, so tapping the current row never pins a default.
    static func pick(launcher: String, model: String?, effort: String?, current: Effective,
                     aliases: [String: String] = [:], configuredModel: String? = nil,
                     configuredIsDefault: Bool = false) -> ModelPick? {
        let sentModel = model?.emptyAsNil
        if current.launcher == launcher {
            let selected = selectedModel(current, launcher: launcher, aliases: aliases, configuredModel: configuredModel)
            if selected == (sentModel ?? "") { return nil }
            // No catalog: the configured model is what this launcher's default already runs.
            if let sentModel, current.model?.emptyAsNil == nil, sentModel == configuredModel?.emptyAsNil { return nil }
            // A locked configured row is the same Default choice, not an override with a concrete ID.
            if configuredIsDefault, sentModel == nil, selected == configuredModel?.emptyAsNil { return nil }
        }
        return ModelPick(launcher: launcher, model: sentModel, effort: effort?.emptyAsNil)
    }

    /// The short name the composer button shows for the effective setup: the picked/configured model, else the
    /// launcher's own model, else just the launcher.
    static func displayName(_ e: Effective, response: APIClient.LaunchersResponse?) -> String? {
        let matched = response?.launchers.first { $0.name == e.launcher }
        let id = e.model?.emptyAsNil ?? matched?.model?.emptyAsNil
        return id.flatMap { SessionModes.modelName($0) } ?? matched?.title ?? Launcher.builtinTitle(name: e.launcher)
    }
}

private extension String {
    // Read by the nonisolated `AssistantModelChoice`; the app target's default would make it main-actor only.
    nonisolated var emptyAsNil: String? { isEmpty ? nil : self }
}

/// An Assistant conversation's model sheet model (#6542): the assistant computer's picker (`?purpose=assistant`), each launcher's
/// `/model` list, and the picks kept on the hub (#5033).
@Observable
final class AssistantModelPanelModel {
    struct Deps {
        var launchers: (_ machine: MachineRef, _ refresh: Bool) async throws -> APIClient.LaunchersResponse
        var supervise: (_ machine: MachineRef, _ refresh: Bool) async throws -> APIClient.LaunchersResponse = { _, _ in
            APIClient.LaunchersResponse(launchers: [], default: nil, unavailable: nil)
        }
        var models: (_ machine: MachineRef, _ launcher: String, _ refresh: Bool) async throws -> LauncherModels
        var keep: (_ key: SessionKey, _ pick: ModelPick) async -> Bool
        var drop: (_ key: SessionKey) async -> Bool
        var canSwitch: (_ key: SessionKey) -> Bool = { _ in false }
        var segments: (_ key: SessionKey) async throws -> APIClient.ConversationSwitchState = { _ in
            throw APIError(status: 409, message: String(localized: "Switching connections is unavailable. Refresh the conversation and try again."))
        }
        var switchConversation: (_ key: SessionKey, _ request: APIClient.ConversationSwitchRequest) async throws -> Void = { _, _ in
            throw APIError(status: 409, message: String(localized: "Switching connections is unavailable. Refresh the conversation and try again."))
        }
        var refreshAssistant: () async -> Void = {}
        static var live: Deps {
            Deps(
                launchers: { try await AppModel.shared.assistantLaunchers($0, refresh: $1, main: true) },
                supervise: { try await AppModel.shared.assistantLaunchers($0, refresh: $1, purpose: "assistant.supervise") },
                models: { try await AppModel.shared.api($0.group)?.launcherModels(machine: $0.id, launcher: $1, refresh: $2, purpose: "assistant")
                    ?? LauncherModels(agent: "", models: [], locked: nil, machine: nil) },
                keep: { await AppModel.shared.keepConversationModel($0, pick: $1) },
                drop: { await AppModel.shared.dropConversationModel($0) },
                canSwitch: { AppModel.shared.features($0.group).contains("assistant_segments") },
                segments: { key in
                    guard let client = AppModel.shared.api(key.group) else {
                        throw APIError(status: 503, message: String(localized: "Can't reach the hub"))
                    }
                    return try await client.conversationSwitchState(machine: key.machineID, conversation: key.sessionID)
                },
                switchConversation: { key, request in
                    guard let client = AppModel.shared.api(key.group) else {
                        throw APIError(status: 503, message: String(localized: "Can't reach the hub"))
                    }
                    try await client.switchConversation(machine: key.machineID, conversation: key.sessionID, request: request)
                },
                refreshAssistant: { await AppModel.shared.refreshAssistant() })
        }
    }

    let key: SessionKey
    let machine: MachineRef
    let defaultRule: AssistantModelChoice.DefaultRule
    private(set) var response: APIClient.LaunchersResponse?
    private(set) var superviseResponse: APIClient.LaunchersResponse?
    private(set) var offeredLauncherNames: Set<String> = []
    private(set) var unavailableLauncherNames: Set<String> = []
    /// Each launcher's `/model` list, by launcher name.
    private(set) var lists: [String: LauncherModels] = [:]
    /// Rows the sheet may offer and resolved-model → row-ID aliases, derived once per answer (P4: not in `body`).
    private(set) var visibleLists: [String: [AgentModel]] = [:]
    private(set) var resolvedAliases: [String: [String: String]] = [:]
    /// Why one launcher's list could not be read (a 404 from an older hub/node leaves it empty, not an error).
    private(set) var listErrors: [String: String] = [:]
    private(set) var loading = false
    private(set) var loadError: String?
    /// The launcher a pick is in flight for, so its rows can dim; nil when idle.
    private(set) var busyLauncher: String?
    private(set) var canSwitch = false
    private(set) var switchPick: ModelPick?
    private(set) var switchDestination = ""
    private(set) var switchError: String?
    private(set) var switchUncertain = false
    private(set) var switching = false
    @ObservationIgnored private var preparedSwitch: APIClient.ConversationSwitchRequest?
    @ObservationIgnored private let deps: Deps

    init(key: SessionKey, machine: MachineRef, defaultRule: AssistantModelChoice.DefaultRule = .main, deps: Deps = .live) {
        self.key = key
        self.machine = machine
        self.defaultRule = defaultRule
        self.deps = deps
        canSwitch = deps.canSwitch(key)
    }

    func list(_ launcher: String) -> LauncherModels? { lists[launcher] }
    func models(_ launcher: String) -> [AgentModel] { visibleLists[launcher] ?? [] }
    func selectionAliases(_ launcher: String) -> [String: String] { resolvedAliases[launcher] ?? [:] }
    /// A current provider absent from both lists needs its own explanation and recovery row.
    func isMissing(_ launcher: String) -> Bool {
        response != nil && !offeredLauncherNames.contains(launcher) && !unavailableLauncherNames.contains(launcher)
    }
    func error(_ launcher: String) -> String? { listErrors[launcher] }
    var unavailable: [UnavailableLauncher] { response?.unavailable ?? [] }

    /// Reads the picker once, then each launcher's model list in turn (the node serves a cached list at once, so this
    /// stays quick); each answer lands as it arrives.
    func load(refresh: Bool = false) async {
        guard !loading else { return }
        canSwitch = deps.canSwitch(key)
        loading = true
        loadError = nil
        defer { loading = false }
        do {
            let r = try await deps.launchers(machine, refresh)
            response = r
            offeredLauncherNames = Set(r.launchers.map(\.name))
            unavailableLauncherNames = Set((r.unavailable ?? []).map(\.name))
            if defaultRule == .supervise {
                superviseResponse = try await deps.supervise(machine, refresh)
            }
        } catch {
            guard !error.isCancellation else { return }
            loadError = error.shown
            return
        }
        for launcher in response?.launchers ?? [] {
            await readList(launcher, refresh: refresh)
        }
    }

    private func readList(_ launcher: Launcher, refresh: Bool = false) async {
        let name = launcher.name
        do {
            let answer = try await deps.models(machine, name, refresh)
            lists[name] = answer
            visibleLists[name] = AssistantModelChoice.visibleModels(answer)
            resolvedAliases[name] = AssistantModelChoice.selectionAliases(answer)
            listErrors[name] = nil
        } catch {
            if let e = error as? APIError, e.status == 404 {
                // Older hub/node: no catalog, but the launcher's configured model is still a concrete choice.
                lists[name] = LauncherModels(agent: launcher.runtimeAgent ?? "claude",
                                             models: [], locked: nil, machine: nil)
                visibleLists[name] = []
                resolvedAliases[name] = [:]
                listErrors[name] = nil
                return
            }
            guard !error.isCancellation else { return }
            // Keep the last good rows selectable; the section shows the refresh failure and Retry beside them.
            listErrors[name] = error.shown
        }
    }

    /// Keeps `model` (nil: its default) for `launcher`; returns whether the hub took it.
    func choose(launcher: String, model: String?, session: Session?) async -> Bool {
        let current = AssistantModelChoice.effective(session: session, response: response, rule: defaultRule,
                                                     superviseResponse: superviseResponse)
        let launcherInfo = response?.launchers.first { $0.name == launcher }
        let fallback = launcherInfo.flatMap {
            AssistantModelChoice.configuredFallback($0, models: models(launcher), locked: list(launcher)?.locked)
        }
        guard let pick = AssistantModelChoice.pick(launcher: launcher, model: model, effort: current.effort, current: current,
                                                   aliases: selectionAliases(launcher), configuredModel: fallback?.id,
                                                   configuredIsDefault: fallback?.sendsDefault == true) else { return true }
        if let session, canSwitch, let launcherInfo, AssistantModelChoice.switchNeeded(session: session, target: launcherInfo) {
            switchPick = pick
            switchDestination = launcherInfo.title
            preparedSwitch = nil
            switchError = nil
            switchUncertain = false
            return false // Confirmation, not a model pick, owns the explicit operation.
        }
        busyLauncher = launcher
        defer { busyLauncher = nil }
        return await deps.keep(key, pick)
    }

    func cancelSwitch() {
        guard !switching else { return }
        switchPick = nil
        switchDestination = ""
        preparedSwitch = nil
        switchError = nil
        switchUncertain = false
    }

    /// Prepares without sending input, then submits one explicit operation. An uncertain retry reuses its exact body.
    func confirmSwitch(session: Session?) async -> Bool {
        guard let session, let pick = switchPick, !switching else { return false }
        switching = true
        busyLauncher = pick.launcher
        switchError = nil
        var submitted = false
        defer {
            switching = false
            busyLauncher = nil
        }
        do {
            if preparedSwitch == nil {
                let state = try await deps.segments(key)
                guard state.enabled, state.epoch >= 1 else {
                    throw APIError(status: 409, message: String(localized: "Switching connections is unavailable. Refresh the conversation and try again."))
                }
                if state.supervisionRequired == true {
                    let latest = try await deps.launchers(machine, true)
                    guard let target = latest.launchers.first(where: { $0.name == pick.launcher }),
                          AssistantModelChoice.launcherSupervisionPolicy(target) else {
                        throw APIError(status: 409, message: String(localized: "This connection cannot receive separate task follow-up instructions."))
                    }
                }
                preparedSwitch = APIClient.ConversationSwitchRequest(
                    operationID: UUID().uuidString.lowercased(), expectedEpoch: state.epoch,
                    sourceMachineID: session.machineID, machineID: session.machineID,
                    launcher: pick.launcher, model: pick.model, effort: pick.effort,
                    permissionMode: session.permissionMode)
            }
            guard let request = preparedSwitch else { return false }
            submitted = true
            try await deps.switchConversation(key, request)
            await deps.refreshAssistant()
            switchPick = nil
            switchDestination = ""
            preparedSwitch = nil
            switchUncertain = false
            Haptics.success()
            return true
        } catch {
            let uncertain = submitted && (error as? APIError)?.fields["delivered"] != "none"
            let problem = error.shown ?? error.localizedDescription
            switchUncertain = uncertain
            switchError = uncertain
                ? String(localized: "The switch result is unconfirmed. Retrying uses the same request; it will not start another switch. \(problem)")
                : problem
            return false
        }
    }

    /// The conversation follows Model configuration › Main chat again.
    func followDefault() async -> Bool {
        busyLauncher = ""
        defer { busyLauncher = nil }
        return await deps.drop(key)
    }
}

/// The sheet opened from an Assistant composer's model button (#6542): every launcher the assistant can use on its
/// machine, each one's models, the ones that can't run here with the hub's reason, and the way back to the default.
/// A pick is kept for this conversation only (#5033) and holds from the next message.
struct AssistantModelSheet: View {
    @Environment(AppModel.self) private var model
    let session: Session?
    var panel: AssistantModelPanelModel
    @Environment(\.dismiss) private var dismiss
    @State private var typingFor: String?

    private var effective: AssistantModelChoice.Effective {
        AssistantModelChoice.effective(session: session, response: panel.response, rule: panel.defaultRule,
                                       superviseResponse: panel.superviseResponse)
    }

    private var backTitle: String {
        String(localized: panel.defaultRule == .own ? "Undo pick" : "Follow default")
    }

    private var backFooter: String {
        switch panel.defaultRule {
        case .own:
            String(localized: "This conversation has its own model. Undoing the pick makes its next messages use the setup it started with again.")
        case .supervise:
            String(localized: "This conversation has its own model. Following the default makes its next messages use Model configuration › Background supervisor again.")
        case .main:
            String(localized: "This conversation has its own model. Following the default makes its next messages use Model configuration › Main chat again.")
        }
    }

    private var missingRecovery: String {
        switch panel.defaultRule {
        case .own:
            String(localized: "This conversation's launcher is no longer available. Undo the pick or choose another launcher.")
        case .supervise:
            String(localized: "This conversation's launcher is no longer available. Follow the Background supervisor default or choose another launcher.")
        case .main:
            String(localized: "This conversation's launcher is no longer available. Follow the Main Chat default or choose another launcher.")
        }
    }

    var body: some View {
        NavigationStack {
            List {
                if let problem = panel.loadError {
                    Section {
                        Label(problem, systemImage: Theme.Icon.error)
                            .font(Theme.Font.secondary).foregroundStyle(Theme.cautionText)
                        Button { Task { await panel.load(refresh: true) } } label: {
                            Label(String(localized: "Retry"), systemImage: Theme.Icon.error)
                        }
                    }
                }
                if effective.picked {
                    Section {
                        Button(role: .destructive) {
                            Task {
                                if await panel.followDefault() { dismiss() }
                            }
                        } label: {
                            Label(backTitle, systemImage: Theme.Icon.clearContext)
                                .lineLimit(1)
                        }
                        .disabled(panel.busyLauncher != nil)
                    } footer: {
                        Text(backFooter)
                    }
                }
                if panel.isMissing(effective.launcher) {
                    Section {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(effective.launcher).font(Theme.Font.heading).foregroundStyle(.secondary)
                            if let model = effective.model {
                                Text(SessionModes.modelName(model) ?? model)
                                    .font(Theme.Font.label).foregroundStyle(.secondary)
                            }
                            Text(missingRecovery)
                                .font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                        .accessibilityIdentifier("assistantModel.missing.\(effective.launcher)")
                    }
                }
                ForEach(panel.response?.launchers ?? []) { launcher in
                    section(launcher)
                }
                if !panel.unavailable.isEmpty {
                    Section(String(localized: "Unavailable here")) {
                        ForEach(panel.unavailable) { item in
                            UnavailableLauncherRow(item: item)
                                .accessibilityIdentifier("assistantModel.unavailable.\(item.name)")
                        }
                    }
                }
            }
            .themedPage()
            .navigationTitle(String(localized: "Model [Assistant View c98e11]"))
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                SheetClose { dismiss() }
            }
            .refreshable { await panel.load(refresh: true) }
            .task { await panel.load() }
            .sheet(item: Binding(get: { typingFor.map { TypingTarget(launcher: $0) } }, set: { typingFor = $0?.launcher })) { target in
                TypedModelSheet(machine: panel.machine, launcher: target.launcher, purpose: "assistant",
                                note: String(localized: "Used for this conversation's next messages, on every device.")) { id in
                    Task {
                        if await panel.choose(launcher: target.launcher, model: id, session: session) { dismiss() }
                    }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .accessibleConfirmationDialog("Switch this conversation?", isPresented: Binding(
            get: { panel.switchPick != nil },
            set: { if !$0 { panel.cancelSwitch() } }
        ), titleVisibility: .visible, hasCancel: true) {
            Button {
                Task {
                    if await panel.confirmSwitch(session: session) { dismiss() }
                }
            } label: {
                HStack(spacing: Theme.Space.s) {
                    if panel.switching { ProgressView().controlSize(.small) }
                    Text("Switch connection").lineLimit(1)
                }
            }
            .disabled(panel.switching)
            .accessibilityIdentifier("assistantModel.switch.confirm")
            Button(panel.switchUncertain ? String(localized: "Close") : String(localized: "Cancel"), role: .cancel) {
                panel.cancelSwitch()
            }
            .lineLimit(1)
            .disabled(panel.switching)
            .accessibilityIdentifier("assistantModel.switch.cancel")
        } message: {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text("Continue with \(panel.switchDestination) using recent messages and a summary. The conversation and its tasks stay here. Some details may be summarized. The new connection will take over with your next message.")
                    .accessibilityIdentifier("assistantModel.switch.message")
                if let problem = panel.switchError {
                    Text(problem)
                        .foregroundStyle(Theme.danger)
                        .accessibilityIdentifier("assistantModel.switch.error")
                }
            }
        }
    }

    @ViewBuilder
    private func section(_ launcher: Launcher) -> some View {
        let list = panel.list(launcher.name)
        let locked = list?.locked
        let models = panel.models(launcher.name)
        let configured = AssistantModelChoice.configuredFallback(launcher, models: models, locked: locked)
        let selected = AssistantModelChoice.selectedModel(
            effective, launcher: launcher.name, aliases: panel.selectionAliases(launcher.name), configuredModel: configured?.id)
        let otherEngine = !panel.canSwitch && effective.launcher != launcher.name && AssistantModelChoice.isOtherEngine(
            sessionAgent: AssistantModelChoice.sessionEngine(session), launcherAgent: launcher.runtimeAgent)
        Section {
            if otherEngine {
                let to = AssistantModelChoice.otherEngineName(launcher.runtimeAgent)
                Label(String(localized: "Switching from \(session?.agentLabel ?? "Claude Code") to \(to) needs a new conversation"),
                      systemImage: Theme.Icon.locked)
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .accessibilityIdentifier("assistantModel.otherEngine.\(launcher.name)")
            } else {
                // A real Default row comes from the catalog. Add one only when the current setup has no such row.
                if selected != nil, configured == nil, !models.contains(where: { $0.isDefault }) {
                    row(launcher: launcher.name, modelID: "", title: String(localized: "Default [Prompt Composer 844b8c]"),
                        detail: launcher.model == "默认" ? nil : launcher.model, ticked: selected == "", locked: nil) // i18n-ignore: legacy wire value, never displayed
                }
                if let locked {
                    Label(locked, systemImage: Theme.Icon.locked)
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                        .accessibilityIdentifier("assistantModel.locked.\(launcher.name)")
                }
                if let problem = panel.error(launcher.name), list != nil {
                    HStack(spacing: Theme.Space.s) {
                        Label(problem, systemImage: Theme.Icon.error)
                            .font(Theme.Font.secondary).foregroundStyle(Theme.cautionText)
                        Spacer(minLength: Theme.Space.s)
                        Button(String(localized: "Retry")) { Task { await panel.load(refresh: true) } }
                            .lineLimit(1)
                    }
                }
                if let list {
                    if let configured {
                        // No catalog (a direct API or an older node): its configured model is still a concrete choice.
                        // A locked launcher sends Default (nil), never an overriding concrete model ID.
                        row(launcher: launcher.name, modelID: configured.sendsDefault ? "" : configured.id,
                            title: SessionModes.modelName(configured.id) ?? configured.id,
                            detail: nil, ticked: selected == configured.id || (configured.sendsDefault && selected == ""), locked: nil)
                    } else {
                        ForEach(models) { m in
                            row(launcher: launcher.name, modelID: m.isDefault ? "" : m.id,
                                title: m.displayName, detail: m.detail,
                                ticked: selected == (m.isDefault ? "" : m.id), locked: nil, custom: m.custom)
                        }
                    }
                    if list.takesTypedModel, model.offersTypedModels(panel.key.group) {
                        Button { typingFor = launcher.name } label: {
                            Label(String(localized: "Other Model…"), systemImage: Theme.Icon.keyboard).lineLimit(1)
                        }
                        .accessibilityIdentifier("assistantModel.other.\(launcher.name)")
                    }
                } else if panel.error(launcher.name) != nil {
                    Button { Task { await panel.load(refresh: true) } } label: {
                        Label(String(localized: "Retry"), systemImage: Theme.Icon.error).lineLimit(1)
                    }
                } else {
                    HStack(spacing: Theme.Space.s) {
                        ProgressView().controlSize(.small)
                        Text(String(localized: "Reading models…")).font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                }
            }
        } header: {
            Text(launcher.title)
        }
    }

    private func row(launcher: String, modelID: String, title: String, detail: String?, ticked: Bool,
                     locked: String?, custom: Bool = false) -> some View {
        let second = launcherModelRowDetail(owner: launcher, detail: detail)
        return Button {
            Haptics.tap()
            Task {
                if await panel.choose(launcher: launcher, model: modelID.isEmpty ? nil : modelID, session: session) {
                    dismiss()
                }
            }
        } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(custom ? Theme.Font.heading.monospaced() : Theme.Font.heading)
                        .foregroundStyle(.primary).lineLimit(1)
                    if let second {
                        Text(second).font(Theme.Font.label.monospaced()).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if ticked { ThemedIcon(Theme.Icon.done).foregroundStyle(Theme.accent).fontWeight(.semibold) }
            }
            .contentShape(.rect)
        }
        .disabled(locked != nil || panel.busyLauncher != nil)
        .accessibilityIdentifier("assistantModel.\(launcher).\(modelID.isEmpty ? "default" : modelID)")
    }

    private struct TypingTarget: Identifiable { let launcher: String; var id: String { launcher } }
}
