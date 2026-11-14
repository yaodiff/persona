import XCTest

/// Your own long message folds (#5676) against scripts/mock/mock_hub.py with MOCK_LONG_INPUT=1 (PERSONA_MOCK_HUB): past
/// 14 lines it shows 10 with Show More (「展开」) at the bubble's bottom, a twelve-line one stays whole; a long press on the
/// folded bubble opens it with no action bar, its top where it was; open, a long press lifts it with the bar as before;
/// Show Less (「收起」) folds it again. `testShots` takes the screenshots for review: before (`-showWholeBubbles`) and after,
/// folded and open, in an Agent chat and in Persona's conversation, light and dark, at the default and the largest text
/// size. PERSONA_SHOTS: a folder the shots are also written to.
final class BubbleFoldUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private let agent = "macbook/a1f3c2d4-0000-4000-8000-000005676a00"
    private let persona = "server/b2e4d6f8-0000-4000-8000-000000000001"
    /// The long report's first and last words, and the twelve-line message's last line.
    private let first = "车道 sample-lane"
    private let last = "最后一行在这里"
    private let twelve = "第 12 行"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_LONG_INPUT=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    /// fresh: `-openChatsFresh` (no chat kept on disk, and every fold closed: `OpenReports`).
    @MainActor private func launch(_ chat: String, scheme: String = "light", largest: Bool = false, whole: Bool = false,
                                   fresh: Bool = true, plain: Bool = false, extra: [String] = []) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let open = chat == persona
            ? ["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", chat]
            : ["-tab", "dev", "-openSession", chat]
        app.launchArguments = ["-pairURL", pair, "-openChatsFresh", fresh ? "YES" : "NO", "-ui.scheme", scheme,
                               "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"] + open
            + (largest ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] : [])
            + (whole ? ["-showWholeBubbles", "YES"] : []) + (plain ? ["-showPlainBubbleOpen", "YES"] : []) + extra
        app.launchPinned()
        // The app's icon follows the appearance: switching it, the system says so in an alert over the chat (it covered
        // the first shot of a run).
        let notice = NSPredicate(format: "label CONTAINS[c] 'icon' OR label CONTAINS '图标'")
        for source in [app!, XCUIApplication(bundleIdentifier: "com.apple.springboard")] {
            let alert = source.alerts.containing(notice).firstMatch
            if alert.waitForExistence(timeout: 2), alert.buttons.firstMatch.exists { alert.buttons.firstMatch.tap(); break }
        }
    }

    @MainActor private func shot(_ name: String) {
        let png = app.screenshot()
        let a = XCTAttachment(screenshot: png)
        a.name = "fold-" + name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? png.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/fold-\(name).png"))
        }
    }

    private func text(_ s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    @MainActor private func press(_ words: XCUIElement) {
        words.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 60, dy: 20)).press(forDuration: 0.8)
    }

    /// Taps Show More where it shows: in the upside-down chat list an element's frame may come flipped (as
    /// MessageLiftUITests' `liftAt` finds), and `isHittable` is then wrong too.
    @MainActor private func tapShowMore() {
        let window = app.windows.firstMatch.frame
        var f = expand.firstMatch.frame
        if !window.insetBy(dx: 0, dy: window.height * 0.12).contains(CGPoint(x: f.midX, y: f.midY)) { f.origin.y = window.height - f.maxY }
        print("FOLD show-more frame \(expand.firstMatch.frame) tapped at y \(f.midY) in \(window)")
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: f.minX + 24, dy: f.midY)).tap()
    }

    /// Taps the button of `query` that is on screen mid-list (the first match may be a row scrolled away): an element's frame
    /// may come flipped (see `tapShowMore`), so each is tried as it is and flipped.
    @MainActor @discardableResult private func tapVisible(_ query: XCUIElementQuery, lowest: Bool = false) -> Bool {
        _ = query.firstMatch.waitForExistence(timeout: 5)
        let window = app.windows.firstMatch.frame
        let band = window.minY + window.height * 0.1 ... window.minY + window.height * 0.9
        for b in (lowest ? query.allElementsBoundByIndex.reversed() : query.allElementsBoundByIndex) {
            let f = b.frame
            print("FOLD button frame \(f) in \(window)")
            for y in [f.midY, window.height - f.maxY + f.height / 2] where band.contains(y) {
                app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: f.minX + 24, dy: y)).tap()
                return true
            }
        }
        return false
    }

    /// `tapVisible`, scrolling to find it: what opened runs on below the screen, so Show Less may be off it. Up first,
    /// then back down past where it started.
    @MainActor private func tapFound(_ query: XCUIElementQuery, lowest: Bool = false) {
        for attempt in 0..<10 {
            if tapVisible(query, lowest: lowest) { return }
            if attempt < 3 { app.swipeUp(velocity: .slow) } else { app.swipeDown(velocity: .slow) }
            sleep(1)
        }
        XCTFail("no such button on screen")
    }

    private var expand: XCUIElementQuery { app.buttons.matching(identifier: "bubble.expand") }
    private var collapse: XCUIElement { app.buttons["bubble.collapse"].firstMatch }
    private var bar: XCUIElement { app.staticTexts["复制"].firstMatch }

    /// Folded with Show More; a long press opens it in place with no bar; a second long press lifts it with the bar; Show
    /// Less folds it again. The twelve-line message has no button.
    @MainActor func testALongPressOpensAFoldedMessage() throws {
        launch(agent)
        let words = text(first)
        XCTAssertTrue(words.waitForExistence(timeout: 20), "the chat did not open:\n\(app.debugDescription)")
        sleep(2)
        XCTAssertEqual(expand.count, 1, "Show More under the long message only, not under the twelve-line one")
        XCTAssertTrue(text(twelve).exists, "the twelve-line message is whole")
        let top = words.frame.minY

        // A long press on the folded bubble (its first lines: the words' element runs past the fold): open, no bar, its
        // top where it was.
        press(words)
        XCTAssertTrue(collapse.waitForExistence(timeout: 5), "the long press did not open it")
        sleep(1)
        XCTAssertFalse(bar.exists, "the long press that opened it also lifted it with the bar")
        XCTAssertEqual(words.frame.minY, top, accuracy: 2, "opening moved the bubble's top")
        shot("press-opened")

        // Open, a long press lifts it with the bar as before.
        press(words)
        XCTAssertTrue(bar.waitForExistence(timeout: 5), "open, a long press did not lift it with the bar")
        shot("press-lifted")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.15)).tap()
        XCTAssertTrue(bar.waitForNonExistence(timeout: 5), "the bar did not close")
        sleep(1)

        // Show Less folds it again; Show More opens it.
        for _ in 0..<8 where !collapse.isHittable {
            app.swipeUp(velocity: .slow)
            sleep(1)
        }
        collapse.tap()
        XCTAssertTrue(expand.firstMatch.waitForExistence(timeout: 5), "Show Less did not fold it")
        sleep(1)
        tapShowMore()
        XCTAssertTrue(collapse.waitForExistence(timeout: 5), "Show More did not open it")
    }

    /// Opens and folds the long message by Show More, with pauses, for a screen recording of the fold's frames
    /// (`simctl io recordVideo` beside the run): the newest end of an Agent chat, light or dark by PERSONA_SCHEME.
    @MainActor func testToggleForRecording() throws {
        let env = ProcessInfo.processInfo.environment
        let scheme = env["PERSONA_SCHEME"] ?? "light"
        let scenario = env["PERSONA_SCENARIO"] ?? (env["PERSONA_MIDLIST"] == "1" ? "mid" : "")
        launch(agent, scheme: scheme, plain: env["PERSONA_PLAIN"] == "1", extra: ["-scrollTrace", "YES"] + (scenario == "top" ? ["-showOldest", "YES"] : []))
        XCTAssertTrue(expand.firstMatch.waitForExistence(timeout: 20), "not folded:\n\(app.debugDescription)")
        sleep(3)
        // PERSONA_MIDLIST=1 (the mock's perf chat, forty exchanges): one exchange up, so the message is mid-list and what
        // opens runs on under the visible rows instead of below the screen. PERSONA_SCENARIO=bottom|top: the newest end
        // (a message just sent, the list held to the bottom) or the oldest loaded (`-showOldest`) of the same chat.
        if scenario == "mid" {
            app.swipeUp(velocity: .slow)
            sleep(2)
        }
        // Mid-list one round: folded, the bubble sits where the second Show More is off screen.
        for _ in 0..<(scenario.isEmpty ? 2 : 1) {
            let mid = !scenario.isEmpty
            if mid { tapFound(expand, lowest: scenario == "bottom") } else { tapShowMore() }
            XCTAssertTrue(collapse.waitForExistence(timeout: 5), "Show More did not open it")
            sleep(3)
            if mid { tapFound(app.buttons.matching(identifier: "bubble.collapse")) } else { collapse.tap() }
            XCTAssertTrue(expand.firstMatch.waitForExistence(timeout: 5), "Show Less did not fold it")
            sleep(3)
        }
        if !scenario.isEmpty { return }
        let words = text(first)
        press(words)
        XCTAssertTrue(collapse.waitForExistence(timeout: 5), "the long press did not open it")
        sleep(3)
    }

    /// Opened by Show More and then lifted by a long press, the copy is the whole message, not its folded height cut to
    /// "…" (#6481): words from past the folded ten lines show in the copy.
    @MainActor func testLiftAfterShowMoreIsWhole() throws {
        launch(agent)
        XCTAssertTrue(expand.firstMatch.waitForExistence(timeout: 20), "not folded:\n\(app.debugDescription)")
        sleep(2)
        tapShowMore()
        XCTAssertTrue(collapse.waitForExistence(timeout: 5), "Show More did not open it")
        sleep(1)
        press(text(first))
        XCTAssertTrue(bar.waitForExistence(timeout: 5), "the long press did not lift it")
        sleep(1)
        shot("lift-after-show-more")
                // Its words are one element as tall as the whole message (more than the screen shows), so it is its height that
        // tells: folded, the element is ten lines (about 280 pt), whole it is past the screen's height less its top.
        let beyond = text("sheet-c-wide")
        XCTAssertTrue(beyond.exists, "the lifted copy stops at the folded lines")
        XCTAssertGreaterThan(beyond.frame.height, 600, "the lifted copy is as tall as the folded bubble: \(beyond.frame)")
    }

    private struct Trace: Decodable {
        var what: String
        var frames: Int
        var hitches: Int
        var worstMS: Int
        var moved: Int
        var end: Int
        var step: Int?
        var ys: [Int?]
        var heights: [Int?]?
    }

    @MainActor private func traces() -> [Trace] {
        app.probe("unfold", as: [Trace].self, page: "agents") ?? app.probe("unfold", as: [Trace].self) ?? []
    }

    /// Opening and folding the long message, measured by the app itself (`UnfoldTrace`, #1123: what the tapped bubble's
    /// top does frame by frame, and the longest frame) at the newest end, mid-list and at the oldest loaded, with the fade
    /// (after) and without it (`-showPlainBubbleOpen`, before), A B B A, one relaunch each. Meant for an optimized build
    /// (docs/apple/performance.md, "Measuring"). Needs the mock's perf chat: MOCK_LONG_INPUT=perf. Prints
    /// `BUBBLE-TRACE <scenario> <variant> <run> <what>: moved … step … hitches … worst … ms ys …`.
    @MainActor func testOpenAndFoldTrace() throws {
        let order = [("after", false), ("before", true), ("before", true), ("after", false)]
        for scenario in ["bottom", "mid", "top"] {
            for (run, (variant, plain)) in order.enumerated() {
                launch(agent, plain: plain, extra: ["-probes", "YES", "-unfoldTrace", "YES", "-perfCounts", "YES"]
                       + (scenario == "top" ? ["-showOldest", "YES"] : []))
                XCTAssertTrue(expand.firstMatch.waitForExistence(timeout: 20), "not folded:\n\(app.debugDescription)")
                sleep(9) // the chat measures itself at rest first (`UnfoldTrace.idle`)
                if scenario == "mid" { app.swipeUp(velocity: .slow); sleep(2) }
                for step in ["open", "fold"] {
                    let before = app.probeSeq("unfold", page: "agents") ?? app.probeSeq("unfold") ?? 0
                    if step == "open" { tapFound(expand, lowest: scenario == "bottom") }
                    else { tapFound(app.buttons.matching(identifier: "bubble.collapse")) }
                    Thread.sleep(forTimeInterval: 3.6) // `UnfoldTrace.window`
                    guard let r = traces().last, (app.probeSeq("unfold", page: "agents") ?? app.probeSeq("unfold") ?? 0) != before else {
                        print("BUBBLE-TRACE \(scenario) \(variant) \(run) \(step): measured nothing")
                        continue
                    }
                    let ys: String = r.ys.prefix(40).map { $0.map(String.init) ?? "-" }.joined(separator: " ")
                    var line: String = "BUBBLE-TRACE \(scenario) \(variant) \(run) \(step) \(r.what): moved \(r.moved) end \(r.end)"
                    line += " step \(r.step ?? -1) hitches \(r.hitches)/\(r.frames) worst \(r.worstMS) ms ys \(ys)"
                    print(line)
                    sleep(1)
                }
                app.terminate()
            }
        }
    }

    /// Open is kept per message: the chat opened again shows it open.
    @MainActor func testOpenIsKept() throws {
        launch(agent)
        XCTAssertTrue(expand.firstMatch.waitForExistence(timeout: 20), "not folded:\n\(app.debugDescription)")
        sleep(1)
        tapShowMore()
        XCTAssertTrue(collapse.waitForExistence(timeout: 5))
        sleep(2) // written off the main thread (StateCache)
        app.terminate()
        launch(agent, fresh: false)
        XCTAssertTrue(text(first).waitForExistence(timeout: 20))
        sleep(2)
        XCTAssertTrue(collapse.exists, "the message folded again")
        foldBack() // as it was, for the other tests
    }

    /// Show Less, scrolled to where it is, then a moment for the open state to be written.
    @MainActor private func foldBack() {
        for _ in 0..<8 where !collapse.isHittable {
            app.swipeUp(velocity: .slow)
            sleep(1)
        }
        if collapse.isHittable { collapse.tap() }
        sleep(2)
    }

    /// The screenshots for review, before and after: each chat, scheme and text size.
    @MainActor func testShots() throws {
        for chat in [agent, persona] {
            for scheme in ["light", "dark"] {
                for largest in [false, true] {
                    let name = "\(chat == agent ? "agent" : "persona")-\(scheme)-\(largest ? "largest" : "default")"
                    launch(chat, scheme: scheme, largest: largest, whole: true)
                    XCTAssertTrue(text(last).waitForExistence(timeout: 20) || text(first).waitForExistence(timeout: 5),
                                  "\(name): the chat did not open")
                    if scheme == "dark" { acceptIconAlert(timeout: 3) }
                    sleep(2)
                    shot(name + "-before")
                    app.terminate()

                    launch(chat, scheme: scheme, largest: largest)
                    XCTAssertTrue(expand.firstMatch.waitForExistence(timeout: 20), "\(name): not folded:\n\(app.debugDescription)")
                    if scheme == "dark" { acceptIconAlert(timeout: 3) }
                    sleep(2)
                    shot(name + "-folded")
                    tapShowMore()
                    if largest, !collapse.waitForExistence(timeout: 5) {
                        // XCUITest has reported Show More's frame off the screen at the largest size (the flipped chat
                        // list): no tap reaches it there. The folded shot is what this size is for.
                        print("FOLD \(name): Show More not reached by the test at the largest size; no opened shot")
                        app.terminate()
                        continue
                    }
                    XCTAssertTrue(collapse.waitForExistence(timeout: 5), "\(name): Show More did not open it")
                    sleep(1)
                    shot(name + "-opened")
                    foldBack() // folded again for the next launch (open is kept)
                    app.terminate()
                }
            }
        }
    }
}
