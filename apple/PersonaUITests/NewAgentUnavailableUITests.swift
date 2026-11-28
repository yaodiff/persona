import XCTest

/// New Agent's launcher list says why a Persona-managed provider is missing on a computer (#8109): one its own-service
/// rule leaves out where Claude Code has a sign-in of its own comes from the hub's `unavailable` and is listed after the
/// launchers with the hub's reason, not picked; How to Fix opens its page in Settings › Model providers. On a computer
/// without a sign-in it is an ordinary launcher. Against the mock hub, the MacBook signed in, the server not:
///   PORT=<port> MOCK_OWN_SERVICE_SKIP=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_OWN_SERVICE_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/NewAgentUnavailableUITests
/// `TEST_RUNNER_PERSONA_SHOTS=<dir>` saves each state in light and dark; `TEST_RUNNER_PERSONA_SHOTS_BEFORE=1` takes the
/// shots only (a build without the feature: nothing is asserted).
final class NewAgentUnavailableUITests: XCTestCase {
    private var app: XCUIApplication!
    private var look = ""
    private var before: Bool { ProcessInfo.processInfo.environment["PERSONA_SHOTS_BEFORE"] != nil }
    private let reason = "MacBook already has its own Claude Code service, and DeepSeek skips such computers"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_OWN_SERVICE_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_OWN_SERVICE_SKIP=1 (PERSONA_OWN_SERVICE_HUB)")
        }
        let list = try Data(contentsOf: URL(string: hub + "/api/v1/machines/macbook/launchers")!)
        let body = try JSONSerialization.jsonObject(with: list) as? [String: Any]
        guard body?["unavailable"] != nil else { throw XCTSkip("needs MOCK_OWN_SERVICE_SKIP=1") }
    }

    private var hub: String { ProcessInfo.processInfo.environment["PERSONA_OWN_SERVICE_HUB"]! }

    /// New Agent on `machine`, in English, its launcher list open once the computer's launchers are in; `largest`: at the
    /// largest accessibility text size.
    @MainActor private func openList(_ machine: String, largest: Bool = false) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-showNewChat", "YES", "-newChat.machine", machine,
                               "-openLauncherPicker", "YES", "-ui.scheme", look, "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
            + (largest ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] : [])
        app.launchPinned()
        // The system's own prompts: notifications once per install, and the app icon changed with the scheme.
        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        if board.alerts.firstMatch.waitForExistence(timeout: 4) {
            for _ in 0..<2 {
                for label in ["Allow", "允许", "OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own buttons
                    board.alerts.buttons[label].tap()
                    sleep(1)
                }
            }
        }
        // The first launch after an install pairs first and can come up on Agents without the sheet: once more.
        if !row("Claude Code").waitForExistence(timeout: 30) {
            app.terminate()
            app.launchPinned()
        }
        XCTAssertTrue(row("Claude Code").waitForExistence(timeout: 30), "no launcher list on \(machine):\n\(app.debugDescription.prefix(4000))")
        sleep(1) // the list settles
    }

    /// A launcher's row, by the title it shows (a provider's display name, #8261), not a row listed off.
    private func row(_ title: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@ AND NOT (identifier BEGINSWITH %@)", title,
                                         "launcherPicker.unavailable.")).firstMatch
    }

    private var skipped: XCUIElement {
        app.descendants(matching: .any).matching(identifier: "launcherPicker.unavailable.deepseek").firstMatch
    }

    private func text(containing s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    @MainActor func testSkippedProviderSaysWhyAndHowToFix() throws {
        for scheme in ["light", "dark"] {
            look = scheme

            // The MacBook: DeepSeek is left out, and the list says why after the launchers.
            openList("macbook")
            if !before {
                XCTAssertTrue(skipped.waitForExistence(timeout: 10), "DeepSeek is not listed off:\n\(app.debugDescription.prefix(4000))")
                XCTAssertTrue(app.staticTexts["Unavailable here"].exists, "no Unavailable here section")
                XCTAssertTrue(text(containing: reason).exists, "the hub's reason is not shown")
                XCTAssertTrue(skipped.label.contains("How to Fix"), "no How to Fix on it: \(skipped.label)")
                XCTAssertFalse(row("DeepSeek").exists, "DeepSeek is offered as a launcher on the MacBook")
            }
            shot("macbook-list")
            if !before {
                skipped.tap()
                // its page in Settings › Model providers: a provider Persona manages, with its Computers
                XCTAssertTrue(text(containing: "Managed by Persona").waitForExistence(timeout: 10),
                              "How to Fix did not open the provider's page:\n\(app.debugDescription.prefix(4000))")
                XCTAssertTrue(app.descendants(matching: .any)["launcher.scope"].exists, "the provider's page has no Computers row")
                shot("macbook-provider")
            }
            app.terminate()

            // The server, without a sign-in of its own: DeepSeek is a launcher like the others, nothing is listed off.
            openList("server")
            if !before {
                XCTAssertTrue(row("DeepSeek").waitForExistence(timeout: 10), "DeepSeek is not offered on the server")
                XCTAssertFalse(skipped.exists, "DeepSeek is listed off on the server")
                XCTAssertFalse(app.staticTexts["Unavailable here"].exists, "an Unavailable here section on the server")
            }
            shot("server-list")
            app.terminate()
        }
    }

    /// The largest accessibility text size: the row wraps, and the list scrolls to it.
    @MainActor func testLargestTextSize() throws {
        for scheme in ["light", "dark"] {
            look = scheme
            openList("macbook", largest: true)
            for _ in 0..<6 where !(skipped.exists && skipped.isHittable) { app.swipeUp() }
            if !before {
                XCTAssertTrue(skipped.exists && skipped.isHittable, "DeepSeek is not listed off at the largest text size")
                XCTAssertTrue(text(containing: reason).exists, "the hub's reason is not shown at the largest text size")
            } else {
                app.swipeUp()
            }
            shot("macbook-list-ax")
            app.terminate()
        }
    }

    @MainActor private func shot(_ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = "new-agent-unavailable \(name) \(look)"
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let stage = before ? "before" : "after"
        try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "new-agent-unavailable-\(name)-\(look)-\(stage).png"))
    }
}
