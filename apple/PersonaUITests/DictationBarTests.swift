import XCTest

/// The dictation bar (#5843): the shell composer's mic tapped turns it into [✕] wave [■] [↑]. ✕ drops what was heard,
/// ■ puts it into the draft without sending, ↑ puts it in and sends. Words come from `-showDictationSample` (a simulator
/// has no microphone). Against scripts/mock/mock_hub.py (TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port>).
final class DictationBarTests: XCTestCase {
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private let words = "dictated words here"
    private var app: XCUIApplication!
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.name = "failure"
            shot.lifetime = .keepAlways
            add(shot)
        }
        super.tearDown()
    }

    @MainActor private func openMain() {
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main,
                               "-showDictationSample", words]
        app.launchPinned()
        XCTAssertTrue(mic.waitForExistence(timeout: 30), "no mic")
        clearDraft()
    }

    private var mic: XCUIElement { app.buttons["shell-composer-mic"].firstMatch }
    private var field: XCUIElement { app.descendants(matching: .any)["shell-composer-field"].firstMatch }
    private func bar(_ part: String) -> XCUIElement { app.buttons["dictation-bar-" + part].firstMatch }

    /// What the field holds (its value is the placeholder while it is empty).
    @MainActor private var draft: String {
        let value = field.value as? String ?? ""
        return value == field.placeholderValue ? "" : value
    }

    /// Earlier runs keep their draft across launches: empty it first.
    @MainActor private func clearDraft() {
        guard !draft.isEmpty else { return }
        field.tap()
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: draft.count + 2))
        app.buttons["shell-composer-dismiss-keyboard"].firstMatch.tap()
        sleep(2) // drafts are saved a moment after the last change
    }

    /// Taps the mic and waits until all the sample words were heard (one every 350 ms). The bar's parts are always
    /// built (hidden from accessibility while it is off, which XCUITest's tree still lists): open is the mic listening
    /// and ✕ hittable.
    @MainActor private func dictate() {
        mic.tap()
        XCTAssertTrue(wait { self.mic.label == self.listening && self.bar("cancel").isHittable }, "the bar did not open")
        XCTAssertTrue(bar("stop").isHittable && bar("send").isHittable, "the bar's ■ or ↑ cannot be tapped")
        sleep(2)
    }

    /// The mic's spoken name while it listens (zh-Hans: the tests run in it), and when it does not.
    private let listening = "停止听写"

    /// The bar closed: dictation is over.
    @MainActor private func closed() -> Bool { wait { self.mic.label != self.listening } }

    @MainActor private func wait(_ timeout: TimeInterval = 5, _ ok: @escaping () -> Bool) -> Bool {
        let end = Date.now.addingTimeInterval(timeout)
        repeat {
            if ok() { return true }
            usleep(200_000)
        } while Date.now < end
        return false
    }

    private func sent() throws -> [String] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/sent")!)
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        return ((json?["sent"] as? [Any]) ?? []).map { "\($0)" }
    }

    @MainActor func testStopPutsTheWordsInTheDraftWithoutSending() throws {
        openMain()
        let before = try sent().count
        dictate()
        bar("stop").tap()
        XCTAssertTrue(closed(), "the bar did not close")
        XCTAssertEqual(draft, words)
        XCTAssertEqual(try sent().count, before, "■ sent the message")
        clearDraft()
    }

    @MainActor func testCancelDropsWhatWasHeard() throws {
        openMain()
        dictate()
        bar("cancel").tap()
        XCTAssertTrue(closed(), "the bar did not close")
        XCTAssertEqual(draft, "", "✕ kept the words")
        XCTAssertTrue(mic.isHittable, "the composer did not come back")
    }

    @MainActor func testSendSendsWhatWasHeard() throws {
        openMain()
        let before = try sent().count
        dictate()
        bar("send").tap()
        XCTAssertTrue(closed(), "the bar did not close")
        let deadline = Date.now.addingTimeInterval(5)
        while try sent().count == before, Date.now < deadline { usleep(200_000) }
        let all = try sent()
        XCTAssertEqual(all.count, before + 1, "↑ did not send")
        XCTAssertTrue(all.last?.contains(words) == true, "↑ sent something else: \(all.last ?? "")")
        XCTAssertEqual(draft, "", "the draft kept the sent words")
    }
}
