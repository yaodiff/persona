// i18n-ignore-file: DEBUG screenshot samples (made-up captions, runs and cards)
#if DEBUG
import SwiftUI
import PersonaWire

/// The voice mode's screenshot switches (docs/assistant/voice.md 6.8):
/// - `-showVoiceSample listening|speaking|working|paused|asking|approval|refused|plan_feedback|unprompted|ended|setup`: a
///   call on the page the app opens on (`-openSession`, `-openAssistant`), in that state, with no socket and no
///   microphone: a fake socket hands the engine the frames that state comes from, and a fake audio plays nothing.
///   `unprompted` is an instruction the user-speech gate held back, with Send Anyway (#5904). `setup` is no call: what
///   the 「+」 menu's Voice Chat says while the voice service is not set up.
/// - `-openVoice YES`: the real path, as a tap on the voice button, once the page offers a call (against the mock hub's
///   `MOCK_VOICE=1`, scripts/mock/voice_mock.py). `-openVoice silent`: the same with no microphone and no speaker (the
///   frames, the bar and the thread are real; the audio says it plays while the voice's PCM comes): a simulator whose
///   Mac gives it no audio input aborts in `AVAudioEngine.inputNode` (docs/lessons/voice.md).
/// Once per launch, on the first composer that is still there after two seconds.
enum VoiceSample: String, CaseIterable {
    case listening, speaking, working, paused, asking, approval, refused
    case planFeedback = "plan_feedback"
    case unprompted
    case ended, setup

    enum Request: Equatable {
        case sample(VoiceSample)
        case open
    }

    static var request: Request? {
        if let raw = UserDefaults.standard.string(forKey: "showVoiceSample"), let sample = VoiceSample(rawValue: raw) { return .sample(sample) }
        return UserDefaults.standard.bool(forKey: "openVoice") || silentAudio ? .open : nil
    }

    /// `-openVoice silent`: calls get `SilentAudio` instead of the microphone and the speaker.
    static var silentAudio: Bool { UserDefaults.standard.string(forKey: "openVoice") == "silent" }

    @MainActor private static var claimed = false

    /// The first page to ask takes it.
    @MainActor static func claim() -> Bool {
        guard !claimed else { return false }
        claimed = true
        return true
    }

    /// What Voice Chat says while the service is not set up (`setup`).
    static var setupNote: VoiceUnavailableNote? { VoiceUnavailableNote.make(.needsSetup([]), extensionID: "voice") }

    @MainActor static func start(_ sample: VoiceSample, key: SessionKey, model: AppModel) {
        let main = model.session(key)?.assistant == true
        let label = VoiceEntry.label(model.session(key), model: model)
        Task {
            await VoiceCalls.shared.start(key, extensionID: "voice", surface: .phone,
                                          deps: deps(sample, frames: frames(sample, key: key, main: main, label: label)))
        }
    }

    /// The cards a sample puts on the page (ChatView's sample cards): the question the voice asks, the permission and the
    /// plan it points to.
    static func cards(_ key: SessionKey) -> [Interaction] {
        guard case .sample(let sample)? = request else { return [] }
        switch sample {
        case .asking: return [question(key)].compactMap { $0 }
        case .approval: return [Interaction.samplePermission(key)].compactMap { $0 }
        case .planFeedback: return [plan(key)].compactMap { $0 }
        default: return []
        }
    }

    // MARK: Fakes

    @MainActor private static func deps(_ sample: VoiceSample, frames: [String]) -> VoiceDeps {
        var offline = APIClient(config: ServerConfig(url: URL(string: "https://sample.invalid")!, token: ""))
        offline.transport = { _ in throw APIError(status: 404, message: "sample") }
        return VoiceDeps(api: { offline }, socket: { _, _ in SampleSocket(frames: frames) }, audio: { SampleAudio(sample: sample) },
                         mic: SampleMic(), microphone: RecordingMicrophone(), idleTimer: SampleIdleTimer(), clock: SystemVoiceClock(),
                         session: { AppModel.shared.latest($0) }, voice: { _, _ in nil }, speechGate: { _, _ in true })
    }

    private static func json(_ object: [String: Any]) -> String {
        guard let data = try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]) else { return "{}" }
        return String(decoding: data, as: UTF8.self)
    }

    private static func caption(_ id: String, _ role: String, _ text: String, final: Bool = true) -> String {
        json(["type": "transcript", "id": id, "role": role, "text": text, "final": final])
    }

    private static func run(_ id: String, _ task: String, _ state: String, started: TimeInterval = 150, extra: [String: Any] = [:]) -> String {
        let at = ISO8601DateFormatter().string(from: .now.addingTimeInterval(-started))
        return json(["type": "run", "id": id, "task": task, "state": state, "msg_ids": [id], "base_seq": 812, "started_at": at]
            .merging(extra) { $1 })
    }

    private static func frames(_ sample: VoiceSample, key: SessionKey, main: Bool, label: String) -> [String] {
        let ready = json(["type": "ready", "protocol": 2, "features": ["target", "runs", "asks", "resume", "suspend", "speech_gate"],
                          "input_rate": 16000, "output_rate": 24000,
                          "target": ["machine_id": key.machineID, "session_id": key.sessionID, "kind": main ? "main" : "agent",
                                     "agent_label": main ? "" : "Claude Code"]])
        let asked = main ? "查一下明天下午有什么会" : "帮我把登录页那个失败的测试修一下"
        let task = main ? "查明天下午的日程" : "修登录页失败的测试"
        var out = [ready, caption("u1", "user", asked), caption("a1", "assistant", main ? "好，这就去查。" : "好，交给 \(label) 了。")]
        let working = run("voice-7c1e4a90b2d35f68", task, "working")
        switch sample {
        case .listening, .setup, .ended:
            break
        case .speaking:
            out += [run("voice-7c1e4a90b2d35f68", task, "done"),
                    caption("a2", "assistant", main ? "明天下午有两个会：三点的周会，五点和设计组对图标。" : "登录页的测试修好了，改了 Login.tsx 和 session.go 两处", final: false)]
        case .working:
            out += [working]
        case .paused:
            out += [working, json(["type": "link", "state": "paused"])]
        case .asking:
            out += [working, caption("a2", "assistant", "\(label) 问：数据库用哪个？Postgres，还是 SQLite？"),
                    json(["type": "ask", "interaction_id": "sample-voice-q", "state": "asking"])]
        case .approval:
            out += [working, caption("a2", "assistant", "\(label) 要运行一条命令，在屏幕上批准一下。")]
        case .refused:
            out += [caption("u2", "user", "顺便把 lint 也修了"),
                    caption("a2", "assistant", "\(label) 的输入框里有电脑上没发出去的字，要清空再发吗？"),
                    run("voice-2b8d0e6f4a1c9375", "顺便把 lint 也修了", "refused",
                        extra: ["code": "input_draft", "message": "input box has a draft",
                                "draft": "[⧉ Selected 3 lines from foo.go] fix it", "clearable": "fix it"])]
        case .planFeedback:
            out += [caption("u2", "user", "第三步先别做"),
                    caption("a2", "assistant", "\(label) 写好了计划，在屏幕上看一下。"),
                    run("voice-5e3f1a7c9b2d4068", "第三步先别做", "refused", extra: ["code": "dialog_open", "message": "a dialog is open"])]
        case .unprompted:
            // The voice handed something over after an agent's words were read out, with nothing heard from the user since.
            out += [run("voice-7c1e4a90b2d35f68", task, "done"),
                    caption("a2", "assistant", main ? "明天下午有两个会。" : "登录页的测试修好了。"),
                    run("voice-9a4c2e7b1d3f5086", main ? "把会议改到周五" : "顺便发布新版本", "refused", extra: ["code": "unprompted", "resendable": true]),
                    caption("a3", "assistant", "刚才没听到你新的请求，为了安全先没发；要做的话再说一遍，或者点屏幕上的「仍然发送」。")]
        }
        return out
    }

    private static func question(_ key: SessionKey) -> Interaction? {
        let payload: [String: Any] = [
            "id": "sample-voice-q", "machine_id": key.machineID, "session_id": key.sessionID, "kind": "question",
            "tool_name": "AskUserQuestion",
            "questions": [["question": "数据库用哪个？", "header": "数据库", "multi_select": false, "options": [
                ["label": "Postgres (Recommended)", "description": "和线上一样，迁移脚本不用改"],
                ["label": "SQLite", "description": "本地开发更轻，不用起容器"]]]],
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: payload) else { return nil }
        return try? JSON.decoder.decode(Interaction.self, from: data)
    }

    private static func plan(_ key: SessionKey) -> Interaction? {
        let payload: [String: Any] = [
            "id": "sample-voice-plan", "machine_id": key.machineID, "session_id": key.sessionID, "kind": "plan",
            "tool_name": "ExitPlanMode",
            "plan": "## 修登录页失败的测试\n\n1. 复现失败的用例\n2. 修 Login.tsx 的表单校验\n3. 顺手把 session.go 的过期时间改成 30 分钟\n4. 跑一遍全部测试",
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: payload) else { return nil }
        return try? JSON.decoder.decode(Interaction.self, from: data)
    }

    /// Hands the engine its frames one by one, as an extension would; Interrupt Voice is answered with `clear`.
    private final class SampleSocket: VoiceSocket, @unchecked Sendable {
        private let frames: [String]
        private let lock = NSLock()
        private var handler: (@MainActor @Sendable (VoiceSocketEvent) -> Void)?

        init(frames: [String]) { self.frames = frames }

        func open(_ handler: @escaping @MainActor @Sendable (VoiceSocketEvent) -> Void) {
            lock.withLock { self.handler = handler }
            let frames = frames
            Task { @MainActor [weak self] in
                for frame in frames {
                    try? await Task.sleep(for: .milliseconds(120))
                    guard let handler = self?.lock.withLock({ self?.handler }) else { return }
                    handler(.text(frame))
                }
            }
        }

        func send(text: String) {
            guard text.contains(#""interrupt""#), let handler = lock.withLock({ handler }) else { return }
            Task { @MainActor in handler(.text(#"{"type":"clear"}"#)) }
        }

        func send(data: Data) {}

        func close() { lock.withLock { handler = nil } }
    }

    /// Plays nothing; for `speaking` it says the voice plays and how loud, for `ended` that a phone call took the audio.
    @MainActor private final class SampleAudio: VoiceAudio {
        let sample: VoiceSample
        private var task: Task<Void, Never>?

        init(sample: VoiceSample) { self.sample = sample }

        func start(_ format: VoiceAudioFormat, uplink: VoiceUplink, events: @escaping @MainActor (VoiceAudioEvent) -> Void) throws {
            let sample = sample
            task = Task { @MainActor in
                try? await Task.sleep(for: .seconds(1.2))
                guard !Task.isCancelled else { return }
                switch sample {
                case .ended:
                    events(.interrupted)
                case .speaking:
                    events(.playing(true))
                    var t = 0.0
                    while !Task.isCancelled {
                        events(.outputLevel(Float(0.5 + 0.4 * sin(t * 5.3) * sin(t * 1.7))))
                        try? await Task.sleep(for: .milliseconds(50))
                        t += 0.05
                    }
                default:
                    break
                }
            }
        }

        func reconfigure(_ format: VoiceAudioFormat) throws {}
        func play(_ pcm: Data) {}
        func cue(_ pcm: Data) {}
        func clear() {}
        func stop() { task?.cancel() }
        var cancelsEcho: Bool { true }
    }

    /// No microphone (it sends nothing up; the extension hears silence) and no speaker: the voice's PCM is counted, and
    /// it says it plays for as long as that PCM would sound, so the bar's Speaking and Interrupt Voice work as with a speaker.
    @MainActor final class SilentAudio: VoiceAudio {
        private var events: (@MainActor (VoiceAudioEvent) -> Void)?
        private var rate = VoiceFrame.defaultOutputRate
        private var until = Date.distantPast
        private var quiet: Task<Void, Never>?

        func start(_ format: VoiceAudioFormat, uplink: VoiceUplink, events: @escaping @MainActor (VoiceAudioEvent) -> Void) throws {
            rate = format.outputRate
            self.events = events
        }

        func reconfigure(_ format: VoiceAudioFormat) throws { rate = format.outputRate }

        /// Nothing goes up, so nothing of the voice comes back.
        var cancelsEcho: Bool { true }

        func play(_ pcm: Data) {
            let now = Date.now
            if until < now { events?(.playing(true)) }
            until = max(until, now).addingTimeInterval(Double(pcm.count / 2) / Double(max(rate, 1)))
            events?(.outputLevel(0.5))
            quiet?.cancel()
            let left = until.timeIntervalSince(now)
            quiet = Task { @MainActor [weak self] in
                try? await Task.sleep(for: .seconds(left))
                guard !Task.isCancelled, let self else { return }
                self.events?(.outputLevel(0))
                self.events?(.playing(false))
            }
        }

        func cue(_ pcm: Data) {}

        func clear() {
            quiet?.cancel()
            if until > .now { events?(.playing(false)) }
            until = .distantPast
        }

        func stop() {
            quiet?.cancel()
            events = nil
        }
    }

    private struct SampleMic: MicPermission {
        func request() async -> Bool { true }
    }

    private final class SampleIdleTimer: IdleTimerControl {
        var isDisabled = false
    }
}
#endif
