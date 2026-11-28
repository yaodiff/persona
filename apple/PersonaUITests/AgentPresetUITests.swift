import XCTest

/// New Agent presets and plan-task tiers (#2225), against PersonaUITests/Fixtures/model_profiles_hub.py.
/// The fixture wraps the ordinary MOCK_MODEL_DEFAULTS=1 hub; it adds no production mock fields or fixtures.
/// PERSONA_SHOTS writes new captures only; PERSONA_SHOTS_APPEARANCE names the simulator appearance.
final class AgentPresetUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_PRESETS_HUB"] else {
            throw XCTSkip("needs PersonaUITests/Fixtures/model_profiles_hub.py (PERSONA_PRESETS_HUB)")
        }
        hub = h
        try use("normal")
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let image = XCTAttachment(screenshot: app.screenshot())
            image.lifetime = .keepAlways
            add(image)
            let tree = XCTAttachment(string: app.debugDescription)
            tree.lifetime = .keepAlways
            add(tree)
        }
        app?.terminate()
        _ = mock("presets?release=1")
        try? use("normal")
        super.tearDown()
    }

    private func mock(_ path: String) -> [String: Any] {
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/" + path)!),
              let body = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
        return body
    }

    private func use(_ mode: String) throws {
        let body = mock("presets?mode=" + mode)
        guard body["mode"] as? String == mode else {
            throw XCTSkip("needs model_profiles_hub.py with its \(mode) fixture")
        }
    }

    private var started: [[String: Any]] { mock("started")["started"] as? [[String: Any]] ?? [] }
    private func any(_ id: String) -> XCUIElement { app.descendants(matching: .any).matching(identifier: id).firstMatch }
    private func text(containing value: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", value)).firstMatch
    }
    private func button(beginning value: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", value)).firstMatch
    }

    @discardableResult
    @MainActor private func see(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) not found:\n\(app.debugDescription)")
        return element
    }

    @MainActor private func wait(_ what: String, timeout: TimeInterval = 15, _ condition: () -> Bool) {
        let deadline = Date.now.addingTimeInterval(timeout)
        while Date.now < deadline, !condition() {
            RunLoop.current.run(until: Date.now.addingTimeInterval(0.05))
        }
        XCTAssertTrue(condition(), what)
    }

    @MainActor private func says(_ element: XCUIElement) -> String { (try? element.snapshot())?.label ?? "" }
    @MainActor private func selected(_ element: XCUIElement) -> Bool { (try? element.snapshot())?.isSelected ?? false }

    @discardableResult
    @MainActor private func reveal(_ element: XCUIElement, _ what: String) -> XCUIElement {
        for _ in 0..<8 where !(element.exists && element.isHittable) { app.swipeUp() }
        for _ in 0..<4 where !(element.exists && element.isHittable) { app.swipeDown() }
        return see(element, what)
    }

    @MainActor private func launchNewAgent(_ look: String = "light", largest: Bool = false, arguments: [String] = []) {
        app?.terminate()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-tab", "dev", "-showNewChat", "YES",
                               "-newChat.machine", "macbook", "-cwd.macbook", "/Users/me/code/blog",
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-ui.scheme", look]
            + (largest ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] : [])
            + arguments
        app.launchPinned()
        if !app.buttons["newChat.directory"].waitForExistence(timeout: 20) {
            app.terminate()
            app.launchPinned()
        }
        reveal(app.buttons["newChat.launcher"], "New Agent's launcher")
    }

    @MainActor private func shot(_ name: String, look: String) {
        let image = app.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = "agent-preset-\(name)-\(look)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? look
        try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir)
            .appending(path: "agent-preset-\(name)-\(look)-\(appearance).png"))
    }

    @MainActor private func openPresets() {
        reveal(any("newChat.preset"), "the active profile's Preset row").tap()
    }

    @MainActor private func reselectReviewAfterManualEdit() {
        openPresets()
        let review = see(app.buttons["newChat.preset.review"], "review after a manual edit")
        XCTAssertFalse(selected(review), "a manual launcher/model/effort edit must unmark the preset")
        review.tap()
        wait("reselecting review restores Opus") { says(reveal(app.buttons["newChat.model"], "the preset model")).contains("Opus") }
        _ = reveal(button(beginning: "Thinking Effort"), "Thinking Effort")
        see(text(containing: "High"), "reselecting review restores High")
    }

    @MainActor private func start() -> [String: Any] {
        let go = reveal(button(beginning: "Start Chat"), "Start Chat")
        wait("Start Chat enabled") { go.isEnabled }
        let before = started.count
        go.tap()
        wait("POST /api/v1/sessions") { started.count > before }
        return started.last ?? [:]
    }

    @MainActor func testPresetSelectionManualEditsReselectionAndStartJSON() throws {
        try use("normal")
        launchNewAgent()
        openPresets()
        see(text(containing: "Performance"), "the active profile")
        let quick = see(app.buttons["newChat.preset.quick"], "quick preset")
        XCTAssertTrue(quick.label.contains("Small, quick fixes"), quick.label)
        let review = see(app.buttons["newChat.preset.review"], "review preset")
        XCTAssertTrue(review.label.contains("Code review and design"), review.label)
        review.tap()
        wait("preset launcher") { says(app.buttons["newChat.launcher"]).contains("Claude Code") }
        wait("preset model") { says(app.buttons["newChat.model"]).contains("Opus") }
        _ = reveal(button(beginning: "Thinking Effort"), "Thinking Effort")
        see(text(containing: "High"), "preset effort")

        // Every field remains editable. A manual model pick unmarks; picking review again restores all its defaults.
        app.buttons["newChat.model"].tap()
        see(app.buttons["newChat.model.haiku"], "manual model pick").tap()
        wait("manual model is editable") { says(app.buttons["newChat.model"]).contains("Haiku") }
        reselectReviewAfterManualEdit()

        // So does a manual effort pick.
        button(beginning: "Thinking Effort").tap()
        see(app.buttons["Low"], "manual effort pick").tap()
        _ = reveal(button(beginning: "Thinking Effort"), "Thinking Effort")
        see(text(containing: "Low"), "manual effort is editable")
        reselectReviewAfterManualEdit()

        // And a manual launcher pick; the same preset then restores launcher, model and effort together.
        app.buttons["newChat.launcher"].tap()
        see(button(beginning: "sonnetcc"), "manual launcher pick").tap()
        wait("manual launcher is editable") { says(app.buttons["newChat.launcher"]).contains("sonnetcc") }
        reselectReviewAfterManualEdit()
        wait("preset launcher restored") {
            let value = says(app.buttons["newChat.launcher"])
            return value.contains("Claude Code") && !value.contains("sonnetcc")
        }

        let body = start()
        XCTAssertEqual(body["machine_id"] as? String, "macbook")
        XCTAssertEqual(body["launcher"] as? String, "claude")
        XCTAssertEqual(body["model"] as? String, "opus")
        XCTAssertEqual(body["effort"] as? String, "high")
        XCTAssertNil(body["tier"], "POST /sessions never sends a tier")
        XCTAssertNil(body["tiers_profile"])
    }

    @MainActor func testOlderHubHasNoPresetRowAndKeepsDefaults() throws {
        try use("older")
        launchNewAgent()
        let launcher = says(app.buttons["newChat.launcher"])
        let model = reveal(app.buttons["newChat.model"], "the existing default model")
        wait("the existing New Agent defaults") {
            launcher.contains("claude") && says(model).contains("model-chat")
        }
        XCTAssertFalse(any("newChat.preset").waitForExistence(timeout: 2), "an older hub must not grow a Preset row")
        let body = start()
        XCTAssertEqual(body["launcher"] as? String, "")
        XCTAssertNil(body["model"])
        XCTAssertNil(body["effort"])
        XCTAssertNil(body["tier"])
    }

    @MainActor func testOmpStartsWithTypedModelOrLauncherDefault() throws {
        let id = "openai/gpt-5.2"
        for (look, largest) in [("light", false), ("dark", false), ("light", true), ("dark", true)] {
            let size = largest ? "accessibility-xxxl" : "default"
            try use("omp")
            launchNewAgent(look, largest: largest, arguments: ["-mode.macbook", "plan"])
            app.buttons["newChat.launcher"].tap()
            reveal(button(beginning: "Oh My Pi"), "the native omp launcher").tap()
            let row = reveal(app.buttons["newChat.model"], "the omp model row")
            row.tap()
            let enter = reveal(app.buttons["newChat.model.other"], "Enter Model ID for the empty catalog")
            XCTAssertTrue(enter.label.contains("Enter Model ID"), enter.label)
            shot("omp-model-\(size)", look: look)
            enter.tap()
            let field = see(app.textFields["typedModel.field"], "the omp model ID field")
            field.tap()
            field.typeText(id)
            see(app.buttons["typedModel.use"], "use the model ID").tap()
            wait("the typed omp model") { says(row).contains(id) }
            reveal(row, "the chosen model on the form")
            shot("omp-selected-\(size)", look: look)
            if look == "dark" {
                row.tap()
                see(app.buttons["newChat.model.default"], "use omp's configured model").tap()
            }
            let body = start()
            XCTAssertEqual(body["launcher"] as? String, "omp")
            XCTAssertEqual(body["model"] as? String, look == "light" ? id : nil)
            XCTAssertNil(body["permission_mode"])
            XCTAssertNil(body["effort"])
            XCTAssertNil(body["ultracode"])
            XCTAssertNil(body["work_session"])
        }
    }

    @MainActor func testOmpInHerdrSpaceUsesThatHostsCatalog() throws {
        try use("omp-workspace")
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-dev.openChat", "",
                               "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001", "-openSpaceFromChat", "YES",
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-ui.scheme", "light"]
        app.launchPinned()
        see(app.buttons["Open a new tab in this space"], "the existing herdr space", timeout: 30).tap()
        reveal(app.buttons["newChat.launcher"], "the workspace launcher").tap()
        see(button(beginning: "Oh My Pi"), "the native omp launcher").tap()
        reveal(app.buttons["newChat.model"], "model override in the explicit herdr workspace").tap()
        see(app.buttons["newChat.model.other"], "enter a model for this host").tap()
        let field = see(app.textFields["typedModel.field"], "the model field")
        field.tap()
        field.typeText("vendor/workspace-model")
        see(app.buttons["typedModel.use"], "use the workspace model").tap()
        let body = start()
        XCTAssertEqual(body["host"] as? String, "herdr")
        XCTAssertEqual(body["workspace_id"] as? String, "w1")
        XCTAssertEqual(body["model"] as? String, "vendor/workspace-model")
    }

    @MainActor func testLockedLauncherModelIsExplainedAndNotSent() throws {
        try use("locked-model")
        launchNewAgent()
        openPresets()
        app.buttons["newChat.preset.locked"].tap()
        let problem = see(any("newChat.preset.problem"), "why the preset model cannot run")
        XCTAssertTrue(problem.label.contains("opus"), problem.label)
        reveal(any("newChat.model.locked"), "the launcher's own locked-model explanation")
        let body = start()
        XCTAssertEqual(body["launcher"] as? String, "sonnetcc")
        XCTAssertNil(body["model"], "a fixed --model must not be silently overridden")
        XCTAssertNil(body["tier"])
    }

    @MainActor func testUnsupportedPresetEffortIsExplainedAndNotSent() throws {
        try use("unsupported-effort")
        launchNewAgent()
        openPresets()
        app.buttons["newChat.preset.deep"].tap()
        let problem = see(any("newChat.preset.problem"), "why the preset effort cannot run")
        XCTAssertTrue(problem.label.contains("max"), problem.label)
        let body = start()
        XCTAssertEqual(body["launcher"] as? String, "claude")
        XCTAssertEqual(body["model"] as? String, "haiku")
        XCTAssertNil(body["effort"], "an unsupported effort must not silently run")
        XCTAssertNil(body["tier"])
    }

    @MainActor func testBlockedPresetNamesTheHealthProblemAndCannotBePicked() throws {
        try use("blocked-launcher")
        launchNewAgent()
        let before = says(app.buttons["newChat.launcher"])
        openPresets()
        let blocked = see(app.buttons["newChat.preset.blocked"], "blocked preset")
        XCTAssertFalse(blocked.isEnabled)
        see(text(containing: "Its key or sign-in was rejected"), "the launcher's health explanation")
        XCTAssertTrue(before.contains("Default:"), "a blocked preset did not change the default: \(before)")
    }

    @MainActor func testPresetCanExplicitlySendTheLaunchersDefaultModel() throws {
        try use("default-model")
        launchNewAgent()
        openPresets()
        app.buttons["newChat.preset.standard"].tap()
        let body = start()
        XCTAssertEqual(body["launcher"] as? String, "claude")
        XCTAssertEqual(body["model"] as? String, "default")
        XCTAssertEqual(body["effort"] as? String, "low")
        XCTAssertNil(body["tier"])
    }

    /// The second Claude launcher is unlocked, but its model list is held open. Start while that request is pending:
    /// the preset's model and effort must not be lost or replaced by the previous launcher's values.
    @MainActor func testStartWhileSecondClaudeModelsArePendingKeepsPresetFields() throws {
        try use("start-race")
        defer { _ = mock("presets?release=1") }
        launchNewAgent()
        openPresets()
        app.buttons["newChat.preset.race"].tap()
        let go = reveal(button(beginning: "Start Chat"), "Start Chat")
        XCTAssertFalse(go.isEnabled, "a pending preset model list must hold Start")
        _ = mock("presets?release=1")
        let body = start()
        XCTAssertEqual(body["launcher"] as? String, "slowcc")
        XCTAssertEqual(body["model"] as? String, "opus")
        XCTAssertEqual(body["effort"] as? String, "high")
        XCTAssertNil(body["tier"])
    }

    @MainActor private func capture(_ look: String, largest: Bool) throws {
        let suffix = largest ? "large" : "default"
        try use("normal")
        launchNewAgent(look, largest: largest)
        openPresets()
        let review = see(app.buttons["newChat.preset.review"], "review in \(look), largest=\(largest)")
        reveal(review, "the visible review preset")
        shot("new-agent-presets-\(suffix)", look: look)
        review.tap()
        reveal(app.buttons["newChat.model"], "the picked model")
        wait("the picked preset in \(look), largest=\(largest)") { says(app.buttons["newChat.model"]).contains("Opus") }
        shot("new-agent-picked-\(suffix)", look: look)
    }

    @MainActor func testNewAgentCapturesInLightAndDark() throws {
        for look in ["light", "dark"] { try capture(look, largest: false) }
    }

    @MainActor func testNewAgentCapturesAtLargestTextInLightAndDark() throws {
        for look in ["light", "dark"] { try capture(look, largest: true) }
    }
}
