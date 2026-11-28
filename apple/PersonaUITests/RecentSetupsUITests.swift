import XCTest

/// New Agent's recent setups (#2134), through the form against scripts/mock/mock_hub.py started with MOCK_MACHINES=1
/// (an offline machine that reconnect brings back and /mock/reset takes offline again):
///   TEST_RUNNER_PERSONA_RECENTS_HUB=http://127.0.0.1:<port>
/// Three starts record three setups: a Claude Code one in Allow Everything, a Codex one on another machine and folder,
/// one on the machine that then goes offline. Reopened, New Agent shows them as cards; tapping one fills every field
/// and starts nothing. TEST_RUNNER_PERSONA_SHOTS=<dir> saves the screens, named with
/// TEST_RUNNER_PERSONA_RECENTS_APPEARANCE (light / dark: the simulator's own appearance, `simctl ui <udid> appearance`).
final class RecentSetupsUITests: XCTestCase {
    private var hub = ""
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_RECENTS_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_MACHINES=1 (PERSONA_RECENTS_HUB)")
        }
        hub = h
        _ = mock("reset") // the offline machine offline again, nothing started
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            add(attachment(XCTAttachment(screenshot: app.screenshot()), "failure"))
            add(attachment(XCTAttachment(string: app.debugDescription), "view tree"))
        }
        _ = mock("reset")
        super.tearDown()
    }

    /// The app again, at Agents. The folders come from the argument domain (it wins over what a start saves; the test
    /// never changes them); the machine is picked on screen each time, so it is not one of them.
    @MainActor private func launch() {
        app?.terminate()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-tab", "dev",
                               "-cwd.macbook", "/Users/me/code/blog", "-cwd.studio", "/Users/me/code/weather",
                               "-cwd.lab", "/home/me/code/shop-api"]
        app.launchPinned()
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
            let look = ProcessInfo.processInfo.environment["PERSONA_RECENTS_APPEARANCE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + look + ".png"))
        }
    }

    @discardableResult
    @MainActor private func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        if !e.waitForExistence(timeout: timeout) { XCTFail("\(what) not found") }
        return e
    }

    @MainActor private func button(beginning s: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", s)).firstMatch
    }

    @MainActor private func card(_ i: Int) -> XCUIElement { app.buttons["newChat.recent.\(i)"] }

    /// Scrolls the form until the row is on screen (a list makes only the rows on screen), down first, then up.
    @discardableResult
    @MainActor private func reveal(_ e: XCUIElement, _ what: String) -> XCUIElement {
        for _ in 0..<6 where !visible(e) { drag(from: 0.5, to: 0.2) }
        for _ in 0..<10 where !visible(e) { drag(from: 0.2, to: 0.5) }
        return see(e, what)
    }

    /// Scrolls the form by a drag in the upper half of the screen: with the keyboard up, a swipe at the middle lands on
    /// the Start button or the keyboard. From / to: fractions of the window's height.
    @MainActor private func drag(from: CGFloat, to: CGFloat) {
        let w = app.windows.firstMatch
        w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: from))
            .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: to)))
    }

    /// On screen between the navigation bar and the keyboard (`isHittable` throws for a row half under the bar).
    /// The row's label now, "" when it is gone (a row the form reloads can go between `exists` and `label`).
    @MainActor private func says(_ e: XCUIElement) -> String { (try? e.snapshot())?.label ?? "" }

    /// Frames come from snapshots: a row the form is reloading can go between `exists` and `frame`.
    @MainActor private func visible(_ e: XCUIElement) -> Bool {
        guard let f = (try? e.snapshot())?.frame, let w = (try? app.windows.firstMatch.snapshot())?.frame else { return false }
        let top = (try? app.navigationBars["新对话"].snapshot())?.frame.maxY ?? w.minY
        let bottom = (try? app.keyboards.firstMatch.snapshot())?.frame.minY ?? w.maxY
        return !f.isEmpty && f.minY >= top && f.maxY <= bottom
    }

    /// A machine's card in the row of machines (scrolled sideways into view).
    @MainActor private func machineCard(_ name: String) -> XCUIElement {
        let m = see(button(beginning: name), "\(name) card", timeout: 15)
        for _ in 0..<3 where !m.isHittable { button(beginning: "MacBook").swipeLeft() }
        return m
    }

    @MainActor private var prompt: XCUIElement {
        app.textViews["newChat.prompt"].exists ? app.textViews["newChat.prompt"] : app.textFields["newChat.prompt"]
    }

    private func mock(_ what: String, method: String = "GET") -> [String: Any] {
        var req = URLRequest(url: URL(string: hub + (what.hasPrefix("/") ? what : "/mock/" + what))!)
        req.httpMethod = method
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        var out: [String: Any] = [:]
        let done = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: req) { data, _, _ in
            out = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] } ?? [:]
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + 10)
        return out
    }

    /// First Message has the focus (the software keyboard may stay hidden while a hardware one is connected).
    @MainActor private var promptFocused: Bool {
        app.keyboards.firstMatch.exists || (prompt.exists && (prompt.value(forKey: "hasKeyboardFocus") as? Bool) == true)
    }

    private var started: [[String: Any]] { (mock("started")["started"] as? [[String: Any]]) ?? [] }

    @MainActor private func wait(_ what: String, _ test: () -> Bool) {
        let deadline = Date.now.addingTimeInterval(15)
        while Date.now < deadline, !test() { usleep(300_000) }
        XCTAssertTrue(test(), what)
    }

    @MainActor private func openNewAgent() {
        see(app.buttons["新建会话"], "新建会话", timeout: 20).tap()
        see(app.navigationBars["新对话"], "新对话 sheet")
    }

    /// Long-press a card and pick an item of its menu.
    @MainActor private func cardMenu(_ i: Int, _ item: String) {
        see(card(i), "card \(i)").press(forDuration: 1.2)
        see(app.buttons[item], "\(item) in the card's menu").tap()
    }

    /// Starts what the form holds and waits for the hub to get it; the sheet closes.
    @MainActor private func start(_ what: String) {
        let go = see(app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "开始对话")).firstMatch, "开始对话")
        wait("开始对话 enabled for \(what)") { go.isEnabled }
        let before = started.count
        go.tap()
        wait("POST /api/v1/sessions for \(what)") { started.count > before }
        XCTAssertFalse(app.navigationBars["新对话"].waitForExistence(timeout: 5), "the sheet closes on start")
        sleep(2) // the setup's file is written a moment after the start
    }

    /// Opens a menu picker row and picks an option; the row is scrolled up off the Start button floating at the bottom.
    @MainActor private func pickMenu(_ title: String, _ option: String) {
        let row = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", title)).firstMatch
        let go = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "开始对话")).firstMatch
        for _ in 0..<5 where !visible(row) || (go.exists && row.frame.maxY > go.frame.minY - 16) {
            app.swipeUp()
        }
        see(row, title).tap()
        see(app.buttons[option], option).tap()
    }

    @MainActor func testRecentSetupsFillTheFormAndNeverStart() throws {
        launch()
        openNewAgent()
        // Whatever an earlier run left: Clear Recent Setups (the card's menu) takes the section away.
        if card(0).waitForExistence(timeout: 5) {
            cardMenu(0, "清除最近配置")
            XCTAssertFalse(card(0).waitForExistence(timeout: 2), "cleared: no Recent section")
        }

        // 1. MacBook, blog, Model Configuration's launcher with opus, Allow Everything, High.
        machineCard("MacBook").tap()
        see(app.buttons.matching(NSPredicate(format: "identifier == 'newChat.directory' AND label CONTAINS 'blog'")).firstMatch,
            "blog, the folder MacBook remembers", timeout: 15)
        see(app.buttons["newChat.model"], "模型 row", timeout: 15).tap()
        see(app.buttons["newChat.model.opus"], "opus").tap()
        pickMenu("权限模式", "完全访问")
        pickMenu("思考强度", "高")
        start("the MacBook setup")
        XCTAssertEqual(started.last?["model"] as? String, "opus")
        XCTAssertEqual(started.last?["permission_mode"] as? String, "bypassPermissions")

        // 2. Mac Studio, weather, codex with gpt-5.5-codex.
        launch()
        openNewAgent()
        see(card(0), "the first setup's card")
        XCTAssertFalse(card(1).exists)
        machineCard("Mac Studio").tap()
        see(app.buttons.matching(NSPredicate(format: "identifier == 'newChat.directory' AND label CONTAINS 'weather'")).firstMatch,
            "weather", timeout: 15)
        see(app.buttons["newChat.launcher"], "启动器 row").tap()
        see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Codex'")).firstMatch, "codex launcher").tap()
        see(app.buttons["newChat.model"], "模型 row", timeout: 15).tap()
        see(app.buttons["newChat.model.gpt-5.5-codex"], "a Codex model").tap()
        let go = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "开始对话")).firstMatch
        for _ in 0..<4 where !visible(prompt) || (go.exists && prompt.frame.minY > go.frame.minY - 40) { app.swipeUp() }
        see(prompt, "the first message").tap()
        prompt.typeText("整理 README")
        start("the Codex setup")
        XCTAssertEqual(started.last?["launcher"] as? String, "codex")

        // 3. The lab machine, back for a moment: its defaults. Then it goes offline again.
        _ = mock("/api/v1/machines/lab/reconnect", method: "POST")
        launch()
        openNewAgent()
        let labMachine = machineCard("实验室主机")
        wait("the lab machine online now") { labMachine.isEnabled }
        labMachine.tap()
        see(app.buttons.matching(NSPredicate(format: "identifier == 'newChat.directory' AND label CONTAINS 'shop-api'")).firstMatch,
            "shop-api", timeout: 15)
        start("the lab setup")
        _ = mock("reset")

        // Reopened: three cards, newest first; the offline machine's is marked and can't be picked.
        launch()
        openNewAgent()
        let lab = see(card(0), "the lab card")
        wait("the lab card marked offline (the hub's list, after the saved one)") { says(lab).contains("离线") && (try? lab.snapshot())?.isEnabled == false }
        XCTAssertTrue(lab.label.contains("shop-api") && lab.label.contains("实验室主机"), lab.label)
        let codex = see(card(1), "the Codex card")
        XCTAssertTrue(codex.label.contains("weather") && codex.label.contains("codex") && codex.label.contains("gpt-5.5-codex"), codex.label)
        XCTAssertFalse(codex.label.contains("完全访问"), "a Codex card says no Claude mode")
        let bypass = see(card(2), "the Allow Everything card")
        XCTAssertTrue(bypass.label.contains("blog") && bypass.label.contains("MacBook") && bypass.label.contains("opus")
                      && bypass.label.contains("完全访问") && bypass.label.contains("高"), bypass.label)
        XCTAssertFalse(card(3).exists, "at most three")
        shot("recents-cards")

        // Picking MacBook by its machine card brings back its model too (its latest setup with this launcher).
        machineCard("MacBook").tap()
        let model = see(app.buttons["newChat.model"], "模型 row", timeout: 15)
        wait("MacBook's model is opus again") { says(model).localizedCaseInsensitiveContains("opus") }

        // The Codex card: another machine, its folder, launcher and model; the first message focused; nothing starts.
        lab.tap()
        XCTAssertTrue(started.isEmpty, "the offline card did nothing")
        codex.tap()
        wait("the first message focused") { promptFocused }
        let weather = app.buttons.matching(NSPredicate(format: "identifier == 'newChat.directory' AND label CONTAINS 'weather'")).firstMatch
        reveal(weather, "the card's folder")
        let launcher = reveal(app.buttons["newChat.launcher"], "启动器 row")
        wait("the card's launcher") { says(launcher).contains("Codex") }
        let codexModel = reveal(app.buttons["newChat.model"], "模型 row")
        wait("the card's model") { says(codexModel).contains("gpt-5.5-codex") }
        XCTAssertTrue(started.isEmpty, "a card only fills the form")
        shot("recents-picked-codex")

        // The Allow Everything card, back on MacBook: every field, Claude's mode and effort too.
        reveal(card(2), "the Allow Everything card").tap()
        let dir = app.buttons.matching(NSPredicate(format: "identifier == 'newChat.directory' AND label CONTAINS 'blog'")).firstMatch
        reveal(dir, "the card's folder")
        let modelRow = reveal(app.buttons["newChat.model"], "模型 row")
        wait("the card's model") { says(modelRow).localizedCaseInsensitiveContains("opus") }
        let launcherRow = reveal(app.buttons["newChat.launcher"], "启动器 row")
        wait("follows Model Configuration again") { !says(launcherRow).isEmpty && !says(launcherRow).contains("Codex") }
        let modeRow = reveal(app.buttons.matching(NSPredicate(format: "label BEGINSWITH '权限模式'")).firstMatch, "权限模式 row")
        wait("Allow Everything") { modeRow.exists && modeRow.staticTexts["完全访问"].exists }
        let effortRow = reveal(app.buttons.matching(NSPredicate(format: "label BEGINSWITH '思考强度'")).firstMatch, "思考强度 row")
        wait("High") { effortRow.exists && effortRow.staticTexts["高"].exists }
        XCTAssertTrue(started.isEmpty, "a card only fills the form")
        shot("recents-picked-bypass")
        reveal(card(2), "the Allow Everything card")
        for _ in 0..<2 { drag(from: 0.2, to: 0.5) }
        XCTAssertTrue(card(2).isSelected, "the picked card is marked")
        shot("recents-picked-bypass-top")

        // Remove from Recent takes one card; Clear Recent Setups the rest and the section.
        cardMenu(0, "从最近中移除")
        wait("two cards left") { !card(2).exists && card(1).exists }
        XCTAssertFalse(app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'newChat.recent' AND label CONTAINS '实验室主机'")).firstMatch.exists)
        cardMenu(0, "清除最近配置")
        wait("no Recent section") { !card(0).exists }
        XCTAssertTrue(started.isEmpty, "nothing started")
    }
}
