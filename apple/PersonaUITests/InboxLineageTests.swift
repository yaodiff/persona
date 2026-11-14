import XCTest

/// The lineage controls on an item's detail, against the existing MOCK_INBOX_LINE=1 fixture:
/// lark:f1:100 → lark:f2:200 → lark:f3:300, with a to-do above the first item. This suite does not use
/// MOCK_INBOX_EVENTS; its separate mock hub keeps the lineage fixture's native item pages. With the default roster the
/// opened item and mail:acc1:t4:504 share one manager; lark:f1:100 belongs to another and is not a valid choice.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> saves each step (exposed to the test runner as PERSONA_SHOTS).
final class InboxLineageTests: XCTestCase {
    private static let itemID = "lark:f2:200"
    private var app: XCUIApplication!
    private var hub: String!
    private var regularWidth = false
    private var layout: String { regularWidth ? "regular" : "phone" }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_LINE=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.name = "inbox-lineage-failure"
            shot.lifetime = .keepAlways
            add(shot)
        }
        app?.terminate()
        super.tearDown()
    }

    @MainActor func testThreadOffersNotTheSameMatter() {
        openDetail()

        let line = reveal(element("inbox.line"), what: "the Thread section")
        XCTAssertTrue(line.exists)
        requireHittable(element("inbox.line.notSame.lark:f1:100"), "Not the same matter on the top ancestor pair")
        requireHittable(element("inbox.line.notSame.lark:f2:200"), "Not the same matter on the nearest ancestor pair")
        requireHittable(element("inbox.line.notSame.lark:f3:300"), "Not the same matter on the child pair")
        requireHittable(element("inbox.line.same"), "Same matter as at the bottom of Thread")
        shot("thread-actions")
    }

    @MainActor func testMoreMenuOpensSameMatterPicker() {
        openDetail()

        requireHittable(element("inbox.detail.more"), "the item detail's More menu")
        element("inbox.detail.more").tap()
        let same = element("inbox.detail.sameMatter")
        requireHittable(same, "Same matter as in More")
        same.tap()

        let picker = element("inbox.sameMatter")
        XCTAssertTrue(picker.waitForExistence(timeout: 10), "the Same matter picker did not open:\n\(app.debugDescription)")
        XCTAssertTrue(app.searchFields.firstMatch.waitForExistence(timeout: 5), "the picker has no title/place search")
        requireHittable(element("inbox.sameMatter.row.mail:acc1:t4:504"), "an open candidate from the item's manager")
        XCTAssertFalse(element("inbox.sameMatter.row.lark:f2:200").exists, "the current item is offered as its own parent")
        XCTAssertFalse(element("inbox.sameMatter.row.lark:f3:300").exists, "a direct child is offered as its parent")
        XCTAssertFalse(element("inbox.sameMatter.row.lark:f1:100").exists, "another manager's item is offered as its parent")
        shot("same-matter-picker")
    }

    @MainActor private func openDetail() {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-openInboxItem", Self.itemID,
                               "-dev.openChat", "", "-openSplitFresh", "YES", "-inbox.kind", "", "-inbox.source", ""]
        app.launchPinned()
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 10), "no app window")
        regularWidth = app.windows.firstMatch.frame.width > 600
        requireHittable(element("inbox.detail.done"), "the lineage item detail")
        shot("detail")
    }

    @MainActor private func reveal(_ e: XCUIElement, what: String) -> XCUIElement {
        for _ in 0..<8 where !(e.exists && e.isHittable) {
            app.swipeUp(velocity: .slow)
            _ = e.waitForExistence(timeout: 1)
        }
        requireHittable(e, what)
        return e
    }

    @MainActor private func requireHittable(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) {
        let predicate = NSPredicate(format: "exists == true AND hittable == true")
        let expectation = XCTNSPredicateExpectation(predicate: predicate, object: e)
        let met = XCTWaiter().wait(for: [expectation], timeout: timeout) == .completed
        XCTAssertTrue(met, "\(what) is not on screen:\n\(app.debugDescription)")
    }

    @MainActor private func element(_ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    @MainActor private func shot(_ name: String) {
        let png = app.screenshot().pngRepresentation
        let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
        attachment.name = "inbox-lineage-\(layout)-\(name)\(appearance)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let file = "inbox-lineage-\(layout)-\(name)\(appearance)-\(UITestStyle.name).png"
        try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
    }
}
