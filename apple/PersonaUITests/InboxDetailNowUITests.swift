import XCTest

/// The inbox detail's shared where/who header and current-state sentence, including a done state with no other event fields.
/// Run against scripts/mock/mock_hub.py with MOCK_INBOX_SHOWCASE=1 (PERSONA_MOCK_HUB).
/// TEST_RUNNER_PERSONA_SHOTS=<dir> saves each screen (exposed to the runner as PERSONA_SHOTS).
final class InboxDetailNowUITests: XCTestCase {
    private var regularWidth = false
    private var layout: String { regularWidth ? "regular" : "phone" }

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor func testWaitingDetailUsesSharedHeaderStateAndClock() throws {
        let app = try open(item: "sc-1")
        XCTAssertTrue(text("订单群 · 张三：好的，我看一下新的", in: app).waitForExistence(timeout: 15),
                      "the detail header is not where · who: newest line")
        XCTAssertTrue(app.descendants(matching: .any)["inbox.now"].firstMatch.exists, "no shared state line")
        XCTAssertTrue(containing("等张三确认新报价", in: app).waitForExistence(timeout: 3), "no waiting state sentence")
        XCTAssertTrue(containing("问更正后的报价行不行", in: app).exists, "the next step is missing")
        XCTAssertTrue(app.descendants(matching: .any)["inbox.now.symbol.time"].firstMatch.exists, "waiting does not use the still clock")
        shot(app, "waiting")
    }

    @MainActor func testSuggestionKeepsTheSharedStateSentence() throws {
        let app = try open(item: "sc-2")
        XCTAssertTrue(text("等你确认回复", in: app).waitForExistence(timeout: 15), "the suggestion replaced the shared state sentence")
        XCTAssertTrue(app.descendants(matching: .any)["inbox.suggestion"].firstMatch.exists, "no suggestion card")
        XCTAssertTrue(app.descendants(matching: .any)["inbox.now.symbol.needsYou"].firstMatch.exists, "no Needs-you mark")
        shot(app, "confirm-reply")
    }

    @MainActor func testDoneStateAloneOpensTheEventDetail() throws {
        let app = try open(item: "sc-5")
        XCTAssertTrue(text("赵六：收到，谢谢", in: app).waitForExistence(timeout: 15), "a direct chat names its person once")
        XCTAssertTrue(text("你回过了", in: app).waitForExistence(timeout: 3), "the done state sentence is missing")
        XCTAssertTrue(app.descendants(matching: .any)["inbox.now"].firstMatch.exists, "a now-only item did not open the event detail")
        shot(app, "done-replied")
    }

    @MainActor private func open(item: String) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_SHOWCASE=1 (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", "", "-inbox.grouping", "",
                               "-openInboxItem", item]
        app.launchPinned()
        acceptIconAlert()
        regularWidth = app.windows.firstMatch.frame.width > 600
        XCTAssertTrue(app.descendants(matching: .any)["inbox.viewSource"].firstMatch.waitForExistence(timeout: 20),
                      "no event detail:\n\(app.debugDescription)")
        return app
    }

    @MainActor private func text(_ words: String, in app: XCUIApplication) -> XCUIElement {
        app.staticTexts[words].firstMatch
    }

    @MainActor private func containing(_ words: String, in app: XCUIApplication) -> XCUIElement {
        app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
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
            try? png.write(to: URL(fileURLWithPath: directory).appending(path: "detail-now-\(name)-\(layout)-\(UITestStyle.name)\(look).png"))
        }
    }
}
