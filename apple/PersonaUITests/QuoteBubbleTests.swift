import XCTest

/// Quote as Telegram's reply (#986): a long press › Quote puts the message over the box, inside it, with × to drop it;
/// several quotes stack. Sent, the bubble opens with the quote, and a tap on it goes back to the quoted message.
///   PORT=18797 MOCK_AGENTS=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18797 xcodebuild test … -only-testing:PersonaUITests/QuoteBubbleTests
/// Both styles (TEST_RUNNER_PERSONA_UI_STYLE=beautiful).
final class QuoteBubbleTests: XCTestCase {
    private var app: XCUIApplication!
    private let answer = "PR #42 看完了"
    private let question = "看一下 PR #42"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AGENTS=1 (PERSONA_MOCK_HUB)")
        }
    }

    @MainActor private func launch(_ extra: [String] = []) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000006"] + extra
        app.launchPinned()
    }

    @MainActor private func expandSource() {
        let source = app.buttons["inbox.detail.source"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 20), "the original source control did not appear")
        source.tapCenter()
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "quote \(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
    }

    /// Long-presses a message by its centre in the window (rows of the flipped list report flipped frames) and picks Quote.
    @MainActor private func quote(_ message: String) {
        let text = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", message)).firstMatch
        XCTAssertTrue(text.waitForExistence(timeout: 15), "\(message) is not in the chat")
        sleep(2)
        let window = app.windows.firstMatch.frame
        var f = text.frame
        if !window.contains(CGPoint(x: f.midX, y: f.midY)) { f.origin.y = window.height - f.maxY }
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: f.midX, dy: f.midY)).press(forDuration: 0.6)
        let pick = app.buttons["引用"]
        XCTAssertTrue(pick.waitForExistence(timeout: 3), "the message's bar did not open")
        pick.tap()
    }

    @MainActor private var quotes: XCUIElement { app.descendants(matching: .any).matching(identifier: "composer.quotes").firstMatch }

    @MainActor private func quoteRows() -> [String] {
        quotes.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "引用"))
            .allElementsBoundByIndex.map { $0.label }
    }

    /// The quote sits inside the composer's box, above the field, and × takes it away.
    @MainActor func testOneQuoteOverTheBox() {
        launch()
        quote(answer)
        XCTAssertTrue(quotes.waitForExistence(timeout: 3), "no quote over the box")
        XCTAssertTrue(quoteRows().contains { $0.hasPrefix("引用 Claude") }, "the quote does not say whom it quotes: \(quoteRows())")
        let field = app.textFields.allElementsBoundByIndex.last!
        XCTAssertLessThanOrEqual(quotes.frame.maxY, field.frame.minY + 1, "the quote is not above the field")
        sleep(1)
        shot("one")
        app.buttons["取消引用"].firstMatch.tap()
        XCTAssertTrue(quotes.waitForNonExistence(timeout: 3), "× did not drop the quote")
    }

    /// Three quotes stack over the box; sent, the bubble opens with them, and the message's quote leads back to it.
    @MainActor func testSeveralQuotesThenSent() {
        launch(["-showSampleQuotes", "YES"])
        quote(answer)
        XCTAssertTrue(quotes.waitForExistence(timeout: 5))
        let deadline = Date().addingTimeInterval(6)
        let drops = quotes.buttons.matching(identifier: "取消引用")
        while drops.count < 3 && Date() < deadline { usleep(200_000) }
        XCTAssertEqual(drops.count, 3, "not three quotes over the box: \(quoteRows())")
        sleep(1)
        shot("three")

        // Quotes alone are a message (the words become 「参考上面的内容。」): nothing typed, no keyboard over the shot.
        let send = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "shell-composer-send", "发送")).firstMatch
        XCTAssertTrue(send.waitForExistence(timeout: 3))
        send.tap()
        XCTAssertTrue(quotes.waitForNonExistence(timeout: 5), "the quotes stayed over the box after sending")

        let blocks = app.buttons.matching(identifier: "bubble.quote")
        XCTAssertTrue(blocks.firstMatch.waitForExistence(timeout: 5), "the sent bubble has no quote")
        XCTAssertEqual(blocks.count, 3, "the sent bubble does not show its three quotes")
        sleep(1)
        shot("sent")

        let mine = blocks.matching(NSPredicate(format: "label BEGINSWITH %@", "引用 Claude")).firstMatch
        mine.tap()
        let target = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", answer)).firstMatch
        XCTAssertTrue(target.waitForExistence(timeout: 3) && target.isHittable, "the tap did not bring the quoted answer into view")
    }
    /// The Feishu chat: Reply quotes the message inside the box over the field, and a reply's quote in its bubble goes
    /// back to the message it answers.
    @MainActor func testFeishuReply() {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-openInboxItem", "lark:f3:300"]
        app.launchPinned()
        expandSource()
        let reply = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "那我也带一个")).firstMatch
        XCTAssertTrue(reply.waitForExistence(timeout: 20), "the 周末徒步群 chat did not open")
        let quote = app.buttons.matching(identifier: "bubble.quote").firstMatch
        XCTAssertTrue(quote.waitForExistence(timeout: 5), "Mia's reply has no quote")
        let quoted = String(quote.label.split(separator: "：", maxSplits: 1).last ?? "")
        sleep(1)
        shot("feishusent")

        let bar = app.descendants(matching: .any).matching(identifier: "lark.replyBar").firstMatch
        if bar.exists { app.buttons["取消回复"].firstMatch.tap() } // a reply left as the chat's draft by an earlier run
        XCTAssertTrue(bar.waitForNonExistence(timeout: 3))
        let route = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "这是这次的路线")).firstMatch
        route.press(forDuration: 1.0)
        let pick = app.buttons["回复"]
        let found = pick.waitForExistence(timeout: 3)
        if !found { shot("feishumenu") }
        XCTAssertTrue(found, "no 回复 in the message's menu")
        pick.tap()
        XCTAssertTrue(bar.waitForExistence(timeout: 3), "no reply bar over the box")
        XCTAssertTrue(app.staticTexts["回复 Tom"].exists, "the reply bar does not name who is answered")
        let box = app.textFields["lark.box"]
        XCTAssertLessThanOrEqual(bar.frame.maxY, box.frame.minY + 1, "the reply bar is not above the field")
        sleep(1)
        shot("feishu") // the keyboard up, as when answering

        quote.tap()
        let target = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", String(quoted.prefix(8)))).firstMatch
        XCTAssertTrue(target.waitForExistence(timeout: 3) && target.isHittable, "the quote did not bring \(quoted) into view")
        app.buttons["取消回复"].firstMatch.tap() // no draft left for the next run
    }
}
