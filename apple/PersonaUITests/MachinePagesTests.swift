import XCTest

/// Every tappable thing on the machine pages (Settings › Machines, a machine's details, an Agents list's machine menu, a
/// machine's projects, New Chat's machine / directory / launcher), against scripts/mock/mock_hub.py started with
/// MOCK_MACHINES=1 (an offline machine that reconnect brings back, a machine with 36 agents):
///   TEST_RUNNER_PERSONA_MACHINES_HUB=http://127.0.0.1:<port>
/// TEST_RUNNER_PERSONA_SHOTS=<dir> also saves a screenshot of each step there (docs/operations/testing.md「Machine pages」).
final class MachinePagesTests: XCTestCase {
    private let widget = "a1f3c2d4-0000-4000-8000-000000000001"
    private var hub = ""
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MACHINES_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_MACHINES=1 (PERSONA_MACHINES_HUB)")
        }
        hub = h
        _ = mock("reset") // the offline machine offline again, no sessions started
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            add(attachment(XCTAttachment(screenshot: app.screenshot()), "failure"))
            add(attachment(XCTAttachment(string: app.debugDescription), "view tree"))
        }
        super.tearDown()
    }

    private func launch(_ args: [String], machine: String = "macbook") {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // Nothing kept from an earlier run: the last dev chat (reopened at launch), New Chat's last machine and launcher. The argument domain wins over what the app saves, so only for values the test never changes.
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-newChat.machine", machine, "-launcher.\(machine)", ""] + args
        app.launchPinned()
    }

    private func attachment(_ a: XCTAttachment, _ name: String) -> XCTAttachment {
        a.name = name
        a.lifetime = .keepAlways
        return a
    }

    /// Saves a screenshot of the step (attachment, and the shots directory when given).
    private func shot(_ name: String) {
        let s = app.screenshot()
        add(attachment(XCTAttachment(screenshot: s), name))
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + style + ".png"))
        }
    }

    @discardableResult
    private func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        if !e.waitForExistence(timeout: timeout) { XCTFail("\(what) not found") }
        return e
    }

    private func text(_ s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    private func button(_ s: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    /// New Chat's card for a machine by its id, not a Recent setup row naming it (#2134: a start in an earlier test
    /// leaves one, and tapping it fills that setup in).
    private func machineCard(_ id: String) -> XCUIElement { app.buttons["newChat.machine.\(id)"] }

    /// The new chat knows its session: the slim bar names it as the open header does (#3631), not 「对话」.
    private func seeChatNamed(_ name: String) {
        let title = see(app.buttons["chat.compactTitle"], "the new chat's name in its header")
        XCTAssertTrue(title.label.contains(name), title.label)
    }

    private func mock(_ what: String) -> [String: Any] {
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/" + what)!),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
        return obj
    }

    private func hits(_ path: String) -> Int { (mock("hits")["hits"] as? [String: Int])?["GET " + path] ?? 0 }

    private func wait(_ what: String, _ test: () -> Bool) {
        let deadline = Date.now.addingTimeInterval(10)
        while Date.now < deadline, !test() { usleep(300_000) }
        XCTAssertTrue(test(), what)
    }

    private func pullToRefresh() {
        let w = app.windows.firstMatch
        w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3))
            .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85)))
    }

    private func openSettingsMachines() {
        launch(["-tab", "settings"])
        // Below Interface Style (styles and Text Size): on a phone the row starts under the fold, and the list only makes rows on screen.
        _ = app.navigationBars["设置"].waitForExistence(timeout: 15)
        for _ in 0..<4 where !button("在线").exists { app.swipeUp() }
        let row = see(button("在线"), "设置 › 机器 row (n / m 在线)", timeout: 15)
        XCTAssertTrue(row.label.contains("3 / 4 在线") || row.label.contains("4 / 4 在线"), row.label)
        row.tap()
        see(app.navigationBars["机器"], "机器 page")
    }

    // MARK: Settings › Machines

    /// The list: every machine, the offline one with its error; pull to refresh asks the hub again; a card opens
    /// its details, a running agent there opens its chat.
    @MainActor func testSettingsMachineList() throws {
        openSettingsMachines()
        for name in ["服务器", "MacBook", "Mac Studio", "实验室主机"] { see(text(name), "card \(name)") }
        see(text("Connection refused"), "the offline machine's error")
        // The hub's machine runs the Chat tab's assistant too: its conversations are not counted as agents.
        let server = see(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "服务器")).firstMatch, "服务器 card")
        XCTAssertTrue(server.label.contains("1 个 Agent 工作中"), server.label)
        shot("machines-list")

        let before = hits("/api/v1/machines")
        pullToRefresh()
        wait("pull to refresh reloads the machines") { hits("/api/v1/machines") > before }

        text("MacBook").tap()
        see(app.navigationBars["MacBook"], "MacBook details")
        see(app.staticTexts["Agent"], "the agents' header")
        see(app.descendants(matching: .any)["metric-range"].firstMatch, "its charts on the same page")
        shot("machine-detail")
        for _ in 0..<6 where !app.staticTexts["启动器"].isHittable { app.swipeUp() }
        see(app.staticTexts["启动器"], "launchers header")
        see(app.staticTexts["claude"], "a launcher")
        for _ in 0..<6 where !text("ssh macbook").isHittable { app.swipeUp() }
        see(text("ssh macbook"), "how the hub reaches it")
        for _ in 0..<6 where !button("补齐网络层的单元测试").isHittable { app.swipeDown() }
        let row = see(button("补齐网络层的单元测试"), "a running agent")
        row.tap()
        see(app.textFields.firstMatch.exists ? app.textFields.firstMatch : app.textViews.firstMatch, "the agent's chat composer")
        app.buttons["返回"].firstMatch.tap() // the chat draws its own bar
        see(app.navigationBars["MacBook"], "back on the details")
    }

    /// An offline machine: its details say so instead of an empty launcher list; Reconnect asks the hub, the page
    /// turns online and its launchers load.
    @MainActor func testReconnectOfflineMachine() throws {
        openSettingsMachines()
        text("实验室主机").tap()
        see(app.navigationBars["实验室主机"], "details of the offline machine")
        see(text("离线"), "offline badge")
        shot("machine-detail-offline")
        let reconnect = see(button("重新连接"), "重新连接")
        reconnect.tap()
        wait("POST …/reconnect sent") { (mock("reconnects")["reconnects"] as? [String])?.contains("lab") == true }
        see(text("在线"), "online after reconnecting", timeout: 15)
        for _ in 0..<6 where !app.staticTexts["启动器"].isHittable { app.swipeUp() }
        see(app.staticTexts["claude"], "launchers load once it is online", timeout: 15)
        shot("machine-detail-reconnected")
    }

    /// Settings › Claude Code Settings: its machine picker offers the online machines only, and switching asks that machine.
    @MainActor func testClaudeSettingsMachinePicker() throws {
        launch(["-tab", "settings"])
        see(button("Claude Code 设置"), "Claude Code 设置 row", timeout: 15).tap()
        let picker = see(button("机器"), "machine picker")
        // The picker remembers its machine: switch to the other one.
        let (name, id) = picker.label.contains("Mac Studio") ? ("MacBook", "macbook") : ("Mac Studio", "studio")
        picker.tap()
        see(button(name), "an online machine")
        XCTAssertFalse(button("实验室主机").exists, "an offline machine is not offered")
        shot("claude-settings-machines")
        let before = hits("/api/v1/machines/\(id)/claude-settings")
        button(name).tap()
        wait("switching loads that machine's settings") { hits("/api/v1/machines/\(id)/claude-settings") > before }
    }

    // MARK: Agents

    /// The Agents list's top-left menu: By Machine shows a section per machine, a machine filter narrows the list and
    /// shows in the button, All Machines clears it.
    @MainActor func testAgentsMachineMenu() throws {
        launch(["-tab", "dev"])
        let menu = see(app.buttons["shell-agents"].firstMatch, "the machine menu", timeout: 15)
        // From the defaults, whatever an earlier run left (the menu's choices are remembered).
        let previousFilter = app.buttons["agents.machineFilter"].firstMatch
        if previousFilter.exists { previousFilter.tap() }
        menu.tap()
        see(button("按机器"), "按机器").tap()
        see(app.staticTexts["服务器"], "a machine section")
        see(app.staticTexts["MacBook"], "another machine section")
        shot("agents-by-machine")

        menu.tap()
        see(button("Mac Studio"), "machine filter item").tap()
        let filtered = see(app.buttons["agents.machineFilter"].firstMatch, "filter shown")
        XCTAssertEqual(filtered.label, "只看 Mac Studio")
        // MacBook's rows are gone (its question stays in Needs You: those cards are for every machine).
        XCTAssertFalse(text("给天气 App 加主屏幕小组件").exists, "MacBook's agents are filtered out")
        shot("agents-filter-studio")
        filtered.tap()
        XCTAssertTrue(filtered.waitForNonExistence(timeout: 5), "filter was not cleared")
        see(app.buttons["shell-agents"].firstMatch, "filter cleared")
    }

    /// A project page's machine name opens the machine's projects; there a project opens its page, the info button
    /// the machine's details. Mac Studio has 36 agents in 5 projects.
    @MainActor func testMachineProjects() throws {
        launch(["-tab", "dev", "-openMachinePage", "studio"])
        see(app.navigationBars["Mac Studio"], "Mac Studio's projects", timeout: 15)
        see(text("个项目"), "summary")
        see(text("工作中"), "busy projects")
        shot("machine-projects")
        let project = see(button("infra"), "a project")
        project.tap()
        let machineLink = see(button("Mac Studio 上的所有项目"), "the project page's machine name")
        shot("space-page")
        machineLink.tap()
        see(app.navigationBars["Mac Studio"], "machine projects again from the project page")
        app.buttons["机器详情"].tap()
        see(app.descendants(matching: .any)["metric-range"].firstMatch, "details from the info button, with the charts")
        // Needs-you first, five of them; the rest behind Show All so the charts stay near.
        XCTAssertFalse(button("infra 的第 1 个任务").exists, "a working agent behind the ones needing you")
        shot("machine-detail-many")
        see(button("显示全部"), "显示全部").tap() // 「显示全部 38 个」: the 36 plus the two Mac Studio always has
        let agent = see(button("infra 的第 1 个任务"), "one of the 36 agents")
        agent.tap()
        see(app.textFields.firstMatch.exists ? app.textFields.firstMatch : app.textViews.firstMatch, "that agent's chat, on top")
        app.buttons["返回"].firstMatch.tap() // the chat draws its own bar
        for _ in 0..<12 where !text("ssh studio").exists { app.swipeUp() }
        see(text("ssh studio"), "studio's footer, below its 36 agents")
    }

    /// An offline machine's projects page explains why it is empty.
    @MainActor func testOfflineMachineProjects() throws {
        launch(["-tab", "dev", "-openMachinePage", "lab"])
        see(app.navigationBars["实验室主机"], "the offline machine's page", timeout: 15)
        see(text("这台机器现在离线"), "offline explanation")
        shot("machine-projects-offline")
        app.buttons["机器详情"].tap()
        see(button("重新连接"), "reconnect from there")
    }

    // MARK: New Chat

    /// A start the computer refuses says why next to the button (it sat below the form, unseen) and stays open.
    @MainActor func testNewSessionFailureShowsWhy() throws {
        launch(["-tab", "dev", "-cwd.macbook", "/Users/me/code/broken"])
        see(app.buttons["新建会话"], "新建会话", timeout: 15).tap()
        see(machineCard("macbook"), "MacBook card").tap()
        see(button("开始对话"), "开始对话").tap()
        let err = see(app.descendants(matching: .any).matching(identifier: "newChat.error").firstMatch, "the error next to the button")
        XCTAssertTrue(err.label.contains("did not become ready"), err.label)
        XCTAssertTrue(err.isHittable, "on screen, not below the form")
        XCTAssertTrue(app.navigationBars["新对话"].exists, "the sheet stays to try again")
        shot("new-chat-failed")
    }

    /// New Session: the offline machine cannot be picked; pick a machine, a directory (browse into it), a launcher and
    /// start: POST /api/v1/sessions with those, and the new chat opens.
    @MainActor func testNewSessionPicksMachineDirectoryLauncher() throws {
        launch(["-tab", "dev"])
        see(app.buttons["新建会话"], "新建会话", timeout: 15).tap()
        see(app.navigationBars["新对话"], "新对话 sheet")
        let lab = see(button("实验室主机"), "offline machine card")
        XCTAssertFalse(lab.isEnabled, "an offline machine cannot be picked")
        see(machineCard("macbook"), "MacBook card").tap()
        shot("new-chat")

        see(app.buttons["newChat.directory"], "directory row").tap()
        // It opens at the directory picked (the last one used): up to ~/code when that is a project.
        let blog = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "blog")).firstMatch // not Use “blog”
        see(button("上一级"), "the directory browser")
        for _ in 0..<3 where !app.navigationBars["code"].exists { button("上一级").tap(); usleep(500_000) }
        see(blog, "a directory").tap()
        see(button("使用「blog」"), "use this directory").tap()
        see(text("blog"), "directory picked")

        see(app.buttons["newChat.launcher"], "launcher row").tap()
        see(app.navigationBars["启动器"], "launcher picker")
        shot("launcher-picker")
        see(button("sonnetcc"), "a Claude launcher").tap()
        see(text("sonnetcc"), "launcher picked")

        // Tab name (optional, below the fold): what is typed names the herdr tab.
        let tabName = app.textFields["newChat.tabName"]
        for _ in 0..<4 where !tabName.isHittable { app.swipeUp() }
        see(tabName, "the tab name field").tap()
        tabName.typeText("blog-astro")
        shot("new-chat-tab-name")
        app.keyboards.buttons.matching(NSPredicate(format: "label IN %@", ["完成", "done", "Done"])).firstMatch.tap()

        // First Message goes out as an ordinary message of the new chat (on screen at once, typed in once claude is up).
        let prompt = app.textViews["newChat.prompt"].exists ? app.textViews["newChat.prompt"] : app.textFields["newChat.prompt"]
        for _ in 0..<4 where !prompt.isHittable { app.swipeDown() }
        see(prompt, "the first message field").tap()
        prompt.typeText("迁移到 Astro")

        let start = see(button("开始对话"), "开始对话")
        XCTAssertTrue(start.isEnabled)
        start.tap()
        wait("POST /api/v1/sessions sent") { !((mock("started")["started"] as? [[String: Any]]) ?? []).isEmpty }
        let body = ((mock("started")["started"] as? [[String: Any]]) ?? []).last ?? [:]
        XCTAssertEqual(body["machine_id"] as? String, "macbook")
        XCTAssertEqual(body["cwd"] as? String, "/Users/me/code/blog")
        XCTAssertEqual(body["launcher"] as? String, "sonnetcc")
        XCTAssertEqual(body["tab_name"] as? String, "blog-astro")
        XCTAssertEqual(body["async"] as? Bool, true, "the hub answers at once and starts claude in the background")
        XCTAssertNil(body["prompt"], "the first message is not part of the start")
        XCTAssertEqual(body["name_hint"] as? String, "迁移到 Astro")
        XCTAssertFalse(app.navigationBars["新对话"].waitForExistence(timeout: 3), "the sheet closes on start")
        see(app.descendants(matching: .any)["chat.starting"], "正在启动 under the new chat")
        see(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "迁移到 Astro")).firstMatch, "the first message on screen")
        wait("the first message sent") {
            ((mock("sent")["sent"] as? [[String: Any]]) ?? []).contains { ($0["text"] as? String) == "迁移到 Astro" }
        }
        shot("new-chat-starting")
        seeChatNamed("迁移到 Astro")
        shot("new-chat-started")
    }

    /// New Session's Model (#794): the list the launcher's CLI shows in /model, searchable; the pick goes with the start,
    /// and only the picked model's efforts are offered.
    @MainActor func testNewSessionPicksAModel() throws {
        launch(["-tab", "dev", "-cwd.macbook", "/Users/me/code/blog"])
        see(app.buttons["新建会话"], "新建会话", timeout: 15).tap()
        shot("new-chat-machines")
        see(machineCard("server"), "Server card").tap()
        // Model Settings' launcher (claude in the mock) runs; with nothing picked, so does its model.
        let row = see(app.buttons["newChat.model"], "模型 row", timeout: 15)
        XCTAssertTrue(row.label.contains("默认模型") && row.label.contains("模型配置：model-chat"), row.label)
        shot("new-chat-model")
        row.tap()
        see(app.navigationBars["模型"], "model picker")
        let alias = see(app.buttons["newChat.model.opus"], "an alias")
        shot("model-picker")
        let aliasDetail = app.staticTexts["claude · opus → claude-opus-5-5"].firstMatch
        XCTAssertTrue(aliasDetail.waitForExistence(timeout: 2), alias.label)
        let defaultRow = see(app.buttons["newChat.model.default"], "默认 first")
        let defaultDetail = app.staticTexts["claude · 模型配置：model-chat"].firstMatch
        XCTAssertTrue(defaultDetail.waitForExistence(timeout: 2), defaultRow.label)
        // The picker's own field, not the Agents page's behind the sheet.
        let search = see(app.searchFields.matching(NSPredicate(format: "label CONTAINS %@", "模型 ID")).firstMatch, "the search field")
        search.tap()
        search.typeText("glm")
        XCTAssertFalse(app.buttons["newChat.model.opus"].waitForExistence(timeout: 1), "the search leaves out what does not match")
        shot("model-picker-search")
        see(app.buttons["newChat.model.gateway/glm-5.3"], "a gateway model").tap()
        let picked = see(app.buttons["newChat.model"], "模型 row")
        XCTAssertTrue(picked.label.contains("glm-5.3"), picked.label)

        // glm-5.3 takes low / medium / high only.
        let effort = button("思考强度")
        for _ in 0..<4 where !effort.isHittable { app.swipeUp() }
        see(effort, "the effort picker").tap()
        see(app.buttons["高"], "high, which it takes")
        XCTAssertFalse(app.buttons["很高"].exists, "an effort the model does not take")
        app.buttons["高"].tap()
        shot("new-chat-model-picked")
        see(button("开始对话"), "开始对话").tap()
        wait("POST /api/v1/sessions sent") { !((mock("started")["started"] as? [[String: Any]]) ?? []).isEmpty }
        let body = ((mock("started")["started"] as? [[String: Any]]) ?? []).last ?? [:]
        XCTAssertEqual(body["model"] as? String, "gateway/glm-5.3")
        XCTAssertEqual(body["effort"] as? String, "high")
    }

    /// A launcher passing its own --model says why its model cannot be picked; a Codex launcher's list picks with no
    /// effort sent.
    @MainActor func testNewSessionModelLockedAndCodex() throws {
        launch(["-tab", "dev", "-cwd.macbook", "/Users/me/code/blog"])
        see(app.buttons["新建会话"], "新建会话", timeout: 15).tap()
        see(machineCard("macbook"), "MacBook card").tap()
        see(app.buttons["newChat.launcher"], "launcher row").tap()
        see(button("sonnetcc"), "a launcher with its own --model").tap()
        let locked = see(app.descendants(matching: .any)["newChat.model.locked"], "the locked model row", timeout: 15)
        XCTAssertTrue(locked.label.contains("固定"), locked.label)
        XCTAssertFalse(app.buttons["newChat.model"].exists, "no list to open")
        shot("new-chat-model-locked")

        see(app.buttons["newChat.launcher"], "launcher row").tap()
        see(button("Codex"), "codex launcher").tap()
        see(app.buttons["newChat.model"], "模型 row", timeout: 15).tap()
        see(app.buttons["newChat.model.gpt-5.5-codex"], "a Codex model").tap()
        let picked = see(app.buttons["newChat.model"], "模型 row")
        XCTAssertTrue(picked.label.contains("GPT-5.5 Codex"), picked.label) // GPT names keep their GPT- prefix (SessionModes.gptModelName)
        let prompt = app.textViews["newChat.prompt"].exists ? app.textViews["newChat.prompt"] : app.textFields["newChat.prompt"]
        for _ in 0..<4 where !prompt.isHittable { app.swipeUp() }
        see(prompt, "the first message field").tap()
        prompt.typeText("整理 README")
        shot("new-chat-model-codex")
        see(button("开始对话"), "开始对话").tap()
        wait("POST /api/v1/sessions sent") { !((mock("started")["started"] as? [[String: Any]]) ?? []).isEmpty }
        let body = ((mock("started")["started"] as? [[String: Any]]) ?? []).last ?? [:]
        XCTAssertEqual(body["launcher"] as? String, "codex")
        XCTAssertEqual(body["model"] as? String, "gpt-5.5-codex")
        XCTAssertNil(body["effort"], "a Codex start takes no effort")
    }

    /// A Codex launcher (docs/integrations/codex.md「Starting from the phone」): marked Codex in the picker; picked, Claude's modes go, the first
    /// message is required and goes with the start as typed (Codex makes its thread from it), and the chat that opens
    /// is that session.
    @MainActor func testNewCodexSessionStartsWithItsFirstMessage() throws {
        launch(["-tab", "dev", "-cwd.macbook", "/Users/me/code/blog"])
        see(app.buttons["新建会话"], "新建会话", timeout: 15).tap()
        see(machineCard("macbook"), "MacBook card").tap()
        see(app.buttons["newChat.launcher"], "launcher row").tap()
        see(app.navigationBars["启动器"], "launcher picker")
        see(text("Codex"), "the Codex mark on its launcher")
        shot("launcher-picker-codex")
        see(button("Codex"), "codex launcher").tap()
        see(text("Codex"), "launcher picked")
        XCTAssertFalse(app.staticTexts["权限与思考"].exists, "Claude Code's modes are not offered for Codex")

        let start = see(button("开始对话"), "开始对话")
        XCTAssertFalse(start.isEnabled, "no first message, no Codex session")
        let prompt = app.textViews["newChat.prompt"].exists ? app.textViews["newChat.prompt"] : app.textFields["newChat.prompt"]
        for _ in 0..<4 where !prompt.isHittable { app.swipeDown() }
        see(prompt, "the first message field").tap()
        let typed = #"把 "README" 里的 $HOME 和 `make` 改成中文说明"#
        prompt.typeText(typed)
        shot("new-chat-codex")
        XCTAssertTrue(start.isEnabled)
        start.tap()
        wait("POST /api/v1/sessions sent") { !((mock("started")["started"] as? [[String: Any]]) ?? []).isEmpty }
        let body = ((mock("started")["started"] as? [[String: Any]]) ?? []).last ?? [:]
        XCTAssertEqual(body["launcher"] as? String, "codex")
        XCTAssertEqual(body["prompt"] as? String, typed, "the first message goes with the start, as typed")
        XCTAssertNil(body["async"], "the start waits for the thread Codex makes")
        XCTAssertNil(body["permission_mode"])
        XCTAssertFalse(app.navigationBars["新对话"].waitForExistence(timeout: 3), "the sheet closes on start")
        seeChatNamed("README")
        see(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "README")).firstMatch, "the first message, already in the thread")
        shot("new-chat-codex-started")
    }

    /// A discovered Traex launcher is not Claude Code: a remembered Plan Only and Claude's effort / ultracode stay out
    /// of its form and start. Its nonempty first message goes with one synchronous start, never as a second message.
    @MainActor func testNewTraexSessionStartsWithItsFirstMessage() throws {
        let variants = [("light", "default", [String]()),
                        ("dark", "default", [String]()),
                        ("light", "largest", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]),
                        ("dark", "largest", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])]
        for (scheme, size, sizeArgs) in variants {
            _ = mock("reset") // reset clears STARTED, not SENT; each variant also baselines SENT below.
            launch(["-tab", "dev", "-cwd.server", "/home/me/code/persona", "-mode.server", "plan",
                    "-ui.scheme", scheme] + sizeArgs, machine: "server")
            see(app.buttons["新建会话"], "新建会话", timeout: 15).tap()
            see(app.buttons["newChat.launcher"], "launcher row").tap()
            see(app.navigationBars["启动器"], "launcher picker")
            let traexRow = see(button("TRAE CLI"), "the discovered Traex launcher")
            for _ in 0..<4 where !traexRow.isHittable { app.swipeUp() }
            XCTAssertTrue(traexRow.isHittable, "the discovered Traex row is on screen")
            traexRow.tap()
            wait("Traex launcher picked") { app.buttons["newChat.launcher"].label.contains("TRAE CLI") }
            let loading = app.descendants(matching: .any)["newChat.model.loading"].firstMatch
            XCTAssertTrue(loading.waitForNonExistence(timeout: 10), "Traex's model list finishes loading")
            XCTAssertFalse(app.staticTexts["权限与思考"].exists, "Claude Code's modes are not offered for Traex")
            XCTAssertFalse(button("权限模式").exists, "no Claude permission-mode picker for Traex")
            XCTAssertFalse(button("思考强度").exists, "no Claude thinking-effort picker for Traex")
            XCTAssertFalse(app.switches["newChat.ultracode"].exists, "no Claude ultracode switch for Traex")

            let prompt = app.textViews["newChat.prompt"].exists ? app.textViews["newChat.prompt"] : app.textFields["newChat.prompt"]
            for _ in 0..<6 where !prompt.isHittable { app.swipeUp() }
            see(prompt, "the first message field").tap()
            let typed = "Traex AC4 \(scheme) \(size)"
            prompt.typeText(typed)
            let tag = "\(scheme)-\(size)"
            shot("new-chat-traex-" + tag)
            let start = see(button("开始对话"), "开始对话")
            XCTAssertTrue(start.isEnabled)
            let sentBefore = ((mock("sent")["sent"] as? [[String: Any]]) ?? []).count
            start.tap()
            wait("POST /api/v1/sessions sent") { !((mock("started")["started"] as? [[String: Any]]) ?? []).isEmpty }
            let starts = (mock("started")["started"] as? [[String: Any]]) ?? []
            XCTAssertEqual(starts.count, 1, "exactly one Traex session start")
            let body = starts.first ?? [:]
            XCTAssertEqual(body["machine_id"] as? String, "server")
            XCTAssertEqual(body["launcher"] as? String, "traex")
            XCTAssertEqual(body["agent_type"] as? String, "traex")
            XCTAssertEqual(body["prompt"] as? String, typed, "the first message goes with the start, as typed")
            XCTAssertNil(body["async"], "the start is synchronous, with no async true")
            XCTAssertNil(body["permission_mode"], "remembered Claude Plan Only does not reach Traex")
            XCTAssertNil(body["effort"])
            XCTAssertNil(body["ultracode"])
            XCTAssertFalse(app.navigationBars["新对话"].waitForExistence(timeout: 3), "the sheet closes on start")
            seeChatNamed(typed)
            shot("new-chat-traex-started-" + tag)
            let newSends = (((mock("sent")["sent"] as? [[String: Any]]) ?? []).dropFirst(sentBefore))
            XCTAssertFalse(newSends.contains { ($0["text"] as? String) == typed },
                           "the prompt was not also sent as an ordinary message")
        }
    }
}
