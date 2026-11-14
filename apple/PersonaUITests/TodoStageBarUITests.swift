import XCTest

/// The to-do stage bar at the default and the largest system text sizes (#5892), against scripts/mock/mock_hub.py with
/// MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB): on the chat's task card (the widget task, on Develop) and on
/// the detail page of the to-do 「天气 App 发 1.3 版」 (on Develop), in zh-Hans, English and Japanese, light and dark. At the
/// default size the bar is one element; at the largest it is that, or the six dots with a line under them when the
/// names do not fit (by width), and it lies within the window. `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots
/// (`stagebar-<place>-<language>-<size>-<scheme>.png`); `TEST_RUNNER_PERSONA_PHASE=before` takes them on an older build,
/// checking nothing.
final class TodoStageBarUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var before: Bool { env["PERSONA_PHASE"] == "before" }

    @MainActor func testStageBarAtEveryTextSize() throws {
        guard let hub = env["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB)")
        }
        continueAfterFailure = false
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let languages: [(String, [String])] = [
            ("zh-Hans", []),
            ("en", ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]),
            ("ja", ["-AppleLanguages", "(ja)", "-AppleLocale", "ja_JP"]),
        ]
        let places: [(String, [String])] = [
            ("chat", ["-tab", "assistant", "-dev.openChat", "", "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]),
            ("todo", ["-tab", "assistant", "-openAssistantPage", "todos", "-openTodoTitled", "天气 App 发 1.3 版"]), // i18n-ignore: the mock's to-do
        ]
        // The look outermost: the app icon follows it, and the system's alert about that comes only when it changes.
        var first = true, lastScheme = ""
        for scheme in ["light", "dark"] {
            for (language, languageArgs) in languages {
                for size in ["default", "largest"] {
                    for (place, placeArgs) in places {
                        let name = "\(place)-\(language)-\(size)-\(scheme)"
                        let largest = size == "largest" ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] : []
                        let app = XCUIApplication()
                        app.launchArguments = ["-pairURL", url, "-ui.scheme", scheme] + placeArgs + languageArgs + largest
                        app.launchPinned()
                        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
                        // The notification prompt comes once per install: waited for only at the first launch.
                        for label in ["允许", "Allow"] where first ? board.buttons[label].waitForExistence(timeout: 3) : board.buttons[label].exists {
                            board.buttons[label].tap()
                        }
                        first = false

                        let bar = app.descendants(matching: .any)["todo.stage"].firstMatch
                        let window = app.windows.firstMatch.frame
                        // At the largest size the to-do detail's stage row is far below the screen, and the list makes
                        // its rows only as they come near: scrolled down to until it exists.
                        var found = bar.waitForExistence(timeout: 10)
                        for _ in 0..<12 where !found {
                            scroll(app, by: window.height * 0.35)
                            found = bar.waitForExistence(timeout: 2)
                        }
                        XCTAssertTrue(found, "\(name): no stage bar:\n\(app.debugDescription)")
                        sleep(2) // the page settles
                        // The bar into the middle of the screen, clear of the navigation bar and the buttons at the bottom.
                        let middle = window.insetBy(dx: 0, dy: window.height * 0.15)
                        for _ in 0..<4 where !middle.contains(bar.frame) {
                            scroll(app, by: max(-window.height * 0.35, min(window.height * 0.35, bar.frame.midY - window.midY)))
                            sleep(1)
                        }
                        if !before {
                            check(app, bar, window: window, largest: size == "largest", language: language, name: name)
                        }
                        if let dir = env["PERSONA_SHOTS"] {
                            // The app icon follows the color scheme, and the system says so in an alert over the page: closed first.
                            if scheme != lastScheme ? board.alerts.firstMatch.waitForExistence(timeout: 3) : board.alerts.firstMatch.exists {
                                for label in ["OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own button
                                    board.alerts.buttons[label].tap(); sleep(1); break
                                }
                            }
                            try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/stagebar-\(name).png"))
                        }
                        lastScheme = scheme
                        app.terminate()
                    }
                }
            }
        }
    }

    /// Scrolls the page by `dy` points (positive: the content moves up), dragged slowly and held at the end so it stops
    /// there instead of flinging past.
    @MainActor private func scroll(_ app: XCUIApplication, by dy: CGFloat) {
        let center = app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        center.press(forDuration: 0.05, thenDragTo: center.withOffset(CGVector(dx: 0, dy: -dy)), withVelocity: .slow,
                     thenHoldForDuration: 0.3)
    }

    /// At the default size: one element that says the stage, no dot of its own. At the largest, by the measured width:
    /// that same one element while the six names fit their sixths, else the six dots, each labeled with its name, place
    /// and state (in zh-Hans word for word). Either way the bar is within the window.
    @MainActor private func check(_ app: XCUIApplication, _ bar: XCUIElement, window: CGRect, largest: Bool, language: String, name: String) {
        let dots = ["plan", "develop", "review", "merge", "deploy", "verify"].map { app.descendants(matching: .any)["todo.stage.\($0)"].firstMatch }
        XCTAssertGreaterThanOrEqual(bar.frame.minX, window.minX - 0.5, "\(name): the bar starts left of the screen")
        XCTAssertLessThanOrEqual(bar.frame.maxX, window.maxX + 0.5, "\(name): the bar ends right of the screen")
        guard dots[1].exists else {
            if language == "zh-Hans" { XCTAssertEqual(bar.label, "阶段：开发（第 2 步，共 6 步）", name) } // i18n-ignore: the app's words
            return
        }
        XCTAssertTrue(largest, "\(name): the names do not fit at the default size")
        for dot in dots {
            XCTAssertTrue(dot.exists, "\(name): a dot without its own element:\n\(app.debugDescription)")
            XCTAssertTrue(window.contains(dot.frame.insetBy(dx: 0.5, dy: 0.5)), "\(name): \(dot.label) off the screen")
        }
        XCTAssertTrue(dots[1].label.contains("2"), "\(name): \(dots[1].label)")
        if language == "zh-Hans" { // i18n-ignore: the app's words
            XCTAssertEqual(dots[0].label, "方案，第 1 步，共 6 步，已完成", name) // i18n-ignore
            XCTAssertEqual(dots[1].label, "开发，第 2 步，共 6 步，当前步骤", name) // i18n-ignore
            XCTAssertEqual(dots[5].label, "已验证，第 6 步，共 6 步，还没到", name) // i18n-ignore
        }
    }
}
