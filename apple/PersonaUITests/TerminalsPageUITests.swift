import XCTest

/// Agents › the list menu › Terminals on a real-sized answer (#7115): about 40 panes per computer, a second engine whose
/// pane ids repeat the first's, panes with no title or folder, a computer with no engine, an offline one.
///   MOCK_TERMINAL_PANES=1 MOCK_PANES_MANY=1 MOCK_MACHINES=1 MOCK_PERF_MACHINES=4 PORT=<port> python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/TerminalsPageUITests
final class TerminalsPageUITests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TERMINAL_PANES=1 MOCK_PANES_MANY=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
        _ = try Data(contentsOf: URL(string: hub + "/mock/reset")!)
    }

    @MainActor func testTerminalsFromTheListMenuOpensAndScrolls() throws {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-tab", "dev", "-openAgentsFresh", "YES"]
        app.launchPinned()
        let menu = app.buttons["shell-agents"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 30), "no list menu")
        menu.tap()
        // The menu is taller than the screen with this many computers: Terminals is its last item, below the fold.
        let anchor = app.buttons["Expand All"].exists ? app.buttons["Expand All"] : app.buttons["全部展开"]
        XCTAssertTrue(anchor.waitForExistence(timeout: 5), "the menu did not open")
        let terminals = app.buttons.matching(NSPredicate(format: "label IN {'终端', 'Terminals'} AND identifier != 'agents.filter.terminal'")).firstMatch
        let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.4, dy: 0.55))
        for _ in 0..<8 where !(terminals.exists && terminals.isHittable) {
            from.press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.4, dy: 0.15)))
        }
        XCTAssertTrue(terminals.exists, "no Terminals item in the menu")
        terminals.tap()
        let rows = app.buttons.matching(identifier: "terminals.row")
        if !rows.firstMatch.waitForExistence(timeout: 25) { print("TREE2:", app.state.rawValue, app.debugDescription) }
        XCTAssertEqual(app.state, .runningForeground, "the app is gone after opening Terminals")
        XCTAssertTrue(rows.firstMatch.exists, "no terminal rows")
        for _ in 0..<12 { app.swipeUp(velocity: .fast) }
        XCTAssertEqual(app.state, .runningForeground, "the app is gone after scrolling the Terminals page")
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "terminals-page"
        a.lifetime = .keepAlways
        add(a)
    }
}
