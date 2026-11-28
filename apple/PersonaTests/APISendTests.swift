import Foundation
import Testing
@testable import Persona
import PersonaWire

private actor PushRegistrations {
    private(set) var count = 0

    func register() -> Bool {
        count += 1
        return true
    }
}

private struct RequestBody: Codable, Equatable {
    var value: String
}

fileprivate struct NonMatchingRefusal: Sendable {
    var path: String
    var status: Int
    var code: String
}

@MainActor
struct APISendTests {
    private let key = SessionKey(machineID: "test", sessionID: "codex")
    private let path = "/api/v1/sessions/test/codex/messages"

    /// The watch's ids (RetryIDs): a retry after a failure keeps the failed try's id, a message that went through
    /// leaves the next one a new id, another message has its own.
    @Test func retriesKeepTheFailedTrysID() {
        var ids = RetryIDs<String>()
        let first = ids.id(for: "继续")
        #expect(UUID(uuidString: first) != nil)
        #expect(ids.id(for: "继续") == first) // the send failed: tapped again
        #expect(ids.id(for: "停一下") != first)
        ids.sent("继续")
        #expect(ids.id(for: "继续") != first)
    }

    /// A failed request and its retry keep the caller's id so the hub can deduplicate delivery.
    @Test func preservesMessageIDOnRetry() async throws {
        let hub = FakeHub()
        let id = "watch-message-1185"
        hub.on("POST", path) { _ in throw URLError(.timedOut) }
        await #expect(throws: URLError.self) {
            try await hub.client.send(key, text: "继续", clientMsgID: id)
        }
        hub.on("POST", path, json: "{}")
        try await hub.client.send(key, text: "继续", clientMsgID: id)

        let requests = hub.sent("POST", path)
        #expect(requests.count == 2)
        for request in requests {
            let body = try #require(request.body)
            let fields = try JSONDecoder().decode([String: String].self, from: body)
            #expect(fields["client_msg_id"] == id)
        }
    }

    /// Empty or whitespace clientMsgID is rejected before any request leaves the client, never replaced with a random UUID.
    @Test(arguments: ["", "   ", "\n\t"])
    func rejectsBlankClientMsgIDWithoutSending(_ blank: String) async {
        let hub = FakeHub()
        hub.on("POST", path, json: "{}")
        do {
            try await hub.client.send(key, text: "hello", clientMsgID: blank)
            Issue.record("expected codex_send_guard refusal for blank clientMsgID")
        } catch let error as APIError {
            #expect(error.code == "codex_send_guard")
            #expect(error.fields["delivered"] == "none")
        } catch {
            Issue.record("unexpected error: \(error)")
        }
        do {
            _ = try await hub.client.sendReply(key, text: "hello", clientMsgID: blank)
            Issue.record("expected codex_send_guard refusal for blank clientMsgID on sendReply")
        } catch let error as APIError {
            #expect(error.code == "codex_send_guard")
            #expect(error.fields["delivered"] == "none")
        } catch {
            Issue.record("unexpected error: \(error)")
        }
        #expect(hub.sent("POST", path).isEmpty)
    }

    /// Hand to Persona (`AppModel.askPersona`) sends a non-empty UUID client_msg_id and matching op id to an existing
    /// runtime main conversation, reuses that id on uncertain retry, and allocates a fresh id after delivery or refusal.
    @Test func askPersonaSendsClientMsgIDAndReusesItAcrossUncertainRetry() async throws {
        let hub = FakeHub()
        let mainPath = "/api/v1/sessions/server/main-runtime/messages"
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant",
               json: #"{"machine_id":"server","main":"main-runtime","launcher":"agy","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#)

        var mode = 0
        hub.on("POST", mainPath) { req in
            let body = try #require(req.body)
            let fields = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            let clientMsgID = fields["client_msg_id"] as? String ?? ""
            guard !clientMsgID.isEmpty else {
                throw APIError(status: 400, message: "missing client_msg_id", fields: ["code": "codex_send_guard", "delivered": "none"])
            }
            mode += 1
            if mode == 1 { throw URLError(.timedOut) }
            if mode == 4 { throw APIError(status: 409, message: "draft present", fields: ["code": "input_draft", "delivered": "none"]) }
            return Data("{}".utf8)
        }

        let transport = try #require(hub.client.transport)
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "ask-persona-" + UUID().uuidString))
        model.forgetRestoredForTests()
        model.linkHooksForTests = (transport: transport, socket: { SyncSocket() })
        model.connectOnly(.demo)
        await model.refreshAssistant()

        // 1) Uncertain timeout keeps the retry id; 2) retry reuses the exact same client_msg_id and X-Persona-Op-Id.
        await model.askPersona("Hand off task A")
        await model.askPersona("Hand off task A")
        // 3) After delivery succeeded, the same text gets a new client_msg_id.
        await model.askPersona("Hand off task A")
        // 4) A definitive refusal (delivered=none) clears the retry id; 5) next try gets a fresh id.
        await model.askPersona("Hand off task B")
        await model.askPersona("Hand off task B")

        let sent = hub.sent("POST", mainPath)
        try #require(sent.count == 5)
        let ids: [String] = try sent.map { req in
            let body = try #require(req.body)
            let fields = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            let id = try #require(fields["client_msg_id"] as? String)
            #expect(UUID(uuidString: id) != nil)
            #expect(req.headers[OpRetry.header] == id.lowercased())
            return id
        }
        #expect(ids[0] == ids[1], "uncertain failure retry must keep the same client_msg_id")
        #expect(ids[2] != ids[1], "a new send after success must allocate a new client_msg_id")
        #expect(ids[4] != ids[3], "a refusal with delivered=none must allocate a new client_msg_id on retry")
    }

    /// `AppModel.send` (used by Screen Share hand-back, urgent call replies, and clearContext) includes client_msg_id
    /// and matching op id, preserves the id on uncertain retry, and honors an explicit caller-provided id.
    @Test func appModelSendIncludesClientMsgIDAndPreservesRetryID() async throws {
        let hub = FakeHub()
        let runtimeKey = SessionKey(machineID: "mac", sessionID: "agy-1")
        let runtimePath = "/api/v1/sessions/mac/agy-1/messages"
        var attempt = 0
        hub.on("POST", runtimePath) { req in
            let body = try #require(req.body)
            let fields = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            let clientMsgID = fields["client_msg_id"] as? String ?? ""
            guard !clientMsgID.isEmpty else {
                throw APIError(status: 400, message: "missing client_msg_id", fields: ["code": "codex_send_guard", "delivered": "none"])
            }
            attempt += 1
            if attempt == 1 { throw URLError(.networkConnectionLost) }
            return Data("{}".utf8)
        }

        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "app-send-" + UUID().uuidString))
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        let sessionJSON = #"{"machine_id":"mac","session_id":"agy-1","title":"Runtime","cwd":"/tmp","status":"idle","state":"idle","source_kind":"runtime","capabilities":{"read":true,"send":true}}"#
        let session = try JSON.decoder.decode(Session.self, from: Data(sessionJSON.utf8))
        model.applySessions([MachineRef(group: "", id: "mac"): [session]])

        let first = await model.send(runtimeKey, text: "I'm done on the computer. Go ahead.", haptic: false)
        #expect(!first)
        let retry = await model.send(runtimeKey, text: "I'm done on the computer. Go ahead.", haptic: false)
        #expect(retry)
        let afterSuccess = await model.send(runtimeKey, text: "I'm done on the computer. Go ahead.", haptic: false)
        #expect(afterSuccess)
        let explicitID = UUID()
        let explicit = await model.send(runtimeKey, text: "Continue", haptic: false, clientMsgID: explicitID)
        #expect(explicit)

        let sent = hub.sent("POST", runtimePath)
        try #require(sent.count == 4)
        let ids: [String] = try sent.map { req in
            let body = try #require(req.body)
            let fields = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            let id = try #require(fields["client_msg_id"] as? String)
            #expect(UUID(uuidString: id) != nil)
            #expect(req.headers[OpRetry.header] == id.lowercased())
            return id
        }
        #expect(ids[0] == ids[1], "lost-connection retry must keep client_msg_id")
        #expect(ids[2] != ids[1], "subsequent send after success must use a new client_msg_id")
        #expect(ids[3] == explicitID.uuidString, "explicit caller clientMsgID must be preserved verbatim")
    }

    /// Inbox Hand to AI's unlisted-main fallback (`InboxHandOffModel.live` -> `plainToMain`) sends client_msg_id and op id.
    @Test func inboxHandOffLivePlainToMainFallbackSendsClientMsgID() async throws {
        let hub = FakeHub()
        let itemJSON = #"{"id":"lark:f1:11","source":"lark","kind":"dm","account":"feishu","account_name":"Lark","title":"Sample Contact","preview":"Status?","count":1,"lark":{"feed_id":"f1","is_group":false}}"#
        let item = try JSON.decoder.decode(InboxItem.self, from: Data(itemJSON.utf8))
        let tz = TimeZone.current.identifier
        let mainPath = "/api/v1/sessions/server/main-unlisted/messages"

        hub.on("GET", "/api/v1/inbox/lark:f1:11", json: itemJSON)
        hub.on("POST", "/api/v1/inbox/lark:f1:11/handoff") { _ in
            throw APIError(status: 404, message: "404 page not found")
        }
        hub.on("GET", "/api/v1/assistant",
               json: #"{"machine_id":"server","main":"main-unlisted","launcher":"agy","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#)
        hub.on("POST", mainPath) { req in
            let body = try #require(req.body)
            let fields = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            let clientMsgID = fields["client_msg_id"] as? String ?? ""
            guard !clientMsgID.isEmpty else {
                throw APIError(status: 400, message: "missing client_msg_id", fields: ["code": "codex_send_guard", "delivered": "none"])
            }
            return Data("{}".utf8)
        }

        let transport = try #require(hub.client.transport)
        let app = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "inbox-handoff-fallback-" + UUID().uuidString))
        app.forgetRestoredForTests()
        app.linkHooksForTests = (transport: transport, socket: { SyncSocket() })
        app.connectOnly(.demo)
        // Leave `server/main-unlisted` absent from `app.sessions` so `plainToMain` takes the direct `client.send` fallback.
        let handOff = InboxHandOffModel.live(item, model: app)
        let ok = await handOff.send(ask: "Check status")
        #expect(ok)

        let sent = hub.sent("POST", mainPath)
        try #require(sent.count == 1)
        let body = try #require(sent[0].body)
        let fields = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        let id = try #require(fields["client_msg_id"] as? String)
        #expect(UUID(uuidString: id) != nil)
        #expect(sent[0].headers[OpRetry.header] == id.lowercased())
    }

    @Test func aSuccessfulRequestSendsOnceWithoutRegistering() async throws {
        let hub = FakeHub(), registrations = PushRegistrations()
        var api = hub.client
        api.registerPush = { _ in await registrations.register() }
        hub.on("PUT", path, json: "{}")

        _ = try await api.request("PUT", path, body: RequestBody(value: "once"))

        #expect(hub.sent("PUT", path).count == 1)
        #expect(await registrations.count == 0)
    }

    /// Only this device-route refusal registers and retries; the same encoded body crosses both sends.
    @Test func pushRegistrationRequiredRegistersAndRetriesOnceWithTheSameBody() async throws {
        let devicePath = "/api/v1/devices/d/prefs"
        let hub = FakeHub(), registrations = PushRegistrations()
        var attempts = 0
        hub.on("PUT", devicePath) { _ in
            attempts += 1
            if attempts == 1 {
                throw APIError(status: 409, message: "register again", fields: ["code": APIClient.pushRegistrationRequired])
            }
            return Data("{}".utf8)
        }
        var api = hub.client
        api.registerPush = { _ in await registrations.register() }

        _ = try await api.request("PUT", devicePath, body: RequestBody(value: "retry"))

        #expect(attempts == 2)
        #expect(await registrations.count == 1)
        let requests = hub.sent("PUT", devicePath)
        try #require(requests.count == 2)
        let body = try #require(requests[0].body)
        #expect(requests[1].body == body)
        #expect(try JSONDecoder().decode(RequestBody.self, from: body) == RequestBody(value: "retry"))
    }

    /// A different code, status or route neither registers nor gains another send.
    @Test(arguments: [
        NonMatchingRefusal(path: "/api/v1/devices/d/prefs", status: 409, code: "input_draft"),
        NonMatchingRefusal(path: "/api/v1/devices/d/prefs", status: 500, code: APIClient.pushRegistrationRequired),
        NonMatchingRefusal(path: "/api/v1/sessions/test/codex/messages", status: 409, code: APIClient.pushRegistrationRequired),
    ])
    fileprivate func nonMatchingRefusalsDoNotRegisterOrRetry(_ refusal: NonMatchingRefusal) async throws {
        let hub = FakeHub(), registrations = PushRegistrations()
        hub.on("PUT", refusal.path) { _ in
            throw APIError(status: refusal.status, message: "refused", fields: ["code": refusal.code])
        }
        var api = hub.client
        api.registerPush = { _ in await registrations.register() }

        await #expect(throws: APIError.self) {
            _ = try await api.request("PUT", refusal.path, body: RequestBody(value: "once"))
        }

        #expect(hub.sent("PUT", refusal.path).count == 1)
        #expect(await registrations.count == 0)
    }
}
