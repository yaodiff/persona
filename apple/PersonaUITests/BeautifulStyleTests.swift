import XCTest

/// The BeautifulUI style (Settings › Interface Style): its prompt bar sends what was typed (it once cleared the draft before the send
/// read it, and nothing went out), and the chat shows its components only there. Against scripts/mock/mock_hub.py
/// (PERSONA_MOCK_HUB), which records sends at /mock/sent.
final class BeautifulStyleTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(style: String, session: String = "macbook/a1f3c2d4-0000-4000-8000-000000000006") -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-ui.style", style, "-tab", "dev",
                               "-openSession", session]
        app.launchPersona()
        return app
    }

    private func sent() -> [String] {
        struct Sent: Decodable { struct Body: Decodable { var text: String? }; var sent: [Body] }
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/sent")!),
              let s = try? JSONDecoder().decode(Sent.self, from: data) else { return [] }
        return s.sent.compactMap(\.text)
    }

    @MainActor private func sendWorks(_ app: XCUIApplication, _ words: String) {
        let field = app.textViews.firstMatch.exists ? app.textViews.firstMatch : app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 20), "no composer:\n\(app.debugDescription)")
        field.tap()
        field.typeText(words)
        app.buttons["发送"].firstMatch.tap()
        let deadline = Date.now.addingTimeInterval(8)
        while !sent().contains(words), Date.now < deadline { usleep(250_000) }
        XCTAssertTrue(sent().contains(words), "「\(words)」 did not reach the hub; sent: \(sent())")
    }

    @MainActor func testPromptBarSends() {
        let app = launch(style: "beautiful")
        // The prompt bar's toolbar: @ is there only in this style.
        XCTAssertTrue(app.buttons["提及文件或会话"].waitForExistence(timeout: 20), "not the prompt bar:\n\(app.debugDescription)")
        sendWorks(app, "BeautifulUI 发送 \(Int.random(in: 1000...9999))")
    }

    /// The "/" menu hangs above the composer, outside the composer's own frame. A tap on a row picks it; it once fell
    /// through to the chat below (the composer sat in its own hosting controller, which only takes touches inside
    /// its bounds). Another chat than the send tests': the draft it leaves stays with its chat.
    @MainActor func testCommandMenuTakesTaps() {
        let app = launch(style: "beautiful", session: "studio/a1f3c2d4-0000-4000-8000-000000000003")
        XCTAssertTrue(app.buttons["提及文件或会话"].waitForExistence(timeout: 20), "not the prompt bar:\n\(app.debugDescription)")
        let field = app.textViews.firstMatch.exists ? app.textViews.firstMatch : app.textFields.firstMatch
        clear(field) // drafts are kept across runs: a "/" after old text opens no menu
        field.typeText("/")
        let row = app.buttons["/汇报"]
        XCTAssertTrue(row.waitForExistence(timeout: 5), "no / menu:\n\(app.debugDescription)")
        shot(app, "menu")
        row.tap()
        let picked = "简要汇报一下目前的进展、还剩什么"
        let deadline = Date.now.addingTimeInterval(5)
        while (field.value as? String) != picked, Date.now < deadline { usleep(200_000) }
        shot(app, "picked")
        XCTAssertEqual(field.value as? String, picked, "the tap on /汇报 did not reach the menu")
        clear(field)
    }

    /// Empties the draft: a tap at the field's end puts the caret there (a pick leaves it elsewhere), then deletes.
    @MainActor private func clear(_ field: XCUIElement) {
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.97, dy: 0.5)).tap()
        guard let v = field.value as? String, !v.isEmpty, v != field.placeholderValue else { return }
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: v.count + 2))
    }

    /// `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps a screenshot of each step.
    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("slash-\(name).png"))
    }

    @MainActor func testDefaultStyleKeepsTheOriginalComposerAndSends() {
        let app = launch(style: "glass")
        // The composer's mic: its send shows only once something is typed (what this found before was the hidden
        // dictation bar's, #7684).
        XCTAssertTrue(app.buttons["shell-composer-mic"].firstMatch.waitForExistence(timeout: 20), "no composer:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons["提及文件或会话"].exists, "the prompt bar showed in 液态玻璃")
        sendWorks(app, "液态玻璃 发送 \(Int.random(in: 1000...9999))")
    }
}
