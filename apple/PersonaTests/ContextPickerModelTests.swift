import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Add as Context in the composer's + (#3721) against a FakeHub: each kind's list, narrowed to the words typed, and what a
/// pick carries to the model.
@MainActor
struct ContextPickerModelTests {
    private func deps(_ hub: FakeHub) -> AssistantDeps {
        AssistantDeps(api: { hub.client }, report: { _ in }, toast: { _ in }, open: { _ in })
    }

    @Test func toDosListOpenOnesFirstAndNarrowToTheWordsTyped() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"""
            {"todos":[{"id":"t1","title":"续签护照","agents":[],"done_at":"2026-11-01T08:00:00Z"},
                      {"id":"t2","title":"预订周五的餐厅","notes":"四个人，靠窗","agents":[]},
                      {"id":"t3","title":"整理发票","agents":[]}]}
            """#)
        let page = ContextPickerModel(kind: .todo, deps: deps(hub))
        await page.load(query: "")
        #expect(page.loaded && page.error == nil)
        #expect(page.rows.map(\.title).last == "续签护照", "a done to-do goes after the open ones")
        await page.load(query: "靠窗")
        #expect(page.rows.map(\.title) == ["预订周五的餐厅"], "the notes are searched too")
        #expect(hub.sent("GET", "/api/v1/todos").count == 1, "the list is read once and narrowed here")
    }

    @Test func knowledgeSearchesTheHubOnceWordsAreTyped() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/kb-search") { req in
            #expect(req.query["q"] == "博客")
            #expect(req.query["results"] == "1")
            return Data(#"""
                {"results":[{"entry":{"kind":"projects","id":"blog","name":"博客迁移到 Astro","one_line":"迁移计划","relevance":3},"count":2}]}
                """#.utf8)
        }
        let page = ContextPickerModel(kind: .knowledge, deps: deps(hub))
        await page.load(query: "博客")
        #expect(page.rows.map(\.title) == ["博客迁移到 Astro"])
    }

    @Test func aListThatCannotBeReadSaysWhy() async {
        let hub = FakeHub() // no Inbox route: the hub answers 404
        let page = ContextPickerModel(kind: .inbox, deps: deps(hub))
        await page.load(query: "")
        #expect(page.loaded && page.rows.isEmpty)
        #expect(page.error != nil)
    }

    @Test func aToDoCarriesItsTitleNotesAndIdAndLeadsBackToIt() throws {
        let todo = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t2","title":"预订周五的餐厅","notes":"四个人，靠窗","agents":[]}"#.utf8))
        let ref = ContextPickerModel.contextRef(todo)
        #expect(ref.kind == "context")
        #expect(ref.url == "persona://task/t2")
        #expect(ref.title == String(localized: "To-do · \("预订周五的餐厅")"))
        #expect(ref.text.contains("四个人，靠窗") && ref.text.contains("t2"))
        #expect(ref.quoteHeading == String(localized: "Quoting \(ref.label)"), "the to-do is quoted as a whole, not as selected text")
    }

    @Test func aNoteLeadsBackToItsEntry() {
        let entry = KBEntry(kind: "projects", id: "blog", name: "博客迁移到 Astro", oneLine: "迁移计划", relevance: 3)
        let ref = KnowledgeDetailView.contextRef(entry, detail: nil, link: true)
        #expect(ref.url == "persona://kb/projects/blog")
        #expect(ref.text == "迁移计划")
        #expect(KnowledgeDetailView.contextRef(entry, detail: nil).url.isEmpty, "Hand to AI's block stays as it was")
    }
}
