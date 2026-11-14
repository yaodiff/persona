import XCTest

/// The Git tab's writes on the iPhone and iPad (#7013) against the mock hub's made-up repository: tick a file, commit
/// what is staged from the commit bar's sheet, Push asks first; a refused commit says why and keeps the message.
/// Run with `MOCK_FILES=1 MOCK_GIT_WRITE=1` and `PERSONA_GIT_HUB=<mock url>`; `PERSONA_SHOTS=<dir>` writes screenshots.
final class GitWriteUITests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_GIT_HUB"] else {
            throw XCTSkip("需要 MOCK_FILES=1 MOCK_GIT_WRITE=1 的 mock hub 和 PERSONA_GIT_HUB") // i18n-ignore: test skip reason
        }
        self.hub = hub
    }

    @discardableResult private func control(_ query: String) async throws -> [String: Any] {
        let (data, _) = try await URLSession.shared.data(from: URL(string: hub + "/mock/git-write?" + query)!)
        return try JSONSerialization.jsonObject(with: data) as! [String: Any]
    }

    @MainActor private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001",
                               "-openPane", "git", "-openAgentsFresh", "YES", "-openChatsFresh", "YES"]
        app.launchPinned()
        XCTAssertTrue(app.staticTexts["Provider.swift"].waitForExistence(timeout: 20), app.debugDescription)
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let tag = env["PERSONA_SHOTS_TAG"] ?? "shot"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/git-write-\(tag)-\(name).png"))
    }

    @MainActor private func toast(_ app: XCUIApplication, _ words: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
    }

    /// Taps e once it stops moving: on an iPad the form sheet slides up as the keyboard comes, and a tap aimed at
    /// where the button will be lands outside the sheet while it is still on its way.
    @MainActor private func tapSettled(_ e: XCUIElement) {
        var last = e.frame
        for _ in 0..<20 {
            RunLoop.current.run(until: Date().addingTimeInterval(0.25))
            let now = e.frame
            if now == last && e.isHittable { break }
            last = now
        }
        e.tap()
    }

    @MainActor func testStageCommitAndPushAfterAsking() async throws {
        try await control("reset=1")
        let app = launch()
        XCTAssertTrue(app.buttons["git.commit"].waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "1-tab")
        // Provider.swift is not staged: its tick box (a toggle to VoiceOver) stages it
        let tick = app.switches["暂存 Provider.swift"]
        XCTAssertTrue(tick.waitForExistence(timeout: 10), app.debugDescription)
        tick.tap()
        XCTAssertTrue(app.switches["取消暂存 Provider.swift"].waitForExistence(timeout: 10), app.debugDescription)
        let afterTick = try await control("")
        XCTAssertEqual(afterTick["files"] as? Int, 4)

        app.buttons["git.commit"].tap()
        let message = app.descendants(matching: .any)["git.commit.message"].firstMatch
        XCTAssertTrue(message.waitForExistence(timeout: 10), app.debugDescription)
        message.typeText("小组件：逐小时预报的数据和视图")
        try shot(app, "2-commit")
        tapSettled(app.buttons["git.commit.go"])
        XCTAssertTrue(toast(app, "已提交 eeeeeee").waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "3-committed")

        // a branch without an upstream: Push says it makes one, and only goes once the user says so
        let push = app.buttons["git-push"]
        XCTAssertTrue(push.waitForExistence(timeout: 10))
        push.tap()
        let ask = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "远端还没有 widget-hourly")).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "4-push-ask")
        let asked = try await control("")
        XCTAssertEqual(asked["ahead"] as? Int, 3, "nothing is pushed before the yes")
        // the dialog's button: by its identifier, or its word where the system drops identifiers in dialogs
        tapSettled(app.buttons.matching(NSPredicate(format: "identifier == 'git.push.confirm' OR (label == '推送' AND identifier != 'git-push')")).firstMatch)
        XCTAssertTrue(toast(app, "已推送 widget-hourly").waitForExistence(timeout: 10), app.debugDescription)
        let pushed = try await control("")
        XCTAssertEqual(pushed["ahead"] as? Int, 0)
        XCTAssertFalse(app.buttons["git-push"].waitForExistence(timeout: 2), "nothing left to push")
    }

    @MainActor func testARefusedCommitSaysWhyAndKeepsTheMessage() async throws {
        try await control("reset=1")
        let app = launch()
        try await control("fail=git_hook_failed")
        app.buttons["git.commit"].tap()
        let message = app.descendants(matching: .any)["git.commit.message"].firstMatch
        XCTAssertTrue(message.waitForExistence(timeout: 10))
        message.typeText("先试一版")
        tapSettled(app.buttons["git.commit.go"])
        let title = app.staticTexts["提交被拒绝了（通常是 pre-commit 之类的 hook）"]
        XCTAssertTrue(title.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Line Length Violation")).firstMatch.exists)
        try shot(app, "5-refused")
        app.alerts.buttons.firstMatch.tap()
        XCTAssertEqual(message.value as? String, "先试一版", "the message stays for another try")
    }

    /// A hub without `git_write` (run with MOCK_GIT_WRITE_FEATURE=0): the tab as before, no tick boxes, bar or Push.
    @MainActor func testWithoutTheHubsWritesTheTabIsAsBefore() async throws {
        guard ProcessInfo.processInfo.environment["PERSONA_GIT_NO_WRITE"] == "1" else { throw XCTSkip("PERSONA_GIT_NO_WRITE=1 with MOCK_GIT_WRITE_FEATURE=0") }
        try await control("reset=1")
        let app = launch()
        XCTAssertFalse(app.switches["暂存 Provider.swift"].exists)
        XCTAssertFalse(app.buttons["git.commit"].exists)
        XCTAssertFalse(app.buttons["git-push"].exists)
        try shot(app, "0-before")
    }

    // MARK: Branches and stashes (#7015)

    /// The dialog's button by its word, where the system drops identifiers in dialogs; never the row that opened it.
    @MainActor private func dialogButton(_ app: XCUIApplication, _ id: String, _ word: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == %@ OR (label == %@ AND NOT (identifier BEGINSWITH 'git.'))", id, word)).firstMatch
    }

    @MainActor func testSwitchAsksWithChangesAndARefusalOffersStashes() async throws {
        try await control("reset=1")
        let app = launch()
        let open = app.buttons["git.open.branches"]
        XCTAssertTrue(open.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "6-rows")
        open.tap()
        let main = app.buttons.matching(NSPredicate(format: "identifier == 'git.branch' AND label BEGINSWITH 'main'")).firstMatch
        XCTAssertTrue(main.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "7-branches")
        // four uncommitted changes go along: it asks first, and nothing switches before the yes
        main.tap()
        let ask = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "4 处未提交的改动会一起带过去")).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "8-switch-ask")
        let before = try await control("")
        XCTAssertEqual(before["branch"] as? String, "widget-hourly")
        // the files the other branch has differently: refused, listed, and Stashes is offered
        try await control("fail=git_conflict&files=WeatherWidget/Provider.swift,Sources/App/main.swift")
        tapSettled(dialogButton(app, "git.branch.confirm", "切换"))
        let refused = app.staticTexts["会覆盖未提交的改动：先提交或收起这些文件"]
        XCTAssertTrue(refused.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Sources/App/main.swift")).firstMatch.exists)
        try shot(app, "9-switch-refused")
        let after = try await control("")
        XCTAssertEqual(after["branch"] as? String, "widget-hourly", "nothing switched")
        app.alerts.buttons["贮藏"].tap()
        XCTAssertTrue(app.buttons["git.stash.push"].waitForExistence(timeout: 10), "Stashes opens in its place: " + app.debugDescription)
    }

    @MainActor func testCreateABranchAfterAsking() async throws {
        try await control("reset=1")
        let app = launch()
        app.buttons["git.open.branches"].tap()
        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 10), app.debugDescription)
        search.tap()
        search.typeText("fix/rain-color")
        let create = app.buttons["git.branch.create"]
        XCTAssertTrue(create.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "10-create")
        tapSettled(create)
        tapSettled(dialogButton(app, "git.branch.confirm", "新建"))
        XCTAssertTrue(toast(app, "已新建分支 fix/rain-color").waitForExistence(timeout: 10), app.debugDescription)
        let state = try await control("")
        XCTAssertEqual(state["branch"] as? String, "fix/rain-color")
    }

    @MainActor func testStashChangesThenDropOneAfterAsking() async throws {
        try await control("reset=1")
        let app = launch()
        app.buttons["git.open.stashes"].tap()
        let message = app.textFields["git.stash.message"]
        XCTAssertTrue(message.waitForExistence(timeout: 10), app.debugDescription)
        message.tap()
        message.typeText("先放一放")
        try shot(app, "11-stashes")
        tapSettled(app.buttons["git.stash.push"])
        XCTAssertTrue(toast(app, "已收起改动").waitForExistence(timeout: 10), app.debugDescription)
        let kept = app.buttons.matching(NSPredicate(format: "identifier == 'git.stash' AND label CONTAINS %@", "先放一放")).firstMatch
        XCTAssertTrue(kept.waitForExistence(timeout: 10), app.debugDescription)
        let state = try await control("")
        XCTAssertEqual(state["files"] as? Int, 0, "new files went too")
        // the older one: its menu, then Drop asks first
        let old = app.buttons.matching(NSPredicate(format: "identifier == 'git.stash' AND label CONTAINS %@", "alternate palette")).firstMatch
        old.tap()
        let drop = app.buttons["丢弃…"]
        XCTAssertTrue(drop.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "12-stash-menu")
        drop.tap()
        let ask = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "会被删除，在这里无法找回")).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "13-drop-ask")
        tapSettled(dialogButton(app, "git.stash.dropConfirm", "丢弃"))
        XCTAssertTrue(toast(app, "已丢弃").waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertFalse(old.waitForExistence(timeout: 2))
    }

    // MARK: The pull request (#7014)

    /// Scrolls the sheet's list up until e can be tapped.
    @MainActor private func reveal(_ app: XCUIApplication, _ e: XCUIElement) {
        for _ in 0..<4 where !(e.exists && e.isHittable) { app.swipeUp() }
    }

    @MainActor private func openPR(_ app: XCUIApplication) {
        let row = app.buttons["git.pr.open"]
        XCTAssertTrue(row.waitForExistence(timeout: 15), app.debugDescription)
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["git.pr.header"].firstMatch.waitForExistence(timeout: 10), app.debugDescription)
    }

    @MainActor func testThePullRequestRowAndMergeAfterAsking() async throws {
        try await control("reset=1&pr=ready")
        let app = launch()
        XCTAssertTrue(app.buttons["git.pr.open"].waitForExistence(timeout: 15), app.debugDescription)
        try shot(app, "14-pr-row")
        openPR(app)
        try shot(app, "15-pr-page")
        let merge = app.buttons["git.pr.merge"]
        reveal(app, merge)
        tapSettled(merge)
        let ask = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "现在就在 GitHub 上用")).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "16-merge-ask")
        tapSettled(dialogButton(app, "git.pr.confirm", "合并"))
        XCTAssertTrue(toast(app, "已合并 #128").waitForExistence(timeout: 10), app.debugDescription)
    }

    @MainActor func testReviewCommentsGoToTheAgentAndMergeWhenReadyAsks() async throws {
        try await control("reset=1&pr=review")
        let app = launch()
        openPR(app)
        let hand = app.buttons.matching(identifier: "git.pr.hand").firstMatch
        reveal(app, hand)
        XCTAssertTrue(hand.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "17-pr-review")
        hand.tap()
        let handed = app.buttons.matching(NSPredicate(format: "identifier == 'git.pr.hand' AND label CONTAINS %@", "已放进消息")).firstMatch
        XCTAssertTrue(handed.waitForExistence(timeout: 10), app.debugDescription)
        let when = app.buttons["git.pr.merge"]
        reveal(app, when)
        try shot(app, "18-pr-merge-section")
        tapSettled(when)
        let ask = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "检查和评审都通过后")).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 10), app.debugDescription)
        tapSettled(dialogButton(app, "git.pr.confirm", "通过后自动合并"))
        XCTAssertTrue(toast(app, "检查和评审通过后会自动合并").waitForExistence(timeout: 10), app.debugDescription)
    }

    @MainActor func testOpenAPullRequestAfterAsking() async throws {
        try await control("reset=1&pr=ready")
        try await control("pr=none")
        let app = launch()
        let create = app.buttons["git.pr.create"]
        XCTAssertTrue(create.waitForExistence(timeout: 15), app.debugDescription)
        try shot(app, "19-pr-none")
        create.tap()
        XCTAssertTrue(app.textFields["git.pr.title"].waitForExistence(timeout: 10), app.debugDescription)
        // the default branch by its name (origin/HEAD, the mock's main)
        let into = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@ OR value CONTAINS %@", "main（默认）", "main（默认）")).firstMatch
        XCTAssertTrue(into.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "20-pr-create")
        tapSettled(app.buttons["git.pr.open.go"])
        let ask = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "从 widget-hourly 合进 main 的拉取请求")).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "21-pr-create-ask")
        tapSettled(dialogButton(app, "git.pr.create.confirm", "创建拉取请求"))
        XCTAssertTrue(toast(app, "已创建拉取请求").waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.descendants(matching: .any)["git.pr.header"].firstMatch.waitForExistence(timeout: 10), "the page takes the sheet's place")
    }
}
