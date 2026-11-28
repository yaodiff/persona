import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Hand to AI > New Agent's ordinary start: starting and opening stay immediate, but the item record waits for the
/// first message's authoritative outbox delivery. Local enqueue, refusal, failure and deletion leave no record.
@MainActor
@Suite(.serialized)
struct InboxHandOffNewAgentDeliveryTests {
    private final class Calls {
        var records: [(itemID: String, key: SessionKey, kind: InboxHandoffRecordKind, title: String, clientID: UUID)] = []
        var waited: [(key: SessionKey, messageID: UUID)] = []
        var finished = 0
        var opened = 0
        var done = 0
        var result = InboxHandOffModel.AgentSendResult(delivered: true, error: nil, filed: false)
        var delay: Duration?
    }

    private let key = SessionKey(machineID: "m", sessionID: "new-agent-delivery")
    private static let item = #"{"id":"mail:a:1","source":"mail","kind":"mail","account":"a","account_name":"Mail","title":"An item","preview":"p","count":1}"#

    private func handOff(_ calls: Calls, app: AppModel? = nil) -> InboxHandOffModel {
        InboxHandOffModel(itemID: "mail:a:1", deps: .init(
            load: { _ in try JSON.decoder.decode(InboxItem.self, from: Data(Self.item.utf8)) },
            handOff: { _, _, _ in SessionKey(machineID: "m", sessionID: "main") },
            record: { id, key, kind, title, clientID in
                calls.records.append((id, key, kind, title, clientID))
            },
            showMain: { _, _, _ in },
            plainToMain: { _, _ in SessionKey(machineID: "m", sessionID: "main") },
            startNew: { _ in SessionKey(machineID: "m", sessionID: "new") },
            sendToAgent: { _, _, _ in .sent },
            waitForNewAgent: { key, message in
                calls.waited.append((key, message.id))
                if let app {
                    let result = await app.waitForHandOffDelivery(key, message, followRetries: true)
                    calls.finished += 1
                    return result
                }
                if let delay = calls.delay { try? await Task.sleep(for: delay) }
                calls.finished += 1
                return calls.result
            },
            open: { _, _ in calls.opened += 1 },
            markDone: { calls.done += 1 }))
    }

    private func prepared(_ calls: Calls, app: AppModel? = nil) async throws -> InboxHandOffModel {
        let model = handOff(calls, app: app)
        _ = try #require(await model.newAgent(ask: "Handle it", group: ""))
        return model
    }

    /// A model whose requests go to `hub`. `paired`: `hub` is also its hub-level group, with no socket, as in the other
    /// `refreshAssistant` tests, so the Persona page is `hub`'s default assistant and what `refreshAssistant` reads lands
    /// (`showsPage(of:)`), whether the simulator restored another pairing or none (#10987).
    private func app(_ hub: FakeHub, paired: Bool = false) -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        if paired {
            model.linkHooksForTests = (transport: hub.client.transport!, socket: { SyncSocket() })
            model.connectOnly(.demo)
        }
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        model.linksUpForTests = true
        let json = #"{"machine_id":"m","session_id":"new-agent-delivery","title":"Agent","cwd":"/tmp/p","status":"idle","state":"working"}"#
        let session = try! JSON.decoder.decode(Session.self, from: Data(json.utf8))
        model.applySessions([MachineRef(group: "", id: "m"): [session]])
        return model
    }

    private func start(_ model: InboxHandOffModel, app: AppModel, markDone: Bool = false) throws -> PendingMessage {
        let before = Set(app.chat(key).pending.map(\.id))
        #expect(app.sendNow(key, text: "context"))
        let message = try #require(app.chat(key).pending.last { !before.contains($0.id) })
        model.newAgentFirstMessage(message, for: key)
        model.started(key, markDone: markDone)
        return message
    }

    private func until(_ condition: () -> Bool) async throws {
        for _ in 0..<300 where !condition() { try await Task.sleep(for: .milliseconds(10)) }
        try #require(condition())
    }

    @Test func acceptedDeliveryRecordsOnceWithoutDelayingStartOrOpen() async throws {
        let calls = Calls(), model = try await prepared(calls)
        calls.delay = .milliseconds(100)
        let message = PendingMessage(text: "context", images: [])
        model.newAgentFirstMessage(message, for: key)
        model.started(key, markDone: true)
        model.open()
        #expect(calls.opened == 1 && calls.done == 1 && model.sent?.key == key)
        try await until { calls.waited.count == 1 }
        #expect(calls.waited.count == 1 && calls.records.isEmpty, "the local enqueue must not record")

        try await until { calls.records.count == 1 }
        let record = try #require(calls.records.first)
        #expect(record.itemID == "mail:a:1" && record.key == key && record.kind == .agent && record.title == "An item")
        #expect(calls.finished == 1)
    }

    @Test func localEnqueueRejectionDoesNotRecordOrWait() async throws {
        let calls = Calls(), model = try await prepared(calls)
        model.newAgentFirstMessage(nil, for: key)
        model.started(key, markDone: false)
        try await Task.sleep(for: .milliseconds(30))
        #expect(calls.waited.isEmpty && calls.records.isEmpty && calls.done == 0)
    }

    @Test func failedAndRefusedOutcomesDoNotRecord() async throws {
        for result in [InboxHandOffModel.AgentSendResult.failed("HTTP 409"), .failed("cancelled"), .failed()] {
            let calls = Calls(), model = try await prepared(calls)
            calls.result = result
            model.newAgentFirstMessage(PendingMessage(text: "context", images: []), for: key)
            model.started(key, markDone: false)
            try await until { calls.finished == 1 }
            #expect(calls.records.isEmpty)
        }
    }

    @Test func duplicateCallbacksProduceOneDeliveryWaitAndOneRecord() async throws {
        let calls = Calls(), model = try await prepared(calls)
        calls.delay = .milliseconds(50)
        let message = PendingMessage(text: "context", images: [])
        model.newAgentFirstMessage(message, for: key)
        model.newAgentFirstMessage(message, for: key)
        model.started(key, markDone: false)
        model.started(key, markDone: false)
        try await until { calls.records.count == 1 }
        try await Task.sleep(for: .milliseconds(30))
        #expect(calls.waited.count == 1 && calls.finished == 1 && calls.records.count == 1)
    }

    @Test func codexStartKeepsItsExistingAcceptedStartRecord() async throws {
        let calls = Calls(), model = try await prepared(calls)
        model.started(key, markDone: false)
        try await until { calls.records.count == 1 }
        #expect(calls.waited.isEmpty && calls.records.first?.key == key)
    }

    @Test func http409DoesNotRecord() async throws {
        let hub = FakeHub(), path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        hub.on("POST", path) { _ in
            throw APIError(status: 409, message: "Finish the draft first", fields: ["code": "input_draft", "delivered": "none"])
        }
        let app = app(hub)
        defer { app.outboxRetry.values.forEach { $0.cancel() } }
        let calls = Calls(), model = try await prepared(calls, app: app)
        let message = try start(model, app: app)
        try await until { app.chat(key).pending.first?.state == .waiting }
        #expect(calls.records.isEmpty)

        app.flushOutbox()
        try await until { app.chat(key).pending.first?.state == .failed && app.flushingOutbox.isEmpty }
        #expect(calls.records.isEmpty)
        #expect(hub.sent("POST", path).count == 1)

        app.discard(key, message)
        try await until { calls.finished == 1 }
        #expect(calls.records.isEmpty)
    }

    @Test func retryAfter409RecordsOnlyAfterAcceptedDelivery() async throws {
        let hub = FakeHub(), path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        var attempts = 0
        hub.on("POST", path) { _ in
            attempts += 1
            if attempts == 1 {
                throw APIError(status: 409, message: "Finish the draft first",
                               fields: ["code": "input_draft", "delivered": "none"])
            }
            return Data(#"{"ok":true}"#.utf8)
        }
        let app = app(hub)
        defer { app.outboxRetry.values.forEach { $0.cancel() } }
        let calls = Calls(), model = try await prepared(calls, app: app)
        let message = try start(model, app: app)
        try await until { app.chat(key).pending.first?.state == .waiting }

        app.flushOutbox()
        try await until { app.chat(key).pending.first?.state == .failed && app.flushingOutbox.isEmpty }
        #expect(calls.records.isEmpty && calls.finished == 0, "a refusal must not finish the record as delivered")

        let refused = try #require(app.chat(key).pending.first)
        app.retry(key, refused)
        try await until {
            app.chat(key).pending.first?.state == .waiting && app.chat(key).pending.first?.id != message.id
        }
        #expect(calls.records.isEmpty)

        app.flushOutbox()
        try await until { calls.records.count == 1 }
        #expect(attempts == 2 && calls.finished == 1 && calls.records.count == 1)
        let requests = hub.sent("POST", path)
        #expect(requests.count == 2)
        #expect(Set(requests.map { ($0.headers[OpRetry.header] ?? "").lowercased() }).count == 2,
                "a refused message retries under a new id, while the hand-off record keeps its prepared client id")
    }

    @Test func olderHubMessageOverridesSurviveRetryAndClearWithoutChangingAssistantConfig() async throws {
        // Reading the assistant can make its main conversation the current one, which is kept for the next launch.
        let savedCurrent = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(savedCurrent, forKey: "assistant.current") }
        let hub = FakeHub(), path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        var attempts = 0
        hub.on("POST", path) { _ in
            attempts += 1
            if attempts == 2 {
                throw APIError(status: 409, message: "Finish the draft first",
                               fields: ["code": "input_draft", "delivered": "none"])
            }
            return Data(#"{"ok":true}"#.utf8)
        }
        hub.on("GET", "/api/v1/assistant", json: """
        {"machine_id":"m","launcher":"saved-launcher","cwd":"/tmp/p","main":"\(key.sessionID)",
         "timezone_picked":true,"conversations":[]}
        """)
        let app = app(hub, paired: true)
        defer {
            app.outboxRetry.values.forEach { $0.cancel() }
            for link in app.links { link.live.disconnect() }
        }
        await app.refreshAssistant()
        let savedLauncher = app.assistantConfig?.launcher
        #expect(savedLauncher == "saved-launcher", "the settings are this test's hub's, not a hub the simulator kept")
        app.assistantLaunchers[key] = "team-api"
        app.assistantModels[key] = "claude-sonnet-5-5"

        let first = PendingMessage(text: "first", images: []), second = PendingMessage(text: "second", images: [])
        app.chat(key).pending = [first]
        app.settleSend(key, first.id, await app.sendPending(key, first))
        app.chat(key).pending.append(second)
        app.settleSend(key, second.id, await app.sendPending(key, second))
        let refused = try #require(app.chat(key).pending.first { $0.id == second.id })
        #expect(refused.state == .failed)

        app.retry(key, refused)
        try await waitForRefreshTest {
            let retry = app.chat(key).pending.first { $0.text == "second" }
            return retry?.state == .waiting && retry?.id != second.id
        }
        app.flushOutbox()
        try await waitForRefreshTest {
            app.flushingOutbox.isEmpty && hub.sent("POST", path).count == 3 &&
                app.chat(key).pending.first { $0.text == "second" }?.state == .sent
        }

        // AssistantTopBar.pickDefault: later messages inherit Settings > Assistant again.
        app.assistantLaunchers[key] = nil
        app.assistantModels[key] = nil
        let third = PendingMessage(text: "default", images: [])
        app.chat(key).pending.append(third)
        app.settleSend(key, third.id, await app.sendPending(key, third))

        let requests = hub.sent("POST", path)
        #expect(requests.count == 4)
        for request in requests.prefix(3) {
            let body = try #require(request.body)
            let fields = try JSONDecoder().decode([String: String].self, from: body)
            #expect(fields["launcher"] == "team-api")
            #expect(fields["model"] == "claude-sonnet-5-5")
        }
        let defaultBody = try #require(requests.last?.body)
        let defaultFields = try JSONDecoder().decode([String: String].self, from: defaultBody)
        #expect(defaultFields["text"] == "default")
        #expect(defaultFields["launcher"] == nil && defaultFields["model"] == nil)
        #expect(app.assistantConfig?.launcher == savedLauncher,
                "default changed from \(savedLauncher ?? "nil") to \(app.assistantConfig?.launcher ?? "nil")")
    }

    @Test func failedDeliveryDoesNotRecord() async throws {
        let hub = FakeHub(), path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        hub.on("POST", path) { _ in throw APIError(status: 500, message: "send failed") }
        let app = app(hub)
        defer { app.outboxRetry.values.forEach { $0.cancel() } }
        let calls = Calls(), model = try await prepared(calls, app: app)
        let message = try start(model, app: app)
        try await until { app.chat(key).pending.first?.state == .waiting }

        app.flushOutbox()
        try await until { app.chat(key).pending.first?.state == .failed && app.flushingOutbox.isEmpty }
        #expect(calls.records.isEmpty)

        app.discard(key, message)
        try await until { calls.finished == 1 }
        #expect(calls.records.isEmpty)
    }

    @Test func discardingAWaitingMessageDoesNotRecord() async throws {
        let app = app(FakeHub())
        defer { app.outboxRetry.values.forEach { $0.cancel() } }
        let calls = Calls(), model = try await prepared(calls, app: app)
        let message = try start(model, app: app)
        try await until { app.chat(key).pending.first?.state == .waiting }
        #expect(calls.records.isEmpty)

        app.discard(key, message)
        try await until { calls.finished == 1 }
        #expect(calls.records.isEmpty && app.chat(key).pending.isEmpty)
    }

    @Test func automaticRetryRecordsExactlyOnceAfterDelivery() async throws {
        let hub = FakeHub(), path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        var attempts = 0
        hub.on("POST", path) { _ in
            attempts += 1
            if attempts == 1 { throw URLError(.timedOut) }
            return Data(#"{"ok":true}"#.utf8)
        }
        let app = app(hub)
        defer { app.outboxRetry.values.forEach { $0.cancel() } }
        let calls = Calls(), model = try await prepared(calls, app: app)
        let message = try start(model, app: app)
        try await until { app.chat(key).pending.first?.state == .waiting }

        app.flushOutbox()
        try await until { attempts == 1 && app.flushingOutbox.isEmpty && app.chat(key).pending.first?.state == .waiting }
        #expect(calls.records.isEmpty, "a timed-out attempt still waiting in the outbox is not delivery")

        app.flushOutbox()
        try await until { calls.records.count == 1 }
        app.settleSend(key, message.id, .sent) // a duplicate acknowledgement cannot start another record
        model.newAgentFirstMessage(message, for: key)
        model.started(key, markDone: false)
        try await Task.sleep(for: .milliseconds(30))
        #expect(calls.records.count == 1 && calls.finished == 1)
        let requests = hub.sent("POST", path)
        #expect(requests.count == 2)
        #expect(Set(requests.map { ($0.headers[OpRetry.header] ?? "").lowercased() }).count == 1)
    }
}
