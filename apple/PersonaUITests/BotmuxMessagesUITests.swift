import XCTest

/// Uses only the DEBUG synthetic conversation; never pairs with or sends to a hub.
final class BotmuxMessagesUITests: XCTestCase {
    @MainActor func testDecodedOriginalAndCardReply() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-showBotmuxSample", "YES", "-chat.botmuxMode", "decode",
                               "-AppleLanguages", "(en)", "-ui.language", "en"]
        app.launch()
        let card = app.staticTexts["botmux.card-sent"]
        XCTAssertTrue(card.waitForExistence(timeout: 20))
        XCTAssertFalse(app.staticTexts["BOTMUX_NOTHING_TO_SEND"].exists)
        let original = app.buttons["botmux.original.example-user"]
        XCTAssertTrue(original.exists)
        original.tap()
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "<user_message>")).firstMatch.exists)
        app.buttons["Original"].tap()
        XCTAssertTrue(app.staticTexts["BOTMUX_NOTHING_TO_SEND"].waitForExistence(timeout: 5))
        XCTAssertFalse(card.exists)
        app.buttons["Decoded"].tap()
        XCTAssertTrue(card.waitForExistence(timeout: 5))
        XCTAssertFalse(app.staticTexts["BOTMUX_NOTHING_TO_SEND"].exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "BotMux-decoded"
        shot.lifetime = .keepAlways
        add(shot)
    }
}
