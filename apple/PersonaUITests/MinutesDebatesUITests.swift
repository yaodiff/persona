import XCTest

/// Disagreements in meeting minutes on iPhone (#16728, docs/assistant/meeting-minutes.md "Disagreements (#16728)",
/// docs/apple/recording.md "Minutes"): exercises the mock hub's `/mock/recording_minutes_debates` domain (`on`,
/// `malformed`, `off`) across the summary-card layout, the legacy four-cards layout, and MeetingPage's after mode
/// (`minutesBoxed`), at default and largest accessibility text sizes, in light and dark mode.
/// PERSONA_SHOTS=<dir> saves debates-<appearance>-<name>.png.
final class MinutesDebatesUITests: XCTestCase {
    override func tearDown() {
        if let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] {
            try? postJSON(hub + "/mock/recording_minutes_debates", ["mode": "off"])
        }
        super.tearDown()
    }

    /// Summary card layout with debates (`shape: overview`, `debates: on`): Disagreements sits after Core Conclusions
    /// and before the first topic, shows settled + open cards with status text, opens inline context, and tapping a
    /// position's time jumps to that line in the Transcript, lit.
    @MainActor func testOverviewDisagreementsAndJumpToTranscript() throws {
        let (app, _) = try openSummary(sample: "debates", shape: "overview", debates: "on")
        let summary = element(app, "minutes-card-summary")
        if !summary.waitForExistence(timeout: 5) { reveal(summary, in: app) }
        XCTAssertTrue(summary.exists, app.debugDescription)

        let heading = element(app, "minutes-debates-heading")
        reveal(heading, in: app)
        XCTAssertTrue(heading.exists, "no Disagreements heading:\n\(app.debugDescription)")
        XCTAssertTrue(heading.label.contains("分歧") || app.staticTexts["分歧"].exists, heading.debugDescription)

        let first = element(app, "minutes-debate-0")
        reveal(first, in: app)
        XCTAssertTrue(first.exists, "no first debate card:\n\(app.debugDescription)")
        XCTAssertTrue(first.staticTexts["导出上限按单条还是按整份录音计算？"].exists, first.debugDescription)
        let firstStatus = first.descendants(matching: .any).matching(identifier: "minutes-debate-status").firstMatch
        XCTAssertTrue(firstStatus.exists && firstStatus.label == "已谈拢", "settled status missing:\n\(first.debugDescription)")
        let outcome = first.descendants(matching: .any).matching(identifier: "minutes-debate-outcome").firstMatch
        reveal(outcome, in: app)
        XCTAssertTrue(outcome.exists, "no outcome box on settled debate:\n\(first.debugDescription)")
        XCTAssertTrue(outcome.descendants(matching: .any).matching(identifier: "minutes-debate-decision").firstMatch.exists,
                      "no hub-linked decision on settled outcome:\n\(outcome.debugDescription)")
        try shot(app, "overview-settled")

        let second = element(app, "minutes-debate-1")
        reveal(second, in: app)
        XCTAssertTrue(second.exists, "no second debate card:\n\(app.debugDescription)")
        XCTAssertTrue(second.staticTexts["周报邮件是否默认开启？"].exists, second.debugDescription)
        let secondStatus = second.descendants(matching: .any).matching(identifier: "minutes-debate-status").firstMatch
        XCTAssertTrue(secondStatus.exists && secondStatus.label == "仍有分歧", "open status missing:\n\(second.debugDescription)")
        XCTAssertFalse(second.descendants(matching: .any).matching(identifier: "minutes-debate-outcome").firstMatch.exists,
                       "open debate must not show an outcome:\n\(second.debugDescription)")
        try shot(app, "overview-open")

        // The first topic still follows after Disagreements.
        let topic = element(app, "minutes-topic-0-0")
        reveal(topic, in: app)
        XCTAssertTrue(topic.exists, "no topic card after Disagreements:\n\(app.debugDescription)")

        // Open inline transcript context on a position, then jump via its time chip to the lit transcript line.
        reveal(first, in: app)
        let position = first.descendants(matching: .any).matching(identifier: "minutes-debate-position").firstMatch
        let disclosure = position.buttons["minutes-item-context"].firstMatch
        reveal(disclosure, in: app)
        XCTAssertTrue(disclosure.isHittable, first.debugDescription)
        disclosure.tap()
        let lines = first.descendants(matching: .any).matching(identifier: "minutes-item-lines").firstMatch
        XCTAssertTrue(lines.waitForExistence(timeout: 3), "position disclosure opened no context:\n\(first.debugDescription)")

        let time = position.buttons["minutes-time"].firstMatch
        reveal(time, in: app)
        time.tap()
        let lit = element(app, "recording-sentence-lit")
        XCTAssertTrue(lit.waitForExistence(timeout: 3), "position time didn't jump to Transcript:\n\(app.debugDescription)")
        XCTAssertTrue(hittable(lit, within: 2), "the jumped transcript line is not in view")
        try shot(app, "overview-transcript")
    }

    /// Legacy layout with debates (`shape: legacy`, `debates: on`): Disagreements sits after Decisions and before Open
    /// Questions, and tapping a position's time jumps to the transcript line.
    @MainActor func testLegacyDisagreementsAfterDecisionsAndBeforeOpenQuestions() throws {
        let (app, _) = try openSummary(sample: "minutes", shape: "legacy", debates: "on")
        let decisions = element(app, "minutes-card-decisions")
        reveal(decisions, in: app)
        XCTAssertTrue(decisions.exists, app.debugDescription)

        let heading = element(app, "minutes-debates-heading")
        reveal(heading, in: app)
        XCTAssertTrue(heading.exists, "no Disagreements heading in legacy layout:\n\(app.debugDescription)")

        let first = element(app, "minutes-debate-0")
        reveal(first, in: app)
        XCTAssertTrue(first.exists, "no first debate card in legacy layout:\n\(app.debugDescription)")
        try shot(app, "legacy")

        // Open Questions and Action Items follow Disagreements.
        let openCard = element(app, "minutes-card-open")
        reveal(openCard, in: app)
        XCTAssertTrue(openCard.exists, "no Open Questions after Disagreements:\n\(app.debugDescription)")

        // Tapping a position time chip jumps to the Transcript with that sentence lit.
        reveal(first, in: app)
        let time = first.descendants(matching: .any).matching(identifier: "minutes-debate-position").firstMatch.buttons["minutes-time"].firstMatch
        reveal(time, in: app)
        time.tap()
        let lit = element(app, "recording-sentence-lit")
        XCTAssertTrue(lit.waitForExistence(timeout: 3), "legacy position time didn't jump to Transcript:\n\(app.debugDescription)")
        XCTAssertTrue(hittable(lit, within: 2), "the jumped line is not in view")
    }

    /// MeetingPage after mode (`-showMeetingSample after-debates`, `minutesBoxed`): reuses the disagreements card in
    /// boxed mode and keeps the after-meeting summary card's counts unchanged.
    @MainActor func testMeetingAfterModeBoxedDisagreements() throws {
        let hub = try mockHub()
        try postJSON(hub + "/mock/recording_minutes", ["mode": "ready", "shape": "overview"])
        try postJSON(hub + "/mock/recording_minutes_debates", ["mode": "on"])
        XCUIDevice.shared.appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] == "dark" ? .dark : .light
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "after-debates"]
        app.launchPinned()

        let summary = element(app, "meeting-after-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 45), "no after summary card:\n\(app.debugDescription)")
        XCTAssertTrue(waitFor(45) { summary.staticTexts["决定"].exists }, "the card has no counts:\n\(summary.debugDescription)")
        let first = element(app, "minutes-debate-0")
        reveal(first, in: app)
        XCTAssertTrue(first.exists, "no boxed debate card in MeetingPage after mode:\n\(app.debugDescription)")
        try shot(app, "after")
    }

    /// Malformed debates block cleans down to nothing: the rest of the minutes render normally with no Disagreements section.
    @MainActor func testMalformedDebatesShowsNoSection() throws {
        let (app, _) = try openSummary(sample: "debates", shape: "overview", debates: "malformed")
        let summary = element(app, "minutes-card-summary")
        if !summary.waitForExistence(timeout: 5) { reveal(summary, in: app) }
        XCTAssertTrue(summary.exists, app.debugDescription)
        let topic = element(app, "minutes-topic-0-0")
        reveal(topic, in: app)
        XCTAssertTrue(topic.exists, "topics missing under malformed debates:\n\(app.debugDescription)")
        XCTAssertFalse(element(app, "minutes-debates-heading").exists, "malformed debates showed a heading")
        XCTAssertFalse(element(app, "minutes-debate-0").exists, "malformed debates showed a card")
    }

    /// Largest accessibility text size (AX5): disagreements read whole without clipping in summary-card, legacy, and
    /// after-meeting boxed layouts.
    @MainActor func testLargestTextSize() throws {
        let ax5 = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL", "-ui.textSize", "standard"]
        // 1. Summary card layout at AX5
        let (overviewApp, _) = try openSummary(sample: "debates", shape: "overview", debates: "on", extra: ax5, searching: true)
        let firstOverview = element(overviewApp, "minutes-debate-0")
        reveal(firstOverview, in: overviewApp)
        XCTAssertTrue(firstOverview.exists, overviewApp.debugDescription)
        try shot(overviewApp, "overview-largest")
        overviewApp.terminate()

        // 2. Legacy layout at AX5
        let (legacyApp, _) = try openSummary(sample: "minutes", shape: "legacy", debates: "on", extra: ax5, searching: true)
        let firstLegacy = element(legacyApp, "minutes-debate-0")
        reveal(firstLegacy, in: legacyApp)
        XCTAssertTrue(firstLegacy.exists, legacyApp.debugDescription)
        try shot(legacyApp, "legacy-largest")
        legacyApp.terminate()

        // 3. MeetingPage after mode at AX5
        let hub = try mockHub()
        try postJSON(hub + "/mock/recording_minutes", ["mode": "ready", "shape": "overview"])
        try postJSON(hub + "/mock/recording_minutes_debates", ["mode": "on"])
        XCUIDevice.shared.appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] == "dark" ? .dark : .light
        let afterApp = XCUIApplication()
        afterApp.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings",
                                    "-showMeetingSample", "after-debates"] + ax5
        afterApp.launchPinned()
        let summary = element(afterApp, "meeting-after-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 45), afterApp.debugDescription)
        XCTAssertTrue(waitFor(45) { summary.staticTexts["决定"].exists }, afterApp.debugDescription)
        let firstAfter = element(afterApp, "minutes-debate-0")
        reveal(firstAfter, in: afterApp)
        XCTAssertTrue(firstAfter.exists, afterApp.debugDescription)
        try shot(afterApp, "after-largest")
        afterApp.terminate()
    }

    // MARK: Helpers

    @MainActor private func openSummary(sample: String, shape: String, debates: String, extra: [String] = [],
                                        searching: Bool = false) throws -> (XCUIApplication, String) {
        let hub = try mockHub()
        try postJSON(hub + "/mock/recording_minutes", ["mode": "ready", "shape": shape])
        try postJSON(hub + "/mock/recording_minutes_debates", ["mode": debates])
        XCUIDevice.shared.appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] == "dark" ? .dark : .light
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings",
                               "-showMeetingSample", sample] + extra
        app.launchPinned()
        let control = findSwitch(app, searching: searching)
        XCTAssertTrue(segment(control, "recording-view-summary", 0).isSelected, "the default isn't Summary:\n\(app.debugDescription)")
        return (app, hub)
    }

    @MainActor private func findSwitch(_ app: XCUIApplication, searching: Bool) -> XCUIElement {
        let control = app.segmentedControls["recording-view-switch"].firstMatch
        if searching || !control.waitForExistence(timeout: 45) {
            let deadline = Date.now.addingTimeInterval(45)
            while !control.exists, Date.now < deadline {
                app.swipeUp()
                if control.exists { break }
                app.swipeDown()
                sleep(1)
            }
        }
        XCTAssertTrue(control.exists, app.debugDescription)
        return control
    }

    @MainActor private func segment(_ control: XCUIElement, _ id: String, _ index: Int) -> XCUIElement {
        let named = control.buttons[id].firstMatch
        return named.exists ? named : control.buttons.element(boundBy: index)
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    @MainActor private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        let window = app.windows.firstMatch.frame
        let band = (window.minY + window.height * 0.2)...(window.minY + window.height * 0.8)
        let middle = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        var swipes = 0
        while !(element.exists && element.isHittable && band.contains(element.frame.midY)), swipes < 24 {
            if element.exists {
                let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: element.frame.midY < band.lowerBound ? 0.75 : 0.25))
                middle.press(forDuration: 0.1, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.2)
            } else if swipes < 12 {
                app.swipeUp()
            } else {
                app.swipeDown()
            }
            swipes += 1
        }
    }

    @MainActor private func hittable(_ element: XCUIElement, within seconds: TimeInterval) -> Bool {
        let deadline = Date.now.addingTimeInterval(seconds)
        repeat {
            if element.exists && element.isHittable { return true }
            usleep(200_000)
        } while Date.now < deadline
        return false
    }

    @MainActor private func waitFor(_ seconds: TimeInterval = 10, _ ok: () -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(seconds)
        while Date.now < deadline {
            if ok() { return true }
            usleep(250_000)
        }
        return ok()
    }

    private func postJSON(_ urlString: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: urlString)!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        var status = 0
        let done = expectation(description: urlString)
        URLSession.shared.dataTask(with: request) { _, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
        XCTAssertEqual(status, 200, "POST \(urlString) returned \(status)")
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/debates-\(appearance)-\(name).png"))
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs its own mock hub") }
        return hub
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }
}
