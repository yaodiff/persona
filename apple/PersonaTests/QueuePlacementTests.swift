import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Where a message sent from this phone is drawn before the transcript has it (#1461): one written while the agent
/// worked goes straight into the queue strip; one written while it was idle stays a bubble in the chat.
@MainActor
struct QueuePlacementTests {
    private let key = SessionKey(machineID: "m", sessionID: "queue-placement")

    private func session(_ status: String, queued: [String] = []) throws -> Session {
        let list = String(decoding: try JSONSerialization.data(withJSONObject: queued), as: UTF8.self)
        let json = #"{"machine_id":"m","session_id":"queue-placement","title":"t","cwd":"/tmp/p","status":"\#(status)","state":"\#(status)","queued":\#(list)}"#
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private func message(_ text: String, _ state: PendingMessage.State = .sending, queued: Bool = true) -> PendingMessage {
        var p = PendingMessage(text: text, images: [])
        p.state = state
        p.queuedMode = queued ? true : nil
        return p
    }

    @Test func aMessageWrittenWhileTheAgentWorkedIsDrawnInTheStrip() {
        for state in [PendingMessage.State.sending, .sent, .waiting, .failed] {
            let placed = QueuePlacement.split([message("then add tests", state)], working: true, queued: [], stripShown: true)
            #expect(placed.strip.count == 1 && placed.chat.isEmpty, "\(state) goes to the strip while the agent works")
        }
    }

    @Test func aMessageWrittenWhileTheAgentWasIdleStaysInTheChat() {
        let placed = QueuePlacement.split([message("hello", queued: false)], working: true, queued: [], stripShown: true)
        #expect(placed.strip.isEmpty && placed.chat.count == 1)
        let idle = QueuePlacement.split([message("hello", queued: false)], working: false, queued: [], stripShown: true)
        #expect(idle.strip.isEmpty && idle.chat.count == 1)
    }

    @Test func aFailedOneStaysInTheStripAfterTheTurnEnded() {
        let failed = QueuePlacement.split([message("x1", .failed)], working: false, queued: [], stripShown: true)
        #expect(failed.strip.count == 1)
        // One that went out is an ordinary message once no turn is left to queue it: a bubble until the transcript has it.
        let sent = QueuePlacement.split([message("x2", .sent)], working: false, queued: [], stripShown: true)
        #expect(sent.strip.isEmpty && sent.chat.count == 1)
    }

    @Test func aMessageClaudeReportsQueuedIsInNeitherPlaceAsAPendingOne() {
        let placed = QueuePlacement.split([message("then add some tests, please"), message("and docs please")], working: true,
                                          queued: ["then add some tests, please"], stripShown: true)
        #expect(placed.strip.map(\.text) == ["and docs please"], "the queued one is the strip's confirmed row")
        #expect(placed.chat.isEmpty)
    }

    @Test func withoutAStripTheChatKeepsEverything() {
        let placed = QueuePlacement.split([message("hello there")], working: true, queued: [], stripShown: false)
        #expect(placed.strip.isEmpty && placed.chat.count == 1)
    }

    @Test func sendingWhileTheAgentWorksMarksItQueueMode() throws {
        let hub = FakeHub()
        let model = AppModel()
        model.apiForTests = hub.client
        model.applySessions([MachineRef(group: "", id: "m"): [try session("working")]])
        #expect(model.sendNow(key, text: "while it works"))
        #expect(model.chat(key).pending.last?.queuedMode == true)

        model.applySessions([MachineRef(group: "", id: "m"): [try session("idle")]])
        #expect(model.sendNow(key, text: "while it rests"))
        #expect(model.chat(key).pending.last?.queuedMode == nil)
    }

    @Test func queueModeSurvivesTheOutbox() throws {
        let p = message("kept")
        let back = try JSONDecoder().decode(PendingMessage.self, from: JSONEncoder().encode(p))
        #expect(back.queuedMode == true)
        let old = try JSONDecoder().decode(PendingMessage.self, from: JSONEncoder().encode(message("old", queued: false)))
        #expect(old.queuedMode == nil)
    }
}
