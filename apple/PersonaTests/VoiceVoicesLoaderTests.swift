import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The voice pickers' list (#2411, docs/assistant/voice.md 5.7): loading, the failure and its retries (by themselves a
/// little later, when a picker opens, on Retry), a stale list, the current voice's name, where the settings row shows.
@MainActor
struct VoiceVoicesLoaderTests {
    static let path = "/api/v1/extensions/voice/voices"
    static let list = #"{"voices":[{"id":"marin","name":"Marin"},{"id":"cedar","name":"Cedar"}],"default":"marin"}"#
    static let staleList = #"{"voices":[{"id":"marin","name":"Marin"}],"default":"marin","stale":true}"#

    final class Box<T> {
        var value: T
        init(_ value: T) { self.value = value }
    }

    private func waitUntil(_ what: () -> Bool) async {
        let deadline = Date.now + 5
        while !what(), Date.now < deadline { try? await Task.sleep(for: .milliseconds(10)) }
    }

    private func rig(_ answer: @escaping FakeHub.Route) -> (VoiceVoicesLoader, FakeHub, FakeVoiceClock) {
        let hub = FakeHub()
        hub.on("GET", Self.path, answer)
        var api = hub.client
        api.extensionsAPI = true
        let clock = FakeVoiceClock()
        return (VoiceVoicesLoader(extensionID: "voice", api: { api }, clock: clock), hub, clock)
    }

    private static let failure = APIError(status: 502, message: "plugin voice is not answering")

    @Test func showAsksForTheModelsListOnce() async {
        let (loader, hub, _) = rig { _ in Data(Self.list.utf8) }
        #expect(loader.state == .idle)
        loader.show(model: "custom/m1")
        #expect(loader.state == .loading)
        await waitUntil { loader.voices != nil }
        #expect(loader.voices?.voices.map(\.id) == ["marin", "cedar"])
        #expect(hub.sent("GET", Self.path).first?.query == ["model": "custom/m1"])
        loader.show(model: "custom/m1")
        loader.opened()
        #expect(hub.sent("GET", Self.path).count == 1, "a list it has is not asked for again")
        loader.show(model: "custom/m2")
        #expect(loader.state == .loading, "another model's list is not this one's")
        await waitUntil { loader.voices != nil }
        #expect(hub.sent("GET", Self.path).last?.query == ["model": "custom/m2"])
    }

    @Test func aFailureIsAskedAgainAfterTwoFiveAndFifteenSecondsThenWhenThePickerOpens() async {
        let failing = Box(true)
        let (loader, hub, clock) = rig { _ in
            if failing.value { throw Self.failure }
            return Data(Self.list.utf8)
        }
        let asked = { hub.sent("GET", Self.path).count }
        loader.show(model: nil)
        await waitUntil { loader.state == .failed }
        #expect(asked() == 1)
        #expect((hub.sent("GET", Self.path).first?.query["model"] ?? "").isEmpty, "nil: the extension's default model")
        for (i, delay) in [2.0, 5.0, 15.0].enumerated() {
            await waitUntil { clock.sleeping == 1 }
            clock.advance(delay - 0.5)
            try? await Task.sleep(for: .milliseconds(30))
            #expect(asked() == i + 1, "not before \(delay) s")
            clock.advance(0.5)
            await waitUntil { asked() == i + 2 && loader.state == .failed }
        }
        await waitUntil { asked() == 4 && loader.state == .failed }
        clock.advance(60)
        try? await Task.sleep(for: .milliseconds(30))
        #expect(asked() == 4 && clock.sleeping == 0, "three tries by itself, no more")
        // The menu opens a minute later: asked at once.
        failing.value = false
        loader.opened()
        #expect(loader.state == .loading)
        await waitUntil { loader.voices != nil }
        #expect(asked() == 5)
    }

    @Test func retryAsksAtOnceButOpeningRightAfterAnAskDoesNot() async {
        let (loader, hub, clock) = rig { _ in throw Self.failure }
        let asked = { hub.sent("GET", Self.path).count }
        loader.show(model: nil)
        await waitUntil { loader.state == .failed }
        loader.opened()
        clock.advance(1)
        loader.opened()
        #expect(asked() == 1, "a menu drawn again as the state changes does not ask in a loop")
        loader.retry()
        #expect(loader.state == .loading)
        await waitUntil { asked() == 2 && loader.state == .failed }
        // Retry started the tries over: the next is 2 s after this failure.
        await waitUntil { clock.sleeping == 1 }
        clock.advance(2)
        await waitUntil { asked() == 3 }
    }

    @Test func aStaleListShowsAndIsAskedForAgainWhenThePickerOpens() async {
        let answers = Box([Self.staleList, "", Self.list])
        let (loader, hub, clock) = rig { _ in
            let next = answers.value.removeFirst()
            if next.isEmpty { throw Self.failure }
            return Data(next.utf8)
        }
        loader.show(model: nil)
        await waitUntil { loader.voices != nil }
        #expect(loader.voices?.stale == true)
        clock.advance(3)
        loader.opened()
        #expect(loader.voices?.stale == true, "it stays on screen while asked for again")
        await waitUntil { hub.sent("GET", Self.path).count == 2 }
        try? await Task.sleep(for: .milliseconds(30))
        #expect(loader.voices?.stale == true && clock.sleeping == 0, "a failure keeps the stale list and waits for the next open")
        clock.advance(3)
        loader.opened()
        await waitUntil { loader.voices?.stale == false }
        #expect(loader.voices?.voices.count == 2)
        loader.opened()
        #expect(hub.sent("GET", Self.path).count == 3)
    }

    @Test func stopDropsALateAnswerAndShowAsksAgain() async {
        let (loader, hub, _) = rig { _ in Data(Self.list.utf8) }
        loader.show(model: nil)
        loader.stop()
        #expect(loader.state == .idle)
        try? await Task.sleep(for: .milliseconds(50))
        #expect(loader.voices == nil)
        loader.show(model: nil)
        await waitUntil { loader.voices != nil }
        #expect(hub.sent("GET", Self.path).count >= 1)
    }

    @Test func theStaleMarkIsRead() throws {
        #expect(try JSON.decoder.decode(VoiceVoices.self, from: Data(Self.staleList.utf8)).stale)
        #expect(try !JSON.decoder.decode(VoiceVoices.self, from: Data(Self.list.utf8)).stale)
    }

    @Test func theCurrentVoiceIsNamed() {
        let list = VoiceVoices(voices: [.init(id: "marin", name: "Marin"), .init(id: "x")], default: "marin")
        #expect(VoiceTimbreName.current(chosen: nil, in: list) == "Marin", "none chosen: the model's default")
        #expect(VoiceTimbreName.current(chosen: "x", in: list) == "x", "no name: its id")
        #expect(VoiceTimbreName.current(chosen: "gone", in: list) == "gone", "another model's voice")
        #expect(VoiceTimbreName.current(chosen: nil, in: VoiceVoices()) == nil)
        #expect(VoiceTimbreName.current(chosen: nil, in: nil) == nil)
    }

    @Test func theSettingsRowIsOnTheRunningVoiceExtensionsPageOnly() throws {
        func ext(_ kind: String, _ status: String, enabled: Bool = true) throws -> HubExtension {
            try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"voice","name":"V","kind":"\#(kind)","status":"\#(status)","enabled":\#(enabled)}"#.utf8))
        }
        #expect(try VoiceTimbreSection.shows(ext("voice", "running"), manager: nil, platform: .phone))
        #expect(try !VoiceTimbreSection.shows(ext("voice", "running"), manager: nil, platform: .other), "no calls on the Mac")
        #expect(try !VoiceTimbreSection.shows(ext("voice", "running"), manager: "m2", platform: .phone), "another manager's copy")
        #expect(try !VoiceTimbreSection.shows(ext("voice", "needs_setup"), manager: nil, platform: .phone))
        #expect(try !VoiceTimbreSection.shows(ext("voice", "running", enabled: false), manager: nil, platform: .phone))
        #expect(try !VoiceTimbreSection.shows(ext("connectors", "running"), manager: nil, platform: .phone))
    }
}
