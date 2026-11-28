#if os(iOS)
import Foundation
import PersonaWire
import Testing
@testable import Persona

@MainActor struct ReplySpeechTurnsTests {
    private let key = SessionKey(machineID: "fixture", sessionID: "main")

    private func item(_ seq: Int, _ text: String, role: String = "assistant", kind: ItemKind = .text) -> Item {
        Item(id: "entry-\(seq)#0", seq: seq, role: role, kind: kind, text: text)
    }

    private func session(_ state: String, _ preview: String, revision: Int) throws -> Session {
        let data = try JSONSerialization.data(withJSONObject: [
            "machine_id": key.machineID, "session_id": key.sessionID, "state": state, "status": state == "working" ? "working" : "done",
            "assistant": true, "last_text": preview, "rev": revision
        ])
        return try JSON.decoder.decode(Session.self, from: data)
    }

    private func send(_ items: [Item], to model: AppModel, backlog: Bool = false) {
        model.handle(.items(key, items, backlog: backlog, hasMore: false, span: ItemSpan(), moreAfter: false), group: key.group)
    }

    private func take(_ window: ReplySpeechTurns, model: AppModel) -> Item? {
        window.take(from: model.chat(key), foldBackground: true, foldRelay: true, needsAttention: false)
    }

    private func settle(_ store: ChatStore) async throws {
        let deadline = ContinuousClock.now + .seconds(4)
        while store.isRebuilding, ContinuousClock.now < deadline { try await Task.sleep(for: .milliseconds(5)) }
        try #require(!store.isRebuilding)
    }

    @Test func liveItemCanPrecedeCoalescedWorkingAndDoneAndIsNeverRepeated() async throws {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        let baseline = try session("idle", "Previous", revision: 1)
        let window = ReplySpeechTurns(key: key, last: nil, session: baseline)
        model.replySpeech = window
        send([], to: model, backlog: true)
        let body = item(12, "Final answer")
        send([item(11, "Question", role: "user"), body], to: model)
        try await settle(model.chat(key))
        #expect(take(window, model: model) == nil)
        model.handle(.sessions(machineID: key.machineID, [try session("working", "Previous", revision: 2)]), group: key.group)
        let done = try session("unread", "Final answer", revision: 3)
        model.handle(.sessions(machineID: key.machineID, [done]), group: key.group)
        model.landPendingSessions()
        try #require(window.finished)
        let store = model.chat(key)
        store.streamed("New words still streaming", agents: false)
        #expect(take(window, model: model) == nil)
        store.dropLive()
        #expect(take(window, model: model)?.id == body.id)
        send([body], to: model)
        window.sessionChanged(try session("idle", "Final answer", revision: 3))
        #expect(take(window, model: model) == nil)
    }

    @Test func completionBeforeFinalTranscriptDoesNotReadInterimWords() async throws {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        let window = ReplySpeechTurns(key: key, last: nil, session: try session("working", "", revision: 1))
        model.replySpeech = window
        send([], to: model, backlog: true)
        send([item(1, "Question", role: "user"), item(2, "I will check.")], to: model)
        try await settle(model.chat(key))
        window.sessionChanged(try session("unread", "Final answer", revision: 3))
        #expect(take(window, model: model) == nil)
        send([item(3, "Final answer")], to: model)
        try await settle(model.chat(key))
        #expect(take(window, model: model)?.id == "entry-3#0")
    }

    @Test func unlockAndReconnectBacklogCannotOfferMissedReplies() async throws {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        let cached = try session("working", "Cached", revision: 1)
        let window = ReplySpeechTurns(key: key, last: nil, session: cached)
        model.replySpeech = window
        send([], to: model, backlog: true)
        window.reconnecting()
        window.sessionChanged(try session("unread", "Completed while away", revision: 2))
        let missed = item(2, "Completed while away")
        send([item(1, "Earlier", role: "user"), missed], to: model, backlog: true)
        send([missed], to: model)
        try await settle(model.chat(key))
        #expect(take(window, model: model) == nil)
        let next = item(4, "Fresh")
        send([item(3, "New question", role: "user"), next], to: model)
        window.sessionChanged(try session("idle", "Fresh", revision: 4))
        try await settle(model.chat(key))
        #expect(take(window, model: model)?.id == next.id)
    }

    @Test func theFinalLineAfterAToolIsReadWhenTheRowPreviewsTheEarlierReport() async throws {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        let window = ReplySpeechTurns(key: key, last: nil, session: nil)
        model.replySpeech = window
        send([], to: model, backlog: true)
        var tool = item(3, "", kind: .toolUse)
        tool.toolName = "send"
        tool.toolUseID = "send"
        var result = item(4, "sent", role: "user", kind: .toolResult)
        result.toolUseID = "send"
        let closing = item(5, "It has been sent.")
        send([item(1, "Report?", role: "user"), item(2, "The report is ready."), tool, result, closing], to: model)
        window.sessionChanged(try session("unread", "The report is ready. It has been sent.", revision: 6))
        try await settle(model.chat(key))
        #expect(take(window, model: model) == nil)
        #expect(window.take(from: model.chat(key), foldBackground: true, foldRelay: true,
                            needsAttention: false, confirmed: closing.id)?.id == closing.id)
        #expect(take(window, model: model) == nil)
    }

    @Test func recreatedObserverRejectsUnmarkedResendsBeforeTheSubscriptionPageAndRecoversAfterHTTPFill() async throws {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        let owner = UUID()
        model.subscribe(key, by: owner)
        defer { model.unsubscribe(key, by: owner) }
        let window = ReplySpeechTurns(key: key, last: nil, session: nil)
        model.replySpeech = window
        let old = item(2, "Finished while locked")
        send([item(1, "Earlier", role: "user"), old], to: model)
        window.sessionChanged(try session("unread", "Finished while locked", revision: 2))
        #expect(window.candidate == nil)
        model.handle(.items(key, [old], backlog: true, hasMore: false, span: ItemSpan(history: "same", to: 2),
                            moreAfter: true), group: key.group)
        _ = model.applyPrefetch(model.chat(key), items: [item(3, "More history")],
                                span: ItemSpan(history: "same", from: 2, to: 3), moreAfter: false)
        let fresh = item(5, "Fresh reply")
        send([item(4, "Next?", role: "user"), fresh], to: model)
        window.sessionChanged(try session("unread", "Fresh reply", revision: 5))
        try await settle(model.chat(key))
        #expect(take(window, model: model)?.id == fresh.id)
    }

    @Test func silentBackgroundRoundAndCardsNeverReachSpeech() async throws {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        let window = ReplySpeechTurns(key: key, last: nil, session: nil)
        model.replySpeech = window
        send([], to: model, backlog: true)
        var relay = item(1, "Background request", role: "user")
        relay.source = .object(["kind": .string("conversation"), "conversation_id": .string("leader-a")])
        var finish = item(2, "", kind: .toolUse)
        finish.toolName = "mcp__persona__supervision_finish"
        finish.toolUseID = "quiet"
        var receipt = item(3, #"{"visibility":"silent"}"#, role: "user", kind: .toolResult)
        receipt.toolUseID = finish.toolUseID
        send([relay, finish, receipt, item(4, "Background reply")], to: model)
        window.sessionChanged(try session("unread", "Background reply", revision: 4))
        try await settle(model.chat(key))
        #expect(take(window, model: model) == nil)
        var narration = item(6, "Interim")
        narration.narration = true
        send([item(5, "Question", role: "user"), narration, item(7, "Private reasoning", kind: .thinking)], to: model)
        #expect(window.candidate == nil)
    }
}
#endif
