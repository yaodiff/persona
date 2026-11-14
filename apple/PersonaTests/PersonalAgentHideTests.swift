import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Remove from List on the personal agents page (#138, docs/protocol/protocol.md "Removing a personal Agent from the
/// list"): the row goes at once and stays gone while the list is pulled again, a refusal brings it back, Restore puts it
/// back, and the prefs read the hub's `hidden` without needing it.
@MainActor struct PersonalAgentHideTests {
    private let row = PersonalAgentSession(id: "lark-bridge:a", source: "lark-bridge", sessionKey: "a", provider: "muse", title: "Alpha",
                                           canSend: true, unreadCount: 0, rankTimeMs: 1_800_000_000_000)
    private var hide: String { APIClient.agentPath(row.id) + "/hide" }
    private var unhide: String { APIClient.agentPath(row.id) + "/unhide" }
    private var hiddenJSON: String {
        #"{"sessions":[{"id":"lark-bridge:a","source":"lark-bridge","sessionKey":"a","provider":"muse","title":"Alpha","canSend":true,"unreadCount":0,"rankTimeMs":1800000000000}]}"#
    }

    @Test func removeTakesTheRowOutAndListsItUnderRemoved() async throws {
        let hub = FakeHub()
        hub.on("POST", hide, json: #"{"ok":true,"hidden":true}"#)
        hub.on("GET", "/api/v1/agents/hidden", json: hiddenJSON)
        let list = PersonalAgentListModel()
        let pulled = try await list.remove(row, api: hub.client)
        #expect(hub.sent("POST", hide).count == 1)
        #expect(list.going == [row.id])
        await pulled.value
        #expect(list.going.isEmpty)
        #expect(list.removed.map(\.id) == [row.id])
    }

    @Test func aRefusalBringsTheRowBack() async {
        let hub = FakeHub()
        hub.on("POST", hide) { _ in throw APIError(status: 404, message: "no such session") }
        let list = PersonalAgentListModel()
        await #expect(throws: APIError.self) { try await list.remove(row, api: hub.client) }
        #expect(list.going.isEmpty)
        #expect(list.removed.isEmpty)
    }

    @Test func restorePutsItBack() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/agents/hidden", json: hiddenJSON)
        hub.on("POST", unhide, json: #"{"ok":true,"hidden":false}"#)
        let list = PersonalAgentListModel()
        await list.loadRemoved(hub.client)
        #expect(list.removed.count == 1)
        hub.on("GET", "/api/v1/agents/hidden", json: #"{"sessions":null}"#)
        try await list.restore(row, api: hub.client)
        #expect(hub.sent("POST", unhide).count == 1)
        #expect(list.removed.isEmpty)
    }

    @Test func aHubWithoutTheRouteListsNothingRemoved() async {
        let list = PersonalAgentListModel()
        await list.loadRemoved(FakeHub().client)
        #expect(list.removed.isEmpty)
    }

    @Test func prefsReadHiddenAndDoWithoutIt() throws {
        let with = try JSON.decoder.decode(PersonalAgentPrefs.self, from: Data(#"{"pins":["x"],"hidden":{"lark-bridge:a":1800000000000}}"#.utf8))
        #expect(with.hidden == ["lark-bridge:a": 1_800_000_000_000])
        #expect(with.pins == ["x"])
        let without = try JSON.decoder.decode(PersonalAgentPrefs.self, from: Data(#"{"pins":[]}"#.utf8))
        #expect(without.hidden == nil)
    }
}
