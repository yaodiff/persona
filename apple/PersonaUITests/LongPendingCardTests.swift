import XCTest

/// Pending cards whose command or plan runs far past the cards' area (#6605): a lone card fits the area, so the area does
/// not scroll under it; what it asks is folded to a few lines with Show All N Lines (a plan: Show Full Text) under it,
/// and the answers are in view and tappable without scrolling. Show All opens the whole text in a sheet that copies it.
/// A long command once scrolled in the card, then the card in the area: two scrolls, Yes / No last.
///   PORT=18605 MOCK_LONG_ASKS=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18605 xcodebuild test … -only-testing:PersonaUITests/LongPendingCardTests
/// TEST_RUNNER_PERSONA_SHOTS=<dir> keeps the screenshots as files too.
final class LongPendingCardTests: XCTestCase {
    private var app: XCUIApplication!
    /// The mock's chats (MOCK_LONG_ASKS=1): a terminal prompt with a long command in its words, a long Bash permission,
    /// a long plan, and two long cards on one chat (the terminal's command prompt and a Bash permission).
    private static let review = "macbook/a1f3c2d4-0000-4000-8000-000000000006"
    private static let blog = "studio/a1f3c2d4-0000-4000-8000-000000000003"
    private static let orders = "server/a1f3c2d4-0000-4000-8000-000000000004"
    private static let release = "macbook/a1f3c2d4-0000-4000-8000-000000000008"
    private static let largest = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryXXXL"]
    /// The most room between the fold's Show All and the first answer: the form's spacing (12) and a little.
    private static let mostGapUnderFold: CGFloat = 24
    /// The mock's chat with no card of its own here: the DEBUG sample permission (a three-line xcodebuild) goes on it.
    private static let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = true // every card and look checked and shot, whichever fails
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_LONG_ASKS=1 (PERSONA_MOCK_HUB)")
        }
    }

    private func launch(_ session: String, _ extra: [String]) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openSession", session, "-openChatsFresh", "YES"] + extra
        app.launchPinned()
        dismissSystemAlerts()
    }

    /// The notification question a fresh install asks, and 「你已更改“Persona”的图标」 after `-ui.scheme`: SpringBoard's,
    /// over the card. Declined or closed there, never by the app's own Allow (the cards have one too).
    private func dismissSystemAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let deadline = Date().addingTimeInterval(4)
        while Date() < deadline {
            let button = springboard.alerts.buttons.matching(NSPredicate(format: "label IN %@", ["不允许", "Don’t Allow", "Don't Allow", "OK", "好"])).firstMatch
            if button.exists { button.tap() } else { usleep(250_000) }
        }
    }

    private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "long cards \(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let tag = ProcessInfo.processInfo.environment["PERSONA_SHOT_TAG"].map { "-" + $0 } ?? ""
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "long-\(name)-\(UITestStyle.name)\(tag).png"))
        }
    }

    private func looks() -> [(String, [String])] {
        [("light", ["-ui.scheme", "light"]), ("dark", ["-ui.scheme", "dark"]), ("xxxlarge", ["-ui.scheme", "light"] + Self.largest)]
    }

    /// One long card alone: no count over it, nothing of it outside the cards' area, its fold control under the folded
    /// words, and its answers whole in the area and hittable, with nothing scrolled.
    private func checkLone(_ session: String, name: String, fold: String, answers: [String]) {
        for (look, extra) in looks() {
            launch(session, extra)
            let cards = app.scrollViews["pending-cards"]
            XCTAssertTrue(cards.waitForExistence(timeout: 15), "the cards' area (\(name), \(look))")
            sleep(2)
            shot("\(name)-\(look)")
            XCTAssertFalse(app.staticTexts["pending-cards-count"].exists, "a lone card fits: no count over it (\(name), \(look))")
            let area = cards.frame.insetBy(dx: -1, dy: -1)
            let toggle = app.buttons[fold + ".expand"].firstMatch
            XCTAssertTrue(toggle.exists && toggle.isHittable && area.contains(toggle.frame),
                          "the fold's Show All (\(toggle.exists ? "\(toggle.frame)" : "missing") in \(cards.frame), \(name), \(look))")
            for label in answers {
                let button = cards.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", label)).firstMatch
                XCTAssertTrue(button.exists && button.isHittable && area.contains(button.frame),
                              "\(label) is not in view (\(button.exists ? "\(button.frame)" : "missing") in \(cards.frame), \(name), \(look))")
            }
            // The answers sit right under the fold: the form's own spacing between them, not a strip of empty room left
            // by a scroll area taller than the lines it shows.
            let first = cards.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", answers[0])).firstMatch
            if toggle.exists, first.exists {
                let frames = "toggle \(toggle.frame), \(answers[0]) \(first.frame), scroll areas \(cards.scrollViews.allElementsBoundByIndex.map { $0.frame })"
                add(XCTAttachment(string: "\(name) \(look): \(frames)"))
                XCTAssertLessThanOrEqual(first.frame.minY - toggle.frame.maxY, Self.mostGapUnderFold,
                                         "empty room under the fold (\(frames), \(name), \(look))")
            }
            // The card ends above the composer.
            let field = app.descendants(matching: .any)
                .matching(NSPredicate(format: "placeholderValue BEGINSWITH %@ OR placeholderValue BEGINSWITH %@", "给", "补充说明")).firstMatch
            if field.exists { XCTAssertLessThanOrEqual(cards.frame.maxY, field.frame.minY + 1, "the card ends above the composer (\(name), \(look))") }
            // Its height holds still (no cap moving every frame, #1580).
            let heights = (0..<4).map { _ -> CGFloat in usleep(200_000); return cards.frame.height }
            XCTAssertEqual(Set(heights).count, 1, "the card's height moves on a still page: \(heights) (\(name), \(look))")
            app.terminate()
        }
    }

    func testLongTerminalWordsFold() {
        checkLone(Self.review, name: "terminal", fold: "screen-detail", answers: ["Yes", "No"])
    }

    func testLongPermissionFolds() {
        checkLone(Self.blog, name: "permission", fold: "permission-command", answers: ["拒绝", "允许"])
    }

    func testLongPlanFolds() {
        checkLone(Self.orders, name: "plan", fold: "card-plan", answers: ["批准并自动执行", "打回"])
    }

    /// A short command is drawn whole, as before #6605: no fold, no Show All, light and dark and at xxxLarge (#6605: a
    /// three-line command was cut to two with Show All for one line).
    func testShortCommandIsWhole() {
        for (look, extra) in looks() {
            launch(Self.widget, ["-showSamplePermission", "YES"] + extra)
            let command = app.descendants(matching: .any)["permission-command"].firstMatch
            XCTAssertTrue(command.waitForExistence(timeout: 15), "the command (\(look))")
            sleep(2)
            shot("short-\(look)")
            XCTAssertFalse(app.buttons["permission-command.expand"].exists, "a short command folds (\(look))")
            app.terminate()
        }
    }

    /// Show All N Lines opens the whole command in a sheet: its last line there, Copy All, Done back to the card.
    func testShowAllOpensTheWholeCommand() {
        launch(Self.blog, ["-ui.scheme", "light"])
        let toggle = app.buttons["permission-command.expand"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 15), "the fold's Show All")
        XCTAssertTrue(toggle.label.contains("52"), "it counts the command's lines: \(toggle.label)")
        toggle.tap()
        let copy = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "拷贝全文")).firstMatch
        XCTAssertTrue(copy.waitForExistence(timeout: 5), "the sheet's Copy All")
        sleep(1)
        shot("sheet-light")
        let last = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@ OR value CONTAINS %@",
                                                                          "git status --short", "git status --short")).firstMatch
        XCTAssertTrue(last.exists, "the sheet holds the command's last line")
        app.buttons["完成"].firstMatch.tap()
        XCTAssertTrue(app.buttons["permission-command.expand"].firstMatch.waitForExistence(timeout: 5), "back to the card")
    }

    /// Two long cards: they scroll in the area under 「2 个待处理」, the permission first, its Deny / Allow in view.
    func testTwoLongCards() {
        for (look, extra) in looks() where look != "dark" {
            launch(Self.release, extra)
            let cards = app.scrollViews["pending-cards"]
            XCTAssertTrue(cards.waitForExistence(timeout: 15), "the cards' area (\(look))")
            sleep(2)
            shot("two-\(look)")
            XCTAssertTrue(app.staticTexts["pending-cards-count"].exists, "two long cards: the count over them (\(look))")
            let area = cards.frame.insetBy(dx: -1, dy: -1)
            for label in ["拒绝", "允许"] {
                let button = cards.buttons.matching(NSPredicate(format: "label == %@", label)).firstMatch
                XCTAssertTrue(button.exists && button.isHittable && area.contains(button.frame),
                              "the first card's \(label) is not in view (\(button.exists ? "\(button.frame)" : "missing"), \(look))")
            }
            app.terminate()
        }
    }
}
