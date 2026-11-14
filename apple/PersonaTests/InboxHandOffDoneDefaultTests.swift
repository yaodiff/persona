import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Hand to AI files nothing unless the user asks (docs/assistant/inbox.md "Item actions"): these check that the rule
/// holds on the bar's queued path as well as on a direct send, and that the box is read when the send goes.
@MainActor
struct InboxHandOffDoneDefaultTests {
    private final class Calls {
        var started = 0
        /// What the Agent path was asked to file, in order.
        var files: [Bool] = []
        var done = 0
        var agentFiled = false
        var agentAccepted = true
        var handOffs = 0
        /// What went out to record a delivered hand-off: (item, session, kind, title, client id).
        var records: [(id: String, key: SessionKey, kind: InboxHandoffRecordKind, title: String, clientID: UUID)] = []
        /// What recording throws, in order; then it answers.
        var recordFailures: [Error] = []
    }

    private static let agent = SessionKey(machineID: "m", sessionID: "a")
    private static let item = """
    {"id":"lark:f1:11","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"Sample Contact",
     "preview":"合入了吗","count":1,"lark":{"feed_id":"f1","is_group":false}}
    """

    private func model(_ calls: Calls) -> InboxHandOffModel {
        InboxHandOffModel(itemID: "lark:f1:11", deps: .init(
            load: { _ in try JSON.decoder.decode(InboxItem.self, from: Data(Self.item.utf8)) },
            handOff: { _, _, _ in calls.handOffs += 1; return SessionKey(machineID: "server", sessionID: "main-1") },
            record: { id, key, kind, title, clientID in
                calls.records.append((id, key, kind, title, clientID))
                if !calls.recordFailures.isEmpty { throw calls.recordFailures.removeFirst() }
            },
            showMain: { _, _, _ in },
            plainToMain: { _ in SessionKey(machineID: "server", sessionID: "main-1") },
            startNew: { _ in calls.started += 1; return SessionKey(machineID: "server", sessionID: "new-1") },
            sendToAgent: { _, _, file in
                calls.files.append(file)
                guard calls.agentAccepted else { return .failed() }
                return InboxHandOffModel.AgentSendResult(delivered: true, error: nil, filed: calls.agentFiled)
            },
            open: { _, _ in },
            markDone: { calls.done += 1 }))
    }

    /// Recording is not awaited by `send`: wait for it rather than assert it has gone when the send answered.
    private func waitForRecords(_ calls: Calls, _ count: Int) async throws {
        for _ in 0..<200 where calls.records.count < count { try await Task.sleep(for: .milliseconds(10)) }
    }

    /// The rule itself: no target files the item by default.
    @Test func noTargetIsFiledByDefault() {
        #expect(!InboxHandOffModel.marksDone(.main))
        #expect(!InboxHandOffModel.marksDone(.newChat))
        #expect(!InboxHandOffModel.marksDone(.agent(Self.agent)))
    }

    /// The bar's chips and typed asks go through `queue`: an Agent target there leaves the item open too.
    @Test func theQueuedAskLeavesTheItemOpen() async throws {
        let calls = Calls(), m = model(calls)
        m.target = .agent(Self.agent)
        m.queue(ask: "a", after: .milliseconds(10))
        for _ in 0..<200 where m.sent == nil { try await Task.sleep(for: .milliseconds(10)) }
        #expect(calls.files == [false] && calls.done == 0 && m.sent?.key == Self.agent)
    }

    /// The box is read when the send goes, not when the target was picked: a choice made after it applies to the next
    /// send, including a retry of the same ask.
    @Test func theBoxIsReadAtTheSend() async {
        let calls = Calls(), m = model(calls)
        m.target = .agent(Self.agent)
        m.setMarkDone(true)
        #expect(await m.send(ask: "a"))
        m.setMarkDone(false)
        #expect(await m.send(ask: "a"))
        #expect(calls.files == [true, false] && calls.done == 1)
    }

    /// The outbox already filed it: choosing the box does not file it a second time.
    @Test func aFilingTheOutboxDidIsNotRepeated() async {
        let calls = Calls(), m = model(calls)
        calls.agentFiled = true
        m.setMarkDone(true)
        m.target = .agent(Self.agent)
        #expect(await m.send(ask: "a"))
        #expect(calls.done == 0)
    }

    // MARK: Recording a delivered hand-off (D3)

    /// A new conversation and an Agent are recorded after they went, with their session and kind, and the main chat is
    /// not recorded (its hand-off already links it). The record is not awaited: `send` answers first.
    @Test func aDeliveredHandOffIsRecordedAndTheMainChatIsNot() async throws {
        let calls = Calls(), m = model(calls)
        #expect(await m.send(ask: "a"))
        #expect(calls.handOffs == 1 && m.sent?.target == .main)
        try await Task.sleep(for: .milliseconds(100))
        #expect(calls.records.isEmpty)

        m.target = .newChat
        #expect(await m.send(ask: "b"))
        try await waitForRecords(calls, 1)
        #expect(calls.records.count == 1)
        let new = try #require(calls.records.first)
        #expect(new.id == "lark:f1:11" && new.key == SessionKey(machineID: "server", sessionID: "new-1"))
        #expect(new.kind == .conversation && new.title == "Sample Contact")

        m.target = .agent(Self.agent)
        #expect(await m.send(ask: "c"))
        try await waitForRecords(calls, 2)
        let agent = try #require(calls.records.last)
        #expect(agent.key == Self.agent && agent.kind == .agent && agent.title == "Sample Contact")
        #expect(Set(calls.records.map(\.clientID)).count == 2) // one id each, not one shared
    }

    /// A refused Agent send recorded nothing: nothing reached that conversation.
    @Test func aRefusedSendIsNotRecorded() async throws {
        let calls = Calls(), m = model(calls)
        calls.agentAccepted = false
        m.target = .agent(Self.agent)
        #expect(!(await m.send(ask: "a")))
        try await Task.sleep(for: .milliseconds(100))
        #expect(calls.records.isEmpty && m.sent == nil)
    }

    /// An older hub's answer to the record (400: no text) or any other failure leaves the hand-off as it was: no error
    /// on screen, the item still where it went, and the box still applied.
    @Test func aFailedRecordLeavesTheHandOffAlone() async throws {
        let calls = Calls(), m = model(calls)
        calls.recordFailures = [APIError(status: 400, message: "text_required")]
        m.target = .newChat
        m.setMarkDone(true)
        #expect(await m.send(ask: "a"))
        try await waitForRecords(calls, 1)
        #expect(m.error == nil && m.sent?.target == .newChat && calls.done == 1)
    }

    /// New Agent's page records the Agent it started, titled with the item (a session not yet listed has no title of its
    /// own): the item, never a session id, is what the chain will show.
    @Test func aStartedAgentIsRecordedWithTheItemsTitle() async throws {
        let calls = Calls(), m = model(calls)
        let prefill = await m.newAgent(ask: "a", group: "")
        #expect(prefill != nil)
        m.started(Self.agent, markDone: false)
        try await waitForRecords(calls, 1)
        let r = try #require(calls.records.first)
        #expect(r.kind == .agent && r.key == Self.agent && r.title == "Sample Contact" && r.id == "lark:f1:11")
    }

    /// The record request carries the session, its kind, the title and one client id, and no `text`: with no text the
    /// hub links the session to the item and sends nothing, so an older hub's 400 cannot make it a second message.
    @Test func theRecordBodyNamesTheSessionAndSendsNoText() async throws {
        let hub = FakeHub(), path = "/api/v1/inbox/lark:f1:11/handoff"
        hub.on("POST", path, json: #"{"item":{"id":"lark:f1:11"},"session":{"machine_id":"server","session_id":"main-1"}}"#)
        let key = SessionKey(group: "work", machineID: "laptop", sessionID: "a-1")
        let id = UUID()
        try await hub.client.inboxHandoffRecord("lark:f1:11", session: key, kind: .agent, title: "Release notes", clientID: id)
        let sent = try #require(hub.sent("POST", path).first)
        let body = try JSONSerialization.jsonObject(with: try #require(sent.body)) as? [String: Any]
        let session = try #require(body?["session"] as? [String: String])
        let keys = (body ?? [:]).keys.sorted()
        #expect(keys == ["client_id", "kind", "session", "title"])
        #expect(body?["text"] == nil)
        #expect(body?["client_id"] as? String == id.uuidString.lowercased()) // the hub's ids are lower case
        #expect(session == ["machine_id": "laptop", "session_id": "a-1"])
        #expect(body?["kind"] as? String == "agent" && body?["title"] as? String == "Release notes")

        try await hub.client.inboxHandoffRecord("lark:f1:11", session: key, kind: .conversation, title: "t", clientID: UUID())
        let again = try JSONSerialization.jsonObject(with: try #require(hub.sent("POST", path).last?.body)) as? [String: Any]
        #expect(again?["kind"] as? String == "conversation" && again?["text"] == nil)
    }
}
