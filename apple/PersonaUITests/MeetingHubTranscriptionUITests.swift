import XCTest

/// Hub transcription on the iPhone (#3862 phase 5; contract docs/assistant/meeting-minutes.md「Phase 5: hub transcription」,
/// the phone's side docs/apple/recording.md「Hub transcription (phase 5)」), end to end against the mock hub's hub mode
/// (PERSONA_MOCK_HUB; docs/operations/testing.md "Meeting transcription": a scripted fixture, no speech service, silence
/// or any tone is audio enough). A made-up meeting stands in for the microphone (`-showMeetingSample room-hub`): a room
/// meeting listening in that asked for hub transcription, whose recorder's tap gets canned audio. Its upload asks for
/// `hub`, the `200` says so, the audio socket opens and the canned audio goes up it; the mock's lines come back to the
/// phone's transcription store, and a problem upstream makes the store fall back to the phone's own lines. The store is
/// read through its probe (`probe.meeting-transcription`, `-probes YES`); the mock's side through `GET
/// /mock/meeting_transcription`. Every test starts from a fresh mock (`POST /mock/recording_minutes`, the features on).
/// The UI (stage 2): the start offers On the Hub only while the hub lists the flag and can transcribe, Settings › Meetings
/// saves the default and the glossary on the hub, and the meeting page shows the hub's lines, then the phone's own with
/// the fallback notice, as does the Lock Screen, and after Stop the recording's page says how it was transcribed; with the
/// highlights (phase 2b) the card holds no line, the hub's lines are on the Full Transcript page, and the notice is on
/// both. The app runs in zh-Hans: elements are found by their identifiers.
/// PERSONA_SHOTS=<dir> saves hub-transcription-<appearance>-<name>.png (PERSONA_SHOTS_APPEARANCE names the appearance).
final class MeetingHubTranscriptionUITests: XCTestCase {
    private let live = "00000000-0000-0000-0000-000000000002"

    override func setUpWithError() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        try post(hub, "/mock/recording_minutes", ["mode": "pending"])
        try post(hub, "/mock/meeting_conversation", ["on": true, "room": "ok", "end_seconds": 180])
        try post(hub, "/mock/meeting_attendance", ["on": true, "default": "off", "answer": "ok", "kb_read_only": false])
        try post(hub, "/mock/meeting_transcription", Self.transcriptionOn)
        try put(hub, "/api/v1/assistant", Self.settingsAsTheyStart)
    }

    override func tearDownWithError() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        try post(hub, "/mock/meeting_transcription", Self.transcriptionOn)
        try post(hub, "/mock/meeting_attendance", ["on": true, "default": "off"])
        try put(hub, "/api/v1/assistant", Self.settingsAsTheyStart)
    }

    /// The hub's transcription settings as a fresh mock has them.
    private static var settingsAsTheyStart: [String: Any] {
        ["meeting_transcription_default": "device", "meeting_glossary": [String](), "meeting_transcription_extension": ""]
    }

    /// The mock's hub mode as it starts: on, ready, its upstream working, a line per 4 seconds of audio.
    private static var transcriptionOn: [String: Any] {
        ["on": true, "ready": true, "upstream": "ok", "fatal": "", "line_seconds": 4, "final_seconds": 2]
    }

    /// What the app's transcription store shows (`MeetingTranscription.Probe`).
    private struct Store: Decodable, CustomStringConvertible {
        var recording: String?
        var asked: String?
        var mode: String?
        var socket: String
        var lines: Int
        var interim: String?
        var problem: String?
        var stopped: String?
        var fellBack: Bool
        var phoneLines: Bool
        var description: String {
            "mode \(mode ?? "–") socket \(socket) lines \(lines) problem \(problem ?? "–") stopped \(stopped ?? "–") fellBack \(fellBack) phoneLines \(phoneLines)"
        }
    }

    /// A hub-mode meeting: the upload asks for `hub` with its timeline, the socket opens (start, role, audio) and the mock
    /// stores the audio; the hub's lines reach the store, which shows them; a problem upstream makes it fall back to the
    /// phone's own lines with the notice, and the hub's sentences coming again bring them back.
    @MainActor func testHubModeStreamsTheMeetingAndFallsBackOnAProblem() throws {
        let hub = try mockHub()
        let app = launch(hub, ["-showMeetingSample", "room-hub"])

        // The upload asked for the hub (the mock keeps the mode and the timeline with its hub mode, not with the upload).
        XCTAssertTrue(waitFor(30) {
            ((try? self.transcription(hub, self.live))?["transcription"] as? [String: Any])?["mode"] as? String == "hub"
        }, "the upload never asked for hub mode: \(String(describing: try? transcription(hub, live))); the store: "
            + "\(String(describing: app.probe("meeting-transcription", as: Store.self))); uploads: \(String(describing: try? json(hub, "/mock/recordings")["puts"]))")
        XCTAssertNotNil(try upload(hub, live)?["room"], "a room meeting's upload")

        // The socket: start first, the role, then the audio the mock stores.
        XCTAssertTrue(waitFor(40) {
            guard let r = try? self.transcription(hub, self.live) else { return false }
            return r["socket"] as? Bool == true && (r["bytes"] as? Int ?? 0) >= 4 * 32000
        }, "no audio reached the hub: \(String(describing: try? transcription(hub, live)))")
        let mock = try XCTUnwrap(try transcription(hub, live))
        let types = (mock["frames"] as? [[String: Any]] ?? []).compactMap { $0["type"] as? String }
        XCTAssertEqual(types.first, "start", "start is the first frame: \(types.prefix(5))")
        XCTAssertTrue(types.contains("role"), "the role goes at the start: \(types.prefix(5))")
        XCTAssertTrue(types.contains("binary"), "no audio frame: \(types.prefix(5))")
        XCTAssertEqual((mock["transcription"] as? [String: Any])?["mode"] as? String, "hub")
        let roles = ((mock["timeline"] as? [String: Any])?["roles"] as? [[String: Any]]) ?? []
        XCTAssertEqual(roles.first?["attendance"] as? String, "listener", "the hub has the meeting's role: \(roles)")

        // The hub's lines in the store, shown instead of the phone's.
        var store: Store?
        XCTAssertTrue(waitFor(40) {
            store = app.probe("meeting-transcription", as: Store.self)
            return store.map { $0.mode == "hub" && $0.socket == "ready" && $0.lines >= 1 } ?? false
        }, "the hub's lines never reached the store: \(String(describing: store))")
        XCTAssertEqual(store?.recording, live)
        XCTAssertEqual(store?.fellBack, false)
        XCTAssertEqual(store?.phoneLines, false, "the hub's lines are shown while it is up and healthy")

        // A problem upstream: the phone's own lines, with the notice.
        try post(hub, "/mock/meeting_transcription", ["upstream": "failed"])
        XCTAssertTrue(waitFor(30) {
            store = app.probe("meeting-transcription", as: Store.self)
            return store.map { $0.problem == "upstream_failed" && $0.fellBack && $0.phoneLines } ?? false
        }, "the store didn't fall back: \(String(describing: store))")
        XCTAssertEqual(store?.socket, "ready", "the audio still goes: the hub stores it")

        // Back: the hub's sentences come again, and with them its lines.
        try post(hub, "/mock/meeting_transcription", ["upstream": "ok"])
        XCTAssertTrue(waitFor(30) {
            store = app.probe("meeting-transcription", as: Store.self)
            return store.map { $0.problem == nil && !$0.fellBack && !$0.phoneLines } ?? false
        }, "the hub's lines never came back: \(String(describing: store))")
    }

    // MARK: The start

    /// With the flag and the hub ready, the start has a Transcription row (the hub's default, On This iPhone here); On the
    /// Hub changes the line under Record to hub mode's privacy text.
    @MainActor func testTheStartOffersOnTheHubWhileTheHubCanTranscribe() throws {
        let hub = try mockHub()
        let app = launchRecordings(hub)
        XCTAssertTrue(app.buttons["recording-meeting-start"].waitForExistence(timeout: 15), app.debugDescription)
        let row = element(app, "meeting-transcription-start")
        if !row.waitForExistence(timeout: 20) { reveal(row, in: app) }
        XCTAssertTrue(row.waitForExistence(timeout: 5), "no Transcription row with the hub ready: \(app.debugDescription)")
        let line = element(app, "recording-start-privacy")
        reveal(line, in: app)
        XCTAssertTrue(line.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertFalse(line.label.contains("会经你的 hub"), "privacy mode by default (the hub's default is device): \(line.label)")
        try shot(app, "start")

        reveal(row, in: app, above: true)
        // The row itself, settled (at the largest text sizes the line under Record scrolled it away).
        sleep(1)
        try shot(app, "start-row")
        row.tap()
        let onHub = app.buttons["在 hub 上"].firstMatch
        XCTAssertTrue(onHub.waitForExistence(timeout: 5), "no On the Hub in the menu: \(app.debugDescription)")
        onHub.tap()
        reveal(line, in: app)
        XCTAssertTrue(waitFor(5) { line.label.contains("会经你的 hub") }, "the line under Record doesn't say hub mode: \(line.label)")
        try shot(app, "start-hub")
    }

    /// The hub can't transcribe (`ready: false`), or has no hub transcription at all (`on: false`, as
    /// MOCK_NO_MEETING_TRANSCRIPTION=1): no Transcription row, and the line under Record keeps privacy mode's words even
    /// with the hub's default `hub` (a one-tap start then records on the phone too).
    @MainActor func testTheStartOffersNoHubWhenTheHubCantTranscribe() throws {
        let hub = try mockHub()
        try put(hub, "/api/v1/assistant", ["meeting_transcription_default": "hub"])
        for (switches, why) in [(["ready": false], "not ready"), (["on": false], "no hub transcription")] as [([String: Any], String)] {
            try post(hub, "/mock/meeting_transcription", switches)
            let app = launchRecordings(hub)
            XCTAssertTrue(app.buttons["recording-meeting-start"].waitForExistence(timeout: 15), app.debugDescription)
            let privately = element(app, "meeting-private-start")
            if !privately.waitForExistence(timeout: 15) { reveal(privately, in: app) }
            XCTAssertTrue(privately.exists, "no room meeting's options: \(app.debugDescription)")
            // A launch goes by what the last hub said until this one's hello and settings are in.
            let row = element(app, "meeting-transcription-start")
            XCTAssertTrue(waitFor(20) { !row.exists }, "a Transcription row with the hub \(why)")
            sleep(3)
            XCTAssertFalse(row.exists, "and it stays away (\(why))")
            let line = element(app, "recording-start-privacy")
            reveal(line, in: app)
            XCTAssertFalse(line.label.contains("会经你的 hub"), "hub mode's words with the hub \(why): \(line.label)")
            if why == "not ready" { try shot(app, "start-not-ready") }
            app.terminate()
            try post(hub, "/mock/meeting_transcription", Self.transcriptionOn)
        }
    }

    // MARK: Settings

    /// Settings › Meetings saves the default mode and the glossary on the hub (`GET /api/v1/assistant` has them), refuses
    /// a term over 40 characters before sending it, and keeps the Wi-Fi switch on this iPhone.
    @MainActor func testSettingsSaveTheDefaultAndTheGlossary() throws {
        let hub = try mockHub()
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "settings", "-openMeetingSettings", "YES"]
        app.launchPinned()
        // An inline picker's option is found by its words (an identifier on its text isn't exposed).
        let onHub = app.buttons.matching(NSPredicate(format: "label == %@", "在 hub 上")).firstMatch
        if !onHub.waitForExistence(timeout: 20) { reveal(onHub, in: app) }
        XCTAssertTrue(onHub.waitForExistence(timeout: 5), "no Transcription section: \(app.debugDescription)")
        try shot(app, "settings")
        onHub.tap()
        XCTAssertTrue(waitFor(10) { (try? self.assistant(hub))?["meeting_transcription_default"] as? String == "hub" },
                      "the hub's default never changed: \(String(describing: try? assistant(hub)))")
        let wifi = element(app, "settings-meeting-wifi-only")
        reveal(wifi, in: app)
        XCTAssertTrue(wifi.waitForExistence(timeout: 5), app.debugDescription)
        try shot(app, "settings-hub")

        // The glossary's row is above the Wi-Fi switch.
        let glossary = element(app, "settings-meeting-glossary")
        reveal(glossary, in: app, above: true)
        glossary.tap()
        let field = element(app, "glossary-field")
        XCTAssertTrue(field.waitForExistence(timeout: 10), app.debugDescription)
        for term in ["Persona", "王五 Alice"] {
            field.tap()
            field.typeText(term)
            app.buttons["glossary-add"].firstMatch.tap()
            XCTAssertTrue(waitFor(10) { ((try? self.assistant(hub))?["meeting_glossary"] as? [String])?.first == term },
                          "\(term) never reached the hub: \(String(describing: try? assistant(hub)))")
        }
        XCTAssertEqual((try assistant(hub))["meeting_glossary"] as? [String], ["王五 Alice", "Persona"], "the newest first")
        XCTAssertTrue(waitFor(5) { app.descendants(matching: .any).matching(identifier: "glossary-term").count == 2 }, app.debugDescription)
        try shot(app, "glossary")
        field.tap()
        field.typeText(String(repeating: "长", count: 41))
        app.buttons["glossary-add"].firstMatch.tap()
        XCTAssertTrue(element(app, "glossary-problem").waitForExistence(timeout: 5), "a term over 40 characters said nothing")
        XCTAssertEqual((try assistant(hub))["meeting_glossary"] as? [String], ["王五 Alice", "Persona"], "and never went")
    }

    // MARK: The meeting page and the Lock Screen

    /// The meeting page in hub mode: the hub's lines in the Live box and "Transcribed on the hub" under the title; an
    /// upstream problem brings the phone's own lines with the fallback notice. The Lock Screen shows the hub's line, then
    /// the phone's own, said so. After Stop the recording's page says it was transcribed on the hub, and how far the hub is.
    @MainActor func testThePageShowsTheHubsLinesThenFallsBack() throws {
        let hub = try mockHub()
        let app = XCUIApplication()
        // `-openChatsFresh`: the mock makes the room under the same id for the same made-up recording in every test.
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-openChatsFresh", "YES",
                               "-showMeetingSample", "room-hub"]
        app.launchPinned()
        XCTAssertTrue(element(app, "meeting-live").waitForExistence(timeout: 20), "no meeting page:\n\(app.debugDescription)")
        let hubLine = element(app, "meeting-live-hub-line")
        XCTAssertTrue(hubLine.waitForExistence(timeout: 60),
                      "the hub's lines never showed: \(String(describing: try? transcription(hub, live)))\n\(app.debugDescription)")
        let subtitle = element(app, "meeting-page-subtitle")
        XCTAssertTrue(waitFor(5) { subtitle.label.contains("在 hub 上转写") }, "hub mode not said: \(subtitle.label)")
        XCTAssertFalse(element(app, "meeting-transcription-fallback").exists, "a fallback notice while the hub is up")
        sleep(2)
        try shot(app, "page-hub")
        try lockScreen(app, "lock-hub", says: nil)

        try post(hub, "/mock/meeting_transcription", ["upstream": "failed"])
        let notice = element(app, "meeting-transcription-fallback")
        XCTAssertTrue(notice.waitForExistence(timeout: 30), "no fallback notice: \(app.debugDescription)")
        XCTAssertTrue(waitFor(10) { !hubLine.exists }, "the hub's lines stayed after the fallback")
        XCTAssertTrue(element(app, "meeting-live-line").exists, "the phone's own lines: \(app.debugDescription)")
        sleep(2)
        try shot(app, "page-fell-back")
        try lockScreen(app, "lock-fell-back", says: "本机转写")

        // Stop, then the recording's page (Open Recording): how it was transcribed and how far the hub is (finishing until
        // its final pass is done), with the stretches it has no audio for when there are any.
        let stop = app.buttons["recording-stop"].firstMatch
        XCTAssertTrue(stop.waitForExistence(timeout: 10), app.debugDescription)
        stop.tap()
        confirmStop(app)
        let open = app.buttons["recording-wrapup-open"].firstMatch
        XCTAssertTrue(open.waitForExistence(timeout: 30), "no Open Recording after Stop:\n\(app.debugDescription)")
        open.tap()
        let facts = element(app, "recording-transcription")
        XCTAssertTrue(facts.waitForExistence(timeout: 30), "the recording's page doesn't say how it was transcribed:\n\(app.debugDescription)")
        XCTAssertTrue(waitFor(30) { facts.label.contains("在 hub 上转写") && ["正在完成", "定稿", "没能转写"].contains { facts.label.contains($0) } },
                      "the recording's page doesn't say the hub's state: \(facts.label); the hub: \(String(describing: try? transcription(hub, live)))")
        sleep(1)
        try shot(app, "detail")
    }

    /// A hub-mode meeting started with the highlights too (#3862 phase 2b, `-showMeetingSample room-hub-highlights`): the
    /// Highlights card holds no line and there is no Live box, so the hub's lines are on the Full Transcript page; an
    /// upstream problem puts the fallback notice under the card's title, and the transcript shows the phone's own lines
    /// under it.
    @MainActor func testWithTheHighlightsTheHubsLinesAreOneTapAway() throws {
        let hub = try mockHub()
        try post(hub, "/mock/meeting_highlights", ["on": true])
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-openChatsFresh", "YES",
                               "-showMeetingSample", "room-hub-highlights"]
        app.launchPinned()
        let card = element(app, "meeting-highlights")
        XCTAssertTrue(card.waitForExistence(timeout: 20), "no Highlights card:\n\(app.debugDescription)")
        XCTAssertFalse(element(app, "meeting-live").exists, "a Live box beside the Highlights card")
        let transcript = card.descendants(matching: .any).matching(identifier: "meeting-transcript").firstMatch
        XCTAssertTrue(transcript.waitForExistence(timeout: 5), "no Full Transcript on the card:\n\(app.debugDescription)")

        // Full Transcript: the hub's lines, no notice while the hub is up.
        transcript.tap()
        let hubLine = element(app, "recording-timeline-hub-line")
        XCTAssertTrue(hubLine.waitForExistence(timeout: 60),
                      "the hub's lines never showed on the transcript: \(String(describing: try? transcription(hub, live)))\n\(app.debugDescription)")
        XCTAssertFalse(element(app, "meeting-transcription-fallback").exists, "a fallback notice while the hub is up")
        sleep(2)
        try shot(app, "highlights-transcript-hub")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(card.waitForExistence(timeout: 5), "Back didn't return:\n\(app.debugDescription)")

        // The room's highlight on the card (at the minute of the hub's last line), and no line of the transcript there.
        try post(hub, "/mock/meeting_highlights", ["id": live, "highlights": [["text": "导出上限这次保持不变。", "at": 100_000]]])
        XCTAssertTrue(element(app, "meeting-highlight").waitForExistence(timeout: 20), "the room's highlight never showed:\n\(app.debugDescription)")
        for id in ["meeting-live-hub-line", "meeting-live-line", "recording-timeline-hub-line", "recording-timeline-sentence"] {
            XCTAssertFalse(element(app, id).exists, "a line of the transcript on the card (\(id))")
        }
        XCTAssertFalse(element(app, "meeting-transcription-fallback").exists, "a fallback notice while the hub is up")
        sleep(1)
        try shot(app, "highlights-card-hub")

        // A problem upstream: the notice under the card's title, and the phone's own lines on the transcript under it.
        try post(hub, "/mock/meeting_transcription", ["upstream": "failed"])
        let notice = card.descendants(matching: .any).matching(identifier: "meeting-transcription-fallback").firstMatch
        XCTAssertTrue(notice.waitForExistence(timeout: 30), "no fallback notice on the card: \(app.debugDescription)")
        XCTAssertTrue(element(app, "meeting-highlight").exists, "the highlights went with the fallback")
        sleep(1)
        try shot(app, "highlights-card-fell-back")
        transcript.tap()
        XCTAssertTrue(element(app, "meeting-transcription-fallback").waitForExistence(timeout: 5),
                      "no fallback notice on the transcript: \(app.debugDescription)")
        XCTAssertTrue(element(app, "recording-timeline-sentence").waitForExistence(timeout: 5), "the phone's own lines: \(app.debugDescription)")
        XCTAssertTrue(waitFor(10) { !hubLine.exists }, "the hub's lines stayed after the fallback")
        sleep(1)
        try shot(app, "highlights-transcript-fell-back")
    }

    // MARK: Helpers

    /// Stop asks first: its confirmation's Stop Recording.
    @MainActor private func confirmStop(_ app: XCUIApplication) {
        let named = app.buttons["停止录音"].firstMatch
        if named.waitForExistence(timeout: 5) { named.tap(); return }
        let dialog = app.sheets.firstMatch.exists ? app.sheets.firstMatch : app.alerts.firstMatch
        XCTAssertTrue(dialog.exists, "Stop didn't ask first:\n\(app.debugDescription)")
        dialog.buttons.element(boundBy: 0).tap()
    }

    /// The Recordings page with the start's options (no sample: nothing records).
    @MainActor private func launchRecordings(_ hub: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings"]
        app.launchPinned()
        return app
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    /// Scrolls until `element` can be tapped: down the page, or back up it when the caller knows it is above (at the
    /// largest text sizes a row scrolls away once a later one was revealed).
    @MainActor private func reveal(_ element: XCUIElement, in app: XCUIApplication, above: Bool = false) {
        for _ in 0..<8 where !(element.exists && element.isHittable) {
            if above { app.swipeDown() } else { app.swipeUp() }
        }
    }

    /// The Lock Screen with the recording's Live Activity: shot as `name`, and, with `says`, a text on it containing that.
    /// Unlocked again after (a locked simulator hangs the next test), back in the app.
    @MainActor private func lockScreen(_ app: XCUIApplication, _ name: String, says: String?) throws {
        XCUIDevice.shared.press(.home)
        sleep(1)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(2)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(3)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["Allow", "允许", "Always Allow", "始终允许"] {
            let b = springboard.buttons[label].firstMatch
            if b.exists { b.tap(); sleep(2) }
        }
        let found = says.map { springboard.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", $0)).firstMatch.waitForExistence(timeout: 10) } ?? true
        // The Lock Screen draws the activity's words a moment after it shows (blurred until then).
        sleep(2)
        let env = ProcessInfo.processInfo.environment
        if let dir = env["PERSONA_SHOTS"] {
            let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
            try XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/hub-transcription-\(appearance)-\(name).png"))
        }
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(1)
        app.activate()
        sleep(1)
        XCTAssertTrue(found, "the Lock Screen doesn't say \(says ?? ""):\n\(springboard.debugDescription)")
    }

    @MainActor private func launch(_ hub: String, _ args: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        // Not the Recordings page: its live sheet would cover the probe; the uplink follows the recording wherever it shows.
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-probes", "YES"] + args
        app.launchPinned()
        return app
    }

    private func waitFor(_ seconds: TimeInterval = 10, _ ok: () -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(seconds)
        while Date.now < deadline {
            if ok() { return true }
            usleep(250_000)
        }
        return ok()
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/hub-transcription-\(appearance)-\(name).png"))
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") } // i18n-ignore
        return hub
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    /// One of the mock's switches.
    private func post(_ hub: String, _ path: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: request) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    /// `PUT` on the hub, as the app would (the mock takes any full-credentials device).
    private func put(_ hub: String, _ path: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = "PUT"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: request) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    /// `GET /api/v1/assistant` as the hub has it now (the mock takes it without a token).
    private func assistant(_ hub: String) throws -> [String: Any] { try json(hub, "/api/v1/assistant") }

    private func json(_ hub: String, _ path: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + path)!)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    /// The recording's last upload as the mock stored it (without `transcription` and `timeline`, kept with its hub mode).
    private func upload(_ hub: String, _ id: String) throws -> [String: Any]? {
        (try json(hub, "/mock/recordings")["recordings"] as? [String: Any])?[id] as? [String: Any]
    }

    /// The mock's hub mode for the recording: its frames, bytes, timeline, gaps, whether a socket is up, `transcription`.
    private func transcription(_ hub: String, _ id: String) throws -> [String: Any]? {
        (try json(hub, "/mock/meeting_transcription")["recordings"] as? [String: Any])?[id] as? [String: Any]
    }
}
