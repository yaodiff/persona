import XCTest

/// #9278: an Agent's row and its detail More menu show the same ordered actions.
/// Demo data is synthetic and never touches a real hub or a real Agent.
final class AgentMenuParityUITests: XCTestCase {
    private var app: XCUIApplication!
    private let title = "给天气 App 加主屏幕小组件" // i18n-ignore: bundled synthetic fixture

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor private func launch(_ extra: [String] = []) {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-app.tab", "chats", "-tab", "dev",
                               "-dev.openChat", "", "-openAgentsFresh", "YES", "-dev.grouping", "space",
                               "-holdChromeOpen", "YES", "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"] + extra
        app.launchPinned()
        let anyRow = app.buttons.matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(anyRow.waitForExistence(timeout: 20), "no Agent row in the list")
        if !row.exists {
            app.revealAgentsSearch()
            let search = app.searchFields.firstMatch
            XCTAssertTrue(search.waitForExistence(timeout: 5), "Agents search did not open")
            search.tap()
            search.typeText("天气") // i18n-ignore: bundled synthetic fixture title
        }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the example Agent is not in the list")
        app.bringAboveTabBar(row)
        sleep(1)
    }

    @MainActor private var row: XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == 'agents.row' AND label CONTAINS %@", title)).firstMatch
    }

    @MainActor private func openRowMenu() {
        row.press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["置顶"].waitForExistence(timeout: 5), "the row's long-press menu did not open")
    }

    @MainActor private func openDetailMenu() {
        row.tap()
        let more = app.buttons["chat.more"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 10), "the detail's More button did not appear")
        more.tap()
        XCTAssertTrue(app.buttons["置顶"].waitForExistence(timeout: 5), "detail has no Pin action")
    }

    /// The open menu's top level, then its More's items (#13396), as `menuLabels` names them, with a shot of each.
    @MainActor private func readMenu(_ name: String) -> (top: [String], more: [String]) {
        let top = menuLabels(marker: "置顶")
        shot(name)
        app.openAgentMenuMore()
        XCTAssertTrue(app.buttons["拷贝目录"].waitForExistence(timeout: 5), "More did not open:\n\(app.debugDescription)")
        let more = menuLabels(marker: "拷贝目录")
        shot(name + "-more")
        return (top, more)
    }

    /// The entries of the menu list that shows `marker`: the top level's or, opened, More's.
    @MainActor private func menuLabels(marker: String) -> [String] {
        let wanted: [String: String] = [
            "更多": "more", "Git": "git", "文件": "files", "子智能体1": "subagents", "空间内成员": "space",
            "实时接入": "attach", "中断": "interrupt", "标为已看": "markSeen", "接管": "takeover",
            "新会话继续": "continueInNewSession", "重命名 Agent": "rename", "自动命名": "autoRename", "置顶": "pin", "取消置顶": "pin",
            "屏蔽通知": "mute", "开启通知": "mute", "关联到待办": "linkTodo", "更换关联待办": "moveTodo",
            "解除关联": "unlinkTodo", "停止循环": "stopLoop", "交给 Persona": "handToPersona",
            "拷贝目录": "copyDirectory", "终端": "terminal", "拷贝恢复命令": "copyResume",
            "在终端里恢复": "resume", "终端分屏": "splitPane", "关闭标签页": "closeTab",
            "关闭 Agent": "close", "删除对话": "delete",
        ]
        let menu = app.collectionViews.containing(NSPredicate(format: "label == %@", marker)).firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 5), "no system action menu:\n\(app.debugDescription)")
        // What the menu shows, without a swipe: the top level (#13396) does not scroll, and a swipe on a menu that does
        // not scroll chooses the item under the finger (Close Agent…, the last one).
        var seen: [String] = []
        for label in menu.buttons.allElementsBoundByIndex.compactMap({ wanted[$0.label] }) where !seen.contains(label) { seen.append(label) }
        print("Agent menu entries: \(seen.joined(separator: ","))")
        return seen
    }

    @MainActor private func shot(_ name: String) {
        let image = app.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }

    @MainActor func testSameAgentRowAndDetailHaveTheSameOrderedMenu() {
        launch()
        openRowMenu()
        let rowItems = readMenu("agent-row-menu-zh")
        app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.1)).tap()
        openDetailMenu()
        let detailItems = readMenu("agent-detail-menu-zh")
        XCTAssertEqual(detailItems.top, rowItems.top, "the detail More menu differs from the row's long-press menu")
        // More is longer than the screen: each shows what fits, the same items in the same order.
        let shown = min(detailItems.more.count, rowItems.more.count)
        XCTAssertGreaterThan(shown, 3, "More shows too little: \(rowItems.more), \(detailItems.more)")
        XCTAssertEqual(Array(detailItems.more.prefix(shown)), Array(rowItems.more.prefix(shown)), "the detail's More differs from the row's")
        XCTAssertTrue(rowItems.top.contains("pin"))
        // #13396: at most seven items on the top level, More counted; the rest are More's.
        XCTAssertLessThanOrEqual(rowItems.top.count, 7, "\(rowItems.top)")
        XCTAssertTrue(rowItems.top.contains("more") && !rowItems.top.contains("copyDirectory"), "\(rowItems.top)")
        XCTAssertTrue(rowItems.more.contains("copyDirectory") && !rowItems.more.contains("pin"), "\(rowItems.more)")
    }

    @MainActor func testMenusInDarkAndLargestText() {
        for (name, args) in [
            ("dark", ["-ui.scheme", "dark"]),
            ("ax5", ["-ui.scheme", "light", "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]),
        ] {
            launch(args)
            openRowMenu()
            _ = readMenu("agent-row-menu-zh-" + name)
            app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.1)).tap()
            openDetailMenu()
            _ = readMenu("agent-detail-menu-zh-" + name)
        }
    }

    @MainActor func testDetailPinAndRowFilesNavigationWork() {
        launch()
        openDetailMenu()
        app.buttons["置顶"].tap()
        app.buttons.matching(NSPredicate(format: "label == %@", "返回")).firstMatch.tap()
        XCTAssertTrue(row.waitForExistence(timeout: 5))
        row.press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["取消置顶"].waitForExistence(timeout: 5), "detail's Pin did not reach the same Agent's row")
        app.openAgentMenuMore()
        app.buttons["文件"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["files.pane"].waitForExistence(timeout: 10),
                      "the row's Files action must open the Agent's Files page, not only its chat")
    }
}
