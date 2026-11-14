import XCTest

/// The rows right under a long message from another session take their own touches (#6645), against
/// scripts/mock/mock_hub.py with MOCK_PEER_FOLD=1 (PERSONA_MOCK_HUB). The message folds to a few lines with Expand; the
/// rest of its words are still laid out under the fold, unseen, and the upside-down chat list draws an older row over the
/// newer ones below it: a long press on the answer under it lifted the message. Four work lines, each right under such
/// a message: a tool and a reply (the one reported), tools only, a reply only, and the running one; each opens on a tap,
/// and on a finger's press at its words or its chevron.
/// `testRecord` (PERSONA_RECORD=1) taps the reported one and long-presses its answer for a screen recording, light then
/// dark, and prints each touch ("FOLDTAP <epoch> <x> <y>") and each part ("FOLDSEG <scheme> <epoch>") so the recording
/// can be cut and marked.
final class WorkLineUnderFoldUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private let chat = "macbook/a1f3c2d4-0000-4000-8000-000000006645"

    /// The work lines by what they read (their `work-head` value): the reported one, tools only, a reply only, running.
    private let reported = NSPredicate(format: "value BEGINSWITH %@", "工具 1 · 回复 1")
    private let toolsOnly = NSPredicate(format: "value BEGINSWITH %@", "工具 2")
    private let replyOnly = NSPredicate(format: "value BEGINSWITH %@", "回复 1")
    private let running = NSPredicate(format: "value CONTAINS %@", "跑剩下两条")

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_PEER_FOLD=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(scheme: String = "light") {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openChatsFresh", "YES", "-ui.scheme", scheme,
                               "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN", "-tab", "dev", "-openSession", chat]
        app.launchPinned()
        // The app's icon follows the appearance: switching it, the system says so in an alert over the chat.
        let notice = NSPredicate(format: "label CONTAINS[c] 'icon' OR label CONTAINS '图标'")
        for source in [app!, XCUIApplication(bundleIdentifier: "com.apple.springboard")] {
            let alert = source.alerts.containing(notice).firstMatch
            if alert.waitForExistence(timeout: 2), alert.buttons.firstMatch.exists { alert.buttons.firstMatch.tap(); break }
        }
        XCTAssertTrue(head(running).waitForExistence(timeout: 20), "the running work line shows")
        XCTAssertTrue(app.buttons.matching(identifier: "message.expand").firstMatch.waitForExistence(timeout: 5),
                      "the message above it is folded, with Expand")
        sleep(2) // the chat settles at its newest end
    }

    @MainActor private func head(_ which: NSPredicate) -> XCUIElement {
        app.buttons.matching(identifier: "work-head").matching(which).firstMatch
    }

    /// Where the element is on screen. Its frame as XCUITest gives it: a row the lazy list made just above the screen is
    /// above it (a negative y), not a flipped frame to mirror back, and mirroring it sent the drags the wrong way.
    @MainActor private func onScreen(_ e: XCUIElement) -> CGRect { e.frame }

    /// Brings the work line on screen, clear of the top bar and the composer: a flick toward older lines while the list
    /// has not made it yet, then slow drags (held at the end: a plain drag flings the upside-down list well past it,
    /// docs/lessons/ios.md). All in the list's left margin: a drag that starts on a message's words was often taken by it
    /// and the list stayed put (`MessageLiftUITests`).
    @MainActor private func reveal(_ which: NSPredicate) {
        let window = app.windows.firstMatch.frame
        let margin = 6 / max(window.width, 1)
        func drag(_ from: Double, _ to: Double, fast: Bool) {
            app.coordinate(withNormalizedOffset: CGVector(dx: margin, dy: from))
                .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: margin, dy: to)),
                       withVelocity: fast ? .fast : .slow, thenHoldForDuration: fast ? 0 : 0.3)
            sleep(1)
        }
        for _ in 0..<16 {
            let e = head(which)
            guard e.exists else { drag(0.3, 0.75, fast: true); continue }
            let y = onScreen(e).midY
            if y > window.height * 0.18, y < window.height * 0.8 { return }
            if y <= window.height * 0.18 { drag(0.35, 0.6, fast: false) } else { drag(0.6, 0.35, fast: false) }
        }
        XCTFail("work line \(which.predicateFormat) never came on screen")
    }

    /// Presses the work line where a person does, for `seconds` (nil: XCUITest's tap), at its words or at its chevron,
    /// and prints where and when.
    @MainActor private func tap(_ which: NSPredicate, seconds: Double? = nil, chevron: Bool = false) {
        let f = onScreen(head(which))
        let at = CGPoint(x: chevron ? f.maxX - 6 : f.minX + min(40, f.width / 2), y: f.midY)
        print(String(format: "FOLDTAP %.3f %.1f %.1f", Date().timeIntervalSince1970, at.x, at.y),
              "frame \(head(which).frame) shown \(f) window \(app.windows.firstMatch.frame)")
        let spot = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: at.x, dy: at.y))
        if let seconds { spot.press(forDuration: seconds) } else { spot.tap() }
    }

    /// Whether the line reads open (`true`) or folded (`false`) within the time.
    @MainActor private func reads(_ which: NSPredicate, open: Bool, timeout: TimeInterval = 4) -> Bool {
        let label = NSPredicate(format: "label == %@", open ? "收起过程" : "展开过程")
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if head(NSCompoundPredicate(andPredicateWithSubpredicates: [which, label])).exists { return true }
            usleep(200_000)
        }
        return false
    }

    /// What the lifted copy says: its label and its elements' (a short message's are one element there, combined).
    @MainActor private func liftedWords() -> String {
        let lifted = app.descendants(matching: .any)["message-lift"].firstMatch
        guard lifted.waitForExistence(timeout: 3) else { return "" }
        let inner = lifted.descendants(matching: .any).allElementsBoundByIndex.prefix(60).map(\.label)
        return ([lifted.label] + inner).filter { !$0.isEmpty }.joined(separator: " | ")
    }

    /// Each of the four work lines opens on a tap, and folds again on another.
    @MainActor func testAWorkLineUnderAFoldedMessageOpens() throws {
        launch()
        for (name, which) in [("running", running), ("reply only", replyOnly), ("tools only", toolsOnly), ("tool and reply", reported)] {
            reveal(which)
            tap(which)
            XCTAssertTrue(reads(which, open: true), "the \(name) work line under the folded message did not open")
            XCTAssertTrue(app.buttons["work-fold-bottom"].firstMatch.waitForExistence(timeout: 3), "no steps under the \(name) work line")
            reveal(which)
            tap(which)
            XCTAssertTrue(reads(which, open: false), "the \(name) work line did not fold again")
        }
    }

    /// A long press under the folded message is the row's under it, not the message's: on the work line nothing lifts
    /// (it is no message); on the answer under it, the answer lifts. The reported exchange and the one with a reply only.
    @MainActor func testBLongPressUnderAFoldedMessageLiftsWhatIsPressed() throws {
        var wrong: [String] = []
        launch()
        reveal(reported)
        tap(reported, seconds: 0.8)
        let onLine = liftedWords()
        print("FOLDLIFT work line: \(onLine)")
        if !onLine.isEmpty { wrong.append("a long press on the work line lifted: \(onLine.prefix(80))") }

        for (name, which, words) in [("reported", reported, "等它的界面测试结果"), ("reply only", replyOnly, "等它下一次报告")] {
            launch()
            reveal(which)
            let answer = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
            XCTAssertTrue(answer.waitForExistence(timeout: 5), "the \(name) answer under the work line shows")
            let a = onScreen(answer)
            print(String(format: "FOLDTAP %.3f %.1f %.1f", Date().timeIntervalSince1970, a.minX + 40, a.minY + 10), "press \(name) answer")
            app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: a.minX + 40, dy: a.minY + 10)).press(forDuration: 0.8)
            let got = liftedWords()
            print("FOLDLIFT \(name) answer: \(got)")
            if got.isEmpty { wrong.append("a long press on the \(name) answer lifted nothing") }
            else if got.contains("来自") || !got.contains(words) { wrong.append("a long press on the \(name) answer lifted: \(got.prefix(80))") }
        }
        XCTAssertEqual(wrong, [], "long presses under the folded message")
    }

    /// A finger's tap lasts a tenth to a third of a second: the reported work line opens and folds on each, at its words
    /// and at its chevron.
    @MainActor func testCFingerTapsOpenTheWorkLine() throws {
        launch()
        var missed: [String] = []
        for seconds in [0.08, 0.15, 0.25, 0.32] {
            for chevron in [false, true] {
                let spot = "\(chevron ? "chevron" : "words") \(seconds) s"
                reveal(reported)
                tap(reported, seconds: seconds, chevron: chevron)
                guard reads(reported, open: true, timeout: 3) else { missed.append("open: " + spot); continue }
                reveal(reported)
                tap(reported, seconds: seconds, chevron: chevron)
                if !reads(reported, open: false, timeout: 3) { missed.append("fold: " + spot) }
            }
        }
        print("FOLDPRESSES missed \(missed)")
        XCTAssertEqual(missed, [], "presses on the work line under the folded message that did nothing")
    }

    /// For the recording, in light and then dark: the reported work line tapped open and folded again, then a long press
    /// on the answer under it.
    @MainActor func testRecord() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_RECORD"] == "1" else { throw XCTSkip("PERSONA_RECORD=1 records") }
        for scheme in ["light", "dark"] {
            launch(scheme: scheme)
            reveal(reported)
            sleep(1)
            print(String(format: "FOLDSEG %@ %.3f", scheme, Date().timeIntervalSince1970))
            sleep(1)
            for _ in 0..<2 {
                reveal(reported)
                tap(reported, seconds: 0.15)
                sleep(3)
            }
            let answer = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "等它的界面测试结果")).firstMatch
            if answer.exists {
                let a = onScreen(answer)
                print(String(format: "FOLDTAP %.3f %.1f %.1f", Date().timeIntervalSince1970, a.minX + 40, a.minY + 10), "press answer")
                app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: a.minX + 40, dy: a.minY + 10)).press(forDuration: 0.8)
                sleep(3)
            }
            print(String(format: "FOLDSEGEND %@ %.3f", scheme, Date().timeIntervalSince1970))
        }
    }
}
