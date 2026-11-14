import XCTest

/// Final E: real quick-filter semantics, a fixed grouping-only menu, and visual checkpoints.
/// Run with PERSONA_CAPSULE_HUB pointing at the synthetic MOCK_AGENTS fixture.
final class AgentsFilterCapsuleUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_CAPSULE_HUB"] else {
            throw XCTSkip("needs PERSONA_CAPSULE_HUB")
        }
        hub = value
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "capsule failure tree"
            tree.lifetime = .keepAlways
            add(tree)
            let image = XCTAttachment(screenshot: app.screenshot())
            image.lifetime = .keepAlways
            add(image)
        }
        app?.terminate()
    }

    @MainActor private func launch(_ appearance: String, large: Bool = false) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-tab", "dev", "-dev.openChat", "",
                               "-openAgentsFresh", "YES", "-dev.grouping", "state", "-ui.scheme", appearance]
        if large { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchPinned()
        XCTAssertTrue(filter("all").waitForExistence(timeout: 25))
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        XCTAssertTrue(filter("unread").waitForExistence(timeout: 15))
        _ = app.revealAgentsSearch()
        XCTAssertFalse(app.keyboards.firstMatch.exists)
    }

    private func filter(_ id: String) -> XCUIElement { app.buttons["agents.filter." + id].firstMatch }
    private var menu: XCUIElement { app.buttons["agents.grouping"].firstMatch }
    private var strip: XCUIElement { app.scrollViews["agents.filters.scroll"].firstMatch }

    private func shot(_ name: String) {
        let image = XCTAttachment(screenshot: app.screenshot())
        image.name = name
        image.lifetime = .keepAlways
        add(image)
        // The host captures this exact settled screen with simctl, rather than exporting a reconstructed image.
        if let hold = ProcessInfo.processInfo.environment["PERSONA_CAPSULE_HOLD"].flatMap(Double.init) {
            print("CAPSULE_SHOT_READY \(name)")
            RunLoop.current.run(until: Date().addingTimeInterval(hold))
        }
    }

    @MainActor private func verify(_ appearance: String) {
        launch(appearance)
        XCTAssertTrue(filter("all").isSelected)
        let selectedValue = filter("all").value as? String
        let unread = filter("unread")
        let compactUnreadWidth = unread.frame.width
        XCTAssertNotEqual(unread.value as? String, selectedValue, "initial unfocused label is compact")
        XCTAssertGreaterThanOrEqual(menu.frame.width, 44)
        XCTAssertGreaterThanOrEqual(menu.frame.height, 44)
        XCTAssertGreaterThanOrEqual(filter("all").frame.height, 44)
        XCTAssertEqual(app.buttons.matching(identifier: "agents.grouping").count, 1)
        let search = app.searchFields.firstMatch
        XCTAssertEqual(strip.frame.minX, search.frame.minX, accuracy: 1)
        XCTAssertEqual(menu.frame.maxX, search.frame.maxX, accuracy: 1)
        let fixedFrame = menu.frame
        shot("\(appearance)-initial")

        let from = strip.coordinate(withNormalizedOffset: CGVector(dx: 0.85, dy: 0.5))
        let to = strip.coordinate(withNormalizedOffset: CGVector(dx: 0.48, dy: 0.5))
        from.press(forDuration: 0.1, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.3)
        let expanded = NSPredicate { _, _ in
            ["needsYou", "unread", "working", "backgroundWaiting", "feishu", "terminal"].contains {
                let item = self.filter($0)
                return item.exists && item.isHittable && item.value as? String == selectedValue && !item.isSelected
            }
        }
        let deadline = Date().addingTimeInterval(5)
        while !expanded.evaluate(with: nil), Date() < deadline {
            RunLoop.current.run(until: Date().addingTimeInterval(0.1))
        }
        XCTAssertTrue(expanded.evaluate(with: nil), "a centered, unselected filter expands")
        XCTAssertEqual(menu.frame.minX, fixedFrame.minX, accuracy: 1)
        XCTAssertEqual(menu.frame.minY, fixedFrame.minY, accuracy: 1)
        XCTAssertTrue(filter("all").isSelected, "scrolling does not change the filter")
        shot("\(appearance)-centered")

        unread.tap()
        XCTAssertTrue(unread.isSelected)
        XCTAssertEqual(unread.value as? String, selectedValue)
        XCTAssertGreaterThan(unread.frame.width, compactUnreadWidth, "selection expands the label's layout, not only its spoken value")
        XCTAssertFalse(app.keyboards.firstMatch.exists)
        shot("\(appearance)-selected")

        menu.tap()
        let group = app.buttons.matching(NSPredicate(format: "label IN %@", ["按项目", "By Project", "プロジェクト別"])).firstMatch
        XCTAssertTrue(group.waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label IN %@", ["按状态", "By Status", "状態別"])).firstMatch.exists)
        XCTAssertFalse(app.buttons["Sort By"].exists)
        XCTAssertFalse(app.buttons["Display"].exists)
        shot("\(appearance)-grouping")
        group.tap()
        XCTAssertTrue(unread.isSelected, "grouping does not change state selection")
        XCTAssertFalse(app.keyboards.firstMatch.exists)
        print("CAPSULE_VERDICT \(appearance): initial, centered, selected, grouping passed")
    }

    @MainActor func testLightStates() { verify("light") }
    @MainActor func testDarkStates() { verify("dark") }

    @MainActor private func verifyLargestText(_ appearance: String) {
        launch(appearance, large: true)
        XCTAssertTrue(menu.isHittable)
        XCTAssertGreaterThanOrEqual(menu.frame.width, 44)
        shot("\(appearance)-accessibility")
        menu.tap()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label IN %@", ["按项目", "By Project", "プロジェクト別"])).firstMatch.waitForExistence(timeout: 5))
    }

    @MainActor func testLargestLightTextKeepsGroupingReachable() { verifyLargestText("light") }
    @MainActor func testLargestDarkTextKeepsGroupingReachable() { verifyLargestText("dark") }
}
