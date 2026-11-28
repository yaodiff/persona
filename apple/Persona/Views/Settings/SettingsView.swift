import BeautifulUI
import AuthenticationServices
import SwiftUI
import VisionKit
import PersonaWire
#if os(iOS)
import PhotosUI
import UIKit
import Vision
#endif

struct SettingsView: View {
    @Environment(\.shell) private var shell
    /// Shown as a sheet without the shell (the iPad's, `PadRoutes`): the same × to close it.
    @Environment(\.settingsSheet) private var inSheet
    @Environment(AppModel.self) private var model
    @State private var confirmUnpair = false
    @State private var showPair = false
    @State private var search = ""
    @State private var searchResults: [SettingsEntry] = []
    @State private var plugins: [HubExtension]?
    /// Where links in the plugins' cards open (docs/extensions/extensions.md「UI slots」).
    @State private var pluginLink: LinkTarget?

    /// What the Extensions row's count and the slots are read again for; the current assistant too, as a switch from
    /// Settings' own heading leaves the path as it is.
    nonisolated private struct SlotsKey: Hashable, Sendable {
        var source: ExtensionSlots.Source
        var depth: Int
        var revision: Int
        var manager: String?
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("SettingsView")
        #endif
        // By value, every page: in the split (the iPad's sheet) they open beside the list, not inside its column, through
        // `settingsPath` (`SettingsListLink`, #12035).
        SplitNavigation(path: Bindable(model).settingsPath, destinations: SettingsDestinations()) {
            List {
                if search.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    if !NativeTemplateStore.shared.shortSettings { assistantSection }
                    if !NativeTemplateStore.shared.shortSettings { deviceSection }
                    hubSection
                    templatesSection
                    if !NativeTemplateStore.shared.shortSettings { advancedSection }
                    else { moreSettingsSection }
                } else {
                    Section {
                        ForEach(searchResults) { entry in
                            SettingsListLink(value: entry.page) {
                                SettingsRow(title: entry.title, symbol: entry.symbol, tint: Theme.accent)
                            }
                            .settingsListRow(entry.page)
                            .accessibilityIdentifier("settings-search-" + entry.id)
                        }
                        if searchResults.isEmpty {
                            Text("No settings found").foregroundStyle(.secondary)
                        }
                    } header: { SettingsHeading(String(localized: "Search Results")) }
                }
            }
            .accessibilityIdentifier("settings-list")
            .themedPage()
            .pageSearchable(text: $search, placement: .navigationBarDrawer(displayMode: .always), prompt: String(localized: "Search Settings"))
            .onChange(of: search) { searchResults = SettingsCatalog.search(search, for: model) }
            .onChange(of: model.assistantManagerID) {
                model.settingsPath = NavigationPath()
                plugins = nil
            }
            .onChange(of: model.hub?.id) {
                model.settingsPath = NavigationPath()
                plugins = nil
            }
            .pageToolbar {
                    // As a sheet over the shell (docs/apple/ios.md「New shell」): ✕ at the leading corner, as every presented
                    // page (#9653); Esc closes it too: a phone's full-screen cover has no swipe down.
                    if shell != nil || inSheet {
                        SheetClose(id: "settings-close") { model.shellSettings = false }
                    }
                }
                .navigationTitle("Settings")
            .task { await model.refreshAssistant() }
            #if DEBUG
            // With `-tab settings`: `-openAssistantPage assistant|routines|routine|newRoutine|followups|memory|memoryFile|
            // memoryEdit|about`, `-openStorage YES` (Storage), `-openNotificationSettings YES` (Notifications), `-openMachines YES` (Machines;
            // with `-openMachineDetail <id>` that machine's page), `-openDevices YES` (Devices), `-openHubUpdate YES` (hub),
            // `-openManagers YES` (Managers; `-openManagerPage <id>` one of them, `-showManagerCode YES` a new one's install code),
            // `-openSentAsYou YES` (Sent as You),
            // `-openSkills YES` (Skills), `-openExtensions YES` (Extensions; `-openExtension <id>` one of them, with `-openExtensionManager <id>`
            // another manager's copy; the old `-openPlugin…` names too), `-openChannels YES`
            // (Channels; `-openChannel <id>` one of them).
            .onAppear(perform: openDebugPage)
            #endif
            .accessibleConfirmationDialog("After disconnecting, you'll need to scan the code to pair again.", isPresented: $confirmUnpair, titleVisibility: .visible) {
                Button("Disconnect", role: .destructive) { Task { await model.disconnectAll() } }
            }
            .sheet(isPresented: $showPair) { PairView() }
            .pluginLinks($pluginLink)
            .extensionPushes(ExtensionSlots.shared.cardPages)
        } empty: {
            SplitPlaceholder(symbol: Theme.Icon.settings, title: String(localized: "Settings"), message: String(localized: "Choose an item on the left."))
                #if os(iOS)
                // Under the pages the list opens (#12035): they keep their large titles, as on the phone, unless a page
                // asks for an inline one.
                .navigationBarTitleDisplayMode(.large)
                #endif
        }
    }

    // MARK: Settings home's rows: the registry's core entries only (#13395, `SettingsRegistryTests`)

    private var assistantSection: some View {
        Section {
            Group {
                if !model.isDemo { ChecklistSettingsRow(inSection: false) }
                let extensions = ExtensionText.listSummary(plugins)
                ForEach(SettingsCatalog.rows(.core, in: .assistant, model)) { SettingsEntryRow(entry: $0, extensions: extensions) }
            }
            .bareListRow()
        } header: { SettingsAssistantHeading() } footer: {
            SettingsAssistantFooter()
        }
        .task(id: model.hubFeatures.contains(AssistantReadiness.feature)) {
            if !model.isDemo { await model.refreshReadiness(force: false) }
        }
        .task(id: SlotsKey(source: ExtensionSlots.source(model), depth: model.settingsPath.count, revision: model.managersRevision,
                           manager: model.assistantManagerID)) {
            guard let api = model.api else { return }
            let context = api.currentAssistantSettings()
            let loaded = try? await context.plugins()
            if model.showsPage(of: api) {
                plugins = loaded.map { ManagerRoster.settingsItems($0, roster: model.managerRoster, manager: context.managerID, stamped: { $0.manager }) }
            }
            await ExtensionSlots.shared.reload()
        }
    }

    private var deviceSection: some View {
        Section {
            Group {
                ForEach(SettingsCatalog.rows(.core, in: .device, model)) { SettingsEntryRow(entry: $0) }
            }
            .bareListRow()
        } header: {
            SettingsHeading(String(localized: "This Device [Settings Scope]"))
                .accessibilityIdentifier("settings-group-device")
        }
    }

    private var hubSection: some View {
        Section {
            Group {
                if model.isDemo {
                    Button { showPair = true } label: {
                        SettingsRow(title: String(localized: "Connect My Hub"), symbol: Theme.Icon.scanCode, tint: Theme.accent)
                    }
                    .modifier(SettingsButtonLook())
                    Button(role: .destructive) { model.unpair() } label: {
                        SettingsRow(title: String(localized: "Exit Demo Mode"), symbol: Theme.Icon.end, tint: Theme.danger)
                    }
                    .modifier(SettingsButtonLook())
                    .accessibilityIdentifier("settings.leaveDemo")
                }
                ForEach(SettingsCatalog.rows(.core, in: .hub, model)) { SettingsEntryRow(entry: $0) }
            }
            .bareListRow()
        } header: {
            SettingsHeading(String(localized: "Hub"))
                .accessibilityIdentifier("settings-group-hub")
        } footer: {
            Text(SettingsAssistantHeading.commonGroupsFooter)
        }
    }

    private var templatesSection: some View {
        Section {
            SettingsListLink(value: SettingsPage.templates) {
                SettingsRow(title: String(localized: "Templates"), symbol: Theme.Icon.style, tint: Theme.accent,
                            value: NativeTemplateStore.shared.enabled ? NativeTemplateStore.shared.templateName : nil)
            }
            .settingsListRow(SettingsPage.templates)
            .accessibilityIdentifier("settings-templates")
            .bareListRow()
        } footer: { Text("Import a shared template, preview its changes and apply the native Agent console. Default restores Persona.") }
    }

    private var moreSettingsSection: some View {
        Section {
            SettingsListLink(value: SettingsPage.advanced) {
                SettingsRow(title: String(localized: "More Settings"), symbol: Theme.Icon.options, tint: Theme.accent)
            }
            .settingsListRow(SettingsPage.advanced)
            .accessibilityIdentifier("settings-more")
            .bareListRow()
        } footer: { Text("All other categories remain available here and through Settings search.") }
    }

    // MARK: End of Settings home's rows

    /// Settings › Advanced (#13395): the rest of Settings, under the same scopes. A hub update waiting there says so here.
    private var advancedSection: some View {
        Section {
            SettingsListLink(value: SettingsPage.advanced) {
                SettingsRow(title: String(localized: "Advanced"), symbol: Theme.Icon.options, tint: Theme.accent,
                            value: SettingsHubVersion.update(model.hubUpdate), look: .oneLine)
            }
            .settingsListRow(SettingsPage.advanced)
            .accessibilityIdentifier("settings-advanced")
            .bareListRow()
        } footer: {
            Text("Appearance, language, storage, devices and more.")
        }
    }

    #if DEBUG
    /// Once per launch: a new look rebuilds the tree (and this view's state), and the route would undo a page opened since.
    @MainActor private static var debugOpened = false

    private func openDebugPage() {
        guard !Self.debugOpened else { return }
        let d = UserDefaults.standard
        if let value = d.string(forKey: "openAppearance"), value.hasPrefix("search:") {
            search = String(value.dropFirst("search:".count))
            searchResults = SettingsCatalog.search(search, for: model)
            Self.debugOpened = true
            return
        }
        // Appended with their own types: a path of AnyHashable finds no destination.
        var p = NavigationPath()
        // `-openExtension…` / `-openMarketExtension`; the `-openPlugin…` names from before Extensions still work (#1859).
        let str = { (name: String) in d.string(forKey: name) ?? d.string(forKey: name.replacingOccurrences(of: "Extension", with: "Plugin")) }
        let flag = { (name: String) in d.bool(forKey: name) || d.bool(forKey: name.replacingOccurrences(of: "Extension", with: "Plugin")) }
        if d.bool(forKey: "openMachines") {
            p.append(MachineListRoute())
            if let id = d.string(forKey: "openMachineDetail") { p.append(MachineDetailRoute(id: id)) }
            // `-openUsageMap <machine>` (`reclaim:<machine>`: the disk alert's reclaim mode): its disk usage map (#14576).
            if let id = d.string(forKey: "openUsageMap") {
                let reclaim = id.hasPrefix("reclaim:")
                p.append(MachineUsageMapRoute(ref: MachineRef(path: reclaim ? String(id.dropFirst("reclaim:".count)) : id), reclaim: reclaim))
            }
        }
        else if d.bool(forKey: "openAdvancedSettings") { p.append(SettingsPage.advanced) } // Settings › Advanced (#13395)
        else if d.bool(forKey: "openStorage") { p.append(SettingsPage.storage) }
        else if d.bool(forKey: "openNotificationSettings") { p.append(SettingsPage.notifications) }
        else if d.string(forKey: "openAssistantPage") != nil { p.append(SettingsPage.assistant) }
        else if d.bool(forKey: "openDevices") { p.append(SettingsPage.devices) }
        else if d.bool(forKey: "openHubs") || d.bool(forKey: "openSSHTunnel") || d.bool(forKey: "openLocalProxy") || d.bool(forKey: "openCloudflareAccess")
                    || d.bool(forKey: "openHubAddresses") {
            p.append(SettingsPage.hubs)
            // `-openSSHTunnel YES` / `-openLocalProxy YES`: the first hub's Settings › hub › SSH Tunnel / Local Proxy (screenshots).
            if d.bool(forKey: "openSSHTunnel"), let first = model.registry.hubs.first { p.append(SettingsPage.sshTunnel(first.id)) }
            if d.bool(forKey: "openLocalProxy"), let first = model.registry.hubs.first { p.append(SettingsPage.localProxy(first.id)) }
            // `-openCloudflareAccess YES`: its Settings › hub › Cloudflare Access.
            if d.bool(forKey: "openCloudflareAccess"), let first = model.registry.hubs.first { p.append(SettingsPage.cloudflareAccess(first.id)) }
            // `-openHubAddresses YES`: its Settings › hub › Addresses (`-seedHubAddresses` fills them).
            if d.bool(forKey: "openHubAddresses"), let first = model.registry.hubs.first { p.append(SettingsPage.hubAddresses(first.id)) }
        }
        else if d.bool(forKey: "openHubUpdate") { p.append(SettingsPage.hub) }
        else if d.bool(forKey: "openManagers") || d.string(forKey: "openManagerPage") != nil || d.bool(forKey: "showManagerCode") {
            // Settings › Managers (› one by its id; `-showManagerCode YES` makes one and opens its code sheet)
            p.append(SettingsPage.managers)
            if let id = d.string(forKey: "openManagerPage") { p.append(SettingsPage.manager(id)) }
        }
        else if d.bool(forKey: "openSentAsYou") { p = SettingsPage.sentPath }
        else if d.bool(forKey: "openConnectors") { p.append(SettingsPage.connectors) }
        else if d.bool(forKey: "openSkills") { p.append(SettingsPage.skills) }
        else if d.bool(forKey: "openInboxSources") { p.append(SettingsPage.inboxSources) } // Settings › Inbox Sources (#5278)
        else if d.bool(forKey: "openSourcesGraph") { p.append(SettingsPage.inboxSources); p.append(SettingsPage.sourcesGraph) } // › Source Relationships (#5741)
        else if let scope = str("openSubscriptions") { // the source subscription pages: `all`, or their first state (#2041)
            p.append(SettingsPage.subscriptions(state: scope == "all" ? nil : scope))
        }
        else if flag("openExtensions") || str("openExtension") != nil { // Settings › Extensions (› one (› a page of it))
            p.append(SettingsPage.plugins)
            if let id = str("openExtension") {
                // `-openExtensionManager <id>`: another manager's copy of it.
                let manager = d.string(forKey: "openExtensionManager")
                p.append(SettingsPage.plugin(id, manager: manager))
                if let ui = str("openExtensionPage") { p.append(SettingsPage.pluginPage(plugin: id, ui: ui, title: "", manager: manager)) }
            }
        }
        else if d.bool(forKey: "openChannels") || d.string(forKey: "openChannel") != nil { // Settings › Channels (› one)
            p.append(SettingsPage.channels)
            if let id = d.string(forKey: "openChannel") { p.append(SettingsPage.channel(id)) }
        }
        else if flag("openExtensionMarket") || flag("openExtensionMarkets") || str("openMarketExtension") != nil {
            // Settings › Extensions › Extension Market (› Markets, or › <market>/<extension>)
            p.append(SettingsPage.plugins)
            p.append(SettingsPage.pluginMarket)
            if flag("openExtensionMarkets") { p.append(SettingsPage.pluginMarkets) }
            if let m = str("openMarketExtension"), let slash = m.firstIndex(of: "/") {
                p.append(SettingsPage.marketExtension(market: String(m[..<slash]), id: String(m[m.index(after: slash)...])))
            }
        }
        else if d.bool(forKey: "openCodexSettings") { p.append(SettingsPage.codex) }
        else if d.bool(forKey: "openClaudeSettings") { p.append(SettingsPage.claude) } // with its Blocked Config Folders
        else if d.bool(forKey: "openConnectorMail") || d.bool(forKey: "addMail") { p.append(SettingsPage.mail) }
        else if d.bool(forKey: "openConnectorFeishu") { p.append(SettingsPage.feishu()) }
        else if d.bool(forKey: "openConnectorGitHub") { p.append(SettingsPage.connector("github")) }
        else if d.bool(forKey: "openConnectorTelegram") { p.append(SettingsPage.connector("telegram")) }
        else if let id = d.string(forKey: "openConnectorPlugin") { p.append(SettingsPage.connector(id)) } // a plugin's connector by its id
        else if d.bool(forKey: "openConnectorMessages") || d.string(forKey: "openMessagesChat") != nil { // Messages (› Session › one)
            p.append(SettingsPage.connector("imessage"))
            if d.bool(forKey: "openMessagesChats") || d.string(forKey: "openMessagesChat") != nil { p.append(MessagesChatsRoute()) }
            if let id = d.string(forKey: "openMessagesChat") { p.append(MessagesThreadRoute(id: id, name: "")) }
        }
        else if d.bool(forKey: "openConnectorBot") { p.append(SettingsPage.feishuBot) }
        else if d.bool(forKey: "showGroupAccess") { p.append(SettingsPage.feishuBot) } // Lark Bot, at Group Access (#16848)
        else if d.bool(forKey: "openConnectorDevice") { p.append(SettingsPage.phoneData) }
        else if d.bool(forKey: "openIPhoneSetup") { p = SettingsPage.iphoneSetupPath } // the guided setup (#8199)
        else if d.bool(forKey: "openTranslationSettings") { p.append(SettingsPage.translation) }
        else if d.bool(forKey: "openLiveScreenSettings") { // Settings › Live Screen (#6396), the screen-share module's
            if Modules.registry.settingsEntry(id: "live-screen") != nil { p.append(SettingsPage.module("live-screen")) }
        }
        else if d.string(forKey: "openAlarms") != nil { // Settings › Alarms and Timers on made-up data (#12805)
            #if os(iOS)
            p.append(SettingsPage.alarms)
            #endif
        }
        else if d.bool(forKey: "openMeetingSettings") { // Settings › Meetings (#2981)
            #if os(iOS)
            p.append(SettingsPage.meetings)
            #endif
        }
        else if d.bool(forKey: "openTriageSettings") { p.append(SettingsPage.triage) }
        else if let a = d.string(forKey: "openAbout") { // About (YES), or its Export Diagnostics (export, #9460)
            p.append(SettingsPage.about)
            if a == "export" { p.append(SettingsPage.devExport) }
        }
        else if d.string(forKey: "openMeetingTapes") != nil { // About › Replay a Meeting Tape (sample | empty, #10589)
            p.append(SettingsPage.about)
            p.append(SettingsPage.meetingTapes)
        }
        else if d.string(forKey: "openKnowledgeSelection") != nil { p.append(SettingsPage.knowledgeSelection) } // (rule | chat | doc: with that sheet)
        else if d.string(forKey: "openAppearance") == "layout" { p.append(SettingsPage.layout) } // Layout (#2530)
        else if d.string(forKey: "openAppearance") == "navigation" { p.append(SettingsPage.navigation) } // Navigation (#6332)
        else if d.string(forKey: "openAppearance") == "language" { p.append(SettingsPage.language) } // Language and dictation (#5005)
        else if let a = d.string(forKey: "openAppearance"), a != "chat" { // persona (or YES, or icon: the icon is the Avatar's) | color | style | scheme
            p.append(a == "color" ? SettingsPage.themeColor : a == "style" ? SettingsPage.style
                     : a == "scheme" ? SettingsPage.colorScheme : SettingsPage.persona)
        }
        else if d.bool(forKey: "openModelConfiguration") {
            p.append(SettingsPage.modelConfiguration)
            if let id = d.string(forKey: "openModelPurpose") { p.append(ModelPurposeRoute(id: id)) }
        }
        else if let n = d.string(forKey: "openLauncherPage") { p.append(ModelLaunchersRoute()); p.append(ModelLauncherRoute(name: n)) }
        else if d.bool(forKey: "openModelLaunchers") || d.string(forKey: "openModelLauncher") != nil { // Settings › Model (› one)
            p.append(ModelLaunchersRoute())
            if let n = d.string(forKey: "openModelLauncher") { p.append(ModelLauncherRoute(name: n)) }
        }
        #if os(iOS)
        if d.bool(forKey: "openWorkoutReports") { p = NavigationPath(); p.append(SettingsPage.health) }
        #endif
        guard !p.isEmpty else { return }
        Self.debugOpened = true
        model.settingsPath = p
    }
    #endif


}

/// The pages of Settings that are not routes of their own elsewhere.
enum SettingsPage: Hashable {
    #if os(iOS)
    case health
    /// Settings › Meetings (#2981): Persona's default role in a recorded meeting.
    case meetings
    /// Settings › Alarms and Timers (#12805): the alarms and timers Persona set through AlarmKit.
    case alarms
    #endif
    case persona, themeColor, style, colorScheme, language, assistant, claude, codex, agy, devices, phoneData, mail, feishuBot, notifications, storage, translation, modelConfiguration
    /// Settings › Connectors › This iPhone › iPhone Control: the guided setup (#8199).
    case iphoneSetup
    /// Settings › Connectors › the chat connector; `manager`: whose (Messages' Settings names the default's, whose
    /// account its chats follow; nil: the current assistant's).
    case feishu(manager: String? = nil)
    case models, agents, appearance, about, pairing, computers
    /// Settings › Advanced (#13395): the rows Settings home leaves out (`SettingsCatalog.advanced`).
    case advanced
    /// Settings › Templates (#17129): shared JSON/link preview and the opt-in native Agent console.
    case templates
    /// A module's page, by its settings entry's id (`ModuleSlot.settings`): Live Screen is the screen-share module's.
    case module(String)
    /// Settings › About › Export Diagnostics (Developing Persona, #9460).
    case devExport
    /// Settings › About › Replay a Meeting Tape (Developing Persona, #10589).
    case meetingTapes
    /// Settings › Session Triage (docs/assistant/agent-work.md「开关」).
    case triage
    /// Assistant Profile › Standing Authorizations (#7002): the go-aheads Persona answers for the owner, each revocable.
    case standingAuthorizations
    /// Assistant Profile › Agent Follow-Up (#7005): how Persona starts and follows up the work it hands out.
    case agentFollowUp
    /// Settings › Appearance › Layout (#2530): how the Agents list draws its agents.
    case layout
    /// Settings › Appearance › Navigation (#6332): Separate or Sidebar (`NavigationLayout`).
    case navigation
    /// Settings › Knowledge Selection (docs/assistant/knowledge.md "Selection"): what the knowledge base reads and keeps.
    case knowledgeSelection
    /// Settings › hub: the hubs paired, by group (docs/protocol/multi-hub.md).
    case hubs
    case hub, skills
    /// A connector of the hub's by its id (GET /api/v1/connectors): GitHub…; `manager`: another manager's copy
    /// (docs/assistant/managers.md "Grouping"), nil the default's.
    case connector(String, manager: String? = nil)
    /// Settings › Extensions, and one plugin by its id (docs/extensions/extensions.md) and manager (another manager's copy of
    /// the id; nil: the default's).
    case plugins
    case plugin(String, manager: String? = nil)
    /// A page a plugin draws (placement plugin_page, docs/extensions/extensions.md「Declarative UI」), titled until its View says
    /// otherwise; `manager`: the plugin's (nil: the default's).
    case pluginPage(plugin: String, ui: String, title: String, manager: String? = nil)
    /// The source subscription pages (#2041, docs/extensions/subscriptions.md "Pages"), drawn by the manager; `state` opens them on
    /// one extension's or one connector's (`SubscriptionsUI.state`), nil on every subscription. `manager`: whose (a page's
    /// of its own manager's things; nil: the current assistant's).
    case subscriptions(state: String? = nil, manager: String? = nil)
    /// Settings › Inbox Sources (#5278): the same pages unnarrowed, every source of the Inbox, once the manager reports
    /// `inbox_sources`.
    case inboxSources
    /// Inbox Sources' relationship view (#5741), pushed from its Source Relationships row.
    case sourcesGraph
    /// Settings › Extensions › Extension Market, one plugin of a market, and the markets (docs/extensions/extensions.md「Extension marketplace」).
    case pluginMarket
    case marketExtension(market: String, id: String)
    case pluginMarkets
    /// Settings › Channels, and one channel by its id (docs/extensions/channels.md).
    case channels
    case channel(String, manager: String? = nil)
    /// Settings › hub › SSH Tunnel of one paired hub (its id; docs/operations/network.md「Connecting to hub through SSH」).
    case sshTunnel(String)
    case inAppTunnel(String)
    /// Settings › hub › Local Proxy of one paired hub (docs/operations/network.md "Connecting to hub through a local SOCKS5 proxy").
    case localProxy(String)
    /// Settings › hub › Cloudflare Access of one paired hub (docs/operations/network.md "Behind Cloudflare Access").
    case cloudflareAccess(String)
    /// Settings › hub › Addresses of one paired hub (#4670, docs/operations/network.md "Client-added candidates").
    case hubAddresses(String)
    /// Settings › Managers, and one manager by its id (docs/assistant/managers.md).
    case managers
    case manager(String)
    /// Settings › Connectors, with individual sources and the sent ledger beneath it.
    case connectors
    /// Settings › Connectors › Records sent as you (docs/extensions/connectors.md "Sent as the user").
    case sent

    /// One canonical path for current callers and legacy Sent destinations.
    static var sentPath: NavigationPath { NavigationPath([SettingsPage.connectors, .sent]) }
    /// This iPhone's guided setup, under Settings › This iPhone (`persona://iphone/setup`).
    static var iphoneSetupPath: NavigationPath { NavigationPath([SettingsPage.phoneData, .iphoneSetup]) }

}

/// The pages the settings list opens, in either layout (`SplitNavigation`). MachinesView pushes its cards by value
/// (MachineDetailRoute), and a machine's page its agents (SessionKey).
private struct SettingsDestinations: ViewModifier {
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        content
            .navigationDestination(for: SettingsPage.self) { destination($0).splitDetailPage() }
            .navigationDestination(for: MachineListRoute.self) { _ in MachinesView(settingsScope: true).splitDetailPage() }
            .navigationDestination(for: ModelLaunchersRoute.self) { r in ModelLaunchersView(route: r).splitDetailPage() }
            .navigationDestination(for: ModelPurposeRoute.self) { r in ModelPurposeEditorView(purposeID: r.id).splitDetailPage() }
            .navigationDestination(for: ModelLauncherRoute.self) { r in ModelLauncherDetailView(name: r.name).splitDetailPage() }
            .navigationDestination(for: MachineDetailRoute.self) { r in MachineDetailView(ref: r.ref).splitDetailPage() }
            .navigationDestination(for: MachineUsageMapRoute.self) { r in UsageMapView(route: r).splitDetailPage() }
            .navigationDestination(for: TerminalsRoute.self) { r in TerminalsView(only: r.machine).splitDetailPage() }
            .navigationDestination(for: MessagesChatsRoute.self) { r in MessagesChatListView(manager: r.manager).splitDetailPage() }
            .navigationDestination(for: MessagesThreadRoute.self) { r in MessagesThreadView(id: r.id, name: r.name, manager: r.manager).splitDetailPage() }
            .navigationDestination(for: SessionKey.self) { key in ChatView(key: key).id(key).splitDetailPage(ownsBack: true) }
            .extensionPushDestination()
    }

    @ViewBuilder private func destination(_ page: SettingsPage) -> some View {
        if page.belongsToCurrentAssistant(in: model), let manager = model.assistantManagerOffline {
            SettingsForm {
                Section { ManagerOfflineNote(manager: manager) }
            }
            .themedGroupedPage()
            .detailTitle(String(localized: "Settings"))
        } else {
            self.page(for: page)
        }
    }

    @ViewBuilder private func page(for page: SettingsPage) -> some View {
        switch page {
        case .models: ModelConfigurationView()
        case .advanced: SettingsAdvancedPage()
        case .templates: NativeTemplatesView()
        case .agents: SettingsAgentView()
        case .appearance: AppearanceSettingsPage()
        case .about: SettingsAboutPage()
        case .devExport: DevExportPage()
        case .meetingTapes:
            #if os(iOS)
            MeetingTapesPage()
            #else
            EmptyView()
            #endif
        case .pairing: SettingsPairingPage()
        case .computers: MachinesView(settingsScope: true)
        case .persona: PersonaSettingsPage()
        case .themeColor: ThemeColorSettingsPage()
        case .style: StyleSettingsPage()
        case .language: LanguageSettingsPage()
        #if os(iOS)
        case .layout: LayoutSettingsPage()
        case .navigation: NavigationSettingsPage()
        case .knowledgeSelection: KnowledgeSelectionView()
        #else
        case .layout: EmptyView()
        case .navigation: EmptyView()
        case .knowledgeSelection: EmptyView()
        #endif
        case .colorScheme: ColorSchemeSettingsPage()
        case .assistant: AssistantProfileView()
        case .claude: ClaudeSettingsView()
        case .codex: ClaudeSettingsView(agent: .codex)
        case .agy: ClaudeSettingsView(agent: .agy)
        case .devices: DevicesView()
        case .phoneData: PhoneDataView()
        case .iphoneSetup:
            #if os(iOS)
            IPhoneSetupView()
            #else
            EmptyView()
            #endif
        case .mail: MailSettingsGate { MailAccountsView(inSheet: false) }
        case .feishu(let manager): FeishuConnectorView(manager: manager)
        case .connector(let id, let manager):
            if id == "imessage" { MessagesConnectorView(manager: manager ?? model.assistantManagerID) } else { HubConnectorView(id: id, manager: manager) }
        case .feishuBot: LarkBotGate { FeishuBotView() }
        #if os(iOS)
        case .health: WorkoutReportSettingsView()
        case .meetings: MeetingSettingsView()
        case .alarms: AlarmsPage()
        #endif
        case .module(let id): Modules.registry.settingsEntry(id: id)?.page()
        case .notifications: NotificationSettingsView()
        case .storage: StorageView()
        case .translation: TranslationSettingsView()
        case .triage: TriageSettingsView()
        case .standingAuthorizations: StandingAuthorizationsView()
        case .agentFollowUp: AgentFollowUpView()
        case .modelConfiguration: ModelConfigurationView()
        case .hubs: HubsView()
        case .sshTunnel(let id): HubSSHTunnelPage(hubID: id)
        case .inAppTunnel(let id): HubInAppTunnelPage(hubID: id)
        case .localProxy(let id): HubLocalProxyPage(hubID: id)
        case .cloudflareAccess(let id): HubCloudflareAccessPage(hubID: id)
        case .hubAddresses(let id): HubAddressesPage(hubID: id)
        case .managers: ManagersView()
        case .manager(let id): ManagerDetailView(id: id)
        case .connectors: ConnectorsView()
        case .sent: SentView()
        case .hub: HubSettingsPage()
        case .skills: SkillsPage()
        case .plugins: ExtensionsView()
        case .plugin(let id, let manager): ExtensionDetailView(id: id, manager: manager)
        case .pluginPage(let plugin, let ui, let title, let manager): ExtensionPageView(plugin: plugin, ui: ui, title: title, manager: manager ?? model.assistantManagerID)
        case .subscriptions(let state, let manager):
            ExtensionPageView(plugin: SubscriptionsUI.id, ui: SubscriptionsUI.page, title: String(localized: "Subscriptions [Source Subscriptions]"), state: state,
                              manager: manager ?? model.assistantManagerID)
        case .inboxSources:
            ExtensionPageView(plugin: SubscriptionsUI.id, ui: SubscriptionsUI.page, title: String(localized: "Inbox Sources"), manager: model.assistantManagerID)
        case .sourcesGraph: SourcesGraphView(manager: model.assistantManagerID)
        case .pluginMarket: ExtensionMarketView()
        case .marketExtension(let market, let id): MarketExtensionDetailView(market: market, id: id)
        case .pluginMarkets: ExtensionMarketsView()
        case .channels: ChannelsView()
        case .channel(let id, let manager):
            // The Lark bot's channel goes with the hub's Lark module (#15995); every other channel is as it was.
            if id == LarkBotEntry.channelID { LarkBotGate { ChannelDetailView(id: id, manager: manager) } } else { ChannelDetailView(id: id, manager: manager) }
        }
    }

}

/// Settings in Demo Mode: what it is, and the two ways out.
private struct DemoSection: View {
    @Environment(AppModel.self) private var model
    var pair: () -> Void

    var body: some View {
        Section {
            Group {
                Button(action: pair) {
                    if AppStyle.current.usesBeautifulUI {
                        SettingsRow(title: String(localized: "Connect My Hub"), symbol: Theme.Icon.scanCode, tint: Theme.accent)
                    } else {
                        Label("Connect My Hub", systemImage: Theme.Icon.scanCode).lineLimit(1)
                    }
                }
                .modifier(SettingsButtonLook())
                Button(role: .destructive) { model.unpair() } label: {
                    if AppStyle.current.usesBeautifulUI {
                        SettingsRow(title: String(localized: "Exit Demo Mode"), symbol: "rectangle.portrait.and.arrow.right", tint: Theme.danger)
                    } else {
                        Label("Exit Demo Mode", systemImage: "rectangle.portrait.and.arrow.right").lineLimit(1)
                    }
                }
                .modifier(SettingsButtonLook())
                .accessibilityIdentifier("settings.leaveDemo")
            }
            .bareListRow()
        } header: {
            SettingsHeading(String(localized: "Demo Mode"))
        } footer: {
            Text("The machines, agents, chats, and inbox you see are demo data. Nothing connects to a server, and messages, approvals, and other actions aren't actually sent. Connecting your own hub exits demo mode automatically.")
        }
    }
}

/// Settings › Connectors › This iPhone: which of the device's data Claude may use (phone_* tools). One switch per source, and
/// the switch is the whole state: on = what the hub is offered (2026-11-01, no Allowed / Tap to Allow labels), that is turned
/// on and allowed, or turned on and not asked yet for a source whose first use asks (`PhoneDataSource.asksWhenUsed`, #7659).
/// Turning one on asks for the permission; refused (or refused before) it stays off and Settings opens where it can be allowed.
struct PhoneDataView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @State private var statuses: [PhoneDataSource: PhoneAuthStatus] = [:]
    @State private var enabled: [PhoneDataSource: Bool] = [:]
    @State private var asking: PhoneDataSource?
    @StoredValue(ClipboardOffer.enabledKey) private var offerClipboard = false

    var body: some View {
        List {
            #if os(iOS)
            // Being operated by the assistant through a computer (docs/extensions/iphone.md): the guided setup, first,
            // always offered on an iPhone (#8199).
            IPhoneSetupEntry()
            #endif
            Section {
                ForEach(PhoneDataSource.onThisDevice) { s in
                    HStack(spacing: Theme.Space.m) {
                        SourceIcon(source: s)
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(s.title).lineLimit(1)
                            Text(s.detail).font(Theme.Font.label).foregroundStyle(.secondary)
                            #if os(iOS)
                            if s == .location, isOn(s) { LocationLevelRow() } // when the assistant may read it (#13200)
                            #endif
                        }
                        Spacer(minLength: Theme.Space.s)
                        if asking == s {
                            ProgressView()
                        } else {
                            Toggle(s.title, isOn: Binding(get: { isOn(s) }, set: { v in Task { await set(s, v) } }))
                                .labelsHidden()
                        }
                    }
                    .padding(.vertical, Theme.Space.xxs)
                }
            } footer: {
                #if os(iOS)
                Text("Claude can read the data you turn on through phone_* tools; writes such as new events or reminders ask you on the phone first. iOS asks for permission the first time Claude uses one, or when you turn it on here; Apple Health asks only here. If you denied it before, you'll go to Settings. The hub can reach the phone only while Persona is in the foreground (or just moved to the background). iOS doesn't expose Messages, Notes, or third-party app data.")
                #else
                Text("Claude can read the data you turn on through phone_* tools; writes such as new events or reminders ask you on the phone first. Turning one on requests system permission; if you denied it before, you'll go to Settings. The hub can reach the phone only while Persona is in the foreground (or just moved to the background). iOS doesn't expose Messages, Notes, or third-party app data.")
                #endif
            }
            #if os(iOS)
            // Sending the phone's clipboard to a computer (#3064): offered when the app comes back after copying.
            Section {
                Toggle(isOn: $offerClipboard) {
                    Text("Offer to Send What You Copied") // a switch's title: wraps at large sizes
                }
                .accessibilityIdentifier("clipboard-offer")
            } header: {
                Text("Clipboard")
            } footer: {
                Text("When you come back to Persona after copying something, offers to send it to a computer that shares its clipboard. The clipboard is read only when you tap Send.")
            }
            // Reminders imported as to-dos (#4009): only while Persona may read Reminders (not while it is on but not asked yet).
            if isOn(.reminders) && status(.reminders).isUsable { RemindersImportSection() }
            #endif
        }
        .themedPage()
        .navigationTitle(ConnectorText.device)
        .navigationBarTitleDisplayMode(.inline) // like the other connectors' pages
        .task { refresh() }
        // Back from the system's Settings, where a permission may have been given or taken away.
        .onChange(of: phase) { _, p in if p == .active { refresh() } }
    }

    private func isOn(_ s: PhoneDataSource) -> Bool {
        (enabled[s] ?? s.isEnabled) && s.offered(status(s))
    }

    private func status(_ s: PhoneDataSource) -> PhoneAuthStatus {
        statuses[s] ?? PhoneAuthorization.status(s)
    }

    private func refresh() {
        for s in PhoneDataSource.onThisDevice { statuses[s] = PhoneAuthorization.status(s) }
    }

    private func set(_ s: PhoneDataSource, _ on: Bool) async {
        defer {
            model.registerPhone() // what the hub may call follows the switch
            #if os(iOS)
            if s == .location { BackgroundLocation.shared.apply() } // Location off: no Always session, no wakes (#13200)
            #endif
        }
        guard on else {
            s.isEnabled = false
            enabled[s] = false
            return
        }
        var st = statuses[s] ?? PhoneAuthorization.status(s)
        if st == .notDetermined {
            asking = s
            st = await PhoneAuthorization.request(s)
            asking = nil
            statuses[s] = st
        }
        if st.isUsable {
            s.isEnabled = true
            enabled[s] = true
            Haptics.tap()
        } else {
            // Refused: iOS asks only once, so the rest is in its Settings; the switch stays off until it is allowed there.
            model.toast = Toast(String(localized: "Allow Persona to access \(s.title) in Settings"), style: .info)
            s.isEnabled = true // on its own once allowed there
            enabled[s] = true
            if let url = URL(string: UIApplication.openSettingsURLString) { await UIApplication.shared.open(url) }
        }
    }
}

#if os(iOS)
/// Under Location in Settings › Connectors › This iPhone (#13200): whether the assistant can read it while Persona isn't
/// open, and the one thing to do about it: Allow Always while iOS can still ask, else iOS Settings. Words and button on
/// one line where they fit, the button under the words where they don't.
private struct LocationLevelRow: View {
    var body: some View {
        let location = BackgroundLocation.shared
        if let line = BackgroundLocationPolicy.line(location.level, asked: location.state.asked) {
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) { words(line); button(line) }
                VStack(alignment: .leading, spacing: Theme.Space.xxs) { words(line); button(line) }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("location-level")
        }
    }

    private func words(_ line: LocationLevelLine) -> some View {
        Text(line.text).font(Theme.Font.label).foregroundStyle(.secondary)
    }

    private func button(_ line: LocationLevelLine) -> some View {
        Button {
            Haptics.tap()
            if line.action == .allowAlways, BackgroundLocation.shared.allowAlways() { return }
            if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
        } label: {
            Text(line.actionTitle).font(Theme.Font.labelStrong).lineLimit(1)
        }
        .buttonStyle(.borderless) // in the row: only the button takes its taps, not the switch beside it
        .tapTargetOutset()
        .accessibilityIdentifier(line.action == .allowAlways ? "location-allow-always" : "location-open-settings")
    }
}

/// Settings › Phone Data › Reminders: import the reminders of the lists picked as to-dos, one way (#4009,
/// `RemindersImport`). Off by default; every list until the user turns some off.
private struct RemindersImportSection: View {
    @Environment(AppModel.self) private var model
    @StoredValue(RemindersImport.enabledKey) private var importOn = false
    @State private var lists: [RemindersImport.ReminderList] = []
    /// nil: every list. Read with the lists in `.task`, not as the initial value (P3).
    @State private var picked: Set<String>?

    var body: some View {
        Section {
            Toggle(isOn: Binding(get: { importOn }, set: { on in
                importOn = on
                RemindersImporter.shared.activate(model, force: on)
            })) {
                Text("Import into To-dos") // a switch's title: wraps at large sizes
            }
            .accessibilityIdentifier("reminders-import")
            if importOn {
                ForEach(lists) { list in
                    Toggle(isOn: Binding(get: { picked?.contains(list.id) ?? true }, set: { on in choose(list.id, on) })) {
                        Text(list.title)
                    }
                }
            }
        } header: {
            Text("Reminders")
        } footer: {
            Text("Incomplete reminders in the lists you choose become to-dos, and later changes to their title, notes or due date follow. Nothing is written back to Reminders; completing or deleting a reminder leaves its to-do as it is. Imports happen while Persona is open.")
        }
        .task {
            picked = RemindersImport.lists // before the rows, which only show once the lists are read
            lists = await RemindersImport.availableLists()
        }
    }

    private func choose(_ id: String, _ on: Bool) {
        var now = picked ?? Set(lists.map(\.id))
        if on { now.insert(id) } else { now.remove(id) }
        picked = now
        UserDefaults.standard.set(Array(now).sorted(), forKey: RemindersImport.listsKey)
        RemindersImporter.shared.sync(model, force: true)
    }
}
#endif

/// A data source's icon: the iPhone app it comes from, else its symbol on a tile of the same size and shape.
private struct SourceIcon: View {
    var source: PhoneDataSource

    var body: some View {
        if let icon = source.appIcon {
            BrandMark(icon, side: Theme.Size.wellS)
        } else {
            ThemedIcon(source.symbol)
                .font(Theme.Font.heading)
                .foregroundStyle(.white)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Theme.accent.gradient)
                .scaledFrame(width: Theme.Size.wellS, height: Theme.Size.wellS)
                .clipShape(AppIconShape())
                .accessibilityHidden(true)
        }
    }
}

/// Persona's logo beside its name (Settings' header, the welcome page, the sidebar's Main Chat row): the Avatar's home screen
/// icon, in the look the home screen shows it in (#1312). A package's own face stands in for it here, in the icon's
/// shape: in the app it is the logo, on the home screen iOS allows only the icons the app ships (#3203).
struct AppGlyph: View {
    @Environment(AppModel.self) private var model
    @Environment(\.colorScheme) private var pageLook
    @Environment(\.displayScale) private var displayScale
    var size: CGFloat
    /// Scaled with the text size, as a mark in a row (`BrandMark`); the headers' are a fixed size.
    var scaled = false

    var body: some View {
        let p = model.persona
        Group {
            if !p.isBuiltin, let face = model.personas.avatar(p, points: size, scale: displayScale) {
                Image(uiImage: face).resizable().interpolation(.high).scaledToFill()
            } else {
                let icon = p.appIcon
                Image(icon.preview)
                    .resizable()
                    .interpolation(.high)
                    .environment(\.colorScheme, icon.homeScreenLook(model.colorScheme, system: systemLook))
            }
        }
        .modifier(GlyphFrame(side: size, scaled: scaled))
        .clipShape(AppIconShape())
        .overlay(AppIconShape().stroke(Theme.cardStroke, lineWidth: scaled ? Theme.Size.hairline : Theme.Size.line))
        .accessibilityHidden(true) // decoration: Persona's name is beside it
    }

    /// Under Match System the page's look (read here so the glyph redraws when the system's changes); else the screen's.
    private var systemLook: ColorScheme {
        model.colorScheme == .system ? pageLook : AppIconChoice.systemLook
    }

    private struct GlyphFrame: ViewModifier {
        var side: CGFloat
        var scaled: Bool

        func body(content: Content) -> some View {
            if scaled { content.scaledFrame(width: side, height: side) } else { content.frame(width: side, height: side) }
        }
    }
}

/// The first screen (docs/assistant/onboarding.md 4.5「首屏」, #4195): what Persona is, one button that pairs (scan the hub's code;
/// the Mac pastes the link), "No hub yet?" (the public getting-started page, when the build has its address), the demo,
/// and Other ways for the rest — typing the address, an SSH tunnel, the command line. No command on the screen itself.
struct WelcomeView: View {
    @Environment(AppModel.self) private var model
    @State private var pair: PairView.Start?
    @State private var guide: LinkTarget?
    @State private var showHelp = false
    private static let pairTitle = String(localized: "Scan to connect a hub")
    private static let pairIcon = Theme.Icon.scanCode
    private static let pairHint = String(localized: "Setting up the hub shows a QR code; or, on a device that's already connected, open Settings › Devices › Connect a new device")

    var body: some View {
        GeometryReader { bounds in
            ScrollView {
                VStack(spacing: Theme.Space.xl) {
                    VStack(spacing: Theme.Space.l) {
                        AppGlyph(size: Theme.Size.box80)
                        Text("Persona").font(Theme.Font.display).accessibilityAddTraits(.isHeader)
                    }
                    Button { pair = .scan } label: {
                        Label(Self.pairTitle, systemImage: Self.pairIcon)
                            .font(Theme.Font.heading).lineLimit(1).minimumScaleFactor(0.8)
                            .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.well)
                    }
                    .themedButton(prominent: true)
                    .controlSize(.large)
                    .accessibilityIdentifier("welcome.pair")
                    ViewThatFits(in: .horizontal) {
                        HStack(spacing: Theme.Space.xl) { actions }
                        VStack(spacing: Theme.Space.s) { actions }
                    }
                }
                .padding(Theme.Space.xl)
                .frame(maxWidth: Theme.Size.box380)
                .frame(maxWidth: .infinity, minHeight: bounds.size.height)
            }
        }
        .sheet(item: $pair) { PairView(start: $0) }
        .sheet(item: $guide) { LinkSheet(target: $0) }
        .alert(Text("Help"), isPresented: $showHelp) {
            if let url = GettingStarted.url {
                Button("No hub yet?") { guide = LinkTarget(url: url) }
                    .accessibilityIdentifier("welcome.guide")
            }
            Button("OK", role: .cancel) {}
                .accessibilityIdentifier("welcome.help.done")
        } message: { Text(Self.pairHint) }
        #if DEBUG
        // `-openPairSheet YES` (or `other`): the pairing sheet, for screenshots (scripts/app/sim.sh).
        .onAppear {
            switch UserDefaults.standard.string(forKey: "openPairSheet") {
            case "other": pair = .other
            case "YES", "1", "scan": pair = .scan
            default: break
            }
        }
        #endif
    }

    @ViewBuilder private var actions: some View {
        shortcut("Other ways", icon: Theme.Icon.keyboard, id: "welcome.other") { pair = .other }
        shortcut("Help", icon: Theme.Icon.help, id: "welcome.help") { showHelp = true }
        shortcut("Demo", icon: Theme.Icon.playMedia, id: "welcome.demo") { model.enterDemo() }
    }

    private func shortcut(_ title: LocalizedStringKey, icon: String, id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(spacing: Theme.Space.xs) {
                ThemedIcon(icon)
                    .font(Theme.Font.title)
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                    .background(.quaternary, in: Circle())
                Text(title).font(Theme.Font.secondaryStrong).lineLimit(1).fixedSize()
            }
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(id)
    }
}

/// The public 「开始使用」 page (docs/operations/getting-started.md as published): from the build config (`publish.getting_started_url`,
/// Info.plist `PersonaGettingStartedURL`); nil when the build has none, and "No hub yet?" is not shown.
enum GettingStarted {
    static var url: URL? {
        guard let s = NativeStateMigration.infoString("PersonaGettingStartedURL"),
              s.hasPrefix("https://"), let u = URL(string: s) else { return nil }
        return u
    }
}

struct PairView: View {
    /// What the sheet opens on: the scanner (the Mac: pasting the link), or Other ways — typing the address, an SSH
    /// tunnel, the command line (#4195: commands only here).
    enum Start: String, Identifiable { case password, scan, other; var id: String { rawValue } }

    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.webAuthenticationSession) private var webAuth
    let start: Start
    @State private var url = ""
    @State private var token = ""
    /// The SSH tunnel set up here, for a hub this device cannot reach directly (docs/operations/network.md「Connecting to hub through SSH」).
    @State private var ssh: SSHRoute?
    /// The hub's Cloudflare Access service token, typed in before pairing (docs/operations/network.md "Behind Cloudflare
    /// Access"): without it the first request is already sent to Access's login.
    @State private var accessID = ""
    @State private var accessSecret = ""
    @State private var showAccess = false
    @State private var connecting = false
    @State private var passwordMode: Bool
    @State private var error: String?
    @State private var scanning = DataScannerViewController.isSupported && DataScannerViewController.isAvailable
    /// The code was refused as expired or used (401 invalid_token): say so, and offer to scan again.
    @State private var expired = false
    @State private var showOther = false
    /// A scanned pairing link waiting for the user's Connect (#9929): a code read off a screen asks first, as a link
    /// from outside the app does.
    @State private var confirming: PairConfirmation?
    #if os(iOS)
    @State private var pickedPhoto: PhotosPickerItem?
    @State private var readingPhoto = false
    #endif

    init(start: Start = .password) {
        self.start = start
        _passwordMode = State(initialValue: start == .password)
    }

    private var other: Bool { start == .other || showOther }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("Connection method", selection: $passwordMode) {
                        Text("Password sign-in").tag(true)
                        Text("Scan or token").tag(false)
                    }.pickerStyle(.segmented)
                }
                if passwordMode {
                    Section("Password sign-in") {
                        TextField("HTTPS hub address", text: $url).autocorrectionDisabled()
                        Text("Enter the connection password set by your administrator in the system sign-in window. Use QR pairing for self-signed certificates.")
                            .font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                }
                if !passwordMode && expired {
                    Section {
                        Label("This code has expired or was already used; get a new one", systemImage: Theme.Icon.error)
                            .font(Theme.Font.secondaryStrong).foregroundStyle(Theme.danger)
                        Button {
                            expired = false
                            error = nil
                            scanning = DataScannerViewController.isSupported && DataScannerViewController.isAvailable
                        } label: { Label("Scan again", systemImage: Theme.Icon.scanCode).lineLimit(1) }
                        .accessibilityIdentifier("pair.rescan")
                    } footer: {
                        Text("Get a new code on the screen from setting up the hub, or on a connected device in Settings › Devices › Connect a new device.")
                    }
                }
                if !passwordMode && scanning && !other {
                    Section {
                        QRScanner { code in accept(code) }
                        .scaledFrame(height: Theme.Size.box280)
                        .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
                        .listRowInsets(EdgeInsets())
                    } footer: {
                        Text("Scan the QR code shown when the hub was set up, or the one from Settings › Devices › Connect a new device on a connected device.")
                    }
                }
                #if os(iOS)
                if !passwordMode && !other && confirming == nil {
                    Section {
                        PhotosPicker(selection: $pickedPhoto, matching: .images) {
                            HStack {
                                Label("Choose from Photos", systemImage: Theme.Icon.photos).lineLimit(1)
                                if readingPhoto { ProgressView() }
                            }
                        }
                        .disabled(readingPhoto)
                        .accessibilityIdentifier("pair.choosePhoto")
                        .onChange(of: pickedPhoto) { _, item in choosePhoto(item) }
                    }
                }
                #endif
                if !passwordMode && !other {
                    Section {
                        Button { withAnimation(Theme.Motion.standard) { showOther = true } } label: {
                            Label("Other ways", systemImage: Theme.Icon.more).lineLimit(1)
                        }
                        .accessibilityIdentifier("pair.other")
                    } footer: {
                        Text("Type the address, connect through an SSH tunnel, or make a code with a command on the hub's machine.")
                    }
                }
                if !passwordMode && other { otherWays }
                if !passwordMode { accessSection }
                if let error { Section { Text(error).foregroundStyle(Theme.danger).font(Theme.Font.caption) } }
            }
            .themedPage()
            .pairConfirmation($confirming) { _ in Task { await connect() } }
            #if DEBUG
            // `-showPairAccess YES`: Cloudflare Access opened (screenshots), its fields from `-seedCloudflareAccess id:secret`;
            // `-showPairAccess <pairing link>` also pairs with it at once (the Access error, MOCK_CF_ACCESS).
            .onAppear {
                let d = UserDefaults.standard
                guard let s = d.string(forKey: "showPairAccess") else { return }
                showAccess = true
                let parts = (d.string(forKey: "seedCloudflareAccess") ?? "").split(separator: ":", maxSplits: 1).map(String.init)
                if parts.count == 2 { accessID = parts[0]; accessSecret = parts[1] }
                if let cfg = ServerConfig(pairURL: s) {
                    url = cfg.url.absoluteString
                    token = cfg.token
                    passwordMode = false
                    Task { await connect() }
                }
            }
            #endif
            .modifier(PairSheetChrome(url: $url, token: $token, passwordMode: $passwordMode, connecting: connecting, connect: { await connect() }))
        }
    }

    /// Other ways: the address and the one-time code typed in, an SSH tunnel, and how the hub prints a code on its machine.
    @ViewBuilder private var otherWays: some View {
                Section("Enter Manually") {
                    TextField(String(localized: "Server Address"), text: $url).keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    // A one-time credential, not a password: beside the address it looks like a login form, and iOS
                    // would offer to save it as one ("Save Password?") once the sheet goes (#1598).
                    SecretField(String(localized: "Pairing code"), filled: String(localized: "Filled in. Tap Connect."), text: $token, identifier: "pair.token")
                }
                Section {
                    NavigationLink {
                        SSHTunnelForm(saved: ssh, dismissOnSave: true) { ssh = $0 }.navigationTitle("SSH Tunnel")
                    } label: {
                        SettingsRow(title: String(localized: "Connect via SSH Tunnel"), symbol: Theme.Icon.sshTunnel, tint: Theme.Hue.green,
                                    value: ssh.map { $0.label } ?? "", look: .oneLine)
                    }
                    .accessibilityIdentifier("pair.ssh")
                } footer: {
                    Text(ssh == nil ? String(localized: "If this device can't reach the hub's address (different network, no VPN), you can go through an SSH server that can reach the hub.")
                                    : ssh!.always ? String(localized: "Always connect to this hub through the SSH tunnel.") : String(localized: "Connect to this address directly first, and use the SSH tunnel if that fails."))
                }
                Section {
                    Text(verbatim: "personad hub pair")
                        .font(Theme.Font.code).textSelection(.enabled)
                } header: {
                    Text("Command line")
                } footer: {
                    Text("If you use the terminal on the hub's machine: run this line there and it prints a QR code and a pairing link.")
                }
    }

    /// Cloudflare Access, closed until needed; filled in before scanning or connecting.
    private var accessSection: some View {
        Section {
            DisclosureGroup(isExpanded: $showAccess) {
                CloudflareAccessFields(clientID: $accessID, clientSecret: $accessSecret, idPrefix: "pair.access")
            } label: {
                Label("Cloudflare Access", systemImage: Theme.Icon.cloudflareAccess).lineLimit(1)
            }
            .accessibilityIdentifier("pair.access")
        } footer: {
            Text("Only for a hub published through Cloudflare Access: the service token's Client ID and Client Secret. Kept with this hub's pairing in the keychain.")
        }
    }
}

/// The pairing sheet's title, Cancel / Connect and the Mac's paste handling, around its form.
private struct PairSheetChrome: ViewModifier {
    @Binding var url: String
    @Binding var token: String
    @Binding var passwordMode: Bool
    var connecting: Bool
    var connect: () async -> Void
    @Environment(\.dismiss) private var dismiss

    func body(content: Content) -> some View {
        content
            .navigationTitle("Connect Hub")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { dismiss() }
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await connect() } } label: { if connecting { ProgressView() } else { Text("Connect [V N C Page a55741]") } }
                        .disabled(connecting || (!passwordMode && token.isEmpty) || url.isEmpty)
                }
            }
    }
}

/// What a failed pairing says (#4195).
enum PairError {
    /// The hub refused the one-time code: expired, already used or revoked (401 `invalid_token`; older hubs: the words).
    static func isExpiredCode(_ error: Error) -> Bool {
        guard let api = error as? APIError else { return false }
        return api.code == "invalid_token" || (api.status == 401 && api.message.lowercased().contains("invalid token")) // i18n-ignore: the hub's words
    
    }

    static var expiredText: String { String(localized: "This code has expired or was already used; get a new one") }
}

extension AppModel {
    /// A pairing link opened from outside (the camera, a pasted link) failed: an expired code in words, else as any error.
    func showPairing(_ error: Error) {
        if PairError.isExpiredCode(error) {
            toast = Toast(PairError.expiredText, style: .error)
            Haptics.error()
        } else {
            show(error)
        }
    }
}

extension PairView {
    /// Camera and a picked photo use the same confirmation and connection path (#11057).
    private func accept(_ code: String) {
        guard let cfg = ServerConfig(pairURL: code) else { return }
        url = cfg.url.absoluteString
        token = cfg.token
        passwordMode = false
        scanning = false
        confirming = PairConfirmation(cfg, paired: model.registry.hubs.map(\.config))
    }

    #if os(iOS)
    private func choosePhoto(_ item: PhotosPickerItem?) {
        guard let item, !readingPhoto else { return }
        pickedPhoto = nil
        error = nil
        readingPhoto = true
        Task {
            defer { readingPhoto = false }
            do {
                guard let data = try await item.loadTransferable(type: Data.self) else { throw PairingPhoto.Failure.unreadableImage }
                let code = try await Task.detached { () throws -> String in
                    guard let image = UIImage(data: data) else { throw PairingPhoto.Failure.unreadableImage }
                    var payload: String?
                    try PairingPhoto.decode(image) { payload = $0 }
                    guard let payload else { throw PairingPhoto.Failure.unreadableImage }
                    return payload
                }.value
                accept(code)
            } catch {
                let failure = error as? PairingPhoto.Failure ?? .unreadableImage
                self.error = failure.localizedDescription
            }
        }
    }
    #endif

    fileprivate func connect() async {
        if passwordMode { await passwordLogin(); return }
        guard let u = URL(string: url.trimmingCharacters(in: .whitespaces)) else { return }
        connecting = true
        defer { connecting = false }
        do {
            try await model.pair(ServerConfig(url: u, token: token.trimmingCharacters(in: .whitespaces),
                                              access: HubAccess(clientID: accessID, clientSecret: accessSecret)), ssh: ssh)
            Haptics.success()
            dismiss()
        } catch {
            if error is CloudflareAccessError {
                // Access, not the address: no advice about networks or SSH; its fields open to fill in or correct.
                self.error = error.localizedDescription
                showAccess = true
            } else if PairError.isExpiredCode(error) {
                // A code already used or out of date (401 invalid_token): not 「invalid token」, and a way to scan again.
                self.error = nil
                expired = true
            } else if ssh != nil {
                if !error.isCancellation { self.error = String(localized: "Couldn't connect: \(error.localizedDescription)") }
            } else {
            if !error.isCancellation { self.error = String(localized: "Couldn't connect: \(error.localizedDescription). Make sure your phone can reach the address (for example, on the same network or VPN), or connect through an SSH tunnel.") }
            }
            if !expired { scanning = DataScannerViewController.isSupported && DataScannerViewController.isAvailable } // scan again
            Haptics.error()
        }
    }

    private func passwordLogin() async {
        connecting = true
        error = nil
        defer { connecting = false }
        do {
            let kind = "phone"
            let flow = try PasswordLoginRequest(address: url, name: UIDevice.current.name, kind: kind)
            let callback = try await webAuth.authenticate(using: flow.page, callback: .customScheme("persona"),
                preferredBrowserSession: .ephemeral, additionalHeaderFields: [:])
            let credential = try await flow.exchange(callback)
            try await model.pair(ServerConfig(url: flow.server, token: credential.token), deviceID: credential.id)
            Haptics.success()
            dismiss()
        } catch let e as ASWebAuthenticationSessionError where e.code == .canceledLogin {
            return
        } catch {
            if !error.isCancellation { self.error = error.localizedDescription; Haptics.error() }
        }
    }
}

#if os(iOS)
/// One picked photo yields one QR payload, or a reason nothing was delivered (#11057).
nonisolated enum PairingPhoto {
    enum Failure: Error, Equatable, LocalizedError {
        case unreadableImage
        case noQRCode
        case multipleQRCodes

        var errorDescription: String? {
            switch self {
            case .unreadableImage: String(localized: "The photo couldn't be read. Try another one.")
            case .noQRCode: String(localized: "No QR code was found in this photo. Try another one.")
            case .multipleQRCodes: String(localized: "This photo contains more than one QR code. Choose a photo with one QR code.")
            }
        }
    }

    static func decode(_ image: UIImage, onCode: (String) -> Void) throws {
        guard let cgImage = image.cgImage else { throw Failure.unreadableImage }
        let request = VNDetectBarcodesRequest()
        // Revision 1 also works in simulator runtimes without the default inference context.
        request.revision = VNDetectBarcodesRequestRevision1
        request.symbologies = [.qr]
        let orientation: CGImagePropertyOrientation = switch image.imageOrientation {
        case .up: .up
        case .down: .down
        case .left: .left
        case .right: .right
        case .upMirrored: .upMirrored
        case .downMirrored: .downMirrored
        case .leftMirrored: .leftMirrored
        case .rightMirrored: .rightMirrored
        @unknown default: .up
        }
        do {
            try VNImageRequestHandler(cgImage: cgImage, orientation: orientation, options: [:]).perform([request])
        } catch {
            throw Failure.unreadableImage
        }
        guard let results = request.results, !results.isEmpty else { throw Failure.noQRCode }
        guard results.count == 1 else { throw Failure.multipleQRCodes }
        guard let payload = results[0].payloadStringValue else { throw Failure.unreadableImage }
        onCode(payload)
    }
}
#endif

struct QRScanner: UIViewControllerRepresentable {
    var onCode: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let vc = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])], qualityLevel: .balanced,
                                           isHighlightingEnabled: true)
        vc.delegate = context.coordinator
        try? vc.startScanning()
        return vc
    }

    func updateUIViewController(_ vc: DataScannerViewController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(onCode: onCode) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let onCode: (String) -> Void
        init(onCode: @escaping (String) -> Void) { self.onCode = onCode }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for item in items {
                if case .barcode(let b) = item, let s = b.payloadStringValue {
                    Haptics.success()
                    onCode(s)
                    return
                }
            }
        }
    }
}

/// Settings' Machines row: N / M online. Its own view: the page read `model.machines` for it, and a machine's heartbeat (on every
/// push of a busy hub) drew the whole page and its list again (#1495); now only this row.
struct MachinesSettingsRow: View {
    @Environment(AppModel.self) private var model
    @State private var summary = ""

    var body: some View {
        SettingsRow(title: String(localized: "Machines"), symbol: Theme.Icon.machines, tint: Theme.Hue.blue, value: summary)
            .task { update() }
            .onChange(of: model.machines) { update() }
            .onChange(of: model.assistantManagerID) { update() }
            .onChange(of: model.managerRoster) { update() }
    }

    private func update() {
        let machines = AssistantSettingsRequestScope(model).machines(model)
        summary = String(localized: "\(machines.filter(\.online).count) / \(machines.count) Online")
    }
}

/// A row of Settings' list: the system's label, with what it is set to on the right; in the BeautifulUI style its list
/// row (a tinted tile, the name, the value) at the height the other styles' rows have.
struct SettingsRow: View {
    enum ValueLook { case plain, code, oneLine, digits }
    var title: String
    var symbol: String
    var tint: Color
    var value: String? = nil
    var look: ValueLook = .plain
    /// A brand's own mark (an asset of `Theme.Brand`) in place of the tinted tile, in the BeautifulUI style.
    var brand: String? = nil
    /// Public catalog artwork; absent artwork preserves the existing row mark.
    var iconURL: String? = nil

    var body: some View {
        if AppStyle.current.usesBeautifulUI {
            BUIListRow(title, value: value.flatMap { $0.isEmpty ? nil : $0 }) {
                if let iconURL, ExtensionImageCache.url(iconURL) != nil {
                    ExtensionLogo(url: iconURL, side: Theme.Size.iconL)
                } else if let brand { SettingsBrandMark(asset: brand) } else { IconTile(symbol, tint: tint) }
            }
        } else if let value {
            LabeledContent {
                switch look {
                case .plain: Text(value)
                case .code: Text(value).font(Theme.Font.body.monospaced())
                case .oneLine: Text(value).lineLimit(1)
                case .digits: Text(value).monospacedDigit()
                }
            } label: {
                Label(title, systemImage: symbol)
            }
        } else {
            Label(title, systemImage: symbol)
        }
    }
}

/// A switch row of Settings with a mark (#1650): the system's switch with its label; in the BeautifulUI style the list row
/// `SettingsRow` draws there (the tile, the name, its description under it) with the switch at its end, so a card's switch
/// rows and the rows that open a page start with the same mark. `tint` is the tile's, `iconColor` the bare symbol's in the
/// other styles (nil leaves it the list's).
struct SettingsSwitchRow: View {
    var title: String
    var detail: String? = nil
    var symbol: String
    var tint: Color = Theme.accent
    var iconColor: Color? = nil
    /// The name's line limit in the other styles; BeautifulUI's row wraps it.
    var titleLines: Int? = nil
    /// On the switch itself, where UI tests look for it (`app.switches[…]`).
    var accessibilityID: String? = nil
    @Binding var isOn: Bool

    var body: some View {
        if AppStyle.current.usesBeautifulUI {
            BUIListRow(title, detail: detail, detailLines: .max) { IconTile(symbol, tint: tint) } trailing: {
                Toggle(title, isOn: $isOn).labelsHidden().accessibilityIdentifier(accessibilityID ?? "")
            }
            .accessibilityElement(children: .contain)
        } else {
            Toggle(isOn: $isOn) {
                Label {
                    if let detail {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(title).lineLimit(titleLines)
                            Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    } else {
                        Text(title).lineLimit(titleLines)
                    }
                } icon: {
                    if let iconColor { ThemedIcon(symbol).foregroundStyle(iconColor) } else { ThemedIcon(symbol) }
                }
            }
            .accessibilityIdentifier(accessibilityID ?? "")
        }
    }
}

/// A brand's mark as a row's leading view: the size of `BUIIconTile`, so the names line up with the other rows'.
struct SettingsBrandMark: View {
    var asset: String
    @Environment(\.buiTheme) private var theme

    var body: some View {
        BrandMark(asset, side: Theme.Size.iconL * max(1, theme.typeScale * 0.9), scaled: false)
    }
}

extension View {
    /// The rows of a `List` section in the BeautifulUI style: a `BUIListRow` has its own height (its padding and minimum),
    /// so the list adds no inset above and below it on top (77.7 pt rows instead of the 52.3 pt the other styles have).
    /// Apply it to the rows (a `Group` of them), not the `Section`: on the section it takes the header's padding too, and
    /// not on a row inside a `NavigationLink`'s label (it does not reach the list there). Other styles keep the list's insets.
    func bareListRow() -> some View { listRowVerticalInsets(AppStyle.current.usesBeautifulUI ? 0 : nil) }

    /// A row of Settings' list that opens `value`: on the Mac it stays selected while that page is open beside the list
    /// (`MacSettingsRowSelection`, #2318). Nothing elsewhere.
    @ViewBuilder func settingsListRow(_ value: some Hashable) -> some View {
        self
    }
}

/// A row of Settings' own list that opens the page `value` (#12035). In one stack (an iPhone, an iPad window at compact
/// width) a `NavigationLink(value:)`, which pushes the page. In the split (the iPad's Settings sheet), where the list is a
/// column beside the page, a button that opens the page through `settingsPath` (`splitOpen`), as the Agents list's rows
/// do: a value link there showed its page by the split's own selection, outside the path, and no row on that page
/// opened anything; a `List(selection:)` instead cleared the paths links and launch switches set.
struct SettingsListLink<Value: Hashable, Label: View>: View {
    #if os(iOS)
    @Environment(\.splitOpen) private var splitOpen
    #endif
    let value: Value
    let label: Label

    init(value: Value, @ViewBuilder label: () -> Label) {
        self.value = value
        self.label = label()
    }

    var body: some View {
        #if os(iOS)
        if let splitOpen {
            // The label as a link draws it, and the whole width of the row to tap.
            Button { splitOpen.open(value) } label: {
                label.frame(maxWidth: .infinity, alignment: .leading).contentShape(.rect)
            }
            .buttonStyle(.plain)
        } else {
            NavigationLink(value: value) { label }
        }
        #else
        NavigationLink(value: value) { label }
        #endif
    }
}

/// A section's title in Settings: the list's own; in the BeautifulUI style its heading.
struct SettingsHeading: View {
    var text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        if AppStyle.current.usesBeautifulUI { BUISectionHeading(text).textCase(nil) } else { Text(text) }
    }
}

/// Settings' buttons that are rows: the list's tinted button; in the BeautifulUI style a plain row (its tile carries the color).
struct SettingsButtonLook: ViewModifier {
    func body(content: Content) -> some View {
        if AppStyle.current.usesBeautifulUI { content.buttonStyle(.plain) } else { content }
    }
}

extension EnvironmentValues {
    /// Settings is a sheet of its own (the iPad's, docs/apple/ios.md「iPad」): its × closes it, as over the shell.
    @Entry var settingsSheet = false
}
