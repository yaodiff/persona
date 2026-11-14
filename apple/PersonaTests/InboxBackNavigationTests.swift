import Foundation
import SwiftUI
import Testing
@testable import Persona

/// The navigation decisions behind Inbox Back: a pushed route carries the item's manager, every nil-push path keeps its
/// previous destination, automatic main-chat selection preserves pushed pages, and a real hub-group change clears them.
@MainActor @Suite(.serialized)
struct InboxBackNavigationTests {
    private static let main = SessionKey(machineID: "server", sessionID: "main")
    private static let other = SessionKey(machineID: "server", sessionID: "other")

    private func item(manager: String?) throws -> InboxItem {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        return try JSON.decoder.decode(InboxItem.self, from: Data(#"""
        {"id":"mail:a1:ci:77","source":"mail","kind":"mail","account":"a1","account_name":"me@example.com",
         "title":"Build failed","preview":"","count":1\#(stamp)}
        """#.utf8))
    }

    private func keepingDefaults(_ body: () throws -> Void) rethrows {
        let keys = ["assistant.current", "dev.openChat", "app.tab"]
        let saved = keys.map { (key: $0, value: UserDefaults.standard.object(forKey: $0)) }
        defer { for entry in saved { UserDefaults.standard.set(entry.value, forKey: entry.key) } }
        try body()
    }

    @Test func automaticMainSelectionKeepsPagesButUserSelectionsDoNot() {
        #expect(AssistantCurrentNavigation.keepsPushedPages(old: nil, new: Self.main, main: Self.main))
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: nil, new: Self.other, main: Self.main))
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: Self.other, new: Self.main, main: Self.main))
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: nil, new: nil, main: nil))
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: nil, new: nil, main: Self.main))
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: nil, new: Self.main, main: nil))
    }

    @Test func onlyARealHubGroupChangeClearsPages() {
        #expect(AssistantCurrentNavigation.clearsPushedPages(oldHub: "one", newHub: "two"))
        #expect(!AssistantCurrentNavigation.clearsPushedPages(oldHub: nil, newHub: "one"))
        #expect(!AssistantCurrentNavigation.clearsPushedPages(oldHub: "one", newHub: nil))
        #expect(!AssistantCurrentNavigation.clearsPushedPages(oldHub: "one", newHub: "one"))
        #expect(!AssistantCurrentNavigation.clearsPushedPages(oldHub: nil, newHub: nil))
    }

    /// Both waiting-Agent rows (the inbox list and the new-chat page) push only a session of the current hub, under its
    /// own group. Another group's card keeps model.open, which selects that group for an assistant conversation.
    @Test func waitingAgentRowsPushOnlyWithinTheCurrentHub() {
        let key = SessionKey(group: "one", machineID: "server", sessionID: "agent")
        var pushed: [InboxChatRoute] = []
        var opened: [SessionKey] = []
        InboxWaitingOpen.session(key, currentHub: "one", push: { pushed.append($0) }, open: { opened.append(key) })
        #expect(pushed == [InboxChatRoute(key: key, manager: nil)])
        #expect(opened.isEmpty)

        let other = SessionKey(group: "two", machineID: key.machineID, sessionID: key.sessionID)
        InboxWaitingOpen.session(other, currentHub: "one", push: { pushed.append($0) }, open: { opened.append(other) })
        #expect(pushed.count == 1 && opened == [other], "a foreign group's route must not be pushed on this hub")

        InboxWaitingOpen.session(key, currentHub: "one", push: nil, open: { opened.append(key) })
        #expect(opened == [other, key], "no closure keeps the old open even on the current hub")
    }

    /// The direct Thread row and a to-do sheet's Agent row use the same route and the item's manager.
    @Test func threadAgentRowsPushTheItemsManagerOrKeepTheTodoFallback() {
        keepingDefaults {
            let model = AppModel()
            model.openChat = nil
            var pushed: [InboxChatRoute] = []
            InboxLineOpen.agent(Self.other, owner: "m2", model: model) { pushed.append($0) }
            #expect(pushed == [InboxChatRoute(key: model.inHub(Self.other), manager: "m2")])
            #expect(model.openChat == nil)

            InboxLineOpen.agent(Self.other, owner: "m2", model: model)
            #expect(model.selectedTab == .chats && model.openChat == model.inHub(Self.other))
        }
    }

    @Test func connectorSummaryPushesItsManagerOrUsesTheOriginalAssistantOpen() {
        keepingDefaults {
            let model = AppModel()
            let current = model.assistantCurrent, reveal = model.assistantReveal
            var pushed: [InboxChatRoute] = []
            LarkChatView.openSummary(Self.other, owner: "m2", model: model) { pushed.append($0) }
            #expect(pushed == [InboxChatRoute(key: model.inHub(Self.other), manager: "m2")])
            #expect(model.assistantCurrent == current && model.assistantReveal == reveal)

            LarkChatView.openSummary(Self.other, owner: "m2", model: model)
            #expect(model.assistantCurrent == Self.other && model.selectedTab == .assistant)
            #expect(model.assistantReveal == reveal + 1)
            #expect(model.assistantItemManagers[Self.other] == nil, "the old fallback did not name an item manager")
        }
    }

    @Test func handOffViewPushesEveryTargetAndKeepsItsFallback() throws {
        try keepingDefaults {
            let model = AppModel(), item = try item(manager: "m2")
            let agent = SessionKey(machineID: "computer", sessionID: "agent")
            let targets: [InboxHandOffModel.Target] = [.main, .newChat, .agent(agent)]
            for target in targets {
                var pushed: [InboxChatRoute] = []
                var fallback = 0
                InboxHandOffBar.open(sent: (target, Self.other), item: item, model: model,
                                     push: { pushed.append($0) }, fallback: { fallback += 1 })
                #expect(pushed == [InboxChatRoute(key: model.inHub(Self.other), manager: "m2")])
                #expect(fallback == 0)
            }

            var fallback = 0
            for target in targets {
                InboxHandOffBar.open(sent: (target, Self.other), item: item, model: model,
                                     push: nil, fallback: { fallback += 1 })
            }
            #expect(fallback == targets.count)
            InboxHandOffBar.open(sent: nil, item: item, model: model,
                                 push: { _ in Issue.record("nothing sent must not push") }, fallback: { fallback += 1 })
            #expect(fallback == targets.count + 1)
        }
    }

    @Test func inboxPlaceRestoresItsPathIncludingThePushedChat() throws {
        var places = ShellPlaces()
        _ = places.enter(.inbox, from: NavigationPath())
        var path = NavigationPath([ShellRoute.inbox])
        path.append(try item(manager: "m2"))
        path.append(InboxChatRoute(key: Self.other, manager: "m2"))

        _ = places.enter(.knowledge, from: path)
        #expect(places.current == .knowledge && places.keeps(.inbox))
        let restored = places.enter(.inbox, from: NavigationPath([ShellRoute.knowledge]))
        #expect(restored == path, "the inbox's saved path must include its item and InboxChatRoute")
    }

    @Test func newAgentCompletionPushesTheAgentRecordedByStarted() throws {
        try keepingDefaults {
            let app = AppModel(), item = try item(manager: "m2")
            let agent = SessionKey(machineID: "computer", sessionID: "started")
            var fallback = 0
            let handOff = InboxHandOffModel(itemID: item.id, deps: .init(
                load: { _ in item },
                handOff: { _, _, _ in agent },
                record: { _, _, _, _, _ in },
                showMain: { _, _, _ in },
                plainToMain: { _ in agent },
                startNew: { _ in agent },
                sendToAgent: { _, _, _ in .sent },
                open: { _, _ in fallback += 1 },
                markDone: {}))
            // The sheet records the started Agent before openSent reads it; this order is part of the navigation contract.
            handOff.started(agent, markDone: false)
            var pushed: [InboxChatRoute] = []
            InboxHandOffBar.open(sent: handOff.sent, item: item, model: app,
                                 push: { pushed.append($0) }, fallback: handOff.open)
            #expect(handOff.sent?.target == .agent(agent))
            #expect(pushed == [InboxChatRoute(key: app.inHub(agent), manager: "m2")])
            #expect(fallback == 0)

            InboxHandOffBar.open(sent: handOff.sent, item: item, model: app, push: nil, fallback: handOff.open)
            #expect(fallback == 1)
        }
    }
}
