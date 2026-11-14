import XCTest

/// The highlights on the iPhone's meeting page (#3862 phase 2b; contract docs/assistant/meeting-minutes.md「Phase 2b:
/// highlights」 and 「The meeting card」, the phone's side docs/apple/recording.md「Highlights (phase 2b)」), end to end
/// against a mock hub (PERSONA_MOCK_HUB): a room meeting started with the highlights shows the room's highlights at the
/// top of its page, earliest first, each time opening the transcript at its minute with those lines lit, and Full
/// Transcript in the card (no transcript lines on it, nor Live Transcript); before the first a waiting line while Persona
/// attends; with the keyboard up only the newest highlight, or the waiting line; a hub without the flag, a meeting started
/// without it on a hub with it, a room that is unavailable and Persona turned off before the first highlights keep the
/// Live box; at the largest text the card is as at xxxLarge (#6813). A made-up meeting stands in for the microphone
/// (`-showMeetingSample room-highlights`, a line in each of fourteen minutes): its lines reach the hub as a real one's do,
/// and once they are there and the room made its own highlight from them, the test sets the highlights as the room's
/// tool would (`POST /mock/meeting_highlights` with each highlight's `at`, which the mock resolves to that line's minute).
/// Every look starts from a fresh mock (`POST /mock/recording_minutes`), with `/mock/meeting_highlights` switching the
/// phase on or off; every call to the mock must answer 2xx (a mock without the phase fails loudly). The app runs in zh-Hans:
/// elements are found by their identifiers. PERSONA_SHOTS=<dir> saves highlights-<look>-<name>.png (`-ui.scheme`).
final class MeetingHighlightsUITests: XCTestCase {
    private let live = "00000000-0000-0000-0000-000000000002"

    /// The highlights the tests set, in the order sent (not the order shown): each `at` is the start of a line of
    /// `RecordingSamples.highlightsLines`, alone in its minute.
    private static let set: [(text: String, at: Double)] = [
        ("张三周五前出两种定价方案的测算。", 1050),
        ("这个版本保留 4 MB 的导出上限。", 225),
        ("待定：招聘计划由谁负责。", 1270),
        ("供应商续约到三季度，下周再核成本。", 620),
    ]
    /// What the page shows, earliest first: each time and words.
    private static let shown: [(clock: String, text: String)] = [
        ("3:45", "这个版本保留 4 MB 的导出上限。"),
        ("10:20", "供应商续约到三季度，下周再核成本。"),
        ("17:30", "张三周五前出两种定价方案的测算。"),
        ("21:10", "待定：招聘计划由谁负责。"),
    ]
    /// The made-up meeting's lines: how many reach the hub.
    private static let lineCount = 14
    /// The words the mock's room makes its own highlight of after the batch: the batch's last line (`highlights_made_up`).
    private static let roomsHighlight = "今天先到这里"
    /// How long the slow room takes to come up (`room_seconds`): long enough for the page to open and show its waiting line.
    private static let slowRoom = 40
    /// Lines only the transcript has, never the words of a highlight here: two of the newest three, which phase 2's Live
    /// box and the card's removed Live Transcript showed, and the first. The card must show none of them.
    private static let transcriptOnly = ["还有招聘计划，目前没有人负责", "招聘计划谁来负责，下次会上再定", "好，我们开始"]

    override func setUpWithError() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        try fresh(hub)
    }

    override func tearDownWithError() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        try post(hub, "/mock/meeting_conversation", ["on": true, "room": "ok", "end_seconds": 180])
        try post(hub, "/mock/meeting_attendance", ["on": true, "default": "off"])
        try post(hub, "/mock/meeting_live_view", ["on": true])
        try post(hub, "/mock/meeting_highlights", ["on": true])
    }

    /// (a) The room's highlights at the top of the page: all four, earliest first, each with its time; the Persona part
    /// under it as phase 1 has it (the thread's header, the composer), Full Transcript in the card at its trailing side
    /// under the highlights, and no Live box, waiting line, Live Transcript or line of the transcript.
    @MainActor func testTheHighlightsShowAtTheTopInTimeOrder() throws {
        let hub = try mockHub()
        for look in ["light", "dark"] {
            if look != "light" { try fresh(hub) }
            let app = try highlightsPage(hub, look: look)
            let card = element(app, "meeting-highlights")
            let header = element(app, "meeting-thread-header")
            XCTAssertLessThanOrEqual(card.frame.maxY, header.frame.minY + 1, "\(look): the card isn't above the thread")
            XCTAssertTrue(element(app, "meeting-ask-field").exists, "\(look): no composer:\n\(app.debugDescription)")
            XCTAssertFalse(element(app, "meeting-live").exists, "\(look): the Live box beside the highlights")
            XCTAssertFalse(element(app, "meeting-highlights-waiting").exists, "\(look): the waiting line beside the highlights")
            assertNoTranscript(app, card, look)
            let transcript = card.descendants(matching: .any).matching(identifier: "meeting-transcript").firstMatch
            XCTAssertTrue(transcript.exists && transcript.isHittable, "\(look): Full Transcript isn't one tap away:\n\(app.debugDescription)")
            let last = rows(app).element(boundBy: Self.shown.count - 1)
            XCTAssertGreaterThanOrEqual(transcript.frame.minY, last.frame.maxY - 1, "\(look): Full Transcript isn't under the highlights")
            XCTAssertGreaterThan(transcript.frame.minX, card.frame.midX, "\(look): Full Transcript isn't at the trailing side")
            try shot(app, "card", look: look)
            // Full Transcript pushes the whole transcript; Back returns to the card.
            transcript.tap()
            XCTAssertTrue(element(app, "recording-timeline-sentence").waitForExistence(timeout: 5),
                          "\(look): Full Transcript opened no transcript:\n\(app.debugDescription)")
            app.navigationBars.buttons.element(boundBy: 0).tap()
            XCTAssertTrue(element(app, "meeting-highlights").waitForExistence(timeout: 5), "\(look): Back didn't return:\n\(app.debugDescription)")
            app.terminate()
        }
    }

    /// (b) A highlight's time pushes the transcript scrolled to its minute, its line lit for a moment (and selected for
    /// VoiceOver); Back returns to the card. Twice: a later minute, then an earlier one. The line stays lit two seconds, so
    /// it is read in one snapshot and shot as soon as it is on the screen, before anything else.
    @MainActor func testAHighlightsTimeOpensTheTranscriptAtItsMinute() throws {
        let hub = try mockHub()
        for look in ["light", "dark"] {
            if look != "light" { try fresh(hub) }
            let app = try highlightsPage(hub, look: look)
            for (index, words) in [(3, "招聘计划谁来负责"), (0, "保留 4 MB 的导出上限，发版说明")] {
                let time = rows(app).element(boundBy: index).buttons["meeting-highlight-time"]
                XCTAssertTrue(time.waitForExistence(timeout: 5), "\(look): no time on highlight \(index):\n\(app.debugDescription)")
                let window = app.windows.firstMatch.frame
                time.tap()
                let lit = element(app, "recording-timeline-sentence-lit")
                XCTAssertTrue(lit.waitForExistence(timeout: 3), "\(look): no line lit:\n\(app.debugDescription)")
                // The page scrolls to it as it lights it: the first snapshot with the line inside the window.
                let seen = onScreen(lit, in: window, within: 1.5)
                if index == 3 { try shot(app, "transcript-lit", look: look, quick: true) }
                guard let seen else {
                    XCTFail("\(look): the lit line never was on the screen:\n\(app.debugDescription)")
                    break
                }
                XCTAssertTrue(seen.label.contains(words), "\(look): the lit line is \(seen.label), not the minute's \(words)")
                // Selected while lit: one run in a dozen read the new name a beat before the selected state, so the state
                // is asked again for up to a second, still inside the two seconds the line stays lit.
                let selected = seen.isSelected || waitFor(1) { (try? lit.snapshot())?.isSelected == true }
                XCTAssertTrue(selected, "\(look): the lit line isn't selected for VoiceOver")
                XCTAssertTrue(lit.waitForNonExistence(timeout: 6), "\(look): the line stays lit")
                app.navigationBars.buttons.element(boundBy: 0).tap()
                XCTAssertTrue(element(app, "meeting-highlights").waitForExistence(timeout: 5), "\(look): Back didn't return:\n\(app.debugDescription)")
            }
            app.terminate()
        }
    }

    /// (d) With the keyboard up the card keeps only its newest highlight (the latest at), without Full Transcript and
    /// with no Live box or line of the transcript, and once it goes down every highlight is back with Full Transcript.
    @MainActor func testWithTheKeyboardUpOnlyTheNewestHighlightStays() throws {
        let hub = try mockHub()
        for look in ["light", "dark"] {
            if look != "light" { try fresh(hub) }
            let app = try highlightsPage(hub, look: look)
            let field = element(app, "meeting-ask-field")
            XCTAssertTrue(waitFor(15) { field.isEnabled }, "\(look): the composer is off: \(app.debugDescription)")
            field.tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "\(look): no keyboard")
            let card = element(app, "meeting-highlights")
            XCTAssertTrue(waitFor(5) { self.rows(app).count == 1 }, "\(look): with the keyboard the card has \(rows(app).count) highlights:\n\(app.debugDescription)")
            let newest = rows(app).firstMatch
            XCTAssertTrue(newest.buttons["meeting-highlight-time"].label.contains(Self.shown[3].clock),
                          "\(look): not the newest: \(newest.debugDescription)")
            XCTAssertTrue(text(newest, Self.shown[3].text).exists, "\(look): not the newest's words: \(newest.debugDescription)")
            XCTAssertTrue(waitFor(5) { !card.descendants(matching: .any).matching(identifier: "meeting-transcript").firstMatch.exists },
                          "\(look): Full Transcript stays with the keyboard")
            XCTAssertFalse(element(app, "meeting-live").exists, "\(look): a Live box with the keyboard:\n\(app.debugDescription)")
            assertNoTranscript(app, card, look)
            try shot(app, "keyboard", look: look)

            // The keyboard down: a drag from inside the thread down into the keyboard, which the thread's scroll view
            // dismisses interactively. A thread at its top hands a drag down to the sheet (which hides the page, also when
            // the scroll reaches the top on the way), so the batch's lines are opened first, making the thread taller than
            // its view, and it is scrolled up to them (they open at their place, not the end); the drag, from the bottom
            // of what shows of the thread just into the keyboard, is then shorter than what is above. Then every highlight
            // is back.
            let lines = element(app, "meeting-thread-lines")
            XCTAssertTrue(lines.waitForExistence(timeout: 10), "\(look): no batch of lines in the thread:\n\(app.debugDescription)")
            lines.tap()
            let keyboard = app.keyboards.firstMatch
            XCTAssertTrue(keyboard.exists, "\(look): opening the lines put the keyboard down")
            sleep(1)
            // The thread's scroll view runs under the bar (its inset): what shows of it ends where the bar begins, with
            // Just Me on top while the keyboard is up.
            let thread = app.scrollViews.containing(.any, identifier: "meeting-thread-lines").firstMatch
            let bar = element(app, "meeting-just-me")
            XCTAssertTrue(thread.exists && bar.exists, "\(look): no thread scroll view or bar:\n\(app.debugDescription)")
            let origin = app.coordinate(withNormalizedOffset: .zero)
            for _ in 0..<2 {
                let view = thread.frame
                origin.withOffset(CGVector(dx: view.midX, dy: bar.frame.minY - 20))
                    .press(forDuration: 0.05, thenDragTo: origin.withOffset(CGVector(dx: view.midX, dy: view.minY + 10)),
                           withVelocity: .fast, thenHoldForDuration: 0)
            }
            sleep(1)
            XCTAssertTrue(keyboard.exists, "\(look): scrolling the thread up put the keyboard down")
            let opened = XCTAttachment(screenshot: app.screenshot())
            opened.name = "highlights-\(look)-keyboard-lines-open"
            opened.lifetime = .keepAlways
            add(opened)
            var down = false
            for _ in 0..<3 where !down {
                let view = thread.frame, top = keyboard.frame.minY
                let from = CGPoint(x: view.midX, y: bar.frame.minY - 20), to = CGPoint(x: view.midX, y: top + 24)
                // Never a drag the sheet would take: it would hide the page, and the test would go on without it.
                guard from.y > view.minY, view.minY - lines.frame.minY > to.y - from.y else {
                    XCTFail("\(look): no room for the drag: the thread \(view), its lines \(lines.frame), the bar \(bar.frame)")
                    break
                }
                origin.withOffset(CGVector(dx: from.x, dy: from.y))
                    .press(forDuration: 0.05, thenDragTo: origin.withOffset(CGVector(dx: to.x, dy: to.y)), withVelocity: .fast, thenHoldForDuration: 0)
                down = keyboard.waitForNonExistence(timeout: 3)
            }
            XCTAssertTrue(down, "\(look): dragging the thread didn't put the keyboard down:\n\(app.debugDescription)")
            XCTAssertTrue(waitFor(5) { self.rows(app).count == Self.shown.count },
                          "\(look): the keyboard down, the card has \(rows(app).count) highlights:\n\(app.debugDescription)")
            XCTAssertTrue(waitFor(5) { card.descendants(matching: .any).matching(identifier: "meeting-transcript").firstMatch.exists },
                          "\(look): Full Transcript isn't back:\n\(app.debugDescription)")
            XCTAssertFalse(element(app, "meeting-live").exists, "\(look): a Live box beside the card")
            app.terminate()
        }
    }

    /// (e) A hub without the highlights (`{"on": false}` before the launch): the phone reads the flag when a meeting starts,
    /// so the meeting starts with the live view alone (`room-taking-part`, as a start on that hub snapshots it);
    /// `GET …/live` carries no highlights, and the page keeps phase 2's Live box with the phone's own lines: no Highlights
    /// card and no waiting line, also once the room had its turn after the batch.
    @MainActor func testAHubWithoutTheHighlightsKeepsTheLiveBox() throws {
        let hub = try mockHub()
        try post(hub, "/mock/meeting_highlights", ["on": false])
        let app = try meetingPage(hub, look: "light", sample: "room-taking-part")
        XCTAssertTrue(element(app, "meeting-live").waitForExistence(timeout: 10), app.debugDescription)
        try waitForTheLines(hub, lines: 1, roomsHighlight: false)
        XCTAssertNil(try liveRead(hub)["highlights"], "the hub without the flag sent highlights: \(String(describing: try? liveRead(hub)))")
        // The room sets its topics after the batch: once the page shows them it has followed the room's turn.
        XCTAssertTrue(element(app, "meeting-topics").waitForExistence(timeout: 20), "the page never followed the room:\n\(app.debugDescription)")
        sleep(3)
        XCTAssertFalse(element(app, "meeting-highlights").exists, "a Highlights card on a hub without them:\n\(app.debugDescription)")
        XCTAssertFalse(element(app, "meeting-highlights-waiting").exists, "a waiting line on a hub without the highlights")
        XCTAssertTrue(element(app, "meeting-live").exists, "no Live box:\n\(app.debugDescription)")
        try shot(app, "no-highlights", look: "light")
    }

    /// (f) At the largest text size (AX5), light and dark, the card is drawn as at xxxLarge, where #6813 caps the interface
    /// (`docs/design/DESIGN_SYSTEM.md` 6.2: a highlight's time is a chip, one of a card's controls): as at the regular
    /// sizes, each time sits before its words, beside them and not under them, every highlight and Full Transcript (under
    /// them, at the trailing side) are on the screen without a scroll, the thread's header is under the card, and there is
    /// no Live box. The card and each highlight are as tall as at xxxLarge itself (`cap`, the first look).
    @MainActor func testLargestTextSizeDrawsTheCardAsAtTheCap() throws {
        let hub = try mockHub()
        var atTheCap: [CGFloat] = []
        for look in ["cap", "large", "large-dark"] {
            if look != "cap" { try fresh(hub) }
            let app = try highlightsPage(hub, look: look)
            let window = app.windows.firstMatch.frame
            let card = element(app, "meeting-highlights")
            var heights = [card.frame.height]
            for (n, shown) in Self.shown.enumerated() {
                let row = rows(app).element(boundBy: n)
                let time = row.buttons["meeting-highlight-time"]
                let words = text(row, String(shown.text.prefix(6)))
                XCTAssertTrue(words.exists && window.contains(time.frame) && window.contains(words.frame),
                              "\(look): highlight \(n) isn't on the screen:\n\(app.debugDescription)")
                XCTAssertLessThanOrEqual(time.frame.maxX, words.frame.minX + 1, "\(look): highlight \(n)'s time isn't before its words")
                XCTAssertLessThan(time.frame.minY, words.frame.maxY - 1, "\(look): highlight \(n)'s time is under its words")
                heights.append(row.frame.height)
            }
            let transcript = card.descendants(matching: .any).matching(identifier: "meeting-transcript").firstMatch
            XCTAssertTrue(transcript.exists && window.contains(transcript.frame), "\(look): Full Transcript isn't on the screen:\n\(card.debugDescription)")
            let last = rows(app).element(boundBy: Self.shown.count - 1)
            XCTAssertGreaterThanOrEqual(transcript.frame.minY, last.frame.maxY - 1, "\(look): Full Transcript isn't under the highlights")
            XCTAssertGreaterThan(transcript.frame.minX, card.frame.midX, "\(look): Full Transcript isn't at the trailing side")
            let header = element(app, "meeting-thread-header")
            XCTAssertGreaterThanOrEqual(header.frame.minY, card.frame.maxY - 1, "\(look): the thread's header isn't under the card")
            XCTAssertFalse(element(app, "meeting-live").exists, "\(look): a Live box at the largest text")
            try shot(app, "card", look: look)
            if look == "cap" {
                atTheCap = heights
            } else {
                for (n, (height, capped)) in zip(heights, atTheCap).enumerated() {
                    XCTAssertEqual(height, capped, accuracy: 0.5,
                                   "\(look): \(n == 0 ? "the card" : "highlight \(n - 1)") is \(height) pt tall, \(capped) pt at xxxLarge")
                }
            }
            app.terminate()
        }
    }

    /// (g) A meeting started without the highlights keeps the Live box on a hub with them, even once its room made some:
    /// the meeting keeps the flag it started with (`RecordingAttendance.highlights`). `room-taking-part` is started with
    /// the live view only, and the mock's room, up with the highlights' tool, makes a highlight from its batch.
    @MainActor func testAMeetingStartedWithoutTheHighlightsKeepsTheLiveBox() throws {
        let hub = try mockHub()
        let app = try meetingPage(hub, look: "light", sample: "room-taking-part")
        XCTAssertTrue(element(app, "meeting-live").waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(waitFor(40) { !((try? self.highlights(hub)) ?? []).isEmpty },
                      "the room never made a highlight: \(String(describing: try? liveRead(hub)))")
        // The room set its topics just before (the same batch): once the page shows them it has followed the room's turn.
        XCTAssertTrue(element(app, "meeting-topics").waitForExistence(timeout: 20), "the page never followed the room:\n\(app.debugDescription)")
        sleep(3)
        XCTAssertFalse(element(app, "meeting-highlights").exists, "highlights for a meeting started without them:\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "meeting-live").exists, "no Live box:\n\(app.debugDescription)")
        try shot(app, "started-without", look: "light")
    }

    /// (h) Before the room's first highlights, while Persona attends, the card holds its waiting line (and Full
    /// Transcript), not the Live box nor a line of the transcript; once the room makes its first highlight the line goes
    /// and the highlight shows. The
    /// room comes up slowly (`{"room": "slow"}`), so the page opens before it has fed the room; then it gets the meeting
    /// so far and makes its own highlight of the batch's last line.
    @MainActor func testBeforeTheFirstHighlightsTheCardWaits() throws {
        let hub = try mockHub()
        for look in ["light", "dark"] {
            if look != "light" { try fresh(hub) }
            try post(hub, "/mock/meeting_conversation", ["room": "slow", "room_seconds": Self.slowRoom])
            let app = try meetingPage(hub, look: look)
            let waiting = element(app, "meeting-highlights-waiting")
            XCTAssertTrue(waiting.waitForExistence(timeout: 10), "\(look): no waiting line:\n\(app.debugDescription)")
            let card = element(app, "meeting-highlights")
            XCTAssertTrue(card.exists, "\(look): the waiting line isn't on the Highlights card")
            XCTAssertTrue(((try? highlights(hub)) ?? []).isEmpty, "\(look): the room already has highlights: \(String(describing: try? highlights(hub)))")
            XCTAssertEqual(rows(app).count, 0, "\(look): highlights beside the waiting line")
            XCTAssertFalse(element(app, "meeting-live").exists, "\(look): the Live box while Persona attends:\n\(app.debugDescription)")
            XCTAssertTrue(card.descendants(matching: .any).matching(identifier: "meeting-transcript").firstMatch.exists,
                          "\(look): no Full Transcript under the waiting line")
            assertNoTranscript(app, card, look)
            try shot(app, "waiting", look: look)

            // The room comes up, is fed the meeting so far and makes its first highlight: the line gives way to it.
            try waitForTheLines(hub)
            let made = (try? highlights(hub)) ?? []
            XCTAssertFalse(made.isEmpty, "\(look): the room made no highlight")
            XCTAssertTrue(waitFor(20) { self.rows(app).count == made.count },
                          "\(look): \(rows(app).count) highlights on the card, the room made \(made.count):\n\(app.debugDescription)")
            XCTAssertTrue(text(card, Self.roomsHighlight).exists, "\(look): not the room's highlight:\n\(card.debugDescription)")
            XCTAssertTrue(waiting.waitForNonExistence(timeout: 5), "\(look): the waiting line stays beside the highlight")
            XCTAssertFalse(element(app, "meeting-live").exists, "\(look): a Live box beside the highlight")
            app.terminate()
        }
    }

    /// (i) A room that can't start (`{"room": "unavailable"}`, its problem `machine_offline`): Persona can't take part, so
    /// with no highlights the top card is phase 2's Live box under the room's problem, never the waiting line, also once
    /// the page has read the room's state.
    @MainActor func testAnUnavailableRoomKeepsTheLiveBox() throws {
        let hub = try mockHub()
        try post(hub, "/mock/meeting_conversation", ["room": "unavailable"])
        let app = try meetingPage(hub, look: "light")
        let problem = element(app, "meeting-room-problem")
        XCTAssertTrue(problem.waitForExistence(timeout: 20), "no room problem:\n\(app.debugDescription)")
        XCTAssertEqual((try liveRead(hub)["room"] as? [String: Any])?["state"] as? String, "unavailable")
        let box = element(app, "meeting-live")
        XCTAssertTrue(box.waitForExistence(timeout: 5), "no Live box under the room's problem:\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "meeting-highlights").waitForNonExistence(timeout: 5), "a Highlights card with the room unavailable")
        XCTAssertFalse(element(app, "meeting-highlights-waiting").exists, "a waiting line with the room unavailable")
        XCTAssertGreaterThanOrEqual(box.frame.minY, problem.frame.maxY - 1, "the Live box isn't under the room's problem")
        sleep(3)
        XCTAssertTrue(((try? highlights(hub)) ?? []).isEmpty, "an unavailable room made highlights: \(String(describing: try? highlights(hub)))")
        XCTAssertTrue(box.exists && !element(app, "meeting-highlights-waiting").exists, "the card changed:\n\(app.debugDescription)")
        try shot(app, "room-unavailable", look: "light")
        app.terminate()
    }

    /// (j) The waiting line with the keyboard up, then Persona turned off before the first highlights. The room here
    /// starts without the highlights' tool (`{"on": false}` after the made-up start took the flag, as a room started
    /// before the hub had it), so the meeting waits for as long as the test looks, with no race against the room. The
    /// card holds the waiting line and no transcript; with the keyboard up it keeps the line alone, without Full
    /// Transcript, and is no Live box; once the page menu turns Persona off it is phase 2's Live box, with no waiting line.
    @MainActor func testTheWaitingLineFoldsWithTheKeyboardAndGoesWithTheRole() throws {
        let hub = try mockHub()
        for look in ["light", "dark"] {
            if look != "light" { try fresh(hub) }
            try post(hub, "/mock/meeting_highlights", ["on": false])
            let app = try meetingPage(hub, look: look)
            let waiting = element(app, "meeting-highlights-waiting")
            XCTAssertTrue(waiting.waitForExistence(timeout: 10), "\(look): no waiting line:\n\(app.debugDescription)")
            let card = element(app, "meeting-highlights")
            // The room had its turn after the batch (its topics), and set no highlight.
            XCTAssertTrue(element(app, "meeting-topics").waitForExistence(timeout: 20), "\(look): the page never followed the room:\n\(app.debugDescription)")
            XCTAssertTrue(waiting.exists && rows(app).count == 0, "\(look): not waiting after the room's turn:\n\(app.debugDescription)")
            assertNoTranscript(app, card, look)

            let field = element(app, "meeting-ask-field")
            XCTAssertTrue(waitFor(15) { field.isEnabled }, "\(look): the composer is off: \(app.debugDescription)")
            field.tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "\(look): no keyboard")
            XCTAssertTrue(waitFor(5) { !card.descendants(matching: .any).matching(identifier: "meeting-transcript").firstMatch.exists },
                          "\(look): Full Transcript stays with the keyboard:\n\(app.debugDescription)")
            XCTAssertTrue(waiting.exists, "\(look): the waiting line went with the keyboard:\n\(app.debugDescription)")
            XCTAssertEqual(rows(app).count, 0, "\(look): highlights beside the waiting line")
            XCTAssertFalse(element(app, "meeting-live").exists, "\(look): a Live box with the keyboard while Persona attends:\n\(app.debugDescription)")
            assertNoTranscript(app, card, look)
            try shot(app, "waiting-keyboard", look: look)

            // Persona off before any highlights: phase 2's Live box in the card's place, the waiting line gone.
            turnPersonaOff(app, look)
            XCTAssertTrue(element(app, "meeting-live").waitForExistence(timeout: 10), "\(look): Persona off, no Live box:\n\(app.debugDescription)")
            XCTAssertTrue(waiting.waitForNonExistence(timeout: 5), "\(look): the waiting line stays with Persona off")
            XCTAssertFalse(element(app, "meeting-highlights").exists, "\(look): a Highlights card with Persona off and none")
            try shot(app, "role-off", look: look)
            app.terminate()
        }
    }

    // MARK: Helpers: the app

    /// The made-up meeting's page with the highlights set and shown: all four, in time order, each with its time.
    @MainActor private func highlightsPage(_ hub: String, look: String) throws -> XCUIApplication {
        let app = try meetingPage(hub, look: look)
        try waitForTheLines(hub)
        try setHighlights(hub)
        XCTAssertTrue(element(app, "meeting-highlights").waitForExistence(timeout: 20),
                      "\(look): no highlights on the page; the hub has \(String(describing: try? self.highlights(hub))):\n\(app.debugDescription)")
        XCTAssertTrue(waitFor(10) { self.rows(app).count == Self.shown.count }, "\(look): \(rows(app).count) highlights:\n\(app.debugDescription)")
        var above = -CGFloat.infinity
        for (n, expected) in Self.shown.enumerated() {
            let row = rows(app).element(boundBy: n)
            XCTAssertTrue(text(row, expected.text).exists, "\(look): highlight \(n) isn't \(expected.text):\n\(row.debugDescription)")
            let time = row.buttons["meeting-highlight-time"]
            XCTAssertTrue(time.exists && time.label.contains(expected.clock), "\(look): highlight \(n)'s time is \(time.label), not \(expected.clock)")
            XCTAssertGreaterThan(row.frame.minY, above, "\(look): highlight \(n) isn't under the one before")
            above = row.frame.minY
        }
        return app
    }

    /// The made-up meeting's page (`sample`: `-showMeetingSample`, the one started with the highlights unless named).
    @MainActor private func meetingPage(_ hub: String, look: String, sample: String = "room-highlights") throws -> XCUIApplication {
        let app = XCUIApplication()
        // `-openChatsFresh`: the mock makes the room under the same id for the same made-up recording in every test.
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-openChatsFresh", "YES",
                               "-showMeetingSample", sample, "-ui.scheme", look.hasSuffix("dark") ? "dark" : "light"]
            + Self.textSize(look)
        app.launchPinned()
        // The app's icon follows the appearance: switching it, the system says so in an alert over the page.
        _ = waitFor(4) { self.dismissIconNotice(app) }
        if !element(app, "meeting-thread-header").waitForExistence(timeout: 20) {
            // That alert, up while the sheet was being presented, can keep the sheet from showing: once more, the icon
            // settled now.
            app.terminate()
            app.launchPinned()
            _ = waitFor(4) { self.dismissIconNotice(app) }
        }
        XCTAssertTrue(element(app, "meeting-thread-header").waitForExistence(timeout: 20), "no meeting page:\n\(app.debugDescription)")
        return app
    }

    /// The launch arguments for a look's text size: the system's, with the app's own Text Size at Standard so that only the
    /// system's counts. `large…` the largest, AX5; `cap` xxxLarge, where #6813 caps the interface; other looks the
    /// simulator's own.
    private static func textSize(_ look: String) -> [String] {
        let size = look.hasPrefix("large") ? "AccessibilityXXXL" : look == "cap" ? "XXXL" : nil
        return size.map { ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategory\($0)", "-ui.textSize", "standard"] } ?? []
    }

    /// Taps away the system's "You have changed the icon" alert, if it is up; true when it was.
    @MainActor @discardableResult private func dismissIconNotice(_ app: XCUIApplication) -> Bool {
        let notice = NSPredicate(format: "label CONTAINS[c] 'icon' OR label CONTAINS '图标'")
        for source in [app, XCUIApplication(bundleIdentifier: "com.apple.springboard")] {
            let alert = source.alerts.containing(notice).firstMatch
            if alert.exists, alert.buttons.firstMatch.exists {
                alert.buttons.firstMatch.tap()
                return true
            }
        }
        return false
    }

    /// The card shows no transcript: no Live Transcript anywhere on the page, and none of the lines only the transcript
    /// has (`transcriptOnly`).
    @MainActor private func assertNoTranscript(_ app: XCUIApplication, _ card: XCUIElement, _ look: String,
                                               file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(card.exists, "\(look): no Highlights card:\n\(app.debugDescription)", file: file, line: line)
        XCTAssertFalse(element(app, "meeting-live-transcript").exists, "\(look): Live Transcript on the page", file: file, line: line)
        for words in Self.transcriptOnly {
            XCTAssertFalse(text(card, words).exists, "\(look): the card shows the transcript's line \(words):\n\(card.debugDescription)",
                           file: file, line: line)
        }
    }

    /// Persona off from the page menu's role picker (`meeting-role`, 「关」 in zh-Hans): its choices are in the menu, or under
    /// the picker when the system shows it as a submenu.
    @MainActor private func turnPersonaOff(_ app: XCUIApplication, _ look: String) {
        let menu = element(app, "meeting-page-menu")
        XCTAssertTrue(menu.waitForExistence(timeout: 5), "\(look): no page menu:\n\(app.debugDescription)")
        menu.tapCenter()
        let off = app.buttons["关"].firstMatch
        if !off.waitForExistence(timeout: 3) {
            let role = element(app, "meeting-role")
            if role.exists { role.tap() }
        }
        XCTAssertTrue(off.waitForExistence(timeout: 5), "\(look): no Off in the page menu:\n\(app.debugDescription)")
        off.tap()
    }

    @MainActor private func rows(_ app: XCUIApplication) -> XCUIElementQuery {
        app.descendants(matching: .any).matching(identifier: "meeting-highlight")
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    /// The element under `parent` whose label holds `words`.
    @MainActor private func text(_ parent: XCUIElement, _ words: String) -> XCUIElement {
        parent.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
    }

    /// `el` read in one snapshot (its label, frame and state together) as soon as it is inside `window`; nil when it
    /// never was within `seconds`.
    @MainActor private func onScreen(_ el: XCUIElement, in window: CGRect, within seconds: TimeInterval) -> XCUIElementSnapshot? {
        let deadline = Date.now.addingTimeInterval(seconds)
        repeat {
            if let seen = try? el.snapshot(), !seen.frame.isEmpty, window.contains(seen.frame) { return seen }
            usleep(100_000)
        } while Date.now < deadline
        return nil
    }

    private func waitFor(_ seconds: TimeInterval = 10, _ ok: () -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(seconds)
        while Date.now < deadline {
            if ok() { return true }
            usleep(250_000)
        }
        return ok()
    }

    /// A shot in a look this test chose itself (`-ui.scheme`, or the largest text), named by it. `quick`: at once, without
    /// looking for the icon alert first (dismissed at the launch), for what shows only for a moment.
    @MainActor private func shot(_ app: XCUIApplication, _ name: String, look: String, quick: Bool = false) throws {
        if !quick, dismissIconNotice(app) { sleep(1) }
        let image = app.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = "highlights-\(look)-\(name)"
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try image.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/highlights-\(look)-\(name).png"))
    }

    // MARK: Helpers: the mock hub

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        return hub
    }

    /// A fresh mock with every phase on: no recordings, rooms or highlights from before.
    private func fresh(_ hub: String) throws {
        try post(hub, "/mock/recording_minutes", ["mode": "pending"])
        try post(hub, "/mock/meeting_conversation", ["on": true, "room": "ok", "end_seconds": 180])
        try post(hub, "/mock/meeting_attendance", ["on": true, "default": "participant", "answer": "ok", "kb_read_only": false])
        try post(hub, "/mock/meeting_live_view", ["on": true])
        try post(hub, "/mock/meeting_highlights", ["on": true])
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    /// A call to the mock, which must answer 2xx: a mock without the route (404) or that refused it (400) fails here.
    private func post(_ hub: String, _ path: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: path)
        let answer = MockAnswer()
        URLSession.shared.dataTask(with: request) { data, response, _ in
            answer.status = (response as? HTTPURLResponse)?.statusCode ?? 0
            answer.body = data.map { String(decoding: $0, as: UTF8.self) } ?? ""
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
        XCTAssertTrue((200..<300).contains(answer.status), "POST \(path) answered \(answer.status): \(answer.body)")
    }

    /// `GET /api/v1/recordings/{id}/live` as the hub answers it (`from`: with the live log's lines from there).
    private func liveRead(_ hub: String, from: Int? = nil) throws -> [String: Any] {
        let query = from.map { "?from=\($0)" } ?? ""
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/recordings/\(live)/live\(query)")!)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    /// The recording's highlights as the hub has them (`highlights` of `GET …/live`).
    private func highlights(_ hub: String) throws -> [[String: Any]] {
        try liveRead(hub)["highlights"] as? [[String: Any]] ?? []
    }

    /// The made-up meeting's lines (`count`, the highlights sample's unless named) are in the hub's live log and its room
    /// is up; with the highlights on (`roomsHighlight`), the room has also had its own turn after the batch, which ends
    /// with its highlight from the batch's last line (the room is ready before it is fed, so ready alone doesn't say that
    /// turn is over).
    private func waitForTheLines(_ hub: String, lines count: Int = MeetingHighlightsUITests.lineCount, roomsHighlight: Bool = true) throws {
        let ready = waitFor(40) {
            guard let read = try? self.liveRead(hub, from: 0) else { return false }
            let lines = (read["lines"] as? [[String: Any]] ?? []).filter { !(($0["text"] as? String) ?? "").isEmpty }
            return (read["room"] as? [String: Any])?["state"] as? String == "ready" && lines.count >= count
        }
        XCTAssertTrue(ready, "the meeting's lines never reached the room: \(String(describing: try? liveRead(hub, from: 0)))")
        guard roomsHighlight else { return }
        let made = waitFor(20) {
            ((try? self.highlights(hub)) ?? []).contains { ($0["text"] as? String)?.hasPrefix(Self.roomsHighlight) == true }
        }
        XCTAssertTrue(made, "the room never made its own highlight: \(String(describing: try? highlights(hub)))")
    }

    /// The room sets the test's highlights (the mock's tool call, each `at` resolved to its line's minute), after the
    /// room's own turn (`waitForTheLines`), so nothing replaces them.
    private func setHighlights(_ hub: String) throws {
        let body: [String: Any] = ["id": live, "highlights": Self.set.map { ["text": $0.text, "at": $0.at] }]
        let texts = Set(Self.set.map(\.text))
        try post(hub, "/mock/meeting_highlights", body)
        XCTAssertTrue(waitFor(10) { Set(((try? self.highlights(hub)) ?? []).compactMap { $0["text"] as? String }) == texts },
                      "the hub never took the highlights: \(String(describing: try? highlights(hub)))")
    }
}

/// What the mock answered a call (`post`), written from URLSession's queue and read after the wait.
private final class MockAnswer: @unchecked Sendable {
    var status = 0
    var body = ""
}
