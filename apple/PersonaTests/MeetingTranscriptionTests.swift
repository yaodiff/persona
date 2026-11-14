import AVFoundation
import Foundation
import Testing
@testable import Persona

// Hub transcription on the phone (#3862 phase 5; contract docs/assistant/meeting-minutes.md「Phase 5: hub transcription」,
// the phone's side docs/apple/recording.md「Hub transcription (phase 5)」): the audio socket's frames, the 60 seconds kept
// in memory and what falls out of them, the resend from `ready.from`, the codes the hub may end it with, the Wi-Fi rule,
// the timeline's limits, the mode in effect, what the page shows, the gap uploads and the settings. The hub is a
// `FakeHub` and its socket a `FakeVoiceSocket`; nothing reaches a network or a microphone.

private func json(_ text: String) -> [String: Any] {
    (try? JSONSerialization.jsonObject(with: Data(text.utf8))) as? [String: Any] ?? [:]
}

private func samples(_ count: Int, from: Int = 0) -> [Int16] { (0..<count).map { Int16(truncatingIfNeeded: from + $0) } }

private func decoded(_ data: Data) -> [Int16] {
    stride(from: 0, to: data.count - 1, by: 2).map { Int16(littleEndian: Int16(data[$0]) | Int16(data[$0 + 1]) << 8) }
}

// MARK: - The wire

struct MeetingAudioWireTests {
    @Test func phoneFramesSayWhatTheContractSays() throws {
        let timeline = MeetingTimeline(roles: [.init(at: 0, attendance: "listener")], speaking: [.init(start: 4, end: 9, text: "好的")])
        let start = json(MeetingAudioOut.start(at: 12.00003, timeline: timeline))
        #expect(start["type"] as? String == "start")
        #expect(start["at"] as? Double == 12.0) // rounded to a sample
        let t = try #require(start["timeline"] as? [String: Any])
        #expect((t["roles"] as? [[String: Any]])?.first?["attendance"] as? String == "listener")
        #expect((t["speaking"] as? [[String: Any]])?.first?["text"] as? String == "好的")

        let on = json(MeetingAudioOut.speaking(on: true, at: 30, text: "答案"))
        #expect(on["type"] as? String == "speaking" && on["on"] as? Bool == true && on["text"] as? String == "答案")
        let off = json(MeetingAudioOut.speaking(on: false, at: 31, text: "ignored"))
        #expect(off["on"] as? Bool == false && off["text"] == nil, "an off frame carries no text")
        #expect(json(MeetingAudioOut.role(at: 5, attendance: .off))["attendance"] as? String == "off")
        #expect(json(MeetingAudioOut.end(at: 3733.5))["type"] as? String == "end")
        #expect(json(MeetingAudioOut.seek(at: 312.4))["at"] as? Double == 312.4)
    }

    @Test func gapUploadTimesArePlainDecimals() {
        #expect(MeetingAudioClock.query(0) == "0")
        #expect(MeetingAudioClock.query(312.4) == "312.4")
        #expect(MeetingAudioClock.query(1.0 / 16000) == "0.0000625")
        #expect(MeetingAudioClock.query(36000.25) == "36000.25")
        #expect(MeetingAudioClock.query(1e-9) == "0", "rounded to a sample")
        for s in [0.5, 7.0001, 123456.789] { #expect(!MeetingAudioClock.query(s).contains("e"), "no exponent: the hub takes digits and a dot") }
    }

    @Test func hubFramesReadLeniently() {
        #expect(MeetingAudioIn.decode(#"{"type":"ready","from":312.4,"gaps":[[0,4.2],[9,"x"]],"next":57}"#)
                == .ready(from: 312.4, gaps: [MeetingGap(from: 0, to: 4.2)], next: 57))
        #expect(MeetingAudioIn.decode(#"{"type":"ready","from":0,"gaps":[],"next":0}"#) == .ready(from: 0, gaps: [], next: 0))
        #expect(MeetingAudioIn.decode(#"{"type":"ack","at":330}"#) == .ack(at: 330))
        #expect(MeetingAudioIn.decode(#"{"type":"interim","start":328.1,"text":"我们先"}"#)
                == .interim(MeetingInterim(start: 328.1, text: "我们先")))
        #expect(MeetingAudioIn.decode(#"{"type":"line","n":57,"start":320.2,"end":326,"text":"Let's revisit pricing."}"#)
                == .line(MeetingHubLine(n: 57, start: 320.2, end: 326, text: "Let's revisit pricing.", source: "hub")))
        #expect(MeetingAudioIn.decode(#"{"type":"problem","code":"upstream_failed"}"#) == .problem("upstream_failed"))
        #expect(MeetingAudioIn.decode(#"{"type":"problem","code":null}"#) == .problem(nil))
        #expect(MeetingAudioIn.decode(#"{"type":"error","code":"replaced","fatal":true}"#) == .error(code: "replaced"))
        #expect(MeetingAudioIn.decode(#"{"type":"something_new"}"#) == .other, "an unknown type is ignored")
        #expect(MeetingAudioIn.decode("not json") == .other)
        #expect(MeetingAudioIn.decode(#"{"type":"line","n":-1,"start":0,"end":1,"text":"x"}"#) == .other)
        #expect(MeetingAudioIn.decode(#"{"type":"ready","from":"later"}"#) == .other)
    }

    @Test func pcmIsLittleEndianWholeSamples() {
        #expect(MeetingPCM.sample(2) == Int16.max && MeetingPCM.sample(-2) == -Int16.max && MeetingPCM.sample(.nan) == 0)
        let data = MeetingPCM.data([1, -2, 0x0102][...])
        #expect(data == Data([0x01, 0x00, 0xFE, 0xFF, 0x02, 0x01]))
    }

    @Test func transcriptionViewReadsLiveAndDetailMeta() throws {
        let meta = try JSON.decoder.decode(JSONValue.self, from: Data(#"""
        {"transcription": {"mode": "hub", "state": "finishing", "gaps": [[600, 642.5]], "problem": "upstream_failed", "fallback": true}}
        """#.utf8))
        let view = try #require(MeetingTranscriptionView(meta: meta))
        #expect(view.mode == .hub && view.known == .finishing && view.gaps == [MeetingGap(from: 600, to: 642.5)])
        #expect(view.problem == "upstream_failed" && view.fallback && !view.settled)
        let device = try #require(MeetingTranscriptionView(.object(["mode": .string("device")])))
        #expect(device.mode == .device && device.known == nil && device.gaps.isEmpty)
        #expect(MeetingTranscriptionView(.object(["mode": .string("hub"), "state": .string("final"), "gaps": .array([])]))?.settled == true)
        #expect(MeetingTranscriptionView(.object(["mode": .string("hub"), "state": .string("someday")]))?.known == .live)
        #expect(MeetingTranscriptionView(.object(["state": .string("live")])) == nil, "no mode, no transcription")
    }
}

// MARK: - The 60 seconds in memory

struct MeetingAudioRingTests {
    @Test func nothingGoesBeforeReadyThenWholeFramesFromReadyFrom() throws {
        var ring = MeetingAudioRing()
        ring.append(samples(4000), at: 0)
        #expect(ring.next() == nil, "no audio before ready")
        ring.ready(from: 0)
        var got: [Int16] = []
        while case .audio(let data, let at)? = ring.next() {
            #expect(data.count % 2 == 0 && data.count <= 64 << 10)
            #expect(data.count / 2 <= MeetingPCM.frameSamples, "about 100 ms a frame")
            #expect(at == Int64(got.count))
            got += decoded(data)
        }
        #expect(got == samples(4000))
        #expect(MeetingAudioRing().capacity == 60 * 16000)
    }

    @Test func aFrameNeverPassesTheBinaryLimit() {
        var ring = MeetingAudioRing()
        ring.append(samples(40000), at: 0)
        ring.ready(from: 0)
        guard case .audio(let data, _)? = ring.next(maxSamples: 1_000_000) else { Issue.record("no audio"); return }
        #expect(data.count == 64 << 10)
    }

    @Test func aReconnectResendsFromReadyFrom() {
        var ring = MeetingAudioRing()
        ring.append(samples(16000), at: 0)
        ring.ready(from: 0)
        while ring.next() != nil {}
        ring.stop()
        ring.append(samples(8000, from: 16000), at: 16000)
        #expect(ring.next() == nil, "down: nothing streams")
        // The hub held only the first half second: everything after it again.
        ring.ready(from: 8000)
        var got: [Int16] = [], first: Int64?
        while case .audio(let data, let at)? = ring.next() {
            if first == nil { first = at }
            got += decoded(data)
        }
        #expect(first == 8000 && got == samples(16000, from: 8000))
    }

    @Test func aHubBehindTheRingGetsASeekAndTheRestIsAGap() {
        var ring = MeetingAudioRing(seconds: 1)
        ring.append(samples(48000), at: 0) // 3 s: the ring holds the last one
        #expect(ring.start == 32000 && ring.end == 48000)
        #expect(ring.lostGaps == [MeetingGap(from: 0, to: 2)], "unacknowledged audio that fell out is a gap")
        ring.ready(from: 0)
        #expect(ring.next() == .seek(32000))
        guard case .audio(_, let at)? = ring.next() else { Issue.record("no audio after the seek"); return }
        #expect(at == 32000)
    }

    @Test func acknowledgedAudioThatFallsOutIsNoGap() {
        var ring = MeetingAudioRing(seconds: 1)
        ring.append(samples(16000), at: 0)
        ring.ack(16000)
        ring.append(samples(16000, from: 16000), at: 16000)
        #expect(ring.lostGaps.isEmpty)
        ring.append(samples(16000, from: 32000), at: 32000)
        #expect(ring.lostGaps == [MeetingGap(from: 1, to: 2)])
    }

    /// The iPhone's crash on the tap's thread (#7740): with the ring full, an `ack` inside the second it holds made what
    /// falls out a reversed range, and `..<` trapped. What falls out acknowledged is no gap.
    @Test func anAckInsideTheHeldAudioSurvivesTheRingFilling() {
        var ring = MeetingAudioRing(seconds: 1)
        ring.append(samples(16000), at: 0)
        ring.ready(from: 0)
        while ring.next() != nil {}
        ring.ack(12000)
        ring.append(samples(1600, from: 16000), at: 16000)
        #expect(ring.start == 1600 && ring.end == 17600)
        #expect(ring.lostGaps.isEmpty)
        ring.append(samples(16000, from: 17600), at: 17600)
        #expect(ring.lostGaps == [MeetingGap(from: 0.75, to: 1.1)], "only what fell out past the hub's ack")
    }

    /// The same trap through a reconnect: `ready.from` inside the audio held counts as acknowledged.
    @Test func aReadyInsideTheHeldAudioSurvivesTheRingFilling() {
        var ring = MeetingAudioRing(seconds: 1)
        ring.append(samples(16000), at: 0)
        ring.ready(from: 8000)
        ring.append(samples(1600, from: 16000), at: 16000)
        #expect(ring.start == 1600 && ring.end == 17600)
        #expect(ring.lostGaps.isEmpty)
        #expect(ring.next() == .audio(MeetingPCM.data(samples(1600, from: 8000)[...]), at: 8000))
    }

    @Test func aSendThatFallsBehindSeeksToTheOldestHeld() {
        var ring = MeetingAudioRing(seconds: 1)
        ring.append(samples(1600), at: 0)
        ring.ready(from: 0)
        ring.append(samples(40000, from: 1600), at: 1600)
        #expect(ring.next() == .seek(ring.start))
    }

    @Test func aReadyAheadOfTheRingWaitsForTheAudio() {
        var ring = MeetingAudioRing()
        ring.append(samples(1600), at: 0)
        ring.ready(from: 3200)
        #expect(ring.next() == nil)
        ring.append(samples(3200, from: 1600), at: 1600)
        guard case .audio(_, let at)? = ring.next() else { Issue.record("no audio"); return }
        #expect(at == 3200)
    }

    @Test func aJumpInTheClockIsAGapAndASeek() {
        var ring = MeetingAudioRing()
        ring.append(samples(1600), at: 0)
        ring.ready(from: 0)
        _ = ring.next()
        ring.append(samples(1600), at: 16000)
        #expect(ring.next() == .seek(16000))
        // What it held went unacknowledged (sent, never acked), and the jump was never held: both gaps.
        #expect(ring.lostGaps == [MeetingGap(from: 0, to: 1)])
    }
}

// MARK: - The uplink on its socket

@MainActor struct MeetingAudioUplinkTests {
    private func open(_ uplink: MeetingAudioUplink, speaking: RecordingSpan? = nil) -> FakeVoiceSocket {
        let socket = FakeVoiceSocket(URLRequest(url: URL(string: "wss://hub.invalid/api/v1/recordings/x/audio")!))
        let timeline = MeetingTimeline(roles: [.init(at: 0, attendance: "listener")])
        uplink.open(socket, timeline: timeline, speaking: speaking, role: RecordingRoleChange(at: 0, attendance: .listener)) { _, _ in }
        uplink.drain()
        return socket
    }

    @Test func startGoesFirstThenTheOpenSpanAndTheRoleAndNoAudioBeforeReady() throws {
        let uplink = MeetingAudioUplink(recording: UUID())
        uplink.feed(samples(3200), at: 1600)
        let socket = open(uplink, speaking: RecordingSpan(start: 0.15, end: 0.15, text: "答案"))
        #expect(socket.frames.map { $0["type"] as? String } == ["start", "speaking", "role"])
        #expect(socket.start?["at"] as? Double == 0.1, "the oldest second held")
        #expect(socket.frames[1]["on"] as? Bool == true && socket.frames[1]["text"] as? String == "答案")
        uplink.feed(samples(1600, from: 4800), at: 4800)
        uplink.drain()
        #expect(socket.audio.isEmpty, "no binary frame before ready")
    }

    @Test func readyStreamsFromItsFromAndTheLiveAudioFollows() {
        let uplink = MeetingAudioUplink(recording: UUID())
        uplink.feed(samples(16000), at: 0)
        let socket = open(uplink)
        uplink.ready(from: 0.5, generation: uplink.currentGeneration)
        uplink.drain()
        #expect(decoded(socket.audio.reduce(Data(), +)) == samples(8000, from: 8000))
        uplink.feed(samples(1600, from: 16000), at: 16000)
        uplink.drain()
        #expect(decoded(socket.audio.reduce(Data(), +)) == samples(9600, from: 8000))
    }

    @Test func aNewSocketResendsWhatTheHubLacks() {
        let uplink = MeetingAudioUplink(recording: UUID())
        uplink.feed(samples(16000), at: 0)
        let first = open(uplink)
        uplink.ready(from: 0, generation: uplink.currentGeneration)
        uplink.drain()
        uplink.dropped(generation: uplink.currentGeneration)
        uplink.feed(samples(16000, from: 16000), at: 16000)
        let second = open(uplink)
        #expect(second.start?["at"] as? Double == 0)
        uplink.ready(from: 0.75, generation: uplink.currentGeneration)
        uplink.drain()
        #expect(decoded(second.audio.reduce(Data(), +)) == samples(20000, from: 12000))
        #expect(!first.audio.isEmpty)
    }

    @Test func aStaleReadyChangesNothing() {
        let uplink = MeetingAudioUplink(recording: UUID())
        uplink.feed(samples(1600), at: 0)
        let socket = open(uplink)
        uplink.ready(from: 0, generation: uplink.currentGeneration - 1)
        uplink.drain()
        #expect(socket.audio.isEmpty)
    }

    @Test func endFollowsTheLastAudio() {
        let uplink = MeetingAudioUplink(recording: UUID())
        uplink.feed(samples(3200), at: 0)
        let socket = open(uplink)
        uplink.ready(from: 0, generation: uplink.currentGeneration)
        uplink.feed(samples(1600, from: 3200), at: 3200)
        #expect(uplink.end(settle: 0))
        Thread.sleep(forTimeInterval: 0.05)
        uplink.drain()
        #expect(socket.frames.last?["type"] as? String == "end")
        #expect(socket.frames.last?["at"] as? Double == 0.3)
        #expect(decoded(socket.audio.reduce(Data(), +)).count == 4800)
    }

    /// #3862 phase 5 review: an uplink kept on the tap after Stop never takes the next recording's audio.
    @Test func nothingJoinsTheUplinkAfterItsEnd() {
        let uplink = MeetingAudioUplink(recording: UUID())
        uplink.feed(samples(3200), at: 0)
        let socket = open(uplink)
        uplink.ready(from: 0, generation: uplink.currentGeneration)
        let done = Locked(false)
        #expect(uplink.end(settle: 0) { done.set(true) })
        Thread.sleep(forTimeInterval: 0.05)
        uplink.drain()
        #expect(done.value && uplink.isFinished, "the end says when it went: the time to take it off the tap")
        let sent = socket.audio.count, held = uplink.heldEnd
        // The next recording's clock starts at 0 and soon passes this one's end: none of it joins or goes.
        uplink.feed(samples(16000), at: 0)
        uplink.feed(samples(16000, from: 16000), at: 16000)
        uplink.drain()
        #expect(socket.audio.count == sent && uplink.heldEnd == held)
    }

    /// #3862 phase 5 follow-up: a meeting turned into a Note at a sample sends nothing from that sample on, also a
    /// buffer the tap hands over after the switch, or one that straddles it.
    @Test func nothingFromTheNoteOnGoes() {
        let uplink = MeetingAudioUplink(recording: UUID())
        uplink.feed(samples(3200), at: 0)
        let socket = open(uplink)
        uplink.ready(from: 0, generation: uplink.currentGeneration)
        uplink.drain()
        uplink.cut(at: 4000)
        uplink.feed(samples(1600, from: 3200), at: 3200)
        uplink.feed(samples(1600, from: 4800), at: 4800)
        uplink.drain()
        #expect(decoded(socket.audio.reduce(Data(), +)) == samples(4000), "the meeting's last sample is the one before the Note")
        #expect(uplink.heldEnd == 0.25)
        // Audio the ring held past the cut never goes either.
        let late = MeetingAudioUplink(recording: UUID())
        late.feed(samples(3200), at: 0)
        let other = open(late)
        late.cut(at: 1600)
        late.ready(from: 0, generation: late.currentGeneration)
        late.drain()
        #expect(decoded(other.audio.reduce(Data(), +)) == samples(1600))
    }

    /// The iPhone's crash (#7740): a hub-mode meeting past its first minute, the hub acknowledging every 5 seconds as it
    /// does, on the tap's own path (converted buffers of 4096 frames at 48 kHz). It trapped at the minute.
    @Test func aHubModeMeetingGoesOnPastTheRingsMinute() throws {
        let uplink = MeetingAudioUplink(recording: UUID())
        let socket = open(uplink)
        uplink.ready(from: 0, generation: uplink.currentGeneration)
        let format = try #require(AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 16000, channels: 1, interleaved: false))
        let frames: AVAudioFrameCount = 1365
        let buffer = try #require(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: frames))
        buffer.frameLength = frames
        var fed: Int64 = 0, acked: Int64 = 0
        while fed < 90 * 16000 {
            uplink.feed(buffer, at: MeetingAudioClock.seconds(fed))
            fed += Int64(frames)
            if fed - acked >= 5 * 16000 {
                uplink.drain()
                acked = fed
                uplink.ack(MeetingAudioClock.seconds(acked))
            }
        }
        uplink.drain()
        #expect(uplink.heldEnd == MeetingAudioClock.seconds(fed))
        #expect(uplink.lost.isEmpty, "what fell out was acknowledged")
        #expect(socket.audio.reduce(0) { $0 + $1.count } == Int(fed) * 2, "all of it went, once")
    }
}

/// A value shared with a closure another queue runs (the tests' own).
private final class Locked<Value: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var stored: Value
    init(_ value: Value) { stored = value }
    var value: Value { lock.withLock { stored } }
    func set(_ value: Value) { lock.withLock { stored = value } }
}

// MARK: - Rules

struct MeetingTranscriptionRuleTests {
    typealias Rule = MeetingTranscriptionRule

    @Test func theModeInEffectIsTheHubsWordButNeverHubUnasked() {
        #expect(Rule.decide(nil, asked: nil, answer: .init(mode: "hub")) == nil, "a meeting that named no mode")
        #expect(Rule.decide(nil, asked: .hub, answer: .init(mode: "hub")) == .init(mode: .hub))
        #expect(Rule.decide(nil, asked: .hub, answer: .init(mode: "device", problem: "unavailable"))
                == .init(mode: .device, problem: "unavailable"))
        #expect(Rule.decide(nil, asked: .device, answer: .init(mode: "hub"))?.mode == .device, "audio never leaves unasked")
        #expect(Rule.decide(nil, asked: .hub, answer: .init(mode: "later")) == .init(mode: .device))
    }

    @Test func anOlderManagerKeepsTheMeetingOnThePhoneForGood() {
        let rolled = Rule.decide(.init(mode: .hub), asked: .hub, answer: .init(mode: nil))
        #expect(rolled == .init(mode: .device, problem: nil, olderManager: true))
        #expect(Rule.decide(rolled, asked: .hub, answer: .init(mode: "hub")) == rolled, "a rollback mid-meeting is for the rest of it")
    }

    @Test func errorsAreActedOnByTheirCode() {
        #expect(Rule.reaction(toError: "recording_not_found") == .uploadFirst)
        #expect(Rule.reaction(toError: "bad_frame") == .retry)
        #expect(Rule.reaction(toError: "replaced") == .yield, "another socket has the meeting: hub transcription goes on")
        for code in ["audio_full", "recording_deleted", "kb_read_only", "not_hub", "transcript_expired",
                     "transcription_final", "a_code_from_tomorrow", ""] {
            #expect(Rule.reaction(toError: code) == .stop, "\(code)")
        }
        #expect(Rule.reaction(toClose: 403) == .stop && Rule.reaction(toClose: 404) == .stop)
        #expect(Rule.reaction(toClose: nil) == .retry && Rule.reaction(toClose: 503) == .retry)
        #expect((1...8).map(Rule.backoff) == [1, 2, 4, 8, 16, 30, 30, 30])
    }

    @Test func cellularIsAllowedUnlessWiFiOnly() {
        #expect(Rule.mayStream(wifiOnly: false, network: .metered))
        #expect(!Rule.mayStream(wifiOnly: true, network: .metered))
        #expect(Rule.mayStream(wifiOnly: true, network: .unmetered))
        #expect(!Rule.mayStream(wifiOnly: false, network: .none))
        #expect(!Rule.mayStream(wifiOnly: true, network: .unknown), "a network not known yet is not Wi-Fi")
        #expect(Rule.mayStream(wifiOnly: false, network: .unknown), "cellular is allowed without the switch")
    }

    /// #3862 phase 5 review: with Wi-Fi Only the system itself keeps the socket and the gap uploads off cellular, and the
    /// network counts as not Wi-Fi until the monitor first answers.
    @MainActor @Test func wifiOnlyIsTheSystemsRuleAndTheNetworkStartsUnknown() throws {
        #expect(MeetingNetwork().kind == .unknown)
        let api = APIClient(config: ServerConfig(url: URL(string: "https://hub.invalid")!, token: "t"))
        let kept = MeetingAudioEndpoint.request(api, recording: UUID(), wifiOnly: true)
        #expect(!kept.allowsCellularAccess && !kept.allowsExpensiveNetworkAccess)
        #expect(kept.value(forHTTPHeaderField: "Authorization") == "Bearer t" && kept.url?.scheme == "wss")
        let free = MeetingAudioEndpoint.request(api, recording: UUID(), wifiOnly: false)
        #expect(free.allowsCellularAccess && free.allowsExpensiveNetworkAccess, "cellular is allowed without the switch")
        var put = URLRequest(url: try #require(URL(string: "https://hub.invalid/api/v1/recordings/x/audio?at=0")))
        put.keepOnWiFi(true)
        #expect(!put.allowsCellularAccess && !put.allowsExpensiveNetworkAccess)
    }

    /// #3862 phase 5 follow-up: through a tunnel the system's guard covers only the hop to its loopback address, and the
    /// tunnel's own connection stays on the path it was made on. With Wi-Fi Only, audio goes through one only while the
    /// phone is on Wi-Fi and has been off cellular since the tunnel began.
    @Test func throughATunnelWiFiOnlyAsksForWiFiSinceTheTunnelBegan() {
        let opened = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(Rule.mayStream(wifiOnly: true, network: .unmetered, tunnel: nil, meteredUntil: opened.addingTimeInterval(60)),
                "straight to the hub: the system's own guard holds")
        #expect(Rule.mayStream(wifiOnly: true, network: .unmetered, tunnel: opened, meteredUntil: nil), "never on cellular")
        #expect(Rule.mayStream(wifiOnly: true, network: .unmetered, tunnel: opened, meteredUntil: opened.addingTimeInterval(-60)),
                "on cellular only before the tunnel began")
        #expect(!Rule.mayStream(wifiOnly: true, network: .unmetered, tunnel: opened, meteredUntil: opened.addingTimeInterval(60)),
                "the tunnel began on cellular, then the phone joined Wi-Fi: it still runs over cellular")
        #expect(!Rule.mayStream(wifiOnly: true, network: .metered, tunnel: opened, meteredUntil: nil))
        #expect(!Rule.mayStream(wifiOnly: true, network: .unknown, tunnel: opened, meteredUntil: nil))
        #expect(Rule.mayStream(wifiOnly: false, network: .metered, tunnel: opened, meteredUntil: .now), "cellular is allowed without the switch")
        #expect(!Rule.mayStream(wifiOnly: false, network: .none, tunnel: opened, meteredUntil: nil))
    }

    /// The path's last moment on cellular: now while on it, the moment it left once off it.
    @Test func thePathRemembersWhenItLeftCellular() throws {
        let state = MeetingNetworkState()
        #expect(state.kind == .unknown && state.meteredUntil == nil)
        let start = Date(timeIntervalSince1970: 1_700_000_000)
        state.set(.unmetered, at: start)
        #expect(state.meteredUntil == nil)
        state.set(.metered, at: start.addingTimeInterval(10))
        #expect(try #require(state.meteredUntil) > start.addingTimeInterval(10), "now, while on cellular")
        state.set(.none, at: start.addingTimeInterval(20))
        state.set(.unmetered, at: start.addingTimeInterval(30))
        #expect(state.kind == .unmetered && state.meteredUntil == start.addingTimeInterval(20))
    }

    @Test func thePageShowsTheHubWhileItIsUpAndHealthy() {
        func shown(_ hub: Bool = true, socket: MeetingAudioSocket = .ready, everUp: Bool = true, away: Bool = false,
                   problem: String? = nil, stopped: Bool = false, role: MeetingAttendance = .listener) -> Rule.Shown {
            Rule.shown(hub: hub, socket: socket, everUp: everUp, away: away, problem: problem, stopped: stopped, role: role)
        }
        #expect(shown(false) == .init(hubInterim: false, hubLines: false, phoneLines: true, fellBack: false))
        #expect(shown() == .init(hubInterim: true, hubLines: true, phoneLines: false, fellBack: false))
        #expect(shown(problem: "no_interim").hubLines && !shown(problem: "no_interim").fellBack)
        #expect(shown(problem: "upstream_failed") == .init(hubInterim: false, hubLines: false, phoneLines: true, fellBack: true))
        #expect(shown(problem: "a_new_problem").fellBack)
        #expect(shown(role: .off) == .init(hubInterim: true, hubLines: false, phoneLines: true, fellBack: false),
                "role off: the hub's caption and the phone's own lines")
        #expect(shown(stopped: true).fellBack && shown(stopped: true).phoneLines)
        #expect(!shown(socket: .connecting, everUp: false).fellBack, "the first connect is no fallback yet")
        #expect(shown(socket: .down, everUp: false).fellBack, "never up and down: fallen back")
        #expect(shown(socket: .waitingForWiFi).fellBack && shown(socket: .waitingForWiFi, everUp: false).fellBack)
    }

    /// #3862 phase 5 review: pausing, the end after Stop and a brief reconnect are no fallback; a socket away longer is.
    @Test func aPauseAnEndOrABriefReconnectIsNoFallback() {
        func fellBack(_ socket: MeetingAudioSocket, away: Bool = false, problem: String? = nil, stopped: Bool = false) -> Bool {
            Rule.shown(hub: true, socket: socket, everUp: true, away: away, problem: problem, stopped: stopped, role: .listener).fellBack
        }
        for socket in [MeetingAudioSocket.paused, .ending, .ended] {
            #expect(!fellBack(socket), "\(socket): nothing is being transcribed")
            #expect(!fellBack(socket, problem: "upstream_failed"), "\(socket): a problem from before says nothing now")
            #expect(fellBack(socket, stopped: true), "\(socket): a stopped uplink is said")
            #expect(!Rule.shown(hub: true, socket: socket, everUp: true, away: false, problem: nil, stopped: false, role: .listener).hubLines,
                    "the hub's lines while the socket is up only, as the contract says")
        }
        #expect(!fellBack(.connecting) && !fellBack(.down), "a reconnect within the grace")
        #expect(fellBack(.connecting, away: true) && fellBack(.down, away: true), "away longer than the grace")
        #expect(Rule.reconnectGrace == .seconds(5))
    }

    @Test func aStartAsksForTheDefaultOrThePickWhileTheHubIsReady() {
        #expect(MeetingAttendanceDefaults.startMode(listed: false, hubDefault: .hub, ready: true) == nil)
        #expect(MeetingAttendanceDefaults.startMode(listed: true, hubDefault: .hub, ready: true) == .hub)
        #expect(MeetingAttendanceDefaults.startMode(listed: true, hubDefault: .hub, ready: false) == .device)
        #expect(MeetingAttendanceDefaults.startMode(listed: true, hubDefault: nil, ready: true) == .device)
        #expect(MeetingAttendanceDefaults.startMode(listed: true, hubDefault: .device, ready: true, picked: .hub) == .hub)
        #expect(MeetingAttendanceDefaults.startMode(listed: true, hubDefault: .hub, ready: false, picked: .hub) == .device)
        #expect(MeetingAttendanceDefaults.startMode(listed: true, hubDefault: .hub, ready: true, picked: .device) == .device)
    }

    @Test func theFlagComesOnlyBesideTheConversationAndTheLiveView() {
        let store = UserDefaults(suiteName: "transcription-\(UUID())")!
        MeetingAttendanceDefaults.remember(hello: ["meeting_attendance", "meeting_conversation", "meeting_transcription"], role: nil, store)
        #expect(!store.bool(forKey: MeetingAttendanceDefaults.transcriptionKey), "not without meeting_live_view")
        MeetingAttendanceDefaults.remember(hello: ["meeting_attendance", "meeting_conversation", "meeting_live_view",
                                                   "meeting_transcription"], role: nil, store)
        #expect(store.bool(forKey: MeetingAttendanceDefaults.transcriptionKey))
    }
}

// MARK: - The timeline and the upload

struct MeetingTimelineTests {
    @Test func theTimelineStaysWithinTheHubsLimits() {
        let roles = (0..<150).map { RecordingRoleChange(at: Double($0), attendance: $0 % 2 == 0 ? .listener : .off) }
        let spans = (0..<1200).map { RecordingSpan(start: Double($0) * 2, end: Double($0) * 2 + 1, text: "x") }
        let t = MeetingTimeline(roles: roles, spoken: spans)
        #expect(t.roles.count == 100 && t.roles.first?.at == 0 && t.roles.last?.at == 99, "the first 100 role changes")
        #expect(t.speaking.count == 1000 && t.speaking.last?.start == 1998, "the first 1000 spans")
    }

    @Test func malformedEntriesAreLeftOutAndTextsCutOnACharacter() throws {
        let long = String(repeating: "会", count: 1500) // 4500 bytes
        let t = MeetingTimeline(roles: [RecordingRoleChange(at: -1, attendance: .listener), RecordingRoleChange(at: 2, attendance: .participant)],
                                spoken: [RecordingSpan(start: 9, end: 3, text: "backwards"), RecordingSpan(start: 1, end: 2, text: long),
                                         RecordingSpan(start: 0, end: 0.5, text: nil), RecordingSpan(start: .nan, end: 1)])
        #expect(t.roles == [.init(at: 2, attendance: "participant")])
        #expect(t.speaking.count == 2 && t.speaking[0].start == 0 && t.speaking[0].text == nil)
        let cut = try #require(t.speaking[1].text)
        #expect(cut.utf8.count == 3999 && cut.allSatisfy { $0 == "会" })
        let encoded = json(String(decoding: try JSONEncoder().encode(t), as: UTF8.self))
        #expect((encoded["speaking"] as? [[String: Any]])?.first?.keys.sorted() == ["end", "start"], "no text: none sent")
    }

    @Test func aMeetingThatAskedForAModeKeepsItsRoleChanges() {
        var a = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, transcription: .hub, at: 0)
        #expect(a.roles == [RecordingRoleChange(at: 0, attendance: .listener)])
        a.set(.off, at: 30)
        a.set(.off, at: 31)
        a.set(.participant, at: 60)
        #expect(a.roles.map(\.attendance) == [.listener, .off, .participant] && a.roles.map(\.at) == [0, 30, 60])
        var plain = RecordingAttendance.room(.listener, private: false, photos: false)
        plain.set(.off, at: 30)
        #expect(plain.roles.isEmpty, "a meeting that named no mode keeps none")
    }

    @MainActor @Test func aNoteTurnedIntoAMeetingStartsItsRolesThere() {
        let start = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, transcription: .hub)
        let a = MeetingAttendees.resumed(nil, start: start, at: 30)
        #expect(a.transcription == .hub && a.roles == [RecordingRoleChange(at: 30, attendance: .listener)])
        #expect(a.hubTime == [RecordingPeriod(start: 30)], "hub-mode time from the switch, not from the recording's start")
    }

    @Test func theAttendanceRoundTripsAndAnUnknownModeNamesNone() throws {
        var a = RecordingAttendance.room(.participant, private: false, photos: false, liveView: true, transcription: .hub)
        a.rollBackHub(at: 42)
        #expect(a.hubTime == [RecordingPeriod(start: 0, end: 42)] && a.hubRolledBack)
        #expect(try JSON.decoder.decode(RecordingAttendance.self, from: JSON.encoder.encode(a)) == a)
        let later = try JSON.decoder.decode(RecordingAttendance.self, from: Data(#"{"role":"listener","transcription":"quantum"}"#.utf8))
        #expect(later.transcription == nil)
        let before = try JSON.decoder.decode(RecordingAttendance.self, from: Data(#"{"role":"listener","transcription":"hub"}"#.utf8))
        #expect(before.hubTime.isEmpty && !before.hubRolledBack, "none read: no hub-mode time, so nothing goes up")
    }

    // MARK: Hub-mode time (#3862 phase 5 review)

    private func recording(_ attendance: RecordingAttendance?, seconds: Double, meeting: Bool = true) -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: Date(timeIntervalSince1970: 1_800_000_000), source: .phone, title: "Weekly", active: true)
        e.meeting = meeting ? RecordingMeeting(title: "Weekly") : nil
        e.attendance = attendance
        e.segments = [RecordingSegment(index: 0, start: 0, duration: seconds, sentences: [])]
        return e
    }

    private let hubStart = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, transcription: .hub)

    @MainActor @Test func aNoteTurnedIntoAHubMeetingNeverUploadsTheNote() {
        let note = recording(nil, seconds: 900, meeting: false)
        let e = MeetingRecorder.switched(note, meeting: true, at: 600, named: nil, start: hubStart)
        #expect(e.attendance?.transcription == .hub && e.attendance?.hubTime == [RecordingPeriod(start: 600)])
        #expect(MeetingTranscription.uploadable(e) == [MeetingGap(from: 600, to: 900)])
        #expect(MeetingGapUpload.plan([MeetingGap(from: 0, to: 600)], held: e.duration, within: MeetingTranscription.uploadable(e)).isEmpty,
                "the hub lacks the Note's ten minutes: they stay on the phone")
    }

    @MainActor @Test func aNoteBetweenTwoHubStretchesStaysOnThePhone() {
        var e = recording(hubStart, seconds: 900)
        #expect(e.attendance?.hubTime == [RecordingPeriod(start: 0)])
        e = MeetingRecorder.switched(e, meeting: false, at: 300, named: nil, start: RecordingAttendance(role: .off))
        #expect(e.attendance?.hubTime == [RecordingPeriod(start: 0, end: 300)], "a Note ends hub-mode time")
        e = MeetingRecorder.switched(e, meeting: true, at: 500, named: nil, start: hubStart)
        #expect(e.attendance?.hubTime == [RecordingPeriod(start: 0, end: 300), RecordingPeriod(start: 500)])
        let allowed = MeetingTranscription.uploadable(e)
        #expect(allowed == [MeetingGap(from: 0, to: 300), MeetingGap(from: 500, to: 900)])
        let pieces = MeetingGapUpload.plan([MeetingGap(from: 0, to: 900)], held: e.duration, within: allowed)
        #expect(!pieces.contains { $0.at < 500 && $0.at + $0.seconds > 300 }, "nothing of the Note between")
        e.active = false
        var a = e.attendance
        a?.closeHubTime(at: 900)
        #expect(a?.hubTime.last == RecordingPeriod(start: 500, end: 900), "Stop ends it")
    }

    @Test func aRolledBackMeetingNeverNeedsGaps() {
        var e = recording(hubStart, seconds: 900)
        e.active = false
        e.expectedSegments = 1
        #expect(MeetingTranscription.needsGaps(e, meeting: nil, expired: false, synced: true, done: false), "before the rollback")
        var a = hubStart
        a.rollBackHub(at: 120)
        e.attendance = a
        #expect(!MeetingTranscription.needsGaps(e, meeting: nil, expired: false, synced: true, done: false))
        #expect(MeetingTranscription.uploadable(e).isEmpty)
        a.openHubTime(at: 300)
        #expect(a.hubTime == [RecordingPeriod(start: 0, end: 120)], "a rollback is for the rest of the recording")
        var none = hubStart
        none.hubTime = []
        e.attendance = none
        #expect(!MeetingTranscription.needsGaps(e, meeting: nil, expired: false, synced: true, done: false), "no hub-mode time: nothing")
    }

    /// #3862 phase 5 follow-up: hub transcription stopped (a fatal error, an unknown code, a refused socket) ends hub-mode
    /// time there for good: nothing after it goes up, a Note turned back into a meeting opens none, and it is kept.
    @MainActor @Test func aStoppedMeetingKeepsItsAudioFromTheStopOn() throws {
        var e = recording(hubStart, seconds: 900)
        var a = hubStart
        a.stopHub(at: 120, code: "a_code_from_tomorrow")
        a.stopHub(at: 300, code: "later")
        #expect(a.hubStopped == RecordingHubStop(at: 120, code: "a_code_from_tomorrow"), "the first stop is kept")
        #expect(a.hubTime == [RecordingPeriod(start: 0, end: 120)])
        e.attendance = a
        e.active = false
        e.expectedSegments = 1
        #expect(MeetingTranscription.needsGaps(e, meeting: nil, expired: false, synced: true, done: false), "what came before may go")
        #expect(MeetingTranscription.uploadable(e) == [MeetingGap(from: 0, to: 120)])
        e = MeetingRecorder.switched(e, meeting: false, at: 400, named: nil, start: RecordingAttendance(role: .off))
        e = MeetingRecorder.switched(e, meeting: true, at: 500, named: nil, start: hubStart)
        #expect(e.attendance?.hubTime == [RecordingPeriod(start: 0, end: 120)], "a meeting again opens no hub-mode time")
        var open = hubStart
        open.hubTime = [RecordingPeriod(start: 0)]
        open.hubStopped = RecordingHubStop(at: 200, code: "http_403")
        e.attendance = open
        #expect(MeetingTranscription.uploadable(e) == [MeetingGap(from: 0, to: 200)], "clipped at the stop whatever the stretches say")
        let back = try JSON.decoder.decode(RecordingAttendance.self, from: JSON.encoder.encode(a))
        #expect(back.hubStopped == a.hubStopped)
        let before = try JSON.decoder.decode(RecordingAttendance.self, from: Data(#"{"role":"listener","transcription":"hub"}"#.utf8))
        #expect(before.hubStopped == nil)
    }

    private func meeting(_ attendance: RecordingAttendance) -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: Date(timeIntervalSince1970: 1_800_000_000), source: .phone, title: "Weekly", active: true)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.attendance = attendance
        return e
    }

    private func upload(_ e: RecordingEntry) throws -> [String: Any] {
        try JSONSerialization.jsonObject(with: JSON.encoder.encode(RecordingUpload(e))) as? [String: Any] ?? [:]
    }

    @Test func aHubModeUploadCarriesTheModeAndTheTimeline() throws {
        var a = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, transcription: .hub)
        a.spoken = [RecordingSpan(start: 4, end: 9, text: "答案")]
        let body = try upload(meeting(a))
        #expect(body["transcription"] as? String == "hub")
        let timeline = try #require(body["timeline"] as? [String: Any])
        #expect((timeline["roles"] as? [[String: Any]])?.count == 1)
        #expect((timeline["speaking"] as? [[String: Any]])?.first?["text"] as? String == "答案")
    }

    @Test func aDeviceModeUploadNamesItsModeOnlyAndOneWithoutTheFlagNeither() throws {
        let device = try upload(meeting(.room(.listener, private: false, photos: false, liveView: true, transcription: .device)))
        #expect(device["transcription"] as? String == "device" && device["timeline"] == nil)
        let none = try upload(meeting(.room(.listener, private: false, photos: false, liveView: true)))
        #expect(none["transcription"] == nil && none["timeline"] == nil)
        var note = meeting(.room(.listener, private: false, photos: false, liveView: true, transcription: .hub))
        note.meeting = nil
        #expect(try upload(note)["transcription"] == nil, "a note names no mode")
    }
}

// MARK: - Gap uploads

struct MeetingGapUploadTests {
    @Test func gapsBecomePiecesOfAtMostFiveMinutesWithinThePhonesAudio() {
        let all = [MeetingGap(from: 0, to: 950)]
        let plan = MeetingGapUpload.plan([MeetingGap(from: 0, to: 700), MeetingGap(from: 690, to: 720), MeetingGap(from: 900, to: 1000)],
                                         held: 950, within: all)
        #expect(plan == [.init(at: 0, seconds: 300), .init(at: 300, seconds: 300), .init(at: 600, seconds: 120), .init(at: 900, seconds: 50)])
        #expect(MeetingGapUpload.plan([MeetingGap(from: 1000, to: 1100)], held: 950, within: [MeetingGap(from: 0, to: 2000)]).isEmpty,
                "past the phone's audio")
        #expect(300 * 16000 * 2 <= 10 << 20, "a piece fits a request")
    }

    /// #3862 phase 5 review: only audio recorded while the recording was a hub-mode meeting goes up; nothing without any.
    @Test func gapsAreClippedToHubModeTime() {
        let hubTime = [MeetingGap(from: 0, to: 300), MeetingGap(from: 500, to: 900)]
        #expect(MeetingGapUpload.plan([MeetingGap(from: 300, to: 500)], held: 900, within: hubTime).isEmpty, "the Note between")
        #expect(MeetingGapUpload.plan([MeetingGap(from: 200, to: 600)], held: 900, within: hubTime)
                == [.init(at: 200, seconds: 100), .init(at: 500, seconds: 100)])
        let whole: [MeetingGapUpload.Piece] = MeetingGapUpload.plan([MeetingGap(from: 0, to: 900)], held: 900, within: hubTime)
        let seconds: Double = whole.reduce(0) { $0 + $1.seconds }
        #expect(seconds == 700)
        #expect(MeetingGapUpload.plan([MeetingGap(from: 0, to: 900)], held: 900, within: []).isEmpty, "no hub-mode time: nothing")
        #expect(MeetingGapUpload.intersection([MeetingGap(from: 0, to: 10), MeetingGap(from: 20, to: 30)], [MeetingGap(from: 5, to: 25)])
                == [MeetingGap(from: 5, to: 10), MeetingGap(from: 20, to: 25)])
    }

    /// #3862 phase 5 review: a pass left `later` is tried again, sooner first, before the hub's final pass (30 minutes).
    @Test func aGapPassLeftLaterIsTriedAgainWithBackoff() {
        let delays: [Double] = (1...8).map { MeetingGapUpload.retryDelay($0) }
        #expect(delays == [15, 30, 60, 120, 240, 300, 300, 300])
        let firstSeven: Double = delays.prefix(7).reduce(0, +)
        #expect(firstSeven < 30 * 60, "several tries before the final pass")
    }

    @Test func refusalsInTheContractsOrderEachHaveTheirAnswer() {
        typealias R = MeetingGapUpload
        #expect(R.reaction(status: 409, code: "transcription_final") == .done)
        #expect(R.reaction(status: 409, code: "not_hub") == .done)
        #expect(R.reaction(status: 409, code: "transcript_expired") == .done)
        #expect(R.reaction(status: 410, code: "recording_deleted") == .done)
        #expect(R.reaction(status: 413, code: "audio_full") == .done)
        #expect(R.reaction(status: 413, code: "too_large") == .smaller)
        #expect(R.reaction(status: 404, code: "recording_not_found") == .uploadFirst)
        #expect(R.reaction(status: 403, code: "kb_read_only") == .later)
        #expect(R.reaction(status: 403, code: "forbidden") == .done)
        #expect(R.reaction(status: 503, code: "assistant_offline") == .later)
        #expect(R.reaction(status: 400, code: "bad_audio") == .done)
    }

    private func ended(_ transcription: MeetingTranscriptionMode?) -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: .now, source: .phone, active: false, expectedSegments: 1)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.attendance = .room(.listener, private: false, photos: false, liveView: true, transcription: transcription)
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 60, sentences: [])]
        return e
    }

    @Test func gapsAreUploadedWithinTheRetentionUntilTheHubIsDone() {
        let e = ended(.hub)
        typealias T = MeetingTranscription
        #expect(T.needsGaps(e, meeting: nil, expired: false, synced: true, done: false))
        #expect(!T.needsGaps(e, meeting: nil, expired: true, synced: true, done: false), "never past `days`")
        #expect(!T.needsGaps(e, meeting: nil, expired: false, synced: false, done: false), "after the complete upload")
        #expect(!T.needsGaps(e, meeting: nil, expired: false, synced: true, done: true))
        var final = MeetingTranscription.Meeting()
        final.hub = MeetingTranscriptionView(mode: .hub, state: "final")
        #expect(!T.needsGaps(e, meeting: final, expired: false, synced: true, done: false), "the hub is done with it")
        var device = MeetingTranscription.Meeting()
        device.decided = .init(mode: .device)
        #expect(!T.needsGaps(e, meeting: device, expired: false, synced: true, done: false))
        #expect(!T.needsGaps(ended(.device), meeting: nil, expired: false, synced: true, done: false))
        var running = e
        running.active = true
        #expect(!T.needsGaps(running, meeting: nil, expired: false, synced: true, done: false))
    }
}

// MARK: - The store

@MainActor struct MeetingTranscriptionStoreTests {
    @MainActor private final class Rig {
        let hub = FakeHub()
        let library = RecordingLibrary(root: FileManager.default.temporaryDirectory.appending(path: "meeting-transcription-\(UUID())"))
        var sockets: [FakeVoiceSocket] = []
        var network: MeetingNetworkKind = .unmetered
        var listening = false
        var reuploads: [UUID] = []
        var audio: (at: Double, data: Data)? = (0, Data(repeating: 0, count: 64000))
        let store = UserDefaults(suiteName: "transcription-store-\(UUID())")!
        /// Every wait asked for, in order.
        var waits: [Duration] = []
        /// Waits of 5 seconds or more never end while a test runs (the reconnect grace, the 20 seconds after `end`, a
        /// healthy socket's 30, a gap retry), unless listed here; shorter ones (a retry's second or two) pass at once.
        var passing: Set<Duration> = []
        /// Where the recording is now (`Deps.position`).
        var position: Double?
        var linesAdded: [UUID] = []
        /// Background time begun and ended for gap passes, and how the system would take the latest back.
        var backgroundBegun = 0
        var backgroundEnded = 0
        var expire: (@MainActor @Sendable () -> Void)?
        /// Waits of these lengths are held until they are listed in `passing` (a test lets them go halfway).
        var gated: Set<Duration> = []
        /// Every gap `PUT` begun, by its `wifiOnly`; while `holdPuts`, each waits until it is stopped (the PUT on its way).
        var puts: [Bool] = []
        var holdPuts = false
        var putsStopped = 0
        /// The tunnel the hub's requests go through, since when (`Deps.tunnel`), and the last moment the path was on
        /// cellular (`Deps.meteredUntil`).
        var tunnel: Date?
        var meteredUntil: Date?
    }

    private func make(_ rig: Rig) -> MeetingTranscription {
        let client = rig.hub.client
        let deps = MeetingTranscription.Deps(
            api: { client },
            socket: { request, _ in
                let socket = FakeVoiceSocket(request)
                rig.sockets.append(socket)
                return socket
            },
            network: { rig.network },
            listen: { sink in rig.listening = sink != nil },
            reupload: { rig.reuploads.append($0) },
            wait: { @MainActor duration in
                rig.waits.append(duration)
                if rig.gated.contains(duration) {
                    while !rig.passing.contains(duration), !Task.isCancelled { try? await Task.sleep(for: .milliseconds(10)) }
                    return
                }
                if duration >= .seconds(5), !rig.passing.contains(duration) { try? await Task.sleep(for: .seconds(600)) }
            },
            store: rig.store,
            audio: { _, piece in rig.audio.map { (piece.at, $0.data) } },
            position: { _ in rig.position },
            linesAdded: { rig.linesAdded.append($0) },
            background: { _, expired in
                rig.backgroundBegun += 1
                rig.expire = expired
                return { rig.backgroundEnded += 1 }
            },
            put: { @MainActor api, path, query, data, wifiOnly in
                rig.puts.append(wifiOnly)
                if rig.holdPuts {
                    do { try await Task.sleep(for: .seconds(600)) } catch {
                        rig.putsStopped += 1
                        throw error
                    }
                }
                return try await api.putBytes(path, query: query, data: data, wifiOnly: wifiOnly)
            },
            tunnel: { rig.tunnel },
            meteredUntil: { rig.meteredUntil })
        return MeetingTranscription(deps: deps, library: rig.library)
    }

    /// A meeting that ended and whose segments cover `seconds`, saved.
    private func ended(_ rig: Rig, seconds: Double = 60, attendance: RecordingAttendance? = nil) throws -> RecordingEntry {
        var e = try meeting(rig)
        if let attendance { e.attendance = attendance }
        e.active = false
        e.expectedSegments = 1
        e.segments = [RecordingSegment(index: 0, start: 0, duration: seconds, sentences: [])]
        try rig.library.save(e)
        return e
    }

    private func meeting(_ rig: Rig, mode: MeetingTranscriptionMode? = .hub, role: MeetingAttendance = .listener) throws -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: .now, source: .phone, title: "Weekly", active: true)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.attendance = .room(role, private: false, photos: false, liveView: true, transcription: mode)
        try rig.library.save(e)
        return e
    }

    private func until(_ what: String, _ done: () -> Bool, sourceLocation: SourceLocation = #_sourceLocation) async {
        for _ in 0..<300 {
            if done() { return }
            try? await Task.sleep(for: .milliseconds(10))
        }
        #expect(done(), "timed out waiting: \(what)", sourceLocation: sourceLocation)
    }

    @Test func aHubMeetingKeepsItsAudioUntilTheModeIsInThenOpensTheSocket() throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.follow(e.id, paused: false, entry: e)
        #expect(rig.listening, "the tap's audio is kept from the start")
        #expect(t.meeting(e.id).socket == .waitingForMode && rig.sockets.isEmpty)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        #expect(socket.request.url?.absoluteString.hasSuffix("/api/v1/recordings/\(e.id.uuidString.lowercased())/audio") == true)
        #expect(socket.request.url?.scheme == "wss" && socket.request.value(forHTTPHeaderField: "Authorization") == "Bearer t")
        #expect(socket.request.allowsCellularAccess, "cellular is allowed without Wi-Fi Only")
        #expect(t.meeting(e.id).socket == .connecting && t.hubTranscribes(e.id))
        let shown = t.shown(e.id, role: .listener)
        #expect(!shown.fellBack && shown.phoneLines, "connecting: the phone's lines, no notice yet")
    }

    @Test func theHubsLinesAndProblemsReachTheStore() async throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        socket.deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        #expect(t.meeting(e.id).socket == .ready)
        socket.deliver(#"{"type":"interim","start":0,"text":"我们先把"}"#)
        #expect(t.meeting(e.id).interim?.text == "我们先把")
        socket.deliver(#"{"type":"line","n":0,"start":0,"end":4,"text":"我们先把 export cap 保持在 4 MB。"}"#)
        #expect(t.meeting(e.id).lines.map(\.n) == [0] && t.meeting(e.id).interim == nil)
        #expect(t.shown(e.id, role: .listener) == .init(hubInterim: true, hubLines: true, phoneLines: false, fellBack: false))
        socket.deliver(#"{"type":"problem","code":"upstream_failed"}"#)
        #expect(t.shown(e.id, role: .listener) == .init(hubInterim: false, hubLines: false, phoneLines: true, fellBack: true))
        socket.deliver(#"{"type":"problem","code":null}"#)
        #expect(t.shown(e.id, role: .listener).hubLines)
        socket.deliver(#"{"type":"ack","at":5}"#)
        #expect(t.meeting(e.id).acked == 5)
    }

    @Test func linesMissedWhileAwayAreReadFromTheLiveLog() async throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        let path = "/api/v1/recordings/\(e.id.uuidString.lowercased())/live"
        rig.hub.on("GET", path) { req in
            #expect(req.query["parts"] == "lines" && req.query["from"] == "0")
            return Data(#"{"lines":[{"n":0,"start":0,"end":4,"text":"a","source":"hub"},{"n":1,"start":4,"end":8,"text":"b","source":"device"}],"more":false}"#.utf8)
        }
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        try #require(rig.sockets.last).deliver(#"{"type":"ready","from":8,"gaps":[[0,2]],"next":2}"#)
        await until("the missed lines") { t.meeting(e.id).lines.count == 2 }
        #expect(t.meeting(e.id).lines.map(\.source) == ["hub", "device"])
        #expect(t.meeting(e.id).gaps == [MeetingGap(from: 0, to: 2)])
    }

    @Test func anUnknownErrorStopsHubTranscriptionForTheMeeting() throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        socket.deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        socket.deliver(#"{"type":"error","code":"quota_exceeded_somewhere","fatal":true}"#)
        #expect(t.meeting(e.id).stopped == "quota_exceeded_somewhere" && t.meeting(e.id).socket == .off)
        #expect(!rig.listening && socket.closed)
        let shown = t.shown(e.id, role: .listener)
        #expect(shown.phoneLines && shown.fellBack, "the phone's own lines, with the notice")
        t.follow(e.id, paused: false, entry: e)
        #expect(rig.sockets.count == 1, "no other socket for this meeting")
    }

    @Test func aDropIsTriedAgainAndANotFoundUploadsFirst() async throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        try #require(rig.sockets.last).deliver(#"{"type":"error","code":"recording_not_found","fatal":true}"#)
        #expect(rig.reuploads == [e.id])
        await until("another socket") { rig.sockets.count == 2 }
        rig.sockets.last?.drop()
        await until("a third socket") { rig.sockets.count == 3 }
        #expect(t.meeting(e.id).stopped == nil)
        rig.sockets.last?.drop(status: 403)
        #expect(t.meeting(e.id).stopped == "http_403")
        #expect(rig.library.entry(e.id)?.attendance?.hubStopped?.code == "http_403", "a refused socket stops it for good")
    }

    @Test func anOlderManagerOpensNoSocketAndStopsARunningUplink() throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        #expect(rig.sockets.count == 1)
        t.uploaded(e, answer: .init(mode: nil))
        t.follow(e.id, paused: false, entry: e)
        #expect(t.meeting(e.id).socket == .off && !rig.listening && rig.sockets.last?.closed == true)
        #expect(!t.hubTranscribes(e.id) && t.meeting(e.id).decided?.olderManager == true)
    }

    @Test func aDeviceMeetingNeverListensOrOpensASocket() throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig, mode: .device)
        t.uploaded(e, answer: .init(mode: "device"))
        t.follow(e.id, paused: false, entry: e)
        #expect(!rig.listening && rig.sockets.isEmpty)
        let unavailable = try meeting(rig)
        t.uploaded(unavailable, answer: .init(mode: "device", problem: "unavailable"))
        t.follow(unavailable.id, paused: false, entry: unavailable)
        #expect(rig.sockets.isEmpty && !rig.listening && t.meeting(unavailable.id).decided?.problem == "unavailable")
    }

    @Test func wifiOnlyKeepsTheAudioOnThePhoneOnCellular() throws {
        let rig = Rig()
        rig.store.set(true, forKey: MeetingAttendanceDefaults.wifiOnlyKey)
        rig.network = .metered
        let t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        #expect(t.meeting(e.id).socket == .waitingForWiFi && rig.sockets.isEmpty && rig.listening)
        #expect(t.shown(e.id, role: .listener).fellBack, "the socket counts as down")
        rig.network = .unmetered
        t.follow(e.id, paused: false, entry: e)
        #expect(rig.sockets.count == 1 && t.meeting(e.id).socket == .connecting)
        let kept = try #require(rig.sockets.last).request
        #expect(!kept.allowsCellularAccess && !kept.allowsExpensiveNetworkAccess, "the system keeps it off cellular too")
        t.wifiOnlyChanged(false)
        rig.network = .metered
        t.follow(e.id, paused: false, entry: e)
        #expect(rig.sockets.count == 1 && t.meeting(e.id).socket == .connecting, "cellular is allowed without the switch")
    }

    @Test func aPauseClosesTheSocketAndContinueOpensAnother() throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        try #require(rig.sockets.last).deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        t.follow(e.id, paused: true, entry: e)
        #expect(t.meeting(e.id).socket == .paused && rig.sockets.last?.closed == true && rig.listening)
        #expect(!t.shown(e.id, role: .listener).hubLines)
        #expect(!t.shown(e.id, role: .listener).fellBack, "a pause is no fallback")
        t.follow(e.id, paused: false, entry: e)
        #expect(rig.sockets.count == 2 && t.meeting(e.id).socket == .connecting)
        #expect(!t.shown(e.id, role: .listener).fellBack, "opening again after Continue: no fallback within the grace")
    }

    /// #3862 phase 5 review: a socket that was up shows no fallback while it reconnects briefly, and does once it has been
    /// away longer than the grace; after Stop none shows.
    @Test func aBriefReconnectShowsNoFallbackALongerOneDoes() async throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        try #require(rig.sockets.last).deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        rig.sockets.last?.drop()
        #expect(t.meeting(e.id).socket == .down || t.meeting(e.id).socket == .connecting)
        #expect(!t.shown(e.id, role: .listener).fellBack, "a brief drop")
        await until("another socket") { rig.sockets.count == 2 }
        #expect(rig.waits.contains(MeetingTranscriptionRule.reconnectGrace), "the grace is waited for")
        try #require(rig.sockets.last).deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        #expect(!t.meeting(e.id).away && !t.shown(e.id, role: .listener).fellBack)

        // Away longer than the grace: the notice.
        rig.passing = [MeetingTranscriptionRule.reconnectGrace]
        rig.sockets.last?.drop()
        await until("away") { t.meeting(e.id).away }
        #expect(t.shown(e.id, role: .listener).fellBack)
        await until("a third socket") { rig.sockets.count == 3 }
        try #require(rig.sockets.last).deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        #expect(!t.meeting(e.id).away && !t.shown(e.id, role: .listener).fellBack, "back: no notice")

        t.ended(e.id)
        #expect(t.meeting(e.id).socket == .ending && !t.shown(e.id, role: .listener).fellBack, "after Stop: no fallback")
    }

    @Test func theRoleAndPersonasSpeakingGoOnTheSocket() throws {
        let rig = Rig(), t = make(rig)
        var e = try meeting(rig)
        t.speaking(e.id, voice: .answer, on: true, at: 1, text: "答案")
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        t.speaking(e.id, voice: .call, on: true, at: 2, text: nil)
        t.speaking(e.id, voice: .answer, on: false, at: 3, text: nil)
        t.speaking(e.id, voice: .call, on: false, at: 4, text: nil)
        e.attendance?.set(.off, at: 5)
        t.follow(e.id, paused: false, entry: e)
        Thread.sleep(forTimeInterval: 0.05)
        let frames = socket.frames
        #expect(frames.map { $0["type"] as? String } == ["start", "speaking", "role", "speaking", "role"])
        #expect(frames[1]["on"] as? Bool == true && frames[1]["text"] as? String == "答案", "the open stretch right after start")
        #expect(frames[3]["on"] as? Bool == false && frames[3]["at"] as? Double == 4, "off once the last voice stops")
        #expect(frames[4]["attendance"] as? String == "off" && frames[4]["at"] as? Double == 5)
    }

    @Test func stopSendsEndAndTheLastLinesStillCome() async throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        socket.deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        t.ended(e.id)
        #expect(t.meeting(e.id).socket == .ending)
        t.follow(nil, paused: false, entry: nil)
        #expect(t.meeting(e.id).socket == .ending, "a socket at its end is left to finish")
        await until("the end frame") { socket.frames.last?["type"] as? String == "end" }
        await until("off the tap after the end") { !rig.listening }
        #expect(t.meeting(e.id).socket == .ending, "the socket stays for the hub's last lines")
        socket.deliver(#"{"type":"line","n":3,"start":10,"end":12,"text":"Bo will share the notes tomorrow."}"#)
        #expect(t.meeting(e.id).lines.last?.n == 3)
        socket.drop()
        #expect(t.meeting(e.id).socket == .ended && !rig.listening)
    }

    @Test func theLiveReadsTranscriptionIsKept() throws {
        let rig = Rig(), t = make(rig)
        let id = UUID()
        t.liveRead(id, transcription: MeetingTranscriptionView(mode: .hub, state: "live", fallback: true), interim: nil, read: ["transcription"])
        #expect(t.meeting(id).hub?.fallback == true)
        t.liveRead(id, transcription: nil, interim: nil, read: ["topics"])
        #expect(t.meeting(id).hub != nil, "a read that didn't ask leaves it")
        t.liveRead(id, transcription: nil, interim: nil, read: ["transcription", "interim"])
        #expect(t.meeting(id).hub == nil)
    }

    @Test func gapsGoUpAfterTheMeetingUntilNoneIsLeft() async throws {
        let rig = Rig(), t = make(rig)
        var e = try meeting(rig)
        e.active = false
        e.expectedSegments = 1
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 60, sentences: [])]
        try rig.library.save(e)
        let base = "/api/v1/recordings/\(e.id.uuidString.lowercased())"
        rig.hub.on("GET", base + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,2]]}}"#)
        rig.hub.on("PUT", base + "/audio") { req in
            #expect(req.query["at"] == "0" && req.body?.count == 64000)
            return Data(#"{"gaps":[]}"#.utf8)
        }
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the gap pass") { t.meeting(e.id).gapUpload == .done }
        #expect(rig.hub.sent("PUT", base + "/audio").count == 1 && t.meeting(e.id).gaps.isEmpty)
    }

    @Test func theFinalPassEndsTheGapUploads() async throws {
        let rig = Rig(), t = make(rig)
        var e = try meeting(rig)
        e.active = false
        e.expectedSegments = 1
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 900, sentences: [])]
        try rig.library.save(e)
        let base = "/api/v1/recordings/\(e.id.uuidString.lowercased())"
        rig.hub.on("GET", base + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,900]]}}"#)
        rig.hub.on("PUT", base + "/audio") { _ in throw APIError(status: 409, message: "final", fields: ["code": "transcription_final"]) }
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the gap pass") { t.meeting(e.id).gapUpload == .done }
        #expect(rig.hub.sent("PUT", base + "/audio").count == 1, "no more after transcription_final")
    }

    @Test func wifiOnlyHoldsTheGapUploadsOnCellular() async throws {
        let rig = Rig()
        rig.store.set(true, forKey: MeetingAttendanceDefaults.wifiOnlyKey)
        rig.network = .metered
        let t = make(rig)
        var e = try meeting(rig)
        e.active = false
        e.expectedSegments = 1
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 60, sentences: [])]
        try rig.library.save(e)
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the gap pass") { t.meeting(e.id).gapUpload == .waitingForWiFi }
        #expect(rig.hub.requests.isEmpty)
    }

    // MARK: Review fixes (#3862 phase 5)

    /// A hub that fails right after `ready` is not tried again every second: the failures in a row count across sockets
    /// until one is healthy (its first `ack`), and so do `bad_frame`s.
    @Test func aSocketCountsAsHealthyOnlyAfterItsFirstAck() async throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        func failRightAfterReady(_ n: Int) async throws {
            let socket = try #require(rig.sockets.last)
            socket.deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
            socket.deliver(#"{"type":"error","code":"hub_failed","fatal":true}"#)
            await until("socket \(n + 1)") { rig.sockets.count == n + 1 }
        }
        try await failRightAfterReady(1)
        try await failRightAfterReady(2)
        try await failRightAfterReady(3)
        #expect(rig.waits.filter { $0 < .seconds(5) } == [.seconds(1), .seconds(2), .seconds(4)], "the backoff grows: \(rig.waits)")
        #expect(rig.waits.contains(MeetingTranscriptionRule.healthyAfter), "or healthy once up that long")

        // Acknowledged audio: healthy, so the next drop starts again from a second.
        let healthy = try #require(rig.sockets.last)
        healthy.deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        healthy.deliver(#"{"type":"ack","at":5}"#)
        rig.waits.removeAll()
        healthy.deliver(#"{"type":"error","code":"hub_failed","fatal":true}"#)
        await until("socket 5") { rig.sockets.count == 5 }
        #expect(rig.waits.filter { $0 < .seconds(5) } == [.seconds(1)])

        // `bad_frame` right after each `ready`: the third in a row stops it.
        for n in 5...7 {
            let socket = try #require(rig.sockets.last)
            socket.deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
            socket.deliver(#"{"type":"error","code":"bad_frame","fatal":true}"#)
            if n < 7 { await until("socket \(n + 1)") { rig.sockets.count == n + 1 } }
        }
        #expect(t.meeting(e.id).stopped == "bad_frame" && rig.sockets.count == 7)
    }

    /// The meeting call's `speaking` on carries no text: its captions at playback's start are only the answer's first
    /// words, so the hub applies the time rule; an answer's says what Persona says.
    @Test func theCallsSpeakingGoesWithoutItsPartialWords() throws {
        #expect(MeetingTranscription.spokenText(.call, "Yes") == nil && MeetingTranscription.spokenText(.answer, "好的") == "好的")
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig, role: .participant)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.speaking(e.id, voice: .call, on: true, at: 1, text: "Yes")
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        t.speaking(e.id, voice: .call, on: false, at: 6, text: nil)
        t.speaking(e.id, voice: .call, on: true, at: 9, text: "Pack")
        Thread.sleep(forTimeInterval: 0.05)
        let on = socket.frames.filter { $0["type"] as? String == "speaking" && $0["on"] as? Bool == true }
        #expect(on.count == 2 && on.allSatisfy { $0["text"] == nil }, "no partial words: \(on)")
        #expect(on.first?["at"] as? Double == 1, "the stretch open as the socket started")
    }

    /// The Lock Screen's "Now:" follows the room in hub mode too: new live log positions bring the throttled topics read.
    @Test func newHubLinesBringTheTopicsRead() async throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        rig.hub.on("GET", "/api/v1/recordings/\(e.id.uuidString.lowercased())/live",
                   json: #"{"lines":[{"n":1,"start":4,"end":8,"text":"b","source":"hub"}],"more":false}"#)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        socket.deliver(#"{"type":"ready","from":0,"gaps":[],"next":0}"#)
        socket.deliver(#"{"type":"line","n":0,"start":0,"end":4,"text":"a"}"#)
        #expect(rig.linesAdded == [e.id])
        socket.deliver(#"{"type":"line","n":0,"start":0,"end":4,"text":"a"}"#)
        #expect(rig.linesAdded == [e.id], "a position it had: nothing new")
        socket.drop()
        await until("another socket") { rig.sockets.count == 2 }
        try #require(rig.sockets.last).deliver(#"{"type":"ready","from":8,"gaps":[],"next":2}"#)
        await until("the missed line") { t.meeting(e.id).lines.count == 2 }
        #expect(rig.linesAdded == [e.id, e.id], "a ?from= read that added positions")
    }

    /// A gap pass that hits a transient failure is tried again (15 s first) under background time, until it's done.
    @Test func aGapPassLeftLaterIsTriedAgain() async throws {
        let rig = Rig(), t = make(rig)
        let e = try ended(rig)
        let base = "/api/v1/recordings/\(e.id.uuidString.lowercased())"
        var reads = 0
        rig.hub.on("GET", base + "/live") { _ in
            reads += 1
            if reads == 1 { throw APIError(status: 503, message: "restarting", fields: ["code": "assistant_offline"]) }
            return Data(#"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,2]]}}"#.utf8)
        }
        rig.hub.on("PUT", base + "/audio", json: #"{"gaps":[]}"#)
        rig.passing = [.seconds(15)]
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the second pass") { t.meeting(e.id).gapUpload == .done }
        #expect(reads == 2 && rig.waits.contains(.seconds(15)), "\(rig.waits)")
        #expect(rig.hub.sent("PUT", base + "/audio").count == 1)
        await until("the background time ended") { rig.backgroundEnded == rig.backgroundBegun }
        #expect(rig.backgroundBegun == 1, "one background time across the wait and the pass after it")
    }

    /// With Wi-Fi Only the gap upload itself keeps off cellular, whatever the network looked like.
    @Test func wifiOnlyGapUploadsKeepOffCellular() async throws {
        let rig = Rig()
        rig.store.set(true, forKey: MeetingAttendanceDefaults.wifiOnlyKey)
        let t = make(rig)
        let e = try ended(rig)
        let base = "/api/v1/recordings/\(e.id.uuidString.lowercased())"
        rig.hub.on("GET", base + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,2]]}}"#)
        rig.hub.on("PUT", base + "/audio", json: #"{"gaps":[]}"#)
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the gap pass") { t.meeting(e.id).gapUpload == .done }
        #expect(rig.hub.sent("PUT", base + "/audio").map(\.wifiOnly) == [true])
    }

    /// A Note turned into a hub-mode meeting: the hub lacks the Note's stretch, and it never goes up.
    @Test func theNotesStretchIsNeverUploaded() async throws {
        let rig = Rig(), t = make(rig)
        var a = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, transcription: .hub)
        a.hubTime = [RecordingPeriod(start: 600)]
        let e = try ended(rig, seconds: 900, attendance: a)
        let base = "/api/v1/recordings/\(e.id.uuidString.lowercased())"
        rig.hub.on("GET", base + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,600]]}}"#)
        rig.hub.on("PUT", base + "/audio", json: #"{"gaps":[]}"#)
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the gap pass") { t.meeting(e.id).gapUpload == .done }
        #expect(rig.hub.sent("PUT", base + "/audio").isEmpty, "the Note's ten minutes stay on the phone")
    }

    /// A rollback mid-meeting is kept with the recording: after a relaunch, with the newer manager back and saying `hub`,
    /// nothing streams and no gap goes up.
    @Test func aRollbackIsKeptAcrossARelaunch() async throws {
        let rig = Rig()
        var e = try meeting(rig)
        let before = make(rig)
        before.uploaded(e, answer: .init(mode: "hub"))
        before.follow(e.id, paused: false, entry: e)
        rig.position = 300
        before.uploaded(e, answer: .init(mode: nil))
        let kept = try #require(rig.library.entry(e.id)?.attendance)
        #expect(kept.hubRolledBack && kept.hubTime == [RecordingPeriod(start: 0, end: 300)], "kept with the recording")

        // The relaunch: a fresh store, the meeting ended, the newer manager back.
        e = try #require(rig.library.entry(e.id))
        e.active = false
        e.expectedSegments = 1
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 900, sentences: [])]
        try rig.library.save(e)
        let base = "/api/v1/recordings/\(e.id.uuidString.lowercased())"
        rig.hub.on("GET", base + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,900]]}}"#)
        rig.hub.on("PUT", base + "/audio", json: #"{"gaps":[]}"#)
        let after = make(rig)
        after.uploaded(e, answer: .init(mode: "hub"))
        #expect(after.meeting(e.id).decided?.olderManager == true && !after.hubTranscribes(e.id))
        after.kickGaps()
        try await Task.sleep(for: .milliseconds(100))
        #expect(rig.hub.sent("PUT", base + "/audio").isEmpty && rig.hub.sent("GET", base + "/live").isEmpty)
        var live = e
        live.active = true
        let sockets = rig.sockets.count
        after.follow(e.id, paused: false, entry: live)
        #expect(rig.sockets.count == sockets, "no socket for it")
    }

    /// Stop ends the recording's hub-mode time where it stopped.
    @Test func stopEndsHubModeTime() throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        #expect(rig.library.entry(e.id)?.attendance?.hubTime == [RecordingPeriod(start: 0)])
        t.ended(e.id, at: 120)
        #expect(rig.library.entry(e.id)?.attendance?.hubTime == [RecordingPeriod(start: 0, end: 120)])
    }

    @Test func liveLinesDontGoInHubMode() async throws {
        let library = RecordingLibrary(root: FileManager.default.temporaryDirectory.appending(path: "meeting-live-hub-\(UUID())"))
        var e = RecordingEntry(id: UUID(), createdAt: .now, source: .phone, title: "Weekly", active: true)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.attendance = .room(.listener, private: false, photos: false, liveView: true, transcription: .hub)
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 15, sentences: [RecordingSentence(start: 1, end: 5, text: "hello")])]
        try library.save(e)
        var posts = 0
        let live = MeetingLive(deps: MeetingLive.Deps(post: { _, from, _ in posts += 1; return MeetingLive.Answer(next: from) },
                                                      settled: { _ in true }, upload: { _ in }, openSpans: { _ in [] }, wait: { _ in },
                                                      hubTranscribes: { _ in true }),
                               library: library)
        live.append(e.id)
        await live.send(e.id)
        #expect(posts == 0, "POST …/live is 409 hub_transcribes in hub mode")
    }

    // MARK: Privacy follow-ups (#3862 phase 5)

    private let readyFrame = #"{"type":"ready","from":0,"gaps":[],"next":0}"#

    private func base(_ e: RecordingEntry) -> String { "/api/v1/recordings/\(e.id.uuidString.lowercased())" }

    /// A fatal error ends hub-mode time where the recording is, kept with it: no socket after a relaunch, and after Stop
    /// only what came before the stop goes up as gaps.
    @Test func aFatalErrorEndsHubModeTimeForGood() async throws {
        let rig = Rig(), t = make(rig)
        var e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        socket.deliver(readyFrame)
        rig.position = 600
        socket.deliver(#"{"type":"error","code":"quota_exceeded_somewhere","fatal":true}"#)
        let kept = try #require(rig.library.entry(e.id)?.attendance)
        #expect(kept.hubStopped == RecordingHubStop(at: 600, code: "quota_exceeded_somewhere"))
        #expect(kept.hubTime == [RecordingPeriod(start: 0, end: 600)], "hub-mode time ends at the stop")
        #expect(!rig.listening && socket.closed)

        // A relaunch while it still records: no socket, and the notice says the hub stopped.
        e = try #require(rig.library.entry(e.id))
        let relaunched = make(rig)
        relaunched.uploaded(e, answer: .init(mode: "hub"))
        relaunched.follow(e.id, paused: false, entry: e)
        #expect(rig.sockets.count == 1 && !rig.listening)
        #expect(relaunched.meeting(e.id).stopped == "quota_exceeded_somewhere" && relaunched.shown(e.id, role: .listener).fellBack)

        // Stopped at 60:00: the 50 minutes after the stop stay on the phone; the 10 before it may still go.
        relaunched.ended(e.id, at: 3600)
        e = try #require(rig.library.entry(e.id))
        e.active = false
        e.expectedSegments = 1
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 3600, sentences: [])]
        try rig.library.save(e)
        rig.hub.on("GET", base(e) + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,3600]]}}"#)
        rig.hub.on("PUT", base(e) + "/audio", json: #"{"gaps":[[0,3600]]}"#)
        relaunched.kickGaps()
        await until("the gap pass") { relaunched.meeting(e.id).gapUpload == .done }
        #expect(rig.hub.sent("PUT", base(e) + "/audio").map { $0.query["at"] } == ["0", "300"], "nothing from the stop on")
    }

    /// `replaced`: another socket has the meeting. This phone opens none while it runs, but hub transcription goes on,
    /// so hub-mode time stays open and the gaps may still go up after the meeting.
    @Test func anotherSocketTakingOverLeavesHubModeTimeOpen() throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        socket.deliver(readyFrame)
        socket.deliver(#"{"type":"error","code":"replaced","fatal":true}"#)
        #expect(t.meeting(e.id).stopped == "replaced" && t.meeting(e.id).socket == .off && socket.closed && !rig.listening)
        t.follow(e.id, paused: false, entry: e)
        #expect(rig.sockets.count == 1, "this phone doesn't take it back")
        let kept = try #require(rig.library.entry(e.id)?.attendance)
        #expect(kept.hubStopped == nil && kept.hubTime == [RecordingPeriod(start: 0)])
    }

    /// A rollback recorded while a gap pass runs ends it before its next piece, and a recording deleted meanwhile is
    /// skipped: both are read again before every piece and every recording of the pass.
    @Test func aRollbackOrADeleteDuringAGapPassStopsIt() async throws {
        let rig = Rig(), t = make(rig)
        let a = try ended(rig, seconds: 900)
        let b = try ended(rig, seconds: 900)
        var first: UUID?
        for e in [a, b] {
            rig.hub.on("GET", base(e) + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,900]]}}"#)
            rig.hub.on("PUT", base(e) + "/audio") { _ in
                if first == nil, let now = rig.library.entry(e.id) {
                    first = e.id
                    // While its first piece goes: its next upload is answered by an older manager, and the other is deleted.
                    t.uploaded(now, answer: .init(mode: nil))
                    try rig.library.remove(e.id == a.id ? b.id : a.id)
                }
                return Data(#"{"gaps":[[0,900]]}"#.utf8)
            }
        }
        t.uploaded(a, answer: .init(mode: "hub"))
        await until("the first piece") { first != nil }
        let rolled = try #require(first)
        await until("the pass ended") { t.meeting(rolled).gapUpload == .done && rig.backgroundEnded == rig.backgroundBegun }
        #expect(rig.hub.requests.filter { $0.method == "PUT" }.count == 1, "no piece after the rollback")
        #expect(rig.hub.requests.filter { $0.method == "GET" }.count == 1, "nothing for the deleted one")
        #expect(rig.library.entry(rolled)?.attendance?.hubRolledBack == true)
    }

    /// Turning Wi-Fi Only on stops a gap upload already going over cellular; the pass starts again under the rule.
    @Test func turningWiFiOnlyOnStopsAGapUploadUnderWay() async throws {
        let rig = Rig()
        rig.network = .metered
        let t = make(rig)
        let e = try ended(rig)
        rig.hub.on("GET", base(e) + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,2]]}}"#)
        rig.hub.on("PUT", base(e) + "/audio", json: #"{"gaps":[]}"#)
        rig.holdPuts = true
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the PUT on its way over cellular") { rig.puts == [false] }
        t.wifiOnlyChanged(true)
        await until("the PUT stopped") { rig.putsStopped == 1 }
        await until("the pass again, under the rule") { t.meeting(e.id).gapUpload == .waitingForWiFi && rig.backgroundEnded == rig.backgroundBegun }
        #expect(rig.puts == [false] && rig.hub.sent("PUT", base(e) + "/audio").isEmpty, "nothing more over cellular")
        rig.holdPuts = false
        rig.network = .unmetered
        t.kickGaps()
        await until("the gap pass on Wi-Fi") { t.meeting(e.id).gapUpload == .done }
        #expect(rig.puts == [false, true] && rig.hub.sent("PUT", base(e) + "/audio").map(\.wifiOnly) == [true])
    }

    /// Through a tunnel (whose own connection the system's guard doesn't reach) with Wi-Fi Only: no socket while the
    /// tunnel began before the phone left cellular; one that began on Wi-Fi opens, and closes once the path leaves it.
    @Test func throughATunnelTheSocketWaitsForWiFiSinceItBegan() throws {
        let rig = Rig()
        rig.store.set(true, forKey: MeetingAttendanceDefaults.wifiOnlyKey)
        let opened = Date()
        rig.tunnel = opened
        rig.meteredUntil = opened.addingTimeInterval(30)
        let t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        #expect(t.meeting(e.id).socket == .waitingForWiFi && rig.sockets.isEmpty && rig.listening, "on Wi-Fi, but the tunnel began on cellular")
        rig.tunnel = opened.addingTimeInterval(60)
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        socket.deliver(readyFrame)
        #expect(t.meeting(e.id).socket == .ready)
        rig.network = .metered
        rig.meteredUntil = opened.addingTimeInterval(90)
        t.follow(e.id, paused: false, entry: e)
        #expect(socket.closed && t.meeting(e.id).socket == .waitingForWiFi)
        rig.network = .unmetered
        t.follow(e.id, paused: false, entry: e)
        #expect(rig.sockets.count == 1, "back on Wi-Fi, the same tunnel may still run over cellular")
    }

    /// Through a tunnel with Wi-Fi Only, a gap upload stops as the path leaves Wi-Fi, and none goes through that tunnel
    /// again; one that began since does.
    @Test func throughATunnelTheGapUploadStopsWhenThePathLeavesWiFi() async throws {
        let rig = Rig()
        rig.store.set(true, forKey: MeetingAttendanceDefaults.wifiOnlyKey)
        rig.tunnel = Date()
        let t = make(rig)
        t.networkIs(.unmetered)
        let e = try ended(rig)
        rig.hub.on("GET", base(e) + "/live", json: #"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,2]]}}"#)
        rig.hub.on("PUT", base(e) + "/audio", json: #"{"gaps":[]}"#)
        rig.holdPuts = true
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the PUT on its way") { rig.puts == [true] }
        rig.network = .metered
        rig.meteredUntil = Date()
        t.networkIs(.metered)
        await until("the PUT stopped") { rig.putsStopped == 1 }
        await until("waiting for Wi-Fi") { t.meeting(e.id).gapUpload == .waitingForWiFi && rig.backgroundEnded == rig.backgroundBegun }
        rig.holdPuts = false
        rig.network = .unmetered
        t.networkIs(.unmetered)
        await until("the pass on Wi-Fi") { rig.backgroundBegun == 2 && rig.backgroundEnded == 2 }
        #expect(t.meeting(e.id).gapUpload == .waitingForWiFi && rig.puts == [true], "the same tunnel: nothing")
        rig.tunnel = Date().addingTimeInterval(1)
        t.kickGaps()
        await until("through the new tunnel") { t.meeting(e.id).gapUpload == .done }
        #expect(rig.puts == [true, true] && rig.hub.sent("PUT", base(e) + "/audio").count == 1)
    }

    /// Switching to a Note takes the uplink off the tap in the same step: nothing waits for the observation.
    @Test func aNoteEndsTheUplinkAtOnce() throws {
        let rig = Rig(), t = make(rig)
        let e = try meeting(rig)
        t.uploaded(e, answer: .init(mode: "hub"))
        t.follow(e.id, paused: false, entry: e)
        let socket = try #require(rig.sockets.last)
        socket.deliver(readyFrame)
        t.becameNote(e.id, at: 42)
        #expect(!rig.listening && socket.closed && t.meeting(e.id).socket == .off)
    }

    /// A gap pass left `later` keeps its background time across the wait before the retry and the retry itself, so the
    /// app isn't suspended in between; it ends once nothing is left `later`.
    @Test func theBackgroundTimeIsHeldAcrossTheRetrysWait() async throws {
        let rig = Rig(), t = make(rig)
        let e = try ended(rig)
        var reads = 0
        rig.hub.on("GET", base(e) + "/live") { _ in
            reads += 1
            if reads == 1 { throw APIError(status: 503, message: "restarting", fields: ["code": "assistant_offline"]) }
            return Data(#"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,2]]}}"#.utf8)
        }
        rig.hub.on("PUT", base(e) + "/audio", json: #"{"gaps":[]}"#)
        rig.gated = [.seconds(15)]
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the retry's wait") { rig.waits.contains(.seconds(15)) }
        #expect(rig.backgroundBegun == 1 && rig.backgroundEnded == 0, "held during the wait")
        rig.passing = [.seconds(15)]
        await until("the retry") { t.meeting(e.id).gapUpload == .done }
        await until("ended once nothing is left") { rig.backgroundEnded == 1 }
        #expect(rig.backgroundBegun == 1 && reads == 2)
    }

    /// Background time the system took back (it ended it itself) is asked for again by the retry's pass.
    @Test func backgroundTimeTheSystemTookBackIsAskedForAgain() async throws {
        let rig = Rig(), t = make(rig)
        let e = try ended(rig)
        var reads = 0
        rig.hub.on("GET", base(e) + "/live") { _ in
            reads += 1
            if reads == 1 { throw APIError(status: 503, message: "restarting", fields: ["code": "assistant_offline"]) }
            return Data(#"{"transcription":{"mode":"hub","state":"finishing","gaps":[[0,2]]}}"#.utf8)
        }
        rig.hub.on("PUT", base(e) + "/audio", json: #"{"gaps":[]}"#)
        rig.gated = [.seconds(15)]
        t.uploaded(e, answer: .init(mode: "hub"))
        await until("the retry's wait") { rig.waits.contains(.seconds(15)) }
        rig.expire?()
        rig.passing = [.seconds(15)]
        await until("the retry") { t.meeting(e.id).gapUpload == .done }
        await until("its own time ended") { rig.backgroundEnded == 1 }
        #expect(rig.backgroundBegun == 2, "the retry's pass asked again")
    }
}

// MARK: - Settings

/// A `PUT` that waits until the test lets it go.
private actor SlowPut {
    private var released = false
    private var waiting: [CheckedContinuation<Void, Never>] = []

    func wait() async {
        if released { return }
        await withCheckedContinuation { waiting.append($0) }
    }

    func open() {
        released = true
        waiting.forEach { $0.resume() }
        waiting.removeAll()
    }
}

@MainActor struct MeetingTranscriptionSettingsTests {
    private let assistant = #"{"machine_id":"m","launcher":"claude","cwd":"/","conversations":[],"meeting_transcription_default":"device","meeting_glossary":["Persona"],"meeting_transcription_extension":"","meeting_transcription_ready":true}"#

    private func model(_ hub: FakeHub, store: UserDefaults, wifi: @escaping (Bool) -> Void = { _ in }) -> MeetingTranscriptionSettings {
        let client = hub.client
        return MeetingTranscriptionSettings(deps: .init(api: { client }, store: store, wifiOnlyChanged: wifi))
    }

    @Test func theGlossaryIsCleanedAsTheHubCleansIt() throws {
        #expect(try MeetingTranscriptionSettings.cleanGlossary(["  Persona ", "persona", "", "export\ncap", "Alice"]).get()
                == ["Persona", "export cap", "Alice"])
        #expect(MeetingTranscriptionSettings.cleanGlossary([String(repeating: "x", count: 41)]) == .failure(.termTooLong(String(repeating: "x", count: 41))))
        #expect((try? MeetingTranscriptionSettings.cleanGlossary([String(repeating: "会", count: 40)]).get())?.count == 1)
        #expect(MeetingTranscriptionSettings.cleanGlossary((0..<201).map { "t\($0)" }) == .failure(.tooManyTerms))
        #expect((try? MeetingTranscriptionSettings.cleanGlossary((0..<200).map { "t\($0)" }).get())?.count == 200)
    }

    @Test func theExtensionChoiceShowsOnlyWithMoreThanTheBuiltInOne() throws {
        func ext(_ id: String, _ transcription: [String]?) throws -> HubExtension {
            let contributes = transcription.map { #"{"transcription":\#(String(decoding: try! JSONEncoder().encode($0), as: UTF8.self))}"# } ?? "{}"
            return try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"\#(id)","name":"\#(id.capitalized)","kind":"x","status":"running","contributes":\#(contributes)}"#.utf8))
        }
        let builtIn = MeetingTranscriptionSettings.choices([try ext("voice", ["gpt-live"]), try ext("telegram", nil)])
        #expect(builtIn.map(\.ref) == [""] && !MeetingTranscriptionSettings.showsChoice(builtIn))
        let more = MeetingTranscriptionSettings.choices([try ext("voice", ["gpt-live"]), try ext("example-asr", ["fast", "fast"])])
        #expect(more.map(\.ref) == ["", "example-asr/fast"] && MeetingTranscriptionSettings.showsChoice(more))
    }

    @Test func loadAndSaveGoThroughTheAssistantSettings() async throws {
        let hub = FakeHub()
        let json = assistant
        hub.on("GET", "/api/v1/assistant", json: json)
        hub.on("PUT", "/api/v1/assistant") { req in
            let body = (try JSONSerialization.jsonObject(with: req.body ?? Data())) as? [String: Any] ?? [:]
            #expect(body.keys.sorted() == ["meeting_transcription_default"] && body["meeting_transcription_default"] as? String == "hub")
            return Data(json.replacingOccurrences(of: #""meeting_transcription_default":"device""#, with: #""meeting_transcription_default":"hub""#).utf8)
        }
        let store = UserDefaults(suiteName: "transcription-settings-\(UUID())")!
        let settings = model(hub, store: store)
        await settings.load()
        #expect(settings.offered && settings.values == .init(defaultMode: .device, glossary: ["Persona"], extensionRef: "", ready: true))
        await settings.setDefault(.hub)
        #expect(settings.values?.defaultMode == .hub && settings.failure == nil)
        #expect(store.string(forKey: MeetingAttendanceDefaults.transcriptionDefaultKey) == "hub", "kept for a one-tap start")
    }

    @Test func aRefusedChangeGoesBack() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: assistant)
        hub.on("PUT", "/api/v1/assistant") { _ in throw APIError(status: 400, message: "bad", fields: ["code": "bad_glossary"]) }
        let settings = model(hub, store: UserDefaults(suiteName: "transcription-settings-\(UUID())")!)
        await settings.load()
        await settings.setGlossary(["Persona", "Bo"])
        #expect(settings.values?.glossary == ["Persona"])
        if case .hub? = settings.failure {} else { Issue.record("no failure: \(String(describing: settings.failure))") }
        await settings.setGlossary([String(repeating: "x", count: 41)])
        #expect(settings.failure == .termTooLong(String(repeating: "x", count: 41)) && hub.sent("PUT", "/api/v1/assistant").count == 1,
                "refused here, never sent")
    }

    /// #3862 phase 5 review: a term submitted while another change saves is not sent and says so (false), so the field
    /// keeps it; one the hub kept says true.
    @Test func aTermWhileASaveIsInFlightIsNotDropped() async throws {
        let gate = SlowPut()
        var client = APIClient(config: ServerConfig(url: URL(string: "https://fake.invalid")!, token: "t"))
        let json = assistant
        client.transport = { req in
            if req.method == "PUT" { await gate.wait() }
            return Data(json.utf8)
        }
        let settings = MeetingTranscriptionSettings(deps: .init(api: { client }, store: UserDefaults(suiteName: "transcription-settings-\(UUID())")!))
        await settings.load()
        let first = Task { await settings.setGlossary(["Alice", "Persona"]) }
        for _ in 0..<200 where !settings.saving { try await Task.sleep(for: .milliseconds(5)) }
        #expect(settings.saving)
        #expect(await settings.setGlossary(["Bob", "Persona"]) == false, "not sent while the first saves")
        await gate.open()
        #expect(await first.value, "the first one went")
        #expect(await settings.setGlossary(["Persona"]), "already the hub's: nothing to send")
    }

    @Test func aHubWithoutHubTranscriptionOffersNothing() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"m","launcher":"claude","cwd":"/","conversations":[]}"#)
        let settings = model(hub, store: UserDefaults(suiteName: "transcription-settings-\(UUID())")!)
        await settings.load()
        #expect(!settings.offered && settings.values == nil)
    }

    @Test func wifiOnlyIsKeptOnThePhoneOffByDefault() {
        let store = UserDefaults(suiteName: "transcription-settings-\(UUID())")!
        var told: [Bool] = []
        let settings = model(FakeHub(), store: store) { told.append($0) }
        #expect(!settings.wifiOnly)
        settings.setWiFiOnly(true)
        #expect(settings.wifiOnly && store.bool(forKey: MeetingAttendanceDefaults.wifiOnlyKey) && told == [true])
        #expect(model(FakeHub(), store: store).wifiOnly)
    }
}

// MARK: - What the phone shows and says

/// What the start, the settings, the meeting page, the Live Activity and the recording's page say about hub
/// transcription (#3862 phase 5): the privacy texts per mode (the contract's Wording, the extension named when it isn't
/// the built-in one), the fallback notice, the recording page's facts, the Lock Screen's line, `detail.meta.transcription`
/// and the speech service's choices. Tests run in zh-Hans, so texts are compared with each other, not with English.
@MainActor struct MeetingTranscriptionShownTests {
    private typealias Words = MeetingTranscriptionWords
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    @Test func theLineUnderRecordSaysWhatEachModeSends() {
        let hub = Words.startPrivacy(.hub, service: nil, talk: true, days: 14)
        let named = Words.startPrivacy(.hub, service: "Example ASR", talk: true, days: 14)
        let device = Words.startPrivacy(.device, service: "Example ASR", talk: true, days: 14)
        let quiet = Words.startPrivacy(.device, service: nil, talk: false, days: 14)
        #expect(named.contains("Example ASR") && !hub.contains("Example ASR"), "the extension named only when the owner chose it")
        #expect(Set([hub, named, device, quiet]).count == 4)
        #expect(!device.contains("Example ASR"), "privacy mode sends no meeting audio, whatever extension is chosen")
        #expect(Words.startPrivacy(nil, service: nil, talk: true, days: 14) == device, "a hub without the flag: as before")
        #expect(Words.startPrivacy(.hub, service: nil, talk: false, days: 14) == hub, "hub mode's words say Talk included")
        #expect(hub.contains("14") && quiet.contains("14"), "how long the text is kept")
        #expect(Words.modePrivacy(.hub, service: "Example ASR").contains("Example ASR"))
        #expect(Words.modePrivacy(.device, service: "Example ASR") == Words.modePrivacy(.device, service: nil))
        #expect(Words.title(.device) != Words.title(.hub))
    }

    @Test func thePagesTalkLineFollowsTheMode() {
        let device = Words.pagePrivacy(hub: false, service: "Example ASR")
        #expect(device == Words.modePrivacy(.device, service: nil), "privacy mode: phase 4's Wording, as before")
        #expect(Words.pagePrivacy(hub: true, service: "Example ASR").contains("Example ASR"))
        #expect(Set([device, Words.pagePrivacy(hub: true, service: nil), Words.pagePrivacy(hub: true, service: "Example ASR")]).count == 3)
    }

    @Test func theFallbackNoticeSaysWhy() {
        var m = MeetingTranscription.Meeting()
        m.decided = .init(mode: .hub)
        m.socket = .waitingForWiFi
        let wifi = Words.fallback(m)
        m.socket = .ready
        m.stopped = "replaced"
        let stopped = Words.fallback(m)
        m.stopped = nil
        m.problem = "upstream_failed"
        let problem = Words.fallback(m)
        #expect(Set([wifi, stopped, problem]).count == 3)
    }

    /// With the Highlights card on top (phase 2b), the hub's lines are the Full Transcript's rows in place of the phone's:
    /// in the live log's order, a placeholder skipped, each row's id its position, so a line the hub sends again keeps its
    /// row; bookmarks and questions keep their places; and a highlight's minute lights the hub's lines in it.
    @Test func theTranscriptShowsTheHubsLinesByPosition() throws {
        let lines = [MeetingHubLine(n: 2, start: 790, end: 794, text: "Third", source: nil),
                     MeetingHubLine(n: 0, start: 754, end: 758, text: "First", source: "hub"),
                     MeetingHubLine(n: 1, start: 760, end: 764, text: "", source: "hub"),
                     MeetingHubLine(n: 3, start: 800, end: 804, text: "Fourth", source: "device")]
        let sentences = RecordingTimeline.sentences(hub: lines)
        #expect(sentences.map(\.line) == [0, 2, 3], "by position, the placeholder left out")
        let ask = UUID()
        let items = RecordingTimeline.make(sentences: sentences, marks: [770], asks: [.init(id: ask, at: 799)],
                                           transcribedUntil: 900, now: 900)
        #expect(items.map(\.id) == [.line(0), .bookmark(0), .line(2), .exchange(ask), .line(3)])
        let again = RecordingTimeline.make(sentences: RecordingTimeline.sentences(hub: lines + [MeetingHubLine(n: 4, start: 810, end: 812, text: "Fifth")]),
                                           marks: [770], asks: [.init(id: ask, at: 799)], transcribedUntil: 900, now: 900)
        #expect(Array(again.map(\.id).prefix(items.count)) == items.map(\.id), "a new line keeps the rows before it")
        let focus = try #require(RecordingTimeline.focus(at: 790, end: 804, in: items))
        #expect(focus.scroll == .line(2) && focus.lit == [.line(2), .line(3)], "the minute's hub lines lit")
    }

    @Test func theRecordingPageSaysTheModeTheStateAndTheGaps() throws {
        #expect(Words.facts(nil, asked: .hub, gapUpload: .idle) == nil, "no mode decided: nothing")
        #expect(Words.facts(.init(mode: .device), asked: nil, gapUpload: .idle) == nil, "a meeting on the phone as always")
        let refused = try #require(Words.facts(.init(mode: .device), asked: .hub, gapUpload: .idle))
        #expect(refused.state != nil && !refused.busy, "asked for the hub and kept on the phone: said why")

        let finishing = try #require(Words.facts(.init(mode: .hub, state: "finishing"), asked: .hub, gapUpload: .uploading))
        #expect(finishing.busy && finishing.gaps == nil && finishing.upload != nil, "being finished, the missing audio going up")
        let live = try #require(Words.facts(.init(mode: .hub, state: "live"), asked: .hub, gapUpload: .waitingForWiFi))
        #expect(live.busy && live.state == finishing.state && live.upload != finishing.upload)
        let final = try #require(Words.facts(.init(mode: .hub, state: "final", gaps: [.init(from: 600, to: 642.5)]), asked: .hub,
                                             gapUpload: .uploading))
        #expect(!final.busy && final.state != finishing.state && final.upload == nil, "final: no more uploads said")
        #expect(final.gaps?.contains("10:00") == true && final.gaps?.contains("10:42") == true)
        let failed = try #require(Words.facts(.init(mode: .hub, state: "failed"), asked: .hub, gapUpload: .idle))
        #expect(!failed.busy && Set([failed.state, final.state, finishing.state]).count == 3)
        let unknown = try #require(Words.facts(.init(mode: .hub, state: "someday"), asked: .hub, gapUpload: .idle))
        #expect(unknown.busy, "a state the app doesn't know reads as still being finished")

        let many = (0..<5).map { MeetingGap(from: Double($0 * 100), to: Double($0 * 100 + 10)) }
        let spans = Words.spans(many)
        #expect(spans.contains("0:00") && spans.contains("3:30") && !spans.contains("6:40") && spans.contains("2"),
                "the first three, then how many more")
        #expect(Words.spans(Array(many.prefix(1))) == "0:00–0:10")
    }

    /// #3862 phase 5 review: the recording page's footer says the audio went through the hub from what the phone knows,
    /// before `detail.meta` arrives.
    @Test func theFooterSaysTheHubBeforeTheMetaArrives() {
        let hub = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, transcription: .hub)
        #expect(Words.audioWentToHub(meta: nil, decided: nil, attendance: hub), "asked for the hub, meta not in yet")
        #expect(!Words.audioWentToHub(meta: .init(mode: .device), decided: nil, attendance: hub), "the hub's word once read")
        #expect(Words.audioWentToHub(meta: .init(mode: .hub), decided: nil, attendance: nil))
        #expect(!Words.audioWentToHub(meta: nil, decided: .init(mode: .device, problem: "unavailable"), attendance: hub),
                "the upload's 200 kept it on the phone")
        #expect(Words.audioWentToHub(meta: nil, decided: .init(mode: .hub), attendance: hub))
        #expect(Words.audioWentToHub(meta: nil, decided: .init(mode: .device, olderManager: true), attendance: hub),
                "rolled back after streaming: the audio before may have gone")
        let device = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, transcription: .device)
        #expect(!Words.audioWentToHub(meta: nil, decided: nil, attendance: device))
        #expect(!Words.audioWentToHub(meta: nil, decided: nil, attendance: nil))
        var never = hub
        never.hubTime = []
        #expect(!Words.audioWentToHub(meta: nil, decided: nil, attendance: never), "no hub-mode time")
    }

    @Test func theDetailReadsTranscriptionFromItsMeta() throws {
        let answer = try JSON.decoder.decode(RecordingMinutesModel.Answer.self, from: Data(#"""
        {"detail": {"meta": {"minutes_state": "pending", "transcription": {"mode": "hub", "state": "finishing", "gaps": [[600, 642.5]], "fallback": true}}}}
        """#.utf8))
        let view = try #require(answer.detail.meta?.transcription)
        #expect(view.mode == .hub && view.known == .finishing && view.gaps == [MeetingGap(from: 600, to: 642.5)] && view.fallback)
        #expect(answer.detail.meta?.state == "pending")
        let odd = try JSON.decoder.decode(RecordingMinutesModel.Answer.self, from: Data(#"""
        {"detail": {"meta": {"minutes_state": "ready", "transcription": "hub"}}}
        """#.utf8))
        #expect(odd.detail.meta?.transcription == nil && odd.detail.meta?.state == "ready", "read leniently, the rest stays")
    }

    // MARK: The Live Activity

    private func meeting(lines: [String]) -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: now, source: .phone, title: "Weekly", locale: "zh-CN", active: true)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.attendance = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, transcription: .hub)
        for (n, text) in lines.enumerated() {
            e.attendance?.live.append(RecordingSentence(start: Double(n * 10), end: Double(n * 10 + 8), text: text))
        }
        return e
    }

    private func content(_ e: RecordingEntry, _ transcribed: RecordingActivity.Transcribed?, answer: String? = nil) -> RecordingActivity.Content {
        RecordingActivity.content(entry: e, recording: true, interrupted: false, elapsed: 100, previous: nil, now: now,
                                  answer: answer, answeredAt: answer == nil ? nil : now, transcribed: transcribed)
    }

    @Test func theLockScreenShowsTheHubsLineThenThePhonesOwnSaidSo() throws {
        let e = meeting(lines: ["手机自己的一句"])
        let healthy = MeetingTranscriptionRule.shown(hub: true, socket: .ready, everUp: true, away: false, problem: nil, stopped: false, role: .listener)
        let fell = MeetingTranscriptionRule.shown(hub: true, socket: .ready, everUp: true, away: false, problem: "upstream_failed", stopped: false,
                                                  role: .listener)
        let up = content(e, .init(hubLine: "Bo will share the notes tomorrow.", shown: healthy))
        #expect(up.line == "Bo will share the notes tomorrow." && up.fellBack == nil)
        let back = content(e, .init(hubLine: "Bo will share the notes tomorrow.", shown: fell))
        #expect(back.line == "手机自己的一句" && back.fellBack == true, "the phone's own line, said so")
        #expect(content(e, nil).line == "手机自己的一句" && content(e, nil).fellBack == nil, "device mode: as before")
        let answered = content(e, .init(hubLine: nil, shown: fell), answer: "下周再定")
        #expect(answered.answer == "下周再定" && answered.line == nil && answered.fellBack == nil, "only beside a line")
        let empty = content(meeting(lines: []), .init(hubLine: nil, shown: fell))
        #expect(empty.line == nil && empty.fellBack == nil)
        // A state from before reads as it was; the new field keeps it small.
        let old = try JSON.decoder.decode(RecordingActivity.Content.self, from: JSON.encoder.encode(content(e, nil)))
        #expect(old.fellBack == nil)
        #expect(try JSON.encoder.encode(back).count < 1500)
    }

    // MARK: The speech service

    @Test func theSpeechServiceIsNamedOnlyWhenTheOwnerChoseAnother() throws {
        func ext(_ id: String, _ name: String, _ transcription: [String]?) throws -> HubExtension {
            let contributes = transcription.map { #"{"transcription":["\#($0.joined(separator: #"",""#))"]}"# } ?? "{}"
            return try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"\#(id)","name":"\#(name)","kind":"x","status":"running","contributes":\#(contributes)}"#.utf8))
        }
        let choices = MeetingTranscriptionSettings.choices([try ext("voice", "Voice", ["gpt-live"]), try ext("example-asr", "Example ASR", ["fast", "accurate"])])
        #expect(choices.map(\.ref) == ["", "example-asr/fast", "example-asr/accurate"])
        #expect(MeetingTranscriptionSettings.service("", choices: choices) == nil)
        #expect(MeetingTranscriptionSettings.service("example-asr/fast", choices: choices) == "Example ASR")
        #expect(MeetingTranscriptionSettings.service("gone/x", choices: choices) == "gone", "one the hub doesn't list goes by its id")
        #expect(MeetingTranscriptionSettings.label(choices[1], in: choices).contains("fast"), "several from one extension: the contribution too")
        #expect(MeetingTranscriptionSettings.label(choices[0], in: choices) != MeetingTranscriptionSettings.label(choices[1], in: choices))
    }

    @Test func theSettingsReadTheExtensionsForTheChoice() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"m","launcher":"claude","cwd":"/","conversations":[],"meeting_transcription_default":"hub","meeting_glossary":[],"meeting_transcription_extension":"example-asr/fast","meeting_transcription_ready":true}"#)
        let client = hub.client
        let list = [try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"example-asr","name":"Example ASR","kind":"x","status":"running","contributes":{"transcription":["fast"]}}"#.utf8))]
        let settings = MeetingTranscriptionSettings(deps: .init(api: { client }, store: UserDefaults(suiteName: "transcription-shown-\(UUID())")!,
                                                                plugins: { _ in list }))
        #expect(!MeetingTranscriptionSettings.showsChoice(settings.choices), "only the built-in one before the list is read")
        await settings.load()
        #expect(MeetingTranscriptionSettings.showsChoice(settings.choices) && settings.service == "Example ASR")
        let failing = MeetingTranscriptionSettings(deps: .init(api: { client }, store: UserDefaults(suiteName: "transcription-shown-\(UUID())")!,
                                                               plugins: { _ in throw APIError(status: 503, message: "down") }))
        await failing.load()
        #expect(failing.offered && !MeetingTranscriptionSettings.showsChoice(failing.choices), "a list that can't be read hides the choice")
        #expect(failing.service == "example-asr", "and the privacy text still names it")
    }
}
