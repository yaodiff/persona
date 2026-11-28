import XCTest

/// Against scripts/mock/composer_hub.py with MOCK_COMPOSER_CLAUDE_PANES=1.
final class ComposerClaudePaneCommandsTests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        guard let url = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = url
        continueAfterFailure = false
    }

    private func sessionID(_ slot: Int) -> String {
        String(format: "c1332500-0000-4000-8000-%012d", slot)
    }

    @MainActor private func open(_ arguments: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair] + arguments
        let env = ProcessInfo.processInfo.environment
        if let scheme = env["PERSONA_SHOTS_APPEARANCE"] { app.launchArguments += ["-ui.scheme", scheme] }
        if env["PERSONA_SHOTS_LARGEST_TEXT"] == "1" {
            app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        }
        app.launchPinned()
        XCTAssertTrue(field(app).waitForExistence(timeout: 30), "no composer for \(arguments)")
        return app
    }

    @MainActor private func pane(_ slot: Int) -> XCUIApplication {
        open(["-openSession", "macbook/\(sessionID(slot))"])
    }

    @MainActor private func field(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any)["shell-composer-field"].firstMatch
    }

    @MainActor private func openQuickCommands(_ app: XCUIApplication) {
        let plus = app.descendants(matching: .any)["shell-composer-add"].firstMatch
        XCTAssertTrue(plus.waitForExistence(timeout: 5))
        if UIDevice.current.userInterfaceIdiom == .pad { plus.tap() } else { plus.press(forDuration: 1) }
        let quick = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == %@ OR label == %@", "attach-menu-commands", "快捷指令")).firstMatch
        XCTAssertTrue(quick.waitForExistence(timeout: 5) || scrollToQuickCommands(quick, in: app),
                      "no Quick Commands:\n\(app.debugDescription)")
        quick.tap()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "汇报进展")).firstMatch.waitForExistence(timeout: 5))
    }

    @MainActor private func scrollToQuickCommands(_ quick: XCUIElement, in app: XCUIApplication) -> Bool {
        let menu = app.collectionViews.containing(NSPredicate(format: "identifier == %@ OR label == %@",
                                                                "composer-menu-mention", "提及文件或会话")).firstMatch
        guard menu.exists else { return false }
        for _ in 0..<4 where !quick.exists { menu.swipeDown() }
        return quick.exists
    }

    @MainActor private func clearCommand(_ app: XCUIApplication) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == %@ OR label BEGINSWITH %@", "attach-clear", "清空上下文")).firstMatch
    }

    @MainActor private func typeSlash(_ text: String, in app: XCUIApplication) {
        let field = field(app)
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.97, dy: 0.5)).tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        if let draft = field.value as? String, draft != field.placeholderValue, !draft.isEmpty {
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: draft.count))
        }
        field.typeText(text)
    }

    @MainActor private func openModels(_ app: XCUIApplication) -> XCUIElement {
        field(app).tapCenter()
        let chip = app.buttons["shell-composer-modes"].firstMatch
        XCTAssertTrue(chip.waitForExistence(timeout: 10), "no model chip:\n\(app.debugDescription)")
        chip.tap()
        return app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "其他模型")).firstMatch
    }

    @MainActor private func shot(_ name: String) {
        guard ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] != nil else { return }
        let image = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        image.name = name
        image.lifetime = .keepAlways
        add(image)
    }

    @MainActor func testAntigravityHasNoClearContextCommand() {
        let app = pane(4)
        defer { app.terminate() }
        openQuickCommands(app)
        XCTAssertFalse(clearCommand(app).exists)
        shot("antigravity-quick-commands")

        // A fresh launch closes the system menu without changing the draft.
        app.terminate()
        let chat = pane(4)
        typeSlash("/cl", in: chat)
        XCTAssertFalse(chat.buttons["slash-menu./clear"].exists)
        shot("antigravity-slash")
    }

    @MainActor func testAntigravityHasNoLegacyModelChoices() {
        let app = pane(4)
        defer { app.terminate() }
        let other = openModels(app)
        XCTAssertFalse(other.exists)
        XCTAssertFalse(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Sonnet")).firstMatch.exists)
        shot("antigravity-model-menu")
    }

    @MainActor func testClaudeAndLegacyPanesKeepTheTerminalCommands() {
        for slot in [1, 2, 3] {
            let app = pane(slot)
            typeSlash("/cl", in: app)
            let clear = app.buttons["slash-menu./clear"]
            XCTAssertTrue(clear.waitForExistence(timeout: 5), "no /clear in fixture \(slot)")
            if slot == 1 { shot("claude-slash") }
            clear.tap()
            let confirm = app.buttons["清空"].firstMatch
            XCTAssertTrue(confirm.waitForExistence(timeout: 5), "no Clear Context confirmation")
            if slot == 1 { shot("claude-clear-confirmation") }
            let cancel = app.buttons["取消"].firstMatch
            if cancel.exists {
                cancel.tap()
            } else {
                app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap()
            }
            XCTAssertTrue(confirm.waitForNonExistence(timeout: 5))
            typeSlash("/mo", in: app)
            XCTAssertTrue(app.buttons["slash-menu./model"].waitForExistence(timeout: 5), "no /model in fixture \(slot)")
            XCTAssertTrue(openModels(app).waitForExistence(timeout: 5), "no model choices in fixture \(slot)")
            if slot == 1 { shot("claude-model-menu") }
            app.terminate()
        }
    }

    @MainActor func testClaudeClearUsesTheExistingQuickCommandsAndBusyState() {
        let idle = pane(1)
        openQuickCommands(idle)
        XCTAssertTrue(clearCommand(idle).waitForExistence(timeout: 5))
        XCTAssertTrue(clearCommand(idle).isEnabled)
        shot("claude-quick-commands")
        idle.terminate()

        let working = open(["-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000002"])
        defer { working.terminate() }
        openQuickCommands(working)
        XCTAssertTrue(clearCommand(working).waitForExistence(timeout: 5))
        XCTAssertFalse(clearCommand(working).isEnabled)
    }

    @MainActor func testPhoneAttachmentMoreOffersClearOnlyForClaude() throws {
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("More is a sheet on iPhone") }
        for slot in [1, 4] {
            let app = pane(slot)
            let plus = app.descendants(matching: .any)["shell-composer-add"].firstMatch
            XCTAssertTrue(plus.waitForExistence(timeout: 5))
            plus.tap()
            let more = app.buttons["attach-tab-more"]
            XCTAssertTrue(more.waitForExistence(timeout: 5))
            more.tap()
            more.tap() // expand More to the full detent
            let sheet = app.descendants(matching: .any)["attach-sheet"].firstMatch
            XCTAssertTrue(sheet.waitForExistence(timeout: 5))
            let report = app.buttons["attach-command-汇报进展"]
            let clear = app.buttons["attach-clear"]
            let target = slot == 1 ? clear : report
            for _ in 0..<4 where !target.isHittable { sheet.swipeUp() }
            XCTAssertTrue(target.isHittable, "More did not show the expected quick command:\n\(app.debugDescription)")
            XCTAssertEqual(clear.exists, slot == 1)
            shot(slot == 1 ? "claude-attachment-more" : "antigravity-attachment-more")
            app.terminate()
        }
    }

    @MainActor func testExplicitModelSupportUsesTheNodesListAndModesRequest() async throws {
        let app = pane(5)
        defer { app.terminate() }
        typeSlash("/mo", in: app)
        XCTAssertFalse(app.buttons["slash-menu./model"].exists)
        let before = try await modeRequests().count
        XCTAssertTrue(openModels(app).waitForExistence(timeout: 10))
        let mini = app.buttons.matching(NSPredicate(format: "label CONTAINS[c] %@", "5.5 Mini")).firstMatch
        XCTAssertTrue(mini.waitForExistence(timeout: 5), "no launcher model list:\n\(app.debugDescription)")
        shot("codex-explicit-model-menu")
        mini.tap()

        let expected = ["session_id": sessionID(5), "model": "gpt-5.5-mini"]
        for _ in 0..<25 {
            if try await modeRequests().dropFirst(before).contains(expected) { return }
            try await Task.sleep(for: .milliseconds(200))
        }
        XCTFail("the model pick did not reach POST …/modes")
    }

    private func modeRequests() async throws -> [[String: String]] {
        let (body, _) = try await URLSession.shared.data(from: URL(string: hub + "/mock/modes")!)
        let value = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: [[String: String]]])
        return try XCTUnwrap(value["modes"])
    }

    @MainActor func testExplicitModelRefusalKeepsClearWithoutModelChoices() {
        let app = pane(6)
        defer { app.terminate() }
        typeSlash("/cl", in: app)
        XCTAssertTrue(app.buttons["slash-menu./clear"].waitForExistence(timeout: 5))
        typeSlash("/mo", in: app)
        XCTAssertFalse(app.buttons["slash-menu./model"].exists)
        XCTAssertFalse(openModels(app).exists)
    }

    @MainActor func testMainConversationKeepsItsOwnClearCommand() {
        let app = open(["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"])
        defer { app.terminate() }
        typeSlash("/cl", in: app)
        XCTAssertTrue(app.buttons["slash-menu./clear"].waitForExistence(timeout: 10))
    }
}
