import XCTest

/// Settings › Translation (docs/apple/ios.md「Translation」): with it on, a mail in another language opens translated with Show Original to go back;
/// a Feishu chat picked there gets translations under others' foreign messages, and no other chat does. Against
/// scripts/mock/mock_hub.py (PERSONA_MOCK_HUB): mail 504 is in English, the 「设计评审」 chat (f2) has English messages.
final class TranslationTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(item: String, kind: String, chats: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", kind, "-inbox.source", "", "-openInboxItem", item,
                               "-translate.enabled", "YES", "-translate.target", "zh-Hans", "-translate.mail", "YES",
                               "-translate.larkChats", chats]
        app.launchPinned()
        let source = app.buttons["inbox.detail.source"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 20), "the original source control did not appear")
        source.tapCenter()
        return app
    }

    @MainActor func testForeignMailOpensTranslated() {
        let app = launch(item: "mail:acc1:t4:504", kind: "mail", chats: "")
        XCTAssertTrue(app.staticTexts["设计评审能改期吗？"].waitForExistence(timeout: 20), "no translated subject:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["已翻译成简体中文"].exists)
        app.buttons["显示原文"].tap()
        XCTAssertTrue(app.staticTexts["Can we move the design review?"].waitForExistence(timeout: 5), "the original did not come back")
        XCTAssertTrue(app.buttons["显示译文"].exists)
    }

    @MainActor func testPickedChatGetsTranslations() {
        // A launch argument is read as a property list: the JSON goes in as a quoted string.
        let app = launch(item: "lark:f2:200", kind: "mention", chats: #""[{\"id\":\"f2\",\"name\":\"设计评审\"}]""#)
        let translated = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "【译】")).firstMatch
        XCTAssertTrue(translated.waitForExistence(timeout: 20), "no translation under the English message:\n\(app.debugDescription)")
        // Only others' foreign messages: the Chinese one and my own are not translated.
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "【译】我先看了")).firstMatch.exists)
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "【译】Sounds good")).firstMatch.exists)
    }

    @MainActor func testOtherChatsAreNotTranslated() {
        let app = launch(item: "lark:f2:200", kind: "mention", chats: "")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "The new icon set")).firstMatch.waitForExistence(timeout: 20))
        sleep(2)
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "【译】")).firstMatch.exists, "a chat not picked was translated")
    }
}
