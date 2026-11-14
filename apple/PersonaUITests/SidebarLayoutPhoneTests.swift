import XCTest

/// The Sidebar layout on an iPhone (#6332, docs/apple/ios.md「Sidebar layout (#6332)」): no Assistant | Agents switch; the
/// persona's face opens the drawer that holds everything (the main conversation, the pages, Persona's chats, the Agents);
/// a pick replaces the page and closes the drawer, and the page it opened has the face where a back would be. Against
/// scripts/mock/mock_hub.py (PERSONA_MOCK_HUB), on an iPhone simulator. TEST_RUNNER_PERSONA_SHOTS=<dir> keeps screenshots.
final class SidebarLayoutPhoneTests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private let agent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    /// The mock's main conversation: the shots open on it, as a fresh install does (an earlier test's New Chat stays).
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("iPhone only") }
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    /// The launch pins the Sidebar layout (`-app.navigation`), which the setting cannot change while it is pinned.
    @MainActor private func launch(_ extra: [String], layoutPinned: Bool = true) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // `-holdChromeOpen`: an Agent's chat keeps its header open (its title), as the other chat tests do.
        let layout = layoutPinned ? ["-app.navigation", "sidebar"] : []
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-holdChromeOpen", "YES"] + layout + extra
        app.launchEnvironment["PERSONA_UI_TEST_SESSION"] = "1" // the chat's title carries its session (`chat.title`'s value)
        app.launchPinned()
    }

    @MainActor private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    @MainActor private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("sidebar-phone-\(name).png"))
    }

    @MainActor private func settle() { usleep(1_500_000) }

    @MainActor private func waitGone(_ e: XCUIElement, _ step: String) {
        let gone = NSPredicate { _, _ in !e.exists || !e.isHittable }
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: gone, object: NSNull())], timeout: 10), .completed,
                       "\(step):\n\(app.debugDescription)")
    }

    /// Scrolls the drawer until `target` can be tapped. The drawer's host names the column, as the Separate layout's.
    @MainActor private func reach(_ target: XCUIElement, _ step: String) {
        let column = element("assistant-sidebar")
        for _ in 0..<10 where !(target.exists && target.isHittable) { column.swipeUp(velocity: .slow) }
        XCTAssertTrue(target.exists && target.isHittable, "\(step): not reached in the drawer:\n\(app.debugDescription)")
    }

    @MainActor func testTheDrawerHoldsEverythingAndAPickReplacesThePage() {
        launch(["-tab", "assistant"])
        let face = element("assistant-sidebar-button")
        XCTAssertTrue(face.waitForExistence(timeout: 20), "no face in the bar:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons["Agents"].firstMatch.exists, "the Assistant | Agents switch is still there")
        shot("home")

        // The face opens the drawer: the main conversation, the pages, Persona's chats.
        face.tap()
        let main = element("sidebar-main"), inbox = element("sidebar-inbox")
        XCTAssertTrue(inbox.waitForExistence(timeout: 10) && inbox.isHittable, "the drawer did not open:\n\(app.debugDescription)")
        XCTAssertLessThan(main.frame.minY, inbox.frame.minY)
        XCTAssertTrue(element("sidebar-fold-chats").exists)

        // Inbox replaces the conversation; the drawer goes; the face is where the back would be.
        inbox.tap()
        XCTAssertTrue(element("inbox.more").waitForExistence(timeout: 10), "no inbox:\n\(app.debugDescription)")
        waitGone(inbox, "the drawer stayed out over the inbox")
        let drawerButton = element("sidebar-drawer-button")
        XCTAssertTrue(drawerButton.waitForExistence(timeout: 5), "no face on the inbox's bar:\n\(app.debugDescription)")
        shot("inbox")

        // From there the drawer again, and an Agent: its chat replaces the inbox.
        drawerButton.tap()
        XCTAssertTrue(inbox.waitForExistence(timeout: 10) && inbox.isHittable)
        reach(element("sidebar-agents-new"), "the Agents' heading")
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        reach(row, "an Agent's row")
        row.tap()
        let title = app.buttons["chat.title"].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 10), "no Agent's chat:\n\(app.debugDescription)")
        waitGone(element("inbox.more"), "the inbox is still under the Agent's chat")
        XCTAssertTrue(element("sidebar-drawer-button").exists, "the chat has a back instead of the face")
        shot("agent")

        // The face on the chat opens the drawer; Persona's row goes back to the conversation.
        element("sidebar-drawer-button").tap()
        XCTAssertTrue(main.waitForExistence(timeout: 10) && main.isHittable, "the drawer did not open from the chat")
        main.tap()
        waitGone(title, "the Agent's chat stayed over the conversation")
        XCTAssertTrue(face.waitForExistence(timeout: 10))
    }

    /// An Agent's chat asked for from outside (a notification, a Live Activity, a link): the page, with the face.
    @MainActor func testAnAgentsChatFromOutsideIsThePage() {
        launch(["-tab", "dev", "-openSession", agent])
        let title = app.buttons["chat.title"].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 20), "no chat:\n\(app.debugDescription)")
        XCTAssertEqual(title.value as? String, agent)
        XCTAssertTrue(element("sidebar-drawer-button").exists, "the chat has a back instead of the face")
    }

    /// Settings › Appearance › Navigation on a phone: each choice applies at once and Settings stays on its page; the
    /// Sidebar layout's drawer has Settings at its foot.
    @MainActor func testTheChoiceAppliesAtOnce() {
        launch(["-tab", "settings", "-openAppearance", "navigation"], layoutPinned: false)
        let separate = element("navigation-layout-separate"), sidebar = element("navigation-layout-sidebar")
        XCTAssertTrue(separate.waitForExistence(timeout: 20), "no Navigation page:\n\(app.debugDescription)")
        func pick(_ choice: XCUIElement, _ step: String) {
            choice.tap()
            let chosen = NSPredicate { _, _ in choice.exists && choice.isSelected }
            XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: chosen, object: NSNull())], timeout: 10),
                           .completed, "\(step): not chosen, or Settings closed:\n\(app.debugDescription)")
        }
        pick(sidebar, "Sidebar")
        closeSettings()
        let face = element("assistant-sidebar-button")
        XCTAssertTrue(face.waitForExistence(timeout: 10), "no face in the bar:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons["Agents"].firstMatch.exists, "the Assistant | Agents switch is still there")
        face.tap()
        let settings = element("sidebar-settings")
        XCTAssertTrue(settings.waitForExistence(timeout: 10) && settings.isHittable, "no Settings in the drawer:\n\(app.debugDescription)")
        settings.tap()
        // Closed from its first page (above), Settings opens there again, as over the shell: Appearance › Navigation.
        let appearance = element("settings-appearance"), navigation = element("settings-navigation")
        XCTAssertTrue(appearance.waitForExistence(timeout: 10), "Settings did not open from the drawer:\n\(app.debugDescription)")
        appearance.tap()
        XCTAssertTrue(navigation.waitForExistence(timeout: 10), "no Navigation row:\n\(app.debugDescription)")
        for _ in 0..<6 where !navigation.isHittable { app.swipeUp(velocity: .slow) }
        navigation.tap()
        XCTAssertTrue(separate.waitForExistence(timeout: 10), "Settings is not on Navigation:\n\(app.debugDescription)")
        pick(separate, "Separate")
        closeSettings()
        XCTAssertTrue(app.buttons["Agents"].firstMatch.waitForExistence(timeout: 10), "the switch did not come back:\n\(app.debugDescription)")
    }

    /// Settings' × is on its first page, as over the shell: back from Navigation (and Appearance) first.
    @MainActor private func closeSettings() {
        let close = app.buttons["settings-close"].firstMatch, back = app.navigationBars.buttons["BackButton"].firstMatch
        for _ in 0..<3 where !close.exists && back.exists {
            back.tap()
            _ = close.waitForExistence(timeout: 2)
        }
        XCTAssertTrue(close.waitForExistence(timeout: 10), "no × on Settings:\n\(app.debugDescription)")
        close.tap()
        waitGone(close, "Settings stayed")
    }

    /// Screenshots for review (#6332): light and dark, the default text size and xxxLarge (where the interface stops growing,
    /// #6448): the conversation, the drawer
    /// at its top, the drawer at the Agents' Needs You (its foot's jump), an Agent's chat.
    @MainActor func testShots() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_SHOTS"] != nil else { throw XCTSkip("screenshots only") }
        for look in ["light", "dark"] {
            for size in ["default", "xxxlarge"] {
                var extra = ["-ui.scheme", look]
                if size == "xxxlarge" { extra += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryXXXL"] }
                launch(["-tab", "assistant", "-openAssistant", main] + extra)
                XCTAssertTrue(element("assistant-sidebar-button").waitForExistence(timeout: 20), "\(look) \(size): no bar")
                settle()
                dismissIconNotice()
                shot("\(look)-\(size)-home")
                element("assistant-sidebar-button").tap()
                settle()
                shot("\(look)-\(size)-drawer")
                let jump = element("sidebar-needs-jump")
                if jump.exists {
                    jump.tap()
                    settle()
                    shot("\(look)-\(size)-drawer-agents")
                }
                app.terminate()
                launch(["-tab", "dev", "-openSession", agent] + extra)
                XCTAssertTrue(app.buttons["chat.title"].firstMatch.waitForExistence(timeout: 20))
                settle()
                dismissIconNotice()
                shot("\(look)-\(size)-agent")
                app.terminate()
            }
            launch(["-tab", "settings", "-openAppearance", "navigation", "-ui.scheme", look])
            XCTAssertTrue(element("navigation-layout-separate").waitForExistence(timeout: 20), "\(look): no Navigation page")
            settle()
            dismissIconNotice()
            shot("\(look)-setting")
            app.terminate()
        }
    }

    /// Changing the look switches the app icon, and the system says so over the screen (`ChatBottomGapTests`).
    @MainActor private func dismissIconNotice() {
        let notice = NSPredicate(format: "label CONTAINS[c] 'icon' OR label CONTAINS '图标'")
        for source in [app!, XCUIApplication(bundleIdentifier: "com.apple.springboard")] {
            let alert = source.alerts.containing(notice).firstMatch
            if alert.waitForExistence(timeout: 2), alert.buttons.firstMatch.exists {
                alert.buttons.firstMatch.tap()
                settle()
                return
            }
        }
    }
}
