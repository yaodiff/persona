import XCTest

/// Claude Code's and the hub's suggested next message over the shared Agent/Persona composer, against
/// scripts/mock/mock_hub.py started with MOCK_NEXT_SUGGESTIONS=1 (PERSONA_MOCK_HUB=http://127.0.0.1:<port>).
final class SuggestionTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = value
    }

    @MainActor private func launch(_ session: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openSession", session, "-openDraftsFresh", "YES"]
        app.launchPinned()
        return app
    }

    @MainActor private func pill(_ app: XCUIApplication, _ suggestion: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", suggestion)).firstMatch
    }

    @MainActor private func field(_ app: XCUIApplication) -> XCUIElement {
        app.textViews.firstMatch.exists ? app.textViews.firstMatch : app.textFields.firstMatch
    }

    private func sent() throws -> [[String: Any]] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/sent_to")!)
        let root = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        return root?["sent"] as? [[String: Any]] ?? []
    }

    @MainActor private func waitForSend(_ text: String, after baseline: Int) {
        let arrived = expectation(for: NSPredicate { _, _ in
            guard let rows = try? self.sent(), rows.count > baseline else { return false }
            return rows[baseline...].contains { $0["text"] as? String == text }
        }, evaluatedWith: nil)
        wait(for: [arrived], timeout: 10)
    }

    @MainActor private func clear(_ field: XCUIElement, _ text: String) {
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: text.count + 2))
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let image = app.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let suffix = ProcessInfo.processInfo.environment["PERSONA_SHOT_SUFFIX"].map { "-" + $0 } ?? ""
            try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + suffix + ".png"))
        }
    }

    @MainActor func testTapFillsTheComposer() throws {
        let suggestion = "按这两条建议改一下命名"
        let baseline = try sent().count
        let app = launch("macbook/a1f3c2d4-0000-4000-8000-000000000006")
        let pill = pill(app, suggestion)
        XCTAssertTrue(pill.waitForExistence(timeout: 15), "no suggestion:\n\(app.debugDescription)")
        pill.tap()
        let field = field(app)
        XCTAssertEqual(field.value as? String, suggestion)
        XCTAssertFalse(pill.exists, "the pill goes once the box has text")
        Thread.sleep(forTimeInterval: 0.5)
        XCTAssertEqual(try sent().count, baseline, "filling is not sending")
        // Drafts are kept: leave the box empty for the next run.
        clear(field, suggestion)
        XCTAssertTrue(pill.waitForExistence(timeout: 3), "the suggestion comes back when the box is empty again; box: \(String(describing: field.value))")
    }

    /// The main Persona conversation already uses this same composer. Its safe pill keeps Send Now and Fill In.
    @MainActor func testMainAssistantSafePillKeepsMenuAndFill() throws {
        let suggestion = "先处理第一件，其余两件下午再说"
        let baseline = try sent().count
        let app = launch("server/b2e4d6f8-0000-4000-8000-000000000001")
        let pill = pill(app, suggestion)
        XCTAssertTrue(pill.waitForExistence(timeout: 15), "no main-Persona suggestion:\n\(app.debugDescription)")
        pill.press(forDuration: 1.2)
        let sendNow = app.buttons["直接发送"]
        let fill = app.buttons["填入输入框"]
        XCTAssertTrue(sendNow.waitForExistence(timeout: 5), "safe Send Now missing:\n\(app.debugDescription)")
        XCTAssertTrue(fill.exists)
        shot(app, "suggestion-main-safe-menu")
        fill.tap()
        let field = field(app)
        XCTAssertEqual(field.value as? String, suggestion)
        Thread.sleep(forTimeInterval: 0.5)
        XCTAssertEqual(try sent().count, baseline)
        clear(field, suggestion)
    }

    /// Non-risky direct send still reaches the hub without first filling the composer.
    @MainActor func testSafeAgentSendNowStillSends() throws {
        let suggestion = "按这两条建议改一下命名"
        let baseline = try sent().count
        let app = launch("macbook/a1f3c2d4-0000-4000-8000-000000000006")
        let pill = pill(app, suggestion)
        XCTAssertTrue(pill.waitForExistence(timeout: 15))
        pill.press(forDuration: 1.2)
        let sendNow = app.buttons["直接发送"]
        XCTAssertTrue(sendNow.waitForExistence(timeout: 5))
        sendNow.tap()
        waitForSend(suggestion, after: baseline)
    }

    @MainActor func testRiskyContextMenuHasFillButNoSendNow() throws {
        let suggestion = "把会议纪要发给参会的人"
        let baseline = try sent().count
        let app = launch("server/b2e4d6f8-0000-4000-8000-000000000002")
        let pill = pill(app, suggestion)
        XCTAssertTrue(pill.waitForExistence(timeout: 15), "no risky suggestion:\n\(app.debugDescription)")
        pill.press(forDuration: 1.2)
        let fill = app.buttons["填入输入框"]
        XCTAssertTrue(fill.waitForExistence(timeout: 5), "risky Fill In missing:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons["直接发送"].exists, "a risky suggestion must not offer one-tap send")
        shot(app, "suggestion-risky-menu")
        fill.tap()
        let field = field(app)
        XCTAssertEqual(field.value as? String, suggestion)
        Thread.sleep(forTimeInterval: 0.5)
        XCTAssertEqual(try sent().count, baseline, "Fill In must leave the message for review")
        clear(field, suggestion)
        XCTAssertTrue(pill.waitForExistence(timeout: 3), "the cleared draft lets the risky pill return before the next run")
    }

    @MainActor func testRiskyTapFillsWithoutSendingThenNormalSendWorks() throws {
        let suggestion = "把会议纪要发给参会的人"
        let baseline = try sent().count
        let app = launch("server/b2e4d6f8-0000-4000-8000-000000000002")
        let pill = pill(app, suggestion)
        XCTAssertTrue(pill.waitForExistence(timeout: 15))
        pill.tap()
        let field = field(app)
        XCTAssertEqual(field.value as? String, suggestion)
        XCTAssertFalse(pill.exists)
        Thread.sleep(forTimeInterval: 0.5)
        XCTAssertEqual(try sent().count, baseline, "an ordinary tap must not send a risky suggestion")

        let send = app.buttons["shell-composer-send"].firstMatch
        XCTAssertTrue(send.waitForExistence(timeout: 5), "the normal composer Send remains available")
        send.tap()
        waitForSend(suggestion, after: baseline)
    }
}
