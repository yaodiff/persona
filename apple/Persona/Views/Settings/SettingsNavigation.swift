import SwiftUI
import PersonaModuleAPI

extension SettingsPage {
    @MainActor func belongsToCurrentAssistant(in model: AppModel) -> Bool {
        switch self {
        case .models, .agents, .assistant, .claude, .codex, .agy, .modelConfiguration, .triage, .standingAuthorizations, .agentFollowUp,
             .connectors, .plugins, .channels,
             .mail, .feishuBot, .sent, .skills,
             .knowledgeSelection, .pluginMarket, .marketExtension, .pluginMarkets, .inboxSources, .sourcesGraph, .computers:
            true
        case .connector(_, let manager), .plugin(_, let manager), .pluginPage(_, _, _, let manager), .channel(_, let manager),
             .subscriptions(_, let manager), .feishu(let manager):
            manager == nil || model.settingsRoute(manager) == nil
        #if os(iOS)
        case .meetings: true
        #endif
        default: false
        }
    }
}

extension AppModel {
    /// The manager a Settings route names for a page stamped `manager`, as its requests name it. A route that names none opens
    /// the current assistant's copy (`ExtensionDetailView`, `HubConnectorView`, …), so with multiple assistants the current
    /// assistant's route names none and another assistant's names its owner. Explicit manager ids normalize the same way; with
    /// one assistant or a hub without a roster, every route names none.
    func settingsRoute(_ manager: String?) -> String? {
        guard let roster = managerRoster, roster.isMulti else { return nil }
        let owner = roster.owner(manager)
        return owner == roster.owner(assistantManagerID) ? nil : owner
    }
}

/// One place in Settings: what search finds (its name and aliases) and, for the rows Settings' lists draw, where and when
/// (#13395, `SettingsCatalog.core` / `advanced`).
struct SettingsEntry: Identifiable {
    enum Group: String, CaseIterable { case assistant, device, hub }
    /// Where Settings draws the entry: on its home, on Settings › Advanced, or only inside another page. Search finds
    /// every entry, whatever its tier.
    enum Tier { case core, advanced, nested }
    /// When its row shows. Settings search applies the same rule when it has the current model.
    enum Shown {
        case always
        /// While the hub reports the feature.
        case hubFeature(String)
        /// A module allowed by the shared HubModules rule: an older or unanswered hub leaves it on.
        case hubModule(String)
        /// While the hub has the extension manager.
        case extensionManager
        /// With a hub paired: not in demo mode.
        case pairedHub
    }
    let id: String
    let title: String
    let symbol: String
    let group: Group
    let page: SettingsPage
    let aliases: String
    var shown: Shown = .always
    /// The row's accessibility identifier, which UI tests find it by; nil: `settings-<id>`.
    var row: String? = nil

    var rowID: String { row ?? "settings-" + id }

    var tier: Tier {
        SettingsCatalog.core.contains(id) ? .core : SettingsCatalog.advanced.contains(id) ? .advanced : .nested
    }

    @MainActor func shows(in model: AppModel) -> Bool {
        switch shown {
        case .always: true
        case .hubFeature(let feature): model.hubFeatures.contains(feature)
        case .hubModule(let id):
            HubModules(hello: model.hubFeatures).allows(ModuleDescriptor(id: ModuleID(rawValue: id), hubGated: true, slots: []))
        case .extensionManager: model.hubFeatures.hasExtensionFeature("manager")
        case .pairedHub: !model.isDemo
        }
    }

    func matches(_ query: String) -> Bool {
        let words = query.split(whereSeparator: \.isWhitespace)
        let text = title + " " + aliases
        return words.allSatisfy { text.localizedStandardContains(String($0)) }
    }
}

enum SettingsCatalog {
    /// Every entry: the core's, then the active modules' (`ModuleSlot.settings`, Live Screen).
    @MainActor static var entries: [SettingsEntry] { coreEntries + Modules.registry.settingsEntries.map(\.info) }

    /// The core's own entries.
    private static let coreEntries: [SettingsEntry] = {
        var rows: [SettingsEntry] = [
            .init(id: "templates", title: String(localized: "Templates"), symbol: Theme.Icon.style, group: .device, page: .templates,
                  aliases: String(localized: "Agent console")),
                        .init(id: "models", title: String(localized: "Models"), symbol: Theme.Icon.model, group: .assistant, page: .models,
                  aliases: String(localized: "settings.search.models")),
            .init(id: "assistant", title: String(localized: "Assistant Profile"), symbol: Theme.Icon.ai, group: .assistant, page: .assistant,
                  aliases: String(localized: "settings.search.assistant")),
            .init(id: "channels", title: String(localized: "Channels"), symbol: Theme.Icon.channels, group: .assistant, page: .channels,
                  aliases: String(localized: "settings.search.channels")),
            .init(id: "connectors", title: String(localized: "Connectors"), symbol: Theme.Icon.connectors, group: .assistant, page: .connectors,
                  aliases: String(localized: "settings.search.connectors"), row: "settings.connectors"),
            .init(id: "inbox-sources", title: String(localized: "Inbox Sources"), symbol: Theme.Icon.subscriptions, group: .assistant,
                  page: .inboxSources, aliases: String(localized: "settings.search.inbox_sources"),
                  shown: .hubFeature(SubscriptionsUI.sourcesFeature)),
            .init(id: "plugins", title: String(localized: "Extensions"), symbol: Theme.Icon.plugins, group: .assistant, page: .plugins,
                  aliases: String(localized: "settings.search.extensions"), shown: .extensionManager),
            .init(id: "agents", title: String(localized: "Agent"), symbol: Theme.Icon.options, group: .assistant, page: .agents,
                  aliases: String(localized: "settings.search.agents")),
            .init(id: "appearance", title: String(localized: "Appearance & Display"), symbol: Theme.Icon.style, group: .device, page: .appearance,
                  aliases: String(localized: "settings.search.appearance")),
            .init(id: "language", title: String(localized: "Language & Dictation"), symbol: Theme.Icon.language, group: .device, page: .language,
                  aliases: String(localized: "settings.search.language")),
            .init(id: "notifications", title: String(localized: "Notifications"), symbol: Theme.Icon.notifications, group: .device, page: .notifications,
                  aliases: String(localized: "settings.search.notifications")),
            .init(id: "translation", title: String(localized: "Translation"), symbol: Theme.Icon.translate, group: .device, page: .translation,
                  aliases: String(localized: "settings.search.translation")),
            .init(id: "storage", title: String(localized: "Storage"), symbol: Theme.Icon.storage, group: .device, page: .storage,
                  aliases: String(localized: "settings.search.storage")),
            .init(id: "about", title: String(localized: "About"), symbol: Theme.Icon.info, group: .device, page: .about,
                  aliases: String(localized: "settings.search.about")),
            .init(id: "hubs", title: String(localized: "Hub Connection"), symbol: Theme.Icon.hubGroup, group: .hub, page: .hubs,
                  aliases: String(localized: "settings.search.hubs"), shown: .pairedHub, row: "settings.hubs"),
            .init(id: "hub", title: String(localized: "Hub Version"), symbol: Theme.Icon.server, group: .hub, page: .hub,
                  aliases: String(localized: "settings.search.version"), shown: .pairedHub, row: "settings.hub"),
            .init(id: "managers", title: String(localized: "Assistants"), symbol: Theme.Icon.managers, group: .hub, page: .managers,
                  aliases: String(localized: "settings.search.assistants"), shown: .pairedHub, row: "settings.managers"),
            .init(id: "persona", title: String(localized: "Avatar"), symbol: Theme.Icon.avatar, group: .hub, page: .persona,
                  aliases: String(localized: "settings.search.avatar")),
            .init(id: "devices", title: String(localized: "Devices & Pairing"), symbol: Theme.Icon.credentials, group: .hub, page: .pairing,
                  aliases: String(localized: "settings.search.devices"), row: "settings-pairing"),
            .init(id: "triage", title: String(localized: "Session Triage"), symbol: Theme.Icon.triage, group: .assistant, page: .triage,
                  aliases: String(localized: "settings.search.triage")),
            .init(id: "standing-authorizations", title: String(localized: "Standing Authorizations"), symbol: Theme.Icon.standingAuthorization,
                  group: .assistant, page: .standingAuthorizations, aliases: String(localized: "settings.search.standing_authorizations")),
            .init(id: "agent-follow-up", title: String(localized: "Agent Follow-Up"), symbol: Theme.Icon.supervised, group: .assistant,
                  page: .agentFollowUp, aliases: String(localized: "settings.search.agent_follow_up")),
            .init(id: "claude", title: String(localized: "Claude Code Settings"), symbol: Theme.Icon.options, group: .assistant, page: .claude,
                  aliases: "Claude Code"),
            .init(id: "codex", title: String(localized: "Codex Settings"), symbol: Theme.Icon.options, group: .assistant, page: .codex,
                  aliases: "Codex"),
            .init(id: "agy", title: String(localized: "Antigravity Settings"), symbol: Theme.Icon.options, group: .assistant, page: .agy,
                  aliases: "Antigravity agy"),
            .init(id: "themeColor", title: String(localized: "Theme Color"), symbol: Theme.Icon.themeColor, group: .device, page: .themeColor,
                  aliases: String(localized: "settings.search.colors")),
            .init(id: "style", title: String(localized: "Interface Style"), symbol: Theme.Icon.style, group: .device, page: .style,
                  aliases: String(localized: "settings.search.style"))
        ]
        rows.append(.init(id: "colorScheme", title: String(localized: "Light & Dark"), symbol: Theme.Icon.colorScheme, group: .device, page: .colorScheme,
                          aliases: String(localized: "settings.search.scheme")))
        #if os(iOS)
        rows += [
            .init(id: "knowledgeSelection", title: String(localized: "Knowledge Selection"), symbol: Theme.Icon.knowledge, group: .assistant, page: .knowledgeSelection,
                  aliases: String(localized: "settings.search.knowledge"), row: "settings-knowledge-selection"),
            .init(id: "meetings", title: String(localized: "Meetings"), symbol: Theme.Icon.meeting, group: .assistant, page: .meetings,
                  aliases: String(localized: "settings.search.meetings"), shown: .hubModule("recording")),
            .init(id: "layout", title: String(localized: "Agents List Layout"), symbol: Theme.Icon.listView, group: .device, page: .layout,
                  aliases: String(localized: "settings.search.layout")),
            .init(id: "health", title: String(localized: "Health"), symbol: Theme.Icon.health, group: .device, page: .health,
                  aliases: String(localized: "settings.search.health"), shown: .hubModule("health")),
            .init(id: "phoneData", title: ConnectorText.device, symbol: Theme.Icon.thisDevice, group: .device, page: .phoneData,
                  aliases: String(localized: "settings.search.phoneData"), row: "settings-phone-data")
        ]
        if AlarmsAccess.isSupported {
            rows.append(.init(id: "alarms", title: String(localized: "Alarms and Timers"), symbol: Theme.Icon.alarmClock, group: .device,
                              page: .alarms, aliases: String(localized: "settings.search.alarms")))
        }
        #endif
        rows.append(.init(id: "computers", title: String(localized: "Machines"), symbol: Theme.Icon.machines, group: .assistant, page: .computers,
                          aliases: String(localized: "settings.search.computers"), row: "settings-machines"))
        return rows
    }()

    /// Settings home (#13395): at most `coreLimit` rows, in this order within each scope (`SettingsEntry.Group`), then
    /// one Advanced row.
    static let core = ["models", "assistant", "channels", "connectors", "inbox-sources", "plugins", "computers",
                       "notifications", "about", "hubs"]
    /// Settings › Advanced: every other row Settings home had, in the order it had them.
    static let advanced = ["knowledgeSelection", "meetings", "agents",
                           "appearance", "language", "live-screen", "translation", "storage", "health", "alarms", "phoneData",
                           "hub", "managers", "persona", "devices"]
    static let coreLimit = 10


    /// The rows of one tier in one scope, as `model` shows them now: an entry this platform does not have (Health off
    /// the iPhone) or whose condition is not met (`SettingsEntry.Shown`) is left out.
    @MainActor static func rows(_ tier: SettingsEntry.Tier, in group: SettingsEntry.Group, _ model: AppModel) -> [SettingsEntry] {
        let all = entries
        if NativeTemplateStore.shared.shortSettings, tier == .advanced {
            // The console's More Settings is the complete native Settings home's categories, not a second dead end.
            return all.filter { $0.shows(in: model) && ($0.group == group || group == .hub) }
        }
        return (tier == .core ? core : tier == .advanced ? advanced : []).compactMap { id in all.first { $0.id == id } }
            .filter { $0.group == group && $0.shows(in: model) }
    }

    @MainActor static func search(_ query: String, for model: AppModel? = nil) -> [SettingsEntry] {
        guard !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return [] }
        return entries.filter { entry in
            entry.matches(query) && (model.map { entry.shows(in: $0) } ?? true)
        }
    }
}

struct AppearanceSettingsPage: View {
    var body: some View {
        SettingsForm { AppearanceSection() }
            .themedGroupedPage()
            .detailTitle(String(localized: "Appearance & Display"))
    }
}

struct SettingsAboutPage: View {
    @State private var check: String?
    #if DEBUG
    /// `-openAbout debug` opens Diagnostics, `-openAbout debug-bottom` also scrolls to Debug Info's Copy All: for
    /// screenshots of the Build row and the whole section (#12926).
    private static let shot = UserDefaults.standard.string(forKey: "openAbout") ?? ""
    @State private var diagnosticsOpen = shot.hasPrefix("debug")
    #else
    @State private var diagnosticsOpen = false
    #endif
    private let version = "\(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "") (\(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? ""))"
    /// `3518b00185 · 11-22 15:00`, `-dirty` after the hash when the tree had uncommitted changes (#12926, `BuildInfo`).
    private let commit = BuildInfo.current.buildLabel(unknown: String(localized: "Unknown [Session Modes 4d8c1c]"))

    var body: some View {
        ScrollViewReader { proxy in
            form
            #if DEBUG
                .task {
                    guard Self.shot == "debug-bottom" else { return }
                    try? await Task.sleep(for: .seconds(1))
                    proxy.scrollTo("settings-debug-info-copy", anchor: .bottom)
                }
            #endif
        }
    }

    private var form: some View {
        SettingsForm {
            Section { LabeledContent("Version", value: version) }
            DevModeSection()
            Section {
                DisclosureGroup("Diagnostics", isExpanded: $diagnosticsOpen) {
                    LabeledContent("Build") { Text(commit).font(Theme.Font.code).textSelection(.enabled) }
                    if let check {
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text("Last New Chat Check")
                            Text(check).font(Theme.Font.code).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                        .accessibilityIdentifier("settings-new-chat-check")
                    }
                }
                .accessibilityIdentifier("settings-diagnostics")
            }
            #if DEBUG
            DebugInfoSection()
            #endif
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "About"))
        .task { check = UserDefaults.standard.string(forKey: NewChatCheck.defaultsKey) }
    }
}

#if DEBUG
/// A Debug build's About › Debug info (#12926): which build this is and what it talks to, with Copy All for pasting
/// into an Agent. The rows and the copied JSON are the same `AppDebugInfo` that `phone_device_info` returns. Never in
/// Release.
private struct DebugInfoSection: View {
    @Environment(AppModel.self) private var model
    @State private var info: AppDebugInfo?

    var body: some View {
        Section {
            if let info {
                ForEach(info.rows(Self.labels)) { row in
                    LabeledContent(row.label) {
                        Text(row.value).font(Theme.Font.code).multilineTextAlignment(.trailing).textSelection(.enabled)
                    }
                }
                Button { copy(info.json) } label: {
                    Label(String(localized: "Copy All [Debug Info]"), systemImage: Theme.Icon.copy).lineLimit(1)
                }
                .accessibilityIdentifier("settings-debug-info-copy")
                .id("settings-debug-info-copy")
            } else {
                ProgressView()
            }
        } header: {
            Text("Debug Info [Settings About]")
        }
        .accessibilityIdentifier("settings-debug-info")
        .task(id: model.live.state) { info = await AppDebugInfo.current(model) }
    }

    private func copy(_ text: String) {
        UIPasteboard.general.string = text
        Haptics.success()
        model.toast = Toast(String(localized: "Debug info copied"), style: .success)
    }

    private static let labels = AppDebugInfo.Labels(
        unknown: String(localized: "Unknown [Session Modes 4d8c1c]"),
        version: String(localized: "Version"),
        commit: String(localized: "Commit [Debug Info]"),
        branch: String(localized: "Branch [Debug Info]"),
        builtAt: String(localized: "Built At [Debug Info]"),
        builder: String(localized: "Build Machine [Debug Info]"),
        xcode: String(localized: "Xcode [Debug Info]"),
        swift: String(localized: "Swift [Debug Info]"),
        configuration: String(localized: "Configuration [Debug Info]"),
        bundleID: String(localized: "Bundle ID [Debug Info]"),
        hub: String(localized: "Hub [Debug Info]"),
        hubVersion: String(localized: "Hub Version [Debug Info]"),
        hubConnection: String(localized: "Hub Connection [Debug Info]"),
        launchedAt: String(localized: "Launched At [Debug Info]"),
        device: String(localized: "Device [Debug Info]"),
        system: String(localized: "System [Debug Info]"))
}
#endif
