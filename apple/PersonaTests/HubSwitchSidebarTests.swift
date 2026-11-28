import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// Another hub made current (#11657): what the sidebar lists, Persona's chats and the Agents list narrowed to one hub,
/// is the current hub's, wherever the switch was made. Two groups ("" Home, "g1" Work), each with a Persona chat and an
/// Agent of its own on machines with the same ids.
@MainActor struct HubSwitchSidebarTests {
    private static func hub(_ id: String, group: String, port: Int) -> PairedHub {
        PairedHub(id: id, config: ServerConfig(url: URL(string: "http://127.0.0.1:\(port)")!, token: "t-\(id)"), group: group, hubID: id)
    }

    private static func session(_ json: String, group: String) throws -> Session {
        var s = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        s.group = group
        return s
    }

    /// Home and Work paired, Home current, each listing its Persona chat and an Agent.
    private func twoHubs(_ hub: FakeHub = FakeHub()) throws -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "hub-switch-sidebar-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.registry = HubRegistry(hubs: [Self.hub("home", group: "", port: 1), Self.hub("work", group: "g1", port: 2)])
        model.buildLinks()
        var byMachine: [MachineRef: [Session]] = [:]
        for (group, name) in [("", "Home"), ("g1", "Work")] {
            let chat = try Self.session(#"{"machine_id":"server","session_id":"chat-\#(name)","title":"\#(name) chat","assistant":true}"#,
                                        group: group)
            let agent = try Self.session(#"{"machine_id":"mac","session_id":"agent-\#(name)","title":"\#(name) agent"}"#, group: group)
            byMachine[chat.machineRef, default: []].append(chat)
            byMachine[agent.machineRef, default: []].append(agent)
        }
        model.applySessions(byMachine)
        return model
    }

    /// The Agents list as the sidebar shows it narrowed to one hub (`stored`, the menu's last pick): its rows' titles.
    private func agents(_ model: AppModel, stored: String) -> [String] {
        let shown = HubFilter.shown(stored, current: model.hub?.group)
        return model.allSessions.filter { !$0.assistant && HubFilter.matches(shown, $0.group) }.map(\.title).sorted()
    }

    @Test func aListNarrowedToOneHubShowsTheCurrentHub() {
        #expect(HubFilter.shown("=", current: "g1") == HubFilter.value("g1"))
        #expect(HubFilter.shown("=g1", current: "") == HubFilter.value(""))
        #expect(HubFilter.shown("=g1", current: "g1") == "=g1")
        #expect(HubFilter.shown("", current: "g1") == "", "every hub stays every hub")
        #expect(HubFilter.shown("=g1", current: nil) == "=g1", "nothing paired: the filter as it was")
    }

    @Test func afterASwitchTheSidebarListsTheNewHubsChatsAndAgents() throws {
        let model = try twoHubs()
        defer { for link in model.links { link.live.disconnect() } }
        let narrowed = HubFilter.value("") // the Agents list narrowed to Home, as its menu left it
        #expect(model.assistantConversations.map(\.title) == ["Home chat"])
        #expect(agents(model, stored: narrowed) == ["Home agent"])

        model.selectHub("g1") // the sidebar's assistant switcher, the Inbox or Settings › hub
        #expect(model.hub?.group == "g1")
        #expect(model.assistantConversations.map(\.title) == ["Work chat"])
        #expect(agents(model, stored: narrowed) == ["Work agent"], "the Agents list stayed on the hub before")
        #expect(agents(model, stored: "") == ["Home agent", "Work agent"], "All Hubs still lists both")

        model.selectHub("")
        #expect(model.assistantConversations.map(\.title) == ["Home chat"])
        #expect(agents(model, stored: HubFilter.value("g1")) == ["Home agent"])
    }

    /// A computer the Agents list was narrowed to stays only while it is one of the hub the list shows (`HubFilter.machine`).
    @Test func aComputerOfAnotherHubIsNotKept() {
        #expect(HubFilter.machine("mac", shown: HubFilter.value("g1")) == "", "Home's computer on a list of Work's")
        #expect(HubFilter.machine("g1|mac", shown: HubFilter.value("")) == "", "Work's computer on a list of Home's")
        #expect(HubFilter.machine("g1|mac", shown: HubFilter.value("g1")) == "g1|mac")
        #expect(HubFilter.machine("mac", shown: HubFilter.value("")) == "mac")
        #expect(HubFilter.machine("g1|mac", shown: "") == "g1|mac", "All Hubs keeps any computer")
        #expect(HubFilter.machine("", shown: HubFilter.value("g1")) == "", "every computer stays every computer")
    }

    /// A delayed response from the first visit to Home must not replace the current visit's settings.
    @Test func returningToAHubRejectsItsEarlierAssistantResponse() async throws {
        let hub = FakeHub()
        let model = try twoHubs(hub)
        defer { for link in model.links { link.live.disconnect() } }

        let started = AsyncStream<Void>.makeStream()
        let release = AsyncStream<Void>.makeStream()
        defer {
            started.continuation.finish()
            release.continuation.finish()
        }
        var first = true
        hub.onAsync("GET", "/api/v1/assistant") { _ in
            let launcher: String
            if first {
                first = false
                started.continuation.yield(())
                for await _ in release.stream { break }
                launcher = "old"
            } else {
                launcher = "current"
            }
            return Data(#"{"machine_id":"box","launcher":"\#(launcher)","cwd":"/","conversations":[],"timezone_picked":true}"#.utf8)
        }
        let pending = Task { await model.refreshAssistant() }
        for await _ in started.stream { break }
        model.selectHub("g1")
        model.selectHub("")
        await model.refreshAssistant()
        #expect(model.assistantConfig?.launcher == "current")
        release.continuation.yield(())
        await pending.value
        #expect(model.assistantConfig?.launcher == "current")
    }

    /// A computer of the hub before, stored while the list was not there to see the switch (another page in front, the
    /// app not running, an update from before the list followed the current hub): the list lets it go as it first
    /// shows, not only when its hub changes while it is there. Kept, the list read the hub before's agents and showed
    /// none (the review of #11721).
    @Test func aComputerOfTheHubBeforeIsLetGoWhenTheListFirstShows() async throws {
        let stale = FilterFixture.Filters(hub: HubFilter.value("g1"), machine: "mac") // Work current, Home's computer kept
        let first = try FilterFixture.host(stale)
        defer { first.isHidden = true }
        #expect(await FilterFixture.settles(stale, at: ""), "Home's computer still narrows the list of Work: \(stale.machine)")

        let own = FilterFixture.Filters(hub: HubFilter.value("g1"), machine: "g1|mac")
        let second = try FilterFixture.host(own)
        defer { second.isHidden = true }
        #expect(!(await FilterFixture.settles(own, at: "")), "Work's own computer let go")
        own.hub = HubFilter.value("") // Home made current while the list shows
        #expect(await FilterFixture.settles(own, at: ""), "Work's computer still narrows the list of Home: \(own.machine)")
    }

    /// The sidebar reads which chats the scheduled tasks started again for another hub, not only for another manager or
    /// another number of chats: a column that stays out (the iPad's) kept the hub before's.
    @Test func theTaskChatsAreReadAgainForAnotherHub() {
        let home = AssistantTaskChatsStore.readKey(out: true, group: "", manager: nil, conversations: 4)
        let work = AssistantTaskChatsStore.readKey(out: true, group: "g1", manager: nil, conversations: 4)
        #expect(home != work)
        #expect(AssistantTaskChatsStore.readKey(out: true, group: "", manager: "m-2", conversations: 4) != home)
        #expect(AssistantTaskChatsStore.readKey(out: true, group: "", manager: nil, conversations: 5) != home)
        #expect(AssistantTaskChatsStore.readKey(out: false, group: "g1", manager: nil, conversations: 4) == "", "nothing while it is in")
    }
}

/// `MachineFilterFollowsHub` alone, in a window of its own: the group shown and the computer stored, as the Agents list
/// holds them.
@MainActor private struct FilterFixture: View {
    @Observable final class Filters {
        var hub: String
        var machine: String
        init(hub: String, machine: String) { (self.hub, self.machine) = (hub, machine) }
    }

    let filters: Filters

    var body: some View {
        Color.clear.modifier(MachineFilterFollowsHub(hubFilter: filters.hub,
                                                     machineFilter: Binding(get: { filters.machine }, set: { filters.machine = $0 })))
    }

    static func host(_ filters: Filters) throws -> UIWindow {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 100, height: 100)
        window.rootViewController = UIHostingController(rootView: FilterFixture(filters: filters))
        window.isHidden = false
        window.layoutIfNeeded()
        return window
    }

    /// Whether the stored computer comes to `value` within a second.
    static func settles(_ filters: Filters, at value: String) async -> Bool {
        for _ in 0..<20 where filters.machine != value { try? await Task.sleep(for: .milliseconds(50)) }
        return filters.machine == value
    }
}
