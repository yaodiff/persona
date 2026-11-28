import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Model change dividers (#5055, the web's `model-change.test.ts`): the transcript's model on the latest assistant
/// item decides an exchange's reply, and only two different known models in the loaded, visible exchanges draw a line.
@MainActor
struct ModelChangeTests {
    private static let opus = "claude-opus-5-5"
    private static let sonnet = "claude-sonnet-4-5-20250929"
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private func item(_ seq: Int, role: String = "assistant", kind: ItemKind = .text, text: String? = nil,
                      model: String? = nil, part: Int = 0) -> Item {
        var value = Item(id: "\(seq)#\(part)", seq: seq, ts: nil, role: role, kind: kind, text: text,
                         toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        value.model = model
        return value
    }

    private func user(_ seq: Int, _ text: String = "next") -> Item {
        item(seq, role: "user", text: text)
    }

    private func answer(_ seq: Int, _ model: String?, _ text: String = "answer", part: Int = 0) -> Item {
        item(seq, text: text, model: model, part: part)
    }

    private func call(_ seq: Int, _ model: String?, part: Int = 0) -> Item {
        var value = item(seq, kind: .toolUse, model: model, part: part)
        value.toolUseID = "call-\(seq)-\(part)"
        value.toolName = "Read"
        value.toolInput = .object(["file_path": .string("/home/user/project/example.txt")])
        return value
    }

    private func result(_ seq: Int, of call: Item) -> Item {
        var value = item(seq, role: "user", kind: .toolResult, text: "ok")
        value.toolUseID = call.toolUseID
        return value
    }

    private func marked(_ items: [Item], folded: Bool = false, openFolded: Bool = false,
                        places: [ChildSessions.Place] = [], sessions: [SessionKey: Session] = [:]) -> [ChatLine] {
        let blocks = ChatStore.rebuild(items, settled: nil).blocks
        var builder = ChatLines(openWork: [], foldSupervision: folded, foldBackground: folded, main: folded,
                                openAllSupervision: openFolded)
        builder.childPlaces = places
        builder.childSessions = sessions
        builder.historyComplete = true
        let lines = builder.build(blocks, live: nil)
        return ModelChanges.mark(lines)
    }

    /// Include a mistakenly inserted line inside a folded round too: folded work is not a visible reply either.
    private func modelLines(_ lines: [ChatLine]) -> [ChatLine] {
        lines.flatMap { line -> [ChatLine] in
            switch line.content {
            case .modelChange: [line]
            case .supervision(_, let children, _, _): modelLines(children)
            default: []
            }
        }
    }

    private func changes(_ lines: [ChatLine]) -> [String] {
        modelLines(lines).map { line in
            guard case .modelChange(_, let from, let to) = line.content else { return "" }
            return "\(from) → \(to)"
        }
    }

    private func changeIDs(_ lines: [ChatLine]) -> [String] { modelLines(lines).map(\.id) }

    private func outline(_ lines: [ChatLine]) -> [String] {
        lines.map { line in
            switch line.content {
            case .row(.user(let item)): "user \(item.id)"
            case .row(.assistant(let item)): "answer \(item.id)"
            case .row(.notice(let item)): "notice \(item.id)"
            case .userImages: "photos"
            case .work: "work"
            case .supervision: "folded"
            case .apiError: "error"
            case .modelChange(_, let from, let to): "model \(from) → \(to)"
            default: "other"
            }
        }
    }

    /// The shared wire case drives the app's actual response decoder, not a test-side item shape or a copied fixture.
    @Test func realItemDecoderReadsReplyModelsFromSharedWireFixture() throws {
        struct Sample: Decodable { var name: String; var input: JSONValue }
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/wire/items.json"))
        let samples = try JSONDecoder().decode([Sample].self, from: data)
        let sample = try #require(samples.first { $0.name == "items_reply_models" })
        let page = try JSON.decoder.decode(APIClient.ItemsResponse.self, from: JSON.encoder.encode(sample.input))
        #expect(page.items.map(\.id) == ["entry_reply_a#0", "entry_ask#0", "entry_reply_b#0"])
        #expect(page.items.map(\.model) == ["example-model", nil, "work-model"])
        #expect(page.items[1].model == nil, "a user's item has no replying model")
    }

    @Test func dividerFollowsInitiatingUserAndPrecedesReplyAndWork() throws {
        let lines = marked([
            user(1, "first"), answer(2, Self.opus),
            user(3, "second"), call(4, Self.sonnet), result(5, of: call(4, Self.sonnet)), answer(6, Self.sonnet),
        ])
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-3#0"])
        #expect(outline(lines) == [
            "user 1#0", "answer 2#0", "user 3#0", "model \(Self.opus) → \(Self.sonnet)", "work", "answer 6#0",
        ])
        let change = try #require(modelLines(lines).first)
        #expect(change.id == "mc-3#0", "the initiating message gives the divider a stable id")
    }

    /// Work after the last words counts: a fallback's later tool call, not the earlier answer, names the exchange.
    @Test func latestAssistantItemBySeqDecidesTheReplyModel() {
        let secondCall = call(5, Self.sonnet)
        let lines = marked([
            user(1, "first"), answer(2, Self.opus),
            user(3, "second"), answer(4, Self.opus, "Looking"), secondCall, result(6, of: secondCall),
            user(7, "third"), answer(8, Self.sonnet),
        ])
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-3#0"], "the next Sonnet exchange continues the run instead of marking again")
    }

    /// Two blocks of one transcript line are ordered by their numeric part, just as the store orders them.
    @Test func latestAssistantItemBySeqAndPartDecidesTheReplyModel() {
        let lines = marked([
            user(1, "first"), answer(2, Self.opus),
            user(3, "second"), call(4, Self.sonnet, part: 0), call(4, Self.opus, part: 1),
            user(5, "third"), answer(6, Self.sonnet),
        ])
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-5#0"], "part 1's Opus is newer than part 0's Sonnet")
    }

    /// An older node's model-less reply and Claude Code's synthetic API error neither draw nor end the known-model run.
    @Test func unknownAndSyntheticRepliesDoNotEndTheKnownModelRun() {
        let lines = marked([
            user(1, "first"), answer(2, Self.opus),
            user(3, "older node"), answer(4, nil),
            user(5, "synthetic"), answer(6, "<synthetic>", "API Error: rate limited"),
            user(7, "third"), answer(8, Self.sonnet),
        ])
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-7#0"])
    }

    /// Quiet supervision and background rounds hold models of their own, but their folded replies are not visible.
    @Test func foldedSupervisionAndBackgroundRepliesAreSkipped() {
        var supervise = user(3, "quiet supervision")
        supervise.injected = Injected(kind: "system", state: "supervise", line: "Synthetic supervision", level: "triage")
        var hiddenSupervision = answer(4, "claude-haiku-4-5", "hidden supervision reply")
        hiddenSupervision.narration = true
        var scheduled = user(5, "quiet scheduled round")
        scheduled.injected = Injected(kind: "followup", state: "daily", title: "Morning brief", line: "Synthetic one-line notice")
        var hiddenBackground = answer(6, "glm-hidden", "hidden background reply")
        hiddenBackground.narration = true
        let lines = marked([
            user(1, "first"), answer(2, Self.opus), supervise, hiddenSupervision, scheduled, hiddenBackground,
            user(7, "second"), answer(8, Self.sonnet),
        ], folded: true)
        #expect(lines.filter { if case .supervision = $0.content { true } else { false } }.count == 2,
                "the fixture really folded both hidden rounds")
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-7#0"], "neither folded model replaces Opus as the previous visible reply")
    }

    @Test func photoOnlyReplyStartsANewExchangeAfterItsPhotos() {
        var photo = item(3, role: "user", kind: .image)
        photo.image = ImagePayload(mime: "image/png", data: "")
        let lines = marked([
            user(1, "first"), answer(2, Self.opus), photo,
            call(4, Self.sonnet), result(5, of: call(4, Self.sonnet)), answer(6, Self.sonnet),
        ])
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-img-3#0"])
        #expect(outline(lines) == [
            "user 1#0", "answer 2#0", "photos", "model \(Self.opus) → \(Self.sonnet)", "work", "answer 6#0",
        ])
    }

    @Test func visibleWakeStartsANewExchangeAfterItsNotice() {
        var wake = item(3, role: "system", kind: .notice, text: "Background work")
        wake.wake = true
        let lines = marked([
            user(1, "first"), answer(2, Self.opus), wake,
            call(4, Self.sonnet), result(5, of: call(4, Self.sonnet)), answer(6, Self.sonnet),
        ])
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-w-3#0"])
        #expect(outline(lines) == [
            "user 1#0", "answer 2#0", "notice 3#0", "model \(Self.opus) → \(Self.sonnet)", "work", "answer 6#0",
        ])
    }

    @Test func subagentWakeWithoutANoticeRowStartsBeforeItsWork() {
        var wake = item(3, kind: .toolResult, text: "Explore complete")
        wake.wake = true
        wake.task = TaskNote(kind: "agent", status: "completed", title: "Explore")
        let lines = marked([
            user(1, "first"), answer(2, Self.opus), wake,
            call(4, Self.sonnet), result(5, of: call(4, Self.sonnet)), answer(6, Self.sonnet),
        ])
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-w-3#0"])
        #expect(outline(lines) == [
            "user 1#0", "answer 2#0", "model \(Self.opus) → \(Self.sonnet)", "work", "answer 6#0",
        ])
    }

    @Test func wakeWithAChildCardKeepsItsModelChangeBoundary() throws {
        var wake = item(3, kind: .toolResult, text: "Explore complete")
        wake.wake = true
        wake.task = TaskNote(kind: "agent", status: "completed", title: "Explore")
        let child = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"child","title":"Child","cwd":"/code/app","status":"idle","state":"idle","folded_under":"parent"}"#.utf8))
        let lines = marked([
            user(1, "first"), answer(2, Self.opus), wake,
            call(4, Self.sonnet), result(5, of: call(4, Self.sonnet)), answer(6, Self.sonnet),
        ], places: [ChildSessions.Place(key: child.key, time: nil)], sessions: [child.key: child])
        let hasChildCard = lines.contains { line in
            guard case .work(_, let items, _, _, _) = line.content else { return false }
            return items.contains { item in
                if case .step(.call(let call)) = item { return call.use.toolName == "Child" }
                return false
            }
        }
        #expect(hasChildCard, "the wake's work line really receives a child-session card")
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-w-3#0"])
        #expect(outline(lines) == [
            "user 1#0", "answer 2#0", "model \(Self.opus) → \(Self.sonnet)", "work", "answer 6#0",
        ])
    }

    @Test func openingFoldedRoundsDoesNotChangeVisibleReplyModels() {
        var supervise = user(3, "quiet supervision")
        supervise.injected = Injected(kind: "system", state: "supervise", line: "Synthetic supervision", level: "triage")
        var hiddenSupervision = answer(4, "claude-haiku-4-5", "hidden supervision reply")
        hiddenSupervision.narration = true
        var scheduled = user(5, "quiet scheduled round")
        scheduled.injected = Injected(kind: "followup", state: "daily", title: "Morning brief", line: "Synthetic one-line notice")
        var hiddenBackground = answer(6, "glm-hidden", "hidden background reply")
        hiddenBackground.narration = true
        let lines = marked([
            user(1, "first"), answer(2, Self.opus), supervise, hiddenSupervision, scheduled, hiddenBackground,
            user(7, "second"), answer(8, Self.sonnet),
        ], folded: true, openFolded: true)
        #expect(lines.contains { if case .supervisionInput = $0.content { true } else { false } },
                "the fixture really expands the folded rounds")
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-7#0"], "opened hidden models neither replace Opus nor draw inside the round")
    }

    /// Aliases, snapshots, provider wrappers, case and the chat's identical friendly names are all one model.
    @Test func canonicalAliasesSnapshotsAndFriendlyNamesDoNotDraw() {
        let spellings = [
            "claude-sonnet-4-5",
            Self.sonnet,
            "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
            "global.anthropic.claude-sonnet-4-5-20250929-v2:0",
            "claude-sonnet-4-5@20250929",
            "anthropic/claude-sonnet-4-5",
            "publishers/anthropic/models/claude-sonnet-4-5@20250929",
            "CLAUDE-SONNET-4-5",
        ]
        #expect(Set(spellings.map(replyModelName)) == Set(["Sonnet 4.5"]))
        var items: [Item] = []
        for (index, model) in spellings.enumerated() {
            let seq = index * 2 + 1
            items += [user(seq, "q\(index)"), answer(seq + 1, model)]
        }
        #expect(changes(marked(items)).isEmpty)
        items += [user(20, "changed"), answer(21, Self.opus)]
        let changed = marked(items)
        #expect(changes(changed) == ["\(spellings.last!) → \(Self.opus)"])
        #expect(changeIDs(changed) == ["mc-20#0"])
    }

    @Test func contextWindowSuffixRemainsADifferentModel() {
        let larger = "claude-opus-5-5-20260101[1m]"
        let lines = marked([user(1), answer(2, Self.opus), user(3), answer(4, larger)])
        #expect(changes(lines) == ["\(Self.opus) → \(larger)"])
        #expect(replyModelName(larger) == "Opus 5.5 1M")
        #expect(bareModel("deepseek-v3") == "deepseek-v3", "a version in the name is not Bedrock's suffix")
    }

    @Test func friendlyNamesMatchTheWeb() {
        #expect(replyModelName(Self.opus) == "Opus 5.5")
        #expect(replyModelName(Self.sonnet) == "Sonnet 4.5")
        #expect(replyModelName("us.anthropic.claude-sonnet-4-5-20250929-v1:0") == "Sonnet 4.5")
        #expect(replyModelName("claude-sonnet-4-5@20250929") == "Sonnet 4.5")
        #expect(replyModelName("gpt-6.1-sol") == "GPT-6.1 Sol")
        #expect(replyModelName("vendor/glm-5.1") == "glm-5.1")
    }

    /// A latest page has no invisible predecessor to compare. Once its older page arrives, the first exchange of that
    /// newer page is exactly where the change appears; later same-model exchanges stay unmarked.
    @Test func loadingAnOlderPageAddsTheDividerAtTheNewerPagesFirstExchange() async throws {
        let store = ChatStore(key: SessionKey(machineID: "test", sessionID: UUID().uuidString))
        store.merge([user(3, "newer first"), answer(4, Self.sonnet), user(5, "newer second"), answer(6, Self.sonnet)],
                    backlog: true, hasMore: true)
        try await settle(store)
        #expect(changes(ModelChanges.mark(ChatLines(openWork: []).build(store.blocks, live: nil))).isEmpty)

        store.merge([user(1, "older"), answer(2, Self.opus)], backlog: true, hasMore: false)
        try await settle(store)
        #expect(store.items.map(\.seq) == [1, 2, 3, 4, 5, 6])
        let lines = ModelChanges.mark(ChatLines(openWork: []).build(store.blocks, live: nil))
        #expect(changes(lines) == ["\(Self.opus) → \(Self.sonnet)"])
        #expect(changeIDs(lines) == ["mc-3#0"])
        #expect(outline(lines) == [
            "user 1#0", "answer 2#0", "user 3#0", "model \(Self.opus) → \(Self.sonnet)", "answer 4#0",
            "user 5#0", "answer 6#0",
        ])
    }

    private func settle(_ store: ChatStore) async throws {
        for _ in 0..<100 where store.isRebuilding || !store.backlogBuilt {
            try await Task.sleep(for: .milliseconds(20))
        }
        #expect(!store.isRebuilding && store.backlogBuilt, "the store did not publish the merged page's blocks")
    }
}
