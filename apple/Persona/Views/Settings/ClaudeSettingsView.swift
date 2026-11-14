import SwiftUI
import PersonaWire

/// Claude Code's /config options (a whitelist of ~/.claude/settings.json keys) on each machine.
/// Which agent's settings a page edits: Claude Code's settings.json or Codex's config.toml (the node keeps a whitelist
/// of each, claudeconfig.go / codexconfig.go).
enum AgentSettingsKind {
    case claude, codex

    var title: String { self == .claude ? String(localized: "Claude Code Settings") : String(localized: "Codex Settings") }

    var footer: String {
        switch self {
        case .claude: String(localized: "This edits ~/.claude/settings.json on that machine and changes only the settings below. New Claude sessions pick them up; running sessions usually need a restart.")
        case .codex: String(localized: "This edits ~/.codex/config.toml on that machine and changes only the settings below; everything else in the file (model providers, project trust, and so on) stays as is. New Codex sessions pick them up.")
        }
    }
}

struct ClaudeSettingsView: View {
    @Environment(AppModel.self) private var model
    var agent: AgentSettingsKind = .claude
    private let requestScope = AssistantSettingsRequestScope(AppModel.shared)
    /// Selection is local to this page; a saved computer from another assistant must never start a request.
    @State private var machineID = ""
    private var ref: MachineRef { MachineRef(path: machineID) }
    private var onlineMachines: [Machine] { requestScope.machines(model).filter(\.online) }
    @StoredValue("claudeSettings.applyAll") private var applyAll = true
    @State private var settings: [APIClient.ClaudeSetting] = []
    @State private var settingsRef: MachineRef?
    @State private var loading = false
    @State private var error: String?
    @State private var strings: [String: String] = [:]
    /// Live Replies of the machine shown (Claude Code only; nil before it loads, and gone on a hub without it).
    @State private var liveReplies: LiveRepliesModel?

    var body: some View {
        SettingsForm {
            Section {
                Picker(String(localized: "picker.machine", defaultValue: "Machine"), selection: $machineID) {
                    ForEach(onlineMachines, id: \.ref) { m in Text(m.title).tag(m.ref.path) }
                }
                Toggle("Apply to All Online Machines", isOn: $applyAll)
            } footer: {
                Text(agent.footer)
            }
            if onlineMachines.isEmpty {
                Section {
                    Text("This assistant has no online computers. Connect one to change its Agent settings.")
                        .font(Theme.Font.label).foregroundStyle(.secondary)
                }
            }
            if let error {
                Section { Text(error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
            }
            #if os(macOS)
            // An empty group draws as a blank gap in a Mac form (while loading, or a machine with nothing to set).
            if !settings.isEmpty { Section { ForEach(settings) { s in row(s) } } }
            #else
            Section {
                ForEach(settings) { s in row(s) }
            }
            #endif
            if agent == .claude, let live = liveReplies, live.ref == ref, !live.unsupported {
                LiveRepliesSection(model: live, others: applyAll ? onlineMachines.filter { $0.ref != ref }.map(\.ref) : [])
            }
            if agent == .claude, !machineID.isEmpty { ClaudeDirsSection(ref: ref) }
        }
        .themedPage()
        .detailTitle(agent.title)
        .onChange(of: onlineMachines, initial: true) { _, available in
            guard !available.contains(where: { $0.ref == ref }) else { return }
            machineID = available.first(where: { $0.id == model.hubMachineID })?.ref.path ?? available.first?.ref.path ?? ""
        }
        .overlay { if loading && settings.isEmpty { ProgressView() } }
        .task(id: machineID) { await load() }
        .task(id: agent == .claude ? machineID : "") { await loadLiveReplies() }
        .refreshable {
            await load()
            await liveReplies?.load()
        }
    }

    /// The machine's Live Replies switch, read again whenever another machine is picked.
    private func loadLiveReplies() async {
        guard agent == .claude, !machineID.isEmpty, onlineMachines.contains(where: { $0.ref == ref }) else { return }
        let api = requestScope.api(model)
        let group = requestScope.group
        let live = LiveRepliesModel(ref: ref, deps: .init(
            api: { $0.group == group ? api : nil },
            name: { model.machine($0)?.title ?? $0.id }, toast: { model.toast = $0 }))
        liveReplies = live
        await live.load()
    }

    @ViewBuilder private func row(_ s: APIClient.ClaudeSetting) -> some View {
        let value = s.value ?? s.default
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            switch s.type {
            case "bool":
                Toggle(isOn: Binding(get: { value?.bool ?? false }, set: { v in Task { await set(s.key, .bool(v)) } })) {
                    title(s)
                }
            case "enum":
                Picker(selection: Binding(get: { value?.string ?? "" }, set: { v in Task { await set(s.key, .string(v)) } })) {
                    ForEach(s.options ?? [], id: \.self) { Text(AgentSettingOptions.name($0, key: s.key)).tag($0) }
                } label: { title(s) }
            default:
                title(s)
                TextField(s.default?.string.flatMap { $0.isEmpty ? nil : $0 } ?? String(localized: "Not set"),
                          text: Binding(get: { strings[s.key] ?? value?.string ?? "" }, set: { strings[s.key] = $0 }))
                    .onSubmit { Task { await set(s.key, .string(strings[s.key] ?? "")) } }
                    .submitLabel(.done)
                    #if os(macOS)
                    .labelsHidden() // a Mac form shows a field's title as its label; here it is the prompt (#1221)
                    #endif
            }
        }
        .padding(.vertical, Theme.Space.xxs)
        .disabled(settingsRef != ref || loading)
    }

    private func title(_ s: APIClient.ClaudeSetting) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            HStack(spacing: Theme.Space.sm) {
                Text(s.title)
                if s.value == nil { Text("Default [Prompt Composer 844b8c]").font(Theme.Font.label).foregroundStyle(.tertiary) }
            }
            Text(s.detail).font(Theme.Font.label).foregroundStyle(.secondary)
        }
    }

    private func load() async {
        let available = onlineMachines
        if !available.contains(where: { $0.ref == self.ref }) {
            machineID = available.first(where: { $0.id == model.hubMachineID })?.ref.path ?? available.first?.ref.path ?? ""
            settings = []
            strings = [:]
            liveReplies = nil
        }
        let ref = ref
        guard !machineID.isEmpty, let api = requestScope.api(model) else { return }
        loading = true
        defer { loading = false }
        do {
            let result = agent == .claude ? try await api.claudeSettings(machine: ref.id) : try await api.codexSettings(machine: ref.id)
            guard ref == self.ref, !Task.isCancelled else { return }
            settings = result
            settingsRef = ref
            strings = [:]
            error = nil
        } catch {
            guard ref == self.ref, !Task.isCancelled else { return }
            self.error = error.shown
        }
    }

    private func set(_ key: String, _ value: JSONValue) async {
        let selected = ref
        guard settingsRef == selected, onlineMachines.contains(where: { $0.ref == selected }), let api = requestScope.api(model) else { return }
        let targets = applyAll ? onlineMachines.map(\.ref) : [selected]
        var failed: [String] = []
        for target in targets {
            do {
                let list = agent == .claude ? try await api.setClaudeSetting(machine: target.id, key: key, value: value)
                    : try await api.setCodexSetting(machine: target.id, key: key, value: value)
                if target == selected, selected == ref { settings = list }
            } catch {
                if !error.isCancellation { failed.append(String(localized: "\(model.machine(target)?.title ?? target.id): \(error.localizedDescription)")) }
            }
        }
        if failed.isEmpty {
            Haptics.success()
            model.toast = Toast(targets.count > 1 ? String(localized: "Applied to \(targets.count) machines") : String(localized: "Saved"), style: .success)
        } else {
            Haptics.error()
            error = failed.joined(separator: "\n")
        }
    }
}

