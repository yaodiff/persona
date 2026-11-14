import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct ChatSendFeedbackTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let key = SessionKey(machineID: "example-computer", sessionID: "feedback")

    private func item(_ seq: Int, role: String, kind: ItemKind = .text, id: UUID? = nil) -> Item {
        var item = Item(id: "\(seq)#0", seq: seq, ts: now, role: role, kind: kind, text: "example",
                        toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        item.clientMsgID = id?.uuidString
        return item
    }

    private func session(status: String, failure: String? = nil) throws -> Session {
        var fields: [String: Any] = ["machine_id": "example-computer", "session_id": "feedback",
                                     "status": status, "state": status]
        if let failure { fields["failure_reason"] = failure }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: fields))
    }

    @Test func sendingIsImmediateAndDoesNotExpireWhileTheRequestRuns() {
        let feedback = ChatSendFeedback(id: UUID(), startedAt: now, afterSequence: 2)
        #expect(feedback.visible(now: now) == .sending)
        #expect(feedback.visible(now: now.addingTimeInterval(90)) == .sending)
        #expect(feedback.deadline == nil)
    }

    @Test func acceptanceDoesNotNeedRuntimeOrOutputAndStartsItsOwnDeadline() {
        let id = UUID()
        var feedback = ChatSendFeedback(id: id, startedAt: now.addingTimeInterval(-90), afterSequence: 2)
        feedback.accept(id, at: now)
        #expect(feedback.visible(now: now) == .processing)
        #expect(feedback.deadline == now.addingTimeInterval(60))
        #expect(feedback.visible(now: now.addingTimeInterval(60)) == nil)
    }

    @Test func oldResultsCannotReplaceANewerSend() {
        let old = UUID(), newest = UUID()
        var feedback = ChatSendFeedback(id: newest, startedAt: now, afterSequence: 2)
        feedback.accept(old, at: now)
        feedback.finish(old)
        #expect(feedback.phase == .sending)
        feedback.accept(newest, at: now)
        #expect(feedback.phase == .processing)
    }

    @Test func echoBeforeAcknowledgementKeepsTheAcceptanceBoundary() {
        let id = UUID()
        var feedback = ChatSendFeedback(id: id, startedAt: now, afterSequence: 2)
        feedback.echo(item(3, role: "user", id: id))
        #expect(feedback.phase == .sending)
        feedback.accept(id, at: now)
        #expect(feedback.phase == .processing)
        #expect(!feedback.answered(by: item(4, role: "assistant", kind: .thinking)))
        #expect(!feedback.answered(by: item(4, role: "assistant", kind: .toolUse)))
        #expect(feedback.answered(by: item(5, role: "assistant")))
    }

    @Test func lateAcknowledgementCannotRestartAFinishedTurn() {
        let id = UUID()
        var feedback = ChatSendFeedback(id: id, startedAt: now, afterSequence: 2)
        feedback.finish(id)
        feedback.accept(id, at: now)
        #expect(feedback.visible(now: now) == nil)
    }

    @Test func inFlightSnapshotsCannotCancelSendingOrItsLaterAcceptance() throws {
        let store = ChatStore(key: key)
        let pending = PendingMessage(text: "example", images: [])
        store.beginSendFeedback(pending)
        store.updateSendFeedback(try session(status: "blocked"), previous: try session(status: "idle"))
        #expect(store.sendFeedback?.phase == .sending)
        store.settleSendFeedback(pending.id, state: .sent)
        #expect(store.sendFeedback?.phase == .processing)
    }

    @Test func staleSnapshotsDoNotSuppressAcceptanceButFreshFailuresDo() throws {
        let store = ChatStore(key: key)
        let pending = PendingMessage(text: "example", images: [])
        let old = try session(status: "idle", failure: "previous failure")
        store.beginSendFeedback(pending)
        store.updateSendFeedback(old, previous: old)
        store.settleSendFeedback(pending.id, state: .sent)
        #expect(store.sendFeedback?.phase == .processing)
        store.updateSendFeedback(try session(status: "offline"), previous: old)
        #expect(store.sendFeedback?.phase == .finished)
    }

    @Test(arguments: [PendingMessage.State.failed, .waiting])
    func refusalOrNetworkWaitEndsTheOptimisticActivity(_ state: PendingMessage.State) {
        let store = ChatStore(key: key)
        let pending = PendingMessage(text: "example", images: [])
        store.beginSendFeedback(pending)
        store.settleSendFeedback(pending.id, state: state)
        #expect(store.sendFeedback?.phase == .finished)
        store.beginSendFeedback(pending)
        #expect(store.sendFeedback?.phase == .sending)
    }

    @Test func restoredAcceptedMessagesKeepProcessingWithoutAnotherRequest() {
        let store = ChatStore(key: key)
        var accepted = PendingMessage(text: "example", images: [])
        accepted.state = .sent
        accepted.createdAt = .now
        store.pending = [accepted]
        #expect(store.sendFeedback?.phase == .processing)
        #expect(store.sendFeedback?.id == accepted.id)
    }

    @Test func acknowledgementStillUpdatesActivityAfterEchoRemovedTheOutboxEntry() {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "send-feedback-\(UUID().uuidString)"))
        model.forgetRestoredForTests()
        let store = model.chat(key)
        let pending = PendingMessage(text: "example", images: [])
        store.pending = [pending]
        store.beginSendFeedback(pending)
        store.merge([item(3, role: "user", id: pending.id)])
        #expect(model.settleSend(key, pending.id, .sent) == nil)
        #expect(store.sendFeedback?.phase == .processing)
        store.merge([item(4, role: "assistant")])
        model.settleSend(key, pending.id, .sent)
        #expect(store.sendFeedback?.phase == .finished)
    }

    @Test func storeRetainsFeedbackWhenEchoRemovesThePendingBubble() {
        let store = ChatStore(key: key)
        let pending = PendingMessage(text: "example", images: [])
        store.pending = [pending]
        store.beginSendFeedback(pending)
        store.merge([item(3, role: "user", id: pending.id)])
        #expect(store.pending.isEmpty)
        store.settleSendFeedback(pending.id, state: .sent)
        #expect(store.sendFeedback?.phase == .processing)
        store.merge([item(4, role: "assistant")])
        #expect(store.sendFeedback?.phase == .finished)
        store.settleSendFeedback(pending.id, state: .sent)
        #expect(store.sendFeedback?.phase == .finished)
    }
}
