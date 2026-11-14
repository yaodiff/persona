import XCTest

/// The briefing's recommended setup (#5804) and the rule suggested under an open plan (#5802), against
/// scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_BRIEF_SETUP=1 MOCK_RULE_SUGGESTIONS=1
/// (PERSONA_MOCK_HUB). A mock without those fixtures skips. With PERSONA_SHOTS set it writes what it saw there
/// (PERSONA_SHOTS_APPEARANCE names the appearance).
final class AvailabilityFollowupsUITests: XCTestCase {
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/availability-\(appearance)-\(name).png"))
    }

    /// Drags the chat until `element` sits clear of the top bar and the composer (as `BriefCardUITests.reveal`).
    @MainActor private func reveal(_ element: XCUIElement, in app: XCUIApplication, file: StaticString = #filePath, line: UInt = #line) {
        let window = app.windows.firstMatch.frame
        let band = (window.minY + window.height * 0.2)...(window.minY + window.height * 0.75)
        let middle = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        for _ in 0..<16 where !band.contains(element.frame.midY) {
            let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: element.frame.midY < band.lowerBound ? 0.75 : 0.25))
            middle.press(forDuration: 0.1, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.2)
        }
        XCTAssertTrue(band.contains(element.frame.midY), "can't bring \(element) into view: \(element.frame)", file: file, line: line)
    }

    @MainActor private func json(_ hub: String, _ path: String) async throws -> [String: Any] {
        var request = URLRequest(url: try XCTUnwrap(URL(string: hub + path)))
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let (data, _) = try await URLSession.shared.data(for: request)
        return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    /// The mock's main conversation feed: its briefing's setup lines and whether the open plan carries a suggestion.
    @MainActor private func fixtures(_ hub: String) async throws -> (setup: Bool, suggestion: Bool) {
        let conversations = try await json(hub, "/api/v1/assistant")["conversations"] as? [[String: Any]]
        let id = try XCTUnwrap(conversations?.first?["session_id"] as? String)
        let feed = try await json(hub, "/api/v1/assistant/conversations/\(id)/feed")
        let items = feed["items"] as? [[String: Any]] ?? []
        let brief = items.first { $0["kind"] as? String == "brief" }?["body"] as? [String: Any]
        let plans = feed["plans"] as? [String: [String: Any]] ?? [:]
        return (brief?["setup"] != nil, plans.values.contains { $0["suggestion"] != nil })
    }

    @MainActor func testTheBriefingEndsWithTheRecommendedSetupAndAPlanCarriesItsSuggestion() async throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_BRIEF_SETUP=1 MOCK_RULE_SUGGESTIONS=1 (PERSONA_MOCK_HUB)")
        }
        let have = try await fixtures(hub)
        guard have.setup, have.suggestion else {
            throw XCTSkip("this mock hub has no setup lines or no suggested rule: start it with MOCK_BRIEF_SETUP=1 MOCK_RULE_SUGGESTIONS=1")
        }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main]
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 3) { allow.tap() }

        let card = app.descendants(matching: .any)["brief.card"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 30), "no brief card:\n\(app.debugDescription)")

        // The setup lines close the card. Only the mechanisms turned on at a page of this app have Open Settings.
        let wake = app.buttons["brief.setup.open.triage_wake"]
        XCTAssertTrue(wake.waitForExistence(timeout: 10), "no setup line with Open Settings:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["brief.setup.open.supervisor_tier"].exists)
        XCTAssertTrue(app.buttons["brief.setup.open.standing_authorizations"].exists, "standing authorizations have a page now (#7002)")
        for id in ["task_trust", "delegate"] {
            XCTAssertFalse(app.buttons["brief.setup.open.\(id)"].exists, "\(id) has no page in this app")
        }
        reveal(wake, in: app)
        try shot(app, "setup")

        // The open plan's suggested rule: one line under the card, and Not Now takes it away for good.
        let notNow = app.buttons["rule-suggestion.not-now"]
        XCTAssertTrue(notNow.waitForExistence(timeout: 10), "no suggested rule under the open plan:\n\(app.debugDescription)")
        let always = app.buttons["rule-suggestion.always"]
        XCTAssertTrue(always.exists)
        reveal(notNow, in: app)
        XCTAssertEqual(notNow.frame.midY, always.frame.midY, accuracy: 1, "the two buttons share one line")
        try shot(app, "plan-suggestion")
        notNow.tap()
        XCTAssertTrue(notNow.waitForNonExistence(timeout: 10), "Not Now left the line")
        XCTAssertFalse(always.exists)
        try shot(app, "plan-suggestion-answered")
    }
}
