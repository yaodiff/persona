import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct AssistantViewsStoreTests {
    private func view(_ version: Int64, id: String = "view_example", conversation: String = "conversation", fallback: String = "进度 50%") -> ExtensionAssistantView {
        var body = ExtensionView()
        body.fallback = fallback
        return ExtensionAssistantView(id: id, conversation: conversation, version: version, view: body,
                                      createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:01Z")
    }

    private func item(_ view: ExtensionAssistantView, id: String = "feed_example") throws -> FeedItem {
        let encoded = try JSON.decoder.decode(JSONValue.self, from: JSONEncoder().encode(view))
        return FeedItem(id: id, conversation: view.conversation, at: .init(timeIntervalSince1970: 100),
                        kind: .view, ref: view.id, body: .object(["view": encoded]))
    }

    private func response(_ view: ExtensionAssistantView, creation: ExtensionAssistantView) throws -> String {
        let encoded = try JSON.decoder.decode(JSONValue.self, from: JSONEncoder().encode(view))
        let feed = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(item(creation)))
        let object: JSONValue = .object(["items": .array([feed]), "views": .object([view.id: encoded])])
        return String(decoding: try JSON.encoder.encode(object), as: UTF8.self)
    }

    @Test func onlyNewerVersionsReplaceTheCurrentCard() throws {
        let store = AssistantViewsStore()
        #expect(store.receive(view(3), conversation: "conversation", group: "a"))
        #expect(!store.receive(view(2), conversation: "conversation", group: "a"))
        #expect(!store.receive(view(3, fallback: "重复版本"), conversation: "conversation", group: "a"))
        store.receive(try item(view(1)), group: "a")
        store.receive(["view_example": view(2)], conversation: "conversation", group: "a")
        #expect(store.view("view_example", conversation: "conversation", group: "a")?.version == 3)
        #expect(store.view("view_example", conversation: "conversation", group: "a")?.view.fallback == "进度 50%")
        #expect(store.receive(view(4), conversation: "conversation", group: "a"))
        #expect(store.view("view_example", conversation: "conversation", group: "a")?.version == 4)
    }

    @Test func groupsAndConversationsHaveIndependentVersions() {
        let store = AssistantViewsStore()
        store.receive(view(9), conversation: "conversation", group: "a")
        store.receive(view(1), conversation: "conversation", group: "b")
        store.receive(view(2, conversation: "other"), conversation: "other", group: "a")
        #expect(store.view("view_example", conversation: "conversation", group: "a")?.version == 9)
        #expect(store.view("view_example", conversation: "conversation", group: "b")?.version == 1)
        #expect(store.view("view_example", conversation: "other", group: "a")?.version == 2)
        #expect(store.view("view_example", conversation: "missing", group: "a") == nil)
    }

    @Test func invalidOwnershipOrSnapshotKeysAreIgnored() throws {
        let store = AssistantViewsStore()
        #expect(!store.receive(view(1, conversation: "other"), conversation: "conversation", group: "a"))
        #expect(!store.receive(view(0), conversation: "conversation", group: "a"))
        #expect(!store.receive(view(1, id: ""), conversation: "conversation", group: "a"))
        store.receive(["wrong-key": view(1)], conversation: "conversation", group: "a")
        var feed = try item(view(1))
        feed.ref = "other-card"
        store.receive(feed, group: "a")
        #expect(store.snapshots.isEmpty)
    }

    @Test func aSnapshotRacingANewCardDoesNotRemoveIt() {
        let store = AssistantViewsStore()
        store.receive(view(1), conversation: "conversation", group: "a")
        store.receive([:], conversation: "conversation", group: "a")
        #expect(store.view("view_example", conversation: "conversation", group: "a") != nil)
        store.forget()
        #expect(store.snapshots.isEmpty)
    }

    @Test func feedLoadsCurrentSnapshotWithoutMovingItsCreationItem() async throws {
        let hub = FakeHub()
        let views = AssistantViewsStore()
        let todos = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: todos, viewStore: views)
        var api = hub.client
        api.group = "test-group"
        let route = "/api/v1/assistant/conversations/conversation/feed"
        hub.on("GET", route, json: try response(view(1), creation: view(1)))
        await tasks.load("conversation", refreshTodos: false, api: api)?.value
        let original = tasks.feed("conversation")
        #expect(original.count == 1 && original.first?.kind == .view)
        #expect(views.view("view_example", conversation: "conversation", group: "test-group")?.version == 1)
        hub.on("GET", route, json: try response(view(4), creation: view(1)))
        for refresh in tasks.reconnected(api: api) { await refresh.value }
        #expect(tasks.feed("conversation") == original)
        #expect(views.view("view_example", conversation: "conversation", group: "test-group")?.version == 4)
        #expect(hub.sent("GET", route).count == 2)
    }

    @Test func aResponseFinishingAfterForgetCannotRestoreOldHubState() async throws {
        let hub = FakeHub()
        let views = AssistantViewsStore()
        let todos = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: todos, viewStore: views)
        hub.on("GET", "/api/v1/assistant/conversations/conversation/feed", json: try response(view(1), creation: view(1)))
        var api = hub.client
        api.transport = { request in
            let data = try await hub.client.transport!(request)
            await MainActor.run { tasks.forget() }
            return data
        }
        await tasks.load("conversation", refreshTodos: false, api: api)?.value
        #expect(tasks.feeds.isEmpty && views.snapshots.isEmpty)
        await tasks.load("conversation", refreshTodos: false, api: hub.client)?.value
        #expect(tasks.feed("conversation").count == 1)
    }

    @Test func decodeLiveUpdateAndRejectConflictingEnvelopeOwnership() throws {
        let encoded = try JSON.decoder.decode(JSONValue.self, from: JSONEncoder().encode(view(2)))
        func event(_ conversation: String) throws -> Data {
            try JSON.encoder.encode(JSONValue.object(["type": .string("assistant_view"), "conversation": .string(conversation), "view": encoded]))
        }
        guard case .assistantView(let decoded)? = ServerEvent.decode(try event("conversation")) else {
            Issue.record("assistant_view event was not decoded")
            return
        }
        #expect(decoded == view(2))
        #expect(ServerEvent.decode(try event("other")) == nil)
        #expect(ServerEvent.decode(Data(#"{"type":"assistant_view","conversation":"conversation","view":{}}"#.utf8)) == nil)
    }

    @Test func aFeedCannotImportAnotherConversationsRows() async throws {
        let hub = FakeHub()
        let views = AssistantViewsStore()
        let todos = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: todos, viewStore: views)
        hub.on("GET", "/api/v1/assistant/conversations/conversation/feed", json: try response(view(1, conversation: "other"), creation: view(1, conversation: "other")))
        await tasks.load("conversation", refreshTodos: false, api: hub.client)?.value
        #expect(tasks.feed("conversation").isEmpty)
        #expect(views.snapshots.isEmpty)
    }

    @Test func anOpaqueConversationIDIsOneEncodedPathSegment() async throws {
        let hub = FakeHub()
        let path = "/api/v1/assistant/conversations/conversation%2Fpart%23one%3Ftwo/feed"
        hub.on("GET", path, json: #"{"items":[],"views":{}}"#)
        _ = try await hub.client.feed(conversation: "conversation/part#one?two")
        #expect(hub.sent("GET", path).count == 1)
    }

    @Test func olderHubsWithoutViewsStillDecodeTheirFeed() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/conversations/conversation/feed", json: #"{"items":[],"plans":{}}"#)
        let feed = try await hub.client.feed(conversation: "conversation")
        #expect(feed.items.isEmpty && feed.views == nil)
    }
}
