import XCTest

/// Closing a question without an answer (#6278), in Persona's main conversation and an Agent chat. Run against two
/// mock hubs: `MOCK_AGENTS=1 MOCK_ASK_LONG=swipe` for `PERSONA_MOCK_HUB`, and the same plus
/// `MOCK_QUESTION_CANCEL=1 MOCK_ASK_LONG=actions` for `PERSONA_QUESTION_CANCEL_HUB`.
final class QuestionSkipTests: XCTestCase {
    private var app: XCUIApplication!
    private static let mainChat = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private static let agentChat = "macbook/a1f3c2d4-0000-4000-8000-000000000002"

    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] == nil, "needs the plain question mock hub")
        try XCTSkipIf(ProcessInfo.processInfo.environment["PERSONA_QUESTION_CANCEL_HUB"] == nil, "needs MOCK_QUESTION_CANCEL=1")
        addUIInterruptionMonitor(withDescription: "notifications") { alert in
            let no = alert.buttons.matching(NSPredicate(format: "label IN %@", ["不允许", "Don’t Allow", "Don't Allow"])).firstMatch
            if no.exists { no.tap(); return true }
            return false
        }
    }

    private var plainHub: String { ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]! }
    private var cancelHub: String { ProcessInfo.processInfo.environment["PERSONA_QUESTION_CANCEL_HUB"]! }

    private func launch(main: Bool, hub: String, question: String, resolveMode: String = "ok") {
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        // Reset the fixture and the previous test's response mode independently: an assertion can end a test before a
        // defer restores the mode, and the next success case must not inherit its deliberate failure.
        _ = try? Data(contentsOf: URL(string: hub + "/mock/resolve-mode?mode=" + resolveMode)!)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        if main {
            app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant",
                                   "-openAssistant", Self.mainChat, "-openChatsFresh", "YES"]
        } else {
            app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-dev.openChat", Self.agentChat, "-tab", "dev",
                                   "-openAgentsFresh", "YES", "-openChatsFresh", "YES"]
        }
        app.launchPinned()
        let asked = app.staticTexts[question].firstMatch
        if !asked.waitForExistence(timeout: 8) { app.tap() } // lets the interruption monitor see a system alert
        XCTAssertTrue(asked.waitForExistence(timeout: 20), "no question card:\n\(app.debugDescription)")
        dismissIconAlert()
    }

    private func launchMain(cancel: Bool = true, resolveMode: String = "ok") {
        launch(main: true, hub: cancel ? cancelHub : plainHub, question: cancel ? "这周先处理哪件事？" : "短拖动也不要替我选",
               resolveMode: resolveMode)
    }

    private func launchAgent() {
        launch(main: false, hub: cancelHub, question: "网络层的测试用哪个框架？", resolveMode: "slow")
    }

    private var skip: XCUIElement { app.buttons["question-skip"].firstMatch }

    private func dismissIconAlert() {
        let ok = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.buttons
            .matching(NSPredicate(format: "label IN %@", ["OK", "好"])).firstMatch
        if ok.waitForExistence(timeout: 1) { ok.tap() }
    }

    private func resolved(_ id: String, hub: String? = nil) -> [[String: Any]] {
        guard let data = try? Data(contentsOf: URL(string: (hub ?? cancelHub) + "/mock/resolved")!),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let rows = object["resolved"] as? [[String: Any]] else { return [] }
        return rows.filter { $0["id"] as? String == id }
    }

    private func waitForResolution(_ id: String, count: Int, file: StaticString = #filePath, line: UInt = #line) {
        let deadline = Date().addingTimeInterval(10)
        while resolved(id).count < count && Date() < deadline {
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        XCTAssertEqual(resolved(id).count, count, file: file, line: line)
    }

    func testMainSkipResolvesExactlyAndShowsReceipt() {
        launchMain()
        XCTAssertTrue(skip.waitForExistence(timeout: 5))
        XCTAssertTrue(skip.isHittable)
        XCTAssertGreaterThanOrEqual(skip.frame.height, 44)
        XCTAssertFalse(app.buttons["interaction-menu"].exists, "Persona's own ask_user has no terminal to hand back to")

        skip.tap()
        XCTAssertTrue(app.staticTexts["这周先处理哪件事？"].waitForNonExistence(timeout: 10), "the card stayed after Skip:\n\(app.debugDescription)")
        waitForResolution("ix_ask_long", count: 1)
        let resolution = resolved("ix_ask_long").first?["resolution"] as? [String: Any]
        XCTAssertEqual(resolution as? NSDictionary, ["cancel": true])
        XCTAssertTrue(app.staticTexts["已关闭提问，未回复"].firstMatch.waitForExistence(timeout: 3), "no closed-question receipt")
    }

    func testAgentSkipWorksAndKeepsTerminalHandoff() {
        launchAgent()
        let menu = app.buttons["interaction-menu"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 5), "an Agent question keeps its card menu")
        menu.tap()
        XCTAssertTrue(app.buttons["交给电脑上的终端处理"].waitForExistence(timeout: 2))
        // BeautifulUI presents this menu as a separate layer; tapping its original ⋯ again has no hit point.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.05, dy: 0.35)).tap()
        XCTAssertTrue(skip.waitForExistence(timeout: 5))
        skip.tap()
        let sending = app.staticTexts.matching(NSPredicate(format: "label IN %@", ["正在发送…", "Sending…"])).firstMatch
        XCTAssertTrue(sending.waitForExistence(timeout: 2), "no immediate Sending feedback")
        XCTAssertFalse(skip.isEnabled, "a second Skip is available while one is in flight")
        XCTAssertTrue(app.staticTexts["网络层的测试用哪个框架？"].waitForNonExistence(timeout: 15))
        waitForResolution("ix_tests", count: 1)
        let resolution = resolved("ix_tests").first?["resolution"] as? [String: Any]
        XCTAssertEqual(resolution as? NSDictionary, ["cancel": true])
        XCTAssertTrue(app.staticTexts["已关闭提问，未回复"].firstMatch.waitForExistence(timeout: 3))
    }

    func testQuestionWithoutCanCancelHasNoSkip() {
        launchMain(cancel: false)
        XCTAssertFalse(skip.exists)
        XCTAssertFalse(app.buttons["interaction-menu"].exists)
    }

    func testFailedSkipKeepsThePickAndCard() {
        launchMain(resolveMode: "fail")
        let first = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "选项 1")).firstMatch
        XCTAssertTrue(first.waitForExistence(timeout: 5))
        first.tap()
        let picked = NSPredicate(format: "isSelected == true OR label CONTAINS %@", "已选")
        let selection = XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: picked, object: first)], timeout: 5)
        XCTAssertTrue(selection == .completed && (first.isSelected || first.label.contains("已选")),
                      "the deliberate pick did not stay selected")
        skip.tap()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label IN %@", ["重试", "Retry"])).firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["这周先处理哪件事？"].exists, "a failed Skip removed the card")
        XCTAssertTrue(first.isSelected || first.label.contains("已选"), "a failed Skip lost the pick")
        XCTAssertTrue(resolved("ix_ask_long").isEmpty, "the failed request reached the mock's resolved list")
    }
}
