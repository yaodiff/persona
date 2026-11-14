import XCTest

/// Renaming a terminal on Terminals (#6996, docs/apple/ios.md「Terminals」): a plain terminal's row and its terminal page's
/// ⋯ rename its tab (`tabs/rename`), the prompt starting from the tab's name; the new name shows in the list, and a
/// refusal puts the old one back with one plain sentence.
///   MOCK_TERMINAL_PANES=1 MOCK_TERMINAL_STREAM=1 MOCK_TERMINAL_CONTROL=1 PORT=<port> python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/TerminalsRenameUITests
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps a picture of each step, in light and dark (`…_SHOTS_TAG` adds to the names).
final class TerminalsRenameUITests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TERMINAL_PANES=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    /// The row menu's Rename…, then the terminal page's ⋯ › Rename…, which starts from the name just saved.
    @MainActor func testAPlainTerminalRenamesItsTab() throws {
        for scheme in ["light", "dark"] {
            _ = try Data(contentsOf: URL(string: hub + "/mock/reset")!) // the tabs' own names again
            let app = launch(scheme: scheme)
            let row = self.row(app, "shell · npm run dev")
            XCTAssertTrue(row.waitForExistence(timeout: 20), "no row for the plain terminal")
            shot(app, "list-\(scheme)")

            row.press(forDuration: 1.2)
            let rename = app.buttons["重命名…"]
            XCTAssertTrue(rename.waitForExistence(timeout: 5), "no 重命名… in the row's menu")
            XCTAssertTrue(app.buttons["关闭终端…"].exists)
            shot(app, "row-menu-\(scheme)")
            rename.tap()

            let field = prompt(app)
            XCTAssertEqual(field.value as? String, "shell", "the prompt does not start from the tab's name")
            shot(app, "prompt-\(scheme)")
            let before = try actions().count
            retype(field, "  dev   server ")
            save(app)

            XCTAssertTrue(self.row(app, "dev server · npm run dev").waitForExistence(timeout: 10), "the row kept the old name")
            XCTAssertTrue(self.row(app, "dev server · zsh").exists, "the tab's other terminal kept the old name")
            let sent = try actions().dropFirst(before)
            XCTAssertEqual(sent.first?["route"], "tabs/rename")
            XCTAssertEqual(sent.first?["tab_id"], "w1:t2")
            XCTAssertEqual(sent.first?["label"], "dev server", "the name went out as typed, not as the computer keeps it")
            XCTAssertEqual(sent.first?["kind"], "herdr")
            shot(app, "renamed-\(scheme)")

            // The terminal page: ⋯ in the settings button's place, Rename… starting from the new name.
            self.row(app, "dev server · npm run dev").tap()
            let more = app.buttons["terminal-more"].firstMatch
            XCTAssertTrue(more.waitForExistence(timeout: 15), "no ⋯ on the plain terminal's page")
            sleep(1)
            shot(app, "cover-\(scheme)")
            more.tap()
            let coverRename = app.buttons["重命名…"]
            XCTAssertTrue(coverRename.waitForExistence(timeout: 5), "no 重命名… in the terminal page's ⋯")
            XCTAssertTrue(app.buttons["终端设置"].exists, "Terminal Settings left the bar")
            shot(app, "cover-menu-\(scheme)")
            coverRename.tap()
            let again = prompt(app)
            XCTAssertEqual(again.value as? String, "dev server")
            shot(app, "cover-prompt-\(scheme)")
            retype(again, "logs")
            save(app)
            XCTAssertTrue(text(app, containing: "已改名为「logs」").waitForExistence(timeout: 10), "no toast over the terminal")
            shot(app, "cover-renamed-\(scheme)")
            XCTAssertEqual(try actions().last?["label"], "logs")
            app.buttons["terminal-close"].firstMatch.tap()
            XCTAssertTrue(self.row(app, "logs · npm run dev").waitForExistence(timeout: 10), "the list behind the page kept the old name")

            // An Agent's terminal page is as it was: the settings button, no ⋯ (its chat's ⋯ renames it).
            let agent = self.row(app, "Claude")
            XCTAssertTrue(agent.waitForExistence(timeout: 5), "no Agent row")
            agent.tap()
            XCTAssertTrue(app.buttons["terminal-close"].firstMatch.waitForExistence(timeout: 15), "the Agent's terminal page did not open")
            sleep(1)
            XCTAssertFalse(app.buttons["terminal-more"].exists, "an Agent's terminal page got the ⋯")
            XCTAssertTrue(app.buttons["终端设置"].exists, "an Agent's terminal page lost its settings button")
            shot(app, "cover-agent-unchanged-\(scheme)")
            app.terminate()
        }
    }

    /// The mock refuses a name that is a refusal's word as the hub relays it: one plain sentence each, the old name back.
    @MainActor func testARefusalSaysWhyAndKeepsTheOldName() throws {
        _ = try Data(contentsOf: URL(string: hub + "/mock/reset")!)
        let app = launch(scheme: "light")
        for (word, said) in [("offline", "没改成：「MacBook」离线或在睡眠。"),
                             ("unsupported", "没改成：「MacBook」上的终端程序不支持改名。"),
                             ("closed", "没改成：这个终端已经关了。")] {
            let row = self.row(app, "shell · npm run dev")
            XCTAssertTrue(row.waitForExistence(timeout: 20), "no row for the plain terminal")
            row.press(forDuration: 1.2)
            let rename = app.buttons["重命名…"]
            XCTAssertTrue(rename.waitForExistence(timeout: 5))
            rename.tap()
            retype(prompt(app), word)
            save(app)
            XCTAssertTrue(text(app, containing: said).waitForExistence(timeout: 10), "no plain sentence for \(word)")
            shot(app, "refused-\(word)")
            XCTAssertTrue(self.row(app, "shell · npm run dev").waitForExistence(timeout: 5), "the old name did not come back after \(word)")
            XCTAssertFalse(self.row(app, "\(word) · npm run dev").exists)
            _ = text(app, containing: said).waitForNonExistence(timeout: 8)
        }
    }

    // MARK: -

    @MainActor private func launch(scheme: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-ui.scheme", scheme, "-tab", "dev", "-app.tab", "chats", "-dev.openChat", "",
                               "-openTerminalsPage", "macbook"]
        app.launchPinned()
        dismissAlerts()
        return app
    }

    /// What the menus asked of the mock's computers so far.
    private func actions() throws -> [[String: String]] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/node_actions")!)
        let root = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        return (root?["actions"] as? [[String: Any]] ?? []).map { $0.compactMapValues { $0 as? String } }
    }

    private func row(_ app: XCUIApplication, _ title: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "terminals.row", title)).firstMatch
    }

    private func text(_ app: XCUIApplication, containing s: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    /// The Rename Terminal prompt's field, once it is up.
    @MainActor private func prompt(_ app: XCUIApplication) -> XCUIElement {
        let alert = app.alerts["重命名终端"]
        XCTAssertTrue(alert.waitForExistence(timeout: 5), "no 重命名终端 prompt")
        let field = alert.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 3))
        // A fresh simulator's keyboard first shows its tip about sliding to type, and takes no keys until it goes.
        let tip = app.buttons.matching(NSPredicate(format: "label IN %@", ["Continue", "继续"])).firstMatch
        if tip.waitForExistence(timeout: 1) { tip.tap() }
        return field
    }

    /// Save, until the prompt goes: a tap while the alert is still settling after typing can be lost (on iPhone every time
    /// here; FilesActionsTests saw it too), so tap again while it stays up.
    @MainActor private func save(_ app: XCUIApplication) {
        let alert = app.alerts["重命名终端"]
        for _ in 0..<3 where alert.exists {
            alert.buttons["保存"].tap()
            if alert.waitForNonExistence(timeout: 5) { break }
        }
        XCTAssertFalse(alert.exists, "Save did not close the prompt")
    }

    /// Clears the field and types `text` (the field has the keyboard as the prompt comes up).
    @MainActor private func retype(_ field: XCUIElement, _ text: String) {
        let old = (field.value as? String) ?? ""
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: old.count + 2) + text)
    }

    private func dismissAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let ok = springboard.alerts.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许", "OK", "好"])).firstMatch
        if ok.waitForExistence(timeout: 2) { ok.tap() }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let tag = ProcessInfo.processInfo.environment["PERSONA_SHOTS_TAG"].map { "-" + $0 } ?? ""
        try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("rename-\(name)\(tag).png"))
    }
}
