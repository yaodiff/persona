import XCTest

/// The device's Main Chat switch and Settings share one preference, including across launches.
final class ReplySpeechSettingsUITests: XCTestCase {
    @MainActor func testMenuAndSettingsShareTheSwitch() throws {
        try checkSwitch(largeText: false)
    }

    @MainActor func testMenuAndSettingsAtLargestTextSize() throws {
        try checkSwitch(largeText: true)
    }

    @MainActor private func checkSwitch(largeText: Bool) throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("iPhone-only setting") }
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py")
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        let originalAppearance = XCUIDevice.shared.appearance
        defer { XCUIDevice.shared.appearance = originalAppearance }
        for (appearance, name) in [(XCUIDevice.Appearance.light, "light"), (.dark, "dark")] {
            XCUIDevice.shared.appearance = appearance
            let presentation = ["-ui.scheme", name, "-showSameIcon", "YES"] + (largeText
                ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] : [])
            let suffix = name + (largeText ? "-ax" : "")
            openSettings(in: app, pair: pair, presentation: presentation)
            let toggle = app.switches["reply-speech-enabled"].firstMatch
            XCTAssertTrue(toggle.waitForExistence(timeout: 8), app.debugDescription)
            let readAloudLabel = toggle.label
            // The previous test run may have left it on.
            if toggle.value as? String == "1" { tapSwitch(toggle) }
            XCTAssertEqual(toggle.value as? String, "0")
            app.terminate()

            app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openChatsFresh", "YES",
                                   "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"] + presentation
            app.launchPinned()
            let menu = app.buttons["shell-assistant"].firstMatch
            XCTAssertTrue(menu.waitForExistence(timeout: 20), app.debugDescription)
            let message = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "今天有"))
            XCTAssertTrue(message.firstMatch.waitForExistence(timeout: 15), app.debugDescription)
            menu.tap()
            // A native menu exposes this toggle as a button with its localized label.
            let item = app.buttons.matching(NSPredicate(format: "label == %@", readAloudLabel)).firstMatch
            XCTAssertTrue(item.waitForExistence(timeout: 8), app.debugDescription)
            capture(app, "reply-speech-menu-" + suffix)
            item.tap()
            app.terminate()

            openSettings(in: app, pair: pair, presentation: presentation)
            XCTAssertTrue(toggle.waitForExistence(timeout: 8))
            XCTAssertEqual(toggle.value as? String, "1", "the menu's choice survives relaunch")
            for preference in ["engine", "range", "rate", "voice"] {
                XCTAssertTrue(app.descendants(matching: .any)["reply-speech-" + preference].firstMatch.exists)
            }
            capture(app, "reply-speech-settings-" + suffix)
            tapSwitch(toggle)
            app.terminate()
        }
    }

    @MainActor private func openSettings(in app: XCUIApplication, pair: String, presentation: [String]) {
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openAssistantPage", "assistant"] + presentation
        app.launchPinned()
        let entry = app.buttons["reply-speech-settings"].firstMatch
        XCTAssertTrue(app.navigationBars.firstMatch.waitForExistence(timeout: 10), app.debugDescription)
        for _ in 0..<8 where !entry.isHittable { app.swipeUp() }
        XCTAssertTrue(entry.isHittable, app.debugDescription)
        entry.tap()
    }

    @MainActor private func tapSwitch(_ toggle: XCUIElement) {
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.5)).tap()
    }

    @MainActor private func capture(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
