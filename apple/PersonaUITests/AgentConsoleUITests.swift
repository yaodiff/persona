import XCTest

/// Native Agent console (#17129): three tabs; external conversations live inside Agents, not a fourth tab.
@MainActor final class AgentConsoleUITests: XCTestCase {
    func testThreeTabsAndExternalInsideAgents() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-persona.ios.console.enabled", "YES", "-tab", "assistant"]
        app.launch()

        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 0).waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 1).exists)
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 2).exists)
        XCTAssertEqual(app.tabBars.buttons.count, 3)

        app.tabBars.buttons.element(boundBy: 1).tap()
        let segments = app.segmentedControls.firstMatch
        XCTAssertTrue(segments.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(segments.buttons.element(boundBy: 0).exists)
        XCTAssertTrue(segments.buttons.element(boundBy: 1).exists)
        segments.buttons.element(boundBy: 1).tap()
        XCTAssertTrue(segments.buttons.element(boundBy: 1).isSelected)
        XCTAssertFalse(app.descendants(matching: .any)["feishu-conversations"].exists)


        app.tabBars.buttons.element(boundBy: 2).tap()
        XCTAssertTrue(app.buttons["settings-templates"].waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.buttons["settings-more"].exists)
    }

    /// Against scripts/mock/mock_hub.py with MOCK_PERSONAL_AGENTS=1 (PERSONA_PERSONAL_HUB).
    func testExternalListsAgentConversationsWithoutFeishuChats() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_PERSONAL_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_PERSONAL_AGENTS=1 (PERSONA_PERSONAL_HUB)")
        }
        for scheme in ["light", "dark"] {
            _ = try Data(contentsOf: URL(string: hub + "/mock/personal-agents?reset=1&state=configured")!)
            let app = XCUIApplication()
            let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
            app.launchArguments = ["-pairURL", pair, "-persona.ios.console.enabled", "YES", "-tab", "assistant", "-dev.openChat", "",
                                   "-ui.scheme", scheme]
            app.launchPinned()
            XCTAssertTrue(app.tabBars.buttons.element(boundBy: 1).waitForExistence(timeout: 30), app.debugDescription)
            app.tabBars.buttons.element(boundBy: 1).tap()

            let segments = app.segmentedControls.firstMatch
            XCTAssertTrue(segments.waitForExistence(timeout: 30), app.debugDescription)
            segments.buttons.element(boundBy: 1).tap()
            XCTAssertTrue(app.staticTexts["周报助手"].waitForExistence(timeout: 20), app.debugDescription)
            XCTAssertTrue(app.staticTexts["旅行规划"].exists, app.debugDescription)
            XCTAssertFalse(app.descendants(matching: .any)["feishu-conversations"].exists)
            if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
                try? XCUIScreen.main.screenshot().pngRepresentation
                    .write(to: URL(fileURLWithPath: dir + "/external-agents-" + scheme + ".png"))
            }
            app.staticTexts["周报助手"].firstMatch.tap()
            XCTAssertTrue(app.navigationBars["周报助手"].waitForExistence(timeout: 15), app.debugDescription)
            XCTAssertTrue(app.textFields.firstMatch.waitForExistence(timeout: 15), app.debugDescription)
            app.terminate()
        }
    }

    func testAppliedConsoleSurvivesTerminationAndRelaunch() {
        let app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-persona.ios.console.enabled", "YES", "-tab", "assistant"]
        app.launch()
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 0).waitForExistence(timeout: 15), app.debugDescription)
        app.tabBars.buttons.element(boundBy: 1).tap()
        XCTAssertTrue(app.segmentedControls.firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        app.terminate()
        app.launchArguments = ["-demo.on", "YES"]
        app.launch()
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 0).waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 1).exists)
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 2).exists)
    }

    func testApplyingTemplatePersistsAcrossRestart() {
        let app = XCUIApplication()
        let json = #"{"persona_template":1,"id":"example-console","name":"Agent console","browser":{"main_chat":"plain","agents_view":"active","settings_view":"short"}}"#
        app.launchArguments = ["-demo.on", "YES", "-tab", "assistant", "-persona.ios.console.enabled", "NO"]
        app.launch()
        let link = "persona://template?template=" + json.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!
        app.terminate()
        app.launchArguments = ["-demo.on", "YES"]
        app.launch()
        app.open(URL(string: link)!)
        // A link opens an already parsed preview; the input belongs to the import form, not this page.
        let apply = app.buttons["template-apply"].firstMatch
        XCTAssertTrue(apply.waitForExistence(timeout: 15), app.debugDescription)
        apply.tap()
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 0).waitForExistence(timeout: 15), app.debugDescription)
        app.tabBars.buttons.element(boundBy: 1).tap()
        XCTAssertTrue(app.segmentedControls.firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        app.terminate()
        app.launchArguments = ["-demo.on", "YES"]
        app.launch()
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 0).waitForExistence(timeout: 15), app.debugDescription)
        app.tabBars.buttons.element(boundBy: 1).tap()
        XCTAssertTrue(app.segmentedControls.firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        app.tabBars.buttons.element(boundBy: 2).tap()
        app.terminate()
        app.launchArguments = ["-demo.on", "YES"]
        app.launch()
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 2).waitForExistence(timeout: 15), app.debugDescription)
    }

    func testMainChatStaysAliveAcrossRepeatedRestarts() {
        let app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-tab", "assistant", "-persona.ios.console.enabled", "NO"]
        app.launch()
        let json = #"{"persona_template":1,"id":"example-console","name":"Agent console","browser":{"main_chat":"plain","agents_view":"active","settings_view":"short"}}"#
        app.open(URL(string: "persona://template?template=" + json.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)!)
        let apply = app.buttons["template-apply"].firstMatch
        XCTAssertTrue(apply.waitForExistence(timeout: 15), app.debugDescription)
        apply.tap()
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 0).waitForExistence(timeout: 15), app.debugDescription)
        for _ in 0..<3 {
            app.terminate()
            app.launchArguments = ["-demo.on", "YES", "-tab", "assistant"]
            app.launch()
            XCTAssertTrue(app.tabBars.buttons.element(boundBy: 0).waitForExistence(timeout: 15), app.debugDescription)
        }
    }

    func testDefaultDoesNotReplaceTheExistingShell() {
        let app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-persona.ios.console.enabled", "NO", "-tab", "assistant"]
        app.launch()
        XCTAssertFalse(app.segmentedControls.firstMatch.waitForExistence(timeout: 3))
    }
}
