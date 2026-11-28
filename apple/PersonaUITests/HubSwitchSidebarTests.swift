import XCTest

/// Another hub made current while the sidebar is closed (#11657): opened again, the sidebar lists the new hub's Persona
/// chats and, with the Agents list narrowed to one hub, the new hub's Agents. Two groups of hubs against two
/// scripts/mock/mock_hub.py, each with data of its own: Home (A) with the mock's titles (「帮我约周五和韩梅梅吃饭」 in Recent,
/// 「服务器 · Home」), Work (B) with the hub's titles (`MOCK_TITLES=1`: 「1031 | 功能 | 约周五和韩梅梅吃饭」) and its own
/// computer names (「Work server · Work」). Start them as docs/operations/testing.md「Switching hubs」 says and pass
/// TEST_RUNNER_PERSONA_HUB_A / _B and TEST_RUNNER_PERSONA_HUB_B_TITLES=1; TEST_RUNNER_PERSONA_SHOTS=<directory> keeps the
/// screenshots, TEST_RUNNER_PERSONA_SCHEME light or dark.
final class HubSwitchSidebarTests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var a = "", b = ""
    private static let homeChat = "帮我约周五和韩梅梅吃饭", workChat = "1031 | 功能 | 约周五和韩梅梅吃饭"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("iPhone: the sidebar is a drawer over the page") }
        guard let a = env["PERSONA_HUB_A"], let b = env["PERSONA_HUB_B"] else {
            throw XCTSkip("needs two scripts/mock/mock_hub.py (PERSONA_HUB_A, PERSONA_HUB_B)")
        }
        guard env["PERSONA_HUB_B_TITLES"] == "1" else { throw XCTSkip("hub B with MOCK_TITLES=1 (PERSONA_HUB_B_TITLES=1)") }
        (self.a, self.b) = (a, b)
    }

    /// The Separate layout (the iPhone's own): Work made current in the sidebar's assistant switcher (#12594), then Home
    /// picked in the Agents page's own hub menu with the sidebar closed. Opened again, it lists the hub picked last.
    @MainActor func testSeparateLayoutSidebarFollowsTheHub() throws {
        let scheme = env["PERSONA_SCHEME"] ?? "light"
        let app = launch(layout: "separate", scheme: scheme)
        openSidebar(app)
        expectChats(of: "Home", app)
        shot(app, "separate-1-home-\(scheme)")

        pickHubInSwitcher("Work", app)
        expectChats(of: "Work", app)
        shot(app, "separate-2-work-\(scheme)")
        closeSidebar(app)

        // The Agents page's ⋯: one hub's agents only, that hub the current one (it was a filter of its own, and the
        // sidebar over the Agents page stayed on the hub before).
        let agents = app.buttons["shell-agents"].firstMatch
        XCTAssertTrue(agents.waitForExistence(timeout: 10), "no Agents in the switch:\n\(app.debugDescription)")
        agents.tap()
        sleep(1) // the slide
        pickHub("Home", menu: "shell-agents", app)
        shot(app, "separate-3-agents-page-\(scheme)")
        XCTAssertTrue(text(app, " · Home").waitForExistence(timeout: 10), "the Agents page does not show Home's agents:\n\(app.debugDescription)")
        XCTAssertTrue(text(app, " · Work").waitForNonExistence(timeout: 10), "Work's agents on the Agents page narrowed to Home:\n\(app.debugDescription)")
        openSidebar(app, button: "work-sidebar-button")
        expectChats(of: "Home", app)
        shot(app, "separate-3-agents-home-\(scheme)")
    }

    /// The Sidebar layout (Merged): the same drawer holds Persona's chats over the Agents list, here narrowed to Home in
    /// its own menu. Work made current in the sidebar's assistant switcher (#12594): both lists are Work's, and stay so
    /// when the sidebar is opened again. The conversation's menu has no hub section any more.
    @MainActor func testMergedLayoutSidebarFollowsTheHub() throws {
        let scheme = env["PERSONA_SCHEME"] ?? "light"
        let app = launch(layout: "sidebar", scheme: scheme)
        openSidebar(app)
        expectChats(of: "Home", app)
        pickHub("Home", menu: "sidebar-agents-menu", app) // the Agents heading's ⋯
        expectAgents(of: "Home", app)
        shot(app, "sidebar-1-home-\(scheme)")
        closeSidebar(app)
        expectNoHubInConversationMenu(app)

        openSidebar(app)
        pickHubInSwitcher("Work", app)
        expectChats(of: "Work", app)
        shot(app, "sidebar-2-work-\(scheme)")
        expectAgents(of: "Work", app)
        shot(app, "sidebar-3-work-agents-\(scheme)")
        closeSidebar(app)

        openSidebar(app)
        pickHubInSwitcher("Home", app)
        closeSidebar(app)
        openSidebar(app)
        expectChats(of: "Home", app)
        expectAgents(of: "Home", app)
    }

    // MARK: -

    /// The sidebar's Persona chats are `hub`'s: its chat in Recent, the other hub's not.
    @MainActor private func expectChats(of hub: String, _ app: XCUIApplication, line: UInt = #line) {
        let (mine, other) = hub == "Home" ? (Self.homeChat, Self.workChat) : (Self.workChat, Self.homeChat)
        // The Merged sidebar keeps where its list was scrolled to: Persona's chats are over the Agents.
        for _ in 0..<3 where !row(app, mine).waitForExistence(timeout: 5) && !row(app, other).exists { app.swipeDown() }
        XCTAssertTrue(row(app, mine).waitForExistence(timeout: 30), "\(hub)'s chat is not in the sidebar:\n\(app.debugDescription)", line: line)
        XCTAssertFalse(row(app, other).exists, "the other hub's chat is in \(hub)'s sidebar", line: line)
    }

    /// The Agents list (scrolled to) names `hub`'s computers only (「服务器 · Home」, 「Work server · Work」).
    @MainActor private func expectAgents(of hub: String, _ app: XCUIApplication, line: UInt = #line) {
        let other = hub == "Home" ? "Work" : "Home"
        for _ in 0..<3 where !text(app, " · \(hub)").exists && !text(app, " · \(other)").exists { app.swipeUp() }
        XCTAssertTrue(text(app, " · \(hub)").waitForExistence(timeout: 15), "no agents of \(hub) in the sidebar:\n\(app.debugDescription)", line: line)
        XCTAssertTrue(text(app, " · \(other)").waitForNonExistence(timeout: 10),
                      "\(other)'s agents in the sidebar after switching to \(hub):\n\(app.debugDescription)", line: line)
    }

    @MainActor private func text(_ app: XCUIApplication, _ s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    @MainActor private func launch(layout: String, scheme: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["--", "-pairURL", pair(a), "-pairURL2", pair(b), "-openHub", "1", "-tab", "assistant",
                               "-app.tab", "assistant", "-app.navigation", layout, "-ui.scheme", scheme,
                               "-openChatsFresh", "YES", "-openAgentsFresh", "YES", "-dev.openChat", ""]
        app.launchPinned()
        // A fresh install asks for notifications first, and the app waits inactive under it.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["不允许", "Don’t Allow", "Don't Allow"] where springboard.alerts.buttons[label].waitForExistence(timeout: 2) {
            springboard.alerts.buttons[label].tap()
            break
        }
        return app
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    /// A sidebar row, found by the start of its label (title, then its state and preview).
    @MainActor private func row(_ app: XCUIApplication, _ title: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", title)).firstMatch
    }

    @MainActor private func openSidebar(_ app: XCUIApplication, button id: String = "assistant-sidebar-button") {
        let button = app.buttons[id].firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 30), "no sidebar button:\n\(app.debugDescription)")
        button.tapCenter()
        // The page beside the open sidebar is a card whose tap closes it (the list itself may be scrolled anywhere).
        XCTAssertTrue(app.buttons["assistant-drawer-close"].firstMatch.waitForExistence(timeout: 10), "the sidebar did not open:\n\(app.debugDescription)")
        sleep(1) // the slide
    }

    /// A tap on the page beside the open sidebar closes it: the page's top bar is back (hidden from VoiceOver while out).
    @MainActor private func closeSidebar(_ app: XCUIApplication) {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        XCTAssertTrue(app.buttons["assistant-drawer-close"].firstMatch.waitForNonExistence(timeout: 10), "the sidebar did not close:\n\(app.debugDescription)")
        sleep(1) // the slide
    }

    /// `name` picked in a menu's hub section: `menu` the button that opens it (the switch's Assistant or Agents side, the
    /// conversation's name in the Merged layout, the Agents heading's ⋯ further down the Merged sidebar).
    @MainActor private func pickHub(_ name: String, menu: String, _ app: XCUIApplication) {
        let button = app.descendants(matching: .any).matching(identifier: menu).firstMatch
        // The sidebar's Agents heading, further down the Merged sidebar: a slow swipe at a time, not past it; back up
        // when the list was left scrolled below it.
        for _ in 0..<6 where !(button.exists && button.isHittable) { app.swipeUp(velocity: .slow) }
        for _ in 0..<8 where !(button.exists && button.isHittable) { app.swipeDown(velocity: .slow) }
        XCTAssertTrue(button.waitForExistence(timeout: 10), "no menu \(menu):\n\(app.debugDescription)")
        button.tap()
        let item = app.buttons[name].firstMatch
        // iOS creates the lower menu rows as its collection scrolls. Scroll the menu itself
        // until the hub is present and reachable on a short screen.
        let picker = app.collectionViews.firstMatch
        for _ in 0..<6 where !(item.exists && item.isHittable) {
            if picker.exists { picker.swipeUp(velocity: .slow) } else { app.swipeUp(velocity: .slow) }
        }
        XCTAssertTrue(item.exists && item.isHittable, "\(name) cannot be reached in the menu:\n\(app.debugDescription)")
        item.tap()
        sleep(2) // the new hub's lists
    }

    /// `name` picked in the open sidebar's header: the assistant switcher's hub group, or the hub menu under the plain
    /// title of a hub without a roster (#12594).
    @MainActor private func pickHubInSwitcher(_ name: String, _ app: XCUIApplication) {
        // The Merged sidebar keeps where its list was scrolled to: the header is at the top.
        // The drawer's identifier can cover the header's own on a phone (as in AssistantIdentityUITests).
        let switcher = app.buttons.matching(NSPredicate(format: "identifier == %@ OR (identifier == %@ AND label CONTAINS %@)",
                                                        "assistant-switcher", "assistant-sidebar", " · ")).firstMatch
        let plain = app.descendants(matching: .any).matching(identifier: "hub.switcher").firstMatch
        for _ in 0..<4 where !(switcher.exists && switcher.isHittable) && !(plain.exists && plain.isHittable) { app.swipeDown() }
        let item: XCUIElement
        if switcher.waitForExistence(timeout: 10) {
            switcher.tap()
            item = app.buttons["assistant-switcher-hub-\(name)"].firstMatch
            shot(app, "switcher-\(name)-\(env["PERSONA_SCHEME"] ?? "light")")
        } else {
            XCTAssertTrue(plain.waitForExistence(timeout: 5), "no switcher in the sidebar's header:\n\(app.debugDescription)")
            plain.tap()
            item = app.buttons[name].firstMatch
        }
        XCTAssertTrue(item.waitForExistence(timeout: 10), "no \(name) among the hubs:\n\(app.debugDescription)")
        item.tap()
        sleep(2) // the new hub's lists
    }

    /// The conversation's menu (its name in the Merged layout) is about the conversation: no hub to pick there (#12594).
    @MainActor private func expectNoHubInConversationMenu(_ app: XCUIApplication) {
        let button = app.descendants(matching: .any).matching(identifier: "persona-avatar").firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 10), "no conversation menu:\n\(app.debugDescription)")
        button.tap()
        XCTAssertTrue(app.buttons["Knowledge"].firstMatch.waitForExistence(timeout: 10)
                      || app.buttons["知识库"].firstMatch.waitForExistence(timeout: 2), "the menu did not open:\n\(app.debugDescription)")
        shot(app, "conversation-menu-\(env["PERSONA_SCHEME"] ?? "light")")
        for hub in ["Home", "Work"] {
            XCTAssertFalse(app.buttons[hub].exists, "\(hub) is still in the conversation's menu")
        }
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.9)).tap() // closes the menu
        sleep(1)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let s = XCTAttachment(screenshot: app.screenshot())
        s.name = name
        s.lifetime = .keepAlways
        add(s)
        if let dir = env["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
        }
    }
}
