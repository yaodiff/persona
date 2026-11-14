import XCTest

/// Regular width (iPad, iPhone Duo open): the inbox list is the split's sidebar and the items it opens replace each
/// other in the detail column. Each opens as its own page: another Feishu chat shows its own messages and draft, not
/// those of the chat open before. Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB); skipped when the app is not a
/// split. TEST_RUNNER_PERSONA_SHOTS=<dir> keeps a screenshot of each step.
final class InboxSplitTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", ""]
        app.launchPinned()
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("not a split: needs an iPad or iPhone Duo open") }
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.lifetime = .keepAlways
            add(a)
        }
        super.tearDown()
    }

    /// An inbox row's title in the list column (the open page's title is the same text, in the detail column).
    @MainActor private func row(_ title: String) -> XCUIElement? {
        let deadline = Date.now.addingTimeInterval(20)
        repeat {
            let half = app.windows.firstMatch.frame.midX
            if let r = app.staticTexts.matching(identifier: title).allElementsBoundByIndex.first(where: { $0.isHittable && $0.frame.maxX < half }) {
                return r
            }
            usleep(250_000)
        } while Date.now < deadline
        XCTFail("no inbox row \(title):\n\(app.debugDescription)")
        return nil
    }

    /// The open page's title, in the detail column's navigation bar.
    @MainActor private func title(_ text: String) -> XCUIElement {
        app.navigationBars.staticTexts.matching(identifier: text).firstMatch
    }

    @MainActor private func message(_ text: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    /// Expands the original source on a freshly opened non-event inbox detail.
    @MainActor private func expandSource() {
        let source = app.buttons["inbox.detail.source"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 15), "the original source control did not appear")
        source.tapCenter()
    }

    /// Types into the chat's box once it has the cursor: a tap while the chat is still settling can leave it without.
    @MainActor private func type(_ text: String, into box: XCUIElement) {
        for _ in 0..<3 {
            box.tapCenter()
            let deadline = Date.now.addingTimeInterval(2)
            repeat {
                if box.value(forKey: "hasKeyboardFocus") as? Bool == true {
                    box.typeText(text)
                    return
                }
                usleep(200_000)
            } while Date.now < deadline
        }
        XCTFail("the box did not take the cursor:\n\(app.debugDescription)")
    }

    /// Puts the keyboard away (it covers the list's lower rows); the draft stays in the box.
    @MainActor private func hideKeyboard() {
        let keyboard = app.keyboards.firstMatch
        guard keyboard.exists else { return }
        let hide = keyboard.buttons["Hide keyboard"]
        if hide.exists { hide.tap() } else { keyboard.coordinate(withNormalizedOffset: CGVector(dx: 0.97, dy: 0.94)).tap() }
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 3), "the keyboard stayed over the list")
    }

    @MainActor private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("inbox-split-\(name).png"))
    }

    @MainActor func testAnotherChatOpensWithItsOwnMessagesAndDraft() {
        let mine = "能赶上，下午三点前给你确认" // in 「韩梅梅」's chat only
        let theirs = "The new icon set"        // in 「设计评审」 only
        let draft = "给韩梅梅的草稿 " + UUID().uuidString.prefix(4)
        row("韩梅梅")?.tapCenter()
        expandSource()
        XCTAssertTrue(message(mine).waitForExistence(timeout: 15), "韩梅梅's chat did not open")
        let box = app.textFields["lark.box"]
        XCTAssertTrue(box.waitForExistence(timeout: 5))
        type(draft, into: box)
        shot("1-first")
        hideKeyboard()

        row("设计评审")?.tapCenter()
        XCTAssertTrue(title("设计评审").waitForExistence(timeout: 10), "设计评审 did not open")
        expandSource()
        _ = message(theirs).waitForExistence(timeout: 15)
        shot("2-second")
        XCTAssertTrue(message(theirs).exists, "设计评审 did not show its own messages")
        XCTAssertFalse(message(mine).exists, "韩梅梅's messages stayed in 设计评审")
        XCTAssertFalse((box.value as? String ?? "").contains(draft), "韩梅梅's draft went along to 设计评审")

        row("韩梅梅")?.tapCenter()
        XCTAssertTrue(title("韩梅梅").waitForExistence(timeout: 10), "韩梅梅's chat did not open again")
        expandSource()
        XCTAssertTrue(message(mine).waitForExistence(timeout: 15), "韩梅梅's chat did not show its messages again")
        XCTAssertFalse(message(theirs).exists, "设计评审's messages stayed in 韩梅梅's chat")
        XCTAssertTrue((box.value as? String ?? "").contains(draft), "韩梅梅's draft was not kept")
        shot("3-back")
        // Her chat as it was, for the tests that open it after this one.
        type(String(repeating: XCUIKeyboardKey.delete.rawValue, count: (box.value as? String ?? "").count), into: box)
    }
}
