import SwiftUI

// The rows Settings' lists draw from the registry (#13395, `SettingsCatalog.core` / `advanced`): Settings home and
// Settings › Advanced draw an entry the same way, wherever its tier puts it, as a `SettingsListLink` (#12035: in the
// iPad's split, Settings home's rows open their page through `settingsPath`; on Advanced, a page of the path, they push). A value that changes on its own (the hub's
// link, its version, the space used) is read in a row of its own, so it redraws that row alone
// (docs/apple/performance.md, P5).

/// One registry entry's row: a link to its page, with what it is set to on the right.
struct SettingsEntryRow: View {
    let entry: SettingsEntry
    /// The Extensions row's count and how many need attention, which Settings home loads.
    var extensions = ""

    var body: some View {
        switch entry.id {
        case "computers":
            SettingsListLink(value: MachineListRoute()) { MachinesSettingsRow() }
                .settingsListRow(MachineListRoute())
                .accessibilityIdentifier(entry.rowID)
        case "plugins": SettingsEntryLink(entry: entry, value: extensions)
        case "language": SettingsEntryLink(entry: entry, value: AppLanguage.settingsSummary)
        case "translation": SettingsTranslationRow(entry: entry)
        case "storage": SettingsStorageRow(entry: entry)
        case "hubs": SettingsModelRow(entry: entry) { SettingsHubLink.state($0) }
        case "hub": SettingsModelRow(entry: entry) { SettingsHubVersion.summary($0.hubUpdate) }
        case "persona": SettingsModelRow(entry: entry) { $0.persona.cardTitle }
        #if os(iOS)
        case "meetings":
            SettingsModelRow(entry: entry) { MeetingAttendees.offered($0) ? MeetingAttendees.defaultRole($0).title : "" }
        case "phoneData":
            SettingsListLink(value: entry.page) { SettingsRow(title: entry.title, symbol: entry.symbol, tint: Theme.accent) }
                .settingsListRow(entry.page)
                .accessibilityIdentifier(entry.rowID)
        #endif
        default:
            // A module's entry draws its own row when it has one (Live Screen's, shown only while a Mac offers it).
            if let row = Modules.registry.settingsEntry(id: entry.id)?.row() { row } else { SettingsEntryLink(entry: entry, value: "") }
        }
    }
}

/// The plain link most entries are.
private struct SettingsEntryLink: View {
    let entry: SettingsEntry
    let value: String

    var body: some View {
        SettingsListLink(value: entry.page) {
            SettingsRow(title: entry.title, symbol: entry.symbol, tint: Theme.accent, value: value, look: .oneLine)
        }
        .settingsListRow(entry.page)
        .accessibilityIdentifier(entry.rowID)
    }
}

/// A link whose value is read from the model in this row's own body.
private struct SettingsModelRow: View {
    @Environment(AppModel.self) private var model
    let entry: SettingsEntry
    let value: @MainActor (AppModel) -> String

    var body: some View { SettingsEntryLink(entry: entry, value: value(model)) }
}

/// Translation: off, or the language it translates into.
private struct SettingsTranslationRow: View {
    let entry: SettingsEntry
    @StoredValue(TranslationPrefs.enabledKey) private var translating = false
    @StoredValue(TranslationPrefs.targetKey) private var translateTarget = TranslationPrefs.defaultTarget

    var body: some View {
        SettingsEntryLink(entry: entry, value: translating ? TranslationPrefs.name(of: translateTarget)
                          : String(localized: "state.off", defaultValue: "Off"))
    }
}

/// Storage: the space Persona uses on this device, measured off the main thread each time the row shows and whenever a
/// page above it closes (back from Storage after clearing; in the split the list stays on screen).
private struct SettingsStorageRow: View {
    @Environment(AppModel.self) private var model
    let entry: SettingsEntry
    @State private var used: Int64?

    var body: some View {
        SettingsEntryLink(entry: entry, value: used?.storageText ?? "")
            .onAppear(perform: measure)
            .onChange(of: model.settingsPath) { measure() }
    }

    private func measure() {
        Task { used = await Task.detached(priority: .utility) { StorageUsage.scan().values.reduce(0, +) }.value }
    }
}

/// The Hub Version row's value: an update out or under way, else the hub's version.
enum SettingsHubVersion {
    static func summary(_ status: HubUpdateStatus?) -> String {
        guard let status else { return "" }
        return update(status).isEmpty ? status.version : update(status)
    }

    /// Only an update out or under way: what Settings home's Advanced row says while the Hub Version row is under it.
    static func update(_ status: HubUpdateStatus?) -> String {
        guard let status else { return "" }
        if status.working { return status.stateTitle }
        if status.latest?.newer == true {
            return status.channel == "docker" ? String(localized: "New Image") : String(localized: "Update Available")
        }
        return ""
    }
}

/// The Hub Connection row's value: connected (and how, when not directly), or why not.
enum SettingsHubLink {
    @MainActor static func state(_ model: AppModel) -> String {
        // While the hub restarts into a new version, the same words as the connection title (Hub updating…).
        guard model.live.looksConnected else {
            let phase = model.connection.phase
            return phase == .online ? String(localized: "Not Connected") : ConnectionLabel.text(phase, hubRestarting: model.hubUpdate?.restarting == true)
        }
        let tunneled = model.hub.flatMap { model.tunnel($0.group) }?.routed == true
        if let t = model.hub.flatMap({ model.inAppTunnelState($0.group) }), t.routed { return String(localized: "Connected to hub · via \(t.route.title)") }
        if model.hub.flatMap({ model.localProxyState($0.group) })?.routed == true {
            return String(localized: "Connected to hub · via \(String(localized: "Local Proxy"))")
        }
        return tunneled ? String(localized: "Connected to hub · via SSH tunnel") : String(localized: "Connected to hub")
    }
}

/// Settings › Advanced (#13395): the rows Settings home leaves out, under the same three scopes, in the order the home
/// had them. Search on Settings home finds them too.
struct SettingsAdvancedPage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        List {
            ForEach(SettingsEntry.Group.allCases, id: \.self) { group in
                let rows = SettingsCatalog.rows(.advanced, in: group, model)
                if !rows.isEmpty {
                    Section {
                        Group {
                            ForEach(rows) { SettingsEntryRow(entry: $0) }
                        }
                        .bareListRow()
                    } header: {
                        SettingsHeading(heading(group))
                    }
                }
            }
        }
        .accessibilityIdentifier("settings-advanced-list")
        .themedPage()
        .detailTitle(NativeTemplateStore.shared.shortSettings ? String(localized: "More Settings") : String(localized: "Advanced"))
    }

    private func heading(_ group: SettingsEntry.Group) -> String {
        switch group {
        case .assistant: SettingsAssistantHeading.content(for: model).title
        case .device: String(localized: "This Device [Settings Scope]")
        case .hub: String(localized: "Hub")
        }
    }
}
