import Foundation
import SwiftUI
import Testing
@testable import Persona
import PersonaWire

/// Links into Settings from a page of one assistant's things open that assistant's copy (docs/assistant/managers.md
/// "The current assistant"): an extension page's settings link opens its own assistant's copy of the extension, Messages'
/// Settings the default assistant's chat connector (its chats follow that account), the voice entry the default's voice
/// extension (calls talk to it). A route that names none opens the current assistant's. With multiple assistants, a route for
/// the current assistant names none, including an explicit manager id; another assistant's route names its owner. With one
/// assistant or no roster, every route names none. Made-up values.
@MainActor
struct SettingsOwnerLinksTests {
    /// The default (`assistant`) and `work`.
    private static let roster = ManagerRoster(managers: [
        HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true),
        HubManager(id: "work", name: "工作", online: true),
    ], defaultID: "assistant")
    private static let one = ManagerRoster(managers: [HubManager(id: "assistant", name: "助手", online: true, isDefault: true)])

    /// A model on one made-up hub: no socket, its requests go to `hub`.
    private func model(_ hub: FakeHub = FakeHub(), roster: ManagerRoster? = roster) throws -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "settings-owner-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(.demo)
        model.managerRosters[""] = roster
        return model
    }

    /// Every Settings route whose destination is stamped with its owning assistant.
    private func ownedRoutes(manager: String?) -> [SettingsPage] {
        [
            .connector("github", manager: manager),
            .plugin("notes", manager: manager),
            .pluginPage(plugin: "notes", ui: "settings", title: "", manager: manager),
            .channel("chat", manager: manager),
            .subscriptions(state: "connector:github", manager: manager),
            .feishu(manager: manager),
        ]
    }

    @Test func theDefaultsPagesAreNamedByItsIDWhileAnotherIsCurrent() throws {
        let model = try model()
        #expect(model.settingsRoute(nil) == nil, "the default current: none named, as before")
        #expect(model.settingsRoute("assistant") == nil)
        #expect(model.settingsRoute("work") == "work", "another assistant's page keeps its owner")
        model.selectAssistantManager("work")
        #expect(model.settingsRoute(nil) == "assistant")
        #expect(model.settingsRoute("assistant") == "assistant", "another assistant's page keeps its owner")
        #expect(model.settingsRoute("work") == nil, "the current assistant's page names none")
        let one = try self.model(roster: Self.one)
        #expect(one.settingsRoute(nil) == nil)
        #expect(one.settingsRoute("assistant") == nil)
        let old = try self.model(roster: nil)
        #expect(old.settingsRoute(nil) == nil)
        #expect(old.settingsRoute("assistant") == nil)
    }

    @Test func explicitWorkRoutesBelongToCurrentWorkAssistantWhetherOnlineOrOffline() throws {
        for online in [true, false] {
            let roster = ManagerRoster(managers: [
                HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true),
                HubManager(id: "work", name: "工作", online: online),
            ], defaultID: "assistant")
            let model = try model(roster: roster)
            model.selectAssistantManager("work")
            if online {
                #expect(model.assistantManagerOffline == nil)
            } else {
                #expect(model.assistantManagerOffline?.id == "work", "the offline destination uses ManagerOfflineNote for its own routes")
            }
            for page in ownedRoutes(manager: "work") + ownedRoutes(manager: nil) {
                #expect(page.belongsToCurrentAssistant(in: model), "work is current (online: \(online)): \(page)")
            }
            for page in ownedRoutes(manager: "assistant") {
                #expect(!page.belongsToCurrentAssistant(in: model), "the default's explicit route is not current (work online: \(online)): \(page)")
            }
        }
    }

    @Test func ownedRoutesPreserveNilDefaultOtherSingleAndNoRosterBehavior() throws {
        let home = try model()
        for page in ownedRoutes(manager: nil) + ownedRoutes(manager: "assistant") {
            #expect(page.belongsToCurrentAssistant(in: home), "the default is current: \(page)")
        }
        for page in ownedRoutes(manager: "work") {
            #expect(!page.belongsToCurrentAssistant(in: home), "another assistant's route: \(page)")
        }

        for scoped in [try self.model(roster: Self.one), try self.model(roster: nil)] {
            for manager in [nil, "assistant", "work"] as [String?] {
                for page in ownedRoutes(manager: manager) {
                    #expect(page.belongsToCurrentAssistant(in: scoped), "one assistant or no roster: \(page)")
                }
            }
        }
    }

    /// An extension page's settings link opens its own assistant's copy of the extension: the current assistant's route names
    /// none and another assistant's route names its owner; a chat card's, the current one's.
    @Test func anExtensionsSettingsLinkOpensItsPagesAssistantsCopy() async throws {
        let model = try model()
        model.selectAssistantManager("work")
        let url = try #require(URL(string: "persona://settings/extensions/notes"))
        for (manager, want) in [("work", nil), (nil, "assistant")] as [(String?, String?)] {
            let hub = FakeHub()
            hub.on("POST", "/api/v1/plugins/notes/-/ui/home", json: #"{"open_url":"persona://settings/extensions/notes"}"#)
            let page = ExtensionViewModel(plugin: "notes", ui: "home", deps: .init(api: { hub.client }), manager: manager)
            let outcome = await page.perform(ExtensionAction(id: "settings"))
            #expect(outcome.url == url && outcome.settingsOwner == .page(manager))
            #expect(model.openIfExtensionSettings(url, from: outcome.settingsOwner))
            #expect(model.settingsPath == NavigationPath([SettingsPage.plugins, SettingsPage.plugin("notes", manager: want)]),
                    "a page of \(manager ?? "the default"): its own copy")
        }
        #expect(model.openIfExtensionSettings(url))
        #expect(model.settingsPath == NavigationPath([SettingsPage.plugins, SettingsPage.plugin("notes")]), "a chat card's: the current one's")

        // The default current: nothing named, as before.
        let home = try self.model()
        #expect(home.openIfExtensionSettings(url, from: .page(nil)))
        #expect(home.settingsPath == NavigationPath([SettingsPage.plugins, SettingsPage.plugin("notes")]))
    }

    /// Messages' Settings opens the chat connector page of the default assistant, whose account its chats follow; the
    /// Connectors row opens the current assistant's.
    @Test func messagesSettingsOpensTheDefaultsChatConnector() throws {
        let model = try model()
        model.selectAssistantManager("work")
        let page = SettingsPage.feishu(manager: model.settingsRoute(nil))
        #expect(page == SettingsPage.feishu(manager: "assistant"))
        #expect(!page.belongsToCurrentAssistant(in: model), "another assistant's page: not replaced while the current one is offline")
        #expect(SettingsPage.feishu().belongsToCurrentAssistant(in: model), "an unstamped route follows the current assistant")
        #expect(SettingsPage.feishu(manager: "work").belongsToCurrentAssistant(in: model))
        let home = try self.model()
        #expect(SettingsPage.feishu(manager: home.settingsRoute(nil)) == SettingsPage.feishu())
        #expect(SettingsPage.feishu().belongsToCurrentAssistant(in: home))

        // Its /agent model is the default's too; the Connectors row's page, the current assistant's.
        #expect(FeishuConnectorView.modelOwner(model.settingsRoute(nil), app: model) == .defaultAssistant)
        #expect(FeishuConnectorView.modelOwner(nil, app: model) == .current)
        #expect(FeishuConnectorView.modelOwner(home.settingsRoute(nil), app: home) == .current, "the default current: its own")
    }

    /// The voice entry opens the default assistant's voice extension, which calls talk to, whichever is current.
    @Test func theVoiceEntryOpensTheDefaultsVoiceExtension() throws {
        let model = try model()
        model.selectAssistantManager("work")
        VoiceEntry.open(.voiceExtension("voice"), model: model)
        #expect(model.settingsPath == NavigationPath([SettingsPage.plugins, SettingsPage.plugin("voice", manager: "assistant")]))
        let home = try self.model()
        VoiceEntry.open(.voiceExtension("voice"), model: home)
        #expect(home.settingsPath == NavigationPath([SettingsPage.plugins, SettingsPage.plugin("voice")]))
    }
}
