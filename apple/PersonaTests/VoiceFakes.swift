import Foundation
@testable import Persona
import PersonaWire

// Stand-ins for a voice call's socket, audio, permission, screen lock and clock (docs/assistant/voice.md 6.10), so
// VoiceConversation and VoiceCalls run their real logic with no network, microphone or waiting.

/// A clock that only moves when a test says so; `sleep` waits until it has moved far enough.
final class FakeVoiceClock: VoiceClock, @unchecked Sendable {
    private let lock = NSLock()
    private var current: Date
    private var sleepers: [(id: UUID, until: Date, wake: CheckedContinuation<Void, Error>)] = []

    init(_ start: Date = Date(timeIntervalSince1970: 1_800_000_000)) {
        current = start
    }

    var now: Date { lock.withLock { current } }

    /// How many are waiting on it.
    var sleeping: Int { lock.withLock { sleepers.count } }

    func sleep(for duration: Duration) async throws {
        let id = UUID()
        let seconds = Double(duration.components.seconds) + Double(duration.components.attoseconds) / 1e18
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (wake: CheckedContinuation<Void, Error>) in
                let cancelled = lock.withLock { () -> Bool in
                    if Task.isCancelled { return true }
                    sleepers.append((id, current.addingTimeInterval(seconds), wake))
                    return false
                }
                if cancelled { wake.resume(throwing: CancellationError()) }
            }
        } onCancel: {
            let wake = lock.withLock { () -> CheckedContinuation<Void, Error>? in
                guard let i = sleepers.firstIndex(where: { $0.id == id }) else { return nil }
                return sleepers.remove(at: i).wake
            }
            wake?.resume(throwing: CancellationError())
        }
    }

    /// Moves time on and wakes every sleeper that is due.
    func advance(_ seconds: TimeInterval) {
        let due = lock.withLock { () -> [CheckedContinuation<Void, Error>] in
            current = current.addingTimeInterval(seconds)
            let now = current
            let due = sleepers.filter { $0.until <= now }.sorted { $0.until < $1.until }
            sleepers.removeAll { $0.until <= now }
            return due.map(\.wake)
        }
        for wake in due { wake.resume() }
    }
}

/// The socket a call opened: what it sent, and the frames a test hands it.
final class FakeVoiceSocket: VoiceSocket, @unchecked Sendable {
    let request: URLRequest
    private let lock = NSLock()
    private var handler: (@MainActor @Sendable (VoiceSocketEvent) -> Void)?
    private var sentTexts: [String] = []
    private var sentAudio: [Data] = []
    private var closedHere = false

    init(_ request: URLRequest) {
        self.request = request
    }

    func open(_ handler: @escaping @MainActor @Sendable (VoiceSocketEvent) -> Void) {
        lock.withLock { self.handler = handler }
    }

    func send(text: String) { lock.withLock { sentTexts.append(text) } }

    func send(data: Data) { lock.withLock { sentAudio.append(data) } }

    func close() {
        lock.withLock {
            closedHere = true
            handler = nil
        }
    }

    var texts: [String] { lock.withLock { sentTexts } }
    var audio: [Data] { lock.withLock { sentAudio } }
    var closed: Bool { lock.withLock { closedHere } }

    /// The text frames it sent, decoded.
    var frames: [[String: Any]] {
        texts.compactMap { (try? JSONSerialization.jsonObject(with: Data($0.utf8))) as? [String: Any] }
    }

    var start: [String: Any]? { frames.first { $0["type"] as? String == "start" } }

    @MainActor func deliver(_ text: String) {
        lock.withLock { handler }?(.text(text))
    }

    @MainActor func deliver(data: Data) {
        lock.withLock { handler }?(.data(data))
    }

    /// The extension's `ready`, protocol 2, bound to `key`.
    @MainActor func ready(_ key: SessionKey, input: Int = 16000, output: Int = 24000, protocolVersion: Int = 2) {
        deliver(#"{"type":"ready","protocol":\#(protocolVersion),"features":["target","runs","asks","resume","suspend"],"input_rate":\#(input),"output_rate":\#(output),"target":{"machine_id":"\#(key.machineID)","session_id":"\#(key.sessionID)","kind":"agent","title":"web-api","agent_label":"Claude Code","machine_name":"Mac"}}"#)
    }

    /// The socket goes away from the other side (`status`: the handshake refused with it).
    @MainActor func drop(status: Int? = nil) {
        let handler = lock.withLock { () -> (@MainActor @Sendable (VoiceSocketEvent) -> Void)? in
            defer { self.handler = nil }
            return self.handler
        }
        handler?(.closed(VoiceSocketClose(status: status)))
    }
}

/// The call's audio without a microphone or speaker.
@MainActor final class FakeVoiceAudio: VoiceAudio {
    var failStart = false
    var cancelsEcho = true
    /// What a failing start throws.
    var startError = VoiceAudioError.engine
    private(set) var format: VoiceAudioFormat?
    private(set) var reconfigured: [VoiceAudioFormat] = []
    private(set) var played: [Data] = []
    private(set) var cues: [Data] = []
    private(set) var clears = 0
    private(set) var stops = 0
    private(set) var uplink: VoiceUplink?
    private var events: (@MainActor (VoiceAudioEvent) -> Void)?

    var running: Bool { uplink != nil }

    func start(_ format: VoiceAudioFormat, uplink: VoiceUplink, events: @escaping @MainActor (VoiceAudioEvent) -> Void) throws {
        if failStart { throw startError }
        self.format = format
        self.uplink = uplink
        self.events = events
    }

    func reconfigure(_ format: VoiceAudioFormat) throws {
        self.format = format
        reconfigured.append(format)
    }

    func play(_ pcm: Data) { played.append(pcm) }
    func cue(_ pcm: Data) { cues.append(pcm) }
    func clear() { clears += 1 }

    func stop() {
        stops += 1
        uplink = nil
        events = nil
    }

    /// What the microphone heard, at the rate the audio runs at.
    func capture(_ pcm: Data) {
        uplink?.push(pcm, rate: format?.inputRate ?? 16000)
    }

    func emit(_ event: VoiceAudioEvent) { events?(event) }
}

struct FakeMicPermission: MicPermission {
    var allowed = true
    func request() async -> Bool { allowed }
}

@MainActor final class FakeIdleTimer: IdleTimerControl {
    var isDisabled = false {
        didSet { changes.append(isDisabled) }
    }
    private(set) var changes: [Bool] = []
}

/// Everything a call needs, faked; `deps` hands them to a VoiceConversation or VoiceCalls.
@MainActor final class VoiceRig {
    let hub = FakeHub()
    let clock = FakeVoiceClock()
    let audio = FakeVoiceAudio()
    let idleTimer = FakeIdleTimer()
    let microphone = RecordingMicrophone()
    var allowed = true
    var session: Session?
    var voice: String? = "marin"
    private(set) var sockets: [FakeVoiceSocket] = []

    var socket: FakeVoiceSocket? { sockets.last }

    /// A group of its own per rig: the rates a call learns are remembered per group and extension.
    let key = SessionKey(group: "g-" + UUID().uuidString, machineID: "m1", sessionID: "s1")

    var api: APIClient {
        var c = hub.client
        c.extensionsAPI = true
        return c
    }

    var deps: VoiceDeps {
        VoiceDeps(api: { self.api },
                  socket: { request, _ in
                      let socket = FakeVoiceSocket(request)
                      self.sockets.append(socket)
                      return socket
                  },
                  audio: { self.audio },
                  mic: FakeMicPermission(allowed: allowed), microphone: microphone, idleTimer: idleTimer, clock: clock,
                  session: { _ in self.session }, voice: { _, _ in self.voice })
    }

    func call(_ key: SessionKey? = nil, surface: VoiceSurface = .phone, recent: [VoiceLine] = []) -> VoiceConversation {
        VoiceConversation(key: key ?? self.key, extensionID: "voice", surface: surface, recent: recent, deps: deps)
    }

    /// Waits (at most two seconds) for work the call started on its own tasks.
    func waitUntil(_ condition: () -> Bool) async {
        let deadline = Date.now.addingTimeInterval(2)
        while !condition(), Date.now < deadline { try? await Task.sleep(for: .milliseconds(5)) }
    }

    /// Moves the clock on once something sleeps on it, then lets the woken tasks run.
    func advance(_ seconds: TimeInterval, sleepers: Int = 1) async {
        await waitUntil { clock.sleeping >= sleepers }
        clock.advance(seconds)
        for _ in 0..<20 { await Task.yield() }
    }

    static func session(_ json: String) -> Session {
        try! JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }
}

extension Data {
    /// PCM16 of `count` samples, all `value`.
    static func pcm(_ count: Int, _ value: Int16 = 1000) -> Data {
        VoicePCM.data([Int16](repeating: value, count: count))
    }
}
