import SwiftUI

// Assistant Profile › Agent Follow-Up (#7005): the web's Settings › Assistant "Starting tasks" and "Agent follow-up"
// groups (`web/src/views/settings/AssistantSettings.tsx`, `AgentAutoNaming.tsx`) on iPhone and iPad. Every row is the
// hub's value in effect from `GET /api/v1/assistant`; a hub that leaves a field out has no row for it. A change shows at
// once and is one sparse `PUT /api/v1/assistant` naming the assistant it was read from; a refusal puts the hub's value
// back. "Tell supervisors about new messages" (`inbox_signals`) is not here: it moves to the Inbox's own settings (#4524).

/// The page's state and its writes (MVVM), so tests run it against a `FakeHub`.
@Observable @MainActor final class AgentFollowUpModel {
    struct Deps {
        var api: () -> APIClient?
        /// What the app already holds for the current assistant: shown at once, before the page's own read answers.
        var held: () -> APIClient.AssistantState?
        /// The hub's answer to a write, for the rest of the app (the background supervisor's rows read it).
        var saved: (APIClient.AssistantState) -> Void
        var toast: (Toast) -> Void

        @MainActor static var live: Deps {
            let app = AppModel.shared
            let scope = AssistantSettingsRequestScope(app)
            return Deps(api: { scope.api(app) },
                        held: { AssistantSettingsRequestScope(app) == scope ? app.assistantConfig : nil },
                        saved: { state in if AssistantSettingsRequestScope(app) == scope { app.assistantSettingsSaved(state) } },
                        toast: { app.toast = $0 })
        }
    }

    /// One field of the page, as it is written.
    enum Change: Equatable {
        case startRequested(Bool)
        case workspace(String)
        case mainSupervises(Bool)
        case agentTasks(Bool)
        case idleHours(Int)
        case archiveDays(Int)
        case naming(Bool)
        case namingMax(Int)
        case namingInterval(Int)

        enum Field: Hashable { case startRequested, workspace, mainSupervises, agentTasks, idleHours, archiveDays, naming, namingMax, namingInterval }

        var field: Field {
            switch self {
            case .startRequested: .startRequested
            case .workspace: .workspace
            case .mainSupervises: .mainSupervises
            case .agentTasks: .agentTasks
            case .idleHours: .idleHours
            case .archiveDays: .archiveDays
            case .naming: .naming
            case .namingMax: .namingMax
            case .namingInterval: .namingInterval
            }
        }

        /// The request: this field alone.
        var body: APIClient.AssistantConfigBody {
            var b = APIClient.AssistantConfigBody()
            switch self {
            case .startRequested(let on): b.startRequestedTasks = on
            case .workspace(let value): b.taskWorkspacePreference = value
            case .mainSupervises(let on): b.mainSupervisesOwnTasks = on
            case .agentTasks(let on): b.agentTasks = on
            case .idleHours(let hours): b.idleCloseHours = hours
            case .archiveDays(let days): b.archiveClosedDays = days
            case .naming(let on): b.agentAutoNaming = on
            case .namingMax(let n): b.agentAutoNamingMax = n
            case .namingInterval(let hours): b.agentAutoNamingIntervalHours = hours
            }
            return b
        }

        func apply(to s: inout APIClient.AssistantState) {
            switch self {
            case .startRequested(let on): s.startRequestedTasks = on
            case .workspace(let value): s.taskWorkspacePreference = value
            case .mainSupervises(let on): s.mainSupervisesOwnTasks = on
            case .agentTasks(let on): s.agentTasks = on
            case .idleHours(let hours): s.idleCloseHours = hours
            case .archiveDays(let days): s.archiveClosedDays = days
            case .naming(let on): s.agentAutoNaming = on
            case .namingMax(let n): s.agentAutoNamingMax = n
            case .namingInterval(let hours): s.agentAutoNamingIntervalHours = hours
            }
        }
    }

    /// The field's value copied from `source` into `target`, when `source` has it.
    static func copy(_ field: Change.Field, from source: APIClient.AssistantState, to target: inout APIClient.AssistantState) {
        switch field {
        case .startRequested: if let v = source.startRequestedTasks { target.startRequestedTasks = v }
        case .workspace: if let v = source.taskWorkspacePreference { target.taskWorkspacePreference = v }
        case .mainSupervises: if let v = source.mainSupervisesOwnTasks { target.mainSupervisesOwnTasks = v }
        case .agentTasks: if let v = source.agentTasks { target.agentTasks = v }
        case .idleHours: if let v = source.idleCloseHours { target.idleCloseHours = v }
        case .archiveDays: if let v = source.archiveClosedDays { target.archiveClosedDays = v }
        case .naming: if let v = source.agentAutoNaming { target.agentAutoNaming = v }
        case .namingMax: if let v = source.agentAutoNamingMax { target.agentAutoNamingMax = v }
        case .namingInterval: if let v = source.agentAutoNamingIntervalHours { target.agentAutoNamingIntervalHours = v }
        }
    }

    private(set) var state: APIClient.AssistantState?
    /// Why the first read failed; with values on screen a failed refresh says so in a toast instead.
    private(set) var error: String?
    /// Fields with a write out: their control waits for it.
    private(set) var saving: Set<Change.Field> = []

    @ObservationIgnored private let deps: Deps
    /// The client of the assistant the page opened for, kept from its first use.
    @ObservationIgnored private var client: APIClient?
    @ObservationIgnored private var reads = 0

    init(deps: Deps = .live) { self.deps = deps }

    private var api: APIClient? {
        if client == nil { client = deps.api()?.currentAssistantSettings() }
        return client
    }

    /// The writes name the manager that answered the read, the default's too: the hub takes the session-name settings
    /// only from a request that names its manager.
    private var writer: APIClient? {
        guard let api else { return nil }
        if let manager = state?.manager, !manager.isEmpty { return api.manager(manager) }
        return api
    }

    /// The session-name rows: only from a hub that has all three settings and says which manager it is.
    var namesShown: Bool {
        guard let s = state, s.agentAutoNaming != nil, let max = s.agentAutoNamingMax, let every = s.agentAutoNamingIntervalHours else { return false }
        return !(s.manager ?? "").isEmpty && (1...AgentFollowUpText.maxNames).contains(max) && (0...AgentFollowUpText.maxHours).contains(every)
    }

    /// Whether the hub has any of the page's settings: the profile shows its row only then.
    static func offered(_ s: APIClient.AssistantState?) -> Bool {
        guard let s else { return false }
        return s.startRequestedTasks != nil || s.taskWorkspacePreference != nil || s.mainSupervisesOwnTasks != nil || s.agentTasks != nil
            || s.idleCloseHours != nil || s.archiveClosedDays != nil || s.agentAutoNaming != nil
    }

    func load() async {
        // What the app already holds shows at once; the read below brings the hub's latest.
        if state == nil { state = deps.held() }
        guard let api else {
            error = String(localized: "Not connected to the hub yet")
            return
        }
        reads += 1
        let read = reads
        do {
            var fresh = try await api.assistant()
            guard read == reads else { return }
            // A write still out keeps the value it asked for until its answer.
            if let shown = state { for field in saving { Self.copy(field, from: shown, to: &fresh) } }
            state = fresh
            error = nil
        } catch {
            guard read == reads, !error.isCancellation else { return }
            let text = error.shown ?? String(localized: "Can't reach the hub")
            if state != nil { deps.toast(Toast(String(localized: "Didn't refresh: \(text)"), style: .error)) } else { self.error = text }
        }
    }

    /// Shows the change at once, sends it alone, and keeps the hub's answer for that field; a refusal puts back the value
    /// the page had and says why.
    func set(_ change: Change) async {
        guard let api = writer, var shown = state, !saving.contains(change.field) else { return }
        let before = shown
        change.apply(to: &shown)
        state = shown
        saving.insert(change.field)
        defer { saving.remove(change.field) }
        do {
            let answer = try await api.setAssistant(change.body)
            if var now = state { Self.copy(change.field, from: answer, to: &now); state = now }
            deps.saved(answer)
        } catch {
            if var now = state { Self.copy(change.field, from: before, to: &now); state = now }
            let text = error.shown ?? String(localized: "Can't reach the hub")
            deps.toast(Toast(String(localized: "Couldn't save: \(text) [Agent Follow-Up]"), style: .error))
        }
    }
}

/// The page's words and its menus' values.
enum AgentFollowUpText {
    /// The hub's bounds (assistant/tasks_idle.go, agent_archive_auto.go, agent_naming_settings.go).
    static let maxHours = 8760
    static let maxDays = 3650
    static let maxNames = 1000

    static let idlePresets = [0, 1, 2, 4, 8, 12, 24, 48, 168]
    static let archivePresets = [0, 1, 3, 7, 14, 30, 90]
    /// 1000 is the hub's most, shown as Unlimited.
    static let namePresets = [1, 3, 5, 10, maxNames]
    static let intervalPresets = [6, 12, 24, 48, 168]

    /// "12 hours", "2 days", "1 week": the largest whole unit; 0 is `never`.
    static func hours(_ h: Int, never: String) -> String {
        guard h > 0 else { return never }
        if h % 168 == 0 { return Duration.seconds(h * 3600).formatted(.units(allowed: [.weeks], width: .wide)) }
        if h % 24 == 0 { return Duration.seconds(h * 3600).formatted(.units(allowed: [.days], width: .wide)) }
        return Duration.seconds(h * 3600).formatted(.units(allowed: [.hours], width: .wide))
    }

    static func days(_ d: Int, never: String) -> String {
        guard d > 0 else { return never }
        return Duration.seconds(d * 86400).formatted(.units(allowed: [.days], width: .wide))
    }

    static func names(_ n: Int) -> String {
        n >= maxNames ? String(localized: "Unlimited [Agent Follow-Up]") : n.formatted()
    }

    /// A menu's choices: the presets, and a value set elsewhere (the web, the assistant) where it sorts among them.
    static func choices(_ presets: [Int], current: Int?) -> [Int] {
        guard let current, !presets.contains(current) else { return presets }
        return (presets + [current]).sorted()
    }
}

/// Assistant Profile › Agent Follow-Up: how Persona starts the work it hands out, who follows it up, how it tidies up,
/// and what it does with unhelpful session names.
struct AgentFollowUpView: View {
    @State private var page = AgentFollowUpModel()
    /// The menu whose Custom… is being typed, and what is typed.
    @State private var custom: Custom?
    @State private var typed = ""

    /// The value a menu's Custom… asks for.
    struct Custom: Identifiable {
        enum Kind { case idleHours, archiveDays, namingMax, namingInterval }
        var kind: Kind
        var id: Kind { kind }

        var range: ClosedRange<Int> {
            switch kind {
            case .idleHours, .namingInterval: 1...AgentFollowUpText.maxHours
            case .archiveDays: 1...AgentFollowUpText.maxDays
            case .namingMax: 1...AgentFollowUpText.maxNames
            }
        }

        var title: String {
            switch kind {
            case .idleHours: String(localized: "Stop After How Many Hours?")
            case .archiveDays: String(localized: "Archive After How Many Days?")
            case .namingMax: String(localized: "How Many Name Changes?")
            case .namingInterval: String(localized: "Update Every How Many Hours?")
            }
        }

        /// The field's placeholder: the range, in digits.
        var hint: String { "\(range.lowerBound)–\(range.upperBound)" }

        func change(_ n: Int) -> AgentFollowUpModel.Change {
            switch kind {
            case .idleHours: .idleHours(n)
            case .archiveDays: .archiveDays(n)
            case .namingMax: .namingMax(n)
            case .namingInterval: .namingInterval(n)
            }
        }
    }

    /// A menu's tag for Custom…: never a value.
    private static let customTag = -1

    var body: some View {
        SettingsForm {
            if let s = page.state {
                startingSection(s)
                followUpSection(s)
                tidyingSection(s)
                if page.namesShown { namesSection(s) }
            } else if let error = page.error {
                EmptyStateView(symbol: Theme.Icon.error, title: String(localized: "Couldn't load [Agent Follow-Up]"), message: error,
                               action: (title: String(localized: "Retry [Components b8784c]"), run: { Task { await page.load() } }))
                    .listRowBackground(Color.clear)
            }
        }
        .overlay { if page.state == nil && page.error == nil { ProgressView() } }
        .themedGroupedPage()
        .pageTitle(String(localized: "Agent Follow-Up"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await page.load() }
        .refreshable { await page.load() }
        .alert(custom?.title ?? "", isPresented: Binding(get: { custom != nil }, set: { if !$0 { custom = nil } }), presenting: custom) { c in
            TextField(c.hint, text: $typed)
                #if os(iOS)
                .keyboardType(.numberPad)
                #endif
            Button(String(localized: "Cancel"), role: .cancel) {}
            Button(String(localized: "Save")) {
                if let n = Int(typed.trimmingCharacters(in: .whitespaces)), c.range.contains(n) { Task { await page.set(c.change(n)) } }
            }
            .disabled(Int(typed.trimmingCharacters(in: .whitespaces)).map { !c.range.contains($0) } ?? true)
        } message: { c in
            Text("A whole number from \(c.range.lowerBound) to \(c.range.upperBound).")
        }
    }

    // MARK: Sections

    @ViewBuilder private func startingSection(_ s: APIClient.AssistantState) -> some View {
        if s.startRequestedTasks != nil || s.taskWorkspacePreference != nil {
            Section {
                if let on = s.startRequestedTasks {
                    SettingsSwitchRow(title: String(localized: "Start What I Ask Right Away"), symbol: Theme.Icon.run, titleLines: 2,
                                      accessibilityID: "followup-start-requested", isOn: binding(on) { .startRequested($0) })
                        .disabled(page.saving.contains(.startRequested))
                }
                if let preference = s.taskWorkspacePreference {
                    Picker(selection: Binding(get: { preference }, set: { v in Task { await page.set(.workspace(v)) } })) {
                        Text("Existing Workspaces").lineLimit(1).tag("prefer-existing")
                        Text("New Workspaces").lineLimit(1).tag("prefer-new")
                    } label: {
                        Label { Text("New Tasks Prefer").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.folder) }
                    }
                    .pickerStyle(.menu)
                    .disabled(page.saving.contains(.workspace))
                    .accessibilityIdentifier("followup-workspace")
                }
            } header: {
                Text("Starting tasks [Agent Follow-Up]")
            } footer: {
                Text("On by default: what you ask for in a chat starts at once; tasks Persona thinks of itself still wait for Start. Read-only tasks never get a new workspace, and a busy folder gets its own copy.")
            }
        }
    }

    @ViewBuilder private func followUpSection(_ s: APIClient.AssistantState) -> some View {
        if s.mainSupervisesOwnTasks != nil || s.agentTasks != nil {
            Section {
                if let on = s.mainSupervisesOwnTasks {
                    SettingsSwitchRow(title: String(localized: "Main Chat Supervises Its Own Tasks"), symbol: Theme.Icon.supervised, titleLines: 2,
                                      accessibilityID: "assistant-main-supervises-own-tasks", isOn: binding(on) { .mainSupervises($0) })
                        .disabled(page.saving.contains(.mainSupervises))
                }
                if let on = s.agentTasks {
                    SettingsSwitchRow(title: String(localized: "Follow Up Every Agent Persona Starts"), symbol: Theme.Icon.agents, titleLines: 2,
                                      accessibilityID: "followup-agent-tasks", isOn: binding(on) { .agentTasks($0) })
                        .disabled(page.saving.contains(.agentTasks))
                }
            } header: {
                Text("Follow-up [Agent Follow-Up]")
            } footer: {
                Text("Both off by default. While the first is off, the background supervisor follows up the tasks Persona hands out, so the main chat answers you first. The second also follows up Agents started outside the main chat, and uses extra model calls.")
            }
        }
    }

    @ViewBuilder private func tidyingSection(_ s: APIClient.AssistantState) -> some View {
        if s.idleCloseHours != nil || s.archiveClosedDays != nil {
            Section {
                if let hours = s.idleCloseHours {
                    menu(String(localized: "Stop Idle Agents After"), Theme.Icon.stopIdle, id: "followup-idle-close", value: hours,
                         choices: AgentFollowUpText.choices(AgentFollowUpText.idlePresets, current: hours), custom: .idleHours,
                         saving: page.saving.contains(.idleHours)) { AgentFollowUpText.hours($0, never: String(localized: "Never [Agent Follow-Up stop]")) } change: { .idleHours($0) }
                }
                if let days = s.archiveClosedDays {
                    menu(String(localized: "Archive Closed Sessions After"), Theme.Icon.archive, id: "followup-archive-closed", value: days,
                         choices: AgentFollowUpText.choices(AgentFollowUpText.archivePresets, current: days), custom: .archiveDays,
                         saving: page.saving.contains(.archiveDays)) { AgentFollowUpText.days($0, never: String(localized: "Never [Agent Follow-Up archive]")) } change: { .archiveDays($0) }
                }
            } header: {
                Text("Tidying up [Agent Follow-Up]")
            } footer: {
                Text("By default an idle Agent stops after 12 hours and keeps its terminal, and a closed session with no open task moves to Archived after 3 idle days.")
            }
        }
    }

    @ViewBuilder private func namesSection(_ s: APIClient.AssistantState) -> some View {
        let on = s.agentAutoNaming ?? false, limit = s.agentAutoNamingMax ?? 1, every = s.agentAutoNamingIntervalHours ?? 0
        Section {
            SettingsSwitchRow(title: String(localized: "Fix Unhelpful Session Names"), symbol: Theme.Icon.autoName, titleLines: 2,
                              accessibilityID: "followup-auto-naming", isOn: binding(on) { .naming($0) })
                .disabled(page.saving.contains(.naming))
            if on {
                menu(String(localized: "Name Changes per Session"), Theme.Icon.edit, id: "followup-naming-max", value: limit,
                     choices: AgentFollowUpText.choices(AgentFollowUpText.namePresets, current: limit), custom: .namingMax,
                     saving: page.saving.contains(.namingMax)) { AgentFollowUpText.names($0) } change: { .namingMax($0) }
                SettingsSwitchRow(title: String(localized: "Update Names Regularly"), symbol: Theme.Icon.recurring, titleLines: 2,
                                  accessibilityID: "followup-naming-periodic",
                                  isOn: binding(every != 0) { .namingInterval($0 ? 24 : 0) })
                    .disabled(page.saving.contains(.namingInterval))
                if every != 0 {
                    menu(String(localized: "Update Every"), Theme.Icon.time, id: "followup-naming-interval", value: every,
                         choices: AgentFollowUpText.choices(AgentFollowUpText.intervalPresets, current: every), custom: .namingInterval,
                         saving: page.saving.contains(.namingInterval)) { AgentFollowUpText.hours($0, never: "") } change: { .namingInterval($0) }
                }
            }
        } header: {
            Text("Session names [Agent Follow-Up]")
        } footer: {
            if on && every != 0 && limit == 1 {
                Text("Raise Name Changes per Session above 1 so regular updates can rename a session.")
                    .foregroundStyle(Theme.cautionText)
            } else {
                Text("On by default: only clearly unusable names are fixed, once per session, with your information sorting model. Names you set are never replaced. Regular updates come only after new output and count toward the limit.")
            }
        }
    }

    // MARK: Parts

    private func binding(_ value: Bool, _ change: @escaping (Bool) -> AgentFollowUpModel.Change) -> Binding<Bool> {
        Binding(get: { value }, set: { v in Task { await page.set(change(v)) } })
    }

    /// A row with a menu of values: the presets, the current one if set elsewhere, and Custom….
    private func menu(_ title: String, _ symbol: String, id: String, value: Int, choices: [Int], custom kind: Custom.Kind, saving: Bool,
                      text: @escaping (Int) -> String, change: @escaping (Int) -> AgentFollowUpModel.Change) -> some View {
        Picker(selection: Binding(get: { value }, set: { v in
            if v == Self.customTag {
                typed = ""
                custom = Custom(kind: kind)
            } else if v != value {
                Task { await page.set(change(v)) }
            }
        })) {
            ForEach(choices, id: \.self) { n in Text(text(n)).lineLimit(1).tag(n) }
            Text("Custom… [Agent Follow-Up]").lineLimit(1).tag(Self.customTag)
        } label: {
            Label { Text(title).lineLimit(2) } icon: { ThemedIcon(symbol) }
        }
        .pickerStyle(.menu)
        .disabled(saving)
        .accessibilityIdentifier(id)
    }
}
