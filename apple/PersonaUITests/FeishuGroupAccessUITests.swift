import XCTest

/// Settings › Connectors › Lark Bot › Group Access (#16848), against scripts/mock/mock_hub.py with MOCK_BOT_GROUP=allowlist:
/// the section is on the Lark Bot page, with the mode in force and a guest's read-only tool. What an edit sends is
/// covered by FeishuGroupAccessModelTests; a row's menu reads as a pop-up button, so the screen is checked by its switch.
///
/// `TEST_RUNNER_PERSONA_GROUP_ACCESS_HUB=http://127.0.0.1:<port>`; `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots,
/// named by `TEST_RUNNER_PERSONA_SHOTS_APPEARANCE` (light / dark).
final class FeishuGroupAccessUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }

    @MainActor func testGroupAccessSection() throws {
        guard let hub = env["PERSONA_GROUP_ACCESS_HUB"] else {
            throw XCTSkip("needs mock_hub.py with MOCK_BOT_GROUP=allowlist (PERSONA_GROUP_ACCESS_HUB)")
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-showGroupAccess", "YES", "-dev.openChat", ""]
        app.launchPinned()
        XCTAssertTrue(app.navigationBars["飞书机器人"].waitForExistence(timeout: 20), "no 飞书机器人:\n\(app.debugDescription)")
        let heading = app.staticTexts["群聊访问"]
        scroll(app, to: heading)
        XCTAssertTrue(heading.waitForExistence(timeout: 5), "no 群聊访问:\n\(app.debugDescription)")
        // The heading can be only just on screen; one more swipe brings the section up where the shot shows it.
        app.swipeUp()
        shot(app, "section")
        // The read-only tool the guest may use, labelled by its connector.
        let tool = app.switches["Wiki · wiki_search"]
        scroll(app, to: tool)
        XCTAssertTrue(tool.exists, "no guest tool switch:\n\(app.debugDescription)")
    }

    /// A grouped list builds a row only once it is on screen, so scroll until it is.
    @MainActor private func scroll(_ app: XCUIApplication, to el: XCUIElement) {
        for _ in 0..<8 where !el.exists { app.swipeUp() }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let look = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "group-access-\(name)-\(look).png"))
    }
}
