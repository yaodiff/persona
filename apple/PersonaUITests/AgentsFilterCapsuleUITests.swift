import XCTest

/// Whole filter names (#9121), real quick-filter semantics, a fixed grouping-only menu, and visual checkpoints.
/// Run with PERSONA_CAPSULE_HUB pointing at the synthetic mock hub with MOCK_AGENTS=1 MOCK_BOTMUX=1 MOCK_BG_WAITING=1: every
/// filter is in the strip, the Lark group and Waiting in the background included. PERSONA_SHOTS=<directory> also writes
/// the checkpoints as PNG files.
final class AgentsFilterCapsuleUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""

    /// The filters in the strip's order, and each one's name in the languages the app ships in (`AgentFilter.title`).
    private static let filters = ["all", "needsYou", "unread", "working", "backgroundWaiting", "feishu", "terminal"]
    private static let names: [String: [String: String]] = [
        "en": ["all": "All", "needsYou": "Needs You", "unread": "Unread", "working": "Working",
               "backgroundWaiting": "Waiting in the background", "feishu": "Lark Group", "terminal": "Terminal"],
        "zh-Hans": ["all": "全部", "needsYou": "需要你", "unread": "完成未看", "working": "工作中",
                    "backgroundWaiting": "后台等待中", "feishu": "飞书群", "terminal": "终端"],
        "ja": ["all": "すべて", "needsYou": "要対応", "unread": "未確認", "working": "作業中",
               "backgroundWaiting": "バックグラウンドで待機中", "feishu": "Larkグループ", "terminal": "ターミナル"],
    ]

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

    @MainActor private func launch(_ appearance: String, large: Bool = false, language: String? = nil) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-tab", "dev", "-dev.openChat", "",
                               "-openAgentsFresh", "YES", "-dev.grouping", "state", "-ui.scheme", appearance]
        if large { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        // The later `-AppleLanguages` wins over the scheme's zh-Hans.
        if let language { app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language == "zh-Hans" ? "zh_CN" : language] }
        app.launchPinned()
        XCTAssertTrue(filter("all").waitForExistence(timeout: 25))
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        XCTAssertTrue(filter("unread").waitForExistence(timeout: 15))
        _ = app.revealAgentsSearch()
        XCTAssertFalse(app.keyboards.firstMatch.exists)
    }

    private func filter(_ id: String) -> XCUIElement { app.buttons["agents.filter." + id].firstMatch }

    @MainActor func testProjectHeadingOpensNewAgentInTheExactFolder() throws {
        let environment = ProcessInfo.processInfo.environment
        guard environment["PERSONA_PROJECT_HEADINGS"] == "1" else { throw XCTSkip("needs the project heading fixture") }
        let appearance = environment["PERSONA_STYLE"] ?? "light"
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-tab", "dev", "-dev.openChat", "",
                               "-openAgentsFresh", "YES", "-dev.grouping", "space", "-ui.scheme", appearance,
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launchPinned()
        XCTAssertTrue(filter("all").waitForExistence(timeout: 25))
        func projectButton(_ heading: String) -> XCUIElement {
            let predicate = NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "agents.project.new", heading)
            return app.descendants(matching: .any).matching(predicate).firstMatch
        }
        let desktop = projectButton("site · ~/Desktop")
        let code = projectButton("site · ~/code")
        app.bringAboveTabBar(desktop)
        app.bringAboveTabBar(code)
        XCTAssertTrue(code.isHittable, app.debugDescription)
        XCTAssertFalse(projectButton("No project").exists)
        for _ in 0..<8 where !desktop.isHittable { app.swipeDown(velocity: .slow) }
        XCTAssertTrue(desktop.isHittable, app.debugDescription)
        shot("project-headings-\(appearance)")
        desktop.tap()
        let directory = app.buttons["newChat.directory"].firstMatch
        XCTAssertTrue(directory.waitForExistence(timeout: 20))
        let folderKnown = expectation(for: NSPredicate { _, _ in
            directory.label.contains("~/Desktop/site") || self.app.staticTexts["~/Desktop/site"].exists
        }, evaluatedWith: directory)
        XCTAssertEqual(XCTWaiter.wait(for: [folderKnown], timeout: 15), .completed)
        shot("project-new-agent-\(appearance)")
    }

    private var menu: XCUIElement { app.buttons["agents.grouping"].firstMatch }
    private var strip: XCUIElement { app.scrollViews["agents.filters.scroll"].firstMatch }
    /// The filters this list has, in the strip's order.
    private var present: [String] { Self.filters.filter { filter($0).exists } }
    /// Each present filter's width: the name, whole, and what goes with it.
    private var widths: [String: CGFloat] { Dictionary(uniqueKeysWithValues: present.map { ($0, filter($0).frame.width) }) }

    /// Every filter is as wide as it was, to the fraction of a point layout rounds.
    private func assertWidths(_ expected: [String: CGFloat], _ message: String) {
        let now = widths
        XCTAssertEqual(Set(now.keys), Set(expected.keys), message)
        for (id, width) in expected { XCTAssertEqual(now[id] ?? 0, width, accuracy: 0.5, "\(message): \(id)") }
    }

    private func shot(_ name: String) {
        let screenshot = app.screenshot()
        let image = XCTAttachment(screenshot: screenshot)
        image.name = name
        image.lifetime = .keepAlways
        add(image)
        if let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent(name + ".png"))
        }
        // The host captures this exact settled screen with simctl, rather than exporting a reconstructed image.
        if let hold = ProcessInfo.processInfo.environment["PERSONA_CAPSULE_HOLD"].flatMap(Double.init) {
            print("CAPSULE_SHOT_READY \(name)")
            RunLoop.current.run(until: Date().addingTimeInterval(hold))
        }
    }

    /// One slow drag across the strip, to its left (`toEnd`) or to its right.
    @MainActor private func drag(toEnd: Bool) {
        let near = strip.coordinate(withNormalizedOffset: CGVector(dx: 0.88, dy: 0.5))
        let far = strip.coordinate(withNormalizedOffset: CGVector(dx: 0.12, dy: 0.5))
        (toEnd ? near : far).press(forDuration: 0.1, thenDragTo: toEnd ? far : near, withVelocity: .slow, thenHoldForDuration: 0.3)
    }

    /// Drags until `target` lies inside the strip.
    @MainActor private func scroll(to id: String, toEnd: Bool) {
        let target = filter(id)
        for _ in 0..<12 where target.frame.minX < strip.frame.minX - 1 || target.frame.maxX > strip.frame.maxX + 1 { drag(toEnd: toEnd) }
        XCTAssertGreaterThanOrEqual(target.frame.minX, strip.frame.minX - 1, "\(id) is left of the strip")
        XCTAssertLessThanOrEqual(target.frame.maxX, strip.frame.maxX + 1, "\(id) is right of the strip")
    }

    private func inside(_ id: String) -> Bool {
        filter(id).frame.minX >= strip.frame.minX - 1 && filter(id).frame.maxX <= strip.frame.maxX + 1
    }

    @MainActor private func verify(_ appearance: String) {
        launch(appearance)
        XCTAssertTrue(filter("all").isSelected)
        let first = present.first!, last = present.last!
        XCTAssertGreaterThanOrEqual(present.count, 6, "the fixture has the filters the capsule is checked with: \(present)")
        XCTAssertGreaterThanOrEqual(menu.frame.width, 44)
        XCTAssertGreaterThanOrEqual(menu.frame.height, 44)
        XCTAssertGreaterThanOrEqual(filter("all").frame.height, 44)
        XCTAssertEqual(app.buttons.matching(identifier: "agents.grouping").count, 1)
        let search = app.searchFields.firstMatch
        XCTAssertEqual(strip.frame.minX, search.frame.minX, accuracy: 1)
        XCTAssertEqual(menu.frame.maxX, search.frame.maxX, accuracy: 1)
        let fixedFrame = menu.frame
        let unread = filter("unread")
        let widths = self.widths
        XCTAssertTrue(inside(first), "the strip starts at its first filter")
        XCTAssertFalse(inside(last), "the filters do not all fit: the strip scrolls to the rest, no name is cut to fit")
        shot("\(appearance)-initial")

        // Scrolling to the end brings the last filter in, whole, and takes the first one out.
        scroll(to: last, toEnd: true)
        XCTAssertFalse(inside(first), "the strip has left its start")
        assertWidths(widths, "scrolling leaves every filter as wide as it was")
        XCTAssertEqual(menu.frame.minX, fixedFrame.minX, accuracy: 1)
        XCTAssertEqual(menu.frame.minY, fixedFrame.minY, accuracy: 1)
        XCTAssertTrue(filter("all").isSelected, "scrolling does not change the filter")
        shot("\(appearance)-end")

        // Choosing a filter brings it into the strip and leaves its width as it was.
        unread.tap()
        XCTAssertTrue(unread.isSelected)
        XCTAssertFalse(filter("all").isSelected)
        XCTAssertTrue(waitInside("unread"), "a chosen filter comes into view")
        assertWidths(widths, "choosing a filter leaves every filter as wide as it was")
        XCTAssertFalse(app.keyboards.firstMatch.exists)
        shot("\(appearance)-selected")

        // The last filter, reached by tapping it from where the strip stands.
        filter(last).tap()
        XCTAssertTrue(filter(last).isSelected)
        XCTAssertTrue(waitInside(last), "the last filter is whole in the strip once chosen")
        assertWidths(widths, "choosing the last filter leaves every filter as wide as it was")
        unread.tap()
        XCTAssertTrue(unread.isSelected)

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
        print("CAPSULE_VERDICT \(appearance): initial, end, selected, grouping passed")
    }

    /// Waits for a filter to lie inside the strip (the scroll to a chosen filter glides).
    @MainActor private func waitInside(_ id: String) -> Bool {
        let deadline = Date().addingTimeInterval(5)
        while !inside(id), Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.1)) }
        return inside(id)
    }

    @MainActor func testLightStates() { verify("light") }
    @MainActor func testDarkStates() { verify("dark") }

    @MainActor private func verifyLargestText(_ appearance: String) {
        launch(appearance, large: true)
        XCTAssertTrue(menu.isHittable)
        XCTAssertGreaterThanOrEqual(menu.frame.width, 44)
        let last = present.last!
        XCTAssertFalse(inside(last), "at the largest text the strip scrolls to most of its filters")
        shot("\(appearance)-accessibility")
        scroll(to: last, toEnd: true)
        XCTAssertTrue(filter(last).isHittable, "the last filter can be tapped once the strip has scrolled to it")
        shot("\(appearance)-accessibility-end")
        menu.tap()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label IN %@", ["按项目", "By Project", "プロジェクト別"])).firstMatch.waitForExistence(timeout: 5))
    }

    @MainActor func testLargestLightTextKeepsGroupingReachable() { verifyLargestText("light") }
    @MainActor func testLargestDarkTextKeepsGroupingReachable() { verifyLargestText("dark") }

    /// Every filter is named whole in each language: a name that is not the whole one, or one cut with an ellipsis, is not
    /// what the strip says. Counts follow the name; the selected filter says so.
    @MainActor private func verifyNames(_ language: String) {
        launch("light", language: language)
        let expected = Self.names[language]!
        for id in present {
            let label = filter(id).label
            let name = expected[id]!
            XCTAssertTrue(label == name || label.hasPrefix(name + " "), "\(language) \(id): \(label)")
            XCTAssertFalse(label.contains("…") || label.contains("..."), "\(language) \(id): \(label)")
        }
        shot("names-\(language)")
        // A filter that is chosen is no wider than it was: its name was whole before (a cut name widens when it opens).
        let widths = self.widths
        filter("unread").tap()
        XCTAssertTrue(filter("unread").isSelected)
        assertWidths(widths, "\(language): choosing a filter leaves every filter as wide as it was")
        scroll(to: present.last!, toEnd: true)
        shot("names-\(language)-end")
    }

    @MainActor func testNamesAreWholeInEnglish() { verifyNames("en") }
    @MainActor func testNamesAreWholeInChinese() { verifyNames("zh-Hans") }
    @MainActor func testNamesAreWholeInJapanese() { verifyNames("ja") }
}
