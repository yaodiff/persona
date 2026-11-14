import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Session lists from both kinds of hub (docs/protocol/protocol.md「Session deltas」): an older one sends whole lists, a newer one a
/// snapshot without the Codex history, then deltas; GET /api/v1/sessions (FakeHub) brings the history. Either way the
/// app ends with what the hub has.
struct SessionDeltaTests {
    private func event(_ json: String) -> ServerEvent? { ServerEvent.decode(Data(json.utf8)) }

    private func list(_ json: String) throws -> (String, [Session]) {
        guard case .sessions(let m, let l)? = event(json) else { throw TestFailure("not a list: \(json)") }
        return (m, l)
    }

    private func delta(_ json: String) throws -> (String, [Session], [String]) {
        guard case .sessionsDelta(let m, let up, let gone)? = event(json) else { throw TestFailure("not a delta: \(json)") }
        return (m, up, gone)
    }

    /// What the hub has at the end of each test: a working one, an idle one, a live Codex one, and history.
    private let truthJSON = #"{"sessions":[{"machine_id":"box","session_id":"s1","status":"working","last_text":"writing"},"#
        + #"{"machine_id":"box","session_id":"c1","agent":"codex","source_kind":"codex_app_server","status":"working"},"#
        + #"{"machine_id":"box","session_id":"h1","agent":"codex","source_kind":"codex_history","status":"offline"}]}"#

    private func summary(_ l: [Session]) -> [String] {
        l.map { "\($0.machineID)/\($0.sessionID):\($0.status.rawValue):\($0.lastText ?? "")" }.sorted()
    }

    @Test func decodesADelta() throws {
        let (m, up, gone) = try delta(#"{"type":"sessions_delta","machine_id":"box","upsert":[{"session_id":"s1","status":"working"}],"remove":["s2"]}"#)
        #expect(m == "box")
        #expect(up.map(\.sessionID) == ["s1"])
        #expect(up.first?.machineID == "box")
        #expect(gone == ["s2"])
    }

    @Test func anOlderHubSendsWholeLists() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/sessions", json: truthJSON)
        var have = try await hub.client.sessions() // refresh
        let (_, first) = try list(#"{"type":"sessions","machine_id":"box","sessions":[{"session_id":"s1","status":"working","last_text":"reading"},{"session_id":"s2","status":"idle"},{"session_id":"h1","agent":"codex","source_kind":"codex_history","status":"offline"}]}"#)
        have = SessionPatch.snapshot(first, over: have, deltaHub: false)
        let (_, second) = try list(#"{"type":"sessions","machine_id":"box","sessions":[{"session_id":"s1","status":"working","last_text":"writing"},{"session_id":"c1","agent":"codex","source_kind":"codex_app_server","status":"working"},{"session_id":"h1","agent":"codex","source_kind":"codex_history","status":"offline"}]}"#)
        have = SessionPatch.snapshot(second, over: have, deltaHub: false)
        let truth = try await hub.client.sessions()
        #expect(summary(have) == summary(truth))
        // Its REST answer is the whole truth, connected or not.
        #expect(summary(SessionPatch.refreshed(truth, over: [], connectedToDeltaHub: false)) == summary(truth))
    }

    @Test func aNewerHubSendsDeltas() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/sessions", json: truthJSON)
        // Connected: the snapshot, no history.
        let (_, snap) = try list(#"{"type":"sessions","machine_id":"box","sessions":[{"session_id":"s1","status":"working","last_text":"reading"},{"session_id":"s2","status":"idle"}]}"#)
        var have = SessionPatch.snapshot(snap, over: [], deltaHub: true)
        // The refresh brings the history, and nothing older than what was pushed.
        have = SessionPatch.refreshed(try await hub.client.sessions(), over: have, connectedToDeltaHub: true)
        #expect(have.first { $0.sessionID == "s1" }?.lastText == "reading") // the push, not the REST answer
        #expect(have.contains { $0.sessionID == "h1" })
        // Deltas: one changes, one comes, one goes.
        let (_, up1, gone1) = try delta(#"{"type":"sessions_delta","machine_id":"box","upsert":[{"session_id":"s1","status":"working","last_text":"writing"},{"session_id":"c1","agent":"codex","source_kind":"codex_app_server","status":"working"}],"remove":["s2"]}"#)
        have = SessionPatch.apply(have, upsert: up1, remove: gone1)
        #expect(summary(have) == summary(try await hub.client.sessions()))
        // Reconnected: the snapshot again (still no history); the history the app has stays.
        let (_, again) = try list(#"{"type":"sessions","machine_id":"box","sessions":[{"session_id":"s1","status":"working","last_text":"writing"},{"session_id":"c1","agent":"codex","source_kind":"codex_app_server","status":"working"}]}"#)
        have = SessionPatch.snapshot(again, over: have, deltaHub: true)
        #expect(summary(have) == summary(try await hub.client.sessions()))
    }

    /// A live Codex session ends: the newer hub sends it as history (same id), and REST no longer listing a history
    /// session removes it.
    @Test func codexSessionsComeAndGo() throws {
        let live = try delta(#"{"type":"sessions_delta","machine_id":"box","upsert":[{"session_id":"c1","agent":"codex","source_kind":"codex_app_server","status":"working"}],"remove":[]}"#)
        var have = SessionPatch.apply([], upsert: live.1, remove: live.2)
        let ended = try delta(#"{"type":"sessions_delta","machine_id":"box","upsert":[{"session_id":"c1","agent":"codex","source_kind":"codex_history","status":"offline"}],"remove":[]}"#)
        have = SessionPatch.apply(have, upsert: ended.1, remove: ended.2)
        #expect(have.count == 1 && have[0].isCodexHistory)
        have = SessionPatch.refreshed([], over: have, connectedToDeltaHub: true)
        #expect(have.isEmpty)
    }

    /// No push carries the history, so a refresh is what moves it: the app's copy becomes the hub's (#2463), in place,
    /// while a live session the REST answer has older stays as pushed.
    @Test func aRefreshUpdatesTheHistoryTheAppHas() throws {
        let (_, snap) = try list(#"{"type":"sessions","machine_id":"box","sessions":[{"session_id":"s1","status":"working","last_text":"writing"}]}"#)
        var have = SessionPatch.snapshot(snap, over: [], deltaHub: true)
        let first = try list(#"{"type":"sessions","machine_id":"box","sessions":[{"session_id":"s1","status":"working","last_text":"reading"},{"session_id":"h1","agent":"codex","source_kind":"codex_history","status":"done"},{"session_id":"h2","agent":"codex","source_kind":"codex_history","status":"working"}]}"#).1
        have = SessionPatch.refreshed(first, over: have, connectedToDeltaHub: true)
        #expect(have.map(\.sessionID) == ["s1", "h1", "h2"])
        let later = try list(#"{"type":"sessions","machine_id":"box","sessions":[{"session_id":"s1","status":"idle","last_text":"old"},{"session_id":"h1","agent":"codex","source_kind":"codex_history","status":"idle"},{"session_id":"h2","agent":"codex","source_kind":"codex_history","status":"offline"}]}"#).1
        have = SessionPatch.refreshed(later, over: have, connectedToDeltaHub: true)
        #expect(have.map(\.sessionID) == ["s1", "h1", "h2"])
        #expect(have.map(\.status.rawValue) == ["working", "idle", "offline"])
        #expect(have[0].lastText == "writing")
    }

    @Test func aDeltaKeepsTheOrder() {
        func s(_ id: String, _ text: String = "") -> Session {
            var x = try! JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"\#(id)"}"#.utf8))
            x.lastText = text
            return x
        }
        let out = SessionPatch.apply([s("a"), s("b"), s("c")], upsert: [s("d"), s("b", "new")], remove: ["a"])
        #expect(out.map(\.sessionID) == ["b", "c", "d"])
        #expect(out[0].lastText == "new")
    }
}

private struct TestFailure: Error, CustomStringConvertible {
    let description: String
    init(_ d: String) { description = d }
}
