import XCTest

/// Open Terminal Here's terminal when its takeover goes unanswered (#6327), against scripts/mock/mock_hub.py
/// (PERSONA_MOCK_HUB) with MOCK_FILES=1 MOCK_FILE_OPS=1 MOCK_TERMINAL_PANES=1 MOCK_TERMINAL_STREAM=1 MOCK_TERMINAL_CONTROL=1.
/// The takeover gets no answer (it went with the node's old connection, `/mock/terminal_node?state=stalled`): the
/// terminal says it is waiting, then why, with Retry. The computer goes offline (`gone`) and comes back (`back`), and
/// the terminal takes itself over again. Each step is attached as a screenshot (PERSONA_SHOTS: also written there).
final class TerminalTakeoverHangTests: XCTestCase {
    private var hub = ""
    private let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB) with MOCK_FILES=1 MOCK_FILE_OPS=1 MOCK_TERMINAL_PANES=1 MOCK_TERMINAL_STREAM=1 MOCK_TERMINAL_CONTROL=1")
        }
        self.hub = hub
    }

    override func tearDown() {
        node("back")
        super.tearDown()
    }

    @MainActor func testUnansweredTakeoverLight() throws { try unanswered("light") }
    @MainActor func testUnansweredTakeoverDark() throws { try unanswered("dark") }

    @MainActor private func unanswered(_ scheme: String) throws {
        node("back")
        node("stalled")
        let app = launch(scheme, ["-openPane", "files"])
        element(app, "files.row.README.md", "the session's folder", timeout: 30)
        element(app, "files.more", "⋯").tap()
        button(app, "在这里打开终端", "⋯ menu").tap()
        let size = element(app, "terminal-size", "the size chip")
        XCTAssertTrue(size.label.contains("接管中"), "not taking it over: \(size.label)")

        // Past four seconds without a screen the link says so, with Retry (before: a bare spinner, for good).
        text(app, "还没有拿到终端", "the wait unexplained", timeout: 8)
        shot(app, "waiting-\(scheme)")
        // Past ten: why, and the next try.
        text(app, "MacBook 没有回应", "no reason after ten seconds", timeout: 10)
        XCTAssertTrue(app.buttons["重试"].exists, "no Retry")
        shot(app, "not-answering-\(scheme)")

        // The node goes for good (a deploy's restart): the computer is offline, in the app's words.
        node("gone")
        text(app, "MacBook 不在线", "the computer offline unexplained", timeout: 8)
        // Between tries the size chip still says it takes the terminal over (not Wrap, a mirror's control).
        XCTAssertTrue(size.waitForExistence(timeout: 2) && size.label.contains("接管中"), "the chip gave up: \(app.debugDescription)")
        shot(app, "offline-\(scheme)")

        // It's back: the terminal subscribes and takes over again by itself, at the phone's size.
        node("back")
        let deadline = Date.now.addingTimeInterval(8) // the chip is gone while it only watches: poll, never read a missing one
        while Date.now < deadline, !(size.exists && size.label.contains("×")) { Thread.sleep(forTimeInterval: 0.25) }
        XCTAssertTrue(size.exists && size.label.contains("×"), "not driving after the computer came back:\n\(app.debugDescription)")
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "不在线")).firstMatch.exists, "the reason stayed")
        shot(app, "recovered-\(scheme)")
        app.terminate()
    }

    // MARK: helpers

    private func node(_ state: String) {
        _ = try? Data(contentsOf: URL(string: hub + "/mock/terminal_node?state=" + state)!)
    }

    @MainActor private func launch(_ scheme: String, _ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openSession", widget, "-openAgentsFresh", "YES", "-openChatsFresh", "YES",
                               "-terminalTipsSeen", "YES", "-terminalSize", "phone", "-ui.scheme", scheme] + extra
        app.launchPinned()
        dismissSystemAlerts(wait: 3)
        return app
    }

    @MainActor private func dismissSystemAlerts(wait: TimeInterval = 1) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        var deadline = Date.now.addingTimeInterval(wait)
        while Date.now < deadline {
            let alert = springboard.alerts.firstMatch
            if alert.waitForExistence(timeout: 0.5) {
                for label in ["允许", "Allow", "好", "OK"] where alert.buttons[label].exists { alert.buttons[label].tap(); break }
                deadline = Date.now.addingTimeInterval(1.5)
            }
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        Thread.sleep(forTimeInterval: 0.6)
        let png = app.screenshot()
        let a = XCTAttachment(screenshot: png)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? png.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("takeover-\(name).png"))
        }
    }

    @MainActor @discardableResult
    private func element(_ app: XCUIApplication, _ id: String, _ why: String, timeout: TimeInterval = 20) -> XCUIElement {
        let e = app.descendants(matching: .any)[id].firstMatch
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "\(why): no \(id):\n\(app.debugDescription)")
        return e
    }

    @MainActor private func button(_ app: XCUIApplication, _ label: String, _ why: String, timeout: TimeInterval = 8) -> XCUIElement {
        let b = app.buttons[label].firstMatch
        XCTAssertTrue(b.waitForExistence(timeout: timeout), "\(why): no button \(label):\n\(app.debugDescription)")
        return b
    }

    /// Waits for a text on screen that contains part.
    @MainActor private func text(_ app: XCUIApplication, _ part: String, _ why: String, timeout: TimeInterval) {
        let e = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", part)).firstMatch
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "\(why): no text with \(part):\n\(app.debugDescription)")
    }
}
