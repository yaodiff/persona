import Foundation
import Testing
@testable import Persona
import PersonaWire

/// New Agent must keep its delivery observer across every retry that changes the pending message id, not only a
/// synchronous refusal retry. A receipt lookup for an uncertain send leaves the old message in `sending` first.
@MainActor
@Suite(.serialized)
struct InboxHandOffNewAgentAdversarialTests {
    private final class Calls {
        var records: [SessionKey] = []
        var waitsFinished = 0
    }

    private let key = SessionKey(machineID: "m", sessionID: "new-agent-uncertain-retry")
    private static let item = #"{"id":"mail:a:1","source":"mail","kind":"mail","account":"a","account_name":"Mail","title":"An item","preview":"p","count":1}"#

    private func app(_ hub: FakeHub) -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        model.linksUpForTests = true
        let json = #"{"machine_id":"m","session_id":"new-agent-uncertain-retry","title":"Agent","cwd":"/tmp/p","status":"idle","state":"working"}"#
        let session = try! JSON.decoder.decode(Session.self, from: Data(json.utf8))
        model.applySessions([MachineRef(group: "", id: "m"): [session]])
        return model
    }

    private func handOff(_ calls: Calls, app: AppModel) -> InboxHandOffModel {
        InboxHandOffModel(itemID: "mail:a:1", deps: .init(
            load: { _ in try JSON.decoder.decode(InboxItem.self, from: Data(Self.item.utf8)) },
            handOff: { _, _, _ in self.key },
            record: { _, key, _, _, _ in calls.records.append(key) },
            showMain: { _, _, _ in },
            plainToMain: { _, _ in self.key },
            startNew: { _ in self.key },
            sendToAgent: { _, _, _ in .sent },
            waitForNewAgent: { key, message in
                let result = await app.waitForHandOffDelivery(key, message, followRetries: true)
                calls.waitsFinished += 1
                return result
            },
            open: { _, _ in },
            markDone: { }))
    }

    private func until(_ condition: () -> Bool) async throws {
        for _ in 0..<300 where !condition() { try await Task.sleep(for: .milliseconds(10)) }
        try #require(condition())
    }

    @Test func confirmedDeliveryAfterUncertainRetryRecordsExactlyOnce() async throws {
        let hub = FakeHub(), path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        var attempts = 0
        hub.on("POST", path) { _ in
            attempts += 1
            if attempts == 1 {
                throw APIError(status: 500, message: "The send outcome is unknown", fields: ["delivered": "unknown"])
            }
            return Data(#"{"ok":true}"#.utf8)
        }
        let app = app(hub)
        defer { app.outboxRetry.values.forEach { $0.cancel() } }
        let calls = Calls(), model = handOff(calls, app: app)
        _ = try #require(await model.newAgent(ask: "Handle it", group: ""))

        #expect(app.sendNow(key, text: "context"))
        let original = try #require(app.chat(key).pending.first)
        model.newAgentFirstMessage(original, for: key)
        model.started(key, markDone: false)
        try await until { app.chat(key).pending.first?.state == .waiting }
        app.flushOutbox()
        try await until {
            app.chat(key).pending.first?.state == .failed && app.flushingOutbox.isEmpty
        }
        #expect(calls.records.isEmpty, "an uncertain, undelivered outcome must not record")
        let failed = try #require(app.chat(key).pending.first)
        #expect(failed.mayHaveArrived == true)

        // Retry asks for the old operation's receipt before it replaces the id. Hold that answer long enough for the
        // delivery observer to see the real intermediate `sending` state, as it does on a slow hub.
        let opPath = "/api/v1/ops/\(failed.id.uuidString.lowercased())"
        hub.onAsync("GET", opPath) { _ in
            try await Task.sleep(for: .milliseconds(350))
            return Data(#"{"state":"unknown"}"#.utf8)
        }
        app.retry(key, failed)
        try await until { hub.sent("GET", opPath).count == 1 }
        try await Task.sleep(for: .milliseconds(100))
        #expect(calls.records.isEmpty)

        try await until {
            app.chat(key).pending.first?.state == .waiting && app.chat(key).pending.first?.id != failed.id
        }
        app.flushOutbox()
        try await until { calls.records.count == 1 }
        #expect(attempts == 2 && calls.waitsFinished == 1 && calls.records == [key])

        // A repeated terminal signal and a repeated page callback cannot create another item record.
        let deliveredID = try #require(app.chat(key).pending.first).id
        app.settleSend(key, deliveredID, .sent)
        model.newAgentFirstMessage(original, for: key)
        model.started(key, markDone: false)
        try await Task.sleep(for: .milliseconds(30))
        #expect(calls.records == [key] && calls.waitsFinished == 1)
    }
}
