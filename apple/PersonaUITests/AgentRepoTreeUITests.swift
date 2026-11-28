import XCTest

/// By Repository's tree (#2531), against scripts/mock/mock_hub.py with MOCK_WORKTREES=1 MOCK_ORCA_CHILDREN=1
/// MOCK_MANAGERS=1 (PERSONA_WORKTREE_HUB): a repository's header has no ⋯ (its actions are its long-press menu), its agents are the
/// display's rows as in By Project (#3753), and the screens it scrolls through go to PERSONA_SHOTS.
final class AgentRepoTreeUITests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_WORKTREE_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_WORKTREES=1 MOCK_ORCA_CHILDREN=1 MOCK_MANAGERS=1 (PERSONA_WORKTREE_HUB)")
        }
        hub = h
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
    }

    /// The tree from the top down, a screen at a time.
    @MainActor func testTheTreeScreens() throws {
        let app = launch()
        let all = app.buttons["agents.filter.all"]
        XCTAssertTrue(all.waitForExistence(timeout: 20), "no Agents list")
        sleep(2)
        shot(app, "repo-tree-0")
        let window = app.windows.firstMatch
        let from = window.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: all.frame.midX - window.frame.minX, dy: window.frame.height * 0.75))
        for i in 1...3 {
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: -window.frame.height * 0.5)),
                       withVelocity: .slow, thenHoldForDuration: 0.3)
            shot(app, "repo-tree-\(i)")
        }
    }

    @MainActor func testHerdrRemovedTreeFollowsTheRepositoryFold() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_HERDR_REPOSITORIES"] == "1" else {
            throw XCTSkip("requires MOCK_HERDR_REPOSITORIES=1")
        }
        let app = launch(extra: ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"])
        let all = app.buttons["agents.filter.all"]
        XCTAssertTrue(all.waitForExistence(timeout: 20))
        let headings = app.buttons.matching(identifier: "agents.project")
            .matching(NSPredicate(format: "label BEGINSWITH %@", "repo-fixture"))
        let heading = headings.firstMatch
        let removed = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Removed tree agent")).firstMatch
        bringUp(removed, in: app, columnX: all.frame.midX)
        XCTAssertTrue(removed.exists && removed.isHittable)
        shot(app, "herdr-repository-tree")
        let window = app.windows.firstMatch
        let from = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35))
        for _ in 0..<8 where !heading.isHittable {
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 220)))
        }
        XCTAssertTrue(heading.isHittable)
        XCTAssertEqual(headings.count, 1)
        XCTAssertTrue(removed.exists)
        heading.tap()
        let hidden = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: removed)
        XCTAssertEqual(XCTWaiter().wait(for: [hidden], timeout: 5), .completed)
        heading.tap()
    }

    @MainActor func testHerdrLoneWorkspaceKeepsItsMarksAndActions() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_HERDR_REPOSITORIES"] == "1" else {
            throw XCTSkip("requires MOCK_HERDR_REPOSITORIES=1")
        }
        let app = launch(extra: ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"])
        let all = app.buttons["agents.filter.all"]
        XCTAssertTrue(all.waitForExistence(timeout: 20))
        let heading = app.buttons.matching(identifier: "agents.project")
            .matching(NSPredicate(format: "label BEGINSWITH %@", "notes-fixture")).firstMatch
        app.bringAboveTabBar(heading)
        XCTAssertTrue(heading.isHittable)
        XCTAssertTrue(heading.label.contains("Pinned"))
        XCTAssertTrue(heading.label.contains("offline"))
        shot(app, "herdr-lone-workspace")
        heading.coordinate(withNormalizedOffset: CGVector(dx: 0.24, dy: 0.5)).press(forDuration: 1.2)
        let copyPath = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "worktree.copyPath", "Copy Path")).firstMatch
        XCTAssertTrue(copyPath.waitForExistence(timeout: 5))
        shot(app, "herdr-workspace-actions")
    }

    /// A repository's header holds no ⋯: what it offered (open, pin, mute, rename the space) is its long-press menu.
    @MainActor func testARepositorysActionsAreItsLongPressMenu() throws {
        let app = launch()
        XCTAssertTrue(app.buttons["agents.filter.all"].waitForExistence(timeout: 20), "no Agents list")
        let weather = app.buttons.matching(identifier: "agents.project").matching(NSPredicate(format: "label BEGINSWITH 'weather'")).firstMatch
        bringUp(weather, in: app, columnX: app.buttons["agents.filter.all"].frame.midX)
        XCTAssertTrue(weather.exists && weather.isHittable, "no weather repository")
        XCTAssertFalse(app.buttons["weather 的更多操作"].exists, "a ⋯ on the repository's header")
        weather.press(forDuration: 1.2)
        let open = app.buttons["打开空间"].firstMatch
        XCTAssertTrue(open.waitForExistence(timeout: 5), "the repository's long-press menu (打开空间)")
        shot(app, "repo-tree-menu")
    }

    /// The agents under a repository's worktrees are the display's rows, saying what they say By Project (#3753): the
    /// widget agent's row is the same there and here, its latest words and where it runs included (the tree's one line
    /// had its title and time alone), in Avatar and in Compact List, and a finger's height.
    @MainActor func testAgentsAreTheDisplaysRows() throws {
        for (display, id) in [("avatar", "agents.row"), ("list", "agents.compactRow")] {
            let byProject = widgetRow(grouping: "space", display: display, id: id)
            let byRepository = widgetRow(grouping: "repo", display: display, id: id)
            XCTAssertFalse(byRepository.label.isEmpty, "no widget agent row By Repository (\(display))")
            XCTAssertEqual(Self.withoutTime(byRepository.label), Self.withoutTime(byProject.label),
                           "the widget agent's row By Repository is not the one By Project (\(display))")
            XCTAssertGreaterThanOrEqual(byRepository.height, 43.5, "the widget agent's row is \(byRepository.height) pt tall (\(display))")
            if display == "avatar" {
                XCTAssertGreaterThan(byRepository.height, 60, "By Repository draws a one-line row in Avatar (\(byRepository.height) pt)")
            }
        }
    }

    /// The widget agent's row in a grouping and display: its label and height, from a fresh launch.
    @MainActor private func widgetRow(grouping: String, display: String, id: String) -> (label: String, height: CGFloat) {
        let look = display == "list" ? ["-dev.compact", "YES"] : ["-dev.compact", "NO", "-agents.rowStyle", display]
        let app = launch(grouping: grouping, extra: look + ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"])
        defer { app.terminate() }
        XCTAssertTrue(app.buttons["agents.filter.all"].waitForExistence(timeout: 20), "no Agents list")
        let row = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", id, "给天气 App 加主屏幕小组件")).firstMatch
        bringUp(row, in: app, columnX: app.buttons["agents.filter.all"].frame.midX)
        guard row.exists else { return ("", 0) }
        shot(app, "repo-row-\(grouping)-\(display)")
        return (row.label, row.frame.height)
    }

    /// A row's spoken label without its time ("3 min ago" moves on between two launches; English, `Fmt.relative`).
    static func withoutTime(_ label: String) -> String {
        let time = #"^(Just now|\d+ min ago|\d{1,2}:\d{2}(\s?[AP]M)?|Yesterday|[A-Z][a-z]+day|[A-Z][a-z]{2}\s\d{1,2})$"#
        return label.components(separatedBy: ", ").filter { $0.range(of: time, options: .regularExpression) == nil }
            .joined(separator: ", ")
    }

    /// Beside the chat (regular width), the open agent's row is washed in its repository's card.
    @MainActor func testTheOpenAgentsLineStandsOutBesideTheChat() throws {
        let app = launch()
        XCTAssertTrue(app.buttons["agents.filter.all"].waitForExistence(timeout: 20), "no Agents list")
        guard app.windows.firstMatch.frame.width > 700 else { throw XCTSkip("the list is beside the chat in regular width only") }
        let row = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == 'agents.row' AND label CONTAINS %@", "给天气 App 加主屏幕小组件")).firstMatch
        bringUp(row, in: app, columnX: app.buttons["agents.filter.all"].frame.midX)
        XCTAssertTrue(row.exists && row.isHittable, "no widget agent line")
        row.tap()
        sleep(3)
        shot(app, "repo-tree-open")
    }

    /// The open agent keeps one selected row while its idle repository is folded, after it opens, and after another opens.
    @MainActor func testIdleRepositorySelectionFollowsTheOpenAgent() throws {
        let notes = "macbook/a1f3c2d4-0000-4000-8000-0000000000c3"
        let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
        let app = launch(extra: ["-holdChromeOpen", "YES"], sessionProof: true)
        let column = app.buttons["agents.filter.all"]
        XCTAssertTrue(column.waitForExistence(timeout: 20), "no Agents list:\n\(app.debugDescription)")
        guard app.windows.firstMatch.frame.width > 700 else { throw XCTSkip("the list is beside the chat in regular width only") }
        let columnX = column.frame.midX
        app.buttons["sidebar-search"].tap()
        let field = app.descendants(matching: .any)["full-search-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no Search field")
        field.tap()
        field.typeText("整理读书笔记")
        let found = app.buttons.matching(NSPredicate(format: "identifier == 'full-search-agent' AND label CONTAINS %@", "整理读书笔记")).firstMatch
        XCTAssertTrue(found.waitForExistence(timeout: 10), "the idle notes agent was not found")
        found.tap()
        let title = app.buttons["chat.title"].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 20), "no notes chat:\n\(app.debugDescription)")
        XCTAssertEqual(title.value as? String, notes)
        scrollToTop(in: app, columnX: columnX)
        let foldedRows = app.buttons.matching(identifier: "agents.quietSpace").matching(NSPredicate(format: "label BEGINSWITH 'notes'"))
        let folded = foldedRows.firstMatch
        bringUp(folded, in: app, columnX: columnX)
        XCTAssertEqual(foldedRows.count, 1, "the notes repository locator is not unique")
        XCTAssertTrue(folded.exists && folded.isHittable, "no folded notes repository")
        shot(app, "idle-repository-folded")
        XCTAssertTrue(folded.isSelected, "the open agent's folded repository is not selected")

        folded.tap()
        let unfolded = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'notes'")).firstMatch
        let agent = app.buttons.matching(NSPredicate(format: "identifier == 'agents.row' AND label CONTAINS %@", "整理读书笔记")).firstMatch
        bringUp(agent, in: app, columnX: columnX)
        XCTAssertTrue(agent.waitForExistence(timeout: 5) && agent.isHittable, "unfolding did not build the notes agent")
        XCTAssertTrue(unfolded.exists && unfolded.isHittable, "the unfolded repository line is not visible with its agent")
        XCTAssertTrue(agent.isSelected, "the unfolded agent is not selected")
        XCTAssertFalse(unfolded.isSelected, "the unfolded repository stayed selected with its agent")
        shot(app, "idle-repository-unfolded")

        scrollToTop(in: app, columnX: columnX)
        let other = app.buttons.matching(NSPredicate(format: "identifier == 'agents.row' AND label CONTAINS %@", "给天气 App 加主屏幕小组件")).firstMatch
        bringUp(other, in: app, columnX: columnX)
        XCTAssertTrue(other.exists && other.isHittable, "no widget agent elsewhere")
        other.tap()
        let opened = NSPredicate(format: "value == %@", widget)
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: opened, object: title)], timeout: 10), .completed,
                       "the other agent did not open")
        XCTAssertTrue(other.isHittable && other.isSelected, "the newly opened widget agent is not selected")

        scrollToTop(in: app, columnX: columnX)
        bringUp(unfolded, in: app, columnX: columnX)
        bringUp(agent, in: app, columnX: columnX)
        XCTAssertTrue(unfolded.exists && unfolded.isHittable && agent.exists && agent.isHittable,
                      "the original rows are not available for the selection check")
        XCTAssertFalse(unfolded.isSelected, "another agent opened, but the original repository is selected")
        XCTAssertFalse(agent.isSelected, "another agent opened, but the original agent is selected")
        shot(app, "idle-repository-other-agent")
    }

    /// Returns the Agents column to its first section before looking for a row above the current position.
    @MainActor private func scrollToTop(in app: XCUIApplication, columnX: CGFloat) {
        let top = app.buttons.matching(identifier: "agents.interaction").firstMatch
        let window = app.windows.firstMatch
        let from = window.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: columnX - window.frame.minX, dy: window.frame.height * 0.35))
        for _ in 0..<12 where !top.isHittable {
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 240)), withVelocity: .slow, thenHoldForDuration: 0.1)
        }
        XCTAssertTrue(top.isHittable, "the Agents column did not reach its first section:\n\(app.debugDescription)")
    }

    /// Lifts an element well clear of the window's bottom with short, slow drags in the list's column (on the iPad the
    /// list is the sidebar, and the window's middle the empty detail).
    @MainActor private func bringUp(_ element: XCUIElement, in app: XCUIApplication, columnX: CGFloat, tries: Int = 16) {
        let window = app.windows.firstMatch
        let bottom = window.frame.maxY - 120
        let from = window.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: columnX - window.frame.minX, dy: window.frame.height * 0.6))
        for _ in 0..<tries where !(element.exists && element.isHittable && element.frame.maxY < bottom) {
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: -200)), withVelocity: .slow, thenHoldForDuration: 0.3)
        }
    }

    @MainActor private func launch(grouping: String = "repo", extra: [String] = [], sessionProof: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-ui.oldLayout", "NO", "-app.tab", "chats", "-tab", "dev", "-dev.openChat", "",
                               "-openAgentsFresh", "YES", "-dev.grouping", grouping] + extra
        if sessionProof { app.launchEnvironment["PERSONA_UI_TEST_SESSION"] = "1" }
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + style + ".png"))
        }
    }
}
