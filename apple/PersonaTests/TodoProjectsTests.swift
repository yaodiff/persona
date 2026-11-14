import Foundation
import Testing
@testable import Persona

/// To-dos in knowledge-base projects (#7077): the choices and their order as the web's `projectChoices`, the list grouped
/// by project as the web's `groupTodosByProject`, and the PATCH that sets or clears it.
@MainActor
struct TodoProjectsTests {
    private func entry(_ id: String, _ name: String, extra: String?) -> KBEntry {
        KBEntry(kind: KBKind.projects.rawValue, id: id, name: name, relevance: 0, extra: extra)
    }

    @Test func notAProjectIsLeftOutAndLongTermComesFirst() {
        let list = TodoProjectLogic.projects([
            entry("weather", "Weather app", extra: "short · active"),
            entry("home", "Home", extra: "long · active"),
            entry("promo", "Promo mail", extra: "short · ignored"),
            entry("blog", "Blog", extra: "short · done"),
            entry("bare", "", extra: nil),
        ])
        #expect(list.map(\.slug) == ["weather", "home", "blog", "bare"])
        #expect(list.first { $0.slug == "bare" }?.name == "bare", "a project without a name shows its slug")
        #expect(TodoProjectLogic.choices(list, current: nil).map(\.slug) == ["home", "bare", "blog", "weather"])
        // The to-do's own project stays, first, while the list doesn't have it (not read yet, or since deleted).
        #expect(TodoProjectLogic.choices(list, current: "gone").map(\.slug) == ["gone", "home", "bare", "blog", "weather"])
        #expect(TodoProjectLogic.choices(list, current: "").count == 4)
        #expect(TodoProjectLogic.name("home", in: list) == "Home" && TodoProjectLogic.name("gone", in: list) == "gone")
    }

    @Test func openToDosGroupByProjectInTheListsOrderWithUnsortedLast() {
        let list = [TodoProject(slug: "weather", name: "Weather app", long: false), TodoProject(slug: "home", name: "Home", long: true)]
        let todos = [
            TodoItem(id: "1", title: "Loose"),
            TodoItem(id: "2", title: "Widget", project: "weather"),
            TodoItem(id: "3", title: "Backup", project: "home"),
            TodoItem(id: "4", title: "Release", project: "weather"),
            TodoItem(id: "5", title: "Old", project: "gone"),
        ]
        let sections = TodoProjectLogic.sections(todos, projects: list)
        #expect(sections.map(\.slug) == ["weather", "home", "gone", ""])
        #expect(sections.map(\.title).prefix(3) == ["Weather app", "Home", "gone"])
        #expect(sections[0].todos.map(\.id) == ["2", "4"] && sections[3].todos.map(\.id) == ["1"])
        #expect(TodoProjectLogic.sections([TodoItem(id: "1", title: "A", project: "home")], projects: list).count == 1, "no empty Unsorted")
    }

    @Test func aProjectIsSetOrClearedOnItsOwn() throws {
        let enc = JSONEncoder()
        enc.outputFormatting = .sortedKeys
        let set = String(decoding: try enc.encode(APIClient.TodoPlanChange(project: "weather")), as: UTF8.self)
        #expect(set == #"{"project":"weather"}"#)
        let clear = String(decoding: try enc.encode(APIClient.TodoPlanChange(project: "")), as: UTF8.self)
        #expect(clear == #"{"project":""}"#)
        // A plan change without a project leaves it alone.
        #expect(!String(decoding: try enc.encode(APIClient.TodoPlanChange(priority: "high")), as: UTF8.self).contains("project"))
        // Decoded from the hub; "" and absent are none.
        let decode = { (json: String) in try JSONDecoder().decode(TodoItem.self, from: Data(json.utf8)).project }
        #expect(try decode(#"{"id":"a","title":"A","project":"weather"}"#) == "weather")
        #expect(try decode(#"{"id":"a","title":"A","project":""}"#) == nil)
        #expect(try decode(#"{"id":"a","title":"A"}"#) == nil)
    }

    @Test func groupByOffersProjectWithoutTheDirectionsTree() {
        #expect(TodosGrouping.offered(hasTree: false) == [.status, .project])
        #expect(TodosGrouping.offered(hasTree: true) == [.status, .project, .direction])
        #expect(TodosGrouping.resolved(.project, hasTree: false) == .project)
        #expect(TodosGrouping.resolved(.direction, hasTree: false) == .status)
    }
}
