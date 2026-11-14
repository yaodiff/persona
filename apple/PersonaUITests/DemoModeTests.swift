import XCTest

/// Demo Mode (docs/apple/ios.md): from the pairing page into the app's own made-up hub (DemoHub, no server), every tab walked
/// through — Agents (filters, Compact List, a Claude and a Codex session, cards, a table, the terminal), the machines and
/// their 24-hour charts, Persona (sidebar, a conversation, Scheduled Tasks), the inbox (a Feishu chat, a mail) — and back out to
/// the pairing page from Settings. Needs no mock hub.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> saves a screenshot of each step there (with TEST_RUNNER_PERSONA_STYLE in the name).
final class DemoModeTests: XCTestCase {
    private var app: XCUIApplication!
    /// The agent at the top of the demo's list (weather's, needing you). The list is lazy: an agent further down (the
    /// working widget one, once the demo got more agents) is not in the view tree until scrolled to.
    private static let firstAgent = "补齐网络层的单元测试"

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            add(attachment(XCTAttachment(screenshot: app.screenshot()), "failure"))
            add(attachment(XCTAttachment(string: app.debugDescription), "view tree"))
        }
        super.tearDown()
    }

    private func attachment(_ a: XCTAttachment, _ name: String) -> XCTAttachment {
        a.name = name
        a.lifetime = .keepAlways
        return a
    }

    @MainActor private func shot(_ name: String) {
        let s = app.screenshot()
        add(attachment(XCTAttachment(screenshot: s), name))
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("demo-" + name + style + ".png"))
        }
    }

    @MainActor @discardableResult
    private func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        if !e.waitForExistence(timeout: timeout) { XCTFail("\(what) not found") }
        return e
    }

    @MainActor private func expandSource() {
        see(app.buttons["inbox.detail.source"].firstMatch, "the original source control", timeout: 15).tapCenter()
    }

    @MainActor private func text(_ s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    @MainActor private func anything(_ s: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    /// No page asked the demo hub for something it does not have, and nothing failed out loud.
    @MainActor private func noErrors(_ page: String) {
        XCTAssertFalse(anything("体验模式里没有这项数据").exists, "\(page): the demo hub had nothing for a request")
        XCTAssertFalse(anything("连不上 hub").exists, "\(page): tried to reach a hub")
    }

    /// The page's own Back (a chat page has two, the folded header's and the hidden full one, both "hittable"): each
    /// in turn until the page goes, then the edge swipe.
    @MainActor private func back() {
        let all = app.buttons.matching(NSPredicate(format: "label == %@", "返回"))
        for b in all.allElementsBoundByIndex.reversed() where b.exists && b.isHittable {
            let frame = b.frame
            b.tap()
            sleep(1)
            if !b.exists || b.frame != frame || !b.isHittable { return }
        }
        // The bar's back button, not a page's own bar buttons (the Agents list's New Session / Group By sit there too).
        let bar = app.navigationBars.buttons.matching(NSPredicate(format: "NOT (label IN %@) AND NOT (label BEGINSWITH %@)",
                                                                  ["新建会话", "新对话", "更多"], "分组")).firstMatch
        if bar.exists, bar.isHittable {
            bar.tap()
        } else {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5)).press(forDuration: 0.05, thenDragTo:
                app.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.5)))
        }
        sleep(1)
    }

    @MainActor private func tab(_ name: String) {
        if name == "Persona" {
            if !app.assistantDrawn() {
                ShellFinger(app).switchTo(agents: false, "Assistant in the demo walkthrough")
            }
        } else {
            if app.navigationBars["收件箱"].exists { back() }
            let id = app.assistantDrawn() ? "assistant-sidebar-button" : "work-sidebar-button"
            let drawer = see(app.buttons[id].firstMatch, "the visible page's drawer button")
            // Retained UIKit pages can confuse hit-testing queries after a push/pop; tap where the finger lands,
            // then require the actual drawer route to appear.
            drawer.tapCenter()
            let entry = name == "收件箱" ? app.buttons["sidebar-inbox"].firstMatch : app.drawerSettingsButton
            see(entry, "the drawer's \(name)").tapCenter()
        }
        sleep(1)
    }

    /// An agent's row in the Agents list (scrolling to it).
    @MainActor private func openAgent(_ title: String) {
        let list = app.buttons["shell-agents"].firstMatch
        // Back on the list already, it can take a moment to be hittable: wait for that first. A back() on the list
        // itself found no Back and tapped the first bar button, New Session, and the search went on inside its sheet.
        func onList() -> Bool { list.exists && list.isHittable }
        for _ in 0..<3 where !onList() {
            let settle = Date().addingTimeInterval(3)
            while !onList(), Date() < settle { usleep(200_000) }
            if !onList() { back() } // a back that did not take
        }
        let row = text(title)
        // Stay inside the visible list. The filters scroll with it; their former pinned height is not a top inset.
        func top() -> CGFloat {
            let nav = app.navigationBars.firstMatch
            let filters = app.buttons["agents.filter.all"]
            return max(nav.exists ? nav.frame.maxY : 0, filters.exists && filters.isHittable ? filters.frame.maxY : 0)
        }
        func clear() -> Bool {
            let bottom = app.frame.maxY
            return row.exists && row.frame.minY > top() + 8 && row.frame.maxY < bottom - 8
        }
        toTop()
        for _ in 0..<10 where !clear() { // a third of a screen at a time: a swipe would fling past it
            let up = !(row.exists && row.frame.minY <= top() + 8)
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.7 : 0.4))
            from.press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.4 : 0.7)))
            sleep(1)
        }
        see(row, "the agent \(title)").tap()
        sleep(2)
    }

    @MainActor private func toTop() {
        for _ in 0..<4 { app.swipeDown() }
    }

    @MainActor func testWalkThroughAndLeave() throws {
        app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-dev.openChat", "", "-openAgentsFresh", "YES", "-probes", "YES"]
        app.launchPinned()

        // The pairing page: the way in, on one line.
        let demo = see(app.buttons["welcome.demo"], "体验模式 on the pairing page", timeout: 15)
        XCTAssertTrue(app.buttons["welcome.pair"].exists)
        shot("welcome")
        demo.tap()

        // Agents: spaces in every state, the quick filters, Compact List.
        see(text(Self.firstAgent), "the demo's agents", timeout: 10)
        for _ in 0..<8 where !text("weather").exists { app.swipeUp() }
        XCTAssertTrue(text("weather").exists)
        toTop()
        noErrors("agents")
        shot("agents")
        see(app.buttons["agents.filter.unread"], "the 完成未看 filter").tap()
        see(text("整理待拍板事项"), "an unread agent")
        XCTAssertFalse(text("给天气 App 加主屏幕小组件").exists, "a working agent under 完成未看")
        shot("agents-unread")
        app.buttons["agents.filter.all"].tap()
        let menu = see(app.buttons["shell-agents"].firstMatch, "the list menu")
        menu.tap()
        see(app.buttons["细列表"], "细列表 in the menu").tap()
        see(app.buttons.matching(identifier: "agents.compactRow").firstMatch, "compact rows")
        shot("agents-compact")
        menu.tap()
        see(app.buttons["头像"], "头像 in the menu").tap() // back from Compact List (#2530: Avatar, the default style)
        sleep(1)

        // A Claude session: the work line, the to-do list, the edit; a message gets the demo's answer.
        openAgent("给天气 App 加主屏幕小组件")
        see(text("小组件的时间线已经能按小时刷新了"), "the Claude session's reply")
        noErrors("claude chat")
        shot("claude")
        let field = see(app.descendants(matching: .any)["shell-composer-field"].firstMatch, "the message field")
        field.tap()
        field.typeText("看一下进度")
        see(app.buttons["发送"], "send").tap()
        see(text("这是体验模式"), "the demo's answer to a message", timeout: 10)
        shot("claude-sent")

        // The terminal: the made-up Claude screen, its spinner ticking.
        app.swipeDown() // the keyboard away
        sleep(1)
        // The page's own bar: the compact copy that shows once the chat scrolled (the full one sits under the clock).
        // The terminal is in the header's ··· menu; a folded header (the slim bar) has no ···: a tap on its place opens it.
        let more = app.buttons["更多"].firstMatch
        let unfold = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "显示标题和标签")).firstMatch
        for _ in 0..<3 where !more.isHittable {
            if unfold.exists, unfold.isHittable { unfold.tap() } else { app.swipeDown() }
            sleep(1)
        }
        guard more.isHittable else { return XCTFail("no ··· on the session page:\n\(app.debugDescription)") }
        more.tap()
        see(app.buttons["终端"].firstMatch, "终端 in the ··· menu", timeout: 5).tap()
        see(app.descendants(matching: .any)["terminal-close"], "the terminal", timeout: 10)
        let tips = app.buttons["知道了"] // the terminal's tips, the first time
        if tips.waitForExistence(timeout: 3) { tips.tap() }
        sleep(2) // a few frames of the made-up screen
        shot("terminal")
        let close = app.descendants(matching: .any)["terminal-close"]
        if close.exists { close.tap() } else { back() }
        sleep(1)
        back()

        // A question card: answering it, the agent picks up (nothing is sent).
        openAgent("补齐网络层的单元测试")
        see(text("网络层的测试用哪个框架"), "the question card")
        shot("question")
        see(anything("Swift Testing"), "the recommended option").tap()
        let submit = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "提交")).firstMatch
        if submit.waitForExistence(timeout: 2), submit.isHittable { submit.tap() }
        see(text("收到，按你的选择接着做"), "the agent picking up after the answer", timeout: 10)
        back()

        // A plan card, a Codex session, a reply with a table and a link.
        openAgent("照片同步脚本支持 HEIC")
        see(anything("支持 HEIC"), "the plan card")
        shot("plan")
        back()
        openAgent("排查 /orders 接口变慢")
        see(text("N+1"), "the Codex session's reply")
        noErrors("codex chat")
        shot("codex")
        back()
        openAgent("整理待拍板事项")
        see(anything("小组件刷新频率"), "the table")
        shot("table")
        back()
        toTop()

        // Machines: all of them, one machine, its 24 hours.
        see(app.buttons["work-sidebar-button"].firstMatch, "Agents' drawer").tapCenter()
        see(app.buttons["sidebar-machines"].firstMatch, "Machines in the drawer").tap()
        let serverCard = see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "服务器、")).firstMatch, "the server's machine card")
        noErrors("machines")
        shot("machines")
        serverCard.tap()
        let range = see(app.descendants(matching: .any)["metric-range"].firstMatch, "the machine page") // either style's segmented control
        if !range.isHittable { app.swipeUp() }
        range.buttons["24 小时"].tap()
        see(app.descendants(matching: .any)["chart-CPU"], "the CPU chart")
        noErrors("machine")
        shot("machine-24h")
        back()
        back()

        // Persona: a conversation, the sidebar, Scheduled Tasks.
        tab("Persona")
        see(text("发布说明我来整理"), "the main conversation", timeout: 10)
        noErrors("persona")
        shot("persona")
        see(app.buttons["历史对话"], "历史对话").tapCenter()
        see(app.descendants(matching: .any)["assistant-sidebar"], "the sidebar")
        shot("persona-sidebar")
        see(app.buttons["assistant-drawer-close"], "the drawer's close card").tapCenter()
        sleep(1)
        see(app.buttons["shell-assistant"], "the avatar").tapCenter()
        see(app.buttons["详细信息"], "详细信息").tap()
        let routines = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "定时任务")).firstMatch
        for _ in 0..<4 where !(routines.exists && routines.isHittable) { app.swipeUp() }
        see(routines, "定时任务").tap()
        see(text("早报"), "the routines")
        shot("routines")
        back()
        back()

        // The inbox: a Feishu chat, a mail.
        tab("收件箱")
        see(text("周五的发布能赶上吗"), "the inbox", timeout: 10)
        noErrors("inbox")
        shot("inbox")
        text("周五的发布能赶上吗").tap()
        expandSource()
        see(text("下午我要和客户确认时间"), "the Feishu chat")
        noErrors("feishu")
        shot("feishu")
        back()
        let mail = text("下周的分享能不能提前到周二")
        for _ in 0..<3 where !(mail.exists && mail.isHittable) { app.swipeUp() }
        mail.tap()
        expandSource()
        see(text("周四我临时要出差"), "the mail")
        shot("mail")
        back()

        // Settings says it is the demo; leaving it is the pairing page again, and stays so after a relaunch.
        tab("设置")
        see(text("体验模式 · 数据都是编造的"), "体验模式 in 设置")
        shot("settings")
        let leave = app.buttons["settings.leaveDemo"]
        for _ in 0..<3 where !(leave.exists && leave.isHittable) { app.swipeUp() }
        see(leave, "退出体验模式").tap()
        see(app.buttons["welcome.demo"], "the pairing page after leaving")
        app.terminate()
        app.launchArguments = ["-dev.openChat", "", "-probes", "YES"]
        app.launchPinned()
        see(app.buttons["welcome.demo"], "the pairing page after a relaunch", timeout: 15)
    }

    /// Demo Mode is remembered: a relaunch opens the demo again, not the pairing page.
    @MainActor func testStaysAcrossLaunches() throws {
        app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-dev.openChat", "", "-probes", "YES"]
        app.launchPinned()
        see(app.buttons["welcome.demo"], "体验模式 on the pairing page", timeout: 15).tap()
        see(text(Self.firstAgent), "the demo's agents")
        app.terminate()
        app.launchArguments = ["-dev.openChat", "", "-probes", "YES"]
        app.launchPinned()
        see(text(Self.firstAgent), "the demo after a relaunch", timeout: 15)
        tab("设置")
        let leave = app.buttons["settings.leaveDemo"]
        for _ in 0..<3 where !(leave.exists && leave.isHittable) { app.swipeUp() }
        see(leave, "退出体验模式").tap()
        see(app.buttons["welcome.demo"], "the pairing page after leaving")
    }
}
