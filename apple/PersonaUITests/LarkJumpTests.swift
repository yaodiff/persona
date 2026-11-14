import XCTest

/// Jump to Latest in a Feishu chat answers a real tap: scrolled away from the newest message, the arrow at the bottom right
/// brings it back, and goes away there. In the glass styles it once took no touches (its glass label had no hit shape),
/// and under the inbox's tall top bar it never went away (the at-bottom check left out the top inset).
/// Against scripts/mock_hub.py (PERSONA_MOCK_HUB); run it in both styles (TEST_RUNNER_PERSONA_UI_STYLE=beautiful).
final class LarkJumpTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func expandSource(_ app: XCUIApplication) {
        let source = app.buttons["inbox.detail.source"]
        XCTAssertTrue(source.waitForExistence(timeout: 20), "the original-source control did not appear")
        source.tap()
    }

    @MainActor func testJumpToLatestScrollsToTheNewestMessage() {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-openInboxItem", "lark:f3:300"]
        app.launchPinned()
        expandSource(app)
        let newest = app.staticTexts["周六早上 8 点地铁站集合，记得带水"]
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "这是这次的路线")).firstMatch.waitForExistence(timeout: 20),
                      "the 周末徒步群 chat did not open")
        let list = app.scrollViews.firstMatch
        for _ in 0..<3 where newest.exists && newest.isHittable { list.swipeDown() }
        XCTAssertFalse(newest.exists && newest.isHittable, "could not scroll away from the newest message")

        let jump = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "回到最新")).firstMatch
        XCTAssertTrue(jump.waitForExistence(timeout: 5), "no 回到最新 arrow away from the bottom")
        jump.tap()
        let back = NSPredicate { _, _ in newest.exists && newest.isHittable }
        XCTAssertEqual(XCTWaiter.wait(for: [expectation(for: back, evaluatedWith: nil)], timeout: 5), .completed,
                       "回到最新 did not bring the newest message on screen")
        XCTAssertTrue(jump.waitForNonExistence(timeout: 3), "the arrow stays at the newest message")
    }
}
