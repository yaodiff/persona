import XCTest

/// Delete Chat (issue #1749) against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB): 「菜谱网站加搜索」 on macbook is not running.
/// The chat's confirmation (`-showDeleteSession YES` opens it, as 「…」›「删除对话」 does) says what deleting does;
/// Cancel leaves it, Delete sends DELETE /api/v1/sessions/{mid}/{sid}, the chat goes and the list no longer has it.
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` also writes the screenshots there.
final class DeleteSessionUITests: XCTestCase {
    private var app: XCUIApplication!
    private let recipe = "a1f3c2d4-0000-4000-8000-000000000007"
    private var pair = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        let reset = expectation(description: "重置会话")
        URLSession.shared.dataTask(with: URL(string: hub + "/mock/reset")!) { _, _, _ in reset.fulfill() }.resume()
        wait(for: [reset], timeout: 5)
        app = XCUIApplication()
        pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", "macbook/" + recipe, "-showDeleteSession", "YES",
                               "-holdChromeOpen", "YES"]
        app.launchPinned()
        // A freshly installed app asks for notifications first; that system prompt would sit over the confirmation.
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.buttons["允许"]
        if allow.waitForExistence(timeout: 3) { allow.tap() }
    }

    @MainActor private func shot(_ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "delete-\(name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("delete-\(name).png"))
        }
    }

    @MainActor private func listed() async throws -> Bool {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        let (data, _) = try await URLSession.shared.data(from: URL(string: hub + "/api/v1/sessions")!)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let sessions = (body["sessions"] as? [[String: Any]]) ?? []
        return sessions.contains { $0["session_id"] as? String == recipe }
    }

    @MainActor private func confirmation() -> XCUIElement {
        let alert = app.alerts["删除这个对话？"]
        XCTAssertTrue(alert.waitForExistence(timeout: 20), "no confirmation:\n\(app.debugDescription)")
        XCTAssertTrue(alert.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "对话记录不会删除")).firstMatch.exists)
        return alert
    }

    @MainActor func testCancelKeepsTheConversation() async throws {
        let alert = confirmation()
        shot("confirm")
        alert.buttons["取消"].tap()
        XCTAssertFalse(alert.waitForExistence(timeout: 1))
        let stillListed = try await listed()
        XCTAssertTrue(stillListed)
    }

    @MainActor func testDeleteTakesItOutOfTheList() async throws {
        confirmation().buttons["删除"].tap()
        // The chat goes back to the list, which no longer has it.
        let title = app.staticTexts["菜谱网站加搜索"]
        let deadline = Date.now.addingTimeInterval(10)
        while title.exists && Date.now < deadline { try await Task.sleep(for: .milliseconds(300)) }
        XCTAssertFalse(title.exists, "still shown:\n\(app.debugDescription)")
        let stillListed = try await listed()
        XCTAssertFalse(stillListed)
        shot("after")
    }

    /// The Agents list offers it on a row's long press too, for one that is not running only: the list shows running
    /// and finished ones (「整理发布说明」 is done), not offline ones.
    @MainActor func testTheRowMenuOffersItOnlyWhenNotRunning() throws {
        // Straight to the list: no chat (an empty remembered chat), no confirmation. Not Back from the chat: #1868.
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-dev.openChat", "", "-holdChromeOpen", "YES"]
        app.launchPinned()
        let search = app.searchFields.firstMatch
        if !search.waitForExistence(timeout: 5) { app.swipeDown() }
        XCTAssertTrue(search.waitForExistence(timeout: 5), "no search:\n\(app.debugDescription)")
        search.tap()
        search.typeText("release") // its tab: typing Chinese into the field is not reliable in UI tests
        let row = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "整理发布说明")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "no row:\n\(app.debugDescription)")
        row.press(forDuration: 1.0)
        let delete = app.buttons["删除对话"]
        // The menu's last item, or More's while Close Agent is the last (#13396).
        if !delete.waitForExistence(timeout: 3) { app.openAgentMenuMore() }
        XCTAssertTrue(delete.waitForExistence(timeout: 5), "no 删除对话:\n\(app.debugDescription)")
        shot("row-menu")
        delete.tap()
        XCTAssertTrue(confirmation().exists)
        app.alerts.buttons["取消"].tap()
        // One at work does not offer it.
        search.buttons.firstMatch.tap() // clear
        search.typeText("orders")
        let working = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "排查 /orders 接口变慢")).firstMatch
        XCTAssertTrue(working.waitForExistence(timeout: 10), "no row:\n\(app.debugDescription)")
        working.press(forDuration: 1.0)
        XCTAssertTrue(app.buttons["置顶"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["删除对话"].exists)
        app.openAgentMenuMore()
        XCTAssertTrue(app.buttons["拷贝目录"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["删除对话"].exists)
    }
}
