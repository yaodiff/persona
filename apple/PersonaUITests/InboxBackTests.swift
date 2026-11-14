import XCTest

/// Inbox-linked conversations stay on the page that opened them. Back is always the system's slow left-edge drag, not a
/// toolbar button. Against scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 (PERSONA_MOCK_HUB); the wide tests need an
/// iPad or an open Duo. TEST_RUNNER_PERSONA_SHOTS=<dir> saves each step for visual acceptance.
final class InboxBackTests: XCTestCase {
    private static let itemID = "mail:a1:ci:77"
    private static let itemTitle = "构建失败：主分支"
    private var app: XCUIApplication!
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.lifetime = .keepAlways
            add(shot)
            print("INBOXBACKTREE-BEGIN\n\(app.debugDescription)\nINBOXBACKTREE-END")
        }
        super.tearDown()
    }

    @MainActor private func launch(_ extra: [String]) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-inbox.kind", "", "-inbox.source", ""] + extra
        app.launchPinned()
        acceptIconAlert()
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let look = env["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
        let url = URL(fileURLWithPath: dir).appendingPathComponent("inbox-back-\(name)\(look).png")
        try? app.screenshot().pngRepresentation.write(to: url)
    }

    @MainActor private func requirePhone() throws {
        guard app.windows.firstMatch.frame.width <= 600 else { throw XCTSkip("phone width only") }
    }

    @MainActor private func requireWide() throws {
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("needs an iPad or an open Duo") }
    }

    @MainActor private func openDetail(_ step: String, extra: [String] = []) {
        launch(["-tab", "mail", "-openInboxItem", Self.itemID] + extra)
        assertDetail(step)
    }

    @MainActor private func assertDetail(_ step: String) {
        let title = app.navigationBars[Self.itemTitle].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 15), "\(step): not the item detail:\n\(app.debugDescription)")
        XCTAssertTrue(app.descendants(matching: .any)["inbox.chain"].firstMatch.waitForExistence(timeout: 5),
                      "\(step): the detail's timeline is missing")
        XCTAssertTrue(chainRow("开了子会话").waitForExistence(timeout: 5),
                      "\(step): the detail's conversation rows are missing")
        shot(step)
    }

    @MainActor private func chainRow(_ text: String) -> XCUIElement {
        // The chain container may combine its children's accessibility elements, so accept either the row's or its id.
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", text))
            .matching(NSPredicate(format: "identifier == %@ OR identifier == %@", "inbox.chain", "inbox.chain.session")).firstMatch
    }

    @MainActor private func revealChainRow(_ text: String) -> XCUIElement {
        let row = chainRow(text)
        if !row.exists {
            let all = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "展开全部")).firstMatch
            for _ in 0..<5 where all.exists && !all.isHittable {
                app.swipeUp()
                _ = all.waitForExistence(timeout: 2)
            }
            if all.waitForExistence(timeout: 5) { all.tap() }
        }
        for _ in 0..<5 where !row.exists || !row.isHittable {
            app.swipeUp()
            _ = row.waitForExistence(timeout: 2)
        }
        XCTAssertTrue(row.exists && row.isHittable, "no reachable timeline row containing \(text):\n\(app.debugDescription)")
        return row
    }

    @MainActor private func openChat(_ row: XCUIElement, _ step: String) {
        row.tap()
        let composer = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 20), "\(step): the conversation did not open:\n\(app.debugDescription)")
        assertPushed(step)
        shot(step)
    }

    @MainActor private func assertPushed(_ step: String) {
        // A pushed ChatView draws its own back button rather than the navigation stack's BackButton.
        let back = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "chevron.left", "返回")).firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 5), "\(step): the conversation is not pushed over its opening page")
    }

    @MainActor private func openPlaceDrawer() {
        let button = app.buttons["shell-place-sidebar"].firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 5), "no sidebar button on the pushed place page")
        button.tap()
        XCTAssertTrue(app.descendants(matching: .any)["assistant-sidebar"].firstMatch.waitForExistence(timeout: 5),
                      "the drawer did not open over the pushed place")
    }

    @MainActor private func pickPlace(_ id: String, title: String? = nil) {
        app.buttons[id].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["assistant-sidebar"].firstMatch.waitForNonExistence(timeout: 5),
                      "the drawer did not close after \(id)")
        if let title {
            XCTAssertTrue(app.navigationBars[title].firstMatch.waitForExistence(timeout: 15), "no \(title) place")
        }
    }

    /// Back as a finger does it: an edge drag, held and slow, after the push has settled.
    @MainActor private func swipeBack() {
        sleep(1)
        let window = app.windows.firstMatch
        // iPad's split pops the detail from the window's left edge; a phone starts just inside that edge.
        let wide = window.frame.width > 600
        window.coordinate(withNormalizedOffset: CGVector(dx: wide ? 0.0 : 0.005, dy: 0.5))
            .press(forDuration: 0.1, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: wide ? 0.6 : 0.9, dy: 0.5)),
                   withVelocity: .slow, thenHoldForDuration: 0.1)
    }

    @MainActor private func assertWideSidebar(_ step: String) {
        let inbox = app.buttons["sidebar-inbox"].firstMatch
        XCTAssertTrue(inbox.waitForExistence(timeout: 5) && inbox.isHittable, "\(step): the wide sidebar is not still there")
        let title = app.navigationBars.staticTexts[Self.itemTitle].firstMatch
        XCTAssertGreaterThan(title.frame.minX, inbox.frame.maxX, "\(step): the detail is not beside the sidebar")
    }

    @MainActor func testPhoneTimelineChatsSwipeBackToTheItem() throws {
        openDetail("phone-detail")
        try requirePhone()

        openChat(revealChainRow("管理者处理"), "phone-handling-chat")
        swipeBack()
        assertDetail("phone-handling-back")

        openChat(revealChainRow("开了子会话"), "phone-agent-chat")
        swipeBack()
        assertDetail("phone-agent-back")
    }

    @MainActor func testWideTimelineChatsSwipeBackBesideTheSidebar() throws {
        openDetail("wide-detail")
        try requireWide()
        assertWideSidebar("wide-sidebar-initial")

        openChat(revealChainRow("管理者处理"), "wide-handling-chat")
        swipeBack()
        assertDetail("wide-handling-back")
        assertWideSidebar("wide-handling-sidebar")

        openChat(revealChainRow("开了子会话"), "wide-agent-chat")
        swipeBack()
        assertDetail("wide-agent-back")
        assertWideSidebar("wide-agent-sidebar")
    }

    @MainActor func testUnifiedSidebarTimelineChatsSwipeBackBesideTheSidebar() throws {
        openDetail("unified-detail", extra: ["-app.navigation", "sidebar"])
        try requireWide()
        assertWideSidebar("unified-sidebar-initial")

        openChat(revealChainRow("管理者处理"), "unified-handling-chat")
        swipeBack()
        assertDetail("unified-handling-back")
        assertWideSidebar("unified-handling-sidebar")
    }

    @MainActor func testInboxAgentRowSwipesBackToTheList() throws {
        launch(["-tab", "mail"])
        try requirePhone()
        let inbox = app.navigationBars["收件箱"].firstMatch
        XCTAssertTrue(inbox.waitForExistence(timeout: 20), "the inbox list did not open")
        let row = app.buttons["inbox.agentRow"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "no waiting Agent row:\n\(app.debugDescription)")
        shot("phone-inbox-list")

        openChat(row, "phone-inbox-agent-chat")
        swipeBack()
        XCTAssertTrue(inbox.waitForExistence(timeout: 10), "Back did not return to the inbox list")
        XCTAssertTrue(row.waitForExistence(timeout: 5), "the waiting Agent row is missing after Back")
        shot("phone-inbox-agent-back")
    }

    @MainActor func testHandOffViewSwipesBackToTheItem() throws {
        openDetail("handoff-detail")
        try requirePhone()
        let chip = app.buttons["handoff-chip-summarize"].firstMatch
        XCTAssertTrue(chip.waitForExistence(timeout: 15), "no Hand to AI chip")
        chip.tap()
        let view = app.buttons["toast-action"].firstMatch
        XCTAssertTrue(view.waitForExistence(timeout: 5), "no View action on the hand-off toast")
        view.tap()

        let composer = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 20), "View did not open the handed-off conversation")
        assertPushed("phone-handoff-chat")
        shot("phone-handoff-chat")
        swipeBack()
        assertDetail("phone-handoff-back")
    }

    @MainActor func testPhoneChatBackAndInboxPlaceRestoration() throws {
        openDetail("place-detail")
        try requirePhone()
        openChat(revealChainRow("开了子会话"), "place-chat")
        swipeBack()
        assertDetail("place-back-to-detail")

        // ChatView has no place-sidebar button yet. Exercise the drawer's save/restore from the inbox list instead.
        app.terminate()
        launch(["-tab", "mail"])
        try requirePhone()
        let inbox = app.navigationBars["收件箱"].firstMatch
        XCTAssertTrue(inbox.waitForExistence(timeout: 20), "the inbox list did not reopen")
        openPlaceDrawer()
        pickPlace("sidebar-知识库", title: "知识库")
        shot("place-knowledge")
        openPlaceDrawer()
        pickPlace("sidebar-inbox")
        XCTAssertTrue(inbox.waitForExistence(timeout: 15), "the inbox place was not restored")
        XCTAssertTrue(app.buttons["inbox.agentRow"].firstMatch.waitForExistence(timeout: 5),
                      "the restored inbox is not its list")
        shot("place-restored-inbox")
    }

    @MainActor func testNewChatAgentRowSwipesBackToNewChat() throws {
        launch(["-tab", "assistant", "-newAssistant", "YES"])
        try requirePhone()
        let row = app.buttons["shell-needs-agent"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "no waiting Agent on the new-chat page:\n\(app.debugDescription)")
        shot("phone-new-chat-needs-you")

        openChat(row, "phone-new-chat-agent-chat")
        swipeBack()
        XCTAssertTrue(row.waitForExistence(timeout: 10), "Back did not return to the new-chat page's waiting Agent")
        XCTAssertTrue(app.descendants(matching: .any)["shell-composer-field"].firstMatch.exists,
                      "the new-chat composer is missing after Back")
        shot("phone-new-chat-agent-back")
    }
}
