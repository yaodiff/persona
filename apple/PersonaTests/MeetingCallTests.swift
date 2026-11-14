import AVFoundation
import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Realtime voice in the meeting on the phone (#3862 phase 4; contract docs/assistant/meeting-minutes.md「Phase 4: realtime
/// voice in the meeting」, the phone's side docs/apple/recording.md「Realtime voice (phase 4)」): who has a meeting call,
/// its start frame and the wire's `talk` frames, the uplink that sends only inside Talk spans, reconnecting while the
/// meeting records, the Lock Screen's talk window, Talk without a call, Persona's echo of the call's voice marked (#5905), the
/// Live Activity's Talk, and no voice in a room opened as a chat. Fakes only (`VoiceRig`, `FakeVoiceSocket`,
/// `FakeVoiceAudio`, a clock moved by hand); nothing reaches a network or a microphone.
@MainActor struct MeetingCallTests {
    // MARK: Fixtures

    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let recording = UUID(uuidString: "6F9A1D2E-0000-4000-8000-00000000A11C")!

    private func meeting(_ role: MeetingAttendance = .participant, room: Bool = true, active: Bool = true) -> RecordingEntry {
        var e = RecordingEntry(id: recording, createdAt: now, source: .phone, title: "Weekly", locale: "zh-CN", active: active)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.attendance = room ? RecordingAttendance.room(role, private: false, photos: false, liveView: true)
            : RecordingAttendance(role: role, session: "m1/s9", sessionTitle: "Agent")
        return e
    }

    private func room(_ state: String = "ready") -> MeetingRoom {
        try! JSON.decoder.decode(MeetingRoom.self, from: Data(#"{"state":"\#(state)","machine_id":"m1","session_id":"room1"}"#.utf8))
    }

    private static let voice = try! JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"voice","name":"实时语音","kind":"voice","status":"running"}"#.utf8))

    private func info(_ features: [String], protocolVersion: Int = 2, setup: [String] = []) -> VoiceInfoState {
        .info(VoiceInfo(protocolVersion: protocolVersion, features: features, setup: setup))
    }

    /// A meeting call on the rig, bound to the room `rig.key` stands for.
    private func meetingCall(_ rig: VoiceRig) -> VoiceConversation {
        VoiceConversation(key: rig.key, extensionID: "voice", surface: .phone, meeting: VoiceMeeting(recordingID: recording), deps: rig.deps)
    }

    /// The extension's `ready` for a meeting call: protocol 2 with `meeting`.
    private func ready(_ socket: FakeVoiceSocket, _ key: SessionKey, features: [String] = ["target", "runs", "resume", "speak", "meeting"]) {
        let list = features.map { "\"\($0)\"" }.joined(separator: ",")
        socket.deliver(#"{"type":"ready","protocol":2,"features":[\#(list)],"input_rate":16000,"output_rate":24000,"target":{"machine_id":"\#(key.machineID)","session_id":"\#(key.sessionID)","kind":"assistant","title":"Weekly","agent_label":"Persona"}}"#)
    }

    // MARK: Who has a call

    /// A participant room meeting with its room ready wants a call; a listener, a room not up, an older manager, a #3746
    /// meeting, a note or a stopped recording doesn't (Talk goes without one there).
    @Test func whoHasAMeetingCall() {
        #expect(MeetingCallRule.wanted(meeting(), roomMissing: false, room: room()))
        #expect(!MeetingCallRule.wanted(meeting(.listener), roomMissing: false, room: room()), "a listener asks without a call")
        #expect(!MeetingCallRule.wanted(meeting(.off), roomMissing: false, room: room()))
        #expect(!MeetingCallRule.wanted(meeting(), roomMissing: false, room: room("starting")), "the room isn't ready yet")
        #expect(!MeetingCallRule.wanted(meeting(), roomMissing: false, room: room("unavailable")))
        #expect(!MeetingCallRule.wanted(meeting(), roomMissing: false, room: nil))
        #expect(!MeetingCallRule.wanted(meeting(), roomMissing: true, room: room()), "an older manager took the meeting without its room")
        #expect(!MeetingCallRule.wanted(meeting(room: false), roomMissing: false, room: room()), "a #3746 meeting has no room")
        #expect(!MeetingCallRule.wanted(meeting(active: false), roomMissing: false, room: room()), "it ended")
        var note = meeting()
        note.meeting = nil
        #expect(!MeetingCallRule.wanted(note, roomMissing: false, room: room()), "a note")
        var withoutIDs = room()
        withoutIDs.sessionID = nil
        #expect(!MeetingCallRule.wanted(meeting(), roomMissing: false, room: withoutIDs), "a room with no ids can't be bound")
    }

    /// Only a voice extension that runs, speaks protocol 2, is set up and lists `meeting` takes meeting calls.
    @Test func theVoiceExtensionMustListMeeting() {
        #expect(MeetingCallRule.extensionID(extensions: [Self.voice], info: info(["target", "meeting"])) == "voice")
        #expect(MeetingCallRule.extensionID(extensions: [Self.voice], info: info(["target", "speak"])) == nil, "an extension from before phase 4")
        #expect(MeetingCallRule.extensionID(extensions: [Self.voice], info: info(["meeting"], protocolVersion: 1)) == nil)
        #expect(MeetingCallRule.extensionID(extensions: [Self.voice], info: info(["meeting"], setup: ["no key"])) == nil)
        #expect(MeetingCallRule.extensionID(extensions: [Self.voice], info: .missing) == nil)
        #expect(MeetingCallRule.extensionID(extensions: nil, info: nil) == nil, "not asked yet")
        let stopped = try! JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"voice","name":"语音","kind":"voice","status":"stopped"}"#.utf8))
        #expect(MeetingCallRule.extensionID(extensions: [stopped], info: info(["meeting"])) == nil)
    }

    /// A running call ends only on the hub's definite no; an ask that failed, or an extension restarting, says nothing.
    @Test func onlyADefiniteNoEndsARunningCall() {
        #expect(!MeetingCallRule.declines(extensions: [Self.voice], info: info(["target", "meeting"])))
        #expect(MeetingCallRule.declines(extensions: [Self.voice], info: info(["target", "speak"])), "no meeting")
        #expect(MeetingCallRule.declines(extensions: [Self.voice], info: info(["meeting"], protocolVersion: 1)))
        #expect(MeetingCallRule.declines(extensions: [Self.voice], info: .missing))
        #expect(MeetingCallRule.declines(extensions: [Self.voice], info: .forbidden))
        #expect(MeetingCallRule.declines(extensions: [], info: nil), "no voice extension at all")
        #expect(!MeetingCallRule.declines(extensions: [Self.voice], info: .failed("offline")), "a failed ask")
        #expect(!MeetingCallRule.declines(extensions: nil, info: nil), "not known")
        let restarting = try! JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"voice","name":"语音","kind":"voice","status":"starting"}"#.utf8))
        #expect(!MeetingCallRule.declines(extensions: [restarting], info: nil), "an extension restarting")
    }

    /// P9: the phone asks whether the hub takes meeting calls as the meeting wants one, then after 30 s, 2 and 5 minutes
    /// while the answer says nothing either way, then no more; never again once the hub answered.
    @Test func askingForMeetingCallsStops() {
        #expect(MeetingCallRule.probeAgain(extensions: nil, info: nil, tries: 1) == 30)
        #expect(MeetingCallRule.probeAgain(extensions: [Self.voice], info: .failed("offline"), tries: 2) == 120)
        #expect(MeetingCallRule.probeAgain(extensions: [Self.voice], info: .failed("offline"), tries: 3) == 300)
        #expect(MeetingCallRule.probeAgain(extensions: [Self.voice], info: .failed("offline"), tries: 4) == nil, "the retries are used up")
        #expect(MeetingCallRule.probeAgain(extensions: [Self.voice], info: info(["target", "speak"]), tries: 1) == nil, "an extension without meeting: never again")
        #expect(MeetingCallRule.probeAgain(extensions: [], info: nil, tries: 1) == nil, "no voice extension")
        #expect(MeetingCallRule.probeAgain(extensions: [Self.voice], info: info(["meeting"]), tries: 1) == nil, "it takes them: the call starts")
    }

    /// Talk is offered wherever Persona can be asked in a room meeting, listening included; it sends audio to the voice
    /// service only for a participant on a hub whose extension takes meeting calls (the privacy texts' rule).
    @Test func whereTalkIsAndWhereItSendsAudio() {
        #expect(MeetingCallRule.talkOffered(meeting(.listener), roomMissing: false))
        #expect(MeetingCallRule.talkOffered(meeting(.participant), roomMissing: false))
        #expect(!MeetingCallRule.talkOffered(meeting(.off), roomMissing: false))
        #expect(!MeetingCallRule.talkOffered(meeting(), roomMissing: true))
        #expect(!MeetingCallRule.talkOffered(meeting(room: false), roomMissing: false), "#3746 meetings keep Ask")
        #expect(MeetingCallRule.talkSendsAudio(conversation: true, role: .participant, voice: "voice"))
        #expect(!MeetingCallRule.talkSendsAudio(conversation: true, role: .listener, voice: "voice"), "a listener's Talk stays on the phone")
        #expect(!MeetingCallRule.talkSendsAudio(conversation: true, role: .participant, voice: nil))
        #expect(!MeetingCallRule.talkSendsAudio(conversation: false, role: .participant, voice: "voice"))
    }

    /// A call that ended by itself starts again 30 s later, unless the end says it never can.
    @Test func whichEndsStartAgain() {
        #expect(MeetingCallRule.retries(after: .lost))
        #expect(MeetingCallRule.retries(after: .failed(code: .notMeetingRoom, message: "")), "the room may be ready by then")
        #expect(MeetingCallRule.retries(after: .failed(code: .upstreamFailed, message: "")))
        #expect(MeetingCallRule.retries(after: .rejected(status: 404)))
        #expect(!MeetingCallRule.retries(after: .rejected(status: 401)))
        #expect(!MeetingCallRule.retries(after: .rejected(status: 403)))
        #expect(!MeetingCallRule.retries(after: .incompatible))
        #expect(!MeetingCallRule.retries(after: .modelUnavailable("gone")))
        #expect(!MeetingCallRule.retries(after: .left))
    }

    // MARK: The start frame and the talk frames

    /// Protocol 2 in meeting mode: the recording (lowercase), the room as the target, whether echo cancellation runs, no
    /// `recent` (the extension reads the room's own turns). Not a phone call: the microphone is the recording's and the
    /// screen may lock.
    @Test func theStartFrameIsAMeetingCall() async throws {
        let rig = VoiceRig()
        rig.audio.cancelsEcho = true
        let call = meetingCall(rig)
        await call.begin()
        let start = try #require(rig.socket?.start)
        #expect(start["protocol"] as? Int == 2)
        #expect(start["mode"] as? String == "meeting")
        #expect(start["recording_id"] as? String == recording.uuidString.lowercased())
        #expect(start["target"] as? [String: String] == ["machine_id": "m1", "session_id": "s1"])
        #expect(start["echo_cancellation"] as? Bool == true)
        #expect(start["recent"] == nil)
        #expect(rig.idleTimer.changes.isEmpty, "a meeting call keeps no screen on")
        #expect(rig.microphone.claim(UUID()), "the call never claims the microphone: the recording holds it")
        let plain = rig.call()
        let ordinary = VoiceStart(model: nil, voice: nil, target: VoiceTarget(rig.key))
        #expect(!VoiceMessage.start(ordinary).contains("mode"), "an ordinary call says no mode")
        #expect(plain.meeting == nil)

        rig.audio.cancelsEcho = false
        let other = VoiceRig()
        other.audio.cancelsEcho = false
        await meetingCall(other).begin()
        #expect(other.socket?.start?["echo_cancellation"] as? Bool == false, "the recording records without it")
    }

    /// #5904: a meeting call says nothing of the user-speech gate, even with Only Send What I Just Said turned off on
    /// this device: the meeting page has no Send Anyway, and the extension keeps the gate on for what goes to the room
    /// as the owner's words. Turning the switch while it runs does not reopen it.
    @Test func aMeetingCallSaysNothingOfTheSpeechGate() async throws {
        let rig = VoiceRig()
        let key = "voice.speechGate." + rig.key.group + ".voice"
        UserDefaults.standard.set(false, forKey: key)
        defer { UserDefaults.standard.removeObject(forKey: key) }
        let call = meetingCall(rig)
        await call.begin()
        let start = try #require(rig.socket?.start)
        #expect(start["speech_gate"] == nil)
        #expect(start["mode"] as? String == "meeting")
        ready(rig.socket!, rig.key)
        call.change(speechGate: !call.speechGate)
        #expect(rig.sockets.count == 1, "no new connection for the switch")
    }

    @Test func theTalkFrames() throws {
        #expect(VoiceMessage.talk(true) == #"{"on":true,"type":"talk"}"#)
        #expect(VoiceMessage.talk(false) == #"{"on":false,"type":"talk"}"#)
    }

    /// An extension that answers `ready` without `meeting` would hold an ordinary call bound to the room: the call ends.
    @Test func aReadyWithoutMeetingEndsTheCall() async {
        let rig = VoiceRig()
        let call = meetingCall(rig)
        await call.begin()
        ready(rig.socket!, rig.key, features: ["target", "runs"])
        #expect(call.phase == .ended(.incompatible))
    }

    // MARK: Hearing only Talk

    /// The uplink of a meeting call sends nothing outside a span, before `ready` included (nothing is held for later),
    /// and everything between `talk on` and `talk off`, in that order on the socket.
    @Test func onlyTheSpansAudioGoesUp() async throws {
        let rig = VoiceRig()
        let call = meetingCall(rig)
        await call.begin()
        let socket = try #require(rig.socket)
        rig.audio.capture(.pcm(320, 1))
        #expect(!call.talk(true), "no span before ready: Talk goes without the call then")
        ready(socket, rig.key)
        #expect(call.phase == .live(.listening))
        #expect(socket.audio.isEmpty, "the meeting before ready was never held")
        rig.audio.capture(.pcm(320, 2))
        #expect(socket.audio.isEmpty, "outside a span nothing goes")

        #expect(call.talk(true))
        #expect(call.talking)
        rig.audio.capture(.pcm(320, 3))
        rig.audio.capture(.pcm(320, 4))
        #expect(call.talk(false))
        rig.audio.capture(.pcm(320, 5))
        #expect(socket.audio == [.pcm(320, 3), .pcm(320, 4)], "only the span's audio")
        let talks = socket.frames.filter { $0["type"] as? String == "talk" }.map { $0["on"] as? Bool }
        #expect(talks == [true, false])
        #expect(!call.talking)
    }

    /// The talk frame and the audio share the uplink's lock, so the socket gets them in order: on, the span's audio, off.
    @Test func theSpanKeepsItsOrderOnTheSocket() {
        let uplink = VoiceUplink(gated: true)
        let order = Order()
        uplink.open(rate: 16000) { pcm in order.add("audio \(pcm.count)") }
        uplink.push(.pcm(160), rate: 16000)
        uplink.talk(true) { order.add("on") }
        uplink.push(.pcm(160), rate: 16000)
        uplink.push(.pcm(80), rate: 16000)
        uplink.talk(false) { order.add("off") }
        uplink.push(.pcm(160), rate: 16000)
        #expect(order.items == ["on", "audio 320", "audio 160", "off"])
        let ordinary = VoiceUplink()
        #expect(!ordinary.gated)
        ordinary.talk(true) { order.add("ignored") }
        #expect(!order.items.contains("ignored"), "an ordinary call has no spans")
    }

    /// A socket that drops takes the span with it: nothing of the next connection goes until a new `talk on`.
    @Test func aDropEndsTheSpan() async throws {
        let rig = VoiceRig()
        let call = meetingCall(rig)
        await call.begin()
        ready(rig.socket!, rig.key)
        #expect(call.talk(true))
        rig.socket!.drop()
        #expect(!call.talking)
        await rig.advance(1)
        let next = try #require(rig.socket)
        ready(next, rig.key)
        rig.audio.capture(.pcm(320, 7))
        #expect(next.audio.isEmpty)
    }

    /// `frames` samples of a half-scale signal at 16 kHz, as the recorder's tap hands them over (`db`: a steady signal at
    /// that level, dBFS).
    private func tapBuffer(_ frames: Int, db: Float? = nil) throws -> AVAudioPCMBuffer {
        let format = try #require(AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 16000, channels: 1, interleaved: false))
        let buffer = try #require(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(frames)))
        buffer.frameLength = AVAudioFrameCount(frames)
        let value = db.map { pow(10, $0 / 20) } ?? 0.5
        for i in 0..<frames { buffer.floatChannelData![0][i] = value }
        return buffer
    }

    /// The recorder's tap hands the call PCM16 of what it captured (and its level for the talk window).
    @Test func theTapFeedsTheUplink() throws {
        let tap = MeetingTalkTap()
        let uplink = VoiceUplink(gated: true)
        let sent = Order()
        uplink.open(rate: 16000) { pcm in sent.add("\(pcm.count)") }
        tap.connect(uplink)
        let buffer = try tapBuffer(160)
        tap.feed(buffer, at: 0)
        #expect(sent.items.isEmpty, "outside a span")
        #expect(abs(tap.level - (-6.02)) < 0.1, "the level of a half-scale signal, dBFS")
        uplink.talk(true) {}
        tap.feed(buffer, at: 0.01)
        #expect(sent.items.isEmpty, "the uplink's span alone doesn't open the tap's")
        tap.open(at: 0.02)
        tap.feed(buffer, at: 0.02)
        #expect(sent.items == ["320"], "160 samples of PCM16")
        tap.connect(nil)
        tap.feed(buffer, at: 0.03)
        #expect(sent.items == ["320"], "nothing after the call went")
        let samples: [Float] = [0, 1, -1, 2]
        let pcm = samples.withUnsafeBufferPointer { MeetingTalkTap.pcm16($0) }
        #expect(VoicePCM.samples(pcm) == [0, 32767, -32767, 32767], "clipped")
    }

    /// A buffer reaches the tap only once it is full: the one that straddles the press sends only its samples from the
    /// press on, the one that straddles the release only those before it, and `covers` says when the release's has gone
    /// through (then `talk off` may go).
    @Test func onlyTheSamplesInsideTheSpanGoUp() throws {
        let tap = MeetingTalkTap()
        let uplink = VoiceUplink(gated: true)
        let sent = Order()
        uplink.open(rate: 16000) { pcm in sent.add("\(pcm.count / 2)") }
        tap.connect(uplink)
        uplink.talk(true) {}
        // Pressed at 1.0 s, 50 ms into a buffer of 100 ms that began at 0.95 s.
        tap.open(at: 1.0)
        tap.feed(try tapBuffer(1600), at: 0.85)
        #expect(sent.items.isEmpty, "a buffer wholly before the press")
        tap.feed(try tapBuffer(1600), at: 0.95)
        #expect(sent.items == ["800"], "only the 50 ms after the press")
        tap.feed(try tapBuffer(1600), at: 1.05)
        #expect(sent.items == ["800", "1600"], "a buffer inside the span goes whole")
        // Let go at 1.2 s, 50 ms into the next buffer.
        tap.close(at: 1.2)
        #expect(!tap.covers(1.2), "the buffer that holds the release hasn't come yet")
        tap.feed(try tapBuffer(1600), at: 1.15)
        #expect(sent.items == ["800", "1600", "800"], "only the 50 ms before the release")
        #expect(tap.covers(1.2))
        tap.feed(try tapBuffer(1600), at: 1.25)
        #expect(sent.items.count == 3, "nothing after the release, though the uplink's span is still open")
        tap.clear()
        tap.feed(try tapBuffer(1600), at: 1.35)
        #expect(sent.items.count == 3)
        #expect(MeetingTalkTap.inside(start: 2, end: nil, position: 0.9, count: 1600, rate: 16000) == nil, "a buffer before the span")
        #expect(MeetingTalkTap.inside(start: 0, end: 0.5, position: 1, count: 1600, rate: 16000) == nil, "a buffer after it")
        #expect(MeetingTalkTap.inside(start: 1, end: nil, position: 0.95, count: 1600, rate: 16000) == 800..<1600)
    }

    /// A press after a whole 512 ms buffer (4096 frames at a Bluetooth headset's 8 kHz) and a stall in its delivery: the
    /// span starts no earlier than the press, so nothing the room said before it goes up (the old half-second clamp put
    /// it 100 ms early here). The release is held back instead: the span never runs past the finger.
    @Test func aLateBufferNeverSendsWhatCameBeforeThePress() throws {
        let tap = MeetingTalkTap()
        let uplink = VoiceUplink(gated: true)
        let sent = Order()
        uplink.open(rate: 16000) { pcm in sent.add("\(pcm.count / 2)") }
        tap.connect(uplink)
        uplink.talk(true) {}
        // The tap has the audio up to 10.24 s; the press comes 0.6 s of capture later (a 0.512 s buffer filling, delivered
        // late), so it truly lies at 10.84 s.
        let press = RecordingSegmentWriter.placed(.press, base: 10.24, since: 0.6, longestBuffer: 0.512)
        #expect(press >= 10.84)
        tap.open(at: press)
        tap.feed(try tapBuffer(8192), at: 10.24)
        tap.feed(try tapBuffer(8192), at: 10.752)
        let first = try #require(sent.items.first.flatMap { Int($0) })
        let total = sent.items.compactMap { Int($0) }.reduce(0, +)
        #expect(Double(total) / 16000 <= 11.264 - 10.84 + 1e-9, "nothing from before the press: \(sent.items)")
        #expect(first > 0)
        // Let go after a stall of 2 s: the release is held at the longest buffer and its delivery past what the tap had.
        let release = RecordingSegmentWriter.placed(.release, base: 11.264, since: 2, longestBuffer: 0.512)
        #expect(release < 11.264 + 2 && release <= 11.264 + 0.512 + RecordingSegmentWriter.deliveryMargin + 1e-9)
    }

    /// A span the call carried and lost (its socket dropped, the call ended) is asked without the call over its whole
    /// length, from where it began; while the call carries it, or for one that never went to it, nothing changes.
    @Test func aSpanTheCallLosesIsAskedWithoutIt() async throws {
        let span = MeetingCall.Talk(recording: recording, source: .hold, viaCall: true, start: 42, opened: now)
        let lost = try #require(span.lost(callTalking: false))
        #expect(!lost.viaCall)
        #expect(lost.id == span.id && lost.start == 42 && lost.source == .hold, "the same span, from its start")
        #expect(span.lost(callTalking: true) == nil, "the call still carries it")
        #expect(lost.lost(callTalking: false) == nil, "already asked without the call")
        // What tells: the call's `talking`, which a dropped socket clears.
        let rig = VoiceRig()
        let call = meetingCall(rig)
        await call.begin()
        ready(rig.socket!, rig.key)
        #expect(call.talk(true))
        #expect(span.lost(callTalking: call.talking) == nil)
        rig.socket!.drop()
        #expect(span.lost(callTalking: call.talking)?.viaCall == false, "the drop hands the span to `…/ask`")
    }

    /// A span closed by the recording's pause is still asked with its words: the open segment is cut while paused too, so
    /// it is transcribed now rather than after Continue.
    @Test func aPausedRecordingStillCutsTheSpansSegment() throws {
        #expect(RecordingCapture.cutsOnFlush(.recording))
        #expect(RecordingCapture.cutsOnFlush(.paused))
        #expect(!RecordingCapture.cutsOnFlush(.idle))
        #expect(!RecordingCapture.cutsOnFlush(.starting))
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let writer = RecordingSegmentWriter(directory: dir)
        writer.append(try tapBuffer(48000), startedAt: now)
        writer.pause()
        let cut = try #require(writer.cut(), "the span's audio is in the segment the pause left open")
        #expect(abs(cut.duration - 3) < 0.001)
        #expect(writer.cut() == nil, "nothing more until the recording goes on")
    }

    // MARK: Reconnecting

    /// A meeting call reconnects as protocol 2 does (1, 4, 10 s, with `resume`), then every 30 s while the meeting
    /// records, never ending as lost.
    @Test func aMeetingCallKeepsTrying() async {
        let rig = VoiceRig()
        let call = meetingCall(rig)
        await call.begin()
        ready(rig.socket!, rig.key)
        rig.socket!.drop()
        let delays: [TimeInterval] = [1, 4, 10, 30, 30]
        for (n, delay) in delays.enumerated() {
            #expect(call.phase == .reconnecting(attempt: n + 1))
            let before = rig.sockets.count
            await rig.advance(delay - 0.5)
            #expect(rig.sockets.count == before, "not before \(delay) s")
            await rig.advance(0.5)
            #expect(rig.sockets.count == before + 1, "a new socket after \(delay) s")
            rig.socket!.drop()
        }
        #expect(!call.phase.isEnded)
        await rig.advance(30)
        ready(rig.socket!, rig.key)
        #expect(call.phase == .live(.listening))
        #expect(rig.socket?.start?["mode"] as? String == "meeting", "every connection starts as a meeting call")
    }

    /// No ten-minute idle end: the call is connected for the whole meeting.
    @Test func noIdleEnd() async {
        let rig = VoiceRig()
        let call = meetingCall(rig)
        await call.begin()
        ready(rig.socket!, rig.key)
        rig.clock.advance(VoiceIdle.limit + 60)
        for _ in 0..<20 { await Task.yield() }
        #expect(call.phase == .live(.listening))
    }

    // MARK: The talk window

    @Test func theWindowEndsTwoSecondsAfterSpeech() {
        var window = MeetingTalkWindow(opened: now)
        #expect(window.heard(-60, at: now.addingTimeInterval(1)) == nil)
        #expect(window.heard(-20, at: now.addingTimeInterval(3)) == nil, "speech")
        #expect(window.heard(-55, at: now.addingTimeInterval(4)) == nil)
        #expect(window.heard(-30, at: now.addingTimeInterval(4.5)) == nil, "speech again")
        #expect(window.heard(-55, at: now.addingTimeInterval(6.4)) == nil)
        #expect(window.heard(-55, at: now.addingTimeInterval(6.5)) == .silence)
        #expect(window.ended == .silence)
        #expect(window.heard(-20, at: now.addingTimeInterval(7)) == nil, "nothing after it ended")
    }

    @Test func theWindowEndsAfterTwentySecondsWithoutSpeech() {
        var window = MeetingTalkWindow(opened: now)
        #expect(window.heard(-60, at: now.addingTimeInterval(19.9)) == nil, "silence before speech doesn't end it")
        #expect(window.tick(now.addingTimeInterval(20)) == .noSpeech)
    }

    @Test func theWindowEndsAfterSixtySecondsWhateverIsSaid() {
        var window = MeetingTalkWindow(opened: now)
        for s in stride(from: 0.5, to: 60, by: 0.5) {
            #expect(window.heard(-25, at: now.addingTimeInterval(s)) == nil)
        }
        #expect(window.heard(-25, at: now.addingTimeInterval(60)) == .limit)
    }

    /// The owner speaks 3 s into the phone over a room that keeps talking at a normal level (above `speechLevel`): the
    /// window ends 2 s after they stop, not at its 60 s limit with the room's talk going to the voice service meanwhile.
    @Test func theRoomTalkingDoesNotKeepTheWindowOpen() {
        let chatter: Float = -32
        var window = MeetingTalkWindow(opened: now, room: .init(levels: Array(repeating: chatter, count: 50), seconds: 5))
        #expect(window.speechThreshold == chatter + MeetingTalkWindow.aboveRoom)
        func at(_ tenths: Int) -> Date { now.addingTimeInterval(Double(tenths) / 10) }
        for t in 1...10 { #expect(window.heard(chatter, at: at(t)) == nil, "the room, before the owner speaks") }
        for t in 11...40 { #expect(window.heard(-14, at: at(t)) == nil, "the owner, into the phone") }
        #expect(window.heardSpeech)
        for t in 41...59 { #expect(window.heard(chatter, at: at(t)) == nil, "the room again") }
        #expect(window.heard(chatter, at: at(60)) == .silence, "2 s after the owner stopped, the room still talking")
        // Without the room's level (too little of it heard) speech is `speechLevel` alone, as before: the same room
        // would keep the window to its limit.
        var unmeasured = MeetingTalkWindow(opened: now, room: .init(levels: Array(repeating: chatter, count: 10), seconds: 1))
        #expect(unmeasured.speechThreshold == MeetingTalkWindow.speechLevel)
        for t in 1...599 {
            if let end = unmeasured.heard(chatter, at: at(t)) { Issue.record("ended at \(t): \(end)") }
        }
        #expect(unmeasured.heard(chatter, at: at(600)) == .limit)
    }

    /// Talk tapped by mistake in a room talking unevenly, the owner silent: no speech, so the window ends after 20 s.
    @Test func aSilentOwnerInATalkingRoomEndsAfterTwentySeconds() {
        let room = (0..<50).map { $0 % 2 == 0 ? Float(-40) : -28 }
        var window = MeetingTalkWindow(opened: now, room: .init(levels: room, seconds: 5))
        for t in 1..<200 {
            #expect(window.heard(room[t % room.count], at: now.addingTimeInterval(Double(t) / 10)) == nil)
        }
        #expect(window.heard(-28, at: now.addingTimeInterval(20)) == .noSpeech)
        #expect(!window.heardSpeech)
    }

    /// What counts as speech: `aboveRoom` over the room's quieter moments, never below `speechLevel`; in a quiet room, or
    /// before enough of the room was heard, `speechLevel` as before.
    @Test func whatCountsAsSpeech() {
        #expect(MeetingTalkWindow.threshold(.init(levels: Array(repeating: -70, count: 30), seconds: 3)) == MeetingTalkWindow.speechLevel,
                "a quiet room")
        #expect(MeetingTalkWindow.threshold(.init()) == MeetingTalkWindow.speechLevel, "nothing heard yet")
        #expect(MeetingTalkWindow.threshold(.init(levels: [-20, -20], seconds: 0.2)) == MeetingTalkWindow.speechLevel, "too little")
        // The owner's own words just before the tap, into the phone, hardly move it: the room's quieter moments set it.
        let room: [Float] = Array(repeating: -42, count: 15) + Array(repeating: -30, count: 25) + Array(repeating: -15, count: 10)
        #expect(MeetingTalkWindow.roomLevel(.init(levels: room.shuffled(), seconds: 5)) == -42)
        #expect(MeetingTalkWindow.threshold(.init(levels: room, seconds: 5)) == -42 + MeetingTalkWindow.aboveRoom)
    }

    /// The tap keeps what it heard of the room over the last 5 s, in pieces of about 100 ms (a long buffer cut into
    /// several), for the window's idea of speech; another recording starts with none.
    @Test func theTapHearsTheRoom() throws {
        let tap = MeetingTalkTap()
        // 8 s of 512 ms buffers (a Bluetooth headset's), the room at -30 dBFS.
        for k in 0..<16 { tap.feed(try tapBuffer(8192, db: -30), at: Double(k) * 0.512) }
        let room = tap.room(before: 16 * 0.512)
        #expect(room.levels.count == 48, "5 pieces a buffer, the last 5 s")
        #expect(room.seconds > 4.8 && room.seconds <= MeetingTalkWindow.roomSpan)
        #expect(room.levels.allSatisfy { abs($0 + 30) < 0.1 })
        #expect(tap.room(before: 2).levels.count == 0, "nothing kept from more than 5 s back")
        tap.forgetRoom()
        #expect(tap.room(before: 16 * 0.512) == MeetingTalkWindow.Room())
        // A buffer half sound, half silence: its pieces show the pause the whole buffer's level hides.
        let half = try tapBuffer(3200)
        for i in 1600..<3200 { half.floatChannelData![0][i] = 0 }
        let samples = UnsafeBufferPointer(start: half.floatChannelData![0], count: 3200)
        let (level, pieces) = MeetingTalkTap.levels(samples, rate: 16000)
        #expect(abs(level - (-9.03)) < 0.05)
        #expect(pieces.map(\.at) == [0, 0.1])
        #expect(abs(pieces[0].db - (-6.02)) < 0.05 && pieces[1].db == MeetingTalkTap.quiet)
        let short = UnsafeBufferPointer(start: half.floatChannelData![0], count: 1365)
        #expect(MeetingTalkTap.levels(short, rate: 16000).pieces.count == 1, "an 85 ms buffer is one piece")
    }

    @Test func anotherTapEndsTheWindow() {
        var window = MeetingTalkWindow(opened: now)
        #expect(window.tap() == .tapped)
        #expect(window.tap() == nil)
        #expect(window.tick(now.addingTimeInterval(61)) == nil)
    }

    /// The Live Activity's tap says what its button showed: Talk opens a window only when nothing is open, Done ends any
    /// open span (a window, or a hold the page lost), and a tap that no longer matches does nothing: a Done tapped just
    /// after the window closed on silence opens no window that would stream the room.
    @Test func theActivitysTapDoesWhatItsButtonShowed() {
        #expect(MeetingCallRule.activityTap(open: true, spanOpen: false) == .open)
        #expect(MeetingCallRule.activityTap(open: false, spanOpen: true) == .close)
        #expect(MeetingCallRule.activityTap(open: false, spanOpen: false) == .ignore, "Done after the window closed itself")
        #expect(MeetingCallRule.activityTap(open: true, spanOpen: true) == .ignore, "Talk drawn before the window opened")
        #expect(MeetingCallRule.activityTap(open: nil, spanOpen: false) == .open, "an activity from before: a toggle")
        #expect(MeetingCallRule.activityTap(open: nil, spanOpen: true) == .close)
        #expect(TalkInMeetingIntent(recordingID: "r", open: false).open == false)
        #expect(TalkInMeetingIntent(recordingID: "r").open == nil)
    }

    /// No span reaches the voice service before the owner was told what Talk sends: from the page (held, or VoiceOver's
    /// window) once the Wording was seen with a span before or is on screen as this one opens; from the Lock Screen or the
    /// island only once seen, since they never show it. Otherwise it is asked without the call, its audio on the phone.
    @Test func noSpanGoesToTheCallBeforeTheWordingShowed() {
        for source in [MeetingTalkSource.hold, .pageWindow] {
            #expect(MeetingCallRule.toCall(live: true, source: source, privacySeen: true, privacyShown: false), "seen: \(source)")
            #expect(MeetingCallRule.toCall(live: true, source: source, privacySeen: false, privacyShown: true), "shown: \(source)")
            #expect(!MeetingCallRule.toCall(live: true, source: source, privacySeen: false, privacyShown: false),
                    "neither (a note in its place, the call not up as the page drew): \(source)")
        }
        #expect(MeetingCallRule.toCall(live: true, source: .activity, privacySeen: true, privacyShown: false), "seen")
        #expect(!MeetingCallRule.toCall(live: true, source: .activity, privacySeen: false, privacyShown: true),
                "the Lock Screen shows no Wording, whatever the page has")
        #expect(!MeetingCallRule.toCall(live: true, source: .activity, privacySeen: false, privacyShown: false), "neither")
        for source in [MeetingTalkSource.hold, .pageWindow, .activity] {
            #expect(!MeetingCallRule.toCall(live: false, source: source, privacySeen: true, privacyShown: true), "no call: asked without it")
        }
    }

    /// Shown is drawn on the page a moment before the span opens, on that recording's page: not drawn in the same instant
    /// (the call came up just as the finger went down), not another recording's, not once it left the screen.
    @Test func theWordingCountsOnlyOnceItWasOnScreen() {
        #expect(!MeetingTalkPrivacy.shown(since: nil, at: now))
        #expect(!MeetingTalkPrivacy.shown(since: now, at: now.addingTimeInterval(0.2)), "drawn as the finger went down")
        #expect(MeetingTalkPrivacy.shown(since: now, at: now.addingTimeInterval(MeetingTalkPrivacy.shownFor)))
        let call = MeetingCall()
        #expect(!call.privacyShown(recording))
        call.showingPrivacy(true, recording: recording)
        let drawn = Date.now
        #expect(!call.privacyShown(recording, at: drawn), "just drawn")
        call.showingPrivacy(true, recording: recording)
        #expect(call.privacyShown(recording, at: drawn.addingTimeInterval(1.5)), "drawn again keeps when it first showed")
        #expect(!call.privacyShown(UUID(), at: drawn.addingTimeInterval(1.5)), "another recording's page")
        call.showingPrivacy(false, recording: UUID())
        #expect(call.privacyShown(recording, at: drawn.addingTimeInterval(1.5)), "another page leaving changes nothing")
        call.showingPrivacy(false, recording: recording)
        #expect(!call.privacyShown(recording, at: drawn.addingTimeInterval(5)), "gone from the screen")
    }

    // MARK: Talk without a call

    /// The spoken question is the span's own words: the sentences at least half inside it, in order.
    @Test func theSpansWordsAreTheQuestion() {
        let segments = [
            RecordingSegment(index: 1, start: 30, duration: 15, sentences: [
                RecordingSentence(start: 30, end: 36, text: "The meeting said this."),
                RecordingSentence(start: 37, end: 41, text: " What did we decide "),
            ]),
            RecordingSegment(index: 2, start: 45, duration: 3, sentences: [RecordingSentence(start: 45, end: 47, text: "on the vendor?")]),
            RecordingSegment(index: 3, start: 48, duration: 10, sentences: [RecordingSentence(start: 48, end: 55, text: "After it.")]),
        ]
        let span = RecordingSpan(start: 37, end: 47.5)
        #expect(MeetingTalkWords.question(segments, span: span) == "What did we decide on the vendor?")
        #expect(MeetingTalkWords.question(segments, span: RecordingSpan(start: 60, end: 62)) == nil, "nothing said in it")
        #expect(MeetingTalkWords.transcribed(segments, span: span))
        let waiting = segments + [RecordingSegment(index: 4, start: 46, duration: 2, sentences: nil)]
        #expect(!MeetingTalkWords.transcribed(waiting, span: span), "a segment of the span still has no text")
    }

    /// A spoken ask says so on the wire (`spoken: true` with `question`); a typed one and a heard one say nothing.
    @Test func aSpokenAskSaysSo() throws {
        func body(_ q: MeetingAsk.Question) throws -> [String: Any] {
            try JSONSerialization.jsonObject(with: JSONEncoder().encode(MeetingAskWire.body(q))) as? [String: Any] ?? [:]
        }
        let spoken = try body(MeetingAsk.Question(id: UUID(), recordingID: recording, at: 42, text: "Who owns it?", spoken: true))
        #expect(spoken["spoken"] as? Bool == true)
        #expect(spoken["question"] as? String == "Who owns it?")
        let typed = try body(MeetingAsk.Question(id: UUID(), recordingID: recording, at: 42, text: "Who owns it?"))
        #expect(typed["spoken"] == nil)
        let heard = try body(MeetingAsk.Question(id: UUID(), recordingID: recording, at: 42, text: nil, spoken: true))
        #expect(heard["spoken"] == nil, "a question heard in the room is never spoken")
    }

    // MARK: Echo

    /// The words a stretch of the call's voice is kept with (#5905): its answers, the voice's own captions, not the
    /// owner's; those kept with an earlier stretch stay out of the next one, except one still being said.
    @Test func aStretchOfTheCallsVoiceCarriesItsAnswers() async throws {
        let rig = VoiceRig()
        let call = meetingCall(rig)
        await call.begin()
        ready(rig.socket!, rig.key)
        #expect(MeetingCallEcho.said(call.captions, kept: []) == nil, "nothing said yet: the time rule")
        rig.socket!.deliver(#"{"type":"transcript","role":"user","id":"u1","text":"What did we decide on the vendor?","final":true}"#)
        rig.socket!.deliver(#"{"type":"transcript","role":"assistant","id":"a1","text":"Let me ask the room.","final":true}"#)
        #expect(MeetingCallEcho.said(call.captions, kept: []) == "Let me ask the room.", "the owner's words are not Persona's")
        let kept = MeetingCallEcho.kept(call.captions, before: [])
        rig.socket!.deliver(#"{"type":"transcript","role":"assistant","id":"a2","text":"On Nov 3 you kept them","final":false}"#)
        #expect(MeetingCallEcho.said(call.captions, kept: kept) == "On Nov 3 you kept them", "the next stretch: only what came after")
        let still = MeetingCallEcho.kept(call.captions, before: kept)
        rig.socket!.deliver(#"{"type":"transcript","role":"assistant","id":"a2","text":"On Nov 3 you kept them through Q3.","final":true}"#)
        #expect(MeetingCallEcho.said(call.captions, kept: still) == "On Nov 3 you kept them through Q3.",
                "an answer still being said as a stretch closed goes on into the next")
        #expect(MeetingCallEcho.kept([], before: still).isEmpty, "captions the call no longer holds are let go")
    }

    /// A stretch of the call's voice is kept as it played, with its answer, never wider: the echo rule widens it itself
    /// (`MeetingEcho.margin`). A line of the answer heard back inside it, or within the margin, is Persona's echo: hidden
    /// in the transcript and kept on the recording; someone talking over it, or the room saying the answer's words after
    /// the margin, stays the meeting's.
    @Test func theCallsEchoIsMarkedAndTheRoomIsKept() {
        let voice = RecordingSpan(start: 100, end: 104, text: "On Nov 3 you kept them through Q3.")
        var e = meeting()
        e.attendance?.spoken = [voice]
        e.segments = [RecordingSegment(index: 0, start: 90, duration: 30, sentences: [
            RecordingSentence(start: 92, end: 98, text: "The room asks."),
            RecordingSentence(start: 100.2, end: 102.5, text: "on Nov 3 you kept them"),
            RecordingSentence(start: 101, end: 103.8, text: "Wait, who owns the export cap?"),
            RecordingSentence(start: 104.3, end: 105.5, text: "through Q3."),
            RecordingSentence(start: 104.6, end: 107, text: "You kept them through Q3, right."),
        ])]
        #expect(e.sentences.map(\.text) == ["The room asks.", "Wait, who owns the export cap?", "You kept them through Q3, right."])
        #expect(e.markedSentences.map { $0.echo } == [false, true, false, true, false])
        #expect(e.segments[0].sentences?.count == 5, "kept on the recording")
    }

    /// The upload while the call's voice plays: to a hub that lists `meeting_echo` the echo of the stretch so far goes
    /// marked and the stretch, with its words, goes in `spoken`; to an older hub the echo is left out. Someone talking
    /// over the voice goes as the meeting's either way, and so into the live log.
    @Test func theCallsOpenStretchMarksTheUploadAndTheLiveLog() async throws {
        var e = meeting()
        e.segments = [RecordingSegment(index: 0, start: 90, duration: 30, sentences: [
            RecordingSentence(start: 92, end: 98, text: "Who owns the export cap?"),
            RecordingSentence(start: 100.2, end: 102.5, text: "Dana owns the export cap."),
            RecordingSentence(start: 101, end: 103.8, text: "Stop for a moment."),
        ])]
        let open = RecordingSpan(start: 100, end: 103, text: "Dana owns the export cap, since Monday.")
        var marked = RecordingUpload(e, hubEcho: true)
        marked.speaking(open)
        #expect(marked.segments.map(\.echo) == [nil, true, nil])
        #expect(marked.spoken?.map(\.text) == [open.text])
        #expect(marked.spoken?.first?.start == 100 && marked.spoken?.first?.end == 103, "never wider than it played")
        var older = RecordingUpload(e, hubEcho: false)
        older.speaking(open)
        #expect(older.segments.map(\.text) == ["Who owns the export cap?", "Stop for a moment."])
        #expect(older.spoken == nil)

        let lib = RecordingLibrary(root: FileManager.default.temporaryDirectory.appending(path: "meeting-call-echo-\(UUID())"))
        try lib.save(e)
        let live = MeetingLive(deps: MeetingLive.Deps(post: { _, from, s in .init(next: from + s.count) }, settled: { _ in true },
                                                      upload: { _ in }, openSpans: { _ in [open] }, wait: { _ in }), library: lib)
        live.append(e.id)
        #expect(lib.entry(e.id)?.attendance?.live.map(\.text) == ["Who owns the export cap?", "Stop for a moment."])
    }

    // MARK: The Live Activity

    /// A room meeting Persona can be asked in has Talk (`talking` false, true while a span is open); a #3746 meeting and
    /// a meeting Persona is off in have none (nil: Ask, or nothing, as before). The state stays small.
    @Test func theLiveActivitysTalk() throws {
        func content(_ e: RecordingEntry, recording: Bool = true, talking: Bool = false) -> RecordingActivity.Content {
            RecordingActivity.content(entry: e, recording: recording, interrupted: false, elapsed: 60, previous: nil, now: now,
                                      talking: talking)
        }
        #expect(content(meeting()).talking == false)
        #expect(content(meeting(), talking: true).talking == true)
        #expect(content(meeting(.listener), talking: true).talking == true, "a listener's Talk is asked without a call")
        #expect(content(meeting(), recording: false, talking: true).talking == false, "paused: no window")
        #expect(content(meeting(room: false)).talking == nil, "#3746 meetings keep Ask")
        #expect(content(meeting(.off)).talking == nil)
        let older = #"{"phase":"recording","runningSince":1800000000,"elapsed":0,"title":"Weekly","meeting":true,"marks":0,"persona":"participant"}"#
        #expect(try JSONDecoder().decode(RecordingActivity.Content.self, from: Data(older.utf8)).talking == nil)
        var long = meeting()
        long.title = String(repeating: "会", count: 300)
        let state = RecordingActivity.content(entry: long, recording: true, interrupted: false, elapsed: 60, previous: nil, now: now,
                                              answer: String(repeating: "答", count: 400), answeredAt: now,
                                              topic: String(repeating: "议", count: 90), talking: true)
        #expect(try JSONEncoder().encode(state).count < 1536)
    }

    /// The third line is the newest answer, whichever way it came: the room's latest ask, or what the meeting call said
    /// (Talk and typed questions go to the call while it is live). It shows for 60 s after it came, then the latest line.
    @Test func theLockScreenShowsTheCallsAnswerToo() throws {
        let asked = RecordingActivity.Answer(text: "From the ask.", at: now)
        let said = RecordingActivity.Answer(text: "From the call.", at: now.addingTimeInterval(5))
        #expect(RecordingActivity.newest([asked, said]) == said)
        #expect(RecordingActivity.newest([said, asked]) == said, "newest first, whatever the order")
        #expect(RecordingActivity.newest([nil, asked]) == asked)
        #expect(RecordingActivity.newest([nil, nil]) == nil)
        var e = meeting()
        e.attendance?.live = [RecordingSentence(start: 50, end: 54, text: "The room goes on.")]
        func content(at: TimeInterval) -> RecordingActivity.Content {
            RecordingActivity.content(entry: e, recording: true, interrupted: false, elapsed: 60, previous: nil,
                                      now: now.addingTimeInterval(at), answer: said.text, answeredAt: said.at)
        }
        #expect(content(at: 30).answer == "From the call.")
        #expect(content(at: 70).answer == nil, "60 s after it came the line takes its place")
        #expect(content(at: 70).line == "The room goes on.")
    }

    /// A meeting call keeps its latest answer: the voice's newest final caption, never a partial one.
    @Test func aMeetingCallKeepsItsLatestAnswer() async throws {
        let rig = VoiceRig()
        let call = meetingCall(rig)
        await call.begin()
        ready(rig.socket!, rig.key)
        rig.socket!.deliver(#"{"type":"transcript","role":"assistant","id":"a1","text":"On Nov 3","final":false}"#)
        #expect(call.lastAnswer == nil, "not final yet")
        rig.socket!.deliver(#"{"type":"transcript","role":"assistant","id":"a1","text":"On Nov 3 you kept them through Q3.","final":true}"#)
        #expect(call.lastAnswer?.text == "On Nov 3 you kept them through Q3.")
        rig.socket!.deliver(#"{"type":"transcript","role":"user","id":"u2","text":"And the export cap?","final":true}"#)
        #expect(call.lastAnswer?.text == "On Nov 3 you kept them through Q3.", "the owner's words are no answer")
        let plain = rig.call()
        #expect(plain.lastAnswer == nil)
    }

    // MARK: A room opened as a chat

    /// No voice button in a room conversation (origin `meeting`): a room takes only meeting calls; the + menu says why.
    @Test func noVoiceInARoomOpenedAsAChat() throws {
        let room = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"m1","session_id":"room1","status":"idle","assistant":true,"origin":"meeting"}"#.utf8))
        let protocol2 = info(["target", "runs", "meeting"])
        #expect(VoiceAvailability.evaluate(platform: .phone, session: room, extensions: [Self.voice], info: protocol2) == .meetingRoom)
        let followup = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"m1","session_id":"f1","status":"idle","assistant":true,"origin":"meeting_followup"}"#.utf8))
        #expect(VoiceAvailability.evaluate(platform: .phone, session: followup, extensions: [Self.voice], info: protocol2).isAvailable,
                "the follow-up is an ordinary conversation")
        let note = try #require(VoiceUnavailableNote.make(.meetingRoom, extensionID: "voice"))
        #expect(note.settings == nil)
        #expect(!note.message.isEmpty)
    }

    /// The fatal errors of meeting calls have codes of their own.
    @Test func theMeetingErrorCodes() throws {
        guard case .error(let failure)? = VoiceFrame.parse(#"{"type":"error","code":"not_meeting_room","message":"no","fatal":true}"#) else {
            Issue.record("not an error frame")
            return
        }
        #expect(failure.code == .notMeetingRoom && failure.fatal)
        #expect(VoiceErrorCode(wire: "meeting_room") == .meetingRoom)
    }
}

/// What a test's sends went out as, in order (any thread).
private final class Order: @unchecked Sendable {
    private let lock = NSLock()
    private var list: [String] = []
    func add(_ item: String) { lock.withLock { list.append(item) } }
    var items: [String] { lock.withLock { list } }
}
