import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The page's side of a call (docs/assistant/voice.md 6.2, 6.5, 6.10): what it may read without being drawn again on every phase,
/// what Never Mind and Send Back as Feedback do, and the cues.
@MainActor
struct VoiceModeTests {
    private func live(_ rig: VoiceRig) async -> (VoiceConversation, FakeVoiceSocket) {
        let call = rig.call()
        await call.begin()
        let socket = rig.socket!
        socket.ready(rig.key)
        return (call, socket)
    }

    @Test func isOverChangesOnlyAsTheCallEndsOrPicksUp() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        #expect(!call.isOver)
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"修一下","final":false}"#)
        #expect(call.phase == .live(.hearing) && !call.isOver)
        rig.audio.emit(.interrupted)
        #expect(call.isOver && call.phase == .ended(.interrupted))
        await call.restart()
        #expect(!call.isOver && call.phase == .connecting)
    }

    @Test func thePageAsksWhetherItsCallIsOn() async {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let other = SessionKey(group: rig.key.group, machineID: "m2", sessionID: "s2")
        #expect(calls.call(on: rig.key) === calls.current && calls.call(on: other) == nil)
        #expect(calls.isLive(on: rig.key) && !calls.isLive(on: other))
        calls.appDidEnterBackground()
        #expect(calls.isBound(to: rig.key) && !calls.isLive(on: rig.key), "ended: 「语音已结束 · 继续」 stays, the composer is back")
        calls.leave(rig.key)
        #expect(!calls.isBound(to: rig.key))
    }

    @Test func dismissingARefusalTakesItsLineAway() async throws {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"run","id":"voice-1","task":"修登录页","state":"refused","code":"unprompted","msg_ids":["voice-1"],"base_seq":1}"#)
        let before = VoiceBarLine.make(phase: call.phase, link: call.link, runs: call.runs, session: nil, cards: [], muted: false,
                                       label: "Claude", dismissed: call.dismissed)
        #expect(before.kind == .notSent)
        call.dismiss(run: "voice-1")
        let after = VoiceBarLine.make(phase: call.phase, link: call.link, runs: call.runs, session: nil, cards: [], muted: false,
                                      label: "Claude", dismissed: call.dismissed)
        #expect(after.kind == .ready)
        #expect(socket.frames.allSatisfy { $0["type"] as? String != "resend" }, "算了 sends nothing")
    }

    @Test func returningThePlanAnswersTheCardWithTheWords() throws {
        let action = VoiceBarLine.Action.returnPlan(card: "plan1", feedback: "第三步先别做", run: "voice-1")
        let body = try JSONSerialization.jsonObject(with: try JSON.encoder.encode(try #require(action.resolution))) as? [String: Any]
        #expect(body?["approve"] as? Bool == false)
        #expect(body?["feedback"] as? String == "第三步先别做")
        #expect(VoiceBarLine.Action.dismiss(run: "voice-1").resolution == nil)
    }

    @Test func clearAndSendGoesWithTheClearableText() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"run","id":"voice-2","task":"顺便修 lint","state":"refused","code":"input_draft","draft":"[⧉ Selected] fix it","clearable":"fix it","msg_ids":["voice-2"],"base_seq":1}"#)
        let line = VoiceBarLine.make(phase: call.phase, link: call.link, runs: call.runs, session: nil, cards: [], muted: false,
                                     label: "Claude", resendable: Set(call.runs.map(\.id).filter { call.canResend($0) }))
        #expect(line.actions.first == .clearAndSend(run: "voice-2"))
        #expect(call.resend("voice-2", clearingDraft: true))
        let resend = socket.frames.last { $0["type"] as? String == "resend" }
        #expect(resend?["id"] as? String == "voice-2" && resend?["clear_draft"] as? String == "fix it")
    }

    /// The extension's `ready` with `features` (`FakeVoiceSocket.ready` says the ones from before #5904).
    private func ready(_ socket: FakeVoiceSocket, _ key: SessionKey, features: [String]) {
        let list = features.map { "\"" + $0 + "\"" }.joined(separator: ",")
        socket.deliver(#"{"type":"ready","protocol":2,"features":[\#(list)],"input_rate":16000,"output_rate":24000,"target":{"machine_id":"\#(key.machineID)","session_id":"\#(key.sessionID)","kind":"agent","agent_label":"Claude Code"}}"#)
    }

    private static let unprompted = #"{"type":"run","id":"voice-3","task":"跑一下测试","state":"refused","code":"unprompted","resendable":true,"msg_ids":["voice-3"],"base_seq":1}"#

    /// #5904: an instruction the user-speech gate held back (refused, unprompted) gets Send Anyway from an extension that
    /// keeps it (`speech_gate` in ready's features), and the tap sends a resend under its id, with nothing to clear.
    @Test func sendAnywayResendsWhatTheGateHeldBack() async {
        let rig = VoiceRig()
        let call = rig.call()
        await call.begin()
        let socket = rig.socket!
        ready(socket, rig.key, features: ["target", "runs", "asks", "resume", "suspend", "speak", "speech_gate"])
        socket.deliver(Self.unprompted)
        let line = VoiceBarLine.make(phase: call.phase, link: call.link, runs: call.runs, session: nil, cards: [], muted: false,
                                     label: "Claude", resendable: Set(call.runs.map(\.id).filter { call.canResend($0) }))
        #expect(line.actions.map(\.name) == ["anyway", "dismiss"])
        #expect(call.resend("voice-3"))
        let resend = socket.frames.last { $0["type"] as? String == "resend" }
        #expect(resend?["id"] as? String == "voice-3" && resend?["clear_draft"] == nil)
    }

    /// #5904 review: a refusal with no task (the model said nothing of what to do, and nothing the user said was there
    /// to take) is kept by no extension: no Send Anyway, and no resend, even from one that lists `speech_gate`.
    @Test func noSendAnywayWithoutATask() async {
        let rig = VoiceRig()
        let call = rig.call()
        await call.begin()
        let socket = rig.socket!
        ready(socket, rig.key, features: ["target", "runs", "asks", "resume", "suspend", "speak", "speech_gate"])
        socket.deliver(#"{"type":"run","id":"voice-4","task":"","state":"refused","code":"unprompted","msg_ids":["voice-4"],"base_seq":1}"#)
        #expect(call.runs.map(\.id) == ["voice-4"])
        #expect(!call.canResend("voice-4"))
        #expect(!call.resend("voice-4"))
        #expect(socket.frames.allSatisfy { $0["type"] as? String != "resend" })
    }

    /// #5904 verification: a run the extension did not keep (a task longer than the bar quotes, so the user could not
    /// see all of what they confirm) comes without `resendable`: no Send Anyway, even from one that lists `speech_gate`.
    @Test func noSendAnywayForARunTheExtensionDidNotKeep() async {
        let rig = VoiceRig()
        let call = rig.call()
        await call.begin()
        let socket = rig.socket!
        ready(socket, rig.key, features: ["target", "runs", "asks", "resume", "suspend", "speak", "speech_gate"])
        socket.deliver(#"{"type":"run","id":"voice-5","task":"把文档的拼写改一下…","state":"refused","code":"unprompted","msg_ids":["voice-5"],"base_seq":1}"#)
        #expect(call.runs.map(\.id) == ["voice-5"])
        #expect(!call.canResend("voice-5"))
        #expect(!call.resend("voice-5"))
        #expect(socket.frames.allSatisfy { $0["type"] as? String != "resend" })
    }

    /// An extension from before #5904 keeps no unprompted run: its resend would do nothing, so there is no Send Anyway.
    @Test func anOlderExtensionOffersNoSendAnyway() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(Self.unprompted)
        #expect(!call.canResend("voice-3"))
        #expect(!call.resend("voice-3"))
        #expect(socket.frames.allSatisfy { $0["type"] as? String != "resend" })
    }

    @Test func cuesPlayAtTheVoicesRateWhileTheCallIsOn() async throws {
        let rig = VoiceRig()
        let call = rig.call()
        await call.begin()
        rig.socket?.ready(rig.key, output: 48000)
        call.play(.ready)
        let pcm = try #require(rig.audio.cues.last)
        #expect(pcm.count == VoiceCueTone.samples(.ready, rate: 48000).count * 2)
        call.end(.user)
        call.play(.attention)
        #expect(rig.audio.cues.count == 1, "nothing plays once the call is over")
    }

    @Test func cueTonesAreShortQuietAndRampedFromSilence() {
        for cue in Theme.VoiceCue.allCases {
            let rate = 24000
            let samples = VoiceCueTone.samples(cue, rate: rate)
            let notes = Double(cue.notes.count)
            let expected = Int(Theme.VoiceCue.note * Double(rate)) * cue.notes.count + Int(Theme.VoiceCue.gap * Double(rate)) * (cue.notes.count - 1)
            #expect(samples.count == expected, "\(cue)")
            #expect(Double(samples.count) / Double(rate) <= notes * (Theme.VoiceCue.note + Theme.VoiceCue.gap), "\(cue) is short")
            #expect(samples.map { abs(Int($0)) }.max()! <= Int(Theme.VoiceCue.gain * Double(Int16.max)) + 1, "\(cue) stays under the gain")
            #expect(abs(Int(samples.first!)) <= 1 && abs(Int(samples.last!)) <= 1, "\(cue) starts and ends at silence")
        }
        #expect(VoiceCueTone.samples(.ready, rate: 0).isEmpty)
    }
}
