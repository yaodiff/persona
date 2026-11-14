import XCTest

/// Real iPhone navigation against Fixtures/dispatch_hub.py: each dispatch names only its own tasks. A live Agent is
/// pushed on the Assistant stack, Back returns to the same conversation, and a task still starting opens its own
/// detail. PERSONA_MOCK_HUB supplies the loopback URL; PERSONA_SHOTS / PERSONA_SHOTS_APPEARANCE save light/dark evidence.
final class DispatchedTaskCardTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs Fixtures/dispatch_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", url, "-holdChromeOpen", "YES", "-ui.scheme", "system", "-tab", "assistant", "-dev.openChat", "",
                               "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]
        app.launchEnvironment["PERSONA_UI_TEST_SESSION"] = "1"
        app.launchPinned()
        XCTAssertTrue(app.buttons["action-bar.running"].waitForExistence(timeout: 20), app.debugDescription)
        return app
    }

    /// The icon confirmation is a SpringBoard window, not part of the app's accessibility tree. Dismiss only that
    /// informational confirmation; never automatically answer a permission request for this screenshot fixture.
    @MainActor private func dismissIconConfirmation() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let alert = springboard.alerts.firstMatch
        guard alert.waitForExistence(timeout: 1) else { return }
        let iconText = alert.staticTexts.matching(NSPredicate(format: "label CONTAINS '图标' OR label CONTAINS[c] 'icon'")).firstMatch
        guard iconText.exists else { return }
        for label in ["好", "OK"] where alert.buttons[label].exists {
            alert.buttons[label].tap()
            XCTAssertTrue(alert.waitForNonExistence(timeout: 5), "icon confirmation stayed over the screenshot")
            return
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        dismissIconConfirmation()
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/iphone-\(appearance)-\(name).png"))
    }

    /// The first card can sit under the Assistant tabs. `isHittable` is then true while a tap lands on the tab, so
    /// keep scrolling until the whole card is between the tab bar and the dock. Their frames come from the app.
    @MainActor private func link(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        let link = app.buttons["dispatch.task.\(id)"]
        let tabs = app.buttons["shell-assistant"]
        let dock = app.otherElements["action-bar"]
        let deadline = Date.now.addingTimeInterval(30)
        while Date.now < deadline {
            if link.exists {
                let frame = link.frame
                let top = tabs.exists ? tabs.frame.maxY : app.frame.minY
                let bottom = dock.exists ? dock.frame.minY : app.frame.maxY
                if link.isHittable, frame.minY >= top, frame.maxY <= bottom { return link }
                if frame.minY < top { app.swipeDown(velocity: .slow) } else { app.swipeUp(velocity: .slow) }
            }
            usleep(400_000)
        }
        XCTFail("no dispatch link for \(id):\n\(app.debugDescription)")
        return link
    }

    @MainActor private func assertPush(_ app: XCUIApplication, title: String, session: String) {
        let header = app.buttons.matching(identifier: "chat.title").firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10), app.debugDescription)
        let deadline = Date.now.addingTimeInterval(10)
        while !header.label.hasPrefix(title) && Date.now < deadline { usleep(200_000) }
        XCTAssertTrue(header.label.hasPrefix(title), "opened another Agent: \(header.label)")
        XCTAssertEqual(header.value as? String, session, "the rendered conversation is not the dispatched SessionKey")
        XCTAssertFalse(app.buttons["关闭"].exists, "opened a sheet instead of pushing")
        XCTAssertFalse(app.buttons["task-detail.fullscreen"].exists, "opened the task sheet")
        XCTAssertFalse(app.buttons["action-bar.running"].isHittable, "the Assistant dock shows through the pushed chat")
        XCTAssertFalse(app.otherElements["action-tray.running"].exists, "opened the entire In Progress tray")
    }

    @MainActor private func back(_ app: XCUIApplication) {
        let button = app.buttons["返回"].firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 5), app.debugDescription)
        button.tap()
        let running = app.buttons["action-bar.running"]
        let deadline = Date.now.addingTimeInterval(10)
        while !running.isHittable && Date.now < deadline { usleep(200_000) }
        XCTAssertTrue(running.isHittable, "Back did not return to the Assistant conversation:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons["关闭"].exists, "a sheet reopened after Back")
    }

    @MainActor func testStandaloneOpensItsAgentAndBackReturnsToAssistant() throws {
        let app = launch()
        let one = link(app, "todo_task_widget")
        // one card: the title, how the Agent came to it, when it last moved and its state, in one element
        XCTAssertTrue(one.label.contains("给天气 App 加主屏幕小组件"), one.label)
        XCTAssertFalse(one.label.contains("派出"), "the old \"Dispatched\" line is back: \(one.label)")
        try shot(app, "one")
        one.tap()
        assertPush(app, title: "给天气 App 加主屏幕小组件", session: "macbook/a1f3c2d4-0000-4000-8000-000000000001")
        sleep(2)
        try shot(app, "one-opened")
        back(app)
        XCTAssertTrue(app.buttons["dispatch.task.todo_task_widget"].exists)
        try shot(app, "one-back")
    }

    @MainActor func testPlanLinksAreIndependentAndStartingTaskOpensDetail() throws {
        let app = launch()
        let starting = link(app, "todo_dispatch_starting")
        starting.tap()
        XCTAssertTrue(app.staticTexts["正在开 Agent"].firstMatch.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.staticTexts["检查启动状态"].exists, "another task's detail opened")
        XCTAssertFalse(app.otherElements["action-tray.running"].exists)
        try shot(app, "starting")
        app.buttons["关闭"].firstMatch.tap()
        XCTAssertTrue(app.buttons["关闭"].firstMatch.waitForNonExistence(timeout: 5))

        let first = link(app, "todo_task_tests")
        XCTAssertTrue(first.label.contains("补齐网络层的单元测试"), first.label)
        XCTAssertEqual(app.buttons.matching(identifier: "dispatch.task.todo_task_tests").count, 1, "duplicate per-task feed link")
        XCTAssertEqual(app.buttons.matching(identifier: "dispatch.task.todo_task_release").count, 1)
        let unstarted = app.descendants(matching: .any)["dispatch.unstarted"].firstMatch
        XCTAssertTrue(unstarted.exists)
        XCTAssertTrue(unstarted.label.contains("未启动的计划任务") && unstarted.label.contains("Unavailable"), unstarted.label)
        XCTAssertFalse(app.buttons["dispatch.unstarted"].exists, "failed plan slot has an invented target")
        try shot(app, "many")
        first.tap()
        assertPush(app, title: "补齐网络层的单元测试", session: "macbook/a1f3c2d4-0000-4000-8000-000000000002")
        try shot(app, "many-first-opened")
        back(app)
        link(app, "todo_task_release").tap()
        assertPush(app, title: "整理发布说明", session: "macbook/a1f3c2d4-0000-4000-8000-000000000008")
        try shot(app, "many-second-opened")
        back(app)
    }
}
