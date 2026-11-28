import XCTest

/// Resume in Terminal (#5521): the chat's ⋯ of an Agent that isn't running brings it back in a terminal on its computer,
/// offered only where a terminal engine can host it. Against the mock's MOCK_RESUME_TERMINAL sessions: on the MacBook a
/// Claude Code from a terminal closed since and a Codex thread nothing has open (both stopped), a claude still running in
/// a terminal of its own; on the Mac mini, which enables no terminal engine, a Claude Code that isn't running.
///   PORT=<port> MOCK_RESUME_TERMINAL=1 MOCK_TERMINAL_STREAM=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_RESUME_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/ResumeInTerminalTests
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` saves each state in light and dark; `TEST_RUNNER_PERSONA_SHOTS_BEFORE=1` only takes the
/// shots of `testShots` (a build without the feature: nothing is asserted).
final class ResumeInTerminalTests: XCTestCase {
    private let stopped = "macbook/5e000000-0000-4000-8000-000000005521"
    private let codex = "macbook/019a5521-0000-7000-8000-000000000001"
    private let live = "macbook/5e000000-0000-4000-8000-000000005522"
    private let noEngine = "mini/5e000000-0000-4000-8000-000000005523"
    private let item = "在终端里恢复"
    private var app: XCUIApplication!
    private var look = ""
    private var before: Bool { ProcessInfo.processInfo.environment["PERSONA_SHOTS_BEFORE"] != nil }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_RESUME_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_RESUME_TERMINAL=1 (PERSONA_RESUME_HUB)")
        }
    }

    private var hub: String { ProcessInfo.processInfo.environment["PERSONA_RESUME_HUB"]! }

    private func mock(_ path: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + path)!)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    /// Each launch starts from the mock's sessions as they were (a resumed one is stopped again).
    private func resetMock() throws { _ = try mock("/mock/reset") }

    /// The mock's starts so far that resumed a session: their ids.
    private func resumed() throws -> [String] {
        ((try mock("/mock/started"))["started"] as? [[String: Any]] ?? []).compactMap { $0["resume_session_id"] as? String }
    }

    /// The chat of `key`, its header held open (⋯ is in it); `extra` launch arguments after.
    @MainActor private func open(_ key: String, _ extra: [String] = []) throws {
        try resetMock()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", key, "-openChatsFresh", "YES", "-holdChromeOpen", "YES",
                               "-terminalTipsSeen", "YES"] + extra
        app.launchPinned()
        let more = app.buttons.matching(NSPredicate(format: "label == %@", "更多")).firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 25), "no ⋯ on \(key):\n\(app.debugDescription.prefix(4000))")
        sleep(2) // the conversation and the row's line settle
    }

    @MainActor private func openMenu() {
        let more = app.buttons.matching(NSPredicate(format: "label == %@", "更多")).firstMatch
        if !more.isHittable {
            let compact = app.buttons["chat.compactTitle"].firstMatch
            if compact.exists && compact.isHittable { compact.tap(); sleep(1) }
        }
        // At the largest text size a tap now and then lands while the header still settles, and only stops it: tap again.
        // Any of the top level's first items says it opened (Rename Agent…, Pin; a session still running elsewhere
        // has Interrupt first).
        let opened = app.buttons.matching(NSPredicate(
            format: "label BEGINSWITH %@ OR label IN %@", "重命名", ["置顶", "取消置顶", "中断"])).firstMatch
        for _ in 0..<3 where !opened.exists {
            more.tap()
            _ = opened.waitForExistence(timeout: 5)
        }
        XCTAssertTrue(opened.exists, "⋯ did not open:\n\(app.debugDescription.prefix(4000))")
        sleep(1)
        app.openAgentMenuMore() // Resume in Terminal is More's (#13396)
        sleep(1)
    }

    private var resumeItem: XCUIElement { app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", item)).firstMatch }

    @MainActor private func closeMenu() {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.08)).tap()
        sleep(1)
    }

    /// Every case's chat and its ⋯, in light and dark.
    @MainActor func testShots() throws {
        let was = XCUIDevice.shared.appearance
        defer { XCUIDevice.shared.appearance = was }
        for mode in [XCUIDevice.Appearance.light, .dark] {
            XCUIDevice.shared.appearance = mode
            look = mode == .dark ? "dark" : "light"

            try open(stopped)
            shot("stopped")
            if !before {
                XCTAssertTrue(app.buttons["chat.stopped.resume"].exists, "no Resume on the stopped line")
                XCTAssertTrue(app.buttons["chat.stopped.terminal"].exists, "no Terminal on the stopped line")
            }
            openMenu()
            shot("stopped-menu")
            if !before { XCTAssertTrue(resumeItem.exists && resumeItem.isEnabled, "no Resume in Terminal for a stopped Agent") }
            closeMenu()

            try open(codex)
            shot("codex")
            if !before { XCTAssertTrue(app.buttons["chat.stopped.resume"].exists, "a stopped Codex thread has no stopped line") }
            openMenu()
            shot("codex-menu")
            if !before { XCTAssertTrue(resumeItem.exists && resumeItem.isEnabled, "no Resume in Terminal for a stopped Codex thread") }
            closeMenu()

            try open(live)
            openMenu()
            shot("live-menu")
            if !before { XCTAssertFalse(resumeItem.exists, "Resume in Terminal offered while it runs in a terminal of its own") }
            closeMenu()

            try open(noEngine)
            openMenu()
            shot("no-engine-menu")
            if !before {
                XCTAssertTrue(resumeItem.exists)
                XCTAssertFalse(resumeItem.isEnabled)
                let reason = "Mac mini 没有启用终端引擎"
                XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", reason)).firstMatch.exists
                              || resumeItem.label.contains(reason), "the menu names the computer without a terminal engine")
            }
            closeMenu()
            app.terminate()
        }
    }

    /// The largest accessibility text size, light: the stopped line (its two buttons one above the other, neither cut
    /// short) and the disabled item with its reason.
    @MainActor func testShotsLargestText() throws {
        let was = XCUIDevice.shared.appearance
        defer { XCUIDevice.shared.appearance = was }
        XCUIDevice.shared.appearance = .light
        look = "ax"
        let ax = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        try open(stopped, ax)
        shot("stopped")
        if !before {
            for id in ["chat.stopped.terminal", "chat.stopped.resume"] {
                let b = app.buttons[id]
                XCTAssertTrue(b.exists && b.frame.maxX <= app.windows.firstMatch.frame.maxX + 1, "\(id) is off the screen at the largest size")
            }
        }
        openMenu()
        shot("stopped-menu")
        closeMenu()
        try open(noEngine, ax)
        openMenu()
        shot("no-engine-menu")
        closeMenu()
        app.terminate()
    }

    /// ⋯ › Resume in Terminal: the terminal comes up over the page, the hub gets the resume, and the Agent's screen shows.
    @MainActor func testResumeFromTheMenu() throws {
        if before { throw XCTSkip("shots only") }
        try open(stopped)
        openMenu()
        resumeItem.tap()
        let screen = app.descendants(matching: .any)["terminal-screen"].firstMatch
        XCTAssertTrue(screen.waitForExistence(timeout: 20), "the terminal did not come up live:\n\(app.debugDescription.prefix(4000))")
        XCTAssertTrue(try resumed().contains(String(stopped.split(separator: "/")[1])), "the hub got no resume")
        sleep(2)
        shot("resumed")
        XCTAssertTrue(app.buttons["terminal-close"].exists, "no Close over the resumed terminal")
    }

    /// The stopped line's Resume Session does the same.
    @MainActor func testResumeFromTheStoppedLine() throws {
        if before { throw XCTSkip("shots only") }
        try open(codex)
        app.buttons["chat.stopped.resume"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["terminal-screen"].firstMatch.waitForExistence(timeout: 20),
                      "the terminal did not come up live:\n\(app.debugDescription.prefix(4000))")
        XCTAssertTrue(try resumed().contains(String(codex.split(separator: "/")[1])), "the hub got no resume")
    }

    @MainActor private func shot(_ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = "resume-terminal \(name) \(look)"
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "resume-terminal-\(name)\(look.isEmpty ? "" : "-" + look).png"))
    }
}
