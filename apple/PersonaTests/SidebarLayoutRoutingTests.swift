#if os(iOS)
import Foundation
import Testing
@testable import Persona

/// The Sidebar layout's routes (#6332, docs/apple/ios.md「Sidebar layout」): every route in the app still sets
/// `AppModel.selectedTab` (and `openChat` for an Agent's chat); the iPad's router lands each where the Separate layout's
/// tabs do, on the page beside the one sidebar. A route to Persona goes back to the conversation (an Agent or a place
/// may be over it), the inbox stays open after its own route, Settings is the sheet over what is in front.
@MainActor
struct SidebarLayoutRoutingTests {
    @Test func aRouteToPersonaGoesBackToTheConversation() {
        let model = AppModel()
        model.selectedTab = .chats
        let pad = PadRouter(unified: true)
        pad.attach(to: model)
        defer { pad.detach(from: model) }
        #expect(pad.request == nil)
        model.selectedTab = .assistant // a Persona notification, Hand to Persona…
        #expect(pad.request?.page == .conversation)
    }

    @Test func theInboxStaysOpenAfterItsRoute() {
        let model = AppModel()
        model.selectedTab = .chats
        let pad = PadRouter(unified: true)
        pad.attach(to: model)
        defer { pad.detach(from: model) }
        model.selectedTab = .mail
        #expect(pad.request?.page == .inbox, "the route to Persona that follows went back from the inbox")
        #expect(model.selectedTab == .assistant)
    }

    @Test func messagesOpenBesideTheSidebar() {
        let model = AppModel()
        model.selectedTab = .chats
        let pad = PadRouter(unified: true)
        pad.attach(to: model)
        defer { pad.detach(from: model) }
        model.selectedTab = .feishu
        #expect(pad.request?.page == .messages)
        #expect(model.selectedTab == .assistant)
    }

    @Test func settingsIsTheSheetOverWhatIsInFront() {
        let model = AppModel()
        model.selectedTab = .chats
        let pad = PadRouter(unified: true)
        pad.attach(to: model)
        defer { pad.detach(from: model) }
        model.selectedTab = .settings
        #expect(model.shellSettings)
        #expect(model.selectedTab == .chats)
        #expect(pad.request == nil, "Settings moved the page beside the sidebar")
    }

    /// An Agent's chat (a notification, a Live Activity, a link) is the Agents list's to open, as in the Separate layout:
    /// the router asks the Persona page for nothing.
    @Test func anAgentsChatIsTheAgentsListsToOpen() {
        let model = AppModel()
        model.selectedTab = .chats
        let pad = PadRouter(unified: true)
        pad.attach(to: model)
        defer { pad.detach(from: model) }
        let key = SessionKey(machineID: "m", sessionID: "s")
        model.open(key)
        #expect(model.openChat == key)
        #expect(model.selectedTab == .chats)
        #expect(pad.request == nil)
    }

    /// The Separate layout's router is as it was: Persona is a tab, so a route to it asks nothing of the page.
    @Test func theSeparateLayoutRoutesAsBefore() {
        let model = AppModel()
        model.selectedTab = .chats
        let pad = PadRouter()
        pad.attach(to: model)
        defer { pad.detach(from: model) }
        model.selectedTab = .assistant
        #expect(pad.request == nil)
        model.selectedTab = .mail
        #expect(pad.request?.page == .inbox)
        #expect(model.selectedTab == .assistant)
        model.selectedTab = .settings
        #expect(model.shellSettings)
        #expect(model.selectedTab == .assistant)
    }

    @Test func theChoiceAndThePlaces() {
        #expect(NavigationLayout.allCases == [.separate, .sidebar])
        #expect(NavigationLayout(rawValue: "sidebar") == .sidebar)
        #expect(NavigationLayout.key == "app.navigation")
        #expect(!NavigationLayout.separate.title.isEmpty && !NavigationLayout.sidebar.summary.isEmpty)
        // The places the sidebar lists are shaded while beside it; a conversation and New Chat are not places.
        #expect(ShellPage.inbox.isSidebarPlace && ShellPage.machines.isSidebarPlace && ShellPage.messages.isSidebarPlace)
        #expect(!ShellPage.conversation.isSidebarPlace && !ShellPage.newConversation.isSidebarPlace && !ShellPage.profile.isSidebarPlace)
    }
}
#endif
