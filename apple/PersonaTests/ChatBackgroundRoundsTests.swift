import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Background rounds in the main conversation (docs/apple/ios.md "Background rounds"): rounds another conversation, a
/// follow-up or a scheduled task started fold into one line, keep the supervision rule for what stays shown, never show
/// a scheduled prompt, and group Needs You by supervising conversation. Off, everything is as before.
@MainActor struct ChatBackgroundRoundsTests {
    private func item(_ seq: Int, role: String = "assistant", kind: ItemKind = .text, text: String = "Synthetic answer") -> Item {
        Item(id: "synthetic-\(seq)#0", seq: seq, role: role, kind: kind, text: text)
    }

    /// A supervising conversation's message, marked with its source.
    private func relayed(_ seq: Int = 1) -> Item {
        var value = item(seq, role: "user", text: "Synthetic supervisor message")
        value.source = .object(["kind": .string("conversation"), "conversation_id": .string("leader-a"), "title": .string("Release review")])
        return value
    }

    /// A scheduled run (`daily`, so `patrol`) or a follow-up (`once`), marked by the hub.
    private func scheduled(_ seq: Int = 1, state: String) -> Item {
        var value = item(seq, role: "user", text: "Synthetic scheduled prompt for the model")
        value.injected = Injected(kind: "followup", state: state, title: "Morning brief", line: "Synthetic one-line notice",
                                  detail: "Synthetic prompt the user wrote")
        return value
    }

    private func build(_ items: [Item], background: Bool = true, open: Set<String> = [], active: Bool = false) -> [ChatLine] {
        let blocks = ChatStore.rebuild(items, settled: nil).blocks
        let live = active ? blocks.last.flatMap { if case .turn(let t) = $0 { t.id } else { nil } } : nil
        return ChatLines(openWork: [], foldSupervision: true, foldBackground: background, openSupervision: open, active: active)
            .build(blocks, live: live)
    }

    private func folded(_ lines: [ChatLine]) -> (line: ChatLine, source: MessageSource?)? {
        for line in lines {
            if case .supervision(_, _, _, let source) = line.content { return (line, source) }
        }
        return nil
    }

    private func askUser(_ seq: Int) -> [Item] {
        var call = item(seq, kind: .toolUse)
        call.toolName = "mcp__persona__ask_user"
        call.toolUseID = "synthetic-ask"
        var result = item(seq + 1, role: "user", kind: .toolResult, text: "Synthetic choice")
        result.toolUseID = call.toolUseID
        return [call, result]
    }

    // MARK: Folding

    @Test func supervisorRoundFoldsAndOpensToItsMessage() throws {
        let items = [relayed(), item(2)]
        let lines = build(items)
        #expect(lines.count == 1)
        let fold = try #require(folded(lines))
        #expect(fold.source?.kind == "conversation")
        #expect(fold.source?.mention == "@Release review")
        let opened = build(items, open: [fold.line.id])
        // The message shows as its source block with its text, not as raw supervision input.
        #expect(!opened.contains { if case .supervisionInput = $0.content { true } else { false } })
        let input = try #require(opened.first { if case .row(.user(let i)) = $0.content { i.id == items[0].id } else { false } })
        #expect(!input.hidesPrompt)
        #expect(opened.contains { if case .row(.assistant(let i)) = $0.content { i.id == items[1].id } else { false } })
    }

    @Test(arguments: ["daily", "once"])
    func scheduledAndFollowUpRoundsFoldAndHideTheirPrompt(state: String) throws {
        let items = [scheduled(state: state), item(2)]
        #expect(MessageSource.of(items[0])?.kind == (state == "daily" ? "patrol" : "followup"))
        let lines = build(items)
        let fold = try #require(folded(lines))
        #expect(lines.count == 1)
        let opened = build(items, open: [fold.line.id])
        #expect(!opened.contains { if case .supervisionInput = $0.content { true } else { false } })
        let input = try #require(opened.first { if case .row(.user) = $0.content { true } else { false } })
        #expect(input.hidesPrompt)
        // A visible round (it told the user something) hides the prompt too.
        var notify = item(3, kind: .toolUse)
        notify.toolName = "mcp__persona__notify_user"
        notify.toolUseID = "synthetic-notify"
        var sent = item(4, role: "user", kind: .toolResult, text: "sent")
        sent.toolUseID = notify.toolUseID
        let shown = build([scheduled(state: state), notify, sent, item(5)])
        #expect(folded(shown) == nil)
        #expect(shown.first { if case .row(.user) = $0.content { true } else { false } }?.hidesPrompt == true)
    }

    @Test func runningRoundSaysSoAndHidesItsStream() throws {
        let lines = build([relayed(), item(2)], active: true)
        let fold = try #require(folded(lines))
        guard case .supervision(_, _, let live, _) = fold.line.content else { return }
        #expect(live)
        #expect(ChatLines.hidesLiveSupervision(lines, open: []))
    }

    @Test func roundThatAskedTheUserStaysShown() {
        let lines = build([relayed()] + askUser(2) + [item(4)])
        #expect(folded(lines) == nil)
        #expect(lines.contains { if case .row(.user) = $0.content { true } else { false } })
        // Only a successful call counts: a failed one leaves the round folded.
        var failed = askUser(2)
        failed[1].isError = true
        #expect(folded(build([relayed()] + failed + [item(4)])) != nil)
    }

    /// Persona continuing the user's own work keeps the answer shown: a usage-limit continuation, a wake-up, an
    /// untitled follow-up.
    @Test(arguments: ["limit_continue", "wake", "untitled"])
    func continuationOfTheUsersWorkStaysShown(state: String) {
        var question = item(1, role: "user", text: "Synthetic question")
        question.clientMsgID = "synthetic-client"
        var continued = item(3, role: "user", text: "Synthetic continuation")
        if state == "untitled" {
            continued.source = .object(["kind": .string("followup")])
        } else {
            continued.injected = Injected(kind: "followup", state: state, title: state == "wake" ? "Synthetic build" : nil,
                                          line: "Synthetic one-line notice")
        }
        #expect(folded(build([question, item(2), continued, item(4, text: "Synthetic final answer")])) == nil)
    }

    @Test func queuedUserMessageNeverJoinsTheRound() throws {
        var next = item(3, role: "user", text: "A genuine queued message")
        next.queued = true
        next.clientMsgID = "synthetic-sent-by-app"
        let items = [relayed(), item(2), next, item(4)]
        // The store keeps the queued message in the same turn; the lines cut it out of the round.
        #expect(ChatStore.rebuild(items, settled: nil).blocks.count == 1)
        let lines = build(items)
        #expect(lines.count == 3)
        let fold = try #require(folded(lines))
        #expect(fold.line.id == lines[0].id)
        #expect(lines[1].holds(next.id))
        #expect(lines[2].holds("synthetic-4#0"))
    }

    @Test func recapsInsideStayInside() throws {
        let lines = build([scheduled(state: "daily"), item(2), item(3, kind: .recap), item(4)])
        #expect(lines.count == 1)
        guard case .supervision(_, let trace, _, _) = try #require(folded(lines)).line.content else { return }
        #expect(trace.contains { if case .row(.recap) = $0.content { true } else { false } })
        #expect(trace.last?.holds("synthetic-4#0") == true)
    }

    // MARK: The switch and other conversations

    @Test func switchOffLooksExactlyAsBefore() {
        for items in [[relayed(), item(2)], [scheduled(state: "daily"), item(2)], [scheduled(state: "once"), item(2)]] {
            let off = build(items, background: false)
            let before = ChatLines(openWork: []).build(ChatStore.rebuild(items, settled: nil).blocks, live: nil)
            #expect(folded(off) == nil)
            #expect(off.map(\.id) == before.map(\.id))
            let hides = off.contains(where: \.hidesPrompt)
            #expect(!hides)
        }
    }

    @Test func onlyTheMainAndLeaderAssistantConversationsFold() {
        #expect(ChatLines.foldsBackground(assistant: true, conversation: "main", main: "main", on: true))
        #expect(!ChatLines.foldsBackground(assistant: true, conversation: "side", main: "main", on: true))
        #expect(!ChatLines.foldsBackground(assistant: true, conversation: "main", main: "main", on: false))
        #expect(!ChatLines.foldsBackground(assistant: false, conversation: "main", main: "main", on: true))
        #expect(!ChatLines.foldsBackground(assistant: true, conversation: "", main: nil, on: true))
        // A leader conversation folds exactly like the main one; another side conversation never does (#6595).
        #expect(ChatLines.foldsBackground(assistant: true, conversation: "lead-1", main: "main", on: true, leaders: ["lead-1", "lead-2"]))
        #expect(ChatLines.foldsBackground(assistant: true, conversation: "lead-2", main: "main", on: true, leaders: ["lead-1", "lead-2"]))
        #expect(!ChatLines.foldsBackground(assistant: true, conversation: "side", main: "main", on: true, leaders: ["lead-1"]))
        #expect(!ChatLines.foldsBackground(assistant: true, conversation: "lead-1", main: "main", on: false, leaders: ["lead-1"]))
        // A missing main still lets a leader fold, and an empty conversation id never does.
        #expect(ChatLines.foldsBackground(assistant: true, conversation: "lead-1", main: nil, on: true, leaders: ["lead-1"]))
        #expect(!ChatLines.foldsBackground(assistant: true, conversation: "", main: nil, on: true, leaders: [""]))
    }

    @Test func aLeaderConversationFoldsTheSameRoundsTheMainChatDoes() throws {
        // The gate on for a leader id drives the same builder option; a relayed round and a patrol round fold while the
        // owner's own exchange stays a normal line.
        var owner = item(5, role: "user", text: "A real request"); owner.clientMsgID = "owner-1"
        let items = [relayed(1), item(2), scheduled(3, state: "daily"), item(4), owner, item(6)]
        let lines = build(items, background: true)
        let folds = lines.compactMap { line -> MessageSource? in if case .supervision(_, _, _, let source) = line.content { return source } else { return nil } }
        #expect(folds.map(\.kind) == ["conversation", "patrol"])
        // the owner's exchange stays outside the folded rounds
        #expect(lines.contains { line in if case .row = line.content { true } else { false } })
        // The same transcript with folding off keeps every exchange open.
        let open = build(items, background: false)
        #expect(open.allSatisfy { line in if case .supervision = line.content { false } else { true } })
    }

    @Test func supervisionRoundsAreUnchanged() throws {
        var input = item(1, role: "user", text: "Synthetic supervision input")
        input.injected = Injected(kind: "system", state: "supervise", line: "Synthetic supervision", level: "triage")
        let lines = build([input, item(2)])
        let fold = try #require(folded(lines))
        #expect(fold.source == nil)
        #expect(build([input, item(2)], open: [fold.line.id]).contains { if case .supervisionInput = $0.content { true } else { false } })
    }

    // MARK: Needs You

    private func task(_ id: String) -> ActionTrayContent.Row { .entry(.task(id: id, status: .needsYou, agents: 1)) }

    @Test func needsYouGroupsBySupervisingConversation() {
        let rows: [ActionTrayContent.Row] = [.ask(.init(id: "own", title: "Synthetic ask")), task("t1"), task("t2"), .entry(.plan(id: "p1")),
                                             task("t3"), task("t4"), task("t5")]
        let origins = ["t1": "main", "t2": "leader-a", "t3": "leader-b", "t4": "leader-a"]
        let groups = ActionTrayContent.grouped(rows, conversation: "main", origin: { origins[$0] },
                                               title: { $0 == "leader-a" ? "Release review" : nil })
        #expect(groups.map(\.id) == ["", "leader-a", "leader-b"])
        #expect(groups.map(\.title) == [nil, "Release review", String(localized: "Another conversation")])
        #expect(groups[0].rows.map(\.id) == ["ask:own", "task:t1", "plan:p1", "task:t5"])
        #expect(groups[1].rows.map(\.id) == ["task:t2", "task:t4"])
        #expect(groups.flatMap(\.rows).count == rows.count)
    }

    @Test func needsYouStaysOneListWithOneSupervisorOrTheSwitchOff() {
        let rows = [task("t1"), task("t2")]
        // One other supervisor only, or only the conversation's own: one group, no headers.
        #expect(ActionTrayContent.grouped(rows, conversation: "main", origin: { _ in "leader-a" }, title: { _ in "A" }).isEmpty)
        #expect(ActionTrayContent.grouped(rows, conversation: "main", origin: { _ in "main" }, title: { _ in "A" }).isEmpty)
        var content = ActionTrayContent()
        content.needsYou = rows
        let origins = ["t1": "main", "t2": "leader-a"]
        content.groupNeedsYou(false, conversation: "main", origin: { origins[$0] }, title: { _ in nil })
        #expect(content.needsYouGroups.isEmpty)
        content.groupNeedsYou(true, conversation: "main", origin: { origins[$0] }, title: { _ in nil })
        #expect(content.needsYouGroups.count == 2)
        #expect(content.count(.needsYou) == 2)
    }

    // MARK: The setting

    @Test func settingDecodesOnWhenMissing() throws {
        func decode(_ json: String) throws -> APIClient.AssistantState {
            try JSON.decoder.decode(APIClient.AssistantState.self, from: Data(json.utf8))
        }
        let old = try decode(#"{"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[]}"#)
        #expect(old.mainBackgroundFolded == nil)
        #expect(old.backgroundFolded)
        let off = try decode(#"{"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[],"main_background_folded":false}"#)
        #expect(off.mainBackgroundFolded == false && !off.backgroundFolded)
        let body = try JSONSerialization.jsonObject(with: JSONEncoder().encode(APIClient.AssistantConfigBody(mainBackgroundFolded: false))) as? [String: Any]
        #expect(body?["main_background_folded"] as? Bool == false)
        let other = try JSONSerialization.jsonObject(with: JSONEncoder().encode(APIClient.AssistantConfigBody(timezone: "UTC"))) as? [String: Any]
        #expect(other?["main_background_folded"] == nil)
    }
}
