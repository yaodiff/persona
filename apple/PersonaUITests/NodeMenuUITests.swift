import XCTest

/// The Agents list's node menu (#3098, docs/apple/ios.md「Information architecture」): an agent row's long press splits its pane, runs Git in its
/// folder and closes its tab after asking; a space's ⋯ runs an agent in one of its shells.
///   MOCK_TERMINAL_PANES=1 PORT=<port> python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/NodeMenuUITests
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps a picture of each step (`…_SHOTS_APPEARANCE` names the simulator's appearance).
final class NodeMenuUITests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TERMINAL_PANES=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
        _ = try Data(contentsOf: URL(string: hub + "/mock/reset")!)
    }

    /// What the menus asked of the mock's computers so far.
    private func actions() throws -> [[String: String]] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/node_actions")!)
        let root = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        return (root?["actions"] as? [[String: Any]] ?? []).map { $0.compactMapValues { $0 as? String } }
    }

    @MainActor private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // By space: the ⋯ these tests open is a space group's, and the list groups by repository by default.
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-tab", "dev", "-openAgentsFresh", "YES", "-dev.grouping", "space"]
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("node-\(name)-\(look).png"))
    }

    private func text(_ app: XCUIApplication, containing s: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    /// Scrolls the list until `e` can be touched (the Needs You cards come first).
    @MainActor @discardableResult private func reveal(_ app: XCUIApplication, _ e: XCUIElement) -> Bool {
        _ = e.waitForExistence(timeout: 20)
        // Clear of the floating tab bar at the bottom too: a press there lands on the bar.
        let low = app.windows.firstMatch.frame.height * 0.65
        for _ in 0..<10 where !(e.exists && e.isHittable && e.frame.midY < low) {
            app.swipeUp(velocity: .slow)
        }
        return e.exists && e.isHittable
    }

    @MainActor private func rowMenu(_ app: XCUIApplication) {
        let row = text(app, containing: "给天气 App 加主屏幕小组件")
        XCTAssertTrue(reveal(app, row), "no row for the agent")
        row.press(forDuration: 1.2)
        app.openAgentMenuMore() // the pane's items are More's (#13396)
        XCTAssertTrue(app.buttons["终端分屏"].waitForExistence(timeout: 5), "no 终端分屏 in the row's menu")
    }

    @MainActor func testAgentRowSplitsPullsAndClosesItsTab() throws {
        let app = launch()
        rowMenu(app)
        XCTAssertTrue(app.buttons["Git"].exists)
        XCTAssertTrue(app.buttons["关闭标签页"].exists)
        shot(app, "row-menu")

        app.buttons["Git"].tap()
        XCTAssertTrue(app.buttons["拉取"].waitForExistence(timeout: 3))
        shot(app, "row-git")
        let before = try actions().count
        app.buttons["拉取"].tap()
        // The outcome in the app's words (#2895), as NodeMenuTests has it.
        XCTAssertTrue(text(app, containing: "拉取：已经是最新代码").waitForExistence(timeout: 15), "no toast with the node's summary")
        shot(app, "row-pulled")
        let pulled = try actions().dropFirst(before)
        XCTAssertEqual(pulled.first?["route"], "panes/git")
        XCTAssertEqual(pulled.first?["op"], "pull")
        XCTAssertEqual(pulled.first?["pane"], "w1:p1", "git ran somewhere other than the agent's pane")

        rowMenu(app)
        app.buttons["终端分屏"].tap()
        XCTAssertTrue(app.buttons["向右拆分"].waitForExistence(timeout: 3))
        app.buttons["向右拆分"].tap()
        XCTAssertTrue(text(app, containing: "已新建窗格").waitForExistence(timeout: 10))
        let split = try actions().last
        XCTAssertEqual(split?["route"], "panes/split")
        XCTAssertEqual(split?["direction"], "right")

        rowMenu(app)
        app.buttons["关闭标签页"].tap()
        let confirm = app.buttons.matching(NSPredicate(format: "label == %@", "关闭标签页")).firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), "closing a tab did not ask first")
        XCTAssertNotEqual(try actions().last?["route"], "tabs/close", "the tab closed before the answer")
        shot(app, "close-confirm")
        confirm.tap()
        XCTAssertTrue(text(app, containing: "已关闭标签页").waitForExistence(timeout: 10))
        XCTAssertEqual(try actions().last?["route"], "tabs/close")
        XCTAssertEqual(try actions().last?["tab_id"], "w1:t1")
    }

    @MainActor func testSpaceRunsAnAgentInOneOfItsShells() throws {
        let app = launch()
        let more = app.buttons["weather 的更多操作"].firstMatch
        XCTAssertTrue(reveal(app, more), "no ⋯ on the space")
        more.tap()
        let runHere = app.buttons["在这里运行 Agent…"]
        XCTAssertTrue(runHere.waitForExistence(timeout: 5), "no 在这里运行 Agent… in the space's menu")
        XCTAssertTrue(app.buttons["Git"].exists)
        shot(app, "space-menu")
        runHere.tap()

        XCTAssertTrue(text(app, containing: "shell · zsh").waitForExistence(timeout: 10), "the space's shells are not listed")
        XCTAssertTrue(text(app, containing: "shell · npm run dev").exists)
        XCTAssertFalse(text(app, containing: "agent-1").exists, "a pane with an agent is offered")
        shot(app, "run-here")
        let run = app.buttons["运行"]
        XCTAssertTrue(run.isEnabled, "no launcher picked")
        run.tap()
        XCTAssertTrue(text(app, containing: "已在窗格里启动").waitForExistence(timeout: 10))
        let started = try actions().last
        XCTAssertEqual(started?["route"], "panes/agent")
        XCTAssertEqual(started?["pane"], "w1:p2")
        XCTAssertFalse((started?["launcher"] ?? "").isEmpty)
        // The agent it started opens, as New Session's does (#3287): its chat comes up, not the list with it idle there.
        let title = app.buttons.matching(identifier: "chat.title").firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 10), "the started agent's chat did not open")
        // Its name comes with the machine's list, which the hub pushes once the node has the session.
        let named = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "chat.title", "新会话")).firstMatch
        XCTAssertTrue(named.waitForExistence(timeout: 10), "another chat opened: \(title.label)")
        shot(app, "run-here-opened")
    }

    /// New Worktree… (#3099, #3586): an uncoded node refusal keeps git's actionable words instead of a generic HTTP 502.
    @MainActor func testWorktreeRefusalKeepsGitsReason() throws {
        let app = launch()
        let more = app.buttons["weather 的更多操作"].firstMatch
        XCTAssertTrue(reveal(app, more), "no ⋯ on the space")
        more.tap()
        let item = app.buttons["新建 worktree…"]
        XCTAssertTrue(item.waitForExistence(timeout: 5), "no 新建 worktree… in the space's menu")
        item.tap()
        XCTAssertTrue(text(app, containing: "persona · 当前分支 main").waitForExistence(timeout: 10), "the repository was not read")
        let branch = app.textFields["worktree.branch"]
        branch.tap()
        branch.typeText("feat/taken")
        app.buttons["worktree.create"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["worktree.failure"].waitForExistence(timeout: 10), "git's refusal is not shown")
        XCTAssertTrue(text(app, containing: "already used by worktree").exists)
        app.swipeUp()
        shot(app, "worktree-refused-reason")
    }

    /// A made worktree is where New Agent starts, and the path follows the branch until it is typed by hand.
    @MainActor func testSpaceMakesAWorktreeAndStartsAnAgentThere() throws {
        let app = launch()
        let more = app.buttons["weather 的更多操作"].firstMatch
        XCTAssertTrue(reveal(app, more), "no ⋯ on the space")
        more.tap()
        let item = app.buttons["新建 worktree…"]
        XCTAssertTrue(item.waitForExistence(timeout: 5), "no 新建 worktree… in the space's menu")
        item.tap()

        XCTAssertTrue(text(app, containing: "persona · 当前分支 main").waitForExistence(timeout: 10), "the repository was not read")
        let branch = app.textFields["worktree.branch"]
        branch.tap()
        branch.typeText("feat/x")
        let path = app.textFields["worktree.path"]
        XCTAssertEqual(path.value as? String, "/home/me/.herdr/worktrees/persona/feat-x", "the path does not follow the branch")
        shot(app, "worktree-form")
        app.buttons["worktree.create"].tap()
        XCTAssertTrue(text(app, containing: "persona · feat/x").waitForExistence(timeout: 10), "新建 Agent is not set to the new worktree")
        shot(app, "worktree-new-agent")
        let made = try actions().last
        XCTAssertEqual(made?["route"], "worktree/create")
        XCTAssertEqual(made?["workspace_id"], "w1")
        XCTAssertEqual(made?["branch"], "feat/x")
        XCTAssertEqual(made?["path"], "/home/me/.herdr/worktrees/persona/feat-x")
    }
}
