import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The voice bar's words and buttons (docs/assistant/voice.md 6.2, 1.3; 6.10「VoiceBarLine 的各种相位」): what the bound
/// conversation does, what needs the user, why an instruction did not go and what can be done, and the first line's
/// state beside the orb.
struct VoiceBarLineTests {
    /// `fields` replace the idle state when they name one (a JSON object's keys must not repeat).
    private static func session(_ fields: String = "") -> Session {
        let state = fields.contains(#""status""#) ? "" : #","status":"idle","state":"idle""#
        return try! JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"m1","session_id":"s1"\#(state)\#(fields)}"#.utf8))
    }

    private static let working = session(#","status":"working","state":"working","state_since":"2026-11-06T10:00:00Z","activity":"在跑测试""#)

    private static func card(_ kind: String, id: String = "ix1") -> Interaction {
        try! JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"\#(id)","machine_id":"m1","session_id":"s1","kind":"\#(kind)"}"#.utf8))
    }

    private static func run(_ state: VoiceRunState, id: String = "voice-1", code: String? = nil, task: String = "修登录页",
                            draft: String? = nil, clearable: String? = nil, started: Date? = nil) -> VoiceRun {
        VoiceRun(id: id, task: task, state: state, msgIDs: [id], baseSeq: 1, startedAt: started.map { .number($0.timeIntervalSince1970) },
                 code: code, message: nil, draft: draft, clearable: clearable)
    }

    private static func line(_ runs: [VoiceRun] = [], session: Session? = session(), cards: [Interaction] = [], link: VoiceLink = .live,
                             muted: Bool = false, asking: String? = nil, notice: VoiceFailure? = nil,
                             dismissed: Set<String> = [], resendable: Set<String> = []) -> VoiceBarLine {
        VoiceBarLine.make(phase: .live(.listening), link: link, runs: runs, session: session, cards: cards, muted: muted, asking: asking,
                          notice: notice, label: "Claude", dismissed: dismissed, resendable: resendable)
    }

    @Test func nothingGoingOnSaysWhereWordsGo() {
        let l = Self.line()
        #expect(l.kind == .ready && l.actions.isEmpty && l.text == "直接说，会交给 Claude")
        #expect(Self.line(muted: true).kind == .muted)
    }

    @Test func workingSaysWhatAndSinceWhenWithStop() {
        let l = Self.line([Self.run(.working)], session: Self.working)
        #expect(l.kind == .working)
        #expect(l.text == "Claude 在跑测试")
        #expect(l.since == Self.working.stateSince)
        #expect(l.actions == [.stopAgent])
    }

    @Test func aRunAtWorkWithTheConversationNotYetSoCountsFromTheRun() throws {
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let l = Self.line([Self.run(.working, started: at)])
        #expect(l.kind == .working && l.text == "Claude 在做")
        #expect(try #require(l.since).timeIntervalSince1970 == at.timeIntervalSince1970)
        #expect(l.actions.isEmpty, "no 停止 Claude while the conversation itself does not work")
    }

    @Test func stopIsOfferedOnlyWhereTheConversationTakesInterrupts() {
        let source = Self.session(#","status":"working","state":"working","source_kind":"codex_app_server","capabilities":{"send":true}"#)
        #expect(Self.line(session: source).actions.isEmpty)
    }

    @Test func handingOverAndStartingUp() {
        #expect(Self.line([Self.run(.sending)]).kind == .handing)
        #expect(Self.line(session: Self.session(#","starting":true"#)).text == "正在把 Claude 拉起来…")
    }

    @Test func pausedUpstreamWhileItWorks() {
        let l = Self.line([Self.run(.working)], session: Self.working, link: .paused)
        #expect(l.kind == .paused && l.text == "在等 Claude · 一开口就接上" && l.actions == [.stopAgent])
    }

    @Test func waitingOnSomethingOutside() {
        let blocked = Self.session(#","status":"idle","state":"idle","work":{"phase":"blocked","on":"external","line":"等部署完成"}"#)
        #expect(Self.line(session: blocked).text == "Claude 在等：等部署完成")
        #expect(Self.line([Self.run(.blocked)]).kind == .waiting)
        let user = Self.session(#","work":{"phase":"blocked","on":"user"}"#)
        #expect(Self.line(session: user).kind == .ready, "waiting on the user is a card's to say")
    }

    @Test func cardsComeFirstAndTheAskedOneSaysSo() {
        #expect(Self.line([Self.run(.working)], session: Self.working, cards: [Self.card("permission")]).kind == .needsTap)
        let asked = Self.line([Self.run(.needsYou)], cards: [Self.card("question", id: "q1")], asking: "q1")
        #expect(asked.kind == .asking && asked.actions.isEmpty)
        #expect(Self.line(cards: [Self.card("question", id: "q1")], asking: "gone").kind == .needsTap,
                "asking about a card that is no longer here: tap the one that is")
    }

    @Test func offlineComputer() {
        let offline = VoiceFailure(code: .machineOffline, message: "machine offline", fatal: false)
        #expect(Self.line(session: Self.working, notice: offline).kind == .offline)
        #expect(Self.line(notice: VoiceFailure(code: .modelUnavailable, message: "", fatal: false)).kind == .ready)
    }

    @Test func aDraftOnTheComputerOffersClearAndSendOnlyWhileTheConnectionKnowsTheRun() {
        let refused = Self.run(.refused, code: "input_draft", draft: "[⧉ Selected 3 lines from foo.go] fix it", clearable: "fix it")
        let known = Self.line([refused], resendable: ["voice-1"])
        #expect(known.kind == .notSent)
        #expect(known.text.contains("[⧉ Selected 3 lines from foo.go] fix it"), "the whole line, as it is on the computer")
        #expect(known.actions == [.clearAndSend(run: "voice-1"), .dismiss(run: "voice-1")])
        #expect(Self.line([refused]).actions == [.dismiss(run: "voice-1")], "after a reconnect it cannot be sent again")
        let unclearable = Self.run(.refused, code: "input_draft", draft: "两行\n草稿")
        #expect(Self.line([unclearable], resendable: ["voice-1"]).actions == [.dismiss(run: "voice-1")])
    }

    @Test func aPlanCardTakesTheRefusedWordsAsItsFeedback() {
        let refused = Self.run(.refused, code: "dialog_open", task: "第三步先别做")
        let l = Self.line([refused], cards: [Self.card("plan", id: "plan1")])
        #expect(l.kind == .notSent)
        #expect(l.actions == [.returnPlan(card: "plan1", feedback: "第三步先别做", run: "voice-1"), .dismiss(run: "voice-1")])
        let other = Self.line([refused], cards: [Self.card("permission")])
        #expect(other.text == "Claude 在等你先处理上面的卡片" && other.actions == [.dismiss(run: "voice-1")])
    }

    @Test func refusalsThatCannotGoAgainOnlyGoAway() {
        for code in ["on_screen", "empty", "open_elsewhere", "engine_missing", "runtime_away"] {
            let l = Self.line([Self.run(.refused, code: code)], resendable: ["voice-1"])
            #expect(l.kind == .notSent && l.actions == [.dismiss(run: "voice-1")], "\(code)")
        }
        let other = Self.line([Self.run(.refused, code: "conflict")], resendable: ["voice-1"])
        #expect(other.actions == [.resend(run: "voice-1"), .dismiss(run: "voice-1")])
    }

    /// #5904: what the user-speech gate held back says no fresh request was heard, never that someone else said it, quotes
    /// what was held back (Send Anyway confirms words the user can see), and offers Send Anyway while the connection up
    /// now knows the run and keeps it (`canResend`: `speech_gate`).
    @Test func anUnpromptedRefusalCanBeSentAnyway() {
        let refused = Self.run(.refused, code: "unprompted")
        let known = Self.line([refused], resendable: ["voice-1"])
        #expect(known.kind == .notSent)
        #expect(known.actions.map(\.name) == ["anyway", "dismiss"])
        #expect(known.actions.first?.id == "anyway-voice-1")
        #expect(known.text == "刚才没听到你要做这件事，所以没发出去：『修登录页』")
        #expect(!known.text.contains("不是你说的"), "not worded as if someone else said it")
        let unknown = Self.line([refused])
        #expect(unknown.actions.map(\.name) == ["dismiss"], "not after a reconnect, nor from an extension that does not keep it")
        #expect(unknown.text == known.text)
    }

    /// #5904 review: nothing to quote is nothing to send anyway (the extension keeps no run without a task), even
    /// when the caller says it can go.
    @Test func anUnpromptedRefusalWithoutATaskOnlyGoesAway() {
        let l = Self.line([Self.run(.refused, code: "unprompted", task: "")], resendable: ["voice-1"])
        #expect(l.kind == .notSent && l.actions == [.dismiss(run: "voice-1")])
        #expect(l.text == "刚才没听到你要做这件事，所以没发出去")
    }

    @Test func unknownAndFailedAreNeverSentAgainByThemselves() {
        #expect(Self.line([Self.run(.unknown, code: "unknown")]).text == "不确定发出去没有，看一下对话")
        #expect(Self.line([Self.run(.failed, code: "unreachable")]).actions == [.dismiss(run: "voice-1")])
    }

    @Test func aLaterInstructionOrDismissingTakesTheRefusalAway() {
        let refused = Self.run(.refused, id: "voice-1", code: "unprompted")
        #expect(Self.line([refused, Self.run(.working, id: "voice-2")], session: Self.working).kind == .working)
        #expect(Self.line([refused], dismissed: ["voice-1"]).kind == .ready)
        #expect(Self.line([refused, Self.run(.folded, id: "voice-2")]).kind == .notSent, "a folded run says nothing of its own")
    }

    @Test func announcedKinds() {
        #expect(VoiceBarLine.Kind.needsTap.announced && VoiceBarLine.Kind.paused.announced && VoiceBarLine.Kind.notSent.announced)
        #expect(!VoiceBarLine.Kind.working.announced && !VoiceBarLine.Kind.ready.announced)
    }

    // MARK: The first line

    @Test func statusBesideTheOrb() {
        func status(_ phase: VoicePhase, link: VoiceLink = .live, muted: Bool = false, working: Bool = false, tool: String? = nil) -> VoiceBarStatus {
            VoiceBarStatus.make(phase: phase, link: link, muted: muted, working: working, tool: tool)
        }
        #expect(status(.connecting) == VoiceBarStatus(text: "正在连接…", orb: .connecting))
        #expect(status(.reconnecting(attempt: 2)).orb == .connecting)
        #expect(status(.live(.listening)) == VoiceBarStatus(text: "在听", orb: .listening))
        #expect(status(.live(.listening), working: true).orb == .working)
        #expect(status(.live(.hearing)).orb == .hearing)
        #expect(status(.live(.speaking), muted: true).orb == .speaking, "muted, the voice still speaks")
        #expect(status(.live(.listening), muted: true) == VoiceBarStatus(text: "已静音", orb: .muted))
        #expect(status(.live(.listening), link: .paused).orb == .paused)
        #expect(status(.live(.speaking), link: .waking).orb == .connecting)
        #expect(status(.live(.listening), tool: "查日程").text == "正在查日程…")
        #expect(status(.ended(.lost)).orb == .ended)
    }
}

/// What the page says where it offers no call, and after one ended by itself (docs/assistant/voice.md 1.2 L, 1.3).
struct VoiceNotesTests {
    @Test func theMenuItemExplainsWhyThereIsNoCall() throws {
        #expect(VoiceUnavailableNote.make(.available(extensionID: "voice"), extensionID: "voice") == nil)
        #expect(VoiceUnavailableNote.make(.unsupported, extensionID: nil) == nil)
        let none = try #require(VoiceUnavailableNote.make(.noExtension, extensionID: nil))
        #expect(none.title == "hub 上还没开实时语音" && none.settings == .extensions)
        let off = try #require(VoiceUnavailableNote.make(.noExtension, extensionID: "voice"))
        #expect(off.settings == .voiceExtension("voice"))
        let setup = try #require(VoiceUnavailableNote.make(.needsSetup(["还没填 Key"]), extensionID: "voice"))
        #expect(setup.title == "还没配实时语音服务" && setup.message == "还没填 Key" && setup.settings == .voiceExtension("voice"))
        #expect(VoiceUnavailableNote.make(.limitedDevice, extensionID: "voice")?.settings == nil, "去设置 is for full devices only")
        #expect(VoiceUnavailableNote.make(.readOnly, extensionID: "voice")?.settings == nil)
        #expect(VoiceUnavailableNote.make(.outdated, extensionID: "voice")?.settings == .extensions)
    }

    @Test func theUsersOwnEndingsLeaveNothing() {
        for end in [VoiceEnd.user, .left, .replaced] { #expect(VoiceEndNote.make(end, extensionID: "voice") == nil) }
    }

    @Test func passingEndingsOfferContinue() throws {
        for end in [VoiceEnd.background, .interrupted, .microphoneTaken, .idle, .lost, .audioReset] {
            let note = try #require(VoiceEndNote.make(end, extensionID: "voice"))
            #expect(note.again == .resume && note.haptic == .attention, "\(end)")
        }
    }

    @Test func failuresKeepTheirOwnWordsForDetails() throws {
        let forbidden = try #require(VoiceEndNote.make(.failed(code: .upstreamForbidden, message: "401 from upstream (cluster x)"), extensionID: "voice"))
        #expect(forbidden.text == "连不上实时语音服务（Key 不对，或所在地区不支持）")
        #expect(forbidden.again == .retry && forbidden.settings == .voiceExtension("voice"))
        #expect(forbidden.details == "401 from upstream (cluster x)" && forbidden.haptic == .failure)
        #expect(VoiceEndNote.make(.failed(code: .upstreamUnavailable, message: ""), extensionID: "voice")?.details == nil)
        #expect(VoiceEndNote.make(.failed(code: .targetGone, message: "gone"), extensionID: "voice")?.again == nil)
        #expect(VoiceEndNote.make(.permissionDenied, extensionID: "voice")?.settings == .system)
        #expect(VoiceEndNote.make(.rejected(status: 403), extensionID: "voice")?.again == nil)
        #expect(VoiceEndNote.make(.modelUnavailable("voice-old"), extensionID: "voice")?.settings == .model("voice"))
    }
}
