import XCTest

/// Correcting an entry by hand (#6997): confirmed facts deleted and brought back with Undo, and a link removed after the
/// question. Against scripts/mock/mock_hub.py with MOCK_KB_CORRECTIONS=1, fresh (PERSONA_MOCK_HUB); PERSONA_SHOTS=<dir>
/// keeps the screenshots.
final class KnowledgeCorrectionsUITests: XCTestCase {
    private var hub = ""
    private let facts = "负责天气 App 的图标和视觉。" // i18n-ignore

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_KB_CORRECTIONS=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        let meta = (get("/api/v1/kb/people/lilei")["detail"] as? [String: Any])?["meta"] as? [String: Any]
        guard (meta?["projects"] as? [String])?.contains("weather-app") == true else {
            throw XCTSkip("needs a fresh mock with MOCK_KB_CORRECTIONS=1")
        }
    }

    @MainActor func testDeletingConfirmedFactsOffersUndo() throws {
        put("/api/v1/kb/people/lilei", ["pinned": facts])
        let app = open("people/lilei")
        let text = app.staticTexts["kb.facts"]
        XCTAssertTrue(text.waitForExistence(timeout: 15), "no confirmed facts:\n\(app.debugDescription)")
        XCTAssertTrue(text.label.contains("图标和视觉"), text.label) // i18n-ignore
        shot(app, "facts")

        app.buttons["kb.facts.edit"].tap()
        XCTAssertTrue(app.textViews["kb.facts.editor"].waitForExistence(timeout: 5), "the editor did not open")
        shot(app, "facts-editor")
        app.buttons["kb.facts.delete"].tap()

        XCTAssertTrue(app.buttons["kb.facts.add"].waitForExistence(timeout: 5), "the facts did not go:\n\(app.debugDescription)")
        let undo = app.buttons["撤销"] // i18n-ignore
        XCTAssertTrue(undo.waitForExistence(timeout: 5), "no Undo:\n\(app.debugDescription)")
        shot(app, "facts-deleted")
        wait("the hub did not get the empty facts") { pinnedSent().last == "" }
        undo.tap()

        XCTAssertTrue(app.staticTexts["kb.facts"].waitForExistence(timeout: 10), "Undo did not bring the facts back")
        wait("Undo did not write the old facts back") { pinnedSent().last == facts }
    }

    @MainActor func testRemovingALinkAsksFirst() throws {
        let app = open("people/lilei")
        // The person's project is held in their own list, so it can be removed; a list row exists only once it is on screen.
        let row = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "天气 App")).firstMatch // i18n-ignore
        let list = app.collectionViews.firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 15), "the entry did not open:\n\(app.debugDescription)")
        for _ in 0..<6 where !row.isHittable { list.swipeUp(velocity: .slow) }
        XCTAssertTrue(row.isHittable, "no linked project:\n\(app.debugDescription)")
        row.swipeLeft()
        let swipe = app.buttons["移除关联"].firstMatch // i18n-ignore
        XCTAssertTrue(swipe.waitForExistence(timeout: 5), "no Remove Link on the row:\n\(app.debugDescription)")
        swipe.tap()

        // An action sheet on the iPhone, a popover on the iPad: the message says both entries are kept.
        let message = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "都会保留")).firstMatch // i18n-ignore
        XCTAssertTrue(message.waitForExistence(timeout: 5), "no question before removing:\n\(app.debugDescription)")
        shot(app, "unlink-question")
        XCTAssertTrue(updates().isEmpty, "something was written before the answer")
        let scope = app.sheets.count > 0 ? app.sheets.firstMatch : app.popovers.count > 0 ? app.popovers.firstMatch : app
        scope.buttons["移除关联"].firstMatch.tap() // i18n-ignore

        wait("the hub did not get remove_projects") { updates().contains { ($0["remove_projects"] as? [String]) == ["weather-app"] } }
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: row)
        waitForExpectations(timeout: 10)
        shot(app, "unlink-done")
    }

    // MARK: Helpers

    @MainActor private func open(_ entry: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openAssistantPage", "knowledge", "-showKnowledgeEntry", entry]
        app.launchPinned()
        return app
    }

    private func updates() -> [[String: Any]] {
        (get("/mock/kb_updates")["updates"] as? [[String: Any]] ?? []).filter { $0["id"] as? String == "lilei" }
            .filter { $0["remove_projects"] != nil || $0["remove_chats"] != nil }
    }

    private func pinnedSent() -> [String] {
        (get("/mock/kb_updates")["updates"] as? [[String: Any]] ?? []).filter { $0["id"] as? String == "lilei" }.compactMap { $0["pinned"] as? String }
    }

    private func get(_ path: String) -> [String: Any] {
        guard let data = try? Data(contentsOf: URL(string: hub + path)!) else { return [:] }
        return (try? JSONSerialization.jsonObject(with: data) as? [String: Any]) ?? [:]
    }

    private func put(_ path: String, _ body: [String: Any]) {
        var r = URLRequest(url: URL(string: hub + path)!)
        r.httpMethod = "PUT"
        r.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        r.httpBody = try? JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: "PUT \(path)")
        URLSession.shared.dataTask(with: r) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 10)
    }

    private func wait(_ what: String, _ done: () -> Bool) {
        for _ in 0..<50 where !done() { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(done(), what)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "kb-\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("kb-\(name)-\(look).png"))
        }
    }
}
