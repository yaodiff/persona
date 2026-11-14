import Foundation
import Testing
@testable import Persona

/// Edges of the Inbox Back rules beyond `InboxBackNavigationTests`: an unpaired app (no current hub) and an unknown main
/// conversation must both keep the old destinations. A waiting card pushes only onto its own hub's stack, unchanged.
@MainActor
struct InboxBackNavigationEdgeTests {
    private static let main = SessionKey(machineID: "server", sessionID: "main")
    private static let other = SessionKey(machineID: "server", sessionID: "other")

    /// No hub read yet (`main` nil): nothing can be known as the app's own choice, so no page is kept.
    @Test func anUnknownMainConversationKeepsNothing() {
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: nil, new: Self.main, main: nil))
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: nil, new: Self.other, main: nil))
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: nil, new: nil, main: nil))
    }

    /// Choosing the conversation already shown is not the automatic pick either: no page is kept.
    @Test func anUnchangedCurrentConversationKeepsNothing() {
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: Self.main, new: Self.main, main: Self.main))
        #expect(!AssistantCurrentNavigation.keepsPushedPages(old: Self.other, new: Self.other, main: Self.main))
    }

    /// Before the first hub answers there is no group to compare: a card opens as it did, nothing is pushed.
    @Test func waitingRowsWithoutACurrentHubKeepTheOldOpen() {
        let key = SessionKey(machineID: "server", sessionID: "agent")
        var pushed: [InboxChatRoute] = []
        var opened: [SessionKey] = []
        InboxWaitingOpen.session(key, currentHub: nil, push: { pushed.append($0) }, open: { opened.append(key) })
        #expect(pushed.isEmpty)
        #expect(opened == [key])
    }

    /// The pushed route is the card's own key, group and all: a session already read in its group is not moved to another.
    @Test func aWaitingRowPushesItsKeyUnchanged() {
        let key = SessionKey(group: "one", machineID: "server", sessionID: "agent")
        var pushed: [InboxChatRoute] = []
        InboxWaitingOpen.session(key, currentHub: "one", push: { pushed.append($0) }, open: { Issue.record("the current hub's card must push") })
        #expect(pushed == [InboxChatRoute(key: key, manager: nil)])
    }
}
