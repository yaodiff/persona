import XCTest

final class LarkMessageDisplayFlowTests: XCTestCase {
    @MainActor private func launch(_ hub: String, settings: Bool) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", settings ? "settings" : "mail", "-demo.on", "NO",                                "-openConnectorFeishu", settings ? "YES" : "NO", "-inbox.source", "", "-inbox.kind", "mention", "-openInboxFirst", "NO"]
        app.launchPinned()
        return app
    }
    @MainActor private func expandSource(_ app: XCUIApplication) {
        let source = app.buttons["inbox.detail.source"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 15), "the original source control did not appear")
        source.tapCenter()
    }
    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let attachment = XCTAttachment(screenshot: app.screenshot()); attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
        if let path = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: path).appendingPathComponent(name + ".png"))
        }
    }
    @MainActor private func configure(_ hub: String, enabled: Bool) throws {
        if ProcessInfo.processInfo.environment["PERSONA_MESSAGING"] == "1" { return try configureSource(hub, enabled: enabled) }
        let app = launch(hub, settings: true)
        let entry = app.buttons["风灵月影"].firstMatch
        XCTAssertTrue(entry.waitForExistence(timeout: 20)); entry.tapCenter()
        let recall = app.switches["lark.display.anti_recall"].firstMatch
        XCTAssertTrue(recall.waitForExistence(timeout: 10))
        for id in ["anti_recall", "show_edit_history"] {
            let toggle = app.switches["lark.display." + id].firstMatch
            XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "enabled == true AND hittable == true"), object: toggle)], timeout: 10), .completed)
            if (toggle.value as? String == "1") != enabled {
                // The outer switch spans the Form row. Its child is the actual control;
                // row-relative coordinates miss the track on some device widths.
                let control = toggle.switches.firstMatch
                XCTAssertTrue(control.waitForExistence(timeout: 5))
                XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "enabled == true AND hittable == true"), object: control)], timeout: 10), .completed)
                control.tapCenter()
            }
            let check = NSPredicate(format: "value == %@ AND enabled == true", enabled ? "1" : "0")
            XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: check, object: toggle)], timeout: 12), .completed)
        }
        try shot(app, enabled ? "trainer-settings-on" : "trainer-settings-off")
        app.terminate()
    }
    /// On a messaging source the settings are its own page (extension_page `display`, #2138): set on the mock as another
    /// device would, then the page shows them.
    @MainActor private func configureSource(_ hub: String, enabled: Bool) throws {
        let url = URL(string: hub + "/api/v1/lark/message-display")!
        let now = try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any] ?? [:]
        for key in ["anti_recall", "show_edit_history"] where (now[key] as? Bool) != enabled {
            var req = URLRequest(url: url)
            req.httpMethod = "POST"
            req.httpBody = try JSONSerialization.data(withJSONObject: ["account_id": now["account_id"] ?? "", "key": key, "value": enabled, "expected": !enabled])
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            let done = expectation(description: key)
            URLSession.shared.dataTask(with: req) { _, _, _ in done.fulfill() }.resume()
            wait(for: [done], timeout: 10)
        }
        let app = launch(hub, settings: true)
        let entry = app.buttons["风灵月影"].firstMatch
        XCTAssertTrue(entry.waitForExistence(timeout: 20)); entry.tapCenter()
        XCTAssertTrue(app.staticTexts["防撤回"].waitForExistence(timeout: 15), "the source's own settings page")
        XCTAssertTrue(app.staticTexts[enabled ? "开" : "关"].firstMatch.waitForExistence(timeout: 10))
        try shot(app, enabled ? "trainer-settings-on" : "trainer-settings-off")
        app.terminate()
    }

    private func change(_ hub: String, key: String, value: Bool) async throws {
        var req = URLRequest(url: URL(string: hub + "/api/v1/lark/message-display")!)
        req.httpMethod = "POST"
        req.httpBody = try JSONSerialization.data(withJSONObject: ["account_id": "trainer-account", "key": key, "value": value, "expected": !value])
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let (_, response) = try await URLSession.shared.data(for: req)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
    }
    private func recall(_ hub: String, enabled: Bool) async throws {
        var req = URLRequest(url: URL(string: hub + "/mock/trainer/recall")!)
        req.httpMethod = "POST"
        req.httpBody = try JSONSerialization.data(withJSONObject: ["recalled": enabled])
        let (_, response) = try await URLSession.shared.data(for: req)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
    }
    @MainActor func testRetainedRecallHistoryAndClosedSettings() async throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs Trainer mock Hub") }
        continueAfterFailure = false
        try configure(hub, enabled: true)
        var app = launch(hub, settings: false)
        var chat = app.staticTexts["设计评审"].firstMatch
        XCTAssertTrue(chat.waitForExistence(timeout: 20)); chat.tapCenter()
        expandSource(app)
        XCTAssertTrue(app.staticTexts["已留存的评审时间：周五下午"].firstMatch.waitForExistence(timeout: 15))
        XCTAssertFalse(app.staticTexts["不能展示的占位原文"].exists)
        XCTAssertFalse(app.staticTexts["不得展示的系统撤回原文"].exists)
        XCTAssertTrue(app.staticTexts["系统成员 撤回了一条消息"].exists)
        XCTAssertTrue(app.staticTexts["消息已撤回"].exists)
        XCTAssertFalse(app.staticTexts["不得展示的匿名系统原文"].exists)
        XCTAssertTrue(app.staticTexts["已撤回"].firstMatch.exists)
        let accountNotice = app.staticTexts["飞书账号已切换，草稿按账号分别保留"].firstMatch
        // The shell retains its last toast while folded. Wait until it no longer intercepts
        // the message's long press, rather than requiring its accessibility node to disappear.
        XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "hittable == false"), object: accountNotice)], timeout: 10), .completed)
        try shot(app, "trainer-chat-on")
        app.staticTexts["已留存的评审时间：周五下午"].firstMatch.press(forDuration: 1)
        XCTAssertTrue(app.buttons["拷贝"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["回复"].exists)
        XCTAssertFalse(app.buttons["表情回复"].exists)
        app.buttons["拷贝"].tapCenter()
        let history = app.buttons["查看编辑历史"].firstMatch
        XCTAssertTrue(history.waitForExistence(timeout: 5)); history.tapCenter()
        XCTAssertTrue(app.staticTexts["原定会议室 A"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["稍后确认"].exists)
        XCTAssertFalse(app.staticTexts["不得缓存的旧加密正文"].exists)
        try shot(app, "trainer-edit-history")
        try await change(hub, key: "show_edit_history", value: false)
        XCTAssertTrue(app.staticTexts["编辑历史已隐藏"].waitForExistence(timeout: 10), "an open history sheet must react to settings changes")
        try await change(hub, key: "show_edit_history", value: true)
        XCTAssertTrue(app.staticTexts["原定会议室 A"].waitForExistence(timeout: 10))
        app.buttons["完成"].firstMatch.tapCenter()
        try await change(hub, key: "anti_recall", value: false)
        XCTAssertTrue(app.buttons["李雷：消息已撤回"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["已留存的评审时间：周五下午"].exists)
        try await change(hub, key: "anti_recall", value: true)
        XCTAssertTrue(app.staticTexts["已留存的评审时间：周五下午"].firstMatch.waitForExistence(timeout: 10))
        app.terminate()
        try configure(hub, enabled: false)
        app = launch(hub, settings: false)
        chat = app.staticTexts["设计评审"].firstMatch
        XCTAssertTrue(chat.waitForExistence(timeout: 20)); chat.tapCenter()
        expandSource(app)
        XCTAssertTrue(app.staticTexts["评审地点改为线上"].waitForExistence(timeout: 15))
        XCTAssertFalse(app.staticTexts["已留存的评审时间：周五下午"].exists)
        XCTAssertFalse(app.buttons["查看编辑历史"].exists)
        XCTAssertFalse(app.staticTexts["不得展示的系统撤回原文"].exists)
        XCTAssertTrue(app.buttons["李雷：消息已撤回"].firstMatch.exists)
        XCTAssertTrue(app.staticTexts["消息已撤回"].exists)
        XCTAssertFalse(app.staticTexts["不得展示的匿名系统原文"].exists)
        try shot(app, "trainer-chat-off")
        try await recall(hub, enabled: false)
        XCTAssertTrue(app.staticTexts["已留存的评审时间：周五下午"].firstMatch.waitForExistence(timeout: 10))
        try await recall(hub, enabled: true)
        let retained = app.staticTexts["已留存的评审时间：周五下午"]
        XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: retained)], timeout: 10), .completed)
        app.terminate()
    }
}
