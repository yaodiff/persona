import XCTest

/// Regular width (iPad, iPhone Duo open): an item whose source is a native chat opens that chat from View original
/// inside the detail column, while the extension really declares `inbox_item` for that source — the declaration is
/// proven here against the hub, so the chat shown is the skip and not a hub that never had a slot. The detail column's
/// own leading edge drags back to the item, and a second visit opens the chat again rather than any cached page.
/// Against scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 MOCK_EXTENSIONS=1 MOCK_INBOX_ITEM_UI=1
/// (PERSONA_MOCK_HUB); skipped on a phone, which `InboxSourceDirectTests` covers.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> saves every step (exposed to the test runner as PERSONA_SHOTS).
final class InboxSourceDirectSplitTests: XCTestCase {
    private static let itemTitle = "张三"
    private static let nativeMessage = "客户催报价了"

    private var app: XCUIApplication!
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 MOCK_EXTENSIONS=1 MOCK_INBOX_ITEM_UI=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant", "-dev.openChat", "",
                               "-openSplitFresh", "YES", "-inbox.kind", "", "-inbox.source", ""]
        app.launchPinned()
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 10), "no app window")
        guard app.windows.firstMatch.frame.width > 600 else {
            throw XCTSkip("not a split: needs an iPad or iPhone Duo open")
        }
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.name = "inbox-source-direct-split-failure"
            shot.lifetime = .keepAlways
            add(shot)
        }
        app?.terminate()
        super.tearDown()
    }

    /// The extension does declare a page for this source: without it the chat would open because nothing was there to
    /// draw, and this test would prove nothing.
    @MainActor func testTheExtensionDeclaresTheItemPageItIsSkippedFor() throws {
        let (places, status) = try declaredInboxItemPlaces()
        guard status == 200 else {
            throw XCTSkip("the mock answered \(status) to the app's own read of the slot list: run it without MOCK_PAIR_TOKEN")
        }
        XCTAssertTrue(places.contains { $0["connector"] as? String == "lark" },
                      "the hub declares no inbox_item page for lark, so the direct chat proves no skip: \(places)")
    }

    @MainActor func testViewOriginalOpensTheChatInTheDetailColumnAndEdgeDragReturns() {
        openItem()

        element("inbox.viewSource").tap()
        assertChat()
        shot("view-2-chat")
        assertListStaysBeside()

        swipeBackInDetailColumn()
        assertDetail()
        shot("view-3-back")

        // Again: the second visit opens the chat too, with no page of the extension kept from the first.
        element("inbox.viewSource").tap()
        assertChat()
        shot("view-4-chat-again")
    }

    // MARK: - Opening the item

    @MainActor private func openItem() {
        let inbox = element("sidebar-inbox")
        requireHittable(inbox, "Inbox in the persistent sidebar")
        inbox.tap()
        requireHittable(element("inbox.more"), "the Inbox")

        guard let row = itemRow() else {
            return XCTFail("no visible row for \(Self.itemTitle):\n\(app.debugDescription)")
        }
        row.tapCenter()
        assertDetail()
        shot("view-1-detail")
    }

    /// The row in the list column: its title, hittable, on the divider's side of the Inbox's own toolbar (the list is
    /// not assumed to be the left column).
    @MainActor private func itemRow() -> XCUIElement? {
        let divider = element("split.divider.assistant.list")
        let inbox = element("inbox.more")
        _ = divider.waitForExistence(timeout: 10)
        for attempt in 0..<8 {
            let rows = app.staticTexts.matching(NSPredicate(format: "label == %@", Self.itemTitle)).allElementsBoundByIndex
            if let row = rows.first(where: { listSide($0, divider: divider, inbox: inbox) }) { return row }
            if attempt == 0 { _ = app.staticTexts[Self.itemTitle].firstMatch.waitForExistence(timeout: 10) }
            app.swipeUp(velocity: .slow)
        }
        return nil
    }

    /// `e` is in the list column: shown, and with the divider read, on the same side of it as the Inbox's toolbar.
    @MainActor private func listSide(_ e: XCUIElement, divider: XCUIElement, inbox: XCUIElement, hittable: Bool = true) -> Bool {
        guard e.exists, !hittable || e.isHittable else { return false }
        guard divider.exists, inbox.exists else { return true }
        return (e.frame.midX > divider.frame.midX) == (inbox.frame.midX > divider.frame.midX)
    }

    // MARK: - The chat, the list and the way back

    @MainActor private func assertChat() {
        let message = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", Self.nativeMessage)).firstMatch
        XCTAssertTrue(message.waitForExistence(timeout: 20), "View original did not open the native chat:\n\(app.debugDescription)")
        XCTAssertFalse(element("inbox-plugin-item-back").exists, "the extension's page kept its back strip over the chat")
        XCTAssertFalse(element("inbox-plugin-item").exists, "the extension's page is drawn for an item with a chat")
        XCTAssertFalse(app.buttons["在会话里看"].exists, "the extension's View-in-chat button is there instead of the chat")
        // One way back: the stack's own button; the extension's Back to event strip would be a second.
        let backs = app.navigationBars.buttons.allElementsBoundByIndex
            .filter { $0.exists && $0.isHittable && ($0.identifier == "BackButton" || $0.label == "返回") }
        XCTAssertEqual(backs.count, 1, "the chat page has \(backs.count) back buttons:\n\(app.debugDescription)")
    }

    /// The push stays in the detail column: the list keeps its rows beside the chat.
    @MainActor private func assertListStaysBeside() {
        let divider = element("split.divider.assistant.list"), inbox = element("inbox.more")
        XCTAssertTrue(divider.exists, "the split lost its divider when the chat opened")
        let rows = app.staticTexts.matching(NSPredicate(format: "label == %@", Self.itemTitle)).allElementsBoundByIndex
        XCTAssertTrue(rows.contains { listSide($0, divider: divider, inbox: inbox, hittable: false) },
                      "the inbox list is gone from beside the chat:\n\(app.debugDescription)")
    }

    @MainActor private func assertDetail() {
        requireHittable(element("inbox.detail.done"), "the item detail")
        let source = element("inbox.viewSource")
        for _ in 0..<8 where !(source.exists && source.isHittable) {
            app.swipeDown(velocity: .slow)
        }
        requireHittable(source, "View original on the item detail")
    }

    /// Back as a finger does it, from the window's left edge: on an iPad the navigation bar spans the window (minX 0),
    /// so neither the divider's edge nor a right column's bar names where the pop starts.
    @MainActor private func swipeBackInDetailColumn() {
        sleep(1) // once the push has settled
        let window = app.windows.firstMatch
        window.coordinate(withNormalizedOffset: CGVector(dx: 0, dy: 0.5))
            .press(forDuration: 0.1, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.5)),
                   withVelocity: .slow, thenHoldForDuration: 0.1)
    }

    // MARK: - The hub's declarations

    /// The hub's `inbox_item` contributions, as it lists them to the app, and the answer's status.
    @MainActor private func declaredInboxItemPlaces() throws -> ([[String: Any]], Int) {
        guard let url = URL(string: hub + "/api/v1/plugins/-/ui?placement=inbox_item") else { return ([], 0) }
        var out: [[String: Any]] = []
        var status = 0
        let done = expectation(description: "plugins ui")
        URLSession.shared.dataTask(with: url) { data, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            if let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let ui = obj["ui"] as? [[String: Any]] {
                out = ui.filter { ($0["placement"] as? String) == "inbox_item" }
            }
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return (out, status)
    }

    // MARK: -

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
        attachment.name = "inbox-source-direct-split-\(name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(
            "inbox-source-direct-split-\(name)-\(UITestStyle.name).png"))
    }
}
