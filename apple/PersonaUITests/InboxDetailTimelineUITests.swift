import XCTest

/// The inbox matter detail's three-step timeline and its no-suggestion instruction entries.
/// Run against scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 (PERSONA_MOCK_HUB).
/// TEST_RUNNER_PERSONA_SHOTS=<dir> saves each screen (exposed to the runner as PERSONA_SHOTS).
final class InboxDetailTimelineUITests: XCTestCase {
    private var regularWidth = false
    private var layout: String { regularWidth ? "regular" : "phone" }

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor func testTimelineShowsThreeNewestStepsThenAll() throws {
        let app = try open(item: "lark:oc_demo3:300")
        XCTAssertTrue(entry(app, "c4").waitForExistence(timeout: 15), "the fourth step is not shown")
        XCTAssertTrue(entry(app, "c5").exists && entry(app, "c6").exists, "the folded timeline is not the newest three")
        for id in ["c1", "c2", "c3"] {
            XCTAssertFalse(entry(app, id).exists, "older step \(id) is visible while folded")
        }
        let all = app.buttons["inbox.chain.all"].firstMatch
        reveal(all, in: app)
        shot(app, "timeline-folded")
        all.tap()
        for id in ["c1", "c2", "c3", "c4", "c5", "c6"] {
            XCTAssertTrue(entry(app, id).waitForExistence(timeout: 3), "expanded timeline is missing \(id)")
        }
        shot(app, "timeline-expanded")
    }

    @MainActor func testShortTimelineHasNothingToExpandAndOffersSaySomething() throws {
        let app = try open(item: "mail:a1:news:99")
        for id in ["c1", "c2"] {
            XCTAssertTrue(entry(app, id).waitForExistence(timeout: 15), "short timeline is missing \(id)")
        }
        XCTAssertFalse(app.buttons["inbox.chain.all"].exists, "two steps should all be shown without an expander")
        let say = app.buttons["inbox.instruction.say"].firstMatch
        XCTAssertTrue(say.waitForExistence(timeout: 10), "an open item without a suggestion has no Say something")
        XCTAssertTrue(app.buttons["说一句"].firstMatch.exists, "Say something does not use the shared Chinese wording")
        reveal(say, in: app)
        say.tap()
        shot(app, "say-something-open")
        let input = app.descendants(matching: .any)["inbox.instruction.text"].firstMatch
        XCTAssertTrue(input.waitForExistence(timeout: 3), "Say something did not open its input")
        shot(app, "say-something")
    }

    @MainActor func testFailedItemOffersTryAgainInsteadOfSaySomething() throws {
        let app = try open(item: "lark:oc_demo6:600")
        let retry = app.buttons["inbox.instruction.retry"].firstMatch
        XCTAssertTrue(retry.waitForExistence(timeout: 15), "a failed item has no Try again")
        XCTAssertTrue(app.buttons["再试一次"].firstMatch.exists, "Try again does not use the shared Chinese wording")
        XCTAssertFalse(app.buttons["inbox.instruction.say"].exists, "a failed item offers Say something instead of Try again")
        reveal(retry, in: app)
        shot(app, "try-again")
    }

    @MainActor private func open(item: String) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", "",
                               "-openInboxItem", item]
        app.launchPinned()
        acceptIconAlert()
        regularWidth = app.windows.firstMatch.frame.width > 600
        XCTAssertTrue(app.descendants(matching: .any)["inbox.chain"].firstMatch.waitForExistence(timeout: 20),
                      "no event timeline:\n\(app.debugDescription)")
        return app
    }

    @MainActor private func entry(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "value == %@", id)).firstMatch
    }

    @MainActor private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<8 where !(element.exists && element.isHittable) { app.swipeUp(velocity: .slow) }
        XCTAssertTrue(element.exists && element.isHittable, "control is not usable:\n\(app.debugDescription)")
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let png = app.screenshot().pngRepresentation
        let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        attachment.name = "\(name)-\(layout)"
        attachment.lifetime = .keepAlways
        add(attachment)
        let environment = ProcessInfo.processInfo.environment
        if let directory = environment["PERSONA_SHOTS"] {
            let look = environment["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
            try? png.write(to: URL(fileURLWithPath: directory).appending(path: "detail-\(name)-\(layout)-\(UITestStyle.name)\(look).png"))
        }
    }
}
