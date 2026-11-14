import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct AssistantViewActionStateTests {
    private func record(_ revision: Int64? = 1, key: String = "item#one/0", status: String = "pending") -> ExtensionViewAction {
        ExtensionViewAction(key: key, action: "choose-b", values: ["choice": "b"], clientMsgId: "client-example", status: status,
                            revision: revision, updatedAt: "2026-01-01T00:00:01Z", at: "2026-01-01T00:00:00Z")
    }

    @Test func revisionControlsDeliveryStateWithoutChangingTheReply() {
        let store = AssistantViewsStore()
        #expect(store.receive(record(2), conversation: "conversation", group: "a"))
        #expect(store.receive(record(4, status: "sent"), conversation: "conversation", group: "a"))
        #expect(!store.receive(record(3), conversation: "conversation", group: "a"))
        #expect(!store.receive(record(4, status: "failed"), conversation: "conversation", group: "a"))
        #expect(store.action("item#one/0", conversation: "conversation", group: "a")?.status == "sent")
        #expect(store.receive(record(5, status: "failed"), conversation: "conversation", group: "a"))
        #expect(store.action("item#one/0", conversation: "conversation", group: "a")?.revision == 5)
    }

    @Test func alreadySelectedReplyCannotBeReplacedByAnotherChoice() {
        let store = AssistantViewsStore()
        store.receive(record(), conversation: "conversation", group: "a")
        for field in ["client", "action", "values", "page"] {
            var other = record(8, status: "sent")
            switch field {
            case "client": other.clientMsgId = "other-client"
            case "action": other.action = "choose-a"
            case "values": other.values = ["choice": "a"]
            default: other.page = "detail"
            }
            #expect(!store.receive(other, conversation: "conversation", group: "a"))
        }
        #expect(store.action("item#one/0", conversation: "conversation", group: "a") == record())
    }

    @Test func opaqueKeysAndScopesRemainIndependent() {
        let store = AssistantViewsStore()
        store.receive(record(7), conversation: "conversation", group: "a")
        store.receive(record(1, status: "sent"), conversation: "conversation", group: "b")
        store.receive(record(2), conversation: "other", group: "a")
        #expect(store.action("item#one/0", conversation: "conversation", group: "a")?.revision == 7)
        #expect(store.action("item#one/0", conversation: "conversation", group: "b")?.revision == 1)
        #expect(store.action("item#one/0", conversation: "other", group: "a")?.revision == 2)
        #expect(store.action("item#one", conversation: "conversation", group: "a") == nil)
    }

    @Test func updatingTheViewDoesNotClearAnAnswer() {
        let store = AssistantViewsStore()
        let chosen = record(1, key: "view_example", status: "sent")
        store.receive(chosen, conversation: "conversation", group: "a")
        store.receive(ExtensionAssistantView(id: "view_example", conversation: "conversation", version: 9), conversation: "conversation", group: "a")
        store.receiveActions([String: ExtensionViewAction](), conversation: "conversation", group: "a")
        #expect(store.action("view_example", conversation: "conversation", group: "a") == chosen)
        store.forget()
        #expect(store.actions.isEmpty && store.snapshots.isEmpty)
    }

    @Test func malformedRecordsAndSnapshotKeyMismatchAreIgnored() {
        let store = AssistantViewsStore()
        store.receiveActions(["wrong": record()], conversation: "conversation", group: "a")
        for field in ["key", "action", "client", "status", "revision"] {
            var malformed = record()
            switch field {
            case "key": malformed.key = ""
            case "action": malformed.action = ""
            case "client": malformed.clientMsgId = ""
            case "status": malformed.status = ""
            default: malformed.revision = 0
            }
            #expect(!store.receive(malformed, conversation: "conversation", group: "a"))
        }
        #expect(store.actions.isEmpty)
    }

    @Test func aLegacyRecordMayLockButCannotOverwriteANewerRevision() {
        let store = AssistantViewsStore()
        #expect(store.receive(record(nil), conversation: "conversation", group: "a"))
        #expect(store.receive(record(2, status: "sent"), conversation: "conversation", group: "a"))
        #expect(!store.receive(record(nil, status: "failed"), conversation: "conversation", group: "a"))
    }

    @Test func decodeActionEventWithoutBreakingOtherActionFields() throws {
        let value = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(record(2, status: "sent")))
        let payload = JSONValue.object(["type": .string("assistant_view_action"), "conversation": .string("conversation"), "action": value])
        guard case .assistantViewAction(let conversation, let action)? = ServerEvent.decode(try JSON.encoder.encode(payload)) else {
            Issue.record("assistant_view_action did not decode")
            return
        }
        #expect(conversation == "conversation" && action == record(2, status: "sent"))
        guard case .inbox? = ServerEvent.decode(Data(#"{"type":"inbox","action":"sync"}"#.utf8)) else {
            Issue.record("a separate event's action field broke its envelope")
            return
        }
        #expect(ServerEvent.decode(Data(#"{"type":"assistant_view_action","conversation":"conversation","action":{}}"#.utf8)) == nil)
    }

    @Test func feedSnapshotsAndReconnectRecoverTheSelectedState() async throws {
        let hub = FakeHub()
        let views = AssistantViewsStore()
        let todos = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: todos, viewStore: views)
        var api = hub.client
        api.group = "test-group"
        func response(_ action: ExtensionViewAction) throws -> String {
            let data = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(action))
            let body = JSONValue.object(["items": .array([]), "view_actions": .object([action.key: data])])
            return String(decoding: try JSON.encoder.encode(body), as: UTF8.self)
        }
        let route = "/api/v1/assistant/conversations/conversation/feed"
        hub.on("GET", route, json: try response(record(1)))
        await tasks.load("conversation", refreshTodos: false, api: api)?.value
        #expect(views.action("item#one/0", conversation: "conversation", group: "test-group")?.status == "pending")
        hub.on("GET", route, json: try response(record(3, status: "sent")))
        for task in tasks.reconnected(api: api) { await task.value }
        #expect(views.action("item#one/0", conversation: "conversation", group: "test-group")?.status == "sent")
        hub.on("GET", route, json: try response(record(2)))
        await tasks.load("conversation", refreshTodos: false, api: api)?.value
        #expect(views.action("item#one/0", conversation: "conversation", group: "test-group")?.revision == 3)
    }
}
