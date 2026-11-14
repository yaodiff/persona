import Foundation
import Testing
@testable import Persona
import PersonaWire

/// An agent's live reply in its chat (#1309, docs/integrations/claude-integration.md "Live replies (MessageDisplay)"): the
/// words Claude shows before its transcript has them are drawn under the newest items, matched against them
/// (`ChatStore.agentStreamed`, `LiveText`); once the transcript lands the reply its row takes over in the same pass (no gap,
/// no double), and only the newest items touch it.
@MainActor
struct AgentLiveReplyTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: Date(timeIntervalSince1970: TimeInterval(seq)), role: role, kind: kind, text: text,
             toolUseID: tool, toolName: tool == nil ? nil : "Bash", toolInput: nil, isError: nil, image: nil)
    }

    /// A chat as `AppModel` feeds it for an agent's session: pushes continue from what is here, deltas go through the pacer.
    @MainActor final class Chat {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: "agent-live-\(UUID().uuidString)"))
        var pacer = DeltaPacer()
        private var to = 0

        /// The latest page from the end (the chat opening, a subscription's backlog).
        func page(_ items: [Item], hasMore: Bool = false) {
            to = items.map { $0.fold?.throughSeq ?? $0.seq }.max() ?? to
            store.apply(items, span: ItemSpan(history: "h", from: nil, to: to), backlog: true, hasMore: hasMore)
        }

        /// AppModel's `.items` for a push.
        func push(_ items: [Item]) {
            let from = to
            to = items.map(\.seq).max() ?? to
            store.apply(items, span: ItemSpan(history: "h", from: from, to: to))
        }

        /// AppModel's `.delta` for an agent's session.
        func delta(_ text: String) {
            let key = store.key
            let newest = pacer.pendingText(key)
            switch pacer.offer(key, text, now: .now) {
            case .draw(let t): store.streamed(t, agents: true, newest: newest)
            case .later(let wait):
                Task { @MainActor [weak self] in
                    try? await Task.sleep(for: .seconds(wait))
                    guard let self, let t = self.pacer.due(key, now: .now) else { return }
                    self.store.streamed(t, agents: true)
                }
            case .kept: break
            }
        }

        /// How a reply's words show: its row, its live copy, both (a double) or neither (a gap).
        func shows(_ reply: Item, _ words: String) -> String {
            let row = store.rows.contains { if case .assistant(let i) = $0 { i.id == reply.id } else { false } }
            let live = store.liveText.contains(words)
            return row && live ? "both" : row ? "row" : live ? "live" : "none"
        }

        /// The rows are built off the main actor: until they are in (at most 2 s).
        func settled() async {
            for _ in 0..<400 where store.isRebuilding { try? await Task.sleep(for: .milliseconds(5)) }
        }
    }

    private static func orders(_ a: [String]) -> [[String]] {
        guard a.count > 1 else { return [a] }
        return a.indices.flatMap { i -> [[String]] in
            var rest = a
            let x = rest.remove(at: i)
            return orders(rest).map { [x] + $0 }
        }
    }

    @Test func aDeltaShowsWhatTheTranscriptDoesNotHave() async {
        let chat = Chat()
        chat.page([item(1, .text, role: "user", text: "Write three lines.")])
        await chat.settled()
        chat.delta("Line 1.\n")
        await chat.settled()
        #expect(chat.store.liveIsAgents && chat.store.liveText == "Line 1.\n")
    }

    /// The node's last events for a reply, in the orders they can reach the chat (its items come before the "" that
    /// ends the live copy; the whole text can come before them or after, late), with and without a rebuild and the
    /// pacer's timer between them: the reply is on screen all along, once.
    @Test func theRowTakesOverFromTheLiveCopyWithoutAGapOrADouble() async {
        let reply = "Line 1.\nLine 2.\nLine 3."
        let landed = item(2, .text, text: reply)
        let orders = Self.orders(["delta", "item", "clear"]).filter { $0.firstIndex(of: "item")! < $0.firstIndex(of: "clear")! }
        let runs = orders.flatMap { order in (0..<4).map { (order, $0) } }
        let tasks = runs.map { order, waits in
            Task { @MainActor () -> String? in
                let chat = Chat()
                chat.page([item(1, .text, role: "user", text: "Write three lines.")])
                await chat.settled()
                chat.delta("Line 1.\n")
                try? await Task.sleep(for: .milliseconds(50))
                chat.delta("Line 1.\nLine 2.\n") // kept back by the pacer
                var seen: [String] = []
                for (k, event) in order.enumerated() {
                    switch event {
                    case "delta": chat.delta(reply)
                    case "item": chat.push([landed])
                    default: chat.delta("")
                    }
                    seen.append("\(event):\(chat.shows(landed, "Line 1."))")
                    if k < 2, waits & (1 << k) != 0 {
                        try? await Task.sleep(for: .milliseconds(250)) // a rebuild and the pacer's timer
                        seen.append("…:\(chat.shows(landed, "Line 1."))")
                    }
                }
                try? await Task.sleep(for: .milliseconds(300))
                await chat.settled()
                seen.append("end:\(chat.shows(landed, "Line 1."))")
                let ok = seen.last == "end:row" && chat.store.liveText.isEmpty && !seen.contains { $0.hasSuffix(":none") || $0.hasSuffix(":both") }
                return ok ? nil : order.joined(separator: ",") + " waits=\(waits): " + seen.joined(separator: " ")
            }
        }
        var bad: [String] = []
        for t in tasks { if let r = await t.value { bad.append(r) } }
        #expect(bad.isEmpty, "\(bad.prefix(10).joined(separator: "\n"))")
    }

    /// The fake hub's flow (MOCK_AGENT_LIVE): the words before a tool call stay live while it runs, land with its result,
    /// a whole copy of them that comes late shows nothing, and the words after the tool are a message of their own,
    /// though they begin alike.
    @Test func theWordsAfterAToolAreTheirOwnMessage() async {
        let first = "I'll update the names.\nThen I'll run the tests."
        let chat = Chat()
        chat.page([item(1, .text, role: "user", text: "Rename them and run the tests.")])
        await chat.settled()
        chat.delta("I'll update the names.\n")
        await chat.settled()
        #expect(chat.store.liveText == "I'll update the names.\n")
        try? await Task.sleep(for: .milliseconds(250)) // past the pacer
        chat.delta(first) // the final batch, before the tool call; the tool runs, nothing comes
        #expect(chat.store.liveText == first)
        let words = item(2, .text, text: first)
        chat.push([words, item(3, .toolUse, tool: "t1"), item(4, .toolResult, role: "user", text: "ok", tool: "t1")])
        #expect(chat.store.liveText == first, "the live copy stays until the row is in")
        await chat.settled()
        #expect(chat.shows(words, "update the names") == "row", "its row took over in the same pass")
        chat.delta("")
        try? await Task.sleep(for: .milliseconds(250))
        chat.delta(first) // late
        await chat.settled()
        #expect(chat.shows(words, "update the names") == "row", "a late whole copy of the message before is dropped")
        try? await Task.sleep(for: .milliseconds(250))
        chat.delta("I'll update the names.\n") // the next message, which begins alike
        await chat.settled()
        #expect(chat.store.liveText == "I'll update the names.\n")
        #expect(chat.shows(words, "Then I'll run the tests") == "row")
    }

    /// Only the newest items touch the live text: an older page is not the reply being written, even once a delta asks
    /// for it to be checked again.
    @Test func anOlderPageNeverTakesTheLiveText() async {
        let chat = Chat()
        // The latest page of a long turn starts in its middle (no prompt): the agent's words, a tool and its result.
        chat.page([item(11, .text, text: "Sea 1.\nSea 2."), item(12, .toolUse, tool: "t1"),
                   item(13, .toolResult, role: "user", text: "ok", tool: "t1")], hasMore: true)
        await chat.settled()
        chat.delta("Mountain 1.\n")
        await chat.settled()
        #expect(chat.store.liveText == "Mountain 1.\n")
        // The page before ends in words the live text begins like (a message of the turn before).
        chat.store.prepend([item(5, .text, role: "user", text: "Write about mountains."), item(6, .text, text: "Mountain 1.")], hasMore: false)
        await chat.settled()
        #expect(chat.store.liveText == "Mountain 1.\n")
        chat.push([]) // a push checks it again, against the newest items only
        await chat.settled()
        #expect(chat.store.liveText == "Mountain 1.\n")
        try? await Task.sleep(for: .milliseconds(250)) // past the pacer
        chat.delta("Mountain 1.\nMountain 2.\n")
        await chat.settled()
        #expect(chat.store.liveText == "Mountain 1.\nMountain 2.\n")
    }

    /// Opening a fold puts its steps in the chat; the live text is checked against the work as the page brought it.
    @Test func anOpenedFoldNeverChangesTheLiveText() async {
        let chat = Chat()
        let words = "Sea 1.\nSea 2."
        var fold = item(3, .fold)
        fold.id = "fold:3#0"
        fold.fold = Fold(items: 4, tools: 2, thoughts: 0, until: Date(timeIntervalSince1970: 6), throughSeq: 6, throughID: "6#0")
        chat.page([item(1, .text, role: "user", text: "Write about the sea, then list the folder."), item(2, .text, text: words), fold])
        await chat.settled()
        chat.delta(words) // its whole copy, late: the folded page has it
        await chat.settled()
        #expect(chat.store.liveText.isEmpty)
        chat.store.unfold(fold, into: [item(3, .toolUse, tool: "a"), item(4, .toolResult, role: "user", text: "ok", tool: "a"),
                                       item(5, .toolUse, tool: "b"), item(6, .toolResult, role: "user", text: "ok", tool: "b")])
        await chat.settled()
        #expect(chat.store.items.map(\.seq) == [1, 2, 3, 4, 5, 6])
        chat.push([]) // a push checks it again
        try? await Task.sleep(for: .milliseconds(250)) // past the pacer
        chat.delta(words)
        await chat.settled()
        #expect(chat.store.liveText.isEmpty, "the copy is still in the items, the steps opened or not")
    }

    /// A socket that started over drops the live text and what it came from: the next items do not bring it back.
    @Test func droppingTheLiveTextForgetsItsMessage() async {
        let chat = Chat()
        chat.page([item(1, .text, role: "user", text: "Go.")])
        await chat.settled()
        chat.delta("Line 1.\n")
        await chat.settled()
        chat.store.dropLive()
        chat.push([item(2, .thinking, text: "…")])
        await chat.settled()
        #expect(chat.store.liveText.isEmpty)
    }

    // MARK: Through the app

    private func session(_ id: String, assistant: Bool) throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"\#(id)","status":"idle","state":"working","pane_id":"p1","assistant":\#(assistant)}"#.utf8))
    }

    /// An agent's delta draws in its chat as an agent's; the headless assistant's stays its whole reply so far.
    @Test func theAppTellsAnAgentsDeltaFromTheAssistants() throws {
        let model = AppModel()
        model.applySessions([MachineRef(group: "", id: "box"): [try session("agent", assistant: false), try session("main", assistant: true)]])
        let agent = SessionKey(machineID: "box", sessionID: "agent"), main = SessionKey(machineID: "box", sessionID: "main")
        #expect(model.streamsAgentReplies(agent) && !model.streamsAgentReplies(main))
        #expect(!model.streamsAgentReplies(SessionKey(machineID: "box", sessionID: "unknown")), "not listed: as before")
        model.handle(.delta(agent, text: "Line 1.\n"), group: "")
        model.handle(.delta(main, text: "The answer so far"), group: "")
        #expect(model.chat(agent).liveIsAgents && model.chat(agent).liveText == "Line 1.\n")
        #expect(!model.chat(main).liveIsAgents && model.chat(main).liveText == "The answer so far")
    }

    /// Leaving an agent's chat ends its subscription, and the `""` that ends the live text then goes to no one; the hub
    /// sends the live text again only while a message is being written. Kept, the words before a tool came back under a
    /// later message of the turn when the chat was opened again (only the latest message and the one before are matched).
    @Test func leavingTheChatForgetsItsLiveText() async throws {
        let model = AppModel()
        model.applySessions([MachineRef(group: "", id: "box"): [try session("agent", assistant: false)]])
        let key = SessionKey(machineID: "box", sessionID: "agent"), owner = UUID()
        func backlog(_ items: [Item]) {
            model.handle(.items(key, items, backlog: true, hasMore: false, span: ItemSpan(history: "h", from: nil, to: items.last?.seq ?? 0),
                                moreAfter: false), group: "")
        }
        func settled() async {
            for _ in 0..<400 where model.chat(key).isRebuilding { try? await Task.sleep(for: .milliseconds(5)) }
        }
        model.subscribe(key, by: owner)
        let prompt = item(1, .text, role: "user", text: "Fix the build.")
        backlog([prompt])
        await settled()
        model.handle(.delta(key, text: "Let me check the config."), group: "")
        await settled()
        #expect(model.chat(key).liveText == "Let me check the config.")
        model.unsubscribe(key, by: owner)
        #expect(model.chat(key).liveText.isEmpty, "gone with the subscription")
        model.handle(.delta(key, text: "Let me check the config.\nThen the imports."), group: "") // crossed the unsubscribe
        // The turn went on meanwhile; opening the chat again brings it in the subscription's backlog.
        model.subscribe(key, by: owner)
        backlog([prompt, item(2, .text, text: "Let me check the config."), item(3, .toolUse, tool: "t1"),
                 item(4, .toolResult, role: "user", text: "ok", tool: "t1"), item(5, .text, text: "Now let me fix the import."),
                 item(6, .toolUse, tool: "t2"), item(7, .toolResult, role: "user", text: "ok", tool: "t2"),
                 item(8, .text, text: "Done: the build passes.")])
        try? await Task.sleep(for: .milliseconds(300)) // past the pacer's timer
        await settled()
        #expect(model.chat(key).liveText.isEmpty, "no old words under the newest message")
        // A message still being written: the hub sends it again after the backlog, and it draws.
        model.handle(.delta(key, text: "One more check.\n"), group: "")
        try? await Task.sleep(for: .milliseconds(300))
        await settled()
        #expect(model.chat(key).liveText == "One more check.\n")
    }
}
