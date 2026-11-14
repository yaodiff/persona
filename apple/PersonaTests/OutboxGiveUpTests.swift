import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The outbox on a link that is up, against a FakeHub: a message whose sends keep coming back waiting fails after
/// `OutboxWait.connectedLimit`, and the waiting messages behind it in its chat go out in the same pass, or fail with it
/// under one notice when the hub does not list the chat.
@MainActor
@Suite(.serialized)
struct OutboxGiveUpTests {
    /// The app's disk: a folder of the test's own, not the simulator's app's.
    private let folder = FileManager.default.temporaryDirectory.appending(path: "outbox-give-up-\(UUID().uuidString)")
    private let key = SessionKey(machineID: "m", sessionID: "give-up-\(UUID().uuidString)")
    private var messagesPath: String { "/api/v1/sessions/m/\(key.sessionID)/messages" }

    private func model(_ hub: FakeHub, listsChat: Bool) -> AppModel {
        let model = AppModel(stateFolder: folder)
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        model.linksUpForTests = true
        if listsChat { model.applySessions([MachineRef(group: "", id: "m"): [session(key)]]) }
        return model
    }

    private func session(_ key: SessionKey) -> Session {
        let json = #"{"machine_id":"m","session_id":"\#(key.sessionID)","title":"t","cwd":"/tmp/p","status":"idle","state":"needs_you"}"#
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    /// A message the outbox holds; `stalledFor`: how long its sends have come back waiting on a link that is up.
    private func waiting(_ text: String, stalledFor: TimeInterval? = nil) -> PendingMessage {
        var p = PendingMessage(text: text, images: [])
        p.state = .waiting
        p.stalledSince = stalledFor.map { Date().addingTimeInterval(-$0) }
        return p
    }

    /// Runs one outbox pass to its end.
    private func flushed(_ model: AppModel) async throws {
        model.flushOutbox()
        for _ in 0..<300 where !model.flushingOutbox.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        try #require(model.flushingOutbox.isEmpty, "the pass did not end within 3 s")
    }

    /// A request that keeps getting no answer: the first message fails, and the one behind it goes now, not at the next
    /// reconnect.
    @Test func theNextMessageGoesOnceTheOneAheadOfItGivesUp() async throws {
        let first = waiting("Book the table for Friday", stalledFor: OutboxWait.connectedLimit + 1)
        let second = waiting("And ask about parking")
        let hub = FakeHub()
        hub.on("POST", messagesPath) { req in
            if req.headers[OpRetry.header]?.caseInsensitiveCompare(first.id.uuidString) == .orderedSame { throw URLError(.timedOut) }
            return Data(#"{"ok":true}"#.utf8)
        }
        let model = model(hub, listsChat: true)
        model.chat(key).pending = [first, second]

        try await flushed(model)

        #expect(model.chat(key).pending.map(\.state) == [.failed, .sent])
        #expect(hub.sent("POST", messagesPath).count == 2)
        #expect(model.toast == nil) // the chat is listed: no notice about it
        #expect(model.outboxRetry.isEmpty) // nothing waits any more
    }

    /// No row for the chat after a minute on a link that is up: every waiting message of it fails at once, under one
    /// notice, and none is posted.
    @Test func aChatTheHubDoesNotListFailsAllItsWaitingMessagesAtOnce() async throws {
        let hub = FakeHub()
        let model = model(hub, listsChat: false)
        model.chat(key).pending = [waiting("Book the table for Friday", stalledFor: OutboxWait.connectedLimit + 1),
                                   waiting("And ask about parking")]

        try await flushed(model)

        #expect(model.chat(key).pending.map(\.state) == [.failed, .failed])
        #expect(hub.sent("POST", messagesPath).isEmpty)
        #expect(model.toast?.style == .error)
        #expect(model.outboxRetry.isEmpty)
    }

    /// Within the limit a message keeps waiting, and however many chats came back waiting, their group gets one retry.
    @Test func waitsWithinTheLimitShareOneRetryPerGroup() {
        let model = model(FakeHub(), listsChat: false)
        let other = SessionKey(machineID: "m", sessionID: "give-up-other-\(UUID().uuidString)")
        let a = waiting("Book the table for Friday"), b = waiting("And ask about parking")
        model.chat(key).pending = [a]
        model.chat(other).pending = [b]

        #expect(model.settleSend(key, a.id, .waiting) == .waiting)
        #expect(model.settleSend(other, b.id, .waiting) == .waiting)

        #expect(model.chat(key).pending.first?.stalledSince != nil)
        #expect(Array(model.outboxRetry.keys) == [""])
        model.outboxRetry.values.forEach { $0.cancel() }
    }
}
