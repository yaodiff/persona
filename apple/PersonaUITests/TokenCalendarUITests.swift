import XCTest

/// The token calendar on the assistant's home (#9506): in the demo (DemoHub's made-up days and hours), the card is there
/// where the computers' chips were, a tapped day shows its own tokens and kinds, the line opens Token Usage. Its
/// attachments are the PR's screenshots, light and dark, and at the largest interface text size.
final class TokenCalendarUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor private func launch(_ scheme: String, _ extra: [String] = [], textSize: String? = nil) {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-app.tab", "assistant", "-dev.openChat", "", "-showHomeState", "empty",
                               "-newAssistant", "YES", "-ui.scheme", scheme, "-showSameIcon", "YES"] + extra
            + (textSize.map { ["-UIPreferredContentSizeCategoryName", $0] } ?? [])
        app.launchPinned()
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
    }

    @MainActor private var card: XCUIElement { app.otherElements["token-calendar"].firstMatch }
    /// The card's line: today's tokens, or the picked day's (a button that opens Token Usage).
    @MainActor private var line: XCUIElement { app.buttons["token-calendar-line"].firstMatch }

    /// The card under the greeting, today's and the last 30 days' tokens on its line; a tapped day shows its date and
    /// its four kinds; tapped again, today's line is back.
    @MainActor func testTheCardShowsADayWhenTapped() {
        for scheme in ["light", "dark"] {
            launch(scheme)
            XCTAssertTrue(line.waitForExistence(timeout: 30), "\(scheme): no token calendar on the home")
            XCTAssertTrue(line.label.hasPrefix("今天") && line.label.contains("近 30 天"), "\(scheme): the line is \(line.label)")
            let reach = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "我能看你的电脑")).firstMatch
            XCTAssertLessThan(reach.frame.maxY, card.frame.minY + 1, "\(scheme): the card is not under the welcome's words")
            shot("home \(scheme)")

            let grid = app.descendants(matching: .any)["token-calendar-grid"].firstMatch
            XCTAssertTrue(grid.exists && grid.label == "每天的 token 用量", "\(scheme): no grid")
            let spot = grid.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.3)) // a few weeks back, early in a week
            spot.tap()
            let picked = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "token-calendar-line", "缓存读")).firstMatch
            XCTAssertTrue(picked.waitForExistence(timeout: 3), "\(scheme): a tapped day shows no kinds: \(line.label)")
            XCTAssertFalse(line.label.hasPrefix("今天"), "\(scheme): today's line stayed after a tap")
            shot("day picked \(scheme)")
            spot.tap()
            let back = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label BEGINSWITH %@", "token-calendar-line", "今天")).firstMatch
            XCTAssertTrue(back.waitForExistence(timeout: 3), "\(scheme): tapped again, today's line is not back")
            app.terminate()
        }
    }

    /// The line opens Token Usage: the totals, the calendar with its key, the last week by the hour, by computer, by
    /// agent and by kind.
    @MainActor func testTheLineOpensTokenUsage() {
        for scheme in ["light", "dark"] {
            launch(scheme)
            XCTAssertTrue(line.waitForExistence(timeout: 30), "\(scheme): no token calendar on the home")
            line.tap()
            XCTAssertTrue(app.navigationBars["Token 用量"].waitForExistence(timeout: 10), "\(scheme): no Token Usage page")
            XCTAssertTrue(app.staticTexts["最近 7 天 · 每小时"].waitForExistence(timeout: 10), "\(scheme): no hours")
            shot("page \(scheme)")
            // By Project (#16530) sits under the totals: the computers are further down.
            let computers = app.staticTexts["按电脑 · 最近 7 天"]
            for _ in 0..<4 where !computers.isHittable { app.swipeUp() }
            XCTAssertTrue(computers.waitForExistence(timeout: 5), "\(scheme): no computers")
            XCTAssertTrue(app.staticTexts["按类别 · 最近 7 天"].exists, "\(scheme): no kinds")
            shot("page lower \(scheme)")
            // By Agent (#10015): the demo's agents, and the usage kept before the hub counted agents apart.
            XCTAssertTrue(app.staticTexts["按 Agent · 最近 7 天"].exists, "\(scheme): no agents")
            XCTAssertTrue(app.staticTexts["Claude Code"].exists && app.staticTexts["未区分（更新前）"].exists,
                          "\(scheme): the agents' names")
            app.terminate()
        }
    }

    /// By Project (#16530): under the totals, the demo's projects in Development and Non-development, the week's
    /// not-split part; a project opens its page, whose line reads the largest day, then a tapped one; the breakdown's
    /// folders and worktrees, computers, Agents, models and launchers. Its attachments are the PR's screenshots.
    @MainActor func testByProject() {
        for scheme in ["light", "dark"] {
            launch(scheme)
            XCTAssertTrue(line.waitForExistence(timeout: 30), "\(scheme): no token calendar on the home")
            line.tap()
            XCTAssertTrue(app.navigationBars["Token 用量"].waitForExistence(timeout: 10), "\(scheme): no Token Usage page")
            XCTAssertTrue(app.staticTexts["按项目"].waitForExistence(timeout: 10), "\(scheme): no By Project")
            XCTAssertTrue(app.staticTexts["开发"].exists && app.staticTexts["非开发"].exists, "\(scheme): the groups")
            let repo = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "acme/storefront")).firstMatch
            XCTAssertTrue(repo.waitForExistence(timeout: 5), "\(scheme): no project row")
            shot("projects \(scheme)")
            app.swipeUp()
            XCTAssertTrue(app.staticTexts["未区分（更新前）"].waitForExistence(timeout: 5), "\(scheme): no not-split row")
            shot("projects lower \(scheme)")
            app.swipeDown()
            repo.tap()
            XCTAssertTrue(app.navigationBars["acme/storefront"].waitForExistence(timeout: 10), "\(scheme): no project page")
            let dayLine = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "token")).firstMatch
            XCTAssertTrue(dayLine.waitForExistence(timeout: 10), "\(scheme): no line over the chart")
            XCTAssertTrue(app.staticTexts["目录与 worktree"].exists, "\(scheme): no folders and worktrees")
            shot("project \(scheme)")
            for _ in 0..<4 where !app.staticTexts["按启动器"].isHittable { app.swipeUp() }
            XCTAssertTrue(app.staticTexts["按模型"].exists && app.staticTexts["按启动器"].exists, "\(scheme): no models or launchers")
            XCTAssertTrue(app.staticTexts["终端里打开的"].exists, "\(scheme): no terminal launcher")
            shot("project lower \(scheme)")
            app.terminate()
        }
    }

    /// By Project and a project's page at the largest text size the interface reaches (xxxLarge): within the screen.
    @MainActor func testByProjectAtTheLargestInterfaceText() {
        launch("light", textSize: "UICTContentSizeCategoryXXXL")
        XCTAssertTrue(line.waitForExistence(timeout: 30), "no token calendar on the home")
        line.tap()
        XCTAssertTrue(app.staticTexts["按项目"].waitForExistence(timeout: 10), "no By Project")
        let repo = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "acme/storefront")).firstMatch
        for _ in 0..<3 where !repo.isHittable { app.swipeUp() }
        let window = app.windows.firstMatch.frame
        XCTAssertTrue(repo.exists && repo.frame.maxX <= window.maxX + 1, "a project row runs off the screen: \(repo.frame)")
        shot("projects xxxLarge")
        repo.tap()
        XCTAssertTrue(app.navigationBars["acme/storefront"].waitForExistence(timeout: 10), "no project page")
        shot("project xxxLarge")
    }

    /// By Project at an accessibility text size (AX3): a project's name on a line of its own, its numbers on the next.
    @MainActor func testByProjectAtAnAccessibilitySize() {
        launch("light", textSize: "UICTContentSizeCategoryAccessibilityL")
        XCTAssertTrue(line.waitForExistence(timeout: 30), "no token calendar on the home")
        line.tap()
        XCTAssertTrue(app.staticTexts["按项目"].waitForExistence(timeout: 10), "no By Project")
        let repo = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "acme/storefront")).firstMatch
        for _ in 0..<3 where !repo.isHittable { app.swipeUp() }
        let window = app.windows.firstMatch.frame
        XCTAssertTrue(repo.exists && repo.frame.maxX <= window.maxX + 1, "a project row runs off the screen: \(repo.frame)")
        shot("projects AX3")
    }

    /// By Agent at the largest text size the interface reaches (xxxLarge): every row on one line, within the screen.
    @MainActor func testTokenUsageByAgentAtTheLargestInterfaceText() {
        launch("light", textSize: "UICTContentSizeCategoryXXXL")
        XCTAssertTrue(line.waitForExistence(timeout: 30), "no token calendar on the home")
        line.tap()
        XCTAssertTrue(app.navigationBars["Token 用量"].waitForExistence(timeout: 10), "no Token Usage page")
        let heading = app.staticTexts["按 Agent · 最近 7 天"]
        for _ in 0..<4 where !heading.isHittable { app.swipeUp() }
        XCTAssertTrue(heading.waitForExistence(timeout: 5), "no agents")
        let window = app.windows.firstMatch.frame
        // By Project (#16530) has a not-split row too, above: By Agent's is the last one.
        let notSplit = app.staticTexts.matching(NSPredicate(format: "label == %@", "未区分（更新前）"))
        let name = notSplit.element(boundBy: max(0, notSplit.count - 1))
        XCTAssertTrue(name.exists && name.frame.maxX <= window.maxX, "the not-split row runs off the screen: \(name.frame)")
        shot("page agents xxxLarge")
    }

    /// At the largest text size the interface reaches (xxxLarge), the card keeps its squares and fits the screen.
    @MainActor func testTheCardAtTheLargestInterfaceText() {
        launch("light", textSize: "UICTContentSizeCategoryXXXL")
        XCTAssertTrue(line.waitForExistence(timeout: 30), "no token calendar on the home")
        let window = app.windows.firstMatch.frame
        XCTAssertTrue(window.contains(card.frame) || card.frame.maxX <= window.maxX, "the card is wider than the screen: \(card.frame)")
        shot("home xxxLarge")
    }
}
