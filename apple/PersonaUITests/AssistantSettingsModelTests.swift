import XCTest

/// Against the mock hub (#1291): Settings › Assistant shows no model. The settings row carries no launcher name and the page has
/// no Chat Model / Model Settings rows; the assistant's model is Model Settings › Main Chat only.
final class AssistantSettingsModelTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py")
        }
        hub = value
    }

    @MainActor private func launch(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", ""] + extra
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
            let suffix = ProcessInfo.processInfo.environment["PERSONA_SHOT_SUFFIX"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + suffix + ".png"))
        }
    }

    @MainActor func testSettingsRowHasNoLauncher() {
        let app = launch()
        let row = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "助手")).firstMatch
        for _ in 0..<4 where !row.isHittable { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        // The mock's assistant runs the launcher "claude": the row no longer names it.
        XCTAssertFalse(row.label.contains("claude"), row.label)
        shot(app, "settings-assistant-row")
    }

    @MainActor func testAssistantPageHasNoModelRows() {
        let app = launch(["-openAssistantPage", "assistant"])
        let workdir = app.staticTexts["工作目录"].firstMatch
        XCTAssertTrue(app.staticTexts["定时任务"].firstMatch.waitForExistence(timeout: 10))
        for _ in 0..<6 where !workdir.isHittable { app.swipeUp() }
        XCTAssertTrue(workdir.exists)
        XCTAssertFalse(app.buttons["model-config-link"].exists, "no 模型配置 shortcut on 设置 › 助手")
        XCTAssertFalse(app.staticTexts["对话模型"].exists, "no 对话模型 row on 设置 › 助手")
        shot(app, "assistant-page-settings")
    }

    /// Settings › Assistant › Task Notifications (#1731): the switch shows the hub's value and turning it off is sent to the hub.
    @MainActor func testMainTasksQuietSwitch() throws {
        try setQuiet(true)
        let app = launch(["-openAssistantPage", "assistant"])
        let toggle = app.switches["assistant-main-tasks-quiet"].firstMatch
        XCTAssertTrue(app.staticTexts["定时任务"].firstMatch.waitForExistence(timeout: 10))
        for _ in 0..<6 where !toggle.isHittable { app.swipeUp() }
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.value as? String, "1")
        XCTAssertTrue(app.staticTexts["只在主对话里通知"].firstMatch.exists)
        shot(app, "assistant-main-tasks-quiet-on")
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap() // the switch, not the label
        let off = expectation(for: NSPredicate { _, _ in (try? self.hubQuiet()) == false }, evaluatedWith: nil)
        wait(for: [off], timeout: 10)
        XCTAssertEqual(toggle.value as? String, "0")
        shot(app, "assistant-main-tasks-quiet-off")
        try setQuiet(true)
    }

    /// Assistant Profile › Agent Follow-Up › Main Chat Supervises Its Own Tasks (#5083; on that page since #7005): off on a
    /// hub with the background supervisor, and turning it on is sent to the hub.
    @MainActor func testMainSupervisesOwnTasksSwitch() throws {
        let key = "main_supervises_own_tasks"
        try setFlag(key, false)
        // Settings › Assistant Profile › Agent Follow-Up, as a person goes there: `-openAssistantPage agentFollowUp`
        // would open the page under the Persona tab as well, and the query could find that copy.
        let app = launch(["-openAssistantPage", "assistant"])
        let row = app.descendants(matching: .any)["assistant-agent-follow-up"].firstMatch
        XCTAssertTrue(app.staticTexts["定时任务"].firstMatch.waitForExistence(timeout: 10))
        for _ in 0..<6 where !(row.exists && row.isHittable) { app.swipeUp() }
        row.tap()
        let toggle = app.switches["assistant-main-supervises-own-tasks"].firstMatch
        XCTAssertTrue(app.switches["followup-start-requested"].firstMatch.waitForExistence(timeout: 10))
        let viewport = app.windows.firstMatch.frame.insetBy(dx: 0, dy: 100)
        for _ in 0..<12 where !(toggle.exists && viewport.contains(toggle.frame)) {
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.70))
            let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.55))
            from.press(forDuration: 0.05, thenDragTo: to)
            usleep(200_000)
        }
        usleep(800_000) // Let the list coast to a stop; a tap while it coasts only stops the scroll.
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertTrue(toggle.isHittable && viewport.contains(toggle.frame),
                      "the whole switch must be visible before the tap: \(toggle.frame)")
        XCTAssertEqual(toggle.value as? String, "0")
        XCTAssertTrue(app.staticTexts["主对话自己督办它派出的任务"].firstMatch.exists)
        shot(app, "assistant-main-supervises-own-tasks-off")
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap() // the switch, not the label
        let on = expectation(for: NSPredicate { _, _ in (try? self.hubFlag(key)) == true }, evaluatedWith: nil)
        wait(for: [on], timeout: 10)
        XCTAssertEqual(toggle.value as? String, "1")
        shot(app, "assistant-main-supervises-own-tasks-on")
        try setFlag(key, false)
    }

    /// Settings › Assistant › Suggest the Next Message (#4696): a supporting hub's explicit off value is visible, and the
    /// switch writes the sparse setting through the real profile page.
    @MainActor func testNextSuggestionsSwitch() throws {
        let key = "next_suggestions"
        try setFlag(key, false)
        defer { try? setFlag(key, false) }
        let app = launch(["-openAssistantPage", "assistant"])
        let toggle = app.switches["assistant-next-suggestions"].firstMatch
        XCTAssertTrue(app.staticTexts["定时任务"].firstMatch.waitForExistence(timeout: 10))
        for _ in 0..<8 where !toggle.isHittable { app.swipeUp() }
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.value as? String, "0")
        XCTAssertTrue(app.staticTexts["建议下一句"].firstMatch.exists)
        shot(app, "assistant-next-suggestions-off")
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        let on = expectation(for: NSPredicate { _, _ in (try? self.hubFlag(key)) == true }, evaluatedWith: nil)
        wait(for: [on], timeout: 10)
        XCTAssertEqual(toggle.value as? String, "1")
        shot(app, "assistant-next-suggestions-on")
    }

    /// A fresh supporting mock, with no earlier PUT, starts off. The dedicated runner opts in so an accidental on default fails
    /// instead of silently skipping; the ordinary suite skips this separate-fixture case.
    @MainActor func testNextSuggestionsDefaultOffOnFreshHub() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_FRESH_SUPPORTING_HUB"] == "1" else {
            throw XCTSkip("requires a fresh supporting mock with PERSONA_FRESH_SUPPORTING_HUB=1")
        }
        XCTAssertEqual(try hubFlag("next_suggestions"), false, "the untouched hub default must be off")
        let app = launch(["-openAssistantPage", "assistant"])
        let toggle = app.switches["assistant-next-suggestions"].firstMatch
        XCTAssertTrue(app.staticTexts["定时任务"].firstMatch.waitForExistence(timeout: 10))
        for _ in 0..<8 where !toggle.isHittable { app.swipeUp() }
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.value as? String, "0")
        XCTAssertTrue(app.staticTexts["建议下一句"].firstMatch.exists)
        shot(app, "assistant-next-suggestions-default-off")
    }

    /// Apple's English settings title is Title Case; the web deliberately keeps its sentence-case title.
    @MainActor func testNextSuggestionsEnglishTitleCase() throws {
        let key = "next_suggestions"
        try setFlag(key, false)
        defer { try? setFlag(key, false) }
        let app = launch(["-openAssistantPage", "assistant", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"])
        let toggle = app.switches["assistant-next-suggestions"].firstMatch
        XCTAssertTrue(app.navigationBars["Persona"].waitForExistence(timeout: 10))
        for _ in 0..<8 where !toggle.isHittable { app.swipeUp() }
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.value as? String, "0")
        XCTAssertTrue(app.staticTexts["Suggest the Next Message"].firstMatch.exists)
        XCTAssertFalse(app.staticTexts["Suggest the next message"].firstMatch.exists)
        shot(app, "assistant-next-suggestions-english")
    }

    /// The row remains reachable and its switch hittable at the largest accessibility text size.
    @MainActor func testNextSuggestionsLargeText() throws {
        let key = "next_suggestions"
        try setFlag(key, false)
        defer { try? setFlag(key, false) }
        let app = launch(["-openAssistantPage", "assistant", "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])
        let toggle = app.switches["assistant-next-suggestions"].firstMatch
        XCTAssertTrue(app.navigationBars["Persona"].waitForExistence(timeout: 10))
        for _ in 0..<12 where !toggle.isHittable { app.swipeUp() }
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertTrue(toggle.isHittable, "the largest text must leave the switch reachable")
        // A full swipe can stop with the row at either edge. Drag by the row's own distance from the middle, and hold
        // before letting go so the list does not coast: a fixed drag pushed the row off the top once the interface's
        // text stopped growing past xxxLarge (#6813).
        let offset = 0.5 - toggle.frame.midY / app.windows.firstMatch.frame.height
        if abs(offset) > 0.1 {
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.5))
            let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.5 + offset))
            from.press(forDuration: 0.2, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.3)
        }
        XCTAssertTrue(toggle.isHittable)
        XCTAssertEqual(toggle.value as? String, "0")
        shot(app, "assistant-next-suggestions-large-text")
    }

    /// An older hub omits the field. Scroll past the row's place before judging it absent, so an early query cannot pass.
    @MainActor func testOldHubHidesNextSuggestions() throws {
        guard try hubFlag("next_suggestions") == nil else { throw XCTSkip("requires MOCK_LEGACY_HUB=1") }
        let app = launch(["-openAssistantPage", "assistant", "-probes", "YES"])
        let toggle = app.switches["assistant-next-suggestions"].firstMatch
        let workdir = app.staticTexts["工作目录"].firstMatch
        eventually("the app's legacy assistant response", within: 10) {
            app.probe("assistant-next-suggestions", as: [String: Bool].self)?["loaded"] == true ? [] : ["the successful read has not landed"]
        }
        XCTAssertEqual(app.probe("assistant-next-suggestions", as: [String: Bool].self)?["loaded"], true)
        XCTAssertTrue(app.staticTexts["定时任务"].firstMatch.waitForExistence(timeout: 10))
        var seen = toggle.exists
        for _ in 0..<8 where !workdir.isHittable {
            app.swipeUp()
            seen = seen || toggle.exists
        }
        XCTAssertTrue(workdir.exists)
        XCTAssertFalse(seen || toggle.exists, "a missing next_suggestions field must hide the row")
    }

    private func hubQuiet() throws -> Bool? { try hubFlag("main_tasks_quiet") }

    private func setQuiet(_ on: Bool) throws { try setFlag("main_tasks_quiet", on) }

    private func hubFlag(_ key: String) throws -> Bool? {
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/assistant")!)
        return (try JSONSerialization.jsonObject(with: data) as? [String: Any])?[key] as? Bool
    }

    private func setFlag(_ key: String, _ on: Bool) throws {
        var req = URLRequest(url: URL(string: hub + "/api/v1/assistant")!)
        req.httpMethod = "PUT"
        req.httpBody = try JSONSerialization.data(withJSONObject: [key: on])
        let done = expectation(description: "PUT")
        URLSession.shared.dataTask(with: req) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }
}
