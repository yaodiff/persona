#if os(iOS)
import AVFoundation

/// Cleans and plays one Main Chat reply. A remote voice gets until the first sound to take over;
/// a stream lost after that plays out its queued audio without repeating the reply on the phone.
@MainActor final class ReplySpeechPlayback {
    typealias DeviceSpeech = (String, Double, String, @escaping @MainActor () -> Void) -> Void

    private let route: (String) async -> MeetingVoiceRoute?
    private let synthesize: (MeetingVoiceRoute, String, String) async throws -> VoiceSpeech
    private let makePlayer: () -> any MeetingVoicePlayback
    private let device: DeviceSpeech
    private let stopDevice: () -> Void
    private let activate: () async -> Bool
    private let deactivate: () -> Void
    private let within: Duration

    private var request: Task<Void, Never>?
    private var deadline: Task<Void, Never>?
    private var player: (any MeetingVoicePlayback)?
    private var sessionRequested = false
    private var started = false
    private var onDevice = false
    private var generation = 0

    convenience init() {
        let session = ReplySpeechSession()
        let device = MeetingDeviceVoice()
        self.init(route: { engine in
            guard let (api, group, probe) = await Self.probe() else { return nil }
            return await MeetingVoiceRoutes.shared.replyRoute(engine, api: api, group: group, probe: probe)
        }, synthesize: { try await $0.synthesize($1, lang: $2) }, player: { MeetingVoicePlayer() },
        device: { text, rate, id, ended in
            let preferred = id.isEmpty ? nil : AVSpeechSynthesisVoice(identifier: id)
            let voice = preferred ?? MeetingSpeaker.voice(lang: nil, text: text, fallback: LanguageStore.shared.launched)
            device.speak(text, voice: voice, rate: rate, ended: ended)
        }, stopDevice: { device.stop() }, activate: { await session.activate() }, deactivate: { session.deactivate() })
    }

    init(route: @escaping (String) async -> MeetingVoiceRoute?,
         synthesize: @escaping (MeetingVoiceRoute, String, String) async throws -> VoiceSpeech,
         player: @escaping () -> any MeetingVoicePlayback, device: @escaping DeviceSpeech, stopDevice: @escaping () -> Void,
         activate: @escaping () async -> Bool, deactivate: @escaping () -> Void, within: Duration = .seconds(8)) {
        self.route = route
        self.synthesize = synthesize
        self.makePlayer = player
        self.device = device
        self.stopDevice = stopDevice
        self.activate = activate
        self.deactivate = deactivate
        self.within = within
    }

    private static func probe() async -> (APIClient, String, VoiceSupport.Probe)? {
        guard let hub = AppModel.shared.hub else { return nil }
        let api = hub.api, group = hub.group
        await VoiceSupport.shared.refresh(group: group, api: api)
        return VoiceSupport.shared.probes[group].map { (api, group, $0) }
    }

    static func extensions() async -> [(id: String, name: String)] {
        guard let (api, group, probe) = await probe() else { return [] }
        var choices: [(id: String, name: String)] = []
        for ext in probe.extensions {
            if await MeetingVoiceRoutes.shared.readySpeech(ext, api: api, group: group) != nil {
                choices.append((ext.id, ext.name))
            }
        }
        return choices
    }

    private static let truncated = String(localized: "The rest is on screen.")
    private static let omitted = String(localized: "Part omitted.")

    @concurrent private static func spokenText(_ body: String, full: Bool, truncated: String, omitted: String) async -> String {
        ReplySpeechText.clean(body, full: full, truncated: truncated, omitted: omitted)
    }

    func speak(_ body: String, engine: String, rate: Double, voice: String, full: Bool = false) {
        stop()
        guard !body.isEmpty else { return }
        let run = generation
        request = Task { [weak self] in
            guard !Task.isCancelled else { return }
            let text = await Self.spokenText(body, full: full, truncated: Self.truncated, omitted: Self.omitted)
            guard let self, generation == run, !text.isEmpty else { return }
            let fallback: () -> Void = { [weak self] in self?.sayOnDevice(text, rate: rate, voice: voice, run: run) }
            sessionRequested = true
            let active = await activate()
            guard generation == run else { return }
            guard active else { return stop() }
            if engine == "device" {
                fallback()
                return
            }
            deadline = Task { [within] in
                guard (try? await Task.sleep(for: within)) != nil else { return }
                fallback()
            }
            guard let selected = await route(engine) else { fallback(); return }
            guard !Task.isCancelled else { return }
            do {
                try await playRemote(selected, text: text, run: run, fallback: fallback)
                try Task.checkCancellation()
                if let player { player.finish() } else { fallback() }
            } catch {
                guard !Task.isCancelled else { return }
                if started { player?.finish() } else { fallback() }
            }
        }
    }

    /// Both built-in speak endpoints accept at most 1,000 Unicode scalars per request. Prefer a word boundary;
    /// the whole reply shares a player so that no segment can restart speech after the first audible sample.
    nonisolated private static func remoteParts(_ text: String) -> [String] {
        var rest = text.unicodeScalars[...]
        var parts: [String] = []
        while let limit = rest.index(rest.startIndex, offsetBy: 1000, limitedBy: rest.endIndex), limit < rest.endIndex {
            let prefix = rest[..<limit]
            let boundary = prefix.lastIndex(where: { $0.properties.isWhitespace }).map { rest.index(after: $0) } ?? limit
            parts.append(String(rest[..<boundary]))
            rest = rest[boundary...]
        }
        if !rest.isEmpty { parts.append(String(rest)) }
        return parts.filter { !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    }

    private func playRemote(_ route: MeetingVoiceRoute, text: String, run: Int, fallback: @escaping () -> Void) async throws {
        let lang = MeetingSpeaker.language(of: text, fallback: LanguageStore.shared.launched)
        var sampleRate: Int?
        for part in Self.remoteParts(text) {
            try Task.checkCancellation()
            let speech = try await synthesize(route, part, lang)
            if let sampleRate, sampleRate != speech.rate { throw URLError(.cannotDecodeContentData) }
            var bytes = 0
            for try await chunk in speech.audio {
                try Task.checkCancellation()
                guard !chunk.isEmpty else { continue }
                if player == nil {
                    let next = makePlayer()
                    try next.start(rate: speech.rate, started: { [weak self] in
                        guard let self, generation == run, !onDevice else { return }
                        started = true
                        deadline?.cancel()
                    }, ended: { [weak self] in
                        guard let self, generation == run, !onDevice else { return }
                        if started { stop() } else { fallback() }
                    })
                    player = next
                    sampleRate = speech.rate
                }
                bytes += chunk.count
                player?.play(chunk)
            }
            guard bytes > 0, bytes.isMultiple(of: 2) else { throw URLError(.cannotDecodeContentData) }
        }
    }

    private func sayOnDevice(_ text: String, rate: Double, voice: String, run: Int) {
        guard generation == run, !onDevice, !started else { return }
        onDevice = true
        request?.cancel()
        deadline?.cancel()
        player?.stop()
        player = nil
        device(text, min(2, max(0.5, rate)), voice) { [weak self] in
            guard let self, generation == run else { return }
            stop()
        }
    }

    func stop() {
        generation += 1
        request?.cancel()
        request = nil
        deadline?.cancel()
        deadline = nil
        player?.stop()
        player = nil
        if onDevice { stopDevice() }
        if sessionRequested { deactivate() }
        sessionRequested = false
        onDevice = false
        started = false
    }
}

extension MeetingVoiceRoutes {
    func replyRoute(_ engine: String, api: APIClient, group: String, probe: VoiceSupport.Probe) async -> MeetingVoiceRoute? {
        for ext in probe.extensions where engine == "auto" || engine == "extension:" + ext.id {
            if let route = await readySpeech(ext, api: api, group: group) { return route }
        }
        guard engine == "auto" else { return nil }
        return await realtimeRoute(api: api, group: group, probe: probe)
    }

    fileprivate func readySpeech(_ ext: HubExtension, api: APIClient, group: String) async -> MeetingVoiceRoute? {
        guard ext.status == "running", VoiceAvailability.validID(ext.id),
              let contributions = ext.contributes?[SpeechInfo.point] else { return nil }
        for id in contributions where VoiceAvailability.validID(id) {
            if let route = await speechRoute(api: api, group: group, extensionID: ext.id, contribution: id) { return route }
        }
        return nil
    }
}

/// The lease is touched only on the call/dictation audio-session queue. An old release is ordered
/// before the next activation, and a recording or call that took the session keeps it.
nonisolated final class ReplySpeechSession: @unchecked Sendable {
    private let broker: AudioSessionBroker
    private var lease: AudioSessionBroker.Lease?

    init(broker: AudioSessionBroker = .shared) {
        self.broker = broker
    }

    @MainActor func activate() async -> Bool {
        guard RecordingMicrophone.shared.isFree else { return false }
        return await withCheckedContinuation { done in
            LiveCallGate.sessionQueue.async { [broker] in
                guard !LiveCallGate.liveNow, broker.highestActiveKind == nil else { return done.resume(returning: false) }
                do {
                    try broker.setCategory(.playback, mode: .spokenAudio)
                    try broker.setActive(true)
                    self.lease = broker.acquire(id: "assistant.reply-speech", kind: .playback)
                    done.resume(returning: true)
                } catch {
                    done.resume(returning: false)
                }
            }
        }
    }

    func deactivate() {
        LiveCallGate.sessionQueue.async { [broker] in
            guard let lease = self.lease else { return }
            lease.release()
            self.lease = nil
            // Dictation can already have opened its record category on this queue while the main actor
            // is still starting the input engine and has not registered its lease.
            guard !LiveCallGate.liveNow, broker.highestActiveKind == nil, broker.category == .playback else { return }
            try? broker.setActive(false, options: .notifyOthersOnDeactivation)
        }
    }
}
#endif
