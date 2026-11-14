import XCTest

/// Assistant Profile › Standing Authorizations (#7002) against scripts/mock/mock_hub.py with
/// MOCK_STANDING_AUTHORIZATIONS=1 (PERSONA_MOCK_HUB): the profile's row with the count, the list in plain words, a grant's
/// page, the confirmation before a revoke, the row leaving, and the empty list. A mock without the grants skips. With
/// PERSONA_SHOTS set it writes what it saw there (PERSONA_SHOTS_APPEARANCE names the appearance; PERSONA_SHOTS_SIZE,
/// a content size category such as UICTContentSizeCategoryXXXL, launches at that text size; PERSONA_SHOTS_DEVICE names
/// the device in the file names).
final class StandingAuthorizationsUITests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_STANDING_AUTHORIZATIONS=1 (PERSONA_MOCK_HUB)")
        }
        hub = value
    }

    /// Each test starts from the mock's two grants (a revoke in an earlier test is undone).
    @MainActor private func prepare() async throws {
        _ = try await call("/mock/reset")
        guard try await grants().contains("sa_mock_weather") else {
            throw XCTSkip("this mock hub has no standing authorizations: start it with MOCK_STANDING_AUTHORIZATIONS=1")
        }
    }

    @MainActor private func call(_ path: String, method: String = "GET") async throws -> Data {
        var request = URLRequest(url: try XCTUnwrap(URL(string: hub + path)))
        request.httpMethod = method
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        return try await URLSession.shared.data(for: request).0
    }

    /// The ids the mock lists now.
    @MainActor private func grants() async throws -> [String] {
        let body = try JSONSerialization.jsonObject(with: try await call("/api/v1/assistant/standing-authorizations")) as? [String: Any]
        return (body?["authorizations"] as? [[String: Any]] ?? []).compactMap { $0["id"] as? String }
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
        try image.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/standing-\(device)-\(appearance)\(size)-\(name).png"))
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    /// The confirmation's destructive button: by its identifier, else by its words.
    private func confirm(_ app: XCUIApplication) -> XCUIElement {
        let byID = app.buttons["standing.confirm"].firstMatch
        if byID.exists { return byID }
        return app.buttons.matching(NSPredicate(format: "label IN %@", ["撤销授权", "Revoke Authorization", "承認を取り消す"])).firstMatch
    }

    @MainActor func testTheProfileListsThemAndARevokeConfirmsFirstThenTheRowLeaves() async throws {
        try await prepare()
        let app = launch("standing")
        let weather = element(app, "standing.sa_mock_weather")
        XCTAssertTrue(weather.waitForExistence(timeout: 20), "no standing authorizations:\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "standing.sa_mock_blog").exists)
        // `-openAssistantPage` opens the page under the Persona tab as well as in Settings: count the row's copies, and
        // wait for the one on screen to go.
        let copies = app.descendants(matching: .any).matching(identifier: "standing.sa_mock_weather")
        let before = copies.count
        try shot(app, "list")

        // Its page: what, where, when, how long, and Revoke at the end.
        weather.tap()
        let revoke = app.buttons["standing.revoke"].firstMatch
        XCTAssertTrue(app.staticTexts["/Users/me/code/weather"].firstMatch.waitForExistence(timeout: 10), "the folder in full")
        try shot(app, "detail")
        for _ in 0..<8 where !revoke.isHittable { app.swipeUp() }
        XCTAssertTrue(revoke.isHittable)
        try shot(app, "detail-end")

        // Revoke asks first; Cancel leaves it.
        revoke.tap()
        XCTAssertTrue(confirm(app).waitForExistence(timeout: 5), "no confirmation:\n\(app.debugDescription)")
        try shot(app, "confirm")
        confirm(app).tap()

        // The page closes and the row is gone at once; the hub has revoked it.
        for _ in 0..<40 where copies.count >= before { try await Task.sleep(for: .milliseconds(250)) }
        XCTAssertEqual(copies.count, before - 1, "the revoked row stayed")
        XCTAssertTrue(element(app, "standing.sa_mock_blog").exists)
        var listed = try await grants()
        for _ in 0..<20 where listed.contains("sa_mock_weather") {
            try await Task.sleep(for: .milliseconds(250))
            listed = try await grants()
        }
        XCTAssertEqual(listed, ["sa_mock_blog"])
        try shot(app, "revoked")
    }

    @MainActor func testASwipeAsksFirstAndCancelKeepsIt() async throws {
        try await prepare()
        let app = launch("standing")
        let blog = element(app, "standing.sa_mock_blog")
        XCTAssertTrue(blog.waitForExistence(timeout: 20), "no standing authorizations:\n\(app.debugDescription)")
        blog.swipeLeft()
        let swipe = app.buttons.matching(NSPredicate(format: "label IN %@", ["撤销", "Revoke", "取り消す"])).firstMatch
        XCTAssertTrue(swipe.waitForExistence(timeout: 5), "no Revoke on the swipe:\n\(app.debugDescription)")
        try shot(app, "swipe")
        swipe.tap()
        XCTAssertTrue(confirm(app).waitForExistence(timeout: 5), "the swipe revoked without asking")
        let cancel = app.buttons.matching(NSPredicate(format: "label IN %@", ["取消", "Cancel", "キャンセル"])).firstMatch
        if cancel.exists { cancel.tap() } else { app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.1)).tap() } // iPad's popover
        XCTAssertTrue(confirm(app).waitForNonExistence(timeout: 5))
        XCTAssertTrue(blog.exists, "Cancel kept the row")
        let listed = try await grants()
        XCTAssertEqual(listed, ["sa_mock_weather", "sa_mock_blog"], "nothing was sent")
    }

    @MainActor func testTheProfileRowCountsThemAndAnEmptyListSaysPersonaAsksFirst() async throws {
        try await prepare()
        let app = launch("assistant")
        let row = element(app, "assistant-standing-authorizations")
        for _ in 0..<6 where !(row.exists && row.isHittable) { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 20), "no Standing Authorizations row:\n\(app.debugDescription)")
        XCTAssertTrue(row.label.contains("2"), "the count: \(row.label)")
        try shot(app, "profile-row")

        _ = try await call("/api/v1/assistant/standing-authorizations/sa_mock_weather", method: "DELETE")
        _ = try await call("/api/v1/assistant/standing-authorizations/sa_mock_blog", method: "DELETE")
        row.tap()
        XCTAssertTrue(element(app, "standing.empty").waitForExistence(timeout: 20), "no empty state:\n\(app.debugDescription)")
        try shot(app, "empty")
    }
}
