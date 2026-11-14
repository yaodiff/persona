import SwiftUI

extension SettingsPage {
    @MainActor func belongsToCurrentAssistant(in model: AppModel) -> Bool {
        switch self {
        case .models, .agents, .assistant, .claude, .codex, .modelConfiguration, .triage, .standingAuthorizations, .agentFollowUp,
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

struct SettingsEntry: Identifiable {
    enum Group: String, CaseIterable { case assistant, device, hub }
    let id: String
    let title: String
    let symbol: String
    let group: Group
    let page: SettingsPage
    let aliases: String

    func matches(_ query: String) -> Bool {
        let words = query.split(whereSeparator: \.isWhitespace)
        let text = title + " " + aliases
        return words.allSatisfy { text.localizedStandardContains(String($0)) }
    }
}

enum SettingsCatalog {
    static let entries: [SettingsEntry] = {
        var rows: [SettingsEntry] = [
            .init(id: "models", title: String(localized: "Models"), symbol: Theme.Icon.model, group: .assistant, page: .models,
                  aliases: String(localized: "settings.search.models")),
            .init(id: "assistant", title: String(localized: "Assistant Profile"), symbol: Theme.Icon.ai, group: .assistant, page: .assistant,
                  aliases: String(localized: "settings.search.assistant")),
            .init(id: "channels", title: String(localized: "Channels"), symbol: Theme.Icon.channels, group: .assistant, page: .channels,
                  aliases: String(localized: "settings.search.channels")),
            .init(id: "connectors", title: String(localized: "Connectors"), symbol: Theme.Icon.connectors, group: .assistant, page: .connectors,
                  aliases: String(localized: "settings.search.connectors")),
            .init(id: "inbox-sources", title: String(localized: "Inbox Sources"), symbol: Theme.Icon.subscriptions, group: .assistant,
                  page: .inboxSources, aliases: String(localized: "settings.search.inbox_sources")),
            .init(id: "plugins", title: String(localized: "Extensions"), symbol: Theme.Icon.plugins, group: .assistant, page: .plugins,
                  aliases: String(localized: "settings.search.extensions")),
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
                  aliases: String(localized: "settings.search.hubs")),
            .init(id: "hub", title: String(localized: "Hub Version"), symbol: Theme.Icon.server, group: .hub, page: .hub,
                  aliases: String(localized: "settings.search.version")),
            .init(id: "managers", title: String(localized: "Assistants"), symbol: Theme.Icon.managers, group: .hub, page: .managers,
                  aliases: String(localized: "settings.search.assistants")),
            .init(id: "persona", title: String(localized: "Avatar"), symbol: Theme.Icon.avatar, group: .hub, page: .persona,
                  aliases: String(localized: "settings.search.avatar")),
            .init(id: "devices", title: String(localized: "Devices & Pairing"), symbol: Theme.Icon.credentials, group: .hub, page: .pairing,
                  aliases: String(localized: "settings.search.devices")),
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
            .init(id: "themeColor", title: String(localized: "Theme Color"), symbol: Theme.Icon.themeColor, group: .device, page: .themeColor,
                  aliases: String(localized: "settings.search.colors")),
            .init(id: "style", title: String(localized: "Interface Style"), symbol: Theme.Icon.style, group: .device, page: .style,
                  aliases: String(localized: "settings.search.style"))
        ]
        #if os(iOS) || os(visionOS)
        rows.append(.init(id: "colorScheme", title: String(localized: "Light & Dark"), symbol: Theme.Icon.colorScheme, group: .device, page: .colorScheme,
                          aliases: String(localized: "settings.search.scheme")))
        #endif
        #if os(iOS)
        rows += [
            .init(id: "knowledgeSelection", title: String(localized: "Knowledge Selection"), symbol: Theme.Icon.knowledge, group: .assistant, page: .knowledgeSelection,
                  aliases: String(localized: "settings.search.knowledge")),
            .init(id: "meetings", title: String(localized: "Meetings"), symbol: Theme.Icon.meeting, group: .assistant, page: .meetings,
                  aliases: String(localized: "settings.search.meetings")),
            .init(id: "live-screen", title: String(localized: "Live Screen"), symbol: Theme.Icon.screen, group: .device, page: .liveScreen,
                  aliases: String(localized: "settings.search.live_screen")),
            .init(id: "layout", title: String(localized: "Agents List Layout"), symbol: Theme.Icon.listView, group: .device, page: .layout,
                  aliases: String(localized: "settings.search.layout")),
            .init(id: "health", title: String(localized: "Health"), symbol: Theme.Icon.health, group: .device, page: .health,
                  aliases: String(localized: "settings.search.health")),
            .init(id: "phoneData", title: ConnectorText.device, symbol: Theme.Icon.thisDevice, group: .device, page: .phoneData,
                  aliases: String(localized: "settings.search.phoneData"))
        ]
        #endif
        rows.append(.init(id: "computers", title: String(localized: "Machines"), symbol: Theme.Icon.machines, group: .assistant, page: .computers,
                          aliases: String(localized: "settings.search.computers")))
        return rows
    }()

    static func search(_ query: String) -> [SettingsEntry] {
        guard !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return [] }
        return entries.filter { $0.matches(query) }
    }
}

struct AppearanceSettingsPage: View {
    var body: some View {
        SettingsForm { AppearanceSection(settingsHome: false) }
            .themedGroupedPage()
            .detailTitle(String(localized: "Appearance & Display"))
    }
}

struct SettingsAboutPage: View {
    @State private var check: String?
    private let version = "\(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "") (\(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? ""))"
    private let commit = Bundle.main.object(forInfoDictionaryKey: "PersonaCommit") as? String ?? String(localized: "Unknown [Session Modes 4d8c1c]")

    var body: some View {
        SettingsForm {
            Section { LabeledContent("Version", value: version) }
            Section {
                DisclosureGroup("Diagnostics") {
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
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "About"))
        .task { check = UserDefaults.standard.string(forKey: NewChatCheck.defaultsKey) }
    }
}
