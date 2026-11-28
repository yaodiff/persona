import XCTest

/// A direction row's one activity mark (#6626): a standing (「常驻」) lead spins exactly while something under it runs
/// (the hub's `running`, not its working count) and says in words only what waits on you (「等你 N」), never 「在做 N」;
/// the To-dos page's standing sections end the same way. Against scripts/mock/mock_hub.py with MOCK_LEADERS=1
/// (PERSONA_LEADERS_HUB), whose tree has a lead with an Agent at work (「基础设施」) and one counting a working to-do whose
/// Agent has stopped (「增长」).
/// With the mock started with MOCK_DIRECTION_SESSIONS=1 too (#11516), the tests below turn its `direction_sessions` on
/// and off (`/mock/direction_sessions?feature=`) before each launch: off, a lead's open row and the menus are as before;
/// on, the row lists its live filed sessions and All ›, which opens the page, and Move To… with Undo restores the book.
/// SHOT_DIR keeps the screenshots.
final class DirectionRowsUITests: XCTestCase {
    private var hub: String!
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_LEADERS_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_LEADERS=1 (PERSONA_LEADERS_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(_ extra: [String] = []) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // The hub's default assistant whatever an earlier run left picked (`assistant.manager` outlives a launch; an
        // argument does not write it).
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-newAssistant", "YES", "-dev.openChat", "",
                               "-assistant.manager", "{}"] + extra
        app.launchPinned()
    }

    /// Launched straight into an Agent's chat ("machine/session").
    @MainActor private func launchInAgentChat(_ chat: String) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-tab", "dev", "-dev.openChat", chat,
                               "-assistant.manager", "{}"]
        app.launchPinned()
    }

    /// Waits (15 s at most) until `done`.
    private func until(_ what: String, _ done: () throws -> Bool) rethrows -> Bool {
        let end = Date().addingTimeInterval(15)
        while Date() < end {
            if try done() { return true }
            usleep(300_000)
        }
        return try done()
    }

    /// The book's entry of `key` as the mock holds it (nil: none).
    private func entry(_ key: String) throws -> [String: Any]? {
        (try mock("")["book"] as? [String: [String: Any]])?[key]
    }

    /// The page of the first lead, from its row's All › (the row held open for this launch).
    @MainActor private func openFirstPage() {
        openSidebar()
        openFirstLead()
        let all = app.buttons["sidebar-direction-all"].firstMatch
        XCTAssertTrue(reveal(all), "no All › under Persona 产品:\n\(app.debugDescription)")
        all.tap()
        XCTAssertTrue(app.descendants(matching: .any)["direction-page"].firstMatch.waitForExistence(timeout: 10))
    }

    /// A row of the page by the start of its title.
    @MainActor private func pageRow(_ title: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == 'direction-page-filed' AND label BEGINSWITH %@", title)).firstMatch
    }

    /// Move To… on a page row, then a choice (a direction's name, or On Its Own's words).
    @MainActor private func moveRow(_ row: XCUIElement, to choice: String) {
        row.press(forDuration: 1.2)
        let move = app.buttons["move-to"].firstMatch
        XCTAssertTrue(move.waitForExistence(timeout: 5), "no Move To…:\n\(app.debugDescription)")
        move.tap()
        let pick = app.buttons[choice].firstMatch
        XCTAssertTrue(pick.waitForExistence(timeout: 5), "no \(choice):\n\(app.debugDescription)")
        pick.tap()
    }

    /// The drawer's button on the phone (the hittable one of the pager's pages; tapped again while a busy simulator has
    /// not opened the drawer yet); the iPad's column is there already. Open once its To-dos entry shows: with four
    /// leads the All Chats row is below the fold, and the lazy list does not make it.
    /// The drawer opened a second time keeps where its list was scrolled, which can leave To-dos out of sight: its
    /// list on screen counts as open too.
    @MainActor private func openSidebar(timeout: TimeInterval = 30) {
        let all = app.buttons["sidebar-todos"].firstMatch
        let list = sidebarList
        let open = { (all.exists && all.isHittable) || (list.exists && list.isHittable) }
        let end = Date().addingTimeInterval(timeout)
        // The drawer's button: the conversation's, a shell place's (the Inbox), or the Sidebar layout's.
        let buttons = NSPredicate(format: "identifier IN %@", ["assistant-sidebar-button", "shell-place-sidebar", "sidebar-drawer-button"])
        while Date() < end && !open() {
            if let menu = app.buttons.matching(buttons).allElementsBoundByIndex.first(where: \.isHittable) {
                menu.tapCenter()
                // On screen, not merely there: a closed drawer's rows exist too.
                let opened = Date().addingTimeInterval(5)
                while Date() < opened && !open() { usleep(300_000) }
            } else {
                usleep(500_000)
            }
        }
        XCTAssertTrue(open(), "the sidebar did not open:\n\(app.debugDescription)")
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

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["SHOT_DIR"] {
            let suffix = ProcessInfo.processInfo.environment["SHOT_SUFFIX"].map { "-" + $0 } ?? ""
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/directions-\(name)\(suffix).png"))
        }
    }

    /// A lead's row, by its direction's name (the row's label starts with it).
    @MainActor private func lead(_ name: String) -> XCUIElement {
        app.buttons.matching(identifier: "sidebar-conversation").matching(NSPredicate(format: "label BEGINSWITH %@", name)).firstMatch
    }

    @MainActor func testALeadSpinsOnWhatRunsUnderItAndSaysOnlyWhatWaitsOnYou() {
        launch()
        openSidebar()
        let infra = lead("基础设施")
        XCTAssertTrue(infra.waitForExistence(timeout: 10), "no 基础设施 lead:\n\(app.debugDescription)")
        sleep(2) // the tree's read lands after the conversations
        shot("sidebar") // before the checks, so a failing run still shows what it drew
        // An Agent of one of its to-dos is at work: the row spins, and VoiceOver hears the spinner's word.
        XCTAssertTrue(infra.label.contains("工作中"), "基础设施 has an Agent at work but does not spin: \(infra.label)")
        XCTAssertTrue(infra.label.contains("等你 1"), "基础设施's to-do waiting on you is not said: \(infra.label)")
        // A working to-do whose Agent has stopped: nothing runs, so no spinner (the working count used to spin it).
        let growth = lead("增长")
        XCTAssertTrue(growth.exists, "no 增长 lead")
        XCTAssertFalse(growth.label.contains("工作中"), "增长 spins with nothing running under it: \(growth.label)")
        // One meaning, one expression: no lead says 「在做 N」 beside its spinner.
        let leads = app.buttons.matching(identifier: "sidebar-conversation").allElementsBoundByIndex
        for row in leads where ["基础设施", "增长", "支付对账", "Persona 产品"].contains(where: { row.label.hasPrefix($0) }) {
            XCTAssertFalse(row.label.contains("在做"), "a lead still says 在做: \(row.label)")
        }
    }

    // MARK: Move To… and the page (#11516)

    /// Asks the mock (`scripts/mock/direction_sessions_mock.py`) to answer differently; nil when it does not file sessions.
    @discardableResult private func mock(_ query: String) throws -> [String: Any] {
        let url = URL(string: hub + "/mock/direction_sessions?" + query)!
        var req = URLRequest(url: url)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let done = expectation(description: "mock")
        let box = MockAnswer()
        URLSession.shared.dataTask(with: req) { data, response, _ in
            if (response as? HTTPURLResponse)?.statusCode == 200 { box.data = data }
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        guard let data = box.data, let out = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw XCTSkip("needs the mock with MOCK_DIRECTION_SESSIONS=1 (scripts/mock/direction_sessions_mock.py)")
        }
        return out
    }

    /// The mock's answer, handed from URLSession's queue to the test.
    private final class MockAnswer: @unchecked Sendable { var data: Data? }

    /// Resets the mock's book and switches, then sets `query`.
    private func mockReset(_ query: String = "") throws {
        let done = expectation(description: "reset")
        var req = URLRequest(url: URL(string: hub + "/mock/reset")!)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        URLSession.shared.dataTask(with: req) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 10)
        try mock(query)
    }

    /// Scrolls the sidebar (a lazy list: rows below the fold are not made) until `element` is on screen.
    @MainActor @discardableResult private func reveal(_ element: XCUIElement, rounds: Int = 8) -> Bool {
        // Down the list first, then back up: the drawer keeps where it was scrolled.
        for round in 0..<rounds * 2 {
            if element.exists && element.isHittable { return true }
            if round < rounds { sidebarList.swipeUp(velocity: .slow) } else { sidebarList.swipeDown(velocity: .slow) }
            usleep(400_000)
        }
        return element.exists && element.isHittable
    }

    /// Show All N on the page, when the rows are more than five.
    @MainActor private func showAll() {
        let more = app.buttons["direction-page-more"].firstMatch
        for _ in 0..<6 where !(more.exists && more.isHittable) { app.collectionViews.firstMatch.swipeUp(velocity: .slow) }
        if more.exists { more.tap() }
    }

    /// The first lead held open for this launch only (its fold is the device's, `UserDefaults`; a launch argument does
    /// not write it), so the other tests here still find the leads below it on screen.
    static let firstLeadOpen = ["-assistant.direction.open.lead0000-0000-4000-8000-000000000000", "YES"]

    /// Opens the first lead's row (「Persona 产品」), which the mock files sessions under.
    @MainActor private func openFirstLead() {
        let toggle = app.buttons.matching(identifier: "sidebar-direction-toggle")
            .matching(NSPredicate(format: "label BEGINSWITH %@", "Persona 产品")).firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 15), "no fold for Persona 产品:\n\(app.debugDescription)")
        if toggle.value as? String != "已展开" { toggle.tap() }
        sleep(1)
    }

    @MainActor func testWithoutTheFeatureTheRowAndTheMenusAreAsBefore() throws {
        try mockReset("feature=off")
        launch(Self.firstLeadOpen)
        openSidebar()
        openFirstLead()
        shot("off-row")
        XCTAssertFalse(app.buttons["sidebar-direction-all"].exists, "All › without direction_sessions")
        XCTAssertFalse(app.buttons["sidebar-direction-filed"].exists, "filed rows without direction_sessions")
        let chat = app.buttons.matching(identifier: "sidebar-conversation")
            .matching(NSPredicate(format: "label BEGINSWITH %@", "帮我约周五")).firstMatch
        XCTAssertTrue(reveal(chat), "no 帮我约周五 chat:\n\(app.debugDescription)")
        chat.press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["拷贝诊断"].waitForExistence(timeout: 5), "the menu did not open:\n\(app.debugDescription)")
        shot("off-menu")
        XCTAssertFalse(app.buttons["move-to"].exists, "Move To… without direction_sessions")
    }

    @MainActor func testTheRowListsItsFiledSessionsAndAllOpensThePage() throws {
        try mockReset()
        launch(Self.firstLeadOpen)
        openSidebar()
        openFirstLead()
        let all = app.buttons["sidebar-direction-all"].firstMatch
        XCTAssertTrue(reveal(all), "no All › under Persona 产品:\n\(app.debugDescription)")
        XCTAssertGreaterThanOrEqual(app.buttons.matching(identifier: "sidebar-direction-filed").count, 2,
                                    "the live filed sessions are listed")
        shot("on-row")
        // What the row says waits on you, read before All › closes the drawer.
        let rowCounts = app.buttons.matching(identifier: "sidebar-direction-toggle")
            .matching(NSPredicate(format: "label BEGINSWITH %@", "Persona 产品")).firstMatch.label
            .components(separatedBy: "，").last ?? ""
        all.tap()
        let page = app.descendants(matching: .any)["direction-page"].firstMatch
        XCTAssertTrue(page.waitForExistence(timeout: 10), "All › did not open the page")
        XCTAssertTrue(app.navigationBars["Persona 产品"].waitForExistence(timeout: 5), "the page is titled by its name")
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-in-progress"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons.matching(identifier: "direction-page-filed").firstMatch.waitForExistence(timeout: 10))
        shot("page")
        // The page's "N need you" is the row's own words for the same tree (`DirectionCountsText`).
        XCTAssertTrue(rowCounts.hasPrefix("等你"), "the row says what waits on you: \(rowCounts)")
        let header = app.descendants(matching: .any)["direction-page-in-progress"].firstMatch.label
        XCTAssertTrue(header.contains(rowCounts), "the page says the same: \(header) / \(rowCounts)")
        // ‹ goes back to the lead conversation, not to the drawer; an iPad's sidebar column stays beside it.
        app.navigationBars.buttons.firstMatch.tap()
        XCTAssertTrue(page.waitForNonExistence(timeout: 5))
        if UIDevice.current.userInterfaceIdiom == .pad {
            XCTAssertTrue(app.buttons["sidebar-direction-all"].isHittable, "back at the conversation, the column still there")
        } else {
            XCTAssertFalse(app.buttons["sidebar-direction-all"].isHittable, "back at the conversation, the drawer closed")
        }
    }

    @MainActor func testMoveToThenUndoRestoresTheBook() throws {
        try mockReset()
        let key = "server/b2e4d6f8-0000-4000-8000-000000000003" // 「帮我约周五…」, in no direction
        let before = (try mock("")["book"] as? [String: Any])?[key]
        XCTAssertNil(before, "the chat starts in no direction")
        launch()
        openSidebar()
        let chat = app.buttons.matching(identifier: "sidebar-conversation")
            .matching(NSPredicate(format: "label BEGINSWITH %@", "帮我约周五")).firstMatch
        XCTAssertTrue(reveal(chat), "no 帮我约周五 chat:\n\(app.debugDescription)")
        chat.press(forDuration: 1.2)
        let move = app.buttons["move-to"].firstMatch
        XCTAssertTrue(move.waitForExistence(timeout: 5), "no Move To…:\n\(app.debugDescription)")
        shot("menu")
        move.tap()
        let choice = app.buttons["增长"].firstMatch
        XCTAssertTrue(choice.waitForExistence(timeout: 5))
        shot("choices")
        choice.tap()
        let undo = app.buttons["toast-action"].firstMatch
        XCTAssertTrue(undo.waitForExistence(timeout: 10), "no Undo on the toast")
        shot("toast")
        let filed = (try mock("")["book"] as? [String: [String: Any]])?[key]?["direction"] as? String
        XCTAssertEqual(filed, "lead0002-0000-4000-8000-000000000002")
        undo.tap()
        XCTAssertTrue(try until("Undo reached the hub") { try entry(key) == nil }, "Undo of a first filing takes the entry out (null)")
        // As the page shows it: not under 「增长」 (its row held open by a launch of its own).
        app.terminate()
        launch(["-assistant.direction.open.lead0002-0000-4000-8000-000000000002", "YES"])
        openSidebar()
        let growth = app.buttons.matching(NSPredicate(format: "identifier == 'sidebar-direction-all' AND value == %@", "增长")).firstMatch
        XCTAssertTrue(reveal(growth), "no All › under 增长:\n\(app.debugDescription)")
        growth.tap()
        XCTAssertTrue(app.descendants(matching: .any)["direction-page-in-progress"].firstMatch.waitForExistence(timeout: 10))
        sleep(3) // the page's read
        XCTAssertFalse(pageRow("帮我约周五").exists, "not on 增长's page after Undo")
    }

    // MARK: Criterion 3 shown by the row and the page (#12603 review)

    /// From another direction: an Agent the owner filed leaves the page the moment it is moved, and Undo puts it back on
    /// the page and in the row, with its filing time.
    @MainActor func testUndoFromAnotherDirectionShowsOnThePageAndTheRow() throws {
        try mockReset()
        let key = "macbook/dirs0001-0000-4000-8000-000000000001"
        let filed = try XCTUnwrap(try entry(key))
        launch(Self.firstLeadOpen)
        openFirstPage()
        let row = pageRow("整理首页改版的设计稿")
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        moveRow(row, to: "增长")
        XCTAssertTrue(row.waitForNonExistence(timeout: 5), "moved away: gone from this page at once")
        let undo = app.buttons["toast-action"].firstMatch
        XCTAssertTrue(undo.waitForExistence(timeout: 10), "no Undo")
        undo.tap()
        XCTAssertTrue(row.waitForExistence(timeout: 10), "Undo puts it back on the page")
        let back = try XCTUnwrap(try entry(key))
        XCTAssertEqual(back["at"] as? String, filed["at"] as? String, "with its filing time")
        app.navigationBars.buttons.firstMatch.tap()
        openSidebar()
        let inRow = app.buttons.matching(NSPredicate(format: "identifier == 'sidebar-direction-filed' AND label BEGINSWITH %@",
                                                     "整理首页改版的设计稿")).firstMatch
        XCTAssertTrue(reveal(inRow), "and in the row's filed sessions:\n\(app.debugDescription)")
    }

    /// The chat its leader opened (placed, never filed): moved, it leaves the page; Undo writes null and it is back, as
    /// opened from that chat.
    @MainActor func testUndoOfWhatTheLeaderOpenedShowsItBack() throws {
        try mockReset()
        launch(Self.firstLeadOpen)
        openSidebar()
        openFirstLead()
        let inRow = app.buttons.matching(NSPredicate(format: "identifier == 'sidebar-direction-filed' AND label BEGINSWITH %@",
                                                     "写新版引导页的文案")).firstMatch
        XCTAssertTrue(reveal(inRow), "the row lists what its leader opened:\n\(app.debugDescription)")
        let all = app.buttons["sidebar-direction-all"].firstMatch
        XCTAssertTrue(reveal(all))
        all.tap()
        XCTAssertTrue(app.descendants(matching: .any)["direction-page"].firstMatch.waitForExistence(timeout: 10))
        let row = pageRow("写新版引导页的文案")
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        let opened = row.label
        moveRow(row, to: "增长")
        XCTAssertTrue(row.waitForNonExistence(timeout: 5))
        let undo = app.buttons["toast-action"].firstMatch
        XCTAssertTrue(undo.waitForExistence(timeout: 10))
        undo.tap()
        XCTAssertTrue(row.waitForExistence(timeout: 10), "back on the page")
        XCTAssertTrue(until("the read again") { row.label == opened }, "as opened from that chat: \(row.label)")
        XCTAssertNil(try entry("studio/dirs0002-0000-4000-8000-000000000002"), "its entry taken out: the leader's placement again")
        app.navigationBars.buttons.firstMatch.tap()
        openSidebar()
        XCTAssertTrue(reveal(inRow), "and in the row again:\n\(app.debugDescription)")
        // Right after a move (no Undo this time) the row follows it too: gone from the row while its neighbour, drawn
        // just under it, is still there.
        XCTAssertTrue(reveal(all))
        all.tap()
        let again = pageRow("写新版引导页的文案")
        XCTAssertTrue(again.waitForExistence(timeout: 10))
        moveRow(again, to: "增长")
        XCTAssertTrue(again.waitForNonExistence(timeout: 5))
        app.navigationBars.buttons.firstMatch.tap()
        openSidebar()
        let neighbour = app.buttons.matching(NSPredicate(format: "identifier == 'sidebar-direction-filed' AND label BEGINSWITH %@",
                                                         "整理首页改版的设计稿")).firstMatch
        XCTAssertTrue(reveal(neighbour), "the row's other filed session:\n\(app.debugDescription)")
        XCTAssertTrue(until("the row follows the move") { !inRow.exists }, "still in the row after the move")
    }

    /// From On Its Own: set on its own, it leaves the page; moved under the first direction again from there and undone,
    /// it is on its own again (not on the page).
    @MainActor func testUndoFromOnItsOwnLeavesItOnItsOwn() throws {
        try mockReset()
        let key = "macbook/a1f3c2d4-0000-4000-8000-000000000007"
        launch(Self.firstLeadOpen)
        openFirstPage()
        XCTAssertTrue(pageRow("写新版引导页的文案").waitForExistence(timeout: 10), "the page is read")
        showAll() // sixth in the app's order, behind Show All 9
        let row = pageRow("菜谱网站加搜索")
        XCTAssertTrue(row.waitForExistence(timeout: 10), "no 菜谱 row:\n\(app.debugDescription)")
        moveRow(row, to: "单独放着")
        XCTAssertTrue(row.waitForNonExistence(timeout: 5))
        XCTAssertTrue(try until("on its own") { (try entry(key))?["direction"] as? String == "" })
        // From On Its Own back under 「Persona 产品」, through the mock as the Agent's own menu would, then Undo there.
        app.terminate()
        launchInAgentChat("macbook/a1f3c2d4-0000-4000-8000-000000000007")
        let more = app.buttons["chat.more"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 20))
        sleep(2)
        more.tap()
        app.openAgentMenuMore() // Move To… is More's (#13396)
        let move = app.buttons["移到"].firstMatch
        XCTAssertTrue(move.waitForExistence(timeout: 5), "no Move To… in the Agent's menu:\n\(app.debugDescription)")
        move.tap()
        app.buttons["Persona 产品"].firstMatch.tap()
        let undo = app.buttons["toast-action"].firstMatch
        XCTAssertTrue(undo.waitForExistence(timeout: 10))
        // What the Undo then takes back, as the screen says it (the toast with an action keeps its words apart).
        XCTAssertTrue(app.staticTexts["已移到「Persona 产品」下"].firstMatch.exists, "moved, as the toast says:\n\(app.debugDescription)")
        undo.tap()
        XCTAssertTrue(try until("on its own again") { (try entry(key))?["direction"] as? String == "" }, "Undo: on its own again")
        app.terminate()
        launch(Self.firstLeadOpen)
        openFirstPage()
        XCTAssertTrue(pageRow("早报").waitForExistence(timeout: 10), "the page is read")
        showAll()
        sleep(1)
        XCTAssertFalse(pageRow("菜谱网站加搜索").exists, "not on the page")
    }

    // MARK: Navigation (#12603 review, A2)

    /// All › of one pinned chat, another place, then All › of another: one page, over the second chat; ‹ is that chat.
    @MainActor func testAllFromAnotherPlaceOpensOnePageOverItsChat() throws {
        try mockReset()
        launch(Self.firstLeadOpen + ["-assistant.direction.open.lead0001-0000-4000-8000-000000000001", "YES"])
        try allThenInboxThenAll()
    }

    /// The same in the Sidebar layout, where each page the sidebar opens is a root of its own (the iPad's default).
    @MainActor func testAllFromAnotherRootInTheSidebarLayout() throws {
        try mockReset()
        launch(Self.firstLeadOpen + ["-assistant.direction.open.lead0001-0000-4000-8000-000000000001", "YES",
                                     "-app.navigation", "sidebar"])
        try allThenInboxThenAll()
    }

    @MainActor private func allThenInboxThenAll() throws {
        openFirstPage()
        XCTAssertTrue(app.navigationBars["Persona 产品"].waitForExistence(timeout: 5))
        // A pushed page has only ‹ on a phone (the drawer's button is the conversation's): back to the lead's chat
        // there. Where the sidebar stays on screen (an iPad's column) the page stays on the path, the case the fix is for.
        let column = app.buttons["sidebar-todos"].firstMatch
        if !(column.exists && column.isHittable) { app.navigationBars["Persona 产品"].buttons.firstMatch.tap() }
        openSidebar()
        let inbox = app.descendants(matching: .any).matching(identifier: "sidebar-inbox").firstMatch
        XCTAssertTrue(reveal(inbox), "no Inbox in the sidebar:\n\(app.debugDescription)")
        inbox.tap()
        sleep(2)
        openSidebar()
        // Each All › says whose it is (its value: the direction's name).
        let infra = app.buttons.matching(NSPredicate(format: "identifier == 'sidebar-direction-all' AND value == %@", "基础设施")).firstMatch
        XCTAssertTrue(reveal(infra), "no All › under 基础设施:\n\(app.debugDescription)")
        infra.tap()
        XCTAssertTrue(app.navigationBars["基础设施"].waitForExistence(timeout: 10), "the second page:\n\(app.debugDescription)")
        app.navigationBars["基础设施"].buttons.firstMatch.tap()
        sleep(2)
        XCTAssertFalse(app.descendants(matching: .any)["direction-page"].exists,
                       "‹ goes to the second chat, not to the first page:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons["inbox.more"].firstMatch.exists, "nor to the Inbox:\n\(app.debugDescription)")
    }

    // MARK: Chats opened from the page on an iPad (#12603 review, sixth look 1)

    /// On an iPad, Merged: a chat opened from the page (an Added Here row, an In Progress Agent line) is pushed over it,
    /// with ‹ back to the page, and the sidebar's selection stays the lead's.
    @MainActor func testAPageChatIsPushedOverThePageOnAnIPadMerged() throws { try pageChatsOnAnIPad("sidebar") }

    /// The same in Separate, where a pick would also move the switch to Agents.
    @MainActor func testAPageChatIsPushedOverThePageOnAnIPadSeparate() throws { try pageChatsOnAnIPad("separate") }

    @MainActor private func pageChatsOnAnIPad(_ layout: String) throws {
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("the split of an iPad") }
        try mockReset()
        launch(Self.firstLeadOpen + ["-app.navigation", layout])
        openFirstPage()
        let lead = app.buttons.matching(NSPredicate(format: "identifier == 'sidebar-conversation' AND label BEGINSWITH %@",
                                                    "Persona 产品")).firstMatch
        XCTAssertTrue(lead.waitForExistence(timeout: 10), "no row for the lead in the sidebar:\n\(app.debugDescription)")
        let selected = lead.isSelected
        let page = app.descendants(matching: .any).matching(identifier: "direction-page").firstMatch
        // The chat's own ‹ (`chevron.left`), or a navigation bar's.
        let back = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["chevron.left", "BackButton"])).firstMatch
        func openedOverThePage(_ what: String) {
            XCTAssertTrue(app.buttons["chat.more"].firstMatch.waitForExistence(timeout: 15), "\(what): no chat:\n\(app.debugDescription)")
            XCTAssertTrue(back.waitForExistence(timeout: 5), "\(what): no ‹ back to the page:\n\(app.debugDescription)")
            XCTAssertTrue(lead.exists && lead.isSelected == selected,
                          "\(what): the sidebar's selection moved:\n\(app.debugDescription)")
            back.tap()
            XCTAssertTrue(page.waitForExistence(timeout: 10), "\(what): ‹ did not go back to the page:\n\(app.debugDescription)")
        }
        // An Added Here row.
        let added = app.buttons.matching(NSPredicate(format: "identifier == 'direction-page-filed' AND label BEGINSWITH %@",
                                                     "整理首页改版的设计稿")).firstMatch
        XCTAssertTrue(added.waitForExistence(timeout: 10))
        added.tap()
        openedOverThePage("Added Here")
        // An In Progress Agent line: the page's own to-do row unfolded (the sidebar draws the same rows, so in the page).
        let toggle = page.buttons.matching(identifier: "sidebar-direction-task-toggle").firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 10), "no to-do with Agents on the page:\n\(app.debugDescription)")
        toggle.tap()
        let line = page.buttons.matching(identifier: "sidebar-direction-agent").firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 5), "no Agent line on the page:\n\(app.debugDescription)")
        line.tap()
        openedOverThePage("In Progress")
    }

    @MainActor func testTheToDosPinnedSectionsEndAsTheirRowsDo() {
        // The list view whatever an earlier test left (the calendar keeps itself per device).
        launch(["-todos.grouping", "direction", "-todos.view", "list"])
        openSidebar()
        app.buttons["sidebar-todos"].firstMatch.tap()
        let header = app.descendants(matching: .any).matching(identifier: "todos.direction").firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 15), "the To-dos page is not grouped by 常驻:\n\(app.debugDescription)")
        sleep(2)
        shot("todos")
        // A list builds a row, and its section's header, only once it is on screen, and 「增长」 sits below the fold on a
        // phone: read the headers while scrolling down to it.
        var labels: [String] = []
        let list = app.collectionViews.firstMatch
        for round in 0..<8 {
            for h in app.descendants(matching: .any).matching(identifier: "todos.direction").allElementsBoundByIndex where !labels.contains(h.label) {
                labels.append(h.label)
            }
            if labels.contains(where: { $0.hasPrefix("增长") }) || round == 7 { break }
            list.swipeUp(velocity: .slow)
        }
        XCTAssertFalse(labels.isEmpty)
        for label in labels {
            XCTAssertFalse(label.contains("在做"), "a 常驻 section still says 在做: \(label)")
        }
        // the spinner exactly where something runs: 「基础设施」 has an Agent at work, 「增长」's working to-do has none
        let infra = labels.first { $0.hasPrefix("基础设施") }
        XCTAssertTrue(infra?.contains("工作中") == true, "基础设施's section does not spin: \(infra ?? "missing")")
        let growth = labels.first { $0.hasPrefix("增长") }
        XCTAssertTrue(growth.map { !$0.contains("工作中") } ?? false, "增长's section spins or is missing: \(growth ?? "missing")")
    }

    @MainActor func testCancelledTodoStaysOutOfPinnedRowsInItsOwnFold() {
        launch(["-todos.grouping", "direction", "-todos.view", "list", "-todos.filter", "all", "-todos.sort", "recent"])
        openSidebar()
        app.buttons["sidebar-todos"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["todos.direction"].firstMatch.waitForExistence(timeout: 15),
                      "the To-dos page is not grouped by 常驻:\n\(app.debugDescription)")
        let fold = app.descendants(matching: .any)["todos.section.cancelled"]
        let row = app.buttons["todos.row.todo_cut_login"]
        let list = app.collectionViews.firstMatch
        for _ in 0..<24 where !fold.exists { list.swipeUp() }
        XCTAssertTrue(fold.exists, "no independent Cancelled fold in direction grouping:\n\(app.debugDescription)")
        XCTAssertFalse(row.exists, "a cancelled to-do stayed in an open direction row")
        for _ in 0..<8 where !fold.isHittable { list.swipeUp() }
        fold.tap()
        for _ in 0..<8 where !row.exists { list.swipeUp() }
        XCTAssertTrue(row.exists, "opening Cancelled did not reveal its to-do")
        shot("todos-cancelled")
    }
}
