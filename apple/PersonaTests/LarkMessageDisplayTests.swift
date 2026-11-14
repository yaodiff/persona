import Foundation
import Testing
@testable import Persona
import PersonaWire

struct LarkMessageDisplayTests {
    private func message() throws -> LarkMessage {
        try JSON.decoder.decode(LarkMessage.self, from: Data(#"{"id":"m","position":"1","sender_name":"成员","is_mine":false,"type":"TEXT","text":"留存内容","parts":[{"kind":"text","text":"留存内容"}],"recalled":true,"retained":true,"edited":true,"reply_count":2,"edit_history":[{"edited_at":"2026-01-01T00:00:00Z","text":"private legacy","parts":[{"kind":"text","text":"private legacy"}],"olk":{"version":1,"state":"decrypted","bodyParts":[{"kind":"text","text":"private old version"}],"publicParts":[{"kind":"text","text":"公开外显"}]}}]}"#.utf8))
    }
    @Test func recalledPermissionStaysRecalledAndRequiresRetention() throws {
        var m = try message()
        let on = LarkMessageDisplay(accountID: "a", antiRecall: true, showEditHistory: true)
        #expect(!LarkMessageDisplay.hidden.showsBody(of: m))
        #expect(on.showsBody(of: m) && on.showsHistory(of: m))
        #expect(m.recalled)
        m.retained = nil
        #expect(!on.showsBody(of: m) && !on.showsHistory(of: m))
        m.retained = true; m.parts = []
        #expect(!on.showsBody(of: m))
        m.recalled = false
        #expect(on.showsBody(of: m))
        #expect(!LarkMessageDisplay.hidden.showsHistory(of: m))
    }
    @Test func historicalEncryptionRedactsEveryCachedBody() throws {
        let m = try message()
        #expect(m.editHistory?.first?.olk?.summary == "private old version")
        let data = try JSON.encoder.encode(m)
        #expect(!String(decoding: data, as: UTF8.self).contains("private"))
        let restored = try JSON.decoder.decode(LarkMessage.self, from: data)
        #expect(restored.recalled && restored.retained == true)
        #expect(restored.editHistory?.first?.olk?.summary == "公开外显")
        #expect(restored.editHistory?.first?.olk?.isOpen == false)
    }
    @Test func explicitNullEncryptionKeepsPlainForwardedPreview() throws {
        let record = try JSON.decoder.decode(JSONValue.self, from: Data(#"{"textPreview":"普通转发内容","olk":null}"#.utf8))
        #expect(LarkMessageDisplay.hidden.forwardedPreview(record) == "普通转发内容")
    }
    @Test func forwardedPreviewsRespectRecallAndNestedEncryptionNeverPersists() throws {
        let data = Data(#"{"kind":"card","metadata":{"variant":"merge-forward","previews":[{"senderName":"成员","text":"private preview"}],"messages":[{"id":"f","senderName":"成员","textPreview":"private legacy","isRecalled":true,"hasRetainedRecallContent":true,"parts":[{"kind":"text","text":"private legacy"}],"olk":{"version":1,"state":"decrypted","publicParts":[{"kind":"text","text":"公开外显"}],"bodyParts":[{"kind":"text","text":"private body"}]},"editHistory":[{"text":"private history","olk":{"state":"future","bodyParts":[{"kind":"text","text":"private unknown"}]}}]}]}}"#.utf8)
        let part = try JSON.decoder.decode(LarkPart.self, from: data)
        let record = try #require(part.meta("messages")?.array?.first)
        #expect(LarkMessageDisplay.hidden.forwardedPreview(record) == "消息已撤回")
        let on = LarkMessageDisplay(accountID: "a", antiRecall: true, showEditHistory: true)
        #expect(on.forwardedPreview(record) == "private body · 已撤回")
        let cached = try JSON.encoder.encode(part)
        #expect(!String(decoding: cached, as: UTF8.self).contains("private"))
        let restored = try JSON.decoder.decode(LarkPart.self, from: cached)
        let closed = try #require(restored.meta("messages")?.array?.first)
        #expect(on.forwardedPreview(closed) == "公开外显 · 已撤回")
        #expect(LarkMessageDisplay.hidden.forwardedPreview(closed) == "消息已撤回")
        #expect(restored.meta("previews")?.array?.first?["text"]?.string == "公开外显")
        #expect(JSON.parseDate("2026-01-01T10:00:00.123Z") != nil)
    }
    @Test func legacyDiskSnapshotsAreClosedBeforeDisplay() async throws {
        let key = "lark-trainer-legacy-" + UUID().uuidString
        defer { StateCache.remove(key) }
        let raw = try JSON.decoder.decode(JSONValue.self, from: Data(#"[{"id":"m","position":"1","sender_name":"成员","is_mine":false,"type":"TEXT","text":"private legacy","recalled":false,"reply_count":0,"olk":{"version":1,"state":"decrypted","publicParts":[{"kind":"text","text":"公开外显"}],"bodyParts":[{"kind":"text","text":"private body"}]}}]"#.utf8))
        StateCache.save(key, raw)
        for _ in 0..<100 {
            if StateCache.load(key, as: JSONValue.self) != nil { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        let restored = try #require(LarkMessage.loadCached(key)?.first)
        #expect(restored.olk?.isOpen == false)
        #expect(restored.displayText == "公开外显")
        for _ in 0..<100 {
            if StateCache.load(key, as: JSONValue.self) == raw.larkCached { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(StateCache.load(key, as: JSONValue.self) == raw.larkCached)
    }
    @Test @MainActor func readsAndWritesOnlyVerifiedAccountAndDoesNotRetryConflict() async throws {
        let hub = FakeHub()
        let path = "/api/v1/lark/message-display"
        hub.on("GET", path, json: #"{"account_id":"a","anti_recall":false,"show_edit_history":true}"#)
        let state = LarkMessageDisplayModel()
        await state.refresh(using: hub.client)
        #expect(state.policy.accountID == "a")
        hub.on("POST", path) { req in
            let body = try JSONSerialization.jsonObject(with: req.body!) as! [String: Any]
            #expect(body["account_id"] as? String == "a")
            #expect(body["key"] as? String == "anti_recall")
            #expect(body["value"] as? Bool == true)
            #expect(body["expected"] as? Bool == false)
            throw APIError(status: 409, message: "conflict")
        }
        await state.set("anti_recall", value: true, using: hub.client)
        #expect(state.snapshot == nil && !state.policy.antiRecall)
        #expect(state.problem?.contains("重新读取") == true)
        #expect(hub.sent("POST", path).count == 1)
        hub.on("GET", path, json: #"{"account_id":"b","anti_recall":true,"show_edit_history":false}"#)
        await state.refresh(using: hub.client)
        #expect(state.policy.accountID == "b" && !state.policy.showEditHistory)
    }
    @Test @MainActor func signedOutReadsAndWritesExplainWhereToSignIn() async {
        let hub = FakeHub(), state = LarkMessageDisplayModel()
        let path = "/api/v1/lark/message-display"
        let failure = APIError(status: 502, message: "signed out", fields: ["code": "lark_not_logged_in"])
        hub.on("GET", path) { _ in throw failure }
        await state.refresh(using: hub.client)
        #expect(state.snapshot == nil && state.problem?.contains("重新登录飞书网关") == true)
        hub.on("GET", path, json: #"{"account_id":"a","anti_recall":true,"show_edit_history":true}"#)
        await state.refresh(using: hub.client)
        hub.on("POST", path) { _ in throw failure }
        await state.set("anti_recall", value: false, using: hub.client)
        #expect(!state.policy.antiRecall && !state.policy.showEditHistory)
        #expect(state.problem?.contains("重新登录飞书网关") == true)
        #expect(hub.sent("POST", path).count == 1)
    }

    @Test @MainActor func offlineAndSeparateHubsCannotInheritEnabledSettings() async {
        let one = FakeHub(), two = FakeHub()
        one.on("GET", "/api/v1/lark/message-display", json: #"{"account_id":"a","anti_recall":true,"show_edit_history":true}"#)
        let a = LarkMessageDisplayModel(), b = LarkMessageDisplayModel()
        await a.refresh(using: one.client); await b.refresh(using: two.client)
        #expect(a.policy.antiRecall && !b.policy.antiRecall)
        one.on("GET", "/api/v1/lark/message-display") { _ in throw URLError(.notConnectedToInternet) }
        await a.refresh(using: one.client)
        #expect(!a.policy.antiRecall && !a.policy.showEditHistory)
    }

    @Test @MainActor func liveChangeDuringReadUsesFreshAccountEvenAfterViewCancellation() async throws {
        let old = DisplayRequestGate(), fresh = DisplayRequestGate()
        defer { old.close(); fresh.close() }
        let state = LarkMessageDisplayModel()
        let original = Task { await state.refresh(using: old.client) }
        try await waitUntil { old.requests.count == 1 }
        var queued = false
        let update = Task { queued = true; await state.refresh(using: fresh.client) }
        try await waitUntil { queued }
        original.cancel()
        try old.answer(#"{"account_id":"a","anti_recall":true,"show_edit_history":true}"#)
        try await waitUntil { fresh.requests.count == 1 }
        #expect(state.snapshot == nil, "the superseded account must not be published")
        try fresh.answer(#"{"account_id":"b","anti_recall":false,"show_edit_history":false}"#)
        await original.value; await update.value
        #expect(state.policy.accountID == "b" && !state.policy.antiRecall && !state.policy.showEditHistory)
        #expect(!state.loading)
        #expect(old.requests.count == 1 && fresh.requests.count == 1)
    }

    @Test @MainActor func liveChangeDuringSaveIsReadAfterAcknowledgementWithoutAnotherWrite() async throws {
        let initial = FakeHub()
        initial.on("GET", "/api/v1/lark/message-display", json: #"{"account_id":"a","anti_recall":false,"show_edit_history":false}"#)
        let state = LarkMessageDisplayModel()
        await state.refresh(using: initial.client)
        let write = DisplayRequestGate(), fresh = DisplayRequestGate()
        defer { write.close(); fresh.close() }
        let saving = Task { await state.set("anti_recall", value: true, using: write.client) }
        try await waitUntil { write.requests.count == 1 }
        await state.refresh(using: fresh.client)
        await state.refresh(using: fresh.client)
        #expect(fresh.requests.isEmpty, "notifications are coalesced until the write finishes")
        try write.answer(#"{"account_id":"a","anti_recall":true,"show_edit_history":false}"#)
        try await waitUntil { fresh.requests.count == 1 }
        #expect(!state.policy.antiRecall, "a stale acknowledgement must not supersede the live change")
        try fresh.answer(#"{"account_id":"a","anti_recall":false,"show_edit_history":true}"#)
        await saving.value
        #expect(!state.saving && !state.loading && !state.policy.antiRecall && state.policy.showEditHistory)
        #expect(write.requests.map(\.method) == ["POST"] && fresh.requests.map(\.method) == ["GET"])
    }

    @MainActor private func waitUntil(_ condition: () -> Bool) async throws {
        for _ in 0..<200 {
            if condition() { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        throw URLError(.timedOut)
    }
}

@MainActor private final class DisplayRequestGate {
    var requests: [LocalRequest] = []
    private var pending: [CheckedContinuation<Data, Error>] = []
    private var closed = false
    var client: APIClient {
        var api = APIClient(config: ServerConfig(url: URL(string: "https://display.example.com")!, token: "test-only"))
        api.transport = { [self] request in try await receive(request) }
        return api
    }
    private func receive(_ request: LocalRequest) async throws -> Data {
        guard !closed else { throw CancellationError() }
        requests.append(request)
        return try await withCheckedThrowingContinuation { pending.append($0) }
    }
    func answer(_ json: String) throws {
        let next = try #require(pending.first)
        pending.removeFirst()
        next.resume(returning: Data(json.utf8))
    }
    func close() {
        closed = true
        let remaining = pending; pending = []
        for item in remaining { item.resume(throwing: CancellationError()) }
    }
}
