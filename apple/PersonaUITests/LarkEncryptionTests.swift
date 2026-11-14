import XCTest

final class LarkEncryptionFlowTests: XCTestCase {
    @MainActor private func expandSource(_ app: XCUIApplication) {
        let source = app.buttons["inbox.detail.source"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 20), "the original source control did not appear")
        source.tapCenter()
    }

    @MainActor func testEncryptedBodyPublicCoverAndKeyHandout() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs OLK mock Hub") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-demo.on", "NO",                                "-inbox.source", "", "-inbox.kind", "mention", "-openInboxFirst", "NO"]
        app.launchPinned()
        let chat = app.staticTexts["设计评审"].firstMatch
        XCTAssertTrue(chat.waitForExistence(timeout: 20)); chat.tapCenter()
        expandSource(app)
        XCTAssertTrue(app.staticTexts["评审文档已更新，请看第二页。"].firstMatch.waitForExistence(timeout: 20))
        XCTAssertTrue(app.staticTexts["午饭后再聊"].exists)
        XCTAssertTrue(app.staticTexts["加密消息 · 对外展示"].firstMatch.exists)
        XCTAssertFalse(app.staticTexts["不可见的正文"].exists)
        XCTAssertTrue(app.staticTexts["分发了本群的加密钥匙"].firstMatch.exists)
        XCTAssertTrue(app.staticTexts["加密群密钥分发消息是一个普通文本示例"].exists)
        XCTAssertFalse(app.staticTexts["加密群密钥分发消息"].exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "lark-encrypted-presentation"; shot.lifetime = .keepAlways; add(shot)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("lark-encrypted-presentation.png"))
        }
        let coverQuote = "加密消息 · 对外展示：午饭后再聊"
        XCTAssertTrue(app.staticTexts["李雷：" + coverQuote].exists)
        app.staticTexts["午饭后再聊"].press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["回复"].waitForExistence(timeout: 5))
        app.buttons["回复"].tapCenter()
        XCTAssertTrue(app.buttons["取消回复"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts[coverQuote].exists, "the composer must identify the cover too")
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("lark-encrypted-quote.png"))
        }
        app.buttons["取消回复"].tapCenter()
        app.terminate()
    }
}
