import XCTest

/// Word lookup (#10977, docs/apple/ios.md "Word lookup (text_lookup)"): a tap on an English word of a reply in Persona's
/// conversation, an Agent's chat and an Inbox item opens the card (word, pronunciation, senses, Save / Saved, the
/// extension's name); a long press still lifts the message and opens no card; a word in a code block opens nothing; with
/// no extension answering, a tap opens nothing at all.
///   PORT=18977 MOCK_TEXT_LOOKUP=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_TEXT_LOOKUP_HUB=http://127.0.0.1:18977 [TEST_RUNNER_PERSONA_SHOTS=<dir>]
///   [TEST_RUNNER_PERSONA_SHOTS_APPEARANCE=light|dark] xcodebuild test … -only-testing:PersonaUITests/TextLookupUITests
final class TextLookupUITests: XCTestCase {
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private let agent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private let reply = "Deploy the weather widget after the review.\n\nThe latency stays low in landscape.\n\n```\ndeploy --now\n```"

    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var appearance: String { env["PERSONA_SHOTS_APPEARANCE"] ?? "light" }

    private func hubOrSkip() throws -> String {
        guard let hub = env["PERSONA_TEXT_LOOKUP_HUB"] else {
            throw XCTSkip("needs MOCK_TEXT_LOOKUP=1 scripts/mock/mock_hub.py (TEST_RUNNER_PERSONA_TEXT_LOOKUP_HUB)")
        }
        return hub
    }

    override func tearDown() {
        if let hub = env["PERSONA_TEXT_LOOKUP_HUB"] { _ = get(hub, "/mock/text-lookup?available=1&fail=0&reset=1") }
        super.tearDown()
    }

    @MainActor private func launch(_ hub: String, _ more: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-AppleLanguages", "(\(env["PERSONA_UI_LANGUAGE"] ?? "en"))",
                               "-ui.scheme", appearance] + more
        app.launchPinned()
        // The icon follows the light or dark scheme picked above, and iOS says so in an alert the first time.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let ok = springboard.alerts.buttons.firstMatch
        if ok.waitForExistence(timeout: 4) { ok.tap() }
        return app
    }

    /// The paragraph that starts with `prefix`, once it is on screen.
    @MainActor private func paragraph(_ app: XCUIApplication, _ prefix: String, timeout: TimeInterval = 20) -> XCUIElement {
        let el = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", prefix)).firstMatch
        XCTAssertTrue(el.waitForExistence(timeout: timeout), "no paragraph starting “\(prefix)”\n\(app.debugDescription)")
        return el
    }

    /// A finger on the first word of `el` (its first line, a few points in).
    @MainActor private func firstWord(_ el: XCUIElement) -> XCUICoordinate {
        el.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 14, dy: 11))
    }

    /// The reply comes to the conversations the app has open: said again until the page has subscribed and shows it.
    @MainActor private func say(_ app: XCUIApplication, _ hub: String) -> XCUIElement {
        let text = reply.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
        let el = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Deploy the weather")).firstMatch
        for _ in 0..<8 where !el.exists {
            Thread.sleep(forTimeInterval: 3)
            _ = get(hub, "/mock/say?text=" + text)
            _ = el.waitForExistence(timeout: 4)
        }
        return paragraph(app, "Deploy the weather")
    }

    @MainActor private func openCard(_ app: XCUIApplication, at el: XCUIElement, word: String) {
        firstWord(el).tap()
        let card = app.descendants(matching: .any)["textLookup.card"]
        XCTAssertTrue(card.waitForExistence(timeout: 5), "no lookup card\n\(app.debugDescription)")
        XCTAssertEqual(app.staticTexts["textLookup.word"].label, word)
        XCTAssertTrue(app.buttons["textLookup.save"].waitForExistence(timeout: 10), "the lookup did not load")
    }

    @MainActor func testChatWordOpensCardAndSaves() throws {
        let hub = try hubOrSkip()
        let app = launch(hub, ["-tab", "assistant", "-app.tab", "assistant", "-openAssistant", main])
        let el = say(app, hub)
        openCard(app, at: el, word: "Deploy")
        shot(app, "chat")
        let save = app.buttons["textLookup.save"]
        save.tap()
        let saved = NSPredicate(format: "value != ''")
        expectation(for: saved, evaluatedWith: save)
        waitForExpectations(timeout: 5)
        XCTAssertTrue(log(hub).contains { $0.first == "POST" && $0.dropFirst().first == "/api/v1/text-lookup/words" }, "Save did not reach the hub")
        shot(app, "chat-saved")
        // A tap outside closes it; the page is as it was.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap()
        XCTAssertTrue(app.descendants(matching: .any)["textLookup.card"].waitForNonExistence(timeout: 5))

        // A long press lifts the message (selection, its menu) and opens no card.
        firstWord(paragraph(app, "The latency")).press(forDuration: 0.9)
        XCTAssertTrue(app.descendants(matching: .any)["message-lift"].waitForExistence(timeout: 5), "the long press no longer lifts")
        XCTAssertFalse(app.descendants(matching: .any)["textLookup.card"].exists, "a long press opened a card")
    }

    @MainActor func testCodeBlockOpensNothing() throws {
        let hub = try hubOrSkip()
        let app = launch(hub, ["-tab", "assistant", "-app.tab", "assistant", "-openAssistant", main])
        _ = say(app, hub)
        let code = paragraph(app, "deploy --now")
        firstWord(code).tap()
        XCTAssertFalse(app.descendants(matching: .any)["textLookup.card"].waitForExistence(timeout: 2), "a word in a code block opened a card")
    }

    @MainActor func testAgentChatWordOpensCard() throws {
        let hub = try hubOrSkip()
        let app = launch(hub, ["-openSession", agent])
        let el = say(app, hub)
        openCard(app, at: el, word: "Deploy")
        shot(app, "agent")
    }

    @MainActor func testInboxItemWordOpensCard() throws {
        let hub = try hubOrSkip()
        let app = launch(hub, ["-tab", "mail", "-openInboxItem", "assistant:tl1"])
        // The item's summary under the bar, after the AI mark: "Deploy" is the word past it.
        let el = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Deploy summary")).firstMatch
        XCTAssertTrue(el.waitForExistence(timeout: 20), app.debugDescription)
        el.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 40, dy: 11)).tap()
        XCTAssertTrue(app.descendants(matching: .any)["textLookup.card"].waitForExistence(timeout: 5), "no lookup card\n\(app.debugDescription)")
        XCTAssertEqual(app.staticTexts["textLookup.word"].label, "Deploy")
        XCTAssertTrue(app.buttons["textLookup.save"].waitForExistence(timeout: 10), "the lookup did not load")
        shot(app, "inbox")
    }

    @MainActor func testNothingWithoutTheExtension() throws {
        let hub = try hubOrSkip()
        _ = get(hub, "/mock/text-lookup?available=0")
        let app = launch(hub, ["-tab", "assistant", "-app.tab", "assistant", "-openAssistant", main])
        let el = say(app, hub)
        firstWord(el).tap()
        XCTAssertFalse(app.descendants(matching: .any)["textLookup.card"].waitForExistence(timeout: 3), "a card without the extension")
        XCTAssertFalse(log(hub).contains { $0.dropFirst().first == "/api/v1/text-lookup/lookup" }, "looked up without the extension")
        shot(app, "unavailable")
    }

    // MARK: Helpers

    private func get(_ hub: String, _ path: String) -> Data? { try? Data(contentsOf: URL(string: hub + path)!) }

    /// What the app sent the mock's text-lookup routes ([method, path, body]).
    private func log(_ hub: String) -> [[String]] {
        guard let data = get(hub, "/mock/text-lookup"),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let rows = obj["log"] as? [[Any]] else { return [] }
        return rows.map { $0.prefix(2).compactMap { $0 as? String } }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        Thread.sleep(forTimeInterval: 0.6)
        let png = XCUIScreen.main.screenshot().pngRepresentation
        let a = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        a.name = "text-lookup-\(name)-\(appearance)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = env["PERSONA_SHOTS"] {
            try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("text-lookup-\(name)-\(appearance).png"))
        }
    }
}
