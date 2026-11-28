import XCTest

/// The main-chat composer against the real hub edition (#17267). ios-core-only-hub.py pairs the app and supplies the
/// current synthetic main session; this test only reuses the app's issued credential. No fixture: skip in ordinary suites.
final class CoreOnlyHubLiveUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }

    @MainActor func testComposerFollowsTheRealHubEdition() throws {
        guard let path = ProcessInfo.processInfo.environment["PERSONA_IOS_CORE_HUB_FIXTURE"] else {
            throw XCTSkip("Set TEST_RUNNER_PERSONA_IOS_CORE_HUB_FIXTURE through scripts/ci/ios-core-only-hub.py")
        }
        let fixtureURL = URL(fileURLWithPath: path)
        let fixture = try JSONSerialization.jsonObject(with: Data(contentsOf: fixtureURL)) as? [String: Any]
        guard let fixture, fixture["schema"] as? Int == 1,
              let runID = fixture["run_id"] as? String, !runID.isEmpty,
              let edition = fixture["edition"] as? String, ["core-only", "full"].contains(edition),
              fixture["confirmed_loopback"] as? Bool == true,
              let main = fixture["main_session"] as? String, !main.isEmpty,
              let base = fixture["base_url"] as? String,
              let components = URLComponents(string: base), components.scheme == "http",
              let host = components.host, ["127.0.0.1", "::1", "localhost"].contains(host),
              components.user == nil && components.password == nil && components.query == nil && components.fragment == nil else {
            XCTFail("The live composer needs a valid loopback fixture with a synthetic main session")
            return
        }
        let shots = ProcessInfo.processInfo.environment["PERSONA_SHOTS"]
        var captures: [String] = []
        for appearance in ["light", "dark"] {
            let shot = try check(app: launch(main: main), edition: edition, appearance: appearance, shots: shots)
            captures.append(shot)
        }
        let proof: [String: Any] = ["schema": 1, "run_id": runID, "edition": edition,
                                    "meeting_visible": edition == "full", "captures": captures]
        let output = fixtureURL.deletingLastPathComponent().appendingPathComponent("composer-proof.json")
        try JSONSerialization.data(withJSONObject: proof, options: [.sortedKeys]).write(to: output)
    }

    @MainActor private func launch(main: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-tab", "assistant", "-openAssistant", main,
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launchPinned()
        return app
    }

    @MainActor private func check(app: XCUIApplication, edition: String, appearance: String, shots: String?) throws -> String {
        defer { app.terminate() }
        XCUIDevice.shared.appearance = appearance == "dark" ? .dark : .light
        let mainMarker = app.descendants(matching: .any).matching(NSPredicate(
            format: "label CONTAINS %@", "Synthetic core-edition fixture")).firstMatch
        XCTAssertTrue(mainMarker.waitForExistence(timeout: 20), "the route did not open the synthetic main conversation:\n\(app.debugDescription)")
        let addButton = app.descendants(matching: .any).matching(identifier: "shell-composer-add").firstMatch
        XCTAssertTrue(addButton.waitForExistence(timeout: 40), "no composer + button:\n\(app.debugDescription)")
        let enabled = XCTNSPredicateExpectation(predicate: NSPredicate(format: "enabled == true"), object: addButton)
        XCTAssertEqual(XCTWaiter.wait(for: [enabled], timeout: 30), .completed,
                       "the main session's composer stayed disabled:\n\(app.debugDescription)")
        addButton.press(forDuration: 1.0)
        let mention = app.descendants(matching: .any).matching(NSPredicate(
            format: "identifier == %@ OR label == %@", "composer-menu-mention", "Mention a File or Session")).firstMatch
        XCTAssertTrue(mention.waitForExistence(timeout: 15), "the composer menu did not open:\n\(app.debugDescription)")
        let meeting = app.descendants(matching: .any).matching(NSPredicate(
            format: "identifier == %@ OR label IN %@", "composer-menu-meeting",
            ["Start Meeting", "Record a Meeting", "Record"])).firstMatch
        let present = meeting.waitForExistence(timeout: 5)
        XCTAssertEqual(present, edition == "full", "wrong recording entry for \(edition):\n\(app.debugDescription)")
        let name = "\(edition)-\(appearance)-composer.png"
        let screenshot = XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let shots {
            try screenshot.pngRepresentation.write(to: URL(fileURLWithPath: shots).appendingPathComponent(name))
        }
        XCTAssertEqual(app.state, .runningForeground)
        return name
    }
}
