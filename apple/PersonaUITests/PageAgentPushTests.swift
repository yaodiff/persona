import XCTest

/// An Agent opened from inside a page is a secondary page (#9971, #12006): it is pushed over that page, it has Back, Back
/// returns to the page, and the sidebar's root and its selected row stay where they were. A task's card already did this
/// (#9987, `SecondaryPageBackTests`); here the other ways in:
/// - a Brief card's Open, in the main conversation (pushed on the conversation's path);
/// - a to-do page's Agent row (the to-do pushed from the To-dos list);
/// - a knowledge project's "Agents on This Project" row (the entry pushed by view, so the chat is pushed by the page).
/// Before the fix each made the Agent a root: no Back beside the sidebar, the sidebar's selection on the Agent; on the
/// iPhone it went to the Agents tab instead.
///
/// Against scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_MANAGERS=0 (PERSONA_MOCK_HUB), the Sidebar
/// layout; the task page's test also needs MOCK_TASKS_SENT=1 (it skips without). `-sideBars YES` (PERSONA_SIDE_BARS=1)
/// lays the pages out as under the open Duo's side bars on any simulator;
/// PERSONA_ORIENTATION=landscape turns the device on its side. PERSONA_SHOTS=<dir> keeps the screens (PERSONA_SHOTS_DEVICE
/// names the device, PERSONA_SHOTS_APPEARANCE the appearance).
final class PageAgentPushTests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private static let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    /// The tests to-do's chat, named by the Brief card's waiting question.
    private static let testsAgent = "macbook/a1f3c2d4-0000-4000-8000-000000000002"
    /// The widget to-do's Agent and one of the weather project's.
    private static let widgetAgent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private static let widgetTitle = "给天气 App 加主屏幕小组件"
    /// The tests Agent's row title in the sidebar.
    private static let testsTitle = "补齐网络层的单元测试"
    /// The widget task's title (its page hosts the widget Agent's chat).
    private static let taskTitle = "修小组件横屏崩溃"
    /// The orders to-do in the list and its one Agent, sharing a title.
    private static let ordersAgent = "server/a1f3c2d4-0000-4000-8000-000000000004"
    private static let ordersTitle = "排查 /orders 接口变慢"

    private struct Page: Decodable, CustomStringConvertible {
        var root: String
        var path: Int
        var depth: Int
        var description: String { "root \(root), path \(path), depth \(depth)" }
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_MANAGERS=0 (PERSONA_MOCK_HUB)")
        }
        hub = h
        XCUIDevice.shared.orientation = ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" ? .landscapeLeft : .portrait
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    // MARK: - Tests

    /// A Brief card's Open pushes the chat it names over the main conversation.
    @MainActor func testABriefCardPushesItsAgentWithBack() throws {
        launch()
        let open = app.buttons["打开：Mock 用协议注入还是保留单例？"]
        reveal(open)
        try drillDown(from: "brief", tap: open, agent: Self.testsAgent) {
            app.descendants(matching: .any)["brief.card"].firstMatch
        }
    }

    /// The Agent pushed over the main conversation belongs to the conversation's stack: another Agent picked in the
    /// sidebar is shown at once, not hidden under the pushed chat; the main conversation picked again comes back with the
    /// pushed chat in front, and Back returns to the conversation itself.
    @MainActor func testAnAgentPushedOverTheConversationStaysWithIt() throws {
        launch()
        try skipWithoutColumnBeside()
        let open = app.buttons["打开：Mock 用协议注入还是保留单例？"]
        reveal(open)
        open.tap()
        assertChat(Self.testsAgent)
        let pushed = settled { $0.root == "conversation" && $0.path == 1 }
        XCTAssertEqual(pushed?.path, 1, "the Agent's chat was not pushed over the conversation: \(pushed.map(\.description) ?? "-")")
        shot("stack-0-pushed")

        // Another Agent from the sidebar: the page is that Agent's chat, its root, nothing pushed on it.
        guard let row = sidebarAgentRow(Self.widgetTitle) else { return }
        row.tap()
        assertChat(Self.widgetAgent)
        let picked = settled { $0.root == "agent" && $0.path == 0 }
        XCTAssertEqual(picked?.root, "agent", "the picked Agent is not the page's root: \(picked.map(\.description) ?? "-")")
        XCTAssertEqual(picked?.path, 0, "the conversation's pushed chat is still on the page: \(picked.map(\.description) ?? "-")")
        app.assertOneSelectedSidebarRow("agents.row", containing: Self.widgetTitle, "the Agent picked in the sidebar")
        shot("stack-1-picked")

        // The main conversation again: back as it was left, the pushed chat in front with its Back.
        app.descendants(matching: .any)["sidebar-main"].firstMatch.tap()
        assertChat(Self.testsAgent)
        let back = settled { $0.root == "conversation" && $0.path == 1 }
        XCTAssertEqual(back?.root, "conversation", "the main conversation is not the root: \(back.map(\.description) ?? "-")")
        XCTAssertEqual(back?.path, 1, "the conversation lost its pushed chat: \(back.map(\.description) ?? "-")")
        app.assertOneSelectedSidebarRow("sidebar-main", "the main conversation picked again")
        shot("stack-2-conversation")
        oneBack("the conversation's pushed chat").tap()
        let card = app.descendants(matching: .any)["brief.card"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 10), "Back did not return to the conversation:\n\(app.debugDescription)")
        let root = settled { $0.root == "conversation" && $0.path == 0 }
        XCTAssertEqual(root?.path, 0, "Back left something on the conversation: \(root.map(\.description) ?? "-")")
        shot("stack-3-back")
    }

    /// A task's page opened from the main conversation (the header of a message Persona sent to it, MOCK_TASKS_SENT=1) is the
    /// conversation's too (#13156): before the fix it was pushed by view, outside the path, and stayed in front of the
    /// Agent picked in the sidebar.
    @MainActor func testATaskPageOpenedFromTheConversationStaysWithIt() throws {
        launch()
        try skipWithoutColumnBeside()
        // The rework Persona sent to the widget task: its header ("Persona → @task") opens the task's page.
        let sent = app.descendants(matching: .any).matching(identifier: "exchange.routed")
        guard sent.firstMatch.waitForExistence(timeout: 30) else {
            throw XCTSkip("no message sent to a task: start the mock with MOCK_TASKS_SENT=1")
        }
        let header = sent.buttons.matching(NSPredicate(format: "label CONTAINS %@", Self.taskTitle)).firstMatch
        reveal(header)
        header.tap()
        XCTAssertTrue(waitFor { taskOnPage().map { $0.isHittable } ?? false }, "the task's page did not open:\n\(app.debugDescription)")
        let pushed = settled { $0.root == "conversation" && $0.path == 1 }
        XCTAssertEqual(pushed?.path, 1, "the task's page is not on the conversation's path: \(pushed.map(\.description) ?? "-")")
        shot("task-0-pushed")

        guard let row = sidebarAgentRow(Self.testsTitle) else { return }
        row.tap()
        assertChat(Self.testsAgent)
        let picked = settled { $0.root == "agent" && $0.path == 0 }
        XCTAssertEqual(picked?.root, "agent", "the picked Agent is not the page's root: \(picked.map(\.description) ?? "-")")
        XCTAssertEqual(picked?.path, 0, "something is still pushed on the picked Agent: \(picked.map(\.description) ?? "-")")
        XCTAssertNil(taskOnPage(), "the task's page stayed in front of the picked Agent:\n\(app.debugDescription)")
        shot("task-1-picked")

        app.descendants(matching: .any)["sidebar-main"].firstMatch.tap()
        let back = settled { $0.root == "conversation" && $0.path == 1 }
        XCTAssertEqual(back?.path, 1, "the conversation lost the task's page: \(back.map(\.description) ?? "-")")
        XCTAssertTrue(waitFor { taskOnPage().map { $0.isHittable } ?? false }, "the task's page did not come back:\n\(app.debugDescription)")
        shot("task-2-conversation")
        oneBack("the task's page").tap()
        let root = settled { $0.root == "conversation" && $0.path == 0 }
        XCTAssertEqual(root?.path, 0, "Back did not return to the conversation: \(root.map(\.description) ?? "-")")
        XCTAssertTrue(waitFor { taskOnPage() == nil }, "the task's page is still up after Back:\n\(app.debugDescription)")
        shot("task-3-back")
    }

    /// A to-do's Agent row pushes the Agent's chat over the to-do's page.
    @MainActor func testAToDoPushesItsAgentWithBack() throws {
        launch(["-openShellPage", "todos"])
        XCTAssertTrue(app.staticTexts["补齐网络层的单元测试"].firstMatch.waitForExistence(timeout: 20), "no to-dos:\n\(app.debugDescription)")
        hideOverlaidSidebar()
        // Under In Progress, below the ones that need the owner: scrolled to.
        scrolledTo(app.staticTexts[Self.ordersTitle].firstMatch, "the orders to-do").tap()
        let handTo = app.descendants(matching: .any)["todo.handToAgent"].firstMatch
        XCTAssertTrue(handTo.waitForExistence(timeout: 10), "the to-do's page did not open:\n\(app.debugDescription)")
        // The Agent's row: the one button carrying its title (the to-do's own title is a field).
        let row = scrolledTo(app.buttons.matching(NSPredicate(format: "label CONTAINS %@", Self.ordersTitle)).firstMatch, "the orders Agent's row")
        try drillDown(from: "todo", tap: row, agent: Self.ordersAgent) { handTo }
    }

    /// A knowledge project's Agent row pushes the Agent's chat over the project's page (pushed by view).
    @MainActor func testAKnowledgeProjectPushesItsAgentWithBack() throws {
        launch(["-openShellPage", "knowledge", "-showKnowledgeEntry", "projects/weather-app"])
        let row = agentRow()
        try drillDown(from: "knowledge", tap: row, agent: Self.widgetAgent) { agentRow() }
    }

    // MARK: - Steps

    /// Taps `tap` on the page in front and checks the drill-down: the Agent's chat on top, the sidebar's root and selected
    /// row unchanged, one Back, and Back returns to the page (`page` is there again), the sidebar still unchanged.
    @MainActor private func drillDown(from name: String, tap: XCUIElement, agent: String, page: () -> XCUIElement) throws {
        let before = probe()
        let selected = sidebarSelection()
        shot("\(name)-0-page")
        tap.tap()
        assertChat(agent)
        if let before {
            let pushed = settled { $0.path > before.path || $0.depth > before.depth }
            XCTAssertEqual(pushed?.root, before.root, "\(name): the Agent was made a root: \(before) → \(pushed.map(\.description) ?? "-")")
            XCTAssertTrue((pushed?.path ?? 0) > before.path || (pushed?.depth ?? 0) > before.depth,
                          "\(name): the Agent's chat was not pushed: \(before) → \(pushed.map(\.description) ?? "-")")
        }
        if let selected {
            XCTAssertEqual(sidebarSelection(), selected, "\(name): the sidebar's selection moved to the Agent")
        }
        XCTAssertFalse(app.descendants(matching: .any)["chat-sidebar-button"].exists, "\(name): the sidebar's button on a pushed chat")
        let back = oneBack("\(name): the Agent's chat")
        shot("\(name)-1-pushed")
        back.tap()
        let target = page()
        let deadline = Date.now.addingTimeInterval(10)
        while !(target.exists && target.isHittable) && Date.now < deadline { usleep(200_000) }
        XCTAssertTrue(target.exists, "\(name): Back did not return to the page:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons.matching(identifier: "chat.title").firstMatch.exists, "\(name): the Agent's chat is still up")
        if let before {
            let popped = settled { $0.path == before.path && $0.depth == before.depth }
            XCTAssertEqual(popped?.root, before.root, "\(name): Back left the sidebar on another root: \(popped.map(\.description) ?? "-")")
        }
        if let selected {
            XCTAssertEqual(sidebarSelection(), selected, "\(name): the sidebar's selection moved after Back")
        }
        shot("\(name)-2-back")
    }

    // MARK: - Helpers

    @MainActor private func launch(_ extra: [String] = []) {
        app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let sideBars = ProcessInfo.processInfo.environment["PERSONA_SIDE_BARS"] == "1"
        app.launchArguments = ["-pairURL", url, "-holdChromeOpen", "YES", "-ui.scheme", "system", "-tab", "assistant",
                               "-dev.openChat", "", "-openAssistant", Self.main, "-app.navigation", "sidebar", "-probes", "YES",
                               "-sideBars", sideBars ? "YES" : "NO"] + extra
        app.launchEnvironment["PERSONA_UI_TEST_SESSION"] = "1"
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 3) { allow.tap() }
    }

    /// A sidebar laid over the page (the open Duo upright) covers it: slid away with its own button.
    @MainActor private func hideOverlaidSidebar() {
        let fold = app.descendants(matching: .any)["sidebar-fold-column"].firstMatch
        guard fold.exists, fold.isHittable, app.windows.firstMatch.frame.width < 1000 else { return }
        let main = app.descendants(matching: .any)["sidebar-main"].firstMatch
        if main.exists, main.isHittable, main.frame.maxX > app.windows.firstMatch.frame.width * 0.45 { fold.tap() }
    }

    /// These need the sidebar beside the page (an iPad, the open Duo on its side): on a phone, or the open Duo upright
    /// where the column is laid over the page, it is a drawer, and a task's page rises as a sheet.
    @MainActor private func skipWithoutColumnBeside() throws {
        let main = app.descendants(matching: .any)["sidebar-main"].firstMatch
        let beside = main.waitForExistence(timeout: 20) && main.isHittable && main.frame.maxX < app.windows.firstMatch.frame.width * 0.45
        if !beside { throw XCTSkip("the sidebar is not beside the page on this screen") }
    }

    /// The Agent's row titled `title` in the sidebar's Agents list, the column scrolled to it; nil (skipped) where the
    /// column is not beside the page (a phone's drawer, the open Duo upright's overlay).
    @MainActor private func sidebarAgentRow(_ title: String) -> XCUIElement? {
        let main = app.descendants(matching: .any)["sidebar-main"].firstMatch
        guard main.exists, main.isHittable else {
            XCTFail("the sidebar is not beside the page:\n\(app.debugDescription)")
            return nil
        }
        let row = app.buttons.matching(identifier: "agents.row").matching(NSPredicate(format: "label CONTAINS %@", title)).firstMatch
        let column = app.windows.firstMatch.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: main.frame.midX, dy: app.windows.firstMatch.frame.height * 0.6))
        for _ in 0..<12 where !(row.exists && row.isHittable) {
            column.press(forDuration: 0.05, thenDragTo: column.withOffset(CGVector(dx: 0, dy: -200)))
            usleep(500_000)
        }
        XCTAssertTrue(row.exists && row.isHittable, "no \(title) row in the sidebar:\n\(app.debugDescription)")
        return row
    }

    /// The task's page in front: its header (the hosted chat's `chat.title`, named for the task) on the page beside the
    /// sidebar, hittable; nil while there is none. Not the task's name elsewhere (a message's header in the conversation).
    @MainActor private func taskOnPage() -> XCUIElement? {
        let main = app.descendants(matching: .any)["sidebar-main"].firstMatch
        let pageStart = main.exists ? main.frame.maxX : 0
        return app.buttons.matching(identifier: "chat.title").matching(NSPredicate(format: "label BEGINSWITH %@", Self.taskTitle))
            .allElementsBoundByIndex.first { $0.exists && $0.frame.minX >= pageStart && $0.isHittable }
    }

    @MainActor private func waitFor(_ done: () -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(15)
        while !done() && Date.now < deadline { usleep(300_000) }
        return done()
    }

    /// The weather widget Agent's row on the page in front, scrolled into view.
    @MainActor private func agentRow() -> XCUIElement {
        let row = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", Self.widgetTitle)).firstMatch
        return row.exists ? scrolledTo(row, "the widget Agent's row") : scrolledTo(app.staticTexts[Self.widgetTitle].firstMatch, "the widget Agent's row")
    }

    /// `element`, the page in front dragged up until it is on screen and hittable.
    @MainActor private func scrolledTo(_ element: XCUIElement, _ what: String) -> XCUIElement {
        let deadline = Date.now.addingTimeInterval(30)
        while Date.now < deadline {
            if element.exists, element.isHittable { return element }
            let window = app.windows.firstMatch
            window.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: 0.7))
                .press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: 0.4)))
            usleep(400_000)
        }
        XCTFail("no \(what):\n\(app.debugDescription)")
        return element
    }

    /// Drags the conversation until `element` sits in the middle band of the window, clear of the bars.
    @MainActor private func reveal(_ element: XCUIElement) {
        XCTAssertTrue(element.waitForExistence(timeout: 30), "not found: \(element)\n\(app.debugDescription)")
        let window = app.windows.firstMatch.frame
        let band = (window.minY + window.height * 0.2)...(window.minY + window.height * 0.75)
        let middle = app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: 0.5))
        for _ in 0..<14 where !(band.contains(element.frame.midY) && element.isHittable) {
            let to = app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: element.frame.midY < band.lowerBound ? 0.75 : 0.25))
            middle.press(forDuration: 0.1, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.2)
        }
        XCTAssertTrue(band.contains(element.frame.midY) && element.isHittable, "can't bring \(element) into view: \(element.frame)")
    }

    @MainActor private func probe() -> Page? { app.probe("sidebar-page", as: Page.self) }

    @MainActor private func settled(_ done: (Page) -> Bool) -> Page? {
        let deadline = Date.now.addingTimeInterval(10)
        var last = probe()
        while Date.now < deadline {
            if let p = last, done(p) { return p }
            Thread.sleep(forTimeInterval: 0.3)
            last = probe()
        }
        return last
    }

    /// The sidebar's selected rows, where the column is on screen beside the page; nil where it is away.
    @MainActor private func sidebarSelection() -> [String]? {
        let main = app.descendants(matching: .any)["sidebar-main"].firstMatch
        guard main.exists, main.isHittable else { return nil }
        // Ids, not labels: a row's label carries a relative time ("2 minutes ago") that moves on its own.
        return app.selectedSidebarRows().map(\.id)
    }

    @MainActor private func assertChat(_ session: String) {
        let header = app.buttons.matching(identifier: "chat.title").firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10), "the Agent's chat did not open:\n\(app.debugDescription)")
        let deadline = Date.now.addingTimeInterval(10)
        while (header.value as? String) != session && Date.now < deadline { usleep(200_000) }
        XCTAssertEqual(header.value as? String, session, "opened another Agent")
    }

    /// The page's one way back, hittable, read once the page has settled.
    @MainActor private func oneBack(_ step: String) -> XCUIElement {
        let backs = app.buttons.matching(NSPredicate(format: "identifier == 'BackButton' OR label IN %@", ["返回", "Back"]))
        XCTAssertTrue(backs.firstMatch.waitForExistence(timeout: 10), "\(step): no Back:\n\(app.debugDescription)")
        sleep(1)
        let shown = backs.allElementsBoundByIndex.filter { $0.exists && $0.isHittable }
        XCTAssertEqual(shown.count, 1, "\(step): \(shown.count) Backs:\n\(app.debugDescription)")
        return shown.first ?? backs.firstMatch
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = env["PERSONA_SHOTS_DEVICE"] ?? (UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone")
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        sleep(1)
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("agentpush-\(device)-\(appearance)-\(name).png"))
    }
}
