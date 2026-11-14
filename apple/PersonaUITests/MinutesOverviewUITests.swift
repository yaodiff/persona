import XCTest

/// A recording's Summary with the summary card on iPhone (#6563, docs/assistant/meeting-minutes.md "Summary card (#6563)",
/// docs/apple/recording.md "Minutes"): the made-up sprint review (`-showMeetingSample overview`, five minutes of lines)
/// against a mock hub (PERSONA_MOCK_HUB) that writes its minutes with an overview (`POST /mock/recording_minutes
/// {"mode": "ready", "shape": "overview"}`): the summary card with the headline and Core Conclusions, topic cards under
/// their group headings, a decision, a time that opens the transcript lit, the fold, Action Items with Add to To-Dos, Open
/// Questions with the added actions' circles, and Copy. `malformed` falls back to the four cards, as `legacy` does.
/// PERSONA_SHOTS=<dir> saves
/// overview-<appearance>-<name>.png (PERSONA_SHOTS_APPEARANCE names the appearance; run once in light and once in dark),
/// at the default and the largest text size.
final class MinutesOverviewUITests: XCTestCase {
    private let legacyCards = ["minutes-card-points", "minutes-card-decisions", "minutes-card-open", "minutes-card-actions"]

    /// The summary card, the topics, a decision and the fold; a bullet's lines in place, its time on the Transcript, lit.
    @MainActor func testSummaryCardAndTopics() throws {
        let (app, _) = try openSummary("overview")
        let summary = element(app, "minutes-card-summary")
        if !summary.waitForExistence(timeout: 5) { reveal(summary, in: app) }
        XCTAssertTrue(summary.exists, app.debugDescription)
        XCTAssertTrue(element(app, "minutes-headline").exists, "no headline:\n\(app.debugDescription)")
        let conclusions = element(app, "minutes-conclusions")
        reveal(conclusions, in: app)
        XCTAssertTrue(conclusions.exists, "no Core Conclusions:\n\(app.debugDescription)")
        XCTAssertTrue(conclusions.staticTexts["核心结论"].exists, "the box has no heading:\n\(conclusions.debugDescription)")
        XCTAssertTrue(conclusions.descendants(matching: .any).matching(identifier: "minutes-conclusion").firstMatch.exists)
        // Points and decisions are not shown again in cards of their own.
        for id in ["minutes-card-points", "minutes-card-decisions", "minutes-card-header"] {
            XCTAssertFalse(element(app, id).exists, "\(id) beside the summary card:\n\(app.debugDescription)")
        }
        try shot(app, "summary")

        // The first topic, under its group's heading (the mock groups five topics in two).
        let topic = element(app, "minutes-topic-0-0")
        reveal(topic, in: app)
        XCTAssertTrue(topic.exists, "no topic card:\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "minutes-group-0").exists, "no group heading:\n\(app.debugDescription)")
        // The tile beside the title: the title starts after it.
        let title = topic.descendants(matching: .any).matching(identifier: "minutes-topic-title").firstMatch
        XCTAssertTrue(title.exists, topic.debugDescription)
        XCTAssertGreaterThan(title.frame.minX - topic.frame.minX, 20, "the tile isn't beside the title: \(title.frame) in \(topic.frame)")
        try shot(app, "topics")

        // A decision: its mark and the word Decision where a point has its speaker.
        let decision = topic.descendants(matching: .any).matching(identifier: "minutes-decision").firstMatch
        XCTAssertTrue(decision.exists, "no decision in the first topic:\n\(topic.debugDescription)")
        XCTAssertTrue(decision.staticTexts["决定"].exists, "the decision doesn't say Decision:\n\(decision.debugDescription)")

        // The fold: four bullets, then Show 1 More opens the fifth in place; Show Less folds it again.
        let more = topic.buttons["minutes-topic-more"].firstMatch
        reveal(more, in: app)
        XCTAssertTrue(more.exists, "no Show More on a topic of five:\n\(topic.debugDescription)")
        XCTAssertEqual(bullets(topic).count, 4, topic.debugDescription)
        XCTAssertTrue(more.label.contains("1"), "Show More doesn't say how many: \(more.label)")
        more.tap()
        let less = topic.buttons["minutes-topic-less"].firstMatch
        XCTAssertTrue(less.waitForExistence(timeout: 3), topic.debugDescription)
        XCTAssertEqual(bullets(topic).count, 5, topic.debugDescription)
        try shot(app, "fold")
        less.tap()
        XCTAssertTrue(topic.buttons["minutes-topic-more"].firstMatch.waitForExistence(timeout: 3), topic.debugDescription)

        // A bullet opens its transcript lines in place.
        let disclosure = topic.buttons["minutes-item-context"].firstMatch
        reveal(disclosure, in: app)
        XCTAssertTrue(disclosure.isHittable, app.debugDescription)
        disclosure.tap()
        let lines = topic.descendants(matching: .any).matching(identifier: "minutes-item-lines").firstMatch
        XCTAssertTrue(lines.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertEqual(disclosure.value as? String, "已展开")
        try shot(app, "context")

        // Its time: the Transcript, that line lit and on the screen.
        let time = decision.buttons["minutes-time"].firstMatch
        reveal(time, in: app)
        time.tap()
        let lit = element(app, "recording-sentence-lit")
        XCTAssertTrue(lit.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(hittable(lit, within: 2), "the line is not in view")
        XCTAssertFalse(element(app, "minutes-card-summary").exists, "the summary card stays over the transcript")
        try shot(app, "transcript")
    }

    /// Action Items (Add to To-Dos, then Added, and its to-do's circle once the app holds it, which ticks the to-do on
    /// the hub and opens it again, VoiceOver naming each circle by its action), Open Questions, the AI note, and Copy,
    /// which sends the hub's Markdown (read back by pasting it into the meeting thread's box: the runner may not read the
    /// app's pasteboard).
    @MainActor func testActionsOpenQuestionsAndCopy() throws {
        let (app, hub) = try openSummary("overview")
        let actions = element(app, "minutes-card-actions")
        reveal(actions, in: app)
        XCTAssertTrue(actions.exists, "no Action Items:\n\(app.debugDescription)")
        try shot(app, "actions")
        let add = actions.buttons["minutes-add"].firstMatch
        reveal(add, in: app)
        XCTAssertTrue(add.isHittable, actions.debugDescription)
        add.tap()
        XCTAssertTrue(actions.descendants(matching: .any).matching(identifier: "minutes-added").firstMatch.waitForExistence(timeout: 10),
                      "no Added after Add to To-Dos:\n\(actions.debugDescription)")
        // The to-do's circle shows once the app has read the new to-do (the hub's `todos` event), named by its action.
        let circle = actions.descendants(matching: .any).matching(identifier: "minutes-todo-open").firstMatch
        XCTAssertTrue(circle.waitForExistence(timeout: 10), "no to-do circle on the added action:\n\(actions.debugDescription)")
        let first = try minutesActions(hub).first
        let what = try XCTUnwrap((first?["text"] ?? first?["what"]) as? String), todo = try XCTUnwrap(first?["todo_id"] as? String)
        XCTAssertFalse(todo.isEmpty, "the mock gave the added action no to-do")
        XCTAssertEqual(circle.label, "已完成：" + what, "the circle isn't named by its action")
        try shot(app, "actions-added")
        // Ticked: done here and on the hub; ticked again: open again, on both.
        reveal(circle, in: app)
        circle.tap()
        let done = actions.descendants(matching: .any).matching(identifier: "minutes-todo-done").firstMatch
        XCTAssertTrue(done.waitForExistence(timeout: 5), "the circle didn't tick:\n\(actions.debugDescription)")
        XCTAssertTrue(todoDone(hub, todo, is: true), "the hub's to-do isn't done")
        try shot(app, "actions-done")
        done.tap()
        XCTAssertTrue(actions.descendants(matching: .any).matching(identifier: "minutes-todo-open").firstMatch.waitForExistence(timeout: 5),
                      "the circle didn't open again:\n\(actions.debugDescription)")
        XCTAssertTrue(todoDone(hub, todo, is: false), "the hub's to-do isn't open again")
        // A second added action: two circles, each named by its own action.
        let next = actions.buttons["minutes-add"].firstMatch
        reveal(next, in: app)
        XCTAssertTrue(next.isHittable, actions.debugDescription)
        next.tap()
        let circles = actions.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'minutes-todo-'"))
        let deadline = Date.now.addingTimeInterval(10)
        while circles.count < 2, Date.now < deadline { usleep(250_000) }
        let labels = circles.allElementsBoundByIndex.map(\.label)
        XCTAssertEqual(labels.count, 2, actions.debugDescription)
        XCTAssertEqual(Set(labels).count, labels.count, "the circles' names are the same: \(labels)")
        XCTAssertTrue(labels.allSatisfy { $0.hasPrefix("已完成：") && $0.count > "已完成：".count }, "\(labels)")
        let open = element(app, "minutes-card-open")
        reveal(open, in: app)
        XCTAssertTrue(open.exists, "no Open Questions:\n\(app.debugDescription)")
        let note = element(app, "minutes-ai-note")
        reveal(note, in: app)
        XCTAssertTrue(note.exists, "no AI note:\n\(app.debugDescription)")
        try shot(app, "open")

        // Copy: the hub's Markdown, in the summary card's layout.
        let control = app.segmentedControls["recording-view-switch"].firstMatch
        back(to: control, in: app)
        app.buttons["minutes-copy"].firstMatch.tap()
        XCTAssertTrue(app.staticTexts["已复制纪要"].firstMatch.waitForExistence(timeout: 5) || app.otherElements["已复制纪要"].exists,
                      "no Minutes copied:\n\(app.debugDescription)")
        let markdown = try minutesMarkdown(hub)
        XCTAssertFalse(markdown.isEmpty, "the mock sent no markdown")
        let copied = pasted(app)
        XCTAssertEqual(copied.trimmingCharacters(in: .whitespacesAndNewlines), markdown.trimmingCharacters(in: .whitespacesAndNewlines),
                       "Copy didn't send the hub's Markdown")
    }

    /// A malformed overview (`"groups": "x"`) never costs the minutes: the four cards, as before.
    @MainActor func testMalformedOverviewShowsTheFourCards() throws {
        let (app, _) = try openSummary("malformed")
        for id in legacyCards {
            let card = element(app, id)
            reveal(card, in: app)
            XCTAssertTrue(card.exists, "no \(id) for a malformed overview:\n\(app.debugDescription)")
        }
        XCTAssertFalse(element(app, "minutes-card-summary").exists, "a summary card from a malformed overview")
        XCTAssertFalse(element(app, "minutes-ai-note").exists, "the AI note under the four cards")
        try shot(app, "malformed")
    }

    /// Minutes without an overview are as they were.
    @MainActor func testLegacyMinutesAreUnchanged() throws {
        let (app, _) = try openSummary("legacy")
        let header = element(app, "minutes-card-header")
        reveal(header, in: app)
        XCTAssertTrue(header.exists, app.debugDescription)
        for id in legacyCards {
            let card = element(app, id)
            reveal(card, in: app)
            XCTAssertTrue(card.exists, "no \(id):\n\(app.debugDescription)")
        }
        XCTAssertFalse(element(app, "minutes-card-summary").exists)
        XCTAssertFalse(element(app, "minutes-ai-note").exists)
    }

    /// At the largest text size a bullet's time goes under its words, the tile above its topic's title (which starts at
    /// the card's edge, under it), and the cards still read whole.
    @MainActor func testLargestTextSize() throws {
        let (app, _) = try openSummary("overview", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"],
                                       searching: true)
        let summary = element(app, "minutes-card-summary")
        reveal(summary, in: app)
        XCTAssertTrue(summary.exists, app.debugDescription)
        try shot(app, "summary-largest")
        let conclusions = element(app, "minutes-conclusions")
        reveal(conclusions, in: app)
        try shot(app, "conclusions-largest")
        let topic = element(app, "minutes-topic-0-0")
        reveal(topic, in: app)
        XCTAssertTrue(topic.exists, app.debugDescription)
        let title = topic.descendants(matching: .any).matching(identifier: "minutes-topic-title").firstMatch
        XCTAssertTrue(title.exists, topic.debugDescription)
        XCTAssertLessThanOrEqual(title.frame.minX - topic.frame.minX, 2, "the title doesn't start at the card's edge: \(title.frame) in \(topic.frame)")
        XCTAssertGreaterThan(title.frame.minY - topic.frame.minY, 20, "the tile isn't above the title: \(title.frame) in \(topic.frame)")
        try shot(app, "topics-largest")
        let decision = topic.descendants(matching: .any).matching(identifier: "minutes-decision").firstMatch
        let time = decision.buttons["minutes-time"].firstMatch
        reveal(time, in: app)
        let words = decision.staticTexts.element(boundBy: 0)
        XCTAssertTrue(words.exists && time.exists, decision.debugDescription)
        XCTAssertGreaterThanOrEqual(time.frame.minY, words.frame.maxY - 1, "the time isn't under the words")
        try shot(app, "decision-largest")
        let actions = element(app, "minutes-card-actions")
        reveal(actions, in: app)
        XCTAssertTrue(actions.exists, app.debugDescription)
        try shot(app, "actions-largest")
    }

    // MARK: Helpers

    /// The bullets a topic card shows now.
    @MainActor private func bullets(_ topic: XCUIElement) -> [XCUIElement] {
        let points = topic.descendants(matching: .any).matching(identifier: "minutes-point").allElementsBoundByIndex
        let decisions = topic.descendants(matching: .any).matching(identifier: "minutes-decision").allElementsBoundByIndex
        return points + decisions
    }

    /// The made-up sprint review's page once its minutes are ready in `shape`, on Summary, the default.
    /// `searching`: the switch may sit below the screen (the accessibility sizes).
    @MainActor private func openSummary(_ shape: String, _ extra: [String] = [], searching: Bool = false) throws -> (XCUIApplication, String) {
        let hub = try mockHub()
        try minutesMode(hub, shape)
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "overview"] + extra
        app.launchPinned()
        let control = findSwitch(app, searching: searching)
        XCTAssertTrue(segment(control, "recording-view-summary", 0).isSelected, "the default isn't Summary:\n\(app.debugDescription)")
        return (app, hub)
    }

    /// The page's switch once the minutes are ready (at the accessibility sizes it can sit below the screen).
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

    /// Brings `element` on screen and clear of the bars: toward it, by slow drags, while the list has it (a card partly in
    /// view has all its rows), else down the page first, then back up if it was passed. An element under the status bar
    /// or the navigation bar reads as hittable, but a tap there scrolls the page to the top or hits the bar instead.
    @MainActor private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        let window = app.windows.firstMatch.frame
        let band = (window.minY + window.height * 0.2)...(window.minY + window.height * 0.8)
        let middle = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        var swipes = 0
        while !(element.exists && element.isHittable && band.contains(element.frame.midY)), swipes < 24 {
            if element.exists {
                let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: element.frame.midY < band.lowerBound ? 0.75 : 0.25))
                middle.press(forDuration: 0.1, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.2)
            } else if swipes < 10 {
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

    /// Back up to the switch at the top of the page.
    @MainActor private func back(to control: XCUIElement, in app: XCUIApplication) {
        var swipes = 0
        while !(control.exists && control.isHittable), swipes < 16 { app.swipeDown(); swipes += 1 }
        while !(control.exists && control.isHittable), swipes < 24 { app.swipeUp(velocity: .slow); swipes += 1 }
        XCTAssertTrue(control.exists, "no switch:\n\(app.debugDescription)")
    }

    /// `POST /mock/recording_minutes {"mode": "ready", "shape": shape}`.
    private func minutesMode(_ hub: String, _ shape: String) throws {
        var request = URLRequest(url: URL(string: hub + "/mock/recording_minutes")!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: ["mode": "ready", "shape": shape])
        var status = 0
        let done = expectation(description: "mode")
        URLSession.shared.dataTask(with: request) { _, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
        XCTAssertEqual(status, 200, "the mock doesn't take shape \(shape)")
    }

    /// `GET path` on the mock hub as JSON.
    private func get(_ hub: String, _ path: String) throws -> [String: Any] {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        var body = Data()
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: request) { data, _, _ in
            body = data ?? Data()
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return try JSONSerialization.jsonObject(with: body) as? [String: Any] ?? [:]
    }

    /// The minutes as the mock hub holds them for the made-up meeting.
    private func minutes(_ hub: String) throws -> [String: Any] {
        let json = try get(hub, "/api/v1/kb/recordings/00000000-0000-0000-0000-000000006563")
        return (json["detail"] as? [String: Any])?["minutes"] as? [String: Any] ?? [:]
    }

    private func minutesMarkdown(_ hub: String) throws -> String {
        try minutes(hub)["markdown"] as? String ?? ""
    }

    private func minutesActions(_ hub: String) throws -> [[String: Any]] {
        try minutes(hub)["actions"] as? [[String: Any]] ?? []
    }

    /// Whether the hub's to-do `id` comes to be done (`is: true`) or open, within a few seconds (the app writes it after
    /// the circle has changed).
    private func todoDone(_ hub: String, _ id: String, is done: Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(5)
        repeat {
            let todo = (try? get(hub, "/api/v1/todos/" + id))?["todo"] as? [String: Any]
            let doneAt = todo?["done_at"] as? String
            if todo != nil, (doneAt?.isEmpty == false) == done { return true }
            usleep(300_000)
        } while Date.now < deadline
        return false
    }

    /// The meeting thread's box (Talk about this meeting), where what Copy put on the pasteboard is pasted to be read.
    @MainActor private func composer(_ app: XCUIApplication) -> XCUIElement {
        element(app, "meeting-ask-field")
    }

    /// The edit menu's item (a menu item or a button, by the system's version).
    @MainActor private func menuItem(_ app: XCUIApplication, _ label: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "(elementType == %d OR elementType == %d) AND label == %@",
            XCUIElement.ElementType.menuItem.rawValue, XCUIElement.ElementType.button.rawValue, label)).firstMatch
    }

    /// What the app put on the pasteboard: the page's Talk about this meeting opens the meeting's thread, and the copy is
    /// pasted into its box from the edit menu, a paste the user taps, which asks nothing (`SelectTextTests.pasted`).
    /// Nothing is sent, and the box's words are the page's own state, gone with the app. (The chat's own box is out of
    /// reach here: going back to it opens Recordings again.)
    @MainActor private func pasted(_ app: XCUIApplication) -> String {
        let talk = element(app, "recording-talk"), field = composer(app)
        // Again if a tap went to the list still moving under it.
        for _ in 0..<3 where !field.exists {
            reveal(talk, in: app)
            XCTAssertTrue(talk.exists, "no Talk about this meeting:\n\(app.debugDescription)")
            sleep(1)
            talk.tapCenter()
            _ = field.waitForExistence(timeout: 5)
        }
        XCTAssertTrue(field.exists, "no thread to paste into:\n\(app.debugDescription)")
        let deadline = Date.now.addingTimeInterval(30)
        while !field.isEnabled, Date.now < deadline { usleep(250_000) }
        XCTAssertTrue(field.isEnabled, "the thread's box takes no words:\n\(app.debugDescription)")
        field.tap()
        field.press(forDuration: 1.0)
        let paste = menuItem(app, "粘贴")
        XCTAssertTrue(paste.waitForExistence(timeout: 3), "no 粘贴 in the box's edit menu:\n\(app.debugDescription)")
        paste.tap()
        sleep(1)
        return field.value as? String ?? ""
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/overview-\(appearance)-\(name).png"))
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs its own mock hub") }
        return hub
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }
}
