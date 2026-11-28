import XCTest

/// End-to-end UI tests for iOS "View through the computer" headless browser preview streaming against
/// `scripts/mock/mock_hub.py` with `MOCK_PREVIEW=1` (`PERSONA_MOCK_HUB`):
/// 1. Switching from direct loopback mode via the header ⋯ menu renders streamed frames and switches back cleanly.
/// 2. Tapping a `<select>` on `/settings` in stream mode opens the native picker sheet, and choosing an option records
///    the exact `select_choose` message on the mock hub.
/// 3. On a recoverable direct-mode failure (`mode=directfail`), the error overlay's "View through the computer" button
///    switches into the stream, and repeated direct failure triggers automatic fallback with its notice banner.
final class PreviewStreamUITests: XCTestCase {
    private var hub: String!
    private let widget = "a1f3c2d4-0000-4000-8000-000000000001"
    private var look = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_PREVIEW=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    private func mock(_ query: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/preview" + query)!)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    @MainActor private func saveShot(_ app: XCUIApplication, _ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name + (look.isEmpty ? "" : "-" + look)
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let suffix = look.isEmpty ? "" : "-" + look
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("preview-\(name)\(suffix).png"))
        }
    }

    @MainActor private func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 15, _ app: XCUIApplication) {
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "\(what):\n\(app.debugDescription.prefix(4000))")
    }

    @MainActor private func launchPreviewServers(look: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        var args = ["-AppleLanguages", "(en)", "-pairURL", pair, "-tab", "dev", "-openSession", "macbook/" + widget,
                    "-openChatsFresh", "YES", "-holdChromeOpen", "YES", "-openPreviewServers", "YES"]
        if look == "light" || look == "dark" { args += ["-ui.scheme", look] }
        app.launchArguments = args
        app.launchPinned()
        return app
    }

    /// 1 & 2: Menu switch to "View through the computer" renders frames; tapping `<select>` on `/settings` opens native
    /// picker and sends `select_choose` with the selected option index.
    @MainActor func testMenuSwitchToStreamAndNativeSelectPicker() throws {
        let looks = ProcessInfo.processInfo.environment["PERSONA_LOOKS"]?.split(separator: ",").map(String.init) ?? [""]
        for l in looks {
            look = l
            if l == "dark" { XCUIDevice.shared.appearance = .dark }
            else if l == "light" { XCUIDevice.shared.appearance = .light }

            _ = try mock("?mode=1")
            let app = launchPreviewServers(look: l)

            let web = app.buttons["preview-server-web"]
            see(web, "running web server missing in chooser", timeout: 25, app)
            web.tap()

            // Navigate to /settings in direct mode so switching preserves /settings (which has #role <select>).
            see(app.webViews.staticTexts["Good morning"], "direct preview did not load", timeout: 25, app)
            app.webViews.links["Settings"].firstMatch.tap()
            see(app.webViews.buttons["Save changes"].firstMatch, "settings page did not load directly", timeout: 15, app)

            // Open header ⋯ menu and switch to "View through the computer".
            let more = app.buttons["link-more"].firstMatch
            see(more, "no header ⋯ menu on preview card", timeout: 10, app)
            more.tap()
            let toStream = app.buttons["View through the computer"].firstMatch
            see(toStream, "no View through the computer option in ⋯ menu", timeout: 5, app)
            toStream.tap()

            // Wait for streamed frame to render.
            let frame = app.descendants(matching: .any)["preview-stream-frame"].firstMatch
            see(frame, "streamed frame did not render after switching to computer view", timeout: 25, app)
            sleep(1)
            saveShot(app, "stream-view")

            // Verify mock recorded mobile open, features {select: true, png: true}, and acks.
            let beforeTap = try mock("")
            let acks = beforeTap["acks"] as? Int ?? 0
            XCTAssertGreaterThan(acks, 0, "stream did not ack rendered frames: \(beforeTap)")
            let logBefore = beforeTap["log"] as? [[String: Any]] ?? []
            XCTAssertTrue(logBefore.contains { ($0["t"] as? String) == "features" && ($0["select"] as? Bool) == true && ($0["png"] as? Bool) == true },
                          "missing features {select:true, png:true} in mock log: \(logBefore)")

            // Tap the #role <select> box in the streamed /settings page: select_rect(390) = {x: 22, y: 154, w: 346, h: 38}
            // within the 390x844 frame -> normalized center (0.50, 173/844 ≈ 0.205).
            let selectCoord = frame.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 173.0 / 844.0))
            selectCoord.tap()

            // Native picker sheet opens with grouped options, disabled item, and checkmark on selected.
            let pickerSheet = app.descendants(matching: .any)["preview-select-sheet"].firstMatch
            see(pickerSheet, "native select picker did not open after tapping <select>", timeout: 15, app)
            let reviewer = app.buttons["preview-select-option-3"].firstMatch
            see(reviewer, "Reviewer option missing in native select picker", timeout: 5, app)
            let disabledOwner = app.buttons["preview-select-option-2"].firstMatch
            see(disabledOwner, "disabled Owner option missing", timeout: 5, app)
            XCTAssertFalse(disabledOwner.isEnabled, "disabled select option must not be enabled")
            sleep(1)
            saveShot(app, "select-picker")

            // Choose Reviewer (index 3) and verify mock recorded select_choose {index: 3}.
            reviewer.tap()
            XCTAssertTrue(pickerSheet.waitForNonExistence(timeout: 5), "native select picker did not dismiss after selection")

            var matched = false
            for _ in 0..<20 {
                let after = try mock("")
                let log = after["log"] as? [[String: Any]] ?? []
                let sel = after["select"] as? [String: Any]
                if log.contains(where: { ($0["t"] as? String) == "select_choose" && ($0["index"] as? Int) == 3 })
                    && (sel?["value"] as? Int) == 3 {
                    matched = true
                    break
                }
                usleep(200_000)
            }
            XCTAssertTrue(matched, "mock did not record select_choose index=3: \(String(describing: try? mock("")))")

            // Verify switching back to direct mode works from the ⋯ menu.
            if l == looks.last {
                more.tap()
                let toDirect = app.buttons["View directly"].firstMatch
                see(toDirect, "no View directly option in ⋯ menu while streaming", timeout: 5, app)
                toDirect.tap()
                see(app.webViews.buttons["Save changes"].firstMatch, "did not switch back to direct WKWebView", timeout: 20, app)
            }
        }
    }

    /// 3: Direct failure shows "View through the computer" on the error overlay (which switches cleanly into stream),
    /// and a second consecutive direct failure triggers automatic fallback with the localized notice banner.
    @MainActor func testDirectFailureErrorOverlayAndAutoFallbackNotice() throws {
        let looks = ProcessInfo.processInfo.environment["PERSONA_LOOKS"]?.split(separator: ",").map(String.init) ?? [""]
        for l in looks {
            look = l
            if l == "dark" { XCUIDevice.shared.appearance = .dark }
            else if l == "light" { XCUIDevice.shared.appearance = .light }

            // Part A: Recoverable direct failure (HTTP 502) shows error overlay with "View through the computer" button.
            _ = try mock("?mode=directfail")
            let app = launchPreviewServers(look: l)
            let web = app.buttons["preview-server-web"]
            see(web, "running web server missing in directfail mode", timeout: 25, app)
            web.tap()

            let overlay = app.descendants(matching: .any)["webview-error-overlay"].firstMatch
            see(overlay, "error overlay did not appear on direct 502 failure", timeout: 20, app)
            let streamBtn = app.buttons["View through the computer"].firstMatch
            see(streamBtn, "no View through the computer button on recoverable error overlay", timeout: 5, app)
            saveShot(app, "error-overlay")

            if l == looks.last {
                // Verify tapping "View through the computer" on the error overlay switches into the stream.
                streamBtn.tap()
                let frame = app.descendants(matching: .any)["preview-stream-frame"].firstMatch
                see(frame, "tapping View through the computer on error overlay did not render stream", timeout: 25, app)

                // Part B: Fresh launch with consecutive direct failure triggers automatic fallback and shows the notice banner.
                let app2 = launchPreviewServers(look: l)
                let web2 = app2.buttons["preview-server-web"]
                see(web2, "running web server missing on second launch", timeout: 25, app2)
                web2.tap()
                see(app2.descendants(matching: .any)["webview-error-overlay"].firstMatch, "error overlay missing on 2nd launch", timeout: 20, app2)
                let retryBtn2 = app2.buttons["Retry"].firstMatch
                see(retryBtn2, "no Retry button on error overlay", timeout: 5, app2)
                retryBtn2.tap()
                let notice2 = app2.descendants(matching: .any)["preview-fallback-notice"].firstMatch
                see(notice2, "auto-fallback notice did not appear after consecutive direct failure", timeout: 25, app2)
                see(app2.descendants(matching: .any)["preview-stream-frame"].firstMatch, "stream did not render after auto-fallback", timeout: 25, app2)
                sleep(1)
                saveShot(app2, "fallback-notice")
            } else {
                // Tap Retry once to trigger consecutive-failure automatic fallback and capture its notice banner screenshot.
                let retryBtn = app.buttons["Retry"].firstMatch
                see(retryBtn, "no Retry button on error overlay", timeout: 5, app)
                retryBtn.tap()
                let notice = app.descendants(matching: .any)["preview-fallback-notice"].firstMatch
                see(notice, "auto-fallback notice did not appear after consecutive direct failure", timeout: 25, app)
                see(app.descendants(matching: .any)["preview-stream-frame"].firstMatch, "stream did not render after auto-fallback", timeout: 25, app)
                sleep(1)
                saveShot(app, "fallback-notice")
            }
        }
    }
}
