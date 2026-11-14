import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The voice extension's socket contract on the app's side (docs/assistant/voice.md 3, 6.10「帧」): frames in, frames out, the
/// address, PCM16 and what is held before `ready`. Pure: no audio, no hub.
struct VoiceWireTests {
    private func object(_ text: String) -> [String: Any]? {
        (try? JSONSerialization.jsonObject(with: Data(text.utf8))) as? [String: Any]
    }

    // MARK: Frames in

    @Test func readyCarriesProtocolFeaturesAndTarget() throws {
        let frame = try #require(VoiceFrame.parse(#"{"type":"ready","protocol":2,"features":["target","runs"],"input_rate":24000,"output_rate":24000.0,"model":"custom/gpt-live-1","voice":"marin","target":{"machine_id":"m1","session_id":"s1","kind":"agent","title":"web-api","agent_label":"Claude Code","machine_name":"Mac"}}"#))
        guard case .ready(let r) = frame else { Issue.record("not ready: \(frame)"); return }
        #expect(r.protocolVersion == 2)
        #expect(r.features == ["target", "runs"])
        #expect(r.inputRate == 24000 && r.outputRate == 24000)
        #expect(r.model == "custom/gpt-live-1" && r.voice == "marin")
        #expect(r.target == VoiceTargetInfo(machineID: "m1", sessionID: "s1", kind: "agent", title: "web-api",
                                            agentLabel: "Claude Code", machineName: "Mac"))
        #expect(r.target?.matches(SessionKey(group: "g", machineID: "m1", sessionID: "s1")) == true)
        #expect(r.target?.matches(SessionKey(machineID: "m1", sessionID: "s2")) == false)
    }

    /// Protocol 1's ready (Android's, #1931's) and odd values fall back to the defaults.
    @Test func readyFromAnOlderExtensionFallsBack() {
        #expect(VoiceFrame.parse(#"{"type":"ready","input_rate":0,"output_rate":"fast"}"#)
            == .ready(VoiceReady(protocolVersion: 1, features: [], inputRate: 16000, outputRate: 24000, model: nil, voice: nil, target: nil)))
        guard case .ready(let r)? = VoiceFrame.parse(#"{"type":"ready","protocol":2,"target":{"machine_id":"m1"}}"#) else {
            Issue.record("not ready")
            return
        }
        #expect(r.target == nil, "a target without both ids is not one")
    }

    @Test func runFramesKeepEveryField() {
        let frame = VoiceFrame.parse(#"{"type":"run","id":"voice-7c1e","task":"修登录页失败的测试","state":"refused","msg_ids":["voice-7c1e"],"base_seq":812,"started_at":"2026-11-06T10:00:00.123456789Z","code":"input_draft","message":"409","draft":"[⧉ Selected 3 lines from foo.go] 没发的字","clearable":"没发的字"}"#)
        guard case .run(let run)? = frame else { Issue.record("not a run: \(String(describing: frame))"); return }
        #expect(run.id == "voice-7c1e" && run.task == "修登录页失败的测试")
        #expect(run.state == .refused && !run.state.isOpen)
        #expect(run.msgIDs == ["voice-7c1e"] && run.baseSeq == 812)
        #expect(run.startedAt == .text("2026-11-06T10:00:00.123456789Z"))
        #expect(run.startedAt?.date.map { abs($0.timeIntervalSince1970 - 1_793_959_200.123) < 0.01 } == true)
        #expect(run.code == "input_draft" && run.message == "409" && run.draft == "[⧉ Selected 3 lines from foo.go] 没发的字" && run.clearable == "没发的字")
    }

    @Test func runStatesAndWhichAreOpen() {
        let open = ["sending", "sent", "working", "needs_you", "blocked", "held", "later_state"]
        let over = ["done", "failed", "refused", "unknown", "folded"]
        for s in open { #expect(VoiceRunState(wire: s).isOpen, "\(s)") }
        for s in over { #expect(!VoiceRunState(wire: s).isOpen, "\(s)") }
        for s in open + over { #expect(VoiceRunState(wire: s).wire == s) }
        #expect(VoiceRunState(wire: "needs_you") == .needsYou)
    }

    @Test func runWithMissingFieldsAndNumericStart() {
        guard case .run(let run)? = VoiceFrame.parse(#"{"type":"run","id":"r1","started_at":1793872800123}"#) else {
            Issue.record("not a run")
            return
        }
        #expect(run.task == "" && run.msgIDs.isEmpty && run.baseSeq == 0 && run.clearable == nil)
        #expect(run.state == .other(""))
        #expect(run.startedAt == .number(1_793_872_800_123))
        #expect(run.startedAt?.date == Date(timeIntervalSince1970: 1_793_872_800.123))
        #expect(VoiceFrame.parse(#"{"type":"run","task":"x"}"#) == .other(type: "run"), "a run needs its id")
    }

    /// A buggy or third-party extension's numbers fall back to the defaults instead of trapping, and `started_at` goes
    /// back exactly as it came, a fraction included.
    @Test func oddNumbersFallBackAndStampsGoBackAsTheyCame() throws {
        guard case .run(let run)? = VoiceFrame.parse(#"{"type":"run","id":"r1","state":"working","base_seq":1e19,"started_at":1e19}"#) else {
            Issue.record("not a run")
            return
        }
        #expect(run.baseSeq == 0, "too large for an Int: the default")
        #expect(run.startedAt == .number(1e19))
        guard case .run(let fraction)? = VoiceFrame.parse(#"{"type":"run","id":"r2","state":"working","base_seq":12.5,"started_at":1793872800.5}"#) else {
            Issue.record("not a run")
            return
        }
        #expect(fraction.baseSeq == 0, "a seq is a whole number")
        #expect(fraction.startedAt == .number(1_793_872_800.5))
        guard case .ready(let ready)? = VoiceFrame.parse(#"{"type":"ready","protocol":1e300,"input_rate":1e300}"#) else {
            Issue.record("not ready")
            return
        }
        #expect(ready.protocolVersion == 1 && ready.inputRate == VoiceFrame.defaultInputRate)

        let text = VoiceMessage.start(VoiceStart(resume: [run, fraction]))
        let runs = try #require((object(text)?["resume"] as? [String: Any])?["runs"] as? [[String: Any]])
        #expect(runs.map { $0["started_at"] as? Double } == [1e19, 1_793_872_800.5])
        #expect(text.contains("1793872800.5"))
    }

    @Test func askLinkErrorTranscriptToolAndClear() {
        #expect(VoiceFrame.parse(#"{"type":"ask","interaction_id":"i1","state":"asking"}"#)
            == .ask(VoiceAsk(interactionID: "i1", state: .asking, by: nil)))
        #expect(VoiceFrame.parse(#"{"type":"ask","interaction_id":"i1","state":"released","by":"elsewhere"}"#)
            == .ask(VoiceAsk(interactionID: "i1", state: .released, by: "elsewhere")))
        #expect(VoiceFrame.parse(#"{"type":"ask","state":"asking"}"#) == .other(type: "ask"))
        #expect(VoiceFrame.parse(#"{"type":"link","state":"paused"}"#) == .link(.paused))
        #expect(VoiceFrame.parse(#"{"type":"link","state":"waking"}"#) == .link(.waking))
        #expect(VoiceFrame.parse(#"{"type":"link","state":"later"}"#) == .link(.other("later")))
        #expect(VoiceFrame.parse(#"{"type":"error","message":"Forbidden","fatal":true,"code":"upstream_forbidden"}"#)
            == .error(VoiceFailure(code: .upstreamForbidden, message: "Forbidden", fatal: true)))
        #expect(VoiceFrame.parse(#"{"type":"error","message":"offline","code":"machine_offline"}"#)
            == .error(VoiceFailure(code: .machineOffline, message: "offline", fatal: false)))
        #expect(VoiceFrame.parse(#"{"type":"error","message":"old"}"#) == .error(VoiceFailure(code: nil, message: "old", fatal: false)))
        #expect(VoiceErrorCode(wire: "target_read_only") == .targetReadOnly)
        #expect(VoiceErrorCode(wire: "new_code") == .other("new_code"))
        #expect(VoiceFrame.parse(#"{"type":"transcript","id":"u1","role":"user","text":"你好"}"#)
            == .transcript(VoiceTranscript(id: "u1", role: "user", text: "你好", final: false)))
        // #5673: the line a late caption goes before; an empty one is none.
        #expect(VoiceFrame.parse(#"{"type":"transcript","id":"u2","role":"user","text":"它到哪了？","final":false,"before":"a1"}"#)
            == .transcript(VoiceTranscript(id: "u2", role: "user", text: "它到哪了？", final: false, before: "a1")))
        #expect(VoiceFrame.parse(#"{"type":"transcript","id":"u2","role":"user","text":"它到哪了？","before":""}"#)
            == .transcript(VoiceTranscript(id: "u2", role: "user", text: "它到哪了？", final: false)))
        #expect(VoiceFrame.parse(#"{"type":"tool","id":"t1","label":"查知识库","done":true}"#) == .tool(id: "t1", label: "查知识库", done: true))
        #expect(VoiceFrame.parse(#"{"type":"clear"}"#) == .clear)
    }

    @Test func unknownFramesAndGarbage() {
        #expect(VoiceFrame.parse(#"{"type":"something_new","x":1}"#) == .other(type: "something_new"))
        #expect(VoiceFrame.parse("") == nil)
        #expect(VoiceFrame.parse("not json") == nil)
        #expect(VoiceFrame.parse("[1,2]") == nil)
        #expect(VoiceFrame.parse(#"{"type":3}"#) == nil)
        #expect(VoiceFrame.parse(#"{"id":"a"}"#) == nil)
    }

    // MARK: Frames out

    @Test func startCarriesProtocolTargetRecentHistoryAndResume() throws {
        let open = VoiceRun(id: "voice-1", task: "跑测试", state: .working, msgIDs: ["voice-1", "voice-2"], baseSeq: 820,
                            startedAt: .number(1_793_872_800_123))
        let text = VoiceMessage.start(VoiceStart(model: "custom/gpt-live-1", voice: "marin", target: VoiceTarget(machineID: "m1", sessionID: "s1"),
                                                 recent: [VoiceLine(role: "user", text: "修一下")], history: [VoiceLine(role: "assistant", text: "好")],
                                                 resume: [open]))
        let o = try #require(object(text))
        #expect(o["type"] as? String == "start")
        #expect(o["protocol"] as? Int == 2)
        #expect(o["model"] as? String == "custom/gpt-live-1")
        #expect(o["voice"] as? String == "marin")
        #expect(o["target"] as? [String: String] == ["machine_id": "m1", "session_id": "s1"])
        #expect(o["recent"] as? [[String: String]] == [["role": "user", "text": "修一下"]])
        #expect(o["history"] as? [[String: String]] == [["role": "assistant", "text": "好"]])
        let runs = try #require((o["resume"] as? [String: Any])?["runs"] as? [[String: Any]])
        #expect(runs.count == 1)
        #expect(runs[0]["id"] as? String == "voice-1")
        #expect(runs[0]["msg_ids"] as? [String] == ["voice-1", "voice-2"])
        #expect(runs[0]["task"] as? String == "跑测试")
        #expect(runs[0]["base_seq"] as? Int == 820)
        #expect(runs[0]["state"] as? String == "working")
        #expect(runs[0]["started_at"] as? Int64 == 1_793_872_800_123, "a number goes back as the same integer")
        #expect(text.contains("1793872800123"))
    }

    @Test func startLeavesOutWhatIsEmpty() throws {
        let o = try #require(object(VoiceMessage.start(VoiceStart(model: "", voice: nil))))
        #expect(Set(o.keys) == ["type", "protocol"])
        let stamp = try #require(object(VoiceMessage.start(VoiceStart(resume: [VoiceRun(id: "r", task: "t", state: .sent, msgIDs: [], baseSeq: 1,
                                                                                          startedAt: .text("2026-11-06T10:00:00Z"))]))))
        #expect(((stamp["resume"] as? [String: Any])?["runs"] as? [[String: Any]])?.first?["started_at"] as? String == "2026-11-06T10:00:00Z")
    }

    @Test func startSaysWhetherTheMicrophoneCancelsEcho() throws {
        #expect(object(VoiceMessage.start(VoiceStart(echoCancellation: false)))?["echo_cancellation"] as? Bool == false)
        #expect(object(VoiceMessage.start(VoiceStart(echoCancellation: true)))?["echo_cancellation"] as? Bool == true)
    }

    @Test func interruptTextAndResend() {
        #expect(object(VoiceMessage.interrupt)?["type"] as? String == "interrupt")
        let text = object(VoiceMessage.text("说个笑话 \"好\" 换行\n"))
        #expect(text?["type"] as? String == "text" && text?["text"] as? String == "说个笑话 \"好\" 换行\n")
        let resend = object(VoiceMessage.resend(id: "voice-1", clearDraft: "电脑上没发出去的字"))
        #expect(resend?["type"] as? String == "resend" && resend?["id"] as? String == "voice-1")
        #expect(resend?["clear_draft"] as? String == "电脑上没发出去的字")
        #expect(object(VoiceMessage.resend(id: "voice-1", clearDraft: nil))?["clear_draft"] == nil)
    }

    // MARK: Address

    @Test func socketAddressFollowsTheHubSchemeAndPathPrefix() {
        var plain = APIClient(config: ServerConfig(url: URL(string: "http://hub.example.com:8080")!, token: "t0"))
        plain.extensionsAPI = true
        #expect(VoiceEndpoint.url(plain, extensionID: "voice").absoluteString == "ws://hub.example.com:8080/api/v1/extensions/voice/ws")
        let request = VoiceEndpoint.request(plain, extensionID: "voice")
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer t0")

        var secure = APIClient(config: ServerConfig(url: URL(string: "https://hub.example.com/persona/")!, token: "t1"))
        secure.extensionsAPI = true
        #expect(VoiceEndpoint.url(secure, extensionID: "voice").absoluteString == "wss://hub.example.com/persona/api/v1/extensions/voice/ws")
    }

    /// A hub from before the rename has the extension routes under /api/v1/plugins only.
    @Test func socketAddressOnAnOlderHub() {
        let old = APIClient(config: ServerConfig(url: URL(string: "https://hub.example.com")!, token: "t"))
        #expect(VoiceEndpoint.url(old, extensionID: "voice").absoluteString == "wss://hub.example.com/api/v1/plugins/voice/ws")
    }

    // MARK: PCM and the uplink

    @Test func pcmRoundTripsLittleEndian() {
        let data = VoicePCM.data([1, -2, 0x1234])
        #expect(Array(data) == [0x01, 0x00, 0xFE, 0xFF, 0x34, 0x12])
        #expect(VoicePCM.samples(data + Data([0x7F])) == [1, -2, 0x1234])
        #expect(VoicePCM.floats(Data([0x00, 0x40])) == [0.5])
        #expect(VoicePCM.silence(bytes: 5) == Data(count: 4))
        #expect(VoicePCM.rms(Data.pcm(100, 0)) == 0)
        #expect(abs(VoicePCM.rms(Data.pcm(100, 16384)) - 0.5) < 0.001)
    }

    @Test func resamplingKeepsDurationAndShape() {
        let ramp = VoicePCM.data((0..<160).map { Int16($0 * 100) })
        let up = VoicePCM.resample(ramp, from: 16000, to: 24000)
        #expect(up.count == 240 * 2)
        let samples = VoicePCM.samples(up)
        #expect(samples.first == 0)
        #expect(zip(samples, samples.dropFirst()).allSatisfy { $0 <= $1 }, "a ramp stays a ramp")
        #expect(VoicePCM.resample(ramp, from: 16000, to: 16000) == ramp)
        #expect(VoicePCM.samples(VoicePCM.resample(ramp, from: 24000, to: 16000)).count == 107)
    }

    @Test func uplinkHoldsAtMostThreeSecondsThenSendsInOrder() {
        let uplink = VoiceUplink(seconds: 3)
        let second = 16000
        for value in Int16(1)...Int16(4) { uplink.push(.pcm(second, value), rate: 16000) } // four seconds, one value each
        #expect(abs(uplink.heldSeconds - 3) < 0.001, "the oldest second is dropped")
        let sent = VoiceBox<[Data]>([])
        uplink.open(rate: 16000) { sent.value.append($0) }
        let samples = VoicePCM.samples(sent.value.reduce(Data(), +))
        #expect(samples.count == 3 * second)
        #expect(samples.first == 2 && samples.last == 4, "what is left starts with the second second")
        #expect(sent.value.allSatisfy { $0.count <= 1600 * 2 }, "sent a tenth of a second at a time")
        uplink.push(.pcm(160, 9), rate: 16000)
        #expect(sent.value.last == .pcm(160, 9), "after opening, a push goes straight out")
        #expect(uplink.heldSeconds == 0)
    }

    @Test func uplinkResamplesWhatWasHeldToTheRateReadyAsksFor() {
        let uplink = VoiceUplink()
        uplink.push(.pcm(1600), rate: 16000) // a tenth of a second at the guessed rate
        let sent = VoiceBox<[Data]>([])
        uplink.open(rate: 24000) { sent.value.append($0) }
        #expect(sent.value.reduce(0) { $0 + $1.count } == 2400 * 2)
        uplink.push(.pcm(1600), rate: 16000) // the converter not switched yet
        #expect(sent.value.last?.count == 2400 * 2)
    }

    @Test func mutedUplinkSendsSilenceOfTheSameLength() {
        let uplink = VoiceUplink()
        let levels = VoiceBox<[Float]>([])
        uplink.onLevel { levels.value.append($0) }
        let sent = VoiceBox<[Data]>([])
        uplink.open(rate: 16000) { sent.value.append($0) }
        uplink.isMuted = true
        uplink.push(.pcm(800, 8000), rate: 16000)
        #expect(sent.value == [Data(count: 1600)])
        #expect(levels.value == [0], "muted reads as quiet")
        uplink.isMuted = false
        uplink.push(.pcm(800, 8000), rate: 16000)
        #expect(sent.value.last == .pcm(800, 8000))
        #expect(levels.value.count == 2 && abs(levels.value[1] - 0.244) < 0.01)
    }

    @Test func heldAgainAfterHoldAndNothingAfterClose() {
        let uplink = VoiceUplink()
        let sent = VoiceBox<[Data]>([])
        uplink.open(rate: 16000) { sent.value.append($0) }
        uplink.hold()
        uplink.push(.pcm(160), rate: 16000)
        #expect(sent.value.isEmpty && uplink.heldSeconds > 0)
        uplink.close()
        uplink.push(.pcm(160), rate: 16000)
        uplink.open(rate: 16000) { sent.value.append($0) }
        #expect(sent.value.isEmpty && uplink.heldSeconds == 0)
    }

    // MARK: recent

    private func item(_ seq: Int, _ role: String, _ text: String, kind: String = "text") -> Item {
        let json = #"{"id":"i\#(seq)","seq":\#(seq),"role":"\#(role)","kind":"\#(kind)","text":\#(String(data: try! JSONSerialization.data(withJSONObject: [text]), encoding: .utf8)!.dropFirst().dropLast())}"#
        return try! JSON.decoder.decode(Item.self, from: Data(json.utf8))
    }

    @Test func recentIsTheLastThreeTurnsOfWords() {
        let items = [
            item(1, "user", "第一轮"), item(2, "assistant", "好的"),
            item(3, "user", "第二轮"), item(4, "assistant", "想一想", kind: "thinking"), item(5, "assistant", "", kind: "tool_use"),
            item(6, "assistant", "改好了"),
            item(7, "user", "<persona-context kind=\"context\" title=\"语音对话\">你：顺便修 lint</persona-context>\n第三轮"),
            item(8, "user", "第四轮"), item(9, "assistant", String(repeating: "长", count: 400)),
        ]
        let recent = VoiceLine.recent(items)
        #expect(recent.map(\.text).prefix(4) == ["第二轮", "改好了", "第三轮", "第四轮"])
        #expect(recent.map(\.role) == ["user", "assistant", "user", "user", "assistant"])
        #expect(recent.last?.text.count == 300 && recent.last?.text.hasSuffix("…") == true)
        #expect(VoiceLine.recent([]).isEmpty)
    }
}

/// A value the uplink's send closures can append to in a test.
final class VoiceBox<T>: @unchecked Sendable {
    var value: T
    init(_ value: T) { self.value = value }
}
