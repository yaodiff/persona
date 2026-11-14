import XCTest

/// The inbox matter detail's agreed block order and the processing conversation's push/back navigation.
/// Run against scripts/mock/mock_hub.py with MOCK_INBOX_SHOWCASE=1 (PERSONA_MOCK_HUB).
final class InboxDetailOrderUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor func testDetailBlocksFollowTheAgreedOrder() throws {
        let app = try openShowcase()
        let identifiers = ["inbox.detail.meta", "inbox.now", "inbox.summary", "inbox.recap", "inbox.chain"]
        var blocks: [(id: String, element: XCUIElement)] = []

        for identifier in identifiers {
            let element = app.descendants(matching: .any)[identifier].firstMatch
            XCTAssertTrue(element.waitForExistence(timeout: 10),
                          "\(identifier) is missing; actual frame=\(element.frame)\n\(app.debugDescription)")
            blocks.append((identifier, element))
        }

        for index in 1..<blocks.count {
            let upper = blocks[index - 1]
            let lower = blocks[index]
            let upperFrame = upper.element.frame
            let lowerFrame = lower.element.frame
            XCTAssertLessThan(upperFrame.minY, lowerFrame.minY,
                              "\(upper.id) must be above \(lower.id); \(upper.id) actual frame=\(upperFrame), "
                              + "\(lower.id) actual frame=\(lowerFrame)")
        }
    }

    @MainActor func testConversationPushesAndReturnsToDetail() throws {
        let app = try openShowcase()
        assertConversationBelow("等张三确认新报价", in: app)
        let conversation = app.buttons["inbox.now.conversation"].firstMatch
        conversation.tap()

        let composer = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 20),
                      "the processing conversation did not open:\n\(app.debugDescription)")
        let back = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "chevron.left", "返回")).firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 5), "the conversation is not pushed over the inbox detail")

        back.tap()
        XCTAssertTrue(app.descendants(matching: .any)["inbox.viewSource"].firstMatch.waitForExistence(timeout: 10),
                      "Back did not return to the inbox detail:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["inbox.now.conversation"].firstMatch.waitForExistence(timeout: 10),
                      "the processing conversation button is missing after Back:\n\(app.debugDescription)")
    }

    @MainActor func testConversationStaysBelowStateWithoutInstruction() throws {
        let app = try openShowcase("sc-2")
        assertConversationBelow("等你确认回复", in: app)
    }

    @MainActor private func assertConversationBelow(_ words: String, in app: XCUIApplication) {
        let state = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
        let conversation = app.buttons["inbox.now.conversation"].firstMatch
        XCTAssertTrue(state.waitForExistence(timeout: 15), "the state sentence is missing:\n\(app.debugDescription)")
        XCTAssertTrue(conversation.waitForExistence(timeout: 15),
                      "the processing conversation button is missing:\n\(app.debugDescription)")
        XCTAssertGreaterThan(conversation.frame.midY, state.frame.maxY,
                             "Conversation's center must be below the state; state=\(state.frame), conversation=\(conversation.frame)")
        XCTAssertEqual(conversation.frame.minX, state.frame.minX, accuracy: 1,
                       "Conversation must align with the state words; state=\(state.frame), conversation=\(conversation.frame)")
    }

    @MainActor private func openShowcase(_ item: String = "sc-1") throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_SHOWCASE=1 (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", "", "-inbox.grouping", "",
                               "-openInboxItem", item]
        app.launchPinned()
        acceptIconAlert()
        XCTAssertTrue(app.descendants(matching: .any)["inbox.viewSource"].firstMatch.waitForExistence(timeout: 20),
                      "no event detail:\n\(app.debugDescription)")
        return app
    }
}
