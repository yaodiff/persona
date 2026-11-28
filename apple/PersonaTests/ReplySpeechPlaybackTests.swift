import AVFoundation
import Foundation
import os
import PersonaWire
import Testing
@testable import Persona

@MainActor struct ReplySpeechPlaybackTests {
    @MainActor private final class World {
        let player = BufferedPlayer()
        var playerCount = 0
        var settings: [(String, Double, String)] = []
        var asked: [String] = []
        var synthesized: [(contribution: String?, text: String)] = []
        var audio: AsyncThrowingStream<Data, Error>.Continuation?
        var stoppedDevice = false
        var closes = 0

        func make(within: Duration = .seconds(8), session: ReplySpeechSession? = nil) -> ReplySpeechPlayback {
            ReplySpeechPlayback(route: { engine in
                self.asked.append(engine)
                return engine == "missing" ? nil : MeetingVoiceRoute(api: nil, extensionID: "example", speech: engine == "extension:example" ? "answer" : nil)
            }, synthesize: { route, text, _ in
                self.synthesized.append((route.speech, text))
                let (audio, continuation) = AsyncThrowingStream<Data, Error>.makeStream()
                self.audio = continuation
                return VoiceSpeech(rate: 24000, audio: audio)
            }, player: { self.playerCount += 1; return self.player }, device: { text, rate, voice, _ in
                self.settings.append((text, rate, voice))
            }, stopDevice: { self.stoppedDevice = true },
            activate: {
                if let session { return await session.activate() }
                return true
            }, deactivate: { session?.deactivate(); self.closes += 1 }, within: within)
        }
    }

    private func until(_ done: () -> Bool, at location: SourceLocation = #_sourceLocation) async throws {
        let deadline = ContinuousClock.now + .seconds(3)
        while !done(), ContinuousClock.now < deadline { try await Task.sleep(for: .milliseconds(5)) }
        #expect(done(), sourceLocation: location)
    }

    @Test func automaticPrefersReadySpeechThenRealtimeAndExplicitOnlyUsesItsOwnExtension() async throws {
        let hub = FakeHub()
        let paths = [("first", "off"), ("second", ""), ("third", "")]
        for (id, setup) in paths {
            hub.on("GET", "/api/v1/extensions/\(id)/speech/answer/info") { _ in
                Data(#"{"protocol":1,"setup":"\#(setup)"}"#.utf8)
            }
        }
        var api = hub.client
        api.extensionsAPI = true
        let extensions = try JSON.decoder.decode([HubExtension].self, from: Data(#"""
        [{"id":"first","name":"First","kind":"ui","status":"running","contributes":{"speech":["answer"]}},
         {"id":"second","name":"Second","kind":"ui","status":"running","contributes":{"speech":["answer"]}},
         {"id":"third","name":"Third","kind":"ui","status":"running","contributes":{"speech":["answer"]}},
         {"id":"voice","name":"Voice","kind":"voice","status":"running","contributes":{"voice":["voice"]}}]
        """#.utf8))
        let routes = MeetingVoiceRoutes()
        func route(_ engine: String, _ list: [HubExtension], ready: Bool = true) async -> MeetingVoiceRoute? {
            let info = VoiceInfo(protocolVersion: 2, features: ready ? ["speak"] : [])
            return await routes.replyRoute(engine, api: api, group: "test", probe: .init(extensions: list, info: .info(info)))
        }
        #expect(await route("auto", extensions)?.extensionID == "second")
        #expect(await route("extension:third", extensions)?.extensionID == "third")
        #expect(await route("extension:first", extensions) == nil)
        let withoutReadySpeech = extensions.filter { ["first", "voice"].contains($0.id) }
        #expect(await route("auto", withoutReadySpeech)?.extensionID == "voice")
        #expect(await route("auto", withoutReadySpeech, ready: false) == nil)
        #expect(await route("auto", []) == nil)
    }

    @Test func deviceOnlyAndUnavailableExtensionUseClampedDeviceSettings() async throws {
        let world = World()
        let speaker = world.make()
        speaker.speak("One.", engine: "device", rate: 3, voice: "chosen")
        try await until { world.settings.count == 1 }
        #expect(world.asked.isEmpty)
        #expect(world.settings[0].1 == 2 && world.settings[0].2 == "chosen")
        speaker.speak("Two.", engine: "missing", rate: 0.1, voice: "")
        try await until { world.settings.count == 2 }
        #expect(world.settings[1].0 == "Two." && world.settings[1].1 == 0.5)
        #expect(world.stoppedDevice)
        speaker.stop()
        #expect(world.closes == 2)
    }

    @Test(arguments: ["auto", "extension:example"]) func bothRemoteVoicesReceiveOnlyCleanedText(_ engine: String) async throws {
        let world = World()
        let speaker = world.make()
        speaker.speak("Open /private/notes/report.txt.\nPassword: hidden-example-value\nDone.", engine: engine, rate: 1, voice: "")
        try await until { !world.synthesized.isEmpty }
        let received = try #require(world.synthesized.first)
        #expect(received.contribution == (engine == "auto" ? nil : "answer"))
        #expect(received.text.contains("report.txt") && received.text.contains("Done."))
        #expect(!received.text.contains("hidden-example-value") && !received.text.contains("/private/notes"))
        speaker.stop()
    }

    @Test func failureBeforeAudioFallsBackOnce() async throws {
        let world = World()
        let speaker = world.make()
        speaker.speak("Reply.", engine: "auto", rate: 1, voice: "")
        try await until { world.audio != nil }
        world.audio?.finish(throwing: URLError(.networkConnectionLost))
        try await until { world.settings.count == 1 }
        #expect(world.settings.first?.0 == "Reply." && world.player.pieces.isEmpty)
        speaker.stop()
    }

    @Test func timeoutDiscardsLateAudio() async throws {
        let world = World()
        let speaker = world.make(within: .milliseconds(50))
        speaker.speak("Reply.", engine: "auto", rate: 1, voice: "")
        try await until { world.settings.count == 1 }
        #expect(world.audio?.yield(Data([0, 0])).isTerminated == true)
        #expect(world.player.pieces.isEmpty)
        speaker.stop()
    }

    @Test func streamLostAfterPlaybackNeverRestarts() async throws {
        let world = World()
        let speaker = world.make()
        speaker.speak("Reply.", engine: "auto", rate: 1.5, voice: "")
        try await until { world.audio != nil }
        world.audio?.yield(Data([0, 0]))
        try await until { world.player.pieces.count == 1 }
        world.player.begin()
        world.audio?.finish(throwing: URLError(.networkConnectionLost))
        try await until { world.player.finishing }
        world.player.playOut()
        #expect(world.settings.isEmpty && world.closes == 1)
    }

    @Test(arguments: ["auto", "extension:example"]) func longReplyUsesOnePlayerAcrossBoundedRequests(_ engine: String) async throws {
        let world = World()
        let speaker = world.make()
        // Two scalars in each flag exercise the services' rune limit rather than the UI's character limit.
        let body = String(repeating: "🇨🇦", count: 1100)
        speaker.speak(body, engine: engine, rate: 1, voice: "", full: true)
        for part in 1...3 {
            try await until { world.synthesized.count == part }
            world.audio?.yield(Data([UInt8(part), 0]))
            try await until { world.player.pieces.count == part }
            if part == 1 { world.player.begin() }
            #expect(!world.player.finishing)
            world.audio?.finish()
        }
        try await until { world.player.finishing }
        #expect(world.synthesized.map(\.text).joined() == body)
        #expect(world.synthesized.allSatisfy { $0.text.unicodeScalars.count <= 1000 })
        #expect(world.playerCount == 1 && world.settings.isEmpty)
        world.player.playOut()
    }

    @Test(arguments: [false, true]) func failureInLaterPartFallsBackOnlyBeforeAudiblePlayback(_ audible: Bool) async throws {
        let world = World()
        let speaker = world.make()
        let body = String(repeating: "A reply. ", count: 160).trimmingCharacters(in: .whitespaces)
        speaker.speak(body, engine: "auto", rate: 1, voice: "", full: true)
        try await until { world.synthesized.count == 1 }
        world.audio?.yield(Data([0, 0]))
        try await until { world.player.pieces.count == 1 }
        if audible { world.player.begin() }
        world.audio?.finish()
        try await until { world.synthesized.count == 2 }
        world.audio?.finish(throwing: URLError(.networkConnectionLost))
        if audible {
            try await until { world.player.finishing }
            #expect(world.settings.isEmpty)
        } else {
            try await until { world.settings.count == 1 }
            #expect(world.settings.first?.0 == body && world.player.stopped)
        }
        speaker.stop()
    }

    @Test func dictationTakesSessionBeforeItsLeaseIsRegistered() async throws {
        let category = OSAllocatedUnfairLock(initialState: AVAudioSession.Category.ambient)
        let activations = OSAllocatedUnfairLock(initialState: [Bool]())
        var backend = AudioSessionBroker.Backend.system
        backend.setCategory = { value, _, _ in category.withLock { $0 = value } }
        backend.setActive = { value, _ in activations.withLock { $0.append(value) } }
        backend.category = { category.withLock { $0 } }
        let broker = AudioSessionBroker(backend: backend)
        let world = World()
        let speaker = world.make(session: ReplySpeechSession(broker: broker))
        speaker.speak("Reply.", engine: "auto", rate: 1, voice: "")
        try await until { world.audio != nil }
        world.audio?.yield(Data([0, 0]))
        try await until { world.player.pieces.count == 1 }
        try LiveCallGate.sessionQueue.sync {
            try broker.setCategory(.record, mode: .measurement)
            try broker.setActive(true)
        }
        speaker.stop()
        LiveCallGate.sessionQueue.sync {} // Wait for the reply's release on this same queue.
        #expect(broker.highestActiveKind == nil)
        #expect(activations.withLock { $0 } == [true, true])
        #expect(world.player.stopped && world.settings.isEmpty)
        #expect(world.audio?.yield(Data([0, 0])).isTerminated == true)
    }

    @Test func stopCutsBufferedAudioAndCancelsPendingSynthesis() async throws {
        let world = World()
        let speaker = world.make()
        for play in [false, true] {
            world.audio = nil
            speaker.speak("Reply.", engine: "auto", rate: 1, voice: "")
            try await until { world.audio != nil }
            if play {
                world.audio?.yield(Data([0, 0]))
                try await until { world.player.pieces.count == 1 }
            }
            speaker.stop()
            #expect(world.audio?.yield(Data([0, 0])).isTerminated == true)
            if play { #expect(world.player.stopped) }
        }
        #expect(world.settings.isEmpty && world.closes == 2)
    }
}

/// The real PCM player buffers a lead before its first sound. Tests advance the two events separately.
@MainActor private final class BufferedPlayer: MeetingVoicePlayback {
    var pieces: [Data] = []
    var finishing = false
    var stopped = false
    private var started: (() -> Void)?
    private var ended: (() -> Void)?

    func start(rate: Int, started: @escaping @MainActor () -> Void, ended: @escaping @MainActor () -> Void) throws {
        self.started = started
        self.ended = ended
        stopped = false
    }

    func play(_ pcm: Data) { pieces.append(pcm) }

    func begin() {
        started?()
        started = nil
    }

    func finish() { finishing = true }

    func stop() {
        stopped = true
        started = nil
        ended = nil
    }

    func playOut() { ended?() }
}

private extension AsyncThrowingStream<Data, Error>.Continuation.YieldResult {
    var isTerminated: Bool {
        if case .terminated = self { return true }
        return false
    }
}
