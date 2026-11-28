import Foundation
import Testing
@testable import Persona

/// Settings home is the registry's core rows and one Advanced row; Settings › Advanced has the rest, under the same three
/// scopes (#13395, docs/design/DESIGN_SYSTEM.md 4.1.1). Every row the home had before keeps exactly one home.
@MainActor
struct SettingsRegistryTests {
    /// Every row Settings home had before #13395, by catalog id, in its order then.
    private static let formerHome = [
        "models", "assistant", "channels", "connectors", "inbox-sources", "plugins", "knowledgeSelection", "meetings", "agents", "computers",
        "appearance", "language", "notifications", "live-screen", "translation", "storage", "health", "alarms", "phoneData", "about",
        "hubs", "hub", "managers", "persona", "devices",
    ]
    private static let settingsView = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .appending(path: "Persona/Views/Settings/SettingsView.swift")

    @Test func homeHasAtMostTenCoreRows() {
        #expect(SettingsCatalog.coreLimit == 10)
        #expect(SettingsCatalog.core.count <= SettingsCatalog.coreLimit, "Settings home: \(SettingsCatalog.core)")
    }

    @Test func everyFormerHomeRowKeepsExactlyOneHome() {
        let listed = SettingsCatalog.core + SettingsCatalog.advanced
        for id in Self.formerHome {
            #expect(listed.filter { $0 == id }.count == 1, "\(id) should be on Settings home or under Advanced, once")
        }
        #expect(Set(SettingsCatalog.core).isDisjoint(with: SettingsCatalog.advanced))
    }

    /// The lists name catalog entries (Alarms only where AlarmKit is, Live Screen only while the screen-share module is
    /// active, #14017), so their rows have a title, a page and aliases.
    @Test func everyListedIDIsACatalogEntry() {
        let ids = Set(SettingsCatalog.entries.map(\.id))
        for id in SettingsCatalog.core + SettingsCatalog.advanced
        where (id != "alarms" || AlarmsAccess.isSupported) && (id != "live-screen" || Modules.registry.isActive("screen-share")) {
            #expect(ids.contains(id), "\(id) is listed but not in SettingsCatalog.entries")
        }
    }

    @Test func eachEntrysTierIsTheListItIsIn() {
        for entry in SettingsCatalog.entries {
            switch entry.tier {
            case .core: #expect(SettingsCatalog.core.contains(entry.id))
            case .advanced: #expect(SettingsCatalog.advanced.contains(entry.id))
            case .nested: #expect(!SettingsCatalog.core.contains(entry.id) && !SettingsCatalog.advanced.contains(entry.id))
            }
        }
        // Pages reached through another page stay out of both lists; search still finds them.
        for id in ["triage", "standing-authorizations", "agent-follow-up", "claude", "codex", "agy", "themeColor", "style", "layout"] {
            #expect(SettingsCatalog.entries.first { $0.id == id }?.tier == .nested, "\(id)")
        }
    }

    @Test func searchFindsEveryAdvancedEntryByItsNameAndAnAlias() {
        for entry in SettingsCatalog.entries where entry.tier == .advanced {
            #expect(SettingsCatalog.search(entry.title).contains { $0.id == entry.id }, "\(entry.id) by its name")
            if let word = entry.aliases.split(whereSeparator: \.isWhitespace).first {
                #expect(SettingsCatalog.search(String(word)).contains { $0.id == entry.id }, "\(entry.id) by \(word)")
            }
        }
    }

    /// The rows keep the identifiers they had on Settings home, which UI tests and people's automations find them by.
    @Test func rowsKeepTheirIdentifiers() {
        let ids = Dictionary(uniqueKeysWithValues: SettingsCatalog.entries.map { ($0.id, $0.rowID) })
        let expected = ["models": "settings-models", "connectors": "settings.connectors", "computers": "settings-machines",
                        "hubs": "settings.hubs", "hub": "settings.hub", "managers": "settings.managers", "devices": "settings-pairing",
                        "persona": "settings-persona", "appearance": "settings-appearance", "agents": "settings-agents",
                        "knowledgeSelection": "settings-knowledge-selection", "phoneData": "settings-phone-data"]
        for (id, row) in expected { #expect(ids[id] == row, "\(id)") }
    }

    @Test func moduleRowsAndSearchFollowThePublishingHub() throws {
        let hub = FakeHub()
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "settings-modules-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(ServerConfig(url: URL(string: "http://127.0.0.1:1")!, token: "synthetic"))
        let meetings = try #require(SettingsCatalog.entries.first { $0.id == "meetings" })
        let health = try #require(SettingsCatalog.entries.first { $0.id == "health" })
        // No hello yet, and an older hub without the modules marker, keep today's entries.
        #expect(meetings.shows(in: model) && health.shows(in: model))
        for (features, expected) in [
            (["modules", "module.recording", "module.health"], true),
            (["modules"], false),
            (["file_upload"], true),
        ] {
            model.handle(.hello(HubHello(features: features)), group: "")
            #expect(meetings.shows(in: model) == expected)
            #expect(health.shows(in: model) == expected)
            #expect(SettingsCatalog.rows(.advanced, in: .assistant, model).contains { $0.id == "meetings" } == expected)
            #expect(SettingsCatalog.rows(.advanced, in: .device, model).contains { $0.id == "health" } == expected)
            #expect(SettingsCatalog.search(meetings.title, for: model).contains { $0.id == "meetings" } == expected)
            #expect(SettingsCatalog.search(health.title, for: model).contains { $0.id == "health" } == expected)
        }
    }

    /// In demo mode no hub is paired: its connection, version and assistants are left out; Avatar and Devices & Pairing stay.
    @Test func demoModeLeavesOutTheRowsThatNeedAPairedHub() throws {
        let hub = FakeHub()
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "settings-registry-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(.demo)
        #expect(model.isDemo)
        #expect(SettingsCatalog.rows(.core, in: .hub, model).isEmpty)
        #expect(SettingsCatalog.rows(.advanced, in: .hub, model).map(\.id) == ["persona", "devices"])
        #expect(SettingsCatalog.rows(.core, in: .device, model).map(\.id) == ["notifications", "about"])
    }

    /// Settings home uses the registry except for its declared Templates and More Settings sections. Between its marks,
    /// only those two custom links are allowed; every other row comes from the registry, except the demo's two buttons.
    @Test func homeDrawsOnlyTheRegistrysRows() throws {
        let source = try String(contentsOf: Self.settingsView, encoding: .utf8)
        let start = try #require(source.range(of: "// MARK: Settings home's rows"))
        let end = try #require(source.range(of: "// MARK: End of Settings home's rows"))
        let home = String(source[start.upperBound..<end.lowerBound])
        var registryRows = home
        for custom in ["SettingsListLink(value: SettingsPage.templates)", "SettingsListLink(value: SettingsPage.advanced)"] {
            #expect(home.components(separatedBy: custom).count - 1 == 1)
            registryRows = registryRows.replacingOccurrences(of: custom, with: "")
        }
        #expect(!registryRows.contains("NavigationLink(") && !registryRows.contains("SettingsListLink("),
                "a row on Settings home outside SettingsCatalog and its two declared custom sections")
        #expect(home.components(separatedBy: "SettingsCatalog.rows(.core,").count - 1 == 3)
        // `SettingsRow(` itself, not `ChecklistSettingsRow(` (the steps still to go, a prompt rather than a setting).
        let rows = try NSRegularExpression(pattern: #"(?<![A-Za-z])SettingsRow\("#)
        #expect(rows.numberOfMatches(in: home, range: NSRange(home.startIndex..., in: home)) == 4,
                "the demo's two buttons, Templates and More Settings")
    }
}
