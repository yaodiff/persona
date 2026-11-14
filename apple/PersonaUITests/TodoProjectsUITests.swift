import XCTest

/// To-dos in knowledge-base projects and deleting a project (#7077) against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB, with
/// MOCK_TODO_PROJECTS=1 for the grouped list): the detail files a to-do under a project and takes it out again, Group By ›
/// Project heads the list with the projects, and a deleted project comes back with Undo. PERSONA_SHOTS=<dir> keeps the
/// screenshots (PERSONA_SHOTS_APPEARANCE names them light / dark).
final class TodoProjectsUITests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    @MainActor func testTheDetailFilesAToDoUnderAProject() throws {
        let app = launch(["-tab", "assistant", "-openAssistantPage", "todos", "-openTodoTitled", "排查 /orders 接口变慢"]) // i18n-ignore
        let picker = app.buttons["todo.plan.project"]
        let list = app.collectionViews.firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 60), "the to-do did not open:\n\(app.debugDescription)")
        for _ in 0..<6 where !picker.isHittable { list.swipeUp(velocity: .slow) }
        XCTAssertTrue(picker.isHittable, "no Project row:\n\(app.debugDescription)")
        shot(app, "todo-project-none")
        picker.tap()
        let weather = app.buttons["天气 App"] // i18n-ignore: the mock's project
        XCTAssertTrue(weather.waitForExistence(timeout: 10), "the menu has no projects:\n\(app.debugDescription)")
        shot(app, "todo-project-menu")
        weather.tap()
        wait("the hub did not get the project") { self.todo("todo_orders")["project"] as? String == "weather-app" }
        shot(app, "todo-project-set")

        picker.tap()
        let none = app.buttons["不归类"] // i18n-ignore
        XCTAssertTrue(none.waitForExistence(timeout: 10))
        none.tap()
        wait("the project was not taken off") { self.todo("todo_orders")["project"] == nil }
    }

    @MainActor func testTheListGroupsByProject() throws {
        guard todo("todo_weather")["project"] as? String == "weather-app" else {
            throw XCTSkip("needs MOCK_TODO_PROJECTS=1")
        }
        // The list, whatever view an earlier test left (the calendar keeps itself per device).
        let app = launch(["-tab", "assistant", "-openAssistantPage", "todos", "-todos.view", "list", "-todos.grouping", "project"])
        let header = app.descendants(matching: .any).matching(identifier: "todos.project")
        XCTAssertTrue(header.firstMatch.waitForExistence(timeout: 60), "no project sections:\n\(app.debugDescription)")
        let labels = header.allElementsBoundByIndex.map(\.label)
        XCTAssertTrue(labels.contains { $0.contains("天气 App") }, "\(labels)") // i18n-ignore
        XCTAssertTrue(labels.contains { $0.contains("照片同步脚本") }, "\(labels)") // i18n-ignore
        shot(app, "todos-by-project")
    }

    @MainActor func testADeletedProjectComesBackWithUndo() throws {
        let app = launch(["-tab", "assistant", "-openAssistantPage", "knowledge"])
        let row = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "读书笔记")).firstMatch // i18n-ignore
        XCTAssertTrue(row.waitForExistence(timeout: 60), "no project row:\n\(app.debugDescription)")
        row.swipeLeft()
        let delete = app.buttons["删除"].firstMatch // i18n-ignore
        XCTAssertTrue(delete.waitForExistence(timeout: 5), "no Delete on the row:\n\(app.debugDescription)")
        delete.tap()

        // An action sheet on the iPhone, a popover on the iPad: it says what stays.
        let message = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "都还在")).firstMatch // i18n-ignore
        XCTAssertTrue(message.waitForExistence(timeout: 5), "no question before deleting:\n\(app.debugDescription)")
        shot(app, "project-delete-question")
        XCTAssertTrue(projects().contains("reading"), "deleted before the answer")
        let scope = app.sheets.count > 0 ? app.sheets.firstMatch : app.popovers.count > 0 ? app.popovers.firstMatch : app
        scope.buttons["删除"].firstMatch.tap() // i18n-ignore

        wait("the hub still has the project") { !self.projects().contains("reading") }
        let undo = app.buttons["撤销"] // i18n-ignore
        XCTAssertTrue(undo.waitForExistence(timeout: 5), "no Undo:\n\(app.debugDescription)")
        shot(app, "project-deleted")
        undo.tap()
        wait("Undo did not bring the project back") { self.projects().contains("reading") }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the row did not come back")
    }

    // MARK: Helpers

    @MainActor private func launch(_ args: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair] + args
        app.launchPinned()
        return app
    }

    private func get(_ path: String) -> [String: Any] {
        guard let data = try? Data(contentsOf: URL(string: hub + path)!) else { return [:] }
        return (try? JSONSerialization.jsonObject(with: data) as? [String: Any]) ?? [:]
    }

    private func todo(_ id: String) -> [String: Any] {
        (get("/api/v1/todos")["todos"] as? [[String: Any]] ?? []).first { $0["id"] as? String == id } ?? [:]
    }

    private func projects() -> [String] {
        (get("/api/v1/kb/projects")["items"] as? [[String: Any]] ?? []).compactMap { $0["id"] as? String }
    }

    private func wait(_ what: String, _ done: () -> Bool) {
        for _ in 0..<50 where !done() { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(done(), what)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)-\(look).png"))
        }
    }
}
