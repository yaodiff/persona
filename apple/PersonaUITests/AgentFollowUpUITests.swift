import XCTest

/// Assistant Profile › Agent Follow-Up (#7005) against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB): the profile's row, the
/// page with the hub's defaults, a menu choice and the session-name rows saved to the hub. A mock without the settings
/// (MOCK_LEGACY_HUB) skips. With PERSONA_SHOTS set it writes what it saw there (PERSONA_SHOTS_APPEARANCE names the
/// appearance, PERSONA_SHOTS_DEVICE the device; PERSONA_SHOTS_SIZE, a content size category such as
/// UICTContentSizeCategoryXXXL, launches at that text size).
final class AgentFollowUpUITests: XCTestCase {
    private var hub: String!
    /// The hub's defaults, as a fresh mock hub has them.
    private static func defaults() -> [String: Any] { [
        "start_requested_tasks": true, "task_workspace_preference": "prefer-existing", "main_supervises_own_tasks": false,
        "agent_tasks": false, "idle_close_hours": 12, "archive_closed_days": 3,
        "agent_auto_naming": true, "agent_auto_naming_max": 1, "agent_auto_naming_interval_hours": 0,
    ] }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py") }
        hub = value
    }

    /// Each test starts from the hub's defaults, whatever an earlier one changed.
    @MainActor private func prepare() async throws {
        guard try await assistant()["agent_auto_naming"] != nil else { throw XCTSkip("this mock hub has no Agent Follow-Up settings") }
        try await reset()
    }

    @MainActor private func request(_ method: String, _ body: [String: Any]? = nil) async throws -> [String: Any] {
        var r = URLRequest(url: try XCTUnwrap(URL(string: hub + "/api/v1/assistant")))
        r.httpMethod = method
        r.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        r.setValue("assistant", forHTTPHeaderField: "X-Persona-Manager")
        if let body {
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
            r.httpBody = try JSONSerialization.data(withJSONObject: body)
        }
        let data = try await URLSession.shared.data(for: r).0
        return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    @MainActor private func assistant() async throws -> [String: Any] { try await request("GET") }
    @MainActor private func reset() async throws { _ = try await request("PUT", Self.defaults()) }

    /// Waits until the mock holds `value` for `key`.
    @MainActor private func hubHas(_ key: String, _ value: Int, file: StaticString = #filePath, line: UInt = #line) async throws {
        var now = try await assistant()[key] as? Int
        for _ in 0..<40 where now != value {
            try await Task.sleep(for: .milliseconds(250))
            now = try await assistant()[key] as? Int
        }
        XCTAssertEqual(now, value, key, file: file, line: line)
    }

    @MainActor private func launch(_ page: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", "", "-openAssistantPage", page]
        if let size = ProcessInfo.processInfo.environment["PERSONA_SHOTS_SIZE"] {
            app.launchArguments += ["-UIPreferredContentSizeCategoryName", size]
        }
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let image = app.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = env["PERSONA_SHOTS_DEVICE"] ?? "iphone"
        let size = env["PERSONA_SHOTS_SIZE"].map { $0.contains("XXXL") ? "-xxxl" : "-" + $0 } ?? ""
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try image.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/followup-\(device)-\(appearance)\(size)-\(name).png"))
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    /// The page as a person reaches it: Settings › Assistant Profile › Agent Follow-Up. (`-openAssistantPage <page>`
    /// opens a page under the Persona tab too, two copies of it; `assistant` opens the profile in Settings only.)
    @MainActor private func openPage() -> XCUIApplication {
        let app = launch("assistant")
        let row = element(app, "assistant-agent-follow-up")
        reveal(row, in: app)
        XCTAssertTrue(row.waitForExistence(timeout: 20), "no Agent Follow-Up row:\n\(app.debugDescription)")
        row.tap()
        XCTAssertTrue(app.switches["followup-start-requested"].firstMatch.waitForExistence(timeout: 10), "no Agent Follow-Up page")
        return app
    }

    /// Scrolls until `e` is on screen and clear of the bars.
    @MainActor private func reveal(_ e: XCUIElement, in app: XCUIApplication) {
        let band = app.windows.firstMatch.frame.insetBy(dx: 0, dy: 120)
        for _ in 0..<10 where !(e.exists && e.isHittable && band.contains(CGPoint(x: band.midX, y: e.frame.midY))) {
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
            let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45))
            from.press(forDuration: 0.05, thenDragTo: to)
            usleep(300_000)
        }
    }

    /// Opens the menu row `id` and picks the choice whose words are one of `labels`.
    @MainActor private func pick(_ id: String, _ labels: [String], in app: XCUIApplication) throws {
        let row = element(app, id)
        reveal(row, in: app)
        XCTAssertTrue(row.waitForExistence(timeout: 5), "no \(id):\n\(app.debugDescription)")
        row.tap()
        let choice = app.buttons.matching(NSPredicate(format: "label IN %@", labels)).firstMatch
        XCTAssertTrue(choice.waitForExistence(timeout: 5), "no \(labels) in \(id)'s menu:\n\(app.debugDescription)")
        choice.tap()
    }

    @MainActor func testTheProfileOpensThePageAndTheSwitchMovedThere() async throws {
        try await prepare()
        let app = launch("assistant")
        let row = element(app, "assistant-agent-follow-up")
        reveal(row, in: app)
        XCTAssertTrue(row.waitForExistence(timeout: 20), "no Agent Follow-Up row:\n\(app.debugDescription)")
        try shot(app, "profile-row")
        // The switch is on the new page now, not on the profile.
        for _ in 0..<8 { app.swipeUp() }
        XCTAssertFalse(app.switches["assistant-main-supervises-own-tasks"].exists)
        try shot(app, "profile-lower")
        for _ in 0..<8 { app.swipeDown() }
        reveal(row, in: app)
        row.tap()
        XCTAssertTrue(app.switches["assistant-main-supervises-own-tasks"].firstMatch.waitForExistence(timeout: 10))
    }

    @MainActor func testThePageShowsTheHubsDefaultsAndSavesAMenuChoice() async throws {
        try await prepare()
        let app = openPage()
        let start = app.switches["followup-start-requested"].firstMatch
        XCTAssertEqual(start.value as? String, "1")
        XCTAssertEqual(app.switches["followup-agent-tasks"].firstMatch.value as? String, "0")
        try shot(app, "top")

        try pick("followup-idle-close", ["不停止", "Never", "停止しない"], in: app)
        try await hubHas("idle_close_hours", 0)
        try pick("followup-archive-closed", ["30天", "30 天", "30 days", "30日"], in: app)
        try await hubHas("archive_closed_days", 30)
        reveal(element(app, "followup-auto-naming"), in: app)
        try shot(app, "tidying")
    }

    @MainActor func testSessionNamesUnlimitedAndRegularUpdates() async throws {
        try await prepare()
        let app = openPage()
        try pick("followup-naming-max", ["不限", "Unlimited", "無制限"], in: app)
        try await hubHas("agent_auto_naming_max", 1000)

        let periodic = app.switches["followup-naming-periodic"].firstMatch
        reveal(periodic, in: app)
        XCTAssertEqual(periodic.value as? String, "0")
        periodic.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap() // the switch, not the label
        try await hubHas("agent_auto_naming_interval_hours", 24)
        let every = element(app, "followup-naming-interval")
        XCTAssertTrue(every.waitForExistence(timeout: 5), "turning regular updates on shows how often")
        reveal(every, in: app)
        try shot(app, "names")
    }
}
