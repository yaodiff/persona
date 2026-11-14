import XCTest

/// Settings › This Device › Live Screen (#6398, #6396, #7775) against scripts/mock/mock_hub.py
/// (TEST_RUNNER_PERSONA_SCREEN_HUB, MOCK_SCREEN_VIDEO=1): the route switch, first, above the quality on mobile data and
/// Scroll Speed (which computers get the row: PersonaTests' LiveScreenRowTests); each keeps what was set, and the page
/// reads in light and dark, at the default size and at xxxLarge (where interface text stops growing), in English and
/// Chinese. Screenshots are attachments (PERSONA_SHOTS also writes them out).
final class LiveScreenSettingsTests: XCTestCase {
    private var hub: String? { ProcessInfo.processInfo.environment["PERSONA_SCREEN_HUB"] }

    @MainActor private func launch(_ args: [String], fresh: Bool = true) throws -> XCUIApplication {
        guard let hub else { throw XCTSkip("needs the mock: TEST_RUNNER_PERSONA_SCREEN_HUB") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=" + UITestToken.device
        app.launchArguments = ["-pairURL", pair, "-tab", "settings"] + args
        app.launchPinned()
        dismissIconNotice(app)
        return app
    }

    /// The row in This Device opens the page; the switch is on; turned off, it stays off on the next launch.
    @MainActor func testTheRouteSwitch() throws {
        let app = try launch(["-showScreenRoutes", "YES", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"])
        let row = app.buttons["settings-live-screen"].firstMatch
        for _ in 0..<8 where !row.isHittable { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the row:\n\(app.debugDescription)")
        row.tap()
        let toggle = app.switches["settings-live-screen-routes"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 10), "the switch:\n\(app.debugDescription)")
        XCTAssertEqual(toggle.value as? String, "1", "on by default")
        toggle.switches.firstMatch.exists ? toggle.switches.firstMatch.tap() : toggle.tap()
        XCTAssertEqual(toggle.value as? String, "0")
        app.terminate()

        let again = try launch(["-showScreenRoutes", "YES", "-openLiveScreenSettings", "YES"])
        let kept = again.switches["settings-live-screen-routes"].firstMatch
        XCTAssertTrue(kept.waitForExistence(timeout: 10))
        XCTAssertEqual(kept.value as? String, "0", "kept off")
        kept.switches.firstMatch.exists ? kept.switches.firstMatch.tap() : kept.tap() // back on for the next test
        XCTAssertEqual(kept.value as? String, "1")
    }

    /// The page and the row for review: light and dark, default size and xxxLarge, English and Chinese. The page is
    /// reached by tapping its row, as people reach it (opened at launch, an iPad's detail column draws a short page's
    /// large title over its first row).
    @MainActor func testShots() throws {
        for lang in ["en", "zh-Hans"] {
            for scheme in ["light", "dark"] {
                for size in ["UICTContentSizeCategoryL", "UICTContentSizeCategoryXXXL"] {
                    let locale = lang == "en" ? "en_US" : "zh_CN"
                    let app = try launch(["-showScreenRoutes", "YES", "-ui.scheme", scheme, "-UIPreferredContentSizeCategoryName", size,
                                          "-AppleLanguages", "(\(lang))", "-AppleLocale", locale])
                    dismissIconNotice(app)
                    let row = app.buttons["settings-live-screen"].firstMatch
                    XCTAssertTrue(app.searchFields.firstMatch.waitForExistence(timeout: 15), "Settings:\n\(app.debugDescription)")
                    for _ in 0..<12 where !(row.exists && row.isHittable) { app.swipeUp() }
                    XCTAssertTrue(row.waitForExistence(timeout: 5), "the row:\n\(app.debugDescription)")
                    let short = size.hasSuffix("XXXL") ? "xxxl" : "default"
                    if short == "default" { shot(app, "live-screen-row-\(lang)-\(scheme)") }
                    row.tap()
                    XCTAssertTrue(app.switches["settings-live-screen-routes"].firstMatch.waitForExistence(timeout: 10))
                    sleep(1) // the push settles
                    shot(app, "live-screen-settings-\(lang)-\(scheme)-\(short)")
                    app.terminate()
                }
            }
        }
    }

    /// The page reached by its row in Settings, as people reach it, then Scroll Speed's choice brought into view.
    @MainActor private func scrollSpeed(_ app: XCUIApplication, choice: String) -> XCUIElement {
        let row = app.buttons["settings-live-screen"].firstMatch
        XCTAssertTrue(app.searchFields.firstMatch.waitForExistence(timeout: 15), "Settings:\n\(app.debugDescription)")
        for _ in 0..<12 where !(row.exists && row.isHittable) { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 5), "the row:\n\(app.debugDescription)")
        row.tap()
        let button = app.buttons[choice].firstMatch
        XCTAssertTrue(app.navigationBars.firstMatch.waitForExistence(timeout: 10))
        for _ in 0..<8 where !(button.exists && button.isHittable) { app.swipeUp() }
        XCTAssertTrue(button.waitForExistence(timeout: 10), "Scroll Speed:\n\(app.debugDescription)")
        app.swipeUp() // and its footer, the page's last words, at the largest sizes too
        return button
    }

    /// Scroll Speed (#7775): Default until chosen; Faster stays chosen on the next launch.
    @MainActor func testScrollSpeed() throws {
        let english = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        let app = try launch(english)
        let faster = scrollSpeed(app, choice: "Faster")
        XCTAssertTrue(app.buttons["Default"].firstMatch.isSelected, "Default until chosen")
        faster.tap()
        XCTAssertTrue(faster.isSelected)
        app.terminate()

        let again = try launch(english)
        let kept = scrollSpeed(again, choice: "Faster")
        XCTAssertTrue(kept.isSelected, "kept on the next launch")
        again.buttons["Default"].firstMatch.tap() // back for the next test
        XCTAssertTrue(again.buttons["Default"].firstMatch.isSelected)
    }

    /// Scroll Speed for review (#7775): light and dark, default size and xxxLarge, English and Chinese; the same test on
    /// an iPhone and an iPad.
    @MainActor func testScrollSpeedShots() throws {
        for lang in ["en", "zh-Hans"] {
            for scheme in ["light", "dark"] {
                for size in ["UICTContentSizeCategoryL", "UICTContentSizeCategoryXXXL"] {
                    let locale = lang == "en" ? "en_US" : "zh_CN"
                    let app = try launch(["-ui.scheme", scheme, "-UIPreferredContentSizeCategoryName", size,
                                          "-AppleLanguages", "(\(lang))", "-AppleLocale", locale])
                    _ = scrollSpeed(app, choice: lang == "en" ? "Faster" : "较快")
                    sleep(1) // the scroll settles
                    shot(app, "live-screen-scroll-speed-\(lang)-\(scheme)-\(size.hasSuffix("XXXL") ? "xxxl" : "default")")
                    app.terminate()
                }
            }
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let full = "\(name)-\(UITestStyle.name)"
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = full
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(full + ".png"))
        }
    }

    /// The icon follows the look, and iOS says so in an alert over the screen: tap it away.
    @MainActor @discardableResult private func dismissIconNotice(_ app: XCUIApplication) -> Bool {
        let notice = NSPredicate(format: "label CONTAINS[c] 'icon' OR label CONTAINS '图标'")
        for source in [app, XCUIApplication(bundleIdentifier: "com.apple.springboard")] {
            let alert = source.alerts.containing(notice).firstMatch
            if alert.waitForExistence(timeout: 2), alert.buttons.firstMatch.exists {
                alert.buttons.firstMatch.tap()
                return true
            }
        }
        return false
    }
}
