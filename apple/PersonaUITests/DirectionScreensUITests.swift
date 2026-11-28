import XCTest

/// Every state of Move To… and a direction's page (#11516), each checked as it is drawn and kept as a screenshot for
/// the PR: the row with its filed sessions and All ›, the menus, the choices, the toast with Undo, a refusal, the page
/// with the standings, the read still out, the hub out of reach, the read failing, `sessions_unavailable`, an ended and
/// a removed direction, and the Unpin confirmation. Against scripts/mock/mock_hub.py with MOCK_LEADERS=1 and
/// MOCK_DIRECTION_SESSIONS=1 (PERSONA_LEADERS_HUB). Runs only when asked (PERSONA_DIRECTION_SHOTS=1), in the language
/// PERSONA_SHOT_LANG names (en by default); SHOT_DIR keeps the screenshots, SHOT_SUFFIX names the run (appearance,
/// text size, device). The simulator's appearance and text size are the run's (`xcrun simctl ui`). The off walk's
/// "manager" case (PERSONA_OFF_CASE=manager) runs on a second mock started with MOCK_SESSIONS_LIST=1 as well
/// (PERSONA_OFF_MANAGER_HUB), the setup its main-vs-branch pairs were taken with; the paged list brings nothing to the
/// other manager's page, whose own conversation does not reach it, so that case compares the page's Main Chat row.
/// PERSONA_SHOT_NAV pins the layout (`sidebar`, `separate`; `auto` leaves the app's default).
final class DirectionScreensUITests: XCTestCase {
    private var hub: String!
    private var app: XCUIApplication!
    private var lang: String { ProcessInfo.processInfo.environment["PERSONA_SHOT_LANG"] ?? "en" }

    /// The default assistant again, whatever a case left picked (the "manager" off case picks manager m-3fa21c).
    override func tearDown() {
        let hub: String? = self.hub
        MainActor.assumeIsolated { AssistantUITestReset.toDefaultMain(hub: hub) }
        super.tearDown()
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
        let env = ProcessInfo.processInfo.environment
        guard env["PERSONA_DIRECTION_SHOTS"] == "1", let hub = env["PERSONA_LEADERS_HUB"] else {
            throw XCTSkip("screenshots only: PERSONA_DIRECTION_SHOTS=1 with the mock (PERSONA_LEADERS_HUB)")
        }
        self.hub = hub
    }

    // MARK: Helpers

    private final class Answer: @unchecked Sendable { var data: Data? }

    @discardableResult private func get(_ path: String) -> Data? {
        var req = URLRequest(url: URL(string: hub + path)!)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        // A semaphore, not an expectation: teardown blocks call this too.
        let done = DispatchSemaphore(value: 0)
        let box = Answer()
        URLSession.shared.dataTask(with: req) { data, _, _ in box.data = data; done.signal() }.resume()
        _ = done.wait(timeout: .now() + 15)
        return box.data
    }

    /// A POST to the mock (a fault, a direction made), answered or not.
    private func post(_ path: String, body: String) {
        var req = URLRequest(url: URL(string: hub + path)!)
        req.httpMethod = "POST"
        req.httpBody = Data(body.utf8)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let done = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: req) { _, _, _ in done.signal() }.resume()
        _ = done.wait(timeout: .now() + 15)
    }

    /// Waits (15 s at most) until `done`.
    private func until(_ done: () throws -> Bool) rethrows -> Bool {
        let end = Date().addingTimeInterval(15)
        while Date() < end {
            if try done() { return true }
            usleep(300_000)
        }
        return try done()
    }

    /// A row of the page by the start of its title.
    @MainActor private func pageRow(_ title: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == 'direction-page-filed' AND label BEGINSWITH %@", title)).firstMatch
    }

    private func reset(_ query: String = "") {
        get("/mock/down?s=0") // an earlier test's outage never outlives it
        get("/mock/reset")
        get("/mock/direction_sessions?" + query)
    }

    /// Move To…: by its identifier where the menu keeps it (a context menu), by its words where not (a Menu).
    @MainActor private var moveTo: XCUIElement {
        let byID = app.buttons["move-to"].firstMatch
        return byID.exists ? byID : app.buttons[word("Move To…", "移到…", "移動…")].firstMatch
    }

    /// The Agent menu has the compact command title in all three languages.
    @MainActor private var agentMoveTo: XCUIElement {
        app.buttons[word("Move To", "移到", "移動")].firstMatch
    }

    /// A word of the app in the run's language.
    private func word(_ en: String, _ zh: String, _ ja: String) -> String { lang == "en" ? en : lang == "ja" ? ja : zh }

    /// Launches with lead `id`'s row held open too.
    @MainActor private func launchHolding(_ id: String) {
        launch(extra: ["-assistant.direction.open.\(id)", "YES"])
    }

    /// Launches on the Assistant page, or (`agentChat`, "machine/session") straight into that Agent's chat. The
    /// assistant picked on this device is the hub's default unless `keepPick` (it outlives a launch, `assistant.manager`).
    @MainActor private func launch(agentChat: String? = nil, keepPick: Bool = false, extra: [String] = []) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let place: [String] = agentChat.map { ["-app.tab", "chats", "-tab", "dev", "-dev.openChat", $0] }
            ?? ["-tab", "assistant", "-newAssistant", "YES", "-dev.openChat", ""]
        // Built in steps: as one expression, Xcode 26.3's type-checker gives up on it (#13098).
        let locale: String = lang == "en" ? "en_US" : (lang == "ja" ? "ja_JP" : "zh_CN")
        var args: [String] = ["-pairURL", pair]
        args += place
        if !keepPick { args += ["-assistant.manager", "{}"] }
        // the first lead held open for this launch only (its fold is the device's)
        args += ["-assistant.direction.open.lead0000-0000-4000-8000-000000000000", "YES"]
        args += ["-AppleLanguages", "(\(lang))", "-AppleLocale", locale]
        // PERSONA_SHOT_NAV: the layout to pin, `sidebar` or `separate` (`launchPinned`'s own when unset); any
        // other value ("auto") picks nothing, so the app's default applies: Merged on an iPad.
        if let nav = ProcessInfo.processInfo.environment["PERSONA_SHOT_NAV"] { args += ["-app.navigation", nav] }
        app.launchArguments = args + extra
        app.launchPinned()
    }

    @MainActor private func shot(_ name: String) {
        let png = app.screenshot()
        let a = XCTAttachment(screenshot: png)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["SHOT_DIR"] else { return }
        let suffix = ProcessInfo.processInfo.environment["SHOT_SUFFIX"].map { "-" + $0 } ?? ""
        try? png.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/\(name)-\(lang)\(suffix).png"))
    }

    @MainActor private func openSidebar() {
        // The drawer's list is on screen once it is out, however far it was scrolled the last time.
        let head = sidebarList
        let end = Date().addingTimeInterval(30)
        while Date() < end && !(head.exists && head.isHittable) {
            if let menu = app.buttons.matching(identifier: "assistant-sidebar-button").allElementsBoundByIndex.first(where: \.isHittable) {
                menu.tapCenter()
                if head.waitForExistence(timeout: 5) { break }
            } else {
                usleep(500_000)
            }
        }
        XCTAssertTrue(head.exists, "the sidebar did not open:\n\(app.debugDescription)")
    }

    /// Scrolls the sidebar (a lazy list: rows below the fold are not made) until `element` is on screen.
    @MainActor @discardableResult private func reveal(_ element: XCUIElement, rounds: Int = 8) -> Bool {
        // Down the list first, then back up: the drawer keeps where it was scrolled.
        for round in 0..<rounds * 2 {
            if element.exists && element.isHittable { return true }
            let list = sidebarList
            guard list.exists else { return false } // no list to scroll (a swipe at nothing fails the walk)
            if round < rounds { list.swipeUp(velocity: .slow) } else { list.swipeDown(velocity: .slow) }
            usleep(400_000)
        }
        return element.exists && element.isHittable
    }

    /// The sidebar's list: on a phone it inherits the drawer's identifier; the iPad's column is the list of its rows.
    @MainActor private var sidebarList: XCUIElement {
        let byID = app.collectionViews.matching(identifier: "assistant-sidebar").firstMatch
        if byID.exists { return byID }
        // In the Sidebar layout the drawer's host carries it and is no list (`SidebarLayoutPhoneTests.reach`).
        let host = app.descendants(matching: .any).matching(identifier: "assistant-sidebar").firstMatch
        if host.exists { return host }
        // Never the direction page's list, which draws the sidebar's own to-do and Agent rows (`sidebar-direction-…`).
        return app.collectionViews.containing(NSPredicate(format: "identifier BEGINSWITH 'sidebar-'"))
            .matching(NSPredicate(format: "identifier != 'direction-page'")).firstMatch
    }

    /// The first lead (「Persona 产品」), whose name is mock data in every language.
    @MainActor private func firstLeadToggle() -> XCUIElement {
        app.buttons.matching(identifier: "sidebar-direction-toggle")
            .matching(NSPredicate(format: "label BEGINSWITH %@", "Persona 产品")).firstMatch
    }

    @MainActor private func openFirstLead() {
        let toggle = firstLeadToggle()
        if !toggle.waitForExistence(timeout: 20) || !toggle.isHittable { reveal(toggle) }
        XCTAssertTrue(toggle.exists, "no fold for Persona 产品:\n\(app.debugDescription)")
        if toggle.value as? String != expanded { toggle.tap() }
        XCTAssertTrue(reveal(app.buttons["sidebar-direction-all"].firstMatch),
                      "no All › under the open row:\n\(app.debugDescription)")
    }

    @MainActor private func openPageFromSidebar() {
        openSidebar()
        openFirstLead()
        app.buttons["sidebar-direction-all"].firstMatch.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 10), "All › did not open the page")
    }

    /// The page, in whichever state it is drawn (each carries its own identifier).
    @MainActor private var page: XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "identifier IN %@", ["direction-page", "direction-page-gone",
                                                                                          "direction-page-unread", "direction-page-reading"])).firstMatch
    }

    /// The conversation's title menu: Assistant tapped again on a phone (docs/design/DESIGN_SYSTEM.md 3.5); beside a sidebar
    /// column (an iPad, either layout) the title pill at the top of the conversation's column.
    @MainActor private func openTitleMenu() {
        // The phone's switch, else the title pill at the top of the detail column beside a sidebar on screen. A menu's
        // label is a button in a button, which XCUITest may not call hittable (the switch at the largest size, the
        // main chat's pill on an iPad): it is tapped where it is drawn.
        let shell = app.buttons["shell-assistant"].firstMatch
        var target: XCUIElement? = shell.waitForExistence(timeout: 5) ? shell : nil
        if target == nil {
            // The column beside the page: the Sidebar layout's (`unified-sidebar`), else the Persona list's.
            let column = app.descendants(matching: .any).matching(identifier: "unified-sidebar").firstMatch
            let list = sidebarList
            let left = column.exists && column.frame.minX >= 0 && column.frame.width < app.frame.width ? column.frame.maxX
                : list.exists && list.isHittable ? list.frame.maxX : 0
            let center = (left + app.frame.width) / 2
            target = app.buttons.allElementsBoundByIndex
                .filter { $0.exists && $0.frame.minY < 220 && $0.frame.width > 60 && abs($0.frame.midX - center) < 60 }
                .min { $0.frame.minY < $1.frame.minY }
        }
        guard let target else { return XCTFail("no title menu:\n\(app.debugDescription)") }
        sleep(1) // a drawer that was just closed has gone
        target.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
    }

    /// A row of Added Here whose spoken label (title, who placed it, where it stands) has `words` in it.
    @MainActor private func filedRow(saying words: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == 'direction-page-filed' AND label CONTAINS %@", words)).firstMatch
    }

    /// The New Chat bar at the sidebar's foot: its own identifier, or on a phone the drawer container's, which SwiftUI
    /// can put on its footer buttons, with its label in the run's language (as `drawerNewChatButton`).
    @MainActor private var newChatBar: XCUIElement {
        let identified = app.buttons["sidebar-new"].firstMatch
        if identified.exists { return identified }
        return app.buttons.matching(NSPredicate(format: "identifier == %@ AND label == %@", "assistant-sidebar",
                                                word("New Chat", "新对话", "新しいチャット"))).firstMatch
    }

    /// Drags `list` up (`by` > 0) or down by about that many points: a step small enough to stop near a row.
    @MainActor private func drag(_ list: XCUIElement, by points: CGFloat) {
        let start = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.6))
        start.press(forDuration: 0.05, thenDragTo: start.withOffset(CGVector(dx: 0, dy: -points)))
        usleep(500_000)
    }

    /// Scrolls the sidebar until `row` is clear of the New Chat bar at its foot, not merely hittable under it, and
    /// asserts it is: a helper that did nothing cannot pass.
    @MainActor private func revealAboveTheBar(_ row: XCUIElement) {
        let bar = newChatBar
        XCTAssertTrue(bar.waitForExistence(timeout: 5), "no New Chat bar:\n\(app.debugDescription)")
        for _ in 0..<10 where row.exists && bar.exists && row.frame.maxY > bar.frame.minY - 8 {
            let list = sidebarList
            guard list.exists else { break }
            drag(list, by: 120)
        }
        XCTAssertTrue(row.exists && row.frame.maxY <= bar.frame.minY - 8,
                      "the row is not above the New Chat bar (\(row.frame) / \(bar.frame)):\n\(app.debugDescription)")
    }

    /// A conversation row by the start of its title, scrolled into view.
    @MainActor private func chat(_ prefix: String) -> XCUIElement {
        let row = app.buttons.matching(identifier: "sidebar-conversation").matching(NSPredicate(format: "label BEGINSWITH %@", prefix)).firstMatch
        if !(row.exists && row.isHittable) { reveal(row) }
        return row
    }

    // MARK: The row, the page and the standings

    @MainActor func testRowPageAndStandings() {
        reset()
        launch()
        openSidebar()
        openFirstLead()
        XCTAssertGreaterThanOrEqual(app.buttons.matching(identifier: "sidebar-direction-filed").count, 2)
        sleep(1)
        shot("row")
        app.buttons["sidebar-direction-all"].firstMatch.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons.matching(identifier: "direction-page-filed").firstMatch.waitForExistence(timeout: 10))
        sleep(1)
        shot("page")
        let more = app.buttons["direction-page-more"].firstMatch
        for _ in 0..<8 where !(more.exists && more.isHittable) { app.collectionViews.firstMatch.swipeUp(velocity: .slow) }
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no Show All N with nine added")
        shot("page-more")
        more.tap()
        sleep(1)
        app.collectionViews.firstMatch.swipeUp(velocity: .slow)
        sleep(1)
        XCTAssertEqual(app.buttons.matching(identifier: "direction-page-filed").count >= 5, true)
        // All nine shown: the button says Show First 5, and a row says where it stands in words.
        XCTAssertEqual(more.label, word("Show First 5", "只显示前 5 个", "最初の5件だけ表示"), "Show All 9 did not open all nine")
        XCTAssertTrue(filedRow(saying: word("Not known yet", "还不知道", "まだ不明")).exists, "no standing in words:\n\(app.debugDescription)")
        shot("standings")
        app.collectionViews.firstMatch.swipeUp(velocity: .slow)
        sleep(1)
        // The end of the list: the last standing, a removed computer.
        XCTAssertTrue(filedRow(saying: word("Computer removed", "电脑已移除", "コンピューターは削除済み")).exists,
                      "no Computer removed at the end:\n\(app.debugDescription)")
        shot("standings-end")
        // Collapse All in Added Here's header: its rows go, the header and its count stay.
        let fold = app.buttons["direction-page-fold"].firstMatch
        // Clear of the navigation bar, not merely hittable under it.
        let bar = app.navigationBars.firstMatch
        let pageList = app.descendants(matching: .any).matching(identifier: "direction-page").firstMatch
        for _ in 0..<12 where !(fold.exists && fold.isHittable && (!bar.exists || fold.frame.minY >= bar.frame.maxY + 4)) {
            drag(pageList, by: -120)
        }
        XCTAssertTrue(fold.isHittable && (!bar.exists || fold.frame.minY >= bar.frame.maxY + 4),
                      "Collapse All is not clear of the navigation bar:\n\(app.debugDescription)")
        fold.tap()
        XCTAssertTrue(app.buttons.matching(identifier: "direction-page-filed").firstMatch.waitForNonExistence(timeout: 5),
                      "Collapse All left rows:\n\(app.debugDescription)")
        sleep(1)
        shot("folded")
    }

    @MainActor func testTitleMenuEntry() {
        reset()
        launch()
        openSidebar()
        chat("Persona 产品").tap()
        sleep(2)
        openTitleMenu()
        // Items of a Menu carry no identifiers: by its words (not the sidebar's All ›, which says the same).
        let item = app.buttons.matching(NSPredicate(format: "label == %@ AND identifier != 'sidebar-direction-all'",
                                                    word("Tasks and Chats", "任务和会话", "タスクとチャット"))).firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 5), "no Tasks and Chats in the lead's title menu:\n\(app.debugDescription)")
        shot("title-menu")
        item.tap()
        if !page.waitForExistence(timeout: 10) { shot("page-from-title-missing") }
        XCTAssertTrue(page.exists, "Tasks and Chats did not open the page:\n\(app.debugDescription)")
        sleep(2)
        shot("page-from-title")
    }

    @MainActor func testDefaultPermissionsEntry() {
        reset()
        launch()
        openSidebar()
        chat("Persona 产品").tap()
        sleep(2)
        openTitleMenu()
        let item = app.buttons.matching(NSPredicate(format: "label == %@", word("Default Permissions", "默认权限", "デフォルト権限"))).firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 5), "no Default Permissions in the direction's title menu:\n\(app.debugDescription)")
        item.tap()
        XCTAssertTrue(app.descendants(matching: .any)["default-permission-mode"].firstMatch.waitForExistence(timeout: 10),
                      "the direction entry did not open the shared default-permissions control:\n\(app.debugDescription)")
        shot("default-permissions")
    }

    // MARK: Menus, choices, toast, refusal

    @MainActor func testMenuChoicesAndToast() {
        reset()
        launch()
        openSidebar()
        let row = chat("帮我约周五")
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.press(forDuration: 1.2)
        let move = app.buttons["move-to"].firstMatch
        XCTAssertTrue(move.waitForExistence(timeout: 5), "no Move To…:\n\(app.debugDescription)")
        shot("menu")
        move.tap()
        let growth = app.buttons["增长"].firstMatch
        XCTAssertTrue(growth.waitForExistence(timeout: 5))
        sleep(1)
        shot("choices")
        growth.tap()
        XCTAssertTrue(app.buttons["toast-action"].firstMatch.waitForExistence(timeout: 10), "no Undo")
        shot("toast")
    }

    @MainActor func testRefusalSaysTheHubsSentence() {
        reset("refuse=direction_full")
        launch()
        openSidebar()
        let row = chat("帮我约周五")
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["move-to"].firstMatch.waitForExistence(timeout: 5))
        app.buttons["move-to"].firstMatch.tap()
        XCTAssertTrue(app.buttons["增长"].firstMatch.waitForExistence(timeout: 5))
        app.buttons["增长"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["toast"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["toast-action"].exists, "a refusal offers no Undo")
        shot("refused")
    }

    /// The Agent menus are one inventory (`AgentActionMenu`, the row's long press and the chat's ⋯): the chat's, on an
    /// Agent filed here, opened from the page.
    @MainActor func testAgentMenu() {
        reset()
        launch()
        openPageFromSidebar()
        let agent = app.buttons.matching(identifier: "direction-page-filed")
            .matching(NSPredicate(format: "label BEGINSWITH %@", "整理首页改版的设计稿")).firstMatch
        XCTAssertTrue(agent.waitForExistence(timeout: 10))
        agent.tap()
        let more = app.buttons["chat.more"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 15), "no ⋯ on the Agent's chat:\n\(app.debugDescription)")
        more.tap()
        sleep(1)
        app.openAgentMenuMore() // Move To… is More's (#13396)
        XCTAssertTrue(agentMoveTo.waitForExistence(timeout: 5), "no Move To on the Agent:\n\(app.debugDescription)")
        shot("agent-menu")
        agentMoveTo.tap()
        sleep(1)
        XCTAssertTrue(app.buttons[word("On Its Own", "单独放着", "単独にする")].firstMatch.waitForExistence(timeout: 5),
                      "the Agent's choices did not open:\n\(app.debugDescription)")
        shot("agent-choices")
    }

    @MainActor func testUnpinConfirmation() {
        reset()
        launch()
        openSidebar()
        let lead = chat("Persona 产品")
        XCTAssertTrue(lead.waitForExistence(timeout: 10))
        lead.press(forDuration: 1.2)
        let unpin = app.buttons.matching(NSPredicate(format: "label IN %@", ["Unpin", "取消常驻", "常駐をやめる"])).firstMatch
        XCTAssertTrue(unpin.waitForExistence(timeout: 5), "no Unpin:\n\(app.debugDescription)")
        // A lead conversation files nowhere (`offers` with the model's leaders): its open menu has no Move To….
        XCTAssertFalse(app.buttons["move-to"].exists, "Move To… on a lead conversation")
        unpin.tap()
        let stays = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@",
            word("What you added to it stays.", "放进来的会话会留着。", "追加したものはそのまま残ります。"))).firstMatch
        XCTAssertTrue(stays.waitForExistence(timeout: 5), "the sentence of its own:\n\(app.debugDescription)")
        shot("unpin")
    }

    // MARK: The page's reads

    @MainActor func testReadStillOut() {
        reset("delay=12")
        launch()
        openPageFromSidebar()
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-reading"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-in-progress"].exists, "In Progress before the read")
        shot("reading")
    }

    @MainActor func testNeverReadAndUnavailable() {
        reset("read=fail")
        launch()
        openPageFromSidebar()
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-unread"].firstMatch.waitForExistence(timeout: 10),
                      "Couldn't read:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["direction-page-retry"].firstMatch.exists, "with Retry")
        shot("unread")
        reset("sessions_unavailable=1")
        app.buttons["direction-page-retry"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-sessions-unavailable"].firstMatch.waitForExistence(timeout: 10),
                      "Couldn't read what was added here:\n\(app.debugDescription)")
        shot("sessions-unavailable")
    }

    @MainActor func testOfflineAfterAnEarlierRead() {
        reset()
        launch()
        openPageFromSidebar()
        XCTAssertTrue(app.buttons.matching(identifier: "direction-page-filed").firstMatch.waitForExistence(timeout: 10))
        app.navigationBars.buttons.firstMatch.tap()
        sleep(1)
        addTeardownBlock { [self] in get("/mock/down?s=0") }
        get("/mock/down?s=90")
        sleep(6)
        openPageFromSidebar()
        let offline = app.buttons.matching(NSPredicate(format: "identifier == 'direction-page-filed' AND label CONTAINS %@",
                                                       word("Offline ·", "离线 ·", "オフライン ·"))).firstMatch
        XCTAssertTrue(offline.waitForExistence(timeout: 20), "rows read before say Offline · time:\n\(app.debugDescription)")
        shot("offline")
        get("/mock/down?s=0")
    }

    /// The hub out of reach on the page's first open in this run: Couldn't read, with Retry (never empty).
    @MainActor func testFirstOpenWithTheHubOutOfReach() {
        reset()
        launch()
        openSidebar()
        openFirstLead()
        addTeardownBlock { [self] in get("/mock/down?s=0") }
        get("/mock/down?s=90")
        sleep(6)
        let all = app.buttons["sidebar-direction-all"].firstMatch
        XCTAssertTrue(reveal(all))
        all.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-unread"].firstMatch.waitForExistence(timeout: 40),
                      "Couldn't read:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["direction-page-retry"].firstMatch.exists, "with Retry")
        shot("unread-offline")
        get("/mock/down?s=0")
    }

    /// Unpinned while the page is open (the tree push drops it): one read says it has ended, read-only. Then a page the
    /// hub no longer has: no longer available.
    @MainActor func testEndedAndGone() {
        reset()
        launch()
        openPageFromSidebar()
        XCTAssertTrue(app.buttons.matching(identifier: "direction-page-filed").firstMatch.waitForExistence(timeout: 10))
        get("/mock/direction_sessions?ended=lead0000-0000-4000-8000-000000000000")
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-ended"].firstMatch.waitForExistence(timeout: 15),
                      "the page did not say it ended:\n\(app.debugDescription)")
        XCTAssertFalse(app.descendants(matching: .any)["direction-page-in-progress"].exists, "no In Progress once ended")
        sleep(1)
        shot("ended")
        reset("read=gone")
        app.navigationBars.buttons.firstMatch.tap()
        sleep(1)
        openPageFromSidebar()
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-gone"].firstMatch.waitForExistence(timeout: 10),
                      "no longer available:\n\(app.debugDescription)")
        shot("gone")
    }

    /// Moving… while the hub has not answered (the mock holds the write), then the answer for the same place: Already in.
    @MainActor func testMovingThenAlreadyThere() {
        reset("put_delay=5")
        launch()
        openPageFromSidebar()
        let row = pageRow("整理首页改版的设计稿")
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.press(forDuration: 1.2)
        XCTAssertTrue(moveTo.waitForExistence(timeout: 5))
        moveTo.tap()
        let here = app.buttons["Persona 产品"].firstMatch
        XCTAssertTrue(here.waitForExistence(timeout: 5))
        here.tap()
        XCTAssertTrue(until { row.label.contains(word("Moving…", "正在移动…", "移動中…")) }, "Moving…: \(row.label)")
        shot("moving")
        let toast = app.descendants(matching: .any)["toast"].firstMatch
        XCTAssertTrue(toast.waitForExistence(timeout: 15))
        XCTAssertFalse(app.buttons["toast-action"].exists, "the same place offers no Undo")
        // Without an action the toast is one element, its label the sentence (`Toasts.swift`).
        let already = word("Already in “Persona 产品”", "已经在「Persona 产品」下", "すでに「Persona 产品」にあります")
        XCTAssertTrue(toast.label.contains(already), "the sentence says where: \(toast.label)")
        shot("already")
    }

    /// On Its Own, and an answer that never came back (Couldn't confirm the move).
    @MainActor func testOnItsOwnAndALostAnswer() {
        reset()
        launch()
        openPageFromSidebar()
        XCTAssertTrue(pageRow("写新版引导页的文案").waitForExistence(timeout: 10), "the page is read")
        // Sixth in the app's order: behind Show All 9.
        let more = app.buttons["direction-page-more"].firstMatch
        for _ in 0..<6 where !(more.exists && more.isHittable) { app.collectionViews.firstMatch.swipeUp(velocity: .slow) }
        more.tap()
        let recipe = pageRow("菜谱网站加搜索")
        XCTAssertTrue(recipe.waitForExistence(timeout: 10))
        recipe.press(forDuration: 1.2)
        XCTAssertTrue(moveTo.waitForExistence(timeout: 5))
        moveTo.tap()
        app.buttons[word("On Its Own", "单独放着", "単独にする")].firstMatch.tap()
        XCTAssertTrue(app.buttons["toast-action"].firstMatch.waitForExistence(timeout: 10), "Set on its own, with Undo")
        shot("own")
        sleep(6) // the toast goes
        post("/mock/fault", body: #"{"fault":"lost_receipt","enabled":true}"#)
        let chat = pageRow("早报")
        XCTAssertTrue(chat.waitForExistence(timeout: 10))
        chat.press(forDuration: 1.2)
        XCTAssertTrue(moveTo.waitForExistence(timeout: 5))
        moveTo.tap()
        app.buttons["增长"].firstMatch.tap()
        let toast = app.descendants(matching: .any)["toast"].firstMatch
        XCTAssertTrue(toast.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["toast-action"].exists, "a lost answer offers no Undo")
        shot("lost")
    }

    /// A pinned chat with nothing under it yet: Nothing Here Yet, and how something gets there.
    @MainActor func testNothingHereYet() {
        reset()
        let chat = "b2e4d6f8-0000-4000-8000-000000000003"
        post("/api/v1/assistant/directions/\(chat)/make", body: "{}")
        launchHolding(chat)
        openSidebar()
        let all = app.buttons.matching(NSPredicate(format: "identifier == 'sidebar-direction-all' AND value BEGINSWITH %@", "帮我约周五")).firstMatch
        XCTAssertTrue(reveal(all), "no All › for the new one:\n\(app.debugDescription)")
        all.tap()
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-empty"].firstMatch.waitForExistence(timeout: 15),
                      "Nothing Here Yet:\n\(app.debugDescription)")
        shot("empty")
    }

    // MARK: Nothing new where the change is off

    /// Acceptance 1 and 2: in each case where nothing of #11516 may be drawn, the sidebar (the first lead open), a
    /// conversation's menu, the title menu and an Agent's menu, kept for comparison with the same run of a build of
    /// main. Only identifiers that exist there too, so this file runs unchanged on that build. PERSONA_OFF_CASE:
    /// `none` (a hub with no directions: the mock with MOCK_LEADERS=0), `feature` (no `direction_sessions`), `manager`
    /// (another manager's page) or `missing` (the routes answer 404 without their codes: one Move To… learns it first).
    @MainActor func testOffCasesLookAsBefore() throws {
        guard let off = ProcessInfo.processInfo.environment["PERSONA_OFF_CASE"], !off.isEmpty else {
            throw XCTSkip("PERSONA_OFF_CASE=none|feature|manager|missing")
        }
        // That case runs against a mock with MOCK_SESSIONS_LIST=1 too (PERSONA_OFF_MANAGER_HUB), the setup its pairs
        // with main were taken with. Manager m-3fa21c's own chat does not reach its page there either: the conversation
        // compared below is its Main Chat row.
        if off == "manager" {
            guard let paged = ProcessInfo.processInfo.environment["PERSONA_OFF_MANAGER_HUB"], !paged.isEmpty else {
                XCTFail("PERSONA_OFF_MANAGER_HUB: the manager case needs a mock with MOCK_LEADERS=1 "
                        + "MOCK_DIRECTION_SESSIONS=1 MOCK_SESSIONS_LIST=1")
                return
            }
            hub = paged
        }
        reset(off == "feature" ? "feature=off" : off == "missing" ? "routes=missing" : "")
        if off == "manager" {
            launch(keepPick: true, extra: ["-openAssistantManager", "m-3fa21c"]) // manager m-3fa21c's page, picked at launch
            sleep(3)
        } else {
            launch()
        }
        openSidebar()
        if off == "missing" {
            let row = chat("帮我约周五")
            if row.waitForExistence(timeout: 10) {
                row.press(forDuration: 1.2)
                if app.buttons["move-to"].firstMatch.waitForExistence(timeout: 3) {
                    app.buttons["move-to"].firstMatch.tap()
                    app.buttons["增长"].firstMatch.tap()
                    sleep(8) // the toast goes; every entry has hidden
                } else {
                    dismissMenu()
                }
            }
        }
        let toggle = firstLeadToggle()
        if toggle.waitForExistence(timeout: 8), toggle.value as? String != expanded { toggle.tap() }
        // The first lead's rows to their end, where All › would be, with the next lead under them: scrolled until that
        // next row is above the New Chat bar (under the bar it still counts as hittable).
        if off != "none" && off != "manager" { revealAboveTheBar(chat("基础设施")) }
        sleep(2)
        shot("off-sidebar-\(off)")
        // Every case has a conversation's menu to compare. Manager m-3fa21c's own conversation (scripts/mock/direction_sessions_mock.py)
        // does not reach its page, on main's build and this one alike: there it is the page's one conversation row, Main Chat.
        let row = off == "manager" ? app.buttons["sidebar-main"].firstMatch : chat("帮我约周五")
        XCTAssertTrue(row.waitForExistence(timeout: 10), "no conversation to long-press:\n\(app.debugDescription)")
        row.press(forDuration: 1.2)
        sleep(1)
        // An item both builds' conversation menus have (manager m-3fa21c's Main Chat opens the chat itself, no menu).
        if off != "manager" {
            XCTAssertTrue(app.buttons[word("Rename…", "重命名…", "名前を変更…")].firstMatch.waitForExistence(timeout: 5),
                          "the conversation's menu did not open:\n\(app.debugDescription)")
        }
        shot("off-chat-menu-\(off)")
        // On manager m-3fa21c's page the press can open Main Chat itself, the drawer gone: no menu to dismiss there (a tap would
        // land on the chat's page), and no lead to look for.
        let drawerGone = off == "manager" && !app.buttons["sidebar-main"].firstMatch.isHittable
        if !drawerGone { dismissMenu() }
        let lead = off == "manager" ? nil : chat("Persona 产品")
        if let lead, lead.exists {
            lead.tap()
        } else {
            // No lead here (no directions, another manager's page): the main chat. On manager m-3fa21c's page a long press on
            // Main Chat opens no menu and can open the chat itself, the drawer gone: it is then what is in front.
            let main = app.buttons["sidebar-main"].firstMatch
            if main.exists && main.isHittable {
                main.tap()
            } else if off != "manager" {
                openSidebar()
                main.tap()
            }
        }
        sleep(2)
        openTitleMenu()
        sleep(1)
        // An item only the menu has (an iPad's sidebar has a Knowledge row of its own).
        XCTAssertTrue(app.buttons[word("Details", "详细信息", "詳細")].firstMatch.waitForExistence(timeout: 5),
                      "the title menu did not open:\n\(app.debugDescription)")
        shot("off-title-menu-\(off)")
        dismissMenu()
        // An Agent on no to-do (Move To… would be offered with the feature on): its chat's ⋯, the same inventory as its
        // row's long press (`AgentActionMenu`).
        app.terminate()
        launch(agentChat: "macbook/a1f3c2d4-0000-4000-8000-000000000007", keepPick: off == "manager")
        if off == "manager" { sleep(2) } // manager m-3fa21c stays selected on the device
        let more = app.buttons["chat.more"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 20), "no ⋯ on the Agent's chat:\n\(app.debugDescription)")
        sleep(2)
        if off == "missing" {
            // A launch is a new hello, which forgets the missing routes: one Move To… learns them again (main has none).
            more.tap()
            app.openAgentMenuMore() // Move To… is More's (#13396)
            if agentMoveTo.waitForExistence(timeout: 3) {
                agentMoveTo.tap()
                if app.buttons["增长"].firstMatch.waitForExistence(timeout: 5) { app.buttons["增长"].firstMatch.tap() }
                sleep(8)
            } else {
                dismissMenu(at: CGVector(dx: 0.08, dy: 0.62))
            }
        }
        more.tap()
        sleep(1)
        app.openAgentMenuMore() // Copy Directory is More's (#13396)
        XCTAssertTrue(app.buttons[word("Copy Path", "拷贝目录", "パスをコピー")].firstMatch.waitForExistence(timeout: 5),
                      "the Agent's menu did not open:\n\(app.debugDescription)")
        shot("off-agent-menu-\(off)")
    }

    /// VoiceOver's Expanded in the run's language.
    private var expanded: String { lang == "en" ? "Expanded" : lang == "ja" ? "展開済み" : "已展开" }

    /// Closes an open menu with a tap outside it: the dimmed page right of the drawer by default.
    @MainActor private func dismissMenu(at point: CGVector = CGVector(dx: 0.92, dy: 0.5)) {
        app.windows.firstMatch.coordinate(withNormalizedOffset: point).tap()
        sleep(1)
    }
}
