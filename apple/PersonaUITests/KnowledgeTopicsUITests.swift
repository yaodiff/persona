import XCTest

/// Topics, knowledge points and the knowledge map (#7251) against scripts/mock/mock_hub.py with MOCK_KB_TOPICS=1
/// (PERSONA_MOCK_HUB): the topics list with each topic's count, a topic's points and one opened, the map's outline folding,
/// and the curation switch in Settings reaching the hub. PERSONA_SHOTS=<dir> keeps the screenshots.
final class KnowledgeTopicsUITests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_KB_TOPICS=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        guard !((get("/api/v1/kb/topics")["items"] as? [Any]) ?? []).isEmpty else { throw XCTSkip("needs MOCK_KB_TOPICS=1") }
    }

    @MainActor func testATopicListsItsPointsAndOpensOne() throws {
        let app = launch(["-tab", "assistant", "-openAssistantPage", "knowledge", "-showKnowledgeKind", "topics"])
        let topic = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "iOS 小组件")).firstMatch // i18n-ignore: the mock's topic
        XCTAssertTrue(topic.waitForExistence(timeout: 30), "no topics list:\n\(app.debugDescription)")
        XCTAssertTrue(topic.label.contains("11 条知识点"), topic.label) // i18n-ignore
        XCTAssertTrue(app.buttons["kb.map.row"].exists, "no map row above the topics")
        shot(app, "topics-list")
        topic.tap()

        let point = app.buttons["kb.point"].firstMatch
        XCTAssertTrue(point.waitForExistence(timeout: 15), "the topic shows no points:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["时间线刷新"].exists, "no subtopic section") // i18n-ignore
        shot(app, "topic-page")
        point.tap()
        XCTAssertTrue(app.navigationBars["知识点"].waitForExistence(timeout: 10), "the point did not open:\n\(app.debugDescription)") // i18n-ignore
        shot(app, "point-sheet")
    }

    @MainActor func testTheMapIsAnOutlineThatFolds() throws {
        let app = launch(["-tab", "assistant", "-openAssistantPage", "knowledge", "-showKnowledgeMap", "YES"])
        let node = app.descendants(matching: .any).matching(identifier: "kb.map.node").firstMatch
        XCTAssertTrue(node.waitForExistence(timeout: 30), "the map did not open:\n\(app.debugDescription)")
        XCTAssertTrue(node.label.contains("iOS 小组件"), "the biggest topic is not first: \(node.label)") // i18n-ignore
        XCTAssertTrue(app.descendants(matching: .any)["kb.map.stats"].exists, "no counts")
        shot(app, "map-topics")
        // The biggest topic starts open onto its subtopics; its fold closes it. (Counting rows proves nothing: a lazy list
        // shows the rows below in their place.)
        let sub = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "kb.map.node", "时间线刷新")).firstMatch // i18n-ignore
        XCTAssertTrue(sub.waitForExistence(timeout: 10), "the biggest topic does not start open:\n\(app.debugDescription)")
        let fold = app.buttons.matching(identifier: "kb.map.fold").firstMatch
        XCTAssertTrue(fold.exists)
        fold.tap()
        expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: sub)
        waitForExpectations(timeout: 5)

        app.buttons["kb.map.view.projects"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "kb.map.node").firstMatch.waitForExistence(timeout: 30))
        shot(app, "map-projects")
    }

    @MainActor func testTheSwitchReachesTheHub() throws {
        let app = launch(["-tab", "settings", "-openKnowledgeSelection", "none"])
        let toggle = app.switches["kb.topics.switch"]
        if !toggle.waitForExistence(timeout: 20) {
            // the page from Settings, as a person gets there
            app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "知识选择")).firstMatch.tap() // i18n-ignore
        }
        XCTAssertTrue(toggle.waitForExistence(timeout: 20), "no switch:\n\(app.debugDescription)")
        XCTAssertEqual(toggle.value as? String, "1")
        shot(app, "settings-switch")
        toggle.switches.firstMatch.tap()
        wait("the hub did not get kb_topics off") { self.get("/api/v1/assistant")["kb_topics"] as? Bool == false }
        toggle.switches.firstMatch.tap()
        wait("the hub did not get kb_topics on again") { self.get("/api/v1/assistant")["kb_topics"] as? Bool == true }
    }

    /// The iPad in landscape has room for the outline and the detail column side by side (the web map's side panel).
    @MainActor func testTheIPadMapHasADetailColumn() throws {
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("the iPad's two columns") }
        XCUIDevice.shared.orientation = .landscapeLeft
        defer { XCUIDevice.shared.orientation = .portrait }
        let app = launch(["-tab", "assistant", "-openAssistantPage", "knowledge", "-showKnowledgeMap", "YES"])
        let detail = app.descendants(matching: .any)["kb.map.detail"]
        XCTAssertTrue(detail.waitForExistence(timeout: 30), "no detail column beside the outline:\n\(app.debugDescription)")
        shot(app, "ipad-map")
        let point = app.descendants(matching: .any).matching(identifier: "kb.map.point").firstMatch
        XCTAssertTrue(point.waitForExistence(timeout: 10), "no point in the outline")
        point.tap()
        XCTAssertTrue(app.staticTexts["来自"].waitForExistence(timeout: 10), "the point's sources are not in the detail column") // i18n-ignore
        shot(app, "ipad-map-point")
    }

    // MARK: Helpers

    @MainActor private func launch(_ args: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair] + args
        app.launchPinned()
        return app
    }

    private func get(_ path: String) -> [String: Any] {
        var r = URLRequest(url: URL(string: hub + path)!)
        r.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        var out: [String: Any] = [:]
        let done = expectation(description: "GET \(path)")
        URLSession.shared.dataTask(with: r) { data, _, _ in
            out = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] } ?? [:]
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return out
    }

    private func wait(_ what: String, _ done: () -> Bool) {
        for _ in 0..<50 where !done() { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(done(), what)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)-\(look).png"))
        }
    }
}
