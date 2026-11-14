import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Pins and mutes (`PinsStore`): kept across launches, changed at once and sent to the hub, the mute rule.
@MainActor struct PinsStoreTests {
    private func store(_ hub: FakeHub, _ defaults: UserDefaults, errors: @escaping (Error) -> Void = { _ in }) -> PinsStore {
        PinsStore(deps: .init(api: { hub.client }, report: errors, defaults: defaults))
    }

    private func defaults() -> UserDefaults {
        let name = "PinsStoreTests-\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        return d
    }

    private func settle(_ hub: FakeHub, _ path: String) async {
        for _ in 0..<50 where hub.sent("PUT", path).isEmpty { try? await Task.sleep(for: .milliseconds(10)) }
    }

    @Test func pinningShowsAtOnceSendsAndIsRemembered() async {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/pins", json: #"{"spaces":[],"sessions":["m/s1"]}"#)
        let d = defaults()
        let pins = store(hub, d)
        pins.togglePin(session: "m/s1")
        #expect(pins.isPinned(session: "m/s1"), "before the hub answers")
        await settle(hub, "/api/v1/pins")
        #expect(hub.sent("PUT", "/api/v1/pins").count == 1)
        #expect(store(hub, d).isPinned(session: "m/s1"), "a new launch reads it back")
        pins.togglePin(session: "m/s1")
        #expect(!pins.isPinned(session: "m/s1"))
    }

    @Test func aMutedSpaceMutesItsAgentsUnlessOneIsTurnedBackOn() {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/mutes", json: "{}")
        let pins = store(hub, defaults())
        pins.toggleMute(space: "m/app")
        #expect(pins.isMuted(session: "m/s1", space: "m/app"))
        pins.toggleMute(session: "m/s1", space: "m/app")
        #expect(!pins.isMuted(session: "m/s1", space: "m/app"), "allowed inside the muted space")
        #expect(pins.isMuted(session: "m/s2", space: "m/app"))
    }

    @Test func aFailedSaveIsReported() async {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/pins") { _ in throw APIError(status: 500, message: "nope") }
        var errors = 0
        let pins = store(hub, defaults()) { _ in errors += 1 }
        pins.togglePin(space: "m/app")
        for _ in 0..<50 where errors == 0 { try? await Task.sleep(for: .milliseconds(10)) }
        #expect(errors == 1)
    }

    /// Waits (bounded) until `done`, then lets the store's remaining saves finish.
    private func until(_ done: () -> Bool) async {
        for _ in 0..<100 where !done() { try? await Task.sleep(for: .milliseconds(10)) }
        try? await Task.sleep(for: .milliseconds(20))
    }

    nonisolated private static func sessions(_ req: LocalRequest) -> [String] {
        (req.body.flatMap { try? JSON.decoder.decode(Pins.self, from: $0) })?.sessions ?? []
    }

    @Test func aPinTheHubDidNotSaveGoesBackAndSaysSoEvenOnATimeout() async {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/pins") { _ in throw URLError(.timedOut) }
        let d = defaults()
        var errors: [Error] = []
        let pins = store(hub, d) { errors.append($0) }
        pins.togglePin(session: "m/s1")
        #expect(pins.isPinned(session: "m/s1"), "before the hub answers")
        await until { !errors.isEmpty }
        #expect(!pins.isPinned(session: "m/s1"), "back to what the hub has")
        #expect(!store(hub, d).isPinned(session: "m/s1"), "the cache went back too")
        #expect(errors.count == 1)
        #expect(errors.first.map { !$0.isQuiet } == true, "a timeout here is shown: the row just changed back")
        #expect(errors.first is PinsStore.NotSaved)
    }

    @Test func aMuteTheHubRefusedGoesBackOffline() async {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/mutes", json: "{}")
        let d = defaults()
        var errors: [Error] = []
        let pins = store(hub, d) { errors.append($0) }
        pins.toggleMute(space: "m/app")
        await settle(hub, "/api/v1/mutes")
        try? await Task.sleep(for: .milliseconds(20))
        hub.on("PUT", "/api/v1/mutes") { _ in throw URLError(.notConnectedToInternet) }
        pins.toggleMute(session: "m/s1", space: "m/app")
        #expect(!pins.isMuted(session: "m/s1", space: "m/app"))
        await until { !errors.isEmpty }
        #expect(pins.isMuted(session: "m/s1", space: "m/app"), "the agent is muted with its space again")
        #expect(store(hub, d).isMuted(session: "m/s1", space: "m/app"))
        #expect(errors.count == 1 && errors.first.map { !$0.isOffline && !$0.isQuiet } == true)
    }

    @Test func anOlderFailedChangeDoesNotUndoANewerOneTheHubTook() async {
        let hub = FakeHub()
        // The first change sends only s1 and fails; the second sends s2 and s1, and the hub takes it.
        hub.on("PUT", "/api/v1/pins") { req in
            if Self.sessions(req) == ["m/s1"] { throw APIError(status: 503, message: "busy") }
            return Data("{}".utf8)
        }
        var errors: [Error] = []
        let pins = store(hub, defaults()) { errors.append($0) }
        pins.togglePin(session: "m/s1")
        pins.togglePin(session: "m/s2")
        await until { hub.sent("PUT", "/api/v1/pins").count == 2 }
        #expect(pins.pins.sessions == ["m/s2", "m/s1"], "the newer change has both")
        #expect(errors.isEmpty, "the hub has them all")
    }

    @Test func aNewerFailedChangeGoesBackToTheOlderOneTheHubTook() async {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/pins") { req in
            if Self.sessions(req).count > 1 { throw URLError(.timedOut) }
            return Data("{}".utf8)
        }
        var errors: [Error] = []
        let pins = store(hub, defaults()) { errors.append($0) }
        pins.togglePin(session: "m/s1")
        pins.togglePin(session: "m/s2")
        await until { !errors.isEmpty }
        #expect(pins.pins.sessions == ["m/s1"])
        #expect(errors.count == 1)
    }

    @Test func overlappingChangesThatAllFailGoBackToWhatTheHubHad() async {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/pins") { _ in throw APIError(status: 500, message: "nope") }
        var errors: [Error] = []
        let pins = store(hub, defaults()) { errors.append($0) }
        pins.togglePin(session: "m/s1")
        pins.togglePin(session: "m/s2")
        await until { !errors.isEmpty }
        #expect(pins.pins.sessions.isEmpty, "not to the first change, which the hub did not take either")
        #expect(errors.count == 1, "one message for the lot")
    }

    @Test func aLoadDoesNotOverwriteAChangeStillOnItsWay() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/pins", json: #"{"spaces":[],"sessions":[]}"#)
        hub.on("GET", "/api/v1/mutes", json: "{}")
        hub.on("PUT", "/api/v1/pins", json: "{}")
        let pins = store(hub, defaults())
        pins.togglePin(session: "m/s1")
        await pins.load()
        #expect(pins.isPinned(session: "m/s1"), "the answer may be from before the change")
    }

    @Test func aRenamedSpaceKeepsItsPinAndMute() {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/pins", json: "{}")
        hub.on("PUT", "/api/v1/mutes", json: "{}")
        let pins = store(hub, defaults())
        pins.togglePin(space: "m/old")
        pins.toggleMute(space: "m/old")
        pins.renameSpace("m/old", to: "m/new")
        #expect(pins.isPinned(space: "m/new") && pins.isMuted(space: "m/new"))
        #expect(!pins.isPinned(space: "m/old"))
    }
}
