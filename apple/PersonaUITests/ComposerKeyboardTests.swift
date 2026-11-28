import XCTest

/// Only the app's composer controls are inspected, not the system keyboard's buttons. Synthetic chats on a private
/// mock hub (`scripts/mock/composer_hub.py`) cover the shared assistant/Agents composer, multiline drafts
/// and drag dismissal (#2200).
final class ComposerKeyboardTests: XCTestCase {
    private var app: XCUIApplication!
    private let agent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private let assistant = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs the isolated composer mock hub (PERSONA_MOCK_HUB)")
        }
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/sessions")!)
        let root = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        let rows = try XCTUnwrap(root?["sessions"] as? [[String: Any]])
        let models = Dictionary(uniqueKeysWithValues: rows.map { ($0["session_id"] as? String ?? "", $0["model"] as? String ?? "") })
        let agentID = String(agent.split(separator: "/").last!)
        let assistantID = String(assistant.split(separator: "/").last!)
        let message = "ComposerKeyboardTests requires scripts/mock/composer_hub.py; scripts/mock/mock_hub.py is a different fixture"
        XCTAssertEqual(models[agentID], "gpt-6.1-sol-codex-mini-preview-2026-11-05", message)
        XCTAssertEqual(models[assistantID], "gpt-6.1-sol", message)
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "composer tree"
            tree.lifetime = .keepAlways
            add(tree)
        }
        super.tearDown()
    }

    @MainActor private func openChat(isAssistant: Bool, sideBars: Bool = false) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        // Pair separately: a first pairing resets the route arguments, and must never point at a real hub.
        app.launchArguments = ["-pairURL", pair]
        app.launchPinned()
        XCTAssertTrue(app.buttons["Persona"].waitForExistence(timeout: 15) || field.waitForExistence(timeout: 15))
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-holdChromeOpen", "YES", "-sideBars", sideBars ? "YES" : "NO",
                               "-dev.openChat", "", "-app.tab", isAssistant ? "assistant" : "chats",
                               "-tab", isAssistant ? "assistant" : "dev"]
        app.launchArguments += isAssistant ? ["-openAssistant", assistant] : ["-openSession", agent]
        app.launchPinned()
        XCTAssertTrue(field.waitForExistence(timeout: 15), "the isolated conversation did not open")
        sleep(2)
        // Only the field: the placeholder over it once carried the identifier too, and on iPhone took the typing (#7632).
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "shell-composer-field").count, 1,
                       "something besides the field has its identifier")
    }

    @MainActor private var field: XCUIElement {
        let shell = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        if shell.exists { return shell }
        return app.textViews.firstMatch.exists ? app.textViews.firstMatch : app.textFields.firstMatch
    }

    @MainActor private func shot(_ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "\(name) \(UITestStyle.name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        if let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            let url = URL(fileURLWithPath: directory).appendingPathComponent("\(UITestStyle.name)-\(appearance)-\(name).png")
            try? screenshot.pngRepresentation.write(to: url)
        }
    }

    @MainActor private var dismissButton: XCUIElement {
        app.buttons["shell-composer-dismiss-keyboard"].firstMatch
    }

    /// The buttons of the chat's header and the system bar above it, within `column`. An iPad's split shows the session
    /// list beside the chat, whose rows end in a relative time ("35 min ago") that moves on during the test (#7479);
    /// only buttons overlapping the chat's column count. The vertical band is the open header (anchored by
    /// `chat.title`) or, while it is folded to fog, the navigation bar — not `field.minY - 120`, which reaches into the
    /// transcript and can pick up message controls such as Expand steps (#8767).
    @MainActor private func headerButtons(in column: CGRect) -> Set<String> {
        let title = app.buttons["chat.title"].firstMatch
        let limit: CGFloat
        if title.exists {
            // Title plus the rows under it (siblings, activity, mode, status); keep clear of the transcript below.
            limit = title.frame.maxY + 160
        } else {
            let bar = app.navigationBars.firstMatch
            limit = bar.exists ? bar.frame.maxY + 8 : 120
        }
        return Set(app.buttons.allElementsBoundByIndex.filter {
            $0.isHittable && $0.frame.maxY < limit && $0.frame.minY > 40 && $0.frame.maxX > column.minX && $0.frame.minX < column.maxX
        }.map(\.label))
    }

    @MainActor private func typeAndDismiss(_ name: String, shell: Bool, sideBars: Bool = false) {
        if shell {
            XCTAssertFalse(dismissButton.exists, "dismiss-keyboard button should not appear before typing")
        }
        // The chat's column, from the open header's title: the composer's own width changes as the draft grows.
        let title = app.buttons["chat.title"].firstMatch
        if sideBars { XCTAssertTrue(title.exists, "side-bars: no header before typing") }
        let column = title.exists ? title.frame : app.frame
        let before = headerButtons(in: column)
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "no software keyboard")
        let existing = field.value as? String ?? ""
        if !existing.isEmpty && existing != field.placeholderValue {
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: existing.count + 2))
        }
        field.typeText("first line\nsecond line")
        sleep(1)
        if shell {
            XCTAssertTrue(dismissButton.exists && dismissButton.isHittable, "missing dismiss-keyboard button while typing")
        }
        XCTAssertEqual(field.value as? String, "first line\nsecond line", "return should insert a newline, not send")
        let send = app.buttons["发送"].firstMatch
        XCTAssertTrue(send.exists && send.isEnabled && send.isHittable)
        var frames: [String: CGRect] = [:]
        if shell {
            let controls = ["shell-composer-add", "shell-composer-modes", "shell-composer-mic", "shell-composer-send"]
            for id in controls {
                let control = app.buttons[id].firstMatch
                XCTAssertTrue(control.exists && control.isHittable, "missing/hit-blocked \(id)")
                XCTAssertLessThanOrEqual(control.frame.maxY, app.keyboards.firstMatch.frame.minY + 1)
                frames[id] = control.frame
            }
            let expected = name.hasPrefix("assistant") ? "GPT-6.1 Sol" : "GPT-6.1 Sol Codex Mini Preview 2026 11 05"
            XCTAssertTrue((app.buttons["shell-composer-modes"].value as? String ?? "").hasPrefix(expected),
                          "the model label lost the version or variant")
            let mic = frames["shell-composer-mic"]!, send = frames["shell-composer-send"]!
            XCTAssertEqual(mic.height, send.height, accuracy: 1)
            XCTAssertEqual(mic.minY, send.minY, accuracy: 1, "controls must stay on one row")
            XCTAssertLessThanOrEqual(frames["shell-composer-modes"]!.maxX, mic.minX)
            XCTAssertLessThanOrEqual(field.frame.maxY, mic.minY + 1)
        }
        shot("\(name)-keyboard-up")
        if sideBars {
            XCTAssertFalse(before.subtracting(headerButtons(in: column)).isEmpty, "side-bars header did not fold")
            // Only the fog, the header has no title meanwhile: the system bar's names the session.
            XCTAssertTrue(app.navigationBars.staticTexts.allElementsBoundByIndex.contains { !$0.label.isEmpty },
                          "side-bars: no title while typing")
        }

        if shell {
            dismissButton.tapCenter()
            XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "dismiss button did not dismiss the keyboard")
            XCTAssertFalse(dismissButton.exists, "dismiss button stayed after keyboard dismissal")
            XCTAssertEqual(field.value as? String, "first line\nsecond line", "button dismissal must preserve the draft")
            field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "keyboard did not reopen")
        }

        let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3))
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 160)), withVelocity: .default,
                   thenHoldForDuration: 0.1)
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "drag no longer dismisses the keyboard")
        sleep(1)
        if shell {
            XCTAssertFalse(dismissButton.exists, "dismiss-keyboard button remained after drag dismissal")
        }
        XCTAssertEqual(field.value as? String, "first line\nsecond line", "dismissal must preserve the draft")
        for (id, frame) in frames {
            let now = app.buttons[id].firstMatch.frame
            XCTAssertEqual(now.height, frame.height, accuracy: 1, "\(id) tap height changed")
            if id == "shell-composer-modes" {
                // The hide-keyboard button leaves the row: a long model label gets that room back, never less.
                XCTAssertGreaterThanOrEqual(now.width, frame.width - 1, "\(id) got narrower after dismissal")
                XCTAssertLessThanOrEqual(now.maxX, app.buttons["shell-composer-mic"].firstMatch.frame.minX + 1, "\(id) ran into the mic")
            } else {
                XCTAssertEqual(now.width, frame.width, accuracy: 1, "\(id) tap width changed")
                XCTAssertEqual(now.minX, frame.minX, accuracy: 1, "\(id) moved horizontally after dismissal")
            }
        }
        shot("\(name)-keyboard-down")
        if sideBars { XCTAssertEqual(headerButtons(in: column), before, "side-bars header did not return") }

        // Sending still works after dismissal; only the local mock receives this synthetic draft.
        send.tapCenter()
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        let cleared = NSPredicate(format: "value == '' OR value == %@", field.placeholderValue ?? "")
        expectation(for: cleared, evaluatedWith: field)
        waitForExpectations(timeout: 8)
    }

    @MainActor func testAgentInTheShell() {
        openChat(isAssistant: false)
        typeAndDismiss("agent-shell", shell: true)
    }

    @MainActor func testAssistantInTheShell() {
        openChat(isAssistant: true)
        typeAndDismiss("assistant-shell", shell: true)
    }

    @MainActor func testModelLabelMatchesTheMenu() {
        openChat(isAssistant: true)
        field.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        let button = app.buttons["shell-composer-modes"]
        XCTAssertTrue((button.value as? String ?? "").hasPrefix("GPT-6.1 Sol"))
        button.tapCenter()
        XCTAssertTrue(app.staticTexts["GPT-6.1 Sol"].waitForExistence(timeout: 5), "menu and current label disagree")
        shot("assistant-model-menu")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap()
    }

    @MainActor func testSideBarsHeaderFoldsWhileTyping() {
        openChat(isAssistant: false, sideBars: true)
        typeAndDismiss("side-bars", shell: false, sideBars: true)
    }
}
