import Foundation
import Testing
import BeautifulUI
@testable import Persona
import PersonaWire

/// A question card stays where it is, waiting, while its answer is on its way, and keeps its picks and an error with Retry
/// when the answer fails (#6279).
@MainActor
struct QuestionSendingTests {
    private let folder = FileManager.default.temporaryDirectory.appending(path: "question-sending-\(UUID().uuidString)")
    private let path = "/api/v1/interactions/ix_1/resolve"
    private let answer = Resolution(answers: ["用哪个库？": "pgx"])

    private func card(kind: InteractionKind = .question) -> Interaction {
        let questions = kind == .question ? [Question(question: "用哪个库？", header: nil, multiSelect: false, options: [QuestionOption(label: "pgx")])] : nil
        return Interaction(id: "ix_1", machineID: "m", sessionID: "", kind: kind, toolName: kind == .permission ? "Bash" : nil, questions: questions)
    }

    private func model(_ hub: FakeHub, _ ix: Interaction) -> AppModel {
        let model = AppModel(stateFolder: folder)
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        model.replaceInteractions("", [ix])
        return model
    }

    private final class Flag: @unchecked Sendable { var on = true }

    // MARK: The attempts

    @Test func aSecondAnswerWhileOneIsOnItsWayIsNotSent() {
        var a = AnswerAttempts()
        #expect(a.begin("ix", answer) != nil)
        #expect(a.progress("ix") == .sending)
        #expect(a.begin("ix", answer) == nil)
        #expect(a.begin("ix", .cancelled) == nil)
        #expect(a.begin("ix", Resolution(answers: ["用哪个库？": "sqlc"])) == nil)
    }

    @Test func aSecondSkipWhileOneIsOnItsWayIsNotSent() {
        var a = AnswerAttempts()
        #expect(a.begin("ix", .cancelled) != nil)
        #expect(a.begin("ix", .cancelled) == nil)
    }

    @Test func canCancelDecodesTrueFalseAndAbsent() throws {
        for (field, expected) in [(#""can_cancel":true"#, true), (#""can_cancel":false"#, false), ("", nil)] as [(String, Bool?)] {
            let json = #"{"id":"ix_1","machine_id":"m","session_id":"","kind":"question""# + (field.isEmpty ? "" : "," + field) + "}"
            let ix = try JSON.decoder.decode(Interaction.self, from: Data(json.utf8))
            #expect(ix.canCancel == expected)
        }
    }

    @Test func theSameAnswerAgainKeepsItsOpAndAnotherOneIsNew() {
        var a = AnswerAttempts()
        let op = a.begin("ix", answer)
        a.fail("ix", "offline")
        #expect(a.progress("ix") == .failed("offline") && a.retryable("ix") == answer)
        #expect(a.begin("ix", answer) == op && a.progress("ix") == .sending)
        a.fail("ix", "offline")
        let other = a.begin("ix", Resolution(answers: ["用哪个库？": "sqlc"]))
        #expect(other != nil && other != op)
    }

    @Test func aFinishedCardLeavesNothing() throws {
        var a = AnswerAttempts()
        let started = a.begin("ix", answer)
        let op = try #require(started)
        let finished = a.finish("ix", op: op)
        #expect(finished)
        #expect(a.progress("ix") == nil && a.retryable("ix") == nil)
    }

    @Test func anOldNilOccurrenceCompletionDoesNotFinishAReplacement() throws {
        var a = AnswerAttempts()
        let started = a.begin("ix", answer)
        let old = try #require(started)
        _ = a.begin("ix", answer, occurrence: "2026-11-21T12:00:01Z")
        let finished = a.finish("ix", op: old)
        #expect(!finished)
        #expect(a.progress("ix") == .sending)
    }

    @Test func aReplacementCompletionRetiresTheOldOperation() throws {
        var a = AnswerAttempts()
        let started = a.begin("ix", answer)
        let old = try #require(started)
        let replaced = a.begin("ix", answer, occurrence: "2026-11-21T12:00:01Z")
        let replacement = try #require(replaced)
        let finished = a.finish("ix", op: replacement)
        #expect(finished)
        let oldFinished = !a.owns("ix", op: old) && !a.finish("ix", op: old)
        #expect(oldFinished)
    }

    @Test func anOldNilOccurrenceFailureDoesNotFailAReplacement() {
        var a = AnswerAttempts()
        _ = a.begin("ix", answer)
        _ = a.begin("ix", answer, occurrence: "2026-11-21T12:00:01Z")
        a.fail("ix", "old request failed")
        #expect(a.progress("ix") == .sending)
    }

    @Test func equivalentOccurrenceTextStillIdentifiesTheSameAttempt() throws {
        var a = AnswerAttempts()
        let started = a.begin("ix", answer, occurrence: "2026-11-21T20:00:00.123456789+08:00")
        let op = try #require(started)
        a.fail("ix", "offline", occurrence: "2026-11-21T12:00:00.123456789Z")
        let repeated = a.begin("ix", answer, occurrence: "2026-11-21T12:00:00.123456789Z")
        #expect(repeated == op)
    }

    // MARK: The model

    @Test func aSkipSendsOnlyCancelAndShowsTheClosedReceipt() async throws {
        let hub = FakeHub()
        hub.on("POST", path, json: #"{"ok":true}"#)
        let ix = card()
        let model = model(hub, ix)
        await model.resolve(ix, .cancelled)
        let requests = hub.sent("POST", path)
        #expect(requests.count == 1)
        let request = try #require(requests.first)
        let body = try #require(request.body)
        #expect(String(data: body, encoding: .utf8) == #"{"cancel":true}"#)
        #expect(model.pendingInteractions.isEmpty)
        #expect(model.toast?.message == "已关闭提问，未回复")
    }

    @Test func aSkipThatFindsAnotherAnswerKeepsItsAlreadyReceipt() async {
        let hub = FakeHub()
        hub.on("POST", path, json: #"{"ok":true,"already":true,"status":"resolved","resolution":{"answers":{"用哪个库？":"pgx"}}}"#)
        let ix = card()
        let model = model(hub, ix)
        await model.resolve(ix, .cancelled)
        #expect(model.toast?.message.contains("pgx") == true)
        #expect(model.toast?.message != "已关闭提问，未回复")
    }

    @Test func aFailedSkipKeepsTheCardWithItsPicks() async {
        let hub = FakeHub()
        let ix = card()
        let model = model(hub, ix)
        // BeautifulUI holds the current answers here before cancelling; a failure rebuilds its card from this value.
        let kept = ApprovalAnswers(picks: [0: [0]], custom: [:])
        let shown = BUIQuestionForm.shown(kept, ix.questions ?? [])
        hub.on("POST", path) { _ in throw APIError(status: 400, message: "bad request") }
        await model.resolve(ix, .cancelled)
        #expect(model.pendingInteractions.count == 1)
        #expect(model.answerFailure(ix.uid) != nil)
        #expect(BUIQuestionForm.shown(kept, ix.questions ?? []) == shown)
        #expect(BUIQuestionForm.answers(kept, ix.questions ?? []) == ["用哪个库？": "pgx"])
    }

    @Test func theCardStaysSendingUntilTheHubHasTheAnswer() async {
        let hub = FakeHub()
        let ix = card()
        let model = model(hub, ix)
        var during: (card: Bool, progress: AnswerAttempts.Progress?)?
        hub.on("POST", path) { _ in
            during = (model.pendingInteractions.count == 1, model.answerProgress(ix.uid))
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.resolve(ix, answer)
        #expect(during?.card == true && during?.progress == .sending)
        #expect(model.pendingInteractions.isEmpty && model.answerProgress(ix.uid) == nil)
    }

    @Test func aFailedAnswerKeepsTheCardWithItsErrorAndRetrySendsTheSameOp() async {
        let hub = FakeHub()
        let ix = card()
        let model = model(hub, ix)
        let failing = Flag()
        hub.on("POST", path) { _ in
            if failing.on { throw APIError(status: 400, message: "bad request") }
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.resolve(ix, answer)
        #expect(model.pendingInteractions.count == 1)
        #expect(model.answerFailure(ix.uid) != nil && model.answerProgress(ix.uid) != .sending)
        #expect(model.toast == nil, "the card says it, not a toast")
        failing.on = false
        await model.retryAnswer(ix)
        #expect(model.pendingInteractions.isEmpty && model.answerProgress(ix.uid) == nil)
        let ops = hub.sent("POST", path).map { $0.headers[OpRetry.header] }
        #expect(ops.count == 2 && ops[0] != nil && ops[0] == ops[1])
    }

    @Test func aSecondTapWhileSendingSendsNothing() async {
        let hub = FakeHub()
        hub.on("POST", path, json: #"{"ok":true}"#)
        let ix = card()
        let model = model(hub, ix)
        // A hub that takes its time, so that the second tap comes while the first answer is on its way.
        var slow = hub.client
        let inner = slow.transport!
        slow.transport = { req in
            try await Task.sleep(for: .milliseconds(200))
            return try await inner(req)
        }
        model.apiForTests = slow
        async let first: Void = model.resolve(ix, answer)
        try? await Task.sleep(for: .milliseconds(50))
        #expect(model.answerProgress(ix.uid) == .sending && model.pendingInteractions.count == 1)
        await model.resolve(ix, answer)
        await first
        #expect(hub.sent("POST", path).count == 1)
        #expect(model.pendingInteractions.isEmpty)
    }

    @Test func theHubEndingTheCardWhileSendingTakesItAway() async {
        let hub = FakeHub()
        let ix = card()
        let model = model(hub, ix)
        hub.on("POST", path) { _ in
            model.handle(.interactionResolved("ix_1", assistant: nil), group: "")
            throw APIError(status: 409, message: "prompt_changed")
        }
        await model.resolve(ix, answer)
        #expect(model.pendingInteractions.isEmpty && model.answerProgress(ix.uid) == nil)
    }

    @Test func aLateQuestionAnswerResponseKeepsASameIDReplacement() async {
        let hub = FakeHub()
        var first = card()
        first.createdAt = Date(timeIntervalSince1970: 1_000)
        var replacement = first
        replacement.createdAt = first.createdAt!.addingTimeInterval(1)
        replacement.summary = "replacement"
        let model = model(hub, first)
        hub.on("POST", path) { _ in
            model.handle(.interaction(replacement), group: "")
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.resolve(first, answer)
        #expect(model.interactions[first.uid]?.createdAt == replacement.createdAt)
        model.replaceInteractions("", [replacement])
        #expect(model.interactions[first.uid]?.createdAt == replacement.createdAt)
    }

    @Test func aLateAlreadyResponseDoesNotPublishForAReplacement() async {
        let hub = FakeHub()
        var first = card()
        first.createdAt = Date(timeIntervalSince1970: 1_000)
        var replacement = first
        replacement.createdAt = first.createdAt!.addingTimeInterval(1)
        replacement.summary = "replacement"
        let model = model(hub, first)
        hub.on("POST", path) { _ in
            model.handle(.interaction(replacement), group: "")
            return Data(#"{"ok":true,"already":true,"status":"resolved","resolution":{"answered_by":"assistant","answers":{"用哪个库？":"pgx"}}}"#.utf8)
        }
        await model.resolve(first, answer)
        #expect(model.interactions[first.uid]?.summary == "replacement")
        #expect(model.delegatedAnswers.isEmpty && model.toast == nil)
    }

    @Test func aReplacementArrivingDuringAPermissionAnswerStaysPending() async {
        let hub = FakeHub()
        var first = card(kind: .permission)
        first.createdAt = Date(timeIntervalSince1970: 1_000)
        var replacement = first
        replacement.createdAt = first.createdAt!.addingTimeInterval(1)
        replacement.summary = "replacement"
        let model = model(hub, first)
        hub.on("POST", path) { _ in
            model.handle(.interaction(replacement), group: "")
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.resolve(first, Resolution(allow: true))
        #expect(model.interactions[first.uid]?.createdAt == replacement.createdAt)
        model.replaceInteractions("", [replacement])
        #expect(model.interactions[first.uid]?.createdAt == replacement.createdAt)
    }

    @Test func catchUpInstallsANanosecondReplacementWithoutRevertingToAnOlderSnapshot() throws {
        let first = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8))
        let replacement = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000002Z","summary":"replacement"}"#.utf8))
        let model = model(FakeHub(), first)
        model.replaceInteractions("", [replacement])
        #expect(model.interactions[first.uid]?.createdAtWire == replacement.createdAtWire)
        model.replaceInteractions("", [first])
        #expect(model.interactions[first.uid]?.createdAtWire == replacement.createdAtWire)
        model.handle(.interaction(first), group: "")
        #expect(model.interactions[first.uid]?.createdAtWire == replacement.createdAtWire)
    }

    @Test func catchUpInstallsANanosecondOnlyReplacement() throws {
        let first = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8))
        let replacement = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000002Z"}"#.utf8))
        #expect(first == replacement) // Compatibility Date equality is why this exercises the store's occurrence fence.
        let model = model(FakeHub(), first)
        model.replaceInteractions("", [replacement])
        #expect(model.interactions[first.uid]?.createdAtWire == replacement.createdAtWire)
    }

    @Test func watchAnswerCompletionRemovesOnlyItsDisplayedOccurrence() throws {
        let first = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8))
        let replacement = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000002Z"}"#.utf8))
        #expect(interactionRemovalMatches(first, resolved: first))
        #expect(!interactionRemovalMatches(replacement, resolved: first))
        var legacy = first
        legacy.createdAt = nil
        #expect(interactionRemovalMatches(replacement, resolved: legacy))
    }

    @Test func aSameIDReplacementIsANewArrivalButItsReplayIsNot() throws {
        let first = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8))
        let replacement = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000002Z"}"#.utf8))
        #expect(AppModel.announcesInteraction(replacement, replacing: first))
        #expect(!AppModel.announcesInteraction(replacement, replacing: replacement))
    }

    @Test func aNanosecondReplacementSurvivesAnEarlierResolvedEvent() throws {
        let first = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8))
        let replacement = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000002Z","summary":"replacement"}"#.utf8))
        let hub = FakeHub()
        let model = model(hub, first)
        model.handle(.interaction(replacement), group: "")
        let resolved = try #require(ServerEvent.decode(Data(#"{"type":"interaction_resolved","id":"ix_1","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8)))
        model.handle(resolved, group: "")
        #expect(model.interactions[first.uid]?.createdAtWire == "2026-11-21T01:02:03.000000002Z")
        model.replaceInteractions("", [replacement])
        #expect(model.interactions[first.uid]?.createdAtWire == replacement.createdAtWire)
    }

    @Test func anEarlierResolvedEventDoesNotBlockANewerPermissionRetry() async throws {
        let first = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8))
        let replacement = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000002Z","summary":"replacement"}"#.utf8))
        let resolved = try #require(ServerEvent.decode(Data(#"{"type":"interaction_resolved","id":"ix_1","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8)))
        let hub = FakeHub()
        let model = model(hub, replacement)
        hub.on("POST", path) { _ in
            // The permission card is optimistically hidden while B is in flight.
            // A's delayed withdrawal must not poison B's put-back state.
            model.handle(resolved, group: "")
            throw APIError(status: 400, message: "try again")
        }
        await model.resolve(replacement, Resolution(allow: true))
        #expect(model.interactions[first.uid]?.createdAtWire == replacement.createdAtWire)
        model.replaceInteractions("", [replacement])
        #expect(model.interactions[first.uid]?.createdAtWire == replacement.createdAtWire)
    }

    @Test func aDatedAnswerCannotRemoveAnArrivedLegacyReplacement() async throws {
        var dated = card()
        dated.createdAt = Date(timeIntervalSince1970: 1_000)
        let first = try JSON.decoder.decode(Interaction.self, from: JSONEncoder().encode(dated))
        var replacement = first
        replacement.createdAt = nil
        replacement.summary = "legacy replacement"
        let hub = FakeHub()
        let model = model(hub, first)
        hub.on("POST", path) { _ in
            model.handle(.interaction(replacement), group: "")
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.resolve(first, answer)
        #expect(model.interactions[first.uid]?.summary == "legacy replacement")
        model.replaceInteractions("", [replacement])
        #expect(model.interactions[first.uid]?.summary == "legacy replacement")
    }

    @Test func anOrdinaryDatedAnswerCannotRestoreItsHubEndedOccurrence() async throws {
        let ix = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"","kind":"permission","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8))
        let resolved = try #require(ServerEvent.decode(Data(#"{"type":"interaction_resolved","id":"ix_1","created_at":"2026-11-21T01:02:03.000000001Z"}"#.utf8)))
        let hub = FakeHub()
        let model = model(hub, ix)
        hub.on("POST", path) { _ in
            model.handle(resolved, group: "")
            throw APIError(status: 400, message: "request failed after withdrawal")
        }
        await model.resolve(ix, Resolution(allow: true))
        #expect(model.interactions[ix.uid] == nil && model.answerProgress(ix.uid) == nil)
        model.replaceInteractions("", [])
        #expect(model.interactions[ix.uid] == nil)
    }

    @Test func otherCardsStillGoAtOnce() async {
        let hub = FakeHub()
        let ix = card(kind: .permission)
        let model = model(hub, ix)
        var during: Int?
        hub.on("POST", path) { _ in
            during = model.pendingInteractions.count
            return Data(#"{"ok":true}"#.utf8)
        }
        await model.resolve(ix, Resolution(allow: true))
        #expect(during == 0)
    }
}
