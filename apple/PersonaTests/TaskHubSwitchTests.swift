import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct TaskHubSwitchTests {
    /// A hub can take arbitrarily long to return a request after we've left it.
    private final class Reply {
        private var request: CheckedContinuation<Data, Never>?

        func receive() async -> Data {
            await withCheckedContinuation { request = $0 }
        }

        func waitForRequest() async throws {
            let clock = ContinuousClock()
            let deadline = clock.now.advanced(by: .seconds(3))
            while request == nil && clock.now < deadline { await Task.yield() }
            try #require(request != nil)
        }

        func finish(_ data: Data) {
            request?.resume(returning: data)
            request = nil
        }
    }

    private func store(_ todos: [TodoItem] = []) -> TodoStore {
        TodoStore(cachedTodos: todos, cacheWriter: nil, hubListChanged: {},
                  v1: .init(isOn: { false }, features: { .init() }, lookup: { _ in nil },
                            loadCache: { nil }, saveCache: nil))
    }

    private func today(_ id: String) throws -> Data {
        struct Answer: Encodable { let item: FeedItem }
        return try JSON.encoder.encode(Answer(item: FeedItem(id: id, conversation: "main", at: .now, kind: .today)))
    }

    @Test func todayIsRequestedOnceForEachVisitAndOldResponsesStayOut() async throws {
        let taskStore = TasksStore(todoStore: store(), toast: { _ in })
        let route = "/api/v1/assistant/conversations/main/today"
        let home = FakeHub(url: "https://home.invalid")
        let work = FakeHub(url: "https://work.invalid")
        let first = Reply()
        home.onAsync("POST", route) { _ in await first.receive() }
        let old = try #require(taskStore.loadToday("main", api: home.client))
        try await first.waitForRequest()

        taskStore.forget()
        work.on("POST", route) { _ in try today("work-today") }
        let workRead = try #require(taskStore.loadToday("main", api: work.client))
        await workRead.value
        #expect(taskStore.feed("main").map(\.id) == ["work-today"])

        taskStore.forget()
        let returning = FakeHub(url: "https://home.invalid")
        returning.on("POST", route) { _ in try today("home-new") }
        let fresh = try #require(taskStore.loadToday("main", api: returning.client))
        await fresh.value
        first.finish(try today("home-stale"))
        await old.value
        #expect(taskStore.feed("main").map(\.id) == ["home-new"])
        #expect(taskStore.loadToday("main", api: returning.client) == nil)
    }

    @Test func oldStartupAnswerCannotRestoreAnOldTodoOrBadge() async throws {
        struct Answer: Encodable { let todo: TodoItem }
        let stale = TodoItem(id: "same", title: "Home")
        let todos = store([stale])
        let tasks = TasksStore(todoStore: todos, toast: { _ in })
        let home = FakeHub()
        let gate = Reply()
        home.onAsync("POST", "/api/v1/todos/same/start/answer") { _ in await gate.receive() }
        let answer = Task {
            try await tasks.answerStart(stale, choice: "y", features: [TaskStartRemedy.answerFeature], api: home.client)
        }
        try await gate.waitForRequest()
        tasks.forget()
        todos.forget()
        gate.finish(try JSON.encoder.encode(Answer(todo: stale)))
        try await answer.value
        #expect(todos.todos.isEmpty)
        #expect(todos.badgeCount == 0)
        #expect(home.sent("GET", "/api/v1/todos").isEmpty)
    }

    @Test func anOldRetryDoesNotReleaseTheNewHubsRetryForTheSameID() async throws {
        let todo = TodoItem(id: "same", title: "Task")
        let tasks = TasksStore(todoStore: store(), toast: { _ in })
        let path = "/api/v1/todos/same/retry"
        let home = FakeHub(url: "https://home.invalid")
        let work = FakeHub(url: "https://work.invalid")
        let old = Reply()
        let fresh = Reply()
        home.onAsync("POST", path) { _ in await old.receive() }
        work.onAsync("POST", path) { _ in await fresh.receive() }

        let first = Task { await tasks.retry(todo, features: [], api: home.client) }
        try await old.waitForRequest()
        tasks.forget()
        let second = Task { await tasks.retry(todo, features: [], api: work.client) }
        try await fresh.waitForRequest()
        old.finish(Data("{}".utf8))
        #expect(!(await first.value))
        #expect(tasks.busyRetries.contains("same"))
        fresh.finish(Data("{}".utf8))
        #expect(!(await second.value))
        #expect(tasks.busyRetries.isEmpty)
    }

    @Test func queuedRefileDropsTheWriteIfTheHubAlreadyChanged() async {
        let home = FakeHub()
        let todos = store([TodoItem(id: "same", title: "Home")])
        let write = todos.refile("same", project: "home-project", api: home.client)
        todos.forget()
        #expect(!(await write.value))
        #expect(home.requests.isEmpty)
        #expect(todos.todos.isEmpty)
    }

    @Test func leavingTheHubDropsPendingBoardNavigationAndErrors() {
        let tasks = TasksStore(todoStore: store(), toast: { _ in })
        tasks.openAfterBoard = "old-task"
        tasks.openAgentAfterBoard = SessionKey(machineID: "box", sessionID: "old")
        tasks.error = "old error"
        tasks.forget()
        #expect(tasks.openAfterBoard == nil)
        #expect(tasks.openAgentAfterBoard == nil)
        #expect(tasks.error == nil)
    }
}
