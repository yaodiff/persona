import XCTest

/// MOCK_LARK=1 MOCK_LARK_UNREAD=1: sixty read chats precede the three unread chats. MOCK_MESSAGING=1 tests the generic fallback.
final class LarkUnreadUITests: XCTestCase {
    @MainActor func testUnreadCrossesReadOnlyPageAndReturnsToAll() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs mock Hub") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", ""]
        app.launchPinned()
        sleep(3)
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-tab", "feishu", "-demo.on", "NO", "-dev.openChat", "",
                               "-integrations.lark.enabled", "YES", "-larkTop", "NO"]
        app.launchPinned()
        let messages = app.buttons["shell-feishu"].firstMatch
        if messages.waitForExistence(timeout: 20) { messages.tapCenter() }
        let all = app.buttons["feishu-filter-all"].firstMatch
        let unread = app.buttons["feishu-filter-unread"].firstMatch
        XCTAssertTrue(all.waitForExistence(timeout: 20))
        XCTAssertTrue(all.isSelected)
        XCTAssertTrue(app.buttons["feishu-chat-read-001"].firstMatch.waitForExistence(timeout: 15))
        // The shelf precedes the header and scrolls away; the filter remains reachable below the page bar.
        app.swipeUp(velocity: .slow)
        XCTAssertTrue(unread.isHittable)
        let pinnedY = unread.frame.minY
        app.swipeUp(velocity: .slow)
        XCTAssertTrue(unread.isHittable)
        XCTAssertEqual(unread.frame.minY, pinnedY, accuracy: 2, "The filter must stay pinned while rows scroll")
        XCTAssertGreaterThanOrEqual(unread.frame.height, 44)
        unread.tapCenter()
        if ProcessInfo.processInfo.environment["PERSONA_EXPECT_UNREAD_CONTINUE"] == "1" {
            let more = app.buttons["feishu-filter-continue"].firstMatch
            XCTAssertTrue(more.waitForExistence(timeout: 20), "A long read-only run must pause before scanning further")
            XCTAssertFalse(app.buttons["feishu-chat-f3"].firstMatch.exists)
            more.tapCenter()
        }
        XCTAssertTrue(app.buttons["feishu-chat-f3"].firstMatch.waitForExistence(timeout: 20), "Unread on page two must be reachable")
        XCTAssertTrue(unread.isSelected)
        XCTAssertTrue(app.buttons["feishu-chat-f2"].firstMatch.exists, "Manually marked unread with zero count is included")
        XCTAssertTrue(app.buttons["feishu-chat-f1"].firstMatch.exists, "Unread pinned chat is kept as an ordinary row")
        XCTAssertFalse(app.buttons["feishu-chat-read-001"].firstMatch.exists)
        let screenshot = XCTAttachment(screenshot: app.screenshot())
        screenshot.name = "messages-unread"; screenshot.lifetime = .keepAlways; add(screenshot)
        all.tapCenter()
        XCTAssertTrue(app.buttons["feishu-chat-read-001"].firstMatch.waitForExistence(timeout: 15))
        XCTAssertTrue(all.isSelected)
        app.terminate()
    }
}
