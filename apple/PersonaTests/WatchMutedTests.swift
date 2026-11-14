import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Muted agents and spaces stay off the watch (#4360): the phone's summary drops their cards, and the cards the watch
/// pulls itself carry the hub's `muted` flag so the watch hides them and never buzzes — the same rule as the Agents
/// badge and the inbox.
@MainActor
struct WatchMutedTests {
    private static let config = ServerConfig(url: URL(string: "http://hub.example.com")!, token: "t")
    /// Unique on purpose: `isQuiet` falls back to the shared `TodoStore`, whose lists other tests fill for "box".
    private let machine = "watchmutedbox"

    private func card(_ id: String, session: String, at index: Int) -> Interaction {
        Interaction(id: id, machineID: machine, sessionID: session, kind: .question,
                    createdAt: Date(timeIntervalSince1970: 1000 + Double(index)))
    }

    private func session(_ id: String, space: String) throws -> Session {
        let o: [String: Any] = ["machine_id": machine, "session_id": id, "title": "agent \(id)", "cwd": "/code/\(id)",
                                "status": "idle", "state": "working", "started_by_persona": false, "assistant": false,
                                "pending_interactions": 0, "space_name": space]
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    /// An AppModel with one default-group link (built like WatchCredentialTests builds one) whose mutes are seeded
    /// for this test's machine. The shared mutes are restored afterwards: the store writes the standard defaults.
    private func model(mutedSessions: [String] = [], mutedSpaces: [String] = [], allowed: [String] = [])
        -> (AppModel, Pins) {
        let model = AppModel()
        model.registry = HubRegistry(hubs: [PairedHub(id: machine, config: Self.config)])
        model.buildLinks()
        let pins = model.pinsStore("")!
        let saved = pins.mutes
        pins.mutes.sessions = mutedSessions.map { machine + "/" + $0 }
        pins.mutes.spaces = mutedSpaces.map { machine + "/" + $0 }
        pins.mutes.allowed = allowed.map { machine + "/" + $0 }
        return (model, saved)
    }

    @Test func watchSummaryLeavesOutAMutedAgentsCard() {
        let (model, saved) = model(mutedSessions: ["s2"])
        defer { model.pinsStore("")?.mutes = saved }
        model.replaceInteractions("", [card("ix1", session: "s1", at: 0), card("ix2", session: "s2", at: 1)])
        #expect(model.watchSummary().interactions.map(\.id) == ["ix1"])
    }

    @Test func watchSummaryLeavesOutAMutedSpacesCardButKeepsAnAgentTurnedBackOn() throws {
        let (model, saved) = model(mutedSpaces: ["team"], allowed: ["s3"])
        defer { model.pinsStore("")?.mutes = saved }
        try [session("s2", space: "team"), session("s3", space: "team")].forEach { model.adopt($0) }
        model.replaceInteractions("", [card("ix1", session: "s1", at: 0), card("ix2", session: "s2", at: 1),
                                      card("ix3", session: "s3", at: 2)])
        #expect(model.watchSummary().interactions.map(\.id) == ["ix1", "ix3"])
    }

    @Test func theWatchsOwnPullHidesMutedCardsAndNeverBuzzesForThem() {
        var muted = card("ix2", session: "s2", at: 1)
        muted.kind = .permission
        muted.muted = true
        let loud = card("ix1", session: "s1", at: 0)
        let shown = WatchInteractions.leavingMuted([loud, muted])
        #expect(shown.map(\.id) == ["ix1"])
        // First pull with nothing shown yet: the muted card alone is no new card to buzz for; a loud one is.
        #expect(WatchInteractions.newOnes(in: WatchInteractions.leavingMuted([muted]), alreadyShown: []).isEmpty)
        #expect(WatchInteractions.newOnes(in: shown, alreadyShown: []).map(\.id) == ["ix1"])
        // Second pull: the same loud card is already shown, so no second buzz.
        #expect(WatchInteractions.newOnes(in: shown, alreadyShown: shown).isEmpty)
    }

    @Test func interactionMutedDecodesFromTheHubAndDefaultsToFalse() throws {
        let oldHub = try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"x","machine_id":"m","session_id":"s","kind":"question"}"#.utf8))
        #expect(!oldHub.muted, "a card from an older hub shows as before")
        let muted = try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"x","machine_id":"m","session_id":"s","kind":"question","muted":true}"#.utf8))
        #expect(muted.muted)
    }
}
