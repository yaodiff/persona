import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Hand to AI › New Agent… (#5933; docs/assistant/inbox.md "Item actions"): New Agent's page gets the item as the same
/// `<persona-context>` an Agent target gets, the ask as the first message, the item's title and its hub; nothing is sent
/// before Start there; once an Agent started, the item is filed as asked and its source remembers that Agent.
@MainActor
struct InboxHandOffNewAgentTests {
    private final class Calls {
        var loads = 0
        var sent = 0
        var done = 0
        var went: [(target: InboxHandOffModel.Target, key: SessionKey)] = []
        var loadFailures: [Error] = []
    }

    private static let item = """
    {"id":"lark:f1:11","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"Sample Contact","preview":"合入了吗",
     "count":1,"lark":{"feed_id":"f1","is_group":false},"context":"Sample Contact：合入了吗\\n我：还没",
     "mutes":[{"tag":"chat","value":"oc_sample","name":"Sample Contact"}]}
    """

    private static func decoded() throws -> InboxItem { try JSON.decoder.decode(InboxItem.self, from: Data(item.utf8)) }

    private func model(_ calls: Calls) -> InboxHandOffModel {
        InboxHandOffModel(itemID: "lark:f1:11", deps: .init(
            load: { _ in
                calls.loads += 1
                if !calls.loadFailures.isEmpty { throw calls.loadFailures.removeFirst() }
                return try Self.decoded()
            },
            handOff: { _, _, _ in calls.sent += 1; return SessionKey(machineID: "server", sessionID: "main") },
            record: { _, _, _, _, _ in },
            showMain: { _, _, _ in },
            plainToMain: { _ in calls.sent += 1; return SessionKey(machineID: "server", sessionID: "main") },
            startNew: { _ in calls.sent += 1; return SessionKey(machineID: "server", sessionID: "new") },
            sendToAgent: { _, _, _ in calls.sent += 1; return .sent },
            open: { calls.went.append(($0, $1)) },
            markDone: { calls.done += 1 }))
    }

    @Test func thePageGetsTheItemAsAnAgentTargetDoesAndNothingIsSent() async throws {
        let calls = Calls(), m = model(calls)
        let p = try #require(await m.newAgent(ask: "查一下背景", group: "home"))
        // The first message New Agent's page sends: the context block, then the ask, as an Agent target's text.
        #expect(p.contextBlock + "\n\n" + p.prompt == InboxHandOffSheet.message(try Self.decoded(), ask: "查一下背景"))
        #expect(p.card == InboxHandOffSheet.contextRef(try Self.decoded()).with(id: p.card.id))
        #expect(p.prompt == "查一下背景" && p.nameHint == "Sample Contact" && p.group == "home" && !p.markDone)
        #expect(calls.loads == 1 && calls.sent == 0 && calls.done == 0 && calls.went.isEmpty)
        #expect(m.target == .main && m.error == nil && !m.preparing)
    }

    @Test func alsoMarkAsDoneIsOffAsForEveryTargetUnlessTheUserSetIt() async throws {
        let m = model(Calls())
        #expect(!m.markDone) // the main chat's default, shown in the sheet
        let initial = try #require(await m.newAgent(ask: "a", group: ""))
        #expect(!initial.markDone)
        m.setMarkDone(false)
        let keptOpen = try #require(await m.newAgent(ask: "a", group: ""))
        #expect(!keptOpen.markDone)
        m.setMarkDone(true)
        let checked = try #require(await m.newAgent(ask: "a", group: ""))
        #expect(checked.markDone)
    }

    @Test func anItemThatCannotBeReadSaysSoWithNothingToRetry() async {
        let calls = Calls(), m = model(calls)
        calls.loadFailures = [URLError(.notConnectedToInternet)]
        let p = await m.newAgent(ask: "a", group: "")
        #expect(p == nil && m.error == String(localized: "Can't reach the hub") && m.lastAsk == nil)
        #expect(calls.sent == 0)
        // Picked again, it reads again and the error goes.
        #expect(await m.newAgent(ask: "a", group: "") != nil && m.error == nil)
    }

    @Test func theStartedAgentOpensAndTheItemIsFiledAsAsked() {
        let calls = Calls(), m = model(calls)
        let agent = SessionKey(machineID: "laptop", sessionID: "fresh")
        m.started(agent, markDone: true)
        #expect(calls.done == 1)
        m.open()
        #expect(calls.went.count == 1 && calls.went[0].target == .agent(agent) && calls.went[0].key == agent)

        let kept = Calls(), other = model(kept)
        other.started(agent, markDone: false)
        #expect(kept.done == 0)
    }

    /// The Mac (#5921): the started Agent opens only once the hand-off sheet has closed, and once.
    @Test func onTheMacTheStartedAgentOpensOnceTheSheetHasClosed() {
        let calls = Calls(), m = model(calls)
        let agent = SessionKey(machineID: "laptop", sessionID: "fresh")
        m.started(agent, markDone: true)
        m.openWhenClosed()
        #expect(calls.went.isEmpty && calls.done == 1)
        m.closed()
        m.closed()
        #expect(calls.went.count == 1 && calls.went[0].target == .agent(agent) && calls.went[0].key == agent)
    }

    @Test func theSourceRemembersTheStartedAgentNotNewAgent() throws {
        let suite = "handoff-new-agent-tests-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defaults.removePersistentDomain(forName: suite)
        defer { defaults.removePersistentDomain(forName: suite) }
        let memory = InboxHandOffMemory(group: "", defaults: defaults), it = try Self.decoded()
        let agent = SessionKey(machineID: "laptop", sessionID: "fresh")
        memory.rememberStarted(it, agent: agent)
        #expect(memory.entries == [.init(source: "lark|chat:oc_sample", target: "agent:" + agent.path)])
        // The next item from that source starts on it while it can take messages, and on the main chat once it can't.
        #expect(memory.resolved(for: it) { $0 == agent } == .init(target: .agent(agent), lastTime: true, gone: false))
        #expect(memory.resolved(for: it) { _ in false } == .init(target: .main, lastTime: false, gone: true))
    }
}

private extension ContextRef {
    /// The same reference under another id, to compare what it carries.
    func with(id: UUID) -> ContextRef {
        var r = self
        r.id = id
        return r
    }
}
