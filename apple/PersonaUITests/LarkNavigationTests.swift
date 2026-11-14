import XCTest

/// A chat returns to the entry it came from, including after a cancelled edge swipe.
/// Film the same flows for bar-material continuity; an accessibility tree cannot prove that a blur stayed visible.
final class LarkNavigationTests: XCTestCase {
    @MainActor private func launch(inbox: String? = nil) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs mock Hub") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", inbox == nil ? "assistant" : "mail", "-demo.on", "NO",
                               "-integrations.lark.enabled", "YES",
                               "-inbox.kind", inbox.map { $0.contains("f3") ? "group" : "dm" } ?? "", "-inbox.source", ""]
        // Navigate through the actual inbox. Its debug deep link replaces the shell's path and skips that page.
        app.launchPinned()
        return app
    }

    @MainActor private func expandSource(_ app: XCUIApplication) {
        let source = app.buttons["inbox.detail.source"]
        XCTAssertTrue(source.waitForExistence(timeout: 20), "the original-source control did not appear")
        source.tapCenter()
    }

    @MainActor private func assertChat(_ app: XCUIApplication, title: String) {
        let nav = app.navigationBars[title]
        XCTAssertTrue(nav.waitForExistence(timeout: 20))
        XCTAssertTrue(app.textFields["lark.box"].waitForExistence(timeout: 15))
        XCTAssertTrue(nav.buttons["BackButton"].isHittable)
    }

    @MainActor private func cancelBack(_ app: XCUIApplication, title: String) {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5))
            .press(forDuration: 0.1,
                   thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.18, dy: 0.5)),
                   withVelocity: .slow, thenHoldForDuration: 0.6)
        assertChat(app, title: title)
    }

    @MainActor private func back(_ app: XCUIApplication) {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5))
            .press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)))
    }

    @MainActor private func shot(_ app: XCUIApplication, name: String) {
        let shot = app.screenshot()
        let attachment = XCTAttachment(screenshot: shot)
        attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
        if let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? shot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent(name + ".png"))
        }
    }

    @MainActor func testMessagesKeepsItsNavigationAfterCancelledAndCompletedBack() throws {
        let app = try launch()
        let entry = app.buttons["shell-feishu"].firstMatch
        XCTAssertTrue(entry.waitForExistence(timeout: 20))
        entry.tapCenter()
        for (feed, title) in [("f3", "周末徒步群"), ("f1", "韩梅梅")] {
            let row = app.buttons["feishu-chat-" + feed]
            XCTAssertTrue(row.waitForExistence(timeout: 15))
            row.tapCenter()
            assertChat(app, title: title)
            cancelBack(app, title: title)
            shot(app, name: "messages-shell-\(feed)")
            back(app)
            XCTAssertTrue(row.waitForExistence(timeout: 8))
            XCTAssertTrue(entry.isHittable, "the original entry returns with the list")
        }
        app.terminate()
    }

    @MainActor func testInboxKeepsItsNavigationAfterCancelledAndCompletedBack() throws {
        for (item, title) in [("lark:f3:300", "周末徒步群"), ("lark:f1:100", "韩梅梅")] {
            let app = try launch(inbox: item)
            XCTAssertTrue(app.navigationBars["收件箱"].waitForExistence(timeout: 20))
            let row = app.staticTexts[title].firstMatch
            for _ in 0..<5 where !row.isHittable { app.swipeUp() }
            XCTAssertTrue(row.waitForExistence(timeout: 10))
            row.tapCenter()
            expandSource(app)
            assertChat(app, title: title)
            cancelBack(app, title: title)
            shot(app, name: "inbox-shell-\(item.contains("f3") ? "group" : "dm")")
            back(app)
            XCTAssertTrue(app.navigationBars["收件箱"].waitForExistence(timeout: 8), "back returns to the inbox")
            app.terminate()
        }
    }
}
