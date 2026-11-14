import XCTest

/// An inbox item whose source is a native chat must not detour through an extension's inbox_item page. Both View original
/// and the Received chain row open the chat itself, and a slow edge drag returns to the item with View original usable.
/// Against scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 MOCK_EXTENSIONS=1 MOCK_INBOX_ITEM_UI=1 (PERSONA_MOCK_HUB),
/// so an extension really declares inbox_item for this source. Runs on a phone and at regular width (>600): the phone
/// opens Inbox from its drawer, while regular width uses the persistent sidebar and a slow drag from the window's leading edge.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> saves every step (exposed to the test runner as PERSONA_SHOTS).
final class InboxSourceDirectTests: XCTestCase {
    private static let itemID = "lark:oc_demo1:100"
    private static let itemTitle = "张三"
    private static let nativeMessage = "客户催报价了"
    private static let focusedMessage = "在吗"
    private static let receivedText = "收到 3 条新消息 · 张三"
    private static let extensionViewInChat = "在会话里看"

    private var app: XCUIApplication!
    private var regularWidth = false
    private var layout: String { regularWidth ? "regular" : "phone" }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INBOX_EVENTS=1 MOCK_EXTENSIONS=1 MOCK_INBOX_ITEM_UI=1 (PERSONA_MOCK_HUB)")
        }
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant", "-dev.openChat", "",
                               "-openSplitFresh", "YES", "-inbox.kind", "", "-inbox.source", "", "-inbox.grouping", ""]
        app.launchPinned()
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 10), "no app window")
        regularWidth = app.windows.firstMatch.frame.width > 600
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.name = "inbox-source-direct-failure"
            shot.lifetime = .keepAlways
            add(shot)
        }
        app?.terminate()
        super.tearDown()
    }

    @MainActor func testViewOriginalOpensNativeChatDirectlyAndEdgeDragReturns() {
        openItem(route: "view")

        element("inbox.viewSource").tap()
        assertDirectChat(route: "View original")
        shot("view-3-chat")

        swipeBack()
        assertDetail(route: "View original")
        shot("view-4-back")
    }

    @MainActor func testReceivedChainOpensNativeChatDirectlyAndEdgeDragReturns() {
        openItem(route: "chain")

        let all = element("inbox.chain.all")
        if all.exists {
            reveal(all, what: "Show all in the Received chain")
            all.tap()
        }
        shot("chain-3-expanded")

        guard let received = receivedRow() else {
            return XCTFail("no Received source row for \(Self.itemID):\n\(app.debugDescription)")
        }
        received.tapCenter()
        assertDirectChat(route: "the Received chain")
        shot("chain-4-chat")

        swipeBack()
        assertDetail(route: "the Received chain")
        shot("chain-5-back")
    }

    // MARK: - Opening the item

    @MainActor private func openItem(route: String) {
        if regularWidth {
            let inbox = element("sidebar-inbox")
            requireHittable(inbox, "Inbox in the persistent sidebar")
            inbox.tap()
        } else {
            let drawer = app.buttons["assistant-sidebar-button"].firstMatch
            requireHittable(drawer, "the Assistant drawer button")
            drawer.tapCenter()
            let inbox = element("sidebar-inbox")
            requireHittable(inbox, "Inbox in the drawer")
            inbox.tap()
        }
        requireHittable(element("inbox.more"), "the Inbox")
        shot("\(route)-1-inbox")

        guard let row = itemRow() else {
            return XCTFail("no visible row for \(Self.itemID):\n\(app.debugDescription)")
        }
        row.tapCenter()
        assertDetail(route: route)
        shot("\(route)-2-detail")
    }

    /// The row's title, as InboxSplitTests finds it. A short bounded scroll covers a phone whose first screen ends above it.
    @MainActor private func itemRow() -> XCUIElement? {
        for attempt in 0..<10 {
            if let row = visibleItemRow() { return row }
            if attempt == 0 {
                _ = app.staticTexts[Self.itemTitle].firstMatch.waitForExistence(timeout: 10)
            } else {
                _ = app.staticTexts[Self.itemTitle].firstMatch.waitForExistence(timeout: 1)
            }
            if let row = visibleItemRow() { return row }
            app.swipeUp(velocity: .slow)
        }
        return visibleItemRow()
    }

    @MainActor private func visibleItemRow() -> XCUIElement? {
        let byIdentifier = app.staticTexts.matching(identifier: Self.itemTitle).allElementsBoundByIndex
        if let row = byIdentifier.first(where: { usableItemRow($0) }) { return row }
        let byLabel = app.staticTexts.matching(NSPredicate(format: "label == %@", Self.itemTitle)).allElementsBoundByIndex
        return byLabel.first(where: { usableItemRow($0) })
    }

    /// At regular width, a matching title belongs to the Inbox only on the same side of the divider as its toolbar.
    @MainActor private func usableItemRow(_ row: XCUIElement) -> Bool {
        guard row.exists && row.isHittable else { return false }
        guard regularWidth else { return true }
        let divider = element("split.divider.assistant.list"), inbox = element("inbox.more")
        guard divider.exists, inbox.exists else { return true }
        return (row.frame.midX > divider.frame.midX) == (inbox.frame.midX > divider.frame.midX)
    }

    // MARK: - Direct native chat

    @MainActor private func assertDirectChat(route: String) {
        let message = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", Self.nativeMessage)).firstMatch
        XCTAssertTrue(message.waitForExistence(timeout: 20), "\(route) did not open the native chat:\n\(app.debugDescription)")
        let focused = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", Self.focusedMessage)).firstMatch
        XCTAssertTrue(focused.waitForExistence(timeout: 5), "\(route) did not show the expected chat focus:\n\(app.debugDescription)")
        XCTAssertFalse(element("inbox-plugin-item-back").exists,
                       "\(route) kept the extension's intermediate-page back strip")
        XCTAssertFalse(app.buttons[Self.extensionViewInChat].exists,
                       "\(route) kept the extension's View-in-chat button instead of opening the chat directly")
    }

    @MainActor private func assertDetail(route: String) {
        requireHittable(element("inbox.detail.done"), "the item detail after \(route)")
        let source = element("inbox.viewSource")
        // The chain can leave the detail scrolled below its header; bring View original back on screen before using it.
        for _ in 0..<8 where !(source.exists && source.isHittable) {
            app.swipeDown(velocity: .slow)
        }
        requireHittable(source, "View original usable on the item detail after \(route)")
    }

    // MARK: - Received chain

    /// Prefer the row's own identifier. If SwiftUI's chain container masks its children, its visible fixture text is the row.
    @MainActor private func receivedRow() -> XCUIElement? {
        for _ in 0..<8 {
            if let row = visibleReceivedRow() { return row }
            _ = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", Self.receivedText)).firstMatch
                .waitForExistence(timeout: 1)
            if let row = visibleReceivedRow() { return row }
            app.swipeUp(velocity: .slow)
        }
        return visibleReceivedRow()
    }

    @MainActor private func visibleReceivedRow() -> XCUIElement? {
        let identified = app.descendants(matching: .any).matching(identifier: "inbox.chain.source").allElementsBoundByIndex
        if let row = identified.first(where: { $0.exists && $0.isHittable }) { return row }
        let text = NSPredicate(format: "label CONTAINS %@", Self.receivedText)
        return app.descendants(matching: .any).matching(text).allElementsBoundByIndex
            .first(where: { $0.exists && $0.isHittable })
    }

    // MARK: - Navigation and evidence

    /// Back as a finger does it. A regular-width navigation bar spans the window, whose leading edge starts the pop.
    @MainActor private func swipeBack() {
        sleep(1) // once the push has settled
        let window = app.windows.firstMatch
        let start = window.coordinate(withNormalizedOffset: CGVector(dx: regularWidth ? 0.0 : 0.005, dy: 0.5))
        let end = window.coordinate(withNormalizedOffset: CGVector(dx: regularWidth ? 0.6 : 0.9, dy: 0.5))
        start.press(forDuration: 0.1, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 0.1)
    }

    @MainActor private func reveal(_ e: XCUIElement, what: String) {
        for _ in 0..<8 where !(e.exists && e.isHittable) {
            app.swipeUp(velocity: .slow)
        }
        requireHittable(e, what)
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
        attachment.name = "inbox-source-direct-\(layout)-\(name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let file = "inbox-source-direct-\(layout)-\(name)-\(UITestStyle.name).png"
        try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
    }
}
